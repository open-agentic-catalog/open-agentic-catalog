//go:build integration

package database

import (
	"crypto/sha256"
	"slices"
	"testing"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/clock"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/id"
)

func TestPostgresReleasedListingMetadata(t *testing.T) {
	f := newMCPToolFixture(t)
	owner := f.author(t)
	hidden := f.seed(t, 0, "Restricted release", "", false)
	ns, err := f.service.CreateNamespace(owner, catalog.CreateNamespaceInput{Slug: "metadata-report", DisplayName: "Report scope"})
	if err != nil {
		t.Fatal(err)
	}
	f.namespace = ns.ID
	first := f.seed(t, 1, "Missing documentation", "", false)
	second := f.seed(t, 2, "Missing documentation too", "", false)
	store, err := NewPostgresIdentityStore(f.repo)
	if err != nil {
		t.Fatal(err)
	}
	key := sha256.Sum256([]byte(t.Name()))
	auth, err := identity.NewService(identity.ServiceOptions{Store: store, Clock: clock.Real{}, IDs: id.Generator{}, CursorKey: key[:]})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = auth.GrantRole(owner, identity.GrantRoleInput{Subject: "report-reader", ActorType: identity.ActorHuman, Role: identity.RoleServiceClient, Reason: "Report fixture membership"}, "member"); err != nil {
		t.Fatal(err)
	}
	principal, err := store.ResolvePrincipal(t.Context(), identity.Principal{TenantID: first.Resource.TenantID, Issuer: "https://identity.performance.example", Subject: "report-reader"})
	if err != nil {
		t.Fatal(err)
	}
	reader := identity.WithActor(t.Context(), principal)
	q := catalog.ListingMetadataQuery{Checks: catalog.ListingMetadataChecks{Documentation: true, License: true}, Limit: 1}
	read := func(q catalog.ListingMetadataQuery) []catalog.ListingMetadataGap {
		t.Helper()
		rows, err := f.service.ListListingMetadataGaps(reader, q)
		if err != nil {
			t.Fatal(err)
		}
		return rows
	}
	if rows := read(q); len(rows) != 0 {
		t.Fatal("ungranted metadata", rows)
	}
	role, err := auth.SaveRoleDefinition(owner, "", 0, identity.RoleDefinitionInput{Name: "Metadata report reader", Permissions: []identity.Permission{"resource.read"}, Reason: "Only released metadata"}, false, "role")
	if err != nil {
		t.Fatal(err)
	}
	assignment, err := auth.GrantAuthorizationAssignment(owner, identity.AccessAssignmentInput{MembershipID: principal.MembershipID, RoleDefinitionID: role.ID, NamespaceID: ns.ID, Reason: "Scoped report qualification"}, "grant")
	if err != nil {
		t.Fatal(err)
	}
	rows := read(q)
	if len(rows) != 2 || rows[0].ListingID != first.Listing.ID || rows[1].ListingID != second.Listing.ID || rows[0].ListingRevisionID != first.ListingRevision.ID || !slices.Equal(rows[0].Missing, []string{"documentation", "license"}) {
		t.Fatal("scope before lookahead", rows)
	}
	q.AfterID = first.Listing.ID
	if rows = read(q); len(rows) != 1 || rows[0].ReleaseID != second.Release.ID {
		t.Fatal("continuation", rows)
	}
	q.NamespaceID = hidden.Resource.NamespaceID
	if rows = read(q); len(rows) != 0 {
		t.Fatal("hidden namespace", rows)
	}
	q.NamespaceID = ns.ID
	q.Kind = catalog.KindAgent
	if rows = read(q); len(rows) != 0 {
		t.Fatal("kind", rows)
	}
	q.Kind = catalog.KindMCPServer
	if _, err = f.service.TransitionRelease(owner, second.Release.ID, catalog.ReleaseDeprecated, "Planned replacement", "", nil, second.Release.Revision); err != nil {
		t.Fatal(err)
	}
	if rows = read(q); len(rows) != 1 || rows[0].State != catalog.ReleaseDeprecated {
		t.Fatal("moving page", rows)
	}
	if _, err = auth.RevokeAuthorizationAssignment(owner, assignment.ID, assignment.Revision, "End report access", "revoke"); err != nil {
		t.Fatal(err)
	}
	q.AfterID = ""
	if rows = read(q); len(rows) != 0 {
		t.Fatal("revoked scope", rows)
	}
}
