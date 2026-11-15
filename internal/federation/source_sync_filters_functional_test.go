//go:build functional

package federation

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/audit"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/clock"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/id"
)

// Real HTTPS cancellation must retain its failed attempt without hiding the
// earlier completed failure during the retry or changing complete membership.
func TestSourceProblemSelectionDuringCancellationAndRecovery(t *testing.T) {
	var mode, requests atomic.Int32
	started := make(chan struct{}, 1)
	server, outbound := newFederationHTTPSOrigin(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		switch mode.Load() {
		case 1:
			http.Error(w, "Owned temporary outage", http.StatusServiceUnavailable)
		case 2:
			select {
			case started <- struct{}{}:
			default:
			}
			<-r.Context().Done()
		default:
			w.Header().Set("Content-Type", ardDocumentMediaType)
			_, _ = io.WriteString(w, nestedHTTPSAgentDocument("stable"))
		}
	}))
	evidence := &recordingFederationEvidence{}
	store := NewMemoryStore(evidence)
	fakeClock := clock.NewFake(time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC))
	manager, err := NewManager(store, outbound, fakeClock, id.Generator{}, WithEvidenceSink(evidence, true))
	if err != nil {
		t.Fatal(err)
	}
	ctx, finish := context.WithTimeout(auditEvidenceContext(), 15*time.Second)
	defer finish()
	source := connectNestedHTTPSSource(t, ctx, manager, server.URL)
	complete, err := syncNestedHTTPSSource(t, ctx, manager, source, evidence)
	if err != nil || complete.State != SnapshotComplete || complete.MemberCount != 1 {
		t.Fatal("successful control", complete, err)
	}
	sealedComplete, err := json.Marshal(complete)
	if err != nil {
		t.Fatal(err)
	}
	mode.Store(1)
	fakeClock.Advance(time.Second)
	failed, err := syncNestedHTTPSSource(t, ctx, manager, source, evidence)
	if failure, _ := ClassifySyncError(err); failure != SyncFailureTransient || failed.State != SnapshotFailed {
		t.Fatal("outage control", failed, err)
	}
	assertProblem := func(snapshotID string) {
		t.Helper()
		before := requests.Load()
		beforeAudit, beforeOutbox := len(evidence.audit), len(evidence.outbox)
		rows, readErr := manager.ListSourcePage(ctx, SourceListRequest{SyncState: SnapshotFailed, Limit: 1})
		if readErr != nil || len(rows) != 1 || rows[0].ID != source.ID || rows[0].LatestCompletedSync == nil || rows[0].LatestCompletedSync.SnapshotID != snapshotID || rows[0].LatestCompletedSync.State != SnapshotFailed || rows[0].LastCompleteSnapshotID != complete.ID {
			t.Fatal("completed problem or membership changed", rows, readErr)
		}
		summary, summaryErr := manager.SourceOverview(ctx)
		if summaryErr != nil || len(summary.Problems) != 1 || summary.Problems[0].SourceID != source.ID || summary.Problems[0].Latest.State != SnapshotFailed || summary.Problems[0].Latest.Revision != source.Revision || summary.Problems[0].LastComplete == nil || !summary.Problems[0].LastComplete.CompletedAt.Equal(*complete.CompletedAt) {
			t.Fatal("Overview disagrees with current completed outcome", summary, summaryErr)
		}
		if len(evidence.audit) != beforeAudit || len(evidence.outbox) != beforeOutbox {
			t.Fatal("metadata triage wrote audit or notifications")
		}
		if requests.Load() != before {
			t.Fatal("metadata filtering contacted the HTTPS origin")
		}
	}
	assertProblem(failed.ID)
	mode.Store(2)
	fakeClock.Advance(time.Second)
	retryContext, cancel := context.WithCancel(ctx)
	defer cancel()
	jobID, err := (id.Generator{}).New(fakeClock.Now())
	if err != nil {
		t.Fatal(err)
	}
	retryContext = audit.WithJobExecution(retryContext, audit.JobExecution{ID: jobID, Kind: "source.sync", Attempt: 1, Initiator: auditEvidenceMembershipID, Trigger: "manual"})
	type completion struct {
		snapshot Snapshot
		err      error
	}
	done := make(chan completion, 1)
	go func() {
		snapshot, syncErr := manager.SyncAtRevision(retryContext, source.ID, source.Revision)
		done <- completion{snapshot, syncErr}
	}()
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal("HTTPS retry did not start", ctx.Err())
	}
	assertProblem(failed.ID)
	fakeClock.Advance(time.Second)
	cancel()
	var cancelled completion
	select {
	case cancelled = <-done:
	case <-ctx.Done():
		t.Fatal("cancelled synchronization did not finish", ctx.Err())
	}
	if failure, _ := ClassifySyncError(cancelled.err); failure != SyncFailureCancelled || cancelled.snapshot.State != SnapshotFailed || cancelled.snapshot.CompletedAt == nil || cancelled.snapshot.ID == failed.ID {
		t.Fatal("cancellation lost its recorded failed classification", cancelled)
	}
	assertProblem(cancelled.snapshot.ID)
	mode.Store(0)
	fakeClock.Advance(time.Second)
	recovered, err := syncNestedHTTPSSource(t, ctx, manager, source, evidence)
	if err != nil || recovered.State != SnapshotComplete || recovered.ID == complete.ID {
		t.Fatal("recovery", recovered, err)
	}
	rows, err := manager.ListSourcePage(ctx, SourceListRequest{SyncState: SnapshotFailed, Limit: 1})
	if err != nil || len(rows) != 0 {
		t.Fatal("new completion did not remove the problem", rows, err)
	}
	retained, err := store.GetSnapshot(ctx, source.TenantID, complete.ID)
	if err != nil {
		t.Fatal(err)
	}
	sealedAfter, err := json.Marshal(retained)
	if err != nil || !bytes.Equal(sealedComplete, sealedAfter) {
		t.Fatal("retained complete snapshot was rewritten", err)
	}
	retained, err = store.GetSnapshot(ctx, source.TenantID, cancelled.snapshot.ID)
	if err != nil || retained.State != SnapshotFailed {
		t.Fatal("recovery rewrote the cancelled attempt", retained, err)
	}
}
