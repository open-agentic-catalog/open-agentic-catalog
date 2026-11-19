// Shared wire construction is inert; network fixtures remain functional-tagged.
package httpserver

import (
	"encoding/json"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func mcpWire(t *testing.T, version, method, name string, args any) []byte {
	t.Helper()
	params := map[string]any{}
	if name != "" {
		params["name"] = name
		params["arguments"] = args
	}
	if version >= "2026-07-28" {
		params["_meta"] = map[string]any{mcp.MetaKeyProtocolVersion: version, mcp.MetaKeyClientInfo: map[string]any{"name": "local-test", "version": "1"}, mcp.MetaKeyClientCapabilities: map[string]any{}, "test.example/extension": true}
	}
	if method == "initialize" {
		params = map[string]any{"protocolVersion": "2025-11-25", "capabilities": map[string]any{}, "clientInfo": map[string]any{"name": "legacy-test", "version": "1"}}
	}
	raw, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
