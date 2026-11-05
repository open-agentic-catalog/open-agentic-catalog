package catalog

import (
	"bytes"
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
)

func TestRevisionDiagnosticsBoundAggregationAndLateErrors(t *testing.T) {
	for _, scenario := range []string{"valid maximum", "aggregate warnings", "aggregate bytes", "late error"} {
		limits := DiagnosticLimits{MaxFindings: 2, MaxDiagnosticBytes: 1024}
		mismatch := scenario == "late error"
		now := time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)
		inputFindings := []Finding{{Code: "FIRST", Severity: SeverityWarning, Message: "warning"}, {Code: "SECOND", Severity: SeverityWarning, Message: "warning"}}
		wantState := EvidenceBlocked
		if scenario == "valid maximum" {
			inputFindings = nil
			wantState = EvidenceComplete
		}
		if scenario == "aggregate bytes" {
			limits.MaxFindings = 128
			// Each representation fits, but combined diagnostics exceed bytes
			// well before the finding-count ceiling.
			inputFindings = []Finding{{Code: "WARNING", Severity: SeverityWarning, Message: strings.Repeat("w", 600)}}
		}
		original := append([]Finding(nil), inputFindings...)
		s := &Service{clock: &guardrailClock{now: now}, ids: &guardrailIDs{}, diagnosticLimits: limits, policy: "test", validator: guardrailValidator(func(context.Context, Kind, RepresentationInput) ValidationResult {
			version := "1.0.0"
			if mismatch {
				version = "2.0.0"
			}
			return ValidationResult{ProfileID: "test", NormalizedFacts: map[string]any{"version": version}, Findings: inputFindings}
		})}
		inputs := make([]RepresentationInput, 16)
		for i := range inputs {
			inputs[i] = RepresentationInput{Role: RepresentationMetadata, MediaType: "application/json", Data: json.RawMessage(`{"name":"fixture"}`)}
		}
		inputs[0].Role = RepresentationPrimary
		revision, evidence, _, err := s.buildVersionRevision(t.Context(), identity.Actor{TenantID: guardrailTenantID, Subject: "test"}, KindAgent, "00000000-0000-7000-8000-000000000088", "1.0.0", 1, now, inputs)
		if err != nil {
			t.Fatal(err)
		}
		if err := ValidateEvidenceSetIntegrity(evidence); err != nil {
			t.Fatal(err)
		}
		if evidence.State != wantState || len(revision.Representations) != 16 {
			t.Fatalf("aggregate state=%s representations=%d", evidence.State, len(revision.Representations))
		}
		originalRevision, err := json.Marshal(revision)
		if err != nil {
			t.Fatal(err)
		}
		revalidated, _, err := s.revalidateStoredRevision(t.Context(), guardrailTenantID, KindAgent, "1.0.0", revision)
		if err != nil {
			t.Fatal(err)
		}
		after, err := json.Marshal(revision)
		if err != nil || !bytes.Equal(originalRevision, after) {
			t.Fatalf("revalidation modified immutable revision: %v", err)
		}
		for _, findings := range [][]Finding{revision.Findings, evidence.Findings, revalidated} {
			raw, err := json.Marshal(findings)
			if err != nil {
				t.Fatal(err)
			}
			if len(findings) > limits.MaxFindings || len(raw) > limits.MaxDiagnosticBytes {
				t.Fatalf("aggregate findings=%+v", findings)
			}
			if wantState == EvidenceComplete && len(findings) != 0 || wantState == EvidenceBlocked && (len(findings) == 0 || findings[len(findings)-1].Code != DiagnosticBudgetExhausted) {
				t.Fatalf("%s: incorrect report %+v", scenario, findings)
			}
		}
		for i, representation := range revision.Representations {
			if !bytes.Equal(representation.Data, inputs[i].Data) {
				t.Fatal("retained representation data changed")
			}
			if len(representation.Findings) > limits.MaxFindings || mismatch && representation.ValidationState != "invalid" {
				t.Fatalf("late error lost: %+v", representation)
			}
			if !mismatch && (representation.ValidationState != "valid" || !reflect.DeepEqual(representation.Findings, inputFindings) && len(inputFindings) != 0) {
				t.Fatalf("aggregate exhaustion changed individual validation: %+v", representation)
			}
		}
		if !reflect.DeepEqual(original, inputFindings) {
			t.Fatal("validator findings mutated")
		}
	}
}

func TestEmptyBoundedEvidencePreservesDigestContract(t *testing.T) {
	now := time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)
	s := &Service{ids: &guardrailIDs{}, diagnosticLimits: DefaultDiagnosticLimits(), policy: "test"}
	evidence, err := s.newValidationEvidence(guardrailTenantID, "sha256:"+strings.Repeat("a", 64), nil, nil, now)
	if err != nil {
		t.Fatal(err)
	}
	if evidence.State != EvidenceComplete || evidence.Findings == nil || len(evidence.Findings) != 0 {
		t.Fatal("empty report no longer encodes as an empty array")
	}
	if err := ValidateEvidenceSetIntegrity(evidence); err != nil {
		t.Fatal(err)
	}
}
