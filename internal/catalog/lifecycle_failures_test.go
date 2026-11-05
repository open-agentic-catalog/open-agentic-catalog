package catalog

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"
)

const lifecycleSubmissionID = "00000000-0000-7000-8000-000000000020"
const lifecycleListingID = "00000000-0000-7000-8000-000000000021"

// This repository supplies only the records needed to prepare a lifecycle
// command. It records the command handoff; it does not simulate persistence.
type lifecycleReadFaults struct {
	Repository
	fail            string
	err             error
	reads           []string
	writes          []string
	listing         CatalogListing
	listingRevision ListingRevision
	version         ResourceVersion
	revision        VersionRevision
	evidence        EvidenceSet
	submission      ReviewSubmission
	resource        Resource
	publisher       Publisher
}

func (r *lifecycleReadFaults) read(phase, tenant, id, expectedID string) error {
	r.reads = append(r.reads, phase)
	if tenant != r.listing.TenantID || id != expectedID {
		return errors.New("lost tenant or record binding")
	}
	if r.fail == phase {
		return r.err
	}
	return nil
}
func (r *lifecycleReadFaults) GetListing(_ context.Context, tenant, id string) (CatalogListing, error) {
	return r.listing, r.read("listing", tenant, id, r.listing.ID)
}
func (r *lifecycleReadFaults) GetListingRevision(_ context.Context, tenant, id string) (ListingRevision, error) {
	return r.listingRevision, r.read("listing revision", tenant, id, r.listingRevision.ID)
}
func (r *lifecycleReadFaults) GetVersion(_ context.Context, tenant, id string) (ResourceVersion, error) {
	return r.version, r.read("version", tenant, id, r.version.ID)
}
func (r *lifecycleReadFaults) GetVersionRevision(_ context.Context, tenant, id string) (VersionRevision, EvidenceSet, error) {
	return r.revision, r.evidence, r.read("revision", tenant, id, r.revision.ID)
}
func (r *lifecycleReadFaults) GetValidationEvidence(_ context.Context, tenant, revision, id string) (EvidenceSet, error) {
	if revision != r.revision.ID {
		return EvidenceSet{}, errors.New("lost evidence revision binding")
	}
	return r.evidence, r.read("evidence", tenant, id, r.evidence.ID)
}
func (r *lifecycleReadFaults) GetSubmission(_ context.Context, tenant, id string) (ReviewSubmission, error) {
	return r.submission, r.read("submission", tenant, id, r.submission.ID)
}
func (r *lifecycleReadFaults) GetOriginatingPrincipal(_ context.Context, tenant, kind, target string) (OriginatingPrincipal, error) {
	if kind != "review_submission" {
		return OriginatingPrincipal{}, errors.New("lost origin binding")
	}
	return OriginatingPrincipal{TenantID: tenant, MembershipID: "00000000-0000-7000-8000-000000000094", RealmID: guardRealm, Subject: r.submission.SubmittedBy}, r.read("origin", tenant, target, r.submission.ID)
}
func (r *lifecycleReadFaults) GetResource(_ context.Context, tenant, id string) (Resource, error) {
	return r.resource, r.read("resource", tenant, id, r.resource.ID)
}
func (r *lifecycleReadFaults) GetPublisher(_ context.Context, tenant, id string) (Publisher, error) {
	return r.publisher, r.read("publisher", tenant, id, r.publisher.ID)
}
func (r *lifecycleReadFaults) GetRelease(_ context.Context, tenant, id string) (ListingRelease, error) {
	return ListingRelease{ID: id}, r.read("release", tenant, id, r.listing.CurrentReleaseID)
}
func (r *lifecycleReadFaults) Submit(_ context.Context, _ SubmitParams) (ReviewSubmission, error) {
	r.writes = append(r.writes, "submit")
	return ReviewSubmission{}, r.err
}
func (r *lifecycleReadFaults) DecideSubmission(_ context.Context, _ DecideSubmissionParams) (ReviewSubmission, ResourceVersion, error) {
	r.writes = append(r.writes, "decide")
	return ReviewSubmission{}, ResourceVersion{}, r.err
}
func (r *lifecycleReadFaults) Publish(_ context.Context, _ PublishParams) (CatalogListing, ListingRelease, *ListingRelease, error) {
	r.writes = append(r.writes, "publish")
	return CatalogListing{}, ListingRelease{}, nil, r.err
}

func (r *lifecycleReadFaults) CreateListing(_ context.Context, _ CreateListingParams) (CatalogListing, error) {
	r.writes = append(r.writes, "create listing")
	return CatalogListing{}, r.err
}

func (r *lifecycleReadFaults) CreateVersion(_ context.Context, _ CreateVersionParams) (ResourceVersion, VersionRevision, EvidenceSet, error) {
	r.writes = append(r.writes, "create version")
	return ResourceVersion{}, VersionRevision{}, EvidenceSet{}, r.err
}

func (r *lifecycleReadFaults) CreateVersionRevision(_ context.Context, _ CreateVersionParams) (ResourceVersion, VersionRevision, EvidenceSet, error) {
	r.writes = append(r.writes, "revise version")
	return ResourceVersion{}, VersionRevision{}, EvidenceSet{}, r.err
}

func (r *lifecycleReadFaults) CreateListingRevision(_ context.Context, _ CreateListingRevisionParams) (CatalogListing, ListingRevision, error) {
	r.writes = append(r.writes, "revise listing")
	return CatalogListing{}, ListingRevision{}, r.err
}

func lifecycleFailureFixture(t *testing.T, operation string) (guardrailFixture, *lifecycleReadFaults) {
	t.Helper()
	f := newGuardrailFixture(t)
	r := &lifecycleReadFaults{err: errors.New("lifecycle repository failed")}
	r.listing = CatalogListing{ID: "listing", TenantID: guardrailTenantID, NamespaceID: "namespace", ResourceID: "resource", CandidateRevisionID: "listing-revision", Revision: 1}
	r.listingRevision = ListingRevision{ID: "listing-revision", VersionRevisionID: "revision", Visibility: VisibilityPublic, RepresentativeQueries: []string{"find agent", "show agent"}, PublisherID: "publisher"}
	r.version = ResourceVersion{ID: "version", Revision: 1}
	r.revision = VersionRevision{ID: "revision", ResourceVersionID: "version", ContentSetDigest: "content"}
	r.evidence = EvidenceSet{ID: "evidence", SubjectDigest: "content", Digest: "evidence-digest", State: EvidenceComplete, ValidationProfile: currentValidationProfile, PolicyVersion: f.service.policy}
	r.submission = ReviewSubmission{ID: lifecycleSubmissionID, ListingID: "listing", ListingRevisionID: "listing-revision", VersionRevisionID: "revision", EvidenceSetID: "evidence", EvidenceDigest: "evidence-digest", PolicyVersion: f.service.policy, SubmittedBy: "another-author", State: SubmissionSubmitted, Revision: 1}
	if operation == "publish" {
		r.submission.State = SubmissionApproved
		r.listing.CurrentReleaseID = "prior-release"
	}
	r.resource = Resource{ID: "resource", CanonicalIdentifier: "urn:air:catalog.example.com:default:agent"}
	r.publisher = Publisher{ID: "publisher", ApprovedPublicationDomains: []string{"catalog.example.com"}}
	if operation == "create listing" || operation == "revise listing" {
		r.listing.ID = lifecycleListingID
		r.resource.TenantID, r.resource.NamespaceID = guardrailTenantID, r.listing.NamespaceID
		r.version.ResourceID = r.resource.ID
	}
	if operation == "create version" || operation == "revise version" {
		r.version.ID = "00000000-0000-7000-8000-000000000022"
		r.version.ResourceID, r.version.Version, r.version.CandidateRevisionID = r.resource.ID, "1.0", r.revision.ID
		r.resource.NamespaceID, r.resource.Kind = r.listing.NamespaceID, KindAgent
	}
	f.service.repo = r
	return f, r
}

func callFailingLifecycle(f guardrailFixture, operation string) error {
	switch operation {
	case "create version":
		_, _, _, err := f.service.CreateVersion(f.adminContext(), "resource", CreateVersionInput{Version: "1.0", Representations: []RepresentationInput{{Role: RepresentationPrimary, MediaType: "application/json", Data: json.RawMessage(`{"name":"Example"}`)}}})
		return err
	case "revise version":
		_, _, _, err := f.service.CreateVersionRevision(f.adminContext(), "00000000-0000-7000-8000-000000000022", []RepresentationInput{{Role: RepresentationPrimary, MediaType: "application/json", Data: json.RawMessage(`{"name":"Example"}`)}}, 1)
		return err
	case "create listing":
		_, err := f.service.CreateListing(f.adminContext(), CreateListingInput{NamespaceID: "namespace", ResourceID: "resource"})
		return err
	case "revise listing":
		_, _, err := f.service.CreateListingRevision(f.adminContext(), lifecycleListingID, CreateListingRevisionInput{VersionRevisionID: "revision", PublisherID: "publisher", DisplayName: "Example", Description: "A governed listing", Visibility: VisibilityTenant}, 1)
		return err
	case "submit":
		_, err := f.service.Submit(f.adminContext(), "listing")
		return err
	case "decide":
		_, _, err := f.service.DecideSubmission(f.adminContext(), lifecycleSubmissionID, "approve", "reviewed", 1)
		return err
	default:
		_, _, _, err := f.service.Publish(f.adminContext(), "listing", PublishInput{SubmissionID: lifecycleSubmissionID}, 1)
		return err
	}
}

func TestLifecyclePreparationStopsAtEachRepositoryFailure(t *testing.T) {
	t.Parallel()
	for operation, reads := range map[string][]string{
		"create version": {"resource"},
		"revise version": {"version", "resource", "revision"},
		"create listing": {"resource"},
		"revise listing": {"listing", "revision", "version", "resource", "publisher", "listing revision"},
		"submit":         {"listing", "listing revision", "revision"},
		"decide":         {"submission", "listing", "origin", "revision", "evidence", "version"},
		"publish":        {"listing", "submission", "revision", "evidence", "listing revision", "resource", "publisher", "release"},
	} {
		for failAt := 0; failAt <= len(reads); failAt++ {
			t.Run(fmt.Sprintf("%s/%d", operation, failAt), func(t *testing.T) {
				f, repo := lifecycleFailureFixture(t, operation)
				wantReads := reads
				if failAt < len(reads) {
					repo.fail = reads[failAt]
					wantReads = reads[:failAt+1]
				}
				if err := callFailingLifecycle(f, operation); !errors.Is(err, repo.err) {
					t.Fatalf("lost failure at %q: %v", repo.fail, err)
				}
				if !reflect.DeepEqual(repo.reads, wantReads) {
					t.Fatalf("reads=%v want=%v", repo.reads, wantReads)
				}
				if failAt < len(reads) && len(repo.writes) != 0 || failAt == len(reads) && !reflect.DeepEqual(repo.writes, []string{operation}) {
					t.Fatalf("unexpected write handoff=%v", repo.writes)
				}
			})
		}
	}
}

func TestLifecycleIdentifierFailuresNeverReachMutation(t *testing.T) {
	t.Parallel()
	for _, operation := range []string{"submit", "decide", "publish", "create listing", "revise listing", "create version", "revise version"} {
		count := 3 // New object, audit and outbox identifiers.
		if operation == "decide" {
			count = 2
		}
		if operation == "create version" {
			count = 6 // Version, revision, representation, validation, audit and outbox.
		}
		if operation == "revise version" {
			count = 5 // A revision retains its existing version identifier.
		}
		for failAt := 1; failAt <= count; failAt++ {
			t.Run(fmt.Sprintf("%s/%d", operation, failAt), func(t *testing.T) {
				f, repo := lifecycleFailureFixture(t, operation)
				calls := 0
				sentinel := errors.New("identifier failed")
				f.service.ids = auditIDFunc(func(time.Time) (string, error) {
					calls++
					if calls == failAt {
						return "", sentinel
					}
					return fmt.Sprintf("00000000-0000-7000-8000-%012d", calls), nil
				})
				if err := callFailingLifecycle(f, operation); !errors.Is(err, sentinel) || ErrorCodeOf(err) != CodeInternal || calls != failAt || len(repo.writes) != 0 {
					t.Fatalf("identifier failure reached write: error=%v calls=%d writes=%v", err, calls, repo.writes)
				}
			})
		}
	}
}

func TestLifecycleMissingApprovedEvidenceHasSafeDomainMeaning(t *testing.T) {
	t.Parallel()
	for _, operation := range []string{"decide", "publish"} {
		f, repo := lifecycleFailureFixture(t, operation)
		repo.fail, repo.err = "evidence", E(CodeNotFound, "stored evidence absent")
		want := CodePreconditionFailed
		if operation == "publish" {
			want = CodePublicationNotReady
		}
		if err := callFailingLifecycle(f, operation); ErrorCodeOf(err) != want || len(repo.writes) != 0 {
			t.Fatalf("%s missing evidence=%v writes=%v", operation, err, repo.writes)
		}
	}
}
