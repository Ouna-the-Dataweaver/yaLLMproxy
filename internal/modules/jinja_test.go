package modules

import (
	"reflect"
	"strings"
	"testing"
)

type jinjaToolExpectation struct {
	tag          string
	open         string
	close        string
	argFormat    string
	argSeparator string
	parser       string
	dropTags     []string
}

type jinjaCorpusCase struct {
	name                    string
	template                string
	wantDetected            bool
	wantThinkTag            string
	wantTool                *jinjaToolExpectation
	wantWarnings            []string
	wantNoWarnings          []string
	wantEvidence            []TemplateEvidence
	wantNoEvidence          []TemplateEvidence
	wantRenderedContains    []string
	wantRenderedNotContains []string
}

func TestJinjaInspectorCorpus(t *testing.T) {
	cases := []jinjaCorpusCase{
		{
			name:         "expressions filters methods and boolean tests",
			wantDetected: false,
			template: `
{% set ns = namespace(count=0) %}
{% set ns.count = 5 - 2 %}
{% set values = ['zero', 'one', 'two'] %}
{% if true is true and false is false and none is none and 'a' is string and tools is iterable and tools is not mapping and messages[0] is mapping and missing is not defined and messages[0].role is not undefined and 3 >= 2 and 2 <= 2 and 4 > 3 and 'b' > 'a' and 2 != 3 and 'prompt' in messages[0].content %}
expr={{ 'pre' ~ '-' + values[1] }}|{{ 'yes' if true else 'no' }}|{{ ns.count }}|{{ messages[2].tool_calls[0].function.arguments["query"] }}
method={{ messages[2].tool_calls[0].function.arguments.get('missing', 'fallback') }}|{{ '  padded  '.strip() }}|{{ '--edge--'.lstrip('-').rstrip('-') }}
{% if 'prefix'.startswith('pre') and 'suffix'.endswith('fix') %}method-bool{% endif %}
{% for part in 'a,b'.split(',') %}[{{ part }}]{% endfor %}
filters={{ ' trim ' | trim | string }}|{{ tools | length }}|{{ messages[2].tool_calls[0].function.arguments | items | length }}|{{ tools | tojson | safe }}
{% endif %}
`,
			wantNoWarnings: []string{"unsupported Jinja", "malformed Jinja"},
			wantRenderedContains: []string{
				"expr=pre-one|yes|3|cats",
				"method=fallback|padded|edge",
				"method-bool",
				"[a][b]",
				"filters=trim|1|1|",
				`"name":"lookup"`,
			},
		},
		{
			name:         "for unpacking loop metadata and branch selection",
			wantDetected: false,
			template: `
{% for k, v in messages[2].tool_calls[0].function.arguments.items() %}
pair={{ k }}:{{ v }}:{{ loop.index0 }}/{{ loop.index }}/{{ loop.first }}/{{ loop.last }}/{{ loop.length }}
{% endfor %}
{% for a, b in [['left', 'right']] %}tuple={{ a }}-{{ b }}{% endfor %}
{% for n in range(1, 4, 2) %}range={{ n }};{% endfor %}
{% for empty in [] %}EMPTY{% endfor %}
{% if false %}bad{% elif messages[1].role == 'user' %}ELIF{% else %}bad{% endif %}
`,
			wantNoWarnings: []string{"unsupported Jinja", "malformed Jinja"},
			wantRenderedContains: []string{
				"pair=query:cats:0/1/true/true/1",
				"tuple=left-right",
				"range=1;",
				"range=3;",
				"ELIF",
			},
			wantRenderedNotContains: []string{"EMPTY", "bad", "range=2;"},
		},
		{
			name:         "unsupported filter and function warnings preserve evidence",
			wantDetected: true,
			wantThinkTag: "analysis",
			template: `
{{ '<' + 'analysis>' }}{{ unknown_wrapper('sample') | upper }}{{ '</analysis>' }}
`,
			wantWarnings: []string{
				"unsupported Jinja function call approximated with first argument: unknown_wrapper",
				"unsupported Jinja filter ignored: upper",
			},
			wantEvidence: []TemplateEvidence{
				{Kind: "think_tag", Value: "analysis", Source: "rendered_inspection"},
			},
			wantRenderedContains: []string{"<analysis>sample</analysis>"},
		},
		{
			name:         "macro comments and unsupported control warnings",
			wantDetected: false,
			template: `
start{# comment should disappear #}{% macro hidden() %}MACRO_BODY{% endmacro %}after
{% break %}{% continue %}
{% block body %}BLOCK_BODY{% endif %}{% else %}
{% set invalid %}
{% for item messages %}BAD_FOR{% endfor %}
`,
			wantWarnings: []string{
				"unsupported Jinja macro block skipped during inspection render",
				"unsupported Jinja loop control ignored during inspection render: break",
				"unsupported Jinja loop control ignored during inspection render: continue",
				"unsupported Jinja statement ignored: block body",
				"malformed Jinja block marker ignored: endif",
				"malformed Jinja block marker ignored: else",
				"unsupported Jinja set syntax: set invalid",
				"unsupported Jinja for syntax: for item messages",
			},
			wantRenderedContains:    []string{"start", "after", "BLOCK_BODY"},
			wantRenderedNotContains: []string{"comment should disappear", "MACRO_BODY", "BAD_FOR"},
		},
		{
			name:         "qwen xml from raw template",
			wantDetected: true,
			wantTool: &jinjaToolExpectation{
				tag:       "tool_call",
				open:      "<tool_call>",
				close:     "</tool_call>",
				argFormat: "xml",
				parser:    "qwen_xml",
			},
			template: `
<tool_call>
<function=lookup>
<parameter=query>
cats
</parameter>
</function>
</tool_call>
`,
			wantEvidence: []TemplateEvidence{
				{Kind: "tool_call_format", Value: "qwen_xml", Source: "raw"},
				{Kind: "tool_tag", Value: "tool_call", Source: "raw"},
			},
		},
		{
			name:         "qwen xml from rendered fragments",
			wantDetected: true,
			wantTool: &jinjaToolExpectation{
				tag:       "tool_call",
				open:      "<tool_call>",
				close:     "</tool_call>",
				argFormat: "xml",
				parser:    "qwen_xml",
			},
			template: `
{{ '<tool_' + 'call>' }}
{{ '<function' + '=' + 'lookup' + '>' }}
{{ '<parameter' + '=' + 'query' + '>' }}cats{{ '</parameter>' }}
{{ '</function>' }}
{{ '</tool_' + 'call>' }}
`,
			wantEvidence: []TemplateEvidence{
				{Kind: "tool_call_format", Value: "qwen_xml", Source: "rendered_inspection"},
				{Kind: "tool_tag", Value: "tool_call", Source: "rendered_inspection"},
			},
			wantNoEvidence: []TemplateEvidence{
				{Kind: "tool_call_format", Value: "qwen_xml", Source: "raw"},
				{Kind: "tool_tag", Value: "tool_call", Source: "raw"},
			},
			wantRenderedContains: []string{
				"<tool_call>",
				"<function=lookup>",
				"<parameter=query>",
				"</tool_call>",
			},
		},
		{
			name:         "k2 special tokens take precedence over qwen xml",
			wantDetected: true,
			wantTool: &jinjaToolExpectation{
				open:         "<|tool_call_begin|>",
				close:        "<|tool_call_end|>",
				argFormat:    "json",
				argSeparator: "<|tool_call_argument_begin|>",
				dropTags:     []string{"<|tool_calls_section_begin|>", "<|tool_calls_section_end|>"},
			},
			template: `
<|tool_calls_section_begin|><|tool_call_begin|>lookup<|tool_call_argument_begin|>{"query":"cats"}<|tool_call_end|><|tool_calls_section_end|>
<tool_call><function=lookup><parameter=query>cats</parameter></function></tool_call>
`,
			wantEvidence: []TemplateEvidence{
				{Kind: "tool_call_format", Value: "k2_json", Source: "raw"},
				{Kind: "tool_call_format", Value: "qwen_xml", Source: "raw"},
			},
		},
		{
			name:         "generic tool tag and rendered thought tag",
			wantDetected: true,
			wantThinkTag: "thought",
			wantTool: &jinjaToolExpectation{
				tag:       "function_call",
				open:      "<function_call>",
				close:     "</function_call>",
				argFormat: "xml",
			},
			template: `
{{ '<thought>' + messages[2].reasoning_content + '</thought>' }}
<function_call>lookup<arg_key>query</arg_key><arg_value>cats</arg_value></function_call>
`,
			wantEvidence: []TemplateEvidence{
				{Kind: "think_tag", Value: "thought", Source: "rendered_inspection"},
				{Kind: "tool_tag", Value: "function_call", Source: "raw"},
			},
		},
		{
			name:         "frequency based generic marker fallback",
			wantDetected: true,
			wantThinkTag: "scratchpad",
			template: `
<scratchpad>first</scratchpad>
<trace>only</trace>
<scratchpad>second</scratchpad>
`,
			wantRenderedContains: []string{"<scratchpad>first</scratchpad>"},
		},
		{
			name:         "special token evidence does not imply config",
			wantDetected: false,
			template: `
[gMASK]{{ bos_token }}{{ '<|im_start|>' }}
`,
			wantEvidence: []TemplateEvidence{
				{Kind: "special_token", Value: "[gMASK]", Source: "raw"},
				{Kind: "special_token", Value: "<s>", Source: "rendered_inspection"},
				{Kind: "special_token", Value: "<|im_start|>", Source: "rendered_inspection"},
			},
		},
		{
			name:         "eos special token evidence does not imply config",
			wantDetected: false,
			template: `
{{ eos_token }}
`,
			wantEvidence: []TemplateEvidence{
				{Kind: "special_token", Value: "</s>", Source: "rendered_inspection"},
			},
		},
		{
			name:         "plain template has no marker config",
			wantDetected: false,
			template: `
Hello {{ messages[1].content }}.
`,
			wantNoWarnings:       []string{"unsupported Jinja", "malformed Jinja"},
			wantRenderedContains: []string{"Hello user prompt."},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			analysis := AnalyzeTemplateText(tc.template)
			rendered, _ := renderJinjaInspectionText(tc.template)

			if analysis.Detected != tc.wantDetected {
				t.Fatalf("Detected = %v, want %v; config=%#v warnings=%#v evidence=%#v", analysis.Detected, tc.wantDetected, analysis.Config, analysis.Warnings, analysis.Evidence)
			}
			if analysis.Config.ThinkTag != tc.wantThinkTag {
				t.Fatalf("ThinkTag = %q, want %q; config=%#v", analysis.Config.ThinkTag, tc.wantThinkTag, analysis.Config)
			}
			assertJinjaToolConfig(t, analysis.Config, tc.wantTool)
			for _, want := range tc.wantWarnings {
				if !hasWarningContaining(analysis.Warnings, want) {
					t.Fatalf("missing warning containing %q in %#v", want, analysis.Warnings)
				}
			}
			for _, needle := range tc.wantNoWarnings {
				if hasWarningContaining(analysis.Warnings, needle) {
					t.Fatalf("unexpected warning containing %q in %#v", needle, analysis.Warnings)
				}
			}
			for _, want := range tc.wantEvidence {
				if !hasTemplateEvidence(analysis.Evidence, want.Kind, want.Value, want.Source) {
					t.Fatalf("missing evidence %#v in %#v", want, analysis.Evidence)
				}
			}
			for _, want := range tc.wantNoEvidence {
				if hasTemplateEvidence(analysis.Evidence, want.Kind, want.Value, want.Source) {
					t.Fatalf("unexpected evidence %#v in %#v", want, analysis.Evidence)
				}
			}
			for _, want := range tc.wantRenderedContains {
				if !strings.Contains(rendered, want) {
					t.Fatalf("rendered output missing %q:\n%s", want, rendered)
				}
			}
			for _, unwanted := range tc.wantRenderedNotContains {
				if strings.Contains(rendered, unwanted) {
					t.Fatalf("rendered output unexpectedly contains %q:\n%s", unwanted, rendered)
				}
			}
		})
	}
}

func TestJinjaInspectorOutputByteBudgetWarnsAndTruncates(t *testing.T) {
	literal := strings.Repeat("x", jinjaInspectionMaxOutputBytes+1024)
	rendered, warnings := renderJinjaInspectionText("{{ '" + literal + "' }}")

	if !hasWarningContaining(warnings.sorted(), "output limit reached") {
		t.Fatalf("missing output budget warning: %#v", warnings.sorted())
	}
	envCount := len(inspectionEnvironments())
	wantLen := jinjaInspectionMaxOutputBytes*envCount + envCount - 1
	if len(rendered) != wantLen {
		t.Fatalf("rendered length = %d, want %d", len(rendered), wantLen)
	}
}

func TestJinjaInspectorRenderDepthBudgetWarns(t *testing.T) {
	var template strings.Builder
	for i := 0; i < jinjaInspectionMaxRenderDepth+2; i++ {
		template.WriteString("{% if true %}")
	}
	template.WriteString("too deep")
	for i := 0; i < jinjaInspectionMaxRenderDepth+2; i++ {
		template.WriteString("{% endif %}")
	}

	analysis := AnalyzeTemplateText(template.String())
	if !hasWarningContaining(analysis.Warnings, "render depth limit") {
		t.Fatalf("missing render depth warning: %#v", analysis.Warnings)
	}
}

func TestJinjaInspectorNestedLoopIterationBudgetWarns(t *testing.T) {
	var template strings.Builder
	for i := 0; i < 5; i++ {
		template.WriteString("{% for message in messages %}")
	}
	template.WriteString("x")
	for i := 0; i < 5; i++ {
		template.WriteString("{% endfor %}")
	}

	analysis := AnalyzeTemplateText(template.String())
	if !hasWarningContaining(analysis.Warnings, "loop iteration limit") {
		t.Fatalf("missing loop iteration warning: %#v", analysis.Warnings)
	}
	if hasWarningContaining(analysis.Warnings, "render depth limit") {
		t.Fatalf("unexpected render depth warning in loop budget test: %#v", analysis.Warnings)
	}
}

func TestJinjaInspectorEvidenceIsDeterministic(t *testing.T) {
	template := `
{% for k, v in messages[2].tool_calls[0].function.arguments.items() %}
{{ '<tool_' + 'call>' }}<function=lookup><parameter={{ k }}>{{ v }}</parameter></function>{{ '</tool_' + 'call>' }}
{% endfor %}
[gMASK]{{ bos_token }}
`
	first := AnalyzeTemplateText(template)
	firstRendered, firstWarnings := renderJinjaInspectionText(template)

	for i := 0; i < 20; i++ {
		analysis := AnalyzeTemplateText(template)
		rendered, warnings := renderJinjaInspectionText(template)
		if !reflect.DeepEqual(analysis.Evidence, first.Evidence) {
			t.Fatalf("evidence changed on iteration %d:\nfirst=%#v\ngot=%#v", i, first.Evidence, analysis.Evidence)
		}
		if !reflect.DeepEqual(analysis.Warnings, first.Warnings) {
			t.Fatalf("warnings changed on iteration %d:\nfirst=%#v\ngot=%#v", i, first.Warnings, analysis.Warnings)
		}
		if rendered != firstRendered {
			t.Fatalf("rendered output changed on iteration %d:\nfirst=%q\ngot=%q", i, firstRendered, rendered)
		}
		if !reflect.DeepEqual(warnings.sorted(), firstWarnings.sorted()) {
			t.Fatalf("render warnings changed on iteration %d:\nfirst=%#v\ngot=%#v", i, firstWarnings.sorted(), warnings.sorted())
		}
	}
}

func assertJinjaToolConfig(t *testing.T, got ParseTagsConfig, want *jinjaToolExpectation) {
	t.Helper()
	if want == nil {
		if got.ToolTag != "" || got.ToolOpen != "" || got.ToolClose != "" || got.ToolArgFormat != "" || got.ToolArgSeparator != "" || got.ToolParser != "" || len(got.DropTags) != 0 {
			t.Fatalf("tool config = %#v, want empty tool config", got)
		}
		return
	}
	if got.ToolTag != want.tag || got.ToolOpen != want.open || got.ToolClose != want.close || got.ToolArgFormat != want.argFormat || got.ToolArgSeparator != want.argSeparator || got.ToolParser != want.parser || !reflect.DeepEqual(got.DropTags, want.dropTags) {
		t.Fatalf("tool config = %#v, want %#v", got, *want)
	}
}
