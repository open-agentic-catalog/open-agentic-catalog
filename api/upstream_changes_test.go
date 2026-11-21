package api

import (
	"strings"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
)

func TestUpstreamChangeOccurrenceRequiresNormalizedEvidence(t *testing.T) {
	doc, err := openapi3.NewLoader().LoadFromData(ControlV1)
	if err != nil {
		t.Fatal(err)
	}
	const id = "018fa000-0000-7000-8000-000000000001"
	digest := "sha256:" + strings.Repeat("a", 64)
	occurrence := map[string]any{
		"observationId": id, "sourceId": id, "snapshotId": id, "sourceRevision": 1,
		"retrievalDigest": digest, "entryLocation": "/entries/0", "exactEntryDigest": digest,
		"evidenceComplete": true, "observedAt": "2026-09-01T12:00:00Z",
	}
	selected := doc.Components.Schemas["UpstreamChange"].Value.Properties["occurrence"].Value
	ordinary := doc.Components.Schemas["ObservationOccurrence"].Value
	if err := ordinary.VisitJSON(occurrence, openapi3.EnableJSONSchema2020()); err != nil {
		t.Fatalf("ordinary occurrence may omit normalization evidence: %v", err)
	}
	if err := selected.VisitJSON(occurrence, openapi3.EnableJSONSchema2020()); err == nil {
		t.Fatal("upstream change accepts an occurrence without the normalized digest")
	}
	occurrence["normalizedEntryDigest"] = digest
	if err := selected.VisitJSON(occurrence, openapi3.EnableJSONSchema2020()); err != nil {
		t.Fatalf("complete upstream change occurrence: %v", err)
	}
	occurrence["unknown"] = true
	if err := selected.VisitJSON(occurrence, openapi3.EnableJSONSchema2020()); err == nil {
		t.Fatal("upstream change accepts an unknown occurrence field")
	}
	delete(occurrence, "unknown")
	delete(occurrence, "snapshotId")
	if err := selected.VisitJSON(occurrence, openapi3.EnableJSONSchema2020()); err == nil {
		t.Fatal("upstream change lost the base occurrence requirements")
	}
}
