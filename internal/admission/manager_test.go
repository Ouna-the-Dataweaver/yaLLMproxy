package admission

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

func TestAcquireImmediateAndReleaseIdempotent(t *testing.T) {
	m := NewManager()
	slot, err := m.Acquire(context.Background(), "key", 2, 100, "model")
	if err != nil {
		t.Fatal(err)
	}
	if slot.scope != "key::model" {
		t.Fatalf("scope = %s", slot.scope)
	}
	slot.Release()
	slot.Release()
	if got := m.Metrics().ActiveByScope["key::model"]; got != 0 {
		t.Fatalf("active = %d", got)
	}
}

func TestAcquireTimeout(t *testing.T) {
	m := NewManager()
	slot, err := m.Acquire(context.Background(), "key", 1, 100, "")
	if err != nil {
		t.Fatal(err)
	}
	defer slot.Release()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err = m.Acquire(ctx, "key", 1, 100, "")
	if !errors.Is(err, ErrQueueTimeout) {
		t.Fatalf("err = %v, want ErrQueueTimeout", err)
	}
}

func TestPriorityOrdering(t *testing.T) {
	m := NewManager()
	first, err := m.Acquire(context.Background(), "key", 1, 100, "")
	if err != nil {
		t.Fatal(err)
	}

	var mu sync.Mutex
	var order []string
	ready := make(chan struct{})
	run := func(priority int, name string) {
		close(ready)
		slot, err := m.Acquire(context.Background(), "key", 1, priority, "")
		if err != nil {
			t.Errorf("acquire %s: %v", name, err)
			return
		}
		mu.Lock()
		order = append(order, name)
		mu.Unlock()
		slot.Release()
	}

	go run(500, "low")
	<-ready
	ready = make(chan struct{})
	go run(10, "high")
	<-ready
	time.Sleep(20 * time.Millisecond)
	first.Release()
	time.Sleep(100 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	if len(order) != 2 || order[0] != "high" || order[1] != "low" {
		t.Fatalf("order = %#v", order)
	}
}

func TestSameKeyDifferentModelsHaveSeparateLimits(t *testing.T) {
	m := NewManager()
	a, err := m.Acquire(context.Background(), "key", 1, 100, "a")
	if err != nil {
		t.Fatal(err)
	}
	defer a.Release()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	b, err := m.Acquire(ctx, "key", 1, 100, "b")
	if err != nil {
		t.Fatal(err)
	}
	b.Release()
}

func TestRateLimitRPSRefillsBySecond(t *testing.T) {
	m := NewManager()
	now := time.Unix(1000, 0)
	m.now = func() time.Time { return now }

	limits := RateLimits{RPS: 1}
	if err := m.CheckRateLimit("key", "model", limits); err != nil {
		t.Fatal(err)
	}
	if err := m.CheckRateLimit("key", "model", limits); !errors.Is(err, ErrRateLimitExceeded) {
		t.Fatalf("err = %v, want ErrRateLimitExceeded", err)
	}
	now = now.Add(time.Second)
	if err := m.CheckRateLimit("key", "model", limits); err != nil {
		t.Fatalf("after refill: %v", err)
	}
	metrics := m.Metrics()
	if got := metrics.State["key::model"].RateLimited; got != 1 {
		t.Fatalf("rate limited count = %d", got)
	}
}

func TestRateLimitRPMRefillsOverMinute(t *testing.T) {
	m := NewManager()
	now := time.Unix(1000, 0)
	m.now = func() time.Time { return now }

	limits := RateLimits{RPM: 2}
	if err := m.CheckRateLimit("key", "model", limits); err != nil {
		t.Fatal(err)
	}
	if err := m.CheckRateLimit("key", "model", limits); err != nil {
		t.Fatal(err)
	}
	if err := m.CheckRateLimit("key", "model", limits); !errors.Is(err, ErrRateLimitExceeded) {
		t.Fatalf("err = %v, want ErrRateLimitExceeded", err)
	}
	now = now.Add(30 * time.Second)
	if err := m.CheckRateLimit("key", "model", limits); err != nil {
		t.Fatalf("after half-minute refill: %v", err)
	}
}

func TestRateLimitDoesNotResetConcurrencyState(t *testing.T) {
	m := NewManager()
	slot, err := m.Acquire(context.Background(), "key", 3, 25, "model")
	if err != nil {
		t.Fatal(err)
	}
	defer slot.Release()
	if err := m.CheckRateLimit("key", "model", RateLimits{RPS: 10}); err != nil {
		t.Fatal(err)
	}
	state := m.Metrics().State["key::model"]
	if state.ConcurrencyLimit != 3 || state.Priority != 25 || state.ActiveCount != 1 {
		t.Fatalf("state was reset by rate check: %#v", state)
	}
}

func TestRateLimitAvailableDoesNotConsumeBudget(t *testing.T) {
	m := NewManager()
	now := time.Unix(1000, 0)
	m.now = func() time.Time { return now }

	limits := RateLimits{RPS: 1, RPM: 2}
	if !m.RateLimitAvailable("key", "model", limits) {
		t.Fatal("new scope should be available")
	}
	if _, ok := m.Metrics().State["key::model"]; ok {
		t.Fatal("availability check created mutable state")
	}
	if err := m.CheckRateLimit("key", "model", limits); err != nil {
		t.Fatal(err)
	}
	if m.RateLimitAvailable("key", "model", limits) {
		t.Fatal("consumed RPS budget should not be available")
	}
	state := m.Metrics().State["key::model"]
	if state.RateLimited != 0 {
		t.Fatalf("availability check incremented rate limited count: %d", state.RateLimited)
	}
	if state.RPSTokens != 0 || state.RPMTokens != 1 {
		t.Fatalf("availability check mutated tokens: rps=%v rpm=%v", state.RPSTokens, state.RPMTokens)
	}
	now = now.Add(time.Second)
	if !m.RateLimitAvailable("key", "model", limits) {
		t.Fatal("refilled RPS budget should be available")
	}
	state = m.Metrics().State["key::model"]
	if state.RPSTokens != 0 || state.RPMTokens != 1 {
		t.Fatalf("refill peek mutated tokens: rps=%v rpm=%v", state.RPSTokens, state.RPMTokens)
	}
	if err := m.CheckRateLimit("key", "model", limits); err != nil {
		t.Fatalf("check after peek: %v", err)
	}
}
