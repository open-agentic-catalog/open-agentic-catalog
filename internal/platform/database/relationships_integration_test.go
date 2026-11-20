//go:build integration

package database

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"slices"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/clock"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/id"
)

func TestPostgresReleaseRelationships(t *testing.T) {
	f := newMCPToolFixture(t)
	target := f.seed(t, 0, "Dependency", "", false)
	_, nextVersion, _, err := f.service.CreateVersion(f.author(t), target.Resource.ID, catalog.CreateVersionInput{Version: "2.0.0", Representations: f.representations(t, "2.0.0", "", false)})
	if err != nil {
		t.Fatal(err)
	}
	exact := catalog.Relationship{Type: "depends_on", TargetIdentifier: target.Resource.CanonicalIdentifier, TargetVersion: "1.0.0"}
	unversioned := exact
	unversioned.TargetVersion = ""
	other := exact
	other.TargetVersion = "2.0.0"
	optional := exact
	optional.Type = "recommended_with"
	first := f.seed(t, 1, "First", "", false, exact, unversioned, optional, other)
	second := f.seed(t, 2, "Second", "", false, unversioned)
	currentNS := f.namespace
	hiddenNS, err := f.service.CreateNamespace(f.author(t), catalog.CreateNamespaceInput{Slug: "hidden-relationships", DisplayName: "Hidden"})
	if err != nil {
		t.Fatal(err)
	}
	f.namespace = hiddenNS.ID
	hidden := f.seed(t, 3, "Restricted dependent", "", false, exact)
	f.namespace = currentNS

	store, err := NewPostgresIdentityStore(f.repo)
	if err != nil {
		t.Fatal(err)
	}
	key := sha256.Sum256([]byte(t.Name()))
	auth, err := identity.NewService(identity.ServiceOptions{Store: store, Clock: clock.Real{}, IDs: id.Generator{}, CursorKey: key[:]})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = auth.GrantRole(f.author(t), identity.GrantRoleInput{Subject: "relationship-reader", ActorType: identity.ActorHuman, Role: identity.RoleServiceClient, Reason: "Owned relationship fixture"}, "member"); err != nil {
		t.Fatal(err)
	}
	actor, err := store.ResolvePrincipal(t.Context(), identity.Principal{TenantID: target.Resource.TenantID, Issuer: "https://identity.performance.example", Subject: "relationship-reader"})
	if err != nil {
		t.Fatal(err)
	}
	role, err := auth.SaveRoleDefinition(f.author(t), "", 0, identity.RoleDefinitionInput{Name: "Scoped relationship metadata", Permissions: []identity.Permission{"resource.read", "resource.read_history"}, Reason: "Inspect only the selected namespace"}, false, "role")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = auth.GrantAuthorizationAssignment(f.author(t), identity.AccessAssignmentInput{RoleDefinitionID: role.ID, MembershipID: actor.MembershipID, NamespaceID: currentNS, Reason: "Scoped relationships"}, "grant"); err != nil {
		t.Fatal(err)
	}
	reader := identity.WithActor(t.Context(), actor)
	selectPage := func(t *testing.T, ctx context.Context, subject string, view catalog.RelationshipView, limit int) catalog.RelationshipSelection {
		t.Helper()
		rows, e := f.service.ListReleaseRelationships(ctx, subject, catalog.RelationshipPageRequest{View: view, Limit: limit})
		if e != nil {
			t.Fatal(e)
		}
		return rows
	}
	// The repository supplies exactly limit+1 matches after admission. Five readable
	// declarations fit an exact-limit page across two distinct current source releases.
	page := selectPage(t, reader, target.Release.ID, catalog.IncomingRelationships, 5)
	if len(page.Items) != 5 {
		t.Fatal(page)
	}
	sources := map[string]catalog.CatalogRecord{first.Release.ID: first, second.Release.ID: second}
	seen := map[string][]catalog.Relationship{}
	for _, e := range page.Items {
		source, ok := sources[e.SourceReleaseID]
		if !ok || e.Related == nil || e.Related.ListingRevisionID != source.ListingRevision.ID || e.Related.ReleaseID != e.SourceReleaseID {
			t.Fatal(e)
		}
		seen[e.SourceReleaseID] = append(seen[e.SourceReleaseID], e.Declaration)
	}
	if !slices.Equal(seen[first.Release.ID], []catalog.Relationship{unversioned, exact, other, optional}) || !slices.Equal(seen[second.Release.ID], []catalog.Relationship{unversioned}) {
		t.Fatal(seen)
	}
	t.Run("identical target identifiers remain tenant-local in both directions", func(t *testing.T) {
		foreign := *f
		actor := func(subject string, roles ...identity.Role) identity.Actor {
			ctx := postgresFixtureContext(t, f.db, t.Context(), identity.Actor{TenantID: housekeepingTenantB, Subject: subject, Roles: roles})
			a, _ := identity.FromContext(ctx)
			return a
		}
		foreign.authorActor = actor("foreign-author", identity.RoleTenantAdmin, identity.RolePublisher, identity.RoleContributor, identity.RoleReader)
		foreign.reviewerActor = actor("foreign-reviewer", identity.RoleReviewer)
		ns, err := f.service.CreateNamespace(foreign.author(t), catalog.CreateNamespaceInput{Slug: "performance", DisplayName: "Foreign relationships"})
		if err != nil {
			t.Fatal(err)
		}
		pub, err := f.service.CreatePublisher(foreign.author(t), catalog.CreatePublisherInput{DisplayName: "Foreign publisher", Domains: []string{"catalog.example.com"}})
		if err != nil {
			t.Fatal(err)
		}
		foreign.namespace, foreign.publisher = ns.ID, pub.ID
		foreignTarget := foreign.seed(t, 0, "Foreign target", "", false)
		foreignSource := foreign.seed(t, 1, "Foreign dependent", "", false, exact)
		if foreignTarget.Resource.CanonicalIdentifier != target.Resource.CanonicalIdentifier {
			t.Fatal("target identifiers must coincide for this control")
		}
		in := selectPage(t, foreign.author(t), foreignTarget.Release.ID, catalog.IncomingRelationships, 10).Items
		if len(in) != 1 || in[0].Related == nil || in[0].Related.ReleaseID != foreignSource.Release.ID || in[0].Declaration != exact {
			t.Fatal("foreign reverse lookup", in)
		}
		out := selectPage(t, foreign.author(t), foreignSource.Release.ID, catalog.OutgoingRelationships, 10).Items
		if len(out) != 1 || out[0].Related == nil || out[0].Related.ReleaseID != foreignTarget.Release.ID || out[0].Declaration != exact {
			t.Fatal("foreign destination", out)
		}
	})
	if own := selectPage(t, reader, target.Release.ID, catalog.IncomingRelationships, 10).Items; len(own) != 5 {
		t.Fatal("foreign declaration changed local results", own)
	}
	if len(selectPage(t, f.author(t), target.Release.ID, catalog.IncomingRelationships, 10).Items) != 6 {
		t.Fatal("restricted positive control")
	}
	if _, err = f.service.ListReleaseRelationships(reader, hidden.Release.ID, catalog.RelationshipPageRequest{View: catalog.OutgoingRelationships, Limit: 10}); catalog.ErrorCodeOf(err) != catalog.CodeNotFound {
		t.Fatal("restricted subject", err)
	}
	small := selectPage(t, reader, target.Release.ID, catalog.ReleaseDependencies, 1)
	if len(small.Items) != 2 {
		t.Fatal("overflow", small)
	}
	if _, err = f.service.TransitionRelease(reader, target.Release.ID, catalog.ReleaseRetired, "Withdraw target", "", nil, target.Release.Revision); catalog.ErrorCodeOf(err) != catalog.CodeForbidden {
		t.Fatal("preview access permitted withdrawal", err)
	}
	last := small.Items[0]
	req := catalog.RelationshipPageRequest{View: catalog.ReleaseDependencies, Limit: 3, AfterReleaseID: last.SourceReleaseID, AfterOrdinal: last.Ordinal, Generation: small.Generation}
	tail, err := f.service.ListReleaseRelationships(reader, target.Release.ID, req)
	if err != nil || len(tail.Items) != 2 {
		t.Fatal(tail, err)
	}
	impact := map[string][]catalog.Relationship{}
	for _, item := range append([]catalog.ReleasedRelationship{last}, tail.Items...) {
		impact[item.SourceReleaseID] = append(impact[item.SourceReleaseID], item.Declaration)
	}
	if !slices.Equal(impact[first.Release.ID], []catalog.Relationship{unversioned, exact}) || !slices.Equal(impact[second.Release.ID], []catalog.Relationship{unversioned}) {
		t.Fatal("missing or repeated dependency across the page boundary", impact)
	}
	if _, err = f.service.TransitionRelease(f.author(t), target.Release.ID, catalog.ReleaseDeprecated, "Earlier version", "", nil, target.Release.Revision); err != nil {
		t.Fatal(err)
	}
	if _, err = f.service.TransitionRelease(f.author(t), target.Release.ID, catalog.ReleaseRetired, "Withdraw after preview", "", nil, target.Release.Revision); catalog.ErrorCodeOf(err) != catalog.CodePreconditionFailed {
		t.Fatal("a preview must not reserve the prior release revision", err)
	}
	out := selectPage(t, reader, first.Release.ID, catalog.OutgoingRelationships, 10)
	for _, item := range out.Items {
		if item.Declaration == other {
			if item.Related != nil {
				t.Fatal("historical link", item)
			}
		} else if item.Related == nil || item.Related.ReleaseID != target.Release.ID || item.Related.Version != "1.0.0" {
			t.Fatal(item)
		}
	}
	if _, err = f.service.ListReleaseRelationships(reader, target.Release.ID, req); catalog.ErrorCodeOf(err) != catalog.CodeInvalidArgument {
		t.Fatal("accepted stale generation", err)
	}
	replaced := f.publish(t, target.Listing, nextVersion, "New dependency")
	out = selectPage(t, reader, first.Release.ID, catalog.OutgoingRelationships, 10)
	for _, item := range out.Items {
		if item.Declaration.TargetVersion == "1.0.0" {
			if item.Related != nil {
				t.Fatal("redirected historical version", item)
			}
		} else if item.Related == nil || item.Related.ReleaseID != replaced.ID || item.Related.Version != "2.0.0" {
			t.Fatal(item)
		}
	}
	if len(selectPage(t, reader, target.Release.ID, catalog.ReleaseDependencies, 10).Items) != 3 {
		t.Fatal("historical release lost exact impact")
	}
	if _, err = f.service.ListReleaseRelationships(reader, target.Release.ID, catalog.RelationshipPageRequest{View: catalog.IncomingRelationships, Limit: 5}); catalog.ErrorCodeOf(err) != catalog.CodeNotFound {
		t.Fatal("historical reader view", err)
	}

	hiddenTarget := catalog.Relationship{Type: "bundled_with", TargetIdentifier: hidden.Resource.CanonicalIdentifier}
	edited, _, err := f.service.CreateListingRevision(f.author(t), first.Listing.ID, catalog.CreateListingRevisionInput{VersionRevisionID: first.VersionRevision.ID, PublisherID: f.publisher, DisplayName: "Unreleased edit", Description: "Preview", Visibility: catalog.VisibilityTenant, Relationships: []catalog.Relationship{hiddenTarget}}, first.Listing.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if len(selectPage(t, reader, replaced.ID, catalog.IncomingRelationships, 8).Items) != 5 {
		t.Fatal("unpublished revision displaced released declarations")
	}
	newSource := f.publish(t, edited, first.VersionRevision, "Replaced source", hiddenTarget)
	edges := selectPage(t, reader, newSource.ID, catalog.OutgoingRelationships, 10).Items
	if len(edges) != 1 || edges[0].Declaration != hiddenTarget || edges[0].Related != nil {
		t.Fatal("restricted destination disclosed", edges)
	}
	if edges := selectPage(t, reader, replaced.ID, catalog.IncomingRelationships, 1).Items; len(edges) != 1 || edges[0].SourceReleaseID != second.Release.ID {
		t.Fatal("replaced source leaked", edges)
	}
	if _, err = f.repo.ListReleaseRelationships(t.Context(), housekeepingTenantB, newSource.ID, catalog.RelationshipPageRequest{View: catalog.OutgoingRelationships, Limit: 1}); catalog.ErrorCodeOf(err) != catalog.CodeNotFound {
		t.Fatal("cross-tenant subject", err)
	}

	beforePolicy := selectPage(t, reader, replaced.ID, catalog.IncomingRelationships, 1)
	if _, err = auth.SaveRoleDefinition(f.author(t), role.ID, role.Revision, identity.RoleDefinitionInput{Name: role.Name, Permissions: []identity.Permission{"resource.read"}, Reason: "Remove access to release history"}, false, "drop-history"); err != nil {
		t.Fatal(err)
	}
	if _, err = f.service.ListReleaseRelationships(reader, replaced.ID, catalog.RelationshipPageRequest{View: catalog.IncomingRelationships, Limit: 1, Generation: beforePolicy.Generation}); catalog.ErrorCodeOf(err) != catalog.CodeInvalidArgument {
		t.Fatal("accepted earlier policy", err)
	}
	if _, err = f.service.ListReleaseRelationships(reader, replaced.ID, catalog.RelationshipPageRequest{View: catalog.ReleaseDependencies, Limit: 1}); catalog.ErrorCodeOf(err) != catalog.CodeNotFound {
		t.Fatal("dependency view disclosed release history", err)
	}
	if len(selectPage(t, reader, replaced.ID, catalog.IncomingRelationships, 1).Items) != 1 {
		t.Fatal("independent incoming read lost")
	}

	// Measure the same SQL and exact API role with the scoped reader's real predicates.
	selected, _, err := identity.WithCollectionContext(reader)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := withTenantCollectionRead(selected, f.repo.pool, target.Resource.TenantID, func(tx pgx.Tx) (json.RawMessage, error) {
		var data json.RawMessage
		err := tx.QueryRow(selected, "EXPLAIN (ANALYZE,BUFFERS,FORMAT JSON) "+incomingRelationshipSQL, target.Resource.TenantID, target.Resource.CanonicalIdentifier, false, "", "", 0, 2).Scan(&data)
		return data, err
	})
	if err != nil {
		t.Fatal(err)
	}
	var stats []struct {
		ExecutionMS float64 `json:"Execution Time"`
		Plan        struct {
			Node string  `json:"Node Type"`
			Rows float64 `json:"Actual Rows"`
			Hits int     `json:"Shared Hit Blocks"`
		} `json:"Plan"`
	}
	if err = json.Unmarshal(plan, &stats); err != nil || len(stats) != 1 || stats[0].Plan.Node != "Limit" || stats[0].Plan.Rows != 1 {
		t.Fatal("reverse query plan", string(plan), err)
	}
	t.Logf("Scoped reverse lookup returned %.0f current edge in %.3f ms (%d shared buffer hits)", stats[0].Plan.Rows, stats[0].ExecutionMS, stats[0].Plan.Hits)

	// Reading impact neither authorizes retirement nor changes a dependent's release.
	finalImpact := selectPage(t, f.author(t), replaced.ID, catalog.ReleaseDependencies, 10).Items
	if len(finalImpact) != 1 || finalImpact[0].SourceReleaseID != second.Release.ID || finalImpact[0].Declaration != unversioned {
		t.Fatal("expected one potentially affected source before retirement", finalImpact)
	}
	if _, err = f.service.TransitionRelease(f.author(t), replaced.ID, catalog.ReleaseRetired, "Withdraw target", "", nil, replaced.Revision); err != nil {
		t.Fatal(err)
	}
	surviving := selectPage(t, reader, second.Release.ID, catalog.OutgoingRelationships, 10).Items
	if len(surviving) != 1 || surviving[0].Declaration != unversioned || surviving[0].Related != nil {
		t.Fatal("withdrawal must retain the source release and its declaration without a link", surviving)
	}
}
