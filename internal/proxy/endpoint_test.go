package proxy

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/theio/yallmp-proxy/internal/config"
	"github.com/theio/yallmp-proxy/internal/modules"
)

func TestNativeInferenceDoesNotReceiveChatDefaultsOrParsers(t *testing.T) {
	const nativeResponse = `{"answers":{"x":{"noul":0.987654321}},"choices":[{"message":{"reasoning_content":"retain","content":"value"}}]}`
	for _, path := range []string{"/v1/systemone", "/v1/decisions", "/v1/embeddings"} {
		t.Run(path, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var payload map[string]any
				_ = json.NewDecoder(r.Body).Decode(&payload)
				if payload["model"] != "upstream" || payload["temperature"] != nil || payload["tool_choice"] != "extension" {
					t.Errorf("outbound payload: %#v", payload)
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, nativeResponse)
			}))
			defer upstream.Close()
			router, err := NewRouter(&config.Config{Nodes: []config.NodeEntry{{Name: "n", Model: "public", UpstreamModel: "upstream", APIBase: upstream.URL, Endpoints: []string{path}, Parameters: map[string]any{"temperature": map[string]any{"default": 0.1}}}}})
			if err != nil {
				t.Fatal(err)
			}
			router.ResponsePipeline = modules.Pipeline{Modules: []modules.ResponseModule{modules.NewReasoningSwap("reasoning_to_content")}}
			payload := map[string]any{"model": "public", "tool_choice": "extension"}
			body, _ := json.Marshal(payload)
			resp, err := router.Forward(context.Background(), "public", path, "", body, payload, false, http.Header{})
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			got, _ := io.ReadAll(resp.Body)
			if string(got) != nativeResponse {
				t.Fatalf("response changed: %s", got)
			}
		})
	}
}

func TestEmbeddingRoutesStayInOneVectorSpace(t *testing.T) {
	router, err := NewRouter(&config.Config{Nodes: []config.NodeEntry{
		{Name: "a", Model: "embed", UpstreamModel: "model-a", APIBase: "http://a.test/v1", Endpoints: []string{"embeddings"}},
		{Name: "b", Model: "embed", UpstreamModel: "model-b", APIBase: "http://b.test/v1", Endpoints: []string{"embeddings"}},
		{Name: "c", Model: "fallback", UpstreamModel: "model-a", APIBase: "http://c.test/v1", Endpoints: []string{"embeddings"}},
	}, RouterSettings: config.RouterSettings{Fallbacks: []map[string]any{{"embed": []any{"fallback"}}}}})
	if err != nil {
		t.Fatal(err)
	}
	for range 3 {
		route, err := router.BuildRouteForPath("embed", "/v1/embeddings", nil)
		if err != nil || len(route) != 2 || route[0].Name != "a" || route[1].Name != "c" {
			t.Fatalf("route=%v err=%v", route, err)
		}
	}
	a, b := router.Backends["a"], router.Backends["b"]
	a.EmbeddingSpace = "same-checkpoint"
	b.EmbeddingSpace = a.EmbeddingSpace
	router.Backends["a"], router.Backends["b"] = a, b
	route, err := router.BuildRouteForPath("embed", "/v1/embeddings", nil)
	if err != nil || len(route) != 2 {
		t.Fatalf("declared equivalent replicas not pooled: %v %v", route, err)
	}
	b.Parameters = map[string]ParameterConfig{"dimensions": {Default: 128}}
	router.Backends["b"] = b
	route, err = router.BuildRouteForPath("embed", "/v1/embeddings", nil)
	if err != nil || len(route) != 1 || route[0].Name != "a" {
		t.Fatalf("incompatible dimension defaults pooled: %v %v", route, err)
	}
}

func TestEmbeddingFallbacksAnchorSpaceWithoutEmbeddingPrimary(t *testing.T) {
	for _, primary := range []string{"chat pool", "chat legacy", "absent"} {
		t.Run(primary, func(t *testing.T) {
			cfg := &config.Config{
				Nodes: []config.NodeEntry{
					{Name: "a", Model: "embed-a", UpstreamModel: "model-a", APIBase: "http://a.test/v1", Endpoints: []string{"embeddings"}},
					{Name: "b", Model: "embed-b", UpstreamModel: "model-b", APIBase: "http://b.test/v1", Endpoints: []string{"embeddings"}},
					{Name: "c", Model: "embed-c", UpstreamModel: "model-a", APIBase: "http://c.test/v1", Endpoints: []string{"embeddings"}},
				},
				RouterSettings: config.RouterSettings{Fallbacks: []map[string]any{{"public": []any{"embed-a", "embed-b", "embed-c"}}}},
			}
			if primary == "chat pool" {
				cfg.Nodes = append(cfg.Nodes, config.NodeEntry{Name: "chat", Model: "public", APIBase: "http://chat.test/v1", Endpoints: []string{"chat"}})
			} else if primary == "chat legacy" {
				cfg.ModelList = []config.ModelEntry{{ModelName: "public", ModelParams: map[string]any{"api_base": "http://chat.test/v1", "model": "chat", "endpoints": []any{"chat"}}}}
			}
			router, err := NewRouter(cfg)
			if err != nil {
				t.Fatal(err)
			}
			for range 3 {
				route, err := router.BuildRouteForPath("public", "/v1/embeddings", nil)
				if err != nil || len(route) != 2 || route[0].Name != "a" || route[1].Name != "c" {
					t.Fatalf("fallback vector space changed: %v %v", route, err)
				}
			}
			router.FailureThreshold = 1
			router.recordBackendFailure("a", 503, "unavailable")
			route, err := router.BuildRouteForPath("public", "/v1/embeddings", nil)
			if err != nil || len(route) != 1 || route[0].Name != "c" {
				t.Fatalf("health selection changed vector space: %v %v", route, err)
			}
		})
	}
}

func TestNewEndpointsRequireDeclaredSupport(t *testing.T) {
	for _, endpoint := range []string{"systemone", "decisions", "transcriptions", "realtime", "voice"} {
		if backendSupportsEndpoint(Backend{APIType: "openai"}, endpoint) {
			t.Fatalf("implicit support for %s", endpoint)
		}
	}
	if backendSupportsEndpoint(Backend{APIType: "gigachat", Endpoints: []string{"systemone"}}, "systemone") {
		t.Fatal("GigaChat accepted System One")
	}
	if !backendSupportsEndpoint(Backend{APIType: "systemone"}, "systemone") {
		t.Fatal("native System One unavailable")
	}
}
