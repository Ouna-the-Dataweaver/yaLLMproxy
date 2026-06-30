package server

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/theio/yallmp-proxy/internal/config"
	"github.com/theio/yallmp-proxy/internal/observability"
	"github.com/theio/yallmp-proxy/internal/proxy"
	"github.com/theio/yallmp-proxy/internal/quota"
)

func TestMetricsDisabledByDefault(t *testing.T) {
	app := newMetricsTestApp(t, nil)

	resp := httptest.NewRecorder()
	app.ServeHTTP(resp, httptest.NewRequest(http.MethodGet, "/metrics", nil))

	if resp.Code != http.StatusNotFound {
		t.Fatalf("status = %d body=%s", resp.Code, resp.Body.String())
	}
}

func TestMetricsInvalidEnabledValueDoesNotEnable(t *testing.T) {
	for _, value := range []any{[]any{"true"}, 2, int64(2), 2.5} {
		app := newMetricsTestApp(t, value)

		resp := httptest.NewRecorder()
		app.ServeHTTP(resp, httptest.NewRequest(http.MethodGet, "/metrics", nil))

		if resp.Code != http.StatusNotFound {
			t.Fatalf("metrics.enabled=%#v status = %d body=%s", value, resp.Code, resp.Body.String())
		}
		issues := app.Config.Validate()
		if len(issues) != 1 || issues[0].Path != "proxy_settings.metrics.enabled" || issues[0].Code != "invalid_bool" {
			t.Fatalf("metrics.enabled=%#v issues = %#v", value, issues)
		}
	}
}

func TestMetricsEnabledRejectsNonGet(t *testing.T) {
	app := newMetricsTestApp(t, true)

	resp := httptest.NewRecorder()
	app.ServeHTTP(resp, httptest.NewRequest(http.MethodPost, "/metrics", nil))

	if resp.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d body=%s", resp.Code, resp.Body.String())
	}
}

func TestMetricsEnabledServesPrometheusSnapshot(t *testing.T) {
	app := newMetricsTestApp(t, "yes")
	app.Quota.Seed("raw-key-id", "alpha", 7)
	app.Quota.Seed("group:raw-group-id", "alpha", 5)
	escapedBackend := "z\\quote\"line\n"
	app.Router.RegisterBackend(proxy.Backend{Name: escapedBackend, BaseURL: "http://escaped.test/v1"}, nil)
	releaseInflight := app.Router.TrackBackendAttempt(escapedBackend)
	defer releaseInflight()
	slot, err := app.Admission.Acquire(context.Background(), "backend:"+escapedBackend, 1, 100, "")
	if err != nil {
		t.Fatal(err)
	}
	slot.Release()

	resp := httptest.NewRecorder()
	app.ServeHTTP(resp, httptest.NewRequest(http.MethodGet, "/metrics?ignored=true", nil))

	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", resp.Code, resp.Body.String())
	}
	if contentType := resp.Header().Get("Content-Type"); contentType != prometheusContentType {
		t.Fatalf("content-type = %q", contentType)
	}
	body := resp.Body.String()
	assertContains(t, body, "# HELP yallmp_admission_requests_total Total requests seen by admission control.")
	assertContains(t, body, "# TYPE yallmp_admission_requests_total counter")
	assertContains(t, body, "yallmp_quota_tokens_used 12")
	assertContains(t, body, "yallmp_quota_scope_count 2")
	assertContains(t, body, "yallmp_required_header_missing_total 0")
	assertContains(t, body, `yallmp_backend_healthy{backend="alpha"} 1`)
	assertContains(t, body, `yallmp_backend_health_state{backend="alpha"} 0`)
	assertContains(t, body, `yallmp_backend_healthy{backend="z\\quote\"line\n"} 1`)
	assertContains(t, body, `yallmp_backend_inflight_requests{backend="z\\quote\"line\n"} 1`)
	assertContains(t, body, `yallmp_backend_admission_requests_total{backend="z\\quote\"line\n"} 1`)

	second := httptest.NewRecorder()
	app.ServeHTTP(second, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if second.Body.String() != body {
		t.Fatalf("metrics output was not deterministic\nfirst:\n%s\nsecond:\n%s", body, second.Body.String())
	}
}

func TestMetricsIncludesConfiguredBackendQuotaWithoutRawScopes(t *testing.T) {
	app, err := New(&config.Config{
		ProxySettings: config.ProxySettings{
			Metrics: config.MetricsConfig{Enabled: true},
		},
		RouterSettings: config.RouterSettings{
			QuotaAwareSelection: config.QuotaAwareSelectionSettings{Enabled: true},
		},
		Nodes: []config.NodeEntry{{
			Name:    "n1",
			APIBase: "http://n1-secret.test/v1",
			APIKey:  "upstream-secret",
			Model:   "openai/n1",
			Limits:  config.LimitsConfig{TokenQuota: 20},
		}},
		Models: []config.ClientModelEntry{{
			Name:  "alpha",
			Nodes: []string{"n1"},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	app.Quota.Seed(quota.BackendScopeKey, "n1", 7)
	app.Quota.Seed(quota.BackendScopeKey, "removed", 99)
	app.Quota.Seed("raw-key-id", "alpha", 5)

	body := getMetricsBody(t, app)
	assertContains(t, body, `yallmp_backend_quota_tokens_used{backend="n1"} 7`)
	assertContains(t, body, `yallmp_backend_quota_tokens_limit{backend="n1"} 20`)
	assertContains(t, body, `yallmp_backend_quota_tokens_remaining{backend="n1"} 13`)
	for _, forbidden := range []string{
		"raw-key-id",
		"backend::removed",
		"removed",
		"upstream-secret",
		"http://",
		"app_key",
	} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("metrics leaked %q in body:\n%s", forbidden, body)
		}
	}
}

func TestMetricsReflectsBackendHealthAfterTraffic(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Errorf("path = %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}],"usage":{"total_tokens":8}}`))
	}))
	defer upstream.Close()
	app := newMetricsTestAppWithBase(t, true, upstream.URL+"/v1")

	resp := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"alpha","messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Content-Type", "application/json")
	app.ServeHTTP(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("chat status = %d body=%s", resp.Code, resp.Body.String())
	}

	body := getMetricsBody(t, app)
	assertContains(t, body, `yallmp_backend_success_total{backend="alpha"} 1`)
	assertContains(t, body, `yallmp_backend_failure_total{backend="alpha"} 0`)
	assertContains(t, body, `yallmp_backend_healthy{backend="alpha"} 1`)
	assertContains(t, body, `yallmp_backend_health_state{backend="alpha"} 0`)
	assertContains(t, body, `yallmp_backend_last_status{backend="alpha"} 200`)
	assertContains(t, body, "yallmp_admission_requests_total 1")
}

func TestPrometheusTimingHistogramsDisabledWhenMetricsEnabledOnly(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}],"usage":{"total_tokens":8}}`))
	}))
	defer upstream.Close()
	app := newMetricsTestAppWithBase(t, true, upstream.URL+"/v1")

	resp := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"alpha","messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Content-Type", "application/json")
	app.ServeHTTP(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("chat status = %d body=%s", resp.Code, resp.Body.String())
	}

	body := getMetricsBody(t, app)
	assertNotContains(t, body, "yallmp_request_duration_seconds")
	assertNotContains(t, body, "yallmp_backend_request_duration_seconds")
	assertNotContains(t, body, "yallmp_stream_ttft_seconds")
	assertNotContains(t, body, "yallmp_stream_tpot_seconds")
}

func TestPrometheusTimingRecorderRequiresMetricsEndpoint(t *testing.T) {
	app := newMetricsTestAppWithTimingsBase(t, false, true, "http://upstream.test/v1")
	if app.PrometheusTimings != nil {
		t.Fatal("timing recorder should be nil when metrics endpoint is disabled")
	}
	if app.requestMetricsEnabled() {
		t.Fatal("request metrics should be disabled when OTel and /metrics are disabled")
	}

	resp := httptest.NewRecorder()
	app.ServeHTTP(resp, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if resp.Code != http.StatusNotFound {
		t.Fatalf("metrics status = %d body=%s", resp.Code, resp.Body.String())
	}
}

func TestPrometheusTimingHistogramsRecordWithOTelDisabled(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}],"usage":{"total_tokens":8}}`))
	}))
	defer upstream.Close()
	app := newMetricsTestAppWithTimingsBase(t, true, true, upstream.URL+"/v1")
	if app.otelEnabled() {
		t.Fatal("OTel should be disabled for this test")
	}

	resp := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"alpha","messages":[{"role":"user","content":"prompt-secret"}]}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer client-secret")
	app.ServeHTTP(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("chat status = %d body=%s", resp.Code, resp.Body.String())
	}

	body := getMetricsBody(t, app)
	assertContains(t, body, "# TYPE yallmp_request_duration_seconds histogram")
	assertContains(t, body, `yallmp_request_duration_seconds_count{endpoint_kind="chat",error_type="none",model="alpha",outcome="success",status_class="2xx",stream="false"} 1`)
	assertContains(t, body, "# TYPE yallmp_backend_request_duration_seconds histogram")
	assertContains(t, body, `yallmp_backend_request_duration_seconds_count{backend="alpha",endpoint_kind="chat",error_type="none",model="alpha",outcome="final",status_class="2xx",stream="false"} 1`)
	for _, forbidden := range []string{
		"prompt-secret",
		"client-secret",
		"upstream-key",
		upstream.URL,
		"/v1/chat/completions",
		"Authorization",
	} {
		assertNotContains(t, body, forbidden)
	}
}

func TestPrometheusTimingHistogramsDoNotDoubleRecordOTelMetrics(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}],"usage":{"total_tokens":8}}`))
	}))
	defer upstream.Close()
	app := newMetricsTestAppWithTimingsBase(t, true, true, upstream.URL+"/v1")
	provider, recorder := observability.NewInMemoryProvider("test-proxy")
	app.SetObservability(provider)

	resp := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"alpha","messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Content-Type", "application/json")
	app.ServeHTTP(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("chat status = %d body=%s", resp.Code, resp.Body.String())
	}

	otelMetrics := collectOTelMetrics(t, recorder)
	if got := metricHistogramCount(otelMetrics, "yallmp.proxy.request.duration", map[string]string{"endpoint_kind": "chat", "model": "alpha", "outcome": "success"}); got != 1 {
		t.Fatalf("OTel request duration count = %d, want 1", got)
	}
	body := getMetricsBody(t, app)
	assertContains(t, body, `yallmp_request_duration_seconds_count{endpoint_kind="chat",error_type="none",model="alpha",outcome="success",status_class="2xx",stream="false"} 1`)
}

func TestPrometheusStreamTimingRecordsSuccessfulWritesOnly(t *testing.T) {
	recorder := newPrometheusTimingRecorder(config.MetricsTimingsConfig{Enabled: true})
	observer := &streamTimingObserver{
		ctx:               context.Background(),
		prometheusTimings: recorder,
		endpointKind:      "chat",
		modelName:         "alpha",
		start:             time.Now().Add(-10 * time.Millisecond),
	}
	resp := httptest.NewRecorder()
	_, err := copyStreaming(resp, &chunkStringReader{chunks: []string{"data: one\n\n", "data: two\n\n"}}, observer)
	if err != nil {
		t.Fatal(err)
	}
	body := timingMetricsText(recorder)
	assertContains(t, body, `yallmp_stream_ttft_seconds_count{endpoint_kind="chat",model="alpha"} 1`)
	assertContains(t, body, `yallmp_stream_tpot_seconds_count{endpoint_kind="chat",model="alpha"} 1`)

	failingRecorder := newPrometheusTimingRecorder(config.MetricsTimingsConfig{Enabled: true})
	failingObserver := &streamTimingObserver{
		ctx:               context.Background(),
		prometheusTimings: failingRecorder,
		endpointKind:      "chat",
		modelName:         "alpha",
		start:             time.Now().Add(-10 * time.Millisecond),
	}
	_, err = copyStreaming(&failingResponseWriter{err: errors.New("client closed prompt-secret")}, &chunkStringReader{chunks: []string{"data: one\n\n"}}, failingObserver)
	if err == nil {
		t.Fatal("expected client write error")
	}
	assertNotContains(t, timingMetricsText(failingRecorder), "yallmp_stream_")

	readErrorRecorder := newPrometheusTimingRecorder(config.MetricsTimingsConfig{Enabled: true})
	readErrorObserver := &streamTimingObserver{
		ctx:               context.Background(),
		prometheusTimings: readErrorRecorder,
		endpointKind:      "chat",
		modelName:         "alpha",
		start:             time.Now().Add(-10 * time.Millisecond),
	}
	_, err = copyStreaming(httptest.NewRecorder(), &errorAfterChunksReader{
		chunks: []string{"data: one\n\n"},
		err:    errors.New("upstream stream failed prompt-secret"),
	}, readErrorObserver)
	if err == nil {
		t.Fatal("expected upstream read error")
	}
	assertNotContains(t, timingMetricsText(readErrorRecorder), "yallmp_stream_")
}

func TestPrometheusStreamTimingSkipsErrorStatus(t *testing.T) {
	app := &App{PrometheusTimings: newPrometheusTimingRecorder(config.MetricsTimingsConfig{Enabled: true})}
	observer := app.newStreamTimingObserver(context.Background(), "chat", "alpha", time.Now(), http.StatusBadGateway)
	if observer != nil {
		t.Fatal("observer should be nil for error status")
	}
	resp := httptest.NewRecorder()
	_, err := copyStreaming(resp, &chunkStringReader{chunks: []string{"data: error\n\n"}}, observer)
	if err != nil {
		t.Fatal(err)
	}
	assertNotContains(t, timingMetricsText(app.PrometheusTimings), "yallmp_stream_")
}

func TestPrometheusTimingLabelsAreBoundedAndSanitized(t *testing.T) {
	recorder := newPrometheusTimingRecorder(config.MetricsTimingsConfig{Enabled: true})
	recorder.RecordRequest(prometheusRequestTiming{
		EndpointKind: "chat?api_key=raw-secret",
		Model:        "Bearer raw-secret",
		Stream:       true,
		Outcome:      "raw-outcome",
		StatusCode:   http.StatusServiceUnavailable,
		Error:        errors.New("Authorization: Bearer raw-secret https://backend-secret.test/v1?api_key=raw-secret"),
		Duration:     time.Millisecond,
	})
	recorder.RecordBackendRequest(prometheusBackendTiming{
		EndpointKind: "responses",
		Model:        "raw model with spaces",
		Backend:      "https://backend-secret.test/v1",
		Stream:       false,
		Outcome:      "raw-outcome",
		StatusCode:   0,
		ErrorType:    "raw-error",
		Duration:     time.Millisecond,
	})

	body := timingMetricsText(recorder)
	assertContains(t, body, `model="unknown"`)
	assertContains(t, body, `backend="unknown"`)
	assertContains(t, body, `error_type="http_5xx"`)
	assertContains(t, body, `error_type="error"`)
	for _, forbidden := range []string{
		"raw-secret",
		"Authorization",
		"backend-secret",
		"https://backend-secret.test/v1",
		"api_key",
		"raw-error",
		"raw-outcome",
	} {
		assertNotContains(t, body, forbidden)
	}
}

func TestMetricsDoesNotExposeRawAppKeyScopeOrPayload(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}],"usage":{"total_tokens":18}}`))
	}))
	defer upstream.Close()
	enabled := true
	app, err := New(&config.Config{
		ModelList: []config.ModelEntry{{
			ModelName: "alpha",
			ModelParams: map[string]any{
				"api_base": upstream.URL + "/v1",
				"model":    "openai/upstream",
			},
		}},
		ProxySettings: config.ProxySettings{
			Metrics: config.MetricsConfig{Enabled: true},
		},
		AppKeys: config.AppKeysConfig{
			Enabled: true,
			RequiredHeaders: []config.RequiredHeader{{
				Name: "X-Tenant-ID",
			}},
			Keys: []config.AppKeyEntry{{
				KeyID: "backend:raw-key-id", GroupID: "raw-group-id", Secret: "super-secret", Enabled: &enabled, Models: []string{"alpha"},
				Limits: config.LimitsConfig{TokenQuota: 1000, Concurrency: 1},
			}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	resp := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"alpha","messages":[{"role":"user","content":"sensitive prompt text"}]}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer super-secret")
	req.Header.Set("X-Tenant-ID", "tenant-secret-value")
	app.ServeHTTP(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("chat status = %d body=%s", resp.Code, resp.Body.String())
	}

	body := getMetricsBody(t, app)
	assertContains(t, body, "yallmp_quota_tokens_used 18")
	assertContains(t, body, "yallmp_admission_requests_total 1")
	assertContains(t, body, `yallmp_backend_success_total{backend="alpha"} 1`)
	for _, forbidden := range []string{
		"raw-key-id",
		"backend:raw-key-id",
		"raw-key-id::alpha",
		"raw-group-id",
		"group:raw-group-id",
		"super-secret",
		"tenant-secret-value",
		"sensitive prompt text",
		"/v1/chat/completions",
		"app_key",
		"last_error",
	} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("metrics leaked %q in body:\n%s", forbidden, body)
		}
	}
}

func getMetricsBody(t *testing.T, app *App) string {
	t.Helper()
	resp := httptest.NewRecorder()
	app.ServeHTTP(resp, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if resp.Code != http.StatusOK {
		t.Fatalf("metrics status = %d body=%s", resp.Code, resp.Body.String())
	}
	return resp.Body.String()
}

func assertContains(t *testing.T, body string, want string) {
	t.Helper()
	if !strings.Contains(body, want) {
		t.Fatalf("body missing %q:\n%s", want, body)
	}
}

func assertNotContains(t *testing.T, body string, forbidden string) {
	t.Helper()
	if forbidden != "" && strings.Contains(body, forbidden) {
		t.Fatalf("body leaked %q:\n%s", forbidden, body)
	}
}

func timingMetricsText(recorder *prometheusTimingRecorder) string {
	var b strings.Builder
	writeTimingHistogramMetrics(&b, recorder)
	return b.String()
}

type errorAfterChunksReader struct {
	chunks []string
	err    error
	idx    int
}

func (r *errorAfterChunksReader) Read(p []byte) (int, error) {
	if r.idx < len(r.chunks) {
		chunk := r.chunks[r.idx]
		r.idx++
		return copy(p, chunk), nil
	}
	if r.err != nil {
		return 0, r.err
	}
	return 0, io.EOF
}

func newMetricsTestApp(t *testing.T, metricsEnabled any) *App {
	t.Helper()
	return newMetricsTestAppWithBase(t, metricsEnabled, "http://upstream.test/v1")
}

func newMetricsTestAppWithBase(t *testing.T, metricsEnabled any, apiBase string) *App {
	t.Helper()
	return newMetricsTestAppWithTimingsBase(t, metricsEnabled, nil, apiBase)
}

func newMetricsTestAppWithTimingsBase(t *testing.T, metricsEnabled any, timingsEnabled any, apiBase string) *App {
	t.Helper()
	raw := map[string]any{
		"model_list": []map[string]any{{
			"model_name": "alpha",
			"model_params": map[string]any{
				"api_base": apiBase,
				"api_key":  "upstream-key",
				"model":    "openai/upstream-model",
			},
		}},
	}
	if metricsEnabled != nil {
		metrics := map[string]any{
			"enabled": metricsEnabled,
		}
		if timingsEnabled != nil {
			metrics["timings"] = map[string]any{
				"enabled": timingsEnabled,
			}
		}
		raw["proxy_settings"] = map[string]any{
			"metrics": metrics,
		}
	}
	cfg, err := config.FromMap(raw, "")
	if err != nil {
		t.Fatal(err)
	}
	app, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return app
}
