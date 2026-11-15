package catalog

import (
	"context"
	"testing"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
)

type withdrawalPreviewFixture struct {
	calls               int
	tenantID, releaseID string
	err                 error
}

func (*withdrawalPreviewFixture) WithdrawPublicationDomain(context.Context, string, string, string, string, string) ([]string, error) {
	panic("preview must not withdraw")
}
func (*withdrawalPreviewFixture) WithdrawPublicationRelease(context.Context, string, string, string, string, string) ([]string, error) {
	panic("preview must not withdraw")
}
func (f *withdrawalPreviewFixture) PreviewPublicationRelease(_ context.Context, tenant, release string) ([]ReleaseWithdrawalSnapshot, error) {
	f.calls++
	f.tenantID, f.releaseID = tenant, release
	return []ReleaseWithdrawalSnapshot{{SnapshotID: "snapshot", Host: "catalog.example", Audience: "anonymous", Generation: 2, ETag: "etag"}}, f.err
}

func TestWithdrawalPreviewComposesExistingReadAuthorityAndPreservesState(t *testing.T) {
	fixture := newGuardrailFixture(t)
	release := ListingRelease{ID: "00000000-0000-7000-8000-000000000111", TenantID: guardrailTenantID, ListingID: "00000000-0000-7000-8000-000000000112", State: ReleasePublished, Revision: 3}
	fixture.repo.releases[release.ID] = release
	fixture.repo.listings[release.ListingID] = CatalogListing{ID: release.ListingID, TenantID: release.TenantID, ResourceID: "resource", NamespaceID: "00000000-0000-7000-8000-000000000113"}
	fixture.repo.resources["resource"] = Resource{ID: "resource", TenantID: release.TenantID}
	port := &withdrawalPreviewFixture{}
	fixture.service.publicationWithdrawer = port
	actor := func(tenant string, namespaces map[string][]identity.Role, templates ...string) context.Context {
		t.Helper()
		policy, err := identity.NewMemoryStore([]identity.Seed{{TenantID: tenant, Issuer: "urn:preview-test", Subject: "reader", ActorType: identity.ActorHuman, RoleTemplates: templates, Namespaces: namespaces}})
		if err != nil {
			t.Fatal(err)
		}
		principal, err := policy.ResolvePrincipal(t.Context(), identity.Principal{TenantID: tenant, Issuer: "urn:preview-test", Subject: "reader"})
		if err != nil {
			t.Fatal(err)
		}
		return identity.WithActor(t.Context(), identity.BindPermissionStore(principal, policy))
	}
	ctx := actor(release.TenantID, nil, "reader", "publication_admin")
	preview, err := fixture.service.PreviewReleaseWithdrawal(ctx, release.ID, ReleaseRetired)
	if err != nil || preview.ReleaseID != release.ID || preview.ReleaseRevision != 3 || preview.Transition != ReleaseRetired || !preview.ObservedAt.Equal(fixture.clock.Now()) || len(preview.Snapshots) != 1 || port.calls != 1 || port.tenantID != release.TenantID || port.releaseID != release.ID {
		t.Fatal(preview, err, port.calls)
	}
	for _, target := range []ReleaseState{ReleaseSuperseded, ReleaseRevoked} {
		value, err := fixture.service.PreviewReleaseWithdrawal(ctx, release.ID, target)
		if err != nil || value.Transition != target || len(value.Snapshots) != 1 {
			t.Fatal(value, err)
		}
	}
	if fixture.repo.releases[release.ID] != release || len(fixture.repo.audit) != 0 || len(fixture.repo.outbox) != 0 {
		t.Fatal("preview mutated release or evidence")
	}
	for _, templates := range [][]string{{"publisher"}, {"reader"}, {"publication_admin"}} {
		if _, err := fixture.service.PreviewReleaseWithdrawal(actor(release.TenantID, nil, templates...), release.ID, ReleaseRetired); ErrorCodeOf(err) != CodeForbidden {
			t.Fatal(templates, err)
		}
	}
	if port.calls != 3 {
		t.Fatal("disclosed snapshots without composed read authority")
	}
	namespace := fixture.repo.listings[release.ListingID].NamespaceID
	scoped := actor(release.TenantID, map[string][]identity.Role{namespace: {identity.RoleReader}}, "publication_admin")
	if value, err := fixture.service.PreviewReleaseWithdrawal(scoped, release.ID, ReleaseRetired); err != nil || len(value.Snapshots) != 1 || port.calls != 4 {
		t.Fatal("exact namespace history grant did not compose with publication read", value, err)
	}
	if _, err := requirePermission(scoped, "release.transition", namespace); ErrorCodeOf(err) != CodeForbidden {
		t.Fatal("read-only preview fixture unexpectedly has release mutation authority", err)
	}
	wrongScope := actor(release.TenantID, map[string][]identity.Role{"00000000-0000-7000-8000-000000000114": {identity.RoleReader}}, "publication_admin")
	if _, err := fixture.service.PreviewReleaseWithdrawal(wrongScope, release.ID, ReleaseRetired); ErrorCodeOf(err) != CodeForbidden || port.calls != 4 {
		t.Fatal("another namespace's history grant disclosed publication impact", err)
	}
	if _, err := fixture.service.PreviewReleaseWithdrawal(actor("00000000-0000-7000-8000-000000000199", nil, "reader", "publication_admin"), release.ID, ReleaseRetired); ErrorCodeOf(err) != CodeNotFound {
		t.Fatal("foreign existence disclosure", err)
	}
	if _, err := fixture.service.PreviewReleaseWithdrawal(ctx, release.ID, ReleaseDeprecated); ErrorCodeOf(err) != CodeInvalidArgument {
		t.Fatal("deprecation invented withdrawal", err)
	}
	port.err = E(CodeDependencyUnavailable, "database unavailable")
	if value, err := fixture.service.PreviewReleaseWithdrawal(ctx, release.ID, ReleaseRetired); ErrorCodeOf(err) != CodeDependencyUnavailable || value.Snapshots != nil {
		t.Fatal("outage became a successful empty preview", value, err)
	}
	release.State = ReleaseRetired
	fixture.repo.releases[release.ID] = release
	if _, err := fixture.service.PreviewReleaseWithdrawal(ctx, release.ID, ReleaseRetired); ErrorCodeOf(err) != CodeInvalidTransition {
		t.Fatal(err)
	}
}
