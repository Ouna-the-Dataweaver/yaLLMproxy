package server

import (
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/theio/yallmp-proxy/internal/config"
	"github.com/theio/yallmp-proxy/internal/proxy"
)

// Discovery is an explicit admin action. It never changes the selected models.
func (a *App) handleDiscoverProvider(w http.ResponseWriter, r *http.Request) {
	if !a.requireAdminPassword(w, r, "provider discovery") {
		return
	}
	var input struct {
		Name    string `json:"name"`
		APIBase string `json:"api_base"`
		APIKey  string `json:"api_key"`
		APIType string `json:"api_type"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&input); err != nil {
		writeError(w, http.StatusBadRequest, "Invalid provider discovery request", "invalid_request")
		return
	}
	provider := config.ProviderEntry{}
	for _, p := range a.Config.Providers {
		if p.Name == input.Name {
			provider = p
			break
		}
	}
	if input.APIBase != "" {
		provider.APIBase = input.APIBase
	}
	if input.APIKey != "" {
		provider.APIKey = input.APIKey
	}
	if input.APIType != "" {
		provider.APIType = input.APIType
	}
	if provider.APIType != "" && provider.APIType != "openai" {
		writeError(w, http.StatusBadRequest, "Discovery supports OpenAI-compatible providers; enter model names manually for this API type", "unsupported_discovery")
		return
	}
	base, err := url.Parse(provider.APIBase)
	if err != nil || base.Host == "" || (base.Scheme != "http" && base.Scheme != "https") || base.User != nil {
		writeError(w, http.StatusBadRequest, "Provider API base must be an http or https URL without user credentials", "invalid_api_base")
		return
	}
	backend := proxy.Backend{BaseURL: provider.APIBase}
	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, backend.BuildURL("/models", ""), nil)
	if err != nil {
		writeError(w, http.StatusBadRequest, "Invalid provider URL", "invalid_api_base")
		return
	}
	req.Header = proxy.BuildOutboundHeaders(http.Header{}, provider.APIKey, false, "openai", "")
	client := &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		writeError(w, http.StatusBadGateway, "Could not reach the provider model list", "discovery_failed")
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		writeError(w, http.StatusBadGateway, "Provider rejected the model-list request; check its URL, credentials, and discovery support", "discovery_failed")
		return
	}
	const maxBytes = 2 << 20
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBytes+1))
	var result struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err != nil || len(body) > maxBytes || json.Unmarshal(body, &result) != nil || result.Data == nil {
		writeError(w, http.StatusBadGateway, "Provider returned an invalid or oversized model list", "invalid_model_list")
		return
	}
	names := []string{}
	seen := map[string]bool{}
	for _, model := range result.Data {
		if model.ID != "" && !seen[model.ID] {
			names = append(names, model.ID)
			seen[model.ID] = true
		}
	}
	sort.Strings(names)
	writeJSON(w, http.StatusOK, map[string]any{"models": names})
}

// Read the effective pools without advancing round-robin or affinity state.
func (a *App) handleRoutingPools(w http.ResponseWriter, r *http.Request) {
	if !a.requireConfiguredAdminPassword(w, r, "routing pools") {
		return
	}
	cfg, err := a.Config.ExpandedRouting()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Could not expand routing", "invalid_routing")
		return
	}
	nodes := map[string]config.NodeEntry{}
	for _, n := range cfg.Nodes {
		nodes[n.Name] = n
	}
	health := map[string]proxy.BackendHealth{}
	for _, h := range a.Router.HealthSnapshot() {
		health[h.Name] = h
	}
	pools := []map[string]any{}
	for _, name := range a.Router.ListModelNames() {
		pool := a.Router.Models[name]
		locations := []map[string]any{}
		for _, nodeName := range pool.Nodes {
			backend, ok := a.Router.GetBackend(nodeName)
			if !ok {
				continue
			}
			n := nodes[nodeName]
			locations = append(locations, map[string]any{"name": nodeName, "provider": n.Provider, "upstream_model": backend.TargetModel, "generated": n.Generated, "healthy": health[nodeName].Healthy})
		}
		pools = append(pools, map[string]any{"name": name, "nodes": locations})
	}
	writeJSON(w, http.StatusOK, map[string]any{"pools": pools, "prefix_cache_affinity": a.Router.PrefixCacheAffinity})
}

func isProviderDiscoveryPath(path string) bool {
	return strings.TrimRight(path, "/") == "/admin/providers/discover"
}
