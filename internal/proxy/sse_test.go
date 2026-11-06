package proxy

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
)

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

func TestSSEJSONDecoderNetworkBoundaries(t *testing.T) {
	for _, newline := range []string{"\n", "\r\n", "\r"} {
		t.Run(fmt.Sprintf("newline_%q", newline), func(t *testing.T) {
			body := strings.Join([]string{
				": keepalive", "", "event: chunk", "data: {", `data: "id":1}`, "",
				`data: {"id":2}`, "", "data: [DONE]", "", "",
			}, newline)
			want := []map[string]any{{"id": float64(1)}, {"id": float64(2)}}
			check := func(t *testing.T, chunks []string) {
				t.Helper()
				var decoder SSEJSONDecoder
				var got []map[string]any
				for _, chunk := range chunks {
					got = append(got, decoder.Feed([]byte(chunk))...)
				}
				got = append(got, decoder.Flush()...)
				if !reflect.DeepEqual(got, want) || !decoder.SawDone() {
					t.Fatalf("payloads = %#v, done = %v", got, decoder.SawDone())
				}
			}
			for split := 1; split < len(body); split++ {
				t.Run(fmt.Sprintf("split_%d", split), func(t *testing.T) {
					check(t, []string{body[:split], body[split:]})
				})
			}
			t.Run("byte_at_a_time", func(t *testing.T) {
				check(t, strings.Split(body, ""))
			})
		})
	}
}

func TestSSEDecoderPreservesMultilineEventAcrossCRLF(t *testing.T) {
	var decoder SSEDecoder
	if got := decoder.Feed([]byte("event: chunk\r\ndata: first\r")); len(got) != 0 {
		t.Fatalf("premature events = %#v", got)
	}
	got := decoder.Feed([]byte("\ndata: second\r\n\r\n"))
	want := []SSEEvent{{Data: "first\nsecond", OtherLines: []string{"event: chunk"}}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("events = %#v, want %#v", got, want)
	}
}

func TestSSEDecoderDispatchesCREventWithoutWaitingForNextChunk(t *testing.T) {
	var decoder SSEDecoder
	got := decoder.Feed([]byte("data: first\r\r"))
	if len(got) != 1 || got[0].Data != "first" {
		t.Fatalf("CR-terminated event delayed: %#v", got)
	}
	got = decoder.Feed([]byte("\ndata: second\r\n\r\n"))
	if len(got) != 1 || got[0].Data != "second" {
		t.Fatalf("optional LF produced an extra event: %#v", got)
	}
}
