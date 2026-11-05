package api

import (
	"fmt"
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
	}{{"CreateResource", "canonicalIdentifier", 512}, {"CreateVersion", "version", 128}, {"Relationship", "targetIdentifier", 512}, {"Relationship", "targetVersion", 128}} {
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
