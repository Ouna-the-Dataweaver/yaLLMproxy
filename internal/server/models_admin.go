package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/theio/yallmp-proxy/internal/config"
)

type adminModelCopyPayload struct {
	Source string `json:"source"`
	Target string `json:"target"`
}

func isAdminModelsReadPath(path string) bool {
	if path == "/admin/models" || path == "/admin/models/tree" {
		return true
	}
	if !strings.HasPrefix(path, "/admin/models/") {
		return false
	}
	_, _, ok := adminModelReadPathParts(path)
	return ok
}

func isAdminModelsHTMLNavigation(r *http.Request) bool {
	return r != nil && r.URL.Path == "/admin/models" && acceptsHTMLNavigation(r.Header.Get("Accept"))
}

func isAdminModelDeletePath(path string) bool {
	_, ok := adminModelDeletePathName(path)
	return ok
}

func acceptsHTMLNavigation(accept string) bool {
	hasHTML := false
	hasJSON := false
	for _, part := range strings.Split(accept, ",") {
		media, q := acceptMediaAndQ(part)
		if media == "" || q == "0" || q == "0.0" || q == "0.00" {
			continue
		}
		if media == "text/html" {
			hasHTML = true
			continue
		}
		if media == "application/json" || strings.HasSuffix(media, "+json") {
			hasJSON = true
		}
	}
	return hasHTML && !hasJSON
}

func acceptMediaAndQ(part string) (string, string) {
	pieces := strings.Split(part, ";")
	media := strings.ToLower(strings.TrimSpace(pieces[0]))
	q := ""
	for _, piece := range pieces[1:] {
		key, value, ok := strings.Cut(strings.TrimSpace(piece), "=")
		if ok && strings.EqualFold(strings.TrimSpace(key), "q") {
			q = strings.TrimSpace(value)
		}
	}
	return media, q
}

func (a *App) handleAdminModelCopy(w http.ResponseWriter, r *http.Request) {
	source, target, ok := adminModelCopyRequest(w, r)
	if !ok {
		return
	}
	if !validAdminModelPathName(target) {
		writeError(w, http.StatusBadRequest, "target model name is invalid", "invalid_request_error")
		return
	}
	entry, found := a.Config.FindLegacyModel(source)
	if !found {
		writeError(w, http.StatusNotFound, fmt.Sprintf("model %q not found", source), "model_not_found")
		return
	}
	if config.NormalizeProtected(entry.Protected, true) && !a.hasAdminPassword(r) {
		writeError(w, http.StatusForbidden, "admin password required for protected model", "permission_error")
		return
	}
	if _, exists := a.Config.FindLegacyModel(target); exists {
		writeError(w, http.StatusConflict, fmt.Sprintf("model %q already exists", target), "duplicate_model")
		return
	}

	raw := configMutationRaw(a.Config)
	candidate, err := config.FromMap(raw, a.Config.Path)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error(), "invalid_config")
		return
	}
	copied, err := candidate.CopyLegacyModel(source, target)
	if err != nil {
		a.writeAdminModelMutationError(w, err, source)
		return
	}
	newCfg, issues := a.validateConfig(candidate.Raw)
	if hasValidationErrors(issues) {
		writeConfigValidation(w, http.StatusBadRequest, issues)
		return
	}
	if err := a.applyConfigLocked(newCfg, true); err != nil {
		writeError(w, http.StatusBadRequest, err.Error(), "config_apply_failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"status": "ok",
		"model":  target,
		"config": config.MaskSensitive(copied),
	})
}

func adminModelCopyRequest(w http.ResponseWriter, r *http.Request) (string, string, bool) {
	query := r.URL.Query()
	source := strings.TrimSpace(query.Get("source"))
	target := strings.TrimSpace(query.Get("target"))

	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeError(w, http.StatusBadRequest, "failed to read request body", "invalid_request_error")
		return "", "", false
	}
	if trimmed := strings.TrimSpace(string(body)); trimmed != "" {
		if !strings.HasPrefix(trimmed, "{") {
			writeError(w, http.StatusBadRequest, "Request body must be a JSON object", "invalid_json")
			return "", "", false
		}
		var payload adminModelCopyPayload
		if err := json.Unmarshal(body, &payload); err != nil {
			writeError(w, http.StatusBadRequest, "Invalid JSON payload", "invalid_json")
			return "", "", false
		}
		source = firstNonEmpty(source, payload.Source)
		target = firstNonEmpty(target, payload.Target)
	}
	source = strings.TrimSpace(source)
	target = strings.TrimSpace(target)
	if source == "" || target == "" {
		writeError(w, http.StatusBadRequest, "source and target are required", "missing_parameter")
		return "", "", false
	}
	return source, target, true
}

func (a *App) handleAdminModelDelete(w http.ResponseWriter, r *http.Request, name string) {
	entry, found := a.Config.FindLegacyModel(name)
	if !found {
		writeError(w, http.StatusNotFound, fmt.Sprintf("model %q not found", name), "model_not_found")
		return
	}
	cascade, _ := boolQuery(r, "cascade")
	direct, descendants, ok := a.Config.ModelDependents(name)
	if !ok {
		writeError(w, http.StatusNotFound, fmt.Sprintf("model %q not found", name), "model_not_found")
		return
	}
	if len(descendants) > 0 && !cascade {
		writeAdminModelDependentsConflict(w, name, direct, descendants)
		return
	}
	protected := config.NormalizeProtected(entry.Protected, true)
	if cascade {
		for _, descendant := range descendants {
			dependent, ok := a.Config.FindLegacyModel(descendant)
			if ok && config.NormalizeProtected(dependent.Protected, true) {
				protected = true
				break
			}
		}
	}
	if protected && !a.hasAdminPassword(r) {
		writeError(w, http.StatusForbidden, "admin password required for protected model", "permission_error")
		return
	}

	raw := configMutationRaw(a.Config)
	candidate, err := config.FromMap(raw, a.Config.Path)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error(), "invalid_config")
		return
	}
	removed, err := candidate.DeleteLegacyModels(name, cascade)
	if err != nil {
		a.writeAdminModelMutationError(w, err, name)
		return
	}
	newCfg, issues := a.validateConfig(candidate.Raw)
	if hasValidationErrors(issues) {
		writeConfigValidation(w, http.StatusBadRequest, issues)
		return
	}
	if err := a.applyConfigLocked(newCfg, true); err != nil {
		writeError(w, http.StatusBadRequest, err.Error(), "config_apply_failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"status":  "ok",
		"model":   name,
		"deleted": removed,
	})
}

func (a *App) writeAdminModelMutationError(w http.ResponseWriter, err error, name string) {
	switch {
	case errors.Is(err, config.ErrLegacyModelNotFound):
		writeError(w, http.StatusNotFound, fmt.Sprintf("model %q not found", name), "model_not_found")
	case errors.Is(err, config.ErrLegacyModelDuplicate):
		writeError(w, http.StatusConflict, fmt.Sprintf("model %q already exists", name), "duplicate_model")
	case errors.Is(err, config.ErrLegacyModelHasDependents):
		var dependentsErr *config.LegacyModelDependentsError
		if errors.As(err, &dependentsErr) {
			writeAdminModelDependentsConflict(w, dependentsErr.Model, dependentsErr.Direct, dependentsErr.Descendants)
			return
		}
		writeAdminModelDependentsConflict(w, name, nil, nil)
	default:
		writeError(w, http.StatusBadRequest, err.Error(), "invalid_request_error")
	}
}

func writeAdminModelDependentsConflict(w http.ResponseWriter, name string, direct []string, descendants []string) {
	writeJSON(w, http.StatusConflict, map[string]any{
		"error": map[string]any{
			"message":         fmt.Sprintf("model %q has dependent models; retry with cascade=true to delete them", name),
			"type":            "invalid_request_error",
			"code":            "model_has_dependents",
			"direct_children": direct,
			"dependents":      descendants,
			"cascade_hint":    "retry with cascade=true",
		},
		"model":           name,
		"direct_children": direct,
		"dependents":      descendants,
		"cascade":         true,
	})
}

func (a *App) handleAdminModelsRead(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.URL.Path == "/admin/models":
		writeJSON(w, http.StatusOK, a.Config.AdminModels())
	case r.URL.Path == "/admin/models/tree":
		writeJSON(w, http.StatusOK, a.Config.ModelTree())
	default:
		name, action, ok := adminModelReadPathParts(r.URL.Path)
		if !ok {
			writeError(w, http.StatusNotFound, "not found", "not_found")
			return
		}
		switch action {
		case "ancestry":
			a.handleAdminModelAncestry(w, name)
		case "dependents":
			a.handleAdminModelDependents(w, name)
		default:
			writeError(w, http.StatusNotFound, "not found", "not_found")
		}
	}
}

func (a *App) handleAdminModelAncestry(w http.ResponseWriter, name string) {
	chain, ok := a.Config.ModelAncestry(name)
	if !ok {
		writeError(w, http.StatusNotFound, fmt.Sprintf("model %q not found", name), "model_not_found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"model":             name,
		"chain":             chain,
		"inheritance_depth": len(chain) - 1,
	})
}

func (a *App) handleAdminModelDependents(w http.ResponseWriter, name string) {
	direct, descendants, ok := a.Config.ModelDependents(name)
	if !ok {
		writeError(w, http.StatusNotFound, fmt.Sprintf("model %q not found", name), "model_not_found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"model":            name,
		"direct_children":  direct,
		"all_descendants":  descendants,
		"descendant_count": len(descendants),
	})
}

func adminModelReadPathParts(path string) (string, string, bool) {
	rest := strings.TrimPrefix(path, "/admin/models/")
	for _, action := range []string{"ancestry", "dependents"} {
		suffix := "/" + action
		if !strings.HasSuffix(rest, suffix) {
			continue
		}
		rawName := strings.TrimSuffix(rest, suffix)
		if rawName == "" {
			return "", "", false
		}
		name, err := url.PathUnescape(rawName)
		if err != nil {
			return "", "", false
		}
		name = strings.TrimSpace(name)
		if !validAdminModelPathName(name) {
			return "", "", false
		}
		return name, action, true
	}
	return "", "", false
}

func adminModelDeletePathName(path string) (string, bool) {
	if !strings.HasPrefix(path, "/admin/models/") {
		return "", false
	}
	rawName := strings.TrimPrefix(path, "/admin/models/")
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

func validAdminModelPathName(name string) bool {
	if name == "" || strings.ContainsRune(name, '\x00') || strings.HasPrefix(name, "/") || strings.HasSuffix(name, "/") {
		return false
	}
	for _, segment := range strings.Split(name, "/") {
		if segment == "" || segment == "." || segment == ".." {
			return false
		}
	}
	return true
}
