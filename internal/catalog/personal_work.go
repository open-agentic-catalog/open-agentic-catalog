// Personal work is a current projection, never another lifecycle authority.
package catalog

import (
	"context"
	"strings"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
)

type PersonalWorkRequest struct {
	Queue, AfterID string
	Limit          int
}
type PersonalWorkItem struct {
	ID                         string `json:"id"`
	Type                       string `json:"type"`
	ResourceID                 string `json:"resourceId"`
	NamespaceID                string `json:"namespaceId"`
	Kind                       Kind   `json:"kind"`
	CanonicalIdentifier        string `json:"canonicalIdentifier,omitempty"`
	DisplayName                string `json:"displayName,omitempty"`
	VersionID                  string `json:"versionId,omitempty"`
	VersionRevisionID          string `json:"versionRevisionId,omitempty"`
	ListingID                  string `json:"listingId,omitempty"`
	ListingRevisionID          string `json:"listingRevisionId,omitempty"`
	SubmissionID               string `json:"submissionId,omitempty"`
	State                      string `json:"state"`
	CurrentCandidateRevisionID string `json:"currentCandidateRevisionId,omitempty"`
}
type PersonalWorkReader interface {
	ListPersonalWork(context.Context, string, PersonalWorkRequest) ([]PersonalWorkItem, error)
}

func (s *Service) ListPersonalWork(ctx context.Context, q PersonalWorkRequest) ([]PersonalWorkItem, error) {
	ctx, a, err := collectionContext(ctx)
	if err != nil {
		return nil, err
	}
	if a.ActorType != identity.ActorHuman || !identity.ValidTenantID(a.MembershipID) || !identity.ValidTenantID(a.RealmID) {
		return nil, E(CodeForbidden, "personal work requires a human membership")
	}
	switch q.Queue {
	case "drafts", "submitted", "changes_required", "approved":
	default:
		return nil, E(CodeInvalidArgument, "personal work queue is invalid")
	}
	if q.Limit < 1 || q.Limit > 100 || len(q.AfterID) > 80 || strings.ContainsRune(q.AfterID, 0) {
		return nil, E(CodeInvalidArgument, "personal work page is invalid")
	}
	q.Limit++
	reader, ok := s.repo.(PersonalWorkReader)
	if !ok {
		return nil, E(CodeDependencyUnavailable, "personal work is unavailable")
	}
	return reader.ListPersonalWork(ctx, a.TenantID, q)
}

// ValidateSavedSearch checks criteria without executing a query or disclosing it.
func ValidateSavedSearch(q SearchQuery) error {
	if q.Cursor != "" || q.Limit != 0 {
		return E(CodeInvalidArgument, "saved criteria cannot contain continuation or page size")
	}
	return validateCatalogSearchQuery(q)
}
