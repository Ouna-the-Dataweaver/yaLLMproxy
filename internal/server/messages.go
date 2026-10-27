package server

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/theio/yallmp-proxy/internal/admission"
	"github.com/theio/yallmp-proxy/internal/logdb"
	anthropicmessages "github.com/theio/yallmp-proxy/internal/messages"
	"github.com/theio/yallmp-proxy/internal/observability"
	"github.com/theio/yallmp-proxy/internal/policy"
	"github.com/theio/yallmp-proxy/internal/proxy"
	"github.com/theio/yallmp-proxy/internal/quota"
)

func (a *App) handleMessages(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeAnthropicError(w, http.StatusBadRequest, "failed to read request body", "invalid_request_error", "invalid_request_error")
		return
	}
	payload, err := proxy.DecodeJSONBody(body)
	if err != nil {
		writeAnthropicError(w, http.StatusBadRequest, "Invalid JSON payload", "invalid_request_error", "invalid_json")
		entry := a.addLog(r, LogEntry{Time: start, Method: r.Method, Path: r.URL.Path, Status: http.StatusBadRequest, Error: err.Error(), DurationMS: time.Since(start).Milliseconds()})
		payloadPath := a.storePayload(entry, r, body, nil, nil)
		a.persistLog(r.Context(), entry, r, false, policy.Context{}, payloadPath, nil)
		return
	}
	modelName := proxy.NormalizeRequestModel(payload["model"])
	if modelName == "" {
		writeAnthropicError(w, http.StatusBadRequest, "You must provide a model parameter", "invalid_request_error", "missing_parameter")
		return
	}
	isStream, _ := payload["stream"].(bool)
	endpointKind := observability.EndpointKind(r.URL.Path)
	requestStatus := 0
	requestOutcome := observability.OutcomeError
	var requestErr error
	var requestSpan observability.Span
	if a.otelEnabled() {
		ctx, span := a.Observability.StartRequestSpan(r.Context(), observability.RequestAttrs{
			EndpointKind: endpointKind,
			Model:        a.traceModelName(modelName),
			KeyScope:     "unknown",
			Stream:       isStream,
		})
		ctx = contextWithProxyRequestTrace(ctx, span)
		r = r.WithContext(ctx)
		requestSpan = span
		defer func() {
			a.recordRequestMetric(r.Context(), endpointKind, modelName, isStream, requestOutcome, requestStatus, requestErr, time.Since(start))
			requestSpan.End(observability.SpanResult{
				Outcome:    requestOutcome,
				StatusCode: requestStatus,
				Error:      requestErr,
			})
		}()
	}
	if err := validateMessages(payload); err != nil {
		requestStatus = http.StatusBadRequest
		requestErr = err
		writeAnthropicError(w, http.StatusBadRequest, err.Error(), "invalid_request_error", "invalid_request_error")
		entry := a.addLog(r, LogEntry{Time: start, Method: r.Method, Path: r.URL.Path, Model: modelName, Status: http.StatusBadRequest, Error: err.Error(), DurationMS: time.Since(start).Milliseconds()})
		payloadPath := a.storePayload(entry, r, body, nil, nil)
		a.persistLog(r.Context(), entry, r, false, policy.Context{}, payloadPath, nil)
		return
	}
	policyCtx, policySpan := a.startProxyPhase(r.Context(), observability.PhasePolicy, endpointKind, modelName, isStream)
	pctx, err := a.Policy.ValidateRequest(r.WithContext(policyCtx), modelName)
	if err != nil {
		status, errorType, code := a.policyErrorDetails(err)
		policySpan.End(observability.SpanResult{Outcome: observability.OutcomeError, StatusCode: status, Error: err})
		requestStatus = status
		requestErr = err
		writeAnthropicError(w, status, err.Error(), errorType, code)
		return
	}
	policySpan.End(observability.SpanResult{Outcome: observability.OutcomeSuccess})
	requestSpan.SetKeyScope(observability.KeyScope(pctx.Authenticated, pctx.KeyID, pctx.GroupID))
	r = requestWithRequiredHeaderAttribution(r, pctx.RequiredHeaders)
	nativeAnthropic := a.directMessagesBackendIsAnthropic(modelName)
	forwardPath := "/v1/messages"
	forwardPayload := payload
	forwardBody := body
	if !nativeAnthropic {
		forwardPath = "/v1/chat/completions"
		openaiPayload, err := anthropicmessages.ToChatCompletions(payload)
		if err != nil {
			requestStatus = http.StatusBadRequest
			requestErr = err
			writeAnthropicError(w, http.StatusBadRequest, err.Error(), "invalid_request_error", "translation_error")
			entry := a.addLog(r, LogEntry{Time: start, Method: r.Method, Path: r.URL.Path, Model: modelName, Status: http.StatusBadRequest, Error: err.Error(), DurationMS: time.Since(start).Milliseconds()})
			payloadPath := a.storePayload(entry, r, body, nil, nil)
			a.persistLog(r.Context(), entry, r, false, pctx, payloadPath, nil)
			return
		}
		openaiBody, err := json.Marshal(openaiPayload)
		if err != nil {
			requestStatus = http.StatusBadRequest
			requestErr = err
			writeAnthropicError(w, http.StatusBadRequest, err.Error(), "invalid_request_error", "translation_error")
			return
		}
		forwardPayload = openaiPayload
		forwardBody = openaiBody
	}
	limits := a.effectiveLimits(modelName, pctx)
	limitKeyID := pctx.LimitKeyID()
	_, admissionSpan := a.startProxyPhase(r.Context(), observability.PhaseAdmission, endpointKind, modelName, isStream)
	if err := a.Admission.CheckRateLimit(limitKeyID, modelName, admission.RateLimits{RPS: limits.RPS, RPM: limits.RPM}); err != nil {
		admissionSpan.End(observability.SpanResult{Outcome: observability.OutcomeError, StatusCode: http.StatusTooManyRequests, Error: err})
		requestStatus = http.StatusTooManyRequests
		requestErr = err
		writeAnthropicError(w, http.StatusTooManyRequests, "Rate limit exceeded", "rate_limit_error", "rate_limit_exceeded")
		entry := a.addLog(r, LogEntry{Time: start, Method: r.Method, Path: r.URL.Path, Model: modelName, Status: http.StatusTooManyRequests, Error: err.Error(), DurationMS: time.Since(start).Milliseconds()})
		payloadPath := a.storePayload(entry, r, body, nil, nil)
		a.persistLog(r.Context(), entry, r, false, pctx, payloadPath, nil)
		return
	}
	estimatedTokens := estimateRequestTokens(forwardPayload)
	tokenReservation, err := a.Quota.ReserveWithWindowSpec(limitKeyID, modelName, limits.TokenQuota, quotaWindowSpec(limits), estimatedTokens)
	if err != nil {
		admissionSpan.End(observability.SpanResult{Outcome: observability.OutcomeError, StatusCode: http.StatusTooManyRequests, Error: err})
		requestStatus = http.StatusTooManyRequests
		requestErr = err
		writeAnthropicError(w, http.StatusTooManyRequests, "Token quota exceeded", "rate_limit_error", "token_quota_exceeded")
		entry := a.addLog(r, LogEntry{Time: start, Method: r.Method, Path: r.URL.Path, Model: modelName, Status: http.StatusTooManyRequests, Error: err.Error(), DurationMS: time.Since(start).Milliseconds()})
		payloadPath := a.storePayload(entry, r, body, nil, nil)
		a.persistLog(r.Context(), entry, r, false, pctx, payloadPath, nil)
		return
	}
	slot, err := a.acquireSlot(r.Context(), modelName, pctx, limits)
	if err != nil {
		tokenReservation.Release()
		requestErr = err
		if errors.Is(err, admission.ErrQueueTimeout) {
			admissionSpan.End(observability.SpanResult{Outcome: observability.OutcomeError, StatusCode: http.StatusTooManyRequests, Error: err})
			requestStatus = http.StatusTooManyRequests
			writeAnthropicError(w, http.StatusTooManyRequests, "Request queued too long - concurrency limit reached", "rate_limit_error", "concurrency_limit_exceeded")
		} else {
			admissionSpan.End(observability.SpanResult{Outcome: observability.OutcomeError, StatusCode: 499, Error: err})
			requestStatus = 499
			w.WriteHeader(499)
		}
		return
	}
	admissionSpan.End(observability.SpanResult{Outcome: observability.OutcomeSuccess})

	translatedCacheOpts := proxy.ForwardOptions{
		Cacheable:         !nativeAnthropic,
		CacheBypass:       proxy.InferenceCacheBypassRequested(r.Header),
		CacheScope:        inferenceCacheScope(pctx),
		KeyID:             pctx.KeyID,
		BackendAvailable:  a.backendRateLimitAvailable,
		QuotaAvailability: a.backendQuotaAvailability(estimatedTokens),
	}
	var forwardResult proxy.ForwardResult
	var translatedCacheHit bool
	if cachedResult, ok := a.Router.LookupTranslatedInferenceCache(modelName, forwardPath, r.URL.Path, r.URL.RawQuery, forwardBody, forwardPayload, isStream, translatedCacheOpts); ok {
		forwardResult = cachedResult
		translatedCacheHit = true
	} else {
		forwardResult, err = a.Router.ForwardWithOptions(r.Context(), modelName, forwardPath, r.URL.RawQuery, forwardBody, forwardPayload, isStream, a.outboundHeaders(r), proxy.ForwardOptions{
			EndpointKind:      endpointKind,
			BeforeAttempt:     a.backendAttemptLimiter,
			BackendAvailable:  a.backendRateLimitAvailable,
			QuotaAvailability: a.backendQuotaAvailability(estimatedTokens),
			KeyID:             pctx.KeyID,
		})
	}
	if err != nil {
		slot.Release()
		tokenReservation.Release()
		if errors.Is(err, proxy.ErrEndpointNotSupported) {
			requestStatus = http.StatusBadRequest
			requestErr = err
			writeAnthropicError(w, http.StatusBadRequest, err.Error(), "invalid_request_error", "unsupported_endpoint")
			entry := a.addLog(r, withForwardWarnings(LogEntry{Time: start, Method: r.Method, Path: r.URL.Path, Model: modelName, Status: http.StatusBadRequest, Backend: forwardResult.BackendName, Error: err.Error(), DurationMS: time.Since(start).Milliseconds()}, forwardResult))
			payloadPath := a.storePayload(entry, r, body, nil, nil)
			a.persistLog(r.Context(), entry, r, false, pctx, payloadPath, nil)
			return
		}
		if errors.Is(err, proxy.ErrNodeAccessDenied) {
			requestStatus = http.StatusForbidden
			requestErr = err
			writeAnthropicError(w, http.StatusForbidden, err.Error(), "permission_error", "model_access_denied")
			entry := a.addLog(r, withForwardWarnings(LogEntry{Time: start, Method: r.Method, Path: r.URL.Path, Model: modelName, Status: http.StatusForbidden, Backend: forwardResult.BackendName, Error: err.Error(), DurationMS: time.Since(start).Milliseconds()}, forwardResult))
			payloadPath := a.storePayload(entry, r, body, nil, nil)
			a.persistLog(r.Context(), entry, r, false, pctx, payloadPath, nil)
			return
		}
		if errors.Is(err, admission.ErrRateLimitExceeded) {
			requestStatus = http.StatusTooManyRequests
			requestErr = err
			writeAnthropicError(w, http.StatusTooManyRequests, "Backend rate limit exceeded", "rate_limit_error", "rate_limit_exceeded")
			entry := a.addLog(r, withForwardWarnings(LogEntry{Time: start, Method: r.Method, Path: r.URL.Path, Model: modelName, Status: http.StatusTooManyRequests, Backend: forwardResult.BackendName, Error: err.Error(), DurationMS: time.Since(start).Milliseconds()}, forwardResult))
			payloadPath := a.storePayload(entry, r, body, nil, nil)
			a.persistLog(r.Context(), entry, r, false, pctx, payloadPath, nil)
			return
		}
		if errors.Is(err, admission.ErrQueueTimeout) {
			requestStatus = http.StatusTooManyRequests
			requestErr = err
			writeAnthropicError(w, http.StatusTooManyRequests, "Backend concurrency queue timed out", "rate_limit_error", "concurrency_limit_exceeded")
			entry := a.addLog(r, withForwardWarnings(LogEntry{Time: start, Method: r.Method, Path: r.URL.Path, Model: modelName, Status: http.StatusTooManyRequests, Backend: forwardResult.BackendName, Error: err.Error(), DurationMS: time.Since(start).Milliseconds()}, forwardResult))
			payloadPath := a.storePayload(entry, r, body, nil, nil)
			a.persistLog(r.Context(), entry, r, false, pctx, payloadPath, nil)
			return
		}
		requestStatus = http.StatusBadGateway
		requestErr = err
		writeAnthropicError(w, http.StatusBadGateway, err.Error(), "api_error", "backend_error")
		entry := a.addLog(r, withForwardWarnings(LogEntry{Time: start, Method: r.Method, Path: r.URL.Path, Model: modelName, Status: http.StatusBadGateway, Backend: forwardResult.BackendName, Error: err.Error(), DurationMS: time.Since(start).Milliseconds()}, forwardResult))
		payloadPath := a.storePayload(entry, r, body, nil, nil)
		a.persistLog(r.Context(), entry, r, false, pctx, payloadPath, nil)
		return
	}
	resp := forwardResult.Response
	defer resp.Body.Close()
	_, responseSpan := a.startProxyPhase(r.Context(), observability.PhaseResponse, endpointKind, modelName, isStream)
	responseStatus := resp.StatusCode
	responseOutcome := otelOutcomeForStatus(resp.StatusCode)
	var responseErr error
	defer func() {
		responseSpan.End(observability.SpanResult{
			Outcome:     responseOutcome,
			BackendName: forwardResult.BackendName,
			StatusCode:  responseStatus,
			Error:       responseErr,
		})
	}()
	if isStream {
		requestStatus = resp.StatusCode
		requestOutcome = otelOutcomeForStatus(resp.StatusCode)
		if streamErr := a.handleMessagesStream(w, r, start, body, modelName, pctx, tokenReservation, slot, resp, nativeAnthropic, forwardResult); streamErr != nil {
			responseOutcome = observability.OutcomeError
			responseErr = streamErr
			requestOutcome = observability.OutcomeError
			requestErr = streamErr
		}
		return
	}
	defer slot.Release()
	respBody, readErr := io.ReadAll(resp.Body)
	if readErr != nil {
		tokenReservation.Release()
		responseStatus = http.StatusBadGateway
		responseOutcome = observability.OutcomeError
		responseErr = readErr
		requestStatus = http.StatusBadGateway
		requestErr = readErr
		writeAnthropicError(w, http.StatusBadGateway, readErr.Error(), "api_error", "backend_read_error")
		entry := a.addLog(r, withForwardWarnings(LogEntry{Time: start, Method: r.Method, Path: r.URL.Path, Model: modelName, Status: http.StatusBadGateway, Backend: forwardResult.BackendName, Error: readErr.Error(), DurationMS: time.Since(start).Milliseconds()}, forwardResult))
		payloadPath := a.storePayload(entry, r, body, resp.Header, nil)
		a.persistLog(r.Context(), entry, r, false, pctx, payloadPath, nil)
		return
	}
	if resp.StatusCode >= 400 {
		tokenReservation.Release()
		requestStatus = resp.StatusCode
		requestOutcome = otelOutcomeForStatus(resp.StatusCode)
		writeAnthropicUpstreamError(w, resp.StatusCode, respBody)
		entry := a.addLog(r, withForwardWarnings(LogEntry{Time: start, Method: r.Method, Path: r.URL.Path, Model: modelName, Status: resp.StatusCode, Backend: forwardResult.BackendName, Error: "upstream error", DurationMS: time.Since(start).Milliseconds()}, forwardResult))
		payloadPath := a.storePayload(entry, r, body, resp.Header, respBody)
		a.persistLog(r.Context(), entry, r, false, pctx, payloadPath, respBody)
		return
	}
	if translatedCacheHit {
		usage, _, _ := logdb.ExtractUsageAndOutcome(respBody)
		a.commitTokenUsage(tokenReservation, forwardResult, totalTokensFromUsage(usage))
		a.recordTokenUsageMetric(r.Context(), endpointKind, modelName, false, usage)
		w.Header().Set("Content-Type", firstContentType(resp.Header.Get("Content-Type"), "application/json"))
		w.WriteHeader(resp.StatusCode)
		_, _ = w.Write(respBody)
		requestStatus = resp.StatusCode
		requestOutcome = otelOutcomeForStatus(resp.StatusCode)
		entry := a.addLog(r, withForwardWarnings(LogEntry{Time: start, Method: r.Method, Path: r.URL.Path, Model: modelName, Status: resp.StatusCode, Backend: forwardResult.BackendName, DurationMS: time.Since(start).Milliseconds()}, forwardResult))
		payloadPath := a.storePayload(entry, r, body, resp.Header, respBody)
		a.persistLog(r.Context(), entry, r, false, pctx, payloadPath, respBody)
		return
	}
	if nativeAnthropic {
		usage, _, _ := logdb.ExtractUsageAndOutcome(respBody)
		a.commitTokenUsage(tokenReservation, forwardResult, totalTokensFromUsage(usage))
		a.recordTokenUsageMetric(r.Context(), endpointKind, modelName, false, usage)
		w.Header().Set("Content-Type", firstContentType(resp.Header.Get("Content-Type"), "application/json"))
		w.WriteHeader(resp.StatusCode)
		_, _ = w.Write(respBody)
		requestStatus = resp.StatusCode
		requestOutcome = otelOutcomeForStatus(resp.StatusCode)
		entry := a.addLog(r, withForwardWarnings(LogEntry{Time: start, Method: r.Method, Path: r.URL.Path, Model: modelName, Status: resp.StatusCode, Backend: forwardResult.BackendName, DurationMS: time.Since(start).Milliseconds()}, forwardResult))
		payloadPath := a.storePayload(entry, r, body, resp.Header, respBody)
		a.persistLog(r.Context(), entry, r, false, pctx, payloadPath, respBody)
		return
	}
	openaiResponse := map[string]any{}
	if err := json.Unmarshal(respBody, &openaiResponse); err != nil {
		a.commitTokenUsage(tokenReservation, forwardResult, 0)
		w.Header().Set("Content-Type", firstContentType(resp.Header.Get("Content-Type"), "application/json"))
		w.WriteHeader(resp.StatusCode)
		_, _ = w.Write(respBody)
		requestStatus = resp.StatusCode
		requestOutcome = otelOutcomeForStatus(resp.StatusCode)
		entry := a.addLog(r, withForwardWarnings(LogEntry{Time: start, Method: r.Method, Path: r.URL.Path, Model: modelName, Status: resp.StatusCode, Backend: forwardResult.BackendName, DurationMS: time.Since(start).Milliseconds()}, forwardResult))
		payloadPath := a.storePayload(entry, r, body, resp.Header, respBody)
		a.persistLog(r.Context(), entry, r, false, pctx, payloadPath, respBody)
		return
	}
	anthropicResponse := anthropicmessages.FromChatCompletion(openaiResponse)
	anthropicBody, err := json.Marshal(anthropicResponse)
	if err != nil {
		tokenReservation.Release()
		responseStatus = http.StatusInternalServerError
		responseOutcome = observability.OutcomeError
		responseErr = err
		requestStatus = http.StatusInternalServerError
		requestErr = err
		writeAnthropicError(w, http.StatusInternalServerError, err.Error(), "api_error", "translation_error")
		return
	}
	usage, _, _ := logdb.ExtractUsageAndOutcome(anthropicBody)
	a.commitTokenUsage(tokenReservation, forwardResult, totalTokensFromUsage(usage))
	a.recordTokenUsageMetric(r.Context(), endpointKind, modelName, false, usage)
	a.Router.StoreTranslatedInferenceCache(forwardResult.BackendName, r.URL.Path, r.URL.RawQuery, forwardBody, forwardPayload, isStream, translatedCacheOpts, resp.StatusCode, http.Header{"Content-Type": {"application/json"}}, anthropicBody)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	_, _ = w.Write(anthropicBody)
	requestStatus = resp.StatusCode
	requestOutcome = otelOutcomeForStatus(resp.StatusCode)
	entry := a.addLog(r, withForwardWarnings(LogEntry{Time: start, Method: r.Method, Path: r.URL.Path, Model: modelName, Status: resp.StatusCode, Backend: forwardResult.BackendName, DurationMS: time.Since(start).Milliseconds()}, forwardResult))
	payloadPath := a.storePayload(entry, r, body, resp.Header, anthropicBody)
	a.persistLog(r.Context(), entry, r, false, pctx, payloadPath, anthropicBody)
}

func (a *App) handleMessagesStream(w http.ResponseWriter, r *http.Request, start time.Time, requestBody []byte, modelName string, pctx policy.Context, tokenReservation *quota.Reservation, slot *admission.Slot, resp *http.Response, nativeAnthropic bool, forwardResult proxy.ForwardResult) error {
	copyHeaders(w.Header(), resp.Header)
	if w.Header().Get("Content-Type") == "" {
		w.Header().Set("Content-Type", "text/event-stream")
	}
	backendName := forwardResult.BackendName
	w.WriteHeader(resp.StatusCode)
	if nativeAnthropic {
		usageReader := &anthropicStreamUsageReadCloser{ReadCloser: resp.Body}
		streamReader := &releaseOnEOF{ReadCloser: usageReader, release: func() {
			slot.Release()
			if resp.StatusCode >= 400 {
				tokenReservation.Release()
			} else {
				a.commitTokenUsage(tokenReservation, forwardResult, usageReader.TotalTokens())
			}
		}}
		_, streamErr := copyStreaming(w, streamReader, a.newStreamTimingObserver(r.Context(), observability.EndpointKind(r.URL.Path), modelName, start, resp.StatusCode))
		if streamErr != nil {
			_ = streamReader.Close()
		}
		if resp.StatusCode < 400 && streamErr == nil {
			a.recordTokenUsageMetric(r.Context(), observability.EndpointKind(r.URL.Path), modelName, true, usageReader.UsageStats())
		}
		entry := a.addLog(r, withForwardWarnings(LogEntry{Time: start, Method: r.Method, Path: r.URL.Path, Model: modelName, Status: resp.StatusCode, Backend: backendName, DurationMS: time.Since(start).Milliseconds()}, forwardResult))
		payloadPath := a.storePayload(entry, r, requestBody, resp.Header, nil)
		a.persistLogWithUsage(r.Context(), entry, r, true, pctx, payloadPath, nil, usageReader.UsageStats())
		return streamErr
	}
	stream := anthropicmessages.NewStreamReadCloser(resp.Body, "", modelName)
	streamReader := &releaseOnEOF{ReadCloser: stream, release: func() {
		slot.Release()
		if resp.StatusCode >= 400 {
			tokenReservation.Release()
		} else {
			a.commitTokenUsage(tokenReservation, forwardResult, stream.Adapter.TotalTokens())
		}
	}}
	_, streamErr := copyStreaming(w, streamReader, a.newStreamTimingObserver(r.Context(), observability.EndpointKind(r.URL.Path), modelName, start, resp.StatusCode))
	if streamErr != nil {
		_ = streamReader.Close()
	}
	if resp.StatusCode < 400 && streamErr == nil {
		a.recordTokenUsageMetric(r.Context(), observability.EndpointKind(r.URL.Path), modelName, true, stream.Adapter.UsageStats())
	}
	entry := a.addLog(r, withForwardWarnings(LogEntry{Time: start, Method: r.Method, Path: r.URL.Path, Model: modelName, Status: resp.StatusCode, Backend: backendName, Error: stream.Adapter.StreamError(), DurationMS: time.Since(start).Milliseconds()}, forwardResult))
	payloadPath := a.storePayload(entry, r, requestBody, resp.Header, nil)
	a.persistLogWithUsage(r.Context(), entry, r, true, pctx, payloadPath, nil, stream.Adapter.UsageStats())
	return streamErr
}

func validateMessages(payload map[string]any) error {
	messages, ok := payload["messages"].([]any)
	if !ok || len(messages) == 0 {
		return errors.New("You must provide a messages array")
	}
	return nil
}

func writeAnthropicError(w http.ResponseWriter, status int, message string, errorType string, code string) {
	payload := map[string]any{
		"type": "error",
		"error": map[string]any{
			"type":    errorType,
			"message": message,
			"code":    code,
		},
	}
	writeJSON(w, status, payload)
}

func writeAnthropicUpstreamError(w http.ResponseWriter, status int, body []byte) {
	var upstream map[string]any
	if err := json.Unmarshal(body, &upstream); err == nil {
		if errPayload, ok := upstream["error"].(map[string]any); ok {
			message := strings.TrimSpace(stringValue(errPayload["message"]))
			if message == "" {
				message = "upstream error"
			}
			errorType := strings.TrimSpace(stringValue(errPayload["type"]))
			if errorType == "" {
				errorType = "api_error"
			}
			code := strings.TrimSpace(stringValue(errPayload["code"]))
			if code == "" {
				code = "backend_error"
			}
			writeAnthropicError(w, status, message, errorType, code)
			return
		}
	}
	message := strings.TrimSpace(string(body))
	if message == "" {
		message = "upstream error"
	}
	writeAnthropicError(w, status, message, "api_error", "backend_error")
}

func firstContentType(value string, fallback string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return fallback
	}
	return value
}

func (a *App) directMessagesBackendIsAnthropic(modelName string) bool {
	if a == nil || a.Router == nil {
		return false
	}
	return a.Router.ModelBackendsMatch(modelName, func(backend proxy.Backend) bool {
		return strings.EqualFold(strings.TrimSpace(backend.APIType), "anthropic")
	})
}

type anthropicStreamUsageReadCloser struct {
	io.ReadCloser
	decoder      proxy.SSEJSONDecoder
	inputTokens  int
	outputTokens int
	totalTokens  int
}

func (r *anthropicStreamUsageReadCloser) Read(p []byte) (int, error) {
	n, err := r.ReadCloser.Read(p)
	if n > 0 {
		r.observe(p[:n])
	}
	if err == io.EOF {
		for _, payload := range r.decoder.Flush() {
			r.observePayload(payload)
		}
	}
	return n, err
}

func (r *anthropicStreamUsageReadCloser) TotalTokens() int {
	if r == nil {
		return 0
	}
	if r.totalTokens > 0 {
		return r.totalTokens
	}
	return r.inputTokens + r.outputTokens
}

func (r *anthropicStreamUsageReadCloser) UsageStats() map[string]any {
	if r == nil || r.inputTokens+r.outputTokens+r.totalTokens == 0 {
		return nil
	}
	usage := map[string]any{
		"input_tokens":      r.inputTokens,
		"output_tokens":     r.outputTokens,
		"prompt_tokens":     r.inputTokens,
		"completion_tokens": r.outputTokens,
	}
	if total := r.TotalTokens(); total > 0 {
		usage["total_tokens"] = total
	}
	return usage
}

func (r *anthropicStreamUsageReadCloser) observe(chunk []byte) {
	for _, payload := range r.decoder.Feed(chunk) {
		r.observePayload(payload)
	}
}

func (r *anthropicStreamUsageReadCloser) observePayload(payload map[string]any) {
	if message, _ := payload["message"].(map[string]any); len(message) > 0 {
		if usage, _ := message["usage"].(map[string]any); len(usage) > 0 {
			r.observeUsage(usage)
		}
	}
	if usage, _ := payload["usage"].(map[string]any); len(usage) > 0 {
		r.observeUsage(usage)
	}
}

func (r *anthropicStreamUsageReadCloser) observeUsage(usage map[string]any) {
	if tokens := intFromAny(usage["input_tokens"]); tokens > 0 {
		r.inputTokens = tokens
	}
	if tokens := intFromAny(usage["prompt_tokens"]); tokens > 0 {
		r.inputTokens = tokens
	}
	if tokens := intFromAny(usage["output_tokens"]); tokens > 0 {
		r.outputTokens = tokens
	}
	if tokens := intFromAny(usage["completion_tokens"]); tokens > 0 {
		r.outputTokens = tokens
	}
	if tokens := intFromAny(usage["total_tokens"]); tokens > 0 {
		r.totalTokens = tokens
	}
}
