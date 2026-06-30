package modules

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestStreamJSONRepairDisabledPipelineNoop(t *testing.T) {
	chunk := []byte(`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"q\""}}]},"index":0}]}` + "\n\n")
	out := RewriteSSEChunkWithState(chunk, Pipeline{}, Context{Path: "/v1/chat/completions", IsStream: true}, nil)
	if !bytes.Equal(out, chunk) {
		t.Fatalf("disabled pipeline changed stream:\n%s\n%s", chunk, out)
	}
}

func TestStreamJSONRepairValidToolArgumentsPassUnchanged(t *testing.T) {
	pipeline := streamJSONRepairTestPipeline(StreamJSONRepairConfig{Enabled: true})
	chunk := sseChunk(map[string]any{"choices": []any{map[string]any{
		"index": 0,
		"delta": map[string]any{
			"tool_calls": []any{map[string]any{
				"index": 0,
				"id":    "call_1",
				"type":  "function",
				"function": map[string]any{
					"name":      "lookup",
					"arguments": `{"query":"cats"}`,
				},
			}},
		},
	}}})
	out := RewriteSSEChunkWithState(chunk, pipeline, Context{Path: "/v1/chat/completions", IsStream: true}, pipeline.NewStreamState())
	if !bytes.Equal(out, chunk) {
		t.Fatalf("valid JSON changed:\n%s\n%s", chunk, out)
	}
}

func TestStreamJSONRepairNormalContentStreamUnchanged(t *testing.T) {
	pipeline := streamJSONRepairTestPipeline(StreamJSONRepairConfig{Enabled: true})
	chunk := []byte(`data: {"choices":[{"delta":{"content":"plain text { not tool args"},"index":0}]}` + "\n\n")
	out := RewriteSSEChunkWithState(chunk, pipeline, Context{Path: "/v1/chat/completions", IsStream: true}, pipeline.NewStreamState())
	if !bytes.Equal(out, chunk) {
		t.Fatalf("normal content stream changed:\n%s\n%s", chunk, out)
	}
}

func TestStreamJSONRepairRepairsFragmentedTruncatedToolArguments(t *testing.T) {
	pipeline := streamJSONRepairTestPipeline(StreamJSONRepairConfig{Enabled: true})
	state := pipeline.NewStreamState()
	ctx := Context{Path: "/v1/chat/completions", IsStream: true}
	chunks := [][]byte{
		sseChunk(map[string]any{"choices": []any{map[string]any{"index": 0, "delta": map[string]any{"tool_calls": []any{map[string]any{
			"index": 0,
			"id":    "call_1",
			"type":  "function",
			"function": map[string]any{
				"name":      "lookup",
				"arguments": `{"query"`,
			},
		}}}}}}),
		sseChunk(map[string]any{"choices": []any{map[string]any{"index": 0, "delta": map[string]any{"tool_calls": []any{map[string]any{
			"index":    0,
			"function": map[string]any{"arguments": `:"cats",`},
		}}}}}}),
		sseChunk(map[string]any{"choices": []any{map[string]any{"index": 0, "delta": map[string]any{}, "finish_reason": "tool_calls"}}}),
	}
	var rewritten [][]byte
	for _, chunk := range chunks {
		rewritten = append(rewritten, RewriteSSEChunkWithState(chunk, pipeline, ctx, state))
	}
	args := collectStreamToolArguments(t, rewritten...)
	if args != `{"query":"cats"}` {
		t.Fatalf("arguments = %q", args)
	}
	if !json.Valid([]byte(args)) {
		t.Fatalf("arguments are not valid JSON: %q", args)
	}
}

func TestStreamJSONRepairFragmentedValidToolArgumentsAggregateUnchanged(t *testing.T) {
	pipeline := streamJSONRepairTestPipeline(StreamJSONRepairConfig{Enabled: true})
	state := pipeline.NewStreamState()
	ctx := Context{Path: "/v1/chat/completions", IsStream: true}
	fragments := []string{`{"query"`, `:"cats"`, `,"limit":2}`}
	var rewritten [][]byte
	for _, fragment := range fragments {
		rewritten = append(rewritten, RewriteSSEChunkWithState(sseChunk(map[string]any{"choices": []any{map[string]any{
			"index": 0,
			"delta": map[string]any{"tool_calls": []any{map[string]any{
				"index":    0,
				"function": map[string]any{"arguments": fragment},
			}}},
		}}}), pipeline, ctx, state))
	}
	args := collectStreamToolArguments(t, rewritten...)
	if want := strings.Join(fragments, ""); args != want {
		t.Fatalf("arguments = %q want %q", args, want)
	}
	if !json.Valid([]byte(args)) {
		t.Fatalf("arguments are not valid JSON: %q", args)
	}
}

func TestStreamJSONRepairRepairsUnterminatedStringAndBracketOnFinish(t *testing.T) {
	args := streamJSONRepairArgumentsAfterFinish(t, StreamJSONRepairConfig{Enabled: true}, `{"items":["cats"`)
	if args != `{"items":["cats"]}` {
		t.Fatalf("arguments = %q", args)
	}
	if !json.Valid([]byte(args)) {
		t.Fatalf("arguments are not valid JSON: %q", args)
	}
}

func TestStreamJSONRepairUncertainFailurePolicies(t *testing.T) {
	passthrough := streamJSONRepairArgumentsAfterFinish(t, StreamJSONRepairConfig{Enabled: true}, `{"query":`)
	if passthrough != `{"query":` {
		t.Fatalf("passthrough arguments = %q", passthrough)
	}
	emptyObject := streamJSONRepairArgumentsAfterFinish(t, StreamJSONRepairConfig{Enabled: true, OnFailure: "empty_object"}, `{"query":`)
	if emptyObject != `{}` {
		t.Fatalf("empty_object arguments = %q", emptyObject)
	}
}

func TestStreamJSONRepairFlushesPendingArgumentsBeforeDone(t *testing.T) {
	pipeline := streamJSONRepairTestPipeline(StreamJSONRepairConfig{Enabled: true})
	state := pipeline.NewStreamState()
	ctx := Context{Path: "/v1/chat/completions", IsStream: true}
	first := sseChunk(map[string]any{"choices": []any{map[string]any{"index": 0, "delta": map[string]any{"tool_calls": []any{map[string]any{
		"index": 0,
		"id":    "call_1",
		"type":  "function",
		"function": map[string]any{
			"name":      "lookup",
			"arguments": `{"query":"cats"`,
		},
	}}}}}})
	done := []byte("data: [DONE]\n\n")
	rewrittenFirst := RewriteSSEChunkWithState(first, pipeline, ctx, state)
	rewrittenDone := RewriteSSEChunkWithState(done, pipeline, ctx, state)
	if !strings.Contains(string(rewrittenDone), "data: [DONE]") {
		t.Fatalf("DONE sentinel missing after flush: %s", rewrittenDone)
	}
	args := collectStreamToolArguments(t, rewrittenFirst, rewrittenDone)
	if args != `{"query":"cats"}` {
		t.Fatalf("arguments = %q", args)
	}
}

func TestStreamJSONRepairCapReleasesBufferedArgumentsRaw(t *testing.T) {
	pipeline := streamJSONRepairTestPipeline(StreamJSONRepairConfig{Enabled: true, MaxStreamBufferBytes: 12})
	state := pipeline.NewStreamState()
	ctx := Context{Path: "/v1/chat/completions", IsStream: true}
	chunks := [][]byte{
		sseChunk(map[string]any{"choices": []any{map[string]any{"index": 0, "delta": map[string]any{"tool_calls": []any{map[string]any{
			"index": 0,
			"function": map[string]any{
				"name":      "lookup",
				"arguments": `{"query"`,
			},
		}}}}}}),
		sseChunk(map[string]any{"choices": []any{map[string]any{"index": 0, "delta": map[string]any{"tool_calls": []any{map[string]any{
			"index":    0,
			"function": map[string]any{"arguments": `:"cats"`},
		}}}}}}),
		sseChunk(map[string]any{"choices": []any{map[string]any{"index": 0, "delta": map[string]any{}, "finish_reason": "tool_calls"}}}),
	}
	var rewritten [][]byte
	for _, chunk := range chunks {
		rewritten = append(rewritten, RewriteSSEChunkWithState(chunk, pipeline, ctx, state))
	}
	args := collectStreamToolArguments(t, rewritten...)
	if args != `{"query":"cats"` {
		t.Fatalf("cap should release raw accumulated arguments, got %q", args)
	}
}

func TestStreamJSONRepairAgeReleasesBufferedArgumentsRaw(t *testing.T) {
	pipeline := streamJSONRepairTestPipeline(StreamJSONRepairConfig{
		Enabled:            true,
		MaxStreamBufferAge: time.Second,
	})
	state := pipeline.NewStreamState()
	now := time.Unix(1_700_000_000, 0)
	ctx := Context{Path: "/v1/chat/completions", IsStream: true, Now: func() time.Time { return now }}
	chunks := [][]byte{
		sseChunk(map[string]any{"choices": []any{map[string]any{"index": 0, "delta": map[string]any{"tool_calls": []any{map[string]any{
			"index": 0,
			"function": map[string]any{
				"name":      "lookup",
				"arguments": `{"query"`,
			},
		}}}}}}),
		sseChunk(map[string]any{"choices": []any{map[string]any{"index": 0, "delta": map[string]any{"tool_calls": []any{map[string]any{
			"index":    0,
			"function": map[string]any{"arguments": `:"cats"`},
		}}}}}}),
		sseChunk(map[string]any{"choices": []any{map[string]any{"index": 0, "delta": map[string]any{}, "finish_reason": "tool_calls"}}}),
	}
	rewritten := [][]byte{RewriteSSEChunkWithState(chunks[0], pipeline, ctx, state)}
	now = now.Add(2 * time.Second)
	rewritten = append(rewritten, RewriteSSEChunkWithState(chunks[1], pipeline, ctx, state))
	rewritten = append(rewritten, RewriteSSEChunkWithState(chunks[2], pipeline, ctx, state))
	args := collectStreamToolArguments(t, rewritten...)
	if args != `{"query":"cats"` {
		t.Fatalf("age should release raw accumulated arguments, got %q", args)
	}
	moduleState := state.ModuleStates[0].(*streamJSONRepairState)
	if len(moduleState.calls) != 0 || moduleState.totalBytes != 0 {
		t.Fatalf("pending state after age release = %#v total=%d", moduleState.calls, moduleState.totalBytes)
	}
}

func TestStreamJSONRepairAgeDisabledByDefault(t *testing.T) {
	pipeline := streamJSONRepairTestPipeline(StreamJSONRepairConfig{Enabled: true})
	state := pipeline.NewStreamState()
	now := time.Unix(1_700_000_000, 0)
	ctx := Context{Path: "/v1/chat/completions", IsStream: true, Now: func() time.Time { return now }}
	chunks := [][]byte{
		sseChunk(map[string]any{"choices": []any{map[string]any{"index": 0, "delta": map[string]any{"tool_calls": []any{map[string]any{
			"index":    0,
			"function": map[string]any{"arguments": `{"query"`},
		}}}}}}),
		sseChunk(map[string]any{"choices": []any{map[string]any{"index": 0, "delta": map[string]any{"tool_calls": []any{map[string]any{
			"index":    0,
			"function": map[string]any{"arguments": `:"cats"}`},
		}}}}}}),
	}
	rewritten := [][]byte{RewriteSSEChunkWithState(chunks[0], pipeline, ctx, state)}
	now = now.Add(24 * time.Hour)
	rewritten = append(rewritten, RewriteSSEChunkWithState(chunks[1], pipeline, ctx, state))
	args := collectStreamToolArguments(t, rewritten...)
	if args != `{"query":"cats"}` {
		t.Fatalf("arguments = %q", args)
	}
	if !json.Valid([]byte(args)) {
		t.Fatalf("arguments are not valid JSON: %q", args)
	}
}

func TestStreamJSONRepairLifecycleHooksClearState(t *testing.T) {
	pipeline := streamJSONRepairTestPipeline(StreamJSONRepairConfig{Enabled: true})
	state := pipeline.NewStreamState()
	ctx := Context{Path: "/v1/chat/completions", IsStream: true}
	_ = RewriteSSEChunkWithState(sseChunk(map[string]any{"choices": []any{map[string]any{"index": 0, "delta": map[string]any{"tool_calls": []any{map[string]any{
		"index":    0,
		"function": map[string]any{"arguments": `{"query"`},
	}}}}}}), pipeline, ctx, state)
	moduleState := state.ModuleStates[0].(*streamJSONRepairState)
	if len(moduleState.calls) != 1 || moduleState.totalBytes == 0 {
		t.Fatalf("expected pending state, got calls=%#v total=%d", moduleState.calls, moduleState.totalBytes)
	}

	pipeline.ResetStreamState(state)
	if len(moduleState.calls) != 0 || len(moduleState.bypass) != 0 || moduleState.totalBytes != 0 {
		t.Fatalf("state after reset = calls=%#v bypass=%#v total=%d", moduleState.calls, moduleState.bypass, moduleState.totalBytes)
	}
	pipeline.CloseStreamState(state)
	pipeline.CloseStreamState(state)
	pipeline.CloseStreamState(nil)
}

func TestRepairJSONFragmentConservative(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
		ok   bool
	}{
		{name: "trailing comma object", in: `{"a":1,`, want: `{"a":1}`, ok: true},
		{name: "missing string quote", in: `{"a":"x`, want: `{"a":"x"}`, ok: true},
		{name: "nested array", in: `{"a":[1,2,`, want: `{"a":[1,2]}`, ok: true},
		{name: "missing value", in: `{"a":`, ok: false},
		{name: "arbitrary text", in: `hello`, ok: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := repairJSONFragment(tc.in)
			if ok != tc.ok || got != tc.want {
				t.Fatalf("repairJSONFragment(%q) = %q, %v; want %q, %v", tc.in, got, ok, tc.want, tc.ok)
			}
		})
	}
}

func streamJSONRepairTestPipeline(cfg StreamJSONRepairConfig) Pipeline {
	return Pipeline{Modules: []ResponseModule{NewStreamJSONRepair(cfg)}}
}

func streamJSONRepairArgumentsAfterFinish(t *testing.T, cfg StreamJSONRepairConfig, fragments ...string) string {
	t.Helper()
	pipeline := streamJSONRepairTestPipeline(cfg)
	state := pipeline.NewStreamState()
	ctx := Context{Path: "/v1/chat/completions", IsStream: true}
	var rewritten [][]byte
	for _, fragment := range fragments {
		rewritten = append(rewritten, RewriteSSEChunkWithState(sseChunk(map[string]any{"choices": []any{map[string]any{
			"index": 0,
			"delta": map[string]any{"tool_calls": []any{map[string]any{
				"index":    0,
				"function": map[string]any{"arguments": fragment},
			}}},
		}}}), pipeline, ctx, state))
	}
	rewritten = append(rewritten, RewriteSSEChunkWithState(sseChunk(map[string]any{"choices": []any{map[string]any{
		"index":         0,
		"delta":         map[string]any{},
		"finish_reason": "tool_calls",
	}}}), pipeline, ctx, state))
	return collectStreamToolArguments(t, rewritten...)
}

func collectStreamToolArguments(t *testing.T, chunks ...[]byte) string {
	t.Helper()
	var out strings.Builder
	for _, payload := range decodeStreamJSONRepairSSEPayloads(t, chunks...) {
		choices, _ := payload["choices"].([]any)
		for _, rawChoice := range choices {
			choice, _ := rawChoice.(map[string]any)
			delta, _ := choice["delta"].(map[string]any)
			calls, _ := delta["tool_calls"].([]any)
			for _, rawCall := range calls {
				call, _ := rawCall.(map[string]any)
				function, _ := call["function"].(map[string]any)
				if args, ok := function["arguments"].(string); ok {
					out.WriteString(args)
				}
			}
		}
	}
	return out.String()
}

func decodeStreamJSONRepairSSEPayloads(t *testing.T, chunks ...[]byte) []map[string]any {
	t.Helper()
	var payloads []map[string]any
	for _, chunk := range chunks {
		for _, event := range strings.Split(string(chunk), "\n\n") {
			for _, line := range strings.Split(event, "\n") {
				if !strings.HasPrefix(line, "data:") {
					continue
				}
				raw := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
				if raw == "" || raw == "[DONE]" {
					continue
				}
				var payload map[string]any
				if err := json.Unmarshal([]byte(raw), &payload); err != nil {
					t.Fatalf("invalid SSE JSON %q: %v", raw, err)
				}
				payloads = append(payloads, payload)
			}
		}
	}
	return payloads
}
