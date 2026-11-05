//go:build integration

package database

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/adapter"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/federation"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/clock"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/fetcher"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/id"
	"github.com/open-agentic-catalog/open-agentic-catalog/migrations"
)

type lexicalCorpus struct {
	Documents []struct {
		Identifier            string   `json:"identifier"`
		DisplayName           string   `json:"displayName"`
		Description           string   `json:"description"`
		Tags                  []string `json:"tags"`
		RepresentativeQueries []string `json:"representativeQueries"`
	} `json:"documents"`
	Cases []struct {
		Name     string `json:"name"`
		Document int    `json:"document"`
		Query    string `json:"query"`
		Score    int    `json:"score"`
	} `json:"cases"`
	Ordering []struct {
		Name     string `json:"name"`
		SortName string `json:"sortName"`
	} `json:"ordering"`
}

func loadLexicalCorpus(t *testing.T) lexicalCorpus {
	t.Helper()
	data, err := os.ReadFile("../../catalog/testdata/core-lexical-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	var corpus lexicalCorpus
	if err := json.Unmarshal(data, &corpus); err != nil {
		t.Fatal(err)
	}
	return corpus
}

func (c lexicalCorpus) documents() []catalog.SearchDocument {
	documents := make([]catalog.SearchDocument, 0, len(c.Documents))
	for _, fixture := range c.Documents {
		documents = append(documents, catalog.SearchDocument{Identifier: fixture.Identifier, DisplayName: fixture.DisplayName, Description: fixture.Description, Tags: fixture.Tags, RepresentativeQueries: fixture.RepresentativeQueries})
	}
	return documents
}

func TestPostgresSharedLexicalCorpus(t *testing.T) {
	corpus := loadLexicalCorpus(t)
	for index := range corpus.Documents {
		corpus.Documents[index].Tags = append(corpus.Documents[index].Tags, fmt.Sprintf("fixture%03d", index))
	}
	db, repository, _, seed := newSearchCostFixture(t, corpus.documents()...)
	for index := range corpus.Documents {
		seed(index)
	}
	// A single normalization pass must equal the former positionless field
	// union, including the shared Unicode, punctuation and long-token corpus.
	for index, document := range corpus.Documents {
		var equal bool
		err := db.api.QueryRow(t.Context(), `SELECT
		catalog_core_lexical_vector_v1(concat_ws(' ',$1::text,$2::text,$3::text,$4::text,$5::text)) =
		(catalog_core_lexical_vector_v1($1) || catalog_core_lexical_vector_v1($2) ||
		 catalog_core_lexical_vector_v1($3) || catalog_core_lexical_vector_v1($4) || catalog_core_lexical_vector_v1($5))`,
			document.Identifier, document.DisplayName, document.Description, strings.Join(document.Tags, " "), strings.Join(document.RepresentativeQueries, " ")).Scan(&equal)
		if err != nil || !equal {
			t.Fatalf("document %d positionless union equality=%v error=%v", index, equal, err)
		}
	}
	for _, test := range corpus.Cases {
		t.Run(test.Name, func(t *testing.T) {
			query := catalog.SearchQuery{Text: strings.Join(strings.Fields(test.Query), " "), Tags: []string{fmt.Sprintf("fixture%03d", test.Document)}}
			page, err := repository.SearchCatalog(t.Context(), housekeepingTenantA, catalog.CatalogSearchRepositoryRequest{Query: query, Limit: 10})
			want := 0
			if query.Text == "" || test.Score > 0 {
				want = 1
			}
			if err != nil || page.Total != want || len(page.Items) != want {
				t.Fatalf("matches=%d items=%d want=%d error=%v", page.Total, len(page.Items), want, err)
			}
			if want > 0 && page.Items[0].RelevanceScore != test.Score {
				t.Fatalf("score=%d want=%d", page.Items[0].RelevanceScore, test.Score)
			}
		})
	}
	for _, test := range corpus.Ordering {
		var got string
		if err := db.api.QueryRow(t.Context(), `SELECT catalog_core_sort_name_v1($1)`, test.Name).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != test.SortName {
			t.Errorf("SQL sort(%q)=%q want=%q", test.Name, got, test.SortName)
		}
	}
	// The pinned Go and PostgreSQL releases ship different Unicode editions.
	// Check every Go lowercase mapping, not just familiar Latin examples.
	var uppercase strings.Builder
	for character := rune(0); character <= unicode.MaxRune; character++ {
		if unicode.ToLower(character) != character {
			uppercase.WriteRune(character)
		}
	}
	var lowered string
	if err := db.api.QueryRow(t.Context(), `SELECT catalog_core_sort_name_v1($1 COLLATE "C")`, uppercase.String()).Scan(&lowered); err != nil {
		t.Fatal(err)
	}
	if lowered != strings.ToLower(uppercase.String()) {
		t.Fatalf("SQL case mapping differs from Go Unicode %s", unicode.Version)
	}
	// Go Fields uses Unicode White_Space, including NEL and non-breaking spaces.
	for character := rune(0); character <= unicode.MaxRune; character++ {
		if !unicode.IsSpace(character) {
			continue
		}
		var got string
		if err := db.api.QueryRow(t.Context(), `SELECT catalog_core_sort_name_v1($1 COLLATE "C")`, "A"+string(character)+"B").Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != "a b" {
			t.Errorf("U+%04X sort=%q", character, got)
		}
	}
	for _, input := range []string{"", "--- | ! :* '", "\u00b2", "CAF\u00c9", strings.Repeat("x", 2048) + " visible"} {
		var terms []string
		if err := db.api.QueryRow(t.Context(), `SELECT catalog_core_lexical_terms_v1($1 COLLATE "C")`, input).Scan(&terms); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(input, "visible") && !slices.Equal(terms, []string{"visible"}) {
			t.Fatalf("long document token handling=%q", terms)
		}
	}
}

func TestPostgresLocalVendorIdentifierExactPrecedence(t *testing.T) {
	const identifier = "vendor.example/skills/invoice-resolution"
	_, repository, _, seed := newSearchCostFixture(t, catalog.SearchDocument{Identifier: identifier, DisplayName: "Unrelated name", Description: "A local vendor identifier fixture"})
	seed(0)
	page, err := repository.SearchCatalog(t.Context(), housekeepingTenantA, catalog.CatalogSearchRepositoryRequest{Query: catalog.SearchQuery{Text: identifier}, Limit: 1})
	if err != nil || page.Total != 1 || len(page.Items) != 1 || page.Items[0].RelevanceScore != 100 {
		t.Fatalf("vendor identifier exact precedence=%+v error=%v", page, err)
	}
}

func TestPostgresSearchNormalizationUpgradeAndBoundedRepair(t *testing.T) {
	corpus := loadLexicalCorpus(t)
	db := newHousekeepingDatabase(t)
	repository, err := NewPostgresRepository(db.api)
	if err != nil {
		t.Fatal(err)
	}
	seedLexicalSource(t, db, repository, corpus.documents())
	var before string
	if err := db.operator.QueryRow(t.Context(), `SELECT string_agg(encode(normalized_entry_bytes,'hex'),'|' ORDER BY id) FROM observations WHERE tenant_id=$1`, housekeepingTenantA).Scan(&before); err != nil {
		t.Fatal(err)
	}
	// Reconstruct only the pre-27 derived schema in this owned fixture. The
	// original immutable migration bytes supply the former functions/index.
	old, err := migrations.Files.ReadFile("000011_bounded_search.sql")
	if err != nil {
		t.Fatal(err)
	}
	text := string(old)
	start := strings.Index(text, "CREATE FUNCTION catalog_core_lexical_vector_v1")
	end := strings.Index(text, "CREATE FUNCTION advance_catalog_search_generation")
	if _, err := db.operator.Exec(t.Context(), strings.ReplaceAll(text[start:end], "CREATE FUNCTION", "CREATE OR REPLACE FUNCTION")); err != nil {
		t.Fatal(err)
	}
	start = strings.Index(text, "CREATE FUNCTION project_observation_catalog_search")
	end = strings.Index(text, "CREATE TRIGGER observations_catalog_search_projection")
	if _, err := db.operator.Exec(t.Context(), strings.ReplaceAll(text[start:end], "CREATE FUNCTION", "CREATE OR REPLACE FUNCTION")); err != nil {
		t.Fatal(err)
	}
	if _, err := db.operator.Exec(t.Context(), `ALTER TABLE resources DROP COLUMN search_identifier_vector;
        ALTER TABLE listing_revisions DROP COLUMN search_display_vector, DROP COLUMN search_description_vector,
          DROP COLUMN search_tags_vector, DROP COLUMN search_queries_vector, DROP COLUMN search_sort_name;
        ALTER TABLE observations DROP COLUMN search_identifier_vector, DROP COLUMN search_display_vector,
          DROP COLUMN search_description_vector, DROP COLUMN search_tags_vector, DROP COLUMN search_queries_vector;
        DELETE FROM catalog_schema_migrations WHERE version=30;
        DROP FUNCTION catalog_core_lexical_array_vector_v1(text[]);
        DELETE FROM catalog_schema_migrations WHERE version=29;
        ALTER TABLE observations DROP COLUMN search_vector_version;
        DROP FUNCTION catalog_core_lexical_terms_v1(text);
        DROP FUNCTION catalog_core_sort_name_v1(text);
        DELETE FROM catalog_schema_migrations WHERE version=27;
        DROP TRIGGER observations_control_filter_projection ON observations;
        DROP FUNCTION project_observation_control_filters();
        DROP FUNCTION catalog_observation_license_v1(jsonb);
        DROP FUNCTION catalog_observation_categories_v1(jsonb);
        DROP FUNCTION catalog_control_term_v1(text);
        ALTER TABLE observations DROP COLUMN search_license, DROP COLUMN search_categories, DROP COLUMN search_filter_version;
        DELETE FROM catalog_schema_migrations WHERE version=39;`); err != nil {
		t.Fatal(err)
	}
	oldIndex, err := migrations.Files.ReadFile("000017_bounded_search_repair.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.operator.Exec(t.Context(), string(oldIndex)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.operator.Exec(t.Context(), `UPDATE observations SET normalized_entry_bytes=normalized_entry_bytes WHERE tenant_id=$1`, housekeepingTenantA); err != nil {
		t.Fatal(err)
	}
	plan, err := Plan(t.Context(), db.operator)
	if err != nil {
		t.Fatal(err)
	}
	pending := 0
	for _, entry := range plan {
		if !entry.Applied {
			pending++
			if entry.Version != 27 && entry.Version != 29 && entry.Version != 30 && entry.Version != 39 {
				t.Fatalf("unexpected pending migration %d", entry.Version)
			}
		}
	}
	if pending != 4 {
		t.Fatalf("pending=%d", pending)
	}
	applied, reports, err := ApplyAndReconcileRuntimePrivileges(t.Context(), db.operator, db.api.Config().ConnConfig.User, db.worker.Config().ConnConfig.User)
	if err != nil || len(applied) != 4 || len(reports) != 2 {
		t.Fatalf("upgrade applied=%d reports=%d err=%v", len(applied), len(reports), err)
	}
	request := catalog.CatalogSearchRepositoryRequest{Limit: 10}
	store, err := NewPostgresFederationStore(repository)
	if err != nil {
		t.Fatal(err)
	}
	searches := map[string]func(string) error{
		"local": func(tenant string) error {
			_, err := repository.SearchCatalog(t.Context(), tenant, request)
			return err
		},
		"indexed": func(tenant string) error {
			_, err := store.SearchIndexedCatalog(t.Context(), tenant, catalog.FederatedCatalogSearchRequest{Limit: 10})
			return err
		},
		"source observations": func(tenant string) error {
			_, err := store.SearchObservationPage(t.Context(), tenant, federation.ObservationSearchRequest{Limit: 10})
			return err
		},
	}
	assertSearches := func(needsRepair bool) {
		t.Helper()
		for name, search := range searches {
			err := search(housekeepingTenantA)
			if needsRepair && catalog.ErrorCodeOf(err) != catalog.CodeDependencyUnavailable || !needsRepair && err != nil {
				t.Fatalf("%s: needs repair=%t error=%v", name, needsRepair, err)
			}
			if err := search(housekeepingTenantB); err != nil {
				t.Fatalf("%s: other tenant blocked by repair: %v", name, err)
			}
		}
	}
	assertSearches(true)
	emptyTenant, err := RepairCatalogSearchProjections(t.Context(), db.operator, housekeepingTenantB, 1, "check empty tenant", "test")
	if err != nil || !emptyTenant.Complete || emptyTenant.Processed != 0 || emptyTenant.BeforeGeneration != 0 || emptyTenant.AfterGeneration != 0 || emptyTenant.AuditEventID != "" {
		t.Fatalf("empty tenant repair=%+v error=%v", emptyTenant, err)
	}
	for _, pool := range []*pgxpool.Pool{db.api, db.worker} {
		if _, err := RepairCatalogSearchProjections(t.Context(), pool, housekeepingTenantA, 1, "rebuild search normalization", "test"); err == nil {
			t.Fatal("runtime role repaired projections")
		}
		if _, err := pool.Exec(t.Context(), `UPDATE observations SET search_vector_version=2 WHERE tenant_id=$1`, housekeepingTenantA); err == nil {
			t.Fatal("runtime role assigned projection version")
		}
	}
	func() {
		ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
		defer cancel()
		readState := func() string {
			t.Helper()
			var state string
			if err := db.operator.QueryRow(ctx, `
SELECT jsonb_build_object(
  'observations', (SELECT jsonb_agg(to_jsonb(observation) ORDER BY id) FROM observations observation WHERE tenant_id=$1),
  'generation', (SELECT to_jsonb(generation) FROM catalog_search_generations generation WHERE tenant_id=$1),
  'audits', (SELECT count(*) FROM audit_events WHERE tenant_id=$1),
  'outbox', (SELECT count(*) FROM outbox_events WHERE tenant_id=$1)
)::text`, housekeepingTenantA).Scan(&state); err != nil {
				t.Fatal(err)
			}
			return state
		}
		before := readState()
		missing, err := RepairCatalogSearchProjections(ctx, db.operator, "00000000-0000-7000-8000-000000000809", 1, "reject absent tenant", "test")
		if err == nil || err.Error() != "search projection repair tenant was not found" || missing != (CatalogSearchProjectionRepairResult{}) {
			t.Fatalf("missing tenant repair=%+v err=%v", missing, err)
		}
		if readState() != before {
			t.Fatal("missing tenant repair changed retained state or evidence")
		}
		if _, err := db.operator.Exec(ctx, `
CREATE FUNCTION reject_search_repair_audit() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN RAISE EXCEPTION 'injected search repair audit failure'; END $$;
CREATE TRIGGER reject_search_repair_audit BEFORE INSERT ON audit_events FOR EACH ROW
WHEN (NEW.action='repair.search_projection.rebuilt' AND NEW.tenant_id='00000000-0000-7000-8000-000000000801')
EXECUTE FUNCTION reject_search_repair_audit()`); err != nil {
			t.Fatal(err)
		}
		defer func() {
			cleanupCtx, cleanupCancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
			defer cleanupCancel()
			if _, err := db.operator.Exec(cleanupCtx, `DROP TRIGGER reject_search_repair_audit ON audit_events; DROP FUNCTION reject_search_repair_audit()`); err != nil {
				t.Errorf("remove search repair audit failure: %v", err)
			}
		}()
		failed, err := RepairCatalogSearchProjections(ctx, db.operator, housekeepingTenantA, 1, "rebuild search normalization", "test")
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != "P0001" || pgErr.Message != "injected search repair audit failure" || failed != (CatalogSearchProjectionRepairResult{}) {
			t.Fatalf("failed repair=%+v err=%v", failed, err)
		}
		if readState() != before {
			t.Fatal("failed repair changed projections, source bytes, generation or evidence")
		}
		assertSearches(true)
	}()
	for step := range len(corpus.Documents) {
		result, err := RepairCatalogSearchProjections(t.Context(), db.operator, housekeepingTenantA, 1, "rebuild search normalization", "test")
		if err != nil {
			t.Fatal(err)
		}
		if result.Processed != 1 || result.Complete != (step == len(corpus.Documents)-1) || result.AfterGeneration <= result.BeforeGeneration {
			t.Fatalf("repair=%+v", result)
		}
		assertSearches(!result.Complete)
	}
	result, err := RepairCatalogSearchProjections(t.Context(), db.operator, housekeepingTenantA, 1, "confirm search normalization", "test")
	if err != nil || result.Processed != 0 || !result.Complete {
		t.Fatalf("repeat repair=%+v err=%v", result, err)
	}
	var after string
	if err := db.operator.QueryRow(t.Context(), `SELECT string_agg(encode(normalized_entry_bytes,'hex'),'|' ORDER BY id) FROM observations WHERE tenant_id=$1`, housekeepingTenantA).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if before != after {
		t.Fatal("repair rewrote immutable source evidence")
	}
	page, err := store.SearchIndexedCatalog(t.Context(), housekeepingTenantA, catalog.FederatedCatalogSearchRequest{Query: catalog.SearchQuery{Text: "\u00b2"}, Limit: 10})
	if err != nil || len(page.Indexed) != 1 || page.Indexed[0].Score != 50 {
		t.Fatalf("repaired numeric search=%+v error=%v", page, err)
	}
	if _, err := db.worker.Exec(t.Context(), `SELECT catalog_core_sort_name_v1('forbidden')`); err == nil {
		t.Fatal("worker received API sort authority")
	}
}

// Ingestion uses the real manager and worker role, not hand-built observations.
func seedLexicalSource(tb testing.TB, db *housekeepingDatabase, repository *PostgresRepository, documents []catalog.SearchDocument) *federation.Manager {
	tb.Helper()
	return seedLexicalSourceProfile(tb, db, repository, documents, false)
}

func seedLexicalSourceProfile(tb testing.TB, db *housekeepingDatabase, repository *PostgresRepository, documents []catalog.SearchDocument, aiCatalog bool) *federation.Manager {
	tb.Helper()
	endpoint := "https://catalog.remote.example/.well-known/ard.json"
	if aiCatalog {
		endpoint = "https://catalog.remote.example/.well-known/ai-catalog.json"
	}
	return seedLexicalSourceEndpoint(tb, db, repository, documents, aiCatalog, endpoint)
}

func seedLexicalSourceEndpoint(tb testing.TB, db *housekeepingDatabase, repository *PostgresRepository, documents []catalog.SearchDocument, aiCatalog bool, endpoint string, versions ...string) *federation.Manager {
	tb.Helper()
	entries := make([]map[string]any, 0, len(documents))
	for _, document := range documents {
		entries = append(entries, map[string]any{"identifier": document.Identifier, "displayName": document.DisplayName, "description": document.Description, "tags": document.Tags, "representativeQueries": document.RepresentativeQueries, "type": adapter.MediaA2AAgentCard, "data": map[string]any{"name": "Synthetic"}})
		if len(versions) == 1 {
			entries[len(entries)-1]["version"] = versions[0]
		}
		if aiCatalog && document.DisplayName == "" {
			delete(entries[len(entries)-1], "displayName")
		}
	}
	envelope := map[string]any{"entries": entries}
	mediaType := "application/ard+json"
	if aiCatalog {
		envelope["specVersion"] = "1.0"
		mediaType = "application/ai-catalog+json"
	}
	body, err := json.Marshal(envelope)
	if err != nil {
		tb.Fatal(err)
	}
	return seedLexicalSourceBody(tb, db, repository, body, mediaType, endpoint, len(documents))
}

func seedLexicalSourceBody(tb testing.TB, db *housekeepingDatabase, repository *PostgresRepository, body []byte, mediaType, endpoint string, count int) *federation.Manager {
	tb.Helper()
	principal, err := ProvisionIdentity(tb.Context(), db.operator, IdentityProvisioningInput{TenantID: housekeepingTenantA, TenantDisplayName: "Housekeeping tenant A", Issuer: "https://identity.performance.example", TenancyMode: "dedicated", Subject: "lexical-source", ActorType: identity.ActorHuman, Roles: []identity.Role{identity.RoleSourceAdmin}, Operator: "lexical-test", Reason: "synthetic lexical source", AcknowledgeOutbox: true})
	if err != nil {
		tb.Fatal(err)
	}
	actor := postgresFixtureContext(tb, db, tb.Context(), identity.Actor{TenantID: housekeepingTenantA, Subject: "lexical-source", Roles: []identity.Role{identity.RoleSourceAdmin}, MembershipID: principal.MembershipID, RealmID: principal.RealmID, ActorType: principal.ActorType})
	outbound := &postgresFederationFetcher{policyDigest: "sha256:" + strings.Repeat("a", 64), result: fetcher.Result{Status: 200, MediaType: mediaType, FetchedAt: time.Now(), Body: body}}
	outbound.beforeReturn = func() error { outbound.result.FetchedAt = time.Now(); return nil }
	store, err := NewPostgresFederationStore(repository)
	if err != nil {
		tb.Fatal(err)
	}
	manager, err := federation.NewManager(store, outbound, clock.Real{}, id.Generator{})
	if err != nil {
		tb.Fatal(err)
	}
	inspection, err := manager.Inspect(actor, endpoint)
	if err != nil {
		tb.Fatal(err)
	}
	source, err := manager.Connect(actor, federation.ConnectInput{InspectionID: inspection.ID, DisplayName: "Lexical fixture", Scope: federation.Scope{Kinds: []catalog.Kind{catalog.KindAgent}}, RetrievalMode: federation.RetrievalIndexed, Reason: "synthetic corpus"})
	if err != nil {
		tb.Fatal(err)
	}
	worker, err := NewPostgresRepository(db.worker)
	if err != nil {
		tb.Fatal(err)
	}
	workerStore, err := NewPostgresFederationStore(worker)
	if err != nil {
		tb.Fatal(err)
	}
	workerManager, err := federation.NewManager(workerStore, outbound, clock.Real{}, id.Generator{})
	if err != nil {
		tb.Fatal(err)
	}
	if snapshot, err := syncClaimedFixture(tb, db, actor, workerManager, source.ID); err != nil || snapshot.Added != count {
		tb.Fatalf("sync added=%d want=%d error=%v", snapshot.Added, count, err)
	}
	return manager
}

// Identical entries from separate sources remain one result with complete
// provenance. Scope changes apply immediately without rewriting observations.
func TestPostgresIndexedDuplicateSourcesAndCurrentScopes(t *testing.T) {
	db, repository, _, _ := newSearchCostFixture(t)
	documents := []catalog.SearchDocument{
		{Identifier: "urn:air:remote.example:finance:one", DisplayName: "Shared One", Tags: []string{"first"}, RepresentativeQueries: []string{}},
		{Identifier: "urn:air:remote.example:finance:two", DisplayName: "Shared Two", Tags: []string{"second"}, RepresentativeQueries: []string{}},
	}
	seedLexicalSourceEndpoint(t, db, repository, documents, false, "https://first.example/.well-known/ard.json")
	manager := seedLexicalSourceEndpoint(t, db, repository, documents, false, "https://second.example/.well-known/ard.json")
	principal, err := ProvisionIdentity(t.Context(), db.operator, IdentityProvisioningInput{TenantID: housekeepingTenantA, TenantDisplayName: "Housekeeping tenant A", Issuer: "https://identity.performance.example", TenancyMode: "dedicated", Subject: "lexical-source", ActorType: identity.ActorHuman, Roles: []identity.Role{identity.RoleSourceAdmin}, Operator: "lexical-test", Reason: "synthetic lexical source", AcknowledgeOutbox: true})
	if err != nil {
		t.Fatal(err)
	}
	admin := postgresFixtureContext(t, db, t.Context(), identity.Actor{TenantID: housekeepingTenantA, Subject: "lexical-source", Roles: []identity.Role{identity.RoleSourceAdmin}, MembershipID: principal.MembershipID, RealmID: principal.RealmID, ActorType: principal.ActorType})
	sources, err := manager.ListSourcePage(admin, federation.SourceListRequest{Limit: 10})
	if err != nil || len(sources) != 2 {
		t.Fatalf("sources=%d error=%v", len(sources), err)
	}
	source := sources[0]
	store, err := NewPostgresFederationStore(repository)
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"force_custom_plan", "force_generic_plan"} {
		// The repository fixture has its own single-connection pool. Set the
		// mode there, not on the separate database helper's API pool.
		if _, err := repository.pool.Exec(t.Context(), "SET plan_cache_mode="+mode); err != nil {
			t.Fatal(err)
		}
		for _, item := range []struct {
			name   string
			scope  federation.Scope
			counts [2]int
		}{
			{"both", federation.Scope{Kinds: []catalog.Kind{catalog.KindAgent}}, [2]int{2, 2}},
			{"identifier", federation.Scope{Identifiers: []string{documents[0].Identifier}}, [2]int{2, 1}},
			{"exclude first", federation.Scope{Kinds: []catalog.Kind{catalog.KindAgent}, ExcludedIdentifiers: []string{documents[0].Identifier}}, [2]int{1, 2}},
			{"exclude both", federation.Scope{Kinds: []catalog.Kind{catalog.KindAgent}, ExcludedIdentifiers: []string{documents[0].Identifier, documents[1].Identifier}}, [2]int{1, 1}},
			{"tag", federation.Scope{Tags: []string{"second"}}, [2]int{1, 2}},
			{"kind", federation.Scope{Kinds: []catalog.Kind{catalog.KindSkill}}, [2]int{1, 1}},
			{"version", federation.Scope{Versions: []string{"missing"}}, [2]int{1, 1}},
			{"publisher", federation.Scope{Publishers: []string{"absent.example"}}, [2]int{1, 1}},
		} {
			t.Run(mode+"/"+item.name, func(t *testing.T) {
				if mode != "force_custom_plan" || item.name != "both" {
					patch := federation.PatchInput{Scope: &item.scope, Intent: federation.ChangeUpdate, Reason: "check current scope eligibility"}
					preview, err := manager.PreviewChange(admin, source.ID, patch)
					if err != nil {
						t.Fatal(err)
					}
					patch.ChangePreviewDigest = preview.Digest
					source, err = manager.UpdateSource(admin, source.ID, patch, source.Revision)
					if err != nil {
						t.Fatal(err)
					}
				}
				legacy, err := store.SearchObservationPage(admin, housekeepingTenantA, federation.ObservationSearchRequest{Limit: 10})
				summary, summaryErr := manager.IndexedOverview(admin)
				if summaryErr != nil || summary.Limited || summary.Count == nil || *summary.Count != 2 {
					t.Fatal("overview changed distinct count under overlapping scopes", summary, summaryErr)
				}
				if err != nil || len(legacy) != item.counts[0]+item.counts[1] {
					t.Fatalf("legacy eligibility: items=%d error=%v", len(legacy), err)
				}
				request := catalog.FederatedCatalogSearchRequest{Query: catalog.SearchQuery{Text: "Shared"}, Limit: 1}
				for index := 0; index < 2; index++ {
					page, err := store.SearchIndexedCatalog(admin, housekeepingTenantA, request)
					if err != nil || page.IndexedTotal != 2 || len(page.Indexed) != 1 || page.HasMore != (index == 0) {
						t.Fatalf("page %d: %+v error=%v", index, page, err)
					}
					result := page.Indexed[0]
					if result.Identifier != documents[index].Identifier || result.Indexed == nil || result.Indexed.SourceCount != item.counts[index] || len(result.Indexed.SourceIDs) != item.counts[index] || result.Indexed.Conflict {
						t.Fatalf("identity/provenance changed: %+v", result)
					}
					position := catalog.CatalogSearchResultPosition(result)
					request.After = &position
					request.ExpectedGeneration = page.Generation
				}
			})
		}
	}
	page, err := store.SearchIndexedCatalog(t.Context(), housekeepingTenantB, catalog.FederatedCatalogSearchRequest{Limit: 10})
	if err != nil || page.IndexedTotal != 0 || len(page.Indexed) != 0 {
		t.Fatalf("cross-tenant results: %+v %v", page, err)
	}
}

func TestPostgresIndexedSharedLexicalCorpus(t *testing.T) {
	corpus := loadLexicalCorpus(t)
	for index := range corpus.Documents {
		corpus.Documents[index].Tags = append(corpus.Documents[index].Tags, fmt.Sprintf("fixture%03d", index))
	}
	db := newHousekeepingDatabase(t)
	repository, err := NewPostgresRepository(db.api)
	if err != nil {
		t.Fatal(err)
	}
	manager := seedLexicalSource(t, db, repository, corpus.documents())
	reader := postgresFixtureContext(t, db, t.Context(), identity.Actor{TenantID: housekeepingTenantA, Subject: "reader", Roles: []identity.Role{identity.RoleReader}})
	for _, test := range corpus.Cases {
		t.Run(test.Name, func(t *testing.T) {
			query := catalog.SearchQuery{Text: strings.Join(strings.Fields(test.Query), " "), Tags: []string{fmt.Sprintf("fixture%03d", test.Document)}}
			page, err := manager.SearchFederatedCatalog(reader, catalog.FederatedCatalogSearchRequest{Query: query, Limit: 10})
			want := 0
			if query.Text == "" || test.Score > 0 {
				want = 1
			}
			if err != nil || page.IndexedTotal != want || len(page.Indexed) != want {
				t.Fatalf("matches=%d items=%d want=%d error=%v", page.IndexedTotal, len(page.Indexed), want, err)
			}
			if want > 0 && page.Indexed[0].Score != test.Score {
				t.Fatalf("score=%d want=%d", page.Indexed[0].Score, test.Score)
			}
		})
	}
}

func TestPostgresIndexedDisplayFallbackDoesNotIncreaseRelevance(t *testing.T) {
	db, repository, _, _ := newSearchCostFixture(t)
	identifier := "urn:air:catalog.remote.example:finance:needle"
	// The supported AI Catalog profile permits an omitted display name; ARD
	// requires one. Exercise the actual ingestion profile, not forged evidence.
	seedLexicalSourceProfile(t, db, repository, []catalog.SearchDocument{{Identifier: identifier, Description: "Metadata only", Tags: []string{}, RepresentativeQueries: []string{}}}, true)
	store, err := NewPostgresFederationStore(repository)
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"force_custom_plan", "force_generic_plan"} {
		// The fixture repository has one isolated connection; this setting never
		// changes another test, runtime or database-wide planner policy.
		if _, err := repository.pool.Exec(t.Context(), "SET plan_cache_mode="+mode); err != nil {
			t.Fatal(err)
		}
		_, err := withTenantTx(t.Context(), repository.pool, housekeepingTenantA, pgx.TxOptions{AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) (int, error) {
			name := "indexed_count_" + mode
			if _, err := tx.Conn().Prepare(t.Context(), name, indexedCatalogSearchRecordsCTE+"SELECT count(*) FROM selected_identities"); err != nil {
				return 0, err
			}
			var plan string
			if err := tx.QueryRow(t.Context(), "EXPLAIN (VERBOSE, FORMAT JSON) EXECUTE "+name+"('"+housekeepingTenantA+"','needle','',NULL,NULL,'','',NULL)").Scan(&plan); err != nil {
				return 0, err
			}
			// Assert unused computation is absent, not a particular join/index
			// strategy or elapsed-time threshold. Page ranking remains tested below.
			for _, expression := range []string{"catalog_core_lexical_vector_v1", "catalog_core_sort_name_v1", "catalog-search-result-v1"} {
				if strings.Contains(plan, expression) {
					t.Fatalf("indexed count evaluates unused ranking under %s: %s", mode, expression)
				}
			}
			// Even a small fixture must not materialize the shared wide set.
			// At 100k that copied unused vectors and forced repeated temp scans.
			for _, name := range []string{"current_eligible", "eligible"} {
				if strings.Contains(plan, `"CTE Name": "`+name+`"`) {
					t.Fatalf("count materializes unused shared projection %s under %s", name, mode)
				}
			}
			name = "indexed_page_" + mode
			if _, err := tx.Conn().Prepare(t.Context(), name, indexedCatalogSearchRecordsCTE+indexedCatalogSearchPageSQL); err != nil {
				return 0, err
			}
			if err := tx.QueryRow(t.Context(), "EXPLAIN (VERBOSE, FORMAT JSON) EXECUTE "+name+"('"+housekeepingTenantA+"','needle','',NULL,NULL,'','',NULL,false,0,'','',2)").Scan(&plan); err != nil {
				return 0, err
			}
			for _, name := range []string{"current_eligible", "eligible"} {
				if strings.Contains(plan, `"CTE Name": "`+name+`"`) {
					t.Fatalf("page rescans materialized shared projection %s under %s", name, mode)
				}
			}
			// Negative control: removing only the inlining directives reproduces
			// the old wide count materialization on this same owned fixture.
			name = "indexed_materialized_count_" + mode
			baseline := strings.ReplaceAll(indexedCatalogSearchRecordsCTE, "AS NOT MATERIALIZED (", "AS (")
			if _, err := tx.Conn().Prepare(t.Context(), name, baseline+"SELECT count(*) FROM selected_identities"); err != nil {
				return 0, err
			}
			if err := tx.QueryRow(t.Context(), "EXPLAIN (VERBOSE, FORMAT JSON) EXECUTE "+name+"('"+housekeepingTenantA+"','needle','',NULL,NULL,'','',NULL)").Scan(&plan); err != nil {
				return 0, err
			}
			if !strings.Contains(plan, `"CTE Name": "eligible"`) {
				t.Fatalf("negative control did not reproduce materialized count under %s", mode)
			}
			return 0, nil
		})
		if err != nil {
			t.Fatal(err)
		}
		for _, query := range []struct {
			text  string
			score int
		}{{"", 0}, {"needle", 45}, {identifier, 100}} {
			page, err := store.SearchIndexedCatalog(t.Context(), housekeepingTenantA, catalog.FederatedCatalogSearchRequest{Query: catalog.SearchQuery{Text: query.text}, Limit: 1})
			if err != nil || page.IndexedTotal != 1 || len(page.Indexed) != 1 {
				t.Fatalf("fallback search: %+v %v", page, err)
			}
			item := page.Indexed[0]
			if item.DisplayName != identifier || item.Score != query.score || item.Indexed == nil || item.Indexed.Conflict || item.Indexed.SameSourceConflict || item.Indexed.SourceCount != 1 {
				t.Fatalf("fallback changed relevance/provenance under %s: %+v", mode, item)
			}
		}
	}
}

func TestPostgresSearchUnicodeCursorTraversal(t *testing.T) {
	corpus := loadLexicalCorpus(t)
	documents := make([]catalog.SearchDocument, 0, len(corpus.Ordering))
	for i, item := range corpus.Ordering {
		documents = append(documents, catalog.SearchDocument{Identifier: fmt.Sprintf("urn:air:catalog.example.com:finance:ordered-%d", i), DisplayName: item.Name, Description: "Search ordering fixture"})
	}
	db, repository, _, seed := newSearchCostFixture(t, documents...)
	for i := range documents {
		seed(i)
	}
	key := sha256.Sum256([]byte(t.Name()))
	service, err := catalog.NewService(catalog.Options{Repository: repository, Clock: clock.Real{}, IDs: id.Generator{}, Validator: adapter.NewRegistry(), CursorKey: key[:], Policy: "search-cost-test"})
	if err != nil {
		t.Fatal(err)
	}
	reader := postgresFixtureContext(t, db, t.Context(), identity.Actor{TenantID: housekeepingTenantA, Subject: "reader", Roles: []identity.Role{identity.RoleReader}})
	// Exercise Unicode cursor keys on both SQL branches. Distinct remote
	// identifiers keep local-precedence deduplication outside this fixture.
	indexed := make([]catalog.SearchDocument, 0, len(corpus.Ordering)+1)
	indexed = append(indexed, catalog.SearchDocument{Identifier: "urn:air:remote.example:finance:ordered", DisplayName: "C", Description: "Search ordering fixture", Tags: []string{"remote"}})
	for i, item := range corpus.Ordering {
		indexed = append(indexed, catalog.SearchDocument{Identifier: fmt.Sprintf("urn:air:remote.example:finance:ordered-%d", i), DisplayName: item.Name, Description: "Search ordering fixture", Tags: []string{"remote"}})
	}
	manager := seedLexicalSource(t, db, repository, indexed)
	composed, err := catalog.NewSearchService(service, manager)
	if err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{"", "ordering"} {
		for _, size := range []int{1, 2} {
			t.Run(fmt.Sprintf("%s/size%d", text, size), func(t *testing.T) {
				full, err := composed.Search(reader, catalog.SearchQuery{Text: text, Limit: 20})
				if err != nil {
					t.Fatal(err)
				}
				if len(full.Items) != len(documents)+len(indexed) {
					t.Fatalf("full result length=%d", len(full.Items))
				}
				wantNames := []string{"a y", "a y", "a y", "a y", "a z", "a z", "b", "b", "c", "i", "i"}
				var names, expected, actual []string
				for _, item := range full.Items {
					names = append(names, catalog.CatalogSearchSortName(item.DisplayName))
					expected = append(expected, item.ID)
				}
				if !slices.Equal(names, wantNames) {
					t.Fatalf("order=%q want=%q", names, wantNames)
				}
				for i := 1; i < len(full.Items); i++ {
					if names[i-1] == names[i] && full.Items[i-1].ID >= full.Items[i].ID {
						t.Fatal("equal names must use result ID order")
					}
				}
				cursor := ""
				seen := map[string]bool{}
				for range len(full.Items) + 1 {
					page, err := composed.Search(reader, catalog.SearchQuery{Text: text, Limit: size, Cursor: cursor})
					if err != nil {
						t.Fatal(err)
					}
					if page.DurableTotal != full.DurableTotal {
						t.Fatal("total changed without writes")
					}
					for _, item := range page.Items {
						if seen[item.ID] {
							t.Fatalf("repeated result %q", item.DisplayName)
						}
						seen[item.ID] = true
						actual = append(actual, item.ID)
					}
					cursor = page.NextCursor
					if cursor == "" {
						break
					}
				}
				if cursor != "" || !reflect.DeepEqual(actual, expected) {
					t.Fatalf("incomplete traversal: got %v want %v", actual, expected)
				}
			})
		}
	}
}
