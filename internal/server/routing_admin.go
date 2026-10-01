package server

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/theio/yallmp-proxy/internal/config"
)

func isAdminProvidersPath(path string) bool {
	return path == "/admin/providers" || strings.HasPrefix(path, "/admin/providers/")
}

func isAdminNodesPath(path string) bool {
	return path == "/admin/nodes" || strings.HasPrefix(path, "/admin/nodes/")
}

func isAdminClientModelsPath(path string) bool {
	return path == "/admin/client-models" || strings.HasPrefix(path, "/admin/client-models/")
}

func isAdminRouterSettingsPath(path string) bool {
	return path == "/admin/router-settings"
}

func (a *App) handleAdminRouterSettings(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, map[string]any{"fallbacks": config.MaskSensitive(a.Config.RawRouterFallbacks())})
	case http.MethodPut:
		a.handleUpdateRouterSettings(w, r)
	default:
		writeError(w, http.StatusNotFound, "not found", "not_found")
	}
}

func (a *App) handleUpdateRouterSettings(w http.ResponseWriter, r *http.Request) {
	if !a.requireAdminPassword(w, r, "router settings management") {
		return
	}
	payload, ok := adminRawPayload(w, r)
	if !ok {
		return
	}
	raw := configMutationRaw(a.Config)
	candidate, err := config.FromMap(raw, a.Config.Path)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error(), "invalid_config")
		return
	}
	fallbacks, err := candidate.ReplaceRouterFallbacks(payload["fallbacks"])
	if err != nil {
		a.writeAdminRoutingMutationError(w, err, "router settings", "")
		return
	}
	if !a.applyAdminRawMutation(w, candidate.Raw) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "fallbacks": config.MaskSensitive(fallbacks)})
}

func (a *App) handleAdminProviders(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.URL.Path == "/admin/providers" && r.Method == http.MethodGet:
		writeJSON(w, http.StatusOK, map[string]any{"providers": a.Config.AdminProviders()})
	case r.URL.Path == "/admin/providers" && r.Method == http.MethodPost:
		a.handleCreateProvider(w, r)
	case strings.HasPrefix(r.URL.Path, "/admin/providers/"):
		name, ok := adminRawResourcePathName(r.URL.Path, "/admin/providers")
		if !ok {
			writeError(w, http.StatusNotFound, "not found", "not_found")
			return
		}
		a.handleProviderItem(w, r, name)
	default:
		writeError(w, http.StatusNotFound, "not found", "not_found")
	}
}

func (a *App) handleProviderItem(w http.ResponseWriter, r *http.Request, name string) {
	switch r.Method {
	case http.MethodGet:
		entry, ok := a.Config.RawProvider(name)
		if !ok {
			writeError(w, http.StatusNotFound, fmt.Sprintf("provider %q not found", name), "provider_not_found")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"provider": config.MaskSensitive(entry)})
	case http.MethodPut:
		a.handleUpdateProvider(w, r, name)
	case http.MethodDelete:
		a.handleDeleteProvider(w, r, name)
	default:
		writeError(w, http.StatusNotFound, "not found", "not_found")
	}
}

func (a *App) handleCreateProvider(w http.ResponseWriter, r *http.Request) {
	if !a.requireAdminPassword(w, r, "provider management") {
		return
	}
	payload, ok := adminRawPayload(w, r)
	if !ok {
		return
	}
	raw := configMutationRaw(a.Config)
	candidate, err := config.FromMap(raw, a.Config.Path)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error(), "invalid_config")
		return
	}
	entry, err := candidate.CreateRawProvider(payload)
	if err != nil {
		a.writeAdminRoutingMutationError(w, err, "provider", "")
		return
	}
	if !a.applyAdminRawMutation(w, candidate.Raw) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "provider": config.MaskSensitive(entry), "created": true})
}

func (a *App) handleUpdateProvider(w http.ResponseWriter, r *http.Request, name string) {
	if !a.requireAdminPassword(w, r, "provider management") {
		return
	}
	payload, ok := adminRawPayload(w, r)
	if !ok {
		return
	}
	raw := configMutationRaw(a.Config)
	candidate, err := config.FromMap(raw, a.Config.Path)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error(), "invalid_config")
		return
	}
	entry, err := candidate.UpdateRawProvider(name, payload)
	if err != nil {
		a.writeAdminRoutingMutationError(w, err, "provider", name)
		return
	}
	if !a.applyAdminRawMutation(w, candidate.Raw) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "provider": config.MaskSensitive(entry)})
}

func (a *App) handleDeleteProvider(w http.ResponseWriter, r *http.Request, name string) {
	if !a.requireAdminPassword(w, r, "provider management") {
		return
	}
	raw := configMutationRaw(a.Config)
	candidate, err := config.FromMap(raw, a.Config.Path)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error(), "invalid_config")
		return
	}
	if err := candidate.DeleteRawProvider(name); err != nil {
		a.writeAdminRoutingMutationError(w, err, "provider", name)
		return
	}
	if !a.applyAdminRawMutation(w, candidate.Raw) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "provider": name})
}

func (a *App) handleAdminNodes(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.URL.Path == "/admin/nodes" && r.Method == http.MethodGet:
		writeJSON(w, http.StatusOK, map[string]any{"nodes": a.Config.AdminNodes()})
	case r.URL.Path == "/admin/nodes" && r.Method == http.MethodPost:
		a.handleCreateNode(w, r)
	case strings.HasPrefix(r.URL.Path, "/admin/nodes/"):
		name, ok := adminRawResourcePathName(r.URL.Path, "/admin/nodes")
		if !ok {
			writeError(w, http.StatusNotFound, "not found", "not_found")
			return
		}
		a.handleNodeItem(w, r, name)
	default:
		writeError(w, http.StatusNotFound, "not found", "not_found")
	}
}

func (a *App) handleNodeItem(w http.ResponseWriter, r *http.Request, name string) {
	switch r.Method {
	case http.MethodGet:
		entry, ok := a.Config.RawNode(name)
		if !ok {
			writeError(w, http.StatusNotFound, fmt.Sprintf("node %q not found", name), "node_not_found")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"node": config.MaskSensitive(entry)})
	case http.MethodPut:
		a.handleUpdateNode(w, r, name)
	case http.MethodDelete:
		a.handleDeleteNode(w, r, name)
	default:
		writeError(w, http.StatusNotFound, "not found", "not_found")
	}
}

func (a *App) handleCreateNode(w http.ResponseWriter, r *http.Request) {
	if !a.requireAdminPassword(w, r, "node management") {
		return
	}
	payload, ok := adminRawPayload(w, r)
	if !ok {
		return
	}
	raw := configMutationRaw(a.Config)
	candidate, err := config.FromMap(raw, a.Config.Path)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error(), "invalid_config")
		return
	}
	entry, err := candidate.CreateRawNode(payload)
	if err != nil {
		a.writeAdminRoutingMutationError(w, err, "node", "")
		return
	}
	if !a.applyAdminRawMutation(w, candidate.Raw) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "node": config.MaskSensitive(entry), "created": true})
}

func (a *App) handleUpdateNode(w http.ResponseWriter, r *http.Request, name string) {
	if !a.requireAdminPassword(w, r, "node management") {
		return
	}
	payload, ok := adminRawPayload(w, r)
	if !ok {
		return
	}
	raw := configMutationRaw(a.Config)
	candidate, err := config.FromMap(raw, a.Config.Path)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error(), "invalid_config")
		return
	}
	entry, err := candidate.UpdateRawNode(name, payload)
	if err != nil {
		a.writeAdminRoutingMutationError(w, err, "node", name)
		return
	}
	if !a.applyAdminRawMutation(w, candidate.Raw) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "node": config.MaskSensitive(entry)})
}

func (a *App) handleDeleteNode(w http.ResponseWriter, r *http.Request, name string) {
	if !a.requireAdminPassword(w, r, "node management") {
		return
	}
	raw := configMutationRaw(a.Config)
	candidate, err := config.FromMap(raw, a.Config.Path)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error(), "invalid_config")
		return
	}
	if err := candidate.DeleteRawNode(name); err != nil {
		a.writeAdminRoutingMutationError(w, err, "node", name)
		return
	}
	if !a.applyAdminRawMutation(w, candidate.Raw) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "node": name})
}

func (a *App) handleAdminClientModels(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.URL.Path == "/admin/client-models" && r.Method == http.MethodGet:
		writeJSON(w, http.StatusOK, map[string]any{"client_models": a.Config.AdminClientModels()})
	case r.URL.Path == "/admin/client-models" && r.Method == http.MethodPost:
		a.handleCreateClientModel(w, r)
	case strings.HasPrefix(r.URL.Path, "/admin/client-models/"):
		name, ok := adminRawResourcePathName(r.URL.Path, "/admin/client-models")
		if !ok {
			writeError(w, http.StatusNotFound, "not found", "not_found")
			return
		}
		a.handleClientModelItem(w, r, name)
	default:
		writeError(w, http.StatusNotFound, "not found", "not_found")
	}
}

func (a *App) handleClientModelItem(w http.ResponseWriter, r *http.Request, name string) {
	switch r.Method {
	case http.MethodGet:
		entry, ok := a.Config.RawClientModel(name)
		if !ok {
			writeError(w, http.StatusNotFound, fmt.Sprintf("client model %q not found", name), "client_model_not_found")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"client_model": config.MaskSensitive(entry)})
	case http.MethodPut:
		a.handleUpdateClientModel(w, r, name)
	case http.MethodDelete:
		a.handleDeleteClientModel(w, r, name)
	default:
		writeError(w, http.StatusNotFound, "not found", "not_found")
	}
}

func (a *App) handleCreateClientModel(w http.ResponseWriter, r *http.Request) {
	if !a.requireAdminPassword(w, r, "client model management") {
		return
	}
	payload, ok := adminRawPayload(w, r)
	if !ok {
		return
	}
	raw := configMutationRaw(a.Config)
	candidate, err := config.FromMap(raw, a.Config.Path)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error(), "invalid_config")
		return
	}
	entry, err := candidate.CreateRawClientModel(payload)
	if err != nil {
		a.writeAdminRoutingMutationError(w, err, "client model", "")
		return
	}
	if !a.applyAdminRawMutation(w, candidate.Raw) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "client_model": config.MaskSensitive(entry), "created": true})
}

func (a *App) handleUpdateClientModel(w http.ResponseWriter, r *http.Request, name string) {
	if !a.requireAdminPassword(w, r, "client model management") {
		return
	}
	payload, ok := adminRawPayload(w, r)
	if !ok {
		return
	}
	raw := configMutationRaw(a.Config)
	candidate, err := config.FromMap(raw, a.Config.Path)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error(), "invalid_config")
		return
	}
	entry, err := candidate.UpdateRawClientModel(name, payload)
	if err != nil {
		a.writeAdminRoutingMutationError(w, err, "client model", name)
		return
	}
	if !a.applyAdminRawMutation(w, candidate.Raw) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "client_model": config.MaskSensitive(entry)})
}

func (a *App) handleDeleteClientModel(w http.ResponseWriter, r *http.Request, name string) {
	if !a.requireAdminPassword(w, r, "client model management") {
		return
	}
	raw := configMutationRaw(a.Config)
	candidate, err := config.FromMap(raw, a.Config.Path)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error(), "invalid_config")
		return
	}
	if err := candidate.DeleteRawClientModel(name); err != nil {
		a.writeAdminRoutingMutationError(w, err, "client model", name)
		return
	}
	if !a.applyAdminRawMutation(w, candidate.Raw) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "client_model": name})
}

func (a *App) requireAdminPassword(w http.ResponseWriter, r *http.Request, subject string) bool {
	if a.hasAdminPassword(r) {
		return true
	}
	writeError(w, http.StatusForbidden, "admin password required for "+subject, "permission_error")
	return false
}

func adminRawPayload(w http.ResponseWriter, r *http.Request) (map[string]any, bool) {
	var payload map[string]any
	if !decodeJSONPayload(w, r, &payload, false) {
		return nil, false
	}
	if payload == nil {
		payload = map[string]any{}
	}
	return payload, true
}

func (a *App) applyAdminRawMutation(w http.ResponseWriter, raw map[string]any) bool {
	newCfg, issues := a.validateConfig(raw)
	if hasValidationErrors(issues) {
		writeConfigValidation(w, http.StatusBadRequest, issues)
		return false
	}
	if err := a.applyConfigLocked(newCfg, true); err != nil {
		writeError(w, http.StatusBadRequest, err.Error(), "config_apply_failed")
		return false
	}
	return true
}

func (a *App) writeAdminRoutingMutationError(w http.ResponseWriter, err error, resourceType string, name string) {
	switch {
	case errors.Is(err, config.ErrAdminResourceNameRequired):
		writeError(w, http.StatusBadRequest, resourceType+" name is required", "missing_parameter")
	case errors.Is(err, config.ErrAdminResourceNameMismatch):
		writeError(w, http.StatusBadRequest, resourceType+" name cannot be changed", "name_mismatch")
	case errors.Is(err, config.ErrProviderNotFound):
		writeError(w, http.StatusNotFound, fmt.Sprintf("provider %q not found", name), "provider_not_found")
	case errors.Is(err, config.ErrProviderDuplicate):
		writeError(w, http.StatusConflict, "provider already exists", "duplicate_provider")
	case errors.Is(err, config.ErrProviderHasNodes):
		a.writeAdminReferenceConflict(w, err, "provider_has_nodes", "nodes")
	case errors.Is(err, config.ErrNodeNotFound):
		writeError(w, http.StatusNotFound, fmt.Sprintf("node %q not found", name), "node_not_found")
	case errors.Is(err, config.ErrNodeDuplicate):
		writeError(w, http.StatusConflict, "node already exists", "duplicate_node")
	case errors.Is(err, config.ErrNodeHasClientModels):
		a.writeAdminReferenceConflict(w, err, "node_has_client_models", "client_models")
	case errors.Is(err, config.ErrClientModelNotFound):
		writeError(w, http.StatusNotFound, fmt.Sprintf("client model %q not found", name), "client_model_not_found")
	case errors.Is(err, config.ErrClientModelDuplicate):
		writeError(w, http.StatusConflict, "client model already exists", "duplicate_client_model")
	case errors.Is(err, config.ErrInvalidFallbacks):
		writeError(w, http.StatusBadRequest, err.Error(), "invalid_fallbacks")
	default:
		writeError(w, http.StatusBadRequest, err.Error(), "invalid_request_error")
	}
}

func (a *App) writeAdminReferenceConflict(w http.ResponseWriter, err error, code string, refsKey string) {
	var refsErr *config.AdminResourceReferencesError
	if !errors.As(err, &refsErr) {
		writeError(w, http.StatusConflict, err.Error(), code)
		return
	}
	writeJSON(w, http.StatusConflict, map[string]any{
		"error": map[string]any{
			"message":    refsErr.Error(),
			"type":       "invalid_request_error",
			"code":       code,
			"references": refsErr.References,
		},
		"resource":   refsErr.Resource,
		refsKey:      refsErr.References,
		"references": refsErr.References,
	})
}

func adminRawResourcePathName(path string, prefix string) (string, bool) {
	if !strings.HasPrefix(path, prefix+"/") {
		return "", false
	}
	rawName := strings.TrimPrefix(path, prefix+"/")
	if rawName == "" {
		return "", false
	}
	name, err := url.PathUnescape(rawName)
	if err != nil {
		return "", false
	}
	name = strings.TrimSpace(name)
	if !validAdminModelPathName(name) {
		return "", false
	}
	return name, true
}
