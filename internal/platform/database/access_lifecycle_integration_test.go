//go:build integration

package database

import (
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/adapter"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/clock"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/id"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/safeerr"
	"testing"
	"time"
)

func TestPostgresAccessLifecycleAssignmentAPIs(t *testing.T) {
	db, store, _, admin, alice, _ := authorizationCoreFixture(t)
	const second = "https://review.second.example"
	if _, err := ProvisionIdentity(t.Context(), db.operator, IdentityProvisioningInput{TenantID: admin.TenantID, TenantDisplayName: "Housekeeping tenant A", Issuer: second, TenancyMode: "dedicated", Subject: "second-admin", ActorType: identity.ActorHuman, Roles: []identity.Role{identity.RoleTenantAdmin}, Operator: "review", Reason: "Owned second issuer realm review"}); err != nil {
		t.Fatal(err)
	}
	other, err := store.ResolvePrincipal(t.Context(), identity.Principal{TenantID: admin.TenantID, Issuer: second, Subject: "second-admin"})
	if err != nil {
		t.Fatal(err)
	}
	var now time.Time
	if err := db.operator.QueryRow(t.Context(), `SELECT clock_timestamp()`).Scan(&now); err != nil {
		t.Fatal(err)
	}
	c := clock.NewFake(now.Add(time.Second))
	s, err := identity.NewService(identity.ServiceOptions{Store: store, Clock: c, IDs: id.Generator{}, CursorKey: []byte("01234567890123456789012345678901")})
	if err != nil {
		t.Fatal(err)
	}
	ctx := identity.WithActor(t.Context(), admin)
	foreignCtx := identity.WithActor(t.Context(), other)
	foreign, err := s.GrantRole(foreignCtx, identity.GrantRoleInput{Subject: "foreign-reader", ActorType: identity.ActorHuman, Role: identity.RoleReader, Reason: "Owned foreign reader control"}, "foreign-grant")
	if err != nil {
		t.Fatal(err)
	}
	_, modernErr := s.RevokeAuthorizationAssignment(ctx, foreign.ID, foreign.Revision, "Review foreign general assignment refusal", "modern-revoke")
	if identity.ErrorCodeOf(modernErr) != identity.CodeNotFound {
		t.Fatal("modern foreign negative control", modernErr)
	}
	old, err := s.RevokeRole(ctx, foreign.ID, "Compare named role realm boundary", "old-revoke", foreign.Revision)
	if identity.ErrorCodeOf(err) != identity.CodeNotFound {
		t.Fatal("named-role cross-realm refusal", err)
	}
	t.Logf("PostgreSQL: general foreign assignment revoke=not_found; named-role foreign revoke=%s", old.State)
	var readerID string
	if err := db.operator.QueryRow(t.Context(), `SELECT id::text FROM role_definitions WHERE tenant_id=$1 AND key='reader'`, admin.TenantID).Scan(&readerID); err != nil {
		t.Fatal(err)
	}
	direct, err := s.GrantAuthorizationAssignment(ctx, identity.AccessAssignmentInput{RoleDefinitionID: readerID, MembershipID: alice.MembershipID, Reason: "Create named Reader through general assignment API"}, "modern-reader")
	if err != nil {
		t.Fatal(err)
	}
	page, err := s.ListRoleAssignments(ctx, identity.RoleAssignmentListRequest{MembershipID: alice.MembershipID, Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	visible := false
	for _, a := range page {
		visible = visible || a.ID == direct.ID
	}
	if !visible {
		t.Fatal("PostgreSQL named-role projection omitted the modern Reader grant")
	}
	if _, err := s.RevokeRole(ctx, direct.ID, "Verify named Reader revocation across APIs", "reader-revoke", direct.Revision); err != nil {
		t.Fatal(err)
	}
	t.Log("PostgreSQL general-API Reader grant is visible and revocable through named-role API")
	custom, err := s.SaveRoleDefinition(ctx, "", 0, identity.RoleDefinitionInput{Name: "Owned metadata reader", Permissions: []identity.Permission{"resource.read"}, Reason: "Create owned custom role"}, false, "custom-role")
	if err != nil {
		t.Fatal(err)
	}
	customGrant, err := s.GrantAuthorizationAssignment(ctx, identity.AccessAssignmentInput{RoleDefinitionID: custom.ID, MembershipID: alice.MembershipID, Reason: "Create owned custom grant"}, "custom-grant")
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.RevokeRole(ctx, customGrant.ID, "Review unsupported named-role projection", "custom-old-revoke", customGrant.Revision)
	if identity.ErrorCodeOf(err) != identity.CodeNotFound {
		t.Fatal("unsupported custom assignment must be refused", err)
	}
	retained, e := s.ListAuthorizationAssignments(ctx, identity.AuthorizationPageRequest{MembershipID: alice.MembershipID}, 100, "")
	if e != nil {
		t.Fatal(e)
	}
	for _, a := range retained.Items {
		if a.ID == customGrant.ID && a.State != identity.AssignmentActive {
			t.Fatal("unsupported command mutated custom assignment")
		}
	}

}

func TestPostgresAccessLifecycleCreatorRealmCollision(t *testing.T) {
	db, store, _, admin, _, _ := authorizationCoreFixture(t)
	type person struct{ issuer, subject string }
	people := []person{{"https://identity.core.example", "collision"}, {"https://review.second.example", "collision"}, {"https://review.second.example", "different"}}
	actors := make([]identity.Actor, 0, 3)
	for _, p := range people {
		if _, err := ProvisionIdentity(t.Context(), db.operator, IdentityProvisioningInput{TenantID: admin.TenantID, TenantDisplayName: "Housekeeping tenant A", Issuer: p.issuer, TenancyMode: "dedicated", Subject: p.subject, ActorType: identity.ActorHuman, Roles: []identity.Role{identity.RoleContributor, identity.RoleReviewer}, Operator: "review", Reason: "Create owned distinct contributor"}); err != nil {
			t.Fatal(err)
		}
		a, err := store.ResolvePrincipal(t.Context(), identity.Principal{TenantID: admin.TenantID, Issuer: p.issuer, Subject: p.subject})
		if err != nil {
			t.Fatal(err)
		}
		actors = append(actors, a)
	}
	if actors[0].RealmID == actors[1].RealmID || actors[0].MembershipID == actors[1].MembershipID {
		t.Fatal("identity fixture collision")
	}
	var now time.Time
	if err := db.operator.QueryRow(t.Context(), `SELECT clock_timestamp()`).Scan(&now); err != nil {
		t.Fatal(err)
	}
	c := clock.NewFake(now.Add(time.Second))
	repo, err := NewPostgresRepository(db.api)
	if err != nil {
		t.Fatal(err)
	}
	s, err := catalog.NewService(catalog.Options{Repository: repo, Clock: c, IDs: id.Generator{}, Validator: adapter.NewRegistry(), CursorKey: []byte("01234567890123456789012345678901")})
	if err != nil {
		t.Fatal(err)
	}
	creator := identity.WithActor(t.Context(), actors[0])
	collision := identity.WithActor(t.Context(), actors[1])
	different := identity.WithActor(t.Context(), actors[2])
	create := func(name string) catalog.Resource {
		r, err := s.CreateResource(creator, catalog.CreateResourceInput{NamespaceID: authorizationCoreNamespace, Kind: catalog.KindAgent, CanonicalIdentifier: "urn:review:" + name})
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	own := create("same-owner")
	target := create("foreign-owner")
	if err := s.DeleteResource(creator, own.ID, "Verify legitimate creator deletion", own.Revision); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteResource(different, target.ID, "Verify different subject refusal", target.Revision); catalog.ErrorCodeOf(err) != catalog.CodeForbidden {
		t.Fatal("invalid different-subject control", err)
	}
	d, err := store.CheckPermissions(t.Context(), actors[1], []identity.PermissionCheck{{Permission: "access.manage"}})
	if err != nil || len(d.Allowed) != 1 || d.Allowed[0] {
		t.Fatal("collision actor unexpectedly has protected administration")
	}
	err = s.DeleteResource(collision, target.ID, "Review colliding issuer subject", target.Revision)
	var deleted bool
	if e := db.operator.QueryRow(t.Context(), `SELECT deleted_at IS NOT NULL FROM resources WHERE tenant_id=$1 AND id=$2`, admin.TenantID, target.ID).Scan(&deleted); e != nil {
		t.Fatal(e)
	}
	t.Logf("PostgreSQL: distinct issuer/realm/membership with equal subject, protected admin=false; delete error=%v; row soft-deleted=%t", err, deleted)
	if catalog.ErrorCodeOf(err) != catalog.CodeForbidden || deleted {
		t.Fatal("creator-only deletion accepted another realm's equal subject")
	}
	var success int
	if e := db.operator.QueryRow(t.Context(), `SELECT count(*) FROM audit_events WHERE tenant_id=$1 AND target_id=$2 AND action='resource.deleted'`, admin.TenantID, target.ID).Scan(&success); e != nil || success != 0 {
		t.Fatal("forbidden deletion evidence", success, e)
	}
	if e := db.operator.QueryRow(t.Context(), `SELECT count(*) FROM outbox_events WHERE tenant_id=$1 AND subject=$2 AND event_type='com.open.agentic.catalog.resource.deleted.v1'`, admin.TenantID, target.ID).Scan(&success); e != nil || success != 0 {
		t.Fatal("forbidden deletion outbox", success, e)
	}
	publisher, e := s.CreatePublisher(identity.WithActor(t.Context(), admin), catalog.CreatePublisherInput{DisplayName: "Review fixture publisher", Domains: []string{"review.example"}})
	if e != nil {
		t.Fatal(e)
	}
	r, e := s.CreateResource(creator, catalog.CreateResourceInput{NamespaceID: authorizationCoreNamespace, Kind: catalog.KindSkill, CanonicalIdentifier: "urn:review:independent-skill"})
	if e != nil {
		t.Fatal(e)
	}
	_, rev, _, e := s.CreateVersion(creator, r.ID, catalog.CreateVersionInput{Version: "1.0.0", Representations: []catalog.RepresentationInput{{Role: catalog.RepresentationPrimary, MediaType: adapter.MediaSkillMarkdown, Content: "---\nname: independent-review\ndescription: Inspect catalog metadata.\n---\n# Instructions\nRead metadata only.\n"}}})
	if e != nil {
		t.Fatal(e)
	}
	listing, e := s.CreateListing(creator, catalog.CreateListingInput{NamespaceID: authorizationCoreNamespace, ResourceID: r.ID})
	if e != nil {
		t.Fatal(e)
	}
	listing, _, e = s.CreateListingRevision(creator, listing.ID, catalog.CreateListingRevisionInput{VersionRevisionID: rev.ID, DisplayName: "Independent review", Description: "Distinct issuer with equal subject", PublisherID: publisher.ID, Visibility: catalog.VisibilityTenant}, listing.Revision)
	if e != nil {
		t.Fatal(e)
	}
	submitted, e := s.Submit(creator, listing.ID)
	if e != nil {
		t.Fatal(e)
	}
	if _, _, e = s.DecideSubmission(creator, submitted.ID, "approve", "Genuine self-review control", submitted.Revision); catalog.ErrorCodeOf(e) != catalog.CodeSeparationOfDuties {
		t.Fatal("genuine self-review admitted", e)
	}
	if _, _, e = s.DecideSubmission(collision, submitted.ID, "approve", "Independent issuer review", submitted.Revision); e != nil {
		t.Fatal("unrelated equal subject was refused", e)
	}

}

func TestPostgresNamedProjectionQualifiesAllTemplates(t *testing.T) {
	db, _, s, admin, alice, _ := authorizationCoreFixture(t)
	ctx := identity.WithActor(t.Context(), admin)
	defs, err := s.ListRoleDefinitions(ctx, 100, "")
	if err != nil || len(defs.Items) != 14 {
		t.Fatal(defs, err)
	}
	for _, d := range defs.Items {
		t.Run(d.Key, func(t *testing.T) {
			a, e := s.GrantAuthorizationAssignment(ctx, identity.AccessAssignmentInput{RoleDefinitionID: d.ID, MembershipID: alice.MembershipID, Reason: "Qualify exact template projection"}, "grant-"+d.Key)
			if e != nil {
				t.Fatal(e)
			}
			listed, e := s.ListRoleAssignments(ctx, identity.RoleAssignmentListRequest{MembershipID: alice.MembershipID, Limit: 100})
			if e != nil {
				t.Fatal(e)
			}
			found := false
			for _, item := range listed {
				found = found || item.ID == a.ID
			}
			eligible := false
			switch d.Key {
			case "reader", "contributor", "reviewer", "publisher", "source_admin", "security_reviewer", "tenant_admin", "service_client":
				eligible = true
			}
			v, e := s.RevokeRole(ctx, a.ID, "Qualify bounded named role projection", "revoke-"+d.Key, a.Revision)
			if eligible {
				if !found || e != nil || v.State != identity.AssignmentRevoked {
					t.Fatal(found, v, e)
				}
			} else {
				if found || identity.ErrorCodeOf(e) != identity.CodeNotFound {
					t.Fatal("unsupported named target", found, v, e)
				}
				if _, e = s.RevokeAuthorizationAssignment(ctx, a.ID, a.Revision, "Use general API for specialist template", "general-"+d.Key); e != nil {
					t.Fatalf("general specialist revocation failed at %s: %v", safeerr.Stage(e), e)
				}
			}
		})
	}
	group, err := s.SaveAccessGroup(ctx, "", 0, identity.GroupInput{Name: "Named projection group", Reason: "Create group-target control"}, "", "group")
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range defs.Items {
		if d.Key != "reader" {
			continue
		}
		a, err := s.GrantAuthorizationAssignment(ctx, identity.AccessAssignmentInput{RoleDefinitionID: d.ID, GroupID: group.ID, Reason: "Qualify group target"}, "group-grant")
		if err != nil {
			t.Fatal(err)
		}
		if _, err = s.RevokeRole(ctx, a.ID, "Do not revoke group through named API", "group-named", a.Revision); identity.ErrorCodeOf(err) != identity.CodeNotFound {
			t.Fatal(err)
		}
		var active bool
		if err = db.operator.QueryRow(t.Context(), `SELECT state='active' FROM role_assignments WHERE tenant_id=$1 AND id=$2`, admin.TenantID, a.ID).Scan(&active); err != nil || !active {
			t.Fatal("unsupported group target mutated", active, err)
		}
		if _, err = s.RevokeAuthorizationAssignment(ctx, a.ID, a.Revision, "Use general API for group assignment", "group-general"); err != nil {
			t.Fatal(err)
		}
	}
}
