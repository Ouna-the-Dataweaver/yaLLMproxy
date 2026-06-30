package server

import (
	"net/http"
	httppprof "net/http/pprof"
	runtimepprof "runtime/pprof"
	"strings"

	"github.com/theio/yallmp-proxy/internal/config"
)

func (a *App) profilingEnabled() bool {
	if a == nil || a.Config == nil {
		return false
	}
	return config.ParseBoolStrict(a.Config.ProxySettings.Profiling.Enabled)
}

func isPprofPath(path string) bool {
	return path == "/debug/pprof" || path == "/debug/pprof/" || strings.HasPrefix(path, "/debug/pprof/")
}

func (a *App) handlePprof(w http.ResponseWriter, r *http.Request) {
	if !a.profilingEnabled() {
		writeError(w, http.StatusNotFound, "not found", "not_found")
		return
	}
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed", "method_not_allowed")
		return
	}
	if !a.hasAdminPassword(r) {
		writeError(w, http.StatusForbidden, "admin password required for profiling", "permission_error")
		return
	}

	switch r.URL.Path {
	case "/debug/pprof":
		http.Redirect(w, r, "/debug/pprof/", http.StatusMovedPermanently)
	case "/debug/pprof/":
		httppprof.Index(w, r)
	case "/debug/pprof/cmdline":
		httppprof.Cmdline(w, r)
	case "/debug/pprof/profile":
		httppprof.Profile(w, r)
	case "/debug/pprof/symbol":
		httppprof.Symbol(w, r)
	case "/debug/pprof/trace":
		httppprof.Trace(w, r)
	default:
		if isRuntimeProfilePath(r.URL.Path) {
			httppprof.Index(w, r)
			return
		}
		writeError(w, http.StatusNotFound, "not found", "not_found")
	}
}

func isRuntimeProfilePath(path string) bool {
	name := strings.TrimPrefix(path, "/debug/pprof/")
	if name == path || name == "" || strings.Contains(name, "/") {
		return false
	}
	return runtimepprof.Lookup(name) != nil
}
