package database

import (
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/audit"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/jobs"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/tenantsettings"
)

func TestTenantSettingsRepositoryReadAndDefaultFailures(t *testing.T) {
	if _, err := NewPostgresTenantSettingsStore(nil); err == nil {
		t.Fatal("nil repository")
	}
	for _, mode := range []string{"absent", "saved", "failure"} {
		t.Run(mode, func(t *testing.T) {
			f := newSemanticRepositoryFixture(t, true)
			store, _ := NewPostgresTenantSettingsStore(f.store.repository)
			off := false
			interval := int64(90500 * time.Millisecond)
			attempts := 3
			now := time.Date(2026, 10, 13, 14, 0, 0, 0, time.FixedZone("test", 3600))
			failure := errors.New("read failure")
			row := scriptedValueRow([]any{int64(2), &off, &interval, &attempts, &now})
			if mode == "absent" {
				row = semanticFaultRow(pgx.ErrNoRows)
			}
			if mode == "failure" {
				row = semanticFaultRow(failure)
			}
			f.tx.queryRows = []pgx.Row{row}
			got, err := store.Get(f.context(t), f.g.TenantID)
			switch {
			case mode == "failure":
				if !errors.Is(err, failure) || catalog.ErrorCodeOf(err) != catalog.CodeDependencyUnavailable {
					t.Fatal(got, err)
				}
			case err != nil || got.TenantID != f.g.TenantID:
				t.Fatal(got, err)
			case mode == "saved" && (got.Revision != 2 || *got.Overrides.NewJobMaxAttempts != 3 || time.Duration(*got.Overrides.AutomaticSourceSyncInterval) != 90500*time.Millisecond || *got.Overrides.AutomaticSourceSyncEnabled || got.UpdatedAt.Location() != time.UTC):
				t.Fatal(got)
			case mode == "absent" && got.Revision != 0:
				t.Fatal(got)
			}
			f.checkRead(t, err, pgx.RepeatableRead)
			if !reflect.DeepEqual(f.tx.queryRowArguments[0], []any{f.g.TenantID}) {
				t.Fatal("unbounded/foreign read")
			}
		})
	}
	for _, mode := range []string{"inherit", "override", "failure"} {
		t.Run("attempts "+mode, func(t *testing.T) {
			f := newSemanticRepositoryFixture(t, false)
			attempts := 2
			f.tx.queryRows = []pgx.Row{semanticFaultRow(pgx.ErrNoRows)}
			if mode == "override" {
				f.tx.queryRows = []pgx.Row{scriptedValueRow([]any{int64(1), (*bool)(nil), (*int64)(nil), &attempts, (*time.Time)(nil)})}
			}
			if mode == "failure" {
				f.tx.queryRows = []pgx.Row{semanticFaultRow(errors.New("unavailable"))}
			}
			got, err := resolveNewJobAttempts(t.Context(), f.tx, f.g.TenantID, 7)
			if mode == "failure" {
				if err == nil || got != 0 {
					t.Fatal("failure treated as inheritance")
				}
			} else if err != nil || mode == "inherit" && got != 7 || mode == "override" && got != 2 {
				t.Fatal(got, err)
			}
		})
	}
}
func TestTenantSettingsRepositorySaveLocksAndBindsEvidence(t *testing.T) {
	for _, mode := range []string{"create", "update", "lock failure", "read failure", "stale", "write failure", "audit failure"} {
		t.Run(mode, func(t *testing.T) {
			f := newSemanticRepositoryFixture(t, false)
			store, _ := NewPostgresTenantSettingsStore(f.store.repository)
			old := tenantsettings.Record{TenantID: f.g.TenantID}
			now := time.Date(2026, 10, 13, 14, 0, 0, 0, time.UTC)
			if mode == "update" {
				old.Revision = 1
				old.UpdatedAt = &now
			}
			next := tenantsettings.Record{TenantID: f.g.TenantID, Revision: old.Revision + 1, UpdatedAt: &now}
			facts := map[string]any{"beforeRevision": old.Revision, "afterRevision": next.Revision, "beforeEnabledSet": false, "afterEnabledSet": false, "beforeIntervalSet": false, "afterIntervalSet": false, "beforeAttemptsSet": false, "afterAttemptsSet": false}
			e, err := audit.NewRecord("00000000-0000-7000-8000-000000000006", f.g.TenantID, audit.Principal{ID: "00000000-0000-7000-8000-000000000002", Type: audit.PrincipalCatalogMembership, ActorType: "human", RealmID: "00000000-0000-7000-8000-000000000003"}, tenantsettings.Action, "tenant_settings", f.g.TenantID, "Change tenant policy", audit.Execution{}, facts, now)
			if err != nil {
				t.Fatal(err)
			}
			defaults := tenantsettings.Policy{AutomaticSourceSyncEnabled: true, AutomaticSourceSyncInterval: tenantsettings.Duration(time.Minute), NewJobMaxAttempts: 5}
			tag := tenantsettings.ETag(old, defaults)
			failure := errors.New("settings transaction failed")
			f.tx.queryRows = []pgx.Row{semanticFaultRow(pgx.ErrNoRows)}
			if mode == "update" {
				f.tx.queryRows = []pgx.Row{scriptedValueRow([]any{int64(1), (*bool)(nil), (*int64)(nil), (*int)(nil), &now})}
			}
			switch mode {
			case "lock failure":
				f.tx.execErrors = []error{failure}
			case "read failure":
				f.tx.queryRows = []pgx.Row{semanticFaultRow(failure)}
			case "stale":
				tag = "stale"
			case "write failure":
				f.tx.execErrors = []error{nil, failure}
			case "audit failure":
				f.tx.execErrors = []error{nil, nil, failure}
			}
			got, err := store.Save(f.context(t), next, defaults, tag, e)
			if mode == "create" || mode == "update" {
				if err != nil || !reflect.DeepEqual(got, next) || len(f.tx.statements) != 3 {
					t.Fatal(got, err)
				}
			} else if err == nil {
				t.Fatal("failure accepted")
			}
			f.checkWrite(t, err)
			if !reflect.DeepEqual(f.tx.arguments[0], []any{catalogStateLockKey("tenant-settings", f.g.TenantID)}) {
				t.Fatal("missing exact-tenant lock")
			}
		})
	}
}
func TestRestoreTenantSettingsBoundsAndCleanup(t *testing.T) {
	for _, mode := range []string{"valid", "invalid", "more", "query failure", "scan failure", "iterate failure"} {
		t.Run(mode, func(t *testing.T) {
			rows := &identityScriptedRows{values: [][]any{{paginationTestTenantID, mode == "invalid"}}}
			tx := &identityScriptedTx{queryResults: []identityQueryResult{{rows: rows}}}
			failure := errors.New("restore failed")
			if mode == "more" {
				rows.values = append(rows.values, []any{paginationTestTenantID, false})
			}
			if mode == "query failure" {
				tx.queryResults[0].err = failure
			}
			if mode == "scan failure" {
				rows.values = [][]any{{"wrong"}}
			}
			if mode == "iterate failure" {
				rows.err = failure
			}
			got, more, err := scanRestoreTenantSettings(t.Context(), tx, paginationTestTenantID, "previous", 1)
			if mode == "query failure" || mode == "scan failure" || mode == "iterate failure" {
				if err == nil {
					t.Fatal("failure hidden")
				}
			} else if err != nil || len(got) != 1 || more != (mode == "more") || (got[0].finding != nil) != (mode == "invalid") {
				t.Fatal(got, more, err)
			}
			if mode != "query failure" && !rows.closed {
				t.Fatal("rows not closed")
			}
			if !reflect.DeepEqual(tx.queryArguments[0], []any{paginationTestTenantID, "previous", 2}) {
				t.Fatal("restore selection unbounded")
			}
		})
	}
}

func TestDefaultJobAttemptAdmissionPreservesExplicitLimits(t *testing.T) {
	for _, useDefault := range []bool{false, true} {
		t.Run(fmt.Sprint(useDefault), func(t *testing.T) {
			store, tx, ctx, job := jobRepositoryFixture(t)
			job.DefaultAttemptLimit = useDefault
			attempts := 2
			now := job.CreatedAt
			tx.queryRows = []pgx.Row{scriptedValueRow([]any{1}), scriptedValueRow([]any{job.ID})}
			if useDefault {
				tx.queryRows = append([]pgx.Row{scriptedValueRow([]any{int64(1), (*bool)(nil), (*int64)(nil), &attempts, &now})}, tx.queryRows...)
			}
			got, reused, err := store.Enqueue(ctx, job, jobRepositoryEvidence(t, job, false))
			want := 5
			if useDefault {
				want = 2
			}
			if err != nil || reused || got.AttemptLimit != want || got.DefaultAttemptLimit {
				t.Fatal(got, reused, err)
			}
			if useDefault && !reflect.DeepEqual(tx.arguments[0], []any{catalogStateLockKey("tenant-settings", job.TenantID)}) {
				t.Fatal("missing settings fence")
			}
			// Check the retained INSERT and the independent audit wire projection,
			// rather than only the returned in-memory Job.
			insert := tx.queryRowArguments[len(tx.queryRowArguments)-1]
			if insert[12] != want {
				t.Fatal("stored attempt budget differs from admitted Job", insert[12])
			}
			auditArguments := tx.arguments[len(tx.arguments)-1]
			state := []byte(fmt.Sprintf(`{"id":%q,"kind":"source.sync","state":"queued","progress":0,"attempt":0,"attemptLimit":%d,"cancellationRequested":false}`, job.ID, want))
			if auditArguments[10] != audit.DigestBytes(state) {
				t.Fatal("enqueue audit is not bound to the captured attempt budget")
			}
		})
	}
}

func TestDefaultJobAttemptAdmissionPolicyFailuresStopBeforeEnqueue(t *testing.T) {
	for _, stage := range []string{"settings lock", "settings read"} {
		t.Run(stage, func(t *testing.T) {
			store, tx, ctx, job := jobRepositoryFixture(t)
			job.DefaultAttemptLimit = true
			failure := errors.New("owned policy dependency failure")
			if stage == "settings lock" {
				tx.execErrors = []error{failure}
			} else {
				tx.queryRows = []pgx.Row{semanticFaultRow(failure)}
			}
			got, reused, err := store.Enqueue(ctx, job, jobRepositoryEvidence(t, job, false))
			if !errors.Is(err, failure) || catalog.ErrorCodeOf(err) != catalog.CodeDependencyUnavailable || reused || !reflect.DeepEqual(got, jobs.Job{}) {
				t.Fatal("policy failure admitted fallback work", got, reused, err)
			}
			if len(tx.statements) != 1 || tx.commits != 0 || tx.rollbacks != 1 {
				t.Fatal("policy failure reached audit or committed", tx.statements, tx.commits, tx.rollbacks)
			}
			wantReads := 0
			if stage == "settings read" {
				wantReads = 1
			}
			if len(tx.queryRowStatements) != wantReads {
				t.Fatal("policy failure reached source lock or Job insert", tx.queryRowStatements)
			}
		})
	}
}
