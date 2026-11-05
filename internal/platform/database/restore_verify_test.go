package database

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/federation"
)

const restoreTestTenant = "00000000-0000-7000-8000-000000000001"

func validRestoreOptions() RestoreVerificationOptions {
	return RestoreVerificationOptions{TenantID: restoreTestTenant, RecoveryPoint: "backup-2026-09-04T12:00Z", APIRole: "catalog_api", WorkerRole: "catalog_worker", Limit: 37}
}

type restorePageFixture struct {
	options RestoreVerificationOptions
	cursor  *restoreCursor
	report  RestoreVerificationReport
	tx      *identityScriptedTx
	rows    *identityScriptedRows
}

// Start at the final stage so page decisions need no simulated schema inventory
// or whole-catalog traversal. Existing scanner tests own row-validation matrices.
func newRestorePageFixture(t *testing.T) restorePageFixture {
	t.Helper()
	options := validRestoreOptions()
	options.Limit = 1
	cursor, err := encodeRestoreCursor(options, 17, "webhook-deliveries", "00000000-0000-7000-8000-000000000010")
	if err != nil {
		t.Fatal(err)
	}
	options.Cursor = cursor
	parsed, err := parseRestoreCursor(options)
	if err != nil {
		t.Fatal(err)
	}
	report := RestoreVerificationReport{FormatVersion: RestoreVerificationFormatVersion, CheckSetVersion: RestoreVerificationCheckSetVersion,
		TenantID: options.TenantID, RecoveryPoint: options.RecoveryPoint, SchemaVersion: 17, PrivilegeManifestVersion: runtimePrivilegeManifestVersion,
		Traversal: restoreTraversalDone, Integrity: restoreIntegrityValid, Findings: []RestoreFinding{}, AssuranceBoundary: "retained assurance boundary"}
	rows := &identityScriptedRows{}
	tx := &identityScriptedTx{fakeCommandTx: &fakeCommandTx{}, queryResults: []identityQueryResult{{rows: rows}}}
	return restorePageFixture{options: options, cursor: parsed, report: report, tx: tx, rows: rows}
}

func TestRestorePageRejectsDifferentActiveVersions(t *testing.T) {
	t.Parallel()
	for _, version := range []string{"schema", "privilege manifest"} {
		t.Run(version, func(t *testing.T) {
			for _, usable := range []bool{false, true} {
				f := newRestorePageFixture(t)
				if version == "schema" {
					f.cursor.SchemaVersion++
				} else {
					f.cursor.PrivilegeManifestVersion++
				}
				// Rebind correctly: parsing must accept this cursor, while the
				// active-version check must refuse it even for an unusable schema.
				f.cursor.BindingDigest = restoreBindingDigest(f.options, f.cursor.SchemaVersion, f.cursor.PrivilegeManifestVersion, f.cursor.Stage, f.cursor.After)
				raw, err := json.Marshal(f.cursor)
				if err != nil {
					t.Fatal(err)
				}
				f.options.Cursor = base64.RawURLEncoding.EncodeToString(raw)
				parsed, err := parseRestoreCursor(f.options)
				if err != nil {
					t.Fatalf("well-bound cursor failed parsing: %v", err)
				}
				before := f.report
				err = verifyRestorePage(t.Context(), f.tx, f.options, parsed, usable, &f.report)
				if err == nil || !strings.Contains(err.Error(), "does not match the active schema or privilege manifest") || !reflect.DeepEqual(f.report, before) {
					t.Fatalf("usable=%t: active version mismatch changed report or was accepted: %+v, %v", usable, f.report, err)
				}
				if len(f.tx.queryStatements) != 0 || len(f.tx.queryRowStatements) != 0 || len(f.tx.statements) != 0 || f.rows.closed || f.tx.commits != 0 || f.tx.rollbacks != 0 {
					t.Fatal("mismatched cursor accessed data or took transaction ownership")
				}
			}
		})
	}
}

func TestRestorePageClassifiesFindings(t *testing.T) {
	t.Parallel()
	integrity := *integrityFinding("RESTORE_GUARD_INVENTORY_INVALID", "schema", "", "investigate retained schema")
	repairable := *repairableFinding("RESTORE_SEARCH_PROJECTION_MISSING", "observation", "", "review derived projection repair")
	operational := *operationalFinding("RESTORE_PENDING_IDEMPOTENCY_V2", "idempotency", "", "review interrupted command")
	for _, test := range []struct {
		name      string
		findings  []RestoreFinding
		traversal string
		integrity string
	}{
		{"empty", []RestoreFinding{}, restoreTraversalDone, restoreIntegrityValid},
		{"repairable", []RestoreFinding{repairable}, restoreTraversalDone, restoreIntegrityValid},
		{"operational", []RestoreFinding{operational}, restoreTraversalMore, restoreIntegrityValid},
		{"warnings", []RestoreFinding{repairable, operational}, restoreTraversalMore, restoreIntegrityValid},
		{"integrity", []RestoreFinding{integrity}, restoreTraversalDone, restoreIntegrityInvalid},
		{"mixed", []RestoreFinding{repairable, integrity, operational}, restoreTraversalMore, restoreIntegrityInvalid},
		{"blocked", []RestoreFinding{integrity}, restoreTraversalBlocked, restoreIntegrityInvalid},
	} {
		t.Run(test.name, func(t *testing.T) {
			options := validRestoreOptions()
			report := RestoreVerificationReport{TenantID: options.TenantID, RecoveryPoint: options.RecoveryPoint,
				FormatVersion: RestoreVerificationFormatVersion, CheckSetVersion: RestoreVerificationCheckSetVersion,
				SchemaVersion: 17, PrivilegeManifestVersion: runtimePrivilegeManifestVersion,
				Traversal: test.traversal, Integrity: restoreIntegrityValid,
				Findings: append([]RestoreFinding{}, test.findings...), AssuranceBoundary: "retained assurance boundary"}
			if test.traversal != restoreTraversalBlocked {
				report.Processed, report.DecodedBytes = 1, 100
			}
			if test.traversal == restoreTraversalMore {
				var err error
				report.Continuation, err = encodeRestoreCursor(options, report.SchemaVersion, "outbox", "00000000-0000-7000-8000-000000000010")
				if err != nil {
					t.Fatal(err)
				}
			}
			want := report
			want.Findings, want.Integrity = test.findings, test.integrity
			classifyRestorePage(&report)
			if !reflect.DeepEqual(report, want) {
				t.Fatalf("classification changed page evidence or traversal: %+v; want %+v", report, want)
			}
		})
	}
}

func TestRestorePageBlocksUnsafeTraversal(t *testing.T) {
	t.Parallel()
	for _, firstPage := range []bool{false, true} {
		f := newRestorePageFixture(t)
		if firstPage {
			f.cursor, f.options.Cursor = nil, ""
		}
		f.report.Findings = []RestoreFinding{*integrityFinding("RESTORE_MIGRATION_PENDING", "schema", "", "review migration plan")}
		want := f.report
		want.Traversal, want.Integrity = restoreTraversalBlocked, restoreIntegrityInvalid
		if err := verifyRestorePage(t.Context(), f.tx, f.options, f.cursor, false, &f.report); err != nil || !reflect.DeepEqual(f.report, want) {
			t.Fatalf("blocked page = %+v, %v; want %+v", f.report, err, want)
		}
		if len(f.tx.queryStatements) != 0 || len(f.tx.queryRowStatements) != 0 || len(f.tx.statements) != 0 || f.rows.closed || f.tx.commits != 0 || f.tx.rollbacks != 0 {
			t.Fatal("unusable schema allowed traversal or took transaction ownership")
		}
	}
}

func TestRestorePageCompletesMatchingContinuation(t *testing.T) {
	t.Parallel()
	f := newRestorePageFixture(t)
	want := f.report
	if err := verifyRestorePage(t.Context(), f.tx, f.options, f.cursor, true, &f.report); err != nil || !reflect.DeepEqual(f.report, want) {
		t.Fatalf("final empty page = %+v, %v; want %+v", f.report, err, want)
	}
	if !reflect.DeepEqual(f.tx.queryArguments, [][]any{{f.options.TenantID, f.cursor.After, f.options.Limit + 1}}) || !f.rows.closed || len(f.tx.queryRowStatements) != 0 || len(f.tx.statements) != 0 || f.tx.commits != 0 || f.tx.rollbacks != 0 {
		t.Fatalf("page lost traversal binding or cleanup: queries=%v closed=%t", f.tx.queryArguments, f.rows.closed)
	}
}

func TestRestorePageClassifiesContinuedDataFindings(t *testing.T) {
	t.Parallel()
	f := newRestorePageFixture(t)
	const delivery = "00000000-0000-7000-8000-000000000011"
	const event = "00000000-0000-7000-8000-000000000012"
	const eventType = "com.open.agentic.catalog.webhook.test.v1"
	f.rows.values = [][]any{{delivery, event, int64(len(eventType)), eventType, false, true}}
	if err := verifyRestorePage(t.Context(), f.tx, f.options, f.cursor, true, &f.report); err != nil {
		t.Fatal(err)
	}
	if f.report.Traversal != restoreTraversalMore || f.report.Integrity != restoreIntegrityInvalid || f.report.Processed != 1 || len(f.report.Findings) != 1 || f.report.Findings[0].Code != "RESTORE_WEBHOOK_DELIVERY_BINDING_INVALID" {
		t.Fatalf("continued invalid page lost its independent conclusions: %+v", f.report)
	}
	f.options.Cursor = f.report.Continuation
	cursor, err := parseRestoreCursor(f.options)
	if err != nil || cursor == nil || cursor.Stage != f.cursor.Stage || cursor.After != delivery || !f.rows.closed {
		t.Fatalf("page lost continuation or row cleanup: %+v, %v", cursor, err)
	}
	if len(f.tx.queryStatements) != 1 || len(f.tx.statements) != 0 || f.tx.commits != 0 || f.tx.rollbacks != 0 {
		t.Fatal("continued page ran extra work or took transaction ownership")
	}
}

func TestRestorePagePropagatesDataReadFailure(t *testing.T) {
	t.Parallel()
	f := newRestorePageFixture(t)
	failure := errors.New("restore page read failed")
	f.tx.queryResults[0].err = failure
	if err := verifyRestorePage(t.Context(), f.tx, f.options, f.cursor, true, &f.report); !errors.Is(err, failure) {
		t.Fatalf("page lost data read failure: %v", err)
	}
	if len(f.tx.queryStatements) != 1 || len(f.tx.queryRowStatements) != 0 || len(f.tx.statements) != 0 || f.rows.closed || f.tx.commits != 0 || f.tx.rollbacks != 0 {
		t.Fatal("failed page continued work or took transaction ownership")
	}
}

func TestRestoreVerificationOptionsAndCursorBinding(t *testing.T) {
	t.Parallel()
	options := validRestoreOptions()
	if parsed, err := parseRestoreCursor(options); err != nil || parsed != nil {
		t.Fatalf("first-page cursor = %+v, %v", parsed, err)
	}
	if err := ValidateRestoreVerificationOptions(options); err != nil {
		t.Fatalf("valid options returned %v", err)
	}
	cursor, err := encodeRestoreCursor(options, 17, "documents", "abc")
	if err != nil {
		t.Fatal(err)
	}
	options.Cursor = cursor
	parsed, err := parseRestoreCursor(options)
	if err != nil || parsed.Stage != "documents" || parsed.After != "abc" || parsed.SchemaVersion != 17 {
		t.Fatalf("parsed cursor = %#v, %v", parsed, err)
	}
	for name, mutate := range map[string]func(*RestoreVerificationOptions){
		"tenant":         func(value *RestoreVerificationOptions) { value.TenantID = "bad" },
		"recovery point": func(value *RestoreVerificationOptions) { value.RecoveryPoint = "line\nbreak" },
		"roles":          func(value *RestoreVerificationOptions) { value.WorkerRole = value.APIRole },
		"zero limit":     func(value *RestoreVerificationOptions) { value.Limit = 0 },
		"large limit":    func(value *RestoreVerificationOptions) { value.Limit = MaxRestoreVerificationLimit + 1 },
		"large cursor": func(value *RestoreVerificationOptions) {
			value.Cursor = strings.Repeat("x", MaxRestoreVerificationCursorBytes+1)
		},
	} {
		t.Run(name, func(t *testing.T) {
			value := validRestoreOptions()
			mutate(&value)
			if err := ValidateRestoreVerificationOptions(value); err == nil {
				t.Fatalf("invalid options were accepted: %#v", value)
			}
		})
	}
}

func TestRestoreCursorRejectsTamperingRebindingAndNonClosedJSON(t *testing.T) {
	t.Parallel()
	options := validRestoreOptions()
	cursor, err := encodeRestoreCursor(options, 17, "documents", "abc")
	if err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*RestoreVerificationOptions){
		"recovery point": func(value *RestoreVerificationOptions) { value.RecoveryPoint = "another-backup" },
		"tenant":         func(value *RestoreVerificationOptions) { value.TenantID = "00000000-0000-7000-8000-000000000002" },
		"API role":       func(value *RestoreVerificationOptions) { value.APIRole = "another_api" },
		"worker role":    func(value *RestoreVerificationOptions) { value.WorkerRole = "another_worker" },
		"limit":          func(value *RestoreVerificationOptions) { value.Limit++ },
	} {
		t.Run(name, func(t *testing.T) {
			value := options
			value.Cursor = cursor
			mutate(&value)
			if _, err := parseRestoreCursor(value); err == nil {
				t.Fatal("rebound cursor was accepted")
			}
		})
	}
	for _, raw := range []string{
		`{"formatVersion":1,"formatVersion":1}`,
		`{"formatVersion":1,"checkSetVersion":1,"bindingDigest":"x","schemaVersion":17,"privilegeManifestVersion":3,"limit":37,"stage":"documents","unknown":true}`,
		`{} trailing`,
	} {
		value := options
		value.Cursor = base64.RawURLEncoding.EncodeToString([]byte(raw))
		if _, err := parseRestoreCursor(value); err == nil {
			t.Fatalf("invalid cursor %q was accepted", raw)
		}
	}
	raw, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		t.Fatal(err)
	}
	var decoded restoreCursor
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*restoreCursor){
		"schema version":             func(value *restoreCursor) { value.SchemaVersion++ },
		"privilege manifest version": func(value *restoreCursor) { value.PrivilegeManifestVersion++ },
		"stage":                      func(value *restoreCursor) { value.Stage = "memberships" },
		"position":                   func(value *restoreCursor) { value.After = "abd" },
	} {
		t.Run(name, func(t *testing.T) {
			mutated := decoded
			mutate(&mutated)
			mutatedRaw, marshalErr := json.Marshal(mutated)
			if marshalErr != nil {
				t.Fatal(marshalErr)
			}
			value := options
			value.Cursor = base64.RawURLEncoding.EncodeToString(mutatedRaw)
			if _, parseErr := parseRestoreCursor(value); parseErr == nil {
				t.Fatal("cursor with modified traversal state was accepted")
			}
		})
	}
	unknownStage, err := encodeRestoreCursor(options, 17, "not-a-stage", "")
	if err != nil {
		t.Fatal(err)
	}
	value := options
	value.Cursor = unknownStage
	if _, err := parseRestoreCursor(value); err == nil {
		t.Fatal("unknown cursor stage was accepted")
	}
	value = options
	replacement := byte('A')
	if cursor[0] == replacement {
		replacement = 'B'
	}
	value.Cursor = string(replacement) + cursor[1:]
	if _, err := parseRestoreCursor(value); err == nil {
		t.Fatal("tampered cursor was accepted")
	}
}

func TestRestoreReportAndCompositePositionsStayBounded(t *testing.T) {
	t.Parallel()
	if cursor, err := encodeRestoreCursor(validRestoreOptions(), 33, "documents", strings.Repeat("x", MaxRestoreVerificationCursorBytes)); err == nil || cursor != "" {
		t.Fatalf("oversized continuation = %q, %v", cursor, err)
	}
	report := RestoreVerificationReport{Findings: []RestoreFinding{}, AssuranceBoundary: "logical"}
	if err := ensureRestoreReportBound(report); err != nil {
		t.Fatal(err)
	}
	report.DecodedBytes = MaxRestoreVerificationDecodedBytes + 1
	if err := ensureRestoreReportBound(report); err == nil {
		t.Fatal("decoded-byte overflow was accepted")
	}
	report.DecodedBytes = 0
	report.Findings = make([]RestoreFinding, MaxRestoreVerificationFindings+1)
	if err := ensureRestoreReportBound(report); err == nil {
		t.Fatal("finding-count overflow was accepted")
	}
	report.Findings = []RestoreFinding{}
	report.AssuranceBoundary = strings.Repeat("x", MaxRestoreVerificationOutputBytes)
	if err := ensureRestoreReportBound(report); err == nil {
		t.Fatal("encoded report overflow was accepted")
	}

	key := encodeSourceRevisionKey(restoreTestTenant, 42)
	source, revision, err := decodeSourceRevisionKey(key)
	if err != nil || source != restoreTestTenant || revision != 42 {
		t.Fatalf("source revision key = %q, %d, %v", source, revision, err)
	}
	if _, _, err := decodeSourceRevisionKey("invalid"); err == nil {
		t.Fatal("invalid source revision key was accepted")
	}
	retrievalKey := encodeRetrievalStateKey(restoreTestTenant, 7, "sha256:"+strings.Repeat("b", 64))
	retrievalSource, retrievalRevision, retrievalRequest, err := decodeRetrievalStateKey(retrievalKey)
	if err != nil || retrievalSource == nil || *retrievalSource != restoreTestTenant || retrievalRevision != 7 || retrievalRequest != "sha256:"+strings.Repeat("b", 64) {
		t.Fatalf("retrieval state key = %v, %d, %q, %v", retrievalSource, retrievalRevision, retrievalRequest, err)
	}
	for _, invalid := range []string{"invalid", restoreTestTenant + "/0/sha256:" + strings.Repeat("b", 64), restoreTestTenant + "/1/sha256:" + strings.Repeat("B", 64)} {
		if _, _, _, err := decodeRetrievalStateKey(invalid); err == nil {
			t.Fatalf("invalid retrieval state position %q was accepted", invalid)
		}
	}
	at := time.Date(2026, time.September, 4, 12, 30, 0, 123000000, time.UTC)
	timeKey := encodeTimeUUIDKey(at, restoreTestTenant)
	decodedAt, decodedID, err := decodeTimeUUIDKey(timeKey)
	if err != nil || decodedAt == nil || !decodedAt.Equal(at) || decodedID != restoreTestTenant {
		t.Fatalf("decoded time position = %v, %q, %v", decodedAt, decodedID, err)
	}
	if _, _, err := decodeTimeUUIDKey("invalid"); err == nil {
		t.Fatal("invalid time position was accepted")
	}
	occurrenceKey := encodeOccurrenceKey(restoreTestTenant, "sha256:"+strings.Repeat("a", 64), "/entries/42")
	snapshotID, retrievalDigest, entryLocation, err := decodeOccurrenceKey(occurrenceKey)
	if err != nil || snapshotID == nil || *snapshotID != restoreTestTenant || retrievalDigest != "sha256:"+strings.Repeat("a", 64) || entryLocation != "/entries/42" {
		t.Fatalf("decoded occurrence position = %v, %q, %q, %v", snapshotID, retrievalDigest, entryLocation, err)
	}
	for _, invalid := range []string{"invalid", restoreTestTenant + "/bad//entries/0", restoreTestTenant + "/sha256:" + strings.Repeat("A", 64) + "//entries/0", restoreTestTenant + "/sha256:" + strings.Repeat("a", 64) + "/"} {
		if _, _, _, err := decodeOccurrenceKey(invalid); err == nil {
			t.Fatalf("invalid occurrence position %q was accepted", invalid)
		}
	}
}

func TestRestoreSearchProjectionRepairIndexRequiresExactShape(t *testing.T) {
	t.Parallel()
	const predicate = `((normalized_entry_digest IS NOT NULL) AND ((search_display_name IS NULL) OR (search_description IS NULL) OR (search_tags IS NULL) OR (search_representative_queries IS NULL) OR (search_publisher IS NULL) OR (search_vector IS NULL) OR (search_vector_version <> 2) OR (search_filter_version <> 1)))`
	type indexShape struct {
		valid, ready, live, unique bool
		keyCount, attributeCount   int
		table, method              string
		firstKey, secondKey        string
		predicate                  string
	}
	exact := indexShape{valid: true, ready: true, live: true, keyCount: 2, attributeCount: 2, table: "observations", method: "btree", firstKey: "tenant_id", secondKey: "id", predicate: predicate}
	tests := map[string]struct {
		shape   indexShape
		wantErr bool
	}{
		"exact":           {shape: exact},
		"wrong table":     {shape: func() indexShape { value := exact; value.table = "jobs"; return value }(), wantErr: true},
		"wrong method":    {shape: func() indexShape { value := exact; value.method = "hash"; return value }(), wantErr: true},
		"included column": {shape: func() indexShape { value := exact; value.attributeCount = 3; return value }(), wantErr: true},
		"not live":        {shape: func() indexShape { value := exact; value.live = false; return value }(), wantErr: true},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			transaction := &identityScriptedTx{fakeCommandTx: &fakeCommandTx{}}
			transaction.queryRows = append(transaction.queryRows, scannerFunc(func(destinations ...any) error {
				setScanDestination(t, destinations, 0, test.shape.valid)
				setScanDestination(t, destinations, 1, test.shape.ready)
				setScanDestination(t, destinations, 2, test.shape.live)
				setScanDestination(t, destinations, 3, test.shape.unique)
				setScanDestination(t, destinations, 4, test.shape.keyCount)
				setScanDestination(t, destinations, 5, test.shape.attributeCount)
				setScanDestination(t, destinations, 6, test.shape.table)
				setScanDestination(t, destinations, 7, test.shape.method)
				setScanDestination(t, destinations, 8, test.shape.firstKey)
				setScanDestination(t, destinations, 9, test.shape.secondKey)
				setScanDestination(t, destinations, 10, test.shape.predicate)
				return nil
			}))
			err := verifySearchProjectionRepairIndex(t.Context(), transaction, "catalog")
			if (err != nil) != test.wantErr {
				t.Fatalf("verify exact index returned %v", err)
			}
		})
	}
}

func TestEmbeddedCriticalTriggerSourcesAreComplete(t *testing.T) {
	t.Parallel()
	sources, err := embeddedTriggerFunctionSources()
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range expectedRestoreTriggers {
		if strings.TrimSpace(sources[expected.function]) == "" {
			t.Fatalf("missing source for %s", expected.function)
		}
	}
	raw, err := json.Marshal(RestoreVerificationReport{FormatVersion: 1, Findings: []RestoreFinding{}})
	if err != nil || !json.Valid(raw) {
		t.Fatalf("closed report JSON = %q, %v", raw, err)
	}
}

func TestRestoreFindingClassificationAndLimit(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		finding  *RestoreFinding
		class    string
		severity string
	}{
		{name: "integrity", finding: integrityFinding("INTEGRITY", "resource", "record", "stop"), class: "integrity", severity: "error"},
		{name: "repairable", finding: repairableFinding("REPAIRABLE", "resource", "record", "repair"), class: "repairable", severity: "warning"},
		{name: "operational", finding: operationalFinding("OPERATIONAL", "resource", "record", "wait"), class: "operational", severity: "warning"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if test.finding.Code == "" || test.finding.Class != test.class || test.finding.Severity != test.severity || test.finding.Domain != "resource" || test.finding.RecordID != "record" || test.finding.Disposition == "" {
				t.Fatalf("finding = %#v", test.finding)
			}
		})
	}
	report := RestoreVerificationReport{Findings: make([]RestoreFinding, MaxRestoreVerificationFindings)}
	appendRestoreFinding(&report, *integrityFinding("OVERFLOW", "resource", "record", "stop"))
	if len(report.Findings) != MaxRestoreVerificationFindings {
		t.Fatalf("finding count = %d", len(report.Findings))
	}
	report.Findings = report.Findings[:0]
	appendRestoreFinding(&report, *integrityFinding("FIRST", "resource", "record", "stop"))
	if len(report.Findings) != 1 || report.Findings[0].Code != "FIRST" {
		t.Fatalf("findings = %#v", report.Findings)
	}
}

func TestSamePostgresTimeIsOrderIndependentAtDatabasePrecision(t *testing.T) {
	t.Parallel()
	retained := time.Date(2026, time.September, 4, 22, 34, 48, 610204875, time.FixedZone("source", 2*60*60))
	databaseValue := retained.UTC().Truncate(time.Microsecond)
	if !samePostgresTime(retained, databaseValue) || !samePostgresTime(databaseValue, retained) {
		t.Fatal("equivalent timestamps were rejected after PostgreSQL precision normalization")
	}
	if samePostgresTime(retained, databaseValue.Add(time.Microsecond)) {
		t.Fatal("distinct PostgreSQL timestamps were accepted")
	}
}

func TestRestoreSourceRevisionTopologyComparison(t *testing.T) {
	t.Parallel()
	source := federation.Source{
		DiscoveryAssignments: []federation.DiscoveryAssignment{
			{Host: "z.example.test", Audience: federation.DiscoveryAudience("public")},
			{Host: "a.example.test", Audience: federation.DiscoveryAudience("tenant")},
		},
		Referrals: federation.ReferralPolicy{ApprovedSourceIDs: []string{
			"00000000-0000-7000-8000-000000000002",
			"00000000-0000-7000-8000-000000000001",
		}},
	}
	assignments := []byte(`[{"host":"a.example.test","audience":"tenant"},{"host":"z.example.test","audience":"public"}]`)
	referrals := []byte(`["00000000-0000-7000-8000-000000000001","00000000-0000-7000-8000-000000000002"]`)
	if !sameSourceRevisionTopology(source, assignments, referrals) {
		t.Fatal("equivalent topology with a different retained order was rejected")
	}
	if sameSourceRevisionTopology(source, []byte(`[]`), referrals) {
		t.Fatal("missing assignment was accepted")
	}
	if sameSourceRevisionTopology(source, assignments, []byte(`[]`)) {
		t.Fatal("missing referral was accepted")
	}
	if sameSourceRevisionTopology(source, []byte(`{`), referrals) || sameSourceRevisionTopology(source, assignments, []byte(`{`)) {
		t.Fatal("malformed relational topology was accepted")
	}
}

func TestRestoreAuditDetailsPreserveNumbersAndRejectDuplicateKeys(t *testing.T) {
	t.Parallel()
	var details map[string]any
	if err := decodeRestoreAuditDetails([]byte(`{"count":9007199254740993}`), &details); err != nil {
		t.Fatal(err)
	}
	if value, ok := details["count"].(json.Number); !ok || value.String() != "9007199254740993" {
		t.Fatalf("audit count = %#v", details["count"])
	}
	for _, raw := range [][]byte{[]byte(`{"a":1,"a":2}`), []byte(`{`)} {
		if err := decodeRestoreAuditDetails(raw, &details); err == nil {
			t.Fatalf("invalid audit details %q were accepted", raw)
		}
	}
	if boundedStageLimit(4, 8) != 4 || boundedStageLimit(12, 8) != 8 || strconvVersion(17) != "17" {
		t.Fatal("restore helper bounds or version formatting changed")
	}
}
