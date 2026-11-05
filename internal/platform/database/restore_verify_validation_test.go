package database

import (
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
)

// Golden digests bind the retained example representation and v7 evidence.
// A valid control passes the public integrity validators before corruptions
// exercise the scanner's classification and relational checks.
const restoreExampleContent = "sha256:8150b9b659c30f381f212c782d8ca8d6e4625b56a0fb4b8f58b0309cc9f094f5"
const restoreExampleSubject = "sha256:a2e00f04591b8abfea9a0e5738ae7d3728035ea8247457c6261c1c64d1797520"
const restoreExampleEvidence = "sha256:621bf04fd975f641cce4a180eb763166cbfba98f759ae6f25b46e1c95fd011cc"

func restoreValidRevisionValues() ([]any, []any) {
	v := versionRevisionReadValues()
	v[4], v[12], v[13] = restoreExampleSubject, restoreExampleSubject, restoreExampleEvidence
	r := representationReadValues()
	r[7], r[8] = restoreExampleContent, int64(len(`{"name":"example"}`))
	return v, r
}

func TestRestoreVersionClassifiesRetainedIntegrityFailures(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"valid", "metadata read", "unreadable", "invalid evidence", "invalid revision", "missing document"} {
		t.Run(mode, func(t *testing.T) {
			v, r := restoreValidRevisionValues()
			code := ""
			switch mode {
			case "unreadable":
				v[5] = []byte(`[`)
				code = "RESTORE_VERSION_REVISION_UNREADABLE"
			case "invalid evidence":
				v[13] = restoreExampleContent
				code = "RESTORE_VALIDATION_EVIDENCE_INVALID"
			case "invalid revision":
				r[8] = int64(99)
				code = "RESTORE_VERSION_REVISION_INVALID"
			case "missing document":
				code = "RESTORE_VERSION_REVISION_DOCUMENT_INVALID"
			}
			positions := &identityScriptedRows{values: [][]any{{"revision"}}}
			representations := &identityScriptedRows{values: [][]any{r}}
			references, inventory := &identityScriptedRows{}, &identityScriptedRows{}
			tx := &identityScriptedTx{queryResults: []identityQueryResult{{rows: positions}, {rows: representations}, {rows: references}, {rows: inventory}}, queryRows: []pgx.Row{scriptedValueRow([]any{1, int64(20), 0, int64(0), 0, int64(0), mode == "missing document", int64(512), int64(1024)}), scriptedValueRow(v)}}
			sentinel := errors.New("metadata read failed")
			if mode == "metadata read" {
				tx.queryRows[0] = scannerFunc(func(...any) error { return sentinel })
			}
			records, more, err := scanRestoreVersionRevisions(t.Context(), tx, restoreTestTenant, "", 1)
			if mode == "metadata read" {
				if !errors.Is(err, sentinel) || more || len(records) != 0 || !positions.closed || len(tx.queryStatements) != 1 || len(tx.queryRowStatements) != 1 {
					t.Fatalf("failed bounds read continued hydration: records=%v error=%v", records, err)
				}
				return
			}
			if err != nil || more || len(records) != 1 || !positions.closed {
				t.Fatalf("records=%v more=%v error=%v", records, more, err)
			}
			if code == "" {
				if records[0].finding != nil {
					t.Fatalf("valid fixture rejected: %+v", records[0].finding)
				}
			} else if records[0].finding == nil || records[0].finding.Code != code {
				t.Fatalf("finding=%+v want=%s", records[0].finding, code)
			}
			if mode != "unreadable" && (!representations.closed || !references.closed || !inventory.closed) {
				t.Fatal("revision child rows left open")
			}
			if records[0].decodedBytes != 1556 || len(tx.queryRowStatements) != 2 || !reflect.DeepEqual(tx.queryRowArguments[1], []any{restoreTestTenant, []string{"revision"}, true}) {
				t.Fatalf("lost bounded tenant/history read: %v", tx.queryRowArguments)
			}
		})
	}
}

func TestRestoreEvidenceValidatesRunBindingAndJSONColumns(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
	revisionID := "revision"
	subject := restoreExampleSubject
	evidence := catalog.EvidenceSet{ID: "latest-evidence", TenantID: restoreTestTenant, SubjectDigest: subject, Digest: restoreExampleEvidence, State: catalog.EvidenceComplete, ValidationProfile: "catalog-validation-v7", PolicyVersion: "policy-v1", LegalRights: catalog.LegalRightsSummary{State: catalog.LegalRightsNotAssessed}, Trust: catalog.TrustSummary{State: catalog.TrustNotAssessed}, CreatedAt: at}
	if err := catalog.ValidateEvidenceSetIntegrity(evidence); err != nil {
		t.Fatalf("invalid golden fixture: %v", err)
	}
	for _, mode := range []string{"valid", "findings JSON", "rights JSON", "trust JSON", "retrievals JSON", "missing revision", "missing subject", "missing run time", "different run time", "different subject", "unexpected expiry"} {
		t.Run(mode, func(t *testing.T) {
			row := []any{evidence.ID, evidence.TenantID, evidence.SubjectDigest, evidence.Digest, evidence.State, evidence.ValidationProfile, evidence.PolicyVersion, at, []byte(`[]`), []byte(`{"state":"not_assessed"}`), []byte(`{"state":"not_assessed"}`), []byte(`[]`), &revisionID, (*time.Time)(nil), &at, &subject}
			switch mode {
			case "findings JSON":
				row[8] = []byte(`[`)
			case "rights JSON":
				row[9] = []byte(`[`)
			case "trust JSON":
				row[10] = []byte(`[`)
			case "retrievals JSON":
				row[11] = []byte(`[`)
			case "missing revision":
				row[12] = (*string)(nil)
			case "missing subject":
				row[15] = (*string)(nil)
			case "missing run time":
				row[14] = (*time.Time)(nil)
			case "different run time":
				other := at.Add(time.Second)
				row[14] = &other
			case "different subject":
				other := restoreExampleContent
				row[15] = &other
			case "unexpected expiry":
				expiry := at.Add(time.Hour)
				row[13] = &expiry
			}
			positions := &identityScriptedRows{values: [][]any{{evidence.ID, int64(1024)}}}
			tx := &identityScriptedTx{queryResults: []identityQueryResult{{rows: positions}}, queryRows: []pgx.Row{scriptedValueRow(row)}}
			records, more, err := scanRestoreValidationEvidence(t.Context(), tx, restoreTestTenant, "", 1)
			if err != nil || more || len(records) != 1 || !positions.closed {
				t.Fatalf("records=%v more=%v error=%v", records, more, err)
			}
			if (records[0].finding != nil) != (mode != "valid") || records[0].finding != nil && records[0].finding.Code != "RESTORE_VALIDATION_EVIDENCE_INVALID" {
				t.Fatalf("finding=%+v", records[0].finding)
			}
			if !reflect.DeepEqual(tx.queryRowArguments, [][]any{{restoreTestTenant, evidence.ID}}) {
				t.Fatal("evidence read lost tenant/record binding")
			}
		})
	}
}
