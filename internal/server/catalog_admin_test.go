package server

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/theio/yallmp-proxy/internal/config"
)

func TestAdminCatalogRequiresAdminPassword(t *testing.T) {
	t.Setenv(config.AdminPasswordEnv, "pw")
	app, err := New(catalogTestConfig())
	if err != nil {
		t.Fatal(err)
	}
	resp := httptest.NewRecorder()
	app.ServeHTTP(resp, httptest.NewRequest(http.MethodGet, "/admin/catalog", nil))
	if resp.Code != http.StatusForbidden {
		t.Fatalf("status = %d body=%s, want forbidden", resp.Code, resp.Body.String())
	}
}

func TestAdminCatalogReturnsReadOnlyRedactedSnapshot(t *testing.T) {
	t.Setenv(config.AdminPasswordEnv, "pw")
	app, err := New(catalogTestConfig())
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/admin/catalog", nil)
	req.Header.Set("x-admin-password", "pw")
	resp := httptest.NewRecorder()
	app.ServeHTTP(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s, want ok", resp.Code, resp.Body.String())
	}
	body := resp.Body.String()
	for _, want := range []string{`"enabled":false`, `"model":"client-model"`, `"node":"node-a"`, `"provider":"provider-a"`, `"warnings"`} {
		if !strings.Contains(body, want) {
			t.Fatalf("response missing %s: %s", want, body)
		}
	}
	for _, forbidden := range []string{"provider-secret", "api_key", "Authorization", "headers", "prompt"} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("response leaked %q: %s", forbidden, body)
		}
	}
}

func TestStartModelCatalogSurvivesReload(t *testing.T) {
	app, err := New(catalogReloadTestConfig("initial"))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := app.StartModelCatalog(ctx)
	defer func() {
		cancel()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatal("model catalog loop did not stop")
		}
	}()

	for i := 0; i < 25; i++ {
		if err := app.applyConfig(catalogReloadTestConfig(fmt.Sprintf("reload-%d", i)), false); err != nil {
			t.Fatal(err)
		}
		time.Sleep(time.Millisecond)
	}
}

func catalogTestConfig() *config.Config {
	return &config.Config{
		Providers: []config.ProviderEntry{{
			Name:    "provider-a",
			APIBase: "https://provider.test/v1",
			APIKey:  "provider-secret",
		}},
		Nodes: []config.NodeEntry{{
			Name:     "node-a",
			Provider: "provider-a",
		}},
		Models: []config.ClientModelEntry{{
			Name:  "client-model",
			Nodes: []string{"node-a"},
		}},
	}
}

func catalogReloadTestConfig(model string) *config.Config {
	return &config.Config{
		ProxySettings: config.ProxySettings{ModelCatalog: config.ModelCatalogConfig{
			Enabled:              true,
			ProbeIntervalSeconds: 0.001,
		}},
		ModelList: []config.ModelEntry{{
			ModelName: model,
			ModelParams: map[string]any{
				"api_base": "https://upstream.test/v1",
			},
		}},
	}
}
