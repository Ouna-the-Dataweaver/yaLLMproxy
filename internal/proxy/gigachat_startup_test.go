package proxy

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/theio/yallmp-proxy/internal/config"
)

func gigaStreamRouter(t *testing.T, retries int, transport http.RoundTripper) *Router {
	t.Helper()
	router, err := NewRouter(&config.Config{
		ModelList: []config.ModelEntry{{ModelName: "giga", ModelParams: map[string]any{
			"api_type": "gigachat", "api_base": "https://giga.test/api/v1", "api_key": "fake", "auth_url": "https://giga.test/oauth", "model": "GigaChat-Max",
		}}},
		RouterSettings: config.RouterSettings{NumRetries: retries},
	})
	if err != nil {
		t.Fatal(err)
	}
	router.Client = &http.Client{Transport: transport}
	return router
}

func gigaHTTPResponse(status int, body io.ReadCloser) *http.Response {
	return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: body}
}

func gigaTokenResponse() *http.Response {
	return gigaHTTPResponse(http.StatusOK, io.NopCloser(strings.NewReader(`{"access_token":"fake","expires_at":4102444800000}`)))
}

func forwardGigaStream(ctx context.Context, router *Router) (ForwardResult, error) {
	payload := map[string]any{"model": "giga", "stream": true, "messages": []any{map[string]any{"role": "user", "content": "Hello"}}}
	body, _ := json.Marshal(payload)
	return router.ForwardWithOptions(ctx, "giga", "/v1/chat/completions", "", body, payload, true, nil, ForwardOptions{})
}

func TestGigaChatStreamRetriesHTTPStartupFailures(t *testing.T) {
	for _, stage := range []string{"/oauth", "/api/v1/chat/completions"} {
		for _, status := range []int{429, 503} {
			t.Run(stage+http.StatusText(status), func(t *testing.T) {
				attempts := 0
				router := gigaStreamRouter(t, 2, cancellationRoundTripper(func(req *http.Request) (*http.Response, error) {
					if req.URL.Path == stage {
						attempts++
						if attempts == 1 {
							return gigaHTTPResponse(status, io.NopCloser(strings.NewReader(`{"message":"try again"}`))), nil
						}
					}
					if req.URL.Path == "/oauth" {
						return gigaTokenResponse(), nil
					}
					return gigaHTTPResponse(200, io.NopCloser(strings.NewReader("data: {\"choices\":[{\"delta\":{\"content\":\"recovered\"}}]}\n\ndata: [DONE]\n\n"))), nil
				}))
				result, err := forwardGigaStream(context.Background(), router)
				if err != nil {
					t.Fatal(err)
				}
				defer result.Response.Body.Close()
				body, err := io.ReadAll(result.Response.Body)
				if err != nil || result.Response.StatusCode != 200 || attempts != 2 || strings.Count(string(body), "recovered") != 1 {
					t.Fatalf("attempts=%d status=%d err=%v body=%s", attempts, result.Response.StatusCode, err, body)
				}
			})
		}
	}
}

func TestGigaChatStreamExhaustedHTTPFailuresRemainJSON(t *testing.T) {
	for _, status := range []int{429, 422} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			attempts := 0
			router := gigaStreamRouter(t, 2, cancellationRoundTripper(func(req *http.Request) (*http.Response, error) {
				if req.URL.Path == "/oauth" {
					return gigaTokenResponse(), nil
				}
				attempts++
				return gigaHTTPResponse(status, io.NopCloser(strings.NewReader(`{"message":"rejected"}`))), nil
			}))
			result, err := forwardGigaStream(context.Background(), router)
			if err != nil {
				t.Fatal(err)
			}
			defer result.Response.Body.Close()
			wantAttempts := 1
			if status == 429 {
				wantAttempts = 2
			}
			var body map[string]any
			decodeErr := json.NewDecoder(result.Response.Body).Decode(&body)
			if decodeErr != nil || result.Response.StatusCode != status || result.Response.Header.Get("Content-Type") != "application/json" || attempts != wantAttempts {
				t.Fatalf("attempts=%d status=%d headers=%v err=%v body=%#v", attempts, result.Response.StatusCode, result.Response.Header, decodeErr, body)
			}
			if body["error"].(map[string]any)["code"] != float64(status) {
				t.Fatalf("error = %#v", body)
			}
		})
	}
}

type gigaFailingBody struct {
	data   string
	err    error
	closed bool
}

func (b *gigaFailingBody) Read(p []byte) (int, error) {
	n := copy(p, b.data)
	b.data = b.data[n:]
	if len(b.data) == 0 {
		return n, b.err
	}
	return n, nil
}

func (b *gigaFailingBody) Close() error { b.closed = true; return nil }

func TestGigaChatStreamRetriesOnlyBeforeFirstConvertedEvent(t *testing.T) {
	for _, beforeOutput := range []bool{false, true} {
		name := "after_output"
		if beforeOutput {
			name = "before_output"
		}
		t.Run(name, func(t *testing.T) {
			sentinel := errors.New("connection lost")
			failed := &gigaFailingBody{err: sentinel, data: "data: {\"choices\":[{\"delta\":{\"content\":\"partial\"}}]}\n\n"}
			if beforeOutput {
				failed.data = ": keepalive\n\ndata: {"
			}
			attempts := 0
			router := gigaStreamRouter(t, 2, cancellationRoundTripper(func(req *http.Request) (*http.Response, error) {
				if req.URL.Path == "/oauth" {
					return gigaTokenResponse(), nil
				}
				attempts++
				if attempts == 1 {
					return gigaHTTPResponse(200, failed), nil
				}
				return gigaHTTPResponse(200, io.NopCloser(strings.NewReader("data: {\"choices\":[{\"delta\":{\"content\":\"recovered\"}}]}\n\ndata: [DONE]\n\n"))), nil
			}))
			result, err := forwardGigaStream(context.Background(), router)
			if err != nil {
				t.Fatal(err)
			}
			body, readErr := io.ReadAll(result.Response.Body)
			_ = result.Response.Body.Close()
			if !failed.closed {
				t.Fatal("failed upstream body was not closed")
			}
			if beforeOutput {
				if attempts != 2 || readErr != nil || !strings.Contains(string(body), "recovered") {
					t.Fatalf("attempts=%d err=%v body=%s", attempts, readErr, body)
				}
			} else if attempts != 1 || !errors.Is(readErr, sentinel) || strings.Count(string(body), "partial") != 1 {
				t.Fatalf("attempts=%d err=%v body=%s", attempts, readErr, body)
			}
			if active := router.inflightCount("giga"); active != 0 {
				t.Fatalf("leaked backend capacity: %d", active)
			}
		})
	}
}

type gigaWaitingBody struct {
	ctx     context.Context
	reading chan struct{}
	closed  chan struct{}
}

func (b *gigaWaitingBody) Read([]byte) (int, error) {
	close(b.reading)
	<-b.ctx.Done()
	return 0, b.ctx.Err()
}

func (b *gigaWaitingBody) Close() error { close(b.closed); return nil }

func TestGigaChatStreamCancellationDuringPrimingClosesBody(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	body := &gigaWaitingBody{ctx: ctx, reading: make(chan struct{}), closed: make(chan struct{})}
	attempts := 0
	router := gigaStreamRouter(t, 2, cancellationRoundTripper(func(req *http.Request) (*http.Response, error) {
		if req.URL.Path == "/oauth" {
			return gigaTokenResponse(), nil
		}
		attempts++
		return gigaHTTPResponse(200, body), nil
	}))
	result := make(chan error, 1)
	go func() { _, err := forwardGigaStream(ctx, router); result <- err }()
	select {
	case <-body.reading:
	case <-time.After(time.Second):
		t.Fatal("startup did not read the upstream body")
	}
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) || attempts != 1 || router.inflightCount("giga") != 0 {
			t.Fatalf("attempts=%d err=%v inflight=%d", attempts, err, router.inflightCount("giga"))
		}
	case <-time.After(time.Second):
		t.Fatal("cancellation did not stop startup")
	}
	select {
	case <-body.closed:
	default:
		t.Fatal("cancelled startup body was not closed")
	}
}
