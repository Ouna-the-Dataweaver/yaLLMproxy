package server

import (
	"context"
	"testing"

	"github.com/theio/yallmp-proxy/internal/admission"
	"github.com/theio/yallmp-proxy/internal/config"
	"github.com/theio/yallmp-proxy/internal/proxy"
)

func TestRoutingSkipsFullReplicaAndRetainsAllFullQueueFallback(t *testing.T) {
	for _, aware := range []bool{false, true} {
		router, err := proxy.NewRouter(&config.Config{
			Nodes: []config.NodeEntry{
				{Name: "large", APIBase: "http://large.test/v1", Model: "m", Limits: config.LimitsConfig{Concurrency: 4}},
				{Name: "small", APIBase: "http://small.test/v1", Model: "m", Limits: config.LimitsConfig{Concurrency: 2}},
			},
			RouterSettings: config.RouterSettings{ConcurrencyAwareSelection: config.ConcurrencyAwareSelectionSettings{Enabled: aware}},
		})
		if err != nil {
			t.Fatal(err)
		}
		app := &App{Admission: admission.NewManager()}
		acquire := func(name string, count int) {
			t.Helper()
			backend := router.Backends[name]
			for i := 0; i < count; i++ {
				slot, err := app.Admission.Acquire(context.Background(), backendAdmissionScope(backend), backend.Limits.Concurrency, 100, "")
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(slot.Release)
			}
		}
		acquire("small", 2)
		for i := 0; i < 6; i++ {
			route, err := router.BuildRouteForKeyWithOptions("m", "/v1/chat/completions", nil, "", proxy.RouteOptions{BackendAvailable: app.backendRateLimitAvailable})
			if err != nil || len(route) != 1 || route[0].Name != "large" {
				t.Fatalf("aware=%v: available replica route=%v err=%v", aware, route, err)
			}
		}
		acquire("large", 4)
		route, err := router.BuildRouteForKeyWithOptions("m", "/v1/chat/completions", nil, "", proxy.RouteOptions{BackendAvailable: app.backendRateLimitAvailable})
		if err != nil || len(route) != 2 {
			t.Fatalf("aware=%v: all-full queue fallback route=%v err=%v", aware, route, err)
		}
	}
}

func TestBackendCapacityAvailabilityUsesSharedScope(t *testing.T) {
	app := &App{Admission: admission.NewManager()}
	first := proxy.Backend{Name: "alias-a", RateLimitScope: "provider/shared", Limits: config.LimitsConfig{Concurrency: 1}}
	second := first
	second.Name = "alias-b"
	if !app.backendRateLimitAvailable(first) {
		t.Fatal("unused scope unavailable")
	}
	release, err := app.backendAttemptLimiter(context.Background(), first)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if app.backendRateLimitAvailable(second) {
		t.Fatal("alias ignored shared concurrency capacity")
	}
	release()
	if !app.backendRateLimitAvailable(second) {
		t.Fatal("released capacity remains unavailable")
	}
}
