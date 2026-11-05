// Semantic cleanup units check bindings, counts and failure stops without SQL.
package database

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
)

// semanticCleanupTransaction supplies affected-row counts without emulating
// SQL eligibility, deletion or transaction commit.
type semanticCleanupTransaction struct {
	*identityScriptedTx
	results []pgconn.CommandTag
}

func (tx *semanticCleanupTransaction) Exec(ctx context.Context, query string, args ...any) (pgconn.CommandTag, error) {
	tag, err := tx.identityScriptedTx.Exec(ctx, query, args...)
	if err != nil {
		return tag, err
	}
	if len(tx.results) == 0 {
		return pgconn.CommandTag{}, errors.New("unexpected semantic cleanup write")
	}
	tag, tx.results = tx.results[0], tx.results[1:]
	return tag, nil
}

// TestSemanticCleanupCountsAndBindings keeps per-class limits and explicit
// tenant/generation binding while excluding generation rows from the count.
func TestSemanticCleanupCountsAndBindings(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name                          string
		found                         bool
		members, generations, vectors int64
		limit                         int
		want                          int64
	}{
		{name: "idle", limit: 1},
		{name: "vectors without obsolete generation", vectors: 3, limit: 5, want: 3},
		{name: "partially drained generation", found: true, members: 5, vectors: 3, limit: 5, want: 8},
		{name: "released generation", found: true, members: 2, generations: 1, vectors: 3, limit: 5, want: 5},
		{name: "empty generation", found: true, generations: 1, limit: 1},
		{name: "maximum per-class work", found: true, members: 1000, vectors: 1000, limit: 1000, want: 2000},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := newSemanticRepositoryFixture(t, false)
			excluded := []string{f.g.TenantID, "00000000-0000-7000-8000-000000000002"}
			f.tx.queryRows = []pgx.Row{semanticFaultRow(pgx.ErrNoRows)}
			counts := []int64{test.vectors}
			operations := []string{"DELETE FROM semantic_vectors"}
			arguments := [][]any{{test.limit}}
			if test.found {
				f.tx.queryRows[0] = scriptedValueRow([]any{f.g.TenantID, f.g.ID})
				counts = []int64{test.members, test.generations, test.vectors}
				operations = []string{"DELETE FROM semantic_members", "DELETE FROM semantic_generations", "DELETE FROM semantic_vectors"}
				arguments = [][]any{{f.g.TenantID, f.g.ID, test.limit}, {f.g.TenantID, f.g.ID}, {test.limit}}
			}
			tx := &semanticCleanupTransaction{identityScriptedTx: f.tx}
			for _, count := range counts {
				tx.results = append(tx.results, pgconn.NewCommandTag(fmt.Sprintf("DELETE %d", count)))
			}
			removed, err := cleanupSemanticTx(t.Context(), tx, f.g.Profile, excluded, test.limit)
			if err != nil || removed != test.want {
				t.Fatalf("cleanup removed=%d, error=%v; want %d", removed, err, test.want)
			}
			if len(tx.queryRowStatements) != 1 || !reflect.DeepEqual(tx.queryRowArguments, [][]any{{f.g.Profile, excluded}}) || len(tx.queryRows) != 0 || len(tx.queryStatements) != 0 {
				t.Fatalf("cleanup selection binding or read count changed: %v", tx.queryRowArguments)
			}
			if !reflect.DeepEqual(tx.arguments, arguments) || len(tx.statements) != len(operations) || len(tx.results) != 0 {
				t.Fatalf("cleanup writes=%v arguments=%v remaining results=%d", tx.statements, tx.arguments, len(tx.results))
			}
			for i, operation := range operations {
				if !strings.Contains(tx.statements[i], operation) {
					t.Fatalf("cleanup write %d did not perform %q: %s", i, operation, tx.statements[i])
				}
			}
			if tx.commits != 0 || tx.rollbacks != 0 {
				t.Fatal("cleanup helper took transaction ownership")
			}
		})
	}
}

// TestSemanticCleanupFailuresDiscardCounts stops on each failed read/write and
// leaves rollback to the caller, including after successful provisional deletes.
func TestSemanticCleanupFailuresDiscardCounts(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name, message string
		noGeneration  bool
		writes        int
	}{
		{name: "selection", message: "select obsolete semantic generation"},
		{name: "members", message: "remove obsolete semantic members", writes: 1},
		{name: "generation", message: "release obsolete semantic generation", writes: 2},
		{name: "vectors", message: "remove unused semantic vectors", writes: 3},
		{name: "vectors without generation", message: "remove unused semantic vectors", noGeneration: true, writes: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := newSemanticRepositoryFixture(t, false)
			failure := errors.New("injected cleanup failure")
			f.tx.queryRows = []pgx.Row{scriptedValueRow([]any{f.g.TenantID, f.g.ID})}
			tx := &semanticCleanupTransaction{identityScriptedTx: f.tx,
				results: []pgconn.CommandTag{pgconn.NewCommandTag("DELETE 2"), pgconn.NewCommandTag("DELETE 1"), pgconn.NewCommandTag("DELETE 3")}}
			if test.noGeneration {
				f.tx.queryRows[0] = semanticFaultRow(pgx.ErrNoRows)
			}
			if test.writes == 0 {
				f.tx.queryRows[0] = semanticFaultRow(failure)
			} else {
				tx.execErrors = make([]error, test.writes)
				tx.execErrors[test.writes-1] = failure
			}
			removed, err := cleanupSemanticTx(t.Context(), tx, f.g.Profile, []string{}, 5)
			if removed != 0 || !errors.Is(err, failure) || catalog.ErrorCodeOf(err) != catalog.CodeDependencyUnavailable || !strings.Contains(err.Error(), test.message) {
				t.Fatalf("failed cleanup returned count=%d error=%v", removed, err)
			}
			if len(tx.queryRowStatements) != 1 || len(tx.queryStatements) != 0 || len(tx.statements) != test.writes || tx.commits != 0 || tx.rollbacks != 0 {
				t.Fatalf("failed cleanup continued work or took ownership: reads=%d writes=%d commits=%d rollbacks=%d", len(tx.queryRowStatements), len(tx.statements), tx.commits, tx.rollbacks)
			}
		})
	}
}
