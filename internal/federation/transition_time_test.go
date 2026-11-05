package federation

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/audit"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/clock"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/fetcher"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/id"
)

type backwardClockFetcher struct {
	fixedAuditFetcher
	clock *clock.Fake
}

func (f backwardClockFetcher) Fetch(ctx context.Context, request fetcher.Request) (fetcher.Result, error) {
	f.clock.Advance(-time.Second)
	return f.fixedAuditFetcher.Fetch(ctx, request)
}

func TestSourceTransitionsPreserveOrderingAcrossClockBacksteps(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 9, 7, 12, 0, 0, 0, time.UTC)
	evidence := &recordingFederationEvidence{}
	store := NewMemoryStore(evidence)
	source := Source{
		ID: "00000000-0000-7000-8000-000000000020", TenantID: auditEvidenceTenantID, DisplayName: "Remote",
		Type: SourceARDManifest, Endpoint: "https://catalog.remote.example/ard.json", Status: SourceActive,
		ProfileID: "ard-0.91-aa3e598", RetrievalMode: RetrievalIndexed, Scope: Scope{Kinds: []catalog.Kind{catalog.KindAgent}},
		UpdateMode: UpdateManual, Live: LivePolicy{FederationMode: "none"}, PolicyID: "test-policy", PolicyRevision: 1,
		PolicyDigest: federationTestPolicyDigest, Revision: 1, CreatedBy: auditEvidenceMembershipID, CreatedAt: now, UpdatedAt: now,
	}
	if _, err := store.CreateSource(context.Background(), source); err != nil {
		t.Fatal(err)
	}
	serviceClock := clock.NewFake(now.Add(-2 * time.Second))
	transport := backwardClockFetcher{fixedAuditFetcher{result: fetcher.Result{Status: http.StatusOK, RequestedURL: source.Endpoint, FinalURL: source.Endpoint, MediaType: "application/ard+json", ETag: `"version-1"`, Body: []byte(`{"entries":[]}`)}}, serviceClock}
	manager, err := NewManager(store, transport, serviceClock, id.Generator{}, WithEvidenceSink(evidence, true))
	if err != nil {
		t.Fatal(err)
	}
	ctx := auditEvidenceContext()
	displayName := "Renamed remote"
	patch := PatchInput{Intent: ChangeUpdate, DisplayName: &displayName, Reason: "Exercise clock-backstep update"}
	preview, err := manager.PreviewChange(ctx, source.ID, patch)
	if err != nil {
		t.Fatal(err)
	}
	if !preview.CreatedAt.Equal(serviceClock.Now()) || !preview.ExpiresAt.After(serviceClock.Now()) {
		t.Fatal("transition ordering changed preview validity timestamps")
	}
	patch.ChangePreviewDigest = preview.Digest
	source, err = manager.UpdateSource(ctx, source.ID, patch, source.Revision)
	if err != nil || !source.UpdatedAt.Equal(now) || source.DisplayName != displayName {
		t.Fatalf("source update = %#v, %v", source, err)
	}
	for _, status := range []SourceStatus{SourcePaused, SourceActive} {
		source, err = manager.SetStatus(ctx, source.ID, status, source.Revision, StatusChangeInput{Reason: "Exercise clock-backstep ordering"})
		if err != nil || !source.UpdatedAt.Equal(now) {
			t.Fatalf("source status %s = %#v, %v", status, source, err)
		}
	}
	jobCtx := audit.WithJobExecution(ctx, audit.JobExecution{ID: "00000000-0000-7000-8000-000000000021", Kind: "source.sync", Attempt: 1, Trigger: "manual", Initiator: auditEvidenceMembershipID})
	var completeID string
	for _, fail := range []bool{false, false, true} {
		if fail {
			transport.err = errors.New("upstream unavailable")
			manager.fetcher = transport
		}
		snapshot, err := manager.SyncAtRevision(jobCtx, source.ID, source.Revision)
		if (err != nil) != fail || !snapshot.StartedAt.Equal(now) || snapshot.CompletedAt == nil || !snapshot.CompletedAt.Equal(now) {
			t.Fatalf("sync (failure=%t) = %#v, %v", fail, snapshot, err)
		}
		if !fail && (len(snapshot.RetrievalStates) != 1 || len(snapshot.Retrievals) != 1) {
			t.Fatal("successful sync did not exercise conditional state and retrieval evidence")
		}
		for _, state := range snapshot.RetrievalStates {
			if state.UpdatedAt.Before(snapshot.StartedAt) {
				t.Fatal("retrieval state precedes the retained source timestamp")
			}
		}
		for _, attempt := range snapshot.Retrievals {
			if attempt.FetchedAt.Before(snapshot.StartedAt) || attempt.FetchedAt.After(*snapshot.CompletedAt) {
				t.Fatal("retrieval evidence falls outside its ordered snapshot interval")
			}
		}
		stored, getErr := store.GetSource(ctx, source.TenantID, source.ID)
		if getErr != nil || !stored.UpdatedAt.Equal(now) || stored.LastSyncAt == nil || !stored.LastSyncAt.Equal(now) {
			t.Fatalf("sync source timestamps = %#v, %v", stored, getErr)
		}
		if fail {
			if snapshot.State != SnapshotFailed || stored.LastCompleteSnapshotID != completeID {
				t.Fatal("failed sync changed the last complete snapshot")
			}
		} else {
			completeID = snapshot.ID
			if snapshot.State != SnapshotComplete || stored.LastCompleteSnapshotID != completeID {
				t.Fatal("complete sync did not retain its snapshot")
			}
		}
	}
	if !serviceClock.Now().Before(now) {
		t.Fatal("transition ordering changed the wall clock")
	}
	for _, record := range evidence.audit {
		if record.Action == audit.ActionSourceChangePreviewCreated {
			continue
		}
		if !record.At.Equal(now) {
			t.Fatalf("audit transition timestamp = %v, want %v", record.At, now)
		}
	}
	for _, event := range evidence.outbox {
		if !event.CreatedAt.Equal(now) || event.PublishedAt == nil || !event.PublishedAt.Equal(now) {
			t.Fatal("notification timestamps disagree with the source transition")
		}
	}
}
