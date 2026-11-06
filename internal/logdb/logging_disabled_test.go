package logdb

import (
	"github.com/theio/yallmp-proxy/internal/config"
	"path/filepath"
	"testing"
)

func TestMasterLoggingDisabledPreservesResponseStateOnly(t *testing.T) {
	store, err := NewStore(&config.Config{ProxySettings: config.ProxySettings{Logging: config.LoggingConfig{Enabled: false}}, Database: map[string]any{"backend": "sqlite", "connection": map[string]any{"sqlite": map[string]any{"path": filepath.Join(t.TempDir(), "state.db")}}}})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if id, err := store.Insert(t.Context(), Record{ModelName: "alpha", Outcome: "error"}); err != nil || id != "" {
		t.Fatalf("request log = %q, err=%v", id, err)
	}
	if id, err := store.InsertError(t.Context(), ErrorRecord{ErrorType: "test", ErrorMessage: "private error"}); err != nil || id != "" {
		t.Fatalf("error log = %q, err=%v", id, err)
	}
	for _, table := range []string{"request_metadata", "request_attempts", "error_logs"} {
		var count int
		if err := store.db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&count); err != nil || count != 0 {
			t.Fatalf("%s rows=%d, err=%v", table, count, err)
		}
	}
	if err := store.UpsertResponseState(t.Context(), ResponseStateRecord{ID: "resp_off", Model: "alpha", Status: "completed", FullResponse: map[string]any{"id": "resp_off", "status": "completed"}}); err != nil {
		t.Fatal(err)
	}
	state, found, err := store.GetResponseState(t.Context(), "resp_off")
	if err != nil || !found || state.Status != "completed" {
		t.Fatalf("state=%#v, found=%v, err=%v", state, found, err)
	}
}
