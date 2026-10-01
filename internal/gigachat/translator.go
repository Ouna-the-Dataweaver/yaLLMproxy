package gigachat

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
)

var trailingCommaRE = regexp.MustCompile(`,\s*([}\]])`)

var toolEmulationSchema = map[string]any{
	"action":    "call_tool | final_answer",
	"tool":      "tool name when action is call_tool",
	"arguments": "object with tool arguments when action is call_tool",
	"content":   "user-facing text when action is final_answer",
}

var terminalToolNames = map[string]bool{
	"extract_assignments": true,
	"analyse_reports":     true,
}

const terminalToolResultInlineLimit = 2000

func OpenAIChatToGigaChat(payload map[string]any, defaultModel string) (map[string]any, error) {
	messages, err := OpenAIMessagesToGigaChat(payload["messages"])
	if err != nil {
		return nil, err
	}
	model := firstNonEmpty(stringOrEmpty(payload["model"]), defaultModel)
	converted := map[string]any{
		"model":           model,
		"messages":        messages,
		"profanity_check": false,
	}
	for _, key := range []string{"temperature", "top_p", "max_tokens", "presence_penalty", "frequency_penalty", "stop"} {
		if value, ok := payload[key]; ok && value != nil {
			converted[key] = value
		}
	}
	if payload["stream"] == true {
		converted["stream"] = true
	}
	if _, ok := converted["max_tokens"]; !ok {
		if value, ok := payload["max_completion_tokens"]; ok && value != nil {
			converted["max_tokens"] = value
		}
	}
	if functions := ToolsToGigaChatFunctions(payload["tools"]); len(functions) > 0 {
		converted["functions"] = functions
		switch choice := payload["tool_choice"].(type) {
		case map[string]any:
			function, _ := choice["function"].(map[string]any)
			if name := stringOrEmpty(function["name"]); name != "" {
				converted["function_call"] = map[string]any{"name": name}
			} else {
				converted["function_call"] = "auto"
			}
		case string:
			if choice == "none" {
				converted["function_call"] = "none"
			} else {
				converted["function_call"] = "auto"
			}
		default:
			converted["function_call"] = "auto"
		}
	}
	return converted, nil
}

func OpenAIChatToGigaChatToolEmulation(payload map[string]any, defaultModel string, retryError string) (map[string]any, error) {
	messages, err := openAIMessagesToToolEmulationGigaChat(payload["messages"], payload["tools"], retryError)
	if err != nil {
		return nil, err
	}
	model := firstNonEmpty(stringOrEmpty(payload["model"]), defaultModel)
	converted := map[string]any{
		"model":           model,
		"messages":        messages,
		"profanity_check": false,
	}
	for _, key := range []string{"temperature", "top_p", "max_tokens", "presence_penalty", "frequency_penalty", "stop"} {
		if value, ok := payload[key]; ok && value != nil {
			converted[key] = value
		}
	}
	if _, ok := converted["max_tokens"]; !ok {
		if value, ok := payload["max_completion_tokens"]; ok && value != nil {
			converted["max_tokens"] = value
		}
	}
	return converted, nil
}

func ParseToolEmulationResponse(content string, tools any) (map[string]any, error) {
	data, err := loadsModelJSON(content)
	if err != nil {
		if plain := strings.TrimSpace(content); plain != "" {
			return map[string]any{"action": "final_answer", "content": plain, "fallback": "plain_text"}, nil
		}
		return nil, err
	}
	obj, ok := data.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("tool emulation response must be a JSON object")
	}
	action := stringOrEmpty(obj["action"])
	knownTools := toolNames(tools)
	if knownTools[action] {
		obj["tool"] = firstNonEmpty(stringOrEmpty(obj["tool"]), action)
		action = "call_tool"
	} else if action == "" && knownTools[stringOrEmpty(obj["tool"])] {
		action = "call_tool"
	}
	switch action {
	case "final_answer":
		content, ok := obj["content"].(string)
		if !ok {
			return nil, fmt.Errorf("tool emulation final_answer requires string field 'content'")
		}
		return map[string]any{"action": "final_answer", "content": content}, nil
	case "call_tool":
		toolName := firstNonEmpty(stringOrEmpty(obj["tool"]), stringOrEmpty(obj["tool_name"]))
		if toolName == "" {
			return nil, fmt.Errorf("tool emulation call_tool requires string field 'tool'")
		}
		if len(knownTools) > 0 && !knownTools[toolName] {
			return nil, fmt.Errorf("unknown tool %q", toolName)
		}
		arguments := obj["arguments"]
		if arguments == nil {
			arguments = obj["args"]
		}
		if arguments == nil {
			arguments = map[string]any{}
		}
		if raw, ok := arguments.(string); ok {
			parsed, err := loadsModelJSON(raw)
			if err != nil {
				return nil, err
			}
			arguments = parsed
		}
		argMap, ok := arguments.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("tool emulation call_tool requires object field 'arguments'")
		}
		return map[string]any{"action": "call_tool", "tool": toolName, "arguments": argMap}, nil
	default:
		return nil, fmt.Errorf("tool emulation response field 'action' must be either 'call_tool' or 'final_answer'")
	}
}

func ToolEmulationResultToOpenAIResponse(result map[string]any, requestModel string, usage any) map[string]any {
	var message map[string]any
	finishReason := "stop"
	if result["action"] == "call_tool" {
		message = map[string]any{
			"role":    "assistant",
			"content": nil,
			"tool_calls": []any{map[string]any{
				"id":   "call_" + strings.ReplaceAll(uuid.NewString(), "-", ""),
				"type": "function",
				"function": map[string]any{
					"name":      stringOrEmpty(result["tool"]),
					"arguments": argumentsToJSON(result["arguments"]),
				},
			}},
		}
		finishReason = "tool_calls"
	} else {
		message = map[string]any{"role": "assistant", "content": stringOrEmpty(result["content"])}
	}
	if usage == nil {
		usage = defaultUsage()
	}
	return map[string]any{
		"id":      "chatcmpl-" + strings.ReplaceAll(uuid.NewString(), "-", ""),
		"object":  "chat.completion",
		"created": time.Now().Unix(),
		"model":   requestModel,
		"choices": []any{map[string]any{
			"index":         0,
			"message":       message,
			"finish_reason": finishReason,
		}},
		"usage": usage,
	}
}

func OpenAIResponseToSSE(response map[string]any) [][]byte {
	choices, _ := asSlice(response["choices"])
	if len(choices) == 0 {
		return [][]byte{[]byte("data: [DONE]\n\n")}
	}
	choice, _ := choices[0].(map[string]any)
	message, _ := choice["message"].(map[string]any)
	delta := map[string]any{}
	if calls, ok := asSlice(message["tool_calls"]); ok && len(calls) > 0 {
		var deltas []any
		for _, rawCall := range calls {
			call, _ := rawCall.(map[string]any)
			deltas = append(deltas, map[string]any{
				"index":    0,
				"id":       call["id"],
				"type":     call["type"],
				"function": call["function"],
			})
		}
		delta["tool_calls"] = deltas
	} else if content := stringOrEmpty(message["content"]); content != "" {
		delta["content"] = content
	}
	chunk := map[string]any{
		"id":      response["id"],
		"object":  "chat.completion.chunk",
		"created": response["created"],
		"model":   response["model"],
		"choices": []any{map[string]any{
			"index":         choice["index"],
			"delta":         delta,
			"finish_reason": choice["finish_reason"],
		}},
		"usage": firstAny(response["usage"], defaultUsage()),
	}
	data, _ := json.Marshal(chunk)
	return [][]byte{append(append([]byte("data: "), data...), []byte("\n\n")...), []byte("data: [DONE]\n\n")}
}

func OpenAIMessagesToGigaChat(messages any) ([]map[string]any, error) {
	items, ok := asSlice(messages)
	if !ok {
		return nil, fmt.Errorf("messages must be a list")
	}
	toolCallNames := map[string]string{}
	var converted []map[string]any
	var systemContents []string
	for _, raw := range items {
		msg, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		role := stringOrEmpty(msg["role"])
		content := contentToText(msg["content"])
		switch role {
		case "system":
			if content != "" {
				systemContents = append(systemContents, content)
			}
		case "user":
			if content != "" {
				converted = append(converted, map[string]any{"role": "user", "content": content})
			}
		case "assistant":
			assistant := map[string]any{"role": "assistant", "content": content}
			if calls, ok := asSlice(msg["tool_calls"]); ok && len(calls) > 0 {
				if functionCall := openAIToolCallToGigaChatFunctionCall(calls[0]); functionCall != nil {
					assistant["function_call"] = functionCall
					if call, ok := calls[0].(map[string]any); ok {
						if id := stringOrEmpty(call["id"]); id != "" {
							toolCallNames[id] = stringOrEmpty(functionCall["name"])
						}
					}
				}
			}
			converted = append(converted, assistant)
		case "tool":
			name := stringOrEmpty(msg["name"])
			if name == "" {
				name = firstNonEmpty(toolCallNames[stringOrEmpty(msg["tool_call_id"])], "tool_result")
			}
			converted = append(converted, map[string]any{"role": "function", "name": name, "content": toolReturnContentToJSON(msg["content"])})
		case "function":
			name := firstNonEmpty(stringOrEmpty(msg["name"]), "tool_result")
			converted = append(converted, map[string]any{"role": "function", "name": name, "content": toolReturnContentToJSON(msg["content"])})
		}
	}
	if len(systemContents) > 0 {
		system := map[string]any{"role": "system", "content": strings.Join(systemContents, "\n\n")}
		converted = append([]map[string]any{system}, converted...)
	}
	if len(converted) == 0 {
		return nil, fmt.Errorf("GigaChat request must contain at least one message")
	}
	return converted, nil
}

func openAIMessagesToToolEmulationGigaChat(messages any, tools any, retryError string) ([]map[string]any, error) {
	items, ok := asSlice(messages)
	if !ok {
		return nil, fmt.Errorf("messages must be a list")
	}
	converted := []map[string]any{{"role": "system", "content": toolEmulationInstruction(tools)}}
	toolCallNames := map[string]string{}
	lastKind := ""
	lastToolResultName := ""
	for _, raw := range items {
		msg, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		role := stringOrEmpty(msg["role"])
		content := contentToText(msg["content"])
		switch role {
		case "system":
			if content != "" {
				converted[0]["content"] = stringOrEmpty(converted[0]["content"]) + "\n\n" + content
			}
		case "user":
			if content != "" {
				converted = append(converted, map[string]any{"role": "user", "content": content})
				lastKind = "user"
				lastToolResultName = ""
			}
		case "assistant":
			var parts []string
			if content != "" {
				parts = append(parts, content)
			}
			if calls, ok := asSlice(msg["tool_calls"]); ok {
				for _, rawCall := range calls {
					functionCall := openAIToolCallToGigaChatFunctionCall(rawCall)
					if functionCall == nil {
						continue
					}
					if call, ok := rawCall.(map[string]any); ok {
						if id := stringOrEmpty(call["id"]); id != "" {
							toolCallNames[id] = stringOrEmpty(functionCall["name"])
						}
					}
					name := stringOrEmpty(functionCall["name"])
					if !terminalToolNames[name] {
						parts = append(parts, "Previous tool call: "+mustJSONString(map[string]any{"tool": name, "arguments": functionCall["arguments"]}))
					}
				}
			}
			if len(parts) > 0 {
				converted = append(converted, map[string]any{"role": "assistant", "content": strings.Join(parts, "\n")})
				lastKind = "assistant"
				lastToolResultName = ""
			}
		case "tool":
			name := stringOrEmpty(msg["name"])
			if name == "" {
				name = firstNonEmpty(toolCallNames[stringOrEmpty(msg["tool_call_id"])], "tool_result")
			}
			converted = append(converted, map[string]any{"role": "user", "content": "Tool result from " + name + ":\n" + toolReturnContentToEmulationJSON(name, msg["content"])})
			lastKind = "tool_result"
			lastToolResultName = name
		case "function":
			name := firstNonEmpty(stringOrEmpty(msg["name"]), "tool_result")
			converted = append(converted, map[string]any{"role": "user", "content": "Tool result from " + name + ":\n" + toolReturnContentToEmulationJSON(name, msg["content"])})
			lastKind = "tool_result"
			lastToolResultName = name
		}
	}
	if lastKind == "tool_result" {
		appendSystemInstruction(converted, afterToolResultInstruction(lastToolResultName))
	}
	if retryError != "" {
		appendSystemInstruction(converted, retryInstruction(retryError))
	}
	return converted, nil
}

func toolEmulationInstruction(tools any) string {
	return strings.Join([]string{
		"You are operating in tool-call emulation mode. Native function calling/tools are not available.",
		"Return only one valid JSON object, without markdown or explanatory text.",
		"",
		"If you need to call a tool, return:",
		`{"action":"call_tool","tool":"tool_name","arguments":{}}`,
		"",
		"If you can answer the user without a tool, return:",
		`{"action":"final_answer","content":"answer text"}`,
		"",
		"Response schema:",
		mustJSONStringIndent(toolEmulationSchema),
		"",
		"Available tools:",
		mustJSONStringIndent(toolSpecsForPrompt(tools)),
	}, "\n")
}

func appendSystemInstruction(messages []map[string]any, instruction string) {
	if len(messages) == 0 {
		return
	}
	messages[0]["content"] = stringOrEmpty(messages[0]["content"]) + "\n\n" + instruction
}

func afterToolResultInstruction(toolName string) string {
	if terminalToolNames[toolName] {
		return "Rule after a terminal tool result: the last result came from `" + toolName + "`. Return only JSON with action=\"final_answer\" and a concise user-facing message. Do not call tools again unless the tool result explicitly asks for another tool."
	}
	return "Rule after a tool result: use the last tool result for the next step. If there is enough information to answer, return JSON with action=\"final_answer\". Call another tool only when it is necessary."
}

func retryInstruction(retryError string) string {
	return "Internal format correction: the previous answer failed validation. Do not mention validation, retries, JSON parsing, or tool-calling internals to the user. Technical reason: " + retryError + ". Return exactly one valid JSON object matching the schema."
}

func toolSpecsForPrompt(tools any) []map[string]any {
	items, ok := asSlice(tools)
	if !ok {
		return nil
	}
	var specs []map[string]any
	for _, rawTool := range items {
		tool, ok := rawTool.(map[string]any)
		if !ok {
			continue
		}
		if toolType := firstNonEmpty(stringOrEmpty(tool["type"]), "function"); toolType != "function" {
			continue
		}
		function, ok := tool["function"].(map[string]any)
		if !ok {
			continue
		}
		name := stringOrEmpty(function["name"])
		if name == "" {
			continue
		}
		parameters, ok := function["parameters"].(map[string]any)
		if !ok {
			parameters = map[string]any{"type": "object", "properties": map[string]any{}}
		}
		specs = append(specs, map[string]any{
			"name":        name,
			"description": firstAny(function["description"], ""),
			"parameters":  PrepareGigaChatJSONSchema(parameters),
		})
	}
	return specs
}

func toolNames(tools any) map[string]bool {
	out := map[string]bool{}
	for _, spec := range toolSpecsForPrompt(tools) {
		if name := stringOrEmpty(spec["name"]); name != "" {
			out[name] = true
		}
	}
	return out
}

func ToolsToGigaChatFunctions(tools any) []map[string]any {
	items, ok := asSlice(tools)
	if !ok {
		return nil
	}
	var functions []map[string]any
	for _, rawTool := range items {
		tool, ok := rawTool.(map[string]any)
		if !ok {
			continue
		}
		if toolType := firstNonEmpty(stringOrEmpty(tool["type"]), "function"); toolType != "function" {
			continue
		}
		functionSpec, ok := tool["function"].(map[string]any)
		if !ok {
			continue
		}
		name := stringOrEmpty(functionSpec["name"])
		if name == "" {
			continue
		}
		parameters, ok := functionSpec["parameters"].(map[string]any)
		if !ok {
			parameters = map[string]any{"type": "object", "properties": map[string]any{}}
		}
		function := map[string]any{
			"name":       name,
			"parameters": PrepareGigaChatJSONSchema(parameters),
		}
		if description := stringOrEmpty(functionSpec["description"]); description != "" {
			function["description"] = description
		}
		functions = append(functions, function)
	}
	return functions
}

func PrepareGigaChatJSONSchema(schema map[string]any) map[string]any {
	return normalizeGigaChatJSONSchema(inlineJSONSchemaRefs(schema))
}

func GigaChatResponseToOpenAI(data map[string]any, requestModel string) map[string]any {
	choices, _ := asSlice(data["choices"])
	outChoices := make([]any, 0, len(choices))
	for i, choice := range choices {
		outChoices = append(outChoices, gigachatChoiceToOpenAI(choice, i))
	}
	return map[string]any{
		"id":      firstNonEmpty(stringOrEmpty(data["id"]), "chatcmpl-"+strings.ReplaceAll(uuid.NewString(), "-", "")),
		"object":  "chat.completion",
		"created": time.Now().Unix(),
		"model":   firstNonEmpty(stringOrEmpty(data["model"]), requestModel),
		"choices": outChoices,
		"usage":   firstAny(data["usage"], defaultUsage()),
	}
}

func GigaChatChunkToOpenAI(data map[string]any, requestModel string) map[string]any {
	choices, _ := asSlice(data["choices"])
	outChoices := make([]any, 0, len(choices))
	for i, choice := range choices {
		outChoices = append(outChoices, gigachatStreamChoiceToOpenAI(choice, i))
	}
	out := map[string]any{
		"id":      firstNonEmpty(stringOrEmpty(data["id"]), "chatcmpl-"+strings.ReplaceAll(uuid.NewString(), "-", "")),
		"object":  "chat.completion.chunk",
		"created": time.Now().Unix(),
		"model":   firstNonEmpty(stringOrEmpty(data["model"]), requestModel),
		"choices": outChoices,
	}
	if usage, ok := data["usage"]; ok {
		out["usage"] = usage
	}
	return out
}

func gigachatChoiceToOpenAI(choice any, index int) map[string]any {
	choiceMap, _ := choice.(map[string]any)
	message, _ := choiceMap["message"].(map[string]any)
	content := stringOrEmpty(message["content"])
	openAIMessage := map[string]any{"role": "assistant", "content": content}
	finishReason := finishReason(choiceMap["finish_reason"])
	if functionCall, ok := message["function_call"].(map[string]any); ok {
		openAIMessage["content"] = nil
		if content != "" {
			openAIMessage["content"] = content
		}
		openAIMessage["tool_calls"] = []any{gigachatFunctionCallToOpenAIToolCall(functionCall)}
		finishReason = "tool_calls"
	}
	return map[string]any{"index": index, "message": openAIMessage, "finish_reason": finishReason}
}

func gigachatStreamChoiceToOpenAI(choice any, index int) map[string]any {
	choiceMap, _ := choice.(map[string]any)
	delta, _ := choiceMap["delta"].(map[string]any)
	if delta == nil {
		delta, _ = choiceMap["message"].(map[string]any)
	}
	openAIDelta := map[string]any{}
	if content := stringOrEmpty(delta["content"]); content != "" {
		openAIDelta["content"] = content
	}
	finish := finishReason(choiceMap["finish_reason"])
	if functionCall, ok := delta["function_call"].(map[string]any); ok {
		openAIDelta["tool_calls"] = []any{gigachatFunctionDeltaToOpenAIToolCallDelta(functionCall)}
		if finish == nil || finish == "stop" {
			finish = "tool_calls"
		}
	}
	return map[string]any{"index": firstAny(choiceMap["index"], index), "delta": openAIDelta, "finish_reason": finish}
}

func openAIToolCallToGigaChatFunctionCall(toolCall any) map[string]any {
	call, ok := toolCall.(map[string]any)
	if !ok {
		return nil
	}
	function, ok := call["function"].(map[string]any)
	if !ok {
		return nil
	}
	name := stringOrEmpty(function["name"])
	if name == "" {
		return nil
	}
	arguments := firstAny(function["arguments"], map[string]any{})
	if raw, ok := arguments.(string); ok {
		if parsed, err := loadsModelJSON(raw); err == nil {
			arguments = parsed
		} else {
			arguments = map[string]any{"arguments": raw}
		}
	}
	return map[string]any{"name": name, "arguments": arguments}
}

func gigachatFunctionCallToOpenAIToolCall(functionCall map[string]any) map[string]any {
	return map[string]any{
		"id":   "call_" + strings.ReplaceAll(uuid.NewString(), "-", ""),
		"type": "function",
		"function": map[string]any{
			"name":      stringOrEmpty(functionCall["name"]),
			"arguments": argumentsToJSON(functionCall["arguments"]),
		},
	}
}

func gigachatFunctionDeltaToOpenAIToolCallDelta(functionCall map[string]any) map[string]any {
	delta := map[string]any{
		"index":    0,
		"type":     "function",
		"function": map[string]any{},
	}
	function := delta["function"].(map[string]any)
	if name := stringOrEmpty(functionCall["name"]); name != "" {
		delta["id"] = "call_" + strings.ReplaceAll(uuid.NewString(), "-", "")
		function["name"] = name
	}
	if arguments, ok := functionCall["arguments"]; ok {
		function["arguments"] = argumentsToJSON(arguments)
	}
	return delta
}

func inlineJSONSchemaRefs(schema map[string]any) map[string]any {
	root := cloneMap(schema)
	defs := map[string]any{}
	if rawDefs, ok := root["$defs"].(map[string]any); ok {
		defs = rawDefs
	} else if rawDefs, ok := root["definitions"].(map[string]any); ok {
		defs = rawDefs
	}
	resolving := map[string]bool{}
	var resolve func(any) any
	resolve = func(node any) any {
		switch typed := node.(type) {
		case []any:
			out := make([]any, len(typed))
			for i, item := range typed {
				out[i] = resolve(item)
			}
			return out
		case map[string]any:
			if ref := stringOrEmpty(typed["$ref"]); strings.HasPrefix(ref, "#/$defs/") || strings.HasPrefix(ref, "#/definitions/") {
				name := strings.TrimPrefix(strings.TrimPrefix(ref, "#/$defs/"), "#/definitions/")
				if target, ok := defs[name].(map[string]any); ok {
					if resolving[name] {
						fallback := map[string]any{"type": "object"}
						for key, value := range typed {
							if key != "$ref" {
								fallback[key] = resolve(value)
							}
						}
						return fallback
					}
					merged := cloneMap(target)
					for key, value := range typed {
						if key != "$ref" {
							merged[key] = value
						}
					}
					resolving[name] = true
					resolved := resolve(merged)
					delete(resolving, name)
					return resolved
				}
			}
			out := map[string]any{}
			for key, value := range typed {
				if key == "$defs" || key == "definitions" {
					continue
				}
				out[key] = resolve(value)
			}
			return out
		default:
			return typed
		}
	}
	resolved, ok := resolve(root).(map[string]any)
	if !ok {
		return root
	}
	return resolved
}

func normalizeGigaChatJSONSchema(schema map[string]any) map[string]any {
	var normalize func(any) any
	normalize = func(node any) any {
		switch typed := node.(type) {
		case []any:
			out := make([]any, len(typed))
			for i, item := range typed {
				out[i] = normalize(item)
			}
			return out
		case map[string]any:
			normalized := map[string]any{}
			for key, value := range typed {
				normalized[key] = normalize(value)
			}
			for _, unionKey := range []string{"anyOf", "oneOf"} {
				variants, ok := asSlice(normalized[unionKey])
				if !ok {
					continue
				}
				nonNull := make([]any, 0, len(variants))
				for _, variant := range variants {
					variantMap, ok := variant.(map[string]any)
					if ok && variantMap["type"] == "null" {
						continue
					}
					nonNull = append(nonNull, variant)
				}
				if len(nonNull) == 1 && len(nonNull) != len(variants) {
					merged, _ := nonNull[0].(map[string]any)
					merged = cloneMap(merged)
					for key, value := range normalized {
						if key == unionKey || (key == "default" && value == nil) {
							continue
						}
						merged[key] = value
					}
					normalized = merged
					break
				}
			}
			if types, ok := asSlice(normalized["type"]); ok {
				var nonNull []any
				for _, item := range types {
					if item != "null" {
						nonNull = append(nonNull, item)
					}
				}
				if len(nonNull) == 1 {
					normalized["type"] = nonNull[0]
					if normalized["default"] == nil {
						delete(normalized, "default")
					}
				}
			}
			return normalized
		default:
			return typed
		}
	}
	normalized, ok := normalize(schema).(map[string]any)
	if !ok {
		return schema
	}
	return normalized
}

func contentToText(content any) string {
	if content == nil {
		return ""
	}
	if text, ok := content.(string); ok {
		return text
	}
	items, ok := asSlice(content)
	if !ok {
		return fmt.Sprint(content)
	}
	var parts []string
	for _, item := range items {
		switch typed := item.(type) {
		case map[string]any:
			if typed["type"] == "text" {
				if text := stringOrEmpty(typed["text"]); text != "" {
					parts = append(parts, text)
				}
			} else if text := stringOrEmpty(typed["text"]); text != "" {
				parts = append(parts, text)
			}
		case string:
			parts = append(parts, typed)
		}
	}
	return strings.Join(parts, "\n")
}

func toolReturnContentToJSON(content any) string {
	var value any
	if raw, ok := content.(string); ok {
		var parsed any
		if err := json.Unmarshal([]byte(raw), &parsed); err == nil {
			if _, ok := parsed.(map[string]any); ok {
				value = parsed
			} else {
				value = map[string]any{"result": parsed}
			}
		} else {
			value = map[string]any{"result": raw}
		}
	} else if _, ok := content.(map[string]any); ok {
		value = content
	} else {
		value = map[string]any{"result": content}
	}
	return mustJSONString(value)
}

func toolReturnContentToEmulationJSON(toolName string, content any) string {
	rawJSON := toolReturnContentToJSON(content)
	if !terminalToolNames[toolName] || len(rawJSON) <= terminalToolResultInlineLimit {
		return rawJSON
	}
	text := toolResultContentToText(content)
	compact := map[string]any{
		"result": terminalToolResultSummary(text),
		"full_result_omitted_from_tool_routing_context": true,
		"full_result_chars":                             len(text),
		"note":                                          "Full tool output was omitted from the tool routing context. If details are needed, read the result file.",
	}
	if resultFile := extractSavedResultPath(text); resultFile != "" {
		compact["result_file"] = resultFile
	}
	return mustJSONString(compact)
}

func toolResultContentToText(content any) string {
	if text, ok := content.(string); ok {
		return text
	}
	if obj, ok := content.(map[string]any); ok {
		if result, ok := obj["result"].(string); ok {
			return result
		}
		return mustJSONString(obj)
	}
	return fmt.Sprint(content)
}

func extractSavedResultPath(text string) string {
	for _, marker := range []string{"Result saved to file ", "Saved result path: "} {
		start := strings.Index(text, marker)
		if start < 0 {
			continue
		}
		start += len(marker)
		end := strings.IndexByte(text[start:], '\n')
		if end < 0 {
			end = len(text)
		} else {
			end += start
		}
		return strings.TrimSpace(text[start:end])
	}
	return ""
}

func terminalToolResultSummary(text string) string {
	marker := "File contents:"
	if idx := strings.Index(text, marker); idx >= 0 {
		text = text[:idx]
	}
	summary := strings.TrimSpace(text)
	if summary == "" {
		summary = "Terminal tool result was produced; full text was omitted from the routing context."
	}
	if len(summary) > 1000 {
		summary = strings.TrimSpace(summary[:1000]) + "..."
	}
	return summary
}

func argumentsToJSON(arguments any) string {
	if arguments == nil {
		return "{}"
	}
	if raw, ok := arguments.(string); ok {
		stripped := strings.TrimSpace(raw)
		if stripped == "" {
			return "{}"
		}
		parsed, err := loadsModelJSON(stripped)
		if err != nil {
			return mustJSONString(map[string]any{"arguments": stripped})
		}
		if _, ok := parsed.(map[string]any); ok {
			return mustJSONString(parsed)
		}
		return mustJSONString(map[string]any{"arguments": parsed})
	}
	return mustJSONString(arguments)
}

func loadsModelJSON(text string) (any, error) {
	text = strings.TrimSpace(text)
	if strings.HasPrefix(text, "```") {
		text = strings.TrimPrefix(text, "```json")
		text = strings.TrimPrefix(text, "```")
		text = strings.TrimSpace(text)
		text = strings.TrimSuffix(text, "```")
		text = strings.TrimSpace(text)
	}
	if start, end := strings.Index(text, "{"), strings.LastIndex(text, "}"); start >= 0 && end > start {
		text = text[start : end+1]
	}
	return loadsLenientJSON(text)
}

func loadsLenientJSON(text string) (any, error) {
	var lastErr error
	candidates := []string{text, strings.ReplaceAll(text, `\'`, `'`)}
	for _, candidate := range candidates {
		for _, variant := range []string{candidate, trailingCommaRE.ReplaceAllString(candidate, "$1"), strings.ReplaceAll(candidate, "'", `"`)} {
			var parsed any
			if err := json.Unmarshal([]byte(variant), &parsed); err == nil {
				return parsed, nil
			} else {
				lastErr = err
			}
		}
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("invalid JSON")
	}
	return nil, lastErr
}

func finishReason(reason any) any {
	text := stringOrEmpty(reason)
	if text == "" {
		return nil
	}
	switch text {
	case "tool_call", "function_call", "tool_calls":
		return "tool_calls"
	default:
		return text
	}
}

func asSlice(value any) ([]any, bool) {
	switch typed := value.(type) {
	case []any:
		return typed, true
	case []map[string]any:
		out := make([]any, len(typed))
		for i, item := range typed {
			out[i] = item
		}
		return out, true
	default:
		return nil, false
	}
}

func firstAny(values ...any) any {
	for _, value := range values {
		if value != nil {
			return value
		}
	}
	return nil
}

func defaultUsage() map[string]any {
	return map[string]any{"prompt_tokens": 0, "completion_tokens": 0, "total_tokens": 0}
}

func cloneMap(in map[string]any) map[string]any {
	out := make(map[string]any, len(in))
	for key, value := range in {
		out[key] = cloneAny(value)
	}
	return out
}

func cloneAny(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		return cloneMap(typed)
	case []any:
		out := make([]any, len(typed))
		for i, item := range typed {
			out[i] = cloneAny(item)
		}
		return out
	default:
		return typed
	}
}

func mustJSONString(value any) string {
	data, err := json.Marshal(value)
	if err != nil {
		return mustJSONString(map[string]any{"result": fmt.Sprint(value)})
	}
	return string(data)
}

func mustJSONStringIndent(value any) string {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return mustJSONString(value)
	}
	return string(data)
}
