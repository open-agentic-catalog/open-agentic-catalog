//go:build integration

// Unicode tag integration uses the existing owned exact-role fixture to prove
// that governed writes, indexed ingestion and SQL filtering retain the same tags.
package database

import (
	"crypto/sha256"
	"slices"
	"strings"
	"testing"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/adapter"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/clock"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/id"
)

// TestPostgresUnicodeTagRoundTrip covers SQL local/indexed filtering and facets
// with the longest accepted tags, without changing schema or retained evidence.
func TestPostgresUnicodeTagRoundTrip(t *testing.T) {
	tags := []string{strings.Repeat("\u00e9", 80), strings.Repeat("\U0001f642", 80)}
	document := catalog.SearchDocument{Identifier: "urn:air:catalog.example.com:finance:unicode-tags", DisplayName: "Unicode tags", Description: "Synthetic character boundary fixture", Tags: tags}
	db, repository, _, seed := newSearchCostFixture(t, document)
	seed(0)
	remote := document
	remote.Identifier = "urn:air:remote.example:finance:unicode-tags"
	manager := seedLexicalSource(t, db, repository, []catalog.SearchDocument{remote})
	var observedTags []string
	var compatibility, normalized string
	if err := db.operator.QueryRow(t.Context(), `SELECT coalesce(search_tags,'{}'),compatibility::text,coalesce(normalized_entry_digest,'') FROM observations WHERE tenant_id=$1`, housekeepingTenantA).Scan(&observedTags, &compatibility, &normalized); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(observedTags, tags) || normalized == "" {
		t.Fatalf("indexed ingestion tags=%q compatibility=%s normalized=%s", observedTags, compatibility, normalized)
	}
	key := sha256.Sum256([]byte(t.Name()))
	service, err := catalog.NewService(catalog.Options{Repository: repository, Clock: clock.Real{}, IDs: id.Generator{}, Validator: adapter.NewRegistry(), CursorKey: key[:], Policy: "unicode-tag-test"})
	if err != nil {
		t.Fatal(err)
	}
	search, err := catalog.NewSearchService(service, manager)
	if err != nil {
		t.Fatal(err)
	}
	ctx := postgresFixtureContext(t, db, t.Context(), identity.Actor{TenantID: housekeepingTenantA, Subject: "reader", Roles: []identity.Role{identity.RoleReader}})
	query := catalog.SearchQuery{Tags: tags, Limit: 10}
	page, err := search.Search(ctx, query)
	if err != nil || page.Partial || page.DurableTotal != 2 || len(page.Items) != 2 {
		t.Fatalf("Unicode search: %+v, %v", page, err)
	}
	for _, item := range page.Items {
		if !slices.Equal(item.Tags, tags) {
			t.Fatalf("round trip changed tags: %q", item.Tags)
		}
	}
	explored, err := service.Explore(ctx, catalog.ExploreRequest{Query: catalog.SearchQuery{Tags: tags}, Facets: []catalog.FacetRequest{{Field: "tag", Limit: 20}}})
	if err != nil || explored.Matched != 1 || len(explored.Facets["tag"].Buckets) != 2 {
		t.Fatalf("Unicode facets: %+v, %v", explored, err)
	}
	query.Tags = []string{strings.Repeat("\u00e9", 80), "absent"}
	page, err = search.Search(ctx, query)
	if err != nil || page.DurableTotal != 0 {
		t.Fatalf("conjunctive filter changed: %+v, %v", page, err)
	}
	other := postgresFixtureContext(t, db, t.Context(), identity.Actor{TenantID: housekeepingTenantB, Subject: "other", Roles: []identity.Role{identity.RoleReader}})
	page, err = search.Search(other, catalog.SearchQuery{Tags: tags})
	if err != nil || page.DurableTotal != 0 {
		t.Fatalf("cross-tenant Unicode query: %+v, %v", page, err)
	}
}
