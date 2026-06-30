package logstore

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestFullRequestStoreWritesBase64Payload(t *testing.T) {
	dir := t.TempDir()
	store := &FullRequestStore{Enabled: true, Dir: dir, Retention: time.Hour, now: func() time.Time {
		return time.Date(2026, 6, 16, 1, 2, 3, 0, time.UTC)
	}}
	path, err := store.StorePayload(
		42,
		http.MethodPost,
		"/v1/chat/completions",
		"alpha",
		http.StatusOK,
		http.Header{
			"Authorization": {"Bearer client"},
			"X-Api-Key":     {"client-key"},
			"X-Trace-Id":    {"trace-1"},
		},
		[]byte(`{"model":"alpha"}`),
		http.Header{
			"Content-Type": {"application/json"},
			"Set-Cookie":   {"session=secret"},
		},
		[]byte(`{"choices":[]}`),
		"",
		map[string]string{"tenant": "acme"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(path) != dir {
		t.Fatalf("path = %s", path)
	}
	var record PayloadRecord
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &record); err != nil {
		t.Fatal(err)
	}
	if record.ID != 42 || record.Model != "alpha" || record.Status != http.StatusOK {
		t.Fatalf("record = %#v", record)
	}
	if got := record.RequestHeaders["Authorization"]; len(got) != 1 || got[0] != "[REDACTED]" {
		t.Fatalf("authorization header was not redacted: %#v", record.RequestHeaders)
	}
	if got := record.RequestHeaders["X-Api-Key"]; len(got) != 1 || got[0] != "[REDACTED]" {
		t.Fatalf("x-api-key header was not redacted: %#v", record.RequestHeaders)
	}
	if got := record.ResponseHeaders["Set-Cookie"]; len(got) != 1 || got[0] != "[REDACTED]" {
		t.Fatalf("set-cookie header was not redacted: %#v", record.ResponseHeaders)
	}
	if got := record.RequestHeaders["X-Trace-Id"]; len(got) != 1 || got[0] != "trace-1" {
		t.Fatalf("non-sensitive header was not preserved: %#v", record.RequestHeaders)
	}
	if record.RequiredHeaders["tenant"] != "acme" {
		t.Fatalf("required header attribution = %#v", record.RequiredHeaders)
	}
	requestBody, err := base64.StdEncoding.DecodeString(record.RequestBodyBase64)
	if err != nil {
		t.Fatal(err)
	}
	if string(requestBody) != `{"model":"alpha"}` {
		t.Fatalf("request body = %s", requestBody)
	}
	responseBody, err := base64.StdEncoding.DecodeString(record.ResponseBodyBase64)
	if err != nil {
		t.Fatal(err)
	}
	if string(responseBody) != `{"choices":[]}` {
		t.Fatalf("response body = %s", responseBody)
	}
}

func TestFullRequestStoreWritesParsedResponseBodies(t *testing.T) {
	dir := t.TempDir()
	store := &FullRequestStore{Enabled: true, Dir: dir, Retention: time.Hour, now: func() time.Time {
		return time.Date(2026, 6, 16, 1, 2, 3, 0, time.UTC)
	}}
	path, err := store.StorePayloadWithParsed(
		43,
		http.MethodPost,
		"/v1/chat/completions",
		"alpha",
		http.StatusOK,
		nil,
		[]byte(`{"model":"alpha"}`),
		http.Header{"Content-Type": {"application/json"}},
		[]byte(`{"choices":[{"message":{"content":"visible"}}]}`),
		"",
		nil,
		&ParsedResponseBodies{
			RawUpstreamBody:       []byte(`{"choices":[{"message":{"content":"<think>hidden</think>visible"}}]}`),
			TransformedClientBody: []byte(`{"choices":[{"message":{"content":"visible","reasoning_content":"hidden"}}]}`),
			ContentType:           "application/json",
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	record, err := ReadPayload(path)
	if err != nil {
		t.Fatal(err)
	}
	if record.ParsedResponse == nil {
		t.Fatalf("parsed response was not stored: %#v", record)
	}
	rawBody, err := DecodeBody(record.ParsedResponse.RawUpstreamResponseBodyBase64)
	if err != nil {
		t.Fatal(err)
	}
	if string(rawBody) != `{"choices":[{"message":{"content":"<think>hidden</think>visible"}}]}` {
		t.Fatalf("raw body = %s", rawBody)
	}
	transformedBody, err := DecodeBody(record.ParsedResponse.TransformedClientResponseBodyBase64)
	if err != nil {
		t.Fatal(err)
	}
	if string(transformedBody) != `{"choices":[{"message":{"content":"visible","reasoning_content":"hidden"}}]}` {
		t.Fatalf("transformed body = %s", transformedBody)
	}
	if record.ParsedResponse.ContentType != "application/json" {
		t.Fatalf("content type = %q", record.ParsedResponse.ContentType)
	}
}

func TestFullRequestStoreParsedStreamSidecarsCapAndMetadata(t *testing.T) {
	dir := t.TempDir()
	store := &FullRequestStore{Enabled: true, Dir: dir, Retention: time.Hour, now: func() time.Time {
		return time.Date(2026, 6, 16, 1, 2, 3, 0, time.UTC)
	}}
	sidecars, err := store.NewParsedStreamSidecars(5)
	if err != nil {
		t.Fatal(err)
	}
	sidecars.SetContentType("text/event-stream")
	if _, err := sidecars.RawWriter().Write([]byte("abcdef")); err != nil {
		t.Fatal(err)
	}
	if _, err := sidecars.TransformedWriter().Write([]byte("abc")); err != nil {
		t.Fatal(err)
	}
	if err := sidecars.Close(); err != nil {
		t.Fatal(err)
	}
	meta := sidecars.Metadata()
	if meta == nil {
		t.Fatal("parsed stream metadata missing")
	}
	if !meta.Truncated || !meta.RawUpstreamResponseTruncated || meta.TransformedClientResponseTruncated {
		t.Fatalf("truncation metadata = %#v", meta)
	}
	if meta.MaxBytes != 5 || meta.RawUpstreamResponseBytes != 5 || meta.TransformedClientResponseBytes != 3 {
		t.Fatalf("byte metadata = %#v", meta)
	}
	if meta.ContentType != "text/event-stream" {
		t.Fatalf("content type = %q", meta.ContentType)
	}
	raw, err := os.ReadFile(meta.RawUpstreamResponsePath)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != "abcde" {
		t.Fatalf("raw sidecar = %q", raw)
	}
	transformed, err := os.ReadFile(meta.TransformedClientResponsePath)
	if err != nil {
		t.Fatal(err)
	}
	if string(transformed) != "abc" {
		t.Fatalf("transformed sidecar = %q", transformed)
	}
}

func TestFullRequestStoreCleanupExpired(t *testing.T) {
	dir := t.TempDir()
	oldPath := filepath.Join(dir, "old.json")
	newPath := filepath.Join(dir, "new.json")
	oldRawSidecar := filepath.Join(dir, "old-raw-upstream.sse")
	oldTransformedSidecar := filepath.Join(dir, "old-transformed-client.sse")
	newRawSidecar := filepath.Join(dir, "new-raw-upstream.sse")
	unmanagedSSE := filepath.Join(dir, "debug.sse")
	for _, path := range []string{oldPath, newPath, oldRawSidecar, oldTransformedSidecar, newRawSidecar, unmanagedSSE} {
		if err := os.WriteFile(path, []byte("{}"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Date(2026, 6, 16, 12, 0, 0, 0, time.UTC)
	for _, path := range []string{oldPath, oldRawSidecar, oldTransformedSidecar, unmanagedSSE} {
		if err := os.Chtimes(path, now.Add(-3*time.Hour), now.Add(-3*time.Hour)); err != nil {
			t.Fatal(err)
		}
	}
	for _, path := range []string{newPath, newRawSidecar} {
		if err := os.Chtimes(path, now.Add(-30*time.Minute), now.Add(-30*time.Minute)); err != nil {
			t.Fatal(err)
		}
	}
	store := &FullRequestStore{Enabled: true, Dir: dir, Retention: time.Hour, now: func() time.Time { return now }}
	deleted, err := store.CleanupExpired()
	if err != nil {
		t.Fatal(err)
	}
	if deleted != 3 {
		t.Fatalf("deleted = %d", deleted)
	}
	for _, path := range []string{oldPath, oldRawSidecar, oldTransformedSidecar} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("old managed file still exists or unexpected error for %s: %v", path, err)
		}
	}
	for _, path := range []string{newPath, newRawSidecar, unmanagedSSE} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("kept file missing for %s: %v", path, err)
		}
	}
}
