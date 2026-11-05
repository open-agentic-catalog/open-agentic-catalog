package catalog

import (
	"context"
	"testing"
	"time"
)

func TestReviewOverviewTimeBoundariesAndMedian(t *testing.T) {
	for _, at := range []time.Time{time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC), time.Date(2028, 3, 1, 12, 0, 0, 0, time.FixedZone("test", 3600))} {
		at = at.UTC()
		start := ReviewInterval(at)
		oldest := at.Add(-168 * time.Hour)
		rows := ReviewTimes{Pending: []time.Time{at, at.Add(-24*time.Hour + time.Nanosecond), at.Add(-24 * time.Hour), at.Add(-168*time.Hour + time.Nanosecond), oldest}, OldestPendingAt: &oldest, Submitted: []time.Time{start, at.Add(-time.Nanosecond)}, Decisions: []ReviewDecisionTime{{start.AddDate(0, 0, -30), start, SubmissionApproved}, {at.Add(-4 * time.Second), at.Add(-time.Second), SubmissionChangesRequired}, {at.Add(-time.Second), at.Add(-time.Second), SubmissionRejected}}}
		out := summarizeReviews("tenant", at, rows)
		if out.PendingAge == nil || *out.PendingAge != (ReviewAgeCounts{Under24Hours: 2, Under7Days: 2, AtLeast7Days: 1}) || out.Pending.Value != 5 || len(out.Days) != 30 || out.MedianSeconds == nil || *out.MedianSeconds != 3 || out.Decisions.Value != 3 {
			t.Fatal(out)
		}
		if !out.StartAt.Equal(start) || !out.EndAt.Equal(at) || out.Days[0].Date != start.Format(time.DateOnly) || !out.Days[29].Partial || *out.Days[0].Approved != 1 {
			t.Fatal(out)
		}
		if at.Hour() == 0 && *out.Days[29].Submitted != 0 {
			t.Fatal("exclusive midnight cutoff included an event", out)
		}
	}
	for _, check := range []struct {
		seconds []float64
		want    float64
	}{{[]float64{0}, 0}, {[]float64{2, 6}, 4}, {[]float64{4, 0, 2}, 2}, {[]float64{1.25, 2.75}, 2}} {
		at := time.Date(2026, 12, 31, 12, 0, 0, 0, time.UTC)
		rows := ReviewTimes{}
		for _, seconds := range check.seconds {
			decided := at.Add(-time.Minute)
			rows.Decisions = append(rows.Decisions, ReviewDecisionTime{decided.Add(-time.Duration(seconds * float64(time.Second))), decided, SubmissionApproved})
		}
		out := summarizeReviews("tenant", at, rows)
		if out.MedianSeconds == nil || *out.MedianSeconds != check.want {
			t.Fatal(out, check)
		}
	}
}

func TestReviewOverviewIndependentLimitsAndInvalidTimes(t *testing.T) {
	at := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	oldest := at.Add(-365 * 24 * time.Hour)
	rows := ReviewTimes{Pending: make([]time.Time, OverviewSelectionLimit+1), OldestPendingAt: &oldest, Submitted: make([]time.Time, OverviewSelectionLimit+1), Decisions: []ReviewDecisionTime{{at.Add(-2 * time.Hour), at.Add(-time.Hour), SubmissionRejected}}}
	out := summarizeReviews("tenant", at, rows)
	if !out.Pending.Limited || out.PendingAge != nil || out.PendingAgeUnavailable != "limited" || out.OldestPendingAt == nil || !out.OldestPendingAt.Equal(oldest) || !out.Submissions.Limited || out.Days[0].Submitted != nil || out.MedianSeconds == nil || *out.MedianSeconds != 3600 {
		t.Fatal(out)
	}
	rows = ReviewTimes{Pending: []time.Time{at.Add(time.Second)}, Decisions: []ReviewDecisionTime{{at, at.Add(-time.Second), SubmissionApproved}}}
	out = summarizeReviews("tenant", at, rows)
	if out.PendingAge != nil || out.PendingAgeUnavailable != "invalid_timestamps" || out.MedianSeconds != nil || out.MedianUnavailable != "invalid_timestamps" || out.Decisions.Value != 1 || *out.Days[29].Approved != 1 {
		t.Fatal(out)
	}
	rows.Decisions = make([]ReviewDecisionTime, OverviewSelectionLimit+1)
	out = summarizeReviews("tenant", at, rows)
	if !out.Decisions.Limited || out.MedianUnavailable != "limited" || out.MedianSeconds != nil || out.Days[0].Approved != nil {
		t.Fatal(out)
	}
	out = summarizeReviews("tenant", at, ReviewTimes{})
	if out.MedianSeconds != nil || out.MedianUnavailable != "" || out.PendingAge == nil || out.Decisions.Value != 0 {
		t.Fatal(out)
	}
}

func TestReviewOverviewExactLimitKeepsCompleteDistributions(t *testing.T) {
	at := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	stamp := at.Add(-time.Hour)
	rows := ReviewTimes{Pending: make([]time.Time, OverviewSelectionLimit), OldestPendingAt: &stamp, ApprovedUnreleased: OverviewSelectionLimit, Submitted: make([]time.Time, OverviewSelectionLimit), Decisions: make([]ReviewDecisionTime, OverviewSelectionLimit)}
	for i := range rows.Pending {
		rows.Pending[i], rows.Submitted[i] = stamp, stamp
		rows.Decisions[i] = ReviewDecisionTime{stamp.Add(-time.Second), stamp, SubmissionApproved}
	}
	out := summarizeReviews("tenant", at, rows)
	for _, count := range []BoundedCount{out.Pending, out.ApprovedUnreleased, out.Submissions, out.Decisions} {
		if count.Limited || count.Value != OverviewSelectionLimit {
			t.Fatal("an exact-limit population was reported as incomplete", count)
		}
	}
	if out.PendingAge == nil || out.PendingAge.Under24Hours != OverviewSelectionLimit || out.PendingAgeUnavailable != "" || out.MedianSeconds == nil || *out.MedianSeconds != 1 || out.MedianUnavailable != "" || out.Days[29].Submitted == nil || *out.Days[29].Submitted != OverviewSelectionLimit || out.Days[29].Approved == nil || *out.Days[29].Approved != OverviewSelectionLimit {
		t.Fatal("complete age, activity or timing data was omitted", out)
	}
}

func TestReviewOverviewElapsedSecondsDoNotSaturate(t *testing.T) {
	at := time.Date(2400, 1, 2, 12, 0, 0, 0, time.UTC)
	decided := at.Add(-time.Hour)
	submitted := decided.AddDate(-400, 0, 0)
	out := summarizeReviews("tenant", at, ReviewTimes{Decisions: []ReviewDecisionTime{{submitted, decided, SubmissionApproved}}})
	// Four Gregorian centuries contain 146,097 days, beyond time.Duration's range.
	const want = 146097 * 24 * 60 * 60
	if out.MedianSeconds == nil || *out.MedianSeconds != want || out.MedianUnavailable != "" || out.Decisions.Value != 1 || *out.Days[29].Approved != 1 {
		t.Fatal("a valid long duration was truncated or omitted", out)
	}
}

func TestReviewOverviewUsesCurrentScopedVisibilityAndUnreleasedFilter(t *testing.T) {
	f, _, _, direct, grouped := collectionFixture(t)
	f.clock.now = f.clock.now.Add(time.Minute)
	// Complete the lightweight collection fixture's review/release links.
	for id, review := range f.repo.submissions {
		decided := f.clock.Now().Add(-time.Hour)
		review.State, review.SubmittedAt, review.DecidedAt = SubmissionApproved, decided.Add(-time.Hour), &decided
		f.repo.submissions[id] = review
		listing := f.repo.listings[review.ListingID]
		release := f.repo.releases[listing.CurrentReleaseID]
		release.SubmissionID = id
		f.repo.releases[release.ID] = release
	}
	for _, ctx := range []context.Context{direct, grouped} {
		out, err := f.service.ReviewOverview(ctx)
		if err != nil || out.Pending.Value != 0 || out.ApprovedUnreleased.Value != 0 || out.Decisions.Value != 2 || out.Submissions.Value != 2 {
			t.Fatal(out, err)
		}
		all, err := f.service.ListSubmissions(ctx, SubmissionListRequest{HistoryPageRequest: HistoryPageRequest{Limit: 100}, State: SubmissionApproved})
		if err != nil || len(all) != 2 {
			t.Fatal(all, err)
		}
		filtered, err := f.service.ListSubmissions(ctx, SubmissionListRequest{HistoryPageRequest: HistoryPageRequest{Limit: 100}, State: SubmissionApproved, Unreleased: true})
		if err != nil || len(filtered) != 0 {
			t.Fatal(filtered, err)
		}
		if _, err = f.service.ListSubmissions(ctx, SubmissionListRequest{HistoryPageRequest: HistoryPageRequest{Limit: 1}, State: SubmissionSubmitted, Unreleased: true}); ErrorCodeOf(err) != CodeInvalidArgument {
			t.Fatal(err)
		}
	}
}
