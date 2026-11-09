package api

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
)

func TestAuthoringIdentitySchemaCharacterLimits(t *testing.T) {
	t.Parallel()
	document, err := openapi3.NewLoader().LoadFromData(ControlV1)
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []struct {
		schema, property string
		limit            int
	}{{"CreateResource", "canonicalIdentifier", 512}, {"Resource", "canonicalIdentifier", 512}, {"CatalogResourceSummary", "canonicalIdentifier", 512}, {"CreateVersion", "version", 128}, {"Relationship", "targetIdentifier", 512}, {"Relationship", "targetVersion", 128}} {
		schema := document.Components.Schemas[field.schema].Value.Properties[field.property].Value
		for _, character := range []string{"x", "é", "🙂"} {
			for _, count := range []int{field.limit - 1, field.limit, field.limit + 1} {
				t.Run(fmt.Sprintf("%s/%s/%s/%d", field.schema, field.property, character, count), func(t *testing.T) {
					err := schema.VisitJSON(strings.Repeat(character, count), openapi3.EnableJSONSchema2020())
					if (err == nil) != (count <= field.limit) {
						t.Fatalf("schema character admission: %v", err)
					}
				})
			}
		}
	}
}

// Local Resources and releases accept opaque identities; adoption requires AIR.
func TestLocalAndAdoptedResourceIdentifierSchemas(t *testing.T) {
	t.Parallel()
	document, err := openapi3.NewLoader().LoadFromData(ControlV1)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"CreateResource", "Resource", "CatalogResourceSummary"} {
		schema := document.Components.Schemas[name].Value.Properties["canonicalIdentifier"].Value
		for _, value := range []string{"custom:draft", "Café:e\u0301:🙂", strings.Repeat("🙂", 512), "urn:air:catalog.example.com:default:agent"} {
			if err := schema.VisitJSON(value, openapi3.EnableJSONSchema2020()); err != nil {
				t.Errorf("%s refused private identity %q: %v", name, value, err)
			}
		}
		for _, value := range []any{"", strings.Repeat("🙂", 513), nil, 42} {
			if err := schema.VisitJSON(value, openapi3.EnableJSONSchema2020()); err == nil {
				t.Errorf("%s accepted invalid identity %v", name, value)
			}
		}
	}
	// Adoption preserves the full metadata shape and its separate AIR constraint.
	resource := map[string]any{"id": "00000000-0000-7000-8000-000000000001", "tenantId": "00000000-0000-7000-8000-000000000001", "namespaceId": "00000000-0000-7000-8000-000000000001", "canonicalIdentifier": "urn:air:catalog.example.com:default:agent", "kind": "agent", "createdBy": "author", "revision": 1, "createdAt": "2026-11-09T00:00:00Z", "updatedAt": "2026-11-09T00:00:00Z"}
	adopted := document.Components.Schemas["ObservationAdoption"].Value.Properties["resource"].Value
	private := document.Components.Schemas["Resource"].Value
	if !reflect.DeepEqual(adopted.Required, private.Required) || len(adopted.Properties) != len(private.Properties) {
		t.Fatal("adopted Resource lost required authoring metadata")
	}
	for name, field := range private.Properties {
		other, ok := adopted.Properties[name]
		if !ok || name != "canonicalIdentifier" && !reflect.DeepEqual(field.Value, other.Value) {
			t.Fatalf("adopted Resource changed metadata field %s", name)
		}
	}
	record := document.Components.Schemas["CatalogRecord"].Value.Properties["resource"].Value
	if err := record.VisitJSON(resource, openapi3.EnableJSONSchema2020()); err != nil {
		t.Fatal("local AIR record control failed", err)
	}
	resource["canonicalIdentifier"] = "custom:draft"
	if err := record.VisitJSON(resource, openapi3.EnableJSONSchema2020()); err != nil {
		t.Fatal("local opaque release record refused", err)
	}
	if err := adopted.VisitJSON(resource, openapi3.EnableJSONSchema2020()); err == nil {
		t.Fatal("observation adoption accepted an opaque identity")
	}
	resource["canonicalIdentifier"] = "urn:air:catalog.example.com:default:agent"
	if err := adopted.VisitJSON(resource, openapi3.EnableJSONSchema2020()); err != nil {
		t.Fatal("adopted AIR control failed", err)
	}
}
