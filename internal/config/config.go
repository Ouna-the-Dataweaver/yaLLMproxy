package config

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

const (
	DefaultConfigPath       = "configs/config.yaml"
	MaxInheritanceDepth     = 10
	AdminPasswordEnv        = "YALLMP_ADMIN_PASSWORD"
	DefaultAppKeyHeader     = "x-api-key"
	DefaultServerHost       = "127.0.0.1"
	DefaultServerPort   int = 7979
)

type Config struct {
	ModelList             []ModelEntry       `yaml:"model_list" json:"model_list"`
	Providers             []ProviderEntry    `yaml:"providers" json:"providers"`
	Nodes                 []NodeEntry        `yaml:"nodes" json:"nodes"`
	Models                []ClientModelEntry `yaml:"models" json:"models"`
	RouterSettings        RouterSettings     `yaml:"router_settings" json:"router_settings"`
	ProxySettings         ProxySettings      `yaml:"proxy_settings" json:"proxy_settings"`
	GeneralSettings       ProxySettings      `yaml:"general_settings" json:"general_settings"`
	AppKeys               AppKeysConfig      `yaml:"app_keys" json:"app_keys"`
	ForwarderSettings     map[string]any     `yaml:"forwarder_settings" json:"forwarder_settings"`
	HTTPForwarderSettings map[string]any     `yaml:"http_forwarder_settings" json:"http_forwarder_settings"`
	Database              map[string]any     `yaml:"database" json:"database"`
	Raw                   map[string]any     `yaml:"-" json:"-"`
	Path                  string             `yaml:"-" json:"-"`
}

type ModelEntry struct {
	ModelName     string         `yaml:"model_name" json:"model_name"`
	Extends       string         `yaml:"extends,omitempty" json:"extends,omitempty"`
	Protected     any            `yaml:"protected,omitempty" json:"protected,omitempty"`
	Editable      any            `yaml:"editable,omitempty" json:"editable,omitempty"`
	ModelParams   map[string]any `yaml:"model_params" json:"model_params"`
	Parameters    map[string]any `yaml:"parameters,omitempty" json:"parameters,omitempty"`
	Modules       map[string]any `yaml:"modules,omitempty" json:"modules,omitempty"`
	AccessControl map[string]any `yaml:"access_control,omitempty" json:"access_control,omitempty"`
	Limits        LimitsConfig   `yaml:"limits,omitempty" json:"limits,omitempty"`
	Raw           map[string]any `yaml:",inline" json:"-"`
}

type AdminModelList struct {
	Protected   []map[string]any `json:"protected"`
	Unprotected []map[string]any `json:"unprotected"`
}

type ModelTree struct {
	Roots []string                 `json:"roots"`
	Nodes map[string]ModelTreeNode `json:"nodes"`
}

type ModelTreeNode struct {
	Config    map[string]any `json:"config"`
	Parent    string         `json:"parent"`
	Children  []string       `json:"children"`
	Protected bool           `json:"protected"`
	Editable  bool           `json:"editable"`
}

type ProviderEntry struct {
	Name                 string         `yaml:"name" json:"name"`
	APIBase              string         `yaml:"api_base" json:"api_base"`
	APIKey               string         `yaml:"api_key" json:"api_key"`
	APIType              string         `yaml:"api_type" json:"api_type"`
	AnthropicVersion     string         `yaml:"anthropic_version,omitempty" json:"anthropic_version,omitempty"`
	Mode                 string         `yaml:"mode,omitempty" json:"mode,omitempty"`
	Scope                string         `yaml:"scope,omitempty" json:"scope,omitempty"`
	AuthURL              string         `yaml:"auth_url,omitempty" json:"auth_url,omitempty"`
	ClientCert           string         `yaml:"client_cert,omitempty" json:"client_cert,omitempty"`
	ClientKey            string         `yaml:"client_key,omitempty" json:"client_key,omitempty"`
	RequestTimeout       any            `yaml:"request_timeout,omitempty" json:"request_timeout,omitempty"`
	VerifySSL            any            `yaml:"verify_ssl,omitempty" json:"verify_ssl,omitempty"`
	EmulateToolCalls     any            `yaml:"emulate_tool_calls,omitempty" json:"emulate_tool_calls,omitempty"`
	SupportsResponsesAPI any            `yaml:"supports_responses_api,omitempty" json:"supports_responses_api,omitempty"`
	Limits               LimitsConfig   `yaml:"limits" json:"limits"`
	Parameters           map[string]any `yaml:"parameters" json:"parameters"`
	Endpoints            []string       `yaml:"endpoints" json:"endpoints"`
	Raw                  map[string]any `yaml:",inline" json:"-"`
}

type NodeEntry struct {
	Name                 string         `yaml:"name" json:"name"`
	Provider             string         `yaml:"provider" json:"provider"`
	APIBase              string         `yaml:"api_base" json:"api_base"`
	APIKey               string         `yaml:"api_key" json:"api_key"`
	APIType              string         `yaml:"api_type" json:"api_type"`
	AnthropicVersion     string         `yaml:"anthropic_version" json:"anthropic_version"`
	Mode                 string         `yaml:"mode,omitempty" json:"mode,omitempty"`
	Scope                string         `yaml:"scope,omitempty" json:"scope,omitempty"`
	AuthURL              string         `yaml:"auth_url,omitempty" json:"auth_url,omitempty"`
	ClientCert           string         `yaml:"client_cert,omitempty" json:"client_cert,omitempty"`
	ClientKey            string         `yaml:"client_key,omitempty" json:"client_key,omitempty"`
	VerifySSL            any            `yaml:"verify_ssl,omitempty" json:"verify_ssl,omitempty"`
	EmulateToolCalls     any            `yaml:"emulate_tool_calls,omitempty" json:"emulate_tool_calls,omitempty"`
	Model                string         `yaml:"model" json:"model"`
	TargetModel          string         `yaml:"target_model" json:"target_model"`
	RequestTimeout       any            `yaml:"request_timeout" json:"request_timeout"`
	SupportsReasoning    any            `yaml:"supports_reasoning" json:"supports_reasoning"`
	SupportsResponsesAPI any            `yaml:"supports_responses_api" json:"supports_responses_api"`
	HTTP2                any            `yaml:"http2" json:"http2"`
	Weight               int            `yaml:"weight" json:"weight"`
	Limits               LimitsConfig   `yaml:"limits" json:"limits"`
	Parameters           map[string]any `yaml:"parameters" json:"parameters"`
	Modules              map[string]any `yaml:"modules" json:"modules"`
	Endpoints            []string       `yaml:"endpoints" json:"endpoints"`
	AllowedKeys          any            `yaml:"allowed_keys" json:"allowed_keys"`
	Raw                  map[string]any `yaml:",inline" json:"-"`
}

type ClientModelEntry struct {
	Name        string         `yaml:"name" json:"name"`
	ModelName   string         `yaml:"model_name" json:"model_name"`
	Nodes       []string       `yaml:"nodes" json:"nodes"`
	NodeWeights map[string]int `yaml:"node_weights" json:"node_weights"`
	Balancing   string         `yaml:"balancing" json:"balancing"`
	AllowedKeys any            `yaml:"allowed_keys" json:"allowed_keys"`
	Limits      LimitsConfig   `yaml:"limits" json:"limits"`
	Raw         map[string]any `yaml:",inline" json:"-"`
}

func (m ClientModelEntry) EffectiveName() string {
	if strings.TrimSpace(m.Name) != "" {
		return strings.TrimSpace(m.Name)
	}
	return strings.TrimSpace(m.ModelName)
}

type RouterSettings struct {
	NumRetries                any                               `yaml:"num_retries" json:"num_retries"`
	Fallbacks                 []map[string]any                  `yaml:"fallbacks" json:"fallbacks"`
	HealthChecks              HealthCheckSettings               `yaml:"health_checks" json:"health_checks"`
	HealthScoring             HealthCheckSettings               `yaml:"health_scoring" json:"health_scoring"`
	QuotaAwareSelection       QuotaAwareSelectionSettings       `yaml:"quota_aware_selection" json:"quota_aware_selection"`
	PrefixCacheAffinity       PrefixCacheAffinitySettings       `yaml:"prefix_cache_affinity" json:"prefix_cache_affinity"`
	ConcurrencyAwareSelection ConcurrencyAwareSelectionSettings `yaml:"concurrency_aware_selection" json:"concurrency_aware_selection"`
}

type HealthCheckSettings struct {
	Enabled                  bool    `yaml:"enabled" json:"enabled"`
	Path                     string  `yaml:"path" json:"path"`
	IntervalSeconds          float64 `yaml:"interval_seconds" json:"interval_seconds"`
	ProbeIntervalSeconds     float64 `yaml:"probe_interval_seconds" json:"probe_interval_seconds"`
	TimeoutSeconds           float64 `yaml:"timeout_seconds" json:"timeout_seconds"`
	FailureThreshold         int     `yaml:"failure_threshold" json:"failure_threshold"`
	UnhealthyThreshold       float64 `yaml:"unhealthy_threshold" json:"unhealthy_threshold"`
	UnhealthyCooldownSeconds float64 `yaml:"unhealthy_cooldown_seconds" json:"unhealthy_cooldown_seconds"`
	CooldownSeconds          float64 `yaml:"cooldown_seconds" json:"cooldown_seconds"`
	RecoverySuccessThreshold int     `yaml:"recovery_success_threshold" json:"recovery_success_threshold"`
}

type QuotaAwareSelectionSettings struct {
	Enabled             any `yaml:"enabled" json:"enabled"`
	PersistBackendQuota any `yaml:"persist_backend_quota" json:"persist_backend_quota"`
}

type PrefixCacheAffinitySettings struct {
	Enabled    any `yaml:"enabled" json:"enabled"`
	MaxEntries int `yaml:"max_entries" json:"max_entries"`
}

type ConcurrencyAwareSelectionSettings struct {
	Enabled any `yaml:"enabled" json:"enabled"`
}

type ProxySettings struct {
	Debug                   any                  `yaml:"debug" json:"debug"`
	Server                  ServerConfig         `yaml:"server" json:"server"`
	EnableResponsesEndpoint any                  `yaml:"enable_responses_endpoint" json:"enable_responses_endpoint"`
	EnableMessagesEndpoint  any                  `yaml:"enable_messages_endpoint" json:"enable_messages_endpoint"`
	InferenceCache          InferenceCacheConfig `yaml:"inference_cache" json:"inference_cache"`
	ModelCatalog            ModelCatalogConfig   `yaml:"model_catalog" json:"model_catalog"`
	Profiling               ProfilingConfig      `yaml:"profiling" json:"profiling"`
	Metrics                 MetricsConfig        `yaml:"metrics" json:"metrics"`
	OTel                    OTelConfig           `yaml:"otel" json:"otel"`
	Logging                 LoggingConfig        `yaml:"logging" json:"logging"`
	Modules                 map[string]any       `yaml:"modules" json:"modules"`
	Parsers                 map[string]any       `yaml:"parsers" json:"parsers"`
	Raw                     map[string]any       `yaml:",inline" json:"-"`
}

type InferenceCacheConfig struct {
	Mode          string                          `yaml:"mode" json:"mode"`
	TTLSeconds    float64                         `yaml:"ttl_seconds" json:"ttl_seconds"`
	MaxAgeSeconds float64                         `yaml:"max_age_seconds" json:"max_age_seconds"`
	MaxEntries    int                             `yaml:"max_entries" json:"max_entries"`
	MaxEntryBytes int                             `yaml:"max_entry_bytes" json:"max_entry_bytes"`
	Semantic      SemanticCacheConfig             `yaml:"semantic" json:"semantic"`
	Persistence   InferenceCachePersistenceConfig `yaml:"persistence" json:"persistence"`
}

type InferenceCachePersistenceConfig struct {
	Enabled   any    `yaml:"enabled" json:"enabled"`
	Path      string `yaml:"path" json:"path"`
	FlushMode string `yaml:"flush_mode" json:"flush_mode"`
}

type SemanticCacheConfig struct {
	Enabled             any     `yaml:"enabled" json:"enabled"`
	EmbeddingModel      string  `yaml:"embedding_model" json:"embedding_model"`
	EmbeddingNode       string  `yaml:"embedding_node" json:"embedding_node"`
	SimilarityThreshold float64 `yaml:"similarity_threshold" json:"similarity_threshold"`
	TopK                int     `yaml:"top_k" json:"top_k"`
	MaxEntries          int     `yaml:"max_entries" json:"max_entries"`
	MaxEntryBytes       int     `yaml:"max_entry_bytes" json:"max_entry_bytes"`
}

type ModelCatalogConfig struct {
	Enabled               bool     `yaml:"enabled" json:"enabled"`
	ProbeIntervalSeconds  float64  `yaml:"probe_interval_seconds" json:"probe_interval_seconds"`
	ProbeTimeoutSeconds   float64  `yaml:"probe_timeout_seconds" json:"probe_timeout_seconds"`
	FailureBackoffSeconds float64  `yaml:"failure_backoff_seconds" json:"failure_backoff_seconds"`
	CacheTTLSeconds       float64  `yaml:"cache_ttl_seconds" json:"cache_ttl_seconds"`
	ProbeProviders        []string `yaml:"probe_providers" json:"probe_providers"`
	ProbeNodes            []string `yaml:"probe_nodes" json:"probe_nodes"`
}

type ServerConfig struct {
	Host string `yaml:"host" json:"host"`
	Port int    `yaml:"port" json:"port"`
}

type ProfilingConfig struct {
	Enabled any `yaml:"enabled" json:"enabled"`
}

type MetricsConfig struct {
	Enabled any                  `yaml:"enabled" json:"enabled"`
	Timings MetricsTimingsConfig `yaml:"timings" json:"timings"`
}

type MetricsTimingsConfig struct {
	Enabled any `yaml:"enabled" json:"enabled"`
}

type OTelConfig struct {
	Enabled     any            `yaml:"enabled" json:"enabled"`
	ServiceName string         `yaml:"service_name" json:"service_name"`
	Exporter    string         `yaml:"exporter" json:"exporter"`
	Endpoint    string         `yaml:"endpoint" json:"endpoint"`
	Insecure    any            `yaml:"insecure" json:"insecure"`
	Headers     map[string]any `yaml:"headers" json:"headers"`
	Logs        OTelLogsConfig `yaml:"logs" json:"logs"`
}

type OTelLogsConfig struct {
	Enabled any `yaml:"enabled" json:"enabled"`
}

type LoggingConfig struct {
	LogParsedResponse any            `yaml:"log_parsed_response" json:"log_parsed_response"`
	LogParsedStream   any            `yaml:"log_parsed_stream" json:"log_parsed_stream"`
	LogToDisk         any            `yaml:"log_to_disk" json:"log_to_disk"`
	FullRequestStore  map[string]any `yaml:"full_request_storage" json:"full_request_storage"`
}

type AppKeysConfig struct {
	Enabled              bool               `yaml:"enabled" json:"enabled"`
	HeaderName           string             `yaml:"header_name" json:"header_name"`
	AllowUnauthenticated bool               `yaml:"allow_unauthenticated" json:"allow_unauthenticated"`
	RequiredHeaders      []RequiredHeader   `yaml:"required_headers,omitempty" json:"required_headers,omitempty"`
	Defaults             LimitsConfig       `yaml:"defaults" json:"defaults"`
	Unauthenticated      LimitsConfig       `yaml:"unauthenticated" json:"unauthenticated"`
	Groups               []AppKeyGroupEntry `yaml:"groups" json:"groups"`
	Keys                 []AppKeyEntry      `yaml:"keys" json:"keys"`
}

type AppKeyGroupEntry struct {
	GroupID         string           `yaml:"group_id" json:"group_id"`
	Name            string           `yaml:"name" json:"name"`
	Enabled         *bool            `yaml:"enabled" json:"enabled"`
	Models          []string         `yaml:"models" json:"models"`
	Limits          LimitsConfig     `yaml:"limits" json:"limits"`
	RequiredHeaders []RequiredHeader `yaml:"required_headers,omitempty" json:"required_headers,omitempty"`
}

type AppKeyEntry struct {
	KeyID            string           `yaml:"key_id" json:"key_id"`
	GroupID          string           `yaml:"group_id" json:"group_id"`
	Name             string           `yaml:"name" json:"name"`
	Description      string           `yaml:"description" json:"description"`
	Secret           string           `yaml:"secret" json:"secret"`
	Enabled          *bool            `yaml:"enabled" json:"enabled"`
	Models           []string         `yaml:"models" json:"models"`
	ConcurrencyLimit int              `yaml:"concurrency_limit" json:"concurrency_limit"`
	Priority         int              `yaml:"priority" json:"priority"`
	QueueTimeout     float64          `yaml:"queue_timeout" json:"queue_timeout"`
	Limits           LimitsConfig     `yaml:"limits" json:"limits"`
	RequiredHeaders  []RequiredHeader `yaml:"required_headers,omitempty" json:"required_headers,omitempty"`
}

func (cfg AppKeysConfig) HasData() bool {
	return cfg.Enabled ||
		cfg.HeaderName != "" ||
		cfg.AllowUnauthenticated ||
		len(cfg.RequiredHeaders) > 0 ||
		!cfg.Defaults.IsZero() ||
		!cfg.Unauthenticated.IsZero() ||
		len(cfg.Groups) > 0 ||
		len(cfg.Keys) > 0
}

func (key AppKeyEntry) EffectiveKeyID() string {
	if keyID := strings.TrimSpace(key.KeyID); keyID != "" {
		return keyID
	}
	_, keyID, ok := ParseLegacyAppKeySecret(key.Secret)
	if ok {
		return keyID
	}
	return ""
}

func (key AppKeyEntry) EffectiveGroupID() string {
	if groupID := strings.TrimSpace(key.GroupID); groupID != "" {
		return groupID
	}
	groupID, _, ok := ParseLegacyAppKeySecret(key.Secret)
	if ok {
		return groupID
	}
	return ""
}

func ParseLegacyAppKeySecret(secret string) (string, string, bool) {
	const prefix = "yallmp-"
	if !strings.HasPrefix(secret, prefix) {
		return "", "", false
	}
	groupID, keyID, ok := strings.Cut(strings.TrimPrefix(secret, prefix), "-")
	if !ok || groupID == "" || keyID == "" || strings.Contains(keyID, "-") {
		return "", "", false
	}
	return groupID, keyID, true
}

type LimitsConfig struct {
	Concurrency             int     `yaml:"concurrency" json:"concurrency"`
	RPS                     float64 `yaml:"rps" json:"rps"`
	RPM                     int     `yaml:"rpm" json:"rpm"`
	TokenQuota              int     `yaml:"token_quota" json:"token_quota"`
	TokenQuotaWindowSeconds float64 `yaml:"token_quota_window_seconds" json:"token_quota_window_seconds"`
	TokenQuotaResetMode     string  `yaml:"token_quota_reset_mode" json:"token_quota_reset_mode"`
	Priority                int     `yaml:"priority" json:"priority"`
	QueueTimeoutSeconds     float64 `yaml:"queue_timeout_seconds" json:"queue_timeout_seconds"`
}

type RequiredHeader struct {
	Name   string   `yaml:"name" json:"name"`
	LogAs  string   `yaml:"log_as,omitempty" json:"log_as,omitempty"`
	Models []string `yaml:"models,omitempty" json:"models,omitempty"`
}

func (l LimitsConfig) IsZero() bool {
	return l.Concurrency == 0 &&
		l.RPS == 0 &&
		l.RPM == 0 &&
		l.TokenQuota == 0 &&
		l.TokenQuotaWindowSeconds == 0 &&
		l.TokenQuotaResetMode == "" &&
		l.Priority == 0 &&
		l.QueueTimeoutSeconds == 0
}

func (l *LimitsConfig) UnmarshalYAML(value *yaml.Node) error {
	type limitsAlias LimitsConfig
	var alias limitsAlias
	if err := value.Decode(&alias); err != nil {
		return err
	}
	*l = LimitsConfig(alias)

	var legacy struct {
		ConcurrencyLimit int     `yaml:"concurrency_limit"`
		QueueTimeout     float64 `yaml:"queue_timeout"`
	}
	if err := value.Decode(&legacy); err != nil {
		return err
	}
	if l.Concurrency == 0 && legacy.ConcurrencyLimit != 0 {
		l.Concurrency = legacy.ConcurrencyLimit
	}
	if l.QueueTimeoutSeconds == 0 && legacy.QueueTimeout != 0 {
		l.QueueTimeoutSeconds = legacy.QueueTimeout
	}
	return nil
}

type ValidationIssue struct {
	Path     string `json:"path"`
	Severity string `json:"severity"`
	Code     string `json:"code"`
	Message  string `json:"message"`
}

var (
	ErrLegacyModelNotFound       = errors.New("model_not_found")
	ErrLegacyModelDuplicate      = errors.New("duplicate_model")
	ErrLegacyModelHasDependents  = errors.New("model_has_dependents")
	ErrProviderNotFound          = errors.New("provider_not_found")
	ErrProviderDuplicate         = errors.New("duplicate_provider")
	ErrProviderHasNodes          = errors.New("provider_has_nodes")
	ErrNodeNotFound              = errors.New("node_not_found")
	ErrNodeDuplicate             = errors.New("duplicate_node")
	ErrNodeHasClientModels       = errors.New("node_has_client_models")
	ErrClientModelNotFound       = errors.New("client_model_not_found")
	ErrClientModelDuplicate      = errors.New("duplicate_client_model")
	ErrInvalidFallbacks          = errors.New("invalid_fallbacks")
	ErrAdminResourceNameRequired = errors.New("name_required")
	ErrAdminResourceNameMismatch = errors.New("name_mismatch")
)

type LegacyModelDependentsError struct {
	Model       string
	Direct      []string
	Descendants []string
}

func (e *LegacyModelDependentsError) Error() string {
	return fmt.Sprintf("model %q has dependent models", e.Model)
}

func (e *LegacyModelDependentsError) Unwrap() error {
	return ErrLegacyModelHasDependents
}

type AdminResourceReferencesError struct {
	Resource   string
	References []string
	Err        error
}

func (e *AdminResourceReferencesError) Error() string {
	return fmt.Sprintf("%s is referenced by %s", e.Resource, strings.Join(e.References, ", "))
}

func (e *AdminResourceReferencesError) Unwrap() error {
	return e.Err
}

type LoadOptions struct {
	Path          string
	EnvPath       string
	SubstituteEnv bool
}

func Load(path string) (*Config, error) {
	return LoadWithOptions(LoadOptions{Path: path, SubstituteEnv: true})
}

func LoadWithOptions(opts LoadOptions) (*Config, error) {
	path := opts.Path
	if path == "" {
		if env := os.Getenv("YALLMP_CONFIG"); env != "" {
			path = env
		} else {
			path = DefaultConfigPath
		}
	}
	configPath := ResolvePath(path)
	rawBytes, err := os.ReadFile(configPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("config file not found: %s", configPath)
		}
		return nil, err
	}
	if len(strings.TrimSpace(string(rawBytes))) == 0 {
		return &Config{Raw: map[string]any{}}, nil
	}

	raw := map[string]any{}
	if err := yaml.Unmarshal(rawBytes, &raw); err != nil {
		return nil, err
	}
	persistRaw := cloneMap(raw)

	if opts.SubstituteEnv {
		envValues, _ := LoadEnvValues(ResolveEnvPath(configPath, opts.EnvPath))
		raw = SubstituteEnv(raw, envValues).(map[string]any)
	}

	normalizedBytes, err := yaml.Marshal(raw)
	if err != nil {
		return nil, err
	}
	var cfg Config
	if err := yaml.Unmarshal(normalizedBytes, &cfg); err != nil {
		return nil, err
	}
	cfg.Raw = persistRaw
	cfg.Path = configPath
	resolved, err := ResolveAllModelInheritance(cfg.ModelList)
	if err != nil {
		return nil, err
	}
	cfg.ModelList = resolved
	return &cfg, nil
}

func (cfg *Config) Save(path string) error {
	if cfg == nil {
		return fmt.Errorf("config is nil")
	}
	if path == "" {
		path = cfg.Path
	}
	if path == "" {
		return nil
	}
	raw := cloneMap(cfg.Raw)
	if raw == nil {
		raw = map[string]any{}
	}
	if _, ok := raw["model_list"]; !ok {
		raw["model_list"] = cfg.ModelList
	}
	if _, ok := raw["router_settings"]; !ok {
		raw["router_settings"] = cfg.RouterSettings
	}
	if _, ok := raw["proxy_settings"]; !ok {
		raw["proxy_settings"] = cfg.ProxySettings
	}
	if _, ok := raw["providers"]; !ok && len(cfg.Providers) > 0 {
		raw["providers"] = cfg.Providers
	}
	if _, ok := raw["nodes"]; !ok && len(cfg.Nodes) > 0 {
		raw["nodes"] = cfg.Nodes
	}
	if _, ok := raw["models"]; !ok && len(cfg.Models) > 0 {
		raw["models"] = cfg.Models
	}
	if _, ok := raw["app_keys"]; !ok && cfg.AppKeys.HasData() {
		raw["app_keys"] = cfg.AppKeys
	}
	if _, ok := raw["database"]; !ok && len(cfg.Database) > 0 {
		raw["database"] = cfg.Database
	}
	buf, err := yaml.Marshal(raw)
	if err != nil {
		return err
	}
	return os.WriteFile(path, buf, 0o600)
}

func FromMap(raw map[string]any, path string) (*Config, error) {
	if raw == nil {
		raw = map[string]any{}
	}
	normalizedBytes, err := yaml.Marshal(raw)
	if err != nil {
		return nil, err
	}
	var cfg Config
	if err := yaml.Unmarshal(normalizedBytes, &cfg); err != nil {
		return nil, err
	}
	cfg.Raw = cloneMap(raw)
	cfg.Path = path
	resolved, err := ResolveAllModelInheritance(cfg.ModelList)
	if err != nil {
		return nil, err
	}
	cfg.ModelList = resolved
	return &cfg, nil
}

func (cfg *Config) ToMap() map[string]any {
	if cfg == nil {
		return map[string]any{}
	}
	raw := cloneMap(cfg.Raw)
	if raw == nil {
		raw = map[string]any{}
	}
	raw["model_list"] = cfg.ModelList
	raw["router_settings"] = cfg.RouterSettings
	raw["proxy_settings"] = cfg.ProxySettings
	if len(cfg.GeneralSettings.Raw) > 0 {
		raw["general_settings"] = cfg.GeneralSettings
	}
	if cfg.AppKeys.HasData() {
		raw["app_keys"] = cfg.AppKeys
	} else if _, ok := raw["app_keys"]; ok {
		raw["app_keys"] = cfg.AppKeys
	}
	if len(cfg.Providers) > 0 {
		raw["providers"] = cfg.Providers
	}
	if len(cfg.Nodes) > 0 {
		raw["nodes"] = cfg.Nodes
	}
	if len(cfg.Models) > 0 {
		raw["models"] = cfg.Models
	}
	if len(cfg.ForwarderSettings) > 0 {
		raw["forwarder_settings"] = cfg.ForwarderSettings
	}
	if len(cfg.HTTPForwarderSettings) > 0 {
		raw["http_forwarder_settings"] = cfg.HTTPForwarderSettings
	}
	if len(cfg.Database) > 0 {
		raw["database"] = cfg.Database
	}
	return ToJSONMap(raw)
}

func (cfg *Config) Validate() []ValidationIssue {
	if cfg == nil {
		return []ValidationIssue{{Path: "$", Severity: "error", Code: "config_nil", Message: "Config is nil"}}
	}
	collector := validationCollector{}
	knownTargets := map[string]bool{}
	knownNodes := map[string]bool{}
	knownProviders := map[string]ProviderEntry{}

	if port := cfg.ProxySettings.Server.Port; port < 0 || port > 65535 {
		collector.add("proxy_settings.server.port", "error", "invalid_port", "Server port must be between 0 and 65535")
	}
	validateModelCatalog(&collector, "proxy_settings.model_catalog", cfg.ProxySettings.ModelCatalog)
	validateStrictBoolLike(&collector, "proxy_settings.profiling.enabled", cfg.ProxySettings.Profiling.Enabled)
	validateStrictBoolLike(&collector, "proxy_settings.metrics.enabled", cfg.ProxySettings.Metrics.Enabled)
	validateStrictBoolLike(&collector, "proxy_settings.metrics.timings.enabled", cfg.ProxySettings.Metrics.Timings.Enabled)
	validateOTel(&collector, "proxy_settings.otel", cfg.ProxySettings.OTel)
	validateStrictBoolLike(&collector, "proxy_settings.logging.log_to_disk", cfg.ProxySettings.Logging.LogToDisk)
	validateStrictBoolLike(&collector, "proxy_settings.logging.log_parsed_response", cfg.ProxySettings.Logging.LogParsedResponse)
	validateStrictBoolLike(&collector, "proxy_settings.logging.log_parsed_stream", cfg.ProxySettings.Logging.LogParsedStream)
	validateStrictBoolLike(&collector, "proxy_settings.logging.full_request_storage.enabled", cfg.ProxySettings.Logging.FullRequestStore["enabled"])
	validateHealthCheckSettings(&collector, "router_settings.health_checks", cfg.RouterSettings.HealthChecks)
	validateHealthCheckSettings(&collector, "router_settings.health_scoring", cfg.RouterSettings.HealthScoring)
	validateStrictBoolLike(&collector, "router_settings.quota_aware_selection.enabled", cfg.RouterSettings.QuotaAwareSelection.Enabled)
	validateStrictBoolLike(&collector, "router_settings.quota_aware_selection.persist_backend_quota", cfg.RouterSettings.QuotaAwareSelection.PersistBackendQuota)
	if IsStrictBoolLike(cfg.RouterSettings.QuotaAwareSelection.Enabled) &&
		IsStrictBoolLike(cfg.RouterSettings.QuotaAwareSelection.PersistBackendQuota) &&
		ParseBoolStrict(cfg.RouterSettings.QuotaAwareSelection.PersistBackendQuota) &&
		!ParseBoolStrict(cfg.RouterSettings.QuotaAwareSelection.Enabled) {
		collector.add(
			"router_settings.quota_aware_selection.persist_backend_quota",
			"error",
			"requires_quota_aware_selection_enabled",
			"persist_backend_quota requires quota_aware_selection.enabled",
		)
	}
	validateStrictBoolLike(&collector, "router_settings.prefix_cache_affinity.enabled", cfg.RouterSettings.PrefixCacheAffinity.Enabled)
	if cfg.RouterSettings.PrefixCacheAffinity.MaxEntries < 0 {
		collector.add("router_settings.prefix_cache_affinity.max_entries", "error", "invalid_limit", "max_entries must be non-negative")
	}
	validateStrictBoolLike(&collector, "router_settings.concurrency_aware_selection.enabled", cfg.RouterSettings.ConcurrencyAwareSelection.Enabled)

	legacyNames := map[string]int{}
	for i, model := range cfg.ModelList {
		path := fmt.Sprintf("model_list[%d]", i)
		name := strings.TrimSpace(model.ModelName)
		if name == "" {
			collector.add(path+".model_name", "error", "required", "Model name is required")
		} else {
			if previous, ok := legacyNames[name]; ok {
				collector.add(path+".model_name", "error", "duplicate_model", fmt.Sprintf("Model name duplicates model_list[%d]", previous))
			}
			legacyNames[name] = i
			knownTargets[name] = true
		}
		params := model.ModelParams
		if params == nil {
			params = map[string]any{}
		}
		apiType := strings.TrimSpace(fmt.Sprint(params["api_type"]))
		if apiType == "" || apiType == "<nil>" {
			apiType = "openai"
		}
		apiBase := strings.TrimSpace(fmt.Sprint(params["api_base"]))
		if apiBase == "<nil>" {
			apiBase = ""
		}
		if apiBase == "" {
			collector.add(path+".model_params.api_base", "error", "required", "API base is required")
		} else if !validHTTPURL(apiBase) {
			collector.add(path+".model_params.api_base", "error", "invalid_url", "API base must be an absolute http or https URL")
		}
		if apiType == "passthrough" && name != "" {
			knownTargets[name] = true
		}
		validateLimits(&collector, path+".limits", model.Limits)
	}

	providerNames := map[string]int{}
	for i, provider := range cfg.Providers {
		path := fmt.Sprintf("providers[%d]", i)
		name := strings.TrimSpace(provider.Name)
		if name == "" {
			collector.add(path+".name", "error", "required", "Provider name is required")
		} else {
			if previous, ok := providerNames[name]; ok {
				collector.add(path+".name", "error", "duplicate_provider", fmt.Sprintf("Provider name duplicates providers[%d]", previous))
			}
			providerNames[name] = i
			knownProviders[name] = provider
		}
		if provider.APIBase != "" && !validHTTPURL(provider.APIBase) {
			collector.add(path+".api_base", "error", "invalid_url", "Provider API base must be an absolute http or https URL")
		}
		validateEndpoints(&collector, path+".endpoints", provider.Endpoints)
		validateLimits(&collector, path+".limits", provider.Limits)
	}

	nodeNames := map[string]int{}
	for i, node := range cfg.Nodes {
		path := fmt.Sprintf("nodes[%d]", i)
		name := strings.TrimSpace(node.Name)
		if name == "" {
			collector.add(path+".name", "error", "required", "Node name is required")
		} else {
			if previous, ok := nodeNames[name]; ok {
				collector.add(path+".name", "error", "duplicate_node", fmt.Sprintf("Node name duplicates nodes[%d]", previous))
			}
			nodeNames[name] = i
			knownNodes[name] = true
		}
		provider, hasProvider := knownProviders[node.Provider]
		if strings.TrimSpace(node.Provider) != "" && !hasProvider {
			collector.add(path+".provider", "error", "unknown_provider", "Node references an unknown provider")
		}
		apiBase := strings.TrimSpace(firstString(node.APIBase, provider.APIBase))
		if apiBase == "" {
			collector.add(path+".api_base", "error", "required", "Node API base is required directly or through its provider")
		} else if !validHTTPURL(apiBase) {
			collector.add(path+".api_base", "error", "invalid_url", "Node API base must be an absolute http or https URL")
		}
		if node.Weight < 0 {
			collector.add(path+".weight", "error", "invalid_limit", "Node weight cannot be negative")
		}
		validateEndpoints(&collector, path+".endpoints", node.Endpoints)
		validateLimits(&collector, path+".limits", node.Limits)
		validateAllowedKeys(&collector, path+".allowed_keys", node.AllowedKeys)
	}
	validateInferenceCache(&collector, "proxy_settings.inference_cache", cfg.ProxySettings.InferenceCache, knownNodes)

	clientModelNames := map[string]int{}
	for i, model := range cfg.Models {
		path := fmt.Sprintf("models[%d]", i)
		name := model.EffectiveName()
		if name == "" {
			collector.add(path+".name", "error", "required", "Client model name is required")
		} else {
			if previous, ok := clientModelNames[name]; ok {
				collector.add(path+".name", "error", "duplicate_model", fmt.Sprintf("Client model name duplicates models[%d]", previous))
			}
			clientModelNames[name] = i
			knownTargets[name] = true
		}
		if len(model.Nodes) == 0 {
			collector.add(path+".nodes", "error", "required", "Client model must reference at least one node")
		}
		for nodeIndex, nodeName := range model.Nodes {
			if !knownNodes[nodeName] {
				collector.add(fmt.Sprintf("%s.nodes[%d]", path, nodeIndex), "error", "unknown_node", "Client model references an unknown node")
			}
		}
		seenWeights := map[string]bool{}
		for nodeName := range model.NodeWeights {
			seenWeights[nodeName] = true
			if !knownNodes[nodeName] {
				collector.add(path+".node_weights."+nodeName, "warning", "unknown_node_weight", "Node weight is set for a node that is not defined")
			}
			if model.NodeWeights[nodeName] < 0 {
				collector.add(path+".node_weights."+nodeName, "error", "invalid_limit", "Node weight cannot be negative")
			}
		}
		_ = seenWeights
		validateLimits(&collector, path+".limits", model.Limits)
		validateAllowedKeys(&collector, path+".allowed_keys", model.AllowedKeys)
	}

	for i, fallbackEntry := range cfg.RouterSettings.Fallbacks {
		for modelName, rawFallbacks := range fallbackEntry {
			if !knownTargets[modelName] && !knownNodes[modelName] {
				collector.add(fmt.Sprintf("router_settings.fallbacks[%d].%s", i, modelName), "warning", "unknown_fallback_source", "Fallback source does not match a defined model")
			}
			for fallbackIndex, fallbackName := range stringList(rawFallbacks) {
				if !knownTargets[fallbackName] && !knownNodes[fallbackName] {
					collector.add(fmt.Sprintf("router_settings.fallbacks[%d].%s[%d]", i, modelName, fallbackIndex), "warning", "unknown_fallback_target", "Fallback target does not match a defined model")
				}
			}
		}
	}
	validateModelCatalogReferences(&collector, "proxy_settings.model_catalog", cfg.ProxySettings.ModelCatalog, knownProviders, knownNodes, knownTargets)

	validateAppKeys(&collector, cfg.AppKeys)
	if len(knownTargets) == 0 && len(knownNodes) == 0 {
		collector.add("model_list", "error", "no_backends", "At least one model or node-backed model is required")
	}
	return collector.sorted()
}

func validateHealthCheckSettings(collector *validationCollector, path string, settings HealthCheckSettings) {
	if settings.RecoverySuccessThreshold < 0 {
		collector.add(path+".recovery_success_threshold", "error", "invalid_limit", "recovery_success_threshold cannot be negative")
	}
}

type validationCollector struct {
	issues []ValidationIssue
}

func (c *validationCollector) add(path, severity, code, message string) {
	c.issues = append(c.issues, ValidationIssue{Path: path, Severity: severity, Code: code, Message: message})
}

func (c validationCollector) sorted() []ValidationIssue {
	out := append([]ValidationIssue(nil), c.issues...)
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Severity != out[j].Severity {
			return out[i].Severity == "error"
		}
		if out[i].Path != out[j].Path {
			return out[i].Path < out[j].Path
		}
		return out[i].Code < out[j].Code
	})
	return out
}

func validateLimits(collector *validationCollector, path string, limits LimitsConfig) {
	checkIntLimit := func(field string, value int) {
		if value < 0 {
			collector.add(path+"."+field, "error", "invalid_limit", field+" cannot be negative")
		}
	}
	checkFloatLimit := func(field string, value float64) {
		if value < 0 {
			collector.add(path+"."+field, "error", "invalid_limit", field+" cannot be negative")
		}
	}
	checkIntLimit("concurrency", limits.Concurrency)
	checkFloatLimit("rps", limits.RPS)
	checkIntLimit("rpm", limits.RPM)
	checkIntLimit("token_quota", limits.TokenQuota)
	checkFloatLimit("token_quota_window_seconds", limits.TokenQuotaWindowSeconds)
	checkFloatLimit("queue_timeout_seconds", limits.QueueTimeoutSeconds)
	resetMode := strings.ToLower(strings.TrimSpace(limits.TokenQuotaResetMode))
	switch resetMode {
	case "", "rolling", "daily", "weekly", "monthly", "yearly":
	default:
		collector.add(path+".token_quota_reset_mode", "error", "invalid_quota_reset_mode", "token_quota_reset_mode must be rolling, daily, weekly, monthly, or yearly")
	}
	if resetMode != "" && resetMode != "rolling" && limits.TokenQuotaWindowSeconds > 0 {
		collector.add(path+".token_quota_window_seconds", "error", "invalid_quota_reset_window", "token_quota_window_seconds cannot be combined with a calendar token_quota_reset_mode")
	}
}

func validateAllowedKeys(collector *validationCollector, path string, value any) {
	switch typed := value.(type) {
	case nil:
		return
	case string:
		return
	case []string:
		for i, item := range typed {
			if strings.TrimSpace(item) == "" {
				collector.add(fmt.Sprintf("%s[%d]", path, i), "error", "invalid_allowed_keys", "allowed_keys entries cannot be empty")
			}
		}
	case []any:
		for i, item := range typed {
			text, ok := item.(string)
			if !ok {
				collector.add(fmt.Sprintf("%s[%d]", path, i), "error", "invalid_allowed_keys", "allowed_keys entries must be strings")
				continue
			}
			if strings.TrimSpace(text) == "" {
				collector.add(fmt.Sprintf("%s[%d]", path, i), "error", "invalid_allowed_keys", "allowed_keys entries cannot be empty")
			}
		}
	default:
		collector.add(path, "error", "invalid_allowed_keys", "allowed_keys must be a string or an array of strings")
	}
}

const (
	maxSemanticCacheTopK          = 100
	maxSemanticCacheEntries       = 100000
	maxSemanticCacheMaxEntryBytes = 64 << 20
)

func validateInferenceCache(collector *validationCollector, path string, cache InferenceCacheConfig, knownNodes map[string]bool) {
	mode := strings.ToLower(strings.TrimSpace(cache.Mode))
	switch mode {
	case "", "off", "disabled", "read_write", "read-only", "read_only", "write-only", "write_only":
	default:
		collector.add(path+".mode", "error", "invalid_cache_mode", "Inference cache mode must be off, read_write, read_only, or write_only")
	}
	if cache.TTLSeconds < 0 {
		collector.add(path+".ttl_seconds", "error", "invalid_limit", "ttl_seconds cannot be negative")
	}
	if cache.MaxAgeSeconds < 0 {
		collector.add(path+".max_age_seconds", "error", "invalid_limit", "max_age_seconds cannot be negative")
	}
	if cache.MaxEntries < 0 {
		collector.add(path+".max_entries", "error", "invalid_limit", "max_entries cannot be negative")
	}
	if cache.MaxEntryBytes < 0 {
		collector.add(path+".max_entry_bytes", "error", "invalid_limit", "max_entry_bytes cannot be negative")
	}
	validateSemanticCache(collector, path+".semantic", cache.Semantic, mode, knownNodes)
	validateInferenceCachePersistence(collector, path+".persistence", cache.Persistence, mode)
}

func ValidateInferenceCacheConfig(cache InferenceCacheConfig) []ValidationIssue {
	collector := validationCollector{}
	validateInferenceCache(&collector, "proxy_settings.inference_cache", cache, nil)
	return collector.sorted()
}

func validateSemanticCache(collector *validationCollector, path string, semantic SemanticCacheConfig, cacheMode string, knownNodes map[string]bool) {
	validateStrictBoolLike(collector, path+".enabled", semantic.Enabled)
	if !ParseBoolStrict(semantic.Enabled) {
		return
	}
	switch cacheMode {
	case "read_write", "read-only", "read_only", "write-only", "write_only":
	default:
		collector.add(path+".enabled", "error", "invalid_semantic_cache", "Semantic cache requires inference_cache.mode to be read_write, read_only, or write_only")
	}
	embeddingNode := strings.TrimSpace(semantic.EmbeddingNode)
	if embeddingNode == "" {
		collector.add(path+".embedding_node", "error", "required", "embedding_node is required when semantic cache is enabled")
	} else if knownNodes != nil && !knownNodes[embeddingNode] {
		collector.add(path+".embedding_node", "error", "unknown_node", "Semantic cache embedding_node must reference a defined node")
	}
	if strings.TrimSpace(semantic.EmbeddingModel) == "" {
		collector.add(path+".embedding_model", "error", "required", "embedding_model is required when semantic cache is enabled")
	}
	if semantic.SimilarityThreshold < 0 || semantic.SimilarityThreshold > 1 {
		collector.add(path+".similarity_threshold", "error", "invalid_limit", "similarity_threshold must be between 0 and 1")
	}
	if semantic.TopK < 0 || semantic.TopK > maxSemanticCacheTopK {
		collector.add(path+".top_k", "error", "invalid_limit", fmt.Sprintf("top_k must be between 0 and %d", maxSemanticCacheTopK))
	}
	if semantic.MaxEntries < 0 || semantic.MaxEntries > maxSemanticCacheEntries {
		collector.add(path+".max_entries", "error", "invalid_limit", fmt.Sprintf("max_entries must be between 0 and %d", maxSemanticCacheEntries))
	}
	if semantic.MaxEntryBytes < 0 || semantic.MaxEntryBytes > maxSemanticCacheMaxEntryBytes {
		collector.add(path+".max_entry_bytes", "error", "invalid_limit", fmt.Sprintf("max_entry_bytes must be between 0 and %d", maxSemanticCacheMaxEntryBytes))
	}
}

func validateInferenceCachePersistence(collector *validationCollector, path string, persistence InferenceCachePersistenceConfig, cacheMode string) {
	validateStrictBoolLike(collector, path+".enabled", persistence.Enabled)
	flushMode := strings.ToLower(strings.TrimSpace(persistence.FlushMode))
	switch flushMode {
	case "", "writethrough", "write-through", "write_through", "off", "read_write", "read-only", "read_only", "write-only", "write_only":
	default:
		collector.add(path+".flush_mode", "error", "invalid_cache_persistence_mode", "flush_mode must be writethrough, off, read_write, read_only, or write_only")
	}
	if !ParseBoolStrict(persistence.Enabled) {
		return
	}
	if cacheMode == "" || cacheMode == "off" || cacheMode == "disabled" {
		collector.add(path+".enabled", "error", "invalid_cache_persistence", "Inference cache persistence requires inference_cache.mode to be read_write, read_only, or write_only")
	}
	if strings.TrimSpace(persistence.Path) == "" {
		collector.add(path+".path", "error", "required", "path is required when inference cache persistence is enabled")
	}
	if strings.TrimSpace(persistence.Path) == ":memory:" {
		collector.add(path+".path", "error", "invalid_path", "inference cache persistence requires a file path")
	}
}

func validateModelCatalog(collector *validationCollector, path string, catalog ModelCatalogConfig) {
	if catalog.ProbeIntervalSeconds < 0 {
		collector.add(path+".probe_interval_seconds", "error", "invalid_limit", "probe_interval_seconds cannot be negative")
	}
	if catalog.ProbeTimeoutSeconds < 0 {
		collector.add(path+".probe_timeout_seconds", "error", "invalid_limit", "probe_timeout_seconds cannot be negative")
	}
	if catalog.FailureBackoffSeconds < 0 {
		collector.add(path+".failure_backoff_seconds", "error", "invalid_limit", "failure_backoff_seconds cannot be negative")
	}
	if catalog.CacheTTLSeconds < 0 {
		collector.add(path+".cache_ttl_seconds", "error", "invalid_limit", "cache_ttl_seconds cannot be negative")
	}
	validateNonEmptyStringList(collector, path+".probe_providers", catalog.ProbeProviders)
	validateNonEmptyStringList(collector, path+".probe_nodes", catalog.ProbeNodes)
}

func validateModelCatalogReferences(collector *validationCollector, path string, catalog ModelCatalogConfig, providers map[string]ProviderEntry, nodes map[string]bool, targets map[string]bool) {
	seenProviders := map[string]bool{}
	for i, provider := range catalog.ProbeProviders {
		name := strings.TrimSpace(provider)
		if name != "" {
			if seenProviders[name] {
				continue
			}
			seenProviders[name] = true
			if _, ok := providers[name]; !ok {
				collector.add(fmt.Sprintf("%s.probe_providers[%d]", path, i), "warning", "unknown_provider", "probe provider does not match a defined provider")
			}
		}
	}
	seenNodes := map[string]bool{}
	for i, node := range catalog.ProbeNodes {
		name := strings.TrimSpace(node)
		if seenNodes[name] {
			continue
		}
		seenNodes[name] = true
		if name != "" && !nodes[name] && !targets[name] {
			collector.add(fmt.Sprintf("%s.probe_nodes[%d]", path, i), "warning", "unknown_node", "probe node does not match a defined node or legacy model")
		}
	}
}

func validateNonEmptyStringList(collector *validationCollector, path string, values []string) {
	seen := map[string]int{}
	for i, value := range values {
		trimmed := strings.TrimSpace(value)
		itemPath := fmt.Sprintf("%s[%d]", path, i)
		if trimmed == "" {
			collector.add(itemPath, "error", "required", "entry cannot be empty")
			continue
		}
		if previous, ok := seen[trimmed]; ok {
			collector.add(itemPath, "warning", "duplicate_entry", fmt.Sprintf("entry duplicates %s[%d]", path, previous))
		}
		seen[trimmed] = i
	}
}

func validateOTel(collector *validationCollector, path string, otel OTelConfig) {
	validateStrictBoolLike(collector, path+".enabled", otel.Enabled)
	validateStrictBoolLike(collector, path+".insecure", otel.Insecure)
	validateStrictBoolLike(collector, path+".logs.enabled", otel.Logs.Enabled)
	exporter := normalizeOTelExporter(otel.Exporter)
	if exporter != "" && exporter != "otlp_grpc" {
		collector.add(path+".exporter", "error", "invalid_exporter", "OTel exporter must be otlp_grpc")
	}
	configEnabled := ParseBoolStrict(otel.Enabled)
	if ParseBoolStrict(otel.Logs.Enabled) && !configEnabled {
		collector.add(path+".logs.enabled", "error", "requires_otel_enabled", "OTel logs require proxy_settings.otel.enabled")
	}
	if configEnabled {
		endpoint := strings.TrimSpace(otel.Endpoint)
		if endpoint == "" {
			collector.add(path+".endpoint", "error", "required", "OTel endpoint is required when tracing is enabled")
		} else if !validOTELEndpoint(endpoint) {
			collector.add(path+".endpoint", "error", "invalid_endpoint", "OTel endpoint must be host:port or an absolute http or https URL")
		}
	}
	if serviceName := strings.TrimSpace(otel.ServiceName); serviceName != "" && len(serviceName) > 256 {
		collector.add(path+".service_name", "error", "invalid_service_name", "OTel service_name cannot exceed 256 characters")
	}
	if len(otel.Headers) > 32 {
		collector.add(path+".headers", "error", "invalid_headers", "OTel headers cannot contain more than 32 entries")
	}
	for name, value := range otel.Headers {
		headerPath := path + ".headers." + name
		trimmed := strings.TrimSpace(name)
		if trimmed == "" || trimmed != name || !validHeaderName(name) {
			collector.add(headerPath, "error", "invalid_header", "OTel header name must be a valid HTTP header field name without surrounding whitespace")
		}
		if _, ok := value.(string); !ok {
			collector.add(headerPath, "error", "invalid_header", "OTel header values must be strings")
		}
	}
}

func normalizeOTelExporter(value string) string {
	normalized := strings.ToLower(strings.TrimSpace(value))
	switch normalized {
	case "", "otlp", "otlp_grpc", "otlp-grpc", "grpc":
		if normalized == "" {
			return ""
		}
		return "otlp_grpc"
	default:
		return normalized
	}
}

func validOTELEndpoint(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" {
		return false
	}
	if strings.Contains(value, "://") {
		parsed, err := url.Parse(value)
		if err != nil || parsed.Host == "" {
			return false
		}
		return parsed.Scheme == "http" || parsed.Scheme == "https"
	}
	host, port, err := net.SplitHostPort(value)
	return err == nil && strings.TrimSpace(host) != "" && strings.TrimSpace(port) != ""
}

func validateRequiredHeaders(collector *validationCollector, path string, headers []RequiredHeader, appKeyHeader string) {
	if len(headers) > 32 {
		collector.add(path, "error", "invalid_required_headers", "required_headers cannot contain more than 32 entries")
	}
	seen := map[string]int{}
	for i, header := range headers {
		itemPath := fmt.Sprintf("%s[%d]", path, i)
		name := strings.TrimSpace(header.Name)
		if name == "" {
			collector.add(itemPath+".name", "error", "required", "Required header name is required")
			continue
		}
		normalizedName := strings.ToLower(name)
		if name != header.Name {
			collector.add(itemPath+".name", "error", "invalid_header", "Required header name cannot contain leading or trailing whitespace")
		}
		if !validHeaderName(name) {
			collector.add(itemPath+".name", "error", "invalid_header", "Required header name must be a valid HTTP header field name")
		}
		if normalizedName == "authorization" || strings.EqualFold(name, appKeyHeader) {
			collector.add(itemPath+".name", "error", "invalid_header", "Required header cannot be the authorization or app-key header")
		}
		if previous, ok := seen[normalizedName]; ok {
			collector.add(itemPath+".name", "error", "duplicate_header", fmt.Sprintf("Required header duplicates %s[%d]", path, previous))
		}
		seen[normalizedName] = i
		if logAs := strings.TrimSpace(header.LogAs); logAs != "" && !validHeaderAttributionName(logAs) {
			collector.add(itemPath+".log_as", "error", "invalid_log_as", "log_as must contain only letters, numbers, underscores, dashes, or dots")
		}
		for j, model := range header.Models {
			if strings.TrimSpace(model) == "" {
				collector.add(fmt.Sprintf("%s.models[%d]", itemPath, j), "error", "required", "Required header model cannot be empty")
			}
		}
	}
}

func validHeaderName(name string) bool {
	if name == "" {
		return false
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' {
			continue
		}
		switch c {
		case '!', '#', '$', '%', '&', '\'', '*', '+', '-', '.', '^', '_', '`', '|', '~':
			continue
		default:
			return false
		}
	}
	return true
}

func validHeaderAttributionName(name string) bool {
	if name == "" || len(name) > 64 {
		return false
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-' || c == '.' {
			continue
		}
		return false
	}
	return true
}

func validateEndpoints(collector *validationCollector, path string, endpoints []string) {
	for i, endpoint := range endpoints {
		if !validEndpoint(endpoint) {
			collector.add(fmt.Sprintf("%s[%d]", path, i), "error", "invalid_endpoint", "Endpoint must be one of chat, embeddings, rerank, messages, responses, all, or a matching /v1 path")
		}
	}
}

func validateBoolLike(collector *validationCollector, path string, value any) {
	if !IsBoolLike(value) {
		collector.add(path, "error", "invalid_bool", "Value must be a boolean or boolean-like string")
	}
}

func validateStrictBoolLike(collector *validationCollector, path string, value any) {
	if !IsStrictBoolLike(value) {
		collector.add(path, "error", "invalid_bool", "Value must be a boolean, 0/1, or boolean-like string")
	}
}

func validEndpoint(value string) bool {
	normalized := normalizeEndpointName(value)
	if normalized == "" {
		return true
	}
	switch normalized {
	case "chat", "embeddings", "rerank", "messages", "responses":
		return true
	default:
		return false
	}
}

func normalizeEndpointName(value string) string {
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

func validateAppKeys(collector *validationCollector, appKeys AppKeysConfig) {
	appKeyHeader := strings.TrimSpace(appKeys.HeaderName)
	if appKeyHeader == "" {
		appKeyHeader = DefaultAppKeyHeader
	}
	validateLimits(collector, "app_keys.defaults", appKeys.Defaults)
	validateLimits(collector, "app_keys.unauthenticated", appKeys.Unauthenticated)
	validateRequiredHeaders(collector, "app_keys.required_headers", appKeys.RequiredHeaders, appKeyHeader)
	groupIDs := map[string]int{}
	for i, group := range appKeys.Groups {
		path := fmt.Sprintf("app_keys.groups[%d]", i)
		groupID := strings.TrimSpace(group.GroupID)
		if groupID == "" {
			collector.add(path+".group_id", "error", "required", "App key group ID is required")
		} else {
			if previous, ok := groupIDs[groupID]; ok {
				collector.add(path+".group_id", "error", "duplicate_group", fmt.Sprintf("Group ID duplicates app_keys.groups[%d]", previous))
			}
			groupIDs[groupID] = i
		}
		validateLimits(collector, path+".limits", group.Limits)
		validateRequiredHeaders(collector, path+".required_headers", group.RequiredHeaders, appKeyHeader)
	}
	keyIDs := map[string]int{}
	secrets := map[string]int{}
	for i, key := range appKeys.Keys {
		path := fmt.Sprintf("app_keys.keys[%d]", i)
		keyID := strings.TrimSpace(key.KeyID)
		if keyID != "" {
			if previous, ok := keyIDs[keyID]; ok {
				collector.add(path+".key_id", "error", "duplicate_key", fmt.Sprintf("Key ID duplicates app_keys.keys[%d]", previous))
			}
			keyIDs[keyID] = i
		}
		secret := strings.TrimSpace(key.Secret)
		if appKeys.Enabled && keyID == "" && secret == "" {
			collector.add(path+".secret", "error", "required", "Enabled app keys need either key_id or secret")
		}
		if secret != "" {
			if previous, ok := secrets[secret]; ok {
				collector.add(path+".secret", "warning", "duplicate_secret", fmt.Sprintf("Secret duplicates app_keys.keys[%d]", previous))
			}
			secrets[secret] = i
		}
		if key.GroupID != "" {
			if _, ok := groupIDs[key.GroupID]; !ok {
				collector.add(path+".group_id", "error", "unknown_group", "App key references an unknown group")
			}
		}
		if key.ConcurrencyLimit < 0 {
			collector.add(path+".concurrency_limit", "error", "invalid_limit", "concurrency_limit cannot be negative")
		}
		if key.Priority < 0 {
			collector.add(path+".priority", "error", "invalid_limit", "priority cannot be negative")
		}
		if key.QueueTimeout < 0 {
			collector.add(path+".queue_timeout", "error", "invalid_limit", "queue_timeout cannot be negative")
		}
		validateLimits(collector, path+".limits", key.Limits)
		validateRequiredHeaders(collector, path+".required_headers", key.RequiredHeaders, appKeyHeader)
	}
	if appKeys.Enabled && !appKeys.AllowUnauthenticated && len(appKeys.Keys) == 0 {
		collector.add("app_keys.keys", "warning", "no_app_keys", "App keys are enabled but no client keys are configured")
	}
}

func validHTTPURL(value string) bool {
	parsed, err := url.Parse(strings.TrimSpace(value))
	if err != nil || parsed == nil || parsed.Host == "" {
		return false
	}
	scheme := strings.ToLower(parsed.Scheme)
	return scheme == "http" || scheme == "https"
}

func firstString(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func stringList(value any) []string {
	switch typed := value.(type) {
	case []string:
		out := make([]string, 0, len(typed))
		for _, item := range typed {
			if text := strings.TrimSpace(item); text != "" {
				out = append(out, text)
			}
		}
		return out
	case []any:
		out := make([]string, 0, len(typed))
		for _, item := range typed {
			if text := strings.TrimSpace(fmt.Sprint(item)); text != "" && text != "<nil>" {
				out = append(out, text)
			}
		}
		return out
	default:
		if text := strings.TrimSpace(fmt.Sprint(value)); text != "" && text != "<nil>" {
			return []string{text}
		}
		return nil
	}
}

func MaskSensitive(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(typed))
		for key, item := range typed {
			if isSensitiveKey(key) {
				continue
			}
			out[key] = MaskSensitive(item)
		}
		return out
	case []any:
		out := make([]any, len(typed))
		for i, item := range typed {
			out[i] = MaskSensitive(item)
		}
		return out
	case []map[string]any:
		out := make([]any, len(typed))
		for i, item := range typed {
			out[i] = MaskSensitive(item)
		}
		return out
	default:
		return typed
	}
}

func PreserveMissingSensitiveFields(incoming, existing any) any {
	switch typed := incoming.(type) {
	case map[string]any:
		out := cloneMap(typed)
		existingMap, ok := toStringAnyMap(existing)
		if !ok {
			return out
		}
		for key, existingValue := range existingMap {
			incomingValue, present := out[key]
			if isSensitiveKey(key) {
				if !present {
					out[key] = cloneAny(existingValue)
				}
				continue
			}
			if present {
				out[key] = PreserveMissingSensitiveFields(incomingValue, existingValue)
			}
		}
		return out
	case []any:
		out := make([]any, len(typed))
		existingSlice, _ := toAnySlice(existing)
		for i, item := range typed {
			existingItem := matchingExistingItem(item, existingSlice, i)
			out[i] = PreserveMissingSensitiveFields(item, existingItem)
		}
		return out
	case []map[string]any:
		out := make([]any, len(typed))
		existingSlice, _ := toAnySlice(existing)
		for i, item := range typed {
			existingItem := matchingExistingItem(item, existingSlice, i)
			out[i] = PreserveMissingSensitiveFields(item, existingItem)
		}
		return out
	default:
		return typed
	}
}

func isSensitiveKey(key string) bool {
	normalized := strings.ToLower(strings.ReplaceAll(key, "-", "_"))
	switch {
	case normalized == "api_key":
		return true
	case normalized == "dsn":
		return true
	case normalized == "x_api_key":
		return true
	case normalized == "authorization":
		return true
	case normalized == "secret":
		return true
	case strings.HasSuffix(normalized, "_secret"):
		return true
	case normalized == "client_key":
		return true
	case normalized == "client_cert":
		return true
	case normalized == "private_key":
		return true
	case normalized == "token":
		return true
	case strings.HasSuffix(normalized, "_token"):
		return true
	case strings.Contains(normalized, "password"):
		return true
	default:
		return false
	}
}

func (cfg *Config) AdminProviders() []map[string]any {
	return maskedRawNamedEntries(rawListForUpdate(rawConfigValue(cfg, "providers"), providerEntriesAsRaw(rawProviders(cfg))), "name")
}

func (cfg *Config) AdminNodes() []map[string]any {
	return maskedRawNamedEntries(rawListForUpdate(rawConfigValue(cfg, "nodes"), nodeEntriesAsRaw(rawNodes(cfg))), "name")
}

func (cfg *Config) AdminClientModels() []map[string]any {
	return maskedRawNamedEntries(rawListForUpdate(rawConfigValue(cfg, "models"), clientModelEntriesAsRaw(rawClientModels(cfg))), "name", "model_name")
}

func (cfg *Config) RawProvider(name string) (map[string]any, bool) {
	return rawNamedEntryByName(rawListForUpdate(rawConfigValue(cfg, "providers"), providerEntriesAsRaw(rawProviders(cfg))), name, "name")
}

func (cfg *Config) RawNode(name string) (map[string]any, bool) {
	return rawNamedEntryByName(rawListForUpdate(rawConfigValue(cfg, "nodes"), nodeEntriesAsRaw(rawNodes(cfg))), name, "name")
}

func (cfg *Config) RawClientModel(name string) (map[string]any, bool) {
	return rawNamedEntryByName(rawListForUpdate(rawConfigValue(cfg, "models"), clientModelEntriesAsRaw(rawClientModels(cfg))), name, "name", "model_name")
}

func (cfg *Config) RawRouterFallbacks() []map[string]any {
	if cfg == nil {
		return []map[string]any{}
	}
	if rawRouter, ok := toStringAnyMap(rawConfigValue(cfg, "router_settings")); ok {
		if fallbacks, ok := rawFallbackEntriesForRead(rawRouter["fallbacks"]); ok {
			return fallbacks
		}
	}
	return cloneFallbackEntries(cfg.RouterSettings.Fallbacks)
}

func (cfg *Config) ReplaceRouterFallbacks(payload any) ([]map[string]any, error) {
	if cfg == nil {
		return nil, invalidRouterFallbacksError("$", "config is nil")
	}
	normalized, err := cfg.normalizeRouterFallbacks(payload)
	if err != nil {
		return nil, err
	}
	raw := cloneMap(cfg.Raw)
	rawRouter := map[string]any{}
	if existing, ok := toStringAnyMap(raw["router_settings"]); ok {
		rawRouter = cloneMap(existing)
	}
	rawRouter["fallbacks"] = normalized
	raw["router_settings"] = rawRouter

	refreshed, err := FromMap(raw, cfg.Path)
	if err != nil {
		return nil, err
	}
	*cfg = *refreshed
	return cfg.RawRouterFallbacks(), nil
}

func (cfg *Config) CreateRawProvider(payload map[string]any) (map[string]any, error) {
	return cfg.upsertRawNamedEntry("providers", providerEntriesAsRaw(rawProviders(cfg)), []string{"name"}, "", payload, true, ErrProviderNotFound, ErrProviderDuplicate)
}

func (cfg *Config) UpdateRawProvider(name string, payload map[string]any) (map[string]any, error) {
	return cfg.upsertRawNamedEntry("providers", providerEntriesAsRaw(rawProviders(cfg)), []string{"name"}, name, payload, false, ErrProviderNotFound, ErrProviderDuplicate)
}

func (cfg *Config) DeleteRawProvider(name string) error {
	name = strings.TrimSpace(name)
	if cfg == nil || name == "" {
		return ErrProviderNotFound
	}
	if refs := cfg.ProviderNodeReferences(name); len(refs) > 0 {
		return &AdminResourceReferencesError{Resource: name, References: refs, Err: ErrProviderHasNodes}
	}
	if err := cfg.deleteRawNamedEntry("providers", providerEntriesAsRaw(rawProviders(cfg)), []string{"name"}, name, ErrProviderNotFound); err != nil {
		return err
	}
	return cfg.reloadFromRaw()
}

func (cfg *Config) CreateRawNode(payload map[string]any) (map[string]any, error) {
	return cfg.upsertRawNamedEntry("nodes", nodeEntriesAsRaw(rawNodes(cfg)), []string{"name"}, "", payload, true, ErrNodeNotFound, ErrNodeDuplicate)
}

func (cfg *Config) UpdateRawNode(name string, payload map[string]any) (map[string]any, error) {
	return cfg.upsertRawNamedEntry("nodes", nodeEntriesAsRaw(rawNodes(cfg)), []string{"name"}, name, payload, false, ErrNodeNotFound, ErrNodeDuplicate)
}

func (cfg *Config) DeleteRawNode(name string) error {
	name = strings.TrimSpace(name)
	if cfg == nil || name == "" {
		return ErrNodeNotFound
	}
	if refs := cfg.NodeClientModelReferences(name); len(refs) > 0 {
		return &AdminResourceReferencesError{Resource: name, References: refs, Err: ErrNodeHasClientModels}
	}
	if err := cfg.deleteRawNamedEntry("nodes", nodeEntriesAsRaw(rawNodes(cfg)), []string{"name"}, name, ErrNodeNotFound); err != nil {
		return err
	}
	return cfg.reloadFromRaw()
}

func (cfg *Config) CreateRawClientModel(payload map[string]any) (map[string]any, error) {
	return cfg.upsertRawNamedEntry("models", clientModelEntriesAsRaw(rawClientModels(cfg)), []string{"name", "model_name"}, "", payload, true, ErrClientModelNotFound, ErrClientModelDuplicate)
}

func (cfg *Config) UpdateRawClientModel(name string, payload map[string]any) (map[string]any, error) {
	return cfg.upsertRawNamedEntry("models", clientModelEntriesAsRaw(rawClientModels(cfg)), []string{"name", "model_name"}, name, payload, false, ErrClientModelNotFound, ErrClientModelDuplicate)
}

func (cfg *Config) DeleteRawClientModel(name string) error {
	name = strings.TrimSpace(name)
	if cfg == nil || name == "" {
		return ErrClientModelNotFound
	}
	if err := cfg.deleteRawNamedEntry("models", clientModelEntriesAsRaw(rawClientModels(cfg)), []string{"name", "model_name"}, name, ErrClientModelNotFound); err != nil {
		return err
	}
	removeFallback(&cfg.RouterSettings, []string{name})
	cfg.setRawRouterFallbacks()
	return cfg.reloadFromRaw()
}

func (cfg *Config) ProviderNodeReferences(provider string) []string {
	provider = strings.TrimSpace(provider)
	if cfg == nil || provider == "" {
		return nil
	}
	refs := []string{}
	for _, node := range cfg.Nodes {
		if strings.TrimSpace(node.Provider) != provider {
			continue
		}
		if name := strings.TrimSpace(node.Name); name != "" {
			refs = append(refs, name)
		}
	}
	sort.Strings(refs)
	return refs
}

func (cfg *Config) NodeClientModelReferences(node string) []string {
	node = strings.TrimSpace(node)
	if cfg == nil || node == "" {
		return nil
	}
	seen := map[string]bool{}
	for _, model := range cfg.Models {
		for _, modelNode := range model.Nodes {
			if strings.TrimSpace(modelNode) != node {
				continue
			}
			if name := model.EffectiveName(); name != "" {
				seen[name] = true
			}
		}
	}
	refs := make([]string, 0, len(seen))
	for name := range seen {
		refs = append(refs, name)
	}
	sort.Strings(refs)
	return refs
}

func (cfg *Config) upsertRawNamedEntry(rawKey string, fallback []any, nameKeys []string, pathName string, payload map[string]any, create bool, notFoundErr, duplicateErr error) (map[string]any, error) {
	if cfg == nil {
		return nil, notFoundErr
	}
	pathName = strings.TrimSpace(pathName)
	incoming := cloneMap(payload)
	if incoming == nil {
		incoming = map[string]any{}
	}
	payloadName, err := validateRawPayloadName(incoming, nameKeys, pathName)
	if err != nil {
		return nil, err
	}
	if pathName != "" {
		payloadName = pathName
	}
	if payloadName == "" {
		return nil, ErrAdminResourceNameRequired
	}

	rawList := rawListForUpdate(rawConfigValue(cfg, rawKey), fallback)
	out := make([]any, 0, len(rawList)+1)
	found := false
	var saved map[string]any
	for _, item := range rawList {
		existingName, ok := rawNamedEntryNameFromValue(item, nameKeys...)
		if !ok || existingName != payloadName {
			out = append(out, cloneAny(item))
			continue
		}
		if create {
			return nil, duplicateErr
		}
		existing, ok := rawNamedEntryMapFromValue(item, nameKeys...)
		if !ok {
			return nil, notFoundErr
		}
		saved = DeepMerge(rawEntryBaseForUpdate(rawKey, existing, incoming), incoming)
		if rawNameFromMap(saved, nameKeys...) == "" {
			saved[nameKeys[0]] = payloadName
		}
		out = append(out, cloneMap(saved))
		found = true
	}
	if !found {
		if !create {
			return nil, notFoundErr
		}
		saved = cloneMap(incoming)
		if rawNameFromMap(saved, nameKeys...) == "" {
			saved[nameKeys[0]] = payloadName
		}
		out = append(out, cloneMap(saved))
	}
	nextRaw := cloneMap(cfg.Raw)
	if nextRaw == nil {
		nextRaw = map[string]any{}
	}
	nextRaw[rawKey] = out
	refreshed, err := FromMap(nextRaw, cfg.Path)
	if err != nil {
		return nil, err
	}
	*cfg = *refreshed
	return cloneMap(saved), nil
}

func rawEntryBaseForUpdate(rawKey string, existing map[string]any, incoming map[string]any) map[string]any {
	base := cloneMap(existing)
	for _, key := range replaceOnlyRawFields(rawKey) {
		if _, ok := incoming[key]; ok {
			delete(base, key)
		}
	}
	return base
}

func replaceOnlyRawFields(rawKey string) []string {
	switch rawKey {
	case "providers":
		return []string{"limits"}
	case "nodes":
		return []string{"allowed_keys", "limits", "modules"}
	case "models":
		return []string{"allowed_keys", "limits", "node_weights"}
	default:
		return nil
	}
}

func (cfg *Config) deleteRawNamedEntry(rawKey string, fallback []any, nameKeys []string, name string, notFoundErr error) error {
	name = strings.TrimSpace(name)
	if cfg == nil || name == "" {
		return notFoundErr
	}
	rawList := rawListForUpdate(rawConfigValue(cfg, rawKey), fallback)
	out := make([]any, 0, len(rawList))
	found := false
	for _, item := range rawList {
		existingName, ok := rawNamedEntryNameFromValue(item, nameKeys...)
		if ok && existingName == name {
			found = true
			continue
		}
		out = append(out, cloneAny(item))
	}
	if !found {
		return notFoundErr
	}
	if cfg.Raw == nil {
		cfg.Raw = map[string]any{}
	}
	cfg.Raw[rawKey] = out
	return nil
}

func validateRawPayloadName(payload map[string]any, nameKeys []string, pathName string) (string, error) {
	pathName = strings.TrimSpace(pathName)
	names := rawNamesFromMap(payload, nameKeys...)
	effectiveName := ""
	for _, name := range names {
		if effectiveName == "" {
			effectiveName = name
		}
		if pathName != "" && name != pathName {
			return "", ErrAdminResourceNameMismatch
		}
		if pathName == "" && effectiveName != "" && name != effectiveName {
			return "", ErrAdminResourceNameMismatch
		}
	}
	return effectiveName, nil
}

func maskedRawNamedEntries(rawList []any, nameKeys ...string) []map[string]any {
	out := []map[string]any{}
	for _, item := range rawList {
		entry, ok := rawNamedEntryMapFromValue(item, nameKeys...)
		if !ok {
			continue
		}
		masked, ok := MaskSensitive(entry).(map[string]any)
		if !ok {
			continue
		}
		out = append(out, masked)
	}
	return out
}

func rawNamedEntryByName(rawList []any, name string, nameKeys ...string) (map[string]any, bool) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, false
	}
	for _, item := range rawList {
		entryName, ok := rawNamedEntryNameFromValue(item, nameKeys...)
		if !ok || entryName != name {
			continue
		}
		return rawNamedEntryMapFromValue(item, nameKeys...)
	}
	return nil, false
}

func rawNamedEntryMapFromValue(value any, nameKeys ...string) (map[string]any, bool) {
	entry, ok := toStringAnyMap(value)
	if !ok {
		return nil, false
	}
	out := cloneMap(entry)
	if rawNameFromMap(out, nameKeys...) == "" {
		return nil, false
	}
	return out, true
}

func rawNamedEntryNameFromValue(value any, nameKeys ...string) (string, bool) {
	entry, ok := toStringAnyMap(value)
	if !ok {
		return "", false
	}
	name := rawNameFromMap(entry, nameKeys...)
	return name, name != ""
}

func rawNameFromMap(entry map[string]any, nameKeys ...string) string {
	names := rawNamesFromMap(entry, nameKeys...)
	if len(names) == 0 {
		return ""
	}
	return names[0]
}

func rawNamesFromMap(entry map[string]any, nameKeys ...string) []string {
	names := []string{}
	for _, key := range nameKeys {
		if name := rawText(entry[key]); name != "" {
			names = append(names, name)
		}
	}
	return names
}

func rawConfigValue(cfg *Config, key string) any {
	if cfg == nil || cfg.Raw == nil {
		return nil
	}
	return cfg.Raw[key]
}

func rawProviders(cfg *Config) []ProviderEntry {
	if cfg == nil {
		return nil
	}
	return cfg.Providers
}

func rawNodes(cfg *Config) []NodeEntry {
	if cfg == nil {
		return nil
	}
	return cfg.Nodes
}

func rawClientModels(cfg *Config) []ClientModelEntry {
	if cfg == nil {
		return nil
	}
	return cfg.Models
}

func rawListForUpdate(raw any, fallback []any) []any {
	switch typed := raw.(type) {
	case []any:
		out := make([]any, len(typed))
		for i, item := range typed {
			out[i] = cloneAny(item)
		}
		return out
	case []map[string]any:
		out := make([]any, len(typed))
		for i, item := range typed {
			out[i] = cloneMap(item)
		}
		return out
	default:
		out := make([]any, len(fallback))
		for i, item := range fallback {
			out[i] = cloneAny(item)
		}
		return out
	}
}

func providerEntriesAsRaw(entries []ProviderEntry) []any {
	out := make([]any, len(entries))
	for i, entry := range entries {
		out[i] = providerEntryToMap(entry)
	}
	return out
}

func nodeEntriesAsRaw(entries []NodeEntry) []any {
	out := make([]any, len(entries))
	for i, entry := range entries {
		out[i] = nodeEntryToMap(entry)
	}
	return out
}

func clientModelEntriesAsRaw(entries []ClientModelEntry) []any {
	out := make([]any, len(entries))
	for i, entry := range entries {
		out[i] = clientModelEntryToMap(entry)
	}
	return out
}

func providerEntryToMap(provider ProviderEntry) map[string]any {
	raw := cloneMap(provider.Raw)
	setRawString(raw, "name", provider.Name)
	setRawString(raw, "api_base", provider.APIBase)
	setRawString(raw, "api_key", provider.APIKey)
	setRawString(raw, "api_type", provider.APIType)
	setRawString(raw, "anthropic_version", provider.AnthropicVersion)
	setRawString(raw, "mode", provider.Mode)
	setRawString(raw, "scope", provider.Scope)
	setRawString(raw, "auth_url", provider.AuthURL)
	setRawString(raw, "client_cert", provider.ClientCert)
	setRawString(raw, "client_key", provider.ClientKey)
	setRawValue(raw, "request_timeout", provider.RequestTimeout)
	setRawValue(raw, "verify_ssl", provider.VerifySSL)
	setRawValue(raw, "emulate_tool_calls", provider.EmulateToolCalls)
	setRawValue(raw, "supports_responses_api", provider.SupportsResponsesAPI)
	if !provider.Limits.IsZero() {
		raw["limits"] = provider.Limits
	}
	if provider.Parameters != nil {
		raw["parameters"] = cloneMap(provider.Parameters)
	}
	if provider.Endpoints != nil {
		raw["endpoints"] = append([]string(nil), provider.Endpoints...)
	}
	return raw
}

func nodeEntryToMap(node NodeEntry) map[string]any {
	raw := cloneMap(node.Raw)
	setRawString(raw, "name", node.Name)
	setRawString(raw, "provider", node.Provider)
	setRawString(raw, "api_base", node.APIBase)
	setRawString(raw, "api_key", node.APIKey)
	setRawString(raw, "api_type", node.APIType)
	setRawString(raw, "anthropic_version", node.AnthropicVersion)
	setRawString(raw, "mode", node.Mode)
	setRawString(raw, "scope", node.Scope)
	setRawString(raw, "auth_url", node.AuthURL)
	setRawString(raw, "client_cert", node.ClientCert)
	setRawString(raw, "client_key", node.ClientKey)
	setRawString(raw, "model", node.Model)
	setRawString(raw, "target_model", node.TargetModel)
	setRawValue(raw, "verify_ssl", node.VerifySSL)
	setRawValue(raw, "emulate_tool_calls", node.EmulateToolCalls)
	setRawValue(raw, "request_timeout", node.RequestTimeout)
	setRawValue(raw, "supports_reasoning", node.SupportsReasoning)
	setRawValue(raw, "supports_responses_api", node.SupportsResponsesAPI)
	setRawValue(raw, "http2", node.HTTP2)
	if node.Weight != 0 {
		raw["weight"] = node.Weight
	}
	if !node.Limits.IsZero() {
		raw["limits"] = node.Limits
	}
	if node.Parameters != nil {
		raw["parameters"] = cloneMap(node.Parameters)
	}
	if node.Modules != nil {
		raw["modules"] = cloneMap(node.Modules)
	}
	if node.Endpoints != nil {
		raw["endpoints"] = append([]string(nil), node.Endpoints...)
	}
	setRawValue(raw, "allowed_keys", node.AllowedKeys)
	return raw
}

func clientModelEntryToMap(model ClientModelEntry) map[string]any {
	raw := cloneMap(model.Raw)
	setRawString(raw, "name", model.Name)
	setRawString(raw, "model_name", model.ModelName)
	if model.Nodes != nil {
		raw["nodes"] = append([]string(nil), model.Nodes...)
	}
	if model.NodeWeights != nil {
		weights := map[string]any{}
		for key, value := range model.NodeWeights {
			weights[key] = value
		}
		raw["node_weights"] = weights
	}
	setRawString(raw, "balancing", model.Balancing)
	setRawValue(raw, "allowed_keys", model.AllowedKeys)
	if !model.Limits.IsZero() {
		raw["limits"] = model.Limits
	}
	return raw
}

func setRawString(raw map[string]any, key string, value string) {
	if strings.TrimSpace(value) != "" {
		raw[key] = value
	}
}

func setRawValue(raw map[string]any, key string, value any) {
	if value != nil {
		raw[key] = value
	}
}

func (cfg *Config) UpsertLegacyModel(entry ModelEntry, fallbacks []string) bool {
	replaced := false
	for i, existing := range cfg.ModelList {
		if existing.ModelName == entry.ModelName {
			cfg.ModelList[i] = entry
			replaced = true
			break
		}
	}
	if !replaced {
		cfg.ModelList = append(cfg.ModelList, entry)
	}
	if len(fallbacks) > 0 {
		setFallback(&cfg.RouterSettings, entry.ModelName, fallbacks)
	}
	if cfg.Raw == nil {
		cfg.Raw = map[string]any{}
	}
	cfg.upsertRawLegacyModel(entry)
	cfg.setRawRouterFallbacks()
	return replaced
}

func (cfg *Config) upsertRawLegacyModel(entry ModelEntry) {
	if cfg.Raw == nil {
		cfg.Raw = map[string]any{}
	}
	rawEntry := modelEntryToMap(entry)
	rawEntry["model_name"] = entry.ModelName

	rawModels := rawModelListForUpdate(cfg.Raw["model_list"], cfg.ModelList)
	out := make([]any, 0, len(rawModels)+1)
	replaced := false
	for _, item := range rawModels {
		ref, ok := rawModelRefFromValue(item)
		if ok && ref.name == entry.ModelName {
			out = append(out, rawEntry)
			replaced = true
			continue
		}
		out = append(out, cloneAny(item))
	}
	if !replaced {
		out = append(out, rawEntry)
	}
	cfg.Raw["model_list"] = out
}

func (cfg *Config) CopyLegacyModel(source, target string) (map[string]any, error) {
	source = strings.TrimSpace(source)
	target = strings.TrimSpace(target)
	if cfg == nil || source == "" {
		return nil, ErrLegacyModelNotFound
	}
	if target == "" {
		return nil, ErrLegacyModelDuplicate
	}
	if _, ok := cfg.rawModelEntryByName(source); !ok {
		return nil, ErrLegacyModelNotFound
	}
	if _, ok := cfg.rawModelEntryByName(target); ok {
		return nil, ErrLegacyModelDuplicate
	}

	rawModels := rawModelListForUpdate(cfg.Raw["model_list"], cfg.ModelList)
	copied := map[string]any{}
	for _, item := range rawModels {
		ref, ok := rawModelRefFromValue(item)
		if !ok || ref.name != source {
			continue
		}
		entry, ok := rawModelEntryMapFromValue(item)
		if !ok {
			return nil, ErrLegacyModelNotFound
		}
		copied = entry
		break
	}
	if len(copied) == 0 {
		return nil, ErrLegacyModelNotFound
	}
	copied["model_name"] = target
	for _, key := range []string{"_inherited_from", "editable", "source"} {
		delete(copied, key)
	}

	out := make([]any, 0, len(rawModels)+1)
	for _, item := range rawModels {
		out = append(out, cloneAny(item))
	}
	out = append(out, cloneMap(copied))
	if cfg.Raw == nil {
		cfg.Raw = map[string]any{}
	}
	cfg.Raw["model_list"] = out
	if err := cfg.reloadFromRaw(); err != nil {
		return nil, err
	}
	return cloneMap(copied), nil
}

func (cfg *Config) DeleteLegacyModels(name string, cascade bool) ([]string, error) {
	name = strings.TrimSpace(name)
	if cfg == nil || name == "" {
		return nil, ErrLegacyModelNotFound
	}
	if _, ok := cfg.rawModelEntryByName(name); !ok {
		return nil, ErrLegacyModelNotFound
	}
	direct, descendants, ok := cfg.ModelDependents(name)
	if !ok {
		return nil, ErrLegacyModelNotFound
	}
	if len(descendants) > 0 && !cascade {
		return nil, &LegacyModelDependentsError{
			Model:       name,
			Direct:      append([]string(nil), direct...),
			Descendants: append([]string(nil), descendants...),
		}
	}

	removed := append([]string{name}, descendants...)
	removedSet := map[string]bool{}
	for _, item := range removed {
		removedSet[item] = true
	}

	rawModels := rawModelListForUpdate(cfg.Raw["model_list"], cfg.ModelList)
	out := make([]any, 0, len(rawModels))
	for _, item := range rawModels {
		ref, ok := rawModelRefFromValue(item)
		if ok && removedSet[ref.name] {
			continue
		}
		out = append(out, cloneAny(item))
	}
	if cfg.Raw == nil {
		cfg.Raw = map[string]any{}
	}
	cfg.Raw["model_list"] = out
	removeFallback(&cfg.RouterSettings, removed)
	cfg.setRawRouterFallbacks()
	if err := cfg.reloadFromRaw(); err != nil {
		return nil, err
	}
	return removed, nil
}

func (cfg *Config) setRawRouterFallbacks() {
	if cfg.Raw == nil {
		cfg.Raw = map[string]any{}
	}
	rawRouter, ok := toStringAnyMap(cfg.Raw["router_settings"])
	if !ok {
		cfg.Raw["router_settings"] = cfg.RouterSettings
		return
	}
	out := cloneMap(rawRouter)
	out["fallbacks"] = cfg.RouterSettings.Fallbacks
	cfg.Raw["router_settings"] = out
}

func (cfg *Config) normalizeRouterFallbacks(payload any) ([]map[string]any, error) {
	entries, ok := toAnySlice(payload)
	if !ok {
		return nil, invalidRouterFallbacksError("router_settings.fallbacks", "must be an array")
	}
	knownNames := cfg.knownRouterFallbackNames()
	out := make([]map[string]any, 0, len(entries))
	seenSources := map[string]bool{}
	for i, rawEntry := range entries {
		path := fmt.Sprintf("router_settings.fallbacks[%d]", i)
		entry, ok := toStringAnyMap(rawEntry)
		if !ok {
			return nil, invalidRouterFallbacksError(path, "must be an object")
		}
		if len(entry) != 1 {
			return nil, invalidRouterFallbacksError(path, "must contain exactly one source model")
		}
		for rawSource, rawTargets := range entry {
			source := strings.TrimSpace(rawSource)
			sourcePath := path + "." + source
			if source == "" {
				return nil, invalidRouterFallbacksError(path, "source model cannot be empty")
			}
			if seenSources[source] {
				return nil, invalidRouterFallbacksError(sourcePath, "duplicates an earlier fallback source")
			}
			if !knownNames[source] {
				return nil, invalidRouterFallbacksError(sourcePath, "source does not match a legacy model, client model, or node")
			}
			targets, err := normalizeRouterFallbackTargets(rawTargets, sourcePath, knownNames)
			if err != nil {
				return nil, err
			}
			out = append(out, map[string]any{source: targets})
			seenSources[source] = true
		}
	}
	return out, nil
}

func normalizeRouterFallbackTargets(value any, path string, knownNames map[string]bool) ([]any, error) {
	rawTargets, ok := fallbackTargetList(value)
	if !ok {
		return nil, invalidRouterFallbacksError(path, "targets must be an array of strings")
	}
	if len(rawTargets) == 0 {
		return nil, invalidRouterFallbacksError(path, "targets cannot be empty")
	}
	targets := make([]any, 0, len(rawTargets))
	for i, rawTarget := range rawTargets {
		target, ok := rawTarget.(string)
		targetPath := fmt.Sprintf("%s[%d]", path, i)
		if !ok {
			return nil, invalidRouterFallbacksError(targetPath, "target must be a string")
		}
		target = strings.TrimSpace(target)
		if target == "" {
			return nil, invalidRouterFallbacksError(targetPath, "target cannot be empty")
		}
		if !knownNames[target] {
			return nil, invalidRouterFallbacksError(targetPath, "target does not match a legacy model, client model, or node")
		}
		targets = append(targets, target)
	}
	return targets, nil
}

func (cfg *Config) knownRouterFallbackNames() map[string]bool {
	known := map[string]bool{}
	if cfg == nil {
		return known
	}
	for _, model := range cfg.ModelList {
		if name := strings.TrimSpace(model.ModelName); name != "" {
			known[name] = true
		}
	}
	for _, model := range cfg.Models {
		if name := model.EffectiveName(); name != "" {
			known[name] = true
		}
	}
	for _, node := range cfg.Nodes {
		if name := strings.TrimSpace(node.Name); name != "" {
			known[name] = true
		}
	}
	return known
}

func fallbackTargetList(value any) ([]any, bool) {
	switch typed := value.(type) {
	case []any:
		return typed, true
	case []string:
		out := make([]any, len(typed))
		for i, item := range typed {
			out[i] = item
		}
		return out, true
	default:
		return nil, false
	}
}

func rawFallbackEntriesForRead(value any) ([]map[string]any, bool) {
	entries, ok := toAnySlice(value)
	if !ok {
		return nil, false
	}
	out := make([]map[string]any, 0, len(entries))
	for _, rawEntry := range entries {
		entry, ok := toStringAnyMap(rawEntry)
		if !ok {
			return nil, false
		}
		out = append(out, cloneMap(entry))
	}
	return out, true
}

func cloneFallbackEntries(entries []map[string]any) []map[string]any {
	if len(entries) == 0 {
		return []map[string]any{}
	}
	out := make([]map[string]any, len(entries))
	for i, entry := range entries {
		out[i] = cloneMap(entry)
	}
	return out
}

func invalidRouterFallbacksError(path string, reason string) error {
	if strings.TrimSpace(path) == "" {
		return fmt.Errorf("%w: %s", ErrInvalidFallbacks, reason)
	}
	return fmt.Errorf("%w: %s %s", ErrInvalidFallbacks, path, reason)
}

func (cfg *Config) RawLegacyModel(name string) (map[string]any, bool) {
	return cfg.rawModelEntryByName(name)
}

func (cfg *Config) rawModelEntryByName(name string) (map[string]any, bool) {
	name = strings.TrimSpace(name)
	if cfg == nil || name == "" {
		return nil, false
	}
	for _, item := range rawModelListForUpdate(cfg.Raw["model_list"], cfg.ModelList) {
		ref, ok := rawModelRefFromValue(item)
		if !ok || ref.name != name {
			continue
		}
		entry, ok := rawModelEntryMapFromValue(item)
		if !ok {
			return nil, false
		}
		return entry, true
	}
	return nil, false
}

func rawModelEntryMapFromValue(value any) (map[string]any, bool) {
	switch typed := value.(type) {
	case ModelEntry:
		entry := modelEntryToMap(typed)
		if rawText(entry["model_name"]) == "" {
			return nil, false
		}
		return entry, true
	case map[string]any:
		entry := cloneMap(typed)
		if rawText(entry["model_name"]) == "" {
			return nil, false
		}
		return entry, true
	case map[any]any:
		return rawModelEntryMapFromValue(toStringKeyedMap(typed))
	default:
		if mapped, ok := toStringAnyMap(value); ok {
			return rawModelEntryMapFromValue(mapped)
		}
		return nil, false
	}
}

func (cfg *Config) reloadFromRaw() error {
	if cfg == nil {
		return fmt.Errorf("config is nil")
	}
	refreshed, err := FromMap(cfg.Raw, cfg.Path)
	if err != nil {
		return err
	}
	*cfg = *refreshed
	return nil
}

func rawModelListForUpdate(raw any, fallback []ModelEntry) []any {
	switch typed := raw.(type) {
	case []any:
		out := make([]any, len(typed))
		for i, item := range typed {
			out[i] = cloneAny(item)
		}
		return out
	case []map[string]any:
		out := make([]any, len(typed))
		for i, item := range typed {
			out[i] = cloneMap(item)
		}
		return out
	case []ModelEntry:
		out := make([]any, len(typed))
		for i, item := range typed {
			out[i] = modelEntryToMap(item)
		}
		return out
	default:
		out := make([]any, len(fallback))
		for i, item := range fallback {
			out[i] = modelEntryToMap(item)
		}
		return out
	}
}

func (cfg *Config) FindLegacyModel(name string) (ModelEntry, bool) {
	for _, entry := range cfg.ModelList {
		if entry.ModelName == name {
			return entry, true
		}
	}
	return ModelEntry{}, false
}

func (cfg *Config) AdminModels() AdminModelList {
	out := AdminModelList{
		Protected:   []map[string]any{},
		Unprotected: []map[string]any{},
	}
	if cfg == nil {
		return out
	}
	for _, entry := range cfg.ModelList {
		if strings.TrimSpace(entry.ModelName) == "" {
			continue
		}
		model := maskedResolvedModelConfig(entry)
		if modelProtected(entry) {
			out.Protected = append(out.Protected, model)
		} else {
			out.Unprotected = append(out.Unprotected, model)
		}
	}
	sortModelConfigs(out.Protected)
	sortModelConfigs(out.Unprotected)
	return out
}

func (cfg *Config) ModelTree() ModelTree {
	tree := ModelTree{
		Roots: []string{},
		Nodes: map[string]ModelTreeNode{},
	}
	if cfg == nil {
		return tree
	}
	graph := cfg.modelInheritanceGraph()
	entries := map[string]ModelEntry{}
	for _, entry := range cfg.ModelList {
		name := strings.TrimSpace(entry.ModelName)
		if name != "" {
			entries[name] = entry
		}
	}
	for _, name := range graph.sortedNames() {
		parent := graph.parents[name]
		if parent != "" && !graph.names[parent] {
			parent = ""
		}
		children := append([]string(nil), graph.children[name]...)
		if children == nil {
			children = []string{}
		}
		entry, ok := entries[name]
		if !ok {
			entry = ModelEntry{ModelName: name}
		}
		if parent == "" {
			tree.Roots = append(tree.Roots, name)
		}
		tree.Nodes[name] = ModelTreeNode{
			Config:    maskedResolvedModelConfig(entry),
			Parent:    parent,
			Children:  children,
			Protected: modelProtected(entry),
			Editable:  modelEditable(entry),
		}
	}
	sort.Strings(tree.Roots)
	return tree
}

func (cfg *Config) ModelAncestry(name string) ([]string, bool) {
	name = strings.TrimSpace(name)
	if cfg == nil || name == "" {
		return nil, false
	}
	graph := cfg.modelInheritanceGraph()
	if !graph.names[name] {
		return nil, false
	}
	chain := []string{name}
	seen := map[string]bool{name: true}
	for parent := graph.parents[name]; parent != ""; parent = graph.parents[parent] {
		chain = append(chain, parent)
		if seen[parent] {
			break
		}
		seen[parent] = true
	}
	return chain, true
}

func (cfg *Config) ModelDependents(name string) ([]string, []string, bool) {
	name = strings.TrimSpace(name)
	if cfg == nil || name == "" {
		return nil, nil, false
	}
	graph := cfg.modelInheritanceGraph()
	if !graph.names[name] {
		return nil, nil, false
	}
	direct := append([]string(nil), graph.children[name]...)
	if direct == nil {
		direct = []string{}
	}
	descendantSet := map[string]bool{}
	var walk func(string)
	walk = func(parent string) {
		for _, child := range graph.children[parent] {
			if descendantSet[child] {
				continue
			}
			descendantSet[child] = true
			walk(child)
		}
	}
	walk(name)
	all := make([]string, 0, len(descendantSet))
	for child := range descendantSet {
		all = append(all, child)
	}
	sort.Strings(all)
	return direct, all, true
}

type rawModelRef struct {
	name   string
	parent string
}

type modelInheritanceGraph struct {
	names    map[string]bool
	parents  map[string]string
	children map[string][]string
}

func (cfg *Config) modelInheritanceGraph() modelInheritanceGraph {
	graph := modelInheritanceGraph{
		names:    map[string]bool{},
		parents:  map[string]string{},
		children: map[string][]string{},
	}
	if cfg == nil {
		return graph
	}
	for _, ref := range cfg.rawModelRefs() {
		if ref.name == "" {
			continue
		}
		graph.names[ref.name] = true
		if ref.parent != "" {
			graph.parents[ref.name] = ref.parent
		}
	}
	for _, entry := range cfg.ModelList {
		name := strings.TrimSpace(entry.ModelName)
		if name != "" {
			graph.names[name] = true
		}
	}
	for child, parent := range graph.parents {
		if parent == "" {
			continue
		}
		graph.children[parent] = append(graph.children[parent], child)
	}
	for parent := range graph.children {
		sort.Strings(graph.children[parent])
	}
	return graph
}

func (graph modelInheritanceGraph) sortedNames() []string {
	names := make([]string, 0, len(graph.names))
	for name := range graph.names {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func (cfg *Config) rawModelRefs() []rawModelRef {
	if cfg == nil {
		return nil
	}
	if cfg.Raw != nil {
		if refs := rawModelRefsFromValue(cfg.Raw["model_list"]); len(refs) > 0 {
			return refs
		}
	}
	return rawModelRefsFromEntries(cfg.ModelList)
}

func rawModelRefsFromEntries(entries []ModelEntry) []rawModelRef {
	refs := make([]rawModelRef, 0, len(entries))
	for _, entry := range entries {
		if ref, ok := rawModelRefFromValue(entry); ok {
			refs = append(refs, ref)
		}
	}
	return refs
}

func rawModelRefsFromValue(value any) []rawModelRef {
	switch typed := value.(type) {
	case []ModelEntry:
		return rawModelRefsFromEntries(typed)
	case []map[string]any:
		refs := make([]rawModelRef, 0, len(typed))
		for _, item := range typed {
			if ref, ok := rawModelRefFromValue(item); ok {
				refs = append(refs, ref)
			}
		}
		return refs
	case []any:
		refs := make([]rawModelRef, 0, len(typed))
		for _, item := range typed {
			if ref, ok := rawModelRefFromValue(item); ok {
				refs = append(refs, ref)
			}
		}
		return refs
	default:
		return nil
	}
}

func rawModelRefFromValue(value any) (rawModelRef, bool) {
	switch typed := value.(type) {
	case ModelEntry:
		name := strings.TrimSpace(typed.ModelName)
		if name == "" {
			return rawModelRef{}, false
		}
		return rawModelRef{name: name, parent: strings.TrimSpace(typed.Extends)}, true
	case map[string]any:
		name := rawText(typed["model_name"])
		if name == "" {
			return rawModelRef{}, false
		}
		return rawModelRef{name: name, parent: rawText(typed["extends"])}, true
	case map[any]any:
		return rawModelRefFromValue(toStringKeyedMap(typed))
	default:
		if mapped, ok := toStringAnyMap(value); ok {
			return rawModelRefFromValue(mapped)
		}
		return rawModelRef{}, false
	}
}

func maskedResolvedModelConfig(entry ModelEntry) map[string]any {
	payload := modelEntryToMap(entry)
	payload["model_name"] = entry.ModelName
	payload["protected"] = modelProtected(entry)
	payload["editable"] = modelEditable(entry)
	if !entry.Limits.IsZero() {
		payload["limits"] = entry.Limits
	}
	masked, ok := MaskSensitive(payload).(map[string]any)
	if !ok {
		return map[string]any{}
	}
	return masked
}

func modelProtected(entry ModelEntry) bool {
	return NormalizeProtected(entry.Protected, true)
}

func modelEditable(entry ModelEntry) bool {
	if entry.Editable != nil {
		return ParseBool(entry.Editable)
	}
	return !modelProtected(entry)
}

func sortModelConfigs(items []map[string]any) {
	sort.SliceStable(items, func(i, j int) bool {
		return rawText(items[i]["model_name"]) < rawText(items[j]["model_name"])
	})
}

func rawText(value any) string {
	if value == nil {
		return ""
	}
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed)
	default:
		text := strings.TrimSpace(fmt.Sprint(value))
		if text == "<nil>" {
			return ""
		}
		return text
	}
}

func toStringKeyedMap(in map[any]any) map[string]any {
	out := map[string]any{}
	for key, value := range in {
		out[fmt.Sprint(key)] = value
	}
	return out
}

func setFallback(settings *RouterSettings, name string, fallbacks []string) {
	values := make([]any, len(fallbacks))
	for i, value := range fallbacks {
		values[i] = value
	}
	for _, entry := range settings.Fallbacks {
		if _, ok := entry[name]; ok {
			entry[name] = values
			return
		}
	}
	settings.Fallbacks = append(settings.Fallbacks, map[string]any{name: values})
}

func removeFallback(settings *RouterSettings, names []string) {
	if settings == nil || len(settings.Fallbacks) == 0 || len(names) == 0 {
		return
	}
	removed := map[string]bool{}
	for _, name := range names {
		if text := strings.TrimSpace(name); text != "" {
			removed[text] = true
		}
	}
	if len(removed) == 0 {
		return
	}

	out := make([]map[string]any, 0, len(settings.Fallbacks))
	for _, entry := range settings.Fallbacks {
		cleaned := map[string]any{}
		for source, rawFallbacks := range entry {
			if removed[source] {
				continue
			}
			targets := stringList(rawFallbacks)
			filtered := make([]any, 0, len(targets))
			for _, target := range targets {
				if removed[target] {
					continue
				}
				filtered = append(filtered, target)
			}
			if len(filtered) > 0 {
				cleaned[source] = filtered
			}
		}
		if len(cleaned) > 0 {
			out = append(out, cleaned)
		}
	}
	settings.Fallbacks = out
}

func ResolvePath(path string) string {
	if filepath.IsAbs(path) {
		return path
	}
	return filepath.Join(ProjectRoot(), path)
}

func ProjectRoot() string {
	wd, err := os.Getwd()
	if err != nil {
		return "."
	}
	for {
		if _, err := os.Stat(filepath.Join(wd, "pyproject.toml")); err == nil {
			return wd
		}
		if _, err := os.Stat(filepath.Join(wd, "go.mod")); err == nil {
			return wd
		}
		parent := filepath.Dir(wd)
		if parent == wd {
			return "."
		}
		wd = parent
	}
}

func ResolveEnvPath(configPath string, envPath string) string {
	if envPath != "" {
		return ResolvePath(envPath)
	}
	dir := filepath.Dir(configPath)
	base := strings.TrimSuffix(filepath.Base(configPath), filepath.Ext(configPath))
	if strings.HasPrefix(base, "config_") {
		return filepath.Join(dir, ".env_"+strings.TrimPrefix(base, "config_"))
	}
	return filepath.Join(dir, ".env")
}

func LoadEnvValues(path string) (map[string]string, error) {
	values := map[string]string{}
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return values, nil
		}
		return values, err
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, val, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		val = strings.TrimSpace(val)
		val = strings.Trim(val, `"'`)
		if key != "" {
			values[key] = val
		}
	}
	return values, scanner.Err()
}

var envPattern = regexp.MustCompile(`\$\{([^}]+)\}|\$([A-Za-z_][A-Za-z0-9_]*)`)

func SubstituteEnv(value any, envValues map[string]string) any {
	switch typed := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(typed))
		for k, v := range typed {
			out[k] = SubstituteEnv(v, envValues)
		}
		return out
	case map[any]any:
		out := make(map[string]any, len(typed))
		for k, v := range typed {
			out[fmt.Sprint(k)] = SubstituteEnv(v, envValues)
		}
		return out
	case []any:
		out := make([]any, len(typed))
		for i, v := range typed {
			out[i] = SubstituteEnv(v, envValues)
		}
		return out
	case string:
		return envPattern.ReplaceAllStringFunc(typed, func(match string) string {
			parts := envPattern.FindStringSubmatch(match)
			name := parts[1]
			if name == "" {
				name = parts[2]
			}
			if envValues != nil {
				if val, ok := envValues[name]; ok {
					return val
				}
			}
			if val, ok := os.LookupEnv(name); ok {
				return val
			}
			return match
		})
	default:
		return value
	}
}

func DeepMerge(base, override map[string]any) map[string]any {
	result := cloneMap(base)
	for k, v := range override {
		if existing, ok := result[k].(map[string]any); ok {
			if incoming, ok := toStringAnyMap(v); ok {
				result[k] = DeepMerge(existing, incoming)
				continue
			}
		}
		result[k] = cloneAny(v)
	}
	return result
}

func ResolveAllModelInheritance(models []ModelEntry) ([]ModelEntry, error) {
	if len(models) == 0 {
		return nil, nil
	}
	lookup := map[string]ModelEntry{}
	for _, model := range models {
		if model.ModelName != "" {
			lookup[model.ModelName] = model
		}
	}
	out := make([]ModelEntry, 0, len(models))
	for _, model := range models {
		if model.ModelName == "" {
			continue
		}
		resolved, err := resolveModel(model, lookup, nil)
		if err != nil {
			return nil, err
		}
		out = append(out, resolved)
	}
	return out, nil
}

func resolveModel(model ModelEntry, lookup map[string]ModelEntry, chain []string) (ModelEntry, error) {
	if model.Extends == "" {
		model.Extends = ""
		model.Protected = NormalizeProtected(model.Protected, true)
		model.Editable = !ParseBool(model.Protected)
		return model, nil
	}
	for _, name := range chain {
		if name == model.Extends {
			return ModelEntry{}, fmt.Errorf("circular inheritance detected for model %q", model.ModelName)
		}
	}
	if len(chain) >= MaxInheritanceDepth {
		return ModelEntry{}, fmt.Errorf("maximum inheritance depth exceeded for model %q", model.ModelName)
	}
	base, ok := lookup[model.Extends]
	if !ok {
		return ModelEntry{}, fmt.Errorf("model %q extends %q, but base model not found", model.ModelName, model.Extends)
	}
	resolvedBase, err := resolveModel(base, lookup, append(chain, model.ModelName))
	if err != nil {
		return ModelEntry{}, err
	}
	baseMap := modelEntryToMap(resolvedBase)
	overrideMap := modelEntryToMap(model)
	merged := DeepMerge(baseMap, overrideMap)
	delete(merged, "extends")
	merged["model_name"] = model.ModelName
	merged["_inherited_from"] = model.Extends
	merged["protected"] = NormalizeProtected(model.Protected, true)
	merged["editable"] = !ParseBool(merged["protected"])
	return mapToModelEntry(merged)
}

func NormalizeProtected(value any, def bool) bool {
	if value == nil {
		return def
	}
	return ParseBool(value)
}

func ParseBool(value any) bool {
	switch typed := value.(type) {
	case bool:
		return typed
	case string:
		switch strings.ToLower(strings.TrimSpace(typed)) {
		case "1", "true", "yes", "on":
			return true
		default:
			return false
		}
	case int:
		return typed != 0
	case int64:
		return typed != 0
	case float64:
		return typed != 0
	default:
		return value != nil
	}
}

func ParseBoolStrict(value any) bool {
	switch typed := value.(type) {
	case bool:
		return typed
	case string:
		switch strings.ToLower(strings.TrimSpace(typed)) {
		case "1", "true", "yes", "on":
			return true
		default:
			return false
		}
	case int:
		return typed == 1
	case int64:
		return typed == 1
	case float64:
		return typed == 1
	default:
		return false
	}
}

func IsBoolLike(value any) bool {
	if value == nil {
		return true
	}
	switch typed := value.(type) {
	case bool:
		return true
	case string:
		switch strings.ToLower(strings.TrimSpace(typed)) {
		case "", "0", "1", "true", "false", "yes", "no", "on", "off":
			return true
		default:
			return false
		}
	case int, int64, float64:
		return true
	default:
		return false
	}
}

func IsStrictBoolLike(value any) bool {
	if value == nil {
		return true
	}
	switch typed := value.(type) {
	case bool:
		return true
	case string:
		switch strings.ToLower(strings.TrimSpace(typed)) {
		case "", "0", "1", "true", "false", "yes", "no", "on", "off":
			return true
		default:
			return false
		}
	case int:
		return typed == 0 || typed == 1
	case int64:
		return typed == 0 || typed == 1
	case float64:
		return typed == 0 || typed == 1
	default:
		return false
	}
}

func modelEntryToMap(model ModelEntry) map[string]any {
	raw := cloneMap(model.Raw)
	if model.ModelName != "" {
		raw["model_name"] = model.ModelName
	}
	if model.Extends != "" {
		raw["extends"] = model.Extends
	}
	if model.Protected != nil {
		raw["protected"] = model.Protected
	}
	if model.Editable != nil {
		raw["editable"] = model.Editable
	}
	if model.ModelParams != nil {
		raw["model_params"] = cloneMap(model.ModelParams)
	}
	if model.Parameters != nil {
		raw["parameters"] = cloneMap(model.Parameters)
	}
	if model.Modules != nil {
		raw["modules"] = cloneMap(model.Modules)
	}
	if model.AccessControl != nil {
		raw["access_control"] = cloneMap(model.AccessControl)
	}
	return raw
}

func mapToModelEntry(raw map[string]any) (ModelEntry, error) {
	buf, err := yaml.Marshal(raw)
	if err != nil {
		return ModelEntry{}, err
	}
	var model ModelEntry
	if err := yaml.Unmarshal(buf, &model); err != nil {
		return ModelEntry{}, err
	}
	model.Raw = raw
	return model, nil
}

func cloneMap(in map[string]any) map[string]any {
	out := map[string]any{}
	for k, v := range in {
		out[k] = cloneAny(v)
	}
	return out
}

func cloneAny(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		return cloneMap(typed)
	case map[any]any:
		out := map[string]any{}
		for k, v := range typed {
			out[fmt.Sprint(k)] = cloneAny(v)
		}
		return out
	case []any:
		out := make([]any, len(typed))
		for i, v := range typed {
			out[i] = cloneAny(v)
		}
		return out
	default:
		return typed
	}
}

func toStringAnyMap(value any) (map[string]any, bool) {
	switch typed := value.(type) {
	case map[string]any:
		return typed, true
	case map[any]any:
		out := map[string]any{}
		for k, v := range typed {
			out[fmt.Sprint(k)] = v
		}
		return out, true
	default:
		return nil, false
	}
}

func toAnySlice(value any) ([]any, bool) {
	switch typed := value.(type) {
	case []any:
		return typed, true
	case []map[string]any:
		out := make([]any, len(typed))
		for i, item := range typed {
			out[i] = item
		}
		return out, true
	default:
		return nil, false
	}
}

func matchingExistingItem(incoming any, existing []any, fallbackIndex int) any {
	incomingMap, ok := toStringAnyMap(incoming)
	if ok {
		for _, key := range []string{"model_name", "name", "key_id", "id"} {
			incomingID := strings.TrimSpace(fmt.Sprint(incomingMap[key]))
			if incomingID == "" || incomingID == "<nil>" {
				continue
			}
			for _, candidate := range existing {
				candidateMap, ok := toStringAnyMap(candidate)
				if !ok {
					continue
				}
				if strings.TrimSpace(fmt.Sprint(candidateMap[key])) == incomingID {
					return candidate
				}
			}
		}
	}
	if fallbackIndex >= 0 && fallbackIndex < len(existing) {
		return existing[fallbackIndex]
	}
	return nil
}

func ToJSONMap(value any) map[string]any {
	buf, _ := json.Marshal(value)
	out := map[string]any{}
	_ = json.Unmarshal(buf, &out)
	return out
}
