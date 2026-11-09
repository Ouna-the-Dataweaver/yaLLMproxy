package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/theio/yallmp-proxy/internal/admission"
	"github.com/theio/yallmp-proxy/internal/config"
)

func inferenceTestApp(t *testing.T, base string, endpoints ...string) *App {
	t.Helper()
	app, err := New(&config.Config{
		Providers: []config.ProviderEntry{{Name: "p", APIBase: base, APIKey: "upstream-secret", Endpoints: endpoints}},
		Nodes:     []config.NodeEntry{{Name: "node", Provider: "p", Model: "public", UpstreamModel: "native-model"}},
		AppKeys:   config.AppKeysConfig{Enabled: true, HeaderName: "x-client-key", Keys: []config.AppKeyEntry{{KeyID: "client", Secret: "client-secret", Models: []string{"public"}}}},
		Database:  map[string]any{"backend": "sqlite", "connection": map[string]any{"sqlite": map[string]any{"path": filepath.Join(t.TempDir(), "requests.db")}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if app.LogDB != nil {
			_ = app.LogDB.Close()
		}
		_ = app.CloseInferenceCache()
	})
	return app
}

func audioMultipart(t *testing.T, model string, stream bool) ([]byte, string) {
	t.Helper()
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	_ = w.WriteField("model", model)
	_ = w.WriteField("provider_extension", "first")
	_ = w.WriteField("provider_extension", "second")
	_ = w.WriteField("timestamp_granularities[]", "word")
	if stream {
		_ = w.WriteField("stream", "true")
	}
	part, err := w.CreateFormFile("file", "speech.wav")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = part.Write([]byte{'R', 'I', 'F', 'F', 0, 255, 1, 2, 3})
	_ = w.Close()
	return body.Bytes(), w.FormDataContentType()
}

func TestTranscriptionPreservesMultipartAndAccountsDuration(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/audio/transcriptions" || r.Header.Get("Authorization") != "Bearer upstream-secret" || r.Header.Get("x-client-key") != "" {
			t.Errorf("upstream request %s %v", r.URL, r.Header)
		}
		if err := r.ParseMultipartForm(1024); err != nil {
			t.Error(err)
			w.WriteHeader(400)
			return
		}
		defer r.MultipartForm.RemoveAll()
		if r.FormValue("model") != "native-model" || len(r.MultipartForm.Value["provider_extension"]) != 2 {
			t.Errorf("form=%v", r.MultipartForm.Value)
		}
		file, _, err := r.FormFile("file")
		if err != nil {
			t.Error(err)
			return
		}
		defer file.Close()
		audio, _ := io.ReadAll(file)
		if !bytes.Equal(audio, []byte{'R', 'I', 'F', 'F', 0, 255, 1, 2, 3}) {
			t.Errorf("audio changed: %v", audio)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"text":"hello","usage":{"type":"duration","seconds":1.25}}`)
	}))
	defer upstream.Close()
	app := inferenceTestApp(t, upstream.URL+"/v1", "transcriptions")
	body, contentType := audioMultipart(t, "public", false)
	req := httptest.NewRequest("POST", "/v1/audio/transcriptions", bytes.NewReader(body))
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("x-client-key", "client-secret")
	resp := httptest.NewRecorder()
	app.ServeHTTP(resp, req)
	if resp.Code != 200 || !strings.Contains(resp.Body.String(), `"hello"`) {
		t.Fatalf("%d %s", resp.Code, resp.Body.String())
	}
	records, err := app.LogDB.List(context.Background(), 10, 0)
	if err != nil || len(records) != 1 || records[0].UsageStats["seconds"] != 1.25 {
		t.Fatalf("duration lost: %+v %v", records, err)
	}
	app.Config.ProxySettings.ASR.MaxUploadBytes = 8
	resp = httptest.NewRecorder()
	req = httptest.NewRequest("POST", "/v1/audio/transcriptions", bytes.NewReader(body))
	req.Header.Set("Content-Type", contentType)
	app.ServeHTTP(resp, req)
	if resp.Code != 413 {
		t.Fatalf("oversized upload status=%d", resp.Code)
	}
}

func TestTranscriptionSSEUsage(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "event: transcript.text.delta\ndata: {\"type\":\"transcript.text.delta\",\"delta\":\"hi\"}\n\nevent: transcript.text.done\ndata: {\"type\":\"transcript.text.done\",\"text\":\"hi\",\"usage\":{\"type\":\"duration\",\"seconds\":2}}\n\n")
	}))
	defer upstream.Close()
	app := inferenceTestApp(t, upstream.URL+"/v1", "transcriptions")
	body, ct := audioMultipart(t, "public", true)
	req := httptest.NewRequest("POST", "/v1/audio/transcriptions", bytes.NewReader(body))
	req.Header.Set("Content-Type", ct)
	req.Header.Set("x-client-key", "client-secret")
	resp := httptest.NewRecorder()
	app.ServeHTTP(resp, req)
	if resp.Code != 200 || !strings.Contains(resp.Body.String(), "transcript.text.done") {
		t.Fatalf("%d %s", resp.Code, resp.Body.String())
	}
	records, err := app.LogDB.List(context.Background(), 10, 0)
	if err != nil || len(records) != 1 || records[0].AudioSeconds != 2 {
		t.Fatalf("stream duration lost: %+v %v", records, err)
	}
}

func TestLiveASRBridgesFramesAndReleasesSlots(t *testing.T) {
	for _, path := range []string{"/v1/realtime", "/ws/voice"} {
		t.Run(path, func(t *testing.T) {
			upstreamDone := make(chan struct{})
			proxyDone := make(chan struct{})
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				defer close(upstreamDone)
				if r.URL.Path != path || (path == "/ws/voice" && r.URL.Query().Get("model") != "native-model") || (path == "/v1/realtime" && (r.URL.Query().Get("model") != "" || r.URL.Query().Get("intent") != "transcription")) || r.Header.Get("Authorization") != "Bearer upstream-secret" || r.Header.Get("x-client-key") != "" || strings.Contains(r.Header.Get("Sec-WebSocket-Protocol"), "client-secret") {
					t.Errorf("bad handshake %s %v", r.URL, r.Header)
				}
				conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{Subprotocols: []string{"realtime"}})
				if err != nil {
					t.Error(err)
					return
				}
				defer conn.CloseNow()
				ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
				defer cancel()
				typ, data, err := conn.Read(ctx)
				if err != nil {
					t.Error(err)
					return
				}
				if typ != websocket.MessageText || !bytes.Contains(data, []byte(`"native-model"`)) {
					t.Errorf("start=%s", data)
				}
				_ = conn.Write(ctx, websocket.MessageText, []byte(`{"type":"ready"}`))
				typ, data, err = conn.Read(ctx)
				if err != nil {
					t.Error(err)
					return
				}
				if typ != websocket.MessageBinary || !bytes.Equal(data, []byte{0, 1, 255, 2}) {
					t.Errorf("binary audio changed: %v", data)
				}
				_ = conn.Write(ctx, websocket.MessageText, []byte(`{"type":"update","revision":1,"text":{"committed":"hello","tentative":"world"}}`))
				_, data, err = conn.Read(ctx)
				if err != nil {
					t.Error(err)
					return
				}
				if string(data) != `{"type":"finish"}` {
					t.Errorf("finish=%s", data)
				}
				_ = conn.Write(ctx, websocket.MessageText, []byte(`{"type":"finished","text":"hello world","usage":{"type":"duration","seconds":0.25}}`))
				_ = conn.Close(websocket.StatusNormalClosure, "")
			}))
			defer upstream.Close()
			endpoint := "realtime"
			if path == "/ws/voice" {
				endpoint = "voice"
			}
			app := inferenceTestApp(t, upstream.URL+"/v1", endpoint)
			front := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { defer close(proxyDone); app.ServeHTTP(w, r) }))
			defer front.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
			defer cancel()
			conn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(front.URL, "http")+path+"?model=public", &websocket.DialOptions{Subprotocols: []string{"realtime", "yallmp-key.client-secret"}})
			if err != nil {
				t.Fatal(err)
			}
			defer conn.CloseNow()
			if strings.Contains(conn.Subprotocol(), "client-secret") {
				t.Fatal("proxy echoed credential")
			}
			_ = conn.Write(ctx, websocket.MessageText, []byte(`{"type":"start","session":{"input_audio_transcription":{"model":"public"}}}`))
			if _, _, err = conn.Read(ctx); err != nil {
				t.Fatal(err)
			}
			_ = conn.Write(ctx, websocket.MessageBinary, []byte{0, 1, 255, 2})
			_, data, err := conn.Read(ctx)
			if err != nil || !bytes.Contains(data, []byte(`"tentative":"world"`)) {
				t.Fatalf("partial %s %v", data, err)
			}
			_ = conn.Write(ctx, websocket.MessageText, []byte(`{"type":"finish"}`))
			_, data, err = conn.Read(ctx)
			if err != nil || !bytes.Contains(data, []byte(`"hello world"`)) {
				t.Fatalf("final %s %v", data, err)
			}
			_, _, closeErr := conn.Read(ctx)
			if websocket.CloseStatus(closeErr) != websocket.StatusNormalClosure {
				t.Errorf("normal close lost: %v", closeErr)
			}
			for _, done := range []chan struct{}{proxyDone, upstreamDone} {
				select {
				case <-done:
				case <-ctx.Done():
					t.Fatal("session did not shut down")
				}
			}
			if app.Router.HealthSnapshot()[0].InflightRequests != 0 {
				t.Fatal("backend slot leaked")
			}
			records, err := app.LogDB.List(context.Background(), 10, 0)
			if err != nil || len(records) != 1 || records[0].AudioSeconds != 0.25 {
				t.Fatalf("session usage %+v %v", records, err)
			}
		})
	}
}

func TestLiveASRBrowserCredentialsStayLocalWhenAuthDisabled(t *testing.T) {
	for _, headerName := range []string{"", "x-client-key"} {
		for _, prefix := range []string{"yallmp-key.", "openai-insecure-api-key."} {
			t.Run(headerName+"/"+prefix, func(t *testing.T) {
				const secret = "browser-client-secret"
				upstreamDone, proxyDone := make(chan struct{}), make(chan struct{})
				upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					defer close(upstreamDone)
					for key, values := range r.Header {
						for _, value := range values {
							if strings.Contains(value, secret) {
								t.Errorf("browser credential forwarded in %s", key)
							}
						}
					}
					if r.Header.Get("Authorization") != "Bearer upstream-secret" {
						t.Errorf("upstream authentication changed: %q", r.Header.Get("Authorization"))
					}
					conn, err := websocket.Accept(w, r, nil)
					if err != nil {
						t.Error(err)
						return
					}
					defer conn.CloseNow()
					_ = conn.Write(r.Context(), websocket.MessageText, []byte(`{"type":"finished","text":"ok"}`))
					_ = conn.Close(websocket.StatusNormalClosure, "")
				}))
				defer upstream.Close()
				app, err := New(&config.Config{
					Nodes:   []config.NodeEntry{{Name: "asr", Model: "public", APIBase: upstream.URL, APIKey: "upstream-secret", Endpoints: []string{"voice"}}},
					AppKeys: config.AppKeysConfig{Enabled: false, HeaderName: headerName},
				})
				if err != nil {
					t.Fatal(err)
				}
				front := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { defer close(proxyDone); app.ServeHTTP(w, r) }))
				defer front.Close()
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				conn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(front.URL, "http")+"/ws/voice?model=public", &websocket.DialOptions{Subprotocols: []string{"realtime", prefix + secret}})
				if err != nil {
					t.Fatal(err)
				}
				defer conn.CloseNow()
				if strings.Contains(conn.Subprotocol(), secret) {
					t.Fatal("browser credential echoed")
				}
				if _, _, err := conn.Read(ctx); err != nil {
					t.Fatal(err)
				}
				_, _, _ = conn.Read(ctx)
				for _, done := range []chan struct{}{upstreamDone, proxyDone} {
					select {
					case <-done:
					case <-ctx.Done():
						t.Fatal("session did not finish")
					}
				}
			})
		}
	}
}

func TestLiveASRModelCannotSwitch(t *testing.T) {
	_, err := rewriteASRModel([]byte(`{"type":"session.update","session":{"model":"unauthorized"}}`), "public", "native")
	if err == nil {
		t.Fatal("model switch accepted")
	}
	got, err := rewriteASRModel([]byte(`{"type":"session.update","session":{"type":"transcription"}}`), "public", "native")
	if err != nil || !bytes.Contains(got, []byte(`"model":"native"`)) {
		t.Fatalf("selected ASR model not injected: %s %v", got, err)
	}
	if _, err := rewriteASRModel([]byte(`{"type":"session.update","session":{"type":"realtime"}}`), "public", "native"); err == nil {
		t.Fatal("conversation session accepted on ASR endpoint")
	}
	unchanged := []byte(`{ "type": "input_audio_buffer.append", "audio": "AAA=" }`)
	got, err = rewriteASRModel(unchanged, "public", "native")
	if err != nil || !bytes.Equal(got, unchanged) {
		t.Fatalf("audio message changed: %s %v", got, err)
	}
}

func TestLiveASRCancellationReleasesBothAdmissionSlots(t *testing.T) {
	upstreamDone, proxyDone := make(chan struct{}, 2), make(chan struct{}, 2)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() { upstreamDone <- struct{}{} }()
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.CloseNow()
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		_ = conn.Write(ctx, websocket.MessageText, []byte(`{"type":"ready"}`))
		_, _, _ = conn.Read(ctx)
	}))
	defer upstream.Close()
	app := inferenceTestApp(t, upstream.URL, "voice")
	// Rebuild policy/admission with explicit client and backend concurrency.
	cfg := app.Config
	cfg.AppKeys.Keys[0].ConcurrencyLimit = 1
	cfg.Nodes[0].Limits.Concurrency = 1
	if err := app.applyConfig(cfg, false); err != nil {
		t.Fatal(err)
	}
	front := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() { proxyDone <- struct{}{} }()
		app.ServeHTTP(w, r)
	}))
	defer front.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	for range 2 {
		conn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(front.URL, "http")+"/ws/voice?model=public", &websocket.DialOptions{HTTPHeader: http.Header{"x-client-key": {"client-secret"}}})
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := conn.Read(ctx); err != nil {
			_ = conn.CloseNow()
			t.Fatal(err)
		}
		_ = conn.CloseNow()
		for _, done := range []chan struct{}{upstreamDone, proxyDone} {
			select {
			case <-done:
			case <-ctx.Done():
				t.Fatal("cancelled session retained its slots")
			}
		}
		if app.Router.HealthSnapshot()[0].InflightRequests != 0 {
			t.Fatal("inflight gauge leaked")
		}
	}
}

func TestLiveASRHandshakeFailoverAndAuthorization(t *testing.T) {
	var failed, successful atomic.Int32
	first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { failed.Add(1); w.WriteHeader(503) }))
	defer first.Close()
	second := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		successful.Add(1)
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.CloseNow()
		_ = conn.Write(r.Context(), websocket.MessageText, []byte(`{"type":"finished","text":"ok"}`))
		_ = conn.Close(websocket.StatusNormalClosure, "")
	}))
	defer second.Close()
	app, err := New(&config.Config{Nodes: []config.NodeEntry{{Name: "a", Model: "public", APIBase: first.URL, Endpoints: []string{"voice"}}, {Name: "b", Model: "public", APIBase: second.URL, Endpoints: []string{"voice"}}}, AppKeys: config.AppKeysConfig{Enabled: true, Keys: []config.AppKeyEntry{{KeyID: "k", Secret: "key", Models: []string{"public"}}}}})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{}, 2)
	front := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() { done <- struct{}{} }()
		app.ServeHTTP(w, r)
	}))
	defer front.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	url := "ws" + strings.TrimPrefix(front.URL, "http") + "/ws/voice?model=public"
	_, resp, err := websocket.Dial(ctx, url, nil)
	if err == nil || resp.StatusCode != 401 || failed.Load() != 0 || successful.Load() != 0 {
		t.Fatalf("unauthorized request reached a backend: %v %v", resp, err)
	}
	conn, _, err := websocket.Dial(ctx, url, &websocket.DialOptions{HTTPHeader: http.Header{"x-api-key": {"key"}}})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()
	_, data, err := conn.Read(ctx)
	if err != nil || !bytes.Contains(data, []byte(`"ok"`)) {
		t.Fatalf("fallback failed: %s %v", data, err)
	}
	_, _, _ = conn.Read(ctx)
	for range 2 {
		select {
		case <-done:
		case <-ctx.Done():
			t.Fatal("handshake/session handler did not finish")
		}
	}
	if failed.Load() != 1 || successful.Load() != 1 {
		t.Fatalf("unexpected attempts %d/%d", failed.Load(), successful.Load())
	}
	for _, health := range app.Router.HealthSnapshot() {
		if health.Name == "a" && health.FailureCount != 1 {
			t.Errorf("failed handshake not recorded: %+v", health)
		}
	}
}

func TestLiveASRMessageLimitClosesBothPeers(t *testing.T) {
	upstreamDone, proxyDone := make(chan struct{}), make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer close(upstreamDone)
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.CloseNow()
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		_ = conn.Write(ctx, websocket.MessageText, []byte(`{"type":"ready"}`))
		_, _, _ = conn.Read(ctx)
	}))
	defer upstream.Close()
	app := inferenceTestApp(t, upstream.URL, "voice")
	app.Config.ProxySettings.ASR.MaxMessageBytes = 64
	front := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { defer close(proxyDone); app.ServeHTTP(w, r) }))
	defer front.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(front.URL, "http")+"/ws/voice?model=public", &websocket.DialOptions{HTTPHeader: http.Header{"x-client-key": {"client-secret"}}})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()
	if _, _, err := conn.Read(ctx); err != nil {
		t.Fatal(err)
	}
	_ = conn.Write(ctx, websocket.MessageBinary, make([]byte, 128))
	_, _, err = conn.Read(ctx)
	if websocket.CloseStatus(err) != websocket.StatusMessageTooBig {
		t.Fatalf("oversized audio accepted: %v", err)
	}
	for _, done := range []chan struct{}{upstreamDone, proxyDone} {
		select {
		case <-done:
		case <-ctx.Done():
			t.Fatal("oversized frame retained the session")
		}
	}
}

func TestLiveASRBackendRateLimitRejectsBeforeHandshake(t *testing.T) {
	var hits atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits.Add(1); w.WriteHeader(500) }))
	defer upstream.Close()
	app := inferenceTestApp(t, upstream.URL, "voice")
	backend := app.Router.Backends["node"]
	backend.Limits.RPM = 1
	app.Router.Backends["node"] = backend
	if err := app.Admission.CheckRateLimit(backendAdmissionScope(backend), "", admission.RateLimits{RPM: 1}); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("GET", "/ws/voice?model=public", nil)
	req.Header.Set("Upgrade", "websocket")
	req.Header.Set("x-client-key", "client-secret")
	resp := httptest.NewRecorder()
	app.ServeHTTP(resp, req)
	if resp.Code != 429 || hits.Load() != 0 {
		t.Fatalf("rate limit: status=%d upstream hits=%d body=%s", resp.Code, hits.Load(), resp.Body.String())
	}
}

func TestNativeDecisionEndpoints(t *testing.T) {
	for _, tc := range []struct{ endpoint, request, response string }{
		{"systemone", `{"model":"public","state":{"ticket":"Refund"},"questions":{"billing":{"type":"noul"}}}`, `{"model":"native-model","answers":{"billing":{"type":"noul","noul":0.987654321}},"usage":{"input_tokens":12,"output_tokens":1}}`},
		{"decisions", `{"model":"public","input":"Refund","questions":[{"type":"predicate","instructions":"Billing?"}]}`, `{"model":"native-model","answers":[{"type":"refusal","name":null}],"usage":{"input_tokens":12,"output_tokens":1}}`},
	} {
		t.Run(tc.endpoint, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body map[string]any
				_ = json.NewDecoder(r.Body).Decode(&body)
				if r.URL.Path != "/v1/"+tc.endpoint || body["model"] != "native-model" {
					t.Errorf("%s %#v", r.URL, body)
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, tc.response)
			}))
			defer upstream.Close()
			app := inferenceTestApp(t, upstream.URL+"/v1", tc.endpoint)
			req := httptest.NewRequest("POST", "/v1/"+tc.endpoint, strings.NewReader(tc.request))
			req.Header.Set("x-client-key", "client-secret")
			resp := httptest.NewRecorder()
			app.ServeHTTP(resp, req)
			if resp.Code != 200 || resp.Body.String() != tc.response {
				t.Fatalf("%d %s", resp.Code, resp.Body.String())
			}
			records, err := app.LogDB.List(context.Background(), 10, 0)
			if err != nil || len(records) != 1 || records[0].TotalTokens != 13 {
				t.Fatalf("decision usage %+v %v", records, err)
			}
		})
	}
}
