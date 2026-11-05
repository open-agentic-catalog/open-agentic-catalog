package observability

import (
	"strings"
	"testing"
	"time"
)

func TestMCPMetricsSeparateProtocolAndToolFailuresWithClosedLabels(t *testing.T) {
	registry := New(time.Now)
	registry.ObserveMCP("list", "completed")
	registry.ObserveMCP("call", "tool_error")
	registry.ObserveMCP("call", "cancelled")
	registry.ObserveMCP("PRIVATE_TOOL", "completed")
	registry.ObserveMCP("call", "PRIVATE_ARGUMENT")
	var absent *Registry
	absent.ObserveMCP("call", "failed")
	body := string(registry.Render(t.Context()))
	for _, expected := range []string{`open_agentic_catalog_mcp_requests_total{operation="list",outcome="completed"} 1`, `open_agentic_catalog_mcp_requests_total{operation="call",outcome="tool_error"} 1`, `open_agentic_catalog_mcp_requests_total{operation="call",outcome="cancelled"} 1`} {
		if !strings.Contains(body, expected) {
			t.Fatal("missing MCP outcome", expected)
		}
	}
	if strings.Contains(body, "PRIVATE") {
		t.Fatal("unbounded metric labels")
	}
}
