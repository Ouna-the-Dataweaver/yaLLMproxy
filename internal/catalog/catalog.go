package catalog

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/theio/yallmp-proxy/internal/config"
	"github.com/theio/yallmp-proxy/internal/proxy"
)

const (
	SourceConfig = "config"
	SourceProbe  = "probe"

	StatusConfigured  = "configured"
	StatusAvailable   = "available"
	StatusUnavailable = "unavailable"
	StatusStale       = "stale"

	ErrorNone            = ""
	ErrorAuth            = "auth"
	ErrorRateLimited     = "rate_limited"
	ErrorStatus          = "status_error"
	ErrorTimeout         = "timeout"
	ErrorNetwork         = "network"
	ErrorInvalidResponse = "invalid_response"
	ErrorUnsupported     = "unsupported_api_type"
)

const (
	defaultProbeInterval = 5 * time.Minute
	defaultProbeTimeout  = 5 * time.Second
	defaultBackoff       = time.Minute
	defaultCacheTTL      = 30 * time.Minute
	maxModelsPerProbe    = 10000
	maxResponseBytes     = 2 << 20
)

type Catalog struct {
	mu       sync.RWMutex
	settings Settings
	entries  map[string]*ModelEntry
	nodes    map[string]NodeSource
	client   *http.Client
	now      func() time.Time
}

type Settings struct {
	Enabled        bool
	Interval       time.Duration
	Timeout        time.Duration
	FailureBackoff time.Duration
	CacheTTL       time.Duration
	ProbeNodes     map[string]bool
	ProbeProviders map[string]bool
}

type NodeSource struct {
	Name             string
	Provider         string
	Backend          proxy.Backend
	ConfiguredModels []string
	ProbeEnabled     bool
}

type ModelEntry struct {
	Model     string          `json:"model"`
	Providers []ProviderEntry `json:"providers"`
}

type ProviderEntry struct {
	Provider string      `json:"provider,omitempty"`
	Nodes    []NodeEntry `json:"nodes"`
}

type NodeEntry struct {
	Node            string    `json:"node"`
	Status          string    `json:"status"`
	Source          string    `json:"source"`
	LastSync        time.Time `json:"last_sync,omitempty"`
	NextSync        time.Time `json:"next_sync,omitempty"`
	ErrorClass      string    `json:"error_class,omitempty"`
	ConfiguredModel string    `json:"configured_model,omitempty"`
	TargetModel     string    `json:"target_model,omitempty"`
}

type Snapshot struct {
	Enabled      bool         `json:"enabled"`
	ProbeEnabled bool         `json:"probe_enabled"`
	GeneratedAt  time.Time    `json:"generated_at"`
	Models       []ModelEntry `json:"models"`
}

type probeState struct {
	LastSync   time.Time
	NextSync   time.Time
	ErrorClass string
	Models     map[string]bool
}

type openAIModelsResponse struct {
	Data []struct {
		ID string `json:"id"`
	} `json:"data"`
}

func New(cfg *config.Config) *Catalog {
	settings := SettingsFromConfig(cfg)
	nodes := sourcesFromConfig(cfg, settings)
	c := &Catalog{
		settings: settings,
		entries:  map[string]*ModelEntry{},
		nodes:    nodes,
		client:   http.DefaultClient,
		now:      time.Now,
	}
	c.seedConfigLocked()
	return c
}

func SettingsFromConfig(cfg *config.Config) Settings {
	var raw config.ModelCatalogConfig
	if cfg != nil {
		raw = cfg.ProxySettings.ModelCatalog
	}
	settings := Settings{
		Enabled:        raw.Enabled,
		Interval:       secondsDuration(raw.ProbeIntervalSeconds, defaultProbeInterval),
		Timeout:        secondsDuration(raw.ProbeTimeoutSeconds, defaultProbeTimeout),
		FailureBackoff: secondsDuration(raw.FailureBackoffSeconds, defaultBackoff),
		CacheTTL:       secondsDuration(raw.CacheTTLSeconds, defaultCacheTTL),
		ProbeNodes:     stringSet(raw.ProbeNodes),
		ProbeProviders: stringSet(raw.ProbeProviders),
	}
	return settings
}

func (c *Catalog) SetHTTPClient(client *http.Client) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if client == nil {
		client = http.DefaultClient
	}
	c.client = client
}

func (c *Catalog) SetNow(now func() time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if now == nil {
		now = time.Now
	}
	c.now = now
}

func (c *Catalog) Snapshot() Snapshot {
	c.mu.RLock()
	defer c.mu.RUnlock()
	now := c.now()
	models := make([]ModelEntry, 0, len(c.entries))
	for _, entry := range c.entries {
		models = append(models, cloneModelEntry(*entry, now, c.settings.CacheTTL))
	}
	sort.Slice(models, func(i, j int) bool {
		return models[i].Model < models[j].Model
	})
	return Snapshot{
		Enabled:      c.settings.Enabled,
		ProbeEnabled: c.probesConfiguredLocked(),
		GeneratedAt:  now,
		Models:       models,
	}
}

func (c *Catalog) RunOnce(ctx context.Context) {
	if c == nil {
		return
	}
	c.mu.RLock()
	if !c.settings.Enabled || !c.probesConfiguredLocked() {
		c.mu.RUnlock()
		return
	}
	sources := make([]NodeSource, 0, len(c.nodes))
	now := c.now()
	for _, source := range c.nodes {
		if !source.ProbeEnabled {
			continue
		}
		if !sourceDueLocked(c.entries, source.Name, now) {
			continue
		}
		sources = append(sources, source)
	}
	timeout := c.settings.Timeout
	client := c.client
	c.mu.RUnlock()

	if timeout <= 0 {
		timeout = defaultProbeTimeout
	}
	if client == nil {
		client = http.DefaultClient
	}
	for _, source := range sources {
		select {
		case <-ctx.Done():
			return
		default:
		}
		result := probeNode(ctx, client, source, timeout)
		c.mergeProbeResult(source, result)
	}
}

func (c *Catalog) Start(ctx context.Context) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		if c == nil {
			return
		}
		for {
			c.RunOnce(ctx)
			interval := c.interval()
			timer := time.NewTimer(interval)
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
		}
	}()
	return done
}

func (c *Catalog) Interval() time.Duration {
	return c.interval()
}

func (c *Catalog) interval() time.Duration {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.settings.Interval > 0 {
		return c.settings.Interval
	}
	return defaultProbeInterval
}

func (c *Catalog) mergeProbeResult(source NodeSource, state probeState) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if state.NextSync.IsZero() {
		state.NextSync = state.LastSync.Add(c.settings.Interval)
		if state.ErrorClass != "" {
			state.NextSync = state.LastSync.Add(c.settings.FailureBackoff)
		}
	}
	for _, model := range source.ConfiguredModels {
		if strings.TrimSpace(model) == "" {
			continue
		}
		if state.ErrorClass == "" {
			status := StatusUnavailable
			if configuredModelProbeMatched(model, source, state.Models) {
				status = StatusAvailable
			}
			c.upsertNodeLocked(model, source, NodeEntry{
				Node:            source.Name,
				Status:          status,
				Source:          SourceProbe,
				LastSync:        state.LastSync,
				NextSync:        state.NextSync,
				ConfiguredModel: model,
				TargetModel:     source.Backend.TargetModel,
			})
		}
	}
	for model := range state.Models {
		c.upsertNodeLocked(model, source, NodeEntry{
			Node:            source.Name,
			Status:          StatusAvailable,
			Source:          SourceProbe,
			LastSync:        state.LastSync,
			NextSync:        state.NextSync,
			ConfiguredModel: firstConfiguredModel(source.ConfiguredModels),
			TargetModel:     source.Backend.TargetModel,
		})
	}
	if state.ErrorClass != "" {
		for _, model := range source.ConfiguredModels {
			c.upsertNodeLocked(model, source, NodeEntry{
				Node:            source.Name,
				Status:          StatusUnavailable,
				Source:          SourceProbe,
				LastSync:        state.LastSync,
				NextSync:        state.NextSync,
				ErrorClass:      state.ErrorClass,
				ConfiguredModel: model,
				TargetModel:     source.Backend.TargetModel,
			})
		}
	}
}

func (c *Catalog) seedConfigLocked() {
	for _, source := range c.nodes {
		for _, model := range source.ConfiguredModels {
			c.upsertNodeLocked(model, source, NodeEntry{
				Node:            source.Name,
				Status:          StatusConfigured,
				Source:          SourceConfig,
				ConfiguredModel: model,
				TargetModel:     source.Backend.TargetModel,
			})
		}
	}
}

func (c *Catalog) upsertNodeLocked(model string, source NodeSource, node NodeEntry) {
	model = sanitizeName(model)
	if model == "" || source.Name == "" {
		return
	}
	providerName := sanitizeName(source.Provider)
	entry := c.entries[model]
	if entry == nil {
		entry = &ModelEntry{Model: model}
		c.entries[model] = entry
	}
	providerIndex := -1
	for i := range entry.Providers {
		if entry.Providers[i].Provider == providerName {
			providerIndex = i
			break
		}
	}
	if providerIndex < 0 {
		entry.Providers = append(entry.Providers, ProviderEntry{Provider: providerName})
		providerIndex = len(entry.Providers) - 1
	}
	node.Node = sanitizeName(node.Node)
	node.ConfiguredModel = sanitizeName(node.ConfiguredModel)
	node.TargetModel = sanitizeName(node.TargetModel)
	node.ErrorClass = sanitizeErrorClass(node.ErrorClass)
	nodes := entry.Providers[providerIndex].Nodes
	for i := range nodes {
		if nodes[i].Node == node.Node {
			nodes[i] = mergeNode(nodes[i], node)
			entry.Providers[providerIndex].Nodes = nodes
			sortProvider(entry)
			return
		}
	}
	entry.Providers[providerIndex].Nodes = append(nodes, node)
	sortProvider(entry)
}

func probeNode(ctx context.Context, client *http.Client, source NodeSource, timeout time.Duration) probeState {
	now := time.Now()
	state := probeState{LastSync: now, Models: map[string]bool{}}
	if source.Backend.APIType != "" && source.Backend.APIType != "openai" {
		state.ErrorClass = ErrorUnsupported
		return state
	}
	probeCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(probeCtx, http.MethodGet, source.Backend.BuildURL("/models", ""), nil)
	if err != nil {
		state.ErrorClass = ErrorInvalidResponse
		return state
	}
	req.Header = proxy.BuildOutboundHeaders(http.Header{}, source.Backend.APIKey, false, source.Backend.APIType, source.Backend.AnthropicVersion)
	resp, err := client.Do(req)
	if err != nil {
		state.ErrorClass = classifyError(err)
		return state
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		state.ErrorClass = classifyStatus(resp.StatusCode)
		io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		return state
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		state.ErrorClass = ErrorInvalidResponse
		return state
	}
	var decoded openAIModelsResponse
	if err := json.Unmarshal(body, &decoded); err != nil {
		state.ErrorClass = ErrorInvalidResponse
		return state
	}
	for _, item := range decoded.Data {
		model := sanitizeName(item.ID)
		if model == "" {
			continue
		}
		state.Models[model] = true
		if len(state.Models) >= maxModelsPerProbe {
			break
		}
	}
	return state
}

func sourcesFromConfig(cfg *config.Config, settings Settings) map[string]NodeSource {
	out := map[string]NodeSource{}
	if cfg == nil {
		return out
	}
	providers := map[string]config.ProviderEntry{}
	for _, provider := range cfg.Providers {
		providers[provider.Name] = provider
	}
	nodeModels := map[string][]string{}
	for _, model := range cfg.Models {
		name := model.EffectiveName()
		for _, node := range model.Nodes {
			nodeModels[node] = appendUnique(nodeModels[node], name)
		}
	}
	for _, entry := range cfg.ModelList {
		backend, ok, err := proxy.BackendFromModelEntry(entry)
		if err != nil || !ok || backend.Passthrough {
			continue
		}
		model := sanitizeName(entry.ModelName)
		backend.Name = model
		out[model] = NodeSource{
			Name:             model,
			Backend:          backend,
			ConfiguredModels: []string{model},
			ProbeEnabled:     settings.nodeOptedIn(model, ""),
		}
	}
	for _, node := range cfg.Nodes {
		provider := providers[node.Provider]
		name := sanitizeName(node.Name)
		if name == "" {
			continue
		}
		apiBase := firstString(node.APIBase, provider.APIBase)
		if apiBase == "" {
			continue
		}
		apiType := firstString(node.APIType, provider.APIType, "openai")
		backend := proxy.Backend{
			Name:             name,
			BaseURL:          apiBase,
			APIKey:           firstString(node.APIKey, provider.APIKey),
			Timeout:          proxy.ParseTimeout(node.RequestTimeout),
			TargetModel:      firstString(node.TargetModel, node.Model),
			APIType:          apiType,
			AnthropicVersion: firstString(node.AnthropicVersion, provider.AnthropicVersion),
			Passthrough:      apiType == "passthrough",
		}
		if backend.Passthrough {
			continue
		}
		models := nodeModels[name]
		if len(models) == 0 {
			models = append(models, firstString(node.Model, node.TargetModel, name))
		}
		out[name] = NodeSource{
			Name:             name,
			Provider:         sanitizeName(node.Provider),
			Backend:          backend,
			ConfiguredModels: sanitizeNames(models),
			ProbeEnabled:     settings.nodeOptedIn(name, node.Provider),
		}
	}
	return out
}

func (s Settings) nodeOptedIn(nodeName, providerName string) bool {
	if len(s.ProbeNodes) == 0 && len(s.ProbeProviders) == 0 {
		return false
	}
	return s.ProbeNodes[nodeName] || (providerName != "" && s.ProbeProviders[providerName])
}

func (c *Catalog) probesConfiguredLocked() bool {
	for _, source := range c.nodes {
		if source.ProbeEnabled {
			return true
		}
	}
	return false
}

func sourceDueLocked(entries map[string]*ModelEntry, nodeName string, now time.Time) bool {
	for _, entry := range entries {
		for _, provider := range entry.Providers {
			for _, node := range provider.Nodes {
				if node.Node == nodeName && node.Source == SourceProbe && node.NextSync.After(now) {
					return false
				}
			}
		}
	}
	return true
}

func cloneModelEntry(entry ModelEntry, now time.Time, ttl time.Duration) ModelEntry {
	out := ModelEntry{Model: entry.Model, Providers: make([]ProviderEntry, 0, len(entry.Providers))}
	for _, provider := range entry.Providers {
		p := ProviderEntry{Provider: provider.Provider, Nodes: make([]NodeEntry, 0, len(provider.Nodes))}
		for _, node := range provider.Nodes {
			copied := node
			if copied.Source == SourceProbe && ttl > 0 && !copied.LastSync.IsZero() && copied.LastSync.Add(ttl).Before(now) {
				copied.Status = StatusStale
			}
			p.Nodes = append(p.Nodes, copied)
		}
		out.Providers = append(out.Providers, p)
	}
	return out
}

func mergeNode(existing, incoming NodeEntry) NodeEntry {
	if incoming.Source == SourceConfig && existing.Source == SourceProbe {
		if incoming.ConfiguredModel != "" {
			existing.ConfiguredModel = incoming.ConfiguredModel
		}
		if incoming.TargetModel != "" {
			existing.TargetModel = incoming.TargetModel
		}
		return existing
	}
	return incoming
}

func sortProvider(entry *ModelEntry) {
	sort.Slice(entry.Providers, func(i, j int) bool {
		return entry.Providers[i].Provider < entry.Providers[j].Provider
	})
	for i := range entry.Providers {
		sort.Slice(entry.Providers[i].Nodes, func(a, b int) bool {
			return entry.Providers[i].Nodes[a].Node < entry.Providers[i].Nodes[b].Node
		})
	}
}

func classifyStatus(status int) string {
	switch status {
	case http.StatusUnauthorized, http.StatusForbidden:
		return ErrorAuth
	case http.StatusTooManyRequests:
		return ErrorRateLimited
	default:
		return ErrorStatus
	}
}

func classifyError(err error) string {
	if err == nil {
		return ErrorNone
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return ErrorTimeout
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return ErrorTimeout
	}
	return ErrorNetwork
}

func sanitizeErrorClass(value string) string {
	switch value {
	case ErrorNone, ErrorAuth, ErrorRateLimited, ErrorStatus, ErrorTimeout, ErrorNetwork, ErrorInvalidResponse, ErrorUnsupported:
		return value
	default:
		if value == "" {
			return ""
		}
		return ErrorStatus
	}
}

func sanitizeNames(values []string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		out = appendUnique(out, sanitizeName(value))
	}
	sort.Strings(out)
	return out
}

func sanitizeName(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	var b strings.Builder
	for _, r := range value {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || strings.ContainsRune("._:/+-", r) {
			b.WriteRune(r)
		}
		if b.Len() >= 256 {
			break
		}
	}
	return b.String()
}

func appendUnique(values []string, value string) []string {
	value = strings.TrimSpace(value)
	if value == "" {
		return values
	}
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}

func stringSet(values []string) map[string]bool {
	out := map[string]bool{}
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			out[trimmed] = true
		}
	}
	return out
}

func firstString(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func firstConfiguredModel(values []string) string {
	if len(values) == 0 {
		return ""
	}
	return sanitizeName(values[0])
}

func configuredModelProbeMatched(model string, source NodeSource, models map[string]bool) bool {
	if models[sanitizeName(model)] {
		return true
	}
	target := sanitizeName(source.Backend.TargetModel)
	return target != "" && models[target]
}

func secondsDuration(value float64, fallback time.Duration) time.Duration {
	if value <= 0 {
		return fallback
	}
	return time.Duration(value * float64(time.Second))
}

func (s Snapshot) Redacted() map[string]any {
	return map[string]any{
		"enabled":       s.Enabled,
		"probe_enabled": s.ProbeEnabled,
		"generated_at":  s.GeneratedAt,
		"models":        s.Models,
		"warnings":      Warnings(s),
	}
}

func (s Snapshot) String() string {
	return fmt.Sprintf("models=%d probe_enabled=%v", len(s.Models), s.ProbeEnabled)
}
