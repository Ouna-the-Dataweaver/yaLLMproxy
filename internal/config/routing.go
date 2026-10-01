package config

import (
	"fmt"
	"net/url"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// ProviderEntries accepts both the original list and a map keyed by provider name.
type ProviderEntries []ProviderEntry

func (p *ProviderEntries) UnmarshalYAML(value *yaml.Node) error {
	if value.Kind != yaml.MappingNode {
		var entries []ProviderEntry
		if err := value.Decode(&entries); err != nil {
			return err
		}
		*p = entries
		return nil
	}
	var entries map[string]ProviderEntry
	if err := value.Decode(&entries); err != nil {
		return err
	}
	names := make([]string, 0, len(entries))
	for name := range entries {
		names = append(names, name)
	}
	sort.Strings(names)
	*p = nil
	for _, name := range names {
		entry := entries[name]
		if entry.Name != "" && entry.Name != name {
			return fmt.Errorf("provider %q has a conflicting name", name)
		}
		entry.Name = name
		*p = append(*p, entry)
	}
	return nil
}

// ProviderModel is either a model ID string or {name, upstream_model} for an alias.
type ProviderModel struct {
	Name          string `yaml:"name" json:"name"`
	UpstreamModel string `yaml:"upstream_model,omitempty" json:"upstream_model,omitempty"`
}

func (m *ProviderModel) UnmarshalYAML(value *yaml.Node) error {
	if value.Kind == yaml.ScalarNode {
		if value.Tag != "!!str" {
			return fmt.Errorf("provider model must be a string or a model object")
		}
		m.Name = value.Value
		return nil
	}
	type plain ProviderModel
	return value.Decode((*plain)(m))
}

func ProviderNodeName(provider, model string) string {
	return "provider/" + url.PathEscape(provider) + "/" + url.PathEscape(model)
}

// ExpandedRouting is a derived runtime view. It never mutates or persists the
// source configuration: provider selections remain the sole source of truth.
func (cfg *Config) ExpandedRouting() (*Config, error) {
	if cfg == nil {
		return nil, fmt.Errorf("config is nil")
	}
	if cfg.routingExpanded {
		return cfg, nil
	}
	out := *cfg
	out.routingExpanded = true
	out.Nodes = append([]NodeEntry(nil), cfg.Nodes...)
	out.Models = append([]ClientModelEntry(nil), cfg.Models...)
	claimed := map[string]bool{}
	explicit := map[string]bool{}
	legacy := map[string]bool{}
	names := map[string]bool{}
	for _, m := range cfg.ModelList {
		legacy[m.ModelName] = true
		names[m.ModelName] = true
	}
	for _, m := range cfg.Models {
		name := m.EffectiveName()
		if legacy[name] {
			return nil, fmt.Errorf("model %q is defined in both model_list and models", name)
		}
		explicit[name] = true
		for _, n := range m.Nodes {
			claimed[n] = true
		}
	}
	for _, n := range cfg.Nodes {
		if names[n.Name] {
			return nil, fmt.Errorf("node name %q conflicts with another node or legacy model", n.Name)
		}
		names[n.Name] = true
	}
	for _, p := range cfg.Providers {
		selected := map[string]bool{}
		for _, m := range p.Models {
			name := strings.TrimSpace(m.Name)
			if name == "" || name != m.Name {
				return nil, fmt.Errorf("provider %q has an empty or untrimmed model name", p.Name)
			}
			if selected[name] {
				return nil, fmt.Errorf("provider %q selects model %q more than once", p.Name, name)
			}
			selected[name] = true
			nodeName := ProviderNodeName(p.Name, name)
			if names[nodeName] {
				return nil, fmt.Errorf("generated node %q conflicts with a configured node", nodeName)
			}
			names[nodeName] = true
			out.Nodes = append(out.Nodes, NodeEntry{Name: nodeName, Provider: p.Name, Model: name, UpstreamModel: firstString(m.UpstreamModel, name), Generated: true})
		}
	}
	pools := map[string]int{}
	poolProtocols := map[string]string{}
	for i := range out.Nodes {
		n := &out.Nodes[i]
		if claimed[n.Name] {
			continue
		}
		name := strings.TrimSpace(n.Model)
		if name == "" {
			continue
		} // Legacy infrastructure-only nodes remain usable by explicit reference.
		if explicit[name] || legacy[name] {
			return nil, fmt.Errorf("automatic model %q conflicts with an explicit model; use a different public name or remove the explicit pool", name)
		}
		apiType := n.APIType
		for _, p := range cfg.Providers {
			if p.Name == n.Provider && apiType == "" {
				apiType = p.APIType
			}
		}
		if apiType == "passthrough" {
			continue
		}
		protocol := "chat"
		if strings.EqualFold(apiType, "anthropic") {
			protocol = "anthropic"
		}
		if previous := poolProtocols[name]; previous != "" && previous != protocol {
			return nil, fmt.Errorf("automatic model %q mixes native Anthropic and chat backends; use separate public names", name)
		}
		poolProtocols[name] = protocol
		if n.UpstreamModel == "" {
			n.UpstreamModel = firstString(n.TargetModel, name)
		}
		index, ok := pools[name]
		if !ok {
			index = len(out.Models)
			pools[name] = index
			out.Models = append(out.Models, ClientModelEntry{Name: name, Generated: true})
		}
		out.Models[index].Nodes = append(out.Models[index].Nodes, n.Name)
	}
	return &out, nil
}
