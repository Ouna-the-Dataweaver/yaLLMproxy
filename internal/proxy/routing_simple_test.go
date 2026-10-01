package proxy

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/theio/yallmp-proxy/internal/config"
)

func TestAutomaticPoolFailoverKeepsSuccessfulNodeAffinity(t *testing.T) {
	calls := []string{}
	var callsMu sync.Mutex
	upstream := func(name string, status int) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			callsMu.Lock()
			calls = append(calls, name)
			callsMu.Unlock()
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			if body["model"] != "vendor/qwen" {
				t.Errorf("upstream model = %v", body["model"])
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(status)
			io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"ok"}}]}`)
		}))
	}
	a, b, c := upstream("a", 503), upstream("b", 200), upstream("fallback", 200)
	defer a.Close()
	defer b.Close()
	defer c.Close()
	router, err := NewRouter(&config.Config{
		Nodes: []config.NodeEntry{
			{Name: "a", Model: "qwen", UpstreamModel: "vendor/qwen", APIBase: a.URL},
			{Name: "b", Model: "qwen", UpstreamModel: "vendor/qwen", APIBase: b.URL},
			{Name: "fallback", Model: "other", UpstreamModel: "vendor/qwen", APIBase: c.URL},
		},
		RouterSettings: config.RouterSettings{Fallbacks: []map[string]any{{"qwen": []any{"other"}}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !router.PrefixCacheAffinity {
		t.Fatal("prefix affinity should be enabled by default")
	}
	payload := map[string]any{"model": "qwen", "conversation_id": "conversation", "messages": prefixAffinityMessages("shared")}
	body, _ := json.Marshal(payload)
	result, err := router.ForwardWithOptions(context.Background(), "qwen", "/v1/chat/completions", "", body, payload, false, http.Header{}, ForwardOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer result.Response.Body.Close()
	callsMu.Lock()
	gotCalls := append([]string(nil), calls...)
	callsMu.Unlock()
	if result.Response.StatusCode != 200 || !reflect.DeepEqual(gotCalls, []string{"a", "b"}) {
		t.Fatalf("status=%d calls=%v", result.Response.StatusCode, gotCalls)
	}
	if len(result.AttemptTrail) != 2 || result.BackendName != "b" {
		t.Fatalf("result: %+v", result)
	}
	route, err := router.BuildRoute("qwen", payload)
	if err != nil || route[0].Name != "b" {
		t.Fatalf("successful node not sticky: %+v %v", route, err)
	}
	delete(payload, "conversation_id")
	route, err = router.BuildRoute("qwen", payload)
	if err != nil || route[0].Name != "b" {
		t.Fatalf("prefix affinity lost: %+v %v", route, err)
	}
	// Access restrictions override both conversation and prefix affinity.
	restricted := router.Backends["b"]
	restricted.AllowedKeys = []string{"other-key"}
	router.Backends["b"] = restricted
	route, err = router.BuildRouteForKey("qwen", "/v1/chat/completions", payload, "caller")
	if err != nil || route[0].Name != "a" || len(route) != 2 {
		t.Fatalf("restricted node was eligible: %+v %v", route, err)
	}
}

func TestProviderExpansionInheritsTimeoutAndSharesAliasRateScope(t *testing.T) {
	router, err := NewRouter(&config.Config{Providers: []config.ProviderEntry{{Name: "hosted", APIBase: "http://example.test/v1", APIKey: "credential", RequestTimeout: 5,
		Models: []config.ProviderModel{{Name: "public-a", UpstreamModel: "vendor/model"}, {Name: "public-b", UpstreamModel: "vendor/model"}},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	a, _ := router.GetBackend(config.ProviderNodeName("hosted", "public-a"))
	b, _ := router.GetBackend(config.ProviderNodeName("hosted", "public-b"))
	if a.Timeout != 5*time.Second || a.APIKey != "credential" || a.TargetModel != "vendor/model" {
		t.Fatalf("inheritance failed: %+v", a)
	}
	if a.RateLimitScope == "" || a.RateLimitScope != b.RateLimitScope {
		t.Fatalf("aliases have distinct rate scopes: %q %q", a.RateLimitScope, b.RateLimitScope)
	}
}

func TestPoolDoesNotFailoverAfterStreamingResponseStarts(t *testing.T) {
	calls := 0
	first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: {\"choices\":[]}\n\n")
		w.(http.Flusher).Flush()
	}))
	defer first.Close()
	second := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++; w.WriteHeader(200) }))
	defer second.Close()
	router, err := NewRouter(&config.Config{Nodes: []config.NodeEntry{{Name: "a", Model: "m", APIBase: first.URL}, {Name: "b", Model: "m", APIBase: second.URL}}})
	if err != nil {
		t.Fatal(err)
	}
	payload := map[string]any{"model": "m", "stream": true, "messages": prefixAffinityMessages("hello")}
	body, _ := json.Marshal(payload)
	result, err := router.ForwardWithOptions(context.Background(), "m", "/v1/chat/completions", "", body, payload, true, http.Header{}, ForwardOptions{})
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, result.Response.Body)
	result.Response.Body.Close()
	if calls != 0 {
		t.Fatal("stream was replayed to a sibling")
	}
}
