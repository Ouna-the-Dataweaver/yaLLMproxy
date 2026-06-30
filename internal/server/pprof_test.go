package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/theio/yallmp-proxy/internal/config"
)

func TestPprofDisabledByDefault(t *testing.T) {
	t.Setenv(config.AdminPasswordEnv, "secret")
	app := newPprofTestApp(t, nil)

	resp := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/debug/pprof/", nil)
	req.Header.Set("x-admin-password", "secret")
	app.ServeHTTP(resp, req)

	if resp.Code != http.StatusNotFound {
		t.Fatalf("status = %d body=%s", resp.Code, resp.Body.String())
	}
}

func TestPprofEnabledRequiresConfiguredAdminPassword(t *testing.T) {
	t.Setenv(config.AdminPasswordEnv, "")
	app := newPprofTestApp(t, true)

	resp := httptest.NewRecorder()
	app.ServeHTTP(resp, httptest.NewRequest(http.MethodGet, "/debug/pprof/", nil))

	if resp.Code != http.StatusForbidden {
		t.Fatalf("status = %d body=%s", resp.Code, resp.Body.String())
	}
}

func TestPprofEnabledRejectsMissingOrInvalidAdminPassword(t *testing.T) {
	t.Setenv(config.AdminPasswordEnv, "secret")
	app := newPprofTestApp(t, true)

	missing := httptest.NewRecorder()
	app.ServeHTTP(missing, httptest.NewRequest(http.MethodGet, "/debug/pprof/", nil))
	if missing.Code != http.StatusForbidden {
		t.Fatalf("missing password status = %d body=%s", missing.Code, missing.Body.String())
	}

	invalid := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/debug/pprof/", nil)
	req.Header.Set("x-admin-password", "wrong")
	app.ServeHTTP(invalid, req)
	if invalid.Code != http.StatusForbidden {
		t.Fatalf("invalid password status = %d body=%s", invalid.Code, invalid.Body.String())
	}
}

func TestPprofEnabledServesIndexWithHeaderPassword(t *testing.T) {
	t.Setenv(config.AdminPasswordEnv, "secret")
	app := newPprofTestApp(t, "true")

	resp := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/debug/pprof/", nil)
	req.Header.Set("x-admin-password", "secret")
	app.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", resp.Code, resp.Body.String())
	}
	if !strings.Contains(resp.Body.String(), "goroutine") {
		t.Fatalf("body does not look like pprof index: %s", resp.Body.String())
	}
}

func TestPprofEnabledRejectsNonGet(t *testing.T) {
	t.Setenv(config.AdminPasswordEnv, "secret")
	app := newPprofTestApp(t, true)

	resp := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/debug/pprof/profile", nil)
	req.Header.Set("x-admin-password", "secret")
	app.ServeHTTP(resp, req)

	if resp.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d body=%s", resp.Code, resp.Body.String())
	}
}

func TestPprofInvalidEnabledValueDoesNotEnable(t *testing.T) {
	t.Setenv(config.AdminPasswordEnv, "secret")
	app := newPprofTestApp(t, []any{"true"})

	resp := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/debug/pprof/", nil)
	req.Header.Set("x-admin-password", "secret")
	app.ServeHTTP(resp, req)

	if resp.Code != http.StatusNotFound {
		t.Fatalf("status = %d body=%s", resp.Code, resp.Body.String())
	}
	issues := app.Config.Validate()
	if len(issues) != 1 || issues[0].Path != "proxy_settings.profiling.enabled" || issues[0].Code != "invalid_bool" {
		t.Fatalf("issues = %#v", issues)
	}
}

func TestPprofEnabledServesStandardSubpathWithQueryPassword(t *testing.T) {
	t.Setenv(config.AdminPasswordEnv, "secret")
	app := newPprofTestApp(t, true)

	resp := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/debug/pprof/cmdline?admin_password=secret", nil)
	app.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", resp.Code, resp.Body.String())
	}
	if resp.Body.Len() == 0 {
		t.Fatalf("empty cmdline response")
	}
}

func TestPprofEnabledServesRuntimeProfileWithQueryPassword(t *testing.T) {
	t.Setenv(config.AdminPasswordEnv, "secret")
	app := newPprofTestApp(t, true)

	resp := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/debug/pprof/goroutine?debug=1&admin_password=secret", nil)
	app.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", resp.Code, resp.Body.String())
	}
	if !strings.Contains(resp.Body.String(), "goroutine profile") {
		t.Fatalf("body does not look like goroutine profile: %s", resp.Body.String())
	}
}

func TestPprofEnabledUnknownProfileRequiresPasswordThen404(t *testing.T) {
	t.Setenv(config.AdminPasswordEnv, "secret")
	app := newPprofTestApp(t, true)

	unauthorized := httptest.NewRecorder()
	app.ServeHTTP(unauthorized, httptest.NewRequest(http.MethodGet, "/debug/pprof/not-a-profile", nil))
	if unauthorized.Code != http.StatusForbidden {
		t.Fatalf("unauthorized status = %d body=%s", unauthorized.Code, unauthorized.Body.String())
	}

	authorized := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/debug/pprof/not-a-profile", nil)
	req.Header.Set("x-admin-password", "secret")
	app.ServeHTTP(authorized, req)
	if authorized.Code != http.StatusNotFound {
		t.Fatalf("authorized status = %d body=%s", authorized.Code, authorized.Body.String())
	}
}

func newPprofTestApp(t *testing.T, profilingEnabled any) *App {
	t.Helper()
	raw := map[string]any{
		"model_list": []map[string]any{{
			"model_name": "alpha",
			"model_params": map[string]any{
				"api_base": "http://upstream.test/v1",
				"api_key":  "upstream-key",
				"model":    "openai/upstream-model",
			},
		}},
	}
	if profilingEnabled != nil {
		raw["proxy_settings"] = map[string]any{
			"profiling": map[string]any{
				"enabled": profilingEnabled,
			},
		}
	}
	cfg, err := config.FromMap(raw, "")
	if err != nil {
		t.Fatal(err)
	}
	app, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return app
}
