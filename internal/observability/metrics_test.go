package observability

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

func TestNoopProviderDoesNotRecordMetrics(t *testing.T) {
	provider := Noop()
	provider.RecordRequest(context.Background(), RequestMetric{
		EndpointKind: "chat",
		Model:        "alpha",
		Outcome:      OutcomeSuccess,
		StatusCode:   200,
		Duration:     time.Millisecond,
	})
	provider.RecordTokenUsage(context.Background(), TokenUsageMetric{
		EndpointKind: "chat",
		Model:        "alpha",
		Usage:        map[string]any{"prompt_tokens": 1, "completion_tokens": 2, "total_tokens": 3},
	})
	if provider.Enabled() {
		t.Fatal("noop provider reported enabled")
	}
}

func TestInMemoryProviderRecordsRequestAndTokenMetrics(t *testing.T) {
	provider, recorder := NewInMemoryProvider("metrics-test")
	ctx := context.Background()
	provider.RecordRequest(ctx, RequestMetric{
		EndpointKind: "chat",
		Model:        "alpha",
		Stream:       false,
		Outcome:      OutcomeSuccess,
		StatusCode:   200,
		Duration:     42 * time.Millisecond,
	})
	provider.RecordTokenUsage(ctx, TokenUsageMetric{
		EndpointKind: "chat",
		Model:        "alpha",
		Usage: map[string]any{
			"prompt_tokens":             5,
			"completion_tokens":         2,
			"total_tokens":              7,
			"prompt_tokens_details":     map[string]any{"cached_tokens": 3},
			"completion_tokens_details": map[string]any{"reasoning_tokens": 1},
		},
	})
	metrics := collectMetrics(t, recorder)
	if got := sumValue(metrics, "yallmp.proxy.requests", map[string]string{"endpoint_kind": "chat", "model": "alpha", "outcome": "success", "error_type": "none", "stream": "false"}); got != 1 {
		t.Fatalf("request count = %d, want 1", got)
	}
	if got := histogramCount(metrics, "yallmp.proxy.request.duration", map[string]string{"endpoint_kind": "chat", "model": "alpha", "outcome": "success"}); got != 1 {
		t.Fatalf("duration count = %d, want 1", got)
	}
	for tokenType, want := range map[string]int64{"input": 5, "output": 2, "total": 7, "cached": 3, "reasoning": 1} {
		if got := sumValue(metrics, "gen_ai.client.token.usage", map[string]string{"endpoint_kind": "chat", "model": "alpha", "token.type": tokenType}); got != want {
			t.Fatalf("%s token metric = %d, want %d", tokenType, got, want)
		}
	}
}

func TestMetricLabelsAreSanitizedAndLowCardinality(t *testing.T) {
	provider, recorder := NewInMemoryProvider("metrics-test")
	secretErr := errors.New("Authorization: Bearer secret-token raw-key raw-group https://upstream.invalid/path")
	provider.RecordRequest(context.Background(), RequestMetric{
		EndpointKind: "/v1/chat/completions/raw-secret",
		Model:        "Bearer secret-token",
		Outcome:      "raw-secret-outcome",
		StatusCode:   418,
		Error:        secretErr,
		Duration:     time.Millisecond,
	})
	provider.RecordTokenUsage(context.Background(), TokenUsageMetric{
		EndpointKind: "/raw-secret",
		Model:        "Bearer secret-token",
		Usage:        map[string]any{"total_tokens": 9},
	})
	metrics := collectMetrics(t, recorder)
	encoded := metricSummary(metrics)
	for _, secret := range []string{"secret-token", "raw-key", "raw-group", "Authorization", "upstream.invalid", "raw-secret"} {
		if strings.Contains(encoded, secret) {
			t.Fatalf("metric data leaked %q: %s", secret, encoded)
		}
	}
	if got := sumValue(metrics, "yallmp.proxy.requests", map[string]string{"endpoint_kind": "other", "model": "unknown", "outcome": "error", "error_type": "http_4xx"}); got != 1 {
		t.Fatalf("sanitized request count = %d, want 1", got)
	}
	if got := sumValue(metrics, "gen_ai.client.token.usage", map[string]string{"endpoint_kind": "other", "model": "unknown", "token.type": "total"}); got != 9 {
		t.Fatalf("sanitized token total = %d, want 9", got)
	}
}

func TestStreamTokenUsageRecordedOnce(t *testing.T) {
	provider, recorder := NewInMemoryProvider("metrics-test")
	usage := map[string]any{"input_tokens": 3, "output_tokens": 4, "total_tokens": 7}
	provider.RecordTokenUsage(context.Background(), TokenUsageMetric{EndpointKind: "messages", Model: "alpha", Stream: true, Usage: usage})
	metrics := collectMetrics(t, recorder)
	if got := sumValue(metrics, "gen_ai.client.token.usage", map[string]string{"endpoint_kind": "messages", "model": "alpha", "stream": "true", "token.type": "total"}); got != 7 {
		t.Fatalf("stream total tokens = %d, want 7", got)
	}
}

func TestStreamTimingMetricsRecordedAsBoundedHistograms(t *testing.T) {
	provider, recorder := NewInMemoryProvider("metrics-test")
	provider.RecordStreamTTFT(context.Background(), StreamTimingMetric{
		EndpointKind: "chat",
		Model:        "alpha",
		TTFT:         25 * time.Millisecond,
	})
	provider.RecordStreamTPOT(context.Background(), StreamTimingMetric{
		EndpointKind: "chat",
		Model:        "alpha",
		TPOT:         10 * time.Millisecond,
	})
	metrics := collectMetrics(t, recorder)
	attrs := map[string]string{"endpoint_kind": "chat", "model": "alpha", "stream": "true"}
	if got := histogramCount(metrics, "gen_ai.client.operation.time_to_first_token", attrs); got != 1 {
		t.Fatalf("ttft count = %d, want 1", got)
	}
	if got := histogramCount(metrics, "gen_ai.client.operation.time_per_output_token", attrs); got != 1 {
		t.Fatalf("tpot count = %d, want 1", got)
	}
}

func TestRecordTokenUsageHandlesNumericShapesAndEmptyUsage(t *testing.T) {
	t.Run("empty usage records nothing", func(t *testing.T) {
		provider, recorder := NewInMemoryProvider("metrics-test")
		provider.RecordTokenUsage(context.Background(), TokenUsageMetric{EndpointKind: "chat", Model: "empty", Usage: nil})
		provider.RecordTokenUsage(context.Background(), TokenUsageMetric{EndpointKind: "chat", Model: "empty", Usage: map[string]any{}})
		if got := metricPointCount(collectMetrics(t, recorder), "gen_ai.client.token.usage"); got != 0 {
			t.Fatalf("token usage point count = %d, want 0", got)
		}
	})

	for _, tc := range []struct {
		name  string
		usage map[string]any
		want  map[string]int64
	}{
		{
			name: "mixed numeric inputs",
			usage: map[string]any{
				"input_tokens":              json.Number("5"),
				"completion_tokens":         float64(2.9),
				"total_tokens":              int64(10),
				"input_tokens_details":      map[string]any{"cached_tokens": "4"},
				"output_tokens_details":     map[string]any{"reasoning_tokens": json.Number("3")},
				"completion_tokens_details": map[string]any{"reasoning_tokens": 99},
			},
			want: map[string]int64{"input": 5, "output": 2, "total": 10, "cached": 4, "reasoning": 3},
		},
		{
			name:  "computed total",
			usage: map[string]any{"prompt_tokens": "6", "completion_tokens": json.Number("1.9")},
			want:  map[string]int64{"input": 6, "output": 1, "total": 7},
		},
		{
			name:  "explicit total smaller than parts",
			usage: map[string]any{"input_tokens": 7, "output_tokens": 8, "total_tokens": 3},
			want:  map[string]int64{"input": 7, "output": 8, "total": 3},
		},
		{
			name:  "malformed values",
			usage: map[string]any{"input_tokens": "not-a-number", "total_tokens": json.Number("bad")},
			want:  map[string]int64{},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			provider, recorder := NewInMemoryProvider("metrics-test")
			provider.RecordTokenUsage(context.Background(), TokenUsageMetric{
				EndpointKind: "responses",
				Model:        "model-" + strings.ReplaceAll(tc.name, " ", "-"),
				Stream:       true,
				Usage:        tc.usage,
			})
			metrics := collectMetrics(t, recorder)
			for tokenType, want := range tc.want {
				if got := sumValue(metrics, "gen_ai.client.token.usage", map[string]string{"token.type": tokenType}); got != want {
					t.Fatalf("%s tokens = %d, want %d", tokenType, got, want)
				}
			}
			if len(tc.want) == 0 {
				if got := metricPointCount(metrics, "gen_ai.client.token.usage"); got != 0 {
					t.Fatalf("token usage point count = %d, want 0", got)
				}
			}
		})
	}
}

func TestRecordRequestAndStreamTimingSkipNegativeDurations(t *testing.T) {
	provider, recorder := NewInMemoryProvider("metrics-test")
	provider.RecordRequest(context.Background(), RequestMetric{
		EndpointKind: "chat",
		Model:        "alpha",
		Outcome:      OutcomeSuccess,
		StatusCode:   200,
		Duration:     -time.Millisecond,
	})
	provider.RecordStreamTTFT(context.Background(), StreamTimingMetric{EndpointKind: "chat", Model: "alpha", TTFT: -time.Millisecond})
	provider.RecordStreamTPOT(context.Background(), StreamTimingMetric{EndpointKind: "chat", Model: "alpha", TPOT: -time.Millisecond})
	provider.RecordStreamTTFT(context.Background(), StreamTimingMetric{EndpointKind: "chat", Model: "alpha", TTFT: 0})
	provider.RecordStreamTPOT(context.Background(), StreamTimingMetric{EndpointKind: "chat", Model: "alpha", TPOT: 0})

	metrics := collectMetrics(t, recorder)
	requestAttrs := map[string]string{"endpoint_kind": "chat", "model": "alpha", "outcome": "success"}
	if got := sumValue(metrics, "yallmp.proxy.requests", requestAttrs); got != 1 {
		t.Fatalf("request count = %d, want 1", got)
	}
	if got := histogramCount(metrics, "yallmp.proxy.request.duration", requestAttrs); got != 0 {
		t.Fatalf("negative duration histogram count = %d, want 0", got)
	}
	streamAttrs := map[string]string{"endpoint_kind": "chat", "model": "alpha", "stream": "true"}
	if got := histogramCount(metrics, "gen_ai.client.operation.time_to_first_token", streamAttrs); got != 1 {
		t.Fatalf("ttft count = %d, want 1 zero-duration sample", got)
	}
	if got := histogramCount(metrics, "gen_ai.client.operation.time_per_output_token", streamAttrs); got != 1 {
		t.Fatalf("tpot count = %d, want 1 zero-duration sample", got)
	}

	noop := Noop()
	noop.RecordStreamTTFT(context.Background(), StreamTimingMetric{EndpointKind: "chat", Model: "alpha", TTFT: time.Millisecond})
	noop.RecordStreamTPOT(context.Background(), StreamTimingMetric{EndpointKind: "chat", Model: "alpha", TPOT: time.Millisecond})
}

func TestRecordRequestBoundsMetricAttributes(t *testing.T) {
	provider, recorder := NewInMemoryProvider("metrics-test")
	for _, item := range []RequestMetric{
		{EndpointKind: "passthrough", Model: "gpt-4o-2024-08-06", Outcome: OutcomeBlocked, ErrorType: "client_closed", Duration: time.Millisecond},
		{EndpointKind: "raw/secret", Model: "Bearer secret", Outcome: "raw-outcome", ErrorType: "raw-error", Duration: time.Millisecond},
		{EndpointKind: "unknown", Model: "clean-model", Outcome: OutcomeRetry, StatusCode: 503, Duration: time.Millisecond},
	} {
		provider.RecordRequest(context.Background(), item)
	}

	metrics := collectMetrics(t, recorder)
	if got := sumValue(metrics, "yallmp.proxy.requests", map[string]string{"endpoint_kind": "passthrough", "model": "gpt-4o-2024-08-06", "outcome": "blocked", "error_type": "client_closed"}); got != 1 {
		t.Fatalf("bounded passthrough request count = %d, want 1", got)
	}
	if got := sumValue(metrics, "yallmp.proxy.requests", map[string]string{"endpoint_kind": "other", "model": "unknown", "outcome": "error", "error_type": "error"}); got != 1 {
		t.Fatalf("sanitized raw request count = %d, want 1", got)
	}
	if got := sumValue(metrics, "yallmp.proxy.requests", map[string]string{"endpoint_kind": "unknown", "model": "clean-model", "outcome": "retry", "error_type": "http_5xx"}); got != 1 {
		t.Fatalf("status bucket request count = %d, want 1", got)
	}
}

func TestSafeModelLabelBoundaries(t *testing.T) {
	for _, tc := range []struct {
		model string
		want  string
	}{
		{model: "", want: "unknown"},
		{model: " Bearer secret ", want: "unknown"},
		{model: "-leading-dash", want: "unknown"},
		{model: strings.Repeat("a", 129), want: "unknown"},
		{model: "model with spaces", want: "unknown"},
		{model: "gpt-4o-2024-08-06", want: "gpt-4o-2024-08-06"},
		{model: "provider/model:v1", want: "provider/model:v1"},
	} {
		t.Run(tc.model, func(t *testing.T) {
			if got := safeModelLabel(tc.model); got != tc.want {
				t.Fatalf("safeModelLabel(%q) = %q, want %q", tc.model, got, tc.want)
			}
		})
	}
}

func collectMetrics(t *testing.T, recorder *Recorder) metricdata.ResourceMetrics {
	t.Helper()
	metrics, err := recorder.Metrics(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return metrics
}

func sumValue(metrics metricdata.ResourceMetrics, name string, attrs map[string]string) int64 {
	var total int64
	for _, scope := range metrics.ScopeMetrics {
		for _, metricItem := range scope.Metrics {
			if metricItem.Name != name {
				continue
			}
			sum, ok := metricItem.Data.(metricdata.Sum[int64])
			if !ok {
				continue
			}
			for _, point := range sum.DataPoints {
				if attrsMatch(point.Attributes, attrs) {
					total += point.Value
				}
			}
		}
	}
	return total
}

func metricPointCount(metrics metricdata.ResourceMetrics, name string) int {
	var total int
	for _, scope := range metrics.ScopeMetrics {
		for _, metricItem := range scope.Metrics {
			if metricItem.Name != name {
				continue
			}
			switch data := metricItem.Data.(type) {
			case metricdata.Sum[int64]:
				total += len(data.DataPoints)
			case metricdata.Histogram[float64]:
				total += len(data.DataPoints)
			}
		}
	}
	return total
}

func histogramCount(metrics metricdata.ResourceMetrics, name string, attrs map[string]string) uint64 {
	var total uint64
	for _, scope := range metrics.ScopeMetrics {
		for _, metricItem := range scope.Metrics {
			if metricItem.Name != name {
				continue
			}
			histogram, ok := metricItem.Data.(metricdata.Histogram[float64])
			if !ok {
				continue
			}
			for _, point := range histogram.DataPoints {
				if attrsMatch(point.Attributes, attrs) {
					total += point.Count
				}
			}
		}
	}
	return total
}

func attrsMatch(set attribute.Set, attrs map[string]string) bool {
	for key, want := range attrs {
		value, ok := set.Value(attribute.Key(key))
		if !ok || value.Emit() != want {
			return false
		}
	}
	return true
}

func metricSummary(metrics metricdata.ResourceMetrics) string {
	var builder strings.Builder
	for _, scope := range metrics.ScopeMetrics {
		for _, metricItem := range scope.Metrics {
			builder.WriteString(metricItem.Name)
			switch data := metricItem.Data.(type) {
			case metricdata.Sum[int64]:
				for _, point := range data.DataPoints {
					builder.WriteString(point.Attributes.Encoded(attribute.DefaultEncoder()))
				}
			case metricdata.Histogram[float64]:
				for _, point := range data.DataPoints {
					builder.WriteString(point.Attributes.Encoded(attribute.DefaultEncoder()))
				}
			}
		}
	}
	return builder.String()
}
