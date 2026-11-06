package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEngineConfigInheritanceAndRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	text := `providers:
  local:
    api_base: http://localhost:8000/v1
    engine: auto
    compatibility:
      enabled: true
      strict: true
      dp_size: 4
    models: [generated]
nodes:
  - name: manual
    provider: local
    model: model
    engine: sglang
    compatibility:
      strict: false
      thinking_budget: false
      dp_rank: 0
`
	if err := os.WriteFile(path, []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if issues := cfg.Validate(); len(issues) != 0 {
		t.Fatalf("issues: %+v", issues)
	}
	c := MergeEngineCompatibility(cfg.Providers[0].Compatibility, cfg.Nodes[0].Compatibility)
	if !c.IsEnabled() || c.IsStrict() || c.ThinkingBudget == nil || *c.ThinkingBudget || c.DPRank == nil || *c.DPRank != 0 || *c.DPSize != 4 {
		t.Fatalf("inheritance lost explicit overrides: %+v", c)
	}
	if err := cfg.Save(""); err != nil {
		t.Fatal(err)
	}
	reloaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.Nodes[0].Engine != "sglang" || reloaded.Nodes[0].Compatibility.Strict == nil || reloaded.Providers[0].Engine != "auto" {
		t.Fatal("round trip lost engine settings")
	}
	// The maps used by admin APIs also preserve typed settings.
	if providerEntryToMap(cfg.Providers[0])["engine"] != "auto" || nodeEntryToMap(cfg.Nodes[0])["compatibility"] == nil {
		t.Fatal("admin projection lost settings")
	}
}

func TestEngineConfigValidation(t *testing.T) {
	for _, text := range []string{
		`engine: typo`,
		"engine: vllm\ncompatibility:\n  dp_size: 0",
		"engine: sglang\ncompatibility:\n  dp_rank: -1",
		"engine: sglang\ncompatibility:\n  dp_size: 2\n  dp_rank: 2",
		"engine: auto\ncompatibility:\n  probe_base: relative",
	} {
		path := filepath.Join(t.TempDir(), "config.yaml")
		source := "providers:\n  p:\n    api_base: http://localhost:8000/v1\n"
		// YAML indent the settings below the provider.
		for _, line := range strings.Split(text, "\n") {
			source += "    " + line + "\n"
		}
		os.WriteFile(path, []byte(source), 0600)
		cfg, err := Load(path)
		if err == nil {
			found := false
			for _, issue := range cfg.Validate() {
				if issue.Code == "invalid_engine_compatibility" {
					found = true
				}
			}
			if !found {
				t.Fatalf("accepted invalid config %s", text)
			}
		}
	}
}
