package proxy

import (
	"bytes"
	"compress/flate"
	"compress/gzip"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/theio/yallmp-proxy/internal/config"
)

func TestBuildOutboundHeadersDropsClientAcceptEncoding(t *testing.T) {
	out := BuildOutboundHeaders(http.Header{"Accept-Encoding": {"br, zstd"}}, "", false, "openai", "")
	if got := out.Get("Accept-Encoding"); got != "" {
		t.Fatalf("accept-encoding = %q, want unset so the transport negotiates gzip", got)
	}
}

func compressedUpstream(t *testing.T, encoding string, contentType string, body string, sawAcceptEncoding *string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if sawAcceptEncoding != nil {
			*sawAcceptEncoding = r.Header.Get("Accept-Encoding")
		}
		var buf bytes.Buffer
		var writer io.WriteCloser
		switch encoding {
		case "gzip":
			writer = gzip.NewWriter(&buf)
		case "deflate":
			writer, _ = flate.NewWriter(&buf, flate.DefaultCompression)
		default:
			t.Fatalf("unsupported test encoding %q", encoding)
		}
		_, _ = writer.Write([]byte(body))
		_ = writer.Close()
		w.Header().Set("Content-Type", contentType)
		w.Header().Set("Content-Encoding", encoding)
		_, _ = w.Write(buf.Bytes())
	}))
}

func encodingTestRouter(t *testing.T, apiBase string) *Router {
	t.Helper()
	router, err := NewRouter(&config.Config{
		ModelList: []config.ModelEntry{{
			ModelName:   "m",
			ModelParams: map[string]any{"api_base": apiBase, "model": "m"},
		}},
		RouterSettings: config.RouterSettings{NumRetries: 1},
	})
	if err != nil {
		t.Fatal(err)
	}
	return router
}

func TestForwardDecodesCompressedResponses(t *testing.T) {
	const want = `{"id":"r1","choices":[{"message":{"content":"hi"}}]}`
	for _, encoding := range []string{"gzip", "deflate"} {
		t.Run(encoding, func(t *testing.T) {
			var sawAcceptEncoding string
			upstream := compressedUpstream(t, encoding, "application/json", want, &sawAcceptEncoding)
			defer upstream.Close()
			router := encodingTestRouter(t, upstream.URL+"/v1")

			resp, err := router.Forward(context.Background(), "m", "/v1/chat/completions", "", []byte(`{"model":"m"}`), map[string]any{"model": "m"}, false, http.Header{
				"Content-Type":    {"application/json"},
				"Accept-Encoding": {"gzip, br"},
			})
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			got, err := io.ReadAll(resp.Body)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != want {
				t.Fatalf("body = %q, want decoded JSON", got)
			}
			if ce := resp.Header.Get("Content-Encoding"); ce != "" {
				t.Fatalf("content-encoding = %q on decoded body", ce)
			}
			if strings.Contains(sawAcceptEncoding, "br") {
				t.Fatalf("client accept-encoding forwarded upstream: %q", sawAcceptEncoding)
			}
		})
	}
}

func TestForwardDecodesUnsolicitedCompressedStream(t *testing.T) {
	const want = "data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\ndata: [DONE]\n\n"
	upstream := compressedUpstream(t, "gzip", "text/event-stream", want, nil)
	defer upstream.Close()
	router := encodingTestRouter(t, upstream.URL+"/v1")

	resp, err := router.Forward(context.Background(), "m", "/v1/chat/completions", "", []byte(`{"model":"m","stream":true}`), map[string]any{"model": "m", "stream": true}, true, http.Header{"Content-Type": {"application/json"}})
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	got, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != want {
		t.Fatalf("stream body = %q", got)
	}
	if ce := resp.Header.Get("Content-Encoding"); ce != "" {
		t.Fatalf("content-encoding = %q on decoded stream", ce)
	}
}
