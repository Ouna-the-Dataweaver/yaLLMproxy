package proxy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/theio/yallmp-proxy/internal/config"
)

func engineBool(v bool) *bool { return &v }
func engineInt(v int) *int    { return &v }

func TestEngineAdapters(t *testing.T) {
	tests := []struct {
		name, engine, path, input, want          string
		strict, disabled, unsupported, wantError bool
	}{
		{name: "sglang budget preserves siblings", engine: "sglang", input: `{"thinking_token_budget":8192,"custom_params":{"other":42},"vendor":true}`, want: `{"custom_params":{"thinking_budget":8192,"other":42},"vendor":true}`},
		{name: "vllm budget preserves siblings", engine: "vllm", input: `{"custom_params":{"thinking_budget":0,"other":42}}`, want: `{"thinking_token_budget":0,"custom_params":{"other":42}}`},
		{name: "equal aliases", engine: "sglang", input: `{"thinking_token_budget":3,"custom_params":{"thinking_budget":3}}`, want: `{"custom_params":{"thinking_budget":3}}`},
		{name: "budget conflict", engine: "sglang", input: `{"thinking_token_budget":3,"custom_params":{"thinking_budget":4}}`, wantError: true},
		{name: "bad nested target", engine: "sglang", input: `{"thinking_token_budget":3,"custom_params":"keep"}`, wantError: true},
		{name: "fractional budget", engine: "vllm", input: `{"thinking_token_budget":0.5}`, wantError: true},
		{name: "sglang regex", engine: "sglang", input: `{"structured_outputs":{"regex":"(yes|no)"},"top_k":4}`, want: `{"regex":"(yes|no)","top_k":4}`},
		{name: "short chat endpoint", engine: "sglang", path: "/chat/completions", input: `{"structured_outputs":{"regex":"yes"}}`, want: `{"regex":"yes"}`},
		{name: "native unlimited budget", engine: "vllm", input: `{"thinking_token_budget":-1}`, want: `{"thinking_token_budget":-1}`},
		{name: "unportable sentinel passthrough", engine: "sglang", input: `{"thinking_token_budget":-1}`, want: `{"thinking_token_budget":-1}`},
		{name: "native vllm JSON options", engine: "vllm", input: `{"structured_outputs":{"json":{"type":"object"},"whitespace_pattern":" "}}`, want: `{"structured_outputs":{"json":{"type":"object"},"whitespace_pattern":" "}}`},
		{name: "vllm regex", engine: "vllm", input: `{"regex":"yes"}`, want: `{"structured_outputs":{"regex":"yes"}}`},
		{name: "legacy regex", engine: "vllm", input: `{"guided_regex":"yes"}`, want: `{"structured_outputs":{"regex":"yes"}}`},
		{name: "regex conflict", engine: "vllm", input: `{"regex":"a","structured_outputs":{"regex":"b"}}`, wantError: true},
		{name: "opaque structured options", engine: "sglang", input: `{"structured_outputs":{"regex":"x","whitespace_pattern":" "}}`, want: `{"structured_outputs":{"regex":"x","whitespace_pattern":" "}}`},
		{name: "strict structured options", engine: "sglang", strict: true, input: `{"structured_outputs":{"regex":"x","whitespace_pattern":" "}}`, wantError: true},
		{name: "chat json", engine: "sglang", input: `{"structured_outputs":{"json":{"type":"object"}}}`, want: `{"response_format":{"type":"json_schema","json_schema":{"name":"response","schema":{"type":"object"}}}}`},
		{name: "sglang completion json", engine: "sglang", path: "/v1/completions", input: `{"structured_outputs":{"json":{"type":"object"}}}`, want: `{"json_schema":"{\"type\":\"object\"}"}`},
		{name: "vllm completion json", engine: "vllm", path: "/v1/completions", input: `{"json_schema":"{\"type\":\"object\"}"}`, want: `{"structured_outputs":{"json":{"type":"object"}}}`},
		{name: "response format metadata", engine: "vllm", input: `{"json_schema":{"type":"object"},"response_format":{"type":"json_schema","json_schema":{"name":"custom","strict":true,"schema":{"type":"object"}}}}`, want: `{"response_format":{"type":"json_schema","json_schema":{"name":"custom","strict":true,"schema":{"type":"object"}}}}`},
		{name: "constraint conflict", engine: "sglang", input: `{"regex":"a","json_schema":{}}`, wantError: true},
		{name: "unsupported budget passthrough", engine: "sglang", unsupported: true, input: `{"thinking_token_budget":8}`, want: `{"thinking_token_budget":8}`},
		{name: "strict unsupported budget", engine: "sglang", strict: true, unsupported: true, input: `{"thinking_token_budget":8}`, wantError: true},
		{name: "unknown conflicting extensions pass", engine: "auto", input: `{"regex":"a","guided_regex":"b"}`, want: `{"regex":"a","guided_regex":"b"}`},
		{name: "unknown pass", engine: "auto", input: `{"regex":"a"}`, want: `{"regex":"a"}`},
		{name: "unknown strict", engine: "auto", strict: true, input: `{"regex":"a"}`, wantError: true},
		{name: "disabled exact passthrough", engine: "sglang", disabled: true, input: `{ "regex": "a", "custom": [1,2] }`, want: `{ "regex": "a", "custom": [1,2] }`},
		{name: "native responses untouched", engine: "sglang", path: "/v1/responses", input: `{"structured_outputs":{"regex":"a"}}`, want: `{"structured_outputs":{"regex":"a"}}`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var payload map[string]any
			if err := json.Unmarshal([]byte(tc.input), &payload); err != nil {
				t.Fatal(err)
			}
			before := clonePayloadMap(payload)
			b := Backend{}
			if err := configureBackendEngine(&b, tc.engine, config.EngineCompatibility{Enabled: engineBool(!tc.disabled), Strict: engineBool(tc.strict), ThinkingBudget: engineBool(!tc.unsupported)}); err != nil {
				t.Fatal(err)
			}
			path := tc.path
			if path == "" {
				path = "/v1/chat/completions"
			}
			result := BuildBackendBodyWithWarnings(payload, b, []byte(tc.input), false, path)
			if !reflect.DeepEqual(payload, before) {
				t.Fatal("mutated original payload")
			}
			if tc.wantError {
				if result.Err == nil {
					t.Fatalf("expected error, got %s", result.Body)
				}
				return
			}
			if result.Err != nil {
				t.Fatal(result.Err)
			}
			var got, want any
			if err := json.Unmarshal(result.Body, &got); err != nil {
				t.Fatal(err)
			}
			json.Unmarshal([]byte(tc.want), &want)
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("got %s, want %s", result.Body, tc.want)
			}
			if tc.disabled && string(result.Body) != tc.input {
				t.Fatal("disabled adapter changed bytes")
			}
		})
	}
}

func TestEngineAdapterPreservesParameterDefaultsAcrossRequests(t *testing.T) {
	for _, allowOverride := range []bool{false, true} {
		t.Run(fmt.Sprint(allowOverride), func(t *testing.T) {
			defaults := map[string]any{"thinking_budget": 8, "other": true}
			b := Backend{Parameters: map[string]ParameterConfig{
				"custom_params": {Default: defaults, AllowOverride: allowOverride},
			}}
			if err := configureBackendEngine(&b, "vllm", config.EngineCompatibility{Enabled: engineBool(true), ThinkingBudget: engineBool(true)}); err != nil {
				t.Fatal(err)
			}
			for i := 0; i < 2; i++ {
				result := BuildBackendBodyWithWarnings(map[string]any{}, b, []byte(`{}`), false)
				if result.Err != nil {
					t.Fatal(result.Err)
				}
				var got map[string]any
				if err := json.Unmarshal(result.Body, &got); err != nil {
					t.Fatal(err)
				}
				if got["thinking_token_budget"] != float64(8) || !reflect.DeepEqual(got["custom_params"], map[string]any{"other": true}) {
					t.Fatalf("request %d lost parameter default: %s", i, result.Body)
				}
			}
			if !reflect.DeepEqual(defaults, map[string]any{"thinking_budget": 8, "other": true}) {
				t.Fatalf("mutated backend defaults: %+v", defaults)
			}
		})
	}
}

func TestEngineDiscoveryAndRefresh(t *testing.T) {
	var hits atomic.Int32
	var engine atomic.Value
	engine.Store("sglang")
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if r.Header.Get("Authorization") != "Bearer test-key" {
			t.Error("probe lost authentication")
		}
		switch r.URL.Path {
		case "/prefix/v1/models":
			fmt.Fprintf(w, `{"data":[{"id":"m","owned_by":%q}]}`, engine.Load().(string))
		case "/prefix/openapi.json":
			if engine.Load() == "sglang" {
				io.WriteString(w, `{"components":{"schemas":{"ChatCompletionRequest":{"properties":{"regex":{},"custom_params":{},"routed_dp_rank":{}}}}}}`)
			} else {
				io.WriteString(w, `{"components":{"schemas":{"ChatCompletionRequest":{"properties":{"structured_outputs":{},"thinking_token_budget":{}}}}}}`)
			}
		case "/prefix/server_info":
			io.WriteString(w, `{"version":"0.5.19","dp_size":2,"enable_strict_thinking":false,"enable_custom_logit_processor":false,"api_key":"must-not-expose"}`)
		case "/prefix/version":
			io.WriteString(w, `{"version":"0.31.0"}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()
	router, err := NewRouter(&config.Config{Nodes: []config.NodeEntry{{Name: "n", Model: "m", APIBase: upstream.URL + "/prefix/v1", APIKey: "test-key", Engine: "auto", Compatibility: config.EngineCompatibility{Enabled: engineBool(true)}}}})
	if err != nil {
		t.Fatal(err)
	}
	router.DiscoverEngines(context.Background())
	b, _ := router.GetBackend("n")
	if p := b.EngineProfile; p.Engine != "sglang" || p.Version != "0.5.19" || p.ThinkingBudget == nil || *p.ThinkingBudget || p.DPSize != 2 {
		t.Fatalf("profile: %+v", p)
	}
	priorHits := hits.Load()
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); router.DiscoverEngines(context.Background()); _ = router.EngineSnapshot() }()
	}
	wg.Wait()
	if hits.Load() != priorHits {
		t.Fatal("cached discovery repeated probes")
	}
	oldRoute, _ := router.BuildRoute("m", nil)
	engine.Store("vllm")
	b.engineState.mu.Lock()
	b.engineState.nextProbe = time.Time{}
	b.engineState.mu.Unlock()
	router.DiscoverEngines(context.Background())
	current, _ := router.GetBackend("n")
	if current.EngineProfile.Engine != "vllm" || current.EngineProfile.ThinkingBudget != nil {
		t.Fatalf("stale profile: %+v", current.EngineProfile)
	}
	if oldRoute[0].EngineProfile.Engine != "sglang" {
		t.Fatal("in-flight snapshot mutated")
	}
	encoded, _ := json.Marshal(router.EngineSnapshot())
	if strings.Contains(string(encoded), "test-key") || strings.Contains(string(encoded), "must-not-expose") {
		t.Fatal("secrets exposed")
	}
}

func TestEngineDiscoveryUnknownAndRedirects(t *testing.T) {
	var redirected atomic.Int32
	sink := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { redirected.Add(1) }))
	defer sink.Close()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/openapi.json" {
			http.Redirect(w, r, sink.URL, http.StatusFound)
			return
		}
		io.WriteString(w, `{"version":"1.0","data":[{"id":"m","owned_by":"someone"}]}`)
	}))
	defer upstream.Close()
	p := DiscoverBackendEngine(context.Background(), nil, Backend{BaseURL: upstream.URL + "/v1", APIKey: "secret", TargetModel: "m"})
	if p.Engine != "unknown" || redirected.Load() != 0 {
		t.Fatalf("unsafe detection: %+v redirects %d", p, redirected.Load())
	}
}

func TestEngineFallbackAndRankRouting(t *testing.T) {
	var first, second atomic.Int32
	a := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		first.Add(1)
		var p map[string]any
		json.NewDecoder(r.Body).Decode(&p)
		if p["regex"] != "yes" || p["structured_outputs"] != nil || p["routed_dp_rank"] != float64(1) || r.Header.Get("X-data-parallel-rank") != "" || r.Header.Get("x-override-routed-dp-rank") != "" {
			t.Errorf("bad SGLang request: %+v headers %+v", p, r.Header)
		}
		w.WriteHeader(503)
	}))
	defer a.Close()
	b := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		second.Add(1)
		var p map[string]any
		json.NewDecoder(r.Body).Decode(&p)
		regex, _ := getPath(p, "structured_outputs.regex")
		if regex != "yes" || p["regex"] != nil || p["routed_dp_rank"] != nil || r.Header.Get("X-data-parallel-rank") != "0" || r.Header.Get("x-override-routed-dp-rank") != "" {
			t.Errorf("bad vLLM request: %+v headers %+v", p, r.Header)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: {\"choices\":[]}\n\ndata: [DONE]\n\n")
	}))
	defer b.Close()
	router, err := NewRouter(&config.Config{Nodes: []config.NodeEntry{
		{Name: "a", Model: "m", APIBase: a.URL + "/v1", Engine: "sglang", Compatibility: config.EngineCompatibility{Enabled: engineBool(true), DPRank: engineInt(1), DPSize: engineInt(2)}},
		{Name: "b", Model: "m", APIBase: b.URL + "/v1", Engine: "vllm", Compatibility: config.EngineCompatibility{Enabled: engineBool(true), DPRank: engineInt(0), DPSize: engineInt(1)}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	body := []byte(`{"model":"m","structured_outputs":{"regex":"yes"},"stream":true,"routed_dp_rank":99}`)
	var p map[string]any
	json.Unmarshal(body, &p)
	headers := http.Header{"X-Data-Parallel-Rank": {"99"}, "X-Override-Routed-Dp-Rank": {"99"}}
	result, err := router.ForwardWithResult(context.Background(), "m", "/v1/chat/completions", "", body, p, true, headers)
	if err != nil {
		t.Fatal(err)
	}
	defer result.Response.Body.Close()
	stream, _ := io.ReadAll(result.Response.Body)
	if !strings.Contains(string(stream), "[DONE]") || first.Load() != 1 || second.Load() != 1 {
		t.Fatalf("fallback failed: %s %d %d", stream, first.Load(), second.Load())
	}
	if p["routed_dp_rank"] != float64(99) || headers.Get("X-Data-Parallel-Rank") != "99" || headers.Get("X-Override-Routed-Dp-Rank") != "99" {
		t.Fatal("mutated original rank hints")
	}
}

func TestEngineLocalErrorsDoNotRetryOrPoisonHealth(t *testing.T) {
	var hits atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits.Add(1) }))
	defer upstream.Close()
	router, err := NewRouter(&config.Config{ModelList: []config.ModelEntry{{ModelName: "m", ModelParams: map[string]any{"api_base": upstream.URL + "/v1", "engine": "vllm", "compatibility": map[string]any{"enabled": true}}}}, RouterSettings: config.RouterSettings{NumRetries: 3}})
	if err != nil {
		t.Fatal(err)
	}
	body := []byte(`{"model":"m","regex":"a","guided_regex":"b"}`)
	var p map[string]any
	json.Unmarshal(body, &p)
	result, err := router.ForwardWithResult(context.Background(), "m", "/v1/chat/completions", "", body, p, false, nil)
	var ce *CompatibilityError
	if !errors.As(err, &ce) || len(result.AttemptTrail) != 1 || hits.Load() != 0 {
		t.Fatalf("local error retried: %v %+v hits %d", err, result, hits.Load())
	}
	for _, h := range router.HealthSnapshot() {
		if h.FailureCount != 0 {
			t.Fatal("local error counted as upstream failure")
		}
	}
}

func TestEngineCapabilityFallback(t *testing.T) {
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"choices":[]}`)
	}))
	defer upstream.Close()
	router, err := NewRouter(&config.Config{Nodes: []config.NodeEntry{
		{Name: "a", Model: "m", APIBase: upstream.URL + "/v1", Engine: "sglang", Compatibility: config.EngineCompatibility{Enabled: engineBool(true), Strict: engineBool(true), ThinkingBudget: engineBool(false)}},
		{Name: "b", Model: "m", APIBase: upstream.URL + "/v1", Engine: "vllm", Compatibility: config.EngineCompatibility{Enabled: engineBool(true), Strict: engineBool(true), ThinkingBudget: engineBool(true)}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	body := []byte(`{"model":"m","thinking_token_budget":4}`)
	var p map[string]any
	json.Unmarshal(body, &p)
	result, err := router.ForwardWithResult(context.Background(), "m", "/v1/chat/completions", "", body, p, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer result.Response.Body.Close()
	if result.BackendName != "b" || calls.Load() != 1 || len(result.AttemptTrail) != 2 {
		t.Fatalf("capability fallback: %+v", result)
	}
}

func TestEngineDisabledDiscoveryAndCancellation(t *testing.T) {
	var hits atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits.Add(1); <-r.Context().Done() }))
	defer upstream.Close()
	router, err := NewRouter(&config.Config{Nodes: []config.NodeEntry{{Name: "n", Model: "m", APIBase: upstream.URL + "/v1"}}})
	if err != nil {
		t.Fatal(err)
	}
	router.DiscoverEngines(context.Background())
	if hits.Load() != 0 {
		t.Fatal("default configuration probed upstream")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	start := time.Now()
	p := DiscoverBackendEngine(ctx, nil, Backend{BaseURL: upstream.URL + "/v1"})
	if time.Since(start) > time.Second || p.Engine != "unknown" {
		t.Fatal("discovery ignored cancellation")
	}
}

func TestEngineAdvertisedFieldsAndOverrides(t *testing.T) {
	b := Backend{}
	configureBackendEngine(&b, "auto", config.EngineCompatibility{Enabled: engineBool(true)})
	b.engineState.profile = &EngineProfile{Engine: "vllm", Source: "models", Fields: map[string]map[string]bool{"/v1/chat/completions": {"guided_regex": true}}}
	body := []byte(`{"regex":"yes"}`)
	var payload map[string]any
	json.Unmarshal(body, &payload)
	result := BuildBackendBodyWithWarnings(payload, b, body, false)
	if result.Err != nil || string(result.Body) != `{"guided_regex":"yes"}` {
		t.Fatalf("legacy field not respected: %+v", result)
	}
	b.engineState.profile = &EngineProfile{Engine: "vllm", Source: "models"}
	result = BuildBackendBodyWithWarnings(payload, b, body, false)
	if result.Err != nil || string(result.Body) != string(body) || len(result.Warnings) != 1 {
		t.Fatalf("unadvertised mapping: %+v", result)
	}
	b.Engine = "sglang"
	b.Compatibility.ThinkingBudget = engineBool(false)
	b.Compatibility.DPSize = engineInt(3)
	snapshot := b.engineSnapshot()
	if snapshot.EngineProfile.Engine != "sglang" || snapshot.EngineProfile.Source != "configured" || snapshot.EngineProfile.ThinkingBudget == nil || *snapshot.EngineProfile.ThinkingBudget || snapshot.EngineProfile.DPSize != 3 {
		t.Fatalf("manual overrides lost: %+v", snapshot.EngineProfile)
	}
	result = BuildBackendBodyWithWarnings(map[string]any{"structured_outputs": map[string]any{"regex": "yes"}}, snapshot, nil, false)
	if result.Err != nil || string(result.Body) != `{"regex":"yes"}` {
		t.Fatalf("manual adapter: %+v", result)
	}
}

func TestEngineCacheIsolatesCapabilityRefresh(t *testing.T) {
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"choices":[]}`)
	}))
	defer upstream.Close()
	router, err := NewRouter(&config.Config{Nodes: []config.NodeEntry{{Name: "n", Model: "m", APIBase: upstream.URL + "/v1", Engine: "auto", Compatibility: config.EngineCompatibility{Enabled: engineBool(true)}}}, ProxySettings: config.ProxySettings{InferenceCache: config.InferenceCacheConfig{Mode: "read_write", MaxEntries: 10}}})
	if err != nil {
		t.Fatal(err)
	}
	defer router.InferenceCache.Close()
	state := router.Backends["n"].engineState
	setProfile := func(supported bool) {
		state.mu.Lock()
		defer state.mu.Unlock()
		state.profile = &EngineProfile{Engine: "sglang", Source: "models", CheckedAt: time.Now(), ThinkingBudget: engineBool(supported), Fields: map[string]map[string]bool{"/v1/chat/completions": {"custom_params": true}}}
	}
	body := []byte(`{"model":"m","messages":[{"role":"user","content":"hi"}],"custom_params":{"thinking_budget":4}}`)
	var payload map[string]any
	json.Unmarshal(body, &payload)
	forward := func() {
		t.Helper()
		result, err := router.ForwardWithOptions(context.Background(), "m", "/v1/chat/completions", "", body, payload, false, nil, ForwardOptions{Cacheable: true})
		if err != nil {
			t.Fatal(err)
		}
		result.Response.Body.Close()
	}
	setProfile(false)
	forward()
	forward()
	if calls.Load() != 1 {
		t.Fatal("identical request missed cache")
	}
	setProfile(true)
	forward()
	if calls.Load() != 2 {
		t.Fatal("capability change reused stale answer")
	}
	setProfile(true)
	forward()
	if calls.Load() != 2 {
		t.Fatal("unchanged profile refresh invalidated cache")
	}
}

func TestEngineThinkingFlagsDoNotProveEnforcement(t *testing.T) {
	for _, flags := range []string{
		`"enable_strict_thinking":true,"enable_custom_logit_processor":false,"reasoning_parser":"qwen3"`,
		`"enable_strict_thinking":false,"enable_custom_logit_processor":true`,
	} {
		t.Run(flags, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/v1/models":
					io.WriteString(w, `{"data":[{"id":"m","owned_by":"sglang"}]}`)
				case "/server_info":
					fmt.Fprintf(w, `{"version":"test",%s}`, flags)
				default:
					http.NotFound(w, r)
				}
			}))
			defer upstream.Close()
			p := DiscoverBackendEngine(context.Background(), nil, Backend{BaseURL: upstream.URL + "/v1", TargetModel: "m"})
			if p.Engine != "sglang" || p.ThinkingBudget != nil || p.Note == "" {
				t.Fatalf("flags incorrectly established enforcement: %+v", p)
			}
		})
	}
}
