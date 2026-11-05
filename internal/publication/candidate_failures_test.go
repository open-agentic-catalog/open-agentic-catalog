// Candidate failure tests preserve active snapshots and reject invalid artifact origins.
package publication

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
)

type candidateStoreSpy struct {
	Store
	calls int
	err   error
}

func (s *candidateStoreSpy) SaveCandidate(ctx context.Context, snapshot Snapshot, evidence ...MutationEvidence) (Snapshot, error) {
	s.calls++
	if s.err != nil {
		return Snapshot{}, s.err
	}
	return s.Store.SaveCandidate(ctx, snapshot, evidence...)
}

type candidateAuditIDs struct {
	calls int
	err   error
}

func (ids *candidateAuditIDs) New(time.Time) (string, error) {
	ids.calls++
	if ids.calls == 2 {
		return "", ids.err
	}
	return "00000000-0000-7000-8000-000000000900", nil
}

func TestPublicationCandidatePreparationAndStorageFailures(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"cancelled", "audit attribution", "audit ID", "store", "selection limit", "aggregate work"} {
		t.Run(mode, func(t *testing.T) {
			manager, store, _ := newPublicationManager(t, []catalog.CatalogRecord{eligiblePublicationRecord()})
			active := activatePublicationSnapshot(t, manager)
			generation, err := store.NextGeneration(t.Context(), "tenant-1", active.Host)
			if err != nil {
				t.Fatal(err)
			}
			spy := &candidateStoreSpy{Store: store}
			manager.store = spy
			sink := &recordingPublicationEvidence{}
			sentinel := errors.New("candidate dependency failed")
			ctx := publicationContext(t.Context(), "tenant-1")
			wantCalls := 0
			wantError := sentinel
			var wantCode catalog.ErrorCode
			ids := &candidateAuditIDs{err: sentinel}
			switch mode {
			case "cancelled":
				release, err := manager.constructionLimiter.acquire(ctx)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(release)
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
				wantError = context.Canceled
			case "audit attribution":
				ctx = t.Context()
				manager.evidence = sink
				wantError, wantCode = nil, catalog.CodeForbidden
			case "audit ID":
				manager.evidence = sink
				manager.ids = ids
				wantCode = catalog.CodeInternal
			case "selection limit":
				manager.records = publicationRecordSource{overflow: true}
				wantError, wantCode = nil, catalog.CodeWorkLimitExceeded
			case "aggregate work":
				oversized := eligiblePublicationRecord()
				oversized.ListingRevision.Tags = make([]string, MaxPublicationWorkUnits)
				manager.records = publicationRecordSource{records: []catalog.CatalogRecord{oversized}}
				wantError, wantCode = nil, catalog.CodeWorkLimitExceeded
			case "store":
				spy.err = sentinel
				wantCalls = 1
			}
			got, err := manager.BuildCandidate(ctx, "tenant-1")
			if got.ID != "" || err == nil || wantError != nil && !errors.Is(err, wantError) || wantCode != "" && catalog.ErrorCodeOf(err) != wantCode {
				t.Fatalf("candidate=%+v error=%v", got, err)
			}
			if mode == "audit ID" && ids.calls != 2 {
				t.Fatalf("identifier failure occurred before audit preparation: %d calls", ids.calls)
			}
			if spy.calls != wantCalls || len(store.snapshots) != 1 || len(sink.audit) != 0 || len(sink.outbox) != 0 {
				t.Fatalf("save calls=%d snapshots=%d evidence=%+v", spy.calls, len(store.snapshots), sink)
			}
			retained, err := store.Active(t.Context(), "tenant-1")
			if err != nil || !reflect.DeepEqual(retained, active) {
				t.Fatal("candidate failure changed active snapshot")
			}
			if next, err := store.NextGeneration(t.Context(), "tenant-1", active.Host); err != nil || next != generation {
				t.Fatalf("candidate failure consumed generation: %d, %v", next, err)
			}
			if mode == "selection limit" || mode == "aggregate work" {
				manager.records = publicationRecordSource{records: []catalog.CatalogRecord{eligiblePublicationRecord()}}
				recovered, err := manager.BuildCandidate(publicationContext(t.Context(), "tenant-1"), "tenant-1")
				if err != nil || recovered.Generation != generation || len(store.snapshots) != 2 {
					t.Fatalf("valid construction after limit rejection: %+v, %v", recovered, err)
				}
			}

		})
	}
}

func TestPublicationRejectsArtifactFromDifferentDeliveryOrigin(t *testing.T) {
	t.Parallel()
	record := eligiblePublicationRecord()
	record.VersionRevision.Representations[0].Data = nil
	record.VersionRevision.Representations[0].ArtifactID = "artifact-1"
	manager, store, _ := newPublicationManager(t, []catalog.CatalogRecord{record})
	snapshot, err := manager.BuildCandidate(publicationContext(t.Context(), "tenant-1"), "tenant-1")
	if err != nil {
		t.Fatal(err)
	}
	for i := range snapshot.ARD.Entries {
		if snapshot.ARD.Entries[i].Metadata[catalogReleaseIDMetadata] != "registry:self" {
			snapshot.ARD.Entries[i].URL = "https://catalog.example.com:8443/public/v1/artifacts/artifact-1"
		}
	}
	snapshot.AICatalog.Entries[0].URL = "https://catalog.example.com:8443/public/v1/artifacts/artifact-1"
	rehashSnapshotDocuments(t, &snapshot)
	// Retained integrity binds the hostname. The manager additionally binds the
	// configured delivery origin, including its port, before activation.
	if err := ValidateSnapshotIntegrity(snapshot); err != nil {
		t.Fatalf("fixture did not reach configured origin validation: %v", err)
	}
	store.snapshots[memorySnapshotKey(snapshot.TenantID, snapshot.ID)] = cloneSnapshot(snapshot)
	spy := &publicationMutationSpy{Store: store}
	manager.store = spy
	got, err := manager.Activate(publicationContext(t.Context(), "tenant-1"), "tenant-1", snapshot.ID, "Activate reviewed snapshot")
	if catalog.ErrorCodeOf(err) != catalog.CodePublicationNotReady || got.ID != "" || spy.calls != 0 {
		t.Fatalf("activation=%+v error=%v writes=%d", got, err, spy.calls)
	}
	if !strings.Contains(err.Error(), "artifact allowlist is inconsistent") {
		t.Fatalf("wrong rejection boundary: %v", err)
	}
	retained, err := store.Get(publicationContext(t.Context(), "tenant-1"), "tenant-1", snapshot.ID)
	if err != nil || !reflect.DeepEqual(retained, snapshot) {
		t.Fatal("rejected activation rewrote retained evidence")
	}
}
