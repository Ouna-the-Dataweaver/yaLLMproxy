package gigachat

import (
	"bytes"
	"encoding/json"
	"io"
	"strings"
)

type streamReadCloser struct {
	src          io.ReadCloser
	requestModel string
	decoder      sseDecoder
	buffer       []byte
	doneSent     bool
}

func newStreamReadCloser(src io.ReadCloser, requestModel string) io.ReadCloser {
	return &streamReadCloser{src: src, requestModel: requestModel}
}

func (s *streamReadCloser) Read(p []byte) (int, error) {
	for len(s.buffer) == 0 {
		chunk := make([]byte, 4096)
		n, err := s.src.Read(chunk)
		if n > 0 {
			s.buffer = append(s.buffer, s.convertEvents(s.decoder.feed(chunk[:n]))...)
		}
		if err != nil {
			if err == io.EOF {
				s.buffer = append(s.buffer, s.convertEvents(s.decoder.flush())...)
				if !s.doneSent {
					s.doneSent = true
					s.buffer = append(s.buffer, []byte("data: [DONE]\n\n")...)
				}
			}
			if len(s.buffer) == 0 {
				return 0, err
			}
			break
		}
	}
	n := copy(p, s.buffer)
	s.buffer = s.buffer[n:]
	return n, nil
}

func (s *streamReadCloser) Close() error {
	return s.src.Close()
}

func (s *streamReadCloser) convertEvents(events []sseEvent) []byte {
	var out []byte
	for _, event := range events {
		data := strings.TrimSpace(event.data)
		if data == "" {
			continue
		}
		if data == "[DONE]" {
			if !s.doneSent {
				s.doneSent = true
				out = append(out, []byte("data: [DONE]\n\n")...)
			}
			continue
		}
		var payload map[string]any
		if err := json.Unmarshal([]byte(data), &payload); err != nil {
			continue
		}
		chunk := GigaChatChunkToOpenAI(payload, s.requestModel)
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
	buffer []byte
}

func (d *sseDecoder) feed(chunk []byte) []sseEvent {
	if len(chunk) == 0 {
		return nil
	}
	d.buffer = append(d.buffer, chunk...)
	d.buffer = bytes.ReplaceAll(d.buffer, []byte("\r\n"), []byte("\n"))
	d.buffer = bytes.ReplaceAll(d.buffer, []byte("\r"), []byte("\n"))
	var events []sseEvent
	for {
		idx := bytes.Index(d.buffer, []byte("\n\n"))
		if idx < 0 {
			break
		}
		raw := string(d.buffer[:idx])
		d.buffer = d.buffer[idx+2:]
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
	remaining := d.buffer
	d.buffer = nil
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
