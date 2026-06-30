package gigachat

import (
	"fmt"
	"strings"
	"time"
)

const (
	DefaultAuthURL   = "https://ngw.devices.sberbank.ru:9443/api/v2/oauth"
	DefaultBaseURL   = "https://gigachat.devices.sberbank.ru/api/v1"
	DefaultModel     = "GigaChat-2-Max"
	DefaultScope     = "GIGACHAT_API_CORP"
	DefaultTimeout   = 120 * time.Second
	DefaultVerifySSL = true
)

type Config struct {
	Mode             string
	ModelName        string
	BaseURL          string
	AuthURL          string
	Scope            string
	APIKey           string
	ClientCertFile   string
	ClientKeyFile    string
	VerifySSL        bool
	Timeout          time.Duration
	EmulateToolCalls bool
}

func BuildConfig(params map[string]any) (Config, error) {
	if params == nil {
		params = map[string]any{}
	}
	apiKey := stringOrEmpty(params["api_key"])
	scope := firstNonEmpty(stringOrEmpty(params["scope"]), DefaultScope)
	clientCert := stringOrEmpty(params["client_cert"])
	clientKey := stringOrEmpty(params["client_key"])
	mode, err := resolveMode(stringOrEmpty(params["mode"]), apiKey, scope, clientCert, clientKey)
	if err != nil {
		return Config{}, err
	}
	cfg := Config{
		Mode:             mode,
		ModelName:        firstNonEmpty(stringOrEmpty(params["model"]), DefaultModel),
		BaseURL:          firstNonEmpty(stringOrEmpty(params["api_base"]), DefaultBaseURL),
		AuthURL:          firstNonEmpty(stringOrEmpty(params["auth_url"]), DefaultAuthURL),
		Scope:            scope,
		APIKey:           apiKey,
		ClientCertFile:   clientCert,
		ClientKeyFile:    clientKey,
		VerifySSL:        parseBool(params["verify_ssl"], DefaultVerifySSL),
		Timeout:          durationOrDefault(params["request_timeout"], DefaultTimeout),
		EmulateToolCalls: parseBool(params["emulate_tool_calls"], false),
	}
	if err := validateConfig(cfg); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func resolveMode(modeValue string, apiKey string, scope string, clientCert string, clientKey string) (string, error) {
	if modeValue != "" {
		normalized := strings.ToLower(strings.TrimSpace(modeValue))
		switch normalized {
		case "cloud", "local":
			return normalized, nil
		default:
			return "", fmt.Errorf("GigaChat mode must be either 'cloud' or 'local'")
		}
	}
	hasCloud := apiKey != "" && scope != ""
	hasLocal := clientCert != "" && clientKey != ""
	if hasCloud && hasLocal {
		return "", fmt.Errorf("both GigaChat cloud and local credentials are configured; set mode to either 'cloud' or 'local'")
	}
	if hasLocal {
		return "local", nil
	}
	if hasCloud {
		return "cloud", nil
	}
	return "", fmt.Errorf("GigaChat credentials are not configured; for cloud mode set api_key and scope; for local mode set client_cert and client_key")
}

func validateConfig(cfg Config) error {
	var missing []string
	switch cfg.Mode {
	case "cloud":
		if cfg.APIKey == "" {
			missing = append(missing, "api_key")
		}
		if cfg.Scope == "" {
			missing = append(missing, "scope")
		}
		if cfg.AuthURL == "" {
			missing = append(missing, "auth_url")
		}
	case "local":
		if cfg.ClientCertFile == "" {
			missing = append(missing, "client_cert")
		}
		if cfg.ClientKeyFile == "" {
			missing = append(missing, "client_key")
		}
	default:
		missing = append(missing, "mode")
	}
	if cfg.BaseURL == "" {
		missing = append(missing, "api_base")
	}
	if len(missing) > 0 {
		return fmt.Errorf("GigaChat %s mode is missing required parameters: %s", cfg.Mode, strings.Join(missing, ", "))
	}
	return nil
}

func stringOrEmpty(value any) string {
	if value == nil {
		return ""
	}
	text := strings.TrimSpace(fmt.Sprint(value))
	if text == "<nil>" {
		return ""
	}
	return text
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func parseBool(value any, defaultValue bool) bool {
	if value == nil {
		return defaultValue
	}
	switch typed := value.(type) {
	case bool:
		return typed
	case string:
		switch strings.ToLower(strings.TrimSpace(typed)) {
		case "", "0", "false", "no", "off":
			return false
		default:
			return true
		}
	default:
		return fmt.Sprint(value) != "" && fmt.Sprint(value) != "0" && fmt.Sprint(value) != "false"
	}
}

func durationOrDefault(value any, defaultValue time.Duration) time.Duration {
	if value == nil {
		return defaultValue
	}
	switch typed := value.(type) {
	case int:
		return time.Duration(typed) * time.Second
	case int64:
		return time.Duration(typed) * time.Second
	case float64:
		return time.Duration(typed * float64(time.Second))
	case string:
		text := strings.TrimSpace(typed)
		if text == "" {
			return defaultValue
		}
		if dur, err := time.ParseDuration(text); err == nil {
			return dur
		}
		var seconds float64
		if _, err := fmt.Sscanf(text, "%f", &seconds); err == nil {
			return time.Duration(seconds * float64(time.Second))
		}
	}
	return defaultValue
}
