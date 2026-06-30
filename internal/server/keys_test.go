package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/theio/yallmp-proxy/internal/config"
)

func TestAdminKeysRequireConfiguredPassword(t *testing.T) {
	enabled := true
	app := newTestApp(t, "http://upstream.test/v1")
	app.Config.AppKeys = config.AppKeysConfig{
		Enabled: true,
		Keys: []config.AppKeyEntry{{
			KeyID:   "client-a",
			Secret:  "client-secret",
			Enabled: &enabled,
		}},
	}
	app.Policy.Config = app.Config

	t.Setenv(config.AdminPasswordEnv, "")
	resp := adminKeyRequest(app, http.MethodGet, "/admin/keys", "", "")
	if resp.Code != http.StatusForbidden {
		t.Fatalf("unset password status = %d body=%s", resp.Code, resp.Body.String())
	}
	if strings.Contains(resp.Body.String(), "client-secret") || strings.Contains(resp.Body.String(), "client-a") {
		t.Fatalf("unauthorized response leaked key data: %s", resp.Body.String())
	}

	t.Setenv(config.AdminPasswordEnv, "pw")
	resp = adminKeyRequest(app, http.MethodGet, "/admin/keys", "", "wrong")
	if resp.Code != http.StatusForbidden {
		t.Fatalf("wrong password status = %d body=%s", resp.Code, resp.Body.String())
	}
	if strings.Contains(resp.Body.String(), "client-secret") || strings.Contains(resp.Body.String(), "client-a") {
		t.Fatalf("forbidden response leaked key data: %s", resp.Body.String())
	}

	resp = adminKeyRequest(app, http.MethodGet, "/admin/keys", "", "pw")
	if resp.Code != http.StatusOK {
		t.Fatalf("correct password status = %d body=%s", resp.Code, resp.Body.String())
	}
}

func TestAdminKeysCreateListGetMasksSecrets(t *testing.T) {
	t.Setenv(config.AdminPasswordEnv, "pw")
	app := newTestApp(t, "http://upstream.test/v1")

	createResp := adminKeyRequest(app, http.MethodPost, "/admin/keys", `{"name":"Generated","description":"Created by test"}`, "pw")
	if createResp.Code != http.StatusOK {
		t.Fatalf("create status = %d body=%s", createResp.Code, createResp.Body.String())
	}
	created := decodeJSONMap(t, createResp)
	keyID := created["key_id"].(string)
	secret := created["secret"].(string)
	if keyID == "" || secret == "" {
		t.Fatalf("generated key_id/secret missing: %#v", created)
	}
	if strings.Count(createResp.Body.String(), secret) != 1 {
		t.Fatalf("secret should appear exactly once in create response: %s", createResp.Body.String())
	}
	key := created["key"].(map[string]any)
	if _, ok := key["secret"]; ok {
		t.Fatalf("masked key metadata included secret: %#v", key)
	}
	if key["has_secret"] != true || key["description"] != "Created by test" {
		t.Fatalf("key metadata = %#v", key)
	}

	listResp := adminKeyRequest(app, http.MethodGet, "/admin/keys", "", "pw")
	if listResp.Code != http.StatusOK {
		t.Fatalf("list status = %d body=%s", listResp.Code, listResp.Body.String())
	}
	if strings.Contains(listResp.Body.String(), secret) {
		t.Fatalf("list leaked secret: %s", listResp.Body.String())
	}
	listed := decodeJSONMap(t, listResp)
	keys := listed["keys"].([]any)
	if len(keys) != 1 || keys[0].(map[string]any)["key_id"] != keyID || keys[0].(map[string]any)["has_secret"] != true {
		t.Fatalf("listed keys = %#v", keys)
	}

	getResp := adminKeyRequest(app, http.MethodGet, "/admin/keys/"+keyID, "", "pw")
	if getResp.Code != http.StatusOK {
		t.Fatalf("get status = %d body=%s", getResp.Code, getResp.Body.String())
	}
	if strings.Contains(getResp.Body.String(), secret) {
		t.Fatalf("get leaked secret: %s", getResp.Body.String())
	}
	got := decodeJSONMap(t, getResp)
	if got["key_id"] != keyID || got["has_secret"] != true {
		t.Fatalf("got key = %#v", got)
	}

	duplicateResp := adminKeyRequest(app, http.MethodPost, "/admin/keys", `{"key_id":"`+keyID+`"}`, "pw")
	if duplicateResp.Code != http.StatusConflict {
		t.Fatalf("duplicate status = %d body=%s", duplicateResp.Code, duplicateResp.Body.String())
	}
}

func TestAdminKeysUpdatePreservesSecret(t *testing.T) {
	t.Setenv(config.AdminPasswordEnv, "pw")
	enabled := true
	app := newTestApp(t, "http://upstream.test/v1")
	app.Config.AppKeys = config.AppKeysConfig{
		Enabled: true,
		Keys: []config.AppKeyEntry{{
			KeyID:   "client-a",
			Name:    "Old",
			Secret:  "old-secret",
			Enabled: &enabled,
		}},
	}
	app.Policy.Config = app.Config

	resp := adminKeyRequest(app, http.MethodPut, "/admin/keys/client-a", `{"name":"New","description":"updated","secret":"attempted-secret"}`, "pw")
	if resp.Code != http.StatusOK {
		t.Fatalf("update status = %d body=%s", resp.Code, resp.Body.String())
	}
	if strings.Contains(resp.Body.String(), "old-secret") || strings.Contains(resp.Body.String(), "attempted-secret") {
		t.Fatalf("update response leaked or accepted secret: %s", resp.Body.String())
	}
	key := app.Config.AppKeys.Keys[0]
	if key.Secret != "old-secret" || key.Name != "New" || key.Description != "updated" {
		t.Fatalf("updated key = %#v", key)
	}
}

func TestAdminKeysDeleteRemovesRuntimeAccess(t *testing.T) {
	t.Setenv(config.AdminPasswordEnv, "pw")
	upstream := newJSONUpstream()
	defer upstream.Close()
	app := newRuntimeKeyApp(t, upstream.URL+"/v1", "client-a", "old-secret")

	if resp := postChat(app, "old-secret"); resp.Code != http.StatusOK {
		t.Fatalf("initial auth status = %d body=%s", resp.Code, resp.Body.String())
	}
	deleteResp := adminKeyRequest(app, http.MethodDelete, "/admin/keys/client-a", "", "pw")
	if deleteResp.Code != http.StatusOK {
		t.Fatalf("delete status = %d body=%s", deleteResp.Code, deleteResp.Body.String())
	}
	afterDelete := postChat(app, "old-secret")
	if afterDelete.Code != http.StatusUnauthorized {
		t.Fatalf("post-delete status = %d body=%s", afterDelete.Code, afterDelete.Body.String())
	}
}

func TestAdminKeysRegenerateInvalidatesOldSecret(t *testing.T) {
	t.Setenv(config.AdminPasswordEnv, "pw")
	upstream := newJSONUpstream()
	defer upstream.Close()
	app := newRuntimeKeyApp(t, upstream.URL+"/v1", "client-a", "old-secret")

	resp := adminKeyRequest(app, http.MethodPost, "/admin/keys/client-a/regenerate", `{}`, "pw")
	if resp.Code != http.StatusOK {
		t.Fatalf("regenerate status = %d body=%s", resp.Code, resp.Body.String())
	}
	payload := decodeJSONMap(t, resp)
	newSecret := payload["secret"].(string)
	if newSecret == "" || newSecret == "old-secret" {
		t.Fatalf("new secret = %q", newSecret)
	}
	if strings.Count(resp.Body.String(), newSecret) != 1 || strings.Contains(resp.Body.String(), "old-secret") {
		t.Fatalf("regenerate response leaked secret data: %s", resp.Body.String())
	}
	if oldResp := postChat(app, "old-secret"); oldResp.Code != http.StatusUnauthorized {
		t.Fatalf("old secret status = %d body=%s", oldResp.Code, oldResp.Body.String())
	}
	if newResp := postChat(app, newSecret); newResp.Code != http.StatusOK {
		t.Fatalf("new secret status = %d body=%s", newResp.Code, newResp.Body.String())
	}
}

func TestAdminKeysConfigUpdatesSettingsGroupsDefaults(t *testing.T) {
	t.Setenv(config.AdminPasswordEnv, "pw")
	app := newTestApp(t, "http://upstream.test/v1")

	body := `{
		"enabled": true,
		"header_name": "x-client-key",
		"allow_unauthenticated": false,
		"defaults": {"rps": 2, "priority": 7},
		"unauthenticated": {"rpm": 4},
		"groups": [{
			"group_id": "team-a",
			"name": "Team A",
			"enabled": true,
			"models": ["alpha"],
			"limits": {"rpm": 30}
		}]
	}`
	resp := adminKeyRequest(app, http.MethodPost, "/admin/keys/config", body, "pw")
	if resp.Code != http.StatusOK {
		t.Fatalf("config status = %d body=%s", resp.Code, resp.Body.String())
	}
	if app.Config.AppKeys.HeaderName != "x-client-key" || !app.Config.AppKeys.Enabled {
		t.Fatalf("app key settings = %#v", app.Config.AppKeys)
	}
	if app.Config.AppKeys.Defaults.RPS != 2 || app.Config.AppKeys.Defaults.Priority != 7 || app.Config.AppKeys.Unauthenticated.RPM != 4 {
		t.Fatalf("limits = defaults %#v unauth %#v", app.Config.AppKeys.Defaults, app.Config.AppKeys.Unauthenticated)
	}
	if len(app.Config.AppKeys.Groups) != 1 || app.Config.AppKeys.Groups[0].GroupID != "team-a" || app.Config.AppKeys.Groups[0].Limits.RPM != 30 {
		t.Fatalf("groups = %#v", app.Config.AppKeys.Groups)
	}
	payload := decodeJSONMap(t, resp)
	if payload["status"] != "ok" || payload["header_name"] != "x-client-key" {
		t.Fatalf("payload = %#v", payload)
	}
}

func TestAdminKeysRequiredHeadersRoundTrip(t *testing.T) {
	t.Setenv(config.AdminPasswordEnv, "pw")
	app := newTestApp(t, "http://upstream.test/v1")

	configResp := adminKeyRequest(app, http.MethodPost, "/admin/keys/config", `{
		"enabled": true,
		"required_headers": [{"name":"X-Org-ID","log_as":"org","models":["alpha"]}],
		"groups": [{
			"group_id": "team-a",
			"required_headers": [{"name":"X-Team-ID","log_as":"team"}]
		}]
	}`, "pw")
	if configResp.Code != http.StatusOK {
		t.Fatalf("config status = %d body=%s", configResp.Code, configResp.Body.String())
	}
	if len(app.Config.AppKeys.RequiredHeaders) != 1 || app.Config.AppKeys.RequiredHeaders[0].Name != "X-Org-ID" {
		t.Fatalf("global required headers = %#v", app.Config.AppKeys.RequiredHeaders)
	}
	if len(app.Config.AppKeys.Groups) != 1 || len(app.Config.AppKeys.Groups[0].RequiredHeaders) != 1 || app.Config.AppKeys.Groups[0].RequiredHeaders[0].Name != "X-Team-ID" {
		t.Fatalf("group required headers = %#v", app.Config.AppKeys.Groups)
	}
	configPayload := decodeJSONMap(t, configResp)
	requiredHeaders := configPayload["required_headers"].([]any)
	if requiredHeaders[0].(map[string]any)["log_as"] != "org" {
		t.Fatalf("config response required_headers = %#v", requiredHeaders)
	}

	createResp := adminKeyRequest(app, http.MethodPost, "/admin/keys", `{
		"key_id": "client-a",
		"secret": "client-secret",
		"group_id": "team-a",
		"required_headers": [{"name":"X-Key-ID","log_as":"key","models":["alpha"]}]
	}`, "pw")
	if createResp.Code != http.StatusOK {
		t.Fatalf("create status = %d body=%s", createResp.Code, createResp.Body.String())
	}
	createPayload := decodeJSONMap(t, createResp)
	key := createPayload["key"].(map[string]any)
	keyRequiredHeaders := key["required_headers"].([]any)
	if keyRequiredHeaders[0].(map[string]any)["name"] != "X-Key-ID" {
		t.Fatalf("created key required_headers = %#v", keyRequiredHeaders)
	}

	updateResp := adminKeyRequest(app, http.MethodPut, "/admin/keys/client-a", `{"name":"Renamed"}`, "pw")
	if updateResp.Code != http.StatusOK {
		t.Fatalf("update status = %d body=%s", updateResp.Code, updateResp.Body.String())
	}
	if len(app.Config.AppKeys.Keys) != 1 || len(app.Config.AppKeys.Keys[0].RequiredHeaders) != 1 || app.Config.AppKeys.Keys[0].RequiredHeaders[0].Name != "X-Key-ID" {
		t.Fatalf("updated key required_headers = %#v", app.Config.AppKeys.Keys)
	}
	getResp := adminKeyRequest(app, http.MethodGet, "/admin/keys/client-a", "", "pw")
	if getResp.Code != http.StatusOK {
		t.Fatalf("get status = %d body=%s", getResp.Code, getResp.Body.String())
	}
	if strings.Contains(getResp.Body.String(), "client-secret") {
		t.Fatalf("get leaked secret: %s", getResp.Body.String())
	}
	got := decodeJSONMap(t, getResp)
	gotRequiredHeaders := got["required_headers"].([]any)
	if gotRequiredHeaders[0].(map[string]any)["log_as"] != "key" {
		t.Fatalf("get required_headers = %#v", gotRequiredHeaders)
	}
}

func TestAdminKeysValidationErrorsDoNotMutate(t *testing.T) {
	t.Setenv(config.AdminPasswordEnv, "pw")
	app := newTestApp(t, "http://upstream.test/v1")

	resp := adminKeyRequest(app, http.MethodPost, "/admin/keys", `{
		"key_id": "bad",
		"secret": "bad-secret",
		"group_id": "missing-group",
		"limits": {"rps": -1}
	}`, "pw")
	if resp.Code != http.StatusBadRequest {
		t.Fatalf("invalid create status = %d body=%s", resp.Code, resp.Body.String())
	}
	body := resp.Body.String()
	if !strings.Contains(body, "unknown_group") || !strings.Contains(body, "invalid_limit") {
		t.Fatalf("validation body missing expected issues: %s", body)
	}
	if strings.Contains(body, "bad-secret") {
		t.Fatalf("validation response leaked secret: %s", body)
	}
	if _, ok := app.findAppKey("bad"); ok {
		t.Fatalf("invalid key mutated app config")
	}
}

func TestAdminKeysLegacyGroupedSecretLookupAndRegenerate(t *testing.T) {
	t.Setenv(config.AdminPasswordEnv, "pw")
	enabled := true
	app := newTestApp(t, "http://upstream.test/v1")
	app.Config.AppKeys = config.AppKeysConfig{
		Enabled: true,
		Groups: []config.AppKeyGroupEntry{{
			GroupID: "team",
			Enabled: &enabled,
		}},
		Keys: []config.AppKeyEntry{{
			Secret:  "yallmp-team-legacy",
			Enabled: &enabled,
		}},
	}
	app.Policy.Config = app.Config

	getResp := adminKeyRequest(app, http.MethodGet, "/admin/keys/legacy", "", "pw")
	if getResp.Code != http.StatusOK {
		t.Fatalf("legacy get status = %d body=%s", getResp.Code, getResp.Body.String())
	}
	got := decodeJSONMap(t, getResp)
	if got["key_id"] != "legacy" || got["group_id"] != "team" || got["has_secret"] != true {
		t.Fatalf("legacy key metadata = %#v", got)
	}
	if strings.Contains(getResp.Body.String(), "yallmp-team-legacy") {
		t.Fatalf("legacy get leaked secret: %s", getResp.Body.String())
	}

	regenResp := adminKeyRequest(app, http.MethodPost, "/admin/keys/legacy/regenerate", `{}`, "pw")
	if regenResp.Code != http.StatusOK {
		t.Fatalf("legacy regenerate status = %d body=%s", regenResp.Code, regenResp.Body.String())
	}
	if app.Config.AppKeys.Keys[0].KeyID != "legacy" || app.Config.AppKeys.Keys[0].GroupID != "team" {
		t.Fatalf("regenerated legacy key lost parsed identity: %#v", app.Config.AppKeys.Keys[0])
	}
	if app.Config.AppKeys.Keys[0].Secret == "yallmp-team-legacy" {
		t.Fatalf("secret was not regenerated")
	}
}

func TestAdminKeysMutationPreservesRawInheritedModelConfig(t *testing.T) {
	t.Setenv(config.AdminPasswordEnv, "pw")
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(cfgPath, []byte(`
model_list:
  - model_name: base
    model_params:
      api_base: http://base.test/v1
      api_key: base-secret
      model: openai/base
  - model_name: derived
    extends: base
    model_params:
      model: openai/derived
app_keys:
  enabled: true
`), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadWithOptions(config.LoadOptions{Path: cfgPath, SubstituteEnv: false})
	if err != nil {
		t.Fatal(err)
	}
	app, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}

	resp := adminKeyRequest(app, http.MethodPost, "/admin/keys", `{"key_id":"client-a","secret":"client-secret"}`, "pw")
	if resp.Code != http.StatusOK {
		t.Fatalf("create status = %d body=%s", resp.Code, resp.Body.String())
	}
	saved, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	text := string(saved)
	if !strings.Contains(text, "extends: base") {
		t.Fatalf("saved config lost inheritance:\n%s", text)
	}
	if strings.Contains(text, "_inherited_from") {
		t.Fatalf("saved config wrote resolved inheritance metadata:\n%s", text)
	}
	if strings.Count(text, "api_base: http://base.test/v1") != 1 {
		t.Fatalf("saved config appears to have flattened inherited api_base:\n%s", text)
	}
	if !strings.Contains(text, "key_id: client-a") || !strings.Contains(text, "secret: client-secret") {
		t.Fatalf("saved config missing created app key:\n%s", text)
	}
}

func adminKeyRequest(app *App, method string, path string, body string, password string) *httptest.ResponseRecorder {
	resp := httptest.NewRecorder()
	var reader *strings.Reader
	if body == "" {
		reader = strings.NewReader("")
	} else {
		reader = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, reader)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if password != "" {
		req.Header.Set("x-admin-password", password)
	}
	app.ServeHTTP(resp, req)
	return resp
}

func decodeJSONMap(t *testing.T, resp *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var payload map[string]any
	if err := json.Unmarshal(resp.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	return payload
}

func newRuntimeKeyApp(t *testing.T, upstream string, keyID string, secret string) *App {
	t.Helper()
	enabled := true
	app, err := New(&config.Config{
		ModelList: []config.ModelEntry{{
			ModelName: "alpha",
			ModelParams: map[string]any{
				"api_base": upstream,
				"api_key":  "upstream-key",
				"model":    "openai/upstream-model",
			},
		}},
		AppKeys: config.AppKeysConfig{
			Enabled: true,
			Keys: []config.AppKeyEntry{{
				KeyID:   keyID,
				Secret:  secret,
				Enabled: &enabled,
				Models:  []string{"alpha"},
			}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return app
}

func newJSONUpstream() *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}]}`))
	}))
}
