package api

import (
	"github.com/getkin/kin-openapi/openapi3"
	"strings"
	"testing"
)

func TestTrustAssessmentEventIdentifierSchema(t *testing.T) {
	document, err := openapi3.NewLoader().LoadFromData(ControlV1)
	if err != nil {
		t.Fatal(err)
	}
	// The offline profile accepts signed log event IDs up to 128 characters.
	schema := document.Components.Schemas["TrustCryptographicResult"].Value.Properties["logId"].Value
	if err := schema.VisitJSON(strings.Repeat("e", 128), openapi3.EnableJSONSchema2020()); err != nil {
		t.Fatalf("schema rejects a profile-supported event ID: %v", err)
	}
	if err := schema.VisitJSON(strings.Repeat("e", 129), openapi3.EnableJSONSchema2020()); err == nil {
		t.Fatal("schema accepts an oversized event ID")
	}
}
