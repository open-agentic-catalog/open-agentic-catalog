package httpserver

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/clock"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/id"
)

// These are actual custom permissions, not an Actor's descriptive role names.
// Reviewing/publishing needs history/content companions, not metadata authority.
func TestReviewContextDoesNotRequireUnrelatedMetadataRead(t *testing.T) {
	repo := controlReadFixture(t)
	repo.read = func(_ context.Context, tenant, _ string, _ string) error {
		if tenant != projectionTenant {
			t.Fatal("cross-tenant projection")
		}
		return nil
	}
	store, err := identity.NewMemoryStore([]identity.Seed{
		{TenantID: projectionTenant, Issuer: "review-unit", Subject: "owner", ActorType: identity.ActorHuman, Roles: []identity.Role{identity.RoleTenantAdmin}, Namespaces: map[string][]identity.Role{projectionResource: {identity.RoleReader}}},
		{TenantID: projectionTenant, Issuer: "review-unit", Subject: "reviewer", ActorType: identity.ActorHuman},
	})
	if err != nil {
		t.Fatal(err)
	}
	resolve := func(subject string) identity.Actor {
		a, e := store.ResolvePrincipal(t.Context(), identity.Principal{TenantID: projectionTenant, Issuer: "review-unit", Subject: subject})
		if e != nil {
			t.Fatal(e)
		}
		return a
	}
	owner, member := resolve("owner"), resolve("reviewer")
	service, err := identity.NewService(identity.ServiceOptions{Store: store, Clock: clock.NewFake(projectionTime), IDs: id.Generator{}, CursorKey: []byte(strings.Repeat("k", 32))})
	if err != nil {
		t.Fatal(err)
	}
	role, err := service.SaveRoleDefinition(identity.WithActor(t.Context(), owner), "", 0, identity.RoleDefinitionInput{Name: "Scoped review and publication", Permissions: []identity.Permission{"resource.read_history", "resource.read_content", "review.decide", "release.publish"}, Reason: "Explicit workflow companions"}, false, "role")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = service.GrantAuthorizationAssignment(identity.WithActor(t.Context(), owner), identity.AccessAssignmentInput{RoleDefinitionID: role.ID, MembershipID: member.MembershipID, NamespaceID: projectionResource, Reason: "Exact review namespace"}, "assignment"); err != nil {
		t.Fatal(err)
	}
	server := projectionServer(projectionCatalog(t, repo, nil))
	ctx := identity.WithActor(t.Context(), member)
	read := func(handler http.HandlerFunc, path string) *httptest.ResponseRecorder {
		r := httptest.NewRequestWithContext(ctx, http.MethodGet, "/api/v1/test", nil)
		r.SetPathValue(path, projectionItem)
		w := httptest.NewRecorder()
		handler(w, r)
		return w
	}
	want := repo.submission
	want.NamespaceID = projectionResource
	want.ListingConcurrencyRevision = 3
	requireControlJSON(t, read(server.handleSubmissionGet, "submissionId"), http.StatusOK, want)
	requireProjectionProblem(t, read(server.handleListingGet, "listingId"), http.StatusForbidden, catalog.CodeForbidden)
	checks, err := store.CheckPermissions(t.Context(), member, []identity.PermissionCheck{{Permission: "review.decide", NamespaceID: projectionResource}, {Permission: "release.publish", NamespaceID: projectionResource}, {Permission: "review.decide", NamespaceID: projectionNext}, {Permission: "resource.read", NamespaceID: projectionResource}})
	if err != nil || len(checks.Allowed) != 4 || !checks.Allowed[0] || !checks.Allowed[1] || checks.Allowed[2] || checks.Allowed[3] {
		t.Fatalf("scope/companion authority = %+v, %v", checks, err)
	}
}
