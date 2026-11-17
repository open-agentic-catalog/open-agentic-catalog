package api

import (
	"encoding/json"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
)

func TestDeclaredMCPToolsSearchContract(t *testing.T) {
	document, err := openapi3.NewLoader().LoadFromData(ControlV1)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := Files.ReadFile("examples/mcp-tools-search.json")
	if err != nil {
		t.Fatal(err)
	}
	var example map[string]any
	if err := json.Unmarshal(raw, &example); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"CatalogSearch", "PersonalCriteria", "CatalogExploreQuery"} {
		schema := document.Components.Schemas[name].Value
		body := map[string]any{"includeMcpTools": true, "text": "pdf tables"}
		if name != "CatalogExploreQuery" {
			body["origins"] = []any{"local"}
		}
		if err := schema.VisitJSON(body, openapi3.EnableJSONSchema2020()); err != nil {
			t.Fatal(name, err)
		}
		invalids := []map[string]any{{"includeMcpTools": nil}, {"includeMcpTools": true}}
		for _, fields := range []map[string]any{{"kinds": []any{"agent"}}, {"agentProtocol": "JSONRPC"}, {"text": ""}, {"origins": []any{"live"}}} {
			invalid := map[string]any{}
			for key, value := range body {
				invalid[key] = value
			}
			for key, value := range fields {
				invalid[key] = value
			}
			invalids = append(invalids, invalid)
		}
		if name != "CatalogExploreQuery" {
			invalids = append(invalids, map[string]any{"includeMcpTools": true, "text": "pdf"})
		}
		for _, invalid := range invalids {
			if err := schema.VisitJSON(invalid, openapi3.EnableJSONSchema2020()); err == nil {
				t.Fatal(name, "accepted unsupported tool scope", invalid)
			}
		}
	}
	if err := document.Components.Schemas["CatalogSearch"].Value.VisitJSON(example, openapi3.EnableJSONSchema2020()); err != nil {
		t.Fatal("request example", err)
	}
}
