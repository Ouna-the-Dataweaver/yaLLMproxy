package server

import "sync"

// runtimeGeneration counts the requests that captured one set of runtime
// resources. A config swap retires the previous generation; its LogDB,
// inference cache, and telemetry provider close once its last request ends.
type runtimeGeneration struct {
	mu      sync.Mutex
	active  int
	cleanup func()
}

func (g *runtimeGeneration) acquire() {
	g.mu.Lock()
	g.active++
	g.mu.Unlock()
}

func (g *runtimeGeneration) release() {
	g.mu.Lock()
	g.active--
	var cleanup func()
	if g.active == 0 && g.cleanup != nil {
		cleanup, g.cleanup = g.cleanup, nil
	}
	g.mu.Unlock()
	if cleanup != nil {
		cleanup()
	}
}

// retire runs cleanup immediately when the generation is idle, otherwise after
// its last in-flight request releases it.
func (g *runtimeGeneration) retire(cleanup func()) {
	g.mu.Lock()
	if g.active > 0 {
		g.cleanup = cleanup
		g.mu.Unlock()
		return
	}
	g.mu.Unlock()
	cleanup()
}

func (a *App) currentGeneration() *runtimeGeneration {
	if g := a.generation.Load(); g != nil {
		return g
	}
	a.generation.CompareAndSwap(nil, &runtimeGeneration{})
	return a.generation.Load()
}

// acquireRuntime returns a view of the current runtime whose fields stay fixed
// for the caller's lifetime, plus a release func. runtimeMu is held only while
// the view is captured, so long-lived streams never block a config swap and a
// pending swap never stalls new requests behind them.
func (a *App) acquireRuntime() (*App, func()) {
	a.runtimeMu.RLock()
	defer a.runtimeMu.RUnlock()
	generation := a.currentGeneration()
	generation.acquire()
	view := &App{
		Config:            a.Config,
		Router:            a.Router,
		Policy:            a.Policy,
		Admission:         a.Admission,
		Logs:              a.Logs,
		LogDB:             a.LogDB,
		Payloads:          a.Payloads,
		Quota:             a.Quota,
		Responses:         a.Responses,
		Observability:     a.Observability,
		PrometheusTimings: a.PrometheusTimings,
		root:              a,
	}
	return view, generation.release
}

// runtimeRoot returns the long-lived App that owns state shared across
// runtime views: the catalog, process-wide counters, and the generation.
func (a *App) runtimeRoot() *App {
	if a.root != nil {
		return a.root
	}
	return a
}
