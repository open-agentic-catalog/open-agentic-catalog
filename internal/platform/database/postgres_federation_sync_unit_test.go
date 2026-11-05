package database

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/audit"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/events"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/federation"
)

func federationSyncFixture(t *testing.T, source federation.Source) (federation.Snapshot, federation.Observation) {
	t.Helper()
	at := source.CreatedAt
	completed := at.Add(time.Minute)
	body := []byte(`{"entries":[{"futureType":"example"}]}`)
	digest := audit.DigestBytes(body)
	requestKey := audit.DigestBytes([]byte("request"))
	urlDigest := audit.DigestBytes([]byte(source.Endpoint))
	snapshot := federation.Snapshot{ID: "00000000-0000-7000-8000-000000000030", TenantID: source.TenantID, SourceID: source.ID, SourceRevision: source.Revision, State: federation.SnapshotComplete, EnumerationComplete: true, WithdrawalAuthority: true, MemberCount: 1, Added: 1, BodyDigest: digest, PolicyID: source.PolicyID, PolicyRevision: source.PolicyRevision, PolicyDigest: source.PolicyDigest, StartedAt: at, CompletedAt: &completed}
	snapshot.Retrievals = []federation.RetrievalEvidence{{ID: "00000000-0000-7000-8000-000000000031", RequestKey: requestKey, RequestedURLDigest: urlDigest, FinalURLDigest: urlDigest, ProfileID: "ard-test", Status: 200, ReceivedBody: true, RepresentationDigest: digest, FetchedAt: at}}
	snapshot.RetrievalStates = []federation.RetrievalState{{TenantID: source.TenantID, SourceID: source.ID, SourceRevision: source.Revision, RequestKey: requestKey, RequestedURL: source.Endpoint, FinalURL: source.Endpoint, RequestIdentityDigest: requestKey, ProfileID: "ard-test", PolicyDigest: source.PolicyDigest, RepresentationDigest: digest, MediaType: "application/json", Body: body, ETag: `"fixture"`, VarySelectionDigest: audit.DigestBytes(nil), UpdatedAt: at}}
	snapshot.RetrievalInvalidations = []string{audit.DigestBytes([]byte("obsolete request"))}
	raw := json.RawMessage(`{"futureType":"example"}`)
	exact := audit.DigestBytes(raw)
	observation := federation.Observation{ID: "00000000-0000-7000-8000-000000000032", TenantID: source.TenantID, SourceID: source.ID, SnapshotID: snapshot.ID, SourceKey: "entry:" + digest + ":/entries/0:" + exact, RawEntry: raw, RawEntryAvailable: true, ExactEntryDigest: exact, IdentityDigest: exact, Freshness: federation.FreshCurrent, FirstSeenAt: at, LastSeenAt: at}
	observation.Occurrence = federation.ObservationOccurrence{ObservationID: observation.ID, SourceID: source.ID, SnapshotID: snapshot.ID, SourceRevision: source.Revision, RetrievalDigest: digest, EntryLocation: "/entries/0", ExactEntryDigest: exact, EvidenceComplete: true, ObservedAt: at}
	if err := federation.ValidateSnapshotCommit(snapshot, []federation.Observation{observation}); err != nil {
		t.Fatalf("invalid synchronization fixture: %v", err)
	}
	return snapshot, observation
}

type staleFederationSyncTx struct{ *identityScriptedTx }

func (tx staleFederationSyncTx) Exec(ctx context.Context, query string, args ...any) (pgconn.CommandTag, error) {
	tag, err := tx.identityScriptedTx.Exec(ctx, query, args...)
	if strings.Contains(query, "UPDATE federation_sources") {
		tag = pgconn.NewCommandTag("UPDATE 0")
	}
	return tag, err
}

func TestFederationSyncAdapterRetainsEvidenceAndStopsOnDriverFailure(t *testing.T) {
	t.Parallel()
	stages := []string{"snapshot", "superseded observation", "occurrence", "invalidation", "retrieval document", "retrieval state", "retrieval attempt", "source state"}
	modes := append([]string{"success", "source read", "reconcile read", "observation write", "lost revision", "invalid audit"}, stages...)
	for _, mode := range modes {
		t.Run(mode, func(t *testing.T) {
			store, tx, ctx, source := federationAdapterFixture(t)
			snapshot, observation := federationSyncFixture(t, source)
			current := source
			current.DisplayName = "Authoritative configuration"
			source.DisplayName = "Stale caller configuration"
			source.LastCompleteSnapshotID = snapshot.ID
			source.LastSyncAt = snapshot.CompletedAt
			source.UpdatedAt = *snapshot.CompletedAt
			snapshot.Withdrawn = 999 // The locked reconciliation result owns this count.
			const retainedID = "00000000-0000-7000-8000-000000000033"
			tx.queryRows = []pgx.Row{federationSourceRow(t, current), scriptedValueRow([]any{2}), scriptedValueRow([]any{retainedID})}
			sentinel := errors.New("synchronization driver failure")
			wantWrites, wantReads := len(stages), 3
			var evidence []federation.MutationEvidence
			switch mode {
			case "source read":
				tx.queryRows[0] = scannerFunc(func(...any) error { return sentinel })
				wantWrites, wantReads = 0, 1
			case "reconcile read":
				tx.queryRows[1] = scannerFunc(func(...any) error { return sentinel })
				wantWrites, wantReads = 0, 2
			case "observation write":
				tx.queryRows[2] = scannerFunc(func(...any) error { return sentinel })
				wantWrites = 2
			case "lost revision":
				ctx = identityRepositoryContext(store.repository.pool, staleFederationSyncTx{tx})
			case "invalid audit":
				evidence = []federation.MutationEvidence{{Audit: audit.Record{ID: "unbound"}}}
				wantWrites, wantReads = 0, 2
			default:
				for index, stage := range stages {
					if mode == stage {
						tx.execErrors = make([]error, index+1)
						tx.execErrors[index] = sentinel
						wantWrites = index + 1
						if index < 2 {
							wantReads = 2
						}
					}
				}
			}
			got, updated, err := store.CommitSync(ctx, snapshot, []federation.Observation{observation}, source, 1, evidence...)
			if mode == "success" {
				if err != nil || got.Withdrawn != 2 || updated.DisplayName != current.DisplayName || updated.LastCompleteSnapshotID != snapshot.ID || updated.LastSyncAt == nil || !updated.LastSyncAt.Equal(*snapshot.CompletedAt) || tx.rollbacks != 0 {
					t.Fatalf("committed state=%+v source=%+v err=%v", got, updated, err)
				}
			} else if err == nil || got.ID != "" || updated.ID != "" || tx.rollbacks != 1 {
				t.Fatalf("failed sync disclosed partial success: snapshot=%s source=%s error=%v rollbacks=%d", got.ID, updated.ID, err, tx.rollbacks)
			}
			if mode != "success" && mode != "lost revision" && mode != "invalid audit" && !errors.Is(err, sentinel) {
				t.Fatalf("driver cause lost: %v", err)
			}
			if mode == "lost revision" && catalog.ErrorCodeOf(err) != catalog.CodePreconditionFailed {
				t.Fatalf("lost optimistic update returned %v", err)
			}
			if len(tx.statements) != wantWrites || len(tx.queryRowStatements) != wantReads || tx.commits != 0 {
				t.Fatalf("writes/reads=%d/%d want=%d/%d commits=%d", len(tx.statements), len(tx.queryRowStatements), wantWrites, wantReads, tx.commits)
			}
			if wantWrites >= 3 && !reflect.DeepEqual(tx.arguments[2][:5], []any{source.TenantID, retainedID, source.ID, snapshot.ID, source.Revision}) {
				t.Fatalf("occurrence did not use returned observation identity: %v", tx.arguments[2])
			}
			if wantWrites >= 4 && !reflect.DeepEqual(tx.arguments[3], []any{source.TenantID, source.ID, source.Revision, snapshot.RetrievalInvalidations[0]}) {
				t.Fatalf("invalidation lost exact source revision: %v", tx.arguments[3])
			}
			if wantWrites == len(stages) && !reflect.DeepEqual(tx.arguments[7], []any{source.TenantID, source.ID, source.Status, snapshot.ID, snapshot.CompletedAt, source.LastError, source.UpdatedAt, source.Revision}) {
				t.Fatalf("source-state binding=%v", tx.arguments[7])
			}
		})
	}
}

func TestFederationSyncAdapterGuardsBeforeWriting(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"source identity", "source tenant", "locked tenant", "locked revision", "caller revision", "snapshot revision", "unapproved reconciliation", "invalid snapshot"} {
		t.Run(mode, func(t *testing.T) {
			store, tx, ctx, source := federationAdapterFixture(t)
			snapshot, observation := federationSyncFixture(t, source)
			current := source
			want := catalog.CodePreconditionFailed
			switch mode {
			case "source identity":
				source.ID, want = "other", catalog.CodeNotFound
			case "source tenant":
				source.TenantID, want = "other", catalog.CodeNotFound
			case "locked tenant":
				current.TenantID, want = "other", catalog.CodeNotFound
			case "locked revision":
				current.Revision = 2
			case "caller revision":
				source.Revision = 2
			case "snapshot revision":
				snapshot.SourceRevision = 2
			case "unapproved reconciliation":
				snapshot.ReconcileCurrentView, snapshot.WithdrawalAuthority, want = true, false, catalog.CodeInvalidArgument
			case "invalid snapshot":
				snapshot.MemberCount, want = 2, catalog.CodeInvalidArgument
			}
			tx.queryRows = []pgx.Row{federationSourceRow(t, current)}
			got, updated, err := store.CommitSync(ctx, snapshot, []federation.Observation{observation}, source, 1)
			if catalog.ErrorCodeOf(err) != want || got.ID != "" || updated.ID != "" || len(tx.statements) != 0 || len(tx.queryRowStatements) != 1 {
				t.Fatalf("guard failed: snapshot=%s source=%s err=%v writes=%d reads=%d", got.ID, updated.ID, err, len(tx.statements), len(tx.queryRowStatements))
			}
		})
	}
}

func TestFederationPartialSyncDoesNotReconcileCurrentMembership(t *testing.T) {
	t.Parallel()
	store, tx, ctx, source := federationAdapterFixture(t)
	snapshot, observation := federationSyncFixture(t, source)
	snapshot.State, snapshot.EnumerationComplete, snapshot.WithdrawalAuthority = federation.SnapshotIncomplete, false, false
	snapshot.RetrievalStates = nil
	observation.Freshness = federation.FreshStale
	source.LastCompleteSnapshotID = "previous-complete"
	tx.queryRows = []pgx.Row{federationSourceRow(t, source), scriptedValueRow([]any{observation.ID})}
	got, updated, err := store.CommitSync(ctx, snapshot, []federation.Observation{observation}, source, 1)
	if err != nil || got.Withdrawn != 0 || updated.LastCompleteSnapshotID != "previous-complete" || len(tx.queryRowStatements) != 2 || len(tx.statements) != 5 {
		t.Fatalf("partial snapshot=%+v source=%+v err=%v", got, updated, err)
	}
	for _, query := range append(append([]string(nil), tx.statements...), tx.queryRowStatements...) {
		if strings.Contains(query, "UPDATE observations") {
			t.Fatal("partial synchronization rewrote current observation membership")
		}
	}
	if got.State != federation.SnapshotIncomplete {
		t.Fatalf("partial snapshot reported %s", got.State)
	}
}

// TestFederationSyncEvidenceUsesReconciledCounts verifies the repository handoff
// to the existing evidence encoder after locked membership reconciliation.
func TestFederationSyncEvidenceUsesReconciledCounts(t *testing.T) {
	for _, outcome := range []string{"retained", "audit failure", "outbox failure"} {
		t.Run(outcome, func(t *testing.T) {
			store, tx, ctx, source := federationAdapterFixture(t)
			snapshot, observation := federationSyncFixture(t, source)
			snapshot.Withdrawn = 999
			source.LastCompleteSnapshotID, source.LastSyncAt, source.UpdatedAt = snapshot.ID, snapshot.CompletedAt, *snapshot.CompletedAt
			facts := map[string]any{"sourceId": source.ID, "sourceRevision": source.Revision, "state": string(snapshot.State), "policyDigest": source.PolicyDigest, "memberCount": 1, "added": 1, "updated": 0, "withdrawn": 999, "jobId": "00000000-0000-7000-8000-000000000080", "jobKind": "source.sync", "jobAttempt": 1, "trigger": "manual"}
			record := currentTestAuditFacts(t, audit.ActionSourceSyncCompleted, "source_snapshot", snapshot.ID, *snapshot.CompletedAt, facts)
			data, err := events.MarshalReference(record.Action, record.TargetType, record.TargetID, "")
			if err != nil {
				t.Fatal(err)
			}
			evidence := federation.MutationEvidence{Audit: record, Outbox: &catalog.OutboxEvent{ID: "00000000-0000-7000-8000-000000000099", TenantID: source.TenantID, Type: events.CanonicalType(record.Action), Subject: snapshot.ID, Data: data, CreatedAt: record.At}}
			evidence, err = federation.FinalizeSyncEvidence(evidence, snapshot)
			if err != nil {
				t.Fatal(err)
			}
			if err := federation.ValidateMutationEvidence(source.TenantID, snapshot.ID, []federation.MutationEvidence{evidence}, record.Action); err != nil {
				t.Fatalf("invalid evidence fixture: %v", err)
			}
			initialDigest := evidence.Audit.AfterDigest
			tx.queryRows = []pgx.Row{federationSourceRow(t, source), scriptedValueRow([]any{2}), scriptedValueRow([]any{observation.ID})}
			failure := errors.New("synchronization evidence write failure")
			writes := 10
			if outcome != "retained" {
				if outcome == "audit failure" {
					writes = 9
				}
				tx.execErrors = make([]error, writes)
				tx.execErrors[writes-1] = failure
			}
			got, updated, err := store.CommitSync(ctx, snapshot, []federation.Observation{observation}, source, source.Revision, evidence)
			if outcome == "retained" {
				if err != nil || got.Withdrawn != 2 || updated.ID != source.ID || tx.rollbacks != 0 {
					t.Fatalf("sync=%+v source=%+v error=%v", got, updated, err)
				}
			} else if !errors.Is(err, failure) || !reflect.DeepEqual(got, federation.Snapshot{}) || !reflect.DeepEqual(updated, federation.Source{}) || tx.rollbacks != 1 {
				t.Fatalf("failed evidence returned partial sync: %+v %+v %v", got, updated, err)
			}
			if len(tx.statements) != writes || tx.commits != 0 {
				t.Fatalf("writes=%d commits=%d", len(tx.statements), tx.commits)
			}
			details, ok := tx.arguments[8][11].([]byte)
			if !ok {
				t.Fatal("audit details are not bytes")
			}
			var retained struct {
				Facts struct {
					Withdrawn int    `json:"withdrawn"`
					SourceID  string `json:"sourceId"`
					JobID     string `json:"jobId"`
				} `json:"facts"`
			}
			if err := json.Unmarshal(details, &retained); err != nil || retained.Facts.Withdrawn != 2 || retained.Facts.SourceID != source.ID || retained.Facts.JobID != facts["jobId"] {
				t.Fatalf("reconciled audit details=%s error=%v", details, err)
			}
			snapshot.Withdrawn = 2
			expected, err := federation.FinalizeSyncEvidence(evidence, snapshot)
			if err != nil || tx.arguments[8][10] != expected.Audit.AfterDigest || tx.arguments[8][10] == initialDigest {
				t.Fatalf("final audit digest=%v error=%v", tx.arguments[8][10], err)
			}
			if writes == 10 && (!reflect.DeepEqual(tx.arguments[9][4], expected.Outbox.Data) || tx.arguments[9][1] != source.TenantID || tx.arguments[9][3] != snapshot.ID) {
				t.Fatalf("notification does not bind final audit: %v", tx.arguments[9])
			}
		})
	}
}
