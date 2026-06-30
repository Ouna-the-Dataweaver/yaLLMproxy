package policy

import (
	"crypto/subtle"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/theio/yallmp-proxy/internal/config"
)

var (
	ErrMissingAPIKey         = errors.New("api key required")
	ErrInvalidAPIKey         = errors.New("invalid api key")
	ErrDisabledAPIKey        = errors.New("api key is disabled")
	ErrModelAccessDenied     = errors.New("api key not authorized for model")
	ErrMissingRequiredHeader = errors.New("missing required header")
)

type Context struct {
	KeyID           string
	KeyName         string
	GroupID         string
	GroupName       string
	Authenticated   bool
	Limits          config.LimitsConfig
	RequiredHeaders map[string]string
}

func (c Context) LimitKeyID() string {
	if c.GroupID != "" {
		return "group:" + c.GroupID
	}
	return c.KeyID
}

func LimitKeyIDForStoredKey(cfg config.AppKeysConfig, appKeyID string) string {
	appKeyID = strings.TrimSpace(appKeyID)
	if appKeyID == "" {
		return ""
	}
	for _, key := range cfg.Keys {
		keyID := strings.TrimSpace(key.KeyID)
		parsedGroupID, parsedKeyID, parsed := parseGroupedSecret(key.Secret)
		if keyID == "" && parsed {
			keyID = parsedKeyID
		}
		if keyID != appKeyID {
			continue
		}
		groupID := strings.TrimSpace(key.GroupID)
		if groupID == "" && parsed {
			groupID = parsedGroupID
		}
		if groupID != "" {
			return "group:" + groupID
		}
		return appKeyID
	}
	return appKeyID
}

type Manager struct {
	Config *config.Config
}

func NewManager(cfg *config.Config) *Manager {
	return &Manager{Config: cfg}
}

func (m *Manager) ValidateRequest(r *http.Request, modelName string) (Context, error) {
	if m == nil || m.Config == nil || !m.Config.AppKeys.Enabled {
		return Context{}, nil
	}
	cfg := m.Config.AppKeys
	header := cfg.HeaderName
	if header == "" {
		header = config.DefaultAppKeyHeader
	}
	provided := r.Header.Get(header)
	if provided == "" {
		auth := r.Header.Get("Authorization")
		if strings.HasPrefix(strings.ToLower(auth), "bearer ") {
			provided = strings.TrimSpace(auth[len("bearer "):])
		}
	}
	if provided == "" {
		if cfg.AllowUnauthenticated {
			requiredHeaders, err := enforceRequiredHeaders(r, cfg.RequiredHeaders, modelName)
			if err != nil {
				return Context{}, err
			}
			return Context{Limits: cfg.Unauthenticated, RequiredHeaders: requiredHeaders}, nil
		}
		return Context{}, ErrMissingAPIKey
	}
	for _, key := range cfg.Keys {
		if key.Secret == "" || subtle.ConstantTimeCompare([]byte(provided), []byte(key.Secret)) != 1 {
			continue
		}
		if key.Enabled != nil && !*key.Enabled {
			return Context{}, ErrDisabledAPIKey
		}
		groupID := strings.TrimSpace(key.GroupID)
		parsedGroupID, parsedKeyID, parsed := parseGroupedSecret(key.Secret)
		if groupID == "" && parsed {
			groupID = parsedGroupID
		}
		group, groupFound := findGroup(cfg.Groups, groupID)
		if groupFound && group.Enabled != nil && !*group.Enabled {
			return Context{}, ErrDisabledAPIKey
		}
		groupModels := []string(nil)
		groupLimits := config.LimitsConfig{}
		groupRequiredHeaders := []config.RequiredHeader(nil)
		groupName := ""
		if groupFound {
			groupModels = group.Models
			groupLimits = group.Limits
			groupRequiredHeaders = group.RequiredHeaders
			groupName = group.Name
		}
		if !allowedForModel(groupModels, modelName) || !allowedForModel(key.Models, modelName) {
			return Context{}, ErrModelAccessDenied
		}
		keyID := strings.TrimSpace(key.KeyID)
		if keyID == "" && parsed {
			keyID = parsedKeyID
		}
		if !m.allowedByModelAccess(keyID, modelName) {
			return Context{}, ErrModelAccessDenied
		}
		requiredHeaders, err := enforceRequiredHeaders(r, mergeRequiredHeaders(mergeRequiredHeaders(cfg.RequiredHeaders, groupRequiredHeaders), key.RequiredHeaders), modelName)
		if err != nil {
			return Context{}, err
		}
		return Context{
			KeyID:           keyID,
			GroupID:         groupID,
			GroupName:       groupName,
			KeyName:         key.Name,
			Authenticated:   true,
			Limits:          mergeLimits(mergeLimits(cfg.Defaults, groupLimits), mergeLimits(key.Limits, legacyKeyLimits(key))),
			RequiredHeaders: requiredHeaders,
		}, nil
	}
	return Context{}, ErrInvalidAPIKey
}

func (m *Manager) allowedByModelAccess(keyID string, modelName string) bool {
	if m == nil || m.Config == nil {
		return true
	}
	matched := false
	allowed := true
	for _, model := range m.Config.ModelList {
		if model.ModelName != modelName {
			continue
		}
		matched = true
		allowed = allowed && allowedKeysValue(model.AccessControl["allowed_keys"], keyID)
	}
	for _, model := range m.Config.Models {
		if model.EffectiveName() != modelName {
			continue
		}
		matched = true
		allowed = allowed && allowedKeysValue(model.AllowedKeys, keyID)
	}
	if !matched {
		return true
	}
	return allowed
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

func legacyKeyLimits(key config.AppKeyEntry) config.LimitsConfig {
	return config.LimitsConfig{
		Concurrency:         key.ConcurrencyLimit,
		Priority:            key.Priority,
		QueueTimeoutSeconds: key.QueueTimeout,
	}
}

func findGroup(groups []config.AppKeyGroupEntry, groupID string) (config.AppKeyGroupEntry, bool) {
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

func parseGroupedSecret(secret string) (string, string, bool) {
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

func allowedForModel(models []string, modelName string) bool {
	if len(models) == 0 {
		return true
	}
	for _, model := range models {
		if model == "*" || model == "all" || model == modelName {
			return true
		}
	}
	return false
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

func mergeRequiredHeaders(base []config.RequiredHeader, override []config.RequiredHeader) []config.RequiredHeader {
	if len(base) == 0 && len(override) == 0 {
		return nil
	}
	out := make([]config.RequiredHeader, 0, len(base)+len(override))
	out = appendRequiredHeaders(out, base)
	out = appendRequiredHeaders(out, override)
	return out
}

func appendRequiredHeaders(dst []config.RequiredHeader, src []config.RequiredHeader) []config.RequiredHeader {
	for _, header := range src {
		header.Models = append([]string(nil), header.Models...)
		dst = append(dst, header)
	}
	return dst
}

func enforceRequiredHeaders(r *http.Request, headers []config.RequiredHeader, modelName string) (map[string]string, error) {
	if len(headers) == 0 {
		return nil, nil
	}
	attribution := map[string]string{}
	for _, header := range headers {
		if !requiredHeaderApplies(header, modelName) {
			continue
		}
		value := strings.TrimSpace(r.Header.Get(strings.TrimSpace(header.Name)))
		if value == "" {
			return nil, ErrMissingRequiredHeader
		}
		if logAs := strings.TrimSpace(header.LogAs); logAs != "" {
			attribution[logAs] = value
		}
	}
	if len(attribution) == 0 {
		return nil, nil
	}
	return attribution, nil
}

func requiredHeaderApplies(header config.RequiredHeader, modelName string) bool {
	if len(header.Models) == 0 {
		return true
	}
	for _, model := range header.Models {
		model = strings.TrimSpace(model)
		if model == "" {
			continue
		}
		if model == "*" || strings.EqualFold(model, "all") || model == modelName {
			return true
		}
	}
	return false
}
