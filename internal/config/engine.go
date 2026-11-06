package config

import (
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

// EngineCompatibility is opt-in. Pointers preserve explicit false/zero overrides
// when a node inherits settings from its provider.
type EngineCompatibility struct {
	Enabled        *bool  `yaml:"enabled,omitempty" json:"enabled,omitempty"`
	Strict         *bool  `yaml:"strict,omitempty" json:"strict,omitempty"`
	ThinkingBudget *bool  `yaml:"thinking_budget,omitempty" json:"thinking_budget,omitempty"`
	DPSize         *int   `yaml:"dp_size,omitempty" json:"dp_size,omitempty"`
	DPRank         *int   `yaml:"dp_rank,omitempty" json:"dp_rank,omitempty"`
	ProbeBase      string `yaml:"probe_base,omitempty" json:"probe_base,omitempty"`
}

func (c EngineCompatibility) IsEnabled() bool { return c.Enabled != nil && *c.Enabled }
func (c EngineCompatibility) IsStrict() bool  { return c.Strict != nil && *c.Strict }

func MergeEngineCompatibility(parent, child EngineCompatibility) EngineCompatibility {
	if child.Enabled != nil {
		parent.Enabled = child.Enabled
	}
	if child.Strict != nil {
		parent.Strict = child.Strict
	}
	if child.ThinkingBudget != nil {
		parent.ThinkingBudget = child.ThinkingBudget
	}
	if child.DPSize != nil {
		parent.DPSize = child.DPSize
	}
	if child.DPRank != nil {
		parent.DPRank = child.DPRank
	}
	if child.ProbeBase != "" {
		parent.ProbeBase = child.ProbeBase
	}
	return parent
}

func ParseEngineCompatibility(value any) (EngineCompatibility, error) {
	var result EngineCompatibility
	data, err := yaml.Marshal(value)
	if err == nil {
		err = yaml.Unmarshal(data, &result)
	}
	return result, err
}

func ValidateEngineCompatibility(engine string, c EngineCompatibility) error {
	switch engine {
	case "", "auto", "generic", "vllm", "sglang":
	default:
		return fmt.Errorf("engine must be auto, generic, vllm, or sglang")
	}
	if c.DPSize != nil && *c.DPSize < 1 {
		return fmt.Errorf("dp_size must be positive")
	}
	if c.DPRank != nil && (*c.DPRank < 0 || (c.DPSize != nil && *c.DPRank >= *c.DPSize)) {
		return fmt.Errorf("dp_rank must be non-negative and less than dp_size")
	}
	if c.ProbeBase != "" && !validHTTPURL(c.ProbeBase) {
		return fmt.Errorf("probe_base must be an absolute http or https URL")
	}
	return nil
}

func validateEngine(collector *validationCollector, path, engine string, c EngineCompatibility) {
	if err := ValidateEngineCompatibility(strings.TrimSpace(engine), c); err != nil {
		collector.add(path, "error", "invalid_engine_compatibility", err.Error())
	}
}
