// Hybrid repository units own snapshot fencing and row projection; real SQL
// ranking and vector arithmetic remain qualified by the integration suite.
package database

import (
	"errors"
	"math"
	"reflect"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/federation"
)

func semanticSearchFixture(t *testing.T) (semanticRepositoryFixture, catalog.SemanticSearchRequest) {
	t.Helper()
	f := newSemanticRepositoryFixture(t, true)
	f.tx.queryRows = []pgx.Row{scriptedValueRow([]any{int64(7), false}), scriptedValueRow([]any{true}), scriptedValueRow([]any{true})}
	return f, catalog.SemanticSearchRequest{Generation: f.g, Vector: []float32{1, 0, 0}, Cutoff: 0.5, Query: catalog.SearchQuery{Text: "tools", Limit: 2, Kinds: []catalog.Kind{catalog.KindAgent}, Tags: []string{"tools"}, Publisher: "publisher", Visibility: catalog.VisibilityPublic}}
}

func semanticSearchRow(id, release string) []any {
	row := []any{5, new(id), new(release), new("urn:air:peer.example:agents:remote"), new(""), new(false), new("agent"), new("sha256:entry"), new("Tools"), new("Helpful tools"), []string{"tools"}, new("publisher"), new(int64(800)), new(80), new("tools"), true, false, 2, []string{"source"}, "source", int64(3), int64(7)}
	if release != "" {
		// Keep the selected local fields consistent with the retained rows
		// returned by selectedCatalogHydration.
		row[3], row[4], row[5] = new("urn:air:catalog.example.com:agents:example"), new("1.2.3"), new(true)
		row[8], row[9], row[14] = new("Example"), new("Description"), new("example")
		row[12], row[13] = new(int64(900)), new(90)
		row[15], row[17], row[18] = false, 0, []string{}
		row[19], row[20], row[21] = "", int64(0), int64(0)
		if release == "second-release" {
			row[12], row[13] = new(int64(700)), new(70)
		}
	}
	return row
}

func TestSemanticSearchProjectsAndHydratesOneSnapshot(t *testing.T) {
	for _, continuation := range []bool{false, true} {
		t.Run(map[bool]string{false: "first", true: "continuation"}[continuation], func(t *testing.T) {
			f, request := semanticSearchFixture(t)
			if continuation {
				request.After = &catalog.SemanticSearchPosition{Rank: 950, SortName: "before", ID: "local:before"}
				request.LocalOnly = true
			}
			selection := &identityScriptedRows{values: [][]any{semanticSearchRow("local:release", "release"), semanticSearchRow("indexed:remote", ""), semanticSearchRow("local:second-release", "second-release")}}
			if continuation {
				// The scripted selection obeys LocalOnly; SQL filtering itself is
				// exercised against PostgreSQL, not inferred from these rows.
				selection.values = [][]any{selection.values[0], selection.values[2]}
			}
			hydration := selectedCatalogHydration()
			ids := []string{"release", "second-release"}
			if !continuation {
				rows, ok := hydration[0].rows.(*identityScriptedRows)
				if !ok {
					t.Fatal("invalid summary rows fixture")
				}
				rows.values = rows.values[1:]
				ids = ids[:1]
			}
			results := append([]identityQueryResult{{rows: selection}}, hydration...)
			f.tx.queryResults = results
			page, err := f.store.Search(f.context(t), request)
			if err != nil || page.Total != 5 || len(page.Items) != 2 || page.HasMore == continuation || !page.PartialIndex {
				t.Fatalf("page=%+v error=%v", page, err)
			}
			local := page.Items[0]
			wantLocal := catalog.CatalogSearchResult{ID: "local:release", Type: catalog.SearchResultLocal, Identifier: "urn:air:catalog.example.com:agents:example", Version: "1.2.3", VersionPresent: true, Kind: catalog.KindAgent, DisplayName: "Example", Description: "Description", Tags: []string{"tools"}, Publisher: "publisher", Score: 90}
			if !reflect.DeepEqual(local.Result, wantLocal) || local.Local == nil || local.Local.Release.ID != "release" || local.Local.RelevanceScore != 90 || local.Position != (catalog.SemanticSearchPosition{Rank: 900, SortName: "example", ID: "local:release"}) {
				t.Fatalf("local projection=%+v", local)
			}
			want, _, _, _ := catalogRecordReadFixture()
			requireCatalogSummary(t, *local.Local, want)
			remote := page.Items[1]
			if !continuation {
				group, err := federation.ObservationGroupID("urn:air:peer.example:agents:remote", "", false)
				if err != nil {
					t.Fatal(err)
				}
				wantRemote := catalog.CatalogSearchResult{ID: "indexed:remote", Type: catalog.SearchResultIndexed, Identifier: "urn:air:peer.example:agents:remote", Kind: catalog.KindAgent, DisplayName: "Tools", Description: "Helpful tools", Tags: []string{"tools"}, Publisher: "publisher", Score: 80,
					Indexed: &catalog.IndexedSearchEvidence{PresentationSourceID: "source", PresentationGeneration: 3, PresentationSourceRevision: 7, ObservationGroupID: group, NormalizedEntryDigest: "sha256:entry", Conflict: true, SourceCount: 2, SourceIDs: []string{"source"}, SourcesTruncated: true}}
				if remote.Local != nil || !reflect.DeepEqual(remote.Result, wantRemote) || remote.Position != (catalog.SemanticSearchPosition{Rank: 800, SortName: "tools", ID: "indexed:remote"}) {
					t.Fatalf("indexed projection=%+v evidence=%+v", remote, remote.Result.Indexed)
				}
			} else {
				wantLocal.ID, wantLocal.Score = "local:second-release", 70
				if !reflect.DeepEqual(remote.Result, wantLocal) || remote.Local == nil || remote.Local.Release.ID != "second-release" || remote.Local.RelevanceScore != 70 || remote.Position != (catalog.SemanticSearchPosition{Rank: 700, SortName: "example", ID: "local:second-release"}) {
					t.Fatalf("continuation hydration=%+v", remote)
				}
			}
			args := []any{pgx.QueryExecModeExec, f.g.TenantID, "tools", "tools", []string{"agent"}, []string{"tools"}, "publisher", catalog.VisibilityPublic, f.g.ID, f.g.Profile, request.Vector, 0.5, continuation, continuation, int64(0), "", "", 3, "", true, true, "", []string{}}
			if continuation {
				args[14], args[15], args[16] = int64(950), "before", "local:before"
			}
			if !reflect.DeepEqual(f.tx.queryArguments[0], args) || !reflect.DeepEqual(f.tx.queryArguments[1], []any{f.g.TenantID, ids}) {
				t.Fatalf("selection/hydration arguments=%v", f.tx.queryArguments)
			}
			if !strings.Contains(f.tx.queryStatements[0], `"vector""schema"."cosine_distance"`) || !strings.Contains(f.tx.queryStatements[0], `"vector""schema"."vector"`) {
				t.Fatal("extension identifiers are not quoted")
			}
			for _, result := range results {
				rows, ok := result.rows.(*identityScriptedRows)
				if !ok || !rows.closed {
					t.Fatal("selection or hydration rows leaked")
				}
			}
			if !reflect.DeepEqual(f.tx.queryRowArguments[2], []any{f.g.TenantID, f.g.ID}) {
				t.Fatalf("partial-index scope=%v", f.tx.queryRowArguments)
			}
			f.checkRead(t, err, pgx.RepeatableRead)
		})
	}
}

func TestSemanticSearchDiscardsIncompletePages(t *testing.T) {
	fault := errors.New("hybrid read failure")
	for _, mode := range []string{"empty", "query", "scan", "iteration", "incomplete", "work limit", "hydration", "missing local", "partial status"} {
		t.Run(mode, func(t *testing.T) {
			f, request := semanticSearchFixture(t)
			rows := &identityScriptedRows{values: [][]any{semanticSearchRow("indexed:remote", "")}}
			f.tx.queryResults = []identityQueryResult{{rows: rows}}
			wantCode, wantCause := catalog.CodeDependencyUnavailable, error(nil)
			switch mode {
			case "empty":
				rows.values[0][0], rows.values[0][1] = 0, (*string)(nil)
			case "query":
				f.tx.queryResults[0].err, wantCause = fault, fault
			case "scan":
				rows.values = [][]any{{"invalid row"}}
			case "iteration":
				rows.err, wantCause = fault, fault
			case "incomplete":
				rows.values[0][6] = (*string)(nil)
				wantCode = catalog.CodeInternal
			case "work limit":
				rows.values[0][0], wantCode = 100001, catalog.CodeWorkLimitExceeded
			case "hydration", "missing local":
				rows.values[0] = semanticSearchRow("local:release", "release")
				hydration := identityQueryResult{rows: &identityScriptedRows{}}
				if mode == "hydration" {
					hydration.err, wantCause = fault, fault
				} else {
					wantCode = catalog.CodeNotFound
				}
				f.tx.queryResults = append(f.tx.queryResults, hydration)
			case "partial status":
				f.tx.queryRows[2], wantCause = semanticFaultRow(fault), fault
			}
			page, err := f.store.Search(f.context(t), request)
			if mode == "empty" {
				if err != nil || page.Total != 0 || page.Items == nil || len(page.Items) != 0 || page.HasMore || !page.PartialIndex {
					t.Fatalf("empty page=%+v error=%v", page, err)
				}
			} else if catalog.ErrorCodeOf(err) != wantCode || (wantCause != nil && !errors.Is(err, wantCause)) || !reflect.DeepEqual(page, catalog.SemanticSearchPage{}) {
				t.Fatalf("failed page=%+v error=%v", page, err)
			}
			if mode != "query" && !rows.closed {
				t.Fatal("selection rows leaked")
			}
			if mode != "empty" && mode != "partial status" && len(f.tx.queryRowStatements) != 2 {
				t.Fatal("read partial status after failed selection")
			}
			f.checkRead(t, err, pgx.RepeatableRead)
		})
	}
}

func TestSemanticSearchAndExploreFenceBeforeSelection(t *testing.T) {
	fault := errors.New("generation lookup failed")
	for _, explore := range []bool{false, true} {
		for _, mode := range []string{"source error", "repair required", "source changed", "ready error", "missing generation", "not ready"} {
			t.Run(map[bool]string{false: "search/", true: "explore/"}[explore]+mode, func(t *testing.T) {
				f, request := semanticSearchFixture(t)
				wantCode, wantCause, reads := catalog.CodeConflict, catalog.ErrSearchGenerationChanged, 2
				switch mode {
				case "source error":
					f.tx.queryRows[0], wantCode, wantCause, reads = semanticFaultRow(fault), catalog.CodeDependencyUnavailable, fault, 1
				case "repair required":
					f.tx.queryRows[0], wantCode, wantCause, reads = scriptedValueRow([]any{int64(7), true}), catalog.CodeDependencyUnavailable, nil, 1
				case "source changed":
					f.tx.queryRows[0], reads = scriptedValueRow([]any{int64(8), false}), 1
				case "ready error":
					f.tx.queryRows[1], wantCode, wantCause = semanticFaultRow(fault), catalog.CodeDependencyUnavailable, fault
				case "missing generation":
					f.tx.queryRows[1] = semanticFaultRow(pgx.ErrNoRows)
				case "not ready":
					f.tx.queryRows[1] = scriptedValueRow([]any{false})
				}
				var err error
				if explore {
					var response catalog.ExploreResponse
					response, err = f.store.Explore(f.context(t), request, []catalog.FacetRequest{{Field: "tag"}})
					if !reflect.DeepEqual(response, catalog.ExploreResponse{}) {
						t.Fatalf("stale facets=%+v", response)
					}
				} else {
					var page catalog.SemanticSearchPage
					page, err = f.store.Search(f.context(t), request)
					if !reflect.DeepEqual(page, catalog.SemanticSearchPage{}) {
						t.Fatalf("stale page=%+v", page)
					}
				}
				if catalog.ErrorCodeOf(err) != wantCode || (wantCause != nil && !errors.Is(err, wantCause)) || len(f.tx.queryStatements) != 0 || len(f.tx.queryRowStatements) != reads {
					t.Fatalf("fencing error=%v reads=%d selections=%d", err, len(f.tx.queryRowStatements), len(f.tx.queryStatements))
				}
				if !reflect.DeepEqual(f.tx.queryRowArguments[0], []any{f.g.TenantID}) {
					t.Fatal("source generation lost tenant")
				}
				if reads == 2 {
					args := []any{f.g.TenantID, f.g.ID, f.g.Profile, f.g.Scope, f.g.SourceGeneration, f.g.Dimensions}
					if explore {
						args = []any{f.g.TenantID, f.g.ID, f.g.Profile, f.g.SourceGeneration, f.g.Dimensions}
					}
					if !reflect.DeepEqual(f.tx.queryRowArguments[1], args) {
						t.Fatalf("readiness arguments=%v", f.tx.queryRowArguments[1])
					}
				}
				f.checkRead(t, err, pgx.RepeatableRead)
			})
		}
	}
}

func TestSemanticHybridPreflight(t *testing.T) {
	for _, explore := range []bool{false, true} {
		for _, mode := range []string{"generation", "public", "nan", "infinite", "cutoff low", "cutoff high", "vector", "limit low", "limit high", "empty text", "no facets", "too many facets"} {
			if (explore && (strings.HasPrefix(mode, "limit") || mode == "empty text")) || (!explore && strings.Contains(mode, "facets")) {
				continue
			}
			t.Run(map[bool]string{false: "search/", true: "explore/"}[explore]+mode, func(t *testing.T) {
				f, request := semanticSearchFixture(t)
				facets := []catalog.FacetRequest{{Field: "tag"}}
				switch mode {
				case "generation":
					request.Generation.ID = "invalid"
				case "public":
					request.Generation.Scope = "public:" + f.g.ID
				case "nan":
					request.Cutoff = math.NaN()
				case "infinite":
					request.Cutoff = math.Inf(1)
				case "cutoff low":
					request.Cutoff = -1.01
				case "cutoff high":
					request.Cutoff = 1.01
				case "vector":
					request.Vector = []float32{0, 0, 0}
				case "limit low":
					request.Query.Limit = 0
				case "limit high":
					request.Query.Limit = 101
				case "empty text":
					request.Query.Text = ""
				case "no facets":
					facets = nil
				case "too many facets":
					facets = make([]catalog.FacetRequest, 11)
					for i := range facets {
						facets[i] = catalog.FacetRequest{Field: "tag"}
					}
				}
				var err error
				if explore {
					_, err = f.store.Explore(f.context(t), request, facets)
				} else {
					_, err = f.store.Search(f.context(t), request)
				}
				if catalog.ErrorCodeOf(err) != catalog.CodeInvalidArgument || len(f.connection.options) != 0 || len(f.tx.queryRowStatements) != 0 {
					t.Fatalf("invalid input reached transaction: %v", err)
				}
			})
		}
	}
}

func TestSemanticExploreProjectsBoundedFacets(t *testing.T) {
	fault := errors.New("facet query failure")
	for _, mode := range []string{"facets", "empty", "maximum total", "over limit", "invalid field", "query", "scan", "iteration", "partial status"} {
		t.Run(mode, func(t *testing.T) {
			f, request := semanticSearchFixture(t)
			facets := []catalog.FacetRequest{{Field: "tag", Limit: 3, MinCount: 2}, {Field: "kind"}}
			rows := &identityScriptedRows{values: [][]any{{5, new(0), new("tools"), new(3), new(2)}, {5, new(1), new("agent"), new(5), new(0)}}}
			f.tx.queryResults = []identityQueryResult{{rows: rows}}
			wantCode, wantCause := catalog.CodeDependencyUnavailable, error(nil)
			switch mode {
			case "empty":
				request.Query.Text = ""
				rows.values = [][]any{{0, (*int)(nil), (*string)(nil), (*int)(nil), (*int)(nil)}}
			case "maximum total", "over limit":
				for _, row := range rows.values {
					row[0] = 100000
					if mode == "over limit" {
						row[0] = 100001
					}
				}
				wantCode = catalog.CodeWorkLimitExceeded
			case "invalid field":
				facets[0].Field, wantCode = "arbitrary", catalog.CodeInvalidArgument
			case "query":
				f.tx.queryResults[0].err, wantCause = fault, fault
			case "scan":
				rows.values = [][]any{{"bad row"}}
			case "iteration":
				rows.err, wantCause = fault, fault
			case "partial status":
				f.tx.queryRows[2], wantCause = semanticFaultRow(fault), fault
			}
			response, err := f.store.Explore(f.context(t), request, facets)
			success := mode == "facets" || mode == "empty" || mode == "maximum total"
			if success {
				wantTotal := 5
				wantFacets := map[string]catalog.FacetResult{"tag": {Buckets: []catalog.FacetBucket{{Value: "tools", Count: 3}}, OtherCount: 2}, "kind": {Buckets: []catalog.FacetBucket{{Value: "agent", Count: 5}}}}
				if mode == "empty" {
					wantTotal = 0
					wantFacets = map[string]catalog.FacetResult{"tag": {Buckets: []catalog.FacetBucket{}}, "kind": {Buckets: []catalog.FacetBucket{}}}
				} else if mode == "maximum total" {
					wantTotal = 100000
				}
				if err != nil || response.Matched != wantTotal || !response.PartialIndex || !reflect.DeepEqual(response.Facets, wantFacets) {
					t.Fatalf("facets=%+v error=%v", response, err)
				}
				args := []any{pgx.QueryExecModeExec, f.g.TenantID, request.Query.Text, request.Query.Text, []string{"agent"}, []string{"tools"}, "publisher", "public", "", "", []string{}, f.g.ID, f.g.Profile, request.Vector, 0.5, 2, 3, 1, 20}
				if !reflect.DeepEqual(f.tx.queryArguments, [][]any{args}) || !reflect.DeepEqual(f.tx.queryRowArguments[2], []any{f.g.TenantID, f.g.ID}) {
					t.Fatalf("facet parameters=%v partial parameters=%v", f.tx.queryArguments, f.tx.queryRowArguments)
				}
			} else if catalog.ErrorCodeOf(err) != wantCode || (wantCause != nil && !errors.Is(err, wantCause)) || !reflect.DeepEqual(response, catalog.ExploreResponse{}) {
				t.Fatalf("failed facets=%+v error=%v", response, err)
			}
			if mode != "query" && mode != "invalid field" && !rows.closed {
				t.Fatal("facet rows leaked")
			}
			if !success && mode != "partial status" && len(f.tx.queryRowStatements) != 2 {
				t.Fatal("read partial status after failed facets")
			}
			f.checkRead(t, err, pgx.RepeatableRead)
		})
	}
}

func TestHybridLiveIdentityProbesPreserveVersionPresenceWithoutHydration(t *testing.T) {
	for _, mode := range []string{"selected identities", "oversized probes"} {
		t.Run(mode, func(t *testing.T) {
			f, request := semanticSearchFixture(t)
			request.Query.Origins = []catalog.SearchResultType{catalog.SearchResultIndexed}
			request.IdentityProbes = []catalog.IndexedIdentity{{Identifier: "urn:air:peer.example:agents:remote"}, {Identifier: "urn:air:peer.example:agents:remote", Version: "1.0", VersionPresent: true}}
			if mode == "oversized probes" {
				request.IdentityProbes = make([]catalog.IndexedIdentity, 51)
			}
			rows := &identityScriptedRows{values: [][]any{{request.IdentityProbes[0].Identifier, "", false}, {"urn:air:peer.example:agents:remote", "1.0", true}}}
			f.tx.queryResults = []identityQueryResult{{rows: rows}}
			page, err := f.store.Search(f.context(t), request)
			if mode == "oversized probes" {
				if catalog.ErrorCodeOf(err) != catalog.CodeInvalidArgument || len(f.tx.queryStatements) != 0 {
					t.Fatal("oversized probes reached selection", err)
				}
				return
			}
			if err != nil || !reflect.DeepEqual(page.Matches, request.IdentityProbes) || len(page.Items) != 0 || len(f.tx.queryStatements) != 1 || len(f.tx.queryRowStatements) != 2 || !rows.closed {
				t.Fatalf("identity-only projection=%+v error=%v", page, err)
			}
			args := f.tx.queryArguments[0]
			want := []any{[]string{request.IdentityProbes[0].Identifier, request.IdentityProbes[1].Identifier}, []string{"", "1.0"}, []bool{false, true}}
			if len(args) != 26 || !reflect.DeepEqual(args[23:], want) || args[19] != false || args[20] != true {
				t.Fatalf("identity probe or origin parameters=%v", args)
			}
			if !strings.Contains(f.tx.queryStatements[0], "LIMIT 51") || !strings.Contains(f.tx.queryStatements[0], "unnest($23::text[],$24::text[],$25::boolean[])") {
				t.Fatal("identity projection lost its fixed probe and work bounds")
			}
			f.checkRead(t, err, pgx.RepeatableRead)
		})
	}
}
