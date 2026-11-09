package server

import (
	"encoding/json"
	"fmt"
	"math"
)

func integerValue(value any) (int64, bool) {
	switch v := value.(type) {
	case int:
		return int64(v), true
	case int64:
		return v, true
	case json.Number:
		n, err := v.Int64()
		return n, err == nil
	case float64:
		if math.IsNaN(v) || math.IsInf(v, 0) || v < math.MinInt64 || v >= math.MaxInt64 || math.Trunc(v) != v {
			return 0, false
		}
		return int64(v), true
	}
	return 0, false
}

func nonStreaming(payload map[string]any) error {
	if value, exists := payload["stream"]; exists && value != false {
		return fmt.Errorf("this endpoint does not support streaming")
	}
	return nil
}

func validateEmbeddings(payload map[string]any) error {
	if err := nonStreaming(payload); err != nil {
		return err
	}
	input, exists := payload["input"]
	if !exists {
		return fmt.Errorf("You must provide an input parameter")
	}
	validTokens := func(values []any) bool {
		if len(values) == 0 {
			return false
		}
		for _, value := range values {
			n, ok := integerValue(value)
			if !ok || n < 0 {
				return false
			}
		}
		return true
	}
	switch v := input.(type) {
	case string:
		if v == "" {
			return fmt.Errorf("input must not be empty")
		}
	case []any:
		if len(v) == 0 {
			return fmt.Errorf("input must not be empty")
		}
		switch v[0].(type) {
		case string:
			for _, item := range v {
				if s, ok := item.(string); !ok || s == "" {
					return fmt.Errorf("input must contain only non-empty strings")
				}
			}
		case []any:
			for _, item := range v {
				if tokens, ok := item.([]any); !ok || !validTokens(tokens) {
					return fmt.Errorf("input must contain only non-empty integer token arrays")
				}
			}
		default:
			if !validTokens(v) {
				return fmt.Errorf("input token IDs must be nonnegative integers")
			}
		}
	default:
		return fmt.Errorf("input must be a string, string array, token array, or array of token arrays")
	}
	if value, exists := payload["dimensions"]; exists {
		if n, ok := integerValue(value); !ok || n <= 0 {
			return fmt.Errorf("dimensions must be a positive integer")
		}
	}
	if value, exists := payload["encoding_format"]; exists && value != "float" && value != "base64" {
		return fmt.Errorf("encoding_format must be float or base64")
	}
	return nil
}

// Native question bodies stay extensible. Detailed provider limits and optional
// criteria remain upstream-owned, avoiding accidental rejection of new features.
func validateSystemOne(payload map[string]any) error {
	if err := nonStreaming(payload); err != nil {
		return err
	}
	switch payload["state"].(type) {
	case string, map[string]any, []any:
	default:
		return fmt.Errorf("state must be a string, object, or array")
	}
	questions, ok := payload["questions"].(map[string]any)
	if !ok || len(questions) == 0 {
		return fmt.Errorf("questions must be a non-empty object of named questions")
	}
	for name, raw := range questions {
		q, ok := raw.(map[string]any)
		if !ok || name == "" {
			return fmt.Errorf("questions must be named objects")
		}
		switch q["type"] {
		case "noul", "choice", "score", "box":
		default:
			return fmt.Errorf("question %q has an unsupported type", name)
		}
	}
	return nil
}

func validateDecisions(payload map[string]any) error {
	if err := nonStreaming(payload); err != nil {
		return err
	}
	switch payload["input"].(type) {
	case string, []any, map[string]any:
	default:
		return fmt.Errorf("input must be a string, object, or array")
	}
	questions, ok := payload["questions"].([]any)
	if !ok || len(questions) == 0 {
		return fmt.Errorf("questions must be a non-empty array")
	}
	for i, raw := range questions {
		q, ok := raw.(map[string]any)
		if !ok {
			return fmt.Errorf("question %d must be an object", i)
		}
		switch q["type"] {
		case nil, "predicate", "choice", "score":
		default:
			return fmt.Errorf("question %d has an unsupported type", i)
		}
		switch q["instructions"].(type) {
		case string, []any:
		default:
			return fmt.Errorf("question %d instructions must be a string or array", i)
		}
	}
	return nil
}

// State can be arbitrary JSON, unlike a chat content block. Count keys and
// values so a large structured state or question rubric cannot reserve zero.
func estimateDecisionValueTokens(value any) int {
	switch v := value.(type) {
	case map[string]any:
		total := 0
		for key, item := range v {
			total += estimateStringTokens(key) + estimateDecisionValueTokens(item)
		}
		return total
	case []any:
		total := 0
		for _, item := range v {
			total += estimateDecisionValueTokens(item)
		}
		return total
	case nil:
		return 0
	default:
		return estimateStringTokens(fmt.Sprint(v))
	}
}
