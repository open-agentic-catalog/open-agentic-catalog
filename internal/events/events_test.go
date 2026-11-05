package events

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/api"
)

const (
	testEventID  = "0198f1c2-24a0-7000-8000-000000000001"
	testTargetID = "0198f1c2-24a0-7000-8000-000000000002"
	testTenantID = "0198f1c2-24a0-7000-8000-000000000010"
)

func TestEveryRegisteredPublicEventProducesAValidClosedCloudEvent(t *testing.T) {
	digest := "sha256:" + strings.Repeat("a", 64)
	for eventType, action := range publicTypes {
		t.Run(action, func(t *testing.T) {
			if !IsPublicType(eventType) {
				t.Fatalf("registered event type %q is not public", eventType)
			}
			host := ""
			if strings.HasPrefix(action, "publication.") {
				host = "catalog.example.com"
			}
			reference, err := MarshalReferenceWithHost(action, targetTypeForAction(action), testTargetID, digest, host)
			if err != nil {
				t.Fatal(err)
			}
			body, err := MarshalCloudEvent(testEventID, testTenantID, eventType, testTargetID, time.Date(2026, 8, 29, 12, 0, 0, 123, time.FixedZone("offset", 3600)), reference)
			if err != nil {
				t.Fatal(err)
			}
			var event map[string]any
			if err := json.Unmarshal(body, &event); err != nil {
				t.Fatal(err)
			}
			if len(event) != 9 || event["source"] != TenantSource(testTenantID) || event["dataschema"] != DataSchema || event["time"] != "2026-08-29T11:00:00.000000123Z" {
				t.Fatalf("unexpected public envelope: %#v", event)
			}
			data, ok := event["data"].(map[string]any)
			wantMembers := 4
			if host != "" {
				wantMembers++
			}
			if !ok || len(data) != wantMembers || data["action"] != action || data["targetType"] != targetTypeForAction(action) || data["targetId"] != testTargetID || data["digest"] != digest || host != "" && data["host"] != host {
				t.Fatalf("unexpected public reference: %#v", event["data"])
			}
		})
	}
	for _, eventType := range []string{"", "com.open.agentic.catalog.internal.v1", CanonicalType("audit.exported")} {
		if IsPublicType(eventType) {
			t.Fatalf("unregistered event type %q is public", eventType)
		}
	}
}

func TestCloudEventRejectsObsoleteAndMalformedReferences(t *testing.T) {
	for _, raw := range []string{
		`{"digest":"sha256:` + strings.Repeat("b", 64) + `","token":"secret","document":{"private":true}}`,
		`{}`, `null`, ``,
		`{"action":"resource.created","targetType":"resource","targetId":"` + testTargetID + `","digest":"invalid"}`,
		`{"action":"resource.created","action":"resource.created","targetType":"resource","targetId":"` + testTargetID + `"}`,
		`{"action":"publisher.created","targetType":"resource","targetId":"` + testTargetID + `"}`,
	} {
		body, err := MarshalCloudEvent(testEventID, testTenantID, CanonicalType("resource.created"), testTargetID, time.Unix(0, 0), []byte(raw))
		if err == nil || body != nil {
			t.Fatal("invalid persisted reference was projected")
		}
	}
}

func TestPublicEventContractRejectsUnknownAndMismatchedValues(t *testing.T) {
	if err := Validate([]byte(`{`)); err == nil || !strings.Contains(err.Error(), "decode event") {
		t.Fatalf("malformed event returned %v", err)
	}
	if targetType := targetTypeForAction("future.action"); targetType != "" {
		t.Fatalf("unknown action target type = %q", targetType)
	}
	valid, err := MarshalCloudEvent(testEventID, testTenantID, CanonicalType("resource.created"), testTargetID, time.Unix(0, 0), testResourceReference(t))
	if err != nil {
		t.Fatal(err)
	}
	var event map[string]any
	if err := json.Unmarshal(valid, &event); err != nil {
		t.Fatal(err)
	}
	tests := map[string]struct {
		envelopeKey string
		dataKey     string
		value       any
	}{
		"unknown envelope field":   {envelopeKey: "private", value: true},
		"lookalike source host":    {envelopeKey: "source", value: strings.Replace(TenantSource(testTenantID), "github.com", "githubXcom", 1)},
		"invalid source namespace": {envelopeKey: "source", value: "urn:example:tenant:" + testTenantID},
		"invalid event namespace":  {envelopeKey: "type", value: "org.example.resource.created.v1"},
		"unknown data field":       {dataKey: "private", value: true},
		"mismatched action":        {dataKey: "action", value: "namespace.created"},
		"mismatched target type":   {dataKey: "targetType", value: "namespace"},
		"mismatched subject":       {envelopeKey: "subject", value: testEventID},
		"host on non-publication":  {dataKey: "host", value: "catalog.example.com"},
		"unsafe subject":           {envelopeKey: "subject", value: "resource\nprivate"},
		"unsafe target":            {dataKey: "targetId", value: "resource\nprivate"},
	}
	for name, mutation := range tests {
		t.Run(name, func(t *testing.T) {
			var candidate map[string]any
			body, _ := json.Marshal(event)
			if err := json.Unmarshal(body, &candidate); err != nil {
				t.Fatal(err)
			}
			if mutation.dataKey != "" {
				eventData(t, candidate)[mutation.dataKey] = mutation.value
			} else {
				candidate[mutation.envelopeKey] = mutation.value
			}
			body, _ = json.Marshal(candidate)
			if err := Validate(body); err == nil {
				t.Fatalf("invalid event matched the public schema: %s", body)
			}
		})
	}
}

func eventData(t *testing.T, event map[string]any) map[string]any {
	t.Helper()
	data, ok := event["data"].(map[string]any)
	if !ok {
		t.Fatalf("event data is not an object: %#v", event["data"])
	}
	return data
}

func TestPublicationEventRequiresCanonicalHostBeyondSchemaValidation(t *testing.T) {
	reference, err := MarshalReferenceWithHost("publication.activated", "publication_snapshot", testTargetID, "", "::1")
	if err != nil {
		t.Fatal(err)
	}
	body, err := MarshalCloudEvent(testEventID, testTenantID, CanonicalType("publication.activated"), testTargetID, time.Unix(0, 0), reference)
	if err != nil {
		t.Fatal(err)
	}
	if err := Validate(body); err != nil {
		t.Fatalf("canonical publication host rejected: %v", err)
	}
	var event map[string]any
	if err := json.Unmarshal(body, &event); err != nil {
		t.Fatal(err)
	}
	eventData(t, event)["host"] = "0:0:0:0:0:0:0:1"
	body, err = json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	if err := Validate(body); err == nil || !strings.Contains(err.Error(), "validate event reference:") {
		t.Fatalf("schema-valid noncanonical host returned %v", err)
	}
}

func TestPublicEventConstructorsRejectInvalidInput(t *testing.T) {
	if CanonicalType("review_submission.approve") != "com.open.agentic.catalog.review.decision.recorded.v1" || CanonicalType("resource_version.created") != "com.open.agentic.catalog.resource.version.created.v1" {
		t.Fatal("canonical event aliases are inconsistent")
	}
	for _, test := range []struct {
		action, targetType, targetID, digest string
	}{
		{action: "internal.maintenance", targetType: "resource", targetID: testTargetID},
		{action: "resource.created", targetType: "namespace", targetID: testTargetID},
		{action: "resource.created", targetType: "resource", targetID: "not-a-uuid"},
		{action: "resource.created", targetType: "resource", targetID: testTargetID, digest: "sha256:invalid"},
	} {
		if _, err := Reference(test.action, test.targetType, test.targetID, test.digest); err == nil {
			t.Fatalf("invalid reference accepted: %#v", test)
		}
	}
	if _, err := MarshalReference("internal.maintenance", "resource", testTargetID, ""); err == nil {
		t.Fatal("invalid marshaled reference was accepted")
	}
	if _, err := MarshalReferenceWithHost("resource.created", "resource", "not-a-uuid", "", ""); err == nil {
		t.Fatal("invalid host-aware marshaled reference was accepted")
	}
	if _, err := Reference("publication.activated", "publication_snapshot", testTargetID, ""); err == nil {
		t.Fatal("publication reference without its discovery host was accepted")
	}
	if _, err := ReferenceWithHost("resource.created", "resource", testTargetID, "", "catalog.example.com"); err == nil {
		t.Fatal("non-publication reference with a discovery host was accepted")
	}
	if _, err := ReferenceWithHost("publication.activated", "publication_snapshot", testTargetID, "", "https://catalog.example.com/path"); err == nil {
		t.Fatal("publication reference with a URL instead of a host was accepted")
	}
	for _, host := range []string{"catalog.example.com:443", "Catalog.example.com", "catalog.example.com.", "catalog..example.com", "-catalog.example.com", "123.456", "[::1]"} {
		if _, err := ReferenceWithHost("publication.activated", "publication_snapshot", testTargetID, "", host); err == nil {
			t.Fatalf("invalid publication host %q was accepted", host)
		}
	}
	for _, host := range []string{"catalog.example.com", "127.0.0.1", "::1"} {
		if _, err := ReferenceWithHost("publication.activated", "publication_snapshot", testTargetID, "", host); err != nil {
			t.Fatalf("valid publication host %q was rejected: %v", host, err)
		}
	}
	digest := "sha256:" + strings.Repeat("c", 64)
	if err := ValidateReferenceData([]byte(` { "targetId":"`+testTargetID+`", "digest":"`+digest+`", "targetType":"resource", "action":"resource.created" } `), "resource.created", "resource", testTargetID, digest, ""); err != nil {
		t.Fatalf("equivalent closed reference data was rejected: %v", err)
	}
	for name, raw := range map[string][]byte{
		"malformed":       []byte(`{`),
		"duplicate":       []byte(`{"action":"resource.created","action":"resource.created","targetType":"resource","targetId":"` + testTargetID + `"}`),
		"unknown member":  []byte(`{"action":"resource.created","targetType":"resource","targetId":"` + testTargetID + `","private":true}`),
		"wrong digest":    []byte(`{"action":"resource.created","targetType":"resource","targetId":"` + testTargetID + `","digest":"sha256:` + strings.Repeat("d", 64) + `"}`),
		"wrong target ID": []byte(`{"action":"resource.created","targetType":"resource","targetId":"` + testEventID + `","digest":"` + digest + `"}`),
	} {
		t.Run("stored reference "+name, func(t *testing.T) {
			if err := ValidateReferenceData(raw, "resource.created", "resource", testTargetID, digest, ""); err == nil {
				t.Fatalf("invalid stored reference was accepted: %s", raw)
			}
		})
	}
	for name, invoke := range map[string]func() error{
		"unknown type": func() error {
			_, err := MarshalCloudEvent(testEventID, testTenantID, "com.open.agentic.catalog.internal.v1", testTargetID, time.Now(), testResourceReference(t))
			return err
		},
		"invalid data": func() error {
			_, err := MarshalCloudEvent(testEventID, testTenantID, CanonicalType("resource.created"), testTargetID, time.Now(), []byte(`{`))
			return err
		},
		"legacy host on non-publication": func() error {
			_, err := MarshalCloudEvent(testEventID, testTenantID, CanonicalType("resource.created"), testTargetID, time.Now(), []byte("{\"host\":\"catalog.example.com\"}"))
			return err
		},
		"invalid event id": func() error {
			_, err := MarshalCloudEvent("event", testTenantID, CanonicalType("resource.created"), testTargetID, time.Now(), testResourceReference(t))
			return err
		},
		"invalid tenant": func() error {
			_, err := MarshalCloudEvent(testEventID, "tenant", CanonicalType("resource.created"), testTargetID, time.Now(), testResourceReference(t))
			return err
		},
	} {
		t.Run(name, func(t *testing.T) {
			if err := invoke(); err == nil {
				t.Fatal("invalid event was accepted")
			}
		})
	}
}

func TestTenantSourceMatchesTheAcceptedTenantIdentifierBoundary(t *testing.T) {
	tenantID := "00000000-0000-0000-0000-0000000000AB"
	body, err := MarshalCloudEvent(testEventID, tenantID, CanonicalType("resource.created"), testTargetID, time.Unix(0, 0), testResourceReference(t))
	if err != nil {
		t.Fatal(err)
	}
	var event CloudEvent
	if err := json.Unmarshal(body, &event); err != nil {
		t.Fatal(err)
	}
	if event.Source != "https://github.com/open-agentic-catalog/open-agentic-catalog/tenants/00000000-0000-0000-0000-0000000000ab" {
		t.Fatalf("tenant-bound source = %q", event.Source)
	}
}

func TestGoRegistryMatchesTheEmbeddedPublicEventSchema(t *testing.T) {
	var schema map[string]any
	raw, err := api.Files.ReadFile("schemas/catalog-event-v1.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &schema); err != nil {
		t.Fatal(err)
	}
	properties, ok := schema["properties"].(map[string]any)
	if !ok {
		t.Fatal("event schema properties are not an object")
	}
	typeSchema, ok := properties["type"].(map[string]any)
	if !ok {
		t.Fatal("event type schema is not an object")
	}
	typeValues, ok := typeSchema["enum"].([]any)
	if !ok {
		t.Fatal("event type enum is not an array")
	}
	dataSchema, ok := properties["data"].(map[string]any)
	if !ok {
		t.Fatal("event data schema is not an object")
	}
	dataProperties, ok := dataSchema["properties"].(map[string]any)
	if !ok {
		t.Fatal("event data properties are not an object")
	}
	actionSchema, ok := dataProperties["action"].(map[string]any)
	if !ok {
		t.Fatal("event action schema is not an object")
	}
	actionValues, ok := actionSchema["enum"].([]any)
	if !ok {
		t.Fatal("event action enum is not an array")
	}
	types := make([]string, len(typeValues))
	actions := make([]string, len(actionValues))
	for index := range typeValues {
		value, ok := typeValues[index].(string)
		if !ok {
			t.Fatalf("event type enum item %d is not a string", index)
		}
		types[index] = value
	}
	for index := range actionValues {
		value, ok := actionValues[index].(string)
		if !ok {
			t.Fatalf("event action enum item %d is not a string", index)
		}
		actions[index] = value
	}
	wantTypes := make([]string, len(publicActions))
	for index, action := range publicActions {
		wantTypes[index] = CanonicalType(action)
	}
	if !slices.Equal(actions, publicActions) || !slices.Equal(types, wantTypes) || len(publicTypes) != len(wantTypes) {
		t.Fatalf("public event registries differ: actions %#v types %#v Go %#v", actions, types, publicActions)
	}
}

func TestReviewedWebhookExamplesMatchTheCompiledEventContract(t *testing.T) {
	for _, name := range []string{"webhook-delivery.json", "source-webhook-delivery.json", "publication-webhook-delivery.json"} {
		body, err := api.Files.ReadFile("examples/" + name)
		if err != nil {
			t.Fatal(err)
		}
		if err := Validate(body); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
	}
}

func testResourceReference(t *testing.T) []byte {
	t.Helper()
	raw, err := MarshalReference("resource.created", "resource", testTargetID, "")
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
