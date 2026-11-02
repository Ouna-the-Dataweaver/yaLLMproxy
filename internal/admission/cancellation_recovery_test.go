package admission

import (
	"context"
	"errors"
	"runtime"
	"sync"
	"testing"
	"time"
)

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
