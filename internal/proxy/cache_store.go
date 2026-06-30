package proxy

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/theio/yallmp-proxy/internal/config"
	_ "modernc.org/sqlite"
)

const inferenceCacheStoreOperationTimeout = 2 * time.Second

type inferenceCachePersistenceMode string

const (
	inferenceCachePersistenceOff       inferenceCachePersistenceMode = "off"
	inferenceCachePersistenceReadWrite inferenceCachePersistenceMode = "read_write"
	inferenceCachePersistenceReadOnly  inferenceCachePersistenceMode = "read_only"
	inferenceCachePersistenceWriteOnly inferenceCachePersistenceMode = "write_only"
)

// CacheStore persists exact inference cache entries. Implementations must treat
// failures as local storage failures; callers keep serving from memory.
type CacheStore interface {
	LoadAll(ctx context.Context) ([]CacheStoreEntry, error)
	Save(ctx context.Context, entry CacheStoreEntry) error
	Delete(ctx context.Context, key string) error
	Close() error
}

type CacheStoreEntry struct {
	Key       string
	Status    int
	Header    http.Header
	Body      []byte
	CreatedAt time.Time
}

type NoopCacheStore struct{}

func (NoopCacheStore) LoadAll(context.Context) ([]CacheStoreEntry, error) {
	return nil, nil
}

func (NoopCacheStore) Save(context.Context, CacheStoreEntry) error {
	return nil
}

func (NoopCacheStore) Delete(context.Context, string) error {
	return nil
}

func (NoopCacheStore) Close() error {
	return nil
}

type SQLiteCacheStore struct {
	db        *sql.DB
	path      string
	now       func() time.Time
	closeOnce sync.Once
	closeErr  error
}

func NewSQLiteCacheStore(path string) (*SQLiteCacheStore, error) {
	resolved, err := resolveInferenceCacheStorePath(path)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(resolved), 0o700); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(resolved, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, err
	}
	if err := file.Close(); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", resolved)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	store := &SQLiteCacheStore{db: db, path: resolved, now: time.Now}
	if err := store.Initialize(context.Background()); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := os.Chmod(resolved, 0o600); err != nil {
		_ = db.Close()
		return nil, err
	}
	return store, nil
}

func resolveInferenceCacheStorePath(path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", errors.New("inference cache persistence path is required")
	}
	if path == ":memory:" {
		return "", errors.New("inference cache persistence requires a file path")
	}
	if filepath.IsAbs(path) {
		return path, nil
	}
	return config.ResolvePath(path), nil
}

func (s *SQLiteCacheStore) Initialize(ctx context.Context) error {
	if s == nil || s.db == nil {
		return nil
	}
	statements := []string{
		`PRAGMA busy_timeout = 5000`,
		`CREATE TABLE IF NOT EXISTS inference_cache_entries (
			cache_key TEXT PRIMARY KEY,
			status INTEGER NOT NULL,
			headers_json BLOB NOT NULL,
			body BLOB NOT NULL,
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_inference_cache_entries_created_at ON inference_cache_entries(created_at)`,
	}
	for _, statement := range statements {
		if _, err := s.db.ExecContext(ctx, statement); err != nil {
			return err
		}
	}
	return nil
}

func (s *SQLiteCacheStore) LoadAll(ctx context.Context) ([]CacheStoreEntry, error) {
	if s == nil || s.db == nil {
		return nil, nil
	}
	rows, err := s.db.QueryContext(ctx, `SELECT cache_key, status, headers_json, body, created_at
		FROM inference_cache_entries
		ORDER BY created_at ASC, cache_key ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var entries []CacheStoreEntry
	for rows.Next() {
		var key string
		var status int
		var headerBytes []byte
		var body []byte
		var createdAtText string
		if err := rows.Scan(&key, &status, &headerBytes, &body, &createdAtText); err != nil {
			return entries, err
		}
		if strings.TrimSpace(key) == "" || status <= 0 {
			continue
		}
		createdAt, err := time.Parse(time.RFC3339Nano, createdAtText)
		if err != nil {
			continue
		}
		header := http.Header{}
		if len(headerBytes) > 0 {
			if err := json.Unmarshal(headerBytes, &header); err != nil {
				continue
			}
		}
		entries = append(entries, CacheStoreEntry{
			Key:       key,
			Status:    status,
			Header:    cloneHeader(header),
			Body:      append([]byte(nil), body...),
			CreatedAt: createdAt,
		})
	}
	return entries, rows.Err()
}

func (s *SQLiteCacheStore) Save(ctx context.Context, entry CacheStoreEntry) error {
	if s == nil || s.db == nil || strings.TrimSpace(entry.Key) == "" {
		return nil
	}
	createdAt := entry.CreatedAt
	if createdAt.IsZero() {
		createdAt = s.now()
	}
	createdAtText := createdAt.UTC().Format(time.RFC3339Nano)
	updatedAtText := s.now().UTC().Format(time.RFC3339Nano)
	headerBytes, err := json.Marshal(cloneHeader(entry.Header))
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO inference_cache_entries
		(cache_key, status, headers_json, body, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(cache_key) DO UPDATE SET
			status = excluded.status,
			headers_json = excluded.headers_json,
			body = excluded.body,
			created_at = excluded.created_at,
			updated_at = excluded.updated_at`,
		entry.Key,
		entry.Status,
		headerBytes,
		append([]byte(nil), entry.Body...),
		createdAtText,
		updatedAtText,
	)
	if err != nil {
		return fmt.Errorf("save inference cache entry: %w", err)
	}
	return nil
}

func (s *SQLiteCacheStore) Delete(ctx context.Context, key string) error {
	if s == nil || s.db == nil || strings.TrimSpace(key) == "" {
		return nil
	}
	if _, err := s.db.ExecContext(ctx, `DELETE FROM inference_cache_entries WHERE cache_key = ?`, key); err != nil {
		return fmt.Errorf("delete inference cache entry: %w", err)
	}
	return nil
}

func (s *SQLiteCacheStore) Close() error {
	if s == nil {
		return nil
	}
	s.closeOnce.Do(func() {
		if s.db != nil {
			s.closeErr = s.db.Close()
		}
	})
	return s.closeErr
}

func normalizeInferenceCachePersistenceMode(mode string) inferenceCachePersistenceMode {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "", "writethrough", "write-through", "write_through", "read_write":
		return inferenceCachePersistenceReadWrite
	case "read-only", "read_only":
		return inferenceCachePersistenceReadOnly
	case "write-only", "write_only":
		return inferenceCachePersistenceWriteOnly
	case "off", "disabled":
		return inferenceCachePersistenceOff
	default:
		return inferenceCachePersistenceOff
	}
}

func (m inferenceCachePersistenceMode) canRead() bool {
	return m == inferenceCachePersistenceReadWrite || m == inferenceCachePersistenceReadOnly
}

func (m inferenceCachePersistenceMode) canWrite() bool {
	return m == inferenceCachePersistenceReadWrite || m == inferenceCachePersistenceWriteOnly
}
