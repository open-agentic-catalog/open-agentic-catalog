package catalog

import (
	"context"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
)

// ReleaseWithdrawalSnapshot identifies an observed active discovery bundle.
// Withdrawing it removes the whole bundle, not just the selected release.
type ReleaseWithdrawalSnapshot struct {
	SnapshotID string `json:"snapshotId"`
	Host       string `json:"host"`
	Audience   string `json:"audience"`
	Generation int64  `json:"generation"`
	ETag       string `json:"etag"`
}

// ReleaseWithdrawalPreview is an advisory read, never a command reservation.
type ReleaseWithdrawalPreview struct {
	ReleaseID       string                      `json:"releaseId"`
	ReleaseRevision int64                       `json:"releaseRevision"`
	Transition      ReleaseState                `json:"transition"`
	ObservedAt      time.Time                   `json:"observedAt"`
	Snapshots       []ReleaseWithdrawalSnapshot `json:"snapshots"`
}

// PublicationWithdrawalPreviewer reads the same configured publication surfaces
// as release withdrawal, without changing pointers or producing evidence.
type PublicationWithdrawalPreviewer interface {
	PreviewPublicationRelease(context.Context, string, string) ([]ReleaseWithdrawalSnapshot, error)
}

// PreviewReleaseWithdrawal composes existing exact release-history and tenant
// publication-read admission. It does not relax final transition checks.
func (s *Service) PreviewReleaseWithdrawal(ctx context.Context, releaseID string, target ReleaseState) (ReleaseWithdrawalPreview, error) {
	ctx = identity.WithPermissionContext(ctx)
	actor, err := requirePermission(ctx, "publication.read", "")
	if err != nil {
		return ReleaseWithdrawalPreview{}, err
	}
	if target != ReleaseRetired && target != ReleaseSuperseded && target != ReleaseRevoked {
		return ReleaseWithdrawalPreview{}, E(CodeInvalidArgument, "transition must be retired, superseded or revoked; deprecation does not withdraw discovery")
	}
	release, err := s.GetRelease(ctx, releaseID)
	if err != nil {
		return ReleaseWithdrawalPreview{}, err
	}
	if !validReleaseTransition(release.State, target) {
		return ReleaseWithdrawalPreview{}, E(CodeInvalidTransition, "release transition is not permitted")
	}
	previewer, ok := s.publicationWithdrawer.(PublicationWithdrawalPreviewer)
	if !ok {
		return ReleaseWithdrawalPreview{}, E(CodeDependencyUnavailable, "publication withdrawal preview is unavailable")
	}
	snapshots, err := previewer.PreviewPublicationRelease(ctx, actor.TenantID, releaseID)
	if err != nil {
		return ReleaseWithdrawalPreview{}, err
	}
	if snapshots == nil {
		snapshots = []ReleaseWithdrawalSnapshot{}
	}
	return ReleaseWithdrawalPreview{ReleaseID: release.ID, ReleaseRevision: release.Revision, Transition: target, ObservedAt: s.clock.Now(), Snapshots: snapshots}, nil
}
