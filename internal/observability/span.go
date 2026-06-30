package observability

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	oteltrace "go.opentelemetry.io/otel/trace"
)

const (
	PhaseRequest         = "request"
	PhasePolicy          = "policy"
	PhaseAdmission       = "admission"
	PhaseRouting         = "routing"
	PhaseUpstreamAttempt = "upstream_attempt"
	PhaseResponse        = "response"
	PhaseParser          = "parser"
	PhaseLogging         = "logging"

	OutcomeSuccess  = "success"
	OutcomeError    = "error"
	OutcomeBlocked  = "blocked"
	OutcomeRetry    = "retry"
	OutcomeFinal    = "final"
	OutcomeCacheHit = "cache_hit"
	OutcomeRouted   = "routed"

	genAISystemOpenAI = "openai"
)

type RequestAttrs struct {
	EndpointKind string
	Model        string
	KeyScope     string
	Stream       bool
}

type PhaseAttrs struct {
	EndpointKind string
	Model        string
	BackendName  string
	AttemptIndex int
	RetryIndex   int
	Stream       bool
}

type SpanResult struct {
	Outcome     string
	BackendName string
	StatusCode  int
	Error       error
	ErrorType   string
}

type Span struct {
	span          oteltrace.Span
	logsEnabled   bool
	phase         string
	endpointKind  string
	model         string
	keyScope      string
	backendName   string
	attemptIndex  int
	retryIndex    int
	hasRetryIndex bool
	stream        bool
}

func (s *Span) SetKeyScope(keyScope string) {
	if s.span == nil {
		return
	}
	s.keyScope = boundedValue(keyScope, "unknown")
	s.span.SetAttributes(attribute.String("key_scope", s.keyScope))
}

func (s Span) SpanContext() oteltrace.SpanContext {
	if s.span == nil {
		return oteltrace.SpanContext{}
	}
	return s.span.SpanContext()
}

func (s Span) TraceID() string {
	spanContext := s.SpanContext()
	if !spanContext.IsValid() {
		return ""
	}
	return spanContext.TraceID().String()
}

func (s Span) SpanID() string {
	spanContext := s.SpanContext()
	if !spanContext.IsValid() {
		return ""
	}
	return spanContext.SpanID().String()
}

func (p *Provider) StartRequestSpan(ctx context.Context, attrs RequestAttrs) (context.Context, Span) {
	if !p.Enabled() {
		return ctx, Span{}
	}
	endpointKind := boundedValue(attrs.EndpointKind, "unknown")
	model := boundedValue(attrs.Model, "unknown")
	spanAttrs := []attribute.KeyValue{
		attribute.String("phase", PhaseRequest),
		attribute.String("endpoint_kind", endpointKind),
		attribute.String("model", model),
		attribute.String("key_scope", boundedValue(attrs.KeyScope, "unknown")),
		attribute.Bool("stream", attrs.Stream),
	}
	spanAttrs = append(spanAttrs, genAIAttrs(endpointKind, model, false)...)
	ctx, span := p.tracer.Start(ctx, "proxy.request", oteltrace.WithSpanKind(oteltrace.SpanKindServer), oteltrace.WithAttributes(spanAttrs...))
	return ctx, Span{
		span:         span,
		logsEnabled:  p.LogsEnabled(),
		phase:        PhaseRequest,
		endpointKind: endpointKind,
		model:        model,
		keyScope:     boundedValue(attrs.KeyScope, "unknown"),
		stream:       attrs.Stream,
	}
}

func (p *Provider) StartPhaseSpan(ctx context.Context, phase string, attrs PhaseAttrs) (context.Context, Span) {
	if !p.Enabled() {
		return ctx, Span{}
	}
	phase = boundedValue(phase, "unknown")
	endpointKind := boundedValue(attrs.EndpointKind, "unknown")
	model := boundedValue(attrs.Model, "unknown")
	spanAttrs := []attribute.KeyValue{
		attribute.String("phase", phase),
		attribute.String("endpoint_kind", endpointKind),
		attribute.String("model", model),
		attribute.Bool("stream", attrs.Stream),
	}
	spanAttrs = append(spanAttrs, genAIAttrs(endpointKind, model, phase == PhaseResponse)...)
	if attrs.BackendName != "" {
		spanAttrs = append(spanAttrs, attribute.String("backend_name", boundedValue(attrs.BackendName, "unknown")))
	}
	if attrs.AttemptIndex > 0 {
		spanAttrs = append(spanAttrs, attribute.Int("attempt_index", attrs.AttemptIndex))
	}
	if attrs.RetryIndex >= 0 && phase == PhaseUpstreamAttempt {
		spanAttrs = append(spanAttrs, attribute.Int("retry_index", attrs.RetryIndex))
	}
	ctx, span := p.tracer.Start(ctx, "proxy."+phase, oteltrace.WithAttributes(spanAttrs...))
	return ctx, Span{
		span:          span,
		logsEnabled:   p.LogsEnabled(),
		phase:         phase,
		endpointKind:  endpointKind,
		model:         model,
		backendName:   boundedValue(attrs.BackendName, "unknown"),
		attemptIndex:  attrs.AttemptIndex,
		retryIndex:    attrs.RetryIndex,
		hasRetryIndex: phase == PhaseUpstreamAttempt && attrs.RetryIndex >= 0,
		stream:        attrs.Stream,
	}
}

func (s Span) End(result SpanResult) {
	if s.span == nil {
		return
	}
	s.recordEndLogEvent(result)
	attrs := make([]attribute.KeyValue, 0, 4)
	if result.Outcome != "" {
		attrs = append(attrs, attribute.String("outcome", boundedValue(result.Outcome, OutcomeError)))
	}
	if result.BackendName != "" {
		attrs = append(attrs, attribute.String("backend_name", boundedValue(result.BackendName, "unknown")))
	}
	if result.StatusCode > 0 {
		attrs = append(attrs, attribute.Int("status_code", result.StatusCode))
	}
	errorType := result.ErrorType
	if errorType == "" {
		errorType = ErrorType(result.Error, result.StatusCode)
	}
	if errorType != "" {
		attrs = append(attrs, attribute.String("error_type", errorType))
		s.span.SetStatus(codes.Error, errorType)
	} else if result.Outcome == OutcomeError || result.Outcome == OutcomeBlocked {
		s.span.SetStatus(codes.Error, "error")
	} else if result.Outcome != "" {
		s.span.SetStatus(codes.Ok, "")
	}
	if len(attrs) > 0 {
		s.span.SetAttributes(attrs...)
	}
	s.span.End()
}

func EndpointKind(path string) string {
	value := strings.ToLower(strings.TrimSpace(path))
	value = strings.TrimPrefix(value, "/")
	value = strings.TrimPrefix(value, "v1/")
	value = strings.TrimPrefix(value, "v2/")
	value = strings.Trim(value, "/")
	value = strings.ReplaceAll(value, "-", "_")
	switch value {
	case "chat", "chat_completions", "chat/completions", "completions":
		return "chat"
	case "embedding", "embeddings":
		return "embeddings"
	case "rerank", "rankings":
		return "rerank"
	case "message", "messages", "anthropic", "anthropic_messages":
		return "messages"
	case "response", "responses":
		return "responses"
	case "":
		return "unknown"
	default:
		if strings.HasPrefix(value, "p1/") {
			return "passthrough"
		}
		return "other"
	}
}

func KeyScope(authenticated bool, keyID string, groupID string) string {
	switch {
	case !authenticated:
		return "unauthenticated"
	case strings.TrimSpace(groupID) != "":
		return "group"
	case strings.TrimSpace(keyID) != "":
		return "authenticated"
	default:
		return "authenticated"
	}
}

func genAIAttrs(endpointKind string, model string, includeResponseModel bool) []attribute.KeyValue {
	model = boundedValue(model, "unknown")
	attrs := []attribute.KeyValue{
		attribute.String("gen_ai.system", genAISystemOpenAI),
		attribute.String("gen_ai.operation.name", genAIOperationName(endpointKind)),
		attribute.String("gen_ai.request.model", model),
	}
	if includeResponseModel && model != "" && model != "unknown" {
		attrs = append(attrs, attribute.String("gen_ai.response.model", model))
	}
	return attrs
}

func genAIOperationName(endpointKind string) string {
	switch boundedValue(endpointKind, "unknown") {
	case "chat":
		return "chat"
	case "messages":
		return "messages"
	case "responses":
		return "responses"
	case "embeddings":
		return "embeddings"
	case "rerank":
		return "rerank"
	case "passthrough":
		return "passthrough"
	default:
		return "other"
	}
}

func ErrorType(err error, statusCode int) string {
	if statusType := StatusErrorType(statusCode); statusType != "" {
		return statusType
	}
	if err != nil {
		return BucketErrorType(err)
	}
	return ""
}

func BucketErrorType(err error) string {
	if err == nil {
		return ""
	}
	if errors.Is(err, context.Canceled) {
		return "context_canceled"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "timeout"
	}
	message := strings.ToLower(err.Error())
	switch {
	case strings.Contains(message, "rate limit"):
		return "rate_limit"
	case strings.Contains(message, "quota"):
		return "quota"
	case strings.Contains(message, "queue"):
		return "queue"
	case strings.Contains(message, "timeout") || strings.Contains(message, "deadline"):
		return "timeout"
	case strings.Contains(message, "context canceled") || strings.Contains(message, "client closed"):
		return "context_canceled"
	case strings.Contains(message, "permission") || strings.Contains(message, "not authorized") || strings.Contains(message, "access denied"):
		return "permission"
	case strings.Contains(message, "not found") || strings.Contains(message, "not defined"):
		return "not_found"
	case strings.Contains(message, "unsupported endpoint"):
		return "unsupported_endpoint"
	case strings.Contains(message, "connection refused") || strings.Contains(message, "no such host"):
		return "network"
	default:
		return "error"
	}
}

func StatusErrorType(statusCode int) string {
	switch {
	case statusCode == 0:
		return ""
	case statusCode == 499:
		return "client_closed"
	case statusCode == 401 || statusCode == 403:
		return "permission"
	case statusCode == 408:
		return "timeout"
	case statusCode == 429:
		return "rate_limit"
	case statusCode >= 500:
		return "http_5xx"
	case statusCode >= 400:
		return "http_4xx"
	default:
		return ""
	}
}

func boundedValue(value string, fallback string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return fallback
	}
	if len(value) > 128 {
		return fmt.Sprintf("%s_truncated", value[:128])
	}
	return value
}
