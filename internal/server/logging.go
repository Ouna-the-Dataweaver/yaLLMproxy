package server

import "github.com/theio/yallmp-proxy/internal/config"

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
