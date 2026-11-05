package observability

import (
	"bytes"
	"fmt"
)

type mcpKey struct{ operation, outcome string }

// ObserveMCP counts finite HTTP requests separately from domain read outcomes.
// Arbitrary client names, tool names, arguments and identity never become labels.
func (r *Registry) ObserveMCP(operation, outcome string) {
	if r == nil {
		return
	}
	switch operation {
	case "protocol", "discover", "list", "call":
	default:
		return
	}
	switch outcome {
	case "completed", "rejected", "failed", "tool_error", "busy", "cancelled":
	default:
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.mcp == nil {
		r.mcp = make(map[mcpKey]uint64)
	}
	if r.mcp[mcpKey{operation, outcome}] == ^uint64(0) {
		r.activityInvalid = true
	}
	r.mcp[mcpKey{operation, outcome}]++
}
func (r *Registry) writeMCPMetrics(output *bytes.Buffer) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.mcp) == 0 {
		return
	}
	const name = "open_agentic_catalog_mcp_requests_total"
	writeMetricHeader(output, name, "Finite MCP HTTP request outcomes; completed protocol responses do not imply successful domain reads.", "counter")
	for _, operation := range []string{"protocol", "discover", "list", "call"} {
		for _, outcome := range []string{"completed", "rejected", "failed", "tool_error", "busy", "cancelled"} {
			if count, ok := r.mcp[mcpKey{operation, outcome}]; ok {
				fmt.Fprintf(output, "%s{operation=%q,outcome=%q} %d\n", name, operation, outcome, count)
			}
		}
	}
}
