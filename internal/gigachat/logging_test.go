package gigachat

import (
	"bytes"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"testing"
)

func TestUpstreamErrorConsoleLoggingHonorsMasterSwitch(t *testing.T) {
	for _, stream := range []bool{false, true} {
		for _, enabled := range []bool{false, true} {
			t.Run(fmt.Sprintf("stream=%t/enabled=%t", stream, enabled), func(t *testing.T) {
				var output bytes.Buffer
				previous := log.Writer()
				log.SetOutput(&output)
				defer log.SetOutput(previous)
				backend := NewBackend(Config{Mode: "local", BaseURL: "https://gigachat.test/api/v1", ModelName: "fallback", APIKey: "private-key"})
				backend.LoggingDisabled = !enabled
				backend.accessToken = "private-token"
				providerBody := `{"message":"schema type invalid; private-key private-token private-prompt"}`
				client := &http.Client{Transport: diagnosticRoundTripper(func(req *http.Request) (*http.Response, error) {
					return &http.Response{StatusCode: 422, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(providerBody)), Request: req}, nil
				})}
				response, err := backend.Execute(t.Context(), map[string]any{"model": "test-model", "messages": []any{map[string]any{"role": "user", "content": "private-prompt"}}}, stream, client)
				if err != nil {
					t.Fatal(err)
				}
				defer response.Body.Close()
				responseBody, err := io.ReadAll(response.Body)
				if err != nil || !strings.Contains(string(responseBody), "private-prompt") {
					t.Fatalf("client response changed: body=%s, err=%v", responseBody, err)
				}
				text := output.String()
				if !enabled {
					if text != "" {
						t.Fatalf("disabled logging emitted %q", text)
					}
					return
				}
				for _, want := range []string{"GigaChat request failed", `model="test-model"`, fmt.Sprintf("stream=%t", stream), "status=422", "schema type invalid"} {
					if !strings.Contains(text, want) {
						t.Fatalf("log %q missing %q", text, want)
					}
				}
				for _, secret := range []string{"private-key", "private-token", "private-prompt"} {
					if strings.Contains(text, secret) {
						t.Fatalf("log exposed %s: %s", secret, text)
					}
				}
			})
		}
	}
}

type diagnosticRoundTripper func(*http.Request) (*http.Response, error)

func (f diagnosticRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }
