package adminui

import (
	"io/fs"
	"strings"
	"testing"
)

func TestEmbeddedProductionBundle(t *testing.T) {
	index, err := fs.ReadFile(Assets(), "index.html")
	if err != nil {
		t.Fatalf("read embedded index: %v", err)
	}
	if !strings.Contains(string(index), `id="root"`) {
		t.Fatalf("embedded index does not contain the React root")
	}
	entries, err := fs.ReadDir(Assets(), "assets")
	if err != nil {
		t.Fatalf("read embedded assets: %v", err)
	}
	if len(entries) < 2 {
		t.Fatalf("embedded assets = %d, want at least JS and CSS", len(entries))
	}
}
