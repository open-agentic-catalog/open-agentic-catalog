package personal

import (
	"context"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
)

// ListingID is optional in a mixed library item. If present on the wire, even
// an empty string must pass the same UUID check as an interactive follow.
type ListingID string

func (id *ListingID) UnmarshalText(value []byte) error {
	if !identity.ValidTenantID(string(value)) {
		return catalog.E(catalog.CodeInvalidArgument, "invalid followed Listing ID")
	}
	*id = ListingID(value)
	return nil
}

// RetainedInput is private storage. The acknowledgement is deliberately absent
// from owner item responses, backups and command receipts.
type RetainedInput struct {
	Input
	Acknowledged *catalog.ReleaseObservation `json:"acknowledged,omitempty"`
}

func (in RetainedInput) Validate() error {
	if in.Acknowledged != nil && (in.Type != "follow" || !validObservation(*in.Acknowledged)) {
		return catalog.E(catalog.CodeInvalidArgument, "invalid retained follow acknowledgement")
	}
	return Validate(in.Input)
}

func validObservation(o catalog.ReleaseObservation) bool {
	return identity.ValidTenantID(o.ReleaseID) && o.Revision > 0
}

func (s *Service) current(ctx context.Context, listingID string) (catalog.ReleaseObservation, error) {
	o, err := s.authority.CurrentListingRelease(ctx, listingID)
	if catalog.ErrorCodeOf(err) == catalog.CodeForbidden || catalog.ErrorCodeOf(err) == catalog.CodeNotFound {
		return catalog.ReleaseObservation{}, catalog.E(catalog.CodeNotFound, "followed Listing is unavailable")
	}
	return o, err
}

type FollowResolution struct {
	Status  string                      `json:"status"`
	Current *catalog.ReleaseObservation `json:"current,omitempty"`
	Change  string                      `json:"change,omitempty"`
}

func (s *Service) ResolveFollow(ctx context.Context, itemID string) (FollowResolution, error) {
	ctx = identity.WithPermissionContext(ctx)
	item, err := s.Get(ctx, itemID)
	if err != nil {
		return FollowResolution{}, err
	}
	if item.Type != "follow" {
		return FollowResolution{}, catalog.E(catalog.CodeInvalidArgument, "expected a follow")
	}
	current, err := s.current(ctx, string(item.ListingID))
	if catalog.ErrorCodeOf(err) == catalog.CodeNotFound {
		return FollowResolution{Status: "unavailable"}, nil
	}
	if err != nil {
		return FollowResolution{}, err
	}
	change := "unacknowledged"
	if item.Acknowledged != nil {
		change = "changed"
		if *item.Acknowledged == current {
			change = "unchanged"
		}
	}
	return FollowResolution{Status: "available", Current: &current, Change: change}, nil
}

// NeedsCurrentTarget distinguishes follow admission and acknowledgement from
// owner-only rename and removal, even when the retained observation is unchanged.
func (i Item) NeedsCurrentTarget() bool { return i.checkedCurrent != nil }
