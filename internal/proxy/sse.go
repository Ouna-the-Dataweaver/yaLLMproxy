package proxy

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

const StreamErrorCheckBufferSize = 4096

func DetectSSEStreamError(data []byte) string {
	text := string(data)
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		raw := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if raw == "" || raw == "[DONE]" {
			continue
		}
		var payload map[string]any
		if err := json.Unmarshal([]byte(raw), &payload); err != nil {
			continue
		}
		if payload["type"] == "error" {
			errObj, _ := payload["error"].(map[string]any)
			message := "unknown error"
			if msg, ok := errObj["message"].(string); ok && msg != "" {
				message = msg
			}
			code := "unknown"
			if val, ok := errObj["http_code"]; ok {
				code = fmt.Sprint(val)
			}
			return fmt.Sprintf("SSE stream error: %s (http_code=%s)", message, code)
		}
		if errObj, ok := payload["error"].(map[string]any); ok {
			message := fmt.Sprint(errObj)
			if msg, ok := errObj["message"].(string); ok && msg != "" {
				message = msg
			}
			errType := "unknown"
			if typ, ok := errObj["type"].(string); ok && typ != "" {
				errType = typ
			}
			return fmt.Sprintf("SSE stream error: %s (type=%s)", message, errType)
		}
	}
	return ""
}

type SSEEvent struct {
	Data       string
	OtherLines []string
}

type SSEDecoder struct {
	buffer []byte
}

func (d *SSEDecoder) Feed(chunk []byte) []SSEEvent {
	if len(chunk) == 0 {
		return nil
	}
	d.buffer = append(d.buffer, chunk...)
	d.buffer = bytes.ReplaceAll(d.buffer, []byte("\r\n"), []byte("\n"))
	d.buffer = bytes.ReplaceAll(d.buffer, []byte("\r"), []byte("\n"))
	var events []SSEEvent
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
		if event.Data != "" || len(event.OtherLines) > 0 {
			events = append(events, event)
		}
	}
	return events
}

func (d *SSEDecoder) Flush() []byte {
	out := d.buffer
	d.buffer = nil
	return out
}

func parseSSEEvent(raw string) SSEEvent {
	event := SSEEvent{}
	var data []string
	for _, line := range strings.Split(raw, "\n") {
		if strings.HasPrefix(line, "data:") {
			data = append(data, strings.TrimLeft(strings.TrimPrefix(line, "data:"), " "))
		} else if strings.TrimSpace(line) != "" {
			event.OtherLines = append(event.OtherLines, line)
		}
	}
	event.Data = strings.Join(data, "\n")
	return event
}

type SSEJSONDecoder struct {
	decoder SSEDecoder
	sawDone bool
}

func (d *SSEJSONDecoder) Feed(chunk []byte) []map[string]any {
	events := d.decoder.Feed(chunk)
	return d.decodeJSONEvents(events)
}

func (d *SSEJSONDecoder) Flush() []map[string]any {
	remaining := d.decoder.Flush()
	if len(bytes.TrimSpace(remaining)) == 0 {
		return nil
	}
	event := parseSSEEvent(string(remaining))
	return d.decodeJSONEvents([]SSEEvent{event})
}

// SawDone reports whether the stream carried the OpenAI "[DONE]" sentinel.
func (d *SSEJSONDecoder) SawDone() bool {
	return d.sawDone
}

func (d *SSEJSONDecoder) decodeJSONEvents(events []SSEEvent) []map[string]any {
	var out []map[string]any
	for _, event := range events {
		data := strings.TrimSpace(event.Data)
		if data == "[DONE]" {
			d.sawDone = true
			continue
		}
		if data == "" {
			continue
		}
		var payload map[string]any
		if err := json.Unmarshal([]byte(data), &payload); err == nil {
			out = append(out, payload)
		}
	}
	return out
}

// SSEPayloadError returns the error object carried by an upstream SSE payload,
// either an OpenAI-style {"error": {...}} chunk or a {"type": "error"} event.
func SSEPayloadError(payload map[string]any) (map[string]any, bool) {
	if errObj, ok := payload["error"].(map[string]any); ok {
		return errObj, true
	}
	if payload["type"] == "error" {
		if errObj, ok := payload["error"].(map[string]any); ok {
			return errObj, true
		}
		if message, ok := payload["error"].(string); ok && message != "" {
			return map[string]any{"message": message}, true
		}
		return map[string]any{"message": "upstream stream error"}, true
	}
	return nil, false
}
