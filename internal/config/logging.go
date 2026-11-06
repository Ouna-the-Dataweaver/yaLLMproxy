package config

// IsEnabled returns the default-on master logging switch. An explicit false
// suppresses request, error, payload, and console logs regardless of sink flags.
func (c LoggingConfig) IsEnabled() bool {
	return c.Enabled == nil || ParseBoolStrict(c.Enabled)
}
