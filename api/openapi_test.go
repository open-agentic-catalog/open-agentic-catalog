package api

import (
	"context"
	"encoding/json"
	"io/fs"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/audit"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/jsondoc"
	"go.yaml.in/yaml/v3"
)

var openAPIPathParameter = regexp.MustCompile(`\{[^}/]+\}`)

func TestControlOpenAPIIsValidAndOperationIDsAreUnique(t *testing.T) {
	loader := openapi3.NewLoader()
	document, err := loader.LoadFromData(ControlV1)
	if err != nil {
		t.Fatal(err)
	}
	if err := document.Validate(context.Background()); err != nil {
		t.Fatal(err)
	}
	version, err := os.ReadFile("../VERSION")
	if err != nil {
		t.Fatal(err)
	}
	if document.Info.Version != strings.TrimSpace(string(version)) {
		t.Fatalf("embedded OpenAPI version %q differs from VERSION %q", document.Info.Version, strings.TrimSpace(string(version)))
	}
	seen := make(map[string]string)
	operationCount := 0
	for _, tag := range document.Tags {
		if strings.TrimSpace(tag.Name) == "" || strings.TrimSpace(tag.Description) == "" {
			t.Fatalf("OpenAPI tag %q is missing a documentation description", tag.Name)
		}
	}
	for path, item := range document.Paths.Map() {
		for method, operation := range item.Operations() {
			operationCount++
			if strings.TrimSpace(operation.Summary) == "" {
				t.Fatalf("%s %s has no documentation summary", method, path)
			}
			if operation.OperationID == "" {
				t.Fatalf("%s %s has no operationId", method, path)
			}
			if prior, exists := seen[operation.OperationID]; exists {
				t.Fatalf("duplicate operationId %q on %s and %s %s", operation.OperationID, prior, method, path)
			}
			seen[operation.OperationID] = method + " " + path
		}
	}
	if operationCount < 83 {
		t.Fatalf("control contract has only %d operations", operationCount)
	}
}

func TestControlPatchRequestsAdvertiseOnlyImplementedJSON(t *testing.T) {
	t.Parallel()
	document, err := openapi3.NewLoader().LoadFromData(ControlV1)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/api/v1/namespaces/{namespaceId}", "/api/v1/publishers/{publisherId}", "/api/v1/webhooks/{webhookId}"} {
		t.Run(path, func(t *testing.T) {
			item := document.Paths.Find(path)
			if item == nil || item.Patch == nil || item.Patch.RequestBody == nil || item.Patch.RequestBody.Value == nil {
				t.Fatal("PATCH request contract is missing")
			}
			body := item.Patch.RequestBody.Value
			media := body.Content.Get("application/json")
			if !body.Required || len(body.Content) != 1 || media == nil || media.Schema == nil || media.Schema.Value == nil {
				t.Fatalf("PATCH must require only application/json with an explicit schema; got %#v", body)
			}
		})
	}
}

func TestControlOpenAPIHasNoGenericSuccessShapesOrUnreviewedOpenObjects(t *testing.T) {
	var document map[string]any
	if err := yaml.Unmarshal(ControlV1, &document); err != nil {
		t.Fatal(err)
	}
	components := mapValue(t, document, "components")
	schemas := mapValue(t, components, "schemas")
	responses := mapValue(t, components, "responses")
	for _, name := range []string{"Object", "Created", "Updated", "Accepted", "Page"} {
		if _, exists := schemas[name]; exists {
			t.Errorf("generic success schema %q remains in the control contract", name)
		}
		if _, exists := responses[name]; exists {
			t.Errorf("generic success response %q remains in the control contract", name)
		}
	}

	approvedOpenObjects := map[string]struct{}{
		// MCP protocol envelopes and method parameters permit protocol extensions.
		// Catalog tool arguments and the OAuth metadata object remain closed.
		"components.schemas.MCPRequest":                   {},
		"components.schemas.MCPRequest.properties.params": {},
		"components.schemas.MCPReply":                     {},
		"paths./api/v1/observations/{observationId}/raw.get.responses.200.content.application/json.schema": {},
		"components.schemas.AuditEvent.properties.details":                                                 {},
		"components.schemas.AuditDetailsV1.properties.facts":                                               {},
		"components.schemas.Representation.properties.data":                                                {},
		"components.schemas.Representation.properties.normalizedFacts":                                     {},
		"components.schemas.Representation.properties.unknownProperties":                                   {},
		"components.schemas.ListingRevision.properties.extensions":                                         {},
		"components.schemas.LiveCatalogSearchEvidence.properties.entry":                                    {},
		"components.schemas.RepresentationInput.properties.data":                                           {},
		"components.schemas.CreateListingRevision.properties.extensions":                                   {},
		"components.schemas.LiveSearch.properties.@context.oneOf.1":                                        {},
		"components.schemas.LiveSearchResponse.properties.results.items.properties.entry":                  {},
		"components.schemas.ARDSearch.properties.query.properties.@context.oneOf.1":                        {},
		"components.schemas.ARDExplore.properties.query.properties.@context.oneOf.1":                       {},
		"components.schemas.ARDEntry":                                                                      {},
		"components.schemas.ARDEntry.properties.data":                                                      {},
		"components.schemas.ARDEntry.properties.metadata":                                                  {},
		"components.schemas.ARDError":                                                                      {},
		"components.schemas.ARDSearchResult":                                                               {},
		"components.schemas.ARDSearchResult.properties.data":                                               {},
		"components.schemas.ARDSearchResult.properties.metadata":                                           {},
		// Trust frameworks define their own extension members; Search preserves them.
		"components.schemas.ARDSearchResult.properties.trustManifest": {},
		// ARD D.3's authoritative RegistryReferral permits extension metadata.
		"components.schemas.ARDSearchResponse.properties.referrals.items": {},
		"components.schemas.AICatalogEntry.properties.extensions":         {},
	}
	openObjects := make(map[string]struct{})
	collectOpenAPIObjects(document, "", openObjects)
	for path := range openObjects {
		if _, approved := approvedOpenObjects[path]; !approved {
			t.Errorf("unreviewed open object at %s", path)
		}
	}
	for path := range approvedOpenObjects {
		if _, present := openObjects[path]; !present {
			t.Errorf("stale approved-open-object exception %s", path)
		}
	}
}

func TestWebhookSelectorsMatchThePublicEventSchema(t *testing.T) {
	var contract map[string]any
	if err := yaml.Unmarshal(ControlV1, &contract); err != nil {
		t.Fatal(err)
	}
	components := mapValue(t, contract, "components")
	schemas := mapValue(t, components, "schemas")
	webhookEventType := mapValue(t, schemas, "WebhookEventType")
	responseValues := stringValues(t, webhookEventType["enum"])
	webhookEventSelector := mapValue(t, schemas, "WebhookEventSelector")
	selectorAlternatives := arrayValue(t, webhookEventSelector, "oneOf")
	if len(selectorAlternatives) != 2 {
		t.Fatalf("WebhookEventSelector alternatives = %#v", selectorAlternatives)
	}
	requestActionSchema, ok := selectorAlternatives[1].(map[string]any)
	if !ok {
		t.Fatalf("WebhookEventSelector action alternative is not an object: %#v", selectorAlternatives[1])
	}
	requestActions := stringValues(t, requestActionSchema["enum"])

	var eventSchema map[string]any
	raw, err := Files.ReadFile("schemas/catalog-event-v1.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &eventSchema); err != nil {
		t.Fatal(err)
	}
	properties := mapValue(t, eventSchema, "properties")
	publicTypeSchema := mapValue(t, properties, "type")
	publicTypes := stringValues(t, publicTypeSchema["enum"])
	dataSchema := mapValue(t, properties, "data")
	dataProperties := mapValue(t, dataSchema, "properties")
	publicActionSchema := mapValue(t, dataProperties, "action")
	publicActions := stringValues(t, publicActionSchema["enum"])

	if !slicesEqualStrings(responseValues, append([]string{"*"}, publicTypes...)) {
		t.Fatalf("WebhookEventType does not match the public event types: %#v", responseValues)
	}
	if !slicesEqualStrings(requestActions, publicActions) {
		t.Fatalf("WebhookEventSelector does not match the public event actions: %#v", requestActions)
	}
}

func TestVersionedAuditActionsMatchTheServiceRegistry(t *testing.T) {
	var contract map[string]any
	if err := yaml.Unmarshal(ControlV1, &contract); err != nil {
		t.Fatal(err)
	}
	components := mapValue(t, contract, "components")
	schemas := mapValue(t, components, "schemas")
	auditEvent := mapValue(t, schemas, "AuditEvent")
	conditions := arrayValue(t, auditEvent, "allOf")
	if len(conditions) != 1 {
		t.Fatalf("AuditEvent version conditions = %#v", conditions)
	}
	condition, ok := conditions[0].(map[string]any)
	if !ok {
		t.Fatalf("AuditEvent version condition is not an object: %#v", conditions[0])
	}
	properties := mapValue(t, condition, "properties")
	action := mapValue(t, properties, "action")
	if got, want := stringValues(t, action["enum"]), audit.Actions(); !slicesEqualStrings(got, want) {
		t.Fatalf("versioned AuditEvent actions = %#v, want %#v", got, want)
	}
}

func stringValues(t *testing.T, value any) []string {
	t.Helper()
	items, ok := value.([]any)
	if !ok {
		t.Fatalf("value is not an array: %#v", value)
	}
	result := make([]string, len(items))
	for index, item := range items {
		result[index], ok = item.(string)
		if !ok {
			t.Fatalf("array value is not a string: %#v", item)
		}
	}
	return result
}

func mapValue(t *testing.T, parent map[string]any, key string) map[string]any {
	t.Helper()
	value, ok := parent[key].(map[string]any)
	if !ok {
		t.Fatalf("%s is not an object: %#v", key, parent[key])
	}
	return value
}

func arrayValue(t *testing.T, parent map[string]any, key string) []any {
	t.Helper()
	value, ok := parent[key].([]any)
	if !ok {
		t.Fatalf("%s is not an array: %#v", key, parent[key])
	}
	return value
}

func slicesEqualStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func collectOpenAPIObjects(value any, path string, result map[string]struct{}) {
	switch node := value.(type) {
	case map[string]any:
		if node["type"] == "object" {
			additional, present := node["additionalProperties"]
			if !present || additional == true {
				result[strings.TrimPrefix(path, ".")] = struct{}{}
			}
		}
		keys := make([]string, 0, len(node))
		for key := range node {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			collectOpenAPIObjects(node[key], path+"."+key, result)
		}
	case []any:
		for index, item := range node {
			collectOpenAPIObjects(item, path+"."+strconv.Itoa(index), result)
		}
	}
}

// TestAPIExamplesAreValidJSON checks exact embedded inputs before permissive
// schema decoders can discard duplicate members or trailing content.
func TestAPIExamplesAreValidJSON(t *testing.T) {
	for _, root := range []string{"examples", "schemas"} {
		err := fs.WalkDir(Files, root, func(path string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil || entry.IsDir() {
				return walkErr
			}
			content, err := Files.ReadFile(path)
			if err != nil {
				return err
			}
			if err := jsondoc.Validate(content); err != nil {
				t.Fatalf("%s is not unambiguous JSON: %v", path, err)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}

func TestMissingEmbeddedAssetIsABuildInvariant(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("missing embedded asset did not panic")
		}
	}()
	_ = mustRead("missing-contract.json")
}

func TestAgentLifecycleExampleMatchesOpenAPI(t *testing.T) {
	t.Parallel()

	document, err := openapi3.NewLoader().LoadFromData(ControlV1)
	if err != nil {
		t.Fatal(err)
	}
	content, err := Files.ReadFile("examples/agent-lifecycle.json")
	if err != nil {
		t.Fatal(err)
	}
	var example struct {
		Description string `json:"description"`
		Requests    []struct {
			Name           string          `json:"name"`
			Actor          string          `json:"actor"`
			Method         string          `json:"method"`
			Path           string          `json:"path"`
			IdempotencyKey string          `json:"idempotencyKey"`
			IfMatch        string          `json:"ifMatch"`
			Body           json.RawMessage `json:"body"`
		} `json:"requests"`
	}
	if err := json.Unmarshal(content, &example); err != nil {
		t.Fatal(err)
	}
	if example.Description == "" || len(example.Requests) < 10 {
		t.Fatal("Agent lifecycle example is incomplete")
	}
	idempotencyKeys := make(map[string]string, len(example.Requests))
	for index, step := range example.Requests {
		if step.Name == "" || step.Actor == "" || step.Method == "" || step.Path == "" || len(step.Body) == 0 {
			t.Fatalf("request %d is missing its name, actor, method, path, or body", index)
		}
		operation := findOpenAPIOperation(document, step.Method, step.Path)
		if operation == nil {
			t.Fatalf("%s does not match an OpenAPI operation", step.Name)
		}
		if operationRequiresParameter(operation, "IdempotencyKey") && step.IdempotencyKey == "" {
			t.Fatalf("%s is missing its Idempotency-Key example", step.Name)
		}
		if prior, exists := idempotencyKeys[step.IdempotencyKey]; step.IdempotencyKey != "" && exists {
			t.Fatalf("%s reuses the Idempotency-Key from %s", step.Name, prior)
		}
		if step.IdempotencyKey != "" {
			idempotencyKeys[step.IdempotencyKey] = step.Name
		}
		if operationRequiresParameter(operation, "IfMatch") && step.IfMatch == "" {
			t.Fatalf("%s is missing its If-Match example", step.Name)
		}
		if operation.RequestBody == nil || operation.RequestBody.Value == nil {
			t.Fatalf("%s has no documented request body", step.Name)
		}
		media := operation.RequestBody.Value.Content.Get("application/json")
		if media == nil || media.Schema == nil || media.Schema.Value == nil {
			t.Fatalf("%s has no JSON request schema", step.Name)
		}
		var body any
		if err := json.Unmarshal(step.Body, &body); err != nil {
			t.Fatalf("%s body is invalid JSON: %v", step.Name, err)
		}
		if err := media.Schema.Value.VisitJSON(body, openapi3.EnableJSONSchema2020()); err != nil {
			t.Fatalf("%s body does not match OpenAPI: %v", step.Name, err)
		}
	}
}

func TestHTTPSRepresentationExampleMatchesCreateVersionSchema(t *testing.T) {
	t.Parallel()

	document, err := openapi3.NewLoader().LoadFromData(ControlV1)
	if err != nil {
		t.Fatal(err)
	}
	content, err := Files.ReadFile("examples/https-representation.json")
	if err != nil {
		t.Fatal(err)
	}
	var example struct {
		Description   string         `json:"description"`
		Prerequisites []string       `json:"prerequisites"`
		CreateVersion map[string]any `json:"createVersion"`
		Expected      struct {
			DeliveryURLRetained             bool `json:"deliveryUrlRetained"`
			ValidationBytesRetained         bool `json:"exactValidationBytesRetainedInPostgreSQL"`
			FreshnessHours                  int  `json:"evidenceFreshnessHours"`
			PublicationRefetches            bool `json:"publicationRefetchesExternalUrl"`
			CatalogAuthorizesPublicDelivery bool `json:"catalogAuthorizesPublicArtifactDelivery"`
		} `json:"expectedBehavior"`
	}
	if err := json.Unmarshal(content, &example); err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(example.Description) == "" || len(example.Prerequisites) != 3 || !example.Expected.DeliveryURLRetained || !example.Expected.ValidationBytesRetained || example.Expected.FreshnessHours != 24 || !example.Expected.PublicationRefetches || example.Expected.CatalogAuthorizesPublicDelivery {
		t.Fatalf("HTTPS representation example is incomplete: %#v", example)
	}
	schema := document.Components.Schemas["CreateVersion"]
	if schema == nil || schema.Value == nil {
		t.Fatal("CreateVersion schema is absent")
	}
	if err := schema.Value.VisitJSON(example.CreateVersion, openapi3.EnableJSONSchema2020()); err != nil {
		t.Fatalf("HTTPS representation example does not match CreateVersion: %v", err)
	}
}

func TestFederationSourceExampleMatchesControlledTopologySchemas(t *testing.T) {
	t.Parallel()
	document, err := openapi3.NewLoader().LoadFromData(ControlV1)
	if err != nil {
		t.Fatal(err)
	}
	content, err := Files.ReadFile("examples/federation-source.json")
	if err != nil {
		t.Fatal(err)
	}
	var example map[string]any
	if err := json.Unmarshal(content, &example); err != nil {
		t.Fatal(err)
	}
	for exampleField, schemaName := range map[string]string{"inspect": "InspectSource", "connect": "ConnectSource"} {
		schema := document.Components.Schemas[schemaName]
		if schema == nil || schema.Value == nil {
			t.Fatalf("%s schema is absent", schemaName)
		}
		if err := schema.Value.VisitJSON(example[exampleField], openapi3.EnableJSONSchema2020()); err != nil {
			t.Fatalf("federation %s example does not match %s: %v", exampleField, schemaName, err)
		}
	}
}

func TestAuditExportExampleMatchesRequestSchema(t *testing.T) {
	t.Parallel()
	document, err := openapi3.NewLoader().LoadFromData(ControlV1)
	if err != nil {
		t.Fatal(err)
	}
	content, err := Files.ReadFile("examples/audit-export-request.json")
	if err != nil {
		t.Fatal(err)
	}
	var body any
	if err := json.Unmarshal(content, &body); err != nil {
		t.Fatal(err)
	}
	path := document.Paths.Find("/api/v1/audit-exports")
	if path == nil {
		t.Fatal("audit export path is absent")
	}
	operation := path.Post
	if operation == nil || operation.RequestBody == nil || operation.RequestBody.Value == nil {
		t.Fatal("audit export operation has no request body")
	}
	media := operation.RequestBody.Value.Content.Get("application/json")
	if media == nil || media.Schema == nil || media.Schema.Value == nil {
		t.Fatal("audit export operation has no JSON request schema")
	}
	if err := media.Schema.Value.VisitJSON(body, openapi3.EnableJSONSchema2020()); err != nil {
		t.Fatalf("audit export example does not match OpenAPI: %v", err)
	}
}

func TestFederationUpdateCanClearDeploymentResolvedAccess(t *testing.T) {
	t.Parallel()
	document, err := openapi3.NewLoader().LoadFromData(ControlV1)
	if err != nil {
		t.Fatal(err)
	}
	patch := document.Components.Schemas["UpdateSource"]
	if patch == nil || patch.Value == nil {
		t.Fatal("UpdateSource schema is absent")
	}
	clearAccess := map[string]any{
		"inspectionId":        "00000000-0000-7000-8000-000000000001",
		"networkProfile":      "",
		"credentialBinding":   "",
		"changePreviewDigest": "sha256:" + strings.Repeat("a", 64),
		"reason":              "Move this source to the reviewed public route",
	}
	if err := patch.Value.VisitJSON(clearAccess, openapi3.EnableJSONSchema2020()); err != nil {
		t.Fatalf("UpdateSource cannot clear deployment-resolved access: %v", err)
	}
	connect := document.Components.Schemas["ConnectSource"]
	if connect == nil || connect.Value == nil {
		t.Fatal("ConnectSource schema is absent")
	}
	if err := connect.Value.VisitJSON(map[string]any{"inspectionId": clearAccess["inspectionId"], "displayName": "Remote", "scope": map[string]any{}, "networkProfile": "", "reason": "Approve the inspected source"}, openapi3.EnableJSONSchema2020()); err == nil {
		t.Fatal("ConnectSource accepted an empty network profile")
	}
	scope := document.Components.Schemas["SourceScope"]
	if scope == nil || scope.Value == nil {
		t.Fatal("SourceScope schema is absent")
	}
	if err := scope.Value.VisitJSON(map[string]any{"kinds": []any{"agent"}}, openapi3.EnableJSONSchema2020()); err != nil {
		t.Fatalf("SourceScope rejected a bounded kind scope: %v", err)
	}
	for name, invalid := range map[string]any{
		"empty object":       map[string]any{},
		"empty array":        map[string]any{"tags": []any{}},
		"blank value":        map[string]any{"tags": []any{" "}},
		"surrounding space":  map[string]any{"tags": []any{" selected"}},
		"duplicate value":    map[string]any{"publishers": []any{"example.com", "example.com"}},
		"duplicate resource": map[string]any{"kinds": []any{"agent", "agent"}},
	} {
		t.Run(name, func(t *testing.T) {
			if err := scope.Value.VisitJSON(invalid, openapi3.EnableJSONSchema2020()); err == nil {
				t.Fatalf("SourceScope accepted %#v", invalid)
			}
		})
	}
}

func TestObservationSearchTagSchemaMatchesRuntimeNonWhitespaceRequirement(t *testing.T) {
	document, err := openapi3.NewLoader().LoadFromData(ControlV1)
	if err != nil {
		t.Fatal(err)
	}
	schema := document.Components.Schemas["ObservationSearch"]
	if schema == nil || schema.Value == nil {
		t.Fatal("ObservationSearch schema is absent")
	}
	if err := schema.Value.VisitJSON(map[string]any{"tags": []any{"  Catalog  "}}, openapi3.EnableJSONSchema2020()); err != nil {
		t.Fatalf("ObservationSearch rejected a runtime-normalized tag: %v", err)
	}
	for _, invalid := range []any{"", "   "} {
		if err := schema.Value.VisitJSON(map[string]any{"tags": []any{invalid}}, openapi3.EnableJSONSchema2020()); err == nil {
			t.Fatalf("ObservationSearch accepted an empty tag %q", invalid)
		}
	}
}

func TestF13CapabilityPublisherAndEvidenceContractsAreExplicit(t *testing.T) {
	t.Parallel()

	document, err := openapi3.NewLoader().LoadFromData(ControlV1)
	if err != nil {
		t.Fatal(err)
	}
	canonical := document.Paths.Value("/api/v1/publishers/{publisherId}/publication-domains/{domain}:approve")
	if canonical == nil || canonical.Post == nil || canonical.Post.OperationID != "approvePublisherPublicationDomain" {
		t.Fatal("publication-domain approval operation is absent")
	}
	if document.Paths.Value("/api/v1/publishers/{publisherId}/domains/{domain}:verify") != nil {
		t.Fatal("legacy publisher-domain verification operation remains documented")
	}

	for name, forbidden := range map[string]string{
		"Capabilities": "federation",
		"Publisher":    "verifiedDomains",
		"EvidenceSet":  "rights",
	} {
		schema := document.Components.Schemas[name]
		if schema == nil || schema.Value == nil {
			t.Fatalf("%s schema is absent", name)
		}
		if _, exists := schema.Value.Properties[forbidden]; exists {
			t.Fatalf("%s schema retains forbidden compatibility property %q", name, forbidden)
		}
		if schema.Value.Example != nil {
			t.Fatalf("%s must use the OpenAPI 3.1 examples array, not deprecated schema example", name)
		}
	}
	capabilities := document.Components.Schemas["Capabilities"].Value
	if len(capabilities.Examples) != 1 {
		t.Fatal("capabilities schema has no example")
	}
	if err := capabilities.VisitJSON(capabilities.Examples[0], openapi3.EnableJSONSchema2020()); err != nil {
		t.Fatalf("capabilities example does not match its schema: %v", err)
	}
	for _, name := range []string{"Publisher", "EvidenceSet"} {
		schema := document.Components.Schemas[name].Value
		if len(schema.Examples) != 1 {
			t.Fatalf("%s schema has no example", name)
		}
		if err := schema.VisitJSON(schema.Examples[0], openapi3.EnableJSONSchema2020()); err != nil {
			t.Fatalf("%s example does not match its schema: %v", name, err)
		}
	}
}

func operationRequiresParameter(operation *openapi3.Operation, name string) bool {
	suffix := "/" + name
	for _, parameter := range operation.Parameters {
		if strings.HasSuffix(parameter.Ref, suffix) || parameter.Value != nil && parameter.Value.Name == name {
			return true
		}
	}
	return false
}

func findOpenAPIOperation(document *openapi3.T, method, requestPath string) *openapi3.Operation {
	for template, item := range document.Paths.Map() {
		locations := openAPIPathParameter.FindAllStringIndex(template, -1)
		var pattern strings.Builder
		pattern.WriteString("^")
		prior := 0
		for _, location := range locations {
			pattern.WriteString(regexp.QuoteMeta(template[prior:location[0]]))
			pattern.WriteString(`[^/]+`)
			prior = location[1]
		}
		pattern.WriteString(regexp.QuoteMeta(template[prior:]))
		pattern.WriteString("$")
		if regexp.MustCompile(pattern.String()).MatchString(requestPath) {
			if operation := item.GetOperation(method); operation != nil {
				return operation
			}
		}
	}
	return nil
}

func TestPersistedDisplayTextSchemasRejectNUL(t *testing.T) {
	document, err := openapi3.NewLoader().LoadFromData(ControlV1)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ schema, field string }{
		{"Namespace", "displayName"}, {"Namespace", "description"}, {"Publisher", "displayName"},
		{"ListingRevision", "displayName"}, {"ListingRevision", "description"},
		{"CreateNamespace", "displayName"}, {"CreateNamespace", "description"}, {"PatchNamespace", "displayName"}, {"PatchNamespace", "description"},
		{"CreatePublisher", "displayName"}, {"PatchPublisher", "displayName"},
		{"CreateListingRevision", "displayName"}, {"CreateListingRevision", "description"},
		{"AdoptObservation", "displayName"}, {"AdoptObservation", "description"},
		{"FederationSource", "displayName"}, {"ConnectSource", "displayName"}, {"PreviewSourceChange", "displayName"}, {"UpdateSource", "displayName"},
		{"Webhook", "name"}, {"CreateWebhook", "name"}, {"PatchWebhook", "name"},
	} {
		t.Run(tc.schema+"/"+tc.field, func(t *testing.T) {
			schema := document.Components.Schemas[tc.schema].Value.Properties[tc.field].Value
			for _, valid := range []string{"Valid 🙂", "line\nbreak", "literal \\u0000"} {
				if err := schema.VisitJSON(valid, openapi3.EnableJSONSchema2020()); err != nil {
					t.Fatalf("valid text rejected: %v", err)
				}
			}
			for _, invalid := range []string{"\x00", "bad\x00text", "text\x00\n"} {
				if err := schema.VisitJSON(invalid, openapi3.EnableJSONSchema2020()); err == nil {
					t.Fatalf("schema accepts NUL: %q", invalid)
				}
			}
		})
	}
}
