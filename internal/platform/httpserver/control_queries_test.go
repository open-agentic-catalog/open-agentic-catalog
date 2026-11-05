package httpserver

import (
	"context"
	"encoding/json"
	"github.com/open-agentic-catalog/open-agentic-catalog/test/identityfixture"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/audit"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/observability"
)

type controlQueryRepository struct {
	catalog.Repository
	search  func(context.Context, string, catalog.CatalogSearchRepositoryRequest) (catalog.CatalogSearchRepositoryPage, error)
	explore func(context.Context, string, catalog.SearchQuery, []catalog.FacetRequest) (catalog.ExploreResponse, error)
	export  func(context.Context, string, catalog.AuditExportRequest) (catalog.AuditExport, error)
}

func (r controlQueryRepository) SearchCatalog(c context.Context, t string, p catalog.CatalogSearchRepositoryRequest) (catalog.CatalogSearchRepositoryPage, error) {
	return r.search(c, t, p)
}
func (r controlQueryRepository) ExploreCatalog(c context.Context, t string, q catalog.SearchQuery, f []catalog.FacetRequest) (catalog.ExploreResponse, error) {
	return r.explore(c, t, q, f)
}
func (r controlQueryRepository) ExportAuditEvents(c context.Context, t string, q catalog.AuditExportRequest) (catalog.AuditExport, error) {
	return r.export(c, t, q)
}

func TestControlSearchInputMapping(t *testing.T) {
	t.Parallel()
	for _, limit := range []int{-1, 0, 1, 100, 101, 20} {
		t.Run(stringInt64(int64(limit)), func(t *testing.T) {
			// -1 denotes omission, distinct from explicit zero.
			var selected *int
			if limit != -1 {
				selected = &limit
			}
			order := catalog.SearchOrderPublisherDesc
			input := catalogSearchRequest{Order: &order, Text: "query", Kinds: []catalog.Kind{catalog.KindAgent}, Tags: []string{"tag"}, License: "Apache-2.0", Categories: []string{"tools"}, Publisher: "publisher", Visibility: catalog.VisibilityPublic, Limit: selected, Cursor: "opaque"}
			query, err := input.searchQuery()
			facetInput := catalogExploreRequest{Query: catalogExploreQuery{Text: input.Text, Kinds: input.Kinds, Tags: input.Tags, License: input.License, Categories: input.Categories, Publisher: input.Publisher, Visibility: input.Visibility}, Facets: []catalogFacetRequest{{Field: "kind", Limit: selected, MinCount: 2}}}
			explore, facetErr := facetInput.exploreRequest()
			if limit == 0 || limit == 101 {
				if catalog.ErrorCodeOf(err) != catalog.CodeInvalidArgument || catalog.ErrorCodeOf(facetErr) != catalog.CodeInvalidArgument {
					t.Fatalf("limits accepted: %v %v", err, facetErr)
				}
				return
			}
			wantLimit := limit
			if limit == -1 {
				wantLimit = 0
			}
			want := catalog.SearchQuery{Order: order, Text: "query", Kinds: []catalog.Kind{catalog.KindAgent}, Tags: []string{"tag"}, License: "Apache-2.0", Categories: []string{"tools"}, Publisher: "publisher", Visibility: catalog.VisibilityPublic, Limit: wantLimit, Cursor: "opaque"}
			if err != nil || !reflect.DeepEqual(query, want) {
				t.Fatalf("search=%+v err=%v", query, err)
			}
			want.Limit = 0
			want.Order = ""
			want.Cursor = ""
			if facetErr != nil || !reflect.DeepEqual(explore, catalog.ExploreRequest{Query: want, Facets: []catalog.FacetRequest{{Field: "kind", Limit: wantLimit, MinCount: 2}}}) {
				t.Fatalf("explore=%+v err=%v", explore, facetErr)
			}
		})
	}
}

func TestControlSearchProjection(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"browse", "search", "explore"} {
		for _, outcome := range []string{"success", "unavailable", "invalid-limit", "invalid-query", "partial", "invalid-empty-kind", "invalid-empty-visibility", "invalid-nul-text", "invalid-nul-publisher", "invalid-nul-tag"} {
			if (outcome == "partial" && kind != "search") || (strings.HasPrefix(outcome, "invalid-empty-") && kind != "browse") || (strings.HasPrefix(outcome, "invalid-nul-") && kind != "search") {
				continue
			}
			t.Run(kind+"/"+outcome, func(t *testing.T) {
				observation := &requestOperationObservation{}
				ctx := identityfixture.WithActor(context.WithValue(t.Context(), requestOperationKey{}, observation), projectionActor(identity.RoleReader))
				calls, external := 0, 0
				query := catalog.SearchQuery{Tags: []string{}, Kinds: []catalog.Kind{catalog.KindAgent}, Visibility: catalog.VisibilityPublic, Limit: 2}
				if kind != "browse" {
					query.Text = "agent"
					query.Tags = []string{"tag"}
					query.Publisher = "publisher"
				}
				check := func(c context.Context, tenant string) error {
					calls++
					actor, ok := identity.FromContext(c)
					if !ok || actor.TenantID != projectionTenant || tenant != projectionTenant {
						t.Fatal("query lost tenant")
					}
					if outcome == "unavailable" {
						return catalog.E(catalog.CodeDependencyUnavailable, "private-fixture-query")
					}
					return nil
				}
				record := controlReadFixture(t).record
				facetResponse := catalog.ExploreResponse{Matched: 3, Facets: map[string]catalog.FacetResult{"kind": {Buckets: []catalog.FacetBucket{{Value: "agent", Count: 3}}}}}
				repo := controlQueryRepository{
					search: func(c context.Context, tenant string, p catalog.CatalogSearchRepositoryRequest) (catalog.CatalogSearchRepositoryPage, error) {
						wantLimit := 2
						if kind == "search" {
							wantLimit++
						}
						if !reflect.DeepEqual(p, catalog.CatalogSearchRepositoryRequest{Query: query, Limit: wantLimit}) {
							t.Fatalf("search request=%+v want query=%+v", p, query)
						}
						err := check(c, tenant)
						return catalog.CatalogSearchRepositoryPage{Items: []catalog.CatalogSummary{record.Summary()}, Total: 1, Generation: "1"}, err
					},
					explore: func(c context.Context, tenant string, q catalog.SearchQuery, facets []catalog.FacetRequest) (catalog.ExploreResponse, error) {
						want := query
						want.Limit = 0
						if !reflect.DeepEqual(q, want) || !reflect.DeepEqual(facets, []catalog.FacetRequest{{Field: "kind", Limit: 2, MinCount: 1}}) {
							t.Fatalf("explore query=%+v facets=%+v", q, facets)
						}
						return facetResponse, check(c, tenant)
					},
				}
				s := projectionServer(projectionCatalog(t, repo, nil))
				search, err := catalog.NewSearchService(s.catalog, indexedProjectionSearch(func(c context.Context, p catalog.FederatedCatalogSearchRequest) (catalog.FederatedCatalogSearchPage, error) {
					external++
					actor, ok := identity.FromContext(c)
					if !ok || actor.TenantID != projectionTenant || !reflect.DeepEqual(p, catalog.FederatedCatalogSearchRequest{Query: query, Limit: 3, IncludeLive: true, LiveLimit: catalog.LiveSearchWindow}) {
						t.Fatalf("external request=%+v", p)
					}
					return catalog.FederatedCatalogSearchPage{Generation: "1", Partial: outcome == "partial"}, nil
				}))
				if err != nil {
					t.Fatal(err)
				}
				s.catalogSearch = search
				body := `{"text":"agent","kinds":["agent"],"tags":["tag"],"publisher":"publisher","visibility":"public","limit":2}`
				target := "/?kind=agent&visibility=public&limit=2"
				method := http.MethodPost
				handler := s.handleCatalogSearch
				if kind == "browse" {
					method = http.MethodGet
					body = ""
					handler = s.handleCatalogBrowse
				}
				if kind == "explore" {
					handler = s.handleCatalogExplore
					body = `{"query":{"text":"agent","kinds":["agent"],"tags":["tag"],"publisher":"publisher","visibility":"public"},"facets":[{"field":"kind","limit":2,"minCount":1}]}`
				}
				if outcome == "invalid-limit" {
					target = "/?limit=0"
					body = strings.ReplaceAll(body, `"limit":2`, `"limit":0`)
				}
				if outcome == "invalid-query" {
					target = "/?kind=unknown"
					body = strings.ReplaceAll(body, `["agent"]`, `["unknown"]`)
				}
				if outcome == "invalid-nul-text" {
					body = strings.ReplaceAll(body, `"text":"agent"`, `"text":"\u0000"`)
				}
				if outcome == "invalid-nul-publisher" {
					body = strings.ReplaceAll(body, `"publisher":"publisher"`, `"publisher":"\u0000"`)
				}
				if outcome == "invalid-nul-tag" {
					body = strings.ReplaceAll(body, `"tags":["tag"]`, `"tags":["\u0000"]`)
				}
				if outcome == "invalid-empty-kind" {
					target = "/?kind=&visibility=public&limit=2"
				}
				if outcome == "invalid-empty-visibility" {
					target = "/?kind=agent&visibility=&limit=2"
				}
				r := httptest.NewRequestWithContext(ctx, method, target, strings.NewReader(body))
				r.Header.Set("Content-Type", "application/json")
				w := httptest.NewRecorder()
				handler(w, r)
				if strings.HasPrefix(outcome, "invalid-") {
					requireProjectionProblem(t, w, 400, catalog.CodeInvalidArgument)
					if calls != 0 || external != 0 {
						t.Fatal("invalid input reached search")
					}
					return
				}
				if outcome == "unavailable" {
					requireProjectionProblem(t, w, 503, catalog.CodeDependencyUnavailable)
					if calls != 1 || external != 0 {
						t.Fatal("failed local query reached external search")
					}
					return
				}
				switch kind {
				case "browse":
					requireControlJSON(t, w, 200, catalog.Page[catalog.CatalogSummary]{Items: []catalog.CatalogSummary{record.Summary()}, Total: 1})
				case "explore":
					requireControlJSON(t, w, 200, facetResponse)
				case "search":
					var got catalog.CatalogSearchResponse
					if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
						t.Fatal(err)
					}
					wantOutcome := observability.OutcomeSucceeded
					if outcome == "partial" {
						wantOutcome = observability.OutcomePartial
					}
					if w.Code != 200 || len(got.Items) != 1 || got.Items[0].Local == nil || !equalPublicSummary(*got.Items[0].Local, record.Summary().SearchLocal()) || got.DurableTotal != 1 || got.Partial != (outcome == "partial") || observation.operation != observability.OperationCatalogSearch || observation.outcome != wantOutcome || external != 1 {
						t.Fatalf("search=%+v observation=%+v external=%d", got, observation, external)
					}
				}
				if calls != 1 {
					t.Fatalf("calls=%d", calls)
				}
			})
		}
	}
}

func TestControlAuditFilterAdmission(t *testing.T) {
	t.Parallel()
	valid := url.Values{"actor": {"reader"}, "action": {"namespace.created"}, "requestId": {"request-1"}, "targetType": {"namespace"}, "targetId": {projectionItem}, "occurredFrom": {"2026-09-20T12:00:00.123456789+02:00"}, "occurredBefore": {"2026-09-20T11:00:00Z"}}
	r := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/?"+valid.Encode(), nil)
	got, binding, err := parseAuditFilters(r)
	if err != nil {
		t.Fatal(err)
	}
	from := time.Date(2026, 9, 20, 10, 0, 0, 123456000, time.UTC)
	before := projectionTime.Add(-time.Hour)
	want := audit.Query{Actor: "reader", Action: "namespace.created", RequestID: "request-1", TargetType: "namespace", TargetID: projectionItem, OccurredFrom: &from, OccurredBefore: &before}
	wantBinding := map[string]string{"actor": "reader", "action": "namespace.created", "requestId": "request-1", "targetType": "namespace", "targetId": projectionItem, "occurredFrom": "2026-09-20T10:00:00.123456Z", "occurredBefore": "2026-09-20T11:00:00Z"}
	if !reflect.DeepEqual(got, want) || !reflect.DeepEqual(binding, wantBinding) {
		t.Fatalf("filters=%+v binding=%v", got, binding)
	}
	bad := []string{"%zz", "occurredFrom=bad", "occurredBefore=bad", "targetId=orphan", "action=INVALID", "actor=", "action=", "requestId=", "targetType=", "targetId=", "occurredFrom=", "occurredBefore="}
	for _, query := range bad {
		t.Run(query, func(t *testing.T) {
			w := httptest.NewRecorder()
			projectionServer(nil).handleAuditEvents(w, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/?"+query, nil))
			requireProjectionProblem(t, w, 400, catalog.CodeInvalidArgument)
		})
	}
}

func TestControlAuditExportProjection(t *testing.T) {
	t.Parallel()
	for _, outcome := range []string{"all-filters", "omitted-filters", "unavailable", "missing-range", "null-actor"} {
		t.Run(outcome, func(t *testing.T) {
			actor := projectionActor(identity.RoleSecurityReviewer)
			ctx := identityfixture.WithActor(t.Context(), actor)
			from, before := projectionTime.Add(-time.Hour), projectionTime
			filters := audit.Query{OccurredFrom: &from, OccurredBefore: &before}
			body := map[string]any{"occurredFrom": from, "occurredBefore": before, "reason": " review records "}
			if outcome != "omitted-filters" {
				filters.Actor = "reader"
				filters.Action = "namespace.created"
				filters.RequestID = "request-1"
				filters.TargetType = "namespace"
				filters.TargetID = projectionItem
				body["actor"] = "reader"
				body["action"] = "namespace.created"
				body["requestId"] = "request-1"
				body["targetType"] = "namespace"
				body["targetId"] = projectionItem
			}
			if outcome == "missing-range" {
				delete(body, "occurredFrom")
			}
			if outcome == "null-actor" {
				body["actor"] = nil
			}
			want, _, err := audit.BuildExport(projectionTenant, filters, before, before, []audit.Record{})
			if err != nil {
				t.Fatal(err)
			}
			calls := 0
			repo := controlQueryRepository{export: func(c context.Context, tenant string, p catalog.AuditExportRequest) (catalog.AuditExport, error) {
				calls++
				if !sameProjectionContext(c, ctx) || tenant != projectionTenant || !reflect.DeepEqual(p.Filters, filters) || !p.Cutoff.Equal(before) || !p.GeneratedAt.Equal(before) || p.Access.Action != audit.ActionAuditExported || p.Access.Actor != actor.MembershipID || p.Access.Reason != "review records" {
					t.Fatalf("export request=%+v", p)
				}
				if err := audit.ValidateNewRecord(p.Access); err != nil {
					t.Fatal(err)
				}
				if outcome == "unavailable" {
					return catalog.AuditExport{}, catalog.E(catalog.CodeDependencyUnavailable, "private-fixture-export")
				}
				return want, nil
			}}
			raw, err := json.Marshal(body)
			if err != nil {
				t.Fatal(err)
			}
			r := httptest.NewRequestWithContext(ctx, http.MethodPost, "/", strings.NewReader(string(raw)))
			r.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			projectionServer(projectionCatalog(t, repo, nil)).handleAuditExport(w, r)
			if outcome == "missing-range" || outcome == "null-actor" {
				requireProjectionProblem(t, w, 400, catalog.CodeInvalidArgument)
				if calls != 0 {
					t.Fatal("invalid export input reached storage")
				}
				return
			}
			if calls != 1 {
				t.Fatalf("calls=%d", calls)
			}
			if outcome == "unavailable" {
				requireProjectionProblem(t, w, 503, catalog.CodeDependencyUnavailable)
				if w.Header().Get("Content-Disposition") != "" {
					t.Fatal("failed export has attachment header")
				}
				return
			}
			requireControlJSON(t, w, 200, want)
			if w.Header().Get("Content-Disposition") != `attachment; filename="open-agentic-catalog-audit-export.json"` {
				t.Fatal("missing export attachment")
			}
		})
	}
	for _, field := range []string{"actor", "action", "requestId", "targetType", "targetId"} {
		for _, value := range []string{`null`, `0`, `""`} {
			t.Run(field+"/"+value, func(t *testing.T) {
				r := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/", strings.NewReader(`{"`+field+`":`+value+`}`))
				r.Header.Set("Content-Type", "application/json")
				w := httptest.NewRecorder()
				projectionServer(nil).handleAuditExport(w, r)
				requireProjectionProblem(t, w, 400, catalog.CodeInvalidArgument)
			})
		}
	}
}

func equalPublicSummary(a, b catalog.CatalogSearchLocal) bool {
	left, err := json.Marshal(a)
	if err != nil {
		return false
	}
	right, err := json.Marshal(b)
	return err == nil && string(left) == string(right)
}

func TestControlNamespaceFilterMapping(t *testing.T) {
	for _, selected := range []*string{nil, new("00000000-0000-7000-8000-000000000001"), new(""), new("foreign")} {
		query, err := (catalogSearchRequest{NamespaceID: selected}).searchQuery()
		explore, exploreErr := (catalogExploreRequest{Query: catalogExploreQuery{NamespaceID: selected}, Facets: []catalogFacetRequest{{Field: "namespace"}}}).exploreRequest()
		invalid := selected != nil && (*selected == "" || *selected == "foreign")
		if invalid {
			if catalog.ErrorCodeOf(err) != catalog.CodeInvalidArgument || catalog.ErrorCodeOf(exploreErr) != catalog.CodeInvalidArgument {
				t.Fatal("invalid namespace accepted")
			}
			continue
		}
		if err != nil || exploreErr != nil || query.NamespaceID != explore.Query.NamespaceID || selected != nil && query.NamespaceID != *selected {
			t.Fatal("namespace selector lost")
		}
	}
}

func TestControlListingFilterAdmission(t *testing.T) {
	for _, endpoint := range []string{"search", "explore"} {
		for _, filters := range []string{
			`"license":" MIT ","categories":[" TOOLS "]`, `"license":null`, `"categories":null`,
			`"license":"MIT","namespaceId":"00000000-0000-7000-8000-000000000001","origins":["indexed"]`,
			`"categories":[""]`, `"categories":["x\u0000"]`, `"license":"x\u0000"`, `"license":"MIT","License":"other"`,
			`"license":"` + strings.Repeat("🙂", 256) + ` "`,
			`"categories":["` + strings.Repeat("🙂", 80) + ` "]`,
			`"categories":[` + strings.Repeat(`"tools",`, 16) + `"tools"]`,
			`"license":42`, `"categories":"tools"`, `"categories":[true]`,
			`"categories":["tools"],"Categories":["finance"]`, `"license":"MIT","license":"MIT"`,
		} {
			t.Run(endpoint+"/"+filters, func(t *testing.T) {
				calls := 0
				want := catalog.SearchQuery{Origins: []catalog.SearchResultType{catalog.SearchResultLocal}, License: "MIT", Categories: []string{"tools"}, Kinds: []catalog.Kind{}, Tags: []string{}}
				repo := controlQueryRepository{
					search: func(_ context.Context, tenant string, request catalog.CatalogSearchRepositoryRequest) (catalog.CatalogSearchRepositoryPage, error) {
						calls++
						if tenant != projectionTenant || request.Query.License != want.License || !reflect.DeepEqual(request.Query.Categories, want.Categories) || request.Query.IncludesOrigin(catalog.SearchResultIndexed) || request.Query.IncludesOrigin(catalog.SearchResultLive) {
							t.Fatalf("incorrect local selection: %+v", request.Query)
						}
						return catalog.CatalogSearchRepositoryPage{Items: []catalog.CatalogSummary{}, Generation: "1"}, nil
					},
					explore: func(_ context.Context, tenant string, query catalog.SearchQuery, _ []catalog.FacetRequest) (catalog.ExploreResponse, error) {
						calls++
						if tenant != projectionTenant || !reflect.DeepEqual(query, want) {
							t.Fatalf("incorrect facet selection: %+v", query)
						}
						return catalog.ExploreResponse{Facets: map[string]catalog.FacetResult{"category": {Buckets: []catalog.FacetBucket{}}}}, nil
					},
				}
				server := projectionServer(projectionCatalog(t, repo, nil))
				search, err := catalog.NewSearchService(server.catalog, indexedProjectionSearch(func(context.Context, catalog.FederatedCatalogSearchRequest) (catalog.FederatedCatalogSearchPage, error) {
					t.Fatal("local Listing filters reached external selection")
					return catalog.FederatedCatalogSearchPage{}, nil
				}))
				if err != nil {
					t.Fatal(err)
				}
				server.catalogSearch = search
				body := "{" + filters + "}"
				if strings.HasPrefix(filters, `"license":" MIT "`) {
					body = "{" + filters + `,"origins":["local"]}`
				}
				handler := server.handleCatalogSearch
				if endpoint == "explore" {
					want.Origins = nil
					body = `{"query":{` + filters + `},"facets":[{"field":"category"}]}`
					handler = server.handleCatalogExplore
				}
				request := httptest.NewRequestWithContext(identityfixture.WithActor(t.Context(), projectionActor(identity.RoleReader)), http.MethodPost, "/", strings.NewReader(body))
				request.Header.Set("Content-Type", "application/json")
				response := httptest.NewRecorder()
				handler(response, request)
				if strings.HasPrefix(filters, `"license":" MIT "`) {
					if response.Code != 200 || calls != 1 {
						t.Fatalf("valid filter status=%d calls=%d", response.Code, calls)
					}
				} else {
					requireProjectionProblem(t, response, 400, catalog.CodeInvalidArgument)
					if calls != 0 {
						t.Fatal("invalid filter reached repository")
					}
				}
			})
		}
	}
}

func TestControlDeclarationFiltersAcrossOrigins(t *testing.T) {
	for _, origins := range []string{"", `,"origins":["indexed"]`, `,"origins":["local","indexed","live"]`} {
		t.Run(origins, func(t *testing.T) {
			calls := 0
			repo := controlQueryRepository{search: func(_ context.Context, tenant string, request catalog.CatalogSearchRepositoryRequest) (catalog.CatalogSearchRepositoryPage, error) {
				if tenant != projectionTenant || request.Query.License != "MIT" || !reflect.DeepEqual(request.Query.Categories, []string{"café", "tools"}) {
					t.Fatal("local predicate lost", request)
				}
				return catalog.CatalogSearchRepositoryPage{Generation: "1"}, nil
			}}
			server := projectionServer(projectionCatalog(t, repo, nil))
			search, err := catalog.NewSearchService(server.catalog, indexedProjectionSearch(func(_ context.Context, request catalog.FederatedCatalogSearchRequest) (catalog.FederatedCatalogSearchPage, error) {
				calls++
				if request.Query.License != "MIT" || !reflect.DeepEqual(request.Query.Categories, []string{"café", "tools"}) || !request.Query.IncludesOrigin(catalog.SearchResultIndexed) || request.IncludeLive != (origins != `,"origins":["indexed"]`) {
					t.Fatal("federated predicate or origin lost", request)
				}
				return catalog.FederatedCatalogSearchPage{Generation: "1", Partial: true, Diagnostics: []catalog.CatalogSearchDiagnostic{{Status: "insufficient_evidence"}}}, nil
			}))
			if err != nil {
				t.Fatal(err)
			}
			server.catalogSearch = search
			request := httptest.NewRequestWithContext(identityfixture.WithActor(t.Context(), projectionActor(identity.RoleReader)), http.MethodPost, "/", strings.NewReader(`{"text":"agent","license":" MIT ","categories":[" TOOLS ","CAFÉ","tools"]`+origins+`}`))
			request.Header.Set("Content-Type", "application/json")
			response := httptest.NewRecorder()
			server.handleCatalogSearch(response, request)
			if response.Code != 200 || calls != 1 || !strings.Contains(response.Body.String(), `"insufficient_evidence"`) {
				t.Fatalf("status=%d calls=%d body=%s", response.Code, calls, response.Body.String())
			}
		})
	}
}

func TestControlSearchRejectsExplicitEmptyOrder(t *testing.T) {
	empty := catalog.SearchOrder("")
	if _, err := (catalogSearchRequest{Order: &empty}).searchQuery(); catalog.ErrorCodeOf(err) != catalog.CodeInvalidArgument {
		t.Fatal("explicit empty order", err)
	}
}
