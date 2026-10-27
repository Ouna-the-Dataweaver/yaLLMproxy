package server

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/theio/yallmp-proxy/internal/config"
	"github.com/theio/yallmp-proxy/internal/logdb"
	"github.com/theio/yallmp-proxy/internal/logstore"
	"github.com/theio/yallmp-proxy/internal/observability"
	"github.com/theio/yallmp-proxy/internal/proxy"
	"github.com/theio/yallmp-proxy/internal/quota"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

func TestModelsEndpointListsConfiguredModels(t *testing.T) {
	app := newTestApp(t, "http://upstream.test/v1")
	resp := httptest.NewRecorder()
	app.ServeHTTP(resp, httptest.NewRequest(http.MethodGet, "/v1/models", nil))
	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", resp.Code, resp.Body.String())
	}
	var payload map[string]any
	if err := json.Unmarshal(resp.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload["object"] != "list" {
		t.Fatalf("payload = %#v", payload)
	}
	data := payload["data"].([]any)
	if len(data) != 1 || data[0].(map[string]any)["id"] != "alpha" {
		t.Fatalf("data = %#v", data)
	}
	model := data[0].(map[string]any)
	if model["supports_reasoning"] != false {
		t.Fatalf("supports_reasoning = %#v", model["supports_reasoning"])
	}
	capabilities := model["capabilities"].(map[string]any)
	if capabilities["reasoning"] != false {
		t.Fatalf("capabilities = %#v", capabilities)
	}
	if tags := model["tags"].([]any); len(tags) != 0 {
		t.Fatalf("tags = %#v", tags)
	}
}

func TestModelsEndpointIncludesReasoningDisplayTag(t *testing.T) {
	app, err := New(&config.Config{ModelList: []config.ModelEntry{{
		ModelName: "reasoner",
		ModelParams: map[string]any{
			"api_base":           "http://upstream.test/v1",
			"model":              "openai/reasoner",
			"supports_reasoning": true,
		},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	resp := httptest.NewRecorder()
	app.ServeHTTP(resp, httptest.NewRequest(http.MethodGet, "/v1/models", nil))
	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", resp.Code, resp.Body.String())
	}
	var payload map[string]any
	if err := json.Unmarshal(resp.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	data := payload["data"].([]any)
	if len(data) != 1 {
		t.Fatalf("data = %#v", data)
	}
	model := data[0].(map[string]any)
	if model["supports_reasoning"] != true {
		t.Fatalf("supports_reasoning = %#v", model["supports_reasoning"])
	}
	capabilities := model["capabilities"].(map[string]any)
	if capabilities["reasoning"] != true {
		t.Fatalf("capabilities = %#v", capabilities)
	}
	tags := model["tags"].([]any)
	if len(tags) != 1 || tags[0] != "reasoning" {
		t.Fatalf("tags = %#v", tags)
	}
}

func TestLegacyStaticLogsPageRoutes(t *testing.T) {
	app := newTestApp(t, "http://upstream.test/v1")
	logsResp := httptest.NewRecorder()
	app.ServeHTTP(logsResp, httptest.NewRequest(http.MethodGet, "/logs", nil))
	if logsResp.Code != http.StatusOK {
		t.Fatalf("logs page status = %d body=%s", logsResp.Code, logsResp.Body.String())
	}
	if !strings.Contains(logsResp.Body.String(), "Request Logs") {
		t.Fatalf("logs page body missing title")
	}

	assetResp := httptest.NewRecorder()
	app.ServeHTTP(assetResp, httptest.NewRequest(http.MethodGet, "/static/admin/logs.js", nil))
	if assetResp.Code != http.StatusOK {
		t.Fatalf("asset status = %d body=%s", assetResp.Code, assetResp.Body.String())
	}
	if !strings.Contains(assetResp.Body.String(), "API_ENDPOINT") {
		t.Fatalf("asset body missing expected script content")
	}
}

func TestChatCompletionForwardsToUpstream(t *testing.T) {
	var gotBody string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Errorf("path = %s", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer upstream-key" {
			t.Errorf("auth = %s", r.Header.Get("Authorization"))
		}
		body, _ := io.ReadAll(r.Body)
		gotBody = string(body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"hello"}}]}`))
	}))
	defer upstream.Close()
	app := newTestApp(t, upstream.URL+"/v1")
	resp := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"alpha","messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Content-Type", "application/json")
	app.ServeHTTP(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", resp.Code, resp.Body.String())
	}
	if !strings.Contains(gotBody, `"model":"upstream-model"`) {
		t.Fatalf("body was not rewritten: %s", gotBody)
	}
	logs := app.Logs.List()
	if len(logs) != 1 {
		t.Fatalf("logs = %#v", logs)
	}
	if logs[0].Backend != "alpha" {
		t.Fatalf("logged backend = %q", logs[0].Backend)
	}
	detailResp := httptest.NewRecorder()
	app.ServeHTTP(detailResp, httptest.NewRequest(http.MethodGet, "/admin/logs/"+strconv.FormatInt(logs[0].ID, 10), nil))
	if detailResp.Code != http.StatusOK {
		t.Fatalf("detail status = %d body=%s", detailResp.Code, detailResp.Body.String())
	}
	var detail map[string]any
	if err := json.Unmarshal(detailResp.Body.Bytes(), &detail); err != nil {
		t.Fatal(err)
	}
	attemptTrail := detail["attempt_trail"].([]any)
	attempts := detail["attempts"].([]any)
	if len(attemptTrail) != 1 || len(attempts) != 1 {
		t.Fatalf("memory attempts = %#v alias=%#v", attemptTrail, attempts)
	}
	if attemptTrail[0].(map[string]any)["backend_name"] != "alpha" || attemptTrail[0].(map[string]any)["outcome"] != "final" {
		t.Fatalf("memory attempt = %#v", attemptTrail[0])
	}
}

func TestChatCompletionRecordsOTelSpansWithInMemoryProvider(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"hello"}}]}`))
	}))
	defer upstream.Close()
	app := newTestApp(t, upstream.URL+"/v1")
	provider, recorder := observability.NewInMemoryProvider("test-proxy")
	app.SetObservability(provider)

	resp := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"alpha","messages":[{"role":"user","content":"hi-secret"}]}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer client-secret")
	app.ServeHTTP(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", resp.Code, resp.Body.String())
	}

	spans := recorder.Spans()
	names := map[string]int{}
	for _, span := range spans {
		names[span.Name()]++
	}
	for _, name := range []string{"proxy.request", "proxy.policy", "proxy.admission", "proxy.routing", "proxy.upstream_attempt", "proxy.response"} {
		if names[name] == 0 {
			t.Fatalf("missing span %s in names=%#v", name, names)
		}
	}
	requestSpan := firstSpanByName(spans, "proxy.request")
	if requestSpan == nil {
		t.Fatal("missing request span")
	}
	attrs := spanAttrs(requestSpan)
	if attrs["endpoint_kind"] != "chat" || attrs["model"] != "alpha" || attrs["key_scope"] != "unauthenticated" || attrs["stream"] != "false" || attrs["status_code"] != "200" {
		t.Fatalf("request attrs = %#v", attrs)
	}
	if attrs["gen_ai.system"] != "openai" || attrs["gen_ai.operation.name"] != "chat" || attrs["gen_ai.request.model"] != "alpha" {
		t.Fatalf("request gen_ai attrs = %#v", attrs)
	}
	attemptSpan := firstSpanByName(spans, "proxy.upstream_attempt")
	attemptAttrs := spanAttrs(attemptSpan)
	if attemptAttrs["backend_name"] != "alpha" || attemptAttrs["attempt_index"] != "1" || attemptAttrs["retry_index"] != "0" || attemptAttrs["outcome"] != "final" {
		t.Fatalf("attempt attrs = %#v", attemptAttrs)
	}
	if attemptAttrs["gen_ai.system"] != "openai" || attemptAttrs["gen_ai.operation.name"] != "chat" || attemptAttrs["gen_ai.request.model"] != "alpha" {
		t.Fatalf("attempt gen_ai attrs = %#v", attemptAttrs)
	}
	responseSpan := firstSpanByName(spans, "proxy.response")
	responseAttrs := spanAttrs(responseSpan)
	if responseAttrs["gen_ai.system"] != "openai" || responseAttrs["gen_ai.operation.name"] != "chat" || responseAttrs["gen_ai.request.model"] != "alpha" || responseAttrs["gen_ai.response.model"] != "alpha" {
		t.Fatalf("response gen_ai attrs = %#v", responseAttrs)
	}
	for _, span := range spans {
		for _, secret := range []string{"client-secret", "upstream-key", "hi-secret", upstream.URL, "Authorization"} {
			for _, attr := range span.Attributes() {
				if strings.Contains(attr.Value.Emit(), secret) {
					t.Fatalf("span %s attr %s leaked %q: %s", span.Name(), attr.Key, secret, attr.Value.Emit())
				}
			}
			if strings.Contains(span.Status().Description, secret) {
				t.Fatalf("span %s status leaked %q: %s", span.Name(), secret, span.Status().Description)
			}
		}
	}
	metrics := collectOTelMetrics(t, recorder)
	if got := metricSumValue(metrics, "yallmp.proxy.requests", map[string]string{"endpoint_kind": "chat", "model": "alpha", "outcome": "success", "error_type": "none", "stream": "false"}); got != 1 {
		t.Fatalf("request metric = %d, want 1", got)
	}
	if got := metricHistogramCount(metrics, "yallmp.proxy.request.duration", map[string]string{"endpoint_kind": "chat", "model": "alpha", "outcome": "success"}); got != 1 {
		t.Fatalf("duration metric count = %d, want 1", got)
	}
}

func TestCopyStreamingRecordsTimingMetricsWithoutChangingBytes(t *testing.T) {
	provider, recorder := observability.NewInMemoryProvider("test-proxy")
	chunks := []string{
		"data: {\"choices\":[{\"delta\":{\"content\":\"one\"}}]}\n\n",
		"data: {\"choices\":[{\"delta\":{\"content\":\"two\"}}]}\n\n",
		"data: [DONE]\n\n",
	}
	reader := &chunkStringReader{chunks: chunks}
	resp := httptest.NewRecorder()
	observer := &streamTimingObserver{
		ctx:          context.Background(),
		provider:     provider,
		endpointKind: "chat",
		modelName:    "alpha",
		start:        time.Now().Add(-10 * time.Millisecond),
	}
	written, err := copyStreaming(resp, reader, observer)
	if err != nil {
		t.Fatal(err)
	}
	wantBody := strings.Join(chunks, "")
	if got := resp.Body.String(); got != wantBody {
		t.Fatalf("stream body changed:\n got %q\nwant %q", got, wantBody)
	}
	if written != int64(len(wantBody)) {
		t.Fatalf("written = %d, want %d", written, len(wantBody))
	}
	metrics := collectOTelMetrics(t, recorder)
	attrs := map[string]string{"endpoint_kind": "chat", "model": "alpha", "stream": "true"}
	if got := metricHistogramCount(metrics, "gen_ai.client.operation.time_to_first_token", attrs); got != 1 {
		t.Fatalf("ttft metric count = %d, want 1", got)
	}
	if got := metricHistogramCount(metrics, "gen_ai.client.operation.time_per_output_token", attrs); got != 1 {
		t.Fatalf("tpot metric count = %d, want 1", got)
	}
}

func TestStreamTimingObserverSkipsErrorStatus(t *testing.T) {
	provider, recorder := observability.NewInMemoryProvider("test-proxy")
	app := &App{Observability: provider}
	observer := app.newStreamTimingObserver(context.Background(), "chat", "alpha", time.Now(), http.StatusBadGateway)
	if observer != nil {
		t.Fatal("observer should be nil for error status")
	}
	reader := &chunkStringReader{chunks: []string{"data: error\n\n"}}
	resp := httptest.NewRecorder()
	written, err := copyStreaming(resp, reader, observer)
	if err != nil {
		t.Fatal(err)
	}
	if written != int64(len("data: error\n\n")) || resp.Body.String() != "data: error\n\n" {
		t.Fatalf("stream copy changed: written=%d body=%q", written, resp.Body.String())
	}
	metrics := collectOTelMetrics(t, recorder)
	attrs := map[string]string{"endpoint_kind": "chat", "model": "alpha", "stream": "true"}
	if got := metricHistogramCount(metrics, "gen_ai.client.operation.time_to_first_token", attrs); got != 0 {
		t.Fatalf("ttft metric count = %d, want 0", got)
	}
	if got := metricHistogramCount(metrics, "gen_ai.client.operation.time_per_output_token", attrs); got != 0 {
		t.Fatalf("tpot metric count = %d, want 0", got)
	}
}

func TestMessagesEndpointRecordsOTelPhaseSpansWithInMemoryProvider(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/messages" {
			t.Errorf("path = %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"msg_native","type":"message","role":"assistant","content":[{"type":"text","text":"native"}],"model":"claude-test","stop_reason":"end_turn","stop_sequence":null,"usage":{"input_tokens":3,"output_tokens":4}}`))
	}))
	defer upstream.Close()
	app, err := New(&config.Config{
		ModelList: []config.ModelEntry{{
			ModelName: "claude",
			ModelParams: map[string]any{
				"api_base":          upstream.URL + "/v1",
				"api_key":           "anthropic-key-secret",
				"api_type":          "anthropic",
				"anthropic_version": "2023-06-01",
				"model":             "claude-test",
			},
		}},
		ProxySettings: config.ProxySettings{EnableMessagesEndpoint: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	provider, recorder := observability.NewInMemoryProvider("test-proxy")
	app.SetObservability(provider)

	resp := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"model":"claude","max_tokens":16,"messages":[{"role":"user","content":"prompt-secret"}]}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", "client-header-secret")
	app.ServeHTTP(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", resp.Code, resp.Body.String())
	}

	spans := recorder.Spans()
	requireOTelPhaseSpans(t, spans, "messages", "claude", []string{"proxy.request", "proxy.policy", "proxy.admission", "proxy.routing", "proxy.upstream_attempt", "proxy.response"})
	requestAttrs := requireSpanAttrsByName(t, spans, "proxy.request")
	if requestAttrs["key_scope"] != "unauthenticated" || requestAttrs["status_code"] != "200" || requestAttrs["outcome"] != "success" {
		t.Fatalf("request attrs = %#v", requestAttrs)
	}
	attemptAttrs := requireSpanAttrsByName(t, spans, "proxy.upstream_attempt")
	if attemptAttrs["backend_name"] != "claude" || attemptAttrs["attempt_index"] != "1" || attemptAttrs["retry_index"] != "0" || attemptAttrs["outcome"] != "final" {
		t.Fatalf("attempt attrs = %#v", attemptAttrs)
	}
	responseAttrs := requireSpanAttrsByName(t, spans, "proxy.response")
	if responseAttrs["gen_ai.response.model"] != "claude" || responseAttrs["backend_name"] != "claude" || responseAttrs["status_code"] != "200" {
		t.Fatalf("response attrs = %#v", responseAttrs)
	}
	assertSpansDoNotLeak(t, spans, []string{"client-header-secret", "anthropic-key-secret", "prompt-secret", upstream.URL, "x-api-key"})
	metrics := collectOTelMetrics(t, recorder)
	if got := metricSumValue(metrics, "gen_ai.client.token.usage", map[string]string{"endpoint_kind": "messages", "model": "claude", "token.type": "total"}); got != 7 {
		t.Fatalf("messages total token metric = %d, want 7", got)
	}
}

func TestResponsesEndpointRecordsOTelPhaseSpansWithInMemoryProvider(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/responses" {
			t.Errorf("path = %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"resp_native","object":"response","status":"completed","model":"native","output":[],"output_text":"hello","usage":{"input_tokens":1,"output_tokens":2,"total_tokens":3}}`))
	}))
	defer upstream.Close()
	app, err := New(&config.Config{
		ModelList: []config.ModelEntry{{
			ModelName: "alpha",
			ModelParams: map[string]any{
				"api_base":               upstream.URL + "/v1",
				"api_key":                "responses-upstream-secret",
				"model":                  "native",
				"supports_responses_api": true,
			},
		}},
		ProxySettings: config.ProxySettings{EnableResponsesEndpoint: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	provider, recorder := observability.NewInMemoryProvider("test-proxy")
	app.SetObservability(provider)

	resp := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"alpha","input":"input-secret"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer client-token-secret")
	app.ServeHTTP(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", resp.Code, resp.Body.String())
	}

	spans := recorder.Spans()
	requireOTelPhaseSpans(t, spans, "responses", "alpha", []string{"proxy.request", "proxy.policy", "proxy.admission", "proxy.routing", "proxy.upstream_attempt", "proxy.response"})
	requestAttrs := requireSpanAttrsByName(t, spans, "proxy.request")
	if requestAttrs["key_scope"] != "unauthenticated" || requestAttrs["status_code"] != "200" || requestAttrs["outcome"] != "success" {
		t.Fatalf("request attrs = %#v", requestAttrs)
	}
	responseAttrs := requireSpanAttrsByName(t, spans, "proxy.response")
	if responseAttrs["gen_ai.response.model"] != "alpha" || responseAttrs["backend_name"] != "alpha" || responseAttrs["status_code"] != "200" {
		t.Fatalf("response attrs = %#v", responseAttrs)
	}
	assertSpansDoNotLeak(t, spans, []string{"client-token-secret", "responses-upstream-secret", "input-secret", upstream.URL, "Authorization"})
	metrics := collectOTelMetrics(t, recorder)
	if got := metricSumValue(metrics, "gen_ai.client.token.usage", map[string]string{"endpoint_kind": "responses", "model": "alpha", "token.type": "total"}); got != 3 {
		t.Fatalf("responses total token metric = %d, want 3", got)
	}
}

func TestMessagesAndResponsesStreamCopyErrorMarksOTelSpans(t *testing.T) {
	cases := []struct {
		name         string
		path         string
		body         string
		endpointKind string
		model        string
		streamBody   string
		config       func(string) *config.Config
	}{
		{
			name:         "messages",
			path:         "/v1/messages",
			body:         `{"model":"claude","max_tokens":16,"stream":true,"messages":[{"role":"user","content":"stream-copy-secret"}]}`,
			endpointKind: "messages",
			model:        "claude",
			streamBody:   "event: ping\ndata: {}\n\n",
			config: func(upstreamURL string) *config.Config {
				return &config.Config{
					ModelList: []config.ModelEntry{{
						ModelName: "claude",
						ModelParams: map[string]any{
							"api_base": upstreamURL + "/v1",
							"api_type": "anthropic",
							"model":    "claude-test",
						},
					}},
					ProxySettings: config.ProxySettings{EnableMessagesEndpoint: true},
				}
			},
		},
		{
			name:         "responses",
			path:         "/v1/responses",
			body:         `{"model":"alpha","input":"stream-copy-secret","stream":true}`,
			endpointKind: "responses",
			model:        "alpha",
			streamBody:   "data: {\"type\":\"response.completed\",\"response\":{\"usage\":{\"total_tokens\":1}}}\n\n",
			config: func(upstreamURL string) *config.Config {
				return &config.Config{
					ModelList: []config.ModelEntry{{
						ModelName: "alpha",
						ModelParams: map[string]any{
							"api_base":               upstreamURL + "/v1",
							"model":                  "native",
							"supports_responses_api": true,
						},
					}},
					ProxySettings: config.ProxySettings{EnableResponsesEndpoint: true},
				}
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = w.Write([]byte(tc.streamBody))
			}))
			defer upstream.Close()
			app, err := New(tc.config(upstream.URL))
			if err != nil {
				t.Fatal(err)
			}
			provider, recorder := observability.NewInMemoryProvider("test-proxy")
			app.SetObservability(provider)

			writer := &failingResponseWriter{err: errors.New("client closed stream-copy-secret")}
			req := httptest.NewRequest(http.MethodPost, tc.path, strings.NewReader(tc.body))
			req.Header.Set("Content-Type", "application/json")
			app.ServeHTTP(writer, req)
			if writer.status != http.StatusOK {
				t.Fatalf("status = %d", writer.status)
			}

			spans := recorder.Spans()
			requireOTelPhaseSpans(t, spans, tc.endpointKind, tc.model, []string{"proxy.request", "proxy.policy", "proxy.admission", "proxy.routing", "proxy.upstream_attempt", "proxy.response"})
			for _, name := range []string{"proxy.request", "proxy.response"} {
				attrs := requireSpanAttrsByName(t, spans, name)
				if attrs["outcome"] != "error" || attrs["error_type"] != "context_canceled" || attrs["status_code"] != "200" {
					t.Fatalf("%s attrs = %#v", name, attrs)
				}
			}
			assertSpansDoNotLeak(t, spans, []string{"stream-copy-secret", upstream.URL})
		})
	}
}

func TestTranslatedMessagesAndResponsesOTelRoutingUsesClientEndpointKind(t *testing.T) {
	cases := []struct {
		name         string
		path         string
		body         string
		endpointKind string
		settings     config.ProxySettings
		upstreamBody string
	}{
		{
			name:         "messages",
			path:         "/v1/messages",
			body:         `{"model":"alpha","max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`,
			endpointKind: "messages",
			settings:     config.ProxySettings{EnableMessagesEndpoint: true},
			upstreamBody: `{"id":"chatcmpl-abc123","model":"openai/upstream","choices":[{"finish_reason":"stop","message":{"content":"hello"}}],"usage":{"prompt_tokens":5,"completion_tokens":2}}`,
		},
		{
			name:         "responses",
			path:         "/v1/responses",
			body:         `{"model":"alpha","input":"hi"}`,
			endpointKind: "responses",
			settings:     config.ProxySettings{EnableResponsesEndpoint: true},
			upstreamBody: `{"id":"chatcmpl-abc123","model":"openai/upstream","choices":[{"finish_reason":"stop","message":{"content":"hello"}}],"usage":{"prompt_tokens":5,"completion_tokens":2,"total_tokens":7}}`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var gotPath string
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotPath = r.URL.Path
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(tc.upstreamBody))
			}))
			defer upstream.Close()
			app, err := New(&config.Config{
				ModelList: []config.ModelEntry{{
					ModelName: "alpha",
					ModelParams: map[string]any{
						"api_base": upstream.URL + "/v1",
						"model":    "openai/upstream",
					},
				}},
				ProxySettings: tc.settings,
			})
			if err != nil {
				t.Fatal(err)
			}
			provider, recorder := observability.NewInMemoryProvider("test-proxy")
			app.SetObservability(provider)

			resp := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, tc.path, strings.NewReader(tc.body))
			req.Header.Set("Content-Type", "application/json")
			app.ServeHTTP(resp, req)
			if resp.Code != http.StatusOK {
				t.Fatalf("status = %d body=%s", resp.Code, resp.Body.String())
			}
			if gotPath != "/v1/chat/completions" {
				t.Fatalf("upstream path = %s", gotPath)
			}
			for _, name := range []string{"proxy.routing", "proxy.upstream_attempt"} {
				attrs := requireSpanAttrsByName(t, recorder.Spans(), name)
				if attrs["endpoint_kind"] != tc.endpointKind || attrs["gen_ai.operation.name"] != tc.endpointKind {
					t.Fatalf("%s attrs = %#v", name, attrs)
				}
			}
		})
	}
}

func TestPassthroughRecordsOTelPhaseSpansWithInMemoryProvider(t *testing.T) {
	var gotBody string
	var gotPath string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.String()
		body, _ := io.ReadAll(r.Body)
		gotBody = string(body)
		w.Header().Set("X-Passthrough-Test", "yes")
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte("accepted"))
	}))
	defer upstream.Close()
	app, err := New(&config.Config{ModelList: []config.ModelEntry{{
		ModelName: "pass",
		ModelParams: map[string]any{
			"api_base": upstream.URL,
			"api_key":  "passthrough-upstream-secret",
			"api_type": "passthrough",
		},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	provider, recorder := observability.NewInMemoryProvider("test-proxy")
	app.SetObservability(provider)

	resp := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/p1/pass/v2/score?secret=client-query-secret", strings.NewReader(`{"value":"body-secret"}`))
	req.Header.Set("Authorization", "Bearer passthrough-client-secret")
	app.ServeHTTP(resp, req)
	if resp.Code != http.StatusAccepted || resp.Body.String() != "accepted" || resp.Header().Get("X-Passthrough-Test") != "yes" {
		t.Fatalf("passthrough response status=%d header=%q body=%s", resp.Code, resp.Header().Get("X-Passthrough-Test"), resp.Body.String())
	}
	if gotPath != "/v2/score?secret=client-query-secret" || gotBody != `{"value":"body-secret"}` {
		t.Fatalf("upstream path/body = %s %s", gotPath, gotBody)
	}

	spans := recorder.Spans()
	requireOTelPhaseSpans(t, spans, "passthrough", "pass", []string{"proxy.request", "proxy.upstream_attempt", "proxy.response"})
	for _, name := range []string{"proxy.policy", "proxy.admission", "proxy.routing"} {
		if countSpansByName(spans, name) != 0 {
			t.Fatalf("passthrough emitted %s spans: %#v", name, spanNames(spans))
		}
	}
	requestAttrs := requireSpanAttrsByName(t, spans, "proxy.request")
	if requestAttrs["key_scope"] != "unknown" || requestAttrs["status_code"] != "202" || requestAttrs["outcome"] != "success" {
		t.Fatalf("request attrs = %#v", requestAttrs)
	}
	attemptAttrs := requireSpanAttrsByName(t, spans, "proxy.upstream_attempt")
	if attemptAttrs["backend_name"] != "pass" || attemptAttrs["attempt_index"] != "1" || attemptAttrs["retry_index"] != "0" || attemptAttrs["status_code"] != "202" {
		t.Fatalf("attempt attrs = %#v", attemptAttrs)
	}
	assertSpansDoNotLeak(t, spans, []string{"passthrough-client-secret", "passthrough-upstream-secret", "body-secret", "client-query-secret", upstream.URL, "Authorization"})
}

func TestOTelDefaultOffPreservesMessagesResponsesAndPassthroughBehavior(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/messages":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"msg_native","type":"message","role":"assistant","content":[{"type":"text","text":"native"}],"model":"claude-test","stop_reason":"end_turn","stop_sequence":null,"usage":{"input_tokens":3,"output_tokens":4}}`))
		case "/v1/responses":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"resp_native","object":"response","status":"completed","model":"native","output":[],"output_text":"hello","usage":{"input_tokens":1,"output_tokens":2,"total_tokens":3}}`))
		case "/v2/score":
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte("accepted"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()
	app, err := New(&config.Config{
		ModelList: []config.ModelEntry{
			{
				ModelName: "claude",
				ModelParams: map[string]any{
					"api_base": upstream.URL + "/v1",
					"api_type": "anthropic",
					"model":    "claude-test",
				},
			},
			{
				ModelName: "alpha",
				ModelParams: map[string]any{
					"api_base":               upstream.URL + "/v1",
					"model":                  "native",
					"supports_responses_api": true,
				},
			},
			{
				ModelName: "pass",
				ModelParams: map[string]any{
					"api_base": upstream.URL,
					"api_type": "passthrough",
				},
			},
		},
		ProxySettings: config.ProxySettings{
			EnableMessagesEndpoint:  true,
			EnableResponsesEndpoint: true,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if app.otelEnabled() || app.Router.Observability.Enabled() {
		t.Fatal("observability enabled by default")
	}

	messagesResp := httptest.NewRecorder()
	messagesReq := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"model":"claude","max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`))
	messagesReq.Header.Set("Content-Type", "application/json")
	app.ServeHTTP(messagesResp, messagesReq)
	if messagesResp.Code != http.StatusOK || !strings.Contains(messagesResp.Body.String(), `"msg_native"`) {
		t.Fatalf("messages response status=%d body=%s", messagesResp.Code, messagesResp.Body.String())
	}

	responsesResp := httptest.NewRecorder()
	responsesReq := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"alpha","input":"hi"}`))
	responsesReq.Header.Set("Content-Type", "application/json")
	app.ServeHTTP(responsesResp, responsesReq)
	if responsesResp.Code != http.StatusOK || !strings.Contains(responsesResp.Body.String(), `"resp_native"`) {
		t.Fatalf("responses response status=%d body=%s", responsesResp.Code, responsesResp.Body.String())
	}

	passthroughResp := httptest.NewRecorder()
	app.ServeHTTP(passthroughResp, httptest.NewRequest(http.MethodPost, "/p1/pass/v2/score", strings.NewReader(`{}`)))
	if passthroughResp.Code != http.StatusAccepted || passthroughResp.Body.String() != "accepted" {
		t.Fatalf("passthrough response status=%d body=%s", passthroughResp.Code, passthroughResp.Body.String())
	}
}

func TestMessagesAndResponsesOTelPolicyRejectionStopsBeforeAdmission(t *testing.T) {
	enabled := true
	cases := []struct {
		name string
		path string
		body string
	}{
		{
			name: "messages",
			path: "/v1/messages",
			body: `{"model":"alpha","max_tokens":16,"messages":[{"role":"user","content":"policy-secret"}]}`,
		},
		{
			name: "responses",
			path: "/v1/responses",
			body: `{"model":"alpha","input":"policy-secret"}`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			app, err := New(&config.Config{
				ModelList: []config.ModelEntry{{
					ModelName: "alpha",
					ModelParams: map[string]any{
						"api_base": "http://upstream.test/v1",
						"model":    "native",
					},
				}},
				ProxySettings: config.ProxySettings{
					EnableMessagesEndpoint:  true,
					EnableResponsesEndpoint: true,
				},
				AppKeys: config.AppKeysConfig{
					Enabled: true,
					RequiredHeaders: []config.RequiredHeader{{
						Name: "X-Tenant-ID",
					}},
					Keys: []config.AppKeyEntry{{
						KeyID: "k1", Secret: "policy-client-secret", Enabled: &enabled, Models: []string{"alpha"},
					}},
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			provider, recorder := observability.NewInMemoryProvider("test-proxy")
			app.SetObservability(provider)

			resp := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, tc.path, strings.NewReader(tc.body))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Authorization", "Bearer policy-client-secret")
			app.ServeHTTP(resp, req)
			if resp.Code != http.StatusBadRequest {
				t.Fatalf("status = %d body=%s", resp.Code, resp.Body.String())
			}

			spans := recorder.Spans()
			requireOTelPhaseSpans(t, spans, tc.name, "alpha", []string{"proxy.request", "proxy.policy"})
			for _, name := range []string{"proxy.admission", "proxy.routing", "proxy.upstream_attempt", "proxy.response"} {
				if countSpansByName(spans, name) != 0 {
					t.Fatalf("policy rejection emitted %s spans: %#v", name, spanNames(spans))
				}
			}
			policyAttrs := requireSpanAttrsByName(t, spans, "proxy.policy")
			if policyAttrs["outcome"] != "error" || policyAttrs["status_code"] != "400" || policyAttrs["error_type"] != "http_4xx" {
				t.Fatalf("policy attrs = %#v", policyAttrs)
			}
			assertSpansDoNotLeak(t, spans, []string{"policy-client-secret", "policy-secret", "X-Tenant-ID"})
		})
	}
}

func TestMessagesEndpointOTelAdmissionRejectionStopsBeforeRouting(t *testing.T) {
	var upstreamHits int
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamHits++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"msg_native","type":"message","role":"assistant","content":[{"type":"text","text":"native"}],"model":"claude-test","stop_reason":"end_turn","stop_sequence":null,"usage":{"input_tokens":3,"output_tokens":4}}`))
	}))
	defer upstream.Close()
	enabled := true
	app, err := New(&config.Config{
		ModelList: []config.ModelEntry{{
			ModelName: "claude",
			ModelParams: map[string]any{
				"api_base": upstream.URL + "/v1",
				"api_type": "anthropic",
				"model":    "claude-test",
			},
		}},
		ProxySettings: config.ProxySettings{EnableMessagesEndpoint: true},
		AppKeys: config.AppKeysConfig{
			Enabled: true,
			Keys: []config.AppKeyEntry{{
				KeyID: "k1", Secret: "admission-client-secret", Enabled: &enabled, Models: []string{"claude"},
				Limits: config.LimitsConfig{RPS: 1},
			}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	provider, recorder := observability.NewInMemoryProvider("test-proxy")
	app.SetObservability(provider)

	first := httptest.NewRecorder()
	firstReq := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"model":"claude","max_tokens":16,"messages":[{"role":"user","content":"first"}]}`))
	firstReq.Header.Set("Content-Type", "application/json")
	firstReq.Header.Set("Authorization", "Bearer admission-client-secret")
	app.ServeHTTP(first, firstReq)
	if first.Code != http.StatusOK {
		t.Fatalf("first status = %d body=%s", first.Code, first.Body.String())
	}
	spanCountBeforeRejection := len(recorder.Spans())

	second := httptest.NewRecorder()
	secondReq := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"model":"claude","max_tokens":16,"messages":[{"role":"user","content":"admission-secret"}]}`))
	secondReq.Header.Set("Content-Type", "application/json")
	secondReq.Header.Set("Authorization", "Bearer admission-client-secret")
	app.ServeHTTP(second, secondReq)
	if second.Code != http.StatusTooManyRequests {
		t.Fatalf("second status = %d body=%s", second.Code, second.Body.String())
	}
	if upstreamHits != 1 {
		t.Fatalf("upstream hits = %d, want 1", upstreamHits)
	}

	spans := recorder.Spans()[spanCountBeforeRejection:]
	requireOTelPhaseSpans(t, spans, "messages", "claude", []string{"proxy.request", "proxy.policy", "proxy.admission"})
	for _, name := range []string{"proxy.routing", "proxy.upstream_attempt", "proxy.response"} {
		if countSpansByName(spans, name) != 0 {
			t.Fatalf("admission rejection emitted %s spans: %#v", name, spanNames(spans))
		}
	}
	admissionAttrs := requireSpanAttrsByName(t, spans, "proxy.admission")
	if admissionAttrs["outcome"] != "error" || admissionAttrs["status_code"] != "429" || admissionAttrs["error_type"] != "rate_limit" {
		t.Fatalf("admission attrs = %#v", admissionAttrs)
	}
	requestAttrs := requireSpanAttrsByName(t, spans, "proxy.request")
	if requestAttrs["key_scope"] != "authenticated" || requestAttrs["status_code"] != "429" {
		t.Fatalf("request attrs = %#v", requestAttrs)
	}
	assertSpansDoNotLeak(t, spans, []string{"admission-client-secret", "admission-secret", upstream.URL})
}

func TestOTelUnknownModelDoesNotExposeRawModel(t *testing.T) {
	app := newTestApp(t, "http://upstream.test/v1")
	provider, recorder := observability.NewInMemoryProvider("test-proxy")
	app.SetObservability(provider)

	rawModel := "client-secret-model"
	resp := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"`+rawModel+`","messages":[{"role":"user","content":"prompt-secret"}]}`))
	req.Header.Set("Content-Type", "application/json")
	app.ServeHTTP(resp, req)
	if resp.Code == http.StatusOK {
		t.Fatalf("status = %d, want failure for unknown model", resp.Code)
	}

	spans := recorder.Spans()
	if len(spans) == 0 {
		t.Fatal("expected spans for failed request")
	}
	for _, span := range spans {
		attrs := spanAttrs(span)
		for _, key := range []string{"model", "gen_ai.request.model"} {
			if model, ok := attrs[key]; ok && model != "unknown" {
				t.Fatalf("span %s %s attr = %q, want unknown", span.Name(), key, model)
			}
		}
		for _, secret := range []string{rawModel, "prompt-secret"} {
			if strings.Contains(span.Status().Description, secret) {
				t.Fatalf("span %s status leaked %q: %s", span.Name(), secret, span.Status().Description)
			}
			for _, attr := range span.Attributes() {
				if strings.Contains(attr.Value.Emit(), secret) {
					t.Fatalf("span %s attr %s leaked %q: %s", span.Name(), attr.Key, secret, attr.Value.Emit())
				}
			}
		}
	}
}

func TestServerOTelDisabledByDefault(t *testing.T) {
	app := newTestApp(t, "http://upstream.test/v1")
	if app.Observability == nil {
		t.Fatal("missing observability provider")
	}
	if app.Observability.Enabled() {
		t.Fatal("observability enabled by default")
	}
	if app.Router == nil || app.Router.Observability == nil {
		t.Fatal("router missing observability provider")
	}
	if app.Router.Observability.Enabled() {
		t.Fatal("router observability enabled by default")
	}
	if err := app.ShutdownObservability(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestChatCompletionForwardsFinalRetryableUpstreamStatus(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"message":"provider rate limited"}}`))
	}))
	defer upstream.Close()

	app := newTestApp(t, upstream.URL+"/v1")
	resp := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"alpha","messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Content-Type", "application/json")
	app.ServeHTTP(resp, req)
	if resp.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d body=%s", resp.Code, resp.Body.String())
	}
	if !strings.Contains(resp.Body.String(), "provider rate limited") {
		t.Fatalf("upstream body was not preserved: %s", resp.Body.String())
	}
	logs := app.Logs.List()
	if len(logs) != 1 || logs[0].Status != http.StatusTooManyRequests || logs[0].Backend != "alpha" {
		t.Fatalf("logs = %#v", logs)
	}
}

func TestMessagesEndpointTranslatesNonStreamingOpenAIBackend(t *testing.T) {
	var gotPath string
	var gotPayload map[string]any
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		if err := json.NewDecoder(r.Body).Decode(&gotPayload); err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl-abc123","model":"openai/upstream","choices":[{"finish_reason":"stop","message":{"reasoning_content":"hidden","content":"hello"}}],"usage":{"prompt_tokens":5,"completion_tokens":2}}`))
	}))
	defer upstream.Close()
	app, err := New(&config.Config{
		ModelList: []config.ModelEntry{{
			ModelName: "alpha",
			ModelParams: map[string]any{
				"api_base": upstream.URL + "/v1",
				"api_key":  "upstream-key",
				"model":    "openai/upstream",
			},
		}},
		ProxySettings: config.ProxySettings{EnableMessagesEndpoint: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	resp := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"model":"alpha","max_tokens":16,"system":"sys","messages":[{"role":"user","content":"hi"}],"stop_sequences":["END"],"metadata":{"user_id":"u1"}}`))
	req.Header.Set("Content-Type", "application/json")
	app.ServeHTTP(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", resp.Code, resp.Body.String())
	}
	if gotPath != "/v1/chat/completions" {
		t.Fatalf("upstream path = %s", gotPath)
	}
	if gotPayload["model"] != "upstream" || gotPayload["max_tokens"] != float64(16) {
		t.Fatalf("upstream payload = %#v", gotPayload)
	}
	messages := gotPayload["messages"].([]any)
	if messages[0].(map[string]any)["role"] != "system" || messages[0].(map[string]any)["content"] != "sys" {
		t.Fatalf("translated messages = %#v", messages)
	}
	if gotPayload["stop"].([]any)[0] != "END" || gotPayload["user"] != "u1" {
		t.Fatalf("translated params = %#v", gotPayload)
	}
	var anthropic map[string]any
	if err := json.Unmarshal(resp.Body.Bytes(), &anthropic); err != nil {
		t.Fatal(err)
	}
	if anthropic["type"] != "message" || anthropic["role"] != "assistant" || anthropic["stop_reason"] != "end_turn" {
		t.Fatalf("anthropic response = %#v", anthropic)
	}
	content := anthropic["content"].([]any)
	if content[0].(map[string]any)["type"] != "thinking" || content[0].(map[string]any)["thinking"] != "hidden" {
		t.Fatalf("thinking content = %#v", content)
	}
	if content[1].(map[string]any)["text"] != "hello" {
		t.Fatalf("content = %#v", content)
	}
	usage := anthropic["usage"].(map[string]any)
	if usage["input_tokens"] != float64(5) || usage["output_tokens"] != float64(2) {
		t.Fatalf("usage = %#v", usage)
	}
}

func TestMessagesEndpointPassesThroughAnthropicBackend(t *testing.T) {
	var gotPath string
	var gotAPIKey string
	var gotVersion string
	var gotPayload map[string]any
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAPIKey = r.Header.Get("x-api-key")
		gotVersion = r.Header.Get("anthropic-version")
		if err := json.NewDecoder(r.Body).Decode(&gotPayload); err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"msg_native","type":"message","role":"assistant","content":[{"type":"text","text":"native"}],"model":"claude-test","stop_reason":"end_turn","stop_sequence":null,"usage":{"input_tokens":3,"output_tokens":4}}`))
	}))
	defer upstream.Close()
	app, err := New(&config.Config{
		ModelList: []config.ModelEntry{{
			ModelName: "claude",
			ModelParams: map[string]any{
				"api_base":          upstream.URL + "/v1",
				"api_key":           "anthropic-key",
				"api_type":          "anthropic",
				"anthropic_version": "2023-06-01",
				"model":             "claude-test",
			},
		}},
		ProxySettings: config.ProxySettings{EnableMessagesEndpoint: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	resp := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"model":"claude","max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", "client-key")
	app.ServeHTTP(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", resp.Code, resp.Body.String())
	}
	if gotPath != "/v1/messages" || gotAPIKey != "anthropic-key" || gotVersion != "2023-06-01" {
		t.Fatalf("upstream path/key/version = %s/%s/%s", gotPath, gotAPIKey, gotVersion)
	}
	if gotPayload["model"] != "claude-test" {
		t.Fatalf("upstream payload = %#v", gotPayload)
	}
	var anthropic map[string]any
	if err := json.Unmarshal(resp.Body.Bytes(), &anthropic); err != nil {
		t.Fatal(err)
	}
	if anthropic["id"] != "msg_native" || anthropic["type"] != "message" {
		t.Fatalf("anthropic response = %#v", anthropic)
	}
}

func TestMessagesEndpointTranslatesStreamingOpenAIBackend(t *testing.T) {
	var gotPath string
	var gotPayload map[string]any
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		if err := json.NewDecoder(r.Body).Decode(&gotPayload); err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(`data: {"choices":[{"delta":{"role":"assistant"},"index":0}]}` + "\n\n"))
		_, _ = w.Write([]byte(`data: {"choices":[{"delta":{"reasoning_content":"hidden"},"index":0}]}` + "\n\n"))
		_, _ = w.Write([]byte(`data: {"choices":[{"delta":{"content":"hello"},"index":0}]}` + "\n\n"))
		_, _ = w.Write([]byte(`data: {"choices":[{"delta":{},"finish_reason":"stop","index":0}],"usage":{"prompt_tokens":5,"completion_tokens":2,"total_tokens":7}}` + "\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer upstream.Close()
	app, err := New(&config.Config{
		ModelList: []config.ModelEntry{{
			ModelName: "alpha",
			ModelParams: map[string]any{
				"api_base": upstream.URL + "/v1",
				"api_key":  "upstream-key",
				"model":    "openai/upstream",
			},
		}},
		ProxySettings: config.ProxySettings{EnableMessagesEndpoint: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	resp := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"model":"alpha","max_tokens":16,"stream":true,"messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Content-Type", "application/json")
	app.ServeHTTP(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", resp.Code, resp.Body.String())
	}
	if gotPath != "/v1/chat/completions" || gotPayload["stream"] != true {
		t.Fatalf("upstream path/payload = %s %#v", gotPath, gotPayload)
	}
	body := resp.Body.String()
	for _, want := range []string{"event: message_start", `"type":"thinking_delta"`, `"thinking":"hidden"`, "event: content_block_delta", `"text":"hello"`, "event: message_delta", `"stop_reason":"end_turn"`, "event: message_stop"} {
		if !strings.Contains(body, want) {
			t.Fatalf("stream body missing %q: %s", want, body)
		}
	}
}

func TestMessagesEndpointPassesThroughAnthropicStreamingBackend(t *testing.T) {
	var gotPath string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: message_start\n"))
		_, _ = w.Write([]byte(`data: {"type":"message_start","message":{"id":"msg_native","type":"message","role":"assistant","content":[],"model":"claude","usage":{"input_tokens":3,"output_tokens":0}}}` + "\n\n"))
		_, _ = w.Write([]byte("event: message_delta\n"))
		_, _ = w.Write([]byte(`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":4}}` + "\n\n"))
		_, _ = w.Write([]byte("event: message_stop\n"))
		_, _ = w.Write([]byte(`data: {"type":"message_stop"}` + "\n\n"))
	}))
	defer upstream.Close()
	app, err := New(&config.Config{
		ModelList: []config.ModelEntry{{
			ModelName: "claude",
			ModelParams: map[string]any{
				"api_base":          upstream.URL + "/v1",
				"api_key":           "anthropic-key",
				"api_type":          "anthropic",
				"anthropic_version": "2023-06-01",
				"model":             "claude-test",
			},
		}},
		ProxySettings: config.ProxySettings{EnableMessagesEndpoint: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	provider, recorder := observability.NewInMemoryProvider("test-proxy")
	app.SetObservability(provider)
	resp := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"model":"claude","max_tokens":16,"stream":true,"messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Content-Type", "application/json")
	app.ServeHTTP(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", resp.Code, resp.Body.String())
	}
	if gotPath != "/v1/messages" {
		t.Fatalf("upstream path = %s", gotPath)
	}
	if body := resp.Body.String(); !strings.Contains(body, "event: message_start") || !strings.Contains(body, "msg_native") {
		t.Fatalf("stream body = %s", body)
	}
	metrics := collectOTelMetrics(t, recorder)
	if got := metricSumValue(metrics, "gen_ai.client.token.usage", map[string]string{"endpoint_kind": "messages", "model": "claude", "stream": "true", "token.type": "total"}); got != 7 {
		t.Fatalf("stream total token metric = %d, want 7", got)
	}
}

func TestAnthropicStreamUsageReadCloserObservesNestedMessageUsage(t *testing.T) {
	stream := "event: message_start\n" +
		`data: {"type":"message_start","message":{"id":"msg_native","type":"message","role":"assistant","content":[],"model":"claude","usage":{"input_tokens":3,"output_tokens":0}}}` + "\n\n" +
		"event: message_delta\n" +
		`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":4}}` + "\n\n" +
		"event: message_stop\n" +
		`data: {"type":"message_stop"}` + "\n\n"
	reader := &anthropicStreamUsageReadCloser{ReadCloser: io.NopCloser(strings.NewReader(stream))}
	got, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != stream {
		t.Fatalf("stream was not preserved:\n%s", string(got))
	}
	if reader.TotalTokens() != 7 {
		t.Fatalf("total tokens = %d", reader.TotalTokens())
	}
	usage := reader.UsageStats()
	if usage["input_tokens"] != 3 || usage["output_tokens"] != 4 || usage["total_tokens"] != 7 {
		t.Fatalf("usage = %#v", usage)
	}
}

func TestResponsesEndpointTranslatesNonStreamingOpenAIBackend(t *testing.T) {
	var gotPath string
	var gotPayload map[string]any
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		if err := json.NewDecoder(r.Body).Decode(&gotPayload); err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl-resp","model":"openai/upstream","choices":[{"finish_reason":"stop","message":{"reasoning_content":"hidden","content":"hello"}}],"usage":{"prompt_tokens":5,"completion_tokens":2,"total_tokens":7,"prompt_tokens_details":{"cached_tokens":4},"completion_tokens_details":{"reasoning_tokens":1}}}`))
	}))
	defer upstream.Close()
	app, err := New(&config.Config{
		ModelList: []config.ModelEntry{{
			ModelName: "alpha",
			ModelParams: map[string]any{
				"api_base": upstream.URL + "/v1",
				"api_key":  "upstream-key",
				"model":    "openai/upstream",
			},
		}},
		ProxySettings: config.ProxySettings{EnableResponsesEndpoint: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	resp := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"alpha","instructions":"sys","input":"hi","max_output_tokens":16}`))
	req.Header.Set("Content-Type", "application/json")
	app.ServeHTTP(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", resp.Code, resp.Body.String())
	}
	if gotPath != "/v1/chat/completions" {
		t.Fatalf("upstream path = %s", gotPath)
	}
	messages := gotPayload["messages"].([]any)
	if messages[0].(map[string]any)["role"] != "system" || messages[1].(map[string]any)["content"] != "hi" || gotPayload["max_tokens"] != float64(16) {
		t.Fatalf("upstream payload = %#v", gotPayload)
	}
	var responsesPayload map[string]any
	if err := json.Unmarshal(resp.Body.Bytes(), &responsesPayload); err != nil {
		t.Fatal(err)
	}
	if responsesPayload["object"] != "response" || responsesPayload["status"] != "completed" || responsesPayload["output_text"] != "hello" {
		t.Fatalf("responses payload = %#v", responsesPayload)
	}
	output := responsesPayload["output"].([]any)
	if output[0].(map[string]any)["type"] != "reasoning" || output[1].(map[string]any)["type"] != "message" {
		t.Fatalf("output = %#v", output)
	}
	summary := output[0].(map[string]any)["summary"].([]any)
	if summary[0].(map[string]any)["text"] != "hidden" {
		t.Fatalf("summary = %#v", summary)
	}
	usage := responsesPayload["usage"].(map[string]any)
	if usage["input_tokens"] != float64(5) || usage["output_tokens"] != float64(2) {
		t.Fatalf("usage = %#v", usage)
	}
	inputDetails := usage["input_tokens_details"].(map[string]any)
	if inputDetails["cached_tokens"] != float64(4) {
		t.Fatalf("input token details = %#v", inputDetails)
	}
	outputDetails := usage["output_tokens_details"].(map[string]any)
	if outputDetails["reasoning_tokens"] != float64(1) {
		t.Fatalf("output token details = %#v", outputDetails)
	}
}

func TestResponsesEndpointStoresAndReplaysPreviousResponse(t *testing.T) {
	var hit int
	var secondPayload map[string]any
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hit++
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		if hit == 2 {
			secondPayload = payload
		}
		w.Header().Set("Content-Type", "application/json")
		if hit == 1 {
			_, _ = w.Write([]byte(`{"id":"chatcmpl-1","model":"openai/upstream","choices":[{"finish_reason":"stop","message":{"content":"first answer"}}],"usage":{"prompt_tokens":4,"completion_tokens":3,"total_tokens":7}}`))
			return
		}
		_, _ = w.Write([]byte(`{"id":"chatcmpl-2","model":"openai/upstream","choices":[{"finish_reason":"stop","message":{"content":"second answer"}}],"usage":{"prompt_tokens":8,"completion_tokens":3,"total_tokens":11}}`))
	}))
	defer upstream.Close()
	app, err := New(&config.Config{
		ModelList: []config.ModelEntry{{
			ModelName: "alpha",
			ModelParams: map[string]any{
				"api_base": upstream.URL + "/v1",
				"model":    "openai/upstream",
			},
		}},
		ProxySettings: config.ProxySettings{EnableResponsesEndpoint: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	first := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"alpha","input":"first question","store":true}`))
	req.Header.Set("Content-Type", "application/json")
	app.ServeHTTP(first, req)
	if first.Code != http.StatusOK {
		t.Fatalf("first status = %d body=%s", first.Code, first.Body.String())
	}
	var firstPayload map[string]any
	if err := json.Unmarshal(first.Body.Bytes(), &firstPayload); err != nil {
		t.Fatal(err)
	}
	responseID := firstPayload["id"].(string)
	second := httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"alpha","input":"second question","previous_response_id":"`+responseID+`"}`))
	req.Header.Set("Content-Type", "application/json")
	app.ServeHTTP(second, req)
	if second.Code != http.StatusOK {
		t.Fatalf("second status = %d body=%s", second.Code, second.Body.String())
	}
	messages := secondPayload["messages"].([]any)
	if len(messages) != 3 {
		t.Fatalf("messages = %#v", messages)
	}
	if messages[0].(map[string]any)["content"] != "first question" || messages[1].(map[string]any)["content"] != "first answer" || messages[2].(map[string]any)["content"] != "second question" {
		t.Fatalf("messages = %#v", messages)
	}
}

func TestResponsesEndpointStickyFollowUpUsesSameNode(t *testing.T) {
	var n1Hits int
	var n2Hits int
	node1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n1Hits++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl-n1","model":"node-one","choices":[{"finish_reason":"stop","message":{"content":"node one"}}],"usage":{"prompt_tokens":4,"completion_tokens":3,"total_tokens":7}}`))
	}))
	defer node1.Close()
	node2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n2Hits++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl-n2","model":"node-two","choices":[{"finish_reason":"stop","message":{"content":"node two"}}],"usage":{"prompt_tokens":4,"completion_tokens":3,"total_tokens":7}}`))
	}))
	defer node2.Close()
	app, err := New(&config.Config{
		Nodes: []config.NodeEntry{
			{Name: "node-one", APIBase: node1.URL + "/v1", Model: "openai/node-one"},
			{Name: "node-two", APIBase: node2.URL + "/v1", Model: "openai/node-two"},
		},
		Models:        []config.ClientModelEntry{{Name: "alpha", Nodes: []string{"node-one", "node-two"}}},
		ProxySettings: config.ProxySettings{EnableResponsesEndpoint: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	first := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"alpha","input":"first question","store":true}`))
	req.Header.Set("Content-Type", "application/json")
	app.ServeHTTP(first, req)
	if first.Code != http.StatusOK {
		t.Fatalf("first status = %d body=%s", first.Code, first.Body.String())
	}
	var firstPayload map[string]any
	if err := json.Unmarshal(first.Body.Bytes(), &firstPayload); err != nil {
		t.Fatal(err)
	}
	responseID := firstPayload["id"].(string)
	second := httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"alpha","input":"second question","previous_response_id":"`+responseID+`"}`))
	req.Header.Set("Content-Type", "application/json")
	app.ServeHTTP(second, req)
	if second.Code != http.StatusOK {
		t.Fatalf("second status = %d body=%s", second.Code, second.Body.String())
	}
	if n1Hits != 2 || n2Hits != 0 {
		t.Fatalf("node hits: n1=%d n2=%d", n1Hits, n2Hits)
	}
}

func TestResponsesEndpointPersistsStoredResponseAcrossRestart(t *testing.T) {
	var hit int
	var secondPayload map[string]any
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hit++
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		if hit == 2 {
			secondPayload = payload
		}
		w.Header().Set("Content-Type", "application/json")
		if hit == 1 {
			_, _ = w.Write([]byte(`{"id":"chatcmpl-1","model":"openai/upstream","choices":[{"finish_reason":"stop","message":{"content":"first answer"}}],"usage":{"prompt_tokens":4,"completion_tokens":3,"total_tokens":7}}`))
			return
		}
		_, _ = w.Write([]byte(`{"id":"chatcmpl-2","model":"openai/upstream","choices":[{"finish_reason":"stop","message":{"content":"second answer"}}],"usage":{"prompt_tokens":8,"completion_tokens":3,"total_tokens":11}}`))
	}))
	defer upstream.Close()

	dbPath := filepath.Join(t.TempDir(), "logs.db")
	cfg := &config.Config{
		ModelList: []config.ModelEntry{{
			ModelName: "alpha",
			ModelParams: map[string]any{
				"api_base": upstream.URL + "/v1",
				"model":    "openai/upstream",
			},
		}},
		ProxySettings: config.ProxySettings{EnableResponsesEndpoint: true},
		Database: map[string]any{
			"backend": "sqlite",
			"connection": map[string]any{
				"sqlite": map[string]any{"path": dbPath},
			},
		},
	}
	app, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	first := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"alpha","input":"first question","store":true}`))
	req.Header.Set("Content-Type", "application/json")
	app.ServeHTTP(first, req)
	if first.Code != http.StatusOK {
		t.Fatalf("first status = %d body=%s", first.Code, first.Body.String())
	}
	var firstPayload map[string]any
	if err := json.Unmarshal(first.Body.Bytes(), &firstPayload); err != nil {
		t.Fatal(err)
	}
	responseID := firstPayload["id"].(string)
	if app.LogDB != nil {
		_ = app.LogDB.Close()
	}

	restarted, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.LogDB.Close()
	second := httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"alpha","input":"second question","previous_response_id":"`+responseID+`"}`))
	req.Header.Set("Content-Type", "application/json")
	restarted.ServeHTTP(second, req)
	if second.Code != http.StatusOK {
		t.Fatalf("second status = %d body=%s", second.Code, second.Body.String())
	}
	messages := secondPayload["messages"].([]any)
	if len(messages) != 3 {
		t.Fatalf("messages = %#v", messages)
	}
	if messages[0].(map[string]any)["content"] != "first question" || messages[1].(map[string]any)["content"] != "first answer" || messages[2].(map[string]any)["content"] != "second question" {
		t.Fatalf("messages = %#v", messages)
	}
}

func TestResponsesEndpointPassesThroughNativeBackend(t *testing.T) {
	var gotPath string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"resp_native","object":"response","status":"completed","model":"native","output":[],"output_text":"","usage":{"input_tokens":1,"output_tokens":2,"total_tokens":3}}`))
	}))
	defer upstream.Close()
	app, err := New(&config.Config{
		ModelList: []config.ModelEntry{{
			ModelName: "alpha",
			ModelParams: map[string]any{
				"api_base":               upstream.URL + "/v1",
				"model":                  "native",
				"supports_responses_api": true,
			},
		}},
		ProxySettings: config.ProxySettings{EnableResponsesEndpoint: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	resp := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"alpha","input":"hi"}`))
	req.Header.Set("Content-Type", "application/json")
	app.ServeHTTP(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", resp.Code, resp.Body.String())
	}
	if gotPath != "/v1/responses" {
		t.Fatalf("upstream path = %s", gotPath)
	}
	if !strings.Contains(resp.Body.String(), `"id":"resp_native"`) {
		t.Fatalf("body = %s", resp.Body.String())
	}
}

func TestResponsesEndpointTranslatesStreamingOpenAIBackend(t *testing.T) {
	var gotPath string
	var gotPayload map[string]any
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		if err := json.NewDecoder(r.Body).Decode(&gotPayload); err != nil {
			t.Fatal(err)
		}
		body := `data: {"choices":[{"delta":{"role":"assistant"},"index":0}]}` + "\n\n" +
			`data: {"choices":[{"delta":{"reasoning_content":"hidden"},"index":0}]}` + "\n\n" +
			`data: {"choices":[{"delta":{"content":"hello"},"index":0}]}` + "\n\n" +
			`data: {"choices":[{"delta":{},"finish_reason":"stop","index":0}],"usage":{"prompt_tokens":5,"completion_tokens":2,"total_tokens":7,"prompt_tokens_details":{"cached_tokens":4},"completion_tokens_details":{"reasoning_tokens":1}}}` + "\n\n" +
			"data: [DONE]\n\n"
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Content-Length", strconv.Itoa(len(body)))
		_, _ = w.Write([]byte(body))
	}))
	defer upstream.Close()
	app, err := New(&config.Config{
		ModelList: []config.ModelEntry{{
			ModelName: "alpha",
			ModelParams: map[string]any{
				"api_base": upstream.URL + "/v1",
				"model":    "openai/upstream",
			},
		}},
		ProxySettings: config.ProxySettings{EnableResponsesEndpoint: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	resp := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"alpha","input":"hi","stream":true}`))
	req.Header.Set("Content-Type", "application/json")
	app.ServeHTTP(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", resp.Code, resp.Body.String())
	}
	if gotPath != "/v1/chat/completions" || gotPayload["stream"] != true {
		t.Fatalf("upstream path/payload = %s %#v", gotPath, gotPayload)
	}
	if got := resp.Header().Get("Content-Length"); got != "" {
		t.Fatalf("content-length leaked from rewritten stream: %s", got)
	}
	body := resp.Body.String()
	for _, want := range []string{"event: response.created", "event: response.in_progress", "event: response.reasoning_summary_text.delta", "event: response.reasoning_summary_text.done", `"delta":"hidden"`, "event: response.output_text.delta", `"delta":"hello"`, "event: response.completed", `"output_text":"hello"`, `"input_tokens_details":{"cached_tokens":4}`, `"output_tokens_details":{"reasoning_tokens":1}`} {
		if !strings.Contains(body, want) {
			t.Fatalf("stream body missing %q: %s", want, body)
		}
	}
}

func TestResponsesEndpointPassesThroughNativeStreamingBackend(t *testing.T) {
	var gotPath string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: response.created\n"))
		_, _ = w.Write([]byte(`data: {"type":"response.created","response":{"id":"resp_native","object":"response","status":"in_progress","model":"native","output":[]}}` + "\n\n"))
		_, _ = w.Write([]byte("event: response.completed\n"))
		_, _ = w.Write([]byte(`data: {"type":"response.completed","response":{"id":"resp_native","object":"response","status":"completed","model":"native","output":[],"output_text":"","usage":{"input_tokens":1,"output_tokens":2,"total_tokens":3}}}` + "\n\n"))
	}))
	defer upstream.Close()
	app, err := New(&config.Config{
		ModelList: []config.ModelEntry{{
			ModelName: "alpha",
			ModelParams: map[string]any{
				"api_base":               upstream.URL + "/v1",
				"model":                  "native",
				"supports_responses_api": true,
			},
		}},
		ProxySettings: config.ProxySettings{EnableResponsesEndpoint: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	resp := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"alpha","input":"hi","stream":true}`))
	req.Header.Set("Content-Type", "application/json")
	app.ServeHTTP(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", resp.Code, resp.Body.String())
	}
	if gotPath != "/v1/responses" {
		t.Fatalf("upstream path = %s", gotPath)
	}
	body := resp.Body.String()
	if !strings.Contains(body, "event: response.created") || !strings.Contains(body, "resp_native") {
		t.Fatalf("stream body = %s", body)
	}
}

func TestProviderConformanceMatrix(t *testing.T) {
	openAIConfig := func(baseURL string) *config.Config {
		return &config.Config{
			ModelList: []config.ModelEntry{{
				ModelName: "alpha",
				ModelParams: map[string]any{
					"api_base": baseURL + "/v1",
					"api_key":  "upstream-key",
					"model":    "openai/upstream",
				},
			}},
			ProxySettings: config.ProxySettings{EnableMessagesEndpoint: true, EnableResponsesEndpoint: true},
		}
	}
	anthropicConfig := func(baseURL string) *config.Config {
		return &config.Config{
			ModelList: []config.ModelEntry{{
				ModelName: "alpha",
				ModelParams: map[string]any{
					"api_base":          baseURL + "/v1",
					"api_key":           "anthropic-key",
					"api_type":          "anthropic",
					"anthropic_version": "2023-06-01",
					"model":             "claude-test",
				},
			}},
			ProxySettings: config.ProxySettings{EnableMessagesEndpoint: true, EnableResponsesEndpoint: true},
		}
	}
	nativeResponsesConfig := func(baseURL string) *config.Config {
		return &config.Config{
			ModelList: []config.ModelEntry{{
				ModelName: "alpha",
				ModelParams: map[string]any{
					"api_base":               baseURL + "/v1",
					"model":                  "native-responses",
					"supports_responses_api": true,
				},
			}},
			ProxySettings: config.ProxySettings{EnableResponsesEndpoint: true},
		}
	}
	deniedResponsesConfig := func(baseURL string) *config.Config {
		return &config.Config{
			ModelList: []config.ModelEntry{{
				ModelName: "alpha",
				ModelParams: map[string]any{
					"api_base":               baseURL + "/v1",
					"model":                  "native-responses",
					"supports_responses_api": true,
					"endpoints":              []string{"chat"},
				},
			}},
			ProxySettings: config.ProxySettings{EnableResponsesEndpoint: true},
		}
	}

	cases := []struct {
		name         string
		path         string
		body         string
		config       func(string) *config.Config
		upstreamBody string
		wantPath     string
		wantStatus   int
		wantHits     int
		assert       func(t *testing.T, payload map[string]any)
	}{
		{
			name:         "openai chat pass through keeps finish reason",
			path:         "/v1/chat/completions",
			body:         `{"model":"alpha","messages":[{"role":"user","content":"hi"}]}`,
			config:       openAIConfig,
			upstreamBody: `{"id":"chatcmpl-matrix","model":"openai/upstream","choices":[{"finish_reason":"length","message":{"content":"cut"}}]}`,
			wantPath:     "/v1/chat/completions",
			wantStatus:   http.StatusOK,
			wantHits:     1,
			assert: func(t *testing.T, payload map[string]any) {
				choice := payload["choices"].([]any)[0].(map[string]any)
				if choice["finish_reason"] != "length" {
					t.Fatalf("chat finish_reason = %#v", choice["finish_reason"])
				}
				if _, ok := payload["status"]; ok {
					t.Fatalf("chat response was unexpectedly mapped to Responses: %#v", payload)
				}
			},
		},
		{
			name:         "translated messages maps content filter finish reason",
			path:         "/v1/messages",
			body:         `{"model":"alpha","max_tokens":8,"messages":[{"role":"user","content":"hi"}]}`,
			config:       openAIConfig,
			upstreamBody: `{"id":"chatcmpl-matrix","model":"openai/upstream","choices":[{"finish_reason":"content_filter","message":{"content":"blocked"}}]}`,
			wantPath:     "/v1/chat/completions",
			wantStatus:   http.StatusOK,
			wantHits:     1,
			assert: func(t *testing.T, payload map[string]any) {
				if payload["type"] != "message" || payload["stop_reason"] != "refusal" {
					t.Fatalf("translated messages payload = %#v", payload)
				}
			},
		},
		{
			name:         "anthropic messages pass through keeps native stop reason",
			path:         "/v1/messages",
			body:         `{"model":"alpha","max_tokens":8,"messages":[{"role":"user","content":"hi"}]}`,
			config:       anthropicConfig,
			upstreamBody: `{"id":"msg_matrix","type":"message","role":"assistant","content":[{"type":"text","text":"native"}],"model":"claude-test","stop_reason":"max_tokens","stop_sequence":null,"usage":{"input_tokens":1,"output_tokens":2}}`,
			wantPath:     "/v1/messages",
			wantStatus:   http.StatusOK,
			wantHits:     1,
			assert: func(t *testing.T, payload map[string]any) {
				if payload["id"] != "msg_matrix" || payload["stop_reason"] != "max_tokens" {
					t.Fatalf("anthropic pass-through payload = %#v", payload)
				}
			},
		},
		{
			name:         "translated responses maps length to incomplete",
			path:         "/v1/responses",
			body:         `{"model":"alpha","input":"hi"}`,
			config:       openAIConfig,
			upstreamBody: `{"id":"chatcmpl-matrix","model":"openai/upstream","choices":[{"finish_reason":"length","message":{"content":"partial"}}]}`,
			wantPath:     "/v1/chat/completions",
			wantStatus:   http.StatusOK,
			wantHits:     1,
			assert: func(t *testing.T, payload map[string]any) {
				if payload["status"] != "incomplete" {
					t.Fatalf("responses status = %#v; payload=%#v", payload["status"], payload)
				}
				details := payload["incomplete_details"].(map[string]any)
				if details["reason"] != "max_output_tokens" || payload["error"] != nil {
					t.Fatalf("incomplete/error fields = %#v/%#v", payload["incomplete_details"], payload["error"])
				}
			},
		},
		{
			name:         "translated responses maps content filter to failed",
			path:         "/v1/responses",
			body:         `{"model":"alpha","input":"hi"}`,
			config:       openAIConfig,
			upstreamBody: `{"id":"chatcmpl-matrix","model":"openai/upstream","choices":[{"finish_reason":"content_filter","message":{"content":"blocked"}}]}`,
			wantPath:     "/v1/chat/completions",
			wantStatus:   http.StatusOK,
			wantHits:     1,
			assert: func(t *testing.T, payload map[string]any) {
				if payload["status"] != "failed" || payload["incomplete_details"] != nil {
					t.Fatalf("responses failed fields = %#v", payload)
				}
				errPayload := payload["error"].(map[string]any)
				if errPayload["code"] != "content_filter" || errPayload["type"] != "model_error" {
					t.Fatalf("responses error = %#v", errPayload)
				}
			},
		},
		{
			name:         "native responses pass through keeps terminal fields",
			path:         "/v1/responses",
			body:         `{"model":"alpha","input":"hi"}`,
			config:       nativeResponsesConfig,
			upstreamBody: `{"id":"resp_matrix","object":"response","status":"failed","model":"native-responses","output":[],"output_text":"","error":{"type":"model_error","code":"native_filter","message":"native"},"incomplete_details":null}`,
			wantPath:     "/v1/responses",
			wantStatus:   http.StatusOK,
			wantHits:     1,
			assert: func(t *testing.T, payload map[string]any) {
				errPayload := payload["error"].(map[string]any)
				if payload["id"] != "resp_matrix" || payload["status"] != "failed" || errPayload["code"] != "native_filter" {
					t.Fatalf("native responses payload = %#v", payload)
				}
			},
		},
		{
			name:       "endpoint denied makes no upstream call",
			path:       "/v1/responses",
			body:       `{"model":"alpha","input":"hi"}`,
			config:     deniedResponsesConfig,
			wantStatus: http.StatusBadRequest,
			wantHits:   0,
			assert: func(t *testing.T, payload map[string]any) {
				errPayload := payload["error"].(map[string]any)
				if errPayload["code"] != "unsupported_endpoint" {
					t.Fatalf("denied error = %#v", payload)
				}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var gotPath string
			var upstreamHits int
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				upstreamHits++
				gotPath = r.URL.Path
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(tc.upstreamBody))
			}))
			defer upstream.Close()

			app, err := New(tc.config(upstream.URL))
			if err != nil {
				t.Fatal(err)
			}
			resp := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, tc.path, strings.NewReader(tc.body))
			req.Header.Set("Content-Type", "application/json")
			app.ServeHTTP(resp, req)
			if resp.Code != tc.wantStatus {
				t.Fatalf("status = %d body=%s", resp.Code, resp.Body.String())
			}
			if upstreamHits != tc.wantHits {
				t.Fatalf("upstream hits = %d, want %d", upstreamHits, tc.wantHits)
			}
			if tc.wantPath != "" && gotPath != tc.wantPath {
				t.Fatalf("upstream path = %s, want %s", gotPath, tc.wantPath)
			}
			var payload map[string]any
			if err := json.Unmarshal(resp.Body.Bytes(), &payload); err != nil {
				t.Fatal(err)
			}
			tc.assert(t, payload)
		})
	}
}

func TestValidationErrors(t *testing.T) {
	app := newTestApp(t, "http://upstream.test/v1")
	cases := []struct {
		path string
		body string
		want string
	}{
		{"/v1/chat/completions", `{"model":"alpha"}`, "messages"},
		{"/v1/embeddings", `{"model":"alpha"}`, "input"},
		{"/v1/rerank", `{"model":"alpha","query":"","documents":["a"]}`, "query"},
		{"/v1/rerank", `{"model":"alpha","query":"q","documents":[]}`, "documents"},
	}
	for _, tc := range cases {
		resp := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, tc.path, strings.NewReader(tc.body))
		req.Header.Set("Content-Type", "application/json")
		app.ServeHTTP(resp, req)
		if resp.Code != http.StatusBadRequest {
			t.Fatalf("%s status = %d body=%s", tc.path, resp.Code, resp.Body.String())
		}
		if !strings.Contains(resp.Body.String(), tc.want) {
			t.Fatalf("%s body = %s, want %q", tc.path, resp.Body.String(), tc.want)
		}
	}
}

func TestPolicyRejectsUnauthorizedModel(t *testing.T) {
	app := newTestApp(t, "http://upstream.test/v1")
	enabled := true
	app.Config.AppKeys = config.AppKeysConfig{
		Enabled: true,
		Keys: []config.AppKeyEntry{{
			KeyID: "k1", Secret: "secret", Enabled: &enabled, Models: []string{"beta"},
		}},
	}
	app.Policy.Config = app.Config
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"alpha","messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Authorization", "Bearer secret")
	resp := httptest.NewRecorder()
	app.ServeHTTP(resp, req)
	if resp.Code != http.StatusForbidden {
		t.Fatalf("status = %d body=%s", resp.Code, resp.Body.String())
	}
}

func TestRequiredHeaderPolicyRejectsBeforeUpstream(t *testing.T) {
	var upstreamHits int
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamHits++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}]}`))
	}))
	defer upstream.Close()
	enabled := true
	app, err := New(&config.Config{
		ModelList: []config.ModelEntry{{
			ModelName: "alpha",
			ModelParams: map[string]any{
				"api_base": upstream.URL + "/v1",
				"model":    "openai/upstream",
			},
		}},
		ProxySettings: config.ProxySettings{
			EnableMessagesEndpoint:  true,
			EnableResponsesEndpoint: true,
			Metrics:                 config.MetricsConfig{Enabled: true},
		},
		AppKeys: config.AppKeysConfig{
			Enabled: true,
			RequiredHeaders: []config.RequiredHeader{{
				Name: "X-Tenant-ID",
			}},
			Keys: []config.AppKeyEntry{{
				KeyID: "k1", Secret: "secret", Enabled: &enabled, Models: []string{"alpha"},
				Limits: config.LimitsConfig{RPS: 1, TokenQuota: 20, Concurrency: 1},
			}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		path string
		body string
	}{
		{"/v1/chat/completions", `{"model":"alpha","messages":[{"role":"user","content":"hi"}]}`},
		{"/v1/messages", `{"model":"alpha","max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`},
		{"/v1/responses", `{"model":"alpha","input":"hi"}`},
	}
	for _, tc := range cases {
		resp := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, tc.path, strings.NewReader(tc.body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer secret")
		app.ServeHTTP(resp, req)
		if resp.Code != http.StatusBadRequest {
			t.Fatalf("%s status = %d body=%s", tc.path, resp.Code, resp.Body.String())
		}
		if !strings.Contains(resp.Body.String(), "missing_required_header") {
			t.Fatalf("%s body = %s", tc.path, resp.Body.String())
		}
	}
	if upstreamHits != 0 {
		t.Fatalf("upstream hits = %d, want 0", upstreamHits)
	}
	if len(app.Admission.Metrics().State) != 0 {
		t.Fatalf("admission was touched before required-header rejection: %#v", app.Admission.Metrics().State)
	}
	body := getMetricsBody(t, app)
	assertContains(t, body, "yallmp_required_header_missing_total 3")
}

func TestAppKeyRateLimitRejectsSecondRequest(t *testing.T) {
	var upstreamHits int
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamHits++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}]}`))
	}))
	defer upstream.Close()
	enabled := true
	app, err := New(&config.Config{
		ModelList: []config.ModelEntry{{
			ModelName: "alpha",
			ModelParams: map[string]any{
				"api_base": upstream.URL + "/v1",
				"model":    "openai/upstream",
			},
		}},
		AppKeys: config.AppKeysConfig{
			Enabled: true,
			Keys: []config.AppKeyEntry{{
				KeyID: "k1", Secret: "secret", Enabled: &enabled, Models: []string{"alpha"},
				Limits: config.LimitsConfig{RPS: 1},
			}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	first := postChat(app, "secret")
	if first.Code != http.StatusOK {
		t.Fatalf("first status = %d body=%s", first.Code, first.Body.String())
	}
	second := postChat(app, "secret")
	if second.Code != http.StatusTooManyRequests {
		t.Fatalf("second status = %d body=%s", second.Code, second.Body.String())
	}
	if !strings.Contains(second.Body.String(), "rate_limit_exceeded") {
		t.Fatalf("body = %s", second.Body.String())
	}
	if upstreamHits != 1 {
		t.Fatalf("upstream hits = %d, want 1", upstreamHits)
	}
	metrics := app.Admission.Metrics()
	if got := metrics.State["k1::alpha"].RateLimited; got != 1 {
		t.Fatalf("rate limited metric = %d", got)
	}
}

func TestAppKeyGroupRateLimitSharedAcrossKeys(t *testing.T) {
	var upstreamHits int
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamHits++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}]}`))
	}))
	defer upstream.Close()
	enabled := true
	app, err := New(&config.Config{
		ModelList: []config.ModelEntry{{
			ModelName: "alpha",
			ModelParams: map[string]any{
				"api_base": upstream.URL + "/v1",
				"model":    "openai/upstream",
			},
		}},
		AppKeys: config.AppKeysConfig{
			Enabled: true,
			Groups: []config.AppKeyGroupEntry{{
				GroupID: "team-a",
				Enabled: &enabled,
				Models:  []string{"alpha"},
				Limits:  config.LimitsConfig{RPS: 1},
			}},
			Keys: []config.AppKeyEntry{
				{KeyID: "k1", GroupID: "team-a", Secret: "secret-a", Enabled: &enabled},
				{KeyID: "k2", GroupID: "team-a", Secret: "secret-b", Enabled: &enabled},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	first := postChat(app, "secret-a")
	if first.Code != http.StatusOK {
		t.Fatalf("first status = %d body=%s", first.Code, first.Body.String())
	}
	second := postChat(app, "secret-b")
	if second.Code != http.StatusTooManyRequests {
		t.Fatalf("second status = %d body=%s", second.Code, second.Body.String())
	}
	if upstreamHits != 1 {
		t.Fatalf("upstream hits = %d, want 1", upstreamHits)
	}
	metrics := app.Admission.Metrics()
	if got := metrics.State["group:team-a::alpha"].RateLimited; got != 1 {
		t.Fatalf("group rate limited metric = %d", got)
	}
}

func TestNodeAllowedKeysSelectsAuthorizedNode(t *testing.T) {
	var key1Hits int
	key1Server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key1Hits++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"key1"}}]}`))
	}))
	defer key1Server.Close()
	var key2Hits int
	key2Server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key2Hits++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"key2"}}]}`))
	}))
	defer key2Server.Close()
	enabled := true
	app, err := New(&config.Config{
		Nodes: []config.NodeEntry{
			{Name: "node-key-1", APIBase: key1Server.URL + "/v1", Model: "openai/key1", AllowedKeys: []any{"key-1"}},
			{Name: "node-key-2", APIBase: key2Server.URL + "/v1", Model: "openai/key2", AllowedKeys: []any{"key-2"}},
		},
		Models: []config.ClientModelEntry{{Name: "alpha", Nodes: []string{"node-key-1", "node-key-2"}}},
		AppKeys: config.AppKeysConfig{
			Enabled: true,
			Keys: []config.AppKeyEntry{
				{KeyID: "key-1", Secret: "secret-1", Enabled: &enabled, Models: []string{"alpha"}},
				{KeyID: "key-2", Secret: "secret-2", Enabled: &enabled, Models: []string{"alpha"}},
				{KeyID: "key-3", Secret: "secret-3", Enabled: &enabled, Models: []string{"alpha"}},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	resp := postChat(app, "secret-2")
	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", resp.Code, resp.Body.String())
	}
	if key1Hits != 0 || key2Hits != 1 {
		t.Fatalf("hits key1=%d key2=%d", key1Hits, key2Hits)
	}
	resp = postChat(app, "secret-3")
	if resp.Code != http.StatusForbidden {
		t.Fatalf("forbidden status = %d body=%s", resp.Code, resp.Body.String())
	}
	if !strings.Contains(resp.Body.String(), "model_access_denied") {
		t.Fatalf("body = %s", resp.Body.String())
	}
	if key1Hits != 0 || key2Hits != 1 {
		t.Fatalf("unexpected upstream hits key1=%d key2=%d", key1Hits, key2Hits)
	}
}

func TestModelRateLimitAppliesWithoutAppKey(t *testing.T) {
	var upstreamHits int
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamHits++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}]}`))
	}))
	defer upstream.Close()
	app, err := New(&config.Config{ModelList: []config.ModelEntry{{
		ModelName: "alpha",
		ModelParams: map[string]any{
			"api_base": upstream.URL + "/v1",
			"model":    "openai/upstream",
		},
		Limits: config.LimitsConfig{RPS: 1},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	first := postChat(app, "")
	if first.Code != http.StatusOK {
		t.Fatalf("first status = %d body=%s", first.Code, first.Body.String())
	}
	second := postChat(app, "")
	if second.Code != http.StatusTooManyRequests {
		t.Fatalf("second status = %d body=%s", second.Code, second.Body.String())
	}
	if upstreamHits != 1 {
		t.Fatalf("upstream hits = %d, want 1", upstreamHits)
	}
}

func TestProviderNodeRateLimitRejectsSecondBackendAttempt(t *testing.T) {
	var upstreamHits int
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamHits++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}]}`))
	}))
	defer upstream.Close()
	app, err := New(&config.Config{
		Providers: []config.ProviderEntry{{
			Name:    "local",
			APIBase: upstream.URL + "/v1",
			Limits:  config.LimitsConfig{RPS: 1},
		}},
		Nodes: []config.NodeEntry{{
			Name:     "n1",
			Provider: "local",
			Model:    "openai/upstream",
		}},
		Models: []config.ClientModelEntry{{
			Name:  "alpha",
			Nodes: []string{"n1"},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	first := postChat(app, "")
	if first.Code != http.StatusOK {
		t.Fatalf("first status = %d body=%s", first.Code, first.Body.String())
	}
	second := postChat(app, "")
	if second.Code != http.StatusTooManyRequests {
		t.Fatalf("second status = %d body=%s", second.Code, second.Body.String())
	}
	if !strings.Contains(second.Body.String(), "rate_limit_exceeded") {
		t.Fatalf("body = %s", second.Body.String())
	}
	if upstreamHits != 1 {
		t.Fatalf("upstream hits = %d, want 1", upstreamHits)
	}
	metrics := app.Admission.Metrics()
	if got := metrics.State["backend:n1"].RateLimited; got != 1 {
		t.Fatalf("backend rate limited metric = %d", got)
	}
}

func TestProviderNodeRateLimitSkipsUnavailableStickySibling(t *testing.T) {
	var n1Hits int
	n1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n1Hits++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"n1"}}]}`))
	}))
	defer n1.Close()
	var n2Hits int
	n2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n2Hits++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"n2"}}]}`))
	}))
	defer n2.Close()
	app, err := New(&config.Config{
		Nodes: []config.NodeEntry{
			{Name: "n1", APIBase: n1.URL + "/v1", Model: "openai/n1", Limits: config.LimitsConfig{RPS: 1}},
			{Name: "n2", APIBase: n2.URL + "/v1", Model: "openai/n2", Limits: config.LimitsConfig{RPS: 1}},
		},
		Models: []config.ClientModelEntry{{
			Name:  "alpha",
			Nodes: []string{"n1", "n2"},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	postStickyChat := func() *httptest.ResponseRecorder {
		resp := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"alpha","conversation_id":"sticky","messages":[{"role":"user","content":"hi"}]}`))
		req.Header.Set("Content-Type", "application/json")
		app.ServeHTTP(resp, req)
		return resp
	}
	first := postStickyChat()
	if first.Code != http.StatusOK {
		t.Fatalf("first status = %d body=%s", first.Code, first.Body.String())
	}
	second := postStickyChat()
	if second.Code != http.StatusOK {
		t.Fatalf("second status = %d body=%s", second.Code, second.Body.String())
	}
	if n1Hits != 1 || n2Hits != 1 {
		t.Fatalf("node hits: n1=%d n2=%d", n1Hits, n2Hits)
	}
	metrics := app.Admission.Metrics()
	if got := metrics.State["backend:n1"].RateLimited; got != 0 {
		t.Fatalf("backend n1 rate limited metric = %d", got)
	}
}

func TestQuotaAwareSelectionSkipsExhaustedBackendAndRecordsSelectedUsageOnce(t *testing.T) {
	var n1Hits int
	n1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n1Hits++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"n1"}}],"usage":{"total_tokens":11}}`))
	}))
	defer n1.Close()
	var n2Hits int
	n2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n2Hits++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"n2"}}],"usage":{"total_tokens":11}}`))
	}))
	defer n2.Close()
	app, err := New(&config.Config{
		RouterSettings: config.RouterSettings{
			QuotaAwareSelection: config.QuotaAwareSelectionSettings{Enabled: true},
		},
		Nodes: []config.NodeEntry{
			{Name: "n1", APIBase: n1.URL + "/v1", Model: "openai/n1", Limits: config.LimitsConfig{TokenQuota: 10}},
			{Name: "n2", APIBase: n2.URL + "/v1", Model: "openai/n2", Limits: config.LimitsConfig{TokenQuota: 50}},
		},
		Models: []config.ClientModelEntry{{
			Name:   "alpha",
			Nodes:  []string{"n1", "n2"},
			Limits: config.LimitsConfig{TokenQuota: 100},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	app.Quota.Seed(quota.BackendScopeKey, "n1", 10)

	resp := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"alpha","messages":[{"role":"user","content":"hi"}],"max_tokens":5}`))
	req.Header.Set("Content-Type", "application/json")
	app.ServeHTTP(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", resp.Code, resp.Body.String())
	}
	if n1Hits != 0 || n2Hits != 1 {
		t.Fatalf("node hits: n1=%d n2=%d", n1Hits, n2Hits)
	}
	metrics := app.Quota.Metrics()
	if got := metrics.UsedByScope[quota.BuildScope("", "alpha")]; got != 11 {
		t.Fatalf("request quota usage = %d, want 11", got)
	}
	if got := metrics.UsedByScope[quota.BackendScope("n1")]; got != 10 {
		t.Fatalf("n1 backend quota usage = %d, want seeded 10", got)
	}
	if got := metrics.UsedByScope[quota.BackendScope("n2")]; got != 11 {
		t.Fatalf("n2 backend quota usage = %d, want 11", got)
	}
}

func TestQuotaAwareSelectionDefaultOffDoesNotSkipExhaustedBackend(t *testing.T) {
	var n1Hits int
	n1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n1Hits++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"n1"}}],"usage":{"total_tokens":11}}`))
	}))
	defer n1.Close()
	var n2Hits int
	n2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n2Hits++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"n2"}}],"usage":{"total_tokens":11}}`))
	}))
	defer n2.Close()
	app, err := New(&config.Config{
		Nodes: []config.NodeEntry{
			{Name: "n1", APIBase: n1.URL + "/v1", Model: "openai/n1", Limits: config.LimitsConfig{TokenQuota: 10}},
			{Name: "n2", APIBase: n2.URL + "/v1", Model: "openai/n2", Limits: config.LimitsConfig{TokenQuota: 50}},
		},
		Models: []config.ClientModelEntry{{Name: "alpha", Nodes: []string{"n1", "n2"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	app.Quota.Seed(quota.BackendScopeKey, "n1", 10)

	resp := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"alpha","messages":[{"role":"user","content":"hi"}],"max_tokens":5}`))
	req.Header.Set("Content-Type", "application/json")
	app.ServeHTTP(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", resp.Code, resp.Body.String())
	}
	if n1Hits != 1 || n2Hits != 0 {
		t.Fatalf("node hits: n1=%d n2=%d", n1Hits, n2Hits)
	}
	if got := app.Quota.Metrics().UsedByScope[quota.BackendScope("n1")]; got != 10 {
		t.Fatalf("n1 backend quota usage = %d, want seeded 10", got)
	}
}

func TestQuotaAwareSelectionAppliesToTranslatedMessagesAndResponses(t *testing.T) {
	for _, tc := range []struct {
		name string
		path string
		body string
	}{
		{
			name: "messages",
			path: "/v1/messages",
			body: `{"model":"alpha","max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`,
		},
		{
			name: "responses",
			path: "/v1/responses",
			body: `{"model":"alpha","input":"hi","max_output_tokens":16}`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var n1Hits int
			n1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				n1Hits++
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"choices":[{"finish_reason":"stop","message":{"content":"n1"}}],"usage":{"prompt_tokens":5,"completion_tokens":6,"total_tokens":11}}`))
			}))
			defer n1.Close()
			var n2Hits int
			n2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				n2Hits++
				if r.URL.Path != "/v1/chat/completions" {
					t.Fatalf("upstream path = %s, want /v1/chat/completions", r.URL.Path)
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"choices":[{"finish_reason":"stop","message":{"content":"n2"}}],"usage":{"prompt_tokens":5,"completion_tokens":6,"total_tokens":11}}`))
			}))
			defer n2.Close()
			app, err := New(&config.Config{
				RouterSettings: config.RouterSettings{
					QuotaAwareSelection: config.QuotaAwareSelectionSettings{Enabled: true},
				},
				ProxySettings: config.ProxySettings{
					EnableMessagesEndpoint:  true,
					EnableResponsesEndpoint: true,
				},
				Nodes: []config.NodeEntry{
					{Name: "n1", APIBase: n1.URL + "/v1", Model: "openai/n1", Limits: config.LimitsConfig{TokenQuota: 10}},
					{Name: "n2", APIBase: n2.URL + "/v1", Model: "openai/n2", Limits: config.LimitsConfig{TokenQuota: 50}},
				},
				Models: []config.ClientModelEntry{{
					Name:   "alpha",
					Nodes:  []string{"n1", "n2"},
					Limits: config.LimitsConfig{TokenQuota: 100},
				}},
			})
			if err != nil {
				t.Fatal(err)
			}
			app.Quota.Seed(quota.BackendScopeKey, "n1", 10)

			resp := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, tc.path, strings.NewReader(tc.body))
			req.Header.Set("Content-Type", "application/json")
			app.ServeHTTP(resp, req)
			if resp.Code != http.StatusOK {
				t.Fatalf("status = %d body=%s", resp.Code, resp.Body.String())
			}
			if n1Hits != 0 || n2Hits != 1 {
				t.Fatalf("node hits: n1=%d n2=%d", n1Hits, n2Hits)
			}
			metrics := app.Quota.Metrics()
			if got := metrics.UsedByScope[quota.BackendScope("n1")]; got != 10 {
				t.Fatalf("n1 backend quota usage = %d, want seeded 10", got)
			}
			if got := metrics.UsedByScope[quota.BackendScope("n2")]; got != 11 {
				t.Fatalf("n2 backend quota usage = %d, want 11", got)
			}
		})
	}
}

func TestUnsupportedProviderEndpointReturnsBadRequest(t *testing.T) {
	var upstreamHits int
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamHits++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[]}`))
	}))
	defer upstream.Close()
	app, err := New(&config.Config{
		Providers: []config.ProviderEntry{{
			Name:      "chat-only",
			APIBase:   upstream.URL + "/v1",
			Endpoints: []string{"chat"},
		}},
		Nodes: []config.NodeEntry{{
			Name:     "n1",
			Provider: "chat-only",
			Model:    "openai/upstream",
		}},
		Models: []config.ClientModelEntry{{Name: "alpha", Nodes: []string{"n1"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	resp := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/embeddings", strings.NewReader(`{"model":"alpha","input":"hi"}`))
	req.Header.Set("Content-Type", "application/json")
	app.ServeHTTP(resp, req)
	if resp.Code != http.StatusBadRequest {
		t.Fatalf("status = %d body=%s", resp.Code, resp.Body.String())
	}
	if !strings.Contains(resp.Body.String(), "unsupported_endpoint") {
		t.Fatalf("body = %s", resp.Body.String())
	}
	if upstreamHits != 0 {
		t.Fatalf("upstream hits = %d, want 0", upstreamHits)
	}
}

func TestAppKeyTokenQuotaRejectsBeforeUpstream(t *testing.T) {
	var upstreamHits int
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamHits++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}]}`))
	}))
	defer upstream.Close()
	enabled := true
	app, err := New(&config.Config{
		ModelList: []config.ModelEntry{{
			ModelName: "alpha",
			ModelParams: map[string]any{
				"api_base": upstream.URL + "/v1",
				"model":    "openai/upstream",
			},
		}},
		AppKeys: config.AppKeysConfig{
			Enabled: true,
			Keys: []config.AppKeyEntry{{
				KeyID: "k1", Secret: "secret", Enabled: &enabled, Models: []string{"alpha"},
				Limits: config.LimitsConfig{TokenQuota: 5},
			}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	resp := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"alpha","messages":[{"role":"user","content":"hi"}],"max_tokens":10}`))
	req.Header.Set("Authorization", "Bearer secret")
	app.ServeHTTP(resp, req)
	if resp.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d body=%s", resp.Code, resp.Body.String())
	}
	if !strings.Contains(resp.Body.String(), "token_quota_exceeded") {
		t.Fatalf("body = %s", resp.Body.String())
	}
	if upstreamHits != 0 {
		t.Fatalf("upstream hits = %d, want 0", upstreamHits)
	}
}

func TestTokenQuotaCommitsActualUsageAndBlocksFutureRequests(t *testing.T) {
	var upstreamHits int
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamHits++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}],"usage":{"prompt_tokens":6,"completion_tokens":12,"total_tokens":18}}`))
	}))
	defer upstream.Close()
	enabled := true
	app, err := New(&config.Config{
		ModelList: []config.ModelEntry{{
			ModelName: "alpha",
			ModelParams: map[string]any{
				"api_base": upstream.URL + "/v1",
				"model":    "openai/upstream",
			},
		}},
		AppKeys: config.AppKeysConfig{
			Enabled: true,
			Keys: []config.AppKeyEntry{{
				KeyID: "k1", Secret: "secret", Enabled: &enabled, Models: []string{"alpha"},
				Limits: config.LimitsConfig{TokenQuota: 20},
			}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	first := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"alpha","messages":[{"role":"user","content":"hi"}],"max_tokens":1}`))
	req.Header.Set("Authorization", "Bearer secret")
	app.ServeHTTP(first, req)
	if first.Code != http.StatusOK {
		t.Fatalf("first status = %d body=%s", first.Code, first.Body.String())
	}
	if got := app.Quota.Metrics().UsedByScope["k1::alpha"]; got != 18 {
		t.Fatalf("used tokens = %d", got)
	}
	second := httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"alpha","messages":[{"role":"user","content":"hi"}],"max_tokens":3}`))
	req.Header.Set("Authorization", "Bearer secret")
	app.ServeHTTP(second, req)
	if second.Code != http.StatusTooManyRequests {
		t.Fatalf("second status = %d body=%s", second.Code, second.Body.String())
	}
	if upstreamHits != 1 {
		t.Fatalf("upstream hits = %d, want 1", upstreamHits)
	}
}

func TestTokenQuotaRestoresUsageFromPersistentLogDB(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "logs.db")
	enabled := true
	cfg := &config.Config{
		ModelList: []config.ModelEntry{{
			ModelName: "alpha",
			ModelParams: map[string]any{
				"api_base": "http://upstream.test/v1",
				"model":    "openai/upstream",
			},
		}},
		AppKeys: config.AppKeysConfig{
			Enabled: true,
			Keys: []config.AppKeyEntry{{
				KeyID: "k1", Secret: "secret", Enabled: &enabled, Models: []string{"alpha"},
				Limits: config.LimitsConfig{TokenQuota: 20},
			}},
		},
		Database: map[string]any{
			"backend": "sqlite",
			"connection": map[string]any{
				"sqlite": map[string]any{"path": dbPath},
			},
		},
	}
	store, err := logdb.NewStore(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Insert(context.Background(), logdb.Record{
		ModelName:  "alpha",
		Outcome:    "success",
		AppKeyID:   "k1",
		UsageStats: map[string]any{"total_tokens": 18},
	}); err != nil {
		t.Fatal(err)
	}
	_ = store.Close()

	app, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer app.LogDB.Close()
	if got := app.Quota.Metrics().UsedByScope["k1::alpha"]; got != 18 {
		t.Fatalf("restored quota usage = %d", got)
	}
	resp := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"alpha","messages":[{"role":"user","content":"hi"}],"max_tokens":3}`))
	req.Header.Set("Authorization", "Bearer secret")
	app.ServeHTTP(resp, req)
	if resp.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d body=%s", resp.Code, resp.Body.String())
	}
	if !strings.Contains(resp.Body.String(), "token_quota_exceeded") {
		t.Fatalf("body = %s", resp.Body.String())
	}
}

func TestTokenQuotaCalendarRestoreUsesCurrentUTCWindow(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "logs.db")
	now := time.Date(2026, time.June, 27, 12, 0, 0, 0, time.UTC)
	enabled := true
	cfg := &config.Config{
		ModelList: []config.ModelEntry{{
			ModelName: "alpha",
			ModelParams: map[string]any{
				"api_base": "http://upstream.test/v1",
				"model":    "openai/upstream",
			},
		}},
		AppKeys: config.AppKeysConfig{
			Enabled: true,
			Keys: []config.AppKeyEntry{{
				KeyID: "k1", Secret: "secret", Enabled: &enabled, Models: []string{"alpha"},
				Limits: config.LimitsConfig{TokenQuota: 20, TokenQuotaResetMode: "daily"},
			}},
		},
		Database: map[string]any{
			"backend": "sqlite",
			"connection": map[string]any{
				"sqlite": map[string]any{"path": dbPath},
			},
		},
	}
	store, err := logdb.NewStore(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	for _, record := range []logdb.Record{
		{RequestTime: now.Add(-24 * time.Hour), ModelName: "alpha", Outcome: "success", AppKeyID: "k1", UsageStats: map[string]any{"total_tokens": 18}},
		{RequestTime: quota.WindowStartAt(quota.ResetModeDaily, now), ModelName: "alpha", Outcome: "success", AppKeyID: "k1", UsageStats: map[string]any{"total_tokens": 4}},
	} {
		if _, err := store.Insert(context.Background(), record); err != nil {
			t.Fatal(err)
		}
	}

	manager := quota.NewManagerWithClock(func() time.Time { return now })
	seedQuotaFromLogDBAt(context.Background(), manager, store, cfg, now)

	metrics := manager.Metrics()
	if got := metrics.UsedByScope["k1::alpha"]; got != 4 {
		t.Fatalf("restored calendar usage = %d, want 4", got)
	}
	wantReset := time.Date(2026, time.June, 28, 0, 0, 0, 0, time.UTC)
	if got := metrics.ResetAtByScope["k1::alpha"]; !got.Equal(wantReset) {
		t.Fatalf("restored reset_at = %s, want %s", got, wantReset)
	}
}

func TestTokenQuotaRollingRestoreUsesOnlyActiveWindow(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "logs.db")
	now := time.Date(2026, time.June, 27, 12, 0, 0, 0, time.UTC)
	enabled := true
	cfg := &config.Config{
		ModelList: []config.ModelEntry{{
			ModelName: "alpha",
			ModelParams: map[string]any{
				"api_base": "http://upstream.test/v1",
				"model":    "openai/upstream",
			},
		}},
		AppKeys: config.AppKeysConfig{
			Enabled: true,
			Keys: []config.AppKeyEntry{{
				KeyID: "k1", Secret: "secret", Enabled: &enabled, Models: []string{"alpha"},
				Limits: config.LimitsConfig{TokenQuota: 20, TokenQuotaWindowSeconds: 3600},
			}},
		},
		Database: map[string]any{
			"backend": "sqlite",
			"connection": map[string]any{
				"sqlite": map[string]any{"path": dbPath},
			},
		},
	}
	store, err := logdb.NewStore(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	for _, record := range []logdb.Record{
		{RequestTime: now.Add(-2 * time.Hour), ModelName: "alpha", Outcome: "success", AppKeyID: "k1", UsageStats: map[string]any{"total_tokens": 18}},
		{RequestTime: now.Add(-30 * time.Minute), ModelName: "alpha", Outcome: "success", AppKeyID: "k1", UsageStats: map[string]any{"total_tokens": 6}},
	} {
		if _, err := store.Insert(context.Background(), record); err != nil {
			t.Fatal(err)
		}
	}

	manager := quota.NewManagerWithClock(func() time.Time { return now })
	seedQuotaFromLogDBAt(context.Background(), manager, store, cfg, now)

	metrics := manager.Metrics()
	if got := metrics.UsedByScope["k1::alpha"]; got != 6 {
		t.Fatalf("restored rolling quota usage = %d, want 6", got)
	}
	if got := metrics.ResetAtByScope["k1::alpha"]; !got.Equal(now.Add(time.Hour)) {
		t.Fatalf("restored rolling reset_at = %s, want %s", got, now.Add(time.Hour))
	}
}

func TestTokenQuotaRestoresGroupedUsageFromPersistentLogDB(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "logs.db")
	var upstreamHits int
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamHits++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}]}`))
	}))
	defer upstream.Close()
	enabled := true
	cfg := &config.Config{
		ModelList: []config.ModelEntry{{
			ModelName: "alpha",
			ModelParams: map[string]any{
				"api_base": upstream.URL + "/v1",
				"model":    "openai/upstream",
			},
		}},
		AppKeys: config.AppKeysConfig{
			Enabled: true,
			Groups: []config.AppKeyGroupEntry{{
				GroupID: "team-a",
				Enabled: &enabled,
				Models:  []string{"alpha"},
				Limits:  config.LimitsConfig{TokenQuota: 20},
			}},
			Keys: []config.AppKeyEntry{
				{KeyID: "k1", GroupID: "team-a", Secret: "secret-a", Enabled: &enabled},
				{KeyID: "k2", GroupID: "team-a", Secret: "secret-b", Enabled: &enabled},
			},
		},
		Database: map[string]any{
			"backend": "sqlite",
			"connection": map[string]any{
				"sqlite": map[string]any{"path": dbPath},
			},
		},
	}
	store, err := logdb.NewStore(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Insert(context.Background(), logdb.Record{
		ModelName:  "alpha",
		Outcome:    "success",
		AppKeyID:   "k1",
		UsageStats: map[string]any{"total_tokens": 18},
	}); err != nil {
		t.Fatal(err)
	}
	_ = store.Close()

	app, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer app.LogDB.Close()
	if got := app.Quota.Metrics().UsedByScope["group:team-a::alpha"]; got != 18 {
		t.Fatalf("restored grouped quota usage = %d", got)
	}
	resp := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"alpha","messages":[{"role":"user","content":"hi"}],"max_tokens":3}`))
	req.Header.Set("Authorization", "Bearer secret-b")
	app.ServeHTTP(resp, req)
	if resp.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d body=%s", resp.Code, resp.Body.String())
	}
	if upstreamHits != 0 {
		t.Fatalf("upstream hits = %d, want 0", upstreamHits)
	}
}

func TestTokenQuotaRestoreAggregatesGroupedUsageFromPersistentLogDB(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "logs.db")
	enabled := true
	cfg := &config.Config{
		ModelList: []config.ModelEntry{{
			ModelName: "alpha",
			ModelParams: map[string]any{
				"api_base": "http://upstream.test/v1",
				"model":    "openai/upstream",
			},
		}},
		AppKeys: config.AppKeysConfig{
			Enabled: true,
			Groups: []config.AppKeyGroupEntry{{
				GroupID: "team-a",
				Enabled: &enabled,
				Models:  []string{"alpha"},
				Limits:  config.LimitsConfig{TokenQuota: 20},
			}},
			Keys: []config.AppKeyEntry{
				{KeyID: "k1", GroupID: "team-a", Secret: "secret-a", Enabled: &enabled},
				{KeyID: "k2", GroupID: "team-a", Secret: "secret-b", Enabled: &enabled},
			},
		},
		Database: map[string]any{
			"backend": "sqlite",
			"connection": map[string]any{
				"sqlite": map[string]any{"path": dbPath},
			},
		},
	}
	store, err := logdb.NewStore(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	for _, record := range []logdb.Record{
		{ModelName: "alpha", Outcome: "success", AppKeyID: "k1", UsageStats: map[string]any{"total_tokens": 7}},
		{ModelName: "alpha", Outcome: "success", AppKeyID: "k2", UsageStats: map[string]any{"total_tokens": 8}},
	} {
		if _, err := store.Insert(context.Background(), record); err != nil {
			t.Fatal(err)
		}
	}

	manager := quota.NewManager()
	seedQuotaFromLogDBAt(context.Background(), manager, store, cfg, time.Date(2026, time.June, 27, 12, 0, 0, 0, time.UTC))

	if got := manager.Metrics().UsedByScope["group:team-a::alpha"]; got != 15 {
		t.Fatalf("restored grouped quota usage = %d, want 15", got)
	}
}

func TestBackendQuotaPersistenceDefaultOffDoesNotRestoreFromLogDB(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "logs.db")
	cfg := backendQuotaRestoreConfig(dbPath, nil, config.LimitsConfig{TokenQuota: 20})
	store, err := logdb.NewStore(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Insert(context.Background(), logdb.Record{
		ModelName:   "alpha",
		Outcome:     "success",
		BackendName: "n1",
		UsageStats:  map[string]any{"total_tokens": 18},
	}); err != nil {
		t.Fatal(err)
	}
	_ = store.Close()

	app, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer app.LogDB.Close()
	if got := app.Quota.Metrics().UsedByScope[quota.BackendScope("n1")]; got != 0 {
		t.Fatalf("backend quota restored with default-off setting: %d", got)
	}
}

func TestBackendQuotaPersistenceRequiresQuotaAwareSelectionEnabled(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "logs.db")
	cfg := backendQuotaRestoreConfig(dbPath, true, config.LimitsConfig{TokenQuota: 20})
	cfg.RouterSettings.QuotaAwareSelection.Enabled = false
	store, err := logdb.NewStore(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Insert(context.Background(), logdb.Record{
		ModelName:   "alpha",
		Outcome:     "success",
		BackendName: "n1",
		UsageStats:  map[string]any{"total_tokens": 18},
	}); err != nil {
		t.Fatal(err)
	}
	_ = store.Close()

	app, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer app.LogDB.Close()
	if got := app.Quota.Metrics().UsedByScope[quota.BackendScope("n1")]; got != 0 {
		t.Fatalf("backend quota restored with quota-aware selection disabled: %d", got)
	}
}

func TestBackendQuotaRollingRestoreUsesActiveWindowAndDoesNotDoubleCount(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "logs.db")
	now := time.Date(2026, time.June, 27, 12, 0, 0, 0, time.UTC)
	cfg := backendQuotaRestoreConfig(dbPath, true, config.LimitsConfig{
		TokenQuota:              20,
		TokenQuotaWindowSeconds: 3600,
	})
	store, err := logdb.NewStore(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	for _, record := range []logdb.Record{
		{RequestTime: now.Add(-2 * time.Hour), ModelName: "alpha", Outcome: "success", BackendName: "n1", UsageStats: map[string]any{"total_tokens": 18}},
		{RequestTime: now.Add(-30 * time.Minute), ModelName: "alpha", Outcome: "success", BackendName: "n1", UsageStats: map[string]any{"total_tokens": 6}},
		{RequestTime: now.Add(-30 * time.Minute), ModelName: "alpha", Outcome: "error", BackendName: "n1", UsageStats: map[string]any{"total_tokens": 99}},
		{RequestTime: now.Add(-30 * time.Minute), ModelName: "alpha", Outcome: "success", BackendName: "removed", UsageStats: map[string]any{"total_tokens": 5}},
	} {
		if _, err := store.Insert(context.Background(), record); err != nil {
			t.Fatal(err)
		}
	}

	manager := quota.NewManagerWithClock(func() time.Time { return now })
	seedBackendQuotaFromLogDBAt(context.Background(), manager, store, cfg, now)
	seedBackendQuotaFromLogDBAt(context.Background(), manager, store, cfg, now)

	metrics := manager.Metrics()
	if got := metrics.UsedByScope[quota.BackendScope("n1")]; got != 6 {
		t.Fatalf("restored rolling backend quota usage = %d, want 6", got)
	}
	if _, ok := metrics.UsedByScope[quota.BackendScope("removed")]; ok {
		t.Fatalf("removed backend was restored: %#v", metrics.UsedByScope)
	}
	if got := metrics.ResetAtByScope[quota.BackendScope("n1")]; !got.Equal(now.Add(time.Hour)) {
		t.Fatalf("restored rolling reset_at = %s, want %s", got, now.Add(time.Hour))
	}
}

func TestBackendQuotaCalendarRestoreUsesCurrentUTCWindow(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "logs.db")
	now := time.Date(2026, time.June, 27, 12, 0, 0, 0, time.UTC)
	cfg := backendQuotaRestoreConfig(dbPath, true, config.LimitsConfig{TokenQuota: 20, TokenQuotaResetMode: "daily"})
	store, err := logdb.NewStore(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	for _, record := range []logdb.Record{
		{RequestTime: now.Add(-24 * time.Hour), ModelName: "alpha", Outcome: "success", BackendName: "n1", UsageStats: map[string]any{"total_tokens": 18}},
		{RequestTime: quota.WindowStartAt(quota.ResetModeDaily, now), ModelName: "alpha", Outcome: "success", BackendName: "n1", UsageStats: map[string]any{"total_tokens": 4}},
	} {
		if _, err := store.Insert(context.Background(), record); err != nil {
			t.Fatal(err)
		}
	}

	manager := quota.NewManagerWithClock(func() time.Time { return now })
	seedBackendQuotaFromLogDBAt(context.Background(), manager, store, cfg, now)

	metrics := manager.Metrics()
	if got := metrics.UsedByScope[quota.BackendScope("n1")]; got != 4 {
		t.Fatalf("restored calendar backend quota usage = %d, want 4", got)
	}
	wantReset := time.Date(2026, time.June, 28, 0, 0, 0, 0, time.UTC)
	if got := metrics.ResetAtByScope[quota.BackendScope("n1")]; !got.Equal(wantReset) {
		t.Fatalf("restored calendar reset_at = %s, want %s", got, wantReset)
	}
}

func TestBackendQuotaRestoreDoesNotDoubleCountAcrossReload(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "logs.db")
	cfg := backendQuotaRestoreConfig(dbPath, true, config.LimitsConfig{TokenQuota: 20})
	store, err := logdb.NewStore(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Insert(context.Background(), logdb.Record{
		ModelName:   "alpha",
		Outcome:     "success",
		BackendName: "n1",
		UsageStats:  map[string]any{"total_tokens": 18},
	}); err != nil {
		t.Fatal(err)
	}
	_ = store.Close()

	app, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = app.LogDB.Close() }()
	if got := app.Quota.Metrics().UsedByScope[quota.BackendScope("n1")]; got != 18 {
		t.Fatalf("initial restored backend quota usage = %d, want 18", got)
	}
	if err := app.applyConfig(cfg, false); err != nil {
		t.Fatal(err)
	}
	if got := app.Quota.Metrics().UsedByScope[quota.BackendScope("n1")]; got != 18 {
		t.Fatalf("reloaded backend quota usage = %d, want 18", got)
	}
}

func TestBackendQuotaRestoreLogDBFailureIsNonFatal(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "logs.db")
	cfg := backendQuotaRestoreConfig(dbPath, true, config.LimitsConfig{TokenQuota: 20})
	store, err := logdb.NewStore(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	manager := quota.NewManager()

	seedBackendQuotaFromLogDBAt(context.Background(), manager, store, cfg, time.Date(2026, time.June, 27, 12, 0, 0, 0, time.UTC))

	if got := manager.Metrics().UsedByScope[quota.BackendScope("n1")]; got != 0 {
		t.Fatalf("backend quota restored after logdb failure: %d", got)
	}
}

func TestAdminBackendQuotaObservabilityRequiresAdminAndSanitizes(t *testing.T) {
	t.Setenv(config.AdminPasswordEnv, "pw")
	cfg := backendQuotaRestoreConfig("", true, config.LimitsConfig{
		TokenQuota:              20,
		TokenQuotaWindowSeconds: 3600,
	})
	app, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	app.Quota.Seed(quota.BackendScopeKey, "n1", 7)
	app.Quota.Seed("raw-app-key", "alpha", 5)

	blocked := httptest.NewRecorder()
	app.ServeHTTP(blocked, httptest.NewRequest(http.MethodGet, "/admin/backend-quotas", nil))
	if blocked.Code != http.StatusForbidden {
		t.Fatalf("status without admin = %d body=%s", blocked.Code, blocked.Body.String())
	}

	resp := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/admin/backend-quotas", nil)
	req.Header.Set("x-admin-password", "pw")
	app.ServeHTTP(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", resp.Code, resp.Body.String())
	}
	body := resp.Body.String()
	assertContains(t, body, `"persistence_enabled":true`)
	assertContains(t, body, `"backend":"n1"`)
	assertContains(t, body, `"used_tokens":7`)
	assertContains(t, body, `"limit_tokens":20`)
	for _, forbidden := range []string{
		"raw-app-key",
		"raw-app-key::alpha",
		"upstream-secret",
		"http://",
		"/v1",
		"app_key",
		"group:",
	} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("backend quota observability leaked %q in body:\n%s", forbidden, body)
		}
	}
}

func backendQuotaRestoreConfig(dbPath string, persist any, limits config.LimitsConfig) *config.Config {
	cfg := &config.Config{
		RouterSettings: config.RouterSettings{
			QuotaAwareSelection: config.QuotaAwareSelectionSettings{
				Enabled:             true,
				PersistBackendQuota: persist,
			},
		},
		Nodes: []config.NodeEntry{{
			Name:    "n1",
			APIBase: "http://n1-secret.test/v1",
			APIKey:  "upstream-secret",
			Model:   "openai/n1",
			Limits:  limits,
		}},
		Models: []config.ClientModelEntry{{
			Name:  "alpha",
			Nodes: []string{"n1"},
		}},
	}
	if dbPath != "" {
		cfg.Database = map[string]any{
			"backend": "sqlite",
			"connection": map[string]any{
				"sqlite": map[string]any{"path": dbPath},
			},
		}
	}
	return cfg
}

func TestStreamUsageReadCloserExtractsFragmentedUsage(t *testing.T) {
	reader := &streamUsageReadCloser{ReadCloser: io.NopCloser(strings.NewReader(
		"data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\n" +
			"data: {\"usage\":{\"prompt_tokens\":6,\"completion_tokens\":12,\"total_tokens\":18}}\n\n" +
			"data: [DONE]\n\n",
	))}
	buf := make([]byte, 9)
	for {
		_, err := reader.Read(buf)
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	if got := reader.TotalTokens(); got != 18 {
		t.Fatalf("total tokens = %d", got)
	}
}

func TestStreamingTokenQuotaCommitsSSEUsage(t *testing.T) {
	var upstreamHits int
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamHits++
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\n"))
		_, _ = w.Write([]byte("data: {\"usage\":{\"prompt_tokens\":6,\"completion_tokens\":12,\"total_tokens\":18}}\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer upstream.Close()
	enabled := true
	app, err := New(&config.Config{
		ModelList: []config.ModelEntry{{
			ModelName: "alpha",
			ModelParams: map[string]any{
				"api_base": upstream.URL + "/v1",
				"model":    "openai/upstream",
			},
		}},
		AppKeys: config.AppKeysConfig{
			Enabled: true,
			Keys: []config.AppKeyEntry{{
				KeyID: "k1", Secret: "secret", Enabled: &enabled, Models: []string{"alpha"},
				Limits: config.LimitsConfig{TokenQuota: 20},
			}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	first := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"alpha","messages":[{"role":"user","content":"hi"}],"stream":true,"max_tokens":1}`))
	req.Header.Set("Authorization", "Bearer secret")
	app.ServeHTTP(first, req)
	if first.Code != http.StatusOK {
		t.Fatalf("first status = %d body=%s", first.Code, first.Body.String())
	}
	if got := app.Quota.Metrics().UsedByScope["k1::alpha"]; got != 18 {
		t.Fatalf("used tokens = %d", got)
	}
	second := httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"alpha","messages":[{"role":"user","content":"hi"}],"stream":true,"max_tokens":3}`))
	req.Header.Set("Authorization", "Bearer secret")
	app.ServeHTTP(second, req)
	if second.Code != http.StatusTooManyRequests {
		t.Fatalf("second status = %d body=%s", second.Code, second.Body.String())
	}
	if upstreamHits != 1 {
		t.Fatalf("upstream hits = %d, want 1", upstreamHits)
	}
}

func TestTokenQuotaWindowAllowsRequestsAfterReset(t *testing.T) {
	var upstreamHits int
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamHits++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}],"usage":{"total_tokens":5}}`))
	}))
	defer upstream.Close()
	enabled := true
	app, err := New(&config.Config{
		ModelList: []config.ModelEntry{{
			ModelName: "alpha",
			ModelParams: map[string]any{
				"api_base": upstream.URL + "/v1",
				"model":    "openai/upstream",
			},
		}},
		AppKeys: config.AppKeysConfig{
			Enabled: true,
			Keys: []config.AppKeyEntry{{
				KeyID: "k1", Secret: "secret", Enabled: &enabled, Models: []string{"alpha"},
				Limits: config.LimitsConfig{TokenQuota: 5, TokenQuotaWindowSeconds: 0.01},
			}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	first := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"alpha","messages":[{"role":"user","content":"hi"}],"max_tokens":1}`))
	req.Header.Set("Authorization", "Bearer secret")
	app.ServeHTTP(first, req)
	if first.Code != http.StatusOK {
		t.Fatalf("first status = %d body=%s", first.Code, first.Body.String())
	}
	blocked := httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"alpha","messages":[{"role":"user","content":"hi"}],"max_tokens":1}`))
	req.Header.Set("Authorization", "Bearer secret")
	app.ServeHTTP(blocked, req)
	if blocked.Code != http.StatusTooManyRequests {
		t.Fatalf("blocked status = %d body=%s", blocked.Code, blocked.Body.String())
	}
	time.Sleep(20 * time.Millisecond)
	afterReset := httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"alpha","messages":[{"role":"user","content":"hi"}],"max_tokens":1}`))
	req.Header.Set("Authorization", "Bearer secret")
	app.ServeHTTP(afterReset, req)
	if afterReset.Code != http.StatusOK {
		t.Fatalf("after reset status = %d body=%s", afterReset.Code, afterReset.Body.String())
	}
	if upstreamHits != 2 {
		t.Fatalf("upstream hits = %d, want 2", upstreamHits)
	}
}

func TestTokenQuotaDailyResetAllowsRequestsAfterUTCMidnight(t *testing.T) {
	var upstreamHits int
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamHits++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}],"usage":{"total_tokens":5}}`))
	}))
	defer upstream.Close()
	now := time.Date(2026, time.June, 27, 23, 59, 0, 0, time.UTC)
	enabled := true
	app, err := New(&config.Config{
		ModelList: []config.ModelEntry{{
			ModelName: "alpha",
			ModelParams: map[string]any{
				"api_base": upstream.URL + "/v1",
				"model":    "openai/upstream",
			},
		}},
		AppKeys: config.AppKeysConfig{
			Enabled: true,
			Keys: []config.AppKeyEntry{{
				KeyID: "k1", Secret: "secret", Enabled: &enabled, Models: []string{"alpha"},
				Limits: config.LimitsConfig{TokenQuota: 5, TokenQuotaResetMode: "daily"},
			}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	app.Quota = quota.NewManagerWithClock(func() time.Time { return now })

	first := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"alpha","messages":[{"role":"user","content":"hi"}],"max_tokens":1}`))
	req.Header.Set("Authorization", "Bearer secret")
	app.ServeHTTP(first, req)
	if first.Code != http.StatusOK {
		t.Fatalf("first status = %d body=%s", first.Code, first.Body.String())
	}
	blocked := httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"alpha","messages":[{"role":"user","content":"hi"}],"max_tokens":1}`))
	req.Header.Set("Authorization", "Bearer secret")
	app.ServeHTTP(blocked, req)
	if blocked.Code != http.StatusTooManyRequests {
		t.Fatalf("blocked status = %d body=%s", blocked.Code, blocked.Body.String())
	}

	now = time.Date(2026, time.June, 28, 0, 0, 0, 0, time.UTC)
	afterReset := httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"alpha","messages":[{"role":"user","content":"hi"}],"max_tokens":1}`))
	req.Header.Set("Authorization", "Bearer secret")
	app.ServeHTTP(afterReset, req)
	if afterReset.Code != http.StatusOK {
		t.Fatalf("after reset status = %d body=%s", afterReset.Code, afterReset.Body.String())
	}
	if upstreamHits != 2 {
		t.Fatalf("upstream hits = %d, want 2", upstreamHits)
	}
}

func TestUsageEndpointExposesDailyQuotaResetAt(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}],"usage":{"total_tokens":5}}`))
	}))
	defer upstream.Close()
	now := time.Date(2026, time.June, 27, 12, 30, 0, 0, time.UTC)
	enabled := true
	app, err := New(&config.Config{
		ModelList: []config.ModelEntry{{
			ModelName: "alpha",
			ModelParams: map[string]any{
				"api_base": upstream.URL + "/v1",
				"model":    "openai/upstream",
			},
		}},
		AppKeys: config.AppKeysConfig{
			Enabled: true,
			Keys: []config.AppKeyEntry{{
				KeyID: "k1", Secret: "secret", Enabled: &enabled, Models: []string{"alpha"},
				Limits: config.LimitsConfig{TokenQuota: 100, TokenQuotaResetMode: "daily"},
			}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	app.Quota = quota.NewManagerWithClock(func() time.Time { return now })

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"alpha","messages":[{"role":"user","content":"hi"}],"max_tokens":1}`))
	req.Header.Set("Authorization", "Bearer secret")
	resp := httptest.NewRecorder()
	app.ServeHTTP(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("chat status = %d body=%s", resp.Code, resp.Body.String())
	}

	usageResp := httptest.NewRecorder()
	app.ServeHTTP(usageResp, httptest.NewRequest(http.MethodGet, "/v1/usage", nil))
	if usageResp.Code != http.StatusOK {
		t.Fatalf("usage status = %d body=%s", usageResp.Code, usageResp.Body.String())
	}
	var payload struct {
		Quota struct {
			ResetAtByScope map[string]time.Time `json:"reset_at_by_scope"`
		} `json:"quota"`
	}
	if err := json.Unmarshal(usageResp.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	want := time.Date(2026, time.June, 28, 0, 0, 0, 0, time.UTC)
	if got := payload.Quota.ResetAtByScope["k1::alpha"]; !got.Equal(want) {
		t.Fatalf("reset_at_by_scope[k1::alpha] = %s, want %s; body=%s", got, want, usageResp.Body.String())
	}
}

func TestAdminRegisterAddsModel(t *testing.T) {
	app := newTestApp(t, "http://upstream.test/v1")
	resp := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/admin/models", strings.NewReader(`{"model_name":"beta","api_base":"http://beta.test/v1","api_key":"k"}`))
	app.ServeHTTP(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", resp.Code, resp.Body.String())
	}
	names := app.Router.ListModelNames()
	if len(names) != 2 || names[0] != "alpha" || names[1] != "beta" {
		t.Fatalf("names = %#v", names)
	}
	if !strings.Contains(resp.Body.String(), `"replaced":false`) {
		t.Fatalf("body = %s", resp.Body.String())
	}
}

func TestAdminRegisterGigaChatGeneratesAPIKeyFromCredentials(t *testing.T) {
	app := newTestApp(t, "http://upstream.test/v1")
	resp := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/admin/models", strings.NewReader(`{
		"model_name":"GigaChat-Admin",
		"api_base":"https://gigachat.example/api/v1",
		"api_type":"gigachat",
		"model":"GigaChat-Max",
		"client_id":"client-id-1",
		"client_secret":"client-secret-2",
		"scope":"GIGACHAT_API_CORP",
		"auth_url":"https://auth.example/oauth",
		"emulate_tool_calls":true
	}`))
	app.ServeHTTP(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", resp.Code, resp.Body.String())
	}
	expectedKey := base64.StdEncoding.EncodeToString([]byte("client-id-1:client-secret-2"))
	backend := app.Router.Backends["GigaChat-Admin"]
	if backend.APIType != "gigachat" || backend.APIKey != expectedKey || backend.GigaChat == nil {
		t.Fatalf("backend = %#v", backend)
	}
	entry, ok := app.Config.FindLegacyModel("GigaChat-Admin")
	if !ok {
		t.Fatalf("registered model not found")
	}
	if entry.ModelParams["api_key"] != expectedKey || entry.ModelParams["client_id"] != "client-id-1" || entry.ModelParams["client_secret"] != "client-secret-2" || entry.ModelParams["emulate_tool_calls"] != true {
		t.Fatalf("params = %#v", entry.ModelParams)
	}
}

func TestAdminGetConfigMasksSensitiveKeys(t *testing.T) {
	app := newTestApp(t, "http://upstream.test/v1")
	resp := httptest.NewRecorder()
	app.ServeHTTP(resp, httptest.NewRequest(http.MethodGet, "/admin/config", nil))
	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", resp.Code, resp.Body.String())
	}
	var payload map[string]any
	if err := json.Unmarshal(resp.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	models := payload["model_list"].([]any)
	params := models[0].(map[string]any)["model_params"].(map[string]any)
	if _, ok := params["api_key"]; ok {
		t.Fatalf("api_key was not masked: %#v", params)
	}
	if params["api_base"] == "" {
		t.Fatalf("non-sensitive fields were not preserved: %#v", params)
	}
}

func TestConfiguredAdminPasswordProtectsConfigAndLogAPIs(t *testing.T) {
	t.Setenv(config.AdminPasswordEnv, "pw")
	app := newTestApp(t, "http://upstream.test/v1")
	tests := []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/admin/config"},
		{http.MethodPut, "/admin/config"},
		{http.MethodPost, "/admin/config/validate"},
		{http.MethodPost, "/admin/config/reload"},
		{http.MethodGet, "/admin/logs"},
		{http.MethodGet, "/admin/logs/stop-reasons"},
		{http.MethodGet, "/admin/logs/1"},
		{http.MethodGet, "/admin/traces/0123456789abcdef0123456789abcdef"},
		{http.MethodGet, "/api/logs"},
		{http.MethodGet, "/api/logs/models"},
		{http.MethodGet, "/api/logs/1"},
	}
	for _, tc := range tests {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			for _, password := range []string{"", "wrong"} {
				resp := httptest.NewRecorder()
				req := httptest.NewRequest(tc.method, tc.path, nil)
				if password != "" {
					req.Header.Set("x-admin-password", password)
				}
				app.ServeHTTP(resp, req)
				if resp.Code != http.StatusForbidden {
					t.Fatalf("password %q: status = %d body=%s", password, resp.Code, resp.Body.String())
				}
			}
		})
	}

	for _, path := range []string{"/admin/config", "/admin/logs", "/api/logs"} {
		resp := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("x-admin-password", "pw")
		app.ServeHTTP(resp, req)
		if resp.Code != http.StatusOK {
			t.Fatalf("authorized %s: status = %d body=%s", path, resp.Code, resp.Body.String())
		}
	}
}

func TestAdminValidateConfigReturnsFieldIssues(t *testing.T) {
	app := newTestApp(t, "http://upstream.test/v1")
	resp := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/admin/config/validate", strings.NewReader(`{
		"model_list": [
			{"model_name": "alpha", "model_params": {"api_base": "not-a-url"}}
		]
	}`))
	req.Header.Set("Content-Type", "application/json")
	app.ServeHTTP(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", resp.Code, resp.Body.String())
	}
	var payload map[string]any
	if err := json.Unmarshal(resp.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload["ok"] != false || payload["error_count"].(float64) == 0 {
		t.Fatalf("payload = %#v", payload)
	}
	issues := payload["issues"].([]any)
	first := issues[0].(map[string]any)
	if first["path"] != "model_list[0].model_params.api_base" || first["code"] != "invalid_url" {
		t.Fatalf("issues = %#v", issues)
	}
}

func TestAdminPutConfigRejectsValidationIssuesWithoutMutatingRouter(t *testing.T) {
	app := newTestApp(t, "http://upstream.test/v1")
	resp := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/admin/config", strings.NewReader(`{
		"model_list": [
			{"model_name": "alpha", "model_params": {"api_base": "not-a-url"}}
		]
	}`))
	req.Header.Set("Content-Type", "application/json")
	app.ServeHTTP(resp, req)
	if resp.Code != http.StatusBadRequest {
		t.Fatalf("status = %d body=%s", resp.Code, resp.Body.String())
	}
	if got := app.Router.Backends["alpha"].BaseURL; got != "http://upstream.test/v1" {
		t.Fatalf("router mutated despite validation failure: %s", got)
	}
	var payload map[string]any
	if err := json.Unmarshal(resp.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload["ok"] != false || payload["error_count"].(float64) == 0 {
		t.Fatalf("payload = %#v", payload)
	}
}

func TestAdminPutConfigPreservesSecretsMissingFromMaskedConfig(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(cfgPath, []byte(`
model_list:
  - model_name: alpha
    protected: false
    model_params:
      api_base: http://alpha.test/v1
      api_key: original-secret
      model: openai/upstream-alpha
`), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadWithOptions(config.LoadOptions{Path: cfgPath, SubstituteEnv: false})
	if err != nil {
		t.Fatal(err)
	}
	app, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}

	getResp := httptest.NewRecorder()
	app.ServeHTTP(getResp, httptest.NewRequest(http.MethodGet, "/admin/config", nil))
	if getResp.Code != http.StatusOK {
		t.Fatalf("GET status = %d body=%s", getResp.Code, getResp.Body.String())
	}
	var raw map[string]any
	if err := json.Unmarshal(getResp.Body.Bytes(), &raw); err != nil {
		t.Fatal(err)
	}
	models := raw["model_list"].([]any)
	model := models[0].(map[string]any)
	params := model["model_params"].(map[string]any)
	if _, ok := params["api_key"]; ok {
		t.Fatalf("masked config still includes api_key: %#v", params)
	}
	params["api_base"] = "http://changed.test/v1"

	body, _ := json.Marshal(raw)
	putResp := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/admin/config", strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	app.ServeHTTP(putResp, req)
	if putResp.Code != http.StatusOK {
		t.Fatalf("PUT status = %d body=%s", putResp.Code, putResp.Body.String())
	}
	if got := app.Config.ModelList[0].ModelParams["api_key"]; got != "original-secret" {
		t.Fatalf("config api_key = %#v", got)
	}
	if got := app.Router.Backends["alpha"].APIKey; got != "original-secret" {
		t.Fatalf("router api_key = %#v", got)
	}
	saved, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(saved), "original-secret") {
		t.Fatalf("saved config lost api key:\n%s", saved)
	}
	if !strings.Contains(string(saved), "http://changed.test/v1") {
		t.Fatalf("saved config missed non-sensitive edit:\n%s", saved)
	}
}

func TestAdminPutConfigUpdatesRouterParameters(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(cfgPath, []byte(`
model_list:
  - model_name: alpha
    protected: false
    model_params:
      api_base: http://alpha.test/v1
      api_key: key
      model: openai/upstream-alpha
    parameters:
      tool_choice:
        default: none
        allow_override: false
`), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadWithOptions(config.LoadOptions{Path: cfgPath, SubstituteEnv: false})
	if err != nil {
		t.Fatal(err)
	}
	app, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	before := app.Router.Backends["alpha"].Parameters["tool_choice"]
	if before.Default != "none" || before.AllowOverride {
		t.Fatalf("initial parameter = %#v", before)
	}

	raw := app.Config.ToMap()
	models := raw["model_list"].([]any)
	model := models[0].(map[string]any)
	model["parameters"] = map[string]any{
		"tool_choice": map[string]any{"default": "required", "allow_override": true},
		"max_tokens":  map[string]any{"default": 1000, "allow_override": true},
	}
	body, _ := json.Marshal(raw)
	resp := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/admin/config", strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	app.ServeHTTP(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", resp.Code, resp.Body.String())
	}
	updated := app.Router.Backends["alpha"].Parameters["tool_choice"]
	if updated.Default != "required" || !updated.AllowOverride {
		t.Fatalf("updated parameter = %#v", updated)
	}
	if got := app.Router.Backends["alpha"].Parameters["max_tokens"].Default; got != int64(1000) && got != float64(1000) && got != 1000 {
		t.Fatalf("max_tokens default = %#v", got)
	}
	saved, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(saved), "required") {
		t.Fatalf("updated config was not persisted:\n%s", saved)
	}
}

func TestAdminReloadConfigReloadsFromDisk(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(cfgPath, []byte(`
model_list:
  - model_name: alpha
    protected: false
    model_params:
      api_base: http://alpha.test/v1
      api_key: key
      model: openai/upstream-alpha
`), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadWithOptions(config.LoadOptions{Path: cfgPath, SubstituteEnv: false})
	if err != nil {
		t.Fatal(err)
	}
	app, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfgPath, []byte(`
model_list:
  - model_name: alpha
    protected: false
    model_params:
      api_base: http://alpha.test/v1
      api_key: key
      model: openai/upstream-alpha
    parameters:
      temperature:
        default: 0.2
        allow_override: false
`), 0o600); err != nil {
		t.Fatal(err)
	}
	resp := httptest.NewRecorder()
	app.ServeHTTP(resp, httptest.NewRequest(http.MethodPost, "/admin/config/reload", nil))
	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", resp.Code, resp.Body.String())
	}
	param := app.Router.Backends["alpha"].Parameters["temperature"]
	if param.Default != 0.2 || param.AllowOverride {
		t.Fatalf("temperature parameter = %#v", param)
	}
}

func TestApplyConfigDoesNotWaitForInFlightRequest(t *testing.T) {
	requestStarted := make(chan struct{})
	releaseRequest := make(chan struct{})
	release := func() {
		select {
		case <-releaseRequest:
		default:
			close(releaseRequest)
		}
	}
	defer release()
	oldUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(requestStarted)
		<-releaseRequest
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"old"}}]}`))
	}))
	defer oldUpstream.Close()
	newUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"new"}}]}`))
	}))
	defer newUpstream.Close()
	app := newTestApp(t, oldUpstream.URL+"/v1")

	requestDone := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		requestDone <- postChat(app, "")
	}()
	select {
	case <-requestStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("request did not reach upstream")
	}

	reloadDone := make(chan error, 1)
	go func() {
		reloadDone <- app.applyConfig(&config.Config{ModelList: []config.ModelEntry{{
			ModelName: "alpha",
			ModelParams: map[string]any{
				"api_base": newUpstream.URL + "/v1",
				"model":    "openai/upstream",
			},
		}}}, false)
	}()
	select {
	case err := <-reloadDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("reload blocked behind an in-flight request")
	}

	// A request arriving after the swap must not queue behind the old one.
	newResp := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		newResp <- postChat(app, "")
	}()
	select {
	case resp := <-newResp:
		if resp.Code != http.StatusOK || !strings.Contains(resp.Body.String(), "new") {
			t.Fatalf("new request status = %d body=%s", resp.Code, resp.Body.String())
		}
	case <-time.After(2 * time.Second):
		t.Fatal("new request stalled behind the in-flight request")
	}

	release()
	select {
	case resp := <-requestDone:
		if resp.Code != http.StatusOK || !strings.Contains(resp.Body.String(), "old") {
			t.Fatalf("in-flight request status = %d body=%s", resp.Code, resp.Body.String())
		}
	case <-time.After(2 * time.Second):
		t.Fatal("in-flight request did not finish")
	}
}

func TestRuntimeGenerationRetiresAfterLastRelease(t *testing.T) {
	var cleaned atomic.Int32
	g := &runtimeGeneration{}
	g.acquire()
	g.acquire()
	g.retire(func() { cleaned.Add(1) })
	g.release()
	if cleaned.Load() != 0 {
		t.Fatal("retired generation cleaned up while a request still held it")
	}
	g.release()
	if cleaned.Load() != 1 {
		t.Fatalf("cleanup count = %d after last release, want 1", cleaned.Load())
	}

	idle := &runtimeGeneration{}
	idle.retire(func() { cleaned.Add(1) })
	if cleaned.Load() != 2 {
		t.Fatal("idle generation did not clean up immediately")
	}
}

func TestApplyConfigRejectsRuntimeView(t *testing.T) {
	app := newTestApp(t, "http://upstream.test/v1")
	view, release := app.acquireRuntime()
	defer release()
	if err := view.applyConfigLocked(app.Config, false); err == nil {
		t.Fatal("expected applying config to a runtime view to fail")
	}
}

func TestServerNewFailsOnInvalidLogDatabase(t *testing.T) {
	blocker := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := New(&config.Config{
		ModelList: []config.ModelEntry{{
			ModelName: "alpha",
			ModelParams: map[string]any{
				"api_base": "http://upstream.test/v1",
				"model":    "openai/upstream",
			},
		}},
		Database: map[string]any{
			"backend": "sqlite",
			"connection": map[string]any{
				"sqlite": map[string]any{"path": filepath.Join(blocker, "logs.db")},
			},
		},
	})
	if err == nil {
		t.Fatal("expected log database startup error")
	}
}

func TestApplyConfigRejectsInvalidLogDatabaseWithoutMutation(t *testing.T) {
	dir := t.TempDir()
	oldCfg := &config.Config{
		ModelList: []config.ModelEntry{{
			ModelName: "alpha",
			ModelParams: map[string]any{
				"api_base": "http://alpha.test/v1",
				"model":    "openai/alpha",
			},
		}},
		Database: map[string]any{
			"backend": "sqlite",
			"connection": map[string]any{
				"sqlite": map[string]any{"path": filepath.Join(dir, "old.db")},
			},
		},
	}
	app, err := New(oldCfg)
	if err != nil {
		t.Fatal(err)
	}
	defer app.LogDB.Close()
	oldLogDB := app.LogDB

	blocker := filepath.Join(dir, "not-a-dir")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	newCfg := &config.Config{
		ModelList: []config.ModelEntry{{
			ModelName: "beta",
			ModelParams: map[string]any{
				"api_base": "http://beta.test/v1",
				"model":    "openai/beta",
			},
		}},
		Database: map[string]any{
			"backend": "sqlite",
			"connection": map[string]any{
				"sqlite": map[string]any{"path": filepath.Join(blocker, "logs.db")},
			},
		},
	}
	if err := app.applyConfig(newCfg, false); err == nil {
		t.Fatal("expected applyConfig log database error")
	}
	if app.Config != oldCfg {
		t.Fatalf("config mutated to %#v", app.Config)
	}
	if app.LogDB != oldLogDB || app.LogDB == nil || !app.LogDB.Enabled() {
		t.Fatalf("log database mutated: old=%p current=%p", oldLogDB, app.LogDB)
	}
	if _, ok := app.Router.Backends["alpha"]; !ok {
		t.Fatalf("old router backend missing: %#v", app.Router.Backends)
	}
	if _, ok := app.Router.Backends["beta"]; ok {
		t.Fatalf("new router backend applied after failed database open: %#v", app.Router.Backends)
	}
}

func TestAdminRegisterPersistsLoadedConfig(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(cfgPath, []byte(`
model_list:
  - model_name: alpha
    protected: false
    model_params:
      api_base: http://alpha.test/v1
      model: openai/upstream-alpha
`), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadWithOptions(config.LoadOptions{Path: cfgPath, SubstituteEnv: false})
	if err != nil {
		t.Fatal(err)
	}
	app, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	resp := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/admin/models", strings.NewReader(`{"model_name":"beta","api_base":"http://beta.test/v1","api_key":"k","fallbacks":["alpha"]}`))
	app.ServeHTTP(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", resp.Code, resp.Body.String())
	}
	saved, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(saved), "model_name: beta") || !strings.Contains(string(saved), "beta:") {
		t.Fatalf("saved config missing beta model/fallback:\n%s", saved)
	}
}

func TestAdminRegisterRejectsProtectedWithoutPassword(t *testing.T) {
	app := newTestApp(t, "http://upstream.test/v1")
	app.Config.ModelList[0].Protected = true
	t.Setenv(config.AdminPasswordEnv, "pw")
	resp := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/admin/models", strings.NewReader(`{"model_name":"alpha","api_base":"http://new.test/v1"}`))
	app.ServeHTTP(resp, req)
	if resp.Code != http.StatusForbidden {
		t.Fatalf("status = %d body=%s", resp.Code, resp.Body.String())
	}
	resp = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/admin/models", strings.NewReader(`{"model_name":"alpha","api_base":"http://new.test/v1"}`))
	req.Header.Set("x-admin-password", "pw")
	app.ServeHTTP(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("status with password = %d body=%s", resp.Code, resp.Body.String())
	}
}

func TestPassthroughRoute(t *testing.T) {
	var gotPath string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.String()
		_, _ = w.Write([]byte("ok"))
	}))
	defer upstream.Close()
	app, err := New(&config.Config{ModelList: []config.ModelEntry{{
		ModelName: "pass",
		ModelParams: map[string]any{
			"api_base": upstream.URL,
			"api_type": "passthrough",
		},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	resp := httptest.NewRecorder()
	app.ServeHTTP(resp, httptest.NewRequest(http.MethodPost, "/p1/pass/v2/score?x=1", strings.NewReader(`{}`)))
	if resp.Code != http.StatusOK || resp.Body.String() != "ok" {
		t.Fatalf("status=%d body=%s", resp.Code, resp.Body.String())
	}
	if gotPath != "/v2/score?x=1" {
		t.Fatalf("upstream path = %s", gotPath)
	}
}

func TestPassthroughEnforcesAppKeyAndDoesNotLeakProxyCredential(t *testing.T) {
	var gotAuthorization string
	var gotAppKey string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuthorization = r.Header.Get("Authorization")
		gotAppKey = r.Header.Get("x-api-key")
		_, _ = w.Write([]byte("ok"))
	}))
	defer upstream.Close()
	enabled := true
	app, err := New(&config.Config{
		ModelList: []config.ModelEntry{{
			ModelName: "pass",
			ModelParams: map[string]any{
				"api_base": upstream.URL,
				"api_key":  "upstream-secret",
				"api_type": "passthrough",
			},
		}},
		AppKeys: config.AppKeysConfig{
			Enabled: true,
			Keys: []config.AppKeyEntry{{
				KeyID: "client", Secret: "client-secret", Enabled: &enabled, Models: []string{"pass"},
				Limits: config.LimitsConfig{RPS: 1},
			}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	blocked := httptest.NewRecorder()
	app.ServeHTTP(blocked, httptest.NewRequest(http.MethodPost, "/p1/pass/v2/score", strings.NewReader(`{}`)))
	if blocked.Code != http.StatusUnauthorized {
		t.Fatalf("missing app key status = %d body=%s", blocked.Code, blocked.Body.String())
	}

	allowed := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/p1/pass/v2/score", strings.NewReader(`{}`))
	req.Header.Set("x-api-key", "client-secret")
	app.ServeHTTP(allowed, req)
	if allowed.Code != http.StatusOK || allowed.Body.String() != "ok" {
		t.Fatalf("authorized status = %d body=%s", allowed.Code, allowed.Body.String())
	}
	if gotAuthorization != "Bearer upstream-secret" {
		t.Fatalf("upstream authorization = %q", gotAuthorization)
	}
	if gotAppKey != "" {
		t.Fatalf("proxy app key leaked upstream: %q", gotAppKey)
	}

	rateLimited := httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/p1/pass/v2/score", strings.NewReader(`{}`))
	req.Header.Set("x-api-key", "client-secret")
	app.ServeHTTP(rateLimited, req)
	if rateLimited.Code != http.StatusTooManyRequests {
		t.Fatalf("second passthrough status = %d body=%s", rateLimited.Code, rateLimited.Body.String())
	}
}

func TestUsageEndpoint(t *testing.T) {
	app := newTestApp(t, "http://upstream.test/v1")
	resp := httptest.NewRecorder()
	app.ServeHTTP(resp, httptest.NewRequest(http.MethodGet, "/v1/usage", nil))
	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", resp.Code, resp.Body.String())
	}
}

func TestHealthEndpointReportsBackends(t *testing.T) {
	app := newTestApp(t, "http://upstream.test/v1")
	releaseInflight := app.Router.TrackBackendAttempt("alpha")
	defer releaseInflight()
	resp := httptest.NewRecorder()
	app.ServeHTTP(resp, httptest.NewRequest(http.MethodGet, "/v1/health", nil))
	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", resp.Code, resp.Body.String())
	}
	var payload map[string]any
	if err := json.Unmarshal(resp.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	data := payload["data"].([]any)
	if len(data) != 1 {
		t.Fatalf("data = %#v", data)
	}
	health := data[0].(map[string]any)
	if health["name"] != "alpha" || health["healthy"] != true {
		t.Fatalf("health = %#v", health)
	}
	if health["inflight_requests"] != float64(1) {
		t.Fatalf("health inflight_requests = %#v, want 1", health["inflight_requests"])
	}
	if _, ok := health["state"]; ok {
		t.Fatalf("default health exposed recovery state: %#v", health)
	}
}

func TestHealthEndpointReportsHalfOpenStateWhenConfigured(t *testing.T) {
	app := newTestApp(t, "http://upstream.test/v1")
	app.Router.RecoverySuccessThreshold = 1
	app.Router.Health["alpha"] = proxy.BackendHealth{
		Name:                     "alpha",
		Healthy:                  false,
		State:                    "half_open",
		Score:                    0.5,
		RecoverySuccessThreshold: 1,
		LastError:                "raw upstream error with tenant-secret-value",
	}

	resp := httptest.NewRecorder()
	app.ServeHTTP(resp, httptest.NewRequest(http.MethodGet, "/v1/health", nil))
	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", resp.Code, resp.Body.String())
	}
	var payload map[string]any
	if err := json.Unmarshal(resp.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	data := payload["data"].([]any)
	if len(data) != 1 {
		t.Fatalf("data = %#v", data)
	}
	health := data[0].(map[string]any)
	if health["state"] != "half_open" || health["recovery_success_threshold"] != float64(1) {
		t.Fatalf("health = %#v", health)
	}
	if _, ok := health["last_error"]; ok {
		t.Fatalf("health exposed raw upstream error: %#v", health)
	}
	if strings.Contains(resp.Body.String(), "tenant-secret-value") {
		t.Fatalf("health leaked raw upstream error: %s", resp.Body.String())
	}
}

func TestAdminFrontendServesBuiltAssetsWithoutShadowingAPIs(t *testing.T) {
	dist := t.TempDir()
	oldDist := adminDistDir
	adminDistDir = dist
	defer func() { adminDistDir = oldDist }()
	if err := os.MkdirAll(filepath.Join(dist, "assets"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dist, "index.html"), []byte(`<div id="root">admin ui</div>`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dist, "assets", "app.js"), []byte(`console.log("admin")`), 0o644); err != nil {
		t.Fatal(err)
	}
	app := newTestApp(t, "http://upstream.test/v1")

	resp := httptest.NewRecorder()
	app.ServeHTTP(resp, httptest.NewRequest(http.MethodGet, "/admin", nil))
	if resp.Code != http.StatusOK || !strings.Contains(resp.Body.String(), "admin ui") {
		t.Fatalf("admin status=%d body=%s", resp.Code, resp.Body.String())
	}

	resp = httptest.NewRecorder()
	app.ServeHTTP(resp, httptest.NewRequest(http.MethodGet, "/admin/assets/app.js", nil))
	if resp.Code != http.StatusOK || !strings.Contains(resp.Body.String(), `console.log("admin")`) {
		t.Fatalf("asset status=%d body=%s", resp.Code, resp.Body.String())
	}

	resp = httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/admin/models", nil)
	req.Header.Set("Accept", "text/html,application/xhtml+xml")
	app.ServeHTTP(resp, req)
	if resp.Code != http.StatusOK || !strings.Contains(resp.Body.String(), "admin ui") {
		t.Fatalf("models page status=%d body=%s", resp.Code, resp.Body.String())
	}

	resp = httptest.NewRecorder()
	app.ServeHTTP(resp, httptest.NewRequest(http.MethodGet, "/admin/models", nil))
	if resp.Code != http.StatusOK || strings.Contains(resp.Body.String(), "admin ui") || !strings.Contains(resp.Body.String(), `"protected"`) {
		t.Fatalf("models api status=%d body=%s", resp.Code, resp.Body.String())
	}

	resp = httptest.NewRecorder()
	app.ServeHTTP(resp, httptest.NewRequest(http.MethodGet, "/admin/config", nil))
	if resp.Code != http.StatusOK || strings.Contains(resp.Body.String(), "admin ui") {
		t.Fatalf("config status=%d body=%s", resp.Code, resp.Body.String())
	}
}

func TestAdminFrontendFallsBackToEmbeddedProductionBundle(t *testing.T) {
	oldDist := adminDistDir
	adminDistDir = filepath.Join(t.TempDir(), "missing-dist")
	defer func() { adminDistDir = oldDist }()

	app := newTestApp(t, "http://upstream.test/v1")
	for _, requestPath := range []string{"/admin", "/admin/models"} {
		resp := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, requestPath, nil)
		req.Header.Set("Accept", "text/html")
		app.ServeHTTP(resp, req)
		if resp.Code != http.StatusOK || !strings.Contains(resp.Body.String(), `id="root"`) {
			t.Fatalf("embedded %s status=%d body=%s", requestPath, resp.Code, resp.Body.String())
		}
	}
}

func TestFullRequestStorageWritesPayloadAndLinksLog(t *testing.T) {
	payloadDir := t.TempDir()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"stored"}}]}`))
	}))
	defer upstream.Close()
	app, err := New(&config.Config{
		ModelList: []config.ModelEntry{{
			ModelName: "alpha",
			ModelParams: map[string]any{
				"api_base": upstream.URL + "/v1",
				"model":    "openai/upstream",
			},
		}},
		ProxySettings: config.ProxySettings{
			Logging: config.LoggingConfig{
				FullRequestStore: map[string]any{
					"enabled":         true,
					"path":            payloadDir,
					"retention_hours": 24,
				},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	resp := postChat(app, "")
	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", resp.Code, resp.Body.String())
	}
	logs := app.Logs.List()
	if len(logs) != 1 || logs[0].PayloadPath == "" {
		t.Fatalf("logs = %#v", logs)
	}
	if filepath.Dir(logs[0].PayloadPath) != payloadDir {
		t.Fatalf("payload path = %s", logs[0].PayloadPath)
	}
	raw, err := os.ReadFile(logs[0].PayloadPath)
	if err != nil {
		t.Fatal(err)
	}
	var record map[string]any
	if err := json.Unmarshal(raw, &record); err != nil {
		t.Fatal(err)
	}
	requestBody, err := base64.StdEncoding.DecodeString(record["request_body_base64"].(string))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(requestBody), `"model":"alpha"`) {
		t.Fatalf("request body = %s", requestBody)
	}
	responseBody, err := base64.StdEncoding.DecodeString(record["response_body_base64"].(string))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(responseBody), "stored") {
		t.Fatalf("response body = %s", responseBody)
	}
}

func TestParsedResponseSidecarCapturesRawAndTransformedBody(t *testing.T) {
	payloadDir := t.TempDir()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"index":0,"finish_reason":"stop","message":{"role":"assistant","content":"<think>hidden</think>visible"}}]}`))
	}))
	defer upstream.Close()
	app, err := New(&config.Config{
		ModelList: []config.ModelEntry{{
			ModelName: "alpha",
			ModelParams: map[string]any{
				"api_base": upstream.URL + "/v1",
				"model":    "openai/upstream",
			},
		}},
		ProxySettings: config.ProxySettings{
			Logging: config.LoggingConfig{
				LogToDisk:         true,
				LogParsedResponse: true,
				FullRequestStore: map[string]any{
					"enabled": true,
					"path":    payloadDir,
				},
			},
			Modules: map[string]any{
				"enabled":  true,
				"response": []any{"parse_tags"},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	resp := postChat(app, "")
	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", resp.Code, resp.Body.String())
	}
	if strings.Contains(resp.Body.String(), "<think>") || !strings.Contains(resp.Body.String(), "reasoning_content") {
		t.Fatalf("client response was not transformed: %s", resp.Body.String())
	}
	logs := app.Logs.List()
	if len(logs) != 1 || logs[0].PayloadPath == "" {
		t.Fatalf("logs = %#v", logs)
	}
	record, err := logstore.ReadPayload(logs[0].PayloadPath)
	if err != nil {
		t.Fatal(err)
	}
	if record.ParsedResponse == nil {
		t.Fatalf("parsed response sidecar missing: %#v", record)
	}
	rawBody, err := logstore.DecodeBody(record.ParsedResponse.RawUpstreamResponseBodyBase64)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(rawBody), "<think>hidden</think>visible") {
		t.Fatalf("raw upstream body = %s", rawBody)
	}
	transformedBody, err := logstore.DecodeBody(record.ParsedResponse.TransformedClientResponseBodyBase64)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(transformedBody), "<think>") || !strings.Contains(string(transformedBody), `"reasoning_content":"hidden"`) {
		t.Fatalf("transformed client body = %s", transformedBody)
	}
	detail := app.memoryLogDetail(logs[0], 100000)
	parsedDetail, _ := detail["parsed_response"].(map[string]any)
	if parsedDetail == nil {
		t.Fatalf("admin detail missing parsed_response: %#v", detail)
	}
	if !strings.Contains(parsedDetail["raw_upstream_response_text"].(string), "<think>hidden</think>visible") {
		t.Fatalf("raw detail = %#v", parsedDetail)
	}
	if strings.Contains(parsedDetail["transformed_client_response_text"].(string), "<think>") {
		t.Fatalf("transformed detail = %#v", parsedDetail)
	}
}

func TestParsedResponseSidecarRequiresParsedLoggingGate(t *testing.T) {
	payloadDir := t.TempDir()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"<think>hidden</think>visible"}}]}`))
	}))
	defer upstream.Close()
	app, err := New(&config.Config{
		ModelList: []config.ModelEntry{{
			ModelName: "alpha",
			ModelParams: map[string]any{
				"api_base": upstream.URL + "/v1",
				"model":    "openai/upstream",
			},
		}},
		ProxySettings: config.ProxySettings{
			Logging: config.LoggingConfig{
				FullRequestStore: map[string]any{
					"enabled": true,
					"path":    payloadDir,
				},
			},
			Modules: map[string]any{
				"enabled":  true,
				"response": []any{"parse_tags"},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	resp := postChat(app, "")
	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", resp.Code, resp.Body.String())
	}
	if strings.Contains(resp.Body.String(), "<think>") {
		t.Fatalf("client response was not transformed: %s", resp.Body.String())
	}
	logs := app.Logs.List()
	if len(logs) != 1 || logs[0].PayloadPath == "" {
		t.Fatalf("logs = %#v", logs)
	}
	record, err := logstore.ReadPayload(logs[0].PayloadPath)
	if err != nil {
		t.Fatal(err)
	}
	if record.ParsedResponse != nil {
		t.Fatalf("parsed response sidecar should be gated off: %#v", record.ParsedResponse)
	}
}

func TestParsedResponseSidecarRejectsMalformedLoggingGate(t *testing.T) {
	payloadDir := t.TempDir()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"<think>hidden</think>visible"}}]}`))
	}))
	defer upstream.Close()
	app, err := New(&config.Config{
		ModelList: []config.ModelEntry{{
			ModelName: "alpha",
			ModelParams: map[string]any{
				"api_base": upstream.URL + "/v1",
				"model":    "openai/upstream",
			},
		}},
		ProxySettings: config.ProxySettings{
			Logging: config.LoggingConfig{
				LogToDisk:         []any{"true"},
				LogParsedResponse: map[string]any{"enabled": true},
				FullRequestStore: map[string]any{
					"enabled": []any{"true"},
					"path":    payloadDir,
				},
			},
			Modules: map[string]any{
				"enabled":  true,
				"response": []any{"parse_tags"},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	resp := postChat(app, "")
	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", resp.Code, resp.Body.String())
	}
	if strings.Contains(resp.Body.String(), "<think>") {
		t.Fatalf("client response was not transformed: %s", resp.Body.String())
	}
	logs := app.Logs.List()
	if len(logs) != 1 || logs[0].PayloadPath == "" {
		t.Fatalf("logs = %#v", logs)
	}
	record, err := logstore.ReadPayload(logs[0].PayloadPath)
	if err != nil {
		t.Fatal(err)
	}
	if record.ParsedResponse != nil {
		t.Fatalf("malformed parsed response gate enabled sidecar: %#v", record.ParsedResponse)
	}
}

func TestParsedStreamSidecarCapturesRawAndTransformedStream(t *testing.T) {
	payloadDir := t.TempDir()
	app := newParsedStreamSidecarApp(t, payloadDir, true, parsedStreamFixture())
	resp := postStreamChat(app)
	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", resp.Code, resp.Body.String())
	}
	if strings.Contains(resp.Body.String(), "<think>") || !strings.Contains(resp.Body.String(), "reasoning_content") {
		t.Fatalf("client stream was not transformed: %s", resp.Body.String())
	}
	logs := app.Logs.List()
	if len(logs) != 1 || logs[0].PayloadPath == "" {
		t.Fatalf("logs = %#v", logs)
	}
	record, err := logstore.ReadPayload(logs[0].PayloadPath)
	if err != nil {
		t.Fatal(err)
	}
	if record.ParsedStream == nil {
		t.Fatalf("parsed stream sidecar missing: %#v", record)
	}
	raw, err := os.ReadFile(record.ParsedStream.RawUpstreamResponsePath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "<think>hidden</think>visible") {
		t.Fatalf("raw stream sidecar = %s", raw)
	}
	transformed, err := os.ReadFile(record.ParsedStream.TransformedClientResponsePath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(transformed), "<think>") || !strings.Contains(string(transformed), "reasoning_content") {
		t.Fatalf("transformed stream sidecar = %s", transformed)
	}
	detail := app.memoryLogDetail(logs[0], 100000)
	parsedDetail, _ := detail["parsed_stream"].(map[string]any)
	if parsedDetail == nil {
		t.Fatalf("admin detail missing parsed_stream: %#v", detail)
	}
	if !strings.Contains(parsedDetail["raw_upstream_response_text"].(string), "<think>hidden</think>visible") {
		t.Fatalf("raw detail = %#v", parsedDetail)
	}
	if strings.Contains(parsedDetail["transformed_client_response_text"].(string), "<think>") {
		t.Fatalf("transformed detail = %#v", parsedDetail)
	}
}

func TestParsedStreamSidecarRequiresStrictGate(t *testing.T) {
	payloadDir := t.TempDir()
	app := newParsedStreamSidecarApp(t, payloadDir, false, parsedStreamFixture())
	resp := postStreamChat(app)
	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", resp.Code, resp.Body.String())
	}
	if strings.Contains(resp.Body.String(), "<think>") {
		t.Fatalf("client stream was not transformed: %s", resp.Body.String())
	}
	logs := app.Logs.List()
	if len(logs) != 1 || logs[0].PayloadPath == "" {
		t.Fatalf("logs = %#v", logs)
	}
	record, err := logstore.ReadPayload(logs[0].PayloadPath)
	if err != nil {
		t.Fatal(err)
	}
	if record.ParsedStream != nil {
		t.Fatalf("parsed stream sidecar should be gated off: %#v", record.ParsedStream)
	}
}

func TestParsedStreamSidecarCapTruncatesSidecarsOnly(t *testing.T) {
	oldCap := parsedStreamSidecarMaxBytes
	parsedStreamSidecarMaxBytes = 48
	defer func() { parsedStreamSidecarMaxBytes = oldCap }()

	payloadDir := t.TempDir()
	app := newParsedStreamSidecarApp(t, payloadDir, true, parsedStreamFixture())
	resp := postStreamChat(app)
	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", resp.Code, resp.Body.String())
	}
	if strings.Contains(resp.Body.String(), "<think>") || !strings.Contains(resp.Body.String(), "visible") {
		t.Fatalf("client stream changed after sidecar cap: %s", resp.Body.String())
	}
	logs := app.Logs.List()
	if len(logs) != 1 || logs[0].PayloadPath == "" {
		t.Fatalf("logs = %#v", logs)
	}
	record, err := logstore.ReadPayload(logs[0].PayloadPath)
	if err != nil {
		t.Fatal(err)
	}
	if record.ParsedStream == nil || !record.ParsedStream.Truncated {
		t.Fatalf("parsed stream truncation missing: %#v", record.ParsedStream)
	}
	if record.ParsedStream.RawUpstreamResponseBytes != 48 || record.ParsedStream.TransformedClientResponseBytes != 48 {
		t.Fatalf("parsed stream byte counts = %#v", record.ParsedStream)
	}
	detail := app.memoryLogDetail(logs[0], 16)
	parsedDetail, _ := detail["parsed_stream"].(map[string]any)
	if parsedDetail == nil || parsedDetail["truncated"] != true || parsedDetail["raw_upstream_response_text_truncated"] != true {
		t.Fatalf("admin detail truncation = %#v", parsedDetail)
	}
}

func TestParsedStreamSidecarDoesNotChangeClientVisibleStream(t *testing.T) {
	enabled := newParsedStreamSidecarApp(t, t.TempDir(), true, parsedStreamFixture())
	disabled := newParsedStreamSidecarApp(t, t.TempDir(), false, parsedStreamFixture())
	enabledResp := postStreamChat(enabled)
	disabledResp := postStreamChat(disabled)
	if enabledResp.Code != disabledResp.Code {
		t.Fatalf("status enabled=%d disabled=%d", enabledResp.Code, disabledResp.Code)
	}
	if enabledResp.Body.String() != disabledResp.Body.String() {
		t.Fatalf("stream changed with sidecar enabled:\nenabled:  %s\ndisabled: %s", enabledResp.Body.String(), disabledResp.Body.String())
	}
}

func TestStreamJSONRepairModuleRepairsToolArgumentsWhenEnabled(t *testing.T) {
	stream := strings.Join([]string{
		`data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"lookup","arguments":"{\"query\""}}]}}]}`,
		``,
		`data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":":\"cats\","}}]}}]}`,
		``,
		`data: {"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
		``,
		`data: [DONE]`,
		``,
		``,
	}, "\n")
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(stream))
	}))
	defer upstream.Close()
	app, err := New(&config.Config{ModelList: []config.ModelEntry{{
		ModelName: "alpha",
		ModelParams: map[string]any{
			"api_base": upstream.URL + "/v1",
			"model":    "openai/upstream",
		},
		Modules: map[string]any{
			"enabled":  true,
			"response": []any{"stream_json_repair"},
			"stream_json_repair": map[string]any{
				"enabled": true,
			},
		},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	resp := postStreamChat(app)
	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", resp.Code, resp.Body.String())
	}
	args := collectStreamToolArgumentsFromBody(t, resp.Body.String())
	if args != `{"query":"cats"}` {
		t.Fatalf("arguments = %q body=%s", args, resp.Body.String())
	}
	if !json.Valid([]byte(args)) {
		t.Fatalf("arguments are not valid JSON: %q", args)
	}
}

func TestRequiredHeaderLogAsStoredInFullRequestPayload(t *testing.T) {
	payloadDir := t.TempDir()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"stored"}}]}`))
	}))
	defer upstream.Close()
	enabled := true
	app, err := New(&config.Config{
		ModelList: []config.ModelEntry{{
			ModelName: "alpha",
			ModelParams: map[string]any{
				"api_base": upstream.URL + "/v1",
				"model":    "openai/upstream",
			},
		}},
		ProxySettings: config.ProxySettings{
			Logging: config.LoggingConfig{
				FullRequestStore: map[string]any{
					"enabled": true,
					"path":    payloadDir,
				},
			},
		},
		AppKeys: config.AppKeysConfig{
			Enabled: true,
			RequiredHeaders: []config.RequiredHeader{{
				Name:  "X-Tenant-ID",
				LogAs: "tenant",
			}},
			Keys: []config.AppKeyEntry{{
				KeyID: "k1", Secret: "secret", Enabled: &enabled, Models: []string{"alpha"},
			}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	resp := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"alpha","messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set("X-Tenant-ID", "acme")
	app.ServeHTTP(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", resp.Code, resp.Body.String())
	}
	logs := app.Logs.List()
	if len(logs) != 1 || logs[0].PayloadPath == "" {
		t.Fatalf("logs = %#v", logs)
	}
	raw, err := os.ReadFile(logs[0].PayloadPath)
	if err != nil {
		t.Fatal(err)
	}
	var record map[string]any
	if err := json.Unmarshal(raw, &record); err != nil {
		t.Fatal(err)
	}
	requiredHeaders := record["required_headers"].(map[string]any)
	if requiredHeaders["tenant"] != "acme" {
		t.Fatalf("required header attribution = %#v", requiredHeaders)
	}
	detail := app.memoryLogDetail(logs[0], 1000)
	detailRequiredHeaders := detail["required_headers"].(map[string]string)
	if detailRequiredHeaders["tenant"] != "acme" {
		t.Fatalf("detail required header attribution = %#v", detailRequiredHeaders)
	}
}

func TestExactCacheIncludesRequiredHeaderAttributionScope(t *testing.T) {
	var upstreamHits int
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamHits++
		tenant := r.Header.Get("X-Tenant-ID")
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id":      "tenant-" + tenant + "-" + strconv.Itoa(upstreamHits),
			"choices": []any{map[string]any{"message": map[string]any{"content": "tenant:" + tenant}}},
		})
	}))
	defer upstream.Close()
	enabled := true
	app, err := New(&config.Config{
		ModelList: []config.ModelEntry{{
			ModelName: "alpha",
			ModelParams: map[string]any{
				"api_base": upstream.URL + "/v1",
				"model":    "openai/upstream",
			},
		}},
		ProxySettings: config.ProxySettings{
			InferenceCache: config.InferenceCacheConfig{
				Mode:          "read_write",
				TTLSeconds:    60,
				MaxAgeSeconds: 60,
				MaxEntries:    16,
			},
		},
		AppKeys: config.AppKeysConfig{
			Enabled: true,
			RequiredHeaders: []config.RequiredHeader{{
				Name:  "X-Tenant-ID",
				LogAs: "tenant",
			}},
			Keys: []config.AppKeyEntry{{
				KeyID: "k1", Secret: "secret", Enabled: &enabled, Models: []string{"alpha"},
			}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	postTenant := func(tenant string) string {
		t.Helper()
		resp := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"alpha","messages":[{"role":"user","content":"same body"}]}`))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer secret")
		req.Header.Set("X-Tenant-ID", tenant)
		app.ServeHTTP(resp, req)
		if resp.Code != http.StatusOK {
			t.Fatalf("tenant %s status = %d body=%s", tenant, resp.Code, resp.Body.String())
		}
		return resp.Body.String()
	}

	acmeFirst := postTenant("acme")
	globex := postTenant("globex")
	acmeSecond := postTenant("acme")
	if !strings.Contains(acmeFirst, "tenant:acme") {
		t.Fatalf("acme response = %s", acmeFirst)
	}
	if !strings.Contains(globex, "tenant:globex") {
		t.Fatalf("globex response = %s", globex)
	}
	if acmeSecond != acmeFirst {
		t.Fatalf("acme cache hit body changed: first=%s second=%s", acmeFirst, acmeSecond)
	}
	if upstreamHits != 2 {
		t.Fatalf("upstream hits = %d, want 2", upstreamHits)
	}
}

func TestSemanticInferenceCacheHitMissBypassAndAdminSnapshot(t *testing.T) {
	t.Setenv(config.AdminPasswordEnv, "pw")
	var chatHits int
	chat := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		chatHits++
		if r.Header.Get("x-yallmp-cache-bypass") != "" {
			t.Fatalf("bypass header leaked upstream: %#v", r.Header)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id":      "chat-" + strconv.Itoa(chatHits),
			"choices": []any{map[string]any{"message": map[string]any{"role": "assistant", "content": "semantic-secret-response-" + strconv.Itoa(chatHits)}}},
			"usage":   map[string]any{"prompt_tokens": 1, "completion_tokens": 1, "total_tokens": 2},
		})
	}))
	defer chat.Close()
	var embedHits int
	embed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		embedHits++
		if r.Header.Get("x-yallmp-cache-bypass") != "" {
			t.Fatalf("bypass header leaked to embedding node: %#v", r.Header)
		}
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		input := fmt.Sprint(payload["input"])
		vector := []float64{0, 1}
		if strings.Contains(input, "semantic-secret-weather") || strings.Contains(input, "semantic-secret-climate") {
			vector = []float64{1, 0}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"data": []any{map[string]any{"embedding": vector}}})
	}))
	defer embed.Close()
	app := newSemanticCacheTestApp(t, chat.URL+"/v1", embed.URL+"/v1", "read_write", nil)

	first := postSemanticChat(t, app, "semantic-secret-weather", "", nil)
	second := postSemanticChat(t, app, "semantic-secret-climate", "", nil)
	if second != first {
		t.Fatalf("semantic hit body changed: first=%s second=%s", first, second)
	}
	if chatHits != 1 {
		t.Fatalf("chat hits after semantic hit = %d, want 1", chatHits)
	}
	third := postSemanticChat(t, app, "unrelated quota", "", nil)
	if third == first {
		t.Fatalf("unrelated prompt returned cached response: %s", third)
	}
	if chatHits != 2 {
		t.Fatalf("chat hits after unrelated prompt = %d, want 2", chatHits)
	}
	beforeBypassEmbeds := embedHits
	bypass := postSemanticChat(t, app, "semantic-secret-climate", "true", nil)
	if bypass == first {
		t.Fatalf("bypass response came from semantic cache: %s", bypass)
	}
	if chatHits != 3 {
		t.Fatalf("chat hits after bypass = %d, want 3", chatHits)
	}
	if embedHits != beforeBypassEmbeds {
		t.Fatalf("bypass performed embedding lookup/write: before=%d after=%d", beforeBypassEmbeds, embedHits)
	}
	again := postSemanticChat(t, app, "semantic-secret-climate", "", nil)
	if again != first {
		t.Fatalf("bypass overwrote semantic cache: first=%s again=%s", first, again)
	}
	if chatHits != 3 {
		t.Fatalf("chat hits after post-bypass semantic hit = %d, want 3", chatHits)
	}

	resp := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/admin/cache", nil)
	req.Header.Set("x-admin-password", "pw")
	app.ServeHTTP(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("cache stats status = %d body=%s", resp.Code, resp.Body.String())
	}
	var snapshot struct {
		Semantic struct {
			Enabled           bool   `json:"enabled"`
			Hits              uint64 `json:"hits"`
			Misses            uint64 `json:"misses"`
			Writes            uint64 `json:"writes"`
			ThresholdRejected uint64 `json:"threshold_rejected"`
			Entries           int    `json:"entries"`
		} `json:"semantic"`
	}
	if err := json.Unmarshal(resp.Body.Bytes(), &snapshot); err != nil {
		t.Fatal(err)
	}
	if !snapshot.Semantic.Enabled || snapshot.Semantic.Hits != 2 || snapshot.Semantic.Misses != 2 || snapshot.Semantic.Writes != 2 || snapshot.Semantic.Entries != 2 {
		t.Fatalf("semantic cache stats = %#v", snapshot.Semantic)
	}
	body := resp.Body.String()
	for _, leaked := range []string{"semantic-secret-weather", "semantic-secret-climate", "semantic-secret-response", chat.URL, embed.URL, "[1,0]"} {
		if strings.Contains(body, leaked) {
			t.Fatalf("semantic cache stats leaked %q: %s", leaked, body)
		}
	}
}

func TestSemanticInferenceCacheScopeIsolationAndEmbeddingFailure(t *testing.T) {
	var chatHits int
	chat := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		chatHits++
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id":      "chat-" + strconv.Itoa(chatHits),
			"choices": []any{map[string]any{"message": map[string]any{"role": "assistant", "content": "tenant-hit-" + strconv.Itoa(chatHits)}}},
		})
	}))
	defer chat.Close()
	embed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"data": []any{map[string]any{"embedding": []float64{1, 0}}}})
	}))
	defer embed.Close()
	enabled := true
	app := newSemanticCacheTestApp(t, chat.URL+"/v1", embed.URL+"/v1", "read_write", &config.AppKeysConfig{
		Enabled: true,
		RequiredHeaders: []config.RequiredHeader{{
			Name:  "X-Tenant-ID",
			LogAs: "tenant",
		}},
		Keys: []config.AppKeyEntry{{
			KeyID: "k1", Secret: "secret", Enabled: &enabled, Models: []string{"alpha"},
		}},
	})

	headersA := http.Header{"Authorization": {"Bearer secret"}, "X-Tenant-ID": {"acme"}}
	headersB := http.Header{"Authorization": {"Bearer secret"}, "X-Tenant-ID": {"globex"}}
	acmeFirst := postSemanticChat(t, app, "same semantic tenant prompt", "", headersA)
	globex := postSemanticChat(t, app, "similar semantic tenant prompt", "", headersB)
	acmeSecond := postSemanticChat(t, app, "similar semantic tenant prompt", "", headersA)
	if acmeFirst == globex {
		t.Fatalf("semantic cache crossed tenant scope: acme=%s globex=%s", acmeFirst, globex)
	}
	if acmeSecond != acmeFirst {
		t.Fatalf("semantic cache missed within tenant: first=%s second=%s", acmeFirst, acmeSecond)
	}
	if chatHits != 2 {
		t.Fatalf("tenant-scoped chat hits = %d, want 2", chatHits)
	}

	var failingChatHits int
	failingChat := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		failingChatHits++
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "fallback-" + strconv.Itoa(failingChatHits), "choices": []any{map[string]any{"message": map[string]any{"content": "fallback"}}}})
	}))
	defer failingChat.Close()
	failingEmbed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "embedding failed", http.StatusBadGateway)
	}))
	defer failingEmbed.Close()
	failingApp := newSemanticCacheTestApp(t, failingChat.URL+"/v1", failingEmbed.URL+"/v1", "read_write", nil)
	_ = postSemanticChat(t, failingApp, "embedding failure", "", nil)
	_ = postSemanticChat(t, failingApp, "embedding failure", "", nil)
	if failingChatHits != 1 {
		t.Fatalf("exact cache fallback hits = %d, want 1", failingChatHits)
	}
	snapshot := failingApp.Router.InferenceCache.Snapshot()
	if snapshot.Semantic.EmbeddingErrors == 0 || snapshot.Semantic.Writes != 0 {
		t.Fatalf("embedding failure semantic snapshot = %#v", snapshot.Semantic)
	}
}

func TestSemanticInferenceCacheMissUsesSingleSelectedRoute(t *testing.T) {
	var nodeAHits int
	nodeA := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		nodeAHits++
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id":      "node-a",
			"choices": []any{map[string]any{"message": map[string]any{"role": "assistant", "content": "node-a"}}},
		})
	}))
	defer nodeA.Close()
	var nodeBHits int
	nodeB := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		nodeBHits++
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id":      "node-b",
			"choices": []any{map[string]any{"message": map[string]any{"role": "assistant", "content": "node-b"}}},
		})
	}))
	defer nodeB.Close()
	embed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"data": []any{map[string]any{"embedding": []float64{1, 0}}}})
	}))
	defer embed.Close()

	app, err := New(&config.Config{
		Nodes: []config.NodeEntry{{
			Name:    "node-a",
			APIBase: nodeA.URL + "/v1",
			Model:   "upstream-a",
		}, {
			Name:    "node-b",
			APIBase: nodeB.URL + "/v1",
			Model:   "upstream-b",
		}, {
			Name:    "embed-node",
			APIBase: embed.URL + "/v1",
			Model:   "upstream-embed",
		}},
		Models: []config.ClientModelEntry{{
			Name:      "alpha",
			Nodes:     []string{"node-a", "node-b"},
			Balancing: "round_robin",
		}},
		ProxySettings: config.ProxySettings{
			InferenceCache: config.InferenceCacheConfig{
				Mode:          "read_write",
				TTLSeconds:    60,
				MaxAgeSeconds: 60,
				MaxEntries:    16,
				MaxEntryBytes: 4096,
				Semantic: config.SemanticCacheConfig{
					Enabled:             true,
					EmbeddingNode:       "embed-node",
					EmbeddingModel:      "text-embedding-test",
					SimilarityThreshold: 0.90,
					TopK:                4,
					MaxEntries:          16,
					MaxEntryBytes:       4096,
				},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	body := postSemanticChat(t, app, "round robin semantic miss", "", nil)
	if !strings.Contains(body, "node-a") {
		t.Fatalf("first response body = %s, want node-a", body)
	}
	if nodeAHits != 1 || nodeBHits != 0 {
		t.Fatalf("semantic miss route hits node-a=%d node-b=%d, want 1/0", nodeAHits, nodeBHits)
	}
}

func TestTranslatedMessagesAndResponsesExactCacheModes(t *testing.T) {
	for _, endpoint := range []struct {
		name string
		path string
		body string
		want string
	}{
		{
			name: "messages",
			path: "/v1/messages",
			body: `{"model":"alpha","max_tokens":16,"messages":[{"role":"user","content":"same translated cache body"}]}`,
			want: `"type":"message"`,
		},
		{
			name: "responses",
			path: "/v1/responses",
			body: `{"model":"alpha","input":"same translated cache body"}`,
			want: `"object":"response"`,
		},
	} {
		for _, tc := range []struct {
			mode     string
			wantHits int
			wantSnap InferenceCacheSnapshotWant
		}{
			{mode: "read_write", wantHits: 1, wantSnap: InferenceCacheSnapshotWant{Hits: 1, Misses: 1, Writes: 1, Entries: 1}},
			{mode: "read_only", wantHits: 2, wantSnap: InferenceCacheSnapshotWant{Hits: 0, Misses: 2, Writes: 0, Entries: 0}},
			{mode: "write_only", wantHits: 2, wantSnap: InferenceCacheSnapshotWant{Hits: 0, Misses: 0, Writes: 2, Entries: 1}},
			{mode: "off", wantHits: 2, wantSnap: InferenceCacheSnapshotWant{Enabled: false}},
		} {
			t.Run(endpoint.name+"/"+tc.mode, func(t *testing.T) {
				var upstreamHits int
				upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					upstreamHits++
					if r.URL.Path != "/v1/chat/completions" {
						t.Errorf("path = %s", r.URL.Path)
					}
					if r.Header.Get("x-yallmp-cache-bypass") != "" {
						t.Errorf("bypass header leaked upstream: %#v", r.Header)
					}
					w.Header().Set("Content-Type", "application/json")
					_ = json.NewEncoder(w).Encode(map[string]any{
						"id":     "chat-" + strconv.Itoa(upstreamHits),
						"object": "chat.completion",
						"model":  "upstream",
						"choices": []any{map[string]any{
							"index":         0,
							"finish_reason": "stop",
							"message":       map[string]any{"role": "assistant", "content": "translated-cache-" + strconv.Itoa(upstreamHits)},
						}},
						"usage": map[string]any{"prompt_tokens": 1, "completion_tokens": 1, "total_tokens": 2},
					})
				}))
				defer upstream.Close()
				app := newTranslatedCacheTestApp(t, upstream.URL+"/v1", tc.mode, nil)

				first := postTranslatedCacheRequest(t, app, endpoint.path, endpoint.body, "")
				second := postTranslatedCacheRequest(t, app, endpoint.path, endpoint.body, "")
				if tc.wantHits == 1 && first != second {
					t.Fatalf("cached translated bodies differed: first=%s second=%s", first, second)
				}
				if !strings.Contains(second, endpoint.want) || strings.Contains(second, "chat.completion") {
					t.Fatalf("translated cache returned wrong client body: %s", second)
				}
				if upstreamHits != tc.wantHits {
					t.Fatalf("upstream hits = %d, want %d", upstreamHits, tc.wantHits)
				}
				assertInferenceCacheSnapshot(t, app, tc.wantSnap)
			})
		}
	}
}

func TestTranslatedExactCacheBypassSkipsReadAndWrite(t *testing.T) {
	var upstreamHits int
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamHits++
		if r.Header.Get("x-yallmp-cache-bypass") != "" {
			t.Fatalf("bypass header leaked upstream: %#v", r.Header)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id":      "chat-" + strconv.Itoa(upstreamHits),
			"choices": []any{map[string]any{"message": map[string]any{"role": "assistant", "content": "hit-" + strconv.Itoa(upstreamHits)}, "finish_reason": "stop"}},
			"usage":   map[string]any{"prompt_tokens": 1, "completion_tokens": 1, "total_tokens": 2},
		})
	}))
	defer upstream.Close()
	app := newTranslatedCacheTestApp(t, upstream.URL+"/v1", "read_write", nil)
	body := `{"model":"alpha","max_tokens":16,"messages":[{"role":"user","content":"translated cache bypass"}]}`

	first := postTranslatedCacheRequest(t, app, "/v1/messages", body, "")
	bypass := postTranslatedCacheRequest(t, app, "/v1/messages", body, "true")
	third := postTranslatedCacheRequest(t, app, "/v1/messages", body, "")
	if first == bypass {
		t.Fatalf("bypass response came from cache: %s", bypass)
	}
	if third != first {
		t.Fatalf("bypass wrote cache entry: first=%s third=%s", first, third)
	}
	if upstreamHits != 2 {
		t.Fatalf("upstream hits = %d, want 2", upstreamHits)
	}
}

func TestNativeMessagesAndResponsesDoNotUseExactCache(t *testing.T) {
	for _, tc := range []struct {
		name        string
		path        string
		body        string
		modelParams map[string]any
	}{
		{
			name: "native messages",
			path: "/v1/messages",
			body: `{"model":"alpha","max_tokens":16,"messages":[{"role":"user","content":"native messages"}]}`,
			modelParams: map[string]any{
				"api_type":          "anthropic",
				"anthropic_version": "2023-06-01",
			},
		},
		{
			name: "native responses",
			path: "/v1/responses",
			body: `{"model":"alpha","input":"native responses"}`,
			modelParams: map[string]any{
				"supports_responses_api": true,
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var upstreamHits int
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				upstreamHits++
				w.Header().Set("Content-Type", "application/json")
				if tc.path == "/v1/messages" {
					if r.URL.Path != "/v1/messages" {
						t.Errorf("path = %s", r.URL.Path)
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"id": "msg-" + strconv.Itoa(upstreamHits), "type": "message", "role": "assistant", "content": []any{map[string]any{"type": "text", "text": "native"}}, "usage": map[string]any{"input_tokens": 1, "output_tokens": 1}})
					return
				}
				if r.URL.Path != "/v1/responses" {
					t.Errorf("path = %s", r.URL.Path)
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"id": "resp-" + strconv.Itoa(upstreamHits), "object": "response", "status": "completed", "model": "alpha", "output": []any{}, "usage": map[string]any{"total_tokens": 2}})
			}))
			defer upstream.Close()
			params := tc.modelParams
			params["api_base"] = upstream.URL + "/v1"
			app := newTranslatedCacheTestApp(t, upstream.URL+"/v1", "read_write", params)

			_ = postTranslatedCacheRequest(t, app, tc.path, tc.body, "")
			_ = postTranslatedCacheRequest(t, app, tc.path, tc.body, "")
			if upstreamHits != 2 {
				t.Fatalf("native upstream hits = %d, want 2", upstreamHits)
			}
			assertInferenceCacheSnapshot(t, app, InferenceCacheSnapshotWant{Hits: 0, Misses: 0, Writes: 0, Entries: 0})
		})
	}
}

func TestNativeMessagesAndResponsesNodeAliasesDoNotUseExactCache(t *testing.T) {
	for _, tc := range []struct {
		name      string
		path      string
		body      string
		node      config.NodeEntry
		wantPath  string
		writeBody func(http.ResponseWriter, int)
	}{
		{
			name:     "native messages node alias",
			path:     "/v1/messages",
			body:     `{"model":"alpha","max_tokens":16,"messages":[{"role":"user","content":"native messages"}]}`,
			node:     config.NodeEntry{Name: "native-node", APIType: "anthropic", AnthropicVersion: "2023-06-01"},
			wantPath: "/v1/messages",
			writeBody: func(w http.ResponseWriter, hit int) {
				_ = json.NewEncoder(w).Encode(map[string]any{
					"id":      "msg-" + strconv.Itoa(hit),
					"type":    "message",
					"role":    "assistant",
					"content": []any{map[string]any{"type": "text", "text": "native"}},
					"usage":   map[string]any{"input_tokens": 1, "output_tokens": 1},
				})
			},
		},
		{
			name:     "native responses node alias",
			path:     "/v1/responses",
			body:     `{"model":"alpha","input":"native responses"}`,
			node:     config.NodeEntry{Name: "native-node", SupportsResponsesAPI: true},
			wantPath: "/v1/responses",
			writeBody: func(w http.ResponseWriter, hit int) {
				_ = json.NewEncoder(w).Encode(map[string]any{
					"id":          "resp-" + strconv.Itoa(hit),
					"object":      "response",
					"status":      "completed",
					"model":       "alpha",
					"output":      []any{},
					"output_text": "native",
					"usage":       map[string]any{"total_tokens": 2},
				})
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var upstreamHits int
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				upstreamHits++
				if r.URL.Path != tc.wantPath {
					t.Errorf("path = %s, want %s", r.URL.Path, tc.wantPath)
				}
				w.Header().Set("Content-Type", "application/json")
				tc.writeBody(w, upstreamHits)
			}))
			defer upstream.Close()
			tc.node.APIBase = upstream.URL + "/v1"
			tc.node.APIKey = "upstream-key"
			app, err := New(&config.Config{
				Nodes: []config.NodeEntry{tc.node},
				Models: []config.ClientModelEntry{{
					Name:  "alpha",
					Nodes: []string{"native-node"},
				}},
				ProxySettings: config.ProxySettings{
					EnableMessagesEndpoint:  true,
					EnableResponsesEndpoint: true,
					InferenceCache: config.InferenceCacheConfig{
						Mode:          "read_write",
						TTLSeconds:    60,
						MaxAgeSeconds: 60,
						MaxEntries:    16,
						MaxEntryBytes: 4096,
					},
				},
			})
			if err != nil {
				t.Fatal(err)
			}

			_ = postTranslatedCacheRequest(t, app, tc.path, tc.body, "")
			_ = postTranslatedCacheRequest(t, app, tc.path, tc.body, "")
			if upstreamHits != 2 {
				t.Fatalf("native upstream hits = %d, want 2", upstreamHits)
			}
			assertInferenceCacheSnapshot(t, app, InferenceCacheSnapshotWant{Hits: 0, Misses: 0, Writes: 0, Entries: 0})
		})
	}
}

func TestAdminCacheStatsEndpointRequiresAdminAndReturnsSnapshot(t *testing.T) {
	t.Setenv(config.AdminPasswordEnv, "pw")
	const promptSecret = "cache-prompt-secret"
	const upstreamKey = "cache-upstream-secret"
	var upstreamHits int
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamHits++
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id":      "cache-test",
			"choices": []any{map[string]any{"message": map[string]any{"content": "cached"}}},
		})
	}))
	defer upstream.Close()
	app, err := New(&config.Config{
		ModelList: []config.ModelEntry{{
			ModelName: "alpha",
			ModelParams: map[string]any{
				"api_base": upstream.URL + "/v1",
				"api_key":  upstreamKey,
				"model":    "openai/upstream",
			},
		}},
		ProxySettings: config.ProxySettings{
			InferenceCache: config.InferenceCacheConfig{
				Mode:          "read_write",
				TTLSeconds:    60,
				MaxAgeSeconds: 60,
				MaxEntries:    16,
				MaxEntryBytes: 2048,
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	postChat := func() {
		t.Helper()
		resp := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"alpha","messages":[{"role":"user","content":"`+promptSecret+`"}]}`))
		req.Header.Set("Content-Type", "application/json")
		app.ServeHTTP(resp, req)
		if resp.Code != http.StatusOK {
			t.Fatalf("chat status = %d body=%s", resp.Code, resp.Body.String())
		}
	}
	postChat()
	postChat()
	if upstreamHits != 1 {
		t.Fatalf("upstream hits = %d, want 1", upstreamHits)
	}

	blocked := httptest.NewRecorder()
	app.ServeHTTP(blocked, httptest.NewRequest(http.MethodGet, "/admin/cache", nil))
	if blocked.Code != http.StatusForbidden {
		t.Fatalf("unguarded cache stats status = %d body=%s", blocked.Code, blocked.Body.String())
	}

	resp := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/admin/cache", nil)
	req.Header.Set("x-admin-password", "pw")
	app.ServeHTTP(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("cache stats status = %d body=%s", resp.Code, resp.Body.String())
	}
	var snapshot struct {
		Enabled         bool   `json:"enabled"`
		Mode            string `json:"mode"`
		Hits            uint64 `json:"hits"`
		Misses          uint64 `json:"misses"`
		Writes          uint64 `json:"writes"`
		Evictions       uint64 `json:"evictions"`
		Entries         int    `json:"entries"`
		TotalEntryBytes int64  `json:"total_entry_bytes"`
		MaxEntries      int    `json:"max_entries"`
		MaxEntryBytes   int    `json:"max_entry_bytes"`
	}
	if err := json.Unmarshal(resp.Body.Bytes(), &snapshot); err != nil {
		t.Fatal(err)
	}
	if !snapshot.Enabled || snapshot.Mode != "read_write" {
		t.Fatalf("cache stats enabled/mode = %#v", snapshot)
	}
	if snapshot.Hits != 1 || snapshot.Misses != 1 || snapshot.Writes != 1 || snapshot.Evictions != 0 {
		t.Fatalf("cache stats counters = %#v", snapshot)
	}
	if snapshot.Entries != 1 || snapshot.TotalEntryBytes <= 0 || snapshot.MaxEntries != 16 || snapshot.MaxEntryBytes != 2048 {
		t.Fatalf("cache stats bounds/size = %#v", snapshot)
	}
	body := resp.Body.String()
	for _, leaked := range []string{promptSecret, upstreamKey, upstream.URL} {
		if strings.Contains(body, leaked) {
			t.Fatalf("cache stats leaked %q: %s", leaked, body)
		}
	}
	again := httptest.NewRecorder()
	againReq := httptest.NewRequest(http.MethodGet, "/admin/cache", nil)
	againReq.Header.Set("x-admin-password", "pw")
	app.ServeHTTP(again, againReq)
	if again.Code != http.StatusOK {
		t.Fatalf("second cache stats status = %d body=%s", again.Code, again.Body.String())
	}
	if again.Body.String() != body {
		t.Fatalf("cache stats endpoint mutated snapshot: first=%s second=%s", body, again.Body.String())
	}
}

func TestAdminCacheStatsEndpointDisabled(t *testing.T) {
	t.Setenv(config.AdminPasswordEnv, "pw")
	app := newTestApp(t, "http://upstream.test/v1")
	resp := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/admin/cache", nil)
	req.Header.Set("x-admin-password", "pw")
	app.ServeHTTP(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("cache stats status = %d body=%s", resp.Code, resp.Body.String())
	}
	var snapshot struct {
		Enabled         bool   `json:"enabled"`
		Mode            string `json:"mode"`
		Hits            uint64 `json:"hits"`
		Misses          uint64 `json:"misses"`
		Writes          uint64 `json:"writes"`
		Evictions       uint64 `json:"evictions"`
		Entries         int    `json:"entries"`
		TotalEntryBytes int64  `json:"total_entry_bytes"`
	}
	if err := json.Unmarshal(resp.Body.Bytes(), &snapshot); err != nil {
		t.Fatal(err)
	}
	if snapshot.Enabled || snapshot.Mode != "off" {
		t.Fatalf("disabled cache stats enabled/mode = %#v", snapshot)
	}
	if snapshot.Hits != 0 || snapshot.Misses != 0 || snapshot.Writes != 0 || snapshot.Evictions != 0 || snapshot.Entries != 0 || snapshot.TotalEntryBytes != 0 {
		t.Fatalf("disabled cache stats counters = %#v", snapshot)
	}
}

func TestAdminCacheStatsEndpointNilSafe(t *testing.T) {
	t.Setenv(config.AdminPasswordEnv, "pw")
	app := &App{}
	resp := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/admin/cache", nil)
	req.Header.Set("x-admin-password", "pw")
	app.ServeHTTP(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("cache stats status = %d body=%s", resp.Code, resp.Body.String())
	}
	var snapshot struct {
		Enabled bool   `json:"enabled"`
		Mode    string `json:"mode"`
	}
	if err := json.Unmarshal(resp.Body.Bytes(), &snapshot); err != nil {
		t.Fatal(err)
	}
	if snapshot.Enabled || snapshot.Mode != "off" {
		t.Fatalf("nil app cache stats = %#v", snapshot)
	}
}

func TestPersistentLogDBStoresRequestMetadataAndAdminLogs(t *testing.T) {
	dir := t.TempDir()
	payloadDir := filepath.Join(dir, "requests")
	dbPath := filepath.Join(dir, "yaLLM.db")
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"error":{"message":"try fallback"}}`))
	}))
	defer primary.Close()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"choices": [{"finish_reason": "stop", "message": {"content": "stored"}}],
			"usage": {
				"prompt_tokens": 3,
				"completion_tokens": 4,
				"total_tokens": 7
			}
		}`))
	}))
	defer upstream.Close()
	app, err := New(&config.Config{
		ModelList: []config.ModelEntry{{
			ModelName: "alpha",
			ModelParams: map[string]any{
				"api_base": primary.URL + "/v1",
				"api_key":  "primary-secret-key",
				"model":    "openai/primary",
			},
		}, {
			ModelName: "beta",
			ModelParams: map[string]any{
				"api_base": upstream.URL + "/v1",
				"api_key":  "secondary-secret-key",
				"model":    "openai/upstream",
			},
		}},
		RouterSettings: config.RouterSettings{Fallbacks: []map[string]any{{"alpha": []any{"beta"}}}},
		ProxySettings: config.ProxySettings{
			Logging: config.LoggingConfig{
				FullRequestStore: map[string]any{
					"enabled":         true,
					"path":            payloadDir,
					"retention_hours": 24,
				},
			},
		},
		Database: map[string]any{
			"backend": "sqlite",
			"connection": map[string]any{
				"sqlite": map[string]any{"path": dbPath},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer app.LogDB.Close()
	provider, recorder := observability.NewInMemoryProvider("test-proxy")
	app.SetObservability(provider)

	resp := postChat(app, "")
	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", resp.Code, resp.Body.String())
	}
	records, err := app.LogDB.List(t.Context(), 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 {
		t.Fatalf("records = %#v", records)
	}
	record := records[0]
	if record.ModelName != "alpha" || record.Outcome != "success" || record.BackendName != "beta" || record.BackendStatus != http.StatusOK {
		t.Fatalf("record = %#v", record)
	}
	requestSpan := firstSpanByName(recorder.Spans(), "proxy.request")
	if requestSpan == nil {
		t.Fatalf("missing request span in %#v", spanNames(recorder.Spans()))
	}
	if record.TraceID != requestSpan.SpanContext().TraceID().String() || record.SpanID != requestSpan.SpanContext().SpanID().String() {
		t.Fatalf("record trace context = %q/%q, want request span %s/%s", record.TraceID, record.SpanID, requestSpan.SpanContext().TraceID(), requestSpan.SpanContext().SpanID())
	}
	if record.UsageStats["total_tokens"] != 7 {
		t.Fatalf("usage = %#v", record.UsageStats)
	}
	if record.StopReason != "stop" {
		t.Fatalf("stop reason = %q", record.StopReason)
	}
	if record.FullRequestPath == "" || filepath.Dir(record.FullRequestPath) != payloadDir {
		t.Fatalf("payload path = %q", record.FullRequestPath)
	}
	if _, err := app.LogDB.InsertError(t.Context(), logdb.ErrorRecord{
		Timestamp:    record.RequestTime,
		ModelName:    record.ModelName,
		ErrorType:    "backend_error",
		ErrorMessage: "raw-upstream-error api_key=secondary-secret-key query-secret",
		BackendName:  record.BackendName,
		HTTPStatus:   http.StatusBadGateway,
		RequestPath:  "/v1/chat/completions?token=query-secret",
		RequestLogID: record.ID,
		ExtraContext: map[string]any{
			"raw": "primary-secret-key",
		},
	}); err != nil {
		t.Fatal(err)
	}

	logsResp := httptest.NewRecorder()
	app.ServeHTTP(logsResp, httptest.NewRequest(http.MethodGet, "/admin/logs?model=alp&outcome=success&stop_reason=stop&search=stored", nil))
	if logsResp.Code != http.StatusOK {
		t.Fatalf("logs status = %d body=%s", logsResp.Code, logsResp.Body.String())
	}
	var payload map[string]any
	if err := json.Unmarshal(logsResp.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload["total"] != float64(1) || payload["has_more"] != false {
		t.Fatalf("pagination payload = %#v", payload)
	}
	data := payload["data"].([]any)
	if len(data) != 1 {
		t.Fatalf("admin logs = %#v", payload)
	}
	if data[0].(map[string]any)["model_name"] != "alpha" {
		t.Fatalf("admin logs record = %#v", data[0])
	}
	if data[0].(map[string]any)["total_tokens"] != float64(7) {
		t.Fatalf("admin logs token summary = %#v", data[0])
	}
	if data[0].(map[string]any)["trace_id"] != record.TraceID || data[0].(map[string]any)["span_id"] != record.SpanID {
		t.Fatalf("admin logs trace context = %#v", data[0])
	}
	logs := payload["logs"].([]any)
	if len(logs) != 1 || logs[0].(map[string]any)["id"] != record.ID {
		t.Fatalf("logs alias = %#v", logs)
	}

	t.Setenv(config.AdminPasswordEnv, "pw")
	blockedTraceLogs := httptest.NewRecorder()
	app.ServeHTTP(blockedTraceLogs, httptest.NewRequest(http.MethodGet, "/admin/logs?trace_id="+record.TraceID, nil))
	if blockedTraceLogs.Code != http.StatusForbidden {
		t.Fatalf("trace logs without admin status = %d body=%s", blockedTraceLogs.Code, blockedTraceLogs.Body.String())
	}
	traceLogsResp := httptest.NewRecorder()
	traceLogsReq := httptest.NewRequest(http.MethodGet, "/admin/logs?trace_id="+record.TraceID, nil)
	traceLogsReq.Header.Set("x-admin-password", "pw")
	app.ServeHTTP(traceLogsResp, traceLogsReq)
	if traceLogsResp.Code != http.StatusOK {
		t.Fatalf("trace logs status = %d body=%s", traceLogsResp.Code, traceLogsResp.Body.String())
	}
	var traceLogsPayload map[string]any
	if err := json.Unmarshal(traceLogsResp.Body.Bytes(), &traceLogsPayload); err != nil {
		t.Fatal(err)
	}
	if traceLogsPayload["total"] != float64(1) {
		t.Fatalf("trace logs payload = %#v", traceLogsPayload)
	}
	traceLogs := traceLogsPayload["logs"].([]any)
	if len(traceLogs) != 1 || traceLogs[0].(map[string]any)["id"] != record.ID {
		t.Fatalf("trace logs records = %#v", traceLogsPayload)
	}
	unknownTraceLogs := httptest.NewRecorder()
	unknownTraceLogsReq := httptest.NewRequest(http.MethodGet, "/admin/logs?trace_id="+strings.Repeat("9", 32), nil)
	unknownTraceLogsReq.Header.Set("x-admin-password", "pw")
	app.ServeHTTP(unknownTraceLogs, unknownTraceLogsReq)
	if unknownTraceLogs.Code != http.StatusOK {
		t.Fatalf("unknown trace logs status = %d body=%s", unknownTraceLogs.Code, unknownTraceLogs.Body.String())
	}
	var unknownTraceLogsPayload map[string]any
	if err := json.Unmarshal(unknownTraceLogs.Body.Bytes(), &unknownTraceLogsPayload); err != nil {
		t.Fatal(err)
	}
	if unknownTraceLogsPayload["total"] != float64(0) {
		t.Fatalf("unknown trace logs payload = %#v", unknownTraceLogsPayload)
	}

	blockedTraceDetail := httptest.NewRecorder()
	app.ServeHTTP(blockedTraceDetail, httptest.NewRequest(http.MethodGet, "/admin/traces/"+record.TraceID, nil))
	if blockedTraceDetail.Code != http.StatusForbidden {
		t.Fatalf("trace detail without admin status = %d body=%s", blockedTraceDetail.Code, blockedTraceDetail.Body.String())
	}
	traceDetailResp := httptest.NewRecorder()
	traceDetailReq := httptest.NewRequest(http.MethodGet, "/admin/traces/"+record.TraceID, nil)
	traceDetailReq.Header.Set("x-admin-password", "pw")
	app.ServeHTTP(traceDetailResp, traceDetailReq)
	if traceDetailResp.Code != http.StatusOK {
		t.Fatalf("trace detail status = %d body=%s", traceDetailResp.Code, traceDetailResp.Body.String())
	}
	var traceDetail map[string]any
	if err := json.Unmarshal(traceDetailResp.Body.Bytes(), &traceDetail); err != nil {
		t.Fatal(err)
	}
	if traceDetail["trace_id"] != record.TraceID || traceDetail["total"] != float64(1) || traceDetail["request_count"] != float64(1) {
		t.Fatalf("trace detail metadata = %#v", traceDetail)
	}
	traceRequests := traceDetail["requests"].([]any)
	if len(traceRequests) != 1 || traceRequests[0].(map[string]any)["id"] != record.ID {
		t.Fatalf("trace detail requests = %#v", traceDetail)
	}
	if _, ok := traceRequests[0].(map[string]any)["body"]; ok {
		t.Fatalf("trace detail request exposed body: %#v", traceRequests[0])
	}
	traceAttempts := traceDetail["attempts"].([]any)
	if len(traceAttempts) != 2 {
		t.Fatalf("trace detail attempts = %#v", traceDetail)
	}
	traceErrors := traceDetail["error_logs"].([]any)
	if len(traceErrors) != 1 || traceErrors[0].(map[string]any)["error_type"] != "backend_error" {
		t.Fatalf("trace detail errors = %#v", traceDetail)
	}
	for _, field := range []string{"error_message", "request_path", "extra_context"} {
		if _, ok := traceErrors[0].(map[string]any)[field]; ok {
			t.Fatalf("trace detail error exposed %s: %#v", field, traceErrors[0])
		}
	}
	traceDetailRaw := traceDetailResp.Body.String()
	for _, leaked := range []string{"stored", "primary-secret-key", "secondary-secret-key", "Authorization", "api_key", "raw-upstream-error", "query-secret"} {
		if strings.Contains(traceDetailRaw, leaked) {
			t.Fatalf("trace detail leaked %q: %s", leaked, traceDetailRaw)
		}
	}
	unknownTraceDetail := httptest.NewRecorder()
	unknownTraceDetailReq := httptest.NewRequest(http.MethodGet, "/admin/traces/"+strings.Repeat("8", 32), nil)
	unknownTraceDetailReq.Header.Set("x-admin-password", "pw")
	app.ServeHTTP(unknownTraceDetail, unknownTraceDetailReq)
	if unknownTraceDetail.Code != http.StatusNotFound {
		t.Fatalf("unknown trace detail status = %d body=%s", unknownTraceDetail.Code, unknownTraceDetail.Body.String())
	}

	detailResp := httptest.NewRecorder()
	detailReq := httptest.NewRequest(http.MethodGet, "/admin/logs/"+record.ID, nil)
	detailReq.Header.Set("x-admin-password", "pw")
	app.ServeHTTP(detailResp, detailReq)
	if detailResp.Code != http.StatusOK {
		t.Fatalf("detail status = %d body=%s", detailResp.Code, detailResp.Body.String())
	}
	var detail map[string]any
	if err := json.Unmarshal(detailResp.Body.Bytes(), &detail); err != nil {
		t.Fatal(err)
	}
	if detail["full_request_status"] != "available" {
		t.Fatalf("detail = %#v", detail)
	}
	if detail["trace_id"] != record.TraceID || detail["span_id"] != record.SpanID {
		t.Fatalf("detail trace context = %#v", detail)
	}
	body := detail["body"].(map[string]any)
	if body["model"] != "alpha" {
		t.Fatalf("detail body = %#v", body)
	}
	if !strings.Contains(detail["full_response"].(string), "stored") {
		t.Fatalf("detail full response = %#v", detail["full_response"])
	}
	if detail["response_body"].(map[string]any)["usage"].(map[string]any)["total_tokens"] != float64(7) {
		t.Fatalf("detail response body = %#v", detail["response_body"])
	}
	attemptTrail := detail["attempt_trail"].([]any)
	attempts := detail["attempts"].([]any)
	if len(attemptTrail) != 2 || len(attempts) != 2 {
		t.Fatalf("detail attempts = %#v alias=%#v", attemptTrail, attempts)
	}
	firstAttempt := attemptTrail[0].(map[string]any)
	secondAttempt := attemptTrail[1].(map[string]any)
	if firstAttempt["attempt_index"] != float64(1) || firstAttempt["backend_name"] != "alpha" || firstAttempt["status"] != float64(http.StatusServiceUnavailable) || firstAttempt["outcome"] != "retry" {
		t.Fatalf("first attempt = %#v", firstAttempt)
	}
	if secondAttempt["attempt_index"] != float64(2) || secondAttempt["backend_name"] != "beta" || secondAttempt["status"] != float64(http.StatusOK) || secondAttempt["outcome"] != "final" {
		t.Fatalf("second attempt = %#v", secondAttempt)
	}
	rawAttempts, _ := json.Marshal(attemptTrail)
	for _, secret := range []string{"primary-secret-key", "secondary-secret-key", "Authorization", "api_key"} {
		if strings.Contains(string(rawAttempts), secret) {
			t.Fatalf("attempt trail leaked %q: %s", secret, string(rawAttempts))
		}
	}

	badResp := httptest.NewRecorder()
	badReq := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"alpha"}`))
	badReq.Header.Set("Content-Type", "application/json")
	app.ServeHTTP(badResp, badReq)
	if badResp.Code != http.StatusBadRequest {
		t.Fatalf("bad request status = %d body=%s", badResp.Code, badResp.Body.String())
	}
	failed, err := app.LogDB.Query(t.Context(), logdb.QueryOptions{Outcome: "error"})
	if err != nil {
		t.Fatal(err)
	}
	if failed.Total != 1 {
		t.Fatalf("failed logs = %#v", failed)
	}
	errorDetailResp := httptest.NewRecorder()
	errorDetailReq := httptest.NewRequest(http.MethodGet, "/admin/logs/"+failed.Records[0].ID, nil)
	errorDetailReq.Header.Set("x-admin-password", "pw")
	app.ServeHTTP(errorDetailResp, errorDetailReq)
	if errorDetailResp.Code != http.StatusOK {
		t.Fatalf("error detail status = %d body=%s", errorDetailResp.Code, errorDetailResp.Body.String())
	}
	var errorDetail map[string]any
	if err := json.Unmarshal(errorDetailResp.Body.Bytes(), &errorDetail); err != nil {
		t.Fatal(err)
	}
	errorLogs := errorDetail["error_logs"].([]any)
	if len(errorLogs) != 1 || errorLogs[0].(map[string]any)["error_type"] != "request_error" {
		t.Fatalf("error logs = %#v", errorLogs)
	}

	now := time.Now().UTC()
	if _, err := app.LogDB.Insert(t.Context(), logdb.Record{
		RequestTime: now,
		ModelName:   "beta",
		Path:        "/v1/chat/completions",
		Method:      "POST",
		Outcome:     "success",
		DurationMS:  50,
		StopReason:  "tool_calls",
		IsToolCall:  true,
	}); err != nil {
		t.Fatal(err)
	}
	stopResp := httptest.NewRecorder()
	stopReq := httptest.NewRequest(http.MethodGet, "/admin/logs/stop-reasons", nil)
	stopReq.Header.Set("x-admin-password", "pw")
	app.ServeHTTP(stopResp, stopReq)
	if stopResp.Code != http.StatusOK {
		t.Fatalf("stop reasons status = %d body=%s", stopResp.Code, stopResp.Body.String())
	}
	var stopPayload map[string]any
	if err := json.Unmarshal(stopResp.Body.Bytes(), &stopPayload); err != nil {
		t.Fatal(err)
	}
	reasons := stopPayload["stop_reasons"].([]any)
	if len(reasons) != 2 {
		t.Fatalf("stop reasons = %#v", stopPayload)
	}
	toolRateResp := httptest.NewRecorder()
	toolRateReq := httptest.NewRequest(http.MethodGet, "/admin/logs/tool-call-rate", nil)
	toolRateReq.Header.Set("x-admin-password", "pw")
	app.ServeHTTP(toolRateResp, toolRateReq)
	if toolRateResp.Code != http.StatusOK {
		t.Fatalf("tool rate status = %d body=%s", toolRateResp.Code, toolRateResp.Body.String())
	}
	var toolRate map[string]any
	if err := json.Unmarshal(toolRateResp.Body.Bytes(), &toolRate); err != nil {
		t.Fatal(err)
	}
	if toolRate["total_requests"] != float64(3) || toolRate["tool_call_requests"] != float64(1) {
		t.Fatalf("tool rate = %#v", toolRate)
	}
	modelsResp := httptest.NewRecorder()
	modelsReq := httptest.NewRequest(http.MethodGet, "/admin/logs/models", nil)
	modelsReq.Header.Set("x-admin-password", "pw")
	app.ServeHTTP(modelsResp, modelsReq)
	if modelsResp.Code != http.StatusOK {
		t.Fatalf("models status = %d body=%s", modelsResp.Code, modelsResp.Body.String())
	}
	var modelsPayload map[string]any
	if err := json.Unmarshal(modelsResp.Body.Bytes(), &modelsPayload); err != nil {
		t.Fatal(err)
	}
	models := modelsPayload["models"].([]any)
	if len(models) != 2 {
		t.Fatalf("models = %#v", modelsPayload)
	}
}

func TestSanitizedRequestWarningsPersistAndAPIAlias(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "yaLLM.db")
	payloadDir := filepath.Join(dir, "requests")
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var got map[string]any
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatal(err)
		}
		if _, ok := got["tool_choice"]; ok {
			t.Fatalf("tool_choice leaked upstream: %#v", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"finish_reason":"stop","message":{"content":"stored"}}],"usage":{"prompt_tokens":3,"completion_tokens":4,"total_tokens":7}}`))
	}))
	defer upstream.Close()
	app, err := New(&config.Config{
		ModelList: []config.ModelEntry{{
			ModelName: "alpha",
			ModelParams: map[string]any{
				"api_base": upstream.URL + "/v1",
				"model":    "upstream-model",
			},
		}},
		ProxySettings: config.ProxySettings{Logging: config.LoggingConfig{FullRequestStore: map[string]any{
			"enabled": true,
			"path":    payloadDir,
		}}},
		Database: map[string]any{
			"backend": "sqlite",
			"connection": map[string]any{
				"sqlite": map[string]any{"path": dbPath},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer app.LogDB.Close()

	resp := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"alpha","messages":[{"role":"user","content":"hi"}],"tool_choice":"auto"}`))
	req.Header.Set("Content-Type", "application/json")
	app.ServeHTTP(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", resp.Code, resp.Body.String())
	}

	logsResp := httptest.NewRecorder()
	app.ServeHTTP(logsResp, httptest.NewRequest(http.MethodGet, "/api/logs?limit=10", nil))
	if logsResp.Code != http.StatusOK {
		t.Fatalf("api logs status = %d body=%s", logsResp.Code, logsResp.Body.String())
	}
	var listPayload map[string]any
	if err := json.Unmarshal(logsResp.Body.Bytes(), &listPayload); err != nil {
		t.Fatal(err)
	}
	logs := listPayload["logs"].([]any)
	if len(logs) != 1 {
		t.Fatalf("logs = %#v", listPayload)
	}
	row := logs[0].(map[string]any)
	if row["warning_count"] != float64(1) {
		t.Fatalf("warning_count = %#v row=%#v", row["warning_count"], row)
	}
	id := row["id"].(string)

	detailResp := httptest.NewRecorder()
	app.ServeHTTP(detailResp, httptest.NewRequest(http.MethodGet, "/api/logs/"+id, nil))
	if detailResp.Code != http.StatusOK {
		t.Fatalf("detail status = %d body=%s", detailResp.Code, detailResp.Body.String())
	}
	var detail map[string]any
	if err := json.Unmarshal(detailResp.Body.Bytes(), &detail); err != nil {
		t.Fatal(err)
	}
	warnings := detail["warnings"].([]any)
	if len(warnings) != 1 {
		t.Fatalf("warnings = %#v", detail)
	}
	warning := warnings[0].(map[string]any)
	if warning["error_type"] != "client_request_warning" || !strings.Contains(warning["error_message"].(string), "tool_choice") {
		t.Fatalf("warning = %#v", warning)
	}
	if detail["full_request_status"] != "available" {
		t.Fatalf("detail = %#v", detail)
	}
}

func TestPersistentLogDBStoresStreamingUsageMetadata(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "yaLLM.db")
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"stored\"}}]}\n\n"))
		_, _ = w.Write([]byte("data: {\"usage\":{\"prompt_tokens\":3,\"completion_tokens\":4,\"total_tokens\":7}}\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer upstream.Close()
	app, err := New(&config.Config{
		ModelList: []config.ModelEntry{{
			ModelName: "alpha",
			ModelParams: map[string]any{
				"api_base": upstream.URL + "/v1",
				"model":    "openai/upstream",
			},
		}},
		Database: map[string]any{
			"backend": "sqlite",
			"connection": map[string]any{
				"sqlite": map[string]any{"path": dbPath},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer app.LogDB.Close()

	resp := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"alpha","messages":[{"role":"user","content":"hi"}],"stream":true}`))
	app.ServeHTTP(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", resp.Code, resp.Body.String())
	}
	records, err := app.LogDB.List(t.Context(), 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 {
		t.Fatalf("records = %#v", records)
	}
	record := records[0]
	if !record.IsStream {
		t.Fatalf("record is not marked stream: %#v", record)
	}
	if record.BackendName != "alpha" {
		t.Fatalf("backend = %q", record.BackendName)
	}
	if record.TraceID != "" || record.SpanID != "" {
		t.Fatalf("disabled otel trace context = %q/%q, want empty", record.TraceID, record.SpanID)
	}
	if record.UsageStats["total_tokens"] != 7 || record.UsageStats["prompt_tokens"] != 3 || record.UsageStats["completion_tokens"] != 4 {
		t.Fatalf("usage = %#v", record.UsageStats)
	}
}

func newTestApp(t *testing.T, upstream string) *App {
	t.Helper()
	app, err := New(&config.Config{ModelList: []config.ModelEntry{{
		ModelName: "alpha",
		ModelParams: map[string]any{
			"api_base": upstream,
			"api_key":  "upstream-key",
			"model":    "openai/upstream-model",
		},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	return app
}

type InferenceCacheSnapshotWant struct {
	Enabled bool
	Hits    uint64
	Misses  uint64
	Writes  uint64
	Entries int
}

func newTranslatedCacheTestApp(t *testing.T, upstream string, mode string, modelParams map[string]any) *App {
	t.Helper()
	params := map[string]any{
		"api_base": upstream,
		"api_key":  "upstream-key",
		"model":    "openai/upstream-model",
	}
	for key, value := range modelParams {
		params[key] = value
	}
	app, err := New(&config.Config{
		ModelList: []config.ModelEntry{{
			ModelName:   "alpha",
			ModelParams: params,
		}},
		ProxySettings: config.ProxySettings{
			EnableMessagesEndpoint:  true,
			EnableResponsesEndpoint: true,
			InferenceCache: config.InferenceCacheConfig{
				Mode:          mode,
				TTLSeconds:    60,
				MaxAgeSeconds: 60,
				MaxEntries:    16,
				MaxEntryBytes: 4096,
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return app
}

func newSemanticCacheTestApp(t *testing.T, chatUpstream string, embeddingUpstream string, mode string, appKeys *config.AppKeysConfig) *App {
	t.Helper()
	cfg := &config.Config{
		Nodes: []config.NodeEntry{{
			Name:    "node-a",
			APIBase: chatUpstream,
			APIKey:  "chat-key",
			Model:   "upstream-chat",
		}, {
			Name:    "embed-node",
			APIBase: embeddingUpstream,
			APIKey:  "embed-key",
			Model:   "upstream-embed",
		}},
		Models: []config.ClientModelEntry{{
			Name:  "alpha",
			Nodes: []string{"node-a"},
		}},
		ProxySettings: config.ProxySettings{
			InferenceCache: config.InferenceCacheConfig{
				Mode:          mode,
				TTLSeconds:    60,
				MaxAgeSeconds: 60,
				MaxEntries:    16,
				MaxEntryBytes: 4096,
				Semantic: config.SemanticCacheConfig{
					Enabled:             true,
					EmbeddingNode:       "embed-node",
					EmbeddingModel:      "text-embedding-test",
					SimilarityThreshold: 0.90,
					TopK:                4,
					MaxEntries:          16,
					MaxEntryBytes:       4096,
				},
			},
		},
	}
	if appKeys != nil {
		cfg.AppKeys = *appKeys
	}
	app, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return app
}

func postSemanticChat(t *testing.T, app *App, prompt string, bypass string, extraHeaders http.Header) string {
	t.Helper()
	resp := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"alpha","messages":[{"role":"user","content":"`+prompt+`"}],"temperature":0}`))
	req.Header.Set("Content-Type", "application/json")
	if bypass != "" {
		req.Header.Set("x-yallmp-cache-bypass", bypass)
	}
	for key, values := range extraHeaders {
		for _, value := range values {
			req.Header.Add(key, value)
		}
	}
	app.ServeHTTP(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("semantic chat status = %d body=%s", resp.Code, resp.Body.String())
	}
	return resp.Body.String()
}

func postTranslatedCacheRequest(t *testing.T, app *App, path string, body string, bypass string) string {
	t.Helper()
	resp := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if bypass != "" {
		req.Header.Set("x-yallmp-cache-bypass", bypass)
	}
	app.ServeHTTP(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("%s status = %d body=%s", path, resp.Code, resp.Body.String())
	}
	return resp.Body.String()
}

func assertInferenceCacheSnapshot(t *testing.T, app *App, want InferenceCacheSnapshotWant) {
	t.Helper()
	if app.Router.InferenceCache == nil {
		if want.Enabled {
			t.Fatal("cache disabled, want enabled")
		}
		return
	}
	snapshot := app.Router.InferenceCache.Snapshot()
	if !snapshot.Enabled {
		t.Fatalf("cache disabled snapshot = %#v", snapshot)
	}
	if snapshot.Hits != want.Hits || snapshot.Misses != want.Misses || snapshot.Writes != want.Writes || snapshot.Entries != want.Entries {
		t.Fatalf("cache snapshot = %#v, want hits=%d misses=%d writes=%d entries=%d", snapshot, want.Hits, want.Misses, want.Writes, want.Entries)
	}
}

func firstSpanByName(spans []sdktrace.ReadOnlySpan, name string) sdktrace.ReadOnlySpan {
	for _, span := range spans {
		if span.Name() == name {
			return span
		}
	}
	return nil
}

func spanAttrs(span sdktrace.ReadOnlySpan) map[string]string {
	out := map[string]string{}
	if span == nil {
		return out
	}
	for _, attr := range span.Attributes() {
		out[string(attr.Key)] = attr.Value.Emit()
	}
	return out
}

func requireSpanAttrsByName(t *testing.T, spans []sdktrace.ReadOnlySpan, name string) map[string]string {
	t.Helper()
	span := firstSpanByName(spans, name)
	if span == nil {
		t.Fatalf("missing span %s in names=%#v", name, spanNames(spans))
	}
	return spanAttrs(span)
}

func requireOTelPhaseSpans(t *testing.T, spans []sdktrace.ReadOnlySpan, endpointKind string, model string, names []string) {
	t.Helper()
	for _, name := range names {
		attrs := requireSpanAttrsByName(t, spans, name)
		if attrs["endpoint_kind"] != endpointKind || attrs["model"] != model {
			t.Fatalf("%s attrs = %#v, want endpoint_kind=%q model=%q", name, attrs, endpointKind, model)
		}
		if attrs["gen_ai.system"] != "openai" || attrs["gen_ai.operation.name"] != endpointKind || attrs["gen_ai.request.model"] != model {
			t.Fatalf("%s gen_ai attrs = %#v", name, attrs)
		}
	}
}

func countSpansByName(spans []sdktrace.ReadOnlySpan, name string) int {
	count := 0
	for _, span := range spans {
		if span.Name() == name {
			count++
		}
	}
	return count
}

func spanNames(spans []sdktrace.ReadOnlySpan) []string {
	names := make([]string, 0, len(spans))
	for _, span := range spans {
		names = append(names, span.Name())
	}
	return names
}

func assertSpansDoNotLeak(t *testing.T, spans []sdktrace.ReadOnlySpan, secrets []string) {
	t.Helper()
	for _, span := range spans {
		for _, secret := range secrets {
			if secret == "" {
				continue
			}
			if strings.Contains(span.Status().Description, secret) {
				t.Fatalf("span %s status leaked %q: %s", span.Name(), secret, span.Status().Description)
			}
			for _, attr := range span.Attributes() {
				if strings.Contains(string(attr.Key), secret) || strings.Contains(attr.Value.Emit(), secret) {
					t.Fatalf("span %s attr %s leaked %q: %s", span.Name(), attr.Key, secret, attr.Value.Emit())
				}
			}
		}
	}
}

func collectOTelMetrics(t *testing.T, recorder *observability.Recorder) metricdata.ResourceMetrics {
	t.Helper()
	metrics, err := recorder.Metrics(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return metrics
}

func metricSumValue(metrics metricdata.ResourceMetrics, name string, attrs map[string]string) int64 {
	var total int64
	for _, scope := range metrics.ScopeMetrics {
		for _, metricItem := range scope.Metrics {
			if metricItem.Name != name {
				continue
			}
			sum, ok := metricItem.Data.(metricdata.Sum[int64])
			if !ok {
				continue
			}
			for _, point := range sum.DataPoints {
				if metricAttrsMatch(point.Attributes, attrs) {
					total += point.Value
				}
			}
		}
	}
	return total
}

func metricHistogramCount(metrics metricdata.ResourceMetrics, name string, attrs map[string]string) uint64 {
	var total uint64
	for _, scope := range metrics.ScopeMetrics {
		for _, metricItem := range scope.Metrics {
			if metricItem.Name != name {
				continue
			}
			histogram, ok := metricItem.Data.(metricdata.Histogram[float64])
			if !ok {
				continue
			}
			for _, point := range histogram.DataPoints {
				if metricAttrsMatch(point.Attributes, attrs) {
					total += point.Count
				}
			}
		}
	}
	return total
}

func metricAttrsMatch(set attribute.Set, attrs map[string]string) bool {
	for key, want := range attrs {
		value, ok := set.Value(attribute.Key(key))
		if !ok || value.Emit() != want {
			return false
		}
	}
	return true
}

type chunkStringReader struct {
	chunks []string
	index  int
}

func (r *chunkStringReader) Read(p []byte) (int, error) {
	if r.index >= len(r.chunks) {
		return 0, io.EOF
	}
	n := copy(p, r.chunks[r.index])
	r.index++
	return n, nil
}

type failingResponseWriter struct {
	header http.Header
	status int
	err    error
}

func (w *failingResponseWriter) Header() http.Header {
	if w.header == nil {
		w.header = http.Header{}
	}
	return w.header
}

func (w *failingResponseWriter) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
	}
}

func (w *failingResponseWriter) Write(_ []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	return 0, w.err
}

func (w *failingResponseWriter) Flush() {}

func postChat(app *App, secret string) *httptest.ResponseRecorder {
	resp := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"alpha","messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Content-Type", "application/json")
	if secret != "" {
		req.Header.Set("Authorization", "Bearer "+secret)
	}
	app.ServeHTTP(resp, req)
	return resp
}

func postStreamChat(app *App) *httptest.ResponseRecorder {
	resp := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"alpha","messages":[{"role":"user","content":"hi"}],"stream":true}`))
	req.Header.Set("Content-Type", "application/json")
	app.ServeHTTP(resp, req)
	return resp
}

func collectStreamToolArgumentsFromBody(t *testing.T, body string) string {
	t.Helper()
	var out strings.Builder
	for _, event := range strings.Split(body, "\n\n") {
		for _, line := range strings.Split(event, "\n") {
			if !strings.HasPrefix(line, "data:") {
				continue
			}
			raw := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
			if raw == "" || raw == "[DONE]" {
				continue
			}
			payload := map[string]any{}
			if err := json.Unmarshal([]byte(raw), &payload); err != nil {
				t.Fatalf("invalid SSE JSON %q: %v", raw, err)
			}
			choices, _ := payload["choices"].([]any)
			for _, rawChoice := range choices {
				choice, _ := rawChoice.(map[string]any)
				delta, _ := choice["delta"].(map[string]any)
				calls, _ := delta["tool_calls"].([]any)
				for _, rawCall := range calls {
					call, _ := rawCall.(map[string]any)
					function, _ := call["function"].(map[string]any)
					if args, ok := function["arguments"].(string); ok {
						out.WriteString(args)
					}
				}
			}
		}
	}
	return out.String()
}

func newParsedStreamSidecarApp(t *testing.T, payloadDir string, logParsedStream any, streamBody string) *App {
	t.Helper()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(streamBody))
	}))
	t.Cleanup(upstream.Close)
	app, err := New(&config.Config{
		ModelList: []config.ModelEntry{{
			ModelName: "alpha",
			ModelParams: map[string]any{
				"api_base": upstream.URL + "/v1",
				"model":    "openai/upstream",
			},
		}},
		ProxySettings: config.ProxySettings{
			Logging: config.LoggingConfig{
				LogToDisk:       true,
				LogParsedStream: logParsedStream,
				FullRequestStore: map[string]any{
					"enabled": true,
					"path":    payloadDir,
				},
			},
			Modules: map[string]any{
				"enabled":  true,
				"response": []any{"parse_tags"},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return app
}

func parsedStreamFixture() string {
	return strings.Join([]string{
		`data: {"choices":[{"delta":{"role":"assistant"},"index":0}]}`,
		``,
		`data: {"choices":[{"delta":{"content":"<think>hidden</think>visible"},"index":0}]}`,
		``,
		`data: {"choices":[{"delta":{},"finish_reason":"stop","index":0}],"usage":{"prompt_tokens":3,"completion_tokens":4,"total_tokens":7}}`,
		``,
		`data: [DONE]`,
		``,
		``,
	}, "\n")
}
