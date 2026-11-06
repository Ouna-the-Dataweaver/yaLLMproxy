package proxy

import (
	"context"
	"errors"
	"net/http"
	"sync/atomic"
	"testing"

	"github.com/theio/yallmp-proxy/internal/config"
)

type cancellationRoundTripper func(*http.Request) (*http.Response, error)

func (f cancellationRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestRouterCancellationStopsFailoverWithoutFailingHealth(t *testing.T) {
	for _, retries := range []int{1, 3} {
		router, err := NewRouter(&config.Config{
			Nodes: []config.NodeEntry{
				{Name: "a", Model: "m", APIBase: "http://a.test/v1"},
				{Name: "b", Model: "m", APIBase: "http://b.test/v1"},
			},
			RouterSettings: config.RouterSettings{NumRetries: retries},
		})
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		var calls atomic.Int32
		router.Client = &http.Client{Transport: cancellationRoundTripper(func(*http.Request) (*http.Response, error) {
			calls.Add(1)
			cancel()
			return nil, context.Canceled
		})}
		var admissions int
		result, err := router.ForwardWithOptions(ctx, "m", "/v1/chat/completions", "", []byte(`{"model":"m"}`), map[string]any{"model": "m"}, false, nil, ForwardOptions{
			BeforeAttempt: func(context.Context, Backend) (func(), error) {
				admissions++
				return func() {}, nil
			},
		})
		cancel()
		if !errors.Is(err, context.Canceled) || calls.Load() != 1 || admissions != 1 || len(result.AttemptTrail) != 1 {
			t.Fatalf("retries=%d err=%v calls=%d admissions=%d trail=%+v", retries, err, calls.Load(), admissions, result.AttemptTrail)
		}
		for _, health := range router.HealthSnapshot() {
			if health.FailureCount != 0 {
				t.Fatalf("canceled client request failed backend health: %+v", health)
			}
		}
	}
}

func TestRouterCancellationDuringAdmissionStopsFailover(t *testing.T) {
	router, err := NewRouter(&config.Config{Nodes: []config.NodeEntry{
		{Name: "a", Model: "m", APIBase: "http://a.test/v1"},
		{Name: "b", Model: "m", APIBase: "http://b.test/v1"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	admissions := 0
	result, err := router.ForwardWithOptions(ctx, "m", "/v1/chat/completions", "", []byte(`{"model":"m"}`), map[string]any{"model": "m"}, false, nil, ForwardOptions{
		BeforeAttempt: func(context.Context, Backend) (func(), error) {
			admissions++
			cancel()
			return nil, context.Canceled
		},
	})
	if !errors.Is(err, context.Canceled) || admissions != 1 || len(result.AttemptTrail) != 1 {
		t.Fatalf("err=%v admissions=%d trail=%+v", err, admissions, result.AttemptTrail)
	}
}
