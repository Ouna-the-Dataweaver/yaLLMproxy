package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestLoadSubstitutesEnvFileBeforeOSEnv(t *testing.T) {
	t.Setenv("TEST_API_KEY", "from-os")
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	envPath := filepath.Join(dir, ".env")
	if err := os.WriteFile(envPath, []byte("TEST_API_KEY=from-env\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfgPath, []byte(`
model_list:
  - model_name: test
    model_params:
      api_base: http://example.test/v1
      api_key: ${TEST_API_KEY}
`), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadWithOptions(LoadOptions{Path: cfgPath, SubstituteEnv: true})
	if err != nil {
		t.Fatal(err)
	}
	got := cfg.ModelList[0].ModelParams["api_key"]
	if got != "from-env" {
		t.Fatalf("api_key = %v, want env-file override", got)
	}
	rawModel := cfg.Raw["model_list"].([]any)[0].(map[string]any)
	rawParams := rawModel["model_params"].(map[string]any)
	if got := rawParams["api_key"]; got != "${TEST_API_KEY}" {
		t.Fatalf("raw api_key = %v, want placeholder", got)
	}
}

func TestLoadPreservesMissingEnvPlaceholders(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(cfgPath, []byte(`
model_list:
  - model_name: test
    model_params:
      api_base: http://example.test/v1
      api_key: ${MISSING_KEY}
`), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadWithOptions(LoadOptions{Path: cfgPath, SubstituteEnv: true})
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.ModelList[0].ModelParams["api_key"]; got != "${MISSING_KEY}" {
		t.Fatalf("api_key = %v, want literal placeholder", got)
	}
}

func TestResolveModelInheritanceDeepMergesAndReplacesLists(t *testing.T) {
	models := []ModelEntry{
		{
			ModelName: "base",
			ModelParams: map[string]any{
				"api_base": "http://base.test/v1",
				"api_key":  "base-key",
				"parameters": map[string]any{
					"temperature": map[string]any{"default": 1.0, "allow_override": false},
					"top_p":       map[string]any{"default": 0.95, "allow_override": false},
				},
			},
			Modules: map[string]any{
				"upstream": map[string]any{
					"enabled":  true,
					"response": []any{"parse_tags"},
					"parse_tags": map[string]any{
						"parse_thinking": true,
					},
				},
			},
		},
		{
			ModelName: "derived",
			Extends:   "base",
			ModelParams: map[string]any{
				"api_key": "derived-key",
				"parameters": map[string]any{
					"temperature": map[string]any{"default": 0.7},
				},
			},
			Modules: map[string]any{
				"upstream": map[string]any{
					"response": []any{"swap_reasoning_content"},
				},
			},
			Protected: false,
		},
	}
	resolved, err := ResolveAllModelInheritance(models)
	if err != nil {
		t.Fatal(err)
	}
	derived := resolved[1]
	if derived.ModelName != "derived" {
		t.Fatalf("model_name = %s", derived.ModelName)
	}
	if got := derived.ModelParams["api_base"]; got != "http://base.test/v1" {
		t.Fatalf("api_base = %v", got)
	}
	if got := derived.ModelParams["api_key"]; got != "derived-key" {
		t.Fatalf("api_key = %v", got)
	}
	params := derived.ModelParams["parameters"].(map[string]any)
	temp := params["temperature"].(map[string]any)
	if temp["default"] != 0.7 || temp["allow_override"] != false {
		t.Fatalf("temperature params = %#v", temp)
	}
	upstream := derived.Modules["upstream"].(map[string]any)
	if got := upstream["response"].([]any)[0]; got != "swap_reasoning_content" {
		t.Fatalf("response list was not replaced: %#v", upstream["response"])
	}
	if ParseBool(derived.Protected) {
		t.Fatalf("derived protected = %v, want false", derived.Protected)
	}
}

func TestResolveModelInheritanceRejectsCycles(t *testing.T) {
	_, err := ResolveAllModelInheritance([]ModelEntry{
		{ModelName: "a", Extends: "b"},
		{ModelName: "b", Extends: "a"},
	})
	if err == nil {
		t.Fatal("expected circular inheritance error")
	}
}

func TestValidateReportsFieldLevelIssues(t *testing.T) {
	cfg := &Config{
		ModelList: []ModelEntry{
			{
				ModelName: "alpha",
				ModelParams: map[string]any{
					"api_base": "not-a-url",
				},
				Limits: LimitsConfig{RPM: -1},
			},
			{
				ModelName: "alpha",
				ModelParams: map[string]any{
					"api_base": "http://alpha.test/v1",
				},
			},
		},
		Nodes: []NodeEntry{{
			Name:     "node-a",
			Provider: "missing",
		}},
		Models: []ClientModelEntry{{
			Name:  "client-a",
			Nodes: []string{"missing-node"},
		}},
		AppKeys: AppKeysConfig{
			Enabled: true,
			Keys: []AppKeyEntry{{
				GroupID: "missing-group",
			}},
		},
	}
	issues := cfg.Validate()
	got := map[string]string{}
	for _, issue := range issues {
		got[issue.Path] = issue.Code
	}
	wants := map[string]string{
		"model_list[0].model_params.api_base": "invalid_url",
		"model_list[0].limits.rpm":            "invalid_limit",
		"model_list[1].model_name":            "duplicate_model",
		"nodes[0].provider":                   "unknown_provider",
		"nodes[0].api_base":                   "required",
		"models[0].nodes[0]":                  "unknown_node",
		"app_keys.keys[0].secret":             "required",
		"app_keys.keys[0].group_id":           "unknown_group",
	}
	for path, code := range wants {
		if got[path] != code {
			t.Fatalf("issue %s = %q, want %q; all issues=%#v", path, got[path], code, issues)
		}
	}
}

func TestValidateInferenceCacheReportsInvalidModeAndNegativeLimits(t *testing.T) {
	cfg := &Config{
		ModelList: []ModelEntry{{
			ModelName: "alpha",
			ModelParams: map[string]any{
				"api_base": "https://alpha.test/v1",
			},
		}},
		Providers: []ProviderEntry{{
			Name:    "provider-a",
			APIBase: "https://provider.test/v1",
		}},
		ProxySettings: ProxySettings{
			InferenceCache: InferenceCacheConfig{
				Mode:          "sometimes",
				TTLSeconds:    -1,
				MaxAgeSeconds: -2,
				MaxEntries:    -3,
				MaxEntryBytes: -4,
			},
		},
	}
	issues := cfg.Validate()
	got := map[string]string{}
	for _, issue := range issues {
		got[issue.Path] = issue.Code
	}
	wants := map[string]string{
		"proxy_settings.inference_cache.mode":            "invalid_cache_mode",
		"proxy_settings.inference_cache.ttl_seconds":     "invalid_limit",
		"proxy_settings.inference_cache.max_age_seconds": "invalid_limit",
		"proxy_settings.inference_cache.max_entries":     "invalid_limit",
		"proxy_settings.inference_cache.max_entry_bytes": "invalid_limit",
	}
	for path, code := range wants {
		if got[path] != code {
			t.Fatalf("issue %s = %q, want %q; all issues=%#v", path, got[path], code, issues)
		}
	}
}

func TestValidateInferenceCachePersistenceRules(t *testing.T) {
	base := func(cache InferenceCacheConfig) *Config {
		return &Config{
			ModelList: []ModelEntry{{
				ModelName: "alpha",
				ModelParams: map[string]any{
					"api_base": "https://alpha.test/v1",
				},
			}},
			ProxySettings: ProxySettings{InferenceCache: cache},
		}
	}
	for _, tc := range []struct {
		name  string
		cache InferenceCacheConfig
		want  map[string]string
	}{
		{
			name: "enabled without path",
			cache: InferenceCacheConfig{
				Mode: "read_write",
				Persistence: InferenceCachePersistenceConfig{
					Enabled: true,
				},
			},
			want: map[string]string{
				"proxy_settings.inference_cache.persistence.path": "required",
			},
		},
		{
			name: "enabled while cache off",
			cache: InferenceCacheConfig{
				Mode: "off",
				Persistence: InferenceCachePersistenceConfig{
					Enabled: true,
					Path:    "logs/cache.db",
				},
			},
			want: map[string]string{
				"proxy_settings.inference_cache.persistence.enabled": "invalid_cache_persistence",
			},
		},
		{
			name: "unknown flush mode",
			cache: InferenceCacheConfig{
				Mode: "read_write",
				Persistence: InferenceCachePersistenceConfig{
					Enabled:   false,
					FlushMode: "eventually",
				},
			},
			want: map[string]string{
				"proxy_settings.inference_cache.persistence.flush_mode": "invalid_cache_persistence_mode",
			},
		},
		{
			name: "invalid enabled bool",
			cache: InferenceCacheConfig{
				Mode: "read_write",
				Persistence: InferenceCachePersistenceConfig{
					Enabled: 2,
					Path:    "logs/cache.db",
				},
			},
			want: map[string]string{
				"proxy_settings.inference_cache.persistence.enabled": "invalid_bool",
			},
		},
		{
			name: "memory path rejected",
			cache: InferenceCacheConfig{
				Mode: "read_write",
				Persistence: InferenceCachePersistenceConfig{
					Enabled: true,
					Path:    ":memory:",
				},
			},
			want: map[string]string{
				"proxy_settings.inference_cache.persistence.path": "invalid_path",
			},
		},
		{
			name: "valid",
			cache: InferenceCacheConfig{
				Mode: "read_write",
				Persistence: InferenceCachePersistenceConfig{
					Enabled:   true,
					Path:      "logs/cache.db",
					FlushMode: "writethrough",
				},
			},
			want: map[string]string{},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			issues := base(tc.cache).Validate()
			got := map[string]string{}
			for _, issue := range issues {
				if strings.HasPrefix(issue.Path, "proxy_settings.inference_cache.persistence") {
					got[issue.Path] = issue.Code
				}
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("persistence issues = %#v, want %#v; all issues=%#v", got, tc.want, issues)
			}
		})
	}
}

func TestValidateSemanticCacheRules(t *testing.T) {
	base := func(cache InferenceCacheConfig) *Config {
		return &Config{
			Nodes: []NodeEntry{{
				Name:    "node-a",
				APIBase: "https://node-a.test/v1",
				Model:   "upstream",
			}, {
				Name:    "embed-node",
				APIBase: "https://embed.test/v1",
				Model:   "embed",
			}},
			Models:        []ClientModelEntry{{Name: "client", Nodes: []string{"node-a"}}},
			ProxySettings: ProxySettings{InferenceCache: cache},
		}
	}
	validSemantic := SemanticCacheConfig{
		Enabled:             true,
		EmbeddingNode:       "embed-node",
		EmbeddingModel:      "text-embedding-test",
		SimilarityThreshold: 0.95,
		TopK:                5,
		MaxEntries:          100,
		MaxEntryBytes:       1024,
	}
	for _, tc := range []struct {
		name  string
		cache InferenceCacheConfig
		want  map[string]string
	}{
		{
			name:  "valid",
			cache: InferenceCacheConfig{Mode: "read_write", Semantic: validSemantic},
			want:  map[string]string{},
		},
		{
			name:  "enabled while global cache off",
			cache: InferenceCacheConfig{Mode: "off", Semantic: validSemantic},
			want: map[string]string{
				"proxy_settings.inference_cache.semantic.enabled": "invalid_semantic_cache",
			},
		},
		{
			name: "missing source fields",
			cache: InferenceCacheConfig{Mode: "read_write", Semantic: SemanticCacheConfig{
				Enabled: true,
			}},
			want: map[string]string{
				"proxy_settings.inference_cache.semantic.embedding_model": "required",
				"proxy_settings.inference_cache.semantic.embedding_node":  "required",
			},
		},
		{
			name: "unknown embedding node",
			cache: InferenceCacheConfig{Mode: "read_write", Semantic: SemanticCacheConfig{
				Enabled:             true,
				EmbeddingNode:       "missing-node",
				EmbeddingModel:      "text-embedding-test",
				SimilarityThreshold: 0.95,
			}},
			want: map[string]string{
				"proxy_settings.inference_cache.semantic.embedding_node": "unknown_node",
			},
		},
		{
			name: "invalid enabled bool",
			cache: InferenceCacheConfig{Mode: "read_write", Semantic: SemanticCacheConfig{
				Enabled: 2,
			}},
			want: map[string]string{
				"proxy_settings.inference_cache.semantic.enabled": "invalid_bool",
			},
		},
		{
			name: "invalid limits",
			cache: InferenceCacheConfig{Mode: "read_write", Semantic: SemanticCacheConfig{
				Enabled:             true,
				EmbeddingNode:       "embed-node",
				EmbeddingModel:      "text-embedding-test",
				SimilarityThreshold: 1.5,
				TopK:                101,
				MaxEntries:          100001,
				MaxEntryBytes:       64<<20 + 1,
			}},
			want: map[string]string{
				"proxy_settings.inference_cache.semantic.max_entries":          "invalid_limit",
				"proxy_settings.inference_cache.semantic.max_entry_bytes":      "invalid_limit",
				"proxy_settings.inference_cache.semantic.similarity_threshold": "invalid_limit",
				"proxy_settings.inference_cache.semantic.top_k":                "invalid_limit",
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			issues := base(tc.cache).Validate()
			got := map[string]string{}
			for _, issue := range issues {
				if strings.HasPrefix(issue.Path, "proxy_settings.inference_cache.semantic") {
					got[issue.Path] = issue.Code
				}
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("semantic issues = %#v, want %#v; all issues=%#v", got, tc.want, issues)
			}
		})
	}
}

func TestModelCatalogConfigDefaultsDisabled(t *testing.T) {
	cfg, err := FromMap(map[string]any{
		"model_list": []any{map[string]any{
			"model_name": "alpha",
			"model_params": map[string]any{
				"api_base": "https://alpha.test/v1",
			},
		}},
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ProxySettings.ModelCatalog.Enabled {
		t.Fatalf("model catalog enabled by default")
	}
	if len(cfg.ProxySettings.ModelCatalog.ProbeNodes) != 0 || len(cfg.ProxySettings.ModelCatalog.ProbeProviders) != 0 {
		t.Fatalf("model catalog probes opted in by default: %#v", cfg.ProxySettings.ModelCatalog)
	}
}

func TestValidateModelCatalogReportsInvalidDurationsAndNames(t *testing.T) {
	cfg := &Config{
		ModelList: []ModelEntry{{
			ModelName: "alpha",
			ModelParams: map[string]any{
				"api_base": "https://alpha.test/v1",
			},
		}},
		Providers: []ProviderEntry{{
			Name:    "provider-a",
			APIBase: "https://provider.test/v1",
		}},
		ProxySettings: ProxySettings{
			ModelCatalog: ModelCatalogConfig{
				ProbeIntervalSeconds:  -1,
				ProbeTimeoutSeconds:   -2,
				FailureBackoffSeconds: -3,
				CacheTTLSeconds:       -4,
				ProbeNodes:            []string{""},
				ProbeProviders:        []string{"provider-a", "provider-a"},
			},
		},
	}
	issues := cfg.Validate()
	got := map[string]string{}
	for _, issue := range issues {
		got[issue.Path] = issue.Code
	}
	wants := map[string]string{
		"proxy_settings.model_catalog.probe_interval_seconds":  "invalid_limit",
		"proxy_settings.model_catalog.probe_timeout_seconds":   "invalid_limit",
		"proxy_settings.model_catalog.failure_backoff_seconds": "invalid_limit",
		"proxy_settings.model_catalog.cache_ttl_seconds":       "invalid_limit",
		"proxy_settings.model_catalog.probe_nodes[0]":          "required",
		"proxy_settings.model_catalog.probe_providers[1]":      "duplicate_entry",
	}
	for path, code := range wants {
		if got[path] != code {
			t.Fatalf("issue %s = %q, want %q; all issues=%#v", path, got[path], code, issues)
		}
	}
}

func TestValidateTokenQuotaResetMode(t *testing.T) {
	for _, mode := range []string{"", "rolling", " daily ", "WEEKLY", "monthly", "YEARLY"} {
		t.Run("accepts "+mode, func(t *testing.T) {
			cfg := validConfigWithQuotaLimits(LimitsConfig{TokenQuotaResetMode: mode})
			for _, issue := range cfg.Validate() {
				if strings.HasPrefix(issue.Path, "app_keys.defaults.token_quota") {
					t.Fatalf("mode %q issue = %#v", mode, issue)
				}
			}
		})
	}
	for _, mode := range []string{"hourly", "fortnightly"} {
		t.Run("rejects "+mode, func(t *testing.T) {
			cfg := validConfigWithQuotaLimits(LimitsConfig{TokenQuotaResetMode: mode})
			issues := cfg.Validate()
			if !hasValidationIssue(issues, "app_keys.defaults.token_quota_reset_mode", "invalid_quota_reset_mode") {
				t.Fatalf("missing invalid reset mode issue for %q in %#v", mode, issues)
			}
		})
	}
}

func TestValidateRejectsCalendarResetModeWithWindowSeconds(t *testing.T) {
	cfg := validConfigWithQuotaLimits(LimitsConfig{
		TokenQuotaResetMode:     "daily",
		TokenQuotaWindowSeconds: 3600,
	})
	issues := cfg.Validate()
	if !hasValidationIssue(issues, "app_keys.defaults.token_quota_window_seconds", "invalid_quota_reset_window") {
		t.Fatalf("missing calendar/window conflict issue in %#v", issues)
	}
}

func TestValidateRequiredHeaderPolicyAcceptsModelScopedRule(t *testing.T) {
	cfg := &Config{
		ModelList: []ModelEntry{{
			ModelName: "alpha",
			ModelParams: map[string]any{
				"api_base": "https://alpha.test/v1",
			},
		}},
		AppKeys: AppKeysConfig{
			Enabled: true,
			RequiredHeaders: []RequiredHeader{{
				Name:   "X-Tenant-ID",
				LogAs:  "tenant.id",
				Models: []string{"alpha", "*"},
			}},
			Keys: []AppKeyEntry{{KeyID: "key-1", Secret: "secret"}},
		},
	}
	for _, issue := range cfg.Validate() {
		if strings.Contains(issue.Path, "required_headers") {
			t.Fatalf("unexpected required header issue: %#v", issue)
		}
	}
}

func TestValidateRequiredHeaderPolicyReportsFieldIssues(t *testing.T) {
	cfg := &Config{
		ModelList: []ModelEntry{{
			ModelName: "alpha",
			ModelParams: map[string]any{
				"api_base": "https://alpha.test/v1",
			},
		}},
		AppKeys: AppKeysConfig{
			Enabled:    true,
			HeaderName: "x-client-key",
			RequiredHeaders: []RequiredHeader{
				{Name: ""},
				{Name: "Authorization"},
				{Name: "x-client-key"},
				{Name: "bad header"},
				{Name: "X-Tenant-ID", LogAs: "bad alias"},
				{Name: "X-Tenant-ID"},
				{Name: "X-Model", Models: []string{" "}},
				{Name: " X-Trim "},
			},
			Groups: []AppKeyGroupEntry{{
				GroupID: "team-a",
				RequiredHeaders: []RequiredHeader{{
					Name: "X Group",
				}},
			}},
			Keys: []AppKeyEntry{{
				KeyID:  "key-1",
				Secret: "secret",
				RequiredHeaders: []RequiredHeader{{
					Name:  "X-Key",
					LogAs: strings.Repeat("a", 65),
				}},
			}},
		},
	}
	issues := cfg.Validate()
	got := map[string]string{}
	for _, issue := range issues {
		got[issue.Path] = issue.Code
	}
	wants := map[string]string{
		"app_keys.required_headers[0].name":           "required",
		"app_keys.required_headers[1].name":           "invalid_header",
		"app_keys.required_headers[2].name":           "invalid_header",
		"app_keys.required_headers[3].name":           "invalid_header",
		"app_keys.required_headers[4].log_as":         "invalid_log_as",
		"app_keys.required_headers[5].name":           "duplicate_header",
		"app_keys.required_headers[6].models[0]":      "required",
		"app_keys.required_headers[7].name":           "invalid_header",
		"app_keys.groups[0].required_headers[0].name": "invalid_header",
		"app_keys.keys[0].required_headers[0].log_as": "invalid_log_as",
	}
	for path, code := range wants {
		if got[path] != code {
			t.Fatalf("issue %s = %q, want %q; all issues=%#v", path, got[path], code, issues)
		}
	}
}

func TestValidateReportsInvalidProviderNodeEndpoints(t *testing.T) {
	cfg := &Config{
		Providers: []ProviderEntry{{
			Name:      "provider-a",
			APIBase:   "https://provider.test/v1",
			Endpoints: []string{"chat", "not-real"},
		}},
		Nodes: []NodeEntry{{
			Name:      "node-a",
			Provider:  "provider-a",
			Endpoints: []string{"embeddings", "bad endpoint"},
		}},
		Models: []ClientModelEntry{{
			Name:  "client-a",
			Nodes: []string{"node-a"},
		}},
	}
	issues := cfg.Validate()
	got := map[string]string{}
	for _, issue := range issues {
		got[issue.Path] = issue.Code
	}
	if got["providers[0].endpoints[1]"] != "invalid_endpoint" {
		t.Fatalf("provider endpoint issue = %q; all issues=%#v", got["providers[0].endpoints[1]"], issues)
	}
	if got["nodes[0].endpoints[1]"] != "invalid_endpoint" {
		t.Fatalf("node endpoint issue = %q; all issues=%#v", got["nodes[0].endpoints[1]"], issues)
	}
}

func TestValidateRejectsInvalidMetricsEnabledValue(t *testing.T) {
	cfg := &Config{
		ModelList: []ModelEntry{{
			ModelName: "alpha",
			ModelParams: map[string]any{
				"api_base": "https://alpha.test/v1",
			},
		}},
		ProxySettings: ProxySettings{Metrics: MetricsConfig{Enabled: []any{"true"}}},
	}

	issues := cfg.Validate()

	if len(issues) != 1 || issues[0].Path != "proxy_settings.metrics.enabled" || issues[0].Code != "invalid_bool" {
		t.Fatalf("issues = %#v", issues)
	}
}

func TestValidateRejectsInvalidMetricsTimingsEnabledValue(t *testing.T) {
	cfg := &Config{
		ModelList: []ModelEntry{{
			ModelName: "alpha",
			ModelParams: map[string]any{
				"api_base": "https://alpha.test/v1",
			},
		}},
		ProxySettings: ProxySettings{Metrics: MetricsConfig{Timings: MetricsTimingsConfig{Enabled: []any{"true"}}}},
	}

	issues := cfg.Validate()

	if len(issues) != 1 || issues[0].Path != "proxy_settings.metrics.timings.enabled" || issues[0].Code != "invalid_bool" {
		t.Fatalf("issues = %#v", issues)
	}
}

func TestValidateAcceptsBoolLikeMetricsEnabled(t *testing.T) {
	for _, value := range []any{nil, true, false, "yes", "off", 1, 0} {
		cfg := &Config{
			ModelList: []ModelEntry{{
				ModelName: "alpha",
				ModelParams: map[string]any{
					"api_base": "https://alpha.test/v1",
				},
			}},
			ProxySettings: ProxySettings{Metrics: MetricsConfig{Enabled: value, Timings: MetricsTimingsConfig{Enabled: value}}},
		}
		var got []ValidationIssue
		for _, issue := range cfg.Validate() {
			if issue.Path == "proxy_settings.metrics.enabled" || issue.Path == "proxy_settings.metrics.timings.enabled" {
				got = append(got, issue)
			}
		}
		if len(got) != 0 {
			t.Fatalf("metrics bool value=%#v issues = %#v", value, got)
		}
	}
}

func TestValidateOTelConfig(t *testing.T) {
	baseConfig := func(otel OTelConfig) *Config {
		return &Config{
			ModelList: []ModelEntry{{
				ModelName: "alpha",
				ModelParams: map[string]any{
					"api_base": "https://alpha.test/v1",
				},
			}},
			ProxySettings: ProxySettings{OTel: otel},
		}
	}
	for _, tc := range []struct {
		name      string
		otel      OTelConfig
		wantPath  string
		wantCode  string
		wantClean bool
	}{
		{name: "disabled without endpoint", otel: OTelConfig{Enabled: false}, wantClean: true},
		{name: "enabled hostport", otel: OTelConfig{Enabled: true, Endpoint: "collector.test:4317", Insecure: "true"}, wantClean: true},
		{name: "enabled logs", otel: OTelConfig{Enabled: true, Endpoint: "collector.test:4317", Logs: OTelLogsConfig{Enabled: true}}, wantClean: true},
		{name: "enabled url", otel: OTelConfig{Enabled: true, Endpoint: "https://collector.test:4317/v1/traces"}, wantClean: true},
		{name: "enabled missing endpoint", otel: OTelConfig{Enabled: true}, wantPath: "proxy_settings.otel.endpoint", wantCode: "required"},
		{name: "invalid enabled", otel: OTelConfig{Enabled: []any{"true"}}, wantPath: "proxy_settings.otel.enabled", wantCode: "invalid_bool"},
		{name: "invalid insecure", otel: OTelConfig{Insecure: 2}, wantPath: "proxy_settings.otel.insecure", wantCode: "invalid_bool"},
		{name: "invalid logs enabled", otel: OTelConfig{Logs: OTelLogsConfig{Enabled: 2}}, wantPath: "proxy_settings.otel.logs.enabled", wantCode: "invalid_bool"},
		{name: "logs require otel enabled", otel: OTelConfig{Enabled: false, Logs: OTelLogsConfig{Enabled: true}}, wantPath: "proxy_settings.otel.logs.enabled", wantCode: "requires_otel_enabled"},
		{name: "invalid exporter", otel: OTelConfig{Exporter: "jaeger"}, wantPath: "proxy_settings.otel.exporter", wantCode: "invalid_exporter"},
		{name: "invalid header name", otel: OTelConfig{Headers: map[string]any{" bad": "value"}}, wantPath: "proxy_settings.otel.headers. bad", wantCode: "invalid_header"},
		{name: "invalid header value", otel: OTelConfig{Headers: map[string]any{"x-otel": 1}}, wantPath: "proxy_settings.otel.headers.x-otel", wantCode: "invalid_header"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			issues := baseConfig(tc.otel).Validate()
			if tc.wantClean {
				for _, issue := range issues {
					if strings.HasPrefix(issue.Path, "proxy_settings.otel") {
						t.Fatalf("otel issues = %#v", issues)
					}
				}
				return
			}
			for _, issue := range issues {
				if issue.Path == tc.wantPath && issue.Code == tc.wantCode {
					return
				}
			}
			t.Fatalf("missing %s/%s in %#v", tc.wantPath, tc.wantCode, issues)
		})
	}
}

func TestToMapIncludesTypedOTelConfig(t *testing.T) {
	cfg := &Config{
		Raw: map[string]any{
			"proxy_settings": map[string]any{
				"metrics": map[string]any{"enabled": false},
			},
		},
		ProxySettings: ProxySettings{OTel: OTelConfig{
			Enabled:     true,
			ServiceName: "test-proxy",
			Exporter:    "otlp_grpc",
			Endpoint:    "collector.test:4317",
			Insecure:    true,
			Headers:     map[string]any{"x-otel-token": "${OTEL_TOKEN}"},
			Logs:        OTelLogsConfig{Enabled: true},
		}},
	}

	raw := cfg.ToMap()
	proxySettings := raw["proxy_settings"].(map[string]any)
	otel := proxySettings["otel"].(map[string]any)
	if otel["enabled"] != true || otel["endpoint"] != "collector.test:4317" || otel["service_name"] != "test-proxy" {
		t.Fatalf("otel config missing from ToMap: %#v", otel)
	}
	logs := otel["logs"].(map[string]any)
	if logs["enabled"] != true {
		t.Fatalf("otel logs config = %#v", logs)
	}
	headers := otel["headers"].(map[string]any)
	if headers["x-otel-token"] != "${OTEL_TOKEN}" {
		t.Fatalf("otel headers = %#v", headers)
	}
}

func validConfigWithQuotaLimits(limits LimitsConfig) *Config {
	return &Config{
		ModelList: []ModelEntry{{
			ModelName: "alpha",
			ModelParams: map[string]any{
				"api_base": "https://alpha.test/v1",
			},
		}},
		AppKeys: AppKeysConfig{Defaults: limits},
	}
}

func hasValidationIssue(issues []ValidationIssue, path string, code string) bool {
	for _, issue := range issues {
		if issue.Path == path && issue.Code == code {
			return true
		}
	}
	return false
}

func TestValidateRejectsNonBooleanNumericGuardedToggles(t *testing.T) {
	for _, tc := range []struct {
		name  string
		value any
		path  string
		cfg   ProxySettings
	}{
		{name: "metrics int", value: 2, path: "proxy_settings.metrics.enabled", cfg: ProxySettings{Metrics: MetricsConfig{Enabled: 2}}},
		{name: "metrics int64", value: int64(2), path: "proxy_settings.metrics.enabled", cfg: ProxySettings{Metrics: MetricsConfig{Enabled: int64(2)}}},
		{name: "metrics float", value: 2.5, path: "proxy_settings.metrics.enabled", cfg: ProxySettings{Metrics: MetricsConfig{Enabled: 2.5}}},
		{name: "metrics timings int", value: 2, path: "proxy_settings.metrics.timings.enabled", cfg: ProxySettings{Metrics: MetricsConfig{Timings: MetricsTimingsConfig{Enabled: 2}}}},
		{name: "otel int", value: 2, path: "proxy_settings.otel.enabled", cfg: ProxySettings{OTel: OTelConfig{Enabled: 2}}},
		{name: "otel logs int", value: 2, path: "proxy_settings.otel.logs.enabled", cfg: ProxySettings{OTel: OTelConfig{Logs: OTelLogsConfig{Enabled: 2}}}},
		{name: "profiling int", value: 2, path: "proxy_settings.profiling.enabled", cfg: ProxySettings{Profiling: ProfilingConfig{Enabled: 2}}},
		{name: "log to disk list", value: []any{"true"}, path: "proxy_settings.logging.log_to_disk", cfg: ProxySettings{Logging: LoggingConfig{LogToDisk: []any{"true"}}}},
		{name: "log parsed response map", value: map[string]any{"enabled": true}, path: "proxy_settings.logging.log_parsed_response", cfg: ProxySettings{Logging: LoggingConfig{LogParsedResponse: map[string]any{"enabled": true}}}},
		{name: "log parsed stream int", value: 2, path: "proxy_settings.logging.log_parsed_stream", cfg: ProxySettings{Logging: LoggingConfig{LogParsedStream: 2}}},
		{name: "full request storage enabled list", value: []any{"true"}, path: "proxy_settings.logging.full_request_storage.enabled", cfg: ProxySettings{Logging: LoggingConfig{FullRequestStore: map[string]any{"enabled": []any{"true"}}}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &Config{
				ModelList: []ModelEntry{{
					ModelName: "alpha",
					ModelParams: map[string]any{
						"api_base": "https://alpha.test/v1",
					},
				}},
				ProxySettings: tc.cfg,
			}
			issues := cfg.Validate()
			if len(issues) != 1 || issues[0].Path != tc.path || issues[0].Code != "invalid_bool" {
				t.Fatalf("enabled=%#v issues = %#v", tc.value, issues)
			}
		})
	}
}

func TestValidateQuotaAwareSelectionStrictBool(t *testing.T) {
	for _, tc := range []struct {
		name     string
		settings QuotaAwareSelectionSettings
		path     string
	}{
		{
			name:     "enabled",
			settings: QuotaAwareSelectionSettings{Enabled: 2},
			path:     "router_settings.quota_aware_selection.enabled",
		},
		{
			name:     "persist backend quota",
			settings: QuotaAwareSelectionSettings{PersistBackendQuota: 2},
			path:     "router_settings.quota_aware_selection.persist_backend_quota",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &Config{
				ModelList: []ModelEntry{{
					ModelName: "alpha",
					ModelParams: map[string]any{
						"api_base": "https://alpha.test/v1",
					},
				}},
				RouterSettings: RouterSettings{
					QuotaAwareSelection: tc.settings,
				},
			}
			issues := cfg.Validate()
			if len(issues) != 1 || issues[0].Path != tc.path || issues[0].Code != "invalid_bool" {
				t.Fatalf("issues = %#v", issues)
			}
		})
	}
}

func TestValidatePersistBackendQuotaRequiresQuotaAwareSelectionEnabled(t *testing.T) {
	cfg := &Config{
		ModelList: []ModelEntry{{
			ModelName: "alpha",
			ModelParams: map[string]any{
				"api_base": "https://alpha.test/v1",
			},
		}},
		RouterSettings: RouterSettings{
			QuotaAwareSelection: QuotaAwareSelectionSettings{
				Enabled:             false,
				PersistBackendQuota: true,
			},
		},
	}
	issues := cfg.Validate()
	if len(issues) != 1 ||
		issues[0].Path != "router_settings.quota_aware_selection.persist_backend_quota" ||
		issues[0].Code != "requires_quota_aware_selection_enabled" {
		t.Fatalf("issues = %#v", issues)
	}
}

func TestValidatePrefixCacheAffinityStrictBoolAndMaxEntries(t *testing.T) {
	cfg := &Config{
		ModelList: []ModelEntry{{
			ModelName: "alpha",
			ModelParams: map[string]any{
				"api_base": "https://alpha.test/v1",
			},
		}},
		RouterSettings: RouterSettings{
			PrefixCacheAffinity: PrefixCacheAffinitySettings{Enabled: 2, MaxEntries: -1},
		},
	}
	issues := cfg.Validate()
	var seenBool, seenLimit bool
	for _, issue := range issues {
		if issue.Path == "router_settings.prefix_cache_affinity.enabled" && issue.Code == "invalid_bool" {
			seenBool = true
		}
		if issue.Path == "router_settings.prefix_cache_affinity.max_entries" && issue.Code == "invalid_limit" {
			seenLimit = true
		}
	}
	if !seenBool || !seenLimit {
		t.Fatalf("expected strict-bool and max_entries issues, got %#v", issues)
	}
}

func TestPrefixCacheAffinityConfigFromMap(t *testing.T) {
	cfg, err := FromMap(map[string]any{
		"model_list": []any{map[string]any{
			"model_name":   "alpha",
			"model_params": map[string]any{"api_base": "http://alpha.test/v1"},
		}},
		"router_settings": map[string]any{
			"prefix_cache_affinity": map[string]any{
				"enabled":     "true",
				"max_entries": 17,
			},
		},
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	if !ParseBoolStrict(cfg.RouterSettings.PrefixCacheAffinity.Enabled) {
		t.Fatalf("prefix_cache_affinity.enabled = %#v, want true-like", cfg.RouterSettings.PrefixCacheAffinity.Enabled)
	}
	if got := cfg.RouterSettings.PrefixCacheAffinity.MaxEntries; got != 17 {
		t.Fatalf("prefix_cache_affinity.max_entries = %d, want 17", got)
	}
	for _, issue := range cfg.Validate() {
		if issue.Severity == "error" {
			t.Fatalf("unexpected validation error: %#v", issue)
		}
	}
}

func TestValidateConcurrencyAwareSelectionStrictBool(t *testing.T) {
	cfg := &Config{
		ModelList: []ModelEntry{{
			ModelName: "alpha",
			ModelParams: map[string]any{
				"api_base": "https://alpha.test/v1",
			},
		}},
		RouterSettings: RouterSettings{
			ConcurrencyAwareSelection: ConcurrencyAwareSelectionSettings{Enabled: 2},
		},
	}
	issues := cfg.Validate()
	if len(issues) != 1 || issues[0].Path != "router_settings.concurrency_aware_selection.enabled" || issues[0].Code != "invalid_bool" {
		t.Fatalf("issues = %#v", issues)
	}
}

func TestConcurrencyAwareSelectionConfigFromMap(t *testing.T) {
	cfg, err := FromMap(map[string]any{
		"model_list": []any{map[string]any{
			"model_name":   "alpha",
			"model_params": map[string]any{"api_base": "http://alpha.test/v1"},
		}},
		"router_settings": map[string]any{
			"concurrency_aware_selection": map[string]any{
				"enabled": "true",
			},
		},
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	if !ParseBoolStrict(cfg.RouterSettings.ConcurrencyAwareSelection.Enabled) {
		t.Fatalf("concurrency_aware_selection.enabled = %#v, want true-like", cfg.RouterSettings.ConcurrencyAwareSelection.Enabled)
	}
	for _, issue := range cfg.Validate() {
		if issue.Severity == "error" {
			t.Fatalf("unexpected validation error: %#v", issue)
		}
	}
}

func TestHealthRecoverySuccessThresholdConfig(t *testing.T) {
	cfg, err := FromMap(map[string]any{
		"model_list": []any{map[string]any{
			"model_name":   "alpha",
			"model_params": map[string]any{"api_base": "http://alpha.test/v1"},
		}},
		"router_settings": map[string]any{
			"health_checks": map[string]any{
				"recovery_success_threshold": 2,
			},
			"health_scoring": map[string]any{
				"recovery_success_threshold": 3,
			},
		},
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.RouterSettings.HealthChecks.RecoverySuccessThreshold; got != 2 {
		t.Fatalf("health_checks recovery_success_threshold = %d", got)
	}
	if got := cfg.RouterSettings.HealthScoring.RecoverySuccessThreshold; got != 3 {
		t.Fatalf("health_scoring recovery_success_threshold = %d", got)
	}
	for _, issue := range cfg.Validate() {
		if issue.Severity == "error" {
			t.Fatalf("unexpected validation error: %#v", issue)
		}
	}
}

func TestValidateRejectsNegativeHealthRecoverySuccessThreshold(t *testing.T) {
	cfg := &Config{
		ModelList: []ModelEntry{{
			ModelName:   "alpha",
			ModelParams: map[string]any{"api_base": "http://alpha.test/v1"},
		}},
		RouterSettings: RouterSettings{
			HealthChecks:  HealthCheckSettings{RecoverySuccessThreshold: -1},
			HealthScoring: HealthCheckSettings{RecoverySuccessThreshold: -2},
		},
	}
	issues := cfg.Validate()
	if !hasValidationIssue(issues, "router_settings.health_checks.recovery_success_threshold", "invalid_limit") {
		t.Fatalf("missing health_checks issue: %#v", issues)
	}
	if !hasValidationIssue(issues, "router_settings.health_scoring.recovery_success_threshold", "invalid_limit") {
		t.Fatalf("missing health_scoring issue: %#v", issues)
	}
}

func TestValidateAcceptsLegacyAndNodeBackedConfig(t *testing.T) {
	cfg := &Config{
		ModelList: []ModelEntry{{
			ModelName: "legacy",
			ModelParams: map[string]any{
				"api_base": "https://legacy.test/v1",
			},
		}},
		Providers: []ProviderEntry{{
			Name:      "provider-a",
			APIBase:   "https://provider.test/v1",
			Endpoints: []string{"chat/completions", "/v1/embeddings", "*"},
		}},
		Nodes: []NodeEntry{{
			Name:      "node-a",
			Provider:  "provider-a",
			Weight:    2,
			Endpoints: []string{"responses", "messages", "rerank"},
		}},
		Models: []ClientModelEntry{{
			Name:  "client-a",
			Nodes: []string{"node-a"},
		}},
	}
	for _, issue := range cfg.Validate() {
		if issue.Severity == "error" {
			t.Fatalf("unexpected validation issue: %#v", issue)
		}
	}
}

func TestSaveWritesTypedAppKeys(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	enabled := true
	cfg := &Config{
		Path: cfgPath,
		Raw:  map[string]any{},
		ModelList: []ModelEntry{{
			ModelName: "alpha",
			ModelParams: map[string]any{
				"api_base": "http://alpha.test/v1",
				"model":    "openai/upstream-alpha",
			},
		}},
		AppKeys: AppKeysConfig{
			Enabled:    true,
			HeaderName: "x-client-key",
			Groups: []AppKeyGroupEntry{{
				GroupID: "team-a",
				Name:    "Team A",
				Enabled: &enabled,
			}},
			Keys: []AppKeyEntry{{
				KeyID:       "client-a",
				GroupID:     "team-a",
				Name:        "Client A",
				Description: "Primary client",
				Secret:      "client-secret",
				Enabled:     &enabled,
				Limits:      LimitsConfig{RPS: 3},
			}},
		},
	}
	if err := cfg.Save(""); err != nil {
		t.Fatal(err)
	}
	saved, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	text := string(saved)
	for _, want := range []string{"app_keys:", "description: Primary client", "secret: client-secret", "header_name: x-client-key"} {
		if !strings.Contains(text, want) {
			t.Fatalf("saved config missing %q:\n%s", want, text)
		}
	}
	loaded, err := LoadWithOptions(LoadOptions{Path: cfgPath, SubstituteEnv: false})
	if err != nil {
		t.Fatal(err)
	}
	if got := loaded.AppKeys.Keys[0].Description; got != "Primary client" {
		t.Fatalf("description = %q", got)
	}
}

func TestSavePreservesRawModelListWhenPresent(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	raw := map[string]any{
		"model_list": []any{
			map[string]any{
				"model_name": "base",
				"model_params": map[string]any{
					"api_base": "http://base.test/v1",
					"model":    "openai/base",
				},
			},
			map[string]any{
				"model_name": "derived",
				"extends":    "base",
				"model_params": map[string]any{
					"model": "openai/derived",
				},
			},
		},
	}
	cfg, err := FromMap(raw, cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ModelList[1].Extends != "" {
		t.Fatalf("runtime model should be resolved, got extends=%q", cfg.ModelList[1].Extends)
	}
	cfg.AppKeys = AppKeysConfig{
		Enabled: true,
		Keys: []AppKeyEntry{{
			KeyID:  "client-a",
			Secret: "client-secret",
		}},
	}
	if err := cfg.Save(""); err != nil {
		t.Fatal(err)
	}
	saved, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	text := string(saved)
	if !strings.Contains(text, "extends: base") {
		t.Fatalf("saved config lost raw inheritance:\n%s", text)
	}
	if strings.Contains(text, "_inherited_from") {
		t.Fatalf("saved config wrote resolved inheritance metadata:\n%s", text)
	}
	if !strings.Contains(text, "key_id: client-a") {
		t.Fatalf("saved config missing app key:\n%s", text)
	}
}

func TestUpsertLegacyModelPreservesExistingRawInheritance(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	cfg, err := FromMap(map[string]any{
		"model_list": []any{
			map[string]any{
				"model_name": "base",
				"model_params": map[string]any{
					"api_base": "http://base.test/v1",
					"api_key":  "base-secret",
				},
			},
			map[string]any{
				"model_name": "child",
				"extends":    "base",
				"protected":  false,
				"model_params": map[string]any{
					"model": "openai/child",
				},
			},
		},
	}, cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ModelList[1].Extends != "" {
		t.Fatalf("runtime child should be resolved, got extends=%q", cfg.ModelList[1].Extends)
	}

	cfg.UpsertLegacyModel(ModelEntry{
		ModelName: "new-model",
		ModelParams: map[string]any{
			"api_base": "http://new.test/v1",
			"model":    "openai/new",
		},
	}, nil)

	chain, ok := cfg.ModelAncestry("child")
	if !ok || !reflect.DeepEqual(chain, []string{"child", "base"}) {
		t.Fatalf("child ancestry after upsert = %#v ok=%v", chain, ok)
	}
	if err := cfg.Save(""); err != nil {
		t.Fatal(err)
	}
	saved, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	text := string(saved)
	if !strings.Contains(text, "extends: base") {
		t.Fatalf("saved config lost raw inheritance:\n%s", text)
	}
	if !strings.Contains(text, "model_name: new-model") {
		t.Fatalf("saved config missing new model:\n%s", text)
	}
}

func TestCopyLegacyModelPreservesRawInheritanceEnvAndStripsMetadata(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	cfg, err := FromMap(map[string]any{
		"model_list": []any{
			map[string]any{
				"model_name": "base",
				"model_params": map[string]any{
					"api_base": "http://base.test/v1",
					"api_key":  "${BASE_KEY}",
					"model":    "openai/base",
				},
			},
			map[string]any{
				"model_name":      "child",
				"extends":         "base",
				"protected":       false,
				"editable":        true,
				"source":          "ui",
				"_inherited_from": "base",
				"unknown_key":     "preserve-me",
				"model_params": map[string]any{
					"api_key": "${CHILD_KEY}",
					"model":   "openai/child",
				},
				"modules": map[string]any{
					"upstream": map[string]any{"response": []any{"parse_tags"}},
				},
				"parameters": map[string]any{
					"temperature": map[string]any{"default": 0.3, "allow_override": false},
				},
				"limits": map[string]any{"rpm": 10},
			},
		},
	}, cfgPath)
	if err != nil {
		t.Fatal(err)
	}

	copied, err := cfg.CopyLegacyModel("child", "child-copy")
	if err != nil {
		t.Fatal(err)
	}
	if copied["model_name"] != "child-copy" || copied["extends"] != "base" || copied["unknown_key"] != "preserve-me" {
		t.Fatalf("copied raw entry = %#v", copied)
	}
	for _, key := range []string{"editable", "source", "_inherited_from"} {
		if _, ok := copied[key]; ok {
			t.Fatalf("copied raw entry kept %s: %#v", key, copied)
		}
	}
	params := copied["model_params"].(map[string]any)
	if params["api_key"] != "${CHILD_KEY}" {
		t.Fatalf("copied api_key placeholder = %#v", params["api_key"])
	}
	chain, ok := cfg.ModelAncestry("child-copy")
	if !ok || !reflect.DeepEqual(chain, []string{"child-copy", "base"}) {
		t.Fatalf("child-copy ancestry = %#v ok=%v", chain, ok)
	}

	if err := cfg.Save(""); err != nil {
		t.Fatal(err)
	}
	saved, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	text := string(saved)
	for _, want := range []string{"model_name: child-copy", "extends: base", "api_key: ${CHILD_KEY}", "unknown_key: preserve-me"} {
		if !strings.Contains(text, want) {
			t.Fatalf("saved config missing %q:\n%s", want, text)
		}
	}
	reloaded, err := LoadWithOptions(LoadOptions{Path: cfgPath, SubstituteEnv: false})
	if err != nil {
		t.Fatal(err)
	}
	reloadedCopy, ok := reloaded.RawLegacyModel("child-copy")
	if !ok {
		t.Fatal("reloaded copy not found")
	}
	for _, key := range []string{"editable", "source", "_inherited_from"} {
		if _, ok := reloadedCopy[key]; ok {
			t.Fatalf("reloaded copied entry kept %s: %#v", key, reloadedCopy)
		}
	}
}

func TestRawRoutingNestedFieldReplacementPreservesOmittedRaw(t *testing.T) {
	cfg, err := FromMap(map[string]any{
		"providers": []any{
			map[string]any{
				"name":            "provider-a",
				"api_base":        "http://provider.test/v1",
				"api_key":         "${PROVIDER_KEY}",
				"api_type":        "openai",
				"limits":          map[string]any{"rpm": 10, "rps": 1},
				"unknown_nested":  map[string]any{"keep": true},
				"custom_provider": "keep-provider",
			},
		},
		"nodes": []any{
			map[string]any{
				"name":         "node-a",
				"provider":     "provider-a",
				"model":        "openai/upstream-a",
				"limits":       map[string]any{"rpm": 20, "concurrency": 2},
				"modules":      map[string]any{"request": []any{"tagger"}, "response": []any{"parser"}},
				"allowed_keys": []any{"key-a", "key-b"},
				"custom_node":  "keep-node",
			},
		},
		"models": []any{
			map[string]any{
				"name":         "client-a",
				"nodes":        []any{"node-a"},
				"limits":       map[string]any{"rpm": 30, "concurrency": 3},
				"allowed_keys": "all",
				"custom_model": "keep-client",
			},
		},
	}, "")
	if err != nil {
		t.Fatal(err)
	}

	if _, err := cfg.UpdateRawProvider("provider-a", map[string]any{"limits": map[string]any{"rps": 2}}); err != nil {
		t.Fatal(err)
	}
	rawProvider, ok := cfg.RawProvider("provider-a")
	if !ok {
		t.Fatal("provider-a missing")
	}
	if !reflect.DeepEqual(rawProvider["limits"], map[string]any{"rps": 2}) {
		t.Fatalf("provider limits were not replaced: %#v", rawProvider["limits"])
	}
	if rawProvider["api_key"] != "${PROVIDER_KEY}" || rawProvider["custom_provider"] != "keep-provider" {
		t.Fatalf("provider omitted raw fields were not preserved: %#v", rawProvider)
	}

	if _, err := cfg.UpdateRawNode("node-a", map[string]any{
		"limits":       map[string]any{},
		"modules":      map[string]any{"response": []any{"new-parser"}},
		"allowed_keys": []any{},
	}); err != nil {
		t.Fatal(err)
	}
	rawNode, ok := cfg.RawNode("node-a")
	if !ok {
		t.Fatal("node-a missing")
	}
	if !reflect.DeepEqual(rawNode["limits"], map[string]any{}) {
		t.Fatalf("node limits were not explicitly cleared: %#v", rawNode["limits"])
	}
	if !reflect.DeepEqual(rawNode["modules"], map[string]any{"response": []any{"new-parser"}}) {
		t.Fatalf("node modules were not replaced: %#v", rawNode["modules"])
	}
	if !reflect.DeepEqual(rawNode["allowed_keys"], []any{}) {
		t.Fatalf("node allowed_keys were not explicitly cleared: %#v", rawNode["allowed_keys"])
	}
	if rawNode["custom_node"] != "keep-node" {
		t.Fatalf("node omitted raw fields were not preserved: %#v", rawNode)
	}

	if _, err := cfg.UpdateRawClientModel("client-a", map[string]any{
		"limits":       map[string]any{"token_quota": 100},
		"allowed_keys": []any{"key-c"},
	}); err != nil {
		t.Fatal(err)
	}
	rawClient, ok := cfg.RawClientModel("client-a")
	if !ok {
		t.Fatal("client-a missing")
	}
	if !reflect.DeepEqual(rawClient["limits"], map[string]any{"token_quota": 100}) {
		t.Fatalf("client limits were not replaced: %#v", rawClient["limits"])
	}
	if !reflect.DeepEqual(rawClient["allowed_keys"], []any{"key-c"}) {
		t.Fatalf("client allowed_keys were not replaced: %#v", rawClient["allowed_keys"])
	}
	if rawClient["custom_model"] != "keep-client" {
		t.Fatalf("client omitted raw fields were not preserved: %#v", rawClient)
	}
}

func TestRawRoutingNestedFieldInvalidUpdateDoesNotMutate(t *testing.T) {
	cfg, err := FromMap(map[string]any{
		"providers": []any{
			map[string]any{
				"name":     "provider-a",
				"api_base": "http://provider.test/v1",
				"api_type": "openai",
				"limits":   map[string]any{"rpm": 10},
			},
		},
		"nodes": []any{
			map[string]any{
				"name":         "node-a",
				"provider":     "provider-a",
				"model":        "openai/upstream-a",
				"limits":       map[string]any{"rpm": 20},
				"modules":      map[string]any{"response": []any{"parser"}},
				"allowed_keys": []any{"key-a"},
			},
		},
		"models": []any{
			map[string]any{
				"name":         "client-a",
				"nodes":        []any{"node-a"},
				"limits":       map[string]any{"rpm": 30},
				"allowed_keys": "all",
			},
		},
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	before := cloneMap(cfg.Raw)

	if _, err := cfg.UpdateRawProvider("provider-a", map[string]any{"limits": "bad"}); err == nil {
		t.Fatal("UpdateRawProvider accepted non-object limits")
	}
	if !reflect.DeepEqual(cfg.Raw, before) {
		t.Fatalf("provider invalid update mutated raw:\nwant %#v\ngot  %#v", before, cfg.Raw)
	}
	if _, err := cfg.UpdateRawNode("node-a", map[string]any{"modules": []any{"bad"}}); err == nil {
		t.Fatal("UpdateRawNode accepted non-object modules")
	}
	if !reflect.DeepEqual(cfg.Raw, before) {
		t.Fatalf("node invalid update mutated raw:\nwant %#v\ngot  %#v", before, cfg.Raw)
	}
	if _, err := cfg.UpdateRawClientModel("client-a", map[string]any{"limits": "bad"}); err == nil {
		t.Fatal("UpdateRawClientModel accepted non-object limits")
	}
	if !reflect.DeepEqual(cfg.Raw, before) {
		t.Fatalf("client invalid update mutated raw:\nwant %#v\ngot  %#v", before, cfg.Raw)
	}
}

func TestCopyLegacyModelRejectsMissingAndDuplicate(t *testing.T) {
	cfg, err := FromMap(map[string]any{
		"model_list": []any{
			map[string]any{
				"model_name": "alpha",
				"model_params": map[string]any{
					"api_base": "http://alpha.test/v1",
					"model":    "openai/alpha",
				},
			},
		},
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cfg.CopyLegacyModel("missing", "copy"); !errors.Is(err, ErrLegacyModelNotFound) {
		t.Fatalf("missing err = %v", err)
	}
	if _, err := cfg.CopyLegacyModel("alpha", "alpha"); !errors.Is(err, ErrLegacyModelDuplicate) {
		t.Fatalf("duplicate err = %v", err)
	}
}

func TestDeleteLegacyModelsRefusesDependentsWithoutCascade(t *testing.T) {
	cfg, err := FromMap(map[string]any{
		"model_list": []any{
			map[string]any{
				"model_name": "base",
				"model_params": map[string]any{
					"api_base": "http://base.test/v1",
				},
			},
			map[string]any{
				"model_name": "child",
				"extends":    "base",
				"model_params": map[string]any{
					"model": "openai/child",
				},
			},
			map[string]any{
				"model_name": "grandchild",
				"extends":    "child",
				"model_params": map[string]any{
					"model": "openai/grandchild",
				},
			},
		},
	}, "")
	if err != nil {
		t.Fatal(err)
	}

	_, err = cfg.DeleteLegacyModels("base", false)
	var dependentsErr *LegacyModelDependentsError
	if !errors.As(err, &dependentsErr) {
		t.Fatalf("err = %v, want LegacyModelDependentsError", err)
	}
	if !reflect.DeepEqual(dependentsErr.Direct, []string{"child"}) ||
		!reflect.DeepEqual(dependentsErr.Descendants, []string{"child", "grandchild"}) {
		t.Fatalf("dependents err = %#v", dependentsErr)
	}
	if _, ok := cfg.FindLegacyModel("base"); !ok {
		t.Fatal("base was deleted despite conflict")
	}
}

func TestDeleteLegacyModelsCascadeDeletesDescendantsAndPreservesUnrelatedRawExtends(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	cfg, err := FromMap(map[string]any{
		"model_list": []any{
			map[string]any{
				"model_name": "base",
				"model_params": map[string]any{
					"api_base": "http://base.test/v1",
				},
			},
			map[string]any{
				"model_name": "child",
				"extends":    "base",
				"model_params": map[string]any{
					"model": "openai/child",
				},
			},
			map[string]any{
				"model_name": "grandchild",
				"extends":    "child",
				"model_params": map[string]any{
					"model": "openai/grandchild",
				},
			},
			map[string]any{
				"model_name": "sibling",
				"extends":    "base",
				"model_params": map[string]any{
					"model": "openai/sibling",
				},
			},
		},
	}, cfgPath)
	if err != nil {
		t.Fatal(err)
	}

	removed, err := cfg.DeleteLegacyModels("child", true)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(removed, []string{"child", "grandchild"}) {
		t.Fatalf("removed = %#v", removed)
	}
	for _, name := range []string{"child", "grandchild"} {
		if _, ok := cfg.FindLegacyModel(name); ok {
			t.Fatalf("%s still present", name)
		}
	}
	chain, ok := cfg.ModelAncestry("sibling")
	if !ok || !reflect.DeepEqual(chain, []string{"sibling", "base"}) {
		t.Fatalf("sibling ancestry = %#v ok=%v", chain, ok)
	}
	if err := cfg.Save(""); err != nil {
		t.Fatal(err)
	}
	saved, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	text := string(saved)
	if strings.Contains(text, "model_name: child") || strings.Contains(text, "model_name: grandchild") {
		t.Fatalf("saved config kept deleted models:\n%s", text)
	}
	if !strings.Contains(text, "model_name: sibling") || !strings.Contains(text, "extends: base") {
		t.Fatalf("saved config lost sibling inheritance:\n%s", text)
	}
}

func TestDeleteLegacyModelsScrubsFallbackSourcesAndTargets(t *testing.T) {
	cfg, err := FromMap(map[string]any{
		"model_list": []any{
			map[string]any{
				"model_name": "target",
				"model_params": map[string]any{
					"api_base": "http://target.test/v1",
				},
			},
			map[string]any{
				"model_name": "target-child",
				"extends":    "target",
				"model_params": map[string]any{
					"model": "openai/child",
				},
			},
			map[string]any{
				"model_name": "other",
				"model_params": map[string]any{
					"api_base": "http://other.test/v1",
				},
			},
			map[string]any{
				"model_name": "source",
				"model_params": map[string]any{
					"api_base": "http://source.test/v1",
				},
			},
		},
		"router_settings": map[string]any{
			"custom_policy": "preserve-me",
			"fallbacks": []any{
				map[string]any{"target": []any{"other"}},
				map[string]any{"other": []any{"target", "target-child", "source"}},
				map[string]any{"source": []any{"target-child"}},
			},
		},
	}, "")
	if err != nil {
		t.Fatal(err)
	}

	removed, err := cfg.DeleteLegacyModels("target", true)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(removed, []string{"target", "target-child"}) {
		t.Fatalf("removed = %#v", removed)
	}
	want := []map[string]any{{"other": []any{"source"}}}
	if !reflect.DeepEqual(cfg.RouterSettings.Fallbacks, want) {
		t.Fatalf("fallbacks = %#v, want %#v", cfg.RouterSettings.Fallbacks, want)
	}
	rawRouter := cfg.Raw["router_settings"].(map[string]any)
	if got := rawRouter["custom_policy"]; got != "preserve-me" {
		t.Fatalf("raw router setting = %#v", rawRouter)
	}
}

func TestReplaceRouterFallbacksPreservesRawRouterSettingsAndUnknowns(t *testing.T) {
	cfg, err := FromMap(map[string]any{
		"unknown_top": "keep-top",
		"model_list": []any{
			map[string]any{
				"model_name": "legacy-a",
				"model_params": map[string]any{
					"api_base": "http://legacy-a.test/v1",
				},
			},
			map[string]any{
				"model_name": "legacy-b",
				"model_params": map[string]any{
					"api_base": "http://legacy-b.test/v1",
				},
			},
		},
		"providers": []any{
			map[string]any{
				"name":     "provider-a",
				"api_base": "http://provider.test/v1",
				"api_type": "openai",
			},
		},
		"nodes": []any{
			map[string]any{
				"name":     "node-a",
				"provider": "provider-a",
				"model":    "openai/upstream-a",
			},
		},
		"models": []any{
			map[string]any{
				"name":  "client-a",
				"nodes": []any{"node-a"},
			},
		},
		"router_settings": map[string]any{
			"custom_policy": "keep-router",
			"num_retries":   2,
			"fallbacks": []any{
				map[string]any{"legacy-a": []any{"legacy-b"}},
			},
		},
	}, "")
	if err != nil {
		t.Fatal(err)
	}

	got, err := cfg.ReplaceRouterFallbacks([]any{
		map[string]any{"client-a": []any{"legacy-a", "node-a"}},
		map[string]any{"legacy-a": []string{"client-a"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []map[string]any{
		{"client-a": []any{"legacy-a", "node-a"}},
		{"legacy-a": []any{"client-a"}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("returned fallbacks = %#v, want %#v", got, want)
	}
	if !reflect.DeepEqual(cfg.RouterSettings.Fallbacks, want) {
		t.Fatalf("runtime fallbacks = %#v, want %#v", cfg.RouterSettings.Fallbacks, want)
	}
	if cfg.Raw["unknown_top"] != "keep-top" {
		t.Fatalf("top-level unknown was not preserved: %#v", cfg.Raw)
	}
	rawRouter := cfg.Raw["router_settings"].(map[string]any)
	if rawRouter["custom_policy"] != "keep-router" || rawRouter["num_retries"] != 2 {
		t.Fatalf("router_settings siblings were not preserved: %#v", rawRouter)
	}
	if !reflect.DeepEqual(rawRouter["fallbacks"], want) {
		t.Fatalf("raw fallbacks = %#v, want %#v", rawRouter["fallbacks"], want)
	}

	readBack := cfg.RawRouterFallbacks()
	readBack[0]["client-a"] = []any{"mutated"}
	if reflect.DeepEqual(cfg.RawRouterFallbacks(), readBack) {
		t.Fatalf("RawRouterFallbacks returned mutable internal state: %#v", readBack)
	}
}

func TestReplaceRouterFallbacksRejectsInvalidPayloadWithoutMutation(t *testing.T) {
	cfg, err := FromMap(map[string]any{
		"model_list": []any{
			map[string]any{
				"model_name": "legacy-a",
				"model_params": map[string]any{
					"api_base": "http://legacy-a.test/v1",
				},
			},
		},
		"providers": []any{
			map[string]any{
				"name":     "provider-a",
				"api_base": "http://provider.test/v1",
				"api_type": "openai",
			},
		},
		"nodes": []any{
			map[string]any{
				"name":     "node-a",
				"provider": "provider-a",
				"model":    "openai/upstream-a",
			},
		},
		"models": []any{
			map[string]any{
				"name":  "client-a",
				"nodes": []any{"node-a"},
			},
		},
		"router_settings": map[string]any{
			"custom_policy": "keep-router",
			"fallbacks": []any{
				map[string]any{"client-a": []any{"legacy-a"}},
			},
		},
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	beforeRaw := cloneMap(cfg.Raw)
	beforeFallbacks := cloneFallbackEntries(cfg.RouterSettings.Fallbacks)

	cases := []struct {
		name    string
		payload any
	}{
		{name: "not a list", payload: map[string]any{"client-a": []any{"legacy-a"}}},
		{name: "entry not object", payload: []any{"client-a"}},
		{name: "multi key entry", payload: []any{map[string]any{"client-a": []any{"legacy-a"}, "legacy-a": []any{"client-a"}}}},
		{name: "empty source", payload: []any{map[string]any{"": []any{"legacy-a"}}}},
		{name: "empty targets", payload: []any{map[string]any{"client-a": []any{}}}},
		{name: "target not list", payload: []any{map[string]any{"client-a": "legacy-a"}}},
		{name: "non string target", payload: []any{map[string]any{"client-a": []any{12}}}},
		{name: "empty target", payload: []any{map[string]any{"client-a": []any{""}}}},
		{name: "unknown source", payload: []any{map[string]any{"missing": []any{"legacy-a"}}}},
		{name: "unknown target", payload: []any{map[string]any{"client-a": []any{"missing"}}}},
		{name: "duplicate source", payload: []any{map[string]any{"client-a": []any{"legacy-a"}}, map[string]any{"client-a": []any{"node-a"}}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := cfg.ReplaceRouterFallbacks(tc.payload)
			if !errors.Is(err, ErrInvalidFallbacks) {
				t.Fatalf("err = %v, want ErrInvalidFallbacks", err)
			}
			if !reflect.DeepEqual(cfg.Raw, beforeRaw) {
				t.Fatalf("raw mutated after invalid payload:\nwant %#v\ngot  %#v", beforeRaw, cfg.Raw)
			}
			if !reflect.DeepEqual(cfg.RouterSettings.Fallbacks, beforeFallbacks) {
				t.Fatalf("fallbacks mutated after invalid payload:\nwant %#v\ngot  %#v", beforeFallbacks, cfg.RouterSettings.Fallbacks)
			}
		})
	}
}

func TestAdminRoutingRawCRUDPreservesUnknownsSecretsAndDependencies(t *testing.T) {
	cfg, err := FromMap(map[string]any{
		"providers": []any{
			map[string]any{
				"name":       "provider-a",
				"api_base":   "http://provider.test/v1",
				"api_key":    "${PROVIDER_KEY}",
				"api_type":   "openai",
				"custom_raw": "keep-provider",
			},
		},
		"nodes": []any{
			map[string]any{
				"name":       "node-a",
				"provider":   "provider-a",
				"model":      "openai/upstream-a",
				"custom_raw": "keep-node",
			},
		},
		"models": []any{
			map[string]any{
				"name":       "client-a",
				"nodes":      []any{"node-a"},
				"custom_raw": "keep-client-model",
			},
			map[string]any{
				"name":  "client-b",
				"nodes": []any{"node-a"},
			},
		},
		"router_settings": map[string]any{
			"custom_policy": "keep-router",
			"fallbacks": []any{
				map[string]any{"client-a": []any{"client-b"}},
				map[string]any{"other": []any{"client-a", "client-b"}},
			},
		},
	}, "")
	if err != nil {
		t.Fatal(err)
	}

	providers := cfg.AdminProviders()
	if len(providers) != 1 || providers[0]["custom_raw"] != "keep-provider" {
		t.Fatalf("providers = %#v", providers)
	}
	if _, ok := providers[0]["api_key"]; ok {
		t.Fatalf("admin provider exposed api_key: %#v", providers[0])
	}
	updated, err := cfg.UpdateRawProvider("provider-a", map[string]any{
		"api_base":   "http://changed-provider.test/v1",
		"parameters": map[string]any{"temperature": 0.2},
	})
	if err != nil {
		t.Fatal(err)
	}
	if updated["api_key"] != "${PROVIDER_KEY}" || updated["custom_raw"] != "keep-provider" {
		t.Fatalf("updated provider did not preserve raw fields: %#v", updated)
	}
	rawProvider, _ := cfg.RawProvider("provider-a")
	if rawProvider["api_key"] != "${PROVIDER_KEY}" || rawProvider["custom_raw"] != "keep-provider" {
		t.Fatalf("raw provider = %#v", rawProvider)
	}
	if _, err := cfg.UpdateRawProvider("provider-a", map[string]any{"name": "renamed"}); !errors.Is(err, ErrAdminResourceNameMismatch) {
		t.Fatalf("rename err = %v", err)
	}

	if err := cfg.DeleteRawProvider("provider-a"); !errors.Is(err, ErrProviderHasNodes) {
		t.Fatalf("provider delete err = %v", err)
	} else {
		var refs *AdminResourceReferencesError
		if !errors.As(err, &refs) || !reflect.DeepEqual(refs.References, []string{"node-a"}) {
			t.Fatalf("provider refs = %#v err=%v", refs, err)
		}
	}
	if err := cfg.DeleteRawNode("node-a"); !errors.Is(err, ErrNodeHasClientModels) {
		t.Fatalf("node delete err = %v", err)
	} else {
		var refs *AdminResourceReferencesError
		if !errors.As(err, &refs) || !reflect.DeepEqual(refs.References, []string{"client-a", "client-b"}) {
			t.Fatalf("node refs = %#v err=%v", refs, err)
		}
	}

	if err := cfg.DeleteRawClientModel("client-a"); err != nil {
		t.Fatal(err)
	}
	if _, ok := cfg.RawClientModel("client-a"); ok {
		t.Fatal("deleted client model still exists")
	}
	wantFallbacks := []map[string]any{{"other": []any{"client-b"}}}
	if !reflect.DeepEqual(cfg.RouterSettings.Fallbacks, wantFallbacks) {
		t.Fatalf("fallbacks = %#v, want %#v", cfg.RouterSettings.Fallbacks, wantFallbacks)
	}
	rawRouter := cfg.Raw["router_settings"].(map[string]any)
	if rawRouter["custom_policy"] != "keep-router" {
		t.Fatalf("router raw setting = %#v", rawRouter)
	}
}

func TestAdminModelTreeUsesRawExtendsAndMasksConfig(t *testing.T) {
	cfg, err := FromMap(map[string]any{
		"model_list": []any{
			map[string]any{
				"model_name": "base",
				"model_params": map[string]any{
					"api_base":      "http://base.test/v1",
					"api_key":       "base-secret",
					"client_cert":   "client-cert-secret",
					"client_key":    "client-key-secret",
					"client_secret": "nested-secret",
					"access_token":  "access-token-secret",
					"model":         "openai/base",
				},
				"_inherited_from": "ghost",
			},
			map[string]any{
				"model_name": "derived",
				"extends":    "base",
				"protected":  false,
				"model_params": map[string]any{
					"model": "openai/derived",
				},
			},
			map[string]any{
				"model_name": "grandchild",
				"extends":    "derived",
				"model_params": map[string]any{
					"model": "openai/grandchild",
				},
			},
		},
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ModelList[1].Extends != "" {
		t.Fatalf("runtime model should be resolved, got extends=%q", cfg.ModelList[1].Extends)
	}

	tree := cfg.ModelTree()
	if !reflect.DeepEqual(tree.Roots, []string{"base"}) {
		t.Fatalf("roots = %#v", tree.Roots)
	}
	if got := tree.Nodes["base"].Parent; got != "" {
		t.Fatalf("base parent = %q, want no _inherited_from edge", got)
	}
	if got := tree.Nodes["derived"].Parent; got != "base" {
		t.Fatalf("derived parent = %q", got)
	}
	if got := tree.Nodes["grandchild"].Parent; got != "derived" {
		t.Fatalf("grandchild parent = %q", got)
	}
	if !reflect.DeepEqual(tree.Nodes["base"].Children, []string{"derived"}) {
		t.Fatalf("base children = %#v", tree.Nodes["base"].Children)
	}
	params := tree.Nodes["derived"].Config["model_params"].(map[string]any)
	if got := params["api_base"]; got != "http://base.test/v1" {
		t.Fatalf("derived inherited api_base = %v", got)
	}
	for _, key := range []string{"api_key", "client_cert", "client_key", "client_secret", "access_token"} {
		if _, ok := params[key]; ok {
			t.Fatalf("derived config exposed %s: %#v", key, params)
		}
	}
	if tree.Nodes["derived"].Protected || !tree.Nodes["derived"].Editable {
		t.Fatalf("derived protection/editable = %#v", tree.Nodes["derived"])
	}
}

func TestMaskSensitiveOmitsPostgresDSNAndPreservesMissing(t *testing.T) {
	existing := map[string]any{
		"database": map[string]any{
			"backend": "postgres",
			"connection": map[string]any{
				"postgres": map[string]any{
					"dsn": "postgres://user:secret@db.internal/yallmp",
				},
			},
		},
	}

	masked, ok := MaskSensitive(existing).(map[string]any)
	if !ok {
		t.Fatalf("masked = %#v", masked)
	}
	db := masked["database"].(map[string]any)
	connection := db["connection"].(map[string]any)
	postgres := connection["postgres"].(map[string]any)
	if _, ok := postgres["dsn"]; ok {
		t.Fatalf("masked config exposed dsn: %#v", masked)
	}

	preserved := PreserveMissingSensitiveFields(masked, existing).(map[string]any)
	db = preserved["database"].(map[string]any)
	connection = db["connection"].(map[string]any)
	postgres = connection["postgres"].(map[string]any)
	if got := postgres["dsn"]; got != "postgres://user:secret@db.internal/yallmp" {
		t.Fatalf("preserved dsn = %#v", got)
	}
}

func TestAdminModelListSplitsProtectedAndUnprotected(t *testing.T) {
	cfg, err := FromMap(map[string]any{
		"model_list": []any{
			map[string]any{
				"model_name": "protected-base",
				"model_params": map[string]any{
					"api_base": "http://protected.test/v1",
					"api_key":  "protected-secret",
				},
			},
			map[string]any{
				"model_name": "editable-child",
				"extends":    "protected-base",
				"protected":  false,
				"model_params": map[string]any{
					"model": "openai/editable",
				},
			},
		},
	}, "")
	if err != nil {
		t.Fatal(err)
	}

	models := cfg.AdminModels()
	if got := modelNames(models.Protected); !reflect.DeepEqual(got, []string{"protected-base"}) {
		t.Fatalf("protected names = %#v", got)
	}
	if got := modelNames(models.Unprotected); !reflect.DeepEqual(got, []string{"editable-child"}) {
		t.Fatalf("unprotected names = %#v", got)
	}
	if strings.Contains(fmt.Sprint(models), "protected-secret") {
		t.Fatalf("admin model list exposed secret: %#v", models)
	}
}

func TestAdminModelAncestryAndDependents(t *testing.T) {
	cfg, err := FromMap(map[string]any{
		"model_list": []any{
			map[string]any{
				"model_name": "base",
				"model_params": map[string]any{
					"api_base": "http://base.test/v1",
				},
			},
			map[string]any{
				"model_name": "z-child",
				"extends":    "base",
				"model_params": map[string]any{
					"model": "openai/z",
				},
			},
			map[string]any{
				"model_name": "a-child",
				"extends":    "base",
				"model_params": map[string]any{
					"model": "openai/a",
				},
			},
			map[string]any{
				"model_name": "grandchild",
				"extends":    "a-child",
				"model_params": map[string]any{
					"model": "openai/grandchild",
				},
			},
		},
	}, "")
	if err != nil {
		t.Fatal(err)
	}

	chain, ok := cfg.ModelAncestry("grandchild")
	if !ok {
		t.Fatal("grandchild ancestry not found")
	}
	if !reflect.DeepEqual(chain, []string{"grandchild", "a-child", "base"}) {
		t.Fatalf("chain = %#v", chain)
	}
	if _, ok := cfg.ModelAncestry("missing"); ok {
		t.Fatal("missing ancestry returned ok")
	}
	direct, descendants, ok := cfg.ModelDependents("base")
	if !ok {
		t.Fatal("base dependents not found")
	}
	if !reflect.DeepEqual(direct, []string{"a-child", "z-child"}) {
		t.Fatalf("direct = %#v", direct)
	}
	if !reflect.DeepEqual(descendants, []string{"a-child", "grandchild", "z-child"}) {
		t.Fatalf("descendants = %#v", descendants)
	}
	if _, _, ok := cfg.ModelDependents("missing"); ok {
		t.Fatal("missing dependents returned ok")
	}
}

func modelNames(models []map[string]any) []string {
	out := make([]string, 0, len(models))
	for _, model := range models {
		out = append(out, model["model_name"].(string))
	}
	return out
}
