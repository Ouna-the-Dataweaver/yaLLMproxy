package proxy

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/theio/yallmp-proxy/internal/config"
)

const engineRefreshInterval = 5 * time.Minute

// EngineProfile is an immutable snapshot. Discovery stores only selected public
// capability metadata, never the upstream's full launch configuration.
type EngineProfile struct {
	Engine         string                     `json:"engine"`
	Version        string                     `json:"version,omitempty"`
	Source         string                     `json:"source"`
	CheckedAt      time.Time                  `json:"checked_at,omitempty"`
	ThinkingBudget *bool                      `json:"thinking_budget"`
	DPSize         int                        `json:"dp_size,omitempty"`
	Fields         map[string]map[string]bool `json:"request_fields,omitempty"`
	Note           string                     `json:"note,omitempty"`
}

type engineState struct {
	mu        sync.Mutex
	profile   *EngineProfile
	probing   bool
	nextProbe time.Time
}

func configureBackendEngine(b *Backend, engine string, c config.EngineCompatibility) error {
	engine = strings.TrimSpace(engine)
	if err := config.ValidateEngineCompatibility(engine, c); err != nil {
		return err
	}
	if engine == "" {
		if c.IsEnabled() {
			engine = "auto"
		} else {
			engine = "generic"
		}
	}
	b.Engine, b.Compatibility = engine, c
	b.engineState = &engineState{}
	return nil
}

func (b Backend) engineSnapshot() Backend {
	if b.EngineProfile != nil {
		return b
	}
	if b.engineState != nil {
		b.engineState.mu.Lock()
		b.EngineProfile = b.engineState.profile
		b.engineState.mu.Unlock()
	}
	p := EngineProfile{Engine: "unknown", Source: "undetected"}
	if b.EngineProfile != nil {
		p = *b.EngineProfile
	}
	if b.Engine == "vllm" || b.Engine == "sglang" || b.Engine == "generic" {
		// A manual engine always wins, but metadata from a different engine must
		// never be used after a deployment changes or a configuration is incorrect.
		if p.Engine != b.Engine {
			p = EngineProfile{Engine: b.Engine}
		}
		p.Engine, p.Source = b.Engine, "configured"
	}
	if b.Compatibility.ThinkingBudget != nil {
		p.ThinkingBudget = b.Compatibility.ThinkingBudget
	}
	if b.Compatibility.DPSize != nil {
		p.DPSize = *b.Compatibility.DPSize
	}
	b.EngineProfile = &p
	return b
}

func (p EngineProfile) supports(path, field string) bool {
	if fields, ok := p.Fields[path]; ok {
		return fields[field]
	}
	// Explicit engine configuration is also the escape hatch for gateways that
	// hide OpenAPI. Auto-detected engines require observed field support.
	if p.Source != "configured" {
		return false
	}
	switch p.Engine {
	case "vllm":
		return field == "structured_outputs" || field == "thinking_token_budget" || field == "response_format"
	case "sglang":
		return field == "regex" || field == "custom_params" || field == "routed_dp_rank" || (path == "/v1/completions" && field == "json_schema") || (path == "/v1/chat/completions" && field == "response_format")
	}
	return false
}

// DiscoverBackendEngine performs bounded, read-only probes. A /version response
// alone does not identify vLLM: unrelated servers also expose that endpoint.
func DiscoverBackendEngine(ctx context.Context, client *http.Client, b Backend) EngineProfile {
	p := EngineProfile{Engine: "unknown", Source: "undetected", CheckedAt: time.Now().UTC()}
	if client == nil {
		client = http.DefaultClient
	}
	probeClient := *client
	probeClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	probe := func(address string) map[string]any {
		probeCtx, done := context.WithTimeout(ctx, 2*time.Second)
		defer done()
		req, err := http.NewRequestWithContext(probeCtx, http.MethodGet, address, nil)
		if err != nil {
			return nil
		}
		req.Header = BuildOutboundHeaders(http.Header{"Accept": {"application/json"}}, b.APIKey, false, "openai", "")
		resp, err := probeClient.Do(req)
		if err != nil {
			return nil
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return nil
		}
		data, err := io.ReadAll(io.LimitReader(resp.Body, 4*1024*1024+1))
		if err != nil || len(data) > 4*1024*1024 {
			return nil
		}
		var obj map[string]any
		if json.Unmarshal(data, &obj) != nil {
			return nil
		}
		return obj
	}
	models := probe(b.BuildURL("/models", ""))
	if entries, ok := models["data"].([]any); ok {
		owner := ""
		for _, entry := range entries {
			m, _ := entry.(map[string]any)
			if b.TargetModel != "" && m["id"] != b.TargetModel {
				continue
			}
			candidate, _ := m["owned_by"].(string)
			if candidate != "vllm" && candidate != "sglang" {
				owner = ""
				break
			}
			if owner != "" && owner != candidate {
				owner = ""
				break
			}
			owner = candidate
		}
		if owner != "" {
			p.Engine, p.Source = owner, "models"
		}
	}
	base := b.Compatibility.ProbeBase
	if base == "" {
		base = strings.TrimSuffix(strings.TrimRight(b.BaseURL, "/"), "/v1")
	}
	// Keep deployment path prefixes (e.g. /inference/node-a); never guess the
	// origin root or send credentials to a redirected host.
	u, err := url.Parse(base)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return p
	}
	u.RawQuery, u.Fragment = "", ""
	base = strings.TrimRight(u.String(), "/")
	schema := probe(base + "/openapi.json")
	components, _ := schema["components"].(map[string]any)
	schemas, _ := components["schemas"].(map[string]any)
	for path, name := range map[string]string{"/v1/chat/completions": "ChatCompletionRequest", "/v1/completions": "CompletionRequest"} {
		if obj, ok := schemas[name].(map[string]any); ok {
			if props, ok := obj["properties"].(map[string]any); ok {
				if p.Fields == nil {
					p.Fields = map[string]map[string]bool{}
				}
				p.Fields[path] = map[string]bool{}
				for name := range props {
					p.Fields[path][name] = true
				}
			}
		}
	}
	if p.Engine == "unknown" {
		fields := p.Fields["/v1/chat/completions"]
		if fields["custom_params"] && fields["regex"] && fields["routed_dp_rank"] {
			p.Engine, p.Source = "sglang", "openapi"
		}
		if fields["structured_outputs"] && fields["thinking_token_budget"] && !fields["custom_params"] {
			p.Engine, p.Source = "vllm", "openapi"
		}
	}
	switch p.Engine {
	case "sglang":
		info := probe(base + "/server_info")
		if info == nil {
			info = probe(base + "/get_server_info")
		}
		p.Version, _ = info["version"].(string)
		args := info
		if nested, ok := info["server_args"].(map[string]any); ok {
			args = nested
		}
		if n, ok := args["dp_size"].(float64); ok && n > 0 {
			p.DPSize = int(n)
		}
		strict, strictKnown := args["enable_strict_thinking"].(bool)
		custom, customKnown := args["enable_custom_logit_processor"].(bool)
		// Strict thinking also depends on parser token boundaries, environment
		// defaults, and tokenizer initialization. Flags alone cannot establish
		// enforcement; only an operator's verified override asserts support.
		if strict {
			p.Note = "Strict thinking is enabled; model/parser budget enforcement is unverified"
		} else if custom {
			p.Note = "Budget enforcement requires a matching per-request custom logit processor"
		}
		if strictKnown && customKnown && !strict && !custom {
			no := false
			p.ThinkingBudget = &no
		}
	case "vllm":
		info := probe(base + "/version")
		p.Version, _ = info["version"].(string)
		// Schema support does not prove that the reasoning parser is configured.
	}
	if p.Engine == "unknown" {
		p.Note = "Engine could not be identified; request fields are passed through"
	}
	return p
}

// DiscoverEngines refreshes due nodes without blocking request handling. Calls
// are safe to overlap; there are at most four probes in flight in this pass.
func (r *Router) DiscoverEngines(ctx context.Context) {
	r.mu.Lock()
	backends := make([]Backend, 0, len(r.Backends))
	for _, b := range r.Backends {
		backends = append(backends, b)
	}
	client := r.Client
	r.mu.Unlock()
	sem := make(chan struct{}, 4)
	var wg sync.WaitGroup
	for _, b := range backends {
		if b.Engine == "generic" || b.Engine == "" || (b.APIType != "openai" && b.APIType != "") || b.engineState == nil {
			continue
		}
		state := b.engineState
		state.mu.Lock()
		due := !state.probing && !time.Now().Before(state.nextProbe)
		if due {
			state.probing = true
		}
		state.mu.Unlock()
		if !due {
			continue
		}
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			state.mu.Lock()
			state.probing = false
			state.mu.Unlock()
			wg.Wait()
			return
		}
		wg.Add(1)
		go func(b Backend) {
			defer wg.Done()
			defer func() { <-sem }()
			p := DiscoverBackendEngine(ctx, client, b)
			state := b.engineState
			state.mu.Lock()
			defer state.mu.Unlock()
			state.probing = false
			if ctx.Err() != nil {
				return
			}
			state.profile = &p
			state.nextProbe = time.Now().Add(engineRefreshInterval)
		}(b)
	}
	wg.Wait()
}

type BackendEngineStatus struct {
	Name                 string        `json:"name"`
	CompatibilityEnabled bool          `json:"compatibility_enabled"`
	Profile              EngineProfile `json:"profile"`
}

func (r *Router) EngineSnapshot() []BackendEngineStatus {
	r.mu.Lock()
	defer r.mu.Unlock()
	result := make([]BackendEngineStatus, 0, len(r.Backends))
	for _, b := range r.Backends {
		b = b.engineSnapshot()
		result = append(result, BackendEngineStatus{Name: b.Name, CompatibilityEnabled: b.Compatibility.IsEnabled(), Profile: *b.EngineProfile})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return result
}

// CompatibilityError is local request validation, not an upstream failure.
// Unsupported capabilities may fall back to another node; conflicts may not.
type CompatibilityError struct {
	Message     string
	Unsupported bool
}

func (e *CompatibilityError) Error() string { return e.Message }
func compatibilityConflict(format string, args ...any) error {
	return &CompatibilityError{Message: fmt.Sprintf(format, args...)}
}

// EngineCacheScope isolates cached answers across capability/version changes.
// Check timestamps are excluded so an unchanged refresh retains cache hits.
func (b Backend) EngineCacheScope(scope string) string {
	if !b.Compatibility.IsEnabled() {
		return scope
	}
	b = b.engineSnapshot()
	profile := *b.EngineProfile
	profile.CheckedAt = time.Time{}
	profile.Note = ""
	data, _ := json.Marshal(struct {
		Scope   string
		Profile EngineProfile
		Rank    *int
		Strict  bool
	}{scope, profile, b.Compatibility.DPRank, b.Compatibility.IsStrict()})
	return fmt.Sprintf("engine:%x", sha256.Sum256(data))
}
