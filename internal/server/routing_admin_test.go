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

func TestAdminProviderNodeClientModelCRUDPreservesRawAndMasksSecrets(t *testing.T) {
	t.Setenv(config.AdminPasswordEnv, "pw")
	app, cfgPath := newRoutingAdminTestApp(t)

	resp := routingAdminRequest(app, http.MethodGet, "/admin/providers", "", "")
	if resp.Code != http.StatusOK {
		t.Fatalf("GET providers status = %d body=%s", resp.Code, resp.Body.String())
	}
	if strings.Contains(resp.Body.String(), "${PROVIDER_KEY}") {
		t.Fatalf("GET providers exposed api_key: %s", resp.Body.String())
	}
	providers := decodeJSONMap(t, resp)["providers"].([]any)
	if len(providers) != 1 || providers[0].(map[string]any)["custom_provider"] != "keep-provider" {
		t.Fatalf("providers = %#v", providers)
	}

	resp = routingAdminRequest(app, http.MethodPost, "/admin/providers", `{"name":"provider-b","api_base":"http://provider-b.test/v1","api_key":"secret-b","api_type":"openai"}`, "")
	if resp.Code != http.StatusForbidden {
		t.Fatalf("POST provider without password status = %d body=%s", resp.Code, resp.Body.String())
	}

	resp = routingAdminRequest(app, http.MethodPost, "/admin/providers", `{"name":"provider-b","api_base":"http://provider-b.test/v1","api_key":"secret-b","api_type":"openai","custom_provider":"new-provider"}`, "pw")
	if resp.Code != http.StatusOK {
		t.Fatalf("POST provider status = %d body=%s", resp.Code, resp.Body.String())
	}
	if strings.Contains(resp.Body.String(), "secret-b") {
		t.Fatalf("POST provider exposed secret: %s", resp.Body.String())
	}
	rawProvider, ok := app.Config.RawProvider("provider-b")
	if !ok || rawProvider["api_key"] != "secret-b" || rawProvider["custom_provider"] != "new-provider" {
		t.Fatalf("raw provider-b = %#v ok=%v", rawProvider, ok)
	}

	resp = routingAdminRequest(app, http.MethodPut, "/admin/providers/provider-b", `{"name":"renamed-provider"}`, "pw")
	if resp.Code != http.StatusBadRequest {
		t.Fatalf("provider rename status = %d body=%s", resp.Code, resp.Body.String())
	}

	resp = routingAdminRequest(app, http.MethodPut, "/admin/providers/provider-a", `{"api_base":"http://changed-provider.test/v1","endpoints":["chat","responses"]}`, "pw")
	if resp.Code != http.StatusOK {
		t.Fatalf("PUT provider status = %d body=%s", resp.Code, resp.Body.String())
	}
	rawProvider, ok = app.Config.RawProvider("provider-a")
	if !ok || rawProvider["api_key"] != "${PROVIDER_KEY}" || rawProvider["custom_provider"] != "keep-provider" {
		t.Fatalf("raw provider-a = %#v ok=%v", rawProvider, ok)
	}
	if got := stringSliceFromAny(rawProvider["endpoints"]); !reflect.DeepEqual(got, []string{"chat", "responses"}) {
		t.Fatalf("provider endpoints = %#v", rawProvider["endpoints"])
	}
	if got := app.Router.Backends["node-a"].BaseURL; got != "http://changed-provider.test/v1" {
		t.Fatalf("router base URL = %q", got)
	}
	saved, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(saved), "api_key: ${PROVIDER_KEY}") || strings.Contains(string(saved), "from-env") {
		t.Fatalf("saved config did not preserve provider placeholder:\n%s", saved)
	}

	resp = routingAdminRequest(app, http.MethodPut, "/admin/nodes/node-a", `{"endpoints":["embeddings"],"custom_node_update":"keep-node-update"}`, "pw")
	if resp.Code != http.StatusOK {
		t.Fatalf("PUT node endpoints status = %d body=%s", resp.Code, resp.Body.String())
	}
	rawNode, ok := app.Config.RawNode("node-a")
	if !ok || rawNode["custom_node"] != "keep-node" || rawNode["custom_node_update"] != "keep-node-update" {
		t.Fatalf("raw node-a = %#v ok=%v", rawNode, ok)
	}
	if got := stringSliceFromAny(rawNode["endpoints"]); !reflect.DeepEqual(got, []string{"embeddings"}) {
		t.Fatalf("node endpoints = %#v", rawNode["endpoints"])
	}
	if got := app.Router.Backends["node-a"].Endpoints; !reflect.DeepEqual(got, []string{"embeddings"}) {
		t.Fatalf("router node endpoints = %#v", got)
	}

	resp = routingAdminRequest(app, http.MethodPut, "/admin/nodes/node-a", `{"endpoints":["bad endpoint"]}`, "pw")
	if resp.Code != http.StatusBadRequest {
		t.Fatalf("invalid node endpoints status = %d body=%s", resp.Code, resp.Body.String())
	}
	rawNode, ok = app.Config.RawNode("node-a")
	if !ok {
		t.Fatal("node-a missing after failed endpoint update")
	}
	if got := stringSliceFromAny(rawNode["endpoints"]); !reflect.DeepEqual(got, []string{"embeddings"}) {
		t.Fatalf("node endpoints mutated after failed validation: %#v", rawNode)
	}
	if got := app.Router.Backends["node-a"].Endpoints; !reflect.DeepEqual(got, []string{"embeddings"}) {
		t.Fatalf("router endpoints mutated after failed node update: %#v", got)
	}

	resp = routingAdminRequest(app, http.MethodPut, "/admin/client-models/client-a", `{"node_weights":{"node-a":7},"balancing":"round_robin"}`, "pw")
	if resp.Code != http.StatusOK {
		t.Fatalf("PUT client model weights status = %d body=%s", resp.Code, resp.Body.String())
	}
	rawClient, ok := app.Config.RawClientModel("client-a")
	if !ok || rawClient["custom_client"] != "keep-client" || rawClient["balancing"] != "round_robin" {
		t.Fatalf("raw client-a = %#v ok=%v", rawClient, ok)
	}
	if got := intMapFromAny(rawClient["node_weights"]); !reflect.DeepEqual(got, map[string]int{"node-a": 7}) {
		t.Fatalf("client-a node_weights = %#v", rawClient["node_weights"])
	}

	resp = routingAdminRequest(app, http.MethodPut, "/admin/client-models/client-a", `{"node_weights":{"node-a":2,"missing-node":5}}`, "pw")
	if resp.Code != http.StatusOK {
		t.Fatalf("replace client model weights status = %d body=%s", resp.Code, resp.Body.String())
	}
	rawClient, ok = app.Config.RawClientModel("client-a")
	if !ok {
		t.Fatal("client-a missing after weight replacement")
	}
	if got := intMapFromAny(rawClient["node_weights"]); !reflect.DeepEqual(got, map[string]int{"node-a": 2, "missing-node": 5}) {
		t.Fatalf("client-a node_weights were not replaced: %#v", rawClient["node_weights"])
	}

	resp = routingAdminRequest(app, http.MethodPut, "/admin/client-models/client-a", `{"node_weights":{"node-a":-1},"balancing":"bad"}`, "pw")
	if resp.Code != http.StatusBadRequest {
		t.Fatalf("invalid client model weights status = %d body=%s", resp.Code, resp.Body.String())
	}
	rawClient, ok = app.Config.RawClientModel("client-a")
	if !ok {
		t.Fatal("client-a missing after failed weight update")
	}
	if got := intMapFromAny(rawClient["node_weights"]); !reflect.DeepEqual(got, map[string]int{"node-a": 2, "missing-node": 5}) {
		t.Fatalf("client-a node_weights mutated after failed validation: %#v", rawClient)
	}
	if rawClient["balancing"] != "round_robin" {
		t.Fatalf("client-a balancing mutated after failed validation: %#v", rawClient)
	}

	resp = routingAdminRequest(app, http.MethodPost, "/admin/client-models", `{"name":"client-b","nodes":["node-a"],"unknown_client":"keep-client-b"}`, "pw")
	if resp.Code != http.StatusOK {
		t.Fatalf("POST client model status = %d body=%s", resp.Code, resp.Body.String())
	}
	if _, ok := app.Config.RawClientModel("client-b"); !ok {
		t.Fatal("client-b missing after create")
	}

	resp = routingAdminRequest(app, http.MethodPut, "/admin/client-models/client-b", `{"nodes":["missing-node"]}`, "pw")
	if resp.Code != http.StatusBadRequest {
		t.Fatalf("invalid client model update status = %d body=%s", resp.Code, resp.Body.String())
	}
	rawClient, ok = app.Config.RawClientModel("client-b")
	if !ok {
		t.Fatal("client-b missing after failed update")
	}
	if got := stringSliceFromAny(rawClient["nodes"]); !reflect.DeepEqual(got, []string{"node-a"}) {
		t.Fatalf("client-b nodes mutated after failed validation: %#v", rawClient)
	}

	resp = routingAdminRequest(app, http.MethodDelete, "/admin/providers/provider-a", "", "pw")
	if resp.Code != http.StatusConflict {
		t.Fatalf("provider delete conflict status = %d body=%s", resp.Code, resp.Body.String())
	}
	payload := decodeJSONMap(t, resp)
	if payload["error"].(map[string]any)["code"] != "provider_has_nodes" {
		t.Fatalf("provider conflict payload = %#v", payload)
	}

	resp = routingAdminRequest(app, http.MethodDelete, "/admin/nodes/node-a", "", "pw")
	if resp.Code != http.StatusConflict {
		t.Fatalf("node delete conflict status = %d body=%s", resp.Code, resp.Body.String())
	}
	payload = decodeJSONMap(t, resp)
	if payload["error"].(map[string]any)["code"] != "node_has_client_models" {
		t.Fatalf("node conflict payload = %#v", payload)
	}
}

func TestAdminClientModelDeleteCleansFallbacksAndValidationFailureDoesNotMutate(t *testing.T) {
	t.Setenv(config.AdminPasswordEnv, "pw")
	app, _ := newRoutingAdminTestApp(t)

	resp := routingAdminRequest(app, http.MethodPost, "/admin/client-models", `{"name":"client-b","nodes":["node-a"]}`, "pw")
	if resp.Code != http.StatusOK {
		t.Fatalf("POST client-b status = %d body=%s", resp.Code, resp.Body.String())
	}
	resp = routingAdminRequest(app, http.MethodDelete, "/admin/client-models/client-b", "", "pw")
	if resp.Code != http.StatusOK {
		t.Fatalf("DELETE client-b status = %d body=%s", resp.Code, resp.Body.String())
	}
	if _, ok := app.Config.RawClientModel("client-b"); ok {
		t.Fatal("client-b still exists after delete")
	}
	wantFallbacks := []map[string]any{{"client-a": []any{"legacy-alpha"}}}
	if !reflect.DeepEqual(app.Config.RouterSettings.Fallbacks, wantFallbacks) {
		t.Fatalf("fallbacks = %#v, want %#v", app.Config.RouterSettings.Fallbacks, wantFallbacks)
	}

	resp = routingAdminRequest(app, http.MethodPut, "/admin/nodes/node-a", `{"api_base":"not-a-url"}`, "pw")
	if resp.Code != http.StatusBadRequest {
		t.Fatalf("invalid node update status = %d body=%s", resp.Code, resp.Body.String())
	}
	rawNode, ok := app.Config.RawNode("node-a")
	if !ok {
		t.Fatal("node-a missing after failed update")
	}
	if _, exists := rawNode["api_base"]; exists {
		t.Fatalf("node raw mutated after failed validation: %#v", rawNode)
	}
	if got := app.Router.Backends["node-a"].BaseURL; got != "http://provider.test/v1" {
		t.Fatalf("router mutated after failed node update: %q", got)
	}
}

func TestAdminRouterSettingsFallbacksReplacePreservesRawAndRejectsInvalid(t *testing.T) {
	t.Setenv(config.AdminPasswordEnv, "pw")
	app, _ := newRoutingAdminTestApp(t)

	resp := routingAdminRequest(app, http.MethodGet, "/admin/router-settings", "", "")
	if resp.Code != http.StatusOK {
		t.Fatalf("GET router settings status = %d body=%s", resp.Code, resp.Body.String())
	}
	payload := decodeJSONMap(t, resp)
	fallbacks := payload["fallbacks"].([]any)
	if len(fallbacks) != 1 {
		t.Fatalf("GET fallbacks = %#v", fallbacks)
	}

	validBody := `{"fallbacks":[{"client-a":["legacy-alpha"]},{"legacy-alpha":["node-a"]}]}`
	resp = routingAdminRequest(app, http.MethodPut, "/admin/router-settings", validBody, "")
	if resp.Code != http.StatusForbidden {
		t.Fatalf("PUT router settings without password status = %d body=%s", resp.Code, resp.Body.String())
	}

	resp = routingAdminRequest(app, http.MethodPut, "/admin/router-settings", validBody, "pw")
	if resp.Code != http.StatusOK {
		t.Fatalf("PUT router settings status = %d body=%s", resp.Code, resp.Body.String())
	}
	wantFallbacks := []map[string]any{
		{"client-a": []any{"legacy-alpha"}},
		{"legacy-alpha": []any{"node-a"}},
	}
	if !reflect.DeepEqual(app.Config.RouterSettings.Fallbacks, wantFallbacks) {
		t.Fatalf("runtime fallbacks = %#v, want %#v", app.Config.RouterSettings.Fallbacks, wantFallbacks)
	}
	rawRouter := app.Config.Raw["router_settings"].(map[string]any)
	if rawRouter["custom_policy"] != "keep-router" {
		t.Fatalf("router_settings siblings were not preserved: %#v", rawRouter)
	}
	if jsonStringForTest(t, rawRouter["fallbacks"]) != jsonStringForTest(t, wantFallbacks) {
		t.Fatalf("raw fallbacks = %#v, want %#v", rawRouter["fallbacks"], wantFallbacks)
	}
	if !reflect.DeepEqual(app.Router.Fallbacks["client-a"], []string{"legacy-alpha"}) {
		t.Fatalf("router client-a fallbacks = %#v", app.Router.Fallbacks["client-a"])
	}
	if !reflect.DeepEqual(app.Router.Fallbacks["legacy-alpha"], []string{"node-a"}) {
		t.Fatalf("router legacy-alpha fallbacks = %#v", app.Router.Fallbacks["legacy-alpha"])
	}

	rawBefore := jsonStringForTest(t, rawRouter)
	routerBefore := cloneRouterFallbacksForTest(app.Router.Fallbacks)
	configFallbacksBefore := cloneRouterSettingsFallbacksForTest(t, app.Config.RouterSettings.Fallbacks)
	invalidBodies := []string{
		`{"fallbacks":[{"client-a":["missing"]}]}`,
		`{"fallbacks":[{"missing":["client-a"]}]}`,
		`{"fallbacks":[{"client-a":[12]}]}`,
		`{"fallbacks":[{"client-a":[]}]}`,
		`{"fallbacks":[{"client-a":["legacy-alpha"],"legacy-alpha":["client-a"]}]}`,
	}
	for _, body := range invalidBodies {
		resp = routingAdminRequest(app, http.MethodPut, "/admin/router-settings", body, "pw")
		if resp.Code != http.StatusBadRequest {
			t.Fatalf("invalid router settings status = %d body=%s", resp.Code, resp.Body.String())
		}
		payload = decodeJSONMap(t, resp)
		if payload["error"].(map[string]any)["code"] != "invalid_fallbacks" {
			t.Fatalf("invalid payload error = %#v", payload)
		}
		rawRouter = app.Config.Raw["router_settings"].(map[string]any)
		if got := jsonStringForTest(t, rawRouter); got != rawBefore {
			t.Fatalf("router raw mutated after invalid PUT:\nwant %s\ngot  %s", rawBefore, got)
		}
		if !reflect.DeepEqual(app.Config.RouterSettings.Fallbacks, configFallbacksBefore) {
			t.Fatalf("config fallbacks mutated after invalid PUT:\nwant %#v\ngot  %#v", configFallbacksBefore, app.Config.RouterSettings.Fallbacks)
		}
		if !reflect.DeepEqual(app.Router.Fallbacks, routerBefore) {
			t.Fatalf("router fallbacks mutated after invalid PUT:\nwant %#v\ngot  %#v", routerBefore, app.Router.Fallbacks)
		}
	}
}

func TestAdminRoutingNestedPUTRoundTripPreservesAndValidationDoesNotMutate(t *testing.T) {
	t.Setenv(config.AdminPasswordEnv, "pw")
	app, _ := newRoutingAdminTestApp(t)

	resp := routingAdminRequest(app, http.MethodPut, "/admin/providers/provider-a", `{"limits":{"rpm":77}}`, "pw")
	if resp.Code != http.StatusOK {
		t.Fatalf("PUT provider limits status = %d body=%s", resp.Code, resp.Body.String())
	}
	rawProvider, ok := app.Config.RawProvider("provider-a")
	if !ok {
		t.Fatal("provider-a missing")
	}
	if !reflect.DeepEqual(rawProvider["limits"], map[string]any{"rpm": float64(77)}) && !reflect.DeepEqual(rawProvider["limits"], map[string]any{"rpm": 77}) {
		t.Fatalf("provider limits = %#v", rawProvider["limits"])
	}
	if rawProvider["api_key"] != "${PROVIDER_KEY}" || rawProvider["custom_provider"] != "keep-provider" {
		t.Fatalf("provider omitted fields were not preserved: %#v", rawProvider)
	}
	providerBefore := cloneJSONMapForTest(t, rawProvider)
	routerURLBefore := app.Router.Backends["node-a"].BaseURL
	resp = routingAdminRequest(app, http.MethodPut, "/admin/providers/provider-a", `{"limits":{"rpm":-1},"api_base":"http://mutated-provider.test/v1"}`, "pw")
	if resp.Code != http.StatusBadRequest {
		t.Fatalf("invalid provider limits status = %d body=%s", resp.Code, resp.Body.String())
	}
	rawProvider, _ = app.Config.RawProvider("provider-a")
	if !reflect.DeepEqual(rawProvider, providerBefore) {
		t.Fatalf("provider mutated after failed validation:\nwant %#v\ngot  %#v", providerBefore, rawProvider)
	}
	if got := app.Router.Backends["node-a"].BaseURL; got != routerURLBefore {
		t.Fatalf("router provider base mutated after failed validation: %q", got)
	}

	resp = routingAdminRequest(app, http.MethodPut, "/admin/nodes/node-a", `{"limits":{"rps":2},"modules":{"response":["new-parser"]},"allowed_keys":"all"}`, "pw")
	if resp.Code != http.StatusOK {
		t.Fatalf("PUT node nested status = %d body=%s", resp.Code, resp.Body.String())
	}
	rawNode, ok := app.Config.RawNode("node-a")
	if !ok {
		t.Fatal("node-a missing")
	}
	if !reflect.DeepEqual(rawNode["limits"], map[string]any{"rps": float64(2)}) && !reflect.DeepEqual(rawNode["limits"], map[string]any{"rps": 2}) {
		t.Fatalf("node limits = %#v", rawNode["limits"])
	}
	if !reflect.DeepEqual(rawNode["modules"], map[string]any{"response": []any{"new-parser"}}) {
		t.Fatalf("node modules = %#v", rawNode["modules"])
	}
	if rawNode["allowed_keys"] != "all" || rawNode["custom_node"] != "keep-node" {
		t.Fatalf("node omitted fields were not preserved: %#v", rawNode)
	}
	nodeBefore := cloneJSONMapForTest(t, rawNode)
	resp = routingAdminRequest(app, http.MethodPut, "/admin/nodes/node-a", `{"modules":["bad"]}`, "pw")
	if resp.Code != http.StatusBadRequest {
		t.Fatalf("invalid node modules status = %d body=%s", resp.Code, resp.Body.String())
	}
	rawNode, _ = app.Config.RawNode("node-a")
	if !reflect.DeepEqual(rawNode, nodeBefore) {
		t.Fatalf("node mutated after failed update:\nwant %#v\ngot  %#v", nodeBefore, rawNode)
	}
	resp = routingAdminRequest(app, http.MethodPut, "/admin/nodes/node-a", `{"allowed_keys":{"key-a":true}}`, "pw")
	if resp.Code != http.StatusBadRequest {
		t.Fatalf("invalid node allowed_keys status = %d body=%s", resp.Code, resp.Body.String())
	}
	rawNode, _ = app.Config.RawNode("node-a")
	if !reflect.DeepEqual(rawNode, nodeBefore) {
		t.Fatalf("node mutated after failed allowed_keys validation:\nwant %#v\ngot  %#v", nodeBefore, rawNode)
	}
	resp = routingAdminRequest(app, http.MethodPut, "/admin/nodes/node-a", `{"limits":{"rpm":-1},"allowed_keys":["mutated"]}`, "pw")
	if resp.Code != http.StatusBadRequest {
		t.Fatalf("invalid node limits status = %d body=%s", resp.Code, resp.Body.String())
	}
	rawNode, _ = app.Config.RawNode("node-a")
	if !reflect.DeepEqual(rawNode, nodeBefore) {
		t.Fatalf("node mutated after failed validation:\nwant %#v\ngot  %#v", nodeBefore, rawNode)
	}

	resp = routingAdminRequest(app, http.MethodPut, "/admin/client-models/client-a", `{"limits":{"token_quota":55},"allowed_keys":["key-z"]}`, "pw")
	if resp.Code != http.StatusOK {
		t.Fatalf("PUT client model nested status = %d body=%s", resp.Code, resp.Body.String())
	}
	rawClient, ok := app.Config.RawClientModel("client-a")
	if !ok {
		t.Fatal("client-a missing")
	}
	if !reflect.DeepEqual(rawClient["limits"], map[string]any{"token_quota": float64(55)}) && !reflect.DeepEqual(rawClient["limits"], map[string]any{"token_quota": 55}) {
		t.Fatalf("client limits = %#v", rawClient["limits"])
	}
	if !reflect.DeepEqual(rawClient["allowed_keys"], []any{"key-z"}) || rawClient["custom_client"] != "keep-client" {
		t.Fatalf("client omitted fields were not preserved: %#v", rawClient)
	}
	clientBefore := cloneJSONMapForTest(t, rawClient)
	resp = routingAdminRequest(app, http.MethodPut, "/admin/client-models/client-a", `{"limits":{"concurrency":-1},"allowed_keys":["mutated"]}`, "pw")
	if resp.Code != http.StatusBadRequest {
		t.Fatalf("invalid client limits status = %d body=%s", resp.Code, resp.Body.String())
	}
	rawClient, _ = app.Config.RawClientModel("client-a")
	if !reflect.DeepEqual(rawClient, clientBefore) {
		t.Fatalf("client mutated after failed validation:\nwant %#v\ngot  %#v", clientBefore, rawClient)
	}
	resp = routingAdminRequest(app, http.MethodPut, "/admin/client-models/client-a", `{"allowed_keys":["key-a",12]}`, "pw")
	if resp.Code != http.StatusBadRequest {
		t.Fatalf("invalid client allowed_keys status = %d body=%s", resp.Code, resp.Body.String())
	}
	rawClient, _ = app.Config.RawClientModel("client-a")
	if !reflect.DeepEqual(rawClient, clientBefore) {
		t.Fatalf("client mutated after failed allowed_keys validation:\nwant %#v\ngot  %#v", clientBefore, rawClient)
	}
}

func newRoutingAdminTestApp(t *testing.T) (*App, string) {
	t.Helper()
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte("PROVIDER_KEY=from-env\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfgPath, []byte(`
model_list:
  - model_name: legacy-alpha
    protected: false
    model_params:
      api_base: http://legacy.test/v1
      model: openai/legacy-alpha
providers:
  - name: provider-a
    api_base: http://provider.test/v1
    api_key: ${PROVIDER_KEY}
    api_type: openai
    endpoints: [chat]
    custom_provider: keep-provider
nodes:
  - name: node-a
    provider: provider-a
    model: openai/upstream-a
    custom_node: keep-node
models:
  - name: client-a
    nodes: [node-a]
    node_weights:
      node-a: 3
    balancing: weighted
    custom_client: keep-client
router_settings:
  custom_policy: keep-router
  fallbacks:
    - client-a: [client-b, legacy-alpha]
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
	return app, cfgPath
}

func routingAdminRequest(app *App, method string, path string, body string, password string) *httptest.ResponseRecorder {
	resp := httptest.NewRecorder()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if password != "" {
		req.Header.Set("x-admin-password", password)
	}
	app.ServeHTTP(resp, req)
	return resp
}

func cloneJSONMapForTest(t *testing.T, value map[string]any) map[string]any {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func jsonStringForTest(t *testing.T, value any) string {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func intMapFromAny(value any) map[string]int {
	out := map[string]int{}
	raw, ok := value.(map[string]any)
	if !ok {
		return out
	}
	for key, value := range raw {
		switch typed := value.(type) {
		case int:
			out[key] = typed
		case int64:
			out[key] = int(typed)
		case float64:
			out[key] = int(typed)
		}
	}
	return out
}

func cloneRouterFallbacksForTest(value map[string][]string) map[string][]string {
	out := make(map[string][]string, len(value))
	for key, targets := range value {
		out[key] = append([]string(nil), targets...)
	}
	return out
}

func cloneRouterSettingsFallbacksForTest(t *testing.T, value []map[string]any) []map[string]any {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	var out []map[string]any
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatal(err)
	}
	return out
}
