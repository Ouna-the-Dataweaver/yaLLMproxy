package gigachat

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"strings"

	"github.com/theio/yallmp-proxy/internal/sse"
)

type streamReadCloser struct {
	src          io.ReadCloser
	requestModel string
	decoder      sseDecoder
	buffer       []byte
	doneSent     bool
	terminal     bool
	pending      error
	tools        *toolCallStream
}

func newStreamReadCloser(src io.ReadCloser, requestModel string, arguments ...*toolArguments) *streamReadCloser {
	var plan *toolArguments
	if len(arguments) > 0 {
		plan = arguments[0]
	}
	return &streamReadCloser{src: src, requestModel: requestModel, tools: newToolCallStream(plan, requestModel)}
}

// Prime the first converted event inside the router's retry boundary. Once any
// event is available, subsequent read failures must not replay model output.
func primeStreamReadCloser(ctx context.Context, src io.ReadCloser, requestModel string, arguments ...*toolArguments) (*streamReadCloser, error) {
	stream := newStreamReadCloser(src, requestModel, arguments...)
	chunk := make([]byte, 4096)
	for emptyReads := 0; emptyReads < 100; emptyReads++ {
		if err := ctx.Err(); err != nil {
			_ = stream.Close()
			return nil, err
		}
		n, err := stream.Read(chunk)
		if ctxErr := ctx.Err(); ctxErr != nil {
			_ = stream.Close()
			return nil, ctxErr
		}
		if n > 0 {
			stream.buffer = append(chunk[:n:n], stream.buffer...)
			return stream, nil
		}
		if err != nil {
			_ = stream.Close()
			return nil, err
		}
	}
	_ = stream.Close()
	return nil, io.ErrNoProgress
}

func (s *streamReadCloser) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	for len(s.buffer) == 0 && !s.terminal {
		chunk := make([]byte, 4096)
		n, err := s.src.Read(chunk)
		if n > 0 {
			converted, conversionErr := s.convertEvents(s.decoder.feed(chunk[:n]))
			s.buffer = append(s.buffer, converted...)
			if conversionErr != nil {
				err = conversionErr
			}
		}
		if err != nil && !s.doneSent {
			if err == io.EOF {
				converted, conversionErr := s.convertEvents(s.decoder.flush())
				s.buffer = append(s.buffer, converted...)
				if conversionErr == nil && !s.doneSent {
					converted, conversionErr = s.finish()
					s.buffer = append(s.buffer, converted...)
				}
				if conversionErr != nil {
					s.pending = conversionErr
				}
			} else {
				s.pending = err
			}
			s.terminal = true
		}
		if n == 0 && err == nil {
			return 0, nil
		}
	}
	if len(s.buffer) > 0 {
		n := copy(p, s.buffer)
		s.buffer = s.buffer[n:]
		return n, nil
	}
	if s.pending != nil {
		err := s.pending
		s.pending = nil
		return 0, err
	}
	return 0, io.EOF
}

func (s *streamReadCloser) Close() error {
	return s.src.Close()
}

func (s *streamReadCloser) convertEvents(events []sseEvent) ([]byte, error) {
	var out []byte
	for _, event := range events {
		data := strings.TrimSpace(event.data)
		if data == "" {
			continue
		}
		if data == "[DONE]" {
			final, err := s.finish()
			out = append(out, final...)
			if err != nil {
				return out, err
			}
			s.terminal = true
			break
		}
		var payload map[string]any
		if err := json.Unmarshal([]byte(data), &payload); err != nil {
			return out, err
		}
		chunks, err := s.tools.feed(payload)
		out = append(out, marshalStreamChunks(chunks)...)
		if err != nil {
			return out, UpstreamError{StatusCode: 502, Body: err.Error()}
		}
	}
	return out, nil
}

func (s *streamReadCloser) finish() ([]byte, error) {
	if s.doneSent {
		return nil, nil
	}
	chunks, err := s.tools.finish()
	out := marshalStreamChunks(chunks)
	if err != nil {
		return out, UpstreamError{StatusCode: 502, Body: err.Error()}
	}
	s.doneSent = true
	return append(out, []byte("data: [DONE]\n\n")...), nil
}

func marshalStreamChunks(chunks []map[string]any) []byte {
	var out []byte
	for _, chunk := range chunks {
		encoded, err := json.Marshal(chunk)
		if err != nil {
			continue
		}
		out = append(out, []byte("data: ")...)
		out = append(out, encoded...)
		out = append(out, []byte("\n\n")...)
	}
	return out
}

type sseEvent struct {
	data string
}

type sseDecoder struct {
	framer sse.Framer
}

func (d *sseDecoder) feed(chunk []byte) []sseEvent {
	if len(chunk) == 0 {
		return nil
	}
	var events []sseEvent
	for _, frame := range d.framer.Feed(chunk) {
		raw := string(frame)
		if strings.TrimSpace(raw) == "" {
			continue
		}
		event := parseSSEEvent(raw)
		if event.data != "" {
			events = append(events, event)
		}
	}
	return events
}

func (d *sseDecoder) flush() []sseEvent {
	remaining := d.framer.Flush()
	if len(bytes.TrimSpace(remaining)) == 0 {
		return nil
	}
	event := parseSSEEvent(string(remaining))
	if event.data == "" {
		return nil
	}
	return []sseEvent{event}
}

func parseSSEEvent(raw string) sseEvent {
	raw = strings.ReplaceAll(raw, "\r\n", "\n")
	raw = strings.ReplaceAll(raw, "\r", "\n")
	var data []string
	for _, line := range strings.Split(raw, "\n") {
		if strings.HasPrefix(line, ":") {
			continue
		}
		if strings.HasPrefix(line, "data:") {
			data = append(data, strings.TrimLeft(strings.TrimPrefix(line, "data:"), " "))
		}
	}
	return sseEvent{data: strings.Join(data, "\n")}
}
