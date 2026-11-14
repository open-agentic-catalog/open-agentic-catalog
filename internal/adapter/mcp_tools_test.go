package adapter

import (
	"encoding/json"
	"testing"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
)

func TestMCPToolMetadataValidationUsesLocalJSONInBothDeliveryForms(t *testing.T) {
	const raw = `{"tools":[{"name":"extract_pdf_tables","description":"Private declaration"}]}`
	registry := NewRegistry()
	for _, content := range []bool{false, true} {
		input := catalog.RepresentationInput{Role: catalog.RepresentationMetadata, MediaType: "Application/Vnd.Open-Agentic-Catalog.Mcp-Tools+Json; charset=utf-8", ProfileID: catalog.MCPToolsProfile}
		if content {
			input.Content = raw
		} else {
			input.Data = json.RawMessage(raw)
		}
		result := registry.Validate(t.Context(), catalog.KindMCPServer, input)
		if result.ProfileID != catalog.MCPToolsProfile || len(result.Findings) != 0 || result.NormalizedFacts["declaredToolCount"] != 1 || len(result.NormalizedFacts) != 1 {
			t.Fatal("private metadata lost or exposed in facts", result)
		}
	}
	for _, input := range []catalog.RepresentationInput{
		{Role: catalog.RepresentationPrimary, MediaType: catalog.MCPToolsMediaType, Data: json.RawMessage(raw)},
		{Role: catalog.RepresentationMetadata, MediaType: catalog.MCPToolsMediaType, Data: json.RawMessage(raw), URL: "https://never-contact.example/tools"},
		{Role: catalog.RepresentationMetadata, MediaType: catalog.MCPToolsMediaType, Data: json.RawMessage(raw), ProfileID: "other-profile"},
		{Role: catalog.RepresentationMetadata, MediaType: catalog.MCPToolsMediaType, Content: `{"tools":[{"name":"x","inputSchema":{}}]}`},
	} {
		result := registry.Validate(t.Context(), catalog.KindMCPServer, input)
		if !hasFinding(result.Findings, "MCP_TOOLS_INVALID") || len(result.NormalizedFacts) != 0 {
			t.Fatal("invalid private metadata admitted", result)
		}
	}
}
