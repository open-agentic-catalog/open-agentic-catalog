//go:build integration

package database

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/adapter"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/clock"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/id"
)

func TestPostgresDirectoryExactFilters(t *testing.T) {
	db, repo, _, _ := newSearchCostFixture(t)
	ctx := semanticDisclosureActor(t, db, housekeepingTenantA)
	service, err := catalog.NewService(catalog.Options{Repository: repo, Clock: clock.Real{}, IDs: id.Generator{}, Validator: adapter.NewRegistry(), CursorKey: []byte("01234567890123456789012345678901")})
	if err != nil {
		t.Fatal(err)
	}
	store, err := NewPostgresIdentityStore(repo)
	if err != nil {
		t.Fatal(err)
	}
	authority, err := identity.NewService(identity.ServiceOptions{Store: store, Clock: clock.Real{}, IDs: id.Generator{}, CursorKey: []byte("01234567890123456789012345678901")})
	if err != nil {
		t.Fatal(err)
	}
	namespaces := make([]catalog.Namespace, 0, 3)
	for _, slug := range []string{"aaa", "bbb", "ccc"} {
		ns, err := service.CreateNamespace(ctx, catalog.CreateNamespaceInput{Slug: slug, DisplayName: "Same 日本語"})
		if err != nil {
			t.Fatal(err)
		}
		namespaces = append(namespaces, ns)
		if slug != "aaa" {
			if _, err := authority.GrantRole(ctx, identity.GrantRoleInput{Subject: "directory-reader", ActorType: identity.ActorHuman, Role: identity.RoleReader, NamespaceID: ns.ID, Reason: "Owned exact directory visibility"}, "grant-"+slug); err != nil {
				t.Fatal(err)
			}
		}
	}
	reader, err := store.ResolvePrincipal(t.Context(), identity.Principal{TenantID: housekeepingTenantA, Issuer: "https://identity.performance.example", Subject: "directory-reader"})
	if err != nil {
		t.Fatal(err)
	}
	readerCtx := identity.WithActor(t.Context(), reader)
	items, err := service.ListNamespaces(readerCtx, catalog.NamespaceListRequest{DisplayName: "Same 日本語", Limit: 1})
	if err != nil || len(items) != 2 || items[0].ID != namespaces[1].ID || items[1].ID != namespaces[2].ID {
		t.Fatal(items, err)
	}
	items, err = service.ListNamespaces(readerCtx, catalog.NamespaceListRequest{Slug: " CCC ", DisplayName: "Same 日本語", Limit: 1})
	if err != nil || len(items) != 1 || items[0].ID != namespaces[2].ID {
		t.Fatal(items, err)
	}
	for _, req := range []catalog.NamespaceListRequest{
		{Slug: "aaa", Limit: 1}, {Slug: "ccc", DisplayName: "same 日本語", Limit: 1},
		{DisplayName: "Same 日本語", AfterSlug: "ccc", AfterID: namespaces[2].ID, Limit: 1},
	} {
		items, err = service.ListNamespaces(readerCtx, req)
		if err != nil || len(items) != 0 {
			t.Fatal(req, items, err)
		}
	}
	otherCtx := semanticDisclosureActor(t, db, housekeepingTenantB)
	items, err = service.ListNamespaces(otherCtx, catalog.NamespaceListRequest{DisplayName: "Same 日本語", Limit: 1})
	if err != nil || len(items) != 0 {
		t.Fatal(items, err)
	}
	publishers := make([]catalog.Publisher, 0, 3)
	for _, domain := range []string{"one.example", "two.example", "three.example"} {
		pub, err := service.CreatePublisher(ctx, catalog.CreatePublisherInput{DisplayName: "Same 日本語", Domains: []string{domain}})
		if err != nil {
			t.Fatal(err)
		}
		publishers = append(publishers, pub)
	}
	page, err := service.ListPublishers(ctx, catalog.PublisherListRequest{DisplayName: "Same 日本語", DeclaredDomain: " TWO.Example. ", Limit: 1})
	if err != nil || len(page) != 1 || page[0].ID != publishers[1].ID {
		t.Fatal(page, err)
	}
	page, err = service.ListPublishers(ctx, catalog.PublisherListRequest{DisplayName: "Same 日本語", Limit: 1})
	if err != nil || len(page) != 2 {
		t.Fatal(page, err)
	}
	first := page[0]
	page, err = service.ListPublishers(ctx, catalog.PublisherListRequest{DisplayName: "Same 日本語", AfterDisplayName: first.DisplayName, AfterID: first.ID, Limit: 1})
	if err != nil || len(page) != 2 || page[0].ID == first.ID {
		t.Fatal(page, err)
	}
	for _, req := range []catalog.PublisherListRequest{{DisplayName: "same 日本語", DeclaredDomain: "two.example", Limit: 1}, {DeclaredDomain: "missing.example", Limit: 1}} {
		page, err = service.ListPublishers(ctx, req)
		if err != nil || len(page) != 0 {
			t.Fatal(page, err)
		}
	}
	page, err = service.ListPublishers(otherCtx, catalog.PublisherListRequest{DisplayName: "Same 日本語", Limit: 1})
	if err != nil || len(page) != 0 {
		t.Fatal(page, err)
	}
	// The same declared domain may exist in another tenant, but never crosses
	// the runtime role's tenant predicate or forced RLS boundary.
	foreign, err := service.CreatePublisher(otherCtx, catalog.CreatePublisherInput{DisplayName: "Same 日本語", Domains: []string{"two.example"}})
	if err != nil {
		t.Fatal(err)
	}
	page, err = service.ListPublishers(ctx, catalog.PublisherListRequest{DeclaredDomain: "two.example", Limit: 1})
	if err != nil || len(page) != 1 || page[0].ID == foreign.ID {
		t.Fatal(page, err)
	}
	collectionCtx, _, err := identity.WithCollectionContext(ctx)
	if err != nil {
		t.Fatal(err)
	}
	_, err = withTenantCollectionRead(collectionCtx, repo.pool, housekeepingTenantA, func(tx pgx.Tx) (bool, error) {
		nsSQL, nsArgs := namespacePageStatement(collectionCtx, housekeepingTenantB, catalog.NamespaceListRequest{DisplayName: "Same 日本語", Limit: 2})
		pubSQL, pubArgs := publisherPageStatement(housekeepingTenantB, catalog.PublisherListRequest{DeclaredDomain: "two.example", Limit: 2})
		for _, query := range []struct {
			sql  string
			args []any
		}{{nsSQL, nsArgs}, {pubSQL, pubArgs}} {
			rows, err := tx.Query(collectionCtx, query.sql, query.args...)
			if err != nil {
				return false, err
			}
			hasForeign := rows.Next()
			rows.Close()
			if rows.Err() != nil {
				return false, rows.Err()
			}
			if hasForeign {
				t.Fatal("forced RLS disclosed another tenant through exact filters")
			}
		}
		return true, nil
	})
	if err != nil {
		t.Fatal(err)
	}

}

func TestPostgresDirectoryLookupMigration(t *testing.T) {
	db := newHousekeepingDatabase(t)
	_ = semanticDisclosureActor(t, db, housekeepingTenantA)
	plan, err := Plan(t.Context(), db.operator)
	if err != nil || len(plan) != 47 || !plan[44].Applied || plan[44].Name != "000045_directory_lookup.sql" {
		t.Fatal(plan, err)
	}
	applied, reports, err := ApplyAndReconcileRuntimePrivileges(t.Context(), db.operator, db.api.Config().ConnConfig.User, db.worker.Config().ConnConfig.User)
	if err != nil || len(applied) != 0 || len(reports) != 2 {
		t.Fatal(applied, reports, err)
	}
	report, err := VerifyRestoredCatalog(t.Context(), db.operator, RestoreVerificationOptions{TenantID: housekeepingTenantA, RecoveryPoint: "directory-index-fixture", APIRole: db.api.Config().ConnConfig.User, WorkerRole: db.worker.Config().ConnConfig.User, Limit: 100})
	if err != nil || report.SchemaVersion != 47 || report.PrivilegeManifestVersion != runtimePrivilegeManifestVersion || report.Integrity != restoreIntegrityValid {
		t.Fatal(report, err)
	}
}

// Measured diagnostics use the production selection SQL, exact API login,
// forced RLS/current policy, realistic selectivity and both plan cache modes.
// Timing is evidence, not a deterministic latency gate or a capacity claim.
func TestPostgresDirectoryFilterPlans(t *testing.T) {
	db, repo, _, _ := newSearchCostFixture(t)
	ctx := semanticDisclosureActor(t, db, housekeepingTenantA)
	for _, query := range []string{
		`INSERT INTO namespaces(id,tenant_id,slug,display_name,revision,created_at,updated_at) SELECT md5('directory-ns-'||n)::uuid,$1,'directory-'||lpad(n::text,5,'0'),CASE WHEN n=10000 THEN 'Rare directory' ELSE 'Common directory' END,1,statement_timestamp(),statement_timestamp() FROM generate_series(1,10000) n`,
		`INSERT INTO publishers(id,tenant_id,display_name,domains,revision,created_at,updated_at) SELECT md5('directory-pub-'||n)::uuid,$1,CASE WHEN n=10000 THEN 'Rare directory' ELSE 'Common directory' END,ARRAY['directory-'||n||'.example'],1,statement_timestamp(),statement_timestamp() FROM generate_series(1,10000) n`,
	} {
		if _, err := db.operator.Exec(t.Context(), query, housekeepingTenantA); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.operator.Exec(t.Context(), `ANALYZE namespaces; ANALYZE publishers`); err != nil {
		t.Fatal(err)
	}
	var namespaceIndexBytes int64
	if err := db.operator.QueryRow(t.Context(), `SELECT pg_relation_size('namespaces_display_name_page'::regclass)`).Scan(&namespaceIndexBytes); err != nil {
		t.Fatal(err)
	}
	t.Logf("Namespace display-name index storage bytes=%d for 10000 synthetic entries; ongoing write maintenance is additional and not a throughput qualification", namespaceIndexBytes)
	var containmentLeakproof bool
	if err := db.operator.QueryRow(t.Context(), `SELECT proleakproof FROM pg_catalog.pg_proc WHERE oid='pg_catalog.arraycontains(anyarray,anyarray)'::regprocedure`).Scan(&containmentLeakproof); err != nil {
		t.Fatal(err)
	}
	t.Logf("PostgreSQL array containment leakproof=%t; retain forced RLS and qualify the runtime plan", containmentLeakproof)
	collectionCtx, _, err := identity.WithCollectionContext(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"force_custom_plan", "force_generic_plan"} {
		for _, name := range []string{"Common directory", "Rare directory", "Absent directory"} {
			for _, directory := range []string{"namespace", "publisher"} {
				request := catalog.NamespaceListRequest{DisplayName: name, Limit: 2}
				query, args := namespacePageStatement(collectionCtx, housekeepingTenantA, request)
				if directory == "publisher" {
					query, args = publisherPageStatement(housekeepingTenantA, catalog.PublisherListRequest{DisplayName: name, Limit: 2})
				}
				directoryPlan(t, repo, collectionCtx, mode, directory+"-"+name, query, args)
			}
		}

		for _, request := range []catalog.NamespaceListRequest{
			{Slug: "directory-10000", Limit: 2},
			{Slug: "directory-10000", DisplayName: "Rare directory", Limit: 2},
			{DisplayName: "Common directory", AfterSlug: "directory-05000", AfterID: "00000000-0000-7000-8000-000000000000", Limit: 2},
		} {
			query, args := namespacePageStatement(collectionCtx, housekeepingTenantA, request)
			directoryPlan(t, repo, collectionCtx, mode, "namespace-position-"+request.Slug, query, args)
		}
		query, args := publisherPageStatement(housekeepingTenantA, catalog.PublisherListRequest{DisplayName: "Rare directory", DeclaredDomain: "directory-10000.example", Limit: 2})
		directoryPlan(t, repo, collectionCtx, mode, "publisher-intersection", query, args)
		query, args = publisherPageStatement(housekeepingTenantA, catalog.PublisherListRequest{DisplayName: "Common directory", AfterDisplayName: "Common directory", AfterID: "80000000-0000-7000-8000-000000000000", Limit: 2})
		directoryPlan(t, repo, collectionCtx, mode, "publisher-continuation", query, args)
		for _, domain := range []string{"directory-10000.example", "absent.example"} {
			query, args := publisherPageStatement(housekeepingTenantA, catalog.PublisherListRequest{DeclaredDomain: domain, Limit: 2})
			directoryPlan(t, repo, collectionCtx, mode, "domain-"+domain, query, args)
		}
	}
}

func directoryPlan(t *testing.T, repo *PostgresRepository, ctx context.Context, mode, name, query string, args []any) {
	t.Helper()
	_, err := withTenantCollectionRead(ctx, repo.pool, housekeepingTenantA, func(tx pgx.Tx) (bool, error) {
		if _, err := tx.Exec(ctx, "SET LOCAL plan_cache_mode="+mode); err != nil {
			return false, err
		}
		literals := make([]string, len(args))
		for i, arg := range args {
			switch v := arg.(type) {
			case string:
				literals[i] = "'" + strings.ReplaceAll(v, "'", "''") + "'"
			case int:
				literals[i] = fmt.Sprint(v)
			case []string:
				literals[i] = "ARRAY['" + strings.Join(v, "','") + "']::text[]"
			default:
				t.Fatalf("unexpected diagnostic argument %T", arg)
			}
		}
		raw, err := explainSearchDiagnostic(ctx, tx, "directory_filter_plan", query, literals)
		if err != nil {
			return false, err
		}
		var result []struct {
			Plan      searchPlanNode `json:"Plan"`
			Execution float64        `json:"Execution Time"`
		}
		if err := json.Unmarshal(raw, &result); err != nil {
			return false, err
		}
		if len(result) != 1 || result[0].Plan.Rows > 2 {
			t.Fatal("unbounded directory plan", string(raw))
		}

		if (name == "namespace-Rare directory" || name == "namespace-Absent directory") && result[0].Plan.Hits+result[0].Plan.Reads > 50 {
			t.Fatal("selective name lookup scanned the directory", string(raw))
		}
		t.Logf("directory-plan mode=%s case=%s rows=%.0f hits=%d reads=%d temp=%d execution-ms=%.3f plan=%s", mode, name, result[0].Plan.Rows, result[0].Plan.Hits, result[0].Plan.Reads, result[0].Plan.Temp, result[0].Execution, raw)
		return true, nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
