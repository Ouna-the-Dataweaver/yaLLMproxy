package config

import "testing"

func TestLoggingMasterSwitchDefaultAndValidation(t *testing.T) {
	for _, tc := range []struct {
		value   any
		enabled bool
	}{{nil, true}, {true, true}, {"true", true}, {false, false}, {"false", false}, {0, false}, {"off", false}} {
		if got := (LoggingConfig{Enabled: tc.value}).IsEnabled(); got != tc.enabled {
			t.Errorf("enabled=%v: got %v, want %v", tc.value, got, tc.enabled)
		}
	}
	cfg := &Config{ProxySettings: ProxySettings{Logging: LoggingConfig{Enabled: "invalid"}}}
	found := false
	for _, issue := range cfg.Validate() {
		if issue.Path == "proxy_settings.logging.enabled" && issue.Severity == "error" {
			found = true
		}
	}
	if !found {
		t.Fatal("invalid master logging switch accepted")
	}
}
