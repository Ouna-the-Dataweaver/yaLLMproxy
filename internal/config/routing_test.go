package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestSimpleRoutingExpandsWithoutPersistingGeneratedResources(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	text := `providers:
  hosted:
    api_base: https://example.test/v1
    api_key: ${ROUTING_TEST_KEY}
    models:
      - name: qwen
        upstream_model: vendor/qwen
      - llama
nodes:
  - name: gpu-a
    model: qwen
    api_base: http://localhost:8000/v1
    upstream_model: local/qwen
`
	if err := os.WriteFile(path, []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ROUTING_TEST_KEY", "secret")
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if issues := cfg.Validate(); len(issues) != 0 {
		t.Fatalf("issues: %+v", issues)
	}
	expanded, err := cfg.ExpandedRouting()
	if err != nil {
		t.Fatal(err)
	}
	if len(expanded.Nodes) != 3 || len(expanded.Models) != 2 {
		t.Fatalf("expanded: %+v", expanded)
	}
	if !reflect.DeepEqual(expanded.Models[0].Nodes, []string{"gpu-a", ProviderNodeName("hosted", "qwen")}) {
		t.Fatalf("qwen pool: %+v", expanded.Models[0])
	}
	if len(cfg.Nodes) != 1 || len(cfg.Models) != 0 {
		t.Fatal("expansion mutated source")
	}
	if err := cfg.Save(""); err != nil {
		t.Fatal(err)
	}
	saved, _ := os.ReadFile(path)
	if strings.Contains(string(saved), "provider/hosted") || strings.Contains(string(saved), "secret") || !strings.Contains(string(saved), "${ROUTING_TEST_KEY}") {
		t.Fatalf("invalid saved source: %s", saved)
	}
	// A provider edit must preserve credentials and replace selections, even
	// when converting the compact named-provider map to the older list shape.
	raw, err := LoadWithOptions(LoadOptions{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.UpdateRawProvider("hosted", map[string]any{"models": []any{"llama"}}); err != nil {
		t.Fatal(err)
	}
	entry, _ := raw.RawProvider("hosted")
	if entry["api_key"] != "${ROUTING_TEST_KEY}" {
		t.Fatalf("credential lost: %+v", entry)
	}
	expanded, err = raw.ExpandedRouting()
	if err != nil {
		t.Fatal(err)
	}
	if len(expanded.Nodes) != 2 || len(expanded.Models[0].Nodes) != 1 {
		t.Fatal("deselected generated node survived")
	}
	if err := raw.DeleteRawProvider("hosted"); err != nil {
		t.Fatal(err)
	}
	expanded, err = raw.ExpandedRouting()
	if err != nil || len(expanded.Nodes) != 1 {
		t.Fatalf("manual node not preserved: %v", err)
	}
}

func TestSimpleRoutingRejectsAmbiguousNames(t *testing.T) {
	for _, cfg := range []*Config{
		{Providers: []ProviderEntry{{Name: "p", Models: []ProviderModel{{Name: "m"}, {Name: "m"}}}}},
		{Providers: []ProviderEntry{{Name: "p", Models: []ProviderModel{{Name: "m"}}}}, Nodes: []NodeEntry{{Name: ProviderNodeName("p", "m")}}},
		{ModelList: []ModelEntry{{ModelName: "m"}}, Nodes: []NodeEntry{{Name: "n", Model: "m"}}},
	} {
		if _, err := cfg.ExpandedRouting(); err == nil {
			t.Fatalf("accepted ambiguity: %+v", cfg)
		}
	}
}

func TestExplicitPoolsDoNotExposeUpstreamNames(t *testing.T) {
	cfg := &Config{Nodes: []NodeEntry{{Name: "n", Model: "private-upstream"}}, Models: []ClientModelEntry{{Name: "public", Nodes: []string{"n"}, AllowedKeys: []string{"allowed"}}}}
	expanded, err := cfg.ExpandedRouting()
	if err != nil {
		t.Fatal(err)
	}
	if len(expanded.Models) != 1 || expanded.Models[0].Name != "public" || expanded.Models[0].Generated {
		t.Fatal("explicit pool changed")
	}
}

func TestSimpleRoutingExample(t *testing.T) {
	path, err := filepath.Abs("../../configs/config_simple_example.yaml")
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadWithOptions(LoadOptions{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	if issues := cfg.Validate(); len(issues) != 0 {
		t.Fatalf("example is invalid: %+v", issues)
	}
	expanded, err := cfg.ExpandedRouting()
	if err != nil {
		t.Fatal(err)
	}
	if len(expanded.Models) != 2 || len(expanded.Models[0].Nodes) != 3 {
		t.Fatalf("unexpected example pools: %+v", expanded.Models)
	}
}
