package responses

import (
	"reflect"
	"testing"
)

func TestToChatCompletionsTranslatesInputAndTools(t *testing.T) {
	payload := map[string]any{
		"model":             "alpha",
		"instructions":      "Be concise.",
		"input":             "Hello",
		"max_output_tokens": float64(32),
		"temperature":       float64(0.2),
		"tools": []any{map[string]any{
			"type":        "function",
			"name":        "lookup",
			"description": "Find data",
			"parameters":  map[string]any{"type": "object"},
		}},
		"tool_choice":      "auto",
		"service_tier":     "flex",
		"prompt_cache_key": "tenant-a:prompt-1",
	}
	got, err := ToChatCompletions(payload, nil)
	if err != nil {
		t.Fatal(err)
	}
	messages := got["messages"].([]any)
	if len(messages) != 2 {
		t.Fatalf("messages = %#v", messages)
	}
	if messages[0].(map[string]any)["role"] != "system" || messages[0].(map[string]any)["content"] != "Be concise." {
		t.Fatalf("system = %#v", messages[0])
	}
	if messages[1].(map[string]any)["role"] != "user" || messages[1].(map[string]any)["content"] != "Hello" {
		t.Fatalf("user = %#v", messages[1])
	}
	if got["max_tokens"] != float64(32) || got["temperature"] != float64(0.2) || got["tool_choice"] != "auto" || got["service_tier"] != "flex" || got["prompt_cache_key"] != "tenant-a:prompt-1" {
		t.Fatalf("params = %#v", got)
	}
	tools := got["tools"].([]any)
	function := tools[0].(map[string]any)["function"].(map[string]any)
	if function["name"] != "lookup" || function["description"] != "Find data" {
		t.Fatalf("tool = %#v", function)
	}
}

func TestToChatCompletionsToolChoiceForms(t *testing.T) {
	cases := []struct {
		name  string
		input any
		want  any
	}{
		{name: "required string", input: "required", want: "required"},
		{name: "any string", input: "any", want: "required"},
		{name: "required object", input: map[string]any{"type": "required"}, want: "required"},
		{name: "any object", input: map[string]any{"type": "any"}, want: "required"},
		{name: "specific function", input: map[string]any{"type": "function", "name": "lookup"}, want: map[string]any{"type": "function", "function": map[string]any{"name": "lookup"}}},
		{name: "nested specific function", input: map[string]any{"type": "function", "function": map[string]any{"name": "search"}}, want: map[string]any{"type": "function", "function": map[string]any{"name": "search"}}},
		{name: "auto object", input: map[string]any{"type": "auto"}, want: "auto"},
		{name: "none object", input: map[string]any{"type": "none"}, want: "none"},
		{name: "unknown object", input: map[string]any{"type": "future", "mode": "strict"}, want: map[string]any{"type": "future", "mode": "strict"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ToChatCompletions(map[string]any{
				"model":       "alpha",
				"stream":      true,
				"input":       "hello",
				"tool_choice": tc.input,
			}, nil)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got["tool_choice"], tc.want) {
				t.Fatalf("tool_choice = %#v; want %#v", got["tool_choice"], tc.want)
			}
			if got["stream"] != true {
				t.Fatalf("stream = %#v", got["stream"])
			}
		})
	}
}

func TestToChatCompletionsInputItemsDeveloperImageFunctionAndHistory(t *testing.T) {
	history := []any{
		map[string]any{"role": "user", "content": "previous question"},
		map[string]any{"role": "assistant", "content": "previous answer"},
	}
	got, err := ToChatCompletions(map[string]any{
		"model": "alpha",
		"input": []any{
			map[string]any{
				"type": "message",
				"role": "developer",
				"content": []any{
					map[string]any{"type": "input_text", "text": "follow policy"},
					map[string]any{"type": "input_image", "image_url": "https://example.com/image.png", "detail": "high"},
					map[string]any{"type": "input_image", "file_id": "file_img_1"},
				},
			},
			map[string]any{"type": "function_call", "call_id": "call_1", "name": "lookup", "arguments": `{"q":"x"}`},
			map[string]any{"type": "function_call_output", "call_id": "call_1", "output": "lookup result"},
		},
	}, history)
	if err != nil {
		t.Fatal(err)
	}
	messages := got["messages"].([]any)
	if len(messages) != 6 {
		t.Fatalf("messages = %#v", messages)
	}
	if messages[0].(map[string]any)["content"] != "previous question" || messages[1].(map[string]any)["content"] != "previous answer" {
		t.Fatalf("history = %#v", messages[:2])
	}
	developer := messages[2].(map[string]any)
	if developer["role"] != "system" {
		t.Fatalf("developer message = %#v", developer)
	}
	if developer["content"] != "follow policy" {
		t.Fatalf("developer text = %#v", developer)
	}
	media := messages[3].(map[string]any)
	if media["role"] != "user" {
		t.Fatalf("media message = %#v", media)
	}
	content := media["content"].([]any)
	image := content[0].(map[string]any)["image_url"].(map[string]any)
	if image["url"] != "https://example.com/image.png" || image["detail"] != "high" {
		t.Fatalf("developer image = %#v", image)
	}
	fileImage := content[1].(map[string]any)["file"].(map[string]any)
	if content[1].(map[string]any)["type"] != "file" || fileImage["file_id"] != "file_img_1" {
		t.Fatalf("developer file image = %#v", fileImage)
	}
	callMessage := messages[4].(map[string]any)
	if callMessage["role"] != "assistant" || callMessage["content"] != nil {
		t.Fatalf("function call message = %#v", callMessage)
	}
	call := callMessage["tool_calls"].([]any)[0].(map[string]any)
	fn := call["function"].(map[string]any)
	if call["id"] != "call_1" || fn["name"] != "lookup" || fn["arguments"] != `{"q":"x"}` {
		t.Fatalf("function call = %#v", call)
	}
	tool := messages[5].(map[string]any)
	if tool["role"] != "tool" || tool["tool_call_id"] != "call_1" || tool["content"] != "lookup result" {
		t.Fatalf("tool output = %#v", tool)
	}
}

func TestFromChatCompletionBuildsResponsesObject(t *testing.T) {
	payload := map[string]any{
		"id":    "chatcmpl-1",
		"model": "alpha",
		"choices": []any{map[string]any{
			"message": map[string]any{
				"content": "Hello",
				"tool_calls": []any{map[string]any{
					"id": "call_1",
					"function": map[string]any{
						"name":      "lookup",
						"arguments": `{"q":"x"}`,
					},
				}},
			},
		}},
		"usage": map[string]any{
			"prompt_tokens":     float64(4),
			"completion_tokens": float64(6),
			"total_tokens":      float64(10),
			"prompt_tokens_details": map[string]any{
				"cached_tokens": float64(2),
			},
			"completion_tokens_details": map[string]any{
				"reasoning_tokens": float64(3),
			},
		},
	}
	got := FromChatCompletion(payload, map[string]any{"model": "alpha"}, "resp_1")
	if got["id"] != "resp_1" || got["object"] != "response" || got["status"] != "completed" {
		t.Fatalf("response = %#v", got)
	}
	if got["output_text"] != "Hello" {
		t.Fatalf("output_text = %#v", got["output_text"])
	}
	output := got["output"].([]any)
	if output[0].(map[string]any)["type"] != "message" || output[1].(map[string]any)["type"] != "function_call" {
		t.Fatalf("output = %#v", output)
	}
	usage := got["usage"].(map[string]any)
	if usage["input_tokens"] != float64(4) || usage["output_tokens"] != float64(6) || usage["total_tokens"] != float64(10) {
		t.Fatalf("usage = %#v", usage)
	}
	inputDetails := usage["input_tokens_details"].(map[string]any)
	if inputDetails["cached_tokens"] != float64(2) {
		t.Fatalf("input token details = %#v", inputDetails)
	}
	outputDetails := usage["output_tokens_details"].(map[string]any)
	if outputDetails["reasoning_tokens"] != float64(3) {
		t.Fatalf("output token details = %#v", outputDetails)
	}
}

func TestFromChatCompletionBuildsReasoningOutputItem(t *testing.T) {
	payload := map[string]any{
		"id":    "chatcmpl-reasoning",
		"model": "alpha",
		"choices": []any{map[string]any{
			"finish_reason": "stop",
			"message": map[string]any{
				"reasoning_content": "hidden reasoning",
				"content":           "Visible answer",
			},
		}},
		"usage": map[string]any{
			"prompt_tokens":     float64(4),
			"completion_tokens": float64(6),
			"total_tokens":      float64(10),
			"completion_tokens_details": map[string]any{
				"reasoning_tokens": float64(2),
			},
		},
	}

	got := FromChatCompletion(payload, map[string]any{"model": "alpha"}, "resp_reasoning")
	if got["output_text"] != "Visible answer" {
		t.Fatalf("output_text = %#v", got["output_text"])
	}
	output := got["output"].([]any)
	if len(output) != 2 {
		t.Fatalf("output = %#v", output)
	}
	reasoning := output[0].(map[string]any)
	if reasoning["type"] != "reasoning" {
		t.Fatalf("reasoning item = %#v", reasoning)
	}
	summary := reasoning["summary"].([]any)
	if summary[0].(map[string]any)["type"] != "summary_text" || summary[0].(map[string]any)["text"] != "hidden reasoning" {
		t.Fatalf("summary = %#v", summary)
	}
	message := output[1].(map[string]any)
	if message["type"] != "message" {
		t.Fatalf("message = %#v", message)
	}
	usage := got["usage"].(map[string]any)
	outputDetails := usage["output_tokens_details"].(map[string]any)
	if outputDetails["reasoning_tokens"] != float64(2) {
		t.Fatalf("usage = %#v", usage)
	}
}

func TestFromChatCompletionMapsTerminalFinishReasons(t *testing.T) {
	cases := []struct {
		name                    string
		finishReasons           []string
		wantStatus              string
		wantIncompleteDetails   any
		wantError               any
		wantFirstMessageStatus  string
		wantSecondMessageStatus string
	}{
		{
			name:                   "stop completes",
			finishReasons:          []string{"stop"},
			wantStatus:             "completed",
			wantFirstMessageStatus: "completed",
		},
		{
			name:                   "tool calls completes",
			finishReasons:          []string{"tool_calls"},
			wantStatus:             "completed",
			wantFirstMessageStatus: "completed",
		},
		{
			name:                   "length is incomplete",
			finishReasons:          []string{"length"},
			wantStatus:             "incomplete",
			wantIncompleteDetails:  map[string]any{"reason": "max_output_tokens"},
			wantFirstMessageStatus: "incomplete",
		},
		{
			name:                   "content filter is failed",
			finishReasons:          []string{"content_filter"},
			wantStatus:             "failed",
			wantError:              map[string]any{"type": "model_error", "code": "content_filter", "param": nil, "message": "Response filtered by content policy."},
			wantFirstMessageStatus: "completed",
		},
		{
			name:                    "length takes precedence over content filter",
			finishReasons:           []string{"content_filter", "length"},
			wantStatus:              "incomplete",
			wantIncompleteDetails:   map[string]any{"reason": "max_output_tokens"},
			wantFirstMessageStatus:  "completed",
			wantSecondMessageStatus: "incomplete",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			choices := make([]any, 0, len(tc.finishReasons))
			for i, finishReason := range tc.finishReasons {
				choices = append(choices, map[string]any{
					"finish_reason": finishReason,
					"message":       map[string]any{"content": "part"},
					"index":         float64(i),
				})
			}
			got := FromChatCompletion(map[string]any{
				"id":      "chatcmpl-terminal",
				"model":   "alpha",
				"choices": choices,
			}, map[string]any{"model": "alpha"}, "resp_terminal")
			if got["status"] != tc.wantStatus {
				t.Fatalf("status = %#v; want %s; response=%#v", got["status"], tc.wantStatus, got)
			}
			if !reflect.DeepEqual(got["incomplete_details"], tc.wantIncompleteDetails) {
				t.Fatalf("incomplete_details = %#v; want %#v", got["incomplete_details"], tc.wantIncompleteDetails)
			}
			if !reflect.DeepEqual(got["error"], tc.wantError) {
				t.Fatalf("error = %#v; want %#v", got["error"], tc.wantError)
			}
			output := got["output"].([]any)
			first := output[0].(map[string]any)
			if first["status"] != tc.wantFirstMessageStatus {
				t.Fatalf("first message status = %#v; want %s", first["status"], tc.wantFirstMessageStatus)
			}
			if tc.wantSecondMessageStatus != "" {
				second := output[1].(map[string]any)
				if second["status"] != tc.wantSecondMessageStatus {
					t.Fatalf("second message status = %#v; want %s", second["status"], tc.wantSecondMessageStatus)
				}
			}
		})
	}
}

func TestFromChatCompletionContentVariantsAndRequestEcho(t *testing.T) {
	payload := map[string]any{
		"id":    "chatcmpl-variants",
		"model": "alpha",
		"choices": []any{map[string]any{
			"message": map[string]any{
				"content": []any{
					map[string]any{"type": "text", "text": "Visible"},
					map[string]any{"type": "refusal", "refusal": "inline refusal"},
					map[string]any{"type": "reasoning_text", "text": "reasoned"},
					map[string]any{"type": "summary_text", "text": "summarized"},
				},
				"refusal": "top-level refusal",
			},
		}},
	}
	original := map[string]any{
		"model":             "alpha",
		"temperature":       float64(0.4),
		"top_p":             float64(0.9),
		"max_output_tokens": float64(64),
		"tools":             []any{map[string]any{"type": "function", "name": "lookup"}},
		"tool_choice":       "auto",
		"service_tier":      "default",
		"prompt_cache_key":  "prompt-key",
	}
	got := FromChatCompletion(payload, original, "resp_variants")
	if got["output_text"] != "Visible" {
		t.Fatalf("output_text = %#v", got["output_text"])
	}
	for _, key := range []string{"temperature", "top_p", "max_output_tokens", "tool_choice", "service_tier", "prompt_cache_key"} {
		if got[key] != original[key] {
			t.Fatalf("%s = %#v", key, got[key])
		}
	}
	if !reflect.DeepEqual(got["tools"], original["tools"]) {
		t.Fatalf("tools = %#v", got["tools"])
	}
	output := got["output"].([]any)
	reasoning := output[0].(map[string]any)
	if reasoning["type"] != "reasoning" {
		t.Fatalf("reasoning item = %#v", reasoning)
	}
	summary := reasoning["summary"].([]any)
	if len(summary) != 2 {
		t.Fatalf("summary = %#v", summary)
	}
	if summary[0].(map[string]any)["text"] != "reasoned" || summary[1].(map[string]any)["text"] != "summarized" {
		t.Fatalf("summary = %#v", summary)
	}
	message := output[1].(map[string]any)
	content := message["content"].([]any)
	if len(content) != 3 {
		t.Fatalf("content = %#v", content)
	}
	if content[0].(map[string]any)["type"] != "output_text" || content[0].(map[string]any)["text"] != "Visible" {
		t.Fatalf("text content = %#v", content[0])
	}
	if content[1].(map[string]any)["type"] != "refusal" || content[1].(map[string]any)["refusal"] != "inline refusal" {
		t.Fatalf("inline refusal = %#v", content[1])
	}
	if content[2].(map[string]any)["type"] != "refusal" || content[2].(map[string]any)["refusal"] != "top-level refusal" {
		t.Fatalf("top-level refusal = %#v", content[2])
	}
}

func TestFromChatCompletionPreservesRefusalsAndAnnotations(t *testing.T) {
	annotations := []any{
		map[string]any{"type": "url_citation", "url": "https://example.com/source", "title": "source"},
		map[string]any{"type": "file_citation", "file_id": "file_123", "quote": "quoted text"},
		map[string]any{"type": "file_path", "file_id": "file_path_123", "path": "/tmp/result.txt"},
	}
	got := FromChatCompletion(map[string]any{
		"id":    "chatcmpl-annotations",
		"model": "alpha",
		"choices": []any{map[string]any{
			"finish_reason": "content_filter",
			"message": map[string]any{
				"content": []any{
					map[string]any{"type": "output_text", "text": "Allowed part", "annotations": annotations},
					map[string]any{"type": "refusal", "refusal": "I cannot help with that."},
				},
				"refusal": "Top-level refusal",
			},
		}},
	}, map[string]any{"model": "alpha"}, "resp_annotations")

	if got["status"] != "failed" {
		t.Fatalf("status = %#v", got["status"])
	}
	if got["output_text"] != "Allowed part" {
		t.Fatalf("output_text = %#v", got["output_text"])
	}
	output := got["output"].([]any)
	message := output[0].(map[string]any)
	if message["status"] != "completed" {
		t.Fatalf("message = %#v", message)
	}
	content := message["content"].([]any)
	if len(content) != 3 {
		t.Fatalf("content = %#v", content)
	}
	text := content[0].(map[string]any)
	if text["type"] != "output_text" || text["text"] != "Allowed part" {
		t.Fatalf("text = %#v", text)
	}
	if !reflect.DeepEqual(text["annotations"], annotations) {
		t.Fatalf("annotations = %#v", text["annotations"])
	}
	if content[1].(map[string]any)["refusal"] != "I cannot help with that." || content[2].(map[string]any)["refusal"] != "Top-level refusal" {
		t.Fatalf("refusals = %#v", content[1:])
	}
	if got["error"].(map[string]any)["code"] != "content_filter" {
		t.Fatalf("error = %#v", got["error"])
	}
	if _, ok := got["error"].(map[string]any)["param"]; !ok {
		t.Fatalf("error missing param = %#v", got["error"])
	}
}

func TestErrorResponseIncludesSafeParamShape(t *testing.T) {
	got := ErrorResponse("resp_error", "invalid_request_error", "invalid_input", "bad input", "alpha", "input")
	errObj := got["error"].(map[string]any)
	if errObj["type"] != "invalid_request_error" || errObj["code"] != "invalid_input" || errObj["param"] != "input" || errObj["message"] != "bad input" {
		t.Fatalf("error = %#v", errObj)
	}
	withoutParam := ErrorResponse("resp_error", "server_error", "upstream_error", "upstream failed", "alpha")
	if _, ok := withoutParam["error"].(map[string]any)["param"]; !ok {
		t.Fatalf("error missing param = %#v", withoutParam["error"])
	}
	if withoutParam["error"].(map[string]any)["param"] != nil {
		t.Fatalf("param = %#v", withoutParam["error"].(map[string]any)["param"])
	}
}

func TestFromChatCompletionEmptyAssistantContentStillReturnsMessage(t *testing.T) {
	got := FromChatCompletion(map[string]any{
		"id":    "chatcmpl-empty",
		"model": "alpha",
		"choices": []any{map[string]any{
			"finish_reason": "stop",
			"message":       map[string]any{"content": ""},
		}},
	}, map[string]any{"model": "alpha"}, "resp_empty")

	if got["status"] != "completed" || got["output_text"] != "" {
		t.Fatalf("response = %#v", got)
	}
	output := got["output"].([]any)
	if len(output) != 1 {
		t.Fatalf("output = %#v", output)
	}
	message := output[0].(map[string]any)
	if message["type"] != "message" || message["status"] != "completed" {
		t.Fatalf("message = %#v", message)
	}
	if content := message["content"].([]any); len(content) != 0 {
		t.Fatalf("content = %#v", content)
	}
}

func TestStoreBuildsConversationHistory(t *testing.T) {
	store := NewStore(10)
	first := FromChatCompletion(
		map[string]any{
			"model": "alpha",
			"choices": []any{map[string]any{
				"message": map[string]any{"content": "First answer"},
			}},
		},
		map[string]any{"model": "alpha", "input": "First question"},
		"resp_1",
	)
	store.Store(first, "First question")
	history := store.ConversationHistory("resp_1")
	if len(history) != 2 {
		t.Fatalf("history = %#v", history)
	}
	if history[0].(map[string]any)["role"] != "user" || history[1].(map[string]any)["role"] != "assistant" {
		t.Fatalf("history = %#v", history)
	}
}
