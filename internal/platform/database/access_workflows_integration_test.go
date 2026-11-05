//go:build integration

// Composed access workflows use the exact API login and owned PostgreSQL policy.
package database

import (
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/clock"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/id"
)

func TestPostgresScopedReaderLibraryAndDisjointGroup(t *testing.T) {
	db, store, _, admin, alice, reader := authorizationCoreFixture(t)
	c := clock.NewFake(reader.GrantedAt.Add(time.Second))
	service, err := identity.NewService(identity.ServiceOptions{Store: store, Clock: c, IDs: id.Generator{}, CursorKey: []byte("01234567890123456789012345678901")})
	if err != nil {
		t.Fatal(err)
	}
	ctx := identity.WithActor(t.Context(), admin)
	const namespaceB = "00000000-0000-7000-8000-000000000811"
	if _, err := db.operator.Exec(t.Context(), `INSERT INTO namespaces(id,tenant_id,slug,display_name,created_at,updated_at,revision) VALUES($1,$2,'workflow-b','Workflow B',$3,$3,1)`, namespaceB, admin.TenantID, c.Now()); err != nil {
		t.Fatal(err)
	}
	library, err := service.ListRoleDefinitions(ctx, 1, "", identity.DirectoryFilter{Key: "library_member"})
	if err != nil || len(library.Items) != 1 {
		t.Fatal(library, err)
	}
	libraryGrant, err := service.GrantAuthorizationAssignment(ctx, identity.AccessAssignmentInput{RoleDefinitionID: library.Items[0].ID, MembershipID: alice.MembershipID, Reason: "Separate tenant library"}, "library")
	if err != nil {
		t.Fatal(err)
	}
	metadata, err := service.SaveRoleDefinition(ctx, "", 0, identity.RoleDefinitionInput{Name: "B metadata", Permissions: []identity.Permission{"resource.read"}, Reason: "No content access in B"}, false, "metadata")
	if err != nil {
		t.Fatal(err)
	}
	group, err := service.SaveAccessGroup(ctx, "", 0, identity.GroupInput{Name: "B readers", Reason: "Disjoint group path"}, "", "group")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.ChangeAccessGroupMember(ctx, group.ID, "", alice.MembershipID, 0, "Join B readers", "join", false); err != nil {
		t.Fatal(err)
	}
	groupGrant, err := service.GrantAuthorizationAssignment(ctx, identity.AccessAssignmentInput{RoleDefinitionID: metadata.ID, GroupID: group.ID, NamespaceID: namespaceB, Reason: "Read B metadata"}, "group-b")
	if err != nil {
		t.Fatal(err)
	}
	checks := []identity.PermissionCheck{{Permission: "resource.read", NamespaceID: authorizationCoreNamespace}, {Permission: "resource.read_content", NamespaceID: authorizationCoreNamespace}, {Permission: "resource.read", NamespaceID: namespaceB}, {Permission: "resource.read_content", NamespaceID: namespaceB}, {Permission: "library.own"}, {Permission: "resource.read"}, {Permission: "federation.discover"}}
	assertDecisions := func(want []bool) {
		t.Helper()
		got, err := store.CheckPermissions(t.Context(), alice, checks)
		if err != nil || !reflect.DeepEqual(got.Allowed, want) {
			t.Fatal("mixed SQL paths changed their exact authority", got, err, want)
		}
	}
	assertProjection := func(request identity.SelfAccessRequest, want identity.SelfAccessProjection) int64 {
		t.Helper()
		got, err := identity.CurrentSelfAccess(identity.WithActor(t.Context(), alice), request)
		if err != nil || got.AuthorizationRevision < 1 {
			t.Fatal("current mixed-scope SQL hints unavailable", got, err)
		}
		revision := got.AuthorizationRevision
		got.AuthorizationRevision = 0
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("mixed-scope SQL hints=%+v, want=%+v", got, want)
		}
		return revision
	}
	assertPaths := func(wantIDs ...string) {
		t.Helper()
		paths, err := service.CurrentAccessAssignments(identity.WithActor(t.Context(), alice), 100, "")
		if err != nil || paths.NextCursor != "" {
			t.Fatal("incomplete current mixed-scope SQL paths", paths, err)
		}
		ids := make([]string, 0, len(paths.Items))
		for _, path := range paths.Items {
			ids = append(ids, path.Assignment.ID)
			if path.Assignment.GroupID == group.ID && path.GroupName != group.Name {
				t.Fatal("surviving group caption changed", path)
			}
		}
		slices.Sort(ids)
		slices.Sort(wantIDs)
		if !slices.Equal(ids, wantIDs) {
			t.Fatal("current SQL paths do not match independent surviving grants", ids, wantIDs)
		}
	}
	readerPermissions := []identity.Permission{"library.own", "namespace.read", "ratings.own", "resource.read", "resource.read_content", "resource.read_history"}
	metadataPermissions := []identity.Permission{"library.own", "resource.read"}
	want := identity.SelfAccessProjection{Permissions: []identity.Permission{"library.own"}, AvailablePermissions: readerPermissions, NamespaceGrants: []identity.NamespacePermissionAccess{{NamespaceID: authorizationCoreNamespace, Permissions: readerPermissions}, {NamespaceID: namespaceB, Permissions: metadataPermissions}}}
	assertDecisions([]bool{true, true, true, false, true, false, false})
	initialRevision := assertProjection(identity.SelfAccessRequest{Limit: 100}, want)
	assertPaths(reader.ID, libraryGrant.ID, groupGrant.ID)
	// Real SQL must compute the any-scope union independently of this page,
	// while each namespace keeps only its own rights and tenant companions.
	for i, request := range []identity.SelfAccessRequest{{Limit: 1}, {Limit: 1, AfterNamespaceID: authorizationCoreNamespace}} {
		page := want
		page.NamespaceGrants = want.NamespaceGrants[i : i+1]
		if revision := assertProjection(request, page); revision != initialRevision {
			t.Fatal("SQL paging changed the policy revision", revision, initialRevision)
		}
	}
	if _, err := service.RevokeRole(ctx, reader.ID, "Remove A only", "revoke-a", reader.Revision); err != nil {
		t.Fatal(err)
	}
	assertDecisions([]bool{false, false, true, false, true, false, false})
	want.AvailablePermissions = metadataPermissions
	want.NamespaceGrants = want.NamespaceGrants[1:]
	afterReader := assertProjection(identity.SelfAccessRequest{Limit: 100}, want)
	if afterReader <= initialRevision {
		t.Fatal("Reader revocation did not advance SQL policy", initialRevision, afterReader)
	}
	assertPaths(libraryGrant.ID, groupGrant.ID)
	if _, err := service.SaveAccessGroup(ctx, group.ID, group.Revision, identity.GroupInput{Reason: "Suspend B only"}, identity.GroupSuspended, "suspend-b"); err != nil {
		t.Fatal(err)
	}
	assertDecisions([]bool{false, false, false, false, true, false, false})
	want.AvailablePermissions = []identity.Permission{"library.own"}
	want.NamespaceGrants = []identity.NamespacePermissionAccess{}
	afterGroup := assertProjection(identity.SelfAccessRequest{Limit: 100}, want)
	if afterGroup <= afterReader {
		t.Fatal("group suspension did not advance SQL policy", afterReader, afterGroup)
	}
	assertPaths(libraryGrant.ID)
	if _, err := service.RevokeAuthorizationAssignment(ctx, libraryGrant.ID, libraryGrant.Revision, "Remove the final specialist path", "revoke-library"); err != nil {
		t.Fatal(err)
	}
	assertDecisions([]bool{false, false, false, false, false, false, false})
	empty := identity.SelfAccessProjection{Permissions: []identity.Permission{}, AvailablePermissions: []identity.Permission{}, NamespaceGrants: []identity.NamespacePermissionAccess{}}
	if revision := assertProjection(identity.SelfAccessRequest{Limit: 100}, empty); revision <= afterGroup {
		t.Fatal("specialist revocation did not advance SQL policy", afterGroup, revision)
	}
	assertPaths()
}

func TestPostgresMembershipOnlyHumanAndServiceThenSpecialist(t *testing.T) {
	_, store, service, admin, _, _ := authorizationCoreFixture(t)
	ctx := identity.WithActor(t.Context(), admin)
	library, err := service.ListRoleDefinitions(ctx, 1, "", identity.DirectoryFilter{Key: "library_member"})
	if err != nil || len(library.Items) != 1 {
		t.Fatal(library, err)
	}
	for _, kind := range []identity.ActorType{identity.ActorHuman, identity.ActorService} {
		grant, err := service.GrantRole(ctx, identity.GrantRoleInput{Subject: "membership-only-" + string(kind), ActorType: kind, Role: identity.RoleServiceClient, Reason: "Start without product grants"}, "empty")
		if err != nil {
			t.Fatal(err)
		}
		actor, err := store.ResolvePrincipal(t.Context(), identity.Principal{TenantID: admin.TenantID, Issuer: "https://identity.core.example", Subject: grant.Subject})
		if err != nil || actor.ActorType != kind {
			t.Fatal(actor, err)
		}
		checks := []identity.PermissionCheck{{Permission: "library.own"}, {Permission: "resource.read"}, {Permission: "resource.read", NamespaceID: authorizationCoreNamespace}}
		before, err := store.CheckPermissions(t.Context(), actor, checks)
		if err != nil || !reflect.DeepEqual(before.Allowed, []bool{false, false, false}) {
			t.Fatal("empty membership supplied Reader", before, err)
		}
		if _, err := service.GrantAuthorizationAssignment(ctx, identity.AccessAssignmentInput{RoleDefinitionID: library.Items[0].ID, MembershipID: actor.MembershipID, Reason: "Add only the requested specialist permission"}, "specialist"); err != nil {
			t.Fatal(err)
		}
		after, err := store.CheckPermissions(t.Context(), actor, checks)
		if err != nil || !reflect.DeepEqual(after.Allowed, []bool{true, false, false}) {
			t.Fatal("specialist grant widened resource access", after, err)
		}
		// The permission exists for both identity types. Human-only library
		// eligibility is a separate domain check, covered by its owning suite.
		paths, err := service.MemberAccess(ctx, actor.MembershipID, 100, "")
		if err != nil || len(paths.Items) != 2 || paths.NextCursor != "" {
			t.Fatal("membership-only and specialist evidence", paths, err)
		}
		for _, path := range paths.Items {
			if path.Role.Key != "library_member" && path.Role.Key != "service_client" {
				t.Fatal("unexpected intermediate Reader/custom grant", path)
			}
		}
	}
}
