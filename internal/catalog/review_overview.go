package catalog

import (
	"context"
	"math"
	"sort"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
)

type ReviewAgeCounts struct {
	Under24Hours int `json:"under24Hours"`
	Under7Days   int `json:"under7Days"`
	AtLeast7Days int `json:"atLeast7Days"`
}

type ReviewDay struct {
	Date            string `json:"date"`
	Partial         bool   `json:"partial"`
	Submitted       *int   `json:"submitted,omitempty"`
	Approved        *int   `json:"approved,omitempty"`
	ChangesRequired *int   `json:"changesRequired,omitempty"`
	Rejected        *int   `json:"rejected,omitempty"`
}

type ReviewOverview struct {
	OverviewScope
	Pending               BoundedCount     `json:"pending"`
	ApprovedUnreleased    BoundedCount     `json:"approvedUnreleased"`
	OldestPendingAt       *time.Time       `json:"oldestPendingAt,omitempty"`
	PendingAge            *ReviewAgeCounts `json:"pendingAge,omitempty"`
	PendingAgeUnavailable string           `json:"pendingAgeUnavailable,omitempty"`
	StartAt               time.Time        `json:"startAt"`
	EndAt                 time.Time        `json:"endAt"`
	Submissions           BoundedCount     `json:"submissions"`
	Decisions             BoundedCount     `json:"decisions"`
	Days                  []ReviewDay      `json:"days"`
	MedianSeconds         *float64         `json:"medianSeconds,omitempty"`
	MedianUnavailable     string           `json:"medianUnavailable,omitempty"`
}

// ReviewTimes contains metadata only. Each population has its own sentinel.
type ReviewTimes struct {
	Pending            []time.Time
	ApprovedUnreleased int
	OldestPendingAt    *time.Time
	Submitted          []time.Time
	Decisions          []ReviewDecisionTime
}
type ReviewDecisionTime struct {
	SubmittedAt, DecidedAt time.Time
	State                  SubmissionState
}
type ReviewOverviewReader interface {
	ReadReviewTimes(context.Context, string, time.Time, time.Time) (ReviewTimes, error)
}

func ReviewInterval(at time.Time) time.Time {
	at = at.UTC()
	return time.Date(at.Year(), at.Month(), at.Day(), 0, 0, 0, 0, time.UTC).AddDate(0, 0, -29)
}

func (s *Service) ReviewOverview(ctx context.Context) (ReviewOverview, error) {
	ctx, actor, err := collectionContext(ctx)
	if err != nil {
		return ReviewOverview{}, err
	}
	reader, ok := s.repo.(ReviewOverviewReader)
	if !ok {
		return ReviewOverview{}, E(CodeDependencyUnavailable, "review summary is unavailable")
	}
	at := s.clock.Now().UTC()
	rows, err := reader.ReadReviewTimes(ctx, actor.TenantID, ReviewInterval(at), at)
	if err != nil {
		return ReviewOverview{}, err
	}
	return summarizeReviews(actor.TenantID, at, rows), nil
}

func summarizeReviews(tenant string, at time.Time, rows ReviewTimes) ReviewOverview {
	start := ReviewInterval(at)
	out := ReviewOverview{OverviewScope: NewOverviewScope(tenant, at), Pending: OverviewCount(len(rows.Pending)), ApprovedUnreleased: OverviewCount(rows.ApprovedUnreleased), OldestPendingAt: rows.OldestPendingAt, StartAt: start, EndAt: at, Submissions: OverviewCount(len(rows.Submitted)), Decisions: OverviewCount(len(rows.Decisions)), Days: make([]ReviewDay, 30)}
	if out.Pending.Limited {
		out.PendingAgeUnavailable = "limited"
	} else {
		ages := ReviewAgeCounts{}
		for _, stamp := range rows.Pending {
			if stamp.IsZero() || stamp.After(at) {
				out.PendingAgeUnavailable = "invalid_timestamps"
				break
			}
			switch age := at.Sub(stamp); {
			case age < 24*time.Hour:
				ages.Under24Hours++
			case age < 168*time.Hour:
				ages.Under7Days++
			default:
				ages.AtLeast7Days++
			}
		}
		if out.PendingAgeUnavailable == "" {
			out.PendingAge = &ages
		}
	}
	for i := range out.Days {
		out.Days[i] = ReviewDay{Date: start.AddDate(0, 0, i).Format(time.DateOnly), Partial: i == 29}
		if !out.Submissions.Limited {
			out.Days[i].Submitted = new(int)
		}
		if !out.Decisions.Limited {
			out.Days[i].Approved = new(int)
			out.Days[i].ChangesRequired = new(int)
			out.Days[i].Rejected = new(int)
		}
	}
	index := func(stamp time.Time) int { return int(stamp.UTC().Sub(start) / (24 * time.Hour)) }
	if !out.Submissions.Limited {
		for _, stamp := range rows.Submitted {
			if !stamp.Before(start) && stamp.Before(at) {
				(*out.Days[index(stamp)].Submitted)++
			}
		}
	}
	if out.Decisions.Limited {
		out.MedianUnavailable = "limited"
		return out
	}
	durations := make([]float64, 0, len(rows.Decisions))
	for _, decision := range rows.Decisions {
		if decision.DecidedAt.Before(start) || !decision.DecidedAt.Before(at) {
			out.MedianUnavailable = "invalid_timestamps"
			continue
		}
		day := &out.Days[index(decision.DecidedAt)]
		switch decision.State {
		case SubmissionApproved:
			(*day.Approved)++
		case SubmissionChangesRequired:
			(*day.ChangesRequired)++
		case SubmissionRejected:
			(*day.Rejected)++
		case SubmissionSubmitted:
			out.MedianUnavailable = "invalid_timestamps"
		default:
			out.MedianUnavailable = "invalid_timestamps"
		}
		if decision.SubmittedAt.IsZero() || decision.DecidedAt.Before(decision.SubmittedAt) {
			out.MedianUnavailable = "invalid_timestamps"
			continue
		}
		seconds := float64(decision.DecidedAt.Unix()-decision.SubmittedAt.Unix()) + float64(decision.DecidedAt.Nanosecond()-decision.SubmittedAt.Nanosecond())/1e9
		if math.IsNaN(seconds) || math.IsInf(seconds, 0) || seconds < 0 {
			out.MedianUnavailable = "invalid_timestamps"
			continue
		}
		durations = append(durations, seconds)
	}
	if out.MedianUnavailable == "" && len(durations) > 0 {
		sort.Float64s(durations)
		n := len(durations)
		median := durations[n/2]
		if n%2 == 0 {
			median = (durations[n/2-1] + median) / 2
		}
		out.MedianSeconds = &median
	}
	return out
}

func (m *MemoryRepository) submissionReleasedLocked(tenant, submission string) bool {
	for _, release := range m.releases {
		if release.TenantID == tenant && release.SubmissionID == submission {
			return true
		}
	}
	return false
}

func (m *MemoryRepository) ReadReviewTimes(ctx context.Context, tenant string, start, end time.Time) (ReviewTimes, error) {
	if err := identity.RequireCollectionTenant(ctx, tenant); err != nil {
		return ReviewTimes{}, err
	}
	if identity.NeedsCollectionAdmission(ctx) {
		return identity.WithCollectionRead(ctx, func(bound context.Context) (ReviewTimes, error) { return m.ReadReviewTimes(bound, tenant, start, end) })
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := ReviewTimes{}
	for _, review := range m.submissions {
		listing, ok := m.listings[review.ListingID]
		if !ok || listing.TenantID != tenant || review.TenantID != tenant {
			continue
		}
		resource, ok := m.resources[listing.ResourceID]
		if !ok || resource.TenantID != tenant || resource.DeletedAt != nil {
			continue
		}
		allowed, err := identity.CollectionAllows(ctx, "resource.read_history", listing.NamespaceID)
		if err != nil {
			return ReviewTimes{}, err
		}
		if !allowed {
			continue
		}
		if review.State == SubmissionSubmitted {
			if len(out.Pending) <= OverviewSelectionLimit {
				out.Pending = append(out.Pending, review.SubmittedAt)
			}
			if out.OldestPendingAt == nil || review.SubmittedAt.Before(*out.OldestPendingAt) {
				stamp := review.SubmittedAt
				out.OldestPendingAt = &stamp
			}
		}
		if review.State == SubmissionApproved && out.ApprovedUnreleased <= OverviewSelectionLimit && !m.submissionReleasedLocked(tenant, review.ID) {
			out.ApprovedUnreleased++
		}
		if !review.SubmittedAt.Before(start) && review.SubmittedAt.Before(end) && len(out.Submitted) <= OverviewSelectionLimit {
			out.Submitted = append(out.Submitted, review.SubmittedAt)
		}
		if review.DecidedAt != nil && !review.DecidedAt.Before(start) && review.DecidedAt.Before(end) && len(out.Decisions) <= OverviewSelectionLimit {
			out.Decisions = append(out.Decisions, ReviewDecisionTime{review.SubmittedAt, *review.DecidedAt, review.State})
		}
	}
	return out, nil
}
