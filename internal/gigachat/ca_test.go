package gigachat

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestCustomCATrustsCloudOAuthAndChat(t *testing.T) {
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/oauth" {
			io.WriteString(w, `{"access_token":"test-token","expires_at":4102444800}`)
			return
		}
		if r.Header.Get("Authorization") != "Bearer test-token" {
			t.Error("chat lost cloud token")
		}
		io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`)
	}))
	defer upstream.Close()
	ca := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: upstream.Certificate().Raw}), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := BuildConfig(map[string]any{"mode": "cloud", "api_key": "key", "api_base": upstream.URL, "auth_url": upstream.URL + "/oauth", "ca_cert": ca})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.CACertFile != ca {
		t.Fatal("lost CA certificate config")
	}
	backend := NewBackend(cfg)
	response, err := backend.Execute(context.Background(), map[string]any{"messages": []any{map[string]any{"role": "user", "content": "hello"}}}, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		t.Fatalf("custom CA request status=%d", response.StatusCode)
	}
}

func TestCustomCAWorksWithClientCertificate(t *testing.T) {
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if len(r.TLS.PeerCertificates) == 0 {
			t.Error("client certificate missing")
		}
		io.WriteString(w, "ok")
	}))
	server.TLS = &tls.Config{ClientAuth: tls.RequireAnyClientCert}
	server.StartTLS()
	defer server.Close()
	dir := t.TempDir()
	certFile := filepath.Join(dir, "client.pem")
	keyFile := filepath.Join(dir, "client.key")
	caFile := filepath.Join(dir, "ca.pem")
	cert := server.TLS.Certificates[0]
	key, err := x509.MarshalPKCS8PrivateKey(cert.PrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	for path, data := range map[string][]byte{certFile: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Certificate[0]}), keyFile: pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: key}), caFile: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw})} {
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	client, err := makeHTTPClient(Config{VerifySSL: true, CACertFile: caFile, ClientCertFile: certFile, ClientKeyFile: keyFile})
	if err != nil {
		t.Fatal(err)
	}
	defer client.CloseIdleConnections()
	resp, err := client.Get(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("mTLS status=%d", resp.StatusCode)
	}
}

func TestCustomCAInvalidFilesAndDisabledVerification(t *testing.T) {
	invalid := filepath.Join(t.TempDir(), "invalid.pem")
	if err := os.WriteFile(invalid, []byte("not a certificate"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{invalid, invalid + ".missing"} {
		if _, err := makeHTTPClient(Config{VerifySSL: true, CACertFile: path}); err == nil {
			t.Fatalf("accepted invalid CA: %s", path)
		}
	}
	client, err := makeHTTPClient(Config{VerifySSL: false, CACertFile: invalid + ".missing"})
	if err != nil {
		t.Fatal(err)
	}
	defer client.CloseIdleConnections()
	if !client.Transport.(*http.Transport).TLSClientConfig.InsecureSkipVerify {
		t.Fatal("explicit verification override lost")
	}
}
