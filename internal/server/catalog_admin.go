package server

import (
	"net/http"
)

func (a *App) handleAdminCatalog(w http.ResponseWriter, r *http.Request) {
	if !a.requireAdminPassword(w, r, "model catalog") {
		return
	}
	catalog := a.currentCatalog()
	if catalog == nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"enabled":       false,
			"probe_enabled": false,
			"models":        []any{},
			"warnings":      []any{},
		})
		return
	}
	writeJSON(w, http.StatusOK, catalog.Snapshot().Redacted())
}
