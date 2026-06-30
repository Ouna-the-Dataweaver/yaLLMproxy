package proxy

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/theio/yallmp-proxy/internal/config"
)

type fakeSemanticEmbedder struct {
	err error
}

func (f fakeSemanticEmbedder) Embed(_ context.Context, input string) ([]float64, error) {
	if f.err != nil {
		return nil, f.err
	}
	switch {
	case strings.Contains(input, "alpha-close"):
		return []float64{0.99, 0.01}, nil
	case strings.Contains(input, "alpha"):
		return []float64{1, 0}, nil
	case strings.Contains(input, "beta"):
		return []float64{0, 1}, nil
	case strings.Contains(input, "middle"):
		return []float64{0.7, 0.7}, nil
	default:
		return []float64{0.5, 0.5}, nil
	}
}

func TestSemanticCacheAddSearchRemove(t *testing.T) {
	cache := newTestSemanticCache(t, semanticTestConfig("read_write", 0.90, 5, 8, 1024), fakeSemanticEmbedder{})
	ctx := context.Background()
	req := semanticTestRequest("scope-a", "node-a", "client", "alpha")
	if !cache.Add(ctx, req, http.StatusOK, semanticJSONHeader(), []byte(`{"answer":"secret-alpha"}`)) {
		t.Fatal("semantic add failed")
	}
	resp, ok := cache.Lookup(ctx, semanticTestRequest("scope-a", "node-a", "client", "alpha-close"))
	if !ok {
		t.Fatal("semantic lookup missed close request")
	}
	if got := readSemanticBody(t, resp); got != `{"answer":"secret-alpha"}` {
		t.Fatalf("semantic response body = %s", got)
	}
	if !cache.Remove(req) {
		t.Fatal("semantic remove failed")
	}
	if _, ok := cache.Lookup(ctx, semanticTestRequest("scope-a", "node-a", "client", "alpha-close")); ok {
		t.Fatal("semantic lookup hit after remove")
	}
}

func TestSemanticCacheCosineRankingThresholdAndTopK(t *testing.T) {
	ctx := context.Background()
	cache := newTestSemanticCache(t, semanticTestConfig("read_write", 0.50, 2, 8, 1024), fakeSemanticEmbedder{})
	for _, prompt := range []string{"beta", "middle", "alpha"} {
		if !cache.Store(ctx, semanticTestRequest("scope-a", "node-a", "client", prompt), http.StatusOK, semanticJSONHeader(), []byte(`{"prompt":"`+prompt+`"}`)) {
			t.Fatalf("store %q failed", prompt)
		}
	}
	prepared, ok := cache.prepare(semanticTestRequest("scope-a", "node-a", "client", "alpha-close"))
	if !ok {
		t.Fatal("prepare lookup failed")
	}
	results := cache.search(prepared, []float64{1, 0})
	if len(results) != 2 {
		t.Fatalf("top-k results = %d, want 2", len(results))
	}
	if !strings.Contains(string(results[0].body), "alpha") || results[0].score < results[1].score {
		t.Fatalf("ranking results = %#v", results)
	}

	thresholdCache := newTestSemanticCache(t, semanticTestConfig("read_write", 0.95, 5, 8, 1024), fakeSemanticEmbedder{})
	if !thresholdCache.Store(ctx, semanticTestRequest("scope-a", "node-a", "client", "beta"), http.StatusOK, semanticJSONHeader(), []byte(`{"answer":"beta"}`)) {
		t.Fatal("threshold store failed")
	}
	if _, ok := thresholdCache.Lookup(ctx, semanticTestRequest("scope-a", "node-a", "client", "alpha")); ok {
		t.Fatal("below-threshold semantic lookup hit")
	}
	snapshot := thresholdCache.Snapshot()
	if snapshot.ThresholdRejected != 1 || snapshot.Misses != 1 {
		t.Fatalf("threshold counters = %#v", snapshot)
	}
}

func TestSemanticCacheEvictionScopeIsolationAndEntryBytes(t *testing.T) {
	ctx := context.Background()
	cache := newTestSemanticCache(t, semanticTestConfig("read_write", 0.90, 5, 1, 16), fakeSemanticEmbedder{})
	if !cache.Store(ctx, semanticTestRequest("scope-a", "node-a", "client", "alpha"), http.StatusOK, semanticJSONHeader(), []byte(`{"a":1}`)) {
		t.Fatal("store alpha failed")
	}
	if !cache.Store(ctx, semanticTestRequest("scope-a", "node-a", "client", "beta"), http.StatusOK, semanticJSONHeader(), []byte(`{"b":2}`)) {
		t.Fatal("store beta failed")
	}
	if _, ok := cache.Lookup(ctx, semanticTestRequest("scope-a", "node-a", "client", "alpha")); ok {
		t.Fatal("evicted alpha entry returned a hit")
	}
	if _, ok := cache.Lookup(ctx, semanticTestRequest("scope-a", "node-a", "client", "beta")); !ok {
		t.Fatal("newest beta entry missed")
	}
	if cache.Store(ctx, semanticTestRequest("scope-a", "node-a", "client", "alpha"), http.StatusOK, semanticJSONHeader(), []byte(`{"too":"large for cap"}`)) {
		t.Fatal("oversized semantic entry stored")
	}

	isolated := newTestSemanticCache(t, semanticTestConfig("read_write", 0.90, 5, 8, 1024), fakeSemanticEmbedder{})
	if !isolated.Store(ctx, semanticTestRequest("scope-a", "node-a", "client", "alpha"), http.StatusOK, semanticJSONHeader(), []byte(`{"answer":"alpha"}`)) {
		t.Fatal("isolated store failed")
	}
	for _, req := range []SemanticCacheRequest{
		semanticTestRequest("scope-b", "node-a", "client", "alpha"),
		semanticTestRequest("scope-a", "node-b", "client", "alpha"),
		semanticTestRequest("scope-a", "node-a", "other", "alpha"),
	} {
		if _, ok := isolated.Lookup(ctx, req); ok {
			t.Fatalf("semantic cache crossed isolation boundary: %#v", req)
		}
	}
}

func TestSemanticCacheEmbeddingErrorsAndSnapshotPrivacy(t *testing.T) {
	ctx := context.Background()
	cache := newTestSemanticCache(t, semanticTestConfig("read_write", 0.90, 5, 8, 1024), fakeSemanticEmbedder{err: errors.New("embed failed")})
	if cache.Store(ctx, semanticTestRequest("scope-a", "node-a", "client", "alpha"), http.StatusOK, semanticJSONHeader(), []byte(`{"secret":"body"}`)) {
		t.Fatal("store succeeded despite embedding error")
	}
	if _, ok := cache.Lookup(ctx, semanticTestRequest("scope-a", "node-a", "client", "alpha")); ok {
		t.Fatal("lookup hit despite embedding error")
	}
	snapshot := cache.Snapshot()
	if snapshot.EmbeddingErrors != 2 {
		t.Fatalf("embedding errors = %d, want 2", snapshot.EmbeddingErrors)
	}
	buf, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	serialized := string(buf)
	for _, forbidden := range []string{"secret", "alpha", "[1", "node-a", "scope-a"} {
		if strings.Contains(serialized, forbidden) {
			t.Fatalf("snapshot leaked %q: %s", forbidden, serialized)
		}
	}
}

func TestSemanticCacheModeSemanticsAndDisabled(t *testing.T) {
	ctx := context.Background()
	if cache := newSemanticCache(semanticTestConfig("off", 0.90, 5, 8, 1024), fakeSemanticEmbedder{}, nil); cache != nil {
		t.Fatalf("off semantic cache = %#v, want nil", cache)
	}
	disabled := semanticTestConfig("read_write", 0.90, 5, 8, 1024)
	disabled.Semantic.Enabled = false
	if cache := newSemanticCache(disabled, fakeSemanticEmbedder{}, nil); cache != nil {
		t.Fatalf("disabled semantic cache = %#v, want nil", cache)
	}

	readOnly := newTestSemanticCache(t, semanticTestConfig("read_only", 0.90, 5, 8, 1024), fakeSemanticEmbedder{})
	req := semanticTestRequest("scope-a", "node-a", "client", "alpha")
	if readOnly.Store(ctx, req, http.StatusOK, semanticJSONHeader(), []byte(`{"answer":"alpha-write"}`)) {
		t.Fatal("read-only semantic cache stored an entry")
	}
	prepared, ok := readOnly.prepare(req)
	if !ok {
		t.Fatal("read-only prepare failed")
	}
	readOnly.add(&semanticCacheEntry{
		key:                prepared.key,
		path:               prepared.path,
		rawQuery:           prepared.rawQuery,
		backendName:        prepared.backendName,
		scope:              prepared.scope,
		modelName:          prepared.modelName,
		controlFingerprint: prepared.controlFingerprint,
		bodyFingerprint:    prepared.bodyFingerprint,
		embeddingModel:     prepared.embeddingModel,
		embedding:          []float64{1, 0},
		status:             http.StatusOK,
		header:             semanticJSONHeader(),
		body:               []byte(`{"answer":"alpha-read"}`),
		createdAt:          readOnly.now(),
	})
	resp, ok := readOnly.Lookup(ctx, semanticTestRequest("scope-a", "node-a", "client", "alpha-close"))
	if !ok {
		t.Fatal("read-only semantic cache missed preloaded entry")
	}
	if got := readSemanticBody(t, resp); got != `{"answer":"alpha-read"}` {
		t.Fatalf("read-only semantic body = %s", got)
	}

	writeOnly := newTestSemanticCache(t, semanticTestConfig("write_only", 0.90, 5, 8, 1024), fakeSemanticEmbedder{})
	if !writeOnly.Store(ctx, req, http.StatusOK, semanticJSONHeader(), []byte(`{"answer":"alpha-write"}`)) {
		t.Fatal("write-only semantic cache did not store")
	}
	if _, ok := writeOnly.Lookup(ctx, semanticTestRequest("scope-a", "node-a", "client", "alpha-close")); ok {
		t.Fatal("write-only semantic cache returned a hit")
	}
	snapshot := writeOnly.Snapshot()
	if snapshot.Writes != 1 || snapshot.Entries != 1 || snapshot.Hits != 0 {
		t.Fatalf("write-only snapshot = %#v", snapshot)
	}
}

func TestCosineSimilarity(t *testing.T) {
	score, ok := cosineSimilarity([]float64{1, 0}, []float64{0.5, 0.5})
	if !ok || score < 0.70 || score > 0.72 {
		t.Fatalf("cosine score = %f ok=%v", score, ok)
	}
	if _, ok := cosineSimilarity([]float64{0, 0}, []float64{1, 0}); ok {
		t.Fatal("zero vector cosine returned ok")
	}
	if _, ok := cosineSimilarity([]float64{1}, []float64{1, 0}); ok {
		t.Fatal("mismatched vector cosine returned ok")
	}
}

func newTestSemanticCache(t *testing.T, cfg config.InferenceCacheConfig, embedder SemanticEmbeddingClient) *SemanticCache {
	t.Helper()
	cache := newSemanticCache(cfg, embedder, nil)
	if cache == nil {
		t.Fatal("semantic cache is nil")
	}
	return cache
}

func semanticTestConfig(mode string, threshold float64, topK int, maxEntries int, maxEntryBytes int) config.InferenceCacheConfig {
	return config.InferenceCacheConfig{
		Mode: mode,
		Semantic: config.SemanticCacheConfig{
			Enabled:             true,
			EmbeddingModel:      "embed-model",
			EmbeddingNode:       "embed-node",
			SimilarityThreshold: threshold,
			TopK:                topK,
			MaxEntries:          maxEntries,
			MaxEntryBytes:       maxEntryBytes,
		},
	}
}

func semanticTestRequest(scope string, backend string, model string, prompt string) SemanticCacheRequest {
	return SemanticCacheRequest{
		Path:        "/v1/chat/completions",
		BackendName: backend,
		Scope:       scope,
		ModelName:   model,
		Body:        []byte(`{"model":"` + model + `","messages":[{"role":"user","content":"` + prompt + `"}],"temperature":0}`),
	}
}

func semanticJSONHeader() http.Header {
	return http.Header{"Content-Type": {"application/json"}}
}

func readSemanticBody(t *testing.T, resp *http.Response) string {
	t.Helper()
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}
