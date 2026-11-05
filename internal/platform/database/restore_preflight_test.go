package database

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/audit"
)

func TestRestoreVerificationPreflightAvoidsPoolAccess(t *testing.T) {
	for _, mode := range []string{"options", "pool", "cursor"} {
		t.Run(mode, func(t *testing.T) {
			options := validRestoreOptions()
			pool := &pgxpool.Pool{} // Inert: invalid input must stop before acquisition.
			message := "limit from 1 through 1000"
			switch mode {
			case "options":
				options.Limit = 0
			case "pool":
				pool, message = nil, "PostgreSQL pool is required"
			case "cursor":
				options.Cursor, message = "!", "cursor is malformed"
			}
			report, err := VerifyRestoredCatalog(t.Context(), pool, options)
			if err == nil || !strings.Contains(err.Error(), message) || !reflect.DeepEqual(report, RestoreVerificationReport{}) {
				t.Fatalf("report=%+v error=%v", report, err)
			}
		})
	}
}

func TestRestoredAuditPreflightRequiresTrustedRecoveryBinding(t *testing.T) {
	from := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
	before := from.Add(time.Hour)
	exported, _, err := audit.BuildExport(restoreTestTenant, audit.Query{OccurredFrom: &from, OccurredBefore: &before}, before, before, nil)
	if err != nil {
		t.Fatal(err)
	}
	trusted, err := audit.BindTrustedManifest(exported, "restore-unit-fixture")
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"pool", "empty recovery point", "rebound recovery point", "invalid manifest"} {
		t.Run(mode, func(t *testing.T) {
			candidate, recovery := trusted, trusted.RecoveryPointID
			pool := &pgxpool.Pool{}
			switch mode {
			case "pool":
				pool = nil
			case "empty recovery point":
				recovery = " \t"
			case "rebound recovery point":
				recovery = "another-recovery-point"
			case "invalid manifest":
				candidate.Manifest.EventsDigest = "invalid"
			}
			report, err := CompareRestoredAudit(t.Context(), pool, candidate, recovery)
			if err == nil || !strings.Contains(err.Error(), "matching trusted recovery-point binding") || !reflect.DeepEqual(report, audit.VerificationReport{}) {
				t.Fatalf("report=%+v error=%v", report, err)
			}
		})
	}
}

func TestRestoreFeedbackPagePreservesFindingsAndContinuation(t *testing.T) {
	const after = "00000000-0000-7000-8000-000000000010"
	const valid = "00000000-0000-7000-8000-000000000011"
	const invalid = "00000000-0000-7000-8000-000000000012"
	for _, extra := range []bool{false, true} {
		rows := &identityScriptedRows{values: [][]any{{valid, false}, {invalid, true}}}
		if extra {
			rows.values = append(rows.values, []any{"00000000-0000-7000-8000-000000000013", true})
		}
		tx := &identityScriptedTx{fakeCommandTx: &fakeCommandTx{}, queryResults: []identityQueryResult{{rows: rows}}}
		records, more, err := scanRestoreFeedback(t.Context(), tx, restoreTestTenant, after, 2)
		if err != nil || more != extra || len(records) != 2 {
			t.Fatalf("records=%+v more=%v error=%v", records, more, err)
		}
		finding := records[1].finding
		if records[0].key != valid || records[0].finding != nil || records[1].key != invalid || finding == nil || finding.Code != "RESTORE_FEEDBACK_INVALID" || finding.Domain != "feedback" || finding.RecordID != invalid || finding.Class != "integrity" || finding.Severity != "error" || finding.Disposition == "" {
			t.Fatalf("feedback records=%+v finding=%+v", records, finding)
		}
		if !rows.closed || !reflect.DeepEqual(tx.queryArguments, [][]any{{restoreTestTenant, after, 3}}) || len(tx.statements) != 0 || len(tx.queryRowStatements) != 0 {
			t.Fatalf("feedback scope/cleanup: args=%v closed=%v", tx.queryArguments, rows.closed)
		}
	}
}

func TestRestorePersonalNeverReportsPrivateCriteria(t *testing.T) {
	raw := []byte(`{"type":"search","label":"private-label","search":{"text":"private-query"}}`)
	at := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	for _, invalid := range []bool{false, true} {
		rows := &identityScriptedRows{values: [][]any{{restoreTestTenant, raw, int64(1), at, at, true, !invalid}}}
		tx := &identityScriptedTx{queryResults: []identityQueryResult{{rows: rows}}}
		got, more, err := scanRestorePersonal(t.Context(), tx, restoreTestTenant, "", 1)
		if err != nil || more || len(got) != 1 || (got[0].finding != nil) != invalid {
			t.Fatal(got, more, err)
		}
		if invalid {
			encoded, marshalErr := json.Marshal(got[0].finding)
			if marshalErr != nil {
				t.Fatal(marshalErr)
			}
			if strings.Contains(string(encoded), "private-label") || strings.Contains(string(encoded), "private-query") {
				t.Fatal("private criteria in restore finding")
			}
		}
	}
}
