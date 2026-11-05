package catalog

import (
	"encoding/json"
	"testing"
)

func TestCurrentRightsEvidenceRoundTrips(t *testing.T) {
	t.Parallel()

	stored := []byte(`{"state":"not_assessed"}`)
	var summary LegalRightsSummary
	if err := json.Unmarshal(stored, &summary); err != nil {
		t.Fatal(err)
	}
	if summary.State != LegalRightsNotAssessed {
		t.Fatalf("legal-rights projection = %q", summary.State)
	}
	projected, err := json.Marshal(summary)
	if err != nil {
		t.Fatal(err)
	}
	if string(projected) != `{"state":"not_assessed"}` {
		t.Fatalf("legal-rights API projection = %s", projected)
	}
}

func TestLegalRightsSummaryRejectsUnsupportedCurrentState(t *testing.T) {
	t.Parallel()

	var summary LegalRightsSummary
	if err := json.Unmarshal([]byte(`{"state":"allowed"}`), &summary); err == nil {
		t.Fatal("unsupported legal-rights state was accepted")
	}
}

func TestRightsEvidenceRejectsObsoleteAndAmbiguousShapes(t *testing.T) {
	t.Parallel()
	for _, raw := range []string{`{"State":"not_assessed"}`, `{"state":null}`, `{"index":"allowed"}`, `{"state":"not_assessed","index":"allowed"}`, `{}`, `null`, `{"state":""}`, `{"state":"not_assessed","state":"not_assessed"}`, `{"state":"not_assessed"}{}`} {
		var value LegalRightsSummary
		if err := json.Unmarshal([]byte(raw), &value); err == nil {
			t.Fatalf("unsupported rights evidence accepted: %s", raw)
		}
	}
}
