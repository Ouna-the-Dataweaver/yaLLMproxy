package observability

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
	"sync"

	"github.com/theio/yallmp-proxy/internal/config"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/metric/noop"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.41.0"
	oteltrace "go.opentelemetry.io/otel/trace"
)

const (
	DefaultServiceName = "yallmp-proxy"
	instrumentation    = "github.com/theio/yallmp-proxy/internal/observability"
	exporterOTLPGRPC   = "otlp_grpc"
)

type Provider struct {
	enabled         bool
	logsEnabled     bool
	tracer          oteltrace.Tracer
	meter           metric.Meter
	requests        metric.Int64Counter
	requestDuration metric.Float64Histogram
	tokenUsage      metric.Int64Counter
	streamTTFT      metric.Float64Histogram
	streamTPOT      metric.Float64Histogram
	shutdown        func(context.Context) error
	shutdownErr     error
	shutdownOnce    sync.Once
}

func Configure(ctx context.Context, cfg config.OTelConfig) (*Provider, error) {
	if !config.IsStrictBoolLike(cfg.Enabled) {
		return nil, fmt.Errorf("invalid OTel enabled value")
	}
	if !config.IsStrictBoolLike(cfg.Logs.Enabled) {
		return nil, fmt.Errorf("invalid OTel logs enabled value")
	}
	enabled := config.ParseBoolStrict(cfg.Enabled)
	logsEnabled := config.ParseBoolStrict(cfg.Logs.Enabled)
	if logsEnabled && !enabled {
		return nil, fmt.Errorf("OTel logs require OTel enabled")
	}
	if !enabled {
		return Noop(), nil
	}
	if !config.IsStrictBoolLike(cfg.Insecure) {
		return nil, fmt.Errorf("invalid OTel insecure value")
	}
	exporter := normalizeExporter(cfg.Exporter)
	if exporter == "" {
		exporter = exporterOTLPGRPC
	}
	if exporter != exporterOTLPGRPC {
		return nil, fmt.Errorf("unsupported OTel exporter %q", cfg.Exporter)
	}
	endpoint := strings.TrimSpace(cfg.Endpoint)
	if endpoint == "" {
		return nil, fmt.Errorf("OTel endpoint is required when tracing is enabled")
	}
	if !validEndpoint(endpoint) {
		return nil, fmt.Errorf("invalid OTel endpoint %q", cfg.Endpoint)
	}
	opts := []otlptracegrpc.Option{endpointOption(endpoint)}
	if config.ParseBoolStrict(cfg.Insecure) {
		opts = append(opts, otlptracegrpc.WithInsecure())
	}
	headers, err := exporterHeaders(cfg.Headers)
	if err != nil {
		return nil, err
	}
	if len(headers) > 0 {
		opts = append(opts, otlptracegrpc.WithHeaders(headers))
	}
	traceExporter, err := otlptracegrpc.New(ctx, opts...)
	if err != nil {
		return nil, err
	}
	metricExporter, err := otlpmetricgrpc.New(ctx, metricOptions(endpoint, config.ParseBoolStrict(cfg.Insecure), headers)...)
	if err != nil {
		_ = traceExporter.Shutdown(ctx)
		return nil, err
	}
	res := serviceResource(cfg.ServiceName)
	tracerProvider := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(traceExporter),
		sdktrace.WithResource(res),
	)
	meterProvider := sdkmetric.NewMeterProvider(
		sdkmetric.WithReader(sdkmetric.NewPeriodicReader(metricExporter)),
		sdkmetric.WithResource(res),
	)
	return fromSDKProviders(tracerProvider, meterProvider, true, logsEnabled), nil
}

func Noop() *Provider {
	meterProvider := noop.NewMeterProvider()
	return fromProviders(oteltrace.NewNoopTracerProvider().Tracer(instrumentation), meterProvider.Meter(instrumentation), false, false, nil)
}

func NewInMemoryProvider(serviceName string) (*Provider, *Recorder) {
	return NewInMemoryProviderWithLogs(serviceName, false)
}

func NewInMemoryProviderWithLogs(serviceName string, logsEnabled bool) (*Provider, *Recorder) {
	recorder := &Recorder{}
	tracerProvider := sdktrace.NewTracerProvider(
		sdktrace.WithSyncer(recorder),
		sdktrace.WithResource(serviceResource(serviceName)),
	)
	recorder.metricReader = sdkmetric.NewManualReader()
	meterProvider := sdkmetric.NewMeterProvider(
		sdkmetric.WithReader(recorder.metricReader),
		sdkmetric.WithResource(serviceResource(serviceName)),
	)
	return fromSDKProviders(tracerProvider, meterProvider, true, logsEnabled), recorder
}

func fromTracerProvider(tracerProvider *sdktrace.TracerProvider, enabled bool) *Provider {
	if tracerProvider == nil {
		return Noop()
	}
	return fromProviders(tracerProvider.Tracer(instrumentation), noop.NewMeterProvider().Meter(instrumentation), enabled, false, tracerProvider.Shutdown)
}

func fromSDKProviders(tracerProvider *sdktrace.TracerProvider, meterProvider *sdkmetric.MeterProvider, enabled bool, logsEnabled bool) *Provider {
	if tracerProvider == nil || meterProvider == nil {
		return Noop()
	}
	return fromProviders(
		tracerProvider.Tracer(instrumentation),
		meterProvider.Meter(instrumentation),
		enabled,
		logsEnabled,
		func(ctx context.Context) error {
			return errors.Join(tracerProvider.Shutdown(ctx), meterProvider.Shutdown(ctx))
		},
	)
}

func fromProviders(tracer oteltrace.Tracer, meter metric.Meter, enabled bool, logsEnabled bool, shutdown func(context.Context) error) *Provider {
	if meter == nil {
		meter = noop.NewMeterProvider().Meter(instrumentation)
	}
	requests, _ := meter.Int64Counter(
		"yallmp.proxy.requests",
		metric.WithDescription("Completed proxy requests"),
		metric.WithUnit("{request}"),
	)
	requestDuration, _ := meter.Float64Histogram(
		"yallmp.proxy.request.duration",
		metric.WithDescription("Completed proxy request duration"),
		metric.WithUnit("ms"),
	)
	tokenUsage, _ := meter.Int64Counter(
		"gen_ai.client.token.usage",
		metric.WithDescription("Reconciled GenAI token usage"),
		metric.WithUnit("{token}"),
	)
	streamTTFT, _ := meter.Float64Histogram(
		"gen_ai.client.operation.time_to_first_token",
		metric.WithDescription("Time from proxy request start to first client-visible streamed chunk"),
		metric.WithUnit("ms"),
	)
	streamTPOT, _ := meter.Float64Histogram(
		"gen_ai.client.operation.time_per_output_token",
		metric.WithDescription("Bounded average time between client-visible streamed chunks"),
		metric.WithUnit("ms"),
	)
	return &Provider{
		enabled:         enabled,
		logsEnabled:     logsEnabled,
		tracer:          tracer,
		meter:           meter,
		requests:        requests,
		requestDuration: requestDuration,
		tokenUsage:      tokenUsage,
		streamTTFT:      streamTTFT,
		streamTPOT:      streamTPOT,
		shutdown:        shutdown,
	}
}

func (p *Provider) Enabled() bool {
	return p != nil && p.enabled && p.tracer != nil
}

func (p *Provider) Shutdown(ctx context.Context) error {
	if p == nil || p.shutdown == nil {
		return nil
	}
	p.shutdownOnce.Do(func() {
		p.shutdownErr = p.shutdown(ctx)
	})
	return p.shutdownErr
}

func endpointOption(endpoint string) otlptracegrpc.Option {
	if strings.Contains(endpoint, "://") {
		return otlptracegrpc.WithEndpointURL(endpoint)
	}
	return otlptracegrpc.WithEndpoint(endpoint)
}

func metricOptions(endpoint string, insecure bool, headers map[string]string) []otlpmetricgrpc.Option {
	opts := []otlpmetricgrpc.Option{metricEndpointOption(endpoint)}
	if insecure {
		opts = append(opts, otlpmetricgrpc.WithInsecure())
	}
	if len(headers) > 0 {
		opts = append(opts, otlpmetricgrpc.WithHeaders(headers))
	}
	return opts
}

func metricEndpointOption(endpoint string) otlpmetricgrpc.Option {
	if strings.Contains(endpoint, "://") {
		return otlpmetricgrpc.WithEndpointURL(endpoint)
	}
	return otlpmetricgrpc.WithEndpoint(endpoint)
}

func validEndpoint(endpoint string) bool {
	endpoint = strings.TrimSpace(endpoint)
	if endpoint == "" {
		return false
	}
	if strings.Contains(endpoint, "://") {
		parsed, err := url.Parse(endpoint)
		if err != nil || parsed.Host == "" {
			return false
		}
		return parsed.Scheme == "http" || parsed.Scheme == "https"
	}
	host, port, err := net.SplitHostPort(endpoint)
	return err == nil && strings.TrimSpace(host) != "" && strings.TrimSpace(port) != ""
}

func exporterHeaders(headers map[string]any) (map[string]string, error) {
	if len(headers) == 0 {
		return nil, nil
	}
	if len(headers) > 32 {
		return nil, fmt.Errorf("OTel headers cannot contain more than 32 entries")
	}
	out := make(map[string]string, len(headers))
	for name, value := range headers {
		if strings.TrimSpace(name) == "" || strings.TrimSpace(name) != name || !validHeaderName(name) {
			return nil, fmt.Errorf("invalid OTel header name %q", name)
		}
		text, ok := value.(string)
		if !ok {
			return nil, fmt.Errorf("invalid OTel header value for %q", name)
		}
		out[name] = text
	}
	return out, nil
}

func normalizeExporter(value string) string {
	normalized := strings.ToLower(strings.TrimSpace(value))
	switch normalized {
	case "", "otlp", "otlp_grpc", "otlp-grpc", "grpc":
		if normalized == "" {
			return ""
		}
		return exporterOTLPGRPC
	default:
		return normalized
	}
}

func serviceResource(serviceName string) *resource.Resource {
	serviceName = strings.TrimSpace(serviceName)
	if serviceName == "" {
		serviceName = DefaultServiceName
	}
	res, err := resource.Merge(
		resource.Default(),
		resource.NewWithAttributes(semconv.SchemaURL, semconv.ServiceName(serviceName)),
	)
	if err != nil {
		return resource.NewWithAttributes(semconv.SchemaURL, semconv.ServiceName(serviceName))
	}
	return res
}

func validHeaderName(name string) bool {
	if name == "" {
		return false
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' {
			continue
		}
		switch c {
		case '!', '#', '$', '%', '&', '\'', '*', '+', '-', '.', '^', '_', '`', '|', '~':
			continue
		default:
			return false
		}
	}
	return true
}
