package responses

import (
	"bytes"
	"encoding/json"
	"io"
	"slices"
	"time"

	"github.com/theio/yallmp-proxy/internal/proxy"
)

const (
	EventResponseCreated       = "response.created"
	EventResponseInProgress    = "response.in_progress"
	EventResponseCompleted     = "response.completed"
	EventResponseFailed        = "response.failed"
	EventResponseIncomplete    = "response.incomplete"
	EventOutputItemAdded       = "response.output_item.added"
	EventContentPartAdded      = "response.content_part.added"
	EventOutputTextDelta       = "response.output_text.delta"
	EventReasoningSummaryDelta = "response.reasoning_summary_text.delta"
	EventReasoningSummaryDone  = "response.reasoning_summary_text.done"
	EventFunctionCallArguments = "response.function_call_arguments.delta"
	EventContentPartDone       = "response.content_part.done"
	EventOutputItemDone        = "response.output_item.done"
)

type StreamAdapter struct {
	ResponseID string
	Model      string
	Original   map[string]any
	decoder    proxy.SSEJSONDecoder

	sequence       int
	createdAt      float64
	createdEmitted bool
	sawDone        bool
	finishReasons  map[string]bool
	usage          map[string]any
	serviceTier    any

	nextOutputIndex  int
	sawAssistantRole bool
	messageStarted   bool
	messageDone      bool
	messageIndex     int
	messageID        string
	text             string

	reasoningStarted bool
	reasoningDone    bool
	reasoningIndex   int
	reasoningID      string
	reasoningText    string

	outputItems map[int]map[string]any
	toolCalls   map[int]*responseToolCall
}

type responseToolCall struct {
	ID          string
	Name        string
	Arguments   string
	OutputIndex int
	Done        bool
}

func NewStreamAdapter(responseID string, model string, original map[string]any) *StreamAdapter {
	if responseID == "" {
		responseID = GenerateResponseID()
	}
	if original == nil {
		original = map[string]any{}
	}
	return &StreamAdapter{
		ResponseID:    responseID,
		Model:         model,
		Original:      original,
		createdAt:     float64(time.Now().Unix()),
		finishReasons: map[string]bool{},
		outputItems:   map[int]map[string]any{},
		toolCalls:     map[int]*responseToolCall{},
	}
}

func (a *StreamAdapter) Feed(chunk []byte) []byte {
	if a == nil {
		return nil
	}
	out := bytes.Buffer{}
	a.emitInitial(&out)
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
	a.emitInitial(&out)
	for _, payload := range a.decoder.Flush() {
		out.Write(a.processPayload(payload))
	}
	out.Write(a.terminalEvents())
	return out.Bytes()
}

func (a *StreamAdapter) TotalTokens() int {
	if a == nil || len(a.usage) == 0 {
		return 0
	}
	if total := intFromAny(a.usage["total_tokens"]); total > 0 {
		return total
	}
	return intFromAny(a.usage["input_tokens"]) + intFromAny(a.usage["output_tokens"])
}

func (a *StreamAdapter) UsageStats() map[string]any {
	if a == nil || len(a.usage) == 0 {
		return nil
	}
	out := map[string]any{}
	if value, ok := a.usage["input_tokens"]; ok {
		out["input_tokens"] = value
		out["prompt_tokens"] = value
	}
	if value, ok := a.usage["output_tokens"]; ok {
		out["output_tokens"] = value
		out["completion_tokens"] = value
	}
	if value, ok := a.usage["total_tokens"]; ok {
		out["total_tokens"] = value
	}
	if value, ok := a.usage["input_tokens_details"]; ok {
		out["input_tokens_details"] = value
	}
	if value, ok := a.usage["output_tokens_details"]; ok {
		out["output_tokens_details"] = value
	}
	return out
}

func (a *StreamAdapter) FinalResponse() map[string]any {
	status := a.terminalStatus()
	response := a.responseObject(status)
	if status == "incomplete" {
		response["incomplete_details"] = map[string]any{"reason": "max_output_tokens"}
	}
	if status == "failed" {
		response["error"] = responseErrorObject("model_error", "content_filter", "Response filtered by content policy.", nil)
	}
	return response
}

func (a *StreamAdapter) emitInitial(out *bytes.Buffer) {
	if a.createdEmitted {
		return
	}
	out.Write(a.event(EventResponseCreated, map[string]any{"response": a.responseObject("in_progress")}))
	out.Write(a.event(EventResponseInProgress, map[string]any{"response": a.responseObject("in_progress")}))
	a.createdEmitted = true
}

func (a *StreamAdapter) processPayload(payload map[string]any) []byte {
	out := bytes.Buffer{}
	if usage, ok := payload["usage"].(map[string]any); ok {
		a.usage = convertUsage(usage)
	}
	if serviceTier, ok := payload["service_tier"]; ok {
		a.serviceTier = serviceTier
	}
	choices, _ := payload["choices"].([]any)
	for _, rawChoice := range choices {
		choice, ok := rawChoice.(map[string]any)
		if !ok {
			continue
		}
		delta, _ := choice["delta"].(map[string]any)
		if role, _ := delta["role"].(string); role == "assistant" {
			a.sawAssistantRole = true
		}
		for _, reasoning := range reasoningSummaryTextsFromDelta(delta) {
			out.Write(a.appendReasoning(reasoning))
		}
		if message, _ := choice["message"].(map[string]any); message != nil {
			for _, reasoning := range reasoningSummaryTextsFromDelta(message) {
				out.Write(a.appendReasoning(reasoning))
			}
		}
		if content, ok := delta["content"]; ok {
			for _, text := range extractTextChunks(content) {
				if !a.messageStarted {
					out.Write(a.startMessage())
				}
				a.text += text
				out.Write(a.event(EventOutputTextDelta, map[string]any{
					"item_id":       a.messageID,
					"output_index":  a.messageIndex,
					"content_index": 0,
					"delta":         text,
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
			a.finishReasons[finishReason] = true
			if a.sawAssistantRole && !a.reasoningStarted && !a.messageStarted && len(a.toolCalls) == 0 {
				out.Write(a.startMessage())
			}
			out.Write(a.finalizeOpenItems(finishReason))
		}
	}
	return out.Bytes()
}

func (a *StreamAdapter) startMessage() []byte {
	if a.messageStarted {
		return nil
	}
	out := bytes.Buffer{}
	out.Write(a.finalizeReasoning())
	a.messageStarted = true
	a.messageIndex = a.reserveOutputIndex()
	a.messageID = GenerateMessageID()
	out.Write(a.event(EventOutputItemAdded, map[string]any{
		"output_index": a.messageIndex,
		"item": map[string]any{
			"type":    "message",
			"id":      a.messageID,
			"role":    "assistant",
			"status":  "in_progress",
			"content": []any{},
		},
	}))
	out.Write(a.event(EventContentPartAdded, map[string]any{
		"item_id":       a.messageID,
		"output_index":  a.messageIndex,
		"content_index": 0,
		"part":          map[string]any{"type": "output_text", "text": "", "annotations": []any{}},
	}))
	return out.Bytes()
}

func (a *StreamAdapter) appendReasoning(text string) []byte {
	if text == "" {
		return nil
	}
	out := bytes.Buffer{}
	if !a.reasoningStarted || a.reasoningDone {
		a.reasoningStarted = true
		a.reasoningDone = false
		a.reasoningIndex = a.reserveOutputIndex()
		a.reasoningID = GenerateReasoningID()
		a.reasoningText = ""
		out.Write(a.event(EventOutputItemAdded, map[string]any{
			"output_index": a.reasoningIndex,
			"item": map[string]any{
				"type":    "reasoning",
				"id":      a.reasoningID,
				"summary": []any{},
			},
		}))
	}
	a.reasoningText += text
	out.Write(a.event(EventReasoningSummaryDelta, map[string]any{
		"item_id":       a.reasoningID,
		"output_index":  a.reasoningIndex,
		"summary_index": 0,
		"delta":         text,
	}))
	return out.Bytes()
}

func (a *StreamAdapter) finalizeReasoning() []byte {
	if !a.reasoningStarted || a.reasoningDone {
		return nil
	}
	out := bytes.Buffer{}
	out.Write(a.event(EventReasoningSummaryDone, map[string]any{
		"item_id":       a.reasoningID,
		"output_index":  a.reasoningIndex,
		"summary_index": 0,
		"text":          a.reasoningText,
	}))
	item := map[string]any{
		"id":      a.reasoningID,
		"type":    "reasoning",
		"summary": []any{map[string]any{"type": "summary_text", "text": a.reasoningText}},
	}
	a.outputItems[a.reasoningIndex] = item
	out.Write(a.event(EventOutputItemDone, map[string]any{"output_index": a.reasoningIndex, "item": item}))
	a.reasoningDone = true
	return out.Bytes()
}

func (a *StreamAdapter) processToolCall(call map[string]any) []byte {
	out := bytes.Buffer{}
	index := intFromAny(call["index"])
	function, _ := call["function"].(map[string]any)
	if _, ok := a.toolCalls[index]; !ok {
		out.Write(a.finalizeReasoning())
		id := firstNonEmptyString(call["id"], GenerateCallID())
		tc := &responseToolCall{ID: id, OutputIndex: a.reserveOutputIndex()}
		a.toolCalls[index] = tc
		out.Write(a.event(EventOutputItemAdded, map[string]any{
			"output_index": tc.OutputIndex,
			"item": map[string]any{
				"type":      "function_call",
				"id":        tc.ID,
				"call_id":   tc.ID,
				"name":      "",
				"arguments": "",
				"status":    "in_progress",
			},
		}))
	}
	tc := a.toolCalls[index]
	if name, ok := function["name"].(string); ok && name != "" {
		tc.Name = name
	}
	if args, ok := function["arguments"].(string); ok {
		tc.Arguments += args
		out.Write(a.event(EventFunctionCallArguments, map[string]any{
			"item_id":      tc.ID,
			"output_index": tc.OutputIndex,
			"call_id":      tc.ID,
			"delta":        args,
		}))
	}
	return out.Bytes()
}

func (a *StreamAdapter) finalizeOpenItems(finishReason string) []byte {
	out := bytes.Buffer{}
	out.Write(a.finalizeReasoning())
	if a.messageStarted && !a.messageDone {
		status := "completed"
		if finishReason == "length" {
			status = "incomplete"
		}
		out.Write(a.event(EventContentPartDone, map[string]any{
			"item_id":       a.messageID,
			"output_index":  a.messageIndex,
			"content_index": 0,
			"part":          map[string]any{"type": "output_text", "text": a.text, "annotations": []any{}},
		}))
		item := map[string]any{
			"id":      a.messageID,
			"type":    "message",
			"role":    "assistant",
			"status":  status,
			"content": []any{map[string]any{"type": "output_text", "text": a.text, "annotations": []any{}}},
		}
		a.outputItems[a.messageIndex] = item
		out.Write(a.event(EventOutputItemDone, map[string]any{"output_index": a.messageIndex, "item": item}))
		a.messageDone = true
	}
	indices := make([]int, 0, len(a.toolCalls))
	for index := range a.toolCalls {
		indices = append(indices, index)
	}
	slices.Sort(indices)
	for _, index := range indices {
		tc := a.toolCalls[index]
		if tc.Done {
			continue
		}
		item := map[string]any{
			"id":        tc.ID,
			"type":      "function_call",
			"call_id":   tc.ID,
			"name":      tc.Name,
			"arguments": tc.Arguments,
			"status":    "completed",
		}
		a.outputItems[tc.OutputIndex] = item
		out.Write(a.event(EventOutputItemDone, map[string]any{"output_index": tc.OutputIndex, "item": item}))
		tc.Done = true
	}
	return out.Bytes()
}

func (a *StreamAdapter) terminalEvents() []byte {
	out := bytes.Buffer{}
	out.Write(a.finalizeOpenItems(""))
	status := a.terminalStatus()
	eventType := EventResponseCompleted
	if status == "incomplete" {
		eventType = EventResponseIncomplete
	}
	if status == "failed" {
		eventType = EventResponseFailed
	}
	out.Write(a.event(eventType, map[string]any{"response": a.FinalResponse()}))
	return out.Bytes()
}

func (a *StreamAdapter) terminalStatus() string {
	if a.finishReasons["length"] {
		return "incomplete"
	}
	if a.finishReasons["content_filter"] {
		return "failed"
	}
	return "completed"
}

func (a *StreamAdapter) responseObject(status string) map[string]any {
	output := make([]any, 0, len(a.outputItems))
	indices := make([]int, 0, len(a.outputItems))
	for index := range a.outputItems {
		indices = append(indices, index)
	}
	slices.Sort(indices)
	for _, index := range indices {
		output = append(output, a.outputItems[index])
	}
	response := map[string]any{
		"id":                   a.ResponseID,
		"object":               "response",
		"created_at":           a.createdAt,
		"status":               status,
		"model":                a.Model,
		"previous_response_id": a.Original["previous_response_id"],
		"output":               output,
		"output_text":          a.text,
		"error":                nil,
		"incomplete_details":   nil,
		"metadata":             mapOrEmpty(a.Original["metadata"]),
	}
	if status == "completed" || status == "failed" || status == "incomplete" {
		response["completed_at"] = float64(time.Now().Unix())
	}
	if len(a.usage) > 0 {
		response["usage"] = a.usage
	}
	for _, key := range []string{"temperature", "top_p", "max_output_tokens", "tools", "tool_choice", "service_tier", "prompt_cache_key"} {
		if value, ok := a.Original[key]; ok {
			response[key] = value
		}
	}
	if a.serviceTier != nil {
		response["service_tier"] = a.serviceTier
	}
	return response
}

func (a *StreamAdapter) event(eventType string, data map[string]any) []byte {
	a.sequence++
	payload := map[string]any{"type": eventType, "sequence_number": a.sequence}
	for key, value := range data {
		payload[key] = value
	}
	raw, _ := json.Marshal(payload)
	return []byte("event: " + eventType + "\ndata: " + string(raw) + "\n\n")
}

func (a *StreamAdapter) reserveOutputIndex() int {
	index := a.nextOutputIndex
	a.nextOutputIndex++
	return index
}

func extractTextChunks(value any) []string {
	switch typed := value.(type) {
	case string:
		if typed == "" {
			return nil
		}
		return []string{typed}
	case map[string]any:
		if typed["type"] == "text" || typed["type"] == "output_text" {
			if text := stringify(typed["text"]); text != "" {
				return []string{text}
			}
		}
	case []any:
		out := make([]string, 0)
		for _, item := range typed {
			out = append(out, extractTextChunks(item)...)
		}
		return out
	}
	return nil
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
}

func NewStreamReadCloser(inner io.ReadCloser, responseID string, model string, original map[string]any) *StreamReadCloser {
	return &StreamReadCloser{ReadCloser: inner, Adapter: NewStreamAdapter(responseID, model, original)}
}

func (r *StreamReadCloser) Read(p []byte) (int, error) {
	for len(r.buffer) == 0 && !r.eof {
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
			return 0, err
		}
	}
	if len(r.buffer) == 0 && r.eof {
		return 0, io.EOF
	}
	n := copy(p, r.buffer)
	r.buffer = r.buffer[n:]
	return n, nil
}
