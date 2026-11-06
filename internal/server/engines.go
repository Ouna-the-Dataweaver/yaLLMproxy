package server

import (
	"context"
	"time"
)

// StartEngineDiscovery follows runtime replacements and stops with the server.
// The router owns per-node refresh deadlines; this loop also picks up new nodes.
func (a *App) StartEngineDiscovery(ctx context.Context) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			if ctx.Err() != nil {
				return
			}
			view, release := a.acquireRuntime()
			if view.Router != nil {
				view.Router.DiscoverEngines(ctx)
			}
			release()
			timer := time.NewTimer(time.Second)
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
		}
	}()
	return done
}
