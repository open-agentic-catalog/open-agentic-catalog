// Audit repository units preserve bounded evidence and failure cleanup using rows.
package database

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/audit"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
)

// TestAuditPageFailures complements the existing populated page/filter test.
func TestAuditPageFailures(t *testing.T) {
	pool := &pgxpool.Pool{}
	repo := &PostgresRepository{pool: pool}
	failure := errors.New("audit page failure")
	for _, outcome := range []string{"empty", "query", "scan", "iteration", "oversized record", "invalid details"} {
		t.Run(outcome, func(t *testing.T) {
			values := auditScanValues(t, auditScanFixture(t))
			rows := &identityScriptedRows{values: [][]any{values}}
			result := identityQueryResult{rows: rows}
			code := catalog.CodeDependencyUnavailable
			switch outcome {
			case "empty":
				rows.values = nil
				code = ""
			case "query":
				result.err = failure
			case "scan":
				rows.values = [][]any{{"partial"}}
			case "iteration":
				rows.err = failure
			case "oversized record":
				values[11] = []byte(nil)
				code = catalog.CodeInternal
			case "invalid details":
				values[11] = []byte(`{`)
				code = catalog.CodeInternal
			}
			tx := &identityScriptedTx{fakeCommandTx: &fakeCommandTx{}, queryResults: []identityQueryResult{result}}
			got, err := repo.AuditEvents(identityRepositoryContext(pool, tx), restoreTestTenant, catalog.AuditEventListRequest{Limit: 2})
			if code == "" {
				if err != nil || got == nil || len(got) != 0 {
					t.Fatalf("empty page=%v %v", got, err)
				}
			} else if err == nil || catalog.ErrorCodeOf(err) != code || got != nil {
				t.Fatalf("failed page=%v %v", got, err)
			}
			if outcome != "query" && !rows.closed {
				t.Fatal("rows leaked")
			}
			if (outcome == "query" || outcome == "iteration") && !errors.Is(err, failure) {
				t.Fatalf("lost driver cause: %v", err)
			}
		})
	}
}

// TestAuditAppendUsesCommandTransaction tests the audit-only public entry point,
// leaving evidence validation matrices with the existing insert helper tests.
func TestAuditAppendUsesCommandTransaction(t *testing.T) {
	pool := &pgxpool.Pool{}
	repo := &PostgresRepository{pool: pool}
	event := auditScanFixture(t)
	failure := errors.New("audit insert failure")
	for _, fail := range []bool{false, true} {
		tx := &identityScriptedTx{fakeCommandTx: &fakeCommandTx{}}
		if fail {
			tx.execErrors = []error{failure}
		}
		err := repo.AppendAuditEvent(identityRepositoryContext(pool, tx), event)
		if fail {
			if !errors.Is(err, failure) || tx.rollbacks != 1 {
				t.Fatalf("failed append: %v", err)
			}
		} else if err != nil || tx.rollbacks != 0 {
			t.Fatalf("append: %v", err)
		}
		if tx.commits != 0 || len(tx.statements) != 1 || !strings.Contains(tx.statements[0], "INSERT INTO audit_events") || tx.arguments[0][2] != event.TenantID || tx.arguments[0][6] != event.TargetID {
			t.Fatalf("audit transaction binding: %v", tx.arguments)
		}
	}
}

// auditExportRows supplies consistent metadata and current-format evidence. The
// projection keeps the exact large integer already validated by auditScanFixture.
func auditExportRows(t *testing.T) (catalog.AuditEvent, *identityScriptedRows, *identityScriptedRows) {
	t.Helper()
	event := auditScanFixture(t)
	metadata := &identityScriptedRows{values: [][]any{{event.ID, event.At, new(audit.RecordFormatV1), int64(2048)}}}
	records := &identityScriptedRows{values: [][]any{auditScanValues(t, event)}}
	return event, metadata, records
}

// TestAuditExportSelectionBoundsAndFailures proves metadata bounds precede full
// row hydration and driver errors close results without exposing partial evidence.
func TestAuditExportSelectionBoundsAndFailures(t *testing.T) {
	failure := errors.New("export selection failure")
	for _, outcome := range []string{"records", "metadata query", "metadata scan", "metadata iteration", "missing format", "wrong format", "record estimate", "row limit", "record query", "record scan", "record details", "record iteration", "encoded record limit", "response limit"} {
		t.Run(outcome, func(t *testing.T) {
			event, metadata, records := auditExportRows(t)
			results := []identityQueryResult{{rows: metadata}, {rows: records}}
			code := catalog.ErrorCode("")
			queries := 2
			switch outcome {
			case "records":
				second := event
				second.ID = "00000000-0000-7000-8000-000000000091"
				second.At = event.At.Add(time.Second)
				metadata.values = append(metadata.values, []any{second.ID, second.At, new(audit.RecordFormatV1), int64(2048)})
				records.values = append(records.values, auditScanValues(t, second))
			case "metadata query":
				results[0].err = failure
				code = catalog.CodeDependencyUnavailable
				queries = 1
			case "metadata scan":
				metadata.values = [][]any{{}}
				code = catalog.CodeDependencyUnavailable
				queries = 1
			case "metadata iteration":
				metadata.err = failure
				code = catalog.CodeDependencyUnavailable
				queries = 1
			case "missing format":
				metadata.values[0][2] = (*int)(nil)
				code = catalog.CodeInternal
				queries = 1
			case "wrong format":
				metadata.values[0][2] = new(2)
				code = catalog.CodeInternal
				queries = 1
			case "record estimate":
				metadata.values[0][3] = int64(audit.MaxEncodedRecordBytes + 1)
				code = catalog.CodeInternal
				queries = 1
			case "row limit":
				metadata.values = make([][]any, audit.MaxExportRows+1)
				for i := range metadata.values {
					metadata.values[i] = []any{event.ID, event.At, new(audit.RecordFormatV1), int64(2048)}
				}
				code = catalog.CodeAuditExportRangeTooLarge
				queries = 1
			case "record query":
				results[1].err = failure
				code = catalog.CodeDependencyUnavailable
			case "record scan":
				records.values = [][]any{{}}
				code = catalog.CodeDependencyUnavailable
			case "record details":
				records.values[0][11] = []byte(`[]`)
				code = catalog.CodeInternal
			case "record iteration":
				records.err = failure
				code = catalog.CodeDependencyUnavailable
			case "response limit":
				// Every event remains valid and individually bounded; escaping
				// a permitted reason makes the combined response exceed 8 MiB.
				event.Reason = strings.Repeat("<", audit.MaxReasonRunes)
				if err := audit.ValidateNewRecord(event); err != nil {
					t.Fatal(err)
				}
				encoded, err := json.Marshal(event)
				if err != nil {
					t.Fatal(err)
				}
				count := audit.MaxExportResponseBytes/len(encoded) + 1
				if count > audit.MaxExportRows {
					t.Fatal("fixture would reach row limit first")
				}
				metadata.values, records.values = make([][]any, count), make([][]any, count)
				for index := range count {
					item := event
					item.ID = fmt.Sprintf("00000000-0000-7000-8000-%012d", index+100)
					item.At = event.At.Add(time.Duration(index) * time.Microsecond)
					metadata.values[index] = []any{item.ID, item.At, new(audit.RecordFormatV1), int64(16384)}
					records.values[index] = auditScanValues(t, item)
				}
				code = catalog.CodeAuditExportRangeTooLarge
			case "encoded record limit":
				// Corrupt retained text can expand during JSON encoding. Reject it
				// even when the metadata estimate did not exceed the record limit.
				records.values[0][7] = strings.Repeat("<", audit.MaxEncodedRecordBytes/5)
				metadata.values[0][3] = int64(audit.MaxEncodedRecordBytes)
				code = catalog.CodeInternal
			}
			tx := &identityScriptedTx{fakeCommandTx: &fakeCommandTx{}, queryResults: results}
			from, before := event.At.Add(-time.Hour), event.At.Add(time.Hour)
			filters := audit.Query{Actor: event.Actor, Action: event.Action, RequestID: event.RequestID, TargetType: event.TargetType, TargetID: event.TargetID, OccurredFrom: &from, OccurredBefore: &before}
			got, err := selectAuditExportRecords(t.Context(), tx, restoreTestTenant, filters)
			if code == "" {
				if err != nil || len(got) != 2 || got[0].ID != event.ID || got[1].ID != "00000000-0000-7000-8000-000000000091" {
					t.Fatalf("selected=%+v error=%v", got, err)
				}
				facts, ok := got[0].Details["facts"].(map[string]any)
				if !ok || facts["generation"] != json.Number("9007199254740993") {
					t.Fatalf("precision lost: %v", facts)
				}
			} else if err == nil || catalog.ErrorCodeOf(err) != code || got != nil {
				t.Fatalf("selected=%+v error=%v", got, err)
			}
			if len(tx.queryStatements) != queries || outcome != "metadata query" && !metadata.closed || queries == 2 && outcome != "record query" && !records.closed {
				t.Fatalf("query/cleanup mismatch queries=%d metadata=%t records=%t", len(tx.queryStatements), metadata.closed, records.closed)
			}
			wantArgs := []any{restoreTestTenant, filters.Actor, filters.Action, filters.RequestID, filters.TargetType, filters.TargetID, &from, &before, audit.MaxExportRows + 1}
			for _, args := range tx.queryArguments {
				if !reflect.DeepEqual(args, wantArgs) {
					t.Fatalf("export bounds=%v", args)
				}
			}
			if (strings.HasSuffix(outcome, "query") || strings.HasSuffix(outcome, "iteration")) && !errors.Is(err, failure) {
				t.Fatalf("lost cause: %v", err)
			}
		})
	}
}

// TestAuditExportBindsAccessEvidence checks a populated export and failures before
// an access event can be committed. Existing tests own empty export completion.
func TestAuditExportBindsAccessEvidence(t *testing.T) {
	pool := &pgxpool.Pool{}
	repo := &PostgresRepository{pool: pool}
	failure := errors.New("audit export failure")
	for _, outcome := range []string{"export", "selection failure", "invalid range", "access failure"} {
		t.Run(outcome, func(t *testing.T) {
			event, metadata, records := auditExportRows(t)
			from, before := event.At.Add(-time.Hour), event.At.Add(time.Hour)
			access, err := audit.NewRecord("00000000-0000-7000-8000-000000000092", restoreTestTenant, audit.Principal{ID: "catalog-api", Type: audit.PrincipalServiceExecutor}, audit.ActionAuditExported, "audit_range", restoreTestTenant, "Review retained evidence", audit.Execution{}, map[string]any{"rowCount": 0, "eventsDigest": audit.DigestBytes([]byte("[]")), "occurredFrom": from.Format(time.RFC3339Nano), "occurredBefore": before.Format(time.RFC3339Nano)}, before)
			if err != nil {
				t.Fatal(err)
			}
			request := catalog.AuditExportRequest{Filters: audit.Query{OccurredFrom: &from, OccurredBefore: &before}, Cutoff: before, GeneratedAt: before, Access: access}
			tx := &identityScriptedTx{fakeCommandTx: &fakeCommandTx{}, queryResults: []identityQueryResult{{rows: metadata}, {rows: records}}}
			code := catalog.ErrorCode("")
			writes := 2
			switch outcome {
			case "selection failure":
				tx.queryResults[0].err = failure
				code = catalog.CodeDependencyUnavailable
				writes = 1
			case "invalid range":
				request.GeneratedAt = from
				code = catalog.CodeInternal
				writes = 1
			case "access failure":
				tx.execErrors = []error{nil, failure}
				code = catalog.CodeDependencyUnavailable
			}
			connection := &captureCommandConnection{transaction: tx}
			command := &commandTransaction{pool: pool, conn: connection, tenantID: restoreTestTenant}
			got, err := repo.ExportAuditEvents(withCommandTransaction(t.Context(), command), restoreTestTenant, request)
			if code != "" {
				if err == nil || catalog.ErrorCodeOf(err) != code || !reflect.DeepEqual(got, catalog.AuditExport{}) || tx.rollbacks != 1 {
					t.Fatalf("export=%+v error=%v", got, err)
				}
			} else {
				if err != nil || got.Manifest.RowCount != 1 || got.Manifest.TenantID != restoreTestTenant || got.Manifest.First == nil || got.Manifest.First.ID != event.ID || tx.rollbacks != 0 {
					t.Fatalf("export=%+v error=%v", got, err)
				}
				raw, err := json.Marshal(got)
				if err != nil {
					t.Fatal(err)
				}
				if _, _, err := audit.VerifyExport(raw); err != nil {
					t.Fatalf("export does not verify: %v", err)
				}
				var details map[string]any
				rawDetails, ok := tx.arguments[1][11].([]byte)
				if !ok {
					t.Fatal("audit details type")
				}
				if err := json.Unmarshal(rawDetails, &details); err != nil {
					t.Fatal(err)
				}
				facts, ok := details["facts"].(map[string]any)
				if !ok || facts["rowCount"] != float64(1) || facts["eventsDigest"] != got.Manifest.EventsDigest || tx.arguments[1][10] != got.Manifest.EventsDigest || tx.arguments[1][4] != audit.ActionAuditExported || tx.arguments[1][2] != restoreTestTenant {
					t.Fatalf("access not bound to export: %v", tx.arguments[1])
				}
			}
			if len(tx.statements) != writes || tx.commits != 0 || !reflect.DeepEqual(connection.options, []pgx.TxOptions{{IsoLevel: pgx.RepeatableRead}}) {
				t.Fatalf("export transaction writes=%d options=%v", len(tx.statements), connection.options)
			}
		})
	}
}
