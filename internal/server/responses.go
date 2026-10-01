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
	"github.com/theio/yallmp-proxy/internal/observability"
	"github.com/theio/yallmp-proxy/internal/policy"
	"github.com/theio/yallmp-proxy/internal/proxy"
	"github.com/theio/yallmp-proxy/internal/quota"
	responsesapi "github.com/theio/yallmp-proxy/internal/responses"
)

func (a *App) handleResponses(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeResponsesError(w, http.StatusBadRequest, "", "invalid_request", "invalid_request_error", "failed to read request body", "")
		return
	}
	payload, err := proxy.DecodeJSONBody(body)
	if err != nil {
		writeResponsesError(w, http.StatusBadRequest, "", "invalid_request", "invalid_json", "Invalid JSON payload", "")
		entry := a.addLog(r, LogEntry{Time: start, Method: r.Method, Path: r.URL.Path, Status: http.StatusBadRequest, Error: err.Error(), DurationMS: time.Since(start).Milliseconds()})
		payloadPath := a.storePayload(entry, r, body, nil, nil)
		a.persistLog(r.Context(), entry, r, false, policy.Context{}, payloadPath, nil)
		return
	}
	responseID := responsesapi.GenerateResponseID()
	modelName := proxy.NormalizeRequestModel(payload["model"])
	if modelName == "" {
		writeResponsesError(w, http.StatusBadRequest, responseID, "invalid_request", "missing_parameter", "You must provide a model parameter", "")
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
	if _, ok := payload["input"]; !ok {
		requestStatus = http.StatusBadRequest
		writeResponsesError(w, http.StatusBadRequest, responseID, "invalid_request", "missing_parameter", "You must provide an input parameter", modelName)
		entry := a.addLog(r, LogEntry{Time: start, Method: r.Method, Path: r.URL.Path, Model: modelName, Status: http.StatusBadRequest, Error: "missing input parameter", DurationMS: time.Since(start).Milliseconds()})
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
		writeResponsesError(w, status, responseID, errorType, code, err.Error(), modelName)
		return
	}
	policySpan.End(observability.SpanResult{Outcome: observability.OutcomeSuccess})
	requestSpan.SetKeyScope(observability.KeyScope(pctx.Authenticated, pctx.KeyID, pctx.GroupID))
	r = requestWithRequiredHeaderAttribution(r, pctx.RequiredHeaders)

	nativeResponses := a.directResponsesBackendSupportsResponses(modelName)
	forwardPath := "/v1/responses"
	forwardPayload := payload
	forwardBody := body
	if !nativeResponses {
		history := []any(nil)
		if previousID, ok := payload["previous_response_id"].(string); ok && previousID != "" && a.Responses != nil {
			history = a.Responses.ConversationHistory(previousID)
		}
		chatPayload, err := responsesapi.ToChatCompletions(payload, history)
		if err != nil {
			requestStatus = http.StatusBadRequest
			requestErr = err
			writeResponsesError(w, http.StatusBadRequest, responseID, "invalid_request", "translation_error", err.Error(), modelName)
			return
		}
		chatBody, err := json.Marshal(chatPayload)
		if err != nil {
			requestStatus = http.StatusBadRequest
			requestErr = err
			writeResponsesError(w, http.StatusBadRequest, responseID, "invalid_request", "translation_error", err.Error(), modelName)
			return
		}
		forwardPath = "/v1/chat/completions"
		forwardPayload = chatPayload
		forwardBody = chatBody
	}

	limits := a.effectiveLimits(modelName, pctx)
	limitKeyID := pctx.LimitKeyID()
	_, admissionSpan := a.startProxyPhase(r.Context(), observability.PhaseAdmission, endpointKind, modelName, isStream)
	if err := a.Admission.CheckRateLimit(limitKeyID, modelName, admission.RateLimits{RPS: limits.RPS, RPM: limits.RPM}); err != nil {
		admissionSpan.End(observability.SpanResult{Outcome: observability.OutcomeError, StatusCode: http.StatusTooManyRequests, Error: err})
		requestStatus = http.StatusTooManyRequests
		requestErr = err
		writeResponsesError(w, http.StatusTooManyRequests, responseID, "rate_limit_error", "rate_limit_exceeded", "Rate limit exceeded", modelName)
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
		writeResponsesError(w, http.StatusTooManyRequests, responseID, "rate_limit_error", "token_quota_exceeded", "Token quota exceeded", modelName)
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
			writeResponsesError(w, http.StatusTooManyRequests, responseID, "rate_limit_error", "concurrency_limit_exceeded", "Request queued too long - concurrency limit reached", modelName)
		} else {
			admissionSpan.End(observability.SpanResult{Outcome: observability.OutcomeError, StatusCode: 499, Error: err})
			requestStatus = 499
			w.WriteHeader(499)
		}
		return
	}
	admissionSpan.End(observability.SpanResult{Outcome: observability.OutcomeSuccess})

	translatedCacheOpts := proxy.ForwardOptions{
		Cacheable:         !nativeResponses,
		CacheBypass:       proxy.InferenceCacheBypassRequested(r.Header),
		CacheScope:        inferenceCacheScope(pctx),
		KeyID:             pctx.KeyID,
		RoutePayload:      payload,
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
			RoutePayload:      payload,
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
			writeResponsesError(w, http.StatusBadRequest, responseID, "invalid_request", "unsupported_endpoint", err.Error(), modelName)
			entry := a.addLog(r, withForwardWarnings(LogEntry{Time: start, Method: r.Method, Path: r.URL.Path, Model: modelName, Status: http.StatusBadRequest, Backend: forwardResult.BackendName, Error: err.Error(), DurationMS: time.Since(start).Milliseconds()}, forwardResult))
			payloadPath := a.storePayload(entry, r, body, nil, nil)
			a.persistLog(r.Context(), entry, r, false, pctx, payloadPath, nil)
			return
		}
		if errors.Is(err, proxy.ErrNodeAccessDenied) {
			requestStatus = http.StatusForbidden
			requestErr = err
			writeResponsesError(w, http.StatusForbidden, responseID, "permission_error", "model_access_denied", err.Error(), modelName)
			entry := a.addLog(r, withForwardWarnings(LogEntry{Time: start, Method: r.Method, Path: r.URL.Path, Model: modelName, Status: http.StatusForbidden, Backend: forwardResult.BackendName, Error: err.Error(), DurationMS: time.Since(start).Milliseconds()}, forwardResult))
			payloadPath := a.storePayload(entry, r, body, nil, nil)
			a.persistLog(r.Context(), entry, r, false, pctx, payloadPath, nil)
			return
		}
		if errors.Is(err, admission.ErrRateLimitExceeded) {
			requestStatus = http.StatusTooManyRequests
			requestErr = err
			writeResponsesError(w, http.StatusTooManyRequests, responseID, "rate_limit_error", "rate_limit_exceeded", "Backend rate limit exceeded", modelName)
			entry := a.addLog(r, withForwardWarnings(LogEntry{Time: start, Method: r.Method, Path: r.URL.Path, Model: modelName, Status: http.StatusTooManyRequests, Backend: forwardResult.BackendName, Error: err.Error(), DurationMS: time.Since(start).Milliseconds()}, forwardResult))
			payloadPath := a.storePayload(entry, r, body, nil, nil)
			a.persistLog(r.Context(), entry, r, false, pctx, payloadPath, nil)
			return
		}
		if errors.Is(err, admission.ErrQueueTimeout) {
			requestStatus = http.StatusTooManyRequests
			requestErr = err
			writeResponsesError(w, http.StatusTooManyRequests, responseID, "rate_limit_error", "concurrency_limit_exceeded", "Backend concurrency queue timed out", modelName)
			entry := a.addLog(r, withForwardWarnings(LogEntry{Time: start, Method: r.Method, Path: r.URL.Path, Model: modelName, Status: http.StatusTooManyRequests, Backend: forwardResult.BackendName, Error: err.Error(), DurationMS: time.Since(start).Milliseconds()}, forwardResult))
			payloadPath := a.storePayload(entry, r, body, nil, nil)
			a.persistLog(r.Context(), entry, r, false, pctx, payloadPath, nil)
			return
		}
		requestStatus = http.StatusBadGateway
		requestErr = err
		writeResponsesError(w, http.StatusBadGateway, responseID, "server_error", "backend_error", err.Error(), modelName)
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
		if streamErr := a.handleResponsesStream(w, r, start, body, modelName, responseID, payload, pctx, tokenReservation, slot, resp, nativeResponses, forwardResult); streamErr != nil {
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
		writeResponsesError(w, http.StatusBadGateway, responseID, "server_error", "backend_read_error", readErr.Error(), modelName)
		entry := a.addLog(r, withForwardWarnings(LogEntry{Time: start, Method: r.Method, Path: r.URL.Path, Model: modelName, Status: http.StatusBadGateway, Backend: forwardResult.BackendName, Error: readErr.Error(), DurationMS: time.Since(start).Milliseconds()}, forwardResult))
		payloadPath := a.storePayload(entry, r, body, resp.Header, nil)
		a.persistLog(r.Context(), entry, r, false, pctx, payloadPath, nil)
		return
	}
	if resp.StatusCode >= 400 {
		tokenReservation.Release()
		requestStatus = resp.StatusCode
		requestOutcome = otelOutcomeForStatus(resp.StatusCode)
		writeResponsesUpstreamError(w, responseID, resp.StatusCode, respBody, modelName)
		entry := a.addLog(r, withForwardWarnings(LogEntry{Time: start, Method: r.Method, Path: r.URL.Path, Model: modelName, Status: resp.StatusCode, Backend: forwardResult.BackendName, Error: "upstream error", DurationMS: time.Since(start).Milliseconds()}, forwardResult))
		payloadPath := a.storePayload(entry, r, body, resp.Header, respBody)
		a.persistLog(r.Context(), entry, r, false, pctx, payloadPath, respBody)
		return
	}
	if translatedCacheHit {
		usage, _, _ := logdb.ExtractUsageAndOutcome(respBody)
		a.commitTokenUsage(tokenReservation, forwardResult, totalTokensFromUsage(usage))
		a.recordTokenUsageMetric(r.Context(), endpointKind, modelName, false, usage)
		a.bindResponseSticky(responseIDFromBody(respBody, ""), forwardResult.BackendName)
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
	if nativeResponses {
		usage, _, _ := logdb.ExtractUsageAndOutcome(respBody)
		a.commitTokenUsage(tokenReservation, forwardResult, totalTokensFromUsage(usage))
		a.recordTokenUsageMetric(r.Context(), endpointKind, modelName, false, usage)
		a.bindResponseSticky(responseIDFromBody(respBody, ""), forwardResult.BackendName)
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
	chatResponse := map[string]any{}
	if err := json.Unmarshal(respBody, &chatResponse); err != nil {
		tokenReservation.Release()
		responseStatus = http.StatusBadGateway
		responseOutcome = observability.OutcomeError
		responseErr = err
		requestStatus = http.StatusBadGateway
		requestErr = err
		writeResponsesError(w, http.StatusBadGateway, responseID, "server_error", "parse_error", "Failed to parse backend response", modelName)
		return
	}
	responsePayload := responsesapi.FromChatCompletion(chatResponse, payload, responseID)
	responseBody, err := json.Marshal(responsePayload)
	if err != nil {
		tokenReservation.Release()
		responseStatus = http.StatusInternalServerError
		responseOutcome = observability.OutcomeError
		responseErr = err
		requestStatus = http.StatusInternalServerError
		requestErr = err
		writeResponsesError(w, http.StatusInternalServerError, responseID, "server_error", "translation_error", err.Error(), modelName)
		return
	}
	if store, _ := payload["store"].(bool); store && a.Responses != nil {
		a.Responses.Store(responsesapi.CloneMap(responsePayload), payload["input"])
	}
	a.bindResponseSticky(responseID, forwardResult.BackendName)
	usage, _, _ := logdb.ExtractUsageAndOutcome(responseBody)
	a.commitTokenUsage(tokenReservation, forwardResult, totalTokensFromUsage(usage))
	a.recordTokenUsageMetric(r.Context(), endpointKind, modelName, false, usage)
	a.Router.StoreTranslatedInferenceCache(forwardResult.BackendName, r.URL.Path, r.URL.RawQuery, forwardBody, forwardPayload, isStream, translatedCacheOpts, http.StatusOK, http.Header{"Content-Type": {"application/json"}}, responseBody)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(responseBody)
	responseStatus = http.StatusOK
	responseOutcome = observability.OutcomeSuccess
	requestStatus = http.StatusOK
	requestOutcome = observability.OutcomeSuccess
	entry := a.addLog(r, withForwardWarnings(LogEntry{Time: start, Method: r.Method, Path: r.URL.Path, Model: modelName, Status: http.StatusOK, Backend: forwardResult.BackendName, DurationMS: time.Since(start).Milliseconds()}, forwardResult))
	payloadPath := a.storePayload(entry, r, body, resp.Header, responseBody)
	a.persistLog(r.Context(), entry, r, false, pctx, payloadPath, responseBody)
}

func (a *App) handleResponsesStream(w http.ResponseWriter, r *http.Request, start time.Time, requestBody []byte, modelName string, responseID string, payload map[string]any, pctx policy.Context, tokenReservation *quota.Reservation, slot *admission.Slot, resp *http.Response, nativeResponses bool, forwardResult proxy.ForwardResult) error {
	copyHeaders(w.Header(), resp.Header)
	if w.Header().Get("Content-Type") == "" {
		w.Header().Set("Content-Type", "text/event-stream")
	}
	backendName := forwardResult.BackendName
	w.WriteHeader(resp.StatusCode)
	if nativeResponses {
		usageReader := &responsesStreamUsageReadCloser{ReadCloser: resp.Body}
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
	stream := responsesapi.NewStreamReadCloser(resp.Body, responseID, modelName, payload)
	streamReader := &releaseOnEOF{ReadCloser: stream, release: func() {
		slot.Release()
		if resp.StatusCode >= 400 {
			tokenReservation.Release()
			return
		}
		a.commitTokenUsage(tokenReservation, forwardResult, stream.Adapter.TotalTokens())
		if store, _ := payload["store"].(bool); store && a.Responses != nil {
			a.Responses.Store(responsesapi.CloneMap(stream.Adapter.FinalResponse()), payload["input"])
		}
		a.bindResponseSticky(responseID, backendName)
	}}
	_, streamErr := copyStreaming(w, streamReader, a.newStreamTimingObserver(r.Context(), observability.EndpointKind(r.URL.Path), modelName, start, resp.StatusCode))
	if streamErr != nil {
		_ = streamReader.Close()
	}
	if resp.StatusCode < 400 && streamErr == nil {
		a.recordTokenUsageMetric(r.Context(), observability.EndpointKind(r.URL.Path), modelName, true, stream.Adapter.UsageStats())
	}
	entry := a.addLog(r, withForwardWarnings(LogEntry{Time: start, Method: r.Method, Path: r.URL.Path, Model: modelName, Status: resp.StatusCode, Backend: backendName, DurationMS: time.Since(start).Milliseconds()}, forwardResult))
	payloadPath := a.storePayload(entry, r, requestBody, resp.Header, nil)
	a.persistLogWithUsage(r.Context(), entry, r, true, pctx, payloadPath, nil, stream.Adapter.UsageStats())
	return streamErr
}

func (a *App) bindResponseSticky(responseID string, backendName string) {
	if a == nil || a.Router == nil || strings.TrimSpace(responseID) == "" {
		return
	}
	a.Router.BindSticky("previous_response_id", responseID, backendName)
}

func responseIDFromBody(body []byte, fallback string) string {
	payload := map[string]any{}
	if err := json.Unmarshal(body, &payload); err != nil {
		return fallback
	}
	if id, ok := payload["id"].(string); ok && strings.TrimSpace(id) != "" {
		return strings.TrimSpace(id)
	}
	return fallback
}

func (a *App) directResponsesBackendSupportsResponses(modelName string) bool {
	if a == nil || a.Router == nil {
		return false
	}
	return a.Router.ModelBackendsMatch(modelName, func(backend proxy.Backend) bool { return backend.SupportsResponsesAPI })
}

func writeResponsesError(w http.ResponseWriter, status int, responseID string, errorType string, code string, message string, model string) {
	writeJSON(w, status, responsesapi.ErrorResponse(responseID, errorType, code, message, model))
}

func writeResponsesUpstreamError(w http.ResponseWriter, responseID string, status int, body []byte, model string) {
	var upstream map[string]any
	if err := json.Unmarshal(body, &upstream); err == nil {
		if errPayload, ok := upstream["error"].(map[string]any); ok {
			message := strings.TrimSpace(stringValue(errPayload["message"]))
			if message == "" {
				message = "upstream error"
			}
			code := strings.TrimSpace(stringValue(errPayload["code"]))
			if code == "" {
				code = "backend_error"
			}
			errorType := strings.TrimSpace(stringValue(errPayload["type"]))
			if errorType == "" {
				errorType = "model_error"
			}
			writeResponsesError(w, status, responseID, errorType, code, message, model)
			return
		}
	}
	message := strings.TrimSpace(string(body))
	if message == "" {
		message = "upstream error"
	}
	writeResponsesError(w, status, responseID, "model_error", "backend_error", message, model)
}

type responsesStreamUsageReadCloser struct {
	io.ReadCloser
	decoder proxy.SSEJSONDecoder
	usage   map[string]any
}

func (r *responsesStreamUsageReadCloser) Read(p []byte) (int, error) {
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

func (r *responsesStreamUsageReadCloser) TotalTokens() int {
	if r == nil || len(r.usage) == 0 {
		return 0
	}
	return totalTokensFromUsage(r.usage)
}

func (r *responsesStreamUsageReadCloser) UsageStats() map[string]any {
	if r == nil || len(r.usage) == 0 {
		return nil
	}
	out := make(map[string]any, len(r.usage))
	for key, value := range r.usage {
		out[key] = value
	}
	return out
}

func (r *responsesStreamUsageReadCloser) observe(chunk []byte) {
	for _, payload := range r.decoder.Feed(chunk) {
		r.observePayload(payload)
	}
}

func (r *responsesStreamUsageReadCloser) observePayload(payload map[string]any) {
	if usage, ok := payload["usage"].(map[string]any); ok && totalTokensFromUsage(usage) > 0 {
		r.usage = usage
		return
	}
	if response, ok := payload["response"].(map[string]any); ok {
		if usage, ok := response["usage"].(map[string]any); ok && totalTokensFromUsage(usage) > 0 {
			r.usage = usage
		}
	}
}
