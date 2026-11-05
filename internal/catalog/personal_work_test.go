package catalog

import (
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"testing"
)

func TestMyWorkExactAttributionQueuesAndRetainedReleases(t *testing.T) {
	f, _, _, direct, grouped := collectionFixture(t)
	actor, _ := identity.FromContext(direct)
	own := workOwner{actor.TenantID, actor.MembershipID, actor.RealmID}
	f.repo.workOwners = map[string]workOwner{}
	// Exact immutable submission attribution; CreatedBy/SubmittedBy alone never grant ownership.
	for _, submission := range f.repo.submissions {
		submission.State = SubmissionApproved
		l := f.repo.listings[submission.ListingID]
		for _, revision := range f.repo.listingRevisions {
			if revision.ListingID == l.ID {
				l.CandidateRevisionID = revision.ID
				submission.ListingRevisionID = revision.ID
				submission.VersionRevisionID = revision.VersionRevisionID
			}
		}
		f.repo.listings[l.ID] = l
		f.repo.submissions[submission.ID] = submission
		f.repo.workOwners[workAttributionKey("review_submission", submission.ID, "")] = own
	}
	// Existing fixture releases refer to its template submission, so only the
	// deliberately bound exact retained release below must exclude a row.
	rows, err := f.service.ListPersonalWork(direct, PersonalWorkRequest{Queue: "approved", Limit: 1})
	if err != nil || len(rows) != 2 {
		t.Fatal(rows, err)
	}
	next, err := f.service.ListPersonalWork(direct, PersonalWorkRequest{Queue: "approved", AfterID: rows[0].ID, Limit: 1})
	if err != nil || len(next) != 1 || next[0].ID != rows[1].ID {
		t.Fatal(next, err)
	}
	if got, e := f.service.ListPersonalWork(grouped, PersonalWorkRequest{Queue: "approved", Limit: 1}); e != nil || len(got) != 0 {
		t.Fatal("other caller attributed", got, e)
	}
	id := rows[0].SubmissionID
	s := f.repo.submissions[id]
	l := f.repo.listings[s.ListingID]
	l.CandidateRevisionID = "newer-candidate"
	f.repo.listings[l.ID] = l
	rows, err = f.service.ListPersonalWork(direct, PersonalWorkRequest{Queue: "approved", Limit: 100})
	if err != nil || len(rows) != 2 {
		t.Fatal("new candidate revoked approval", rows, err)
	}
	for _, state := range []ReleaseState{ReleasePublished, ReleaseDeprecated, ReleaseRetired, ReleaseSuperseded, ReleaseRevoked} {
		f.repo.releases["retained-release"] = ListingRelease{ID: "retained-release", TenantID: actor.TenantID, SubmissionID: id, State: state}
		got, e := f.service.ListPersonalWork(direct, PersonalWorkRequest{Queue: "approved", Limit: 100})
		if e != nil || len(got) != 1 {
			t.Fatal("retained release not excluded", state, got, e)
		}
	}
	delete(f.repo.releases, "retained-release")
	s.State = SubmissionChangesRequired
	f.repo.submissions[id] = s
	changed, e := f.service.ListPersonalWork(direct, PersonalWorkRequest{Queue: "changes_required", Limit: 100})
	if e != nil || len(changed) != 1 || changed[0].CurrentCandidateRevisionID != "newer-candidate" || changed[0].ListingRevisionID != s.ListingRevisionID {
		t.Fatal(changed, e)
	}
	later := s
	later.ID = "later-submission"
	later.State = SubmissionSubmitted
	later.SubmittedAt = later.SubmittedAt.Add(1)
	f.repo.submissions[later.ID] = later
	if got, e := f.service.ListPersonalWork(direct, PersonalWorkRequest{Queue: "changes_required", Limit: 100}); e != nil || len(got) != 0 {
		t.Fatal("resubmitted correction still pending", got, e)
	}
	// A same-subject attribution in another realm is never guessed into this queue.
	f.repo.workOwners[workAttributionKey("review_submission", later.ID, "")] = workOwner{own.Tenant, own.Member, collectionHiddenNamespace}
	if got, e := f.service.ListPersonalWork(direct, PersonalWorkRequest{Queue: "submitted", Limit: 100}); e != nil || len(got) != 0 {
		t.Fatal("realm escaped", got, e)
	}
}

func TestMyWorkRevisionAttributionAndDraftPointers(t *testing.T) {
	f, _, _, direct, _ := collectionFixture(t)
	a, _ := identity.FromContext(direct)
	own := workOwner{a.TenantID, a.MembershipID, a.RealmID}
	f.repo.workOwners = map[string]workOwner{}
	r := f.repo.resources["00000000-0000-7000-8000-000000000101"]
	empty := r
	empty.ID = "00000000-0000-7000-8000-000000000901"
	f.repo.resources[empty.ID] = empty
	f.repo.workOwners[workAttributionKey("resource", empty.ID, "")] = own
	version := f.repo.versions["00000000-0000-7000-8000-000000000201"]
	version.SealedRevisionID = ""
	f.repo.versions[version.ID] = version
	// Old revision evidence without exact revisionId is deliberately omitted.
	f.repo.indexWorkAudit(AuditEvent{TenantID: a.TenantID, Actor: a.MembershipID, Action: "resource_version.revised", TargetType: "resource_version", TargetID: version.ID, Details: map[string]any{"realmId": a.RealmID, "actorType": "human", "principalType": "catalog_membership"}})
	got, e := f.service.ListPersonalWork(direct, PersonalWorkRequest{Queue: "drafts", Limit: 100})
	if e != nil || len(got) != 1 || got[0].ResourceID != empty.ID {
		t.Fatal(got, e)
	}
	f.repo.workOwners[workAttributionKey("resource_version", version.ID, version.CandidateRevisionID)] = own
	got, e = f.service.ListPersonalWork(direct, PersonalWorkRequest{Queue: "drafts", Limit: 100})
	if e != nil || len(got) != 2 {
		t.Fatal(got, e)
	}
	version.CandidateRevisionID = "different-revision"
	f.repo.versions[version.ID] = version
	got, e = f.service.ListPersonalWork(direct, PersonalWorkRequest{Queue: "drafts", Limit: 100})
	if e != nil || len(got) != 1 {
		t.Fatal("previous revision ownership reused", got, e)
	}
	listing := f.repo.listings["00000000-0000-7000-8000-000000000401"]
	listing.CandidateRevisionID = "00000000-0000-7000-8000-000000000501"
	f.repo.listings[listing.ID] = listing
	f.repo.workOwners[workAttributionKey("catalog_listing", listing.ID, listing.CandidateRevisionID)] = own
	got, e = f.service.ListPersonalWork(direct, PersonalWorkRequest{Queue: "drafts", Limit: 100})
	if e != nil || len(got) != 2 {
		t.Fatal(got, e)
	}
	for _, row := range got {
		if row.Type == "candidate" && (row.CurrentCandidateRevisionID != listing.CandidateRevisionID || row.ListingRevisionID != listing.CandidateRevisionID) {
			t.Fatal("candidate pointer projection drift", row)
		}
	}
}
