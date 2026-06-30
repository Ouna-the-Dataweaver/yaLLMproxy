package responses

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestStreamAdapterTextUsageAndLifecycle(t *testing.T) {
	adapter := NewStreamAdapter("resp_test", "alpha", map[string]any{
		"model":             "alpha",
		"service_tier":      "flex",
		"prompt_cache_key":  "tenant-a:prompt-1",
		"tool_choice":       "required",
		"max_output_tokens": float64(32),
	})
	var out []byte
	for _, chunk := range []string{
		`data: {"choices":[{"delta":{"role":"assistant"},"index":0}]}` + "\n\n",
		`data: {"choices":[{"delta":{"content":"Hello"},"index":0}]}` + "\n\n",
		`data: {"choices":[{"delta":{"content":" world"},"index":0}]}` + "\n\n",
		`data: {"choices":[{"delta":{},"finish_reason":"stop","index":0}],"service_tier":"default","usage":{"prompt_tokens":5,"completion_tokens":2,"total_tokens":7,"prompt_tokens_details":{"cached_tokens":4},"completion_tokens_details":{"reasoning_tokens":1}}}` + "\n\n",
		"data: [DONE]\n\n",
	} {
		out = append(out, adapter.Feed([]byte(chunk))...)
	}
	out = append(out, adapter.Flush()...)
	events := parseResponsesSSEEvents(t, string(out))
	names := eventNames(events)
	for _, want := range []string{
		"response.created",
		"response.in_progress",
		"response.output_item.added",
		"response.content_part.added",
		"response.output_text.delta",
		"response.content_part.done",
		"response.output_item.done",
		"response.completed",
	} {
		if !contains(names, want) {
			t.Fatalf("events %v missing %s", names, want)
		}
	}
	if len(filterEvents(events, "response.reasoning_summary_text.delta")) != 0 || len(filterEvents(events, "response.reasoning_summary_text.done")) != 0 {
		t.Fatalf("unexpected reasoning events: %v", eventNames(events))
	}
	deltas := filterEvents(events, "response.output_text.delta")
	if len(deltas) != 2 || deltas[0]["data"].(map[string]any)["delta"] != "Hello" || deltas[1]["data"].(map[string]any)["delta"] != " world" {
		t.Fatalf("deltas = %#v", deltas)
	}
	completed := events[len(events)-1]["data"].(map[string]any)["response"].(map[string]any)
	if completed["status"] != "completed" || completed["output_text"] != "Hello world" {
		t.Fatalf("completed = %#v", completed)
	}
	for _, key := range []string{"prompt_cache_key", "tool_choice", "max_output_tokens"} {
		if completed[key] != adapter.Original[key] {
			t.Fatalf("%s = %#v", key, completed[key])
		}
	}
	if completed["service_tier"] != "default" {
		t.Fatalf("service_tier = %#v", completed["service_tier"])
	}
	usage := completed["usage"].(map[string]any)
	if usage["input_tokens"] != float64(5) || usage["output_tokens"] != float64(2) || usage["total_tokens"] != float64(7) {
		t.Fatalf("usage = %#v", usage)
	}
	inputDetails := usage["input_tokens_details"].(map[string]any)
	if inputDetails["cached_tokens"] != float64(4) {
		t.Fatalf("input token details = %#v", inputDetails)
	}
	outputDetails := usage["output_tokens_details"].(map[string]any)
	if outputDetails["reasoning_tokens"] != float64(1) {
		t.Fatalf("output token details = %#v", outputDetails)
	}
	if adapter.TotalTokens() != 7 {
		t.Fatalf("total tokens = %d", adapter.TotalTokens())
	}
	stats := adapter.UsageStats()
	statsInputDetails := stats["input_tokens_details"].(map[string]any)
	if statsInputDetails["cached_tokens"] != float64(4) {
		t.Fatalf("usage stats input details = %#v", statsInputDetails)
	}
	statsOutputDetails := stats["output_tokens_details"].(map[string]any)
	if statsOutputDetails["reasoning_tokens"] != float64(1) {
		t.Fatalf("usage stats output details = %#v", statsOutputDetails)
	}
}

func TestStreamAdapterReasoningContentLifecycle(t *testing.T) {
	adapter := NewStreamAdapter("resp_reasoning", "alpha", map[string]any{"model": "alpha"})
	var out []byte
	for _, chunk := range []string{
		`data: {"choices":[{"delta":{"role":"assistant"},"index":0}]}` + "\n\n",
		`data: {"choices":[{"delta":{"reasoning_content":"think "},"index":0}]}` + "\n\n",
		`data: {"choices":[{"delta":{"reasoning_content":"hard"},"index":0}]}` + "\n\n",
		`data: {"choices":[{"delta":{"content":"Visible"},"index":0}]}` + "\n\n",
		`data: {"choices":[{"delta":{},"finish_reason":"stop","index":0}],"usage":{"prompt_tokens":5,"completion_tokens":3,"total_tokens":8,"completion_tokens_details":{"reasoning_tokens":2}}}` + "\n\n",
		"data: [DONE]\n\n",
	} {
		out = append(out, adapter.Feed([]byte(chunk))...)
	}
	out = append(out, adapter.Flush()...)
	events := parseResponsesSSEEvents(t, string(out))
	names := eventNames(events)
	for _, want := range []string{
		"response.reasoning_summary_text.delta",
		"response.reasoning_summary_text.done",
		"response.output_text.delta",
		"response.completed",
	} {
		if !contains(names, want) {
			t.Fatalf("events %v missing %s", names, want)
		}
	}
	added := filterEvents(events, "response.output_item.added")
	if len(added) != 2 {
		t.Fatalf("added = %#v", added)
	}
	firstAdded := added[0]["data"].(map[string]any)
	if firstAdded["output_index"] != float64(0) || firstAdded["item"].(map[string]any)["type"] != "reasoning" {
		t.Fatalf("first added = %#v", firstAdded)
	}
	secondAdded := added[1]["data"].(map[string]any)
	if secondAdded["output_index"] != float64(1) || secondAdded["item"].(map[string]any)["type"] != "message" {
		t.Fatalf("second added = %#v", secondAdded)
	}
	reasoningDeltas := filterEvents(events, "response.reasoning_summary_text.delta")
	if len(reasoningDeltas) != 2 || reasoningDeltas[0]["data"].(map[string]any)["delta"] != "think " || reasoningDeltas[1]["data"].(map[string]any)["delta"] != "hard" {
		t.Fatalf("reasoning deltas = %#v", reasoningDeltas)
	}
	reasoningDone := filterEvents(events, "response.reasoning_summary_text.done")
	if len(reasoningDone) != 1 || reasoningDone[0]["data"].(map[string]any)["text"] != "think hard" {
		t.Fatalf("reasoning done = %#v", reasoningDone)
	}
	textDeltas := filterEvents(events, "response.output_text.delta")
	if len(textDeltas) != 1 || textDeltas[0]["data"].(map[string]any)["delta"] != "Visible" {
		t.Fatalf("text deltas = %#v", textDeltas)
	}
	completed := events[len(events)-1]["data"].(map[string]any)["response"].(map[string]any)
	if completed["output_text"] != "Visible" {
		t.Fatalf("completed = %#v", completed)
	}
	output := completed["output"].([]any)
	if output[0].(map[string]any)["type"] != "reasoning" || output[1].(map[string]any)["type"] != "message" {
		t.Fatalf("output = %#v", output)
	}
	summary := output[0].(map[string]any)["summary"].([]any)
	if summary[0].(map[string]any)["text"] != "think hard" {
		t.Fatalf("summary = %#v", summary)
	}
	usage := completed["usage"].(map[string]any)
	outputDetails := usage["output_tokens_details"].(map[string]any)
	if outputDetails["reasoning_tokens"] != float64(2) || adapter.TotalTokens() != 8 {
		t.Fatalf("usage=%#v total=%d", usage, adapter.TotalTokens())
	}
}

func TestStreamAdapterToolCall(t *testing.T) {
	adapter := NewStreamAdapter("resp_tool", "alpha", nil)
	var out []byte
	out = append(out, adapter.Feed([]byte(`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"lookup","arguments":"{\"q\""}}]},"index":0}]}`+"\n\n"))...)
	out = append(out, adapter.Feed([]byte(`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":":\"x\"}"}}]},"index":0}]}`+"\n\n"))...)
	out = append(out, adapter.Feed([]byte(`data: {"choices":[{"delta":{},"finish_reason":"tool_calls","index":0}]}`+"\n\n"))...)
	out = append(out, adapter.Flush()...)
	events := parseResponsesSSEEvents(t, string(out))
	added := filterEvents(events, "response.output_item.added")
	if len(added) != 1 {
		t.Fatalf("added = %#v", added)
	}
	item := added[0]["data"].(map[string]any)["item"].(map[string]any)
	if item["type"] != "function_call" || item["id"] != "call_1" {
		t.Fatalf("item = %#v", item)
	}
	argDeltas := filterEvents(events, "response.function_call_arguments.delta")
	if len(argDeltas) != 2 {
		t.Fatalf("arg deltas = %#v", argDeltas)
	}
	done := filterEvents(events, "response.output_item.done")
	doneItem := done[len(done)-1]["data"].(map[string]any)["item"].(map[string]any)
	if doneItem["name"] != "lookup" || doneItem["arguments"] != `{"q":"x"}` {
		t.Fatalf("done item = %#v", doneItem)
	}
}

func TestStreamAdapterIncompleteStatusAndEmptyContent(t *testing.T) {
	adapter := NewStreamAdapter("resp_incomplete", "alpha", map[string]any{"model": "alpha"})
	var out []byte
	for _, chunk := range []string{
		`data: {"choices":[{"delta":{"role":"assistant"},"index":0}]}` + "\n\n",
		`data: {"choices":[{"delta":{},"finish_reason":"length","index":0}],"usage":{"prompt_tokens":3,"completion_tokens":0,"total_tokens":3}}` + "\n\n",
		"data: [DONE]\n\n",
	} {
		out = append(out, adapter.Feed([]byte(chunk))...)
	}
	out = append(out, adapter.Flush()...)
	events := parseResponsesSSEEvents(t, string(out))
	names := eventNames(events)
	if !contains(names, "response.incomplete") {
		t.Fatalf("events = %v", names)
	}
	done := filterEvents(events, "response.output_item.done")
	if len(done) != 1 {
		t.Fatalf("done = %#v", done)
	}
	item := done[0]["data"].(map[string]any)["item"].(map[string]any)
	if item["status"] != "incomplete" {
		t.Fatalf("item = %#v", item)
	}
	content := item["content"].([]any)
	if len(content) != 1 || content[0].(map[string]any)["text"] != "" {
		t.Fatalf("content = %#v", content)
	}
	final := events[len(events)-1]["data"].(map[string]any)["response"].(map[string]any)
	if final["status"] != "incomplete" {
		t.Fatalf("final = %#v", final)
	}
	if !reflect.DeepEqual(final["incomplete_details"], map[string]any{"reason": "max_output_tokens"}) {
		t.Fatalf("incomplete_details = %#v", final["incomplete_details"])
	}
	if adapter.TotalTokens() != 3 {
		t.Fatalf("total tokens = %d", adapter.TotalTokens())
	}
}

func TestStreamAdapterContentFilterErrorParamShape(t *testing.T) {
	adapter := NewStreamAdapter("resp_filtered", "alpha", map[string]any{"model": "alpha"})
	var out []byte
	out = append(out, adapter.Feed([]byte(`data: {"choices":[{"delta":{"content":"partial"},"index":0}]}`+"\n\n"))...)
	out = append(out, adapter.Feed([]byte(`data: {"choices":[{"delta":{},"finish_reason":"content_filter","index":0}]}`+"\n\n"))...)
	out = append(out, adapter.Flush()...)
	events := parseResponsesSSEEvents(t, string(out))
	final := events[len(events)-1]["data"].(map[string]any)["response"].(map[string]any)
	if final["status"] != "failed" {
		t.Fatalf("final = %#v", final)
	}
	errObj := final["error"].(map[string]any)
	if errObj["type"] != "model_error" || errObj["code"] != "content_filter" || errObj["param"] != nil {
		t.Fatalf("error = %#v", errObj)
	}
}

func TestStreamAdapterIgnoresMalformedAndUnknownEvents(t *testing.T) {
	adapter := NewStreamAdapter("resp_malformed", "alpha", nil)
	var out []byte
	out = append(out, adapter.Feed([]byte("event: ping\ndata: {not-json}\n\n"))...)
	out = append(out, adapter.Feed([]byte(`event: future.event`+"\n"+`data: {"unknown":true}`+"\n\n"))...)
	out = append(out, adapter.Feed([]byte(`data: {"choices":[{"delta":{"content":"ok"},"index":0}]}`+"\n\n"))...)
	out = append(out, adapter.Feed([]byte(`data: {"choices":[{"delta":{},"finish_reason":"stop","index":0}]}`+"\n\n"))...)
	out = append(out, adapter.Flush()...)
	events := parseResponsesSSEEvents(t, string(out))
	if len(filterEvents(events, "response.output_text.delta")) != 1 {
		t.Fatalf("events = %v", eventNames(events))
	}
	final := events[len(events)-1]["data"].(map[string]any)["response"].(map[string]any)
	if final["status"] != "completed" || final["output_text"] != "ok" {
		t.Fatalf("final = %#v", final)
	}
}

func parseResponsesSSEEvents(t *testing.T, text string) []map[string]any {
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

func contains(items []string, needle string) bool {
	for _, item := range items {
		if item == needle {
			return true
		}
	}
	return false
}
