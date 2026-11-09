package gigachat

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
)

// ExecuteEmbeddings shares OAuth, mTLS, and error handling with chat, without
// passing embeddings through the chat translator or tool emulation.
func (b *Backend) ExecuteEmbeddings(ctx context.Context, payload map[string]any, client *http.Client) (*http.Response, error) {
	badRequest := func(message string) (*http.Response, error) {
		return errorResponse(http.StatusBadRequest, message), nil
	}
	if _, exists := payload["dimensions"]; exists {
		return badRequest("GigaChat embeddings do not support dimensions")
	}
	switch input := payload["input"].(type) {
	case string:
	case []any:
		for _, item := range input {
			if _, ok := item.(string); !ok {
				return badRequest("GigaChat embeddings require text input; token IDs are not supported")
			}
		}
	default:
		return badRequest("GigaChat embeddings require text input")
	}
	outbound := make(map[string]any, len(payload))
	for key, value := range payload {
		outbound[key] = value
	}
	delete(outbound, "encoding_format")
	delete(outbound, "stream")
	resp, err := b.postJSON(ctx, "/embeddings", outbound, false, client)
	if upstream, ok := err.(UpstreamError); ok {
		return errorResponse(upstream.StatusCode, upstream.Body), nil
	}
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var result map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}
	data, ok := result["data"].([]any)
	if !ok || len(data) == 0 {
		return nil, fmt.Errorf("GigaChat returned no embeddings")
	}
	var tokens int
	allUsageReported := true
	for _, raw := range data {
		item, ok := raw.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("GigaChat returned an invalid embedding")
		}
		if usage, ok := item["usage"].(map[string]any); ok {
			if n, ok := usage["prompt_tokens"].(float64); ok && n >= 0 && math.Trunc(n) == n && n < float64(math.MaxInt-tokens) {
				tokens += int(n)
			} else {
				allUsageReported = false
			}
		} else {
			allUsageReported = false
		}
		if payload["encoding_format"] == "base64" {
			vector, ok := item["embedding"].([]any)
			if !ok {
				return nil, fmt.Errorf("GigaChat returned an invalid vector")
			}
			buf := make([]byte, len(vector)*4)
			for i, raw := range vector {
				n, ok := raw.(float64)
				if !ok || math.IsNaN(n) || math.IsInf(n, 0) || math.Abs(n) > math.MaxFloat32 {
					return nil, fmt.Errorf("GigaChat returned a non-finite vector")
				}
				binary.LittleEndian.PutUint32(buf[i*4:], math.Float32bits(float32(n)))
			}
			item["embedding"] = base64.StdEncoding.EncodeToString(buf)
		}
	}
	if _, exists := result["usage"]; !exists && allUsageReported {
		result["usage"] = map[string]any{"prompt_tokens": tokens, "total_tokens": tokens}
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		return nil, err
	}
	return responseFromBytes(resp.StatusCode, http.Header{"Content-Type": {"application/json"}}, encoded), nil
}
