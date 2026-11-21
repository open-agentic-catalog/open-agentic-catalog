package catalog

import (
	"context"
	"strings"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
)

// ListingMetadataChecks selects optional declarations on a released ListingRevision.
type ListingMetadataChecks struct {
	Documentation, Support, License bool
}

// ParseListingMetadataChecks accepts a nonempty, unordered set of known fields.
func ParseListingMetadataChecks(value string) (ListingMetadataChecks, error) {
	var checks ListingMetadataChecks
	for name := range strings.SplitSeq(value, ",") {
		switch {
		case name == "documentation" && !checks.Documentation:
			checks.Documentation = true
		case name == "support" && !checks.Support:
			checks.Support = true
		case name == "license" && !checks.License:
			checks.License = true
		default:
			return ListingMetadataChecks{}, E(CodeInvalidArgument, "checks must select documentation, support or license once each")
		}
	}
	return checks, nil
}

// Fields returns the selected names in stable response and cursor-binding order.
func (checks ListingMetadataChecks) Fields() []string {
	var fields []string
	if checks.Documentation {
		fields = append(fields, "documentation")
	}
	if checks.Support {
		fields = append(fields, "support")
	}
	if checks.License {
		fields = append(fields, "license")
	}
	return fields
}

type ListingMetadataQuery struct {
	Checks               ListingMetadataChecks
	Kind                 Kind
	NamespaceID, AfterID string
	Limit                int
}

// ListingMetadataGap identifies the released metadata only. The editor must read its own candidate.
type ListingMetadataGap struct {
	ListingID         string       `json:"listingId"`
	NamespaceID       string       `json:"namespaceId"`
	ResourceID        string       `json:"resourceId"`
	ReleaseID         string       `json:"releaseId"`
	ListingRevisionID string       `json:"listingRevisionId"`
	Kind              Kind         `json:"kind"`
	DisplayName       string       `json:"displayName"`
	Version           string       `json:"version"`
	State             ReleaseState `json:"state"`
	Missing           []string     `json:"missing"`
}

// ListListingMetadataGaps reads one moving page, with a single lookahead row.
func (s *Service) ListListingMetadataGaps(ctx context.Context, q ListingMetadataQuery) ([]ListingMetadataGap, error) {
	ctx, actor, err := collectionContext(ctx)
	if err != nil {
		return nil, err
	}
	if q.Checks == (ListingMetadataChecks{}) || q.Limit < 1 || q.Limit > MaxCatalogPageSize || q.Kind != "" && !q.Kind.Valid() || q.NamespaceID != "" && !identity.ValidTenantID(q.NamespaceID) || q.AfterID != "" && !identity.ValidTenantID(q.AfterID) {
		return nil, E(CodeInvalidArgument, "invalid listing metadata selection")
	}
	q.Limit++
	return s.repo.ListListingMetadataGaps(ctx, actor.TenantID, q)
}
