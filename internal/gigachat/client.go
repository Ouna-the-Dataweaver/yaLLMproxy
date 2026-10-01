package gigachat

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

const toolEmulationAttempts = 3

type UpstreamError struct {
	StatusCode int
	Body       string
}

func (e UpstreamError) Error() string {
	return fmt.Sprintf("upstream returned status %d: %s", e.StatusCode, e.Body)
}

type Backend struct {
	Config Config

	mu                   sync.Mutex
	accessToken          string
	accessTokenExpiresAt time.Time
	tokenRefresh         chan struct{}
	defaultClient        *http.Client
	defaultClientErr     error
}

func NewBackend(cfg Config) *Backend {
	return &Backend{Config: cfg}
}

func (b *Backend) Execute(ctx context.Context, payload map[string]any, isStream bool, overrideClient *http.Client) (*http.Response, error) {
	resp, err := b.execute(ctx, payload, isStream, overrideClient)
	if err == nil {
		return resp, nil
	}
	var upstreamErr UpstreamError
	if errors.As(err, &upstreamErr) {
		if isStream {
			return streamErrorResponse(upstreamErr.StatusCode, upstreamErr.Body), nil
		}
		return errorResponse(upstreamErr.StatusCode, upstreamErr.Body), nil
	}
	return nil, err
}

func (b *Backend) Probe(ctx context.Context, path string, overrideClient *http.Client) (*http.Response, error) {
	if strings.TrimSpace(path) == "" {
		path = "/models"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, joinURL(b.Config.BaseURL, path), nil)
	if err != nil {
		return nil, err
	}
	headers, err := b.headers(ctx, overrideClient)
	if err != nil {
		return nil, err
	}
	for key, values := range headers {
		for _, value := range values {
			req.Header.Add(key, value)
		}
	}
	req.Header.Set("Accept", "application/json")
	client, err := b.client(overrideClient)
	if err != nil {
		return nil, err
	}
	return client.Do(req)
}

func (b *Backend) execute(ctx context.Context, payload map[string]any, isStream bool, overrideClient *http.Client) (*http.Response, error) {
	requestModel := firstNonEmpty(stringOrEmpty(payload["model"]), b.Config.ModelName)
	if shouldEmulateTools(b.Config, payload) {
		response, err := b.chatCompletionsWithToolEmulation(ctx, payload, requestModel, overrideClient)
		if err != nil {
			return nil, err
		}
		if isStream {
			chunks := OpenAIResponseToSSE(response)
			return responseFromBytes(http.StatusOK, http.Header{
				"Content-Type":  {"text/event-stream"},
				"Cache-Control": {"no-cache"},
			}, bytes.Join(chunks, nil)), nil
		}
		body, _ := json.Marshal(response)
		return responseFromBytes(http.StatusOK, http.Header{"Content-Type": {"application/json"}}, body), nil
	}
	gigachatPayload, err := OpenAIChatToGigaChat(payload, b.Config.ModelName)
	if err != nil {
		return nil, err
	}
	if isStream {
		gigachatPayload["stream"] = true
		resp, err := b.postJSON(ctx, "/chat/completions", gigachatPayload, true, overrideClient)
		if err != nil {
			return nil, err
		}
		resp.Body = newStreamReadCloser(resp.Body, requestModel)
		resp.Header = filterHeaders(resp.Header)
		resp.Header.Set("Content-Type", "text/event-stream")
		resp.Header.Set("Cache-Control", "no-cache")
		return resp, nil
	}
	resp, err := b.postJSON(ctx, "/chat/completions", gigachatPayload, false, overrideClient)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var data map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return nil, err
	}
	openAIResponse := GigaChatResponseToOpenAI(data, requestModel)
	body, _ := json.Marshal(openAIResponse)
	return responseFromBytes(http.StatusOK, http.Header{"Content-Type": {"application/json"}}, body), nil
}

func (b *Backend) chatCompletionsWithToolEmulation(ctx context.Context, payload map[string]any, requestModel string, overrideClient *http.Client) (map[string]any, error) {
	var retryError string
	var lastErr error
	for attempt := 0; attempt < toolEmulationAttempts; attempt++ {
		gigachatPayload, err := OpenAIChatToGigaChatToolEmulation(payload, b.Config.ModelName, retryError)
		if err != nil {
			return nil, err
		}
		resp, err := b.postJSON(ctx, "/chat/completions", gigachatPayload, false, overrideClient)
		if err != nil {
			return nil, err
		}
		var data map[string]any
		decodeErr := json.NewDecoder(resp.Body).Decode(&data)
		_ = resp.Body.Close()
		if decodeErr != nil {
			lastErr = decodeErr
			retryError = decodeErr.Error()
			continue
		}
		content, err := extractFirstMessageContent(data)
		if err != nil {
			lastErr = err
			retryError = err.Error()
			continue
		}
		result, err := ParseToolEmulationResponse(content, payload["tools"])
		if err != nil {
			lastErr = err
			retryError = err.Error()
			continue
		}
		return ToolEmulationResultToOpenAIResponse(result, requestModel, data["usage"]), nil
	}
	return nil, UpstreamError{StatusCode: http.StatusBadGateway, Body: fmt.Sprintf("GigaChat tool emulation failed after %d attempts: %v", toolEmulationAttempts, lastErr)}
}

func (b *Backend) postJSON(ctx context.Context, path string, payload map[string]any, isStream bool, overrideClient *http.Client) (*http.Response, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, joinURL(b.Config.BaseURL, path), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	headers, err := b.headers(ctx, overrideClient)
	if err != nil {
		return nil, err
	}
	for key, values := range headers {
		for _, value := range values {
			req.Header.Add(key, value)
		}
	}
	req.Header.Set("Content-Type", "application/json")
	if isStream {
		req.Header.Set("Accept", "text/event-stream")
	} else {
		req.Header.Set("Accept", "application/json")
	}
	client, err := b.client(overrideClient)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 400 {
		defer resp.Body.Close()
		respBody, _ := io.ReadAll(resp.Body)
		return nil, UpstreamError{StatusCode: resp.StatusCode, Body: string(respBody)}
	}
	return resp, nil
}

func (b *Backend) headers(ctx context.Context, overrideClient *http.Client) (http.Header, error) {
	out := http.Header{}
	if b.Config.Mode != "cloud" {
		return out, nil
	}
	token, err := b.accessTokenForCloud(ctx, overrideClient)
	if err != nil {
		return nil, err
	}
	out.Set("Authorization", "Bearer "+token)
	return out, nil
}

func (b *Backend) accessTokenForCloud(ctx context.Context, overrideClient *http.Client) (string, error) {
	for {
		b.mu.Lock()
		if b.accessToken != "" && time.Now().Before(b.accessTokenExpiresAt.Add(-30*time.Second)) {
			token := b.accessToken
			b.mu.Unlock()
			return token, nil
		}
		if refresh := b.tokenRefresh; refresh != nil {
			b.mu.Unlock()
			select {
			case <-refresh:
				continue
			case <-ctx.Done():
				return "", ctx.Err()
			}
		}
		b.tokenRefresh = make(chan struct{})
		b.mu.Unlock()

		token, expiresAt, err := b.fetchCloudAccessToken(ctx, overrideClient)
		b.mu.Lock()
		if err == nil {
			b.accessToken = token
			b.accessTokenExpiresAt = expiresAt
		}
		close(b.tokenRefresh)
		b.tokenRefresh = nil
		b.mu.Unlock()
		return token, err
	}
}

func (b *Backend) fetchCloudAccessToken(ctx context.Context, overrideClient *http.Client) (string, time.Time, error) {
	if b.Config.APIKey == "" {
		return "", time.Time{}, fmt.Errorf("api_key is required for GigaChat cloud mode")
	}
	form := url.Values{}
	form.Set("scope", b.Config.Scope)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, b.Config.AuthURL, strings.NewReader(form.Encode()))
	if err != nil {
		return "", time.Time{}, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("RqUID", uuid.NewString())
	req.Header.Set("Authorization", "Basic "+b.Config.APIKey)
	client, err := b.client(overrideClient)
	if err != nil {
		return "", time.Time{}, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", time.Time{}, err
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", time.Time{}, err
	}
	if resp.StatusCode >= 400 {
		return "", time.Time{}, UpstreamError{StatusCode: resp.StatusCode, Body: string(respBody)}
	}
	var data map[string]any
	if err := json.Unmarshal(respBody, &data); err != nil {
		return "", time.Time{}, err
	}
	token := stringOrEmpty(data["access_token"])
	if token == "" {
		return "", time.Time{}, fmt.Errorf("GigaChat auth response does not contain access_token")
	}
	expiresAt := normalizeExpiresAt(data["expires_at"])
	return token, expiresAt, nil
}

func (b *Backend) client(override *http.Client) (*http.Client, error) {
	if override != nil {
		return override, nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.defaultClient != nil || b.defaultClientErr != nil {
		return b.defaultClient, b.defaultClientErr
	}
	b.defaultClient, b.defaultClientErr = makeHTTPClient(b.Config)
	return b.defaultClient, b.defaultClientErr
}

func makeHTTPClient(cfg Config) (*http.Client, error) {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	if !cfg.VerifySSL || cfg.ClientCertFile != "" || cfg.ClientKeyFile != "" {
		tlsConfig := &tls.Config{InsecureSkipVerify: !cfg.VerifySSL} //nolint:gosec
		if cfg.ClientCertFile != "" || cfg.ClientKeyFile != "" {
			cert, err := tls.LoadX509KeyPair(cfg.ClientCertFile, cfg.ClientKeyFile)
			if err != nil {
				return nil, err
			}
			tlsConfig.Certificates = []tls.Certificate{cert}
		}
		transport.TLSClientConfig = tlsConfig
	}
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	return &http.Client{Transport: transport, Timeout: timeout}, nil
}

func shouldEmulateTools(cfg Config, payload map[string]any) bool {
	if !cfg.EmulateToolCalls || payload["tool_choice"] == "none" {
		return false
	}
	tools, ok := asSlice(payload["tools"])
	return ok && len(tools) > 0
}

func extractFirstMessageContent(data map[string]any) (string, error) {
	choices, ok := asSlice(data["choices"])
	if !ok || len(choices) == 0 {
		return "", fmt.Errorf("GigaChat response does not contain choices")
	}
	first, ok := choices[0].(map[string]any)
	if !ok {
		return "", fmt.Errorf("GigaChat response choice must be an object")
	}
	message, ok := first["message"].(map[string]any)
	if !ok {
		return "", fmt.Errorf("GigaChat response choice does not contain message")
	}
	content, ok := message["content"].(string)
	if !ok {
		return "", fmt.Errorf("GigaChat response message content must be a string")
	}
	return content, nil
}

func normalizeExpiresAt(value any) time.Time {
	switch typed := value.(type) {
	case int:
		return normalizeUnixTimestamp(float64(typed))
	case int64:
		return normalizeUnixTimestamp(float64(typed))
	case float64:
		return normalizeUnixTimestamp(typed)
	case json.Number:
		if f, err := typed.Float64(); err == nil {
			return normalizeUnixTimestamp(f)
		}
	}
	return time.Now().Add(25 * time.Minute)
}

func normalizeUnixTimestamp(timestamp float64) time.Time {
	if timestamp > 10_000_000_000 {
		timestamp /= 1000
	}
	return time.Unix(int64(timestamp), 0)
}

func joinURL(base string, path string) string {
	return strings.TrimRight(base, "/") + "/" + strings.TrimLeft(path, "/")
}

func responseFromBytes(status int, headers http.Header, body []byte) *http.Response {
	if headers == nil {
		headers = http.Header{}
	}
	return &http.Response{
		StatusCode:    status,
		Status:        fmt.Sprintf("%d %s", status, http.StatusText(status)),
		Header:        headers,
		Body:          io.NopCloser(bytes.NewReader(body)),
		ContentLength: int64(len(body)),
	}
}

func errorResponse(status int, body string) *http.Response {
	payload := map[string]any{
		"error": map[string]any{
			"message": body,
			"type":    "upstream_error",
			"code":    status,
		},
	}
	data, _ := json.Marshal(payload)
	return responseFromBytes(status, http.Header{"Content-Type": {"application/json"}}, data)
}

func streamErrorResponse(status int, body string) *http.Response {
	payload := map[string]any{
		"error": map[string]any{
			"message": body,
			"type":    "upstream_error",
			"code":    status,
		},
	}
	data, _ := json.Marshal(payload)
	streamBody := append(append([]byte("data: "), data...), []byte("\n\ndata: [DONE]\n\n")...)
	return responseFromBytes(status, http.Header{
		"Content-Type":  {"text/event-stream"},
		"Cache-Control": {"no-cache"},
	}, streamBody)
}

func filterHeaders(headers http.Header) http.Header {
	out := http.Header{}
	for key, values := range headers {
		lower := strings.ToLower(key)
		if lower == "content-length" || lower == "transfer-encoding" || lower == "content-encoding" {
			continue
		}
		for _, value := range values {
			out.Add(key, value)
		}
	}
	return out
}
