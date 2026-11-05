//go:build integration

package database

import (
	"encoding/json"
	"errors"
	"slices"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
)

func TestPostgresLocalSearchProjectionUpgrade(t *testing.T) {
	db, repository, _, seed := newSearchCostFixture(t, loadLexicalCorpus(t).documents()...)
	for i := 0; i < 3; i++ {
		seed(i)
	}
	documents := loadLexicalCorpus(t).documents()
	seedLexicalSource(t, db, repository, documents)
	var indexedBefore string
	if err := db.operator.QueryRow(t.Context(), "SELECT string_agg(exact_entry_digest || normalized_entry_digest,'|' ORDER BY id) FROM observations").Scan(&indexedBefore); err != nil {
		t.Fatal(err)
	}
	var before string
	if err := db.operator.QueryRow(t.Context(), "SELECT string_agg(digest,'|' ORDER BY id) FROM listing_revisions").Scan(&before); err != nil {
		t.Fatal(err)
	}
	// Reconstruct only the pre-29 derived schema in this owned test database.
	// Keep original metadata and sealed digests, then apply the actual migration.
	if _, err := db.operator.Exec(t.Context(), `ALTER TABLE resources DROP COLUMN search_identifier_vector;
        ALTER TABLE listing_revisions DROP COLUMN search_display_vector, DROP COLUMN search_description_vector,
          DROP COLUMN search_tags_vector, DROP COLUMN search_queries_vector, DROP COLUMN search_sort_name;
        ALTER TABLE observations DROP COLUMN search_identifier_vector, DROP COLUMN search_display_vector,
          DROP COLUMN search_description_vector, DROP COLUMN search_tags_vector, DROP COLUMN search_queries_vector;
        DELETE FROM catalog_schema_migrations WHERE version=30;
        DROP FUNCTION catalog_core_lexical_array_vector_v1(text[]);
        DELETE FROM catalog_schema_migrations WHERE version=29;`); err != nil {
		t.Fatal(err)
	}
	apiRole, workerRole := db.api.Config().ConnConfig.User, db.worker.Config().ConnConfig.User
	applied, reports, err := ApplyAndReconcileRuntimePrivileges(t.Context(), db.operator, apiRole, workerRole)
	if err != nil || len(applied) != 2 || applied[0].Version != 29 || applied[1].Version != 30 || len(reports) != 2 || !reports[0].Compliant || !reports[1].Compliant {
		t.Fatalf("projection upgrade/reconciliation: %v %v %v", applied, reports, err)
	}
	var after string
	if err := db.operator.QueryRow(t.Context(), "SELECT string_agg(digest,'|' ORDER BY id) FROM listing_revisions").Scan(&after); err != nil || after != before {
		t.Fatalf("sealed digests changed: %v", err)
	}
	var indexedAfter string
	if err := db.operator.QueryRow(t.Context(), "SELECT string_agg(exact_entry_digest || normalized_entry_digest,'|' ORDER BY id) FROM observations").Scan(&indexedAfter); err != nil || indexedBefore != indexedAfter {
		t.Fatalf("indexed evidence digests changed: %v", err)
	}
	var equal bool
	if err := db.operator.QueryRow(t.Context(), `SELECT bool_and(
        search_identifier_vector=catalog_core_lexical_vector_v1(identifier)
        AND search_display_vector=catalog_core_lexical_vector_v1(search_display_name)
        AND search_description_vector=catalog_core_lexical_vector_v1(search_description)
        AND search_tags_vector=catalog_core_lexical_array_vector_v1(search_tags)
        AND search_queries_vector=catalog_core_lexical_array_vector_v1(search_representative_queries))
        FROM observations WHERE tenant_id=$1`, housekeepingTenantA).Scan(&equal); err != nil || !equal {
		t.Fatalf("indexed ranking vectors differ from retained fields: equal=%v err=%v", equal, err)
	}
	if _, err := db.worker.Exec(t.Context(), "SELECT search_display_vector FROM observations LIMIT 1"); err == nil {
		t.Fatal("worker can read API-only indexed ranking projections")
	} else {
		requireInsufficientPrivilege(t, err)
	}
	var stored int
	if err := db.operator.QueryRow(t.Context(), `SELECT count(*) FROM pg_catalog.pg_attribute
        WHERE attrelid IN ('resources'::regclass,'listing_revisions'::regclass) AND attgenerated='s' AND attnotnull`).Scan(&stored); err != nil || stored != 6 {
		t.Fatalf("expected six stored, non-null projections: count=%d err=%v", stored, err)
	}
	_, err = withTenantRead(t.Context(), db.api, housekeepingTenantA, func(tx pgx.Tx) (int, error) {
		var count int
		var equal bool
		err := tx.QueryRow(t.Context(), `SELECT count(*),bool_and(
          resource.search_identifier_vector = catalog_core_lexical_vector_v1(resource.canonical_identifier)
          AND revision.search_display_vector = catalog_core_lexical_vector_v1(revision.display_name)
          AND revision.search_description_vector = catalog_core_lexical_vector_v1(revision.description)
          AND revision.search_tags_vector = catalog_core_lexical_vector_v1(array_to_string(revision.tags,' '))
          AND revision.search_queries_vector = catalog_core_lexical_vector_v1(array_to_string(revision.representative_queries,' '))
          AND revision.search_sort_name = catalog_core_sort_name_v1(revision.display_name))
          FROM listing_revisions revision
          JOIN catalog_listings listing ON listing.tenant_id=revision.tenant_id AND listing.id=revision.listing_id
          JOIN resources resource ON resource.tenant_id=listing.tenant_id AND resource.id=listing.resource_id
          WHERE revision.tenant_id=$1`, housekeepingTenantA).Scan(&count, &equal)
		if err != nil || count != 3 || !equal {
			t.Fatalf("stored projections differ: count=%d equal=%v err=%v", count, equal, err)
		}
		return count, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	// The database itself rejects writes to generated fields, even by its owner.
	_, err = db.operator.Exec(t.Context(), "UPDATE resources SET search_identifier_vector='forged'::tsvector")
	var pgError *pgconn.PgError
	if !errors.As(err, &pgError) || pgError.Code != "428C9" {
		t.Fatalf("generated field overwrite was not rejected: %v", err)
	}
	for _, sql := range []string{"SELECT search_identifier_vector FROM resources LIMIT 1", "SELECT search_tags_vector FROM listing_revisions LIMIT 1"} {
		_, err := db.worker.Exec(t.Context(), sql)
		if !errors.As(err, &pgError) || pgError.Code != "42501" {
			t.Fatalf("worker acquired local search authority: %v", err)
		}
	}
	page, err := repository.SearchCatalog(t.Context(), housekeepingTenantB, catalog.CatalogSearchRepositoryRequest{Limit: 20})
	if err != nil || page.Total != 0 || len(page.Items) != 0 {
		t.Fatalf("projection leaked across tenants: %+v %v", page, err)
	}
	page, err = repository.SearchCatalog(t.Context(), housekeepingTenantA, catalog.CatalogSearchRepositoryRequest{Limit: 20})
	if err != nil || page.Total != 3 || len(page.Items) != 3 {
		t.Fatalf("upgraded search: %+v %v", page, err)
	}
	latest := 0
	for _, item := range page.Items {
		var original, current string
		if err := db.operator.QueryRow(t.Context(), `SELECT revision.evidence_set_id,
          coalesce((SELECT run.evidence_set_id FROM version_validation_runs run
            WHERE run.tenant_id=revision.tenant_id AND run.version_revision_id=revision.id
            ORDER BY run.run_number DESC LIMIT 1),revision.evidence_set_id)
          FROM resource_version_revisions revision WHERE revision.tenant_id=$1 AND revision.id=$2`, housekeepingTenantA, item.Release.VersionRevisionID).Scan(&original, &current); err != nil {
			t.Fatal(err)
		}
		if item.Evidence.ID != current {
			t.Fatal("search hydration returned obsolete evidence")
		}
		if original != current {
			latest++
		}
	}
	if latest != 1 {
		t.Fatal("fixture must include one revalidated version")
	}
	explored, err := repository.ExploreCatalog(t.Context(), housekeepingTenantA, catalog.SearchQuery{}, []catalog.FacetRequest{{Field: "trustState"}})
	if err != nil || explored.Matched != 3 || !slices.Equal(explored.Facets["trustState"].Buckets, []catalog.FacetBucket{{Value: "not_assessed", Count: 3}}) {
		t.Fatalf("trust facet lost evidence after selection pruning: %+v %v", explored, err)
	}
	// Reconciliation is repeat-safe after the materializing migration.
	applied, _, err = ApplyAndReconcileRuntimePrivileges(t.Context(), db.operator, apiRole, workerRole)
	if err != nil || len(applied) != 0 {
		t.Fatalf("projection upgrade is not repeat-safe: %v %v", applied, err)
	}
}

func TestPostgresSearchPlansDoNotReadUnusedEvidence(t *testing.T) {
	db, _, _, seed := newSearchCostFixture(t)
	seed(0)
	for _, text := range []string{"", "Performance"} {
		query := catalog.SearchQuery{Text: text}
		records, page := catalogSearchStatements(query)
		facets, limits, err := catalogFacetStatement(query, []catalog.FacetRequest{{Field: "kind"}, {Field: "tag"}})
		if err != nil {
			t.Fatal(err)
		}
		for _, statement := range []struct {
			sql  string
			args []any
		}{
			{records + "SELECT count(*) FROM matched", catalogSearchParameters(housekeepingTenantA, query)},
			{page, append(catalogSearchParameters(housekeepingTenantA, query), false, 0, "", "", 21)},
			{facets, append(catalogSearchParameters(housekeepingTenantA, query), limits...)},
		} {
			_, err := withTenantRead(t.Context(), db.api, housekeepingTenantA, func(tx pgx.Tx) (int, error) {
				var raw []byte
				if err := tx.QueryRow(t.Context(), "EXPLAIN (FORMAT JSON) "+statement.sql, statement.args...).Scan(&raw); err != nil {
					return 0, err
				}
				var plans []struct {
					Plan searchPlanNode `json:"Plan"`
				}
				if err := json.Unmarshal(raw, &plans); err != nil {
					return 0, err
				}
				if len(plans) != 1 {
					t.Fatal("missing query plan")
				}
				var check func(searchPlanNode)
				check = func(node searchPlanNode) {
					// Assert dependency removal, not an index, scan/join strategy
					// or timing threshold. Hydration still reads current evidence.
					if slices.Contains([]string{"version_validation_runs", "evidence_sets", "resource_version_revisions"}, node.Relation) {
						t.Fatalf("unused evidence read remains in selection: %s", node.Relation)
					}
					for _, child := range node.Plans {
						check(child)
					}
				}
				check(plans[0].Plan)
				return 0, nil
			})
			if err != nil {
				t.Fatal(err)
			}
		}
	}
}
