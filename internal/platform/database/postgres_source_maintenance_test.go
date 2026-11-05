// Automatic maintenance units exercise candidate reads, admission decisions and arguments.
// PostgreSQL tests own eligibility SQL, capacity serialization and deduplication.
package database

import (
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/audit"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/federation"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/jobs"
)

const admissionPolicy = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func TestListDueAutomaticSourcesMapsCandidatesAndClosesRows(t *testing.T) {
	t.Parallel()
	const (
		tenantA  = "00000000-0000-7000-8000-000000000011"
		tenantB  = "00000000-0000-7000-8000-000000000022"
		sourceA  = "00000000-0000-7000-8000-000000000033"
		sourceB  = "00000000-0000-7000-8000-000000000044"
		interval = 9*time.Minute + 37*time.Second
		limit    = 3
	)
	dueA := time.Date(2026, time.September, 21, 10, 11, 12, 123000, time.UTC)
	dueB := dueA.Add(2 * time.Hour)
	for _, mode := range []string{"empty", "multiple", "query failure", "first scan failure", "later scan failure", "empty iteration failure", "partial iteration failure"} {
		t.Run(mode, func(t *testing.T) {
			rows := &searchHydrationRows{identityScriptedRows: identityScriptedRows{values: [][]any{
				{tenantA, sourceA, int64(3), dueA},
				{tenantB, sourceB, int64(7), dueB},
			}}}
			want := []federation.MaintenanceCandidate{
				{TenantID: tenantA, SourceID: sourceA, SourceRevision: 3, DueAt: dueA},
				{TenantID: tenantB, SourceID: sourceB, SourceRevision: 7, DueAt: dueB},
			}
			failure := errors.New("automatic candidate read failed")
			query := &identityScriptedTx{fakeCommandTx: &fakeCommandTx{}, queryResults: []identityQueryResult{{rows: rows}}}
			wantOperation := ""
			wantIndex, failScanAt := 2, 0
			wantClosed, wantNil := true, false
			switch mode {
			case "empty":
				rows.values, want, wantIndex = nil, nil, 0
			case "query failure":
				query.queryResults[0] = identityQueryResult{err: failure}
				want, wantOperation, wantIndex, wantClosed, wantNil = nil, "list due automatic federation sources", 0, false, true
			case "first scan failure", "later scan failure":
				want, wantOperation, wantNil = nil, "scan due automatic federation source", true
				failScanAt, wantIndex = 1, 1
				if mode == "later scan failure" {
					failScanAt, wantIndex = 2, 2
					// A later row must remain unread after the failed scan.
					rows.values = append(rows.values, []any{tenantA, "00000000-0000-7000-8000-000000000055", int64(8), dueB.Add(time.Hour)})
				}
			case "empty iteration failure", "partial iteration failure":
				rows.err, wantOperation = failure, "iterate due automatic federation sources"
				if mode == "empty iteration failure" {
					rows.values, want, wantIndex = nil, nil, 0
				}
			}
			rows.scan = func(destinations ...any) error {
				if rows.index == failScanAt {
					tenant, ok := rows.values[rows.index-1][0].(string)
					if !ok {
						t.Fatal("scripted candidate tenant is not a string")
					}
					setScanDestination(t, destinations, 0, tenant)
					return failure
				}
				return rows.identityScriptedRows.Scan(destinations...)
			}
			got, err := listDueAutomaticSources(t.Context(), query, interval, admissionPolicy, limit)
			if wantOperation == "" {
				if err != nil {
					t.Fatalf("candidate read failed: %v", err)
				}
			} else {
				var domain *catalog.DomainError
				if !errors.Is(err, failure) || !errors.As(err, &domain) || domain.Code != catalog.CodeDependencyUnavailable || domain.Message != wantOperation {
					t.Fatalf("candidate read error = %v; want original cause and %q dependency error", err, wantOperation)
				}
			}
			if len(got) != len(want) || len(want) > 0 && !reflect.DeepEqual(got, want) || wantNil && got != nil {
				t.Fatalf("candidates = %#v; want %#v (nil required=%t)", got, want, wantNil)
			}
			if rows.closed != wantClosed || rows.index != wantIndex {
				t.Fatalf("row cleanup/progress = closed %t, index %d; want %t, %d", rows.closed, rows.index, wantClosed, wantIndex)
			}
			if len(query.queryStatements) != 1 || len(query.queryResults) != 0 || !reflect.DeepEqual(query.queryArguments, [][]any{{interval.String(), admissionPolicy, limit}}) {
				t.Fatalf("candidate query count = %d; arguments = %#v; unread results = %d", len(query.queryStatements), query.queryArguments, len(query.queryResults))
			}
			if len(query.queryRowStatements) != 0 || len(query.statements) != 0 || query.commits != 0 || query.rollbacks != 0 {
				t.Fatal("candidate read performed unrelated work or took transaction ownership")
			}
		})
	}
}

type automaticAdmissionFixture struct {
	tx          *identityScriptedTx
	candidate   federation.MaintenanceCandidate
	job         jobs.Job
	databaseNow time.Time
}

func newAutomaticAdmissionFixture() automaticAdmissionFixture {
	now := time.Date(2026, time.September, 20, 12, 0, 0, 123000, time.UTC)
	callerNow := now.Add(-5 * time.Minute)
	candidate := federation.MaintenanceCandidate{TenantID: paginationTestTenantID, SourceID: jobSourceID, SourceRevision: 3, DueAt: now.Add(-time.Hour)}
	job := jobs.Job{
		ID: "00000000-0000-7000-8000-000000000005", TenantID: candidate.TenantID,
		Kind: "source.sync", Actor: audit.InitiatorSourceMaintainer, PayloadVersion: 2,
		Payload: json.RawMessage(`{"sourceId":"` + jobSourceID + `","sourceRevision":3,"trigger":"automatic"}`),
		State:   jobs.StateQueued, AttemptLimit: 5, DeduplicationKey: "source.sync:" + jobSourceID + ":3",
		NotBefore: callerNow, CreatedAt: callerNow, UpdatedAt: callerNow,
	}
	tx := &identityScriptedTx{fakeCommandTx: &fakeCommandTx{}, queryRows: []pgx.Row{
		scriptedValueRow([]any{1}),
		scriptedValueRow([]any{now.Add(-time.Minute)}),
		scriptedValueRow([]any{2}),
		scriptedValueRow([]any{job.ID}),
	}}
	return automaticAdmissionFixture{tx: tx, candidate: candidate, job: job, databaseNow: now}
}

func TestAdmitAutomaticSyncTxResultsAndBindings(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name     string
		live     int
		existing bool
	}{
		{name: "new job with no live work", live: 0},
		{name: "new job just below capacity", live: 2},
		{name: "reuse retained waiting job", live: 1, existing: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			f := newAutomaticAdmissionFixture()
			inputBefore := f.job
			inputBefore.Payload = slices.Clone(f.job.Payload)
			f.tx.queryRows[2] = scriptedValueRow([]any{tt.live})
			want := f.job
			want.CreatedAt, want.UpdatedAt, want.NotBefore = f.databaseNow, f.databaseNow, f.databaseNow
			if tt.existing {
				want.ID = "00000000-0000-7000-8000-000000000006"
				want.State, want.Attempt = jobs.StateWaiting, 1
				want.CreatedAt = f.databaseNow.Add(-time.Hour)
				want.UpdatedAt = f.databaseNow.Add(-time.Minute)
				want.NotBefore = f.databaseNow.Add(30 * time.Second)
				f.tx.queryRows[3] = scannerFunc(func(...any) error { return pgx.ErrNoRows })
				f.tx.queryRows = append(f.tx.queryRows, scriptedValueRow(jobRepositoryValues(want)))
			}
			got, existing, err := admitAutomaticSyncTx(t.Context(), f.tx, f.candidate, f.job, time.Minute, admissionPolicy, 3, f.databaseNow)
			if err != nil || existing != tt.existing || !reflect.DeepEqual(got, want) {
				t.Fatalf("admission = %#v, existing=%t, %v; want %#v, existing=%t", got, existing, err, want, tt.existing)
			}
			if !reflect.DeepEqual(f.job, inputBefore) {
				t.Fatal("admission changed the caller's job or payload")
			}
			wantQueries := [][]any{
				{f.candidate.TenantID, f.candidate.SourceID, f.candidate.SourceRevision},
				{time.Minute.String(), admissionPolicy, f.candidate.TenantID, f.candidate.SourceID, f.candidate.SourceRevision},
				nil, // The live-job count is global, with no tenant parameter.
				{f.job.ID, f.job.TenantID, f.job.Kind, f.job.Actor, f.job.ParentJobID, f.job.PayloadVersion, f.job.Payload,
					f.job.State, f.job.Priority, f.job.Progress, f.databaseNow, f.job.Attempt, f.job.AttemptLimit,
					f.job.DeduplicationKey, f.databaseNow, f.databaseNow},
			}
			if tt.existing {
				wantQueries = append(wantQueries, []any{f.job.TenantID, f.job.Kind, f.job.DeduplicationKey})
			}
			if !reflect.DeepEqual(f.tx.queryRowArguments, wantQueries) || len(f.tx.queryRows) != 0 || len(f.tx.queryStatements) != 0 {
				t.Fatalf("admission query bindings = %#v; want %#v", f.tx.queryRowArguments, wantQueries)
			}
			wantMaintenance := [][]any{{f.candidate.TenantID, f.candidate.SourceID, f.candidate.SourceRevision, f.databaseNow}}
			if len(f.tx.statements) != 1 || !strings.Contains(f.tx.statements[0], "INSERT INTO source_maintenance_state") || !reflect.DeepEqual(f.tx.arguments, wantMaintenance) {
				t.Fatalf("admission maintenance writes = %#v, arguments=%#v", f.tx.statements, f.tx.arguments)
			}
			if f.tx.commits != 0 || f.tx.rollbacks != 0 {
				t.Fatal("admission took transaction ownership")
			}
		})
	}
}

func TestAdmitAutomaticSyncTxStopsOnFailure(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{
		"missing or stale source", "source read", "no longer due or eligible", "due recheck", "live count",
		"at capacity", "above capacity", "enqueue", "deduplicated job read", "new admission marker", "reused admission marker",
	} {
		t.Run(mode, func(t *testing.T) {
			f := newAutomaticAdmissionFixture()
			inputBefore := f.job
			inputBefore.Payload = slices.Clone(f.job.Payload)
			failure := errors.New("injected automatic admission failure")
			failedRow := scannerFunc(func(...any) error { return failure })
			absentRow := scannerFunc(func(...any) error { return pgx.ErrNoRows })
			wantCode := catalog.CodeDependencyUnavailable
			wantReads, wantWrites := 0, 0
			switch mode {
			case "missing or stale source":
				f.tx.queryRows[0], wantReads, wantCode = absentRow, 1, catalog.CodeNotFound
			case "source read":
				f.tx.queryRows[0], wantReads = failedRow, 1
			case "no longer due or eligible":
				f.tx.queryRows[1], wantReads, wantCode = absentRow, 2, catalog.CodePreconditionFailed
			case "due recheck":
				f.tx.queryRows[1], wantReads = failedRow, 2
			case "live count":
				f.tx.queryRows[2], wantReads = failedRow, 3
			case "at capacity", "above capacity":
				live := 3
				if mode == "above capacity" {
					live++
				}
				f.tx.queryRows[2], wantReads, wantCode = scriptedValueRow([]any{live}), 3, catalog.CodeConflict
			case "enqueue":
				f.tx.queryRows[3], wantReads = failedRow, 4
			case "deduplicated job read":
				f.tx.queryRows[3] = absentRow
				f.tx.queryRows = append(f.tx.queryRows, failedRow)
				wantReads = 5
			case "new admission marker", "reused admission marker":
				f.tx.execErrors = []error{failure}
				wantReads, wantWrites = 4, 1
				if mode == "reused admission marker" {
					retained := f.job
					retained.ID = "00000000-0000-7000-8000-000000000006"
					f.tx.queryRows[3] = absentRow
					f.tx.queryRows = append(f.tx.queryRows, scriptedValueRow(jobRepositoryValues(retained)))
					wantReads = 5
				}
			}
			got, existing, err := admitAutomaticSyncTx(t.Context(), f.tx, f.candidate, f.job, time.Minute, admissionPolicy, 3, f.databaseNow)
			if catalog.ErrorCodeOf(err) != wantCode || existing || !reflect.DeepEqual(got, jobs.Job{}) {
				t.Fatalf("failed admission = %#v, existing=%t, %v; want no result and %s", got, existing, err, wantCode)
			}
			if wantCode == catalog.CodeDependencyUnavailable && !errors.Is(err, failure) {
				t.Fatalf("admission lost the injected failure: %v", err)
			}
			if len(f.tx.queryRowStatements) != wantReads || len(f.tx.statements) != wantWrites || len(f.tx.queryStatements) != 0 {
				t.Fatalf("failure skipped or continued work: reads=%d writes=%d; want %d, %d", len(f.tx.queryRowStatements), len(f.tx.statements), wantReads, wantWrites)
			}
			if !reflect.DeepEqual(f.job, inputBefore) || f.tx.commits != 0 || f.tx.rollbacks != 0 {
				t.Fatal("failed admission changed the caller's job or took transaction ownership")
			}
		})
	}
}
