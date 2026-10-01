package messages

import (
	"bytes"
	"encoding/json"
	"io"
	"slices"

	"github.com/google/uuid"
	"github.com/theio/yallmp-proxy/internal/proxy"
)

type StreamAdapter struct {
	MessageID string
	Model     string
	decoder   proxy.SSEJSONDecoder

	messageStarted  bool
	currentText     int
	currentThinking int
	text            string
	thinking        string
	finishReason    string
	inputTokens     int
	outputTokens    int
	totalTokens     int

	cacheReadInputTokens     int
	cacheReadInputTokensSeen bool
	cacheCreationInputTokens int
	cacheCreationTokensSeen  bool

	blocks       []map[string]any
	toolCalls    map[int]*streamToolCall
	toolBlockIdx map[int]int
	toolStopped  map[int]bool
}

type streamToolCall struct {
	ID        string
	Name      string
	Arguments string
}

func NewStreamAdapter(messageID string, model string) *StreamAdapter {
	if messageID == "" {
		messageID = "msg_" + uuid.NewString()
	}
	return &StreamAdapter{
		MessageID:       messageID,
		Model:           model,
		currentText:     -1,
		currentThinking: -1,
		toolCalls:       map[int]*streamToolCall{},
		toolBlockIdx:    map[int]int{},
		toolStopped:     map[int]bool{},
	}
}

func (a *StreamAdapter) Feed(chunk []byte) []byte {
	if a == nil {
		return nil
	}
	out := bytes.Buffer{}
	for _, payload := range a.decoder.Feed(chunk) {
		out.Write(a.processPayload(payload))
	}
	return out.Bytes()
}

func (a *StreamAdapter) Flush() []byte {
	if a == nil {
		return nil
	}
	out := bytes.Buffer{}
	for _, payload := range a.decoder.Flush() {
		out.Write(a.processPayload(payload))
	}
	out.Write(a.terminalEvents())
	return out.Bytes()
}

func (a *StreamAdapter) TotalTokens() int {
	if a == nil {
		return 0
	}
	if a.totalTokens > 0 {
		return a.totalTokens
	}
	return a.inputTokens + a.outputTokens
}

func (a *StreamAdapter) UsageStats() map[string]any {
	if a == nil || (a.inputTokens+a.outputTokens+a.totalTokens == 0 && !a.cacheReadInputTokensSeen && !a.cacheCreationTokensSeen) {
		return nil
	}
	usage := map[string]any{
		"input_tokens":      a.inputTokens,
		"output_tokens":     a.outputTokens,
		"prompt_tokens":     a.inputTokens,
		"completion_tokens": a.outputTokens,
	}
	if total := a.TotalTokens(); total > 0 {
		usage["total_tokens"] = total
	}
	a.addCacheUsage(usage)
	return usage
}

func (a *StreamAdapter) processPayload(payload map[string]any) []byte {
	out := bytes.Buffer{}
	if usage, ok := payload["usage"].(map[string]any); ok {
		a.observeUsage(usage)
	}
	choices, _ := payload["choices"].([]any)
	for _, rawChoice := range choices {
		choice, ok := rawChoice.(map[string]any)
		if !ok {
			continue
		}
		delta, _ := choice["delta"].(map[string]any)
		if !a.messageStarted && meaningfulDelta(delta) {
			out.Write(a.messageStart())
			a.messageStarted = true
		}
		for _, reasoning := range reasoningTextChunksFromDelta(delta) {
			out.Write(a.processThinking(reasoning))
		}
		if message, _ := choice["message"].(map[string]any); message != nil {
			for _, reasoning := range reasoningTextChunksFromDelta(message) {
				out.Write(a.processThinking(reasoning))
			}
			for _, signature := range reasoningSignatureChunksFromDelta(message) {
				out.Write(a.processThinkingSignature(signature))
			}
		}
		for _, signature := range reasoningSignatureChunksFromDelta(delta) {
			out.Write(a.processThinkingSignature(signature))
		}
		if content, exists := delta["content"]; exists {
			for _, text := range streamTextChunks(content) {
				out.Write(a.closeOpenToolBlocks())
				if a.currentThinking >= 0 {
					out.Write(a.closeThinking())
				}
				if a.currentText < 0 {
					a.currentText = len(a.blocks)
					a.blocks = append(a.blocks, map[string]any{"type": "text", "text": ""})
					out.Write(sseEvent("content_block_start", map[string]any{
						"type":          "content_block_start",
						"index":         a.currentText,
						"content_block": map[string]any{"type": "text", "text": ""},
					}))
				}
				a.text += text
				out.Write(sseEvent("content_block_delta", map[string]any{
					"type":  "content_block_delta",
					"index": a.currentText,
					"delta": map[string]any{"type": "text_delta", "text": text},
				}))
			}
		}
		if toolCalls, ok := delta["tool_calls"].([]any); ok {
			for _, rawCall := range toolCalls {
				call, ok := rawCall.(map[string]any)
				if !ok {
					continue
				}
				out.Write(a.processToolCall(call))
			}
		}
		if finishReason, ok := choice["finish_reason"].(string); ok && finishReason != "" {
			a.finishReason = finishReason
		}
	}
	return out.Bytes()
}

func (a *StreamAdapter) processThinking(text string) []byte {
	if text == "" {
		return nil
	}
	out := bytes.Buffer{}
	out.Write(a.closeOpenToolBlocks())
	if a.currentThinking < 0 {
		if a.currentText >= 0 {
			a.blocks[a.currentText]["text"] = a.text
			out.Write(a.contentBlockStop(a.currentText))
			a.currentText = -1
		}
		a.currentThinking = len(a.blocks)
		a.thinking = ""
		a.blocks = append(a.blocks, map[string]any{"type": "thinking", "thinking": ""})
		out.Write(sseEvent("content_block_start", map[string]any{
			"type":          "content_block_start",
			"index":         a.currentThinking,
			"content_block": map[string]any{"type": "thinking", "thinking": ""},
		}))
	}
	a.thinking += text
	out.Write(sseEvent("content_block_delta", map[string]any{
		"type":  "content_block_delta",
		"index": a.currentThinking,
		"delta": map[string]any{"type": "thinking_delta", "thinking": text},
	}))
	return out.Bytes()
}

func (a *StreamAdapter) processThinkingSignature(signature string) []byte {
	if signature == "" || a.currentThinking < 0 {
		return nil
	}
	if blockIndex := a.currentThinking; blockIndex < len(a.blocks) {
		a.blocks[blockIndex]["signature"] = signature
	}
	return sseEvent("content_block_delta", map[string]any{
		"type":  "content_block_delta",
		"index": a.currentThinking,
		"delta": map[string]any{"type": "signature_delta", "signature": signature},
	})
}

func (a *StreamAdapter) closeThinking() []byte {
	if a.currentThinking < 0 {
		return nil
	}
	index := a.currentThinking
	if index < len(a.blocks) {
		a.blocks[index]["thinking"] = a.thinking
	}
	a.currentThinking = -1
	a.thinking = ""
	return a.contentBlockStop(index)
}

func (a *StreamAdapter) processToolCall(call map[string]any) []byte {
	out := bytes.Buffer{}
	index := intFromAny(call["index"])
	function, _ := call["function"].(map[string]any)
	if _, ok := a.toolCalls[index]; !ok {
		if a.currentThinking >= 0 {
			out.Write(a.closeThinking())
		}
		if a.currentText >= 0 {
			a.blocks[a.currentText]["text"] = a.text
			out.Write(a.contentBlockStop(a.currentText))
			a.currentText = -1
		}
		blockIndex := len(a.blocks)
		toolID := firstNonEmptyString(call["id"], "toolu_"+uuid.NewString()[:12])
		name := stringifyContent(function["name"])
		a.toolCalls[index] = &streamToolCall{ID: toolID, Name: name}
		a.toolBlockIdx[index] = blockIndex
		a.blocks = append(a.blocks, map[string]any{"type": "tool_use", "id": toolID, "name": name, "input": map[string]any{}})
		out.Write(sseEvent("content_block_start", map[string]any{
			"type":          "content_block_start",
			"index":         blockIndex,
			"content_block": map[string]any{"type": "tool_use", "id": toolID, "name": name, "input": map[string]any{}},
		}))
	}
	state := a.toolCalls[index]
	if name, ok := function["name"].(string); ok && name != "" {
		state.Name = name
		if blockIndex, ok := a.toolBlockIdx[index]; ok && blockIndex < len(a.blocks) {
			a.blocks[blockIndex]["name"] = name
		}
	}
	if args, ok := function["arguments"].(string); ok {
		state.Arguments += args
		out.Write(sseEvent("content_block_delta", map[string]any{
			"type":  "content_block_delta",
			"index": a.toolBlockIdx[index],
			"delta": map[string]any{"type": "input_json_delta", "partial_json": args},
		}))
	}
	return out.Bytes()
}

func (a *StreamAdapter) terminalEvents() []byte {
	out := bytes.Buffer{}
	if !a.messageStarted {
		out.Write(a.messageStart())
		a.messageStarted = true
	}
	if a.currentText >= 0 {
		a.blocks[a.currentText]["text"] = a.text
		out.Write(a.contentBlockStop(a.currentText))
		a.currentText = -1
	}
	if a.currentThinking >= 0 {
		out.Write(a.closeThinking())
	}
	indices := make([]int, 0, len(a.toolBlockIdx))
	for index := range a.toolBlockIdx {
		indices = append(indices, index)
	}
	slices.Sort(indices)
	for _, index := range indices {
		if a.toolStopped[index] {
			continue
		}
		blockIndex := a.toolBlockIdx[index]
		state := a.toolCalls[index]
		input := parseToolArguments(state.Arguments)
		if blockIndex < len(a.blocks) {
			a.blocks[blockIndex]["input"] = input
		}
		out.Write(a.contentBlockStop(blockIndex))
		a.toolStopped[index] = true
	}
	out.Write(sseEvent("message_delta", map[string]any{
		"type":  "message_delta",
		"delta": map[string]any{"stop_reason": convertStopReason(a.finishReason), "stop_sequence": nil},
		"usage": a.messageDeltaUsage(),
	}))
	out.Write(sseEvent("message_stop", map[string]any{"type": "message_stop"}))
	return out.Bytes()
}

func (a *StreamAdapter) closeOpenToolBlocks() []byte {
	if len(a.toolBlockIdx) == 0 {
		return nil
	}
	out := bytes.Buffer{}
	indices := make([]int, 0, len(a.toolBlockIdx))
	for index := range a.toolBlockIdx {
		indices = append(indices, index)
	}
	slices.Sort(indices)
	for _, index := range indices {
		if a.toolStopped[index] {
			continue
		}
		out.Write(a.contentBlockStop(a.toolBlockIdx[index]))
		a.toolStopped[index] = true
	}
	return out.Bytes()
}

func (a *StreamAdapter) messageStart() []byte {
	usage := map[string]any{"input_tokens": a.inputTokens, "output_tokens": 0}
	a.addCacheUsage(usage)
	return sseEvent("message_start", map[string]any{
		"type": "message_start",
		"message": map[string]any{
			"id":            a.MessageID,
			"type":          "message",
			"role":          "assistant",
			"content":       []any{},
			"model":         a.Model,
			"stop_reason":   nil,
			"stop_sequence": nil,
			"usage":         usage,
		},
	})
}

func (a *StreamAdapter) contentBlockStop(index int) []byte {
	return sseEvent("content_block_stop", map[string]any{"type": "content_block_stop", "index": index})
}

func (a *StreamAdapter) observeUsage(usage map[string]any) {
	if tokens := intFromAny(usage["prompt_tokens"]); tokens > 0 {
		a.inputTokens = tokens
	}
	if tokens := intFromAny(usage["input_tokens"]); tokens > 0 {
		a.inputTokens = tokens
	}
	if tokens := intFromAny(usage["completion_tokens"]); tokens > 0 {
		a.outputTokens = tokens
	}
	if tokens := intFromAny(usage["output_tokens"]); tokens > 0 {
		a.outputTokens = tokens
	}
	if tokens := intFromAny(usage["total_tokens"]); tokens > 0 {
		a.totalTokens = tokens
	}
	a.observeCacheUsageDetails(usage["prompt_tokens_details"])
	a.observeCacheUsageDetails(usage["input_tokens_details"])
	if value, ok := firstPresent(usage, "cache_read_input_tokens", "cached_tokens"); ok {
		a.cacheReadInputTokens = intFromAny(value)
		a.cacheReadInputTokensSeen = true
	}
	if value, ok := firstPresent(usage, "cache_creation_input_tokens", "cache_creation_tokens"); ok {
		a.cacheCreationInputTokens = intFromAny(value)
		a.cacheCreationTokensSeen = true
	}
}

func (a *StreamAdapter) observeCacheUsageDetails(value any) {
	details, ok := value.(map[string]any)
	if !ok {
		return
	}
	if value, ok := details["cached_tokens"]; ok {
		a.cacheReadInputTokens = intFromAny(value)
		a.cacheReadInputTokensSeen = true
	}
	if value, ok := firstPresent(details, "cache_creation_input_tokens", "cache_creation_tokens"); ok {
		a.cacheCreationInputTokens = intFromAny(value)
		a.cacheCreationTokensSeen = true
	}
}

func (a *StreamAdapter) messageDeltaUsage() map[string]any {
	usage := map[string]any{"output_tokens": a.outputTokens}
	a.addCacheUsage(usage)
	return usage
}

func (a *StreamAdapter) addCacheUsage(usage map[string]any) {
	if a.cacheReadInputTokensSeen {
		usage["cache_read_input_tokens"] = a.cacheReadInputTokens
	}
	if a.cacheCreationTokensSeen {
		usage["cache_creation_input_tokens"] = a.cacheCreationInputTokens
	}
}

func meaningfulDelta(delta map[string]any) bool {
	if len(delta) == 0 {
		return false
	}
	if _, ok := delta["role"]; ok {
		return true
	}
	if _, ok := delta["content"]; ok {
		return true
	}
	if len(reasoningTextChunksFromDelta(delta)) > 0 {
		return true
	}
	if calls, ok := delta["tool_calls"].([]any); ok && len(calls) > 0 {
		return true
	}
	return false
}

func reasoningTextChunksFromDelta(delta map[string]any) []string {
	if delta == nil {
		return nil
	}
	chunks := openAIReasoningTextChunks(delta)
	chunks = append(chunks, reasoningTextChunksFromStreamContent(delta["content"])...)
	chunks = append(chunks, reasoningTextChunksFromValue(delta["reasoning_details"])...)
	return compactStrings(chunks)
}

func reasoningTextChunksFromStreamContent(content any) []string {
	switch typed := content.(type) {
	case []any:
		chunks := make([]string, 0)
		for _, raw := range typed {
			if part, ok := raw.(map[string]any); ok {
				for _, block := range openAIReasoningPartToAnthropicBlocks(part) {
					if thinking, ok := block.(map[string]any); ok && thinking["type"] == "thinking" {
						chunks = append(chunks, stringifyContent(thinking["thinking"]))
					}
				}
			}
		}
		return compactStrings(chunks)
	case map[string]any:
		for _, block := range openAIReasoningPartToAnthropicBlocks(typed) {
			if thinking, ok := block.(map[string]any); ok && thinking["type"] == "thinking" {
				return compactStrings([]string{stringifyContent(thinking["thinking"])})
			}
		}
	}
	return nil
}

func streamTextChunks(content any) []string {
	switch typed := content.(type) {
	case string:
		return compactStrings([]string{typed})
	case []any:
		chunks := make([]string, 0)
		for _, raw := range typed {
			chunks = append(chunks, streamTextChunks(raw)...)
		}
		return compactStrings(chunks)
	case map[string]any:
		if typed["type"] == "text" || typed["type"] == "output_text" {
			return compactStrings([]string{stringifyContent(typed["text"])})
		}
	}
	return nil
}

func reasoningSignatureChunksFromDelta(delta map[string]any) []string {
	if delta == nil {
		return nil
	}
	chunks := make([]string, 0)
	for _, key := range []string{"reasoning_signature", "signature"} {
		if signature := stringifyContent(delta[key]); signature != "" {
			chunks = append(chunks, signature)
		}
	}
	chunks = append(chunks, reasoningSignatureChunksFromDetails(delta["reasoning_details"])...)
	return compactStrings(chunks)
}

func reasoningSignatureChunksFromDetails(value any) []string {
	switch typed := value.(type) {
	case []any:
		chunks := make([]string, 0)
		for _, item := range typed {
			chunks = append(chunks, reasoningSignatureChunksFromDetails(item)...)
		}
		return compactStrings(chunks)
	case map[string]any:
		if signature := stringifyContent(typed["signature"]); signature != "" {
			return []string{signature}
		}
	}
	return nil
}

func sseEvent(eventType string, data map[string]any) []byte {
	raw, _ := json.Marshal(data)
	return []byte("event: " + eventType + "\ndata: " + string(raw) + "\n\n")
}

func intFromAny(value any) int {
	switch typed := value.(type) {
	case int:
		return typed
	case int64:
		return int(typed)
	case float64:
		return int(typed)
	case json.Number:
		n, _ := typed.Int64()
		return int(n)
	default:
		return 0
	}
}

type StreamReadCloser struct {
	io.ReadCloser
	Adapter *StreamAdapter
	buffer  []byte
	eof     bool
	pending error
}

func NewStreamReadCloser(inner io.ReadCloser, messageID string, model string) *StreamReadCloser {
	return &StreamReadCloser{ReadCloser: inner, Adapter: NewStreamAdapter(messageID, model)}
}

func (r *StreamReadCloser) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	for len(r.buffer) == 0 && !r.eof && r.pending == nil {
		tmp := make([]byte, 32*1024)
		n, err := r.ReadCloser.Read(tmp)
		if n > 0 {
			r.buffer = append(r.buffer, r.Adapter.Feed(tmp[:n])...)
		}
		if err != nil {
			if err == io.EOF {
				r.buffer = append(r.buffer, r.Adapter.Flush()...)
				r.eof = true
				break
			}
			r.pending = err
			r.eof = true
		}
		if n == 0 && err == nil {
			return 0, nil
		}
	}
	if len(r.buffer) > 0 {
		n := copy(p, r.buffer)
		r.buffer = r.buffer[n:]
		return n, nil
	}
	if r.pending != nil {
		err := r.pending
		r.pending = nil
		return 0, err
	}
	return 0, io.EOF
}
