package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/theio/yallmp-proxy/internal/admission"
	"github.com/theio/yallmp-proxy/internal/config"
	"github.com/theio/yallmp-proxy/internal/logdb"
	"github.com/theio/yallmp-proxy/internal/observability"
	"github.com/theio/yallmp-proxy/internal/proxy"
)

// One connection owns one backend. Failover is safe only before upgrading:
// audio and provisional results cannot be replayed transparently mid-session.
func (a *App) handleASRStream(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	model := proxy.NormalizeRequestModel(r.URL.Query().Get("model"))
	if model == "" {
		writeError(w, 400, "provide a model query parameter", "missing_parameter")
		return
	}
	if !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
		writeError(w, 400, "a WebSocket upgrade is required", "invalid_request_error")
		return
	}
	if r.URL.Path == "/v1/realtime" && r.URL.Query().Get("intent") != "" && r.URL.Query().Get("intent") != "transcription" {
		writeError(w, 400, "only transcription realtime sessions are supported", "invalid_request_error")
		return
	}
	// Browsers cannot set arbitrary upgrade headers. Consume proxy credentials
	// locally; never forward or echo a credential-bearing subprotocol.
	r = r.Clone(r.Context())
	headerName := a.Config.AppKeys.HeaderName
	if headerName == "" {
		headerName = config.DefaultAppKeyHeader
	}
	consumedCredentialHeader := ""
	for _, protocol := range websocketProtocols(r) {
		for _, prefix := range []string{"yallmp-key.", "openai-insecure-api-key."} {
			if strings.HasPrefix(protocol, prefix) && r.Header.Get(headerName) == "" {
				credential := strings.TrimPrefix(protocol, prefix)
				if strings.EqualFold(headerName, "Authorization") {
					credential = "Bearer " + credential
				}
				r.Header.Set(headerName, credential)
				consumedCredentialHeader = headerName
			}
		}
	}
	pctx, err := a.Policy.ValidateRequest(r, model)
	if err != nil {
		status, _, code := a.policyErrorDetails(err)
		writeError(w, status, err.Error(), code)
		return
	}
	r = requestWithRequiredHeaderAttribution(r, pctx.RequiredHeaders)
	ctx, cancel := context.WithTimeout(r.Context(), a.Config.ProxySettings.ASR.SessionTimeout())
	defer cancel()
	kind := observability.EndpointKind(r.URL.Path)
	var span observability.Span
	if a.otelEnabled() {
		ctx, span = a.Observability.StartRequestSpan(ctx, observability.RequestAttrs{EndpointKind: kind, Model: a.traceModelName(model), KeyScope: observability.KeyScope(pctx.Authenticated, pctx.KeyID, pctx.GroupID), Stream: true})
		ctx = contextWithProxyRequestTrace(ctx, span)
	}
	r = r.WithContext(ctx)
	status, backendName := http.StatusBadGateway, ""
	var requestErr error
	var usage map[string]any
	var attempts []proxy.UpstreamAttempt
	defer func() {
		outcome := otelOutcomeForStatus(status)
		if requestErr != nil {
			outcome = observability.OutcomeError
		}
		a.recordRequestMetric(ctx, kind, model, true, outcome, status, requestErr, time.Since(start))
		span.End(observability.SpanResult{Outcome: outcome, StatusCode: status, BackendName: backendName, Error: requestErr})
		entry := a.addLog(r, LogEntry{Time: start, Method: r.Method, Path: r.URL.Path, Model: model, Status: status, Backend: backendName, Error: streamErrorMessage(requestErr), DurationMS: time.Since(start).Milliseconds(), AttemptTrail: attempts})
		a.persistLogWithUsage(ctx, entry, r, true, pctx, "", nil, usage)
	}()
	fail := func(code int, message, errorCode string, err error) {
		status, requestErr = code, err
		writeError(w, code, message, errorCode)
	}
	limits := a.effectiveLimits(model, pctx)
	if err := a.Admission.CheckRateLimit(pctx.LimitKeyID(), model, admission.RateLimits{RPS: limits.RPS, RPM: limits.RPM}); err != nil {
		fail(429, "Rate limit exceeded", "rate_limit_exceeded", err)
		return
	}
	reservation, err := a.Quota.ReserveWithWindowSpec(pctx.LimitKeyID(), model, limits.TokenQuota, quotaWindowSpec(limits), 1)
	if err != nil {
		fail(429, "Token quota exceeded", "token_quota_exceeded", err)
		return
	}
	defer reservation.Release()
	slot, err := a.acquireSlot(ctx, model, pctx, limits)
	if err != nil {
		fail(429, "Concurrency limit reached", "concurrency_limit_exceeded", err)
		return
	}
	defer slot.Release()
	route, err := a.Router.BuildRouteForKeyWithOptions(model, r.URL.Path, map[string]any{}, pctx.KeyID, proxy.RouteOptions{BackendAvailable: a.backendRateLimitAvailable, QuotaAvailability: a.backendQuotaAvailability(1)})
	if err != nil {
		requestErr = err
		if code, ok := a.writeBackendForwardError(w, err); ok {
			status = code
		} else {
			fail(502, "No ASR backend available", "backend_error", err)
		}
		return
	}
	var upstream *websocket.Conn
	var backend proxy.Backend
	var release func()
	lastStatus := http.StatusBadGateway
	for _, candidate := range route {
		attemptStart := time.Now()
		releaseLimiter, err := a.backendAttemptLimiter(ctx, candidate)
		if err != nil {
			requestErr = err
			continue
		}
		releaseInflight := a.Router.TrackBackendAttempt(candidate.Name)
		once := &sync.Once{}
		done := func() {
			once.Do(func() {
				releaseInflight()
				if releaseLimiter != nil {
					releaseLimiter()
				}
			})
		}
		target, err := asrStreamURL(candidate, r)
		if err != nil {
			done()
			requestErr = err
			continue
		}
		outbound := a.outboundHeaders(r)
		if consumedCredentialHeader != "" {
			// Subprotocol credentials belong to this proxy even when app-key
			// enforcement is disabled; they must never become upstream headers.
			outbound = outbound.Clone()
			outbound.Del(consumedCredentialHeader)
		}
		headers := proxy.BuildOutboundHeaders(outbound, candidate.APIKey, false, candidate.APIType, candidate.AnthropicVersion)
		for key := range headers {
			if strings.HasPrefix(strings.ToLower(key), "sec-websocket-") {
				headers.Del(key)
			}
		}
		headers.Del("Cookie")
		headers.Del("Origin")
		client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
		if a.Router.Client != nil {
			*client = *a.Router.Client
			client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
		}
		client.Timeout = 0
		dialCtx, stopDial := context.WithTimeout(ctx, candidate.TimeoutOrDefault())
		protocols := []string(nil)
		if r.URL.Path == "/v1/realtime" {
			protocols = []string{"realtime"}
		}
		conn, resp, err := websocket.Dial(dialCtx, target, &websocket.DialOptions{HTTPClient: client, HTTPHeader: headers, Subprotocols: protocols})
		stopDial()
		attemptStatus := 0
		if resp != nil {
			attemptStatus = resp.StatusCode
		}
		a.recordPassthroughBackendAttemptMetric(kind, model, candidate.Name, attemptStatus, err, time.Since(attemptStart), true)
		a.Router.ObserveBackendResult(candidate.Name, attemptStatus, err)
		attemptOutcome := "final"
		if err != nil {
			attemptOutcome = "error"
		}
		attempts = append(attempts, proxy.UpstreamAttempt{AttemptIndex: len(attempts) + 1, BackendName: candidate.Name, Status: attemptStatus, Retryable: err != nil && (attemptStatus == 0 || proxy.RetryableStatuses[attemptStatus]), Outcome: attemptOutcome, DurationMS: time.Since(attemptStart).Milliseconds()})
		if err != nil {
			done()
			requestErr = err
			if attemptStatus >= 400 {
				lastStatus = attemptStatus
			}
			if resp != nil && !proxy.RetryableStatuses[resp.StatusCode] {
				break
			}
			continue
		}
		upstream, backend, backendName, release = conn, candidate, candidate.Name, done
		break
	}
	if upstream == nil {
		if code, ok := a.writeBackendForwardError(w, requestErr); ok {
			status = code
			return
		}
		fail(lastStatus, "Could not connect to ASR backend", "backend_error", errors.New("ASR upstream handshake failed"))
		return
	}
	defer release()
	defer upstream.CloseNow()
	protocols := []string(nil)
	if upstream.Subprotocol() != "" {
		protocols = []string{upstream.Subprotocol()}
	}
	downstream, err := websocket.Accept(w, r, &websocket.AcceptOptions{Subprotocols: protocols, OriginPatterns: a.Config.ProxySettings.ASR.AllowedOrigins})
	if err != nil {
		status, requestErr = 400, err
		return
	}
	defer downstream.CloseNow()
	status, requestErr = http.StatusSwitchingProtocols, nil
	upstream.SetReadLimit(a.Config.ProxySettings.ASR.MessageLimit())
	downstream.SetReadLimit(a.Config.ProxySettings.ASR.MessageLimit())
	bridgeCtx, stopBridge := context.WithCancel(ctx)
	defer stopBridge()
	type pumpResult struct {
		err      error
		upstream bool
	}
	errorsCh := make(chan pumpResult, 2)
	go func() {
		for {
			typ, data, err := downstream.Read(bridgeCtx)
			if err != nil {
				errorsCh <- pumpResult{err: err}
				return
			}
			if typ == websocket.MessageText {
				data, err = rewriteASRModel(data, model, backend.TargetModel)
				if err != nil {
					errorsCh <- pumpResult{err: err}
					return
				}
			}
			if err := upstream.Write(bridgeCtx, typ, data); err != nil {
				errorsCh <- pumpResult{err: err}
				return
			}
		}
	}()
	// Only this goroutine writes usage; wait for both pumps before reading it.
	go func() {
		for {
			typ, data, err := upstream.Read(bridgeCtx)
			if err != nil {
				errorsCh <- pumpResult{err: err, upstream: true}
				return
			}
			if typ == websocket.MessageText {
				var event map[string]any
				if json.Unmarshal(data, &event) == nil {
					if current, _, _ := logdb.ExtractUsageAndOutcome(data); len(current) > 0 {
						usage = addASRUsage(usage, current)
					}
					if event["type"] == "error" {
						requestErr = errors.New("ASR upstream reported a session error")
					}
				}
			}
			if err := downstream.Write(bridgeCtx, typ, data); err != nil {
				errorsCh <- pumpResult{err: err, upstream: true}
				return
			}
		}
	}()
	first := <-errorsCh
	closeCode := websocket.CloseStatus(first.err)
	if closeCode < 1000 || closeCode == websocket.StatusAbnormalClosure || closeCode == websocket.StatusNoStatusRcvd || closeCode == websocket.StatusTLSHandshake {
		closeCode = websocket.StatusInternalError
	}
	// Preserve the peer's close status, including normal finalization. Closing
	// the other peer also wakes its read pump; cancellation bounds all cleanup.
	if first.upstream {
		_ = downstream.Close(closeCode, "")
	} else {
		_ = upstream.Close(closeCode, "")
	}
	stopBridge()
	_ = upstream.CloseNow()
	// Terminate both reads before returning the runtime generation or its slots.
	_ = downstream.CloseNow()
	<-errorsCh
	if closeCode != websocket.StatusNormalClosure && closeCode != websocket.StatusGoingAway {
		requestErr = errors.New("ASR connection interrupted")
		if ctx.Err() != nil {
			requestErr = ctx.Err()
		}
	}
	result := proxy.ForwardResult{BackendName: backend.Name, AttemptTrail: []proxy.UpstreamAttempt{{BackendName: backend.Name, Status: 101}}}
	a.commitTokenUsage(reservation, result, totalTokensFromUsage(usage))
	a.recordTokenUsageMetric(ctx, kind, model, true, usage)
}

func websocketProtocols(r *http.Request) []string {
	var protocols []string
	for _, line := range r.Header.Values("Sec-WebSocket-Protocol") {
		for _, value := range strings.Split(line, ",") {
			protocols = append(protocols, strings.TrimSpace(value))
		}
	}
	return protocols
}

func asrStreamURL(backend proxy.Backend, r *http.Request) (string, error) {
	u, err := url.Parse(backend.BuildURL(r.URL.Path, ""))
	if err != nil {
		return "", err
	}
	if backend.RealtimePath != "" {
		if err := config.ValidateRealtimePath(backend.RealtimePath); err != nil {
			return "", err
		}
		u.Path, u.RawPath = backend.RealtimePath, ""
	} else if r.URL.Path == "/ws/voice" {
		u.Path, u.RawPath = "/ws/voice", ""
	}
	switch u.Scheme {
	case "http":
		u.Scheme = "ws"
	case "https":
		u.Scheme = "wss"
	default:
		return "", fmt.Errorf("invalid ASR upstream URL")
	}
	query := r.URL.Query()
	if backend.TargetModel != "" {
		query.Set("model", backend.TargetModel)
	}
	if r.URL.Path == "/v1/realtime" {
		// This query is proxy routing metadata. OpenAI interprets an upstream
		// model query as a conversation session, even for an ASR model.
		query.Del("model")
		query.Set("intent", "transcription")
	}
	u.RawQuery = query.Encode()
	return u.String(), nil
}

func rewriteASRModel(data []byte, publicModel, upstreamModel string) ([]byte, error) {
	var event map[string]any
	if json.Unmarshal(data, &event) != nil {
		return data, nil
	}
	changed := false
	if event["type"] == "session.update" || event["type"] == "transcription_session.update" {
		if session, ok := event["session"].(map[string]any); ok {
			if typ, exists := session["type"]; exists && typ != "transcription" {
				return nil, fmt.Errorf("only transcription sessions are supported")
			}
			if upstreamModel != "" {
				if legacy, ok := session["input_audio_transcription"].(map[string]any); ok {
					if _, exists := legacy["model"]; !exists {
						legacy["model"], changed = upstreamModel, true
					}
				} else {
					audio, ok := session["audio"].(map[string]any)
					if !ok {
						audio = map[string]any{}
						session["audio"] = audio
					}
					input, ok := audio["input"].(map[string]any)
					if !ok {
						input = map[string]any{}
						audio["input"] = input
					}
					transcription, ok := input["transcription"].(map[string]any)
					if !ok {
						transcription = map[string]any{}
						input["transcription"] = transcription
					}
					if _, exists := transcription["model"]; !exists {
						transcription["model"], changed = upstreamModel, true
					}
				}
			}
		}
	}
	var visit func(map[string]any) error
	visit = func(object map[string]any) error {
		for key, value := range object {
			if key == "model" {
				if value != publicModel && value != upstreamModel {
					return fmt.Errorf("cannot change the model of an ASR session")
				}
				if upstreamModel != "" && value != upstreamModel {
					object[key], changed = upstreamModel, true
				}
			} else if child, ok := value.(map[string]any); ok {
				if err := visit(child); err != nil {
					return err
				}
			}
		}
		return nil
	}
	if err := visit(event); err != nil {
		return nil, err
	}
	if !changed {
		return data, nil
	}
	return json.Marshal(event)
}

func addASRUsage(total, current map[string]any) map[string]any {
	if total == nil {
		total = map[string]any{}
	}
	for _, key := range []string{"input_tokens", "output_tokens", "prompt_tokens", "completion_tokens", "total_tokens"} {
		if value, exists := current[key]; exists {
			total[key] = intFromAny(total[key]) + intFromAny(value)
		}
	}
	if current["type"] == "duration" {
		total["type"] = "duration"
		total["seconds"] = numericSeconds(total["seconds"]) + numericSeconds(current["seconds"])
	}
	return total
}

func numericSeconds(value any) float64 {
	var n float64
	switch v := value.(type) {
	case float64:
		n = v
	case int:
		n = float64(v)
	case int64:
		n = float64(v)
	case json.Number:
		n, _ = v.Float64()
	}
	if math.IsNaN(n) || math.IsInf(n, 0) || n < 0 {
		return 0
	}
	return n
}
