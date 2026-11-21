package database

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/federation"
)

// Exercise the row decoder separately from the real indexed selection. A later
// attempt may explain source health but must never replace the evidence time.
func TestUpstreamChangeRowsRetainEvidenceAndRefuseCorruptPage(t *testing.T) {
	pool := &pgxpool.Pool{}
	repo, err := NewPostgresRepository(pool)
	if err != nil {
		t.Fatal(err)
	}
	store, err := NewPostgresFederationStore(repo)
	if err != nil {
		t.Fatal(err)
	}
	digest := func(c string) string { return "sha256:" + strings.Repeat(c, 64) }
	observed := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	finished := observed.Add(time.Minute)
	later := finished.Add(time.Hour)
	failed, attempt := "failed", "failed-attempt"
	values := func(outcome, normalizationInput string, withAttempt bool) []any {
		var attemptID, state *string
		var completed *time.Time
		if withAttempt {
			attemptID, state, completed = &attempt, &failed, &later
		}
		return []any{"resource", "namespace", "prior-revision", "local-label", "opaque-rc", "urn:air:peer.example:agents:sample", catalog.KindAgent, "application/a2a-agent-card+json", outcome,
			"observation", "source", "complete-snapshot", int64(2), digest("a"), "/entries/2", digest("b"), digest("c"), normalizationInput, true, observed,
			digest("d"), "prior-profile", "current-profile", finished, attemptID, int64(3), state, completed}
	}
	valid := values("comparison_unavailable", digest("e"), true)
	for _, tc := range []struct {
		name      string
		input     identityQueryResult
		wantError bool
	}{
		{"evidence and status", identityQueryResult{rows: &identityScriptedRows{values: [][]any{valid, values("unadopted_version", "", false)}}}, false},
		{"broken selected binding", identityQueryResult{rows: &identityScriptedRows{values: [][]any{valid, values("invalid", "", false)}}}, true},
		{"invalid optional digest", identityQueryResult{rows: &identityScriptedRows{values: [][]any{valid, values("changed_entry", "bad", false)}}}, true},
		{"incomplete row", identityQueryResult{rows: &identityScriptedRows{values: [][]any{valid, {"incomplete"}}}}, true},
		{"selection failed", identityQueryResult{err: errors.New("store unavailable")}, true},
		{"iteration interrupted", identityQueryResult{rows: &identityScriptedRows{values: [][]any{valid}, err: errors.New("stream interrupted")}}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tx := &identityScriptedTx{fakeCommandTx: &fakeCommandTx{}, queryResults: []identityQueryResult{tc.input}}
			got, err := store.ListUpstreamChanges(identityRepositoryContext(pool, tx), paginationTestTenantID, federation.UpstreamChangesQuery{Check: "all", Limit: 1})
			if tc.wantError {
				if catalog.ErrorCodeOf(err) != catalog.CodeDependencyUnavailable || len(got) != 0 {
					t.Fatalf("broken page = %+v, %v", got, err)
				}
				return
			}
			if err != nil || len(got) != 2 {
				t.Fatal(got, err)
			}
			r := got[0]
			if r.Target.ResourceID != "resource" || r.Target.NamespaceID != "namespace" || r.Target.PriorVersionRevisionID != "prior-revision" || r.Target.Version != "local-label" || r.Target.UpstreamVersion != "opaque-rc" ||
				r.Outcome != "comparison_unavailable" || r.PriorProfile != "prior-profile" || r.CurrentProfile != "current-profile" || r.PriorDigest != digest("d") || r.Occurrence.NormalizedEntryDigest != digest("c") || r.Occurrence.NormalizationInputDigest != digest("e") ||
				r.Occurrence.SnapshotID != "complete-snapshot" || r.Occurrence.SourceRevision != 2 || r.Occurrence.ObservedAt != observed || r.SnapshotCompletedAt != finished {
				t.Fatal("lost exact source or local evidence", r)
			}
			wantAttempt := federation.SourceSyncAttempt{SnapshotID: attempt, Revision: 3, State: federation.SnapshotFailed, CompletedAt: later}
			if r.LatestAttempt == nil || *r.LatestAttempt != wantAttempt || got[1].LatestAttempt != nil {
				t.Fatal("incorrect current-revision attempts", got)
			}
		})
	}
}
