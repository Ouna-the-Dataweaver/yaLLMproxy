package proxy

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/theio/yallmp-proxy/internal/config"
)

const (
	defaultInferenceCacheTTLSeconds    = 300
	defaultInferenceCacheMaxAgeSeconds = 300
	defaultInferenceCacheMaxEntries    = 1024
	defaultInferenceCacheMaxEntryBytes = 1 << 20
	inferenceCacheKeyVersion           = "v1"
	InferenceCacheBypassHeader         = "x-yallmp-cache-bypass"
)

type inferenceCacheMode string

const (
	inferenceCacheOff       inferenceCacheMode = "off"
	inferenceCacheReadWrite inferenceCacheMode = "read_write"
	inferenceCacheReadOnly  inferenceCacheMode = "read_only"
	inferenceCacheWriteOnly inferenceCacheMode = "write_only"
)

type InferenceCache struct {
	mu            sync.Mutex
	mode          inferenceCacheMode
	ttl           time.Duration
	maxAge        time.Duration
	maxEntries    int
	maxEntryBytes int
	now           func() time.Time
	entries       map[string]*inferenceCacheEntry
	order         []string
	store         CacheStore
	storeMode     inferenceCachePersistenceMode
	storeEnabled  bool
	hits          atomic.Uint64
	misses        atomic.Uint64
	writes        atomic.Uint64
	evictions     atomic.Uint64
	loaded        atomic.Uint64
	semantic      *SemanticCache
}

type inferenceCacheEntry struct {
	status    int
	header    http.Header
	body      []byte
	createdAt time.Time
}

type InferenceCacheSnapshot struct {
	Enabled           bool                  `json:"enabled"`
	Mode              string                `json:"mode"`
	Hits              uint64                `json:"hits"`
	Misses            uint64                `json:"misses"`
	Writes            uint64                `json:"writes"`
	Evictions         uint64                `json:"evictions"`
	Entries           int                   `json:"entries"`
	TotalEntryBytes   int64                 `json:"total_entry_bytes"`
	MaxEntries        int                   `json:"max_entries"`
	MaxEntryBytes     int                   `json:"max_entry_bytes"`
	PersistentEnabled bool                  `json:"persistent_enabled"`
	PersistentMode    string                `json:"persistent_mode,omitempty"`
	PersistedLoaded   uint64                `json:"persisted_loaded"`
	Semantic          SemanticCacheSnapshot `json:"semantic"`
}

func NewInferenceCache(cfg config.InferenceCacheConfig) *InferenceCache {
	return newInferenceCache(cfg, nil, time.Now)
}

func newInferenceCache(cfg config.InferenceCacheConfig, store CacheStore, now func() time.Time) *InferenceCache {
	mode := normalizeInferenceCacheMode(cfg.Mode)
	if mode == inferenceCacheOff {
		return nil
	}
	if now == nil {
		now = time.Now
	}
	ttl := secondsDuration(cfg.TTLSeconds, defaultInferenceCacheTTLSeconds)
	maxAge := secondsDuration(cfg.MaxAgeSeconds, defaultInferenceCacheMaxAgeSeconds)
	maxEntries := cfg.MaxEntries
	if maxEntries <= 0 {
		maxEntries = defaultInferenceCacheMaxEntries
	}
	maxEntryBytes := cfg.MaxEntryBytes
	if maxEntryBytes <= 0 {
		maxEntryBytes = defaultInferenceCacheMaxEntryBytes
	}
	storeMode := inferenceCachePersistenceOff
	storeEnabled := false
	if store == nil {
		store, storeMode, storeEnabled = newInferenceCacheStoreFromConfig(cfg.Persistence)
	} else {
		storeMode = normalizeInferenceCachePersistenceMode(cfg.Persistence.FlushMode)
		if config.ParseBoolStrict(cfg.Persistence.Enabled) && storeMode != inferenceCachePersistenceOff {
			storeEnabled = true
		}
	}
	if store == nil {
		store = NoopCacheStore{}
	}
	cache := &InferenceCache{
		mode:          mode,
		ttl:           ttl,
		maxAge:        maxAge,
		maxEntries:    maxEntries,
		maxEntryBytes: maxEntryBytes,
		now:           now,
		entries:       map[string]*inferenceCacheEntry{},
		store:         store,
		storeMode:     storeMode,
		storeEnabled:  storeEnabled,
	}
	cache.hydrateFromStore()
	return cache
}

func newInferenceCacheStoreFromConfig(cfg config.InferenceCachePersistenceConfig) (CacheStore, inferenceCachePersistenceMode, bool) {
	if !config.ParseBoolStrict(cfg.Enabled) {
		return NoopCacheStore{}, inferenceCachePersistenceOff, false
	}
	mode := normalizeInferenceCachePersistenceMode(cfg.FlushMode)
	if mode == inferenceCachePersistenceOff {
		return NoopCacheStore{}, mode, false
	}
	store, err := NewSQLiteCacheStore(cfg.Path)
	if err != nil {
		log.Printf("inference cache persistence disabled: %v", err)
		return NoopCacheStore{}, inferenceCachePersistenceOff, false
	}
	return store, mode, true
}

func InferenceCacheSnapshotForConfig(cfg config.InferenceCacheConfig, cache *InferenceCache) InferenceCacheSnapshot {
	if cache != nil {
		return cache.Snapshot()
	}
	maxEntries := cfg.MaxEntries
	if maxEntries <= 0 {
		maxEntries = defaultInferenceCacheMaxEntries
	}
	maxEntryBytes := cfg.MaxEntryBytes
	if maxEntryBytes <= 0 {
		maxEntryBytes = defaultInferenceCacheMaxEntryBytes
	}
	return InferenceCacheSnapshot{
		Enabled:       false,
		Mode:          string(normalizeInferenceCacheMode(cfg.Mode)),
		MaxEntries:    maxEntries,
		MaxEntryBytes: maxEntryBytes,
		Semantic:      SemanticCacheSnapshotForConfig(cfg, nil),
	}
}

func normalizeInferenceCacheMode(mode string) inferenceCacheMode {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "", "off", "disabled":
		return inferenceCacheOff
	case "read_write":
		return inferenceCacheReadWrite
	case "read-only", "read_only":
		return inferenceCacheReadOnly
	case "write-only", "write_only":
		return inferenceCacheWriteOnly
	default:
		return inferenceCacheOff
	}
}

func (c *InferenceCache) Snapshot() InferenceCacheSnapshot {
	if c == nil {
		return InferenceCacheSnapshotForConfig(config.InferenceCacheConfig{}, nil)
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
	hits := c.hits.Load()
	misses := c.misses.Load()
	writes := c.writes.Load()
	evictions := c.evictions.Load()
	maxEntries := c.maxEntries
	maxEntryBytes := c.maxEntryBytes
	persistent := c.storeEnabled
	persistentMode := c.storeMode
	loaded := c.loaded.Load()
	semantic := c.semantic
	c.mu.Unlock()
	return InferenceCacheSnapshot{
		Enabled:           true,
		Mode:              string(mode),
		Hits:              hits,
		Misses:            misses,
		Writes:            writes,
		Evictions:         evictions,
		Entries:           entries,
		TotalEntryBytes:   totalEntryBytes,
		MaxEntries:        maxEntries,
		MaxEntryBytes:     maxEntryBytes,
		PersistentEnabled: persistent,
		PersistentMode:    string(persistentMode),
		PersistedLoaded:   loaded,
		Semantic:          SemanticCacheSnapshotForConfig(config.InferenceCacheConfig{}, semantic),
	}
}

func secondsDuration(value float64, fallbackSeconds int) time.Duration {
	if value <= 0 {
		return time.Duration(fallbackSeconds) * time.Second
	}
	return time.Duration(value * float64(time.Second))
}

func (c *InferenceCache) CanRead() bool {
	return c != nil && (c.mode == inferenceCacheReadWrite || c.mode == inferenceCacheReadOnly)
}

func (c *InferenceCache) CanWrite() bool {
	return c != nil && (c.mode == inferenceCacheReadWrite || c.mode == inferenceCacheWriteOnly)
}

func InferenceCacheBypassRequested(header http.Header) bool {
	for _, value := range header.Values(InferenceCacheBypassHeader) {
		switch strings.ToLower(strings.TrimSpace(value)) {
		case "1", "true", "yes", "on":
			return true
		}
	}
	return false
}

func (c *InferenceCache) Lookup(key string) (*http.Response, bool) {
	if !c.CanRead() || key == "" {
		return nil, false
	}
	c.mu.Lock()
	entry, ok := c.entries[key]
	if !ok {
		c.misses.Add(1)
		c.mu.Unlock()
		return nil, false
	}
	if c.expired(entry) {
		c.deleteLocked(key)
		c.evictions.Add(1)
		c.misses.Add(1)
		c.mu.Unlock()
		c.deletePersisted(key)
		return nil, false
	}
	c.touchLocked(key)
	c.hits.Add(1)
	resp := responseFromCacheEntry(entry)
	c.mu.Unlock()
	return resp, true
}

func (c *InferenceCache) Store(key string, status int, header http.Header, body []byte) bool {
	if !c.CanWrite() || key == "" || len(body) > c.maxEntryBytes {
		return false
	}
	createdAt := c.now()
	persisted := CacheStoreEntry{
		Key:       key,
		Status:    status,
		Header:    cloneHeader(header),
		Body:      append([]byte(nil), body...),
		CreatedAt: createdAt,
	}
	var evicted []string
	c.mu.Lock()
	if existing := c.entries[key]; existing != nil {
		existing.status = status
		existing.header = cloneHeader(header)
		existing.body = append(existing.body[:0], body...)
		existing.createdAt = createdAt
		c.touchLocked(key)
		c.writes.Add(1)
		c.mu.Unlock()
		c.savePersisted(persisted)
		return true
	}
	c.entries[key] = &inferenceCacheEntry{
		status:    status,
		header:    cloneHeader(header),
		body:      append([]byte(nil), body...),
		createdAt: createdAt,
	}
	c.order = append(c.order, key)
	for len(c.entries) > c.maxEntries && len(c.order) > 0 {
		evicted = append(evicted, c.order[0])
		c.deleteLocked(c.order[0])
		c.evictions.Add(1)
	}
	c.writes.Add(1)
	c.mu.Unlock()
	c.savePersisted(persisted)
	for _, evictedKey := range evicted {
		c.deletePersisted(evictedKey)
	}
	return true
}

func (c *InferenceCache) Close() error {
	if c == nil || c.store == nil {
		return nil
	}
	if c.semantic != nil {
		_ = c.semantic.Close()
	}
	return c.store.Close()
}

func (c *InferenceCache) hydrateFromStore() {
	if c == nil || !c.persistenceCanRead() || !c.CanRead() {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), inferenceCacheStoreOperationTimeout)
	defer cancel()
	entries, err := c.store.LoadAll(ctx)
	if err != nil {
		log.Printf("inference cache persistence load skipped: %v", err)
		return
	}
	var deletes []string
	c.mu.Lock()
	for _, stored := range entries {
		if strings.TrimSpace(stored.Key) == "" {
			continue
		}
		entry := &inferenceCacheEntry{
			status:    stored.Status,
			header:    cloneHeader(stored.Header),
			body:      append([]byte(nil), stored.Body...),
			createdAt: stored.CreatedAt,
		}
		if len(entry.body) > c.maxEntryBytes || c.expired(entry) {
			deletes = append(deletes, stored.Key)
			continue
		}
		c.entries[stored.Key] = entry
		c.touchLocked(stored.Key)
		for len(c.entries) > c.maxEntries && len(c.order) > 0 {
			evicted := c.order[0]
			deletes = append(deletes, evicted)
			c.deleteLocked(evicted)
		}
	}
	c.loaded.Store(uint64(len(c.entries)))
	c.mu.Unlock()
	for _, key := range deletes {
		c.deletePersisted(key)
	}
}

func (c *InferenceCache) persistenceCanRead() bool {
	return c != nil && c.storeEnabled && c.store != nil && c.storeMode.canRead()
}

func (c *InferenceCache) persistenceCanWrite() bool {
	return c != nil && c.storeEnabled && c.store != nil && c.storeMode.canWrite()
}

func (c *InferenceCache) savePersisted(entry CacheStoreEntry) {
	if !c.persistenceCanWrite() {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), inferenceCacheStoreOperationTimeout)
	defer cancel()
	if err := c.store.Save(ctx, entry); err != nil {
		log.Printf("inference cache persistence save failed: %v", err)
	}
}

func (c *InferenceCache) deletePersisted(key string) {
	if !c.persistenceCanWrite() || key == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), inferenceCacheStoreOperationTimeout)
	defer cancel()
	if err := c.store.Delete(ctx, key); err != nil {
		log.Printf("inference cache persistence delete failed: %v", err)
	}
}

func (c *InferenceCache) expired(entry *inferenceCacheEntry) bool {
	if entry == nil {
		return true
	}
	age := c.now().Sub(entry.createdAt)
	return age > c.ttl || age > c.maxAge
}

func (c *InferenceCache) touchLocked(key string) {
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

func (c *InferenceCache) deleteLocked(key string) {
	delete(c.entries, key)
	for i, candidate := range c.order {
		if candidate == key {
			c.order = append(c.order[:i], c.order[i+1:]...)
			return
		}
	}
}

func responseFromCacheEntry(entry *inferenceCacheEntry) *http.Response {
	body := append([]byte(nil), entry.body...)
	return &http.Response{
		StatusCode:    entry.status,
		Status:        fmt.Sprintf("%d %s", entry.status, http.StatusText(entry.status)),
		Header:        cloneHeader(entry.header),
		Body:          io.NopCloser(bytes.NewReader(body)),
		ContentLength: int64(len(body)),
	}
}

func cloneHeader(header http.Header) http.Header {
	out := http.Header{}
	for key, values := range header {
		for _, value := range values {
			out.Add(key, value)
		}
	}
	return out
}

func inferenceCacheKey(path string, rawQuery string, backendName string, appKeyScope string, outboundBody []byte) (string, bool) {
	canonicalBody, ok := canonicalJSONBody(outboundBody)
	if !ok {
		return "", false
	}
	scope := strings.TrimSpace(appKeyScope)
	if scope == "" {
		scope = "unauthenticated"
	}
	h := sha256.New()
	for _, part := range []string{
		inferenceCacheKeyVersion,
		normalizeEndpoint(path),
		path,
		rawQuery,
		backendName,
		scope,
		string(canonicalBody),
	} {
		_, _ = h.Write([]byte(part))
		_, _ = h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil)), true
}

func canonicalJSONBody(body []byte) ([]byte, bool) {
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	var payload any
	if err := dec.Decode(&payload); err != nil {
		return nil, false
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		return nil, false
	}
	canonical, err := json.Marshal(normalizeJSON(payload))
	if err != nil {
		return nil, false
	}
	return canonical, true
}

func inferenceCacheableEndpoint(path string) bool {
	switch normalizeEndpoint(path) {
	case "chat", "embeddings", "messages", "responses":
		return true
	default:
		return false
	}
}

func inferenceCacheableResponse(resp *http.Response) bool {
	if resp == nil || resp.StatusCode != http.StatusOK {
		return false
	}
	contentType := strings.ToLower(resp.Header.Get("Content-Type"))
	return strings.Contains(contentType, "json")
}
