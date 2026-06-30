package gigachat

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestBuildCloudAndLocalConfig(t *testing.T) {
	cloud, err := BuildConfig(map[string]any{
		"api_type": "gigachat",
		"api_base": "https://gigachat.example/api/v1",
		"api_key":  "encoded-key",
		"scope":    DefaultScope,
		"auth_url": "https://auth.example/oauth",
		"model":    "GigaChat-Max",
	})
	if err != nil {
		t.Fatal(err)
	}
	if cloud.Mode != "cloud" || cloud.APIKey != "encoded-key" || cloud.Scope != DefaultScope || cloud.AuthURL != "https://auth.example/oauth" {
		t.Fatalf("cloud config = %#v", cloud)
	}
	local, err := BuildConfig(map[string]any{
		"api_type":    "gigachat",
		"api_base":    "https://gigachat.local/api/v1",
		"client_cert": "/certs/client.pem",
		"client_key":  "/certs/client.key",
		"model":       "GigaChat-Local",
	})
	if err != nil {
		t.Fatal(err)
	}
	if local.Mode != "local" || local.ClientCertFile != "/certs/client.pem" || local.ClientKeyFile != "/certs/client.key" || local.APIKey != "" {
		t.Fatalf("local config = %#v", local)
	}
	if _, err := BuildConfig(map[string]any{"api_type": "gigachat", "api_base": "https://x"}); err == nil {
		t.Fatalf("expected missing credentials error")
	}
}

func TestTranslatorConvertsToolsAndFunctionCallResponse(t *testing.T) {
	payload := map[string]any{
		"model":    "GigaChat-Max",
		"messages": []any{map[string]any{"role": "user", "content": "Read file"}},
		"tools": []any{map[string]any{
			"type": "function",
			"function": map[string]any{
				"name":        "read",
				"description": "Read a file.",
				"parameters": map[string]any{
					"type":       "object",
					"properties": map[string]any{"filename": map[string]any{"type": "string"}},
				},
			},
		}},
	}
	converted, err := OpenAIChatToGigaChat(payload, "GigaChat-Max")
	if err != nil {
		t.Fatal(err)
	}
	if converted["function_call"] != "auto" {
		t.Fatalf("function_call = %#v", converted["function_call"])
	}
	functions := converted["functions"].([]map[string]any)
	if functions[0]["name"] != "read" {
		t.Fatalf("functions = %#v", functions)
	}

	response := GigaChatResponseToOpenAI(map[string]any{
		"id":    "answer-1",
		"model": "GigaChat-Max",
		"choices": []any{map[string]any{
			"message":       map[string]any{"content": "", "function_call": map[string]any{"name": "show_files", "arguments": map[string]any{"limit": 10}}},
			"finish_reason": "tool_call",
		}},
	}, "GigaChat-Max")
	choices := response["choices"].([]any)
	message := choices[0].(map[string]any)["message"].(map[string]any)
	toolCalls := message["tool_calls"].([]any)
	call := toolCalls[0].(map[string]any)
	function := call["function"].(map[string]any)
	if choices[0].(map[string]any)["finish_reason"] != "tool_calls" || function["name"] != "show_files" {
		t.Fatalf("response = %#v", response)
	}
	var args map[string]any
	if err := json.Unmarshal([]byte(function["arguments"].(string)), &args); err != nil {
		t.Fatal(err)
	}
	if args["limit"] != float64(10) {
		t.Fatalf("arguments = %#v", args)
	}
}

func TestClientGetsCloudTokenAndConvertsChatResponse(t *testing.T) {
	var authHits atomic.Int32
	var chatHits atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/oauth":
			authHits.Add(1)
			if r.Header.Get("Authorization") != "Basic encoded-key" {
				t.Errorf("auth header = %s", r.Header.Get("Authorization"))
			}
			body, _ := io.ReadAll(r.Body)
			if string(body) != "scope=GIGACHAT_API_CORP" {
				t.Errorf("auth body = %s", body)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"access_token":"token-1","expires_at":4102444800000}`))
		case "/api/v1/chat/completions":
			chatHits.Add(1)
			if r.Header.Get("Authorization") != "Bearer token-1" {
				t.Errorf("chat auth = %s", r.Header.Get("Authorization"))
			}
			if r.Header.Get("Accept") != "application/json" {
				t.Errorf("chat accept = %s", r.Header.Get("Accept"))
			}
			var got map[string]any
			if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
				t.Fatal(err)
			}
			messages := got["messages"].([]any)
			if messages[0].(map[string]any)["content"] != "Hello" {
				t.Fatalf("messages = %#v", messages)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"resp-1","model":"GigaChat-Max","choices":[{"message":{"content":"Hi"},"finish_reason":"stop"}]}`))
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer upstream.Close()

	cfg, err := BuildConfig(map[string]any{
		"api_type": "gigachat",
		"api_base": upstream.URL + "/api/v1",
		"api_key":  "encoded-key",
		"scope":    DefaultScope,
		"auth_url": upstream.URL + "/oauth",
		"model":    "GigaChat-Max",
	})
	if err != nil {
		t.Fatal(err)
	}
	resp, err := NewBackend(cfg).Execute(context.Background(), map[string]any{
		"messages": []any{map[string]any{"role": "user", "content": "Hello"}},
	}, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.Contains(resp.Header.Get("Content-Type"), "application/json") {
		t.Fatalf("response status=%d content-type=%q", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	var data map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		t.Fatal(err)
	}
	choice := data["choices"].([]any)[0].(map[string]any)
	message := choice["message"].(map[string]any)
	if message["content"] != "Hi" || authHits.Load() != 1 || chatHits.Load() != 1 {
		t.Fatalf("data=%#v authHits=%d chatHits=%d", data, authHits.Load(), chatHits.Load())
	}
}

func TestClientEmulatesToolCallsWhenEnabled(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/oauth":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"access_token":"token-1","expires_at":4102444800000}`))
		case "/api/v1/chat/completions":
			var got map[string]any
			if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
				t.Fatal(err)
			}
			if _, ok := got["functions"]; ok {
				t.Fatalf("native functions should not be sent during emulation: %#v", got)
			}
			messages := got["messages"].([]any)
			if !strings.Contains(messages[0].(map[string]any)["content"].(string), `"action":"call_tool"`) {
				t.Fatalf("system message = %#v", messages[0])
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"resp-1","model":"GigaChat-Max","choices":[{"message":{"content":"{\"action\":\"call_tool\",\"tool\":\"show_files\",\"arguments\":{}}"},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}`))
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer upstream.Close()

	cfg, err := BuildConfig(map[string]any{
		"api_type":           "gigachat",
		"api_base":           upstream.URL + "/api/v1",
		"api_key":            "encoded-key",
		"scope":              DefaultScope,
		"auth_url":           upstream.URL + "/oauth",
		"model":              "GigaChat-Max",
		"emulate_tool_calls": true,
	})
	if err != nil {
		t.Fatal(err)
	}
	resp, err := NewBackend(cfg).Execute(context.Background(), map[string]any{
		"messages": []any{map[string]any{"role": "user", "content": "Show files"}},
		"tools": []any{map[string]any{
			"type":     "function",
			"function": map[string]any{"name": "show_files", "parameters": map[string]any{"type": "object", "properties": map[string]any{}}},
		}},
	}, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var data map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		t.Fatal(err)
	}
	choice := data["choices"].([]any)[0].(map[string]any)
	if choice["finish_reason"] != "tool_calls" {
		t.Fatalf("data = %#v", data)
	}
	if data["usage"].(map[string]any)["total_tokens"] != float64(15) {
		t.Fatalf("usage = %#v", data["usage"])
	}
}

func TestClientConvertsStreamingChunks(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/oauth":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"access_token":"token-1","expires_at":4102444800000}`))
		case "/api/v1/chat/completions":
			if r.Header.Get("Accept") != "text/event-stream" {
				t.Errorf("chat accept = %s", r.Header.Get("Accept"))
			}
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte("data: {\"id\":\"chunk-1\",\"model\":\"GigaChat-Max\",\"choices\":[{\"delta\":{\"content\":\"Hi\"},\"finish_reason\":null}]}\n\n"))
			_, _ = w.Write([]byte("data: [DONE]\n\n"))
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer upstream.Close()

	cfg, err := BuildConfig(map[string]any{
		"api_type": "gigachat",
		"api_base": upstream.URL + "/api/v1",
		"api_key":  "encoded-key",
		"scope":    DefaultScope,
		"auth_url": upstream.URL + "/oauth",
		"model":    "GigaChat-Max",
	})
	if err != nil {
		t.Fatal(err)
	}
	resp, err := NewBackend(cfg).Execute(context.Background(), map[string]any{
		"messages": []any{map[string]any{"role": "user", "content": "Hello"}},
		"stream":   true,
	}, true, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.Contains(resp.Header.Get("Content-Type"), "text/event-stream") {
		t.Fatalf("response status=%d content-type=%q", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), `"object":"chat.completion.chunk"`) || !strings.Contains(string(body), `"content":"Hi"`) || !strings.Contains(string(body), "data: [DONE]") {
		t.Fatalf("stream body = %s", body)
	}
}

func TestClientNormalizesNonStreamUpstreamErrorAsJSON(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/oauth":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"access_token":"token-1","expires_at":4102444800000}`))
		case "/api/v1/chat/completions":
			if r.Header.Get("Accept") != "application/json" {
				t.Errorf("chat accept = %s", r.Header.Get("Accept"))
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"message":"quota"}`))
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer upstream.Close()

	cfg, err := BuildConfig(map[string]any{
		"api_type": "gigachat",
		"api_base": upstream.URL + "/api/v1",
		"api_key":  "encoded-key",
		"scope":    DefaultScope,
		"auth_url": upstream.URL + "/oauth",
		"model":    "GigaChat-Max",
	})
	if err != nil {
		t.Fatal(err)
	}
	resp, err := NewBackend(cfg).Execute(context.Background(), map[string]any{
		"messages": []any{map[string]any{"role": "user", "content": "Hello"}},
	}, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusTooManyRequests || !strings.Contains(resp.Header.Get("Content-Type"), "application/json") {
		t.Fatalf("response status=%d content-type=%q", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	var data map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		t.Fatal(err)
	}
	errObj := data["error"].(map[string]any)
	if errObj["type"] != "upstream_error" || errObj["message"] != `{"message":"quota"}` || errObj["code"] != float64(http.StatusTooManyRequests) {
		t.Fatalf("error response = %#v", data)
	}
}

func TestClientNormalizesStreamUpstreamErrorAsSSE(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/oauth":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"access_token":"token-1","expires_at":4102444800000}`))
		case "/api/v1/chat/completions":
			if r.Header.Get("Accept") != "text/event-stream" {
				t.Errorf("chat accept = %s", r.Header.Get("Accept"))
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"message":"quota"}`))
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer upstream.Close()

	cfg, err := BuildConfig(map[string]any{
		"api_type": "gigachat",
		"api_base": upstream.URL + "/api/v1",
		"api_key":  "encoded-key",
		"scope":    DefaultScope,
		"auth_url": upstream.URL + "/oauth",
		"model":    "GigaChat-Max",
	})
	if err != nil {
		t.Fatal(err)
	}
	resp, err := NewBackend(cfg).Execute(context.Background(), map[string]any{
		"messages": []any{map[string]any{"role": "user", "content": "Hello"}},
		"stream":   true,
	}, true, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusTooManyRequests || !strings.Contains(resp.Header.Get("Content-Type"), "text/event-stream") || resp.Header.Get("Cache-Control") != "no-cache" {
		t.Fatalf("response status=%d content-type=%q cache-control=%q", resp.StatusCode, resp.Header.Get("Content-Type"), resp.Header.Get("Cache-Control"))
	}
	body, _ := io.ReadAll(resp.Body)
	parts := strings.SplitN(string(body), "\n\n", 2)
	if len(parts) != 2 || !strings.HasPrefix(parts[0], "data: ") || !strings.Contains(string(body), "data: [DONE]") {
		t.Fatalf("stream error body = %s", body)
	}
	var data map[string]any
	if err := json.Unmarshal([]byte(strings.TrimPrefix(parts[0], "data: ")), &data); err != nil {
		t.Fatal(err)
	}
	errObj := data["error"].(map[string]any)
	if errObj["type"] != "upstream_error" || errObj["message"] != `{"message":"quota"}` || errObj["code"] != float64(http.StatusTooManyRequests) {
		t.Fatalf("stream error response = %#v", data)
	}
}
