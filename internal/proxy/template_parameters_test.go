package proxy

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestTemplateParameterDefaultsAreForwardedWithoutRecursiveUnwrapping(t *testing.T) {
	low := map[string]any{"thinking": true, "reasoning_effort": "low"}
	for name, value := range map[string]any{
		"flat":                              low,
		"nested_default_is_literal_payload": map[string]any{"default": low, "allow_override": true},
	} {
		t.Run(name, func(t *testing.T) {
			backend := Backend{TargetModel: "probe", Parameters: parseParameterConfigs(map[string]any{
				"chat_template_kwargs": map[string]any{"default": value, "allow_override": true},
			})}
			body := BuildBackendBody(map[string]any{"model": "probe"}, backend, []byte(`{"model":"probe"}`), false)
			var got map[string]any
			if err := json.Unmarshal(body, &got); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got["chat_template_kwargs"], value) {
				t.Fatalf("template kwargs = %#v, want %#v", got["chat_template_kwargs"], value)
			}
		})
	}
}
