package gigachat

import (
	"encoding/json"
	"fmt"
	"math"
	"strings"
)

// argumentSchema retains the caller's contract while exposing the subset of
// JSON Schema accepted by GigaChat. Plans belong to a single request.
type argumentSchema struct {
	schema         map[string]any
	nullable       bool
	nullOnly       bool
	properties     map[string]*argumentSchema
	requiredNulls  map[string]bool
	items          *argumentSchema
	additional     *argumentSchema
	scalarVariants map[string]*argumentSchema
}

func (p *argumentSchema) needsRestoration() bool {
	if p == nil {
		return false
	}
	if len(p.requiredNulls) > 0 || len(p.scalarVariants) > 0 {
		return true
	}
	for _, child := range p.properties {
		if child.needsRestoration() {
			return true
		}
	}
	return p.items.needsRestoration() || p.additional.needsRestoration()
}

func scalarKind(value any) string {
	switch v := value.(type) {
	case string:
		return "string"
	case bool:
		return "boolean"
	case int, int32, int64:
		return "integer"
	case float64:
		if v == math.Trunc(v) {
			return "integer"
		}
		return "number"
	case json.Number:
		if n, err := v.Float64(); err == nil {
			if n == math.Trunc(n) {
				return "integer"
			}
			return "number"
		}
	}
	return ""
}

func (p *argumentSchema) transform(value any, upstream bool) (any, error) {
	if len(p.scalarVariants) > 0 {
		if value == nil && p.nullable {
			return nil, nil
		}
		if upstream {
			kind := scalarKind(value)
			if kind == "integer" && p.scalarVariants[kind] == nil {
				kind = "number"
			}
			if p.scalarVariants[kind] == nil {
				return nil, fmt.Errorf("invalid GigaChat scalar union argument type")
			}
			return map[string]any{kind: cloneAny(value)}, nil
		}
		obj, ok := value.(map[string]any)
		if !ok || len(obj) != 1 {
			return nil, fmt.Errorf("GigaChat scalar union requires exactly one typed value")
		}
		for kind, scalar := range obj {
			actual := scalarKind(scalar)
			if p.scalarVariants[kind] == nil || (actual != kind && !(kind == "number" && actual == "integer")) {
				return nil, fmt.Errorf("invalid GigaChat scalar union argument type")
			}
			return cloneAny(scalar), nil
		}
	}
	if obj, ok := value.(map[string]any); ok {
		result := make(map[string]any, len(obj))
		for name, v := range obj {
			child := p.properties[name]
			if child == nil {
				child = p.additional
			}
			if upstream && child != nil && child.nullable && v == nil {
				continue
			}
			if child == nil {
				result[name] = cloneAny(v)
				continue
			}
			converted, err := child.transform(v, upstream)
			if err != nil {
				return nil, err
			}
			result[name] = converted
		}
		if !upstream {
			for name := range p.requiredNulls {
				if _, exists := result[name]; !exists {
					result[name] = nil
				}
			}
		}
		return result, nil
	}
	if values, ok := asSlice(value); ok && p.items != nil {
		result := make([]any, len(values))
		for i, v := range values {
			converted, err := p.items.transform(v, upstream)
			if err != nil {
				return nil, err
			}
			result[i] = converted
		}
		return result, nil
	}
	return cloneAny(value), nil
}

func (p *argumentSchema) arguments(value any, upstream bool) (any, error) {
	if text, ok := value.(string); ok {
		var parsed any
		decoder := json.NewDecoder(strings.NewReader(text))
		decoder.UseNumber()
		if decoder.Decode(&parsed) != nil || !json.Valid([]byte(text)) {
			return text, nil // Do not manufacture arguments from malformed JSON.
		}
		converted, err := p.transform(parsed, upstream)
		if err != nil {
			return nil, err
		}
		data, err := json.Marshal(converted)
		return string(data), err
	}
	return p.transform(value, upstream)
}

func adaptSchema(schema map[string]any) (*argumentSchema, error) {
	root := cloneMap(schema)
	var compile func(map[string]any, map[string]bool) (*argumentSchema, error)
	compile = func(source map[string]any, refs map[string]bool) (*argumentSchema, error) {
		node := cloneMap(source)
		if refValue, exists := node["$ref"]; exists {
			ref, ok := refValue.(string)
			if !ok || !strings.HasPrefix(ref, "#/") || refs[ref] {
				return nil, fmt.Errorf("GigaChat tool schemas require non-recursive local references")
			}
			var target any = root
			for _, part := range strings.Split(ref[2:], "/") {
				object, ok := target.(map[string]any)
				if !ok {
					return nil, fmt.Errorf("unresolved GigaChat tool schema reference: %s", ref)
				}
				target = object[strings.ReplaceAll(strings.ReplaceAll(part, "~1", "/"), "~0", "~")]
			}
			object, ok := target.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("unresolved GigaChat tool schema reference: %s", ref)
			}
			merged := cloneMap(object)
			for key, value := range node {
				if key != "$ref" {
					merged[key] = value
				}
			}
			next := make(map[string]bool, len(refs)+1)
			for ref := range refs {
				next[ref] = true
			}
			next[ref] = true
			return compile(merged, next)
		}
		delete(node, "$defs")
		delete(node, "definitions")
		if format, ok := node["format"].(string); ok && format != "date" && format != "date-time" && format != "time" {
			delete(node, "format")
			node["description"] = strings.TrimSpace(stringOrEmpty(node["description"]) + " Format: " + format + ".")
		}
		constant, hasConstant := node["const"]
		constNull := hasConstant && constant == nil
		nullable := node["nullable"] == true || constNull || node["type"] == "null"
		if types, ok := asSlice(node["type"]); ok && len(types) > 0 {
			var nonNull []any
			for _, kind := range types {
				if kind == "null" {
					nullable = true
				} else {
					nonNull = append(nonNull, kind)
				}
			}
			switch len(nonNull) {
			case 0:
				node["type"] = "null"
			case 1:
				node["type"] = nonNull[0]
			default:
				node["type"] = nonNull
			}
		}
		enum, isEnum := asSlice(node["enum"])
		if isEnum {
			var values []any
			for _, value := range enum {
				if value == nil {
					nullable = true
				} else {
					values = append(values, value)
				}
			}
			if len(values) != len(enum) {
				node["enum"] = values
				if _, exists := node["type"]; !exists && len(values) > 0 {
					kind := scalarKind(values[0])
					if _, ok := values[0].(map[string]any); ok {
						kind = "object"
					}
					if _, ok := asSlice(values[0]); ok {
						kind = "array"
					}
					for _, v := range values[1:] {
						other := scalarKind(v)
						if _, ok := v.(map[string]any); ok {
							other = "object"
						}
						if _, ok := asSlice(v); ok {
							other = "array"
						}
						if (kind == "integer" || kind == "number") && (other == "integer" || other == "number") {
							if other == "number" {
								kind = "number"
							}
						} else if other != kind {
							kind = ""
							break
						}
					}
					if kind != "" {
						node["type"] = kind
					}
				}
			}
		}
		for _, key := range []string{"anyOf", "oneOf", "allOf"} {
			variants, ok := asSlice(node[key])
			if !ok {
				continue
			}
			var compiled, nonNull []*argumentSchema
			valid := true
			for _, value := range variants {
				branch, ok := value.(map[string]any)
				if !ok {
					valid = false
					break
				}
				plan, err := compile(branch, refs)
				if err != nil {
					return nil, err
				}
				compiled = append(compiled, plan)
				if !plan.nullOnly {
					nonNull = append(nonNull, plan)
				}
				if key != "allOf" && plan.nullable {
					nullable = true
				}
			}
			if !valid {
				continue
			}
			if len(nonNull) == 1 && (key != "allOf" || len(compiled) == 1) {
				var branch map[string]any
				for i, plan := range compiled {
					if !plan.nullOnly {
						branch = cloneMap(variants[i].(map[string]any))
						break
					}
				}
				for name, value := range node {
					if name != key {
						branch[name] = value
					}
				}
				plan, err := compile(branch, refs)
				if err != nil {
					return nil, err
				}
				plan.nullable = plan.nullable || nullable
				if plan.nullable {
					delete(plan.schema, "nullable")
					if plan.schema["default"] == nil {
						delete(plan.schema, "default")
					}
				}
				return plan, nil
			}
			if len(nonNull) == 0 && key != "allOf" {
				return &argumentSchema{schema: map[string]any{"type": "null"}, nullable: true, nullOnly: true}, nil
			}
			if key != "allOf" && len(nonNull) >= 2 {
				plan, err := scalarUnion(node, key, nonNull, nullable)
				if err != nil || plan != nil {
					return plan, err
				}
			}
			for _, plan := range compiled {
				if plan.needsRestoration() || plan.nullable {
					return nil, fmt.Errorf("GigaChat nullable tool schemas require a single non-null union variant")
				}
			}
			normalized := make([]any, len(compiled))
			for i, plan := range compiled {
				normalized[i] = plan.schema
			}
			node[key] = normalized
		}
		plan := &argumentSchema{schema: node, nullable: nullable, nullOnly: constNull || node["type"] == "null" || (isEnum && len(enum) > 0 && lenSlice(node["enum"]) == 0), properties: map[string]*argumentSchema{}, requiredNulls: map[string]bool{}}
		if nullable {
			delete(node, "nullable")
			if node["default"] == nil {
				delete(node, "default")
			}
		}
		if properties, ok := node["properties"].(map[string]any); ok {
			required, requiredList := asSlice(node["required"])
			for name, value := range properties {
				child, ok := value.(map[string]any)
				if !ok {
					continue
				}
				adapted, err := compile(child, refs)
				if err != nil {
					return nil, err
				}
				plan.properties[name] = adapted
				if adapted.nullable {
					for _, nameValue := range required {
						if nameValue == name {
							plan.requiredNulls[name] = true
						}
					}
				}
				if adapted.nullOnly {
					delete(properties, name)
				} else {
					properties[name] = adapted.schema
				}
			}
			if requiredList {
				filtered := []any{}
				for _, name := range required {
					if text, ok := name.(string); !ok || !plan.requiredNulls[text] {
						filtered = append(filtered, name)
					}
				}
				node["required"] = filtered
			}
		}
		for _, key := range []string{"items", "additionalProperties"} {
			if child, ok := node[key].(map[string]any); ok {
				adapted, err := compile(child, refs)
				if err != nil {
					return nil, err
				}
				node[key] = adapted.schema
				if key == "items" {
					plan.items = adapted
				} else {
					plan.additional = adapted
				}
			}
		}
		return plan, nil
	}
	return compile(root, map[string]bool{})
}

func lenSlice(value any) int { values, _ := asSlice(value); return len(values) }

func scalarUnion(node map[string]any, key string, variants []*argumentSchema, nullable bool) (*argumentSchema, error) {
	kinds := make([]string, len(variants))
	seen := map[string]bool{}
	for i, variant := range variants {
		kind, _ := variant.schema["type"].(string)
		if kind != "string" && kind != "integer" && kind != "number" && kind != "boolean" {
			return nil, nil
		}
		if seen[kind] {
			return nil, fmt.Errorf("GigaChat scalar union branches must have distinct types")
		}
		seen[kind] = true
		kinds[i] = kind
	}
	if key == "oneOf" && seen["integer"] && seen["number"] {
		return nil, fmt.Errorf("GigaChat scalar union branches must have distinct types")
	}
	for name := range node {
		if name != key && name != "title" && name != "description" && name != "default" && name != "examples" && name != "nullable" {
			return nil, fmt.Errorf("unsupported constraints on GigaChat scalar union")
		}
	}
	plan := &argumentSchema{nullable: nullable, scalarVariants: map[string]*argumentSchema{}}
	properties := map[string]any{}
	for i, variant := range variants {
		if variant.needsRestoration() {
			return nil, fmt.Errorf("unsupported constraints on GigaChat scalar union")
		}
		for _, name := range []string{"anyOf", "oneOf", "allOf", "not"} {
			if _, exists := variant.schema[name]; exists {
				return nil, fmt.Errorf("unsupported constraints on GigaChat scalar union")
			}
		}
		property := cloneMap(variant.schema)
		if _, exists := property["description"]; !exists {
			property["description"] = fmt.Sprintf("The %s value. Set only this member when choosing %s.", kinds[i], kinds[i])
		}
		properties[kinds[i]] = property
		plan.scalarVariants[kinds[i]] = variant
	}
	plan.schema = map[string]any{"type": "object", "properties": properties, "additionalProperties": false, "description": strings.TrimSpace(stringOrEmpty(node["description"]) + " Provide exactly one member: " + strings.Join(kinds, ", ") + ". Do not set the other members.")}
	if title, exists := node["title"]; exists {
		plan.schema["title"] = title
	}
	if value, exists := node["default"]; exists && value != nil {
		converted, err := plan.transform(value, true)
		if err != nil {
			return nil, err
		}
		plan.schema["default"] = converted
	}
	if examples, ok := asSlice(node["examples"]); ok {
		values := []any{}
		for _, value := range examples {
			if value != nil {
				converted, err := plan.transform(value, true)
				if err != nil {
					return nil, err
				}
				values = append(values, converted)
			}
		}
		plan.schema["examples"] = values
	}
	return plan, nil
}

type toolArguments struct {
	plans  map[string]*argumentSchema
	legacy bool
}

func (a *toolArguments) needsRestoration() bool {
	for _, plan := range a.plans {
		if plan.needsRestoration() {
			return true
		}
	}
	return false
}

func prepareToolArguments(payload map[string]any) (map[string]any, *toolArguments, error) {
	prepared := cloneMap(payload)
	_, hasTools := prepared["tools"]
	functions, hasFunctions := asSlice(prepared["functions"])
	a := &toolArguments{plans: map[string]*argumentSchema{}, legacy: !hasTools && hasFunctions}
	if a.legacy {
		tools := make([]any, len(functions))
		for i, spec := range functions {
			tools[i] = map[string]any{"type": "function", "function": spec}
		}
		prepared["tools"] = tools
		delete(prepared, "functions")
		choice, exists := prepared["function_call"]
		if !exists {
			choice = "auto"
		}
		delete(prepared, "function_call")
		if obj, ok := choice.(map[string]any); ok {
			prepared["tool_choice"] = map[string]any{"type": "function", "function": obj}
		} else {
			prepared["tool_choice"] = choice
		}
	}
	tools, _ := asSlice(prepared["tools"])
	for _, value := range tools {
		tool, ok := value.(map[string]any)
		if !ok {
			continue
		}
		if kind, exists := tool["type"]; exists && kind != "function" {
			continue
		}
		spec, _ := tool["function"].(map[string]any)
		name, ok := spec["name"].(string)
		if !ok {
			continue
		}
		schema, ok := spec["parameters"].(map[string]any)
		if !ok {
			continue
		}
		plan, err := adaptSchema(schema)
		if err != nil {
			return nil, nil, err
		}
		a.plans[name] = plan
		spec["parameters"] = plan.schema
	}
	messages, _ := asSlice(prepared["messages"])
	for _, value := range messages {
		message, ok := value.(map[string]any)
		if !ok {
			continue
		}
		var calls []map[string]any
		toolCalls, _ := asSlice(message["tool_calls"])
		for _, value := range toolCalls {
			call, _ := value.(map[string]any)
			if function, ok := call["function"].(map[string]any); ok {
				calls = append(calls, function)
			}
		}
		if function, ok := message["function_call"].(map[string]any); ok {
			calls = append(calls, function)
			if _, exists := message["tool_calls"]; !exists {
				message["tool_calls"] = []any{map[string]any{"type": "function", "function": function}}
				delete(message, "function_call")
			}
		}
		for _, call := range calls {
			plan := a.plans[stringOrEmpty(call["name"])]
			value, exists := call["arguments"]
			if plan == nil || !exists {
				continue
			}
			converted, err := plan.arguments(value, true)
			if err != nil {
				return nil, nil, err
			}
			call["arguments"] = converted
		}
	}
	return prepared, a, nil
}

func (a *toolArguments) restoreCall(call map[string]any) (map[string]any, error) {
	result := cloneMap(call)
	if plan := a.plans[stringOrEmpty(result["name"])]; plan != nil {
		value, exists := result["arguments"]
		if !exists {
			value = map[string]any{}
		}
		converted, err := plan.arguments(value, false)
		if err != nil {
			return nil, err
		}
		result["arguments"] = converted
	}
	return result, nil
}

func (a *toolArguments) restoreResponse(response map[string]any, convertLegacy bool) (map[string]any, error) {
	result := cloneMap(response)
	choices, _ := asSlice(result["choices"])
	for _, value := range choices {
		choice, _ := value.(map[string]any)
		message, _ := choice["message"].(map[string]any)
		calls, _ := asSlice(message["tool_calls"])
		for _, value := range calls {
			call, _ := value.(map[string]any)
			if function, ok := call["function"].(map[string]any); ok {
				restored, err := a.restoreCall(function)
				if err != nil {
					return nil, err
				}
				call["function"] = restored
			}
		}
	}
	if convertLegacy {
		return a.clientFormat(result, false), nil
	}
	return result, nil
}

func (a *toolArguments) clientFormat(response map[string]any, streaming bool) map[string]any {
	result := cloneMap(response)
	if !a.legacy {
		return result
	}
	key := "message"
	if streaming {
		key = "delta"
	}
	choices, _ := asSlice(result["choices"])
	for _, value := range choices {
		choice, _ := value.(map[string]any)
		message, _ := choice[key].(map[string]any)
		calls, _ := asSlice(message["tool_calls"])
		if len(calls) > 0 {
			call, _ := calls[0].(map[string]any)
			message["function_call"] = call["function"]
			delete(message, "tool_calls")
		}
		if choice["finish_reason"] == "tool_calls" {
			choice["finish_reason"] = "function_call"
		}
	}
	return result
}
