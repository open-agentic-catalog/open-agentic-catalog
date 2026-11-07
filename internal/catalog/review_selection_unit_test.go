package catalog

import (
	"errors"
	"fmt"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
)

func pass2CatalogReadID(index int) string {
	return fmt.Sprintf("00000000-0000-7000-8000-%012x", index)
}

// These are retained metadata headers for one repository selection, not a
// workflow that constructs, validates, reviews or publishes an application.
func pass2ReviewSelectionRow(repo *MemoryRepository, index int, submitted, decided time.Time, state SubmissionState) ReviewSubmission {
	resourceID, listingID := pass2CatalogReadID(100000+index), pass2CatalogReadID(200000+index)
	listingRevisionID, versionRevisionID := pass2CatalogReadID(300000+index), pass2CatalogReadID(400000+index)
	repo.resources[resourceID] = Resource{ID: resourceID, TenantID: pass2CatalogTenant, NamespaceID: pass2CatalogNamespace, Kind: KindAgent}
	repo.listings[listingID] = CatalogListing{ID: listingID, TenantID: pass2CatalogTenant, NamespaceID: pass2CatalogNamespace, ResourceID: resourceID, CandidateRevisionID: listingRevisionID}
	repo.listingRevisions[listingRevisionID] = ListingRevision{ID: listingRevisionID, TenantID: pass2CatalogTenant, ListingID: listingID, VersionRevisionID: versionRevisionID}
	row := ReviewSubmission{ID: pass2CatalogReadID(500000 + index), TenantID: pass2CatalogTenant, ListingID: listingID, ListingRevisionID: listingRevisionID, VersionRevisionID: versionRevisionID, SubmittedAt: submitted, State: state}
	if !decided.IsZero() {
		row.DecidedAt = &decided
	}
	repo.submissions[row.ID] = row
	return row
}

func pass2ReviewSelectionRelease(repo *MemoryRepository, row ReviewSubmission, index int, state ReleaseState) {
	release := ListingRelease{ID: pass2CatalogReadID(600000 + index), TenantID: pass2CatalogTenant, ListingID: row.ListingID, SubmissionID: row.ID, ListingRevisionID: row.ListingRevisionID, VersionRevisionID: row.VersionRevisionID, State: state}
	repo.releases[release.ID] = release
}

func TestReviewSelectionUsesHalfOpenActivityAndRetainedReleaseHistory(t *testing.T) {
	t.Parallel()
	ctx, _ := pass2CatalogSelectionContext(t, "resource.read_history")
	repo := NewMemoryRepository()
	start := time.Date(2026, 10, 9, 0, 0, 0, 0, time.UTC)
	end := time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC)
	oldest := start.Add(-24 * time.Hour)
	pass2ReviewSelectionRow(repo, 1, oldest, time.Time{}, SubmissionSubmitted)
	pass2ReviewSelectionRow(repo, 2, start, time.Time{}, SubmissionSubmitted)
	pass2ReviewSelectionRow(repo, 3, oldest, start, SubmissionApproved)
	pass2ReviewSelectionRow(repo, 4, start.Add(time.Hour), end, SubmissionRejected)
	pass2ReviewSelectionRow(repo, 7, end, end, SubmissionRejected)
	retired := pass2ReviewSelectionRow(repo, 5, oldest, start.Add(-time.Microsecond), SubmissionApproved)
	pass2ReviewSelectionRelease(repo, retired, 5, ReleaseRetired)
	revoked := pass2ReviewSelectionRow(repo, 6, start.Add(2*time.Hour), start.Add(3*time.Hour), SubmissionApproved)
	pass2ReviewSelectionRelease(repo, revoked, 6, ReleaseRevoked)
	got, err := repo.ReadReviewTimes(ctx, pass2CatalogTenant, start, end)
	if err != nil {
		t.Fatal(err)
	}
	slices.SortFunc(got.Pending, func(a, b time.Time) int { return a.Compare(b) })
	slices.SortFunc(got.Submitted, func(a, b time.Time) int { return a.Compare(b) })
	slices.SortFunc(got.Decisions, func(a, b ReviewDecisionTime) int { return a.DecidedAt.Compare(b.DecidedAt) })
	want := ReviewTimes{
		Pending: []time.Time{oldest, start}, ApprovedUnreleased: 1, OldestPendingAt: &oldest,
		Submitted: []time.Time{start, start.Add(time.Hour), start.Add(2 * time.Hour)},
		Decisions: []ReviewDecisionTime{{SubmittedAt: oldest, DecidedAt: start, State: SubmissionApproved}, {SubmittedAt: start.Add(2 * time.Hour), DecidedAt: start.Add(3 * time.Hour), State: SubmissionApproved}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("review activity or retained release filter changed: got=%+v want=%+v", got, want)
	}
}

func TestReviewSelectionRequiresCurrentHistoryPermission(t *testing.T) {
	t.Parallel()
	ctx, policy := pass2CatalogSelectionContext(t, "resource.read_history")
	start := time.Date(2026, 10, 9, 0, 0, 0, 0, time.UTC)
	end := start.Add(24 * time.Hour)
	repo := NewMemoryRepository()
	pass2ReviewSelectionRow(repo, 1, start, time.Time{}, SubmissionSubmitted)
	control, err := repo.ReadReviewTimes(ctx, pass2CatalogTenant, start, end)
	if err != nil || len(control.Pending) != 1 || len(control.Submitted) != 1 {
		t.Fatal("valid history-selection control", control, err)
	}
	policy.allowed["resource.read_history"] = false
	got, err := repo.ReadReviewTimes(ctx, pass2CatalogTenant, start, end)
	if err != nil || !reflect.DeepEqual(got, ReviewTimes{}) {
		t.Fatal("history-denied review metadata disclosed", got, err)
	}
	policy.allowed["resource.read_history"] = true
	policy.rowChecks = 0
	got, err = repo.ReadReviewTimes(ctx, pass2CatalogOther, start, end)
	if identity.ErrorCodeOf(err) != identity.CodeForbidden || !reflect.DeepEqual(got, ReviewTimes{}) || policy.rowChecks != 0 {
		t.Fatal("cross-tenant review selection reached row checks", got, err, policy.rowChecks)
	}
}

func TestReviewSelectionPermissionFailureReturnsNoSummary(t *testing.T) {
	t.Parallel()
	for _, after := range []int{0, 1} {
		t.Run(fmt.Sprintf("after-%d-rows", after), func(t *testing.T) {
			ctx, policy := pass2CatalogSelectionContext(t, "resource.read_history")
			start := time.Date(2026, 10, 9, 0, 0, 0, 0, time.UTC)
			end := start.Add(24 * time.Hour)
			repo := NewMemoryRepository()
			pass2ReviewSelectionRow(repo, 1, start, time.Time{}, SubmissionSubmitted)
			pass2ReviewSelectionRow(repo, 2, start.Add(time.Hour), time.Time{}, SubmissionSubmitted)
			if control, err := repo.ReadReviewTimes(ctx, pass2CatalogTenant, start, end); err != nil || len(control.Pending) != 2 || len(control.Submitted) != 2 || control.OldestPendingAt == nil || !control.OldestPendingAt.Equal(start) {
				t.Fatal("valid history-selection control", control, err)
			}
			cause := errors.New("synthetic history-policy failure")
			policy.fail, policy.failure = "resource.read_history", cause
			// One check admits one row, regardless of map iteration order. Fail
			// after accumulation as well as before it, without returning metadata.
			policy.rowChecks, policy.failAfterRowChecks = 0, after
			got, err := repo.ReadReviewTimes(ctx, pass2CatalogTenant, start, end)
			if !errors.Is(err, cause) || !reflect.DeepEqual(got, ReviewTimes{}) || policy.failureCalls != 1 || policy.rowChecks != after+1 {
				t.Fatal("review policy failure became partial success or lost its cause", got, err, policy.failureCalls)
			}
		})
	}
}

func TestReviewSelectionCapsEachPopulationAtOneSentinel(t *testing.T) {
	t.Parallel()
	ctx, _ := pass2CatalogSelectionContext(t, "resource.read_history")
	start := time.Date(2026, 10, 9, 0, 0, 0, 0, time.UTC)
	end := start.Add(24 * time.Hour)
	pendingAt, decidedAt := start.Add(time.Hour), start.Add(2*time.Hour)
	repo := NewMemoryRepository()
	// Pending submissions populate one series. Older submissions decided in
	// this interval independently populate the decision/unreleased series.
	for i := range OverviewSelectionLimit + 2 {
		pass2ReviewSelectionRow(repo, 2*i, pendingAt, time.Time{}, SubmissionSubmitted)
		pass2ReviewSelectionRow(repo, 2*i+1, start.Add(-time.Hour), decidedAt, SubmissionApproved)
	}
	got, err := repo.ReadReviewTimes(ctx, pass2CatalogTenant, start, end)
	if err != nil || len(got.Pending) != OverviewSelectionLimit+1 || len(got.Submitted) != OverviewSelectionLimit+1 || len(got.Decisions) != OverviewSelectionLimit+1 || got.ApprovedUnreleased != OverviewSelectionLimit+1 || got.OldestPendingAt == nil || !got.OldestPendingAt.Equal(pendingAt) {
		t.Fatal("review populations did not retain exactly one independent overflow sentinel", len(got.Pending), len(got.Submitted), len(got.Decisions), got.ApprovedUnreleased, got.OldestPendingAt, err)
	}
	for _, stamp := range got.Pending {
		if !stamp.Equal(pendingAt) {
			t.Fatal("decision-only population entered pending age inputs", stamp)
		}
	}
	for _, stamp := range got.Submitted {
		if !stamp.Equal(pendingAt) {
			t.Fatal("outside-interval submission entered activity inputs", stamp)
		}
	}
	for _, decision := range got.Decisions {
		if decision.State != SubmissionApproved || !decision.SubmittedAt.Equal(start.Add(-time.Hour)) || !decision.DecidedAt.Equal(decidedAt) {
			t.Fatal("decision population lost its distinct input timestamps", decision)
		}
	}
}
