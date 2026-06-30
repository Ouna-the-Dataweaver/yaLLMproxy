package messages

import (
	"reflect"
	"testing"
)

func TestToChatCompletionsTranslatesAnthropicRequest(t *testing.T) {
	payload := map[string]any{
		"model":          "alpha",
		"system":         "You are helpful.",
		"max_tokens":     float64(128),
		"stop_sequences": []any{"END"},
		"temperature":    float64(0.4),
		"metadata":       map[string]any{"user_id": "user-1"},
		"tools": []any{map[string]any{
			"name":         "lookup",
			"description":  "Find data",
			"input_schema": map[string]any{"type": "object"},
		}},
		"tool_choice": map[string]any{"type": "tool", "name": "lookup"},
		"messages": []any{
			map[string]any{"role": "user", "content": []any{
				map[string]any{"type": "text", "text": "Find x"},
			}},
			map[string]any{"role": "assistant", "content": []any{
				map[string]any{"type": "text", "text": "I will check."},
				map[string]any{"type": "tool_use", "id": "toolu_1", "name": "lookup", "input": map[string]any{"q": "x"}},
			}},
			map[string]any{"role": "user", "content": []any{
				map[string]any{"type": "tool_result", "tool_use_id": "toolu_1", "content": "ok"},
			}},
		},
	}

	got, err := ToChatCompletions(payload)
	if err != nil {
		t.Fatal(err)
	}
	messages := got["messages"].([]any)
	if len(messages) != 4 {
		t.Fatalf("messages = %#v", messages)
	}
	if messages[0].(map[string]any)["role"] != "system" || messages[0].(map[string]any)["content"] != "You are helpful." {
		t.Fatalf("system message = %#v", messages[0])
	}
	assistant := messages[2].(map[string]any)
	toolCalls := assistant["tool_calls"].([]any)
	toolCall := toolCalls[0].(map[string]any)
	if toolCall["id"] != "toolu_1" || toolCall["type"] != "function" {
		t.Fatalf("tool call = %#v", toolCall)
	}
	function := toolCall["function"].(map[string]any)
	if function["name"] != "lookup" || function["arguments"] != `{"q":"x"}` {
		t.Fatalf("function = %#v", function)
	}
	toolResult := messages[3].(map[string]any)
	if toolResult["role"] != "tool" || toolResult["tool_call_id"] != "toolu_1" || toolResult["content"] != "ok" {
		t.Fatalf("tool result = %#v", toolResult)
	}
	if got["stop"].([]any)[0] != "END" || got["user"] != "user-1" {
		t.Fatalf("params = %#v", got)
	}
	choice := got["tool_choice"].(map[string]any)
	if choice["type"] != "function" {
		t.Fatalf("tool choice = %#v", choice)
	}
}

func TestToChatCompletionsToolChoiceRequiredAnyAndSpecific(t *testing.T) {
	cases := []struct {
		name  string
		input any
		want  any
	}{
		{name: "required string", input: "required", want: "required"},
		{name: "any string", input: "any", want: "required"},
		{name: "required object", input: map[string]any{"type": "required"}, want: "required"},
		{name: "any object", input: map[string]any{"type": "any"}, want: "required"},
		{name: "specific tool", input: map[string]any{"type": "tool", "name": "lookup"}, want: map[string]any{"type": "function", "function": map[string]any{"name": "lookup"}}},
		{name: "function object", input: map[string]any{"type": "function", "function": map[string]any{"name": "search"}}, want: map[string]any{"type": "function", "function": map[string]any{"name": "search"}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ToChatCompletions(map[string]any{
				"model":       "alpha",
				"stream":      true,
				"tool_choice": tc.input,
				"messages": []any{map[string]any{
					"role":    "user",
					"content": "hello",
				}},
			})
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

func TestToChatCompletionsDefaultsMissingToolSchema(t *testing.T) {
	got, err := ToChatCompletions(map[string]any{
		"model": "alpha",
		"tools": []any{map[string]any{
			"name": "ping",
		}},
		"messages": []any{map[string]any{
			"role":    "user",
			"content": "hello",
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	tools := got["tools"].([]any)
	function := tools[0].(map[string]any)["function"].(map[string]any)
	parameters, ok := function["parameters"].(map[string]any)
	if !ok {
		t.Fatalf("parameters type = %T, value = %#v", function["parameters"], function["parameters"])
	}
	if len(parameters) != 0 {
		t.Fatalf("parameters = %#v", parameters)
	}
}

func TestToChatCompletionsPreservesEmptySystemPrompt(t *testing.T) {
	got, err := ToChatCompletions(map[string]any{
		"model":  "alpha",
		"system": "",
		"messages": []any{map[string]any{
			"role":    "user",
			"content": "hello",
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	messages := got["messages"].([]any)
	if len(messages) != 2 {
		t.Fatalf("messages = %#v", messages)
	}
	system := messages[0].(map[string]any)
	if system["role"] != "system" || system["content"] != "" {
		t.Fatalf("system = %#v", system)
	}
}

func TestToChatCompletionsConvertsSystemArrayAndToolResultArrayError(t *testing.T) {
	got, err := ToChatCompletions(map[string]any{
		"model": "alpha",
		"system": []any{
			map[string]any{"type": "text", "text": "First system line"},
			map[string]any{"type": "text", "text": "Second system line"},
		},
		"messages": []any{map[string]any{
			"role": "user",
			"content": []any{
				map[string]any{
					"type":        "tool_result",
					"tool_use_id": "toolu_1",
					"is_error":    true,
					"content": []any{
						map[string]any{"type": "text", "text": "first error"},
						map[string]any{"type": "text", "text": "second error"},
					},
				},
				map[string]any{"type": "text", "text": "next question"},
			},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	messages := got["messages"].([]any)
	if len(messages) != 3 {
		t.Fatalf("messages = %#v", messages)
	}
	system := messages[0].(map[string]any)
	if system["role"] != "system" || system["content"] != "First system line\nSecond system line" {
		t.Fatalf("system = %#v", system)
	}
	tool := messages[1].(map[string]any)
	if tool["role"] != "tool" || tool["tool_call_id"] != "toolu_1" || tool["content"] != "[Error] first error\nsecond error" {
		t.Fatalf("tool result = %#v", tool)
	}
	user := messages[2].(map[string]any)
	if user["role"] != "user" || user["content"] != "next question" {
		t.Fatalf("user = %#v", user)
	}
}

func TestToChatCompletionsConvertsImageAndDocumentBlocks(t *testing.T) {
	got, err := ToChatCompletions(map[string]any{
		"model": "alpha",
		"messages": []any{map[string]any{
			"role": "user",
			"content": []any{
				map[string]any{"type": "text", "text": "inspect these", "cache_control": map[string]any{"type": "ephemeral"}},
				map[string]any{"type": "image", "source": map[string]any{
					"type":       "base64",
					"media_type": "image/jpeg",
					"data":       "aW1hZ2U=",
				}, "cache_control": map[string]any{"type": "ephemeral"}},
				map[string]any{"type": "document", "name": "brief.pdf", "source": map[string]any{
					"media_type": "application/pdf",
				}},
				map[string]any{"type": "document", "source": map[string]any{
					"type":       "url",
					"media_type": "image/png",
					"url":        "https://example.com/page.png",
				}},
			},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	messages := got["messages"].([]any)
	content := messages[0].(map[string]any)["content"].([]any)
	if len(content) != 4 {
		t.Fatalf("content = %#v", content)
	}
	if content[0].(map[string]any)["text"] != "inspect these" {
		t.Fatalf("text part = %#v", content[0])
	}
	base64Image := content[1].(map[string]any)["image_url"].(map[string]any)
	if base64Image["url"] != "data:image/jpeg;base64,aW1hZ2U=" {
		t.Fatalf("base64 image = %#v", base64Image)
	}
	if content[2].(map[string]any)["text"] != "[Document: brief.pdf (application/pdf)]" {
		t.Fatalf("document = %#v", content[2])
	}
	urlImage := content[3].(map[string]any)["image_url"].(map[string]any)
	if urlImage["url"] != "https://example.com/page.png" {
		t.Fatalf("document image = %#v", urlImage)
	}
}

func TestToChatCompletionsPreservesUnknownTextBlock(t *testing.T) {
	got, err := ToChatCompletions(map[string]any{
		"model": "alpha",
		"messages": []any{map[string]any{
			"role": "user",
			"content": []any{
				map[string]any{"type": "custom_text", "text": "fallback text"},
			},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	messages := got["messages"].([]any)
	if len(messages) != 1 {
		t.Fatalf("messages = %#v", messages)
	}
	message := messages[0].(map[string]any)
	if message["content"] != "fallback text" {
		t.Fatalf("message = %#v", message)
	}
}

func TestToChatCompletionsPreservesEmptyContentMessages(t *testing.T) {
	got, err := ToChatCompletions(map[string]any{
		"model": "alpha",
		"messages": []any{
			map[string]any{"role": "user", "content": []any{}},
			map[string]any{"role": "assistant", "content": []any{}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	messages := got["messages"].([]any)
	if len(messages) != 2 {
		t.Fatalf("messages = %#v", messages)
	}
	if messages[0].(map[string]any)["role"] != "user" || messages[0].(map[string]any)["content"] != "" {
		t.Fatalf("user = %#v", messages[0])
	}
	if messages[1].(map[string]any)["role"] != "assistant" || messages[1].(map[string]any)["content"] != "" {
		t.Fatalf("assistant = %#v", messages[1])
	}
}

func TestToChatCompletionsConvertsThinkingBlocks(t *testing.T) {
	got, err := ToChatCompletions(map[string]any{
		"model": "alpha",
		"messages": []any{map[string]any{
			"role": "assistant",
			"content": []any{
				map[string]any{"type": "thinking", "thinking": "hidden plan", "signature": "sig-1"},
				map[string]any{"type": "redacted_thinking", "data": "opaque"},
				map[string]any{"type": "text", "text": "visible answer"},
			},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	messages := got["messages"].([]any)
	if len(messages) != 1 {
		t.Fatalf("messages = %#v", messages)
	}
	assistant := messages[0].(map[string]any)
	if assistant["role"] != "assistant" || assistant["content"] != "visible answer" {
		t.Fatalf("assistant = %#v", assistant)
	}
	if assistant["reasoning_content"] != "hidden plan" || assistant["reasoning_signature"] != "sig-1" {
		t.Fatalf("reasoning fields = %#v", assistant)
	}
	details := assistant["reasoning_details"].([]any)
	if len(details) != 2 {
		t.Fatalf("reasoning details = %#v", details)
	}
	first := details[0].(map[string]any)
	if first["type"] != "thinking" || first["thinking"] != "hidden plan" || first["signature"] != "sig-1" {
		t.Fatalf("thinking detail = %#v", first)
	}
	second := details[1].(map[string]any)
	if second["type"] != "redacted_thinking" || second["data"] != "opaque" {
		t.Fatalf("redacted detail = %#v", second)
	}
}

func TestToChatCompletionsPassesThroughUnknownToolChoiceString(t *testing.T) {
	got, err := ToChatCompletions(map[string]any{
		"model":       "alpha",
		"tool_choice": "future_mode",
		"messages": []any{map[string]any{
			"role":    "user",
			"content": "hello",
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got["tool_choice"] != "future_mode" {
		t.Fatalf("tool_choice = %#v", got["tool_choice"])
	}
}

func TestFromChatCompletionTranslatesOpenAIResponse(t *testing.T) {
	payload := map[string]any{
		"id":    "chatcmpl-abc123",
		"model": "alpha",
		"choices": []any{map[string]any{
			"finish_reason": "tool_calls",
			"message": map[string]any{
				"content": "Checking",
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
			"prompt_tokens":     float64(7),
			"completion_tokens": float64(3),
			"prompt_tokens_details": map[string]any{
				"cached_tokens": float64(2),
			},
		},
	}

	got := FromChatCompletion(payload)
	if got["id"] != "msg_abc123" || got["type"] != "message" || got["stop_reason"] != "tool_use" {
		t.Fatalf("response = %#v", got)
	}
	content := got["content"].([]any)
	if content[0].(map[string]any)["text"] != "Checking" {
		t.Fatalf("content = %#v", content)
	}
	tool := content[1].(map[string]any)
	if tool["type"] != "tool_use" || tool["id"] != "call_1" || tool["name"] != "lookup" {
		t.Fatalf("tool = %#v", tool)
	}
	input := tool["input"].(map[string]any)
	if input["q"] != "x" {
		t.Fatalf("input = %#v", input)
	}
	usage := got["usage"].(map[string]any)
	if usage["input_tokens"] != float64(7) || usage["output_tokens"] != float64(3) || usage["cache_read_input_tokens"] != float64(2) {
		t.Fatalf("usage = %#v", usage)
	}
}

func TestFromChatCompletionConvertsOpenAIImageURLContent(t *testing.T) {
	got := FromChatCompletion(map[string]any{
		"id":    "chatcmpl-images",
		"model": "alpha",
		"choices": []any{map[string]any{
			"finish_reason": "stop",
			"message": map[string]any{
				"content": []any{
					map[string]any{"type": "text", "text": "images"},
					map[string]any{"type": "image_url", "image_url": map[string]any{"url": "data:image/png;base64,aW1hZ2U="}},
					map[string]any{"type": "image_url", "image_url": map[string]any{"url": "https://example.com/image.png"}},
				},
			},
		}},
	})
	content := got["content"].([]any)
	if len(content) != 3 {
		t.Fatalf("content = %#v", content)
	}
	base64Image := content[1].(map[string]any)
	base64Source := base64Image["source"].(map[string]any)
	if base64Image["type"] != "image" || base64Source["type"] != "base64" || base64Source["media_type"] != "image/png" || base64Source["data"] != "aW1hZ2U=" {
		t.Fatalf("base64 image = %#v", base64Image)
	}
	urlImage := content[2].(map[string]any)
	urlSource := urlImage["source"].(map[string]any)
	if urlImage["type"] != "image" || urlSource["type"] != "url" || urlSource["url"] != "https://example.com/image.png" {
		t.Fatalf("url image = %#v", urlImage)
	}
}

func TestFromChatCompletionConvertsReasoningToThinkingBlocks(t *testing.T) {
	payload := map[string]any{
		"id":    "chatcmpl-thinking",
		"model": "alpha",
		"choices": []any{map[string]any{
			"finish_reason": "stop",
			"message": map[string]any{
				"reasoning_content":   "hidden plan",
				"reasoning_signature": "sig-1",
				"content":             "visible answer",
			},
		}},
	}

	got := FromChatCompletion(payload)
	content := got["content"].([]any)
	if len(content) != 2 {
		t.Fatalf("content = %#v", content)
	}
	thinking := content[0].(map[string]any)
	if thinking["type"] != "thinking" || thinking["thinking"] != "hidden plan" || thinking["signature"] != "sig-1" {
		t.Fatalf("thinking = %#v", thinking)
	}
	text := content[1].(map[string]any)
	if text["type"] != "text" || text["text"] != "visible answer" {
		t.Fatalf("text = %#v", text)
	}
}

func TestFromChatCompletionPreservesReasoningDetails(t *testing.T) {
	payload := map[string]any{
		"id":    "chatcmpl-details",
		"model": "alpha",
		"choices": []any{map[string]any{
			"finish_reason": "stop",
			"message": map[string]any{
				"reasoning_content": "duplicate fallback",
				"reasoning_details": []any{
					map[string]any{"type": "thinking", "thinking": "hidden plan", "signature": "sig-1"},
					map[string]any{"type": "redacted_thinking", "data": "opaque"},
				},
				"content": "visible answer",
			},
		}},
	}

	got := FromChatCompletion(payload)
	content := got["content"].([]any)
	if len(content) != 3 {
		t.Fatalf("content = %#v", content)
	}
	thinking := content[0].(map[string]any)
	if thinking["type"] != "thinking" || thinking["thinking"] != "hidden plan" || thinking["signature"] != "sig-1" {
		t.Fatalf("thinking = %#v", thinking)
	}
	redacted := content[1].(map[string]any)
	if redacted["type"] != "redacted_thinking" || redacted["data"] != "opaque" {
		t.Fatalf("redacted = %#v", redacted)
	}
	if content[2].(map[string]any)["text"] != "visible answer" {
		t.Fatalf("text = %#v", content[2])
	}
}

func TestFromChatCompletionAppliesTopLevelSignatureToRedactedThinking(t *testing.T) {
	payload := map[string]any{
		"id":    "chatcmpl-redacted-signature",
		"model": "alpha",
		"choices": []any{map[string]any{
			"finish_reason": "stop",
			"message": map[string]any{
				"reasoning_signature": "sig-redacted",
				"reasoning_details": []any{
					map[string]any{"type": "redacted_thinking", "data": "opaque"},
				},
				"content": "visible answer",
			},
		}},
	}

	got := FromChatCompletion(payload)
	content := got["content"].([]any)
	if len(content) != 2 {
		t.Fatalf("content = %#v", content)
	}
	redacted := content[0].(map[string]any)
	if redacted["type"] != "redacted_thinking" || redacted["data"] != "opaque" || redacted["signature"] != "sig-redacted" {
		t.Fatalf("redacted = %#v", redacted)
	}
	if content[1].(map[string]any)["text"] != "visible answer" {
		t.Fatalf("text = %#v", content[1])
	}
}

func TestFromChatCompletionRefusalAndEmptyContent(t *testing.T) {
	got := FromChatCompletion(map[string]any{
		"id":    "chatcmpl-refusal",
		"model": "alpha",
		"choices": []any{map[string]any{
			"finish_reason": "content_filter",
			"message": map[string]any{
				"content": "",
				"refusal": "I cannot help with that.",
			},
		}},
		"usage": map[string]any{"prompt_tokens": float64(2), "completion_tokens": float64(0), "total_tokens": float64(2)},
	})

	if got["stop_reason"] != "refusal" {
		t.Fatalf("stop_reason = %#v", got["stop_reason"])
	}
	content := got["content"].([]any)
	if len(content) != 1 {
		t.Fatalf("content = %#v", content)
	}
	if content[0].(map[string]any)["type"] != "text" || content[0].(map[string]any)["text"] != "I cannot help with that." {
		t.Fatalf("content = %#v", content)
	}
	usage := got["usage"].(map[string]any)
	if usage["input_tokens"] != float64(2) || usage["output_tokens"] != float64(0) {
		t.Fatalf("usage = %#v", usage)
	}
}

func TestFromChatCompletionMapsInlineRefusalContent(t *testing.T) {
	got := FromChatCompletion(map[string]any{
		"id":    "chatcmpl-inline-refusal",
		"model": "alpha",
		"choices": []any{map[string]any{
			"finish_reason": "content_filter",
			"message": map[string]any{
				"content": []any{
					map[string]any{"type": "refusal", "refusal": "I cannot provide that."},
				},
			},
		}},
	})

	if got["stop_reason"] != "refusal" {
		t.Fatalf("stop_reason = %#v", got["stop_reason"])
	}
	content := got["content"].([]any)
	if len(content) != 1 {
		t.Fatalf("content = %#v", content)
	}
	block := content[0].(map[string]any)
	if block["type"] != "text" || block["text"] != "I cannot provide that." {
		t.Fatalf("content = %#v", content)
	}
}
