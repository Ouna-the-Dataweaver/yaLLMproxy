package messages

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/google/uuid"
)

func ToChatCompletions(payload map[string]any) (map[string]any, error) {
	openaiMessages := make([]any, 0)
	if system := convertSystem(payload["system"]); len(system) > 0 {
		openaiMessages = append(openaiMessages, system)
	}
	rawMessages, _ := payload["messages"].([]any)
	for _, raw := range rawMessages {
		msg, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		role := strings.TrimSpace(fmt.Sprint(msg["role"]))
		if role == "" {
			role = "user"
		}
		content := msg["content"]
		if text, ok := content.(string); ok {
			openaiMessages = append(openaiMessages, map[string]any{"role": role, "content": text})
			continue
		}
		blocks, ok := content.([]any)
		if !ok {
			openaiMessages = append(openaiMessages, map[string]any{"role": role, "content": stringifyContent(content)})
			continue
		}
		if len(blocks) == 0 {
			openaiMessages = append(openaiMessages, map[string]any{"role": role, "content": ""})
			continue
		}
		toolResults, otherBlocks := splitToolResults(blocks)
		for _, block := range toolResults {
			openaiMessages = append(openaiMessages, convertToolResult(block))
		}
		converted := convertContentBlocks(otherBlocks)
		if role == "assistant" {
			out := map[string]any{"role": "assistant"}
			if converted.Content != nil {
				out["content"] = converted.Content
			}
			if converted.ReasoningContent != "" {
				out["reasoning_content"] = converted.ReasoningContent
			}
			if converted.ReasoningSignature != "" {
				out["reasoning_signature"] = converted.ReasoningSignature
			}
			if len(converted.ReasoningDetails) > 0 {
				out["reasoning_details"] = converted.ReasoningDetails
			}
			if len(converted.ToolCalls) > 0 {
				out["tool_calls"] = converted.ToolCalls
			}
			if out["content"] != nil || out["tool_calls"] != nil || out["reasoning_content"] != nil || out["reasoning_details"] != nil {
				openaiMessages = append(openaiMessages, out)
			}
			continue
		}
		if converted.Content != nil {
			openaiMessages = append(openaiMessages, map[string]any{"role": role, "content": converted.Content})
		}
	}
	result := map[string]any{
		"model":    payload["model"],
		"messages": openaiMessages,
	}
	copyParam(payload, result, "max_tokens", "max_tokens")
	copyParam(payload, result, "temperature", "temperature")
	copyParam(payload, result, "top_p", "top_p")
	copyParam(payload, result, "stream", "stream")
	copyParam(payload, result, "stop_sequences", "stop")
	if tools := convertTools(payload["tools"]); len(tools) > 0 {
		result["tools"] = tools
	}
	if choice := convertToolChoice(payload["tool_choice"]); choice != nil {
		result["tool_choice"] = choice
	}
	if metadata, ok := payload["metadata"].(map[string]any); ok {
		if userID, ok := metadata["user_id"].(string); ok && strings.TrimSpace(userID) != "" {
			result["user"] = userID
		}
	}
	return result, nil
}

func FromChatCompletion(payload map[string]any) map[string]any {
	openaiID := strings.TrimSpace(fmt.Sprint(payload["id"]))
	if openaiID == "" || openaiID == "<nil>" {
		openaiID = "chatcmpl-" + uuid.NewString()
	}
	model := ""
	if value, ok := payload["model"].(string); ok {
		model = value
	}
	choice := firstMap(payload["choices"])
	message := map[string]any{}
	if nested, ok := choice["message"].(map[string]any); ok {
		message = nested
	}
	finishReason, _ := choice["finish_reason"].(string)
	content := openAIContentToBlocks(message)
	if len(content) == 0 {
		content = []any{map[string]any{"type": "text", "text": ""}}
	}
	return map[string]any{
		"id":            "msg_" + strings.TrimPrefix(openaiID, "chatcmpl-"),
		"type":          "message",
		"role":          "assistant",
		"content":       content,
		"model":         model,
		"stop_reason":   convertStopReason(finishReason),
		"stop_sequence": nil,
		"usage":         anthropicUsageFromChatUsage(payload["usage"]),
	}
}

func convertSystem(value any) map[string]any {
	switch typed := value.(type) {
	case string:
		return map[string]any{"role": "system", "content": typed}
	case []any:
		parts := make([]string, 0)
		for _, raw := range typed {
			block, ok := raw.(map[string]any)
			if !ok || block["type"] != "text" {
				continue
			}
			if text, ok := block["text"].(string); ok {
				parts = append(parts, text)
			}
		}
		if len(parts) > 0 {
			return map[string]any{"role": "system", "content": strings.Join(parts, "\n")}
		}
	}
	return nil
}

func splitToolResults(blocks []any) ([]map[string]any, []any) {
	toolResults := make([]map[string]any, 0)
	otherBlocks := make([]any, 0, len(blocks))
	for _, raw := range blocks {
		block, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if block["type"] == "tool_result" {
			toolResults = append(toolResults, block)
		} else {
			otherBlocks = append(otherBlocks, block)
		}
	}
	return toolResults, otherBlocks
}

func convertToolResult(block map[string]any) map[string]any {
	content := toolResultContent(block["content"])
	if isError, _ := block["is_error"].(bool); isError {
		content = "[Error] " + content
	}
	return map[string]any{
		"role":         "tool",
		"tool_call_id": stringifyContent(block["tool_use_id"]),
		"content":      content,
	}
}

func toolResultContent(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case []any:
		parts := make([]string, 0)
		for _, raw := range typed {
			block, ok := raw.(map[string]any)
			if !ok || block["type"] != "text" {
				continue
			}
			if text, ok := block["text"].(string); ok {
				parts = append(parts, text)
			}
		}
		return strings.Join(parts, "\n")
	default:
		return stringifyContent(value)
	}
}

type convertedContentBlocks struct {
	Content            any
	ToolCalls          []any
	ReasoningContent   string
	ReasoningSignature string
	ReasoningDetails   []any
}

func convertContentBlocks(blocks []any) convertedContentBlocks {
	contentParts := make([]any, 0)
	toolCalls := make([]any, 0)
	reasoningParts := make([]string, 0)
	reasoningDetails := make([]any, 0)
	reasoningSignature := ""
	for _, raw := range blocks {
		block, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		switch block["type"] {
		case "text":
			contentParts = append(contentParts, map[string]any{"type": "text", "text": stringifyContent(block["text"])})
		case "image":
			contentParts = append(contentParts, convertImage(block))
		case "document":
			contentParts = append(contentParts, convertDocument(block))
		case "tool_use":
			toolCalls = append(toolCalls, map[string]any{
				"id":   firstNonEmptyString(block["id"], "call_"+uuid.NewString()[:8]),
				"type": "function",
				"function": map[string]any{
					"name":      stringifyContent(block["name"]),
					"arguments": serializeJSONValue(block["input"]),
				},
			})
		case "thinking":
			text := firstNonEmptyString(block["thinking"], stringifyContent(block["text"]))
			if text != "" {
				reasoningParts = append(reasoningParts, text)
			}
			detail := map[string]any{"type": "thinking"}
			if text != "" {
				detail["thinking"] = text
			}
			if signature := stringifyContent(block["signature"]); signature != "" {
				detail["signature"] = signature
				if reasoningSignature == "" {
					reasoningSignature = signature
				}
			}
			reasoningDetails = append(reasoningDetails, detail)
		case "redacted_thinking":
			detail := map[string]any{"type": "redacted_thinking"}
			if data := block["data"]; data != nil {
				detail["data"] = data
			}
			if signature := stringifyContent(block["signature"]); signature != "" {
				detail["signature"] = signature
				if reasoningSignature == "" {
					reasoningSignature = signature
				}
			}
			reasoningDetails = append(reasoningDetails, detail)
		case "tool_result":
			continue
		default:
			if _, ok := block["text"]; ok {
				contentParts = append(contentParts, map[string]any{"type": "text", "text": stringifyContent(block["text"])})
			}
		}
	}
	if len(contentParts) == 1 {
		if part, ok := contentParts[0].(map[string]any); ok && part["type"] == "text" {
			return convertedContentBlocks{
				Content:            part["text"],
				ToolCalls:          toolCalls,
				ReasoningContent:   strings.Join(reasoningParts, ""),
				ReasoningSignature: reasoningSignature,
				ReasoningDetails:   reasoningDetails,
			}
		}
	}
	result := convertedContentBlocks{
		ToolCalls:          toolCalls,
		ReasoningContent:   strings.Join(reasoningParts, ""),
		ReasoningSignature: reasoningSignature,
		ReasoningDetails:   reasoningDetails,
	}
	if len(contentParts) == 0 {
		return result
	}
	result.Content = contentParts
	return result
}

func convertImage(block map[string]any) map[string]any {
	source, _ := block["source"].(map[string]any)
	sourceType, _ := source["type"].(string)
	url := ""
	switch sourceType {
	case "base64":
		mediaType := firstNonEmptyString(source["media_type"], "image/png")
		url = "data:" + mediaType + ";base64," + stringifyContent(source["data"])
	case "url":
		url = stringifyContent(source["url"])
	default:
		url = firstNonEmptyString(source["url"], stringifyContent(source["data"]))
	}
	return map[string]any{"type": "image_url", "image_url": map[string]any{"url": url}}
}

func convertDocument(block map[string]any) map[string]any {
	source, _ := block["source"].(map[string]any)
	mediaType := firstNonEmptyString(source["media_type"], "application/pdf")
	if strings.HasPrefix(mediaType, "image/") {
		return convertImage(map[string]any{"source": source})
	}
	name := firstNonEmptyString(block["name"], "document")
	return map[string]any{"type": "text", "text": "[Document: " + name + " (" + mediaType + ")]"}
}

func convertTools(value any) []any {
	rawTools, ok := value.([]any)
	if !ok {
		return nil
	}
	tools := make([]any, 0, len(rawTools))
	for _, raw := range rawTools {
		tool, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		parameters := tool["input_schema"]
		if parameters == nil {
			parameters = map[string]any{}
		}
		tools = append(tools, map[string]any{
			"type": "function",
			"function": map[string]any{
				"name":        stringifyContent(tool["name"]),
				"description": stringifyContent(tool["description"]),
				"parameters":  parameters,
			},
		})
	}
	return tools
}

func convertToolChoice(value any) any {
	switch typed := value.(type) {
	case string:
		if typed == "any" {
			return "required"
		}
		return typed
	case map[string]any:
		switch typed["type"] {
		case "tool":
			return map[string]any{"type": "function", "function": map[string]any{"name": stringifyContent(typed["name"])}}
		case "function":
			name := stringifyContent(typed["name"])
			if function, ok := typed["function"].(map[string]any); ok {
				name = firstNonEmptyString(function["name"], name)
			}
			return map[string]any{"type": "function", "function": map[string]any{"name": name}}
		case "any", "required":
			return "required"
		case "auto":
			return "auto"
		case "none":
			return "none"
		}
	}
	return nil
}

func openAIContentToBlocks(message map[string]any) []any {
	blocks := make([]any, 0)
	blocks = append(blocks, openAIReasoningToAnthropicBlocks(message)...)
	content := message["content"]
	switch typed := content.(type) {
	case string:
		if typed != "" {
			blocks = append(blocks, map[string]any{"type": "text", "text": typed})
		}
	case []any:
		for _, raw := range typed {
			part, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			switch part["type"] {
			case "text", "output_text":
				blocks = append(blocks, map[string]any{"type": "text", "text": stringifyContent(part["text"])})
			case "refusal":
				if text := firstNonEmptyString(part["refusal"], stringifyContent(part["text"])); text != "" {
					blocks = append(blocks, map[string]any{"type": "text", "text": text})
				}
			case "image_url":
				if block := openAIImagePartToAnthropicBlock(part); block != nil {
					blocks = append(blocks, block)
				}
			case "reasoning", "reasoning_text", "summary_text":
				blocks = append(blocks, openAIReasoningPartToAnthropicBlocks(part)...)
			}
		}
	}
	if refusal := stringifyContent(message["refusal"]); refusal != "" {
		blocks = append(blocks, map[string]any{"type": "text", "text": refusal})
	}
	if toolCalls, ok := message["tool_calls"].([]any); ok {
		for _, raw := range toolCalls {
			call, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			function, _ := call["function"].(map[string]any)
			blocks = append(blocks, map[string]any{
				"type":  "tool_use",
				"id":    firstNonEmptyString(call["id"], "toolu_"+uuid.NewString()[:8]),
				"name":  stringifyContent(function["name"]),
				"input": parseToolArguments(function["arguments"]),
			})
		}
	}
	return blocks
}

func openAIImagePartToAnthropicBlock(part map[string]any) map[string]any {
	url := ""
	switch typed := part["image_url"].(type) {
	case string:
		url = typed
	case map[string]any:
		url = stringifyContent(typed["url"])
	}
	if url == "" {
		return nil
	}
	source := map[string]any{}
	if mediaType, data, ok := parseImageDataURL(url); ok {
		source["type"] = "base64"
		source["media_type"] = mediaType
		source["data"] = data
	} else {
		source["type"] = "url"
		source["url"] = url
	}
	return map[string]any{"type": "image", "source": source}
}

func parseImageDataURL(url string) (string, string, bool) {
	payload, ok := strings.CutPrefix(url, "data:")
	if !ok {
		return "", "", false
	}
	mediaType, data, ok := strings.Cut(payload, ";base64,")
	if !ok || mediaType == "" {
		return "", "", false
	}
	return mediaType, data, true
}

func openAIReasoningToAnthropicBlocks(message map[string]any) []any {
	if message == nil {
		return nil
	}
	blocks := make([]any, 0)
	hasThinking := false
	for _, block := range openAIReasoningDetailsToAnthropicBlocks(message["reasoning_details"]) {
		if typed, ok := block.(map[string]any); ok && typed["type"] == "thinking" {
			hasThinking = true
		}
		blocks = append(blocks, block)
	}
	if !hasThinking {
		for _, text := range openAIReasoningTextChunks(message) {
			block := map[string]any{"type": "thinking", "thinking": text}
			if signature := stringifyContent(message["reasoning_signature"]); signature != "" {
				block["signature"] = signature
			}
			blocks = append(blocks, block)
			hasThinking = true
		}
	}
	if signature := stringifyContent(message["reasoning_signature"]); signature != "" {
		for _, raw := range blocks {
			block, ok := raw.(map[string]any)
			if ok && (block["type"] == "thinking" || block["type"] == "redacted_thinking") && stringifyContent(block["signature"]) == "" {
				block["signature"] = signature
				break
			}
		}
	}
	return blocks
}

func openAIReasoningDetailsToAnthropicBlocks(value any) []any {
	switch typed := value.(type) {
	case nil:
		return nil
	case string:
		if typed == "" {
			return nil
		}
		return []any{map[string]any{"type": "thinking", "thinking": typed}}
	case []any:
		blocks := make([]any, 0)
		for _, item := range typed {
			blocks = append(blocks, openAIReasoningDetailsToAnthropicBlocks(item)...)
		}
		return blocks
	case map[string]any:
		switch typed["type"] {
		case "redacted_thinking":
			block := map[string]any{"type": "redacted_thinking"}
			if data := typed["data"]; data != nil {
				block["data"] = data
			}
			if signature := stringifyContent(typed["signature"]); signature != "" {
				block["signature"] = signature
			}
			return []any{block}
		case "thinking":
			text := firstNonEmptyString(typed["thinking"], firstNonEmptyString(typed["text"], stringifyContent(typed["content"])))
			if text == "" {
				return nil
			}
			block := map[string]any{"type": "thinking", "thinking": text}
			if signature := stringifyContent(typed["signature"]); signature != "" {
				block["signature"] = signature
			}
			return []any{block}
		case "reasoning_text", "summary_text", "reasoning":
			return openAIReasoningPartToAnthropicBlocks(typed)
		default:
			for _, key := range []string{"reasoning_content", "thinking", "text", "content", "reasoning"} {
				if blocks := openAIReasoningDetailsToAnthropicBlocks(typed[key]); len(blocks) > 0 {
					return blocks
				}
			}
		}
	}
	return nil
}

func openAIReasoningPartToAnthropicBlocks(part map[string]any) []any {
	if part == nil {
		return nil
	}
	switch part["type"] {
	case "reasoning_text", "summary_text":
		if text := stringifyContent(part["text"]); text != "" {
			return []any{map[string]any{"type": "thinking", "thinking": text}}
		}
	case "reasoning":
		if blocks := openAIReasoningDetailsToAnthropicBlocks(part["summary"]); len(blocks) > 0 {
			return blocks
		}
		for _, key := range []string{"reasoning_content", "thinking", "text", "content"} {
			if blocks := openAIReasoningDetailsToAnthropicBlocks(part[key]); len(blocks) > 0 {
				return blocks
			}
		}
	}
	return nil
}

func openAIReasoningTextChunks(message map[string]any) []string {
	if message == nil {
		return nil
	}
	chunks := make([]string, 0)
	for _, key := range []string{"reasoning_content", "reasoning"} {
		chunks = append(chunks, reasoningTextChunksFromValue(message[key])...)
	}
	return compactStrings(chunks)
}

func reasoningTextChunksFromValue(value any) []string {
	switch typed := value.(type) {
	case nil:
		return nil
	case string:
		return compactStrings([]string{typed})
	case []any:
		chunks := make([]string, 0)
		for _, item := range typed {
			chunks = append(chunks, reasoningTextChunksFromValue(item)...)
		}
		return compactStrings(chunks)
	case map[string]any:
		for _, key := range []string{"reasoning_content", "thinking", "text", "content", "reasoning"} {
			if chunks := reasoningTextChunksFromValue(typed[key]); len(chunks) > 0 {
				return chunks
			}
		}
	}
	return nil
}

func parseToolArguments(value any) any {
	if raw, ok := value.(string); ok {
		decoded := map[string]any{}
		if err := json.Unmarshal([]byte(raw), &decoded); err == nil {
			return decoded
		}
		return map[string]any{"raw": raw}
	}
	if value == nil {
		return map[string]any{}
	}
	return value
}

func anthropicUsageFromChatUsage(value any) map[string]any {
	usage, _ := value.(map[string]any)
	out := map[string]any{
		"input_tokens":  numberOrZero(usage["prompt_tokens"]),
		"output_tokens": numberOrZero(usage["completion_tokens"]),
	}
	if details, ok := usage["prompt_tokens_details"].(map[string]any); ok {
		if value, ok := details["cached_tokens"]; ok {
			out["cache_read_input_tokens"] = numberOrZero(value)
		}
		if value, ok := firstPresent(details, "cache_creation_input_tokens", "cache_creation_tokens"); ok {
			out["cache_creation_input_tokens"] = numberOrZero(value)
		}
	}
	if value, ok := firstPresent(usage, "cache_read_input_tokens", "cached_tokens"); ok {
		out["cache_read_input_tokens"] = numberOrZero(value)
	}
	if value, ok := firstPresent(usage, "cache_creation_input_tokens", "cache_creation_tokens"); ok {
		out["cache_creation_input_tokens"] = numberOrZero(value)
	}
	return out
}

func firstPresent(values map[string]any, keys ...string) (any, bool) {
	for _, key := range keys {
		if value, ok := values[key]; ok {
			return value, true
		}
	}
	return nil, false
}

func convertStopReason(finishReason string) string {
	switch finishReason {
	case "", "stop":
		return "end_turn"
	case "length":
		return "max_tokens"
	case "tool_calls", "function_call":
		return "tool_use"
	case "content_filter":
		return "refusal"
	default:
		return "end_turn"
	}
}

func firstMap(value any) map[string]any {
	items, ok := value.([]any)
	if !ok || len(items) == 0 {
		return map[string]any{}
	}
	item, _ := items[0].(map[string]any)
	if item == nil {
		return map[string]any{}
	}
	return item
}

func copyParam(src map[string]any, dst map[string]any, from string, to string) {
	if value, ok := src[from]; ok {
		dst[to] = value
	}
}

func serializeJSONValue(value any) string {
	if text, ok := value.(string); ok {
		return text
	}
	if value == nil {
		return "{}"
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return "{}"
	}
	return string(raw)
}

func stringifyContent(value any) string {
	if value == nil {
		return ""
	}
	if text, ok := value.(string); ok {
		return text
	}
	return fmt.Sprint(value)
}

func firstNonEmptyString(value any, fallback string) string {
	text := strings.TrimSpace(stringifyContent(value))
	if text == "" || text == "<nil>" {
		return fallback
	}
	return text
}

func numberOrZero(value any) any {
	if value == nil {
		return 0
	}
	return value
}

func compactStrings(values []string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		if value == "" {
			continue
		}
		out = append(out, value)
	}
	return out
}
