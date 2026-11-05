package identity

import (
	"context"
	"reflect"
	"slices"
	"testing"
)

func TestSelfPermissionHintsAreScopedCurrentAndDetached(t *testing.T) {
	store, service, admin, alice, _ := administrationFixture(t)
	ctx := WithActor(t.Context(), admin)
	role, err := service.SaveRoleDefinition(ctx, "", 0, RoleDefinitionInput{Name: "Metadata browser", Permissions: []Permission{"namespace.read", "resource.read"}, Reason: "Qualify custom scoped hints"}, false, "role")
	if err != nil {
		t.Fatal(err)
	}
	group, err := service.SaveAccessGroup(ctx, "", 0, GroupInput{Name: "Metadata readers", Reason: "Qualify grouped access"}, "", "group")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = service.ChangeAccessGroupMember(ctx, group.ID, "", alice.MembershipID, 0, "Explicit group membership", "member", false); err != nil {
		t.Fatal(err)
	}
	for _, namespace := range []string{administrationNamespace, administrationOtherNamespace} {
		if _, err = service.GrantAuthorizationAssignment(ctx, AccessAssignmentInput{RoleDefinitionID: role.ID, GroupID: group.ID, NamespaceID: namespace, Reason: "Grant a known namespace"}, "grant"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = service.GrantAuthorizationAssignment(ctx, AccessAssignmentInput{RoleDefinitionID: role.ID, MembershipID: alice.MembershipID, NamespaceID: administrationNamespace, Reason: "Overlapping direct path"}, "direct"); err != nil {
		t.Fatal(err)
	}
	read := func(request SelfAccessRequest) SelfAccessProjection {
		t.Helper()
		projection, err := CurrentSelfAccess(WithActor(t.Context(), alice), request)
		if err != nil {
			t.Fatal(err)
		}
		return projection
	}
	expected := []Permission{"namespace.read", "resource.read"}
	first := read(SelfAccessRequest{Limit: 1})
	if len(first.Permissions) != 0 || !reflect.DeepEqual(first.AvailablePermissions, expected) || len(first.NamespaceGrants) != 1 || first.NamespaceGrants[0].NamespaceID != administrationNamespace || !reflect.DeepEqual(first.NamespaceGrants[0].Permissions, expected) {
		t.Fatal(first)
	}
	first.NamespaceGrants[0].Permissions[0] = "access.manage"
	next := read(SelfAccessRequest{Limit: 1, AfterNamespaceID: administrationNamespace})
	if len(next.NamespaceGrants) != 1 || next.NamespaceGrants[0].NamespaceID != administrationOtherNamespace || !reflect.DeepEqual(next.NamespaceGrants[0].Permissions, expected) {
		t.Fatal(next)
	}
	exact := read(SelfAccessRequest{Limit: 100, NamespaceID: administrationOtherNamespace})
	if len(exact.NamespaceGrants) != 1 || len(read(SelfAccessRequest{Limit: 1, NamespaceID: "00000000-0000-7000-8000-000000000999"}).NamespaceGrants) != 0 {
		t.Fatal("exact lookup escaped caller scopes")
	}
	if _, err = service.SaveAccessGroup(ctx, group.ID, group.Revision, GroupInput{Reason: "Suspend the grouped path"}, GroupSuspended, "suspend"); err != nil {
		t.Fatal(err)
	}
	fresh := read(SelfAccessRequest{Limit: 100})
	if fresh.AuthorizationRevision <= first.AuthorizationRevision || len(fresh.NamespaceGrants) != 1 || fresh.NamespaceGrants[0].NamespaceID != administrationNamespace {
		t.Fatal("revocation or direct-path preservation failed", fresh)
	}
	if _, err := store.ReadSelfAccess(t.Context(), alice, SelfAccessRequest{Limit: 1, AuthorizationRevision: first.AuthorizationRevision}); ErrorCodeOf(err) != CodeInvalidArgument {
		t.Fatal("stale projection accepted", err)
	}
	foreign := alice
	foreign.TenantID = "00000000-0000-7000-8000-000000000999"
	if _, err := store.ReadSelfAccess(t.Context(), foreign, SelfAccessRequest{Limit: 1, AuthorizationRevision: fresh.AuthorizationRevision}); ErrorCodeOf(err) != CodeForbidden {
		t.Fatal("cross-tenant principal accepted", err)
	}
	for _, request := range []SelfAccessRequest{{Limit: 0}, {Limit: 102}, {Limit: 1, NamespaceID: "invalid"}, {Limit: 1, AfterNamespaceID: "invalid"}} {
		if _, err := CurrentSelfAccess(WithActor(t.Context(), alice), request); ErrorCodeOf(err) != CodeInvalidArgument {
			t.Fatal("invalid request accepted", request, err)
		}
	}
	canceled, cancel := context.WithCancel(WithActor(t.Context(), alice))
	cancel()
	if _, err := CurrentSelfAccess(canceled, SelfAccessRequest{Limit: 1}); err == nil {
		t.Fatal("canceled read accepted")
	}
}

func TestSelfPermissionHintsGlobalUnionAndUnavailableStore(t *testing.T) {
	store, service, admin, alice, _ := administrationFixture(t)
	ctx := WithActor(t.Context(), admin)
	role, err := service.SaveRoleDefinition(ctx, "", 0, RoleDefinitionInput{Name: "Tenant metadata", Permissions: []Permission{"resource.read"}, Reason: "Finite global hint"}, false, "role")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = service.GrantAuthorizationAssignment(ctx, AccessAssignmentInput{RoleDefinitionID: role.ID, MembershipID: alice.MembershipID, Reason: "Tenant hint"}, "global"); err != nil {
		t.Fatal(err)
	}
	scoped, err := service.SaveRoleDefinition(ctx, "", 0, RoleDefinitionInput{Name: "Scoped namespace", Permissions: []Permission{"namespace.read"}, Reason: "Scope union"}, false, "role")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = service.GrantAuthorizationAssignment(ctx, AccessAssignmentInput{RoleDefinitionID: scoped.ID, MembershipID: alice.MembershipID, NamespaceID: administrationNamespace, Reason: "Scope union"}, "scoped"); err != nil {
		t.Fatal(err)
	}
	got, err := CurrentSelfAccess(WithActor(t.Context(), alice), SelfAccessRequest{Limit: 100})
	if err != nil || !reflect.DeepEqual(got.Permissions, []Permission{"resource.read"}) || !reflect.DeepEqual(got.NamespaceGrants[0].Permissions, []Permission{"namespace.read", "resource.read"}) {
		t.Fatal(got, err)
	}
	got.Permissions[0] = "access.manage"
	got.AvailablePermissions[0] = "access.manage"
	again, err := CurrentSelfAccess(WithActor(t.Context(), alice), SelfAccessRequest{Limit: 100})
	if err != nil || again.Permissions[0] != "resource.read" {
		t.Fatal("caller mutated retained permissions", err)
	}
	if _, err = service.GrantRole(ctx, GrantRoleInput{Subject: "empty-member", ActorType: ActorHuman, Role: RoleServiceClient, Reason: "No permission control"}, "empty"); err != nil {
		t.Fatal(err)
	}
	noGrant, err := store.ResolvePrincipal(t.Context(), Principal{Issuer: "issuer", TenantID: admin.TenantID, Subject: "empty-member"})
	if err != nil {
		t.Fatal(err)
	}
	none, err := CurrentSelfAccess(WithActor(t.Context(), noGrant), SelfAccessRequest{Limit: 100})
	if err != nil || none.Permissions == nil || none.AvailablePermissions == nil || none.NamespaceGrants == nil || len(none.AvailablePermissions) != 0 {
		t.Fatal("empty must remain a valid finite projection", none, err)
	}
	if _, err = CurrentSelfAccess(t.Context(), SelfAccessRequest{Limit: 1}); ErrorCodeOf(err) != CodeForbidden {
		t.Fatal("missing actor", err)
	}
	if err = ValidateSelfAccessRequest(SelfAccessRequest{Limit: 1}); ErrorCodeOf(err) != CodeInvalidArgument {
		t.Fatal("missing revision", err)
	}
	// A current evaluator without the explicitly implemented self-read port cannot supply hints.
	bound := BindPermissionStore(alice, &selfPermissionOnlyStore{MemoryStore: store})
	if _, err = CurrentSelfAccess(WithActor(t.Context(), bound), SelfAccessRequest{Limit: 1}); ErrorCodeOf(err) != CodeDependency {
		t.Fatal("unavailable port", err)
	}
}

func TestScopedReaderLibraryAndDisjointGroupRemainIndependent(t *testing.T) {
	store, service, admin, alice, _ := administrationFixture(t)
	ctx := WithActor(t.Context(), admin)
	reader, err := service.GrantRole(ctx, GrantRoleInput{Subject: alice.Subject, ActorType: ActorHuman, Role: RoleReader, NamespaceID: administrationNamespace, Reason: "Read namespace A only"}, "reader-a")
	if err != nil {
		t.Fatal(err)
	}
	library, err := service.ListRoleDefinitions(ctx, 1, "", DirectoryFilter{Key: "library_member"})
	if err != nil || len(library.Items) != 1 {
		t.Fatal(library, err)
	}
	libraryGrant, err := service.GrantAuthorizationAssignment(ctx, AccessAssignmentInput{RoleDefinitionID: library.Items[0].ID, MembershipID: alice.MembershipID, Reason: "Separate tenant library"}, "library")
	if err != nil {
		t.Fatal(err)
	}
	metadata := administrationRole(t, service, ctx, "resource.read")
	group, err := service.SaveAccessGroup(ctx, "", 0, GroupInput{Name: "Namespace B metadata", Reason: "Separate group path"}, "", "group")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.ChangeAccessGroupMember(ctx, group.ID, "", alice.MembershipID, 0, "Join metadata group", "join", false); err != nil {
		t.Fatal(err)
	}
	groupGrant, err := service.GrantAuthorizationAssignment(ctx, AccessAssignmentInput{RoleDefinitionID: metadata.ID, GroupID: group.ID, NamespaceID: administrationOtherNamespace, Reason: "Read metadata in B only"}, "group-b")
	if err != nil {
		t.Fatal(err)
	}
	checks := []PermissionCheck{{Permission: "resource.read", NamespaceID: administrationNamespace}, {Permission: "resource.read_content", NamespaceID: administrationNamespace}, {Permission: "resource.read", NamespaceID: administrationOtherNamespace}, {Permission: "resource.read_content", NamespaceID: administrationOtherNamespace}, {Permission: "library.own"}, {Permission: "resource.read"}, {Permission: "federation.discover"}}
	assertDecisions := func(want []bool) {
		t.Helper()
		got, err := store.CheckPermissions(t.Context(), alice, checks)
		if err != nil || !reflect.DeepEqual(got.Allowed, want) {
			t.Fatal("mixed paths changed their exact authority", got, err, want)
		}
	}
	assertProjection := func(request SelfAccessRequest, want SelfAccessProjection) int64 {
		t.Helper()
		got, err := CurrentSelfAccess(WithActor(t.Context(), alice), request)
		if err != nil || got.AuthorizationRevision < 1 {
			t.Fatal("current mixed-scope hints unavailable", got, err)
		}
		revision := got.AuthorizationRevision
		got.AuthorizationRevision = 0
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("mixed-scope hints=%+v, want=%+v", got, want)
		}
		return revision
	}
	assertPaths := func(wantIDs ...string) {
		t.Helper()
		paths, err := service.CurrentAccessAssignments(WithActor(t.Context(), alice), 100, "")
		if err != nil || paths.NextCursor != "" {
			t.Fatal("incomplete current mixed-scope paths", paths, err)
		}
		ids := make([]string, 0, len(paths.Items))
		for _, path := range paths.Items {
			ids = append(ids, path.Assignment.ID)
			if path.Assignment.GroupID == group.ID && path.GroupName != group.Name {
				t.Fatal("surviving group lost its caption", path)
			}
		}
		slices.Sort(ids)
		slices.Sort(wantIDs)
		if !slices.Equal(ids, wantIDs) {
			t.Fatal("current paths do not match independent surviving grants", ids, wantIDs)
		}
	}
	readerPermissions := []Permission{"library.own", "namespace.read", "ratings.own", "resource.read", "resource.read_content", "resource.read_history"}
	metadataPermissions := []Permission{"library.own", "resource.read"}
	want := SelfAccessProjection{Permissions: []Permission{"library.own"}, AvailablePermissions: readerPermissions, NamespaceGrants: []NamespacePermissionAccess{{NamespaceID: administrationNamespace, Permissions: readerPermissions}, {NamespaceID: administrationOtherNamespace, Permissions: metadataPermissions}}}
	assertDecisions([]bool{true, true, true, false, true, false, false})
	initialRevision := assertProjection(SelfAccessRequest{Limit: 100}, want)
	assertPaths(reader.ID, libraryGrant.ID, groupGrant.ID)
	// Navigation hints cover all effective scopes even when a page contains
	// only metadata authority; its exact namespace must not gain content reads.
	for i, request := range []SelfAccessRequest{{Limit: 1}, {Limit: 1, AfterNamespaceID: administrationNamespace}} {
		page := want
		page.NamespaceGrants = want.NamespaceGrants[i : i+1]
		if revision := assertProjection(request, page); revision != initialRevision {
			t.Fatal("paging changed the policy revision", revision, initialRevision)
		}
	}
	if _, err := service.RevokeRole(ctx, reader.ID, "Remove A without removing B or library", "revoke-a", reader.Revision); err != nil {
		t.Fatal(err)
	}
	assertDecisions([]bool{false, false, true, false, true, false, false})
	want.AvailablePermissions = metadataPermissions
	want.NamespaceGrants = want.NamespaceGrants[1:]
	afterReader := assertProjection(SelfAccessRequest{Limit: 100}, want)
	if afterReader <= initialRevision {
		t.Fatal("Reader revocation did not advance policy", initialRevision, afterReader)
	}
	assertPaths(libraryGrant.ID, groupGrant.ID)
	if _, err := service.SaveAccessGroup(ctx, group.ID, group.Revision, GroupInput{Reason: "Suspend B while retaining the library"}, GroupSuspended, "suspend-b"); err != nil {
		t.Fatal(err)
	}
	assertDecisions([]bool{false, false, false, false, true, false, false})
	want.AvailablePermissions = []Permission{"library.own"}
	want.NamespaceGrants = []NamespacePermissionAccess{}
	afterGroup := assertProjection(SelfAccessRequest{Limit: 100}, want)
	if afterGroup <= afterReader {
		t.Fatal("group suspension did not advance policy", afterReader, afterGroup)
	}
	assertPaths(libraryGrant.ID)
	if _, err := service.RevokeAuthorizationAssignment(ctx, libraryGrant.ID, libraryGrant.Revision, "Remove the final specialist path", "revoke-library"); err != nil {
		t.Fatal(err)
	}
	assertDecisions([]bool{false, false, false, false, false, false, false})
	empty := SelfAccessProjection{Permissions: []Permission{}, AvailablePermissions: []Permission{}, NamespaceGrants: []NamespacePermissionAccess{}}
	if revision := assertProjection(SelfAccessRequest{Limit: 100}, empty); revision <= afterGroup {
		t.Fatal("specialist revocation did not advance policy", afterGroup, revision)
	}
	assertPaths()
}

// Deliberately expose only the existing permission port, excluding the optional projection.
type selfPermissionOnlyStore struct{ MemoryStore PermissionStore }

func (s *selfPermissionOnlyStore) ResolvePrincipal(ctx context.Context, p Principal) (Actor, error) {
	return s.MemoryStore.ResolvePrincipal(ctx, p)
}
func (s *selfPermissionOnlyStore) CheckPermissions(ctx context.Context, a Actor, p []PermissionCheck) (PermissionDecision, error) {
	return s.MemoryStore.CheckPermissions(ctx, a, p)
}
func (s *selfPermissionOnlyStore) WithPermissionAdmission(ctx context.Context, a Actor, p []PermissionCheck, f func(context.Context) error) error {
	return s.MemoryStore.WithPermissionAdmission(ctx, a, p, f)
}
