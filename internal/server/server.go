package server

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/theio/yallmp-proxy/internal/admission"
	"github.com/theio/yallmp-proxy/internal/catalog"
	"github.com/theio/yallmp-proxy/internal/config"
	"github.com/theio/yallmp-proxy/internal/logdb"
	"github.com/theio/yallmp-proxy/internal/logstore"
	"github.com/theio/yallmp-proxy/internal/observability"
	"github.com/theio/yallmp-proxy/internal/policy"
	"github.com/theio/yallmp-proxy/internal/proxy"
	"github.com/theio/yallmp-proxy/internal/quota"
	responsesapi "github.com/theio/yallmp-proxy/internal/responses"
	adminui "github.com/theio/yallmp-proxy/web/admin"
)

type App struct {
	runtimeMu                  sync.RWMutex
	Config                     *config.Config
	Router                     *proxy.Router
	Catalog                    *catalog.Catalog
	catalogMu                  sync.RWMutex
	Policy                     *policy.Manager
	Admission                  *admission.Manager
	Logs                       *MemoryLogs
	LogDB                      *logdb.Store
	Payloads                   *logstore.FullRequestStore
	Quota                      *quota.Manager
	Responses                  *responsesapi.Store
	Observability              *observability.Provider
	PrometheusTimings          *prometheusTimingRecorder
	requiredHeaderMissingTotal atomic.Uint64
	generation                 atomic.Pointer[runtimeGeneration]
	// root is set on request-scoped runtime views and points at the owning App.
	root *App
}

var adminDistDir = filepath.Join(config.ProjectRoot(), "web", "admin", "dist")

var embeddedAdminAssets = adminui.Assets()

var parsedStreamSidecarMaxBytes int64 = 4 << 20

const observabilityShutdownTimeout = 5 * time.Second

type MemoryLogs struct {
	mu      sync.Mutex
	entries []LogEntry
	nextID  int64
}

type LogEntry struct {
	ID           int64                   `json:"id"`
	Time         time.Time               `json:"time"`
	Method       string                  `json:"method"`
	Path         string                  `json:"path"`
	Model        string                  `json:"model"`
	Status       int                     `json:"status"`
	Backend      string                  `json:"backend,omitempty"`
	Error        string                  `json:"error,omitempty"`
	DurationMS   int64                   `json:"duration_ms"`
	TraceID      string                  `json:"trace_id,omitempty"`
	SpanID       string                  `json:"span_id,omitempty"`
	PayloadPath  string                  `json:"payload_path,omitempty"`
	Warnings     []proxy.RequestWarning  `json:"warnings,omitempty"`
	AttemptTrail []proxy.UpstreamAttempt `json:"attempt_trail,omitempty"`
}

func New(cfg *config.Config) (*App, error) {
	if cfg == nil {
		return nil, fmt.Errorf("config is nil")
	}
	otelProvider, err := observability.Configure(context.Background(), cfg.ProxySettings.OTel)
	if err != nil {
		return nil, err
	}
	router, err := proxy.NewRouter(cfg)
	if err != nil {
		_ = shutdownObservabilityBounded(otelProvider)
		return nil, err
	}
	router.Observability = otelProvider
	if err := configureSemanticInferenceCache(cfg, router); err != nil {
		if router.InferenceCache != nil {
			_ = router.InferenceCache.Close()
		}
		_ = shutdownObservabilityBounded(otelProvider)
		return nil, err
	}
	dbStore, err := logdb.NewStore(cfg)
	if err != nil {
		if router.InferenceCache != nil {
			_ = router.InferenceCache.Close()
		}
		_ = shutdownObservabilityBounded(otelProvider)
		return nil, err
	}
	quotaManager := quota.NewManager()
	seedQuotaFromLogDB(context.Background(), quotaManager, dbStore, cfg)
	seedBackendQuotaFromLogDB(context.Background(), quotaManager, dbStore, cfg)
	responseStore := responsesapi.NewStore(1000)
	responseStore.SetPersistent(dbStore)
	return &App{
		Config:            cfg,
		Router:            router,
		Catalog:           catalog.New(cfg),
		Policy:            policy.NewManager(cfg),
		Admission:         admission.NewManager(),
		Logs:              &MemoryLogs{},
		LogDB:             dbStore,
		Payloads:          logstore.NewFullRequestStore(cfg),
		Quota:             quotaManager,
		Responses:         responseStore,
		Observability:     otelProvider,
		PrometheusTimings: newAppPrometheusTimingRecorder(cfg.ProxySettings.Metrics),
	}, nil
}

func (a *App) SetObservability(provider *observability.Provider) {
	if provider == nil {
		provider = observability.Noop()
	}
	a.Observability = provider
	if a.Router != nil {
		a.Router.Observability = provider
	}
}

func (a *App) ShutdownObservability(ctx context.Context) error {
	if a == nil || a.Observability == nil {
		return nil
	}
	return a.Observability.Shutdown(ctx)
}

func (a *App) CloseInferenceCache() error {
	if a == nil || a.Router == nil || a.Router.InferenceCache == nil {
		return nil
	}
	return a.Router.InferenceCache.Close()
}

func configureSemanticInferenceCache(cfg *config.Config, router *proxy.Router) error {
	if cfg == nil || router == nil {
		return nil
	}
	semantic := cfg.ProxySettings.InferenceCache.Semantic
	if !config.ParseBoolStrict(semantic.Enabled) {
		return nil
	}
	if router.InferenceCache == nil {
		return fmt.Errorf("semantic inference cache requires proxy_settings.inference_cache.mode to be read_write, read_only, or write_only")
	}
	embeddingNode := strings.TrimSpace(semantic.EmbeddingNode)
	backend, ok := router.GetBackend(embeddingNode)
	if !ok {
		return fmt.Errorf("semantic inference cache embedding_node %q is not a configured node backend", embeddingNode)
	}
	embeddingModel := strings.TrimSpace(semantic.EmbeddingModel)
	if embeddingModel == "" {
		return fmt.Errorf("semantic inference cache embedding_model is required")
	}
	router.InferenceCache.ConfigureSemantic(cfg.ProxySettings.InferenceCache, proxy.NewHTTPSemanticEmbeddingClient(backend, embeddingModel, nil))
	return nil
}

func shutdownObservabilityBounded(provider *observability.Provider) error {
	if provider == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), observabilityShutdownTimeout)
	defer cancel()
	return provider.Shutdown(ctx)
}

func (a *App) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if runtimeMutationRequest(r) {
		a.runtimeMu.Lock()
		defer a.runtimeMu.Unlock()
		a.serve(w, r)
		return
	}
	view, release := a.acquireRuntime()
	defer release()
	view.serve(w, r)
}

func (a *App) serve(w http.ResponseWriter, r *http.Request) {
	if sensitiveAdminAPIRequest(r) && !a.requireConfiguredAdminPassword(w, r, "admin API access") {
		return
	}
	switch {
	case isPprofPath(r.URL.Path) && a.profilingEnabled():
		a.handlePprof(w, r)
	case isMetricsPath(r.URL.Path) && a.metricsEnabled():
		a.handleMetrics(w, r)
	case r.Method == http.MethodGet && r.URL.Path == "/v1/models":
		a.handleModels(w, r)
	case r.Method == http.MethodGet && r.URL.Path == "/admin/config":
		a.handleGetConfig(w, r)
	case r.Method == http.MethodGet && r.URL.Path == "/admin/catalog":
		a.handleAdminCatalog(w, r)
	case r.Method == http.MethodPut && r.URL.Path == "/admin/config":
		a.handlePutConfig(w, r)
	case r.Method == http.MethodPost && r.URL.Path == "/admin/config/validate":
		a.handleValidateConfig(w, r)
	case r.Method == http.MethodPost && r.URL.Path == "/admin/config/reload":
		a.handleReloadConfig(w, r)
	case r.Method == http.MethodPost && isProviderDiscoveryPath(r.URL.Path):
		a.handleDiscoverProvider(w, r)
	case r.Method == http.MethodGet && r.URL.Path == "/admin/routing-pools":
		a.handleRoutingPools(w, r)
	case isAdminProvidersPath(r.URL.Path):
		a.handleAdminProviders(w, r)
	case isAdminNodesPath(r.URL.Path):
		a.handleAdminNodes(w, r)
	case isAdminClientModelsPath(r.URL.Path):
		a.handleAdminClientModels(w, r)
	case isAdminRouterSettingsPath(r.URL.Path):
		a.handleAdminRouterSettings(w, r)
	case r.Method == http.MethodGet && r.URL.Path == "/admin/cache":
		a.handleCacheStats(w, r)
	case r.Method == http.MethodGet && r.URL.Path == "/admin/backend-quotas":
		a.handleBackendQuotas(w, r)
	case r.Method == http.MethodPost && r.URL.Path == "/admin/models/copy":
		a.handleAdminModelCopy(w, r)
	case r.Method == http.MethodDelete && isAdminModelDeletePath(r.URL.Path):
		name, _ := adminModelDeletePathName(r.URL.Path)
		a.handleAdminModelDelete(w, r, name)
	case r.Method == http.MethodPost && r.URL.Path == "/admin/models":
		a.handleAdminModels(w, r)
	case r.Method == http.MethodGet && isAdminModelsReadPath(r.URL.Path) && !isAdminModelsHTMLNavigation(r):
		a.handleAdminModelsRead(w, r)
	case isAdminKeysPath(r.URL.Path):
		a.handleAdminKeys(w, r)
	case r.Method == http.MethodGet && r.URL.Path == "/admin/templates":
		a.handleListTemplates(w, r)
	case r.Method == http.MethodPost && r.URL.Path == "/admin/templates":
		a.handleUploadTemplate(w, r)
	case r.Method == http.MethodGet && r.URL.Path == "/admin/templates/inspect":
		a.handleInspectTemplate(w, r)
	case r.Method == http.MethodGet && r.URL.Path == "/admin/logs":
		a.handleLogs(w, r)
	case r.Method == http.MethodGet && r.URL.Path == "/api/logs":
		a.handleLogs(w, r)
	case r.Method == http.MethodGet && r.URL.Path == "/admin/logs/stop-reasons":
		a.handleLogStopReasons(w, r)
	case r.Method == http.MethodGet && r.URL.Path == "/api/logs/stop-reasons":
		a.handleLogStopReasons(w, r)
	case r.Method == http.MethodGet && r.URL.Path == "/admin/logs/tool-call-rate":
		a.handleLogToolCallRate(w, r)
	case r.Method == http.MethodGet && r.URL.Path == "/api/logs/tool-call-rate":
		a.handleLogToolCallRate(w, r)
	case r.Method == http.MethodGet && r.URL.Path == "/admin/logs/models":
		a.handleLogModels(w, r)
	case r.Method == http.MethodGet && r.URL.Path == "/api/logs/models":
		a.handleLogModels(w, r)
	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/admin/traces/"):
		a.handleTraceDetail(w, r)
	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/admin/logs/"):
		a.handleLogDetail(w, r)
	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/api/logs/"):
		a.handleLogDetail(w, r)
	case isAdminFrontendRequest(r):
		a.handleAdminFrontend(w, r)
	case r.Method == http.MethodGet && r.URL.Path == "/logs":
		a.handleLegacyStaticAdmin(w, r, "logs.html")
	case r.Method == http.MethodGet && r.URL.Path == "/usage":
		a.handleLegacyStaticAdmin(w, r, "usage.html")
	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/static/"):
		a.handleStaticAsset(w, r)
	case r.Method == http.MethodGet && r.URL.Path == "/v1/usage":
		a.handleUsage(w, r)
	case r.Method == http.MethodGet && r.URL.Path == "/v1/health":
		a.handleHealth(w, r)
	case r.Method == http.MethodPost && (r.URL.Path == "/v1/chat/completions" || r.URL.Path == "/chat/completions"):
		a.handleJSONProxy(w, r, validateChat)
	case r.Method == http.MethodPost && (r.URL.Path == "/v1/embeddings" || r.URL.Path == "/embeddings"):
		a.handleJSONProxy(w, r, validateEmbeddings)
	case r.Method == http.MethodPost && (r.URL.Path == "/v1/rerank" || r.URL.Path == "/rerank"):
		a.handleJSONProxy(w, r, validateRerank)
	case r.Method == http.MethodPost && (r.URL.Path == "/v1/messages" || r.URL.Path == "/messages") && a.messagesEndpointEnabled():
		a.handleMessages(w, r)
	case r.Method == http.MethodPost && (r.URL.Path == "/v1/responses" || r.URL.Path == "/responses") && a.responsesEndpointEnabled():
		a.handleResponses(w, r)
	case strings.HasPrefix(r.URL.Path, "/p1/"):
		a.handlePassthrough(w, r)
	default:
		writeError(w, http.StatusNotFound, "not found", "not_found")
	}
}

func runtimeMutationRequest(r *http.Request) bool {
	if r == nil {
		return false
	}
	path := r.URL.Path
	if isProviderDiscoveryPath(path) {
		return false
	}
	if r.Method == http.MethodPut && path == "/admin/config" || r.Method == http.MethodPost && path == "/admin/config/reload" {
		return true
	}
	if isAdminProvidersPath(path) || isAdminNodesPath(path) || isAdminClientModelsPath(path) || isAdminRouterSettingsPath(path) {
		return r.Method != http.MethodGet && r.Method != http.MethodHead
	}
	if isAdminKeysPath(path) {
		return r.Method != http.MethodGet && r.Method != http.MethodHead
	}
	return r.Method == http.MethodPost && (path == "/admin/models" || path == "/admin/models/copy") ||
		r.Method == http.MethodDelete && isAdminModelDeletePath(path)
}

func (a *App) messagesEndpointEnabled() bool {
	return a != nil && a.Config != nil && config.ParseBool(a.Config.ProxySettings.EnableMessagesEndpoint)
}

func (a *App) responsesEndpointEnabled() bool {
	return a != nil && a.Config != nil && config.ParseBool(a.Config.ProxySettings.EnableResponsesEndpoint)
}

func (a *App) quotaAwareSelectionEnabled() bool {
	return a != nil && a.Config != nil && config.ParseBoolStrict(a.Config.RouterSettings.QuotaAwareSelection.Enabled)
}

func (a *App) otelEnabled() bool {
	return a != nil && a.Observability != nil && a.Observability.Enabled()
}

func (a *App) traceModelName(modelName string) string {
	if a == nil || a.Router == nil {
		return "unknown"
	}
	return a.Router.TraceModelName(modelName)
}

type proxyRequestTraceContext struct {
	traceID string
	spanID  string
}

type proxyRequestTraceContextKey struct{}

func contextWithProxyRequestTrace(ctx context.Context, span observability.Span) context.Context {
	traceID := normalizeOTelTraceID(span.TraceID())
	spanID := normalizeOTelSpanID(span.SpanID())
	if traceID == "" && spanID == "" {
		return ctx
	}
	return context.WithValue(ctx, proxyRequestTraceContextKey{}, proxyRequestTraceContext{traceID: traceID, spanID: spanID})
}

func proxyRequestTraceFromContext(ctx context.Context) (string, string) {
	if ctx == nil {
		return "", ""
	}
	traceContext, ok := ctx.Value(proxyRequestTraceContextKey{}).(proxyRequestTraceContext)
	if !ok {
		return "", ""
	}
	return traceContext.traceID, traceContext.spanID
}

func normalizeOTelTraceID(value string) string {
	return normalizeOTelHexID(value, 32)
}

func normalizeOTelSpanID(value string) string {
	return normalizeOTelHexID(value, 16)
}

func normalizeOTelHexID(value string, wantLen int) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if len(value) != wantLen {
		return ""
	}
	for i := 0; i < len(value); i++ {
		c := value[i]
		if (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') {
			continue
		}
		return ""
	}
	return value
}

func (a *App) startProxyPhase(ctx context.Context, phase string, endpointKind string, modelName string, isStream bool) (context.Context, observability.Span) {
	if !a.otelEnabled() {
		return ctx, observability.Span{}
	}
	return a.Observability.StartPhaseSpan(ctx, phase, observability.PhaseAttrs{
		EndpointKind: endpointKind,
		Model:        a.traceModelName(modelName),
		Stream:       isStream,
	})
}

func (a *App) startProxyAttemptPhase(ctx context.Context, endpointKind string, modelName string, backendName string, attemptIndex int, retryIndex int, isStream bool) (context.Context, observability.Span) {
	if !a.otelEnabled() {
		return ctx, observability.Span{}
	}
	return a.Observability.StartPhaseSpan(ctx, observability.PhaseUpstreamAttempt, observability.PhaseAttrs{
		EndpointKind: endpointKind,
		Model:        a.traceModelName(modelName),
		BackendName:  backendName,
		AttemptIndex: attemptIndex,
		RetryIndex:   retryIndex,
		Stream:       isStream,
	})
}

func (a *App) recordRequestMetric(ctx context.Context, endpointKind string, modelName string, isStream bool, outcome string, statusCode int, err error, duration time.Duration) {
	traceModelName := ""
	if a.otelEnabled() || a.prometheusTimingsEnabled() {
		traceModelName = a.traceModelName(modelName)
	}
	if a.otelEnabled() {
		a.Observability.RecordRequest(ctx, observability.RequestMetric{
			EndpointKind: endpointKind,
			Model:        traceModelName,
			Stream:       isStream,
			Outcome:      outcome,
			StatusCode:   statusCode,
			Error:        err,
			Duration:     duration,
		})
	}
	if a != nil && a.PrometheusTimings != nil {
		a.PrometheusTimings.RecordRequest(prometheusRequestTiming{
			EndpointKind: endpointKind,
			Model:        traceModelName,
			Stream:       isStream,
			Outcome:      outcome,
			StatusCode:   statusCode,
			Error:        err,
			Duration:     duration,
		})
	}
}

func (a *App) recordBackendAttemptMetrics(endpointKind string, modelName string, isStream bool, result proxy.ForwardResult) {
	if a == nil || a.PrometheusTimings == nil || len(result.AttemptTrail) == 0 {
		return
	}
	traceModelName := a.traceModelName(modelName)
	for _, attempt := range result.AttemptTrail {
		a.PrometheusTimings.RecordBackendRequest(prometheusBackendTiming{
			EndpointKind: endpointKind,
			Model:        traceModelName,
			Backend:      attempt.BackendName,
			Stream:       isStream,
			Outcome:      attempt.Outcome,
			StatusCode:   attempt.Status,
			ErrorType:    prometheusAttemptErrorType(attempt),
			Duration:     time.Duration(attempt.DurationMS) * time.Millisecond,
		})
	}
}

func (a *App) recordPassthroughBackendAttemptMetric(endpointKind string, modelName string, backendName string, statusCode int, err error, duration time.Duration) {
	if a == nil || a.PrometheusTimings == nil {
		return
	}
	outcome := observability.OutcomeFinal
	if err != nil {
		outcome = observability.OutcomeError
	}
	a.PrometheusTimings.RecordBackendRequest(prometheusBackendTiming{
		EndpointKind: endpointKind,
		Model:        a.traceModelName(modelName),
		Backend:      backendName,
		Stream:       false,
		Outcome:      outcome,
		StatusCode:   statusCode,
		ErrorType:    observability.ErrorType(err, statusCode),
		Duration:     duration,
	})
}

func prometheusAttemptErrorType(attempt proxy.UpstreamAttempt) string {
	if errorType := observability.StatusErrorType(attempt.Status); errorType != "" {
		return errorType
	}
	if strings.TrimSpace(attempt.Error) == "" {
		return "none"
	}
	return observability.BucketErrorType(errors.New(attempt.Error))
}

func (a *App) recordTokenUsageMetric(ctx context.Context, endpointKind string, modelName string, isStream bool, usage map[string]any) {
	if !a.otelEnabled() || len(usage) == 0 {
		return
	}
	a.Observability.RecordTokenUsage(ctx, observability.TokenUsageMetric{
		EndpointKind: endpointKind,
		Model:        a.traceModelName(modelName),
		Stream:       isStream,
		Usage:        usage,
	})
}

func (a *App) newStreamTimingObserver(ctx context.Context, endpointKind string, modelName string, start time.Time, statusCode int) *streamTimingObserver {
	if statusCode >= 400 {
		return nil
	}
	var provider *observability.Provider
	if a.otelEnabled() {
		provider = a.Observability
	}
	prometheusTimings := (*prometheusTimingRecorder)(nil)
	if a != nil {
		prometheusTimings = a.PrometheusTimings
	}
	if provider == nil && prometheusTimings == nil {
		return nil
	}
	return &streamTimingObserver{
		ctx:               ctx,
		provider:          provider,
		prometheusTimings: prometheusTimings,
		endpointKind:      endpointKind,
		modelName:         a.traceModelName(modelName),
		start:             start,
	}
}

func otelOutcomeForStatus(status int) string {
	if status >= 400 || status == 499 {
		return observability.OutcomeError
	}
	return observability.OutcomeSuccess
}

func isAdminFrontendRequest(r *http.Request) bool {
	if r == nil || (r.Method != http.MethodGet && r.Method != http.MethodHead) {
		return false
	}
	path := r.URL.Path
	return path == "/admin" || path == "/admin/" || path == "/admin/models" || strings.HasPrefix(path, "/admin/assets/")
}

func (a *App) handleAdminFrontend(w http.ResponseWriter, r *http.Request) {
	relativePath := strings.TrimPrefix(r.URL.Path, "/admin")
	relativePath = strings.TrimPrefix(relativePath, "/")
	cleanPath := path.Clean(relativePath)
	if cleanPath == "." {
		cleanPath = "index.html"
	}
	if cleanPath == ".." || strings.HasPrefix(cleanPath, "../") || path.IsAbs(cleanPath) {
		writeError(w, http.StatusBadRequest, "invalid admin asset path", "invalid_request_error")
		return
	}

	indexPath := filepath.Join(adminDistDir, "index.html")
	if _, err := os.Stat(indexPath); err == nil {
		assetPath := filepath.Join(adminDistDir, filepath.FromSlash(cleanPath))
		if info, assetErr := os.Stat(assetPath); assetErr == nil && !info.IsDir() {
			http.ServeFile(w, r, assetPath)
			return
		}
		http.ServeFile(w, r, indexPath)
		return
	}

	if info, err := fs.Stat(embeddedAdminAssets, cleanPath); err != nil || info.IsDir() {
		cleanPath = "index.html"
	}
	contents, err := fs.ReadFile(embeddedAdminAssets, cleanPath)
	if err != nil {
		writeError(w, http.StatusNotFound, "admin frontend asset not found", "not_found")
		return
	}
	info, _ := fs.Stat(embeddedAdminAssets, cleanPath)
	http.ServeContent(w, r, path.Base(cleanPath), info.ModTime(), bytes.NewReader(contents))
}

func (a *App) handleLegacyStaticAdmin(w http.ResponseWriter, r *http.Request, filename string) {
	path := filepath.Join(config.ProjectRoot(), "static", "admin", filename)
	if _, err := os.Stat(path); err != nil {
		writeError(w, http.StatusNotFound, "legacy admin frontend file not found", "not_found")
		return
	}
	http.ServeFile(w, r, path)
}

func (a *App) handleStaticAsset(w http.ResponseWriter, r *http.Request) {
	relativePath := strings.TrimPrefix(r.URL.Path, "/static/")
	cleanPath := filepath.Clean(relativePath)
	if cleanPath == "." || strings.HasPrefix(cleanPath, "..") || filepath.IsAbs(cleanPath) {
		writeError(w, http.StatusBadRequest, "invalid static asset path", "invalid_request_error")
		return
	}
	path := filepath.Join(config.ProjectRoot(), "static", cleanPath)
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		writeError(w, http.StatusNotFound, "static asset not found", "not_found")
		return
	}
	http.ServeFile(w, r, path)
}

func (a *App) handleModels(w http.ResponseWriter, _ *http.Request) {
	data := make([]map[string]any, 0)
	for _, model := range a.Router.ListModelMetadata() {
		tags := []string{}
		if model.SupportsReasoning {
			tags = append(tags, "reasoning")
		}
		data = append(data, map[string]any{
			"id":                 model.Name,
			"object":             "model",
			"supports_reasoning": model.SupportsReasoning,
			"capabilities":       map[string]any{"reasoning": model.SupportsReasoning},
			"tags":               tags,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"object": "list", "data": data})
}

func (a *App) handleGetConfig(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, config.MaskSensitive(a.Config.ToMap()))
}

func (a *App) handleCacheStats(w http.ResponseWriter, r *http.Request) {
	if !a.requireAdminPassword(w, r, "cache statistics") {
		return
	}
	var cacheCfg config.InferenceCacheConfig
	var cache *proxy.InferenceCache
	if a != nil {
		if a.Config != nil {
			cacheCfg = a.Config.ProxySettings.InferenceCache
		}
		if a.Router != nil {
			cache = a.Router.InferenceCache
		}
	}
	writeJSON(w, http.StatusOK, proxy.InferenceCacheSnapshotForConfig(cacheCfg, cache))
}

func (a *App) handlePutConfig(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeError(w, http.StatusBadRequest, "failed to read request body", "invalid_request_error")
		return
	}
	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		writeError(w, http.StatusBadRequest, "Invalid JSON payload", "invalid_json")
		return
	}
	newCfg, issues := a.validateConfig(raw)
	if hasValidationErrors(issues) {
		writeConfigValidation(w, http.StatusBadRequest, issues)
		return
	}
	if err := a.applyConfigLocked(newCfg, true); err != nil {
		writeError(w, http.StatusBadRequest, err.Error(), "config_apply_failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok"})
}

func (a *App) handleValidateConfig(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeError(w, http.StatusBadRequest, "failed to read request body", "invalid_request_error")
		return
	}
	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		writeError(w, http.StatusBadRequest, "Invalid JSON payload", "invalid_json")
		return
	}
	_, issues := a.validateConfig(raw)
	writeConfigValidation(w, http.StatusOK, issues)
}

func (a *App) validateConfig(raw map[string]any) (*config.Config, []config.ValidationIssue) {
	if raw == nil {
		raw = map[string]any{}
	}
	if a != nil && a.Config != nil {
		raw = config.PreserveMissingSensitiveFields(raw, configMutationRaw(a.Config)).(map[string]any)
	}
	path := ""
	if a != nil && a.Config != nil {
		path = a.Config.Path
	}
	runtimeRaw := raw
	if path != "" {
		envValues, _ := config.LoadEnvValues(config.ResolveEnvPath(path, ""))
		if substituted, ok := config.SubstituteEnv(raw, envValues).(map[string]any); ok {
			runtimeRaw = substituted
		}
	}
	newCfg, err := config.FromMap(runtimeRaw, path)
	if err != nil {
		return nil, []config.ValidationIssue{{
			Path:     "$",
			Severity: "error",
			Code:     "invalid_config",
			Message:  err.Error(),
		}}
	}
	issues := newCfg.Validate()
	if !hasValidationErrors(issues) {
		routerCfg := *newCfg
		routerCfg.ProxySettings.InferenceCache.Persistence = config.InferenceCachePersistenceConfig{}
		validationRouter, err := proxy.NewRouter(&routerCfg)
		if err != nil {
			issues = append(issues, config.ValidationIssue{
				Path:     "model_list",
				Severity: "error",
				Code:     "router_build_failed",
				Message:  err.Error(),
			})
		} else if validationRouter.InferenceCache != nil {
			_ = validationRouter.InferenceCache.Close()
		}
	}
	newCfg.Raw = raw
	return newCfg, issues
}

func hasValidationErrors(issues []config.ValidationIssue) bool {
	for _, issue := range issues {
		if issue.Severity == "error" {
			return true
		}
	}
	return false
}

func writeConfigValidation(w http.ResponseWriter, status int, issues []config.ValidationIssue) {
	errorCount := 0
	warningCount := 0
	for _, issue := range issues {
		switch issue.Severity {
		case "error":
			errorCount++
		case "warning":
			warningCount++
		}
	}
	writeJSON(w, status, map[string]any{
		"ok":            errorCount == 0,
		"issues":        issues,
		"error_count":   errorCount,
		"warning_count": warningCount,
	})
}

func (a *App) handleReloadConfig(w http.ResponseWriter, _ *http.Request) {
	if a.Config.Path == "" {
		if err := a.applyConfigLocked(a.Config, false); err != nil {
			writeError(w, http.StatusBadRequest, err.Error(), "config_reload_failed")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"status": "ok"})
		return
	}
	newCfg, err := config.LoadWithOptions(config.LoadOptions{Path: a.Config.Path, SubstituteEnv: true})
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error(), "config_reload_failed")
		return
	}
	if err := a.applyConfigLocked(newCfg, false); err != nil {
		writeError(w, http.StatusBadRequest, err.Error(), "config_apply_failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok"})
}

func (a *App) applyConfig(newCfg *config.Config, persist bool) error {
	a.runtimeMu.Lock()
	defer a.runtimeMu.Unlock()
	return a.applyConfigLocked(newCfg, persist)
}

func (a *App) applyConfigLocked(newCfg *config.Config, persist bool) error {
	if a.root != nil {
		return errors.New("runtime config can only be applied to the root app under runtimeMu")
	}
	newRouter, err := proxy.NewRouter(newCfg)
	if err != nil {
		return err
	}
	if err := configureSemanticInferenceCache(newCfg, newRouter); err != nil {
		if newRouter.InferenceCache != nil {
			_ = newRouter.InferenceCache.Close()
		}
		return err
	}
	newObservability, err := observability.Configure(context.Background(), newCfg.ProxySettings.OTel)
	if err != nil {
		if newRouter.InferenceCache != nil {
			_ = newRouter.InferenceCache.Close()
		}
		return err
	}
	newRouter.Observability = newObservability
	newLogDB, err := logdb.NewStore(newCfg)
	if err != nil {
		if newRouter.InferenceCache != nil {
			_ = newRouter.InferenceCache.Close()
		}
		_ = shutdownObservabilityBounded(newObservability)
		return err
	}
	if persist {
		if err := newCfg.Save(""); err != nil {
			if newRouter.InferenceCache != nil {
				_ = newRouter.InferenceCache.Close()
			}
			_ = newLogDB.Close()
			_ = shutdownObservabilityBounded(newObservability)
			return err
		}
	}
	oldLogDB := a.LogDB
	oldObservability := a.Observability
	var oldInferenceCache *proxy.InferenceCache
	if a.Router != nil {
		oldInferenceCache = a.Router.InferenceCache
	}
	a.Config = newCfg
	a.Router = newRouter
	a.replaceCatalog(catalog.New(newCfg))
	a.Policy = policy.NewManager(newCfg)
	a.LogDB = newLogDB
	a.Observability = newObservability
	a.Payloads = logstore.NewFullRequestStore(newCfg)
	a.Quota = quota.NewManager()
	if a.Responses == nil {
		a.Responses = responsesapi.NewStore(1000)
	}
	a.Responses.SetPersistent(newLogDB)
	seedQuotaFromLogDB(context.Background(), a.Quota, newLogDB, newCfg)
	seedBackendQuotaFromLogDB(context.Background(), a.Quota, newLogDB, newCfg)
	a.PrometheusTimings = newAppPrometheusTimingRecorder(newCfg.ProxySettings.Metrics)
	retired := a.currentGeneration()
	a.generation.Store(&runtimeGeneration{})
	retired.retire(func() {
		if oldInferenceCache != nil {
			_ = oldInferenceCache.Close()
		}
		_ = oldLogDB.Close()
		_ = shutdownObservabilityBounded(oldObservability)
	})
	return nil
}

func (a *App) currentCatalog() *catalog.Catalog {
	if a == nil {
		return nil
	}
	a = a.runtimeRoot()
	a.catalogMu.RLock()
	defer a.catalogMu.RUnlock()
	return a.Catalog
}

func (a *App) replaceCatalog(c *catalog.Catalog) {
	if a == nil {
		return
	}
	a = a.runtimeRoot()
	a.catalogMu.Lock()
	defer a.catalogMu.Unlock()
	a.Catalog = c
}

func (a *App) handleAdminModels(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeError(w, http.StatusBadRequest, "failed to read request body", "invalid_request_error")
		return
	}
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		writeError(w, http.StatusBadRequest, "Invalid JSON payload", "invalid_json")
		return
	}
	entry, fallbacks, err := adminModelEntryFromPayload(payload)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error(), "missing_parameter")
		return
	}
	name := entry.ModelName
	apiBase := stringValue(entry.ModelParams["api_base"])
	if name == "" || apiBase == "" {
		writeError(w, http.StatusBadRequest, "model_name and api_base are required", "missing_parameter")
		return
	}
	if existing, ok := a.Config.FindLegacyModel(name); ok && config.ParseBool(existing.Protected) && !a.hasAdminPassword(r) {
		writeError(w, http.StatusForbidden, "admin password required for protected model", "permission_error")
		return
	}
	_, replaced := a.Config.FindLegacyModel(name)
	raw := configMutationRaw(a.Config)
	candidate, err := config.FromMap(raw, a.Config.Path)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error(), "invalid_config")
		return
	}
	candidate.UpsertLegacyModel(entry, fallbacks)
	newCfg, issues := a.validateConfig(candidate.Raw)
	if hasValidationErrors(issues) {
		writeConfigValidation(w, http.StatusBadRequest, issues)
		return
	}
	if err := a.applyConfigLocked(newCfg, true); err != nil {
		writeError(w, http.StatusBadRequest, err.Error(), "config_apply_failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "model": name, "replaced": replaced})
}

func adminModelEntryFromPayload(payload map[string]any) (config.ModelEntry, []string, error) {
	params := map[string]any{}
	if nested, ok := payload["model_params"].(map[string]any); ok {
		for key, value := range nested {
			params[key] = value
		}
	}
	for _, key := range []string{"api_base", "api_key", "model", "api_type", "mode", "scope", "auth_url", "client_cert", "client_key", "client_id", "client_secret", "verify_ssl", "emulate_tool_calls"} {
		if value, ok := payload[key]; ok {
			if text, isString := value.(string); isString && strings.TrimSpace(text) == "" {
				continue
			}
			params[key] = value
		}
	}
	name := strings.TrimSpace(stringValue(payload["model_name"]))
	apiBase := strings.TrimSpace(stringValue(params["api_base"]))
	if name == "" || apiBase == "" {
		return config.ModelEntry{}, nil, fmt.Errorf("model_name and api_base are required")
	}
	apiType := firstNonEmpty(stringValue(params["api_type"]), "openai")
	params["api_type"] = apiType
	params["api_base"] = apiBase
	params["model"] = firstNonEmpty(stringValue(params["model"]), name)
	if apiType == "gigachat" {
		clientID := stringValue(params["client_id"])
		clientSecret := stringValue(params["client_secret"])
		if clientID != "" && clientSecret != "" {
			params["api_key"] = base64.StdEncoding.EncodeToString([]byte(clientID + ":" + clientSecret))
		}
	}
	for key, value := range params {
		if text, ok := value.(string); ok && strings.TrimSpace(text) == "" {
			delete(params, key)
		}
	}
	return config.ModelEntry{ModelName: name, Protected: false, ModelParams: params}, stringSlice(payload["fallbacks"]), nil
}

func (a *App) RouterRegister(entry config.ModelEntry, fallbacks []string) (bool, error) {
	backend, ok, err := proxy.BackendFromModelEntry(entry)
	if err != nil {
		return false, err
	}
	if !ok {
		return false, fmt.Errorf("model_name and api_base are required")
	}
	backend.Editable = true
	return a.Router.RegisterBackend(backend, fallbacks), nil
}

func (a *App) hasAdminPassword(r *http.Request) bool {
	expected := a.expectedAdminPassword()
	if expected == "" || r == nil {
		return false
	}
	provided := r.Header.Get("x-admin-password")
	if provided == "" {
		provided = r.URL.Query().Get("admin_password")
	}
	return subtle.ConstantTimeCompare([]byte(provided), []byte(expected)) == 1
}

func sensitiveAdminAPIRequest(r *http.Request) bool {
	if r == nil {
		return false
	}
	path := r.URL.Path
	return path == "/admin/config" ||
		path == "/admin/config/validate" ||
		path == "/admin/config/reload" ||
		path == "/admin/logs" ||
		strings.HasPrefix(path, "/admin/logs/") ||
		strings.HasPrefix(path, "/admin/traces/") ||
		path == "/api/logs" ||
		strings.HasPrefix(path, "/api/logs/")
}

func (a *App) requireConfiguredAdminPassword(w http.ResponseWriter, r *http.Request, subject string) bool {
	if a.expectedAdminPassword() == "" {
		return true
	}
	return a.requireAdminPassword(w, r, subject)
}

func (a *App) expectedAdminPassword() string {
	if expected := strings.TrimSpace(os.Getenv(config.AdminPasswordEnv)); expected != "" {
		return expected
	}
	if a == nil || a.Config == nil || a.Config.Path == "" {
		return ""
	}
	envValues, err := config.LoadEnvValues(config.ResolveEnvPath(a.Config.Path, ""))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(envValues[config.AdminPasswordEnv])
}

func (a *App) handleLogs(w http.ResponseWriter, r *http.Request) {
	opts := logQueryOptions(r)
	if opts.TraceID != "" && r.URL.Path == "/admin/logs" && !a.requireAdminPassword(w, r, "trace navigation") {
		return
	}
	if a != nil && a.LogDB != nil && a.LogDB.Enabled() {
		result, err := a.LogDB.Query(r.Context(), opts)
		if err == nil {
			records := a.enrichLogRecords(r.Context(), result.Records)
			writeJSON(w, http.StatusOK, map[string]any{
				"data":     records,
				"logs":     records,
				"total":    result.Total,
				"limit":    result.Limit,
				"offset":   result.Offset,
				"has_more": result.HasMore,
			})
			return
		}
	}
	records := a.memoryLogRecords(opts.TraceID)
	writeJSON(w, http.StatusOK, map[string]any{
		"data":     records,
		"logs":     records,
		"total":    len(records),
		"limit":    len(records),
		"offset":   0,
		"has_more": false,
	})
}

func (a *App) handleTraceDetail(w http.ResponseWriter, r *http.Request) {
	if !a.requireAdminPassword(w, r, "trace navigation") {
		return
	}
	traceID := normalizeOTelTraceID(traceIDFromPath(r.URL.Path))
	if traceID == "" {
		writeError(w, http.StatusBadRequest, "invalid trace ID format", "invalid_request_error")
		return
	}
	if a == nil || a.LogDB == nil || !a.LogDB.Enabled() {
		writeError(w, http.StatusNotFound, "trace not found", "not_found")
		return
	}
	result, err := a.LogDB.Query(r.Context(), logdb.QueryOptions{
		TraceID: traceID,
		Limit:   intQuery(r, "limit", 100),
		Offset:  intQuery(r, "offset", 0),
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error(), "trace_lookup_failed")
		return
	}
	if result.Total == 0 {
		writeError(w, http.StatusNotFound, "trace not found", "not_found")
		return
	}
	for i := range result.Records {
		attempts, err := a.LogDB.AttemptsForRequest(r.Context(), result.Records[i].ID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error(), "trace_lookup_failed")
			return
		}
		result.Records[i].AttemptTrail = attempts
	}
	writeJSON(w, http.StatusOK, a.traceDetail(r.Context(), traceID, result))
}

func (a *App) handleLogDetail(w http.ResponseWriter, r *http.Request) {
	if a == nil || a.LogDB == nil || !a.LogDB.Enabled() {
		if a == nil || a.Logs == nil {
			writeError(w, http.StatusNotFound, "log database is not configured", "not_found")
			return
		}
		id, err := strconv.ParseInt(logIDFromPath(r.URL.Path), 10, 64)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid log ID format", "invalid_request_error")
			return
		}
		entry, ok := a.Logs.Get(id)
		if !ok {
			writeError(w, http.StatusNotFound, "log not found", "not_found")
			return
		}
		writeJSON(w, http.StatusOK, a.memoryLogDetail(entry, intQuery(r, "body_max_chars", 100000)))
		return
	}
	id := logIDFromPath(r.URL.Path)
	if _, err := uuid.Parse(id); err != nil {
		writeError(w, http.StatusBadRequest, "invalid log ID format", "invalid_request_error")
		return
	}
	record, ok, err := a.LogDB.Get(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error(), "log_lookup_failed")
		return
	}
	if !ok {
		writeError(w, http.StatusNotFound, "log not found", "not_found")
		return
	}
	writeJSON(w, http.StatusOK, a.logDetail(r.Context(), record, intQuery(r, "body_max_chars", 100000)))
}

func (a *App) handleLogStopReasons(w http.ResponseWriter, r *http.Request) {
	if a == nil || a.LogDB == nil || !a.LogDB.Enabled() {
		writeError(w, http.StatusNotFound, "log database is not configured", "not_found")
		return
	}
	start, end := logTimeRange(r)
	counts, actualStart, actualEnd, err := a.LogDB.StopReasonCounts(r.Context(), start, end)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error(), "log_analytics_failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"stop_reasons": counts,
		"time_range":   timeRangePayload(actualStart, actualEnd),
	})
}

func (a *App) handleLogToolCallRate(w http.ResponseWriter, r *http.Request) {
	if a == nil || a.LogDB == nil || !a.LogDB.Enabled() {
		writeError(w, http.StatusNotFound, "log database is not configured", "not_found")
		return
	}
	start, end := logTimeRange(r)
	rate, err := a.LogDB.ToolCallRate(r.Context(), start, end)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error(), "log_analytics_failed")
		return
	}
	writeJSON(w, http.StatusOK, rate)
}

func (a *App) handleLogModels(w http.ResponseWriter, r *http.Request) {
	if a == nil || a.LogDB == nil || !a.LogDB.Enabled() {
		writeError(w, http.StatusNotFound, "log database is not configured", "not_found")
		return
	}
	start, end := logTimeRange(r)
	models, actualStart, actualEnd, err := a.LogDB.RequestsPerModelWithStopReasons(r.Context(), start, end, intQuery(r, "limit", 100))
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error(), "log_analytics_failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"models":     models,
		"time_range": timeRangePayload(actualStart, actualEnd),
	})
}

func (a *App) handleUsage(w http.ResponseWriter, _ *http.Request) {
	metrics := a.Admission.Metrics()
	raw := map[string]any{}
	buf, _ := json.Marshal(metrics)
	_ = json.Unmarshal(buf, &raw)
	if a != nil && a.Quota != nil {
		raw["quota"] = a.Quota.Metrics()
	}
	writeJSON(w, http.StatusOK, raw)
}

type backendQuotaState struct {
	Backend         string     `json:"backend"`
	UsedTokens      int        `json:"used_tokens"`
	LimitTokens     int        `json:"limit_tokens"`
	RemainingTokens int        `json:"remaining_tokens"`
	ResetAt         *time.Time `json:"reset_at,omitempty"`
}

func (a *App) handleBackendQuotas(w http.ResponseWriter, r *http.Request) {
	if !a.requireAdminPassword(w, r, "backend quota observability") {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"quota_aware_selection_enabled": a.quotaAwareSelectionEnabled(),
		"persistence_enabled":           backendQuotaPersistenceEnabled(a.Config),
		"backends":                      a.backendQuotaStates(),
	})
}

func (a *App) backendQuotaStates() []backendQuotaState {
	var cfg *config.Config
	if a != nil {
		cfg = a.Config
	}
	limitsByBackend := configuredBackendQuotaLimits(cfg)
	if len(limitsByBackend) == 0 {
		return []backendQuotaState{}
	}
	metrics := quota.Metrics{UsedByScope: map[string]int{}}
	if a != nil && a.Quota != nil {
		metrics = a.Quota.Metrics()
	}
	names := make([]string, 0, len(limitsByBackend))
	for name := range limitsByBackend {
		names = append(names, name)
	}
	sort.Strings(names)
	out := make([]backendQuotaState, 0, len(names))
	for _, name := range names {
		limits := limitsByBackend[name]
		scope := quota.BackendScope(name)
		used := metrics.UsedByScope[scope]
		remaining := limits.TokenQuota - used
		if remaining < 0 {
			remaining = 0
		}
		var resetAt *time.Time
		if value := metrics.ResetAtByScope[scope]; !value.IsZero() {
			copied := value
			resetAt = &copied
		}
		out = append(out, backendQuotaState{
			Backend:         name,
			UsedTokens:      used,
			LimitTokens:     limits.TokenQuota,
			RemainingTokens: remaining,
			ResetAt:         resetAt,
		})
	}
	return out
}

func (a *App) handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"data": a.Router.HealthSnapshot()})
}

func (a *App) ActiveHealthCheckOnce(ctx context.Context) []proxy.BackendHealth {
	if a == nil {
		return nil
	}
	view, release := a.acquireRuntime()
	defer release()
	if view.Router == nil {
		return nil
	}
	return view.Router.ActiveHealthCheckOnce(ctx)
}

func (a *App) StartHealthChecks(ctx context.Context) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			view, release := a.acquireRuntime()
			router := view.Router
			interval := time.Second
			if router != nil && router.ActiveHealthChecks {
				router.ActiveHealthCheckOnce(ctx)
				interval = router.HealthCheckInterval
				if interval <= 0 {
					interval = 15 * time.Second
				}
			}
			release()
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

func (a *App) StartModelCatalog(ctx context.Context) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			catalog := a.currentCatalog()
			interval := time.Minute
			if catalog != nil {
				catalog.RunOnce(ctx)
				if snapshot := catalog.Snapshot(); snapshot.Enabled {
					interval = catalog.Interval()
				}
			}
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

type validator func(map[string]any) error

func (a *App) handleJSONProxy(w http.ResponseWriter, r *http.Request, validate validator) {
	start := time.Now()
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeError(w, http.StatusBadRequest, "failed to read request body", "invalid_request_error")
		return
	}
	payload, err := proxy.DecodeJSONBody(body)
	if err != nil {
		writeError(w, http.StatusBadRequest, "Invalid JSON payload", "invalid_json")
		entry := a.addLog(r, LogEntry{Time: start, Method: r.Method, Path: r.URL.Path, Status: http.StatusBadRequest, Error: err.Error(), DurationMS: time.Since(start).Milliseconds()})
		payloadPath := a.storePayload(entry, r, body, nil, nil)
		a.persistLog(r.Context(), entry, r, false, policy.Context{}, payloadPath, nil)
		return
	}
	modelName := proxy.NormalizeRequestModel(payload["model"])
	if modelName == "" {
		writeError(w, http.StatusBadRequest, "You must provide a model parameter", "missing_parameter")
		return
	}
	isStream, _ := payload["stream"].(bool)
	endpointKind := observability.EndpointKind(r.URL.Path)
	requestStatus := 0
	requestOutcome := observability.OutcomeError
	var requestErr error
	var requestSpan observability.Span
	if a.requestMetricsEnabled() {
		if a.otelEnabled() {
			ctx, span := a.Observability.StartRequestSpan(r.Context(), observability.RequestAttrs{
				EndpointKind: endpointKind,
				Model:        a.traceModelName(modelName),
				KeyScope:     "unknown",
				Stream:       isStream,
			})
			ctx = contextWithProxyRequestTrace(ctx, span)
			r = r.WithContext(ctx)
			requestSpan = span
		}
		defer func() {
			a.recordRequestMetric(r.Context(), endpointKind, modelName, isStream, requestOutcome, requestStatus, requestErr, time.Since(start))
			requestSpan.End(observability.SpanResult{
				Outcome:    requestOutcome,
				StatusCode: requestStatus,
				Error:      requestErr,
			})
		}()
	}
	if err := validate(payload); err != nil {
		requestStatus = http.StatusBadRequest
		requestErr = err
		writeError(w, http.StatusBadRequest, err.Error(), "invalid_request_error")
		entry := a.addLog(r, LogEntry{Time: start, Method: r.Method, Path: r.URL.Path, Model: modelName, Status: http.StatusBadRequest, Error: err.Error(), DurationMS: time.Since(start).Milliseconds()})
		payloadPath := a.storePayload(entry, r, body, nil, nil)
		a.persistLog(r.Context(), entry, r, false, policy.Context{}, payloadPath, nil)
		return
	}
	policyCtx, policySpan := a.startProxyPhase(r.Context(), observability.PhasePolicy, endpointKind, modelName, isStream)
	pctx, err := a.Policy.ValidateRequest(r.WithContext(policyCtx), modelName)
	if err != nil {
		status, _, code := a.policyErrorDetails(err)
		policySpan.End(observability.SpanResult{Outcome: observability.OutcomeError, StatusCode: status, Error: err})
		requestStatus = status
		requestErr = err
		writeError(w, status, err.Error(), code)
		return
	}
	policySpan.End(observability.SpanResult{Outcome: observability.OutcomeSuccess})
	requestSpan.SetKeyScope(observability.KeyScope(pctx.Authenticated, pctx.KeyID, pctx.GroupID))
	r = requestWithRequiredHeaderAttribution(r, pctx.RequiredHeaders)
	limits := a.effectiveLimits(modelName, pctx)
	limitKeyID := pctx.LimitKeyID()
	_, admissionSpan := a.startProxyPhase(r.Context(), observability.PhaseAdmission, endpointKind, modelName, isStream)
	if err := a.Admission.CheckRateLimit(limitKeyID, modelName, admission.RateLimits{RPS: limits.RPS, RPM: limits.RPM}); err != nil {
		admissionSpan.End(observability.SpanResult{Outcome: observability.OutcomeError, StatusCode: http.StatusTooManyRequests, Error: err})
		requestStatus = http.StatusTooManyRequests
		requestErr = err
		writeError(w, http.StatusTooManyRequests, "Rate limit exceeded", "rate_limit_exceeded")
		entry := a.addLog(r, LogEntry{Time: start, Method: r.Method, Path: r.URL.Path, Model: modelName, Status: http.StatusTooManyRequests, Error: err.Error(), DurationMS: time.Since(start).Milliseconds()})
		payloadPath := a.storePayload(entry, r, body, nil, nil)
		a.persistLog(r.Context(), entry, r, false, pctx, payloadPath, nil)
		return
	}
	estimatedTokens := estimateRequestTokens(payload)
	tokenReservation, err := a.Quota.ReserveWithWindowSpec(limitKeyID, modelName, limits.TokenQuota, quotaWindowSpec(limits), estimatedTokens)
	if err != nil {
		admissionSpan.End(observability.SpanResult{Outcome: observability.OutcomeError, StatusCode: http.StatusTooManyRequests, Error: err})
		requestStatus = http.StatusTooManyRequests
		requestErr = err
		writeError(w, http.StatusTooManyRequests, "Token quota exceeded", "token_quota_exceeded")
		entry := a.addLog(r, LogEntry{Time: start, Method: r.Method, Path: r.URL.Path, Model: modelName, Status: http.StatusTooManyRequests, Error: err.Error(), DurationMS: time.Since(start).Milliseconds()})
		payloadPath := a.storePayload(entry, r, body, nil, nil)
		a.persistLog(r.Context(), entry, r, false, pctx, payloadPath, nil)
		return
	}
	slot, err := a.acquireSlot(r.Context(), modelName, pctx, limits)
	if err != nil {
		tokenReservation.Release()
		if errors.Is(err, admission.ErrQueueTimeout) {
			admissionSpan.End(observability.SpanResult{Outcome: observability.OutcomeError, StatusCode: http.StatusTooManyRequests, Error: err})
			requestStatus = http.StatusTooManyRequests
			writeError(w, http.StatusTooManyRequests, "Request queued too long - concurrency limit reached", "concurrency_limit_exceeded")
		} else {
			admissionSpan.End(observability.SpanResult{Outcome: observability.OutcomeError, StatusCode: 499, Error: err})
			requestStatus = 499
			w.WriteHeader(499)
		}
		requestErr = err
		return
	}
	admissionSpan.End(observability.SpanResult{Outcome: observability.OutcomeSuccess})

	var parsedStreamSidecars *logstore.ParsedStreamSidecars
	if isStream && a.captureParsedStreamSidecars() {
		parsedStreamSidecars, _ = a.Payloads.NewParsedStreamSidecars(parsedStreamSidecarMaxBytes)
	}
	var parsedStreamCapture *proxy.ParsedStreamCapture
	if parsedStreamSidecars != nil {
		parsedStreamCapture = &proxy.ParsedStreamCapture{
			Raw:         parsedStreamSidecars.RawWriter(),
			Transformed: parsedStreamSidecars.TransformedWriter(),
		}
	}
	cacheBypass := proxy.InferenceCacheBypassRequested(r.Header)
	cacheScope := inferenceCacheScope(pctx)
	forwardOpts := proxy.ForwardOptions{
		BeforeAttempt:         a.backendAttemptLimiter,
		BackendAvailable:      a.backendRateLimitAvailable,
		QuotaAvailability:     a.backendQuotaAvailability(estimatedTokens),
		KeyID:                 pctx.KeyID,
		Cacheable:             exactInferenceCachePath(r.URL.Path),
		CacheBypass:           cacheBypass,
		CacheScope:            cacheScope,
		CaptureParsedResponse: a.captureParsedResponseSidecars(),
		ParsedStreamCapture:   parsedStreamCapture,
	}
	forwardResult, err := a.Router.ForwardWithOptions(r.Context(), modelName, r.URL.Path, r.URL.RawQuery, body, payload, isStream, a.outboundHeaders(r), forwardOpts)
	a.recordBackendAttemptMetrics(endpointKind, modelName, isStream, forwardResult)
	if err != nil {
		if parsedStreamSidecars != nil {
			_ = parsedStreamSidecars.Close()
			parsedStreamSidecars.RemoveIfEmpty()
		}
		slot.Release()
		tokenReservation.Release()
		if status, ok := a.writeBackendForwardError(w, err); ok {
			requestStatus = status
			requestErr = err
			entry := a.addLog(r, withForwardWarnings(LogEntry{Time: start, Method: r.Method, Path: r.URL.Path, Model: modelName, Status: status, Backend: forwardResult.BackendName, Error: err.Error(), DurationMS: time.Since(start).Milliseconds()}, forwardResult))
			payloadPath := a.storePayload(entry, r, body, nil, nil)
			a.persistLog(r.Context(), entry, r, false, pctx, payloadPath, nil)
			return
		}
		requestStatus = http.StatusBadGateway
		requestErr = err
		writeError(w, http.StatusBadGateway, err.Error(), "backend_error")
		entry := a.addLog(r, withForwardWarnings(LogEntry{Time: start, Method: r.Method, Path: r.URL.Path, Model: modelName, Status: http.StatusBadGateway, Backend: forwardResult.BackendName, Error: err.Error(), DurationMS: time.Since(start).Milliseconds()}, forwardResult))
		payloadPath := a.storePayload(entry, r, body, nil, nil)
		a.persistLog(r.Context(), entry, r, false, pctx, payloadPath, nil)
		return
	}
	resp := forwardResult.Response
	defer resp.Body.Close()
	_, responseSpan := a.startProxyPhase(r.Context(), observability.PhaseResponse, endpointKind, modelName, isStream)
	copyHeaders(w.Header(), resp.Header)
	if isStream {
		if w.Header().Get("Content-Type") == "" {
			w.Header().Set("Content-Type", "text/event-stream")
		}
		if parsedStreamSidecars != nil {
			parsedStreamSidecars.SetContentType(resp.Header.Get("Content-Type"))
		}
		w.WriteHeader(resp.StatusCode)
		streamUsage := &streamUsageReadCloser{ReadCloser: resp.Body}
		streamReader := &releaseOnEOF{ReadCloser: streamUsage, release: func() {
			slot.Release()
			if resp.StatusCode >= 400 {
				tokenReservation.Release()
			} else {
				a.commitTokenUsage(tokenReservation, forwardResult, streamUsage.TotalTokens())
			}
		}}
		_, streamErr := copyStreaming(w, streamReader, a.newStreamTimingObserver(r.Context(), endpointKind, modelName, start, resp.StatusCode))
		if streamErr != nil {
			_ = streamReader.Close()
		}
		responseOutcome := otelOutcomeForStatus(resp.StatusCode)
		if streamErr != nil {
			responseOutcome = observability.OutcomeError
			requestErr = streamErr
		}
		responseSpan.End(observability.SpanResult{Outcome: responseOutcome, BackendName: forwardResult.BackendName, StatusCode: resp.StatusCode, Error: streamErr})
		if parsedStreamSidecars != nil {
			_ = parsedStreamSidecars.Close()
		}
		requestStatus = resp.StatusCode
		requestOutcome = responseOutcome
		if resp.StatusCode < 400 && streamErr == nil {
			a.recordTokenUsageMetric(r.Context(), endpointKind, modelName, true, streamUsage.UsageStats())
		}
		entry := a.addLog(r, withForwardWarnings(LogEntry{Time: start, Method: r.Method, Path: r.URL.Path, Model: modelName, Status: resp.StatusCode, Backend: forwardResult.BackendName, DurationMS: time.Since(start).Milliseconds()}, forwardResult))
		payloadPath := a.storePayloadWithParsedArtifacts(entry, r, body, resp.Header, nil, nil, parsedStreamSidecars)
		a.persistLogWithUsage(r.Context(), entry, r, true, pctx, payloadPath, nil, streamUsage.UsageStats())
	} else {
		defer slot.Release()
		respBody, readErr := io.ReadAll(resp.Body)
		if readErr != nil {
			tokenReservation.Release()
			responseSpan.End(observability.SpanResult{Outcome: observability.OutcomeError, BackendName: forwardResult.BackendName, StatusCode: http.StatusBadGateway, Error: readErr})
			requestStatus = http.StatusBadGateway
			requestErr = readErr
			writeError(w, http.StatusBadGateway, readErr.Error(), "backend_read_error")
			entry := a.addLog(r, withForwardWarnings(LogEntry{Time: start, Method: r.Method, Path: r.URL.Path, Model: modelName, Status: http.StatusBadGateway, Backend: forwardResult.BackendName, Error: readErr.Error(), DurationMS: time.Since(start).Milliseconds()}, forwardResult))
			payloadPath := a.storePayload(entry, r, body, resp.Header, nil)
			a.persistLog(r.Context(), entry, r, false, pctx, payloadPath, nil)
			return
		}
		w.WriteHeader(resp.StatusCode)
		_, _ = w.Write(respBody)
		if !forwardResult.SemanticCacheHit {
			a.storeSemanticInferenceCache(r.Context(), modelName, r.URL.Path, r.URL.RawQuery, body, payload, isStream, forwardOpts, forwardResult.BackendName, resp.StatusCode, resp.Header, respBody)
		}
		if resp.StatusCode >= 400 {
			tokenReservation.Release()
		} else {
			usage, _, _ := logdb.ExtractUsageAndOutcome(respBody)
			a.commitTokenUsage(tokenReservation, forwardResult, totalTokensFromUsage(usage))
			a.recordTokenUsageMetric(r.Context(), endpointKind, modelName, false, usage)
		}
		responseSpan.End(observability.SpanResult{Outcome: otelOutcomeForStatus(resp.StatusCode), BackendName: forwardResult.BackendName, StatusCode: resp.StatusCode})
		requestStatus = resp.StatusCode
		requestOutcome = otelOutcomeForStatus(resp.StatusCode)
		entry := a.addLog(r, withForwardWarnings(LogEntry{Time: start, Method: r.Method, Path: r.URL.Path, Model: modelName, Status: resp.StatusCode, Backend: forwardResult.BackendName, DurationMS: time.Since(start).Milliseconds()}, forwardResult))
		payloadPath := a.storePayloadWithParsedResponse(entry, r, body, resp.Header, respBody, forwardResult.ParsedResponse)
		a.persistLog(r.Context(), entry, r, false, pctx, payloadPath, respBody)
	}
}

func (a *App) acquireSlot(ctx context.Context, modelName string, pctx policy.Context, limits config.LimitsConfig) (*admission.Slot, error) {
	limit := limits.Concurrency
	priority := limits.Priority
	timeout := limits.QueueTimeoutSeconds
	if priority == 0 {
		priority = 100
	}
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, time.Duration(timeout*float64(time.Second)))
		defer cancel()
	}
	return a.Admission.Acquire(ctx, pctx.LimitKeyID(), limit, priority, modelName)
}

func exactInferenceCachePath(path string) bool {
	return path == "/v1/chat/completions" || path == "/v1/embeddings" || path == "/v1/messages" || path == "/v1/responses"
}

func semanticInferenceCachePath(path string) bool {
	return path == "/v1/chat/completions"
}

func (a *App) storeSemanticInferenceCache(ctx context.Context, modelName string, path string, rawQuery string, body []byte, payload map[string]any, isStream bool, opts proxy.ForwardOptions, backendName string, status int, header http.Header, responseBody []byte) bool {
	if a == nil || a.Router == nil || a.Router.InferenceCache == nil || !opts.Cacheable || opts.CacheBypass || isStream || !semanticInferenceCachePath(path) {
		return false
	}
	semanticCache := a.Router.InferenceCache.SemanticCache()
	if semanticCache == nil || !semanticCache.CanWrite() {
		return false
	}
	backend, ok := a.Router.GetBackend(backendName)
	if !ok {
		return false
	}
	bodyResult := proxy.BuildBackendBodyWithWarnings(payload, backend, body, false)
	return a.Router.InferenceCache.StoreSemantic(ctx, proxy.SemanticCacheRequest{
		Path:        path,
		RawQuery:    rawQuery,
		BackendName: backend.Name,
		Scope:       opts.CacheScope,
		ModelName:   modelName,
		Body:        bodyResult.Body,
	}, status, proxy.FilterResponseHeaders(header), responseBody)
}

func inferenceCacheScope(pctx policy.Context) string {
	parts := []string{}
	if !pctx.Authenticated {
		parts = append(parts, "unauthenticated")
	} else if pctx.KeyID != "" {
		parts = append(parts, "key:"+pctx.KeyID)
	}
	if pctx.GroupID != "" {
		parts = append(parts, "group:"+pctx.GroupID)
	}
	if len(pctx.RequiredHeaders) > 0 {
		names := make([]string, 0, len(pctx.RequiredHeaders))
		for name := range pctx.RequiredHeaders {
			names = append(names, name)
		}
		sort.Strings(names)
		pairs := make([][2]string, 0, len(names))
		for _, name := range names {
			pairs = append(pairs, [2]string{name, pctx.RequiredHeaders[name]})
		}
		buf, _ := json.Marshal(pairs)
		parts = append(parts, "required_headers:"+string(buf))
	}
	if len(parts) == 0 {
		return "authenticated"
	}
	return strings.Join(parts, ";")
}

func (a *App) backendAttemptLimiter(ctx context.Context, backend proxy.Backend) (func(), error) {
	limits := backend.Limits
	if limits.RPS <= 0 && limits.RPM <= 0 && limits.Concurrency <= 0 {
		return nil, nil
	}
	scope := backendAdmissionScope(backend)
	if err := a.Admission.CheckRateLimit(scope, "", admission.RateLimits{RPS: limits.RPS, RPM: limits.RPM}); err != nil {
		return nil, err
	}
	if limits.Concurrency <= 0 {
		return nil, nil
	}
	priority := limits.Priority
	if priority == 0 {
		priority = 100
	}
	if limits.QueueTimeoutSeconds > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, time.Duration(limits.QueueTimeoutSeconds*float64(time.Second)))
		defer cancel()
	}
	slot, err := a.Admission.Acquire(ctx, scope, limits.Concurrency, priority, "")
	if err != nil {
		return nil, err
	}
	return slot.Release, nil
}

func (a *App) backendRateLimitAvailable(backend proxy.Backend) bool {
	limits := backend.Limits
	if limits.RPS <= 0 && limits.RPM <= 0 {
		return true
	}
	return a.Admission.RateLimitAvailable(backendAdmissionScope(backend), "", admission.RateLimits{RPS: limits.RPS, RPM: limits.RPM})
}

func backendAdmissionScope(backend proxy.Backend) string {
	if backend.RateLimitScope != "" {
		return backend.RateLimitScope
	}
	return "backend:" + backend.Name
}

func (a *App) backendQuotaAvailability(estimatedTokens int) proxy.BackendAvailability {
	if !a.quotaAwareSelectionEnabled() {
		return nil
	}
	return func(backend proxy.Backend) bool {
		return a.backendQuotaAvailable(backend, estimatedTokens)
	}
}

func (a *App) backendQuotaAvailable(backend proxy.Backend, estimatedTokens int) bool {
	limits := backend.Limits
	if a == nil || a.Quota == nil || limits.TokenQuota <= 0 || estimatedTokens <= 0 {
		return true
	}
	return a.Quota.WouldAccept(quota.BackendScopeKey, backend.Name, limits.TokenQuota, quotaWindowSpec(limits), estimatedTokens)
}

func (a *App) commitTokenUsage(reservation *quota.Reservation, result proxy.ForwardResult, actualTokens int) {
	if reservation != nil {
		reservation.Commit(actualTokens)
	}
	a.recordBackendQuotaUsage(result, actualTokens)
}

func (a *App) recordBackendQuotaUsage(result proxy.ForwardResult, tokens int) {
	if !a.quotaAwareSelectionEnabled() || a == nil || a.Router == nil || a.Quota == nil || tokens <= 0 || result.BackendName == "" || len(result.AttemptTrail) == 0 {
		return
	}
	backend, ok := a.Router.GetBackend(result.BackendName)
	if !ok || backend.Limits.TokenQuota <= 0 {
		return
	}
	a.Quota.RecordUsageWithWindowSpec(quota.BackendScopeKey, backend.Name, backend.Limits.TokenQuota, quotaWindowSpec(backend.Limits), tokens)
}

func (a *App) writeBackendForwardError(w http.ResponseWriter, err error) (int, bool) {
	if errors.Is(err, proxy.ErrEndpointNotSupported) {
		writeError(w, http.StatusBadRequest, err.Error(), "unsupported_endpoint")
		return http.StatusBadRequest, true
	}
	if errors.Is(err, proxy.ErrNodeAccessDenied) {
		writeError(w, http.StatusForbidden, err.Error(), "model_access_denied")
		return http.StatusForbidden, true
	}
	if errors.Is(err, admission.ErrRateLimitExceeded) {
		writeError(w, http.StatusTooManyRequests, "Backend rate limit exceeded", "rate_limit_exceeded")
		return http.StatusTooManyRequests, true
	}
	if errors.Is(err, admission.ErrQueueTimeout) {
		writeError(w, http.StatusTooManyRequests, "Backend concurrency queue timed out", "concurrency_limit_exceeded")
		return http.StatusTooManyRequests, true
	}
	if errors.Is(err, admission.ErrClientDisconnected) || errors.Is(err, context.Canceled) {
		w.WriteHeader(499)
		return 499, true
	}
	return 0, false
}

func (a *App) effectiveLimits(modelName string, pctx policy.Context) config.LimitsConfig {
	limits := config.LimitsConfig{}
	if a != nil && a.Config != nil {
		if entry, ok := a.Config.FindLegacyModel(modelName); ok {
			limits = entry.Limits
		}
		for _, model := range a.Config.Models {
			if model.EffectiveName() == modelName {
				limits = mergeLimits(limits, model.Limits)
				break
			}
		}
	}
	limits = mergeLimits(limits, pctx.Limits)
	return limits
}

func (a *App) handlePassthrough(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	rest := strings.TrimPrefix(r.URL.Path, "/p1/")
	model, suffix, ok := strings.Cut(rest, "/")
	if !ok {
		writeError(w, http.StatusBadRequest, "passthrough path must include model and path", "invalid_request_error")
		return
	}
	endpointKind := observability.EndpointKind(r.URL.Path)
	requestStatus := 0
	requestOutcome := observability.OutcomeError
	var requestErr error
	var requestSpan observability.Span
	if a.requestMetricsEnabled() {
		if a.otelEnabled() {
			ctx, span := a.Observability.StartRequestSpan(r.Context(), observability.RequestAttrs{
				EndpointKind: endpointKind,
				Model:        a.traceModelName(model),
				KeyScope:     "unknown",
				Stream:       false,
			})
			ctx = contextWithProxyRequestTrace(ctx, span)
			r = r.WithContext(ctx)
			requestSpan = span
		}
		defer func() {
			a.recordRequestMetric(r.Context(), endpointKind, model, false, requestOutcome, requestStatus, requestErr, time.Since(start))
			requestSpan.End(observability.SpanResult{
				Outcome:    requestOutcome,
				StatusCode: requestStatus,
				Error:      requestErr,
			})
		}()
	}
	var pctx policy.Context
	if a != nil && a.Config != nil && a.Config.AppKeys.Enabled {
		policyCtx, policySpan := a.startProxyPhase(r.Context(), observability.PhasePolicy, endpointKind, model, false)
		var err error
		pctx, err = a.Policy.ValidateRequest(r.WithContext(policyCtx), model)
		if err != nil {
			status, _, code := a.policyErrorDetails(err)
			policySpan.End(observability.SpanResult{Outcome: observability.OutcomeError, StatusCode: status, Error: err})
			requestStatus = status
			requestErr = err
			writeError(w, status, err.Error(), code)
			return
		}
		policySpan.End(observability.SpanResult{Outcome: observability.OutcomeSuccess})
		requestSpan.SetKeyScope(observability.KeyScope(pctx.Authenticated, pctx.KeyID, pctx.GroupID))
		r = requestWithRequiredHeaderAttribution(r, pctx.RequiredHeaders)
	}
	backend, ok := a.Router.GetPassthroughBackend(model)
	if !ok {
		requestStatus = http.StatusNotFound
		writeError(w, http.StatusNotFound, "passthrough model not found", "model_not_found")
		return
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		requestStatus = http.StatusBadRequest
		requestErr = err
		writeError(w, http.StatusBadRequest, "failed to read request body", "invalid_request_error")
		return
	}
	limits := a.effectiveLimits(model, pctx)
	var clientSlot *admission.Slot
	if limits.RPS > 0 || limits.RPM > 0 || limits.Concurrency > 0 {
		_, admissionSpan := a.startProxyPhase(r.Context(), observability.PhaseAdmission, endpointKind, model, false)
		if err := a.Admission.CheckRateLimit(pctx.LimitKeyID(), model, admission.RateLimits{RPS: limits.RPS, RPM: limits.RPM}); err != nil {
			admissionSpan.End(observability.SpanResult{Outcome: observability.OutcomeError, StatusCode: http.StatusTooManyRequests, Error: err})
			requestStatus = http.StatusTooManyRequests
			requestErr = err
			writeError(w, http.StatusTooManyRequests, "Rate limit exceeded", "rate_limit_exceeded")
			return
		}
		clientSlot, err = a.acquireSlot(r.Context(), model, pctx, limits)
		if err != nil {
			requestErr = err
			if errors.Is(err, admission.ErrQueueTimeout) {
				admissionSpan.End(observability.SpanResult{Outcome: observability.OutcomeError, StatusCode: http.StatusTooManyRequests, Error: err})
				requestStatus = http.StatusTooManyRequests
				writeError(w, http.StatusTooManyRequests, "Request queued too long - concurrency limit reached", "concurrency_limit_exceeded")
			} else {
				admissionSpan.End(observability.SpanResult{Outcome: observability.OutcomeError, StatusCode: 499, Error: err})
				requestStatus = 499
				w.WriteHeader(499)
			}
			return
		}
		defer clientSlot.Release()
		admissionSpan.End(observability.SpanResult{Outcome: observability.OutcomeSuccess})
	}
	attemptCtx, attemptSpan := a.startProxyAttemptPhase(r.Context(), endpointKind, model, backend.Name, 1, 0, false)
	releaseBackend, err := a.backendAttemptLimiter(attemptCtx, backend)
	if err != nil {
		attemptSpan.End(observability.SpanResult{Outcome: observability.OutcomeBlocked, BackendName: backend.Name, Error: err})
		requestErr = err
		if status, ok := a.writeBackendForwardError(w, err); ok {
			requestStatus = status
		} else {
			requestStatus = http.StatusBadGateway
			writeError(w, http.StatusBadGateway, err.Error(), "backend_error")
		}
		return
	}
	if releaseBackend != nil {
		defer releaseBackend()
	}
	req, err := http.NewRequestWithContext(attemptCtx, r.Method, backend.BuildURL("/"+suffix, r.URL.RawQuery), bytes.NewReader(body))
	if err != nil {
		attemptSpan.End(observability.SpanResult{Outcome: observability.OutcomeError, BackendName: backend.Name, StatusCode: http.StatusBadGateway, Error: err})
		requestStatus = http.StatusBadGateway
		requestErr = err
		writeError(w, http.StatusBadGateway, err.Error(), "backend_error")
		return
	}
	req.Header = proxy.BuildOutboundHeaders(a.outboundHeaders(r), backend.APIKey, false, backend.APIType, backend.AnthropicVersion)
	client := a.Router.Client
	if client == nil {
		client = &http.Client{Timeout: backend.TimeoutOrDefault()}
	}
	releaseAttempt := a.Router.TrackBackendAttempt(backend.Name)
	attemptStarted := time.Now()
	resp, err := client.Do(req)
	if err != nil {
		releaseAttempt()
		a.recordPassthroughBackendAttemptMetric(endpointKind, model, backend.Name, 0, err, time.Since(attemptStarted))
		attemptSpan.End(observability.SpanResult{Outcome: observability.OutcomeError, BackendName: backend.Name, StatusCode: http.StatusBadGateway, Error: err})
		requestStatus = http.StatusBadGateway
		requestErr = err
		writeError(w, http.StatusBadGateway, err.Error(), "backend_error")
		return
	}
	if err := proxy.DecodeResponseBody(resp); err != nil {
		releaseAttempt()
		a.recordPassthroughBackendAttemptMetric(endpointKind, model, backend.Name, 0, err, time.Since(attemptStarted))
		attemptSpan.End(observability.SpanResult{Outcome: observability.OutcomeError, BackendName: backend.Name, StatusCode: http.StatusBadGateway, Error: err})
		requestStatus = http.StatusBadGateway
		requestErr = err
		writeError(w, http.StatusBadGateway, err.Error(), "backend_error")
		return
	}
	a.recordPassthroughBackendAttemptMetric(endpointKind, model, backend.Name, resp.StatusCode, nil, time.Since(attemptStarted))
	attemptSpan.End(observability.SpanResult{Outcome: observability.OutcomeFinal, BackendName: backend.Name, StatusCode: resp.StatusCode})
	defer releaseAttempt()
	defer resp.Body.Close()
	_, responseSpan := a.startProxyPhase(r.Context(), observability.PhaseResponse, endpointKind, model, false)
	copyHeaders(w.Header(), proxy.FilterResponseHeaders(resp.Header))
	w.WriteHeader(resp.StatusCode)
	_, copyErr := io.Copy(w, resp.Body)
	responseOutcome := otelOutcomeForStatus(resp.StatusCode)
	if copyErr != nil {
		responseOutcome = observability.OutcomeError
	}
	responseSpan.End(observability.SpanResult{
		Outcome:     responseOutcome,
		BackendName: backend.Name,
		StatusCode:  resp.StatusCode,
		Error:       copyErr,
	})
	requestStatus = resp.StatusCode
	requestOutcome = responseOutcome
	requestErr = copyErr
}

// outboundHeaders removes the proxy's own credential header after policy has
// consumed it. Authorization is always replaced by proxy.BuildOutboundHeaders;
// a configurable app-key header needs the same treatment to avoid disclosing
// client credentials to an upstream service.
func (a *App) outboundHeaders(r *http.Request) http.Header {
	if r == nil {
		return nil
	}
	if a == nil || a.Config == nil || !a.Config.AppKeys.Enabled {
		return r.Header
	}
	headerName := strings.TrimSpace(a.Config.AppKeys.HeaderName)
	if headerName == "" {
		headerName = config.DefaultAppKeyHeader
	}
	headers := r.Header.Clone()
	headers.Del(headerName)
	return headers
}

func (a *App) storePayload(entry LogEntry, r *http.Request, requestBody []byte, responseHeaders http.Header, responseBody []byte) string {
	return a.storePayloadWithParsedResponse(entry, r, requestBody, responseHeaders, responseBody, nil)
}

func (a *App) storePayloadWithParsedResponse(entry LogEntry, r *http.Request, requestBody []byte, responseHeaders http.Header, responseBody []byte, parsedResponse *proxy.ParsedResponseTrace) string {
	return a.storePayloadWithParsedArtifacts(entry, r, requestBody, responseHeaders, responseBody, parsedResponse, nil)
}

func (a *App) storePayloadWithParsedArtifacts(entry LogEntry, r *http.Request, requestBody []byte, responseHeaders http.Header, responseBody []byte, parsedResponse *proxy.ParsedResponseTrace, parsedStreamSidecars *logstore.ParsedStreamSidecars) string {
	if a == nil || a.Payloads == nil {
		return ""
	}
	isStream := parsedStreamSidecars != nil
	_, loggingSpan := a.startProxyPhase(r.Context(), observability.PhaseLogging, observability.EndpointKind(entry.Path), entry.Model, isStream)
	var parsedBodies *logstore.ParsedResponseBodies
	if parsedResponse != nil && len(parsedResponse.RawUpstreamBody) > 0 && len(parsedResponse.TransformedClientBody) > 0 {
		parsedBodies = &logstore.ParsedResponseBodies{
			RawUpstreamBody:       parsedResponse.RawUpstreamBody,
			TransformedClientBody: parsedResponse.TransformedClientBody,
			ContentType:           parsedResponse.ContentType,
		}
	}
	var parsedStream *logstore.ParsedStream
	if parsedStreamSidecars != nil {
		parsedStream = parsedStreamSidecars.Metadata()
	}
	path, err := a.Payloads.StorePayloadWithParsedAndStream(
		entry.ID,
		entry.Method,
		entry.Path,
		entry.Model,
		entry.Status,
		r.Header,
		requestBody,
		responseHeaders,
		responseBody,
		entry.Error,
		requiredHeaderAttribution(r),
		parsedBodies,
		parsedStream,
	)
	if err != nil || path == "" {
		loggingSpan.End(observability.SpanResult{Outcome: observability.OutcomeError, StatusCode: entry.Status, Error: err})
		return ""
	}
	a.Logs.SetPayloadPath(entry.ID, path)
	loggingSpan.End(observability.SpanResult{Outcome: observability.OutcomeSuccess, StatusCode: entry.Status})
	return path
}

func (a *App) captureParsedResponseSidecars() bool {
	if a == nil || a.Config == nil || a.Payloads == nil || !a.Payloads.Enabled {
		return false
	}
	logging := a.Config.ProxySettings.Logging
	return config.ParseBoolStrict(logging.FullRequestStore["enabled"]) &&
		config.ParseBoolStrict(logging.LogToDisk) &&
		config.ParseBoolStrict(logging.LogParsedResponse)
}

func (a *App) captureParsedStreamSidecars() bool {
	if a == nil || a.Config == nil || a.Payloads == nil || !a.Payloads.Enabled {
		return false
	}
	logging := a.Config.ProxySettings.Logging
	return config.ParseBoolStrict(logging.FullRequestStore["enabled"]) &&
		config.ParseBoolStrict(logging.LogToDisk) &&
		config.ParseBoolStrict(logging.LogParsedStream)
}

func (a *App) persistLog(ctx context.Context, entry LogEntry, r *http.Request, isStream bool, pctx policy.Context, payloadPath string, responseBody []byte) {
	a.persistLogWithUsage(ctx, entry, r, isStream, pctx, payloadPath, responseBody, nil)
}

func (a *App) persistLogWithUsage(ctx context.Context, entry LogEntry, r *http.Request, isStream bool, pctx policy.Context, payloadPath string, responseBody []byte, usageOverride map[string]any) {
	if a == nil || a.LogDB == nil || !a.LogDB.Enabled() || strings.TrimSpace(entry.Model) == "" {
		return
	}
	_, loggingSpan := a.startProxyPhase(ctx, observability.PhaseLogging, observability.EndpointKind(entry.Path), entry.Model, isStream)
	outcome := "success"
	if entry.Status >= 400 || entry.Error != "" {
		outcome = "error"
	}
	usage, stopReason, isToolCall := logdb.ExtractUsageAndOutcome(responseBody)
	if len(usageOverride) > 0 {
		usage = usageOverride
	}
	var expiresAt *time.Time
	if payloadPath != "" && a.Payloads != nil && a.Payloads.Retention > 0 {
		expires := entry.Time.Add(a.Payloads.Retention)
		expiresAt = &expires
	}
	requestID, err := a.LogDB.Insert(ctx, logdb.Record{
		RequestTime:          entry.Time,
		ModelName:            entry.Model,
		IsStream:             isStream,
		Path:                 entry.Path,
		Method:               entry.Method,
		Query:                r.URL.RawQuery,
		Outcome:              outcome,
		DurationMS:           entry.DurationMS,
		StopReason:           stopReason,
		IsToolCall:           isToolCall,
		AppKeyID:             pctx.KeyID,
		BackendName:          entry.Backend,
		BackendStatus:        entry.Status,
		TraceID:              entry.TraceID,
		SpanID:               entry.SpanID,
		UsageStats:           usage,
		FullRequestPath:      payloadPath,
		FullRequestExpiresAt: expiresAt,
		AttemptTrail:         logdbAttempts(entry.AttemptTrail),
	})
	if err != nil || requestID == "" {
		loggingSpan.End(observability.SpanResult{Outcome: observability.OutcomeError, StatusCode: entry.Status, Error: err})
		return
	}
	a.persistWarnings(ctx, entry, r, requestID)
	if outcome != "error" {
		loggingSpan.End(observability.SpanResult{Outcome: observability.OutcomeSuccess, StatusCode: entry.Status})
		return
	}
	errorMessage := entry.Error
	if errorMessage == "" {
		errorMessage = fmt.Sprintf("HTTP status %d", entry.Status)
	}
	_, _ = a.LogDB.InsertError(ctx, logdb.ErrorRecord{
		Timestamp:    entry.Time,
		ModelName:    entry.Model,
		ErrorType:    errorTypeForEntry(entry),
		ErrorMessage: errorMessage,
		BackendName:  entry.Backend,
		HTTPStatus:   entry.Status,
		RequestPath:  requestPath(entry.Path, r.URL.RawQuery),
		RequestLogID: requestID,
	})
	loggingSpan.End(observability.SpanResult{Outcome: observability.OutcomeError, StatusCode: entry.Status})
}

func (a *App) persistWarnings(ctx context.Context, entry LogEntry, r *http.Request, requestID string) {
	if a == nil || a.LogDB == nil || !a.LogDB.Enabled() || requestID == "" {
		return
	}
	for _, warning := range entry.Warnings {
		_, _ = a.LogDB.InsertError(ctx, logdb.ErrorRecord{
			Timestamp:    entry.Time,
			ModelName:    entry.Model,
			ErrorType:    "client_request_warning",
			ErrorMessage: warning.Message,
			BackendName:  entry.Backend,
			HTTPStatus:   entry.Status,
			RequestPath:  requestPath(entry.Path, r.URL.RawQuery),
			RequestLogID: requestID,
			ExtraContext: map[string]any{
				"severity": warning.Severity,
				"code":     warning.Code,
				"path":     warning.Path,
				"details":  warning.Details,
			},
		})
	}
}

func seedQuotaFromLogDB(ctx context.Context, manager *quota.Manager, store *logdb.Store, cfg *config.Config) {
	seedQuotaFromLogDBAt(ctx, manager, store, cfg, time.Now())
}

func seedBackendQuotaFromLogDB(ctx context.Context, manager *quota.Manager, store *logdb.Store, cfg *config.Config) {
	seedBackendQuotaFromLogDBAt(ctx, manager, store, cfg, time.Now())
}

type quotaLogScope struct {
	appKeyID  string
	modelName string
}

type quotaSeedScope struct {
	keyID     string
	modelName string
}

type quotaSeedUsage struct {
	tokens int
	spec   quota.WindowSpec
}

func seedBackendQuotaFromLogDBAt(ctx context.Context, manager *quota.Manager, store *logdb.Store, cfg *config.Config, now time.Time) {
	if manager == nil || store == nil || !store.Enabled() || !backendQuotaPersistenceEnabled(cfg) {
		return
	}
	limitsByBackend := configuredBackendQuotaLimits(cfg)
	if len(limitsByBackend) == 0 {
		return
	}
	usage, err := store.TokenUsageByBackend(ctx)
	if err != nil {
		return
	}
	usageSince := map[time.Time]map[string]int{}
	for _, item := range usage {
		backendName := strings.TrimSpace(item.BackendName)
		limits, ok := limitsByBackend[backendName]
		if !ok || limits.TokenQuota <= 0 {
			continue
		}
		spec := quotaWindowSpec(limits)
		tokens := item.TotalTokens
		if since := quotaWindowStartAt(spec, now); !since.IsZero() {
			totals, ok := usageSince[since]
			if !ok {
				windowUsage, err := store.TokenUsageByBackendSince(ctx, since)
				if err != nil {
					return
				}
				totals = quotaUsageByBackend(windowUsage)
				usageSince[since] = totals
			}
			tokens = totals[backendName]
			if tokens <= 0 {
				continue
			}
		}
		if quotaWindowActive(spec) {
			manager.SeedWithWindowSpecAt(quota.BackendScopeKey, backendName, tokens, spec, now)
			continue
		}
		manager.Seed(quota.BackendScopeKey, backendName, tokens)
	}
}

func seedQuotaFromLogDBAt(ctx context.Context, manager *quota.Manager, store *logdb.Store, cfg *config.Config, now time.Time) {
	if manager == nil || store == nil || !store.Enabled() {
		return
	}
	usage, err := store.TokenUsageByQuotaScope(ctx)
	if err != nil {
		return
	}
	usageSince := map[time.Time]map[quotaLogScope]int{}
	seedUsage := map[quotaSeedScope]quotaSeedUsage{}
	for _, item := range usage {
		tokens := item.TotalTokens
		limits := effectiveLimitsForStoredQuotaUsage(cfg, item.AppKeyID, item.ModelName)
		spec := quotaWindowSpec(limits)
		if since := quotaWindowStartAt(spec, now); !since.IsZero() {
			totals, ok := usageSince[since]
			if !ok {
				windowUsage, err := store.TokenUsageByQuotaScopeSince(ctx, since)
				if err != nil {
					return
				}
				totals = quotaUsageByLogScope(windowUsage)
				usageSince[since] = totals
			}
			tokens = totals[quotaLogScope{appKeyID: item.AppKeyID, modelName: item.ModelName}]
			if tokens <= 0 {
				continue
			}
		}
		keyID := item.AppKeyID
		if cfg != nil {
			keyID = policy.LimitKeyIDForStoredKey(cfg.AppKeys, item.AppKeyID)
		}
		scope := quotaSeedScope{keyID: keyID, modelName: item.ModelName}
		current := seedUsage[scope]
		current.tokens += tokens
		if quotaWindowActive(spec) {
			current.spec = spec
		}
		seedUsage[scope] = current
	}
	for scope, item := range seedUsage {
		if quotaWindowActive(item.spec) {
			manager.SeedWithWindowSpecAt(scope.keyID, scope.modelName, item.tokens, item.spec, now)
			continue
		}
		manager.Seed(scope.keyID, scope.modelName, item.tokens)
	}
}

func quotaUsageByLogScope(usage []logdb.TokenUsageByScope) map[quotaLogScope]int {
	out := make(map[quotaLogScope]int, len(usage))
	for _, item := range usage {
		out[quotaLogScope{appKeyID: item.AppKeyID, modelName: item.ModelName}] = item.TotalTokens
	}
	return out
}

func quotaUsageByBackend(usage []logdb.TokenUsageByBackend) map[string]int {
	out := make(map[string]int, len(usage))
	for _, item := range usage {
		out[strings.TrimSpace(item.BackendName)] = item.TotalTokens
	}
	return out
}

func quotaWindowStartAt(spec quota.WindowSpec, now time.Time) time.Time {
	resetMode := quota.NormalizeResetMode(string(spec.ResetMode))
	if isCalendarResetMode(resetMode) {
		return quota.WindowStartAt(resetMode, now)
	}
	if spec.Window > 0 {
		return now.UTC().Add(-spec.Window)
	}
	return time.Time{}
}

func quotaWindowActive(spec quota.WindowSpec) bool {
	return spec.Window > 0 || isCalendarResetMode(quota.NormalizeResetMode(string(spec.ResetMode)))
}

func backendQuotaPersistenceEnabled(cfg *config.Config) bool {
	return cfg != nil &&
		config.ParseBoolStrict(cfg.RouterSettings.QuotaAwareSelection.Enabled) &&
		config.ParseBoolStrict(cfg.RouterSettings.QuotaAwareSelection.PersistBackendQuota)
}

func configuredBackendQuotaLimits(cfg *config.Config) map[string]config.LimitsConfig {
	out := map[string]config.LimitsConfig{}
	if cfg == nil {
		return out
	}
	if expanded, err := cfg.ExpandedRouting(); err == nil {
		cfg = expanded
	}
	providers := map[string]config.ProviderEntry{}
	for _, provider := range cfg.Providers {
		name := strings.TrimSpace(provider.Name)
		if name != "" {
			providers[name] = provider
		}
	}
	for _, node := range cfg.Nodes {
		name := strings.TrimSpace(node.Name)
		if name == "" {
			continue
		}
		provider := providers[strings.TrimSpace(node.Provider)]
		apiBase := firstNonEmptyString(node.APIBase, provider.APIBase)
		if strings.TrimSpace(apiBase) == "" {
			continue
		}
		apiType := strings.ToLower(strings.TrimSpace(firstNonEmptyString(node.APIType, provider.APIType, "openai")))
		if apiType == "passthrough" {
			continue
		}
		limits := mergeLimits(provider.Limits, node.Limits)
		if limits.TokenQuota > 0 {
			out[name] = limits
		}
	}
	return out
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func isCalendarResetMode(mode quota.ResetMode) bool {
	switch mode {
	case quota.ResetModeDaily, quota.ResetModeWeekly, quota.ResetModeMonthly, quota.ResetModeYearly:
		return true
	default:
		return false
	}
}

func effectiveLimitsForStoredQuotaUsage(cfg *config.Config, appKeyID string, modelName string) config.LimitsConfig {
	limits := config.LimitsConfig{}
	if cfg == nil {
		return limits
	}
	if entry, ok := cfg.FindLegacyModel(modelName); ok {
		limits = entry.Limits
	}
	for _, model := range cfg.Models {
		if model.EffectiveName() == modelName {
			limits = mergeLimits(limits, model.Limits)
			break
		}
	}
	if cfg.AppKeys.Enabled {
		limits = mergeLimits(limits, storedAppKeyLimits(cfg.AppKeys, appKeyID))
	}
	return limits
}

func storedAppKeyLimits(appKeys config.AppKeysConfig, appKeyID string) config.LimitsConfig {
	if strings.TrimSpace(appKeyID) == "" {
		return appKeys.Unauthenticated
	}
	limits := appKeys.Defaults
	for _, key := range appKeys.Keys {
		if key.EffectiveKeyID() != appKeyID {
			continue
		}
		if group, ok := storedAppKeyGroup(appKeys.Groups, key.EffectiveGroupID()); ok {
			limits = mergeLimits(limits, group.Limits)
		}
		limits = mergeLimits(limits, key.Limits)
		limits = mergeLimits(limits, config.LimitsConfig{
			Concurrency:         key.ConcurrencyLimit,
			Priority:            key.Priority,
			QueueTimeoutSeconds: key.QueueTimeout,
		})
		return limits
	}
	return limits
}

func storedAppKeyGroup(groups []config.AppKeyGroupEntry, groupID string) (config.AppKeyGroupEntry, bool) {
	groupID = strings.TrimSpace(groupID)
	if groupID == "" {
		return config.AppKeyGroupEntry{}, false
	}
	for _, group := range groups {
		if strings.TrimSpace(group.GroupID) == groupID {
			return group, true
		}
	}
	return config.AppKeyGroupEntry{}, false
}

func (a *App) logDetail(ctx context.Context, record logdb.Record, bodyMaxChars int) map[string]any {
	detail := recordToMap(record)
	if a != nil && a.LogDB != nil {
		if errors, err := a.LogDB.ErrorsForRequest(ctx, record.ID); err == nil {
			detail["error_logs"] = errors
			detail["warnings"] = warningRecords(errors)
			detail["warning_count"] = len(warningRecords(errors))
		}
	}
	payloadStatus, payload := resolvePayload(record)
	detail["full_request_status"] = payloadStatus
	if payloadStatus != "available" {
		if _, ok := detail["error_logs"]; !ok {
			detail["error_logs"] = []any{}
		}
		return detail
	}
	detail["request_headers"] = payload.RequestHeaders
	detail["response_headers"] = payload.ResponseHeaders
	if len(payload.RequiredHeaders) > 0 {
		detail["required_headers"] = payload.RequiredHeaders
	}
	if requestBody, err := logstore.DecodeBody(payload.RequestBodyBase64); err == nil && len(requestBody) > 0 {
		detail["body"] = truncateValue(decodeJSONOrString(requestBody), bodyMaxChars, detail, "body")
		detail["request_body"] = detail["body"]
	}
	if responseBody, err := logstore.DecodeBody(payload.ResponseBodyBase64); err == nil && len(responseBody) > 0 {
		responseText := string(responseBody)
		if len(responseText) > 100000 {
			detail["full_response"] = responseText[:100000]
			detail["full_response_truncated"] = true
		} else {
			detail["full_response"] = responseText
		}
		detail["response_body"] = decodeJSONOrString(responseBody)
	}
	addParsedResponseDetail(detail, payload.ParsedResponse)
	addParsedStreamDetail(detail, payload.ParsedStream, bodyMaxChars)
	if payload.Error != "" {
		detail["errors"] = []map[string]any{{"message": payload.Error}}
	}
	if _, ok := detail["error_logs"]; !ok {
		detail["error_logs"] = []any{}
	}
	return detail
}

func logIDFromPath(path string) string {
	path = strings.TrimSpace(path)
	for _, prefix := range []string{"/admin/logs/", "/api/logs/"} {
		if strings.HasPrefix(path, prefix) {
			return strings.TrimSpace(strings.TrimPrefix(path, prefix))
		}
	}
	return ""
}

func traceIDFromPath(path string) string {
	path = strings.TrimSpace(path)
	const prefix = "/admin/traces/"
	if strings.HasPrefix(path, prefix) {
		return strings.TrimSpace(strings.TrimPrefix(path, prefix))
	}
	return ""
}

func (a *App) enrichLogRecords(ctx context.Context, records []logdb.Record) []map[string]any {
	out := make([]map[string]any, 0, len(records))
	for _, record := range records {
		item := recordToMap(record)
		if a != nil && a.LogDB != nil && record.ID != "" {
			if errors, err := a.LogDB.ErrorsForRequest(ctx, record.ID); err == nil {
				item["warning_count"] = len(warningRecords(errors))
			}
		}
		out = append(out, item)
	}
	return out
}

func (a *App) traceDetail(ctx context.Context, traceID string, result logdb.QueryResult) map[string]any {
	requests := make([]map[string]any, 0, len(result.Records))
	attempts := make([]map[string]any, 0)
	errorRecords := make([]map[string]any, 0)
	models := map[string]bool{}
	backends := map[string]bool{}
	outcomes := map[string]bool{}
	var startedAt time.Time
	var endedAt time.Time
	var durationMS int64
	for _, record := range result.Records {
		errorsForRequest := []logdb.ErrorRecord(nil)
		if a != nil && a.LogDB != nil {
			if records, err := a.LogDB.ErrorsForRequest(ctx, record.ID); err == nil {
				errorsForRequest = records
			}
		}
		requests = append(requests, traceRecordToMap(record, len(warningRecords(errorsForRequest)), len(errorsForRequest)))
		for _, attempt := range record.AttemptTrail {
			attempts = append(attempts, traceAttemptToMap(record.ID, traceID, attempt))
		}
		for _, errorRecord := range errorsForRequest {
			errorRecords = append(errorRecords, traceErrorToMap(errorRecord))
		}
		if record.ModelName != "" {
			models[record.ModelName] = true
		}
		if record.BackendName != "" {
			backends[record.BackendName] = true
		}
		if record.Outcome != "" {
			outcomes[record.Outcome] = true
		}
		if startedAt.IsZero() || record.RequestTime.Before(startedAt) {
			startedAt = record.RequestTime
		}
		if recordEnd := record.RequestTime.Add(time.Duration(record.DurationMS) * time.Millisecond); endedAt.IsZero() || recordEnd.After(endedAt) {
			endedAt = recordEnd
		}
		durationMS += record.DurationMS
	}
	payload := map[string]any{
		"trace_id":      traceID,
		"data":          requests,
		"logs":          requests,
		"requests":      requests,
		"attempts":      attempts,
		"attempt_trail": attempts,
		"errors":        errorRecords,
		"error_logs":    errorRecords,
		"total":         result.Total,
		"request_count": result.Total,
		"returned":      len(requests),
		"limit":         result.Limit,
		"offset":        result.Offset,
		"has_more":      result.HasMore,
		"models":        sortedTraceKeys(models),
		"backends":      sortedTraceKeys(backends),
		"outcomes":      sortedTraceKeys(outcomes),
		"duration_ms":   durationMS,
	}
	if !startedAt.IsZero() {
		payload["started_at"] = startedAt
	}
	if !endedAt.IsZero() {
		payload["ended_at"] = endedAt
	}
	return payload
}

func traceRecordToMap(record logdb.Record, warningCount int, errorCount int) map[string]any {
	out := map[string]any{
		"id":               record.ID,
		"request_time":     record.RequestTime,
		"model_name":       record.ModelName,
		"is_stream":        record.IsStream,
		"path":             record.Path,
		"method":           record.Method,
		"outcome":          record.Outcome,
		"duration_ms":      record.DurationMS,
		"duration_seconds": record.DurationSeconds,
		"is_tool_call":     record.IsToolCall,
		"trace_id":         record.TraceID,
		"span_id":          record.SpanID,
		"warning_count":    warningCount,
		"error_count":      errorCount,
		"attempt_count":    len(record.AttemptTrail),
		"key_scope":        storedKeyScope(record.AppKeyID),
	}
	if record.StopReason != "" {
		out["stop_reason"] = record.StopReason
	}
	if record.BackendName != "" {
		out["backend_name"] = record.BackendName
	}
	if record.BackendStatus > 0 {
		out["backend_status"] = record.BackendStatus
	}
	if len(record.UsageStats) > 0 {
		out["usage_stats"] = record.UsageStats
	}
	if record.PromptTokens > 0 {
		out["prompt_tokens"] = record.PromptTokens
	}
	if record.CompletionTokens > 0 {
		out["completion_tokens"] = record.CompletionTokens
	}
	if record.TotalTokens > 0 {
		out["total_tokens"] = record.TotalTokens
	}
	if record.CachedTokens > 0 {
		out["cached_tokens"] = record.CachedTokens
	}
	if record.ReasoningTokens > 0 {
		out["reasoning_tokens"] = record.ReasoningTokens
	}
	return out
}

func traceAttemptToMap(requestID string, traceID string, attempt logdb.Attempt) map[string]any {
	out := map[string]any{
		"request_id":    requestID,
		"trace_id":      traceID,
		"attempt_index": attempt.AttemptIndex,
		"backend_name":  attempt.BackendName,
		"retry_index":   attempt.RetryIndex,
		"status":        attempt.Status,
		"retryable":     attempt.Retryable,
		"outcome":       attempt.Outcome,
		"duration_ms":   attempt.DurationMS,
	}
	if attempt.Error != "" {
		out["error_type"] = observability.BucketErrorType(errors.New(attempt.Error))
	}
	return out
}

func traceErrorToMap(record logdb.ErrorRecord) map[string]any {
	out := map[string]any{
		"id":             record.ID,
		"timestamp":      record.Timestamp,
		"error_type":     record.ErrorType,
		"request_log_id": record.RequestLogID,
		"created_at":     record.CreatedAt,
	}
	if record.ModelName != "" {
		out["model_name"] = record.ModelName
	}
	if record.BackendName != "" {
		out["backend_name"] = record.BackendName
	}
	if record.HTTPStatus > 0 {
		out["http_status"] = record.HTTPStatus
	}
	return out
}

func storedKeyScope(appKeyID string) string {
	if strings.TrimSpace(appKeyID) == "" {
		return "unauthenticated"
	}
	return "authenticated"
}

func sortedTraceKeys(values map[string]bool) []string {
	out := make([]string, 0, len(values))
	for value := range values {
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

func warningRecords(records []logdb.ErrorRecord) []logdb.ErrorRecord {
	out := make([]logdb.ErrorRecord, 0)
	for _, record := range records {
		if isWarningRecord(record) {
			out = append(out, record)
		}
	}
	return out
}

func isWarningRecord(record logdb.ErrorRecord) bool {
	if strings.Contains(strings.ToLower(record.ErrorType), "warning") {
		return true
	}
	if severity, ok := record.ExtraContext["severity"].(string); ok {
		return strings.EqualFold(severity, "warning")
	}
	return false
}

func (a *App) memoryLogRecords(traceID string) []map[string]any {
	if a == nil || a.Logs == nil {
		return nil
	}
	entries := a.Logs.List()
	out := make([]map[string]any, 0, len(entries))
	for _, entry := range entries {
		if traceID != "" && entry.TraceID != traceID {
			continue
		}
		out = append(out, memoryLogToMap(entry))
	}
	return out
}

func (a *App) memoryLogDetail(entry LogEntry, bodyMaxChars int) map[string]any {
	detail := memoryLogToMap(entry)
	detail["warnings"] = entry.Warnings
	detail["warning_count"] = len(entry.Warnings)
	payloadStatus, payload := resolveMemoryPayload(entry)
	detail["full_request_status"] = payloadStatus
	if payloadStatus != "available" {
		return detail
	}
	detail["request_headers"] = payload.RequestHeaders
	detail["response_headers"] = payload.ResponseHeaders
	if len(payload.RequiredHeaders) > 0 {
		detail["required_headers"] = payload.RequiredHeaders
	}
	if requestBody, err := logstore.DecodeBody(payload.RequestBodyBase64); err == nil && len(requestBody) > 0 {
		detail["body"] = truncateValue(decodeJSONOrString(requestBody), bodyMaxChars, detail, "body")
		detail["request_body"] = detail["body"]
	}
	if responseBody, err := logstore.DecodeBody(payload.ResponseBodyBase64); err == nil && len(responseBody) > 0 {
		detail["full_response"] = string(responseBody)
		detail["response_body"] = decodeJSONOrString(responseBody)
	}
	addParsedResponseDetail(detail, payload.ParsedResponse)
	addParsedStreamDetail(detail, payload.ParsedStream, bodyMaxChars)
	return detail
}

func addParsedResponseDetail(detail map[string]any, parsed *logstore.ParsedResponse) {
	if parsed == nil {
		return
	}
	out := map[string]any{}
	if parsed.ContentType != "" {
		out["content_type"] = parsed.ContentType
	}
	if raw, err := logstore.DecodeBody(parsed.RawUpstreamResponseBodyBase64); err == nil && len(raw) > 0 {
		out["raw_upstream_response_body"] = decodeJSONOrString(raw)
		out["raw_upstream_response_text"] = string(raw)
	}
	if transformed, err := logstore.DecodeBody(parsed.TransformedClientResponseBodyBase64); err == nil && len(transformed) > 0 {
		out["transformed_client_response_body"] = decodeJSONOrString(transformed)
		out["transformed_client_response_text"] = string(transformed)
	}
	if len(out) > 0 {
		detail["parsed_response"] = out
	}
}

func addParsedStreamDetail(detail map[string]any, parsed *logstore.ParsedStream, bodyMaxChars int) {
	if parsed == nil {
		return
	}
	out := map[string]any{
		"truncated": parsed.Truncated,
	}
	if parsed.ContentType != "" {
		out["content_type"] = parsed.ContentType
	}
	if parsed.MaxBytes > 0 {
		out["max_bytes"] = parsed.MaxBytes
	}
	if parsed.RawUpstreamResponsePath != "" {
		out["raw_upstream_response_path"] = parsed.RawUpstreamResponsePath
		out["raw_upstream_response_bytes"] = parsed.RawUpstreamResponseBytes
		out["raw_upstream_response_truncated"] = parsed.RawUpstreamResponseTruncated
		if text, truncated := readBoundedTextFile(parsed.RawUpstreamResponsePath, bodyMaxChars); text != "" || truncated {
			out["raw_upstream_response_text"] = text
			if truncated {
				out["raw_upstream_response_text_truncated"] = true
			}
		}
	}
	if parsed.TransformedClientResponsePath != "" {
		out["transformed_client_response_path"] = parsed.TransformedClientResponsePath
		out["transformed_client_response_bytes"] = parsed.TransformedClientResponseBytes
		out["transformed_client_response_truncated"] = parsed.TransformedClientResponseTruncated
		if text, truncated := readBoundedTextFile(parsed.TransformedClientResponsePath, bodyMaxChars); text != "" || truncated {
			out["transformed_client_response_text"] = text
			if truncated {
				out["transformed_client_response_text_truncated"] = true
			}
		}
	}
	if len(out) > 1 {
		detail["parsed_stream"] = out
	}
}

func readBoundedTextFile(path string, maxChars int) (string, bool) {
	if maxChars <= 0 {
		maxChars = 100000
	}
	file, err := os.Open(path)
	if err != nil {
		return "", false
	}
	defer file.Close()
	limited := io.LimitReader(file, int64(maxChars)+1)
	raw, err := io.ReadAll(limited)
	if err != nil {
		return "", false
	}
	if len(raw) > maxChars {
		return string(raw[:maxChars]), true
	}
	return string(raw), false
}

func memoryLogToMap(entry LogEntry) map[string]any {
	out := map[string]any{
		"id":             entry.ID,
		"time":           entry.Time,
		"request_time":   entry.Time,
		"method":         entry.Method,
		"path":           entry.Path,
		"request_path":   entry.Path,
		"model":          entry.Model,
		"model_name":     entry.Model,
		"status":         entry.Status,
		"backend_status": entry.Status,
		"backend":        entry.Backend,
		"backend_name":   entry.Backend,
		"duration_ms":    entry.DurationMS,
		"outcome":        "success",
		"warning_count":  len(entry.Warnings),
	}
	if entry.Status >= 400 || entry.Error != "" {
		out["outcome"] = "error"
	}
	if entry.Error != "" {
		out["error"] = entry.Error
	}
	if entry.TraceID != "" {
		out["trace_id"] = entry.TraceID
	}
	if entry.SpanID != "" {
		out["span_id"] = entry.SpanID
	}
	if len(entry.Warnings) > 0 {
		out["warnings"] = entry.Warnings
	}
	if len(entry.AttemptTrail) > 0 {
		attempts := cloneAttemptTrail(entry.AttemptTrail)
		out["attempt_trail"] = attempts
		out["attempts"] = attempts
	}
	return out
}

func resolveMemoryPayload(entry LogEntry) (string, logstore.PayloadRecord) {
	if entry.PayloadPath == "" {
		return "missing", logstore.PayloadRecord{}
	}
	payload, err := logstore.ReadPayload(entry.PayloadPath)
	if err != nil {
		return "missing", logstore.PayloadRecord{}
	}
	return "available", payload
}

func withForwardWarnings(entry LogEntry, result proxy.ForwardResult) LogEntry {
	if len(result.AttemptTrail) > 0 {
		entry.AttemptTrail = append(entry.AttemptTrail, result.AttemptTrail...)
	}
	if len(result.Warnings) == 0 {
		return entry
	}
	entry.Warnings = append(entry.Warnings, result.Warnings...)
	for _, warning := range result.Warnings {
		log.Printf("WARNING client request sanitized model=%q backend=%q path=%q status=%d code=%s warning=%q", entry.Model, entry.Backend, entry.Path, entry.Status, warning.Code, warning.Message)
	}
	return entry
}

func resolvePayload(record logdb.Record) (string, logstore.PayloadRecord) {
	if record.FullRequestPath == "" {
		if record.FullRequestExpiresAt != nil && record.FullRequestExpiresAt.Before(time.Now().UTC()) {
			return "expired", logstore.PayloadRecord{}
		}
		return "missing", logstore.PayloadRecord{}
	}
	if record.FullRequestExpiresAt != nil && record.FullRequestExpiresAt.Before(time.Now().UTC()) {
		return "expired", logstore.PayloadRecord{}
	}
	payload, err := logstore.ReadPayload(record.FullRequestPath)
	if err != nil {
		if record.FullRequestExpiresAt != nil && record.FullRequestExpiresAt.Before(time.Now().UTC()) {
			return "expired", logstore.PayloadRecord{}
		}
		return "missing", logstore.PayloadRecord{}
	}
	return "available", payload
}

func recordToMap(record logdb.Record) map[string]any {
	raw, _ := json.Marshal(record)
	out := map[string]any{}
	_ = json.Unmarshal(raw, &out)
	if len(record.AttemptTrail) > 0 {
		out["attempts"] = out["attempt_trail"]
	}
	return out
}

func logdbAttempts(attempts []proxy.UpstreamAttempt) []logdb.Attempt {
	if len(attempts) == 0 {
		return nil
	}
	out := make([]logdb.Attempt, 0, len(attempts))
	for _, attempt := range attempts {
		out = append(out, logdb.Attempt{
			AttemptIndex: attempt.AttemptIndex,
			BackendName:  attempt.BackendName,
			RetryIndex:   attempt.RetryIndex,
			Status:       attempt.Status,
			Retryable:    attempt.Retryable,
			Outcome:      attempt.Outcome,
			DurationMS:   attempt.DurationMS,
			Error:        attempt.Error,
		})
	}
	return out
}

func cloneAttemptTrail(attempts []proxy.UpstreamAttempt) []proxy.UpstreamAttempt {
	if len(attempts) == 0 {
		return nil
	}
	out := make([]proxy.UpstreamAttempt, len(attempts))
	copy(out, attempts)
	return out
}

func decodeJSONOrString(raw []byte) any {
	var out any
	if err := json.Unmarshal(raw, &out); err == nil {
		return out
	}
	return string(raw)
}

func truncateValue(value any, maxChars int, detail map[string]any, field string) any {
	if maxChars <= 0 {
		return value
	}
	raw, err := json.Marshal(value)
	if err != nil {
		raw = []byte(fmt.Sprint(value))
	}
	if len(raw) <= maxChars {
		return value
	}
	detail[field+"_truncated"] = true
	detail[field+"_total_chars"] = len(raw)
	detail[field+"_preview_chars"] = maxChars
	return string(raw[:maxChars])
}

func validateChat(payload map[string]any) error {
	messages, ok := payload["messages"].([]any)
	if !ok || len(messages) == 0 {
		return fmt.Errorf("You must provide a messages array")
	}
	return nil
}

func validateEmbeddings(payload map[string]any) error {
	input, ok := payload["input"]
	if !ok {
		return fmt.Errorf("You must provide an input parameter")
	}
	switch typed := input.(type) {
	case string:
		return nil
	case []any:
		for _, item := range typed {
			switch item.(type) {
			case string, []any:
			default:
				return fmt.Errorf("Input array items must be strings or token arrays")
			}
		}
		return nil
	default:
		return fmt.Errorf("Input must be a string or array of strings/token arrays")
	}
}

func validateRerank(payload map[string]any) error {
	query, ok := payload["query"].(string)
	if !ok || strings.TrimSpace(query) == "" {
		return fmt.Errorf("You must provide a non-empty query parameter")
	}
	docs, ok := payload["documents"].([]any)
	if !ok || len(docs) == 0 {
		return fmt.Errorf("You must provide a non-empty documents array")
	}
	for i, doc := range docs {
		if _, ok := doc.(string); !ok {
			return fmt.Errorf("All documents must be strings, document at index %d is not", i)
		}
	}
	if topN, ok := payload["top_n"]; ok {
		if number, ok := topN.(int64); ok && number <= 0 {
			return fmt.Errorf("top_n must be a positive integer")
		}
		if number, ok := topN.(float64); ok && number <= 0 {
			return fmt.Errorf("top_n must be a positive integer")
		}
	}
	return nil
}

func estimateRequestTokens(payload map[string]any) int {
	if len(payload) == 0 {
		return 0
	}
	return estimateInputTokens(payload) + maxInt(
		intFromAny(payload["max_tokens"]),
		intFromAny(payload["max_completion_tokens"]),
		intFromAny(payload["max_output_tokens"]),
	)
}

func estimateInputTokens(payload map[string]any) int {
	if messages, ok := payload["messages"].([]any); ok {
		total := 0
		for _, raw := range messages {
			message, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			total += estimateValueTokens(message["content"])
		}
		return total
	}
	if input, ok := payload["input"]; ok {
		return estimateValueTokens(input)
	}
	total := 0
	if query, ok := payload["query"]; ok {
		total += estimateValueTokens(query)
	}
	if docs, ok := payload["documents"]; ok {
		total += estimateValueTokens(docs)
	}
	return total
}

func estimateValueTokens(value any) int {
	switch typed := value.(type) {
	case string:
		return estimateStringTokens(typed)
	case []any:
		total := 0
		for _, item := range typed {
			if _, ok := item.(float64); ok {
				total++
				continue
			}
			total += estimateValueTokens(item)
		}
		return total
	case []int:
		return len(typed)
	case map[string]any:
		total := 0
		for _, key := range []string{"text", "content", "input"} {
			if item, ok := typed[key]; ok {
				total += estimateValueTokens(item)
			}
		}
		return total
	default:
		return 0
	}
}

func estimateStringTokens(text string) int {
	text = strings.TrimSpace(text)
	if text == "" {
		return 0
	}
	count := (len([]rune(text)) + 3) / 4
	if count < 1 {
		return 1
	}
	return count
}

func totalTokensFromUsage(usage map[string]any) int {
	if len(usage) == 0 {
		return 0
	}
	if total := intFromAny(usage["total_tokens"]); total > 0 {
		return total
	}
	return intFromAny(usage["prompt_tokens"]) + intFromAny(usage["completion_tokens"]) + intFromAny(usage["input_tokens"]) + intFromAny(usage["output_tokens"])
}

func quotaWindowSpec(limits config.LimitsConfig) quota.WindowSpec {
	resetMode := quota.NormalizeResetMode(limits.TokenQuotaResetMode)
	if resetMode != quota.ResetModeRolling {
		return quota.WindowSpec{ResetMode: resetMode}
	}
	if limits.TokenQuotaWindowSeconds <= 0 {
		return quota.WindowSpec{}
	}
	return quota.WindowSpec{Window: time.Duration(limits.TokenQuotaWindowSeconds * float64(time.Second))}
}

func intFromAny(value any) int {
	switch typed := value.(type) {
	case int:
		return typed
	case int64:
		return int(typed)
	case float64:
		return int(typed)
	case json.Number:
		out, _ := typed.Int64()
		return int(out)
	case string:
		out, err := strconv.Atoi(strings.TrimSpace(typed))
		if err == nil {
			return out
		}
	}
	return 0
}

func maxInt(values ...int) int {
	best := 0
	for _, value := range values {
		if value > best {
			best = value
		}
	}
	return best
}

func writeError(w http.ResponseWriter, status int, message string, code string) {
	writeJSON(w, status, map[string]any{
		"error": map[string]any{
			"message": message,
			"type":    "invalid_request_error",
			"code":    code,
		},
	})
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func copyHeaders(dst, src http.Header) {
	for key, values := range proxy.FilterResponseHeaders(src) {
		for _, value := range values {
			dst.Add(key, value)
		}
	}
}

func copyStreaming(w http.ResponseWriter, r io.Reader, observer *streamTimingObserver) (int64, error) {
	buf := make([]byte, 32*1024)
	var written int64
	flusher, _ := w.(http.Flusher)
	for {
		nr, er := r.Read(buf)
		if nr > 0 {
			nw, ew := w.Write(buf[:nr])
			if flusher != nil {
				flusher.Flush()
			}
			written += int64(nw)
			if ew != nil {
				return written, ew
			}
			if nr != nw {
				return written, io.ErrShortWrite
			}
			if observer != nil {
				observer.ObserveWrite()
			}
		}
		if er != nil {
			if er != io.EOF {
				return written, er
			}
			if observer != nil {
				observer.ObserveEOF()
			}
			return written, nil
		}
	}
}

type streamTimingObserver struct {
	ctx               context.Context
	provider          *observability.Provider
	prometheusTimings *prometheusTimingRecorder
	endpointKind      string
	modelName         string
	start             time.Time
	first             time.Time
	last              time.Time
	intervals         int64
	intervalSum       time.Duration
}

func (s *streamTimingObserver) ObserveWrite() {
	now := time.Now()
	if s.first.IsZero() {
		s.first = now
		s.last = now
		return
	}
	s.intervalSum += now.Sub(s.last)
	s.intervals++
	s.last = now
}

func (s *streamTimingObserver) ObserveEOF() {
	if s.first.IsZero() {
		return
	}
	ttft := s.first.Sub(s.start)
	if s.provider != nil {
		s.provider.RecordStreamTTFT(s.ctx, observability.StreamTimingMetric{
			EndpointKind: s.endpointKind,
			Model:        s.modelName,
			TTFT:         ttft,
		})
	}
	if s.prometheusTimings != nil {
		s.prometheusTimings.RecordStreamTTFT(prometheusStreamTiming{
			EndpointKind: s.endpointKind,
			Model:        s.modelName,
			Duration:     ttft,
		})
	}
	tpot := time.Duration(0)
	if s.intervals > 0 {
		tpot = s.intervalSum / time.Duration(s.intervals)
	}
	if s.provider != nil {
		s.provider.RecordStreamTPOT(s.ctx, observability.StreamTimingMetric{
			EndpointKind: s.endpointKind,
			Model:        s.modelName,
			TPOT:         tpot,
		})
	}
	if s.prometheusTimings != nil {
		s.prometheusTimings.RecordStreamTPOT(prometheusStreamTiming{
			EndpointKind: s.endpointKind,
			Model:        s.modelName,
			Duration:     tpot,
		})
	}
}

type streamUsageReadCloser struct {
	io.ReadCloser
	decoder     proxy.SSEJSONDecoder
	totalTokens int
	usage       map[string]any
}

func (s *streamUsageReadCloser) Read(p []byte) (int, error) {
	n, err := s.ReadCloser.Read(p)
	if n > 0 {
		s.observe(p[:n])
	}
	if err == io.EOF {
		for _, payload := range s.decoder.Flush() {
			s.observePayload(payload)
		}
	}
	return n, err
}

func (s *streamUsageReadCloser) TotalTokens() int {
	if s == nil {
		return 0
	}
	return s.totalTokens
}

func (s *streamUsageReadCloser) UsageStats() map[string]any {
	if s == nil || len(s.usage) == 0 {
		return nil
	}
	out := make(map[string]any, len(s.usage))
	for key, value := range s.usage {
		out[key] = value
	}
	return out
}

func (s *streamUsageReadCloser) observe(chunk []byte) {
	for _, payload := range s.decoder.Feed(chunk) {
		s.observePayload(payload)
	}
}

func (s *streamUsageReadCloser) observePayload(payload map[string]any) {
	usage, _ := payload["usage"].(map[string]any)
	if tokens := totalTokensFromUsage(usage); tokens > 0 {
		s.totalTokens = tokens
		s.usage = usage
	}
}

type releaseOnEOF struct {
	io.ReadCloser
	release func()
	once    sync.Once
}

func (r *releaseOnEOF) Read(p []byte) (int, error) {
	n, err := r.ReadCloser.Read(p)
	if err != nil {
		r.once.Do(r.release)
	}
	return n, err
}

func (r *releaseOnEOF) Close() error {
	r.once.Do(r.release)
	return r.ReadCloser.Close()
}

func (a *App) addLog(r *http.Request, entry LogEntry) LogEntry {
	if r != nil {
		traceID, spanID := proxyRequestTraceFromContext(r.Context())
		if entry.TraceID == "" {
			entry.TraceID = traceID
		}
		if entry.SpanID == "" {
			entry.SpanID = spanID
		}
	}
	entry.TraceID = normalizeOTelTraceID(entry.TraceID)
	entry.SpanID = normalizeOTelSpanID(entry.SpanID)
	if a == nil || a.Logs == nil {
		return entry
	}
	return a.Logs.Add(entry)
}

func (l *MemoryLogs) Add(entry LogEntry) LogEntry {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.nextID++
	entry.ID = l.nextID
	l.entries = append(l.entries, entry)
	return entry
}

func (l *MemoryLogs) SetPayloadPath(id int64, path string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for i := range l.entries {
		if l.entries[i].ID == id {
			l.entries[i].PayloadPath = path
			return
		}
	}
}

func (l *MemoryLogs) Get(id int64) (LogEntry, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, entry := range l.entries {
		if entry.ID == id {
			return entry, true
		}
	}
	return LogEntry{}, false
}

func (l *MemoryLogs) List() []LogEntry {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]LogEntry, len(l.entries))
	copy(out, l.entries)
	return out
}

func stringValue(value any) string {
	if value == nil {
		return ""
	}
	return strings.TrimSpace(fmt.Sprint(value))
}

func stringSlice(value any) []string {
	switch typed := value.(type) {
	case []string:
		return typed
	case []any:
		out := make([]string, 0, len(typed))
		for _, item := range typed {
			if s := stringValue(item); s != "" {
				out = append(out, s)
			}
		}
		return out
	case string:
		if typed != "" {
			return []string{typed}
		}
	}
	return nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func requestPath(path, rawQuery string) string {
	if rawQuery == "" {
		return path
	}
	return path + "?" + rawQuery
}

func errorTypeForEntry(entry LogEntry) string {
	switch {
	case entry.Status >= 500:
		return "backend_error"
	case entry.Status == http.StatusTooManyRequests:
		return "rate_limit_error"
	case entry.Status == http.StatusUnauthorized || entry.Status == http.StatusForbidden:
		return "authentication_error"
	case entry.Status >= 400:
		return "request_error"
	default:
		return "request_error"
	}
}

func intQuery(r *http.Request, key string, fallback int) int {
	if r == nil {
		return fallback
	}
	value := strings.TrimSpace(r.URL.Query().Get(key))
	if value == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return fallback
	}
	return parsed
}

func logTimeRange(r *http.Request) (*time.Time, *time.Time) {
	var startPtr *time.Time
	var endPtr *time.Time
	if start, ok := timeQuery(r, "start_date", "start_time"); ok {
		startPtr = &start
	}
	if end, ok := timeQuery(r, "end_date", "end_time"); ok {
		endPtr = &end
	}
	return startPtr, endPtr
}

func timeRangePayload(start, end time.Time) map[string]any {
	return map[string]any{
		"start": start,
		"end":   end,
	}
}

func logQueryOptions(r *http.Request) logdb.QueryOptions {
	query := r.URL.Query()
	opts := logdb.QueryOptions{
		Limit:      intQuery(r, "limit", 100),
		Offset:     intQuery(r, "offset", 0),
		ModelName:  firstNonEmpty(query.Get("model"), query.Get("model_name")),
		Outcome:    query.Get("outcome"),
		StopReason: query.Get("stop_reason"),
		Search:     query.Get("search"),
	}
	if r.URL.Path == "/admin/logs" {
		if traceID := strings.TrimSpace(query.Get("trace_id")); traceID != "" {
			if normalized := normalizeOTelTraceID(traceID); normalized != "" {
				opts.TraceID = normalized
			} else {
				opts.TraceID = traceID
			}
		}
	}
	if value, ok := boolQuery(r, "is_tool_call"); ok {
		opts.IsToolCall = &value
	}
	if value, ok := timeQuery(r, "start_date", "start_time"); ok {
		opts.StartTime = &value
	}
	if value, ok := timeQuery(r, "end_date", "end_time"); ok {
		opts.EndTime = &value
	}
	return opts
}

func boolQuery(r *http.Request, key string) (bool, bool) {
	if r == nil {
		return false, false
	}
	raw := strings.TrimSpace(r.URL.Query().Get(key))
	if raw == "" {
		return false, false
	}
	switch strings.ToLower(raw) {
	case "1", "true", "yes", "on":
		return true, true
	case "0", "false", "no", "off":
		return false, true
	default:
		return false, false
	}
}

func timeQuery(r *http.Request, keys ...string) (time.Time, bool) {
	if r == nil {
		return time.Time{}, false
	}
	for _, key := range keys {
		raw := strings.TrimSpace(r.URL.Query().Get(key))
		if raw == "" {
			continue
		}
		for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02"} {
			parsed, err := time.Parse(layout, raw)
			if err == nil {
				return parsed.UTC(), true
			}
		}
	}
	return time.Time{}, false
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
