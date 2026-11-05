//go:build integration

package database

import (
	"context"
	"crypto/sha256"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/adapter"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/clock"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/id"
)

// Actual API LOGIN/NOBYPASSRLS, current retained policy and immutable revisions.
// These reads do not attempt the separately deferred expiry/locking matrix.
func TestPostgresAuthoringFindabilityAndReviewContext(t *testing.T) {
	originalName := strings.Repeat("🙂", 160)
	db, repo, _, seed := newSearchCostFixture(t, catalog.SearchDocument{Identifier: "urn:air:catalog.example.com:Finance:MyAgent", DisplayName: originalName, Description: "Synthetic findability fixture"})
	seed(0)
	author := semanticDisclosureActor(t, db, housekeepingTenantA)
	key := sha256.Sum256([]byte(t.Name()))
	service, err := catalog.NewService(catalog.Options{Repository: repo, Clock: clock.Real{}, IDs: id.Generator{}, Validator: adapter.NewRegistry(), CursorKey: key[:], Policy: "findability"})
	if err != nil {
		t.Fatal(err)
	}
	records, err := repo.SearchCatalog(t.Context(), housekeepingTenantA, catalog.CatalogSearchRepositoryRequest{Limit: 2})
	if err != nil || len(records.Items) != 1 {
		t.Fatal(records, err)
	}
	record := records.Items[0]
	authored, err := service.GetResource(author, record.Resource.ID)
	if err != nil {
		t.Fatal(err)
	}
	// Explicitly newer drafts place the known identity beyond the first page,
	// without assuming the application and database clocks advance together.
	// The exact filter must reach it without scanning earlier pages in the caller.
	if _, err := db.operator.Exec(t.Context(), `INSERT INTO resources(id,tenant_id,namespace_id,canonical_identifier,kind,created_by,revision,created_at,updated_at)
 SELECT gen_random_uuid(),$1,$2,'draft:'||n,'skill',$3,1,$4,$4 FROM generate_series(1,32) n`, housekeepingTenantA, record.Resource.NamespaceID, authored.CreatedBy, authored.UpdatedAt.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	first, err := service.ListResources(author, catalog.ResourceListRequest{Limit: 1})
	if err != nil || len(first) != 2 || first[0].ID == record.Resource.ID {
		t.Fatal("target must be on later page", first, err)
	}
	exact, err := service.ListResources(author, catalog.ResourceListRequest{CanonicalIdentifier: " URN:AIR:CATALOG.EXAMPLE.COM:Finance:MyAgent ", Kind: catalog.KindSkill, Limit: 1})
	if err != nil || len(exact) != 1 || exact[0].ID != record.Resource.ID {
		t.Fatal("exact lookup failed", exact, err)
	}
	for _, tc := range []catalog.ResourceListRequest{
		{CanonicalIdentifier: record.Resource.CanonicalIdentifier, Kind: catalog.KindAgent, Limit: 1},
		{CanonicalIdentifier: "urn:air:catalog.example.com:finance:MyAgent", Limit: 1},
		{CanonicalIdentifier: "absent", Limit: 1},
	} {
		if got, e := service.ListResources(author, tc); e != nil || len(got) != 0 {
			t.Fatal("wrong exact selection", got, e)
		}
	}
	opaque, err := service.CreateResource(author, catalog.CreateResourceInput{NamespaceID: record.Resource.NamespaceID, CanonicalIdentifier: " Café:e\u0301:🙂 ", Kind: catalog.KindAgent})
	if err != nil {
		t.Fatal(err)
	}
	if got, e := service.ListResources(author, catalog.ResourceListRequest{CanonicalIdentifier: " Café:e\u0301:🙂 ", Limit: 1}); e != nil || len(got) != 1 || got[0].ID != opaque.ID {
		t.Fatal(got, e)
	}
	if got, e := service.ListResources(author, catalog.ResourceListRequest{CanonicalIdentifier: "Café:é:🙂", Limit: 1}); e != nil || len(got) != 0 {
		t.Fatal("opaque normalized", got, e)
	}
	if err := service.DeleteResource(author, opaque.ID, "Remove synthetic unreviewed draft", opaque.Revision); err != nil {
		t.Fatal(err)
	}
	if got, e := service.ListResources(author, catalog.ResourceListRequest{CanonicalIdentifier: opaque.CanonicalIdentifier, Limit: 1}); e != nil || len(got) != 0 {
		t.Fatal("soft deleted leaked", got, e)
	}
	const opaqueBOM = "\ufeffOpaque:e\u0301:🙂\ufeff"
	bom, err := service.CreateResource(author, catalog.CreateResourceInput{NamespaceID: record.Resource.NamespaceID, CanonicalIdentifier: " " + opaqueBOM + " ", Kind: catalog.KindAgent})
	if err != nil {
		t.Fatal(err)
	}
	gotBOM, err := service.ListResources(author, catalog.ResourceListRequest{CanonicalIdentifier: " " + opaqueBOM + " ", Limit: 1})
	if err != nil || len(gotBOM) != 1 || gotBOM[0].ID != bom.ID || gotBOM[0].CanonicalIdentifier != opaqueBOM {
		t.Fatal("opaque BOM spelling changed", gotBOM, err)
	}
	if got, err := service.ListResources(author, catalog.ResourceListRequest{CanonicalIdentifier: "Opaque:e\u0301:🙂", Limit: 1}); err != nil || len(got) != 0 {
		t.Fatal("BOM was silently removed", got, err)
	}
	// A new candidate does not rename the exact previously submitted evidence.
	listing, err := service.GetListing(author, record.Listing.ID)
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = service.CreateListingRevision(author, listing.ID, catalog.CreateListingRevisionInput{VersionRevisionID: record.VersionRevision.ID, DisplayName: "Renamed after submission", Description: "A later candidate", PublisherID: record.ListingRevision.PublisherID, Visibility: catalog.VisibilityTenant}, listing.Revision)
	if err != nil {
		t.Fatal(err)
	}
	store, err := NewPostgresIdentityStore(repo)
	if err != nil {
		t.Fatal(err)
	}
	authority, err := identity.NewService(identity.ServiceOptions{Store: store, Clock: clock.Real{}, IDs: id.Generator{}, CursorKey: key[:]})
	if err != nil {
		t.Fatal(err)
	}
	for i, tc := range []struct{ metadata, content bool }{{false, false}, {true, false}, {false, true}, {true, true}} {
		subject := fmt.Sprintf("findability-%d", i)
		temporary, e := authority.GrantRole(author, identity.GrantRoleInput{Subject: subject, ActorType: identity.ActorHuman, Role: identity.RoleReader, Reason: "Provision bounded fixture principal"}, "temp")
		if e != nil {
			t.Fatal(e)
		}
		actor, e := store.ResolvePrincipal(t.Context(), identity.Principal{TenantID: housekeepingTenantA, Issuer: "https://identity.performance.example", Subject: subject})
		if e != nil {
			t.Fatal(e)
		}
		if _, e := authority.RevokeRole(author, temporary.ID, "Remove temporary broad permission", "revoke-temp", temporary.Revision); e != nil {
			t.Fatal(e)
		}
		permissions := []identity.Permission{"resource.read_history"}
		if tc.metadata {
			permissions = append(permissions, "resource.read")
		}
		if tc.content {
			permissions = append(permissions, "resource.read_content")
		}
		role, e := authority.SaveRoleDefinition(author, "", 0, identity.RoleDefinitionInput{Name: subject, Permissions: permissions, Reason: "Qualify independent review context"}, false, "role")
		if e != nil {
			t.Fatal(e)
		}
		grant, e := authority.GrantAuthorizationAssignment(author, identity.AccessAssignmentInput{MembershipID: actor.MembershipID, RoleDefinitionID: role.ID, NamespaceID: record.Listing.NamespaceID, Reason: "Exact fixture namespace"}, "grant")
		if e != nil {
			t.Fatal(e)
		}
		user := identity.WithActor(t.Context(), actor)
		check := func(ctx context.Context, wantIdentity, wantName string) {
			t.Helper()
			got, e := service.ListSubmissions(ctx, catalog.SubmissionListRequest{HistoryPageRequest: catalog.HistoryPageRequest{Limit: 1}, ListingID: listing.ID})
			if e != nil || len(got) != 1 || got[0].CanonicalIdentifier != wantIdentity || got[0].SubmittedDisplayName != wantName {
				t.Fatalf("independent queue context %d: %+v %v", i, got, e)
			}
		}
		wantIdentity, wantName := "", ""
		if tc.metadata {
			wantIdentity = record.Resource.CanonicalIdentifier
		}
		if tc.content {
			wantName = originalName
		}
		check(user, wantIdentity, wantName)
		got, e := service.ListResources(user, catalog.ResourceListRequest{CanonicalIdentifier: record.Resource.CanonicalIdentifier, Limit: 1})
		want := 0
		if tc.metadata {
			want = 1
		}
		if e != nil || len(got) != want {
			t.Fatal("resource namespace admission changed", got, e)
		}
		if _, e := authority.RevokeAuthorizationAssignment(author, grant.ID, grant.Revision, "Remove current fixture permission", "revoke"); e != nil {
			t.Fatal(e)
		}
		if got, e := service.ListSubmissions(user, catalog.SubmissionListRequest{HistoryPageRequest: catalog.HistoryPageRequest{Limit: 1}}); e != nil || len(got) != 0 {
			t.Fatal("revoked queue leaked", got, e)
		}
	}
	other := postgresFixtureContext(t, db, t.Context(), identity.Actor{TenantID: housekeepingTenantB, Subject: "other-findability", Roles: []identity.Role{identity.RoleReader}})
	if got, e := service.ListResources(other, catalog.ResourceListRequest{CanonicalIdentifier: record.Resource.CanonicalIdentifier, Limit: 1}); e != nil || len(got) != 0 {
		t.Fatal("cross tenant lookup leaked", got, e)
	}
	if got, e := service.ListSubmissions(other, catalog.SubmissionListRequest{HistoryPageRequest: catalog.HistoryPageRequest{Limit: 1}}); e != nil || len(got) != 0 {
		t.Fatal("cross tenant queue leaked", got, e)
	}
	// The existing tenant/canonical unique index supports the exact predicate.
	// No new index or ordering is required.
	_, err = withTenantRead(t.Context(), repo.pool, housekeepingTenantA, func(tx pgx.Tx) (bool, error) {
		if _, e := tx.Exec(t.Context(), `SET LOCAL enable_seqscan=off`); e != nil {
			return false, e
		}
		var plan string
		rows, e := tx.Query(t.Context(), `EXPLAIN SELECT id FROM resources WHERE tenant_id=$1 AND canonical_identifier=$2 AND deleted_at IS NULL ORDER BY updated_at DESC,id DESC LIMIT 2`, housekeepingTenantA, record.Resource.CanonicalIdentifier)
		if e != nil {
			return false, e
		}
		defer rows.Close()
		for rows.Next() {
			var line string
			if e := rows.Scan(&line); e != nil {
				return false, e
			}
			plan += line + "\n"
		}
		if !strings.Contains(plan, "Index") || !strings.Contains(plan, "canonical_identifier") {
			t.Fatalf("exact lookup lost index support: %s", plan)
		}
		t.Log("Existing exact index plan:", plan)
		return true, rows.Err()
	})
	if err != nil {
		t.Fatal(err)
	}
}
