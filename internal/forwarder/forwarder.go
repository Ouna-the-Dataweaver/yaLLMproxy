package forwarder

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/theio/yallmp-proxy/internal/config"
)

type Settings struct {
	Enabled      bool
	ListenHost   string
	ListenPort   int
	TargetScheme string
	TargetHost   string
	TargetPort   int
	PreserveHost bool
	Timeout      time.Duration
}

type TCPSettings struct {
	Enabled       bool
	ListenHost    string
	ListenPort    int
	TargetHost    string
	TargetPort    int
	BufferSize    int
	AcceptTimeout time.Duration
	DialTimeout   time.Duration
}

func (s TCPSettings) ListenAddr() string {
	return net.JoinHostPort(s.ListenHost, strconv.Itoa(s.ListenPort))
}

func (s TCPSettings) TargetAddr() string {
	return net.JoinHostPort(s.TargetHost, strconv.Itoa(s.TargetPort))
}

type TCPServer struct {
	Settings TCPSettings
	Listener net.Listener
}

func (s Settings) ListenAddr() string {
	return net.JoinHostPort(s.ListenHost, strconv.Itoa(s.ListenPort))
}

func (s Settings) TargetBase() string {
	return fmt.Sprintf("%s://%s", s.TargetScheme, net.JoinHostPort(s.TargetHost, strconv.Itoa(s.TargetPort)))
}

func SettingsFromConfig(cfg *config.Config) Settings {
	proxyHost := "127.0.0.1"
	proxyPort := config.DefaultServerPort
	if cfg != nil {
		if cfg.ProxySettings.Server.Host != "" {
			proxyHost = cfg.ProxySettings.Server.Host
		}
		if cfg.ProxySettings.Server.Port != 0 {
			proxyPort = cfg.ProxySettings.Server.Port
		}
	}
	if proxyHost == "0.0.0.0" || proxyHost == "::" {
		proxyHost = "127.0.0.1"
	}

	raw := map[string]any{}
	if cfg != nil && cfg.HTTPForwarderSettings != nil {
		raw = cfg.HTTPForwarderSettings
	}
	listen := mapValue(raw["listen"])
	target := mapValue(raw["target"])
	settings := Settings{
		Enabled:      config.ParseBool(raw["enabled"]),
		ListenHost:   stringValue(listen["host"], "0.0.0.0"),
		ListenPort:   intValue(listen["port"], 6969),
		TargetScheme: stringValue(target["scheme"], "http"),
		TargetHost:   stringValue(target["host"], proxyHost),
		TargetPort:   intValue(target["port"], proxyPort),
		PreserveHost: true,
	}
	if _, ok := raw["preserve_host"]; ok {
		settings.PreserveHost = config.ParseBool(raw["preserve_host"])
	}
	if timeout := floatValue(raw["timeout_seconds"], 0); timeout > 0 {
		settings.Timeout = time.Duration(timeout * float64(time.Second))
	}

	settings.Enabled = boolEnv("YALLMP_HTTP_FORWARDER_ENABLED", settings.Enabled)
	settings.ListenHost = stringEnv("HTTP_FORWARD_LISTEN_HOST", settings.ListenHost)
	settings.ListenPort = intEnv("HTTP_FORWARD_LISTEN_PORT", settings.ListenPort)
	settings.TargetScheme = stringEnv("HTTP_FORWARD_TARGET_SCHEME", settings.TargetScheme)
	settings.TargetHost = stringEnv("HTTP_FORWARD_TARGET_HOST", settings.TargetHost)
	settings.TargetPort = intEnv("HTTP_FORWARD_TARGET_PORT", settings.TargetPort)
	settings.PreserveHost = boolEnv("HTTP_FORWARD_PRESERVE_HOST", settings.PreserveHost)
	if timeoutEnv := os.Getenv("HTTP_FORWARD_TIMEOUT"); timeoutEnv != "" {
		if parsed, err := strconv.ParseFloat(timeoutEnv, 64); err == nil && parsed > 0 {
			settings.Timeout = time.Duration(parsed * float64(time.Second))
		}
	}
	return settings
}

func TCPSettingsFromConfig(cfg *config.Config) TCPSettings {
	proxyHost := "127.0.0.1"
	proxyPort := config.DefaultServerPort
	if cfg != nil {
		if cfg.ProxySettings.Server.Host != "" {
			proxyHost = cfg.ProxySettings.Server.Host
		}
		if cfg.ProxySettings.Server.Port != 0 {
			proxyPort = cfg.ProxySettings.Server.Port
		}
	}
	if proxyHost == "0.0.0.0" || proxyHost == "::" {
		proxyHost = "127.0.0.1"
	}

	raw := map[string]any{}
	if cfg != nil && cfg.ForwarderSettings != nil {
		raw = cfg.ForwarderSettings
	}
	listen := mapValue(raw["listen"])
	target := mapValue(raw["target"])
	settings := TCPSettings{
		Enabled:       config.ParseBool(raw["enabled"]),
		ListenHost:    stringValue(listen["host"], "0.0.0.0"),
		ListenPort:    intValue(listen["port"], 6969),
		TargetHost:    stringValue(target["host"], proxyHost),
		TargetPort:    intValue(target["port"], proxyPort),
		BufferSize:    intValue(raw["bufsize"], 65536),
		AcceptTimeout: time.Duration(floatValue(raw["accept_timeout_seconds"], 1) * float64(time.Second)),
		DialTimeout:   time.Duration(floatValue(raw["dial_timeout_seconds"], 10) * float64(time.Second)),
	}
	settings.Enabled = boolEnv("YALLMP_TCP_FORWARDER_ENABLED", settings.Enabled)
	settings.ListenHost = stringEnv("FORWARD_LISTEN_HOST", settings.ListenHost)
	settings.ListenPort = intEnv("FORWARD_LISTEN_PORT", settings.ListenPort)
	settings.TargetHost = stringEnv("FORWARD_TARGET_HOST", settings.TargetHost)
	settings.TargetPort = intEnv("FORWARD_TARGET_PORT", settings.TargetPort)
	settings.BufferSize = intEnv("FORWARD_BUFSIZE", settings.BufferSize)
	if timeoutEnv := os.Getenv("FORWARD_ACCEPT_TIMEOUT"); timeoutEnv != "" {
		if parsed, err := strconv.ParseFloat(timeoutEnv, 64); err == nil && parsed > 0 {
			settings.AcceptTimeout = time.Duration(parsed * float64(time.Second))
		}
	}
	if timeoutEnv := os.Getenv("FORWARD_DIAL_TIMEOUT"); timeoutEnv != "" {
		if parsed, err := strconv.ParseFloat(timeoutEnv, 64); err == nil && parsed > 0 {
			settings.DialTimeout = time.Duration(parsed * float64(time.Second))
		}
	}
	if settings.BufferSize <= 0 {
		settings.BufferSize = 65536
	}
	return settings
}

type Handler struct {
	Settings Settings
	Client   *http.Client
}

func NewHandler(settings Settings) *Handler {
	client := &http.Client{}
	if settings.Timeout > 0 {
		client.Timeout = settings.Timeout
	}
	return &Handler{Settings: settings, Client: client}
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	targetURL := h.targetURL(r)
	req, err := http.NewRequestWithContext(r.Context(), r.Method, targetURL, r.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	req.ContentLength = r.ContentLength
	req.Header = FilterHeaders(r.Header, h.Settings.PreserveHost)
	if h.Settings.PreserveHost {
		req.Host = r.Host
	}
	client := h.Client
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		http.Error(w, "Upstream connection failed", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	responseHeaders := FilterHeaders(resp.Header, true)
	copyHeaders(w.Header(), responseHeaders)
	w.WriteHeader(resp.StatusCode)
	if strings.Contains(strings.ToLower(resp.Header.Get("Content-Type")), "text/event-stream") {
		if flusher, ok := w.(http.Flusher); ok {
			_, _ = io.Copy(flushingWriter{Writer: w, Flusher: flusher}, resp.Body)
			return
		}
	}
	_, _ = io.Copy(w, resp.Body)
}

type flushingWriter struct {
	io.Writer
	http.Flusher
}

func (w flushingWriter) Write(p []byte) (int, error) {
	n, err := w.Writer.Write(p)
	if n > 0 {
		w.Flusher.Flush()
	}
	return n, err
}

func (h *Handler) targetURL(r *http.Request) string {
	path := r.URL.EscapedPath()
	if path == "" {
		path = "/"
	}
	target := strings.TrimRight(h.Settings.TargetBase(), "/") + path
	if r.URL.RawQuery != "" {
		target += "?" + r.URL.RawQuery
	}
	return target
}

func StartServer(ctx context.Context, settings Settings) (*http.Server, <-chan error) {
	handler := NewHandler(settings)
	server := &http.Server{
		Addr:              settings.ListenAddr(),
		Handler:           handler,
		ReadHeaderTimeout: 15 * time.Second,
	}
	errCh := make(chan error, 1)
	listener, err := net.Listen("tcp", settings.ListenAddr())
	if err != nil {
		errCh <- err
		close(errCh)
		return server, errCh
	}
	go func() {
		err := server.Serve(listener)
		if err == http.ErrServerClosed || (ctx.Err() != nil && errors.Is(err, net.ErrClosed)) {
			err = nil
		}
		errCh <- err
		close(errCh)
	}()
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
		_ = listener.Close()
	}()
	return server, errCh
}

func StartTCPServer(ctx context.Context, settings TCPSettings) (*TCPServer, <-chan error) {
	if settings.BufferSize <= 0 {
		settings.BufferSize = 65536
	}
	if settings.DialTimeout <= 0 {
		settings.DialTimeout = 10 * time.Second
	}
	listener, err := net.Listen("tcp", settings.ListenAddr())
	errCh := make(chan error, 1)
	if err != nil {
		errCh <- err
		close(errCh)
		return &TCPServer{Settings: settings}, errCh
	}
	server := &TCPServer{Settings: settings, Listener: listener}
	var connID atomic.Int64
	var wg sync.WaitGroup
	go func() {
		<-ctx.Done()
		_ = listener.Close()
	}()
	go func() {
		defer close(errCh)
		defer wg.Wait()
		for {
			if tcpListener, ok := listener.(*net.TCPListener); ok && settings.AcceptTimeout > 0 {
				_ = tcpListener.SetDeadline(time.Now().Add(settings.AcceptTimeout))
			}
			client, err := listener.Accept()
			if err != nil {
				if ctx.Err() != nil {
					errCh <- nil
					return
				}
				if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
					continue
				}
				errCh <- err
				return
			}
			id := connID.Add(1)
			wg.Add(1)
			go func() {
				defer wg.Done()
				handleTCPClient(ctx, settings, client, id)
			}()
		}
	}()
	return server, errCh
}

func handleTCPClient(ctx context.Context, settings TCPSettings, client net.Conn, _ int64) {
	defer client.Close()
	dialer := net.Dialer{Timeout: settings.DialTimeout}
	target, err := dialer.DialContext(ctx, "tcp", settings.TargetAddr())
	if err != nil {
		return
	}
	defer target.Close()
	setNoDelay(client)
	setNoDelay(target)

	done := make(chan struct{}, 2)
	go func() {
		pipeTCP(client, target, settings.BufferSize)
		closeWrite(target)
		done <- struct{}{}
	}()
	go func() {
		pipeTCP(target, client, settings.BufferSize)
		closeWrite(client)
		done <- struct{}{}
	}()
	completed := 0
	for completed < 2 {
		select {
		case <-ctx.Done():
			_ = client.Close()
			_ = target.Close()
			for completed < 2 {
				<-done
				completed++
			}
		case <-done:
			completed++
		}
	}
}

func pipeTCP(src net.Conn, dst net.Conn, bufsize int) int64 {
	if bufsize <= 0 {
		bufsize = 65536
	}
	written, _ := io.CopyBuffer(dst, src, make([]byte, bufsize))
	return written
}

func setNoDelay(conn net.Conn) {
	if tcp, ok := conn.(*net.TCPConn); ok {
		_ = tcp.SetNoDelay(true)
	}
}

func closeWrite(conn net.Conn) {
	if tcp, ok := conn.(interface{ CloseWrite() error }); ok {
		_ = tcp.CloseWrite()
	}
}

var hopByHopHeaders = map[string]bool{
	"connection":          true,
	"keep-alive":          true,
	"proxy-authenticate":  true,
	"proxy-authorization": true,
	"te":                  true,
	"trailer":             true,
	"trailers":            true,
	"transfer-encoding":   true,
	"upgrade":             true,
	"proxy-connection":    true,
}

func FilterHeaders(headers http.Header, preserveHost bool) http.Header {
	remove := map[string]bool{}
	for key, values := range headers {
		if strings.EqualFold(key, "Connection") {
			for _, value := range values {
				for _, part := range strings.Split(value, ",") {
					name := strings.ToLower(strings.TrimSpace(part))
					if name != "" {
						remove[name] = true
					}
				}
			}
		}
	}
	out := http.Header{}
	for key, values := range headers {
		lower := strings.ToLower(key)
		if hopByHopHeaders[lower] || remove[lower] {
			continue
		}
		if !preserveHost && lower == "host" {
			continue
		}
		for _, value := range values {
			out.Add(key, strings.TrimSpace(value))
		}
	}
	return out
}

func copyHeaders(dst, src http.Header) {
	for key, values := range src {
		for _, value := range values {
			dst.Add(key, value)
		}
	}
}

func mapValue(value any) map[string]any {
	if typed, ok := value.(map[string]any); ok {
		return typed
	}
	return map[string]any{}
}

func stringValue(value any, fallback string) string {
	if value == nil {
		return fallback
	}
	text := strings.TrimSpace(fmt.Sprint(value))
	if text == "" || text == "<nil>" {
		return fallback
	}
	return text
}

func intValue(value any, fallback int) int {
	switch typed := value.(type) {
	case int:
		if typed != 0 {
			return typed
		}
	case int64:
		if typed != 0 {
			return int(typed)
		}
	case float64:
		if typed != 0 {
			return int(typed)
		}
	case string:
		if parsed, err := strconv.Atoi(typed); err == nil && parsed != 0 {
			return parsed
		}
	}
	return fallback
}

func floatValue(value any, fallback float64) float64 {
	switch typed := value.(type) {
	case int:
		return float64(typed)
	case int64:
		return float64(typed)
	case float64:
		return typed
	case string:
		if parsed, err := strconv.ParseFloat(typed, 64); err == nil {
			return parsed
		}
	}
	return fallback
}

func stringEnv(name string, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}

func intEnv(name string, fallback int) int {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		if parsed, err := strconv.Atoi(value); err == nil {
			return parsed
		}
	}
	return fallback
}

func boolEnv(name string, fallback bool) bool {
	value := strings.TrimSpace(strings.ToLower(os.Getenv(name)))
	switch value {
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	default:
		return fallback
	}
}
