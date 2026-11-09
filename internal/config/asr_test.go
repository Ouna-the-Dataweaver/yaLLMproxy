package config

import (
	"encoding/json"
	"math"
	"testing"
)

func TestInferenceConfigRoundTrip(t *testing.T) {
	raw := map[string]any{
		"providers":      []any{map[string]any{"name": "speech", "api_base": "http://localhost:9000/v1", "embedding_space": "checkpoint-v1", "realtime_path": "/custom/voice", "endpoints": []any{"transcriptions", "realtime", "voice", "systemone", "decisions"}}},
		"nodes":          []any{map[string]any{"name": "n", "provider": "speech", "model": "public", "embedding_space": "node-space", "realtime_path": "/ws/voice"}},
		"proxy_settings": map[string]any{"asr": map[string]any{"max_upload_bytes": 4096, "max_message_bytes": 512, "session_timeout_seconds": 30, "allowed_origins": []any{"app.example.com"}}},
	}
	cfg, err := FromMap(raw, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, issue := range cfg.Validate() {
		if issue.Severity == "error" {
			t.Fatalf("invalid inference configuration: %+v", issue)
		}
	}
	encoded, err := json.Marshal(cfg.ToMap())
	if err != nil {
		t.Fatal(err)
	}
	var roundTrip map[string]any
	if err := json.Unmarshal(encoded, &roundTrip); err != nil {
		t.Fatal(err)
	}
	again, err := FromMap(roundTrip, "")
	if err != nil {
		t.Fatal(err)
	}
	if again.Providers[0].EmbeddingSpace != "checkpoint-v1" || again.Nodes[0].RealtimePath != "/ws/voice" || again.ProxySettings.ASR.UploadLimit() != 4096 || again.ProxySettings.ASR.MessageLimit() != 512 {
		t.Fatalf("settings lost: %+v", again)
	}
	for _, endpoint := range []string{"/v1/systemone", "/v1/decisions", "/v1/audio/transcriptions", "/v1/realtime", "/ws/voice"} {
		if !validEndpoint(endpoint) {
			t.Errorf("rejected endpoint %s", endpoint)
		}
	}
}

func TestInferenceConfigRejectsUnsafePathsAndInvalidLimits(t *testing.T) {
	for _, path := range []string{"https://other.test/ws", "//other.test/ws", "/ws?token=secret", "/ws#fragment", "relative"} {
		if err := ValidateRealtimePath(path); err == nil {
			t.Errorf("accepted path %s", path)
		}
	}
	for _, settings := range []ASRSettings{{MaxUploadBytes: -1}, {MaxMessageBytes: -1}, {SessionTimeoutSeconds: math.Inf(1)}, {SessionTimeoutSeconds: math.NaN()}, {SessionTimeoutSeconds: math.MaxFloat64}} {
		if err := settings.Validate(); err == nil {
			t.Errorf("accepted limits %+v", settings)
		}
	}
}
