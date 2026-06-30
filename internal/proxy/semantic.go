package proxy

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/theio/yallmp-proxy/internal/config"
)

const (
	defaultSemanticCacheSimilarityThreshold = 0.95
	defaultSemanticCacheTopK                = 5
	defaultSemanticCacheMaxEntries          = 256
	defaultSemanticCacheMaxEntryBytes       = 1 << 20
)

type SemanticEmbeddingClient interface {
	Embed(ctx context.Context, input string) ([]float64, error)
}

type HTTPSemanticEmbeddingClient struct {
	backend Backend
	model   string
	client  *http.Client
}

func NewHTTPSemanticEmbeddingClient(backend Backend, model string, client *http.Client) *HTTPSemanticEmbeddingClient {
	if client == nil {
		client = &http.Client{Timeout: backend.TimeoutOrDefault()}
	}
	return &HTTPSemanticEmbeddingClient{
		backend: backend,
		model:   strings.TrimSpace(model),
		client:  client,
	}
}

func (c *HTTPSemanticEmbeddingClient) Embed(ctx context.Context, input string) ([]float64, error) {
	if c == nil || c.client == nil {
		return nil, errors.New("semantic embedding client is not configured")
	}
	model := strings.TrimSpace(c.model)
	if model == "" {
		return nil, errors.New("semantic embedding model is not configured")
	}
	payload, err := json.Marshal(map[string]any{
		"model": model,
		"input": input,
	})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.backend.BuildURL("/v1/embeddings", ""), bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header = BuildOutboundHeaders(http.Header{}, c.backend.APIKey, false, c.backend.APIType, c.backend.AnthropicVersion)
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	respBody, readErr := io.ReadAll(io.LimitReader(resp.Body, defaultSemanticCacheMaxEntryBytes))
	if readErr != nil {
		return nil, readErr
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("embedding backend returned status %d", resp.StatusCode)
	}
	var decoded struct {
		Data []struct {
			Embedding []float64 `json:"embedding"`
		} `json:"data"`
	}
	if err := json.Unmarshal(respBody, &decoded); err != nil {
		return nil, err
	}
	if len(decoded.Data) == 0 || len(decoded.Data[0].Embedding) == 0 {
		return nil, errors.New("embedding backend returned no embedding")
	}
	return append([]float64(nil), decoded.Data[0].Embedding...), nil
}

type SemanticCacheRequest struct {
	Path        string
	RawQuery    string
	BackendName string
	Scope       string
	ModelName   string
	Body        []byte
}

type SemanticCacheSnapshot struct {
	Enabled           bool    `json:"enabled"`
	Mode              string  `json:"mode"`
	Hits              uint64  `json:"hits"`
	Misses            uint64  `json:"misses"`
	Writes            uint64  `json:"writes"`
	Evictions         uint64  `json:"evictions"`
	EmbeddingErrors   uint64  `json:"embedding_errors"`
	ThresholdRejected uint64  `json:"threshold_rejected"`
	Entries           int     `json:"entries"`
	TotalEntryBytes   int64   `json:"total_entry_bytes"`
	MaxEntries        int     `json:"max_entries"`
	MaxEntryBytes     int     `json:"max_entry_bytes"`
	TopK              int     `json:"top_k"`
	Similarity        float64 `json:"similarity_threshold"`
}

type SemanticCache struct {
	mu                  sync.Mutex
	mode                inferenceCacheMode
	threshold           float64
	topK                int
	maxEntries          int
	maxEntryBytes       int
	embeddingModel      string
	now                 func() time.Time
	embedder            SemanticEmbeddingClient
	entries             map[string]*semanticCacheEntry
	order               []string
	hits                atomic.Uint64
	misses              atomic.Uint64
	writes              atomic.Uint64
	evictions           atomic.Uint64
	embeddingErrors     atomic.Uint64
	thresholdRejections atomic.Uint64
}

type semanticCacheEntry struct {
	key                string
	path               string
	rawQuery           string
	backendName        string
	scope              string
	modelName          string
	controlFingerprint string
	bodyFingerprint    string
	embeddingModel     string
	embedding          []float64
	status             int
	header             http.Header
	body               []byte
	createdAt          time.Time
}

type semanticPreparedRequest struct {
	text               string
	path               string
	rawQuery           string
	backendName        string
	scope              string
	modelName          string
	controlFingerprint string
	bodyFingerprint    string
	embeddingModel     string
	key                string
}

type semanticSearchResult struct {
	key        string
	score      float64
	status     int
	header     http.Header
	body       []byte
	createdAt  time.Time
	entryBytes int
}

func newSemanticCache(cfg config.InferenceCacheConfig, embedder SemanticEmbeddingClient, now func() time.Time) *SemanticCache {
	mode := normalizeInferenceCacheMode(cfg.Mode)
	if mode == inferenceCacheOff || !config.ParseBoolStrict(cfg.Semantic.Enabled) {
		return nil
	}
	if now == nil {
		now = time.Now
	}
	threshold := cfg.Semantic.SimilarityThreshold
	if threshold <= 0 {
		threshold = defaultSemanticCacheSimilarityThreshold
	}
	topK := cfg.Semantic.TopK
	if topK <= 0 {
		topK = defaultSemanticCacheTopK
	}
	maxEntries := cfg.Semantic.MaxEntries
	if maxEntries <= 0 {
		maxEntries = defaultSemanticCacheMaxEntries
	}
	maxEntryBytes := cfg.Semantic.MaxEntryBytes
	if maxEntryBytes <= 0 {
		maxEntryBytes = defaultSemanticCacheMaxEntryBytes
	}
	return &SemanticCache{
		mode:           mode,
		threshold:      threshold,
		topK:           topK,
		maxEntries:     maxEntries,
		maxEntryBytes:  maxEntryBytes,
		embeddingModel: strings.TrimSpace(cfg.Semantic.EmbeddingModel),
		now:            now,
		embedder:       embedder,
		entries:        map[string]*semanticCacheEntry{},
	}
}

func SemanticCacheSnapshotForConfig(cfg config.InferenceCacheConfig, cache *SemanticCache) SemanticCacheSnapshot {
	if cache != nil {
		return cache.Snapshot()
	}
	topK := cfg.Semantic.TopK
	if topK <= 0 {
		topK = defaultSemanticCacheTopK
	}
	maxEntries := cfg.Semantic.MaxEntries
	if maxEntries <= 0 {
		maxEntries = defaultSemanticCacheMaxEntries
	}
	maxEntryBytes := cfg.Semantic.MaxEntryBytes
	if maxEntryBytes <= 0 {
		maxEntryBytes = defaultSemanticCacheMaxEntryBytes
	}
	threshold := cfg.Semantic.SimilarityThreshold
	if threshold <= 0 {
		threshold = defaultSemanticCacheSimilarityThreshold
	}
	return SemanticCacheSnapshot{
		Enabled:       false,
		Mode:          string(normalizeInferenceCacheMode(cfg.Mode)),
		MaxEntries:    maxEntries,
		MaxEntryBytes: maxEntryBytes,
		TopK:          topK,
		Similarity:    threshold,
	}
}

func (c *InferenceCache) ConfigureSemantic(cfg config.InferenceCacheConfig, embedder SemanticEmbeddingClient) {
	if c == nil {
		return
	}
	c.semantic = newSemanticCache(cfg, embedder, time.Now)
}

func (c *InferenceCache) SemanticCache() *SemanticCache {
	if c == nil {
		return nil
	}
	return c.semantic
}

func (c *InferenceCache) LookupSemantic(ctx context.Context, req SemanticCacheRequest) (*http.Response, bool) {
	if c == nil || c.semantic == nil {
		return nil, false
	}
	return c.semantic.Lookup(ctx, req)
}

func (c *InferenceCache) StoreSemantic(ctx context.Context, req SemanticCacheRequest, status int, header http.Header, body []byte) bool {
	if c == nil || c.semantic == nil {
		return false
	}
	return c.semantic.Store(ctx, req, status, header, body)
}

func (c *SemanticCache) CanRead() bool {
	return c != nil && (c.mode == inferenceCacheReadWrite || c.mode == inferenceCacheReadOnly)
}

func (c *SemanticCache) CanWrite() bool {
	return c != nil && (c.mode == inferenceCacheReadWrite || c.mode == inferenceCacheWriteOnly)
}

func (c *SemanticCache) Lookup(ctx context.Context, req SemanticCacheRequest) (*http.Response, bool) {
	if !c.CanRead() {
		return nil, false
	}
	prepared, ok := c.prepare(req)
	if !ok {
		return nil, false
	}
	embedding, err := c.embed(ctx, prepared.text)
	if err != nil {
		c.embeddingErrors.Add(1)
		c.misses.Add(1)
		return nil, false
	}
	results := c.search(prepared, embedding)
	if len(results) == 0 {
		c.misses.Add(1)
		return nil, false
	}
	best := results[0]
	if best.score < c.threshold {
		c.thresholdRejections.Add(1)
		c.misses.Add(1)
		return nil, false
	}
	resp, ok := c.responseForSearchResult(best.key)
	if !ok {
		c.misses.Add(1)
		return nil, false
	}
	c.hits.Add(1)
	return resp, true
}

func (c *SemanticCache) Store(ctx context.Context, req SemanticCacheRequest, status int, header http.Header, body []byte) bool {
	if !c.CanWrite() || status != http.StatusOK || len(body) > c.maxEntryBytes {
		return false
	}
	if !inferenceCacheableResponse(&http.Response{StatusCode: status, Header: header}) {
		return false
	}
	prepared, ok := c.prepare(req)
	if !ok {
		return false
	}
	embedding, err := c.embed(ctx, prepared.text)
	if err != nil {
		c.embeddingErrors.Add(1)
		return false
	}
	entry := &semanticCacheEntry{
		key:                prepared.key,
		path:               prepared.path,
		rawQuery:           prepared.rawQuery,
		backendName:        prepared.backendName,
		scope:              prepared.scope,
		modelName:          prepared.modelName,
		controlFingerprint: prepared.controlFingerprint,
		bodyFingerprint:    prepared.bodyFingerprint,
		embeddingModel:     prepared.embeddingModel,
		embedding:          append([]float64(nil), embedding...),
		status:             status,
		header:             cloneHeader(header),
		body:               append([]byte(nil), body...),
		createdAt:          c.now(),
	}
	c.add(entry)
	return true
}

func (c *SemanticCache) Add(ctx context.Context, req SemanticCacheRequest, status int, header http.Header, body []byte) bool {
	return c.Store(ctx, req, status, header, body)
}

func (c *SemanticCache) Remove(req SemanticCacheRequest) bool {
	if c == nil {
		return false
	}
	prepared, ok := c.prepare(req)
	if !ok {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.entries[prepared.key]; !ok {
		return false
	}
	c.deleteLocked(prepared.key)
	return true
}

func (c *SemanticCache) Snapshot() SemanticCacheSnapshot {
	if c == nil {
		return SemanticCacheSnapshotForConfig(config.InferenceCacheConfig{}, nil)
	}
	c.mu.Lock()
	entries := len(c.entries)
	totalEntryBytes := int64(0)
	for _, entry := range c.entries {
		if entry != nil {
			totalEntryBytes += int64(len(entry.body))
		}
	}
	mode := c.mode
	maxEntries := c.maxEntries
	maxEntryBytes := c.maxEntryBytes
	topK := c.topK
	threshold := c.threshold
	c.mu.Unlock()
	return SemanticCacheSnapshot{
		Enabled:           true,
		Mode:              string(mode),
		Hits:              c.hits.Load(),
		Misses:            c.misses.Load(),
		Writes:            c.writes.Load(),
		Evictions:         c.evictions.Load(),
		EmbeddingErrors:   c.embeddingErrors.Load(),
		ThresholdRejected: c.thresholdRejections.Load(),
		Entries:           entries,
		TotalEntryBytes:   totalEntryBytes,
		MaxEntries:        maxEntries,
		MaxEntryBytes:     maxEntryBytes,
		TopK:              topK,
		Similarity:        threshold,
	}
}

func (c *SemanticCache) Close() error {
	return nil
}

func (c *SemanticCache) add(entry *semanticCacheEntry) {
	if c == nil || entry == nil || entry.key == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if existing := c.entries[entry.key]; existing != nil {
		existing.embedding = append(existing.embedding[:0], entry.embedding...)
		existing.status = entry.status
		existing.header = cloneHeader(entry.header)
		existing.body = append(existing.body[:0], entry.body...)
		existing.createdAt = entry.createdAt
		c.touchLocked(entry.key)
		c.writes.Add(1)
		return
	}
	c.entries[entry.key] = entry
	c.order = append(c.order, entry.key)
	for len(c.entries) > c.maxEntries && len(c.order) > 0 {
		c.deleteLocked(c.order[0])
		c.evictions.Add(1)
	}
	c.writes.Add(1)
}

func (c *SemanticCache) search(prepared semanticPreparedRequest, embedding []float64) []semanticSearchResult {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.pruneExpiredLocked()
	results := make([]semanticSearchResult, 0, min(len(c.entries), c.topK))
	for _, entry := range c.entries {
		if !entry.matches(prepared) {
			continue
		}
		score, ok := cosineSimilarity(embedding, entry.embedding)
		if !ok {
			continue
		}
		results = append(results, semanticSearchResult{
			key:        entry.key,
			score:      score,
			status:     entry.status,
			header:     cloneHeader(entry.header),
			body:       append([]byte(nil), entry.body...),
			createdAt:  entry.createdAt,
			entryBytes: len(entry.body),
		})
	}
	sort.SliceStable(results, func(i, j int) bool {
		if results[i].score == results[j].score {
			return results[i].createdAt.After(results[j].createdAt)
		}
		return results[i].score > results[j].score
	})
	if c.topK > 0 && len(results) > c.topK {
		results = results[:c.topK]
	}
	return results
}

func (c *SemanticCache) responseForSearchResult(key string) (*http.Response, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry := c.entries[key]
	if entry == nil {
		return nil, false
	}
	c.touchLocked(key)
	return responseFromSemanticCacheEntry(entry), true
}

func (c *SemanticCache) embed(ctx context.Context, text string) ([]float64, error) {
	if c == nil || c.embedder == nil {
		return nil, errors.New("semantic embedding client is not configured")
	}
	embedding, err := c.embedder.Embed(ctx, text)
	if err != nil {
		return nil, err
	}
	if len(embedding) == 0 {
		return nil, errors.New("semantic embedding is empty")
	}
	return embedding, nil
}

func (c *SemanticCache) prepare(req SemanticCacheRequest) (semanticPreparedRequest, bool) {
	if c == nil || normalizeEndpoint(req.Path) != "chat" {
		return semanticPreparedRequest{}, false
	}
	canonical, ok := canonicalJSONBody(req.Body)
	if !ok || len(canonical) == 0 {
		return semanticPreparedRequest{}, false
	}
	var payload any
	if err := json.Unmarshal(canonical, &payload); err != nil {
		return semanticPreparedRequest{}, false
	}
	if !semanticPayloadHasMessages(payload) {
		return semanticPreparedRequest{}, false
	}
	control, err := json.Marshal(normalizeJSON(semanticControlValue(payload)))
	if err != nil {
		return semanticPreparedRequest{}, false
	}
	scope := strings.TrimSpace(req.Scope)
	if scope == "" {
		scope = "unauthenticated"
	}
	prepared := semanticPreparedRequest{
		text:               string(canonical),
		path:               normalizeEndpoint(req.Path),
		rawQuery:           strings.TrimSpace(req.RawQuery),
		backendName:        strings.TrimSpace(req.BackendName),
		scope:              scope,
		modelName:          strings.TrimSpace(req.ModelName),
		controlFingerprint: semanticHash(control),
		bodyFingerprint:    semanticHash(canonical),
		embeddingModel:     strings.TrimSpace(c.embeddingModel),
	}
	if prepared.backendName == "" || prepared.modelName == "" || prepared.embeddingModel == "" {
		return semanticPreparedRequest{}, false
	}
	prepared.key = semanticCacheKey(prepared)
	return prepared, true
}

func (e *semanticCacheEntry) matches(prepared semanticPreparedRequest) bool {
	return e != nil &&
		e.path == prepared.path &&
		e.rawQuery == prepared.rawQuery &&
		e.backendName == prepared.backendName &&
		e.scope == prepared.scope &&
		e.modelName == prepared.modelName &&
		e.controlFingerprint == prepared.controlFingerprint &&
		e.embeddingModel == prepared.embeddingModel
}

func (c *SemanticCache) pruneExpiredLocked() {
	if c == nil {
		return
	}
	for key, entry := range c.entries {
		if entry == nil {
			c.deleteLocked(key)
			c.evictions.Add(1)
		}
	}
}

func (c *SemanticCache) touchLocked(key string) {
	for i, candidate := range c.order {
		if candidate != key {
			continue
		}
		copy(c.order[i:], c.order[i+1:])
		c.order[len(c.order)-1] = key
		return
	}
	c.order = append(c.order, key)
}

func (c *SemanticCache) deleteLocked(key string) {
	delete(c.entries, key)
	for i, candidate := range c.order {
		if candidate == key {
			c.order = append(c.order[:i], c.order[i+1:]...)
			return
		}
	}
}

func responseFromSemanticCacheEntry(entry *semanticCacheEntry) *http.Response {
	body := append([]byte(nil), entry.body...)
	return &http.Response{
		StatusCode:    entry.status,
		Status:        fmt.Sprintf("%d %s", entry.status, http.StatusText(entry.status)),
		Header:        cloneHeader(entry.header),
		Body:          io.NopCloser(bytes.NewReader(body)),
		ContentLength: int64(len(body)),
	}
}

func semanticCacheKey(prepared semanticPreparedRequest) string {
	h := sha256.New()
	for _, part := range []string{
		"semantic-v1",
		prepared.path,
		prepared.rawQuery,
		prepared.backendName,
		prepared.scope,
		prepared.modelName,
		prepared.bodyFingerprint,
		prepared.embeddingModel,
	} {
		_, _ = h.Write([]byte(part))
		_, _ = h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

func semanticHash(body []byte) string {
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}

func semanticPayloadHasMessages(value any) bool {
	payload, ok := value.(map[string]any)
	if !ok {
		return false
	}
	messages, ok := payload["messages"].([]any)
	return ok && len(messages) > 0
}

func semanticControlValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(typed))
		for key, item := range typed {
			if key == "messages" {
				out[key] = semanticMessagesShape(item)
				continue
			}
			out[key] = semanticControlValue(item)
		}
		return out
	case []any:
		out := make([]any, len(typed))
		for i, item := range typed {
			out[i] = semanticControlValue(item)
		}
		return out
	default:
		return typed
	}
}

func semanticMessagesShape(value any) any {
	messages, ok := value.([]any)
	if !ok {
		return value
	}
	out := make([]any, 0, len(messages))
	for _, raw := range messages {
		msg, ok := raw.(map[string]any)
		if !ok {
			out = append(out, map[string]any{"type": fmt.Sprintf("%T", raw)})
			continue
		}
		shape := map[string]any{}
		if role, ok := msg["role"].(string); ok {
			shape["role"] = role
		}
		if name, ok := msg["name"].(string); ok {
			shape["name"] = name
		}
		shape["content_shape"] = semanticContentShape(msg["content"])
		if toolCallID, ok := msg["tool_call_id"].(string); ok {
			shape["tool_call_id"] = toolCallID
		}
		if toolCalls, ok := msg["tool_calls"]; ok {
			shape["tool_calls_shape"] = semanticContentShape(toolCalls)
		}
		out = append(out, shape)
	}
	return out
}

func semanticContentShape(value any) any {
	switch typed := value.(type) {
	case nil:
		return nil
	case string:
		return "text"
	case []any:
		shape := make([]any, len(typed))
		for i, item := range typed {
			shape[i] = semanticContentShape(item)
		}
		return shape
	case map[string]any:
		out := map[string]any{}
		if typ, ok := typed["type"].(string); ok {
			out["type"] = typ
		}
		for key, item := range typed {
			switch key {
			case "text", "input_text", "content", "arguments":
				continue
			default:
				out[key] = semanticContentShape(item)
			}
		}
		return out
	default:
		return fmt.Sprintf("%T", typed)
	}
}

func cosineSimilarity(a []float64, b []float64) (float64, bool) {
	if len(a) == 0 || len(a) != len(b) {
		return 0, false
	}
	var dot, normA, normB float64
	for i := range a {
		dot += a[i] * b[i]
		normA += a[i] * a[i]
		normB += b[i] * b[i]
	}
	if normA == 0 || normB == 0 {
		return 0, false
	}
	score := dot / (math.Sqrt(normA) * math.Sqrt(normB))
	if score > 1 && score < 1+1e-12 {
		score = 1
	}
	if score < -1 && score > -1-1e-12 {
		score = -1
	}
	return score, true
}
