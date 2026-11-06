package admission

import (
	"container/heap"
	"context"
	"errors"
	"runtime"
	"sync"
	"testing"
	"time"
)

func TestAcquireRejectsCancelledContextWithoutTakingSlot(t *testing.T) {
	for _, deadline := range []bool{false, true} {
		m := NewManager()
		ctx, cancel := context.WithCancel(context.Background())
		want := ErrClientDisconnected
		if deadline {
			ctx, cancel = context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
			want = ErrQueueTimeout
		}
		cancel()
		slot, err := m.Acquire(ctx, "key", 1, 100, "")
		if slot != nil || !errors.Is(err, want) {
			t.Fatalf("deadline=%v: slot=%v, err=%v", deadline, slot, err)
		}
		if got := m.Metrics().ActiveByScope["key"]; got != 0 {
			t.Fatalf("cancelled request took %d slots", got)
		}
	}
}

func TestAcquireCancelledAtPromotionReclaimsSlot(t *testing.T) {
	m := NewManager()
	ctx, cancel := context.WithCancel(context.Background())
	holder, err := m.Acquire(context.Background(), "key", 1, 100, "")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		slot, err := m.Acquire(ctx, "key", 1, 100, "")
		if slot != nil {
			slot.Release()
		}
		done <- err
	}()
	deadline := time.Now().Add(time.Second)
	for m.Metrics().GlobalQueueDepth != 1 && time.Now().Before(deadline) {
		runtime.Gosched()
	}
	m.mu.Lock()
	if len(m.waiting) != 1 {
		m.mu.Unlock()
		cancel()
		holder.Release()
		t.Fatal("waiter did not queue")
	}
	// Make both select arms ready while holding the scheduler lock. The
	// waiter cannot complete cancellation cleanup until this lock is released.
	q := heap.Pop(&m.waiting).(*queuedRequest)
	cancel()
	m.states["key"].active[q.seq] = struct{}{}
	close(q.ready)
	m.mu.Unlock()
	if err := <-done; !errors.Is(err, ErrClientDisconnected) {
		t.Fatalf("cancelled promoted request: %v", err)
	}
	holder.Release()
	if got := m.Metrics().ActiveByScope["key"]; got != 0 {
		t.Fatalf("promotion leaked %d slots", got)
	}
}

func TestCancelledBurstsPreserveAllAdmissionCapacity(t *testing.T) {
	m := NewManager()
	for round := 0; round < 10; round++ {
		var holders []*Slot
		for i := 0; i < 5; i++ {
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			slot, err := m.Acquire(ctx, "recovery", 5, 100, "model")
			cancel()
			if err != nil {
				t.Fatalf("round %d lost capacity: %v", round, err)
			}
			holders = append(holders, slot)
		}
		ctx, cancel := context.WithCancel(context.Background())
		var wg sync.WaitGroup
		for i := 0; i < 25; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				slot, err := m.Acquire(ctx, "recovery", 5, 100, "model")
				if slot != nil {
					slot.Release()
				}
				if !errors.Is(err, ErrClientDisconnected) {
					t.Errorf("cancelled waiter: %v", err)
				}
			}()
		}
		deadline := time.Now().Add(time.Second)
		for m.Metrics().GlobalQueueDepth != 25 && time.Now().Before(deadline) {
			runtime.Gosched()
		}
		queued := m.Metrics().GlobalQueueDepth
		cancel()
		wg.Wait()
		for _, slot := range holders {
			slot.Release()
		}
		if queued != 25 {
			t.Fatalf("waiters did not reach queue: %d", queued)
		}
		metrics := m.Metrics()
		if metrics.ActiveByScope["recovery::model"] != 0 || metrics.GlobalQueueDepth != 0 {
			t.Fatalf("round %d leaked admission state: %+v", round, metrics)
		}
	}
}
