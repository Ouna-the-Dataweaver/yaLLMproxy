package forwarder

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/theio/yallmp-proxy/internal/config"
)

func TestPreserveHostForwardsOriginalHostHeader(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(r.Host))
	}))
	defer upstream.Close()
	forwarder := testForwarder(t, upstream.URL, true)
	req := httptest.NewRequest(http.MethodGet, "http://forwarder.local/echo-host", nil)
	req.Host = "public.test"
	resp := httptest.NewRecorder()
	forwarder.ServeHTTP(resp, req)
	if got := strings.TrimSpace(resp.Body.String()); got != "public.test" {
		t.Fatalf("host = %q", got)
	}
}

func TestStripHostHeaderWhenPreserveHostDisabled(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(r.Host))
	}))
	defer upstream.Close()
	forwarder := testForwarder(t, upstream.URL, false)
	req := httptest.NewRequest(http.MethodGet, "http://forwarder.local/echo-host", nil)
	req.Host = "public.test"
	resp := httptest.NewRecorder()
	forwarder.ServeHTTP(resp, req)
	if got := strings.TrimSpace(resp.Body.String()); got != strings.TrimPrefix(upstream.URL, "http://") {
		t.Fatalf("host = %q", got)
	}
}

func TestConnectionHeaderOverridesAreRemoved(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(r.Header.Get("X-Test")))
	}))
	defer upstream.Close()
	forwarder := testForwarder(t, upstream.URL, true)
	req := httptest.NewRequest(http.MethodGet, "http://forwarder.local/headers", nil)
	req.Header.Set("Connection", "X-Test")
	req.Header.Set("X-Test", "secret")
	resp := httptest.NewRecorder()
	forwarder.ServeHTTP(resp, req)
	if got := resp.Body.String(); got != "" {
		t.Fatalf("x-test leaked: %q", got)
	}
}

func TestNonStreamResponseStripsEncodingAndLength(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var buf bytes.Buffer
		gz := gzip.NewWriter(&buf)
		_, _ = gz.Write([]byte("hello"))
		_ = gz.Close()
		w.Header().Set("Content-Encoding", "gzip")
		w.Header().Set("Content-Length", fmt.Sprint(buf.Len()))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(buf.Bytes())
	}))
	defer upstream.Close()
	forwarder := testForwarder(t, upstream.URL, true)
	resp := httptest.NewRecorder()
	forwarder.ServeHTTP(resp, httptest.NewRequest(http.MethodGet, "http://forwarder.local/payload", nil))
	if got := resp.Header().Get("Content-Encoding"); got != "" {
		t.Fatalf("content-encoding = %q", got)
	}
	if got := resp.Header().Get("Content-Length"); got != "" {
		t.Fatalf("content-length was not stripped: %q", got)
	}
	if got := resp.Body.String(); got != "hello" {
		t.Fatalf("body = %q", got)
	}
}

func TestNonStreamResponsePreservesEncodingWhenClientRequestedIt(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var buf bytes.Buffer
		gz := gzip.NewWriter(&buf)
		_, _ = gz.Write([]byte("hello"))
		_ = gz.Close()
		w.Header().Set("Content-Encoding", "gzip")
		w.Header().Set("Content-Length", fmt.Sprint(buf.Len()))
		_, _ = w.Write(buf.Bytes())
	}))
	defer upstream.Close()
	forwarder := testForwarder(t, upstream.URL, true)
	req := httptest.NewRequest(http.MethodGet, "http://forwarder.local/payload", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	resp := httptest.NewRecorder()
	forwarder.ServeHTTP(resp, req)
	if got := resp.Header().Get("Content-Encoding"); got != "gzip" {
		t.Fatalf("content-encoding = %q", got)
	}
	reader, err := gzip.NewReader(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if string(decoded) != "hello" {
		t.Fatalf("decoded body = %q", decoded)
	}
}

func TestEventStreamIsForwardedWithoutBuffering(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: one\n\n"))
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		_, _ = w.Write([]byte("data: two\n\n"))
	}))
	defer upstream.Close()
	forwarder := testForwarder(t, upstream.URL, true)
	resp := httptest.NewRecorder()
	forwarder.ServeHTTP(resp, httptest.NewRequest(http.MethodGet, "http://forwarder.local/events", nil))
	if !strings.Contains(resp.Header().Get("Content-Type"), "text/event-stream") {
		t.Fatalf("content-type = %q", resp.Header().Get("Content-Type"))
	}
	if body := resp.Body.String(); !strings.Contains(body, "data: one") || !strings.Contains(body, "data: two") {
		t.Fatalf("body = %q", body)
	}
}

func TestEventStreamFlushesEachUpstreamChunk(t *testing.T) {
	release := make(chan struct{}, 1)
	defer func() {
		select {
		case release <- struct{}{}:
		default:
		}
	}()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: one\n\n"))
		w.(http.Flusher).Flush()
		<-release
		_, _ = w.Write([]byte("data: two\n\n"))
	}))
	defer upstream.Close()
	forwarderServer := httptest.NewServer(testForwarder(t, upstream.URL, true))
	defer forwarderServer.Close()

	type responseResult struct {
		resp *http.Response
		err  error
	}
	responseCh := make(chan responseResult, 1)
	go func() {
		resp, err := http.Get(forwarderServer.URL + "/events")
		responseCh <- responseResult{resp: resp, err: err}
	}()
	var resp *http.Response
	select {
	case result := <-responseCh:
		if result.err != nil {
			t.Fatal(result.err)
		}
		resp = result.resp
	case <-time.After(time.Second):
		t.Fatal("client did not receive the first flushed event")
	}
	defer resp.Body.Close()
	reader := bufio.NewReader(resp.Body)
	first, err := reader.ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	if first != "data: one\n" {
		t.Fatalf("first event line = %q", first)
	}
	release <- struct{}{}
}

func TestSettingsFromConfigAndEnv(t *testing.T) {
	t.Setenv("YALLMP_HTTP_FORWARDER_ENABLED", "true")
	t.Setenv("HTTP_FORWARD_LISTEN_PORT", "7777")
	settings := SettingsFromConfig(&config.Config{
		ProxySettings: config.ProxySettings{Server: config.ServerConfig{Host: "0.0.0.0", Port: 7979}},
		HTTPForwarderSettings: map[string]any{
			"listen": map[string]any{"host": "127.0.0.1", "port": 6969},
			"target": map[string]any{"scheme": "http"},
		},
	})
	if !settings.Enabled || settings.ListenPort != 7777 || settings.TargetHost != "127.0.0.1" || settings.TargetPort != 7979 {
		t.Fatalf("settings = %#v", settings)
	}
}

func TestStartServerShutsDownWithContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	settings := Settings{ListenHost: "127.0.0.1", ListenPort: 0, TargetScheme: "http", TargetHost: "127.0.0.1", TargetPort: 1}
	server, errCh := StartServer(ctx, settings)
	cancel()
	_ = server.Shutdown(context.Background())
	if err := <-errCh; err != nil {
		t.Fatalf("server error = %v", err)
	}
}

func TestStartServerWithCanceledContextDoesNotStartLate(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, errCh := StartServer(ctx, Settings{ListenHost: "127.0.0.1", ListenPort: 0, TargetScheme: "http", TargetHost: "127.0.0.1", TargetPort: 1})
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("server error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("server started after its context was already canceled")
	}
}

func TestTCPSettingsFromConfigAndEnv(t *testing.T) {
	t.Setenv("YALLMP_TCP_FORWARDER_ENABLED", "true")
	t.Setenv("FORWARD_LISTEN_PORT", "7778")
	settings := TCPSettingsFromConfig(&config.Config{
		ProxySettings: config.ProxySettings{Server: config.ServerConfig{Host: "0.0.0.0", Port: 7979}},
		ForwarderSettings: map[string]any{
			"listen": map[string]any{"host": "127.0.0.1", "port": 6969},
			"target": map[string]any{"host": "127.0.0.2"},
		},
	})
	if !settings.Enabled || settings.ListenPort != 7778 || settings.TargetHost != "127.0.0.2" || settings.TargetPort != 7979 {
		t.Fatalf("settings = %#v", settings)
	}
}

func TestTCPForwarderProxiesBytesAndShutsDownWithContext(t *testing.T) {
	target, targetDone := startTCPEchoServer(t)
	defer target.Close()
	targetHost, targetPort := splitAddr(t, target.Addr().String())

	ctx, cancel := context.WithCancel(context.Background())
	server, errCh := StartTCPServer(ctx, TCPSettings{
		ListenHost:    "127.0.0.1",
		ListenPort:    0,
		TargetHost:    targetHost,
		TargetPort:    targetPort,
		BufferSize:    8,
		AcceptTimeout: 20 * time.Millisecond,
		DialTimeout:   time.Second,
	})
	if server.Listener == nil {
		t.Fatalf("listener was not created")
	}

	conn, err := net.Dial("tcp", server.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Write([]byte("ping")); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 4)
	if _, err := io.ReadFull(conn, buf); err != nil {
		t.Fatal(err)
	}
	if string(buf) != "ping" {
		t.Fatalf("echo = %q", buf)
	}
	_ = conn.Close()
	<-targetDone

	cancel()
	if err := <-errCh; err != nil {
		t.Fatalf("tcp server error = %v", err)
	}
}

func TestTCPForwarderPreservesHalfCloseForResponse(t *testing.T) {
	target, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer target.Close()
	targetDone := make(chan struct{})
	go func() {
		defer close(targetDone)
		conn, err := target.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		request, err := io.ReadAll(conn)
		if err == nil && string(request) == "ping" {
			_, _ = conn.Write([]byte("pong"))
		}
	}()
	targetHost, targetPort := splitAddr(t, target.Addr().String())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	server, errCh := StartTCPServer(ctx, TCPSettings{
		ListenHost:  "127.0.0.1",
		ListenPort:  0,
		TargetHost:  targetHost,
		TargetPort:  targetPort,
		DialTimeout: time.Second,
	})
	conn, err := net.Dial("tcp", server.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.Write([]byte("ping")); err != nil {
		t.Fatal(err)
	}
	if err := conn.(*net.TCPConn).CloseWrite(); err != nil {
		t.Fatal(err)
	}
	response, err := io.ReadAll(conn)
	if err != nil {
		t.Fatal(err)
	}
	if string(response) != "pong" {
		t.Fatalf("response after half-close = %q", response)
	}
	<-targetDone
	cancel()
	if err := <-errCh; err != nil {
		t.Fatalf("tcp server error = %v", err)
	}
}

func TestPipeTCPCopiesWithoutClosingDestination(t *testing.T) {
	srcRead, srcWrite := net.Pipe()
	dstRead, dstWrite := net.Pipe()
	done := make(chan int64, 1)
	go func() {
		done <- pipeTCP(srcRead, dstWrite, 4)
	}()
	if _, err := srcWrite.Write([]byte("abc")); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 3)
	if _, err := io.ReadFull(dstRead, buf); err != nil {
		t.Fatal(err)
	}
	_ = srcWrite.Close()
	if copied := <-done; copied != 3 {
		t.Fatalf("copied = %d", copied)
	}
	readDone := make(chan error, 1)
	go func() {
		one := make([]byte, 1)
		_, err := io.ReadFull(dstRead, one)
		if err == nil && one[0] != 'z' {
			err = fmt.Errorf("read %q", one)
		}
		readDone <- err
	}()
	if _, err := dstWrite.Write([]byte("z")); err != nil {
		t.Fatalf("destination writer was closed by pipeTCP: %v", err)
	}
	if err := <-readDone; err != nil {
		t.Fatal(err)
	}
	_ = srcRead.Close()
	_ = dstRead.Close()
	_ = dstWrite.Close()
}

func testForwarder(t *testing.T, upstreamURL string, preserveHost bool) *Handler {
	t.Helper()
	hostPort := strings.TrimPrefix(upstreamURL, "http://")
	host, port, ok := strings.Cut(hostPort, ":")
	if !ok {
		t.Fatalf("unexpected upstream URL: %s", upstreamURL)
	}
	return NewHandler(Settings{
		TargetScheme: "http",
		TargetHost:   host,
		TargetPort:   mustAtoi(t, port),
		PreserveHost: preserveHost,
	})
}

func startTCPEchoServer(t *testing.T) (net.Listener, <-chan struct{}) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		_, _ = io.Copy(conn, conn)
	}()
	return listener, done
}

func splitAddr(t *testing.T, addr string) (string, int) {
	t.Helper()
	host, portText, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatal(err)
	}
	return host, mustAtoi(t, portText)
}

func mustAtoi(t *testing.T, value string) int {
	t.Helper()
	var out int
	if _, err := fmt.Sscanf(value, "%d", &out); err != nil {
		t.Fatal(err)
	}
	return out
}

var _ io.Writer = (*bytes.Buffer)(nil)
