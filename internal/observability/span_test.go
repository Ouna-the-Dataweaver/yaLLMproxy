package observability

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestEndpointKindBucketsPaths(t *testing.T) {
	for _, tc := range []struct {
		path string
		want string
	}{
		{path: "/v1/chat/completions", want: "chat"},
		{path: " chat-completions ", want: "chat"},
		{path: "/v2/completions/", want: "chat"},
		{path: "/v1/embeddings", want: "embeddings"},
		{path: "rankingS", want: "rerank"},
		{path: "/v1/anthropic_messages", want: "messages"},
		{path: "/V2/Responses", want: "responses"},
		{path: "/p1/native/path", want: "passthrough"},
		{path: "", want: "unknown"},
		{path: "/v1/raw-secret", want: "other"},
	} {
		t.Run(tc.path, func(t *testing.T) {
			if got := EndpointKind(tc.path); got != tc.want {
				t.Fatalf("EndpointKind(%q) = %q, want %q", tc.path, got, tc.want)
			}
		})
	}
}

func TestKeyScopeBuckets(t *testing.T) {
	for _, tc := range []struct {
		name          string
		authenticated bool
		keyID         string
		groupID       string
		want          string
	}{
		{name: "unauthenticated", want: "unauthenticated"},
		{name: "group", authenticated: true, keyID: "key-a", groupID: " group-a ", want: "group"},
		{name: "key", authenticated: true, keyID: " key-a ", want: "authenticated"},
		{name: "authenticated without ids", authenticated: true, want: "authenticated"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := KeyScope(tc.authenticated, tc.keyID, tc.groupID); got != tc.want {
				t.Fatalf("KeyScope() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestErrorTypeBucketsStatusAndError(t *testing.T) {
	for _, tc := range []struct {
		name   string
		err    error
		status int
		want   string
	}{
		{name: "none", want: ""},
		{name: "status wins", err: errors.New("quota exhausted"), status: 429, want: "rate_limit"},
		{name: "context canceled", err: context.Canceled, want: "context_canceled"},
		{name: "deadline", err: context.DeadlineExceeded, want: "timeout"},
		{name: "rate limit", err: errors.New("upstream rate limit exceeded"), want: "rate_limit"},
		{name: "quota", err: errors.New("quota exhausted"), want: "quota"},
		{name: "queue", err: errors.New("queue is full"), want: "queue"},
		{name: "timeout text", err: errors.New("request timeout"), want: "timeout"},
		{name: "client closed", err: errors.New("client closed stream"), want: "context_canceled"},
		{name: "permission", err: errors.New("access denied"), want: "permission"},
		{name: "not found", err: errors.New("model not defined"), want: "not_found"},
		{name: "unsupported endpoint", err: errors.New("unsupported endpoint /v9"), want: "unsupported_endpoint"},
		{name: "network", err: errors.New("dial tcp: connection refused"), want: "network"},
		{name: "default", err: errors.New("raw upstream failure"), want: "error"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := ErrorType(tc.err, tc.status); got != tc.want {
				t.Fatalf("ErrorType() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestStatusErrorTypeBuckets(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		want   string
	}{
		{name: "zero", status: 0, want: ""},
		{name: "ok", status: 200, want: ""},
		{name: "redirect", status: 399, want: ""},
		{name: "bad request", status: 400, want: "http_4xx"},
		{name: "unauthorized", status: 401, want: "permission"},
		{name: "forbidden", status: 403, want: "permission"},
		{name: "timeout", status: 408, want: "timeout"},
		{name: "rate limit", status: 429, want: "rate_limit"},
		{name: "client closed", status: 499, want: "client_closed"},
		{name: "server error", status: 500, want: "http_5xx"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := StatusErrorType(tc.status); got != tc.want {
				t.Fatalf("StatusErrorType(%d) = %q, want %q", tc.status, got, tc.want)
			}
		})
	}
}

func TestBoundedValueAndNilSpanAreDefensive(t *testing.T) {
	if got := boundedValue("", "fallback"); got != "fallback" {
		t.Fatalf("empty bounded value = %q", got)
	}
	if got := boundedValue(" \t\n ", "fallback"); got != "fallback" {
		t.Fatalf("blank bounded value = %q", got)
	}
	if got := boundedValue(" value ", "fallback"); got != "value" {
		t.Fatalf("trimmed bounded value = %q", got)
	}
	exact := strings.Repeat("a", 128)
	if got := boundedValue(exact, "fallback"); got != exact {
		t.Fatalf("exact-length bounded value changed")
	}
	long := strings.Repeat("b", 129)
	if got := boundedValue(long, "fallback"); got != strings.Repeat("b", 128)+"_truncated" {
		t.Fatalf("long bounded value = %q", got)
	}
	if got := boundedValue("model-名", "fallback"); got != "model-名" {
		t.Fatalf("unicode bounded value = %q", got)
	}

	var span Span
	span.SetKeyScope("group")
	if span.TraceID() != "" || span.SpanID() != "" || span.SpanContext().IsValid() {
		t.Fatalf("nil span context = %q/%q valid=%v", span.TraceID(), span.SpanID(), span.SpanContext().IsValid())
	}
	span.End(SpanResult{Outcome: OutcomeSuccess, StatusCode: 200})
}

func TestSpanSetKeyScopeUpdatesRecordedAttribute(t *testing.T) {
	provider, recorder := NewInMemoryProvider("span-test")
	_, span := provider.StartRequestSpan(context.Background(), RequestAttrs{
		EndpointKind: "chat",
		Model:        "alpha",
		KeyScope:     "authenticated",
	})
	span.SetKeyScope(" group ")
	span.End(SpanResult{Outcome: OutcomeSuccess, StatusCode: 200})

	attrs := observedSpanAttrs(firstObservedSpanByName(recorder.Spans(), "proxy.request"))
	if attrs["key_scope"] != "group" {
		t.Fatalf("key_scope attr = %q, want group; attrs=%#v", attrs["key_scope"], attrs)
	}
}
