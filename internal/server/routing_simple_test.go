package server

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/theio/yallmp-proxy/internal/config"
)

func TestProviderDiscoverySelectionAndRemoval(t *testing.T) {
	t.Setenv(config.AdminPasswordEnv, "pw")
	app, path := newRoutingAdminTestApp(t)
	var hits atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if r.URL.Path != "/v1/models" || r.Header.Get("Authorization") != "Bearer provider-secret" || r.Header.Get("x-admin-password") != "" {
			t.Errorf("unexpected discovery request: %s %+v", r.URL, r.Header)
		}
		io.WriteString(w, `{"data":[{"id":"qwen"},{"id":"llama"},{"id":"qwen"}]}`)
	}))
	defer upstream.Close()
	payload := fmt.Sprintf(`{"name":"hosted","api_base":%q,"api_key":"provider-secret"}`, upstream.URL+"/v1")
	resp := routingAdminRequest(app, http.MethodPost, "/admin/providers/discover", payload, "")
	if resp.Code != http.StatusForbidden || hits.Load() != 0 {
		t.Fatal("unauthorized discovery reached upstream")
	}
	resp = routingAdminRequest(app, http.MethodPost, "/admin/providers/discover", payload, "pw")
	if resp.Code != 200 || !strings.Contains(resp.Body.String(), `"llama","qwen"`) {
		t.Fatalf("discovery: %d %s", resp.Code, resp.Body)
	}
	if _, ok := app.Config.RawProvider("hosted"); ok {
		t.Fatal("discovery saved provider")
	}
	// Selection and credentials are saved in one mutation; nodes/pools are derived.
	payload = fmt.Sprintf(`{"name":"hosted","api_base":%q,"api_key":"provider-secret","models":["qwen","llama"]}`, upstream.URL+"/v1")
	resp = routingAdminRequest(app, http.MethodPost, "/admin/providers", payload, "pw")
	if resp.Code != 200 {
		t.Fatalf("create: %d %s", resp.Code, resp.Body)
	}
	resp = routingAdminRequest(app, http.MethodGet, "/admin/routing-pools", "", "pw")
	if resp.Code != 200 || !strings.Contains(resp.Body.String(), `provider/hosted/qwen`) || strings.Contains(resp.Body.String(), "provider-secret") {
		t.Fatalf("pools: %d %s", resp.Code, resp.Body)
	}
	resp = routingAdminRequest(app, http.MethodPost, "/admin/providers/discover", `{"name":"hosted"}`, "pw")
	if resp.Code != 200 {
		t.Fatalf("saved credentials discovery: %d %s", resp.Code, resp.Body)
	}
	// A masked config round trip must retain the provider key and model selection.
	resp = routingAdminRequest(app, http.MethodGet, "/admin/config", "", "pw")
	resp = routingAdminRequest(app, http.MethodPut, "/admin/config", resp.Body.String(), "pw")
	if resp.Code != 200 {
		t.Fatalf("config round trip: %d %s", resp.Code, resp.Body)
	}
	b, _ := app.Router.GetBackend(config.ProviderNodeName("hosted", "qwen"))
	if b.APIKey != "provider-secret" {
		t.Fatal("round trip lost credentials")
	}
	saved, _ := os.ReadFile(path)
	if strings.Contains(string(saved), "provider/hosted") {
		t.Fatal("generated nodes persisted")
	}
	resp = routingAdminRequest(app, http.MethodPut, "/admin/providers/hosted", `{"models":["llama"]}`, "pw")
	if resp.Code != 200 {
		t.Fatalf("deselect: %d %s", resp.Code, resp.Body)
	}
	if _, ok := app.Router.GetBackend(config.ProviderNodeName("hosted", "qwen")); ok {
		t.Fatal("deselected node still routable")
	}
	resp = routingAdminRequest(app, http.MethodDelete, "/admin/providers/hosted", "", "pw")
	if resp.Code != 200 {
		t.Fatalf("delete generated provider: %d %s", resp.Code, resp.Body)
	}
	if _, ok := app.Router.GetBackend("node-a"); !ok {
		t.Fatal("unrelated manual node disappeared")
	}
}

func TestProviderAliasesShareRateAllowance(t *testing.T) {
	var hits atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"ok"}}]}`)
	}))
	defer upstream.Close()
	app, err := New(&config.Config{Providers: []config.ProviderEntry{{Name: "p", APIBase: upstream.URL, Limits: config.LimitsConfig{RPM: 1}, Models: []config.ProviderModel{{Name: "a", UpstreamModel: "same"}, {Name: "b", UpstreamModel: "same"}}}}})
	if err != nil {
		t.Fatal(err)
	}
	for i, model := range []string{"a", "b"} {
		body, _ := json.Marshal(map[string]any{"model": model, "messages": []map[string]string{{"role": "user", "content": "hi"}}})
		resp := routingAdminRequest(app, http.MethodPost, "/v1/chat/completions", string(body), "")
		want := 200
		if i == 1 {
			want = 429
		}
		if resp.Code != want {
			t.Fatalf("alias %s: got %d want %d body=%s", model, resp.Code, want, resp.Body)
		}
	}
	if hits.Load() != 1 {
		t.Fatalf("provider received %d calls", hits.Load())
	}
}

func TestDiscoveryRejectsInvalidResponsesWithoutLeakingBody(t *testing.T) {
	t.Setenv(config.AdminPasswordEnv, "pw")
	app, _ := newRoutingAdminTestApp(t)
	for _, response := range []string{`{"error":"secret-debug-data"}`, `not-json`, `{"data":null}`} {
		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, response) }))
		payload := fmt.Sprintf(`{"api_base":%q}`, upstream.URL)
		resp := routingAdminRequest(app, http.MethodPost, "/admin/providers/discover", payload, "pw")
		upstream.Close()
		if resp.Code != 502 || strings.Contains(resp.Body.String(), "secret-debug-data") {
			t.Fatalf("invalid response: %d %s", resp.Code, resp.Body)
		}
	}
}

func TestNativePoolsPreserveEndpointAndFailOver(t *testing.T) {
	for _, endpoint := range []string{"messages", "responses"} {
		t.Run(endpoint, func(t *testing.T) {
			var calls atomic.Int32
			upstream := func(status int) *httptest.Server {
				return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					calls.Add(1)
					if r.URL.Path != "/v1/"+endpoint {
						t.Errorf("native request used %s", r.URL.Path)
					}
					var body map[string]any
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
						t.Error(err)
					}
					if body["model"] != "upstream" {
						t.Errorf("model: %v", body["model"])
					}
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(status)
					if status != 200 {
						io.WriteString(w, `{"error":{"message":"retry"}}`)
						return
					}
					if endpoint == "messages" {
						io.WriteString(w, `{"id":"msg_native","type":"message","role":"assistant","content":[{"type":"text","text":"hello"}],"stop_reason":"end_turn"}`)
					} else {
						io.WriteString(w, `{"id":"resp_native","object":"response","status":"completed","output":[]}`)
					}
				}))
			}
			a, b := upstream(503), upstream(200)
			defer a.Close()
			defer b.Close()
			nodes := []config.NodeEntry{{Name: "a", Model: "public", UpstreamModel: "upstream", APIBase: a.URL + "/v1"}, {Name: "b", Model: "public", UpstreamModel: "upstream", APIBase: b.URL + "/v1"}}
			for i := range nodes {
				if endpoint == "messages" {
					nodes[i].APIType = "anthropic"
				} else {
					nodes[i].SupportsResponsesAPI = true
				}
			}
			app, err := New(&config.Config{Nodes: nodes, ProxySettings: config.ProxySettings{EnableMessagesEndpoint: true, EnableResponsesEndpoint: true}})
			if err != nil {
				t.Fatal(err)
			}
			body := `{"model":"public","max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`
			if endpoint == "responses" {
				body = `{"model":"public","input":"hello","store":false}`
			}
			resp := routingAdminRequest(app, http.MethodPost, "/v1/"+endpoint, body, "")
			if resp.Code != 200 || calls.Load() != 2 {
				t.Fatalf("status=%d calls=%d body=%s", resp.Code, calls.Load(), resp.Body)
			}
		})
	}
}
