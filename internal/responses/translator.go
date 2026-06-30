package responses

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

func GenerateResponseID() string {
	return "resp_" + strings.ReplaceAll(uuid.NewString(), "-", "")
}

func GenerateMessageID() string {
	return "msg_" + strings.ReplaceAll(uuid.NewString(), "-", "")[:24]
}

func GenerateCallID() string {
	return "call_" + strings.ReplaceAll(uuid.NewString(), "-", "")[:24]
}

func GenerateReasoningID() string {
	return "rs_" + strings.ReplaceAll(uuid.NewString(), "-", "")[:24]
}

func ToChatCompletions(payload map[string]any, history []any) (map[string]any, error) {
	input, ok := payload["input"]
	if !ok || input == nil {
		return nil, fmt.Errorf("input is required")
	}
	messages := make([]any, 0)
	if instructions, ok := payload["instructions"].(string); ok && strings.TrimSpace(instructions) != "" {
		messages = append(messages, map[string]any{"role": "system", "content": instructions})
	}
	messages = append(messages, history...)
	messages = append(messages, inputToMessages(input)...)
	request := map[string]any{
		"model":    payload["model"],
		"messages": messages,
	}
	copyParam(payload, request, "stream", "stream")
	copyParam(payload, request, "temperature", "temperature")
	copyParam(payload, request, "top_p", "top_p")
	copyParam(payload, request, "presence_penalty", "presence_penalty")
	copyParam(payload, request, "frequency_penalty", "frequency_penalty")
	copyParam(payload, request, "stop", "stop")
	copyParam(payload, request, "max_output_tokens", "max_tokens")
	copyParam(payload, request, "service_tier", "service_tier")
	copyParam(payload, request, "prompt_cache_key", "prompt_cache_key")
	if tools := convertTools(payload["tools"]); len(tools) > 0 {
		request["tools"] = tools
	}
	if choice, ok := payload["tool_choice"]; ok && choice != nil {
		request["tool_choice"] = convertToolChoice(choice)
	}
	return request, nil
}

func FromChatCompletion(completion map[string]any, original map[string]any, responseID string) map[string]any {
	if responseID == "" {
		responseID = GenerateResponseID()
	}
	output := make([]any, 0)
	outputTextParts := make([]string, 0)
	finishReasons := map[string]bool{}
	if choices, ok := completion["choices"].([]any); ok {
		for _, rawChoice := range choices {
			choice, ok := rawChoice.(map[string]any)
			if !ok {
				continue
			}
			finishReason, _ := choice["finish_reason"].(string)
			if finishReason != "" {
				finishReasons[finishReason] = true
			}
			message, _ := choice["message"].(map[string]any)
			if message == nil {
				continue
			}
			contentItems, textParts, contentReasoning := chatContentToOutput(message["content"], message["refusal"])
			reasoningParts := append(reasoningSummaryTextsFromMessage(message), contentReasoning...)
			if len(reasoningParts) > 0 {
				output = append(output, reasoningOutputItem(reasoningParts))
			}
			if len(contentItems) > 0 || hasPresentContent(message) {
				messageStatus := "completed"
				if finishReason == "length" {
					messageStatus = "incomplete"
				}
				output = append(output, map[string]any{
					"id":      GenerateMessageID(),
					"type":    "message",
					"role":    "assistant",
					"status":  messageStatus,
					"content": contentItems,
				})
				outputTextParts = append(outputTextParts, textParts...)
			}
			if calls, ok := message["tool_calls"].([]any); ok {
				for _, rawCall := range calls {
					call, ok := rawCall.(map[string]any)
					if !ok {
						continue
					}
					function, _ := call["function"].(map[string]any)
					callID := firstNonEmptyString(call["id"], GenerateCallID())
					output = append(output, map[string]any{
						"id":        callID,
						"type":      "function_call",
						"call_id":   callID,
						"name":      stringify(function["name"]),
						"arguments": stringify(function["arguments"]),
						"status":    "completed",
					})
				}
			}
		}
	}
	now := float64(time.Now().Unix())
	status, incompleteDetails, responseError := terminalResponseState(finishReasons)
	response := map[string]any{
		"id":                   responseID,
		"object":               "response",
		"created_at":           now,
		"completed_at":         now,
		"status":               status,
		"model":                firstNonEmptyString(completion["model"], stringify(original["model"])),
		"previous_response_id": original["previous_response_id"],
		"output":               output,
		"output_text":          strings.Join(outputTextParts, ""),
		"usage":                convertUsage(completion["usage"]),
		"error":                responseError,
		"incomplete_details":   incompleteDetails,
		"metadata":             mapOrEmpty(original["metadata"]),
	}
	for _, key := range []string{"temperature", "top_p", "max_output_tokens", "tools", "tool_choice", "service_tier", "prompt_cache_key"} {
		if value, ok := original[key]; ok {
			response[key] = value
		}
	}
	if value, ok := completion["service_tier"]; ok {
		response["service_tier"] = value
	}
	return response
}

func terminalResponseState(finishReasons map[string]bool) (string, any, any) {
	if finishReasons["length"] {
		return "incomplete", map[string]any{"reason": "max_output_tokens"}, nil
	}
	if finishReasons["content_filter"] {
		return "failed", nil, responseErrorObject("model_error", "content_filter", "Response filtered by content policy.", nil)
	}
	return "completed", nil, nil
}

func hasPresentContent(message map[string]any) bool {
	if message == nil {
		return false
	}
	value, ok := message["content"]
	return ok && value != nil
}

func ErrorResponse(responseID string, errorType string, code string, message string, model string, params ...string) map[string]any {
	if responseID == "" {
		responseID = GenerateResponseID()
	}
	var param any
	if len(params) > 0 && strings.TrimSpace(params[0]) != "" {
		param = params[0]
	}
	return map[string]any{
		"id":          responseID,
		"object":      "response",
		"created_at":  float64(time.Now().Unix()),
		"status":      "failed",
		"model":       model,
		"output":      []any{},
		"output_text": "",
		"error": map[string]any{
			"type":    errorType,
			"code":    code,
			"param":   param,
			"message": message,
		},
	}
}

func responseErrorObject(errorType string, code string, message string, param any) map[string]any {
	return map[string]any{
		"type":    errorType,
		"code":    code,
		"param":   param,
		"message": message,
	}
}

func inputToMessages(input any) []any {
	switch typed := input.(type) {
	case string:
		return []any{map[string]any{"role": "user", "content": typed}}
	case []any:
		out := make([]any, 0, len(typed))
		for _, raw := range typed {
			out = append(out, itemToMessages(raw)...)
		}
		return out
	default:
		return []any{map[string]any{"role": "user", "content": stringify(input)}}
	}
}

func itemToMessages(raw any) []any {
	item, ok := raw.(map[string]any)
	if !ok {
		return nil
	}
	if item["type"] == "message" {
		role := firstNonEmptyString(item["role"], "user")
		if role == "developer" {
			role = "system"
		}
		content := responsesContentToChat(item["content"])
		if role == "system" {
			return instructionAndMediaMessages(role, content)
		}
		return []any{map[string]any{"role": role, "content": content}}
	}
	if msg := itemToMessage(raw); msg != nil {
		return []any{msg}
	}
	return nil
}

func itemToMessage(raw any) map[string]any {
	item, ok := raw.(map[string]any)
	if !ok {
		return nil
	}
	switch item["type"] {
	case "function_call":
		callID := firstNonEmptyString(firstNonNil(item["call_id"], item["id"]), GenerateCallID())
		return map[string]any{
			"role":    "assistant",
			"content": nil,
			"tool_calls": []any{map[string]any{
				"id":   callID,
				"type": "function",
				"function": map[string]any{
					"name":      stringify(item["name"]),
					"arguments": firstNonEmptyString(item["arguments"], "{}"),
				},
			}},
		}
	case "function_call_output":
		return map[string]any{
			"role":         "tool",
			"tool_call_id": stringify(item["call_id"]),
			"content":      stringify(item["output"]),
		}
	default:
		return nil
	}
}

func instructionAndMediaMessages(role string, content any) []any {
	parts, ok := content.([]any)
	if !ok {
		return []any{map[string]any{"role": role, "content": content}}
	}
	textParts := make([]any, 0, len(parts))
	mediaParts := make([]any, 0, len(parts))
	for _, raw := range parts {
		part, ok := raw.(map[string]any)
		if ok && part["type"] == "text" {
			textParts = append(textParts, part)
			continue
		}
		mediaParts = append(mediaParts, raw)
	}
	out := make([]any, 0, 2)
	if len(textParts) > 0 {
		textContent := any(textParts)
		if len(textParts) == 1 {
			textContent = stringify(textParts[0].(map[string]any)["text"])
		}
		out = append(out, map[string]any{"role": role, "content": textContent})
	}
	if len(mediaParts) > 0 {
		out = append(out, map[string]any{"role": "user", "content": mediaParts})
	}
	if len(out) == 0 {
		return []any{map[string]any{"role": role, "content": ""}}
	}
	return out
}

func responsesContentToChat(content any) any {
	parts, ok := content.([]any)
	if !ok {
		return stringify(content)
	}
	if len(parts) == 1 {
		if part, ok := parts[0].(map[string]any); ok && (part["type"] == "input_text" || part["type"] == "output_text") {
			return stringify(part["text"])
		}
	}
	out := make([]any, 0, len(parts))
	for _, raw := range parts {
		part, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		switch part["type"] {
		case "input_text", "output_text", "summary_text", "reasoning_text":
			out = append(out, map[string]any{"type": "text", "text": stringify(part["text"])})
		case "refusal":
			out = append(out, map[string]any{"type": "text", "text": firstNonEmptyString(part["refusal"], stringify(part["text"]))})
		case "input_image":
			out = append(out, responseImagePartToChat(part))
		}
	}
	if len(out) == 0 {
		return ""
	}
	return out
}

func responseImagePartToChat(part map[string]any) map[string]any {
	if url := stringify(part["image_url"]); url != "" {
		imageURL := map[string]any{"detail": firstNonEmptyString(part["detail"], "auto")}
		imageURL["url"] = url
		return map[string]any{"type": "image_url", "image_url": imageURL}
	}
	if fileID := stringify(part["file_id"]); fileID != "" {
		return map[string]any{"type": "file", "file": map[string]any{"file_id": fileID}}
	}
	return map[string]any{"type": "image_url", "image_url": map[string]any{"detail": firstNonEmptyString(part["detail"], "auto")}}
}

func chatContentToOutput(content any, refusal any) ([]any, []string, []string) {
	items := make([]any, 0)
	textParts := make([]string, 0)
	reasoningParts := make([]string, 0)
	addText := func(text string, annotations any) {
		if text == "" {
			return
		}
		if annotations == nil {
			annotations = []any{}
		}
		items = append(items, map[string]any{"type": "output_text", "text": text, "annotations": annotations})
		textParts = append(textParts, text)
	}
	switch typed := content.(type) {
	case string:
		addText(typed, nil)
	case []any:
		for _, raw := range typed {
			part, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			switch part["type"] {
			case "text", "output_text":
				addText(stringify(part["text"]), part["annotations"])
			case "refusal":
				items = append(items, map[string]any{"type": "refusal", "refusal": firstNonEmptyString(part["refusal"], stringify(part["text"]))})
			case "summary_text", "reasoning_text", "reasoning":
				reasoningParts = append(reasoningParts, reasoningSummaryTextsFromContentPart(part)...)
			default:
				if text := stringify(part["text"]); text != "" {
					addText(text, part["annotations"])
				}
			}
		}
	case map[string]any:
		switch typed["type"] {
		case "refusal":
			items = append(items, map[string]any{"type": "refusal", "refusal": firstNonEmptyString(typed["refusal"], stringify(typed["text"]))})
		case "summary_text", "reasoning_text", "reasoning":
			reasoningParts = append(reasoningParts, reasoningSummaryTextsFromContentPart(typed)...)
		default:
			addText(stringify(typed["text"]), typed["annotations"])
		}
	}
	if text := stringify(refusal); text != "" {
		items = append(items, map[string]any{"type": "refusal", "refusal": text})
	}
	return items, textParts, compactStrings(reasoningParts)
}

func reasoningOutputItem(texts []string) map[string]any {
	summary := make([]any, 0, len(texts))
	for _, text := range compactStrings(texts) {
		summary = append(summary, map[string]any{"type": "summary_text", "text": text})
	}
	return map[string]any{
		"id":      GenerateReasoningID(),
		"type":    "reasoning",
		"summary": summary,
	}
}

func reasoningSummaryTextsFromMessage(message map[string]any) []string {
	if message == nil {
		return nil
	}
	texts := make([]string, 0)
	for _, key := range []string{"reasoning_content", "reasoning", "reasoning_details"} {
		texts = append(texts, reasoningSummaryTextsFromValue(message[key])...)
	}
	return compactStrings(texts)
}

func reasoningSummaryTextsFromContentPart(part map[string]any) []string {
	if part == nil {
		return nil
	}
	switch part["type"] {
	case "summary_text", "reasoning_text":
		return compactStrings([]string{stringify(part["text"])})
	case "reasoning":
		if texts := reasoningSummaryTextsFromValue(part["summary"]); len(texts) > 0 {
			return texts
		}
		for _, key := range []string{"reasoning_content", "text", "content", "reasoning_text"} {
			if texts := reasoningSummaryTextsFromValue(part[key]); len(texts) > 0 {
				return texts
			}
		}
	}
	return nil
}

func reasoningSummaryTextsFromValue(value any) []string {
	switch typed := value.(type) {
	case nil:
		return nil
	case string:
		return compactStrings([]string{typed})
	case []any:
		out := make([]string, 0)
		for _, item := range typed {
			out = append(out, reasoningSummaryTextsFromValue(item)...)
		}
		return compactStrings(out)
	case map[string]any:
		if texts := reasoningSummaryTextsFromContentPart(typed); len(texts) > 0 {
			return texts
		}
		if texts := reasoningSummaryTextsFromValue(typed["summary"]); len(texts) > 0 {
			return texts
		}
		for _, key := range []string{"reasoning_content", "text", "content", "reasoning", "reasoning_text", "summary_text"} {
			if texts := reasoningSummaryTextsFromValue(typed[key]); len(texts) > 0 {
				return texts
			}
		}
	}
	return nil
}

func reasoningSummaryTextsFromDelta(delta map[string]any) []string {
	if delta == nil {
		return nil
	}
	texts := make([]string, 0)
	for _, key := range []string{"reasoning_content", "reasoning", "reasoning_details"} {
		texts = append(texts, reasoningSummaryTextsFromValue(delta[key])...)
	}
	texts = append(texts, reasoningSummaryTextsFromStreamContent(delta["content"])...)
	return compactStrings(texts)
}

func reasoningSummaryTextsFromStreamContent(content any) []string {
	switch typed := content.(type) {
	case []any:
		out := make([]string, 0)
		for _, raw := range typed {
			if part, ok := raw.(map[string]any); ok {
				out = append(out, reasoningSummaryTextsFromContentPart(part)...)
			}
		}
		return compactStrings(out)
	case map[string]any:
		return reasoningSummaryTextsFromContentPart(typed)
	default:
		return nil
	}
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

func convertTools(value any) []any {
	rawTools, ok := value.([]any)
	if !ok {
		return nil
	}
	out := make([]any, 0, len(rawTools))
	for _, raw := range rawTools {
		tool, ok := raw.(map[string]any)
		if !ok || firstNonEmptyString(tool["type"], "function") != "function" {
			continue
		}
		out = append(out, map[string]any{
			"type": "function",
			"function": map[string]any{
				"name":        stringify(tool["name"]),
				"description": stringify(tool["description"]),
				"parameters":  firstNonNil(tool["parameters"], map[string]any{}),
			},
		})
	}
	return out
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
		case "function", "tool":
			name := stringify(typed["name"])
			if function, ok := typed["function"].(map[string]any); ok {
				name = firstNonEmptyString(function["name"], name)
			}
			return map[string]any{"type": "function", "function": map[string]any{"name": name}}
		case "required", "any":
			return "required"
		case "auto":
			return "auto"
		case "none":
			return "none"
		default:
			return value
		}
	default:
		return value
	}
}

func convertUsage(value any) map[string]any {
	usage, _ := value.(map[string]any)
	out := map[string]any{}
	if value, ok := usage["prompt_tokens"]; ok {
		out["input_tokens"] = value
	}
	if value, ok := usage["completion_tokens"]; ok {
		out["output_tokens"] = value
	}
	if value, ok := usage["total_tokens"]; ok {
		out["total_tokens"] = value
	}
	if details, ok := usage["prompt_tokens_details"].(map[string]any); ok {
		if value, ok := details["cached_tokens"]; ok {
			out["input_tokens_details"] = map[string]any{"cached_tokens": value}
		}
	}
	if details, ok := usage["completion_tokens_details"].(map[string]any); ok {
		if value, ok := details["reasoning_tokens"]; ok {
			out["output_tokens_details"] = map[string]any{"reasoning_tokens": value}
		}
	}
	return out
}

type Store struct {
	mu         sync.Mutex
	max        int
	order      []string
	records    map[string]Record
	persistent PersistentStore
}

type Record struct {
	Response      map[string]any
	OriginalInput any
}

type PersistentStore interface {
	UpsertResponseState(context.Context, PersistentRecord) error
	GetResponseState(context.Context, string) (PersistentRecord, bool, error)
}

type PersistentRecord struct {
	ID                 string
	PreviousResponseID string
	Model              string
	Status             string
	InputData          any
	OutputData         []any
	FullResponse       map[string]any
	Usage              map[string]any
	Metadata           map[string]any
	CreatedAt          time.Time
	ExpiresAt          *time.Time
}

func NewStore(maxEntries int) *Store {
	if maxEntries <= 0 {
		maxEntries = 1000
	}
	return &Store{max: maxEntries, records: map[string]Record{}}
}

func (s *Store) SetPersistent(persistent PersistentStore) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.persistent = persistent
}

func (s *Store) Store(response map[string]any, originalInput any) string {
	if s == nil || response == nil {
		return ""
	}
	id := stringify(response["id"])
	if id == "" {
		return ""
	}
	record := Record{Response: response, OriginalInput: originalInput}
	s.mu.Lock()
	s.storeLocked(id, record)
	persistent := s.persistent
	s.mu.Unlock()
	if persistent != nil {
		_ = persistent.UpsertResponseState(context.Background(), persistentRecordFrom(response, originalInput))
	}
	return id
}

func (s *Store) ConversationHistory(responseID string) []any {
	if s == nil || responseID == "" {
		return nil
	}
	records := make([]Record, 0)
	seen := map[string]bool{}
	for current := responseID; current != "" && !seen[current]; {
		seen[current] = true
		record, ok := s.record(current)
		if !ok {
			break
		}
		records = append([]Record{record}, records...)
		current = stringify(record.Response["previous_response_id"])
	}
	history := make([]any, 0)
	for _, record := range records {
		history = append(history, inputToMessages(record.OriginalInput)...)
		history = append(history, outputToMessages(record.Response["output"])...)
	}
	return history
}

func (s *Store) record(id string) (Record, bool) {
	s.mu.Lock()
	record, ok := s.records[id]
	persistent := s.persistent
	s.mu.Unlock()
	if ok {
		return record, true
	}
	if persistent == nil {
		return Record{}, false
	}
	persisted, ok, err := persistent.GetResponseState(context.Background(), id)
	if err != nil || !ok {
		return Record{}, false
	}
	record = Record{Response: responseFromPersistent(persisted), OriginalInput: persisted.InputData}
	if record.Response == nil {
		return Record{}, false
	}
	s.mu.Lock()
	s.storeLocked(id, record)
	s.mu.Unlock()
	return record, true
}

func (s *Store) storeLocked(id string, record Record) {
	if _, exists := s.records[id]; !exists {
		s.order = append(s.order, id)
	}
	s.records[id] = record
	for len(s.order) > s.max {
		evict := s.order[0]
		s.order = s.order[1:]
		delete(s.records, evict)
	}
}

func persistentRecordFrom(response map[string]any, originalInput any) PersistentRecord {
	output, _ := response["output"].([]any)
	usage, _ := response["usage"].(map[string]any)
	metadata, _ := response["metadata"].(map[string]any)
	return PersistentRecord{
		ID:                 stringify(response["id"]),
		PreviousResponseID: stringify(response["previous_response_id"]),
		Model:              stringify(response["model"]),
		Status:             firstNonEmptyString(response["status"], "completed"),
		InputData:          originalInput,
		OutputData:         output,
		FullResponse:       CloneMap(response),
		Usage:              usage,
		Metadata:           metadata,
		CreatedAt:          responseTime(response["created_at"]),
	}
}

func responseFromPersistent(record PersistentRecord) map[string]any {
	if record.FullResponse != nil {
		return CloneMap(record.FullResponse)
	}
	response := map[string]any{
		"id":                   record.ID,
		"object":               "response",
		"previous_response_id": record.PreviousResponseID,
		"model":                record.Model,
		"status":               firstNonEmptyString(record.Status, "completed"),
		"output":               record.OutputData,
		"usage":                record.Usage,
		"metadata":             record.Metadata,
	}
	return response
}

func responseTime(value any) time.Time {
	switch typed := value.(type) {
	case int:
		return time.Unix(int64(typed), 0).UTC()
	case int64:
		return time.Unix(typed, 0).UTC()
	case float64:
		return time.Unix(int64(typed), 0).UTC()
	case json.Number:
		seconds, err := typed.Int64()
		if err == nil {
			return time.Unix(seconds, 0).UTC()
		}
	case string:
		if parsed, err := time.Parse(time.RFC3339Nano, typed); err == nil {
			return parsed.UTC()
		}
	}
	return time.Now().UTC()
}

func outputToMessages(output any) []any {
	items, ok := output.([]any)
	if !ok {
		return nil
	}
	messages := make([]any, 0)
	for _, raw := range items {
		item, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		switch item["type"] {
		case "message":
			messages = append(messages, map[string]any{"role": firstNonEmptyString(item["role"], "assistant"), "content": responsesContentToChat(item["content"])})
		case "function_call":
			messages = append(messages, itemToMessage(item))
		case "function_call_output":
			messages = append(messages, itemToMessage(item))
		}
	}
	return messages
}

func copyParam(src map[string]any, dst map[string]any, from string, to string) {
	if value, ok := src[from]; ok && value != nil {
		dst[to] = value
	}
}

func stringify(value any) string {
	if value == nil {
		return ""
	}
	if text, ok := value.(string); ok {
		return text
	}
	return fmt.Sprint(value)
}

func firstNonEmptyString(value any, fallback string) string {
	text := strings.TrimSpace(stringify(value))
	if text == "" || text == "<nil>" {
		return fallback
	}
	return text
}

func firstNonNil(values ...any) any {
	for _, value := range values {
		if value != nil {
			return value
		}
	}
	return nil
}

func mapOrEmpty(value any) any {
	if value == nil {
		return map[string]any{}
	}
	return value
}

func CloneMap(value map[string]any) map[string]any {
	raw, err := json.Marshal(value)
	if err != nil {
		return value
	}
	out := map[string]any{}
	if err := json.Unmarshal(raw, &out); err != nil {
		return value
	}
	return out
}
