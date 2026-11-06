package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCACertConfigRoundTripAndAdminMasking(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	source := []byte("providers:\n  p:\n    api_type: gigachat\n    api_base: https://example.test\n    ca_cert: /certs/provider.pem\nnodes:\n  - name: n\n    provider: p\n    model: m\n    ca_cert: /certs/node.pem\n")
	if err := os.WriteFile(path, source, 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Providers[0].CACert != "/certs/provider.pem" || cfg.Nodes[0].CACert != "/certs/node.pem" {
		t.Fatal("lost typed CA fields")
	}
	if providerEntryToMap(cfg.Providers[0])["ca_cert"] != "/certs/provider.pem" || nodeEntryToMap(cfg.Nodes[0])["ca_cert"] != "/certs/node.pem" {
		t.Fatal("CA projection lost settings")
	}
	if cfg.AdminProviders()[0]["ca_cert"] == "/certs/provider.pem" || cfg.AdminNodes()[0]["ca_cert"] == "/certs/node.pem" {
		t.Fatal("CA path not masked")
	}
	if err := cfg.Save(""); err != nil {
		t.Fatal(err)
	}
	reloaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.Providers[0].CACert != cfg.Providers[0].CACert || reloaded.Nodes[0].CACert != cfg.Nodes[0].CACert {
		t.Fatal("CA fields lost on save")
	}
}
