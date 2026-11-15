// Scripted tenant transactions isolate comparison projection and cleanup
// failures; the integration suite separately proves SQL, RLS and work bounds.
package database

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/federation"
)

// comparisonSnapshotValues follows the compact physical header, without the
// full retained retrieval document used by ordinary snapshot detail.
func comparisonSnapshotValues(s federation.Snapshot) []any {
	return []any{s.ID, s.TenantID, s.SourceID, s.SourceRevision, s.State, s.EnumerationComplete, s.WithdrawalAuthority, s.MemberCount, s.StartedAt, s.CompletedAt}
}

func TestFederationComparisonProjectionAndFailures(t *testing.T) {
	for _, mode := range []string{"success", "empty", "before missing", "after driver", "ineligible", "preflight driver", "overflow", "text budget", "count mismatch", "invalid evidence", "query", "scan", "before decode", "after decode", "iteration"} {
		t.Run(mode, func(t *testing.T) {
			store, tx, ctx, source := federationAdapterFixture(t)
			before, o := federationSyncFixture(t, source)
			after := before
			after.ID = "00000000-0000-7000-8000-000000000039"
			member := federation.SnapshotComparisonMember{ObservationID: o.ID, IdentityDigest: o.IdentityDigest, Occurrence: o.Occurrence}
			beforeRaw, err := json.Marshal(member)
			if err != nil {
				t.Fatal(err)
			}
			member.Occurrence.SnapshotID = after.ID
			afterRaw, err := json.Marshal(member)
			if err != nil {
				t.Fatal(err)
			}
			failure := errors.New("private comparison database sentinel")
			preflight := []any{1, 1, int64(1000), false}
			rows := &identityScriptedRows{values: [][]any{{1, o.SourceKey, "unchanged", 1, 1, beforeRaw, afterRaw}}}
			query := identityQueryResult{rows: rows}
			code := catalog.ErrorCode("")
			var beforeError, afterError, preflightError error
			switch mode {
			case "empty":
				before.MemberCount = 0
				after.MemberCount = 0
				preflight = []any{0, 0, int64(0), false}
				rows.values = nil
			case "before missing":
				beforeError = pgx.ErrNoRows
				code = catalog.CodeNotFound
			case "after driver":
				afterError = failure
				code = catalog.CodeDependencyUnavailable
			case "ineligible":
				after.State = federation.SnapshotIncomplete
				after.EnumerationComplete = false
				code = catalog.CodePreconditionFailed
			case "preflight driver":
				preflightError = failure
				code = catalog.CodeDependencyUnavailable
			case "overflow":
				preflight[0] = federation.MaxSynchronizationEntries + 1
				code = catalog.CodePreconditionFailed
			case "text budget":
				preflight[2] = int64(federation.MaxSnapshotComparisonTextBytes + 1)
				code = catalog.CodePreconditionFailed
			case "count mismatch":
				preflight[1] = 0
				code = catalog.CodeDependencyUnavailable
			case "invalid evidence":
				preflight[3] = true
				code = catalog.CodeDependencyUnavailable
			case "query":
				query.err = failure
				code = catalog.CodeDependencyUnavailable
			case "scan":
				rows.values = [][]any{{}}
				code = catalog.CodeDependencyUnavailable
			case "before decode":
				rows.values[0][5] = []byte(`[]`)
				code = catalog.CodeInternal
			case "after decode":
				rows.values[0][6] = []byte(`[]`)
				code = catalog.CodeInternal
			case "iteration":
				rows.err = failure
				code = catalog.CodeDependencyUnavailable
			}
			row := func(values []any, err error) pgx.Row {
				if err != nil {
					return scannerFunc(func(...any) error { return err })
				}
				return scriptedValueRow(values)
			}
			tx.queryRows = []pgx.Row{row(comparisonSnapshotValues(before), beforeError), row(comparisonSnapshotValues(after), afterError), row(preflight, preflightError)}
			tx.queryResults = []identityQueryResult{query}
			request := federation.SnapshotComparisonRequest{BeforeID: before.ID, AfterID: after.ID, IncludeUnchanged: true, Limit: 2}
			got, err := store.CompareSnapshots(ctx, source.TenantID, request)
			if code != "" {
				if catalog.ErrorCodeOf(err) != code || got.Items != nil || got.SourceID != "" || tx.rollbacks != 1 {
					t.Fatalf("partial or misclassified failure: %+v %v rollbacks=%d", got, err, tx.rollbacks)
				}
			} else {
				if err != nil || got.Items == nil || got.SourceID != source.ID || got.SourceRevision != source.Revision || got.Before.ID != before.ID || got.After.ID != after.ID {
					t.Fatalf("comparison=%+v %v", got, err)
				}
				if mode == "success" && (len(got.Items) != 1 || got.Items[0].Before.Occurrence.SnapshotID != before.ID || got.Items[0].After.Occurrence.SnapshotID != after.ID) {
					t.Fatal("projection lost exact occurrence identity")
				}
				if mode == "empty" && len(got.Items) != 0 {
					t.Fatal("empty membership acquired a result")
				}
				args := []any{source.TenantID, source.ID, before.ID, after.ID, federation.MaxSynchronizationEntries + 1, true, 0, 2}
				if !reflect.DeepEqual(tx.queryArguments, [][]any{args}) {
					t.Fatalf("selection/page binding=%v", tx.queryArguments)
				}
			}
			if mode == "success" || mode == "empty" || mode == "scan" || mode == "before decode" || mode == "after decode" || mode == "iteration" {
				if !rows.closed {
					t.Fatal("comparison rows were not closed")
				}
			}
		})
	}
}
