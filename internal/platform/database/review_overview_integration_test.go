//go:build integration

package database

import (
	"crypto/sha256"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/adapter"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/clock"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/id"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/publication"
)

func TestPostgresReviewOverviewLongHistoryKeepsFixedQueryWork(t *testing.T) {
	db, repo, counts, seed := newSearchCostFixture(t)
	seed(0)
	author := semanticDisclosureActor(t, db, housekeepingTenantA)
	key := sha256.Sum256([]byte(t.Name()))
	svc, err := catalog.NewService(catalog.Options{Repository: repo, Clock: clock.Real{}, IDs: id.Generator{}, Validator: adapter.NewRegistry(), CursorKey: key[:], Policy: "search-cost-test"})
	if err != nil {
		t.Fatal(err)
	}
	counts.reset()
	before, err := svc.ReviewOverview(author)
	if err != nil || before.Decisions.Value != 1 {
		t.Fatal(before, err)
	}
	baseline := counts.counts
	// Populate only this disposable database. Follow both lifecycle transitions
	// and retain valid same-tenant parents and sealed evidence. No guard is disabled.
	if _, err := db.operator.Exec(t.Context(), `DO $fixture$
DECLARE template record; selected uuid; n integer;
BEGIN
 SELECT tenant_id,listing_id,version_revision_id,listing_revision_id,evidence_set_id,evidence_digest,policy_version,submitted_by,submitted_at,decided_at INTO STRICT template FROM review_submissions WHERE tenant_id='00000000-0000-7000-8000-000000000801' LIMIT 1;
 FOR n IN 1..10000 LOOP
  selected := gen_random_uuid();
  INSERT INTO review_submissions(id,tenant_id,listing_id,version_revision_id,listing_revision_id,evidence_set_id,evidence_digest,policy_version,state,submitted_by,revision,submitted_at)
  VALUES(selected,template.tenant_id,template.listing_id,template.version_revision_id,template.listing_revision_id,template.evidence_set_id,template.evidence_digest,template.policy_version,'submitted',template.submitted_by,1,template.submitted_at);
  UPDATE review_submissions SET state='changes_required',revision=2,decided_at=template.decided_at,
   decisions=jsonb_build_array(jsonb_build_object('actor','search-reviewer','action','request_changes','reason','Synthetic retained history','at',template.decided_at))
   WHERE tenant_id=template.tenant_id AND id=selected;
 END LOOP;
END $fixture$`); err != nil {
		t.Fatal(err)
	}
	counts.reset()
	after, err := svc.ReviewOverview(author)
	if err != nil || after.Decisions.Value != 10000 || !after.Decisions.Limited || !after.Submissions.Limited || after.MedianSeconds != nil || after.MedianUnavailable != "limited" || len(after.Days) != 30 {
		t.Fatal(after, err)
	}
	if !reflect.DeepEqual(baseline, counts.counts) {
		t.Fatal("history size changed query work", baseline, counts.counts)
	}
	for _, day := range after.Days {
		if day.Submitted != nil || day.Approved != nil || day.ChangesRequired != nil || day.Rejected != nil {
			t.Fatal("partial history presented as a complete daily total", day)
		}
	}
}

func TestPostgresReviewOverviewAndUnreleasedQueueFollowRealLifecycle(t *testing.T) {
	db, repo, _, seed := newSearchCostFixture(t)
	seed(0)
	author := semanticDisclosureActor(t, db, housekeepingTenantA)
	key := sha256.Sum256([]byte(t.Name()))
	svc, err := catalog.NewService(catalog.Options{Repository: repo, Clock: clock.Real{}, IDs: id.Generator{}, Validator: adapter.NewRegistry(), CursorKey: key[:], Policy: "search-cost-test"})
	if err != nil {
		t.Fatal(err)
	}
	resources, err := svc.ListResources(author, catalog.ResourceListRequest{Limit: 2})
	if err != nil || len(resources) != 1 {
		t.Fatal(resources, err)
	}
	listings, err := svc.ListListings(author, resources[0].ID, catalog.HistoryPageRequest{Limit: 2})
	if err != nil || len(listings) != 1 {
		t.Fatal(listings, err)
	}
	listing := listings[0]
	released, err := svc.GetRelease(author, listing.CurrentReleaseID)
	if err != nil {
		t.Fatal(err)
	}
	metadata, err := svc.GetListingRevision(author, released.ListingRevisionID)
	if err != nil {
		t.Fatal(err)
	}
	listing, _, err = svc.CreateListingRevision(author, listing.ID, catalog.CreateListingRevisionInput{VersionRevisionID: released.VersionRevisionID, DisplayName: "Review overview", Description: "Synthetic lifecycle fixture", PublisherID: metadata.PublisherID, Visibility: catalog.VisibilityTenant}, listing.Revision)
	if err != nil {
		t.Fatal(err)
	}
	submission, err := svc.Submit(author, listing.ID)
	if err != nil {
		t.Fatal(err)
	}
	result, err := svc.ReviewOverview(author)
	if err != nil || result.Pending.Value != 1 || result.ApprovedUnreleased.Value != 0 || result.Submissions.Value != 2 || result.Decisions.Value != 1 || result.PendingAge == nil || result.PendingAge.Under24Hours != 1 || result.OldestPendingAt == nil {
		var earliest, latest *time.Time
		var decisions int
		evidenceErr := db.operator.QueryRow(t.Context(), "SELECT count(*),min(decided_at),max(decided_at) FROM review_submissions WHERE tenant_id=$1 AND decided_at IS NOT NULL", housekeepingTenantA).Scan(&decisions, &earliest, &latest)
		t.Logf("review window evidence: end=%s decisions=%d earliest=%v latest=%v querySucceeded=%t", result.EndAt.Format(time.RFC3339Nano), decisions, earliest, latest, evidenceErr == nil)
		t.Fatal(result.Pending, result.ApprovedUnreleased, result.Submissions, result.Decisions, err)
	}
	roles := []identity.Role{identity.RoleReviewer}
	principal, err := ProvisionIdentity(t.Context(), db.operator, IdentityProvisioningInput{TenantID: housekeepingTenantA, TenantDisplayName: "Housekeeping tenant A", Issuer: "https://identity.performance.example", TenancyMode: "dedicated", Subject: "search-reviewer", ActorType: identity.ActorHuman, Roles: roles, Operator: "overview-test", Reason: "reuse the independent synthetic reviewer", AcknowledgeOutbox: true})
	if err != nil {
		t.Fatal(err)
	}
	reviewer := postgresFixtureContext(t, db, t.Context(), identity.Actor{TenantID: housekeepingTenantA, Subject: "search-reviewer", Roles: roles, MembershipID: principal.MembershipID, RealmID: principal.RealmID, ActorType: principal.ActorType})
	if _, _, err = svc.DecideSubmission(reviewer, submission.ID, "approve", "synthetic overview approval", submission.Revision); err != nil {
		t.Fatal(err)
	}
	result, err = svc.ReviewOverview(author)
	if err != nil || result.Pending.Value != 0 || result.ApprovedUnreleased.Value != 1 || result.Decisions.Value != 2 || result.MedianSeconds == nil {
		t.Fatal(result.Pending, result.ApprovedUnreleased, result.Decisions, err)
	}
	queue, err := svc.ListSubmissions(author, catalog.SubmissionListRequest{HistoryPageRequest: catalog.HistoryPageRequest{Limit: 1}, State: catalog.SubmissionApproved, Unreleased: true})
	if err != nil || len(queue) != 1 || queue[0].ID != submission.ID {
		t.Fatal(queue, err)
	}
	all, err := svc.ListSubmissions(author, catalog.SubmissionListRequest{HistoryPageRequest: catalog.HistoryPageRequest{Limit: 10}, State: catalog.SubmissionApproved})
	if err != nil || len(all) != 2 {
		t.Fatal(all, err)
	}
	_, newRelease, _, err := svc.Publish(author, listing.ID, catalog.PublishInput{SubmissionID: submission.ID}, listing.Revision)
	if err != nil {
		t.Fatal(err)
	}
	result, err = svc.ReviewOverview(author)
	if err != nil || result.Pending.Value != 0 || result.ApprovedUnreleased.Value != 0 || result.Decisions.Value != 2 {
		t.Fatal("publication did not clear the unreleased overview count", result, err)
	}
	if _, err = svc.TransitionRelease(author, newRelease.ID, catalog.ReleaseRetired, "retire synthetic overview release", "", nil, newRelease.Revision); err != nil {
		t.Fatal(err)
	}
	queue, err = svc.ListSubmissions(author, catalog.SubmissionListRequest{HistoryPageRequest: catalog.HistoryPageRequest{Limit: 10}, State: catalog.SubmissionApproved, Unreleased: true})
	if err != nil || len(queue) != 0 {
		t.Fatal("retirement restored an already released submission", queue, err)
	}
	result, err = svc.ReviewOverview(author)
	if err != nil || result.ApprovedUnreleased.Value != 0 || result.Decisions.Value != 2 {
		t.Fatal("retirement restored the unreleased overview count", result, err)
	}
	other := semanticDisclosureActor(t, db, housekeepingTenantB)
	result, err = svc.ReviewOverview(other)
	if err != nil || result.Pending.Value != 0 || result.Decisions.Value != 0 || result.ApprovedUnreleased.Value != 0 {
		t.Fatal("review metadata crossed tenants", err)
	}
}

func TestPostgresPublicationOverviewUsesConfiguredHostSet(t *testing.T) {
	db, repo, _, seed := newSearchCostFixture(t)
	seed(0)
	author := semanticDisclosureActor(t, db, housekeepingTenantA)
	key := sha256.Sum256([]byte(t.Name()))
	svc, err := catalog.NewService(catalog.Options{Repository: repo, Clock: clock.Real{}, IDs: id.Generator{}, Validator: adapter.NewRegistry(), CursorKey: key[:], Policy: "search-cost-test"})
	if err != nil {
		t.Fatal(err)
	}
	store, err := NewPostgresPublicationStore(repo, "catalog.example.com")
	if err != nil {
		t.Fatal(err)
	}
	manager, err := publication.NewManager(publication.Options{Records: svc, Store: store, Clock: clock.Real{}, IDs: id.Generator{}, BaseURL: "https://catalog.example.com", PublisherDomain: "catalog.example.com", Host: "catalog.example.com", Audience: publication.AudienceAnonymous, ARDProfile: "ard-test", AICatalogProfile: "ai-test", CursorKey: key[:]})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := manager.BuildCandidate(author, housekeepingTenantA)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = manager.Activate(author, housekeepingTenantA, snapshot.ID, "activate synthetic overview publication"); err != nil {
		t.Fatal(err)
	}
	configs := []publication.HostConfiguration{{Host: "catalog.example.com", Mode: "authenticated", ARDProfile: "ard-test", AICatalogProfile: "ai-test"}, {Host: "missing.example", Mode: "anonymous", ARDProfile: "ard-test", AICatalogProfile: "ai-test"}, {Host: "disabled.example", Mode: "disabled"}}
	out, err := publication.ReadOverview(author, configs, repo, time.Now().UTC())
	if err != nil || len(out.Hosts) != 3 || out.Hosts[0].Status != "recorded" || out.Hosts[0].Active.ID != snapshot.ID || len(out.Hosts[0].Mismatches) != 1 || out.Hosts[1].Status != "disabled" || out.Hosts[2].Status != "unpublished" {
		t.Fatal(out, err)
	}
	maximum := append([]publication.HostConfiguration(nil), configs...)
	for i := len(maximum); i < 64; i++ {
		maximum = append(maximum, publication.HostConfiguration{Host: fmt.Sprintf("z-host-%02d.example", i), Mode: "anonymous", ARDProfile: "ard-test", AICatalogProfile: "ai-test"})
	}
	bounded, err := publication.ReadOverview(author, maximum, repo, time.Now().UTC())
	if err != nil || len(bounded.Hosts) != 64 || bounded.Hosts[0].Active.ID != snapshot.ID {
		t.Fatal(bounded, err)
	}
	rows, err := repo.ReadActiveSummaries(author, housekeepingTenantA, []string{"missing.example"})
	if err != nil || len(rows) != 0 {
		t.Fatal("unconfigured active host leaked", rows, err)
	}
	if _, err = repo.ReadActiveSummaries(author, housekeepingTenantB, []string{"catalog.example.com"}); catalog.ErrorCodeOf(err) != catalog.CodeForbidden {
		t.Fatal("cross-tenant arguments were accepted", err)
	}
	other := semanticDisclosureActor(t, db, housekeepingTenantB)
	rows, err = repo.ReadActiveSummaries(other, housekeepingTenantB, []string{"catalog.example.com"})
	if err != nil || len(rows) != 0 {
		t.Fatal("active metadata crossed tenants", rows, err)
	}
	second, err := manager.BuildCandidate(author, housekeepingTenantA)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = manager.Activate(author, housekeepingTenantA, second.ID, "activate second synthetic publication"); err != nil {
		t.Fatal(err)
	}
	out, err = publication.ReadOverview(author, configs, repo, time.Now().UTC())
	if err != nil || out.SelectionLimit != 64 || out.Hosts[0].Active.ID != second.ID {
		t.Fatal(out, err)
	}
	if _, err = manager.Rollback(author, housekeepingTenantA, snapshot.ID, "restore first synthetic publication"); err != nil {
		t.Fatal(err)
	}
	out, err = publication.ReadOverview(author, configs, repo, time.Now().UTC())
	if err != nil || out.Hosts[0].Active.ID != snapshot.ID {
		t.Fatal(out, err)
	}
	if _, err = manager.Unpublish(author, housekeepingTenantA, "withdraw synthetic publication"); err != nil {
		t.Fatal(err)
	}
	out, err = publication.ReadOverview(author, configs, repo, time.Now().UTC())
	if err != nil || out.Hosts[0].Status != "unpublished" || out.Hosts[0].Active != nil {
		t.Fatal(out, err)
	}
}
