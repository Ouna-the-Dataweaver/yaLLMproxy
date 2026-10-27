package server

import (
	"fmt"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/theio/yallmp-proxy/internal/admission"
	"github.com/theio/yallmp-proxy/internal/config"
	"github.com/theio/yallmp-proxy/internal/observability"
	"github.com/theio/yallmp-proxy/internal/proxy"
	"github.com/theio/yallmp-proxy/internal/quota"
)

const prometheusContentType = "text/plain; version=0.0.4; charset=utf-8"

type promSample struct {
	Labels map[string]string
	Value  float64
}

type promHistogramSample struct {
	Labels  map[string]string
	Buckets []uint64
	Count   uint64
	Sum     float64
}

type promTimingHistogram struct {
	name    string
	help    string
	buckets []float64
	series  map[string]*promTimingSeries
}

type promTimingSeries struct {
	labels  map[string]string
	buckets []uint64
	count   uint64
	sum     float64
}

type prometheusTimingRecorder struct {
	mu             sync.Mutex
	request        promTimingHistogram
	backendRequest promTimingHistogram
	streamTTFT     promTimingHistogram
	streamTPOT     promTimingHistogram
}

type prometheusRequestTiming struct {
	EndpointKind string
	Model        string
	Stream       bool
	Outcome      string
	StatusCode   int
	Error        error
	Duration     time.Duration
}

type prometheusBackendTiming struct {
	EndpointKind string
	Model        string
	Backend      string
	Stream       bool
	Outcome      string
	StatusCode   int
	ErrorType    string
	Duration     time.Duration
}

type prometheusStreamTiming struct {
	EndpointKind string
	Model        string
	Duration     time.Duration
}

var prometheusTimingBuckets = []float64{
	0.005,
	0.01,
	0.025,
	0.05,
	0.1,
	0.25,
	0.5,
	1,
	2.5,
	5,
	10,
	30,
	60,
	120,
	300,
	600,
}

func (a *App) metricsEnabled() bool {
	if a == nil || a.Config == nil {
		return false
	}
	return config.ParseBoolStrict(a.Config.ProxySettings.Metrics.Enabled)
}

func newPrometheusTimingRecorder(cfg config.MetricsTimingsConfig) *prometheusTimingRecorder {
	if !config.ParseBoolStrict(cfg.Enabled) {
		return nil
	}
	return &prometheusTimingRecorder{
		request: newPromTimingHistogram(
			"yallmp_request_duration_seconds",
			"End-to-end proxy request duration observed by the Prometheus metrics endpoint.",
			prometheusTimingBuckets,
		),
		backendRequest: newPromTimingHistogram(
			"yallmp_backend_request_duration_seconds",
			"Backend upstream attempt duration observed by the Prometheus metrics endpoint.",
			prometheusTimingBuckets,
		),
		streamTTFT: newPromTimingHistogram(
			"yallmp_stream_ttft_seconds",
			"Time from proxy request start to first successful client-visible streamed write.",
			prometheusTimingBuckets,
		),
		streamTPOT: newPromTimingHistogram(
			"yallmp_stream_tpot_seconds",
			"Aggregate average time between successful client-visible streamed writes.",
			prometheusTimingBuckets,
		),
	}
}

func newAppPrometheusTimingRecorder(cfg config.MetricsConfig) *prometheusTimingRecorder {
	if !config.ParseBoolStrict(cfg.Enabled) {
		return nil
	}
	return newPrometheusTimingRecorder(cfg.Timings)
}

func newPromTimingHistogram(name string, help string, buckets []float64) promTimingHistogram {
	copiedBuckets := append([]float64(nil), buckets...)
	return promTimingHistogram{
		name:    name,
		help:    help,
		buckets: copiedBuckets,
		series:  map[string]*promTimingSeries{},
	}
}

func (a *App) prometheusTimingsEnabled() bool {
	return a != nil && a.PrometheusTimings != nil
}

func (a *App) requestMetricsEnabled() bool {
	return a.otelEnabled() || a.prometheusTimingsEnabled()
}

func (r *prometheusTimingRecorder) RecordRequest(timing prometheusRequestTiming) {
	if r == nil || timing.Duration < 0 {
		return
	}
	errorType := observability.ErrorType(timing.Error, timing.StatusCode)
	if errorType == "" {
		errorType = "none"
	}
	labels := map[string]string{
		"endpoint_kind": promEndpointKind(timing.EndpointKind),
		"error_type":    promErrorType(errorType),
		"model":         promSafeLabel(timing.Model),
		"outcome":       promOutcome(timing.Outcome),
		"status_class":  promStatusClass(timing.StatusCode),
		"stream":        strconv.FormatBool(timing.Stream),
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.request.observe(labels, timing.Duration.Seconds())
}

func (r *prometheusTimingRecorder) RecordBackendRequest(timing prometheusBackendTiming) {
	if r == nil || timing.Duration < 0 {
		return
	}
	errorType := timing.ErrorType
	if errorType == "" {
		errorType = observability.StatusErrorType(timing.StatusCode)
	}
	if errorType == "" {
		errorType = "none"
	}
	labels := map[string]string{
		"backend":       promBackendLabel(timing.Backend),
		"endpoint_kind": promEndpointKind(timing.EndpointKind),
		"error_type":    promErrorType(errorType),
		"model":         promSafeLabel(timing.Model),
		"outcome":       promOutcome(timing.Outcome),
		"status_class":  promStatusClass(timing.StatusCode),
		"stream":        strconv.FormatBool(timing.Stream),
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.backendRequest.observe(labels, timing.Duration.Seconds())
}

func (r *prometheusTimingRecorder) RecordStreamTTFT(timing prometheusStreamTiming) {
	if r == nil || timing.Duration < 0 {
		return
	}
	labels := map[string]string{
		"endpoint_kind": promEndpointKind(timing.EndpointKind),
		"model":         promSafeLabel(timing.Model),
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.streamTTFT.observe(labels, timing.Duration.Seconds())
}

func (r *prometheusTimingRecorder) RecordStreamTPOT(timing prometheusStreamTiming) {
	if r == nil || timing.Duration < 0 {
		return
	}
	labels := map[string]string{
		"endpoint_kind": promEndpointKind(timing.EndpointKind),
		"model":         promSafeLabel(timing.Model),
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.streamTPOT.observe(labels, timing.Duration.Seconds())
}

func (r *prometheusTimingRecorder) snapshot() []promTimingHistogram {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return []promTimingHistogram{
		r.request.clone(),
		r.backendRequest.clone(),
		r.streamTTFT.clone(),
		r.streamTPOT.clone(),
	}
}

func (h *promTimingHistogram) observe(labels map[string]string, value float64) {
	if h == nil || math.IsNaN(value) || math.IsInf(value, 0) || value < 0 {
		return
	}
	key := labelSet(labels)
	series := h.series[key]
	if series == nil {
		series = &promTimingSeries{
			labels:  copyLabels(labels),
			buckets: make([]uint64, len(h.buckets)),
		}
		h.series[key] = series
	}
	for i, bucket := range h.buckets {
		if value <= bucket {
			series.buckets[i]++
		}
	}
	series.count++
	series.sum += value
}

func (h promTimingHistogram) clone() promTimingHistogram {
	out := promTimingHistogram{
		name:    h.name,
		help:    h.help,
		buckets: append([]float64(nil), h.buckets...),
		series:  make(map[string]*promTimingSeries, len(h.series)),
	}
	for key, series := range h.series {
		out.series[key] = &promTimingSeries{
			labels:  copyLabels(series.labels),
			buckets: append([]uint64(nil), series.buckets...),
			count:   series.count,
			sum:     series.sum,
		}
	}
	return out
}

func isMetricsPath(path string) bool {
	return path == "/metrics"
}

func (a *App) handleMetrics(w http.ResponseWriter, r *http.Request) {
	if !a.metricsEnabled() {
		writeError(w, http.StatusNotFound, "not found", "not_found")
		return
	}
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed", "method_not_allowed")
		return
	}
	w.Header().Set("Content-Type", prometheusContentType)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(a.metricsText()))
}

func (a *App) metricsText() string {
	var b strings.Builder
	admissionMetrics := admission.Metrics{}
	if a != nil && a.Admission != nil {
		admissionMetrics = a.Admission.Metrics()
	}
	quotaMetrics := quota.Metrics{UsedByScope: map[string]int{}}
	if a != nil && a.Quota != nil {
		quotaMetrics = a.Quota.Metrics()
	}
	var backendHealth []proxy.BackendHealth
	if a != nil && a.Router != nil {
		backendHealth = a.Router.HealthSnapshot()
	}
	backendQuotaLimits := map[string]config.LimitsConfig{}
	if a != nil {
		backendQuotaLimits = configuredBackendQuotaLimits(a.Config)
	}

	writeAdmissionMetrics(&b, admissionMetrics)
	writeBackendAdmissionMetrics(&b, admissionMetrics, backendNames(backendHealth))
	writeQuotaMetrics(&b, quotaMetrics)
	writeBackendQuotaMetrics(&b, quotaMetrics, backendQuotaLimits)
	writeRequiredHeaderMetrics(&b, a.requiredHeaderMissingMetric())
	writeBackendHealthMetrics(&b, backendHealth)
	if a != nil {
		writeTimingHistogramMetrics(&b, a.PrometheusTimings)
	}
	return b.String()
}

func (a *App) recordRequiredHeaderMissing() {
	if a == nil {
		return
	}
	a.runtimeRoot().requiredHeaderMissingTotal.Add(1)
}

func (a *App) requiredHeaderMissingMetric() uint64 {
	if a == nil {
		return 0
	}
	return a.runtimeRoot().requiredHeaderMissingTotal.Load()
}

func writeAdmissionMetrics(b *strings.Builder, metrics admission.Metrics) {
	active := 0
	requestsTotal := 0
	queuedTotal := 0
	rateLimitedTotal := 0
	maxQueueDepth := 0
	maxConcurrencyLimit := 0
	totalQueuedForWait := 0
	totalWait := time.Duration(0)
	for _, state := range metrics.State {
		active += state.ActiveCount
		requestsTotal += state.TotalRequests
		queuedTotal += state.TotalQueued
		rateLimitedTotal += state.RateLimited
		if state.MaxQueueDepth > maxQueueDepth {
			maxQueueDepth = state.MaxQueueDepth
		}
		if state.ConcurrencyLimit > maxConcurrencyLimit {
			maxConcurrencyLimit = state.ConcurrencyLimit
		}
		if state.TotalQueued > 0 {
			totalQueuedForWait += state.TotalQueued
			totalWait += state.AverageWait * time.Duration(state.TotalQueued)
		}
	}
	averageWaitSeconds := 0.0
	if totalQueuedForWait > 0 {
		averageWaitSeconds = totalWait.Seconds() / float64(totalQueuedForWait)
	}

	writeMetric(b, "yallmp_admission_active_requests", "Current active requests held by admission control.", "gauge", []promSample{{Value: float64(active)}})
	writeMetric(b, "yallmp_admission_global_queue_depth", "Current global admission queue depth.", "gauge", []promSample{{Value: float64(metrics.GlobalQueueDepth)}})
	writeMetric(b, "yallmp_admission_requests_total", "Total requests seen by admission control.", "counter", []promSample{{Value: float64(requestsTotal)}})
	writeMetric(b, "yallmp_admission_queued_total", "Total requests queued by admission control.", "counter", []promSample{{Value: float64(queuedTotal)}})
	writeMetric(b, "yallmp_admission_rate_limited_total", "Total requests rejected by admission rate limits.", "counter", []promSample{{Value: float64(rateLimitedTotal)}})
	writeMetric(b, "yallmp_admission_max_queue_depth", "Maximum admission queue depth observed for any scope.", "gauge", []promSample{{Value: float64(maxQueueDepth)}})
	writeMetric(b, "yallmp_admission_concurrency_limit", "Largest positive admission concurrency limit observed for any scope.", "gauge", []promSample{{Value: float64(maxConcurrencyLimit)}})
	writeMetric(b, "yallmp_admission_average_queue_wait_seconds", "Average queue wait across queued admission requests.", "gauge", []promSample{{Value: averageWaitSeconds}})
}

func writeBackendAdmissionMetrics(b *strings.Builder, metrics admission.Metrics, allowedBackends map[string]bool) {
	type backendAdmission struct {
		Backend          string
		Active           int
		QueueDepth       int
		RequestsTotal    int
		QueuedTotal      int
		RateLimitedTotal int
		MaxQueueDepth    int
		ConcurrencyLimit int
		AverageWait      time.Duration
	}
	backends := make([]backendAdmission, 0)
	for scope, state := range metrics.State {
		backend, ok := strings.CutPrefix(scope, "backend:")
		if !ok || backend == "" {
			continue
		}
		if !allowedBackends[backend] {
			continue
		}
		backends = append(backends, backendAdmission{
			Backend:          backend,
			Active:           state.ActiveCount,
			QueueDepth:       metrics.QueuedByScope[scope],
			RequestsTotal:    state.TotalRequests,
			QueuedTotal:      state.TotalQueued,
			RateLimitedTotal: state.RateLimited,
			MaxQueueDepth:    state.MaxQueueDepth,
			ConcurrencyLimit: state.ConcurrencyLimit,
			AverageWait:      state.AverageWait,
		})
	}
	sort.Slice(backends, func(i, j int) bool {
		return backends[i].Backend < backends[j].Backend
	})
	samples := func(value func(backendAdmission) float64) []promSample {
		out := make([]promSample, 0, len(backends))
		for _, backend := range backends {
			out = append(out, promSample{Labels: map[string]string{"backend": backend.Backend}, Value: value(backend)})
		}
		return out
	}
	writeMetric(b, "yallmp_backend_admission_active_requests", "Current active requests held by backend admission control.", "gauge", samples(func(backend backendAdmission) float64 {
		return float64(backend.Active)
	}))
	writeMetric(b, "yallmp_backend_admission_queue_depth", "Current backend admission queue depth.", "gauge", samples(func(backend backendAdmission) float64 {
		return float64(backend.QueueDepth)
	}))
	writeMetric(b, "yallmp_backend_admission_requests_total", "Total requests seen by backend admission control.", "counter", samples(func(backend backendAdmission) float64 {
		return float64(backend.RequestsTotal)
	}))
	writeMetric(b, "yallmp_backend_admission_queued_total", "Total requests queued by backend admission control.", "counter", samples(func(backend backendAdmission) float64 {
		return float64(backend.QueuedTotal)
	}))
	writeMetric(b, "yallmp_backend_admission_rate_limited_total", "Total requests rejected by backend admission rate limits.", "counter", samples(func(backend backendAdmission) float64 {
		return float64(backend.RateLimitedTotal)
	}))
	writeMetric(b, "yallmp_backend_admission_max_queue_depth", "Maximum backend admission queue depth observed.", "gauge", samples(func(backend backendAdmission) float64 {
		return float64(backend.MaxQueueDepth)
	}))
	writeMetric(b, "yallmp_backend_admission_concurrency_limit", "Current backend admission concurrency limit.", "gauge", samples(func(backend backendAdmission) float64 {
		return float64(backend.ConcurrencyLimit)
	}))
	writeMetric(b, "yallmp_backend_admission_average_queue_wait_seconds", "Average backend admission queue wait.", "gauge", samples(func(backend backendAdmission) float64 {
		return backend.AverageWait.Seconds()
	}))
}

func backendNames(health []proxy.BackendHealth) map[string]bool {
	names := map[string]bool{}
	for _, backend := range health {
		if backend.Name != "" {
			names[backend.Name] = true
		}
	}
	return names
}

func writeQuotaMetrics(b *strings.Builder, metrics quota.Metrics) {
	usedTotal := 0
	for _, used := range metrics.UsedByScope {
		usedTotal += used
	}
	nextReset := time.Time{}
	for _, resetAt := range metrics.ResetAtByScope {
		if resetAt.IsZero() {
			continue
		}
		if nextReset.IsZero() || resetAt.Before(nextReset) {
			nextReset = resetAt
		}
	}
	nextResetUnix := 0.0
	if !nextReset.IsZero() {
		nextResetUnix = float64(nextReset.Unix())
	}
	writeMetric(b, "yallmp_quota_tokens_used", "Total tokens currently used across quota scopes.", "gauge", []promSample{{Value: float64(usedTotal)}})
	writeMetric(b, "yallmp_quota_scope_count", "Number of quota scopes currently tracked.", "gauge", []promSample{{Value: float64(len(metrics.UsedByScope))}})
	writeMetric(b, "yallmp_quota_next_reset_timestamp_seconds", "Earliest quota reset timestamp across scopes, or 0 when no window is active.", "gauge", []promSample{{Value: nextResetUnix}})
}

func writeBackendQuotaMetrics(b *strings.Builder, metrics quota.Metrics, limitsByBackend map[string]config.LimitsConfig) {
	names := make([]string, 0, len(limitsByBackend))
	for name := range limitsByBackend {
		names = append(names, name)
	}
	sort.Strings(names)
	usedSamples := make([]promSample, 0, len(names))
	limitSamples := make([]promSample, 0, len(names))
	remainingSamples := make([]promSample, 0, len(names))
	resetSamples := make([]promSample, 0, len(names))
	for _, name := range names {
		limits := limitsByBackend[name]
		if limits.TokenQuota <= 0 {
			continue
		}
		labels := map[string]string{"backend": name}
		scope := quota.BackendScope(name)
		used := metrics.UsedByScope[scope]
		remaining := limits.TokenQuota - used
		if remaining < 0 {
			remaining = 0
		}
		resetAt := 0.0
		if value := metrics.ResetAtByScope[scope]; !value.IsZero() {
			resetAt = float64(value.Unix())
		}
		usedSamples = append(usedSamples, promSample{Labels: labels, Value: float64(used)})
		limitSamples = append(limitSamples, promSample{Labels: labels, Value: float64(limits.TokenQuota)})
		remainingSamples = append(remainingSamples, promSample{Labels: labels, Value: float64(remaining)})
		resetSamples = append(resetSamples, promSample{Labels: labels, Value: resetAt})
	}
	writeMetric(b, "yallmp_backend_quota_tokens_used", "Tokens currently used for configured backend quota scopes.", "gauge", usedSamples)
	writeMetric(b, "yallmp_backend_quota_tokens_limit", "Configured token quota for backend scopes.", "gauge", limitSamples)
	writeMetric(b, "yallmp_backend_quota_tokens_remaining", "Remaining tokens for configured backend quota scopes.", "gauge", remainingSamples)
	writeMetric(b, "yallmp_backend_quota_next_reset_timestamp_seconds", "Backend quota reset timestamp, or 0 when no window is active.", "gauge", resetSamples)
}

func writeRequiredHeaderMetrics(b *strings.Builder, missingTotal uint64) {
	writeMetric(b, "yallmp_required_header_missing_total", "Requests rejected because a required header was missing.", "counter", []promSample{{Value: float64(missingTotal)}})
}

func writeBackendHealthMetrics(b *strings.Builder, health []proxy.BackendHealth) {
	sort.SliceStable(health, func(i, j int) bool {
		return health[i].Name < health[j].Name
	})
	samples := func(value func(proxy.BackendHealth) float64) []promSample {
		out := make([]promSample, 0, len(health))
		for _, backend := range health {
			if backend.Name == "" {
				continue
			}
			out = append(out, promSample{Labels: map[string]string{"backend": backend.Name}, Value: value(backend)})
		}
		return out
	}
	writeMetric(b, "yallmp_backend_healthy", "1 when backend is currently healthy, 0 otherwise.", "gauge", samples(func(backend proxy.BackendHealth) float64 {
		if backend.Healthy {
			return 1
		}
		return 0
	}))
	writeMetric(b, "yallmp_backend_health_state", "Current backend health state: 0 healthy, 1 half-open, 2 unhealthy.", "gauge", samples(func(backend proxy.BackendHealth) float64 {
		switch backend.State {
		case "half_open":
			return 1
		case "unhealthy":
			return 2
		default:
			if backend.Healthy {
				return 0
			}
			return 2
		}
	}))
	writeMetric(b, "yallmp_backend_inflight_requests", "Current live upstream attempts for configured backends.", "gauge", samples(func(backend proxy.BackendHealth) float64 {
		return float64(backend.InflightRequests)
	}))
	writeMetric(b, "yallmp_backend_score", "Current backend health score.", "gauge", samples(func(backend proxy.BackendHealth) float64 {
		return backend.Score
	}))
	writeMetric(b, "yallmp_backend_success_total", "Total successful backend attempts.", "counter", samples(func(backend proxy.BackendHealth) float64 {
		return float64(backend.SuccessCount)
	}))
	writeMetric(b, "yallmp_backend_failure_total", "Total failed backend attempts.", "counter", samples(func(backend proxy.BackendHealth) float64 {
		return float64(backend.FailureCount)
	}))
	writeMetric(b, "yallmp_backend_consecutive_failures", "Current consecutive backend failures.", "gauge", samples(func(backend proxy.BackendHealth) float64 {
		return float64(backend.ConsecutiveFailures)
	}))
	writeMetric(b, "yallmp_backend_last_status", "Last observed backend HTTP status, or 0 when none has been observed.", "gauge", samples(func(backend proxy.BackendHealth) float64 {
		return float64(backend.LastStatus)
	}))
	writeMetric(b, "yallmp_backend_last_success_timestamp_seconds", "Unix timestamp of the last successful backend attempt, or 0 when none has been observed.", "gauge", samples(func(backend proxy.BackendHealth) float64 {
		return unixSeconds(backend.LastSuccess)
	}))
	writeMetric(b, "yallmp_backend_last_failure_timestamp_seconds", "Unix timestamp of the last failed backend attempt, or 0 when none has been observed.", "gauge", samples(func(backend proxy.BackendHealth) float64 {
		return unixSeconds(backend.LastFailure)
	}))
	writeMetric(b, "yallmp_backend_unhealthy_until_timestamp_seconds", "Unix timestamp until which the backend is considered unhealthy, or 0 when healthy.", "gauge", samples(func(backend proxy.BackendHealth) float64 {
		return unixSeconds(backend.UnhealthyUntil)
	}))
}

func unixSeconds(value time.Time) float64 {
	if value.IsZero() {
		return 0
	}
	return float64(value.Unix())
}

func writeMetric(b *strings.Builder, name string, help string, metricType string, samples []promSample) {
	b.WriteString("# HELP ")
	b.WriteString(name)
	b.WriteByte(' ')
	b.WriteString(escapeHelp(help))
	b.WriteByte('\n')
	b.WriteString("# TYPE ")
	b.WriteString(name)
	b.WriteByte(' ')
	b.WriteString(metricType)
	b.WriteByte('\n')
	sort.SliceStable(samples, func(i, j int) bool {
		return labelSet(samples[i].Labels) < labelSet(samples[j].Labels)
	})
	for _, sample := range samples {
		if math.IsNaN(sample.Value) || math.IsInf(sample.Value, 0) {
			continue
		}
		b.WriteString(name)
		b.WriteString(labelSet(sample.Labels))
		b.WriteByte(' ')
		b.WriteString(strconv.FormatFloat(sample.Value, 'f', -1, 64))
		b.WriteByte('\n')
	}
}

func writeTimingHistogramMetrics(b *strings.Builder, recorder *prometheusTimingRecorder) {
	for _, histogram := range recorder.snapshot() {
		writeHistogramMetric(b, histogram)
	}
}

func writeHistogramMetric(b *strings.Builder, histogram promTimingHistogram) {
	if len(histogram.series) == 0 {
		return
	}
	b.WriteString("# HELP ")
	b.WriteString(histogram.name)
	b.WriteByte(' ')
	b.WriteString(escapeHelp(histogram.help))
	b.WriteByte('\n')
	b.WriteString("# TYPE ")
	b.WriteString(histogram.name)
	b.WriteString(" histogram\n")

	samples := make([]promHistogramSample, 0, len(histogram.series))
	for _, series := range histogram.series {
		samples = append(samples, promHistogramSample{
			Labels:  copyLabels(series.labels),
			Buckets: append([]uint64(nil), series.buckets...),
			Count:   series.count,
			Sum:     series.sum,
		})
	}
	sort.SliceStable(samples, func(i, j int) bool {
		return labelSet(samples[i].Labels) < labelSet(samples[j].Labels)
	})
	for _, sample := range samples {
		for i, bucket := range histogram.buckets {
			labels := copyLabels(sample.Labels)
			labels["le"] = formatBucket(bucket)
			writeHistogramSample(b, histogram.name+"_bucket", labels, float64(sample.Buckets[i]))
		}
		labels := copyLabels(sample.Labels)
		labels["le"] = "+Inf"
		writeHistogramSample(b, histogram.name+"_bucket", labels, float64(sample.Count))
		writeHistogramSample(b, histogram.name+"_sum", sample.Labels, sample.Sum)
		writeHistogramSample(b, histogram.name+"_count", sample.Labels, float64(sample.Count))
	}
}

func writeHistogramSample(b *strings.Builder, name string, labels map[string]string, value float64) {
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return
	}
	b.WriteString(name)
	b.WriteString(labelSet(labels))
	b.WriteByte(' ')
	b.WriteString(strconv.FormatFloat(value, 'f', -1, 64))
	b.WriteByte('\n')
}

func formatBucket(bucket float64) string {
	return strconv.FormatFloat(bucket, 'f', -1, 64)
}

func labelSet(labels map[string]string) string {
	if len(labels) == 0 {
		return ""
	}
	keys := make([]string, 0, len(labels))
	for key := range labels {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, fmt.Sprintf(`%s="%s"`, key, escapeLabelValue(labels[key])))
	}
	return "{" + strings.Join(parts, ",") + "}"
}

func escapeLabelValue(value string) string {
	value = strings.ReplaceAll(value, `\`, `\\`)
	value = strings.ReplaceAll(value, "\n", `\n`)
	value = strings.ReplaceAll(value, `"`, `\"`)
	return value
}

func escapeHelp(value string) string {
	value = strings.ReplaceAll(value, `\`, `\\`)
	value = strings.ReplaceAll(value, "\n", `\n`)
	return value
}

func copyLabels(labels map[string]string) map[string]string {
	if len(labels) == 0 {
		return nil
	}
	out := make(map[string]string, len(labels))
	for key, value := range labels {
		out[key] = value
	}
	return out
}

func promEndpointKind(endpointKind string) string {
	switch observability.EndpointKind(endpointKind) {
	case "chat", "messages", "responses", "embeddings", "rerank", "passthrough", "other", "unknown":
		return observability.EndpointKind(endpointKind)
	default:
		return "other"
	}
}

func promOutcome(outcome string) string {
	switch strings.TrimSpace(outcome) {
	case observability.OutcomeSuccess, observability.OutcomeError, observability.OutcomeBlocked, observability.OutcomeRetry, observability.OutcomeFinal, observability.OutcomeCacheHit, observability.OutcomeRouted:
		return strings.TrimSpace(outcome)
	default:
		return observability.OutcomeError
	}
}

func promErrorType(errorType string) string {
	switch strings.TrimSpace(errorType) {
	case "none", "context_canceled", "timeout", "rate_limit", "quota", "queue", "permission", "not_found", "unsupported_endpoint", "network", "client_closed", "http_4xx", "http_5xx", "error":
		return strings.TrimSpace(errorType)
	default:
		return "error"
	}
}

func promStatusClass(statusCode int) string {
	switch {
	case statusCode == 0:
		return "none"
	case statusCode == 499:
		return "499"
	case statusCode >= 100 && statusCode < 200:
		return "1xx"
	case statusCode >= 200 && statusCode < 300:
		return "2xx"
	case statusCode >= 300 && statusCode < 400:
		return "3xx"
	case statusCode >= 400 && statusCode < 500:
		return "4xx"
	case statusCode >= 500 && statusCode < 600:
		return "5xx"
	default:
		return "other"
	}
}

func promSafeLabel(value string) string {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 128 || !promSafeLabelStart(value[0]) {
		return "unknown"
	}
	for i := 1; i < len(value); i++ {
		if !promSafeLabelChar(value[i]) {
			return "unknown"
		}
	}
	return value
}

func promSafeLabelStart(c byte) bool {
	return (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9')
}

func promSafeLabelChar(c byte) bool {
	return promSafeLabelStart(c) || c == '.' || c == '_' || c == ':' || c == '/' || c == '-'
}

func promBackendLabel(value string) string {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 128 || !promSafeLabelStart(value[0]) {
		return "unknown"
	}
	for i := 1; i < len(value); i++ {
		if !promBackendLabelChar(value[i]) {
			return "unknown"
		}
	}
	return value
}

func promBackendLabelChar(c byte) bool {
	return promSafeLabelStart(c) || c == '.' || c == '_' || c == '-'
}
