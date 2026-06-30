package proxy

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"
)

func TestBackendBuildURL(t *testing.T) {
	backend := Backend{BaseURL: "https://api.example.com/v1/"}
	if got := backend.BuildURL("/v1/chat/completions", "stream=true"); got != "https://api.example.com/v1/chat/completions?stream=true" {
		t.Fatalf("url = %s", got)
	}
}

func TestBuildOutboundHeadersStripsAndAddsAuth(t *testing.T) {
	in := http.Header{
		"Authorization":           {"Bearer client"},
		"Connection":              {"keep-alive"},
		"Content-Type":            {"application/json; charset=utf-8"},
		"Accept-Encoding":         {"gzip"},
		"X-Custom-Header":         {"value"},
		"X-Yallmp-Cache-Bypass":   {"true"},
		"X-Yallmp-Internal-Trace": {"secret"},
		"X-Yallmp-Other-Internal": {"secret"},
	}
	out := BuildOutboundHeaders(in, "backend-key", true, "openai", "")
	if got := out.Get("Authorization"); got != "Bearer backend-key" {
		t.Fatalf("authorization = %s", got)
	}
	if out.Get("Connection") != "" {
		t.Fatalf("connection leaked: %#v", out)
	}
	if got := out.Get("Accept-Encoding"); got != "identity" {
		t.Fatalf("accept-encoding = %s", got)
	}
	if got := out.Get("X-Custom-Header"); got != "value" {
		t.Fatalf("custom header = %s", got)
	}
	for _, key := range []string{"X-Yallmp-Cache-Bypass", "X-Yallmp-Internal-Trace", "X-Yallmp-Other-Internal"} {
		if out.Get(key) != "" {
			t.Fatalf("%s leaked: %#v", key, out)
		}
	}
}

func TestBuildOutboundHeadersAnthropic(t *testing.T) {
	in := http.Header{"x-api-key": {"client-key"}}
	out := BuildOutboundHeaders(in, "backend-key", false, "anthropic", "2023-06-01")
	if got := out.Get("x-api-key"); got != "backend-key" {
		t.Fatalf("x-api-key = %s", got)
	}
	if got := out.Get("anthropic-version"); got != "2023-06-01" {
		t.Fatalf("anthropic-version = %s", got)
	}
	if out.Get("Authorization") != "" {
		t.Fatalf("authorization should not be set: %#v", out)
	}
}

func TestBuildBackendBodyRewritesModelAndParametersWithoutImplicitThinking(t *testing.T) {
	payload := map[string]any{
		"model":       "client-model",
		"temperature": 0.1,
	}
	backend := Backend{
		TargetModel:       "upstream-model",
		SupportsReasoning: true,
		Parameters: map[string]ParameterConfig{
			"temperature":                  {Default: 1.0, AllowOverride: false},
			"stream_options.include_usage": {Default: true, AllowOverride: false},
			"top_p":                        {Default: 0.95, AllowOverride: true},
		},
	}
	body := BuildBackendBody(payload, backend, []byte(`{"model":"client-model"}`), true)
	var got map[string]any
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}
	if got["model"] != "upstream-model" {
		t.Fatalf("model = %v", got["model"])
	}
	if got["temperature"] != 1.0 {
		t.Fatalf("temperature = %v", got["temperature"])
	}
	if got["top_p"] != 0.95 {
		t.Fatalf("top_p = %v", got["top_p"])
	}
	if got["stream"] != true {
		t.Fatalf("stream = %v", got["stream"])
	}
	if _, ok := got["thinking"]; ok {
		t.Fatalf("thinking was added from supports_reasoning: %#v", got["thinking"])
	}
	if got["stream_options"].(map[string]any)["include_usage"] != true {
		t.Fatalf("stream_options = %#v", got["stream_options"])
	}
}

func TestBuildBackendBodyPreservesExplicitThinking(t *testing.T) {
	payload := map[string]any{
		"model":    "client-model",
		"thinking": map[string]any{"type": "enabled"},
	}
	backend := Backend{
		TargetModel:       "upstream-model",
		SupportsReasoning: true,
	}
	body := BuildBackendBody(payload, backend, []byte(`{"model":"client-model","thinking":{"type":"enabled"}}`), false)
	var got map[string]any
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}
	if got["model"] != "upstream-model" {
		t.Fatalf("model = %v", got["model"])
	}
	if got["thinking"].(map[string]any)["type"] != "enabled" {
		t.Fatalf("thinking = %#v", got["thinking"])
	}
}

func TestBuildBackendBodyDropsToolOnlyFieldsWithoutTools(t *testing.T) {
	payload := map[string]any{
		"model":               "client-model",
		"tool_choice":         "auto",
		"parallel_tool_calls": true,
	}
	backend := Backend{
		TargetModel: "upstream-model",
		Parameters: map[string]ParameterConfig{
			"tool_choice": {Default: "auto", AllowOverride: false},
		},
	}
	result := BuildBackendBodyWithWarnings(payload, backend, mustJSON(payload), false)
	body := result.Body
	var got map[string]any
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}
	if _, ok := got["tool_choice"]; ok {
		t.Fatalf("tool_choice leaked without tools: %#v", got)
	}
	if _, ok := got["parallel_tool_calls"]; ok {
		t.Fatalf("parallel_tool_calls leaked without tools: %#v", got)
	}
	if got["model"] != "upstream-model" {
		t.Fatalf("model = %v", got["model"])
	}
	if !hasWarningCode(result.Warnings, "tool_parameter_without_tools") || !hasWarningCode(result.Warnings, "tool_field_without_tools") {
		t.Fatalf("warnings = %#v", result.Warnings)
	}
}

func TestBuildBackendBodyKeepsToolChoiceWithTools(t *testing.T) {
	payload := map[string]any{
		"model": "client-model",
		"tools": []any{map[string]any{
			"type":     "function",
			"function": map[string]any{"name": "lookup"},
		}},
	}
	backend := Backend{
		TargetModel: "upstream-model",
		Parameters: map[string]ParameterConfig{
			"tool_choice": {Default: "auto", AllowOverride: false},
		},
	}
	body := BuildBackendBody(payload, backend, mustJSON(payload), false)
	var got map[string]any
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}
	if got["tool_choice"] != "auto" {
		t.Fatalf("tool_choice = %#v", got["tool_choice"])
	}
}

func TestBuildBackendBodyNormalizesInvalidToolCallArguments(t *testing.T) {
	payload := map[string]any{
		"model": "client-model",
		"messages": []any{
			map[string]any{"role": "assistant", "tool_calls": []any{
				map[string]any{
					"id":   "call_bad",
					"type": "function",
					"function": map[string]any{
						"name":      "lookup",
						"arguments": "{",
					},
				},
				map[string]any{
					"id":   "call_ok",
					"type": "function",
					"function": map[string]any{
						"name":      "lookup",
						"arguments": `{"q":"x"}`,
					},
				},
				map[string]any{
					"id":   "call_map",
					"type": "function",
					"function": map[string]any{
						"name":      "lookup",
						"arguments": map[string]any{"q": "y"},
					},
				},
			}},
		},
	}
	backend := Backend{TargetModel: "upstream-model"}
	result := BuildBackendBodyWithWarnings(payload, backend, mustJSON(payload), false)
	body := result.Body
	originalMessages := payload["messages"].([]any)
	originalCalls := originalMessages[0].(map[string]any)["tool_calls"].([]any)
	originalFunction := originalCalls[0].(map[string]any)["function"].(map[string]any)
	if originalFunction["arguments"] != "{" {
		t.Fatalf("original payload was mutated: %#v", originalFunction["arguments"])
	}
	var got map[string]any
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}
	messages := got["messages"].([]any)
	calls := messages[0].(map[string]any)["tool_calls"].([]any)
	badFunction := calls[0].(map[string]any)["function"].(map[string]any)
	if badFunction["arguments"] != "{}" {
		t.Fatalf("bad arguments = %#v", badFunction["arguments"])
	}
	okFunction := calls[1].(map[string]any)["function"].(map[string]any)
	if okFunction["arguments"] != `{"q":"x"}` {
		t.Fatalf("valid arguments changed: %#v", okFunction["arguments"])
	}
	mapFunction := calls[2].(map[string]any)["function"].(map[string]any)
	if mapFunction["arguments"] != `{"q":"y"}` {
		t.Fatalf("map arguments = %#v", mapFunction["arguments"])
	}
	if !hasWarningCode(result.Warnings, "invalid_tool_call_arguments") {
		t.Fatalf("warnings = %#v", result.Warnings)
	}
}

func TestBuildBackendBodySkipsStreamOptionsForNonStreamRequests(t *testing.T) {
	payload := map[string]any{"model": "client-model"}
	backend := Backend{
		TargetModel: "upstream-model",
		Parameters: map[string]ParameterConfig{
			"stream_options.include_usage": {Default: true, AllowOverride: false},
		},
	}
	body := BuildBackendBody(payload, backend, []byte(`{"model":"client-model"}`), false)
	var got map[string]any
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}
	if _, ok := got["stream_options"]; ok {
		t.Fatalf("stream_options leaked into non-stream request: %#v", got)
	}
	if got["model"] != "upstream-model" {
		t.Fatalf("model = %v", got["model"])
	}
}

func TestFilterResponseHeaders(t *testing.T) {
	headers := http.Header{
		"Transfer-Encoding": {"chunked"},
		"Content-Length":    {"100"},
		"Content-Encoding":  {"gzip"},
		"Content-Type":      {"application/json"},
		"X-Request-Id":      {"abc"},
	}
	got := FilterResponseHeaders(headers)
	if got.Get("Transfer-Encoding") != "" || got.Get("Content-Length") != "" || got.Get("Content-Encoding") != "" {
		t.Fatalf("hop or body headers leaked: %#v", got)
	}
	if got.Get("X-Request-Id") != "abc" {
		t.Fatalf("custom header missing: %#v", got)
	}
}

func TestNormalizeAndExtractModel(t *testing.T) {
	if got := NormalizeRequestModel(" OpenAI/gpt-4 "); got != "gpt-4" {
		t.Fatalf("normalized = %q", got)
	}
	if got := ExtractAPIType(map[string]any{"api_type": "Azure"}); got != "azure" {
		t.Fatalf("api type = %q", got)
	}
	if got := ExtractTargetModel(map[string]any{"model": "openai/gpt-4"}, "openai"); got != "gpt-4" {
		t.Fatalf("target model = %q", got)
	}
}

func TestParseTimeout(t *testing.T) {
	if got := ParseTimeout(1.5); got != 1500*time.Millisecond {
		t.Fatalf("duration = %s", got)
	}
	if got := ParseTimeout("2s"); got != 2*time.Second {
		t.Fatalf("duration = %s", got)
	}
}

func mustJSON(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}

func hasWarningCode(warnings []RequestWarning, code string) bool {
	for _, warning := range warnings {
		if warning.Code == code {
			return true
		}
	}
	return false
}
