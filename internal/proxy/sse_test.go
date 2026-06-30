package proxy

import "testing"

func TestDetectSSEStreamError(t *testing.T) {
	if got := DetectSSEStreamError([]byte(`data: {"id":"ok"}`)); got != "" {
		t.Fatalf("unexpected error: %s", got)
	}
	got := DetectSSEStreamError([]byte(`data: {"error":{"message":"rate limited","type":"rate_limit"}}`))
	if got == "" || got != "SSE stream error: rate limited (type=rate_limit)" {
		t.Fatalf("error = %q", got)
	}
	got = DetectSSEStreamError([]byte(`data: {"type":"error","error":{"message":"fail","http_code":500}}`))
	if got != "SSE stream error: fail (http_code=500)" {
		t.Fatalf("error = %q", got)
	}
}

func TestSSEDecoderBuffersUntilCompleteEvent(t *testing.T) {
	var decoder SSEDecoder
	if events := decoder.Feed([]byte("data: hello")); len(events) != 0 {
		t.Fatalf("events before terminator = %#v", events)
	}
	if got := string(decoder.Flush()); got != "data: hello" {
		t.Fatalf("flush = %q", got)
	}
}

func TestSSEJSONDecoderFlush(t *testing.T) {
	var decoder SSEJSONDecoder
	got := decoder.Feed([]byte("data: {\"id\":1}\n\ndata: {\"id\":2}"))
	if len(got) != 1 || got[0]["id"] != float64(1) {
		t.Fatalf("feed = %#v", got)
	}
	remaining := decoder.Flush()
	if len(remaining) != 1 || remaining[0]["id"] != float64(2) {
		t.Fatalf("flush = %#v", remaining)
	}
}
