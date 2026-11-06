package gigachat

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func schemaJSON(t *testing.T, text string) map[string]any {
	t.Helper()
	var value map[string]any
	if err := json.Unmarshal([]byte(text), &value); err != nil {
		t.Fatal(err)
	}
	return value
}

func TestToolSchemaParityNullableFormsAndSingletons(t *testing.T) {
	for _, form := range []string{
		`{"type":["string","null"],"enum":["Day","Year",null]}`,
		`{"type":"string","nullable":true,"enum":["Day","Year"]}`,
		`{"anyOf":[{"type":"string","enum":["Day","Year"]},{"type":"null"}]}`,
		`{"oneOf":[{"type":["string"],"enum":["Day","Year"]},{"type":["null"]}]}`,
		`{"enum":["Day","Year",null]}`,
	} {
		schema := schemaJSON(t, `{"type":"object","properties":{"period":`+form+`,"query":{"type":"string"}},"required":["period","query"]}`)
		before := cloneMap(schema)
		plan, err := adaptSchema(schema)
		if err != nil {
			t.Fatal(err)
		}
		want := schemaJSON(t, `{"type":"object","properties":{"period":{"type":"string","enum":["Day","Year"]},"query":{"type":"string"}},"required":["query"]}`)
		if !reflect.DeepEqual(plan.schema, want) || !reflect.DeepEqual(schema, before) {
			t.Fatalf("form=%s schema=%#v", form, plan.schema)
		}
		got, err := plan.transform(map[string]any{"query": "test"}, false)
		if err != nil || !reflect.DeepEqual(got, map[string]any{"period": nil, "query": "test"}) {
			t.Fatalf("restored=%#v err=%v", got, err)
		}
		got, err = plan.transform(map[string]any{"period": nil, "query": "test"}, true)
		if err != nil || !reflect.DeepEqual(got, map[string]any{"query": "test"}) {
			t.Fatalf("upstream=%#v err=%v", got, err)
		}
	}
	for _, kind := range []string{"string", "integer", "number", "boolean", "array", "object"} {
		schema := map[string]any{"type": []string{"object"}, "properties": map[string]any{"categories": map[string]any{"type": []string{kind}}}, "required": []string{"categories"}}
		plan, err := adaptSchema(schema)
		if err != nil {
			t.Fatal(err)
		}
		if plan.schema["type"] != "object" || plan.properties["categories"].schema["type"] != kind || plan.properties["categories"].nullable || plan.needsRestoration() {
			t.Fatalf("singleton normalization: %+v", plan)
		}
		got, _ := plan.transform(map[string]any{}, false)
		if len(got.(map[string]any)) != 0 {
			t.Fatalf("manufactured non-nullable field: %+v", got)
		}
	}
}

func TestToolSchemaParityNestedRefsMapsAndLiteralData(t *testing.T) {
	schema := schemaJSON(t, `{"$defs":{"Filter":{"type":"object","properties":{"term":{"type":["string","null"]}},"required":["term"]}},"type":"object","properties":{"filters":{"type":"array","items":{"$ref":"#/$defs/Filter"}},"mapping":{"type":"object","additionalProperties":{"$ref":"#/$defs/Filter"}},"parent":{"$ref":"#/$defs/Filter"},"nullable_parent":{"anyOf":[{"$ref":"#/$defs/Filter"},{"type":"null"}]}},"required":["filters","nullable_parent"]}`)
	plan, err := adaptSchema(schema)
	if err != nil {
		t.Fatal(err)
	}
	input := schemaJSON(t, `{"filters":[{}, {"term":"x"}],"mapping":{"one":{}},"nullable_parent":{}}`)
	before := cloneMap(input)
	got, err := plan.transform(input, false)
	want := schemaJSON(t, `{"filters":[{"term":null},{"term":"x"}],"mapping":{"one":{"term":null}},"nullable_parent":{"term":null}}`)
	if err != nil || !reflect.DeepEqual(got, want) || !reflect.DeepEqual(input, before) {
		t.Fatalf("restored=%#v err=%v", got, err)
	}
	literal := schemaJSON(t, `{"type":"object","default":{"properties":{"x":{"enum":[null],"format":"uuid"}}},"examples":[{"$ref":"#/literal"}],"properties":{"empty":{"type":"null"},"enum_null":{"enum":[null]},"optional":{"type":"null"},"literal_null":{"const":null}},"required":["empty","enum_null","literal_null"]}`)
	plan, err = adaptSchema(literal)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(plan.schema["default"], literal["default"]) || !reflect.DeepEqual(plan.schema["examples"], literal["examples"]) {
		t.Fatal("rewrote literal defaults/examples as schemas")
	}
	got, err = plan.transform(map[string]any{}, false)
	if err != nil || !reflect.DeepEqual(got, map[string]any{"empty": nil, "enum_null": nil, "literal_null": nil}) {
		t.Fatalf("null-only fields=%#v err=%v", got, err)
	}
	escaped := schemaJSON(t, `{"definitions":{"a/b~c":{"type":["integer","null"]}},"type":"object","properties":{"value":{"allOf":[{"$ref":"#/definitions/a~1b~0c"}],"description":"value"}},"required":["value"]}`)
	plan, err = adaptSchema(escaped)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(plan.properties["value"].schema, map[string]any{"type": "integer", "description": "value"}) {
		t.Fatalf("escaped ref=%+v", plan.schema)
	}
}

func TestToolSchemaParityFormatsAndScalarUnions(t *testing.T) {
	for _, format := range []string{"uuid", "email", "uri", "regex", "date", "date-time", "time"} {
		plan, err := adaptSchema(map[string]any{"type": "string", "format": format, "description": "identifier"})
		if err != nil {
			t.Fatal(err)
		}
		if strings.HasPrefix(format, "date") || format == "time" {
			if plan.schema["format"] != format {
				t.Fatalf("lost temporal format: %+v", plan.schema)
			}
		} else if _, exists := plan.schema["format"]; exists || !strings.Contains(plan.schema["description"].(string), "Format: "+format+".") {
			t.Fatalf("unsupported format: %+v", plan.schema)
		}
	}
	for _, keyword := range []string{"anyOf", "oneOf"} {
		schema := schemaJSON(t, `{"type":"object","properties":{"date":{"`+keyword+`":[{"type":"string","format":"date"},{"type":"integer","minimum":0},{"type":"null"}]},"optional":{"type":["string","null"]}},"required":["date"]}`)
		plan, err := adaptSchema(schema)
		if err != nil {
			t.Fatal(err)
		}
		if plan.properties["date"].schema["type"] != "object" || !plan.needsRestoration() {
			t.Fatalf("missing scalar envelope: %+v", plan.schema)
		}
		for _, value := range []any{"2026-09-21", float64(0), float64(1726876800), nil} {
			input := map[string]any{"date": value}
			wire, err := plan.transform(input, true)
			if err != nil {
				t.Fatal(err)
			}
			got, err := plan.transform(wire, false)
			if err != nil || !reflect.DeepEqual(got, input) {
				t.Fatalf("scalar roundtrip=%#v want=%#v err=%v", got, input, err)
			}
		}
		for _, wire := range []any{map[string]any{}, map[string]any{"integer": 1, "string": "x"}, map[string]any{"integer": true}, "raw"} {
			if _, err := plan.properties["date"].transform(wire, false); err == nil {
				t.Fatalf("accepted ambiguous scalar envelope: %#v", wire)
			}
		}
	}
	for _, values := range []string{`[false,true,null]`, `[0,1,null]`, `[0,1.5,null]`} {
		if _, err := adaptSchema(schemaJSON(t, `{"enum":`+values+`}`)); err != nil {
			t.Fatal(err)
		}
	}
}

func TestToolSchemaParityUnsafeSchemasFailExplicitly(t *testing.T) {
	for _, source := range []string{
		`{"$ref":"#/missing"}`,
		`{"$ref":"https://example.com/schema"}`,
		`{"$defs":{"Loop":{"$ref":"#/$defs/Loop"}},"$ref":"#/$defs/Loop"}`,
		`{"oneOf":[{"type":"integer"},{"type":"number"}]}`,
		`{"anyOf":[{"type":"string"},{"type":"string"}]}`,
		`{"anyOf":[{"type":"string"},{"type":"integer"}],"minimum":0}`,
		`{"anyOf":[{"type":"object","properties":{"x":{"type":["string","null"]}},"required":["x"]},{"type":"string"}]}`,
	} {
		if _, err := adaptSchema(schemaJSON(t, source)); err == nil {
			t.Fatalf("accepted unsafe schema: %s", source)
		}
	}
}

func TestToolArgumentsParityLegacyHistoryAndIsolation(t *testing.T) {
	payload := schemaJSON(t, `{"functions":[{"name":"f","parameters":{"type":"object","properties":{"date":{"anyOf":[{"type":"string"},{"type":"integer"},{"type":"null"}]},"optional":{"type":["string","null"]}},"required":["date"]}}],"function_call":{"name":"f"},"messages":[{"role":"assistant","function_call":{"name":"f","arguments":"{\"date\":0,\"optional\":null}"}}]}`)
	before := cloneMap(payload)
	prepared, args, err := prepareToolArguments(payload)
	if err != nil {
		t.Fatal(err)
	}
	if !args.legacy || !args.needsRestoration() || !reflect.DeepEqual(payload, before) {
		t.Fatal("preparation mutated caller or lost legacy plan")
	}
	messages, _ := asSlice(prepared["messages"])
	message := messages[0].(map[string]any)
	calls, _ := asSlice(message["tool_calls"])
	call := calls[0].(map[string]any)["function"].(map[string]any)
	if call["arguments"] != `{"date":{"integer":0}}` {
		t.Fatalf("history arguments=%v", call["arguments"])
	}
	restored, err := args.restoreResponse(schemaJSON(t, `{"choices":[{"message":{"tool_calls":[{"function":{"name":"f","arguments":"{}"}}]},"finish_reason":"tool_calls"}]}`), true)
	if err != nil {
		t.Fatal(err)
	}
	choices, _ := asSlice(restored["choices"])
	choice := choices[0].(map[string]any)
	function := choice["message"].(map[string]any)["function_call"].(map[string]any)
	if function["arguments"] != `{"date":null}` || choice["finish_reason"] != "function_call" {
		t.Fatalf("legacy restoration=%+v", restored)
	}
	_, unrelated, err := prepareToolArguments(map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	if unrelated.needsRestoration() || len(unrelated.plans) != 0 {
		t.Fatal("schema state shared across requests")
	}
	malformed := `{"date":`
	got, err := args.restoreCall(map[string]any{"name": "f", "arguments": malformed})
	if err != nil || got["arguments"] != malformed {
		t.Fatal("manufactured malformed arguments")
	}
}
