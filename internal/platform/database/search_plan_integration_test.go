//go:build integration

package database

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
)

type searchPlanNode struct {
	Type     string           `json:"Node Type"`
	Relation string           `json:"Relation Name"`
	Rows     float64          `json:"Actual Rows"`
	Loops    float64          `json:"Actual Loops"`
	Hits     int              `json:"Shared Hit Blocks"`
	Reads    int              `json:"Shared Read Blocks"`
	Temp     int              `json:"Temp Written Blocks"`
	Plans    []searchPlanNode `json:"Plans"`
}

// Keep the connection leased through rollback and prepared-statement cleanup.
// A pool-owned transaction releases its connection during Rollback itself.
func withSearchDiagnosticTx[T any](ctx context.Context, pool *pgxpool.Pool, operation func(pgx.Tx) (T, error)) (T, error) {
	conn, err := pool.Acquire(ctx)
	if err != nil {
		var zero T
		return zero, err
	}
	defer conn.Release()
	return withStandaloneTenantTx(ctx, conn, housekeepingTenantA, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}, operation)
}

// Prepared statements outlive a rolled-back transaction. Release only the
// owned statement while withSearchDiagnosticTx still owns the connection.
func explainSearchDiagnostic(ctx context.Context, tx pgx.Tx, name, query string, args []string) (raw []byte, err error) {
	description, err := tx.Conn().Prepare(ctx, name, query)
	if err != nil {
		return nil, err
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		if err != nil {
			err = errors.Join(err, tx.Rollback(cleanup))
		}
		err = errors.Join(err, tx.Conn().Deallocate(cleanup, name))
	}()
	if err = validateScalePlanArguments(len(description.ParamOIDs), args); err != nil {
		return nil, err
	}
	// Arguments are closed synthetic SQL literals, never caller-supplied values.
	err = tx.QueryRow(ctx, "EXPLAIN (ANALYZE, BUFFERS, TIMING OFF, FORMAT JSON) EXECUTE "+name+"("+strings.Join(args, ",")+")").Scan(&raw)
	return raw, err
}

func TestPostgresSearchDiagnosticStatementCleanup(t *testing.T) {
	_, repository, _, _ := newSearchCostFixture(t)
	for _, tc := range []struct {
		name, query string
		args        []string
	}{
		{"arity", "SELECT $1::text", nil},
		{"aborted transaction", "SELECT $1::integer", []string{"'not-an-integer'"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			const name = "search_diagnostic_cleanup"
			var backendPID uint32
			_, err := withSearchDiagnosticTx(t.Context(), repository.pool, func(tx pgx.Tx) ([]byte, error) {
				backendPID = tx.Conn().PgConn().PID()
				return explainSearchDiagnostic(t.Context(), tx, name, tc.query, tc.args)
			})
			if err == nil {
				t.Fatal("invalid diagnostic unexpectedly succeeded")
			}
			if tc.name == "arity" && !strings.Contains(err.Error(), "argument count") {
				t.Fatalf("argument mismatch reached another failure: %v", err)
			}
			if tc.name == "aborted transaction" {
				var failure *pgconn.PgError
				if !errors.As(err, &failure) || failure.Code != "22P02" {
					t.Fatalf("invalid integer did not abort the transaction: %v", err)
				}
			}
			// A replacement connection would mask a leaked statement. Require
			// the same backend and a different SQL statement under this name.
			raw, err := withSearchDiagnosticTx(t.Context(), repository.pool, func(tx pgx.Tx) ([]byte, error) {
				if tx.Conn().PgConn().PID() != backendPID {
					return nil, errors.New("diagnostic replaced its backend connection")
				}
				return explainSearchDiagnostic(t.Context(), tx, name, "SELECT length($1::text)", []string{"'control'"})
			})
			if err != nil || !json.Valid(raw) {
				t.Fatalf("connection or statement survived failed probe: %v", err)
			}
		})
	}
}

func searchDiagnosticReader(tb testing.TB, actor identity.Actor) context.Context {
	tb.Helper()
	ctx, _, err := identity.WithCollectionContext(identity.WithActor(tb.Context(), actor))
	if err != nil {
		tb.Fatal(err)
	}
	return ctx
}

// Explicit opt-in diagnostic: no timing or planner-choice assertion belongs in
// the normal regression suite. It uses real governed seeds and exact API roles.
func BenchmarkPostgresSearchPlans(b *testing.B) {
	db, repository, _, seed := newSearchCostFixture(b)
	// The fixture's explicit reader bootstrap bundle includes discovery reads.
	readerContext := postgresFixtureContext(b, db, b.Context(), identity.Actor{TenantID: housekeepingTenantA, Subject: "search-plan-reader", Roles: []identity.Role{identity.RoleReader}})
	reader, ok := identity.FromContext(readerContext)
	if !ok {
		b.Fatal("owned plan reader is unavailable")
	}
	seeded := 0
	for _, size := range []int{45, 100, 1000} {
		for seeded < size {
			seed(seeded)
			seeded++
		}
		for _, table := range []string{"resources", "resource_versions", "resource_version_revisions", "catalog_listings", "listing_releases", "listing_revisions", "evidence_sets", "version_validation_runs"} {
			if size == 45 {
				continue // Small initial population before explicit ANALYZE.
			}
			if _, err := db.operator.Exec(b.Context(), "ANALYZE "+pgx.Identifier{table}.Sanitize()); err != nil {
				b.Fatal(err)
			}
		}
		for _, probe := range []struct{ name, text string }{{"", ""}, {"Performance", "Performance"}, {"missingterm", "missingterm"}, {"skill-099", "skill-099"}, {"kind", "Performance"}} {
			text := probe.text
			search := catalog.SearchQuery{Text: text}
			kinds := "NULL"
			if probe.name == "kind" {
				search.Kinds = []catalog.Kind{catalog.KindSkill}
				kinds = "ARRAY['skill']"
			}
			for _, mode := range []string{"force_custom_plan", "force_generic_plan"} {
				for _, page := range []bool{false, true} {
					b.Run(fmt.Sprintf("rows=%d/text=%s/%s/page=%t", size, probe.name, mode, page), func(b *testing.B) {
						ctx, cancel := context.WithTimeout(searchDiagnosticReader(b, reader), 10*time.Second)
						defer cancel()
						_, err := withSearchDiagnosticTx(ctx, db.api, func(tx pgx.Tx) (int, error) {
							if err := initializeCollectionPolicy(ctx, tx, housekeepingTenantA); err != nil {
								return 0, err
							}
							if _, err := tx.Exec(ctx, "SET LOCAL plan_cache_mode="+mode); err != nil {
								return 0, err
							}
							recordsSQL, pageSQL := catalogSearchStatements(search)
							query := recordsSQL + "SELECT count(*) FROM matched"
							if page {
								query = pageSQL
							}
							const name = "catalog_search_plan_diagnostic"
							// Only fixed synthetic literals; never user or operator text.
							args := scalePlanArguments(housekeepingTenantA, "local", text, kinds)
							if page {
								args = append(args, "false", "0", "''", "''", "21")
							}
							raw, err := explainSearchDiagnostic(ctx, tx, name, query, args)
							if err != nil {
								return 0, err
							}
							var plans []struct {
								Plan      searchPlanNode `json:"Plan"`
								Planning  float64        `json:"Planning Time"`
								Execution float64        `json:"Execution Time"`
							}
							if err := json.Unmarshal(raw, &plans); err != nil {
								return 0, err
							}
							if len(plans) != 1 {
								return 0, fmt.Errorf("expected one query plan")
							}
							plan := plans[0]
							if page {
								expected := size
								if probe.name == "missingterm" {
									expected = 0
								} else if probe.name == "skill-099" {
									expected = 0
									if size >= 100 {
										expected = 1
									}
								}
								// The outer join retains one total-only row for an
								// empty page; it is not a resource result.
								expectedRows := max(1, min(expected, 21))
								if plan.Plan.Rows != float64(expectedRows) {
									return 0, fmt.Errorf("local plan population differs: got %.0f want %d", plan.Plan.Rows, expectedRows)
								}
							}
							b.Logf("planning_ms=%.3f execution_ms=%.3f rows=%.0f hits=%d reads=%d temp_written=%d", plan.Planning, plan.Execution, plan.Plan.Rows, plan.Plan.Hits, plan.Plan.Reads, plan.Plan.Temp)
							var logScans func(searchPlanNode)
							logScans = func(node searchPlanNode) {
								if node.Relation != "" {
									b.Logf("node=%s relation=%s rows=%.0f loops=%.0f", node.Type, node.Relation, node.Rows, node.Loops)
								}
								for _, child := range node.Plans {
									logScans(child)
								}
							}
							logScans(plan.Plan)
							return 0, nil
						})
						if err != nil {
							b.Fatal(err)
						}
					})
				}
			}
		}
	}
	benchmarkIndexedSearchPlans(b, db, repository, reader)
	benchmarkSearchRequestPlanning(b, db, reader)
	b.Run("local-facets", func(b *testing.B) {
		ctx := searchDiagnosticReader(b, reader)
		request := []catalog.FacetRequest{{Field: "kind", Limit: 2}, {Field: "tag", Limit: 2}}
		for range b.N {
			for range 20 {
				result, err := repository.ExploreCatalog(ctx, housekeepingTenantA, catalog.SearchQuery{Text: "Performance"}, request)
				if err != nil || result.Matched != 1000 || len(result.Facets["kind"].Buckets) != 1 || result.Facets["kind"].Buckets[0].Count != 1000 || len(result.Facets["tag"].Buckets) != 1 || result.Facets["tag"].Buckets[0].Count != 1000 {
					b.Fatalf("facet results changed: %+v %v", result, err)
				}
			}
		}
		b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(b.N*20), "ns/explore")
	})
}

// The adapter changes only the execution protocol of the real local selection;
// SQL, typed parameters, tenant context, snapshot and hydration stay identical.
type searchPlanningTx struct {
	pgx.Tx
	mode pgx.QueryExecMode
}

func (tx searchPlanningTx) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	if len(args) > 0 && args[0] == pgx.QueryExecModeExec {
		args = append([]any{tx.mode}, args[1:]...)
	}
	return tx.Tx.Query(ctx, sql, args...)
}

// Compare warmed cached/request-planned execution, including parameter encoding
// and hydration. EXPLAIN diagnoses SQL work; this uses normal auto planning.
func benchmarkSearchRequestPlanning(b *testing.B, db *housekeepingDatabase, reader identity.Actor) {
	b.Helper()
	for _, mode := range []pgx.QueryExecMode{pgx.QueryExecModeCacheStatement, pgx.QueryExecModeExec} {
		b.Run("local-request/"+mode.String(), func(b *testing.B) {
			ctx := searchDiagnosticReader(b, reader)
			request := catalog.CatalogSearchRepositoryRequest{Query: catalog.SearchQuery{Text: "Performance", Kinds: []catalog.Kind{catalog.KindSkill}}, Limit: 20}
			read := func() {
				b.Helper()
				page, err := withTenantTx(ctx, db.api, housekeepingTenantA, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) (catalog.CatalogSearchRepositoryPage, error) {
					if err := initializeCollectionPolicy(ctx, tx, housekeepingTenantA); err != nil {
						return catalog.CatalogSearchRepositoryPage{}, err
					}
					return searchCatalogTx(ctx, searchPlanningTx{Tx: tx, mode: mode}, housekeepingTenantA, request)
				})
				if err != nil || page.Total != 1000 || len(page.Items) != 20 || !page.HasMore {
					b.Fatalf("request planning changed results: total=%d page=%d error=%v", page.Total, len(page.Items), err)
				}
			}
			for range 6 {
				read()
			}
			b.ResetTimer()
			for range b.N {
				for range 20 {
					read()
				}
			}
			b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(b.N*20), "ns/search")
		})
	}
}

// Reuse the governed source fixture to expose per-observation scope and
// ranking work. This remains a fixed diagnostic, not an arbitrary SQL runner.
func benchmarkIndexedSearchPlans(b *testing.B, db *housekeepingDatabase, repository *PostgresRepository, reader identity.Actor) {
	b.Helper()
	documents := make([]catalog.SearchDocument, 10000)
	for i := range documents {
		documents[i] = catalog.SearchDocument{Identifier: fmt.Sprintf("urn:air:remote.example:finance:entry-%05d", i), DisplayName: fmt.Sprintf("Performance %05d", i), Description: "commonterm", Tags: []string{"performance"}, RepresentativeQueries: []string{"Read commonterm metadata"}}
	}
	seedLexicalSource(b, db, repository, documents)
	for _, table := range []string{"observations", "federation_sources", "federation_source_revisions"} {
		if _, err := db.operator.Exec(b.Context(), "ANALYZE "+pgx.Identifier{table}.Sanitize()); err != nil {
			b.Fatal(err)
		}
	}
	for _, text := range []string{"", "Performance", "missingterm"} {
		for _, mode := range []string{"force_custom_plan", "force_generic_plan"} {
			for _, page := range []bool{false, true} {
				for _, disableJIT := range []bool{false, true} {
					suffix := ""
					if disableJIT {
						suffix = "/jit-off"
					}
					b.Run(fmt.Sprintf("indexed=10000/text=%s/%s/page=%t%s", text, mode, page, suffix), func(b *testing.B) {
						ctx, cancel := context.WithTimeout(searchDiagnosticReader(b, reader), 10*time.Second)
						defer cancel()
						_, err := withSearchDiagnosticTx(ctx, db.api, func(tx pgx.Tx) (int, error) {
							if err := initializeCollectionPolicy(ctx, tx, housekeepingTenantA); err != nil {
								return 0, err
							}
							if _, err := tx.Exec(ctx, "SET LOCAL plan_cache_mode="+mode); err != nil {
								return 0, err
							}
							if disableJIT {
								if _, err := tx.Exec(ctx, indexedSearchPlanningSQL); err != nil {
									return 0, err
								}
							}
							sql := indexedCatalogSearchRecordsCTE + "SELECT count(*) FROM selected_identities"
							args := scalePlanArguments(housekeepingTenantA, "indexed", text, "NULL")
							if page {
								sql = indexedCatalogSearchRecordsCTE + indexedCatalogSearchPageSQL
								args = append(args, "false", "0", "''", "''", "21")
							}
							const name = "indexed_search_plan_diagnostic"
							raw, err := explainSearchDiagnostic(ctx, tx, name, sql, args)
							if err != nil {
								return 0, err
							}
							var plans []struct {
								Plan      searchPlanNode `json:"Plan"`
								Execution float64        `json:"Execution Time"`
								JIT       struct {
									Functions int `json:"Functions"`
								} `json:"JIT"`
							}
							if json.Unmarshal(raw, &plans) != nil || len(plans) != 1 {
								return 0, fmt.Errorf("invalid indexed plan")
							}
							if page {
								expected := 21
								if text == "missingterm" {
									expected = 0
								}
								if plans[0].Plan.Rows != float64(expected) {
									return 0, fmt.Errorf("indexed plan population differs: got %.0f want %d", plans[0].Plan.Rows, expected)
								}
							}
							b.Logf("execution_ms=%.3f hits=%d reads=%d temp_written=%d jit_functions=%d", plans[0].Execution, plans[0].Plan.Hits, plans[0].Plan.Reads, plans[0].Plan.Temp, plans[0].JIT.Functions)
							return 0, nil
						})
						if err != nil {
							b.Fatal(err)
						}
					})
				}
			}
		}
	}
}
