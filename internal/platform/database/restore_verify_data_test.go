package database

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/audit"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/jobs"
)

// Scripted rows exercise Go validation and traversal only. SQL selection,
// permissions and snapshot consistency belong to the PostgreSQL tests.
func TestRestoreScannersPropagateReadFailuresAndCloseRows(t *testing.T) {
	t.Parallel()
	failure := errors.New("scripted restore read failure")
	for _, stage := range restoreVerificationStages()[1:] {
		t.Run(stage.name, func(t *testing.T) {
			for _, mode := range []string{"empty", "query", "scan", "iteration"} {
				t.Run(mode, func(t *testing.T) {
					rows := &identityScriptedRows{}
					result := identityQueryResult{rows: rows}
					switch mode {
					case "query":
						result = identityQueryResult{err: failure}
					case "scan":
						rows.values = [][]any{{}} // A malformed row must not be accepted as empty data.
					case "iteration":
						rows.err = failure
					}
					tx := &identityScriptedTx{queryResults: []identityQueryResult{result}}
					records, more, err := stage.scan(context.Background(), tx, restoreTestTenant, "", 2)
					if mode == "empty" && err != nil || mode != "empty" && err == nil {
						t.Fatalf("records=%v more=%v error=%v", records, more, err)
					}
					if (mode == "query" || mode == "iteration") && !errors.Is(err, failure) {
						t.Fatalf("lost read error: %v", err)
					}
					if len(records) != 0 || more || len(tx.queryStatements) != 1 || len(tx.queryRowStatements) != 0 {
						t.Fatalf("unexpected result or follow-up read: records=%v more=%v tx=%+v", records, more, tx)
					}
					if mode != "query" && !rows.closed {
						t.Fatal("scanner left rows open")
					}
					if tx.queryArguments[0][0] != restoreTestTenant {
						t.Fatal("scanner did not bind the tenant")
					}
				})
			}
		})
	}
}

func TestRestoreBoundedJSONValidation(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name     string
		raw      []byte
		size     int64
		validate bool
	}{
		{"valid", []byte(`{"field":1}`), 11, true},
		{"duplicate keys", []byte(`{"field":1,"field":2}`), 21, false},
		{"malformed", []byte(`{`), 1, false},
		{"oversized", nil, maxRestoreSmallDocumentBytes + 1, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rows := &identityScriptedRows{values: [][]any{{"inspection", restoreTestTenant, tc.size, tc.raw, at.Add(time.Hour), at}}}
			tx := &identityScriptedTx{queryResults: []identityQueryResult{{rows: rows}}}
			calls := 0
			records, more, err := scanRestoreJSONDocuments(t.Context(), tx, restoreTestTenant, "previous", 100, "source_inspections", func(id, tenant string, raw []byte, expires, created time.Time) *RestoreFinding {
				calls++
				if id != "inspection" || tenant != restoreTestTenant || !reflect.DeepEqual(raw, tc.raw) || !expires.Equal(at.Add(time.Hour)) || !created.Equal(at) {
					t.Fatal("validator received different row data")
				}
				return operationalFinding("TEST_FINDING", "source_inspection", id, "test")
			})
			if err != nil || more || len(records) != 1 || !rows.closed {
				t.Fatalf("records=%v more=%v error=%v", records, more, err)
			}
			code := "RESTORE_BOUNDED_DOCUMENT_INVALID"
			if tc.validate {
				code = "TEST_FINDING"
			}
			if (calls == 1) != tc.validate || records[0].finding == nil || records[0].finding.Code != code || records[0].decodedBytes != int64(len(tc.raw)) {
				t.Fatalf("incorrect validation: calls=%d record=%+v", calls, records[0])
			}
			if !reflect.DeepEqual(tx.queryArguments[0], []any{restoreTestTenant, "previous", 17, maxRestoreSmallDocumentBytes}) {
				t.Fatalf("incorrect bounded query arguments: %v", tx.queryArguments)
			}
		})
	}
}

func TestRestoreOutboxClassification(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
	_, event := currentTestEvidence(t, "resource.created", "resource", restoreTestTenant, at)
	unknownType := "not-a-public-event"
	valid := []any{event.ID, event.TenantID, false, int64(len(event.Type)), int64(len(event.Subject)), int64(len(event.Data)), &event.Type, &event.Subject, []byte(event.Data), event.CreatedAt}
	for _, tc := range []struct {
		name        string
		mutate      func([]any)
		code, class string
	}{
		{"public", func([]any) {}, "RESTORE_UNACKNOWLEDGED_OUTBOX_EVENT", "operational"},
		{"internal", func(row []any) { row[2] = true; row[6] = (*string)(nil); row[7] = (*string)(nil); row[8] = []byte(nil) }, "RESTORE_PENDING_INTERNAL_OUTBOX_EVENT", "operational"},
		{"unknown public type", func(row []any) { row[6] = &unknownType }, "RESTORE_PUBLIC_OUTBOX_TYPE_INVALID", "integrity"},
		{"missing type", func(row []any) { row[6] = (*string)(nil) }, "RESTORE_PUBLIC_OUTBOX_REFERENCE_INVALID", "integrity"},
		{"missing subject", func(row []any) { row[7] = (*string)(nil) }, "RESTORE_PUBLIC_OUTBOX_REFERENCE_INVALID", "integrity"},
		{"oversized type", func(row []any) { row[3] = int64(161) }, "RESTORE_PUBLIC_OUTBOX_REFERENCE_INVALID", "integrity"},
		{"oversized subject", func(row []any) { row[4] = int64(513) }, "RESTORE_PUBLIC_OUTBOX_REFERENCE_INVALID", "integrity"},
		{"oversized data", func(row []any) { row[5] = int64(maxRestoreOutboxDataBytes + 1) }, "RESTORE_PUBLIC_OUTBOX_REFERENCE_INVALID", "integrity"},
		{"invalid reference", func(row []any) { row[8] = []byte(`{}`) }, "RESTORE_PUBLIC_OUTBOX_REFERENCE_INVALID", "integrity"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			row := append([]any(nil), valid...)
			tc.mutate(row)
			rows := &identityScriptedRows{values: [][]any{row}}
			tx := &identityScriptedTx{queryResults: []identityQueryResult{{rows: rows}}}
			records, more, err := scanRestoreOutbox(t.Context(), tx, restoreTestTenant, "", 1)
			if err != nil || more || len(records) != 1 || !rows.closed {
				t.Fatalf("records=%v more=%v error=%v", records, more, err)
			}
			finding := records[0].finding
			data, ok := row[8].([]byte)
			if !ok {
				t.Fatal("outbox fixture data is not bytes")
			}
			if finding == nil || finding.Code != tc.code || finding.Class != tc.class || finding.RecordID != event.ID || records[0].decodedBytes != int64(len(data)) {
				t.Fatalf("incorrect finding or accounting: %+v", records[0])
			}
		})
	}
}

func TestRestoreAuditRecordDecoding(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
	event, _ := currentTestEvidence(t, "resource.created", "resource", restoreTestTenant, at)
	details, err := json.Marshal(event.Details)
	if err != nil {
		t.Fatal(err)
	}
	oldFormat := audit.RecordFormatV1 + 1
	valid := []any{event.FormatVersion, event.ID, event.TenantID, &event.Actor, &event.Action, &event.TargetType, &event.TargetID, &event.Reason, &event.RequestID, &event.BeforeDigest, &event.AfterDigest, details, at, int64(len(details) + 512)}
	for _, tc := range []struct {
		name    string
		mutate  func([]any)
		invalid bool
	}{
		{"valid", func([]any) {}, false},
		{"missing format", func(row []any) { row[0] = (*int)(nil) }, true},
		{"unsupported format", func(row []any) { row[0] = &oldFormat }, true},
		{"oversized", func(row []any) { row[13] = int64(audit.MaxEncodedRecordBytes + 1) }, true},
		{"missing actor", func(row []any) { row[3] = (*string)(nil) }, true},
		{"duplicate detail", func(row []any) { row[11] = []byte(`{"a":1,"a":2}`) }, true},
		{"missing details", func(row []any) { row[11] = []byte(nil) }, true},
		{"invalid action", func(row []any) { action := "not-an-action"; row[4] = &action }, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			row := append([]any(nil), valid...)
			tc.mutate(row)
			rows := &identityScriptedRows{values: [][]any{row}}
			tx := &identityScriptedTx{queryResults: []identityQueryResult{{rows: rows}}}
			records, more, err := scanRestoreAuditEvents(t.Context(), tx, restoreTestTenant, "", 1)
			if err != nil || more || len(records) != 1 || !rows.closed {
				t.Fatalf("records=%v more=%v error=%v", records, more, err)
			}
			if (records[0].finding != nil) != tc.invalid || records[0].finding != nil && records[0].finding.Code != "RESTORE_AUDIT_RECORD_INVALID" {
				t.Fatalf("unexpected finding: %+v", records[0].finding)
			}
		})
	}
}

func TestRestoreTraversalStopsAtReportBudgets(t *testing.T) {
	t.Parallel()
	options := validRestoreOptions()
	for _, tc := range []struct {
		name, stage, after string
		report             RestoreVerificationReport
		row                []any
		processed          int
		finding            string
	}{
		{"row budget", "resources", "previous", RestoreVerificationReport{Processed: options.Limit}, nil, options.Limit, ""},
		{"byte budget", "documents", "previous", RestoreVerificationReport{DecodedBytes: MaxRestoreVerificationDecodedBytes}, []any{"document", restoreTestTenant, "sha256:invalid", "application/json", int64(2), int64(2), []byte(`{}`), time.Time{}}, 0, ""},
		{"finding budget", "resources", "current", RestoreVerificationReport{Findings: make([]RestoreFinding, MaxRestoreVerificationFindings-1)}, []any{"current", false, true, false, false, false, false}, 1, "RESTORE_RESOURCE_TOMBSTONE_INVALID"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			report := tc.report
			report.SchemaVersion = 28
			tx := &identityScriptedTx{}
			if tc.row != nil {
				tx.queryResults = []identityQueryResult{{rows: &identityScriptedRows{values: [][]any{tc.row}}}}
			}
			if err := verifyRestoreData(t.Context(), tx, options, &restoreCursor{Stage: tc.stage, After: "previous"}, &report); err != nil {
				t.Fatal(err)
			}
			boundOptions := options
			boundOptions.Cursor = report.Continuation
			cursor, err := parseRestoreCursor(boundOptions)
			if err != nil || cursor.Stage != tc.stage || cursor.After != tc.after || report.Processed != tc.processed || report.Traversal != restoreTraversalMore {
				t.Fatalf("report=%+v cursor=%+v error=%v", report, cursor, err)
			}
			if tc.row == nil && len(tx.queryStatements) != 0 {
				t.Fatal("exhausted row budget performed a read")
			}
			if tc.finding != "" && (len(report.Findings) != MaxRestoreVerificationFindings || report.Findings[len(report.Findings)-1].Code != tc.finding) {
				t.Fatal("finding budget lost the last processed row")
			}
			if tc.name == "byte budget" && (report.DecodedBytes != tc.report.DecodedBytes || len(report.Findings) != 0) {
				t.Fatal("unprocessed oversized page affected the report")
			}
		})
	}
	failure := errors.New("scanner failure")
	tx := &identityScriptedTx{queryResults: []identityQueryResult{{err: failure}}}
	report := RestoreVerificationReport{}
	if err := verifyRestoreData(t.Context(), tx, options, &restoreCursor{Stage: "resources"}, &report); !errors.Is(err, failure) || report.Processed != 0 || report.Continuation != "" {
		t.Fatalf("failed scan advanced traversal: %+v %v", report, err)
	}
}

func TestRestoreTenantStateAndResume(t *testing.T) {
	t.Parallel()
	for _, active := range []bool{true, false} {
		t.Run(map[bool]string{true: "administrator", false: "no administrator"}[active], func(t *testing.T) {
			tx := &identityScriptedTx{queryRows: []pgx.Row{scannerFunc(func(dest ...any) error {
				setScanDestination(t, dest, 0, active)
				return nil
			})}}
			records, more, err := scanRestoreTenantState(context.Background(), tx, restoreTestTenant, "", 1)
			if err != nil || more || len(records) != 1 || records[0].key != restoreTestTenant {
				t.Fatalf("records=%v more=%v error=%v", records, more, err)
			}
			if active && records[0].finding != nil || !active && (records[0].finding == nil || records[0].finding.Code != "RESTORE_NO_ACTIVE_TENANT_ADMIN") {
				t.Fatalf("incorrect administrator finding: %+v", records[0].finding)
			}
			records, more, err = scanRestoreTenantState(context.Background(), tx, restoreTestTenant, restoreTestTenant, 1)
			if err != nil || more || len(records) != 0 || len(tx.queryRowStatements) != 1 {
				t.Fatal("resume reread the singleton tenant state")
			}
		})
	}
	failure := errors.New("tenant read failed")
	tx := &identityScriptedTx{queryRows: []pgx.Row{scannerFunc(func(...any) error { return failure })}}
	if _, _, err := scanRestoreTenantState(context.Background(), tx, restoreTestTenant, "", 1); !errors.Is(err, failure) {
		t.Fatalf("lost tenant read failure: %v", err)
	}
}

func TestRestoreScannersClassifyRetainedRowsAndBoundPages(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
	past, future := at.Add(-time.Second), at.Add(time.Second)
	jobPayload := []byte(`{"sourceId":"` + restoreTestTenant + `","sourceRevision":1,"trigger":"manual"}`)
	cases := []struct {
		name string
		scan func(context.Context, pgx.Tx, string, string, int) ([]restoreRecord, bool, error)
		row  []any
		code string
	}{
		{"live resource", scanRestoreResources, []any{"resource", false, false, false, false, false, false}, ""},
		{"deleted resource", scanRestoreResources, []any{"resource", true, false, false, false, false, false}, ""},
		{"live deletion markers", scanRestoreResources, []any{"resource", false, true, false, false, false, false}, "RESTORE_RESOURCE_TOMBSTONE_INVALID"},
		{"missing deletion markers", scanRestoreResources, []any{"resource", true, false, true, false, false, false}, "RESTORE_RESOURCE_TOMBSTONE_INVALID"},
		{"deleted adopted resource", scanRestoreResources, []any{"resource", true, false, false, true, false, false}, "RESTORE_RESOURCE_TOMBSTONE_INVALID"},
		{"deleted reviewed resource", scanRestoreResources, []any{"resource", true, false, false, false, true, false}, "RESTORE_RESOURCE_TOMBSTONE_INVALID"},
		{"deleted released resource", scanRestoreResources, []any{"resource", true, false, false, false, false, true}, "RESTORE_RESOURCE_TOMBSTONE_INVALID"},
		{"active membership", scanRestoreMemberships, []any{"member", "active", true, true}, ""},
		{"offboarded membership", scanRestoreMemberships, []any{"member", "offboarded", false, false}, ""},
		{"offboarded role", scanRestoreMemberships, []any{"member", "offboarded", true, false}, "RESTORE_OFFBOARDED_MEMBERSHIP_HAS_ACTIVE_GRANTS"},
		{"offboarded namespace", scanRestoreMemberships, []any{"member", "offboarded", false, true}, "RESTORE_OFFBOARDED_MEMBERSHIP_HAS_ACTIVE_GRANTS"},
		{"queued job", scanRestoreJobs, []any{"job", "source.sync", 2, int64(len(jobPayload)), jobPayload, jobs.StateQueued, (*time.Time)(nil), at}, "RESTORE_ACTIONABLE_JOB_RETAINED"},
		{"expired job", scanRestoreJobs, []any{"job", "source.sync", 2, int64(len(jobPayload)), jobPayload, jobs.StateRunning, &past, at}, "RESTORE_EXPIRED_JOB_LEASE"},
		{"lease boundary", scanRestoreJobs, []any{"job", "source.sync", 2, int64(len(jobPayload)), jobPayload, jobs.StateRunning, &at, at}, "RESTORE_EXPIRED_JOB_LEASE"},
		{"unexpired job", scanRestoreJobs, []any{"job", "source.sync", 2, int64(len(jobPayload)), jobPayload, jobs.StateRunning, &future, at}, "RESTORE_ACTIONABLE_JOB_RETAINED"},
		{"invalid payload", scanRestoreJobs, []any{"job", "source.sync", 2, int64(1), []byte(`{`), jobs.StateQueued, (*time.Time)(nil), at}, "RESTORE_ACTIONABLE_JOB_PAYLOAD_INVALID"},
		{"oversized job", scanRestoreJobs, []any{"job", "", 2, int64(maxRestoreSmallRecordBytes + 1), []byte(nil), jobs.StateQueued, (*time.Time)(nil), at}, "RESTORE_ACTIONABLE_JOB_PAYLOAD_INVALID"},
		{"reservation", scanRestoreIdempotencyV2, []any{restoreTestTenant, true, at, future}, "RESTORE_PENDING_IDEMPOTENCY_V2"},
		{"missing execution token", scanRestoreIdempotencyV2, []any{restoreTestTenant, false, at, future}, "RESTORE_IDEMPOTENCY_STATE_INVALID"},
		{"invalid reservation time", scanRestoreIdempotencyV2, []any{restoreTestTenant, true, at, past}, "RESTORE_IDEMPOTENCY_TIME_INVALID"},
		{"bound delivery", scanRestoreWebhookDeliveries, []any{"delivery", "event", int64(4), "type", true, true}, ""},
		{"unbound delivery", scanRestoreWebhookDeliveries, []any{"delivery", "event", int64(4), "type", false, true}, "RESTORE_WEBHOOK_DELIVERY_BINDING_INVALID"},
		{"oversized event type", scanRestoreWebhookDeliveries, []any{"delivery", "event", int64(161), "", true, true}, "RESTORE_WEBHOOK_DELIVERY_BINDING_INVALID"},
		{"invalid cleanup", scanRestoreWebhookDeliveries, []any{"delivery", "event", int64(4), "type", true, false}, "RESTORE_WEBHOOK_DELIVERY_CLEANUP_INVALID"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for _, extra := range []bool{false, true} {
				rows := &identityScriptedRows{values: [][]any{tc.row}}
				if extra {
					rows.values = append(rows.values, tc.row)
				}
				tx := &identityScriptedTx{queryResults: []identityQueryResult{{rows: rows}}}
				records, more, err := tc.scan(context.Background(), tx, restoreTestTenant, "", 1)
				if err != nil || more != extra || len(records) != 1 || !rows.closed {
					t.Fatalf("records=%v more=%v error=%v closed=%v", records, more, err, rows.closed)
				}
				finding := records[0].finding
				if tc.code == "" && finding != nil || tc.code != "" && (finding == nil || finding.Code != tc.code) {
					t.Fatalf("finding=%+v, want %q", finding, tc.code)
				}
				if records[0].key == "" || len(tx.queryArguments) != 1 || tx.queryArguments[0][0] != restoreTestTenant {
					t.Fatal("lost position or tenant binding")
				}
			}
		})
	}
}

func TestRestoreDocumentValidation(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
	content := []byte(`{"name":"example"}`)
	sum := sha256.Sum256(content)
	id := hex.EncodeToString(sum[:])
	valid := []any{id, restoreTestTenant, "sha256:" + id, "application/json", int64(len(content)), int64(len(content)), content, at}
	for _, tc := range []struct {
		name    string
		mutate  func([]any)
		invalid bool
	}{
		{"valid", func([]any) {}, false},
		{"digest mismatch", func(row []any) { row[2] = "sha256:incorrect" }, true},
		{"wrong tenant", func(row []any) { row[1] = "00000000-0000-7000-8000-000000000002" }, true},
		{"size mismatch", func(row []any) { row[4] = int64(1) }, true},
		{"oversized stored content", func(row []any) { row[5] = int64(catalog.MaxCatalogDocumentBytes + 1); row[6] = []byte(nil) }, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			row := append([]any(nil), valid...)
			tc.mutate(row)
			rows := &identityScriptedRows{values: [][]any{row}}
			tx := &identityScriptedTx{queryResults: []identityQueryResult{{rows: rows}}}
			records, more, err := scanRestoreDocuments(context.Background(), tx, restoreTestTenant, "", 20)
			if err != nil || more || len(records) != 1 || !rows.closed {
				t.Fatalf("records=%v more=%v error=%v", records, more, err)
			}
			if (records[0].finding != nil) != tc.invalid || records[0].finding != nil && records[0].finding.Code != "RESTORE_CATALOG_DOCUMENT_INVALID" {
				t.Fatalf("finding=%+v", records[0].finding)
			}
			data, ok := row[6].([]byte)
			if !ok {
				t.Fatal("document fixture content is not bytes")
			}
			if records[0].decodedBytes != int64(len(data)) || tx.queryArguments[0][2] != 5 {
				t.Fatal("document byte accounting or bounded selection changed")
			}
		})
	}
}

func TestRestoreTraversalContinuationAndCancellation(t *testing.T) {
	t.Parallel()
	options := validRestoreOptions()
	options.Limit = 1
	rows := &identityScriptedRows{values: [][]any{{"first", false, false, false, false, false, false}, {"second", false, false, false, false, false, false}}}
	tx := &identityScriptedTx{queryResults: []identityQueryResult{{rows: rows}}}
	report := RestoreVerificationReport{SchemaVersion: 28, Traversal: restoreTraversalDone, Integrity: restoreIntegrityValid}
	if err := verifyRestoreData(context.Background(), tx, options, &restoreCursor{Stage: "resources"}, &report); err != nil {
		t.Fatal(err)
	}
	options.Cursor = report.Continuation
	cursor, err := parseRestoreCursor(options)
	if err != nil || cursor.Stage != "resources" || cursor.After != "first" || report.Processed != 1 || report.Traversal != restoreTraversalMore || report.Integrity != restoreIntegrityValid {
		t.Fatalf("report=%+v cursor=%+v error=%v", report, cursor, err)
	}
	// Resume against empty remaining stages; the previous page must not be reread.
	tx = &identityScriptedTx{}
	for range restoreVerificationStages()[3:] {
		tx.queryResults = append(tx.queryResults, identityQueryResult{rows: &identityScriptedRows{}})
	}
	resumed := RestoreVerificationReport{SchemaVersion: 28, Traversal: restoreTraversalDone, Integrity: restoreIntegrityValid}
	if err := verifyRestoreData(context.Background(), tx, options, cursor, &resumed); err != nil {
		t.Fatal(err)
	}
	if resumed.Processed != 0 || resumed.Continuation != "" || resumed.Traversal != restoreTraversalDone || tx.queryArguments[0][1] != "first" || tx.queryArguments[1][1] != "" || len(tx.queryResults) != 0 {
		t.Fatalf("invalid resumed traversal: report=%+v tx=%+v", resumed, tx)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	untouched := RestoreVerificationReport{}
	tx = &identityScriptedTx{}
	if err := verifyRestoreData(ctx, tx, options, nil, &untouched); !errors.Is(err, context.Canceled) || len(tx.queryStatements) != 0 || !reflect.DeepEqual(untouched, RestoreVerificationReport{}) {
		t.Fatalf("cancelled traversal did work: report=%+v error=%v", untouched, err)
	}
	if err := verifyRestoreData(context.Background(), tx, options, &restoreCursor{Stage: "unknown"}, &untouched); err == nil {
		t.Fatal("unknown stage accepted")
	}
}
