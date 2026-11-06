package logstore

import (
	"github.com/theio/yallmp-proxy/internal/config"
	"os"
	"path/filepath"
	"testing"
)

func TestMasterLoggingDisabledDoesNotInitializePayloadStore(t *testing.T) {
	root := t.TempDir()
	for _, value := range []any{false, "false"} {
		path := filepath.Join(root, "requests")
		cfg := &config.Config{ProxySettings: config.ProxySettings{Logging: config.LoggingConfig{Enabled: value, FullRequestStore: map[string]any{"enabled": true, "path": path, "cleanup_on_startup": true}}}}
		if store := NewFullRequestStore(cfg); store != nil {
			t.Fatalf("disabled master returned store=%#v", store)
		}
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("payload directory exists, err=%v", err)
		}
	}
}
