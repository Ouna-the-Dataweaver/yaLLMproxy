package logstore

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/theio/yallmp-proxy/internal/config"
)

type FullRequestStore struct {
	Enabled   bool
	Dir       string
	Retention time.Duration
	now       func() time.Time
}

type PayloadRecord struct {
	ID                 int64               `json:"id"`
	Timestamp          time.Time           `json:"timestamp"`
	Method             string              `json:"method"`
	Path               string              `json:"path"`
	Model              string              `json:"model,omitempty"`
	Status             int                 `json:"status"`
	RequestHeaders     map[string][]string `json:"request_headers,omitempty"`
	ResponseHeaders    map[string][]string `json:"response_headers,omitempty"`
	RequiredHeaders    map[string]string   `json:"required_headers,omitempty"`
	RequestBodyBase64  string              `json:"request_body_base64,omitempty"`
	ResponseBodyBase64 string              `json:"response_body_base64,omitempty"`
	ParsedResponse     *ParsedResponse     `json:"parsed_response,omitempty"`
	ParsedStream       *ParsedStream       `json:"parsed_stream,omitempty"`
	Error              string              `json:"error,omitempty"`
}

type ParsedResponse struct {
	RawUpstreamResponseBodyBase64       string `json:"raw_upstream_response_body_base64,omitempty"`
	TransformedClientResponseBodyBase64 string `json:"transformed_client_response_body_base64,omitempty"`
	ContentType                         string `json:"content_type,omitempty"`
}

type ParsedResponseBodies struct {
	RawUpstreamBody       []byte
	TransformedClientBody []byte
	ContentType           string
}

type ParsedStream struct {
	RawUpstreamResponsePath            string `json:"raw_upstream_response_path,omitempty"`
	TransformedClientResponsePath      string `json:"transformed_client_response_path,omitempty"`
	RawUpstreamResponseBytes           int64  `json:"raw_upstream_response_bytes,omitempty"`
	TransformedClientResponseBytes     int64  `json:"transformed_client_response_bytes,omitempty"`
	MaxBytes                           int64  `json:"max_bytes,omitempty"`
	Truncated                          bool   `json:"truncated,omitempty"`
	RawUpstreamResponseTruncated       bool   `json:"raw_upstream_response_truncated,omitempty"`
	TransformedClientResponseTruncated bool   `json:"transformed_client_response_truncated,omitempty"`
	ContentType                        string `json:"content_type,omitempty"`
}

type ParsedStreamSidecars struct {
	raw         *cappedSidecarWriter
	transformed *cappedSidecarWriter
	maxBytes    int64
	contentType string
}

func (s *FullRequestStore) NewParsedStreamSidecars(maxBytes int64) (*ParsedStreamSidecars, error) {
	if s == nil || !s.Enabled {
		return nil, nil
	}
	if err := os.MkdirAll(s.Dir, 0o700); err != nil {
		return nil, err
	}
	if maxBytes <= 0 {
		maxBytes = 4 << 20
	}
	prefix := fmt.Sprintf("%s-%s", s.currentTime().UTC().Format("20060102T150405.000000000Z"), randomHex(6))
	return &ParsedStreamSidecars{
		raw:         newCappedSidecarWriter(filepath.Join(s.Dir, safeFileName(prefix+"-raw-upstream.sse")), maxBytes),
		transformed: newCappedSidecarWriter(filepath.Join(s.Dir, safeFileName(prefix+"-transformed-client.sse")), maxBytes),
		maxBytes:    maxBytes,
	}, nil
}

func (c *ParsedStreamSidecars) RawWriter() *cappedSidecarWriter {
	if c == nil {
		return nil
	}
	return c.raw
}

func (c *ParsedStreamSidecars) TransformedWriter() *cappedSidecarWriter {
	if c == nil {
		return nil
	}
	return c.transformed
}

func (c *ParsedStreamSidecars) SetContentType(contentType string) {
	if c != nil {
		c.contentType = contentType
	}
}

func (c *ParsedStreamSidecars) Close() error {
	if c == nil {
		return nil
	}
	rawErr := c.raw.close()
	transformedErr := c.transformed.close()
	if rawErr != nil {
		return rawErr
	}
	return transformedErr
}

func (c *ParsedStreamSidecars) Metadata() *ParsedStream {
	if c == nil {
		return nil
	}
	rawTruncated := c.raw.truncated
	transformedTruncated := c.transformed.truncated
	if c.raw.written == 0 && c.transformed.written == 0 && !rawTruncated && !transformedTruncated {
		return nil
	}
	return &ParsedStream{
		RawUpstreamResponsePath:            c.raw.path,
		TransformedClientResponsePath:      c.transformed.path,
		RawUpstreamResponseBytes:           c.raw.written,
		TransformedClientResponseBytes:     c.transformed.written,
		MaxBytes:                           c.maxBytes,
		Truncated:                          rawTruncated || transformedTruncated,
		RawUpstreamResponseTruncated:       rawTruncated,
		TransformedClientResponseTruncated: transformedTruncated,
		ContentType:                        c.contentType,
	}
}

func (c *ParsedStreamSidecars) RemoveIfEmpty() {
	if c == nil {
		return
	}
	c.raw.removeIfEmpty()
	c.transformed.removeIfEmpty()
}

type cappedSidecarWriter struct {
	path      string
	maxBytes  int64
	file      *os.File
	written   int64
	truncated bool
	disabled  bool
}

func newCappedSidecarWriter(path string, maxBytes int64) *cappedSidecarWriter {
	return &cappedSidecarWriter{path: path, maxBytes: maxBytes}
}

func (w *cappedSidecarWriter) Write(p []byte) (int, error) {
	if w == nil || w.disabled || len(p) == 0 {
		return len(p), nil
	}
	if w.maxBytes > 0 && w.written >= w.maxBytes {
		w.truncated = true
		return len(p), nil
	}
	if w.file == nil {
		file, err := os.OpenFile(w.path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			w.disabled = true
			return len(p), nil
		}
		w.file = file
	}
	toWrite := p
	if w.maxBytes > 0 {
		remaining := w.maxBytes - w.written
		if int64(len(toWrite)) > remaining {
			toWrite = toWrite[:remaining]
			w.truncated = true
		}
	}
	n, err := w.file.Write(toWrite)
	w.written += int64(n)
	if err != nil || n != len(toWrite) {
		w.disabled = true
	}
	return len(p), nil
}

func (w *cappedSidecarWriter) close() error {
	if w == nil || w.file == nil {
		return nil
	}
	err := w.file.Close()
	w.file = nil
	if err != nil {
		w.disabled = true
	}
	return err
}

func (w *cappedSidecarWriter) removeIfEmpty() {
	if w == nil || w.path == "" || w.written > 0 {
		return
	}
	_ = w.close()
	_ = os.Remove(w.path)
}

func NewFullRequestStore(cfg *config.Config) *FullRequestStore {
	if cfg == nil {
		return nil
	}
	settings := cfg.ProxySettings.Logging.FullRequestStore
	enabled := config.ParseBool(settings["enabled"])
	if !enabled {
		return nil
	}
	path := stringSetting(settings, "path", "logs/requests")
	if !filepath.IsAbs(path) {
		path = filepath.Join(config.ProjectRoot(), path)
	}
	retentionHours := floatSetting(settings, "retention_hours", 48)
	store := &FullRequestStore{
		Enabled:   true,
		Dir:       path,
		Retention: time.Duration(retentionHours * float64(time.Hour)),
		now:       time.Now,
	}
	if config.ParseBool(settings["cleanup_on_startup"]) {
		_, _ = store.CleanupExpired()
	}
	return store
}

func (s *FullRequestStore) Store(record PayloadRecord) (string, error) {
	if s == nil || !s.Enabled {
		return "", nil
	}
	if err := os.MkdirAll(s.Dir, 0o700); err != nil {
		return "", err
	}
	if record.Timestamp.IsZero() {
		record.Timestamp = s.currentTime()
	}
	name := fmt.Sprintf("%s-%06d.json", record.Timestamp.UTC().Format("20060102T150405.000000000Z"), record.ID)
	path := filepath.Join(s.Dir, safeFileName(name))
	buf, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(path, buf, 0o600); err != nil {
		return "", err
	}
	return path, nil
}

func (s *FullRequestStore) StorePayload(
	id int64,
	method string,
	path string,
	model string,
	status int,
	requestHeaders http.Header,
	requestBody []byte,
	responseHeaders http.Header,
	responseBody []byte,
	errText string,
	requiredHeaders map[string]string,
) (string, error) {
	return s.StorePayloadWithParsed(
		id,
		method,
		path,
		model,
		status,
		requestHeaders,
		requestBody,
		responseHeaders,
		responseBody,
		errText,
		requiredHeaders,
		nil,
	)
}

func (s *FullRequestStore) StorePayloadWithParsed(
	id int64,
	method string,
	path string,
	model string,
	status int,
	requestHeaders http.Header,
	requestBody []byte,
	responseHeaders http.Header,
	responseBody []byte,
	errText string,
	requiredHeaders map[string]string,
	parsedBodies *ParsedResponseBodies,
) (string, error) {
	return s.StorePayloadWithParsedAndStream(
		id,
		method,
		path,
		model,
		status,
		requestHeaders,
		requestBody,
		responseHeaders,
		responseBody,
		errText,
		requiredHeaders,
		parsedBodies,
		nil,
	)
}

func (s *FullRequestStore) StorePayloadWithParsedAndStream(
	id int64,
	method string,
	path string,
	model string,
	status int,
	requestHeaders http.Header,
	requestBody []byte,
	responseHeaders http.Header,
	responseBody []byte,
	errText string,
	requiredHeaders map[string]string,
	parsedBodies *ParsedResponseBodies,
	parsedStream *ParsedStream,
) (string, error) {
	if s == nil || !s.Enabled {
		return "", nil
	}
	var parsedResponse *ParsedResponse
	if parsedBodies != nil && len(parsedBodies.RawUpstreamBody) > 0 && len(parsedBodies.TransformedClientBody) > 0 {
		parsedResponse = &ParsedResponse{
			RawUpstreamResponseBodyBase64:       base64.StdEncoding.EncodeToString(parsedBodies.RawUpstreamBody),
			TransformedClientResponseBodyBase64: base64.StdEncoding.EncodeToString(parsedBodies.TransformedClientBody),
			ContentType:                         parsedBodies.ContentType,
		}
	}
	return s.Store(PayloadRecord{
		ID:                 id,
		Timestamp:          s.currentTime(),
		Method:             method,
		Path:               path,
		Model:              model,
		Status:             status,
		RequestHeaders:     cloneHeaderRedactingSensitive(requestHeaders),
		ResponseHeaders:    cloneHeaderRedactingSensitive(responseHeaders),
		RequiredHeaders:    cloneStringMap(requiredHeaders),
		RequestBodyBase64:  base64.StdEncoding.EncodeToString(requestBody),
		ResponseBodyBase64: base64.StdEncoding.EncodeToString(responseBody),
		ParsedResponse:     parsedResponse,
		ParsedStream:       parsedStream,
		Error:              errText,
	})
}

func ReadPayload(path string) (PayloadRecord, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return PayloadRecord{}, err
	}
	var record PayloadRecord
	if err := json.Unmarshal(raw, &record); err != nil {
		return PayloadRecord{}, err
	}
	return record, nil
}

func DecodeBody(encoded string) ([]byte, error) {
	if encoded == "" {
		return nil, nil
	}
	return base64.StdEncoding.DecodeString(encoded)
}

func randomHex(bytesLen int) string {
	if bytesLen <= 0 {
		bytesLen = 6
	}
	buf := make([]byte, bytesLen)
	if _, err := rand.Read(buf); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(buf)
}

func (s *FullRequestStore) CleanupExpired() (int, error) {
	if s == nil || !s.Enabled || s.Retention <= 0 {
		return 0, nil
	}
	entries, err := os.ReadDir(s.Dir)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}
	cutoff := s.currentTime().Add(-s.Retention)
	deleted := 0
	for _, entry := range entries {
		if entry.IsDir() || !cleanupManagedPayloadFile(entry.Name()) {
			continue
		}
		path := filepath.Join(s.Dir, entry.Name())
		info, err := entry.Info()
		if err != nil {
			continue
		}
		if info.ModTime().Before(cutoff) {
			if err := os.Remove(path); err != nil {
				return deleted, err
			}
			deleted++
		}
	}
	return deleted, nil
}

func cleanupManagedPayloadFile(name string) bool {
	return strings.HasSuffix(name, ".json") ||
		strings.HasSuffix(name, "-raw-upstream.sse") ||
		strings.HasSuffix(name, "-transformed-client.sse")
}

func (s *FullRequestStore) currentTime() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now()
}

func stringSetting(settings map[string]any, key string, fallback string) string {
	if value := strings.TrimSpace(fmt.Sprint(settings[key])); value != "" && value != "<nil>" {
		return value
	}
	return fallback
}

func floatSetting(settings map[string]any, key string, fallback float64) float64 {
	switch typed := settings[key].(type) {
	case int:
		return float64(typed)
	case int64:
		return float64(typed)
	case float64:
		return typed
	case string:
		var out float64
		if _, err := fmt.Sscanf(typed, "%f", &out); err == nil {
			return out
		}
	}
	return fallback
}

func safeFileName(name string) string {
	name = filepath.Base(name)
	name = strings.ReplaceAll(name, string(filepath.Separator), "_")
	return name
}

func cloneHeaderRedactingSensitive(header http.Header) map[string][]string {
	if header == nil {
		return nil
	}
	out := make(map[string][]string, len(header))
	for key, values := range header {
		if isSensitiveHeader(key) {
			out[key] = []string{"[REDACTED]"}
			continue
		}
		copied := make([]string, len(values))
		copy(copied, values)
		out[key] = copied
	}
	return out
}

func cloneStringMap(values map[string]string) map[string]string {
	if len(values) == 0 {
		return nil
	}
	out := make(map[string]string, len(values))
	for key, value := range values {
		out[key] = value
	}
	return out
}

func isSensitiveHeader(key string) bool {
	normalized := strings.ToLower(strings.ReplaceAll(strings.TrimSpace(key), "-", "_"))
	switch {
	case normalized == "authorization":
		return true
	case normalized == "proxy_authorization":
		return true
	case normalized == "x_api_key":
		return true
	case normalized == "api_key":
		return true
	case normalized == "cookie":
		return true
	case normalized == "set_cookie":
		return true
	case strings.Contains(normalized, "token"):
		return true
	case strings.Contains(normalized, "secret"):
		return true
	default:
		return false
	}
}
