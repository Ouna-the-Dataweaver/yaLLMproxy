package proxy

import (
	"bytes"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/theio/yallmp-proxy/internal/modules"
)

func TestStreamRewriteReadCloserFragmentedEvents(t *testing.T) {
	for _, newline := range []string{"\n", "\r\n", "\r"} {
		for _, multiline := range []bool{false, true} {
			for _, bufferSize := range []int{1, 512} {
				t.Run(fmt.Sprintf("newline_%q_multiline_%v_buffer_%d", newline, multiline, bufferSize), func(t *testing.T) {
					data := `data: {"choices":[{"index":0,"delta":{"reasoning_content":"hidden","content":"visible"}}]}`
					if multiline {
						data = strings.Replace(data, `"delta":`, newline+`data: "delta":`, 1)
					}
					body := ": keepalive" + newline + newline + "event: chunk" + newline + "id: 42" + newline + data + newline + newline + "data: [DONE]" + newline + newline
					var readers []io.Reader
					for _, chunk := range strings.Split(body, "") {
						readers = append(readers, strings.NewReader(chunk))
					}
					var raw, transformed bytes.Buffer
					pipeline := modules.Pipeline{Modules: []modules.ResponseModule{modules.NewReasoningSwap("reasoning_to_content")}}
					stream := &streamRewriteReadCloser{
						ReadCloser:         io.NopCloser(io.MultiReader(readers...)),
						pipeline:           pipeline,
						state:              pipeline.NewStreamState(),
						ctx:                modules.Context{Path: "/v1/chat/completions", IsStream: true},
						captureRaw:         &raw,
						captureTransformed: &transformed,
					}
					defer stream.Close()
					var out []byte
					buf := make([]byte, bufferSize)
					for {
						n, err := stream.Read(buf)
						out = append(out, buf[:n]...)
						if err == io.EOF {
							break
						}
						if err != nil || n == 0 {
							t.Fatalf("read made no progress: n=%d err=%v", n, err)
						}
					}
					var decoder SSEJSONDecoder
					payloads := decoder.Feed(out)
					payloads = append(payloads, decoder.Flush()...)
					if len(payloads) != 1 || !decoder.SawDone() {
						t.Fatalf("payloads = %#v, done = %v, body = %q", payloads, decoder.SawDone(), out)
					}
					delta := payloads[0]["choices"].([]any)[0].(map[string]any)["delta"].(map[string]any)
					if delta["content"] != "<think>hidden</think>\nvisible" || delta["reasoning_content"] != nil {
						t.Fatalf("delta = %#v", delta)
					}
					if !strings.Contains(string(out), "event: chunk\nid: 42\n") || !bytes.HasPrefix(out, []byte(": keepalive"+newline+newline)) {
						t.Fatalf("event metadata or heartbeat lost: %q", out)
					}
					if raw.String() != body || !bytes.Equal(transformed.Bytes(), out) {
						t.Fatalf("stream capture mismatch: raw=%q transformed=%q output=%q", raw.Bytes(), transformed.Bytes(), out)
					}
				})
			}
		}
	}
}

// terminalSSEReader returns data and the terminal error together, which is
// permitted by io.Reader and must not lose any buffered output.
type terminalSSEReader struct {
	*strings.Reader
	err error
}

func (r terminalSSEReader) Read(p []byte) (int, error) {
	n, err := r.Reader.Read(p)
	if r.Len() == 0 {
		return n, r.err
	}
	return n, err
}

func TestStreamRewriteReadCloserDrainsBeforeTerminalError(t *testing.T) {
	for _, terminalErr := range []error{io.EOF, io.ErrUnexpectedEOF} {
		for _, valid := range []bool{false, true} {
			t.Run(fmt.Sprintf("error_%v_valid_%v", terminalErr, valid), func(t *testing.T) {
				body := "data: incomplete"
				if valid {
					body = `data: {"choices":[{"delta":{"reasoning_content":"hidden"}}]}`
				}
				pipeline := modules.Pipeline{Modules: []modules.ResponseModule{modules.NewReasoningSwap("reasoning_to_content")}}
				stream := &streamRewriteReadCloser{
					ReadCloser: io.NopCloser(terminalSSEReader{strings.NewReader(body), terminalErr}),
					pipeline:   pipeline,
					state:      pipeline.NewStreamState(),
					ctx:        modules.Context{Path: "/v1/chat/completions", IsStream: true},
				}
				defer stream.Close()
				if n, err := stream.Read(nil); n != 0 || err != nil {
					t.Fatalf("empty read = %d, %v", n, err)
				}
				// Force expanded output to be drained after the upstream error.
				first := make([]byte, len(body))
				n, err := stream.Read(first)
				if n == 0 || err != nil {
					t.Fatalf("first read = %d, %v", n, err)
				}
				tail, err := io.ReadAll(stream)
				out := append(first[:n], tail...)
				if terminalErr == io.EOF && err != nil || terminalErr != io.EOF && err != terminalErr {
					t.Fatalf("terminal error = %v, want %v", err, terminalErr)
				}
				if !valid {
					if string(out) != body {
						t.Fatalf("incomplete tail = %q, want %q", out, body)
					}
					return
				}
				var decoder SSEJSONDecoder
				payloads := decoder.Feed(out)
				payloads = append(payloads, decoder.Flush()...)
				if len(payloads) != 1 {
					t.Fatalf("final payload missing: %q", out)
				}
				delta := payloads[0]["choices"].([]any)[0].(map[string]any)["delta"].(map[string]any)
				if delta["content"] != "<think>hidden</think>" || delta["reasoning_content"] != nil {
					t.Fatalf("delta = %#v", delta)
				}
			})
		}
	}
}

func TestParsedStreamPreservesUsageAfterFinishReason(t *testing.T) {
	for _, reason := range []string{"stop", "length", "tool_calls"} {
		for _, bufferSize := range []int{1, 4096} {
			t.Run(fmt.Sprintf("%s_buffer_%d", reason, bufferSize), func(t *testing.T) {
				body := "data: {\"choices\":[{\"index\":0,\"delta\":{\"reasoning_content\":\"think\",\"content\":\"test\"},\"finish_reason\":null}]}\n\n" +
					"data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"" + reason + "\"}]}\n\n" +
					"data: {\"choices\":[],\"usage\":{\"prompt_tokens\":12,\"completion_tokens\":1,\"total_tokens\":13}}\n\n" +
					"data: [DONE]\n\n"
				var readers []io.Reader
				for _, event := range strings.SplitAfter(body, "\n\n") {
					readers = append(readers, strings.NewReader(event))
				}
				var raw, transformed bytes.Buffer
				pipeline := modules.Pipeline{Modules: []modules.ResponseModule{modules.NewReasoningSwap("reasoning_to_content")}}
				stream := &streamRewriteReadCloser{
					ReadCloser: io.NopCloser(io.MultiReader(readers...)), pipeline: pipeline, state: pipeline.NewStreamState(),
					ctx: modules.Context{Path: "/v1/chat/completions", IsStream: true}, captureRaw: &raw, captureTransformed: &transformed,
				}
				defer stream.Close()
				var out bytes.Buffer
				if _, err := io.CopyBuffer(struct{ io.Writer }{&out}, stream, make([]byte, bufferSize)); err != nil {
					t.Fatal(err)
				}
				var decoder SSEJSONDecoder
				payloads := decoder.Feed(out.Bytes())
				if len(payloads) != 3 || !decoder.SawDone() {
					t.Fatalf("payloads=%#v done=%v", payloads, decoder.SawDone())
				}
				if usage := payloads[2]["usage"].(map[string]any); usage["total_tokens"] != float64(13) {
					t.Fatalf("usage = %#v", usage)
				}
				if finish := payloads[1]["choices"].([]any)[0].(map[string]any)["finish_reason"]; finish != reason {
					t.Fatalf("finish_reason = %#v", finish)
				}
				if raw.String() != body || transformed.String() != out.String() || strings.Count(out.String(), "[DONE]") != 1 {
					t.Fatalf("capture or terminator changed: %s", out.String())
				}
			})
		}
	}
}
