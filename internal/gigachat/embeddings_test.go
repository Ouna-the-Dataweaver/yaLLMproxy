package gigachat

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"strings"
	"testing"
)

func TestEmbeddingExecutionAndUsage(t *testing.T) {
	calls := 0
	client := &http.Client{Transport: parityTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.URL.Path != "/v1/embeddings" {
			t.Errorf("wrong endpoint %s", r.URL)
		}
		var payload map[string]any
		_ = json.NewDecoder(r.Body).Decode(&payload)
		if payload["model"] != "Embeddings" || payload["encoding_format"] != nil {
			t.Errorf("wrong body %#v", payload)
		}
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"object":"list","model":"Embeddings","data":[{"index":0,"object":"embedding","embedding":[0.5,-1],"usage":{"prompt_tokens":3}},{"index":1,"object":"embedding","embedding":[1,0],"usage":{"prompt_tokens":4}}]}`))}, nil
	})}
	backend := NewBackend(Config{Mode: "local", BaseURL: "https://giga.test/v1"})
	resp, err := backend.ExecuteEmbeddings(context.Background(), map[string]any{"model": "Embeddings", "input": []any{"a", "b"}, "encoding_format": "base64"}, client)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var result map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&result)
	if result["usage"].(map[string]any)["total_tokens"] != float64(7) || calls != 1 {
		t.Fatalf("usage or calls: %#v %d", result, calls)
	}
	vector := result["data"].([]any)[0].(map[string]any)["embedding"].(string)
	buf, err := base64.StdEncoding.DecodeString(vector)
	if err != nil || len(buf) != 8 || math.Float32frombits(binary.LittleEndian.Uint32(buf)) != 0.5 {
		t.Fatalf("bad base64 vector %s %v", vector, err)
	}
	for _, payload := range []map[string]any{{"input": []any{int64(1)}}, {"input": "a", "dimensions": 2}} {
		resp, err := backend.ExecuteEmbeddings(context.Background(), payload, client)
		if err != nil || resp.StatusCode != 400 || calls != 1 {
			t.Fatalf("unsupported option reached upstream: %v %v %d", resp, err, calls)
		}
		_ = resp.Body.Close()
	}
}
