package proxy

import (
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"strings"
)

// adaptEngineRequest operates only on the private per-attempt payload. Unknown
// extensions are retained; only successfully translated aliases are removed.
func adaptEngineRequest(payload map[string]any, b Backend, path string) (bool, []RequestWarning, error) {
	path = engineEndpointPath(path)
	if !b.Compatibility.IsEnabled() || (b.APIType != "" && b.APIType != "openai") || (path != "/v1/chat/completions" && path != "/v1/completions") {
		return false, nil, nil
	}
	b = b.engineSnapshot()
	p := b.EngineProfile
	var warnings []RequestWarning
	changed := false
	unsupported := func(field, message string) error {
		if b.Compatibility.IsStrict() {
			return &CompatibilityError{Message: message, Unsupported: true}
		}
		warnings = append(warnings, RequestWarning{Code: "engine_capability_unavailable", Severity: "warning", Path: field, Message: message})
		return nil
	}
	translated := func(from []string, to string) {
		warnings = append(warnings, RequestWarning{Code: "engine_parameter_translated", Severity: "info", Path: to, Message: fmt.Sprintf("Adapted request fields for %s", p.Engine), Details: map[string]any{"from": from, "to": to, "engine": p.Engine}})
	}
	groups := [][]string{{"thinking_token_budget", "custom_params.thinking_budget"}, {"regex", "guided_regex", "structured_outputs.regex"}, {"json_schema", "guided_json", "structured_outputs.json"}}
	if p.Engine != "vllm" && p.Engine != "sglang" {
		if b.Compatibility.DPRank != nil {
			return false, nil, &CompatibilityError{Message: "dp_rank requires a known engine", Unsupported: true}
		}
		for _, aliases := range groups {
			for _, alias := range aliases {
				if value, exists := getPath(payload, alias); exists && value != nil {
					err := unsupported(alias, "Engine is unknown or generic; cannot adapt "+alias)
					return false, warnings, err
				}
			}
		}
		return false, nil, nil
	}
	for groupIndex, aliases := range groups {
		var value any
		var present []string
		for _, alias := range aliases {
			v, ok := getPath(payload, alias)
			if !ok || v == nil {
				continue
			}
			if groupIndex == 2 {
				if str, ok := v.(string); ok {
					if json.Unmarshal([]byte(str), &v) != nil {
						return false, warnings, compatibilityConflict("%s must contain a valid JSON schema", alias)
					}
				}
			}
			if len(present) > 0 && !valuesEqual(value, v) {
				return false, warnings, compatibilityConflict("conflicting aliases %s and %s", present[0], alias)
			}
			value = v
			present = append(present, alias)
		}
		if len(present) == 0 {
			continue
		}

		target := ""
		outValue := value
		switch groupIndex {
		case 0:
			number, ok := numericBudget(value)
			if !ok {
				return false, warnings, compatibilityConflict("thinking budget must be an integer")
			}
			if number < 0 {
				if p.Engine == "vllm" && number == -1 && len(present) == 1 && present[0] == "thinking_token_budget" {
					continue
				}
				if err := unsupported(present[0], "Negative thinking-budget sentinels have no portable mapping"); err != nil {
					return false, warnings, err
				}
				continue
			}
			if p.ThinkingBudget == nil || !*p.ThinkingBudget {
				message := "Thinking-budget enforcement is not verified for this node"
				if p.ThinkingBudget != nil {
					message = "Thinking-budget enforcement is disabled on this node"
				}
				if err := unsupported(present[0], message); err != nil {
					return false, warnings, err
				}
				// Known-disabled enforcement must not look like a successful translation.
				if p.ThinkingBudget != nil {
					continue
				}
			}
			target = "thinking_token_budget"
			if p.Engine == "sglang" {
				target = "custom_params.thinking_budget"
			}
		case 1:
			if _, ok := value.(string); !ok {
				return false, warnings, compatibilityConflict("regex constraint must be a string")
			}
			for _, other := range []string{"json_schema", "guided_json", "structured_outputs.json", "response_format"} {
				if v, ok := getPath(payload, other); ok && v != nil {
					return false, warnings, compatibilityConflict("regex and JSON constraints cannot be combined")
				}
			}
			target = "regex"
			if p.Engine == "vllm" {
				target = "structured_outputs.regex"
				if !p.supports(path, "structured_outputs") && p.supports(path, "guided_regex") {
					target = "guided_regex"
				}
			}
		case 2:
			switch value.(type) {
			case map[string]any, bool:
			default:
				return false, warnings, compatibilityConflict("JSON schema must be an object or boolean")
			}
			target = "structured_outputs.json"
			if !p.supports(path, "structured_outputs") && p.supports(path, "guided_json") {
				target = "guided_json"
			}
			if p.Engine == "sglang" {
				if path == "/v1/chat/completions" {
					target = "response_format"
					outValue = map[string]any{"type": "json_schema", "json_schema": map[string]any{"name": "response", "schema": value}}
				} else {
					target = "json_schema"
					encoded, _ := json.Marshal(value)
					outValue = string(encoded)
				}
			}
			if existing, exists := payload["response_format"]; exists && existing != nil {
				format, _ := existing.(map[string]any)
				descriptor, _ := format["json_schema"].(map[string]any)
				if format["type"] != "json_schema" || !valuesEqual(descriptor["schema"], value) {
					return false, warnings, compatibilityConflict("JSON schema alias conflicts with response_format")
				}
				target, outValue = "response_format", existing
			}
		}
		root := strings.SplitN(target, ".", 2)[0]
		if !p.supports(path, root) {
			if err := unsupported(present[0], "Destination does not advertise support for "+target); err != nil {
				return false, warnings, err
			}
			continue
		}
		// Do not partially convert an engine-specific constraint object: options
		// such as whitespace rules or grammar dialects have no guaranteed equivalent.
		if groupIndex > 0 && !strings.HasPrefix(target, "structured_outputs.") {
			if structured, ok := payload["structured_outputs"].(map[string]any); ok && len(structured) > 1 {
				if err := unsupported("structured_outputs", "Cannot translate structured_outputs with additional constraint options"); err != nil {
					return false, warnings, err
				}
				continue
			}
		}
		if strings.Contains(target, ".") {
			if existing, ok := payload[root]; ok && existing != nil {
				if _, ok := existing.(map[string]any); !ok {
					return false, warnings, compatibilityConflict("%s must be an object to set %s", root, target)
				}
			}
		}
		needsChange := len(present) != 1 || present[0] != target
		current, exists := getPath(payload, target)
		if !exists || !valuesEqual(current, outValue) {
			needsChange = true
		}
		if !needsChange {
			continue
		}
		for _, alias := range aliases {
			if alias != target {
				deleteEnginePath(payload, alias)
			}
		}
		setPath(payload, target, outValue)
		changed = true
		translated(present, target)
	}
	if rank := b.Compatibility.DPRank; rank != nil {
		if p.Engine != "vllm" && p.Engine != "sglang" {
			return false, warnings, &CompatibilityError{Message: "dp_rank requires a known engine", Unsupported: true}
		}
		if p.DPSize <= 0 || *rank >= p.DPSize {
			return false, warnings, &CompatibilityError{Message: "dp_rank requires a known dp_size greater than the configured rank", Unsupported: true}
		}
		if p.Engine == "sglang" {
			if !p.supports(path, "routed_dp_rank") {
				return false, warnings, &CompatibilityError{Message: "This SGLang endpoint does not advertise routed_dp_rank; leave routing upstream-managed", Unsupported: true}
			}
			payload["routed_dp_rank"] = *rank
			changed = true
		} else if _, exists := payload["routed_dp_rank"]; exists {
			delete(payload, "routed_dp_rank")
			changed = true
		}
	}
	return changed, warnings, nil
}

func numericBudget(v any) (float64, bool) {
	var n float64
	switch x := v.(type) {
	case float64:
		n = x
	case int:
		n = float64(x)
	case int64:
		n = float64(x)
	case json.Number:
		var err error
		n, err = x.Float64()
		if err != nil {
			return 0, false
		}
	default:
		return 0, false
	}
	return n, !math.IsInf(n, 0) && !math.IsNaN(n) && n == math.Trunc(n)
}

func deleteEnginePath(payload map[string]any, path string) {
	parts := strings.SplitN(path, ".", 2)
	if len(parts) == 1 {
		delete(payload, path)
		return
	}
	if nested, ok := payload[parts[0]].(map[string]any); ok {
		delete(nested, parts[1])
		if len(nested) == 0 {
			delete(payload, parts[0])
		}
	}
}

func applyEngineRoutingHeaders(headers http.Header, b Backend, path string) {
	path = engineEndpointPath(path)
	if !b.Compatibility.IsEnabled() || b.Compatibility.DPRank == nil || (b.APIType != "" && b.APIType != "openai") || (path != "/v1/chat/completions" && path != "/v1/completions") {
		return
	}
	b = b.engineSnapshot()
	headers.Del("X-data-parallel-rank")
	headers.Del("x-override-routed-dp-rank")
	if b.EngineProfile.Engine == "vllm" {
		headers.Set("X-data-parallel-rank", strconv.Itoa(*b.Compatibility.DPRank))
	}
}

func engineEndpointPath(path string) string {
	if path == "/chat/completions" || path == "/completions" {
		return "/v1" + path
	}
	return path
}
