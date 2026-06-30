package modules

import (
	"errors"
	"strings"
	"testing"
)

func TestRuntimeJinjaRendererExpressionsFiltersAndAccess(t *testing.T) {
	env := runtimeTestEnv()
	template := strings.Join([]string{
		`{{ bos_token }}`,
		`{{ messages[1].role }}:{{ messages[1]['content'] | trim | upper | replace('HELLO', 'Hi') }}`,
		`{{ missing | default('fallback') }}`,
		`{{ tools | length }}:{{ tools[0].function.name }}`,
		`{{ {'b': 2, 'a': 1} | tojson }}`,
		`{{ 'abcdef' | slice(1, 4) }}`,
		`{{ 2 + 3 * 4 }}:{{ 7 // 2 }}:{{ 7 % 3 }}`,
	}, "|")

	result, err := RenderJinjaTemplate(template, env, RuntimeJinjaRenderOptions{})
	if err != nil {
		t.Fatal(err)
	}
	want := `<s>|user:Hi WORLD|fallback|1:lookup|{"a":1,"b":2}|bcd|14:3:1`
	if result.Rendered != want {
		t.Fatalf("rendered = %q, want %q", result.Rendered, want)
	}
}

func TestRuntimeJinjaRendererIfForRangeAndLoopMetadata(t *testing.T) {
	env := runtimeTestEnv()
	template := `
{% for message in messages %}
[{{ loop.index0 }}:{{ loop.index }}:{{ loop.first }}:{{ loop.last }}:{{ message.role }}]
{% endfor %}
{% for n in range(1, 6, 2) %}{{ n }};{% endfor %}
{% if false %}bad{% elif messages[0].role == 'system' and 'system' in messages[0].content %}ELIF{% else %}bad{% endif %}
{% for message in messages[::-1] %}{{ message.role }};{% endfor %}
`

	result, err := RenderJinjaTemplate(template, env, RuntimeJinjaRenderOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"[0:1:true:false:system]",
		"[3:4:false:true:tool]",
		"1;3;5;",
		"ELIF",
		"tool;assistant;user;system;",
	} {
		if !strings.Contains(result.Rendered, want) {
			t.Fatalf("rendered output missing %q:\n%s", want, result.Rendered)
		}
	}
	if strings.Contains(result.Rendered, "bad") {
		t.Fatalf("rendered output included untaken branch:\n%s", result.Rendered)
	}
}

func TestRuntimeJinjaRendererWhitespaceTrimCanBePreserved(t *testing.T) {
	template := "a {{- 'b' -}} c"
	result, err := RenderJinjaTemplate(template, nil, RuntimeJinjaRenderOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if result.Rendered != "abc" {
		t.Fatalf("trimmed rendered = %q, want abc", result.Rendered)
	}

	result, err = RenderJinjaTemplate(template, nil, RuntimeJinjaRenderOptions{PreserveWhitespace: true})
	if err != nil {
		t.Fatal(err)
	}
	if result.Rendered != "a b c" {
		t.Fatalf("preserved rendered = %q, want %q", result.Rendered, "a b c")
	}
}

func TestRuntimeJinjaRendererUnknownFilterAndStatementError(t *testing.T) {
	if _, err := RenderJinjaTemplate(`{{ 'x' | mystery }}`, nil, RuntimeJinjaRenderOptions{}); err == nil || !strings.Contains(err.Error(), "unsupported Jinja filter: mystery") {
		t.Fatalf("unknown filter err = %v", err)
	}
	if _, err := RenderJinjaTemplate(`{{ mystery('x') }}`, nil, RuntimeJinjaRenderOptions{}); err == nil || !strings.Contains(err.Error(), "unsupported Jinja function: mystery") {
		t.Fatalf("unknown function err = %v", err)
	}
	if _, err := RenderJinjaTemplate(`{% include 'other.jinja' %}`, nil, RuntimeJinjaRenderOptions{}); err == nil || !strings.Contains(err.Error(), "unsupported Jinja statement") {
		t.Fatalf("unknown statement err = %v", err)
	}
}

func TestRuntimeJinjaRendererOutputLimitReturnsError(t *testing.T) {
	result, err := RenderJinjaTemplate(`{{ 'abcdef' }}`, nil, RuntimeJinjaRenderOptions{MaxOutputBytes: 4})
	if err == nil {
		t.Fatal("expected output limit error")
	}
	if !errors.Is(err, ErrRuntimeJinjaLimit) {
		t.Fatalf("err = %v, want ErrRuntimeJinjaLimit", err)
	}
	if !result.Truncated || result.Rendered != "abcd" {
		t.Fatalf("result = %#v, want truncated abcd", result)
	}
}

func TestRuntimeJinjaRendererOutputLimitCountsBlockBytesOnce(t *testing.T) {
	template := `{% for i in range(3) %}abcd{% endfor %}`
	result, err := RenderJinjaTemplate(template, nil, RuntimeJinjaRenderOptions{MaxOutputBytes: 12})
	if err != nil {
		t.Fatalf("unexpected output limit error at exact cap: %v", err)
	}
	if result.Truncated || result.Rendered != "abcdabcdabcd" {
		t.Fatalf("result = %#v, want untruncated 12-byte loop output", result)
	}

	result, err = RenderJinjaTemplate(template, nil, RuntimeJinjaRenderOptions{MaxOutputBytes: 11})
	if err == nil {
		t.Fatal("expected output limit error")
	}
	if !errors.Is(err, ErrRuntimeJinjaLimit) {
		t.Fatalf("err = %v, want ErrRuntimeJinjaLimit", err)
	}
	if !result.Truncated || result.Rendered != "abcdabcdabc" {
		t.Fatalf("result = %#v, want truncated 11-byte loop output", result)
	}
}

func TestRuntimeJinjaRendererNestedLoopLimit(t *testing.T) {
	template := `{% for i in range(0, 3) %}{% for j in range(0, 3) %}{{ i }}{{ j }};{% endfor %}{% endfor %}`
	result, err := RenderJinjaTemplate(template, nil, RuntimeJinjaRenderOptions{MaxLoopIterations: 5})
	if err == nil {
		t.Fatal("expected loop limit error")
	}
	if !errors.Is(err, ErrRuntimeJinjaLimit) {
		t.Fatalf("err = %v, want ErrRuntimeJinjaLimit", err)
	}
	if !strings.Contains(result.Rendered, "00;01;02;") {
		t.Fatalf("partial nested loop output = %q", result.Rendered)
	}
}

func TestRuntimeJinjaRendererRenderDepthLimit(t *testing.T) {
	var template strings.Builder
	for i := 0; i < 5; i++ {
		template.WriteString("{% if true %}")
	}
	template.WriteString("deep")
	for i := 0; i < 5; i++ {
		template.WriteString("{% endif %}")
	}
	_, err := RenderJinjaTemplate(template.String(), nil, RuntimeJinjaRenderOptions{MaxRenderDepth: 3})
	if err == nil {
		t.Fatal("expected render depth limit error")
	}
	if !errors.Is(err, ErrRuntimeJinjaLimit) {
		t.Fatalf("err = %v, want ErrRuntimeJinjaLimit", err)
	}
}

func TestRuntimeJinjaRendererQwenStyleTemplate(t *testing.T) {
	template := `
{%- if tools %}
{{- '<|im_start|>system\n# Tools\n<tools>' }}
{%- for tool in tools %}
{{- '\n' + (tool | tojson) }}
{%- endfor %}
{{- '\n</tools><|im_end|>\n' }}
{%- endif %}
{%- for message in messages %}
{%- if message.role == 'system' %}
{{- '<|im_start|>system\n' + (message.content | trim) + '<|im_end|>\n' }}
{%- elif message.role == 'user' %}
{{- '<|im_start|>user\n' + (message.content | trim) + '<|im_end|>\n' }}
{%- elif message.role == 'assistant' and message.tool_calls %}
{{- '<|im_start|>assistant\n' + (message.content | default('', true) | trim) }}
{%- for tool_call in message.tool_calls %}
{{- '\n<tool_call>\n<function=' + tool_call.function.name + '>\n' }}
{%- for args_name, args_value in tool_call.function.arguments | items %}
{{- '<parameter=' + args_name + '>\n' }}
{{- args_value | tojson if args_value is mapping or (args_value is sequence and args_value is not string) else args_value | string }}
{{- '\n</parameter>\n' }}
{%- endfor %}
{{- '</function>\n</tool_call>' }}
{%- endfor %}
{{- '<|im_end|>\n' }}
{%- elif message.role == 'tool' %}
{{- '<|im_start|>user\n<tool_response>\n' + message.content + '\n</tool_response><|im_end|>\n' }}
{%- endif %}
{%- endfor %}
{%- if add_generation_prompt %}
{{- '<|im_start|>assistant\n' }}
{%- endif %}
`
	result, err := RenderJinjaTemplate(template, runtimeTestEnv(), RuntimeJinjaRenderOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"<|im_start|>system",
		"<|im_start|>user\nhello world",
		"<tool_call>\n<function=lookup>",
		"<parameter=query>\ncats\n</parameter>",
		"<tool_response>\ntool result\n</tool_response>",
		"<|im_start|>assistant\n",
	} {
		if !strings.Contains(result.Rendered, want) {
			t.Fatalf("rendered output missing %q:\n%s", want, result.Rendered)
		}
	}
}

func TestRuntimeJinjaRendererDeterministic(t *testing.T) {
	template := `{% for args_name, args_value in messages[2].tool_calls[0].function.arguments | items %}{{ args_name }}={{ args_value }};{% endfor %}{{ tools | tojson }}`
	first, err := RenderJinjaTemplate(template, runtimeTestEnv(), RuntimeJinjaRenderOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 20; i++ {
		got, err := RenderJinjaTemplate(template, runtimeTestEnv(), RuntimeJinjaRenderOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if got.Rendered != first.Rendered || strings.Join(got.Warnings, "\x00") != strings.Join(first.Warnings, "\x00") {
			t.Fatalf("render changed on iteration %d:\nfirst=%#v\ngot=%#v", i, first, got)
		}
	}
}

func runtimeTestEnv() map[string]any {
	return map[string]any{
		"bos_token":             "<s>",
		"eos_token":             "</s>",
		"add_generation_prompt": true,
		"tools": []any{
			map[string]any{
				"type": "function",
				"function": map[string]any{
					"name":        "lookup",
					"description": "find things",
					"parameters":  map[string]any{"type": "object"},
				},
			},
		},
		"messages": []any{
			map[string]any{"role": "system", "content": "system prompt"},
			map[string]any{"role": "user", "content": " hello world "},
			map[string]any{
				"role":    "assistant",
				"content": "",
				"tool_calls": []any{
					map[string]any{
						"id": "call_1",
						"function": map[string]any{
							"name":      "lookup",
							"arguments": map[string]any{"query": "cats"},
						},
					},
				},
			},
			map[string]any{"role": "tool", "content": "tool result", "tool_call_id": "call_1"},
		},
	}
}
