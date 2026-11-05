package api

import (
	"github.com/getkin/kin-openapi/openapi3"
	"testing"
)

func TestPersonalContractsAreClosedAndKeepExactOrigins(t *testing.T) {
	doc, err := openapi3.NewLoader().LoadFromData(ControlV1)
	if err != nil {
		t.Fatal(err)
	}
	const id = "00000000-0000-7000-8000-000000000001"
	local := map[string]any{"origin": "local", "releaseId": id, "versionRevisionId": id}
	foreign := map[string]any{"origin": "foreign", "kind": "agent", "sourceId": id, "presentationGeneration": float64(1), "identifier": "urn:air:example.com:default:agent", "versionPresent": false}
	cases := []struct {
		value map[string]any
		valid bool
	}{{map[string]any{"type": "resource", "label": "Exact local", "reference": local}, true}, {map[string]any{"type": "resource", "label": "Exact external", "reference": foreign}, true}, {map[string]any{"type": "search", "label": "Supported criteria", "search": map[string]any{"text": "metadata", "origins": []any{"local"}}}, true}, {map[string]any{"type": "search", "label": "No transient state", "search": map[string]any{"cursor": "secret"}}, false}, {map[string]any{"type": "resource", "label": "No mixed origins", "reference": map[string]any{"origin": "local", "releaseId": id, "versionRevisionId": id, "sourceId": ""}}, false}, {map[string]any{"type": "resource", "label": "Exact presence", "reference": map[string]any{"origin": "foreign", "kind": "agent", "sourceId": id, "presentationGeneration": float64(1), "identifier": "urn:air:example.com:default:agent"}}, false}, {map[string]any{"type": "search", "label": "No credentials", "search": map[string]any{"text": "metadata"}, "token": "not-accepted"}, false}}
	for i, c := range cases {
		err := doc.Components.Schemas["PersonalInput"].Value.VisitJSON(c.value, openapi3.EnableJSONSchema2020())
		if (err == nil) != c.valid {
			t.Fatal(i, err)
		}
	}
	for _, value := range []map[string]any{{"status": "unavailable", "reference": local}, {"status": "available"}, {"status": "available", "reference": foreign}} {
		if err := doc.Components.Schemas["PersonalResolution"].Value.VisitJSON(value, openapi3.EnableJSONSchema2020()); err == nil {
			t.Fatal("invalid resolution admitted", value["status"])
		}
	}
}
func TestCurrentAccessScopeCapacityMatchesFiniteVocabulary(t *testing.T) {
	doc, err := openapi3.NewLoader().LoadFromData(ControlV1)
	if err != nil {
		t.Fatal(err)
	}
	scopes := doc.Components.Schemas["CurrentAccess"].Value.Properties["scopes"].Value
	if scopes.MaxItems == nil || *scopes.MaxItems != uint64(len(scopes.Items.Value.Enum)) {
		t.Fatal("scope capacity differs from the finite reported vocabulary")
	}
}
