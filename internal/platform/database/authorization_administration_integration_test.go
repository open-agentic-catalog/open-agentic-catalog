//go:build integration

package database

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/clock"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/id"
)

func TestPostgresAuthorizationAdministrationPreservesLegacyRoleProjection(t *testing.T) {
	_, store, service, admin, alice, _ := authorizationCoreFixture(t)
	ctx := identity.WithActor(t.Context(), admin)
	// This independent list is the existing public role vocabulary, not the
	// newer built-in permission-template catalog.
	want := []identity.Role{identity.RoleReader, identity.RoleContributor, identity.RoleReviewer, identity.RolePublisher, identity.RoleSourceAdmin, identity.RoleSecurityReviewer, identity.RoleTenantAdmin, identity.RoleServiceClient}
	for _, role := range want {
		if _, err := service.GrantRole(ctx, identity.GrantRoleInput{Subject: alice.Subject, ActorType: alice.ActorType, Role: role, Reason: "Preserve existing role projection"}, "grant"); err != nil {
			t.Fatal(err)
		}
	}
	definitions, err := service.ListRoleDefinitions(ctx, 100, "")
	if err != nil {
		t.Fatal(err)
	}
	var futureTemplate string
	for _, role := range definitions.Items {
		if role.Key == "discovery_reader" {
			futureTemplate = role.ID
		}
	}
	custom, err := service.SaveRoleDefinition(ctx, "", 0, identity.RoleDefinitionInput{Name: "Custom read", Permissions: []identity.Permission{"resource.read"}, Reason: "Check projection isolation"}, false, "custom")
	if err != nil || futureTemplate == "" {
		t.Fatal("role controls", err)
	}
	for _, definition := range []string{custom.ID, futureTemplate} {
		if _, err := service.GrantAuthorizationAssignment(ctx, identity.AccessAssignmentInput{RoleDefinitionID: definition, MembershipID: alice.MembershipID, Reason: "Keep new templates out of legacy projection"}, "grant"); err != nil {
			t.Fatal(err)
		}
	}
	actor, err := store.Resolve(t.Context(), identity.Principal{Issuer: "https://identity.core.example", TenantID: alice.TenantID, Subject: alice.Subject})
	slices.Sort(want)
	if err != nil || !slices.Equal(actor.Roles, want) {
		t.Fatalf("legacy roles=%v want=%v error=%v", actor.Roles, want, err)
	}
	assignments, err := service.ListRoleAssignments(ctx, identity.RoleAssignmentListRequest{MembershipID: alice.MembershipID, Limit: 100})
	if err != nil || len(assignments) != len(want)+1 { // The existing namespace Reader is retained too.
		t.Fatal("legacy assignment history lost or expanded", len(assignments), err)
	}
	for _, assignment := range assignments {
		if !slices.Contains(want, assignment.Role) {
			t.Fatal("new template leaked into legacy enum", assignment.Role)
		}
	}
}

func TestPostgresAuthorizationAdministrationRestoreGroupMembership(t *testing.T) {
	db, _, service, admin, alice, direct := authorizationCoreFixture(t)
	ctx := identity.WithActor(t.Context(), admin)
	group, err := service.SaveAccessGroup(ctx, "", 0, identity.GroupInput{Name: "Restore qualification", Reason: "Create retained group"}, "", "group")
	if err != nil {
		t.Fatal(err)
	}
	member, err := service.ChangeAccessGroupMember(ctx, group.ID, "", alice.MembershipID, 0, "Retain group relation", "add", false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.RevokeRole(ctx, direct.ID, "Exclude direct grant control", "revoke", direct.Revision); err != nil {
		t.Fatal(err)
	}
	check := func(wantFinding bool) {
		t.Helper()
		tx := trialBegin(t, db.operator, admin.TenantID, pgx.TxOptions{AccessMode: pgx.ReadOnly})
		records, more, err := scanRestoreMemberships(t.Context(), tx, admin.TenantID, "", 100)
		if err != nil || more {
			t.Fatal("restore scan", more, err)
		}
		for _, record := range records {
			if record.key == alice.MembershipID {
				if (record.finding != nil) != wantFinding || wantFinding && record.finding.Code != "RESTORE_OFFBOARDED_MEMBERSHIP_HAS_ACTIVE_GRANTS" {
					t.Fatalf("restore finding=%+v want=%v", record.finding, wantFinding)
				}
				if err := tx.Rollback(t.Context()); err != nil {
					t.Fatal(err)
				}
				return
			}
		}
		t.Fatal("restore scan omitted retained membership")
	}
	check(false)
	// Simulate a restored incomplete offboarding through a legal owner-only
	// state transition. Prospective grants to offboarded members remain denied.
	// Keep the monotonic-time guard authoritative, including after host-clock
	// corrections. A backdated transition must fail and preserve the membership.
	_, err = db.operator.Exec(t.Context(), `UPDATE tenant_memberships SET state='offboarded',revision=revision+1,updated_at=updated_at-interval '1 second' WHERE tenant_id=$1 AND id=$2`, admin.TenantID, alice.MembershipID)
	var backdated *pgconn.PgError
	if !errors.As(err, &backdated) || backdated.Code != "P0001" || !strings.Contains(backdated.Message, "tenant membership lifecycle transition is invalid") {
		t.Fatal("backdated restore fixture transition was not rejected by its lifecycle guard", err)
	}
	check(false)
	// This fixture needs a valid transition, not a measurement of elapsed time.
	if _, err := db.operator.Exec(t.Context(), `UPDATE tenant_memberships SET state='offboarded',revision=revision+1,updated_at=updated_at+interval '1 second' WHERE tenant_id=$1 AND id=$2`, admin.TenantID, alice.MembershipID); err != nil {
		t.Fatal(err)
	}
	check(true)
	var retained int
	if err := db.operator.QueryRow(t.Context(), `SELECT count(*) FROM access_group_members WHERE tenant_id=$1 AND id=$2 AND state='active'`, admin.TenantID, member.ID).Scan(&retained); err != nil || retained != 1 {
		t.Fatal("restore verification changed group evidence", retained, err)
	}
}

func TestPostgresAuthorizationAdministrationCompositionAndLifecycle(t *testing.T) {
	db, store, _, admin, alice, seeded := authorizationCoreFixture(t)
	// This tests policy composition, not elapsed wall time. Pin valid domain
	// timestamps after the retained seed; backwards host-clock steps must not
	// turn a cosmetic-edit control into an unrelated time-guard rejection.
	s, err := identity.NewService(identity.ServiceOptions{Store: store, Clock: clock.NewFake(seeded.GrantedAt.Add(time.Second)), IDs: id.Generator{}, CursorKey: []byte("01234567890123456789012345678901")})
	if err != nil {
		t.Fatal(err)
	}
	ctx := identity.WithActor(t.Context(), admin)
	role, err := s.SaveRoleDefinition(ctx, "", 0, identity.RoleDefinitionInput{Name: "Scoped custom reader", Permissions: []identity.Permission{"resource.read"}, Reason: "Create reviewed role"}, false, "role")
	if err != nil {
		t.Fatal(err)
	}
	group, err := s.SaveAccessGroup(ctx, "", 0, identity.GroupInput{Name: "Same realm team", Reason: "Create reviewed group"}, "", "group")
	if err != nil {
		t.Fatal(err)
	}
	gm, err := s.ChangeAccessGroupMember(ctx, group.ID, "", alice.MembershipID, 0, "Add same-realm member", "member", false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ChangeAccessGroupMember(ctx, group.ID, "", alice.MembershipID, 0, "Reject duplicate group relation", "duplicate-member", false); identity.ErrorCodeOf(err) != identity.CodeConflict {
		t.Fatal("duplicate group member", err)
	}
	ga, err := s.GrantAuthorizationAssignment(ctx, identity.AccessAssignmentInput{RoleDefinitionID: role.ID, GroupID: group.ID, NamespaceID: authorizationCoreNamespace, Reason: "Grant group scope"}, "grant")
	if err != nil {
		t.Fatal(err)
	}
	da, err := s.GrantAuthorizationAssignment(ctx, identity.AccessAssignmentInput{RoleDefinitionID: role.ID, MembershipID: alice.MembershipID, NamespaceID: authorizationCoreNamespace, Reason: "Grant direct scope"}, "grant")
	if err != nil {
		t.Fatal(err)
	}
	check := func(want bool) int64 {
		t.Helper()
		d, err := store.CheckPermissions(t.Context(), alice, []identity.PermissionCheck{{Permission: "resource.read", NamespaceID: authorizationCoreNamespace}, {Permission: "resource.read"}, {Permission: "access.manage"}})
		if err != nil || !reflect.DeepEqual(d.Allowed, []bool{want, false, false}) {
			t.Fatalf("permission decisions=%+v %v", d, err)
		}
		return d.Revision
	}
	rev := check(true)
	role, err = s.SaveRoleDefinition(ctx, role.ID, role.Revision, identity.RoleDefinitionInput{Name: "Renamed role", Permissions: role.Permissions, Reason: "Cosmetic edit"}, false, "rename")
	if err != nil {
		t.Fatal(err)
	}
	if check(true) != rev {
		t.Fatal("cosmetic edit changed authority revision")
	}
	if _, err := s.GrantAuthorizationAssignment(ctx, identity.AccessAssignmentInput{RoleDefinitionID: role.ID, GroupID: group.ID, NamespaceID: authorizationCoreNamespace, Reason: "Duplicate grant"}, "duplicate"); identity.ErrorCodeOf(err) != identity.CodeConflict {
		t.Fatal("duplicate group assignment", err)
	}
	if _, err := s.SaveRoleDefinition(ctx, role.ID, role.Revision, identity.RoleDefinitionInput{Name: role.Name, Permissions: []identity.Permission{"audit.read"}, Reason: "Incompatible scope edit"}, false, "bad-scope"); identity.ErrorCodeOf(err) != identity.CodeConflict {
		t.Fatal("scope edit accepted", err)
	}
	if _, err := s.SaveAccessGroup(ctx, group.ID, group.Revision, identity.GroupInput{Reason: "Archive assigned group"}, identity.GroupArchived, "bad-archive"); identity.ErrorCodeOf(err) != identity.CodeConflict {
		t.Fatal("assigned archive", err)
	}
	group, err = s.SaveAccessGroup(ctx, group.ID, group.Revision, identity.GroupInput{Reason: "Suspend retained team"}, identity.GroupSuspended, "suspend")
	if err != nil {
		t.Fatal(err)
	}
	check(true)
	group, err = s.SaveAccessGroup(ctx, group.ID, group.Revision, identity.GroupInput{Reason: "Reactivate retained team"}, identity.GroupActive, "reactivate")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.RevokeAuthorizationAssignment(ctx, da.ID, da.Revision, "Remove direct path", "revoke"); err != nil {
		t.Fatal(err)
	}
	check(true)
	if _, err := s.ChangeAccessGroupMember(ctx, group.ID, gm.ID, "", gm.Revision, "Remove group member", "remove", true); err != nil {
		t.Fatal(err)
	}
	// Alice still has the fixture's built-in reader. Replace that final path
	// separately; removing a custom/group path must not revoke unrelated access.
	legacy, err := s.ListRoleAssignments(ctx, identity.RoleAssignmentListRequest{Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range legacy {
		if v.MembershipID == alice.MembershipID {
			if _, err := s.RevokeRole(ctx, v.ID, "Remove final legacy path", "revoke", v.Revision); err != nil {
				t.Fatal(err)
			}
		}
	}
	check(false)
	_, err = s.ChangeAccessGroupMember(ctx, group.ID, "", alice.MembershipID, 0, "Restore with new stable relation", "readd", false)
	if err != nil {
		t.Fatal(err)
	}
	check(true)
	outcome, err := s.OffboardMembership(ctx, alice.MembershipID, "Terminal offboarding", "offboard", 1)
	if err != nil || outcome.RevokedGroupMemberships != 1 {
		t.Fatalf("offboard=%+v %v", outcome, err)
	}
	check(false)
	if _, err := s.RevokeAuthorizationAssignment(ctx, ga.ID, ga.Revision, "Revoke unused group assignment", "revoke"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SaveAccessGroup(ctx, group.ID, group.Revision, identity.GroupInput{Reason: "Archive unused group"}, identity.GroupArchived, "archive"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SaveRoleDefinition(ctx, role.ID, role.Revision, identity.RoleDefinitionInput{Reason: "Archive unused definition"}, true, "archive"); err != nil {
		t.Fatal(err)
	}
	var audits, outbox int
	if err := db.operator.QueryRow(t.Context(), `SELECT count(*) FROM audit_events WHERE tenant_id=$1 AND action LIKE 'authorization.%'`, admin.TenantID).Scan(&audits); err != nil {
		t.Fatal(err)
	}
	if err := db.operator.QueryRow(t.Context(), `SELECT count(*) FROM outbox_events WHERE tenant_id=$1 AND event_type LIKE 'authorization.%'`, admin.TenantID).Scan(&outbox); err != nil {
		t.Fatal(err)
	}
	if audits < 10 || outbox != 0 {
		t.Fatalf("private audit-only events audit=%d outbox=%d", audits, outbox)
	}
}

func TestPostgresAuthorizationAdministrationSchemaAndRollback(t *testing.T) {
	db, store, _, admin, alice, seeded := authorizationCoreFixture(t)
	// This tests atomicity and cursor invalidation, not elapsed wall time.
	// Use a clock after the retained seed, as in the lifecycle controls above.
	s, err := identity.NewService(identity.ServiceOptions{Store: store, Clock: clock.NewFake(seeded.GrantedAt.Add(time.Second)), IDs: id.Generator{}, CursorKey: []byte("01234567890123456789012345678901")})
	if err != nil {
		t.Fatal(err)
	}
	ctx := identity.WithActor(t.Context(), admin)
	role, err := s.SaveRoleDefinition(ctx, "", 0, identity.RoleDefinitionInput{Name: "Atomic role", Permissions: []identity.Permission{"resource.read"}, Reason: "Create atomicity control"}, false, "role")
	if err != nil {
		t.Fatal(err)
	}
	before, err := store.CheckPermissions(t.Context(), admin, []identity.PermissionCheck{{Permission: "access.manage"}})
	if err != nil {
		t.Fatal(err)
	}
	// A correct current administrator cannot write without exactly bound audit.
	_, err = store.SaveDefinition(ctx, identity.SaveDefinitionParams{AdministrationChange: identity.AdministrationChange{Actor: admin, ID: role.ID, ExpectedRevision: role.Revision, At: role.UpdatedAt}, Input: identity.RoleDefinitionInput{Name: "Must roll back", Permissions: []identity.Permission{"namespace.read"}}})
	if err == nil {
		t.Fatal("unaudited role edit accepted")
	}
	current, err := s.GetRoleDefinition(ctx, role.ID)
	if err != nil || current.Revision != role.Revision || !reflect.DeepEqual(current.Permissions, role.Permissions) {
		t.Fatal("failed transaction changed retained role", err)
	}
	after, err := store.CheckPermissions(t.Context(), admin, []identity.PermissionCheck{{Permission: "access.manage"}})
	if err != nil || after.Revision != before.Revision {
		t.Fatal("rollback changed authority revision", err)
	}
	page, err := s.ListRoleDefinitions(ctx, 1, "")
	if err != nil || page.NextCursor == "" {
		t.Fatal("page missing continuation", err)
	}
	if _, err := s.ListRoleDefinitions(ctx, 1, page.NextCursor); err != nil {
		t.Fatal(err)
	}
	own, err := s.CurrentAccessAssignments(identity.WithActor(t.Context(), alice), 1, "")
	if err != nil || len(own.Items) != 1 || own.Items[0].Role.Key != "reader" {
		t.Fatalf("own summary=%+v %v", own, err)
	}
	if _, err := s.ListRoleDefinitions(identity.WithActor(t.Context(), alice), 10, ""); identity.ErrorCodeOf(err) != identity.CodeForbidden {
		t.Fatal("non-admin graph access", err)
	}
	if _, err := s.SaveRoleDefinition(ctx, role.ID, role.Revision, identity.RoleDefinitionInput{Name: role.Name, Permissions: []identity.Permission{"namespace.read"}, Reason: "Advance authority"}, false, "edit"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ListRoleDefinitions(ctx, 1, page.NextCursor); err == nil {
		t.Fatal("stale cursor accepted")
	}
	tx, err := db.operator.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(t.Context()) }()
	schema, _, err := currentCatalogSchema(t.Context(), tx)
	if err != nil {
		t.Fatal(err)
	}
	if err := verifyRestoreConstraints(t.Context(), tx, schema); err != nil {
		t.Error(err)
	}
	if err := verifyRestoreTriggers(t.Context(), tx, schema); err != nil {
		t.Error(err)
	}
	// Exact API privileges deliberately exclude built-in identity and group
	// deletion. RLS independently denies another tenant even to an API session.
	for _, query := range []string{`UPDATE role_definitions SET builtin=false WHERE tenant_id='` + admin.TenantID + `'`, `DELETE FROM access_groups WHERE tenant_id='` + admin.TenantID + `'`} {
		_, err := db.api.Exec(ctx, query)
		if err == nil || !strings.Contains(err.Error(), "permission denied") {
			t.Fatalf("exact privilege denial %q: %v", query, err)
		}
	}
}

func TestPostgresAuthorizationAdministrationRealmIsolationAndSchemaGuards(t *testing.T) {
	db, store, s, admin, alice, legacyAssignment := authorizationCoreFixture(t)
	ctx := identity.WithActor(t.Context(), admin)
	_, err := ProvisionIdentity(t.Context(), db.operator, IdentityProvisioningInput{TenantID: admin.TenantID, TenantDisplayName: "Housekeeping tenant A", Issuer: "https://second.realm.example", TenancyMode: "dedicated", Subject: "other-admin", ActorType: identity.ActorHuman, Roles: []identity.Role{identity.RoleTenantAdmin}, Operator: "qualification", Reason: "Create isolated second realm"})
	if err != nil {
		t.Fatal(err)
	}
	other, err := store.Resolve(t.Context(), identity.Principal{TenantID: admin.TenantID, Issuer: "https://second.realm.example", Subject: "other-admin"})
	if err != nil {
		t.Fatal(err)
	}
	otherCtx := identity.WithActor(t.Context(), other)
	role, err := s.SaveRoleDefinition(ctx, "", 0, identity.RoleDefinitionInput{Name: "Reusable tenant definition", Permissions: []identity.Permission{"resource.read"}, Reason: "Create common definition"}, false, "role")
	if err != nil {
		t.Fatal(err)
	}
	group, err := s.SaveAccessGroup(ctx, "", 0, identity.GroupInput{Name: "Realm one", Reason: "Create local realm group"}, "", "group")
	if err != nil {
		t.Fatal(err)
	}
	foreign, err := s.SaveAccessGroup(otherCtx, "", 0, identity.GroupInput{Name: "Realm two", Reason: "Create other realm group"}, "", "group")
	if err != nil {
		t.Fatal(err)
	}
	for _, control := range []struct {
		actor    identity.Actor
		expected string
	}{{admin, group.ID}, {other, foreign.ID}} {
		page, err := s.ListAccessGroups(identity.WithActor(t.Context(), control.actor), 100, "")
		if err != nil || len(page.Items) != 1 || page.Items[0].ID != control.expected || page.NextCursor != "" {
			t.Fatal("group directory crossed the current realm", err)
		}
	}
	if _, err := s.GetAccessGroup(ctx, foreign.ID); identity.ErrorCodeOf(err) != identity.CodeNotFound {
		t.Fatal("foreign realm group disclosed", err)
	}
	if _, err := s.ChangeAccessGroupMember(ctx, group.ID, "", other.MembershipID, 0, "Reject cross-realm member", "member", false); identity.ErrorCodeOf(err) != identity.CodeNotFound {
		t.Fatal("foreign member admitted", err)
	}
	if _, err := s.GrantAuthorizationAssignment(ctx, identity.AccessAssignmentInput{RoleDefinitionID: role.ID, MembershipID: other.MembershipID, Reason: "Reject arbitrary realm target"}, "grant"); identity.ErrorCodeOf(err) != identity.CodeNotFound {
		t.Fatal("foreign membership grant", err)
	}
	assigned, err := s.GrantAuthorizationAssignment(otherCtx, identity.AccessAssignmentInput{RoleDefinitionID: role.ID, GroupID: foreign.ID, Reason: "Reuse role in own realm"}, "grant")
	if err != nil {
		t.Fatal(err)
	}
	impact, err := s.ListAuthorizationAssignments(ctx, identity.AuthorizationPageRequest{RoleDefinitionID: role.ID}, 100, "")
	if err != nil || len(impact.Items) != 1 || impact.Items[0].ID != assigned.ID {
		t.Fatal("bounded tenant role impact omitted other realm", err)
	}
	// Actual API-role RLS, not a service-side filtering substitute.
	tx := trialBegin(t, db.api, housekeepingTenantB, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	var count int
	for _, table := range []string{"access_groups", "access_group_members", "role_definitions"} {
		if err := tx.QueryRow(t.Context(), `SELECT count(*) FROM `+table+` WHERE tenant_id=$1`, admin.TenantID).Scan(&count); err != nil || count != 0 {
			t.Fatal("cross-tenant visibility", table, count, err)
		}
	}
	_ = tx.Rollback(t.Context())
	all, err := s.ListRoleDefinitions(ctx, 100, "")
	if err != nil {
		t.Fatal(err)
	}
	var adminRole string
	for _, v := range all.Items {
		if v.Key == "tenant_admin" {
			adminRole = v.ID
		}
	}
	if adminRole == "" {
		t.Fatal("missing protected builtin")
	}
	trials := []struct {
		name, sql string
		args      []any
	}{
		{"protected group admin", `INSERT INTO role_assignments(id,tenant_id,role_definition_id,group_id,realm_id,role,granted_by,grant_reason,granted_at) VALUES(gen_random_uuid(),$1,$2,$3,$4,'','qualification','Reject group administrator',clock_timestamp())`, []any{admin.TenantID, adminRole, group.ID, admin.RealmID}},
		{"cross-realm FK", `INSERT INTO access_group_members(id,tenant_id,realm_id,group_id,membership_id,added_by,add_reason,added_at) VALUES(gen_random_uuid(),$1,$2,$3,$4,'qualification','Reject foreign member',clock_timestamp())`, []any{admin.TenantID, admin.RealmID, group.ID, other.MembershipID}},
		{"protected permission", `INSERT INTO role_permissions(tenant_id,role_definition_id,permission) VALUES($1,$2,'access.manage')`, []any{admin.TenantID, role.ID}},
		{"missing companions", `INSERT INTO role_permissions(tenant_id,role_definition_id,permission) VALUES($1,$2,'resource.edit')`, []any{admin.TenantID, role.ID}},
		{"empty final set", `DELETE FROM role_permissions WHERE tenant_id=$1 AND role_definition_id=$2`, []any{admin.TenantID, role.ID}},
		{"built-in deletion", `DELETE FROM role_permissions WHERE tenant_id=$1 AND role_definition_id=$2`, []any{admin.TenantID, adminRole}},
		{"terminal member missing attribution", `INSERT INTO access_group_members(id,tenant_id,realm_id,group_id,membership_id,state,added_by,add_reason,added_at) VALUES(gen_random_uuid(),$1,$2,$3,$4,'revoked','qualification','Reject invalid history',clock_timestamp())`, []any{admin.TenantID, admin.RealmID, group.ID, alice.MembershipID}},
	}
	for _, trial := range trials {
		t.Run(trial.name, func(t *testing.T) {
			tx := trialBegin(t, db.api, admin.TenantID, pgx.TxOptions{})
			_, err := tx.Exec(t.Context(), trial.sql, trial.args...)
			if err == nil {
				err = tx.Commit(t.Context())
			}
			var pgerr *pgconn.PgError
			if !errors.As(err, &pgerr) || pgerr.Code != "23514" && !(trial.name == "terminal member missing attribution" && pgerr.Code == "42501") {
				t.Fatalf("schema guard did not reject trial with constraint failure: %v", err)
			}
		})
	}
	// Shared permission admission must fence a real group removal through the
	// response-commit boundary, then deny that exact group path on a new request.
	gm, err := s.ChangeAccessGroupMember(ctx, group.ID, "", alice.MembershipID, 0, "Create fenced member path", "add", false)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.GrantAuthorizationAssignment(ctx, identity.AccessAssignmentInput{RoleDefinitionID: role.ID, GroupID: group.ID, NamespaceID: authorizationCoreNamespace, Reason: "Create fenced scoped path"}, "grant")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.RevokeRole(ctx, legacyAssignment.ID, "Remove direct control before admission", "control", legacyAssignment.Revision); err != nil {
		t.Fatal(err)
	}

	conn, err := db.api.Acquire(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Release()
	command := &commandTransaction{pool: db.api, conn: conn, tenantID: admin.TenantID}
	admitted := withCommandTransaction(t.Context(), command)
	checks := []identity.PermissionCheck{{Permission: "resource.read", NamespaceID: authorizationCoreNamespace}}
	if err := store.WithPermissionAdmission(admitted, alice, checks, func(context.Context) error { return nil }); err != nil {
		t.Fatal(err)
	}
	writerConn, err := db.api.Acquire(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer writerConn.Release()
	writer := &commandTransaction{pool: db.api, conn: writerConn, tenantID: admin.TenantID}
	result := trialAsync(t, func(ctx context.Context) error {
		writerCtx := identity.WithActor(withCommandTransaction(ctx, writer), admin)
		_, err := s.ChangeAccessGroupMember(writerCtx, group.ID, gm.ID, "", gm.Revision, "Serialize actual group removal", "remove", true)
		if err != nil {
			return err
		}
		return writer.complete(ctx, 200, func(context.Context, pgx.Tx) error { return nil })
	})
	trialWaitBlocked(t, db.operator, writerConn.Conn().PgConn().PID(), conn.Conn().PgConn().PID())
	if err := command.complete(admitted, 200, func(context.Context, pgx.Tx) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if err := <-result; err != nil {
		t.Fatal(err)
	}
	called := false
	if err := store.WithPermissionAdmission(t.Context(), alice, checks, func(context.Context) error { called = true; return nil }); identity.ErrorCodeOf(err) != identity.CodeForbidden || called {
		t.Fatal("revoked group still admitted", err)
	}

	// The revoked stable group relation remains available as private history.
	members, err := s.ListAccessGroupMembers(ctx, group.ID, 100, "")
	if err != nil || len(members.Items) != 1 || members.Items[0].State != identity.AssignmentRevoked {
		t.Fatal("group revocation not committed", err)
	}
}

func TestPostgresAuthorizationAdministrationDirectoryAndFinalAdministrator(t *testing.T) {
	db, store, _, admin, alice, seed := authorizationCoreFixture(t)
	testClock := clock.NewFake(seed.GrantedAt.Add(time.Second))
	service, err := identity.NewService(identity.ServiceOptions{Store: store, Clock: testClock, IDs: id.Generator{}, CursorKey: []byte("01234567890123456789012345678901")})
	if err != nil {
		t.Fatal(err)
	}
	ctx := identity.WithActor(t.Context(), admin)
	expected := make([]string, 0, 2)
	for _, name := range []string{"First team", "Second team"} {
		group, err := service.SaveAccessGroup(ctx, "", 0, identity.GroupInput{Name: name, Reason: "Qualify bounded group directory"}, "", "directory")
		if err != nil {
			t.Fatal(err)
		}
		expected = append(expected, group.ID)
	}
	first, err := service.ListAccessGroups(ctx, 1, "")
	if err != nil || len(first.Items) != 1 || first.NextCursor == "" {
		t.Fatal("directory first page", err)
	}
	second, err := service.ListAccessGroups(ctx, 1, first.NextCursor)
	if err != nil || len(second.Items) != 1 || second.NextCursor != "" {
		t.Fatal("directory continuation", err)
	}
	actual := []string{first.Items[0].ID, second.Items[0].ID}
	slices.Sort(expected)
	slices.Sort(actual)
	if !slices.Equal(actual, expected) || first.Items[0].ID == second.Items[0].ID {
		t.Fatal("directory lost or duplicated a group")
	}
	definitions, err := service.ListRoleDefinitions(ctx, 100, "")
	if err != nil || definitions.NextCursor != "" {
		t.Fatal("built-in catalog", err)
	}
	var roleID string
	for _, definition := range definitions.Items {
		if definition.Key == "tenant_admin" {
			roleID = definition.ID
		}
	}
	if roleID == "" {
		t.Fatal("administrator template absent")
	}
	assignments, err := service.ListAuthorizationAssignments(ctx, identity.AuthorizationPageRequest{MembershipID: admin.MembershipID, RoleDefinitionID: roleID}, 100, "")
	if err != nil || len(assignments.Items) != 1 || assignments.NextCursor != "" {
		t.Fatal("original administrator assignment", err)
	}
	original := assignments.Items[0]
	assertRefusal := func(actor identity.Actor, assignment identity.AccessAssignment) {
		t.Helper()
		before, err := store.CheckPermissions(t.Context(), actor, []identity.PermissionCheck{{Permission: "access.manage"}})
		if err != nil || !before.Allowed[0] {
			t.Fatal("refusal principal was not an administrator", err)
		}
		var auditsBefore, auditsAfter int
		query := `SELECT count(*) FROM audit_events WHERE tenant_id=$1`
		if err := db.operator.QueryRow(t.Context(), query, admin.TenantID).Scan(&auditsBefore); err != nil {
			t.Fatal(err)
		}
		_, err = service.RevokeAuthorizationAssignment(identity.WithActor(t.Context(), actor), assignment.ID, assignment.Revision, "Must retain final active administrator", "last-admin")
		if identity.ErrorCodeOf(err) != identity.CodeSeparationOfDuties {
			t.Fatal("final administrator was revocable", err)
		}
		after, err := store.CheckPermissions(t.Context(), actor, []identity.PermissionCheck{{Permission: "access.manage"}})
		if err != nil || !reflect.DeepEqual(before, after) {
			t.Fatal("refused revocation changed current authority", err)
		}
		if err := db.operator.QueryRow(t.Context(), query, admin.TenantID).Scan(&auditsAfter); err != nil || auditsBefore != auditsAfter {
			t.Fatal("refused revocation appended audit", err)
		}
		retained, err := service.ListAuthorizationAssignments(identity.WithActor(t.Context(), actor), identity.AuthorizationPageRequest{MembershipID: actor.MembershipID, RoleDefinitionID: roleID}, 100, "")
		if err != nil || len(retained.Items) != 1 || retained.Items[0] != assignment {
			t.Fatal("refused revocation changed retained assignment", err)
		}
	}
	assertRefusal(admin, original)
	testClock.Advance(time.Second)
	replacement, err := service.GrantAuthorizationAssignment(ctx, identity.AccessAssignmentInput{MembershipID: alice.MembershipID, RoleDefinitionID: roleID, Reason: "Qualify explicit administrator handover"}, "second-admin")
	if err != nil {
		t.Fatal(err)
	}
	testClock.Advance(time.Second)
	revoked, err := service.RevokeAuthorizationAssignment(ctx, original.ID, original.Revision, "Complete explicit administrator handover", "handover")
	if err != nil || revoked.State != identity.AssignmentRevoked || revoked.Revision != original.Revision+1 {
		t.Fatal("two-administrator handover failed", err)
	}
	decision, err := store.CheckPermissions(t.Context(), admin, []identity.PermissionCheck{{Permission: "access.manage"}})
	if err != nil || decision.Allowed[0] {
		t.Fatal("former administrator retained authority", err)
	}
	assertRefusal(alice, replacement)
	var audits int
	if err := db.operator.QueryRow(t.Context(), `SELECT count(*) FROM audit_events WHERE tenant_id=$1 AND action='authorization.assignment.revoked' AND target_id=$2`, admin.TenantID, original.ID).Scan(&audits); err != nil || audits != 1 {
		t.Fatal("handover lacks singular audit", err)
	}
}
