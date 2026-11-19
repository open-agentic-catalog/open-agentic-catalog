//go:build integration

package database

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/adapter"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/clock"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/id"
	"github.com/open-agentic-catalog/open-agentic-catalog/migrations"
)

func TestPostgresAgentProtocolSelectionAndAuthority(t *testing.T) {
	for _, native := range []bool{false, true} {
		t.Run(fmt.Sprintf("native=%t", native), func(t *testing.T) { testPostgresAgentProtocolSelectionAndAuthority(t, native) })
	}
}

func testPostgresAgentProtocolSelectionAndAuthority(t *testing.T, native bool) {
	t.Helper()
	documents := make([]catalog.SearchDocument, 9)
	listings := make([]catalog.CreateListingRevisionInput, 9)
	for i := range documents {
		documents[i] = catalog.SearchDocument{Identifier: fmt.Sprintf("urn:air:catalog.example.com:finance:protocol-%d", i), DisplayName: fmt.Sprintf("Protocol %d", i), Description: "Retained declaration selection", Tags: []string{"protocol"}}
	}
	documents[8].Identifier = "opaque-private-agent"
	listings[8].Visibility = catalog.VisibilityPrivate
	db, repo, _, seed := newSearchCostFixtureWithListings(t, listings, documents...)
	raw, err := os.ReadFile("../../../samples/cards/dice-rest.json")
	if err != nil {
		t.Fatal(err)
	}
	card := func(bindings ...string) json.RawMessage {
		t.Helper()
		var body map[string]any
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Fatal(err)
		}
		interfaces := make([]any, 0, len(bindings))
		for _, binding := range bindings {
			interfaces = append(interfaces, map[string]any{"url": "https://inert.example/agent", "protocolBinding": binding, "protocolVersion": "0.3"})
		}
		body["supportedInterfaces"] = interfaces
		body["capabilities"] = map[string]any{"streaming": false}
		data, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
	seed(0, card("HTTP+JSON"))
	seed(2, card("JSONRPC"))
	seed(3, card("JSONRPC", "HTTP+JSON"))
	seed(4, card("私有+V1 "))
	seed(5, card("http+json"))
	seed(6, card("HTTP+JSON"), card("GRPC"))
	seed(7, json.RawMessage(strings.ReplaceAll(strings.ReplaceAll(string(card("HTTP+JSON")), "supportedInterfaces", "supported_interfaces"), "protocolBinding", "protocol_binding")))
	seed(8, card("HTTP+JSON"))
	q := catalog.SearchQuery{AgentProtocol: "HTTP+JSON", Origins: []catalog.SearchResultType{catalog.SearchResultLocal}, Limit: 2}
	if native {
		streaming := false
		q.NativeCapabilities = &catalog.NativeCapabilitiesFilter{AgentStreaming: &streaming}
	}
	seen := map[string]bool{}
	var after *catalog.CatalogSearchPosition
	generation := ""
	for {
		page, err := repo.SearchCatalog(t.Context(), housekeepingTenantA, catalog.CatalogSearchRepositoryRequest{Query: q, Limit: 2, After: after, ExpectedGeneration: generation})
		if err != nil || page.Total != 5 || len(page.Items) > 2 {
			t.Fatalf("wrong pre-page population: %+v %v", page, err)
		}
		generation = page.Generation
		for _, item := range page.Items {
			if seen[item.Release.ID] {
				t.Fatal("duplicate page")
			}
			seen[item.Release.ID] = true
			if len(item.VersionRevision.ID) == 0 {
				t.Fatal("missing exact revision")
			}
		}
		if !page.HasMore {
			break
		}
		last := page.Items[len(page.Items)-1]
		after = &catalog.CatalogSearchPosition{Score: last.RelevanceScore, DisplayName: catalog.CatalogSearchSortName(last.ListingRevision.DisplayName), ResultID: "local:" + last.Release.ID}
	}
	if len(seen) != 5 {
		t.Fatal("lost matches", seen)
	}
	for _, tc := range []struct {
		binding string
		want    int
	}{{"JSONRPC", 2}, {"GRPC", 0}, {"私有+V1 ", 1}, {"私有+V1", 0}, {"http+json", 1}} {
		binding, want := tc.binding, tc.want
		q.AgentProtocol = binding
		page, err := repo.SearchCatalog(t.Context(), housekeepingTenantA, catalog.CatalogSearchRepositoryRequest{Query: q, Limit: 2})
		if err != nil || page.Total != want {
			t.Fatal(binding, page.Total, err)
		}
	}
	q.AgentProtocol = "HTTP+JSON"
	facets, err := repo.ExploreCatalog(t.Context(), housekeepingTenantA, q, []catalog.FacetRequest{{Field: "kind", Limit: 10}})
	if err != nil || facets.Matched != 5 || len(facets.Facets["kind"].Buckets) != 1 || facets.Facets["kind"].Buckets[0].Count != 5 {
		t.Fatal("facet population", facets, err)
	}
	other, err := repo.SearchCatalog(t.Context(), housekeepingTenantB, catalog.CatalogSearchRepositoryRequest{Query: q, Limit: 2})
	if err != nil || other.Total != 0 {
		t.Fatal("foreign tenant disclosure", err)
	}

	author := semanticDisclosureActor(t, db, housekeepingTenantA)
	// Diagnose the actual bound page statement under current API authority.
	// Both retention forms use stored arrays, with no per-request JSON parsing.
	planContext, _, err := identity.WithCollectionContext(author)
	if err != nil {
		t.Fatal(err)
	}
	_, err = withSearchDiagnosticTx(planContext, db.api, func(tx pgx.Tx) (bool, error) {
		if err := initializeCollectionPolicy(planContext, tx, housekeepingTenantA); err != nil {
			return false, err
		}
		_, pageSQL := catalogSearchStatements(q)
		args := append(catalogSearchParameters(housekeepingTenantA, q), false, 0, "", "", 2)
		statement, args := agentProtocolQuery(pageSQL, args, q)
		var raw []byte
		if err := tx.QueryRow(planContext, "EXPLAIN (ANALYZE,BUFFERS,TIMING OFF,FORMAT JSON) "+statement, append([]any{pgx.QueryExecModeExec}, args...)...).Scan(&raw); err != nil {
			return false, err
		}
		var plans []struct {
			Plan searchPlanNode `json:"Plan"`
		}
		if err := json.Unmarshal(raw, &plans); err != nil {
			return false, err
		}
		if len(plans) != 1 || plans[0].Plan.Rows != 2 {
			t.Fatal("page bound missing from plan")
		}
		var walk func(searchPlanNode)
		walk = func(node searchPlanNode) {
			if node.Relation == "representations" && node.Rows*node.Loops > 128 {
				t.Fatal("unbounded representation scan", node.Rows, node.Loops)
			}
			if node.Temp != 0 {
				t.Fatal("unexpected protocol plan spill")
			}
			for _, child := range node.Plans {
				walk(child)
			}
		}
		walk(plans[0].Plan)
		return true, nil
	})
	if err != nil {
		t.Fatal("protocol page plan", err)
	}
	key := sha256.Sum256([]byte(t.Name()))
	external := &postgresExternalFetcher{at: time.Now().UTC(), policyDigest: "sha256:" + strings.Repeat("e", 64)}
	service, err := catalog.NewService(catalog.Options{ExternalFetcher: external, Repository: repo, Clock: clock.Real{}, IDs: id.Generator{}, Validator: adapter.NewRegistry(), CursorKey: key[:], Policy: "protocol-test"})
	if err != nil {
		t.Fatal(err)
	}
	page, err := service.Search(author, q)
	if err != nil || page.NextCursor == "" {
		t.Fatal("missing cursor", err)
	}
	changed := q
	changed.AgentProtocol = "JSONRPC"
	changed.Cursor = page.NextCursor
	if _, err := service.Search(author, changed); catalog.ErrorCodeOf(err) != catalog.CodeInvalidArgument {
		t.Fatal("cursor changed selection", err)
	}
	// Exercise the Control Search service's signed cursors against real SQL,
	// including the explicit-order path rather than only repository positions.
	storeForSearch, err := NewPostgresFederationStore(repo)
	if err != nil {
		t.Fatal(err)
	}
	search, err := catalog.NewSearchService(service, &searchCostInterleave{searcher: postgresIndexedCatalogSearcher{store: storeForSearch}, before: func() {
		t.Fatal("local-only protocol query reached federation search")
	}})
	if err != nil {
		t.Fatal(err)
	}
	for _, ordered := range []struct {
		order catalog.SearchOrder
		names []string
	}{
		{names: []string{"Protocol 0", "Protocol 3", "Protocol 6", "Protocol 7", "Protocol 8"}},
		{order: catalog.SearchOrderNameDesc, names: []string{"Protocol 8", "Protocol 7", "Protocol 6", "Protocol 3", "Protocol 0"}},
	} {
		query := q
		query.Order = ordered.order
		names := make([]string, 0, len(ordered.names))
		ids, cursors := map[string]bool{}, map[string]bool{}
		for number := range 3 {
			part, err := search.Search(author, query)
			wantSize := 2
			if number == 2 {
				wantSize = 1
			}
			if err != nil || part.DurableTotal != 5 || len(part.Items) != wantSize || part.Partial || part.LiveCount != 0 {
				t.Fatal("Control protocol page lost selection or bounds", ordered.order, number, part, err)
			}
			for _, item := range part.Items {
				if item.Type != catalog.SearchResultLocal || item.Local == nil || !seen[item.Local.Release.ID] || ids[item.ID] {
					t.Fatal("Control continuation changed exact release membership", item)
				}
				ids[item.ID] = true
				names = append(names, item.DisplayName)
			}
			if number == 2 {
				if part.NextCursor != "" {
					t.Fatal("Control protocol traversal did not terminate")
				}
			} else {
				if part.NextCursor == "" || cursors[part.NextCursor] {
					t.Fatal("Control protocol traversal ended early or repeated a cursor")
				}
				cursors[part.NextCursor] = true
				query.Cursor = part.NextCursor
			}
			if number == 0 {
				wrong := query
				wrong.AgentProtocol = "JSONRPC"
				if _, err := search.Search(author, wrong); catalog.ErrorCodeOf(err) != catalog.CodeInvalidArgument {
					t.Fatal("Control continuation accepted a changed binding", err)
				}
			}
		}
		if !reflect.DeepEqual(names, ordered.names) {
			t.Fatal("Control protocol order or page membership changed", names, ordered.names)
		}
	}
	selected := page.Items[0]
	retained, err := repo.GetCatalogRecord(author, housekeepingTenantA, selected.Release.ID)
	if err != nil {
		t.Fatal(err)
	}
	var draftCard map[string]any
	if err := json.Unmarshal(card("GRPC"), &draftCard); err != nil {
		t.Fatal(err)
	}
	draftCard["version"] = "2.0.0"
	// Valid native extension data can retain escaped NUL in a blob. It must not hide the declared binding.
	draftCard["capabilities"] = map[string]any{"streaming": false, "extensions": []any{map[string]any{"uri": "https://extensions.example.test/projection", "params": map[string]any{"unrelated": "\x00"}}}}
	draftBytes, err := json.Marshal(draftCard)
	if err != nil {
		t.Fatal(err)
	}
	external.body = draftBytes
	_, draftRevision, _, err := service.CreateVersion(author, retained.Resource.ID, catalog.CreateVersionInput{Version: "2.0.0", Representations: []catalog.RepresentationInput{{Role: catalog.RepresentationPrimary, MediaType: adapter.MediaA2AAgentCard, URL: "https://cards.example.test/agent.json"}}})
	if err != nil {
		t.Fatal(err)
	}
	fresh, err := service.Search(author, q)
	if err != nil || fresh.Total != 5 {
		t.Fatal("draft changed released selection", err)
	}

	store, err := NewPostgresIdentityStore(repo)
	if err != nil {
		t.Fatal(err)
	}
	authority, err := identity.NewService(identity.ServiceOptions{Store: store, Clock: clock.Real{}, IDs: id.Generator{}, CursorKey: key[:]})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = authority.GrantRole(author, identity.GrantRoleInput{Subject: "protocol-metadata", ActorType: identity.ActorHuman, Role: identity.RoleServiceClient, Reason: "Own protocol metadata-only fixture"}, "member"); err != nil {
		t.Fatal(err)
	}
	actor, err := store.ResolvePrincipal(t.Context(), identity.Principal{TenantID: housekeepingTenantA, Issuer: "https://identity.performance.example", Subject: "protocol-metadata"})
	if err != nil {
		t.Fatal(err)
	}
	role, err := authority.SaveRoleDefinition(author, "", 0, identity.RoleDefinitionInput{Name: "Protocol metadata", Permissions: []identity.Permission{"resource.read"}, Reason: "Separate metadata from card authority"}, false, "metadata-role")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = authority.GrantAuthorizationAssignment(author, identity.AccessAssignmentInput{RoleDefinitionID: role.ID, MembershipID: actor.MembershipID, Reason: "Allow metadata only"}, "metadata"); err != nil {
		t.Fatal(err)
	}
	ctx := identity.WithActor(t.Context(), actor)
	ordinary, err := service.Search(ctx, catalog.SearchQuery{Limit: 2})
	if err != nil || ordinary.Total != 8 {
		t.Fatal("metadata control", ordinary.Total, err)
	}
	denied, err := service.Search(ctx, q)
	if err != nil || denied.Total != 0 || len(denied.Items) != 0 {
		t.Fatal("private card facts leaked", denied, err)
	}
	facetQuery := q
	facetQuery.Limit = 0
	empty, err := service.Explore(ctx, catalog.ExploreRequest{Query: facetQuery, Facets: []catalog.FacetRequest{{Field: "kind"}}})
	if err != nil || empty.Matched != 0 || len(empty.Facets["kind"].Buckets) != 0 {
		t.Fatal("private facet leaked", empty, err)
	}
	content, err := authority.SaveRoleDefinition(author, "", 0, identity.RoleDefinitionInput{Name: "Protocol content", Permissions: []identity.Permission{"resource.read_content"}, Reason: "Explicit card authority"}, false, "content-role")
	if err != nil {
		t.Fatal(err)
	}
	otherNamespace, err := service.CreateNamespace(author, catalog.CreateNamespaceInput{Slug: "other-protocol", DisplayName: "Other protocol scope"})
	if err != nil {
		t.Fatal(err)
	}
	wrong, err := authority.GrantAuthorizationAssignment(author, identity.AccessAssignmentInput{RoleDefinitionID: content.ID, MembershipID: actor.MembershipID, NamespaceID: otherNamespace.ID, Reason: "Separate namespace control"}, "wrong-content")
	if err != nil {
		t.Fatal(err)
	}
	if out, err := service.Search(ctx, q); err != nil || out.Total != 0 {
		t.Fatal("out-of-scope card leaked", out, err)
	}
	if _, err := authority.RevokeAuthorizationAssignment(author, wrong.ID, wrong.Revision, "Remove wrong scope", "revoke-wrong"); err != nil {
		t.Fatal(err)
	}
	assignment, err := authority.GrantAuthorizationAssignment(author, identity.AccessAssignmentInput{RoleDefinitionID: content.ID, MembershipID: actor.MembershipID, NamespaceID: retained.Listing.NamespaceID, Reason: "Allow selected declaration reads"}, "content")
	if err != nil {
		t.Fatal(err)
	}
	admitted, err := service.Search(ctx, q)
	if err != nil || admitted.Total != 5 || admitted.NextCursor == "" {
		t.Fatal("content control", admitted, err)
	}
	if _, err := authority.RevokeAuthorizationAssignment(author, assignment.ID, assignment.Revision, "Revoke card authority", "revoke-content"); err != nil {
		t.Fatal(err)
	}
	q.Cursor = admitted.NextCursor
	if _, err := service.Search(ctx, q); catalog.ErrorCodeOf(err) != catalog.CodeInvalidArgument {
		t.Fatal("revoked cursor accepted", err)
	}
	q.Cursor = ""
	if out, err := service.Search(ctx, q); err != nil || out.Total != 0 || len(out.Items) != 0 {
		t.Fatal("revoked content still matched", out, err)
	}
	if out, err := service.Explore(ctx, catalog.ExploreRequest{Query: facetQuery, Facets: []catalog.FacetRequest{{Field: "kind"}}}); err != nil || out.Matched != 0 || len(out.Facets["kind"].Buckets) != 0 {
		t.Fatal("revoked content still counted", out, err)
	}
	private, err := service.Search(author, catalog.SearchQuery{AgentProtocol: "HTTP+JSON", Origins: q.Origins, Visibility: catalog.VisibilityPrivate, Limit: 25})
	if err != nil || private.Total != 1 || len(private.Items) != 1 || private.Items[0].Resource.CanonicalIdentifier != "opaque-private-agent" {
		t.Fatal("private opaque declaration not searchable", private, err)
	}
	deprecated, err := service.TransitionRelease(author, retained.Release.ID, catalog.ReleaseDeprecated, "Protocol lifecycle control", "", nil, retained.Release.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if out, err := service.Search(author, q); err != nil || out.Total != 5 {
		t.Fatal("deprecation removed discovery", out, err)
	}
	if _, err := service.TransitionRelease(author, deprecated.ID, catalog.ReleaseRevoked, "Withdraw selected fixture", "", nil, deprecated.Revision); err != nil {
		t.Fatal(err)
	}
	if out, err := service.Search(author, q); err != nil || out.Total != 4 {
		t.Fatal("withdrawal still searchable", out, err)
	}

	// Activate the new version through independent review. Its retained blob,
	// rather than inline JSON, now supplies the released declaration.
	listing, err := service.GetListing(author, retained.Listing.ID)
	if err != nil {
		t.Fatal(err)
	}
	listing, _, err = service.CreateListingRevision(author, listing.ID, catalog.CreateListingRevisionInput{VersionRevisionID: draftRevision.ID, PublisherID: retained.ListingRevision.PublisherID, DisplayName: retained.ListingRevision.DisplayName, Description: retained.ListingRevision.Description, Visibility: retained.ListingRevision.Visibility, Tags: retained.ListingRevision.Tags}, listing.Revision)
	if err != nil {
		t.Fatal(err)
	}
	submitted, err := service.Submit(author, listing.ID)
	if err != nil {
		t.Fatal(err)
	}
	reviewer, err := store.ResolvePrincipal(t.Context(), identity.Principal{TenantID: housekeepingTenantA, Issuer: "https://identity.performance.example", Subject: "search-reviewer"})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := service.DecideSubmission(identity.WithActor(t.Context(), reviewer), submitted.ID, "approve", "Review the new declared binding", submitted.Revision); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := service.Publish(author, listing.ID, catalog.PublishInput{SubmissionID: submitted.ID}, listing.Revision); err != nil {
		t.Fatal(err)
	}
	q.Cursor = ""
	if out, err := service.Search(author, q); err != nil || out.Total != 4 {
		t.Fatal("new release broadened old protocol", out, err)
	}
	q.AgentProtocol = "GRPC"
	calls := external.requests
	if out, err := service.Search(author, q); err != nil || out.Total != 1 || len(out.Items) != 1 || out.Items[0].VersionRevision.ID != draftRevision.ID {
		t.Fatal("active retained blob did not match", out, err)
	}

	if external.requests != calls {
		t.Fatal("protocol discovery fetched an artifact")
	}
}

func TestPostgresAgentProtocolProjectionAndPrivileges(t *testing.T) {
	db := newHousekeepingDatabase(t)
	tx, err := db.operator.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer rollbackForCleanup(t.Context(), tx)()
	var schema string
	if err := tx.QueryRow(t.Context(), "SELECT current_schema()").Scan(&schema); err != nil {
		t.Fatal(err)
	}
	if err := verifyRestoreConstraints(t.Context(), tx, schema); err != nil {
		t.Fatal(err)
	}
	if err := tx.Rollback(t.Context()); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		media, raw string
		want       []string
	}{
		{"Application/A2A-Agent-Card+JSON; charset=utf-8", `{"supportedInterfaces":[{"protocolBinding":"JSONRPC"},{"protocol_binding":"HTTP+JSON"}]}`, []string{"HTTP+JSON", "JSONRPC"}},
		{adapter.MediaA2AAgentCard, `{"unrelated":"\u0000","supportedInterfaces":[{"protocolBinding":"HTTP+JSON"},{"protocolBinding":"JSONRPC"},{"protocolBinding":"HTTP+JSON"}]}`, []string{"HTTP+JSON", "JSONRPC"}},
		{adapter.MediaA2AAgentCard, `{"supportedInterfaces":[{"protocolBinding":"\u0000"},{"protocolBinding":"GRPC"}]}`, []string{"GRPC"}},
		{adapter.MediaA2AAgentCard, `{"supportedInterfaces":[{"protocolBinding":"\\u0000"},{"protocolBinding":"foo\\\\bar"}]}`, []string{`\u0000`, `foo\\bar`}},
		{"text/plain", `{"supportedInterfaces":[{"protocolBinding":"GRPC"}]}`, []string{}},
		{adapter.MediaA2AAgentCard, "invalid JSON", []string{}},
	} {
		var got []string
		if err := db.api.QueryRow(t.Context(), "SELECT catalog_agent_protocol_bindings_v1($1,$2,NULL)", tc.media, []byte(tc.raw)).Scan(&got); err != nil || !reflect.DeepEqual(got, tc.want) {
			t.Fatal(tc.raw, got, err)
		}
	}
	var allowed bool
	for _, table := range []string{"document_blobs", "representations"} {
		if err := db.worker.QueryRow(t.Context(), "SELECT has_column_privilege(current_user,$1,'agent_protocol_bindings','SELECT')", table).Scan(&allowed); err != nil || allowed {
			t.Fatal("worker gained native discovery projection", err)
		}
	}
	var expression string
	if err := db.operator.QueryRow(t.Context(), `SELECT pg_get_expr(d.adbin,d.adrelid) FROM pg_attrdef d JOIN pg_attribute a ON a.attrelid=d.adrelid AND a.attnum=d.adnum WHERE d.adrelid='document_blobs'::regclass AND a.attname='agent_protocol_bindings'`).Scan(&expression); err != nil || !strings.Contains(expression, "catalog_agent_protocol_bindings_v1") {
		t.Fatal("projection is not database-maintained", err)
	}
}

// The provider is injected; real PostgreSQL owns matching, ranking and facets.
// One call per fresh query and captured metadata verify the disclosure boundary.
type agentProtocolProvider struct {
	fixedIndexProvider
	queries     []string
	unavailable bool
}

func (p *agentProtocolProvider) Query(_ context.Context, text string) ([]float32, error) {
	p.queries = append(p.queries, text)
	if p.unavailable {
		return nil, catalog.ErrEmbeddingUnavailable
	}
	return []float32{1, 0, 0}, nil
}

func TestPostgresAgentProtocolHybridAndFreshFallback(t *testing.T) {
	for _, native := range []bool{false, true} {
		t.Run(fmt.Sprintf("native=%t", native), func(t *testing.T) { testPostgresAgentProtocolHybridAndFreshFallback(t, native) })
	}
}

func testPostgresAgentProtocolHybridAndFreshFallback(t *testing.T, native bool) {
	t.Helper()
	db, repo, _, seed := newSearchCostFixture(t)
	raw, err := os.ReadFile("../../../samples/cards/dice-rest.json")
	if err != nil {
		t.Fatal(err)
	}
	for i, binding := range []string{"HTTP+JSON", "JSONRPC", "HTTP+JSON"} {
		var card map[string]any
		if err := json.Unmarshal(raw, &card); err != nil {
			t.Fatal(err)
		}
		card["capabilities"] = map[string]any{"streaming": false}
		card["supportedInterfaces"] = []any{map[string]any{"url": "https://inert.example/agent", "protocolBinding": binding, "protocolVersion": "1.0"}}
		bytes, err := json.Marshal(card)
		if err != nil {
			t.Fatal(err)
		}
		seed(i*2, bytes)
	}
	store := provisionSemanticFixture(t, db)
	g := catalog.SemanticGeneration{ID: "00000000-0000-7000-8000-000000000951", TenantID: housekeepingTenantA, Profile: "sha256:" + strings.Repeat("a", 64), Scope: "control", Dimensions: 3}
	if err := db.operator.QueryRow(t.Context(), "SELECT generation::text FROM catalog_search_generations WHERE tenant_id=$1", g.TenantID).Scan(&g.SourceGeneration); err != nil {
		t.Fatal(err)
	}
	job := housekeepingJob(g.ID, "", time.Now().UTC().Add(-time.Second))
	job.Kind, job.Priority, job.PayloadVersion = "semantic.reindex", 100, 1
	job.Payload, err = json.Marshal(catalog.SemanticReindexInput{Profile: g.Profile, Scope: g.Scope, SourceGeneration: g.SourceGeneration})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := db.apiJobs.Enqueue(t.Context(), job); err != nil {
		t.Fatal(err)
	}
	workerJobs, err := NewPostgresJobStore(store.repository, g.Profile)
	if err != nil {
		t.Fatal(err)
	}
	if claimed, err := workerJobs.Claim(t.Context(), "index", time.Now().UTC(), time.Minute); err != nil || claimed.ID != g.ID {
		t.Fatal("index claim", err)
	}
	provider := &agentProtocolProvider{}
	indexer, err := catalog.NewSemanticIndexer(store, provider, 16, 8192)
	if err != nil {
		t.Fatal(err)
	}
	if count, err := indexer.Run(t.Context(), g, "index"); err != nil || count != 3 {
		t.Fatal("indexing", count, err)
	}
	for _, text := range provider.texts {
		if strings.Contains(text, "HTTP+JSON") || strings.Contains(text, "JSONRPC") || strings.Contains(text, "inert.example") {
			t.Fatal("native declaration sent to provider")
		}
	}
	apiStore, err := NewPostgresSemanticStore(t.Context(), repo)
	if err != nil {
		t.Fatal(err)
	}
	semantic, err := catalog.NewSemanticSearch(catalog.SemanticSearchOptions{Store: apiStore, Provider: provider, Profile: g.Profile, RankingProfile: "sha256:" + strings.Repeat("b", 64), Dimensions: 3, Cutoff: 0.5, Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	key := sha256.Sum256([]byte(t.Name()))
	service, err := catalog.NewService(catalog.Options{Repository: repo, Clock: clock.Real{}, IDs: id.Generator{}, Validator: adapter.NewRegistry(), CursorKey: key[:], Policy: "protocol-test", SemanticSearch: semantic})
	if err != nil {
		t.Fatal(err)
	}
	author := semanticDisclosureActor(t, db, g.TenantID)
	q := catalog.SearchQuery{Text: "synthetic", AgentProtocol: "HTTP+JSON", Origins: []catalog.SearchResultType{catalog.SearchResultLocal}, Limit: 1}
	if native {
		streaming := false
		q.NativeCapabilities = &catalog.NativeCapabilitiesFilter{AgentStreaming: &streaming}
	}
	first, err := service.Search(author, q)
	if err != nil || first.Tier != "hybrid" || first.Total != 2 || len(first.Items) != 1 || first.NextCursor == "" {
		t.Fatal("hybrid preselection", first, err)
	}
	if len(provider.queries) != 1 || provider.queries[0] != q.Text {
		t.Fatal("filter changed query disclosure", provider.queries)
	}
	q.Cursor = first.NextCursor
	next, err := service.Search(author, q)
	if err != nil || next.Total != 2 || len(next.Items) != 1 || next.NextCursor != "" || next.Items[0].Release.ID == first.Items[0].Release.ID {
		t.Fatal("hybrid continuation", next, err)
	}
	q.Cursor = ""
	facetQuery := q
	facetQuery.Limit = 0
	facets, err := service.Explore(author, catalog.ExploreRequest{Query: facetQuery, Facets: []catalog.FacetRequest{{Field: "kind", Limit: 10}}})
	if err != nil || facets.Tier != "hybrid" || facets.Matched != 2 || facets.Facets["kind"].Buckets[0].Count != 2 {
		t.Fatal("hybrid facet preselection", facets, err)
	}
	provider.unavailable = true
	fallback, err := service.Search(author, q)
	if err != nil || fallback.Tier == "hybrid" || fallback.FallbackReason != "provider_unavailable" || fallback.Total != 2 {
		t.Fatal("fresh fallback broadened", fallback, err)
	}
}

func TestPostgresAgentProtocolProjectionUpgradePreservesBytes(t *testing.T) {
	db := newHousekeepingDatabase(t)
	tx, err := db.operator.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer rollbackForCleanup(t.Context(), tx)()
	schema := fmt.Sprintf("catalog_protocol_upgrade_%d", time.Now().UnixNano())
	var crypto string
	if err := tx.QueryRow(t.Context(), `SELECT n.nspname FROM pg_extension e JOIN pg_namespace n ON n.oid=e.extnamespace WHERE e.extname='pgcrypto'`).Scan(&crypto); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(t.Context(), `CREATE SCHEMA `+pgx.Identifier{schema}.Sanitize()+`; SET LOCAL search_path TO `+pgx.Identifier{schema}.Sanitize()+`,`+pgx.Identifier{crypto}.Sanitize()+`,public`); err != nil {
		t.Fatal(err)
	}
	entries, err := fs.ReadDir(migrations.Files, ".")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".sql") || entry.Name() >= "000048" {
			continue
		}
		raw, err := fs.ReadFile(migrations.Files, entry.Name())
		if err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(t.Context(), string(raw)); err != nil {
			t.Fatal(entry.Name(), err)
		}
	}
	if _, err := tx.Exec(t.Context(), `INSERT INTO tenants(id,display_name,created_at) VALUES ($1,'Protocol upgrade',now())`, housekeepingTenantA); err != nil {
		t.Fatal(err)
	}
	raw := []byte(`{"unrelated":"\u0000","supportedInterfaces":[{"protocolBinding":"HTTP+JSON"}]}`)
	sum := sha256.Sum256(raw)
	blobID := hex.EncodeToString(sum[:])
	if _, err := tx.Exec(t.Context(), `INSERT INTO document_blobs(tenant_id,id,media_type,content_digest,size_bytes,content,created_at) VALUES ($1,$2,$3,$4,$5,$6,now())`, housekeepingTenantA, blobID, adapter.MediaA2AAgentCard, "sha256:"+blobID, len(raw), raw); err != nil {
		t.Fatal(err)
	}
	migration, err := fs.ReadFile(migrations.Files, "000048_agent_protocol_projection.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(t.Context(), string(migration)); err != nil {
		t.Fatal(err)
	}
	var content []byte
	var digest string
	var bindings []string
	if err := tx.QueryRow(t.Context(), `SELECT content,content_digest,agent_protocol_bindings FROM document_blobs WHERE tenant_id=$1 AND id=$2`, housekeepingTenantA, blobID).Scan(&content, &digest, &bindings); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(content, raw) || digest != "sha256:"+blobID || !reflect.DeepEqual(bindings, []string{"HTTP+JSON"}) {
		t.Fatal("migration changed evidence or lost derived declaration", digest, bindings)
	}
}
