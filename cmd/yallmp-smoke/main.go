package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/theio/yallmp-proxy/internal/config"
	"github.com/theio/yallmp-proxy/internal/observability"
	"github.com/theio/yallmp-proxy/internal/quota"
	"github.com/theio/yallmp-proxy/internal/server"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

type smokeClient struct {
	base    string
	client  *http.Client
	headers http.Header
}

type upstreamRequest struct {
	Path    string
	Payload map[string]any
	Header  http.Header
}

type capture struct {
	mu       sync.Mutex
	requests []upstreamRequest
}

type passthroughRequest struct {
	Path   string
	Query  string
	Body   string
	Marker string
	Auth   string
}

type passthroughCapture struct {
	mu       sync.Mutex
	requests []passthroughRequest
}

type sseClientEvent struct {
	Name string
	Data map[string]any
}

const (
	smokeGroupID        = "11111111111111111111111111111111"
	smokeGroupKeySecret = "smoke-group-secret"
)

func main() {
	mode := flag.String("mode", "fake", "smoke mode: fake or real")
	configPath := flag.String("config", defaultRealConfigPath(), "config path for real smoke")
	model := flag.String("model", defaultString(os.Getenv("YALLMP_SMOKE_MODEL"), "glm_local"), "model name for real smoke")
	timeout := flag.Duration("timeout", 180*time.Second, "client timeout")
	flag.Parse()

	var err error
	switch *mode {
	case "fake":
		err = runFakeSmoke(*timeout)
	case "real":
		err = runRealSmoke(*configPath, *model, *timeout)
	default:
		err = fmt.Errorf("unknown mode %q", *mode)
	}
	if err != nil {
		log.Fatalf("smoke failed: %v", err)
	}
	log.Printf("%s smoke passed", *mode)
}

func runFakeSmoke(timeout time.Duration) error {
	const smokeAdminPassword = "smoke-admin-password"
	previousAdminPassword, hadAdminPassword := os.LookupEnv(config.AdminPasswordEnv)
	if err := os.Setenv(config.AdminPasswordEnv, smokeAdminPassword); err != nil {
		return err
	}
	defer func() {
		if hadAdminPassword {
			_ = os.Setenv(config.AdminPasswordEnv, previousAdminPassword)
			return
		}
		_ = os.Unsetenv(config.AdminPasswordEnv)
	}()

	tempDir, err := os.MkdirTemp("", "yallmp-smoke-fake-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tempDir)

	seen := &capture{}
	upstream := httptest.NewServer(fakeUpstream(seen))
	defer upstream.Close()
	passthroughSeen := &passthroughCapture{}
	passthroughUpstream := httptest.NewServer(passthroughEchoUpstream(passthroughSeen))
	defer passthroughUpstream.Close()
	fallbackPrimarySeen := &capture{}
	fallbackPrimary := httptest.NewServer(failingFallbackUpstream(fallbackPrimarySeen))
	defer fallbackPrimary.Close()
	fallbackSecondarySeen := &capture{}
	fallbackSecondary := httptest.NewServer(successfulFallbackUpstream(fallbackSecondarySeen))
	defer fallbackSecondary.Close()

	cfg, err := config.FromMap(map[string]any{
		"model_list": []any{
			map[string]any{
				"model_name": "smoke-chat",
				"model_params": map[string]any{
					"api_base": upstream.URL + "/v1",
					"api_key":  "fake-upstream-key",
					"model":    "fake-upstream-model",
					"parameters": map[string]any{
						"stream_options.include_usage": map[string]any{"default": true, "allow_override": false},
					},
				},
			},
			map[string]any{
				"model_name": "smoke-passthrough",
				"model_params": map[string]any{
					"api_base": passthroughUpstream.URL,
					"api_key":  "fake-passthrough-key",
					"api_type": "passthrough",
				},
			},
			map[string]any{
				"model_name": "smoke-derived",
				"extends":    "smoke-chat",
				"protected":  false,
				"model_params": map[string]any{
					"model": "fake-upstream-derived-model",
				},
			},
			map[string]any{
				"model_name": "smoke-fallback-primary",
				"model_params": map[string]any{
					"api_base": fallbackPrimary.URL + "/v1",
					"api_key":  "fake-fallback-primary-key",
					"model":    "fake-fallback-primary-model",
				},
			},
			map[string]any{
				"model_name": "smoke-fallback-secondary",
				"model_params": map[string]any{
					"api_base": fallbackSecondary.URL + "/v1",
					"api_key":  "fake-fallback-secondary-key",
					"model":    "fake-fallback-secondary-model",
				},
			},
			map[string]any{
				"model_name": "smoke-parsed-sidecar",
				"model_params": map[string]any{
					"api_base": upstream.URL + "/v1",
					"api_key":  "fake-upstream-key",
					"model":    "fake-upstream-parsed-sidecar-model",
				},
				"modules": map[string]any{
					"enabled":  true,
					"response": []any{"parse_tags"},
				},
			},
			map[string]any{
				"model_name": "smoke-parsed-stream-sidecar",
				"model_params": map[string]any{
					"api_base": upstream.URL + "/v1",
					"api_key":  "fake-upstream-key",
					"model":    "fake-upstream-parsed-sidecar-model",
				},
				"modules": map[string]any{
					"enabled":  true,
					"response": []any{"parse_tags"},
				},
			},
			map[string]any{
				"model_name": "smoke-json-repair-stream",
				"model_params": map[string]any{
					"api_base": upstream.URL + "/v1",
					"api_key":  "fake-upstream-key",
					"model":    "fake-upstream-json-repair-model",
				},
				"modules": map[string]any{
					"enabled":  true,
					"response": []any{"stream_json_repair"},
					"stream_json_repair": map[string]any{
						"enabled": true,
					},
				},
			},
		},
		"router_settings": map[string]any{
			"fallbacks": []any{map[string]any{"smoke-fallback-primary": []any{"smoke-fallback-secondary"}}},
		},
		"proxy_settings": map[string]any{
			"enable_responses_endpoint": true,
			"enable_messages_endpoint":  true,
			"inference_cache": map[string]any{
				"mode":            "read_write",
				"ttl_seconds":     60,
				"max_age_seconds": 60,
				"max_entries":     32,
			},
			"metrics": map[string]any{
				"enabled": true,
			},
			"logging": map[string]any{
				"log_to_disk":         true,
				"log_parsed_response": true,
				"log_parsed_stream":   true,
				"full_request_storage": map[string]any{
					"enabled":         true,
					"path":            filepath.Join(tempDir, "requests"),
					"retention_hours": 1,
				},
			},
		},
		"database": sqliteConfig(filepath.Join(tempDir, "yaLLM.db")),
	}, "")
	if err != nil {
		return err
	}
	app, err := server.New(cfg)
	if err != nil {
		return err
	}
	defer app.LogDB.Close()
	otelProvider, otelRecorder := observability.NewInMemoryProvider("yallmp-smoke")
	app.SetObservability(otelProvider)
	defer app.ShutdownObservability(context.Background())
	proxy := httptest.NewServer(app)
	defer proxy.Close()

	client := smokeClient{base: proxy.URL, client: &http.Client{Timeout: timeout}}
	adminClient := client.withHeader("x-admin-password", smokeAdminPassword)
	start := time.Now().UTC().Add(-time.Second)
	if err := client.expectModels("smoke-chat"); err != nil {
		return err
	}
	if _, err := client.postJSON("/v1/chat/completions", map[string]any{
		"model":          "smoke-chat",
		"messages":       []any{map[string]any{"role": "user", "content": "fake smoke nonstream"}},
		"temperature":    0.2,
		"sentinel_param": "preserve-me",
	}); err != nil {
		return err
	}
	if body, err := client.postJSON("/v1/chat/completions", map[string]any{
		"model":          "smoke-chat",
		"messages":       []any{map[string]any{"role": "user", "content": "fake smoke stream"}},
		"stream":         true,
		"sentinel_param": "preserve-me-stream",
	}); err != nil {
		return err
	} else if !strings.Contains(string(body), "total_tokens") || !strings.Contains(string(body), "data: [DONE]") {
		return fmt.Errorf("stream response did not include usage and DONE sentinel: %s", string(body))
	}
	if body, err := client.postJSON("/v1/responses", map[string]any{
		"model":             "smoke-chat",
		"input":             "fake smoke responses nonstream",
		"max_output_tokens": 16,
	}); err != nil {
		return err
	} else if err := verifyResponsesUsageDetails(body); err != nil {
		return err
	}
	if body, err := client.postJSON("/v1/responses", map[string]any{
		"model":  "smoke-chat",
		"input":  "fake smoke responses stream",
		"stream": true,
	}); err != nil {
		return err
	} else if !strings.Contains(string(body), "event: response.completed") ||
		!strings.Contains(string(body), `"input_tokens_details":{"cached_tokens":2}`) ||
		!strings.Contains(string(body), `"output_tokens_details":{"reasoning_tokens":1}`) {
		return fmt.Errorf("stream responses smoke missing detailed usage: %s", string(body))
	}
	if body, err := client.postJSON("/v1/responses", map[string]any{
		"model": "smoke-chat",
		"input": "fake smoke responses reasoning nonstream",
	}); err != nil {
		return err
	} else if !strings.Contains(string(body), `"type":"reasoning"`) ||
		!strings.Contains(string(body), `"text":"hidden responses reasoning smoke"`) ||
		!strings.Contains(string(body), `"output_text":"visible responses reasoning smoke"`) {
		return fmt.Errorf("responses reasoning smoke missing translated reasoning item: %s", string(body))
	}
	if body, err := client.postJSON("/v1/responses", map[string]any{
		"model":  "smoke-chat",
		"input":  "fake smoke responses reasoning stream",
		"stream": true,
	}); err != nil {
		return err
	} else if err := verifyResponsesReasoningStream(body); err != nil {
		return err
	}
	if body, err := client.postJSON("/v1/responses", map[string]any{
		"model":       "smoke-chat",
		"input":       "fake smoke responses tool stream",
		"stream":      true,
		"tools":       []any{map[string]any{"type": "function", "name": "lookup", "parameters": map[string]any{"type": "object"}}},
		"tool_choice": "auto",
	}); err != nil {
		return err
	} else if err := verifyResponsesToolStream(body); err != nil {
		return err
	}
	if body, err := client.postJSON("/v1/responses", map[string]any{
		"model":  "smoke-chat",
		"input":  "fake smoke responses length stream",
		"stream": true,
	}); err != nil {
		return err
	} else if err := verifyResponsesTerminalStream(body, "response.incomplete", "incomplete", "max_output_tokens"); err != nil {
		return err
	}
	if body, err := client.postJSON("/v1/responses", map[string]any{
		"model":  "smoke-chat",
		"input":  "fake smoke responses content filter stream",
		"stream": true,
	}); err != nil {
		return err
	} else if err := verifyResponsesTerminalStream(body, "response.failed", "failed", "content_filter"); err != nil {
		return err
	}
	if body, err := client.postJSON("/v1/messages", map[string]any{
		"model":      "smoke-chat",
		"max_tokens": 16,
		"messages":   []any{map[string]any{"role": "user", "content": "fake smoke messages reasoning nonstream"}},
	}); err != nil {
		return err
	} else if !strings.Contains(string(body), `"type":"thinking"`) ||
		!strings.Contains(string(body), `"thinking":"hidden messages reasoning smoke"`) ||
		!strings.Contains(string(body), `"text":"visible messages reasoning smoke"`) {
		return fmt.Errorf("messages reasoning smoke missing translated thinking block: %s", string(body))
	}
	if body, err := client.postJSON("/v1/messages", map[string]any{
		"model":      "smoke-chat",
		"max_tokens": 16,
		"stream":     true,
		"messages":   []any{map[string]any{"role": "user", "content": "fake smoke messages reasoning stream"}},
	}); err != nil {
		return err
	} else if err := verifyMessagesReasoningStream(body); err != nil {
		return err
	}
	if body, err := client.postJSON("/v1/messages", map[string]any{
		"model":      "smoke-chat",
		"max_tokens": 16,
		"stream":     true,
		"messages":   []any{map[string]any{"role": "user", "content": "fake smoke messages tool stream"}},
	}); err != nil {
		return err
	} else if err := verifyMessagesToolStream(body); err != nil {
		return err
	}
	if _, err := client.postJSON("/v1/embeddings", map[string]any{
		"model": "smoke-chat",
		"input": "fake smoke embeddings",
	}); err != nil {
		return err
	}
	if err := verifyExactCacheSmoke(client, seen); err != nil {
		return err
	}
	if _, err := client.postJSON("/v1/rerank", map[string]any{
		"model":     "smoke-chat",
		"query":     "fake smoke",
		"documents": []any{"first", "second"},
		"top_n":     1,
	}); err != nil {
		return err
	}
	if body, err := client.postJSON("/v1/chat/completions", map[string]any{
		"model":          "smoke-parsed-sidecar",
		"messages":       []any{map[string]any{"role": "user", "content": "fake smoke parsed sidecar"}},
		"sentinel_param": "preserve-me-parsed-sidecar",
	}); err != nil {
		return err
	} else if strings.Contains(string(body), "<think>") || !strings.Contains(string(body), "reasoning_content") {
		return fmt.Errorf("parsed sidecar smoke response was not transformed: %s", string(body))
	}
	if body, err := client.postJSON("/v1/chat/completions", map[string]any{
		"model":          "smoke-parsed-stream-sidecar",
		"messages":       []any{map[string]any{"role": "user", "content": "fake smoke parsed stream sidecar"}},
		"stream":         true,
		"stream_options": map[string]any{"include_usage": true},
		"sentinel_param": "preserve-me-parsed-stream-sidecar",
	}); err != nil {
		return err
	} else if strings.Contains(string(body), "<think>") || !strings.Contains(string(body), "reasoning_content") || !strings.Contains(string(body), "visible stream smoke") {
		return fmt.Errorf("parsed stream sidecar smoke response was not transformed: %s", string(body))
	}
	if body, err := client.postJSON("/v1/chat/completions", map[string]any{
		"model":          "smoke-json-repair-stream",
		"messages":       []any{map[string]any{"role": "user", "content": "fake smoke json repair stream"}},
		"stream":         true,
		"stream_options": map[string]any{"include_usage": true},
		"sentinel_param": "preserve-me-json-repair-stream",
	}); err != nil {
		return err
	} else if args, err := streamToolArguments(body); err != nil {
		return err
	} else if args != `{"query":"cats"}` || !json.Valid([]byte(args)) {
		return fmt.Errorf("json repair stream arguments = %q body=%s", args, string(body))
	}
	if body, err := client.postJSON("/v1/chat/completions", map[string]any{
		"model":          "smoke-fallback-primary",
		"messages":       []any{map[string]any{"role": "user", "content": "fake smoke fallback"}},
		"sentinel_param": "preserve-me-fallback",
	}); err != nil {
		return err
	} else if !strings.Contains(string(body), "fallback smoke") {
		return fmt.Errorf("fallback response did not come from secondary upstream: %s", string(body))
	}
	if err := seen.verify(); err != nil {
		return err
	}
	if err := verifyResponsesSmokeRequests(seen); err != nil {
		return err
	}
	if err := verifyFallback(fallbackPrimarySeen, fallbackSecondarySeen); err != nil {
		return err
	}
	if err := verifyFallbackLog(client, "smoke-fallback-primary", start); err != nil {
		return err
	}
	if err := verifyAdminModelReadAPIs(client); err != nil {
		return err
	}
	if err := verifyMetrics(client); err != nil {
		return err
	}
	if err := verifyPassthroughSmoke(client, passthroughSeen); err != nil {
		return err
	}
	if err := verifyOTelSmoke(otelRecorder, client, start); err != nil {
		return err
	}
	if err := verifyParsedResponseSidecar(client, start); err != nil {
		return err
	}
	if err := verifyParsedStreamSidecar(client, start); err != nil {
		return err
	}
	if err := verifyAdminModelWriteAPIs(client); err != nil {
		return err
	}
	if err := verifyAdminRoutingCRUDAPIs(client, adminClient, upstream.URL); err != nil {
		return err
	}
	if err := verifyPprofDisabledByDefault(adminClient); err != nil {
		return err
	}
	if err := verifyGuardedAdminSmoke(timeout, smokeAdminPassword, tempDir); err != nil {
		return err
	}
	if err := verifyParsedResponseSidecarDisabled(timeout, tempDir); err != nil {
		return err
	}
	return verifyLogs(client, "smoke-chat", start, true)
}

func runRealSmoke(configPath string, model string, timeout time.Duration) error {
	if strings.TrimSpace(configPath) == "" {
		return errors.New("config path is required")
	}
	tempDir, err := os.MkdirTemp("", "yallmp-smoke-real-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tempDir)

	cfg, err := config.LoadWithOptions(config.LoadOptions{Path: configPath, SubstituteEnv: true})
	if err != nil {
		return err
	}
	if !configHasModel(cfg, model) {
		return fmt.Errorf("model %q not found in %s", model, configPath)
	}
	cfg.Database = sqliteConfig(filepath.Join(tempDir, "yaLLM.db"))
	cfg.ProxySettings.Logging.FullRequestStore = map[string]any{
		"enabled":         true,
		"path":            filepath.Join(tempDir, "requests"),
		"retention_hours": 1,
	}
	app, err := server.New(cfg)
	if err != nil {
		return err
	}
	defer app.LogDB.Close()
	proxy := httptest.NewServer(app)
	defer proxy.Close()

	client, err := smokeClientForConfig(proxy.URL, &http.Client{Timeout: timeout}, cfg)
	if err != nil {
		return err
	}
	start := time.Now().UTC().Add(-time.Second)
	if err := client.expectModels(model); err != nil {
		return err
	}
	body, err := client.postJSON("/v1/chat/completions", map[string]any{
		"model":       model,
		"messages":    []any{map[string]any{"role": "user", "content": "Reply with one short sentence for a yaLLMproxy smoke test."}},
		"temperature": 0,
		"max_tokens":  64,
		"user":        "yallmp-smoke",
	})
	if err != nil {
		return err
	}
	if !jsonHasAssistantText(body) {
		return fmt.Errorf("nonstream response did not contain assistant text: %s", string(body))
	}
	streamBody, err := client.postJSON("/v1/chat/completions", map[string]any{
		"model":          model,
		"messages":       []any{map[string]any{"role": "user", "content": "Stream a tiny yaLLMproxy smoke response."}},
		"stream":         true,
		"temperature":    0,
		"max_tokens":     64,
		"stream_options": map[string]any{"include_usage": true},
	})
	if err != nil {
		return err
	}
	if !strings.Contains(string(streamBody), "data:") {
		return fmt.Errorf("stream response did not look like SSE: %s", string(streamBody))
	}
	return verifyLogs(client, model, start, false)
}

func fakeUpstream(seen *capture) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		payload, ok := readFakeRequest(w, r, "fake-upstream-key", seen)
		if !ok {
			return
		}
		switch r.URL.Path {
		case "/v1/chat/completions":
			if payload["model"] != "fake-upstream-model" && payload["model"] != "fake-upstream-derived-model" && payload["model"] != "fake-upstream-parsed-sidecar-model" && payload["model"] != "fake-upstream-json-repair-model" {
				http.Error(w, "model was not rewritten", http.StatusBadRequest)
				return
			}
			w.Header().Set("Content-Type", contentType(payload))
			if stream, _ := payload["stream"].(bool); stream {
				if got, _ := nestedBool(payload, "stream_options", "include_usage"); !got {
					http.Error(w, "stream_options.include_usage was not forced for stream", http.StatusBadRequest)
					return
				}
				if requestHasMessageContent(payload, "fake smoke responses reasoning stream") {
					_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"role\":\"assistant\"},\"index\":0}]}\n\n"))
					_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"reasoning_content\":\"hidden responses reasoning stream smoke\"},\"index\":0}]}\n\n"))
					_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"visible responses reasoning stream smoke\"},\"index\":0}]}\n\n"))
					_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\",\"index\":0}],\"usage\":{\"prompt_tokens\":3,\"completion_tokens\":4,\"total_tokens\":7,\"prompt_tokens_details\":{\"cached_tokens\":2},\"completion_tokens_details\":{\"reasoning_tokens\":1}}}\n\n"))
					_, _ = w.Write([]byte("data: [DONE]\n\n"))
					return
				}
				if requestHasMessageContent(payload, "fake smoke messages reasoning stream") {
					_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"role\":\"assistant\"},\"index\":0}]}\n\n"))
					_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"reasoning_content\":\"hidden messages reasoning stream smoke\"},\"index\":0}]}\n\n"))
					_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"visible messages reasoning stream smoke\"},\"index\":0}]}\n\n"))
					_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\",\"index\":0}],\"usage\":{\"prompt_tokens\":3,\"completion_tokens\":4,\"total_tokens\":7,\"prompt_tokens_details\":{\"cached_tokens\":2},\"completion_tokens_details\":{\"reasoning_tokens\":1}}}\n\n"))
					_, _ = w.Write([]byte("data: [DONE]\n\n"))
					return
				}
				if requestHasMessageContent(payload, "fake smoke messages tool stream") {
					writeFakeToolCallStream(w, "call_messages_smoke")
					return
				}
				if requestHasMessageContent(payload, "fake smoke responses tool stream") {
					writeFakeToolCallStream(w, "call_responses_smoke")
					return
				}
				if requestHasMessageContent(payload, "fake smoke responses length stream") {
					_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"role\":\"assistant\"},\"index\":0}]}\n\n"))
					_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"partial responses length smoke\"},\"index\":0}]}\n\n"))
					_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"length\",\"index\":0}],\"usage\":{\"prompt_tokens\":3,\"completion_tokens\":4,\"total_tokens\":7}}\n\n"))
					_, _ = w.Write([]byte("data: [DONE]\n\n"))
					return
				}
				if requestHasMessageContent(payload, "fake smoke responses content filter stream") {
					_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"role\":\"assistant\"},\"index\":0}]}\n\n"))
					_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"content_filter\",\"index\":0}],\"usage\":{\"prompt_tokens\":3,\"completion_tokens\":0,\"total_tokens\":3}}\n\n"))
					_, _ = w.Write([]byte("data: [DONE]\n\n"))
					return
				}
				if payload["model"] == "fake-upstream-parsed-sidecar-model" {
					_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"role\":\"assistant\"},\"index\":0}]}\n\n"))
					_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"<think>smoke stream hidden</think>visible stream smoke\"},\"index\":0}]}\n\n"))
					_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\",\"index\":0}],\"usage\":{\"prompt_tokens\":3,\"completion_tokens\":4,\"total_tokens\":7,\"prompt_tokens_details\":{\"cached_tokens\":2},\"completion_tokens_details\":{\"reasoning_tokens\":1}}}\n\n"))
					_, _ = w.Write([]byte("data: [DONE]\n\n"))
					return
				}
				if payload["model"] == "fake-upstream-json-repair-model" {
					_, _ = w.Write([]byte(`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_smoke","type":"function","function":{"name":"lookup","arguments":"{\"query\""}}]},"index":0}]}` + "\n\n"))
					_, _ = w.Write([]byte(`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":":\"cats\","}}]},"index":0}]}` + "\n\n"))
					_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"tool_calls\",\"index\":0}],\"usage\":{\"prompt_tokens\":3,\"completion_tokens\":4,\"total_tokens\":7,\"prompt_tokens_details\":{\"cached_tokens\":2},\"completion_tokens_details\":{\"reasoning_tokens\":1}}}\n\n"))
					_, _ = w.Write([]byte("data: [DONE]\n\n"))
					return
				}
				_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"role\":\"assistant\"},\"index\":0}]}\n\n"))
				_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"fake smoke\"},\"index\":0}]}\n\n"))
				_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\",\"index\":0}],\"usage\":{\"prompt_tokens\":3,\"completion_tokens\":4,\"total_tokens\":7,\"prompt_tokens_details\":{\"cached_tokens\":2},\"completion_tokens_details\":{\"reasoning_tokens\":1}}}\n\n"))
				_, _ = w.Write([]byte("data: [DONE]\n\n"))
				return
			}
			if requestHasMessageContent(payload, "fake smoke responses reasoning nonstream") {
				_, _ = w.Write([]byte(`{"id":"chatcmpl-responses-reasoning-smoke","object":"chat.completion","model":"fake-upstream-model","choices":[{"index":0,"finish_reason":"stop","message":{"role":"assistant","reasoning_content":"hidden responses reasoning smoke","content":"visible responses reasoning smoke"}}],"usage":{"prompt_tokens":3,"completion_tokens":4,"total_tokens":7,"prompt_tokens_details":{"cached_tokens":2},"completion_tokens_details":{"reasoning_tokens":1}}}`))
				return
			}
			if requestHasMessageContent(payload, "fake smoke messages reasoning nonstream") {
				_, _ = w.Write([]byte(`{"id":"chatcmpl-messages-reasoning-smoke","object":"chat.completion","model":"fake-upstream-model","choices":[{"index":0,"finish_reason":"stop","message":{"role":"assistant","reasoning_content":"hidden messages reasoning smoke","content":"visible messages reasoning smoke"}}],"usage":{"prompt_tokens":3,"completion_tokens":4,"total_tokens":7,"prompt_tokens_details":{"cached_tokens":2},"completion_tokens_details":{"reasoning_tokens":1}}}`))
				return
			}
			if payload["model"] == "fake-upstream-parsed-sidecar-model" {
				_, _ = w.Write([]byte(`{"id":"chatcmpl-parsed-sidecar-smoke","object":"chat.completion","model":"fake-upstream-parsed-sidecar-model","choices":[{"index":0,"finish_reason":"stop","message":{"role":"assistant","content":"<think>smoke hidden</think>visible smoke"}}],"usage":{"prompt_tokens":3,"completion_tokens":4,"total_tokens":7}}`))
				return
			}
			_, _ = w.Write([]byte(`{"id":"chatcmpl-smoke","object":"chat.completion","model":"fake-upstream-model","choices":[{"index":0,"finish_reason":"stop","message":{"role":"assistant","content":"fake smoke"}}],"usage":{"prompt_tokens":3,"completion_tokens":4,"total_tokens":7,"prompt_tokens_details":{"cached_tokens":2},"completion_tokens_details":{"reasoning_tokens":1}}}`))
		case "/v1/embeddings":
			if payload["model"] != "fake-upstream-model" {
				http.Error(w, "embedding model was not rewritten", http.StatusBadRequest)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"object":"list","data":[{"object":"embedding","index":0,"embedding":[0.1,0.2]}],"model":"fake-upstream-model","usage":{"prompt_tokens":2,"total_tokens":2}}`))
		case "/v1/rerank":
			if payload["model"] != "fake-upstream-model" {
				http.Error(w, "rerank model was not rewritten", http.StatusBadRequest)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"rerank-smoke","model":"fake-upstream-model","usage":{"total_tokens":6},"results":[{"index":0,"relevance_score":0.9}]}`))
		default:
			http.NotFound(w, r)
		}
	})
}

func writeFakeToolCallStream(w http.ResponseWriter, callID string) {
	_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"role\":\"assistant\"},\"index\":0}]}\n\n"))
	_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"Checking\"},\"index\":0}]}\n\n"))
	_, _ = w.Write([]byte(`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"` + callID + `","type":"function","function":{"name":"lookup","arguments":"{\"q\""}}]},"index":0}]}` + "\n\n"))
	_, _ = w.Write([]byte(`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":":\"x\"}"}}]},"index":0}]}` + "\n\n"))
	_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"tool_calls\",\"index\":0}],\"usage\":{\"prompt_tokens\":3,\"completion_tokens\":4,\"total_tokens\":7}}\n\n"))
	_, _ = w.Write([]byte("data: [DONE]\n\n"))
}

func failingFallbackUpstream(seen *capture) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		payload, ok := readFakeRequest(w, r, "fake-fallback-primary-key", seen)
		if !ok {
			return
		}
		if r.URL.Path != "/v1/chat/completions" {
			http.NotFound(w, r)
			return
		}
		if payload["model"] != "fake-fallback-primary-model" {
			http.Error(w, "primary model was not rewritten", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"error":{"message":"primary unavailable","type":"upstream_unavailable"}}`))
	})
}

func successfulFallbackUpstream(seen *capture) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		payload, ok := readFakeRequest(w, r, "fake-fallback-secondary-key", seen)
		if !ok {
			return
		}
		if r.URL.Path != "/v1/chat/completions" {
			http.NotFound(w, r)
			return
		}
		if payload["model"] != "fake-fallback-secondary-model" {
			http.Error(w, "secondary model was not rewritten", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl-fallback-smoke","object":"chat.completion","model":"fake-fallback-secondary-model","choices":[{"index":0,"finish_reason":"stop","message":{"role":"assistant","content":"fallback smoke"}}],"usage":{"prompt_tokens":2,"completion_tokens":3,"total_tokens":5}}`))
	})
}

func readFakeRequest(w http.ResponseWriter, r *http.Request, apiKey string, seen *capture) (map[string]any, bool) {
	if r.Header.Get("Authorization") != "Bearer "+apiKey {
		http.Error(w, "missing upstream auth", http.StatusUnauthorized)
		return nil, false
	}
	body, _ := io.ReadAll(r.Body)
	payload := map[string]any{}
	if len(body) > 0 {
		if err := json.Unmarshal(body, &payload); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return nil, false
		}
	}
	seen.add(r.URL.Path, payload, r.Header)
	return payload, true
}

func passthroughEchoUpstream(seen *passthroughCapture) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if r.Header.Get("Authorization") != "Bearer fake-passthrough-key" {
			http.Error(w, "missing passthrough upstream auth", http.StatusUnauthorized)
			return
		}
		seen.add(passthroughRequest{
			Path:   r.URL.Path,
			Query:  r.URL.RawQuery,
			Body:   string(body),
			Marker: r.Header.Get("X-Smoke-Marker"),
			Auth:   r.Header.Get("Authorization"),
		})
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Passthrough-Marker", "preserved")
		_, _ = w.Write([]byte(`{"passthrough":"ok"}`))
	})
}

func (c *capture) add(path string, payload map[string]any, header http.Header) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.requests = append(c.requests, upstreamRequest{Path: path, Payload: payload, Header: header.Clone()})
}

func (c *capture) snapshot() []upstreamRequest {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]upstreamRequest(nil), c.requests...)
}

func (c *passthroughCapture) add(req passthroughRequest) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.requests = append(c.requests, req)
}

func (c *passthroughCapture) snapshot() []passthroughRequest {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]passthroughRequest(nil), c.requests...)
}

func (c *capture) countMatching(path string, match func(map[string]any) bool) int {
	count := 0
	for _, req := range c.snapshot() {
		if req.Path == path && match(req.Payload) {
			count++
		}
	}
	return count
}

func (c *capture) verify() error {
	paths := map[string]int{}
	for _, req := range c.snapshot() {
		paths[req.Path]++
		if req.Path == "/v1/chat/completions" {
			sentinel := fmt.Sprint(req.Payload["sentinel_param"])
			if !strings.HasPrefix(sentinel, "preserve-me") && !strings.HasPrefix(sentinel, "cache-chat") && !isTranslatedResponsesSmoke(req.Payload) && !isTranslatedMessagesSmoke(req.Payload) {
				return fmt.Errorf("chat sentinel_param was not forwarded: %#v", req.Payload)
			}
		}
	}
	for _, path := range []string{"/v1/chat/completions", "/v1/embeddings", "/v1/rerank"} {
		if paths[path] == 0 {
			return fmt.Errorf("fake upstream did not receive %s", path)
		}
	}
	return nil
}

func verifyResponsesSmokeRequests(seen *capture) error {
	var nonstreamSeen bool
	var streamSeen bool
	for _, req := range seen.snapshot() {
		if req.Path != "/v1/chat/completions" {
			continue
		}
		if requestHasMessageContent(req.Payload, "fake smoke responses nonstream") {
			nonstreamSeen = req.Payload["max_tokens"] == float64(16)
		}
		if requestHasMessageContent(req.Payload, "fake smoke responses stream") {
			streamSeen = req.Payload["stream"] == true
		}
	}
	if !nonstreamSeen {
		return errors.New("fake responses nonstream request was not translated to chat completions")
	}
	if !streamSeen {
		return errors.New("fake responses stream request was not translated to chat completions")
	}
	return nil
}

func streamToolArguments(body []byte) (string, error) {
	var out strings.Builder
	for _, event := range strings.Split(string(body), "\n\n") {
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
				return "", fmt.Errorf("invalid stream JSON %q: %w", raw, err)
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
	if out.Len() == 0 {
		return "", fmt.Errorf("stream did not include tool arguments: %s", string(body))
	}
	return out.String(), nil
}

func parseSSEEvents(body []byte) ([]sseClientEvent, error) {
	events := make([]sseClientEvent, 0)
	for _, event := range strings.Split(string(body), "\n\n") {
		event = strings.TrimSpace(event)
		if event == "" {
			continue
		}
		parsed := sseClientEvent{}
		for _, line := range strings.Split(event, "\n") {
			switch {
			case strings.HasPrefix(line, "event:"):
				parsed.Name = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
			case strings.HasPrefix(line, "data:"):
				raw := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
				if raw == "" || raw == "[DONE]" {
					continue
				}
				data := map[string]any{}
				if err := json.Unmarshal([]byte(raw), &data); err != nil {
					return nil, fmt.Errorf("invalid SSE data %q: %w", raw, err)
				}
				parsed.Data = data
			}
		}
		if parsed.Name != "" || parsed.Data != nil {
			events = append(events, parsed)
		}
	}
	return events, nil
}

func hasSSEEvent(events []sseClientEvent, name string, match func(map[string]any) bool) bool {
	for _, event := range events {
		if event.Name != name {
			continue
		}
		if match == nil || match(event.Data) {
			return true
		}
	}
	return false
}

func countSSEEvents(events []sseClientEvent, name string, match func(map[string]any) bool) int {
	count := 0
	for _, event := range events {
		if event.Name != name {
			continue
		}
		if match == nil || match(event.Data) {
			count++
		}
	}
	return count
}

func mapValue(value any) map[string]any {
	out, _ := value.(map[string]any)
	return out
}

func verifyMessagesToolStream(body []byte) error {
	events, err := parseSSEEvents(body)
	if err != nil {
		return fmt.Errorf("messages tool stream parse: %w body=%s", err, string(body))
	}
	if !hasSSEEvent(events, "content_block_start", func(data map[string]any) bool {
		block := mapValue(data["content_block"])
		return block["type"] == "tool_use" && block["id"] == "call_messages_smoke" && block["name"] == "lookup"
	}) {
		return fmt.Errorf("messages tool stream missing tool_use start: %s", string(body))
	}
	if got := countSSEEvents(events, "content_block_delta", func(data map[string]any) bool {
		return mapValue(data["delta"])["type"] == "input_json_delta"
	}); got != 2 {
		return fmt.Errorf("messages tool stream input_json_delta count = %d body=%s", got, string(body))
	}
	if !hasSSEEvent(events, "message_delta", func(data map[string]any) bool {
		return mapValue(data["delta"])["stop_reason"] == "tool_use"
	}) {
		return fmt.Errorf("messages tool stream missing tool_use stop_reason: %s", string(body))
	}
	return nil
}

func verifyMessagesReasoningStream(body []byte) error {
	events, err := parseSSEEvents(body)
	if err != nil {
		return fmt.Errorf("messages reasoning stream parse: %w body=%s", err, string(body))
	}
	if !hasSSEEvent(events, "content_block_delta", func(data map[string]any) bool {
		delta := mapValue(data["delta"])
		return delta["type"] == "thinking_delta" && delta["thinking"] == "hidden messages reasoning stream smoke"
	}) {
		return fmt.Errorf("messages reasoning stream missing thinking delta: %s", string(body))
	}
	if !hasSSEEvent(events, "content_block_delta", func(data map[string]any) bool {
		delta := mapValue(data["delta"])
		return delta["type"] == "text_delta" && delta["text"] == "visible messages reasoning stream smoke"
	}) {
		return fmt.Errorf("messages reasoning stream missing text delta: %s", string(body))
	}
	if !hasSSEEvent(events, "message_delta", func(data map[string]any) bool {
		return mapValue(data["delta"])["stop_reason"] == "end_turn"
	}) {
		return fmt.Errorf("messages reasoning stream missing end_turn stop_reason: %s", string(body))
	}
	return nil
}

func verifyResponsesToolStream(body []byte) error {
	events, err := parseSSEEvents(body)
	if err != nil {
		return fmt.Errorf("responses tool stream parse: %w body=%s", err, string(body))
	}
	if !hasSSEEvent(events, "response.output_item.added", func(data map[string]any) bool {
		item := mapValue(data["item"])
		return item["type"] == "function_call" && item["id"] == "call_responses_smoke"
	}) {
		return fmt.Errorf("responses tool stream missing function_call item: %s", string(body))
	}
	if got := countSSEEvents(events, "response.function_call_arguments.delta", nil); got != 2 {
		return fmt.Errorf("responses tool stream argument delta count = %d body=%s", got, string(body))
	}
	if !hasSSEEvent(events, "response.output_item.done", func(data map[string]any) bool {
		item := mapValue(data["item"])
		return item["type"] == "function_call" && item["name"] == "lookup" && item["arguments"] == `{"q":"x"}`
	}) {
		return fmt.Errorf("responses tool stream missing completed function_call item: %s", string(body))
	}
	if !hasSSEEvent(events, "response.completed", func(data map[string]any) bool {
		return mapValue(data["response"])["status"] == "completed"
	}) {
		return fmt.Errorf("responses tool stream missing completed terminal event: %s", string(body))
	}
	return nil
}

func verifyResponsesReasoningStream(body []byte) error {
	events, err := parseSSEEvents(body)
	if err != nil {
		return fmt.Errorf("responses reasoning stream parse: %w body=%s", err, string(body))
	}
	if !hasSSEEvent(events, "response.reasoning_summary_text.delta", func(data map[string]any) bool {
		return data["delta"] == "hidden responses reasoning stream smoke"
	}) {
		return fmt.Errorf("responses reasoning stream missing reasoning delta: %s", string(body))
	}
	if !hasSSEEvent(events, "response.reasoning_summary_text.done", nil) {
		return fmt.Errorf("responses reasoning stream missing reasoning done event: %s", string(body))
	}
	if !hasSSEEvent(events, "response.completed", func(data map[string]any) bool {
		response := mapValue(data["response"])
		return response["status"] == "completed" && response["output_text"] == "visible responses reasoning stream smoke"
	}) {
		return fmt.Errorf("responses reasoning stream missing completed response output_text: %s", string(body))
	}
	return nil
}

func verifyResponsesTerminalStream(body []byte, eventName string, status string, reasonOrCode string) error {
	events, err := parseSSEEvents(body)
	if err != nil {
		return fmt.Errorf("responses terminal stream parse: %w body=%s", err, string(body))
	}
	if !hasSSEEvent(events, eventName, func(data map[string]any) bool {
		response := mapValue(data["response"])
		if response["status"] != status {
			return false
		}
		if status == "incomplete" {
			return mapValue(response["incomplete_details"])["reason"] == reasonOrCode
		}
		if status == "failed" {
			return mapValue(response["error"])["code"] == reasonOrCode
		}
		return true
	}) {
		return fmt.Errorf("responses terminal stream missing %s/%s/%s: %s", eventName, status, reasonOrCode, string(body))
	}
	return nil
}

func verifyExactCacheSmoke(client smokeClient, seen *capture) error {
	cacheChat := func(payload map[string]any) bool {
		return payload["sentinel_param"] == "cache-chat"
	}
	cacheChatMiss := func(payload map[string]any) bool {
		return payload["sentinel_param"] == "cache-chat-miss"
	}
	chatBefore := seen.countMatching("/v1/chat/completions", cacheChat)
	firstChat, err := client.postJSON("/v1/chat/completions", map[string]any{
		"model":          "smoke-chat",
		"messages":       []any{map[string]any{"role": "user", "content": "fake smoke exact cache"}},
		"temperature":    0.2,
		"sentinel_param": "cache-chat",
	})
	if err != nil {
		return err
	}
	secondChat, err := client.postJSON("/v1/chat/completions", map[string]any{
		"model":          "smoke-chat",
		"messages":       []any{map[string]any{"role": "user", "content": "fake smoke exact cache"}},
		"temperature":    0.2,
		"sentinel_param": "cache-chat",
	})
	if err != nil {
		return err
	}
	if !bytes.Equal(firstChat, secondChat) {
		return fmt.Errorf("exact cache chat responses differed: %s != %s", string(firstChat), string(secondChat))
	}
	if got := seen.countMatching("/v1/chat/completions", cacheChat) - chatBefore; got != 1 {
		return fmt.Errorf("exact cache chat upstream hits = %d, want 1", got)
	}
	if _, err := client.withHeader("x-yallmp-cache-bypass", "true").postJSON("/v1/chat/completions", map[string]any{
		"model":          "smoke-chat",
		"messages":       []any{map[string]any{"role": "user", "content": "fake smoke exact cache"}},
		"temperature":    0.2,
		"sentinel_param": "cache-chat",
	}); err != nil {
		return err
	}
	if got := seen.countMatching("/v1/chat/completions", cacheChat) - chatBefore; got != 2 {
		return fmt.Errorf("exact cache bypass chat upstream hits = %d, want 2", got)
	}
	for _, req := range seen.snapshot() {
		if req.Path == "/v1/chat/completions" && cacheChat(req.Payload) && req.Header.Get("x-yallmp-cache-bypass") != "" {
			return fmt.Errorf("exact cache bypass header leaked upstream: %#v", req.Header)
		}
	}
	chatMissBefore := seen.countMatching("/v1/chat/completions", cacheChatMiss)
	if _, err := client.postJSON("/v1/chat/completions", map[string]any{
		"model":          "smoke-chat",
		"messages":       []any{map[string]any{"role": "user", "content": "fake smoke exact cache changed"}},
		"temperature":    0.2,
		"sentinel_param": "cache-chat-miss",
	}); err != nil {
		return err
	}
	if got := seen.countMatching("/v1/chat/completions", cacheChatMiss) - chatMissBefore; got != 1 {
		return fmt.Errorf("exact cache changed chat upstream hits = %d, want 1", got)
	}

	cacheEmbedding := func(payload map[string]any) bool {
		return payload["input"] == "fake smoke exact cache embeddings"
	}
	cacheEmbeddingMiss := func(payload map[string]any) bool {
		return payload["input"] == "fake smoke exact cache embeddings changed"
	}
	embBefore := seen.countMatching("/v1/embeddings", cacheEmbedding)
	firstEmbedding, err := client.postJSON("/v1/embeddings", map[string]any{
		"model": "smoke-chat",
		"input": "fake smoke exact cache embeddings",
	})
	if err != nil {
		return err
	}
	secondEmbedding, err := client.postJSON("/v1/embeddings", map[string]any{
		"model": "smoke-chat",
		"input": "fake smoke exact cache embeddings",
	})
	if err != nil {
		return err
	}
	if !bytes.Equal(firstEmbedding, secondEmbedding) {
		return fmt.Errorf("exact cache embedding responses differed: %s != %s", string(firstEmbedding), string(secondEmbedding))
	}
	if got := seen.countMatching("/v1/embeddings", cacheEmbedding) - embBefore; got != 1 {
		return fmt.Errorf("exact cache embeddings upstream hits = %d, want 1", got)
	}
	embMissBefore := seen.countMatching("/v1/embeddings", cacheEmbeddingMiss)
	if _, err := client.postJSON("/v1/embeddings", map[string]any{
		"model": "smoke-chat",
		"input": "fake smoke exact cache embeddings changed",
	}); err != nil {
		return err
	}
	if got := seen.countMatching("/v1/embeddings", cacheEmbeddingMiss) - embMissBefore; got != 1 {
		return fmt.Errorf("exact cache changed embeddings upstream hits = %d, want 1", got)
	}

	cacheMessages := func(payload map[string]any) bool {
		return requestHasMessageContent(payload, "fake smoke exact cache messages")
	}
	messagesBefore := seen.countMatching("/v1/chat/completions", cacheMessages)
	firstMessages, err := client.postJSON("/v1/messages", map[string]any{
		"model":      "smoke-chat",
		"max_tokens": 16,
		"messages":   []any{map[string]any{"role": "user", "content": "fake smoke exact cache messages"}},
	})
	if err != nil {
		return err
	}
	secondMessages, err := client.postJSON("/v1/messages", map[string]any{
		"model":      "smoke-chat",
		"max_tokens": 16,
		"messages":   []any{map[string]any{"role": "user", "content": "fake smoke exact cache messages"}},
	})
	if err != nil {
		return err
	}
	if !bytes.Equal(firstMessages, secondMessages) {
		return fmt.Errorf("exact cache messages responses differed: %s != %s", string(firstMessages), string(secondMessages))
	}
	if got := seen.countMatching("/v1/chat/completions", cacheMessages) - messagesBefore; got != 1 {
		return fmt.Errorf("exact cache messages upstream hits = %d, want 1", got)
	}
	if _, err := client.withHeader("x-yallmp-cache-bypass", "true").postJSON("/v1/messages", map[string]any{
		"model":      "smoke-chat",
		"max_tokens": 16,
		"messages":   []any{map[string]any{"role": "user", "content": "fake smoke exact cache messages"}},
	}); err != nil {
		return err
	}
	if got := seen.countMatching("/v1/chat/completions", cacheMessages) - messagesBefore; got != 2 {
		return fmt.Errorf("exact cache messages bypass upstream hits = %d, want 2", got)
	}

	cacheResponses := func(payload map[string]any) bool {
		return requestHasMessageContent(payload, "fake smoke exact cache responses")
	}
	responsesBefore := seen.countMatching("/v1/chat/completions", cacheResponses)
	firstResponses, err := client.postJSON("/v1/responses", map[string]any{
		"model": "smoke-chat",
		"input": "fake smoke exact cache responses",
	})
	if err != nil {
		return err
	}
	secondResponses, err := client.postJSON("/v1/responses", map[string]any{
		"model": "smoke-chat",
		"input": "fake smoke exact cache responses",
	})
	if err != nil {
		return err
	}
	if !bytes.Equal(firstResponses, secondResponses) {
		return fmt.Errorf("exact cache responses responses differed: %s != %s", string(firstResponses), string(secondResponses))
	}
	if got := seen.countMatching("/v1/chat/completions", cacheResponses) - responsesBefore; got != 1 {
		return fmt.Errorf("exact cache responses upstream hits = %d, want 1", got)
	}
	if _, err := client.withHeader("x-yallmp-cache-bypass", "true").postJSON("/v1/responses", map[string]any{
		"model": "smoke-chat",
		"input": "fake smoke exact cache responses",
	}); err != nil {
		return err
	}
	if got := seen.countMatching("/v1/chat/completions", cacheResponses) - responsesBefore; got != 2 {
		return fmt.Errorf("exact cache responses bypass upstream hits = %d, want 2", got)
	}
	for _, req := range seen.snapshot() {
		if req.Path == "/v1/chat/completions" && (cacheMessages(req.Payload) || cacheResponses(req.Payload)) && req.Header.Get("x-yallmp-cache-bypass") != "" {
			return fmt.Errorf("translated exact cache bypass header leaked upstream: %#v", req.Header)
		}
	}
	return nil
}

func verifyFallback(primary *capture, secondary *capture) error {
	primaryRequests := primary.snapshot()
	if len(primaryRequests) != 1 {
		return fmt.Errorf("fallback primary attempts = %d, want 1: %#v", len(primaryRequests), primaryRequests)
	}
	if err := verifyFallbackRequest(primaryRequests[0], "fake-fallback-primary-model"); err != nil {
		return fmt.Errorf("fallback primary request: %w", err)
	}
	secondaryRequests := secondary.snapshot()
	if len(secondaryRequests) != 1 {
		return fmt.Errorf("fallback secondary attempts = %d, want 1: %#v", len(secondaryRequests), secondaryRequests)
	}
	if err := verifyFallbackRequest(secondaryRequests[0], "fake-fallback-secondary-model"); err != nil {
		return fmt.Errorf("fallback secondary request: %w", err)
	}
	return nil
}

func verifyFallbackRequest(req upstreamRequest, targetModel string) error {
	if req.Path != "/v1/chat/completions" {
		return fmt.Errorf("path = %s", req.Path)
	}
	if req.Payload["model"] != targetModel {
		return fmt.Errorf("model = %#v, want %q; payload=%#v", req.Payload["model"], targetModel, req.Payload)
	}
	if req.Payload["sentinel_param"] != "preserve-me-fallback" {
		return fmt.Errorf("sentinel_param was not forwarded: %#v", req.Payload)
	}
	return nil
}

func verifyAdminModelReadAPIs(client smokeClient) error {
	tree, err := client.getJSON("/admin/models/tree")
	if err != nil {
		return err
	}
	nodes, _ := tree["nodes"].(map[string]any)
	derived, _ := nodes["smoke-derived"].(map[string]any)
	if derived == nil || derived["parent"] != "smoke-chat" {
		return fmt.Errorf("smoke-derived tree node mismatch: %#v", derived)
	}
	chat, _ := nodes["smoke-chat"].(map[string]any)
	if chat == nil || !containsString(jsonStringList(chat["children"]), "smoke-derived") {
		return fmt.Errorf("smoke-chat children missing smoke-derived: %#v", chat)
	}

	ancestry, err := client.getJSON("/admin/models/smoke-derived/ancestry")
	if err != nil {
		return err
	}
	if got := jsonStringList(ancestry["chain"]); len(got) != 2 || got[0] != "smoke-derived" || got[1] != "smoke-chat" {
		return fmt.Errorf("smoke-derived ancestry = %#v", ancestry)
	}

	dependents, err := client.getJSON("/admin/models/smoke-chat/dependents")
	if err != nil {
		return err
	}
	if !containsString(jsonStringList(dependents["direct_children"]), "smoke-derived") {
		return fmt.Errorf("smoke-chat direct_children missing smoke-derived: %#v", dependents)
	}
	if !containsString(jsonStringList(dependents["all_descendants"]), "smoke-derived") {
		return fmt.Errorf("smoke-chat all_descendants missing smoke-derived: %#v", dependents)
	}
	return nil
}

func verifyAdminModelWriteAPIs(client smokeClient) error {
	const copiedModel = "smoke-derived-copy"
	copyPath := "/admin/models/copy?source=" + url.QueryEscape("smoke-derived") + "&target=" + url.QueryEscape(copiedModel)
	if _, err := client.postJSON(copyPath, nil); err != nil {
		return fmt.Errorf("copy model: %w", err)
	}
	if err := client.expectModels(copiedModel); err != nil {
		return err
	}
	if _, err := client.postJSON("/v1/chat/completions", map[string]any{
		"model":          copiedModel,
		"messages":       []any{map[string]any{"role": "user", "content": "fake smoke copied model"}},
		"sentinel_param": "preserve-me-copy",
	}); err != nil {
		return err
	}
	deletePath := "/admin/models/" + url.PathEscape(copiedModel)
	if err := client.deleteOK(deletePath); err != nil {
		return fmt.Errorf("delete copied model: %w", err)
	}
	if err := client.expectModelsAbsent(copiedModel); err != nil {
		return err
	}
	if err := client.expectPostJSONFailure("/v1/chat/completions", map[string]any{
		"model":    copiedModel,
		"messages": []any{map[string]any{"role": "user", "content": "fake smoke deleted model"}},
	}); err != nil {
		return err
	}
	return nil
}

func verifyAdminRoutingCRUDAPIs(client smokeClient, adminClient smokeClient, upstreamURL string) error {
	const (
		providerName = "smoke-provider-crud"
		nodeName     = "smoke-node-crud"
		modelName    = "smoke-client-crud"
	)
	if _, err := adminClient.postJSON("/admin/providers", map[string]any{
		"name":          providerName,
		"api_base":      upstreamURL + "/v1",
		"api_key":       "fake-upstream-key",
		"api_type":      "openai",
		"custom_marker": "provider-raw",
	}); err != nil {
		return fmt.Errorf("create provider: %w", err)
	}
	provider, err := adminClient.getJSON("/admin/providers/" + url.PathEscape(providerName))
	if err != nil {
		return fmt.Errorf("get provider: %w", err)
	}
	providerPayload, _ := provider["provider"].(map[string]any)
	if providerPayload == nil || providerPayload["custom_marker"] != "provider-raw" {
		return fmt.Errorf("provider payload missing raw marker: %#v", provider)
	}
	if _, ok := providerPayload["api_key"]; ok {
		return fmt.Errorf("provider payload exposed api_key: %#v", providerPayload)
	}

	if _, err := adminClient.postJSON("/admin/nodes", map[string]any{
		"name":          nodeName,
		"provider":      providerName,
		"model":         "fake-upstream-model",
		"custom_marker": "node-raw",
	}); err != nil {
		return fmt.Errorf("create node: %w", err)
	}
	node, err := adminClient.getJSON("/admin/nodes/" + url.PathEscape(nodeName))
	if err != nil {
		return fmt.Errorf("get node: %w", err)
	}
	nodePayload, _ := node["node"].(map[string]any)
	if nodePayload == nil || nodePayload["custom_marker"] != "node-raw" {
		return fmt.Errorf("node payload missing raw marker: %#v", node)
	}

	if _, err := adminClient.postJSON("/admin/client-models", map[string]any{
		"name":          modelName,
		"nodes":         []any{nodeName},
		"custom_marker": "client-model-raw",
	}); err != nil {
		return fmt.Errorf("create client model: %w", err)
	}
	clientModel, err := adminClient.getJSON("/admin/client-models/" + url.PathEscape(modelName))
	if err != nil {
		return fmt.Errorf("get client model: %w", err)
	}
	clientPayload, _ := clientModel["client_model"].(map[string]any)
	if clientPayload == nil || clientPayload["custom_marker"] != "client-model-raw" {
		return fmt.Errorf("client model payload missing raw marker: %#v", clientModel)
	}

	if err := client.expectModels(modelName); err != nil {
		return err
	}
	if _, err := client.postJSON("/v1/chat/completions", map[string]any{
		"model":          modelName,
		"messages":       []any{map[string]any{"role": "user", "content": "fake smoke client model CRUD"}},
		"sentinel_param": "preserve-me-client-crud",
	}); err != nil {
		return fmt.Errorf("route client model: %w", err)
	}
	if err := adminClient.deleteStatus("/admin/providers/"+url.PathEscape(providerName), http.StatusConflict); err != nil {
		return fmt.Errorf("provider dependency conflict: %w", err)
	}
	if err := adminClient.deleteStatus("/admin/nodes/"+url.PathEscape(nodeName), http.StatusConflict); err != nil {
		return fmt.Errorf("node dependency conflict: %w", err)
	}
	if err := adminClient.deleteOK("/admin/client-models/" + url.PathEscape(modelName)); err != nil {
		return fmt.Errorf("delete client model: %w", err)
	}
	if err := client.expectModelsAbsent(modelName); err != nil {
		return err
	}
	if err := adminClient.deleteOK("/admin/nodes/" + url.PathEscape(nodeName)); err != nil {
		return fmt.Errorf("delete node: %w", err)
	}
	if err := adminClient.deleteOK("/admin/providers/" + url.PathEscape(providerName)); err != nil {
		return fmt.Errorf("delete provider: %w", err)
	}
	return nil
}

func verifyGuardedAdminSmoke(timeout time.Duration, adminPassword string, tempDir string) error {
	guardDir := filepath.Join(tempDir, "guarded-admin")
	if err := os.MkdirAll(guardDir, 0o700); err != nil {
		return err
	}

	seen := &capture{}
	upstream := httptest.NewServer(fakeUpstream(seen))
	defer upstream.Close()

	cfg, err := config.FromMap(map[string]any{
		"model_list": []any{
			map[string]any{
				"model_name": "guard-chat",
				"model_params": map[string]any{
					"api_base": upstream.URL + "/v1",
					"api_key":  "fake-upstream-key",
					"model":    "fake-upstream-model",
				},
			},
		},
		"app_keys": map[string]any{
			"enabled":     true,
			"header_name": "x-api-key",
			"required_headers": []any{
				map[string]any{"name": "X-Tenant-ID", "log_as": "tenant", "models": []any{"*"}},
			},
			"keys": []any{
				map[string]any{
					"key_id":  "smoke-key",
					"secret":  "smoke-secret",
					"enabled": true,
					"models":  []any{"guard-chat"},
					"limits": map[string]any{
						"token_quota":            1000,
						"token_quota_reset_mode": "daily",
					},
				},
			},
		},
		"proxy_settings": map[string]any{
			"profiling": map[string]any{
				"enabled": true,
			},
			"metrics": map[string]any{
				"enabled": true,
			},
			"logging": map[string]any{
				"full_request_storage": map[string]any{
					"enabled":         true,
					"path":            filepath.Join(guardDir, "requests"),
					"retention_hours": 1,
				},
			},
		},
		"database": sqliteConfig(filepath.Join(guardDir, "yaLLM.db")),
	}, "")
	if err != nil {
		return err
	}
	app, err := server.New(cfg)
	if err != nil {
		return err
	}
	quotaNow := time.Date(2026, time.June, 27, 23, 59, 30, 0, time.UTC)
	app.Quota = quota.NewManagerWithClock(func() time.Time { return quotaNow })
	defer app.LogDB.Close()
	proxy := httptest.NewServer(app)
	defer proxy.Close()

	client := smokeClient{base: proxy.URL, client: &http.Client{Timeout: timeout}}
	adminClient := client.withHeader("x-admin-password", adminPassword)
	start := time.Now().UTC().Add(-time.Second)

	regeneratedSecret, err := verifyAdminAppKeys(client, adminClient, seen)
	if err != nil {
		return err
	}
	app.Quota = quota.NewManagerWithClock(func() time.Time { return quotaNow })
	if err := verifyRequiredHeaderPolicy(client, seen, start, quota.NextResetAt(quota.ResetModeDaily, quotaNow)); err != nil {
		return err
	}
	if err := verifyAdminTemplates(client, adminClient); err != nil {
		return err
	}
	if err := verifyPprofMetricsGuards(client, adminClient, regeneratedSecret, timeout, guardDir); err != nil {
		return err
	}
	return nil
}

func verifyAdminAppKeys(client smokeClient, adminClient smokeClient, seen *capture) (string, error) {
	if _, err := client.expectStatus(http.MethodGet, "/admin/keys", nil, http.StatusForbidden, "permission_error"); err != nil {
		return "", fmt.Errorf("admin keys require password: %w", err)
	}

	listPayload, err := adminClient.getJSON("/admin/keys")
	if err != nil {
		return "", fmt.Errorf("list app keys: %w", err)
	}
	if containsJSONField(listPayload, "secret") {
		return "", fmt.Errorf("list app keys exposed secret: %#v", listPayload)
	}
	if !hasAppKey(listPayload, "smoke-key") {
		return "", fmt.Errorf("list app keys missing smoke-key: %#v", listPayload)
	}

	createdBody, err := adminClient.postJSON("/admin/keys", map[string]any{
		"key_id":      "smoke-created",
		"name":        "Created by fake smoke",
		"description": "created key should return a one-time secret",
		"models":      []any{"guard-chat"},
		"required_headers": []any{
			map[string]any{"name": "X-Workload-ID", "log_as": "workload", "models": []any{"guard-chat"}},
		},
	})
	if err != nil {
		return "", fmt.Errorf("create app key: %w", err)
	}
	created, err := decodeJSONMap("create app key", createdBody)
	if err != nil {
		return "", err
	}
	createdSecret, _ := created["secret"].(string)
	if strings.TrimSpace(createdSecret) == "" {
		return "", fmt.Errorf("create app key did not return one-time secret: %#v", created)
	}
	if keyPayload, _ := created["key"].(map[string]any); keyPayload == nil || containsJSONField(keyPayload, "secret") || keyPayload["has_secret"] != true {
		return "", fmt.Errorf("create app key returned invalid masked key metadata: %#v", created["key"])
	}

	gotCreated, err := adminClient.getJSON("/admin/keys/" + url.PathEscape("smoke-created"))
	if err != nil {
		return "", fmt.Errorf("get created app key: %w", err)
	}
	if containsJSONField(gotCreated, "secret") {
		return "", fmt.Errorf("get app key exposed secret: %#v", gotCreated)
	}

	updateBody, err := adminClient.putJSON("/admin/keys/"+url.PathEscape("smoke-created"), map[string]any{
		"name":        "Updated by fake smoke",
		"description": "updated key should preserve the existing secret",
		"models":      []any{"guard-chat"},
		"required_headers": []any{
			map[string]any{"name": "X-Workload-ID", "log_as": "workload", "models": []any{"guard-chat"}},
		},
	})
	if err != nil {
		return "", fmt.Errorf("update app key: %w", err)
	}
	updated, err := decodeJSONMap("update app key", updateBody)
	if err != nil {
		return "", err
	}
	if containsJSONField(updated, "secret") {
		return "", fmt.Errorf("update app key exposed secret: %#v", updated)
	}
	createdClient := client.withHeader("x-api-key", createdSecret).
		withHeader("X-Tenant-ID", "crud-tenant").
		withHeader("X-Workload-ID", "crud-workload")
	if _, err := createdClient.postJSON("/v1/chat/completions", guardChatPayload("created-key-secret-preserved")); err != nil {
		return "", fmt.Errorf("updated app key did not preserve existing secret: %w", err)
	}

	if err := adminClient.deleteOK("/admin/keys/" + url.PathEscape("smoke-created")); err != nil {
		return "", fmt.Errorf("delete app key: %w", err)
	}
	beforeDeletedCheck := len(seen.snapshot())
	if _, err := createdClient.expectStatus(http.MethodPost, "/v1/chat/completions", guardChatPayload("deleted-key-rejected"), http.StatusUnauthorized, "authentication_error"); err != nil {
		return "", fmt.Errorf("deleted app key still accepted: %w", err)
	}
	if got := len(seen.snapshot()); got != beforeDeletedCheck {
		return "", fmt.Errorf("deleted app key reached upstream: before=%d after=%d", beforeDeletedCheck, got)
	}

	regenCreateBody, err := adminClient.postJSON("/admin/keys", map[string]any{
		"key_id":  "smoke-regen",
		"secret":  "smoke-regenerate-old-secret",
		"models":  []any{"guard-chat"},
		"enabled": true,
	})
	if err != nil {
		return "", fmt.Errorf("create regenerate app key: %w", err)
	}
	regenCreated, err := decodeJSONMap("create regenerate app key", regenCreateBody)
	if err != nil {
		return "", err
	}
	if regenCreated["secret"] != "smoke-regenerate-old-secret" {
		return "", fmt.Errorf("create app key did not echo provided one-time secret: %#v", regenCreated)
	}
	if keyPayload, _ := regenCreated["key"].(map[string]any); keyPayload == nil || containsJSONField(keyPayload, "secret") {
		return "", fmt.Errorf("create regenerate key exposed nested secret: %#v", regenCreated["key"])
	}

	regenBody, err := adminClient.postJSON("/admin/keys/"+url.PathEscape("smoke-regen")+"/regenerate", map[string]any{})
	if err != nil {
		return "", fmt.Errorf("regenerate app key: %w", err)
	}
	regenerated, err := decodeJSONMap("regenerate app key", regenBody)
	if err != nil {
		return "", err
	}
	regeneratedSecret, _ := regenerated["secret"].(string)
	if strings.TrimSpace(regeneratedSecret) == "" || regeneratedSecret == "smoke-regenerate-old-secret" {
		return "", fmt.Errorf("regenerate app key returned invalid secret: %#v", regenerated)
	}
	if keyPayload, _ := regenerated["key"].(map[string]any); keyPayload == nil || containsJSONField(keyPayload, "secret") || keyPayload["has_secret"] != true {
		return "", fmt.Errorf("regenerate app key returned invalid masked key metadata: %#v", regenerated["key"])
	}
	oldRegenClient := client.withHeader("x-api-key", "smoke-regenerate-old-secret").withHeader("X-Tenant-ID", "regen-old-tenant")
	beforeOldRegen := len(seen.snapshot())
	if _, err := oldRegenClient.expectStatus(http.MethodPost, "/v1/chat/completions", guardChatPayload("old-regenerated-secret-rejected"), http.StatusUnauthorized, "authentication_error"); err != nil {
		return "", fmt.Errorf("old regenerated secret accepted: %w", err)
	}
	if got := len(seen.snapshot()); got != beforeOldRegen {
		return "", fmt.Errorf("old regenerated secret reached upstream: before=%d after=%d", beforeOldRegen, got)
	}
	newRegenClient := client.withHeader("x-api-key", regeneratedSecret).withHeader("X-Tenant-ID", "regen-new-tenant")
	if _, err := newRegenClient.postJSON("/v1/chat/completions", guardChatPayload("new-regenerated-secret-accepted")); err != nil {
		return "", fmt.Errorf("new regenerated secret was rejected: %w", err)
	}

	configBody, err := adminClient.postJSON("/admin/keys/config", map[string]any{
		"enabled":               true,
		"header_name":           "x-smoke-key",
		"allow_unauthenticated": false,
		"required_headers": []any{
			map[string]any{"name": "X-Tenant-ID", "log_as": "tenant", "models": []any{"*"}},
		},
		"defaults": map[string]any{
			"priority":              90,
			"queue_timeout_seconds": 12,
		},
		"groups": []any{
			map[string]any{
				"group_id": smokeGroupID,
				"name":     "Smoke group",
				"enabled":  true,
				"models":   []any{"guard-chat"},
				"limits": map[string]any{
					"rps": 25,
				},
				"required_headers": []any{
					map[string]any{"name": "X-Group-ID", "log_as": "group", "models": []any{"guard-chat"}},
				},
			},
		},
	})
	if err != nil {
		return "", fmt.Errorf("update app key config: %w", err)
	}
	configPayload, err := decodeJSONMap("update app key config", configBody)
	if err != nil {
		return "", err
	}
	if configPayload["status"] != "ok" || configPayload["enabled"] != true || configPayload["header_name"] != "x-smoke-key" {
		return "", fmt.Errorf("app key config response mismatch: %#v", configPayload)
	}
	if numeric(configPayloadMap(configPayload, "defaults")["priority"]) != 90 {
		return "", fmt.Errorf("app key config defaults not reflected: %#v", configPayload["defaults"])
	}
	if !hasAppKeyGroup(configPayload, smokeGroupID) {
		return "", fmt.Errorf("app key config groups not reflected: %#v", configPayload["groups"])
	}
	if _, err := newRegenClient.expectStatus(http.MethodPost, "/v1/chat/completions", guardChatPayload("old-header-rejected-after-config"), http.StatusUnauthorized, "authentication_error"); err != nil {
		return "", fmt.Errorf("old app-key header still accepted after config update: %w", err)
	}
	newHeaderClient := client.withHeader("x-smoke-key", regeneratedSecret).withHeader("X-Tenant-ID", "new-header-tenant")
	if _, err := newHeaderClient.postJSON("/v1/chat/completions", guardChatPayload("new-header-accepted-after-config")); err != nil {
		return "", fmt.Errorf("new app-key header not accepted after config update: %w", err)
	}

	groupCreateBody, err := adminClient.postJSON("/admin/keys", map[string]any{
		"key_id":   "smoke-grouped",
		"group_id": smokeGroupID,
		"secret":   smokeGroupKeySecret,
		"models":   []any{"guard-chat"},
		"enabled":  true,
	})
	if err != nil {
		return "", fmt.Errorf("create grouped app key: %w", err)
	}
	groupCreated, err := decodeJSONMap("create grouped app key", groupCreateBody)
	if err != nil {
		return "", err
	}
	if groupCreated["secret"] != smokeGroupKeySecret {
		return "", fmt.Errorf("create grouped app key did not return one-time secret: %#v", groupCreated)
	}
	if keyPayload, _ := groupCreated["key"].(map[string]any); keyPayload == nil || keyPayload["group_id"] != smokeGroupID || containsJSONField(keyPayload, "secret") {
		return "", fmt.Errorf("create grouped app key returned invalid masked metadata: %#v", groupCreated["key"])
	}

	return regeneratedSecret, nil
}

func verifyRequiredHeaderPolicy(client smokeClient, seen *capture, start time.Time, expectedQuotaReset time.Time) error {
	if err := expectRequiredHeaderMissingMetric(client, 0); err != nil {
		return fmt.Errorf("initial required-header metric: %w", err)
	}
	beforeAuthFailure := len(seen.snapshot())
	if _, err := client.expectStatus(http.MethodPost, "/v1/chat/completions", guardChatPayload("missing-auth-and-required-header"), http.StatusUnauthorized, "authentication_error"); err != nil {
		return fmt.Errorf("missing app key should fail auth before required-header policy: %w", err)
	}
	if got := len(seen.snapshot()); got != beforeAuthFailure {
		return fmt.Errorf("missing app key reached upstream: before=%d after=%d", beforeAuthFailure, got)
	}
	if err := expectRequiredHeaderMissingMetric(client, 0); err != nil {
		return fmt.Errorf("missing app key changed required-header metric: %w", err)
	}
	invalidKeyClient := client.withHeader("x-smoke-key", "invalid-smoke-key")
	if _, err := invalidKeyClient.expectStatus(http.MethodPost, "/v1/chat/completions", guardChatPayload("invalid-auth-and-required-header"), http.StatusUnauthorized, "authentication_error"); err != nil {
		return fmt.Errorf("invalid app key should fail auth before required-header policy: %w", err)
	}
	if got := len(seen.snapshot()); got != beforeAuthFailure {
		return fmt.Errorf("invalid app key reached upstream: before=%d after=%d", beforeAuthFailure, got)
	}
	if err := expectRequiredHeaderMissingMetric(client, 0); err != nil {
		return fmt.Errorf("invalid app key changed required-header metric: %w", err)
	}

	keyClient := client.withHeader("x-smoke-key", "smoke-secret")
	beforeMissing := len(seen.snapshot())
	if _, err := keyClient.expectStatus(http.MethodPost, "/v1/chat/completions", guardChatPayload("missing-required-header"), http.StatusBadRequest, "missing_required_header"); err != nil {
		return fmt.Errorf("missing required header rejection: %w", err)
	}
	if got := len(seen.snapshot()); got != beforeMissing {
		return fmt.Errorf("missing required header reached upstream: before=%d after=%d", beforeMissing, got)
	}
	if err := expectRequiredHeaderMissingMetric(client, 1); err != nil {
		return fmt.Errorf("valid-key missing required header metric: %w", err)
	}

	attributedClient := keyClient.withHeader("X-Tenant-ID", "acme-smoke-log")
	beforeAccepted := len(seen.snapshot())
	if _, err := attributedClient.postJSON("/v1/chat/completions", guardChatPayload("required-header-attribution")); err != nil {
		return fmt.Errorf("accepted required-header request: %w", err)
	}
	if got := len(seen.snapshot()); got != beforeAccepted+1 {
		return fmt.Errorf("accepted required-header request did not reach upstream: before=%d after=%d", beforeAccepted, got)
	}
	if err := verifyRequiredHeaderLogAttribution(client, start, "acme-smoke-log"); err != nil {
		return err
	}
	if err := verifyCalendarQuotaResetSmoke(client, "smoke-key::guard-chat", expectedQuotaReset); err != nil {
		return err
	}

	groupClient := client.withHeader("x-smoke-key", smokeGroupKeySecret).withHeader("X-Tenant-ID", "group-tenant")
	beforeGroupMissing := len(seen.snapshot())
	if _, err := groupClient.expectStatus(http.MethodPost, "/v1/chat/completions", guardChatPayload("missing-group-required-header"), http.StatusBadRequest, "missing_required_header"); err != nil {
		return fmt.Errorf("grouped key missing group required header: %w", err)
	}
	if got := len(seen.snapshot()); got != beforeGroupMissing {
		return fmt.Errorf("grouped key missing group header reached upstream: before=%d after=%d", beforeGroupMissing, got)
	}
	if err := expectRequiredHeaderMissingMetric(client, 2); err != nil {
		return fmt.Errorf("grouped key missing required header metric: %w", err)
	}
	beforeGroupAccepted := len(seen.snapshot())
	if _, err := groupClient.withHeader("X-Group-ID", "smoke-group").postJSON("/v1/chat/completions", guardChatPayload("group-required-headers-accepted")); err != nil {
		return fmt.Errorf("grouped key with global and group headers: %w", err)
	}
	if got := len(seen.snapshot()); got != beforeGroupAccepted+1 {
		return fmt.Errorf("grouped key accepted request did not reach upstream: before=%d after=%d", beforeGroupAccepted, got)
	}
	return nil
}

func expectRequiredHeaderMissingMetric(client smokeClient, want int) error {
	metricsBody, err := client.getText("/metrics")
	if err != nil {
		return fmt.Errorf("get metrics: %w", err)
	}
	wantLine := fmt.Sprintf("yallmp_required_header_missing_total %d", want)
	if !strings.Contains(string(metricsBody), wantLine) {
		return fmt.Errorf("required-header metric missing %q: %s", wantLine, string(metricsBody))
	}
	return nil
}

func verifyRequiredHeaderLogAttribution(client smokeClient, start time.Time, tenant string) error {
	logsPath := "/admin/logs?limit=50&model=" + url.QueryEscape("guard-chat") + "&start_time=" + url.QueryEscape(start.Format(time.RFC3339Nano))
	logsPayload, err := client.getJSON(logsPath)
	if err != nil {
		return fmt.Errorf("list guard logs: %w", err)
	}
	rawLogs, _ := firstList(logsPayload["logs"], logsPayload["data"])
	for _, raw := range rawLogs {
		record, _ := raw.(map[string]any)
		if record == nil {
			continue
		}
		detailID := strings.TrimSpace(fmt.Sprint(record["id"]))
		if detailID == "" {
			continue
		}
		detail, err := client.getJSON("/admin/logs/" + url.PathEscape(detailID) + "?body_max_chars=100000")
		if err != nil {
			return fmt.Errorf("get guard log detail: %w", err)
		}
		body, _ := detail["body"].(map[string]any)
		if body == nil || body["model"] != "guard-chat" || !bodyContainsMessage(body, "required-header-attribution") {
			continue
		}
		if detail["full_request_status"] != "available" {
			return fmt.Errorf("guard log full request not available: %#v", detail)
		}
		requiredHeaders, _ := detail["required_headers"].(map[string]any)
		if requiredHeaders["tenant"] == tenant {
			return nil
		}
		return fmt.Errorf("guard log missing required-header attribution: %#v", detail["required_headers"])
	}
	return fmt.Errorf("no guard log detail found for required-header attribution: %#v", rawLogs)
}

func verifyAdminTemplates(client smokeClient, adminClient smokeClient) error {
	listPayload, err := client.getJSON("/admin/templates")
	if err != nil {
		return fmt.Errorf("list templates: %w", err)
	}
	if !hasTemplate(listPayload, "qwen.jinja") && !hasTemplate(listPayload, "template_example.jinja") {
		return fmt.Errorf("template list missing expected repo templates: %#v", listPayload)
	}

	uploadName := fmt.Sprintf("smoke-upload-%d.jinja", time.Now().UnixNano())
	uploadedPath := filepath.Join(config.ProjectRoot(), "configs", "jinja_templates", uploadName)
	defer os.Remove(uploadedPath)
	uploadBody, err := adminClient.postJSON("/admin/templates", map[string]any{
		"name":    uploadName,
		"content": "{% for message in messages %}{% if message.role == 'assistant' %}<think>{{ message.reasoning_content | default('') }}</think>{% endif %}{% endfor %}",
	})
	if err != nil {
		return fmt.Errorf("upload template: %w", err)
	}
	uploadPayload, err := decodeJSONMap("upload template", uploadBody)
	if err != nil {
		return err
	}
	if uploadPayload["name"] != uploadName || uploadPayload["path"] != "configs/jinja_templates/"+uploadName {
		return fmt.Errorf("upload template response mismatch: %#v", uploadPayload)
	}
	if _, err := os.Stat(uploadedPath); err != nil {
		return fmt.Errorf("uploaded template was not written at expected path: %w", err)
	}
	previewPayload, err := client.getJSON("/admin/templates/inspect?name=" + url.QueryEscape(uploadName) + "&render_preview=1")
	if err != nil {
		return fmt.Errorf("inspect uploaded template preview: %w", err)
	}
	preview, _ := previewPayload["render_preview"].(map[string]any)
	if preview["engine"] != "bounded_jinja_runtime" || !strings.Contains(fmt.Sprint(preview["rendered"]), "<think>Need a lookup.</think>") {
		return fmt.Errorf("uploaded template runtime preview mismatch: %#v", previewPayload)
	}

	inspectPayload, err := client.getJSON("/admin/templates/inspect?template_path=" + url.QueryEscape("configs/jinja_templates/template_example.jinja"))
	if err != nil {
		return fmt.Errorf("inspect template: %w", err)
	}
	if inspectPayload["detected"] != true || strings.TrimSpace(fmt.Sprint(inspectPayload["detected_think_tag"])) == "" {
		return fmt.Errorf("inspect template did not detect parse config: %#v", inspectPayload)
	}
	if hasTemplate(listPayload, "qwen.jinja") {
		qwenInspectPayload, err := client.getJSON("/admin/templates/inspect?template_path=" + url.QueryEscape("configs/jinja_templates/qwen.jinja"))
		if err != nil {
			return fmt.Errorf("inspect qwen template: %w", err)
		}
		qwenConfig, _ := qwenInspectPayload["config"].(map[string]any)
		if qwenInspectPayload["detected"] != true || qwenConfig["tool_parser"] != "qwen_xml" {
			return fmt.Errorf("inspect qwen template did not detect qwen_xml parser: %#v", qwenInspectPayload)
		}
	}
	if _, err := adminClient.expectStatus(http.MethodPost, "/admin/templates", map[string]any{"name": "../smoke-evil.jinja", "content": "x"}, http.StatusBadRequest, "invalid_template_path"); err != nil {
		return fmt.Errorf("template upload traversal rejection: %w", err)
	}
	if _, err := client.expectStatus(http.MethodGet, "/admin/templates/inspect?template_path=../../etc/passwd", nil, http.StatusBadRequest, "invalid_template_path"); err != nil {
		return fmt.Errorf("template inspect traversal rejection: %w", err)
	}
	if err := os.Remove(uploadedPath); err != nil {
		return fmt.Errorf("remove uploaded smoke template: %w", err)
	}
	if _, err := os.Stat(uploadedPath); err == nil {
		return fmt.Errorf("uploaded smoke template still exists after cleanup: %s", uploadedPath)
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("check uploaded smoke template cleanup: %w", err)
	}
	return nil
}

func verifyPprofDisabledByDefault(adminClient smokeClient) error {
	if _, err := adminClient.expectStatus(http.MethodGet, "/debug/pprof/", nil, http.StatusNotFound, "not_found"); err != nil {
		return fmt.Errorf("pprof disabled-by-default guard: %w", err)
	}
	return nil
}

func verifyPprofMetricsGuards(client smokeClient, adminClient smokeClient, regeneratedSecret string, timeout time.Duration, tempDir string) error {
	if _, err := client.expectStatus(http.MethodGet, "/debug/pprof/", nil, http.StatusForbidden, "permission_error"); err != nil {
		return fmt.Errorf("pprof requires admin password: %w", err)
	}
	pprofBody, err := adminClient.expectStatus(http.MethodGet, "/debug/pprof/", nil, http.StatusOK, "goroutine")
	if err != nil {
		return fmt.Errorf("pprof admin access: %w", err)
	}
	if !strings.Contains(string(pprofBody), "heap") {
		return fmt.Errorf("pprof index missing expected profiles: %s", string(pprofBody))
	}
	if _, err := adminClient.expectStatus(http.MethodPost, "/debug/pprof/profile", nil, http.StatusMethodNotAllowed, "method_not_allowed"); err != nil {
		return fmt.Errorf("pprof method guard: %w", err)
	}
	if _, err := adminClient.expectStatus(http.MethodGet, "/debug/pprof/not-a-profile", nil, http.StatusNotFound, "not_found"); err != nil {
		return fmt.Errorf("pprof unknown profile guard: %w", err)
	}
	if _, err := client.withHeader("x-smoke-key", regeneratedSecret).withHeader("X-Tenant-ID", "metrics-tenant").getText("/metrics"); err != nil {
		return fmt.Errorf("metrics endpoint should stay available without admin password when enabled: %w", err)
	}

	noMetricsDir := filepath.Join(tempDir, "metrics-disabled")
	if err := os.MkdirAll(noMetricsDir, 0o700); err != nil {
		return err
	}
	cfg, err := config.FromMap(map[string]any{
		"model_list": []any{
			map[string]any{
				"model_name": "metrics-disabled-chat",
				"model_params": map[string]any{
					"api_base": "http://127.0.0.1:1/v1",
					"api_key":  "unused",
					"model":    "unused",
				},
			},
		},
		"database": sqliteConfig(filepath.Join(noMetricsDir, "yaLLM.db")),
	}, "")
	if err != nil {
		return err
	}
	app, err := server.New(cfg)
	if err != nil {
		return err
	}
	defer app.LogDB.Close()
	proxy := httptest.NewServer(app)
	defer proxy.Close()
	noMetricsClient := smokeClient{base: proxy.URL, client: &http.Client{Timeout: timeout}}
	if _, err := noMetricsClient.expectStatus(http.MethodGet, "/metrics", nil, http.StatusNotFound, "not_found"); err != nil {
		return fmt.Errorf("metrics disabled-by-default guard: %w", err)
	}
	return nil
}

func (c smokeClient) expectModels(model string) error {
	req, err := http.NewRequest(http.MethodGet, c.base+"/v1/models", nil)
	if err != nil {
		return err
	}
	c.applyHeaders(req)
	resp, err := c.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("/v1/models status=%d body=%s", resp.StatusCode, string(body))
	}
	if !strings.Contains(string(body), model) {
		return fmt.Errorf("/v1/models did not list %q: %s", model, string(body))
	}
	return nil
}

func (c smokeClient) expectModelsAbsent(model string) error {
	req, err := http.NewRequest(http.MethodGet, c.base+"/v1/models", nil)
	if err != nil {
		return err
	}
	c.applyHeaders(req)
	resp, err := c.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("/v1/models status=%d body=%s", resp.StatusCode, string(body))
	}
	if strings.Contains(string(body), model) {
		return fmt.Errorf("/v1/models still listed %q: %s", model, string(body))
	}
	return nil
}

func (c smokeClient) postJSON(path string, payload map[string]any) ([]byte, error) {
	var requestBody io.Reader
	if payload != nil {
		raw, _ := json.Marshal(payload)
		requestBody = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(http.MethodPost, c.base+path, requestBody)
	if err != nil {
		return nil, err
	}
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	c.applyHeaders(req)
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("%s status=%d body=%s", path, resp.StatusCode, string(body))
	}
	return body, nil
}

func (c smokeClient) putJSON(path string, payload map[string]any) ([]byte, error) {
	raw, _ := json.Marshal(payload)
	status, body, err := c.doRequest(http.MethodPut, path, bytes.NewReader(raw), "application/json")
	if err != nil {
		return nil, err
	}
	if status < 200 || status >= 300 {
		return nil, fmt.Errorf("%s status=%d body=%s", path, status, string(body))
	}
	return body, nil
}

func (c smokeClient) expectStatus(method string, path string, payload map[string]any, wantStatus int, wantBodySubstring string) ([]byte, error) {
	var requestBody io.Reader
	contentType := ""
	if payload != nil {
		raw, _ := json.Marshal(payload)
		requestBody = bytes.NewReader(raw)
		contentType = "application/json"
	}
	status, body, err := c.doRequest(method, path, requestBody, contentType)
	if err != nil {
		return nil, err
	}
	if status != wantStatus {
		return nil, fmt.Errorf("%s %s status=%d want=%d body=%s", method, path, status, wantStatus, string(body))
	}
	if wantBodySubstring != "" && !strings.Contains(string(body), wantBodySubstring) {
		return nil, fmt.Errorf("%s %s body missing %q: %s", method, path, wantBodySubstring, string(body))
	}
	return body, nil
}

func (c smokeClient) doRequest(method string, path string, body io.Reader, contentType string) (int, []byte, error) {
	req, err := http.NewRequest(method, c.base+path, body)
	if err != nil {
		return 0, nil, err
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	c.applyHeaders(req)
	resp, err := c.client.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	responseBody, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, responseBody, nil
}

func (c smokeClient) expectPostJSONFailure(path string, payload map[string]any) error {
	raw, _ := json.Marshal(payload)
	req, err := http.NewRequest(http.MethodPost, c.base+path, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	c.applyHeaders(req)
	resp, err := c.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return fmt.Errorf("%s unexpectedly succeeded after delete: status=%d body=%s", path, resp.StatusCode, string(body))
	}
	return nil
}

func (c smokeClient) deleteOK(path string) error {
	req, err := http.NewRequest(http.MethodDelete, c.base+path, nil)
	if err != nil {
		return err
	}
	c.applyHeaders(req)
	resp, err := c.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("%s status=%d body=%s", path, resp.StatusCode, string(body))
	}
	return nil
}

func (c smokeClient) deleteStatus(path string, wantStatus int) error {
	req, err := http.NewRequest(http.MethodDelete, c.base+path, nil)
	if err != nil {
		return err
	}
	c.applyHeaders(req)
	resp, err := c.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != wantStatus {
		return fmt.Errorf("%s status=%d want=%d body=%s", path, resp.StatusCode, wantStatus, string(body))
	}
	return nil
}

func (c smokeClient) getJSON(path string) (map[string]any, error) {
	req, err := http.NewRequest(http.MethodGet, c.base+path, nil)
	if err != nil {
		return nil, err
	}
	c.applyHeaders(req)
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("%s status=%d body=%s", path, resp.StatusCode, string(body))
	}
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("decode %s: %w body=%s", path, err, string(body))
	}
	return payload, nil
}

func (c smokeClient) getText(path string) ([]byte, error) {
	req, err := http.NewRequest(http.MethodGet, c.base+path, nil)
	if err != nil {
		return nil, err
	}
	c.applyHeaders(req)
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("%s status=%d body=%s", path, resp.StatusCode, string(body))
	}
	return body, nil
}

func (c smokeClient) applyHeaders(req *http.Request) {
	for key, values := range c.headers {
		for _, value := range values {
			req.Header.Add(key, value)
		}
	}
}

func (c smokeClient) withHeader(key string, value string) smokeClient {
	out := c
	out.headers = http.Header{}
	for headerKey, values := range c.headers {
		out.headers[headerKey] = append([]string(nil), values...)
	}
	out.headers.Set(key, value)
	return out
}

func smokeClientForConfig(base string, client *http.Client, cfg *config.Config) (smokeClient, error) {
	out := smokeClient{base: base, client: client}
	if cfg == nil || !cfg.AppKeys.Enabled {
		return out, nil
	}
	if cfg.AppKeys.AllowUnauthenticated {
		return out, nil
	}
	header := cfg.AppKeys.HeaderName
	if strings.TrimSpace(header) == "" {
		header = config.DefaultAppKeyHeader
	}
	for _, key := range cfg.AppKeys.Keys {
		if strings.TrimSpace(key.Secret) == "" {
			continue
		}
		if key.Enabled != nil && !*key.Enabled {
			continue
		}
		out.headers = http.Header{header: []string{key.Secret}}
		return out, nil
	}
	return out, errors.New("real config has app_keys enabled but no enabled key with a secret")
}

func verifyMetrics(client smokeClient) error {
	body, err := client.getText("/metrics")
	if err != nil {
		return err
	}
	text := string(body)
	for _, want := range []string{
		`yallmp_backend_success_total{backend="smoke-fallback-secondary"} 1`,
		`yallmp_backend_failure_total{backend="smoke-fallback-primary"} 1`,
		`yallmp_backend_consecutive_failures{backend="smoke-fallback-primary"} 1`,
	} {
		if !strings.Contains(text, want) {
			return fmt.Errorf("/metrics missing %q: %s", want, text)
		}
	}
	return nil
}

func verifyPassthroughSmoke(client smokeClient, seen *passthroughCapture) error {
	req, err := http.NewRequest(http.MethodPost, client.base+"/p1/smoke-passthrough/v1/echo?k=v", strings.NewReader(`{"value":"passthrough-fidelity"}`))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer client-passthrough-secret")
	req.Header.Set("X-Smoke-Marker", "m")
	client.applyHeaders(req)
	resp, err := client.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("passthrough status=%d body=%s", resp.StatusCode, string(body))
	}
	if strings.TrimSpace(string(body)) != `{"passthrough":"ok"}` {
		return fmt.Errorf("passthrough body mismatch: %s", string(body))
	}
	if resp.Header.Get("X-Passthrough-Marker") != "preserved" {
		return fmt.Errorf("passthrough response header was not preserved: %#v", resp.Header)
	}
	requests := seen.snapshot()
	if len(requests) != 1 {
		return fmt.Errorf("passthrough upstream requests = %d, want 1: %#v", len(requests), requests)
	}
	got := requests[0]
	if got.Path != "/echo" || got.Query != "k=v" || got.Body != `{"value":"passthrough-fidelity"}` || got.Marker != "m" {
		return fmt.Errorf("passthrough path/query/body/header mismatch: path=%q query=%q body=%q marker=%q", got.Path, got.Query, got.Body, got.Marker)
	}
	if got.Auth != "Bearer fake-passthrough-key" {
		return fmt.Errorf("passthrough upstream auth mismatch")
	}
	return nil
}

func verifyCalendarQuotaResetSmoke(client smokeClient, scope string, expectedReset time.Time) error {
	payload, err := client.getJSON("/v1/usage")
	if err != nil {
		return fmt.Errorf("usage quota reset smoke: %w", err)
	}
	quotaPayload := mapValue(payload["quota"])
	usedByScope := mapValue(quotaPayload["used_by_scope"])
	if numeric(usedByScope[scope]) <= 0 {
		return fmt.Errorf("usage quota scope %q was not recorded: %#v", scope, quotaPayload)
	}
	resetByScope := mapValue(quotaPayload["reset_at_by_scope"])
	rawReset, _ := resetByScope[scope].(string)
	resetAt, err := time.Parse(time.RFC3339Nano, rawReset)
	if err != nil {
		return fmt.Errorf("usage reset_at_by_scope[%s] parse %q: %w body=%#v", scope, rawReset, err, payload)
	}
	if resetAt.Location() != time.UTC {
		return fmt.Errorf("usage reset_at_by_scope[%s] is not UTC: %s", scope, resetAt)
	}
	if !expectedReset.IsZero() {
		expectedReset = expectedReset.UTC()
		if !resetAt.Equal(expectedReset) {
			return fmt.Errorf("usage reset_at_by_scope[%s] = %s, want controlled daily UTC reset %s body=%#v", scope, resetAt, expectedReset, payload)
		}
		return nil
	}
	if !resetAt.After(time.Now().UTC().Add(-time.Second)) || resetAt.After(time.Now().UTC().Add(25*time.Hour)) {
		return fmt.Errorf("usage reset_at_by_scope[%s] not plausible daily future reset: %s body=%#v", scope, resetAt, payload)
	}
	return nil
}

func verifyOTelSmoke(recorder *observability.Recorder, client smokeClient, start time.Time) error {
	if recorder == nil {
		return errors.New("otel recorder is nil")
	}
	spans := recorder.Spans()
	if len(spans) == 0 {
		return errors.New("otel recorder captured no spans")
	}
	require := []struct {
		name  string
		attrs map[string]string
	}{
		{name: "proxy.request", attrs: map[string]string{"phase": "request", "endpoint_kind": "chat", "model": "smoke-chat", "status_code": "200", "gen_ai.system": "openai", "gen_ai.operation.name": "chat", "gen_ai.request.model": "smoke-chat"}},
		{name: "proxy.response", attrs: map[string]string{"phase": "response", "endpoint_kind": "chat", "model": "smoke-chat", "status_code": "200", "gen_ai.system": "openai", "gen_ai.operation.name": "chat", "gen_ai.request.model": "smoke-chat", "gen_ai.response.model": "smoke-chat"}},
		{name: "proxy.request", attrs: map[string]string{"phase": "request", "endpoint_kind": "messages", "model": "smoke-chat", "stream": "true", "status_code": "200", "gen_ai.system": "openai", "gen_ai.operation.name": "messages", "gen_ai.request.model": "smoke-chat"}},
		{name: "proxy.response", attrs: map[string]string{"phase": "response", "endpoint_kind": "responses", "model": "smoke-chat", "stream": "true", "status_code": "200", "gen_ai.system": "openai", "gen_ai.operation.name": "responses", "gen_ai.request.model": "smoke-chat", "gen_ai.response.model": "smoke-chat"}},
		{name: "proxy.upstream_attempt", attrs: map[string]string{"phase": "upstream_attempt", "endpoint_kind": "passthrough", "model": "smoke-passthrough", "backend_name": "smoke-passthrough", "status_code": "200", "gen_ai.system": "openai", "gen_ai.operation.name": "passthrough", "gen_ai.request.model": "smoke-passthrough"}},
		{name: "proxy.parser", attrs: map[string]string{"phase": "parser", "endpoint_kind": "chat", "model": "smoke-parsed-stream-sidecar", "stream": "true", "status_code": "200", "gen_ai.system": "openai", "gen_ai.operation.name": "chat", "gen_ai.request.model": "smoke-parsed-stream-sidecar"}},
		{name: "proxy.logging", attrs: map[string]string{"phase": "logging", "endpoint_kind": "chat", "model": "smoke-chat", "status_code": "200", "gen_ai.system": "openai", "gen_ai.operation.name": "chat", "gen_ai.request.model": "smoke-chat"}},
	}
	for _, want := range require {
		if !hasSpan(spans, want.name, want.attrs) {
			return fmt.Errorf("otel smoke missing span %s attrs=%#v captured=%s", want.name, want.attrs, spanSummary(spans))
		}
	}
	for _, secret := range []string{"fake-upstream-key", "fake-passthrough-key", "fake-fallback-primary-key", "fake-fallback-secondary-key", "client-passthrough-secret", "passthrough-fidelity", "Authorization"} {
		if spanAttrsContain(spans, secret) {
			return fmt.Errorf("otel smoke leaked %q in span attributes", secret)
		}
	}
	detail, err := findChatLogDetail(client, "smoke-chat", start)
	if err != nil {
		return fmt.Errorf("otel smoke log detail: %w", err)
	}
	traceID := stringField(detail, "trace_id")
	spanID := stringField(detail, "span_id")
	if traceID == "" || spanID == "" {
		return fmt.Errorf("otel smoke log detail missing trace context: %#v", detail)
	}
	if !hasRequestSpanContext(spans, traceID, spanID) {
		return fmt.Errorf("otel smoke log trace context %s/%s did not match a proxy.request span; captured=%s", traceID, spanID, spanSummary(spans))
	}
	metrics, err := recorder.Metrics(context.Background())
	if err != nil {
		return fmt.Errorf("otel smoke metric collection: %w", err)
	}
	if got := metricSumValue(metrics, "yallmp.proxy.requests", map[string]string{"endpoint_kind": "chat", "model": "smoke-chat", "outcome": "success"}); got == 0 {
		return fmt.Errorf("otel smoke missing chat request metric")
	}
	if got := metricSumValue(metrics, "gen_ai.client.token.usage", map[string]string{"endpoint_kind": "messages", "model": "smoke-chat", "stream": "true", "token.type": "total"}); got == 0 {
		return fmt.Errorf("otel smoke missing streamed messages token metric")
	}
	if got := metricHistogramCount(metrics, "gen_ai.client.operation.time_to_first_token", map[string]string{"endpoint_kind": "messages", "model": "smoke-chat", "stream": "true"}); got == 0 {
		return fmt.Errorf("otel smoke missing streamed messages TTFT metric")
	}
	if got := metricHistogramCount(metrics, "gen_ai.client.operation.time_per_output_token", map[string]string{"endpoint_kind": "messages", "model": "smoke-chat", "stream": "true"}); got == 0 {
		return fmt.Errorf("otel smoke missing streamed messages TPOT metric")
	}
	if metricAttrsContain(metrics, "fake-upstream-key") || metricAttrsContain(metrics, "Authorization") || metricAttrsContain(metrics, "client-passthrough-secret") {
		return fmt.Errorf("otel smoke leaked a secret in metric attributes")
	}
	return nil
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

func metricAttrsContain(metrics metricdata.ResourceMetrics, needle string) bool {
	for _, scope := range metrics.ScopeMetrics {
		for _, metricItem := range scope.Metrics {
			switch data := metricItem.Data.(type) {
			case metricdata.Sum[int64]:
				for _, point := range data.DataPoints {
					if strings.Contains(point.Attributes.Encoded(attribute.DefaultEncoder()), needle) {
						return true
					}
				}
			case metricdata.Histogram[float64]:
				for _, point := range data.DataPoints {
					if strings.Contains(point.Attributes.Encoded(attribute.DefaultEncoder()), needle) {
						return true
					}
				}
			}
		}
	}
	return false
}

func stringField(values map[string]any, key string) string {
	if values == nil {
		return ""
	}
	value, _ := values[key].(string)
	return strings.TrimSpace(value)
}

func hasRequestSpanContext(spans []sdktrace.ReadOnlySpan, traceID string, spanID string) bool {
	for _, span := range spans {
		if span.Name() != "proxy.request" {
			continue
		}
		if span.SpanContext().TraceID().String() == traceID && span.SpanContext().SpanID().String() == spanID {
			return true
		}
	}
	return false
}

func hasSpan(spans []sdktrace.ReadOnlySpan, name string, attrs map[string]string) bool {
	for _, span := range spans {
		if span.Name() != name {
			continue
		}
		got := spanAttrs(span)
		matched := true
		for key, want := range attrs {
			if got[key] != want {
				matched = false
				break
			}
		}
		if matched {
			return true
		}
	}
	return false
}

func spanAttrs(span sdktrace.ReadOnlySpan) map[string]string {
	out := map[string]string{}
	for _, attr := range span.Attributes() {
		out[string(attr.Key)] = attr.Value.Emit()
	}
	return out
}

func spanAttrsContain(spans []sdktrace.ReadOnlySpan, needle string) bool {
	for _, span := range spans {
		for _, attr := range span.Attributes() {
			if strings.Contains(string(attr.Key), needle) || strings.Contains(attr.Value.Emit(), needle) {
				return true
			}
		}
		if strings.Contains(span.Status().Description, needle) {
			return true
		}
	}
	return false
}

func spanSummary(spans []sdktrace.ReadOnlySpan) string {
	counts := map[string]int{}
	for _, span := range spans {
		counts[span.Name()]++
	}
	raw, _ := json.Marshal(counts)
	return string(raw)
}

func verifyParsedResponseSidecar(client smokeClient, start time.Time) error {
	detail, err := findChatLogDetail(client, "smoke-parsed-sidecar", start)
	if err != nil {
		return err
	}
	parsed, _ := detail["parsed_response"].(map[string]any)
	if parsed == nil {
		return fmt.Errorf("parsed response detail missing: %#v", detail)
	}
	rawText, _ := parsed["raw_upstream_response_text"].(string)
	transformedText, _ := parsed["transformed_client_response_text"].(string)
	if !strings.Contains(rawText, "<think>smoke hidden</think>visible smoke") {
		return fmt.Errorf("parsed sidecar raw response mismatch: %#v", parsed)
	}
	if strings.Contains(transformedText, "<think>") || !strings.Contains(transformedText, "reasoning_content") || !strings.Contains(transformedText, "visible smoke") {
		return fmt.Errorf("parsed sidecar transformed response mismatch: %#v", parsed)
	}
	return nil
}

func verifyParsedStreamSidecar(client smokeClient, start time.Time) error {
	detail, err := findChatLogDetail(client, "smoke-parsed-stream-sidecar", start)
	if err != nil {
		return err
	}
	parsed, _ := detail["parsed_stream"].(map[string]any)
	if parsed == nil {
		return fmt.Errorf("parsed stream detail missing: %#v", detail)
	}
	rawText, _ := parsed["raw_upstream_response_text"].(string)
	transformedText, _ := parsed["transformed_client_response_text"].(string)
	if !strings.Contains(rawText, "<think>smoke stream hidden</think>visible stream smoke") {
		return fmt.Errorf("parsed stream raw response mismatch: %#v", parsed)
	}
	if strings.Contains(transformedText, "<think>") || !strings.Contains(transformedText, "reasoning_content") || !strings.Contains(transformedText, "visible stream smoke") {
		return fmt.Errorf("parsed stream transformed response mismatch: %#v", parsed)
	}
	if truncated, _ := parsed["truncated"].(bool); truncated {
		return fmt.Errorf("parsed stream smoke was unexpectedly truncated: %#v", parsed)
	}
	if strings.TrimSpace(fmt.Sprint(parsed["raw_upstream_response_path"])) == "" || strings.TrimSpace(fmt.Sprint(parsed["transformed_client_response_path"])) == "" {
		return fmt.Errorf("parsed stream paths missing: %#v", parsed)
	}
	return nil
}

func verifyParsedResponseSidecarDisabled(timeout time.Duration, tempDir string) error {
	seen := &capture{}
	upstream := httptest.NewServer(fakeUpstream(seen))
	defer upstream.Close()
	cfg, err := config.FromMap(map[string]any{
		"model_list": []any{
			map[string]any{
				"model_name": "smoke-parsed-disabled",
				"model_params": map[string]any{
					"api_base": upstream.URL + "/v1",
					"api_key":  "fake-upstream-key",
					"model":    "fake-upstream-parsed-sidecar-model",
				},
				"modules": map[string]any{
					"enabled":  true,
					"response": []any{"parse_tags"},
				},
			},
		},
		"proxy_settings": map[string]any{
			"logging": map[string]any{
				"log_to_disk":         true,
				"log_parsed_response": false,
				"full_request_storage": map[string]any{
					"enabled":         true,
					"path":            filepath.Join(tempDir, "parsed-disabled-requests"),
					"retention_hours": 1,
				},
			},
		},
	}, "")
	if err != nil {
		return err
	}
	app, err := server.New(cfg)
	if err != nil {
		return err
	}
	defer app.LogDB.Close()
	proxyServer := httptest.NewServer(app)
	defer proxyServer.Close()
	client := smokeClient{base: proxyServer.URL, client: &http.Client{Timeout: timeout}}
	start := time.Now().UTC().Add(-time.Second)
	body, err := client.postJSON("/v1/chat/completions", map[string]any{
		"model":    "smoke-parsed-disabled",
		"messages": []any{map[string]any{"role": "user", "content": "fake smoke parsed disabled"}},
	})
	if err != nil {
		return err
	}
	if strings.Contains(string(body), "<think>") || !strings.Contains(string(body), "reasoning_content") {
		return fmt.Errorf("disabled parsed sidecar response was not transformed: %s", string(body))
	}
	detail, err := findChatLogDetail(client, "smoke-parsed-disabled", start)
	if err != nil {
		return err
	}
	if _, ok := detail["parsed_response"]; ok {
		return fmt.Errorf("parsed_response was written while disabled: %#v", detail["parsed_response"])
	}
	if traceID := stringField(detail, "trace_id"); traceID != "" {
		return fmt.Errorf("trace_id was written while OTel disabled: %#v", detail)
	}
	if spanID := stringField(detail, "span_id"); spanID != "" {
		return fmt.Errorf("span_id was written while OTel disabled: %#v", detail)
	}
	return nil
}

func findChatLogDetail(client smokeClient, model string, start time.Time) (map[string]any, error) {
	logsPath := "/admin/logs?limit=20&model=" + url.QueryEscape(model) + "&start_time=" + url.QueryEscape(start.Format(time.RFC3339Nano))
	logsPayload, err := client.getJSON(logsPath)
	if err != nil {
		return nil, err
	}
	rawLogs, _ := firstList(logsPayload["logs"], logsPayload["data"])
	for _, raw := range rawLogs {
		record, _ := raw.(map[string]any)
		if record == nil {
			continue
		}
		if path, _ := record["path"].(string); path != "/v1/chat/completions" {
			continue
		}
		detailID := strings.TrimSpace(fmt.Sprint(record["id"]))
		if detailID == "" {
			continue
		}
		return client.getJSON("/admin/logs/" + url.PathEscape(detailID) + "?body_max_chars=100000")
	}
	return nil, fmt.Errorf("no chat log detail found for model %q: %#v", model, rawLogs)
}

func verifyLogs(client smokeClient, model string, start time.Time, requireUsage bool) error {
	logsPath := "/admin/logs?limit=20&model=" + url.QueryEscape(model) + "&start_time=" + url.QueryEscape(start.Format(time.RFC3339Nano))
	logsPayload, err := client.getJSON(logsPath)
	if err != nil {
		return err
	}
	rawLogs, _ := firstList(logsPayload["logs"], logsPayload["data"])
	if len(rawLogs) == 0 {
		return fmt.Errorf("no admin logs found for model %q: %#v", model, logsPayload)
	}
	var detailID string
	usageSeen := false
	for _, raw := range rawLogs {
		record, _ := raw.(map[string]any)
		if record == nil {
			continue
		}
		if record["backend_name"] == "" {
			return fmt.Errorf("log record missing backend_name: %#v", record)
		}
		if path, _ := record["path"].(string); path == "/v1/chat/completions" && record["is_stream"] != true && detailID == "" {
			detailID, _ = record["id"].(string)
		}
		if usage, _ := record["usage_stats"].(map[string]any); numeric(usage["total_tokens"]) > 0 {
			usageSeen = true
		}
	}
	if requireUsage && !usageSeen {
		return fmt.Errorf("logs did not include token usage: %#v", rawLogs)
	}
	if detailID == "" {
		return fmt.Errorf("no chat completion log found in %#v", rawLogs)
	}
	detail, err := client.getJSON("/admin/logs/" + detailID + "?body_max_chars=100000")
	if err != nil {
		return err
	}
	if detail["full_request_status"] != "available" {
		return fmt.Errorf("full request payload is not available: %#v", detail)
	}
	body, _ := detail["body"].(map[string]any)
	if body == nil || body["model"] != model {
		return fmt.Errorf("logged request body missing model %q: %#v", model, detail["body"])
	}
	if _, ok := body["temperature"]; !ok {
		return fmt.Errorf("logged request body missing temperature: %#v", body)
	}
	if requireUsage {
		responseBody, _ := detail["response_body"].(map[string]any)
		usage, _ := responseBody["usage"].(map[string]any)
		if numeric(usage["total_tokens"]) <= 0 {
			return fmt.Errorf("log detail response body missing usage: %#v", detail["response_body"])
		}
	}
	return nil
}

func verifyFallbackLog(client smokeClient, model string, start time.Time) error {
	logsPath := "/admin/logs?limit=20&model=" + url.QueryEscape(model) + "&start_time=" + url.QueryEscape(start.Format(time.RFC3339Nano))
	logsPayload, err := client.getJSON(logsPath)
	if err != nil {
		return err
	}
	rawLogs, _ := firstList(logsPayload["logs"], logsPayload["data"])
	if len(rawLogs) == 0 {
		return fmt.Errorf("no fallback admin logs found for model %q: %#v", model, logsPayload)
	}
	record, _ := rawLogs[0].(map[string]any)
	if record == nil {
		return fmt.Errorf("fallback log record has unexpected shape: %#v", rawLogs[0])
	}
	detailID, _ := record["id"].(string)
	if detailID == "" {
		return fmt.Errorf("fallback log missing id: %#v", record)
	}
	detail, err := client.getJSON("/admin/logs/" + detailID + "?body_max_chars=100000")
	if err != nil {
		return err
	}
	attempts, _ := firstList(detail["attempt_trail"], detail["attempts"])
	if len(attempts) != 2 {
		return fmt.Errorf("fallback attempt trail length = %d, want 2: %#v", len(attempts), detail)
	}
	first, _ := attempts[0].(map[string]any)
	second, _ := attempts[1].(map[string]any)
	if first == nil || second == nil {
		return fmt.Errorf("fallback attempts have unexpected shape: %#v", attempts)
	}
	if first["backend_name"] != "smoke-fallback-primary" || numeric(first["status"]) != http.StatusServiceUnavailable || first["outcome"] != "retry" {
		return fmt.Errorf("fallback primary attempt mismatch: %#v", first)
	}
	if second["backend_name"] != "smoke-fallback-secondary" || numeric(second["status"]) != http.StatusOK || second["outcome"] != "final" {
		return fmt.Errorf("fallback secondary attempt mismatch: %#v", second)
	}
	return nil
}

func sqliteConfig(path string) map[string]any {
	return map[string]any{
		"backend": "sqlite",
		"connection": map[string]any{
			"sqlite": map[string]any{"path": path},
		},
	}
}

func configHasModel(cfg *config.Config, model string) bool {
	if cfg == nil {
		return false
	}
	if _, ok := cfg.FindLegacyModel(model); ok {
		return true
	}
	for _, item := range cfg.Models {
		if item.EffectiveName() == model {
			return true
		}
	}
	return false
}

func firstList(values ...any) ([]any, bool) {
	for _, value := range values {
		if items, ok := value.([]any); ok {
			return items, true
		}
	}
	return nil, false
}

func decodeJSONMap(label string, body []byte) (map[string]any, error) {
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("decode %s: %w body=%s", label, err, string(body))
	}
	return payload, nil
}

func containsJSONField(value any, field string) bool {
	switch typed := value.(type) {
	case map[string]any:
		for key, child := range typed {
			if key == field {
				return true
			}
			if containsJSONField(child, field) {
				return true
			}
		}
	case []any:
		for _, child := range typed {
			if containsJSONField(child, field) {
				return true
			}
		}
	}
	return false
}

func hasAppKey(payload map[string]any, keyID string) bool {
	keys, _ := firstList(payload["keys"])
	for _, raw := range keys {
		key, _ := raw.(map[string]any)
		if key != nil && key["key_id"] == keyID {
			return true
		}
	}
	return false
}

func hasAppKeyGroup(payload map[string]any, groupID string) bool {
	groups, _ := firstList(payload["groups"])
	for _, raw := range groups {
		group, _ := raw.(map[string]any)
		if group != nil && group["group_id"] == groupID {
			return true
		}
	}
	return false
}

func hasTemplate(payload map[string]any, name string) bool {
	templates, _ := firstList(payload["templates"])
	for _, raw := range templates {
		template, _ := raw.(map[string]any)
		if template != nil && template["name"] == name {
			return true
		}
	}
	return false
}

func configPayloadMap(payload map[string]any, key string) map[string]any {
	child, _ := payload[key].(map[string]any)
	if child == nil {
		return map[string]any{}
	}
	return child
}

func guardChatPayload(message string) map[string]any {
	return map[string]any{
		"model":    "guard-chat",
		"messages": []any{map[string]any{"role": "user", "content": message}},
	}
}

func bodyContainsMessage(body map[string]any, content string) bool {
	messages, _ := firstList(body["messages"])
	for _, raw := range messages {
		message, _ := raw.(map[string]any)
		if message != nil && strings.Contains(fmt.Sprint(message["content"]), content) {
			return true
		}
	}
	return false
}

func jsonStringList(value any) []string {
	items, _ := value.([]any)
	out := make([]string, 0, len(items))
	for _, item := range items {
		if text, ok := item.(string); ok {
			out = append(out, text)
		}
	}
	return out
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func jsonHasAssistantText(body []byte) bool {
	payload := map[string]any{}
	if err := json.Unmarshal(body, &payload); err != nil {
		return false
	}
	choices, _ := payload["choices"].([]any)
	if len(choices) == 0 {
		return false
	}
	choice, _ := choices[0].(map[string]any)
	message, _ := choice["message"].(map[string]any)
	return strings.TrimSpace(fmt.Sprint(message["content"])) != ""
}

func verifyResponsesUsageDetails(body []byte) error {
	payload := map[string]any{}
	if err := json.Unmarshal(body, &payload); err != nil {
		return err
	}
	usage, _ := payload["usage"].(map[string]any)
	inputDetails, _ := usage["input_tokens_details"].(map[string]any)
	outputDetails, _ := usage["output_tokens_details"].(map[string]any)
	if numeric(inputDetails["cached_tokens"]) != 2 || numeric(outputDetails["reasoning_tokens"]) != 1 {
		return fmt.Errorf("responses smoke missing detailed usage: %s", string(body))
	}
	return nil
}

func isTranslatedResponsesSmoke(payload map[string]any) bool {
	return requestHasMessageContent(payload, "fake smoke responses nonstream") ||
		requestHasMessageContent(payload, "fake smoke responses stream") ||
		requestHasMessageContent(payload, "fake smoke responses reasoning nonstream") ||
		requestHasMessageContent(payload, "fake smoke responses reasoning stream") ||
		requestHasMessageContent(payload, "fake smoke responses tool stream") ||
		requestHasMessageContent(payload, "fake smoke responses length stream") ||
		requestHasMessageContent(payload, "fake smoke responses content filter stream") ||
		requestHasMessageContent(payload, "fake smoke exact cache responses")
}

func isTranslatedMessagesSmoke(payload map[string]any) bool {
	return requestHasMessageContent(payload, "fake smoke messages reasoning nonstream") ||
		requestHasMessageContent(payload, "fake smoke messages reasoning stream") ||
		requestHasMessageContent(payload, "fake smoke messages tool stream") ||
		requestHasMessageContent(payload, "fake smoke exact cache messages")
}

func requestHasMessageContent(payload map[string]any, want string) bool {
	messages, _ := payload["messages"].([]any)
	for _, raw := range messages {
		message, _ := raw.(map[string]any)
		if fmt.Sprint(message["content"]) == want {
			return true
		}
	}
	return false
}

func nestedBool(payload map[string]any, keys ...string) (bool, bool) {
	var current any = payload
	for _, key := range keys {
		object, ok := current.(map[string]any)
		if !ok {
			return false, false
		}
		current, ok = object[key]
		if !ok {
			return false, false
		}
	}
	value, ok := current.(bool)
	return value, ok
}

func numeric(value any) float64 {
	switch typed := value.(type) {
	case int:
		return float64(typed)
	case int64:
		return float64(typed)
	case float64:
		return typed
	case json.Number:
		out, _ := typed.Float64()
		return out
	default:
		return 0
	}
}

func contentType(payload map[string]any) string {
	if stream, _ := payload["stream"].(bool); stream {
		return "text/event-stream"
	}
	return "application/json"
}

func defaultRealConfigPath() string {
	if value := strings.TrimSpace(os.Getenv("YALLMP_REAL_CONFIG")); value != "" {
		return value
	}
	return "/home/theio/repos/nidai_db/infra/yaLLMproxy/configs/config.yaml"
}

func defaultString(value string, fallback string) string {
	if strings.TrimSpace(value) != "" {
		return strings.TrimSpace(value)
	}
	return fallback
}
