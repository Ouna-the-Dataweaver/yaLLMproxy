package config

import "testing"

func TestMaskSensitiveRemovesAPIKeysRecursively(t *testing.T) {
	data := map[string]any{
		"api_key":       "secret",
		"client_key":    "client-key-secret",
		"client_cert":   "client-cert-secret",
		"access_token":  "access-token-secret",
		"refresh_token": "refresh-token-secret",
		"token_quota":   100,
		"nested": []any{
			map[string]any{"API_KEY": "secret"},
			map[string]any{"api_key": nil, "value": 1},
		},
		"normal": "ok",
	}
	masked := MaskSensitive(data).(map[string]any)
	if _, ok := masked["api_key"]; ok {
		t.Fatalf("top-level api_key was not removed: %#v", masked)
	}
	for _, key := range []string{"client_key", "client_cert", "access_token", "refresh_token"} {
		if _, ok := masked[key]; ok {
			t.Fatalf("%s was not removed: %#v", key, masked)
		}
	}
	if masked["token_quota"] != 100 {
		t.Fatalf("non-sensitive token quota changed: %#v", masked)
	}
	nested := masked["nested"].([]any)
	if _, ok := nested[0].(map[string]any)["API_KEY"]; ok {
		t.Fatalf("nested API_KEY was not removed: %#v", nested[0])
	}
	if _, ok := nested[1].(map[string]any)["api_key"]; ok {
		t.Fatalf("nil api_key was not removed: %#v", nested[1])
	}
	if nested[1].(map[string]any)["value"] != 1 {
		t.Fatalf("non-sensitive value not preserved: %#v", nested[1])
	}
	if masked["normal"] != "ok" {
		t.Fatalf("normal key changed: %#v", masked)
	}
}

func TestPreserveMissingSensitiveFieldsRestoresOmittedSecrets(t *testing.T) {
	existing := map[string]any{
		"model_list": []any{
			map[string]any{
				"model_name": "alpha",
				"model_params": map[string]any{
					"api_base": "http://alpha.test/v1",
					"api_key":  "alpha-secret",
				},
			},
			map[string]any{
				"model_name": "beta",
				"model_params": map[string]any{
					"api_base": "http://beta.test/v1",
					"api_key":  "beta-secret",
				},
			},
		},
	}
	incoming := map[string]any{
		"model_list": []any{
			map[string]any{
				"model_name": "beta",
				"model_params": map[string]any{
					"api_base": "http://changed.test/v1",
				},
			},
		},
	}
	merged := PreserveMissingSensitiveFields(incoming, existing).(map[string]any)
	models := merged["model_list"].([]any)
	params := models[0].(map[string]any)["model_params"].(map[string]any)
	if params["api_key"] != "beta-secret" {
		t.Fatalf("api_key = %#v, want beta-secret", params["api_key"])
	}
	if params["api_base"] != "http://changed.test/v1" {
		t.Fatalf("api_base was not preserved from incoming: %#v", params)
	}
}

func TestPreserveMissingSensitiveFieldsRestoresAppKeySecrets(t *testing.T) {
	existing := map[string]any{
		"app_keys": map[string]any{
			"keys": []any{
				map[string]any{
					"key_id": "client-a",
					"name":   "old",
					"secret": "client-secret",
				},
			},
		},
	}
	incoming := map[string]any{
		"app_keys": map[string]any{
			"keys": []any{
				map[string]any{
					"key_id": "client-a",
					"name":   "new",
				},
			},
		},
	}
	merged := PreserveMissingSensitiveFields(incoming, existing).(map[string]any)
	keys := merged["app_keys"].(map[string]any)["keys"].([]any)
	key := keys[0].(map[string]any)
	if key["secret"] != "client-secret" {
		t.Fatalf("secret = %#v, want preserved", key["secret"])
	}
	if key["name"] != "new" {
		t.Fatalf("name = %#v, want incoming value", key["name"])
	}
}
