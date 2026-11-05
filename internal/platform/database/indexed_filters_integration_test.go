//go:build integration

package database

import (
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode"

	"github.com/jackc/pgx/v5"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/federation"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/migrations"
)

func TestPostgresObservationControlFilterParity(t *testing.T) {
	db := newHousekeepingDatabase(t)
	raw, err := os.ReadFile("../../federation/testdata/control-filters.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		Name       string          `json:"name"`
		Entry      json.RawMessage `json:"entry"`
		License    *string         `json:"license"`
		Categories []string        `json:"categories"`
	}
	if err := json.Unmarshal(raw, &cases); err != nil {
		t.Fatal(err)
	}
	for _, c := range cases {
		t.Run(c.Name, func(t *testing.T) {
			var license *string
			var categories []string
			err := db.api.QueryRow(t.Context(), `SELECT catalog_observation_license_v1($1::jsonb#>'{metadata,license}'), catalog_observation_categories_v1($1::jsonb#>'{unknownProperties,http://purl.org/dc/terms/subject}')`, string(c.Entry)).Scan(&license, &categories)
			if err != nil || !reflect.DeepEqual(license, c.License) || !reflect.DeepEqual(categories, c.Categories) {
				t.Fatalf("SQL license=%v categories=%v err=%v; want %v %v", license, categories, err, c.License, c.Categories)
			}
			if goProjection := federation.ProjectObservationControlFilters(c.Entry); !reflect.DeepEqual(goProjection, federation.ObservationControlFilters{License: license, Categories: categories}) {
				t.Fatalf("Go/SQL mismatch: %+v", goProjection)
			}
		})
	}
	var upper strings.Builder
	for r := rune(0); r <= unicode.MaxRune; r++ {
		if unicode.ToLower(r) != r {
			upper.WriteRune(r)
		}
	}
	var lowered string
	if err := db.api.QueryRow(t.Context(), `SELECT catalog_control_term_v1($1 COLLATE "C")`, upper.String()).Scan(&lowered); err != nil || lowered != strings.ToLower(upper.String()) {
		t.Fatalf("simple lowercase differs from Go Unicode %s: %v", unicode.Version, err)
	}
	for r := rune(0); r <= unicode.MaxRune; r++ {
		if !unicode.IsSpace(r) {
			continue
		}
		var term string
		if err := db.api.QueryRow(t.Context(), `SELECT catalog_control_term_v1($1 COLLATE "C")`, string(r)+"A  B"+string(r)).Scan(&term); err != nil || term != "a  b" {
			t.Fatalf("U+%04X trim/internal-space parity=%q %v", r, term, err)
		}
	}
}

func TestPostgresIndexedControlFiltersAndRepair(t *testing.T) {
	db, repo, _, seed := newSearchCostFixture(t, catalog.SearchDocument{Identifier: "urn:air:filter.example:default:agent7", DisplayName: "Local authoritative", Description: "Local release without matching license"})
	entries := make([]map[string]any, 0, 8)
	for i, declaration := range []struct {
		license any
		topics  any
	}{
		{"MIT", []string{" TOOLS ", "CAFÉ"}},
		{"MIT", "TOOLS"},
		{"Apache-2.0", []string{"tools", "café"}},
		{nil, []string{"tools", "café"}},
		{"MIT", []string{}},
		{"MIT", nil},
		{true, []string{"tools"}},
		{"MIT", []string{"tools", "café"}},
	} {
		entry := map[string]any{
			"identifier": fmt.Sprintf("urn:air:filter.example:default:agent%d", i), "version": "1.0.0",
			"displayName": fmt.Sprintf("Agent %d", i), "description": "Invoice helper", "type": "application/a2a-agent-card+json",
			"data":     map[string]any{"name": "Synthetic", "license": "MIT"},
			"@context": map[string]any{"dc": "http://purl.org/dc/terms/"},
		}
		if declaration.license != nil {
			entry["metadata"] = map[string]any{"license": declaration.license}
		}
		if declaration.topics != nil {
			entry["dc:subject"] = declaration.topics
		}
		entries = append(entries, entry)
	}
	body, err := json.Marshal(map[string]any{"entries": entries})
	if err != nil {
		t.Fatal(err)
	}
	manager := seedLexicalSourceBody(t, db, repo, body, "application/ard+json", "https://filters.example/.well-known/ard.json", len(entries))
	// Identical bytes from a second source preserve source attribution.
	seedLexicalSourceBody(t, db, repo, body, "application/ard+json", "https://filters-two.example/.well-known/ard.json", len(entries))
	conflict := maps.Clone(entries[0])
	conflict["metadata"] = map[string]any{"license": "Apache-2.0"}
	conflictBody, err := json.Marshal(map[string]any{"entries": []map[string]any{conflict}})
	if err != nil {
		t.Fatal(err)
	}
	seedLexicalSourceBody(t, db, repo, conflictBody, "application/ard+json", "https://filters-conflict.example/.well-known/ard.json", 1)
	store, err := NewPostgresFederationStore(repo)
	if err != nil {
		t.Fatal(err)
	}
	query := catalog.SearchQuery{License: "MIT", Categories: []string{"café", "tools"}, Origins: []catalog.SearchResultType{catalog.SearchResultIndexed}}
	var after *catalog.CatalogSearchPosition
	generation := ""
	seen := make([]string, 0, 2)
	for range 2 {
		page, err := store.SearchIndexedCatalog(t.Context(), housekeepingTenantA, catalog.FederatedCatalogSearchRequest{Query: query, Limit: 1, After: after, ExpectedGeneration: generation})
		if err != nil || page.IndexedTotal != 2 || len(page.Indexed) != 1 {
			t.Fatalf("filtered page=%+v %v", page, err)
		}
		item := page.Indexed[0]
		if item.Indexed.SourceCount != 2 || item.Indexed.Conflict != strings.HasSuffix(item.Identifier, ":agent0") {
			t.Fatalf("attribution=%+v", item.Indexed)
		}
		seen = append(seen, item.Identifier)
		position := catalog.CatalogSearchResultPosition(item)
		after, generation = &position, page.Generation
		if page.HasMore != (len(seen) == 1) {
			t.Fatal("incorrect continuation")
		}
	}
	if !slices.Equal(seen, []string{"urn:air:filter.example:default:agent0", "urn:air:filter.example:default:agent7"}) {
		t.Fatalf("traversal=%v", seen)
	}
	for _, c := range []struct {
		query catalog.SearchQuery
		total int
	}{
		// Unavailable optional evidence must not remove valid unfiltered entries.
		{catalog.SearchQuery{}, 8},
		{catalog.SearchQuery{License: "MIT"}, 5},
		{catalog.SearchQuery{License: "mit"}, 0},
		{catalog.SearchQuery{Categories: []string{"tools"}}, 6},
		// Both agent2 and the conflicting Apache declaration for agent0 match.
		{catalog.SearchQuery{License: "Apache-2.0", Categories: []string{"café", "tools"}}, 2},
		{catalog.SearchQuery{License: "MIT", Categories: []string{"absent"}}, 0},
	} {
		c.query.Origins = []catalog.SearchResultType{catalog.SearchResultIndexed}
		page, err := store.SearchIndexedCatalog(t.Context(), housekeepingTenantA, catalog.FederatedCatalogSearchRequest{Query: c.query, Limit: 10})
		if err != nil || page.IndexedTotal != c.total || len(page.Indexed) != c.total {
			t.Fatalf("query=%+v page=%+v %v", c.query, page, err)
		}
	}
	// Exact identity uses the shifted filter/page bindings too.
	exact := &catalog.IndexedIdentity{Identifier: seen[0], Version: "1.0.0", VersionPresent: true}
	page, err := store.SearchIndexedCatalog(t.Context(), housekeepingTenantA, catalog.FederatedCatalogSearchRequest{Query: query, ExactIdentity: exact, Limit: 1})
	if err != nil || page.IndexedTotal != 1 || len(page.Indexed) != 1 {
		t.Fatalf("exact=%+v %v", page, err)
	}
	other, err := store.SearchIndexedCatalog(t.Context(), housekeepingTenantB, catalog.FederatedCatalogSearchRequest{Query: query, Limit: 10})
	if err != nil || other.IndexedTotal != 0 {
		t.Fatalf("cross tenant=%+v %v", other, err)
	}
	// Runtime DML cannot assign derived filters or their readiness marker.
	for _, column := range []string{"search_license", "search_categories", "search_filter_version"} {
		_, err := db.api.Exec(t.Context(), `UPDATE observations SET `+column+`=`+column+` WHERE tenant_id=$1`, housekeepingTenantA)
		requireInsufficientPrivilege(t, err)
		_, err = db.worker.Exec(t.Context(), `UPDATE observations SET `+column+`=`+column+` WHERE tenant_id=$1`, housekeepingTenantA)
		requireInsufficientPrivilege(t, err)
	}
	// Hybrid projection must retain the same selected source and presentation
	// generation as lexical discovery, including identical-byte contributors.
	lexical, err := store.SearchIndexedCatalog(t.Context(), housekeepingTenantA, catalog.FederatedCatalogSearchRequest{Query: query, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	evidence := map[string]*catalog.IndexedSearchEvidence{}
	for _, item := range lexical.Indexed {
		evidence[item.Identifier] = item.Indexed
	}
	// Real optional storage, metadata indexing and rank fusion apply the same
	// predicates before identity grouping, totals and either rank.
	workerSemantic := provisionSemanticFixture(t, db)
	indexer, err := catalog.NewSemanticIndexer(workerSemantic, &fixedIndexProvider{}, 4, 8192)
	if err != nil {
		t.Fatal(err)
	}
	g := catalog.SemanticGeneration{ID: "00000000-0000-7000-8000-000000000985", TenantID: housekeepingTenantA, Profile: "sha256:" + strings.Repeat("a", 64), Scope: "control", Dimensions: 3}
	if err := db.operator.QueryRow(t.Context(), "SELECT generation::text FROM catalog_search_generations WHERE tenant_id=$1", g.TenantID).Scan(&g.SourceGeneration); err != nil {
		t.Fatal(err)
	}
	workerJobs, err := NewPostgresJobStore(workerSemantic.repository, g.Profile)
	if err != nil {
		t.Fatal(err)
	}
	job := housekeepingJob(g.ID, "", time.Now().UTC().Add(-time.Second))
	job.Kind = "semantic.reindex"
	job.Payload, err = json.Marshal(catalog.SemanticReindexInput{Profile: g.Profile, Scope: g.Scope, SourceGeneration: g.SourceGeneration})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := db.apiJobs.Enqueue(t.Context(), job); err != nil {
		t.Fatal(err)
	}
	if _, err := workerJobs.Claim(t.Context(), "filter-index", time.Now().UTC(), time.Minute); err != nil {
		t.Fatal(err)
	}
	if _, err := indexer.Run(t.Context(), g, "filter-index"); err != nil {
		t.Fatal(err)
	}
	apiSemantic, err := NewPostgresSemanticStore(t.Context(), repo)
	if err != nil {
		t.Fatal(err)
	}
	hybrid := catalog.SemanticSearchRequest{Generation: g, Query: query, Vector: []float32{1, 0, 0}, Cutoff: 0.5}
	for _, text := range []string{"invoice", "no lexical match"} {
		hybrid.Query.Text, hybrid.Query.Limit, hybrid.After = text, 1, nil
		for i := range 2 {
			page, err := apiSemantic.Search(t.Context(), hybrid)
			if err != nil || page.Total != 2 || len(page.Items) != 1 || page.Items[0].Result.Identifier != seen[i] {
				t.Fatalf("hybrid text=%q page=%+v %v", text, page, err)
			}
			item := page.Items[0].Result
			if !reflect.DeepEqual(item.Indexed, evidence[item.Identifier]) || item.Indexed.PresentationSourceID == "" || item.Indexed.PresentationGeneration < 1 || item.Indexed.PresentationSourceRevision < 1 {
				t.Fatalf("hybrid source evidence=%+v lexical=%+v", item.Indexed, evidence[item.Identifier])
			}
			if page.HasMore != (i == 0) {
				t.Fatal("hybrid continuation")
			}
			position := page.Items[0].Position
			hybrid.After = &position
		}
	}

	verifyIndexedFilterUpgradeAndRepair(t, db, store, query, generation)
	seed(0)
	indexedCount, indexedLimited, indexedErr := store.CountIndexedInventory(t.Context(), housekeepingTenantA)
	if indexedErr != nil || indexedLimited || indexedCount == nil || *indexedCount != 8 {
		t.Fatal("indexed overview applied local shadowing or counted duplicate contributions", indexedCount, indexedLimited, indexedErr)
	}
	indexedOnly, err := store.SearchIndexedCatalog(t.Context(), housekeepingTenantA, catalog.FederatedCatalogSearchRequest{Query: query, Limit: 10})
	if err != nil || indexedOnly.IndexedTotal != 2 || len(indexedOnly.Indexed) != 2 {
		t.Fatalf("excluded local origin suppressed indexed match: %+v %v", indexedOnly, err)
	}
	withLocal := query
	withLocal.Origins = []catalog.SearchResultType{catalog.SearchResultLocal, catalog.SearchResultIndexed}
	localPrecedence, err := store.SearchIndexedCatalog(t.Context(), housekeepingTenantA, catalog.FederatedCatalogSearchRequest{Query: withLocal, Limit: 10})
	if err != nil || localPrecedence.IndexedTotal != 1 || len(localPrecedence.Indexed) != 1 || localPrecedence.Indexed[0].Identifier != seen[0] {
		t.Fatalf("local authority bypassed by optional filter: %+v %v", localPrecedence, err)
	}
	// Apply current source exclusions through the ordinary administrator path.
	// Retained observations are not resynchronized or rewritten.
	principal, err := ProvisionIdentity(t.Context(), db.operator, IdentityProvisioningInput{TenantID: housekeepingTenantA, TenantDisplayName: "Housekeeping tenant A", Issuer: "https://identity.performance.example", TenancyMode: "dedicated", Subject: "lexical-source", ActorType: identity.ActorHuman, Roles: []identity.Role{identity.RoleSourceAdmin}, Operator: "lexical-test", Reason: "synthetic lexical source", AcknowledgeOutbox: true})
	if err != nil {
		t.Fatal(err)
	}
	admin := postgresFixtureContext(t, db, t.Context(), identity.Actor{TenantID: housekeepingTenantA, Subject: "lexical-source", Roles: []identity.Role{identity.RoleSourceAdmin}, MembershipID: principal.MembershipID, RealmID: principal.RealmID, ActorType: principal.ActorType})
	sources, err := manager.ListSourcePage(admin, federation.SourceListRequest{Limit: 10})
	if err != nil || len(sources) != 3 {
		t.Fatalf("sources=%d %v", len(sources), err)
	}
	for _, source := range sources {
		scope := source.Scope
		scope.ExcludedIdentifiers = seen
		patch := federation.PatchInput{Scope: &scope, Intent: federation.ChangeUpdate, Reason: "Exclude the remaining declaration match"}
		preview, err := manager.PreviewChange(admin, source.ID, patch)
		if err != nil {
			t.Fatal(err)
		}
		patch.ChangePreviewDigest = preview.Digest
		if _, err := manager.UpdateSource(admin, source.ID, patch, source.Revision); err != nil {
			t.Fatal(err)
		}
	}
	excluded, err := store.SearchIndexedCatalog(t.Context(), housekeepingTenantA, catalog.FederatedCatalogSearchRequest{Query: query, Limit: 10})
	if err != nil || excluded.IndexedTotal != 0 || len(excluded.Indexed) != 0 || excluded.HasMore {
		t.Fatalf("current source exclusions bypassed by declaration filters: %+v %v", excluded, err)
	}
}

func verifyIndexedFilterUpgradeAndRepair(t *testing.T, db *housekeepingDatabase, store *PostgresFederationStore, query catalog.SearchQuery, oldGeneration string) {
	t.Helper()
	readEvidence := func() string {
		t.Helper()
		var evidence string
		if err := db.operator.QueryRow(t.Context(), `SELECT jsonb_agg(jsonb_build_array(id,raw_entry_bytes,normalized_entry_bytes,exact_entry_digest,normalized_entry_digest,identity_digest) ORDER BY id)::text FROM observations WHERE tenant_id=$1`, housekeepingTenantA).Scan(&evidence); err != nil {
			t.Fatal(err)
		}
		return evidence
	}
	before := readEvidence()
	// Remove only this migration's derived schema in the disposable fixture.
	if _, err := db.operator.Exec(t.Context(), `DROP INDEX observations_catalog_search_projection_missing;
DROP TRIGGER observations_control_filter_projection ON observations;
DROP FUNCTION project_observation_control_filters();
DROP FUNCTION catalog_observation_license_v1(jsonb); DROP FUNCTION catalog_observation_categories_v1(jsonb); DROP FUNCTION catalog_control_term_v1(text);
ALTER TABLE observations DROP COLUMN search_license, DROP COLUMN search_categories, DROP COLUMN search_filter_version;
DELETE FROM catalog_schema_migrations WHERE version=39;`); err != nil {
		t.Fatal(err)
	}
	old, err := migrations.Files.ReadFile("000027_search_normalization.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := string(old)
	start := strings.Index(sql, "CREATE INDEX observations_catalog_search_projection_missing")
	end := start + strings.Index(sql[start:], "COMMENT ON COLUMN")
	if _, err := db.operator.Exec(t.Context(), sql[start:end]); err != nil {
		t.Fatal(err)
	}
	plan, err := Plan(t.Context(), db.operator)
	if err != nil || len(plan) != 47 || plan[38].Applied {
		t.Fatalf("upgrade plan=%+v %v", plan, err)
	}
	applied, reports, err := ApplyAndReconcileRuntimePrivileges(t.Context(), db.operator, db.api.Config().ConnConfig.User, db.worker.Config().ConnConfig.User, true)
	if err != nil || len(applied) != 1 || applied[0].Version != 39 || len(reports) != 2 || !reports[0].Compliant || !reports[1].Compliant {
		t.Fatalf("apply=%+v reports=%+v %v", applied, reports, err)
	}
	if readEvidence() != before {
		t.Fatal("migration rewrote retained bytes or digests")
	}
	if _, err := store.SearchIndexedCatalog(t.Context(), housekeepingTenantA, catalog.FederatedCatalogSearchRequest{Query: query, Limit: 10}); catalog.ErrorCodeOf(err) != catalog.CodeDependencyUnavailable {
		t.Fatalf("unrepaired search=%v", err)
	}
	if count, _, err := store.CountIndexedInventory(t.Context(), housekeepingTenantA); catalog.ErrorCodeOf(err) != catalog.CodeDependencyUnavailable || count != nil {
		t.Fatalf("overview ignored missing projections: %v %v", count, err)
	}
	if empty, err := store.SearchIndexedCatalog(t.Context(), housekeepingTenantB, catalog.FederatedCatalogSearchRequest{Query: query, Limit: 10}); err != nil || empty.IndexedTotal != 0 {
		t.Fatalf("other tenant blocked=%+v %v", empty, err)
	}
	for step := range 9 {
		result, err := RepairCatalogSearchProjections(t.Context(), db.operator, housekeepingTenantA, 2, "Rebuild optional discovery filters", "s02-batch4")
		want := int64(2)
		if step == 8 {
			want = 1
		}
		if err != nil || result.Processed != want || result.Complete != (step == 8) || result.AfterGeneration-result.BeforeGeneration != want || result.AuditEventID == "" {
			t.Fatalf("repair=%+v %v", result, err)
		}
	}
	if readEvidence() != before {
		t.Fatal("repair rewrote retained bytes/digests")
	}
	if done, err := RepairCatalogSearchProjections(t.Context(), db.operator, housekeepingTenantA, 2, "Repeat completed repair", "s02-batch4"); err != nil || !done.Complete || done.Processed != 0 || done.AuditEventID != "" {
		t.Fatalf("repeat=%+v %v", done, err)
	}
	if _, err := store.SearchIndexedCatalog(t.Context(), housekeepingTenantA, catalog.FederatedCatalogSearchRequest{Query: query, Limit: 1, ExpectedGeneration: oldGeneration}); catalog.ErrorCodeOf(err) != catalog.CodeInvalidArgument {
		t.Fatalf("old cursor survived repair=%v", err)
	}
	page, err := store.SearchIndexedCatalog(t.Context(), housekeepingTenantA, catalog.FederatedCatalogSearchRequest{Query: query, Limit: 10})
	if err != nil || page.IndexedTotal != 2 {
		t.Fatalf("repaired=%+v %v", page, err)
	}
	// A current marker cannot conceal corrupted derived fields during restore.
	// Roll back the fixture fault, including its trigger change and generation.
	func() {
		tx, err := db.operator.Begin(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx.Rollback(t.Context()) }()
		if _, err := tx.Exec(t.Context(), `SELECT set_config('app.tenant_id',$1,true)`, housekeepingTenantA); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(t.Context(), `ALTER TABLE observations DISABLE TRIGGER observations_control_filter_projection;
UPDATE observations SET search_license='invented' WHERE id=(SELECT id FROM observations ORDER BY id LIMIT 1);`); err != nil {
			t.Fatal(err)
		}
		records, _, err := scanRestoreObservations(t.Context(), tx, housekeepingTenantA, "", 1)
		if err != nil || len(records) != 1 || records[0].finding == nil || records[0].finding.Code != "RESTORE_SEARCH_FILTER_PROJECTION_INVALID" {
			t.Fatalf("restore declaration drift=%+v %v", records, err)
		}
	}()
	_, err = withTenantTx(t.Context(), db.api, housekeepingTenantB, pgx.TxOptions{}, func(tx pgx.Tx) (int, error) {
		var count int
		err := tx.QueryRow(t.Context(), `SELECT count(*) FROM observations WHERE tenant_id=$1`, housekeepingTenantA).Scan(&count)
		if count != 0 {
			t.Error("RLS disclosed another tenant")
		}
		return count, err
	})
	if err != nil {
		t.Fatal(err)
	}
}
