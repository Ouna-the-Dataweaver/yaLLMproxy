package observability

import (
	"context"
	"encoding/json"
	"regexp"
	"strconv"
	"strings"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

const (
	tokenTypeInput     = "input"
	tokenTypeOutput    = "output"
	tokenTypeTotal     = "total"
	tokenTypeCached    = "cached"
	tokenTypeReasoning = "reasoning"
)

var safeMetricModelPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/-]{0,127}$`)

type RequestMetric struct {
	EndpointKind string
	Model        string
	Stream       bool
	Outcome      string
	StatusCode   int
	Error        error
	ErrorType    string
	Duration     time.Duration
}

type TokenUsageMetric struct {
	EndpointKind string
	Model        string
	Stream       bool
	Usage        map[string]any
}

type StreamTimingMetric struct {
	EndpointKind string
	Model        string
	TTFT         time.Duration
	TPOT         time.Duration
}

func (p *Provider) RecordRequest(ctx context.Context, metricData RequestMetric) {
	if !p.Enabled() || p.requests == nil || p.requestDuration == nil {
		return
	}
	attrs := requestMetricAttrs(metricData)
	p.requests.Add(ctx, 1, metric.WithAttributes(attrs...))
	if metricData.Duration >= 0 {
		p.requestDuration.Record(ctx, float64(metricData.Duration)/float64(time.Millisecond), metric.WithAttributes(attrs...))
	}
}

func (p *Provider) RecordTokenUsage(ctx context.Context, metricData TokenUsageMetric) {
	if !p.Enabled() || p.tokenUsage == nil || len(metricData.Usage) == 0 {
		return
	}
	baseAttrs := []attribute.KeyValue{
		attribute.String("endpoint_kind", boundedEndpointKind(metricData.EndpointKind)),
		attribute.String("model", safeModelLabel(metricData.Model)),
		attribute.Bool("stream", metricData.Stream),
		attribute.String("gen_ai.system", genAISystemOpenAI),
		attribute.String("gen_ai.operation.name", genAIOperationName(metricData.EndpointKind)),
		attribute.String("gen_ai.request.model", safeModelLabel(metricData.Model)),
	}
	inputTokens := firstPositiveInt(metricData.Usage, "input_tokens", "prompt_tokens")
	outputTokens := firstPositiveInt(metricData.Usage, "output_tokens", "completion_tokens")
	totalTokens := intFromAny(metricData.Usage["total_tokens"])
	if totalTokens <= 0 {
		totalTokens = inputTokens + outputTokens
	}
	for _, item := range []struct {
		tokenType string
		value     int
	}{
		{tokenType: tokenTypeInput, value: inputTokens},
		{tokenType: tokenTypeOutput, value: outputTokens},
		{tokenType: tokenTypeTotal, value: totalTokens},
		{tokenType: tokenTypeCached, value: nestedPositiveInt(metricData.Usage, "input_tokens_details", "cached_tokens", "prompt_tokens_details")},
		{tokenType: tokenTypeReasoning, value: nestedPositiveInt(metricData.Usage, "output_tokens_details", "reasoning_tokens", "completion_tokens_details")},
	} {
		if item.value <= 0 {
			continue
		}
		attrs := append([]attribute.KeyValue{}, baseAttrs...)
		attrs = append(attrs, attribute.String("token.type", item.tokenType))
		p.tokenUsage.Add(ctx, int64(item.value), metric.WithAttributes(attrs...))
	}
}

func (p *Provider) RecordStreamTTFT(ctx context.Context, metricData StreamTimingMetric) {
	if !p.Enabled() || p.streamTTFT == nil || metricData.TTFT < 0 {
		return
	}
	p.streamTTFT.Record(ctx, float64(metricData.TTFT)/float64(time.Millisecond), metric.WithAttributes(streamTimingAttrs(metricData)...))
}

func (p *Provider) RecordStreamTPOT(ctx context.Context, metricData StreamTimingMetric) {
	if !p.Enabled() || p.streamTPOT == nil || metricData.TPOT < 0 {
		return
	}
	p.streamTPOT.Record(ctx, float64(metricData.TPOT)/float64(time.Millisecond), metric.WithAttributes(streamTimingAttrs(metricData)...))
}

func requestMetricAttrs(metricData RequestMetric) []attribute.KeyValue {
	errorType := metricData.ErrorType
	if errorType == "" {
		errorType = ErrorType(metricData.Error, metricData.StatusCode)
	}
	if errorType == "" {
		errorType = "none"
	}
	outcome := boundedOutcome(metricData.Outcome)
	return []attribute.KeyValue{
		attribute.String("endpoint_kind", boundedEndpointKind(metricData.EndpointKind)),
		attribute.String("model", safeModelLabel(metricData.Model)),
		attribute.String("outcome", outcome),
		attribute.String("error_type", boundedErrorType(errorType)),
		attribute.Bool("stream", metricData.Stream),
		attribute.String("gen_ai.system", genAISystemOpenAI),
		attribute.String("gen_ai.operation.name", genAIOperationName(metricData.EndpointKind)),
		attribute.String("gen_ai.request.model", safeModelLabel(metricData.Model)),
	}
}

func streamTimingAttrs(metricData StreamTimingMetric) []attribute.KeyValue {
	endpointKind := boundedEndpointKind(metricData.EndpointKind)
	model := safeModelLabel(metricData.Model)
	return []attribute.KeyValue{
		attribute.String("endpoint_kind", endpointKind),
		attribute.String("model", model),
		attribute.Bool("stream", true),
		attribute.String("gen_ai.system", genAISystemOpenAI),
		attribute.String("gen_ai.operation.name", genAIOperationName(endpointKind)),
		attribute.String("gen_ai.request.model", model),
	}
}

func boundedOutcome(outcome string) string {
	switch boundedValue(outcome, OutcomeError) {
	case OutcomeSuccess, OutcomeError, OutcomeBlocked, OutcomeRetry, OutcomeFinal, OutcomeCacheHit, OutcomeRouted:
		return boundedValue(outcome, OutcomeError)
	default:
		return OutcomeError
	}
}

func boundedEndpointKind(endpointKind string) string {
	switch boundedValue(endpointKind, "unknown") {
	case "chat", "messages", "responses", "embeddings", "rerank", "passthrough", "other", "unknown":
		return boundedValue(endpointKind, "unknown")
	default:
		return "other"
	}
}

func boundedErrorType(errorType string) string {
	errorType = boundedValue(errorType, "error")
	switch errorType {
	case "none", "context_canceled", "timeout", "rate_limit", "quota", "queue", "permission", "not_found", "unsupported_endpoint", "network", "client_closed", "http_4xx", "http_5xx", "error":
		return errorType
	default:
		return "error"
	}
}

func safeModelLabel(model string) string {
	model = strings.TrimSpace(model)
	if model == "" {
		return "unknown"
	}
	if !safeMetricModelPattern.MatchString(model) {
		return "unknown"
	}
	return boundedValue(model, "unknown")
}

func firstPositiveInt(values map[string]any, keys ...string) int {
	for _, key := range keys {
		if value := intFromAny(values[key]); value > 0 {
			return value
		}
	}
	return 0
}

func nestedPositiveInt(values map[string]any, primaryKey string, nestedKey string, fallbackKey string) int {
	for _, parentKey := range []string{primaryKey, fallbackKey} {
		if nested, _ := values[parentKey].(map[string]any); len(nested) > 0 {
			if value := intFromAny(nested[nestedKey]); value > 0 {
				return value
			}
		}
	}
	return 0
}

func intFromAny(value any) int {
	switch typed := value.(type) {
	case int:
		return typed
	case int64:
		return int(typed)
	case float64:
		return int(typed)
	case json.Number:
		intValue, err := strconv.Atoi(typed.String())
		if err == nil {
			return intValue
		}
		floatValue, _ := strconv.ParseFloat(typed.String(), 64)
		return int(floatValue)
	case string:
		intValue, _ := strconv.Atoi(strings.TrimSpace(typed))
		return intValue
	default:
		return 0
	}
}
