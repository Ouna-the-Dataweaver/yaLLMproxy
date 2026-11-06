package server

import (
	"net/http"
	"strings"

	"github.com/theio/yallmp-proxy/internal/config"
)

func (a *App) loggingEnabled() bool {
	return a != nil && (a.Config == nil || a.Config.ProxySettings.Logging.IsEnabled())
}

func proxyOTelConfig(cfg *config.Config) config.OTelConfig {
	otel := cfg.ProxySettings.OTel
	if !cfg.ProxySettings.Logging.IsEnabled() {
		otel.Logs.Enabled = false
	}
	return otel
}

// payloadHeaders masks the configurable client credential in both header copies
// stored in request sidecars. The original request remains available to policy.
func (a *App) payloadHeaders(r *http.Request) (http.Header, map[string]string) {
	headers := r.Header.Clone()
	required := cloneStringMap(requiredHeaderAttribution(r))
	headerName := config.DefaultAppKeyHeader
	if a != nil && a.Config != nil && strings.TrimSpace(a.Config.AppKeys.HeaderName) != "" {
		headerName = strings.TrimSpace(a.Config.AppKeys.HeaderName)
	}
	for name := range headers {
		if strings.EqualFold(name, headerName) {
			headers[name] = []string{"[REDACTED]"}
		}
	}
	for name := range required {
		if strings.EqualFold(name, headerName) {
			required[name] = "[REDACTED]"
		}
	}
	return headers, required
}
