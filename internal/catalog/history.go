// History reads authorize the selected parent before requesting bounded pages
// of immutable revisions, submissions and releases.

package catalog

import (
	"context"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
)

// HistoryPageRequest uses immutable UUID ordering, independent of later edits.
// Services accept 1..100 items and repositories receive one extra sentinel.
type HistoryPageRequest struct {
	AfterID string
	Limit   int
}

type SubmissionListRequest struct {
	HistoryPageRequest
	ListingID  string
	State      SubmissionState
	Unreleased bool
}

// RevisionSummary omits representation bodies and private validation evidence.
// The digest is the version content-set digest or listing revision digest.
type RevisionSummary struct {
	ID             string    `json:"id"`
	RevisionNumber int       `json:"revisionNumber"`
	Digest         string    `json:"digest"`
	CreatedBy      string    `json:"createdBy"`
	CreatedAt      time.Time `json:"createdAt"`
}

// historyPage validates the client page and adds one repository sentinel row
// for continuation detection.
func historyPage(request HistoryPageRequest) (HistoryPageRequest, error) {
	if request.Limit < 1 || request.Limit > MaxCatalogPageSize || request.AfterID != "" && !identity.ValidTenantID(request.AfterID) {
		return request, E(CodeInvalidArgument, "history page position or limit is invalid")
	}
	request.Limit++
	return request, nil
}

// ListVersions returns an authorized resource's version page, including a
// possible continuation sentinel.
func (s *Service) ListVersions(ctx context.Context, resourceID string, request HistoryPageRequest) ([]ResourceVersion, error) {
	ctx = identity.WithPermissionContext(ctx)
	resource, err := s.resourceHistory(ctx, resourceID)
	if err != nil {
		return nil, err
	}
	request, err = historyPage(request)
	if err != nil {
		return nil, err
	}
	return s.repo.ListVersions(ctx, resource.TenantID, resourceID, request)
}

// ListVersionRevisions returns bounded revision summaries for an authorized
// version without representation bodies.
func (s *Service) ListVersionRevisions(ctx context.Context, versionID string, request HistoryPageRequest) ([]RevisionSummary, error) {
	ctx = identity.WithPermissionContext(ctx)
	actor, err := requireActor(ctx)
	if err != nil {
		return nil, err
	}
	version, err := s.repo.GetVersion(ctx, actor.TenantID, versionID)
	if err == nil {
		_, err = s.resourceHistory(ctx, version.ResourceID)
	}
	if err != nil {
		return nil, err
	}
	request, err = historyPage(request)
	if err != nil {
		return nil, err
	}
	return s.repo.ListVersionRevisions(ctx, version.TenantID, versionID, request)
}

// ListListings returns an authorized resource's listings with one possible
// continuation sentinel.
func (s *Service) ListListings(ctx context.Context, resourceID string, request HistoryPageRequest) ([]CatalogListing, error) {
	ctx = identity.WithPermissionContext(ctx)
	resource, err := s.resourceHistory(ctx, resourceID)
	if err != nil {
		return nil, err
	}
	request, err = historyPage(request)
	if err != nil {
		return nil, err
	}
	return s.repo.ListListings(ctx, resource.TenantID, resourceID, request)
}

// ListListingRevisions returns bounded immutable summaries for an authorized
// listing.
func (s *Service) ListListingRevisions(ctx context.Context, listingID string, request HistoryPageRequest) ([]RevisionSummary, error) {
	ctx = identity.WithPermissionContext(ctx)
	listing, err := s.listingHistory(ctx, listingID)
	if err != nil {
		return nil, err
	}
	request, err = historyPage(request)
	if err != nil {
		return nil, err
	}
	return s.repo.ListListingRevisions(ctx, listing.TenantID, listingID, request)
}

// ListSubmissions validates optional listing and state filters before reading
// the actor's tenant review queue.
func (s *Service) ListSubmissions(ctx context.Context, request SubmissionListRequest) ([]ReviewSubmission, error) {
	ctx, actor, err := collectionContext(ctx)
	if err != nil {
		return nil, err
	}
	if request.ListingID != "" {
		if !identity.ValidTenantID(request.ListingID) {
			return nil, E(CodeInvalidArgument, "listingId must be a UUID")
		}
		if _, err := s.listingHistory(ctx, request.ListingID); err != nil {
			return nil, err
		}
	}
	switch request.State {
	case "", SubmissionSubmitted, SubmissionApproved, SubmissionChangesRequired, SubmissionRejected:
	default:
		return nil, E(CodeInvalidArgument, "review state is invalid")
	}
	if request.Unreleased && request.State != SubmissionApproved {
		return nil, E(CodeInvalidArgument, "unreleased requires the approved review state")
	}
	request.HistoryPageRequest, err = historyPage(request.HistoryPageRequest)
	if err != nil {
		return nil, err
	}
	return s.repo.ListSubmissions(ctx, actor.TenantID, request)
}

// ListReleases returns an authorized listing's bounded release history.
func (s *Service) ListReleases(ctx context.Context, listingID string, request HistoryPageRequest) ([]ListingRelease, error) {
	ctx = identity.WithPermissionContext(ctx)
	listing, err := s.listingHistory(ctx, listingID)
	if err != nil {
		return nil, err
	}
	request, err = historyPage(request)
	if err != nil {
		return nil, err
	}
	return s.repo.ListReleases(ctx, listing.TenantID, listingID, request)
}

// GetSubmissionEvidence returns the immutable evidence bound by submission,
// not the newest validation run for its version revision.
func (s *Service) GetSubmissionEvidence(ctx context.Context, submissionID string) (EvidenceSet, error) {
	ctx = identity.WithPermissionContext(ctx)
	actor, err := requireActor(ctx)
	if err != nil {
		return EvidenceSet{}, err
	}
	submission, err := s.repo.GetSubmission(ctx, actor.TenantID, submissionID)
	if err != nil {
		return EvidenceSet{}, err
	}
	// Reuse the existing fail-closed private-version-read audit boundary. The
	// latest evidence returned there is deliberately not used for this response.
	if _, _, _, err := s.GetVersionRevision(ctx, submission.VersionRevisionID); err != nil {
		return EvidenceSet{}, err
	}
	evidence, err := s.repo.GetValidationEvidence(ctx, submission.TenantID, submission.VersionRevisionID, submission.EvidenceSetID)
	if err != nil {
		return EvidenceSet{}, err
	}
	if evidence.Digest != submission.EvidenceDigest || evidence.PolicyVersion != submission.PolicyVersion {
		return EvidenceSet{}, E(CodeConflict, "submission evidence binding does not match")
	}
	return evidence, nil
}

func (s *Service) resourceHistory(ctx context.Context, id string) (Resource, error) {
	a, err := requireActor(ctx)
	if err != nil {
		return Resource{}, err
	}
	r, err := s.repo.GetResource(ctx, a.TenantID, id)
	if err != nil {
		return Resource{}, err
	}
	if _, err := requirePermission(ctx, "resource.read_history", r.NamespaceID); err != nil {
		return Resource{}, err
	}
	return r, nil
}
func (s *Service) listingHistory(ctx context.Context, id string) (CatalogListing, error) {
	a, err := requireActor(ctx)
	if err != nil {
		return CatalogListing{}, err
	}
	r, err := s.repo.GetListing(ctx, a.TenantID, id)
	if err != nil {
		return CatalogListing{}, err
	}
	if _, err := requirePermission(ctx, "resource.read_history", r.NamespaceID); err != nil {
		return CatalogListing{}, err
	}
	return r, nil
}
