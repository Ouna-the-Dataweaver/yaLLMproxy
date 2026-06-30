package proxy

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/theio/yallmp-proxy/internal/config"
)

func TestInferenceCacheKeyCanonicalizationAndIsolation(t *testing.T) {
	bodyA := []byte(`{"model":"client","messages":[{"role":"user","content":"hi"}],"temperature":1}`)
	bodyB := []byte(`{
		"temperature": 1,
		"messages": [{"content": "hi", "role": "user"}],
		"model": "client"
	}`)
	keyA, ok := inferenceCacheKey("/v1/chat/completions", "x=1", "node-a", "key:key-a", bodyA)
	if !ok {
		t.Fatal("keyA not generated")
	}
	keyB, ok := inferenceCacheKey("/v1/chat/completions", "x=1", "node-a", "key:key-a", bodyB)
	if !ok {
		t.Fatal("keyB not generated")
	}
	if keyA != keyB {
		t.Fatalf("canonical-equivalent bodies produced different keys")
	}
	for name, args := range map[string]struct {
		path    string
		query   string
		backend string
		scope   string
		body    []byte
	}{
		"path":    {path: "/v1/embeddings", query: "x=1", backend: "node-a", scope: "key:key-a", body: bodyA},
		"query":   {path: "/v1/chat/completions", query: "x=2", backend: "node-a", scope: "key:key-a", body: bodyA},
		"backend": {path: "/v1/chat/completions", query: "x=1", backend: "node-b", scope: "key:key-a", body: bodyA},
		"scope":   {path: "/v1/chat/completions", query: "x=1", backend: "node-a", scope: "key:key-b", body: bodyA},
		"body":    {path: "/v1/chat/completions", query: "x=1", backend: "node-a", scope: "key:key-a", body: []byte(`{"model":"client","messages":[{"role":"user","content":"bye"}]}`)},
	} {
		key, ok := inferenceCacheKey(args.path, args.query, args.backend, args.scope, args.body)
		if !ok {
			t.Fatalf("%s key not generated", name)
		}
		if key == keyA {
			t.Fatalf("%s change did not isolate cache key", name)
		}
	}
}

func TestInferenceCacheModes(t *testing.T) {
	if cache := NewInferenceCache(config.InferenceCacheConfig{}); cache != nil {
		t.Fatal("empty config should disable cache")
	}
	readOnly := NewInferenceCache(config.InferenceCacheConfig{Mode: "read_only", MaxEntries: 2})
	if readOnly.Store("k", http.StatusOK, http.Header{"Content-Type": {"application/json"}}, []byte(`{"ok":true}`)) {
		t.Fatal("read-only cache stored an entry")
	}
	if _, ok := readOnly.Lookup("k"); ok {
		t.Fatal("read-only cache returned a cold entry")
	}
	writeOnly := NewInferenceCache(config.InferenceCacheConfig{Mode: "write_only", MaxEntries: 2})
	if !writeOnly.Store("k", http.StatusOK, http.Header{"Content-Type": {"application/json"}}, []byte(`{"ok":true}`)) {
		t.Fatal("write-only cache did not store")
	}
	if _, ok := writeOnly.Lookup("k"); ok {
		t.Fatal("write-only cache returned a hit")
	}
	readWrite := NewInferenceCache(config.InferenceCacheConfig{Mode: "read_write", MaxEntries: 2})
	if !readWrite.Store("k", http.StatusOK, http.Header{"Content-Type": {"application/json"}}, []byte(`{"ok":true}`)) {
		t.Fatal("read-write cache did not store")
	}
	if _, ok := readWrite.Lookup("k"); !ok {
		t.Fatal("read-write cache missed stored entry")
	}
}

func TestInferenceCacheSemanticModes(t *testing.T) {
	ctx := context.Background()
	if cache := NewInferenceCache(semanticTestConfig("off", 0.90, 5, 8, 1024)); cache != nil {
		t.Fatal("semantic cache should not enable global off cache")
	}
	readOnly := NewInferenceCache(semanticTestConfig("read_only", 0.90, 5, 8, 1024))
	readOnly.ConfigureSemantic(semanticTestConfig("read_only", 0.90, 5, 8, 1024), fakeSemanticEmbedder{})
	if readOnly.StoreSemantic(ctx, semanticTestRequest("scope-a", "node-a", "client", "alpha"), http.StatusOK, semanticJSONHeader(), []byte(`{"ok":true}`)) {
		t.Fatal("read-only semantic cache stored")
	}
	if _, ok := readOnly.LookupSemantic(ctx, semanticTestRequest("scope-a", "node-a", "client", "alpha")); ok {
		t.Fatal("read-only semantic cache returned a cold hit")
	}

	writeOnly := NewInferenceCache(semanticTestConfig("write_only", 0.90, 5, 8, 1024))
	writeOnly.ConfigureSemantic(semanticTestConfig("write_only", 0.90, 5, 8, 1024), fakeSemanticEmbedder{})
	if !writeOnly.StoreSemantic(ctx, semanticTestRequest("scope-a", "node-a", "client", "alpha"), http.StatusOK, semanticJSONHeader(), []byte(`{"ok":true}`)) {
		t.Fatal("write-only semantic cache did not store")
	}
	if _, ok := writeOnly.LookupSemantic(ctx, semanticTestRequest("scope-a", "node-a", "client", "alpha")); ok {
		t.Fatal("write-only semantic cache returned a hit")
	}

	readWrite := NewInferenceCache(semanticTestConfig("read_write", 0.90, 5, 8, 1024))
	readWrite.ConfigureSemantic(semanticTestConfig("read_write", 0.90, 5, 8, 1024), fakeSemanticEmbedder{})
	if !readWrite.StoreSemantic(ctx, semanticTestRequest("scope-a", "node-a", "client", "alpha"), http.StatusOK, semanticJSONHeader(), []byte(`{"ok":true}`)) {
		t.Fatal("read-write semantic cache did not store")
	}
	if _, ok := readWrite.LookupSemantic(ctx, semanticTestRequest("scope-a", "node-a", "client", "alpha")); !ok {
		t.Fatal("read-write semantic cache missed stored entry")
	}
}

func TestInferenceCacheExpiryAndEviction(t *testing.T) {
	now := time.Date(2026, 6, 27, 12, 0, 0, 0, time.UTC)
	ttlCache := NewInferenceCache(config.InferenceCacheConfig{Mode: "read_write", TTLSeconds: 1, MaxAgeSeconds: 60, MaxEntries: 2})
	ttlCache.now = func() time.Time { return now }
	ttlCache.Store("ttl", http.StatusOK, http.Header{"Content-Type": {"application/json"}}, []byte(`{"ok":true}`))
	now = now.Add(1100 * time.Millisecond)
	if _, ok := ttlCache.Lookup("ttl"); ok {
		t.Fatal("TTL-expired entry returned a hit")
	}

	now = time.Date(2026, 6, 27, 12, 0, 0, 0, time.UTC)
	maxAgeCache := NewInferenceCache(config.InferenceCacheConfig{Mode: "read_write", TTLSeconds: 60, MaxAgeSeconds: 1, MaxEntries: 2})
	maxAgeCache.now = func() time.Time { return now }
	maxAgeCache.Store("age", http.StatusOK, http.Header{"Content-Type": {"application/json"}}, []byte(`{"ok":true}`))
	now = now.Add(1100 * time.Millisecond)
	if _, ok := maxAgeCache.Lookup("age"); ok {
		t.Fatal("max-age-expired entry returned a hit")
	}

	evictCache := NewInferenceCache(config.InferenceCacheConfig{Mode: "read_write", MaxEntries: 1})
	evictCache.Store("first", http.StatusOK, http.Header{"Content-Type": {"application/json"}}, []byte(`{"first":true}`))
	evictCache.Store("second", http.StatusOK, http.Header{"Content-Type": {"application/json"}}, []byte(`{"second":true}`))
	if _, ok := evictCache.Lookup("first"); ok {
		t.Fatal("oldest entry was not evicted")
	}
	if _, ok := evictCache.Lookup("second"); !ok {
		t.Fatal("newest entry was evicted")
	}
}

func TestInferenceCacheSnapshotCountersAndBounds(t *testing.T) {
	cache := NewInferenceCache(config.InferenceCacheConfig{Mode: "read_write", MaxEntries: 2, MaxEntryBytes: 32})
	if cache == nil {
		t.Fatal("cache is nil")
	}
	if _, ok := cache.Lookup("missing"); ok {
		t.Fatal("missing key returned a hit")
	}
	bodyA := []byte(`{"a":1}`)
	bodyB := []byte(`{"b":2}`)
	bodyC := []byte(`{"c":3}`)
	if !cache.Store("a", http.StatusOK, http.Header{"Content-Type": {"application/json"}}, bodyA) {
		t.Fatal("store a failed")
	}
	if _, ok := cache.Lookup("a"); !ok {
		t.Fatal("stored key missed")
	}
	if !cache.Store("b", http.StatusOK, http.Header{"Content-Type": {"application/json"}}, bodyB) {
		t.Fatal("store b failed")
	}
	if !cache.Store("c", http.StatusOK, http.Header{"Content-Type": {"application/json"}}, bodyC) {
		t.Fatal("store c failed")
	}
	if cache.Store("oversized", http.StatusOK, http.Header{"Content-Type": {"application/json"}}, make([]byte, 33)) {
		t.Fatal("oversized entry stored")
	}

	snapshot := cache.Snapshot()
	if !snapshot.Enabled || snapshot.Mode != "read_write" {
		t.Fatalf("snapshot enabled/mode = %#v", snapshot)
	}
	if snapshot.Hits != 1 || snapshot.Misses != 1 || snapshot.Writes != 3 || snapshot.Evictions != 1 {
		t.Fatalf("snapshot counters = %#v", snapshot)
	}
	if snapshot.Entries != 2 || snapshot.TotalEntryBytes != int64(len(bodyB)+len(bodyC)) {
		t.Fatalf("snapshot size = %#v", snapshot)
	}
	if snapshot.MaxEntries != 2 || snapshot.MaxEntryBytes != 32 {
		t.Fatalf("snapshot bounds = %#v", snapshot)
	}
}

func TestInferenceCacheSnapshotCountsExpiryEviction(t *testing.T) {
	now := time.Date(2026, 6, 27, 12, 0, 0, 0, time.UTC)
	cache := NewInferenceCache(config.InferenceCacheConfig{Mode: "read_write", TTLSeconds: 1, MaxAgeSeconds: 60, MaxEntries: 2})
	cache.now = func() time.Time { return now }
	if !cache.Store("ttl", http.StatusOK, http.Header{"Content-Type": {"application/json"}}, []byte(`{"ok":true}`)) {
		t.Fatal("store failed")
	}
	now = now.Add(1100 * time.Millisecond)
	if _, ok := cache.Lookup("ttl"); ok {
		t.Fatal("expired entry returned a hit")
	}

	snapshot := cache.Snapshot()
	if snapshot.Hits != 0 || snapshot.Misses != 1 || snapshot.Writes != 1 || snapshot.Evictions != 1 {
		t.Fatalf("snapshot counters = %#v", snapshot)
	}
	if snapshot.Entries != 0 || snapshot.TotalEntryBytes != 0 {
		t.Fatalf("expired entry still counted in snapshot: %#v", snapshot)
	}
}

func TestInferenceCacheSnapshotIsReadOnly(t *testing.T) {
	now := time.Date(2026, 6, 27, 12, 0, 0, 0, time.UTC)
	cache := NewInferenceCache(config.InferenceCacheConfig{Mode: "read_write", TTLSeconds: 1, MaxAgeSeconds: 60, MaxEntries: 2})
	cache.now = func() time.Time { return now }
	if !cache.Store("expired", http.StatusOK, http.Header{"Content-Type": {"application/json"}}, []byte(`{"ok":true}`)) {
		t.Fatal("store failed")
	}
	now = now.Add(2 * time.Second)

	snapshot := cache.Snapshot()
	if snapshot.Entries != 1 || snapshot.Misses != 0 || snapshot.Evictions != 0 {
		t.Fatalf("snapshot mutated expired entry state: %#v", snapshot)
	}
	if _, ok := cache.Lookup("expired"); ok {
		t.Fatal("expired entry returned a hit")
	}
	afterLookup := cache.Snapshot()
	if afterLookup.Entries != 0 || afterLookup.Misses != 1 || afterLookup.Evictions != 1 {
		t.Fatalf("expired lookup accounting = %#v", afterLookup)
	}
}

func TestInferenceCacheSnapshotDoesNotTouchLRU(t *testing.T) {
	cache := NewInferenceCache(config.InferenceCacheConfig{Mode: "read_write", MaxEntries: 2})
	if !cache.Store("a", http.StatusOK, http.Header{"Content-Type": {"application/json"}}, []byte(`{"a":1}`)) {
		t.Fatal("store a failed")
	}
	if !cache.Store("b", http.StatusOK, http.Header{"Content-Type": {"application/json"}}, []byte(`{"b":2}`)) {
		t.Fatal("store b failed")
	}
	_ = cache.Snapshot()
	if !cache.Store("c", http.StatusOK, http.Header{"Content-Type": {"application/json"}}, []byte(`{"c":3}`)) {
		t.Fatal("store c failed")
	}
	if _, ok := cache.Lookup("a"); ok {
		t.Fatal("snapshot touched LRU order; oldest entry was not evicted")
	}
	if _, ok := cache.Lookup("b"); !ok {
		t.Fatal("snapshot changed LRU order; second entry was evicted")
	}
	if _, ok := cache.Lookup("c"); !ok {
		t.Fatal("newest entry was evicted")
	}
}

func TestInferenceCacheSnapshotModes(t *testing.T) {
	disabled := InferenceCacheSnapshotForConfig(config.InferenceCacheConfig{Mode: "disabled", MaxEntries: 3, MaxEntryBytes: 4}, nil)
	if disabled.Enabled || disabled.Mode != "off" || disabled.MaxEntries != 3 || disabled.MaxEntryBytes != 4 {
		t.Fatalf("disabled snapshot = %#v", disabled)
	}

	readOnly := NewInferenceCache(config.InferenceCacheConfig{Mode: "read_only", MaxEntries: 2})
	if _, ok := readOnly.Lookup("missing"); ok {
		t.Fatal("read-only missing key returned a hit")
	}
	if readOnly.Store("k", http.StatusOK, http.Header{"Content-Type": {"application/json"}}, []byte(`{"ok":true}`)) {
		t.Fatal("read-only cache stored an entry")
	}
	readOnlySnapshot := readOnly.Snapshot()
	if readOnlySnapshot.Misses != 1 || readOnlySnapshot.Hits != 0 || readOnlySnapshot.Writes != 0 || readOnlySnapshot.Entries != 0 {
		t.Fatalf("read-only snapshot = %#v", readOnlySnapshot)
	}

	writeOnly := NewInferenceCache(config.InferenceCacheConfig{Mode: "write_only", MaxEntries: 2})
	if !writeOnly.Store("k", http.StatusOK, http.Header{"Content-Type": {"application/json"}}, []byte(`{"ok":true}`)) {
		t.Fatal("write-only cache did not store")
	}
	if _, ok := writeOnly.Lookup("k"); ok {
		t.Fatal("write-only cache returned a hit")
	}
	writeOnlySnapshot := writeOnly.Snapshot()
	if writeOnlySnapshot.Misses != 0 || writeOnlySnapshot.Hits != 0 || writeOnlySnapshot.Writes != 1 || writeOnlySnapshot.Entries != 1 {
		t.Fatalf("write-only snapshot = %#v", writeOnlySnapshot)
	}
}

func TestRouterExactCacheModesAndBypasses(t *testing.T) {
	t.Run("read_write chat hit avoids upstream and attempt limiter", func(t *testing.T) {
		router, hits := newCountingCacheRouter(t, "read_write", http.StatusOK)
		body := []byte(`{"model":"client","messages":[{"role":"user","content":"hi"}]}`)
		payload, _ := DecodeJSONBody(body)
		var attempts atomic.Int32
		first := forwardCached(t, router, "/v1/chat/completions", body, payload, false, "key:a", func(context.Context, Backend) (func(), error) {
			attempts.Add(1)
			return func() {}, nil
		})
		second := forwardCached(t, router, "/v1/chat/completions", body, payload, false, "key:a", func(context.Context, Backend) (func(), error) {
			attempts.Add(1)
			return func() {}, nil
		})
		if string(first.body) != string(second.body) {
			t.Fatalf("cached body mismatch: %s != %s", first.body, second.body)
		}
		if hits.Load() != 1 {
			t.Fatalf("upstream hits = %d, want 1", hits.Load())
		}
		if attempts.Load() != 1 {
			t.Fatalf("backend attempt limiter calls = %d, want 1", attempts.Load())
		}
		if len(second.result.AttemptTrail) != 0 {
			t.Fatalf("cache hit attempt trail = %#v", second.result.AttemptTrail)
		}
	})

	t.Run("truthy request bypass skips read and write over cached entry", func(t *testing.T) {
		router, hits := newCountingCacheRouter(t, "read_write", http.StatusOK)
		body := []byte(`{"model":"client","messages":[{"role":"user","content":"bypass cached"}]}`)
		payload, _ := DecodeJSONBody(body)
		first := forwardCached(t, router, "/v1/chat/completions", body, payload, false, "key:a", nil)
		bypass := forwardCached(t, router, "/v1/chat/completions", body, payload, false, "key:a", nil, http.Header{InferenceCacheBypassHeader: {"true"}})
		if string(first.body) == string(bypass.body) {
			t.Fatalf("bypass response unexpectedly came from cache: %s", bypass.body)
		}
		if hits.Load() != 2 {
			t.Fatalf("bypass over cached entry upstream hits = %d, want 2", hits.Load())
		}
		third := forwardCached(t, router, "/v1/chat/completions", body, payload, false, "key:a", nil)
		if string(third.body) != string(first.body) {
			t.Fatalf("bypass wrote cache entry: third=%s first=%s", third.body, first.body)
		}
		if hits.Load() != 2 {
			t.Fatalf("post-bypass cached request upstream hits = %d, want 2", hits.Load())
		}
	})

	t.Run("falsey request bypass header preserves cache behavior", func(t *testing.T) {
		router, hits := newCountingCacheRouter(t, "read_write", http.StatusOK)
		body := []byte(`{"model":"client","messages":[{"role":"user","content":"falsey bypass"}]}`)
		payload, _ := DecodeJSONBody(body)
		first := forwardCached(t, router, "/v1/chat/completions", body, payload, false, "key:a", nil, http.Header{InferenceCacheBypassHeader: {"0"}})
		second := forwardCached(t, router, "/v1/chat/completions", body, payload, false, "key:a", nil, http.Header{InferenceCacheBypassHeader: {"no"}})
		if string(first.body) != string(second.body) {
			t.Fatalf("falsey bypass headers did not use cache: %s != %s", first.body, second.body)
		}
		if hits.Load() != 1 {
			t.Fatalf("falsey bypass upstream hits = %d, want 1", hits.Load())
		}
	})

	t.Run("request bypass on miss does not populate cache", func(t *testing.T) {
		router, hits := newCountingCacheRouter(t, "read_write", http.StatusOK)
		body := []byte(`{"model":"client","messages":[{"role":"user","content":"bypass miss"}]}`)
		payload, _ := DecodeJSONBody(body)
		_ = forwardCached(t, router, "/v1/chat/completions", body, payload, false, "key:a", nil, http.Header{InferenceCacheBypassHeader: {"on"}})
		uncached := forwardCached(t, router, "/v1/chat/completions", body, payload, false, "key:a", nil)
		cached := forwardCached(t, router, "/v1/chat/completions", body, payload, false, "key:a", nil)
		if string(uncached.body) != string(cached.body) {
			t.Fatalf("non-bypass response after bypass miss was not cached: %s != %s", uncached.body, cached.body)
		}
		if hits.Load() != 2 {
			t.Fatalf("bypass miss upstream hits = %d, want 2", hits.Load())
		}
	})

	t.Run("write_only stores but does not read", func(t *testing.T) {
		router, hits := newCountingCacheRouter(t, "write_only", http.StatusOK)
		body := []byte(`{"model":"client","messages":[{"role":"user","content":"hi"}]}`)
		payload, _ := DecodeJSONBody(body)
		_ = forwardCached(t, router, "/v1/chat/completions", body, payload, false, "key:a", nil)
		_ = forwardCached(t, router, "/v1/chat/completions", body, payload, false, "key:a", nil)
		if hits.Load() != 2 {
			t.Fatalf("write-only upstream hits = %d, want 2", hits.Load())
		}
	})

	t.Run("read_only cold cache does not write", func(t *testing.T) {
		router, hits := newCountingCacheRouter(t, "read_only", http.StatusOK)
		body := []byte(`{"model":"client","messages":[{"role":"user","content":"hi"}]}`)
		payload, _ := DecodeJSONBody(body)
		_ = forwardCached(t, router, "/v1/chat/completions", body, payload, false, "key:a", nil)
		_ = forwardCached(t, router, "/v1/chat/completions", body, payload, false, "key:a", nil)
		if hits.Load() != 2 {
			t.Fatalf("read-only upstream hits = %d, want 2", hits.Load())
		}
	})

	t.Run("stream bypass", func(t *testing.T) {
		router, hits := newCountingCacheRouter(t, "read_write", http.StatusOK)
		body := []byte(`{"model":"client","messages":[{"role":"user","content":"hi"}],"stream":true}`)
		payload, _ := DecodeJSONBody(body)
		_ = forwardCached(t, router, "/v1/chat/completions", body, payload, true, "key:a", nil)
		_ = forwardCached(t, router, "/v1/chat/completions", body, payload, true, "key:a", nil)
		if hits.Load() != 2 {
			t.Fatalf("stream upstream hits = %d, want 2", hits.Load())
		}
	})

	t.Run("error bypass", func(t *testing.T) {
		router, hits := newCountingCacheRouter(t, "read_write", http.StatusInternalServerError)
		body := []byte(`{"model":"client","messages":[{"role":"user","content":"hi"}]}`)
		payload, _ := DecodeJSONBody(body)
		_ = forwardCached(t, router, "/v1/chat/completions", body, payload, false, "key:a", nil)
		_ = forwardCached(t, router, "/v1/chat/completions", body, payload, false, "key:a", nil)
		if hits.Load() != 2 {
			t.Fatalf("error upstream hits = %d, want 2", hits.Load())
		}
	})

	t.Run("embeddings hit and changed body miss", func(t *testing.T) {
		router, hits := newCountingCacheRouter(t, "read_write", http.StatusOK)
		bodyA := []byte(`{"model":"client","input":"hello"}`)
		payloadA, _ := DecodeJSONBody(bodyA)
		bodyB := []byte(`{"model":"client","input":"goodbye"}`)
		payloadB, _ := DecodeJSONBody(bodyB)
		_ = forwardCached(t, router, "/v1/embeddings", bodyA, payloadA, false, "key:a", nil)
		_ = forwardCached(t, router, "/v1/embeddings", bodyA, payloadA, false, "key:a", nil)
		_ = forwardCached(t, router, "/v1/embeddings", bodyB, payloadB, false, "key:a", nil)
		if hits.Load() != 2 {
			t.Fatalf("embeddings upstream hits = %d, want 2", hits.Load())
		}
	})
}

func TestRouterExactCacheIsolation(t *testing.T) {
	router, hits := newCountingCacheRouter(t, "read_write", http.StatusOK)
	body := []byte(`{"model":"client","messages":[{"role":"user","content":"hi"}]}`)
	payload, _ := DecodeJSONBody(body)
	_ = forwardCached(t, router, "/v1/chat/completions", body, payload, false, "key:a", nil)
	_ = forwardCached(t, router, "/v1/chat/completions", body, payload, false, "key:b", nil)
	_ = forwardCached(t, router, "/v1/chat/completions", body, payload, false, "key:a", nil)
	if hits.Load() != 2 {
		t.Fatalf("app-key isolation upstream hits = %d, want 2", hits.Load())
	}

	backendB := router.Backends["node-b"]
	backendB.BaseURL = router.Backends["node-a"].BaseURL
	router.Backends["node-b"] = backendB
	router.Models["other"] = Model{Name: "other", Nodes: []string{"node-b"}, NodeWeights: map[string]int{"node-b": 1}}
	bodyOther := []byte(`{"model":"other","messages":[{"role":"user","content":"hi"}]}`)
	payloadOther, _ := DecodeJSONBody(bodyOther)
	_ = forwardCached(t, router, "/v1/chat/completions", bodyOther, payloadOther, false, "key:a", nil)
	if hits.Load() != 3 {
		t.Fatalf("backend isolation upstream hits = %d, want 3", hits.Load())
	}
}

func TestTranslatedInferenceCacheHelpers(t *testing.T) {
	body := []byte(`{"model":"client","messages":[{"role":"user","content":"hi"}]}`)
	payload := map[string]any{"model": "client", "messages": []any{map[string]any{"role": "user", "content": "hi"}}}
	opts := ForwardOptions{Cacheable: true, CacheScope: "key:a"}
	header := http.Header{"Content-Type": {"application/json"}, "Content-Length": {"999"}}
	responseBody := []byte(`{"id":"translated","output_text":"hello"}`)

	router := newTranslatedCacheRouter(t, "read_write")
	if !router.StoreTranslatedInferenceCache("node-a", "/v1/messages", "beta=1", body, payload, false, opts, http.StatusOK, header, responseBody) {
		t.Fatal("translated store failed")
	}
	result, ok := router.LookupTranslatedInferenceCache("client", "/v1/messages", "/v1/messages", "beta=1", body, payload, false, opts)
	if !ok {
		t.Fatal("translated lookup missed")
	}
	defer result.Response.Body.Close()
	gotBody, err := io.ReadAll(result.Response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if result.BackendName != "node-a" || string(gotBody) != string(responseBody) {
		t.Fatalf("translated lookup result = backend %q body %s", result.BackendName, gotBody)
	}
	if result.Response.Header.Get("Content-Type") != "application/json" || result.Response.Header.Get("Content-Length") != "" {
		t.Fatalf("cached translated headers = %#v", result.Response.Header)
	}

	writeOnly := newTranslatedCacheRouter(t, "write_only")
	if !writeOnly.StoreTranslatedInferenceCache("node-a", "/v1/messages", "", body, payload, false, opts, http.StatusOK, header, responseBody) {
		t.Fatal("write-only translated store failed")
	}
	if _, ok := writeOnly.LookupTranslatedInferenceCache("client", "/v1/messages", "/v1/messages", "", body, payload, false, opts); ok {
		t.Fatal("write-only translated cache returned a hit")
	}

	readOnly := newTranslatedCacheRouter(t, "read_only")
	if readOnly.StoreTranslatedInferenceCache("node-a", "/v1/messages", "", body, payload, false, opts, http.StatusOK, header, responseBody) {
		t.Fatal("read-only translated cache stored an entry")
	}
	if _, ok := readOnly.LookupTranslatedInferenceCache("client", "/v1/messages", "/v1/messages", "", body, payload, false, opts); ok {
		t.Fatal("read-only translated cache returned a cold hit")
	}

	for _, tc := range []struct {
		name      string
		router    *Router
		cachePath string
		opts      ForwardOptions
		stream    bool
		status    int
		header    http.Header
		backend   string
	}{
		{name: "nil router", router: nil, cachePath: "/v1/messages", opts: opts, status: http.StatusOK, header: header, backend: "node-a"},
		{name: "bypass", router: router, cachePath: "/v1/messages", opts: ForwardOptions{Cacheable: true, CacheBypass: true, CacheScope: "key:a"}, status: http.StatusOK, header: header, backend: "node-a"},
		{name: "not cacheable", router: router, cachePath: "/v1/messages", opts: ForwardOptions{CacheScope: "key:a"}, status: http.StatusOK, header: header, backend: "node-a"},
		{name: "stream", router: router, cachePath: "/v1/messages", opts: opts, stream: true, status: http.StatusOK, header: header, backend: "node-a"},
		{name: "passthrough path", router: router, cachePath: "/p1/messages", opts: opts, status: http.StatusOK, header: header, backend: "node-a"},
		{name: "non ok status", router: router, cachePath: "/v1/messages", opts: opts, status: http.StatusBadGateway, header: header, backend: "node-a"},
		{name: "non json", router: router, cachePath: "/v1/messages", opts: opts, status: http.StatusOK, header: http.Header{"Content-Type": {"text/plain"}}, backend: "node-a"},
		{name: "unknown backend", router: router, cachePath: "/v1/messages", opts: opts, status: http.StatusOK, header: header, backend: "missing"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.router != nil {
				if tc.router.StoreTranslatedInferenceCache(tc.backend, tc.cachePath, "negative="+tc.name, body, payload, tc.stream, tc.opts, tc.status, tc.header, responseBody) {
					t.Fatal("negative translated store succeeded")
				}
			} else if (*Router)(nil).StoreTranslatedInferenceCache(tc.backend, tc.cachePath, "", body, payload, tc.stream, tc.opts, tc.status, tc.header, responseBody) {
				t.Fatal("nil router translated store succeeded")
			}
			if tc.router != nil {
				if _, ok := tc.router.LookupTranslatedInferenceCache("client", "/v1/messages", tc.cachePath, "negative="+tc.name, body, payload, tc.stream, tc.opts); ok {
					t.Fatal("negative translated lookup hit")
				}
			} else if _, ok := (*Router)(nil).LookupTranslatedInferenceCache("client", "/v1/messages", tc.cachePath, "", body, payload, tc.stream, tc.opts); ok {
				t.Fatal("nil router translated lookup hit")
			}
		})
	}
}

func TestInferenceCacheKeyRejectsInvalidAndEmptyBodies(t *testing.T) {
	for _, tc := range []struct {
		name string
		body []byte
	}{
		{name: "nil", body: nil},
		{name: "empty", body: []byte("")},
		{name: "invalid", body: []byte("{not json")},
		{name: "trailing garbage", body: []byte(`{"model":"client"} garbage`)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if key, ok := inferenceCacheKey("/v1/chat/completions", "", "node-a", "", tc.body); ok || key != "" {
				t.Fatalf("key = %q, ok=%v; want empty false", key, ok)
			}
		})
	}
}

func TestInferenceCacheableResponseAndEndpoint(t *testing.T) {
	for _, tc := range []struct {
		path string
		want bool
	}{
		{path: "/v1/chat/completions", want: true},
		{path: "/v1/messages", want: true},
		{path: "/v1/responses", want: true},
		{path: "/v1/embeddings", want: true},
		{path: "/v1/rerank", want: false},
		{path: "/p1/native", want: false},
	} {
		t.Run(tc.path, func(t *testing.T) {
			if got := inferenceCacheableEndpoint(tc.path); got != tc.want {
				t.Fatalf("inferenceCacheableEndpoint(%q) = %v, want %v", tc.path, got, tc.want)
			}
		})
	}

	for _, tc := range []struct {
		name string
		resp *http.Response
		want bool
	}{
		{name: "nil", want: false},
		{name: "non ok", resp: &http.Response{StatusCode: http.StatusCreated, Header: http.Header{"Content-Type": {"application/json"}}}, want: false},
		{name: "event stream", resp: &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"text/event-stream"}}}, want: false},
		{name: "json", resp: &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/json"}}}, want: true},
		{name: "json charset", resp: &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/json; charset=utf-8"}}}, want: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := inferenceCacheableResponse(tc.resp); got != tc.want {
				t.Fatalf("inferenceCacheableResponse() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestInferenceCacheStoreUpdatePreservesStatusAndHeader(t *testing.T) {
	cache := NewInferenceCache(config.InferenceCacheConfig{Mode: "read_write", MaxEntries: 2})
	if !cache.Store("key", http.StatusOK, http.Header{"Content-Type": {"application/json"}, "X-Request-Id": {"first"}}, []byte(`{"first":true}`)) {
		t.Fatal("initial store failed")
	}
	if !cache.Store("key", http.StatusCreated, http.Header{"Content-Type": {"application/json; charset=utf-8"}, "X-Request-Id": {"second"}}, []byte(`{"second":true}`)) {
		t.Fatal("update store failed")
	}
	resp, ok := cache.Lookup("key")
	if !ok {
		t.Fatal("updated entry missed")
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusCreated || resp.Header.Get("Content-Type") != "application/json; charset=utf-8" || resp.Header.Get("X-Request-Id") != "second" || string(body) != `{"second":true}` {
		t.Fatalf("updated cached response = status %d headers %#v body %s", resp.StatusCode, resp.Header, body)
	}
	snapshot := cache.Snapshot()
	if snapshot.Writes != 2 || snapshot.Entries != 1 {
		t.Fatalf("updated snapshot = %#v", snapshot)
	}
}

func TestInferenceCacheExpiredNilEntry(t *testing.T) {
	cache := NewInferenceCache(config.InferenceCacheConfig{Mode: "read_write"})
	if !cache.expired(nil) {
		t.Fatal("nil cache entry should be expired")
	}
}

type cachedForwardResult struct {
	result ForwardResult
	body   []byte
}

func forwardCached(t *testing.T, router *Router, path string, body []byte, payload map[string]any, isStream bool, scope string, limiter AttemptLimiter, extraHeaders ...http.Header) cachedForwardResult {
	t.Helper()
	headers := http.Header{"Content-Type": {"application/json"}}
	for _, extra := range extraHeaders {
		for key, values := range extra {
			for _, value := range values {
				headers.Add(key, value)
			}
		}
	}
	result, err := router.ForwardWithOptions(context.Background(), fmt.Sprint(payload["model"]), path, "", body, payload, isStream, headers, ForwardOptions{
		BeforeAttempt: limiter,
		KeyID:         strings.TrimPrefix(scope, "key:"),
		Cacheable:     true,
		CacheBypass:   InferenceCacheBypassRequested(headers),
		CacheScope:    scope,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer result.Response.Body.Close()
	respBody, err := io.ReadAll(result.Response.Body)
	if err != nil {
		t.Fatal(err)
	}
	return cachedForwardResult{result: result, body: respBody}
}

func newCountingCacheRouter(t *testing.T, mode string, status int) (*Router, *atomic.Int32) {
	t.Helper()
	var hits atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hit := hits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		switch r.URL.Path {
		case "/v1/embeddings":
			_, _ = fmt.Fprintf(w, `{"id":"emb-%d","object":"list","data":[{"object":"embedding","index":0,"embedding":[%d]}],"model":"upstream","usage":{"prompt_tokens":1,"total_tokens":1}}`, hit, hit)
		default:
			_, _ = fmt.Fprintf(w, `{"id":"chat-%d","object":"chat.completion","model":"upstream","choices":[{"index":0,"finish_reason":"stop","message":{"role":"assistant","content":"hit-%d"}}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`, hit, hit)
		}
	}))
	t.Cleanup(upstream.Close)
	router, err := NewRouter(&config.Config{
		Nodes: []config.NodeEntry{
			{Name: "node-a", APIBase: upstream.URL + "/v1", Model: "upstream"},
			{Name: "node-b", APIBase: "http://node-b.test/v1", Model: "upstream"},
		},
		Models: []config.ClientModelEntry{
			{Name: "client", Nodes: []string{"node-a"}},
		},
		ProxySettings: config.ProxySettings{InferenceCache: config.InferenceCacheConfig{
			Mode:       mode,
			TTLSeconds: 60,
			MaxEntries: 8,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return router, &hits
}

func newTranslatedCacheRouter(t *testing.T, mode string) *Router {
	t.Helper()
	router, err := NewRouter(&config.Config{
		Nodes: []config.NodeEntry{{
			Name:    "node-a",
			APIBase: "http://node-a.test/v1",
			Model:   "upstream-model",
		}},
		Models: []config.ClientModelEntry{{Name: "client", Nodes: []string{"node-a"}}},
		ProxySettings: config.ProxySettings{InferenceCache: config.InferenceCacheConfig{
			Mode:       mode,
			TTLSeconds: 60,
			MaxEntries: 8,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return router
}
