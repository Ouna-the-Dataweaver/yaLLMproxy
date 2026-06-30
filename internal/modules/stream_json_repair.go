package modules

import (
	"encoding/json"
	"sort"
	"strings"
	"time"
)

const (
	streamJSONRepairFailurePassthrough = "passthrough"
	streamJSONRepairFailureEmptyObject = "empty_object"
)

type StreamJSONRepairConfig struct {
	Enabled              bool
	MaxStreamBufferBytes int
	MaxStreamBufferAge   time.Duration
	OnFailure            string
}

type StreamJSONRepair struct {
	Config StreamJSONRepairConfig
}

type streamJSONRepairState struct {
	calls      map[streamJSONRepairKey]*streamJSONRepairCall
	bypass     map[streamJSONRepairKey]bool
	totalBytes int
}

type streamJSONRepairKey struct {
	choice int
	tool   int
}

type streamJSONRepairCall struct {
	buffer        string
	bufferedSince time.Time
}

func NewStreamJSONRepair(cfg StreamJSONRepairConfig) *StreamJSONRepair {
	if cfg.MaxStreamBufferBytes <= 0 {
		cfg.MaxStreamBufferBytes = defaultMaxStreamBufferBytes
	}
	cfg.OnFailure = normalizeStreamJSONRepairFailure(cfg.OnFailure)
	return &StreamJSONRepair{Config: cfg}
}

func (m *StreamJSONRepair) Name() string { return "stream_json_repair" }

func (m *StreamJSONRepair) TransformPayload(_ map[string]any, _ Context) bool {
	return false
}

func (m *StreamJSONRepair) TransformStreamPayload(_ map[string]any, _ Context) bool {
	return false
}

func (m *StreamJSONRepair) NewStreamState() any {
	return &streamJSONRepairState{
		calls:  map[streamJSONRepairKey]*streamJSONRepairCall{},
		bypass: map[streamJSONRepairKey]bool{},
	}
}

func (m *StreamJSONRepair) TransformStreamPayloadWithState(payload map[string]any, ctx Context, rawState any) bool {
	if !m.Config.Enabled {
		return false
	}
	state, ok := rawState.(*streamJSONRepairState)
	if !ok || state == nil {
		return false
	}
	choices, ok := payload["choices"].([]any)
	if !ok {
		return false
	}
	changed := false
	now := ctx.now()
	if m.flushAgedCalls(payload, state, now) {
		changed = true
		choices, _ = payload["choices"].([]any)
	}
	for position, rawChoice := range choices {
		choice, ok := rawChoice.(map[string]any)
		if !ok {
			continue
		}
		choiceIdx := choiceIndex(choice, position)
		finishReason, hasFinish := choice["finish_reason"].(string)
		delta, _ := choice["delta"].(map[string]any)
		if delta == nil {
			if !hasFinish || finishReason == "" || !state.hasPendingChoice(choiceIdx) {
				if hasFinish && finishReason != "" {
					state.clearBypassChoice(choiceIdx)
				}
				continue
			}
			delta = map[string]any{}
			choice["delta"] = delta
			changed = true
		}
		if m.transformDeltaToolCalls(state, choiceIdx, delta, now) {
			changed = true
		}
		if hasFinish && finishReason != "" {
			if m.flushChoice(state, choiceIdx, delta) {
				changed = true
			}
			state.clearBypassChoice(choiceIdx)
		}
	}
	return changed
}

func (m *StreamJSONRepair) FlushStreamPayloads(_ Context, rawState any) []map[string]any {
	if !m.Config.Enabled {
		return nil
	}
	state, ok := rawState.(*streamJSONRepairState)
	if !ok || state == nil || len(state.calls) == 0 {
		return nil
	}
	byChoice := map[int][]any{}
	for _, key := range state.pendingKeys() {
		byChoice[key.choice] = append(byChoice[key.choice], m.finishToolCall(state, key))
	}
	state.bypass = map[streamJSONRepairKey]bool{}
	choiceIndexes := make([]int, 0, len(byChoice))
	for choiceIdx := range byChoice {
		choiceIndexes = append(choiceIndexes, choiceIdx)
	}
	sort.Ints(choiceIndexes)
	choices := make([]any, 0, len(choiceIndexes))
	for _, choiceIdx := range choiceIndexes {
		choices = append(choices, map[string]any{
			"index": choiceIdx,
			"delta": map[string]any{
				"tool_calls": byChoice[choiceIdx],
			},
		})
	}
	if len(choices) == 0 {
		return nil
	}
	return []map[string]any{{"choices": choices}}
}

func (m *StreamJSONRepair) ResetStreamState(rawState any) {
	resetStreamJSONRepairState(rawState)
}

func (m *StreamJSONRepair) CloseStreamState(rawState any) {
	resetStreamJSONRepairState(rawState)
}

func resetStreamJSONRepairState(rawState any) {
	state, ok := rawState.(*streamJSONRepairState)
	if !ok || state == nil {
		return
	}
	state.calls = map[streamJSONRepairKey]*streamJSONRepairCall{}
	state.bypass = map[streamJSONRepairKey]bool{}
	state.totalBytes = 0
}

func (m *StreamJSONRepair) transformDeltaToolCalls(state *streamJSONRepairState, choiceIdx int, delta map[string]any, now time.Time) bool {
	rawCalls, ok := delta["tool_calls"].([]any)
	if !ok {
		return false
	}
	currentTools := map[int]bool{}
	out := make([]any, 0, len(rawCalls))
	callsChanged := false
	for position, rawCall := range rawCalls {
		call, ok := rawCall.(map[string]any)
		if !ok {
			out = append(out, rawCall)
			continue
		}
		toolIdx := choiceIndex(call, position)
		currentTools[toolIdx] = true
		keep, changed := m.transformToolCall(state, streamJSONRepairKey{choice: choiceIdx, tool: toolIdx}, call, now)
		if changed {
			callsChanged = true
		}
		if keep {
			out = append(out, call)
		}
	}
	changed := callsChanged
	if callsChanged {
		if len(out) == 0 {
			delete(delta, "tool_calls")
		} else {
			delta["tool_calls"] = out
		}
	}
	if len(currentTools) > 0 && m.flushChoiceExcept(state, choiceIdx, currentTools, delta) {
		changed = true
	}
	return changed
}

func (m *StreamJSONRepair) transformToolCall(state *streamJSONRepairState, key streamJSONRepairKey, call map[string]any, now time.Time) (bool, bool) {
	if state.bypass[key] {
		return true, false
	}
	function, ok := call["function"].(map[string]any)
	if !ok {
		return true, false
	}
	fragment, ok := function["arguments"].(string)
	if !ok {
		return true, false
	}
	if pending := state.calls[key]; pending != nil {
		return m.appendPendingArgument(state, key, pending, call, fragment)
	}
	if validStructuredJSON(fragment) {
		return true, false
	}
	if !looksStructuredJSONFragment(fragment) {
		return true, false
	}
	if len(fragment) > m.Config.MaxStreamBufferBytes || state.totalBytes+len(fragment) > m.Config.MaxStreamBufferBytes {
		state.bypass[key] = true
		return true, false
	}
	state.calls[key] = &streamJSONRepairCall{buffer: fragment, bufferedSince: now}
	state.totalBytes += len(fragment)
	return stripToolCallArguments(call), true
}

func (m *StreamJSONRepair) flushAgedCalls(payload map[string]any, state *streamJSONRepairState, now time.Time) bool {
	if m.Config.MaxStreamBufferAge <= 0 || state == nil || len(state.calls) == 0 {
		return false
	}
	byChoice := map[int][]any{}
	for _, key := range state.pendingKeys() {
		pending := state.calls[key]
		if pending == nil || pending.bufferedSince.IsZero() || now.Sub(pending.bufferedSince) <= m.Config.MaxStreamBufferAge {
			continue
		}
		byChoice[key.choice] = append(byChoice[key.choice], releaseRawToolCall(state, key))
	}
	if len(byChoice) == 0 {
		return false
	}
	choices, _ := payload["choices"].([]any)
	choiceIndexes := make([]int, 0, len(byChoice))
	for choiceIdx := range byChoice {
		choiceIndexes = append(choiceIndexes, choiceIdx)
	}
	sort.Ints(choiceIndexes)
	for _, choiceIdx := range choiceIndexes {
		delta := ensureChoiceDelta(&choices, choiceIdx)
		prependDeltaToolCalls(delta, byChoice[choiceIdx])
	}
	payload["choices"] = choices
	return true
}

func (m *StreamJSONRepair) appendPendingArgument(state *streamJSONRepairState, key streamJSONRepairKey, pending *streamJSONRepairCall, call map[string]any, fragment string) (bool, bool) {
	combined := pending.buffer + fragment
	if len(combined) > m.Config.MaxStreamBufferBytes || state.totalBytes+len(fragment) > m.Config.MaxStreamBufferBytes {
		state.totalBytes -= len(pending.buffer)
		if state.totalBytes < 0 {
			state.totalBytes = 0
		}
		delete(state.calls, key)
		state.bypass[key] = true
		setToolCallArguments(call, combined)
		return true, true
	}
	pending.buffer = combined
	state.totalBytes += len(fragment)
	if validStructuredJSON(combined) {
		state.totalBytes -= len(combined)
		if state.totalBytes < 0 {
			state.totalBytes = 0
		}
		delete(state.calls, key)
		setToolCallArguments(call, combined)
		return true, true
	}
	return stripToolCallArguments(call), true
}

func (m *StreamJSONRepair) flushChoiceExcept(state *streamJSONRepairState, choiceIdx int, activeTools map[int]bool, delta map[string]any) bool {
	var additions []any
	for _, key := range state.pendingKeys() {
		if key.choice != choiceIdx || activeTools[key.tool] {
			continue
		}
		additions = append(additions, m.finishToolCall(state, key))
	}
	if len(additions) == 0 {
		return false
	}
	appendDeltaToolCalls(delta, additions)
	return true
}

func (m *StreamJSONRepair) flushChoice(state *streamJSONRepairState, choiceIdx int, delta map[string]any) bool {
	var additions []any
	for _, key := range state.pendingKeys() {
		if key.choice == choiceIdx {
			additions = append(additions, m.finishToolCall(state, key))
		}
	}
	if len(additions) == 0 {
		return false
	}
	appendDeltaToolCalls(delta, additions)
	return true
}

func (m *StreamJSONRepair) finishToolCall(state *streamJSONRepairState, key streamJSONRepairKey) any {
	pending := state.calls[key]
	if pending == nil {
		return map[string]any{"index": key.tool, "function": map[string]any{"arguments": "{}"}}
	}
	args := m.finishArguments(pending.buffer)
	state.totalBytes -= len(pending.buffer)
	if state.totalBytes < 0 {
		state.totalBytes = 0
	}
	delete(state.calls, key)
	return map[string]any{
		"index": key.tool,
		"function": map[string]any{
			"arguments": args,
		},
	}
}

func releaseRawToolCall(state *streamJSONRepairState, key streamJSONRepairKey) any {
	pending := state.calls[key]
	args := ""
	if pending != nil {
		args = pending.buffer
		state.totalBytes -= len(pending.buffer)
		if state.totalBytes < 0 {
			state.totalBytes = 0
		}
	}
	delete(state.calls, key)
	state.bypass[key] = true
	return map[string]any{
		"index": key.tool,
		"function": map[string]any{
			"arguments": args,
		},
	}
}

func ensureChoiceDelta(choices *[]any, choiceIdx int) map[string]any {
	for _, rawChoice := range *choices {
		choice, ok := rawChoice.(map[string]any)
		if !ok || choiceIndex(choice, -1) != choiceIdx {
			continue
		}
		delta, _ := choice["delta"].(map[string]any)
		if delta == nil {
			delta = map[string]any{}
			choice["delta"] = delta
		}
		return delta
	}
	delta := map[string]any{}
	*choices = append(*choices, map[string]any{
		"index": choiceIdx,
		"delta": delta,
	})
	return delta
}

func (m *StreamJSONRepair) finishArguments(buffer string) string {
	if validStructuredJSON(buffer) {
		return buffer
	}
	if repaired, ok := repairJSONFragment(buffer); ok {
		return repaired
	}
	if m.Config.OnFailure == streamJSONRepairFailureEmptyObject {
		return "{}"
	}
	return buffer
}

func (state *streamJSONRepairState) pendingKeys() []streamJSONRepairKey {
	keys := make([]streamJSONRepairKey, 0, len(state.calls))
	for key := range state.calls {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].choice != keys[j].choice {
			return keys[i].choice < keys[j].choice
		}
		return keys[i].tool < keys[j].tool
	})
	return keys
}

func (state *streamJSONRepairState) hasPendingChoice(choiceIdx int) bool {
	for key := range state.calls {
		if key.choice == choiceIdx {
			return true
		}
	}
	return false
}

func (state *streamJSONRepairState) clearBypassChoice(choiceIdx int) {
	for key := range state.bypass {
		if key.choice == choiceIdx {
			delete(state.bypass, key)
		}
	}
}

func setToolCallArguments(call map[string]any, arguments string) {
	function, _ := call["function"].(map[string]any)
	if function == nil {
		function = map[string]any{}
		call["function"] = function
	}
	function["arguments"] = arguments
}

func stripToolCallArguments(call map[string]any) bool {
	function, _ := call["function"].(map[string]any)
	if function == nil {
		return true
	}
	delete(function, "arguments")
	if len(function) == 0 {
		delete(call, "function")
	}
	return hasVisibleToolCallFields(call)
}

func hasVisibleToolCallFields(call map[string]any) bool {
	for key, value := range call {
		if key == "index" {
			continue
		}
		if key == "function" {
			if function, ok := value.(map[string]any); ok && len(function) == 0 {
				continue
			}
		}
		return true
	}
	return false
}

func appendDeltaToolCalls(delta map[string]any, additions []any) {
	if len(additions) == 0 {
		return
	}
	existing, _ := delta["tool_calls"].([]any)
	merged := make([]any, 0, len(existing)+len(additions))
	merged = append(merged, existing...)
	merged = append(merged, additions...)
	delta["tool_calls"] = merged
}

func prependDeltaToolCalls(delta map[string]any, additions []any) {
	if len(additions) == 0 {
		return
	}
	existing, _ := delta["tool_calls"].([]any)
	merged := make([]any, 0, len(existing)+len(additions))
	merged = append(merged, additions...)
	merged = append(merged, existing...)
	delta["tool_calls"] = merged
}

func normalizeStreamJSONRepairFailure(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", streamJSONRepairFailurePassthrough, "pass_through":
		return streamJSONRepairFailurePassthrough
	case "empty", streamJSONRepairFailureEmptyObject, "empty_object_on_failure":
		return streamJSONRepairFailureEmptyObject
	default:
		return streamJSONRepairFailurePassthrough
	}
}

func looksStructuredJSONFragment(value string) bool {
	trimmed := strings.TrimLeftFunc(value, isJSONSpaceRune)
	return strings.HasPrefix(trimmed, "{") || strings.HasPrefix(trimmed, "[")
}

func validStructuredJSON(value string) bool {
	trimmed := strings.TrimSpace(value)
	if !strings.HasPrefix(trimmed, "{") && !strings.HasPrefix(trimmed, "[") {
		return false
	}
	return json.Valid([]byte(trimmed))
}

func repairJSONFragment(value string) (string, bool) {
	candidate := strings.TrimSpace(value)
	if !looksStructuredJSONFragment(candidate) {
		return "", false
	}
	stack := make([]byte, 0, 8)
	inString := false
	escaped := false
	rootClosed := false
	for i := 0; i < len(candidate); i++ {
		ch := candidate[i]
		if inString {
			if escaped {
				escaped = false
				continue
			}
			switch ch {
			case '\\':
				escaped = true
			case '"':
				inString = false
			}
			continue
		}
		if isJSONSpaceByte(ch) {
			continue
		}
		switch ch {
		case '"':
			if rootClosed {
				return "", false
			}
			inString = true
		case '{':
			if rootClosed {
				return "", false
			}
			stack = append(stack, '}')
		case '[':
			if rootClosed {
				return "", false
			}
			stack = append(stack, ']')
		case '}', ']':
			if len(stack) == 0 || stack[len(stack)-1] != ch {
				return "", false
			}
			stack = stack[:len(stack)-1]
			if len(stack) == 0 {
				rootClosed = true
			}
		default:
			if rootClosed {
				return "", false
			}
		}
	}
	if inString {
		if escaped {
			candidate += `\`
		}
		candidate += `"`
	}
	for len(stack) > 0 {
		candidate = trimTrailingCommaSuffix(candidate)
		candidate += string(stack[len(stack)-1])
		stack = stack[:len(stack)-1]
	}
	candidate = removeTrailingCommas(candidate)
	if validStructuredJSON(candidate) {
		return candidate, true
	}
	return "", false
}

func trimTrailingCommaSuffix(value string) string {
	end := len(value)
	for end > 0 && isJSONSpaceByte(value[end-1]) {
		end--
	}
	if end > 0 && value[end-1] == ',' {
		return strings.TrimRightFunc(value[:end-1], isJSONSpaceRune)
	}
	return value
}

func removeTrailingCommas(value string) string {
	var out strings.Builder
	out.Grow(len(value))
	inString := false
	escaped := false
	for i := 0; i < len(value); i++ {
		ch := value[i]
		if inString {
			out.WriteByte(ch)
			if escaped {
				escaped = false
				continue
			}
			switch ch {
			case '\\':
				escaped = true
			case '"':
				inString = false
			}
			continue
		}
		if ch == '"' {
			inString = true
			out.WriteByte(ch)
			continue
		}
		if ch == ',' {
			j := i + 1
			for j < len(value) && isJSONSpaceByte(value[j]) {
				j++
			}
			if j < len(value) && (value[j] == '}' || value[j] == ']') {
				continue
			}
		}
		out.WriteByte(ch)
	}
	return out.String()
}

func isJSONSpaceByte(ch byte) bool {
	return ch == ' ' || ch == '\t' || ch == '\n' || ch == '\r'
}

func isJSONSpaceRune(ch rune) bool {
	return ch == ' ' || ch == '\t' || ch == '\n' || ch == '\r'
}
