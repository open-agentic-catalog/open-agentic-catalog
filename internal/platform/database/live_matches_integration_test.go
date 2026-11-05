//go:build integration

package database

import (
	"slices"
	"testing"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
)

func TestPostgresLiveIdentityProjectionUsesWholeMatchingPopulation(t *testing.T) {
	db, repo, _, seed := newSearchCostFixture(t)
	for i := range 28 {
		seed(i)
	}
	page, err := repo.SearchCatalog(t.Context(), housekeepingTenantA, catalog.CatalogSearchRepositoryRequest{Limit: 30})
	if err != nil || len(page.Items) != 28 {
		t.Fatalf("seed: %+v %v", page, err)
	}
	last := page.Items[len(page.Items)-1]
	key := catalog.IndexedIdentity{Identifier: last.Resource.CanonicalIdentifier, Version: last.Version.Version, VersionPresent: true}
	probes := []catalog.IndexedIdentity{key, {Identifier: key.Identifier, Version: key.Version, VersionPresent: false}, {Identifier: "urn:air:absent.example:agent:a", Version: "1", VersionPresent: true}}
	position := catalog.CatalogSearchPosition{Score: 100, DisplayName: "after-everything", ResultID: "ignored"}
	request := catalog.CatalogSearchRepositoryRequest{IdentityProbes: probes, ExpectedGeneration: page.Generation, Limit: 1, After: &position}
	matched, err := repo.SearchCatalog(t.Context(), housekeepingTenantA, request)
	if err != nil || !slices.Equal(matched.Matches, []catalog.IndexedIdentity{key}) || len(matched.Items) != 0 || matched.Total != 0 {
		t.Fatalf("whole-set projection: %+v %v", matched, err)
	}
	for _, q := range []catalog.SearchQuery{{Origins: []catalog.SearchResultType{catalog.SearchResultIndexed, catalog.SearchResultLive}}, {Text: "nonmatchingtext"}, {NamespaceID: "00000000-0000-7000-8000-000000000099"}, {License: "not-the-license"}} {
		request.Query = q
		got, err := repo.SearchCatalog(t.Context(), housekeepingTenantA, request)
		if err != nil || len(got.Matches) != 0 {
			t.Fatalf("excluded selected population: %+v %v", got, err)
		}
	}
	request.Query = catalog.SearchQuery{}
	request.ExpectedGeneration = "stale"
	if _, err := repo.SearchCatalog(t.Context(), housekeepingTenantA, request); catalog.ErrorCodeOf(err) != catalog.CodeInvalidArgument {
		t.Fatalf("stale projection: %v", err)
	}
	request.ExpectedGeneration = ""
	other, err := repo.SearchCatalog(t.Context(), housekeepingTenantB, request)
	if err != nil || len(other.Matches) != 0 {
		t.Fatalf("cross-tenant projection: %+v %v", other, err)
	}
	request.IdentityProbes = make([]catalog.IndexedIdentity, 51)
	if _, err := repo.SearchCatalog(t.Context(), housekeepingTenantA, request); catalog.ErrorCodeOf(err) != catalog.CodeInvalidArgument {
		t.Fatalf("unbounded projection: %v", err)
	}
	// The indexed branch uses exact tuples at SQL selection too, including the
	// absent-version distinction and the same short snapshot generation fence.
	remote := catalog.SearchDocument{Identifier: "urn:air:remote.example:agent:projection", DisplayName: "Performance remote", Tags: []string{}, RepresentativeQueries: []string{}}
	duplicate := remote
	duplicate.Identifier = last.Resource.CanonicalIdentifier
	seedLexicalSourceEndpoint(t, db, repo, []catalog.SearchDocument{remote, duplicate}, false, "https://catalog.remote.example/.well-known/ard.json", last.Version.Version)
	store, err := NewPostgresFederationStore(repo)
	if err != nil {
		t.Fatal(err)
	}
	indexed, err := store.SearchIndexedCatalog(t.Context(), housekeepingTenantA, catalog.FederatedCatalogSearchRequest{Limit: 1})
	if err != nil || len(indexed.Indexed) != 1 {
		t.Fatalf("indexed seed: %+v %v", indexed, err)
	}
	item := indexed.Indexed[0]
	remoteKey := catalog.IndexedIdentity{Identifier: item.Identifier, Version: item.Version, VersionPresent: item.VersionPresent}
	query := catalog.SearchQuery{Origins: []catalog.SearchResultType{catalog.SearchResultIndexed, catalog.SearchResultLive}}
	ir := catalog.FederatedCatalogSearchRequest{Query: query, IdentityProbes: []catalog.IndexedIdentity{remoteKey, {Identifier: remoteKey.Identifier, Version: remoteKey.Version, VersionPresent: !remoteKey.VersionPresent}}, ExpectedGeneration: indexed.Generation, After: &position, Limit: 1}
	result, err := store.SearchIndexedCatalog(t.Context(), housekeepingTenantA, ir)
	if err != nil || !slices.Equal(result.Matches, []catalog.IndexedIdentity{remoteKey}) || len(result.Indexed) != 0 {
		t.Fatalf("indexed projection: %+v %v", result, err)
	}

	// An excluded local origin must not hide the selected indexed copy.
	duplicateRequest := ir
	duplicateRequest.IdentityProbes = []catalog.IndexedIdentity{key}
	duplicatePage, err := store.SearchIndexedCatalog(t.Context(), housekeepingTenantA, duplicateRequest)
	if err != nil || !slices.Equal(duplicatePage.Matches, []catalog.IndexedIdentity{key}) {
		t.Fatalf("excluded local suppressed indexed: %+v %v", duplicatePage, err)
	}
	bookmark := catalog.FederatedCatalogSearchRequest{Query: catalog.SearchQuery{Origins: []catalog.SearchResultType{catalog.SearchResultIndexed}}, ExactIdentity: &key, Limit: 1}
	opened, err := store.SearchIndexedCatalog(t.Context(), housekeepingTenantA, bookmark)
	if err != nil || len(opened.Indexed) != 1 || opened.Indexed[0].Identifier != key.Identifier {
		t.Fatalf("indexed-only exact bookmark: %+v %v", opened, err)
	}
	bookmark.Query.Origins = nil
	combined, err := store.SearchIndexedCatalog(t.Context(), housekeepingTenantA, bookmark)
	if err != nil || len(combined.Indexed) != 0 {
		t.Fatalf("combined local precedence: %+v %v", combined, err)
	}
	duplicateRequest.Query.Origins = nil
	duplicatePage, err = store.SearchIndexedCatalog(t.Context(), housekeepingTenantA, duplicateRequest)
	if err != nil || len(duplicatePage.Matches) != 0 {
		t.Fatalf("selected local precedence: %+v %v", duplicatePage, err)
	}
	ir.Query.License = "not-the-license"
	result, err = store.SearchIndexedCatalog(t.Context(), housekeepingTenantA, ir)
	if err != nil || len(result.Matches) != 0 {
		t.Fatalf("indexed filter: %+v %v", result, err)
	}
	ir.Query = query
	ir.ExpectedGeneration = ""
	result, err = store.SearchIndexedCatalog(t.Context(), housekeepingTenantB, ir)
	if err != nil || len(result.Matches) != 0 {
		t.Fatalf("indexed tenant: %+v %v", result, err)
	}
	ir.IdentityProbes = make([]catalog.IndexedIdentity, 51)
	if _, err := store.SearchIndexedCatalog(t.Context(), housekeepingTenantA, ir); catalog.ErrorCodeOf(err) != catalog.CodeInvalidArgument {
		t.Fatalf("indexed bound: %v", err)
	}
}
