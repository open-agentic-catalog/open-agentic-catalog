package publication

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"testing"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
)

func TestWithdrawalPreviewMatchesBothProfilesWithoutChangingPublication(t *testing.T) {
	first := eligiblePublicationRecord()
	second := eligiblePublicationRecord()
	second.Resource.ID, second.Resource.CanonicalIdentifier = "resource-two", "urn:air:catalog.example.com:default:second"
	second.Release.ID = "ai-only-release"
	manager, store, _ := newPublicationManager(t, []catalog.CatalogRecord{first, second})
	snapshot := activatePublicationSnapshot(t, manager)
	// A valid bundle may contain AI Catalog entries without an ARD counterpart.
	snapshot.ARD.Entries = slices.DeleteFunc(snapshot.ARD.Entries, func(entry Entry) bool { return entry.Metadata[catalogReleaseIDMetadata] == second.Release.ID })
	rehashSnapshotDocuments(t, &snapshot)
	if err := manager.validateSnapshotStructure(snapshot); err != nil {
		t.Fatal("valid AI-only control", err)
	}
	store.snapshots[memorySnapshotKey("tenant-1", snapshot.ID)] = snapshot
	mutable := &observedPublicationRecordSource{fail: true}
	manager.records = mutable
	before, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	for _, releaseID := range []string{first.Release.ID, second.Release.ID} {
		preview, matched, err := manager.PreviewReleaseWithdrawal(t.Context(), "tenant-1", releaseID, &WithdrawalPreviewBudget{})
		if err != nil || !matched || preview.SnapshotID != snapshot.ID || preview.Host != snapshot.Host || preview.Audience != string(snapshot.Audience) || preview.Generation != snapshot.Generation || preview.ETag != snapshot.ETag {
			t.Fatal(preview, err)
		}
	}
	if preview, matched, err := manager.PreviewReleaseWithdrawal(t.Context(), "tenant-1", "absent", &WithdrawalPreviewBudget{}); err != nil || matched || preview.SnapshotID != "" {
		t.Fatal("verified absence", preview, err)
	}
	after, err := store.Active(t.Context(), "tenant-1")
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(after)
	if err != nil || string(encoded) != string(before) {
		t.Fatal("preview changed active evidence", err)
	}
	if mutable.calls != 0 {
		t.Fatal("preview consulted mutable Catalog records")
	}
	if changed, err := manager.WithdrawRelease(t.Context(), "tenant-1", second.Release.ID, WithdrawalCause{Cause: "release_retired", OriginatingAction: "listing_release.retired", OriginatingTargetID: second.Release.ID}); err != nil || !changed {
		t.Fatal("actual command disagrees with AI-only preview", changed, err)
	}
}

func TestWithdrawalPreviewRefusesCorruptEvidenceAndAggregateWork(t *testing.T) {
	manager, store, _ := newPublicationManager(t, []catalog.CatalogRecord{eligiblePublicationRecord()})
	snapshot := activatePublicationSnapshot(t, manager)
	for _, budget := range []*WithdrawalPreviewBudget{nil, {bytes: MaxWithdrawalPreviewBytes}, {bytes: MaxWithdrawalPreviewBytes - 1}, {work: MaxPublicationWorkUnits}, {work: MaxPublicationWorkUnits - 1}} {
		preview, matched, err := manager.PreviewReleaseWithdrawal(t.Context(), "tenant-1", "release-1", budget)
		want := catalog.CodeWorkLimitExceeded
		if budget == nil {
			want = catalog.CodeInvalidArgument
		}
		if catalog.ErrorCodeOf(err) != want || matched || preview.SnapshotID != "" {
			t.Fatal(preview, err)
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if preview, matched, err := manager.PreviewReleaseWithdrawal(ctx, "tenant-1", "release-1", &WithdrawalPreviewBudget{}); !errors.Is(err, context.Canceled) || matched || preview.SnapshotID != "" {
		t.Fatal(preview, err)
	}
	if preview, matched, err := manager.PreviewReleaseWithdrawal(t.Context(), "other-tenant", "release-1", &WithdrawalPreviewBudget{}); err != nil || matched || preview.SnapshotID != "" {
		t.Fatal("cross-tenant publication read", preview, err)
	}
	// Valid digests isolate structural corruption rather than only a checksum failure.
	snapshot.ARD.Entries = append(snapshot.ARD.Entries, snapshot.ARD.Entries[0])
	rehashSnapshotDocuments(t, &snapshot)
	store.snapshots[memorySnapshotKey("tenant-1", snapshot.ID)] = snapshot
	if preview, matched, err := manager.PreviewReleaseWithdrawal(t.Context(), "tenant-1", "absent", &WithdrawalPreviewBudget{}); catalog.ErrorCodeOf(err) != catalog.CodePublicationNotReady || matched || preview.SnapshotID != "" {
		t.Fatal("corrupt evidence became a false zero", preview, err)
	}
	if _, err := store.Active(t.Context(), "tenant-1"); err != nil {
		t.Fatal("preview withdrew publication", err)
	}
	if changed, err := manager.WithdrawRelease(t.Context(), "tenant-1", "absent", WithdrawalCause{Cause: "release_retired", OriginatingAction: "listing_release.retired", OriginatingTargetID: "absent"}); err != nil || !changed {
		t.Fatal("actual conservative withdrawal changed", changed, err)
	}
}
