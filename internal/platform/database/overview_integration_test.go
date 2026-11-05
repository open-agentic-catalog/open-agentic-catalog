//go:build integration

package database

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/adapter"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/clock"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/id"
)

func TestPostgresOverviewLocalCeilingAndIndependentReleasePopulation(t *testing.T) {
	db, repo, _, seed := newSearchCostFixture(t)
	author := semanticDisclosureActor(t, db, housekeepingTenantA)
	key := sha256.Sum256([]byte(t.Name()))
	service, err := catalog.NewService(catalog.Options{Repository: repo, Clock: clock.Real{}, IDs: id.Generator{}, Validator: adapter.NewRegistry(), CursorKey: key[:], Policy: "overview-test"})
	if err != nil {
		t.Fatal(err)
	}
	empty, err := service.ResourceOverview(author)
	if err != nil || empty.Total.Value != 0 || len(empty.Kinds) != 4 {
		t.Fatal(empty, err)
	}
	seed(0)
	rows, err := service.ListResources(author, catalog.ResourceListRequest{Limit: 2})
	if err != nil || len(rows) != 1 {
		t.Fatal(rows, err)
	}
	resource := rows[0]
	if _, err = db.operator.Exec(t.Context(), `INSERT INTO resources(id,tenant_id,namespace_id,canonical_identifier,kind,created_by,revision,created_at,updated_at)
 SELECT gen_random_uuid(),$1,$2,'urn:air:catalog.example.com:performance:overview-'||n,'plugin',$3,1,clock_timestamp(),clock_timestamp() FROM generate_series(1,9999) n`, housekeepingTenantA, resource.NamespaceID, resource.CreatedBy); err != nil {
		t.Fatal(err)
	}
	exact, err := service.ResourceOverview(author)
	if err != nil || exact.Total != (catalog.BoundedCount{Value: 10000}) || exact.Kinds[3].Count != 9999 {
		t.Fatal(exact, err)
	}
	if _, err = db.operator.Exec(t.Context(), `INSERT INTO resources(id,tenant_id,namespace_id,canonical_identifier,kind,created_by,revision,created_at,updated_at) VALUES(gen_random_uuid(),$1,$2,'urn:air:catalog.example.com:performance:overview-overflow','agent',$3,1,clock_timestamp(),clock_timestamp())`, housekeepingTenantA, resource.NamespaceID, resource.CreatedBy); err != nil {
		t.Fatal(err)
	}
	limited, err := service.ResourceOverview(author)
	if err != nil || limited.Total != (catalog.BoundedCount{Value: 10000, Limited: true}) || limited.Kinds != nil {
		t.Fatal(limited, err)
	}
	releases, err := service.ReleaseOverview(author)
	if err != nil || releases.Published.Value != 1 || releases.Published.Limited || releases.Deprecated.Value != 0 {
		t.Fatal("drafts inflated current releases", releases, err)
	}
	listings, err := service.ListListings(author, resource.ID, catalog.HistoryPageRequest{Limit: 2})
	if err != nil || len(listings) != 1 {
		t.Fatal(listings, err)
	}
	release, err := service.GetRelease(author, listings[0].CurrentReleaseID)
	if err != nil {
		t.Fatal(err)
	}
	for _, state := range []catalog.ReleaseState{catalog.ReleaseDeprecated, catalog.ReleaseRetired} {
		release, err = service.TransitionRelease(author, release.ID, state, "synthetic overview lifecycle fixture", "", nil, release.Revision)
		if err != nil {
			t.Fatal(err)
		}
		releases, err = service.ReleaseOverview(author)
		wantDeprecated := 0
		if state == catalog.ReleaseDeprecated {
			wantDeprecated = 1
		}
		if err != nil || releases.Published != (catalog.BoundedCount{}) || releases.Deprecated != (catalog.BoundedCount{Value: wantDeprecated}) {
			t.Fatal("overview does not follow the current release lifecycle", state, releases, err)
		}
	}
	for _, measure := range []catalog.InventoryMeasure{catalog.InventoryNamespaces, catalog.InventoryPublishers} {
		directory, err := service.DirectoryOverview(author, measure)
		if err != nil || directory.Count != (catalog.BoundedCount{Value: 1}) {
			t.Fatal(directory, err)
		}
	}
	other := semanticDisclosureActor(t, db, housekeepingTenantB)
	isolated, err := service.ResourceOverview(other)
	if err != nil || isolated.Total.Value != 0 {
		t.Fatal("RLS summary crossed tenants", isolated, err)
	}
}

func TestPostgresIndexedOverviewBoundsContributionsBeforeDeduplication(t *testing.T) {
	db, repo, _, _ := newSearchCostFixture(t)
	entries := make([]map[string]any, 5000)
	for i := range entries {
		entries[i] = map[string]any{"identifier": fmt.Sprintf("urn:air:overview.example:default:agent%d", i), "version": "1", "displayName": "Overview fixture", "type": adapter.MediaA2AAgentCard, "data": map[string]any{"name": "Synthetic"}}
	}
	body, err := json.Marshal(map[string]any{"entries": entries})
	if err != nil {
		t.Fatal(err)
	}
	seedLexicalSourceBody(t, db, repo, body, "application/ard+json", "https://overview-one.example/.well-known/ard.json", len(entries))
	seedLexicalSourceBody(t, db, repo, body, "application/ard+json", "https://overview-two.example/.well-known/ard.json", len(entries))
	store, err := NewPostgresFederationStore(repo)
	if err != nil {
		t.Fatal(err)
	}
	count, limited, err := store.CountIndexedInventory(t.Context(), housekeepingTenantA)
	if err != nil || limited || count == nil || *count != 5000 {
		t.Fatal("exact ceiling duplicates", count, limited, err)
	}
	body, err = json.Marshal(map[string]any{"entries": entries[:1]})
	if err != nil {
		t.Fatal(err)
	}
	seedLexicalSourceBody(t, db, repo, body, "application/ard+json", "https://overview-three.example/.well-known/ard.json", 1)
	count, limited, err = store.CountIndexedInventory(t.Context(), housekeepingTenantA)
	if err != nil || !limited || count != nil {
		t.Fatal("invented identity lower bound", count, limited, err)
	}
	count, limited, err = store.CountIndexedInventory(t.Context(), housekeepingTenantB)
	if err != nil || limited || count == nil || *count != 0 {
		t.Fatal("cross-tenant contributions", count, limited, err)
	}
}
