package server

import (
	"bytes"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/theio/yallmp-proxy/internal/config"
)

func TestMasterLoggingSwitchControlsRequestSinks(t *testing.T) {
	for _, value := range []any{nil, true, false, "false"} {
		for _, stream := range []bool{false, true} {
			for _, status := range []int{200, 502} {
				t.Run(fmt.Sprintf("enabled=%v/stream=%t/status=%d", value, stream, status), func(t *testing.T) {
					var console bytes.Buffer
					previous := log.Writer()
					log.SetOutput(&console)
					defer log.SetOutput(previous)
					dir := t.TempDir()
					payloadDir := filepath.Join(dir, "requests")
					cfg := &config.Config{ModelList: []config.ModelEntry{{ModelName: "alpha", ModelParams: map[string]any{"api_base": "http://upstream.test/v1", "model": "openai/upstream"}}}, ProxySettings: config.ProxySettings{Logging: config.LoggingConfig{Enabled: value, LogToDisk: true, LogParsedResponse: true, LogParsedStream: true, FullRequestStore: map[string]any{"enabled": true, "path": payloadDir, "cleanup_on_startup": true}}}, Database: map[string]any{"backend": "sqlite", "connection": map[string]any{"sqlite": map[string]any{"path": filepath.Join(dir, "state.db")}}}}
					app, err := New(cfg)
					if err != nil {
						t.Fatal(err)
					}
					defer app.LogDB.Close()
					body := `{"choices":[{"message":{"content":"response"},"finish_reason":"stop"}],"usage":{"total_tokens":3}}`
					contentType := "application/json"
					if stream {
						body = "data: " + body + "\n\ndata: [DONE]\n\n"
						contentType = "text/event-stream"
					}
					app.Router.Client = &http.Client{Transport: loggingRoundTripper(func(req *http.Request) (*http.Response, error) {
						return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": {contentType}}, Body: io.NopCloser(strings.NewReader(body)), Request: req}, nil
					})}
					req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(fmt.Sprintf(`{"model":"alpha","messages":[{"role":"user","content":"private-prompt"}],"tool_choice":"auto","stream":%t}`, stream)))
					writer := httptest.NewRecorder()
					app.ServeHTTP(writer, req)
					if writer.Code != status {
						t.Fatalf("status=%d, body=%s", writer.Code, writer.Body.String())
					}
					rows, err := app.LogDB.List(t.Context(), 10, 0)
					if err != nil {
						t.Fatal(err)
					}
					enabled := cfg.ProxySettings.Logging.IsEnabled()
					if enabled {
						if len(rows) != 1 || len(app.Logs.List()) != 1 {
							t.Fatalf("missing enabled logs: SQL=%#v", rows)
						}
						if files, err := os.ReadDir(payloadDir); err != nil || len(files) == 0 {
							t.Fatalf("missing payload: entries=%v, err=%v", files, err)
						}
						if console.Len() == 0 {
							t.Fatal("expected adaptation warning console log")
						}
					} else {
						if len(rows) != 0 || len(app.Logs.List()) != 0 {
							t.Fatalf("disabled logging recorded request: %#v", rows)
						}
						if _, err := os.Stat(payloadDir); !os.IsNotExist(err) {
							t.Fatalf("disabled logging wrote payload directory: %v", err)
						}
						if app.captureParsedResponseSidecars() || app.captureParsedStreamSidecars() {
							t.Fatal("disabled logging captures sidecars")
						}
						if console.Len() != 0 {
							t.Fatalf("disabled logging wrote console: %q", console.String())
						}
					}
				})
			}
		}
	}
}

func TestMasterLoggingDisablesOTelLogEventsOnly(t *testing.T) {
	cfg := &config.Config{ProxySettings: config.ProxySettings{Logging: config.LoggingConfig{Enabled: false}, OTel: config.OTelConfig{Enabled: true, Logs: config.OTelLogsConfig{Enabled: true}}}}
	otel := proxyOTelConfig(cfg)
	if !config.ParseBool(otel.Enabled) || config.ParseBool(otel.Logs.Enabled) {
		t.Fatalf("effective OTel config=%#v", otel)
	}
	if !config.ParseBool(cfg.ProxySettings.OTel.Logs.Enabled) {
		t.Fatal("logging gate mutated source configuration")
	}
}
