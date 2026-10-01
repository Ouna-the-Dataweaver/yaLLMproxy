package logdb

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/theio/yallmp-proxy/internal/config"
	responsesapi "github.com/theio/yallmp-proxy/internal/responses"
	_ "modernc.org/sqlite"
)

type Store struct {
	db      *sql.DB
	path    string
	enabled bool
	now     func() time.Time
	dialect databaseDialect
}

type Record struct {
	ID                   string         `json:"id"`
	RequestTime          time.Time      `json:"request_time"`
	ModelName            string         `json:"model_name"`
	IsStream             bool           `json:"is_stream"`
	Path                 string         `json:"path"`
	Method               string         `json:"method"`
	Query                string         `json:"query"`
	RequestPath          string         `json:"request_path"`
	Outcome              string         `json:"outcome"`
	DurationMS           int64          `json:"duration_ms"`
	DurationSeconds      float64        `json:"duration_seconds"`
	StopReason           string         `json:"stop_reason,omitempty"`
	IsToolCall           bool           `json:"is_tool_call"`
	AppKeyID             string         `json:"app_key_id,omitempty"`
	BackendName          string         `json:"backend_name,omitempty"`
	BackendStatus        int            `json:"backend_status,omitempty"`
	TraceID              string         `json:"trace_id,omitempty"`
	SpanID               string         `json:"span_id,omitempty"`
	UsageStats           map[string]any `json:"usage_stats,omitempty"`
	PromptTokens         int            `json:"prompt_tokens,omitempty"`
	CompletionTokens     int            `json:"completion_tokens,omitempty"`
	TotalTokens          int            `json:"total_tokens,omitempty"`
	CachedTokens         int            `json:"cached_tokens,omitempty"`
	ReasoningTokens      int            `json:"reasoning_tokens,omitempty"`
	TokensPerSecond      float64        `json:"tokens_per_second,omitempty"`
	WeightedTokens       float64        `json:"weighted_tokens,omitempty"`
	FullRequestPath      string         `json:"full_request_path,omitempty"`
	FullRequestExpiresAt *time.Time     `json:"full_request_expires_at,omitempty"`
	AttemptTrail         []Attempt      `json:"attempt_trail,omitempty"`
	CreatedAt            time.Time      `json:"created_at"`
}

type Attempt struct {
	AttemptIndex int    `json:"attempt_index"`
	BackendName  string `json:"backend_name"`
	RetryIndex   int    `json:"retry_index"`
	Status       int    `json:"status"`
	Retryable    bool   `json:"retryable"`
	Outcome      string `json:"outcome"`
	DurationMS   int64  `json:"duration_ms"`
	Error        string `json:"error,omitempty"`
}

type QueryOptions struct {
	Limit      int
	Offset     int
	ModelName  string
	Outcome    string
	StopReason string
	TraceID    string
	IsToolCall *bool
	StartTime  *time.Time
	EndTime    *time.Time
	Search     string
}

type QueryResult struct {
	Records []Record `json:"logs"`
	Total   int      `json:"total"`
	Limit   int      `json:"limit"`
	Offset  int      `json:"offset"`
	HasMore bool     `json:"has_more"`
}

type ErrorRecord struct {
	ID           string         `json:"id"`
	Timestamp    time.Time      `json:"timestamp"`
	ModelName    string         `json:"model_name,omitempty"`
	ErrorType    string         `json:"error_type"`
	ErrorMessage string         `json:"error_message"`
	BackendName  string         `json:"backend_name,omitempty"`
	HTTPStatus   int            `json:"http_status,omitempty"`
	RequestPath  string         `json:"request_path,omitempty"`
	RequestLogID string         `json:"request_log_id,omitempty"`
	ExtraContext map[string]any `json:"extra_context,omitempty"`
	CreatedAt    time.Time      `json:"created_at"`
}

type StopReasonCount struct {
	Reason     string  `json:"reason"`
	Count      int     `json:"count"`
	Percentage float64 `json:"percentage"`
}

type ToolCallRate struct {
	TotalRequests    int       `json:"total_requests"`
	ToolCallRequests int       `json:"tool_call_requests"`
	ToolCallRate     float64   `json:"tool_call_rate"`
	StartTime        time.Time `json:"start_time"`
	EndTime          time.Time `json:"end_time"`
}

type ModelStopReasonStats struct {
	ModelName     string         `json:"model_name"`
	TotalRequests int            `json:"total_requests"`
	StopReasons   map[string]int `json:"stop_reasons"`
}

type TokenUsageByScope struct {
	AppKeyID    string `json:"app_key_id"`
	ModelName   string `json:"model_name"`
	TotalTokens int    `json:"total_tokens"`
}

type TokenUsageByBackend struct {
	BackendName string `json:"backend_name"`
	TotalTokens int    `json:"total_tokens"`
}

type ResponseStateRecord = responsesapi.PersistentRecord

type CleanupOptions struct {
	Requests bool
	Errors   bool
	KeepDays int
	DryRun   bool
}

type CleanupResult struct {
	RequestsMatched int  `json:"requests_matched"`
	ErrorsMatched   int  `json:"errors_matched"`
	RequestsDeleted int  `json:"requests_deleted"`
	ErrorsDeleted   int  `json:"errors_deleted"`
	DryRun          bool `json:"dry_run"`
}

func NewStore(cfg *config.Config) (*Store, error) {
	if cfg == nil || len(cfg.Database) == 0 {
		return nil, nil
	}
	if enabled, ok := cfg.Database["enabled"]; ok && !config.ParseBool(enabled) {
		return nil, nil
	}
	dialect, err := databaseBackend(cfg.Database)
	if err != nil {
		return nil, err
	}
	var path string
	var dataSource string
	var secrets []string
	switch dialect.name {
	case "sqlite":
		path = sqlitePath(cfg.Database)
		if path == "" {
			path = "logs/yaLLM.db"
		}
		if path != ":memory:" && !filepath.IsAbs(path) {
			path = filepath.Join(config.ProjectRoot(), path)
		}
		if path != ":memory:" {
			if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				return nil, err
			}
		}
		dataSource = path
	case "postgres":
		dataSource, secrets, err = postgresDataSource(cfg.Database)
		if err != nil {
			return nil, err
		}
	default:
		return nil, fmt.Errorf("unsupported database backend %q", dialect.name)
	}
	db, err := sql.Open(dialect.driverName, dataSource)
	if err != nil {
		if dialect.name == "postgres" {
			return nil, fmt.Errorf("open postgres log database: %w", redactDatabaseError(err, secrets...))
		}
		return nil, err
	}
	applyDatabasePoolSettings(db, cfg.Database)
	store := &Store{db: db, path: path, enabled: true, now: time.Now, dialect: dialect}
	if err := store.Initialize(context.Background()); err != nil {
		_ = db.Close()
		if dialect.name == "postgres" {
			return nil, fmt.Errorf("initialize postgres log database: %w", redactDatabaseError(err, secrets...))
		}
		return nil, err
	}
	return store, nil
}

func (s *Store) Initialize(ctx context.Context) error {
	if s == nil || !s.enabled {
		return nil
	}
	statements := []string{
		`CREATE TABLE IF NOT EXISTS request_metadata (
			id TEXT PRIMARY KEY,
			request_time TEXT NOT NULL,
			model_name TEXT NOT NULL,
			is_stream INTEGER NOT NULL DEFAULT 0,
			path TEXT,
			method TEXT,
			query TEXT,
			request_path TEXT,
			outcome TEXT,
			duration_ms INTEGER,
			stop_reason TEXT,
			is_tool_call INTEGER NOT NULL DEFAULT 0,
			app_key_id TEXT,
			backend_name TEXT,
			backend_status INTEGER,
			prompt_tokens INTEGER,
			completion_tokens INTEGER,
			total_tokens INTEGER,
			cached_tokens INTEGER,
			reasoning_tokens INTEGER,
			tokens_per_second REAL,
			weighted_tokens REAL,
			full_request_path TEXT,
			full_request_expires_at TEXT,
			created_at TEXT NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_request_metadata_request_time ON request_metadata(request_time)`,
		`CREATE INDEX IF NOT EXISTS idx_request_metadata_model_name ON request_metadata(model_name)`,
		`CREATE INDEX IF NOT EXISTS idx_request_metadata_outcome ON request_metadata(outcome)`,
		`CREATE INDEX IF NOT EXISTS idx_request_metadata_app_key_id ON request_metadata(app_key_id)`,
		`CREATE INDEX IF NOT EXISTS idx_request_metadata_backend_name ON request_metadata(backend_name)`,
		`CREATE TABLE IF NOT EXISTS request_attempts (
				request_id TEXT NOT NULL,
				attempt_index INTEGER NOT NULL,
			backend_name TEXT NOT NULL,
			retry_index INTEGER NOT NULL DEFAULT 0,
			status INTEGER NOT NULL DEFAULT 0,
			retryable INTEGER NOT NULL DEFAULT 0,
			outcome TEXT NOT NULL,
			duration_ms INTEGER NOT NULL DEFAULT 0,
			error TEXT,
			created_at TEXT NOT NULL,
			PRIMARY KEY (request_id, attempt_index)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_request_attempts_request_id ON request_attempts(request_id)`,
		`CREATE TABLE IF NOT EXISTS error_logs (
			id TEXT PRIMARY KEY,
			timestamp TEXT NOT NULL,
			model_name TEXT,
			error_type TEXT NOT NULL,
			error_message TEXT NOT NULL,
			backend_name TEXT,
			http_status INTEGER,
			request_path TEXT,
			request_log_id TEXT,
			extra_context TEXT,
			created_at TEXT NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_error_logs_timestamp ON error_logs(timestamp)`,
		`CREATE INDEX IF NOT EXISTS idx_error_logs_request_log_id ON error_logs(request_log_id)`,
		`CREATE INDEX IF NOT EXISTS idx_error_logs_error_type ON error_logs(error_type)`,
		`CREATE TABLE IF NOT EXISTS response_states (
			id TEXT PRIMARY KEY,
			previous_response_id TEXT,
			model TEXT NOT NULL,
			status TEXT NOT NULL DEFAULT 'completed',
			input_data TEXT,
			output_data TEXT,
			full_response TEXT,
			usage TEXT,
			response_metadata TEXT,
			expires_at TEXT,
			created_at TEXT NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS ix_response_states_model_created ON response_states(model, created_at)`,
		`CREATE INDEX IF NOT EXISTS ix_response_states_previous ON response_states(previous_response_id)`,
		`CREATE INDEX IF NOT EXISTS ix_response_states_expires_at ON response_states(expires_at)`,
	}
	for _, statement := range statements {
		if _, err := s.execContext(ctx, statement); err != nil {
			return err
		}
	}
	return s.migrateRequestMetadataColumns(ctx)
}

func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

func (s *Store) Enabled() bool {
	return s != nil && s.enabled && s.db != nil
}

func (s *Store) execContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	return s.db.ExecContext(ctx, s.dialect.bind(query), args...)
}

func (s *Store) queryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	return s.db.QueryContext(ctx, s.dialect.bind(query), args...)
}

func (s *Store) queryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	return s.db.QueryRowContext(ctx, s.dialect.bind(query), args...)
}

func txExecContext(ctx context.Context, tx *sql.Tx, dialect databaseDialect, query string, args ...any) (sql.Result, error) {
	return tx.ExecContext(ctx, dialect.bind(query), args...)
}

func txQueryRowContext(ctx context.Context, tx *sql.Tx, dialect databaseDialect, query string, args ...any) *sql.Row {
	return tx.QueryRowContext(ctx, dialect.bind(query), args...)
}

func (s *Store) Insert(ctx context.Context, record Record) (string, error) {
	if !s.Enabled() {
		return "", nil
	}
	if strings.TrimSpace(record.ID) == "" {
		record.ID = uuid.NewString()
	}
	if record.RequestTime.IsZero() {
		record.RequestTime = s.currentTime()
	}
	if record.CreatedAt.IsZero() {
		record.CreatedAt = s.currentTime()
	}
	if record.RequestPath == "" {
		record.RequestPath = record.Path
		if record.Query != "" {
			record.RequestPath += "?" + record.Query
		}
	}
	usage := usageColumns(record.UsageStats)
	record.PromptTokens = usage.promptTokens
	record.CompletionTokens = usage.completionTokens
	record.TotalTokens = usage.totalTokens
	record.CachedTokens = usage.cachedTokens
	record.ReasoningTokens = usage.reasoningTokens
	record.TokensPerSecond = usage.tokensPerSecond
	record.WeightedTokens = usage.weightedTokens
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	_, err = txExecContext(ctx, tx, s.dialect, `INSERT INTO request_metadata (
		id, request_time, model_name, is_stream, path, method, query, request_path,
		outcome, duration_ms, stop_reason, is_tool_call, app_key_id, backend_name,
		backend_status, trace_id, span_id, prompt_tokens, completion_tokens, total_tokens, cached_tokens,
		reasoning_tokens, tokens_per_second, weighted_tokens, full_request_path,
		full_request_expires_at, created_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		record.ID,
		formatTime(record.RequestTime),
		record.ModelName,
		boolInt(record.IsStream),
		record.Path,
		record.Method,
		record.Query,
		record.RequestPath,
		record.Outcome,
		record.DurationMS,
		nullableString(record.StopReason),
		boolInt(record.IsToolCall),
		nullableString(record.AppKeyID),
		nullableString(record.BackendName),
		nullableInt(record.BackendStatus),
		nullableString(record.TraceID),
		nullableString(record.SpanID),
		nullableInt(record.PromptTokens),
		nullableInt(record.CompletionTokens),
		nullableInt(record.TotalTokens),
		nullableInt(record.CachedTokens),
		nullableInt(record.ReasoningTokens),
		nullableFloat(record.TokensPerSecond),
		nullableFloat(record.WeightedTokens),
		nullableString(record.FullRequestPath),
		nullableTime(record.FullRequestExpiresAt),
		formatTime(record.CreatedAt),
	)
	if err != nil {
		return "", err
	}
	if err := insertAttempts(ctx, tx, s.dialect, record.ID, record.AttemptTrail, record.CreatedAt); err != nil {
		return "", err
	}
	if err := tx.Commit(); err != nil {
		return "", err
	}
	return record.ID, nil
}

func insertAttempts(ctx context.Context, tx *sql.Tx, dialect databaseDialect, requestID string, attempts []Attempt, createdAt time.Time) error {
	if len(attempts) == 0 {
		return nil
	}
	for i, attempt := range attempts {
		if attempt.AttemptIndex <= 0 {
			attempt.AttemptIndex = i + 1
		}
		_, err := txExecContext(ctx, tx, dialect, `INSERT INTO request_attempts (
			request_id, attempt_index, backend_name, retry_index, status,
			retryable, outcome, duration_ms, error, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			requestID,
			attempt.AttemptIndex,
			attempt.BackendName,
			attempt.RetryIndex,
			attempt.Status,
			boolInt(attempt.Retryable),
			attempt.Outcome,
			attempt.DurationMS,
			nullableString(attempt.Error),
			formatTime(createdAt),
		)
		if err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) UpsertResponseState(ctx context.Context, record ResponseStateRecord) error {
	if !s.Enabled() {
		return nil
	}
	record.ID = strings.TrimSpace(record.ID)
	if record.ID == "" {
		return fmt.Errorf("response state id is required")
	}
	record.Model = strings.TrimSpace(record.Model)
	if record.Status = strings.TrimSpace(record.Status); record.Status == "" {
		record.Status = "completed"
	}
	if record.CreatedAt.IsZero() {
		record.CreatedAt = s.currentTime()
	}
	inputData, err := jsonText(record.InputData)
	if err != nil {
		return err
	}
	outputData, err := jsonText(record.OutputData)
	if err != nil {
		return err
	}
	fullResponse, err := jsonText(record.FullResponse)
	if err != nil {
		return err
	}
	usage, err := jsonText(record.Usage)
	if err != nil {
		return err
	}
	metadata, err := jsonText(record.Metadata)
	if err != nil {
		return err
	}
	_, err = s.execContext(ctx, `INSERT INTO response_states (
		id, previous_response_id, model, status, input_data, output_data,
		full_response, usage, response_metadata, expires_at, created_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	ON CONFLICT(id) DO UPDATE SET
		previous_response_id = excluded.previous_response_id,
		model = excluded.model,
		status = excluded.status,
		input_data = excluded.input_data,
		output_data = excluded.output_data,
		full_response = excluded.full_response,
		usage = excluded.usage,
		response_metadata = excluded.response_metadata,
		expires_at = excluded.expires_at,
		created_at = excluded.created_at`,
		record.ID,
		nullableString(record.PreviousResponseID),
		record.Model,
		record.Status,
		inputData,
		outputData,
		fullResponse,
		usage,
		metadata,
		nullableTime(record.ExpiresAt),
		formatTime(record.CreatedAt),
	)
	return err
}

func (s *Store) GetResponseState(ctx context.Context, id string) (ResponseStateRecord, bool, error) {
	if !s.Enabled() {
		return ResponseStateRecord{}, false, nil
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return ResponseStateRecord{}, false, nil
	}
	var record ResponseStateRecord
	var previousID, inputData, outputData, fullResponse, usage, metadata, expiresAt sql.NullString
	var createdAt string
	err := s.queryRowContext(ctx, `SELECT
		id, previous_response_id, model, status, input_data, output_data,
		full_response, usage, response_metadata, expires_at, created_at
		FROM response_states WHERE id = ?`, id).Scan(
		&record.ID,
		&previousID,
		&record.Model,
		&record.Status,
		&inputData,
		&outputData,
		&fullResponse,
		&usage,
		&metadata,
		&expiresAt,
		&createdAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return ResponseStateRecord{}, false, nil
	}
	if err != nil {
		return ResponseStateRecord{}, false, err
	}
	record.PreviousResponseID = nullString(previousID)
	record.CreatedAt = parseTime(createdAt)
	if expiresAt.Valid {
		parsed := parseTime(expiresAt.String)
		if !parsed.IsZero() {
			record.ExpiresAt = &parsed
		}
	}
	if err := decodeJSON(inputData, &record.InputData); err != nil {
		return ResponseStateRecord{}, false, err
	}
	if err := decodeJSON(outputData, &record.OutputData); err != nil {
		return ResponseStateRecord{}, false, err
	}
	if err := decodeJSON(fullResponse, &record.FullResponse); err != nil {
		return ResponseStateRecord{}, false, err
	}
	if err := decodeJSON(usage, &record.Usage); err != nil {
		return ResponseStateRecord{}, false, err
	}
	if err := decodeJSON(metadata, &record.Metadata); err != nil {
		return ResponseStateRecord{}, false, err
	}
	return record, true, nil
}

func (s *Store) List(ctx context.Context, limit, offset int) ([]Record, error) {
	result, err := s.Query(ctx, QueryOptions{Limit: limit, Offset: offset})
	if err != nil {
		return nil, err
	}
	return result.Records, nil
}

func (s *Store) Query(ctx context.Context, opts QueryOptions) (QueryResult, error) {
	if !s.Enabled() {
		return QueryResult{}, nil
	}
	limit, offset := normalizePagination(opts.Limit, opts.Offset)
	opts.Limit = limit
	opts.Offset = offset
	where, args := buildWhere(opts)
	selectSQL := `SELECT
		id, request_time, model_name, is_stream, path, method, query, request_path,
		outcome, duration_ms, stop_reason, is_tool_call, app_key_id, backend_name,
		backend_status, trace_id, span_id, prompt_tokens, completion_tokens, total_tokens, cached_tokens,
		reasoning_tokens, tokens_per_second, weighted_tokens, full_request_path,
		full_request_expires_at, created_at
		FROM request_metadata` + where + `
		ORDER BY request_time DESC`
	if strings.TrimSpace(opts.Search) == "" {
		countSQL := `SELECT COUNT(*) FROM request_metadata` + where
		var total int
		if err := s.queryRowContext(ctx, countSQL, args...).Scan(&total); err != nil {
			return QueryResult{}, err
		}
		pagedArgs := append(append([]any{}, args...), limit, offset)
		rows, err := s.queryContext(ctx, selectSQL+` LIMIT ? OFFSET ?`, pagedArgs...)
		if err != nil {
			return QueryResult{}, err
		}
		defer rows.Close()
		records, err := scanRecords(rows)
		if err != nil {
			return QueryResult{}, err
		}
		return QueryResult{Records: records, Total: total, Limit: limit, Offset: offset, HasMore: offset+len(records) < total}, nil
	}
	rows, err := s.queryContext(ctx, selectSQL, args...)
	if err != nil {
		return QueryResult{}, err
	}
	defer rows.Close()
	all, err := scanRecords(rows)
	if err != nil {
		return QueryResult{}, err
	}
	filtered := filterPayloadSearch(all, opts.Search)
	total := len(filtered)
	end := offset + limit
	if offset > total {
		offset = total
	}
	if end > total {
		end = total
	}
	records := filtered[offset:end]
	return QueryResult{Records: records, Total: total, Limit: limit, Offset: offset, HasMore: end < total}, nil
}

func (s *Store) ListByTraceID(ctx context.Context, traceID string, limit int) ([]Record, error) {
	if !s.Enabled() {
		return nil, nil
	}
	traceID = strings.TrimSpace(traceID)
	if traceID == "" {
		return nil, nil
	}
	result, err := s.Query(ctx, QueryOptions{TraceID: traceID, Limit: limit})
	if err != nil {
		return nil, err
	}
	for i := range result.Records {
		attempts, err := s.AttemptsForRequest(ctx, result.Records[i].ID)
		if err != nil {
			return nil, err
		}
		result.Records[i].AttemptTrail = attempts
	}
	return result.Records, nil
}

func (s *Store) Get(ctx context.Context, id string) (Record, bool, error) {
	if !s.Enabled() {
		return Record{}, false, nil
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return Record{}, false, nil
	}
	row := s.queryRowContext(ctx, `SELECT
		id, request_time, model_name, is_stream, path, method, query, request_path,
		outcome, duration_ms, stop_reason, is_tool_call, app_key_id, backend_name,
		backend_status, trace_id, span_id, prompt_tokens, completion_tokens, total_tokens, cached_tokens,
		reasoning_tokens, tokens_per_second, weighted_tokens, full_request_path,
		full_request_expires_at, created_at
		FROM request_metadata
		WHERE id = ?`, id)
	record, err := scanRecord(row)
	if err != nil {
		if err == sql.ErrNoRows {
			return Record{}, false, nil
		}
		return Record{}, false, err
	}
	attempts, err := s.AttemptsForRequest(ctx, id)
	if err != nil {
		return Record{}, false, err
	}
	record.AttemptTrail = attempts
	return record, true, nil
}

func (s *Store) AttemptsForRequest(ctx context.Context, requestID string) ([]Attempt, error) {
	if !s.Enabled() || strings.TrimSpace(requestID) == "" {
		return nil, nil
	}
	rows, err := s.queryContext(ctx, `SELECT
		attempt_index, backend_name, retry_index, status, retryable, outcome,
		duration_ms, error
		FROM request_attempts
		WHERE request_id = ?
		ORDER BY attempt_index ASC`, requestID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]Attempt, 0)
	for rows.Next() {
		attempt, err := scanAttempt(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, attempt)
	}
	return out, rows.Err()
}

func (s *Store) InsertError(ctx context.Context, record ErrorRecord) (string, error) {
	if !s.Enabled() {
		return "", nil
	}
	if strings.TrimSpace(record.ID) == "" {
		record.ID = uuid.NewString()
	}
	if record.Timestamp.IsZero() {
		record.Timestamp = s.currentTime()
	}
	if record.CreatedAt.IsZero() {
		record.CreatedAt = s.currentTime()
	}
	extraJSON := ""
	if len(record.ExtraContext) > 0 {
		buf, err := json.Marshal(record.ExtraContext)
		if err != nil {
			return "", err
		}
		extraJSON = string(buf)
	}
	_, err := s.execContext(ctx, `INSERT INTO error_logs (
		id, timestamp, model_name, error_type, error_message, backend_name,
		http_status, request_path, request_log_id, extra_context, created_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		record.ID,
		formatTime(record.Timestamp),
		nullableString(record.ModelName),
		errorType(record.ErrorType),
		record.ErrorMessage,
		nullableString(record.BackendName),
		nullableInt(record.HTTPStatus),
		nullableString(record.RequestPath),
		nullableString(record.RequestLogID),
		nullableString(extraJSON),
		formatTime(record.CreatedAt),
	)
	if err != nil {
		return "", err
	}
	return record.ID, nil
}

func (s *Store) ErrorsForRequest(ctx context.Context, requestLogID string) ([]ErrorRecord, error) {
	if !s.Enabled() || strings.TrimSpace(requestLogID) == "" {
		return nil, nil
	}
	rows, err := s.queryContext(ctx, `SELECT
		id, timestamp, model_name, error_type, error_message, backend_name,
		http_status, request_path, request_log_id, extra_context, created_at
		FROM error_logs
		WHERE request_log_id = ?
		ORDER BY timestamp ASC`, requestLogID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]ErrorRecord, 0)
	for rows.Next() {
		record, err := scanErrorRecord(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, record)
	}
	return out, rows.Err()
}

func (s *Store) StopReasonCounts(ctx context.Context, startTime, endTime *time.Time) ([]StopReasonCount, time.Time, time.Time, error) {
	if !s.Enabled() {
		return nil, time.Time{}, time.Time{}, nil
	}
	start, end := s.effectiveRange(startTime, endTime)
	var total int
	if err := s.queryRowContext(ctx, `SELECT COUNT(*) FROM request_metadata
		WHERE request_time >= ? AND request_time <= ? AND stop_reason IS NOT NULL AND stop_reason != ''`,
		formatTime(start), formatTime(end),
	).Scan(&total); err != nil {
		return nil, start, end, err
	}
	rows, err := s.queryContext(ctx, `SELECT stop_reason, COUNT(*) FROM request_metadata
		WHERE request_time >= ? AND request_time <= ? AND stop_reason IS NOT NULL AND stop_reason != ''
		GROUP BY stop_reason
		ORDER BY COUNT(*) DESC`, formatTime(start), formatTime(end))
	if err != nil {
		return nil, start, end, err
	}
	defer rows.Close()
	out := make([]StopReasonCount, 0)
	for rows.Next() {
		var reason string
		var count int
		if err := rows.Scan(&reason, &count); err != nil {
			return nil, start, end, err
		}
		percentage := 0.0
		if total > 0 {
			percentage = round2(float64(count) / float64(total) * 100)
		}
		out = append(out, StopReasonCount{Reason: reason, Count: count, Percentage: percentage})
	}
	return out, start, end, rows.Err()
}

func (s *Store) ToolCallRate(ctx context.Context, startTime, endTime *time.Time) (ToolCallRate, error) {
	if !s.Enabled() {
		return ToolCallRate{}, nil
	}
	start, end := s.effectiveRange(startTime, endTime)
	var total int
	if err := s.queryRowContext(ctx, `SELECT COUNT(*) FROM request_metadata
		WHERE request_time >= ? AND request_time <= ?`, formatTime(start), formatTime(end)).Scan(&total); err != nil {
		return ToolCallRate{}, err
	}
	var toolCount int
	if err := s.queryRowContext(ctx, `SELECT COUNT(*) FROM request_metadata
		WHERE request_time >= ? AND request_time <= ? AND is_tool_call = 1`,
		formatTime(start), formatTime(end),
	).Scan(&toolCount); err != nil {
		return ToolCallRate{}, err
	}
	rate := 0.0
	if total > 0 {
		rate = round2(float64(toolCount) / float64(total) * 100)
	}
	return ToolCallRate{TotalRequests: total, ToolCallRequests: toolCount, ToolCallRate: rate, StartTime: start, EndTime: end}, nil
}

func (s *Store) RequestsPerModelWithStopReasons(ctx context.Context, startTime, endTime *time.Time, limit int) ([]ModelStopReasonStats, time.Time, time.Time, error) {
	if !s.Enabled() {
		return nil, time.Time{}, time.Time{}, nil
	}
	start, end := s.effectiveRange(startTime, endTime)
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.queryContext(ctx, `SELECT model_name, COUNT(*) FROM request_metadata
		WHERE request_time >= ? AND request_time <= ?
		GROUP BY model_name
		ORDER BY COUNT(*) DESC
		LIMIT ?`, formatTime(start), formatTime(end), limit)
	if err != nil {
		return nil, start, end, err
	}
	defer rows.Close()
	out := make([]ModelStopReasonStats, 0)
	for rows.Next() {
		var modelName string
		var total int
		if err := rows.Scan(&modelName, &total); err != nil {
			return nil, start, end, err
		}
		stopReasons, err := s.stopReasonCountsForModel(ctx, modelName, start, end)
		if err != nil {
			return nil, start, end, err
		}
		out = append(out, ModelStopReasonStats{ModelName: modelName, TotalRequests: total, StopReasons: stopReasons})
	}
	return out, start, end, rows.Err()
}

func (s *Store) TokenUsageByQuotaScope(ctx context.Context) ([]TokenUsageByScope, error) {
	return s.tokenUsageByQuotaScope(ctx, nil)
}

func (s *Store) TokenUsageByQuotaScopeSince(ctx context.Context, since time.Time) ([]TokenUsageByScope, error) {
	if since.IsZero() {
		return s.TokenUsageByQuotaScope(ctx)
	}
	since = since.UTC()
	return s.tokenUsageByQuotaScope(ctx, &since)
}

func (s *Store) tokenUsageByQuotaScope(ctx context.Context, since *time.Time) ([]TokenUsageByScope, error) {
	if !s.Enabled() {
		return nil, nil
	}
	query := `SELECT
		COALESCE(app_key_id, ''),
		model_name,
		COALESCE(SUM(total_tokens), 0)
		FROM request_metadata
		WHERE total_tokens IS NOT NULL AND total_tokens > 0
			AND (outcome IS NULL OR outcome != 'error')`
	args := []any(nil)
	if since != nil {
		query += ` AND request_time >= ?`
		args = append(args, formatTime(*since))
	}
	query += ` GROUP BY COALESCE(app_key_id, ''), model_name`
	rows, err := s.queryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]TokenUsageByScope, 0)
	for rows.Next() {
		var usage TokenUsageByScope
		if err := rows.Scan(&usage.AppKeyID, &usage.ModelName, &usage.TotalTokens); err != nil {
			return nil, err
		}
		out = append(out, usage)
	}
	return out, rows.Err()
}

func (s *Store) TokenUsageByBackend(ctx context.Context) ([]TokenUsageByBackend, error) {
	return s.tokenUsageByBackend(ctx, nil)
}

func (s *Store) TokenUsageByBackendSince(ctx context.Context, since time.Time) ([]TokenUsageByBackend, error) {
	if since.IsZero() {
		return s.TokenUsageByBackend(ctx)
	}
	since = since.UTC()
	return s.tokenUsageByBackend(ctx, &since)
}

func (s *Store) tokenUsageByBackend(ctx context.Context, since *time.Time) ([]TokenUsageByBackend, error) {
	if !s.Enabled() {
		return nil, nil
	}
	query := `SELECT
		backend_name,
		COALESCE(SUM(total_tokens), 0)
		FROM request_metadata
		WHERE total_tokens IS NOT NULL AND total_tokens > 0
			AND (outcome IS NULL OR outcome != 'error')
			AND backend_name IS NOT NULL AND backend_name != ''`
	args := []any(nil)
	if since != nil {
		query += ` AND request_time >= ?`
		args = append(args, formatTime(*since))
	}
	query += ` GROUP BY backend_name`
	rows, err := s.queryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]TokenUsageByBackend, 0)
	for rows.Next() {
		var usage TokenUsageByBackend
		if err := rows.Scan(&usage.BackendName, &usage.TotalTokens); err != nil {
			return nil, err
		}
		out = append(out, usage)
	}
	return out, rows.Err()
}

func (s *Store) Cleanup(ctx context.Context, opts CleanupOptions) (CleanupResult, error) {
	if !s.Enabled() {
		return CleanupResult{DryRun: opts.DryRun}, nil
	}
	if !opts.Requests && !opts.Errors {
		opts.Requests = true
		opts.Errors = true
	}
	result := CleanupResult{DryRun: opts.DryRun}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return result, err
	}
	defer tx.Rollback()
	if opts.Requests {
		count, deleted, err := cleanupRequests(ctx, tx, s.dialect, opts.KeepDays, opts.DryRun, s.currentTime())
		if err != nil {
			return result, err
		}
		result.RequestsMatched = count
		result.RequestsDeleted = deleted
	}
	if opts.Errors {
		count, deleted, err := cleanupTable(ctx, tx, s.dialect, "error_logs", "timestamp", opts.KeepDays, opts.DryRun, s.currentTime())
		if err != nil {
			return result, err
		}
		result.ErrorsMatched = count
		result.ErrorsDeleted = deleted
	}
	if err := tx.Commit(); err != nil {
		return result, err
	}
	return result, nil
}

func normalizePagination(limit, offset int) (int, int) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	if offset < 0 {
		offset = 0
	}
	return limit, offset
}

func buildWhere(opts QueryOptions) (string, []any) {
	conditions := make([]string, 0)
	args := make([]any, 0)
	if strings.TrimSpace(opts.ModelName) != "" {
		conditions = append(conditions, "LOWER(model_name) LIKE ?")
		args = append(args, "%"+strings.ToLower(strings.TrimSpace(opts.ModelName))+"%")
	}
	if strings.TrimSpace(opts.Outcome) != "" {
		conditions = append(conditions, "outcome = ?")
		args = append(args, strings.TrimSpace(opts.Outcome))
	}
	if strings.TrimSpace(opts.StopReason) != "" {
		conditions = append(conditions, "stop_reason = ?")
		args = append(args, strings.TrimSpace(opts.StopReason))
	}
	if strings.TrimSpace(opts.TraceID) != "" {
		conditions = append(conditions, "trace_id = ?")
		args = append(args, strings.TrimSpace(opts.TraceID))
	}
	if opts.IsToolCall != nil {
		conditions = append(conditions, "is_tool_call = ?")
		args = append(args, boolInt(*opts.IsToolCall))
	}
	if opts.StartTime != nil && !opts.StartTime.IsZero() {
		conditions = append(conditions, "request_time >= ?")
		args = append(args, formatTime(*opts.StartTime))
	}
	if opts.EndTime != nil && !opts.EndTime.IsZero() {
		conditions = append(conditions, "request_time <= ?")
		args = append(args, formatTime(*opts.EndTime))
	}
	if len(conditions) == 0 {
		return "", args
	}
	return " WHERE " + strings.Join(conditions, " AND "), args
}

func scanRecords(rows *sql.Rows) ([]Record, error) {
	records := make([]Record, 0)
	for rows.Next() {
		record, err := scanRecord(rows)
		if err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	return records, rows.Err()
}

func cleanupRequests(ctx context.Context, tx *sql.Tx, dialect databaseDialect, keepDays int, dryRun bool, now time.Time) (int, int, error) {
	where := ""
	args := []any{}
	if keepDays > 0 {
		where = " WHERE request_time < ?"
		args = append(args, formatTime(now.UTC().AddDate(0, 0, -keepDays)))
	}
	var count int
	if err := txQueryRowContext(ctx, tx, dialect, "SELECT COUNT(*) FROM request_metadata"+where, args...).Scan(&count); err != nil {
		return 0, 0, err
	}
	if dryRun || count == 0 {
		return count, 0, nil
	}
	if _, err := txExecContext(ctx, tx, dialect, "DELETE FROM request_attempts WHERE request_id IN (SELECT id FROM request_metadata"+where+")", args...); err != nil {
		return count, 0, err
	}
	result, err := txExecContext(ctx, tx, dialect, "DELETE FROM request_metadata"+where, args...)
	if err != nil {
		return count, 0, err
	}
	deleted, err := result.RowsAffected()
	if err != nil {
		return count, 0, err
	}
	return count, int(deleted), nil
}

func cleanupTable(ctx context.Context, tx *sql.Tx, dialect databaseDialect, tableName string, timestampColumn string, keepDays int, dryRun bool, now time.Time) (int, int, error) {
	switch tableName {
	case "error_logs":
	default:
		return 0, 0, fmt.Errorf("unsupported cleanup table: %s", tableName)
	}
	switch timestampColumn {
	case "request_time", "timestamp":
	default:
		return 0, 0, fmt.Errorf("unsupported cleanup timestamp column: %s", timestampColumn)
	}
	where := ""
	args := []any{}
	if keepDays > 0 {
		where = " WHERE " + timestampColumn + " < ?"
		args = append(args, formatTime(now.UTC().AddDate(0, 0, -keepDays)))
	}
	var count int
	if err := txQueryRowContext(ctx, tx, dialect, "SELECT COUNT(*) FROM "+tableName+where, args...).Scan(&count); err != nil {
		return 0, 0, err
	}
	if dryRun || count == 0 {
		return count, 0, nil
	}
	result, err := txExecContext(ctx, tx, dialect, "DELETE FROM "+tableName+where, args...)
	if err != nil {
		return count, 0, err
	}
	deleted, err := result.RowsAffected()
	if err != nil {
		return count, 0, err
	}
	return count, int(deleted), nil
}

func scanErrorRecord(rows scanner) (ErrorRecord, error) {
	var record ErrorRecord
	var timestamp, createdAt string
	var modelName, backendName, requestPath, requestLogID, extraContext sql.NullString
	var httpStatus sql.NullInt64
	if err := rows.Scan(
		&record.ID,
		&timestamp,
		&modelName,
		&record.ErrorType,
		&record.ErrorMessage,
		&backendName,
		&httpStatus,
		&requestPath,
		&requestLogID,
		&extraContext,
		&createdAt,
	); err != nil {
		return ErrorRecord{}, err
	}
	record.Timestamp = parseTime(timestamp)
	record.CreatedAt = parseTime(createdAt)
	record.ModelName = nullString(modelName)
	record.BackendName = nullString(backendName)
	record.HTTPStatus = intFromNull(httpStatus)
	record.RequestPath = nullString(requestPath)
	record.RequestLogID = nullString(requestLogID)
	if extraContext.Valid && strings.TrimSpace(extraContext.String) != "" {
		_ = json.Unmarshal([]byte(extraContext.String), &record.ExtraContext)
	}
	return record, nil
}

func (s *Store) stopReasonCountsForModel(ctx context.Context, modelName string, start, end time.Time) (map[string]int, error) {
	rows, err := s.queryContext(ctx, `SELECT stop_reason, COUNT(*) FROM request_metadata
		WHERE model_name = ? AND request_time >= ? AND request_time <= ?
			AND stop_reason IS NOT NULL AND stop_reason != ''
		GROUP BY stop_reason`, modelName, formatTime(start), formatTime(end))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var reason string
		var count int
		if err := rows.Scan(&reason, &count); err != nil {
			return nil, err
		}
		out[reason] = count
	}
	return out, rows.Err()
}

func (s *Store) effectiveRange(startTime, endTime *time.Time) (time.Time, time.Time) {
	end := s.currentTime().UTC()
	start := end.Add(-24 * time.Hour)
	if startTime != nil && !startTime.IsZero() {
		start = startTime.UTC()
	}
	if endTime != nil && !endTime.IsZero() {
		end = endTime.UTC()
	}
	return start, end
}

func round2(value float64) float64 {
	return math.Round(value*100) / 100
}

func filterPayloadSearch(records []Record, search string) []Record {
	search = strings.ToLower(strings.TrimSpace(search))
	if search == "" {
		return records
	}
	filtered := make([]Record, 0)
	for _, record := range records {
		if strings.Contains(strings.ToLower(record.ModelName), search) ||
			strings.Contains(strings.ToLower(record.Path), search) ||
			strings.Contains(strings.ToLower(record.RequestPath), search) ||
			strings.Contains(strings.ToLower(record.Outcome), search) {
			filtered = append(filtered, record)
			continue
		}
		if record.FullRequestPath == "" {
			continue
		}
		raw, err := os.ReadFile(record.FullRequestPath)
		if err == nil && strings.Contains(strings.ToLower(string(raw)), search) {
			filtered = append(filtered, record)
			continue
		}
		if err == nil && payloadBodyContains(raw, search) {
			filtered = append(filtered, record)
		}
	}
	return filtered
}

func payloadBodyContains(raw []byte, search string) bool {
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		return false
	}
	for _, key := range []string{"request_body_base64", "response_body_base64"} {
		encoded := cleanString(payload[key])
		if encoded == "" {
			continue
		}
		decoded, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil {
			continue
		}
		if strings.Contains(strings.ToLower(string(decoded)), search) {
			return true
		}
	}
	return false
}

func sqlitePath(raw map[string]any) string {
	connection, ok := toMap(raw["connection"])
	if !ok {
		return cleanString(raw["path"])
	}
	sqlite, ok := toMap(connection["sqlite"])
	if !ok {
		return ""
	}
	return cleanString(sqlite["path"])
}

type scanner interface {
	Scan(dest ...any) error
}

func scanRecord(rows scanner) (Record, error) {
	var record Record
	var requestTime, createdAt string
	var expiresAt sql.NullString
	var path, method, query, requestPath, outcome sql.NullString
	var stopReason, appKeyID, backendName, traceID, spanID, fullRequestPath sql.NullString
	var backendStatus, promptTokens, completionTokens, totalTokens, cachedTokens, reasoningTokens sql.NullInt64
	var tokensPerSecond, weightedTokens sql.NullFloat64
	var isStream, isToolCall int
	if err := rows.Scan(
		&record.ID,
		&requestTime,
		&record.ModelName,
		&isStream,
		&path,
		&method,
		&query,
		&requestPath,
		&outcome,
		&record.DurationMS,
		&stopReason,
		&isToolCall,
		&appKeyID,
		&backendName,
		&backendStatus,
		&traceID,
		&spanID,
		&promptTokens,
		&completionTokens,
		&totalTokens,
		&cachedTokens,
		&reasoningTokens,
		&tokensPerSecond,
		&weightedTokens,
		&fullRequestPath,
		&expiresAt,
		&createdAt,
	); err != nil {
		return Record{}, err
	}
	record.RequestTime = parseTime(requestTime)
	record.CreatedAt = parseTime(createdAt)
	record.IsStream = isStream != 0
	record.IsToolCall = isToolCall != 0
	record.Path = nullString(path)
	record.Method = nullString(method)
	record.Query = nullString(query)
	record.RequestPath = nullString(requestPath)
	record.Outcome = nullString(outcome)
	record.StopReason = nullString(stopReason)
	record.AppKeyID = nullString(appKeyID)
	record.BackendName = nullString(backendName)
	record.BackendStatus = intFromNull(backendStatus)
	record.TraceID = nullString(traceID)
	record.SpanID = nullString(spanID)
	record.PromptTokens = intFromNull(promptTokens)
	record.CompletionTokens = intFromNull(completionTokens)
	record.TotalTokens = intFromNull(totalTokens)
	record.CachedTokens = intFromNull(cachedTokens)
	record.ReasoningTokens = intFromNull(reasoningTokens)
	record.TokensPerSecond = floatFromNull(tokensPerSecond)
	record.WeightedTokens = floatFromNull(weightedTokens)
	record.FullRequestPath = nullString(fullRequestPath)
	if expiresAt.Valid {
		parsed := parseTime(expiresAt.String)
		if !parsed.IsZero() {
			record.FullRequestExpiresAt = &parsed
		}
	}
	record.DurationSeconds = float64(record.DurationMS) / 1000
	record.UsageStats = buildUsage(record)
	return record, nil
}

func scanAttempt(rows scanner) (Attempt, error) {
	var attempt Attempt
	var retryable int
	var errorMessage sql.NullString
	if err := rows.Scan(
		&attempt.AttemptIndex,
		&attempt.BackendName,
		&attempt.RetryIndex,
		&attempt.Status,
		&retryable,
		&attempt.Outcome,
		&attempt.DurationMS,
		&errorMessage,
	); err != nil {
		return Attempt{}, err
	}
	attempt.Retryable = retryable != 0
	attempt.Error = nullString(errorMessage)
	return attempt, nil
}

type usageValues struct {
	promptTokens     int
	completionTokens int
	totalTokens      int
	cachedTokens     int
	reasoningTokens  int
	tokensPerSecond  float64
	weightedTokens   float64
}

func usageColumns(usage map[string]any) usageValues {
	if usage == nil {
		return usageValues{}
	}
	promptDetails, _ := toMap(usage["prompt_tokens_details"])
	completionDetails, _ := toMap(usage["completion_tokens_details"])
	if len(promptDetails) == 0 {
		promptDetails, _ = toMap(usage["input_tokens_details"])
	}
	if len(completionDetails) == 0 {
		completionDetails, _ = toMap(usage["output_tokens_details"])
	}
	return usageValues{
		promptTokens:     firstIntValue(usage["prompt_tokens"], usage["input_tokens"]),
		completionTokens: firstIntValue(usage["completion_tokens"], usage["output_tokens"]),
		totalTokens:      intValue(usage["total_tokens"]),
		cachedTokens:     intValue(promptDetails["cached_tokens"]),
		reasoningTokens:  intValue(completionDetails["reasoning_tokens"]),
		tokensPerSecond:  floatValue(usage["tokens_per_second"]),
		weightedTokens:   floatValue(usage["weighted_tokens"]),
	}
}

func buildUsage(record Record) map[string]any {
	usage := map[string]any{}
	if record.PromptTokens > 0 {
		usage["prompt_tokens"] = record.PromptTokens
	}
	if record.CompletionTokens > 0 {
		usage["completion_tokens"] = record.CompletionTokens
	}
	if record.TotalTokens > 0 {
		usage["total_tokens"] = record.TotalTokens
	}
	if record.CachedTokens > 0 {
		usage["prompt_tokens_details"] = map[string]any{"cached_tokens": record.CachedTokens}
	}
	if record.ReasoningTokens > 0 {
		usage["completion_tokens_details"] = map[string]any{"reasoning_tokens": record.ReasoningTokens}
	}
	if record.TokensPerSecond > 0 {
		usage["tokens_per_second"] = record.TokensPerSecond
	}
	if record.WeightedTokens > 0 {
		usage["weighted_tokens"] = record.WeightedTokens
	}
	if len(usage) == 0 {
		return nil
	}
	return usage
}

func ExtractUsageAndOutcome(body []byte) (map[string]any, string, bool) {
	if len(body) == 0 {
		return nil, "", false
	}
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, "", false
	}
	usage, _ := toMap(payload["usage"])
	stopReason := ""
	isToolCall := false
	if choices, ok := payload["choices"].([]any); ok && len(choices) > 0 {
		if first, ok := toMap(choices[0]); ok {
			stopReason = strings.TrimSpace(fmt.Sprint(first["finish_reason"]))
			if stopReason == "<nil>" {
				stopReason = ""
			}
			if stopReason == "tool_calls" {
				isToolCall = true
			}
			if message, ok := toMap(first["message"]); ok {
				if toolCalls, ok := message["tool_calls"].([]any); ok && len(toolCalls) > 0 {
					isToolCall = true
				}
			}
		}
	}
	if content, ok := payload["content"].([]any); ok {
		for _, raw := range content {
			if block, ok := toMap(raw); ok && cleanString(block["type"]) == "tool_use" {
				isToolCall = true
				break
			}
		}
	}
	if output, ok := payload["output"].([]any); ok {
		for _, raw := range output {
			if item, ok := toMap(raw); ok && cleanString(item["type"]) == "function_call" {
				isToolCall = true
				break
			}
		}
	}
	if stopReason == "" {
		stopReason = cleanString(payload["stop_reason"])
	}
	stopReason = canonicalStopReason(stopReason)
	if isToolCall {
		stopReason = "tool_calls"
	} else if stopReason == "" {
		switch cleanString(payload["status"]) {
		case "completed":
			stopReason = "stop"
		case "incomplete":
			if details, ok := toMap(payload["incomplete_details"]); ok {
				stopReason = canonicalStopReason(cleanString(details["reason"]))
			}
		case "failed":
			if failure, ok := toMap(payload["error"]); ok {
				stopReason = canonicalStopReason(cleanString(failure["code"]))
			}
		}
	}
	return usage, stopReason, isToolCall
}

func canonicalStopReason(reason string) string {
	switch strings.TrimSpace(reason) {
	case "tool_use", "function_call", "tool_call", "tool_calls":
		return "tool_calls"
	case "end_turn", "stop_sequence", "completed", "stop":
		return "stop"
	case "max_tokens", "max_output_tokens", "length":
		return "length"
	default:
		return strings.TrimSpace(reason)
	}
}

func toMap(value any) (map[string]any, bool) {
	typed, ok := value.(map[string]any)
	return typed, ok
}

func jsonText(value any) (any, error) {
	if value == nil {
		return nil, nil
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	return string(raw), nil
}

func decodeJSON(value sql.NullString, target any) error {
	if !value.Valid || strings.TrimSpace(value.String) == "" {
		return nil
	}
	return json.Unmarshal([]byte(value.String), target)
}

func cleanString(value any) string {
	if value == nil {
		return ""
	}
	text := strings.TrimSpace(fmt.Sprint(value))
	if text == "<nil>" {
		return ""
	}
	return text
}

func errorType(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "request_error"
	}
	return value
}

func intValue(value any) int {
	switch typed := value.(type) {
	case int:
		return typed
	case int64:
		return int(typed)
	case float64:
		return int(typed)
	case json.Number:
		i, _ := typed.Int64()
		return int(i)
	default:
		return 0
	}
}

func firstIntValue(values ...any) int {
	for _, value := range values {
		if out := intValue(value); out > 0 {
			return out
		}
	}
	return 0
}

func floatValue(value any) float64 {
	switch typed := value.(type) {
	case float64:
		return typed
	case int:
		return float64(typed)
	case int64:
		return float64(typed)
	case json.Number:
		f, _ := typed.Float64()
		return f
	default:
		return 0
	}
}

func (s *Store) currentTime() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now()
}

func formatTime(value time.Time) string {
	return value.UTC().Format(time.RFC3339Nano)
}

func parseTime(value string) time.Time {
	parsed, _ := time.Parse(time.RFC3339Nano, value)
	return parsed
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func nullableString(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func nullableInt(value int) any {
	if value == 0 {
		return nil
	}
	return value
}

func nullableFloat(value float64) any {
	if value == 0 {
		return nil
	}
	return value
}

func nullableTime(value *time.Time) any {
	if value == nil || value.IsZero() {
		return nil
	}
	return formatTime(*value)
}

func nullString(value sql.NullString) string {
	if value.Valid {
		return value.String
	}
	return ""
}

func intFromNull(value sql.NullInt64) int {
	if value.Valid {
		return int(value.Int64)
	}
	return 0
}

func floatFromNull(value sql.NullFloat64) float64 {
	if value.Valid {
		return value.Float64
	}
	return 0
}
