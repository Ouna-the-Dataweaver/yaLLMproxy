package gigachat

import (
	"log"
	"strconv"
	"strings"
)

func (b *Backend) logUpstreamError(payload map[string]any, isStream bool, upstreamErr UpstreamError) {
	if b.LoggingDisabled {
		return
	}
	b.mu.Lock()
	accessToken := b.accessToken
	b.mu.Unlock()
	sensitive := []string{b.Config.APIKey, accessToken}
	for _, key := range []string{"prompt", "input"} {
		collectDiagnosticText(payload[key], &sensitive)
	}
	if messages, ok := payload["messages"].([]any); ok {
		for _, raw := range messages {
			if message, ok := raw.(map[string]any); ok {
				collectDiagnosticText(message["content"], &sensitive)
			}
		}
	}
	body := upstreamErr.Body
	for _, value := range sensitive {
		if value == "" {
			continue
		}
		body = strings.ReplaceAll(body, value, "[REDACTED]")
		escaped := strconv.Quote(value)
		body = strings.ReplaceAll(body, escaped[1:len(escaped)-1], "[REDACTED]")
	}
	if len(body) > 4096 {
		body = body[:4096] + " [truncated]"
	}
	model := firstNonEmpty(stringOrEmpty(payload["model"]), b.Config.ModelName)
	log.Printf("ERROR GigaChat request failed model=%q stream=%t status=%d upstream_error=%q", model, isStream, upstreamErr.StatusCode, body)
}

func collectDiagnosticText(value any, out *[]string) {
	switch value := value.(type) {
	case string:
		*out = append(*out, value)
	case []any:
		for _, item := range value {
			collectDiagnosticText(item, out)
		}
	case map[string]any:
		// Content parts carry text under text/content; exclude structural metadata.
		for _, key := range []string{"text", "content"} {
			collectDiagnosticText(value[key], out)
		}
	}
}
