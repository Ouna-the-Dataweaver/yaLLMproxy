package logdb

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/theio/yallmp-proxy/internal/config"
)

func TestStoreInsertAndListRequestMetadata(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "logs.db")
	store, err := NewStore(&config.Config{Database: map[string]any{
		"backend": "sqlite",
		"connection": map[string]any{
			"sqlite": map[string]any{"path": dbPath},
		},
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	expiresAt := time.Date(2026, 6, 16, 12, 0, 0, 0, time.UTC)
	traceID := strings.Repeat("a", 32)
	spanID := strings.Repeat("b", 16)
	id, err := store.Insert(context.Background(), Record{
		RequestTime:   time.Date(2026, 6, 16, 11, 0, 0, 0, time.UTC),
		ModelName:     "alpha",
		IsStream:      false,
		Path:          "/v1/chat/completions",
		Method:        "POST",
		Query:         "trace=1",
		Outcome:       "success",
		DurationMS:    1500,
		StopReason:    "stop",
		AppKeyID:      "key-1",
		BackendName:   "node-a",
		BackendStatus: 200,
		TraceID:       traceID,
		SpanID:        spanID,
		UsageStats: map[string]any{
			"prompt_tokens":     10,
			"completion_tokens": 20,
			"total_tokens":      30,
			"prompt_tokens_details": map[string]any{
				"cached_tokens": 2,
			},
			"completion_tokens_details": map[string]any{
				"reasoning_tokens": 4,
			},
		},
		FullRequestPath:      "/tmp/request.json",
		FullRequestExpiresAt: &expiresAt,
	})
	if err != nil {
		t.Fatal(err)
	}
	if id == "" {
		t.Fatal("missing generated id")
	}

	records, err := store.List(context.Background(), 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 {
		t.Fatalf("records = %#v", records)
	}
	got := records[0]
	if got.ID != id || got.ModelName != "alpha" || got.RequestPath != "/v1/chat/completions?trace=1" {
		t.Fatalf("record = %#v", got)
	}
	if got.TraceID != traceID || got.SpanID != spanID {
		t.Fatalf("trace context = %q/%q, want %q/%q", got.TraceID, got.SpanID, traceID, spanID)
	}
	if got.DurationSeconds != 1.5 {
		t.Fatalf("duration seconds = %v", got.DurationSeconds)
	}
	if got.UsageStats["prompt_tokens"] != 10 || got.UsageStats["total_tokens"] != 30 {
		t.Fatalf("usage = %#v", got.UsageStats)
	}
	if got.UsageStats["prompt_tokens_details"].(map[string]any)["cached_tokens"] != 2 {
		t.Fatalf("usage details = %#v", got.UsageStats)
	}
	if got.FullRequestPath != "/tmp/request.json" || got.FullRequestExpiresAt == nil {
		t.Fatalf("payload link = %#v expires=%v", got.FullRequestPath, got.FullRequestExpiresAt)
	}
}

func TestStoreMigratesTraceColumnsIdempotently(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "logs.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`CREATE TABLE request_metadata (
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
	)`)
	if closeErr := db.Close(); closeErr != nil && err == nil {
		err = closeErr
	}
	if err != nil {
		t.Fatal(err)
	}

	store, err := NewStore(&config.Config{Database: map[string]any{
		"backend": "sqlite",
		"connection": map[string]any{
			"sqlite": map[string]any{"path": dbPath},
		},
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.Initialize(context.Background()); err != nil {
		t.Fatal(err)
	}
	columns, err := store.requestMetadataColumns(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !columns["trace_id"] || !columns["span_id"] {
		t.Fatalf("missing migrated trace columns: %#v", columns)
	}

	traceID := strings.Repeat("c", 32)
	spanID := strings.Repeat("d", 16)
	id, err := store.Insert(context.Background(), Record{
		ModelName: "migrated",
		Path:      "/v1/chat/completions",
		Method:    "POST",
		Outcome:   "success",
		TraceID:   traceID,
		SpanID:    spanID,
	})
	if err != nil {
		t.Fatal(err)
	}
	got, ok, err := store.Get(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if !ok || got.TraceID != traceID || got.SpanID != spanID {
		t.Fatalf("migrated record ok=%v record=%#v", ok, got)
	}
}

func TestStorePersistsAttemptTrail(t *testing.T) {
	store, err := NewStore(&config.Config{Database: map[string]any{
		"backend": "sqlite",
		"connection": map[string]any{
			"sqlite": map[string]any{"path": filepath.Join(t.TempDir(), "logs.db")},
		},
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	id, err := store.Insert(context.Background(), Record{
		RequestTime: time.Date(2026, 6, 16, 11, 0, 0, 0, time.UTC),
		ModelName:   "alpha",
		Path:        "/v1/chat/completions",
		Method:      "POST",
		Outcome:     "success",
		DurationMS:  25,
		AttemptTrail: []Attempt{
			{AttemptIndex: 1, BackendName: "primary", RetryIndex: 0, Status: 503, Retryable: true, Outcome: "retry", DurationMS: 12, Error: "primary returned status 503"},
			{AttemptIndex: 2, BackendName: "secondary", RetryIndex: 0, Status: 200, Retryable: false, Outcome: "final", DurationMS: 8},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	got, ok, err := store.Get(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("missing record")
	}
	if len(got.AttemptTrail) != 2 {
		t.Fatalf("attempt trail = %#v", got.AttemptTrail)
	}
	if got.AttemptTrail[0].AttemptIndex != 1 || got.AttemptTrail[0].BackendName != "primary" || got.AttemptTrail[0].Outcome != "retry" || !got.AttemptTrail[0].Retryable {
		t.Fatalf("first attempt = %#v", got.AttemptTrail[0])
	}
	if got.AttemptTrail[1].AttemptIndex != 2 || got.AttemptTrail[1].BackendName != "secondary" || got.AttemptTrail[1].Outcome != "final" || got.AttemptTrail[1].Retryable {
		t.Fatalf("second attempt = %#v", got.AttemptTrail[1])
	}
}

func TestExtractUsageAndOutcome(t *testing.T) {
	usage, stopReason, isToolCall := ExtractUsageAndOutcome([]byte(`{
		"choices": [{"finish_reason": "tool_calls", "message": {"tool_calls": [{"id": "call_1"}]}}],
		"usage": {
			"prompt_tokens": 5,
			"completion_tokens": 7,
			"total_tokens": 12
		}
	}`))
	if stopReason != "tool_calls" || !isToolCall {
		t.Fatalf("stop=%q tool=%v", stopReason, isToolCall)
	}
	if usage["prompt_tokens"] != float64(5) || usage["total_tokens"] != float64(12) {
		t.Fatalf("usage = %#v", usage)
	}
}

func TestStoreMapsResponsesUsageColumns(t *testing.T) {
	store, err := NewStore(&config.Config{Database: map[string]any{
		"backend": "sqlite",
		"connection": map[string]any{
			"sqlite": map[string]any{"path": filepath.Join(t.TempDir(), "logs.db")},
		},
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	id, err := store.Insert(context.Background(), Record{
		ModelName: "alpha",
		Outcome:   "success",
		UsageStats: map[string]any{
			"input_tokens":  17,
			"output_tokens": 19,
			"total_tokens":  36,
			"output_tokens_details": map[string]any{
				"reasoning_tokens": 5,
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	got, ok, err := store.Get(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("missing record")
	}
	if got.UsageStats["prompt_tokens"] != 17 || got.UsageStats["completion_tokens"] != 19 || got.UsageStats["total_tokens"] != 36 {
		t.Fatalf("usage = %#v", got.UsageStats)
	}
	details := got.UsageStats["completion_tokens_details"].(map[string]any)
	if details["reasoning_tokens"] != 5 {
		t.Fatalf("usage details = %#v", got.UsageStats)
	}
}

func TestTokenUsageByQuotaScope(t *testing.T) {
	store, err := NewStore(&config.Config{Database: map[string]any{
		"backend": "sqlite",
		"connection": map[string]any{
			"sqlite": map[string]any{"path": filepath.Join(t.TempDir(), "logs.db")},
		},
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	records := []Record{
		{ModelName: "alpha", Outcome: "success", AppKeyID: "key-1", UsageStats: map[string]any{"total_tokens": 10}},
		{ModelName: "alpha", Outcome: "success", AppKeyID: "key-1", UsageStats: map[string]any{"total_tokens": 7}},
		{ModelName: "beta", Outcome: "success", UsageStats: map[string]any{"total_tokens": 3}},
		{ModelName: "alpha", Outcome: "error", AppKeyID: "key-1", UsageStats: map[string]any{"total_tokens": 99}},
	}
	for _, record := range records {
		if _, err := store.Insert(context.Background(), record); err != nil {
			t.Fatal(err)
		}
	}
	usage, err := store.TokenUsageByQuotaScope(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]int{}
	for _, item := range usage {
		got[item.AppKeyID+"::"+item.ModelName] = item.TotalTokens
	}
	if got["key-1::alpha"] != 17 {
		t.Fatalf("key alpha usage = %#v", got)
	}
	if got["::beta"] != 3 {
		t.Fatalf("unauth beta usage = %#v", got)
	}
}

func TestTokenUsageByQuotaScopeSince(t *testing.T) {
	store, err := NewStore(&config.Config{Database: map[string]any{
		"backend": "sqlite",
		"connection": map[string]any{
			"sqlite": map[string]any{"path": filepath.Join(t.TempDir(), "logs.db")},
		},
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	boundary := time.Date(2026, time.June, 27, 0, 0, 0, 0, time.UTC)
	records := []Record{
		{RequestTime: boundary.Add(-time.Nanosecond), ModelName: "alpha", Outcome: "success", AppKeyID: "key-1", UsageStats: map[string]any{"total_tokens": 10}},
		{RequestTime: boundary, ModelName: "alpha", Outcome: "success", AppKeyID: "key-1", UsageStats: map[string]any{"total_tokens": 7}},
		{RequestTime: boundary.Add(time.Hour), ModelName: "alpha", Outcome: "success", AppKeyID: "key-1", UsageStats: map[string]any{"total_tokens": 3}},
		{RequestTime: boundary.Add(time.Hour), ModelName: "alpha", Outcome: "error", AppKeyID: "key-1", UsageStats: map[string]any{"total_tokens": 99}},
	}
	for _, record := range records {
		if _, err := store.Insert(context.Background(), record); err != nil {
			t.Fatal(err)
		}
	}

	usage, err := store.TokenUsageByQuotaScopeSince(context.Background(), boundary)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]int{}
	for _, item := range usage {
		got[item.AppKeyID+"::"+item.ModelName] = item.TotalTokens
	}
	if got["key-1::alpha"] != 10 {
		t.Fatalf("usage since boundary = %d, want 10; all=%#v", got["key-1::alpha"], got)
	}
}

func TestTokenUsageByBackend(t *testing.T) {
	store, err := NewStore(&config.Config{Database: map[string]any{
		"backend": "sqlite",
		"connection": map[string]any{
			"sqlite": map[string]any{"path": filepath.Join(t.TempDir(), "logs.db")},
		},
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	records := []Record{
		{ModelName: "alpha", Outcome: "success", BackendName: "n1", UsageStats: map[string]any{"total_tokens": 10}},
		{ModelName: "alpha", Outcome: "success", BackendName: "n1", UsageStats: map[string]any{"total_tokens": 7}},
		{ModelName: "beta", Outcome: "success", BackendName: "n2", UsageStats: map[string]any{"total_tokens": 3}},
		{ModelName: "alpha", Outcome: "error", BackendName: "n1", UsageStats: map[string]any{"total_tokens": 99}},
		{ModelName: "alpha", Outcome: "success", UsageStats: map[string]any{"total_tokens": 5}},
	}
	for _, record := range records {
		if _, err := store.Insert(context.Background(), record); err != nil {
			t.Fatal(err)
		}
	}
	usage, err := store.TokenUsageByBackend(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]int{}
	for _, item := range usage {
		got[item.BackendName] = item.TotalTokens
	}
	if got["n1"] != 17 {
		t.Fatalf("n1 usage = %#v", got)
	}
	if got["n2"] != 3 {
		t.Fatalf("n2 usage = %#v", got)
	}
	if _, ok := got[""]; ok {
		t.Fatalf("empty backend was included: %#v", got)
	}
}

func TestTokenUsageByBackendSince(t *testing.T) {
	store, err := NewStore(&config.Config{Database: map[string]any{
		"backend": "sqlite",
		"connection": map[string]any{
			"sqlite": map[string]any{"path": filepath.Join(t.TempDir(), "logs.db")},
		},
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	boundary := time.Date(2026, time.June, 27, 0, 0, 0, 0, time.UTC)
	records := []Record{
		{RequestTime: boundary.Add(-time.Nanosecond), ModelName: "alpha", Outcome: "success", BackendName: "n1", UsageStats: map[string]any{"total_tokens": 10}},
		{RequestTime: boundary, ModelName: "alpha", Outcome: "success", BackendName: "n1", UsageStats: map[string]any{"total_tokens": 7}},
		{RequestTime: boundary.Add(time.Hour), ModelName: "alpha", Outcome: "success", BackendName: "n1", UsageStats: map[string]any{"total_tokens": 3}},
		{RequestTime: boundary.Add(time.Hour), ModelName: "alpha", Outcome: "error", BackendName: "n1", UsageStats: map[string]any{"total_tokens": 99}},
	}
	for _, record := range records {
		if _, err := store.Insert(context.Background(), record); err != nil {
			t.Fatal(err)
		}
	}

	usage, err := store.TokenUsageByBackendSince(context.Background(), boundary)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]int{}
	for _, item := range usage {
		got[item.BackendName] = item.TotalTokens
	}
	if got["n1"] != 10 {
		t.Fatalf("usage since boundary = %d, want 10; all=%#v", got["n1"], got)
	}
}

func TestResponseStateUpsertAndGet(t *testing.T) {
	store, err := NewStore(&config.Config{Database: map[string]any{
		"backend": "sqlite",
		"connection": map[string]any{
			"sqlite": map[string]any{"path": filepath.Join(t.TempDir(), "logs.db")},
		},
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	createdAt := time.Date(2026, 6, 16, 12, 0, 0, 0, time.UTC)
	record := ResponseStateRecord{
		ID:                 "resp_1",
		PreviousResponseID: "resp_0",
		Model:              "alpha",
		Status:             "completed",
		InputData:          "hello",
		OutputData: []any{map[string]any{
			"type": "message",
			"role": "assistant",
		}},
		FullResponse: map[string]any{
			"id":          "resp_1",
			"status":      "completed",
			"output_text": "hello back",
		},
		Usage:     map[string]any{"total_tokens": float64(7)},
		Metadata:  map[string]any{"trace": "one"},
		CreatedAt: createdAt,
	}
	if err := store.UpsertResponseState(context.Background(), record); err != nil {
		t.Fatal(err)
	}

	got, ok, err := store.GetResponseState(context.Background(), "resp_1")
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("missing response state")
	}
	if got.ID != "resp_1" || got.PreviousResponseID != "resp_0" || got.Model != "alpha" || got.Status != "completed" {
		t.Fatalf("record = %#v", got)
	}
	if got.InputData != "hello" || got.FullResponse["output_text"] != "hello back" || got.Usage["total_tokens"] != float64(7) {
		t.Fatalf("record payload = %#v", got)
	}
	if !got.CreatedAt.Equal(createdAt) {
		t.Fatalf("created_at = %v", got.CreatedAt)
	}

	record.Status = "failed"
	record.FullResponse["status"] = "failed"
	if err := store.UpsertResponseState(context.Background(), record); err != nil {
		t.Fatal(err)
	}
	got, ok, err = store.GetResponseState(context.Background(), "resp_1")
	if err != nil {
		t.Fatal(err)
	}
	if !ok || got.Status != "failed" || got.FullResponse["status"] != "failed" {
		t.Fatalf("updated record = %#v ok=%v", got, ok)
	}

	_, ok, err = store.GetResponseState(context.Background(), "missing")
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("unexpected missing response state")
	}
}

func TestStoreQueryFiltersSearchPaginationAndGet(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(&config.Config{Database: map[string]any{
		"backend": "sqlite",
		"connection": map[string]any{
			"sqlite": map[string]any{"path": filepath.Join(dir, "logs.db")},
		},
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	payloadPath := filepath.Join(dir, "payload.json")
	if err := os.WriteFile(payloadPath, []byte(`{"request_body_base64":"ZmluZC1tZQ=="}`), 0o600); err != nil {
		t.Fatal(err)
	}
	baseTime := time.Date(2026, 6, 16, 10, 0, 0, 0, time.UTC)
	tool := true
	firstID, err := store.Insert(context.Background(), Record{
		RequestTime:     baseTime,
		ModelName:       "gpt-4.1",
		Path:            "/v1/chat/completions",
		Method:          "POST",
		Outcome:         "success",
		DurationMS:      100,
		StopReason:      "stop",
		FullRequestPath: payloadPath,
	})
	if err != nil {
		t.Fatal(err)
	}
	secondID, err := store.Insert(context.Background(), Record{
		RequestTime: baseTime.Add(time.Minute),
		ModelName:   "claude",
		Path:        "/v1/chat/completions",
		Method:      "POST",
		Outcome:     "error",
		DurationMS:  200,
		StopReason:  "tool_calls",
		IsToolCall:  tool,
	})
	if err != nil {
		t.Fatal(err)
	}

	result, err := store.Query(context.Background(), QueryOptions{Limit: 1, Offset: 0})
	if err != nil {
		t.Fatal(err)
	}
	if result.Total != 2 || len(result.Records) != 1 || !result.HasMore {
		t.Fatalf("result = %#v", result)
	}
	if result.Records[0].ID != secondID {
		t.Fatalf("ordering = %#v", result.Records)
	}

	result, err = store.Query(context.Background(), QueryOptions{ModelName: "gpt"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Total != 1 || result.Records[0].ID != firstID {
		t.Fatalf("model filter = %#v", result)
	}

	result, err = store.Query(context.Background(), QueryOptions{Outcome: "error", StopReason: "tool_calls", IsToolCall: &tool})
	if err != nil {
		t.Fatal(err)
	}
	if result.Total != 1 || result.Records[0].ID != secondID {
		t.Fatalf("combined filter = %#v", result)
	}

	result, err = store.Query(context.Background(), QueryOptions{Search: "find-me"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Total != 1 || result.Records[0].ID != firstID {
		t.Fatalf("search = %#v", result)
	}

	got, ok, err := store.Get(context.Background(), firstID)
	if err != nil {
		t.Fatal(err)
	}
	if !ok || got.ID != firstID || got.ModelName != "gpt-4.1" {
		t.Fatalf("get ok=%v record=%#v", ok, got)
	}
	if _, ok, err := store.Get(context.Background(), "00000000-0000-0000-0000-000000000000"); err != nil || ok {
		t.Fatalf("missing get ok=%v err=%v", ok, err)
	}
}

func TestStoreQueryByTraceIDAndListByTraceID(t *testing.T) {
	store, err := NewStore(&config.Config{Database: map[string]any{
		"backend": "sqlite",
		"connection": map[string]any{
			"sqlite": map[string]any{"path": filepath.Join(t.TempDir(), "logs.db")},
		},
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	traceID := strings.Repeat("1", 32)
	otherTraceID := strings.Repeat("2", 32)
	baseTime := time.Date(2026, 6, 27, 12, 0, 0, 0, time.UTC)
	firstID, err := store.Insert(context.Background(), Record{
		RequestTime: baseTime,
		ModelName:   "alpha",
		Outcome:     "success",
		TraceID:     traceID,
		SpanID:      strings.Repeat("a", 16),
		AttemptTrail: []Attempt{
			{AttemptIndex: 1, BackendName: "node-a", Status: 200, Outcome: "final"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	secondID, err := store.Insert(context.Background(), Record{
		RequestTime: baseTime.Add(time.Minute),
		ModelName:   "beta",
		Outcome:     "error",
		TraceID:     traceID,
		SpanID:      strings.Repeat("b", 16),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Insert(context.Background(), Record{
		RequestTime: baseTime.Add(2 * time.Minute),
		ModelName:   "gamma",
		Outcome:     "success",
		TraceID:     otherTraceID,
		SpanID:      strings.Repeat("c", 16),
	}); err != nil {
		t.Fatal(err)
	}

	result, err := store.Query(context.Background(), QueryOptions{TraceID: traceID, Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if result.Total != 2 || len(result.Records) != 1 || !result.HasMore || result.Records[0].ID != secondID {
		t.Fatalf("trace query result = %#v", result)
	}
	if result.Limit != 1 || result.Offset != 0 {
		t.Fatalf("trace query pagination = %#v", result)
	}

	capped, err := store.Query(context.Background(), QueryOptions{TraceID: traceID, Limit: 999})
	if err != nil {
		t.Fatal(err)
	}
	if capped.Limit != 100 || capped.Total != 2 {
		t.Fatalf("trace query cap = %#v", capped)
	}

	records, err := store.ListByTraceID(context.Background(), traceID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 2 || records[0].ID != secondID || records[1].ID != firstID {
		t.Fatalf("trace records = %#v", records)
	}
	if len(records[1].AttemptTrail) != 1 || records[1].AttemptTrail[0].BackendName != "node-a" {
		t.Fatalf("trace attempts = %#v", records)
	}

	unknown, err := store.Query(context.Background(), QueryOptions{TraceID: strings.Repeat("f", 32)})
	if err != nil {
		t.Fatal(err)
	}
	if unknown.Total != 0 || len(unknown.Records) != 0 || unknown.HasMore {
		t.Fatalf("unknown trace query = %#v", unknown)
	}
	unknownRecords, err := store.ListByTraceID(context.Background(), strings.Repeat("f", 32), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(unknownRecords) != 0 {
		t.Fatalf("unknown trace records = %#v", unknownRecords)
	}
}

func TestStoreErrorRecordsAndAnalytics(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(&config.Config{Database: map[string]any{
		"backend": "sqlite",
		"connection": map[string]any{
			"sqlite": map[string]any{"path": filepath.Join(dir, "logs.db")},
		},
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	start := time.Date(2026, 6, 16, 10, 0, 0, 0, time.UTC)
	end := start.Add(time.Hour)
	requestID, err := store.Insert(context.Background(), Record{
		RequestTime: start.Add(10 * time.Minute),
		ModelName:   "model-a",
		Outcome:     "error",
		DurationMS:  100,
		StopReason:  "tool_calls",
		IsToolCall:  true,
		Path:        "/v1/chat/completions",
		Method:      "POST",
	})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if _, err := store.Insert(context.Background(), Record{
			RequestTime: start.Add(time.Duration(20+i) * time.Minute),
			ModelName:   "model-b",
			Outcome:     "success",
			DurationMS:  100,
			StopReason:  "stop",
			Path:        "/v1/chat/completions",
			Method:      "POST",
		}); err != nil {
			t.Fatal(err)
		}
	}
	errorID, err := store.InsertError(context.Background(), ErrorRecord{
		Timestamp:    start.Add(11 * time.Minute),
		ModelName:    "model-a",
		ErrorType:    "backend_error",
		ErrorMessage: "upstream failed",
		HTTPStatus:   502,
		RequestPath:  "/v1/chat/completions",
		RequestLogID: requestID,
		ExtraContext: map[string]any{"retry": 1},
	})
	if err != nil {
		t.Fatal(err)
	}
	if errorID == "" {
		t.Fatal("missing error id")
	}
	errors, err := store.ErrorsForRequest(context.Background(), requestID)
	if err != nil {
		t.Fatal(err)
	}
	if len(errors) != 1 || errors[0].ErrorType != "backend_error" || errors[0].ExtraContext["retry"] != float64(1) {
		t.Fatalf("errors = %#v", errors)
	}

	stopReasons, gotStart, gotEnd, err := store.StopReasonCounts(context.Background(), &start, &end)
	if err != nil {
		t.Fatal(err)
	}
	if !gotStart.Equal(start) || !gotEnd.Equal(end) {
		t.Fatalf("range = %s %s", gotStart, gotEnd)
	}
	reasons := map[string]StopReasonCount{}
	for _, item := range stopReasons {
		reasons[item.Reason] = item
	}
	if reasons["stop"].Count != 2 || reasons["tool_calls"].Count != 1 {
		t.Fatalf("stop reasons = %#v", stopReasons)
	}
	if reasons["stop"].Percentage != 66.67 {
		t.Fatalf("stop percentage = %#v", reasons["stop"])
	}

	rate, err := store.ToolCallRate(context.Background(), &start, &end)
	if err != nil {
		t.Fatal(err)
	}
	if rate.TotalRequests != 3 || rate.ToolCallRequests != 1 || rate.ToolCallRate != 33.33 {
		t.Fatalf("rate = %#v", rate)
	}

	models, _, _, err := store.RequestsPerModelWithStopReasons(context.Background(), &start, &end, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 2 {
		t.Fatalf("models = %#v", models)
	}
	modelMap := map[string]ModelStopReasonStats{}
	for _, item := range models {
		modelMap[item.ModelName] = item
	}
	if modelMap["model-b"].TotalRequests != 2 || modelMap["model-b"].StopReasons["stop"] != 2 {
		t.Fatalf("model-b = %#v", modelMap["model-b"])
	}
	if modelMap["model-a"].StopReasons["tool_calls"] != 1 {
		t.Fatalf("model-a = %#v", modelMap["model-a"])
	}
}

func TestStoreCleanupRequestsAndErrors(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(&config.Config{Database: map[string]any{
		"backend": "sqlite",
		"connection": map[string]any{
			"sqlite": map[string]any{"path": filepath.Join(dir, "logs.db")},
		},
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	now := time.Date(2026, 6, 16, 12, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return now }
	oldRequestID, err := store.Insert(context.Background(), Record{
		RequestTime: now.Add(-10 * 24 * time.Hour),
		ModelName:   "old",
		Path:        "/v1/chat/completions",
		Method:      "POST",
		Outcome:     "success",
		AttemptTrail: []Attempt{
			{AttemptIndex: 1, BackendName: "old-node", RetryIndex: 0, Status: 200, Outcome: "final"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	newRequestID, err := store.Insert(context.Background(), Record{
		RequestTime: now.Add(-2 * 24 * time.Hour),
		ModelName:   "new",
		Path:        "/v1/chat/completions",
		Method:      "POST",
		Outcome:     "success",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.InsertError(context.Background(), ErrorRecord{
		Timestamp:    now.Add(-10 * 24 * time.Hour),
		ErrorType:    "old_error",
		ErrorMessage: "old",
		RequestLogID: oldRequestID,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.InsertError(context.Background(), ErrorRecord{
		Timestamp:    now.Add(-2 * 24 * time.Hour),
		ErrorType:    "new_error",
		ErrorMessage: "new",
		RequestLogID: newRequestID,
	}); err != nil {
		t.Fatal(err)
	}

	dryRun, err := store.Cleanup(context.Background(), CleanupOptions{Requests: true, Errors: true, KeepDays: 7, DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	if dryRun.RequestsMatched != 1 || dryRun.ErrorsMatched != 1 || dryRun.RequestsDeleted != 0 || dryRun.ErrorsDeleted != 0 {
		t.Fatalf("dry run = %#v", dryRun)
	}
	if records, err := store.List(context.Background(), 10, 0); err != nil || len(records) != 2 {
		t.Fatalf("records after dry run = %#v err=%v", records, err)
	}

	result, err := store.Cleanup(context.Background(), CleanupOptions{Requests: true, KeepDays: 7})
	if err != nil {
		t.Fatal(err)
	}
	if result.RequestsMatched != 1 || result.RequestsDeleted != 1 || result.ErrorsMatched != 0 {
		t.Fatalf("request cleanup = %#v", result)
	}
	records, err := store.List(context.Background(), 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 || records[0].ID != newRequestID {
		t.Fatalf("remaining records = %#v", records)
	}
	oldErrors, err := store.ErrorsForRequest(context.Background(), oldRequestID)
	if err != nil {
		t.Fatal(err)
	}
	if len(oldErrors) != 1 {
		t.Fatalf("request-only cleanup removed old errors: %#v", oldErrors)
	}
	oldAttempts, err := store.AttemptsForRequest(context.Background(), oldRequestID)
	if err != nil {
		t.Fatal(err)
	}
	if len(oldAttempts) != 0 {
		t.Fatalf("request cleanup left old attempts: %#v", oldAttempts)
	}

	result, err = store.Cleanup(context.Background(), CleanupOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if result.RequestsMatched != 1 || result.RequestsDeleted != 1 || result.ErrorsMatched != 2 || result.ErrorsDeleted != 2 {
		t.Fatalf("default cleanup = %#v", result)
	}
	records, err = store.List(context.Background(), 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 0 {
		t.Fatalf("records after default cleanup = %#v", records)
	}
}

func TestNewStoreSkipsMissingAndDisabledConfig(t *testing.T) {
	cases := []*config.Config{
		nil,
		{},
		{Database: map[string]any{"enabled": false}},
	}
	for _, cfg := range cases {
		store, err := NewStore(cfg)
		if err != nil {
			t.Fatal(err)
		}
		if store != nil {
			t.Fatalf("store = %#v, want nil", store)
		}
	}
}

func TestNewStoreRejectsUnsupportedBackend(t *testing.T) {
	store, err := NewStore(&config.Config{Database: map[string]any{"backend": "mysql"}})
	if err == nil {
		t.Fatal("expected unsupported backend error")
	}
	if store != nil {
		t.Fatalf("store = %#v, want nil", store)
	}
	if !strings.Contains(err.Error(), "unsupported database backend") {
		t.Fatalf("error = %v", err)
	}
}

func TestPostgresBackendAliasesRequireConnection(t *testing.T) {
	for _, backend := range []string{"postgres", "postgresql"} {
		t.Run(backend, func(t *testing.T) {
			store, err := NewStore(&config.Config{Database: map[string]any{"backend": backend}})
			if err == nil {
				t.Fatal("expected missing connection error")
			}
			if store != nil {
				t.Fatalf("store = %#v, want nil", store)
			}
			if !strings.Contains(err.Error(), "database.connection.postgres") {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestPostgresDataSourceFromFieldsAndRedaction(t *testing.T) {
	dsn, secrets, err := postgresDataSource(map[string]any{
		"connection": map[string]any{
			"postgres": map[string]any{
				"host":     "db.internal",
				"port":     5432,
				"database": "yallmp_logs",
				"user":     "proxy",
				"password": "super-secret",
				"sslmode":  "require",
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"host='db.internal'", "port='5432'", "dbname='yallmp_logs'", "user='proxy'", "password='super-secret'", "sslmode='require'"} {
		if !strings.Contains(dsn, want) {
			t.Fatalf("dsn %q missing %q", dsn, want)
		}
	}
	redacted := redactDatabaseError(errors.New("connect failed: password='super-secret'"), secrets...).Error()
	if strings.Contains(redacted, "super-secret") {
		t.Fatalf("password leaked in error: %s", redacted)
	}
}

func TestPostgresDialectRebindsQuestionPlaceholders(t *testing.T) {
	query := "SELECT '?' AS literal, col = ? AND \"?\" = \"?\" -- ?\n AND other = ? /* ? */ AND third = ?"
	got := postgresDialect.bind(query)
	want := "SELECT '?' AS literal, col = $1 AND \"?\" = \"?\" -- ?\n AND other = $2 /* ? */ AND third = $3"
	if got != want {
		t.Fatalf("bound SQL:\n got: %s\nwant: %s", got, want)
	}
	if postgresDialect.placeholder(3) != "$3" {
		t.Fatalf("postgres placeholder = %q", postgresDialect.placeholder(3))
	}
	if sqliteDialect.bind(query) != query {
		t.Fatal("sqlite dialect should not rewrite placeholders")
	}
	if sqliteDialect.placeholder(3) != "?" {
		t.Fatalf("sqlite placeholder = %q", sqliteDialect.placeholder(3))
	}
}

func TestPostgresIntegrationLogDB(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("YALLMP_TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("YALLMP_TEST_POSTGRES_DSN not set")
	}
	store, err := NewStore(&config.Config{Database: map[string]any{
		"backend": "postgres",
		"connection": map[string]any{
			"postgres": map[string]any{"dsn": dsn},
		},
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	ctx := context.Background()
	id := "test-logdb-" + strings.ReplaceAll(time.Now().UTC().Format("20060102150405.000000000"), ".", "")
	defer cleanupPostgresIntegration(t, store, id)

	now := time.Now().UTC()
	traceID := strings.Repeat("e", 32)
	spanID := strings.Repeat("f", 16)
	if _, err := store.Insert(ctx, Record{
		ID:          id,
		RequestTime: now,
		ModelName:   "pg-integration-model-" + id,
		Path:        "/v1/chat/completions",
		Method:      "POST",
		Outcome:     "success",
		DurationMS:  42,
		AppKeyID:    "pg-integration-key",
		TraceID:     traceID,
		SpanID:      spanID,
		UsageStats:  map[string]any{"total_tokens": 13},
		AttemptTrail: []Attempt{
			{AttemptIndex: 1, BackendName: "pg-node", Status: 200, Outcome: "final"},
		},
	}); err != nil {
		t.Fatal(err)
	}
	got, ok, err := store.Get(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if !ok || got.ID != id || len(got.AttemptTrail) != 1 || got.TotalTokens != 13 || got.TraceID != traceID || got.SpanID != spanID {
		t.Fatalf("record ok=%v record=%#v", ok, got)
	}
	traceResult, err := store.Query(ctx, QueryOptions{TraceID: traceID, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if traceResult.Total != 1 || len(traceResult.Records) != 1 || traceResult.Records[0].ID != id {
		t.Fatalf("postgres trace query = %#v", traceResult)
	}
	traceRecords, err := store.ListByTraceID(ctx, traceID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(traceRecords) != 1 || traceRecords[0].ID != id || len(traceRecords[0].AttemptTrail) != 1 {
		t.Fatalf("postgres trace records = %#v", traceRecords)
	}

	errorID := id + "-error"
	if _, err := store.InsertError(ctx, ErrorRecord{
		ID:           errorID,
		Timestamp:    now,
		ErrorType:    "integration_warning",
		ErrorMessage: "test warning",
		RequestLogID: id,
	}); err != nil {
		t.Fatal(err)
	}
	errors, err := store.ErrorsForRequest(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if len(errors) != 1 || errors[0].ID != errorID {
		t.Fatalf("errors = %#v", errors)
	}

	stateID := id + "-response"
	if err := store.UpsertResponseState(ctx, ResponseStateRecord{
		ID:           stateID,
		Model:        "pg-integration-model",
		Status:       "completed",
		FullResponse: map[string]any{"id": stateID},
		CreatedAt:    now,
	}); err != nil {
		t.Fatal(err)
	}
	state, ok, err := store.GetResponseState(ctx, stateID)
	if err != nil {
		t.Fatal(err)
	}
	if !ok || state.ID != stateID {
		t.Fatalf("state ok=%v state=%#v", ok, state)
	}
}

func cleanupPostgresIntegration(t *testing.T, store *Store, id string) {
	t.Helper()
	ctx := context.Background()
	for _, statement := range []string{
		"DELETE FROM request_attempts WHERE request_id = ?",
		"DELETE FROM error_logs WHERE request_log_id = ? OR id = ?",
		"DELETE FROM request_metadata WHERE id = ?",
		"DELETE FROM response_states WHERE id = ?",
	} {
		args := []any{id}
		if strings.Contains(statement, " OR id = ?") {
			args = append(args, id+"-error")
		}
		if strings.Contains(statement, "response_states") {
			args = []any{id + "-response"}
		}
		if _, err := store.execContext(ctx, statement, args...); err != nil {
			t.Logf("postgres integration cleanup failed for %q: %v", statement, err)
		}
	}
}
