package database

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
)

func TestSemanticBeginFencesBothEndsOfWrite(t *testing.T) {
	for _, mode := range []string{"created", "lease missing", "lease expired", "lease query", "insert", "generation query", "generation changed", "lease lost after write", "final lease query"} {
		t.Run(mode, func(t *testing.T) {
			f := newSemanticRepositoryFixture(t, false)
			failure := errors.New("semantic write failed")
			f.tx.queryRows = []pgx.Row{scriptedValueRow([]any{true}), scriptedValueRow([]any{true}), scriptedValueRow([]any{true})}
			reads, writes := 3, 1
			code := catalog.ErrorCode("")
			switch mode {
			case "lease missing":
				f.tx.queryRows[0] = semanticFaultRow(pgx.ErrNoRows)
				reads, writes, code = 1, 0, catalog.CodeConflict
			case "lease expired":
				f.tx.queryRows[0] = scriptedValueRow([]any{false})
				reads, writes, code = 1, 0, catalog.CodeConflict
			case "lease query":
				f.tx.queryRows[0] = semanticFaultRow(failure)
				reads, writes, code = 1, 0, catalog.CodeDependencyUnavailable
			case "insert":
				f.tx.execErrors = []error{failure}
				reads, code = 1, catalog.CodeDependencyUnavailable
			case "generation query":
				f.tx.queryRows[1] = semanticFaultRow(failure)
				reads, code = 2, catalog.CodeDependencyUnavailable
			case "generation changed":
				f.tx.queryRows[1] = scriptedValueRow([]any{false})
				reads, code = 2, catalog.CodeConflict
			case "lease lost after write":
				f.tx.queryRows[2] = scriptedValueRow([]any{false})
				code = catalog.CodeConflict
			case "final lease query":
				f.tx.queryRows[2] = semanticFaultRow(failure)
				code = catalog.CodeDependencyUnavailable
			}
			err := f.store.Begin(f.context(t), f.g, "worker")
			if mode == "created" {
				if err != nil {
					t.Fatal(err)
				}
			} else if catalog.ErrorCodeOf(err) != code {
				t.Fatalf("begin = %v; want %s", err, code)
			}
			if code == catalog.CodeDependencyUnavailable && !errors.Is(err, failure) {
				t.Fatal("driver cause lost")
			}
			f.checkWrite(t, err)
			want := [][]any{semanticLeaseArguments(f.g), semanticGenerationArguments(f.g, true), semanticLeaseArguments(f.g)}
			if !reflect.DeepEqual(f.tx.queryRowArguments, want[:reads]) || len(f.tx.statements) != writes {
				t.Fatalf("begin calls = %v; writes=%v", f.tx.queryRowArguments, f.tx.arguments)
			}
			if writes != 0 && !reflect.DeepEqual(f.tx.arguments[0], []any{f.g.TenantID, f.g.ID, f.g.Profile, f.g.Scope, f.g.SourceGeneration, f.g.Dimensions}) {
				t.Fatal("generation insert binding lost")
			}
		})
	}
}

func TestSemanticSaveBatchDisclosuresAndReuse(t *testing.T) {
	for _, scope := range []string{"control", "public:00000000-0000-7000-8000-000000000902"} {
		t.Run(scope, func(t *testing.T) {
			f := newSemanticRepositoryFixture(t, false)
			f.g.Scope = scope
			members := []catalog.SemanticMember{
				{Key: "entry:one", InputDigest: f.g.Profile, Vector: []float32{1, 0, 0}},
				{Key: "entry:two", InputDigest: f.g.Profile, Reuse: true},
				{Key: "entry:three", Failure: "input_too_large"},
			}
			for range 6 {
				f.tx.queryRows = append(f.tx.queryRows, scriptedValueRow([]any{true}))
			}
			err := f.store.SaveBatch(f.context(t), f.g, "worker", members)
			if err != nil {
				t.Fatal(err)
			}
			f.checkWrite(t, err)
			disclosure := "control"
			if scope != "control" {
				disclosure = "public"
			}
			wantWrites := make([][]any, 0, 1+len(members))
			wantWrites = append(wantWrites, []any{f.g.TenantID, f.g.Profile, disclosure, f.g.Profile, 3, []float32{1, 0, 0}})
			wantReads := make([][]any, 0, 3+len(members))
			wantReads = append(wantReads, semanticLeaseArguments(f.g), semanticGenerationArguments(f.g, false))
			for _, m := range members {
				wantWrites = append(wantWrites, []any{f.g.TenantID, f.g.ID, m.Key, f.g.Profile, 3, disclosure, m.InputDigest, m.Failure})
				wantReads = append(wantReads, []any{f.g.TenantID, f.g.ID, m.Key, m.InputDigest, m.Failure})
			}
			wantReads = append(wantReads, semanticLeaseArguments(f.g))
			if !reflect.DeepEqual(f.tx.arguments, wantWrites) || !reflect.DeepEqual(f.tx.queryRowArguments, wantReads) {
				t.Fatalf("batch bindings = %v / %v", f.tx.arguments, f.tx.queryRowArguments)
			}
			if !strings.Contains(f.tx.statements[0], `"vector""schema"."vector"`) {
				t.Fatal("vector schema was not quoted")
			}
		})
	}
}

func TestSemanticSaveBatchStopsOnFailure(t *testing.T) {
	for _, mode := range []string{"generation", "vector", "member", "member read", "member changed", "final lease"} {
		t.Run(mode, func(t *testing.T) {
			f := newSemanticRepositoryFixture(t, false)
			failure := errors.New("batch failed")
			for range 4 {
				f.tx.queryRows = append(f.tx.queryRows, scriptedValueRow([]any{true}))
			}
			members := []catalog.SemanticMember{{Key: "local:one", InputDigest: f.g.Profile, Vector: []float32{1, 0, 0}}}
			reads, writes := 4, 2
			switch mode {
			case "generation":
				f.tx.queryRows[1] = semanticFaultRow(failure)
				reads, writes = 2, 0
			case "vector":
				f.tx.execErrors = []error{failure}
				reads, writes = 2, 1
			case "member":
				f.tx.execErrors = []error{nil, failure}
				reads = 2
			case "member read":
				f.tx.queryRows[2] = semanticFaultRow(failure)
				reads = 3
			case "member changed":
				f.tx.queryRows[2] = scriptedValueRow([]any{false})
				reads = 3
			case "final lease":
				f.tx.queryRows[3] = scriptedValueRow([]any{false})
			}
			err := f.store.SaveBatch(f.context(t), f.g, "worker", members)
			if mode == "member changed" || mode == "final lease" {
				if catalog.ErrorCodeOf(err) != catalog.CodeConflict {
					t.Fatalf("conflict = %v", err)
				}
			} else if !errors.Is(err, failure) {
				t.Fatalf("cause = %v", err)
			}
			f.checkWrite(t, err)
			if len(f.tx.queryRowStatements) != reads || len(f.tx.statements) != writes {
				t.Fatalf("continued after failure: reads/writes=%d/%d", len(f.tx.queryRowStatements), len(f.tx.statements))
			}
		})
	}
}

func TestSemanticCompleteChecksAccountingAndCurrentSource(t *testing.T) {
	for _, mode := range []string{"control", "public", "generation", "count read", "incomplete", "source read", "source changed", "update"} {
		t.Run(mode, func(t *testing.T) {
			f := newSemanticRepositoryFixture(t, false)
			failure := errors.New("completion failed")
			if mode == "public" {
				f.g.Scope = "public:" + semanticSnapshotTestID
			}
			f.tx.queryRows = []pgx.Row{scriptedValueRow([]any{true}), scriptedValueRow([]any{true}), scriptedValueRow([]any{int64(3)}), scriptedValueRow([]any{"7"}), scriptedValueRow([]any{true})}
			reads, writes := 5, 1
			switch mode {
			case "generation":
				f.tx.queryRows[1] = semanticFaultRow(failure)
				reads, writes = 2, 0
			case "count read":
				f.tx.queryRows[2] = semanticFaultRow(failure)
				reads, writes = 3, 0
			case "incomplete":
				f.tx.queryRows[2] = scriptedValueRow([]any{int64(2)})
				reads, writes = 3, 0
			case "source read":
				f.tx.queryRows[3] = semanticFaultRow(failure)
				reads, writes = 4, 0
			case "source changed":
				f.tx.queryRows[3] = scriptedValueRow([]any{"8"})
				reads, writes = 4, 0
			case "update":
				f.tx.execErrors = []error{failure}
				reads = 4
			}
			err := f.store.Complete(f.context(t), f.g, "worker", 3)
			switch mode {
			case "control", "public":
				if err != nil {
					t.Fatal(err)
				}
			case "incomplete", "source changed":
				if catalog.ErrorCodeOf(err) != catalog.CodeConflict {
					t.Fatalf("completion = %v", err)
				}
			default:
				if !errors.Is(err, failure) {
					t.Fatalf("cause = %v", err)
				}
			}
			if (mode == "source changed") != errors.Is(err, catalog.ErrSearchGenerationChanged) {
				t.Fatalf("source fence cause = %v", err)
			}
			f.checkWrite(t, err)
			sourceArgs := []any{f.g.TenantID}
			if mode == "public" {
				sourceArgs = append(sourceArgs, semanticSnapshotTestID)
			}
			want := [][]any{semanticLeaseArguments(f.g), semanticGenerationArguments(f.g, true), {f.g.TenantID, f.g.ID}, sourceArgs, semanticLeaseArguments(f.g)}
			if !reflect.DeepEqual(f.tx.queryRowArguments, want[:reads]) || len(f.tx.statements) != writes {
				t.Fatalf("completion calls=%v/%v", f.tx.queryRowArguments, f.tx.arguments)
			}
			if writes == 1 && !reflect.DeepEqual(f.tx.arguments[0], []any{f.g.TenantID, f.g.ID}) {
				t.Fatal("ready update lost tenant identity")
			}
		})
	}
}
