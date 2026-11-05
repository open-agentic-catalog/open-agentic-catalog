// Operational projection units check Go field mapping and failure handling;
// PostgreSQL integration tests retain aggregate and eligibility semantics.
package database

import (
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/observability"
)

// operationalSnapshotFixture supplies distinct values in SQL result order,
// independently of the expected named fields. Job state and kind totals agree.
func operationalSnapshotFixture() (*identityScriptedTx, observability.OperationalSnapshot) {
	query := &identityScriptedTx{fakeCommandTx: &fakeCommandTx{}}
	for _, values := range [][]int64{
		{11, 12, 13, 14, 15, 16, 17, 18, 19, 41, 20, 9, 120},
		{21, 22, 23, 24},
		{25, 26, 27, 180},
		{28, 29, 30, 31, 240},
		{32, 33, 300},
		{6, 90},
		{35},
		{36},
		{5, 38, 39},
	} {
		columns := make([]any, len(values))
		for i, value := range values {
			columns[i] = value
		}
		query.queryRows = append(query.queryRows, scriptedValueRow(columns))
	}
	return query, observability.OperationalSnapshot{
		Jobs: observability.JobSnapshot{
			Queued: 11, Running: 12, Waiting: 13, Succeeded: 14, Failed: 15, Cancelled: 16, DeadLetter: 17,
			SourceSync: 18, Publication: 19, Other: 41, Semantic: 20, SemanticPending: 9, OldestAgeSec: 120,
		},
		Sources: observability.SourceSnapshot{
			Active: 21, Paused: 22, Error: 23, Removed: 24, Complete: 25, Incomplete: 26, Failed: 27, LastAgeSec: 180,
		},
		Publications: observability.PublicationSnapshot{Candidate: 28, Active: 29, Retired: 30, Failed: 31, OldestAgeSec: 240},
		Outbox:       observability.OutboxSnapshot{Pending: 32, Acknowledged: 33, OldestAgeSec: 300},
		Webhooks: observability.WebhookSnapshot{
			DispatchableEvents: 6, OldestDispatchableAgeSec: 90, WaitingRetries: 35, LastAttemptFailedReceivers: 36,
			ReconciliationTenants: 5, CapacityLostEvents: 38, TerminalLostDeliveries: 39,
		},
	}
}

func TestCollectOperationalSnapshotMapsAggregates(t *testing.T) {
	t.Parallel()
	query, want := operationalSnapshotFixture()
	now := time.Date(2026, time.September, 21, 12, 34, 56, 789, time.FixedZone("fixture", 5*60*60+45*60))
	want.CollectedAt = now.UTC()
	got, err := collectOperationalSnapshot(t.Context(), query, now)
	if err != nil || got != want {
		t.Fatalf("snapshot = %+v, %v; want %+v", got, err, want)
	}
	wantArguments := [][]any{{now.UTC()}, nil, {now.UTC()}, {now.UTC()}, {now.UTC()}, {now.UTC()}, nil, nil, nil}
	if !reflect.DeepEqual(query.queryRowArguments, wantArguments) || len(query.queryRows) != 0 {
		t.Fatalf("aggregate arguments = %v, unread rows = %d; want %v and no unread rows", query.queryRowArguments, len(query.queryRows), wantArguments)
	}
}

// Each failed scan must discard both earlier aggregates and partially decoded
// fields, preserve the cause and stop before another read.
func TestCollectOperationalSnapshotStopsOnFailure(t *testing.T) {
	t.Parallel()
	for index, operation := range []string{
		"collect job operational aggregates",
		"collect source operational aggregates",
		"collect source snapshot operational aggregates",
		"collect publication operational aggregates",
		"collect outbox operational aggregates",
		"collect dispatchable webhook aggregates",
		"collect webhook retry aggregates",
		"collect webhook receiver outcome aggregates",
		"collect webhook reconciliation aggregates",
	} {
		t.Run(operation, func(t *testing.T) {
			query, _ := operationalSnapshotFixture()
			reads := len(query.queryRows)
			failure := errors.New("aggregate scan failed")
			query.queryRows[index] = scannerFunc(func(destinations ...any) error {
				if len(destinations) > 1 {
					setScanDestination(t, destinations, 0, int64(99))
				}
				return failure
			})
			got, err := collectOperationalSnapshot(t.Context(), query, time.Unix(100, 0))
			if got != (observability.OperationalSnapshot{}) {
				t.Fatalf("failed collection returned partial data: %+v", got)
			}
			var domain *catalog.DomainError
			if !errors.Is(err, failure) || !errors.As(err, &domain) || domain.Code != catalog.CodeDependencyUnavailable || domain.Message != operation {
				t.Fatalf("error = %v; want dependency failure with %q context and original cause", err, operation)
			}
			if len(query.queryRowStatements) != index+1 || len(query.queryRows) != reads-index-1 {
				t.Fatalf("failed collection made %d reads and left %d rows; want %d and %d", len(query.queryRowStatements), len(query.queryRows), index+1, reads-index-1)
			}
		})
	}
}
