package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/theio/yallmp-proxy/internal/config"
)

func TestAdminModelsListReturnsMaskedProtectedSplitForAPI(t *testing.T) {
	app := newAdminModelsReadTestApp(t)

	resp := httptest.NewRecorder()
	app.ServeHTTP(resp, httptest.NewRequest(http.MethodGet, "/admin/models", nil))
	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", resp.Code, resp.Body.String())
	}
	for _, forbidden := range []string{"base-secret", "child-secret", "base-client-key", "base-client-cert", "base-access-token"} {
		if strings.Contains(resp.Body.String(), forbidden) {
			t.Fatalf("response exposed %q: %s", forbidden, resp.Body.String())
		}
	}
	var payload config.AdminModelList
	if err := json.Unmarshal(resp.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if got := modelConfigNames(payload.Protected); !reflect.DeepEqual(got, []string{"base"}) {
		t.Fatalf("protected names = %#v", got)
	}
	if got := modelConfigNames(payload.Unprotected); !reflect.DeepEqual(got, []string{"child", "grandchild", "openai/gpt-4.1"}) {
		t.Fatalf("unprotected names = %#v", got)
	}
	params := payload.Unprotected[0]["model_params"].(map[string]any)
	if got := params["api_base"]; got != "http://base.test/v1" {
		t.Fatalf("child resolved api_base = %v", got)
	}
}

func TestAdminModelsTreeUsesRawInheritanceGraph(t *testing.T) {
	app := newAdminModelsReadTestApp(t)

	resp := httptest.NewRecorder()
	app.ServeHTTP(resp, httptest.NewRequest(http.MethodGet, "/admin/models/tree", nil))
	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", resp.Code, resp.Body.String())
	}
	for _, forbidden := range []string{"base-secret", "child-secret", "base-client-key", "base-client-cert", "base-access-token"} {
		if strings.Contains(resp.Body.String(), forbidden) {
			t.Fatalf("tree exposed %q: %s", forbidden, resp.Body.String())
		}
	}
	var payload config.ModelTree
	if err := json.Unmarshal(resp.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(payload.Roots, []string{"base"}) {
		t.Fatalf("roots = %#v", payload.Roots)
	}
	base := payload.Nodes["base"]
	if !reflect.DeepEqual(base.Children, []string{"child", "openai/gpt-4.1"}) {
		t.Fatalf("base children = %#v", base.Children)
	}
	child := payload.Nodes["child"]
	if child.Parent != "base" || !reflect.DeepEqual(child.Children, []string{"grandchild"}) {
		t.Fatalf("child node = %#v", child)
	}
	if child.Protected || !child.Editable {
		t.Fatalf("child protection/editable = %#v", child)
	}
	params := child.Config["model_params"].(map[string]any)
	if got := params["api_base"]; got != "http://base.test/v1" {
		t.Fatalf("child config api_base = %v", got)
	}
	if _, ok := params["api_key"]; ok {
		t.Fatalf("child config exposed api_key: %#v", params)
	}
}

func TestAdminModelAncestryAndDependents(t *testing.T) {
	app := newAdminModelsReadTestApp(t)

	resp := httptest.NewRecorder()
	app.ServeHTTP(resp, httptest.NewRequest(http.MethodGet, "/admin/models/grandchild/ancestry", nil))
	if resp.Code != http.StatusOK {
		t.Fatalf("ancestry status = %d body=%s", resp.Code, resp.Body.String())
	}
	var ancestry map[string]any
	if err := json.Unmarshal(resp.Body.Bytes(), &ancestry); err != nil {
		t.Fatal(err)
	}
	if got := stringSliceFromAny(ancestry["chain"]); !reflect.DeepEqual(got, []string{"grandchild", "child", "base"}) {
		t.Fatalf("chain = %#v", got)
	}
	if ancestry["inheritance_depth"] != float64(2) {
		t.Fatalf("inheritance_depth = %#v", ancestry["inheritance_depth"])
	}

	resp = httptest.NewRecorder()
	app.ServeHTTP(resp, httptest.NewRequest(http.MethodGet, "/admin/models/openai/gpt-4.1/ancestry", nil))
	if resp.Code != http.StatusOK {
		t.Fatalf("slash ancestry status = %d body=%s", resp.Code, resp.Body.String())
	}
	if err := json.Unmarshal(resp.Body.Bytes(), &ancestry); err != nil {
		t.Fatal(err)
	}
	if got := stringSliceFromAny(ancestry["chain"]); !reflect.DeepEqual(got, []string{"openai/gpt-4.1", "base"}) {
		t.Fatalf("slash chain = %#v", got)
	}

	resp = httptest.NewRecorder()
	app.ServeHTTP(resp, httptest.NewRequest(http.MethodGet, "/admin/models/base/dependents", nil))
	if resp.Code != http.StatusOK {
		t.Fatalf("dependents status = %d body=%s", resp.Code, resp.Body.String())
	}
	var dependents map[string]any
	if err := json.Unmarshal(resp.Body.Bytes(), &dependents); err != nil {
		t.Fatal(err)
	}
	if got := stringSliceFromAny(dependents["direct_children"]); !reflect.DeepEqual(got, []string{"child", "openai/gpt-4.1"}) {
		t.Fatalf("direct_children = %#v", got)
	}
	if got := stringSliceFromAny(dependents["all_descendants"]); !reflect.DeepEqual(got, []string{"child", "grandchild", "openai/gpt-4.1"}) {
		t.Fatalf("all_descendants = %#v", got)
	}
	if dependents["descendant_count"] != float64(3) {
		t.Fatalf("descendant_count = %#v", dependents["descendant_count"])
	}
}

func TestAdminModelReadAPIsReturnJSONNotFound(t *testing.T) {
	app := newAdminModelsReadTestApp(t)

	resp := httptest.NewRecorder()
	app.ServeHTTP(resp, httptest.NewRequest(http.MethodGet, "/admin/models/missing/ancestry", nil))
	if resp.Code != http.StatusNotFound {
		t.Fatalf("ancestry status = %d body=%s", resp.Code, resp.Body.String())
	}
	var payload map[string]any
	if err := json.Unmarshal(resp.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	errPayload := payload["error"].(map[string]any)
	if errPayload["code"] != "model_not_found" {
		t.Fatalf("error = %#v", errPayload)
	}

	resp = httptest.NewRecorder()
	app.ServeHTTP(resp, httptest.NewRequest(http.MethodGet, "/admin/models/missing/dependents", nil))
	if resp.Code != http.StatusNotFound {
		t.Fatalf("dependents status = %d body=%s", resp.Code, resp.Body.String())
	}
}

func TestAdminModelCopyHappyPathPreservesRawAndMasksResponse(t *testing.T) {
	app := newAdminModelsReadTestApp(t)

	resp := adminModelRequest(app, http.MethodPost, "/admin/models/copy?source=child&target=child-copy", "", "")
	if resp.Code != http.StatusOK {
		t.Fatalf("copy status = %d body=%s", resp.Code, resp.Body.String())
	}
	for _, forbidden := range []string{"base-secret", "child-secret", "base-client-key", "base-client-cert", "base-access-token"} {
		if strings.Contains(resp.Body.String(), forbidden) {
			t.Fatalf("copy response exposed %q: %s", forbidden, resp.Body.String())
		}
	}
	payload := decodeJSONMap(t, resp)
	if payload["model"] != "child-copy" {
		t.Fatalf("payload = %#v", payload)
	}
	copiedConfig := payload["config"].(map[string]any)
	if copiedConfig["extends"] != "base" {
		t.Fatalf("copied config = %#v", copiedConfig)
	}
	params := copiedConfig["model_params"].(map[string]any)
	if _, ok := params["api_key"]; ok {
		t.Fatalf("copy response exposed api_key: %#v", copiedConfig)
	}
	rawCopy, ok := app.Config.RawLegacyModel("child-copy")
	if !ok {
		t.Fatal("runtime config missing copied raw model")
	}
	if rawCopy["model_name"] != "child-copy" || rawCopy["extends"] != "base" {
		t.Fatalf("raw copy = %#v", rawCopy)
	}
	for _, key := range []string{"editable", "source", "_inherited_from"} {
		if _, ok := rawCopy[key]; ok {
			t.Fatalf("raw copy kept %s: %#v", key, rawCopy)
		}
	}
	modelsResp := httptest.NewRecorder()
	app.ServeHTTP(modelsResp, httptest.NewRequest(http.MethodGet, "/v1/models", nil))
	if modelsResp.Code != http.StatusOK || !strings.Contains(modelsResp.Body.String(), "child-copy") {
		t.Fatalf("/v1/models after copy status=%d body=%s", modelsResp.Code, modelsResp.Body.String())
	}
}

func TestAdminModelCopyAcceptsJSONBody(t *testing.T) {
	app := newAdminModelsReadTestApp(t)

	resp := adminModelRequest(app, http.MethodPost, "/admin/models/copy", `{"source":"child","target":"child-json-copy"}`, "")
	if resp.Code != http.StatusOK {
		t.Fatalf("copy status = %d body=%s", resp.Code, resp.Body.String())
	}
	if _, ok := app.Config.FindLegacyModel("child-json-copy"); !ok {
		t.Fatal("copied model missing from runtime config")
	}
}

func TestAdminModelCopyPreservesEnvPlaceholdersOnDiskAndUsesRuntimeEnv(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	envPath := filepath.Join(dir, ".env")
	if err := os.WriteFile(envPath, []byte("CHILD_KEY=env-child-secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfgPath, []byte(`
model_list:
  - model_name: base
    protected: false
    model_params:
      api_base: http://base.test/v1
      model: openai/base
  - model_name: child
    extends: base
    protected: false
    model_params:
      api_key: ${CHILD_KEY}
      model: openai/child
`), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadWithOptions(config.LoadOptions{Path: cfgPath, EnvPath: envPath, SubstituteEnv: true})
	if err != nil {
		t.Fatal(err)
	}
	app, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if got := app.Router.Backends["child"].APIKey; got != "env-child-secret" {
		t.Fatalf("initial runtime api key = %#v", got)
	}

	resp := adminModelRequest(app, http.MethodPost, "/admin/models/copy?source=child&target=child-copy", "", "")
	if resp.Code != http.StatusOK {
		t.Fatalf("copy status = %d body=%s", resp.Code, resp.Body.String())
	}
	if got := app.Router.Backends["child-copy"].APIKey; got != "env-child-secret" {
		t.Fatalf("copied runtime api key = %#v", got)
	}
	saved, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	text := string(saved)
	if !strings.Contains(text, "api_key: ${CHILD_KEY}") {
		t.Fatalf("saved config lost placeholder:\n%s", text)
	}
	if strings.Contains(text, "env-child-secret") {
		t.Fatalf("saved config leaked substituted secret:\n%s", text)
	}
}

func TestAdminModelCopyProtectedRequiresPassword(t *testing.T) {
	t.Setenv(config.AdminPasswordEnv, "pw")
	app := newAdminModelsReadTestApp(t)

	resp := adminModelRequest(app, http.MethodPost, "/admin/models/copy?source=base&target=base-copy", "", "")
	if resp.Code != http.StatusForbidden {
		t.Fatalf("copy without password status = %d body=%s", resp.Code, resp.Body.String())
	}
	if _, ok := app.Config.FindLegacyModel("base-copy"); ok {
		t.Fatal("protected copy mutated config without password")
	}

	resp = adminModelRequest(app, http.MethodPost, "/admin/models/copy?source=base&target=base-copy", "", "pw")
	if resp.Code != http.StatusOK {
		t.Fatalf("copy with password status = %d body=%s", resp.Code, resp.Body.String())
	}
	if _, ok := app.Config.FindLegacyModel("base-copy"); !ok {
		t.Fatal("protected copy with password did not create target")
	}
}

func TestAdminModelCopyDuplicateTarget(t *testing.T) {
	app := newAdminModelsReadTestApp(t)

	resp := adminModelRequest(app, http.MethodPost, "/admin/models/copy?source=child&target=grandchild", "", "")
	if resp.Code != http.StatusConflict {
		t.Fatalf("duplicate status = %d body=%s", resp.Code, resp.Body.String())
	}
	payload := decodeJSONMap(t, resp)
	errPayload := payload["error"].(map[string]any)
	if errPayload["code"] != "duplicate_model" {
		t.Fatalf("error = %#v", errPayload)
	}
}

func TestAdminModelDeleteUnknown(t *testing.T) {
	app := newAdminModelsReadTestApp(t)

	resp := adminModelRequest(app, http.MethodDelete, "/admin/models/missing", "", "")
	if resp.Code != http.StatusNotFound {
		t.Fatalf("delete missing status = %d body=%s", resp.Code, resp.Body.String())
	}
	payload := decodeJSONMap(t, resp)
	errPayload := payload["error"].(map[string]any)
	if errPayload["code"] != "model_not_found" {
		t.Fatalf("error = %#v", errPayload)
	}
}

func TestAdminModelDeleteDependentConflict(t *testing.T) {
	app := newAdminModelsReadTestApp(t)

	resp := adminModelRequest(app, http.MethodDelete, "/admin/models/child", "", "")
	if resp.Code != http.StatusConflict {
		t.Fatalf("delete conflict status = %d body=%s", resp.Code, resp.Body.String())
	}
	payload := decodeJSONMap(t, resp)
	if got := stringSliceFromAny(payload["dependents"]); !reflect.DeepEqual(got, []string{"grandchild"}) {
		t.Fatalf("dependents = %#v", payload)
	}
	if _, ok := app.Config.FindLegacyModel("child"); !ok {
		t.Fatal("child was deleted despite cascade conflict")
	}
}

func TestAdminModelDeleteSlashNameHappyPath(t *testing.T) {
	app := newAdminModelsReadTestApp(t)

	resp := adminModelRequest(app, http.MethodDelete, "/admin/models/openai/gpt-4.1", "", "")
	if resp.Code != http.StatusOK {
		t.Fatalf("delete status = %d body=%s", resp.Code, resp.Body.String())
	}
	if _, ok := app.Config.FindLegacyModel("openai/gpt-4.1"); ok {
		t.Fatal("slash-containing model still present after delete")
	}
	modelsResp := httptest.NewRecorder()
	app.ServeHTTP(modelsResp, httptest.NewRequest(http.MethodGet, "/v1/models", nil))
	if modelsResp.Code != http.StatusOK {
		t.Fatalf("/v1/models status = %d body=%s", modelsResp.Code, modelsResp.Body.String())
	}
	if strings.Contains(modelsResp.Body.String(), "openai/gpt-4.1") {
		t.Fatalf("/v1/models listed deleted model: %s", modelsResp.Body.String())
	}
}

func TestAdminModelDeleteCascadeProtectedDescendantRequiresPassword(t *testing.T) {
	t.Setenv(config.AdminPasswordEnv, "pw")
	app := newAdminModelCascadeProtectionTestApp(t)

	resp := adminModelRequest(app, http.MethodDelete, "/admin/models/root?cascade=true", "", "")
	if resp.Code != http.StatusForbidden {
		t.Fatalf("delete without password status = %d body=%s", resp.Code, resp.Body.String())
	}
	if _, ok := app.Config.FindLegacyModel("root"); !ok {
		t.Fatal("root deleted without password")
	}

	resp = adminModelRequest(app, http.MethodDelete, "/admin/models/root?cascade=true", "", "pw")
	if resp.Code != http.StatusOK {
		t.Fatalf("delete with password status = %d body=%s", resp.Code, resp.Body.String())
	}
	for _, name := range []string{"root", "protected-child"} {
		if _, ok := app.Config.FindLegacyModel(name); ok {
			t.Fatalf("%s still present after cascade delete", name)
		}
	}
	if _, ok := app.Config.FindLegacyModel("survivor"); !ok {
		t.Fatal("unrelated survivor model was removed")
	}
}

func TestAdminModelDeleteValidationFailureDoesNotMutateRuntime(t *testing.T) {
	cfg, err := config.FromMap(map[string]any{
		"model_list": []any{
			map[string]any{
				"model_name": "solo",
				"protected":  false,
				"model_params": map[string]any{
					"api_base": "http://solo.test/v1",
					"model":    "openai/solo",
				},
			},
		},
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	app, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}

	resp := adminModelRequest(app, http.MethodDelete, "/admin/models/solo", "", "")
	if resp.Code != http.StatusBadRequest {
		t.Fatalf("delete validation status = %d body=%s", resp.Code, resp.Body.String())
	}
	if !strings.Contains(resp.Body.String(), "no_backends") {
		t.Fatalf("validation response missing no_backends: %s", resp.Body.String())
	}
	if _, ok := app.Config.FindLegacyModel("solo"); !ok {
		t.Fatal("runtime config mutated after validation failure")
	}
	modelsResp := httptest.NewRecorder()
	app.ServeHTTP(modelsResp, httptest.NewRequest(http.MethodGet, "/v1/models", nil))
	if modelsResp.Code != http.StatusOK || !strings.Contains(modelsResp.Body.String(), "solo") {
		t.Fatalf("runtime router mutated after validation failure: status=%d body=%s", modelsResp.Code, modelsResp.Body.String())
	}
}

func newAdminModelsReadTestApp(t *testing.T) *App {
	t.Helper()
	cfg, err := config.FromMap(map[string]any{
		"model_list": []any{
			map[string]any{
				"model_name": "base",
				"model_params": map[string]any{
					"api_base":      "http://base.test/v1",
					"api_key":       "base-secret",
					"client_key":    "base-client-key",
					"client_cert":   "base-client-cert",
					"client_secret": "base-client-secret",
					"access_token":  "base-access-token",
					"model":         "openai/base",
				},
			},
			map[string]any{
				"model_name": "child",
				"extends":    "base",
				"protected":  false,
				"model_params": map[string]any{
					"api_key": "child-secret",
					"model":   "openai/child",
				},
			},
			map[string]any{
				"model_name": "openai/gpt-4.1",
				"extends":    "base",
				"protected":  false,
				"model_params": map[string]any{
					"model": "openai/slash",
				},
			},
			map[string]any{
				"model_name": "grandchild",
				"extends":    "child",
				"protected":  false,
				"model_params": map[string]any{
					"model": "openai/grandchild",
				},
			},
		},
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	app, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return app
}

func newAdminModelCascadeProtectionTestApp(t *testing.T) *App {
	t.Helper()
	cfg, err := config.FromMap(map[string]any{
		"model_list": []any{
			map[string]any{
				"model_name": "root",
				"protected":  false,
				"model_params": map[string]any{
					"api_base": "http://root.test/v1",
					"model":    "openai/root",
				},
			},
			map[string]any{
				"model_name": "protected-child",
				"extends":    "root",
				"protected":  true,
				"model_params": map[string]any{
					"model": "openai/protected-child",
				},
			},
			map[string]any{
				"model_name": "survivor",
				"protected":  false,
				"model_params": map[string]any{
					"api_base": "http://survivor.test/v1",
					"model":    "openai/survivor",
				},
			},
		},
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	app, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return app
}

func adminModelRequest(app *App, method string, path string, body string, password string) *httptest.ResponseRecorder {
	resp := httptest.NewRecorder()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if password != "" {
		req.Header.Set("x-admin-password", password)
	}
	app.ServeHTTP(resp, req)
	return resp
}

func modelConfigNames(models []map[string]any) []string {
	names := make([]string, 0, len(models))
	for _, model := range models {
		names = append(names, model["model_name"].(string))
	}
	return names
}

func stringSliceFromAny(value any) []string {
	items := value.([]any)
	out := make([]string, 0, len(items))
	for _, item := range items {
		out = append(out, item.(string))
	}
	return out
}
