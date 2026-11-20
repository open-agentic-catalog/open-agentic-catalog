package catalog

import (
	"context"
	"strconv"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
)

// RelationshipView selects the declaration's direction or the selected release's dependency impact.
type RelationshipView string

const (
	OutgoingRelationships RelationshipView = "outgoing"
	IncomingRelationships RelationshipView = "incoming"
	ReleaseDependencies   RelationshipView = "dependencies"
)

type RelationshipPageRequest struct {
	View           RelationshipView
	Limit          int
	AfterReleaseID string
	AfterOrdinal   int
	Generation     string
}

// RelatedRelease identifies an independently readable current release and its sealed Listing metadata.
type RelatedRelease struct {
	ReleaseID         string `json:"releaseId"`
	ListingRevisionID string `json:"listingRevisionId"`
	DisplayName       string `json:"displayName"`
	Version           string `json:"version"`
}

type ReleasedRelationship struct {
	Declaration Relationship `json:"declaration"`
	// Related is the source on an incoming page, or the eligible target on an outgoing page.
	Related         *RelatedRelease `json:"related,omitempty"`
	SourceReleaseID string          `json:"-"`
	Ordinal         int             `json:"-"`
}

type RelationshipSelection struct {
	Items      []ReleasedRelationship
	Generation string
}

// ListReleaseRelationships selects both endpoints against the same current policy.
func (s *Service) ListReleaseRelationships(ctx context.Context, releaseID string, request RelationshipPageRequest) (RelationshipSelection, error) {
	ctx, actor, err := collectionContext(ctx)
	if err != nil {
		return RelationshipSelection{}, err
	}
	if request.View != OutgoingRelationships && request.View != IncomingRelationships && request.View != ReleaseDependencies {
		return RelationshipSelection{}, E(CodeInvalidArgument, "view must be outgoing, incoming or dependencies")
	}
	if request.Limit < 1 || request.Limit > 100 {
		return RelationshipSelection{}, E(CodeInvalidArgument, "relationship page limit must be between 1 and 100")
	}
	return s.repo.ListReleaseRelationships(ctx, actor.TenantID, releaseID, request)
}

// RelationshipGeneration binds the observed release population and the policy actually used for selection.
func RelationshipGeneration(ctx context.Context, generation string) string {
	revision, _ := identity.CollectionRevision(ctx)
	return strconv.FormatInt(revision, 10) + "/" + generation
}
