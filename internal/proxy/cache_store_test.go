package proxy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/theio/yallmp-proxy/internal/config"
)

func TestSQLiteCacheStoreRoundTripRehydratesNewCache(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cache.db")
	cfg := persistentCacheConfig(path, "read_write")
	cache := NewInferenceCache(cfg)
	if cache == nil {
		t.Fatal("cache is nil")
	}
	body := []byte(`{"id":"cached","choices":[{"message":{"content":"hello"}}]}`)
	if !cache.Store("round-trip", http.StatusOK, http.Header{"Content-Type": {"application/json"}, "X-Trace": {"one"}}, body) {
		t.Fatal("store failed")
	}
	if err := cache.Close(); err != nil {
		t.Fatal(err)
	}

	restarted := NewInferenceCache(cfg)
	defer restarted.Close()
	resp, ok := restarted.Lookup("round-trip")
	if !ok {
		t.Fatal("rehydrated cache missed")
	}
	defer resp.Body.Close()
	got, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(body) {
		t.Fatalf("body = %s, want %s", got, body)
	}
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Content-Type") != "application/json" || resp.Header.Get("X-Trace") != "one" {
		t.Fatalf("response = status %d header %#v", resp.StatusCode, resp.Header)
	}
	snapshot := restarted.Snapshot()
	if !snapshot.PersistentEnabled || snapshot.PersistentMode != "read_write" || snapshot.PersistedLoaded != 1 {
		t.Fatalf("snapshot persistence fields = %#v", snapshot)
	}
}

func TestInferenceCachePersistenceTTLExpiryAfterRehydrate(t *testing.T) {
	now := time.Date(2026, 6, 28, 12, 0, 0, 0, time.UTC)
	store := &recordingCacheStore{entries: []CacheStoreEntry{{
		Key:       "expired",
		Status:    http.StatusOK,
		Header:    http.Header{"Content-Type": {"application/json"}},
		Body:      []byte(`{"expired":true}`),
		CreatedAt: now.Add(-2 * time.Second),
	}}}
	cache := newInferenceCache(config.InferenceCacheConfig{
		Mode:       "read_write",
		TTLSeconds: 1,
		Persistence: config.InferenceCachePersistenceConfig{
			Enabled:   true,
			FlushMode: "read_write",
		},
	}, store, func() time.Time { return now })
	if cache == nil {
		t.Fatal("cache is nil")
	}
	if snapshot := cache.Snapshot(); snapshot.Entries != 0 || snapshot.PersistedLoaded != 0 {
		t.Fatalf("expired entry rehydrated: %#v", snapshot)
	}
	if !store.deleted("expired") {
		t.Fatalf("expired persisted entry was not deleted: %#v", store.deletes)
	}
}

func TestInferenceCachePersistenceMaxEntryEvictionAfterRehydrate(t *testing.T) {
	now := time.Date(2026, 6, 28, 12, 0, 0, 0, time.UTC)
	store := &recordingCacheStore{entries: []CacheStoreEntry{
		cacheStoreEntry("first", now),
		cacheStoreEntry("second", now.Add(time.Second)),
		cacheStoreEntry("third", now.Add(2*time.Second)),
	}}
	cache := newInferenceCache(config.InferenceCacheConfig{
		Mode:       "read_write",
		MaxEntries: 2,
		Persistence: config.InferenceCachePersistenceConfig{
			Enabled:   true,
			FlushMode: "read_write",
		},
	}, store, func() time.Time { return now })
	if cache == nil {
		t.Fatal("cache is nil")
	}
	if _, ok := cache.Lookup("first"); ok {
		t.Fatal("oldest persisted entry survived max-entry eviction")
	}
	if _, ok := cache.Lookup("second"); !ok {
		t.Fatal("second persisted entry missed")
	}
	if _, ok := cache.Lookup("third"); !ok {
		t.Fatal("third persisted entry missed")
	}
	if !store.deleted("first") {
		t.Fatalf("evicted persisted entry was not deleted: %#v", store.deletes)
	}
}

func TestInferenceCacheCorruptSQLiteStoreLoadNonFatal(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cache.db")
	if err := os.WriteFile(path, []byte("not a sqlite database"), 0o600); err != nil {
		t.Fatal(err)
	}
	cache := NewInferenceCache(persistentCacheConfig(path, "read_write"))
	if cache == nil {
		t.Fatal("cache is nil")
	}
	defer cache.Close()
	if snapshot := cache.Snapshot(); snapshot.PersistentEnabled {
		t.Fatalf("corrupt store should disable persistence for this cache: %#v", snapshot)
	}
	if !cache.Store("memory", http.StatusOK, http.Header{"Content-Type": {"application/json"}}, []byte(`{"ok":true}`)) {
		t.Fatal("memory store failed after corrupt persistent store")
	}
	if _, ok := cache.Lookup("memory"); !ok {
		t.Fatal("memory lookup missed after corrupt persistent store")
	}
}

func TestRouterRejectsInvalidInferenceCachePersistenceConfig(t *testing.T) {
	_, err := NewRouter(&config.Config{
		Nodes: []config.NodeEntry{{
			Name:    "node-a",
			APIBase: "https://example.test/v1",
			Model:   "upstream",
		}},
		Models: []config.ClientModelEntry{{
			Name:  "client",
			Nodes: []string{"node-a"},
		}},
		ProxySettings: config.ProxySettings{InferenceCache: config.InferenceCacheConfig{
			Mode: "read_write",
			Persistence: config.InferenceCachePersistenceConfig{
				Enabled: true,
			},
		}},
	})
	if err == nil || !strings.Contains(err.Error(), "proxy_settings.inference_cache.persistence.path") {
		t.Fatalf("NewRouter error = %v, want strict persistence path validation", err)
	}
}

func TestInferenceCachePersistenceModeInteractions(t *testing.T) {
	now := time.Date(2026, 6, 28, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name        string
		cacheMode   string
		storeMode   string
		wantLoad    bool
		wantSave    bool
		wantWarmHit bool
		wantStore   bool
	}{
		{name: "read_write", cacheMode: "read_write", storeMode: "read_write", wantLoad: true, wantSave: true, wantWarmHit: true, wantStore: true},
		{name: "read_only persistence", cacheMode: "read_write", storeMode: "read_only", wantLoad: true, wantSave: false, wantWarmHit: true, wantStore: true},
		{name: "write_only persistence", cacheMode: "read_write", storeMode: "write_only", wantLoad: false, wantSave: true, wantWarmHit: false, wantStore: true},
		{name: "off persistence", cacheMode: "read_write", storeMode: "off", wantLoad: false, wantSave: false, wantWarmHit: false, wantStore: true},
		{name: "read_only cache", cacheMode: "read_only", storeMode: "read_write", wantLoad: true, wantSave: false, wantWarmHit: true, wantStore: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := &recordingCacheStore{entries: []CacheStoreEntry{cacheStoreEntry("warm", now)}}
			cache := newInferenceCache(config.InferenceCacheConfig{
				Mode:       tc.cacheMode,
				MaxEntries: 4,
				Persistence: config.InferenceCachePersistenceConfig{
					Enabled:   true,
					FlushMode: tc.storeMode,
				},
			}, store, func() time.Time { return now })
			if cache == nil {
				t.Fatal("cache is nil")
			}
			gotLoad := store.loads == 1
			if gotLoad != tc.wantLoad {
				t.Fatalf("loads = %d, wantLoad %v", store.loads, tc.wantLoad)
			}
			_, warmHit := cache.Lookup("warm")
			if warmHit != tc.wantWarmHit {
				t.Fatalf("warm hit = %v, want %v", warmHit, tc.wantWarmHit)
			}
			stored := cache.Store("new", http.StatusOK, http.Header{"Content-Type": {"application/json"}}, []byte(`{"new":true}`))
			if stored != tc.wantStore {
				t.Fatalf("Store returned %v, want %v", stored, tc.wantStore)
			}
			gotSave := len(store.saves) == 1
			if gotSave != tc.wantSave {
				t.Fatalf("saves = %d, wantSave %v", len(store.saves), tc.wantSave)
			}
		})
	}
}

func TestInferenceCachePersistenceWriteFailureNonFatal(t *testing.T) {
	store := &recordingCacheStore{saveErr: errors.New("disk full")}
	cache := newInferenceCache(config.InferenceCacheConfig{
		Mode: "read_write",
		Persistence: config.InferenceCachePersistenceConfig{
			Enabled:   true,
			FlushMode: "read_write",
		},
	}, store, time.Now)
	if !cache.Store("key", http.StatusOK, http.Header{"Content-Type": {"application/json"}}, []byte(`{"ok":true}`)) {
		t.Fatal("store failed despite persistent write error")
	}
	if len(store.saves) != 1 {
		t.Fatalf("persistent save attempts = %d, want 1", len(store.saves))
	}
	if _, ok := cache.Lookup("key"); !ok {
		t.Fatal("memory cache missed after persistent write error")
	}
}

func TestInferenceCachePersistenceRuntimeEvictionDeletes(t *testing.T) {
	store := &recordingCacheStore{}
	cache := newInferenceCache(config.InferenceCacheConfig{
		Mode:       "read_write",
		MaxEntries: 1,
		Persistence: config.InferenceCachePersistenceConfig{
			Enabled:   true,
			FlushMode: "read_write",
		},
	}, store, time.Now)
	if !cache.Store("first", http.StatusOK, http.Header{"Content-Type": {"application/json"}}, []byte(`{"first":true}`)) {
		t.Fatal("store first failed")
	}
	if !cache.Store("second", http.StatusOK, http.Header{"Content-Type": {"application/json"}}, []byte(`{"second":true}`)) {
		t.Fatal("store second failed")
	}
	if len(store.saves) != 2 {
		t.Fatalf("persistent saves = %d, want 2", len(store.saves))
	}
	if !store.deleted("first") {
		t.Fatalf("evicted key was not deleted from persistence: %#v", store.deletes)
	}
}

func TestRouterInferenceCacheBypassDoesNotPersist(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cache.db")
	cacheCfg := persistentCacheConfig(path, "read_write")
	body := []byte(`{"model":"client","messages":[{"role":"user","content":"persist bypass"}]}`)
	payload, _ := DecodeJSONBody(body)

	bypassRouter, bypassHits := newCountingCacheRouterWithCacheConfig(t, cacheCfg, http.StatusOK)
	_ = forwardCached(t, bypassRouter, "/v1/chat/completions", body, payload, false, "key:a", nil, http.Header{InferenceCacheBypassHeader: {"true"}})
	if bypassHits.Load() != 1 {
		t.Fatalf("bypass upstream hits = %d, want 1", bypassHits.Load())
	}
	key := cacheKeyForRouterBody(t, bypassRouter, "/v1/chat/completions", "", body, payload, "key:a")
	if err := bypassRouter.InferenceCache.Close(); err != nil {
		t.Fatal(err)
	}
	afterBypass := NewInferenceCache(cacheCfg)
	if _, ok := afterBypass.Lookup(key); ok {
		t.Fatal("bypass request persisted a cache entry")
	}
	_ = afterBypass.Close()

	writeRouter, _ := newCountingCacheRouterWithCacheConfig(t, cacheCfg, http.StatusOK)
	written := forwardCached(t, writeRouter, "/v1/chat/completions", body, payload, false, "key:a", nil)
	if err := writeRouter.InferenceCache.Close(); err != nil {
		t.Fatal(err)
	}
	afterWrite := NewInferenceCache(cacheCfg)
	defer afterWrite.Close()
	resp, ok := afterWrite.Lookup(key)
	if !ok {
		t.Fatal("non-bypass request did not persist cache entry")
	}
	defer resp.Body.Close()
	got, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(written.body) {
		t.Fatalf("persisted body = %s, want %s", got, written.body)
	}
}

func cacheStoreEntry(key string, createdAt time.Time) CacheStoreEntry {
	return CacheStoreEntry{
		Key:       key,
		Status:    http.StatusOK,
		Header:    http.Header{"Content-Type": {"application/json"}},
		Body:      []byte(fmt.Sprintf(`{"key":%q}`, key)),
		CreatedAt: createdAt,
	}
}

func persistentCacheConfig(path string, flushMode string) config.InferenceCacheConfig {
	return config.InferenceCacheConfig{
		Mode:       "read_write",
		TTLSeconds: 60,
		MaxEntries: 8,
		Persistence: config.InferenceCachePersistenceConfig{
			Enabled:   true,
			Path:      path,
			FlushMode: flushMode,
		},
	}
}

func cacheKeyForRouterBody(t *testing.T, router *Router, path string, rawQuery string, body []byte, payload map[string]any, scope string) string {
	t.Helper()
	backend := router.Backends["node-a"]
	bodyResult := BuildBackendBodyWithWarnings(payload, backend, body, false)
	key, ok := inferenceCacheKey(path, rawQuery, backend.Name, scope, bodyResult.Body)
	if !ok {
		t.Fatal("cache key not generated")
	}
	return key
}

func newCountingCacheRouterWithCacheConfig(t *testing.T, cacheCfg config.InferenceCacheConfig, status int) (*Router, *atomic.Int32) {
	t.Helper()
	var hits atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hit := hits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = fmt.Fprintf(w, `{"id":"chat-%d","object":"chat.completion","model":"upstream","choices":[{"index":0,"finish_reason":"stop","message":{"role":"assistant","content":"hit-%d"}}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`, hit, hit)
	}))
	t.Cleanup(upstream.Close)
	router, err := NewRouter(&config.Config{
		Nodes: []config.NodeEntry{
			{Name: "node-a", APIBase: upstream.URL + "/v1", Model: "upstream"},
		},
		Models: []config.ClientModelEntry{
			{Name: "client", Nodes: []string{"node-a"}},
		},
		ProxySettings: config.ProxySettings{InferenceCache: cacheCfg},
	})
	if err != nil {
		t.Fatal(err)
	}
	return router, &hits
}

type recordingCacheStore struct {
	mu      sync.Mutex
	entries []CacheStoreEntry
	loadErr error
	saveErr error
	delErr  error
	loads   int
	saves   []CacheStoreEntry
	deletes []string
	closed  bool
}

func (s *recordingCacheStore) LoadAll(context.Context) ([]CacheStoreEntry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.loads++
	if s.loadErr != nil {
		return nil, s.loadErr
	}
	out := make([]CacheStoreEntry, 0, len(s.entries))
	for _, entry := range s.entries {
		out = append(out, cloneCacheStoreEntry(entry))
	}
	return out, nil
}

func (s *recordingCacheStore) Save(_ context.Context, entry CacheStoreEntry) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.saves = append(s.saves, cloneCacheStoreEntry(entry))
	return s.saveErr
}

func (s *recordingCacheStore) Delete(_ context.Context, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.deletes = append(s.deletes, key)
	return s.delErr
}

func (s *recordingCacheStore) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	return nil
}

func (s *recordingCacheStore) deleted(key string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, deleted := range s.deletes {
		if deleted == key {
			return true
		}
	}
	return false
}

func cloneCacheStoreEntry(entry CacheStoreEntry) CacheStoreEntry {
	return CacheStoreEntry{
		Key:       strings.TrimSpace(entry.Key),
		Status:    entry.Status,
		Header:    cloneHeader(entry.Header),
		Body:      append([]byte(nil), entry.Body...),
		CreatedAt: entry.CreatedAt,
	}
}
