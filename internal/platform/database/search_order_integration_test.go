//go:build integration

package database

import (
	"encoding/json"
	"github.com/jackc/pgx/v5"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/adapter"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/clock"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/id"
	"reflect"
	"strings"
	"testing"
)

func TestPostgresCatalogColumnOrderTraversal(t *testing.T) {
	docs := []catalog.SearchDocument{
		{Identifier: "urn:air:middle.example.com:finance:three", DisplayName: "Zulu", Description: "Shared words"},
		{Identifier: "urn:air:zulu.example.com:finance:one", DisplayName: "Alpha", Description: "Shared words"},
		{Identifier: "urn:air:alpha.example.com:finance:two", DisplayName: "Middle", Description: "Shared words"},
	}
	listings := make([]catalog.CreateListingRevisionInput, len(docs))
	db, repo, _, seed := newSearchCostFixtureWithListings(t, listings, docs...)
	service, err := catalog.NewService(catalog.Options{Repository: repo, Clock: clock.Real{}, IDs: id.Generator{}, Validator: adapter.NewRegistry(), CursorKey: []byte(strings.Repeat("k", 32)), Policy: "search-order-test"})
	if err != nil {
		t.Fatal(err)
	}
	admin := postgresFixtureContext(t, db, t.Context(), identity.Actor{TenantID: housekeepingTenantA, Subject: "order-publishers", Roles: []identity.Role{identity.RoleTenantAdmin}})
	domains := []string{"middle.example.com", "zulu.example.com", "alpha.example.com"}
	for i, name := range []string{"Middle publisher", "Zulu publisher", "Alpha publisher"} {
		publisher, createErr := service.CreatePublisher(admin, catalog.CreatePublisherInput{DisplayName: name, Domains: []string{domains[i]}})
		if createErr != nil {
			t.Fatal(createErr)
		}
		if _, approveErr := service.ApprovePublisherPublicationDomain(admin, publisher.ID, domains[i], "owned ordering fixture", publisher.Revision); approveErr != nil {
			t.Fatal(approveErr)
		}
		listings[i].PublisherID = publisher.ID
	}
	card := json.RawMessage(`{"name":"Ordering Agent","description":"Synthetic ordering fixture","version":"1.0.0","supportedInterfaces":[{"url":"https://agent.example.com/a2a","protocolBinding":"JSONRPC","protocolVersion":"1.0"}],"capabilities":{},"defaultInputModes":["text/plain"],"defaultOutputModes":["text/plain"],"skills":[{"id":"ordering","name":"Ordering","description":"Read metadata only","tags":["ordering"]}]}`)
	seed(0, card)
	seed(1)
	seed(2, card)
	remote := []catalog.SearchDocument{
		{Identifier: "urn:air:zulu.remote.example:remote:first", DisplayName: "Bravo", Description: "Shared words"},
		{Identifier: "urn:air:alpha.remote.example:remote:second", DisplayName: "Yankee", Description: "Shared words"},
	}
	seedLexicalSource(t, db, repo, remote)
	for _, tc := range []struct {
		order          catalog.SearchOrder
		local, indexed []string
	}{
		{catalog.SearchOrderNameAsc, []string{"Alpha", "Middle", "Zulu"}, []string{"Bravo", "Yankee"}},
		{catalog.SearchOrderNameDesc, []string{"Zulu", "Middle", "Alpha"}, []string{"Yankee", "Bravo"}},
		{catalog.SearchOrderKindAsc, []string{"Middle", "Zulu", "Alpha"}, []string{"Bravo", "Yankee"}},
		{catalog.SearchOrderKindDesc, []string{"Alpha", "Middle", "Zulu"}, []string{"Bravo", "Yankee"}},
		{catalog.SearchOrderPublisherAsc, []string{"Middle", "Zulu", "Alpha"}, []string{"Yankee", "Bravo"}},
		{catalog.SearchOrderPublisherDesc, []string{"Alpha", "Zulu", "Middle"}, []string{"Bravo", "Yankee"}},
	} {
		t.Run(string(tc.order), func(t *testing.T) {
			q := catalog.SearchQuery{Text: "Shared", Order: tc.order}
			var after *catalog.CatalogSearchPosition
			names := []string{}
			for i := 0; i < 4; i++ {
				page, err := repo.SearchCatalog(t.Context(), housekeepingTenantA, catalog.CatalogSearchRepositoryRequest{Query: q, After: after, Limit: 1})
				if err != nil {
					t.Fatal(err)
				}
				if len(page.Items) == 0 {
					break
				}
				item := page.Items[0]
				names = append(names, item.ListingRevision.DisplayName)
				position := catalog.CatalogSearchResultPosition(catalog.CatalogSearchResult{DisplayName: item.ListingRevision.DisplayName, Kind: item.Resource.Kind, Publisher: item.ListingRevision.PublisherID, Local: &catalog.CatalogSearchLocal{Display: item.Display}, ID: "local:" + item.Release.ID}, q.Order)
				after = &position
			}
			if !reflect.DeepEqual(names, tc.local) {
				t.Fatal("local traversal", names)
			}
			after = nil
			names = []string{}
			for i := 0; i < 3; i++ {
				page, err := db.federation.SearchIndexedCatalog(t.Context(), housekeepingTenantA, catalog.FederatedCatalogSearchRequest{Query: q, After: after, Limit: 1})
				if err != nil {
					t.Fatal(err)
				}
				if len(page.Indexed) == 0 {
					break
				}
				item := page.Indexed[0]
				names = append(names, item.DisplayName)
				position := catalog.CatalogSearchResultPosition(item, q.Order)
				after = &position
			}
			if !reflect.DeepEqual(names, tc.indexed) {
				t.Fatal("indexed traversal", names)
			}
			other, err := repo.SearchCatalog(t.Context(), housekeepingTenantB, catalog.CatalogSearchRepositoryRequest{Query: q, Limit: 1})
			if err != nil || other.Total != 0 || len(other.Items) != 0 {
				t.Fatal("cross tenant", other, err)
			}
		})
	}
	before, err := repo.SearchCatalog(t.Context(), housekeepingTenantA, catalog.CatalogSearchRepositoryRequest{Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	orderedGenerations := map[catalog.SearchOrder]string{}
	for _, order := range []catalog.SearchOrder{catalog.SearchOrderPublisherAsc, catalog.SearchOrderPublisherDesc} {
		page, err := repo.SearchCatalog(t.Context(), housekeepingTenantA, catalog.CatalogSearchRepositoryRequest{Query: catalog.SearchQuery{Text: "Shared", Order: order}, Limit: 1})
		if err != nil || !page.HasMore {
			t.Fatal("ordered rename control", page, err)
		}
		orderedGenerations[order] = page.Generation
	}
	if _, err = db.operator.Exec(t.Context(), `UPDATE publishers SET display_name='Renamed publisher',revision=revision+1,updated_at=updated_at+interval '1 second' WHERE tenant_id=$1`, housekeepingTenantA); err != nil {
		t.Fatal(err)
	}
	if _, err = repo.SearchCatalog(t.Context(), housekeepingTenantA, catalog.CatalogSearchRepositoryRequest{ExpectedGeneration: before.Generation, Limit: 1}); catalog.ErrorCodeOf(err) != catalog.CodeInvalidArgument {
		t.Fatal("rename retained old cursor", err)
	}
	for order, generation := range orderedGenerations {
		_, err := repo.SearchCatalog(t.Context(), housekeepingTenantA, catalog.CatalogSearchRepositoryRequest{Query: catalog.SearchQuery{Text: "Shared", Order: order}, ExpectedGeneration: generation, Limit: 1})
		if catalog.ErrorCodeOf(err) != catalog.CodeInvalidArgument {
			t.Fatal("publisher rename retained ordered cursor", order, err)
		}
	}
	_, err = withTenantCollectionRead(t.Context(), db.operator, housekeepingTenantA, func(tx pgx.Tx) (bool, error) {
		var schema string
		if err := tx.QueryRow(t.Context(), "SELECT current_schema()").Scan(&schema); err != nil {
			return false, err
		}
		return true, verifyRestoreTriggers(t.Context(), tx, schema)
	})
	if err != nil {
		t.Fatal("trigger inventory", err)
	}
}
