package modules

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"
)

type Context struct {
	Path     string
	Model    string
	Backend  string
	IsStream bool
	Now      func() time.Time
}

func (c Context) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

type Pipeline struct {
	Modules []ResponseModule
	Paths   []string
}

type StreamState struct {
	ModuleStates []any
}

type ResponseModule interface {
	Name() string
	TransformPayload(payload map[string]any, ctx Context) bool
	TransformStreamPayload(payload map[string]any, ctx Context) bool
}

type StatefulResponseModule interface {
	NewStreamState() any
	TransformStreamPayloadWithState(payload map[string]any, ctx Context, state any) bool
}

type StreamFlushResponseModule interface {
	FlushStreamPayloads(ctx Context, state any) []map[string]any
}

type StreamResetResponseModule interface {
	ResetStreamState(state any)
}

type StreamCloseResponseModule interface {
	CloseStreamState(state any)
}

func (p Pipeline) NewStreamState() *StreamState {
	if len(p.Modules) == 0 {
		return nil
	}
	state := &StreamState{ModuleStates: make([]any, len(p.Modules))}
	for i, module := range p.Modules {
		if stateful, ok := module.(StatefulResponseModule); ok {
			state.ModuleStates[i] = stateful.NewStreamState()
		}
	}
	return state
}

func (p Pipeline) Applies(ctx Context) bool {
	if len(p.Modules) == 0 {
		return false
	}
	if len(p.Paths) == 0 {
		return true
	}
	for _, path := range p.Paths {
		if strings.Contains(ctx.Path, path) {
			return true
		}
	}
	return false
}

func (p Pipeline) TransformResponseBody(body []byte, contentType string, ctx Context) []byte {
	if !p.Applies(ctx) || !strings.Contains(strings.ToLower(contentType), "json") {
		return nil
	}
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil
	}
	changed := NormalizeReasoningDetailsPayload(payload)
	for _, module := range p.Modules {
		if module.TransformPayload(payload, ctx) {
			changed = true
		}
	}
	if !changed {
		return nil
	}
	out, err := json.Marshal(payload)
	if err != nil {
		return nil
	}
	return out
}

func (p Pipeline) TransformSSEEvent(data string, ctx Context) (string, bool) {
	return p.TransformSSEEventWithState(data, ctx, nil)
}

func (p Pipeline) TransformSSEEventWithState(data string, ctx Context, state *StreamState) (string, bool) {
	if !p.Applies(ctx) || strings.TrimSpace(data) == "" || strings.TrimSpace(data) == "[DONE]" {
		return data, false
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(data), &payload); err != nil {
		return data, false
	}
	changed := NormalizeReasoningDetailsPayload(payload)
	for i, module := range p.Modules {
		if state != nil && i < len(state.ModuleStates) {
			if stateful, ok := module.(StatefulResponseModule); ok {
				if stateful.TransformStreamPayloadWithState(payload, ctx, state.ModuleStates[i]) {
					changed = true
				}
				continue
			}
		}
		if module.TransformStreamPayload(payload, ctx) {
			changed = true
		}
	}
	if !changed {
		return data, false
	}
	out, err := json.Marshal(payload)
	if err != nil {
		return data, false
	}
	return string(out), true
}

func (p Pipeline) FlushSSEPayloadsWithState(ctx Context, state *StreamState) []map[string]any {
	if !p.Applies(ctx) || state == nil {
		return nil
	}
	var out []map[string]any
	for i, module := range p.Modules {
		flusher, ok := module.(StreamFlushResponseModule)
		if !ok || i >= len(state.ModuleStates) {
			continue
		}
		out = append(out, flusher.FlushStreamPayloads(ctx, state.ModuleStates[i])...)
	}
	return out
}

func (p Pipeline) ResetStreamState(state *StreamState) {
	if state == nil {
		return
	}
	for i, module := range p.Modules {
		resetter, ok := module.(StreamResetResponseModule)
		if !ok || i >= len(state.ModuleStates) {
			continue
		}
		resetter.ResetStreamState(state.ModuleStates[i])
	}
}

func (p Pipeline) CloseStreamState(state *StreamState) {
	if state == nil {
		return
	}
	for i, module := range p.Modules {
		closer, ok := module.(StreamCloseResponseModule)
		if !ok || i >= len(state.ModuleStates) {
			continue
		}
		closer.CloseStreamState(state.ModuleStates[i])
	}
}

func NormalizeReasoningDetailsPayload(payload map[string]any) bool {
	choices, ok := payload["choices"].([]any)
	if !ok {
		return false
	}
	changed := false
	for _, rawChoice := range choices {
		choice, ok := rawChoice.(map[string]any)
		if !ok {
			continue
		}
		for _, key := range []string{"message", "delta"} {
			msg, ok := choice[key].(map[string]any)
			if !ok {
				continue
			}
			if normalizeReasoningDetails(msg) {
				changed = true
			}
		}
	}
	return changed
}

func normalizeReasoningDetails(message map[string]any) bool {
	if existing, ok := message["reasoning_content"].(string); ok && strings.TrimSpace(existing) != "" {
		return false
	}
	text := extractReasoningText(message["reasoning_details"])
	if strings.TrimSpace(text) == "" {
		return false
	}
	message["reasoning_content"] = text
	return true
}

func extractReasoningText(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case []any:
		var b strings.Builder
		for _, item := range typed {
			b.WriteString(extractReasoningText(item))
		}
		return b.String()
	case map[string]any:
		if text, ok := typed["text"].(string); ok {
			return text
		}
		for _, key := range []string{"content", "reasoning", "reasoning_text"} {
			if text := extractReasoningText(typed[key]); text != "" {
				return text
			}
		}
	}
	return ""
}

type ParseTagsConfig struct {
	ThinkTag             string
	ToolTag              string
	ToolOpen             string
	ToolClose            string
	ToolArgFormat        string
	ToolArgSeparator     string
	ToolParser           string
	ToolBufferLimit      int
	MaxStreamBufferBytes int
	MaxStreamBufferAge   time.Duration
	DropTags             []string
	ParseThinking        bool
	ParseToolCalls       bool
	StreamToolCalls      bool
	StartInThink         bool
}

type ParseTags struct {
	Config ParseTagsConfig
}

const defaultMaxStreamBufferBytes = 1 << 20

func DeriveParseTagsConfigFromTemplatePath(path string) (ParseTagsConfig, bool) {
	path = strings.TrimSpace(path)
	if path == "" {
		return ParseTagsConfig{}, false
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return ParseTagsConfig{}, false
	}
	return DeriveParseTagsConfigFromTemplateText(string(raw))
}

func DeriveParseTagsConfigFromTemplateText(text string) (ParseTagsConfig, bool) {
	analysis := AnalyzeTemplateText(text)
	return analysis.Config, analysis.Detected
}

func deriveParseTagsConfigFromTemplateText(text string) (ParseTagsConfig, bool) {
	cfg := ParseTagsConfig{}
	changed := false
	if tag := detectThinkTag(text); tag != "" {
		cfg.ThinkTag = tag
		changed = true
	}
	if strings.Contains(text, "<|tool_call_begin|>") && strings.Contains(text, "<|tool_call_end|>") {
		cfg.ToolArgFormat = "json"
		cfg.ToolOpen = "<|tool_call_begin|>"
		cfg.ToolClose = "<|tool_call_end|>"
		cfg.ToolArgSeparator = "<|tool_call_argument_begin|>"
		cfg.DropTags = []string{"<|tool_calls_section_begin|>", "<|tool_calls_section_end|>"}
		changed = true
	} else if detectQwenXMLToolCallTemplate(text) {
		cfg.ToolArgFormat = "xml"
		cfg.ToolTag = "tool_call"
		cfg.ToolOpen = "<tool_call>"
		cfg.ToolClose = "</tool_call>"
		cfg.ToolParser = "qwen_xml"
		changed = true
	} else if tag := detectToolTag(text); tag != "" {
		cfg.ToolArgFormat = "xml"
		cfg.ToolTag = tag
		cfg.ToolOpen = "<" + tag + ">"
		cfg.ToolClose = "</" + tag + ">"
		changed = true
	}
	return cfg, changed
}

func NewParseTags(cfg ParseTagsConfig) *ParseTags {
	if cfg.ThinkTag == "" {
		cfg.ThinkTag = "think"
	}
	if cfg.ToolTag == "" {
		cfg.ToolTag = "tool_call"
	}
	if cfg.ToolOpen == "" {
		cfg.ToolOpen = "<" + cfg.ToolTag + ">"
	}
	if cfg.ToolClose == "" {
		cfg.ToolClose = "</" + cfg.ToolTag + ">"
	}
	if strings.TrimSpace(cfg.ToolArgFormat) == "" {
		cfg.ToolArgFormat = "xml"
	}
	return &ParseTags{Config: cfg}
}

func (p *ParseTags) Name() string { return "parse_tags" }

func (p *ParseTags) TransformPayload(payload map[string]any, _ Context) bool {
	changed := false
	forEachChoiceMessageWithChoice(payload, "message", func(choice map[string]any, message map[string]any) {
		content, ok := message["content"].(string)
		if !ok || content == "" {
			return
		}
		scanned := p.scan(content)
		if scanned.changed {
			message["content"] = scanned.content
			if scanned.reasoning != "" {
				message["reasoning_content"] = scanned.reasoning
			}
			if len(scanned.toolCalls) > 0 {
				message["tool_calls"] = scanned.toolCalls
				choice["finish_reason"] = "tool_calls"
				if _, ok := message["content"].(string); ok && strings.TrimSpace(message["content"].(string)) == "" {
					message["content"] = nil
				}
			}
			changed = true
		}
	})
	return changed
}

func (p *ParseTags) TransformStreamPayload(payload map[string]any, _ Context) bool {
	changed := false
	forEachChoiceMessage(payload, "delta", func(delta map[string]any) {
		content, ok := delta["content"].(string)
		if !ok || content == "" {
			return
		}
		scanned := p.scan(content)
		if scanned.changed {
			delta["content"] = scanned.content
			if scanned.reasoning != "" {
				delta["reasoning_content"] = scanned.reasoning
			}
			if len(scanned.toolCalls) > 0 {
				delta["tool_calls"] = scanned.toolCalls
				if strings.TrimSpace(scanned.content) == "" {
					delete(delta, "content")
				}
			}
			changed = true
		}
	})
	return changed
}

type parseTagsStreamState struct {
	choices map[int]*parseTagsChoiceState
}

type parseTagsChoiceState struct {
	scanner         *tagScanner
	nextToolIndex   int
	activeToolIndex int
	hasActiveTool   bool
	sawToolCalls    bool
	bufferedSince   time.Time
}

func (p *ParseTags) NewStreamState() any {
	return &parseTagsStreamState{choices: map[int]*parseTagsChoiceState{}}
}

func (p *ParseTags) TransformStreamPayloadWithState(payload map[string]any, ctx Context, rawState any) bool {
	state, ok := rawState.(*parseTagsStreamState)
	if !ok || state == nil {
		return p.TransformStreamPayload(payload, ctx)
	}
	choices, ok := payload["choices"].([]any)
	if !ok {
		return false
	}
	changed := false
	now := ctx.now()
	for position, rawChoice := range choices {
		choice, ok := rawChoice.(map[string]any)
		if !ok {
			continue
		}
		delta, ok := choice["delta"].(map[string]any)
		if !ok {
			continue
		}
		content, ok := delta["content"].(string)
		if !ok || content == "" {
			continue
		}
		index := choiceIndex(choice, position)
		choiceState := state.choices[index]
		if choiceState == nil {
			choiceState = &parseTagsChoiceState{scanner: newStreamTagScanner(p.Config)}
			state.choices[index] = choiceState
		}
		var scanned scanResult
		if choiceState.streamBufferExpired(now, p.Config.MaxStreamBufferAge) {
			scanned = choiceState.flushStreamBuffer()
		}
		scanned = mergeScanResults(scanned, choiceState.scanner.feed(content))
		choiceState.updateBufferedSince(now)
		if scanned.content != content {
			if scanned.content == "" {
				delete(delta, "content")
			} else {
				delta["content"] = scanned.content
			}
			changed = true
		}
		if scanned.reasoning != "" {
			delta["reasoning_content"] = scanned.reasoning
			changed = true
		}
		if len(scanned.toolCalls) > 0 {
			delta["tool_calls"] = scanned.toolCalls
			choice["finish_reason"] = "tool_calls"
			choiceState.sawToolCalls = true
			choiceState.nextToolIndex += len(scanned.toolCalls)
			choiceState.hasActiveTool = false
			changed = true
		}
		if len(scanned.toolCallChunks) > 0 {
			var calls []any
			for _, chunk := range scanned.toolCallChunks {
				toolIndex := choiceState.activeToolIndex
				if chunk.Name != "" || !choiceState.hasActiveTool {
					toolIndex = choiceState.nextToolIndex
					choiceState.activeToolIndex = toolIndex
					choiceState.hasActiveTool = true
				}
				call := map[string]any{
					"index":    toolIndex,
					"function": map[string]any{},
				}
				function := call["function"].(map[string]any)
				if chunk.Name != "" {
					call["id"] = streamToolCallID(index, toolIndex)
					call["type"] = "function"
					function["name"] = chunk.Name
				}
				if chunk.HasArguments {
					function["arguments"] = chunk.Arguments
				}
				calls = append(calls, call)
				if chunk.Finished {
					if choiceState.nextToolIndex < toolIndex+1 {
						choiceState.nextToolIndex = toolIndex + 1
					}
					choiceState.hasActiveTool = false
					choiceState.sawToolCalls = true
				}
			}
			delta["tool_calls"] = calls
			choice["finish_reason"] = "tool_calls"
			changed = true
		}
	}
	return changed
}

func (p *ParseTags) FlushStreamPayloads(_ Context, rawState any) []map[string]any {
	state, ok := rawState.(*parseTagsStreamState)
	if !ok || state == nil || len(state.choices) == 0 {
		return nil
	}
	indexes := make([]int, 0, len(state.choices))
	for index := range state.choices {
		indexes = append(indexes, index)
	}
	sort.Ints(indexes)
	choices := make([]any, 0, len(indexes))
	for _, index := range indexes {
		choiceState := state.choices[index]
		if choiceState == nil || choiceState.scanner == nil {
			continue
		}
		scanned := choiceState.scanner.flush()
		choiceState.bufferedSince = time.Time{}
		delta := map[string]any{}
		if scanned.content != "" {
			delta["content"] = scanned.content
		}
		if scanned.reasoning != "" {
			delta["reasoning_content"] = scanned.reasoning
		}
		if len(scanned.toolCalls) > 0 {
			delta["tool_calls"] = scanned.toolCalls
		}
		if len(delta) == 0 {
			continue
		}
		choices = append(choices, map[string]any{
			"index": index,
			"delta": delta,
		})
	}
	if len(choices) == 0 {
		return nil
	}
	return []map[string]any{{"choices": choices}}
}

func (p *ParseTags) ResetStreamState(rawState any) {
	resetParseTagsStreamState(rawState)
}

func (p *ParseTags) CloseStreamState(rawState any) {
	resetParseTagsStreamState(rawState)
}

func resetParseTagsStreamState(rawState any) {
	state, ok := rawState.(*parseTagsStreamState)
	if !ok || state == nil {
		return
	}
	state.choices = map[int]*parseTagsChoiceState{}
}

func (s *parseTagsChoiceState) streamBufferExpired(now time.Time, maxAge time.Duration) bool {
	if s == nil || s.scanner == nil || maxAge <= 0 || s.bufferedSince.IsZero() {
		return false
	}
	return s.scanner.streamBufferSize() > 0 && now.Sub(s.bufferedSince) > maxAge
}

func (s *parseTagsChoiceState) flushStreamBuffer() scanResult {
	if s == nil || s.scanner == nil || s.scanner.streamBufferSize() == 0 {
		return scanResult{}
	}
	var out scanResult
	var content strings.Builder
	var reasoning strings.Builder
	s.scanner.flushStreamBuffer(&out, &content, &reasoning)
	out.content = content.String()
	out.reasoning = reasoning.String()
	s.bufferedSince = time.Time{}
	return out
}

func (s *parseTagsChoiceState) updateBufferedSince(now time.Time) {
	if s == nil || s.scanner == nil {
		return
	}
	if s.scanner.streamBufferSize() == 0 {
		s.bufferedSince = time.Time{}
		return
	}
	if s.bufferedSince.IsZero() {
		s.bufferedSince = now
	}
}

type scanResult struct {
	content        string
	reasoning      string
	toolCalls      []any
	toolCallChunks []toolCallChunk
	changed        bool
}

type toolCallChunk struct {
	Name         string
	Arguments    string
	HasArguments bool
	Finished     bool
}

func (p *ParseTags) scan(text string) scanResult {
	scanner := newTagScanner(p.Config)
	scanned := scanner.feed(text)
	endedInsideThink := scanner.mode == "think"
	flushed := scanner.flush()
	result := mergeScanResults(scanned, flushed)
	if p.Config.StartInThink && p.Config.ParseThinking && endedInsideThink && len(result.toolCalls) == 0 && len(result.toolCallChunks) == 0 && strings.TrimSpace(result.content) == "" && strings.TrimSpace(result.reasoning) != "" {
		return scanResult{content: text}
	}
	if !result.changed && result.content == "" {
		result.content = text
	}
	return result
}

type tagScanner struct {
	config            ParseTagsConfig
	mode              string
	buffer            string
	toolBuffer        string
	toolParentMode    string
	streamBufferLimit int
	dropAfterToolCall bool
	toolCallsStarted  bool
	toolStream        *xmlToolCallStreamer
}

func newTagScanner(cfg ParseTagsConfig) *tagScanner {
	return newTagScannerWithStreamBufferLimit(cfg, 0)
}

func newStreamTagScanner(cfg ParseTagsConfig) *tagScanner {
	return newTagScannerWithStreamBufferLimit(cfg, maxStreamBufferBytes(cfg))
}

func newTagScannerWithStreamBufferLimit(cfg ParseTagsConfig, streamBufferLimit int) *tagScanner {
	if cfg.ThinkTag == "" {
		cfg.ThinkTag = "think"
	}
	if cfg.ToolTag == "" {
		cfg.ToolTag = "tool_call"
	}
	if cfg.ToolOpen == "" {
		cfg.ToolOpen = "<" + cfg.ToolTag + ">"
	}
	if cfg.ToolClose == "" {
		cfg.ToolClose = "</" + cfg.ToolTag + ">"
	}
	if cfg.ToolArgFormat == "" {
		cfg.ToolArgFormat = "xml"
	}
	if cfg.ToolParser == "" {
		cfg.ToolParser = cfg.ToolArgFormat
	}
	mode := "text"
	if cfg.StartInThink {
		mode = "think"
	}
	return &tagScanner{config: cfg, mode: mode, streamBufferLimit: streamBufferLimit, dropAfterToolCall: cfg.ParseToolCalls}
}

func maxStreamBufferBytes(cfg ParseTagsConfig) int {
	if cfg.MaxStreamBufferBytes > 0 {
		return cfg.MaxStreamBufferBytes
	}
	return defaultMaxStreamBufferBytes
}

func (s *tagScanner) emitLiteral(out *scanResult, builder *strings.Builder, value string) {
	if value == "" {
		return
	}
	if s.dropAfterToolCall && s.toolCallsStarted {
		out.changed = true
		return
	}
	builder.WriteString(value)
}

func (s *tagScanner) enterTool(parentMode string, out *scanResult) {
	s.buffer = s.buffer[len(s.config.ToolOpen):]
	s.toolBuffer = ""
	s.toolParentMode = parentMode
	s.toolStream = nil
	if s.config.StreamToolCalls && strings.EqualFold(s.config.ToolArgFormat, "xml") {
		s.toolStream = newXMLToolCallStreamer(s.config.ToolParser)
	}
	s.mode = "tool"
	out.changed = true
}

func (s *tagScanner) streamBufferExceeded() bool {
	if s.streamBufferLimit <= 0 {
		return false
	}
	return s.streamBufferSize() > s.streamBufferLimit
}

func (s *tagScanner) streamBufferSize() int {
	if s == nil {
		return 0
	}
	size := len(s.buffer)
	if s.mode == "tool" {
		size += len(s.config.ToolOpen) + len(s.toolBuffer)
	}
	return size
}

func (s *tagScanner) flushStreamBuffer(out *scanResult, content *strings.Builder, reasoning *strings.Builder) {
	switch s.mode {
	case "think":
		s.emitLiteral(out, reasoning, s.buffer)
	case "tool":
		literal := s.config.ToolOpen + s.toolBuffer + s.buffer
		if s.toolParentMode == "think" {
			s.emitLiteral(out, reasoning, literal)
		} else {
			s.emitLiteral(out, content, literal)
		}
	default:
		s.emitLiteral(out, content, s.buffer)
	}
	s.buffer = ""
	s.toolBuffer = ""
	s.toolParentMode = ""
	s.toolStream = nil
	s.mode = "text"
	out.changed = true
}

func (s *tagScanner) feed(text string) scanResult {
	s.buffer += text
	out := scanResult{}
	thinkOpen := "<" + s.config.ThinkTag + ">"
	thinkClose := "</" + s.config.ThinkTag + ">"
	toolOpen := s.config.ToolOpen
	toolClose := s.config.ToolClose
	openTags := make([]string, 0, 2)
	if s.config.ParseThinking {
		openTags = append(openTags, thinkOpen)
	}
	if s.config.ParseToolCalls {
		openTags = append(openTags, toolOpen)
	}
	openTags = append(openTags, s.config.DropTags...)
	var content strings.Builder
	var reasoning strings.Builder
	for s.buffer != "" {
		switch s.mode {
		case "think":
			if s.config.ParseThinking && strings.HasPrefix(s.buffer, thinkClose) {
				s.buffer = s.buffer[len(thinkClose):]
				s.mode = "text"
				out.changed = true
				continue
			}
			if s.config.ParseToolCalls && strings.HasPrefix(s.buffer, toolOpen) {
				s.enterTool("think", &out)
				continue
			}
			if drop := matchingDropTag(s.buffer, s.config.DropTags); drop != "" {
				s.buffer = s.buffer[len(drop):]
				out.changed = true
				continue
			}
			tags := []string{thinkClose}
			if s.config.ParseToolCalls {
				tags = append(tags, toolOpen)
			}
			tags = append(tags, s.config.DropTags...)
			next := firstIndexAny(s.buffer, tags)
			if next < 0 {
				head, tail := splitTailForPrefixes(s.buffer, tags)
				s.emitLiteral(&out, &reasoning, head)
				s.buffer = tail
				if head != "" {
					out.changed = true
				}
				if s.streamBufferExceeded() {
					s.flushStreamBuffer(&out, &content, &reasoning)
				}
				return finishScan(out, content.String(), reasoning.String())
			}
			if next > 0 {
				s.emitLiteral(&out, &reasoning, s.buffer[:next])
				s.buffer = s.buffer[next:]
				out.changed = true
				continue
			}
			s.emitLiteral(&out, &reasoning, s.buffer[:1])
			s.buffer = s.buffer[1:]
			out.changed = true
		case "tool":
			idx := strings.Index(s.buffer, toolClose)
			if idx < 0 {
				head, tail := splitTailForPrefix(s.buffer, toolClose)
				s.toolBuffer += head
				if s.toolStream != nil {
					out.toolCallChunks = append(out.toolCallChunks, s.toolStream.feed(head)...)
				}
				s.buffer = tail
				if head != "" {
					out.changed = true
				}
				if s.config.ToolBufferLimit > 0 && len(s.toolBuffer) > s.config.ToolBufferLimit {
					literal := toolOpen + s.toolBuffer
					if s.toolParentMode == "think" {
						s.emitLiteral(&out, &reasoning, literal)
					} else {
						s.emitLiteral(&out, &content, literal)
					}
					s.toolBuffer = ""
					s.toolStream = nil
					if s.toolParentMode == "" {
						s.toolParentMode = "text"
					}
					s.mode = s.toolParentMode
					continue
				}
				if s.streamBufferExceeded() {
					s.flushStreamBuffer(&out, &content, &reasoning)
				}
				return finishScan(out, content.String(), reasoning.String())
			}
			piece := s.buffer[:idx]
			s.toolBuffer += piece
			if s.toolStream != nil {
				out.toolCallChunks = append(out.toolCallChunks, s.toolStream.feed(piece)...)
			}
			s.buffer = s.buffer[idx+len(toolClose):]
			streamed := s.toolStream != nil && s.toolStream.emitted
			if streamed {
				s.toolCallsStarted = true
				out.changed = true
			} else if call, ok := buildToolCallAny(s.toolBuffer, s.config); ok {
				out.toolCalls = append(out.toolCalls, call)
				s.toolCallsStarted = true
				out.changed = true
			} else if s.toolParentMode == "think" {
				s.emitLiteral(&out, &reasoning, toolOpen+s.toolBuffer+toolClose)
			} else {
				s.emitLiteral(&out, &content, toolOpen+s.toolBuffer+toolClose)
			}
			s.toolBuffer = ""
			s.toolStream = nil
			if s.toolParentMode == "" {
				s.toolParentMode = "text"
			}
			s.mode = s.toolParentMode
		default:
			idx := strings.Index(s.buffer, "<")
			if idx < 0 {
				s.emitLiteral(&out, &content, s.buffer)
				s.buffer = ""
				continue
			}
			if idx > 0 {
				s.emitLiteral(&out, &content, s.buffer[:idx])
				s.buffer = s.buffer[idx:]
			}
			if s.config.ParseThinking && strings.HasPrefix(s.buffer, thinkOpen) {
				s.buffer = s.buffer[len(thinkOpen):]
				s.mode = "think"
				out.changed = true
				continue
			}
			if s.config.ParseToolCalls && strings.HasPrefix(s.buffer, toolOpen) {
				s.enterTool("text", &out)
				continue
			}
			if drop := matchingDropTag(s.buffer, s.config.DropTags); drop != "" {
				s.buffer = s.buffer[len(drop):]
				out.changed = true
				continue
			}
			if hasTagPrefix(openTags, s.buffer) {
				if s.streamBufferExceeded() {
					s.flushStreamBuffer(&out, &content, &reasoning)
				}
				return finishScan(out, content.String(), reasoning.String())
			}
			s.emitLiteral(&out, &content, s.buffer[:1])
			s.buffer = s.buffer[1:]
		}
	}
	return finishScan(out, content.String(), reasoning.String())
}

func (s *tagScanner) flush() scanResult {
	out := scanResult{}
	if s.dropAfterToolCall && s.toolCallsStarted {
		if s.buffer != "" || s.toolBuffer != "" {
			out.changed = true
		}
		s.buffer = ""
		s.toolBuffer = ""
		s.toolStream = nil
		s.mode = "text"
		return out
	}
	switch s.mode {
	case "think":
		out.reasoning = s.buffer
	case "tool":
		literal := s.config.ToolOpen + s.toolBuffer + s.buffer
		if s.toolParentMode == "think" {
			out.reasoning = literal
		} else {
			out.content = literal
		}
	default:
		out.content = s.buffer
	}
	if out.content != "" || out.reasoning != "" {
		out.changed = true
	}
	s.buffer = ""
	s.toolBuffer = ""
	s.toolStream = nil
	s.mode = "text"
	return out
}

func mergeScanResults(parts ...scanResult) scanResult {
	var out scanResult
	for _, part := range parts {
		out.content += part.content
		out.reasoning += part.reasoning
		out.toolCalls = append(out.toolCalls, part.toolCalls...)
		out.toolCallChunks = append(out.toolCallChunks, part.toolCallChunks...)
		out.changed = out.changed || part.changed
	}
	return out
}

func finishScan(result scanResult, content string, reasoning string) scanResult {
	result.content = content
	result.reasoning = reasoning
	return result
}

func extractBlocks(text, open, close string) (string, string, bool) {
	var reasoning strings.Builder
	changed := false
	for {
		start := strings.Index(text, open)
		if start < 0 {
			break
		}
		end := strings.Index(text[start+len(open):], close)
		if end < 0 {
			break
		}
		endAbs := start + len(open) + end
		reasoning.WriteString(text[start+len(open) : endAbs])
		text = text[:start] + text[endAbs+len(close):]
		changed = true
	}
	return text, reasoning.String(), changed
}

func extractToolCalls(text, open, close string) (string, []any, bool) {
	return extractToolCallsWithConfig(text, ParseTagsConfig{ToolOpen: open, ToolClose: close, ToolArgFormat: "xml"})
}

func extractToolCallsWithConfig(text string, cfg ParseTagsConfig) (string, []any, bool) {
	var calls []any
	changed := false
	open := cfg.ToolOpen
	close := cfg.ToolClose
	if open == "" {
		open = "<" + firstNonEmpty(cfg.ToolTag, "tool_call") + ">"
	}
	if close == "" {
		close = "</" + firstNonEmpty(cfg.ToolTag, "tool_call") + ">"
	}
	for {
		start := strings.Index(text, open)
		if start < 0 {
			break
		}
		end := strings.Index(text[start+len(open):], close)
		if end < 0 {
			break
		}
		endAbs := start + len(open) + end
		block := text[start+len(open) : endAbs]
		if call, ok := buildToolCallAny(block, cfg); ok {
			calls = append(calls, call)
			text = text[:start] + text[endAbs+len(close):]
			changed = true
		} else {
			break
		}
	}
	return text, calls, changed
}

func buildToolCallAny(block string, cfg ParseTagsConfig) (map[string]any, bool) {
	var call ToolCall
	var ok bool
	if strings.EqualFold(strings.TrimSpace(cfg.ToolArgFormat), "json") {
		call, ok = ParseJSONToolCall(block, cfg.ToolArgSeparator)
	} else if strings.EqualFold(strings.TrimSpace(cfg.ToolParser), "qwen_xml") {
		call, ok = ParseQwenXMLToolCall(block)
	} else {
		call, ok = ParseXMLishToolCall(block)
	}
	if !ok {
		return nil, false
	}
	return map[string]any{
		"id":   "",
		"type": "function",
		"function": map[string]any{
			"name":      call.Name,
			"arguments": mustJSONString(call.Arguments),
		},
	}, true
}

func ParseJSONToolCall(text string, separator string) (ToolCall, bool) {
	if separator == "" {
		separator = "<|tool_call_argument_begin|>"
	}
	stripped := strings.TrimSpace(text)
	if stripped == "" {
		return ToolCall{}, false
	}
	name := stripped
	rawArgs := "{}"
	if idx := strings.Index(stripped, separator); idx >= 0 {
		name = strings.TrimSpace(stripped[:idx])
		rawArgs = strings.TrimSpace(stripped[idx+len(separator):])
	}
	if name == "" || strings.ContainsAny(name, " \t\r\n") {
		return ToolCall{}, false
	}
	args := map[string]any{}
	if rawArgs != "" {
		if err := json.Unmarshal([]byte(rawArgs), &args); err != nil {
			return ToolCall{}, false
		}
	}
	return ToolCall{Name: name, Arguments: args}, true
}

type xmlToolCallStreamer struct {
	qwen              bool
	phase             string
	buffer            string
	name              string
	firstParameter    bool
	paramTail         string
	paramValueStarted bool
	paramValueMode    string
	disabled          bool
	emitted           bool
}

func newXMLToolCallStreamer(parser string) *xmlToolCallStreamer {
	switch strings.ToLower(strings.TrimSpace(parser)) {
	case "", "xml", "xmlish", "legacy_xml":
		return &xmlToolCallStreamer{phase: "await_function", firstParameter: true}
	case "qwen_xml":
		return &xmlToolCallStreamer{qwen: true, phase: "await_function", firstParameter: true}
	default:
		return nil
	}
}

func (s *xmlToolCallStreamer) feed(text string) []toolCallChunk {
	if s == nil || text == "" || s.disabled {
		return nil
	}
	s.buffer += text
	var chunks []toolCallChunk
	for s.buffer != "" && !s.disabled {
		switch s.phase {
		case "await_function":
			match := functionOpenRE.FindStringSubmatchIndex(s.buffer)
			if match == nil {
				trimmed := strings.TrimSpace(s.buffer)
				if strings.Contains(s.buffer, "<function") && !strings.Contains(s.buffer, ">") {
					return chunks
				}
				if trimmed != "" && !strings.HasPrefix("<function", trimmed) {
					s.disabled = true
				}
				return chunks
			}
			s.name = s.buffer[match[2]:match[3]]
			s.buffer = s.buffer[match[1]:]
			s.phase = "between_parameters"
			if !s.qwen {
				chunks = append(chunks, toolCallChunk{Name: s.name, Arguments: "{", HasArguments: true})
				s.emitted = true
			}
		case "between_parameters":
			stripped := strings.TrimLeft(s.buffer, " \t\r\n")
			if stripped == "" {
				s.buffer = ""
				return chunks
			}
			if s.qwen {
				if duplicate := functionOpenRE.FindStringSubmatchIndex(stripped); duplicate != nil {
					duplicateName := stripped[duplicate[2]:duplicate[3]]
					if duplicateName != s.name {
						s.disabled = true
						return chunks
					}
					s.buffer = stripped[duplicate[1]:]
					continue
				}
			}
			if strings.HasPrefix(stripped, "</function>") {
				s.buffer = stripped[len("</function>"):]
				if s.qwen && !s.emitted && s.name != "" {
					chunks = append(chunks, toolCallChunk{Name: s.name, Arguments: "{}", HasArguments: true, Finished: true})
					s.emitted = true
				} else if s.emitted {
					chunks = append(chunks, toolCallChunk{Arguments: "}", HasArguments: true, Finished: true})
				}
				s.phase = "done"
				continue
			}
			if !strings.HasPrefix(stripped, "<parameter") {
				if strings.HasPrefix("<parameter", stripped) || strings.HasPrefix("</function>", stripped) {
					s.buffer = stripped
					return chunks
				}
				s.disabled = true
				return chunks
			}
			match := parameterOpenRE.FindStringSubmatchIndex(s.buffer)
			if match == nil {
				if !strings.Contains(s.buffer, ">") {
					return chunks
				}
				s.disabled = true
				return chunks
			}
			if s.qwen && !s.emitted && s.name != "" {
				chunks = append(chunks, toolCallChunk{Name: s.name, Arguments: "{", HasArguments: true})
				s.emitted = true
			}
			key := strings.TrimSpace(s.buffer[match[2]:match[3]])
			prefix := ""
			if !s.firstParameter {
				prefix = ","
			}
			chunks = append(chunks, toolCallChunk{Arguments: prefix + jsonString(key) + ":", HasArguments: true})
			s.firstParameter = false
			s.buffer = s.buffer[match[1]:]
			s.paramTail = ""
			s.paramValueStarted = false
			s.paramValueMode = ""
			s.phase = "parameter_value"
		case "parameter_value":
			const close = "</parameter>"
			closeIdx := strings.Index(s.buffer, close)
			if closeIdx < 0 {
				head, tail := splitTailForPrefix(s.buffer, close)
				chunks = append(chunks, s.appendParameterText(head)...)
				s.buffer = tail
				return chunks
			}
			chunks = append(chunks, s.appendParameterText(s.buffer[:closeIdx])...)
			finalValue := strings.TrimRight(s.paramTail, " \t\r\n")
			if s.paramValueMode == "json" {
				valueText := strings.TrimSpace(finalValue)
				chunks = append(chunks, toolCallChunk{Arguments: normalizeJSONArgumentFragment(valueText), HasArguments: true})
			} else if !s.paramValueStarted {
				chunks = append(chunks, toolCallChunk{Arguments: "\"", HasArguments: true})
				chunks = append(chunks, toolCallChunk{Arguments: jsonStringFragment(finalValue) + "\"", HasArguments: true})
			} else {
				chunks = append(chunks, toolCallChunk{Arguments: jsonStringFragment(finalValue) + "\"", HasArguments: true})
			}
			s.paramTail = ""
			s.buffer = s.buffer[closeIdx+len(close):]
			s.phase = "between_parameters"
		case "done":
			s.buffer = ""
			return chunks
		default:
			s.disabled = true
			return chunks
		}
	}
	return chunks
}

func (s *xmlToolCallStreamer) appendParameterText(text string) []toolCallChunk {
	if text == "" {
		return nil
	}
	if s.paramValueMode == "json" {
		s.paramTail += text
		return nil
	}
	if !s.paramValueStarted {
		text = s.paramTail + text
		s.paramTail = ""
		stripped := strings.TrimLeft(text, " \t\r\n")
		if stripped == "" {
			s.paramTail = text
			return nil
		}
		if strings.HasPrefix(stripped, "{") || strings.HasPrefix(stripped, "[") {
			s.paramValueMode = "json"
			s.paramValueStarted = true
			s.paramTail = text
			return nil
		}
		text = stripped
		s.paramValueStarted = true
		return append([]toolCallChunk{{Arguments: "\"", HasArguments: true}}, s.appendParameterText(text)...)
	}
	combined := s.paramTail + text
	head, tail := splitRuneTail(combined, 32)
	s.paramTail = tail
	if head == "" {
		return nil
	}
	return []toolCallChunk{{Arguments: jsonStringFragment(head), HasArguments: true}}
}

func splitRuneTail(text string, tailLen int) (string, string) {
	runes := []rune(text)
	if len(runes) <= tailLen {
		return "", text
	}
	return string(runes[:len(runes)-tailLen]), string(runes[len(runes)-tailLen:])
}

func jsonString(value string) string {
	buf, err := json.Marshal(value)
	if err != nil {
		return `""`
	}
	return string(buf)
}

func jsonStringFragment(value string) string {
	encoded := jsonString(value)
	if len(encoded) < 2 {
		return ""
	}
	return encoded[1 : len(encoded)-1]
}

func normalizeJSONArgumentFragment(value string) string {
	if value == "" {
		return "null"
	}
	var decoded any
	if err := json.Unmarshal([]byte(value), &decoded); err != nil {
		return `"` + jsonStringFragment(value) + `"`
	}
	buf, err := json.Marshal(decoded)
	if err != nil {
		return `"` + jsonStringFragment(value) + `"`
	}
	return string(buf)
}

func dropLiteralTags(text string, tags []string) string {
	for _, tag := range tags {
		if tag != "" {
			text = strings.ReplaceAll(text, tag, "")
		}
	}
	return text
}

func matchingDropTag(text string, tags []string) string {
	for _, tag := range tags {
		if tag != "" && strings.HasPrefix(text, tag) {
			return tag
		}
	}
	return ""
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func choiceIndex(choice map[string]any, fallback int) int {
	switch typed := choice["index"].(type) {
	case int:
		return typed
	case int64:
		return int(typed)
	case float64:
		return int(typed)
	default:
		return fallback
	}
}

func streamToolCallID(choiceIndex int, toolIndex int) string {
	return fmt.Sprintf("call_%d_%d", choiceIndex, toolIndex)
}

func firstIndexAny(text string, needles []string) int {
	best := -1
	for _, needle := range needles {
		if needle == "" {
			continue
		}
		idx := strings.Index(text, needle)
		if idx >= 0 && (best < 0 || idx < best) {
			best = idx
		}
	}
	return best
}

func hasTagPrefix(tags []string, value string) bool {
	if value == "" {
		return false
	}
	for _, tag := range tags {
		if strings.HasPrefix(tag, value) {
			return true
		}
	}
	return false
}

func splitTailForPrefixes(text string, prefixes []string) (string, string) {
	keep := 0
	for i := 1; i <= len(text); i++ {
		tail := text[len(text)-i:]
		for _, prefix := range prefixes {
			if strings.HasPrefix(prefix, tail) && i > keep {
				keep = i
			}
		}
	}
	if keep == 0 {
		return text, ""
	}
	return text[:len(text)-keep], text[len(text)-keep:]
}

func splitTailForPrefix(text string, prefix string) (string, string) {
	return splitTailForPrefixes(text, []string{prefix})
}

type ToolCall struct {
	Name      string
	Arguments map[string]any
}

var argPairRE = regexp.MustCompile(`(?s)<arg_key>(.*?)</arg_key>\s*<arg_value>(.*?)</arg_value>`)
var parameterRE = regexp.MustCompile(`(?s)<parameter\s*=\s*([^>\s]+)\s*>(.*?)</parameter>`)
var legacyFunctionRE = regexp.MustCompile(`(?s)^<function\s*=\s*([A-Za-z_][\w.-]*)\s*>(.*?)</function>$`)
var legacyFunctionTagRE = regexp.MustCompile(`(?s)^<function>(.*?)</function>(.*)$`)
var functionOpenRE = regexp.MustCompile(`(?s)^\s*<function\s*=\s*([A-Za-z_][\w.-]*)\s*>`)
var functionOpenAnywhereRE = regexp.MustCompile(`(?s)<function\s*=\s*([A-Za-z_][\w.-]*)\s*>`)
var parameterOpenRE = regexp.MustCompile(`(?s)^\s*<parameter\s*=\s*([^>\s]+)\s*>`)

func ParseXMLishToolCall(text string) (ToolCall, bool) {
	stripped := strings.TrimSpace(text)
	if stripped == "" {
		return ToolCall{}, false
	}
	if match := legacyFunctionRE.FindStringSubmatch(stripped); match != nil {
		args, ok := parseArgs(match[2])
		if !ok {
			return ToolCall{}, false
		}
		return ToolCall{Name: strings.TrimSpace(match[1]), Arguments: args}, true
	}
	if match := legacyFunctionTagRE.FindStringSubmatch(stripped); match != nil {
		name := strings.TrimSpace(match[1])
		if name == "" || strings.ContainsAny(name, " \t\r\n") {
			return ToolCall{}, false
		}
		args, ok := parseArgs(match[2])
		if !ok {
			return ToolCall{}, false
		}
		return ToolCall{Name: name, Arguments: args}, true
	}
	argStart := strings.Index(stripped, "<arg_key>")
	paramStart := strings.Index(stripped, "<parameter")
	start := -1
	if argStart >= 0 {
		start = argStart
	}
	if paramStart >= 0 && (start < 0 || paramStart < start) {
		start = paramStart
	}
	if start < 0 {
		if strings.ContainsAny(stripped, " \t\r\n") {
			return ToolCall{}, false
		}
		return ToolCall{Name: stripped, Arguments: map[string]any{}}, true
	}
	name := strings.TrimSpace(stripped[:start])
	if name == "" || strings.ContainsAny(name, " \t\r\n") {
		return ToolCall{}, false
	}
	args, ok := parseArgs(stripped[start:])
	if !ok {
		return ToolCall{}, false
	}
	return ToolCall{Name: name, Arguments: args}, true
}

func ParseQwenXMLToolCall(text string) (ToolCall, bool) {
	stripped := strings.TrimSpace(text)
	if stripped == "" {
		return ToolCall{}, false
	}
	if call, ok := parseQwenJSONToolCall(stripped); ok {
		return call, true
	}
	if call, ok := ParseXMLishToolCall(text); ok {
		return call, true
	}
	loc := functionOpenAnywhereRE.FindStringSubmatchIndex(stripped)
	if loc == nil {
		return ToolCall{}, false
	}
	name := strings.TrimSpace(stripped[loc[2]:loc[3]])
	if name == "" || strings.ContainsAny(name, " \t\r\n") {
		return ToolCall{}, false
	}
	bodyStart := loc[1]
	bodyEnd := strings.LastIndex(stripped, "</function>")
	if bodyEnd < bodyStart {
		bodyEnd = len(stripped)
	}
	return parseQwenFunctionBody(name, stripped[bodyStart:bodyEnd])
}

func parseQwenJSONToolCall(stripped string) (ToolCall, bool) {
	var payload map[string]any
	if err := json.Unmarshal([]byte(stripped), &payload); err != nil {
		return ToolCall{}, false
	}
	name, _ := payload["name"].(string)
	if name == "" {
		if fn, ok := payload["function"].(map[string]any); ok {
			name, _ = fn["name"].(string)
		}
	}
	name = strings.TrimSpace(name)
	if name == "" || strings.ContainsAny(name, " \t\r\n") {
		return ToolCall{}, false
	}
	args := payload["parameters"]
	if args == nil {
		args = payload["arguments"]
	}
	if args == nil {
		if fn, ok := payload["function"].(map[string]any); ok {
			args = fn["parameters"]
			if args == nil {
				args = fn["arguments"]
			}
		}
	}
	if args == nil {
		return ToolCall{Name: name, Arguments: map[string]any{}}, true
	}
	if raw, ok := args.(string); ok {
		var parsed map[string]any
		if err := json.Unmarshal([]byte(raw), &parsed); err == nil {
			return ToolCall{Name: name, Arguments: parsed}, true
		}
		return ToolCall{}, false
	}
	argsMap, ok := args.(map[string]any)
	if !ok {
		return ToolCall{}, false
	}
	return ToolCall{Name: name, Arguments: argsMap}, true
}

func parseQwenFunctionBody(functionName string, body string) (ToolCall, bool) {
	body = strings.TrimSpace(body)
	for {
		loc := functionOpenRE.FindStringSubmatchIndex(body)
		if loc == nil {
			break
		}
		duplicateName := body[loc[2]:loc[3]]
		if duplicateName != functionName {
			break
		}
		body = strings.TrimSpace(body[loc[1]:])
	}
	if body == "" {
		return ToolCall{Name: functionName, Arguments: map[string]any{}}, true
	}
	args, ok := parseArgs(body)
	if !ok {
		return ToolCall{}, false
	}
	return ToolCall{Name: functionName, Arguments: args}, true
}

func parseArgs(text string) (map[string]any, bool) {
	args := map[string]any{}
	if strings.TrimSpace(text) == "" {
		return args, true
	}
	if matches := argPairRE.FindAllStringSubmatch(text, -1); len(matches) > 0 {
		covered := argPairRE.ReplaceAllString(text, "")
		if strings.TrimSpace(covered) != "" {
			return nil, false
		}
		for _, match := range matches {
			args[strings.TrimSpace(match[1])] = maybeJSON(strings.TrimSpace(match[2]))
		}
		return args, true
	}
	if matches := parameterRE.FindAllStringSubmatch(text, -1); len(matches) > 0 {
		covered := parameterRE.ReplaceAllString(text, "")
		if strings.TrimSpace(covered) != "" {
			return nil, false
		}
		for _, match := range matches {
			args[strings.TrimSpace(match[1])] = maybeJSON(strings.TrimSpace(match[2]))
		}
		return args, true
	}
	return nil, false
}

func maybeJSON(text string) any {
	var value any
	if err := json.Unmarshal([]byte(text), &value); err == nil {
		return value
	}
	return text
}

func mustJSONString(value any) string {
	buf, err := json.Marshal(value)
	if err != nil {
		return "{}"
	}
	return string(buf)
}

var tagOpenRE = regexp.MustCompile(`<([A-Za-z][\w:-]*)>`)
var tagCloseRE = regexp.MustCompile(`</([A-Za-z][\w:-]*)>`)
var jinjaExprRE = regexp.MustCompile(`(?s)\{\{(.*?)\}\}`)
var stringLiteralRE = regexp.MustCompile(`(?s)(['"])((?:\\.|.)*?)['"]`)
var qwenXMLFunctionMarkerRE = regexp.MustCompile(`<function\s*=`)
var qwenXMLParameterMarkerRE = regexp.MustCompile(`<parameter\s*=`)

func detectThinkTag(text string) string {
	ignore := map[string]bool{"tool_call": true, "function_call": true, "tool": true, "arg_key": true, "arg_value": true}
	for _, tag := range []string{"think", "analysis", "reasoning", "thought"} {
		if strings.Contains(text, "<"+tag+">") && strings.Contains(text, "</"+tag+">") {
			return tag
		}
	}
	var exprLiterals []string
	for _, match := range jinjaExprRE.FindAllStringSubmatch(text, -1) {
		if !strings.Contains(match[1], "reasoning_content") {
			continue
		}
		for _, literal := range stringLiteralRE.FindAllStringSubmatch(match[1], -1) {
			exprLiterals = append(exprLiterals, literal[2])
		}
	}
	if len(exprLiterals) > 0 {
		if tag := pickTagFromText(strings.Join(exprLiterals, " "), ignore); tag != "" {
			return tag
		}
	}
	return pickTagFromText(text, ignore)
}

func detectToolTag(text string) string {
	for _, tag := range []string{"tool_call", "function_call", "tool"} {
		if strings.Contains(text, "<"+tag+">") && strings.Contains(text, "</"+tag+">") {
			return tag
		}
	}
	return ""
}

func detectQwenXMLToolCallTemplate(text string) bool {
	return strings.Contains(text, "<tool_call>") &&
		strings.Contains(text, "</tool_call>") &&
		qwenXMLFunctionMarkerRE.MatchString(text) &&
		qwenXMLParameterMarkerRE.MatchString(text)
}

func pickTagFromText(text string, ignore map[string]bool) string {
	openCounts := map[string]int{}
	closeCounts := map[string]int{}
	for _, match := range tagOpenRE.FindAllStringSubmatch(text, -1) {
		openCounts[match[1]]++
	}
	for _, match := range tagCloseRE.FindAllStringSubmatch(text, -1) {
		closeCounts[match[1]]++
	}
	bestTag := ""
	bestCount := 0
	for tag, openCount := range openCounts {
		if ignore[tag] || closeCounts[tag] == 0 {
			continue
		}
		count := openCount
		if closeCounts[tag] < count {
			count = closeCounts[tag]
		}
		if count > bestCount {
			bestTag = tag
			bestCount = count
		}
	}
	return bestTag
}

type ReasoningSwap struct {
	Mode           string
	ThinkTag       string
	IncludeNewline bool
}

func NewReasoningSwap(mode string) *ReasoningSwap {
	if mode == "" {
		mode = "reasoning_to_content"
	}
	return &ReasoningSwap{Mode: mode, ThinkTag: "think", IncludeNewline: true}
}

func (r *ReasoningSwap) Name() string { return "swap_reasoning_content" }

func (r *ReasoningSwap) TransformPayload(payload map[string]any, _ Context) bool {
	changed := false
	forEachChoiceMessage(payload, "message", func(message map[string]any) {
		if r.swapMessage(message) {
			changed = true
		}
	})
	return changed
}

func (r *ReasoningSwap) TransformStreamPayload(payload map[string]any, _ Context) bool {
	changed := false
	forEachChoiceMessage(payload, "delta", func(delta map[string]any) {
		if r.swapMessage(delta) {
			changed = true
		}
	})
	return changed
}

func (r *ReasoningSwap) swapMessage(message map[string]any) bool {
	switch r.Mode {
	case "content_to_reasoning":
		content, _ := message["content"].(string)
		if content == "" {
			return false
		}
		text, reasoning, ok := extractBlocks(content, "<"+r.ThinkTag+">", "</"+r.ThinkTag+">")
		if !ok {
			return false
		}
		message["content"] = text
		message["reasoning_content"] = reasoning
		return true
	default:
		reasoning, _ := message["reasoning_content"].(string)
		if reasoning == "" {
			return false
		}
		content, _ := message["content"].(string)
		sep := ""
		if r.IncludeNewline && content != "" {
			sep = "\n"
		}
		message["content"] = "<" + r.ThinkTag + ">" + reasoning + "</" + r.ThinkTag + ">" + sep + content
		delete(message, "reasoning_content")
		return true
	}
}

func forEachChoiceMessage(payload map[string]any, key string, fn func(map[string]any)) {
	forEachChoiceMessageWithChoice(payload, key, func(_ map[string]any, message map[string]any) {
		fn(message)
	})
}

func forEachChoiceMessageWithChoice(payload map[string]any, key string, fn func(map[string]any, map[string]any)) {
	choices, ok := payload["choices"].([]any)
	if !ok {
		return
	}
	for _, rawChoice := range choices {
		choice, ok := rawChoice.(map[string]any)
		if !ok {
			continue
		}
		message, ok := choice[key].(map[string]any)
		if ok {
			fn(choice, message)
		}
	}
}

func RewriteSSEChunk(chunk []byte, pipeline Pipeline, ctx Context) []byte {
	return RewriteSSEChunkWithState(chunk, pipeline, ctx, nil)
}

func FlushSSEChunkWithState(pipeline Pipeline, ctx Context, state *StreamState) []byte {
	if len(pipeline.Modules) == 0 {
		return nil
	}
	payloads := pipeline.FlushSSEPayloadsWithState(ctx, state)
	if len(payloads) == 0 {
		return nil
	}
	lines := marshalSSEPayloadLines(payloads)
	if len(lines) == 0 {
		return nil
	}
	return []byte(strings.Join(lines, "\n\n") + "\n\n")
}

func RewriteSSEChunkWithState(chunk []byte, pipeline Pipeline, ctx Context, state *StreamState) []byte {
	if len(chunk) == 0 || len(pipeline.Modules) == 0 {
		return chunk
	}
	parts := bytes.SplitAfter(chunk, []byte("\n\n"))
	changed := false
	for i, part := range parts {
		text := string(part)
		lines := strings.Split(text, "\n")
		for j, line := range lines {
			if !strings.HasPrefix(line, "data:") {
				continue
			}
			raw := strings.TrimLeft(strings.TrimPrefix(line, "data:"), " ")
			if strings.TrimSpace(raw) == "[DONE]" {
				if payloads := pipeline.FlushSSEPayloadsWithState(ctx, state); len(payloads) > 0 {
					lines[j] = strings.Join(append(marshalSSEPayloadLines(payloads), line), "\n\n")
					changed = true
				}
				continue
			}
			if rewritten, ok := pipeline.TransformSSEEventWithState(raw, ctx, state); ok {
				lines[j] = "data: " + rewritten
				changed = true
			}
		}
		parts[i] = []byte(strings.Join(lines, "\n"))
	}
	if !changed {
		return chunk
	}
	return bytes.Join(parts, nil)
}

func marshalSSEPayloadLines(payloads []map[string]any) []string {
	lines := make([]string, 0, len(payloads))
	for _, payload := range payloads {
		if len(payload) == 0 {
			continue
		}
		buf, err := json.Marshal(payload)
		if err != nil {
			continue
		}
		lines = append(lines, "data: "+string(buf))
	}
	return lines
}
