package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/theio/yallmp-proxy/internal/config"
	"github.com/theio/yallmp-proxy/internal/proxy"
)

func TestEngineAdminAndLocalValidation(t *testing.T) {
	enabled := true
	app, err := New(&config.Config{Nodes: []config.NodeEntry{{Name: "n", Model: "m", APIBase: "http://unused.invalid/v1", Engine: "vllm", Compatibility: config.EngineCompatibility{Enabled: &enabled}}}})
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(config.AdminPasswordEnv, "engine-admin-secret")
	for _, password := range []string{"", "engine-admin-secret"} {
		req := httptest.NewRequest(http.MethodGet, "/admin/engines", nil)
		req.Header.Set("x-admin-password", password)
		w := httptest.NewRecorder()
		app.ServeHTTP(w, req)
		if password == "" {
			if w.Code != 403 {
				t.Fatalf("unprotected engine status: %d", w.Code)
			}
		} else if w.Code != 200 || !strings.Contains(w.Body.String(), `"engine":"vllm"`) {
			t.Fatalf("engine status: %d %s", w.Code, w.Body)
		}
	}
	w := httptest.NewRecorder()
	app.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"m","messages":[{"role":"user","content":"hi"}],"regex":"a","guided_regex":"b"}`)))
	if w.Code != 400 || !strings.Contains(w.Body.String(), "engine_compatibility_error") {
		t.Fatalf("local error: %d %s", w.Code, w.Body)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	done := app.StartEngineDiscovery(ctx)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("discovery did not stop")
	}
}

// TestEngineLive uses the real proxy handler without starting a listening server
// or changing the running proxy/config. It is opt-in because it runs inference.
func TestEngineLive(t *testing.T) {
	urls := os.Getenv("YALLMP_TEST_ENGINE_URLS")
	if urls == "" {
		t.Skip("set YALLMP_TEST_ENGINE_URLS to comma-separated OpenAI API bases")
	}
	client := &http.Client{Timeout: 90 * time.Second}
	for index, base := range strings.Split(urls, ",") {
		t.Run(fmt.Sprintf("upstream_%d", index), func(t *testing.T) {
			base = strings.TrimRight(strings.TrimSpace(base), "/")
			req, err := http.NewRequest(http.MethodGet, base+"/models", nil)
			if err != nil {
				t.Fatal(err)
			}
			key := os.Getenv("YALLMP_TEST_ENGINE_API_KEY")
			if key != "" {
				req.Header.Set("Authorization", "Bearer "+key)
			}
			resp, err := client.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			var models struct {
				Data []struct {
					ID    string `json:"id"`
					Owner string `json:"owned_by"`
				} `json:"data"`
			}
			err = json.NewDecoder(io.LimitReader(resp.Body, 1024*1024)).Decode(&models)
			resp.Body.Close()
			if err != nil || resp.StatusCode != 200 || len(models.Data) == 0 {
				t.Fatalf("model discovery failed: status %d error %v", resp.StatusCode, err)
			}
			enabled := true
			app, err := New(&config.Config{Nodes: []config.NodeEntry{{Name: "live", Model: "engine-test", UpstreamModel: models.Data[0].ID, APIBase: base, APIKey: key, Engine: "auto", Compatibility: config.EngineCompatibility{Enabled: &enabled}}}, ProxySettings: config.ProxySettings{Logging: config.LoggingConfig{LogToDisk: false}}})
			if err != nil {
				t.Fatal(err)
			}
			defer app.CloseInferenceCache()
			app.Router.Client = client
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			app.Router.DiscoverEngines(ctx)
			backend, _ := app.Router.GetBackend("live")
			profile := backend.EngineProfile
			if profile.Engine != "vllm" && profile.Engine != "sglang" {
				t.Fatalf("engine not detected: %+v", profile)
			}
			if models.Data[0].Owner != profile.Engine {
				t.Fatalf("unexpected engine: %s", profile.Engine)
			}
			budget := "unknown"
			if profile.ThinkingBudget != nil {
				budget = fmt.Sprint(*profile.ThinkingBudget)
			}
			t.Logf("detected %s %s; dp_size=%d; thinking_budget=%s", profile.Engine, profile.Version, profile.DPSize, budget)
			for _, stream := range []bool{false, true} {
				payload := map[string]any{"model": "engine-test", "messages": []any{map[string]any{"role": "user", "content": "Reply with exactly the word no."}}, "max_tokens": 16, "temperature": 0, "stream": stream, "chat_template_kwargs": map[string]any{"thinking": false}}
				if profile.Engine == "sglang" {
					payload["structured_outputs"] = map[string]any{"regex": "yes"}
				} else {
					payload["regex"] = "yes"
				}
				body, _ := json.Marshal(payload)
				prepared := proxy.BuildBackendBodyWithWarnings(payload, backend, body, stream)
				if prepared.Err != nil {
					t.Fatal(prepared.Err)
				}
				translated := false
				for _, warning := range prepared.Warnings {
					if warning.Code == "engine_parameter_translated" {
						translated = true
					}
				}
				if !translated {
					t.Fatal("live request was not translated")
				}
				requestCtx, stop := context.WithTimeout(context.Background(), 90*time.Second)
				r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body)).WithContext(requestCtx)
				w := httptest.NewRecorder()
				app.ServeHTTP(w, r)
				stop()
				if w.Code != 200 {
					t.Fatalf("inference failed: %d %.1200s", w.Code, w.Body.String())
				}
				content := ""
				if stream {
					if !strings.Contains(w.Body.String(), "data: [DONE]") {
						t.Fatal("stream missing terminator")
					}
					for _, line := range strings.Split(w.Body.String(), "\n") {
						if !strings.HasPrefix(line, "data: ") {
							continue
						}
						var chunk struct {
							Choices []struct {
								Delta struct {
									Content string `json:"content"`
								} `json:"delta"`
							} `json:"choices"`
						}
						if json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &chunk) == nil {
							for _, choice := range chunk.Choices {
								content += choice.Delta.Content
							}
						}
					}
				} else {
					var result struct {
						Choices []struct {
							Message struct {
								Content string `json:"content"`
							} `json:"message"`
						} `json:"choices"`
					}
					if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
						t.Fatal(err)
					}
					if len(result.Choices) > 0 {
						content = result.Choices[0].Message.Content
					}
				}
				if content != "yes" {
					t.Fatalf("constraint not enforced (stream=%v): content=%q response=%.1200s", stream, content, w.Body.String())
				}
				t.Logf("%s regex translation enforced, stream=%v, output=%q", profile.Engine, stream, content)
			}
		})
	}
}
