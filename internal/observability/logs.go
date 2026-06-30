package observability

import (
	"context"
	"strings"

	"go.opentelemetry.io/otel/attribute"
	oteltrace "go.opentelemetry.io/otel/trace"
)

const (
	LogEventName           = "proxy.log"
	maxTelemetryTokenCount = 1_000_000_000
)

type LogEventAttrs struct {
	Phase           string
	Outcome         string
	EndpointKind    string
	Model           string
	BackendName     string
	StatusCode      int
	ErrorType       string
	KeyScope        string
	AttemptIndex    int
	RetryIndex      int
	HasRetryIndex   bool
	Stream          bool
	InputTokens     int
	OutputTokens    int
	TotalTokens     int
	CachedTokens    int
	ReasoningTokens int
}

func (p *Provider) LogsEnabled() bool {
	return p != nil && p.Enabled() && p.logsEnabled
}

func (p *Provider) LogEvent(ctx context.Context, event LogEventAttrs) {
	if !p.LogsEnabled() || ctx == nil {
		return
	}
	span := oteltrace.SpanFromContext(ctx)
	addLogEvent(span, event)
}

func (s Span) recordEndLogEvent(result SpanResult) {
	if !s.logsEnabled || s.span == nil {
		return
	}
	errorType := result.ErrorType
	if errorType == "" {
		errorType = ErrorType(result.Error, result.StatusCode)
	}
	backendName := result.BackendName
	if backendName == "" {
		backendName = s.backendName
	}
	addLogEvent(s.span, LogEventAttrs{
		Phase:         s.phase,
		Outcome:       result.Outcome,
		EndpointKind:  s.endpointKind,
		Model:         s.model,
		BackendName:   backendName,
		StatusCode:    result.StatusCode,
		ErrorType:     errorType,
		KeyScope:      s.keyScope,
		AttemptIndex:  s.attemptIndex,
		RetryIndex:    s.retryIndex,
		HasRetryIndex: s.hasRetryIndex,
		Stream:        s.stream,
	})
}

func addLogEvent(span oteltrace.Span, event LogEventAttrs) {
	if span == nil {
		return
	}
	spanContext := span.SpanContext()
	if !spanContext.IsValid() {
		return
	}
	attrs := []attribute.KeyValue{
		attribute.String("phase", boundedPhase(event.Phase)),
		attribute.String("outcome", boundedOutcome(event.Outcome)),
		attribute.String("endpoint_kind", boundedEndpointKind(event.EndpointKind)),
		attribute.String("model", safeModelLabel(event.Model)),
		attribute.Bool("stream", event.Stream),
		attribute.String("trace_id", spanContext.TraceID().String()),
		attribute.String("span_id", spanContext.SpanID().String()),
	}
	if backendName := safeTelemetryLabel(event.BackendName, ""); backendName != "" && backendName != "unknown" {
		attrs = append(attrs, attribute.String("backend_name", backendName))
	}
	if event.StatusCode > 0 && event.StatusCode <= 999 {
		attrs = append(attrs, attribute.Int("status_code", event.StatusCode))
	}
	if strings.TrimSpace(event.ErrorType) != "" {
		errorType := boundedErrorType(event.ErrorType)
		attrs = append(attrs, attribute.String("error_type", errorType))
	}
	if keyScope := boundedKeyScope(event.KeyScope); keyScope != "" {
		attrs = append(attrs, attribute.String("key_scope", keyScope))
	}
	if event.AttemptIndex > 0 {
		attrs = append(attrs, attribute.Int("attempt_index", event.AttemptIndex))
	}
	if event.HasRetryIndex && event.RetryIndex >= 0 {
		attrs = append(attrs, attribute.Int("retry_index", event.RetryIndex))
	}
	for _, item := range []struct {
		name  string
		value int
	}{
		{name: "input_tokens", value: event.InputTokens},
		{name: "output_tokens", value: event.OutputTokens},
		{name: "total_tokens", value: event.TotalTokens},
		{name: "cached_tokens", value: event.CachedTokens},
		{name: "reasoning_tokens", value: event.ReasoningTokens},
	} {
		if value := boundedTokenCount(item.value); value > 0 {
			attrs = append(attrs, attribute.Int(item.name, value))
		}
	}
	span.AddEvent(LogEventName, oteltrace.WithAttributes(attrs...))
}

func boundedPhase(phase string) string {
	switch boundedValue(phase, "unknown") {
	case PhaseRequest, PhasePolicy, PhaseAdmission, PhaseRouting, PhaseUpstreamAttempt, PhaseResponse, PhaseParser, PhaseLogging:
		return boundedValue(phase, "unknown")
	default:
		return "unknown"
	}
}

func boundedKeyScope(keyScope string) string {
	switch strings.ToLower(strings.TrimSpace(keyScope)) {
	case "unauthenticated", "authenticated", "group":
		return strings.ToLower(strings.TrimSpace(keyScope))
	case "", "unknown":
		return ""
	default:
		return "unknown"
	}
}

func safeTelemetryLabel(value string, fallback string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return fallback
	}
	if !safeMetricModelPattern.MatchString(value) {
		return fallback
	}
	return boundedValue(value, fallback)
}

func boundedTokenCount(value int) int {
	if value <= 0 {
		return 0
	}
	if value > maxTelemetryTokenCount {
		return maxTelemetryTokenCount
	}
	return value
}
