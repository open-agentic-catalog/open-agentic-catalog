package publication

import (
	"context"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
)

const (
	MaxWithdrawalPreviewHosts = 64
	MaxWithdrawalPreviewBytes = 2 * MaxPublicationSnapshotBytes
)

// WithdrawalPreviewBudget bounds one sequential cross-host preview, including
// full retained documents rather than only matching references.
type WithdrawalPreviewBudget struct{ bytes, work int }

// WithdrawalPreviewStore enforces the remaining encoded-byte budget at the
// retained-data boundary, before transferring or decoding an oversized bundle.
type WithdrawalPreviewStore interface {
	ActiveForWithdrawalPreview(context.Context, string, int) (Snapshot, int, error)
}

func (b *WithdrawalPreviewBudget) admit(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if b.bytes >= MaxWithdrawalPreviewBytes || b.work >= MaxPublicationWorkUnits {
		return catalog.E(catalog.CodeWorkLimitExceeded, "withdrawal preview exceeds its aggregate publication work limit")
	}
	return nil
}

// PreviewReleaseWithdrawal shares structural validation and both-profile
// membership with WithdrawRelease. Invalid evidence refuses the entire preview;
// the actual emergency command retains its conservative withdrawal behavior.
func (m *Manager) PreviewReleaseWithdrawal(ctx context.Context, tenantID, releaseID string, budget *WithdrawalPreviewBudget) (catalog.ReleaseWithdrawalSnapshot, bool, error) {
	if budget == nil {
		return catalog.ReleaseWithdrawalSnapshot{}, false, catalog.E(catalog.CodeInvalidArgument, "withdrawal preview budget is required")
	}
	if err := budget.admit(ctx); err != nil {
		return catalog.ReleaseWithdrawalSnapshot{}, false, err
	}
	release, err := m.constructionLimiter.acquire(ctx)
	if err != nil {
		return catalog.ReleaseWithdrawalSnapshot{}, false, err
	}
	defer release()
	store, ok := m.store.(WithdrawalPreviewStore)
	if !ok {
		return catalog.ReleaseWithdrawalSnapshot{}, false, catalog.E(catalog.CodeDependencyUnavailable, "bounded publication withdrawal preview is unavailable")
	}
	snapshot, encodedBytes, err := store.ActiveForWithdrawalPreview(ctx, tenantID, min(MaxPublicationSnapshotBytes, MaxWithdrawalPreviewBytes-budget.bytes))
	if catalog.ErrorCodeOf(err) == catalog.CodeNotFound {
		return catalog.ReleaseWithdrawalSnapshot{}, false, nil
	}
	if err != nil {
		return catalog.ReleaseWithdrawalSnapshot{}, false, err
	}
	if err := ValidateSnapshotBounds(snapshot); err != nil {
		return catalog.ReleaseWithdrawalSnapshot{}, false, err
	}
	budget.bytes += encodedBytes
	budget.work += len(snapshot.ARD.Entries) + len(snapshot.AICatalog.Entries) + len(snapshot.ArtifactIDs) + len(snapshot.Exclusions)
	if budget.work > MaxPublicationWorkUnits {
		return catalog.ReleaseWithdrawalSnapshot{}, false, catalog.E(catalog.CodeWorkLimitExceeded, "withdrawal preview exceeds its aggregate publication work limit")
	}
	if err := ctx.Err(); err != nil {
		return catalog.ReleaseWithdrawalSnapshot{}, false, err
	}
	if snapshot.TenantID != tenantID || snapshot.State != SnapshotActive || m.validateSnapshotStructure(snapshot) != nil {
		return catalog.ReleaseWithdrawalSnapshot{}, false, catalog.E(catalog.CodePublicationNotReady, "active publication membership is invalid; withdrawal may conservatively retire the whole snapshot")
	}
	if !snapshotContainsRelease(snapshot, releaseID) {
		return catalog.ReleaseWithdrawalSnapshot{}, false, nil
	}
	return catalog.ReleaseWithdrawalSnapshot{SnapshotID: snapshot.ID, Host: snapshot.Host, Audience: string(snapshot.Audience), Generation: snapshot.Generation, ETag: snapshot.ETag}, true, nil
}
