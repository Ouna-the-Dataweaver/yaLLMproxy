package proxy

import (
	"bytes"
	"compress/flate"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/theio/yallmp-proxy/internal/config"
	"github.com/theio/yallmp-proxy/internal/gigachat"
)

const (
	DefaultTimeout    = 60 * time.Second
	DefaultRetryDelay = 250 * time.Millisecond
	MaxRetryDelay     = 2 * time.Second
)

var RetryableStatuses = map[int]bool{
	http.StatusRequestTimeout:      true,
	http.StatusConflict:            true,
	http.StatusTooManyRequests:     true,
	http.StatusInternalServerError: true,
	http.StatusBadGateway:          true,
	http.StatusServiceUnavailable:  true,
	http.StatusGatewayTimeout:      true,
}

type ParameterConfig struct {
	Default       any
	AllowOverride bool
}

type RequestWarning struct {
	Code     string         `json:"code"`
	Message  string         `json:"message"`
	Path     string         `json:"path,omitempty"`
	Severity string         `json:"severity"`
	Details  map[string]any `json:"details,omitempty"`
}

type BackendBodyResult struct {
	Body     []byte
	Warnings []RequestWarning
}

type Backend struct {
	RateLimitScope       string
	Name                 string
	BaseURL              string
	APIKey               string
	Timeout              time.Duration
	TargetModel          string
	APIType              string
	AnthropicVersion     string
	SupportsReasoning    bool
	SupportsResponsesAPI bool
	HTTP2                bool
	Editable             bool
	Passthrough          bool
	Limits               config.LimitsConfig
	Endpoints            []string
	AllowedKeys          any
	Parameters           map[string]ParameterConfig
	Modules              map[string]any
	GigaChat             *gigachat.Backend
}

func (b Backend) BuildURL(path string, rawQuery string) string {
	base := strings.TrimRight(b.BaseURL, "/")
	normalized := path
	if normalized == "" {
		normalized = "/"
	}
	if !strings.HasPrefix(normalized, "/") {
		normalized = "/" + normalized
	}
	if strings.HasPrefix(normalized, "/v1") {
		normalized = strings.TrimPrefix(normalized, "/v1")
		if normalized == "" {
			normalized = "/"
		}
	}
	out := base + normalized
	if rawQuery != "" {
		sep := "?"
		if strings.Contains(out, "?") {
			sep = "&"
		}
		out += sep + rawQuery
	}
	return out
}

func (b Backend) TimeoutOrDefault() time.Duration {
	if b.Timeout > 0 {
		return b.Timeout
	}
	return DefaultTimeout
}

var hopByHopHeaders = map[string]bool{
	"connection":          true,
	"keep-alive":          true,
	"proxy-authenticate":  true,
	"proxy-authorization": true,
	"te":                  true,
	"trailers":            true,
	"transfer-encoding":   true,
	"upgrade":             true,
}

func BuildOutboundHeaders(incoming http.Header, backendAPIKey string, isStream bool, apiType string, anthropicVersion string) http.Header {
	out := http.Header{}
	seen := map[string]bool{}
	normalizedAPIType := strings.ToLower(strings.TrimSpace(apiType))
	if normalizedAPIType == "" {
		normalizedAPIType = "openai"
	}
	for key, values := range incoming {
		lower := strings.ToLower(key)
		// Accept-Encoding is not forwarded: the proxy reads, logs, caches, and
		// translates bodies, and response filtering drops Content-Encoding. The
		// Go transport negotiates gzip itself and decodes it transparently.
		if hopByHopHeaders[lower] || lower == "authorization" || lower == "host" || lower == "content-length" || lower == "accept-encoding" || strings.HasPrefix(lower, "x-yallmp-") {
			continue
		}
		if normalizedAPIType == "anthropic" && lower == "x-api-key" {
			continue
		}
		if seen[lower] {
			continue
		}
		for _, value := range values {
			out.Add(key, strings.TrimSpace(value))
		}
		seen[lower] = true
	}
	if !seen["content-type"] {
		contentType := incoming.Get("Content-Type")
		if contentType == "" {
			contentType = "application/json"
		}
		out.Set("Content-Type", contentType)
		seen["content-type"] = true
	}
	if isStream {
		out.Set("Accept", "text/event-stream")
		out.Set("Accept-Encoding", "identity")
		seen["accept"] = true
		seen["accept-encoding"] = true
	}
	if backendAPIKey != "" {
		if normalizedAPIType == "anthropic" {
			out.Set("x-api-key", backendAPIKey)
		} else {
			out.Set("Authorization", "Bearer "+backendAPIKey)
		}
	}
	if normalizedAPIType == "anthropic" && !seen["anthropic-version"] && anthropicVersion != "" {
		out.Set("anthropic-version", anthropicVersion)
	}
	return out
}

func BackendFromModelEntry(entry config.ModelEntry) (Backend, bool, error) {
	name := strings.TrimSpace(entry.ModelName)
	if name == "" {
		return Backend{}, false, nil
	}
	params := entry.ModelParams
	if params == nil {
		params = map[string]any{}
	}
	base, _ := params["api_base"].(string)
	if strings.TrimSpace(base) == "" {
		return Backend{}, false, nil
	}
	apiType := ExtractAPIType(params)
	backend := Backend{
		Name:                 name,
		BaseURL:              strings.TrimSpace(base),
		APIKey:               stringValue(params["api_key"]),
		Timeout:              ParseTimeout(params["request_timeout"]),
		TargetModel:          ExtractTargetModel(params, apiType),
		APIType:              apiType,
		AnthropicVersion:     stringValue(params["anthropic_version"]),
		SupportsReasoning:    config.ParseBool(params["supports_reasoning"]),
		SupportsResponsesAPI: config.ParseBool(params["supports_responses_api"]),
		HTTP2:                config.ParseBool(params["http2"]),
		Editable:             config.ParseBool(entry.Editable),
		Passthrough:          apiType == "passthrough",
		Endpoints:            endpointsFromValues(entry.Raw["endpoints"], params["endpoints"]),
		Parameters:           parseParameterConfigs(firstMap(entry.Parameters, params["parameters"])),
		Modules:              entry.Modules,
	}
	if apiType == "gigachat" {
		gigachatConfig, err := gigachat.BuildConfig(params)
		if err != nil {
			return Backend{}, false, err
		}
		backend.GigaChat = gigachat.NewBackend(gigachatConfig)
		backend.APIKey = gigachatConfig.APIKey
		backend.TargetModel = firstString(backend.TargetModel, gigachatConfig.ModelName)
	}
	return backend, true, nil
}

func BuildBackendBody(payload map[string]any, backend Backend, original []byte, isStream bool) []byte {
	return BuildBackendBodyWithWarnings(payload, backend, original, isStream).Body
}

func BuildBackendBodyWithWarnings(payload map[string]any, backend Backend, original []byte, isStream bool) BackendBodyResult {
	updated := clonePayloadMap(payload)
	changed := false
	warnings := make([]RequestWarning, 0)
	if isStream && updated["stream"] != true {
		updated["stream"] = true
		changed = true
	}
	if backend.TargetModel != "" {
		if updated["model"] != backend.TargetModel {
			updated["model"] = backend.TargetModel
			changed = true
		}
	}
	for name, param := range backend.Parameters {
		if isStreamOnlyParameter(name) && !isStream {
			continue
		}
		if isToolOnlyParameter(name) && !hasToolDefinitions(updated) {
			warnings = append(warnings, RequestWarning{
				Code:     "tool_parameter_without_tools",
				Severity: "warning",
				Path:     name,
				Message:  fmt.Sprintf("Dropped backend parameter %q because the request has no tools", name),
				Details:  map[string]any{"parameter": name},
			})
			continue
		}
		if param.AllowOverride {
			if _, ok := getPath(updated, name); !ok {
				setPath(updated, name, param.Default)
				changed = true
			}
			continue
		}
		current, exists := getPath(updated, name)
		setPath(updated, name, param.Default)
		if !exists || !valuesEqual(current, param.Default) {
			changed = true
		}
	}
	sanitizedWarnings := sanitizeOutboundRequest(updated)
	if len(sanitizedWarnings) > 0 {
		warnings = append(warnings, sanitizedWarnings...)
		changed = true
	}
	if !changed {
		return BackendBodyResult{Body: original, Warnings: warnings}
	}
	buf, err := json.Marshal(updated)
	if err != nil {
		return BackendBodyResult{Body: original, Warnings: warnings}
	}
	return BackendBodyResult{Body: buf, Warnings: warnings}
}

func isStreamOnlyParameter(name string) bool {
	normalized := strings.TrimSpace(name)
	return normalized == "stream_options" || strings.HasPrefix(normalized, "stream_options.")
}

func isToolOnlyParameter(name string) bool {
	normalized := strings.TrimSpace(name)
	return normalized == "tool_choice" || strings.HasPrefix(normalized, "tool_choice.") || normalized == "parallel_tool_calls"
}

func sanitizeOutboundRequest(payload map[string]any) []RequestWarning {
	warnings := make([]RequestWarning, 0)
	if !hasToolDefinitions(payload) {
		for _, key := range []string{"tool_choice", "parallel_tool_calls"} {
			if _, ok := payload[key]; ok {
				delete(payload, key)
				warnings = append(warnings, RequestWarning{
					Code:     "tool_field_without_tools",
					Severity: "warning",
					Path:     key,
					Message:  fmt.Sprintf("Dropped request field %q because the request has no tools", key),
					Details:  map[string]any{"field": key},
				})
			}
		}
	}
	warnings = append(warnings, sanitizeMessageToolCalls(payload["messages"])...)
	return warnings
}

func hasToolDefinitions(payload map[string]any) bool {
	if lenAnySlice(payload["tools"]) > 0 {
		return true
	}
	return lenAnySlice(payload["functions"]) > 0
}

func lenAnySlice(value any) int {
	switch typed := value.(type) {
	case []any:
		return len(typed)
	case []map[string]any:
		return len(typed)
	default:
		return 0
	}
}

func sanitizeMessageToolCalls(value any) []RequestWarning {
	messages, ok := value.([]any)
	if !ok {
		return nil
	}
	warnings := make([]RequestWarning, 0)
	for _, rawMessage := range messages {
		message, ok := rawMessage.(map[string]any)
		if !ok {
			continue
		}
		calls, ok := message["tool_calls"].([]any)
		if !ok {
			continue
		}
		for _, rawCall := range calls {
			call, ok := rawCall.(map[string]any)
			if !ok {
				continue
			}
			function, ok := call["function"].(map[string]any)
			if !ok {
				continue
			}
			normalized, ok := normalizeToolArguments(function["arguments"])
			if !ok {
				continue
			}
			current, isString := function["arguments"].(string)
			if !isString || current != normalized {
				name, _ := function["name"].(string)
				function["arguments"] = normalized
				warnings = append(warnings, RequestWarning{
					Code:     "invalid_tool_call_arguments",
					Severity: "warning",
					Path:     "messages.tool_calls.function.arguments",
					Message:  "Normalized invalid replayed tool call arguments before forwarding to upstream",
					Details:  map[string]any{"tool_name": name},
				})
			}
		}
	}
	return warnings
}

func normalizeToolArguments(value any) (string, bool) {
	switch typed := value.(type) {
	case string:
		if strings.TrimSpace(typed) == "" || !json.Valid([]byte(typed)) {
			return "{}", true
		}
		return typed, true
	case nil:
		return "{}", true
	default:
		raw, err := json.Marshal(typed)
		if err != nil || !json.Valid(raw) {
			return "{}", true
		}
		return string(raw), true
	}
}

func valuesEqual(a any, b any) bool {
	rawA, errA := json.Marshal(a)
	rawB, errB := json.Marshal(b)
	return errA == nil && errB == nil && bytes.Equal(rawA, rawB)
}

func clonePayloadMap(value map[string]any) map[string]any {
	out := make(map[string]any, len(value))
	for key, item := range value {
		out[key] = clonePayloadValue(item)
	}
	return out
}

func clonePayloadValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		return clonePayloadMap(typed)
	case []any:
		out := make([]any, len(typed))
		for i, item := range typed {
			out[i] = clonePayloadValue(item)
		}
		return out
	default:
		return typed
	}
}

// DecodeResponseBody replaces a gzip or deflate encoded upstream body with its
// decoded stream. The transport already decodes gzip it negotiated itself; this
// covers upstreams that compress without being asked, so downstream parsing and
// the Content-Encoding-free response headers always see identity bytes.
func DecodeResponseBody(resp *http.Response) error {
	if resp == nil || resp.Body == nil {
		return nil
	}
	encoding := strings.ToLower(strings.TrimSpace(resp.Header.Get("Content-Encoding")))
	var decoded io.ReadCloser
	switch encoding {
	case "gzip", "x-gzip":
		reader, err := gzip.NewReader(resp.Body)
		if err != nil {
			if err == io.EOF {
				// An empty body has nothing to decode.
				resp.Header.Del("Content-Encoding")
				return nil
			}
			_ = resp.Body.Close()
			return fmt.Errorf("decode gzip response: %w", err)
		}
		decoded = reader
	case "deflate":
		decoded = flate.NewReader(resp.Body)
	default:
		return nil
	}
	resp.Body = &decodedBody{Reader: decoded, decoder: decoded, raw: resp.Body}
	resp.Header.Del("Content-Encoding")
	resp.Header.Del("Content-Length")
	resp.ContentLength = -1
	resp.Uncompressed = true
	return nil
}

type decodedBody struct {
	io.Reader
	decoder io.Closer
	raw     io.Closer
}

func (b *decodedBody) Close() error {
	_ = b.decoder.Close()
	return b.raw.Close()
}

func FilterResponseHeaders(headers http.Header) http.Header {
	out := http.Header{}
	for key, values := range headers {
		lower := strings.ToLower(key)
		if hopByHopHeaders[lower] || lower == "content-length" || lower == "transfer-encoding" || lower == "content-encoding" {
			continue
		}
		for _, value := range values {
			out.Add(key, value)
		}
	}
	return out
}

func NormalizeRequestModel(model any) string {
	text, ok := model.(string)
	if !ok {
		return ""
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}
	if strings.Contains(text, "/") {
		prefix, rest, _ := strings.Cut(text, "/")
		if strings.EqualFold(prefix, "openai") && rest != "" {
			return rest
		}
	}
	return text
}

func ExtractAPIType(params map[string]any) string {
	raw, _ := params["api_type"].(string)
	raw = strings.ToLower(strings.TrimSpace(raw))
	if raw == "" {
		return "openai"
	}
	return raw
}

func ExtractTargetModel(params map[string]any, apiType string) string {
	for _, key := range []string{"target_model", "forward_model"} {
		if raw, ok := params[key].(string); ok && strings.TrimSpace(raw) != "" {
			return strings.TrimSpace(raw)
		}
	}
	raw, _ := params["model"].(string)
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	normalizedAPIType := strings.ToLower(strings.TrimSpace(apiType))
	if normalizedAPIType == "" {
		normalizedAPIType = ExtractAPIType(params)
	}
	for _, prefix := range []string{normalizedAPIType + "/", "openai/"} {
		if strings.HasPrefix(strings.ToLower(raw), prefix) {
			return raw[len(prefix):]
		}
	}
	return raw
}

func ParseTimeout(value any) time.Duration {
	switch typed := value.(type) {
	case int:
		return time.Duration(typed) * time.Second
	case int64:
		return time.Duration(typed) * time.Second
	case float64:
		return time.Duration(typed * float64(time.Second))
	case string:
		if typed == "" {
			return 0
		}
		if dur, err := time.ParseDuration(typed); err == nil {
			return dur
		}
		var seconds float64
		if _, err := fmt.Sscanf(typed, "%f", &seconds); err == nil {
			return time.Duration(seconds * float64(time.Second))
		}
	}
	return 0
}

func getPath(root map[string]any, path string) (any, bool) {
	if value, ok := root[path]; ok {
		return value, true
	}
	parts := strings.Split(path, ".")
	current := any(root)
	for _, part := range parts {
		m, ok := current.(map[string]any)
		if !ok {
			return nil, false
		}
		current, ok = m[part]
		if !ok {
			return nil, false
		}
	}
	return current, true
}

func setPath(root map[string]any, path string, value any) {
	parts := strings.Split(path, ".")
	if len(parts) == 1 {
		root[path] = value
		return
	}
	current := root
	for _, part := range parts[:len(parts)-1] {
		next, ok := current[part].(map[string]any)
		if !ok {
			next = map[string]any{}
			current[part] = next
		}
		current = next
	}
	current[parts[len(parts)-1]] = value
}

func DecodeJSONBody(body []byte) (map[string]any, error) {
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	var payload any
	if err := dec.Decode(&payload); err != nil {
		return nil, err
	}
	m, ok := normalizeJSON(payload).(map[string]any)
	if !ok {
		return nil, fmt.Errorf("request body must be a JSON object")
	}
	return m, nil
}

func normalizeJSON(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(typed))
		for k, v := range typed {
			out[k] = normalizeJSON(v)
		}
		return out
	case []any:
		out := make([]any, len(typed))
		for i, v := range typed {
			out[i] = normalizeJSON(v)
		}
		return out
	case json.Number:
		if i, err := typed.Int64(); err == nil {
			return i
		}
		if f, err := typed.Float64(); err == nil {
			return f
		}
		return typed.String()
	default:
		return typed
	}
}

func JoinURL(base string, elems ...string) string {
	u, err := url.Parse(base)
	if err != nil {
		return base
	}
	p := strings.TrimRight(u.Path, "/")
	for _, elem := range elems {
		if elem == "" {
			continue
		}
		p += "/" + strings.Trim(elem, "/")
	}
	u.Path = p
	return u.String()
}
