package gigachat

import "sort"

// toolCallStream assembles native argument fragments before restoring the
// caller's schema. Text and usage remain incremental while tool JSON waits.
type toolCallStream struct {
	arguments *toolArguments
	model     string
	pending   map[int]map[string]any
	envelopes map[int]map[string]any
}

func newToolCallStream(arguments *toolArguments, model string) *toolCallStream {
	return &toolCallStream{arguments: arguments, model: model, pending: map[int]map[string]any{}, envelopes: map[int]map[string]any{}}
}

func (s *toolCallStream) convert(data map[string]any) map[string]any {
	converted := GigaChatChunkToOpenAI(data, s.model)
	if s.arguments != nil {
		converted = s.arguments.clientFormat(converted, true)
	}
	return converted
}

func (s *toolCallStream) feed(data map[string]any) ([]map[string]any, error) {
	if s.arguments == nil || (!s.arguments.legacy && !s.arguments.needsRestoration()) {
		return []map[string]any{s.convert(data)}, nil
	}
	data = cloneMap(data)
	choices, _ := asSlice(data["choices"])
	if len(choices) == 0 {
		return []map[string]any{s.convert(data)}, nil
	}
	var out []map[string]any
	for position, raw := range choices {
		choice, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		index := position
		if number, ok := choice["index"].(float64); ok {
			index = int(number)
		} else if number, ok := choice["index"].(int); ok {
			index = number
		}
		delta, _ := choice["delta"].(map[string]any)
		if delta == nil {
			delta, _ = choice["message"].(map[string]any)
		}
		if call, ok := delta["function_call"].(map[string]any); ok {
			pending := s.pending[index]
			if pending == nil {
				pending = map[string]any{}
				s.pending[index] = pending
			}
			envelope := cloneMap(data)
			delete(envelope, "choices")
			delete(envelope, "usage")
			s.envelopes[index] = envelope
			if name := stringOrEmpty(call["name"]); name != "" {
				previous := stringOrEmpty(pending["name"])
				if previous == "" || previous == name {
					pending["name"] = name
				} else {
					pending["name"] = previous + name
				}
			}
			if value, exists := call["arguments"]; exists {
				switch typed := value.(type) {
				case string:
					if previous, ok := pending["arguments"].(string); ok {
						value = previous + typed
					}
				case map[string]any:
					if previous, ok := pending["arguments"].(map[string]any); ok {
						for key, item := range typed {
							previous[key] = item
						}
						value = previous
					}
				}
				pending["arguments"] = value
			}
			delete(delta, "function_call")
		}
		finish := choice["finish_reason"]
		if s.pending[index] != nil {
			if len(delta) > 0 {
				textData := cloneMap(data)
				textData["choices"] = []any{map[string]any{"index": index, "delta": delta, "finish_reason": nil}}
				if finish != nil {
					delete(textData, "usage")
				}
				out = append(out, s.convert(textData))
			}
			if finish != nil {
				completed, err := s.finishChoice(index, finish, data["usage"])
				if err != nil {
					return out, err
				}
				out = append(out, completed)
			}
		} else {
			item := cloneMap(data)
			item["choices"] = []any{choice}
			out = append(out, s.convert(item))
		}
	}
	return out, nil
}

func (s *toolCallStream) finishChoice(index int, reason any, usage any) (map[string]any, error) {
	call, err := s.arguments.restoreCall(s.pending[index])
	if err != nil {
		return nil, err
	}
	envelope := s.envelopes[index]
	delete(s.pending, index)
	delete(s.envelopes, index)
	envelope["choices"] = []any{map[string]any{"index": index, "delta": map[string]any{"function_call": call}, "finish_reason": reason}}
	if usage != nil {
		envelope["usage"] = usage
	}
	return s.convert(envelope), nil
}

func (s *toolCallStream) finish() ([]map[string]any, error) {
	indexes := make([]int, 0, len(s.pending))
	for index := range s.pending {
		indexes = append(indexes, index)
	}
	sort.Ints(indexes)
	var out []map[string]any
	for _, index := range indexes {
		completed, err := s.finishChoice(index, "tool_call", nil)
		if err != nil {
			return out, err
		}
		out = append(out, completed)
	}
	return out, nil
}
