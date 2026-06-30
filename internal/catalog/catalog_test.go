package catalog

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/theio/yallmp-proxy/internal/config"
)

func TestCatalogSeedsConfigWithoutProbeOptIn(t *testing.T) {
	cfg := &config.Config{
		Providers: []config.ProviderEntry{{
			Name:    "provider-a",
			APIBase: "https://provider.test/v1",
			APIKey:  "secret",
		}},
		Nodes: []config.NodeEntry{{
			Name:     "node-a",
			Provider: "provider-a",
		}},
		Models: []config.ClientModelEntry{{
			Name:  "client-model",
			Nodes: []string{"node-a"},
		}},
	}
	c := New(cfg)
	snapshot := c.Snapshot()
	if snapshot.Enabled {
		t.Fatalf("catalog enabled = true, want false")
	}
	if snapshot.ProbeEnabled {
		t.Fatalf("probe enabled = true without opt-in")
	}
	node := onlyNode(t, snapshot, "client-model")
	if node.Status != StatusConfigured || node.Source != SourceConfig {
		t.Fatalf("node = %#v, want configured config source", node)
	}
	if node.ErrorClass != "" {
		t.Fatalf("error class leaked on config entry: %#v", node)
	}
}

func TestCatalogRunOnceProbesOptedInNodeWithProviderAuth(t *testing.T) {
	var gotAuth, gotInternal, gotClientKey string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotInternal = r.Header.Get("x-yallmp-trace")
		gotClientKey = r.Header.Get("x-api-key")
		if r.URL.Path != "/v1/models" {
			t.Fatalf("path = %s, want /v1/models", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]string{{"id": "client-model"}, {"id": "remote/model"}},
		})
	}))
	defer upstream.Close()

	cfg := &config.Config{
		ProxySettings: config.ProxySettings{ModelCatalog: config.ModelCatalogConfig{
			Enabled:         true,
			ProbeNodes:      []string{"node-a"},
			CacheTTLSeconds: 60,
		}},
		Providers: []config.ProviderEntry{{
			Name:    "provider-a",
			APIBase: upstream.URL + "/v1",
			APIKey:  "provider-secret",
		}},
		Nodes: []config.NodeEntry{{
			Name:     "node-a",
			Provider: "provider-a",
		}},
		Models: []config.ClientModelEntry{{
			Name:  "client-model",
			Nodes: []string{"node-a"},
		}},
	}
	c := New(cfg)
	c.RunOnce(context.Background())

	if gotAuth != "Bearer provider-secret" {
		t.Fatalf("Authorization = %q, want provider bearer", gotAuth)
	}
	if gotInternal != "" || gotClientKey != "" {
		t.Fatalf("probe sent disallowed headers: internal=%q client=%q", gotInternal, gotClientKey)
	}
	snapshot := c.Snapshot()
	if !snapshot.ProbeEnabled {
		t.Fatalf("probe_enabled = false, want true")
	}
	if node := onlyNode(t, snapshot, "client-model"); node.Status != StatusAvailable || node.Source != SourceProbe {
		t.Fatalf("client-model node = %#v, want available probe source", node)
	}
	if node := onlyNode(t, snapshot, "remote/model"); node.Status != StatusAvailable || node.Source != SourceProbe {
		t.Fatalf("remote/model node = %#v, want available probe source", node)
	}
}

func TestCatalogBucketsProbeFailuresWithoutRawError(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "Authorization: Bearer secret raw upstream failure", http.StatusUnauthorized)
	}))
	defer upstream.Close()

	cfg := probeConfig(upstream.URL)
	c := New(cfg)
	c.RunOnce(context.Background())
	node := onlyNode(t, c.Snapshot(), "client-model")
	if node.Status != StatusUnavailable || node.ErrorClass != ErrorAuth {
		t.Fatalf("node = %#v, want unavailable auth bucket", node)
	}
	if node.ErrorClass == "Authorization: Bearer secret raw upstream failure" {
		t.Fatalf("raw upstream error leaked: %#v", node)
	}
}

func TestCatalogMarksExpiredProbeEntriesStale(t *testing.T) {
	cfg := probeConfig("https://unused.test/v1")
	cfg.ProxySettings.ModelCatalog.CacheTTLSeconds = 1
	c := New(cfg)
	start := time.Date(2026, 6, 28, 12, 0, 0, 0, time.UTC)
	c.SetNow(func() time.Time { return start })
	c.mergeProbeResult(c.nodes["node-a"], probeState{
		LastSync: start.Add(-2 * time.Second),
		NextSync: start.Add(time.Minute),
		Models:   map[string]bool{"client-model": true},
	})
	node := onlyNode(t, c.Snapshot(), "client-model")
	if node.Status != StatusStale {
		t.Fatalf("status = %s, want stale", node.Status)
	}
}

func TestCatalogSanitizesRemoteModelIDs(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]string{{"id": "safe-model\nAuthorization: Bearer secret"}},
		})
	}))
	defer upstream.Close()
	c := New(probeConfig(upstream.URL))
	c.RunOnce(context.Background())
	snapshot := c.Snapshot()
	for _, model := range snapshot.Models {
		if model.Model == "safe-modelAuthorization:Bearersecret" {
			return
		}
		if model.Model == "safe-model\nAuthorization: Bearer secret" {
			t.Fatalf("unsanitized model leaked: %q", model.Model)
		}
	}
	t.Fatalf("sanitized probed model not found in %#v", snapshot.Models)
}

func TestCatalogMatchesConfiguredModelByTargetModel(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]string{{"id": "upstream-model"}},
		})
	}))
	defer upstream.Close()

	cfg := probeConfig(upstream.URL)
	cfg.Nodes[0].TargetModel = "upstream-model"
	c := New(cfg)
	c.RunOnce(context.Background())

	snapshot := c.Snapshot()
	node := onlyNode(t, snapshot, "client-model")
	if node.Status != StatusAvailable {
		t.Fatalf("client-model status = %s, want available through target model", node.Status)
	}
	if node.TargetModel != "upstream-model" {
		t.Fatalf("target_model = %q, want upstream-model", node.TargetModel)
	}
	for _, warning := range Warnings(snapshot) {
		if warning.Code == "all_nodes_unavailable" && warning.Model == "client-model" {
			t.Fatalf("target-model match generated unavailable warning: %#v", warning)
		}
	}
}

func TestSnapshotWarningsAreDerivedAndBounded(t *testing.T) {
	snapshot := Snapshot{
		Enabled:      true,
		ProbeEnabled: false,
		Models: []ModelEntry{{
			Model: "client-model",
			Providers: []ProviderEntry{{
				Provider: "provider-a",
				Nodes: []NodeEntry{{
					Node:       "node-a",
					Status:     StatusUnavailable,
					Source:     SourceProbe,
					ErrorClass: ErrorAuth,
				}},
			}},
		}},
	}
	warnings := Warnings(snapshot)
	if len(warnings) != 3 {
		t.Fatalf("warnings = %#v, want probe-off, auth, and unavailable warnings", warnings)
	}
	wantCodes := map[string]bool{
		"probe_off_no_data":     true,
		"node_auth_error":       true,
		"all_nodes_unavailable": true,
	}
	for _, warning := range warnings {
		if !wantCodes[warning.Code] {
			t.Fatalf("unexpected warning %#v", warning)
		}
		if warning.Severity != "info" && warning.Severity != "warning" {
			t.Fatalf("unexpected severity %#v", warning)
		}
	}

	large := Snapshot{Enabled: true, ProbeEnabled: true}
	for i := 0; i < maxSnapshotWarnings+20; i++ {
		large.Models = append(large.Models, ModelEntry{
			Model: "model",
			Providers: []ProviderEntry{{
				Provider: "provider",
				Nodes: []NodeEntry{{
					Node:       "node",
					Status:     StatusUnavailable,
					Source:     SourceProbe,
					ErrorClass: ErrorAuth,
				}},
			}},
		})
	}
	if got := len(Warnings(large)); got != maxSnapshotWarnings {
		t.Fatalf("warning count = %d, want cap %d", got, maxSnapshotWarnings)
	}
}

func TestCatalogStartHonorsCancellationAndDefensiveSetters(t *testing.T) {
	c := New(&config.Config{})
	c.SetHTTPClient(nil)
	c.SetNow(nil)

	ctx, cancel := context.WithCancel(context.Background())
	done := c.Start(ctx)
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("catalog Start did not stop after context cancellation")
	}

	var nilCatalog *Catalog
	nilDone := nilCatalog.Start(context.Background())
	select {
	case <-nilDone:
	case <-time.After(time.Second):
		t.Fatal("nil catalog Start did not close")
	}
}

func TestCatalogIntervalFallbacks(t *testing.T) {
	defaultCatalog := New(&config.Config{})
	if got := defaultCatalog.Interval(); got != defaultProbeInterval {
		t.Fatalf("default interval = %s, want %s", got, defaultProbeInterval)
	}

	configured := New(&config.Config{ProxySettings: config.ProxySettings{ModelCatalog: config.ModelCatalogConfig{
		ProbeIntervalSeconds: 120,
	}}})
	if got := configured.Interval(); got != 120*time.Second {
		t.Fatalf("configured interval = %s, want 120s", got)
	}
}

func TestCatalogProviderProbeOptInAndDisabledSkips(t *testing.T) {
	var hits atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]string{{"id": "client-model"}}})
	}))
	defer upstream.Close()

	providerOptIn := probeConfig(upstream.URL)
	providerOptIn.ProxySettings.ModelCatalog.ProbeNodes = nil
	providerOptIn.ProxySettings.ModelCatalog.ProbeProviders = []string{"provider-a"}
	c := New(providerOptIn)
	c.RunOnce(context.Background())
	if hits.Load() != 1 {
		t.Fatalf("provider opt-in hits = %d, want 1", hits.Load())
	}
	if !c.Snapshot().ProbeEnabled {
		t.Fatal("provider opt-in snapshot did not enable probes")
	}
	if node := onlyNode(t, c.Snapshot(), "client-model"); node.Status != StatusAvailable || node.Source != SourceProbe {
		t.Fatalf("provider opt-in node = %#v, want available probe", node)
	}

	noOptIn := probeConfig(upstream.URL)
	noOptIn.ProxySettings.ModelCatalog.ProbeNodes = nil
	noOptIn.ProxySettings.ModelCatalog.ProbeProviders = nil
	c = New(noOptIn)
	c.RunOnce(context.Background())
	if hits.Load() != 1 {
		t.Fatalf("no-opt-in catalog hit upstream; hits=%d", hits.Load())
	}
	if c.Snapshot().ProbeEnabled {
		t.Fatal("no-opt-in snapshot reported probes enabled")
	}

	disabled := probeConfig(upstream.URL)
	disabled.ProxySettings.ModelCatalog.Enabled = false
	c = New(disabled)
	c.RunOnce(context.Background())
	if hits.Load() != 1 {
		t.Fatalf("disabled catalog hit upstream; hits=%d", hits.Load())
	}
	if c.Snapshot().Enabled {
		t.Fatal("disabled snapshot reported enabled")
	}
}

func TestCatalogRunOnceSkipsSourcesNotDue(t *testing.T) {
	var hits atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]string{{"id": "client-model"}}})
	}))
	defer upstream.Close()

	c := New(probeConfig(upstream.URL))
	now := time.Date(2026, 6, 28, 12, 0, 0, 0, time.UTC)
	c.SetNow(func() time.Time { return now })
	c.mergeProbeResult(c.nodes["node-a"], probeState{
		LastSync: now,
		NextSync: now.Add(time.Hour),
		Models:   map[string]bool{"client-model": true},
	})
	c.RunOnce(context.Background())
	if hits.Load() != 0 {
		t.Fatalf("not-due source was probed; hits=%d", hits.Load())
	}
}

func TestCatalogProbeFailureBuckets(t *testing.T) {
	for _, tc := range []struct {
		name     string
		status   int
		body     string
		mutate   func(*config.Config)
		noServer bool
		want     string
	}{
		{name: "rate limited", status: http.StatusTooManyRequests, body: "rate limit", want: ErrorRateLimited},
		{name: "auth", status: http.StatusForbidden, body: "forbidden", want: ErrorAuth},
		{name: "status", status: http.StatusInternalServerError, body: "server error", want: ErrorStatus},
		{name: "invalid json", status: http.StatusOK, body: "{not json", want: ErrorInvalidResponse},
		{name: "unsupported api type", noServer: true, mutate: func(cfg *config.Config) { cfg.Nodes[0].APIType = "anthropic" }, want: ErrorUnsupported},
		{name: "network", noServer: true, mutate: func(cfg *config.Config) {
			server := httptest.NewServer(http.NotFoundHandler())
			url := server.URL
			server.Close()
			cfg.Providers[0].APIBase = url + "/v1"
		}, want: ErrorNetwork},
		{name: "timeout", noServer: true, mutate: func(cfg *config.Config) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				<-r.Context().Done()
			}))
			t.Cleanup(server.Close)
			cfg.Providers[0].APIBase = server.URL + "/v1"
			cfg.ProxySettings.ModelCatalog.ProbeTimeoutSeconds = 0.001
		}, want: ErrorTimeout},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var upstream *httptest.Server
			baseURL := "https://unused.test"
			if !tc.noServer {
				upstream = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.WriteHeader(tc.status)
					_, _ = w.Write([]byte(tc.body))
				}))
				defer upstream.Close()
				baseURL = upstream.URL
			}
			cfg := probeConfig(baseURL)
			if tc.mutate != nil {
				tc.mutate(cfg)
			}
			c := New(cfg)
			c.RunOnce(context.Background())
			node := onlyNode(t, c.Snapshot(), "client-model")
			if node.Status != StatusUnavailable || node.ErrorClass != tc.want {
				t.Fatalf("node = %#v, want unavailable %q", node, tc.want)
			}
		})
	}
}

func TestCatalogPureHelpersAndSnapshotStrings(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want string
	}{
		{want: ErrorNone},
		{err: context.Canceled, want: ErrorTimeout},
		{err: context.DeadlineExceeded, want: ErrorTimeout},
		{err: catalogTimeoutError{}, want: ErrorTimeout},
		{err: errors.New("dial tcp: connection refused"), want: ErrorNetwork},
	} {
		if got := classifyError(tc.err); got != tc.want {
			t.Fatalf("classifyError(%v) = %q, want %q", tc.err, got, tc.want)
		}
	}

	for _, value := range []string{ErrorNone, ErrorAuth, ErrorRateLimited, ErrorStatus, ErrorTimeout, ErrorNetwork, ErrorInvalidResponse, ErrorUnsupported} {
		if got := sanitizeErrorClass(value); got != value {
			t.Fatalf("sanitizeErrorClass(%q) = %q", value, got)
		}
	}
	if got := sanitizeErrorClass("raw upstream failure"); got != ErrorStatus {
		t.Fatalf("unknown error class = %q, want %q", got, ErrorStatus)
	}

	long := strings.Repeat("a", 300)
	if got := sanitizeName(long); len(got) != 256 {
		t.Fatalf("long sanitized name len = %d, want 256", len(got))
	}
	sanitized := sanitizeName(" bad 名\n._:/+- ")
	if strings.ContainsAny(sanitized, " \n\t") || !strings.Contains(sanitized, "名") || !strings.Contains(sanitized, "._:/+-") {
		t.Fatalf("sanitized name = %q", sanitized)
	}
	if got := appendUnique([]string{"a"}, " a "); len(got) != 1 || got[0] != "a" {
		t.Fatalf("appendUnique duplicate = %#v", got)
	}
	if got := appendUnique([]string{"a"}, "b"); len(got) != 2 || got[1] != "b" {
		t.Fatalf("appendUnique new value = %#v", got)
	}
	if got := firstConfiguredModel(nil); got != "" {
		t.Fatalf("firstConfiguredModel(nil) = %q", got)
	}
	if got := firstConfiguredModel([]string{" bad name "}); got != "badname" {
		t.Fatalf("firstConfiguredModel sanitized = %q", got)
	}

	existingProbe := NodeEntry{Source: SourceProbe, Status: StatusAvailable, ConfiguredModel: "old", TargetModel: "old-target"}
	incomingConfig := NodeEntry{Source: SourceConfig, Status: StatusConfigured, ConfiguredModel: "client", TargetModel: "target"}
	merged := mergeNode(existingProbe, incomingConfig)
	if merged.Source != SourceProbe || merged.Status != StatusAvailable || merged.ConfiguredModel != "client" || merged.TargetModel != "target" {
		t.Fatalf("merge config over probe = %#v", merged)
	}
	if got := mergeNode(incomingConfig, existingProbe); got != existingProbe {
		t.Fatalf("probe over config = %#v, want incoming probe", got)
	}

	snapshot := Snapshot{
		Enabled:      true,
		ProbeEnabled: true,
		GeneratedAt:  time.Date(2026, 6, 28, 12, 0, 0, 0, time.UTC),
		Models:       []ModelEntry{{Model: "alpha"}},
	}
	redacted := snapshot.Redacted()
	if redacted["enabled"] != true || redacted["probe_enabled"] != true || redacted["generated_at"] != snapshot.GeneratedAt || len(redacted["models"].([]ModelEntry)) != 1 {
		t.Fatalf("redacted snapshot = %#v", redacted)
	}
	if got := snapshot.String(); got != "models=1 probe_enabled=true" {
		t.Fatalf("snapshot string = %q", got)
	}
}

func TestCatalogMultipleProvidersUnderOneModelAreSorted(t *testing.T) {
	cfg := &config.Config{
		Providers: []config.ProviderEntry{
			{Name: "provider-b", APIBase: "https://provider-b.test/v1"},
			{Name: "provider-a", APIBase: "https://provider-a.test/v1"},
		},
		Nodes: []config.NodeEntry{
			{Name: "node-b", Provider: "provider-b"},
			{Name: "node-a", Provider: "provider-a"},
		},
		Models: []config.ClientModelEntry{{Name: "client-model", Nodes: []string{"node-b", "node-a"}}},
	}
	snapshot := New(cfg).Snapshot()
	var entry ModelEntry
	for _, candidate := range snapshot.Models {
		if candidate.Model == "client-model" {
			entry = candidate
			break
		}
	}
	if len(entry.Providers) != 2 {
		t.Fatalf("providers = %#v, want two", entry.Providers)
	}
	if entry.Providers[0].Provider != "provider-a" || entry.Providers[1].Provider != "provider-b" {
		t.Fatalf("providers not sorted: %#v", entry.Providers)
	}
}

func TestCatalogSourcesFromConfigEdges(t *testing.T) {
	if got := sourcesFromConfig(nil, Settings{}); len(got) != 0 {
		t.Fatalf("nil config sources = %#v, want empty", got)
	}

	settings := Settings{
		ProbeNodes:     map[string]bool{"legacy": true, "fallback-node": true},
		ProbeProviders: map[string]bool{"provider-a": true},
	}
	sources := sourcesFromConfig(&config.Config{
		ModelList: []config.ModelEntry{
			{ModelName: "legacy", ModelParams: map[string]any{"api_base": "https://legacy.test/v1", "model": "legacy-upstream"}},
			{ModelName: "missing-base", ModelParams: map[string]any{}},
			{ModelName: "passthrough", ModelParams: map[string]any{"api_base": "https://pass.test/v1", "api_type": "passthrough"}},
		},
		Providers: []config.ProviderEntry{
			{Name: "provider-a", APIBase: "https://provider-a.test/v1", APIKey: "provider-key", AnthropicVersion: "2023-06-01"},
		},
		Nodes: []config.NodeEntry{
			{Name: ""},
			{Name: "no-base"},
			{Name: "passthrough-node", APIBase: "https://pass-node.test/v1", APIType: "passthrough"},
			{Name: "fallback-node", APIBase: "https://fallback.test/v1", Model: "fallback-model"},
			{Name: "provider-node", Provider: "provider-a", TargetModel: "provider-target"},
		},
	}, settings)

	if _, ok := sources["missing-base"]; ok {
		t.Fatalf("missing-base model list entry should be skipped: %#v", sources)
	}
	if _, ok := sources["passthrough"]; ok {
		t.Fatalf("passthrough model list entry should be skipped: %#v", sources)
	}
	if _, ok := sources["passthrough-node"]; ok {
		t.Fatalf("passthrough node should be skipped: %#v", sources)
	}
	legacy := sources["legacy"]
	if legacy.Name != "legacy" || !legacy.ProbeEnabled || len(legacy.ConfiguredModels) != 1 || legacy.ConfiguredModels[0] != "legacy" {
		t.Fatalf("legacy source = %#v", legacy)
	}
	fallback := sources["fallback-node"]
	if fallback.Name != "fallback-node" || !fallback.ProbeEnabled || len(fallback.ConfiguredModels) != 1 || fallback.ConfiguredModels[0] != "fallback-model" {
		t.Fatalf("fallback source = %#v", fallback)
	}
	provider := sources["provider-node"]
	if provider.Provider != "provider-a" || !provider.ProbeEnabled || provider.Backend.APIKey != "provider-key" || provider.Backend.TargetModel != "provider-target" || provider.Backend.AnthropicVersion != "2023-06-01" {
		t.Fatalf("provider source = %#v", provider)
	}
}

type catalogTimeoutError struct{}

func (catalogTimeoutError) Error() string   { return "timeout" }
func (catalogTimeoutError) Timeout() bool   { return true }
func (catalogTimeoutError) Temporary() bool { return false }

func probeConfig(baseURL string) *config.Config {
	return &config.Config{
		ProxySettings: config.ProxySettings{ModelCatalog: config.ModelCatalogConfig{
			Enabled:         true,
			ProbeNodes:      []string{"node-a"},
			CacheTTLSeconds: 60,
		}},
		Providers: []config.ProviderEntry{{
			Name:    "provider-a",
			APIBase: baseURL + "/v1",
			APIKey:  "provider-secret",
		}},
		Nodes: []config.NodeEntry{{
			Name:     "node-a",
			Provider: "provider-a",
		}},
		Models: []config.ClientModelEntry{{
			Name:  "client-model",
			Nodes: []string{"node-a"},
		}},
	}
}

func onlyNode(t *testing.T, snapshot Snapshot, model string) NodeEntry {
	t.Helper()
	for _, entry := range snapshot.Models {
		if entry.Model != model {
			continue
		}
		if len(entry.Providers) != 1 || len(entry.Providers[0].Nodes) != 1 {
			t.Fatalf("model %s providers = %#v, want one node", model, entry.Providers)
		}
		return entry.Providers[0].Nodes[0]
	}
	t.Fatalf("model %s not found in %#v", model, snapshot.Models)
	return NodeEntry{}
}
