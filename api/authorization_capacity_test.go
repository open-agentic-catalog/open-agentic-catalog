package api_test

import (
	"github.com/getkin/kin-openapi/openapi3"
	serviceapi "github.com/open-agentic-catalog/open-agentic-catalog/api"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"testing"
)

func TestCustomRoleRequestCapacityMatchesDelegableRegistry(t *testing.T) {
	document, err := openapi3.NewLoader().LoadFromData(serviceapi.ControlV1)
	if err != nil {
		t.Fatal(err)
	}
	permissions := []any{}
	for _, descriptor := range identity.Permissions() {
		if descriptor.Delegable {
			permissions = append(permissions, string(descriptor.Key))
		}
	}
	schema := document.Components.Schemas["RoleDefinitionInput"].Value
	if schema.Properties["permissions"].Value.MaxItems == nil || *schema.Properties["permissions"].Value.MaxItems != uint64(len(permissions)) {
		t.Fatalf("custom role capacity does not match %d delegable actions", len(permissions))
	}
	value := map[string]any{"name": "All delegable actions", "permissions": permissions, "reason": "Explicit finite authority"}
	if err := schema.VisitJSON(value, openapi3.EnableJSONSchema2020()); err != nil {
		t.Fatal("maximum valid role violates schema", err)
	}
	for _, invalid := range [][]any{{}, append(append([]any{}, permissions...), "access.manage")} {
		value["permissions"] = invalid
		if err := schema.VisitJSON(value, openapi3.EnableJSONSchema2020()); err == nil {
			t.Fatalf("schema accepted invalid permission count %d", len(invalid))
		}
	}
}
