//go:build integration

package database

import (
	"context"
	"errors"
	"fmt"
	"github.com/jackc/pgx/v5"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/clock"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/id"
	"github.com/open-agentic-catalog/open-agentic-catalog/migrations"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
)

const authorizationCoreNamespace = "00000000-0000-7000-8000-000000000810"

func authorizationCoreFixture(t *testing.T) (*housekeepingDatabase, *PostgresIdentityStore, *identity.Service, identity.Actor, identity.Actor, identity.RoleAssignment) {
	t.Helper()
	db := newHousekeepingDatabase(t)
	_, err := ProvisionIdentity(t.Context(), db.operator, IdentityProvisioningInput{TenantID: housekeepingTenantA, TenantDisplayName: "Housekeeping tenant A", Issuer: "https://identity.core.example", TenancyMode: "dedicated", Subject: "admin", ActorType: identity.ActorHuman, Roles: []identity.Role{identity.RoleTenantAdmin}, Operator: "qualification", Reason: "Initialize owned authorization qualification"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.operator.Exec(t.Context(), `INSERT INTO namespaces(id,tenant_id,slug,display_name,created_at,updated_at,revision) VALUES($1,$2,'core','Core',clock_timestamp(),clock_timestamp(),1)`, authorizationCoreNamespace, housekeepingTenantA)
	if err != nil {
		t.Fatal(err)
	}
	repo, err := NewPostgresRepository(db.api)
	if err != nil {
		t.Fatal(err)
	}
	store, err := NewPostgresIdentityStore(repo)
	if err != nil {
		t.Fatal(err)
	}
	admin, err := store.Resolve(t.Context(), identity.Principal{Issuer: "https://identity.core.example", TenantID: housekeepingTenantA, Subject: "admin"})
	if err != nil {
		t.Fatal(err)
	}
	service, err := identity.NewService(identity.ServiceOptions{Store: store, Clock: clock.Real{}, IDs: id.Generator{}, CursorKey: []byte("01234567890123456789012345678901")})
	if err != nil {
		t.Fatal(err)
	}
	assignment, err := service.GrantRole(identity.WithActor(t.Context(), admin), identity.GrantRoleInput{Subject: "alice", ActorType: identity.ActorHuman, Role: identity.RoleReader, NamespaceID: authorizationCoreNamespace, Reason: "Grant namespace qualification read"}, "grant")
	if err != nil {
		t.Fatal(err)
	}
	alice, err := store.ResolvePrincipal(t.Context(), identity.Principal{Issuer: "https://identity.core.example", TenantID: housekeepingTenantA, Subject: "alice"})
	if err != nil {
		t.Fatal(err)
	}
	return db, store, service, admin, alice, assignment
}
func TestPostgresAuthorizationCoreDecisionsAndHistory(t *testing.T) {
	db, store, service, admin, alice, assignment := authorizationCoreFixture(t)
	checks := []identity.PermissionCheck{{Permission: "resource.read", NamespaceID: authorizationCoreNamespace}, {Permission: "resource.read"}, {Permission: "access.manage"}}
	decision, err := store.CheckPermissions(t.Context(), alice, checks)
	if err != nil || !reflect.DeepEqual(decision.Allowed, []bool{true, false, false}) || alice.Namespaces != nil || len(alice.Roles) != 0 {
		t.Fatalf("%+v %v actor=%+v", decision, err, alice)
	}
	ordered, err := store.CheckPermissions(t.Context(), alice, []identity.PermissionCheck{checks[2], checks[0], checks[1]})
	if err != nil || ordered.Revision != decision.Revision || !reflect.DeepEqual(ordered.Allowed, []bool{false, true, false}) {
		t.Fatalf("revision or decision order changed: %+v %v", ordered, err)
	}
	for _, d := range identity.Permissions() {
		if _, err := store.CheckPermissions(t.Context(), admin, []identity.PermissionCheck{{Permission: d.Key}}); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := db.operator.Query(t.Context(), `SELECT d.key,p.permission FROM role_definitions d LEFT JOIN role_permissions p ON p.tenant_id=d.tenant_id AND p.role_definition_id=d.id WHERE d.tenant_id=$1 ORDER BY d.key,p.permission`, housekeepingTenantA)
	if err != nil {
		t.Fatal(err)
	}
	actual := map[string][]identity.Permission{}
	for rows.Next() {
		var key string
		var permission *string
		if err := rows.Scan(&key, &permission); err != nil {
			t.Fatal(err)
		}
		if _, ok := actual[key]; !ok {
			actual[key] = nil
		}
		if permission != nil {
			actual[key] = append(actual[key], identity.Permission(*permission))
		}
	}
	rows.Close()
	if rows.Err() != nil {
		t.Fatal(rows.Err())
	}
	if len(actual) != 14 {
		t.Fatal("missing builtins")
	}
	for key, got := range actual {
		want, ok := identity.BuiltinPermissions(key)
		slices.Sort(want)
		if !ok || !slices.Equal(got, want) {
			t.Fatalf("migration template drift %s", key)
		}
	}
	_, err = service.GrantRole(identity.WithActor(t.Context(), admin), identity.GrantRoleInput{Subject: "alice", ActorType: identity.ActorHuman, Role: identity.RoleReader, NamespaceID: authorizationCoreNamespace, Reason: "Duplicate read assignment"}, "duplicate")
	if identity.ErrorCodeOf(err) != identity.CodeConflict {
		t.Fatalf("duplicate %v", err)
	}
	after, _ := store.CheckPermissions(t.Context(), alice, checks)
	if after.Revision != decision.Revision {
		t.Fatal("failed duplicate advanced authority")
	}
	// Failure after membership/assignment writes must roll back their revision
	// increments as well as the business rows, not just omit audit output.
	_, err = store.GrantRole(identity.WithActor(t.Context(), admin), identity.GrantRoleParams{TenantID: admin.TenantID, RealmID: admin.RealmID, MembershipID: "00000000-0000-7000-8000-000000000811", AssignmentID: "00000000-0000-7000-8000-000000000812", Subject: "rollback", ActorType: identity.ActorHuman, Role: identity.RoleReader, Actor: admin.Subject, Reason: "Reject missing atomic evidence", At: time.Now()})
	if err == nil {
		t.Fatal("missing evidence accepted")
	}
	after, err = store.CheckPermissions(t.Context(), alice, checks)
	if err != nil || after.Revision != decision.Revision {
		t.Fatal("failed evidence advanced authority", err)
	}
	var leaked int
	if err := db.operator.QueryRow(t.Context(), `SELECT count(*) FROM tenant_memberships WHERE tenant_id=$1 AND subject='rollback'`, housekeepingTenantA).Scan(&leaked); err != nil || leaked != 0 {
		t.Fatal("failed evidence retained membership", err)
	}
	for _, sql := range []string{`UPDATE role_definitions SET revision=revision WHERE tenant_id=$1`, `DELETE FROM role_permissions WHERE tenant_id=$1`, `INSERT INTO role_permissions(tenant_id,role_definition_id,permission) SELECT tenant_id,id,'unknown' FROM role_definitions WHERE tenant_id=$1 LIMIT 1`, `INSERT INTO role_permissions(tenant_id,role_definition_id,permission) SELECT tenant_id,id,'access.manage' FROM role_definitions WHERE tenant_id=$1 AND key='reader'`} {
		tx := trialBegin(t, db.operator, housekeepingTenantA, pgx.TxOptions{})
		if _, err := tx.Exec(t.Context(), sql, housekeepingTenantA); err == nil {
			t.Fatalf("accepted invalid built-in mutation %s", sql)
		}
	}
	forged := alice
	forged.Roles = []identity.Role{identity.RoleTenantAdmin}
	projection, err := store.CheckPermissions(t.Context(), forged, checks)
	if err != nil || !reflect.DeepEqual(projection.Allowed, decision.Allowed) {
		t.Fatal("Actor role projection granted authority", err)
	}
	forged.Subject = "different"
	d, e := store.CheckPermissions(t.Context(), forged, checks)
	if e != nil || slices.Contains(d.Allowed, true) {
		t.Fatal("hydrated/forged actor grants authority")
	}
	revoked, err := service.RevokeRole(identity.WithActor(t.Context(), admin), assignment.ID, "Withdraw qualification read", "revoke", assignment.Revision)
	if err != nil || revoked.State != identity.AssignmentRevoked {
		t.Fatal(err)
	}
	after, err = store.CheckPermissions(t.Context(), alice, checks)
	if err != nil || slices.Contains(after.Allowed, true) || after.Revision <= decision.Revision {
		t.Fatalf("revocation %v %v", after, err)
	}
	// The API's new schema reads remain tenant-isolated; the worker cannot read them.
	tx := trialBegin(t, db.api, housekeepingTenantB, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	var count int
	if err := tx.QueryRow(t.Context(), `SELECT count(*) FROM role_definitions WHERE tenant_id=$1`, housekeepingTenantA).Scan(&count); err != nil || count != 0 {
		t.Fatal("cross-tenant roles leaked", err)
	}
	if _, err := db.worker.Exec(t.Context(), `SELECT permission FROM role_permissions LIMIT 1`); err == nil {
		t.Fatal("worker gained authority reads")
	}
}
func TestPostgresAuthorizationCoreAdmissionAtomicityAndOrdering(t *testing.T) {
	db, store, service, admin, alice, assignment := authorizationCoreFixture(t)
	checks := []identity.PermissionCheck{{Permission: "resource.read", NamespaceID: authorizationCoreNamespace}}
	// A domain write joins the coordinator, and an error rolls it back.
	sentinel := errors.New("local operation failed")
	err := store.WithPermissionAdmission(t.Context(), admin, []identity.PermissionCheck{{Permission: "namespace.manage", NamespaceID: authorizationCoreNamespace}}, func(ctx context.Context) error {
		_, e := withTenantTx(ctx, db.api, housekeepingTenantA, pgx.TxOptions{}, func(tx pgx.Tx) (bool, error) {
			_, e := tx.Exec(ctx, `UPDATE namespaces SET display_name='Provisional',revision=revision+1,updated_at=clock_timestamp() WHERE tenant_id=$1 AND id=$2`, housekeepingTenantA, authorizationCoreNamespace)
			return true, e
		})
		if e != nil {
			return e
		}
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatal(err)
	}
	var name string
	if err := db.operator.QueryRow(t.Context(), `SELECT display_name FROM namespaces WHERE tenant_id=$1 AND id=$2`, housekeepingTenantA, authorizationCoreNamespace).Scan(&name); err != nil || name != "Core" {
		t.Fatal("admission failed to roll back", err, name)
	}
	// Actual command response completion keeps the admitted lock after the callback.
	conn, err := db.api.Acquire(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Release()
	command := &commandTransaction{pool: db.api, conn: conn, tenantID: housekeepingTenantA}
	ctx := withCommandTransaction(t.Context(), command)
	if err := store.WithPermissionAdmission(ctx, alice, checks, func(context.Context) error { return nil }); err != nil {
		t.Fatal(err)
	}
	holder := conn.Conn().PgConn().PID()
	writerConn, err := db.api.Acquire(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer writerConn.Release()
	writer := &commandTransaction{pool: db.api, conn: writerConn, tenantID: housekeepingTenantA}
	result := trialAsync(t, func(ctx context.Context) error {
		writerCtx := identity.WithActor(withCommandTransaction(ctx, writer), admin)
		_, err := service.RevokeRole(writerCtx, assignment.ID, "Serialize revocation", "revoke", assignment.Revision)
		if err != nil {
			return err
		}
		return writer.complete(ctx, 200, func(context.Context, pgx.Tx) error { return nil })
	})
	trialWaitBlocked(t, db.operator, writerConn.Conn().PgConn().PID(), holder)
	if err := command.complete(ctx, 200, func(context.Context, pgx.Tx) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if err := <-result; err != nil {
		t.Fatal(err)
	}
	called := false
	if err := store.WithPermissionAdmission(t.Context(), alice, checks, func(context.Context) error { called = true; return nil }); identity.ErrorCodeOf(err) != identity.CodeForbidden || called {
		t.Fatalf("revoked admission %v called=%t", err, called)
	}
	// Readmission waiting behind an exclusive change checks a fresh RC statement.
	grant, err := service.GrantRole(identity.WithActor(t.Context(), admin), identity.GrantRoleInput{Subject: "alice", ActorType: identity.ActorHuman, Role: identity.RoleReader, NamespaceID: authorizationCoreNamespace, Reason: "Readmit namespace read"}, "regrant")
	if err != nil {
		t.Fatal(err)
	}
	exclusive := trialBegin(t, db.api, housekeepingTenantA, pgx.TxOptions{})
	exclusiveCtx := identity.WithActor(t.Context(), admin)
	if err := lockAuthorizationWriter(exclusiveCtx, exclusive, housekeepingTenantA); err != nil {
		t.Fatal(err)
	}
	if _, err := exclusive.Exec(t.Context(), `UPDATE role_assignments SET state='revoked',revision=revision+1,revoked_by='admin',revoke_reason='Controlled revocation',revoked_at=clock_timestamp() WHERE tenant_id=$1 AND id=$2`, housekeepingTenantA, grant.ID); err != nil {
		t.Fatal(err)
	}
	waiting, err := db.api.Acquire(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer waiting.Release()
	waitingCommand := &commandTransaction{pool: db.api, conn: waiting, tenantID: housekeepingTenantA}
	done := trialAsync(t, func(ctx context.Context) error {
		return store.WithPermissionAdmission(withCommandTransaction(ctx, waitingCommand), alice, checks, func(context.Context) error { return errors.New("revoked callback executed") })
	})
	trialWaitBlocked(t, db.operator, waiting.Conn().PgConn().PID(), trialPID(t, exclusive))
	if err := exclusive.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if identity.ErrorCodeOf(err) != identity.CodeForbidden {
			t.Fatalf("waited check %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("waiting admission failed to finish")
	}
}

func TestPostgresAuthorizationCoreForwardMigration(t *testing.T) {
	db := newHousekeepingDatabase(t)
	for _, trial := range []struct{ invalidGate, unfinishedCommand bool }{{}, {invalidGate: true}, {unfinishedCommand: true}} {
		invalidGate := trial.invalidGate
		t.Run(fmt.Sprintf("invalid_gate_%t_unfinished_command_%t", invalidGate, trial.unfinishedCommand), func(t *testing.T) {
			schema := fmt.Sprintf("catalog_auth_upgrade_%d", time.Now().UnixNano())
			if _, err := db.operator.Exec(t.Context(), `CREATE SCHEMA `+pgx.Identifier{schema}.Sanitize()); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				_, err := db.operator.Exec(context.Background(), `DROP SCHEMA IF EXISTS `+pgx.Identifier{schema}.Sanitize()+` CASCADE`)
				if err != nil {
					t.Error(err)
				}
			})
			var cryptoSchema string
			if err := db.operator.QueryRow(t.Context(), `SELECT n.nspname FROM pg_extension e JOIN pg_namespace n ON n.oid=e.extnamespace WHERE e.extname='pgcrypto'`).Scan(&cryptoSchema); err != nil {
				t.Fatal(err)
			}
			pool, err := Open(t.Context(), withSearchPath(t, db.operator.Config().ConnString(), schema+","+cryptoSchema+",public"), testPoolConfig(2))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(pool.Close)
			plan, err := Plan(t.Context(), pool)
			if err != nil {
				t.Fatal(err)
			}
			// Construct the actual predecessor using its immutable migration bodies and
			// checksums, without changing the production runner or historical migrations.
			for _, m := range plan {
				if m.Version >= 34 {
					break
				}
				body, err := migrations.Files.ReadFile(m.Name)
				if err != nil {
					t.Fatal(err)
				}
				tx, err := pool.Begin(t.Context())
				if err != nil {
					t.Fatal(err)
				}
				if _, err := tx.Exec(t.Context(), string(body)); err != nil {
					_ = tx.Rollback(t.Context())
					t.Fatal(err)
				}
				if _, err := tx.Exec(t.Context(), `INSERT INTO catalog_schema_migrations(version,checksum) VALUES($1,$2)`, m.Version, m.Checksum); err != nil {
					_ = tx.Rollback(t.Context())
					t.Fatal(err)
				}
				if err := tx.Commit(t.Context()); err != nil {
					t.Fatal(err)
				}
			}
			const member = "00000000-0000-7000-8000-000000000020"
			const realm = "00000000-0000-7000-8000-000000000010"
			const assignment = "00000000-0000-7000-8000-000000000030"
			for _, sql := range []string{
				`INSERT INTO tenants(id,display_name,created_at) VALUES($1,'Upgrade',clock_timestamp())`,
				`INSERT INTO tenant_identity_realms(id,tenant_id,issuer,tenancy_mode,external_organization_key,state,revision,created_at,updated_at) VALUES('` + realm + `',$1,'https://upgrade.example','dedicated','','active',1,clock_timestamp(),clock_timestamp())`,
				`INSERT INTO tenant_memberships(id,tenant_id,realm_id,subject,actor_type,state,revision,created_at,updated_at) VALUES('` + member + `',$1,'` + realm + `','retained','human','active',1,clock_timestamp(),clock_timestamp())`,
				`INSERT INTO namespaces(id,tenant_id,slug,display_name,created_at,updated_at,revision) VALUES('` + authorizationCoreNamespace + `',$1,'retained','Retained',clock_timestamp(),clock_timestamp(),1)`,
			} {
				if _, err := pool.Exec(t.Context(), sql, housekeepingTenantA); err != nil {
					t.Fatal(err)
				}
			}
			namespace := ""
			role := "tenant_admin"
			if invalidGate {
				namespace = authorizationCoreNamespace
				role = "reader"
			}
			if _, err := pool.Exec(t.Context(), `INSERT INTO role_assignments(id,tenant_id,membership_id,namespace_id,role,state,revision,granted_by,grant_reason,granted_at) VALUES($1,$2,$3,nullif($4,'')::uuid,$5,'active',1,'fixture','Retained reviewed authority',clock_timestamp())`, assignment, housekeepingTenantA, member, namespace, role); err != nil {
				t.Fatal(err)
			}
			if trial.unfinishedCommand {
				if _, err := pool.Exec(t.Context(), `INSERT INTO idempotency_keys(tenant_id,actor,route,idempotency_key,request_digest,expires_at,created_at,protocol_version,authorization_digest,execution_token) VALUES($1,'retained','POST /test','unfinished','digest',clock_timestamp()-interval '1 hour',clock_timestamp()-interval '2 hours',2,'sha256:'||repeat('a',64),uuidv7())`, housekeepingTenantA); err != nil {
					t.Fatal(err)
				}
			}
			applied, err := applyMigrationsForTest(t.Context(), pool)
			switch {
			case trial.unfinishedCommand:
				if err == nil || !strings.Contains(err.Error(), "authorization cutover requires") {
					t.Fatalf("unfinished reservation did not stop cutover: applied=%v err=%v", applied, err)
				}
				var committed int
				if err := pool.QueryRow(t.Context(), `SELECT max(version) FROM catalog_schema_migrations`).Scan(&committed); err != nil || committed != 35 {
					t.Fatalf("cutover did not retain committed predecessors: version=%d err=%v", committed, err)
				}
				var absent bool
				if err := pool.QueryRow(t.Context(), `SELECT NOT EXISTS(SELECT 1 FROM information_schema.columns WHERE table_schema=$1 AND table_name='idempotency_keys' AND column_name='replay_authorization')`, schema).Scan(&absent); err != nil || !absent {
					t.Fatal("refused cutover changed replay schema", err)
				}
				// Model the exact schema-owner release accepted by the existing
				// repair procedure; no reservation is deleted or re-executed.
				if _, err := pool.Exec(t.Context(), `UPDATE idempotency_keys SET released_at=clock_timestamp(), repair_reason='Owned cutover repair',execution_token=NULL WHERE tenant_id=$1 AND idempotency_key='unfinished'`, housekeepingTenantA); err != nil {
					t.Fatal(err)
				}
				if resumed, err := applyMigrationsForTest(t.Context(), pool); err != nil || len(resumed) != 12 || resumed[0].Version != 36 || resumed[1].Version != 37 || resumed[2].Version != 38 || resumed[3].Version != 39 || resumed[4].Version != 40 || resumed[5].Version != 41 || resumed[6].Version != 42 || resumed[7].Version != 43 || resumed[8].Version != 44 || resumed[9].Version != 45 || resumed[10].Version != 46 || resumed[11].Version != 47 {
					t.Fatalf("repaired cutover did not resume: %v %v", resumed, err)
				}
			case invalidGate:
				if err == nil || !strings.Contains(err.Error(), "active namespace assignment") {
					t.Fatalf("unsupported state accepted %v", err)
				}
				var absent bool
				if err := pool.QueryRow(t.Context(), `SELECT to_regclass($1) IS NULL`, schema+".tenant_authorization_state").Scan(&absent); err != nil || !absent {
					t.Fatal("failed migration left new state", err)
				}
			default:
				if err != nil || len(applied) != 14 || applied[0].Version != 34 || applied[1].Version != 35 || applied[2].Version != 36 || applied[3].Version != 37 || applied[4].Version != 38 || applied[5].Version != 39 || applied[6].Version != 40 || applied[7].Version != 41 || applied[8].Version != 42 || applied[9].Version != 43 || applied[10].Version != 44 || applied[11].Version != 45 || applied[12].Version != 46 || applied[13].Version != 47 {
					t.Fatalf("%v %v", applied, err)
				}
				var reference string
				var revision int64
				if err := pool.QueryRow(t.Context(), `SELECT role_definition_id::text,revision FROM role_assignments WHERE tenant_id=$1 AND id=$2`, housekeepingTenantA, assignment).Scan(&reference, &revision); err != nil || reference == "" || revision != 1 {
					t.Fatal("retained assignment changed", err)
				}
				if applied, err := applyMigrationsForTest(t.Context(), pool); err != nil || len(applied) != 0 {
					t.Fatal("migration not repeat safe", err)
				}
			}
			var retained int
			if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM role_assignments WHERE id=$1 AND state='active' AND revision=1`, assignment).Scan(&retained); err != nil || retained != 1 {
				t.Fatal("retained identity/history lost", err)
			}
		})
	}
}

func TestPostgresAuthorizationCoreStaleAdministratorAndMissingState(t *testing.T) {
	db, store, service, admin, alice, _ := authorizationCoreFixture(t)
	ctx := identity.WithActor(t.Context(), admin)
	if _, err := service.GrantRole(ctx, identity.GrantRoleInput{Subject: "second-admin", ActorType: identity.ActorHuman, Role: identity.RoleTenantAdmin, Reason: "Preserve direct administrator continuity"}, "second-admin"); err != nil {
		t.Fatal(err)
	}
	var ownAssignment string
	if err := db.operator.QueryRow(t.Context(), `SELECT id::text FROM role_assignments WHERE tenant_id=$1 AND membership_id=$2 AND role='tenant_admin' AND state='active'`, admin.TenantID, admin.MembershipID).Scan(&ownAssignment); err != nil {
		t.Fatal(err)
	}
	if _, err := service.RevokeRole(ctx, ownAssignment, "Withdraw first administrator", "withdraw-admin", 1); err != nil {
		t.Fatal(err)
	}
	before, err := store.CheckPermissions(t.Context(), admin, []identity.PermissionCheck{{Permission: "access.manage"}})
	if err != nil || before.Allowed[0] {
		t.Fatal("revoked administrator retained authority", err)
	}
	// The old hydrated Actor still has tenant_admin: repository admission must
	// re-read current data rather than trusting the domain's old role projection.
	if !admin.HasRole(identity.RoleTenantAdmin, "") {
		t.Fatal("test lost its stale Actor")
	}
	if _, err := service.GrantRole(ctx, identity.GrantRoleInput{Subject: "denied", ActorType: identity.ActorHuman, Role: identity.RoleReader, Reason: "Rejected stale authority"}, "stale-grant"); identity.ErrorCodeOf(err) != identity.CodeForbidden {
		t.Fatalf("stale administrator admitted %v", err)
	}
	after, _ := store.CheckPermissions(t.Context(), admin, []identity.PermissionCheck{{Permission: "access.manage"}})
	if after.Revision != before.Revision {
		t.Fatal("denied administration changed authority")
	}
	// A same-tenant assignment may not point to another tenant's template.
	tx := trialBegin(t, db.operator, housekeepingTenantA, pgx.TxOptions{})
	_, err = tx.Exec(t.Context(), `INSERT INTO role_assignments(id,tenant_id,membership_id,role_definition_id,role,state,revision,granted_by,grant_reason,granted_at) SELECT uuidv7(),$1,$2,id,'contributor','active',1,'fixture','Invalid cross-tenant reference',clock_timestamp() FROM role_definitions WHERE tenant_id=$3 AND key='contributor'`, housekeepingTenantA, alice.MembershipID, housekeepingTenantB)
	if err == nil {
		t.Fatal("cross-tenant definition reference accepted")
	}
	_ = tx.Rollback(t.Context())
	// Corrupt only this invocation-owned fixture to exercise missing-state refusal.
	for _, sql := range []string{`ALTER TABLE tenant_authorization_state DISABLE TRIGGER tenant_authorization_state_history`, `DELETE FROM tenant_authorization_state WHERE tenant_id='` + housekeepingTenantA + `'`, `ALTER TABLE tenant_authorization_state ENABLE TRIGGER tenant_authorization_state_history`} {
		if _, err := db.operator.Exec(t.Context(), sql); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.CheckPermissions(t.Context(), alice, []identity.PermissionCheck{{Permission: "resource.read", NamespaceID: authorizationCoreNamespace}}); identity.ErrorCodeOf(err) != identity.CodeDependency {
		t.Fatalf("missing state admitted %v", err)
	}
	var apiRole, workerRole string
	if err := db.api.QueryRow(t.Context(), `SELECT current_user`).Scan(&apiRole); err != nil {
		t.Fatal(err)
	}
	if err := db.worker.QueryRow(t.Context(), `SELECT current_user`).Scan(&workerRole); err != nil {
		t.Fatal(err)
	}
	restoreTx := trialBegin(t, db.operator, housekeepingTenantA, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	report := RestoreVerificationReport{}
	usable, err := verifyRestoreSchema(t.Context(), restoreTx, RestoreVerificationOptions{TenantID: housekeepingTenantA, APIRole: apiRole, WorkerRole: workerRole}, &report)
	if err != nil || usable || !slices.Equal(restoreFindingCodes(report), []string{"RESTORE_AUTHORIZATION_STATE_MISSING"}) {
		t.Fatalf("missing-state restore usable=%t report=%+v err=%v", usable, report, err)
	}
}
