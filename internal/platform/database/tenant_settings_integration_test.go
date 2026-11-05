//go:build integration

package database

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/audit"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/federation"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/jobs"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/clock"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/id"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/tenantsettings"
)

func tenantSettingsFixture(t *testing.T) (*housekeepingDatabase, *tenantsettings.Service, context.Context) {
	t.Helper()
	db := newHousekeepingDatabase(t)
	repo, err := NewPostgresRepository(db.api)
	if err != nil {
		t.Fatal(err)
	}
	store, err := NewPostgresTenantSettingsStore(repo)
	if err != nil {
		t.Fatal(err)
	}
	service, err := tenantsettings.New(store, 15*time.Minute, 5, clock.NewFake(time.Now().UTC()), id.Generator{})
	if err != nil {
		t.Fatal(err)
	}
	ctx := postgresFixtureContext(t, db, t.Context(), identity.Actor{TenantID: housekeepingTenantA, Subject: "settings-admin", Roles: []identity.Role{identity.RoleTenantAdmin}})
	return db, service, ctx
}
func saveTenantPolicy(t *testing.T, s *tenantsettings.Service, ctx context.Context, o tenantsettings.Overrides) tenantsettings.View {
	t.Helper()
	v, err := s.Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	v, err = s.Save(ctx, o, s.Tag(v), "Change owned test tenant operations")
	if err != nil {
		t.Fatal(err)
	}
	return v
}
func TestPostgresTenantSettingsPolicyIsolationAndAuditRollback(t *testing.T) {
	db, s, ctx := tenantSettingsFixture(t)
	initial, err := s.Get(ctx)
	if err != nil || initial.Revision != 0 {
		t.Fatal(initial, err)
	}
	var count int
	if err = db.operator.QueryRow(ctx, `SELECT count(*) FROM tenant_settings`).Scan(&count); err != nil || count != 0 {
		t.Fatal("read created settings", count, err)
	}
	off := false
	interval := tenantsettings.Duration(90500 * time.Millisecond)
	attempts := 2
	saved := saveTenantPolicy(t, s, ctx, tenantsettings.Overrides{AutomaticSourceSyncEnabled: &off, AutomaticSourceSyncInterval: &interval, NewJobMaxAttempts: &attempts})
	read, err := s.Get(ctx)
	if err != nil || s.Tag(read) != s.Tag(saved) || read.Effective.NewJobMaxAttempts != 2 || read.Effective.AutomaticSourceSyncEnabled {
		t.Fatal("retained policy/tag mismatch", read, err)
	}
	if _, err = s.Save(ctx, tenantsettings.Overrides{}, s.Tag(initial), "Stale first save"); catalog.ErrorCodeOf(err) != catalog.CodePreconditionFailed {
		t.Fatal(err)
	}
	other := postgresFixtureContext(t, db, t.Context(), identity.Actor{TenantID: housekeepingTenantB, Subject: "settings-other", Roles: []identity.Role{identity.RoleTenantAdmin}})
	v, err := s.Get(other)
	if err != nil || v.Revision != 0 || v.Effective.NewJobMaxAttempts != 5 {
		t.Fatal("cross-tenant policy", v, err)
	}
	reader := postgresFixtureContext(t, db, t.Context(), identity.Actor{TenantID: housekeepingTenantA, Subject: "settings-reader", Roles: []identity.Role{identity.RoleReader}})
	if _, err = s.Get(reader); catalog.ErrorCodeOf(err) != catalog.CodeForbidden {
		t.Fatal("reader received settings", err)
	}
	// Runtime SQL grants are exact; workers read policy but cannot edit it.
	if _, err = db.worker.Exec(ctx, `UPDATE tenant_settings SET new_job_max_attempts=3 WHERE tenant_id=$1`, housekeepingTenantA); err == nil {
		t.Fatal("worker edited policy")
	}
	tx, err := db.api.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer rollbackForCleanup(ctx, tx)()
	if _, err = tx.Exec(ctx, `SELECT set_config('app.tenant_id',$1,true)`, housekeepingTenantB); err != nil {
		t.Fatal(err)
	}
	if err = tx.QueryRow(ctx, `SELECT count(*) FROM tenant_settings`).Scan(&count); err != nil || count != 0 {
		t.Fatal("RLS exposed another tenant", count, err)
	}
	if err = tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	// A failing required audit must roll back the settings revision and values.
	_, err = db.operator.Exec(ctx, `CREATE FUNCTION reject_test_settings_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='tenant.settings.changed' THEN RAISE EXCEPTION 'owned audit failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER reject_test_settings_audit BEFORE INSERT ON audit_events FOR EACH ROW EXECUTE FUNCTION reject_test_settings_audit()`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Save(ctx, tenantsettings.Overrides{}, s.Tag(saved), "Reset must roll back"); err == nil {
		t.Fatal("failed audit accepted")
	}
	read, err = s.Get(ctx)
	if err != nil || s.Tag(read) != s.Tag(saved) {
		t.Fatal("audit failure changed policy", read, err)
	}
	if _, err = db.operator.Exec(ctx, `DROP TRIGGER reject_test_settings_audit ON audit_events; DROP FUNCTION reject_test_settings_audit()`); err != nil {
		t.Fatal(err)
	}
	reset := saveTenantPolicy(t, s, ctx, tenantsettings.Overrides{})
	if reset.Revision != 2 || reset.Effective.NewJobMaxAttempts != 5 || reset.Effective.AutomaticSourceSyncInterval != tenantsettings.Duration(15*time.Minute) {
		t.Fatal("reset changed inheritance", reset)
	}
	if err = db.operator.QueryRow(ctx, `SELECT count(*) FROM audit_events WHERE tenant_id=$1 AND action='tenant.settings.changed'`, housekeepingTenantA).Scan(&count); err != nil || count != 2 {
		t.Fatal("audit count", count, err)
	}
	for _, sql := range []string{`UPDATE tenant_settings SET revision=revision+2 WHERE tenant_id=$1`, `UPDATE tenant_settings SET revision=revision+1,new_job_max_attempts=21 WHERE tenant_id=$1`, `UPDATE tenant_settings SET revision=revision+1,automatic_source_sync_interval_ns=59999999999 WHERE tenant_id=$1`, `DELETE FROM tenant_settings WHERE tenant_id=$1`} {
		if _, err = db.operator.Exec(ctx, sql, housekeepingTenantA); err == nil {
			t.Fatalf("invalid transition accepted: %s", sql)
		}
	}
}
func TestPostgresTenantSettingsNewJobsAndSourcePause(t *testing.T) {
	db, s, ctx := tenantSettingsFixture(t)
	attempts := 2
	saveTenantPolicy(t, s, ctx, tenantsettings.Overrides{NewJobMaxAttempts: &attempts})
	now := time.Now().UTC()
	policy := "sha256:" + strings.Repeat("a", 64)
	sourceID := "00000000-0000-7000-8000-000000000241"
	source := federation.Source{ID: sourceID, TenantID: housekeepingTenantA, DisplayName: "settings source", Type: federation.SourceARDManifest, Endpoint: "https://source.example/manifest", Status: federation.SourceActive, RetrievalMode: federation.RetrievalIndexed, UpdateMode: federation.UpdateAutomatic, Scope: federation.Scope{Kinds: []catalog.Kind{catalog.KindAgent}}, PolicyID: "integration", PolicyRevision: 1, PolicyDigest: policy, Revision: 1, ProfileID: "integration", CreatedBy: "integration", CreatedAt: now.Add(-10 * time.Minute), UpdatedAt: now.Add(-10 * time.Minute)}
	if _, err := db.federation.CreateSource(ctx, source); err != nil {
		t.Fatal(err)
	}
	store, err := NewPostgresSourceMaintenanceStore(db.housekeeper.repository)
	if err != nil {
		t.Fatal(err)
	}
	candidates, err := store.ListDueAutomaticSources(ctx, now, time.Minute, policy, 1)
	if err != nil || len(candidates) != 1 {
		t.Fatal(candidates, err)
	}
	off := false
	saveTenantPolicy(t, s, ctx, tenantsettings.Overrides{NewJobMaxAttempts: &attempts, AutomaticSourceSyncEnabled: &off})
	if got, err := store.ListDueAutomaticSources(ctx, now, time.Minute, policy, 1); err != nil || len(got) != 0 {
		t.Fatal("paused selection", got, err)
	}
	payload := json.RawMessage(fmt.Sprintf(`{"sourceId":%q,"sourceRevision":1,"trigger":"automatic"}`, sourceID))
	job := jobs.Job{ID: "00000000-0000-7000-8000-000000000243", TenantID: housekeepingTenantA, Kind: "source.sync", Actor: audit.InitiatorSourceMaintainer, State: jobs.StateQueued, PayloadVersion: 2, Payload: payload, AttemptLimit: 5, DeduplicationKey: "source.sync:" + sourceID + ":1", NotBefore: now, CreatedAt: now, UpdatedAt: now}
	if _, _, err = store.AdmitAutomaticSync(ctx, candidates[0], job, time.Minute, policy, 32); catalog.ErrorCodeOf(err) != catalog.CodePreconditionFailed {
		t.Fatal("stale candidate admitted after pause", err)
	}
	saveTenantPolicy(t, s, ctx, tenantsettings.Overrides{NewJobMaxAttempts: &attempts})
	admitted, reused, err := store.AdmitAutomaticSync(ctx, candidates[0], job, time.Minute, policy, 32)
	if err != nil || reused || admitted.AttemptLimit != 2 {
		t.Fatal("automatic limit", admitted, reused, err)
	}
	// Manual producers use omitted intent; exact internal budgets remain authoritative.
	service, err := jobs.NewService(db.apiJobs, clock.NewFake(now), id.Generator{}, jobs.WithMaxAttempts(5))
	if err != nil {
		t.Fatal(err)
	}
	in := jobs.EnqueueInput{Kind: "source.sync", PayloadVersion: 2, Payload: json.RawMessage(fmt.Sprintf(`{"sourceId":%q,"sourceRevision":1,"trigger":"manual"}`, sourceID)), DeduplicationKey: job.DeduplicationKey}
	existing, reused, err := service.Enqueue(ctx, in)
	if err != nil || !reused || existing.ID != admitted.ID || existing.AttemptLimit != 2 {
		t.Fatal("dedup changed budget", existing, reused, err)
	}
	attempts = 7
	saveTenantPolicy(t, s, ctx, tenantsettings.Overrides{NewJobMaxAttempts: &attempts, AutomaticSourceSyncEnabled: &off})
	// A newly admitted independent Job adopts the policy; an explicit 5 does not.
	for _, tc := range []struct {
		limit, want int
		key         string
	}{{0, 7, "new-default"}, {5, 5, "explicit-old-default"}} {
		in.DeduplicationKey = tc.key
		in.AttemptLimit = tc.limit
		got, _, err := service.Enqueue(ctx, in)
		if err != nil || got.AttemptLimit != tc.want || got.DefaultAttemptLimit {
			t.Fatal(tc, got, err)
		}
	}
	retained, err := db.apiJobs.Get(ctx, housekeepingTenantA, admitted.ID)
	if err != nil || retained.AttemptLimit != 2 {
		t.Fatal("policy rewrote retained Job", retained, err)
	}
}

func TestPostgresTenantSettingsWaitedAdmissionSeesCommittedPolicy(t *testing.T) {
	db, _, ctx := tenantSettingsFixture(t)
	held, err := db.operator.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer rollbackForCleanup(ctx, held)()
	if err = lockTenantSettings(ctx, held, housekeepingTenantA, false); err != nil {
		t.Fatal(err)
	}
	var pid int
	if err = held.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&pid); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	job := jobs.Job{ID: "00000000-0000-7000-8000-000000000281", TenantID: housekeepingTenantA, Kind: "test-policy", Actor: "test", State: jobs.StateQueued, PayloadVersion: 1, Payload: json.RawMessage(`{}`), AttemptLimit: 5, DefaultAttemptLimit: true, DeduplicationKey: "waited-policy", CreatedAt: now, UpdatedAt: now, NotBefore: now}
	waitCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	result := make(chan error, 1)
	go func() {
		got, _, err := db.workerJobs.Enqueue(waitCtx, job)
		if err == nil && got.AttemptLimit != 3 {
			err = fmt.Errorf("waited admission used %d attempts", got.AttemptLimit)
		}
		result <- err
	}()
	deadline := time.Now().Add(5 * time.Second)
	for {
		var waiting bool
		if err = db.operator.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE $1=ANY(pg_blocking_pids(pid)))`, pid).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("admission did not wait on settings fence")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if _, err = held.Exec(ctx, `INSERT INTO tenant_settings(tenant_id,revision,new_job_max_attempts,updated_at) VALUES($1,1,3,clock_timestamp())`, housekeepingTenantA); err != nil {
		t.Fatal(err)
	}
	if err = held.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err = <-result; err != nil {
		t.Fatal(err)
	}
	// Fresh connections remain usable after lock cleanup.
	if err = db.worker.Ping(ctx); err != nil {
		t.Fatal(err)
	}
}

func waitForSettingsWaiter(t *testing.T, db *housekeepingDatabase, ctx context.Context, pid int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		var waiting bool
		if err := db.operator.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE $1=ANY(pg_blocking_pids(pid)))`, pid).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("settings operation did not wait on its tenant fence")
		}
		time.Sleep(5 * time.Millisecond)
	}
}
func TestPostgresTenantSettingsConcurrentFirstSave(t *testing.T) {
	db, s, ctx := tenantSettingsFixture(t)
	initial, err := s.Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	replies := make(chan error, 2)
	for range 2 {
		go func() {
			_, err := s.Save(ctx, tenantsettings.Overrides{}, s.Tag(initial), "Concurrent owned first save")
			replies <- err
		}()
	}
	success, stale := 0, 0
	for range 2 {
		err := <-replies
		switch {
		case err == nil:
			success++
		case catalog.ErrorCodeOf(err) == catalog.CodePreconditionFailed:
			stale++
		default:
			t.Fatal(err)
		}
	}
	current, err := s.Get(ctx)
	if err != nil || current.Revision != 1 || success != 1 || stale != 1 {
		t.Fatal(current, success, stale, err)
	}
	var count int
	if err = db.operator.QueryRow(ctx, `SELECT count(*) FROM audit_events WHERE tenant_id=$1 AND action='tenant.settings.changed'`, housekeepingTenantA).Scan(&count); err != nil || count != 1 {
		t.Fatal("race duplicated audit", count, err)
	}
}
func TestPostgresTenantSettingsSaveWaitsForAdmissionAndCancels(t *testing.T) {
	for _, cancelSave := range []bool{false, true} {
		t.Run(fmt.Sprint(cancelSave), func(t *testing.T) {
			db, s, ctx := tenantSettingsFixture(t)
			initial, err := s.Get(ctx)
			if err != nil {
				t.Fatal(err)
			}
			held, err := db.worker.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer rollbackForCleanup(ctx, held)()
			if err = lockTenantSettings(ctx, held, housekeepingTenantA, true); err != nil {
				t.Fatal(err)
			}
			var pid int
			if err = held.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&pid); err != nil {
				t.Fatal(err)
			}
			waitCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
			defer cancel()
			replies := make(chan error, 1)
			go func() {
				off := false
				_, err := s.Save(waitCtx, tenantsettings.Overrides{AutomaticSourceSyncEnabled: &off}, s.Tag(initial), "Pause after owned admission")
				replies <- err
			}()
			waitForSettingsWaiter(t, db, ctx, pid)
			if cancelSave {
				cancel()
				if err = <-replies; !errors.Is(err, context.Canceled) {
					t.Fatal("cancelled save", err)
				}
			}
			if err = held.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			if !cancelSave {
				if err = <-replies; err != nil {
					t.Fatal(err)
				}
			}
			current, err := s.Get(ctx)
			want := int64(1)
			if cancelSave {
				want = 0
			}
			if err != nil || current.Revision != want {
				t.Fatal("waited/cancelled save state", current, err)
			}
		})
	}
}

// Paused tenants must be excluded before LIMIT, rather than occupy the worker's
// entire batch and starve another tenant. Plan timings are evidence, not an SLA.
func TestPostgresTenantSettingsSelectionFairnessAndPlans(t *testing.T) {
	db, s, ctx := tenantSettingsFixture(t)
	off := false
	saveTenantPolicy(t, s, ctx, tenantsettings.Overrides{AutomaticSourceSyncEnabled: &off})
	var now time.Time
	if err := db.operator.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&now); err != nil {
		t.Fatal(err)
	}
	policy := "sha256:" + strings.Repeat("a", 64)
	other := postgresFixtureContext(t, db, t.Context(), identity.Actor{TenantID: housekeepingTenantB, Subject: "settings-fairness", Roles: []identity.Role{identity.RoleTenantAdmin}})
	var expected string
	for i := 0; i < 36; i++ {
		tenant, sourceCtx := housekeepingTenantA, ctx
		recordedAt := now.Add(-time.Hour)
		if i == 35 {
			tenant, sourceCtx = housekeepingTenantB, other
			// Every paused candidate is older even after its bounded jitter.
			// A LIMIT-before-filter regression must exclude this eligible source.
			recordedAt = now.Add(-30 * time.Minute)
		}
		identifier := fmt.Sprintf("00000000-0000-7000-8000-%012d", 3000+i)
		source := federation.Source{ID: identifier, TenantID: tenant, DisplayName: "Owned fairness fixture", Type: federation.SourceARDManifest, Endpoint: "https://source.example/manifest/" + identifier, Status: federation.SourceActive, RetrievalMode: federation.RetrievalIndexed, UpdateMode: federation.UpdateAutomatic, Scope: federation.Scope{Kinds: []catalog.Kind{catalog.KindAgent}}, PolicyID: "integration", PolicyRevision: 1, PolicyDigest: policy, Revision: 1, ProfileID: "integration", CreatedBy: "integration", CreatedAt: recordedAt, UpdatedAt: recordedAt}
		if _, err := db.federation.CreateSource(sourceCtx, source); err != nil {
			t.Fatal(err)
		}
		if i == 35 {
			expected = identifier
		}
	}
	store, err := NewPostgresSourceMaintenanceStore(db.housekeeper.repository)
	if err != nil {
		t.Fatal(err)
	}
	got, err := store.ListDueAutomaticSources(ctx, now, time.Minute, policy, 32)
	if err != nil || len(got) != 1 || got[0].TenantID != housekeepingTenantB || got[0].SourceID != expected {
		t.Fatal("paused tenant filled bounded candidate selection", got, err)
	}
	var raw json.RawMessage
	if err := db.worker.QueryRow(ctx, `EXPLAIN(ANALYZE,BUFFERS,FORMAT JSON) `+automaticSourceDueCTE+`SELECT tenant_id,source_id,revision,due_at FROM due WHERE due_at<=clock_timestamp() ORDER BY due_at,tenant_id,source_id LIMIT $3`, time.Minute.String(), policy, 32).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var plans []struct {
		Plan struct {
			NodeType   string  `json:"Node Type"`
			ActualRows float64 `json:"Actual Rows"`
		} `json:"Plan"`
		PlanningTime  float64 `json:"Planning Time"`
		ExecutionTime float64 `json:"Execution Time"`
	}
	if err := json.Unmarshal(raw, &plans); err != nil || len(plans) != 1 || plans[0].Plan.NodeType != "Limit" || plans[0].Plan.ActualRows != 1 {
		t.Fatal("unbounded or incorrect due-selection plan", err)
	}
	started := time.Now()
	for range 16 {
		record, err := withTenantRead(ctx, db.api, housekeepingTenantA, func(tx pgx.Tx) (tenantsettings.Record, error) {
			return readTenantSettings(ctx, tx, housekeepingTenantA)
		})
		if err != nil || record.Revision != 1 || record.Overrides.AutomaticSourceSyncEnabled == nil || *record.Overrides.AutomaticSourceSyncEnabled {
			t.Fatal("exact singleton policy read", record, err)
		}
	}
	t.Logf("tenant-settings selection: paused=35 eligible=1 limit=32 actual_rows=1 planning_ms=%.3f execution_ms=%.3f singleton_reads=16 singleton_duration=%s", plans[0].PlanningTime, plans[0].ExecutionTime, time.Since(started))
}
