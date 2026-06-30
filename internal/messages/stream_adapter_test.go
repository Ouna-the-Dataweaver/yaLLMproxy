package messages

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestStreamAdapterTextUsageAndStop(t *testing.T) {
	adapter := NewStreamAdapter("msg_test", "alpha")
	chunks := []string{
		`data: {"choices":[{"delta":{"role":"assistant"},"index":0}]}` + "\n\n",
		`data: {"choices":[{"delta":{"content":"Hello"},"index":0}]}` + "\n\n",
		`data: {"choices":[{"delta":{"content":" world"},"index":0}]}` + "\n\n",
		`data: {"choices":[{"delta":{},"finish_reason":"stop","index":0}],"usage":{"prompt_tokens":5,"completion_tokens":2,"total_tokens":7}}` + "\n\n",
		"data: [DONE]\n\n",
	}
	var out []byte
	for _, chunk := range chunks {
		out = append(out, adapter.Feed([]byte(chunk))...)
	}
	out = append(out, adapter.Flush()...)
	events := parseMessagesSSEEvents(t, string(out))
	if got := eventNames(events); strings.Join(got, ",") != "message_start,content_block_start,content_block_delta,content_block_delta,content_block_stop,message_delta,message_stop" {
		t.Fatalf("events = %v", got)
	}
	if events[0]["data"].(map[string]any)["message"].(map[string]any)["id"] != "msg_test" {
		t.Fatalf("message_start = %#v", events[0])
	}
	if events[2]["data"].(map[string]any)["delta"].(map[string]any)["text"] != "Hello" {
		t.Fatalf("first delta = %#v", events[2])
	}
	messageDelta := events[len(events)-2]["data"].(map[string]any)
	if messageDelta["delta"].(map[string]any)["stop_reason"] != "end_turn" {
		t.Fatalf("message_delta = %#v", messageDelta)
	}
	if messageDelta["usage"].(map[string]any)["output_tokens"] != float64(2) {
		t.Fatalf("usage = %#v", messageDelta["usage"])
	}
	if adapter.TotalTokens() != 7 {
		t.Fatalf("total tokens = %d", adapter.TotalTokens())
	}
}

func TestStreamAdapterCacheUsageFields(t *testing.T) {
	adapter := NewStreamAdapter("msg_cache", "alpha")
	var out []byte
	out = append(out, adapter.Feed([]byte(`data: {"choices":[{"delta":{"role":"assistant"},"index":0}],"usage":{"prompt_tokens":8,"completion_tokens":0,"total_tokens":8,"prompt_tokens_details":{"cached_tokens":5,"cache_creation_input_tokens":2}}}`+"\n\n"))...)
	out = append(out, adapter.Feed([]byte(`data: {"choices":[{"delta":{"content":"cached"},"index":0}]}`+"\n\n"))...)
	out = append(out, adapter.Feed([]byte(`data: {"choices":[{"delta":{},"finish_reason":"stop","index":0}],"usage":{"prompt_tokens":8,"completion_tokens":3,"total_tokens":11,"prompt_tokens_details":{"cached_tokens":5,"cache_creation_input_tokens":2}}}`+"\n\n"))...)
	out = append(out, adapter.Flush()...)
	events := parseMessagesSSEEvents(t, string(out))
	messageStart := events[0]["data"].(map[string]any)["message"].(map[string]any)
	startUsage := messageStart["usage"].(map[string]any)
	if startUsage["input_tokens"] != float64(8) || startUsage["cache_read_input_tokens"] != float64(5) || startUsage["cache_creation_input_tokens"] != float64(2) {
		t.Fatalf("message_start usage = %#v", startUsage)
	}
	messageDelta := events[len(events)-2]["data"].(map[string]any)
	deltaUsage := messageDelta["usage"].(map[string]any)
	if deltaUsage["output_tokens"] != float64(3) || deltaUsage["cache_read_input_tokens"] != float64(5) || deltaUsage["cache_creation_input_tokens"] != float64(2) {
		t.Fatalf("message_delta usage = %#v", deltaUsage)
	}
	stats := adapter.UsageStats()
	if stats["input_tokens"] != 8 || stats["output_tokens"] != 3 || stats["total_tokens"] != 11 || stats["cache_read_input_tokens"] != 5 || stats["cache_creation_input_tokens"] != 2 {
		t.Fatalf("stats = %#v", stats)
	}
}

func TestStreamAdapterToolCall(t *testing.T) {
	adapter := NewStreamAdapter("msg_tool", "alpha")
	var out []byte
	out = append(out, adapter.Feed([]byte(`data: {"choices":[{"delta":{"content":"Checking"},"index":0}]}`+"\n\n"))...)
	out = append(out, adapter.Feed([]byte(`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"lookup","arguments":"{\"q\""}}]},"index":0}]}`+"\n\n"))...)
	out = append(out, adapter.Feed([]byte(`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":":\"x\"}"}}]},"index":0}]}`+"\n\n"))...)
	out = append(out, adapter.Feed([]byte(`data: {"choices":[{"delta":{},"finish_reason":"tool_calls","index":0}]}`+"\n\n"))...)
	out = append(out, adapter.Flush()...)
	events := parseMessagesSSEEvents(t, string(out))
	starts := filterEvents(events, "content_block_start")
	if len(starts) != 2 {
		t.Fatalf("starts = %#v", starts)
	}
	toolBlock := starts[1]["data"].(map[string]any)["content_block"].(map[string]any)
	if toolBlock["type"] != "tool_use" || toolBlock["id"] != "call_1" || toolBlock["name"] != "lookup" {
		t.Fatalf("tool block = %#v", toolBlock)
	}
	messageDelta := events[len(events)-2]["data"].(map[string]any)
	if messageDelta["delta"].(map[string]any)["stop_reason"] != "tool_use" {
		t.Fatalf("message_delta = %#v", messageDelta)
	}
	inputDeltas := 0
	for _, event := range filterEvents(events, "content_block_delta") {
		if event["data"].(map[string]any)["delta"].(map[string]any)["type"] == "input_json_delta" {
			inputDeltas++
		}
	}
	if inputDeltas != 2 {
		t.Fatalf("input delta count = %d", inputDeltas)
	}
}

func TestStreamAdapterReasoningContent(t *testing.T) {
	adapter := NewStreamAdapter("msg_reasoning", "alpha")
	var out []byte
	for _, chunk := range []string{
		`data: {"choices":[{"delta":{"role":"assistant"},"index":0}]}` + "\n\n",
		`data: {"choices":[{"delta":{"reasoning_content":"think ","reasoning_signature":"sig-1"},"index":0}]}` + "\n\n",
		`data: {"choices":[{"delta":{"reasoning_content":"hard"},"index":0}]}` + "\n\n",
		`data: {"choices":[{"delta":{"content":"Visible"},"index":0}]}` + "\n\n",
		`data: {"choices":[{"delta":{},"finish_reason":"stop","index":0}],"usage":{"prompt_tokens":5,"completion_tokens":2,"total_tokens":7}}` + "\n\n",
		"data: [DONE]\n\n",
	} {
		out = append(out, adapter.Feed([]byte(chunk))...)
	}
	out = append(out, adapter.Flush()...)
	events := parseMessagesSSEEvents(t, string(out))
	starts := filterEvents(events, "content_block_start")
	if len(starts) != 2 {
		t.Fatalf("starts = %#v", starts)
	}
	thinkingStart := starts[0]["data"].(map[string]any)
	if thinkingStart["index"] != float64(0) || thinkingStart["content_block"].(map[string]any)["type"] != "thinking" {
		t.Fatalf("thinking start = %#v", thinkingStart)
	}
	textStart := starts[1]["data"].(map[string]any)
	if textStart["index"] != float64(1) || textStart["content_block"].(map[string]any)["type"] != "text" {
		t.Fatalf("text start = %#v", textStart)
	}
	var thinkingDeltas []string
	var signature string
	var textDeltas []string
	for _, event := range filterEvents(events, "content_block_delta") {
		delta := event["data"].(map[string]any)["delta"].(map[string]any)
		switch delta["type"] {
		case "thinking_delta":
			thinkingDeltas = append(thinkingDeltas, delta["thinking"].(string))
		case "signature_delta":
			signature = delta["signature"].(string)
		case "text_delta":
			textDeltas = append(textDeltas, delta["text"].(string))
		}
	}
	if strings.Join(thinkingDeltas, "") != "think hard" || signature != "sig-1" {
		t.Fatalf("thinking deltas=%#v signature=%q", thinkingDeltas, signature)
	}
	if strings.Join(textDeltas, "") != "Visible" {
		t.Fatalf("text deltas = %#v", textDeltas)
	}
	if strings.Contains(string(out), `"text":"think `) {
		t.Fatalf("reasoning leaked into text deltas: %s", string(out))
	}
	messageDelta := events[len(events)-2]["data"].(map[string]any)
	if messageDelta["usage"].(map[string]any)["output_tokens"] != float64(2) || adapter.TotalTokens() != 7 {
		t.Fatalf("message_delta=%#v total=%d", messageDelta, adapter.TotalTokens())
	}
}

func TestStreamAdapterTextToolTextBlocksDoNotOverlap(t *testing.T) {
	adapter := NewStreamAdapter("msg_interleave", "alpha")
	var out []byte
	for _, chunk := range []string{
		`data: {"choices":[{"delta":{"content":"Before"},"index":0}]}` + "\n\n",
		`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"lookup","arguments":"{\"q\":\"x\"}"}}]},"index":0}]}` + "\n\n",
		`data: {"choices":[{"delta":{"content":"After"},"index":0}]}` + "\n\n",
		`data: {"choices":[{"delta":{},"finish_reason":"tool_calls","index":0}],"usage":{"prompt_tokens":4,"completion_tokens":5,"total_tokens":9}}` + "\n\n",
		"data: [DONE]\n\n",
	} {
		out = append(out, adapter.Feed([]byte(chunk))...)
	}
	out = append(out, adapter.Flush()...)
	events := parseMessagesSSEEvents(t, string(out))
	if got := eventNames(events); strings.Join(got, ",") != "message_start,content_block_start,content_block_delta,content_block_stop,content_block_start,content_block_delta,content_block_stop,content_block_start,content_block_delta,content_block_stop,message_delta,message_stop" {
		t.Fatalf("events = %v", got)
	}
	starts := filterEvents(events, "content_block_start")
	if len(starts) != 3 {
		t.Fatalf("starts = %#v", starts)
	}
	if starts[0]["data"].(map[string]any)["content_block"].(map[string]any)["type"] != "text" {
		t.Fatalf("first start = %#v", starts[0])
	}
	if starts[1]["data"].(map[string]any)["content_block"].(map[string]any)["type"] != "tool_use" {
		t.Fatalf("second start = %#v", starts[1])
	}
	if starts[2]["data"].(map[string]any)["content_block"].(map[string]any)["type"] != "text" {
		t.Fatalf("third start = %#v", starts[2])
	}
	deltas := filterEvents(events, "content_block_delta")
	if deltas[0]["data"].(map[string]any)["delta"].(map[string]any)["text"] != "Before" {
		t.Fatalf("first delta = %#v", deltas[0])
	}
	if deltas[1]["data"].(map[string]any)["delta"].(map[string]any)["type"] != "input_json_delta" {
		t.Fatalf("tool delta = %#v", deltas[1])
	}
	if deltas[2]["data"].(map[string]any)["delta"].(map[string]any)["text"] != "After" {
		t.Fatalf("third delta = %#v", deltas[2])
	}
	messageDelta := events[len(events)-2]["data"].(map[string]any)
	if messageDelta["delta"].(map[string]any)["stop_reason"] != "tool_use" {
		t.Fatalf("message_delta = %#v", messageDelta)
	}
	if messageDelta["usage"].(map[string]any)["output_tokens"] != float64(5) || adapter.TotalTokens() != 9 {
		t.Fatalf("message_delta=%#v total=%d", messageDelta, adapter.TotalTokens())
	}
}

func TestStreamAdapterMalformedUnknownAndLengthStop(t *testing.T) {
	adapter := NewStreamAdapter("msg_malformed", "alpha")
	var out []byte
	out = append(out, adapter.Feed([]byte("event: ping\ndata: {not-json}\n\n"))...)
	out = append(out, adapter.Feed([]byte(`event: future.event`+"\n"+`data: {"ignored":true}`+"\n\n"))...)
	out = append(out, adapter.Feed([]byte(`data: {"choices":[{"delta":{"role":"assistant"},"index":0}]}`+"\n\n"))...)
	out = append(out, adapter.Feed([]byte(`data: {"choices":[{"delta":{},"finish_reason":"length","index":0}],"usage":{"prompt_tokens":6,"completion_tokens":7,"total_tokens":13}}`+"\n\n"))...)
	out = append(out, adapter.Flush()...)

	events := parseMessagesSSEEvents(t, string(out))
	if got := eventNames(events); strings.Join(got, ",") != "message_start,message_delta,message_stop" {
		t.Fatalf("events = %v", got)
	}
	messageStart := events[0]["data"].(map[string]any)["message"].(map[string]any)
	if messageStart["usage"].(map[string]any)["input_tokens"] != float64(0) {
		t.Fatalf("message_start = %#v", messageStart)
	}
	messageDelta := events[1]["data"].(map[string]any)
	if messageDelta["delta"].(map[string]any)["stop_reason"] != "max_tokens" {
		t.Fatalf("message_delta = %#v", messageDelta)
	}
	if messageDelta["usage"].(map[string]any)["output_tokens"] != float64(7) {
		t.Fatalf("usage = %#v", messageDelta["usage"])
	}
	stats := adapter.UsageStats()
	if stats["input_tokens"] != 6 || stats["output_tokens"] != 7 || stats["total_tokens"] != 13 {
		t.Fatalf("stats = %#v", stats)
	}
}

func parseMessagesSSEEvents(t *testing.T, text string) []map[string]any {
	t.Helper()
	parts := strings.Split(text, "\n\n")
	events := make([]map[string]any, 0)
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		event := map[string]any{}
		for _, line := range strings.Split(part, "\n") {
			if strings.HasPrefix(line, "event: ") {
				event["event"] = strings.TrimPrefix(line, "event: ")
			}
			if strings.HasPrefix(line, "data: ") {
				var data map[string]any
				if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &data); err != nil {
					t.Fatalf("invalid event data %q: %v", line, err)
				}
				event["data"] = data
			}
		}
		events = append(events, event)
	}
	return events
}

func eventNames(events []map[string]any) []string {
	names := make([]string, 0, len(events))
	for _, event := range events {
		names = append(names, event["event"].(string))
	}
	return names
}

func filterEvents(events []map[string]any, name string) []map[string]any {
	out := make([]map[string]any, 0)
	for _, event := range events {
		if event["event"] == name {
			out = append(out, event)
		}
	}
	return out
}
