// Observation repository units preserve bounded sets and retained provenance.
package database

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/federation"
)

func TestObservationSnapshotEvidenceQueryErrorsAndBinding(t *testing.T) {
	for _, scenario := range []string{"present", "absent", "scan"} {
		t.Run(scenario, func(t *testing.T) {
			store, tx, ctx, source := federationAdapterFixture(t)
			row := scriptedValueRow([]any{scenario == "present"})
			if scenario == "scan" {
				row = scriptedValueRow(nil)
			}
			tx.queryRows = []pgx.Row{row}
			found, err := store.ObservationHasSnapshotEvidence(ctx, source.TenantID, "observation", source.ID, "snapshot", 7, "sha256:exact")
			if scenario == "scan" {
				if err == nil || found {
					t.Fatal(found, err)
				}
			} else if err != nil || found != (scenario == "present") {
				t.Fatal(found, err)
			}
			if !reflect.DeepEqual(tx.queryRowArguments, [][]any{{source.TenantID, "observation", source.ID, "snapshot", int64(7), "sha256:exact"}}) {
				t.Fatal("lost exact tenant/provenance binding")
			}
		})
	}
}

func TestFederationObservationMetadataReads(t *testing.T) {
	for _, outcome := range []string{"retained", "missing", "scan", "compatibility", "missing bytes"} {
		t.Run(outcome, func(t *testing.T) {
			store, tx, ctx, source := federationAdapterFixture(t)
			values := postgresObservationMetadataValues(source.CreatedAt, source.ID, "snapshot")
			values[11] = []byte(`{"structural":"valid","profileId":"ard-test","support":"supported","findings":[{"code":"RETAINED","message":"Metadata finding","severity":"warning","path":"/data"}]}`)
			values[24] = source.CreatedAt.Add(time.Hour)
			want := federation.Observation{
				ID: "00000000-0000-7000-8000-000000000092", TenantID: paginationTestTenantID, SourceID: source.ID, SnapshotID: "snapshot", SourceKey: "entry-1",
				Identifier: "urn:air:source.example.com:default:agent", Version: "1.0.0", VersionPresent: true, Kind: catalog.KindAgent, MediaType: "application/json", Completeness: federation.CompleteEntry,
				Compatibility:     federation.Compatibility{Structural: "valid", ProfileID: "ard-test", Support: "supported", Findings: []catalog.Finding{{Code: "RETAINED", Message: "Metadata finding", Severity: "warning", Path: "/data"}}},
				RawEntryAvailable: true, ExactEntryDigest: "sha256:" + strings.Repeat("f", 64), IdentityDigest: "sha256:" + strings.Repeat("1", 64), NormalizedEntryDigest: "sha256:" + strings.Repeat("2", 64), NormalizationProfile: "ard-test",
				SearchDisplayName: "Agent", SearchDescription: "Searchable agent", SearchTags: []string{"agent"}, SearchQueries: []string{"find agent"}, SearchPublisher: "source.example.com",
				Freshness: federation.FreshCurrent, FirstSeenAt: source.CreatedAt, LastSeenAt: source.CreatedAt.Add(time.Hour),
			}
			code := catalog.ErrorCode("")
			row := scriptedValueRow(values)
			switch outcome {
			case "missing":
				row, code = scannerFunc(func(...any) error { return pgx.ErrNoRows }), catalog.CodeNotFound
			case "scan":
				row, code = scriptedValueRow(nil), catalog.CodeDependencyUnavailable
			case "compatibility":
				values[11], code = []byte(`[]`), catalog.CodeInternal
			case "missing bytes":
				values[12], code = false, catalog.CodeInternal
			}
			tx.queryRows = []pgx.Row{row}
			got, err := store.GetObservationMetadata(ctx, source.TenantID, "observation")
			if code != "" {
				if catalog.ErrorCodeOf(err) != code || !reflect.DeepEqual(got, federation.Observation{}) {
					t.Fatalf("metadata error: %+v, %v", got, err)
				}
			} else if err != nil || !reflect.DeepEqual(got, want) {
				t.Fatalf("metadata result: %+v, %v", got, err)
			}
			if !reflect.DeepEqual(tx.queryRowArguments, [][]any{{source.TenantID, "observation"}}) {
				t.Fatal("parent check lost exact tenant/observation binding")
			}
		})
	}
}

// TestFederationCurrentObservationSets distinguishes complete sets from overflow
// before callers can mistake a partial selection for withdrawal authority.
func TestFederationCurrentObservationSets(t *testing.T) {
	failure := errors.New("current set failure")
	for _, summary := range []bool{false, true} {
		for _, outcome := range []string{"complete", "empty", "overflow", "query", "invalid limit"} {
			t.Run(map[bool]string{true: "summary", false: "records"}[summary]+"/"+outcome, func(t *testing.T) {
				store, tx, ctx, source := federationAdapterFixture(t)
				limit := 1
				if outcome == "invalid limit" {
					limit = 0
				}
				count := 1
				if outcome == "empty" {
					count = 0
				}
				if outcome == "overflow" {
					count = 2
				}
				digest := source.PolicyDigest
				tx.queryRows = []pgx.Row{scriptedValueRow([]any{count, outcome == "overflow", digest})}
				if outcome == "query" {
					tx.queryRows[0] = scannerFunc(func(...any) error { return failure })
				}
				rows := &identityScriptedRows{}
				for i := 0; i < count; i++ {
					values := postgresObservationValues(source.CreatedAt, source.ID, "snapshot")
					if i == 1 {
						values[0] = "00000000-0000-7000-8000-000000000093"
					}
					rows.values = append(rows.values, values)
				}
				result := identityQueryResult{rows: rows}
				if outcome == "query" {
					result.err = failure
				}
				tx.queryResults = []identityQueryResult{result}
				var more bool
				var err error
				if summary {
					var got federation.ObservationSet
					got, more, err = store.CurrentObservationSet(ctx, source.TenantID, source.ID, limit)
					if outcome == "complete" || outcome == "empty" {
						if got.Count != count || got.Digest != digest {
							t.Fatalf("set=%+v", got)
						}
					} else if got != (federation.ObservationSet{}) {
						t.Fatalf("partial set=%+v", got)
					}
				} else {
					var got []federation.Observation
					got, more, err = store.ListCurrentObservations(ctx, source.TenantID, source.ID, limit)
					if outcome == "complete" || outcome == "empty" {
						if got == nil || len(got) != count {
							t.Fatalf("records=%+v", got)
						}
					} else if got != nil {
						t.Fatal("partial observations returned")
					}
				}
				switch outcome {
				case "query":
					if !errors.Is(err, failure) || more {
						t.Fatalf("query failure=%t %v", more, err)
					}
				case "invalid limit":
					if catalog.ErrorCodeOf(err) != catalog.CodeInvalidArgument || more {
						t.Fatalf("bound=%t %v", more, err)
					}
				default:
					if err != nil || more != (outcome == "overflow") {
						t.Fatalf("set result=%t %v", more, err)
					}
				}
				if outcome == "invalid limit" {
					if len(tx.queryStatements)+len(tx.queryRowStatements) != 0 {
						t.Fatal("invalid limit queried")
					}
					return
				}
				args := []any{source.TenantID, source.ID, federation.FreshCurrent, 2}
				if summary {
					args = append(args, 1)
					if !reflect.DeepEqual(tx.queryRowArguments, [][]any{args}) {
						t.Fatalf("summary args=%v", tx.queryRowArguments)
					}
				} else if !reflect.DeepEqual(tx.queryArguments, [][]any{args}) || outcome != "query" && !rows.closed {
					t.Fatalf("selection args=%v closed=%t", tx.queryArguments, rows.closed)
				}
			})
		}
	}
}

// TestFederationObservationReads exercises the exact wrapper and verifies that
// returned retained bytes are owned by the result, not the driver's buffers.
func TestFederationObservationReads(t *testing.T) {
	for _, outcome := range []string{"retained", "missing", "scan", "compatibility", "missing bytes"} {
		t.Run(outcome, func(t *testing.T) {
			store, tx, ctx, source := federationAdapterFixture(t)
			values := postgresObservationValues(source.CreatedAt, source.ID, "snapshot")
			code := catalog.ErrorCode("")
			row := scriptedValueRow(values)
			switch outcome {
			case "missing":
				row = scannerFunc(func(...any) error { return pgx.ErrNoRows })
				code = catalog.CodeNotFound
			case "scan":
				row = scriptedValueRow(nil)
				code = catalog.CodeDependencyUnavailable
			case "compatibility":
				values[11] = []byte(`[]`)
				code = catalog.CodeInternal
			case "missing bytes":
				values[12] = []byte(nil)
				code = catalog.CodeInternal
			}
			tx.queryRows = []pgx.Row{row}
			observationID, ok := values[0].(string)
			if !ok {
				t.Fatal("fixture observation ID is not a string")
			}
			got, err := store.GetObservation(ctx, source.TenantID, observationID)
			if code != "" {
				if err == nil || catalog.ErrorCodeOf(err) != code || !reflect.DeepEqual(got, federation.Observation{}) {
					t.Fatalf("observation=%+v %v", got, err)
				}
			} else {
				if err != nil || got.ID != values[0] || got.SourceID != source.ID || got.Freshness != federation.FreshCurrent || !got.RawEntryAvailable || !got.VersionPresent || string(got.RawEntry) != `{"name":"Agent"}` || string(got.NormalizedEntry) != `{"name":"Agent"}` {
					t.Fatalf("observation=%+v %v", got, err)
				}
				raw, rawOK := values[12].([]byte)
				normalized, normalizedOK := values[13].([]byte)
				if !rawOK || !normalizedOK {
					t.Fatal("fixture evidence is not byte slices")
				}
				raw[0], normalized[0] = '!', '!'
				if got.RawEntry[0] != '{' || got.NormalizedEntry[0] != '{' {
					t.Fatal("driver buffer aliases retained evidence")
				}
			}
			if !reflect.DeepEqual(tx.queryRowArguments, [][]any{{source.TenantID, values[0]}}) {
				t.Fatalf("exact observation binding=%v", tx.queryRowArguments)
			}
		})
	}
}

// TestFederationObservationPages checks empty/error cleanup and continuation
// bindings; populated baseline decoding remains in postgres_pagination_test.go.
func TestFederationObservationPages(t *testing.T) {
	failure := errors.New("observation page failure")
	for _, mode := range []string{"current", "page", "group", "search"} {
		for _, outcome := range []string{"retained", "empty", "query", "scan", "iteration", "compatibility", "missing bytes", "generation"} {
			if mode == "current" && (outcome == "empty" || outcome == "query") {
				continue // Complete/empty/query controls belong to the bounded-set test.
			}
			if outcome == "retained" && mode != "group" {
				continue
			}
			if outcome == "generation" && mode != "search" {
				continue
			}
			t.Run(mode+"/"+outcome, func(t *testing.T) {
				store, tx, ctx, source := federationAdapterFixture(t)
				values := postgresObservationValues(source.CreatedAt, source.ID, "snapshot")
				if mode != "current" {
					values = postgresObservationMetadataValues(source.CreatedAt, source.ID, "snapshot")
				}
				if mode == "search" {
					values = append(values, 80)
				}
				rows := &identityScriptedRows{values: [][]any{values}}
				result := identityQueryResult{rows: rows}
				code := catalog.CodeDependencyUnavailable
				switch outcome {
				case "retained":
					code = ""
					values[5], values[6], values[7] = "urn:air:peer.example:a:first", "", false
				case "empty":
					rows.values = nil
					code = ""
				case "query":
					result.err = failure
				case "scan":
					rows.values = [][]any{{}}
				case "iteration":
					rows.err = failure
				case "compatibility":
					values[11] = []byte(`[]`)
					code = catalog.CodeInternal
				case "missing bytes":
					if mode == "current" {
						values[12] = []byte(nil)
					} else {
						values[12] = false
					}
					code = catalog.CodeInternal
				}
				tx.queryResults = []identityQueryResult{result}
				tx.queryRows = []pgx.Row{scriptedValueRow([]any{int64(7), false})}
				if outcome == "generation" {
					tx.queryRows[0] = scannerFunc(func(...any) error { return failure })
				}
				var got any
				var err error
				var args []any
				switch mode {
				case "current":
					var items []federation.Observation
					var more bool
					items, more, err = store.ListCurrentObservations(ctx, source.TenantID, source.ID, 1)
					got = items
					if more {
						t.Fatal("failed/empty selection reports overflow")
					}
					args = []any{source.TenantID, source.ID, federation.FreshCurrent, 2}
				case "page":
					got, err = store.ListObservationPage(ctx, source.TenantID, federation.ObservationListRequest{SourceID: source.ID, CurrentOnly: true, AfterIdentifier: "urn:air:peer.example:a:first", AfterVersion: "1", AfterVersionPresent: true, AfterID: source.InspectionID, Limit: 2})
					args = []any{source.TenantID, source.ID, true, federation.FreshCurrent, "urn:air:peer.example:a:first", true, "1", source.InspectionID, 2}
				case "group":
					got, err = store.ListObservationGroupMembers(ctx, source.TenantID, "urn:air:peer.example:a:first", "", false, 2)
					args = []any{source.TenantID, "urn:air:peer.example:a:first", false, "", federation.FreshCurrent, 2}
				case "search":
					got, err = store.SearchObservationPage(ctx, source.TenantID, federation.ObservationSearchRequest{Query: federation.SearchQuery{Text: "agent", Kinds: []catalog.Kind{catalog.KindAgent}, Tags: []string{"tools"}}, AfterScore: 90, AfterIdentifier: "before", AfterID: source.InspectionID, Limit: 2})
					args = []any{source.TenantID, "agent", []string{"agent"}, []string{"tools"}, true, 90, "before", source.InspectionID, 2}
				}
				if code == "" {
					wantCount := 0
					if outcome == "retained" {
						wantCount = 1
						members, ok := got.([]federation.Observation)
						if !ok || len(members) != 1 || members[0].Identifier != "urn:air:peer.example:a:first" || members[0].Version != "" || members[0].VersionPresent || members[0].SourceID != source.ID {
							t.Fatalf("group member provenance=%+v", got)
						}
					}
					if err != nil || reflect.ValueOf(got).IsNil() || reflect.ValueOf(got).Len() != wantCount {
						t.Fatalf("empty=%v %v", got, err)
					}
				} else if err == nil || catalog.ErrorCodeOf(err) != code || !reflect.ValueOf(got).IsNil() {
					t.Fatalf("failed page=%v %v", got, err)
				}
				if outcome == "generation" {
					if len(tx.queryStatements) != 0 {
						t.Fatal("generation failure reached selection")
					}
					return
				}
				if !reflect.DeepEqual(tx.queryArguments, [][]any{args}) || outcome != "query" && !rows.closed {
					t.Fatalf("page bindings=%v closed=%t", tx.queryArguments, rows.closed)
				}
				if mode != "current" {
					query := tx.queryStatements[0]
					if strings.Contains(query, "normalized_entry_bytes") || !strings.Contains(query, "octet_length(") {
						t.Fatal("metadata query must use presence, without normalized documents")
					}
				}
			})
		}
	}
}

// TestFederationObservationGroupConflicts keeps cross-source and same-source
// conflicts separate and avoids the per-source query when no sources are requested.
func TestFederationObservationGroupConflicts(t *testing.T) {
	failure := errors.New("conflict read failure")
	for _, outcome := range []string{"none", "content", "kind", "within source", "count failure", "query", "scan", "iteration"} {
		t.Run(outcome, func(t *testing.T) {
			store, tx, ctx, source := federationAdapterFixture(t)
			digests, kinds := 1, 1
			if outcome == "content" {
				digests = 2
			}
			if outcome == "kind" {
				kinds = 2
			}
			ids := []string{source.ID}
			if outcome == "none" {
				ids = nil
			}
			tx.queryRows = []pgx.Row{scriptedValueRow([]any{digests, kinds})}
			rows := &identityScriptedRows{}
			result := identityQueryResult{rows: rows}
			if outcome == "within source" {
				rows.values = [][]any{{source.ID}}
			}
			switch outcome {
			case "count failure":
				tx.queryRows[0] = scannerFunc(func(...any) error { return failure })
			case "query":
				result.err = failure
			case "scan":
				rows.values = [][]any{{}}
			case "iteration":
				rows.err = failure
			}
			tx.queryResults = []identityQueryResult{result}
			conflict, perSource, err := store.ObservationGroupConflicts(ctx, source.TenantID, "identifier", "", false, ids)
			fail := outcome == "count failure" || outcome == "query" || outcome == "scan" || outcome == "iteration"
			if fail {
				if err == nil || catalog.ErrorCodeOf(err) != catalog.CodeDependencyUnavailable || conflict || perSource != nil {
					t.Fatalf("conflicts=%t %v %v", conflict, perSource, err)
				}
			} else {
				if err != nil || conflict != (digests > 1 || kinds > 1) || perSource == nil || perSource[source.ID] != (outcome == "within source") {
					t.Fatalf("conflicts=%t %v %v", conflict, perSource, err)
				}
			}
			args := []any{source.TenantID, "identifier", false, "", federation.FreshCurrent}
			if !reflect.DeepEqual(tx.queryRowArguments, [][]any{args}) {
				t.Fatal("conflict identity lost version presence")
			}
			if outcome == "none" || outcome == "count failure" {
				if len(tx.queryStatements) != 0 {
					t.Fatal("unnecessary per-source selection")
				}
			} else {
				if !reflect.DeepEqual(tx.queryArguments, [][]any{append(args, ids)}) || outcome != "query" && !rows.closed {
					t.Fatal("per-source scope or cleanup mismatch")
				}
			}
		})
	}
}
