package modules

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
)

func TestRewriteSSEChunkWithStateNetworkBoundaries(t *testing.T) {
	pipeline := Pipeline{Modules: []ResponseModule{NewReasoningSwap("reasoning_to_content")}}
	ctx := Context{Path: "/v1/chat/completions", IsStream: true}
	for _, newline := range []string{"\n", "\r\n", "\r"} {
		body := []byte(`data: {"choices":[{"index":0,"delta":{"reasoning_content":"hidden","content":"visible"}}]}` + newline + newline)
		check := func(t *testing.T, chunks [][]byte) {
			t.Helper()
			state := pipeline.NewStreamState()
			defer pipeline.CloseStreamState(state)
			var out []byte
			for _, chunk := range chunks {
				out = append(out, RewriteSSEChunkWithState(chunk, pipeline, ctx, state)...)
			}
			out = append(out, FlushSSEChunkWithState(pipeline, ctx, state)...)
			delta := firstMessage(decodeSSEPayload(t, out), "delta")
			if delta["content"] != "<think>hidden</think>\nvisible" || delta["reasoning_content"] != nil {
				t.Fatalf("delta = %#v", delta)
			}
		}
		for split := 1; split < len(body); split++ {
			t.Run(fmt.Sprintf("newline_%q_split_%d", newline, split), func(t *testing.T) {
				check(t, [][]byte{body[:split], body[split:]})
			})
		}
		t.Run(fmt.Sprintf("newline_%q_byte_at_a_time", newline), func(t *testing.T) {
			check(t, bytes.Split(body, nil))
		})
	}
}

func TestRewriteSSEChunkWithStatePreservesUnchangedEvents(t *testing.T) {
	pipeline := Pipeline{Modules: []ResponseModule{NewReasoningSwap("reasoning_to_content")}}
	ctx := Context{Path: "/v1/chat/completions", IsStream: true}
	body := []byte(": keepalive\r\n\r\nevent: custom\r\nid: 42\r\ndata: {bad json}\r\n\r\ndata: [DONE]\r\n\r\ndata: incomplete\r")
	state := pipeline.NewStreamState()
	var out []byte
	for _, chunk := range bytes.Split(body, nil) {
		out = append(out, RewriteSSEChunkWithState(chunk, pipeline, ctx, state)...)
	}
	out = append(out, FlushSSEChunkWithState(pipeline, ctx, state)...)
	if !bytes.Equal(out, body) {
		t.Fatalf("unchanged stream modified:\ngot:  %q\nwant: %q", out, body)
	}
	if got := FlushSSEChunkWithState(pipeline, ctx, state); len(got) != 0 {
		t.Fatalf("second flush = %q", got)
	}
}

func TestRewriteSSEChunkWithStateFlushesBeforeSplitDone(t *testing.T) {
	pipeline := Pipeline{Modules: []ResponseModule{NewParseTags(ParseTagsConfig{ParseThinking: true})}}
	ctx := Context{Path: "/v1/chat/completions", IsStream: true}
	state := pipeline.NewStreamState()
	_ = RewriteSSEChunkWithState(sseChunk(map[string]any{"choices": []any{map[string]any{"index": 0, "delta": map[string]any{"content": "<thi"}}}}), pipeline, ctx, state)
	if out := RewriteSSEChunkWithState([]byte("data: [DO"), pipeline, ctx, state); len(out) != 0 {
		t.Fatalf("incomplete event emitted: %q", out)
	}
	out := RewriteSSEChunkWithState([]byte("NE]\n\n"), pipeline, ctx, state)
	parts := strings.Split(string(out), "\n\n")
	if len(parts) != 3 || parts[1] != "data: [DONE]" {
		t.Fatalf("flush/DONE ordering: %q", out)
	}
	if got := firstMessage(decodeSSEPayload(t, []byte(parts[0]+"\n\n")), "delta")["content"]; got != "<thi" {
		t.Fatalf("flushed content = %#v", got)
	}
	if got := FlushSSEChunkWithState(pipeline, ctx, state); len(got) != 0 {
		t.Fatalf("duplicate EOF flush = %q", got)
	}
}

func TestRewriteSSEChunkWithStateFlushesUnterminatedEvent(t *testing.T) {
	pipeline := Pipeline{Modules: []ResponseModule{NewParseTags(ParseTagsConfig{ParseThinking: true})}}
	ctx := Context{Path: "/v1/chat/completions", IsStream: true}
	state := pipeline.NewStreamState()
	body := bytes.TrimRight(sseChunk(map[string]any{"choices": []any{map[string]any{"index": 0, "delta": map[string]any{"content": "<thi"}}}}), "\n")
	if out := RewriteSSEChunkWithState(body, pipeline, ctx, state); len(out) != 0 {
		t.Fatalf("incomplete event emitted: %q", out)
	}
	out := FlushSSEChunkWithState(pipeline, ctx, state)
	parts := strings.Split(string(out), "\n\n")
	if len(parts) != 3 {
		t.Fatalf("missing flush event separator: %q", out)
	}
	if got := firstMessage(decodeSSEPayload(t, []byte(parts[1]+"\n\n")), "delta")["content"]; got != "<thi" {
		t.Fatalf("flushed content = %#v", got)
	}
}

func TestPipelineResetDiscardsIncompleteSSEEvent(t *testing.T) {
	pipeline := Pipeline{Modules: []ResponseModule{NewReasoningSwap("reasoning_to_content")}}
	ctx := Context{Path: "/v1/chat/completions", IsStream: true}
	state := pipeline.NewStreamState()
	_ = RewriteSSEChunkWithState([]byte("data: unfinished"), pipeline, ctx, state)
	pipeline.ResetStreamState(state)
	body := []byte("data: [DONE]\n\n")
	if out := RewriteSSEChunkWithState(body, pipeline, ctx, state); !bytes.Equal(out, body) {
		t.Fatalf("stale event after reset: %q", out)
	}
}
