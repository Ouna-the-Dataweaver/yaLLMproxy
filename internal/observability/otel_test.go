package observability

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/theio/yallmp-proxy/internal/config"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

func TestConfigureDisabledReturnsNoopAndIgnoresEndpoint(t *testing.T) {
	provider, err := Configure(context.Background(), config.OTelConfig{
		Enabled:  false,
		Endpoint: "http://[invalid-endpoint",
		Headers:  map[string]any{"authorization": 12},
	})
	if err != nil {
		t.Fatal(err)
	}
	if provider.Enabled() {
		t.Fatal("disabled provider reported enabled")
	}
	ctx, span := provider.StartRequestSpan(context.Background(), RequestAttrs{EndpointKind: "chat", Model: "alpha"})
	if span.TraceID() != "" || span.SpanID() != "" {
		t.Fatalf("disabled span trace context = %q/%q, want empty", span.TraceID(), span.SpanID())
	}
	span.End(SpanResult{Outcome: OutcomeSuccess, StatusCode: 200})
	if ctx == nil {
		t.Fatal("nil context")
	}
	if err := provider.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestConfigureEnabledRequiresEndpoint(t *testing.T) {
	_, err := Configure(context.Background(), config.OTelConfig{Enabled: true})
	if err == nil || !strings.Contains(err.Error(), "endpoint") {
		t.Fatalf("err = %v, want endpoint error", err)
	}
}

func TestConfigureEnabledRejectsInvalidEndpoint(t *testing.T) {
	_, err := Configure(context.Background(), config.OTelConfig{Enabled: true, Endpoint: "collector-without-port"})
	if err == nil || !strings.Contains(err.Error(), "invalid OTel endpoint") {
		t.Fatalf("err = %v, want invalid endpoint error", err)
	}
}

func TestConfigureEnabledRejectsInvalidRuntimeConfig(t *testing.T) {
	for _, tc := range []struct {
		name string
		cfg  config.OTelConfig
		want string
	}{
		{name: "invalid enabled", cfg: config.OTelConfig{Enabled: []any{"true"}}, want: "enabled"},
		{name: "invalid logs enabled", cfg: config.OTelConfig{Logs: config.OTelLogsConfig{Enabled: 2}}, want: "logs enabled"},
		{name: "logs require global enabled", cfg: config.OTelConfig{Enabled: false, Logs: config.OTelLogsConfig{Enabled: true}}, want: "logs require OTel enabled"},
		{name: "invalid insecure", cfg: config.OTelConfig{Enabled: true, Endpoint: "collector.test:4317", Insecure: 2}, want: "insecure"},
		{name: "invalid header name", cfg: config.OTelConfig{Enabled: true, Endpoint: "collector.test:4317", Headers: map[string]any{" bad": "value"}}, want: "header name"},
		{name: "invalid header value", cfg: config.OTelConfig{Enabled: true, Endpoint: "collector.test:4317", Headers: map[string]any{"x-otel": 1}}, want: "header value"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Configure(context.Background(), tc.cfg)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestOTelPureConfigValidators(t *testing.T) {
	for _, tc := range []struct {
		input string
		want  string
	}{
		{input: "", want: ""},
		{input: " otlp ", want: exporterOTLPGRPC},
		{input: "OTLP_GRPC", want: exporterOTLPGRPC},
		{input: "otlp-grpc", want: exporterOTLPGRPC},
		{input: "grpc", want: exporterOTLPGRPC},
		{input: "zipkin", want: "zipkin"},
	} {
		t.Run("exporter "+tc.input, func(t *testing.T) {
			if got := normalizeExporter(tc.input); got != tc.want {
				t.Fatalf("normalizeExporter(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}

	for _, tc := range []struct {
		endpoint string
		want     bool
	}{
		{endpoint: "collector.test:4317", want: true},
		{endpoint: "collector.test:", want: false},
		{endpoint: ":4317", want: false},
		{endpoint: "http://collector.test", want: true},
		{endpoint: "https://collector.test:443/v1/traces", want: true},
		{endpoint: "ftp://collector.test", want: false},
		{endpoint: "collector-without-port", want: false},
	} {
		t.Run("endpoint "+tc.endpoint, func(t *testing.T) {
			if got := validEndpoint(tc.endpoint); got != tc.want {
				t.Fatalf("validEndpoint(%q) = %v, want %v", tc.endpoint, got, tc.want)
			}
		})
	}
}

func TestExporterHeadersValidationBoundaries(t *testing.T) {
	valid := map[string]any{"x-foo-bar": "one", "a!b": "two"}
	got, err := exporterHeaders(valid)
	if err != nil {
		t.Fatal(err)
	}
	if got["x-foo-bar"] != "one" || got["a!b"] != "two" {
		t.Fatalf("headers = %#v", got)
	}

	thirtyTwo := map[string]any{}
	for i := 0; i < 32; i++ {
		thirtyTwo[fmt.Sprintf("x-header-%02d", i)] = "value"
	}
	if _, err := exporterHeaders(thirtyTwo); err != nil {
		t.Fatalf("32 headers rejected: %v", err)
	}
	thirtyThree := map[string]any{}
	for i := 0; i < 33; i++ {
		thirtyThree[fmt.Sprintf("x-header-%02d", i)] = "value"
	}
	if _, err := exporterHeaders(thirtyThree); err == nil || !strings.Contains(err.Error(), "32") {
		t.Fatalf("33 headers err = %v, want limit error", err)
	}

	for _, tc := range []struct {
		name    string
		headers map[string]any
		want    string
	}{
		{name: "blank name", headers: map[string]any{"": "value"}, want: "header name"},
		{name: "trimmed name", headers: map[string]any{" x-otel": "value"}, want: "header name"},
		{name: "invalid character", headers: map[string]any{"x bad": "value"}, want: "header name"},
		{name: "quote", headers: map[string]any{"x\"bad": "value"}, want: "header name"},
		{name: "non string value", headers: map[string]any{"x-otel": 1}, want: "header value"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := exporterHeaders(tc.headers)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestConfigureRejectsUnsupportedExporterBeforeDial(t *testing.T) {
	_, err := Configure(context.Background(), config.OTelConfig{
		Enabled:  true,
		Endpoint: "collector.test:4317",
		Insecure: true,
		Exporter: "zipkin",
	})
	if err == nil || !strings.Contains(err.Error(), "unsupported OTel exporter") {
		t.Fatalf("err = %v, want unsupported exporter", err)
	}
}

func TestProviderConstructorsAndOptionBuildersAreDefensive(t *testing.T) {
	if provider := fromTracerProvider(nil, true); provider.Enabled() {
		t.Fatal("nil tracer provider should return disabled noop")
	}
	tracerProvider := sdktrace.NewTracerProvider()
	provider := fromTracerProvider(tracerProvider, true)
	if !provider.Enabled() {
		t.Fatal("sdk tracer provider should create enabled provider")
	}
	if err := provider.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}

	meterProvider := sdkmetric.NewMeterProvider()
	if provider := fromSDKProviders(nil, meterProvider, true, false); provider.Enabled() {
		t.Fatal("nil sdk provider input should return disabled noop")
	}
	if provider := fromProviders(nil, nil, true, false, nil); provider.Enabled() {
		t.Fatal("nil tracer should not report enabled")
	}

	_ = endpointOption("collector.test:4317")
	_ = endpointOption("http://collector.test:4317/v1/traces")
	if got := len(metricOptions("collector.test:4317", true, map[string]string{"x-otel": "value"})); got != 3 {
		t.Fatalf("metricOptions with insecure headers len = %d, want 3", got)
	}
	if got := len(metricOptions("https://collector.test/v1/metrics", false, nil)); got != 1 {
		t.Fatalf("metricOptions URL len = %d, want 1", got)
	}
	if serviceResource("") == nil || serviceResource("custom-service") == nil {
		t.Fatal("serviceResource returned nil")
	}
}

func TestLogEventsDisabledByDefault(t *testing.T) {
	provider, recorder := NewInMemoryProvider("test-service")
	if provider.LogsEnabled() {
		t.Fatal("in-memory provider enabled logs by default")
	}
	ctx, span := provider.StartRequestSpan(context.Background(), RequestAttrs{
		EndpointKind: "chat",
		Model:        "alpha",
		KeyScope:     "authenticated",
		Stream:       true,
	})
	provider.LogEvent(ctx, LogEventAttrs{
		Phase:        PhaseResponse,
		Outcome:      OutcomeSuccess,
		EndpointKind: "chat",
		Model:        "alpha",
		TotalTokens:  17,
	})
	span.End(SpanResult{Outcome: OutcomeSuccess, StatusCode: 200})

	for _, observedSpan := range recorder.Spans() {
		if events := observedSpan.Events(); len(events) != 0 {
			t.Fatalf("disabled logs emitted events on %s: %#v", observedSpan.Name(), events)
		}
	}
}

func TestLogEventsEnabledAreSanitizedAndBounded(t *testing.T) {
	provider, recorder := NewInMemoryProviderWithLogs("test-service", true)
	if !provider.LogsEnabled() {
		t.Fatal("logs provider did not report logs enabled")
	}
	ctx, requestSpan := provider.StartRequestSpan(context.Background(), RequestAttrs{
		EndpointKind: "chat",
		Model:        "unsafe model prompt-secret",
		KeyScope:     "authenticated",
		Stream:       true,
	})
	requestSpan.SetKeyScope("group")
	_, attemptSpan := provider.StartPhaseSpan(ctx, PhaseUpstreamAttempt, PhaseAttrs{
		EndpointKind: "chat",
		Model:        "unsafe model prompt-secret",
		BackendName:  "node-a",
		AttemptIndex: 2,
		RetryIndex:   1,
		Stream:       true,
	})
	err := errors.New("Authorization: Bearer client-secret provider-key prompt-secret response-body required-header-value raw-upstream-error")
	attemptSpan.End(SpanResult{Outcome: OutcomeError, StatusCode: 503, Error: err})
	requestSpan.End(SpanResult{Outcome: OutcomeError, StatusCode: 503, Error: err})

	attemptEvents := observedLogEvents(firstObservedSpanByName(recorder.Spans(), "proxy.upstream_attempt"))
	if len(attemptEvents) != 1 {
		t.Fatalf("attempt events = %#v", attemptEvents)
	}
	attrs := observedEventAttrs(attemptEvents[0])
	wants := map[string]string{
		"phase":         PhaseUpstreamAttempt,
		"outcome":       OutcomeError,
		"endpoint_kind": "chat",
		"model":         "unknown",
		"backend_name":  "node-a",
		"status_code":   "503",
		"error_type":    "http_5xx",
		"attempt_index": "2",
		"retry_index":   "1",
		"stream":        "true",
	}
	for key, want := range wants {
		if attrs[key] != want {
			t.Fatalf("event attr %s = %q, want %q; attrs=%#v", key, attrs[key], want, attrs)
		}
	}
	if len(attrs["trace_id"]) != 32 || len(attrs["span_id"]) != 16 {
		t.Fatalf("trace context attrs = %q/%q", attrs["trace_id"], attrs["span_id"])
	}

	requestEvents := observedLogEvents(firstObservedSpanByName(recorder.Spans(), "proxy.request"))
	if len(requestEvents) != 1 {
		t.Fatalf("request events = %#v", requestEvents)
	}
	if requestAttrs := observedEventAttrs(requestEvents[0]); requestAttrs["key_scope"] != "group" {
		t.Fatalf("request key_scope = %#v", requestAttrs)
	}
	for _, event := range append(attemptEvents, requestEvents...) {
		if event.Name != LogEventName {
			t.Fatalf("event name = %q, want %q", event.Name, LogEventName)
		}
		for _, secret := range []string{"Authorization", "client-secret", "provider-key", "prompt-secret", "response-body", "required-header-value", "raw-upstream-error"} {
			if strings.Contains(event.Name, secret) {
				t.Fatalf("event name leaked %q: %s", secret, event.Name)
			}
			for _, attr := range event.Attributes {
				if strings.Contains(string(attr.Key), secret) || strings.Contains(attr.Value.Emit(), secret) {
					t.Fatalf("event attr leaked %q: %s=%s", secret, attr.Key, attr.Value.Emit())
				}
			}
		}
	}
}

func TestManualLogEventBoundsTokenCounts(t *testing.T) {
	provider, recorder := NewInMemoryProviderWithLogs("test-service", true)
	ctx, span := provider.StartRequestSpan(context.Background(), RequestAttrs{
		EndpointKind: "responses",
		Model:        "gpt-4.1",
		KeyScope:     "authenticated",
	})
	provider.LogEvent(ctx, LogEventAttrs{
		Phase:           PhaseResponse,
		Outcome:         OutcomeSuccess,
		EndpointKind:    "responses",
		Model:           "gpt-4.1",
		BackendName:     "bad backend with spaces",
		StatusCode:      200,
		InputTokens:     3,
		OutputTokens:    4,
		TotalTokens:     maxTelemetryTokenCount + 42,
		CachedTokens:    -1,
		ReasoningTokens: 5,
	})
	span.End(SpanResult{Outcome: OutcomeSuccess, StatusCode: 200})

	events := observedLogEvents(firstObservedSpanByName(recorder.Spans(), "proxy.request"))
	if len(events) != 2 {
		t.Fatalf("events = %#v", events)
	}
	attrs := observedEventAttrs(events[0])
	if attrs["input_tokens"] != "3" || attrs["output_tokens"] != "4" || attrs["total_tokens"] != "1000000000" || attrs["reasoning_tokens"] != "5" {
		t.Fatalf("token attrs = %#v", attrs)
	}
	if _, ok := attrs["cached_tokens"]; ok {
		t.Fatalf("negative cached token count emitted: %#v", attrs)
	}
	if _, ok := attrs["backend_name"]; ok {
		t.Fatalf("invalid backend name emitted: %#v", attrs)
	}
}

func TestInMemoryProviderRecordsSafeSpans(t *testing.T) {
	provider, recorder := NewInMemoryProvider("test-service")
	ctx, requestSpan := provider.StartRequestSpan(context.Background(), RequestAttrs{
		EndpointKind: "chat",
		Model:        "alpha",
		KeyScope:     KeyScope(true, "raw-key-id", "raw-group-id"),
		Stream:       false,
	})
	requestTraceID := requestSpan.TraceID()
	requestSpanID := requestSpan.SpanID()
	if len(requestTraceID) != 32 || len(requestSpanID) != 16 {
		t.Fatalf("request span trace context = %q/%q", requestTraceID, requestSpanID)
	}
	_, admissionSpan := provider.StartPhaseSpan(ctx, PhaseAdmission, PhaseAttrs{
		EndpointKind: "chat",
		Model:        "alpha",
		Stream:       false,
	})
	_, responseSpan := provider.StartPhaseSpan(ctx, PhaseResponse, PhaseAttrs{
		EndpointKind: "chat",
		Model:        "alpha",
		Stream:       false,
	})
	err := errors.New("Authorization: Bearer client-secret raw-key-id raw-group-id api_key=provider-key url=https://backend-secret.invalid/v1 raw-failure")
	responseSpan.End(SpanResult{Outcome: OutcomeSuccess, BackendName: "alpha", StatusCode: 200})
	admissionSpan.End(SpanResult{Outcome: OutcomeError, StatusCode: 429, Error: err})
	requestSpan.End(SpanResult{Outcome: OutcomeError, StatusCode: 429, Error: err})

	spans := recorder.Spans()
	if len(spans) != 3 {
		t.Fatalf("spans = %d, want 3", len(spans))
	}
	requestAttrs := observedSpanAttrs(firstObservedSpanByName(spans, "proxy.request"))
	if requestAttrs["gen_ai.system"] != genAISystemOpenAI || requestAttrs["gen_ai.operation.name"] != "chat" || requestAttrs["gen_ai.request.model"] != "alpha" {
		t.Fatalf("request gen_ai attrs = %#v", requestAttrs)
	}
	observedRequest := firstObservedSpanByName(spans, "proxy.request")
	if observedRequest.SpanContext().TraceID().String() != requestTraceID || observedRequest.SpanContext().SpanID().String() != requestSpanID {
		t.Fatalf("recorded request span context = %s/%s, want %s/%s", observedRequest.SpanContext().TraceID(), observedRequest.SpanContext().SpanID(), requestTraceID, requestSpanID)
	}
	responseAttrs := observedSpanAttrs(firstObservedSpanByName(spans, "proxy.response"))
	if responseAttrs["gen_ai.system"] != genAISystemOpenAI || responseAttrs["gen_ai.operation.name"] != "chat" || responseAttrs["gen_ai.request.model"] != "alpha" || responseAttrs["gen_ai.response.model"] != "alpha" {
		t.Fatalf("response gen_ai attrs = %#v", responseAttrs)
	}
	admissionAttrs := observedSpanAttrs(firstObservedSpanByName(spans, "proxy.admission"))
	if _, ok := admissionAttrs["gen_ai.response.model"]; ok {
		t.Fatalf("admission span unexpectedly recorded response model: %#v", admissionAttrs)
	}
	for _, span := range spans {
		for _, secret := range []string{"client-secret", "provider-key", "raw-key-id", "raw-group-id", "Authorization", "https://backend-secret.invalid/v1", "raw-failure"} {
			if strings.Contains(span.Status().Description, secret) {
				t.Fatalf("status leaked %q in %q", secret, span.Status().Description)
			}
			for _, attr := range span.Attributes() {
				if strings.Contains(attr.Value.Emit(), secret) {
					t.Fatalf("span %s attr %s leaked %q: %s", span.Name(), attr.Key, secret, attr.Value.Emit())
				}
			}
		}
	}
	if err := provider.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if recorder.ShutdownCount() != 1 {
		t.Fatalf("shutdown count = %d", recorder.ShutdownCount())
	}
}

func TestBucketedHelpers(t *testing.T) {
	if got := KeyScope(true, "key-1", "group-1"); got != "group" {
		t.Fatalf("key scope = %q", got)
	}
	if got := KeyScope(true, "key-1", ""); got != "authenticated" {
		t.Fatalf("key scope = %q", got)
	}
	if got := KeyScope(false, "", ""); got != "unauthenticated" {
		t.Fatalf("key scope = %q", got)
	}
	if got := EndpointKind("/v1/chat/completions"); got != "chat" {
		t.Fatalf("endpoint kind = %q", got)
	}
	if got := BucketErrorType(context.DeadlineExceeded); got != "timeout" {
		t.Fatalf("error type = %q", got)
	}
	if got := StatusErrorType(503); got != "http_5xx" {
		t.Fatalf("status error type = %q", got)
	}
}

func TestGenAIOperationNameUsesLowCardinalityEndpointKinds(t *testing.T) {
	for _, tc := range []struct {
		endpointKind string
		want         string
	}{
		{endpointKind: "chat", want: "chat"},
		{endpointKind: "messages", want: "messages"},
		{endpointKind: "responses", want: "responses"},
		{endpointKind: "embeddings", want: "embeddings"},
		{endpointKind: "rerank", want: "rerank"},
		{endpointKind: "passthrough", want: "passthrough"},
		{endpointKind: "other", want: "other"},
		{endpointKind: "unknown", want: "other"},
		{endpointKind: "client-secret-path", want: "other"},
		{endpointKind: "", want: "other"},
	} {
		t.Run(tc.endpointKind, func(t *testing.T) {
			if got := genAIOperationName(tc.endpointKind); got != tc.want {
				t.Fatalf("operation = %q, want %q", got, tc.want)
			}
		})
	}
}

func firstObservedSpanByName(spans []sdktrace.ReadOnlySpan, name string) sdktrace.ReadOnlySpan {
	for _, span := range spans {
		if span.Name() == name {
			return span
		}
	}
	return nil
}

func observedSpanAttrs(span sdktrace.ReadOnlySpan) map[string]string {
	out := map[string]string{}
	if span == nil {
		return out
	}
	for _, attr := range span.Attributes() {
		out[string(attr.Key)] = attr.Value.Emit()
	}
	return out
}

func observedLogEvents(span sdktrace.ReadOnlySpan) []sdktrace.Event {
	if span == nil {
		return nil
	}
	events := make([]sdktrace.Event, 0)
	for _, event := range span.Events() {
		if event.Name == LogEventName {
			events = append(events, event)
		}
	}
	return events
}

func observedEventAttrs(event sdktrace.Event) map[string]string {
	out := map[string]string{}
	for _, attr := range event.Attributes {
		out[string(attr.Key)] = attr.Value.Emit()
	}
	return out
}
