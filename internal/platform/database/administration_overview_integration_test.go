//go:build integration

package database

import (
	"context"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/clock"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/id"
	"testing"
	"time"
)

func TestPostgresAdministrationOverviewRetainedStateAndBounds(t *testing.T) {
	db, store, _, admin, alice, seeded := authorizationCoreFixture(t)
	// Reuse the authorization fixture's fixed valid chronology. This tests retained metadata, not host-clock stability.
	s, err := identity.NewService(identity.ServiceOptions{Store: store, Clock: clock.NewFake(seeded.GrantedAt.Add(time.Second)), IDs: id.Generator{}, CursorKey: []byte("01234567890123456789012345678901")})
	if err != nil {
		t.Fatal(err)
	}
	ctx := identity.WithActor(t.Context(), admin)
	base, err := s.AdministrationOverview(ctx)
	if err != nil || base.Human.Total.Value != 2 || base.Service.Total.Value != 0 {
		t.Fatal(base, err)
	}
	role, err := s.SaveRoleDefinition(ctx, "", 0, identity.RoleDefinitionInput{Name: "Summary group reader", Permissions: []identity.Permission{"resource.read"}, Reason: "Qualify retained assignment counts"}, false, "overview-role")
	if err != nil {
		t.Fatal(err)
	}
	group, err := s.SaveAccessGroup(ctx, "", 0, identity.GroupInput{Name: "Summary group", Reason: "Qualify group counts"}, "", "overview-group")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.ChangeAccessGroupMember(ctx, group.ID, "", alice.MembershipID, 0, "Qualify an active group path", "overview-member", false); err != nil {
		t.Fatal(err)
	}
	if _, err = s.GrantAuthorizationAssignment(ctx, identity.AccessAssignmentInput{RoleDefinitionID: role.ID, GroupID: group.ID, Reason: "Qualify group assignment"}, "overview-assignment"); err != nil {
		t.Fatal(err)
	}
	decision, err := store.CheckPermissions(t.Context(), alice, []identity.PermissionCheck{{Permission: "resource.read"}})
	if err != nil || !decision.Allowed[0] {
		t.Fatal(decision, err)
	}
	before, err := s.AdministrationOverview(ctx)
	if err != nil || before.ActiveGroups.Value != 1 || before.ActiveAssignments.Value != base.ActiveAssignments.Value+1 {
		t.Fatal(before, err)
	}
	if _, err = s.SaveAccessGroup(ctx, group.ID, group.Revision, identity.GroupInput{Reason: "Suspend the group path"}, identity.GroupSuspended, "overview-suspend"); err != nil {
		t.Fatal(err)
	}
	after, err := s.AdministrationOverview(ctx)
	if err != nil || after.ActiveGroups.Value != 0 || after.ActiveAssignments != before.ActiveAssignments || after.ActiveRoles.Total.Value != base.ActiveRoles.Total.Value+1 || *after.ActiveRoles.Custom != 1 {
		t.Fatal(after, err)
	}
	decision, err = store.CheckPermissions(t.Context(), alice, []identity.PermissionCheck{{Permission: "resource.read"}})
	if err != nil || decision.Allowed[0] {
		t.Fatal("suspended group path remained effective", decision, err)
	}
	if _, err = s.AdministrationOverview(identity.WithActor(t.Context(), alice)); identity.ErrorCodeOf(err) != identity.CodeForbidden {
		t.Fatal(err)
	}
	secondary, err := ProvisionIdentity(t.Context(), db.operator, IdentityProvisioningInput{TenantID: admin.TenantID, TenantDisplayName: "Housekeeping tenant A", Issuer: "https://overview.secondary.example", TenancyMode: "dedicated", Subject: "service", ActorType: identity.ActorService, Roles: []identity.Role{identity.RoleServiceClient}, Operator: "overview", Reason: "Qualify a second realm"})
	if err != nil {
		t.Fatal(err)
	}
	// Another realm's active Group must not inflate the linked current-realm directory.
	if _, err = db.operator.Exec(t.Context(), `INSERT INTO access_groups(id,tenant_id,realm_id,name,description,state,revision,created_at,updated_at) VALUES(uuidv7(),$1,$2,'Other realm group','','active',1,$3,$3)`, admin.TenantID, secondary.RealmID, seeded.GrantedAt); err != nil {
		t.Fatal(err)
	}
	// Retain a legal realm transition at the fixture's recorded timestamp.
	if _, err = db.operator.Exec(t.Context(), `UPDATE tenant_identity_realms SET state='suspended',revision=revision+1 WHERE tenant_id=$1 AND id=$2`, admin.TenantID, secondary.RealmID); err != nil {
		t.Fatal(err)
	}
	after, err = s.AdministrationOverview(ctx)
	if err != nil || after.Human.Total.Value != 2 || after.Service.Total.Value != 1 || after.Service.States[0].Count != 1 || after.ActiveGroups.Value != 0 {
		t.Fatal(after, err)
	}
	other := postgresFixtureContext(t, db, t.Context(), identity.Actor{TenantID: housekeepingTenantB, Subject: "overview-admin", Roles: []identity.Role{identity.RoleTenantAdmin}})
	otherActor, _ := identity.FromContext(other)
	isolated, err := s.AdministrationOverview(other)
	if err != nil || isolated.Human.Total.Value != 1 || isolated.Service.Total.Value != 0 || isolated.ActiveGroups.Value != 0 || *isolated.ActiveRoles.Custom != 0 {
		t.Fatal(isolated, err)
	}
	spoof := otherActor
	spoof.TenantID = admin.TenantID
	if _, err = store.ReadAdministrationOverview(context.Background(), spoof); err == nil {
		t.Fatal("foreign principal read another tenant")
	}
	// Populate each independent measure beyond the selection ceiling. All writes stay in this owned fixture.
	tx, err := db.operator.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, err = tx.Exec(t.Context(), `INSERT INTO tenant_memberships(id,tenant_id,realm_id,subject,actor_type,state,revision,created_at,updated_at) SELECT uuidv7(),$1,$2,'overview-human-'||n,'human','active',1,$3,$3 FROM generate_series(1,10001) n`, admin.TenantID, admin.RealmID, seeded.GrantedAt); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(t.Context(), `INSERT INTO access_groups(id,tenant_id,realm_id,name,description,state,revision,created_at,updated_at) SELECT uuidv7(),$1,$2,'Overview scale '||n,'','active',1,$3,$3 FROM generate_series(1,10001) n`, admin.TenantID, admin.RealmID, seeded.GrantedAt); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(t.Context(), `WITH inserted AS (INSERT INTO role_definitions(id,tenant_id,key,builtin,name,description,state,revision,created_at,updated_at) SELECT uuidv7(),$1,NULL,false,'Overview scale '||n,'','active',1,$2,$2 FROM generate_series(1,10001) n RETURNING id) INSERT INTO role_permissions(tenant_id,role_definition_id,permission) SELECT $1,id,'resource.read' FROM inserted`, admin.TenantID, seeded.GrantedAt); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(t.Context(), `INSERT INTO role_assignments(id,tenant_id,realm_id,role_definition_id,membership_id,role,state,revision,granted_by,grant_reason,granted_at) SELECT uuidv7(),$1,$2,d.id,$3,'','active',1,'overview','Owned bounded summary fixture',$4 FROM role_definitions d WHERE d.tenant_id=$1 AND d.name LIKE 'Overview scale %'`, admin.TenantID, admin.RealmID, admin.MembershipID, seeded.GrantedAt); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	after, err = s.AdministrationOverview(ctx)
	if err != nil || !after.Human.Total.Limited || after.Human.Total.Value != 10000 || after.Human.States != nil || after.Service.Total.Value != 1 || after.Service.Total.Limited || !after.ActiveGroups.Limited || !after.ActiveRoles.Total.Limited || after.ActiveRoles.Builtin != nil || after.ActiveRoles.Custom != nil || !after.ActiveAssignments.Limited {
		t.Fatal(after, err)
	}
}
