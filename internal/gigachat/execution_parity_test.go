package gigachat

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"
)

type parityTransport func(*http.Request) (*http.Response, error)

func (f parityTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestExecuteRestoresToolArgumentsAcrossClientFormats(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		for _, emulated := range []bool{false, true} {
			for _, legacy := range []bool{false, true} {
				name := strings.Join([]string{map[bool]string{true: "stream", false: "json"}[streaming], map[bool]string{true: "emulated", false: "native"}[emulated], map[bool]string{true: "legacy", false: "tools"}[legacy]}, "_")
				t.Run(name, func(t *testing.T) {
					function := schemaJSON(t, `{"name":"lookup","parameters":{"type":"object","properties":{"optional":{"type":["string","null"]},"value":{"anyOf":[{"type":"string"},{"type":"integer"}]}},"required":["optional","value"]}}`)
					payload := map[string]any{"model": "giga", "stream": streaming, "messages": []any{map[string]any{"role": "user", "content": "look up"}}}
					if legacy {
						payload["functions"] = []any{function}
						payload["function_call"] = map[string]any{"name": "lookup"}
					} else {
						payload["tools"] = []any{map[string]any{"type": "function", "function": function}}
					}
					before := cloneMap(payload)
					calls := 0
					client := &http.Client{Transport: parityTransport(func(req *http.Request) (*http.Response, error) {
						calls++
						var request map[string]any
						if err := json.NewDecoder(req.Body).Decode(&request); err != nil {
							t.Fatal(err)
						}
						if !emulated {
							functions := request["functions"].([]any)
							schema := functions[0].(map[string]any)["parameters"].(map[string]any)
							if schema["properties"].(map[string]any)["value"].(map[string]any)["type"] != "object" || !reflect.DeepEqual(schema["required"], []any{"value"}) {
								t.Fatalf("upstream schema = %#v", schema)
							}
						}
						body := `{"choices":[{"message":{"function_call":{"name":"lookup","arguments":{"value":{"integer":7}}}},"finish_reason":"tool_call"}],"usage":{"total_tokens":13}}`
						if emulated {
							body = `{"choices":[{"message":{"content":"{\"action\":\"call_tool\",\"tool\":\"lookup\",\"arguments\":{\"value\":{\"integer\":7}}}"},"finish_reason":"stop"}],"usage":{"total_tokens":13}}`
						} else if streaming {
							body = "data: {\"choices\":[{\"index\":0,\"delta\":{\"function_call\":{\"name\":\"lookup\",\"arguments\":\"{\\\"value\\\":\"}}}]}\r\n\r\n" +
								"data: {\"choices\":[{\"index\":0,\"delta\":{\"function_call\":{\"arguments\":\"{\\\"integer\\\":7}}\"}}}]}\r\n\r\n" +
								"data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"tool_call\"}],\"usage\":{\"total_tokens\":13}}\r\n\r\n" +
								"data: [DONE]\r\n\r\n"
						}
						var readers []io.Reader
						for _, piece := range strings.Split(body, "") {
							readers = append(readers, strings.NewReader(piece))
						}
						return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(io.MultiReader(readers...))}, nil
					})}
					backend := NewBackend(Config{Mode: "local", BaseURL: "https://giga.test/api/v1", ModelName: "giga", EmulateToolCalls: emulated})
					resp, err := backend.Execute(context.Background(), payload, streaming, client)
					if err != nil {
						t.Fatal(err)
					}
					defer resp.Body.Close()
					body, err := io.ReadAll(resp.Body)
					if err != nil || resp.StatusCode != 200 || calls != 1 {
						t.Fatalf("status=%d calls=%d err=%v body=%s", resp.StatusCode, calls, err, body)
					}
					var events []map[string]any
					if streaming {
						var decoder sseDecoder
						for _, event := range decoder.feed(body) {
							if event.data != "[DONE]" {
								events = append(events, schemaJSON(t, event.data))
							}
						}
					} else {
						events = []map[string]any{schemaJSON(t, string(body))}
					}
					found := 0
					for _, event := range events {
						choices, _ := asSlice(event["choices"])
						for _, raw := range choices {
							choice := raw.(map[string]any)
							key := "message"
							if streaming {
								key = "delta"
							}
							message := choice[key].(map[string]any)
							var call map[string]any
							if legacy {
								call, _ = message["function_call"].(map[string]any)
								if message["tool_calls"] != nil {
									t.Fatal("legacy request received tool_calls")
								}
							} else if calls, ok := asSlice(message["tool_calls"]); ok && len(calls) > 0 {
								call = calls[0].(map[string]any)["function"].(map[string]any)
							}
							if call != nil {
								found++
								arguments := schemaJSON(t, call["arguments"].(string))
								if call["name"] != "lookup" || !reflect.DeepEqual(arguments, map[string]any{"optional": nil, "value": float64(7)}) {
									t.Fatalf("call = %#v arguments=%#v", call, arguments)
								}
							}
						}
					}
					if found != 1 || !reflect.DeepEqual(payload, before) {
						t.Fatalf("calls=%d request mutated=%v body=%s", found, !reflect.DeepEqual(payload, before), body)
					}
				})
			}
		}
	}
}

type singleEventBody struct {
	body   string
	read   bool
	closed bool
}

func (b *singleEventBody) Read(p []byte) (int, error) {
	if b.read {
		panic("upstream read after DONE")
	}
	b.read = true
	return copy(p, b.body), nil
}

func (b *singleEventBody) Close() error { b.closed = true; return nil }

func TestPrimedStreamDoneDoesNotWaitForUpstreamEOF(t *testing.T) {
	source := &singleEventBody{body: "data: {\"choices\":[{\"delta\":{\"content\":\"done\"}}]}\n\ndata: [DONE]\n\n"}
	stream, err := primeStreamReadCloser(context.Background(), source, "giga")
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(stream)
	if err != nil || !strings.Contains(string(body), `"content":"done"`) || strings.Count(string(body), "[DONE]") != 1 {
		t.Fatalf("err=%v body=%s", err, body)
	}
	if err := stream.Close(); err != nil || !source.closed {
		t.Fatalf("close=%v closed=%v", err, source.closed)
	}
}

func TestToolStreamRestoresPendingCallAtDoneAndEOF(t *testing.T) {
	payload := schemaJSON(t, `{"tools":[{"type":"function","function":{"name":"lookup","parameters":{"type":"object","properties":{"optional":{"type":["string","null"]}},"required":["optional"]}}}]}`)
	_, plan, err := prepareToolArguments(payload)
	if err != nil {
		t.Fatal(err)
	}
	for _, done := range []string{"", "data: [DONE]\n\n"} {
		t.Run("ending_"+done, func(t *testing.T) {
			body := "data: {\"choices\":[{\"index\":0,\"delta\":{\"function_call\":{\"name\":\"lookup\",\"arguments\":{}}}}]}\n\n" + done
			stream := newStreamReadCloser(io.NopCloser(strings.NewReader(body)), "giga", plan)
			defer stream.Close()
			out, err := io.ReadAll(stream)
			if err != nil || strings.Count(string(out), `\"optional\":null`) != 1 || strings.Count(string(out), "[DONE]") != 1 {
				t.Fatalf("err=%v body=%s", err, out)
			}
		})
	}
}
