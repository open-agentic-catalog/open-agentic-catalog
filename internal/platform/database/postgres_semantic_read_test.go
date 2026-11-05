package database

import (
	"errors"
	"math"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
)

func TestSemanticInputsPageProjection(t *testing.T) {
	for _, mode := range []string{"control", "public", "exhausted", "source read", "source changed", "query", "scan", "missing name", "missing description", "iteration"} {
		t.Run(mode, func(t *testing.T) {
			f := newSemanticRepositoryFixture(t, true)
			failure := errors.New("input enumeration failed")
			if mode == "public" {
				f.g.Scope = "public:" + semanticSnapshotTestID
			}
			row := []any{int64(5), new("local:one"), new("Display"), new("Description"), []string{"tools"}, []string{"find tools"}}
			rows := &identityScriptedRows{values: [][]any{row}}
			f.tx.queryRows = []pgx.Row{scriptedValueRow([]any{"7"})}
			f.tx.queryResults = []identityQueryResult{{rows: rows}}
			code := catalog.ErrorCode("")
			switch mode {
			case "exhausted":
				rows.values = [][]any{{int64(5), (*string)(nil), (*string)(nil), (*string)(nil), []string(nil), []string(nil)}}
			case "source read":
				f.tx.queryRows[0] = semanticFaultRow(failure)
			case "source changed":
				f.tx.queryRows[0] = scriptedValueRow([]any{"8"})
				code = catalog.CodeConflict
			case "query":
				f.tx.queryResults[0].err = failure
			case "scan":
				rows.values = [][]any{{int64(5)}}
			case "missing name":
				row[2] = (*string)(nil)
				code = catalog.CodeInternal
			case "missing description":
				row[3] = (*string)(nil)
				code = catalog.CodeInternal
			case "iteration":
				rows.err = failure
			}
			got, total, err := f.store.Inputs(f.context(t), f.g, "local:before", 32)
			success := mode == "control" || mode == "public" || mode == "exhausted"
			if success {
				want := []catalog.SemanticInput{{Key: "local:one", Document: catalog.SemanticDocument{DisplayName: "Display", Description: "Description", Tags: []string{"tools"}, RepresentativeQueries: []string{"find tools"}}}}
				if mode == "exhausted" {
					want = []catalog.SemanticInput{}
				}
				if err != nil || total != 5 || !reflect.DeepEqual(got, want) {
					t.Fatalf("inputs=%+v total=%d err=%v", got, total, err)
				}
			} else if err == nil || got != nil || total != 0 {
				t.Fatalf("failed inputs=%+v total=%d err=%v", got, total, err)
			}
			if code != "" && catalog.ErrorCodeOf(err) != code {
				t.Fatalf("classification=%v", err)
			}
			if (mode == "source read" || mode == "query" || mode == "iteration") && !errors.Is(err, failure) {
				t.Fatal("enumeration cause lost")
			}
			if (mode == "source changed") != errors.Is(err, catalog.ErrSearchGenerationChanged) {
				t.Fatalf("generation cause=%v", err)
			}
			f.checkRead(t, err, pgx.RepeatableRead)
			wantSource := []any{f.g.TenantID}
			if mode == "public" {
				wantSource = append(wantSource, semanticSnapshotTestID)
			}
			if !reflect.DeepEqual(f.tx.queryRowArguments, [][]any{wantSource}) {
				t.Fatal("source scope lost")
			}
			if mode == "source read" || mode == "source changed" {
				if len(f.tx.queryStatements) != 0 {
					t.Fatal("stale source enumerated")
				}
				return
			}
			wantArgs := []any{f.g.TenantID, f.g.ID, "local:before", 32}
			if mode == "public" {
				wantArgs = append(wantArgs, semanticSnapshotTestID)
			}
			if !reflect.DeepEqual(f.tx.queryArguments, [][]any{wantArgs}) || rows.closed != (mode != "query") {
				t.Fatalf("query args=%v closed=%t", f.tx.queryArguments, rows.closed)
			}
		})
	}
}

func TestSemanticKnownInputsScopesAndErrors(t *testing.T) {
	for _, mode := range []string{"control", "public", "empty", "query", "scan", "iteration"} {
		t.Run(mode, func(t *testing.T) {
			f := newSemanticRepositoryFixture(t, true)
			failure := errors.New("reuse failed")
			if mode == "public" {
				f.g.Scope = "public:" + semanticSnapshotTestID
			}
			second := "sha256:" + strings.Repeat("b", 64)
			digests := []string{f.g.Profile, second}
			rows := &identityScriptedRows{values: [][]any{{f.g.Profile}}}
			f.tx.queryResults = []identityQueryResult{{rows: rows}}
			switch mode {
			case "empty":
				digests = nil
			case "query":
				f.tx.queryResults[0].err = failure
			case "scan":
				rows.values = [][]any{{}}
			case "iteration":
				rows.err = failure
			}
			got, err := f.store.KnownInputs(f.context(t), f.g, digests)
			if mode == "empty" {
				if err != nil || got == nil || len(got) != 0 || len(f.connection.options) != 0 {
					t.Fatalf("empty reuse=%v %v", got, err)
				}
				return
			}
			if mode == "control" || mode == "public" {
				if err != nil || !reflect.DeepEqual(got, map[string]bool{f.g.Profile: true}) {
					t.Fatalf("reuse=%v %v", got, err)
				}
			} else if err == nil || got != nil {
				t.Fatalf("failed reuse=%v %v", got, err)
			}
			if (mode == "query" || mode == "iteration") && !errors.Is(err, failure) {
				t.Fatal("reuse cause lost")
			}
			disclosure := "control"
			if mode == "public" {
				disclosure = "public"
			}
			if !reflect.DeepEqual(f.tx.queryArguments, [][]any{{f.g.TenantID, f.g.Profile, disclosure, 3, digests}}) || rows.closed != (mode != "query") {
				t.Fatalf("reuse scope=%v", f.tx.queryArguments)
			}
			f.checkRead(t, err, "")
		})
	}
}

func TestSemanticSimilaritiesFenceAndClamp(t *testing.T) {
	for _, mode := range []string{"scores", "public", "source read", "stale", "missing", "unready", "readiness query", "query", "scan", "nan", "infinite", "iteration"} {
		t.Run(mode, func(t *testing.T) {
			f := newSemanticRepositoryFixture(t, true)
			failure := errors.New("comparison failed")
			if mode == "public" {
				f.g.Scope = "public:" + semanticSnapshotTestID
			}
			rows := &identityScriptedRows{values: [][]any{{"one", 1.0000001}, {"two", -1.0000001}, {"three", 0.25}}}
			f.tx.queryRows = []pgx.Row{scriptedValueRow([]any{"7"}), scriptedValueRow([]any{true})}
			f.tx.queryResults = []identityQueryResult{{rows: rows}}
			switch mode {
			case "source read":
				f.tx.queryRows[0] = semanticFaultRow(failure)
			case "stale":
				f.tx.queryRows[0] = scriptedValueRow([]any{"8"})
			case "missing":
				f.tx.queryRows[1] = semanticFaultRow(pgx.ErrNoRows)
			case "unready":
				f.tx.queryRows[1] = scriptedValueRow([]any{false})
			case "readiness query":
				f.tx.queryRows[1] = semanticFaultRow(failure)
			case "query":
				f.tx.queryResults[0].err = failure
			case "scan":
				rows.values = [][]any{{"one"}}
			case "nan":
				rows.values[0][1] = math.NaN()
			case "infinite":
				rows.values[0][1] = math.Inf(1)
			case "iteration":
				rows.err = failure
			}
			keys := []string{"one", "two", "three", "unindexed"}
			vector := []float32{1, 0, 0}
			got, err := f.store.Similarities(f.context(t), f.g, vector, keys)
			if mode == "scores" || mode == "public" {
				if err != nil || !reflect.DeepEqual(got, map[string]float64{"one": 1, "two": -1, "three": 0.25}) {
					t.Fatalf("scores=%v %v", got, err)
				}
			} else if err == nil || got != nil {
				t.Fatalf("failed scores=%v %v", got, err)
			}
			changed := mode == "stale" || mode == "missing" || mode == "unready"
			if changed != errors.Is(err, catalog.ErrSearchGenerationChanged) || changed && catalog.ErrorCodeOf(err) != catalog.CodeConflict {
				t.Fatalf("generation cause=%v", err)
			}
			if (mode == "nan" || mode == "infinite") && catalog.ErrorCodeOf(err) != catalog.CodeInternal {
				t.Fatalf("nonfinite=%v", err)
			}
			if (mode == "source read" || mode == "readiness query" || mode == "query" || mode == "iteration") && !errors.Is(err, failure) {
				t.Fatal("comparison cause lost")
			}
			f.checkRead(t, err, pgx.RepeatableRead)
			count := 2
			if mode == "source read" || mode == "stale" {
				count = 1
			}
			sourceArgs := []any{f.g.TenantID}
			if mode == "public" {
				sourceArgs = append(sourceArgs, semanticSnapshotTestID)
			}
			want := [][]any{sourceArgs, {f.g.TenantID, f.g.ID, f.g.Profile, f.g.Scope, f.g.SourceGeneration, f.g.Dimensions}}
			if !reflect.DeepEqual(f.tx.queryRowArguments, want[:count]) {
				t.Fatalf("generation args=%v", f.tx.queryRowArguments)
			}
			if changed || mode == "source read" || mode == "readiness query" {
				if len(f.tx.queryStatements) != 0 {
					t.Fatal("unavailable generation compared")
				}
				return
			}
			if !reflect.DeepEqual(f.tx.queryArguments, [][]any{{f.g.TenantID, f.g.ID, keys, vector}}) || rows.closed != (mode != "query") {
				t.Fatalf("comparison scope=%v closed=%t", f.tx.queryArguments, rows.closed)
			}
		})
	}
}

func TestSemanticStatusCurrentAccounting(t *testing.T) {
	for _, mode := range []string{"ready", "pending", "building", "failed", "source read", "status read", "counts", "too large"} {
		t.Run(mode, func(t *testing.T) {
			f := newSemanticRepositoryFixture(t, true)
			failure := errors.New("status failed")
			state := "ready"
			if mode == "building" || mode == "failed" {
				state = mode
			}
			f.tx.queryRows = []pgx.Row{scriptedValueRow([]any{"7"}), scriptedValueRow([]any{f.g.ID, state}), scriptedValueRow([]any{int64(100000), int64(99998), int64(2)})}
			reads := 3
			switch mode {
			case "pending":
				f.tx.queryRows[1] = semanticFaultRow(pgx.ErrNoRows)
				reads = 2
			case "source read":
				f.tx.queryRows[0] = semanticFaultRow(failure)
				reads = 1
			case "status read":
				f.tx.queryRows[1] = semanticFaultRow(failure)
				reads = 2
			case "counts":
				f.tx.queryRows[2] = semanticFaultRow(failure)
			case "too large":
				f.tx.queryRows[2] = scriptedValueRow([]any{int64(100001), int64(99999), int64(2)})
			}
			got, err := f.store.Status(f.context(t), f.g.TenantID, f.g.Profile, f.g.Dimensions)
			switch mode {
			case "ready", "building", "failed", "pending":
				want := catalog.SemanticIndexStatus{JobID: f.g.ID, State: state, Indexed: 99998, Failed: 2}
				if mode == "pending" {
					want = catalog.SemanticIndexStatus{State: "pending"}
				}
				if err != nil || got != want {
					t.Fatalf("status=%+v %v", got, err)
				}
			default:
				if err == nil || got != (catalog.SemanticIndexStatus{}) {
					t.Fatalf("failed status=%+v %v", got, err)
				}
				if mode == "too large" {
					if catalog.ErrorCodeOf(err) != catalog.CodeWorkLimitExceeded {
						t.Fatal(err)
					}
				} else if !errors.Is(err, failure) {
					t.Fatal("status cause lost")
				}
			}
			f.checkRead(t, err, pgx.RepeatableRead)
			want := [][]any{{f.g.TenantID}, {f.g.TenantID, f.g.Profile, 3, "7"}, {f.g.TenantID, f.g.ID}}
			if !reflect.DeepEqual(f.tx.queryRowArguments, want[:reads]) || len(f.tx.queryStatements) != 0 {
				t.Fatalf("status arguments=%v", f.tx.queryRowArguments)
			}
		})
	}
}

func TestSemanticCurrentAndActiveReadScopes(t *testing.T) {
	for _, scope := range []string{"control", "public:00000000-0000-7000-8000-000000000902"} {
		for _, mode := range []string{"current", "active", "absent", "source failure", "active failure", "public missing"} {
			if mode == "public missing" && scope == "control" {
				continue
			}
			t.Run(scope+"/"+mode, func(t *testing.T) {
				f := newSemanticRepositoryFixture(t, true)
				f.g.Scope = scope
				failure := errors.New("generation read failed")
				now := time.Date(2026, time.September, 20, 12, 0, 0, 0, time.UTC)
				f.tx.queryRows = []pgx.Row{scriptedValueRow([]any{"7"}), scriptedValueRow([]any{f.g.ID, "ready", now, now})}
				switch mode {
				case "absent":
					f.tx.queryRows[1] = semanticFaultRow(pgx.ErrNoRows)
				case "source failure":
					f.tx.queryRows[0] = semanticFaultRow(failure)
				case "active failure":
					f.tx.queryRows[1] = semanticFaultRow(failure)
				case "public missing":
					f.tx.queryRows[0] = semanticFaultRow(pgx.ErrNoRows)
				}
				var err error
				reads := 2
				isolation := pgx.RepeatableRead
				if mode == "current" {
					var got string
					got, err = f.store.Current(f.context(t), f.g.TenantID, scope)
					if err != nil || got != "7" {
						t.Fatalf("current=%q %v", got, err)
					}
					reads = 1
					isolation = ""
				} else {
					got, found, readErr := f.store.Active(f.context(t), f.g.TenantID, f.g.Profile, scope, 3)
					err = readErr
					if mode == "active" {
						want := f.g
						want.State = "ready"
						want.CreatedAt = now
						want.UpdatedAt = now
						if err != nil || !found || got != want {
							t.Fatalf("active=%+v found=%t err=%v", got, found, err)
						}
					} else if found || got != (catalog.SemanticGeneration{}) {
						t.Fatalf("failed active=%+v found=%t err=%v", got, found, err)
					}
					if mode == "absent" && err != nil {
						t.Fatal(err)
					}
					if mode == "source failure" || mode == "public missing" {
						reads = 1
					}
					if mode == "source failure" || mode == "active failure" {
						if !errors.Is(err, failure) {
							t.Fatal("generation cause lost")
						}
					}
					if mode == "public missing" && catalog.ErrorCodeOf(err) != catalog.CodeNotFound {
						t.Fatalf("missing snapshot=%v", err)
					}
				}
				f.checkRead(t, err, isolation)
				sourceArgs := []any{f.g.TenantID}
				if scope != "control" {
					sourceArgs = append(sourceArgs, strings.TrimPrefix(scope, "public:"))
				}
				want := [][]any{sourceArgs, {f.g.TenantID, f.g.Profile, scope, "7", 3}}
				if !reflect.DeepEqual(f.tx.queryRowArguments, want[:reads]) || len(f.tx.queryStatements) != 0 {
					t.Fatalf("active scope=%v", f.tx.queryRowArguments)
				}
			})
		}
	}
}
