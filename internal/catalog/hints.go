package catalog

import "fmt"

const maxSnapshotWarnings = 100

type Warning struct {
	Severity   string `json:"severity"`
	Code       string `json:"code"`
	Message    string `json:"message"`
	Model      string `json:"model,omitempty"`
	Provider   string `json:"provider,omitempty"`
	Node       string `json:"node,omitempty"`
	ErrorClass string `json:"error_class,omitempty"`
}

func Warnings(snapshot Snapshot) []Warning {
	warnings := make([]Warning, 0)
	add := func(warning Warning) bool {
		if len(warnings) >= maxSnapshotWarnings {
			return false
		}
		warnings = append(warnings, warning)
		return true
	}
	if snapshot.Enabled && !snapshot.ProbeEnabled {
		add(Warning{
			Severity: "info",
			Code:     "probe_off_no_data",
			Message:  "Catalog probes are not opted in; availability is based on configured routes only.",
		})
	}
	for _, model := range snapshot.Models {
		totalNodes := 0
		unavailableNodes := 0
		for _, provider := range model.Providers {
			for _, node := range provider.Nodes {
				totalNodes++
				if node.Status == StatusUnavailable {
					unavailableNodes++
				}
				if warning := nodeWarning(model.Model, provider.Provider, node); warning.Code != "" {
					if !add(warning) {
						return warnings
					}
				}
			}
		}
		if totalNodes > 0 && unavailableNodes == totalNodes {
			if !add(Warning{
				Severity: "warning",
				Code:     "all_nodes_unavailable",
				Message:  fmt.Sprintf("Model %q has no available catalog nodes.", model.Model),
				Model:    model.Model,
			}) {
				return warnings
			}
		}
	}
	return warnings
}

func nodeWarning(model, provider string, node NodeEntry) Warning {
	switch {
	case node.ErrorClass == ErrorAuth:
		return Warning{
			Severity:   "warning",
			Code:       "node_auth_error",
			Message:    fmt.Sprintf("Node %q probe failed authentication.", node.Node),
			Model:      model,
			Provider:   provider,
			Node:       node.Node,
			ErrorClass: node.ErrorClass,
		}
	case node.ErrorClass == ErrorRateLimited:
		return Warning{
			Severity:   "info",
			Code:       "node_rate_limited",
			Message:    fmt.Sprintf("Node %q probe was rate limited.", node.Node),
			Model:      model,
			Provider:   provider,
			Node:       node.Node,
			ErrorClass: node.ErrorClass,
		}
	case node.Status == StatusStale:
		return Warning{
			Severity: "info",
			Code:     "node_stale",
			Message:  fmt.Sprintf("Node %q probe data is stale.", node.Node),
			Model:    model,
			Provider: provider,
			Node:     node.Node,
		}
	default:
		return Warning{}
	}
}
