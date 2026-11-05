// Retained federation history is read through scripted tenant transactions.
package database

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/federation"
)

// TestFederationSnapshotHistory checks both bounded history selectors, including
// continuation handoff and failure paths absent from the populated page baseline.
func TestFederationSnapshotHistory(t *testing.T) {
	failure := errors.New("snapshot history failure")
	for _, paged := range []bool{false, true} {
		for _, outcome := range []string{"retained", "empty", "query", "scan", "decode", "iteration"} {
			t.Run(map[bool]string{true: "page", false: "history"}[paged]+"/"+outcome, func(t *testing.T) {
				store, tx, ctx, source := federationAdapterFixture(t)
				snapshot, _ := federationSyncFixture(t, source)
				raw, err := json.Marshal(snapshot)
				if err != nil {
					t.Fatal(err)
				}
				rows := &identityScriptedRows{values: [][]any{{raw}}}
				result := identityQueryResult{rows: rows}
				code := catalog.ErrorCode("")
				switch outcome {
				case "empty":
					rows.values = nil
				case "query":
					result.err = failure
					code = catalog.CodeDependencyUnavailable
				case "scan":
					rows.values = [][]any{{}}
					code = catalog.CodeDependencyUnavailable
				case "decode":
					rows.values = [][]any{{[]byte(`[]`)}}
					code = catalog.CodeInternal
				case "iteration":
					rows.err = failure
					code = catalog.CodeDependencyUnavailable
				}
				tx.queryResults = []identityQueryResult{result}
				var got []federation.Snapshot
				args := []any{source.TenantID, source.ID, 2}
				if paged {
					after := source.CreatedAt.Add(time.Hour)
					request := federation.SnapshotListRequest{SourceID: source.ID, AfterStartedAt: &after, AfterID: snapshot.ID, Limit: 2}
					got, err = store.ListSnapshotPage(ctx, source.TenantID, request)
					args = []any{source.TenantID, source.ID, request.AfterStartedAt, snapshot.ID, 2}
				} else {
					got, err = store.ListSnapshots(ctx, source.TenantID, source.ID, 2)
				}
				if code != "" {
					if err == nil || catalog.ErrorCodeOf(err) != code || got != nil {
						t.Fatalf("history=%v %v", got, err)
					}
				} else {
					if err != nil || got == nil {
						t.Fatalf("history=%v %v", got, err)
					}
					if outcome == "empty" {
						if len(got) != 0 {
							t.Fatal("empty history returned records")
						}
					} else {
						// Retrieval states/invalidations are commit inputs, omitted from retained JSON.
						snapshot.RetrievalStates = nil
						snapshot.RetrievalInvalidations = nil
						if len(got) != 1 || !reflect.DeepEqual(got[0], snapshot) {
							t.Fatalf("retained history=%+v want=%+v", got, snapshot)
						}
					}
				}
				if !reflect.DeepEqual(tx.queryArguments, [][]any{args}) || outcome != "query" && !rows.closed {
					t.Fatalf("history scope=%v closed=%t", tx.queryArguments, rows.closed)
				}
			})
		}
	}
}

// occurrenceReadValues retains every member of the exact provenance tuple.
func occurrenceReadValues(o federation.ObservationOccurrence) []any {
	return []any{o.ObservationID, o.SourceID, o.SnapshotID, o.SourceRevision, o.RetrievalDigest, o.EntryLocation, o.ExactEntryDigest, o.NormalizedEntryDigest, o.EvidenceComplete, o.ObservedAt, o.NormalizationInputDigest}
}

func TestFederationOccurrenceBatchCompletenessAndCleanup(t *testing.T) {
	failure := errors.New("summary rows failed")
	for _, outcome := range []string{"complete", "missing member", "scan", "iteration", "empty selection", "over limit"} {
		t.Run(outcome, func(t *testing.T) {
			store, tx, ctx, source := federationAdapterFixture(t)
			_, observation := federationSyncFixture(t, source)
			first := observation.Occurrence
			second := first
			second.ObservationID = "00000000-0000-7000-8000-000000000093"
			ids := []string{first.ObservationID, second.ObservationID}
			rows := &identityScriptedRows{values: [][]any{append(occurrenceReadValues(first), 123), append(occurrenceReadValues(second), 2)}}
			switch outcome {
			case "missing member":
				rows.values = rows.values[:1]
			case "scan":
				rows.values[1] = nil
			case "iteration":
				rows.err = failure
			case "empty selection":
				ids = nil
			case "over limit":
				ids = make([]string, federation.MaxObservationGroupContributions+1)
			}
			tx.queryResults = []identityQueryResult{{rows: rows}}
			got, err := store.ObservationOccurrenceSummaries(ctx, source.TenantID, ids)
			switch outcome {
			case "empty selection":
				if err != nil || len(got) != 0 || len(tx.queryStatements) != 0 {
					t.Fatalf("empty: %v", err)
				}
				return
			case "over limit":
				if catalog.ErrorCodeOf(err) != catalog.CodeInvalidArgument || got != nil || len(tx.queryStatements) != 0 {
					t.Fatalf("unbounded: %v", err)
				}
				return
			case "complete":
				want := map[string]federation.OccurrenceSummary{first.ObservationID: {Latest: first, Total: 123}, second.ObservationID: {Latest: second, Total: 2}}
				if err != nil || !reflect.DeepEqual(got, want) {
					t.Fatalf("batch: %+v %v", got, err)
				}
			default:
				if catalog.ErrorCodeOf(err) != catalog.CodeDependencyUnavailable || got != nil {
					t.Fatalf("partial summary accepted: %+v %v", got, err)
				}
				if outcome == "iteration" && !errors.Is(err, failure) {
					t.Fatal("iteration cause lost")
				}
			}
			if !rows.closed || !reflect.DeepEqual(tx.queryArguments, [][]any{{source.TenantID, ids}}) {
				t.Fatal("summary selection or cleanup changed")
			}
		})
	}
}

// TestFederationOccurrenceExactAndSummary distinguishes absent exact evidence
// from a missing summary, which is a dependency-integrity failure.
func TestFederationOccurrenceExactAndSummary(t *testing.T) {
	failure := errors.New("occurrence read failure")
	for _, summary := range []bool{false, true} {
		for _, outcome := range []string{"retained", "missing", "driver"} {
			t.Run(map[bool]string{true: "summary", false: "exact"}[summary]+"/"+outcome, func(t *testing.T) {
				store, tx, ctx, source := federationAdapterFixture(t)
				_, observation := federationSyncFixture(t, source)
				want := observation.Occurrence
				values := occurrenceReadValues(want)
				if summary {
					values = append(values, 4)
				}
				row := scriptedValueRow(values)
				if outcome == "missing" {
					row = scannerFunc(func(...any) error { return pgx.ErrNoRows })
				}
				if outcome == "driver" {
					row = scannerFunc(func(...any) error { return failure })
				}
				tx.queryRows = []pgx.Row{row}
				var got federation.ObservationOccurrence
				var total int
				var err error
				args := []any{source.TenantID, want.ObservationID}
				if summary {
					rows := &identityScriptedRows{values: [][]any{values}}
					result := identityQueryResult{rows: rows}
					if outcome == "missing" {
						rows.values = nil
					}
					if outcome == "driver" {
						result.err = failure
					}
					tx.queryResults = []identityQueryResult{result}
					var summaries map[string]federation.OccurrenceSummary
					summaries, err = store.ObservationOccurrenceSummaries(ctx, source.TenantID, []string{want.ObservationID})
					got, total = summaries[want.ObservationID].Latest, summaries[want.ObservationID].Total
					if outcome != "driver" && !rows.closed {
						t.Fatal("summary rows left open")
					}
					args = []any{source.TenantID, []string{want.ObservationID}}
				} else {
					got, err = store.GetObservationOccurrence(ctx, source.TenantID, want.ObservationID, want.SnapshotID, want.RetrievalDigest, want.EntryLocation)
					args = append(args, want.SnapshotID, want.RetrievalDigest, want.EntryLocation)
				}
				if outcome == "retained" {
					if err != nil || got != want || summary && total != 4 {
						t.Fatalf("occurrence=%+v count=%d error=%v", got, total, err)
					}
				} else {
					code := catalog.CodeDependencyUnavailable
					if outcome == "missing" && !summary {
						code = catalog.CodeNotFound
					}
					if err == nil || catalog.ErrorCodeOf(err) != code || got != (federation.ObservationOccurrence{}) || total != 0 {
						t.Fatalf("failed evidence=%+v count=%d error=%v", got, total, err)
					}
					if outcome == "driver" && !errors.Is(err, failure) {
						t.Fatalf("lost cause: %v", err)
					}
				}
				arguments := tx.queryRowArguments
				if summary {
					arguments = tx.queryArguments
				}
				if !reflect.DeepEqual(arguments, [][]any{args}) {
					t.Fatalf("provenance scope=%v", arguments)
				}
			})
		}
	}
}

// TestFederationOccurrenceHistory keeps page totals separate from bounded items
// and preserves the complete continuation tuple on the paginated path.
func TestFederationOccurrenceHistory(t *testing.T) {
	failure := errors.New("occurrence page failure")
	for _, paged := range []bool{false, true} {
		for _, outcome := range []string{"retained", "empty", "count", "query", "scan", "iteration"} {
			if paged && outcome == "count" {
				continue
			}
			t.Run(map[bool]string{true: "page", false: "history"}[paged]+"/"+outcome, func(t *testing.T) {
				store, tx, ctx, source := federationAdapterFixture(t)
				_, observation := federationSyncFixture(t, source)
				want := observation.Occurrence
				rows := &identityScriptedRows{values: [][]any{occurrenceReadValues(want)}}
				result := identityQueryResult{rows: rows}
				code := catalog.ErrorCode("")
				tx.queryRows = []pgx.Row{scriptedValueRow([]any{4})}
				switch outcome {
				case "empty":
					rows.values = nil
					tx.queryRows = []pgx.Row{scriptedValueRow([]any{0})}
				case "count":
					tx.queryRows[0] = scannerFunc(func(...any) error { return failure })
					code = catalog.CodeDependencyUnavailable
				case "query":
					result.err = failure
					code = catalog.CodeDependencyUnavailable
				case "scan":
					rows.values = [][]any{{}}
					code = catalog.CodeDependencyUnavailable
				case "iteration":
					rows.err = failure
					code = catalog.CodeDependencyUnavailable
				}
				tx.queryResults = []identityQueryResult{result}
				var got []federation.ObservationOccurrence
				var total int
				var err error
				args := []any{source.TenantID, want.ObservationID, 1}
				if paged {
					after := want.ObservedAt.Add(time.Hour)
					request := federation.OccurrenceListRequest{ObservationID: want.ObservationID, AfterObservedAt: &after, AfterSnapshotID: want.SnapshotID, AfterRetrievalDigest: want.RetrievalDigest, AfterEntryLocation: want.EntryLocation, Limit: 2}
					got, err = store.ListObservationOccurrencePage(ctx, source.TenantID, request)
					args = []any{source.TenantID, want.ObservationID, request.AfterObservedAt, want.SnapshotID, want.RetrievalDigest, want.EntryLocation, 2}
				} else {
					got, total, err = store.ListObservationOccurrences(ctx, source.TenantID, want.ObservationID, 1)
				}
				if code != "" {
					if err == nil || catalog.ErrorCodeOf(err) != code || got != nil || total != 0 {
						t.Fatalf("history=%v count=%d error=%v", got, total, err)
					}
				} else {
					if err != nil || got == nil {
						t.Fatalf("history=%v %v", got, err)
					}
					if outcome == "empty" {
						if len(got) != 0 || total != 0 {
							t.Fatal("empty history changed")
						}
					} else if len(got) != 1 || got[0] != want || !paged && total != 4 {
						t.Fatalf("history=%v count=%d", got, total)
					}
				}
				if outcome == "count" {
					if len(tx.queryStatements) != 0 {
						t.Fatal("failed count reached selection")
					}
					return
				}
				if !reflect.DeepEqual(tx.queryArguments, [][]any{args}) || outcome != "query" && !rows.closed {
					t.Fatalf("occurrence scope=%v closed=%t", tx.queryArguments, rows.closed)
				}
				if !paged && !reflect.DeepEqual(tx.queryRowArguments, [][]any{{source.TenantID, want.ObservationID}}) {
					t.Fatal("unscoped count")
				}
			})
		}
	}
}
