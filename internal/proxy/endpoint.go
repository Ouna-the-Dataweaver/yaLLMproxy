package proxy

import (
	"encoding/json"
	"fmt"
	"strings"
)

func bypassChatParsers(path string) bool {
	switch normalizeEndpoint(path) {
	case "embeddings", "systemone", "decisions", "transcriptions", "realtime", "voice":
		return true
	}
	return false
}

// ObserveBackendResult applies the same health accounting to upgraded sessions
// as ordinary HTTP inference. Callers report each handshake, never audio frames.
func (r *Router) ObserveBackendResult(name string, status int, err error) {
	if err != nil {
		r.recordBackendFailure(name, status, "ASR handshake failed")
		return
	}
	r.recordBackendSuccess(name, status)
}

// Inference parameters remain extensible. Known chat defaults must not leak
// into a different protocol. Other provider extensions remain configurable.
func parameterAppliesToEndpoint(name string, endpoints []string) bool {
	endpoint := "chat"
	if len(endpoints) > 0 {
		endpoint = normalizeEndpoint(endpoints[0])
	}
	if endpoint == "chat" || endpoint == "messages" || endpoint == "responses" {
		return true
	}
	root, _, _ := strings.Cut(name, ".")
	switch root {
	case "temperature", "top_p", "top_k", "min_p", "max_tokens", "max_completion_tokens", "max_output_tokens", "stop", "presence_penalty", "frequency_penalty", "repetition_penalty", "seed", "thinking", "reasoning", "reasoning_effort", "thinking_token_budget", "chat_template_kwargs", "tools", "tool_choice", "parallel_tool_calls", "stream_options", "response_format", "guided_regex", "guided_json", "structured_outputs":
		return false
	}
	return true
}

func embeddingIdentity(b Backend, payload map[string]any) string {
	model := b.TargetModel
	if model == "" {
		model, _ = payload["model"].(string)
	}
	settings := clonePayloadMap(payload)
	for _, key := range []string{"input", "model", "encoding_format", "user"} {
		delete(settings, key)
	}
	for key, parameter := range b.Parameters {
		if !parameterAppliesToEndpoint(key, []string{"embeddings"}) {
			continue
		}
		if key == "model" {
			if !parameter.AllowOverride || model == "" {
				model = fmt.Sprint(parameter.Default)
			}
			continue
		}
		if key == "input" || key == "encoding_format" || key == "user" {
			continue
		}
		if _, exists := getPath(settings, key); !exists || !parameter.AllowOverride {
			setPath(settings, key, parameter.Default)
		}
	}
	encoded, _ := json.Marshal(settings)
	suffix := ":" + string(encoded)
	if b.EmbeddingSpace != "" {
		return "space:" + b.EmbeddingSpace + suffix
	}
	if model != "" {
		return "model:" + b.APIType + ":" + model + suffix
	}
	return "node:" + b.Name + suffix
}
