package policy

import (
	"errors"
	"net/http/httptest"
	"testing"

	"github.com/theio/yallmp-proxy/internal/config"
)

func TestValidateRequestDisabledAuthAllowsRequest(t *testing.T) {
	ctx, err := NewManager(&config.Config{}).ValidateRequest(httptest.NewRequest("POST", "/", nil), "m")
	if err != nil {
		t.Fatal(err)
	}
	if ctx.Authenticated {
		t.Fatalf("authenticated = true")
	}
}

func TestValidateRequestBearerKeyAndModelAccess(t *testing.T) {
	enabled := true
	manager := NewManager(&config.Config{AppKeys: config.AppKeysConfig{
		Enabled: true,
		Keys: []config.AppKeyEntry{{
			KeyID:   "key-1",
			Name:    "dev",
			Secret:  "secret",
			Enabled: &enabled,
			Models:  []string{"alpha"},
		}},
	}})
	req := httptest.NewRequest("POST", "/", nil)
	req.Header.Set("Authorization", "Bearer secret")
	ctx, err := manager.ValidateRequest(req, "alpha")
	if err != nil {
		t.Fatal(err)
	}
	if !ctx.Authenticated || ctx.KeyID != "key-1" {
		t.Fatalf("ctx = %#v", ctx)
	}
	_, err = manager.ValidateRequest(req, "beta")
	if !errors.Is(err, ErrModelAccessDenied) {
		t.Fatalf("err = %v", err)
	}
}

func TestValidateRequestEnforcesLegacyModelAccessControl(t *testing.T) {
	manager := NewManager(&config.Config{
		ModelList: []config.ModelEntry{{
			ModelName: "alpha",
			AccessControl: map[string]any{
				"allowed_keys": []any{"key-1"},
			},
		}},
		AppKeys: config.AppKeysConfig{
			Enabled: true,
			Keys: []config.AppKeyEntry{
				{KeyID: "key-1", Secret: "secret-1"},
				{KeyID: "key-2", Secret: "secret-2"},
			},
		},
	})
	req := httptest.NewRequest("POST", "/", nil)
	req.Header.Set("x-api-key", "secret-1")
	if _, err := manager.ValidateRequest(req, "alpha"); err != nil {
		t.Fatal(err)
	}
	req.Header.Set("x-api-key", "secret-2")
	_, err := manager.ValidateRequest(req, "alpha")
	if !errors.Is(err, ErrModelAccessDenied) {
		t.Fatalf("err = %v", err)
	}
}

func TestValidateRequestEnforcesClientModelAllowedKeys(t *testing.T) {
	manager := NewManager(&config.Config{
		Models: []config.ClientModelEntry{{
			Name:        "alpha",
			AllowedKeys: []any{"key-1"},
		}},
		AppKeys: config.AppKeysConfig{
			Enabled: true,
			Keys: []config.AppKeyEntry{
				{KeyID: "key-1", Secret: "secret-1"},
				{KeyID: "key-2", Secret: "secret-2"},
			},
		},
	})
	req := httptest.NewRequest("POST", "/", nil)
	req.Header.Set("x-api-key", "secret-1")
	if _, err := manager.ValidateRequest(req, "alpha"); err != nil {
		t.Fatal(err)
	}
	req.Header.Set("x-api-key", "secret-2")
	_, err := manager.ValidateRequest(req, "alpha")
	if !errors.Is(err, ErrModelAccessDenied) {
		t.Fatalf("err = %v", err)
	}
}

func TestValidateRequestRejectsMissingAndInvalid(t *testing.T) {
	manager := NewManager(&config.Config{AppKeys: config.AppKeysConfig{
		Enabled: true,
		Keys:    []config.AppKeyEntry{{KeyID: "key-1", Secret: "secret"}},
	}})
	_, err := manager.ValidateRequest(httptest.NewRequest("POST", "/", nil), "alpha")
	if !errors.Is(err, ErrMissingAPIKey) {
		t.Fatalf("missing err = %v", err)
	}
	req := httptest.NewRequest("POST", "/", nil)
	req.Header.Set("x-api-key", "wrong")
	_, err = manager.ValidateRequest(req, "alpha")
	if !errors.Is(err, ErrInvalidAPIKey) {
		t.Fatalf("invalid err = %v", err)
	}
}

func TestValidateRequestAppliesUnauthenticatedLimits(t *testing.T) {
	cfg, err := config.FromMap(map[string]any{
		"app_keys": map[string]any{
			"enabled":               true,
			"allow_unauthenticated": true,
			"unauthenticated": map[string]any{
				"concurrency_limit": 3,
				"priority":          1000,
				"queue_timeout":     2.5,
				"token_quota":       50,
			},
		},
	}, "")
	if err != nil {
		t.Fatal(err)
	}

	ctx, err := NewManager(cfg).ValidateRequest(httptest.NewRequest("POST", "/", nil), "alpha")
	if err != nil {
		t.Fatal(err)
	}
	if ctx.Authenticated {
		t.Fatalf("authenticated = true")
	}
	if ctx.LimitKeyID() != "" {
		t.Fatalf("limit key = %q", ctx.LimitKeyID())
	}
	if ctx.Limits.Concurrency != 3 || ctx.Limits.Priority != 1000 || ctx.Limits.QueueTimeoutSeconds != 2.5 || ctx.Limits.TokenQuota != 50 {
		t.Fatalf("limits = %#v", ctx.Limits)
	}
}

func TestValidateRequestAppliesDefaultsAndLegacyKeyLimits(t *testing.T) {
	cfg, err := config.FromMap(map[string]any{
		"app_keys": map[string]any{
			"enabled": true,
			"defaults": map[string]any{
				"concurrency_limit": 4,
				"priority":          100,
				"queue_timeout":     5,
				"rps":               2.5,
			},
			"keys": []any{map[string]any{
				"key_id":            "key-1",
				"secret":            "secret",
				"concurrency_limit": 2,
				"priority":          25,
				"queue_timeout":     1.5,
			}},
		},
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("POST", "/", nil)
	req.Header.Set("x-api-key", "secret")

	ctx, err := NewManager(cfg).ValidateRequest(req, "alpha")
	if err != nil {
		t.Fatal(err)
	}
	if ctx.Limits.Concurrency != 2 || ctx.Limits.Priority != 25 || ctx.Limits.QueueTimeoutSeconds != 1.5 || ctx.Limits.RPS != 2.5 {
		t.Fatalf("limits = %#v", ctx.Limits)
	}
}

func TestValidateRequestMergesGroupAndKeyPolicy(t *testing.T) {
	enabled := true
	manager := NewManager(&config.Config{AppKeys: config.AppKeysConfig{
		Enabled: true,
		Groups: []config.AppKeyGroupEntry{{
			GroupID: "team-a",
			Name:    "Team A",
			Enabled: &enabled,
			Models:  []string{"alpha", "beta"},
			Limits: config.LimitsConfig{
				RPS:        1,
				TokenQuota: 100,
				Priority:   10,
			},
		}},
		Keys: []config.AppKeyEntry{{
			KeyID:   "key-1",
			GroupID: "team-a",
			Name:    "dev",
			Secret:  "secret",
			Enabled: &enabled,
			Models:  []string{"beta"},
			Limits: config.LimitsConfig{
				Concurrency: 2,
				TokenQuota:  50,
			},
		}},
	}})
	req := httptest.NewRequest("POST", "/", nil)
	req.Header.Set("x-api-key", "secret")

	ctx, err := manager.ValidateRequest(req, "beta")
	if err != nil {
		t.Fatal(err)
	}
	if !ctx.Authenticated || ctx.KeyID != "key-1" || ctx.GroupID != "team-a" || ctx.GroupName != "Team A" {
		t.Fatalf("ctx = %#v", ctx)
	}
	if ctx.LimitKeyID() != "group:team-a" {
		t.Fatalf("limit scope key = %q", ctx.LimitKeyID())
	}
	if ctx.Limits.RPS != 1 || ctx.Limits.Concurrency != 2 || ctx.Limits.TokenQuota != 50 || ctx.Limits.Priority != 10 {
		t.Fatalf("limits = %#v", ctx.Limits)
	}

	_, err = manager.ValidateRequest(req, "alpha")
	if !errors.Is(err, ErrModelAccessDenied) {
		t.Fatalf("alpha err = %v", err)
	}
}

func TestRequiredHeaderPolicyMergesGlobalGroupAndKeyRules(t *testing.T) {
	enabled := true
	manager := NewManager(&config.Config{AppKeys: config.AppKeysConfig{
		Enabled: true,
		RequiredHeaders: []config.RequiredHeader{{
			Name:  "X-Global-Tenant",
			LogAs: "tenant",
		}},
		Groups: []config.AppKeyGroupEntry{{
			GroupID: "team-a",
			Enabled: &enabled,
			RequiredHeaders: []config.RequiredHeader{{
				Name:   "X-Team-Project",
				LogAs:  "project",
				Models: []string{"beta"},
			}},
		}},
		Keys: []config.AppKeyEntry{{
			KeyID:   "key-1",
			GroupID: "team-a",
			Secret:  "secret",
			Enabled: &enabled,
			RequiredHeaders: []config.RequiredHeader{{
				Name:   "X-Key-Workload",
				LogAs:  "workload",
				Models: []string{"alpha"},
			}},
		}},
	}})

	req := httptest.NewRequest("POST", "/", nil)
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set("X-Global-Tenant", "acme")
	_, err := manager.ValidateRequest(req, "alpha")
	if !errors.Is(err, ErrMissingRequiredHeader) {
		t.Fatalf("alpha missing key-scoped header err = %v", err)
	}

	req.Header.Set("X-Key-Workload", "batch")
	ctx, err := manager.ValidateRequest(req, "alpha")
	if err != nil {
		t.Fatal(err)
	}
	if ctx.RequiredHeaders["tenant"] != "acme" || ctx.RequiredHeaders["workload"] != "batch" {
		t.Fatalf("required header attribution = %#v", ctx.RequiredHeaders)
	}
	if _, ok := ctx.RequiredHeaders["project"]; ok {
		t.Fatalf("beta-scoped header attribution was captured for alpha: %#v", ctx.RequiredHeaders)
	}

	delete(req.Header, "X-Key-Workload")
	_, err = manager.ValidateRequest(req, "beta")
	if !errors.Is(err, ErrMissingRequiredHeader) {
		t.Fatalf("beta missing group-scoped header err = %v", err)
	}
	req.Header.Set("X-Team-Project", "search")
	ctx, err = manager.ValidateRequest(req, "beta")
	if err != nil {
		t.Fatal(err)
	}
	if ctx.RequiredHeaders["tenant"] != "acme" || ctx.RequiredHeaders["project"] != "search" {
		t.Fatalf("required header attribution = %#v", ctx.RequiredHeaders)
	}
}

func TestRequiredHeaderPolicyAppliesToAllowedUnauthenticatedRequests(t *testing.T) {
	manager := NewManager(&config.Config{AppKeys: config.AppKeysConfig{
		Enabled:              true,
		AllowUnauthenticated: true,
		RequiredHeaders: []config.RequiredHeader{{
			Name:  "X-Tenant-ID",
			LogAs: "tenant",
		}},
	}})
	req := httptest.NewRequest("POST", "/", nil)
	_, err := manager.ValidateRequest(req, "alpha")
	if !errors.Is(err, ErrMissingRequiredHeader) {
		t.Fatalf("missing header err = %v", err)
	}
	req.Header.Set("X-Tenant-ID", "acme")
	ctx, err := manager.ValidateRequest(req, "alpha")
	if err != nil {
		t.Fatal(err)
	}
	if ctx.Authenticated || ctx.RequiredHeaders["tenant"] != "acme" {
		t.Fatalf("ctx = %#v", ctx)
	}
}

func TestRequiredHeaderPolicyTrimsConfiguredHeaderNameAtRuntime(t *testing.T) {
	manager := NewManager(&config.Config{AppKeys: config.AppKeysConfig{
		Enabled:              true,
		AllowUnauthenticated: true,
		RequiredHeaders: []config.RequiredHeader{{
			Name:  " X-Tenant-ID ",
			LogAs: "tenant",
		}},
	}})
	req := httptest.NewRequest("POST", "/", nil)
	req.Header.Set("X-Tenant-ID", "acme")

	ctx, err := manager.ValidateRequest(req, "alpha")
	if err != nil {
		t.Fatal(err)
	}
	if ctx.RequiredHeaders["tenant"] != "acme" {
		t.Fatalf("required header attribution = %#v", ctx.RequiredHeaders)
	}
}

func TestValidateRequestParsesGroupedSecretIDs(t *testing.T) {
	secret := "yallmp-0123456789abcdef-abcdef0123456789"
	manager := NewManager(&config.Config{AppKeys: config.AppKeysConfig{
		Enabled: true,
		Keys:    []config.AppKeyEntry{{Secret: secret}},
	}})
	req := httptest.NewRequest("POST", "/", nil)
	req.Header.Set("Authorization", "Bearer "+secret)

	ctx, err := manager.ValidateRequest(req, "alpha")
	if err != nil {
		t.Fatal(err)
	}
	if ctx.KeyID != "abcdef0123456789" || ctx.GroupID != "0123456789abcdef" {
		t.Fatalf("ctx = %#v", ctx)
	}
	if ctx.LimitKeyID() != "group:0123456789abcdef" {
		t.Fatalf("limit scope key = %q", ctx.LimitKeyID())
	}
}

func TestLimitKeyIDForStoredKeyMapsConcreteKeyToGroup(t *testing.T) {
	cfg := config.AppKeysConfig{Keys: []config.AppKeyEntry{
		{KeyID: "key-1", GroupID: "team-a", Secret: "secret"},
		{Secret: "yallmp-0123456789abcdef-abcdef0123456789"},
	}}
	if got := LimitKeyIDForStoredKey(cfg, "key-1"); got != "group:team-a" {
		t.Fatalf("explicit group scope = %q", got)
	}
	if got := LimitKeyIDForStoredKey(cfg, "abcdef0123456789"); got != "group:0123456789abcdef" {
		t.Fatalf("parsed group scope = %q", got)
	}
	if got := LimitKeyIDForStoredKey(cfg, "missing"); got != "missing" {
		t.Fatalf("missing scope = %q", got)
	}
}
