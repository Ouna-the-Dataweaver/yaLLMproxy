package observability

import (
	"context"
	"sync"

	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

type Recorder struct {
	mu           sync.Mutex
	spans        []sdktrace.ReadOnlySpan
	shutdowns    int
	metricReader *sdkmetric.ManualReader
}

func (r *Recorder) ExportSpans(_ context.Context, spans []sdktrace.ReadOnlySpan) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.spans = append(r.spans, spans...)
	return nil
}

func (r *Recorder) Shutdown(_ context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.shutdowns++
	return nil
}

func (r *Recorder) Spans() []sdktrace.ReadOnlySpan {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]sdktrace.ReadOnlySpan, len(r.spans))
	copy(out, r.spans)
	return out
}

func (r *Recorder) ShutdownCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.shutdowns
}

func (r *Recorder) Metrics(ctx context.Context) (metricdata.ResourceMetrics, error) {
	var out metricdata.ResourceMetrics
	if r == nil || r.metricReader == nil {
		return out, nil
	}
	err := r.metricReader.Collect(ctx, &out)
	return out, err
}
