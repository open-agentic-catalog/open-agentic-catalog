//go:build integration

package database

import (
	"context"
	"crypto/sha256"
	"maps"
	"strings"
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/adapter"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/federation"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/clock"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/fetcher"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/id"
)

type searchCostInterleave struct {
	searcher catalog.FederatedCatalogSearcher
	before   func()
	after    func()
}

func (s *searchCostInterleave) SearchFederatedCatalog(ctx context.Context, request catalog.FederatedCatalogSearchRequest) (catalog.FederatedCatalogSearchPage, error) {
	if s.before != nil {
		before := s.before
		s.before = nil
		before()
	}
	page, err := s.searcher.SearchFederatedCatalog(ctx, request)
	if s.after != nil {
		after := s.after
		s.after = nil
		after()
	}
	return page, err
}

func TestPostgresComposedSearchQueryCost(t *testing.T) {
	db, repository, counts, seed := newSearchCostFixture(t)
	key := sha256.Sum256([]byte(t.Name()))
	service, err := catalog.NewService(catalog.Options{Repository: repository, Clock: clock.Real{}, IDs: id.Generator{}, Validator: adapter.NewRegistry(), CursorKey: key[:], Policy: "composed-cost"})
	if err != nil {
		t.Fatal(err)
	}
	store, err := NewPostgresFederationStore(repository)
	if err != nil {
		t.Fatal(err)
	}
	// No approved live source: the actual manager still performs bounded live
	// topology selection for a lexical first page, but never contacts a host.
	outbound := &postgresFederationFetcher{policyDigest: "sha256:" + strings.Repeat("a", 64)}
	manager, err := federation.NewManager(store, outbound, clock.Real{}, id.Generator{})
	if err != nil {
		t.Fatal(err)
	}
	interleave := &searchCostInterleave{searcher: manager}
	search, err := catalog.NewSearchService(service, interleave)
	if err != nil {
		t.Fatal(err)
	}
	ctx := postgresFixtureContext(t, db, t.Context(), identity.Actor{TenantID: housekeepingTenantA, Subject: "cost-reader", Roles: []identity.Role{identity.RoleReader}})
	check := func(ctx context.Context, query catalog.SearchQuery, total, length, selects, withs, transactions int) catalog.CatalogSearchResponse {
		t.Helper()
		counts.reset()
		page, err := search.Search(ctx, query)
		if err != nil {
			t.Fatal(err)
		}
		if page.Partial || page.LiveCount != 0 || page.DurableTotal != total || len(page.Items) != length {
			t.Fatalf("unexpected composed result: total=%d length=%d partial=%t", page.DurableTotal, len(page.Items), page.Partial)
		}
		// One permission statement is saved on the two-transaction path, two
		// on the three-transaction path. Collection-policy work is unchanged.
		if counts.counts["select"] != selects+transactions+3 || counts.counts["with"] != withs || counts.counts["begin"] != transactions || counts.counts["commit"] != transactions {
			t.Fatalf("query=%+v counts=%v want select=%d with=%d tx=%d", query, counts.counts, selects+transactions+3, withs, transactions)
		}
		seen := map[string]bool{}
		for _, item := range page.Items {
			if item.Local == nil && item.Indexed == nil || item.Local != nil && item.Local.Resource.TenantID != housekeepingTenantA || seen[item.ID] {
				t.Fatal("tenant/duplicate result")
			}
			seen[item.ID] = true
		}
		t.Logf("page=%d total=%d statement_classes=%v", length, total, counts.counts)
		return page
	}
	// Local combines count and page selection; indexed skips its page after a
	// zero count. Both retain context and generation checks; indexed selection
	// now additionally binds its current collection policy in two statements. The empty
	// indexed corpus is intentional, not an omitted composed repository call.
	check(ctx, catalog.SearchQuery{Limit: 100}, 0, 0, 4, 2, 2)
	for i := 0; i < 101; i++ {
		seed(i)
	}
	first := check(ctx, catalog.SearchQuery{Limit: 100}, 101, 100, 6, 2, 2)
	if first.NextCursor == "" {
		t.Fatal("lookahead lost continuation")
	}
	last := check(ctx, catalog.SearchQuery{Limit: 100, Cursor: first.NextCursor}, 101, 1, 6, 2, 2)
	if last.NextCursor != "" {
		t.Fatal("terminal page has cursor")
	}
	check(ctx, catalog.SearchQuery{Text: "Performance", Limit: 20}, 101, 20, 8, 2, 3)
	other := postgresFixtureContext(t, db, t.Context(), identity.Actor{TenantID: housekeepingTenantB, Subject: "other-reader", Roles: []identity.Role{identity.RoleReader}})
	check(other, catalog.SearchQuery{Limit: 100}, 0, 0, 4, 2, 2)
	interleave.before = func() {
		// Commit the concurrent fixture change between the real repository
		// reads; exclude only these setup writes from request attribution.
		before := maps.Clone(counts.counts)
		seed(101)
		counts.counts = before
	}
	// The reconciliation read additionally binds policy after federation admission.
	check(ctx, catalog.SearchQuery{Limit: 100}, 102, 100, 12, 3, 3)
	change := func(index int) func() {
		return func() {
			before := maps.Clone(counts.counts)
			seed(index)
			counts.counts = before
		}
	}
	interleave.before, interleave.after = change(102), change(103)
	counts.reset()
	reconciled, err := search.Search(ctx, catalog.SearchQuery{Limit: 100})
	// The four permission decisions each save their separate revision read.
	if err != nil || reconciled.DurableTotal != 104 || len(reconciled.Items) != 100 || counts.counts["select"] != 28 || counts.counts["with"] != 4 || counts.counts["begin"] != 5 || counts.counts["commit"] != 4 || counts.counts["rollback"] != 1 {
		t.Fatalf("repeated committed generation race: total=%d page=%d statements=%v err=%v", reconciled.DurableTotal, len(reconciled.Items), counts.counts, err)
	}
	if _, err := search.Search(ctx, catalog.SearchQuery{Limit: 100, Cursor: first.NextCursor}); catalog.ErrorCodeOf(err) != catalog.CodeInvalidArgument {
		t.Fatalf("stale cursor accepted: %v", err)
	}
	principal, err := ProvisionIdentity(t.Context(), db.operator, IdentityProvisioningInput{TenantID: housekeepingTenantA, TenantDisplayName: "Housekeeping tenant A", Issuer: "https://identity.performance.example", TenancyMode: "dedicated", Subject: "cost-source-admin", ActorType: identity.ActorHuman, Roles: []identity.Role{identity.RoleSourceAdmin}, Operator: "search-cost-test", Reason: "provision synthetic source identity", AcknowledgeOutbox: true})
	if err != nil {
		t.Fatal(err)
	}
	admin := postgresFixtureContext(t, db, t.Context(), identity.Actor{TenantID: housekeepingTenantA, Subject: "cost-source-admin", Roles: []identity.Role{identity.RoleSourceAdmin}, MembershipID: principal.MembershipID, RealmID: principal.RealmID, ActorType: principal.ActorType})
	outbound.result = fetcher.Result{Status: 200, MediaType: "application/ard+json", FetchedAt: time.Now(), Body: []byte(`{"entries":[{"identifier":"urn:air:remote.example:default:one","displayName":"Remote indexed","type":"application/a2a-agent-card+json","data":{"name":"Remote"},"tags":["selected"]}]}`)}
	inspection, err := manager.Inspect(admin, "https://catalog.remote.example/.well-known/ard.json")
	if err != nil {
		t.Fatal(err)
	}
	source, err := manager.Connect(admin, federation.ConnectInput{InspectionID: inspection.ID, DisplayName: "Owned indexed source", Scope: federation.Scope{Tags: []string{"selected"}}, RetrievalMode: federation.RetrievalIndexed, Reason: "Approve synthetic indexed source"})
	if err != nil {
		t.Fatal(err)
	}
	workerRepository, err := NewPostgresRepository(db.worker)
	if err != nil {
		t.Fatal(err)
	}
	workerStore, err := NewPostgresFederationStore(workerRepository)
	if err != nil {
		t.Fatal(err)
	}
	workerManager, err := federation.NewManager(workerStore, outbound, clock.Real{}, id.Generator{})
	if err != nil {
		t.Fatal(err)
	}
	outbound.beforeReturn = func() error { outbound.result.FetchedAt = time.Now(); return nil }
	if snapshot, err := syncClaimedFixture(t, db, admin, workerManager, source.ID); err != nil || snapshot.Added != 1 {
		t.Fatalf("indexed seed: added=%d error=%v", snapshot.Added, err)
	}
	check(ctx, catalog.SearchQuery{Text: "Remote", Limit: 20}, 1, 1, 6, 3, 3)
	check(ctx, catalog.SearchQuery{Limit: 100}, 105, 100, 6, 3, 2)
	check(other, catalog.SearchQuery{Text: "Remote", Limit: 20}, 0, 0, 6, 2, 3)
}
