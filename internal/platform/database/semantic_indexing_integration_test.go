//go:build integration

// Indexing integration tests exercise real eligibility and immutable vector
// reuse through restricted runtime roles, not privileged fake repositories.
package database

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/clock"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/id"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
)

// provisionSemanticFixture installs optional state with the fixture owner only.
func provisionSemanticFixture(t *testing.T, db *housekeepingDatabase) *PostgresSemanticStore {
	t.Helper()
	var schema, apiRole, workerRole string
	if err := db.operator.QueryRow(t.Context(), "SELECT current_schema()").Scan(&schema); err != nil {
		t.Fatal(err)
	}
	if err := db.api.QueryRow(t.Context(), "SELECT current_user").Scan(&apiRole); err != nil {
		t.Fatal(err)
	}
	if err := db.worker.QueryRow(t.Context(), "SELECT current_user").Scan(&workerRole); err != nil {
		t.Fatal(err)
	}
	if _, err := db.operator.Exec(t.Context(), "CREATE EXTENSION vector WITH SCHEMA "+pgx.Identifier{schema}.Sanitize()); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ApplyAndReconcileRuntimePrivileges(t.Context(), db.operator, apiRole, workerRole, true); err != nil {
		t.Fatal(err)
	}
	repository, err := NewPostgresRepository(db.worker)
	if err != nil {
		t.Fatal(err)
	}
	store, err := NewPostgresSemanticStore(t.Context(), repository)
	if err != nil {
		t.Fatal(err)
	}
	return store
}

type fixedIndexProvider struct {
	calls, inputs  int
	texts          []string
	documentVector []float32
}

func (p *fixedIndexProvider) Query(context.Context, string) ([]float32, error) {
	return []float32{1, 0, 0}, nil
}
func (p *fixedIndexProvider) Documents(_ context.Context, texts []string) ([][]float32, error) {
	p.calls++
	p.inputs += len(texts)
	p.texts = append(p.texts, texts...)
	result := make([][]float32, len(texts))
	for i := range result {
		result[i] = []float32{1, 0, 0}
		if p.documentVector != nil {
			result[i] = append([]float32(nil), p.documentVector...)
		}
	}
	return result, nil
}

// TestSemanticProfileReplacementDoesNotReuseIncompatibleVectors completes two
// equal-dimension profiles over unchanged metadata through the real role ports.
func TestSemanticProfileReplacementDoesNotReuseIncompatibleVectors(t *testing.T) {
	db, repository, _, seed := newSearchCostFixture(t, catalog.SearchDocument{
		Identifier: "urn:air:catalog.example.com:finance:profile", DisplayName: "Profile fixture", Description: "Shared metadata", Tags: []string{"profiles"},
	})
	seed(0)
	store := provisionSemanticFixture(t, db)
	reader, err := NewPostgresSemanticStore(t.Context(), repository)
	if err != nil {
		t.Fatal(err)
	}
	g := catalog.SemanticGeneration{TenantID: housekeepingTenantA, Scope: "control", Dimensions: 3}
	if err := db.operator.QueryRow(t.Context(), "SELECT generation::text FROM catalog_search_generations WHERE tenant_id=$1", g.TenantID).Scan(&g.SourceGeneration); err != nil {
		t.Fatal(err)
	}
	text := "Name: Profile fixture\nDescription: Shared metadata\nTags: profiles\nRepresentative queries: "
	digest := fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(text)))
	provider := &fixedIndexProvider{}
	var original catalog.ListingRevision
	for i, profile := range []struct {
		marker string
		vector []float32
		stored string
	}{
		{"a", []float32{1, 0, 0}, "[1,0,0]"},
		{"b", []float32{0, 1, 0}, "[0,1,0]"},
	} {
		g.ID, g.Profile = fmt.Sprintf("00000000-0000-7000-8000-%012d", 970+i), "sha256:"+strings.Repeat(profile.marker, 64)
		provider.documentVector = profile.vector
		if admitted, err := store.Admit(t.Context(), g, nil, 5); err != nil || !admitted {
			t.Fatalf("profile %d admission=%v error=%v", i, admitted, err)
		}
		worker, err := NewPostgresJobStore(store.repository, g.Profile)
		if err != nil {
			t.Fatal(err)
		}
		if job, err := worker.Claim(t.Context(), "replacement", time.Now().UTC(), time.Minute); err != nil || job.ID != g.ID {
			t.Fatalf("profile %d claim=%s error=%v", i, job.ID, err)
		}
		if err := store.Begin(t.Context(), g, "replacement"); err != nil {
			t.Fatal(err)
		}
		if active, ready, err := reader.Active(t.Context(), g.TenantID, g.Profile, g.Scope, 3); err != nil || ready || active.ID != "" {
			t.Fatalf("incomplete profile %d selected: %+v ready=%v error=%v", i, active, ready, err)
		}
		if known, err := store.KnownInputs(t.Context(), g, []string{digest}); err != nil || len(known) != 0 {
			t.Fatalf("profile %d reused incompatible input: %v %v", i, known, err)
		}
		inputs, total, err := store.Inputs(t.Context(), g, "", 1)
		if err != nil || len(inputs) != 1 || total != 1 {
			t.Fatalf("profile input: %v total=%d error=%v", inputs, total, err)
		}
		release, err := repository.GetRelease(t.Context(), g.TenantID, strings.TrimPrefix(inputs[0].Key, "local:"))
		if err != nil {
			t.Fatal(err)
		}
		listing, err := repository.GetListingRevision(t.Context(), g.TenantID, release.ListingRevisionID)
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			original = listing
		} else if !reflect.DeepEqual(listing, original) {
			t.Fatal("profile replacement changed authoritative listing")
		}
		indexer, err := catalog.NewSemanticIndexer(store, provider, 1, 8192)
		if err != nil {
			t.Fatal(err)
		}
		if count, err := indexer.Run(t.Context(), g, "replacement"); err != nil || count != 1 || provider.calls != i+1 || provider.inputs != i+1 || provider.texts[i] != text {
			t.Fatalf("profile %d indexing count=%d calls=%d inputs=%d error=%v", i, count, provider.calls, provider.inputs, err)
		}
		if active, ready, err := reader.Active(t.Context(), g.TenantID, g.Profile, g.Scope, 3); err != nil || !ready || active.ID != g.ID || active.Profile != g.Profile {
			t.Fatalf("completed profile %d selection=%+v ready=%v error=%v", i, active, ready, err)
		}
		var key, savedDigest, vector, failure string
		if err := db.worker.QueryRow(t.Context(), `SELECT m.item_key,m.input_digest,v.embedding::text,m.failure
 FROM semantic_members m JOIN semantic_vectors v USING(tenant_id,profile,dimensions,disclosure,input_digest)
 WHERE m.tenant_id=$1 AND m.generation_id=$2`, g.TenantID, g.ID).Scan(&key, &savedDigest, &vector, &failure); err != nil || key != inputs[0].Key || savedDigest != digest || vector != profile.stored || failure != "" {
			t.Fatalf("profile %d membership=%s/%s/%s/%s error=%v", i, key, savedDigest, vector, failure, err)
		}
		if _, err := worker.Complete(t.Context(), g.TenantID, g.ID, "replacement", time.Now().UTC(), json.RawMessage(`{}`)); err != nil {
			t.Fatal(err)
		}
	}
	after, err := repository.GetListingRevision(t.Context(), g.TenantID, original.ID)
	if err != nil || !reflect.DeepEqual(after, original) {
		t.Fatalf("indexing changed authoritative metadata: %v", err)
	}
}

// TestSemanticIndexingEnumeratesCurrentInputsAndReusesVectors includes local
// versioned local records and unversioned indexed records. An absent remote
// version is a distinct identity, but identical metadata still reuses a vector.
func TestSemanticIndexingEnumeratesCurrentInputsAndReusesVectors(t *testing.T) {
	documents := []catalog.SearchDocument{
		{Identifier: "urn:air:catalog.example.com:finance:one", DisplayName: "Shared title", Description: "Shared description", Tags: []string{"catalog"}},
		{Identifier: "urn:air:catalog.example.com:finance:two", DisplayName: "Shared title", Description: "Shared description", Tags: []string{"catalog"}},
	}
	db, repository, _, seed := newSearchCostFixtureWithListings(t, []catalog.CreateListingRevisionInput{{License: "MIT", Categories: []string{"finance", "tools"}}, {License: "Apache-2.0", Categories: []string{"tools"}}}, documents...)
	seed(0)
	seed(1)
	seedLexicalSource(t, db, repository, []catalog.SearchDocument{documents[0], {Identifier: "urn:air:remote.example:finance:remote", DisplayName: "Remote search", Description: "Distinct description", Tags: []string{"remote"}}})
	store := provisionSemanticFixture(t, db)
	g := catalog.SemanticGeneration{ID: "00000000-0000-7000-8000-000000000951", TenantID: housekeepingTenantA, Profile: "sha256:" + strings.Repeat("a", 64), Scope: "control", Dimensions: 3}
	if err := db.operator.QueryRow(t.Context(), "SELECT generation::text FROM catalog_search_generations WHERE tenant_id=$1", g.TenantID).Scan(&g.SourceGeneration); err != nil {
		t.Fatal(err)
	}
	workerJobs, err := NewPostgresJobStore(store.repository, g.Profile)
	if err != nil {
		t.Fatal(err)
	}
	start := func(g catalog.SemanticGeneration) {
		t.Helper()
		job := housekeepingJob(g.ID, "", time.Now().UTC().Add(-time.Second))
		job.Kind = "semantic.reindex"
		job.Priority = 100
		var payloadErr error
		job.PayloadVersion = 1
		job.Payload, payloadErr = json.Marshal(catalog.SemanticReindexInput{Profile: g.Profile, Scope: g.Scope, SourceGeneration: g.SourceGeneration})
		if payloadErr != nil {
			t.Fatal(payloadErr)
		}
		if _, _, err := db.apiJobs.Enqueue(t.Context(), job); err != nil {
			t.Fatal(err)
		}
		claimed, err := workerJobs.Claim(t.Context(), "index", time.Now().UTC(), time.Minute)
		if err != nil || claimed.ID != g.ID {
			t.Fatalf("claim = %s %v", claimed.ID, err)
		}
	}
	start(g)
	provider := &fixedIndexProvider{}
	indexer, err := catalog.NewSemanticIndexer(store, provider, 16, 8192)
	if err != nil {
		t.Fatal(err)
	}
	count, err := indexer.Run(t.Context(), g, "index")
	if err != nil || count != 4 || provider.inputs != 2 {
		t.Fatalf("index count=%d provider inputs=%d err=%v", count, provider.inputs, err)
	}
	page, total, err := store.Inputs(t.Context(), g, "", 1)
	if err != nil || len(page) != 0 || total != 4 {
		t.Fatalf("resume page=%d total=%d err=%v", len(page), total, err)
	}
	if _, err := workerJobs.Complete(t.Context(), g.TenantID, g.ID, "index", time.Now().UTC(), json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
	g.ID = "00000000-0000-7000-8000-000000000952"
	start(g)
	count, err = indexer.Run(t.Context(), g, "index")
	if err != nil || count != 4 || provider.inputs != 2 {
		t.Fatalf("rebuild repeated model calls: count=%d inputs=%d err=%v", count, provider.inputs, err)
	}
	active, ready, err := store.Active(t.Context(), g.TenantID, g.Profile, g.Scope, g.Dimensions)
	if err != nil || !ready || active.ID != g.ID {
		t.Fatalf("active generation=%+v ready=%v err=%v", active, ready, err)
	}
	apiSemantic, err := NewPostgresSemanticStore(t.Context(), repository)
	if err != nil {
		t.Fatal(err)
	}
	request := catalog.SemanticSearchRequest{Generation: active, Query: catalog.SearchQuery{Text: "no lexical match", Limit: 2}, Vector: []float32{1, 0, 0}, Cutoff: 0.5}
	first, err := apiSemantic.Search(t.Context(), request)
	if err != nil || first.Total != 4 || len(first.Items) != 2 || !first.HasMore {
		t.Fatalf("semantic-only first page=%+v err=%v", first, err)
	}
	// The shared API pool returns its idle connection for this sequential
	// probe. Hybrid matching must not enter the named statement cache, where
	// auto planning can replace selective plans with a costly generic plan.
	var cached int
	if err := db.api.QueryRow(t.Context(), "SELECT count(*) FROM pg_prepared_statements WHERE statement LIKE $1", "%compared AS NOT MATERIALIZED%").Scan(&cached); err != nil || cached != 0 {
		t.Fatalf("hybrid ranking entered named plan cache: %d %v", cached, err)
	}
	request.After = &first.Items[1].Position
	second, err := apiSemantic.Search(t.Context(), request)
	if err != nil || second.Total != 4 || len(second.Items) != 2 || second.HasMore || second.Items[0].Result.ID == first.Items[0].Result.ID {
		t.Fatalf("semantic-only second page=%+v err=%v", second, err)
	}
	// Whole-match identity probes include semantic-only winners outside the
	// current page and preserve missing versus present versions. They return
	// no documents and ignore the requested page position.
	probe := request
	selected := second.Items[0].Result
	key := catalog.IndexedIdentity{Identifier: selected.Identifier, Version: selected.Version, VersionPresent: selected.VersionPresent}
	probe.IdentityProbes = []catalog.IndexedIdentity{key, {Identifier: key.Identifier, Version: key.Version, VersionPresent: !key.VersionPresent}, {Identifier: "urn:air:absent.example:agent:missing"}}
	matched, err := apiSemantic.Search(t.Context(), probe)
	if err != nil || len(matched.Items) != 0 || len(matched.Matches) != 1 || matched.Matches[0] != key {
		t.Fatalf("semantic-only probes: %+v %v", matched, err)
	}
	probe.Query.Tags = []string{"absent-tag"}
	if matched, err := apiSemantic.Search(t.Context(), probe); err != nil || len(matched.Matches) != 0 {
		t.Fatalf("filtered probes: %+v %v", matched, err)
	}
	probe.Query.Tags = nil
	probe.IdentityProbes = make([]catalog.IndexedIdentity, 51)
	if _, err := apiSemantic.Search(t.Context(), probe); catalog.ErrorCodeOf(err) != catalog.CodeInvalidArgument {
		t.Fatalf("unbounded hybrid probes: %v", err)
	}
	request.After = &second.Items[1].Position
	last, err := apiSemantic.Search(t.Context(), request)
	if err != nil || last.Total != 4 || len(last.Items) != 0 || last.HasMore {
		t.Fatalf("semantic-only terminal page=%+v err=%v", last, err)
	}
	request.After = nil
	request.Query.Tags = []string{"remote"}
	filtered, err := apiSemantic.Search(t.Context(), request)
	if err != nil || filtered.Total != 1 || len(filtered.Items) != 1 || filtered.Items[0].Result.Indexed == nil || filtered.Items[0].Result.Indexed.SourceCount != 1 {
		t.Fatalf("filtered semantic-only result=%+v err=%v", filtered, err)
	}
	request.LocalOnly = true
	if page, err := apiSemantic.Search(t.Context(), request); err != nil || page.Total != 0 || len(page.Items) != 0 {
		t.Fatalf("local-only search disclosed indexed result=%+v err=%v", page, err)
	}
	request.LocalOnly = false
	request.Query.Tags = nil
	request.Vector = []float32{0, 1, 0}
	if page, err := apiSemantic.Search(t.Context(), request); err != nil || page.Total != 0 {
		t.Fatalf("zero-match hybrid search=%+v err=%v", page, err)
	}
	request.Vector = []float32{1, 0, 0}
	facets, err := apiSemantic.Explore(t.Context(), request, []catalog.FacetRequest{{Field: "tag", Limit: 10}})
	if err != nil || facets.Matched != 2 || len(facets.Facets["tag"].Buckets) != 1 || facets.Facets["tag"].Buckets[0].Count != 2 {
		t.Fatalf("hybrid local facets=%+v err=%v", facets, err)
	}
	// Listing filters remove indexed candidates and nonmatching local records
	// before vector comparison, global fusion, complete totals and facets.
	for _, tc := range []struct {
		license    string
		categories []string
		total      int
	}{
		{"MIT", []string{"tools"}, 1}, {"mit", nil, 0}, {"", []string{"tools"}, 2},
		{"Apache-2.0", []string{"finance"}, 0}, {"", []string{"absent"}, 0},
	} {
		request.Query.License, request.Query.Categories = tc.license, tc.categories
		page, err := apiSemantic.Search(t.Context(), request)
		if err != nil || page.Total != tc.total || len(page.Items) != tc.total {
			t.Fatalf("filtered hybrid page=%+v err=%v", page, err)
		}
		for _, item := range page.Items {
			if item.Result.Type != catalog.SearchResultLocal {
				t.Fatal("Listing filter admitted an indexed result")
			}
		}
		facets, err := apiSemantic.Explore(t.Context(), request, []catalog.FacetRequest{{Field: "category", Limit: 1}})
		if err != nil || facets.Matched != tc.total {
			t.Fatalf("filtered hybrid facets=%+v err=%v", facets, err)
		}
		if tc.total == 1 && (len(facets.Facets["category"].Buckets) != 1 || facets.Facets["category"].Buckets[0] != (catalog.FacetBucket{Value: "finance", Count: 1}) || facets.Facets["category"].OtherCount != 1) {
			t.Fatalf("hybrid omitted buckets=%+v", facets)
		}
	}
	request.Query.License, request.Query.Categories = "", nil
	// Origin/namespace predicates apply to the complete semantic population,
	// before fusion, totals or hydration; live is not a durable candidate.
	for mask := 1; mask < 8; mask++ {
		request.Query.Origins = nil
		for i, origin := range []catalog.SearchResultType{catalog.SearchResultLocal, catalog.SearchResultIndexed, catalog.SearchResultLive} {
			if mask&(1<<i) != 0 {
				request.Query.Origins = append(request.Query.Origins, origin)
			}
		}
		selected, err := apiSemantic.Search(t.Context(), request)
		want := 0
		if mask&1 != 0 {
			want += 2
		}
		if mask&2 != 0 {
			want += 2
		}
		if err != nil || selected.Total != want {
			t.Fatalf("hybrid origins %d: %+v %v", mask, selected, err)
		}
		for _, item := range selected.Items {
			if !request.Query.IncludesOrigin(item.Result.Type) {
				t.Fatal("excluded semantic origin returned")
			}
		}
	}
	request.Query.Origins = nil
	var namespaceID string
	if err := db.operator.QueryRow(t.Context(), "SELECT namespace_id::text FROM catalog_listings WHERE tenant_id=$1 LIMIT 1", g.TenantID).Scan(&namespaceID); err != nil {
		t.Fatal(err)
	}

	// Current namespace authority filters the complete vector population before
	// comparison/fusion/counts; indexing itself remains worker-owned and complete.
	author := semanticDisclosureActor(t, db, g.TenantID)
	identityStore, err := NewPostgresIdentityStore(repository)
	if err != nil {
		t.Fatal(err)
	}
	authority, err := identity.NewService(identity.ServiceOptions{Store: identityStore, Clock: clock.Real{}, IDs: id.Generator{}, CursorKey: []byte("01234567890123456789012345678901")})
	if err != nil {
		t.Fatal(err)
	}
	grant, err := authority.GrantRole(author, identity.GrantRoleInput{Subject: "semantic-scoped", ActorType: identity.ActorHuman, Role: identity.RoleReader, NamespaceID: namespaceID, Reason: "Qualify scoped vector selection"}, "scoped")
	if err != nil {
		t.Fatal(err)
	}
	actor, err := identityStore.ResolvePrincipal(t.Context(), identity.Principal{TenantID: g.TenantID, Issuer: "https://identity.performance.example", Subject: "semantic-scoped"})
	if err != nil {
		t.Fatal(err)
	}
	scoped, _, err := identity.WithCollectionContext(identity.WithActor(t.Context(), actor))
	if err != nil {
		t.Fatal(err)
	}
	scopedRequest := request
	scopedRequest.Query.Origins = nil
	scopedRequest.Query.NamespaceID = ""
	scopedRequest.Query.Limit = 1
	scopedPage, err := apiSemantic.Search(scoped, scopedRequest)
	if err != nil || scopedPage.Total != 2 || len(scopedPage.Items) != 1 || !scopedPage.HasMore || scopedPage.Items[0].Local == nil {
		t.Fatal("scoped hybrid population", scopedPage, err)
	}
	scopedRequest.After = &scopedPage.Items[0].Position
	scopedLast, err := apiSemantic.Search(scoped, scopedRequest)
	if err != nil || scopedLast.Total != 2 || len(scopedLast.Items) != 1 || scopedLast.HasMore || scopedLast.Items[0].Local == nil || scopedLast.Items[0].Result.ID == scopedPage.Items[0].Result.ID {
		t.Fatal("scoped hybrid continuation", scopedLast, err)
	}
	scopedRequest.After = nil
	scopedFacets, err := apiSemantic.Explore(scoped, scopedRequest, []catalog.FacetRequest{{Field: "tag", Limit: 10}})
	if err != nil || scopedFacets.Matched != 2 || len(scopedFacets.Facets["tag"].Buckets) != 1 || scopedFacets.Facets["tag"].Buckets[0].Count != 2 {
		t.Fatal("scoped hybrid facets", scopedFacets, err)
	}
	if _, err := authority.RevokeRole(author, grant.ID, "Withdraw hybrid scope", "revoke-scoped", grant.Revision); err != nil {
		t.Fatal(err)
	}
	if _, err := apiSemantic.Search(scoped, scopedRequest); catalog.ErrorCodeOf(err) != catalog.CodeInvalidArgument {
		t.Fatal("hybrid accepted changed policy", err)
	}
	freshScoped, _, err := identity.WithCollectionContext(identity.WithActor(t.Context(), actor))
	if err != nil {
		t.Fatal(err)
	}
	if page, err := apiSemantic.Search(freshScoped, scopedRequest); err != nil || page.Total != 0 || len(page.Items) != 0 {
		t.Fatal("hybrid retained revoked authority", page, err)
	}
	for _, namespace := range []string{namespaceID, "00000000-0000-7000-8000-000000000099"} {
		request.Query.NamespaceID = namespace
		selected, err := apiSemantic.Search(t.Context(), request)
		want := 0
		if namespace == namespaceID {
			want = 2
		}
		if err != nil || selected.Total != want {
			t.Fatalf("hybrid namespace totals=%d %v", selected.Total, err)
		}
		for _, item := range selected.Items {
			if item.Local == nil || item.Local.Listing.NamespaceID != namespace {
				t.Fatal("foreign namespace in hybrid page")
			}
		}
		facets, err := apiSemantic.Explore(t.Context(), request, []catalog.FacetRequest{{Field: "namespace", Limit: 10}})
		if err != nil || facets.Matched != want {
			t.Fatalf("hybrid namespace facets=%+v %v", facets, err)
		}
	}
	request.Query.NamespaceID = ""
	if _, err := workerJobs.Complete(t.Context(), g.TenantID, g.ID, "index", time.Now().UTC(), json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
	// Drain the superseded generation in small chunks; current membership stays.
	func() {
		checkCtx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
		defer cancel()
		holder, err := db.operator.BeginTx(checkCtx, pgx.TxOptions{})
		if err != nil {
			t.Fatal(err)
		}
		defer rollbackForCleanup(checkCtx, holder)()
		if _, err := holder.Exec(checkCtx, `SELECT pg_advisory_xact_lock($1)`, semanticAdmissionLock+1); err != nil {
			t.Fatal(err)
		}
		readState := func() string {
			t.Helper()
			var state string
			if err := db.operator.QueryRow(checkCtx, `
SELECT jsonb_build_object(
  'generations', (SELECT jsonb_agg(to_jsonb(generation) ORDER BY id) FROM semantic_generations generation WHERE tenant_id=$1),
  'members', (SELECT jsonb_agg(to_jsonb(member) ORDER BY generation_id,item_key) FROM semantic_members member WHERE tenant_id=$1),
  'vectors', (SELECT jsonb_agg(to_jsonb(vector) ORDER BY profile,disclosure,input_digest) FROM semantic_vectors vector WHERE tenant_id=$1)
)::text`, g.TenantID).Scan(&state); err != nil {
				t.Fatal(err)
			}
			return state
		}
		before := readState()
		if removed, err := store.Cleanup(checkCtx, g.Profile, nil, 1); err != nil || removed != 0 || checkCtx.Err() != nil {
			t.Fatalf("contended cleanup removed=%d err=%v (context: %v)", removed, err, checkCtx.Err())
		}
		if acquired := db.worker.Stat().AcquiredConns(); acquired != 0 {
			t.Fatalf("contended cleanup retained a transaction connection: %d", acquired)
		}
		if holder.Conn().PgConn().TxStatus() != 'T' || readState() != before {
			t.Fatal("contended cleanup lost its blocker or changed generations, members or vectors")
		}
		if err := holder.Rollback(checkCtx); err != nil {
			t.Fatal(err)
		}
	}()
	for range 4 {
		if removed, err := store.Cleanup(t.Context(), g.Profile, nil, 1); err != nil || removed > 2 {
			t.Fatalf("bounded cleanup removed=%d err=%v", removed, err)
		}
	}
	var generations, members int
	if err := db.operator.QueryRow(t.Context(), `
SELECT (SELECT count(*) FROM semantic_generations WHERE tenant_id=$1),
       (SELECT count(*) FROM semantic_members WHERE tenant_id=$1 AND generation_id=$2)`, g.TenantID, g.ID).
		Scan(&generations, &members); err != nil || generations != 1 || members != 4 {
		t.Fatalf("cleanup generation/member counts: generations=%d current members=%d err=%v", generations, members, err)
	}
	if active, ready, err := store.Active(t.Context(), g.TenantID, g.Profile, g.Scope, g.Dimensions); err != nil || !ready || active.ID != g.ID {
		t.Fatalf("cleanup removed current generation: id=%s ready=%v err=%v", active.ID, ready, err)
	}
	_, digest, _ := catalog.SemanticDocument{DisplayName: documents[0].DisplayName, Description: documents[0].Description, Tags: documents[0].Tags}.Projection()
	known, err := store.KnownInputs(t.Context(), g, []string{digest})
	if err != nil || !known[digest] {
		t.Fatalf("reuse: %v %v", known, err)
	}
	other := g
	other.TenantID = housekeepingTenantB
	known, err = store.KnownInputs(t.Context(), other, []string{digest})
	if err != nil || len(known) != 0 {
		t.Fatal("cross-tenant reuse")
	}
	other = g
	other.Scope = "public:" + g.ID
	known, err = store.KnownInputs(t.Context(), other, []string{digest})
	if err != nil || len(known) != 0 {
		t.Fatal("private vector exposed to public input")
	}
	other = g
	other.SourceGeneration = "stale"
	if _, _, err := store.Inputs(t.Context(), other, "", 1); catalog.ErrorCodeOf(err) != catalog.CodeConflict {
		t.Fatalf("stale enumeration accepted: %v", err)
	}
	// A public snapshot can disappear after candidate selection. Declining that
	// stale scope must leave later admissions available and create no Job.
	other = g
	other.ID = "00000000-0000-7000-8000-000000000957"
	other.Scope = "public:" + other.ID
	if admitted, err := store.Admit(t.Context(), other, nil, 5); err != nil || admitted {
		t.Fatalf("missing public snapshot admission=%v err=%v", admitted, err)
	}
	if _, err := db.apiJobs.Get(t.Context(), other.TenantID, other.ID); catalog.ErrorCodeOf(err) != catalog.CodeNotFound {
		t.Fatalf("missing public snapshot created a Job: %v", err)
	}
	// A different profile schedules exactly one Job across competing admissions.
	other = g
	other.ID = "00000000-0000-7000-8000-000000000953"
	other.Profile = "sha256:" + strings.Repeat("c", 64)
	due, err := store.Due(t.Context(), other.Profile, other.Dimensions, []string{housekeepingTenantB}, 1)
	if err != nil || len(due) != 1 || due[0].TenantID != g.TenantID {
		t.Fatalf("due candidates=%+v err=%v", due, err)
	}
	if admitted, err := store.Admit(t.Context(), other, nil, 7); err != nil || !admitted {
		t.Fatalf("admission=%v err=%v", admitted, err)
	}
	admittedJob, err := db.apiJobs.Get(t.Context(), other.TenantID, other.ID)
	if err != nil || admittedJob.AttemptLimit != 7 || !admittedJob.CreatedAt.Equal(admittedJob.UpdatedAt) || !admittedJob.CreatedAt.Equal(admittedJob.NotBefore) {
		t.Fatalf("admission timestamps did not use one statement instant: %+v %v", admittedJob, err)
	}
	other.ID = "00000000-0000-7000-8000-000000000954"
	if admitted, err := store.Admit(t.Context(), other, nil, 5); err != nil || admitted {
		t.Fatalf("duplicate admission=%v err=%v", admitted, err)
	}
	retainedJob, err := db.apiJobs.Get(t.Context(), admittedJob.TenantID, admittedJob.ID)
	if err != nil || retainedJob.AttemptLimit != 7 || !retainedJob.NotBefore.Equal(admittedJob.NotBefore) {
		t.Fatalf("duplicate semantic admission changed retained policy: %#v %v", retainedJob, err)
	}
	if due, err := store.Due(t.Context(), other.Profile, other.Dimensions, []string{housekeepingTenantA, housekeepingTenantB}, 32); err != nil || len(due) != 0 {
		t.Fatalf("excluded tenant scheduled: %v %v", due, err)
	}
	// Queued work for an old profile is not claimable by replacement workers.
	// It must not exhaust the new profile's bounded admission capacity.
	for i := range 64 {
		job := housekeepingJob(fmt.Sprintf("00000000-0000-7000-8000-%012d", 2000+i), "", time.Now().UTC())
		job.Kind, job.PayloadVersion = "semantic.reindex", 1
		job.Payload, err = json.Marshal(catalog.SemanticReindexInput{Profile: g.Profile, Scope: g.Scope, SourceGeneration: g.SourceGeneration})
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := db.apiJobs.Enqueue(t.Context(), job); err != nil {
			t.Fatal(err)
		}
	}
	other.ID = "00000000-0000-7000-8000-000000000955"
	other.Profile = "sha256:" + strings.Repeat("d", 64)
	if admitted, err := store.Admit(t.Context(), other, nil, 5); err != nil || !admitted {
		t.Fatalf("old-profile backlog blocked replacement: %v %v", admitted, err)
	}
	if state, err := apiSemantic.Status(t.Context(), g.TenantID, g.Profile, g.Dimensions); err != nil || state.State != "ready" || state.Indexed != 4 || state.Failed != 0 {
		t.Fatalf("administrator status disagrees with ready membership: %+v %v", state, err)
	}
	// Excluded tenants' retained, unclaimable Jobs must not block current
	// work for other tenants sharing the same configured model profile.
	other.ID = "00000000-0000-7000-8000-000000000956"
	other.Profile = "sha256:" + strings.Repeat("e", 64)
	for i := range 64 {
		job := housekeepingJob(fmt.Sprintf("00000000-0000-7000-8000-%012d", 4000+i), "", time.Now().UTC())
		job.TenantID, job.Kind, job.PayloadVersion = housekeepingTenantB, "semantic.reindex", 1
		job.Payload, err = json.Marshal(catalog.SemanticReindexInput{Profile: other.Profile, Scope: "control", SourceGeneration: "1"})
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := db.apiJobs.Enqueue(t.Context(), job); err != nil {
			t.Fatal(err)
		}
	}
	if admitted, err := store.Admit(t.Context(), other, []string{housekeepingTenantB}, 5); err != nil || !admitted {
		t.Fatalf("excluded backlog blocked eligible tenant: %v %v", admitted, err)
	}
	other.TenantID = housekeepingTenantB
	if admitted, err := store.Admit(t.Context(), other, []string{housekeepingTenantB}, 5); err != nil || admitted {
		t.Fatalf("excluded tenant admitted: %v %v", admitted, err)
	}
}

// A coherent restore may omit rebuildable generations. A successful old Job
// must not throttle reconstruction; unchanged vectors may still be reused.
func TestSemanticMissingGenerationRebuildsWithoutSuccessCooldown(t *testing.T) {
	db, _, _, seed := newSearchCostFixture(t, catalog.SearchDocument{Identifier: "urn:air:catalog.example.com:finance:restore", DisplayName: "Restore metadata", Description: "Rebuild derived state from retained metadata"})
	seed(0)
	store := provisionSemanticFixture(t, db)
	profile := "sha256:" + strings.Repeat("e", 64)
	workerJobs, err := NewPostgresJobStore(store.repository, profile)
	if err != nil {
		t.Fatal(err)
	}
	provider := &fixedIndexProvider{}
	indexer, err := catalog.NewSemanticIndexer(store, provider, 16, 8192)
	if err != nil {
		t.Fatal(err)
	}
	for iteration := range 2 {
		due, err := store.Due(t.Context(), profile, 3, nil, 1)
		if err != nil || len(due) != 1 {
			t.Fatalf("rebuild admission: %v %v", due, err)
		}
		g := due[0]
		g.ID = fmt.Sprintf("00000000-0000-7000-8000-%012d", 3000+iteration)
		if admitted, err := store.Admit(t.Context(), g, nil, 5); err != nil || !admitted {
			t.Fatalf("admit=%v err=%v", admitted, err)
		}
		job, err := workerJobs.Claim(t.Context(), "restore-index", time.Now().UTC(), time.Minute)
		if err != nil || job.ID != g.ID {
			t.Fatalf("claim=%s err=%v", job.ID, err)
		}
		if count, err := indexer.Run(t.Context(), g, "restore-index"); err != nil || count != 1 {
			t.Fatalf("index count=%d err=%v", count, err)
		}
		if _, err := workerJobs.Complete(t.Context(), g.TenantID, g.ID, "restore-index", time.Now().UTC(), json.RawMessage(`{}`)); err != nil {
			t.Fatal(err)
		}
		if provider.inputs != 1 {
			t.Fatal("restored metadata was unnecessarily re-embedded")
		}
		if iteration == 0 {
			// This explicit mutation is confined to the owned test schema. All
			// authoritative rows, Jobs and immutable vector bytes remain intact.
			if _, err := db.operator.Exec(t.Context(), "DELETE FROM semantic_generations WHERE tenant_id=$1 AND id=$2", g.TenantID, g.ID); err != nil {
				t.Fatal(err)
			}
			if _, ready, err := store.Active(t.Context(), g.TenantID, profile, "control", 3); err != nil || ready {
				t.Fatal("missing index reported ready")
			}
		}
	}
}
