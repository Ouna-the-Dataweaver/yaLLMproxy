package server

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/google/uuid"
	"github.com/theio/yallmp-proxy/internal/config"
)

type adminAppKeyPayload struct {
	KeyID            string                   `json:"key_id"`
	GroupID          *string                  `json:"group_id"`
	Name             *string                  `json:"name"`
	Description      *string                  `json:"description"`
	Secret           *string                  `json:"secret"`
	Enabled          *bool                    `json:"enabled"`
	Models           *[]string                `json:"models"`
	ConcurrencyLimit *int                     `json:"concurrency_limit"`
	Priority         *int                     `json:"priority"`
	QueueTimeout     *float64                 `json:"queue_timeout"`
	Limits           *config.LimitsConfig     `json:"limits"`
	RequiredHeaders  *[]config.RequiredHeader `json:"required_headers"`
}

type adminAppKeysConfigPayload struct {
	Enabled              *bool                      `json:"enabled"`
	HeaderName           *string                    `json:"header_name"`
	AllowUnauthenticated *bool                      `json:"allow_unauthenticated"`
	RequiredHeaders      *[]config.RequiredHeader   `json:"required_headers"`
	Defaults             *config.LimitsConfig       `json:"defaults"`
	Unauthenticated      *config.LimitsConfig       `json:"unauthenticated"`
	Groups               *[]config.AppKeyGroupEntry `json:"groups"`
}

type adminAppKeyResponse struct {
	KeyID            string                  `json:"key_id"`
	GroupID          string                  `json:"group_id,omitempty"`
	Name             string                  `json:"name,omitempty"`
	Description      string                  `json:"description,omitempty"`
	Enabled          bool                    `json:"enabled"`
	Models           []string                `json:"models,omitempty"`
	ConcurrencyLimit int                     `json:"concurrency_limit,omitempty"`
	Priority         int                     `json:"priority,omitempty"`
	QueueTimeout     float64                 `json:"queue_timeout,omitempty"`
	Limits           config.LimitsConfig     `json:"limits"`
	RequiredHeaders  []config.RequiredHeader `json:"required_headers,omitempty"`
	HasSecret        bool                    `json:"has_secret"`
}

type adminAppKeysResponse struct {
	Status               string                    `json:"status,omitempty"`
	Enabled              bool                      `json:"enabled"`
	HeaderName           string                    `json:"header_name"`
	AllowUnauthenticated bool                      `json:"allow_unauthenticated"`
	RequiredHeaders      []config.RequiredHeader   `json:"required_headers,omitempty"`
	Defaults             config.LimitsConfig       `json:"defaults"`
	Unauthenticated      config.LimitsConfig       `json:"unauthenticated"`
	Groups               []config.AppKeyGroupEntry `json:"groups"`
	Keys                 []adminAppKeyResponse     `json:"keys,omitempty"`
}

func isAdminKeysPath(path string) bool {
	return path == "/admin/keys" || strings.HasPrefix(path, "/admin/keys/")
}

func (a *App) handleAdminKeys(w http.ResponseWriter, r *http.Request) {
	if !a.hasAdminPassword(r) {
		writeError(w, http.StatusForbidden, "admin password required for app key management", "permission_error")
		return
	}
	switch {
	case r.URL.Path == "/admin/keys" && r.Method == http.MethodGet:
		a.handleListAppKeys(w, r)
	case r.URL.Path == "/admin/keys" && r.Method == http.MethodPost:
		a.handleCreateAppKey(w, r)
	case r.URL.Path == "/admin/keys/config" && r.Method == http.MethodPost:
		a.handleAppKeysConfig(w, r)
	case strings.HasPrefix(r.URL.Path, "/admin/keys/"):
		a.handleAppKeyItem(w, r)
	default:
		writeError(w, http.StatusNotFound, "not found", "not_found")
	}
}

func (a *App) handleListAppKeys(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, a.adminAppKeysResponse(""))
}

func (a *App) handleAppKeyItem(w http.ResponseWriter, r *http.Request) {
	keyID, regenerate, ok := appKeyPathParts(r.URL.Path)
	if !ok {
		writeError(w, http.StatusNotFound, "not found", "not_found")
		return
	}
	switch {
	case r.Method == http.MethodGet && !regenerate:
		a.handleGetAppKey(w, r, keyID)
	case r.Method == http.MethodPut && !regenerate:
		a.handleUpdateAppKey(w, r, keyID)
	case r.Method == http.MethodDelete && !regenerate:
		a.handleDeleteAppKey(w, r, keyID)
	case r.Method == http.MethodPost && regenerate:
		a.handleRegenerateAppKey(w, r, keyID)
	default:
		writeError(w, http.StatusNotFound, "not found", "not_found")
	}
}

func appKeyPathParts(path string) (string, bool, bool) {
	rest := strings.TrimPrefix(path, "/admin/keys/")
	regenerate := false
	if strings.HasSuffix(rest, "/regenerate") {
		regenerate = true
		rest = strings.TrimSuffix(rest, "/regenerate")
	}
	if rest == "" || strings.Contains(rest, "/") {
		return "", false, false
	}
	keyID, err := url.PathUnescape(rest)
	if err != nil || strings.TrimSpace(keyID) == "" {
		return "", false, false
	}
	return strings.TrimSpace(keyID), regenerate, true
}

func (a *App) handleGetAppKey(w http.ResponseWriter, _ *http.Request, keyID string) {
	key, ok := a.findAppKey(keyID)
	if !ok {
		writeError(w, http.StatusNotFound, fmt.Sprintf("app key %q not found", keyID), "not_found")
		return
	}
	writeJSON(w, http.StatusOK, adminAppKeyFromEntry(key))
}

func (a *App) handleCreateAppKey(w http.ResponseWriter, r *http.Request) {
	var payload adminAppKeyPayload
	if !decodeJSONPayload(w, r, &payload, false) {
		return
	}
	keyID := strings.TrimSpace(payload.KeyID)
	if keyID == "" {
		keyID = "key-" + uuid.NewString()
	}
	if _, ok := a.findAppKey(keyID); ok {
		writeError(w, http.StatusConflict, fmt.Sprintf("app key %q already exists", keyID), "duplicate_key")
		return
	}
	secret := ""
	if payload.Secret != nil {
		secret = strings.TrimSpace(*payload.Secret)
	}
	if secret == "" {
		generated, err := generateAppKeySecret()
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error(), "secret_generation_failed")
			return
		}
		secret = generated
	}
	enabled := true
	if payload.Enabled != nil {
		enabled = *payload.Enabled
	}
	entry := config.AppKeyEntry{
		KeyID:   keyID,
		Secret:  secret,
		Enabled: &enabled,
	}
	applyAppKeyPayload(&entry, payload, false)
	appKeys := cloneAppKeysConfig(a.Config.AppKeys)
	appKeys.Keys = append(appKeys.Keys, entry)
	if !a.applyAppKeysOrWrite(w, appKeys) {
		return
	}
	created, _ := a.findAppKey(keyID)
	writeJSON(w, http.StatusOK, map[string]any{
		"status":  "ok",
		"key_id":  keyID,
		"secret":  secret,
		"key":     adminAppKeyFromEntry(created),
		"message": "Store this secret securely - it will not be shown again",
	})
}

func (a *App) handleUpdateAppKey(w http.ResponseWriter, r *http.Request, keyID string) {
	var payload adminAppKeyPayload
	if !decodeJSONPayload(w, r, &payload, false) {
		return
	}
	if incomingID := strings.TrimSpace(payload.KeyID); incomingID != "" && incomingID != keyID {
		writeError(w, http.StatusBadRequest, "key_id cannot be changed", "invalid_request_error")
		return
	}
	index, entry, ok := a.findAppKeyIndex(keyID)
	if !ok {
		writeError(w, http.StatusNotFound, fmt.Sprintf("app key %q not found", keyID), "not_found")
		return
	}
	if strings.TrimSpace(entry.KeyID) == "" {
		entry.KeyID = keyID
	}
	if strings.TrimSpace(entry.GroupID) == "" {
		entry.GroupID = config.AppKeyEntry{Secret: entry.Secret}.EffectiveGroupID()
	}
	applyAppKeyPayload(&entry, payload, true)
	appKeys := cloneAppKeysConfig(a.Config.AppKeys)
	appKeys.Keys[index] = entry
	if !a.applyAppKeysOrWrite(w, appKeys) {
		return
	}
	updated, _ := a.findAppKey(keyID)
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "key": adminAppKeyFromEntry(updated)})
}

func (a *App) handleDeleteAppKey(w http.ResponseWriter, _ *http.Request, keyID string) {
	index, _, ok := a.findAppKeyIndex(keyID)
	if !ok {
		writeError(w, http.StatusNotFound, fmt.Sprintf("app key %q not found", keyID), "not_found")
		return
	}
	appKeys := cloneAppKeysConfig(a.Config.AppKeys)
	appKeys.Keys = append(appKeys.Keys[:index], appKeys.Keys[index+1:]...)
	if !a.applyAppKeysOrWrite(w, appKeys) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "key_id": keyID})
}

func (a *App) handleRegenerateAppKey(w http.ResponseWriter, _ *http.Request, keyID string) {
	index, entry, ok := a.findAppKeyIndex(keyID)
	if !ok {
		writeError(w, http.StatusNotFound, fmt.Sprintf("app key %q not found", keyID), "not_found")
		return
	}
	secret, err := generateAppKeySecret()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error(), "secret_generation_failed")
		return
	}
	if strings.TrimSpace(entry.KeyID) == "" {
		entry.KeyID = keyID
	}
	if strings.TrimSpace(entry.GroupID) == "" {
		entry.GroupID = config.AppKeyEntry{Secret: entry.Secret}.EffectiveGroupID()
	}
	entry.Secret = secret
	appKeys := cloneAppKeysConfig(a.Config.AppKeys)
	appKeys.Keys[index] = entry
	if !a.applyAppKeysOrWrite(w, appKeys) {
		return
	}
	updated, _ := a.findAppKey(keyID)
	writeJSON(w, http.StatusOK, map[string]any{
		"status":  "ok",
		"key_id":  keyID,
		"secret":  secret,
		"key":     adminAppKeyFromEntry(updated),
		"message": "Store this secret securely - it will not be shown again",
	})
}

func (a *App) handleAppKeysConfig(w http.ResponseWriter, r *http.Request) {
	var payload adminAppKeysConfigPayload
	if !decodeJSONPayload(w, r, &payload, false) {
		return
	}
	appKeys := cloneAppKeysConfig(a.Config.AppKeys)
	if payload.Enabled != nil {
		appKeys.Enabled = *payload.Enabled
	}
	if payload.HeaderName != nil {
		appKeys.HeaderName = strings.TrimSpace(*payload.HeaderName)
	}
	if payload.AllowUnauthenticated != nil {
		appKeys.AllowUnauthenticated = *payload.AllowUnauthenticated
	}
	if payload.RequiredHeaders != nil {
		appKeys.RequiredHeaders = cloneRequiredHeaders(*payload.RequiredHeaders)
	}
	if payload.Defaults != nil {
		appKeys.Defaults = *payload.Defaults
	}
	if payload.Unauthenticated != nil {
		appKeys.Unauthenticated = *payload.Unauthenticated
	}
	if payload.Groups != nil {
		appKeys.Groups = *payload.Groups
	}
	if !a.applyAppKeysOrWrite(w, appKeys) {
		return
	}
	writeJSON(w, http.StatusOK, a.adminAppKeysResponse("ok"))
}

func decodeJSONPayload(w http.ResponseWriter, r *http.Request, target any, allowEmpty bool) bool {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeError(w, http.StatusBadRequest, "failed to read request body", "invalid_request_error")
		return false
	}
	trimmed := strings.TrimSpace(string(body))
	if trimmed == "" {
		if allowEmpty {
			return true
		}
		writeError(w, http.StatusBadRequest, "Request body must be a JSON object", "invalid_json")
		return false
	}
	if !strings.HasPrefix(trimmed, "{") {
		writeError(w, http.StatusBadRequest, "Request body must be a JSON object", "invalid_json")
		return false
	}
	if err := json.Unmarshal(body, target); err != nil {
		writeError(w, http.StatusBadRequest, "Invalid JSON payload", "invalid_json")
		return false
	}
	return true
}

func applyAppKeyPayload(entry *config.AppKeyEntry, payload adminAppKeyPayload, preserveSecret bool) {
	if payload.GroupID != nil {
		entry.GroupID = strings.TrimSpace(*payload.GroupID)
	}
	if payload.Name != nil {
		entry.Name = strings.TrimSpace(*payload.Name)
	}
	if payload.Description != nil {
		entry.Description = strings.TrimSpace(*payload.Description)
	}
	if payload.Enabled != nil {
		entry.Enabled = payload.Enabled
	}
	if payload.Models != nil {
		entry.Models = append([]string(nil), (*payload.Models)...)
	}
	if payload.ConcurrencyLimit != nil {
		entry.ConcurrencyLimit = *payload.ConcurrencyLimit
	}
	if payload.Priority != nil {
		entry.Priority = *payload.Priority
	}
	if payload.QueueTimeout != nil {
		entry.QueueTimeout = *payload.QueueTimeout
	}
	if payload.Limits != nil {
		entry.Limits = *payload.Limits
	}
	if payload.RequiredHeaders != nil {
		entry.RequiredHeaders = cloneRequiredHeaders(*payload.RequiredHeaders)
	}
	if !preserveSecret && payload.Secret != nil {
		entry.Secret = strings.TrimSpace(*payload.Secret)
	}
}

func (a *App) applyAppKeysOrWrite(w http.ResponseWriter, appKeys config.AppKeysConfig) bool {
	raw := configMutationRaw(a.Config)
	raw["app_keys"] = appKeys
	newCfg, issues := a.validateConfig(raw)
	if hasValidationErrors(issues) {
		writeConfigValidation(w, http.StatusBadRequest, issues)
		return false
	}
	if err := a.applyConfig(newCfg, true); err != nil {
		writeError(w, http.StatusBadRequest, err.Error(), "config_apply_failed")
		return false
	}
	return true
}

func configMutationRaw(cfg *config.Config) map[string]any {
	if cfg == nil {
		return map[string]any{}
	}
	rawSource := cfg.Raw
	if strings.TrimSpace(cfg.Path) != "" {
		if diskCfg, err := config.LoadWithOptions(config.LoadOptions{Path: cfg.Path, SubstituteEnv: false}); err == nil && len(diskCfg.Raw) > 0 {
			rawSource = diskCfg.Raw
		}
	}
	if len(rawSource) == 0 {
		return cfg.ToMap()
	}
	raw := map[string]any{}
	buf, err := json.Marshal(rawSource)
	if err != nil || json.Unmarshal(buf, &raw) != nil {
		return cfg.ToMap()
	}
	return raw
}

func (a *App) adminAppKeysResponse(status string) adminAppKeysResponse {
	cfg := a.Config.AppKeys
	keys := make([]adminAppKeyResponse, 0, len(cfg.Keys))
	for _, key := range cfg.Keys {
		keys = append(keys, adminAppKeyFromEntry(key))
	}
	headerName := strings.TrimSpace(cfg.HeaderName)
	if headerName == "" {
		headerName = config.DefaultAppKeyHeader
	}
	return adminAppKeysResponse{
		Status:               status,
		Enabled:              cfg.Enabled,
		HeaderName:           headerName,
		AllowUnauthenticated: cfg.AllowUnauthenticated,
		RequiredHeaders:      cloneRequiredHeaders(cfg.RequiredHeaders),
		Defaults:             cfg.Defaults,
		Unauthenticated:      cfg.Unauthenticated,
		Groups:               cfg.Groups,
		Keys:                 keys,
	}
}

func adminAppKeyFromEntry(entry config.AppKeyEntry) adminAppKeyResponse {
	return adminAppKeyResponse{
		KeyID:            entry.EffectiveKeyID(),
		GroupID:          entry.EffectiveGroupID(),
		Name:             entry.Name,
		Description:      entry.Description,
		Enabled:          entry.Enabled == nil || *entry.Enabled,
		Models:           append([]string(nil), entry.Models...),
		ConcurrencyLimit: entry.ConcurrencyLimit,
		Priority:         entry.Priority,
		QueueTimeout:     entry.QueueTimeout,
		Limits:           entry.Limits,
		RequiredHeaders:  cloneRequiredHeaders(entry.RequiredHeaders),
		HasSecret:        strings.TrimSpace(entry.Secret) != "",
	}
}

func cloneAppKeysConfig(in config.AppKeysConfig) config.AppKeysConfig {
	out := in
	out.RequiredHeaders = cloneRequiredHeaders(in.RequiredHeaders)
	out.Groups = append([]config.AppKeyGroupEntry(nil), in.Groups...)
	for i := range out.Groups {
		out.Groups[i].Models = append([]string(nil), in.Groups[i].Models...)
		out.Groups[i].RequiredHeaders = cloneRequiredHeaders(in.Groups[i].RequiredHeaders)
		if in.Groups[i].Enabled != nil {
			enabled := *in.Groups[i].Enabled
			out.Groups[i].Enabled = &enabled
		}
	}
	out.Keys = append([]config.AppKeyEntry(nil), in.Keys...)
	for i := range out.Keys {
		out.Keys[i].Models = append([]string(nil), in.Keys[i].Models...)
		out.Keys[i].RequiredHeaders = cloneRequiredHeaders(in.Keys[i].RequiredHeaders)
		if in.Keys[i].Enabled != nil {
			enabled := *in.Keys[i].Enabled
			out.Keys[i].Enabled = &enabled
		}
	}
	return out
}

func cloneRequiredHeaders(in []config.RequiredHeader) []config.RequiredHeader {
	if len(in) == 0 {
		return nil
	}
	out := make([]config.RequiredHeader, len(in))
	copy(out, in)
	for i := range out {
		out[i].Models = append([]string(nil), in[i].Models...)
	}
	return out
}

func (a *App) findAppKey(keyID string) (config.AppKeyEntry, bool) {
	_, entry, ok := a.findAppKeyIndex(keyID)
	return entry, ok
}

func (a *App) findAppKeyIndex(keyID string) (int, config.AppKeyEntry, bool) {
	keyID = strings.TrimSpace(keyID)
	if keyID == "" || a == nil || a.Config == nil {
		return -1, config.AppKeyEntry{}, false
	}
	for i, key := range a.Config.AppKeys.Keys {
		if key.EffectiveKeyID() == keyID {
			return i, key, true
		}
	}
	return -1, config.AppKeyEntry{}, false
}

func generateAppKeySecret() (string, error) {
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return "yallmp_sk_" + base64.RawURLEncoding.EncodeToString(raw[:]), nil
}
