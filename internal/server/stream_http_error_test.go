package server

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestStreamingRequestsPreserveStartupHTTPErrors(t *testing.T) {
	for _, endpoint := range []struct {
		name, path string
		params     map[string]any
	}{
		{"translated messages", "/v1/messages", nil},
		{"native messages", "/v1/messages", map[string]any{"model": "anthropic/upstream"}},
		{"translated responses", "/v1/responses", nil},
		{"native responses", "/v1/responses", map[string]any{"supports_responses_api": true}},
	} {
		for _, status := range []int{http.StatusBadRequest, http.StatusTooManyRequests, http.StatusServiceUnavailable} {
			t.Run(fmt.Sprintf("%s/%d", endpoint.name, status), func(t *testing.T) {
				app := newLoggingRegressionApp(t, endpoint.params)
				app.Router.Client.Transport = loggingRoundTripper(func(req *http.Request) (*http.Response, error) {
					return &http.Response{
						StatusCode: status,
						Header: http.Header{
							"Content-Type": {"application/json"},
							"Retry-After":  {"7"},
						},
						Body:    io.NopCloser(strings.NewReader(`{"error":{"message":"upstream rejected request","type":"rate_limit_error","code":"rate_limit"}}`)),
						Request: req,
					}, nil
				})
				request := httptest.NewRequest(http.MethodPost, endpoint.path, strings.NewReader(`{"model":"alpha","messages":[{"role":"user","content":"hi"}],"input":"hi","max_tokens":16,"stream":true}`))
				writer := httptest.NewRecorder()
				app.ServeHTTP(writer, request)
				if writer.Code != status || writer.Header().Get("Content-Type") != "application/json" || writer.Header().Get("Retry-After") != "7" {
					t.Fatalf("status=%d headers=%v body=%s", writer.Code, writer.Header(), writer.Body)
				}
				if !json.Valid(writer.Body.Bytes()) || !strings.Contains(writer.Body.String(), "upstream rejected request") {
					t.Fatalf("startup error was converted into a broken stream: %s", writer.Body)
				}
				logs, err := app.LogDB.List(t.Context(), 10, 0)
				if err != nil || len(logs) != 1 || !logs[0].IsStream || logs[0].Outcome != "error" || logs[0].BackendStatus != status {
					t.Fatalf("logs=%+v err=%v", logs, err)
				}
				for scope, active := range app.Admission.Metrics().ActiveByScope {
					if active != 0 {
						t.Fatalf("admission scope %s retains %d slots", scope, active)
					}
				}
			})
		}
	}
}
