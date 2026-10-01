package proxy

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/theio/yallmp-proxy/internal/config"
	"github.com/theio/yallmp-proxy/internal/modules"
	"github.com/theio/yallmp-proxy/internal/observability"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

func TestRouterSeparatesPassthroughBackends(t *testing.T) {
	router, err := NewRouter(&config.Config{ModelList: []config.ModelEntry{
		{ModelName: "regular", ModelParams: map[string]any{"api_base": "http://regular.test/v1", "api_type": "openai"}},
		{ModelName: "pass", ModelParams: map[string]any{"api_base": "http://pass.test", "api_type": "passthrough"}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := router.Backends["regular"]; !ok {
		t.Fatalf("regular backend missing")
	}
	if _, ok := router.PassthroughBackends["pass"]; !ok {
		t.Fatalf("passthrough backend missing")
	}
	if slices := router.ListPassthroughModelNames(); len(slices) != 1 || slices[0] != "pass" {
		t.Fatalf("passthrough names = %#v", slices)
	}
}

func TestRouterBuildRouteFallback(t *testing.T) {
	router, err := NewRouter(&config.Config{
		ModelList: []config.ModelEntry{
			{ModelName: "alpha", ModelParams: map[string]any{"api_base": "http://alpha.test/v1"}},
			{ModelName: "beta", ModelParams: map[string]any{"api_base": "http://beta.test/v1"}},
		},
		RouterSettings: config.RouterSettings{Fallbacks: []map[string]any{{"alpha": []any{"beta"}}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	route, err := router.BuildRoute("alpha", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	if len(route) != 2 || route[0].Name != "alpha" || route[1].Name != "beta" {
		t.Fatalf("route = %#v", route)
	}
}

func TestRouterNodePoolRoundRobinAndSticky(t *testing.T) {
	router, err := NewRouter(&config.Config{
		Nodes: []config.NodeEntry{
			{Name: "n1", APIBase: "http://n1.test/v1", Model: "m"},
			{Name: "n2", APIBase: "http://n2.test/v1", Model: "m"},
		},
		Models: []config.ClientModelEntry{{Name: "client", Nodes: []string{"n1", "n2"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	r1, _ := router.BuildRoute("client", map[string]any{})
	r2, _ := router.BuildRoute("client", map[string]any{})
	if r1[0].Name != "n1" || r2[0].Name != "n2" {
		t.Fatalf("round robin got %s then %s", r1[0].Name, r2[0].Name)
	}
	sticky1, _ := router.BuildRoute("client", map[string]any{"conversation_id": "abc"})
	sticky2, _ := router.BuildRoute("client", map[string]any{"conversation_id": "abc"})
	if sticky1[0].Name != sticky2[0].Name {
		t.Fatalf("sticky route changed from %s to %s", sticky1[0].Name, sticky2[0].Name)
	}
}

func TestRouterConcurrencyAwareSelectionDisabledByDefaultPreservesRoundRobin(t *testing.T) {
	router, err := NewRouter(&config.Config{
		Nodes: []config.NodeEntry{
			{Name: "n1", APIBase: "http://n1.test/v1", Model: "m"},
			{Name: "n2", APIBase: "http://n2.test/v1", Model: "m"},
		},
		Models: []config.ClientModelEntry{{Name: "client", Nodes: []string{"n1", "n2"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	release := router.TrackBackendAttempt("n1")
	defer release()
	if router.ConcurrencyAwareSelection {
		t.Fatal("concurrency-aware selection should be disabled by default")
	}
	r1, err := router.BuildRoute("client", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	r2, err := router.BuildRoute("client", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	if r1[0].Name != "n1" || r2[0].Name != "n2" {
		t.Fatalf("round robin changed when disabled: got %s then %s", r1[0].Name, r2[0].Name)
	}
}

func TestRouterConcurrencyAwareSelectionPrefersLeastInflightNode(t *testing.T) {
	router := newConcurrencyAwareRouter(t)
	release := router.TrackBackendAttempt("n1")
	defer release()
	route, err := router.BuildRoute("client", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	if route[0].Name != "n2" {
		t.Fatalf("route = %s, want least-inflight n2", route[0].Name)
	}
}

func TestRouterConcurrencyAwareSelectionTieUsesWeightedRoundRobin(t *testing.T) {
	router, err := NewRouter(&config.Config{
		Nodes: []config.NodeEntry{
			{Name: "n1", APIBase: "http://n1.test/v1", Model: "m", Weight: 2},
			{Name: "n2", APIBase: "http://n2.test/v1", Model: "m", Weight: 1},
		},
		Models: []config.ClientModelEntry{{Name: "client", Nodes: []string{"n1", "n2"}, Balancing: "weighted_round_robin"}},
		RouterSettings: config.RouterSettings{
			ConcurrencyAwareSelection: config.ConcurrencyAwareSelectionSettings{Enabled: true},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for i := 0; i < 6; i++ {
		route, err := router.BuildRoute("client", map[string]any{})
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, route[0].Name)
	}
	want := []string{"n1", "n1", "n2", "n1", "n1", "n2"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("weighted tie sequence = %#v, want %#v", got, want)
		}
	}
}

func TestRouterConcurrencyAwareSelectionStickyWins(t *testing.T) {
	router := newConcurrencyAwareRouter(t)
	release := router.TrackBackendAttempt("n1")
	defer release()
	payload := map[string]any{"conversation_id": "abc"}
	router.BindSticky("conversation_id", "abc", "n1")
	route, err := router.BuildRoute("client", payload)
	if err != nil {
		t.Fatal(err)
	}
	if route[0].Name != "n1" {
		t.Fatalf("sticky route must win over concurrency-aware selection: got %s", route[0].Name)
	}
}

func TestRouterConcurrencyAwareSelectionPrefixAffinityWins(t *testing.T) {
	router, err := NewRouter(&config.Config{
		Nodes: []config.NodeEntry{
			{Name: "n1", APIBase: "http://n1.test/v1", Model: "m"},
			{Name: "n2", APIBase: "http://n2.test/v1", Model: "m"},
		},
		Models: []config.ClientModelEntry{{Name: "client", Nodes: []string{"n1", "n2"}}},
		RouterSettings: config.RouterSettings{
			PrefixCacheAffinity:       config.PrefixCacheAffinitySettings{Enabled: true},
			ConcurrencyAwareSelection: config.ConcurrencyAwareSelectionSettings{Enabled: true},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	release := router.TrackBackendAttempt("n1")
	defer release()
	payload := map[string]any{"messages": prefixAffinityMessages("shared")}
	router.recordPrefixAffinity("n1", payload)
	route, err := router.BuildRoute("client", payload)
	if err != nil {
		t.Fatal(err)
	}
	if route[0].Name != "n1" {
		t.Fatalf("prefix affinity must win over concurrency-aware selection: got %s", route[0].Name)
	}
}

func TestRouterConcurrencyAwareSelectionRespectsEligibilityPredicates(t *testing.T) {
	router := newConcurrencyAwareRouter(t)
	for i := 0; i < 3; i++ {
		release := router.TrackBackendAttempt("n2")
		defer release()
	}
	route, err := router.BuildRouteForKeyWithOptions("client", "/v1/chat/completions", map[string]any{}, "", RouteOptions{
		BackendAvailable:  func(backend Backend) bool { return backend.Name != "n1" },
		QuotaAvailability: func(backend Backend) bool { return backend.Name != "n1" },
	})
	if err != nil {
		t.Fatal(err)
	}
	if route[0].Name != "n2" {
		t.Fatalf("ineligible lower-inflight node must not be selected: got %s", route[0].Name)
	}
}

func TestRouterConcurrencyAwareSelectionRespectsEndpointKeysAndHealth(t *testing.T) {
	t.Run("endpoint support", func(t *testing.T) {
		router, err := NewRouter(&config.Config{
			Nodes: []config.NodeEntry{
				{Name: "n1", APIBase: "http://n1.test/v1", Model: "m", Endpoints: []string{"embeddings"}},
				{Name: "n2", APIBase: "http://n2.test/v1", Model: "m", Endpoints: []string{"chat"}},
			},
			Models: []config.ClientModelEntry{{Name: "client", Nodes: []string{"n1", "n2"}}},
			RouterSettings: config.RouterSettings{
				ConcurrencyAwareSelection: config.ConcurrencyAwareSelectionSettings{Enabled: true},
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		release := router.TrackBackendAttempt("n2")
		defer release()
		route, err := router.BuildRouteForPath("client", "/v1/chat/completions", map[string]any{})
		if err != nil {
			t.Fatal(err)
		}
		if route[0].Name != "n2" {
			t.Fatalf("endpoint-ineligible lower-inflight node must not be selected: got %s", route[0].Name)
		}
	})
	t.Run("allowed keys", func(t *testing.T) {
		router, err := NewRouter(&config.Config{
			Nodes: []config.NodeEntry{
				{Name: "n1", APIBase: "http://n1.test/v1", Model: "m", AllowedKeys: []any{"key-1"}},
				{Name: "n2", APIBase: "http://n2.test/v1", Model: "m", AllowedKeys: []any{"key-2"}},
			},
			Models: []config.ClientModelEntry{{Name: "client", Nodes: []string{"n1", "n2"}}},
			RouterSettings: config.RouterSettings{
				ConcurrencyAwareSelection: config.ConcurrencyAwareSelectionSettings{Enabled: true},
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		release := router.TrackBackendAttempt("n2")
		defer release()
		route, err := router.BuildRouteForKey("client", "/v1/chat/completions", map[string]any{}, "key-2")
		if err != nil {
			t.Fatal(err)
		}
		if route[0].Name != "n2" {
			t.Fatalf("key-ineligible lower-inflight node must not be selected: got %s", route[0].Name)
		}
	})
	t.Run("health", func(t *testing.T) {
		router := newConcurrencyAwareRouter(t)
		router.FailureThreshold = 1
		router.recordBackendFailure("n1", http.StatusServiceUnavailable, "down")
		release := router.TrackBackendAttempt("n2")
		defer release()
		route, err := router.BuildRoute("client", map[string]any{})
		if err != nil {
			t.Fatal(err)
		}
		if route[0].Name != "n2" {
			t.Fatalf("unhealthy lower-inflight node must not be selected: got %s", route[0].Name)
		}
	})
}

func TestRouterWeightedRoundRobinUsesNodeWeights(t *testing.T) {
	router, err := NewRouter(&config.Config{
		Nodes: []config.NodeEntry{
			{Name: "n1", APIBase: "http://n1.test/v1", Model: "m", Weight: 2},
			{Name: "n2", APIBase: "http://n2.test/v1", Model: "m", Weight: 1},
		},
		Models: []config.ClientModelEntry{{Name: "client", Nodes: []string{"n1", "n2"}, Balancing: "weighted_round_robin"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for i := 0; i < 6; i++ {
		route, err := router.BuildRoute("client", map[string]any{})
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, route[0].Name)
	}
	want := []string{"n1", "n1", "n2", "n1", "n1", "n2"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("weighted sequence = %#v, want %#v", got, want)
		}
	}
}

func TestRouterWeightedRoundRobinHonorsModelWeightOverridesAndHealth(t *testing.T) {
	router, err := NewRouter(&config.Config{
		Nodes: []config.NodeEntry{
			{Name: "n1", APIBase: "http://n1.test/v1", Model: "m", Weight: 5},
			{Name: "n2", APIBase: "http://n2.test/v1", Model: "m", Weight: 1},
		},
		Models: []config.ClientModelEntry{{
			Name:        "client",
			Nodes:       []string{"n1", "n2"},
			NodeWeights: map[string]int{"n1": 1, "n2": 3},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for i := 0; i < 4; i++ {
		route, err := router.BuildRoute("client", map[string]any{})
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, route[0].Name)
	}
	want := []string{"n1", "n2", "n2", "n2"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("weighted override sequence = %#v, want %#v", got, want)
		}
	}

	router.FailureThreshold = 1
	router.recordBackendFailure("n2", http.StatusServiceUnavailable, "down")
	route, err := router.BuildRoute("client", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	if len(route) != 1 || route[0].Name != "n1" {
		t.Fatalf("route after n2 failure = %#v", route)
	}
}

func TestRouterBackendLimitsInheritProviderAndNodeOverrides(t *testing.T) {
	router, err := NewRouter(&config.Config{
		Providers: []config.ProviderEntry{{
			Name:   "p1",
			Limits: config.LimitsConfig{RPS: 1, Concurrency: 4, Priority: 100},
		}},
		Nodes: []config.NodeEntry{
			{Name: "n1", Provider: "p1", APIBase: "http://n1.test/v1", Model: "m"},
			{Name: "n2", Provider: "p1", APIBase: "http://n2.test/v1", Model: "m", Limits: config.LimitsConfig{Concurrency: 2, RPM: 30}},
		},
		Models: []config.ClientModelEntry{{Name: "client", Nodes: []string{"n1", "n2"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	n1 := router.Backends["n1"]
	if n1.Limits.RPS != 1 || n1.Limits.Concurrency != 4 || n1.Limits.Priority != 100 {
		t.Fatalf("n1 limits = %#v", n1.Limits)
	}
	n2 := router.Backends["n2"]
	if n2.Limits.RPS != 1 || n2.Limits.Concurrency != 2 || n2.Limits.RPM != 30 || n2.Limits.Priority != 100 {
		t.Fatalf("n2 limits = %#v", n2.Limits)
	}
}

func TestRouterForwardSkipsBackendWithUnsupportedEndpoint(t *testing.T) {
	var chatHits atomic.Int32
	chatServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		chatHits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"error":"chat only"}`))
	}))
	defer chatServer.Close()
	var embeddingsHits atomic.Int32
	embeddingsServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		embeddingsHits.Add(1)
		if r.URL.Path != "/v1/embeddings" {
			t.Errorf("path = %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"embedding":[1]}]}`))
	}))
	defer embeddingsServer.Close()

	router, err := NewRouter(&config.Config{
		Providers: []config.ProviderEntry{
			{Name: "chat-provider", APIBase: chatServer.URL + "/v1", Endpoints: []string{"chat"}},
			{Name: "embedding-provider", APIBase: embeddingsServer.URL + "/v1", Endpoints: []string{"embeddings"}},
		},
		Nodes: []config.NodeEntry{
			{Name: "chat-node", Provider: "chat-provider", Model: "chat-upstream"},
			{Name: "embedding-node", Provider: "embedding-provider", Model: "embedding-upstream"},
		},
		Models: []config.ClientModelEntry{{Name: "alpha", Nodes: []string{"chat-node", "embedding-node"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	resp, err := router.Forward(context.Background(), "alpha", "/v1/embeddings", "", []byte(`{"model":"alpha","input":"hi"}`), map[string]any{"model": "alpha", "input": "hi"}, false, http.Header{"Content-Type": {"application/json"}})
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if embeddingsHits.Load() != 1 || chatHits.Load() != 0 {
		t.Fatalf("hits chat=%d embeddings=%d", chatHits.Load(), embeddingsHits.Load())
	}
}

func TestRouterRoutesToGigaChatBackend(t *testing.T) {
	var authHits atomic.Int32
	var chatHits atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/oauth":
			authHits.Add(1)
			if r.Header.Get("Authorization") != "Basic encoded-key" {
				t.Errorf("auth header = %s", r.Header.Get("Authorization"))
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"access_token":"token-1","expires_at":4102444800000}`))
		case "/api/v1/chat/completions":
			chatHits.Add(1)
			if r.Header.Get("Authorization") != "Bearer token-1" {
				t.Errorf("chat auth = %s", r.Header.Get("Authorization"))
			}
			var got map[string]any
			if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
				t.Fatal(err)
			}
			if got["model"] != "GigaChat-Max" {
				t.Fatalf("model = %#v", got["model"])
			}
			messages := got["messages"].([]any)
			if messages[0].(map[string]any)["content"] != "Hi" {
				t.Fatalf("messages = %#v", messages)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"resp-1","model":"GigaChat-Max","choices":[{"message":{"content":"Hello from GigaChat"},"finish_reason":"stop"}]}`))
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer upstream.Close()

	router, err := NewRouter(&config.Config{
		ModelList: []config.ModelEntry{{
			ModelName: "GigaChat-Max",
			ModelParams: map[string]any{
				"api_type": "gigachat",
				"api_base": upstream.URL + "/api/v1",
				"api_key":  "encoded-key",
				"scope":    "GIGACHAT_API_CORP",
				"auth_url": upstream.URL + "/oauth",
				"model":    "GigaChat-Max",
			},
		}},
		RouterSettings: config.RouterSettings{NumRetries: 1},
	})
	if err != nil {
		t.Fatal(err)
	}
	resp, err := router.Forward(context.Background(), "GigaChat-Max", "/v1/chat/completions", "", []byte(`{"messages":[{"role":"user","content":"Hi"}]}`), map[string]any{"messages": []any{map[string]any{"role": "user", "content": "Hi"}}}, false, http.Header{"Content-Type": {"application/json"}})
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var data map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		t.Fatal(err)
	}
	choice := data["choices"].([]any)[0].(map[string]any)
	message := choice["message"].(map[string]any)
	if message["content"] != "Hello from GigaChat" || authHits.Load() != 1 || chatHits.Load() != 1 {
		t.Fatalf("data=%#v authHits=%d chatHits=%d", data, authHits.Load(), chatHits.Load())
	}
}

func TestRouterActiveHealthCheckUsesGigaChatToken(t *testing.T) {
	var authHits atomic.Int32
	var modelHits atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/oauth":
			authHits.Add(1)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"access_token":"token-1","expires_at":4102444800000}`))
		case "/api/v1/models":
			modelHits.Add(1)
			if r.Header.Get("Authorization") != "Bearer token-1" {
				t.Errorf("health auth = %s", r.Header.Get("Authorization"))
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"data":[]}`))
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer upstream.Close()

	router, err := NewRouter(&config.Config{
		ModelList: []config.ModelEntry{{
			ModelName: "GigaChat-Max",
			ModelParams: map[string]any{
				"api_type": "gigachat",
				"api_base": upstream.URL + "/api/v1",
				"api_key":  "encoded-key",
				"scope":    "GIGACHAT_API_CORP",
				"auth_url": upstream.URL + "/oauth",
				"model":    "GigaChat-Max",
			},
		}},
		RouterSettings: config.RouterSettings{HealthChecks: config.HealthCheckSettings{
			Enabled:          true,
			Path:             "/models",
			TimeoutSeconds:   1,
			FailureThreshold: 1,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	router.ActiveHealthCheckOnce(context.Background())
	health := router.HealthSnapshot()
	if len(health) != 1 || !health[0].Healthy || authHits.Load() != 1 || modelHits.Load() != 1 {
		t.Fatalf("health=%#v authHits=%d modelHits=%d", health, authHits.Load(), modelHits.Load())
	}
}

func TestRouterNodeInheritsGigaChatProviderSettings(t *testing.T) {
	router, err := NewRouter(&config.Config{
		Providers: []config.ProviderEntry{{
			Name:             "giga",
			APIBase:          "https://gigachat.example/api/v1",
			APIKey:           "encoded-key",
			APIType:          "gigachat",
			Scope:            "GIGACHAT_API_CORP",
			AuthURL:          "https://auth.example/oauth",
			EmulateToolCalls: true,
		}},
		Nodes: []config.NodeEntry{{
			Name:     "giga-node",
			Provider: "giga",
			Model:    "GigaChat-Max",
		}},
		Models: []config.ClientModelEntry{{Name: "client-giga", Nodes: []string{"giga-node"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	backend := router.Backends["giga-node"]
	if backend.APIType != "gigachat" || backend.GigaChat == nil {
		t.Fatalf("backend = %#v", backend)
	}
	if backend.GigaChat.Config.Scope != "GIGACHAT_API_CORP" || !backend.GigaChat.Config.EmulateToolCalls || backend.TargetModel != "GigaChat-Max" {
		t.Fatalf("gigachat config = %#v target=%s", backend.GigaChat.Config, backend.TargetModel)
	}
}

func TestRouterNodeEndpointsOverrideProviderEndpoints(t *testing.T) {
	router, err := NewRouter(&config.Config{
		Providers: []config.ProviderEntry{{Name: "provider", APIBase: "http://provider.test/v1", Endpoints: []string{"chat"}}},
		Nodes: []config.NodeEntry{{
			Name:      "embed-node",
			Provider:  "provider",
			Model:     "embedding-upstream",
			Endpoints: []string{"embeddings"},
		}},
		Models: []config.ClientModelEntry{{Name: "alpha", Nodes: []string{"embed-node"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	route, err := router.BuildRouteForPath("alpha", "/v1/embeddings", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	if len(route) != 1 || route[0].Name != "embed-node" {
		t.Fatalf("route = %#v", route)
	}
	_, err = router.BuildRouteForPath("alpha", "/v1/chat/completions", map[string]any{})
	if !errors.Is(err, ErrEndpointNotSupported) {
		t.Fatalf("err = %v", err)
	}
}

func TestRouterForwardSkipsNodeDisallowedForKey(t *testing.T) {
	var key1Hits atomic.Int32
	key1Server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key1Hits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"key1"}}]}`))
	}))
	defer key1Server.Close()
	var key2Hits atomic.Int32
	key2Server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key2Hits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"key2"}}]}`))
	}))
	defer key2Server.Close()

	router, err := NewRouter(&config.Config{
		Nodes: []config.NodeEntry{
			{Name: "node-key-1", APIBase: key1Server.URL + "/v1", Model: "upstream-key-1", AllowedKeys: []any{"key-1"}},
			{Name: "node-key-2", APIBase: key2Server.URL + "/v1", Model: "upstream-key-2", AllowedKeys: []any{"key-2"}},
		},
		Models: []config.ClientModelEntry{{Name: "alpha", Nodes: []string{"node-key-1", "node-key-2"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := router.ForwardWithOptions(context.Background(), "alpha", "/v1/chat/completions", "", []byte(`{"model":"alpha"}`), map[string]any{"model": "alpha"}, false, http.Header{"Content-Type": {"application/json"}}, ForwardOptions{KeyID: "key-2"})
	if err != nil {
		t.Fatal(err)
	}
	defer result.Response.Body.Close()
	if key1Hits.Load() != 0 || key2Hits.Load() != 1 {
		t.Fatalf("hits key1=%d key2=%d", key1Hits.Load(), key2Hits.Load())
	}
}

func TestRouterReturnsAccessDeniedWhenNoNodeAllowedForKey(t *testing.T) {
	router, err := NewRouter(&config.Config{
		Nodes: []config.NodeEntry{{
			Name:        "restricted",
			APIBase:     "http://restricted.test/v1",
			Model:       "upstream",
			AllowedKeys: []any{"key-1"},
		}},
		Models: []config.ClientModelEntry{{Name: "alpha", Nodes: []string{"restricted"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = router.BuildRouteForKey("alpha", "/v1/chat/completions", map[string]any{}, "key-2")
	if !errors.Is(err, ErrNodeAccessDenied) {
		t.Fatalf("err = %v", err)
	}
}

func TestRouterSkipsUnhealthyNodeWhenAlternativeExists(t *testing.T) {
	router, err := NewRouter(&config.Config{
		Nodes: []config.NodeEntry{
			{Name: "n1", APIBase: "http://n1.test/v1", Model: "m"},
			{Name: "n2", APIBase: "http://n2.test/v1", Model: "m"},
		},
		Models: []config.ClientModelEntry{{Name: "client", Nodes: []string{"n1", "n2"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	router.FailureThreshold = 1
	router.recordBackendFailure("n1", http.StatusServiceUnavailable, "down")
	route, err := router.BuildRoute("client", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	if len(route) != 1 || route[0].Name != "n2" {
		t.Fatalf("route = %#v", route)
	}
	health := router.HealthSnapshot()
	if len(health) != 2 || health[0].Name != "n1" || health[0].Healthy {
		t.Fatalf("health = %#v", health)
	}
}

func TestRouterSkipsUnavailableSiblingNodeWhenAlternativeExists(t *testing.T) {
	router, err := NewRouter(&config.Config{
		Nodes: []config.NodeEntry{
			{Name: "n1", APIBase: "http://n1.test/v1", Model: "m"},
			{Name: "n2", APIBase: "http://n2.test/v1", Model: "m"},
		},
		Models: []config.ClientModelEntry{{Name: "client", Nodes: []string{"n1", "n2"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	route, err := router.BuildRouteForKeyWithOptions("client", "/v1/chat/completions", map[string]any{}, "", RouteOptions{
		BackendAvailable: func(backend Backend) bool {
			return backend.Name != "n1"
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(route) != 1 || route[0].Name != "n2" {
		t.Fatalf("route = %#v", route)
	}
}

func TestRouterKeepsCandidateWhenAllSiblingNodesUnavailable(t *testing.T) {
	router, err := NewRouter(&config.Config{
		Nodes: []config.NodeEntry{
			{Name: "n1", APIBase: "http://n1.test/v1", Model: "m"},
			{Name: "n2", APIBase: "http://n2.test/v1", Model: "m"},
		},
		Models: []config.ClientModelEntry{{Name: "client", Nodes: []string{"n1", "n2"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	route, err := router.BuildRouteForKeyWithOptions("client", "/v1/chat/completions", map[string]any{}, "", RouteOptions{
		BackendAvailable: func(Backend) bool { return false },
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(route) != 2 || route[0].Name != "n1" {
		t.Fatalf("route = %#v", route)
	}
}

func TestRouterBypassesUnavailableStickyNodeWithoutRebinding(t *testing.T) {
	router, err := NewRouter(&config.Config{
		Nodes: []config.NodeEntry{
			{Name: "n1", APIBase: "http://n1.test/v1", Model: "m"},
			{Name: "n2", APIBase: "http://n2.test/v1", Model: "m"},
		},
		Models: []config.ClientModelEntry{{Name: "client", Nodes: []string{"n1", "n2"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	payload := map[string]any{"conversation_id": "abc"}
	router.BindSticky("conversation_id", "abc", "n1")
	route, err := router.BuildRouteForKeyWithOptions("client", "/v1/chat/completions", payload, "", RouteOptions{
		BackendAvailable: func(backend Backend) bool {
			return backend.Name != "n1"
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(route) != 1 || route[0].Name != "n2" {
		t.Fatalf("route = %#v", route)
	}
	route, err = router.BuildRoute("client", payload)
	if err != nil {
		t.Fatal(err)
	}
	if len(route) != 2 || route[0].Name != "n1" {
		t.Fatalf("sticky route was rebound: %#v", route)
	}
}

func TestRouterQuotaAvailabilitySkipsFallbackPrimary(t *testing.T) {
	router, err := NewRouter(&config.Config{
		ModelList: []config.ModelEntry{
			{ModelName: "alpha", ModelParams: map[string]any{"api_base": "http://alpha.test/v1"}},
			{ModelName: "beta", ModelParams: map[string]any{"api_base": "http://beta.test/v1"}},
		},
		RouterSettings: config.RouterSettings{Fallbacks: []map[string]any{{"alpha": []any{"beta"}}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	route, err := router.BuildRouteForKeyWithOptions("alpha", "/v1/chat/completions", map[string]any{}, "", RouteOptions{
		QuotaAvailability: func(backend Backend) bool {
			return backend.Name != "alpha"
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(route) != 1 || route[0].Name != "beta" {
		t.Fatalf("route = %#v", route)
	}
}

func TestRouterQuotaAvailabilityKeepsFullRouteWhenAllExhausted(t *testing.T) {
	router, err := NewRouter(&config.Config{
		ModelList: []config.ModelEntry{
			{ModelName: "alpha", ModelParams: map[string]any{"api_base": "http://alpha.test/v1"}},
			{ModelName: "beta", ModelParams: map[string]any{"api_base": "http://beta.test/v1"}},
		},
		RouterSettings: config.RouterSettings{Fallbacks: []map[string]any{{"alpha": []any{"beta"}}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	route, err := router.BuildRouteForKeyWithOptions("alpha", "/v1/chat/completions", map[string]any{}, "", RouteOptions{
		QuotaAvailability: func(Backend) bool { return false },
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(route) != 2 || route[0].Name != "alpha" || route[1].Name != "beta" {
		t.Fatalf("route = %#v", route)
	}
}

func TestRouterWeightedRoundRobinPrefersQuotaAvailableNodes(t *testing.T) {
	router, err := NewRouter(&config.Config{
		Nodes: []config.NodeEntry{
			{Name: "n1", APIBase: "http://n1.test/v1", Model: "m", Weight: 5},
			{Name: "n2", APIBase: "http://n2.test/v1", Model: "m", Weight: 1},
		},
		Models: []config.ClientModelEntry{{Name: "client", Nodes: []string{"n1", "n2"}, Balancing: "weighted_round_robin"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 4; i++ {
		route, err := router.BuildRouteForKeyWithOptions("client", "/v1/chat/completions", map[string]any{}, "", RouteOptions{
			QuotaAvailability: func(backend Backend) bool {
				return backend.Name == "n2"
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		if len(route) != 1 || route[0].Name != "n2" {
			t.Fatalf("route %d = %#v", i, route)
		}
	}
}

func TestRouterNilQuotaAvailabilityPreservesRoute(t *testing.T) {
	router, err := NewRouter(&config.Config{
		ModelList: []config.ModelEntry{
			{ModelName: "alpha", ModelParams: map[string]any{"api_base": "http://alpha.test/v1"}},
			{ModelName: "beta", ModelParams: map[string]any{"api_base": "http://beta.test/v1"}},
		},
		RouterSettings: config.RouterSettings{Fallbacks: []map[string]any{{"alpha": []any{"beta"}}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	route, err := router.BuildRouteForKeyWithOptions("alpha", "/v1/chat/completions", map[string]any{}, "", RouteOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(route) != 2 || route[0].Name != "alpha" || route[1].Name != "beta" {
		t.Fatalf("route = %#v", route)
	}
}

func TestRouterHealthCooldownRestoresBackend(t *testing.T) {
	router, err := NewRouter(&config.Config{ModelList: []config.ModelEntry{
		{ModelName: "alpha", ModelParams: map[string]any{"api_base": "http://alpha.test/v1"}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	router.FailureThreshold = 1
	router.UnhealthyCooldown = time.Nanosecond
	router.recordBackendFailure("alpha", http.StatusServiceUnavailable, "down")
	time.Sleep(time.Millisecond)
	health := router.HealthSnapshot()
	if len(health) != 1 || !health[0].Healthy || health[0].ConsecutiveFailures != 0 {
		t.Fatalf("health did not recover: %#v", health)
	}
	if health[0].State != "" || health[0].RecoverySuccessThreshold != 0 || health[0].RecoverySuccesses != 0 {
		t.Fatalf("default health exposed recovery state: %#v", health[0])
	}
}

func TestRouterHalfOpenTransitionSkipsWhenAlternativeExists(t *testing.T) {
	router, err := NewRouter(&config.Config{
		Nodes: []config.NodeEntry{
			{Name: "n1", APIBase: "http://n1.test/v1", Model: "m"},
			{Name: "n2", APIBase: "http://n2.test/v1", Model: "m"},
		},
		Models: []config.ClientModelEntry{{Name: "client", Nodes: []string{"n1", "n2"}}},
		RouterSettings: config.RouterSettings{HealthChecks: config.HealthCheckSettings{
			FailureThreshold:         1,
			UnhealthyCooldownSeconds: 0.001,
			RecoverySuccessThreshold: 2,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	router.recordBackendFailure("n1", http.StatusServiceUnavailable, "down")
	time.Sleep(time.Millisecond)

	route, err := router.BuildRoute("client", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	if len(route) != 1 || route[0].Name != "n2" {
		t.Fatalf("route = %#v", route)
	}
	health := router.HealthSnapshot()
	if len(health) != 2 || health[0].Name != "n1" || health[0].State != "half_open" || health[0].Healthy {
		t.Fatalf("health = %#v", health)
	}
}

func TestRouterHalfOpenAdmittedAsLastResort(t *testing.T) {
	router, err := NewRouter(&config.Config{ModelList: []config.ModelEntry{
		{ModelName: "alpha", ModelParams: map[string]any{"api_base": "http://alpha.test/v1"}},
	}, RouterSettings: config.RouterSettings{HealthChecks: config.HealthCheckSettings{
		FailureThreshold:         1,
		UnhealthyCooldownSeconds: 0.001,
		RecoverySuccessThreshold: 1,
	}}})
	if err != nil {
		t.Fatal(err)
	}
	router.recordBackendFailure("alpha", http.StatusServiceUnavailable, "down")
	time.Sleep(time.Millisecond)

	route, err := router.BuildRoute("alpha", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	if len(route) != 1 || route[0].Name != "alpha" {
		t.Fatalf("route = %#v", route)
	}
	health := router.HealthSnapshot()
	if len(health) != 1 || health[0].State != "half_open" || health[0].Healthy {
		t.Fatalf("health = %#v", health)
	}
}

func TestRouterHalfOpenRequiresConsecutiveSuccesses(t *testing.T) {
	router, err := NewRouter(&config.Config{ModelList: []config.ModelEntry{
		{ModelName: "alpha", ModelParams: map[string]any{"api_base": "http://alpha.test/v1"}},
	}, RouterSettings: config.RouterSettings{HealthChecks: config.HealthCheckSettings{
		FailureThreshold:         1,
		UnhealthyCooldownSeconds: 0.001,
		RecoverySuccessThreshold: 3,
	}}})
	if err != nil {
		t.Fatal(err)
	}
	router.recordBackendFailure("alpha", http.StatusServiceUnavailable, "down")
	time.Sleep(time.Millisecond)
	if health := router.HealthSnapshot(); len(health) != 1 || health[0].State != "half_open" {
		t.Fatalf("health after cooldown = %#v", health)
	}

	router.recordBackendSuccess("alpha", http.StatusOK)
	router.recordBackendSuccess("alpha", http.StatusOK)
	health := router.HealthSnapshot()
	if len(health) != 1 || health[0].State != "half_open" || health[0].Healthy || health[0].RecoverySuccesses != 2 {
		t.Fatalf("health after partial recovery = %#v", health)
	}

	router.recordBackendSuccess("alpha", http.StatusOK)
	health = router.HealthSnapshot()
	if len(health) != 1 || !health[0].Healthy || health[0].State != "healthy" || health[0].RecoverySuccesses != 0 || health[0].ConsecutiveFailures != 0 {
		t.Fatalf("health after recovery = %#v", health)
	}
}

func TestRouterHalfOpenFailureReclosesCooldown(t *testing.T) {
	router, err := NewRouter(&config.Config{ModelList: []config.ModelEntry{
		{ModelName: "alpha", ModelParams: map[string]any{"api_base": "http://alpha.test/v1"}},
	}, RouterSettings: config.RouterSettings{HealthChecks: config.HealthCheckSettings{
		FailureThreshold:         1,
		UnhealthyCooldownSeconds: 30,
		RecoverySuccessThreshold: 2,
	}}})
	if err != nil {
		t.Fatal(err)
	}
	router.recordBackendFailure("alpha", http.StatusServiceUnavailable, "down")
	router.mu.Lock()
	health := router.Health["alpha"]
	health.UnhealthyUntil = time.Now().Add(-time.Millisecond)
	router.Health["alpha"] = health
	router.mu.Unlock()
	router.recordBackendSuccess("alpha", http.StatusOK)
	router.recordBackendFailure("alpha", http.StatusServiceUnavailable, "still down")

	snapshot := router.HealthSnapshot()
	if len(snapshot) != 1 || snapshot[0].Healthy || snapshot[0].State != "unhealthy" || snapshot[0].RecoverySuccesses != 0 || !snapshot[0].UnhealthyUntil.After(time.Now()) {
		t.Fatalf("health after half-open failure = %#v", snapshot)
	}
}

func TestRouterActiveHealthCheckMarksUnhealthyAndRecovers(t *testing.T) {
	var status atomic.Int32
	status.Store(http.StatusServiceUnavailable)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/healthz" {
			t.Errorf("path = %s", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer upstream-key" {
			t.Errorf("auth = %s", r.Header.Get("Authorization"))
		}
		w.WriteHeader(int(status.Load()))
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer upstream.Close()

	router, err := NewRouter(&config.Config{
		ModelList: []config.ModelEntry{{
			ModelName: "alpha",
			ModelParams: map[string]any{
				"api_base": upstream.URL + "/v1",
				"api_key":  "upstream-key",
			},
		}},
		RouterSettings: config.RouterSettings{HealthChecks: config.HealthCheckSettings{
			Enabled:                  true,
			Path:                     "/healthz",
			TimeoutSeconds:           1,
			FailureThreshold:         1,
			UnhealthyCooldownSeconds: 60,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !router.ActiveHealthChecks || router.FailureThreshold != 1 || router.UnhealthyCooldown != time.Minute {
		t.Fatalf("health settings not applied: enabled=%v threshold=%d cooldown=%s", router.ActiveHealthChecks, router.FailureThreshold, router.UnhealthyCooldown)
	}
	router.ActiveHealthCheckOnce(context.Background())
	health := router.HealthSnapshot()
	if len(health) != 1 || health[0].Healthy || health[0].LastStatus != http.StatusServiceUnavailable {
		t.Fatalf("health after failure = %#v", health)
	}

	status.Store(http.StatusOK)
	router.ActiveHealthCheckOnce(context.Background())
	health = router.HealthSnapshot()
	if len(health) != 1 || !health[0].Healthy || health[0].ConsecutiveFailures != 0 || health[0].LastStatus != http.StatusOK {
		t.Fatalf("health after recovery = %#v", health)
	}
}

func TestRouterActiveHealthCheckRecoversHalfOpenBackend(t *testing.T) {
	var status atomic.Int32
	status.Store(http.StatusServiceUnavailable)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(int(status.Load()))
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer upstream.Close()

	router, err := NewRouter(&config.Config{
		ModelList: []config.ModelEntry{{
			ModelName:   "alpha",
			ModelParams: map[string]any{"api_base": upstream.URL + "/v1"},
		}},
		RouterSettings: config.RouterSettings{HealthChecks: config.HealthCheckSettings{
			Enabled:                  true,
			Path:                     "/healthz",
			TimeoutSeconds:           1,
			FailureThreshold:         1,
			UnhealthyCooldownSeconds: 0.001,
			RecoverySuccessThreshold: 1,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	router.ActiveHealthCheckOnce(context.Background())
	time.Sleep(time.Millisecond)
	if health := router.HealthSnapshot(); len(health) != 1 || health[0].State != "half_open" {
		t.Fatalf("health after cooldown = %#v", health)
	}

	status.Store(http.StatusOK)
	router.ActiveHealthCheckOnce(context.Background())
	health := router.HealthSnapshot()
	if len(health) != 1 || !health[0].Healthy || health[0].State != "healthy" || health[0].LastStatus != http.StatusOK {
		t.Fatalf("health after probe recovery = %#v", health)
	}
}

func TestRouterActiveHealthCheckRecoveryUsesTrialBudget(t *testing.T) {
	var status atomic.Int32
	status.Store(http.StatusServiceUnavailable)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(int(status.Load()))
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer upstream.Close()

	router, err := NewRouter(&config.Config{
		ModelList: []config.ModelEntry{{
			ModelName:   "alpha",
			ModelParams: map[string]any{"api_base": upstream.URL + "/v1"},
		}},
		RouterSettings: config.RouterSettings{HealthChecks: config.HealthCheckSettings{
			Enabled:                  true,
			Path:                     "/healthz",
			TimeoutSeconds:           1,
			FailureThreshold:         1,
			UnhealthyCooldownSeconds: 30,
			RecoverySuccessThreshold: 2,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	router.ActiveHealthCheckOnce(context.Background())
	status.Store(http.StatusOK)
	router.ActiveHealthCheckOnce(context.Background())
	health := router.HealthSnapshot()
	if len(health) != 1 || health[0].Healthy || health[0].State != "unhealthy" || health[0].RecoverySuccesses != 1 {
		t.Fatalf("health after first probe success = %#v", health)
	}

	router.ActiveHealthCheckOnce(context.Background())
	health = router.HealthSnapshot()
	if len(health) != 1 || !health[0].Healthy || health[0].State != "healthy" || health[0].RecoverySuccesses != 0 {
		t.Fatalf("health after second probe success = %#v", health)
	}
}

func TestRouterHealthScoringAliasConfig(t *testing.T) {
	router, err := NewRouter(&config.Config{
		ModelList: []config.ModelEntry{{
			ModelName:   "alpha",
			ModelParams: map[string]any{"api_base": "http://alpha.test/v1"},
		}},
		RouterSettings: config.RouterSettings{HealthScoring: config.HealthCheckSettings{
			Enabled:              true,
			ProbeIntervalSeconds: 3,
			CooldownSeconds:      7,
			FailureThreshold:     4,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !router.ActiveHealthChecks || router.HealthCheckInterval != 3*time.Second || router.UnhealthyCooldown != 7*time.Second || router.FailureThreshold != 4 {
		t.Fatalf("health scoring alias not applied: enabled=%v interval=%s cooldown=%s threshold=%d", router.ActiveHealthChecks, router.HealthCheckInterval, router.UnhealthyCooldown, router.FailureThreshold)
	}
}

func TestRouterHealthScoreUpdatesFromSuccessAndFailure(t *testing.T) {
	router, err := NewRouter(&config.Config{
		ModelList: []config.ModelEntry{{
			ModelName:   "alpha",
			ModelParams: map[string]any{"api_base": "http://alpha.test/v1"},
		}},
		RouterSettings: config.RouterSettings{HealthScoring: config.HealthCheckSettings{
			Enabled: true,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	router.recordBackendSuccess("alpha", http.StatusOK)
	router.recordBackendSuccess("alpha", http.StatusOK)
	router.recordBackendFailure("alpha", http.StatusServiceUnavailable, "down")

	health := router.HealthSnapshot()
	if len(health) != 1 {
		t.Fatalf("health = %#v", health)
	}
	if health[0].SuccessCount != 2 || health[0].FailureCount != 1 {
		t.Fatalf("counters = %#v", health[0])
	}
	if health[0].Score < 0.66 || health[0].Score > 0.67 {
		t.Fatalf("score = %f", health[0].Score)
	}
}

func TestRouterHealthScoringReordersFallbacks(t *testing.T) {
	router, err := NewRouter(&config.Config{
		ModelList: []config.ModelEntry{
			{ModelName: "alpha", ModelParams: map[string]any{"api_base": "http://alpha.test/v1"}},
			{ModelName: "beta", ModelParams: map[string]any{"api_base": "http://beta.test/v1"}},
			{ModelName: "gamma", ModelParams: map[string]any{"api_base": "http://gamma.test/v1"}},
		},
		RouterSettings: config.RouterSettings{
			Fallbacks: []map[string]any{{"alpha": []any{"beta", "gamma"}}},
			HealthScoring: config.HealthCheckSettings{
				Enabled:          true,
				FailureThreshold: 10,
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	router.recordBackendSuccess("alpha", http.StatusOK)
	router.recordBackendFailure("alpha", http.StatusServiceUnavailable, "primary degraded")
	router.recordBackendFailure("alpha", http.StatusServiceUnavailable, "primary degraded")
	router.recordBackendSuccess("beta", http.StatusOK)
	router.recordBackendFailure("beta", http.StatusServiceUnavailable, "fallback degraded")
	router.recordBackendSuccess("gamma", http.StatusOK)
	router.recordBackendSuccess("gamma", http.StatusOK)

	route, err := router.BuildRoute("alpha", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	if len(route) != 3 || route[0].Name != "gamma" || route[1].Name != "beta" || route[2].Name != "alpha" {
		t.Fatalf("route = %#v", route)
	}
}

func TestRouterHealthScoringThresholdSkipsLowScoreWhenAlternativeExists(t *testing.T) {
	router, err := NewRouter(&config.Config{
		ModelList: []config.ModelEntry{
			{ModelName: "alpha", ModelParams: map[string]any{"api_base": "http://alpha.test/v1"}},
			{ModelName: "beta", ModelParams: map[string]any{"api_base": "http://beta.test/v1"}},
		},
		RouterSettings: config.RouterSettings{
			Fallbacks: []map[string]any{{"alpha": []any{"beta"}}},
			HealthScoring: config.HealthCheckSettings{
				Enabled:            true,
				FailureThreshold:   10,
				UnhealthyThreshold: 0.5,
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	router.recordBackendSuccess("alpha", http.StatusOK)
	router.recordBackendFailure("alpha", http.StatusServiceUnavailable, "degraded")
	router.recordBackendFailure("alpha", http.StatusServiceUnavailable, "degraded")
	router.recordBackendSuccess("beta", http.StatusOK)

	route, err := router.BuildRoute("alpha", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	if len(route) != 1 || route[0].Name != "beta" {
		t.Fatalf("route = %#v", route)
	}
}

func TestBuildResponsePipelineDerivesParseTagsFromTemplatePath(t *testing.T) {
	templatePath := filepath.Join(t.TempDir(), "template.jinja")
	if err := os.WriteFile(templatePath, []byte(`{{ '<analysis>' + reasoning_content + '</analysis>' }} <function_call>x</function_call>`), 0o600); err != nil {
		t.Fatal(err)
	}
	pipeline := BuildResponsePipelineFromConfig(map[string]any{
		"enabled":  true,
		"response": []any{"parse_template"},
		"parse_template": map[string]any{
			"template_path":                 templatePath,
			"parse_thinking":                true,
			"parse_tool_calls":              false,
			"start_in_think":                false,
			"max_stream_buffer_age_seconds": 1.5,
			"max_stream_buffer_bytes":       128,
		},
	}, nil, false)
	if len(pipeline.Modules) != 1 {
		t.Fatalf("modules = %#v", pipeline.Modules)
	}
	parser, ok := pipeline.Modules[0].(*modules.ParseTags)
	if !ok {
		t.Fatalf("module type = %T", pipeline.Modules[0])
	}
	if parser.Config.MaxStreamBufferAge != 1500*time.Millisecond || parser.Config.MaxStreamBufferBytes != 128 {
		t.Fatalf("parser stream buffer config = %#v", parser.Config)
	}
	out := pipeline.TransformResponseBody(
		[]byte(`{"choices":[{"message":{"content":"<analysis>Reason</analysis>Answer"}}]}`),
		"application/json",
		modules.Context{Path: "/v1/chat/completions"},
	)
	if out == nil {
		t.Fatal("expected transformed response")
	}
	if !strings.Contains(string(out), `"reasoning_content":"Reason"`) || !strings.Contains(string(out), `"content":"Answer"`) {
		t.Fatalf("response = %s", out)
	}
}

func TestBuildResponsePipelineDerivesQwenXMLToolParserFromTemplatePath(t *testing.T) {
	templatePath := filepath.Join(t.TempDir(), "qwen.jinja")
	if err := os.WriteFile(templatePath, []byte(`{{ '<tool_call>\n<function=' + tool_call.name + '>\n<parameter=' + args_name + '>\nvalue\n</parameter>\n</function>\n</tool_call>' }}`), 0o600); err != nil {
		t.Fatal(err)
	}
	pipeline := BuildResponsePipelineFromConfig(map[string]any{
		"enabled":  true,
		"response": []any{"parse_template"},
		"parse_template": map[string]any{
			"template_path":     templatePath,
			"parse_thinking":    false,
			"parse_tool_calls":  true,
			"stream_tool_calls": false,
		},
	}, nil, false)
	out := pipeline.TransformResponseBody(
		[]byte(`{"choices":[{"message":{"content":"<tool_call>\n<function=search>\n<parameter=query>\ncats\n</parameter>\n</function>\n</tool_call>"}}]}`),
		"application/json",
		modules.Context{Path: "/v1/chat/completions"},
	)
	if out == nil {
		t.Fatal("expected transformed response")
	}
	var payload map[string]any
	if err := json.Unmarshal(out, &payload); err != nil {
		t.Fatal(err)
	}
	msg := payload["choices"].([]any)[0].(map[string]any)["message"].(map[string]any)
	calls := msg["tool_calls"].([]any)
	fn := calls[0].(map[string]any)["function"].(map[string]any)
	if fn["name"] != "search" || fn["arguments"] != `{"query":"cats"}` || msg["content"] != nil {
		t.Fatalf("message = %#v", msg)
	}
}

func TestBuildResponsePipelineStreamJSONRepairRequiresInnerEnable(t *testing.T) {
	disabled := BuildResponsePipelineFromConfig(map[string]any{
		"enabled":  true,
		"response": []any{"stream_json_repair"},
	}, nil, false)
	if len(disabled.Modules) != 0 {
		t.Fatalf("disabled stream_json_repair modules = %#v", disabled.Modules)
	}

	explicitFalse := BuildResponsePipelineFromConfig(map[string]any{
		"enabled":  true,
		"response": []any{"stream_json_repair"},
		"stream_json_repair": map[string]any{
			"enabled": false,
		},
	}, nil, false)
	if len(explicitFalse.Modules) != 0 {
		t.Fatalf("explicit false stream_json_repair modules = %#v", explicitFalse.Modules)
	}

	notListed := BuildResponsePipelineFromConfig(map[string]any{
		"enabled":  true,
		"response": []any{"parse_tags"},
		"stream_json_repair": map[string]any{
			"enabled": true,
		},
	}, nil, false)
	if len(notListed.Modules) != 1 || notListed.Modules[0].Name() != "parse_tags" {
		t.Fatalf("not-listed stream_json_repair modules = %#v", notListed.Modules)
	}

	enabled := BuildResponsePipelineFromConfig(map[string]any{
		"enabled":  true,
		"response": []any{"stream_json_repair"},
		"stream_json_repair": map[string]any{
			"enabled":                       true,
			"max_stream_buffer_bytes":       64,
			"max_stream_buffer_age_seconds": "2.25",
			"on_failure":                    "empty_object",
		},
	}, nil, false)
	if len(enabled.Modules) != 1 || enabled.Modules[0].Name() != "stream_json_repair" {
		t.Fatalf("enabled stream_json_repair modules = %#v", enabled.Modules)
	}
	repair, ok := enabled.Modules[0].(*modules.StreamJSONRepair)
	if !ok {
		t.Fatalf("module type = %T", enabled.Modules[0])
	}
	if !repair.Config.Enabled || repair.Config.MaxStreamBufferBytes != 64 || repair.Config.MaxStreamBufferAge != 2250*time.Millisecond || repair.Config.OnFailure != "empty_object" {
		t.Fatalf("repair config = %#v", repair.Config)
	}
}

func TestStreamRewriteReadCloserFlushesPendingRepairOnEOF(t *testing.T) {
	pipeline := modules.Pipeline{Modules: []modules.ResponseModule{
		modules.NewStreamJSONRepair(modules.StreamJSONRepairConfig{Enabled: true}),
	}}
	body := `data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"query\":"}}]}}]}` + "\n\n"
	stream := &streamRewriteReadCloser{
		ReadCloser: io.NopCloser(strings.NewReader(body)),
		pipeline:   pipeline,
		state:      pipeline.NewStreamState(),
		ctx:        modules.Context{Path: "/v1/chat/completions", IsStream: true},
	}
	out, err := io.ReadAll(stream)
	if err != nil {
		t.Fatal(err)
	}
	args := collectRouterStreamToolArguments(t, string(out))
	if args != `{"query":` {
		t.Fatalf("arguments = %q body=%s", args, out)
	}
}

func TestStreamRewriteReadCloserCloseClosesPipelineState(t *testing.T) {
	var closes int32
	module := &routerLifecycleModule{closes: &closes}
	pipeline := modules.Pipeline{Modules: []modules.ResponseModule{module}}
	stream := &streamRewriteReadCloser{
		ReadCloser: io.NopCloser(strings.NewReader("")),
		pipeline:   pipeline,
		state:      pipeline.NewStreamState(),
		ctx:        modules.Context{Path: "/v1/chat/completions", IsStream: true},
	}
	if err := stream.Close(); err != nil {
		t.Fatal(err)
	}
	if got := atomic.LoadInt32(&closes); got != 1 {
		t.Fatalf("close calls = %d", got)
	}
	if err := stream.Close(); err != nil {
		t.Fatal(err)
	}
	if got := atomic.LoadInt32(&closes); got != 2 {
		t.Fatalf("second close calls = %d", got)
	}
}

func TestRouterForwardRetriesFallbackAndRewritesBody(t *testing.T) {
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "bad", http.StatusServiceUnavailable)
	}))
	defer primary.Close()
	var upstreamBody string
	fallback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		upstreamBody = string(body)
		if r.URL.Path != "/v1/chat/completions" {
			t.Errorf("path = %s", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer fallback-key" {
			t.Errorf("auth = %s", r.Header.Get("Authorization"))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}]}`))
	}))
	defer fallback.Close()

	router, err := NewRouter(&config.Config{
		ModelList: []config.ModelEntry{
			{ModelName: "alpha", ModelParams: map[string]any{"api_base": primary.URL + "/v1", "api_key": "primary-key", "model": "openai/upstream-a"}},
			{ModelName: "beta", ModelParams: map[string]any{"api_base": fallback.URL + "/v1", "api_key": "fallback-key", "model": "openai/upstream-b"}},
		},
		RouterSettings: config.RouterSettings{Fallbacks: []map[string]any{{"alpha": []any{"beta"}}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := router.ForwardWithResult(context.Background(), "alpha", "/v1/chat/completions", "", []byte(`{"model":"alpha"}`), map[string]any{"model": "alpha"}, false, http.Header{"Content-Type": {"application/json"}})
	if err != nil {
		t.Fatal(err)
	}
	resp := result.Response
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if !strings.Contains(upstreamBody, `"model":"upstream-b"`) {
		t.Fatalf("body was not rewritten for fallback: %s", upstreamBody)
	}
	if len(result.AttemptTrail) != 2 {
		t.Fatalf("attempt trail = %#v", result.AttemptTrail)
	}
	if got := result.AttemptTrail[0]; got.AttemptIndex != 1 || got.BackendName != "alpha" || got.RetryIndex != 0 || got.Status != http.StatusServiceUnavailable || !got.Retryable || got.Outcome != "retry" {
		t.Fatalf("primary attempt = %#v", got)
	}
	if got := result.AttemptTrail[1]; got.AttemptIndex != 2 || got.BackendName != "beta" || got.RetryIndex != 0 || got.Status != http.StatusOK || got.Retryable || got.Outcome != "final" {
		t.Fatalf("fallback attempt = %#v", got)
	}
}

func TestRouterForwardRecordsOTelAttemptSpans(t *testing.T) {
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "bad", http.StatusServiceUnavailable)
	}))
	defer primary.Close()
	fallback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}]}`))
	}))
	defer fallback.Close()

	router, err := NewRouter(&config.Config{
		ModelList: []config.ModelEntry{
			{ModelName: "alpha", ModelParams: map[string]any{"api_base": primary.URL + "/v1", "api_key": "primary-key"}},
			{ModelName: "beta", ModelParams: map[string]any{"api_base": fallback.URL + "/v1", "api_key": "fallback-key"}},
		},
		RouterSettings: config.RouterSettings{Fallbacks: []map[string]any{{"alpha": []any{"beta"}}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	provider, recorder := observability.NewInMemoryProvider("test-proxy")
	router.Observability = provider
	result, err := router.ForwardWithResult(context.Background(), "alpha", "/v1/chat/completions", "", []byte(`{"model":"alpha"}`), map[string]any{"model": "alpha"}, false, http.Header{"Content-Type": {"application/json"}})
	if err != nil {
		t.Fatal(err)
	}
	defer result.Response.Body.Close()
	if len(result.AttemptTrail) != 2 {
		t.Fatalf("attempt trail = %#v", result.AttemptTrail)
	}

	spans := routerSpansByName(recorder.Spans(), "proxy.upstream_attempt")
	if len(spans) != 2 {
		t.Fatalf("attempt spans = %d", len(spans))
	}
	first := routerSpanAttrs(spans[0])
	if first["backend_name"] != "alpha" || first["attempt_index"] != "1" || first["status_code"] != "503" || first["outcome"] != "retry" || first["error_type"] != "http_5xx" {
		t.Fatalf("first attempt attrs = %#v", first)
	}
	if first["gen_ai.system"] != "openai" || first["gen_ai.operation.name"] != "chat" || first["gen_ai.request.model"] != "alpha" {
		t.Fatalf("first attempt gen_ai attrs = %#v", first)
	}
	second := routerSpanAttrs(spans[1])
	if second["backend_name"] != "beta" || second["attempt_index"] != "2" || second["status_code"] != "200" || second["outcome"] != "final" {
		t.Fatalf("second attempt attrs = %#v", second)
	}
	if second["gen_ai.system"] != "openai" || second["gen_ai.operation.name"] != "chat" || second["gen_ai.request.model"] != "alpha" {
		t.Fatalf("second attempt gen_ai attrs = %#v", second)
	}
	for _, span := range spans {
		for _, secret := range []string{"primary-key", "fallback-key", primary.URL, fallback.URL} {
			for _, attr := range span.Attributes() {
				if strings.Contains(attr.Value.Emit(), secret) {
					t.Fatalf("span %s attr %s leaked %q: %s", span.Name(), attr.Key, secret, attr.Value.Emit())
				}
			}
		}
	}
}

func TestRouterForwardReturnsFinalRetryableResponse(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"error":"temporarily unavailable"}`))
	}))
	defer upstream.Close()

	router, err := NewRouter(&config.Config{ModelList: []config.ModelEntry{{
		ModelName:   "alpha",
		ModelParams: map[string]any{"api_base": upstream.URL + "/v1"},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	resp, err := router.Forward(context.Background(), "alpha", "/v1/chat/completions", "", []byte(`{"model":"alpha"}`), map[string]any{"model": "alpha"}, false, http.Header{"Content-Type": {"application/json"}})
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusServiceUnavailable || !strings.Contains(string(body), "temporarily unavailable") {
		t.Fatalf("status=%d body=%s", resp.StatusCode, string(body))
	}
}

func TestRouterForwardUsesBackendTimeoutByDefault(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(200 * time.Millisecond)
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"late"}}]}`))
	}))
	defer upstream.Close()

	router, err := NewRouter(&config.Config{ModelList: []config.ModelEntry{{
		ModelName: "alpha",
		ModelParams: map[string]any{
			"api_base":        upstream.URL + "/v1",
			"request_timeout": "50ms",
		},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	_, err = router.Forward(context.Background(), "alpha", "/v1/chat/completions", "", []byte(`{"model":"alpha"}`), map[string]any{"model": "alpha"}, false, http.Header{"Content-Type": {"application/json"}})
	if err == nil {
		t.Fatal("expected backend timeout error")
	}
	if elapsed := time.Since(start); elapsed > 500*time.Millisecond {
		t.Fatalf("backend timeout was not enforced quickly enough: %s", elapsed)
	}
}

func TestRouterForwardRecordsLimiterBlockedAttempt(t *testing.T) {
	var upstreamHits atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamHits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	router, err := NewRouter(&config.Config{ModelList: []config.ModelEntry{{
		ModelName: "alpha",
		ModelParams: map[string]any{
			"api_base": upstream.URL + "/v1",
			"api_key":  "provider-key",
		},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	blockErr := errors.New("Authorization: Bearer client-token api_key=provider-key https://upstream.test/v1/chat?client_secret=query-secret")
	result, err := router.ForwardWithOptions(
		context.Background(),
		"alpha",
		"/v1/chat/completions",
		"client_secret=query-secret",
		[]byte(`{"model":"alpha"}`),
		map[string]any{"model": "alpha"},
		false,
		http.Header{"Content-Type": {"application/json"}},
		ForwardOptions{BeforeAttempt: func(context.Context, Backend) (func(), error) {
			return nil, blockErr
		}},
	)
	if !errors.Is(err, blockErr) {
		t.Fatalf("err = %v", err)
	}
	if upstreamHits.Load() != 0 {
		t.Fatalf("upstream hits = %d", upstreamHits.Load())
	}
	if len(result.AttemptTrail) != 1 {
		t.Fatalf("attempt trail = %#v", result.AttemptTrail)
	}
	got := result.AttemptTrail[0]
	if got.AttemptIndex != 1 || got.BackendName != "alpha" || got.Outcome != "blocked" || got.Status != 0 || got.Retryable {
		t.Fatalf("blocked attempt = %#v", got)
	}
	for _, secret := range []string{"client-token", "provider-key", "query-secret", "client_secret=query-secret"} {
		if strings.Contains(got.Error, secret) {
			t.Fatalf("attempt error leaked %q: %q", secret, got.Error)
		}
	}
}

func TestRouterInflightReturnsToZeroForNonStreamSuccess(t *testing.T) {
	var router *Router
	seenInflight := make(chan int64, 1)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenInflight <- router.inflightCount("alpha")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}]}`))
	}))
	defer upstream.Close()

	var err error
	router, err = NewRouter(&config.Config{ModelList: []config.ModelEntry{{
		ModelName:   "alpha",
		ModelParams: map[string]any{"api_base": upstream.URL + "/v1"},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	resp, err := router.Forward(context.Background(), "alpha", "/v1/chat/completions", "", []byte(`{"model":"alpha"}`), map[string]any{"model": "alpha"}, false, http.Header{"Content-Type": {"application/json"}})
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if got := <-seenInflight; got != 1 {
		t.Fatalf("inflight during upstream attempt = %d, want 1", got)
	}
	if got := router.inflightCount("alpha"); got != 0 {
		t.Fatalf("inflight after non-stream success = %d", got)
	}
}

func TestRouterInflightReturnsToZeroForUpstreamError(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	baseURL := upstream.URL
	upstream.Close()

	router, err := NewRouter(&config.Config{ModelList: []config.ModelEntry{{
		ModelName:   "alpha",
		ModelParams: map[string]any{"api_base": baseURL + "/v1"},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = router.Forward(context.Background(), "alpha", "/v1/chat/completions", "", []byte(`{"model":"alpha"}`), map[string]any{"model": "alpha"}, false, http.Header{"Content-Type": {"application/json"}})
	if err == nil {
		t.Fatal("expected upstream error")
	}
	if got := router.inflightCount("alpha"); got != 0 {
		t.Fatalf("inflight after upstream error = %d", got)
	}
}

func TestRouterInflightReturnsToZeroForRetryFallback(t *testing.T) {
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "try fallback", http.StatusServiceUnavailable)
	}))
	defer primary.Close()
	fallback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}]}`))
	}))
	defer fallback.Close()

	router, err := NewRouter(&config.Config{
		ModelList: []config.ModelEntry{
			{ModelName: "alpha", ModelParams: map[string]any{"api_base": primary.URL + "/v1"}},
			{ModelName: "beta", ModelParams: map[string]any{"api_base": fallback.URL + "/v1"}},
		},
		RouterSettings: config.RouterSettings{Fallbacks: []map[string]any{{"alpha": []any{"beta"}}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := router.ForwardWithResult(context.Background(), "alpha", "/v1/chat/completions", "", []byte(`{"model":"alpha"}`), map[string]any{"model": "alpha"}, false, http.Header{"Content-Type": {"application/json"}})
	if err != nil {
		t.Fatal(err)
	}
	defer result.Response.Body.Close()
	if len(result.AttemptTrail) != 2 {
		t.Fatalf("attempt trail = %#v", result.AttemptTrail)
	}
	for _, name := range []string{"alpha", "beta"} {
		if got := router.inflightCount(name); got != 0 {
			t.Fatalf("inflight for %s after retry/fallback = %d", name, got)
		}
	}
}

func TestRouterForwardWithOptionsHoldsAttemptUntilStreamClosed(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer upstream.Close()

	router, err := NewRouter(&config.Config{ModelList: []config.ModelEntry{{
		ModelName:   "alpha",
		ModelParams: map[string]any{"api_base": upstream.URL + "/v1"},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	var active atomic.Int32
	result, err := router.ForwardWithOptions(
		context.Background(),
		"alpha",
		"/v1/chat/completions",
		"",
		[]byte(`{"model":"alpha","stream":true}`),
		map[string]any{"model": "alpha", "stream": true},
		true,
		http.Header{"Content-Type": {"application/json"}},
		ForwardOptions{BeforeAttempt: func(context.Context, Backend) (func(), error) {
			active.Add(1)
			return func() { active.Add(-1) }, nil
		}},
	)
	if err != nil {
		t.Fatal(err)
	}
	if active.Load() != 1 {
		t.Fatalf("active attempts after headers = %d", active.Load())
	}
	if got := router.inflightCount("alpha"); got != 1 {
		t.Fatalf("router inflight after stream headers = %d, want 1", got)
	}
	body, _ := io.ReadAll(result.Response.Body)
	_ = result.Response.Body.Close()
	if !strings.Contains(string(body), "data: [DONE]") || !strings.Contains(string(body), "content") {
		t.Fatalf("stream body changed: %s", string(body))
	}
	if len(result.AttemptTrail) != 1 {
		t.Fatalf("attempt trail = %#v", result.AttemptTrail)
	}
	if got := result.AttemptTrail[0]; got.BackendName != "alpha" || got.Status != http.StatusOK || got.Outcome != "final" {
		t.Fatalf("stream attempt = %#v", got)
	}
	if active.Load() != 0 {
		t.Fatalf("active attempts after body close = %d", active.Load())
	}
	if got := router.inflightCount("alpha"); got != 0 {
		t.Fatalf("router inflight after stream close = %d", got)
	}
}

func TestRouterInflightCancelledStreamCloseReleases(t *testing.T) {
	upstreamStarted := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		close(upstreamStarted)
		<-r.Context().Done()
	}))
	defer upstream.Close()

	router, err := NewRouter(&config.Config{ModelList: []config.ModelEntry{{
		ModelName:   "alpha",
		ModelParams: map[string]any{"api_base": upstream.URL + "/v1"},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	result, err := router.ForwardWithOptions(
		ctx,
		"alpha",
		"/v1/chat/completions",
		"",
		[]byte(`{"model":"alpha","stream":true}`),
		map[string]any{"model": "alpha", "stream": true},
		true,
		http.Header{"Content-Type": {"application/json"}},
		ForwardOptions{},
	)
	if err != nil {
		t.Fatal(err)
	}
	<-upstreamStarted
	if got := router.inflightCount("alpha"); got != 1 {
		t.Fatalf("router inflight after stream headers = %d, want 1", got)
	}
	cancel()
	_ = result.Response.Body.Close()
	if got := router.inflightCount("alpha"); got != 0 {
		t.Fatalf("router inflight after cancelled stream close = %d", got)
	}
}

func TestRouterInflightConcurrentTrackingIsRaceSafe(t *testing.T) {
	router := newConcurrencyAwareRouter(t)
	var wg sync.WaitGroup
	errCh := make(chan error, 1)
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				release := router.TrackBackendAttempt("n1")
				if _, err := router.BuildRoute("client", map[string]any{}); err != nil {
					select {
					case errCh <- err:
					default:
					}
				}
				release()
			}
		}()
	}
	wg.Wait()
	select {
	case err := <-errCh:
		t.Fatal(err)
	default:
	}
	if got := router.inflightCount("n1"); got != 0 {
		t.Fatalf("inflight after concurrent tracking = %d", got)
	}
}

func TestRouterStreamingParserSpanPreservesRewrittenBody(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"<think>hidden-secret</think>visible\"}}]}\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer upstream.Close()

	router, err := NewRouter(&config.Config{
		ModelList: []config.ModelEntry{{
			ModelName:   "alpha",
			ModelParams: map[string]any{"api_base": upstream.URL + "/v1"},
		}},
		ProxySettings: config.ProxySettings{Modules: map[string]any{
			"enabled":  true,
			"response": []any{"parse_tags"},
			"parse_tags": map[string]any{
				"parse_thinking": true,
			},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	provider, recorder := observability.NewInMemoryProvider("test-proxy")
	router.Observability = provider
	result, err := router.ForwardWithOptions(
		context.Background(),
		"alpha",
		"/v1/chat/completions",
		"",
		[]byte(`{"model":"alpha","stream":true}`),
		map[string]any{"model": "alpha", "stream": true},
		true,
		http.Header{"Content-Type": {"application/json"}},
		ForwardOptions{EndpointKind: "chat"},
	)
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(result.Response.Body)
	if err != nil {
		t.Fatal(err)
	}
	_ = result.Response.Body.Close()
	if strings.Contains(string(body), "<think>") || !strings.Contains(string(body), "reasoning_content") || !strings.Contains(string(body), "visible") {
		t.Fatalf("unexpected rewritten stream body: %s", string(body))
	}
	spans := routerSpansByName(recorder.Spans(), "proxy.parser")
	if len(spans) != 1 {
		t.Fatalf("parser spans = %d, want 1", len(spans))
	}
	attrs := routerSpanAttrs(spans[0])
	if attrs["endpoint_kind"] != "chat" || attrs["model"] != "alpha" || attrs["stream"] != "true" || attrs["backend_name"] != "alpha" || attrs["status_code"] != "200" {
		t.Fatalf("parser attrs = %#v", attrs)
	}
	for _, secret := range []string{"hidden-secret", upstream.URL, "Authorization"} {
		for _, attr := range spans[0].Attributes() {
			if strings.Contains(attr.Value.Emit(), secret) {
				t.Fatalf("parser span leaked %q in %s=%s", secret, attr.Key, attr.Value.Emit())
			}
		}
	}
}

func collectRouterStreamToolArguments(t *testing.T, body string) string {
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

type routerLifecycleModule struct {
	closes *int32
}

func (m *routerLifecycleModule) Name() string { return "router_lifecycle" }

func (m *routerLifecycleModule) TransformPayload(map[string]any, modules.Context) bool {
	return false
}

func (m *routerLifecycleModule) TransformStreamPayload(map[string]any, modules.Context) bool {
	return false
}

func (m *routerLifecycleModule) NewStreamState() any {
	return struct{}{}
}

func (m *routerLifecycleModule) TransformStreamPayloadWithState(map[string]any, modules.Context, any) bool {
	return false
}

func (m *routerLifecycleModule) CloseStreamState(any) {
	atomic.AddInt32(m.closes, 1)
}

func routerSpansByName(spans []sdktrace.ReadOnlySpan, name string) []sdktrace.ReadOnlySpan {
	out := []sdktrace.ReadOnlySpan{}
	for _, span := range spans {
		if span.Name() == name {
			out = append(out, span)
		}
	}
	return out
}

func routerSpanAttrs(span sdktrace.ReadOnlySpan) map[string]string {
	out := map[string]string{}
	if span == nil {
		return out
	}
	for _, attr := range span.Attributes() {
		out[string(attr.Key)] = attr.Value.Emit()
	}
	return out
}

func prefixAffinityMessages(content ...string) []any {
	out := make([]any, 0, len(content))
	for _, c := range content {
		out = append(out, map[string]any{"role": "user", "content": c})
	}
	return out
}

func newPrefixAffinityRouter(t *testing.T, maxEntries int) *Router {
	t.Helper()
	router, err := NewRouter(&config.Config{
		Nodes: []config.NodeEntry{
			{Name: "n1", APIBase: "http://n1.test/v1", Model: "m"},
			{Name: "n2", APIBase: "http://n2.test/v1", Model: "m"},
		},
		Models:         []config.ClientModelEntry{{Name: "client", Nodes: []string{"n1", "n2"}}},
		RouterSettings: config.RouterSettings{PrefixCacheAffinity: config.PrefixCacheAffinitySettings{Enabled: true, MaxEntries: maxEntries}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return router
}

func newConcurrencyAwareRouter(t *testing.T) *Router {
	t.Helper()
	router, err := NewRouter(&config.Config{
		Nodes: []config.NodeEntry{
			{Name: "n1", APIBase: "http://n1.test/v1", Model: "m"},
			{Name: "n2", APIBase: "http://n2.test/v1", Model: "m"},
		},
		Models: []config.ClientModelEntry{{Name: "client", Nodes: []string{"n1", "n2"}}},
		RouterSettings: config.RouterSettings{
			ConcurrencyAwareSelection: config.ConcurrencyAwareSelectionSettings{Enabled: true},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return router
}

func TestRouterPrefixCacheAffinityCanBeDisabled(t *testing.T) {
	router, err := NewRouter(&config.Config{
		RouterSettings: config.RouterSettings{PrefixCacheAffinity: config.PrefixCacheAffinitySettings{Enabled: false}},
		Nodes: []config.NodeEntry{
			{Name: "n1", APIBase: "http://n1.test/v1", Model: "m"},
			{Name: "n2", APIBase: "http://n2.test/v1", Model: "m"},
		},
		Models: []config.ClientModelEntry{{Name: "client", Nodes: []string{"n1", "n2"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if router.PrefixCacheAffinity || router.prefixCache != nil {
		t.Fatalf("prefix cache affinity should honor explicit disable: enabled=%v tracker=%v", router.PrefixCacheAffinity, router.prefixCache)
	}
	payload := map[string]any{"messages": prefixAffinityMessages("x")}
	router.recordPrefixAffinity("n1", payload)
	if router.prefixCache != nil {
		t.Fatalf("disabled router must not allocate a tracker")
	}
	r1, err := router.BuildRoute("client", payload)
	if err != nil {
		t.Fatal(err)
	}
	r2, err := router.BuildRoute("client", payload)
	if err != nil {
		t.Fatal(err)
	}
	if r1[0].Name != "n1" || r2[0].Name != "n2" {
		t.Fatalf("round robin changed when disabled: %s then %s", r1[0].Name, r2[0].Name)
	}
}

func TestRouterPrefixCacheAffinityPrefersMatchingNode(t *testing.T) {
	router := newPrefixAffinityRouter(t, 0)
	payload := map[string]any{"messages": prefixAffinityMessages("hello", "world")}
	router.recordPrefixAffinity("n2", payload)
	route, err := router.BuildRoute("client", payload)
	if err != nil {
		t.Fatal(err)
	}
	if route[0].Name != "n2" {
		t.Fatalf("route = %s, want n2 (prefix affinity)", route[0].Name)
	}
}

func TestRouterCombinedGatesPreferStickyThenQuotaPrefixThenHealthFallback(t *testing.T) {
	router, err := NewRouter(&config.Config{
		Nodes: []config.NodeEntry{
			{Name: "n1", APIBase: "http://n1.test/v1", Model: "m", Weight: 3},
			{Name: "n2", APIBase: "http://n2.test/v1", Model: "m", Weight: 1},
			{Name: "n3", APIBase: "http://n3.test/v1", Model: "m", Weight: 1},
		},
		Models: []config.ClientModelEntry{{
			Name:      "client",
			Nodes:     []string{"n1", "n2", "n3"},
			Balancing: "weighted_round_robin",
		}},
		RouterSettings: config.RouterSettings{
			HealthChecks:        config.HealthCheckSettings{RecoverySuccessThreshold: 1},
			PrefixCacheAffinity: config.PrefixCacheAffinitySettings{Enabled: true},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	payload := map[string]any{
		"conversation_id": "combined",
		"messages":        prefixAffinityMessages("shared-prefix"),
	}
	router.BindSticky("conversation_id", "combined", "n1")
	router.recordPrefixAffinity("n2", payload)

	route, err := router.BuildRoute("client", payload)
	if err != nil {
		t.Fatal(err)
	}
	if len(route) != 3 || route[0].Name != "n1" {
		t.Fatalf("sticky route = %#v, want n1", route)
	}
	if got := router.weightedRoundRobin["client"]; got != 0 {
		t.Fatalf("sticky/prefix path advanced weighted counter to %d", got)
	}

	route, err = router.BuildRouteForKeyWithOptions("client", "/v1/chat/completions", payload, "", RouteOptions{
		QuotaAvailability: func(backend Backend) bool { return backend.Name != "n1" },
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(route) != 2 || route[0].Name != "n2" {
		t.Fatalf("quota-filtered prefix route = %#v, want n2", route)
	}
	if got := router.weightedRoundRobin["client"]; got != 0 {
		t.Fatalf("prefix match advanced weighted counter to %d", got)
	}

	router.mu.Lock()
	router.Health["n2"] = BackendHealth{Name: "n2", Healthy: false, State: "half_open", Score: 1}
	router.mu.Unlock()
	route, err = router.BuildRouteForKeyWithOptions("client", "/v1/chat/completions", payload, "", RouteOptions{
		QuotaAvailability: func(backend Backend) bool { return backend.Name != "n1" },
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(route) != 1 || route[0].Name != "n3" {
		t.Fatalf("half-open prefix route = %#v, want weighted fallback n3", route)
	}

	route, err = router.BuildRoute("client", payload)
	if err != nil {
		t.Fatal(err)
	}
	if len(route) != 2 || route[0].Name != "n1" {
		t.Fatalf("temporary quota route rebound sticky entry: %#v", route)
	}
}

func TestRouterPrefixCacheAffinityStickyWinsOverPrefix(t *testing.T) {
	router := newPrefixAffinityRouter(t, 0)
	payload := map[string]any{
		"conversation_id": "abc",
		"messages":        prefixAffinityMessages("hi"),
	}
	router.BindSticky("conversation_id", "abc", "n1")
	router.recordPrefixAffinity("n2", payload)
	route, err := router.BuildRoute("client", payload)
	if err != nil {
		t.Fatal(err)
	}
	if route[0].Name != "n1" {
		t.Fatalf("sticky hint must win over prefix affinity: got %s", route[0].Name)
	}
}

func TestRouterPrefixCacheAffinityHitDoesNotAdvanceWeightedRoundRobin(t *testing.T) {
	router, err := NewRouter(&config.Config{
		Nodes: []config.NodeEntry{
			{Name: "n1", APIBase: "http://n1.test/v1", Model: "m", Weight: 2},
			{Name: "n2", APIBase: "http://n2.test/v1", Model: "m", Weight: 1},
		},
		Models:         []config.ClientModelEntry{{Name: "client", Nodes: []string{"n1", "n2"}, Balancing: "weighted_round_robin"}},
		RouterSettings: config.RouterSettings{PrefixCacheAffinity: config.PrefixCacheAffinitySettings{Enabled: true}},
	})
	if err != nil {
		t.Fatal(err)
	}
	prefixPayload := map[string]any{"messages": prefixAffinityMessages("cached-prefix")}
	router.recordPrefixAffinity("n2", prefixPayload)
	route, err := router.BuildRoute("client", prefixPayload)
	if err != nil {
		t.Fatal(err)
	}
	if len(route) != 2 || route[0].Name != "n2" {
		t.Fatalf("prefix route = %#v, want n2", route)
	}
	if got := router.weightedRoundRobin["client"]; got != 0 {
		t.Fatalf("prefix route advanced weighted counter to %d", got)
	}

	var got []string
	for i := 0; i < 3; i++ {
		route, err := router.BuildRoute("client", map[string]any{"messages": prefixAffinityMessages("uncached-prefix")})
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, route[0].Name)
	}
	want := []string{"n1", "n1", "n2"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("weighted sequence after prefix hit = %#v, want %#v", got, want)
		}
	}
}

func TestRouterPrefixCacheAffinitySkipsUnhealthyMatchingNode(t *testing.T) {
	router := newPrefixAffinityRouter(t, 0)
	router.FailureThreshold = 1
	payload := map[string]any{"messages": prefixAffinityMessages("hi")}
	router.recordPrefixAffinity("n1", payload)
	router.recordBackendFailure("n1", http.StatusServiceUnavailable, "down")
	route, err := router.BuildRoute("client", payload)
	if err != nil {
		t.Fatal(err)
	}
	if route[0].Name != "n2" {
		t.Fatalf("expected n2 after matching node n1 became unhealthy, got %s", route[0].Name)
	}
}

func TestRouterPrefixCacheAffinityRespectsEndpointFilter(t *testing.T) {
	router, err := NewRouter(&config.Config{
		Nodes: []config.NodeEntry{
			{Name: "n1", APIBase: "http://n1.test/v1", Model: "m", Endpoints: []string{"embeddings"}},
			{Name: "n2", APIBase: "http://n2.test/v1", Model: "m", Endpoints: []string{"chat"}},
		},
		Models:         []config.ClientModelEntry{{Name: "client", Nodes: []string{"n1", "n2"}}},
		RouterSettings: config.RouterSettings{PrefixCacheAffinity: config.PrefixCacheAffinitySettings{Enabled: true}},
	})
	if err != nil {
		t.Fatal(err)
	}
	payload := map[string]any{"messages": prefixAffinityMessages("hi")}
	router.recordPrefixAffinity("n1", payload)
	route, err := router.BuildRouteForPath("client", "/v1/chat/completions", payload)
	if err != nil {
		t.Fatal(err)
	}
	if route[0].Name != "n2" {
		t.Fatalf("endpoint-ineligible node must not be affinity-selected: got %s", route[0].Name)
	}
}

func TestRouterPrefixCacheAffinityRespectsAllowedKeys(t *testing.T) {
	router, err := NewRouter(&config.Config{
		Nodes: []config.NodeEntry{
			{Name: "n1", APIBase: "http://n1.test/v1", Model: "m", AllowedKeys: []any{"key-1"}},
			{Name: "n2", APIBase: "http://n2.test/v1", Model: "m", AllowedKeys: []any{"key-2"}},
		},
		Models:         []config.ClientModelEntry{{Name: "client", Nodes: []string{"n1", "n2"}}},
		RouterSettings: config.RouterSettings{PrefixCacheAffinity: config.PrefixCacheAffinitySettings{Enabled: true}},
	})
	if err != nil {
		t.Fatal(err)
	}
	payload := map[string]any{"messages": prefixAffinityMessages("hi")}
	router.recordPrefixAffinity("n1", payload)
	route, err := router.BuildRouteForKey("client", "/v1/chat/completions", payload, "key-2")
	if err != nil {
		t.Fatal(err)
	}
	if route[0].Name != "n2" {
		t.Fatalf("key-ineligible node must not be affinity-selected: got %s", route[0].Name)
	}
}

func TestRouterPrefixCacheAffinityRespectsAvailabilityPredicate(t *testing.T) {
	router := newPrefixAffinityRouter(t, 0)
	payload := map[string]any{"messages": prefixAffinityMessages("hi")}
	router.recordPrefixAffinity("n1", payload)
	route, err := router.BuildRouteForKeyWithOptions("client", "/v1/chat/completions", payload, "", RouteOptions{
		BackendAvailable: func(backend Backend) bool { return backend.Name != "n1" },
	})
	if err != nil {
		t.Fatal(err)
	}
	if route[0].Name != "n2" {
		t.Fatalf("unavailable matching node must not be affinity-selected: got %s", route[0].Name)
	}
}

func TestRouterPrefixCacheAffinityQuotaPredicateFiltersBeforeMatch(t *testing.T) {
	router := newPrefixAffinityRouter(t, 0)
	payload := map[string]any{"messages": prefixAffinityMessages("hi")}
	router.recordPrefixAffinity("n1", payload)
	route, err := router.BuildRouteForKeyWithOptions("client", "/v1/chat/completions", payload, "", RouteOptions{
		QuotaAvailability: func(backend Backend) bool { return backend.Name != "n1" },
	})
	if err != nil {
		t.Fatal(err)
	}
	if route[0].Name != "n2" {
		t.Fatalf("quota-exhausted matching node must not be affinity-selected: got %s", route[0].Name)
	}
}

func TestRouterPrefixCacheAffinityNoMatchPreservesWeightedRoundRobin(t *testing.T) {
	router, err := NewRouter(&config.Config{
		Nodes: []config.NodeEntry{
			{Name: "n1", APIBase: "http://n1.test/v1", Model: "m", Weight: 2},
			{Name: "n2", APIBase: "http://n2.test/v1", Model: "m", Weight: 1},
		},
		Models:         []config.ClientModelEntry{{Name: "client", Nodes: []string{"n1", "n2"}, Balancing: "weighted_round_robin"}},
		RouterSettings: config.RouterSettings{PrefixCacheAffinity: config.PrefixCacheAffinitySettings{Enabled: true}},
	})
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for i := 0; i < 6; i++ {
		route, err := router.BuildRoute("client", map[string]any{"messages": prefixAffinityMessages("fresh")})
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, route[0].Name)
	}
	want := []string{"n1", "n1", "n2", "n1", "n1", "n2"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("weighted no-match sequence = %#v, want %#v", got, want)
		}
	}
}

func TestRouterPrefixCacheAffinityNoMatchFallsBackToRoundRobin(t *testing.T) {
	router := newPrefixAffinityRouter(t, 0)
	payload := map[string]any{"messages": prefixAffinityMessages("fresh")}
	r1, err := router.BuildRoute("client", payload)
	if err != nil {
		t.Fatal(err)
	}
	r2, err := router.BuildRoute("client", payload)
	if err != nil {
		t.Fatal(err)
	}
	if r1[0].Name != "n1" || r2[0].Name != "n2" {
		t.Fatalf("no-match path should preserve round robin: got %s then %s", r1[0].Name, r2[0].Name)
	}
}

func TestRouterPrefixCacheAffinityPrefersDeepestPrefixMatch(t *testing.T) {
	router := newPrefixAffinityRouter(t, 0)
	payloadA := map[string]any{"messages": prefixAffinityMessages("a0", "a1", "a2")}
	router.recordPrefixAffinity("n1", payloadA)
	payloadB := map[string]any{"messages": prefixAffinityMessages("a0", "b1", "b2")}
	router.recordPrefixAffinity("n2", payloadB)

	rA, err := router.BuildRoute("client", payloadA)
	if err != nil {
		t.Fatal(err)
	}
	if rA[0].Name != "n1" {
		t.Fatalf("request A should prefer deepest match n1, got %s", rA[0].Name)
	}
	rB, err := router.BuildRoute("client", payloadB)
	if err != nil {
		t.Fatal(err)
	}
	if rB[0].Name != "n2" {
		t.Fatalf("request B should prefer deepest match n2, got %s", rB[0].Name)
	}
}

func TestRouterPrefixCacheAffinityConcurrentSelectionAndFIFOEviction(t *testing.T) {
	const maxEntries = 8
	router := newPrefixAffinityRouter(t, maxEntries)
	errCh := make(chan error, 64)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for worker := 0; worker < 8; worker++ {
		worker := worker
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			for i := 0; i < 50; i++ {
				payload := map[string]any{"messages": prefixAffinityMessages(fmt.Sprintf("worker-%02d-%02d", worker, i))}
				node := "n1"
				if (worker+i)%2 == 1 {
					node = "n2"
				}
				router.recordPrefixAffinity(node, payload)
				if _, err := router.BuildRoute("client", payload); err != nil {
					errCh <- err
					return
				}
			}
		}()
	}
	close(start)
	wg.Wait()
	close(errCh)
	for err := range errCh {
		if err != nil {
			t.Fatal(err)
		}
	}

	for i := 0; i < maxEntries+2; i++ {
		router.recordPrefixAffinity("n1", map[string]any{"messages": prefixAffinityMessages(fmt.Sprintf("fifo-%02d", i))})
	}
	oldestPrefix := requestPrefixHashes(map[string]any{"messages": prefixAffinityMessages("fifo-00")})
	recentPrefix := requestPrefixHashes(map[string]any{"messages": prefixAffinityMessages(fmt.Sprintf("fifo-%02d", maxEntries+1))})
	if len(oldestPrefix) != 1 || len(recentPrefix) != 1 {
		t.Fatalf("prefix hashes oldest=%d recent=%d", len(oldestPrefix), len(recentPrefix))
	}

	router.mu.Lock()
	gotEntries := len(router.prefixCache.entries)
	_, oldestPresent := router.prefixCache.entries[prefixCacheAffinityKey{node: "n1", hash: oldestPrefix[0].hash}]
	_, recentPresent := router.prefixCache.entries[prefixCacheAffinityKey{node: "n1", hash: recentPrefix[0].hash}]
	router.mu.Unlock()
	if gotEntries > maxEntries {
		t.Fatalf("entries = %d, want <= %d", gotEntries, maxEntries)
	}
	if oldestPresent {
		t.Fatalf("oldest FIFO prefix remained after bounded eviction")
	}
	if !recentPresent {
		t.Fatalf("recent FIFO prefix was evicted")
	}
}

func TestRouterPrefixCacheAffinityForwardRecordsAndReusesPrefix(t *testing.T) {
	var n1Hits, n2Hits atomic.Int32
	n1Server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		n1Hits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"a"}}]}`))
	}))
	defer n1Server.Close()
	n2Server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		n2Hits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"b"}}]}`))
	}))
	defer n2Server.Close()

	router, err := NewRouter(&config.Config{
		Nodes: []config.NodeEntry{
			{Name: "n1", APIBase: n1Server.URL + "/v1", Model: "m"},
			{Name: "n2", APIBase: n2Server.URL + "/v1", Model: "m"},
		},
		Models:         []config.ClientModelEntry{{Name: "client", Nodes: []string{"n1", "n2"}}},
		RouterSettings: config.RouterSettings{PrefixCacheAffinity: config.PrefixCacheAffinitySettings{Enabled: true}},
	})
	if err != nil {
		t.Fatal(err)
	}

	body := []byte(`{"model":"client","messages":[{"role":"user","content":"shared"}]}`)
	payload := map[string]any{"model": "client", "messages": []any{map[string]any{"role": "user", "content": "shared"}}}
	headers := http.Header{"Content-Type": {"application/json"}}

	r1, err := router.Forward(context.Background(), "client", "/v1/chat/completions", "", body, payload, false, headers)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, r1.Body)
	r1.Body.Close()
	r2, err := router.Forward(context.Background(), "client", "/v1/chat/completions", "", body, payload, false, headers)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, r2.Body)
	r2.Body.Close()

	if n1Hits.Load() != 2 || n2Hits.Load() != 0 {
		t.Fatalf("expected prefix affinity to pin n1 (n1=2, n2=0), got n1=%d n2=%d", n1Hits.Load(), n2Hits.Load())
	}
}

func TestRouterRetryable429FallsBackAndDoesNotRecordFailedPrefix(t *testing.T) {
	var n1Hits, n2Hits atomic.Int32
	n1Server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		n1Hits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"message":"quota"}}`))
	}))
	defer n1Server.Close()
	n2Server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		n2Hits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}],"usage":{"total_tokens":7}}`))
	}))
	defer n2Server.Close()

	router, err := NewRouter(&config.Config{
		Nodes: []config.NodeEntry{
			{Name: "n1", APIBase: n1Server.URL + "/v1", Model: "m"},
			{Name: "n2", APIBase: n2Server.URL + "/v1", Model: "m"},
		},
		Models: []config.ClientModelEntry{
			{Name: "client", Nodes: []string{"n1"}},
			{Name: "client-fallback", Nodes: []string{"n2"}},
		},
		RouterSettings: config.RouterSettings{
			Fallbacks:           []map[string]any{{"client": []any{"client-fallback"}}},
			PrefixCacheAffinity: config.PrefixCacheAffinitySettings{Enabled: true},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	router.FailureThreshold = 1

	body := []byte(`{"model":"client","messages":[{"role":"user","content":"shared"}]}`)
	payload := map[string]any{"model": "client", "messages": prefixAffinityMessages("shared")}
	result, err := router.ForwardWithOptions(context.Background(), "client", "/v1/chat/completions", "", body, payload, false, http.Header{"Content-Type": {"application/json"}}, ForwardOptions{
		QuotaAvailability: func(Backend) bool { return true },
	})
	if err != nil {
		t.Fatal(err)
	}
	defer result.Response.Body.Close()
	_, _ = io.Copy(io.Discard, result.Response.Body)
	if result.Response.StatusCode != http.StatusOK || result.BackendName != "n2" {
		t.Fatalf("result status=%d backend=%s", result.Response.StatusCode, result.BackendName)
	}
	if n1Hits.Load() != 1 || n2Hits.Load() != 1 {
		t.Fatalf("hits n1=%d n2=%d", n1Hits.Load(), n2Hits.Load())
	}
	if len(result.AttemptTrail) != 2 || result.AttemptTrail[0].Status != http.StatusTooManyRequests || !result.AttemptTrail[0].Retryable || result.AttemptTrail[1].Status != http.StatusOK {
		t.Fatalf("attempt trail = %#v", result.AttemptTrail)
	}

	health := router.HealthSnapshot()
	if len(health) != 2 || health[0].Name != "n1" || health[0].Healthy || health[0].FailureCount != 1 || health[0].LastStatus != http.StatusTooManyRequests {
		t.Fatalf("health after 429 = %#v", health)
	}
	prefixes := requestPrefixHashes(payload)
	if len(prefixes) != 1 {
		t.Fatalf("prefixes = %d, want 1", len(prefixes))
	}
	router.mu.Lock()
	_, failedRecorded := router.prefixCache.entries[prefixCacheAffinityKey{node: "n1", hash: prefixes[0].hash}]
	_, fallbackRecorded := router.prefixCache.entries[prefixCacheAffinityKey{node: "n2", hash: prefixes[0].hash}]
	router.mu.Unlock()
	if failedRecorded {
		t.Fatalf("429 backend recorded prefix affinity")
	}
	if !fallbackRecorded {
		t.Fatalf("successful fallback did not record prefix affinity")
	}

	route, err := router.BuildRoute("client", payload)
	if err != nil {
		t.Fatal(err)
	}
	if len(route) != 1 || route[0].Name != "n2" {
		t.Fatalf("route after 429 health/fallback = %#v", route)
	}
}

func TestRouterPrefixCacheAffinityForwardDoesNotRecordNonOKResponse(t *testing.T) {
	var n1Hits, n2Hits atomic.Int32
	n1Server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		n1Hits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"created"}}]}`))
	}))
	defer n1Server.Close()
	n2Server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		n2Hits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}]}`))
	}))
	defer n2Server.Close()

	router, err := NewRouter(&config.Config{
		Nodes: []config.NodeEntry{
			{Name: "n1", APIBase: n1Server.URL + "/v1", Model: "m"},
			{Name: "n2", APIBase: n2Server.URL + "/v1", Model: "m"},
		},
		Models:         []config.ClientModelEntry{{Name: "client", Nodes: []string{"n1", "n2"}}},
		RouterSettings: config.RouterSettings{PrefixCacheAffinity: config.PrefixCacheAffinitySettings{Enabled: true}},
	})
	if err != nil {
		t.Fatal(err)
	}

	body := []byte(`{"model":"client","messages":[{"role":"user","content":"shared"}]}`)
	payload := map[string]any{"model": "client", "messages": []any{map[string]any{"role": "user", "content": "shared"}}}
	headers := http.Header{"Content-Type": {"application/json"}}
	for i := 0; i < 2; i++ {
		resp, err := router.Forward(context.Background(), "client", "/v1/chat/completions", "", body, payload, false, headers)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
	}
	if n1Hits.Load() != 1 || n2Hits.Load() != 1 {
		t.Fatalf("non-200 response should not create affinity binding: n1=%d n2=%d", n1Hits.Load(), n2Hits.Load())
	}
}

func TestRouterPrefixCacheAffinityTrackerEvictsBounded(t *testing.T) {
	tracker := newPrefixCacheAffinityTracker(2)
	hash := func(seed string) [sha256.Size]byte { return sha256.Sum256([]byte(seed)) }
	tracker.observe("n1", []prefixCacheRequestPrefix{{depth: 0, hash: hash("a")}})
	tracker.observe("n1", []prefixCacheRequestPrefix{{depth: 0, hash: hash("b")}})
	tracker.observe("n1", []prefixCacheRequestPrefix{{depth: 0, hash: hash("c")}})
	if got := len(tracker.entries); got > 2 {
		t.Fatalf("entries = %d, want <= 2", got)
	}
	if _, ok := tracker.entries[prefixCacheAffinityKey{node: "n1", hash: hash("a")}]; ok {
		t.Fatalf("oldest entry should have been evicted")
	}
	if _, ok := tracker.match([]string{"n1"}, []prefixCacheRequestPrefix{{depth: 0, hash: hash("a")}}); ok {
		t.Fatalf("evicted prefix must not match")
	}
	if _, ok := tracker.match([]string{"n1"}, []prefixCacheRequestPrefix{{depth: 0, hash: hash("c")}}); !ok {
		t.Fatalf("recent prefix should still match")
	}
}
