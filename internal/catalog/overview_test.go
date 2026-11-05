package catalog

import (
	"context"
	"fmt"
	"reflect"
	"testing"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/test/identityfixture"
)

type failedOverviewRepository struct{ Repository }

func (failedOverviewRepository) ReadInventoryCounts(context.Context, string, InventoryMeasure) (InventoryCounts, error) {
	return InventoryCounts{}, E(CodeDependencyUnavailable, "controlled inventory failure")
}

func TestOverviewPreservesReadFailures(t *testing.T) {
	f := newGuardrailFixture(t)
	f.service.repo = failedOverviewRepository{Repository: f.repo}
	value, err := f.service.ResourceOverview(f.adminContext())
	if ErrorCodeOf(err) != CodeDependencyUnavailable || value.Kinds != nil {
		t.Fatal(value, err)
	}
	f.service.repo = struct{ Repository }{Repository: f.repo}
	if _, err = f.service.ResourceOverview(f.adminContext()); ErrorCodeOf(err) != CodeDependencyUnavailable {
		t.Fatal("missing summary support became a successful zero", err)
	}
}

func TestOverviewSelectsCurrentAuthorityWithoutJoinMultiplication(t *testing.T) {
	f, authority, owner, direct, grouped := collectionFixture(t)
	for name, ctx := range map[string]context.Context{"direct": direct, "group": grouped} {
		t.Run(name, func(t *testing.T) {
			resources, err := f.service.ResourceOverview(ctx)
			if err != nil || resources.Total != (BoundedCount{Value: 2}) || len(resources.Kinds) != 4 || resources.Kinds[0].Count != 2 || resources.TenantID != guardrailTenantID || !resources.ObservedAt.Equal(f.clock.Now()) {
				t.Fatal(resources, err)
			}
			releases, err := f.service.ReleaseOverview(ctx)
			if err != nil || releases.Published.Value != 2 || releases.Deprecated.Value != 0 {
				t.Fatal(releases, err)
			}
			namespaces, err := f.service.DirectoryOverview(ctx, InventoryNamespaces)
			if err != nil || namespaces.Count.Value != 1 {
				t.Fatal(namespaces, err)
			}
			if _, err = f.service.DirectoryOverview(ctx, InventoryPublishers); ErrorCodeOf(err) != CodeForbidden {
				t.Fatal("publisher summary disclosed", err)
			}
		})
	}
	// History access in another Namespace must not expand Resource access.
	actor, _ := identity.FromContext(direct)
	role, err := authority.SaveRoleDefinition(owner, "", 0, identity.RoleDefinitionInput{Name: "History only", Permissions: []identity.Permission{"resource.read_history"}, Reason: "Independent overview scope"}, false, "history-role")
	if err != nil {
		t.Fatal(err)
	}
	secondGroup, err := authority.SaveAccessGroup(owner, "", 0, identity.GroupInput{Name: "Overlapping history readers", Reason: "Qualify duplicate group paths"}, "", "second-group")
	if err != nil {
		t.Fatal(err)
	}
	groupActor, _ := identity.FromContext(grouped)
	if _, err := authority.ChangeAccessGroupMember(owner, secondGroup.ID, "", groupActor.MembershipID, 0, "Add overlapping group member", "second-member", false); err != nil {
		t.Fatal(err)
	}
	if _, err := authority.GrantAuthorizationAssignment(owner, identity.AccessAssignmentInput{RoleDefinitionID: role.ID, GroupID: secondGroup.ID, NamespaceID: collectionNamespace, Reason: "Overlap the first group"}, "second-group-grant"); err != nil {
		t.Fatal(err)
	}
	groupCounts, err := f.service.ReleaseOverview(grouped)
	if err != nil || groupCounts.Published.Value != 2 {
		t.Fatal("duplicate group paths multiplied releases", groupCounts, err)
	}
	if _, err = authority.GrantAuthorizationAssignment(owner, identity.AccessAssignmentInput{RoleDefinitionID: role.ID, MembershipID: actor.MembershipID, NamespaceID: collectionHiddenNamespace, Reason: "Read hidden history only"}, "history-assignment"); err != nil {
		t.Fatal(err)
	}
	resources, err := f.service.ResourceOverview(direct)
	if err != nil || resources.Total.Value != 2 {
		t.Fatal(resources, err)
	}
	releases, err := f.service.ReleaseOverview(direct)
	if err != nil || releases.Published.Value != 4 {
		t.Fatal(releases, err)
	}
	// A second path to the same history population must not duplicate counts.
	if _, err = authority.GrantAuthorizationAssignment(owner, identity.AccessAssignmentInput{RoleDefinitionID: role.ID, MembershipID: actor.MembershipID, NamespaceID: collectionNamespace, Reason: "Overlap an existing permission"}, "overlap-assignment"); err != nil {
		t.Fatal(err)
	}
	duplicate, err := f.service.ReleaseOverview(direct)
	if err != nil || !reflect.DeepEqual(duplicate, releases) {
		t.Fatal(duplicate, err)
	}
	// Only the current pointer counts. Old release rows and representations do not.
	for _, listing := range f.repo.listings {
		release := f.repo.releases[listing.CurrentReleaseID]
		historical := release
		historical.ID = "old-" + release.ID
		f.repo.releases[historical.ID] = historical
		release.State = ReleaseDeprecated
		f.repo.releases[release.ID] = release
		break
	}
	changed, err := f.service.ReleaseOverview(direct)
	if err != nil || changed.Published.Value != 3 || changed.Deprecated.Value != 1 {
		t.Fatal(changed, err)
	}
	for key, resource := range f.repo.resources {
		if resource.NamespaceID == collectionNamespace {
			at := f.clock.Now()
			resource.DeletedAt = &at
			f.repo.resources[key] = resource
			break
		}
	}
	after, err := f.service.ResourceOverview(direct)
	if err != nil || after.Total.Value != 1 {
		t.Fatal(after, err)
	}
	deletedRelease, err := f.service.ReleaseOverview(direct)
	if err != nil || deletedRelease.Published.Value+deletedRelease.Deprecated.Value != 3 {
		t.Fatal(deletedRelease, err)
	}
}

func TestOverviewEmptyTenantAndIndependentCeilings(t *testing.T) {
	f := newGuardrailFixture(t)
	empty, err := f.service.ResourceOverview(f.readerContext())
	if err != nil || empty.Total != (BoundedCount{}) || len(empty.Kinds) != 4 {
		t.Fatal(empty, err)
	}
	for i := 0; i <= OverviewSelectionLimit; i++ {
		key := fmt.Sprintf("item-%d", i)
		f.repo.resources[key] = Resource{ID: key, TenantID: guardrailTenantID, NamespaceID: "namespace", Kind: KindSkill}
		f.repo.namespaces[key] = Namespace{ID: key, TenantID: guardrailTenantID}
		f.repo.publishers[key] = Publisher{ID: key, TenantID: guardrailTenantID}
		f.repo.listings[key] = CatalogListing{ID: key, TenantID: guardrailTenantID, NamespaceID: "namespace", ResourceID: key, CurrentReleaseID: key}
		f.repo.releases[key] = ListingRelease{ID: key, TenantID: guardrailTenantID, ListingID: key, State: ReleasePublished}
	}
	limited, err := f.service.ResourceOverview(f.adminContext())
	if err != nil || limited.Total != (BoundedCount{Value: 10000, Limited: true}) || limited.Kinds != nil {
		t.Fatal(limited, err)
	}
	releases, err := f.service.ReleaseOverview(f.adminContext())
	if err != nil || !releases.Published.Limited || releases.Deprecated != (BoundedCount{}) {
		t.Fatal(releases, err)
	}
	for _, measure := range []InventoryMeasure{InventoryNamespaces, InventoryPublishers} {
		v, err := f.service.DirectoryOverview(f.adminContext(), measure)
		if err != nil || !v.Count.Limited || v.Count.Value != 10000 {
			t.Fatal(v, err)
		}
	}
	delete(f.repo.resources, "item-10000")
	exact, err := f.service.ResourceOverview(f.adminContext())
	if err != nil || exact.Total != (BoundedCount{Value: 10000}) || exact.Kinds[2].Count != 10000 {
		t.Fatal(exact, err)
	}
	other := f.reader
	other.TenantID = "00000000-0000-7000-8000-000000000098"
	isolated, err := f.service.ResourceOverview(identityfixture.WithActor(t.Context(), other))
	if err != nil || isolated.Total.Value != 0 {
		t.Fatal(isolated, err)
	}
	if _, err = f.service.ResourceOverview(t.Context()); ErrorCodeOf(err) != CodeForbidden {
		t.Fatal(err)
	}
	if _, err = f.service.DirectoryOverview(f.adminContext(), "unknown"); ErrorCodeOf(err) != CodeInvalidArgument {
		t.Fatal(err)
	}
}
