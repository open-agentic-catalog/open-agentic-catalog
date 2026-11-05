//go:build integration

package database

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/idempotency"
)

// These test-only relations qualify PostgreSQL mechanics for ADR-0022. They
// are not production migrations, privilege manifests or integrated enforcement.
func authorizationTrial(t *testing.T) *housekeepingDatabase {
	t.Helper()
	db := newHousekeepingDatabase(t)
	statements := []string{
		`CREATE TABLE rbac_trial_state (tenant_id uuid PRIMARY KEY REFERENCES tenants(id), revision bigint NOT NULL CHECK(revision>0))`,
		`CREATE TABLE rbac_trial_members (tenant_id uuid NOT NULL, realm text NOT NULL, member text NOT NULL, active boolean NOT NULL, PRIMARY KEY(tenant_id,realm,member))`,
		`CREATE TABLE rbac_trial_permissions (tenant_id uuid NOT NULL, role text NOT NULL, permission text NOT NULL, PRIMARY KEY(tenant_id,role,permission))`,
		`CREATE TABLE rbac_trial_groups (tenant_id uuid NOT NULL, realm text NOT NULL, id text NOT NULL, active boolean NOT NULL, PRIMARY KEY(tenant_id,realm,id))`,
		`CREATE TABLE rbac_trial_group_members (tenant_id uuid NOT NULL, realm text NOT NULL, group_id text NOT NULL, member text NOT NULL, active boolean NOT NULL, PRIMARY KEY(tenant_id,realm,group_id,member), FOREIGN KEY(tenant_id,realm,group_id) REFERENCES rbac_trial_groups(tenant_id,realm,id), FOREIGN KEY(tenant_id,realm,member) REFERENCES rbac_trial_members(tenant_id,realm,member))`,
		`CREATE TABLE rbac_trial_assignments (tenant_id uuid NOT NULL, realm text NOT NULL, role text NOT NULL, member text, group_id text, namespace text, active boolean NOT NULL, CHECK ((member IS NULL)<>(group_id IS NULL)), FOREIGN KEY(tenant_id,realm,member) REFERENCES rbac_trial_members(tenant_id,realm,member), FOREIGN KEY(tenant_id,realm,group_id) REFERENCES rbac_trial_groups(tenant_id,realm,id))`,
		`CREATE TABLE rbac_trial_effects (tenant_id uuid PRIMARY KEY, value integer NOT NULL DEFAULT 0)`,
	}
	for _, sql := range statements {
		if _, err := db.operator.Exec(t.Context(), sql); err != nil {
			t.Fatal(err)
		}
	}
	var apiRole string
	if err := db.api.QueryRow(t.Context(), `SELECT current_user`).Scan(&apiRole); err != nil {
		t.Fatal(err)
	}
	for table, columns := range map[string]string{
		"rbac_trial_state": "tenant_id,revision", "rbac_trial_members": "tenant_id,realm,member,active", "rbac_trial_permissions": "tenant_id,role,permission", "rbac_trial_groups": "tenant_id,realm,id,active", "rbac_trial_group_members": "tenant_id,realm,group_id,member,active", "rbac_trial_assignments": "tenant_id,realm,role,member,group_id,namespace,active", "rbac_trial_effects": "tenant_id,value",
	} {
		name := pgx.Identifier{table}.Sanitize()
		for _, sql := range []string{
			`ALTER TABLE ` + name + ` ENABLE ROW LEVEL SECURITY`, `ALTER TABLE ` + name + ` FORCE ROW LEVEL SECURITY`,
			`CREATE POLICY tenant_isolation ON ` + name + ` USING (tenant_id=nullif(current_setting('app.tenant_id',true),'')::uuid) WITH CHECK (tenant_id=nullif(current_setting('app.tenant_id',true),'')::uuid)`,
			`GRANT SELECT (` + columns + `) ON ` + name + ` TO ` + pgx.Identifier{apiRole}.Sanitize(),
		} {
			if _, err := db.operator.Exec(t.Context(), sql); err != nil {
				t.Fatal(err)
			}
		}
	}
	for table, column := range map[string]string{"rbac_trial_state": "revision", "rbac_trial_assignments": "active", "rbac_trial_effects": "value"} {
		if _, err := db.operator.Exec(t.Context(), `GRANT UPDATE (`+column+`) ON `+pgx.Identifier{table}.Sanitize()+` TO `+pgx.Identifier{apiRole}.Sanitize()); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.operator.Exec(t.Context(), `
 INSERT INTO rbac_trial_state VALUES ($1,1),($2,1);
 INSERT INTO rbac_trial_effects VALUES ($1,0),($2,0);
 INSERT INTO rbac_trial_members VALUES ($1,'one','alice',true),($1,'two','alice',true),($1,'one','bob',true),($1,'one','suspended',false),($1,'one','service',true),($2,'one','alice',true);
 INSERT INTO rbac_trial_permissions VALUES ($1,'editor','resource.edit'),($1,'editor','resource.read'),($2,'reader','resource.read');
 INSERT INTO rbac_trial_groups VALUES ($1,'one','editors',true);
 INSERT INTO rbac_trial_group_members VALUES ($1,'one','editors','alice',true);
 INSERT INTO rbac_trial_assignments VALUES ($1,'one','editor','alice',NULL,'A',true),($1,'one','editor','bob',NULL,'B',true),($1,'one','editor',NULL,'editors','B',true),($1,'one','editor',NULL,'editors','A',true),($1,'one','editor','suspended',NULL,'A',true);`, pgx.QueryExecModeSimpleProtocol, housekeepingTenantA, housekeepingTenantB); err != nil {
		t.Fatal(err)
	}
	return db
}

const authorizationTrialPredicate = `EXISTS (
 SELECT 1 FROM rbac_trial_members m
 JOIN rbac_trial_assignments a ON a.tenant_id=m.tenant_id AND a.realm=m.realm AND a.active
 JOIN rbac_trial_permissions p ON p.tenant_id=a.tenant_id AND p.role=a.role AND p.permission=$4
 WHERE m.tenant_id=$1 AND m.realm=$2 AND m.member=$3 AND m.active
 AND (a.namespace IS NULL OR a.namespace=$5)
 AND (a.member=m.member OR EXISTS (
 SELECT 1 FROM rbac_trial_group_members gm JOIN rbac_trial_groups g ON g.tenant_id=gm.tenant_id AND g.realm=gm.realm AND g.id=gm.group_id AND g.active
 WHERE gm.tenant_id=m.tenant_id AND gm.realm=m.realm AND gm.member=m.member AND gm.group_id=a.group_id AND gm.active)))`

func trialBegin(t *testing.T, pool *pgxpool.Pool, tenant string, options pgx.TxOptions) pgx.Tx {
	t.Helper()
	tx, err := pool.BeginTx(t.Context(), options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(t.Context()), 5*time.Second)
		defer cancel()
		_ = tx.Rollback(ctx)
	})
	if err := setTenantContext(t.Context(), tx, tenant); err != nil {
		t.Fatal(err)
	}
	return tx
}

func trialAllow(ctx context.Context, tx pgx.Tx, namespace string) (bool, error) {
	var allow bool
	err := tx.QueryRow(ctx, `SELECT `+authorizationTrialPredicate, housekeepingTenantA, "one", "alice", "resource.edit", namespace).Scan(&allow)
	return allow, err
}

func TestPostgresAuthorizationEvolutionScenarioOracle(t *testing.T) {
	db := authorizationTrial(t)
	var fixture struct {
		Cases []struct {
			Name       string `json:"name"`
			Tenant     string `json:"tenant"`
			Realm      string `json:"realm"`
			Member     string `json:"member"`
			Namespace  string `json:"namespace"`
			Permission string `json:"permission"`
			Allow      bool   `json:"allow"`
		} `json:"cases"`
	}
	raw, err := os.ReadFile("../../../test/authorization/scenarios.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	if len(fixture.Cases) != 12 {
		t.Fatal("review changes to independent scenarios")
	}
	for _, c := range fixture.Cases {
		t.Run(c.Name, func(t *testing.T) {
			tenant := housekeepingTenantA
			if c.Tenant == "b" {
				tenant = housekeepingTenantB
			}
			tx := trialBegin(t, db.api, tenant, pgx.TxOptions{AccessMode: pgx.ReadOnly})
			var got bool
			if err := tx.QueryRow(t.Context(), `SELECT `+authorizationTrialPredicate, tenant, c.Realm, c.Member, c.Permission, c.Namespace).Scan(&got); err != nil {
				t.Fatal(err)
			}
			if got != c.Allow {
				t.Fatalf("allow=%v want=%v", got, c.Allow)
			}
		})
	}
	tx := trialBegin(t, db.api, housekeepingTenantA, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	var visible int
	if err := tx.QueryRow(t.Context(), `SELECT count(*) FROM (VALUES ('A'),('B'),('C')) target(namespace) WHERE `+authorizationTrialPredicate, housekeepingTenantA, "one", "alice", "resource.read", "unused").Scan(&visible); err != nil {
		t.Fatal(err)
	}
	// The constant predicate above deliberately cannot stand in for per-target
	// collection filtering. Use a correlated namespace before counting instead.
	if visible != 0 {
		t.Fatal("wrong-scope negative control unexpectedly allowed")
	}
	sql := `SELECT count(*) FROM (VALUES ('A'),('B'),('C')) target(namespace) WHERE ` + replaceTrialNamespace(authorizationTrialPredicate)
	if err := tx.QueryRow(t.Context(), sql, housekeepingTenantA, "one", "alice", "resource.read").Scan(&visible); err != nil || visible != 2 {
		t.Fatalf("authorized population=%d err=%v", visible, err)
	}
	_ = tx.Rollback(t.Context())
	if _, err := db.operator.Exec(t.Context(), `UPDATE rbac_trial_group_members SET active=false WHERE tenant_id=$1`, housekeepingTenantA); err != nil {
		t.Fatal(err)
	}
	tx = trialBegin(t, db.api, housekeepingTenantA, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	for scope, want := range map[string]bool{"A": true, "B": false} {
		got, err := trialAllow(t.Context(), tx, scope)
		if err != nil || got != want {
			t.Fatalf("group removal scope=%s got=%v err=%v", scope, got, err)
		}
	}
	_ = tx.Rollback(t.Context())
	// Tenant scope supplies all namespaces, while RLS still prevents a caller
	// from selecting another tenant's otherwise valid grants.
	if _, err := db.operator.Exec(t.Context(), `INSERT INTO rbac_trial_assignments VALUES ($1,'one','editor','alice',NULL,NULL,true),($2,'one','reader','alice',NULL,NULL,true)`, housekeepingTenantA, housekeepingTenantB); err != nil {
		t.Fatal(err)
	}
	tx = trialBegin(t, db.api, housekeepingTenantA, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	allow, err := trialAllow(t.Context(), tx, "C")
	if err != nil || !allow {
		t.Fatalf("tenant grant did not cover namespace C: %v %v", allow, err)
	}
	other := trialBegin(t, db.api, housekeepingTenantB, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err := other.QueryRow(t.Context(), `SELECT `+authorizationTrialPredicate, housekeepingTenantB, "one", "alice", "resource.read", "C").Scan(&allow); err != nil || !allow {
		t.Fatalf("other tenant positive control has no access: %v %v", allow, err)
	}
	if err := tx.QueryRow(t.Context(), `SELECT `+authorizationTrialPredicate, housekeepingTenantB, "one", "alice", "resource.read", "C").Scan(&allow); err != nil || allow {
		t.Fatalf("RLS admitted another tenant's valid grant: %v %v", allow, err)
	}
}

func replaceTrialNamespace(sql string) string {
	return strings.ReplaceAll(sql, "a.namespace=$5", "a.namespace=target.namespace")
}

func trialLock(ctx context.Context, tx pgx.Tx, tenant, mode string) error {
	// Mode is chosen only by code in this test, never by a Catalog caller.
	if mode != "SHARE" && mode != "NO KEY UPDATE" && mode != "KEY SHARE" {
		return errors.New("invalid trial lock")
	}
	var revision int64
	return tx.QueryRow(ctx, `SELECT revision FROM rbac_trial_state WHERE tenant_id=$1 FOR `+mode, tenant).Scan(&revision)
}

func trialRevoke(ctx context.Context, tx pgx.Tx) error {
	if _, err := tx.Exec(ctx, `UPDATE rbac_trial_assignments SET active=false WHERE tenant_id=$1`, housekeepingTenantA); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `UPDATE rbac_trial_state SET revision=revision+1 WHERE tenant_id=$1`, housekeepingTenantA)
	return err
}

func trialPID(t *testing.T, tx pgx.Tx) uint32 {
	t.Helper()
	var pid uint32
	if err := tx.QueryRow(t.Context(), `SELECT pg_backend_pid()`).Scan(&pid); err != nil {
		t.Fatal(err)
	}
	return pid
}

func trialWaitBlocked(t *testing.T, pool *pgxpool.Pool, waiting, holder uint32) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 4*time.Second)
	defer cancel()
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for {
		var blocked bool
		if err := pool.QueryRow(ctx, `SELECT $1=ANY(pg_blocking_pids($2))`, holder, waiting).Scan(&blocked); err != nil {
			t.Fatal(err)
		}
		if blocked {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatal("selected session never waited on the selected holder")
		case <-ticker.C:
		}
	}
}

func trialAsync(t *testing.T, operation func(context.Context) error) <-chan error {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 6*time.Second)
	done := make(chan error, 1)
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		defer close(done)
		done <- operation(ctx)
	}()
	t.Cleanup(func() { cancel(); <-stopped })
	return done
}

func TestPostgresAuthorizationEvolutionAdmissionOrdering(t *testing.T) {
	db := authorizationTrial(t)
	reset := func(t *testing.T) {
		t.Helper()
		if _, err := db.operator.Exec(t.Context(), `UPDATE rbac_trial_assignments SET active=true WHERE tenant_id=$1; UPDATE rbac_trial_state SET revision=1 WHERE tenant_id=$1; UPDATE rbac_trial_effects SET value=0 WHERE tenant_id=$1`, pgx.QueryExecModeSimpleProtocol, housekeepingTenantA); err != nil {
			t.Fatal(err)
		}
	}
	t.Run("admitted write commits before revocation", func(t *testing.T) {
		reset(t)
		admitted := trialBegin(t, db.api, housekeepingTenantA, pgx.TxOptions{})
		if err := trialLock(t.Context(), admitted, housekeepingTenantA, "SHARE"); err != nil {
			t.Fatal(err)
		}
		allow, err := trialAllow(t.Context(), admitted, "A")
		if err != nil || !allow {
			t.Fatalf("admission=%v err=%v", allow, err)
		}
		changing := trialBegin(t, db.api, housekeepingTenantA, pgx.TxOptions{})
		changingPID, admittedPID := trialPID(t, changing), trialPID(t, admitted)
		done := trialAsync(t, func(ctx context.Context) error {
			err := trialLock(ctx, changing, housekeepingTenantA, "NO KEY UPDATE")
			if err == nil {
				err = trialRevoke(ctx, changing)
			}
			if err == nil {
				err = changing.Commit(ctx)
			}
			return err
		})
		trialWaitBlocked(t, db.operator, changingPID, admittedPID)
		if _, err := admitted.Exec(t.Context(), `UPDATE rbac_trial_effects SET value=value+1 WHERE tenant_id=$1`, housekeepingTenantA); err != nil {
			t.Fatal(err)
		}
		if err := admitted.Commit(t.Context()); err != nil {
			t.Fatal(err)
		}
		if err := <-done; err != nil {
			t.Fatal(err)
		}
		verify := trialBegin(t, db.api, housekeepingTenantA, pgx.TxOptions{AccessMode: pgx.ReadOnly})
		allow, err = trialAllow(t.Context(), verify, "A")
		if err != nil || allow {
			t.Fatalf("revoked=%v err=%v", allow, err)
		}
		var value, revision int
		if err := verify.QueryRow(t.Context(), `SELECT e.value,s.revision FROM rbac_trial_effects e JOIN rbac_trial_state s USING(tenant_id) WHERE e.tenant_id=$1`, housekeepingTenantA).Scan(&value, &revision); err != nil || value != 1 || revision != 2 {
			t.Fatalf("effect=%d revision=%d err=%v", value, revision, err)
		}
	})
	t.Run("revocation wins and advisory preflight cannot admit", func(t *testing.T) {
		reset(t)
		change := trialBegin(t, db.api, housekeepingTenantA, pgx.TxOptions{})
		if err := trialLock(t.Context(), change, housekeepingTenantA, "NO KEY UPDATE"); err != nil {
			t.Fatal(err)
		}
		if err := trialRevoke(t.Context(), change); err != nil {
			t.Fatal(err)
		}
		write := trialBegin(t, db.api, housekeepingTenantA, pgx.TxOptions{})
		allow, err := trialAllow(t.Context(), write, "A")
		if err != nil || !allow {
			t.Fatalf("preflight must see pre-revocation state: %v %v", allow, err)
		}
		writePID, changePID := trialPID(t, write), trialPID(t, change)
		done := trialAsync(t, func(ctx context.Context) error { return trialLock(ctx, write, housekeepingTenantA, "SHARE") })
		trialWaitBlocked(t, db.operator, writePID, changePID)
		if err := change.Commit(t.Context()); err != nil {
			t.Fatal(err)
		}
		if err := <-done; err != nil {
			t.Fatal(err)
		}
		// A separate Read Committed statement after the wait is essential.
		allow, err = trialAllow(t.Context(), write, "A")
		if err != nil || allow {
			t.Fatalf("stale admission: %v %v", allow, err)
		}
		if err := write.Rollback(t.Context()); err != nil {
			t.Fatal(err)
		}
		var value int
		if err := db.operator.QueryRow(t.Context(), `SELECT value FROM rbac_trial_effects WHERE tenant_id=$1`, housekeepingTenantA).Scan(&value); err != nil || value != 0 {
			t.Fatalf("denied write changed state: %d %v", value, err)
		}
	})
	t.Run("same-statement negative control retains pre-wait authority", func(t *testing.T) {
		reset(t)
		change := trialBegin(t, db.api, housekeepingTenantA, pgx.TxOptions{})
		if err := trialLock(t.Context(), change, housekeepingTenantA, "NO KEY UPDATE"); err != nil {
			t.Fatal(err)
		}
		if err := trialRevoke(t.Context(), change); err != nil {
			t.Fatal(err)
		}
		waiter := trialBegin(t, db.api, housekeepingTenantA, pgx.TxOptions{})
		waiterPID, changePID := trialPID(t, waiter), trialPID(t, change)
		var stale bool
		var revision int
		done := trialAsync(t, func(ctx context.Context) error {
			// The non-key row update can be rechecked after waiting, while the
			// independent permission relation still uses the statement snapshot.
			return waiter.QueryRow(ctx, `WITH admission AS MATERIALIZED (SELECT revision FROM rbac_trial_state WHERE tenant_id=$1 FOR SHARE) SELECT revision,`+authorizationTrialPredicate+` FROM admission`, housekeepingTenantA, "one", "alice", "resource.edit", "A").Scan(&revision, &stale)
		})
		trialWaitBlocked(t, db.operator, waiterPID, changePID)
		if err := change.Commit(t.Context()); err != nil {
			t.Fatal(err)
		}
		if err := <-done; err != nil {
			t.Fatal(err)
		}
		if !stale || revision != 2 {
			t.Fatalf("negative control authority=%v revision=%d", stale, revision)
		}
		fresh, err := trialAllow(t.Context(), waiter, "A")
		if err != nil || fresh {
			t.Fatalf("fresh statement did not see revocation: %v %v", fresh, err)
		}
	})
	t.Run("rollback leaves permission and revision unchanged", func(t *testing.T) {
		reset(t)
		change := trialBegin(t, db.api, housekeepingTenantA, pgx.TxOptions{})
		if err := trialLock(t.Context(), change, housekeepingTenantA, "NO KEY UPDATE"); err != nil {
			t.Fatal(err)
		}
		if err := trialRevoke(t.Context(), change); err != nil {
			t.Fatal(err)
		}
		if err := change.Rollback(t.Context()); err != nil {
			t.Fatal(err)
		}
		check := trialBegin(t, db.api, housekeepingTenantA, pgx.TxOptions{})
		if err := trialLock(t.Context(), check, housekeepingTenantA, "SHARE"); err != nil {
			t.Fatal(err)
		}
		allow, err := trialAllow(t.Context(), check, "A")
		if err != nil || !allow {
			t.Fatalf("rollback lost access: %v %v", allow, err)
		}
		var revision int
		if err := check.QueryRow(t.Context(), `SELECT revision FROM rbac_trial_state WHERE tenant_id=$1`, housekeepingTenantA).Scan(&revision); err != nil || revision != 1 {
			t.Fatalf("rollback changed revision: %d %v", revision, err)
		}
	})
	t.Run("shared admission and other tenants proceed", func(t *testing.T) {
		reset(t)
		first := trialBegin(t, db.api, housekeepingTenantA, pgx.TxOptions{})
		if err := trialLock(t.Context(), first, housekeepingTenantA, "SHARE"); err != nil {
			t.Fatal(err)
		}
		for _, tenant := range []string{housekeepingTenantA, housekeepingTenantB} {
			second := trialBegin(t, db.api, tenant, pgx.TxOptions{})
			mode := "SHARE"
			if tenant == housekeepingTenantB {
				mode = "NO KEY UPDATE"
			}
			ctx, cancel := context.WithTimeout(t.Context(), time.Second)
			err := trialLock(ctx, second, tenant, mode)
			cancel()
			if err != nil {
				t.Fatal(err)
			}
			_ = second.Rollback(t.Context())
		}
	})
	t.Run("key-share negative control does not fence revision update", func(t *testing.T) {
		reset(t)
		first := trialBegin(t, db.api, housekeepingTenantA, pgx.TxOptions{})
		if err := trialLock(t.Context(), first, housekeepingTenantA, "KEY SHARE"); err != nil {
			t.Fatal(err)
		}
		second := trialBegin(t, db.api, housekeepingTenantA, pgx.TxOptions{})
		ctx, cancel := context.WithTimeout(t.Context(), time.Second)
		defer cancel()
		if _, err := second.Exec(ctx, `UPDATE rbac_trial_state SET revision=revision+1 WHERE tenant_id=$1`, housekeepingTenantA); err != nil {
			t.Fatal(err)
		}
		_ = second.Rollback(t.Context())
	})
	t.Run("observed lock wait is bounded and rolls back", func(t *testing.T) {
		reset(t)
		holder := trialBegin(t, db.api, housekeepingTenantA, pgx.TxOptions{})
		if err := trialLock(t.Context(), holder, housekeepingTenantA, "SHARE"); err != nil {
			t.Fatal(err)
		}
		waiter := trialBegin(t, db.api, housekeepingTenantA, pgx.TxOptions{})
		if _, err := waiter.Exec(t.Context(), `SET LOCAL lock_timeout='2s'`); err != nil {
			t.Fatal(err)
		}
		waiterPID, holderPID := trialPID(t, waiter), trialPID(t, holder)
		done := trialAsync(t, func(ctx context.Context) error { return trialLock(ctx, waiter, housekeepingTenantA, "NO KEY UPDATE") })
		trialWaitBlocked(t, db.operator, waiterPID, holderPID)
		err := <-done
		var pgerr *pgconn.PgError
		if !errors.As(err, &pgerr) || pgerr.Code != "55P03" {
			t.Fatalf("expected lock timeout, got %v", err)
		}
		if err := waiter.Rollback(t.Context()); err != nil {
			t.Fatal(err)
		}
		if err := holder.Rollback(t.Context()); err != nil {
			t.Fatal(err)
		}
		after := trialBegin(t, db.api, housekeepingTenantA, pgx.TxOptions{})
		if err := trialLock(t.Context(), after, housekeepingTenantA, "NO KEY UPDATE"); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("cancelled admission releases its connection and changes nothing", func(t *testing.T) {
		reset(t)
		holder := trialBegin(t, db.api, housekeepingTenantA, pgx.TxOptions{})
		if err := trialLock(t.Context(), holder, housekeepingTenantA, "SHARE"); err != nil {
			t.Fatal(err)
		}
		waiter := trialBegin(t, db.api, housekeepingTenantA, pgx.TxOptions{})
		waiterPID, holderPID := trialPID(t, waiter), trialPID(t, holder)
		ctx, cancel := context.WithTimeout(t.Context(), 6*time.Second)
		defer cancel()
		done := trialAsync(t, func(context.Context) error { return trialLock(ctx, waiter, housekeepingTenantA, "NO KEY UPDATE") })
		trialWaitBlocked(t, db.operator, waiterPID, holderPID)
		cancel()
		err := <-done
		var pgerr *pgconn.PgError
		if !errors.Is(err, context.Canceled) && (!errors.As(err, &pgerr) || pgerr.Code != "57014") {
			t.Fatalf("expected client or server cancellation, got %v", err)
		}
		// pgx may close the cancelled connection; explicit rollback releases
		// the transaction or that already-closed connection back to the pool.
		_ = waiter.Rollback(t.Context())
		if err := holder.Rollback(t.Context()); err != nil {
			t.Fatal(err)
		}
		after := trialBegin(t, db.api, housekeepingTenantA, pgx.TxOptions{})
		if err := trialLock(t.Context(), after, housekeepingTenantA, "NO KEY UPDATE"); err != nil {
			t.Fatal(err)
		}
		var revision, value int
		if err := after.QueryRow(t.Context(), `SELECT s.revision,e.value FROM rbac_trial_state s JOIN rbac_trial_effects e USING(tenant_id) WHERE s.tenant_id=$1`, housekeepingTenantA).Scan(&revision, &value); err != nil || revision != 1 || value != 0 {
			t.Fatalf("cancelled admission changed state: revision=%d effect=%d err=%v", revision, value, err)
		}
	})
}

func TestPostgresAuthorizationEvolutionReadSnapshots(t *testing.T) {
	for _, isolation := range []pgx.TxIsoLevel{pgx.ReadCommitted, pgx.RepeatableRead} {
		t.Run(string(isolation), func(t *testing.T) {
			db := authorizationTrial(t)
			read := trialBegin(t, db.api, housekeepingTenantA, pgx.TxOptions{AccessMode: pgx.ReadOnly, IsoLevel: isolation})
			allow, err := trialAllow(t.Context(), read, "A")
			if err != nil || !allow {
				t.Fatal("initial permission unavailable")
			}
			var revision int
			if err := read.QueryRow(t.Context(), `SELECT revision FROM rbac_trial_state WHERE tenant_id=$1`, housekeepingTenantA).Scan(&revision); err != nil || revision != 1 {
				t.Fatal("initial revision unavailable")
			}
			change := trialBegin(t, db.api, housekeepingTenantA, pgx.TxOptions{})
			if err := trialLock(t.Context(), change, housekeepingTenantA, "NO KEY UPDATE"); err != nil {
				t.Fatal(err)
			}
			if err := trialRevoke(t.Context(), change); err != nil {
				t.Fatal(err)
			}
			if err := change.Commit(t.Context()); err != nil {
				t.Fatal(err)
			}
			allow, err = trialAllow(t.Context(), read, "A")
			if err != nil {
				t.Fatal(err)
			}
			if err := read.QueryRow(t.Context(), `SELECT revision FROM rbac_trial_state WHERE tenant_id=$1`, housekeepingTenantA).Scan(&revision); err != nil {
				t.Fatal(err)
			}
			want := isolation == pgx.RepeatableRead
			if allow != want || want && revision != 1 || !want && revision != 2 {
				t.Fatalf("snapshot allow=%v revision=%d", allow, revision)
			}
			if err := read.Commit(t.Context()); err != nil {
				t.Fatal(err)
			}
			fresh := trialBegin(t, db.api, housekeepingTenantA, pgx.TxOptions{AccessMode: pgx.ReadOnly, IsoLevel: pgx.RepeatableRead})
			allow, err = trialAllow(t.Context(), fresh, "A")
			if err != nil || allow {
				t.Fatalf("new snapshot reused allow: %v %v", allow, err)
			}
		})
	}
}

func TestPostgresAuthorizationEvolutionBootstrapAndLockOrder(t *testing.T) {
	db := authorizationTrial(t)
	if _, err := db.operator.Exec(t.Context(), `DELETE FROM rbac_trial_state WHERE tenant_id=$1`, housekeepingTenantA); err != nil {
		t.Fatal(err)
	}
	missing := trialBegin(t, db.api, housekeepingTenantA, pgx.TxOptions{})
	if err := trialLock(t.Context(), missing, housekeepingTenantA, "SHARE"); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("missing authority state must refuse: %v", err)
	}
	_, err := missing.Exec(t.Context(), `INSERT INTO rbac_trial_state VALUES($1,1)`, housekeepingTenantA)
	var pgerr *pgconn.PgError
	if !errors.As(err, &pgerr) || pgerr.Code != "42501" {
		t.Fatalf("runtime acquired bootstrap authority: %v", err)
	}
	_ = missing.Rollback(t.Context())
	first := trialBegin(t, db.operator, housekeepingTenantA, pgx.TxOptions{})
	if _, err := first.Exec(t.Context(), `INSERT INTO rbac_trial_state VALUES($1,7) ON CONFLICT DO NOTHING`, housekeepingTenantA); err != nil {
		t.Fatal(err)
	}
	second := trialBegin(t, db.operator, housekeepingTenantA, pgx.TxOptions{})
	secondPID, firstPID := trialPID(t, second), trialPID(t, first)
	done := trialAsync(t, func(ctx context.Context) error {
		command, err := second.Exec(ctx, `INSERT INTO rbac_trial_state VALUES($1,1) ON CONFLICT DO NOTHING`, housekeepingTenantA)
		if err == nil && command.RowsAffected() != 0 {
			return errors.New("repeated bootstrap replaced state")
		}
		if err == nil {
			err = second.Commit(ctx)
		}
		return err
	})
	trialWaitBlocked(t, db.operator, secondPID, firstPID)
	if err := first.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	var revision int
	if err := db.operator.QueryRow(t.Context(), `SELECT revision FROM rbac_trial_state WHERE tenant_id=$1`, housekeepingTenantA).Scan(&revision); err != nil || revision != 7 {
		t.Fatalf("repeat bootstrap reset revision: %d %v", revision, err)
	}
	connection, err := db.api.Acquire(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Release()
	commandKey := idempotency.AdvisoryLockKey(idempotency.Scope{TenantID: housekeepingTenantA, Actor: "authorization-trial", Route: "trial", Key: "owned-lock-order"})
	if _, err := connection.Exec(t.Context(), `SELECT pg_advisory_lock($1)`, commandKey); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := releaseCommandLock(t.Context(), connection, commandKey); err != nil {
			t.Errorf("owned command lock cleanup: %v", err)
		}
	}()
	tx, err := connection.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(t.Context()), 5*time.Second)
		defer cancel()
		_ = tx.Rollback(ctx)
	}()
	if err := setTenantContext(t.Context(), tx, housekeepingTenantA); err != nil {
		t.Fatal(err)
	}
	if err := trialLock(t.Context(), tx, housekeepingTenantA, "SHARE"); err != nil {
		t.Fatal(err)
	}
	if err := acquireCatalogStateLock(t.Context(), tx, "tenant", housekeepingTenantA); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(t.Context(), `UPDATE rbac_trial_effects SET value=value+1 WHERE tenant_id=$1`, housekeepingTenantA); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	// The actual command/state helpers and a protected row were acquired in the
	// declared order. Whole-operation deadlock freedom remains integration work.
}
