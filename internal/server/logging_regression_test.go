package server

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/theio/yallmp-proxy/internal/config"
	"github.com/theio/yallmp-proxy/internal/logstore"
	"github.com/theio/yallmp-proxy/internal/observability"
	"github.com/theio/yallmp-proxy/internal/policy"
	"github.com/theio/yallmp-proxy/internal/proxy"
)

func TestPersistentLogSurvivesCanceledRequest(t *testing.T) {
	app := newLoggingRegressionApp(t, nil)
	provider, recorder := observability.NewInMemoryProvider("logging-test")
	app.SetObservability(provider)
	ctx, requestSpan := provider.StartRequestSpan(t.Context(), observability.RequestAttrs{EndpointKind: "chat_completions", Model: "alpha"})
	ctx, cancel := context.WithCancel(contextWithProxyRequestTrace(ctx, requestSpan))
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions?test=1", nil).WithContext(ctx)
	entry := app.addLog(req, LogEntry{Time: time.Now(), Method: req.Method, Path: req.URL.Path, Model: "alpha", Status: 200, Error: "client disconnected", Warnings: []proxy.RequestWarning{{Code: "test", Message: "test warning"}}})
	cancel()
	app.persistLog(ctx, entry, req, true, policy.Context{}, "", nil)
	requestSpan.End(observability.SpanResult{Outcome: observability.OutcomeError})
	records, err := app.LogDB.List(t.Context(), 10, 0)
	if err != nil || len(records) != 1 {
		t.Fatalf("logs = %#v, err = %v", records, err)
	}
	record := records[0]
	if record.Outcome != "error" || record.TraceID == "" || record.TraceID != entry.TraceID || record.SpanID != entry.SpanID {
		t.Fatalf("record = %#v", record)
	}
	errs, err := app.LogDB.ErrorsForRequest(t.Context(), record.ID)
	if err != nil || len(errs) != 2 {
		t.Fatalf("errors = %#v, err = %v", errs, err)
	}
	foundLogging := false
	for _, span := range recorder.Spans() {
		if span.Name() == "proxy.logging" {
			foundLogging = true
			if span.SpanContext().TraceID().String() != record.TraceID {
				t.Fatalf("logging span lost request trace")
			}
		}
	}
	if !foundLogging {
		t.Fatal("logging span missing")
	}
}

func TestStreamFailuresPersistErrorLogs(t *testing.T) {
	for _, endpoint := range []struct {
		name, path string
		params     map[string]any
	}{
		{"chat", "/v1/chat/completions", nil},
		{"native messages", "/v1/messages", map[string]any{"model": "anthropic/upstream"}},
		{"translated messages", "/v1/messages", nil},
		{"native responses", "/v1/responses", map[string]any{"supports_responses_api": true}},
		{"translated responses", "/v1/responses", nil},
	} {
		for _, failure := range []string{"read", "write", "protocol"} {
			t.Run(endpoint.name+"/"+failure, func(t *testing.T) {
				app := newLoggingRegressionApp(t, endpoint.params)
				provider, recorder := observability.NewInMemoryProvider("logging-test")
				app.SetObservability(provider)
				chunk := "data: {\"choices\":[{\"delta\":{\"content\":\"hello\"}}]}\n\n"
				if failure == "protocol" {
					chunk = "data: {\"error\":{\"message\":\"protocol failed\"}}\n\n"
					if endpoint.name == "native responses" {
						chunk = "event: response.failed\ndata: {\"type\":\"response.failed\",\"response\":{\"error\":{\"message\":\"protocol failed\"}}}\n\n"
					}
				}
				var tailErr error
				if failure == "read" {
					tailErr = errors.New("upstream read failed")
				}
				app.Router.Client.Transport = loggingRoundTripper(func(req *http.Request) (*http.Response, error) {
					return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(&errorAfterChunksReader{chunks: []string{chunk}, err: tailErr}), Request: req}, nil
				})
				req := httptest.NewRequest(http.MethodPost, endpoint.path, strings.NewReader(`{"model":"alpha","messages":[{"role":"user","content":"hi"}],"input":"hi","max_tokens":16,"stream":true}`))
				var writer http.ResponseWriter = httptest.NewRecorder()
				if failure == "write" {
					writer = &failingResponseWriter{err: errors.New("client write failed")}
				}
				app.ServeHTTP(writer, req)
				records, err := app.LogDB.List(t.Context(), 10, 0)
				if err != nil || len(records) != 1 {
					t.Fatalf("logs = %#v, err = %v", records, err)
				}
				record := records[0]
				if record.Outcome != "error" || !record.IsStream || record.BackendStatus != 200 {
					t.Fatalf("record = %#v", record)
				}
				errs, err := app.LogDB.ErrorsForRequest(t.Context(), record.ID)
				if err != nil || len(errs) != 1 || !strings.Contains(errs[0].ErrorMessage, "failed") {
					t.Fatalf("errors = %#v, err = %v", errs, err)
				}
				for _, name := range []string{"proxy.request", "proxy.response"} {
					attrs := requireSpanAttrsByName(t, recorder.Spans(), name)
					if attrs["outcome"] != "error" {
						t.Fatalf("%s = %#v", name, attrs)
					}
				}
			})
		}
	}
}

func newLoggingRegressionApp(t *testing.T, params map[string]any) *App {
	t.Helper()
	modelParams := map[string]any{"api_base": "http://upstream.test/v1", "model": "openai/upstream"}
	for key, value := range params {
		modelParams[key] = value
	}
	app, err := New(&config.Config{
		ModelList:     []config.ModelEntry{{ModelName: "alpha", ModelParams: modelParams, Limits: config.LimitsConfig{Concurrency: 1}}},
		ProxySettings: config.ProxySettings{EnableMessagesEndpoint: true, EnableResponsesEndpoint: true},
		Database:      map[string]any{"backend": "sqlite", "connection": map[string]any{"sqlite": map[string]any{"path": filepath.Join(t.TempDir(), "logs.db")}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	app.Router.Client = &http.Client{}
	t.Cleanup(func() { app.LogDB.Close(); app.ShutdownObservability(context.Background()) })
	return app
}

type loggingRoundTripper func(*http.Request) (*http.Response, error)

func (f loggingRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func TestResponsesWriteFailureStoresFailedState(t *testing.T) {
	app := newLoggingRegressionApp(t, nil)
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	resp := &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(&errorAfterChunksReader{chunks: []string{"data: {\"choices\":[{\"delta\":{\"content\":\"partial\"}}]}\n\n"}})}
	writer := &failingResponseWriter{err: errors.New("client write failed")}
	err := app.handleResponsesStream(writer, req, time.Now(), nil, "alpha", "resp_disconnected", map[string]any{"store": true, "input": "hi"}, policy.Context{}, nil, nil, resp, false, proxy.ForwardResult{BackendName: "alpha"})
	if err == nil {
		t.Fatal("expected client write failure")
	}
	record, found, err := app.LogDB.GetResponseState(t.Context(), "resp_disconnected")
	if err != nil || !found || record.Status != "failed" {
		t.Fatalf("response state = %#v, found = %v, err = %v", record, found, err)
	}
}

func TestQueueCancellationPersistsLog(t *testing.T) {
	for _, path := range []string{"/v1/chat/completions", "/v1/messages", "/v1/responses"} {
		t.Run(path, func(t *testing.T) {
			app := newLoggingRegressionApp(t, nil)
			slot, err := app.Admission.Acquire(t.Context(), "", 1, 100, "alpha")
			if err != nil {
				t.Fatal(err)
			}
			defer slot.Release()
			ctx, cancel := context.WithCancel(t.Context())
			cancel()
			req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{"model":"alpha","messages":[{"role":"user","content":"hi"}],"input":"hi","max_tokens":16,"stream":true}`)).WithContext(ctx)
			writer := httptest.NewRecorder()
			app.ServeHTTP(writer, req)
			if writer.Code != 499 {
				t.Fatalf("status = %d, body = %s", writer.Code, writer.Body.String())
			}
			records, err := app.LogDB.List(t.Context(), 10, 0)
			if err != nil || len(records) != 1 || records[0].Outcome != "error" || records[0].BackendStatus != 499 || !records[0].IsStream {
				t.Fatalf("logs = %#v, err = %v", records, err)
			}
		})
	}
}

func TestNativeMessagesCacheUsageSurvivesLogging(t *testing.T) {
	for _, count := range []string{"0", "128", "1.5", "null", `"invalid"`} {
		t.Run(count, func(t *testing.T) {
			app := newLoggingRegressionApp(t, nil)
			body := "data: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":3,\"cache_read_input_tokens\":" + count + "}}}\n\ndata: {\"type\":\"message_delta\",\"usage\":{\"output_tokens\":2}}\n\n"
			req := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
			resp := &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(body))}
			err := app.handleMessagesStream(httptest.NewRecorder(), req, time.Now(), nil, "alpha", policy.Context{}, nil, nil, resp, true, proxy.ForwardResult{BackendName: "alpha"})
			if err != nil {
				t.Fatal(err)
			}
			records, err := app.LogDB.List(t.Context(), 10, 0)
			if err != nil || len(records) != 1 {
				t.Fatalf("logs = %#v, err = %v", records, err)
			}
			details, _ := records[0].UsageStats["prompt_tokens_details"].(map[string]any)
			want := 0
			if count == "128" {
				want = 128
			}
			wantReported := count == "0" || count == "128"
			if value, reported := details["cached_tokens"]; reported != wantReported || (reported && value != want) {
				t.Fatalf("cached tokens = %v, reported = %v", value, reported)
			}
		})
	}
}

func TestPayloadRedactsConfiguredClientCredential(t *testing.T) {
	app := newLoggingRegressionApp(t, nil)
	app.Config.AppKeys.HeaderName = "X-Client-Key"
	app.Payloads = &logstore.FullRequestStore{Enabled: true, Dir: t.TempDir()}
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	req.Header.Set("X-Client-Key", "private-client-credential")
	req.Header.Set("X-Tenant", "team")
	req = requestWithRequiredHeaderAttribution(req, map[string]string{"x-client-key": "private-client-credential", "X-Tenant": "team"})
	path := app.storePayload(LogEntry{ID: 1, Method: req.Method, Path: req.URL.Path, Status: 200}, req, nil, nil, nil)
	payload, err := logstore.ReadPayload(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := payload.RequestHeaders["X-Client-Key"]; len(got) != 1 || got[0] != "[REDACTED]" {
		t.Fatalf("custom credential header was not redacted: %#v", got)
	}
	if payload.RequiredHeaders["x-client-key"] != "[REDACTED]" {
		t.Fatal("custom credential attribution was not redacted")
	}
	if payload.RequestHeaders["X-Tenant"][0] != "team" || payload.RequiredHeaders["X-Tenant"] != "team" {
		t.Fatal("non-sensitive attribution changed")
	}
	if req.Header.Get("X-Client-Key") != "private-client-credential" || requiredHeaderAttribution(req)["x-client-key"] != "private-client-credential" {
		t.Fatal("payload redaction mutated request")
	}
}
