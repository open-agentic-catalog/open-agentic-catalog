package api

import (
	"strings"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
)

func TestPersonalLabelContract(t *testing.T) {
	doc, err := openapi3.NewLoader().LoadFromData(ControlV1)
	if err != nil {
		t.Fatal(err)
	}
	library := doc.Paths.Find("/api/v1/personal/items")
	item := doc.Paths.Find("/api/v1/personal/items/{itemId}")
	filter := library.Get.Parameters.GetByInAndName("query", "label")
	if filter == nil || filter.Schema == nil {
		t.Fatal("missing label query parameter schema")
	}
	schemas := map[string]*openapi3.Schema{
		"create": library.Post.RequestBody.Value.Content.Get("application/json").Schema.Value.Properties["label"].Value,
		"update": item.Patch.RequestBody.Value.Content.Get("application/json").Schema.Value.Properties["label"].Value,
		"read":   item.Get.Responses.Status(200).Value.Content.Get("application/json").Schema.Value.Properties["label"].Value,
		"filter": filter.Schema.Value,
	}
	for _, tc := range []struct {
		name, label string
		valid       bool
	}{
		{"empty", "", false},
		{"unicode spaces", " \u00a0\u1680\u2007\u2028\u2029\u202f\u205f\u3000", false},
		{"padded", "\u00a0 Café \u3000", true},
		{"byte order mark", "\ufeff", true},
		{"embedded control", "abc\u0085def", false},
		{"trailing newline", "label\n", false},
		{"at limit", strings.Repeat("🙂", 120), true},
		{"over limit", strings.Repeat("🙂", 121), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for use, schema := range schemas {
				if err := schema.VisitJSON(tc.label); (err == nil) != tc.valid {
					t.Errorf("%s label %q: expected valid=%t, got %v", use, tc.label, tc.valid, err)
				}
			}
		})
	}
}

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

func TestFollowShapesSeparateCommandsItemsAndCurrentFacts(t *testing.T) {
	doc, err := openapi3.NewLoader().LoadFromData(ControlV1)
	if err != nil {
		t.Fatal(err)
	}
	const id = "00000000-0000-7000-8000-000000000001"
	current := map[string]any{"releaseId": id, "revision": float64(2)}
	for _, tc := range []struct {
		schema string
		body   map[string]any
		valid  bool
	}{
		{"PersonalInput", map[string]any{"type": "follow", "listingId": id, "label": "restored"}, true},
		{"PersonalInput", map[string]any{"type": "follow", "listingId": id, "label": "interactive", "observed": current}, true},
		{"PersonalInput", map[string]any{"type": "follow", "listingId": id, "label": "mixed", "search": map[string]any{"text": ""}}, false},
		{"PersonalInput", map[string]any{"type": "search", "label": "mixed", "observed": current, "search": map[string]any{"text": ""}}, false},
		{"PersonalItem", map[string]any{"id": id, "type": "follow", "listingId": id, "label": "x", "createdAt": "2026-01-01T00:00:00Z", "updatedAt": "2026-01-01T00:00:00Z", "revision": float64(1), "acknowledged": current}, false},
		{"PersonalResolution", map[string]any{"status": "available", "change": "unacknowledged", "current": current}, true},
		{"PersonalResolution", map[string]any{"status": "unavailable", "current": current}, false},
		{"PersonalResolution", map[string]any{"status": "unavailable", "change": "changed"}, false},
		{"PersonalResolution", map[string]any{"status": "available", "change": "changed"}, false},
	} {
		e := doc.Components.Schemas[tc.schema].Value.VisitJSON(tc.body, openapi3.EnableJSONSchema2020())
		if (e == nil) != tc.valid {
			t.Fatalf("%s %#v: %v", tc.schema, tc.body, e)
		}
	}
}
