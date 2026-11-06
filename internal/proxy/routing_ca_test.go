package proxy

import (
	"testing"

	"github.com/theio/yallmp-proxy/internal/config"
)

func TestGigaChatCACertProviderInheritanceAndNodeOverride(t *testing.T) {
	router, err := NewRouter(&config.Config{
		Providers: []config.ProviderEntry{{Name: "p", APIType: "gigachat", APIBase: "https://example.test", APIKey: "key", Mode: "cloud", CACert: "/certs/provider.pem"}},
		Nodes:     []config.NodeEntry{{Name: "inherited", Model: "m", Provider: "p"}, {Name: "override", Model: "m", Provider: "p", CACert: "/certs/node.pem"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]string{"inherited": "/certs/provider.pem", "override": "/certs/node.pem"} {
		backend, ok := router.GetBackend(name)
		if !ok || backend.GigaChat == nil || backend.GigaChat.Config.CACertFile != want {
			t.Fatalf("node=%s backend=%+v", name, backend)
		}
	}
}
