package api

import (
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
	"go.yaml.in/yaml/v3"
)

// Keep parameter components directly usable by editors and generators that do
// not follow a second Reference Object when checking a path template.
func TestControlParameterComponentsAreConcrete(t *testing.T) {
	t.Parallel()
	var document map[string]any
	if err := yaml.Unmarshal(ControlV1, &document); err != nil {
		t.Fatal(err)
	}
	parameters := mapValue(t, mapValue(t, document, "components"), "parameters")
	for name := range parameters {
		parameter := mapValue(t, parameters, name)
		if _, alias := parameter["$ref"]; alias || parameter["name"] == nil || parameter["in"] == nil {
			t.Errorf("parameter component %s must define name and in directly", name)
		}
	}
}

func TestControlBodyBearingCommandsUsePOST(t *testing.T) {
	t.Parallel()
	document, err := openapi3.NewLoader().LoadFromData(ControlV1)
	if err != nil {
		t.Fatal(err)
	}
	for path, item := range document.Paths.Map() {
		for method, operation := range item.Operations() {
			if operation.RequestBody != nil && method != "POST" && method != "PATCH" && method != "PUT" {
				t.Errorf("%s %s has a body without portable HTTP request-content semantics", method, path)
			}
		}
	}
	for path, operationID := range map[string]string{
		"/api/v1/role-assignments/{assignmentId}:revoke": "revokeRoleAssignment",
		"/api/v1/resources/{resourceId}:delete":          "deleteResource",
		"/api/v1/federation/sources/{sourceId}:remove":   "removeFederationSource",
	} {
		item := document.Paths.Value(path)
		if item == nil || item.Post == nil || item.Post.OperationID != operationID || item.Post.RequestBody == nil || !item.Post.RequestBody.Value.Required {
			t.Errorf("%s must retain its POST operation identity and required command body", path)
		}
	}
	webhook := document.Paths.Value("/api/v1/webhooks/{webhookId}")
	if webhook == nil || webhook.Delete == nil || webhook.Delete.RequestBody != nil {
		t.Fatal("webhook disable must remain a bodyless DELETE")
	}
}

func TestSourceRemovalRequestExampleMatchesItsSchema(t *testing.T) {
	t.Parallel()
	document, err := openapi3.NewLoader().LoadFromData(ControlV1)
	if err != nil {
		t.Fatal(err)
	}
	operation := document.Paths.Value("/api/v1/federation/sources/{sourceId}:remove").Post
	media := operation.RequestBody.Value.Content["application/json"]
	if media.Example == nil {
		t.Fatal("source removal must have a schema-valid illustrative request example")
	}
	// Singular example is valid on a Media Type Object, unlike the deprecated
	// Schema Object annotation. Keep its actual digest and reason constraints.
	if err := media.Schema.Value.VisitJSON(media.Example, openapi3.EnableJSONSchema2020()); err != nil {
		t.Fatalf("source removal example violates the request contract: %v", err)
	}
}

// An explicit empty items schema must not narrow JSON-LD context values. The
// failed Skill inspection response still forbids partial file inventories.
func TestControlExplicitArrayItemsPreserveStandardsBoundaries(t *testing.T) {
	t.Parallel()
	document, err := openapi3.NewLoader().LoadFromData(ControlV1)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"LiveSearch", "ARDSearch", "ARDExplore"} {
		schema := document.Components.Schemas[name].Value
		if name != "LiveSearch" {
			schema = schema.Properties["query"].Value
		}
		array := schema.Properties["@context"].Value.OneOf[2].Value
		if array.Items == nil {
			t.Fatalf("%s context array has no explicit items schema", name)
		}
		for _, value := range []any{[]any{}, []any{"https://example.com/context", map[string]any{"term": "https://example.com/term"}, nil}} {
			if err := array.VisitJSON(value, openapi3.EnableJSONSchema2020()); err != nil {
				t.Errorf("%s context array was narrowed: %v", name, err)
			}
		}
	}
	files := document.Components.Schemas["SkillPackageInspection"].Value.OneOf[1].Value.Properties["files"].Value
	if files.Items == nil || files.VisitJSON([]any{}, openapi3.EnableJSONSchema2020()) != nil || files.VisitJSON([]any{map[string]any{}}, openapi3.EnableJSONSchema2020()) == nil {
		t.Fatal("failed inspection must require an empty file array when present")
	}
}
