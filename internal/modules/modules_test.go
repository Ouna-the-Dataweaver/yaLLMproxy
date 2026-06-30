package modules

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestNormalizeReasoningDetailsPayload(t *testing.T) {
	payload := map[string]any{
		"choices": []any{
			map[string]any{
				"message": map[string]any{
					"content": "hello",
					"reasoning_details": []any{
						map[string]any{"text": "one "},
						map[string]any{"reasoning": map[string]any{"text": "two"}},
					},
				},
			},
		},
	}
	if !NormalizeReasoningDetailsPayload(payload) {
		t.Fatal("expected payload to change")
	}
	msg := payload["choices"].([]any)[0].(map[string]any)["message"].(map[string]any)
	if got := msg["reasoning_content"]; got != "one two" {
		t.Fatalf("reasoning_content = %v", got)
	}
}

func TestParseTagsNonStreamReasoningAndToolCalls(t *testing.T) {
	payload := chatPayload(`before <think>hidden</think> after <tool_call>search<arg_key>query</arg_key><arg_value>"cats"</arg_value></tool_call>`)
	parser := NewParseTags(ParseTagsConfig{ParseThinking: true, ParseToolCalls: true})
	if !parser.TransformPayload(payload, Context{Path: "/v1/chat/completions"}) {
		t.Fatal("expected transform")
	}
	msg := firstMessage(payload, "message")
	if got := msg["content"]; got != "before  after " {
		t.Fatalf("content = %q", got)
	}
	if got := msg["reasoning_content"]; got != "hidden" {
		t.Fatalf("reasoning = %q", got)
	}
	calls := msg["tool_calls"].([]any)
	fn := calls[0].(map[string]any)["function"].(map[string]any)
	if fn["name"] != "search" {
		t.Fatalf("tool name = %#v", fn)
	}
	if fn["arguments"] != `{"query":"cats"}` {
		t.Fatalf("arguments = %s", fn["arguments"])
	}
	choice := payload["choices"].([]any)[0].(map[string]any)
	if choice["finish_reason"] != "tool_calls" {
		t.Fatalf("finish_reason = %#v", choice["finish_reason"])
	}
}

func TestReasoningSwap(t *testing.T) {
	payload := chatPayload("visible")
	msg := firstMessage(payload, "message")
	msg["reasoning_content"] = "hidden"
	swap := NewReasoningSwap("reasoning_to_content")
	if !swap.TransformPayload(payload, Context{}) {
		t.Fatal("expected transform")
	}
	if got := msg["content"]; got != "<think>hidden</think>\nvisible" {
		t.Fatalf("content = %q", got)
	}
	if _, ok := msg["reasoning_content"]; ok {
		t.Fatalf("reasoning_content was not removed: %#v", msg)
	}

	swapBack := NewReasoningSwap("content_to_reasoning")
	if !swapBack.TransformPayload(payload, Context{}) {
		t.Fatal("expected reverse transform")
	}
	if got := msg["reasoning_content"]; got != "hidden" {
		t.Fatalf("reasoning_content = %q", got)
	}
}

func TestRewriteSSEChunk(t *testing.T) {
	event := map[string]any{
		"choices": []any{
			map[string]any{
				"delta": map[string]any{"content": "<think>x</think>y"},
			},
		},
	}
	buf, _ := json.Marshal(event)
	chunk := append([]byte("data: "), append(buf, []byte("\n\n")...)...)
	pipeline := Pipeline{Modules: []ResponseModule{NewParseTags(ParseTagsConfig{ParseThinking: true})}}
	out := RewriteSSEChunk(chunk, pipeline, Context{Path: "/v1/chat/completions", IsStream: true})
	var decoded map[string]any
	if err := json.Unmarshal(out[len("data: "):len(out)-2], &decoded); err != nil {
		t.Fatalf("rewritten chunk is not json: %s: %q", err, out)
	}
	delta := firstMessage(decoded, "delta")
	if delta["reasoning_content"] != "x" || delta["content"] != "y" {
		t.Fatalf("delta = %#v", delta)
	}
}

func TestRewriteSSEChunkWithStateParsesFragmentedReasoning(t *testing.T) {
	pipeline := Pipeline{Modules: []ResponseModule{NewParseTags(ParseTagsConfig{ParseThinking: true})}}
	state := pipeline.NewStreamState()
	ctx := Context{Path: "/v1/chat/completions", IsStream: true}
	chunks := [][]byte{
		sseChunk(map[string]any{"choices": []any{map[string]any{"index": 0, "delta": map[string]any{"content": "<thi"}}}}),
		sseChunk(map[string]any{"choices": []any{map[string]any{"index": 0, "delta": map[string]any{"content": "nk>Rea"}}}}),
		sseChunk(map[string]any{"choices": []any{map[string]any{"index": 0, "delta": map[string]any{"content": "son</think>Answer"}}}}),
	}
	var reasoning string
	var content string
	for _, chunk := range chunks {
		out := RewriteSSEChunkWithState(chunk, pipeline, ctx, state)
		payload := decodeSSEPayload(t, out)
		delta := firstMessage(payload, "delta")
		if part, ok := delta["reasoning_content"].(string); ok {
			reasoning += part
		}
		if part, ok := delta["content"].(string); ok {
			content += part
		}
	}
	if reasoning != "Reason" {
		t.Fatalf("reasoning = %q", reasoning)
	}
	if content != "Answer" {
		t.Fatalf("content = %q", content)
	}
}

func TestRewriteSSEChunkWithStateParsesFragmentedToolCall(t *testing.T) {
	pipeline := Pipeline{Modules: []ResponseModule{NewParseTags(ParseTagsConfig{ParseToolCalls: true})}}
	state := pipeline.NewStreamState()
	ctx := Context{Path: "/v1/chat/completions", IsStream: true}
	chunks := [][]byte{
		sseChunk(map[string]any{"choices": []any{map[string]any{"index": 0, "delta": map[string]any{"content": "<tool_call>sea"}}}}),
		sseChunk(map[string]any{"choices": []any{map[string]any{"index": 0, "delta": map[string]any{"content": "rch<arg_key>query</arg_key>"}}}}),
		sseChunk(map[string]any{"choices": []any{map[string]any{"index": 0, "delta": map[string]any{"content": "<arg_value>\"cats\"</arg_value></tool_call>"}}}}),
	}
	var calls []any
	var finishReason any
	for _, chunk := range chunks {
		out := RewriteSSEChunkWithState(chunk, pipeline, ctx, state)
		payload := decodeSSEPayload(t, out)
		choice := payload["choices"].([]any)[0].(map[string]any)
		if choice["finish_reason"] != nil {
			finishReason = choice["finish_reason"]
		}
		delta := choice["delta"].(map[string]any)
		if deltaCalls, ok := delta["tool_calls"].([]any); ok {
			calls = append(calls, deltaCalls...)
		}
		if content, ok := delta["content"].(string); ok && content != "" {
			t.Fatalf("unexpected content fragment = %q", content)
		}
	}
	if len(calls) != 1 {
		t.Fatalf("calls = %#v", calls)
	}
	fn := calls[0].(map[string]any)["function"].(map[string]any)
	if fn["name"] != "search" || fn["arguments"] != `{"query":"cats"}` {
		t.Fatalf("function = %#v", fn)
	}
	if finishReason != "tool_calls" {
		t.Fatalf("finish_reason = %#v", finishReason)
	}
}

func TestParseTagsToolInsideThinkDropsTrailingText(t *testing.T) {
	payload := chatPayload(`<think>before<tool_call>lookup<arg_key>q</arg_key><arg_value>x</arg_value></tool_call>after</think>Answer`)
	parser := NewParseTags(ParseTagsConfig{ParseThinking: true, ParseToolCalls: true})
	if !parser.TransformPayload(payload, Context{}) {
		t.Fatal("expected transform")
	}
	msg := firstMessage(payload, "message")
	if got := msg["reasoning_content"]; got != "before" {
		t.Fatalf("reasoning_content = %#v", got)
	}
	if got := msg["content"]; got != nil {
		t.Fatalf("content = %#v", got)
	}
	calls := msg["tool_calls"].([]any)
	fn := calls[0].(map[string]any)["function"].(map[string]any)
	if fn["name"] != "lookup" || fn["arguments"] != `{"q":"x"}` {
		t.Fatalf("function = %#v", fn)
	}
	choice := payload["choices"].([]any)[0].(map[string]any)
	if choice["finish_reason"] != "tool_calls" {
		t.Fatalf("finish_reason = %#v", choice["finish_reason"])
	}
}

func TestRewriteSSEChunkWithStateToolBufferLimitFallsBackToContent(t *testing.T) {
	pipeline := Pipeline{Modules: []ResponseModule{NewParseTags(ParseTagsConfig{ParseToolCalls: true, ToolBufferLimit: 5})}}
	state := pipeline.NewStreamState()
	ctx := Context{Path: "/v1/chat/completions", IsStream: true}
	chunk := sseChunk(map[string]any{"choices": []any{map[string]any{"index": 0, "delta": map[string]any{"content": "<tool_call>abcdef"}}}})
	out := RewriteSSEChunkWithState(chunk, pipeline, ctx, state)
	payload := decodeSSEPayload(t, out)
	delta := firstMessage(payload, "delta")
	if got := delta["content"]; got != "<tool_call>abcdef" {
		t.Fatalf("content = %#v", got)
	}
	if _, ok := delta["tool_calls"]; ok {
		t.Fatalf("unexpected tool calls: %#v", delta["tool_calls"])
	}
}

func TestParseQwenXMLRepairsDuplicateFunction(t *testing.T) {
	payload := chatPayload("<tool_call>\n<function=law_searcher>\n<function=law_searcher>\n<parameter=description>\nплатье\n</parameter>\n</function>\n</tool_call>")
	parser := NewParseTags(ParseTagsConfig{
		ParseThinking:  true,
		ParseToolCalls: true,
		ToolParser:     "qwen_xml",
	})
	if !parser.TransformPayload(payload, Context{}) {
		t.Fatal("expected transform")
	}
	msg := firstMessage(payload, "message")
	if got := msg["content"]; got != nil {
		t.Fatalf("content = %#v", got)
	}
	calls := msg["tool_calls"].([]any)
	fn := calls[0].(map[string]any)["function"].(map[string]any)
	if fn["name"] != "law_searcher" || fn["arguments"] != `{"description":"платье"}` {
		t.Fatalf("function = %#v", fn)
	}
}

func TestRewriteSSEChunkWithStateStreamsQwenXMLArguments(t *testing.T) {
	pipeline := Pipeline{Modules: []ResponseModule{NewParseTags(ParseTagsConfig{
		ParseToolCalls:  true,
		StreamToolCalls: true,
		ToolParser:      "qwen_xml",
	})}}
	state := pipeline.NewStreamState()
	ctx := Context{Path: "/v1/chat/completions", IsStream: true}
	chunks := [][]byte{
		sseChunk(map[string]any{"choices": []any{map[string]any{"index": 0, "delta": map[string]any{"content": "<tool_call>\n<function=write_report>\n<parameter=text>\n"}}}}),
		sseChunk(map[string]any{"choices": []any{map[string]any{"index": 0, "delta": map[string]any{"content": "Прокси должен отдавать длинные аргументы инструмента небольшими "}}}}),
		sseChunk(map[string]any{"choices": []any{map[string]any{"index": 0, "delta": map[string]any{"content": "фрагментами во время генерации.\n</parameter>\n</function>\n</tool_call>"}}}}),
	}
	var name string
	var arguments string
	var toolEvents int
	for _, chunk := range chunks {
		out := RewriteSSEChunkWithState(chunk, pipeline, ctx, state)
		payload := decodeSSEPayload(t, out)
		choice := payload["choices"].([]any)[0].(map[string]any)
		delta := choice["delta"].(map[string]any)
		for _, rawCall := range delta["tool_calls"].([]any) {
			toolEvents++
			call := rawCall.(map[string]any)
			fn := call["function"].(map[string]any)
			if fnName, ok := fn["name"].(string); ok && fnName != "" {
				name = fnName
			}
			if fragment, ok := fn["arguments"].(string); ok {
				arguments += fragment
			}
		}
	}
	if toolEvents <= 1 {
		t.Fatalf("expected incremental tool deltas, saw %d", toolEvents)
	}
	if name != "write_report" {
		t.Fatalf("name = %q", name)
	}
	var parsed map[string]string
	if err := json.Unmarshal([]byte(arguments), &parsed); err != nil {
		t.Fatalf("arguments are not valid JSON %q: %v", arguments, err)
	}
	if parsed["text"] != "Прокси должен отдавать длинные аргументы инструмента небольшими фрагментами во время генерации." {
		t.Fatalf("arguments = %#v", parsed)
	}
}

func TestParseXMLishToolCall(t *testing.T) {
	call, ok := ParseXMLishToolCall(`<function=get_weather><parameter=city>Paris</parameter><parameter=units>"metric"</parameter></function>`)
	if !ok {
		t.Fatal("expected tool call")
	}
	if call.Name != "get_weather" {
		t.Fatalf("name = %s", call.Name)
	}
	if call.Arguments["city"] != "Paris" || call.Arguments["units"] != "metric" {
		t.Fatalf("args = %#v", call.Arguments)
	}
}

func TestDeriveParseTagsConfigFromTemplateText(t *testing.T) {
	cfg, ok := DeriveParseTagsConfigFromTemplateText(`{{ '<analysis>' + reasoning_content + '</analysis>' }} <function_call>x</function_call>`)
	if !ok {
		t.Fatal("expected derived config")
	}
	if cfg.ThinkTag != "analysis" {
		t.Fatalf("think tag = %q", cfg.ThinkTag)
	}
	if cfg.ToolTag != "function_call" || cfg.ToolOpen != "<function_call>" || cfg.ToolClose != "</function_call>" {
		t.Fatalf("tool config = %#v", cfg)
	}

	k2, ok := DeriveParseTagsConfigFromTemplateText(`<|tool_calls_section_begin|><|tool_call_begin|>name<|tool_call_argument_begin|>{}<|tool_call_end|><|tool_calls_section_end|>`)
	if !ok {
		t.Fatal("expected k2 config")
	}
	if k2.ToolArgFormat != "json" || k2.ToolOpen != "<|tool_call_begin|>" || k2.ToolClose != "<|tool_call_end|>" {
		t.Fatalf("k2 config = %#v", k2)
	}
	if len(k2.DropTags) != 2 {
		t.Fatalf("drop tags = %#v", k2.DropTags)
	}
}

func TestAnalyzeTemplateTextRendersBoundedJinjaBranches(t *testing.T) {
	analysis := AnalyzeTemplateText(`
{{ bos_token }}
{% set open = '<' + 'analysis' + '>' %}
{% set close = '</' + 'analysis' + '>' %}
{% for message in messages %}
{% if message.role == 'assistant' %}
{{ open + message.reasoning_content + close }}
{% elif message.role == 'user' %}
{{ message.content }}
{% endif %}
{% endfor %}
	`)
	if !analysis.Detected {
		t.Fatalf("expected rendered template analysis to detect markers: %#v", analysis)
	}
	if analysis.Config.ThinkTag != "analysis" {
		t.Fatalf("think tag = %q config=%#v", analysis.Config.ThinkTag, analysis.Config)
	}
	if !hasTemplateEvidence(analysis.Evidence, "think_tag", "analysis", "rendered_inspection") {
		t.Fatalf("missing rendered think evidence: %#v", analysis.Evidence)
	}
	if !hasTemplateEvidence(analysis.Evidence, "special_token", "<s>", "rendered_inspection") {
		t.Fatalf("missing rendered bos evidence: %#v", analysis.Evidence)
	}
}

func TestAnalyzeTemplateTextRendersToolCallLoop(t *testing.T) {
	analysis := AnalyzeTemplateText(`
{% for message in messages %}
{% if message.tool_calls %}
{% for tc in message.tool_calls %}
{% set tc = tc.function %}
{{ '<tool_' + 'call>' + tc.name }}
{% for k, v in tc.arguments.items() %}
<arg_key>{{ k }}</arg_key><arg_value>{{ v }}</arg_value>
{% endfor %}
</tool_call>
{% endfor %}
{% endif %}
{% endfor %}
	`)
	if !analysis.Detected {
		t.Fatalf("expected rendered tool-call template analysis to detect markers: %#v", analysis)
	}
	if analysis.Config.ToolTag != "tool_call" || analysis.Config.ToolOpen != "<tool_call>" || analysis.Config.ToolClose != "</tool_call>" {
		t.Fatalf("tool config = %#v", analysis.Config)
	}
	if !hasTemplateEvidence(analysis.Evidence, "tool_tag", "tool_call", "rendered_inspection") {
		t.Fatalf("missing rendered tool evidence: %#v", analysis.Evidence)
	}
}

func TestAnalyzeTemplateTextWarnsForUnsupportedAndMalformedJinja(t *testing.T) {
	analysis := AnalyzeTemplateText(`before {% include 'partial.jinja' %} {{ '<' + 'think' + '>' }`)
	if len(analysis.Warnings) == 0 {
		t.Fatalf("expected warnings, got %#v", analysis)
	}
	if !hasWarningContaining(analysis.Warnings, "unsupported Jinja statement") {
		t.Fatalf("missing unsupported statement warning: %#v", analysis.Warnings)
	}
	if !hasWarningContaining(analysis.Warnings, "unterminated {{") {
		t.Fatalf("missing malformed marker warning: %#v", analysis.Warnings)
	}
}

func TestAnalyzeTemplateTextCapsNestedLoopRendering(t *testing.T) {
	var template strings.Builder
	for i := 0; i < 10; i++ {
		template.WriteString("{% for message in messages %}")
	}
	template.WriteString("x")
	for i := 0; i < 10; i++ {
		template.WriteString("{% endfor %}")
	}

	analysis := AnalyzeTemplateText(template.String())
	if !hasWarningContaining(analysis.Warnings, "loop iteration limit") {
		t.Fatalf("missing bounded loop warning: %#v", analysis.Warnings)
	}
}

func TestDeriveParseTagsConfigFromTemplateTextQwenXML(t *testing.T) {
	cfg, ok := DeriveParseTagsConfigFromTemplateText(`
{{ '<tool_call>\n<function=' + tool_call.name + '>\n' }}
{{ '<parameter=' + args_name + '>\n' }}
{{ value }}
{{ '\n</parameter>\n</function>\n</tool_call>' }}
`)
	if !ok {
		t.Fatal("expected qwen config")
	}
	if cfg.ToolParser != "qwen_xml" || cfg.ToolTag != "tool_call" || cfg.ToolOpen != "<tool_call>" || cfg.ToolClose != "</tool_call>" || cfg.ToolArgFormat != "xml" {
		t.Fatalf("qwen config = %#v", cfg)
	}

	parser := NewParseTags(ParseTagsConfig{
		ToolTag:        cfg.ToolTag,
		ToolOpen:       cfg.ToolOpen,
		ToolClose:      cfg.ToolClose,
		ToolArgFormat:  cfg.ToolArgFormat,
		ToolParser:     cfg.ToolParser,
		ParseToolCalls: true,
	})
	payload := chatPayload("<tool_call>\n<function=search>\n<parameter=query>\ncats\n</parameter>\n</function>\n</tool_call>")
	if !parser.TransformPayload(payload, Context{}) {
		t.Fatal("expected transform")
	}
	msg := firstMessage(payload, "message")
	fn := msg["tool_calls"].([]any)[0].(map[string]any)["function"].(map[string]any)
	if fn["name"] != "search" || fn["arguments"] != `{"query":"cats"}` {
		t.Fatalf("function = %#v", fn)
	}
}

func TestParseTagsK2TemplateDerivedToolCall(t *testing.T) {
	cfg, _ := DeriveParseTagsConfigFromTemplateText(`<think></think><|tool_calls_section_begin|><|tool_call_begin|>name<|tool_call_argument_begin|>{}<|tool_call_end|><|tool_calls_section_end|>`)
	parser := NewParseTags(ParseTagsConfig{
		ThinkTag:         cfg.ThinkTag,
		ToolOpen:         cfg.ToolOpen,
		ToolClose:        cfg.ToolClose,
		ToolArgFormat:    cfg.ToolArgFormat,
		ToolArgSeparator: cfg.ToolArgSeparator,
		DropTags:         cfg.DropTags,
		ParseThinking:    true,
		ParseToolCalls:   true,
	})
	payload := chatPayload(`<think>Reason</think>Answer<|tool_calls_section_begin|><|tool_call_begin|>lookup<|tool_call_argument_begin|>{"query":"x"}<|tool_call_end|><|tool_calls_section_end|>`)
	if !parser.TransformPayload(payload, Context{}) {
		t.Fatal("expected transform")
	}
	msg := firstMessage(payload, "message")
	if msg["reasoning_content"] != "Reason" || msg["content"] != "Answer" {
		t.Fatalf("message = %#v", msg)
	}
	calls := msg["tool_calls"].([]any)
	fn := calls[0].(map[string]any)["function"].(map[string]any)
	if fn["name"] != "lookup" || fn["arguments"] != `{"query":"x"}` {
		t.Fatalf("function = %#v", fn)
	}
	choice := payload["choices"].([]any)[0].(map[string]any)
	if choice["finish_reason"] != "tool_calls" {
		t.Fatalf("finish_reason = %#v", choice["finish_reason"])
	}
}

func TestParseQwenJSONToolCallSetsFinishReason(t *testing.T) {
	payload := chatPayload(`<tool_call>{"name":"show_files","parameters":{}}</tool_call>`)
	parser := NewParseTags(ParseTagsConfig{
		ParseThinking:  true,
		ParseToolCalls: true,
		ToolParser:     "qwen_xml",
	})
	if !parser.TransformPayload(payload, Context{}) {
		t.Fatal("expected transform")
	}
	msg := firstMessage(payload, "message")
	if got := msg["content"]; got != nil {
		t.Fatalf("content = %#v", got)
	}
	calls := msg["tool_calls"].([]any)
	fn := calls[0].(map[string]any)["function"].(map[string]any)
	if fn["name"] != "show_files" || fn["arguments"] != `{}` {
		t.Fatalf("function = %#v", fn)
	}
	choice := payload["choices"].([]any)[0].(map[string]any)
	if choice["finish_reason"] != "tool_calls" {
		t.Fatalf("finish_reason = %#v", choice["finish_reason"])
	}
}

func TestParseTagsStartInThinkNonStream(t *testing.T) {
	parser := NewParseTags(ParseTagsConfig{
		ParseThinking: true,
		StartInThink:  true,
	})

	payload := chatPayload("hidden reasoning</think>visible answer")
	if !parser.TransformPayload(payload, Context{}) {
		t.Fatal("expected transform")
	}
	msg := firstMessage(payload, "message")
	if msg["reasoning_content"] != "hidden reasoning" || msg["content"] != "visible answer" {
		t.Fatalf("message = %#v", msg)
	}

	unterminated := chatPayload("reasoning without close")
	if parser.TransformPayload(unterminated, Context{}) {
		t.Fatalf("unterminated start-in-think should be preserved: %#v", unterminated)
	}
	if got := firstMessage(unterminated, "message")["content"]; got != "reasoning without close" {
		t.Fatalf("content = %#v", got)
	}
}

func TestRewriteSSEChunkWithStateParsesStartInThinkFragments(t *testing.T) {
	pipeline := Pipeline{Modules: []ResponseModule{NewParseTags(ParseTagsConfig{
		ParseThinking: true,
		StartInThink:  true,
	})}}
	state := pipeline.NewStreamState()
	ctx := Context{Path: "/v1/chat/completions", IsStream: true}
	chunks := [][]byte{
		sseChunk(map[string]any{"choices": []any{map[string]any{"index": 0, "delta": map[string]any{"content": "hidden "}}}}),
		sseChunk(map[string]any{"choices": []any{map[string]any{"index": 0, "delta": map[string]any{"content": "</thi"}}}}),
		sseChunk(map[string]any{"choices": []any{map[string]any{"index": 0, "delta": map[string]any{"content": "nk>visible"}}}}),
	}
	var reasoning string
	var content string
	for _, chunk := range chunks {
		out := RewriteSSEChunkWithState(chunk, pipeline, ctx, state)
		payload := decodeSSEPayload(t, out)
		delta := firstMessage(payload, "delta")
		if part, ok := delta["reasoning_content"].(string); ok {
			reasoning += part
		}
		if part, ok := delta["content"].(string); ok {
			content += part
		}
	}
	if reasoning != "hidden " {
		t.Fatalf("reasoning = %q", reasoning)
	}
	if content != "visible" {
		t.Fatalf("content = %q", content)
	}
}

func TestRewriteSSEChunkWithStateKeepsChoiceStateIsolated(t *testing.T) {
	pipeline := Pipeline{Modules: []ResponseModule{NewParseTags(ParseTagsConfig{ParseThinking: true})}}
	state := pipeline.NewStreamState()
	ctx := Context{Path: "/v1/chat/completions", IsStream: true}
	chunks := [][]byte{
		sseChunk(map[string]any{"choices": []any{
			map[string]any{"index": 0, "delta": map[string]any{"content": "<thi"}},
			map[string]any{"index": 1, "delta": map[string]any{"content": "plain "}},
		}}),
		sseChunk(map[string]any{"choices": []any{
			map[string]any{"index": 0, "delta": map[string]any{"content": "nk>zero</think>answer"}},
			map[string]any{"index": 1, "delta": map[string]any{"content": "<think>one</think>visible"}},
		}}),
	}
	contentByChoice := map[int]string{}
	reasoningByChoice := map[int]string{}
	for _, chunk := range chunks {
		out := RewriteSSEChunkWithState(chunk, pipeline, ctx, state)
		payload := decodeSSEPayload(t, out)
		for _, rawChoice := range payload["choices"].([]any) {
			choice := rawChoice.(map[string]any)
			index := choiceIndex(choice, -1)
			delta := choice["delta"].(map[string]any)
			if part, ok := delta["content"].(string); ok {
				contentByChoice[index] += part
			}
			if part, ok := delta["reasoning_content"].(string); ok {
				reasoningByChoice[index] += part
			}
		}
	}
	if reasoningByChoice[0] != "zero" || contentByChoice[0] != "answer" {
		t.Fatalf("choice 0 content=%q reasoning=%q", contentByChoice[0], reasoningByChoice[0])
	}
	if reasoningByChoice[1] != "one" || contentByChoice[1] != "plain visible" {
		t.Fatalf("choice 1 content=%q reasoning=%q", contentByChoice[1], reasoningByChoice[1])
	}
}

func TestRewriteSSEChunkWithStateParsesQwenJSONToolCall(t *testing.T) {
	pipeline := Pipeline{Modules: []ResponseModule{NewParseTags(ParseTagsConfig{
		ParseToolCalls:  true,
		StreamToolCalls: true,
		ToolParser:      "qwen_xml",
	})}}
	state := pipeline.NewStreamState()
	ctx := Context{Path: "/v1/chat/completions", IsStream: true}
	chunks := [][]byte{
		sseChunk(map[string]any{"choices": []any{map[string]any{"index": 0, "delta": map[string]any{"content": `<tool_call>{"name":"show_files",`}}}}),
		sseChunk(map[string]any{"choices": []any{map[string]any{"index": 0, "delta": map[string]any{"content": `"parameters":{}}</tool_call>"`}}}}),
	}
	var calls []any
	var content string
	var finishReason any
	for _, chunk := range chunks {
		out := RewriteSSEChunkWithState(chunk, pipeline, ctx, state)
		payload := decodeSSEPayload(t, out)
		choice := payload["choices"].([]any)[0].(map[string]any)
		if choice["finish_reason"] != nil {
			finishReason = choice["finish_reason"]
		}
		delta := choice["delta"].(map[string]any)
		if part, ok := delta["content"].(string); ok {
			content += part
		}
		if deltaCalls, ok := delta["tool_calls"].([]any); ok {
			calls = append(calls, deltaCalls...)
		}
	}
	if content != "" {
		t.Fatalf("content = %q", content)
	}
	if len(calls) != 1 {
		t.Fatalf("calls = %#v", calls)
	}
	fn := calls[0].(map[string]any)["function"].(map[string]any)
	if fn["name"] != "show_files" || fn["arguments"] != `{}` {
		t.Fatalf("function = %#v", fn)
	}
	if finishReason != "tool_calls" {
		t.Fatalf("finish_reason = %#v", finishReason)
	}
}

func TestRewriteSSEChunkWithStateStreamsQwenXMLMultipleParameters(t *testing.T) {
	pipeline := Pipeline{Modules: []ResponseModule{NewParseTags(ParseTagsConfig{
		ParseToolCalls:  true,
		StreamToolCalls: true,
		ToolParser:      "qwen_xml",
	})}}
	state := pipeline.NewStreamState()
	ctx := Context{Path: "/v1/chat/completions", IsStream: true}
	chunks := [][]byte{
		sseChunk(map[string]any{"choices": []any{map[string]any{"index": 0, "delta": map[string]any{"content": "<tool_call>\n<function=search>\n<parameter=query>\ncat"}}}}),
		sseChunk(map[string]any{"choices": []any{map[string]any{"index": 0, "delta": map[string]any{"content": "s\n</parameter>\n<parameter=filters>\n{\"kind\":\"image\",\"limit\":2}"}}}}),
		sseChunk(map[string]any{"choices": []any{map[string]any{"index": 0, "delta": map[string]any{"content": "\n</parameter>\n</function>\n</tool_call>"}}}}),
	}
	var name string
	var arguments string
	var finishReason any
	for _, chunk := range chunks {
		out := RewriteSSEChunkWithState(chunk, pipeline, ctx, state)
		payload := decodeSSEPayload(t, out)
		choice := payload["choices"].([]any)[0].(map[string]any)
		if choice["finish_reason"] != nil {
			finishReason = choice["finish_reason"]
		}
		delta := choice["delta"].(map[string]any)
		if content, ok := delta["content"].(string); ok && content != "" {
			t.Fatalf("unexpected content fragment = %q", content)
		}
		for _, rawCall := range delta["tool_calls"].([]any) {
			call := rawCall.(map[string]any)
			fn := call["function"].(map[string]any)
			if fnName, ok := fn["name"].(string); ok && fnName != "" {
				name = fnName
			}
			if fragment, ok := fn["arguments"].(string); ok {
				arguments += fragment
			}
		}
	}
	if name != "search" {
		t.Fatalf("name = %q", name)
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(arguments), &parsed); err != nil {
		t.Fatalf("arguments are not valid JSON %q: %v", arguments, err)
	}
	if parsed["query"] != "cats" {
		t.Fatalf("query = %#v", parsed["query"])
	}
	filters := parsed["filters"].(map[string]any)
	if filters["kind"] != "image" || filters["limit"] != float64(2) {
		t.Fatalf("filters = %#v", filters)
	}
	if finishReason != "tool_calls" {
		t.Fatalf("finish_reason = %#v", finishReason)
	}
}

func TestRewriteSSEChunkWithStateParsesK2TemplateToolCall(t *testing.T) {
	cfg, _ := DeriveParseTagsConfigFromTemplateText(`<think></think><|tool_calls_section_begin|><|tool_call_begin|>name<|tool_call_argument_begin|>{}<|tool_call_end|><|tool_calls_section_end|>`)
	parser := NewParseTags(ParseTagsConfig{
		ThinkTag:         cfg.ThinkTag,
		ToolOpen:         cfg.ToolOpen,
		ToolClose:        cfg.ToolClose,
		ToolArgFormat:    cfg.ToolArgFormat,
		ToolArgSeparator: cfg.ToolArgSeparator,
		DropTags:         cfg.DropTags,
		ParseThinking:    true,
		ParseToolCalls:   true,
	})
	pipeline := Pipeline{Modules: []ResponseModule{parser}}
	state := pipeline.NewStreamState()
	ctx := Context{Path: "/v1/chat/completions", IsStream: true}
	chunks := [][]byte{
		sseChunk(map[string]any{"choices": []any{map[string]any{"index": 0, "delta": map[string]any{"content": "<think>Reason</think>Answer<|tool_calls_section_begin|><|tool_call_begin|>look"}}}}),
		sseChunk(map[string]any{"choices": []any{map[string]any{"index": 0, "delta": map[string]any{"content": "up<|tool_call_argument_begin|>{\"query\":\"x\"}<|tool_call_end|><|tool_calls_section_end|>"}}}}),
	}
	var content string
	var reasoning string
	var calls []any
	for _, chunk := range chunks {
		out := RewriteSSEChunkWithState(chunk, pipeline, ctx, state)
		payload := decodeSSEPayload(t, out)
		choice := payload["choices"].([]any)[0].(map[string]any)
		delta := choice["delta"].(map[string]any)
		if part, ok := delta["content"].(string); ok {
			content += part
		}
		if part, ok := delta["reasoning_content"].(string); ok {
			reasoning += part
		}
		if deltaCalls, ok := delta["tool_calls"].([]any); ok {
			calls = append(calls, deltaCalls...)
		}
	}
	if content != "Answer" || reasoning != "Reason" {
		t.Fatalf("content=%q reasoning=%q", content, reasoning)
	}
	if len(calls) != 1 {
		t.Fatalf("calls = %#v", calls)
	}
	fn := calls[0].(map[string]any)["function"].(map[string]any)
	if fn["name"] != "lookup" || fn["arguments"] != `{"query":"x"}` {
		t.Fatalf("function = %#v", fn)
	}
}

func TestParseTagsInvalidToolCallLiteralPreserved(t *testing.T) {
	literal := `<tool_call>not a call</tool_call>`
	parser := NewParseTags(ParseTagsConfig{ParseToolCalls: true})
	payload := chatPayload(literal)

	if !parser.TransformPayload(payload, Context{}) {
		t.Fatal("expected transform from parser scan")
	}
	msg := firstMessage(payload, "message")
	if got := msg["content"]; got != literal {
		t.Fatalf("content = %#v", got)
	}
	if _, ok := msg["tool_calls"]; ok {
		t.Fatalf("unexpected tool calls: %#v", msg["tool_calls"])
	}
	choice := payload["choices"].([]any)[0].(map[string]any)
	if got := choice["finish_reason"]; got != nil {
		t.Fatalf("finish_reason = %#v", got)
	}

	pipeline := Pipeline{Modules: []ResponseModule{parser}}
	streamed := streamChunks(t, pipeline, []string{literal})
	if streamed.Content != literal {
		t.Fatalf("stream content = %q", streamed.Content)
	}
	if len(streamed.Calls) != 0 || streamed.FinishReason != nil {
		t.Fatalf("stream calls=%#v finish=%#v", streamed.Calls, streamed.FinishReason)
	}
}

func TestRewriteSSEChunkWithStateStartInThinkQwenXMLToolCall(t *testing.T) {
	pipeline := Pipeline{Modules: []ResponseModule{NewParseTags(ParseTagsConfig{
		ParseThinking:   true,
		ParseToolCalls:  true,
		StreamToolCalls: true,
		StartInThink:    true,
		ToolParser:      "qwen_xml",
	})}}
	streamed := streamChunks(t, pipeline, []string{
		"Reasoning ",
		"trace</thi",
		"nk><tool_call>\n<function=show_files>\n",
		"</function>\n</tool_call>",
	})
	if streamed.Reasoning != "Reasoning trace" {
		t.Fatalf("reasoning = %q", streamed.Reasoning)
	}
	if streamed.Content != "" {
		t.Fatalf("content = %q", streamed.Content)
	}
	if len(streamed.Calls) != 1 {
		t.Fatalf("calls = %#v", streamed.Calls)
	}
	fn := streamed.Calls[0].(map[string]any)["function"].(map[string]any)
	if fn["name"] != "show_files" || fn["arguments"] != `{}` {
		t.Fatalf("function = %#v", fn)
	}
	if streamed.FinishReason != "tool_calls" {
		t.Fatalf("finish_reason = %#v", streamed.FinishReason)
	}
}

func TestParseQwenXMLNoArgFunction(t *testing.T) {
	parser := NewParseTags(ParseTagsConfig{
		ParseToolCalls: true,
		ToolParser:     "qwen_xml",
	})
	payload := chatPayload("<tool_call>\n<function=show_files>\n</function>\n</tool_call>")
	if !parser.TransformPayload(payload, Context{}) {
		t.Fatal("expected transform")
	}
	msg := firstMessage(payload, "message")
	if got := msg["content"]; got != nil {
		t.Fatalf("content = %#v", got)
	}
	calls := msg["tool_calls"].([]any)
	fn := calls[0].(map[string]any)["function"].(map[string]any)
	if fn["name"] != "show_files" || fn["arguments"] != `{}` {
		t.Fatalf("function = %#v", fn)
	}
	choice := payload["choices"].([]any)[0].(map[string]any)
	if choice["finish_reason"] != "tool_calls" {
		t.Fatalf("finish_reason = %#v", choice["finish_reason"])
	}

	pipeline := Pipeline{Modules: []ResponseModule{NewParseTags(ParseTagsConfig{
		ParseToolCalls:  true,
		StreamToolCalls: true,
		ToolParser:      "qwen_xml",
	})}}
	streamed := streamChunks(t, pipeline, []string{
		"<tool_call>\n<function=show_",
		"files>\n</function>\n</tool_call>",
	})
	if len(streamed.Calls) != 1 {
		t.Fatalf("stream calls = %#v", streamed.Calls)
	}
	fn = streamed.Calls[0].(map[string]any)["function"].(map[string]any)
	if fn["name"] != "show_files" || fn["arguments"] != `{}` {
		t.Fatalf("stream function = %#v", fn)
	}
	if streamed.Content != "" || streamed.FinishReason != "tool_calls" {
		t.Fatalf("stream content=%q finish=%#v", streamed.Content, streamed.FinishReason)
	}
}

func TestRewriteSSEChunkWithStateQwenXMLDuplicateFunctionRepairMultipleCalls(t *testing.T) {
	pipeline := Pipeline{Modules: []ResponseModule{NewParseTags(ParseTagsConfig{
		ParseToolCalls:  true,
		StreamToolCalls: true,
		ToolParser:      "qwen_xml",
	})}}
	streamed := streamChunks(t, pipeline, []string{
		"<tool_call>\n<function=search>\n<fun",
		"ction=search>\n<parameter=query>\ncat",
		"s\n</parameter>\n</function>\n</tool_call>",
		"<tool_call>\n<function=lookup>\n",
		"<parameter=id>\n42\n</parameter>\n</function>\n</tool_call>",
	})
	calls := collapseStreamToolCalls(t, streamed.Calls)
	if len(calls) != 2 {
		t.Fatalf("calls = %#v", calls)
	}
	if calls[0].Name != "search" || calls[0].Arguments["query"] != "cats" {
		t.Fatalf("first call = %#v", calls[0])
	}
	if calls[1].Name != "lookup" || calls[1].Arguments["id"] != "42" {
		t.Fatalf("second call = %#v", calls[1])
	}
	if streamed.Content != "" || streamed.FinishReason != "tool_calls" {
		t.Fatalf("content=%q finish=%#v", streamed.Content, streamed.FinishReason)
	}
}

func TestParseTagsJSONToolCallTrailingTextBehavior(t *testing.T) {
	cfg, _ := DeriveParseTagsConfigFromTemplateText(`<|tool_calls_section_begin|><|tool_call_begin|>name<|tool_call_argument_begin|>{}<|tool_call_end|><|tool_calls_section_end|>`)
	parser := NewParseTags(ParseTagsConfig{
		ToolOpen:         cfg.ToolOpen,
		ToolClose:        cfg.ToolClose,
		ToolArgFormat:    cfg.ToolArgFormat,
		ToolArgSeparator: cfg.ToolArgSeparator,
		DropTags:         cfg.DropTags,
		ParseToolCalls:   true,
	})
	payload := chatPayload(`prefix <|tool_calls_section_begin|><|tool_call_begin|>lookup<|tool_call_argument_begin|>{"q":"x"}<|tool_call_end|><|tool_calls_section_end|> trailing text`)
	if !parser.TransformPayload(payload, Context{}) {
		t.Fatal("expected transform")
	}
	msg := firstMessage(payload, "message")
	if got := msg["content"]; got != "prefix " {
		t.Fatalf("content = %#v", got)
	}
	fn := msg["tool_calls"].([]any)[0].(map[string]any)["function"].(map[string]any)
	if fn["name"] != "lookup" || fn["arguments"] != `{"q":"x"}` {
		t.Fatalf("function = %#v", fn)
	}
}

func TestRewriteSSEChunkWithStateQwenJSONToolCallTrailingTextDropped(t *testing.T) {
	pipeline := Pipeline{Modules: []ResponseModule{NewParseTags(ParseTagsConfig{
		ParseToolCalls: true,
		ToolParser:     "qwen_xml",
	})}}
	streamed := streamChunks(t, pipeline, []string{
		`<tool_call>{"name":"show_files",`,
		`"parameters":{}}</tool_call> trailing text`,
	})
	if streamed.Content != "" {
		t.Fatalf("content = %q", streamed.Content)
	}
	if len(streamed.Calls) != 1 {
		t.Fatalf("calls = %#v", streamed.Calls)
	}
	fn := streamed.Calls[0].(map[string]any)["function"].(map[string]any)
	if fn["name"] != "show_files" || fn["arguments"] != `{}` {
		t.Fatalf("function = %#v", fn)
	}
}

func TestRewriteSSEChunkWithStateArbitraryFragmentBoundaries(t *testing.T) {
	tests := []struct {
		name     string
		cfg      ParseTagsConfig
		input    string
		content  string
		reason   string
		callName string
		args     map[string]any
	}{
		{
			name:    "reasoning",
			cfg:     ParseTagsConfig{ParseThinking: true},
			input:   "<think>Reason</think>Answer",
			content: "Answer",
			reason:  "Reason",
		},
		{
			name:     "xml tool",
			cfg:      ParseTagsConfig{ParseToolCalls: true},
			input:    `<tool_call>search<arg_key>query</arg_key><arg_value>"cats"</arg_value></tool_call>`,
			callName: "search",
			args:     map[string]any{"query": "cats"},
		},
		{
			name:     "qwen xml",
			cfg:      ParseTagsConfig{ParseToolCalls: true, ToolParser: "qwen_xml"},
			input:    "<tool_call>\n<function=search>\n<parameter=query>\ncats\n</parameter>\n</function>\n</tool_call>",
			callName: "search",
			args:     map[string]any{"query": "cats"},
		},
		{
			name:     "qwen json",
			cfg:      ParseTagsConfig{ParseToolCalls: true, ToolParser: "qwen_xml"},
			input:    `<tool_call>{"name":"show_files","parameters":{}}</tool_call>`,
			callName: "show_files",
			args:     map[string]any{},
		},
		{
			name: "k2",
			cfg: ParseTagsConfig{
				ToolOpen:         "<|tool_call_begin|>",
				ToolClose:        "<|tool_call_end|>",
				ToolArgFormat:    "json",
				ToolArgSeparator: "<|tool_call_argument_begin|>",
				DropTags:         []string{"<|tool_calls_section_begin|>", "<|tool_calls_section_end|>"},
				ParseToolCalls:   true,
			},
			input:    `<|tool_calls_section_begin|><|tool_call_begin|>lookup<|tool_call_argument_begin|>{"q":"x"}<|tool_call_end|><|tool_calls_section_end|>`,
			callName: "lookup",
			args:     map[string]any{"q": "x"},
		},
		{
			name:    "start in think",
			cfg:     ParseTagsConfig{ParseThinking: true, StartInThink: true},
			input:   "Reason</think>Answer",
			content: "Answer",
			reason:  "Reason",
		},
		{
			name:    "invalid literal",
			cfg:     ParseTagsConfig{ParseToolCalls: true},
			input:   "<tool_call>not a call</tool_call>",
			content: "<tool_call>not a call</tool_call>",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for split := 1; split < len(tt.input); split++ {
				streamed := streamChunks(t, Pipeline{Modules: []ResponseModule{NewParseTags(tt.cfg)}}, []string{
					tt.input[:split],
					tt.input[split:],
				})
				assertStreamAggregate(t, streamed, tt.content, tt.reason, tt.callName, tt.args)
			}

			for splitA := 1; splitA < len(tt.input); splitA += 3 {
				for splitB := splitA + 1; splitB < len(tt.input); splitB += 5 {
					streamed := streamChunks(t, Pipeline{Modules: []ResponseModule{NewParseTags(tt.cfg)}}, []string{
						tt.input[:splitA],
						tt.input[splitA:splitB],
						tt.input[splitB:],
					})
					assertStreamAggregate(t, streamed, tt.content, tt.reason, tt.callName, tt.args)
				}
			}
		})
	}
}

func TestParseTagsCustomTagsAndDropTags(t *testing.T) {
	payload := chatPayload(`<analysis>hidden</analysis>visible`)
	parser := NewParseTags(ParseTagsConfig{ThinkTag: "analysis", ParseThinking: true})
	if !parser.TransformPayload(payload, Context{}) {
		t.Fatal("expected custom think transform")
	}
	msg := firstMessage(payload, "message")
	if msg["reasoning_content"] != "hidden" || msg["content"] != "visible" {
		t.Fatalf("custom think message = %#v", msg)
	}

	payload = chatPayload(`<function_call>search<arg_key>query</arg_key><arg_value>cats</arg_value></function_call>`)
	parser = NewParseTags(ParseTagsConfig{
		ToolTag:        "function_call",
		ParseToolCalls: true,
	})
	if !parser.TransformPayload(payload, Context{}) {
		t.Fatal("expected custom tool transform")
	}
	msg = firstMessage(payload, "message")
	fn := msg["tool_calls"].([]any)[0].(map[string]any)["function"].(map[string]any)
	if fn["name"] != "search" || fn["arguments"] != `{"query":"cats"}` {
		t.Fatalf("custom tool function = %#v", fn)
	}

	payload = chatPayload(`A<drop>B</drop>C`)
	parser = NewParseTags(ParseTagsConfig{DropTags: []string{"<drop>", "</drop>"}})
	if !parser.TransformPayload(payload, Context{}) {
		t.Fatal("expected drop tag transform")
	}
	msg = firstMessage(payload, "message")
	if msg["content"] != "ABC" {
		t.Fatalf("drop-tag content = %#v", msg["content"])
	}
}

func TestRewriteSSEChunkWithStateToolBufferFallbackInsideThink(t *testing.T) {
	pipeline := Pipeline{Modules: []ResponseModule{NewParseTags(ParseTagsConfig{
		ParseThinking:   true,
		ParseToolCalls:  true,
		ToolBufferLimit: 5,
	})}}
	streamed := streamChunks(t, pipeline, []string{"<think><tool_call>abcdef</think>Answer"})
	if streamed.Reasoning != "<tool_call>abcdef</think>Answer" {
		t.Fatalf("reasoning = %q", streamed.Reasoning)
	}
	if streamed.Content != "" {
		t.Fatalf("content = %q", streamed.Content)
	}
	if len(streamed.Calls) != 0 {
		t.Fatalf("calls = %#v", streamed.Calls)
	}
}

func TestRewriteSSEChunkWithStateToolBufferFallbackThenRecovery(t *testing.T) {
	pipeline := Pipeline{Modules: []ResponseModule{NewParseTags(ParseTagsConfig{
		ParseToolCalls:  true,
		ToolBufferLimit: 5,
	})}}
	streamed := streamChunks(t, pipeline, []string{
		"<tool_call>abcdef",
		` before <tool_call>search<arg_key>query</arg_key><arg_value>"cats"</arg_value></tool_call>`,
	})
	if streamed.Content != "<tool_call>abcdef before " {
		t.Fatalf("content = %q", streamed.Content)
	}
	if len(streamed.Calls) != 1 {
		t.Fatalf("calls = %#v", streamed.Calls)
	}
	fn := streamed.Calls[0].(map[string]any)["function"].(map[string]any)
	if fn["name"] != "search" || fn["arguments"] != `{"query":"cats"}` {
		t.Fatalf("function = %#v", fn)
	}
}

func TestRewriteSSEChunkWithStateStreamBufferCapFlushesTagPrefixAsContent(t *testing.T) {
	pipeline := Pipeline{Modules: []ResponseModule{NewParseTags(ParseTagsConfig{
		ToolOpen:             "<long_tool_call>",
		ToolClose:            "</long_tool_call>",
		ParseToolCalls:       true,
		MaxStreamBufferBytes: 8,
	})}}
	streamed := streamChunks(t, pipeline, []string{"<long_too", " visible"})
	if streamed.Content != "<long_too visible" {
		t.Fatalf("content = %q", streamed.Content)
	}
	if streamed.Reasoning != "" {
		t.Fatalf("reasoning = %q", streamed.Reasoning)
	}
	if len(streamed.Calls) != 0 || streamed.FinishReason != nil {
		t.Fatalf("calls=%#v finish=%#v", streamed.Calls, streamed.FinishReason)
	}
}

func TestRewriteSSEChunkWithStateStreamBufferCapFlushesThinkPrefixAsReasoning(t *testing.T) {
	pipeline := Pipeline{Modules: []ResponseModule{NewParseTags(ParseTagsConfig{
		ParseThinking:        true,
		MaxStreamBufferBytes: 4,
	})}}
	streamed := streamChunks(t, pipeline, []string{"<think>why </thi", " visible"})
	if streamed.Reasoning != "why </thi" {
		t.Fatalf("reasoning = %q", streamed.Reasoning)
	}
	if streamed.Content != " visible" {
		t.Fatalf("content = %q", streamed.Content)
	}
	if len(streamed.Calls) != 0 || streamed.FinishReason != nil {
		t.Fatalf("calls=%#v finish=%#v", streamed.Calls, streamed.FinishReason)
	}
}

func TestRewriteSSEChunkWithStateStreamBufferCapFlushesToolBufferAsContent(t *testing.T) {
	pipeline := Pipeline{Modules: []ResponseModule{NewParseTags(ParseTagsConfig{
		ParseToolCalls:       true,
		MaxStreamBufferBytes: 20,
	})}}
	streamed := streamChunks(t, pipeline, []string{"<tool_call>abcdefghijklmnop", " visible"})
	if streamed.Content != "<tool_call>abcdefghijklmnop visible" {
		t.Fatalf("content = %q", streamed.Content)
	}
	if streamed.Reasoning != "" {
		t.Fatalf("reasoning = %q", streamed.Reasoning)
	}
	if len(streamed.Calls) != 0 || streamed.FinishReason != nil {
		t.Fatalf("calls=%#v finish=%#v", streamed.Calls, streamed.FinishReason)
	}
}

func TestRewriteSSEChunkWithStateStreamBufferCapFlushesToolBufferAsReasoning(t *testing.T) {
	pipeline := Pipeline{Modules: []ResponseModule{NewParseTags(ParseTagsConfig{
		ParseThinking:        true,
		ParseToolCalls:       true,
		MaxStreamBufferBytes: 20,
	})}}
	streamed := streamChunks(t, pipeline, []string{"<think><tool_call>abcdefghijklmnop", " visible"})
	if streamed.Reasoning != "<tool_call>abcdefghijklmnop" {
		t.Fatalf("reasoning = %q", streamed.Reasoning)
	}
	if streamed.Content != " visible" {
		t.Fatalf("content = %q", streamed.Content)
	}
	if len(streamed.Calls) != 0 || streamed.FinishReason != nil {
		t.Fatalf("calls=%#v finish=%#v", streamed.Calls, streamed.FinishReason)
	}
}

func TestRewriteSSEChunkWithStateStreamBufferAgeFlushesTagPrefixAsContent(t *testing.T) {
	pipeline := Pipeline{Modules: []ResponseModule{NewParseTags(ParseTagsConfig{
		ToolOpen:           "<long_tool_call>",
		ToolClose:          "</long_tool_call>",
		ParseToolCalls:     true,
		MaxStreamBufferAge: time.Second,
	})}}
	state := pipeline.NewStreamState()
	now := time.Unix(1_700_000_000, 0)
	ctx := Context{Path: "/v1/chat/completions", IsStream: true, Now: func() time.Time { return now }}

	first := RewriteSSEChunkWithState(sseChunk(map[string]any{"choices": []any{map[string]any{"index": 0, "delta": map[string]any{"content": "<long_too"}}}}), pipeline, ctx, state)
	if delta := firstMessage(decodeSSEPayload(t, first), "delta"); delta["content"] != nil {
		t.Fatalf("first content = %#v", delta["content"])
	}
	now = now.Add(2 * time.Second)
	second := RewriteSSEChunkWithState(sseChunk(map[string]any{"choices": []any{map[string]any{"index": 0, "delta": map[string]any{"content": " visible"}}}}), pipeline, ctx, state)
	delta := firstMessage(decodeSSEPayload(t, second), "delta")
	if got := delta["content"]; got != "<long_too visible" {
		t.Fatalf("content = %#v", got)
	}
	if got := delta["reasoning_content"]; got != nil {
		t.Fatalf("reasoning = %#v", got)
	}
}

func TestRewriteSSEChunkWithStateStreamBufferAgeFlushesThinkPrefixAsReasoning(t *testing.T) {
	pipeline := Pipeline{Modules: []ResponseModule{NewParseTags(ParseTagsConfig{
		ParseThinking:      true,
		MaxStreamBufferAge: time.Second,
	})}}
	state := pipeline.NewStreamState()
	now := time.Unix(1_700_000_000, 0)
	ctx := Context{Path: "/v1/chat/completions", IsStream: true, Now: func() time.Time { return now }}
	var reasoning string
	var content string

	first := RewriteSSEChunkWithState(sseChunk(map[string]any{"choices": []any{map[string]any{"index": 0, "delta": map[string]any{"content": "<think>why </thi"}}}}), pipeline, ctx, state)
	delta := firstMessage(decodeSSEPayload(t, first), "delta")
	if part, ok := delta["reasoning_content"].(string); ok {
		reasoning += part
	}
	if part, ok := delta["content"].(string); ok {
		content += part
	}

	now = now.Add(2 * time.Second)
	second := RewriteSSEChunkWithState(sseChunk(map[string]any{"choices": []any{map[string]any{"index": 0, "delta": map[string]any{"content": " visible"}}}}), pipeline, ctx, state)
	delta = firstMessage(decodeSSEPayload(t, second), "delta")
	if part, ok := delta["reasoning_content"].(string); ok {
		reasoning += part
	}
	if part, ok := delta["content"].(string); ok {
		content += part
	}

	if reasoning != "why </thi" {
		t.Fatalf("reasoning = %q", reasoning)
	}
	if content != " visible" {
		t.Fatalf("content = %q", content)
	}
}

func TestRewriteSSEChunkWithStateStreamBufferAgeDisabledByDefault(t *testing.T) {
	pipeline := Pipeline{Modules: []ResponseModule{NewParseTags(ParseTagsConfig{
		ParseThinking: true,
	})}}
	state := pipeline.NewStreamState()
	now := time.Unix(1_700_000_000, 0)
	ctx := Context{Path: "/v1/chat/completions", IsStream: true, Now: func() time.Time { return now }}
	var reasoning string
	var content string
	for _, part := range []string{"<thi", "nk>why</think>answer"} {
		chunk := RewriteSSEChunkWithState(sseChunk(map[string]any{"choices": []any{map[string]any{"index": 0, "delta": map[string]any{"content": part}}}}), pipeline, ctx, state)
		delta := firstMessage(decodeSSEPayload(t, chunk), "delta")
		if part, ok := delta["reasoning_content"].(string); ok {
			reasoning += part
		}
		if part, ok := delta["content"].(string); ok {
			content += part
		}
		now = now.Add(24 * time.Hour)
	}
	if reasoning != "why" {
		t.Fatalf("reasoning = %q", reasoning)
	}
	if content != "answer" {
		t.Fatalf("content = %q", content)
	}
}

func TestPipelineFlushThenResetStreamStatePreservesBufferedParseTagsContent(t *testing.T) {
	pipeline := Pipeline{Modules: []ResponseModule{NewParseTags(ParseTagsConfig{ParseThinking: true})}}
	state := pipeline.NewStreamState()
	ctx := Context{Path: "/v1/chat/completions", IsStream: true}
	_ = RewriteSSEChunkWithState(sseChunk(map[string]any{"choices": []any{map[string]any{"index": 0, "delta": map[string]any{"content": "<thi"}}}}), pipeline, ctx, state)

	payloads := pipeline.FlushSSEPayloadsWithState(ctx, state)
	if len(payloads) != 1 {
		t.Fatalf("flush payloads = %#v", payloads)
	}
	delta := firstMessage(payloads[0], "delta")
	if got := delta["content"]; got != "<thi" {
		t.Fatalf("flushed content = %#v", got)
	}

	pipeline.ResetStreamState(state)
	moduleState := state.ModuleStates[0].(*parseTagsStreamState)
	if len(moduleState.choices) != 0 {
		t.Fatalf("choices after reset = %#v", moduleState.choices)
	}
	next := RewriteSSEChunkWithState(sseChunk(map[string]any{"choices": []any{map[string]any{"index": 0, "delta": map[string]any{"content": "plain"}}}}), pipeline, ctx, state)
	delta = firstMessage(decodeSSEPayload(t, next), "delta")
	if got := delta["content"]; got != "plain" {
		t.Fatalf("content after reset = %#v", got)
	}
	if got := delta["reasoning_content"]; got != nil {
		t.Fatalf("stale reasoning after reset = %#v", got)
	}
}

func TestPipelineLifecycleHooksNilSafeIdempotentAndNoopForStatelessModules(t *testing.T) {
	parsePipeline := Pipeline{Modules: []ResponseModule{NewParseTags(ParseTagsConfig{ParseThinking: true})}}
	state := parsePipeline.NewStreamState()
	parsePipeline.CloseStreamState(nil)
	parsePipeline.CloseStreamState(state)
	parsePipeline.CloseStreamState(state)
	parsePipeline.ResetStreamState(nil)
	parsePipeline.ResetStreamState(state)
	parsePipeline.ResetStreamState(state)

	stateless := Pipeline{Modules: []ResponseModule{NewReasoningSwap("auto")}}
	stateless.ResetStreamState(nil)
	stateless.CloseStreamState(nil)
	statelessState := stateless.NewStreamState()
	stateless.ResetStreamState(statelessState)
	stateless.CloseStreamState(statelessState)
}

func TestDeriveParseTagsConfigRoundTripsCustomTemplate(t *testing.T) {
	cfg, ok := DeriveParseTagsConfigFromTemplateText(`{{ '<analysis>' + reasoning_content + '</analysis>' }} {{ '<function_call>' + tool_name + '</function_call>' }}`)
	if !ok {
		t.Fatal("expected derived config")
	}
	parser := NewParseTags(ParseTagsConfig{
		ThinkTag:       cfg.ThinkTag,
		ToolTag:        cfg.ToolTag,
		ToolOpen:       cfg.ToolOpen,
		ToolClose:      cfg.ToolClose,
		ToolArgFormat:  cfg.ToolArgFormat,
		ParseThinking:  true,
		ParseToolCalls: true,
	})
	payload := chatPayload(`<analysis>why</analysis>answer<function_call>lookup<arg_key>q</arg_key><arg_value>x</arg_value></function_call>`)
	if !parser.TransformPayload(payload, Context{}) {
		t.Fatal("expected transform")
	}
	msg := firstMessage(payload, "message")
	if msg["reasoning_content"] != "why" || msg["content"] != "answer" {
		t.Fatalf("message = %#v", msg)
	}
	fn := msg["tool_calls"].([]any)[0].(map[string]any)["function"].(map[string]any)
	if fn["name"] != "lookup" || fn["arguments"] != `{"q":"x"}` {
		t.Fatalf("function = %#v", fn)
	}
}

func sseChunk(payload map[string]any) []byte {
	buf, _ := json.Marshal(payload)
	return append([]byte("data: "), append(buf, []byte("\n\n")...)...)
}

func decodeSSEPayload(t *testing.T, chunk []byte) map[string]any {
	t.Helper()
	text := string(chunk)
	if len(text) < len("data: \n\n") || text[:len("data: ")] != "data: " {
		t.Fatalf("unexpected SSE chunk: %q", text)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(text[len("data: "):len(text)-2]), &payload); err != nil {
		t.Fatalf("invalid SSE payload %q: %v", text, err)
	}
	return payload
}

func chatPayload(content string) map[string]any {
	return map[string]any{
		"choices": []any{
			map[string]any{
				"message": map[string]any{"content": content},
			},
		},
	}
}

func firstMessage(payload map[string]any, key string) map[string]any {
	return payload["choices"].([]any)[0].(map[string]any)[key].(map[string]any)
}

type streamAggregate struct {
	Content      string
	Reasoning    string
	Calls        []any
	FinishReason any
}

func streamChunks(t *testing.T, pipeline Pipeline, parts []string) streamAggregate {
	t.Helper()
	state := pipeline.NewStreamState()
	ctx := Context{Path: "/v1/chat/completions", IsStream: true}
	var out streamAggregate
	for _, part := range parts {
		chunk := sseChunk(map[string]any{"choices": []any{map[string]any{"index": 0, "delta": map[string]any{"content": part}}}})
		rewritten := RewriteSSEChunkWithState(chunk, pipeline, ctx, state)
		payload := decodeSSEPayload(t, rewritten)
		choice := payload["choices"].([]any)[0].(map[string]any)
		if choice["finish_reason"] != nil {
			out.FinishReason = choice["finish_reason"]
		}
		delta := choice["delta"].(map[string]any)
		if part, ok := delta["content"].(string); ok {
			out.Content += part
		}
		if part, ok := delta["reasoning_content"].(string); ok {
			out.Reasoning += part
		}
		if calls, ok := delta["tool_calls"].([]any); ok {
			out.Calls = append(out.Calls, calls...)
		}
	}
	return out
}

type collapsedToolCall struct {
	Name      string
	Arguments map[string]any
}

func collapseStreamToolCalls(t *testing.T, calls []any) []collapsedToolCall {
	t.Helper()
	type partial struct {
		name      string
		arguments string
	}
	partials := map[int]*partial{}
	var order []int
	for _, rawCall := range calls {
		call := rawCall.(map[string]any)
		index := choiceIndex(call, 0)
		if _, ok := partials[index]; !ok {
			partials[index] = &partial{}
			order = append(order, index)
		}
		fn := call["function"].(map[string]any)
		if name, ok := fn["name"].(string); ok && name != "" {
			partials[index].name = name
		}
		if args, ok := fn["arguments"].(string); ok {
			partials[index].arguments += args
		}
	}
	out := make([]collapsedToolCall, 0, len(order))
	for _, index := range order {
		item := partials[index]
		args := map[string]any{}
		if item.arguments != "" {
			if err := json.Unmarshal([]byte(item.arguments), &args); err != nil {
				t.Fatalf("tool call %d arguments are not JSON %q: %v", index, item.arguments, err)
			}
		}
		out = append(out, collapsedToolCall{Name: item.name, Arguments: args})
	}
	return out
}

func hasTemplateEvidence(evidence []TemplateEvidence, kind string, value string, source string) bool {
	for _, item := range evidence {
		if item.Kind == kind && item.Value == value && item.Source == source {
			return true
		}
	}
	return false
}

func hasWarningContaining(warnings []string, needle string) bool {
	for _, warning := range warnings {
		if strings.Contains(warning, needle) {
			return true
		}
	}
	return false
}

func assertStreamAggregate(t *testing.T, got streamAggregate, content, reasoning, callName string, args map[string]any) {
	t.Helper()
	if got.Content != content {
		t.Fatalf("content = %q, want %q", got.Content, content)
	}
	if got.Reasoning != reasoning {
		t.Fatalf("reasoning = %q, want %q", got.Reasoning, reasoning)
	}
	calls := collapseStreamToolCalls(t, got.Calls)
	if callName == "" {
		if len(calls) != 0 {
			t.Fatalf("calls = %#v", calls)
		}
		return
	}
	if len(calls) != 1 {
		t.Fatalf("calls = %#v", calls)
	}
	if calls[0].Name != callName {
		t.Fatalf("call name = %q, want %q", calls[0].Name, callName)
	}
	if !reflect.DeepEqual(calls[0].Arguments, args) {
		t.Fatalf("arguments = %#v, want %#v", calls[0].Arguments, args)
	}
	if got.FinishReason != "tool_calls" {
		t.Fatalf("finish_reason = %#v", got.FinishReason)
	}
}
