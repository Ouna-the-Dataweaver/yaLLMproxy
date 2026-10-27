package proxy

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/theio/yallmp-proxy/internal/config"
	"github.com/theio/yallmp-proxy/internal/gigachat"
	"github.com/theio/yallmp-proxy/internal/modules"
	"github.com/theio/yallmp-proxy/internal/observability"
)

var ErrEndpointNotSupported = errors.New("endpoint not supported by configured backends")
var ErrNodeAccessDenied = errors.New("api key not authorized for any node")

var (
	attemptURLPattern    = regexp.MustCompile(`https?://[^\s"']+`)
	attemptBearerPattern = regexp.MustCompile(`(?i)bearer\s+[A-Za-z0-9._~+/=-]+`)
	attemptSecretPattern = regexp.MustCompile(`(?i)(authorization|api[_-]?key|x-api-key|token|secret)\s*[:=]\s*[^,\s]+`)
)

const (
	defaultPrefixCacheAffinityMaxEntries = 4096
	prefixCacheAffinityMaxDepth          = 3
)

type Model struct {
	Name        string
	Nodes       []string
	NodeWeights map[string]int
	Balancing   string
	AllowedKeys any
	Limits      config.LimitsConfig
}

type ModelMetadata struct {
	Name              string
	SupportsReasoning bool
}

type Router struct {
	mu                        sync.Mutex
	Backends                  map[string]Backend
	PassthroughBackends       map[string]Backend
	Models                    map[string]Model
	Fallbacks                 map[string][]string
	NumRetries                int
	Client                    *http.Client
	ResponsePipeline          modules.Pipeline
	ResponseOverrides         map[string]modules.Pipeline
	Health                    map[string]BackendHealth
	FailureThreshold          int
	UnhealthyCooldown         time.Duration
	ActiveHealthChecks        bool
	HealthScoring             bool
	UnhealthyThreshold        float64
	HealthCheckPath           string
	HealthCheckInterval       time.Duration
	HealthCheckTimeout        time.Duration
	RecoverySuccessThreshold  int
	InferenceCache            *InferenceCache
	Observability             *observability.Provider
	PrefixCacheAffinity       bool
	ConcurrencyAwareSelection bool

	roundRobin         map[string]int
	weightedRoundRobin map[string]int
	sticky             map[string]string
	prefixCache        *prefixCacheAffinityTracker
	inflight           *inflightTracker
}

type BackendHealth struct {
	Name                     string    `json:"name"`
	Healthy                  bool      `json:"healthy"`
	State                    string    `json:"state,omitempty"`
	Score                    float64   `json:"score"`
	SuccessCount             int       `json:"success_count"`
	FailureCount             int       `json:"failure_count"`
	ConsecutiveFailures      int       `json:"consecutive_failures"`
	RecoverySuccesses        int       `json:"recovery_successes,omitempty"`
	RecoverySuccessThreshold int       `json:"recovery_success_threshold,omitempty"`
	LastStatus               int       `json:"last_status,omitempty"`
	LastError                string    `json:"-"`
	LastSuccess              time.Time `json:"last_success,omitempty"`
	LastFailure              time.Time `json:"last_failure,omitempty"`
	UnhealthyUntil           time.Time `json:"unhealthy_until,omitempty"`
	InflightRequests         int64     `json:"inflight_requests"`
}

type ForwardResult struct {
	Response         *http.Response
	BackendName      string
	SemanticCacheHit bool
	Warnings         []RequestWarning
	AttemptTrail     []UpstreamAttempt
	ParsedResponse   *ParsedResponseTrace
}

type UpstreamAttempt struct {
	AttemptIndex int    `json:"attempt_index"`
	BackendName  string `json:"backend_name"`
	RetryIndex   int    `json:"retry_index"`
	Status       int    `json:"status"`
	Retryable    bool   `json:"retryable"`
	Outcome      string `json:"outcome"`
	DurationMS   int64  `json:"duration_ms"`
	Error        string `json:"error,omitempty"`
}

type AttemptLimiter func(context.Context, Backend) (func(), error)
type BackendAvailability func(Backend) bool

type ParsedResponseTrace struct {
	RawUpstreamBody       []byte
	TransformedClientBody []byte
	ContentType           string
}

type ParsedStreamCapture struct {
	Raw         io.Writer
	Transformed io.Writer
}

type ForwardOptions struct {
	EndpointKind          string
	RoutePayload          map[string]any
	BeforeAttempt         AttemptLimiter
	BackendAvailable      BackendAvailability
	QuotaAvailability     BackendAvailability
	KeyID                 string
	Cacheable             bool
	CacheBypass           bool
	CacheScope            string
	CaptureParsedResponse bool
	ParsedStreamCapture   *ParsedStreamCapture
}

type RouteOptions struct {
	BackendAvailable  BackendAvailability
	QuotaAvailability BackendAvailability
}

type prefixCacheAffinityTracker struct {
	maxEntries int
	entries    map[prefixCacheAffinityKey]struct{}
	order      []prefixCacheAffinityKey
}

type prefixCacheAffinityKey struct {
	node string
	hash [sha256.Size]byte
}

type prefixCacheRequestPrefix struct {
	depth int
	hash  [sha256.Size]byte
}

type inflightTracker struct {
	mu       sync.RWMutex
	counters map[string]*atomic.Int64
}

func newInflightTracker() *inflightTracker {
	return &inflightTracker{counters: map[string]*atomic.Int64{}}
}

func (t *inflightTracker) counter(name string) *atomic.Int64 {
	if t == nil || name == "" {
		return nil
	}
	t.mu.RLock()
	counter := t.counters[name]
	t.mu.RUnlock()
	if counter != nil {
		return counter
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if counter = t.counters[name]; counter != nil {
		return counter
	}
	counter = &atomic.Int64{}
	t.counters[name] = counter
	return counter
}

func (t *inflightTracker) begin(name string) func() {
	counter := t.counter(strings.TrimSpace(name))
	if counter == nil {
		return func() {}
	}
	counter.Add(1)
	var once sync.Once
	return func() {
		once.Do(func() {
			counter.Add(-1)
		})
	}
}

func (t *inflightTracker) load(name string) int64 {
	counter := t.counter(strings.TrimSpace(name))
	if counter == nil {
		return 0
	}
	value := counter.Load()
	if value < 0 {
		return 0
	}
	return value
}

func newPrefixCacheAffinityTracker(maxEntries int) *prefixCacheAffinityTracker {
	if maxEntries <= 0 {
		maxEntries = defaultPrefixCacheAffinityMaxEntries
	}
	return &prefixCacheAffinityTracker{
		maxEntries: maxEntries,
		entries:    map[prefixCacheAffinityKey]struct{}{},
		order:      make([]prefixCacheAffinityKey, 0, maxEntries),
	}
}

func (t *prefixCacheAffinityTracker) observe(node string, prefixes []prefixCacheRequestPrefix) {
	if t == nil || node == "" || len(prefixes) == 0 {
		return
	}
	for _, prefix := range prefixes {
		key := prefixCacheAffinityKey{node: node, hash: prefix.hash}
		if _, ok := t.entries[key]; !ok {
			t.entries[key] = struct{}{}
			t.order = append(t.order, key)
		}
	}
	for len(t.order) > t.maxEntries {
		oldest := t.order[0]
		delete(t.entries, oldest)
		t.order = append([]prefixCacheAffinityKey(nil), t.order[1:]...)
	}
}

func (t *prefixCacheAffinityTracker) match(nodes []string, prefixes []prefixCacheRequestPrefix) (string, bool) {
	if t == nil || len(nodes) == 0 || len(prefixes) == 0 {
		return "", false
	}
	for depth := len(prefixes) - 1; depth >= 0; depth-- {
		hash := prefixes[depth].hash
		for _, node := range nodes {
			if _, ok := t.entries[prefixCacheAffinityKey{node: node, hash: hash}]; ok {
				return node, true
			}
		}
	}
	return "", false
}

func requestPrefixHashes(payload map[string]any) []prefixCacheRequestPrefix {
	if payload == nil {
		return nil
	}
	messages, ok := payload["messages"].([]any)
	if !ok || len(messages) == 0 {
		return nil
	}
	maxDepth := prefixCacheAffinityMaxDepth
	if len(messages) < maxDepth {
		maxDepth = len(messages)
	}
	hasher := sha256.New()
	out := make([]prefixCacheRequestPrefix, 0, maxDepth)
	for i := 0; i < maxDepth; i++ {
		raw, err := json.Marshal(messages[i])
		if err != nil {
			break
		}
		hasher.Write([]byte{0})
		hasher.Write(raw)
		var sum [sha256.Size]byte
		hasher.Sum(sum[:0])
		out = append(out, prefixCacheRequestPrefix{depth: i, hash: sum})
	}
	return out
}

func (r *Router) applyPrefixCacheAffinitySettings(settings config.PrefixCacheAffinitySettings) {
	if settings.Enabled != nil && !config.ParseBoolStrict(settings.Enabled) {
		r.PrefixCacheAffinity = false
		r.prefixCache = nil
		return
	}
	r.PrefixCacheAffinity = true
	maxEntries := settings.MaxEntries
	if maxEntries <= 0 {
		maxEntries = defaultPrefixCacheAffinityMaxEntries
	}
	r.prefixCache = newPrefixCacheAffinityTracker(maxEntries)
}

func (r *Router) applyConcurrencyAwareSelectionSettings(settings config.ConcurrencyAwareSelectionSettings) {
	r.ConcurrencyAwareSelection = config.ParseBoolStrict(settings.Enabled)
}

func (r *Router) recordPrefixAffinity(node string, payload map[string]any) {
	if r == nil || node == "" {
		return
	}
	prefixes := requestPrefixHashes(payload)
	if len(prefixes) == 0 {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.PrefixCacheAffinity || r.prefixCache == nil {
		return
	}
	r.prefixCache.observe(node, prefixes)
}

func NewRouter(cfg *config.Config) (*Router, error) {
	var err error
	cfg, err = cfg.ExpandedRouting()
	if err != nil {
		return nil, err
	}
	r := &Router{
		Backends:            map[string]Backend{},
		PassthroughBackends: map[string]Backend{},
		Models:              map[string]Model{},
		Fallbacks:           map[string][]string{},
		NumRetries:          1,
		ResponseOverrides:   map[string]modules.Pipeline{},
		Health:              map[string]BackendHealth{},
		FailureThreshold:    2,
		UnhealthyCooldown:   30 * time.Second,
		HealthCheckPath:     "/models",
		HealthCheckInterval: 15 * time.Second,
		HealthCheckTimeout:  2 * time.Second,
		roundRobin:          map[string]int{},
		weightedRoundRobin:  map[string]int{},
		sticky:              map[string]string{},
		inflight:            newInflightTracker(),
	}
	if cfg == nil {
		return nil, fmt.Errorf("config is nil")
	}
	r.NumRetries = parsePositiveInt(cfg.RouterSettings.NumRetries, 1)
	r.Fallbacks = parseFallbacks(cfg.RouterSettings.Fallbacks)
	r.applyHealthCheckSettings(cfg.RouterSettings)
	r.applyPrefixCacheAffinitySettings(cfg.RouterSettings.PrefixCacheAffinity)
	r.applyConcurrencyAwareSelectionSettings(cfg.RouterSettings.ConcurrencyAwareSelection)
	for _, issue := range config.ValidateInferenceCacheConfig(cfg.ProxySettings.InferenceCache) {
		if issue.Severity == "error" {
			return nil, fmt.Errorf("%s: %s", issue.Path, issue.Message)
		}
	}
	r.InferenceCache = NewInferenceCache(cfg.ProxySettings.InferenceCache)
	closeInferenceCache := func() {
		if r.InferenceCache != nil {
			_ = r.InferenceCache.Close()
		}
	}
	r.ResponsePipeline = BuildResponsePipelineFromConfig(cfg.ProxySettings.Modules, cfg.ProxySettings.Parsers, false)
	if len(cfg.ModelList) > 0 {
		if err := r.addLegacyModels(cfg.ModelList); err != nil {
			closeInferenceCache()
			return nil, err
		}
	}
	if len(cfg.Nodes) > 0 || len(cfg.Models) > 0 {
		if err := r.addNodeModels(cfg); err != nil {
			closeInferenceCache()
			return nil, err
		}
	}
	if len(r.Backends) == 0 && len(r.PassthroughBackends) == 0 {
		closeInferenceCache()
		return nil, fmt.Errorf("no backends found in config")
	}
	return r, nil
}

func (r *Router) addLegacyModels(entries []config.ModelEntry) error {
	for _, entry := range entries {
		backend, ok, err := BackendFromModelEntry(entry)
		if err != nil {
			return fmt.Errorf("model %q: %w", entry.ModelName, err)
		}
		if !ok {
			continue
		}
		if backend.Passthrough {
			r.PassthroughBackends[backend.Name] = backend
		} else {
			r.Backends[backend.Name] = backend
			r.Models[backend.Name] = Model{Name: backend.Name, Nodes: []string{backend.Name}, NodeWeights: map[string]int{backend.Name: 1}, Balancing: "round_robin", Limits: entry.Limits}
		}
		if len(entry.Modules) > 0 {
			r.ResponseOverrides[backend.Name] = BuildResponsePipelineFromConfig(entry.Modules, nil, true)
		}
	}
	return nil
}

func (r *Router) addNodeModels(cfg *config.Config) error {
	providers := map[string]config.ProviderEntry{}
	nodeWeights := map[string]int{}
	for _, provider := range cfg.Providers {
		if provider.Name != "" {
			providers[provider.Name] = provider
		}
	}
	for _, node := range cfg.Nodes {
		name := strings.TrimSpace(node.Name)
		if name == "" {
			continue
		}
		if node.Weight > 0 {
			nodeWeights[name] = node.Weight
		}
		provider := providers[node.Provider]
		apiBase := firstString(node.APIBase, provider.APIBase)
		if apiBase == "" {
			continue
		}
		apiType := firstString(node.APIType, provider.APIType, "openai")
		params := nodeBackendParams(provider, node, apiBase, apiType)
		if node.UpstreamModel != "" {
			params["target_model"] = node.UpstreamModel
		} else if node.TargetModel != "" {
			params["target_model"] = node.TargetModel
		}
		backend := Backend{
			Name:                 name,
			BaseURL:              apiBase,
			APIKey:               firstString(node.APIKey, provider.APIKey),
			Timeout:              ParseTimeout(firstConfiguredAny(node.RequestTimeout, provider.RequestTimeout)),
			TargetModel:          ExtractTargetModel(params, apiType),
			APIType:              apiType,
			AnthropicVersion:     firstString(node.AnthropicVersion, provider.AnthropicVersion),
			SupportsReasoning:    config.ParseBool(node.SupportsReasoning),
			SupportsResponsesAPI: config.ParseBool(firstConfiguredAny(node.SupportsResponsesAPI, provider.SupportsResponsesAPI)),
			HTTP2:                config.ParseBool(node.HTTP2),
			Passthrough:          apiType == "passthrough",
			Limits:               mergeLimits(provider.Limits, node.Limits),
			Endpoints:            effectiveEndpoints(provider.Endpoints, node.Endpoints),
			AllowedKeys:          node.AllowedKeys,
			Parameters:           parseParameterConfigs(mergeMaps(provider.Parameters, node.Parameters)),
			Modules:              node.Modules,
		}
		if len(provider.Models) > 0 {
			backend.RateLimitScope = fmt.Sprintf("provider:%d:%s:model:%s", len(provider.Name), provider.Name, backend.TargetModel)
		}
		if apiType == "gigachat" {
			gigachatConfig, err := gigachat.BuildConfig(params)
			if err != nil {
				return fmt.Errorf("node %q: %w", name, err)
			}
			backend.GigaChat = gigachat.NewBackend(gigachatConfig)
			backend.APIKey = gigachatConfig.APIKey
			backend.TargetModel = firstString(backend.TargetModel, gigachatConfig.ModelName)
		}
		if backend.Passthrough {
			r.PassthroughBackends[name] = backend
		} else {
			r.Backends[name] = backend
		}
		if len(node.Modules) > 0 {
			r.ResponseOverrides[name] = BuildResponsePipelineFromConfig(node.Modules, nil, true)
		}
	}
	for _, model := range cfg.Models {
		name := model.EffectiveName()
		if name == "" {
			continue
		}
		weights := map[string]int{}
		for _, node := range model.Nodes {
			if weight := nodeWeights[node]; weight > 0 {
				weights[node] = weight
			}
		}
		for node, weight := range model.NodeWeights {
			if weight > 0 {
				weights[node] = weight
			}
		}
		r.Models[name] = Model{Name: name, Nodes: slices.Clone(model.Nodes), NodeWeights: weights, Balancing: strings.TrimSpace(model.Balancing), AllowedKeys: model.AllowedKeys, Limits: model.Limits}
	}
	return nil
}

func (r *Router) ListModelNames() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	names := make([]string, 0, len(r.Models))
	for name := range r.Models {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}

func (r *Router) ListModelMetadata() []ModelMetadata {
	r.mu.Lock()
	defer r.mu.Unlock()
	names := make([]string, 0, len(r.Models))
	for name := range r.Models {
		names = append(names, name)
	}
	slices.Sort(names)
	out := make([]ModelMetadata, 0, len(names))
	for _, name := range names {
		out = append(out, ModelMetadata{Name: name, SupportsReasoning: r.modelSupportsReasoningLocked(name)})
	}
	return out
}

func (r *Router) modelSupportsReasoningLocked(name string) bool {
	if model, ok := r.Models[name]; ok && len(model.Nodes) > 0 {
		for _, node := range model.Nodes {
			if backend, ok := r.Backends[node]; ok && backend.SupportsReasoning {
				return true
			}
		}
	}
	if backend, ok := r.Backends[name]; ok && backend.SupportsReasoning {
		return true
	}
	return false
}

func (r *Router) ListPassthroughModelNames() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	names := make([]string, 0, len(r.PassthroughBackends))
	for name := range r.PassthroughBackends {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}

func (r *Router) GetPassthroughBackend(name string) (Backend, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	backend, ok := r.PassthroughBackends[name]
	return backend, ok
}

func (r *Router) GetBackend(name string) (Backend, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	backend, ok := r.Backends[name]
	return backend, ok
}

func (r *Router) GetSingleModelBackend(modelName string) (Backend, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if backend, ok := r.Backends[modelName]; ok {
		return backend, true
	}
	model, ok := r.Models[modelName]
	if !ok || len(model.Nodes) != 1 {
		return Backend{}, false
	}
	backend, ok := r.Backends[model.Nodes[0]]
	return backend, ok
}

// ModelBackendsMatch checks the entire possible route without selecting nodes.
// Native endpoint handling must agree across replicas and explicit fallbacks.
func (r *Router) ModelBackendsMatch(modelName string, matches func(Backend) bool) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	names := append([]string{modelName}, r.Fallbacks[modelName]...)
	found := false
	for _, name := range names {
		nodes := []string{name}
		if model, ok := r.Models[name]; ok {
			nodes = model.Nodes
		}
		for _, node := range nodes {
			backend, ok := r.Backends[node]
			if !ok {
				continue
			}
			found = true
			if !matches(backend) {
				return false
			}
		}
	}
	return found
}

func (r *Router) RegisterBackend(backend Backend, fallbacks []string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if backend.APIType == "" {
		backend.APIType = "openai"
	}
	backend.Passthrough = backend.APIType == "passthrough"
	target := r.Backends
	if backend.Passthrough {
		target = r.PassthroughBackends
		delete(r.Backends, backend.Name)
		delete(r.Models, backend.Name)
	} else {
		delete(r.PassthroughBackends, backend.Name)
		r.Models[backend.Name] = Model{Name: backend.Name, Nodes: []string{backend.Name}, NodeWeights: map[string]int{backend.Name: 1}, Balancing: "round_robin"}
	}
	_, replaced := target[backend.Name]
	target[backend.Name] = backend
	r.ensureHealthLocked(backend.Name)
	if fallbacks != nil {
		r.Fallbacks[backend.Name] = slices.Clone(fallbacks)
	}
	return replaced
}

func (r *Router) BuildRoute(modelName string, payload map[string]any) ([]Backend, error) {
	return r.BuildRouteForPath(modelName, "", payload)
}

func (r *Router) BuildRouteForPath(modelName string, path string, payload map[string]any) ([]Backend, error) {
	return r.BuildRouteForKey(modelName, path, payload, "")
}

func (r *Router) BuildRouteForKey(modelName string, path string, payload map[string]any, keyID string) ([]Backend, error) {
	return r.BuildRouteForKeyWithOptions(modelName, path, payload, keyID, RouteOptions{})
}

func (r *Router) BuildRouteForKeyWithOptions(modelName string, path string, payload map[string]any, keyID string, opts RouteOptions) ([]Backend, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var route []Backend
	seen := map[string]bool{}
	hadBackendCandidate := false
	hadEndpointCandidate := false
	endpoint := normalizeEndpoint(path)
	addNode := func(name string) {
		if name == "" || seen[name] {
			return
		}
		if backend, ok := r.Backends[name]; ok {
			hadBackendCandidate = true
			if !backendSupportsEndpoint(backend, endpoint) {
				return
			}
			hadEndpointCandidate = true
			if !backendAllowsKey(backend, keyID) {
				return
			}
			route = append(route, backend)
			seen[name] = true
		}
	}
	addModel := func(name string) {
		if model, ok := r.Models[name]; ok && len(model.Nodes) > 0 {
			// Hard eligibility filters must intersect before applying preferences.
			eligible := make([]string, 0, len(model.Nodes))
			for _, node := range model.Nodes {
				if b, ok := r.Backends[node]; ok {
					hadBackendCandidate = true
					if backendSupportsEndpoint(b, endpoint) {
						hadEndpointCandidate = true
						if backendAllowsKey(b, keyID) {
							eligible = append(eligible, node)
						}
					}
				}
			}
			if len(eligible) == 0 {
				return
			}
			if opts.BackendAvailable != nil {
				available := make([]string, 0, len(eligible))
				for _, node := range eligible {
					if opts.BackendAvailable(r.Backends[node]) {
						available = append(available, node)
					}
				}
				if len(available) > 0 {
					eligible = available
				}
			}
			model.Nodes = eligible
			first := r.pickNodeLocked(model, payload, endpoint, keyID, opts)
			addNode(first)
			// Advance balancing once per request, then retain every sibling for
			// failover before moving to an explicitly configured fallback model.
			start := slices.Index(eligible, first)
			for i := 1; i < len(eligible); i++ {
				addNode(eligible[(start+i)%len(eligible)])
			}
			return
		}
		addNode(name)
	}
	addModel(modelName)
	for _, fallback := range r.Fallbacks[modelName] {
		addModel(fallback)
	}
	if len(route) == 0 {
		if hadBackendCandidate && !hadEndpointCandidate && endpoint != "" {
			return nil, fmt.Errorf("%w: model %q endpoint %q", ErrEndpointNotSupported, modelName, endpoint)
		}
		if hadEndpointCandidate {
			return nil, fmt.Errorf("%w: model %q key %q", ErrNodeAccessDenied, modelName, keyID)
		}
		return nil, fmt.Errorf("model %q is not defined in config", modelName)
	}
	route = r.filterHealthyLocked(route)
	route = r.sortByHealthScoreLocked(route)
	route = r.filterQuotaAvailableLocked(route, opts.QuotaAvailability)
	if hint := stickyHint(payload); hint != "" {
		if existing := r.sticky[hint]; existing == "" || existing == route[0].Name || (opts.BackendAvailable == nil && opts.QuotaAvailability == nil) {
			r.sticky[hint] = route[0].Name
		}
	}
	return route, nil
}

func (r *Router) filterHealthyLocked(route []Backend) []Backend {
	if len(route) <= 1 {
		return route
	}
	filtered := make([]Backend, 0, len(route))
	for _, backend := range route {
		health := r.healthLocked(backend.Name)
		if r.routePreferredHealth(health) {
			filtered = append(filtered, backend)
		}
	}
	if len(filtered) == 0 {
		return route
	}
	return filtered
}

func (r *Router) sortByHealthScoreLocked(route []Backend) []Backend {
	if len(route) <= 1 || !r.HealthScoring {
		return route
	}
	type candidate struct {
		backend Backend
		health  BackendHealth
		index   int
	}
	candidates := make([]candidate, 0, len(route))
	filtered := make([]candidate, 0, len(route))
	for i, backend := range route {
		health := r.healthLocked(backend.Name)
		item := candidate{backend: backend, health: health, index: i}
		candidates = append(candidates, item)
		if r.UnhealthyThreshold <= 0 || health.Score >= r.UnhealthyThreshold || health.SuccessCount+health.FailureCount == 0 {
			filtered = append(filtered, item)
		}
	}
	if len(filtered) == 0 {
		filtered = candidates
	}
	slices.SortStableFunc(filtered, func(a, b candidate) int {
		if a.health.Score > b.health.Score {
			return -1
		}
		if a.health.Score < b.health.Score {
			return 1
		}
		if a.index < b.index {
			return -1
		}
		if a.index > b.index {
			return 1
		}
		return 0
	})
	out := make([]Backend, 0, len(filtered))
	for _, item := range filtered {
		out = append(out, item.backend)
	}
	return out
}

func (r *Router) filterQuotaAvailableLocked(route []Backend, quotaAvailable BackendAvailability) []Backend {
	if len(route) <= 1 || quotaAvailable == nil {
		return route
	}
	filtered := make([]Backend, 0, len(route))
	for _, backend := range route {
		if quotaAvailable(backend) {
			filtered = append(filtered, backend)
		}
	}
	if len(filtered) == 0 {
		return route
	}
	return filtered
}

func (r *Router) pickNodeLocked(model Model, payload map[string]any, endpoint string, keyID string, opts RouteOptions) string {
	nodes := model.Nodes
	if endpoint != "" {
		supported := make([]string, 0, len(nodes))
		for _, node := range nodes {
			if backend, ok := r.Backends[node]; ok && backendSupportsEndpoint(backend, endpoint) {
				supported = append(supported, node)
			}
		}
		if len(supported) > 0 {
			nodes = supported
		}
	}
	if strings.TrimSpace(keyID) != "" {
		allowed := make([]string, 0, len(nodes))
		for _, node := range nodes {
			if backend, ok := r.Backends[node]; ok && backendAllowsKey(backend, keyID) {
				allowed = append(allowed, node)
			}
		}
		if len(allowed) > 0 {
			nodes = allowed
		}
	}
	if len(nodes) == 1 {
		return nodes[0]
	}
	healthy := make([]string, 0, len(nodes))
	for _, node := range nodes {
		health := r.healthLocked(node)
		if r.routePreferredHealth(health) {
			healthy = append(healthy, node)
		}
	}
	if len(healthy) > 0 {
		nodes = healthy
	}
	if hint := stickyHint(payload); hint != "" {
		if node := r.sticky[hint]; node != "" && slices.Contains(nodes, node) {
			if r.nodeAvailableLocked(node, opts) {
				return node
			}
		}
	}
	if opts.BackendAvailable != nil {
		available := make([]string, 0, len(nodes))
		for _, node := range nodes {
			if r.backendAvailableLocked(node, opts.BackendAvailable) {
				available = append(available, node)
			}
		}
		if len(available) > 0 {
			nodes = available
		}
	}
	if opts.QuotaAvailability != nil {
		available := make([]string, 0, len(nodes))
		for _, node := range nodes {
			if r.backendAvailableLocked(node, opts.QuotaAvailability) {
				available = append(available, node)
			}
		}
		if len(available) > 0 {
			nodes = available
		}
	}
	if r.PrefixCacheAffinity && r.prefixCache != nil && len(nodes) > 1 {
		if prefixes := requestPrefixHashes(payload); len(prefixes) > 0 {
			if node, ok := r.prefixCache.match(nodes, prefixes); ok {
				return node
			}
		}
	}
	if node := r.pickLeastInflightNodeLocked(model, nodes); node != "" {
		return node
	}
	return r.pickRoundRobinNodeLocked(model, nodes)
}

func (r *Router) pickLeastInflightNodeLocked(model Model, nodes []string) string {
	if !r.ConcurrencyAwareSelection || len(nodes) <= 1 {
		return ""
	}
	minInflight := int64(0)
	tied := make([]string, 0, len(nodes))
	for i, node := range nodes {
		inflight := r.inflightCount(node)
		if i == 0 || inflight < minInflight {
			minInflight = inflight
			tied = tied[:0]
			tied = append(tied, node)
			continue
		}
		if inflight == minInflight {
			tied = append(tied, node)
		}
	}
	if len(tied) == 0 {
		return ""
	}
	if len(tied) == 1 {
		return tied[0]
	}
	return r.pickRoundRobinNodeLocked(model, tied)
}

func (r *Router) pickRoundRobinNodeLocked(model Model, nodes []string) string {
	if len(nodes) == 0 {
		return ""
	}
	if modelUsesWeightedRoundRobin(model) {
		weightedNodes := expandWeightedNodes(nodes, model.NodeWeights)
		if len(weightedNodes) > 0 {
			idx := r.weightedRoundRobin[model.Name] % len(weightedNodes)
			r.weightedRoundRobin[model.Name] = idx + 1
			return weightedNodes[idx]
		}
	}
	idx := r.roundRobin[model.Name] % len(nodes)
	r.roundRobin[model.Name] = idx + 1
	return nodes[idx]
}

func (r *Router) inflightCount(name string) int64 {
	if r == nil {
		return 0
	}
	return r.inflight.load(name)
}

func (r *Router) nodeAvailableLocked(name string, opts RouteOptions) bool {
	if opts.BackendAvailable != nil && !r.backendAvailableLocked(name, opts.BackendAvailable) {
		return false
	}
	if opts.QuotaAvailability != nil && !r.backendAvailableLocked(name, opts.QuotaAvailability) {
		return false
	}
	return true
}

func (r *Router) backendAvailableLocked(name string, backendAvailable BackendAvailability) bool {
	if backendAvailable == nil {
		return true
	}
	backend, ok := r.Backends[name]
	if !ok {
		return false
	}
	return backendAvailable(backend)
}

func modelUsesWeightedRoundRobin(model Model) bool {
	if len(model.NodeWeights) == 0 {
		return false
	}
	mode := strings.ToLower(strings.TrimSpace(model.Balancing))
	return mode == "" || mode == "weighted_round_robin" || mode == "weighted"
}

func expandWeightedNodes(nodes []string, weights map[string]int) []string {
	if len(nodes) == 0 || len(weights) == 0 {
		return nil
	}
	out := make([]string, 0, len(nodes))
	for _, node := range nodes {
		weight := weights[node]
		if weight <= 0 {
			weight = 1
		}
		for i := 0; i < weight; i++ {
			out = append(out, node)
		}
	}
	return out
}

func stickyHint(payload map[string]any) string {
	for _, key := range []string{"previous_response_id", "conversation_id", "thread_id"} {
		if value := stringValue(payload[key]); value != "" {
			return key + ":" + value
		}
	}
	if metadata, ok := payload["metadata"].(map[string]any); ok {
		for _, key := range []string{"conversation_id", "thread_id"} {
			if value := stringValue(metadata[key]); value != "" {
				return "metadata." + key + ":" + value
			}
		}
	}
	return ""
}

func (r *Router) BindSticky(key string, value string, backendName string) {
	if r == nil {
		return
	}
	key = strings.TrimSpace(key)
	value = strings.TrimSpace(value)
	backendName = strings.TrimSpace(backendName)
	if key == "" || value == "" || backendName == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sticky[key+":"+value] = backendName
}

func (r *Router) HealthSnapshot() []BackendHealth {
	r.mu.Lock()
	defer r.mu.Unlock()
	names := make([]string, 0, len(r.Backends)+len(r.PassthroughBackends))
	for name := range r.Backends {
		names = append(names, name)
	}
	for name := range r.PassthroughBackends {
		names = append(names, name)
	}
	slices.Sort(names)
	out := make([]BackendHealth, 0, len(names))
	for _, name := range names {
		out = append(out, r.healthLocked(name))
	}
	return out
}

func (r *Router) ActiveHealthCheckOnce(ctx context.Context) []BackendHealth {
	if r == nil {
		return nil
	}
	backends := r.healthCheckBackends()
	for _, backend := range backends {
		r.probeBackend(ctx, backend)
	}
	return r.HealthSnapshot()
}

func (r *Router) healthCheckBackends() []Backend {
	r.mu.Lock()
	defer r.mu.Unlock()
	backends := make([]Backend, 0, len(r.Backends)+len(r.PassthroughBackends))
	for _, backend := range r.Backends {
		backends = append(backends, backend)
	}
	for _, backend := range r.PassthroughBackends {
		backends = append(backends, backend)
	}
	slices.SortFunc(backends, func(a, b Backend) int {
		return strings.Compare(a.Name, b.Name)
	})
	return backends
}

func (r *Router) probeBackend(ctx context.Context, backend Backend) {
	path := r.HealthCheckPath
	if strings.TrimSpace(path) == "" {
		path = "/models"
	}
	timeout := r.HealthCheckTimeout
	if timeout <= 0 {
		timeout = 2 * time.Second
	}
	probeCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	client := r.Client
	if client == nil {
		client = &http.Client{Timeout: backend.TimeoutOrDefault()}
	}
	var resp *http.Response
	var err error
	if backend.GigaChat != nil {
		resp, err = backend.GigaChat.Probe(probeCtx, path, client)
	} else {
		req, reqErr := http.NewRequestWithContext(probeCtx, http.MethodGet, backend.BuildURL(path, ""), nil)
		if reqErr != nil {
			r.recordBackendFailure(backend.Name, 0, reqErr.Error())
			return
		}
		req.Header = BuildOutboundHeaders(http.Header{"Accept": []string{"application/json"}}, backend.APIKey, false, backend.APIType, backend.AnthropicVersion)
		resp, err = client.Do(req)
	}
	if err != nil {
		r.recordBackendFailure(backend.Name, 0, err.Error())
		return
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	if resp.StatusCode >= 200 && resp.StatusCode < 400 {
		r.recordBackendSuccess(backend.Name, resp.StatusCode)
		return
	}
	r.recordBackendFailure(backend.Name, resp.StatusCode, fmt.Sprintf("active health check returned status %d", resp.StatusCode))
}

func (r *Router) ensureHealthLocked(name string) {
	if name == "" {
		return
	}
	if _, ok := r.Health[name]; !ok {
		r.Health[name] = BackendHealth{Name: name, Healthy: true, Score: 1}
	}
}

func (r *Router) healthLocked(name string) BackendHealth {
	r.ensureHealthLocked(name)
	health := r.Health[name]
	if !health.Healthy && !health.UnhealthyUntil.IsZero() && !health.UnhealthyUntil.After(time.Now()) {
		if r.halfOpenRecoveryEnabled() {
			health.Healthy = false
			health.State = "half_open"
			health.RecoverySuccessThreshold = r.RecoverySuccessThreshold
		} else {
			health.Healthy = true
			health.ConsecutiveFailures = 0
		}
		health.UnhealthyUntil = time.Time{}
		r.Health[name] = health
	}
	health = r.decorateHealthState(health)
	health.InflightRequests = r.inflightCount(name)
	return health
}

func (r *Router) recordBackendSuccess(name string, status int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := time.Now()
	health := r.healthLocked(name)
	health.SuccessCount++
	health.LastStatus = status
	health.LastError = ""
	health.LastSuccess = now
	health.Score = healthScore(health.SuccessCount, health.FailureCount)
	if r.halfOpenRecoveryEnabled() && (healthState(health) == "half_open" || healthState(health) == "unhealthy") {
		state := healthState(health)
		health.RecoverySuccesses++
		health.RecoverySuccessThreshold = r.RecoverySuccessThreshold
		if health.RecoverySuccesses >= r.RecoverySuccessThreshold {
			health.Healthy = true
			health.State = "healthy"
			health.ConsecutiveFailures = 0
			health.RecoverySuccesses = 0
			health.RecoverySuccessThreshold = r.RecoverySuccessThreshold
			health.UnhealthyUntil = time.Time{}
		} else {
			health.Healthy = false
			health.State = state
			if state == "half_open" {
				health.UnhealthyUntil = time.Time{}
			}
		}
		r.Health[name] = health
		return
	}
	health.Healthy = true
	health.State = ""
	health.ConsecutiveFailures = 0
	health.RecoverySuccesses = 0
	health.RecoverySuccessThreshold = 0
	if r.halfOpenRecoveryEnabled() {
		health.State = "healthy"
		health.RecoverySuccessThreshold = r.RecoverySuccessThreshold
	}
	health.UnhealthyUntil = time.Time{}
	r.Health[name] = health
}

func (r *Router) recordBackendFailure(name string, status int, message string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := time.Now()
	health := r.healthLocked(name)
	health.FailureCount++
	health.ConsecutiveFailures++
	health.LastStatus = status
	health.LastError = message
	health.LastFailure = now
	health.Score = healthScore(health.SuccessCount, health.FailureCount)
	if r.halfOpenRecoveryEnabled() && healthState(health) == "half_open" {
		health.Healthy = false
		health.State = "unhealthy"
		health.RecoverySuccesses = 0
		health.RecoverySuccessThreshold = r.RecoverySuccessThreshold
		health.UnhealthyUntil = now.Add(r.UnhealthyCooldown)
		r.Health[name] = health
		return
	}
	if health.ConsecutiveFailures >= max(1, r.FailureThreshold) {
		health.Healthy = false
		health.UnhealthyUntil = now.Add(r.UnhealthyCooldown)
		if r.halfOpenRecoveryEnabled() {
			health.State = "unhealthy"
			health.RecoverySuccesses = 0
			health.RecoverySuccessThreshold = r.RecoverySuccessThreshold
		}
	}
	r.Health[name] = health
}

func (r *Router) halfOpenRecoveryEnabled() bool {
	return r != nil && r.RecoverySuccessThreshold > 0
}

func (r *Router) routePreferredHealth(health BackendHealth) bool {
	state := healthState(health)
	if r.halfOpenRecoveryEnabled() && state == "half_open" {
		return false
	}
	return health.Healthy
}

func (r *Router) decorateHealthState(health BackendHealth) BackendHealth {
	if !r.halfOpenRecoveryEnabled() {
		health.State = ""
		health.RecoverySuccesses = 0
		health.RecoverySuccessThreshold = 0
		return health
	}
	health.State = healthState(health)
	health.RecoverySuccessThreshold = r.RecoverySuccessThreshold
	return health
}

func healthState(health BackendHealth) string {
	if health.State == "half_open" {
		return "half_open"
	}
	if health.Healthy {
		return "healthy"
	}
	return "unhealthy"
}

func healthScore(successes int, failures int) float64 {
	total := successes + failures
	if total <= 0 {
		return 1
	}
	return float64(successes) / float64(total)
}

func (r *Router) Forward(ctx context.Context, modelName string, path string, rawQuery string, body []byte, payload map[string]any, isStream bool, headers http.Header) (*http.Response, error) {
	result, err := r.ForwardWithResult(ctx, modelName, path, rawQuery, body, payload, isStream, headers)
	return result.Response, err
}

func (r *Router) ForwardWithResult(ctx context.Context, modelName string, path string, rawQuery string, body []byte, payload map[string]any, isStream bool, headers http.Header, routePayload ...map[string]any) (ForwardResult, error) {
	if len(routePayload) > 0 && routePayload[0] != nil {
		return r.ForwardWithOptions(ctx, modelName, path, rawQuery, body, payload, isStream, headers, ForwardOptions{RoutePayload: routePayload[0]})
	}
	return r.ForwardWithOptions(ctx, modelName, path, rawQuery, body, payload, isStream, headers, ForwardOptions{})
}

func (r *Router) otelEnabled() bool {
	return r != nil && r.Observability != nil && r.Observability.Enabled()
}

func (r *Router) TraceModelName(modelName string) string {
	modelName = strings.TrimSpace(modelName)
	if modelName == "" || r == nil {
		return "unknown"
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.Models[modelName]; ok {
		return modelName
	}
	if _, ok := r.Backends[modelName]; ok {
		return modelName
	}
	if _, ok := r.PassthroughBackends[modelName]; ok {
		return modelName
	}
	return "unknown"
}

func (r *Router) startForwardPhase(ctx context.Context, phase string, endpointKind string, modelName string, isStream bool) (context.Context, observability.Span) {
	if !r.otelEnabled() {
		return ctx, observability.Span{}
	}
	return r.Observability.StartPhaseSpan(ctx, phase, observability.PhaseAttrs{
		EndpointKind: endpointKind,
		Model:        r.TraceModelName(modelName),
		Stream:       isStream,
	})
}

func (r *Router) startAttemptSpan(ctx context.Context, endpointKind string, modelName string, backend Backend, attemptIndex int, retryIndex int, isStream bool) (context.Context, observability.Span) {
	if !r.otelEnabled() {
		return ctx, observability.Span{}
	}
	return r.Observability.StartPhaseSpan(ctx, observability.PhaseUpstreamAttempt, observability.PhaseAttrs{
		EndpointKind: endpointKind,
		Model:        r.TraceModelName(modelName),
		BackendName:  backend.Name,
		AttemptIndex: attemptIndex,
		RetryIndex:   retryIndex,
		Stream:       isStream,
	})
}

func (r *Router) ForwardWithOptions(ctx context.Context, modelName string, path string, rawQuery string, body []byte, payload map[string]any, isStream bool, headers http.Header, opts ForwardOptions) (ForwardResult, error) {
	routingPayload := payload
	if opts.RoutePayload != nil {
		routingPayload = opts.RoutePayload
	}
	endpointKind := observability.EndpointKind(path)
	if strings.TrimSpace(opts.EndpointKind) != "" {
		endpointKind = observability.EndpointKind(opts.EndpointKind)
	}
	_, routingSpan := r.startForwardPhase(ctx, observability.PhaseRouting, endpointKind, modelName, isStream)
	route, err := r.BuildRouteForKeyWithOptions(modelName, path, routingPayload, opts.KeyID, RouteOptions{BackendAvailable: opts.BackendAvailable, QuotaAvailability: opts.QuotaAvailability})
	if err != nil {
		routingSpan.End(observability.SpanResult{Outcome: observability.OutcomeError, Error: err})
		return ForwardResult{}, err
	}
	if resp, backendName, warnings, ok := r.lookupInferenceCache(route, path, rawQuery, body, payload, isStream, opts); ok {
		status := 0
		if resp != nil {
			status = resp.StatusCode
		}
		routingSpan.End(observability.SpanResult{Outcome: observability.OutcomeCacheHit, BackendName: backendName, StatusCode: status})
		return ForwardResult{Response: resp, BackendName: backendName, Warnings: warnings}, nil
	}
	if resp, backendName, warnings, ok := r.lookupSemanticInferenceCache(ctx, route, modelName, path, rawQuery, body, payload, isStream, opts); ok {
		status := 0
		if resp != nil {
			status = resp.StatusCode
		}
		routingSpan.End(observability.SpanResult{Outcome: observability.OutcomeCacheHit, BackendName: backendName, StatusCode: status})
		return ForwardResult{Response: resp, BackendName: backendName, SemanticCacheHit: true, Warnings: warnings}, nil
	}
	routingSpan.End(observability.SpanResult{Outcome: observability.OutcomeRouted})
	var lastErr error
	var lastBackend string
	var lastWarnings []RequestWarning
	attemptTrail := make([]UpstreamAttempt, 0)
	for backendIndex, backend := range route {
		attempts := max(1, r.NumRetries)
		delay := DefaultRetryDelay
		for attempt := 0; attempt < attempts; attempt++ {
			attemptStarted := time.Now()
			attemptIndex := len(attemptTrail) + 1
			attemptCtx, attemptSpan := r.startAttemptSpan(ctx, endpointKind, modelName, backend, attemptIndex, attempt, isStream)
			releaseAttempt, err := r.acquireAttempt(attemptCtx, backend, opts.BeforeAttempt)
			if err != nil {
				lastErr = err
				lastBackend = backend.Name
				attemptSpan.End(observability.SpanResult{Outcome: observability.OutcomeBlocked, BackendName: backend.Name, Error: err})
				attemptTrail = append(attemptTrail, buildUpstreamAttempt(
					attemptIndex,
					backend,
					attempt,
					0,
					false,
					"blocked",
					time.Since(attemptStarted),
					err,
				))
				break
			}
			resp, warnings, parsedResponse, err := r.execute(attemptCtx, backend, endpointKind, modelName, path, rawQuery, body, payload, isStream, headers, opts.CaptureParsedResponse)
			attemptDuration := time.Since(attemptStarted)
			if len(warnings) > 0 {
				lastWarnings = warnings
			}
			if err != nil {
				releaseAttempt()
			} else if resp != nil {
				if isStream {
					resp.Body = &releaseReadCloser{ReadCloser: resp.Body, release: releaseAttempt}
				} else {
					releaseAttempt()
				}
			} else {
				releaseAttempt()
			}
			if err == nil && resp != nil && !RetryableStatuses[resp.StatusCode] {
				attemptSpan.End(observability.SpanResult{Outcome: observability.OutcomeFinal, BackendName: backend.Name, StatusCode: resp.StatusCode})
				attemptTrail = append(attemptTrail, buildUpstreamAttempt(
					attemptIndex,
					backend,
					attempt,
					resp.StatusCode,
					false,
					"final",
					attemptDuration,
					nil,
				))
				r.recordBackendSuccess(backend.Name, resp.StatusCode)
				if resp.StatusCode == http.StatusOK {
					r.recordPrefixAffinity(backend.Name, routingPayload)
					if hint := stickyHint(routingPayload); hint != "" {
						r.mu.Lock()
						r.sticky[hint] = backend.Name
						r.mu.Unlock()
					}
				}
				if isStream {
					r.prepareStreamResponse(attemptCtx, resp, backend, endpointKind, modelName, path, payload, opts.ParsedStreamCapture)
				}
				r.storeInferenceCache(backend, path, rawQuery, body, payload, isStream, opts, resp)
				return ForwardResult{Response: resp, BackendName: backend.Name, Warnings: warnings, AttemptTrail: attemptTrail, ParsedResponse: parsedResponse}, nil
			}
			if err != nil {
				lastErr = err
				lastBackend = backend.Name
				r.recordBackendFailure(backend.Name, 0, err.Error())
				outcome := "retry"
				if !hasRemainingAttempt(route, backendIndex, attempt, attempts) {
					outcome = "error"
				}
				attemptSpan.End(observability.SpanResult{Outcome: outcome, BackendName: backend.Name, Error: err})
				attemptTrail = append(attemptTrail, buildUpstreamAttempt(
					attemptIndex,
					backend,
					attempt,
					0,
					true,
					outcome,
					attemptDuration,
					err,
				))
				if resp != nil {
					_ = resp.Body.Close()
				}
			}
			if err == nil && resp != nil {
				lastBackend = backend.Name
				lastErr = fmt.Errorf("%s returned status %d", backend.Name, resp.StatusCode)
				r.recordBackendFailure(backend.Name, resp.StatusCode, lastErr.Error())
				if backendIndex == len(route)-1 && attempt+1 == attempts {
					attemptSpan.End(observability.SpanResult{Outcome: observability.OutcomeFinal, BackendName: backend.Name, StatusCode: resp.StatusCode, Error: lastErr})
					attemptTrail = append(attemptTrail, buildUpstreamAttempt(
						attemptIndex,
						backend,
						attempt,
						resp.StatusCode,
						RetryableStatuses[resp.StatusCode],
						"final",
						attemptDuration,
						lastErr,
					))
					if isStream {
						r.prepareStreamResponse(attemptCtx, resp, backend, endpointKind, modelName, path, payload, opts.ParsedStreamCapture)
					}
					return ForwardResult{Response: resp, BackendName: backend.Name, Warnings: warnings, AttemptTrail: attemptTrail, ParsedResponse: parsedResponse}, nil
				}
				attemptSpan.End(observability.SpanResult{Outcome: observability.OutcomeRetry, BackendName: backend.Name, StatusCode: resp.StatusCode, Error: lastErr})
				attemptTrail = append(attemptTrail, buildUpstreamAttempt(
					attemptIndex,
					backend,
					attempt,
					resp.StatusCode,
					RetryableStatuses[resp.StatusCode],
					"retry",
					attemptDuration,
					lastErr,
				))
				_ = resp.Body.Close()
			}
			if attempt+1 < attempts {
				timer := time.NewTimer(delay)
				select {
				case <-ctx.Done():
					timer.Stop()
					return ForwardResult{BackendName: lastBackend, Warnings: lastWarnings, AttemptTrail: attemptTrail}, ctx.Err()
				case <-timer.C:
				}
				delay *= 2
				if delay > MaxRetryDelay {
					delay = MaxRetryDelay
				}
			}
		}
	}
	if lastErr != nil {
		return ForwardResult{BackendName: lastBackend, Warnings: lastWarnings, AttemptTrail: attemptTrail}, lastErr
	}
	return ForwardResult{AttemptTrail: attemptTrail}, fmt.Errorf("all backends failed for model %q", modelName)
}

func (r *Router) lookupInferenceCache(route []Backend, path string, rawQuery string, body []byte, payload map[string]any, isStream bool, opts ForwardOptions) (*http.Response, string, []RequestWarning, bool) {
	if len(route) == 0 || !opts.Cacheable || opts.CacheBypass || isStream || !inferenceCacheableEndpoint(path) || !r.InferenceCache.CanRead() {
		return nil, "", nil, false
	}
	backend := route[0]
	bodyResult := BuildBackendBodyWithWarnings(payload, backend, body, false)
	key, ok := inferenceCacheKey(path, rawQuery, backend.Name, opts.CacheScope, bodyResult.Body)
	if !ok {
		return nil, "", nil, false
	}
	resp, ok := r.InferenceCache.Lookup(key)
	if !ok {
		return nil, "", nil, false
	}
	return resp, backend.Name, bodyResult.Warnings, true
}

func (r *Router) lookupSemanticInferenceCache(ctx context.Context, route []Backend, modelName string, path string, rawQuery string, body []byte, payload map[string]any, isStream bool, opts ForwardOptions) (*http.Response, string, []RequestWarning, bool) {
	if r == nil || len(route) == 0 || !opts.Cacheable || opts.CacheBypass || isStream || path != "/v1/chat/completions" {
		return nil, "", nil, false
	}
	semanticCache := r.InferenceCache.SemanticCache()
	if semanticCache == nil || !semanticCache.CanRead() {
		return nil, "", nil, false
	}
	backend := route[0]
	bodyResult := BuildBackendBodyWithWarnings(payload, backend, body, false)
	resp, ok := r.InferenceCache.LookupSemantic(ctx, SemanticCacheRequest{
		Path:        path,
		RawQuery:    rawQuery,
		BackendName: backend.Name,
		Scope:       opts.CacheScope,
		ModelName:   modelName,
		Body:        bodyResult.Body,
	})
	if !ok {
		return nil, "", nil, false
	}
	return resp, backend.Name, bodyResult.Warnings, true
}

func (r *Router) LookupTranslatedInferenceCache(modelName string, routePath string, cachePath string, rawQuery string, body []byte, payload map[string]any, isStream bool, opts ForwardOptions) (ForwardResult, bool) {
	if r == nil || !opts.Cacheable || opts.CacheBypass || isStream || !inferenceCacheableEndpoint(cachePath) || !r.InferenceCache.CanRead() {
		return ForwardResult{}, false
	}
	routingPayload := payload
	if opts.RoutePayload != nil {
		routingPayload = opts.RoutePayload
	}
	route, err := r.BuildRouteForKeyWithOptions(modelName, routePath, routingPayload, opts.KeyID, RouteOptions{BackendAvailable: opts.BackendAvailable, QuotaAvailability: opts.QuotaAvailability})
	if err != nil {
		return ForwardResult{}, false
	}
	resp, backendName, warnings, ok := r.lookupInferenceCache(route, cachePath, rawQuery, body, payload, isStream, opts)
	if !ok {
		return ForwardResult{}, false
	}
	return ForwardResult{Response: resp, BackendName: backendName, Warnings: warnings}, true
}

func (r *Router) storeInferenceCache(backend Backend, path string, rawQuery string, body []byte, payload map[string]any, isStream bool, opts ForwardOptions, resp *http.Response) {
	if !opts.Cacheable || opts.CacheBypass || isStream || !inferenceCacheableEndpoint(path) || !r.InferenceCache.CanWrite() || !inferenceCacheableResponse(resp) {
		return
	}
	bodyResult := BuildBackendBodyWithWarnings(payload, backend, body, false)
	key, ok := inferenceCacheKey(path, rawQuery, backend.Name, opts.CacheScope, bodyResult.Body)
	if !ok {
		return
	}
	respBody, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil {
		resp.Body = io.NopCloser(bytes.NewReader(respBody))
		resp.ContentLength = int64(len(respBody))
		return
	}
	resp.Body = io.NopCloser(bytes.NewReader(respBody))
	resp.ContentLength = int64(len(respBody))
	r.InferenceCache.Store(key, resp.StatusCode, FilterResponseHeaders(resp.Header), respBody)
}

func (r *Router) StoreTranslatedInferenceCache(backendName string, cachePath string, rawQuery string, body []byte, payload map[string]any, isStream bool, opts ForwardOptions, status int, header http.Header, responseBody []byte) bool {
	if r == nil || !opts.Cacheable || opts.CacheBypass || isStream || !inferenceCacheableEndpoint(cachePath) || !r.InferenceCache.CanWrite() || status != http.StatusOK {
		return false
	}
	contentType := strings.ToLower(header.Get("Content-Type"))
	if !strings.Contains(contentType, "json") {
		return false
	}
	backend, ok := r.GetBackend(backendName)
	if !ok {
		return false
	}
	bodyResult := BuildBackendBodyWithWarnings(payload, backend, body, false)
	key, ok := inferenceCacheKey(cachePath, rawQuery, backend.Name, opts.CacheScope, bodyResult.Body)
	if !ok {
		return false
	}
	return r.InferenceCache.Store(key, status, FilterResponseHeaders(header), responseBody)
}

func hasRemainingAttempt(route []Backend, backendIndex int, retryIndex int, retriesForBackend int) bool {
	return retryIndex+1 < retriesForBackend || backendIndex+1 < len(route)
}

func buildUpstreamAttempt(index int, backend Backend, retryIndex int, status int, retryable bool, outcome string, duration time.Duration, err error) UpstreamAttempt {
	return UpstreamAttempt{
		AttemptIndex: index,
		BackendName:  backend.Name,
		RetryIndex:   retryIndex,
		Status:       status,
		Retryable:    retryable,
		Outcome:      outcome,
		DurationMS:   duration.Milliseconds(),
		Error:        sanitizeAttemptError(err, backend),
	}
}

func sanitizeAttemptError(err error, backend Backend) string {
	if err == nil {
		return ""
	}
	message := strings.TrimSpace(err.Error())
	if message == "" {
		return ""
	}
	if backend.APIKey != "" {
		message = strings.ReplaceAll(message, backend.APIKey, "[REDACTED]")
	}
	message = attemptURLPattern.ReplaceAllStringFunc(message, sanitizeAttemptURL)
	message = attemptBearerPattern.ReplaceAllString(message, "Bearer [REDACTED]")
	message = attemptSecretPattern.ReplaceAllString(message, "$1=[REDACTED]")
	if len(message) > 512 {
		message = message[:512]
	}
	return message
}

func sanitizeAttemptURL(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	parsed.RawQuery = ""
	parsed.Fragment = ""
	if parsed.User != nil {
		parsed.User = url.User("[REDACTED]")
	}
	return parsed.String()
}

func (r *Router) acquireAttempt(ctx context.Context, backend Backend, limiter AttemptLimiter) (func(), error) {
	var limiterRelease func()
	if limiter == nil {
		limiterRelease = nil
	} else {
		release, err := limiter(ctx, backend)
		if err != nil {
			return nil, err
		}
		limiterRelease = release
	}
	releaseInflight := r.TrackBackendAttempt(backend.Name)
	var once sync.Once
	return func() {
		once.Do(func() {
			if limiterRelease != nil {
				limiterRelease()
			}
			releaseInflight()
		})
	}, nil
}

func (r *Router) TrackBackendAttempt(name string) func() {
	if r == nil || r.inflight == nil {
		return func() {}
	}
	return r.inflight.begin(name)
}

type releaseReadCloser struct {
	io.ReadCloser
	release func()
}

func (r *releaseReadCloser) Read(p []byte) (int, error) {
	n, err := r.ReadCloser.Read(p)
	if err != nil {
		r.release()
	}
	return n, err
}

func (r *releaseReadCloser) Close() error {
	err := r.ReadCloser.Close()
	r.release()
	return err
}

func (r *Router) execute(ctx context.Context, backend Backend, endpointKind string, modelName string, path string, rawQuery string, body []byte, payload map[string]any, isStream bool, headers http.Header, captureParsedResponse bool) (*http.Response, []RequestWarning, *ParsedResponseTrace, error) {
	bodyResult := BuildBackendBodyWithWarnings(payload, backend, body, isStream)
	outboundBody := bodyResult.Body
	if backend.GigaChat != nil {
		gigachatPayload := map[string]any{}
		if err := json.Unmarshal(outboundBody, &gigachatPayload); err != nil {
			gigachatPayload = payload
		}
		resp, err := backend.GigaChat.Execute(ctx, gigachatPayload, isStream, r.Client)
		return resp, bodyResult.Warnings, nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, backend.BuildURL(path, rawQuery), bytes.NewReader(outboundBody))
	if err != nil {
		return nil, bodyResult.Warnings, nil, err
	}
	req.Header = BuildOutboundHeaders(headers, backend.APIKey, isStream, backend.APIType, backend.AnthropicVersion)
	client := r.Client
	if client == nil {
		client = &http.Client{Timeout: backend.TimeoutOrDefault()}
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, bodyResult.Warnings, nil, err
	}
	if err := DecodeResponseBody(resp); err != nil {
		return nil, bodyResult.Warnings, nil, err
	}
	if isStream {
		return resp, bodyResult.Warnings, nil, nil
	}
	respBody, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil {
		return nil, bodyResult.Warnings, nil, err
	}
	pipeline := r.pipelineForBackend(backend.Name)
	var parsedResponse *ParsedResponseTrace
	var parserSpan observability.Span
	if len(pipeline.Modules) > 0 {
		_, parserSpan = r.startForwardPhase(ctx, observability.PhaseParser, endpointKind, modelName, false)
	}
	if parsed := pipeline.TransformResponseBody(respBody, resp.Header.Get("Content-Type"), modules.Context{Path: path, Model: payloadModel(payload), Backend: backend.Name}); parsed != nil {
		if captureParsedResponse {
			parsedResponse = &ParsedResponseTrace{
				RawUpstreamBody:       respBody,
				TransformedClientBody: parsed,
				ContentType:           resp.Header.Get("Content-Type"),
			}
		}
		respBody = parsed
	}
	parserSpan.End(observability.SpanResult{Outcome: observability.OutcomeSuccess, BackendName: backend.Name, StatusCode: resp.StatusCode})
	resp.Body = io.NopCloser(bytes.NewReader(respBody))
	resp.ContentLength = int64(len(respBody))
	resp.Header = FilterResponseHeaders(resp.Header)
	return resp, bodyResult.Warnings, parsedResponse, nil
}

func (r *Router) prepareStreamResponse(ctx context.Context, resp *http.Response, backend Backend, endpointKind string, modelName string, path string, payload map[string]any, capture *ParsedStreamCapture) {
	if resp == nil || resp.Body == nil {
		return
	}
	pipeline := r.pipelineForBackend(backend.Name)
	hasCapture := capture != nil && (capture.Raw != nil || capture.Transformed != nil)
	if len(pipeline.Modules) == 0 && !hasCapture {
		return
	}
	streamBody := &streamRewriteReadCloser{
		ReadCloser:         resp.Body,
		pipeline:           pipeline,
		state:              pipeline.NewStreamState(),
		ctx:                modules.Context{Path: path, Model: payloadModel(payload), Backend: backend.Name, IsStream: true},
		captureRaw:         captureRawWriter(capture),
		captureTransformed: captureTransformedWriter(capture),
		backendName:        backend.Name,
		statusCode:         resp.StatusCode,
	}
	if len(pipeline.Modules) > 0 {
		_, streamBody.parserSpan = r.startForwardPhase(ctx, observability.PhaseParser, endpointKind, modelName, true)
	}
	resp.Body = streamBody
}

func captureRawWriter(capture *ParsedStreamCapture) io.Writer {
	if capture == nil {
		return nil
	}
	return capture.Raw
}

func captureTransformedWriter(capture *ParsedStreamCapture) io.Writer {
	if capture == nil {
		return nil
	}
	return capture.Transformed
}

func (r *Router) pipelineForBackend(name string) modules.Pipeline {
	if pipeline, ok := r.ResponseOverrides[name]; ok {
		return pipeline
	}
	return r.ResponsePipeline
}

type streamRewriteReadCloser struct {
	io.ReadCloser
	pipeline modules.Pipeline
	state    *modules.StreamState
	ctx      modules.Context
	buffer   []byte
	eof      bool
	flushed  bool

	captureRaw         io.Writer
	captureTransformed io.Writer
	parserSpan         observability.Span
	parserSpanOnce     sync.Once
	backendName        string
	statusCode         int
}

func (s *streamRewriteReadCloser) Read(p []byte) (int, error) {
	if len(s.buffer) > 0 {
		n := copy(p, s.buffer)
		s.buffer = s.buffer[n:]
		s.writeTransformed(p[:n])
		if s.eof && len(s.buffer) == 0 {
			s.endParserSpan(nil)
		}
		return n, nil
	}
	if s.eof {
		return 0, io.EOF
	}
	n, err := s.ReadCloser.Read(p)
	if n <= 0 {
		if err == io.EOF {
			if flushed := s.flushPending(); len(flushed) > 0 {
				copied := copy(p, flushed)
				if copied < len(flushed) {
					s.buffer = append(s.buffer, flushed[copied:]...)
				}
				s.eof = true
				s.writeTransformed(p[:copied])
				if len(s.buffer) == 0 {
					s.endParserSpan(nil)
				}
				return copied, nil
			}
			s.eof = true
			s.endParserSpan(nil)
		} else if err != nil {
			s.endParserSpan(err)
		}
		return n, err
	}
	s.writeRaw(p[:n])
	rewritten := p[:n]
	if len(s.pipeline.Modules) > 0 {
		rewritten = modules.RewriteSSEChunkWithState(p[:n], s.pipeline, s.ctx, s.state)
		if err == io.EOF {
			if flushed := s.flushPending(); len(flushed) > 0 {
				rewritten = append(append([]byte(nil), rewritten...), flushed...)
			}
		}
	}
	copied := copy(p, rewritten)
	if copied < len(rewritten) {
		s.buffer = append(s.buffer, rewritten[copied:]...)
	}
	s.writeTransformed(p[:copied])
	if err == io.EOF {
		s.eof = true
		if len(s.buffer) == 0 {
			s.endParserSpan(nil)
		}
		return copied, nil
	}
	if err != nil {
		s.endParserSpan(err)
	}
	return copied, err
}

func (s *streamRewriteReadCloser) Close() error {
	s.pipeline.CloseStreamState(s.state)
	if s.ReadCloser == nil {
		if !s.eof {
			s.endParserSpan(io.ErrClosedPipe)
		}
		return nil
	}
	err := s.ReadCloser.Close()
	if !s.eof {
		if err != nil {
			s.endParserSpan(err)
		} else {
			s.endParserSpan(io.ErrClosedPipe)
		}
	}
	return err
}

func (s *streamRewriteReadCloser) endParserSpan(err error) {
	s.parserSpanOnce.Do(func() {
		outcome := observability.OutcomeSuccess
		if err != nil {
			outcome = observability.OutcomeError
		}
		s.parserSpan.End(observability.SpanResult{
			Outcome:     outcome,
			BackendName: s.backendName,
			StatusCode:  s.statusCode,
			Error:       err,
		})
	})
}

func (s *streamRewriteReadCloser) flushPending() []byte {
	if s.flushed {
		return nil
	}
	s.flushed = true
	return modules.FlushSSEChunkWithState(s.pipeline, s.ctx, s.state)
}

func (s *streamRewriteReadCloser) writeRaw(chunk []byte) {
	if s.captureRaw != nil && len(chunk) > 0 {
		_, _ = s.captureRaw.Write(chunk)
	}
}

func (s *streamRewriteReadCloser) writeTransformed(chunk []byte) {
	if s.captureTransformed != nil && len(chunk) > 0 {
		_, _ = s.captureTransformed.Write(chunk)
	}
}

func BuildResponsePipelineFromConfig(modulesCfg map[string]any, parsersCfg map[string]any, enabledDefault bool) modules.Pipeline {
	root := selectUpstreamConfig(modulesCfg)
	if len(root) == 0 && len(parsersCfg) > 0 {
		root = parsersCfg
	}
	if len(root) == 0 {
		return modules.Pipeline{}
	}
	enabledRaw, ok := root["enabled"]
	if ok {
		if !config.ParseBool(enabledRaw) {
			return modules.Pipeline{}
		}
	} else if !enabledDefault {
		return modules.Pipeline{}
	}
	names := stringSlice(root["response"])
	if len(names) == 0 {
		return modules.Pipeline{}
	}
	var parsed []modules.ResponseModule
	for _, name := range names {
		switch name {
		case "parse_tags", "parse_unparsed", "template_parse", "parse_template":
			moduleCfg := responseModuleConfig(root, name)
			derived := modules.ParseTagsConfig{}
			if templatePath := stringValue(moduleCfg["template_path"]); templatePath != "" {
				derived, _ = modules.DeriveParseTagsConfigFromTemplatePath(templatePath)
			}
			cfg := parseTagsConfigFromMap(moduleCfg, derived)
			parsed = append(parsed, modules.NewParseTags(cfg))
		case "swap_reasoning_content":
			mode := stringValueFromMap(root, "swap_reasoning_content", "mode", "reasoning_to_content")
			swap := modules.NewReasoningSwap(mode)
			swap.ThinkTag = stringValueFromMap(root, "swap_reasoning_content", "think_tag", "think")
			swap.IncludeNewline = boolValueFromMap(root, "swap_reasoning_content", "include_newline", true)
			parsed = append(parsed, swap)
		case "stream_json_repair":
			cfg := streamJSONRepairConfigFromMap(responseModuleConfig(root, name))
			if cfg.Enabled {
				parsed = append(parsed, modules.NewStreamJSONRepair(cfg))
			}
		}
	}
	return modules.Pipeline{Modules: parsed, Paths: stringSlice(root["paths"])}
}

func selectUpstreamConfig(root map[string]any) map[string]any {
	if len(root) == 0 {
		return nil
	}
	if upstream, ok := root["upstream"].(map[string]any); ok {
		return upstream
	}
	if nested, ok := root["modules"].(map[string]any); ok {
		if upstream, ok := nested["upstream"].(map[string]any); ok {
			return upstream
		}
	}
	return root
}

func parseFallbacks(entries []map[string]any) map[string][]string {
	out := map[string][]string{}
	for _, entry := range entries {
		for primary, rawTargets := range entry {
			out[primary] = stringSlice(rawTargets)
		}
	}
	return out
}

func (r *Router) applyHealthCheckSettings(settings config.RouterSettings) {
	health := settings.HealthChecks
	if !healthSettingsConfigured(health) && healthSettingsConfigured(settings.HealthScoring) {
		health = settings.HealthScoring
	}
	if strings.TrimSpace(health.Path) != "" {
		r.HealthCheckPath = strings.TrimSpace(health.Path)
	}
	if health.FailureThreshold > 0 {
		r.FailureThreshold = health.FailureThreshold
	}
	if seconds := firstPositiveFloat(health.UnhealthyCooldownSeconds, health.CooldownSeconds); seconds > 0 {
		r.UnhealthyCooldown = durationSeconds(seconds)
	}
	if seconds := firstPositiveFloat(health.IntervalSeconds, health.ProbeIntervalSeconds); seconds > 0 {
		r.HealthCheckInterval = durationSeconds(seconds)
	}
	if health.TimeoutSeconds > 0 {
		r.HealthCheckTimeout = durationSeconds(health.TimeoutSeconds)
	}
	if health.RecoverySuccessThreshold > 0 {
		r.RecoverySuccessThreshold = health.RecoverySuccessThreshold
	}
	r.ActiveHealthChecks = health.Enabled
	scoring := settings.HealthScoring
	if healthSettingsConfigured(scoring) {
		r.HealthScoring = scoring.Enabled
		if scoring.UnhealthyThreshold > 0 {
			r.UnhealthyThreshold = scoring.UnhealthyThreshold
		}
		if scoring.RecoverySuccessThreshold > 0 {
			r.RecoverySuccessThreshold = scoring.RecoverySuccessThreshold
		}
	} else if health.UnhealthyThreshold > 0 {
		r.HealthScoring = true
		r.UnhealthyThreshold = health.UnhealthyThreshold
	}
}

func healthSettingsConfigured(settings config.HealthCheckSettings) bool {
	return settings.Enabled ||
		strings.TrimSpace(settings.Path) != "" ||
		settings.IntervalSeconds > 0 ||
		settings.ProbeIntervalSeconds > 0 ||
		settings.TimeoutSeconds > 0 ||
		settings.FailureThreshold > 0 ||
		settings.UnhealthyThreshold > 0 ||
		settings.UnhealthyCooldownSeconds > 0 ||
		settings.CooldownSeconds > 0 ||
		settings.RecoverySuccessThreshold > 0
}

func firstPositiveFloat(values ...float64) float64 {
	for _, value := range values {
		if value > 0 {
			return value
		}
	}
	return 0
}

func durationSeconds(seconds float64) time.Duration {
	if seconds <= 0 {
		return 0
	}
	return time.Duration(seconds * float64(time.Second))
}

func parseParameterConfigs(raw map[string]any) map[string]ParameterConfig {
	out := map[string]ParameterConfig{}
	for name, rawCfg := range raw {
		cfgMap, ok := rawCfg.(map[string]any)
		if !ok {
			continue
		}
		allow := true
		if val, ok := cfgMap["allow_override"]; ok {
			allow = config.ParseBool(val)
		}
		out[name] = ParameterConfig{Default: cfgMap["default"], AllowOverride: allow}
	}
	return out
}

func firstMap(values ...any) map[string]any {
	for _, value := range values {
		if m, ok := value.(map[string]any); ok && len(m) > 0 {
			return m
		}
	}
	return nil
}

func mergeMaps(base, override map[string]any) map[string]any {
	out := map[string]any{}
	maps.Copy(out, base)
	maps.Copy(out, override)
	return out
}

func nodeBackendParams(provider config.ProviderEntry, node config.NodeEntry, apiBase string, apiType string) map[string]any {
	params := map[string]any{
		"api_type": apiType,
		"api_base": apiBase,
		"api_key":  firstString(node.APIKey, provider.APIKey),
		"model":    firstString(node.UpstreamModel, node.Model, node.TargetModel),
	}
	setStringParam(params, "mode", node.Mode, provider.Mode)
	setStringParam(params, "anthropic_version", node.AnthropicVersion, provider.AnthropicVersion)
	setStringParam(params, "scope", node.Scope, provider.Scope)
	setStringParam(params, "auth_url", node.AuthURL, provider.AuthURL)
	setStringParam(params, "client_cert", node.ClientCert, provider.ClientCert)
	setStringParam(params, "client_key", node.ClientKey, provider.ClientKey)
	if value := firstConfiguredAny(node.RequestTimeout, provider.RequestTimeout); value != nil {
		params["request_timeout"] = value
	}
	if value := firstConfiguredAny(node.VerifySSL, provider.VerifySSL); value != nil {
		params["verify_ssl"] = value
	}
	if value := firstConfiguredAny(node.EmulateToolCalls, provider.EmulateToolCalls); value != nil {
		params["emulate_tool_calls"] = value
	}
	if value := firstConfiguredAny(node.SupportsResponsesAPI, provider.SupportsResponsesAPI); value != nil {
		params["supports_responses_api"] = value
	}
	return params
}

func setStringParam(params map[string]any, key string, values ...string) {
	if value := firstString(values...); value != "" {
		params[key] = value
	}
}

func firstConfiguredAny(values ...any) any {
	for _, value := range values {
		if value == nil {
			continue
		}
		if text, ok := value.(string); ok {
			text = strings.TrimSpace(text)
			if text == "" || text == "<nil>" {
				continue
			}
			return text
		}
		return value
	}
	return nil
}

func effectiveEndpoints(providerEndpoints []string, nodeEndpoints []string) []string {
	if len(nodeEndpoints) > 0 {
		return normalizeEndpoints(nodeEndpoints)
	}
	return normalizeEndpoints(providerEndpoints)
}

func endpointsFromValues(values ...any) []string {
	for _, value := range values {
		if endpoints := normalizeEndpoints(stringSlice(value)); len(endpoints) > 0 {
			return endpoints
		}
	}
	return nil
}

func normalizeEndpoints(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	out := make([]string, 0, len(values))
	for _, value := range values {
		endpoint := normalizeEndpoint(value)
		if endpoint != "" && !slices.Contains(out, endpoint) {
			out = append(out, endpoint)
		}
	}
	return out
}

func backendSupportsEndpoint(backend Backend, endpoint string) bool {
	if endpoint == "" || len(backend.Endpoints) == 0 {
		return true
	}
	return slices.Contains(backend.Endpoints, endpoint)
}

func backendAllowsKey(backend Backend, keyID string) bool {
	return allowedKeysValue(backend.AllowedKeys, keyID)
}

func allowedKeysValue(value any, keyID string) bool {
	switch typed := value.(type) {
	case nil:
		return true
	case string:
		text := strings.TrimSpace(typed)
		switch strings.ToLower(text) {
		case "", "all":
			return true
		case "none":
			return false
		default:
			return text == keyID
		}
	case []string:
		for _, allowed := range typed {
			if strings.TrimSpace(allowed) == keyID {
				return true
			}
		}
		return false
	case []any:
		for _, raw := range typed {
			if strings.TrimSpace(fmt.Sprint(raw)) == keyID {
				return true
			}
		}
		return false
	default:
		return true
	}
}

func normalizeEndpoint(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	value = strings.TrimPrefix(value, "/")
	value = strings.TrimPrefix(value, "v1/")
	value = strings.TrimPrefix(value, "v2/")
	value = strings.Trim(value, "/")
	value = strings.ReplaceAll(value, "-", "_")
	switch value {
	case "", "all", "*":
		return ""
	case "chat", "chat_completions", "chat/completions", "completions":
		return "chat"
	case "embedding", "embeddings":
		return "embeddings"
	case "rerank", "rankings":
		return "rerank"
	case "message", "messages", "anthropic", "anthropic_messages":
		return "messages"
	case "response", "responses":
		return "responses"
	default:
		return value
	}
}

func mergeLimits(base config.LimitsConfig, override config.LimitsConfig) config.LimitsConfig {
	if override.Concurrency != 0 {
		base.Concurrency = override.Concurrency
	}
	if override.RPS != 0 {
		base.RPS = override.RPS
	}
	if override.RPM != 0 {
		base.RPM = override.RPM
	}
	if override.TokenQuota != 0 {
		base.TokenQuota = override.TokenQuota
	}
	if override.TokenQuotaWindowSeconds != 0 {
		base.TokenQuotaWindowSeconds = override.TokenQuotaWindowSeconds
	}
	if override.TokenQuotaResetMode != "" {
		base.TokenQuotaResetMode = override.TokenQuotaResetMode
	}
	if override.Priority != 0 {
		base.Priority = override.Priority
	}
	if override.QueueTimeoutSeconds != 0 {
		base.QueueTimeoutSeconds = override.QueueTimeoutSeconds
	}
	return base
}

func stringSlice(value any) []string {
	switch typed := value.(type) {
	case nil:
		return nil
	case []string:
		return slices.Clone(typed)
	case []any:
		out := make([]string, 0, len(typed))
		for _, item := range typed {
			if s := stringValue(item); s != "" {
				out = append(out, s)
			}
		}
		return out
	case string:
		if typed == "" {
			return nil
		}
		return []string{typed}
	default:
		if s := stringValue(typed); s != "" {
			return []string{s}
		}
		return nil
	}
}

func stringValue(value any) string {
	if value == nil {
		return ""
	}
	return strings.TrimSpace(fmt.Sprint(value))
}

func firstString(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func parsePositiveInt(value any, def int) int {
	switch typed := value.(type) {
	case int:
		if typed > 0 {
			return typed
		}
	case int64:
		if typed > 0 {
			return int(typed)
		}
	case float64:
		if typed > 0 {
			return int(typed)
		}
	case string:
		var out int
		if _, err := fmt.Sscanf(typed, "%d", &out); err == nil && out > 0 {
			return out
		}
	}
	return def
}

func parsePositiveFloat(value any, def float64) float64 {
	switch typed := value.(type) {
	case int:
		if typed > 0 {
			return float64(typed)
		}
	case int64:
		if typed > 0 {
			return float64(typed)
		}
	case float64:
		if typed > 0 {
			return typed
		}
	case string:
		var out float64
		if _, err := fmt.Sscanf(typed, "%f", &out); err == nil && out > 0 {
			return out
		}
	}
	return def
}

func payloadModel(payload map[string]any) string {
	return NormalizeRequestModel(payload["model"])
}

func responseModuleConfig(root map[string]any, name string) map[string]any {
	keys := []string{name}
	for _, fallback := range []string{"parse_tags", "parse_template", "template_parse", "parse_unparsed"} {
		if !slices.Contains(keys, fallback) {
			keys = append(keys, fallback)
		}
	}
	for _, key := range keys {
		if cfg, ok := root[key].(map[string]any); ok {
			return cfg
		}
	}
	return map[string]any{}
}

func parseTagsConfigFromMap(raw map[string]any, derived modules.ParseTagsConfig) modules.ParseTagsConfig {
	return modules.ParseTagsConfig{
		ThinkTag:             stringValueDefault(raw["think_tag"], firstString(derived.ThinkTag, "think")),
		ToolTag:              stringValueDefault(raw["tool_tag"], firstString(derived.ToolTag, "tool_call")),
		ToolOpen:             stringValueDefault(raw["tool_open"], derived.ToolOpen),
		ToolClose:            stringValueDefault(raw["tool_close"], derived.ToolClose),
		ToolArgFormat:        stringValueDefault(raw["tool_arg_format"], firstString(derived.ToolArgFormat, "xml")),
		ToolArgSeparator:     stringValueDefault(raw["tool_arg_separator"], derived.ToolArgSeparator),
		ToolParser:           stringValueDefault(raw["tool_parser"], derived.ToolParser),
		ToolBufferLimit:      parsePositiveInt(raw["tool_buffer_limit"], derived.ToolBufferLimit),
		MaxStreamBufferBytes: parsePositiveInt(raw["max_stream_buffer_bytes"], derived.MaxStreamBufferBytes),
		MaxStreamBufferAge:   durationSeconds(firstPositiveFloat(parsePositiveFloat(raw["max_stream_buffer_age_seconds"], 0), derived.MaxStreamBufferAge.Seconds())),
		DropTags:             firstStringSlice(stringSlice(raw["drop_tags"]), derived.DropTags),
		ParseThinking:        boolValueDefault(raw, "parse_thinking", true),
		ParseToolCalls:       boolValueDefault(raw, "parse_tool_calls", true),
		StreamToolCalls:      boolValueDefault(raw, "stream_tool_calls", derived.StreamToolCalls),
		StartInThink:         boolValueDefault(raw, "start_in_think", derived.StartInThink),
	}
}

func streamJSONRepairConfigFromMap(raw map[string]any) modules.StreamJSONRepairConfig {
	return modules.StreamJSONRepairConfig{
		Enabled:              boolValueDefault(raw, "enabled", false),
		MaxStreamBufferBytes: parsePositiveInt(raw["max_stream_buffer_bytes"], 0),
		MaxStreamBufferAge:   durationSeconds(parsePositiveFloat(raw["max_stream_buffer_age_seconds"], 0)),
		OnFailure:            stringValue(raw["on_failure"]),
	}
}

func stringValueDefault(value any, fallback string) string {
	if text := stringValue(value); text != "" {
		return text
	}
	return fallback
}

func boolValueDefault(raw map[string]any, key string, fallback bool) bool {
	if value, ok := raw[key]; ok {
		return config.ParseBool(value)
	}
	return fallback
}

func firstStringSlice(values ...[]string) []string {
	for _, value := range values {
		if len(value) > 0 {
			return value
		}
	}
	return nil
}

func stringValueFromMap(root map[string]any, section string, key string, def string) string {
	cfg, _ := root[section].(map[string]any)
	if value := stringValue(cfg[key]); value != "" {
		return value
	}
	return def
}

func boolValueFromMap(root map[string]any, section string, key string, def bool) bool {
	cfg, _ := root[section].(map[string]any)
	if value, ok := cfg[key]; ok {
		return config.ParseBool(value)
	}
	return def
}
