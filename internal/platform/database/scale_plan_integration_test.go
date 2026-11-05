//go:build integration

package database

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/authn"
)

type scalePlanSample struct {
	Repository string        `json:"repository"`
	Case       string        `json:"case"`
	Mode       string        `json:"mode"`
	Statement  string        `json:"statement"`
	Status     string        `json:"status"`
	Planning   float64       `json:"planningMillis"`
	Execution  float64       `json:"executionMillis"`
	Plan       scalePlanNode `json:"plan"`
}

// TestPostgresScalePlans is an opt-in diagnostic on the performance runner's
// already seeded database, not another fixture builder. The runner supplies
// only its owned loopback port and invokes this after HTTP readers have stopped.
// Each plan uses the exact API role, tenant context and a read-only transaction.
func TestPostgresScalePlans(t *testing.T) {
	rawPort := os.Getenv("CATALOG_SCALE_PLAN_PORT")
	if rawPort == "" {
		t.Skip("owned scale runner only")
	}
	failureStage := "connection"
	t.Cleanup(func() {
		if t.Failed() {
			// The supervisor accepts only these fixed stages, never test output.
			fmt.Printf("scale-plan-failure: %s\n", failureStage)
		}
	})
	port, err := strconv.Atoi(rawPort)
	if err != nil || port < 1 || port > 65535 || strconv.Itoa(port) != rawPort {
		t.Fatal("invalid owned scale port")
	}
	configuration, err := pgxpool.ParseConfig(fmt.Sprintf("postgres://catalog_api:catalog-api@127.0.0.1:%d/catalog?sslmode=disable", port))
	if err != nil {
		t.Fatal("invalid owned scale connection")
	}
	configuration.MaxConns = 1
	configuration.ConnConfig.ConnectTimeout = 5 * time.Second
	configuration.ConnConfig.RuntimeParams["default_transaction_read_only"] = "on"
	configuration.ConnConfig.RuntimeParams["statement_timeout"] = "5000"
	pool, err := pgxpool.NewWithConfig(t.Context(), configuration)
	if err != nil {
		t.Fatal("owned scale connection unavailable")
	}
	defer pool.Close()
	failureStage = "repository"
	repository, err := NewPostgresRepository(pool)
	if err != nil {
		t.Fatal("owned scale repository unavailable")
	}
	authority, err := NewPostgresIdentityStore(repository)
	if err != nil {
		t.Fatal("owned scale identity store unavailable")
	}
	failureStage = "reader"
	actor, err := authority.ResolvePrincipal(t.Context(), identity.Principal{TenantID: "00000000-0000-7000-8000-000000000001", Issuer: authn.DevelopmentIssuer, Subject: "local-reader"})
	if err != nil {
		t.Fatal("owned scale reader unavailable")
	}
	failureStage = "policy"
	readerContext, _, err := identity.WithCollectionContext(identity.WithActor(t.Context(), actor))
	if err != nil {
		t.Fatal("owned scale collection policy unavailable")
	}

	continuation, continuationErr := scalePlanContinuation(readerContext, pool)
	results := make([]scalePlanSample, 0, 86)
	for _, repository := range []string{"local", "indexed"} {
		queries := []struct{ name, text string }{
			{"all", ""}, {"common", "commonterm"}, {"rare", "rareterm"},
			{"absent", "scaleabsentterm"}, {"exact-local", "urn:air:scale.example:scale:local-000000"},
			{"kind", "Scale"},
		}
		if repository == "local" {
			queries = append(queries, struct{ name, text string }{"local-first", "Scale Local"}, struct{ name, text string }{"local-third", "Scale Local"})
			queries = append(queries, struct{ name, text string }{"facets", "Scale"})
			for _, name := range []string{"publisher-asc-first", "publisher-asc-third", "publisher-desc-first", "publisher-desc-third"} {
				queries = append(queries, struct{ name, text string }{name, "Scale Local"})
			}
		}
		for _, query := range queries {
			search := catalog.SearchQuery{Text: query.text}
			if strings.HasPrefix(query.name, "publisher-asc-") {
				search.Order = catalog.SearchOrderPublisherAsc
			}
			if strings.HasPrefix(query.name, "publisher-desc-") {
				search.Order = catalog.SearchOrderPublisherDesc
			}
			var orderedContinuation []string
			var orderedErr error
			if strings.HasSuffix(query.name, "-third") {
				orderedContinuation, orderedErr = scalePlanContinuation(readerContext, pool, search.Order)
			}
			kinds := "NULL"
			if query.name == "kind" {
				search.Kinds = []catalog.Kind{catalog.KindSkill}
				kinds = "ARRAY['skill']"
			}
			if repository == "indexed" && query.name == "exact-local" {
				query.name, query.text = "exact-indexed", "urn:air:scale.example:scale:indexed-000000"
				search.Text = query.text
			}
			for _, mode := range []string{"force_custom_plan", "force_generic_plan"} {
				for _, page := range []bool{false, true} {
					if (query.name == "facets" || search.Order != "") && !page {
						continue // Explore includes its exact total in the one statement.
					}
					value := scalePlanSample{Repository: repository, Case: query.name, Mode: mode, Statement: "count", Status: "measured"}
					if page {
						value.Statement = "page"
					}
					if query.name == "facets" {
						value.Statement = "facets"
					}
					ctx, cancel := context.WithTimeout(readerContext, 6*time.Second)
					_, err := withTenantTx(ctx, pool, "00000000-0000-7000-8000-000000000001", pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) (int, error) {
						if err := initializeCollectionPolicy(ctx, tx, actor.TenantID); err != nil {
							return 0, err
						}
						if orderedErr != nil {
							return 0, orderedErr
						}
						if query.name == "local-third" && continuationErr != nil {
							return 0, continuationErr
						}
						if _, err := tx.Exec(ctx, "SET LOCAL plan_cache_mode="+mode); err != nil {
							return 0, err
						}
						if repository == "indexed" {
							if _, err := tx.Exec(ctx, indexedSearchPlanningSQL); err != nil {
								return 0, err
							}
						}
						recordsSQL, pageSQL := catalogSearchStatements(search)
						sql := recordsSQL + "SELECT count(*) FROM matched"
						if repository == "indexed" {
							sql = indexedCatalogSearchRecordsCTE + "SELECT count(*) FROM selected_identities"
							pageSQL = indexedCatalogSearchRecordsCTE + indexedCatalogSearchPageSQL
						}
						if search.Order != "" {
							pageSQL = localColumnOrderSQL(pageSQL, search.Order)
						}
						if page {
							sql = pageSQL
						}
						if query.name == "facets" {
							var err error
							// Exactly the HTTP scale case's requested fields and limits.
							sql, _, err = catalogFacetStatement(search, []catalog.FacetRequest{{Field: "kind", Limit: 2}, {Field: "tag", Limit: 2}})
							if err != nil {
								return 0, err
							}
						}
						name := fmt.Sprintf("scale_plan_%d", len(results))
						failureStage = "preparation"
						prepared, err := tx.Conn().Prepare(ctx, name, sql)
						if err != nil {
							t.Fatal("scale plan preparation failed")
						}
						// These literals are the closed synthetic cases above, never
						// user/operator SQL or query text. EXECUTE preserves plan mode.
						args := scalePlanArguments(actor.TenantID, repository, query.text, kinds)
						if query.name == "facets" {
							args = append(args, "1", "2", "1", "2")
						} else if page {
							switch {
							case strings.HasSuffix(query.name, "-third") && search.Order != "":
								args = append(args, orderedContinuation...)
							case query.name == "local-third":
								args = append(args, continuation...)
							default:
								args = append(args, "false", "0", "''", "''", "21")
							}
						}
						failureStage = "arguments"
						if err := validateScalePlanArguments(len(prepared.ParamOIDs), args); err != nil {
							t.Fatal(err)
						}
						var raw []byte
						if err := tx.QueryRow(ctx, "EXPLAIN (ANALYZE, BUFFERS, TIMING OFF, FORMAT JSON) EXECUTE "+name+"("+strings.Join(args, ",")+")").Scan(&raw); err != nil {
							return 0, err
						}
						var plans []struct {
							Plan      scalePlanNode `json:"Plan"`
							Planning  float64       `json:"Planning Time"`
							Execution float64       `json:"Execution Time"`
						}
						if len(raw) > 1<<20 || json.Unmarshal(raw, &plans) != nil || len(plans) != 1 {
							return 0, fmt.Errorf("invalid bounded scale plan")
						}
						value.Plan, value.Planning, value.Execution = plans[0].Plan, plans[0].Planning, plans[0].Execution
						return 0, nil
					})
					cancel()
					if err != nil {
						// A failed or deadline-limited plan is useful evidence, not a
						// passing query. Never emit database causes or connection input.
						value.Status = "query-failed"
					}
					results = append(results, value)
				}
			}
		}
	}
	results = append(results, scaleOverviewPlans(t, pool, authority, &failureStage)...)
	// scalePlanNode drops expressions, parameters, SQL text and redundant zeros.
	failureStage = "report-json"
	raw, err := json.Marshal(results)
	if err != nil {
		t.Fatal("invalid bounded scale report")
	}
	if len(raw) > 512<<10 {
		failureStage = "report-output-limit"
		t.Fatal("invalid bounded scale report")
	}
	fmt.Printf("scale-plans: %s\n", raw)
}

// Traverse the same two 20-item pages as the HTTP continuation oracle. Keep the
// real score/sort/identity boundary, not an invented OFFSET or fixture UUID.
// The fixture is quiescent; the read-only snapshot also binds both traversals.
func scalePlanContinuation(ctx context.Context, pool *pgxpool.Pool, order ...catalog.SearchOrder) ([]string, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	return withTenantTx(ctx, pool, "00000000-0000-7000-8000-000000000001", pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) ([]string, error) {
		if err := initializeCollectionPolicy(ctx, tx, "00000000-0000-7000-8000-000000000001"); err != nil {
			return nil, err
		}
		request := catalog.CatalogSearchRepositoryRequest{Query: catalog.SearchQuery{Text: "Scale Local"}, Limit: 20}
		if len(order) != 0 {
			request.Query.Order = order[0]
		}
		for range 2 {
			page, err := searchCatalogTx(ctx, tx, "00000000-0000-7000-8000-000000000001", request)
			if err != nil {
				return nil, err
			}
			if len(page.Items) != 20 || !page.HasMore {
				return nil, fmt.Errorf("scale continuation fixture is incomplete")
			}
			last := page.Items[len(page.Items)-1]
			position := catalog.CatalogSearchResultPosition(catalog.CatalogSearchResult{ID: "local:" + last.Release.ID, DisplayName: last.ListingRevision.DisplayName, Score: last.RelevanceScore, Publisher: last.ListingRevision.PublisherID, Local: &catalog.CatalogSearchLocal{Display: last.Display}}, request.Query.Order)
			request.After, request.ExpectedGeneration = &position, page.Generation
		}
		// PostgreSQL quotes retained metadata; no raw string interpolation or
		// externally supplied SQL enters the EXECUTE diagnostic.
		var name, id, sortValue string
		if err := tx.QueryRow(ctx, "SELECT quote_literal($1::text),quote_literal($2::text),quote_literal($3::text)", request.After.DisplayName, request.After.ResultID, request.After.SortValue).Scan(&name, &id, &sortValue); err != nil {
			return nil, err
		}
		key := strconv.Itoa(request.After.Score)
		if request.Query.Order != "" {
			key = sortValue
		}
		return []string{"true", key, name, id, "21"}, nil
	})
}

// Measure the actual dashboard statements with the same role, policy and
// parameter binding. Sparse access includes grants beyond the first /me page.
func scaleOverviewPlans(t *testing.T, pool *pgxpool.Pool, authority *PostgresIdentityStore, failureStage *string) []scalePlanSample {
	t.Helper()
	tenant := "00000000-0000-7000-8000-000000000001"
	results := make([]scalePlanSample, 0, 20)
	for _, scope := range []string{"overview", "overview-sparse"} {
		subject := "local-admin"
		if scope == "overview-sparse" {
			subject = "scale-sparse"
		}
		*failureStage = "reader"
		actor, err := authority.ResolvePrincipal(t.Context(), identity.Principal{TenantID: tenant, Issuer: authn.DevelopmentIssuer, Subject: subject})
		if err != nil {
			t.Fatal("owned dashboard reader unavailable")
		}
		*failureStage = "policy"
		ctx, _, err := identity.WithCollectionContext(identity.WithActor(t.Context(), actor))
		if err != nil {
			t.Fatal("owned dashboard policy unavailable")
		}
		probes := []struct {
			name, sql string
			args      []string
		}{
			{"resources", resourceInventoryCountsSQL, []string{"10001"}},
			{"reviews-pending", reviewPendingTimesSQL, []string{"10001"}},
		}
		if scope == "overview" {
			probes = append(probes,
				struct {
					name, sql string
					args      []string
				}{"releases", releaseInventoryCountsSQL, []string{"10001", "'published'"}},
				struct {
					name, sql string
					args      []string
				}{"indexed", indexedInventoryCountsSQL(), []string{"10001"}},
				struct {
					name, sql string
					args      []string
				}{"reviews-oldest", reviewOldestPendingSQL, nil},
				struct {
					name, sql string
					args      []string
				}{"reviews-decisions", reviewDecisionTimesSQL, []string{"date_trunc('day',now())-interval '29 days'", "now()", "10001"}},
				struct {
					name, sql string
					args      []string
				}{"sources", sourceOverviewRowsSQL, []string{"10001"}},
				struct {
					name, sql string
					args      []string
				}{"source-problems", sourceOverviewProblemsSQL, nil},
			)
		}
		for _, probe := range probes {
			for _, mode := range []string{"force_custom_plan", "force_generic_plan"} {
				value := scalePlanSample{Repository: scope, Case: probe.name, Mode: mode, Statement: "summary", Status: "measured"}
				call, cancel := context.WithTimeout(ctx, 6*time.Second)
				_, err := withTenantTx(call, pool, tenant, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) (int, error) {
					if err := initializeCollectionPolicy(call, tx, tenant); err != nil {
						return 0, err
					}
					if _, err := tx.Exec(call, "SET LOCAL plan_cache_mode="+mode); err != nil {
						return 0, err
					}
					name := fmt.Sprintf("scale_overview_%d", len(results))
					*failureStage = "preparation"
					prepared, err := tx.Conn().Prepare(call, name, probe.sql)
					if err != nil {
						t.Fatal("dashboard plan preparation failed")
					}
					args := append([]string{"'" + tenant + "'"}, probe.args...)
					*failureStage = "arguments"
					if err := validateScalePlanArguments(len(prepared.ParamOIDs), args); err != nil {
						t.Fatal(err)
					}
					var raw []byte
					if err := tx.QueryRow(call, "EXPLAIN (ANALYZE, BUFFERS, TIMING OFF, FORMAT JSON) EXECUTE "+name+"("+strings.Join(args, ",")+")").Scan(&raw); err != nil {
						return 0, err
					}
					var plans []struct {
						Plan      scalePlanNode `json:"Plan"`
						Planning  float64       `json:"Planning Time"`
						Execution float64       `json:"Execution Time"`
					}
					if len(raw) > 1<<20 || json.Unmarshal(raw, &plans) != nil || len(plans) != 1 {
						return 0, fmt.Errorf("invalid bounded dashboard plan")
					}
					value.Plan, value.Planning, value.Execution = plans[0].Plan, plans[0].Planning, plans[0].Execution
					return 0, nil
				})
				cancel()
				if err != nil {
					value.Status = "query-failed"
				}
				results = append(results, value)
			}
		}
	}
	return results
}
