package database

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
)

// These fixtures exercise adapter decisions and driver handoffs, not SQL semantics.
type listingWriteFixture struct {
	version    catalog.CreateVersionParams
	listing    catalog.CatalogListing
	revision   catalog.ListingRevision
	submission catalog.ReviewSubmission
	release    catalog.ListingRelease
}

type listingReadFaultTx struct {
	*identityScriptedTx
	failAt, calls int
	err           error
}

func (tx *listingReadFaultTx) QueryRow(ctx context.Context, query string, args ...any) pgx.Row {
	row := tx.identityScriptedTx.QueryRow(ctx, query, args...)
	tx.calls++
	if tx.calls == tx.failAt {
		return scannerFunc(func(...any) error { return tx.err })
	}
	return row
}

func TestListingLifecycleReadFailuresStopBeforeDependentWrites(t *testing.T) {
	t.Parallel()
	// The advisory-lock request precedes the final publication read. Submission
	// and review read failures are covered alongside their guards in a sibling test.
	for operation, writesBeforeRead := range map[string][]int{
		"create": {0}, "revise": {0, 0}, "publish": {0, 0, 1}, "adopt": {0, 0},
	} {
		for index, wantWrites := range writesBeforeRead {
			t.Run(fmt.Sprintf("%s/%d", operation, index), func(t *testing.T) {
				f := newListingWriteFixture(t)
				base := &identityScriptedTx{fakeCommandTx: &fakeCommandTx{}}
				tx := &listingReadFaultTx{identityScriptedTx: base, failAt: index + 1, err: errors.New("read failed")}
				pool := &pgxpool.Pool{}
				repo, err := NewPostgresRepository(pool)
				if err != nil {
					t.Fatal(err)
				}
				ctx := identityRepositoryContext(pool, tx)
				err = f.run(t, ctx, repo, base, operation, false)
				if !errors.Is(err, tx.err) || tx.calls != index+1 || len(tx.statements) != wantWrites || tx.rollbacks != 1 || tx.commits != 0 {
					t.Fatalf("error=%v reads=%d writes=%d rollback=%d", err, tx.calls, len(tx.statements), tx.rollbacks)
				}
			})
		}
	}
}

func newListingWriteFixture(t *testing.T) listingWriteFixture {
	t.Helper()
	v := versionWriteParams(t, false)
	l := catalog.CatalogListing{ID: "00000000-0000-7000-8000-000000000021", TenantID: v.Version.TenantID, NamespaceID: "namespace", ResourceID: v.Version.ResourceID, Revision: 1, CreatedAt: v.Version.CreatedAt, UpdatedAt: v.Version.CreatedAt}
	r := catalog.ListingRevision{ID: "00000000-0000-7000-8000-000000000022", TenantID: l.TenantID, ListingID: l.ID, VersionRevisionID: v.VersionRevision.ID, RevisionNumber: 1, DisplayName: "Example", Description: "Example listing", Tags: []string{"test"}, Categories: []string{"tools"}, RepresentativeQueries: []string{"find example"}, PublisherID: "publisher", DocumentationURL: "https://example.com/docs", SupportURL: "https://example.com/support", License: "Apache-2.0", TermsURL: "https://example.com/terms", Visibility: catalog.VisibilityPublic, Digest: "digest", CreatedBy: "author", CreatedAt: l.CreatedAt}
	l.CandidateRevisionID = r.ID
	s := catalog.ReviewSubmission{ID: "00000000-0000-7000-8000-000000000023", TenantID: l.TenantID, ListingID: l.ID, ListingRevisionID: r.ID, VersionRevisionID: v.VersionRevision.ID, EvidenceSetID: v.Evidence.ID, EvidenceDigest: v.Evidence.Digest, PolicyVersion: v.Evidence.PolicyVersion, State: catalog.SubmissionSubmitted, SubmittedBy: "author", Revision: 1, SubmittedAt: l.CreatedAt}
	release := catalog.ListingRelease{ID: "00000000-0000-7000-8000-000000000024", TenantID: l.TenantID, ListingID: l.ID, SubmissionID: s.ID, VersionRevisionID: s.VersionRevisionID, ListingRevisionID: r.ID, State: catalog.ReleasePublished, ActivatedBy: "publisher", Revision: 1, ActivatedAt: l.CreatedAt, UpdatedAt: l.CreatedAt}
	return listingWriteFixture{v, l, r, s, release}
}

func listingWriteRow(l catalog.CatalogListing) pgx.Row {
	return scriptedValueRow([]any{l.ID, l.TenantID, l.NamespaceID, l.ResourceID, l.CandidateRevisionID, l.CurrentReleaseID, l.Revision, l.CreatedAt, l.UpdatedAt})
}

func submissionWriteRow(s catalog.ReviewSubmission) pgx.Row {
	return scriptedValueRow([]any{s.ID, s.TenantID, s.ListingID, s.VersionRevisionID, s.ListingRevisionID, s.EvidenceSetID, s.EvidenceDigest, s.PolicyVersion, s.State, s.SubmittedBy, []byte(`[]`), s.Revision, s.SubmittedAt, s.DecidedAt})
}

// Every operation starts from a successful baseline. Injecting each driver error
// must stop subsequent writes, request rollback, and leave commit to the command.
func TestListingLifecycleWriteFailures(t *testing.T) {
	t.Parallel()
	for _, operation := range []string{"create", "revise", "submit", "approve", "reject", "request_changes", "publish", "adopt"} {
		t.Run(operation, func(t *testing.T) {
			f := newListingWriteFixture(t)
			stages := map[string][]string{
				"create":          {"INSERT INTO catalog_listings", "audit_events", "outbox_events"},
				"revise":          {"INSERT INTO listing_revisions", "UPDATE catalog_listings", "audit_events", "outbox_events"},
				"submit":          {"pg_advisory_xact_lock", "INSERT INTO review_submissions", "audit_events", "outbox_events"},
				"approve":         {"pg_advisory_xact_lock", "UPDATE resource_versions", "UPDATE review_submissions", "audit_events", "outbox_events"},
				"reject":          {"pg_advisory_xact_lock", "UPDATE review_submissions", "audit_events", "outbox_events"},
				"request_changes": {"pg_advisory_xact_lock", "UPDATE review_submissions", "audit_events", "outbox_events"},
				"publish":         {"pg_advisory_xact_lock", "INSERT INTO listing_releases", "UPDATE catalog_listings", "audit_events", "outbox_events"},
				"adopt":           {"INSERT INTO resources", "INSERT INTO resource_versions", "document_blobs", "evidence_sets", "resource_version_revisions", "version_validation_runs", "representations", "INSERT INTO catalog_listings", "INSERT INTO listing_revisions", "audit_events", "outbox_events"},
			}[operation]
			for failAt := -1; failAt < len(stages); failAt++ {
				t.Run(fmt.Sprintf("failure=%d", failAt), func(t *testing.T) {
					tx := &identityScriptedTx{fakeCommandTx: &fakeCommandTx{}}
					sentinel := errors.New("driver failure")
					if failAt >= 0 {
						tx.execErrors = make([]error, failAt+1)
						tx.execErrors[failAt] = sentinel
					}
					pool := &pgxpool.Pool{}
					repo, err := NewPostgresRepository(pool)
					if err != nil {
						t.Fatal(err)
					}
					ctx := identityRepositoryContext(pool, tx)
					err = f.run(t, ctx, repo, tx, operation, failAt < 0)
					wantWrites := len(stages)
					if failAt >= 0 {
						wantWrites = failAt + 1
						if !errors.Is(err, sentinel) || tx.rollbacks != 1 || commandTransactionFrom(ctx).failed == nil {
							t.Fatalf("error=%v rollbacks=%d", err, tx.rollbacks)
						}
					} else if err != nil || tx.rollbacks != 0 || len(tx.queryRows) != 0 {
						t.Fatalf("success: error=%v rollbacks=%d unread rows=%d", err, tx.rollbacks, len(tx.queryRows))
					}
					if len(tx.statements) != wantWrites || tx.commits != 0 {
						t.Fatalf("writes=%d want=%d commits=%d", len(tx.statements), wantWrites, tx.commits)
					}
					for i, statement := range tx.statements {
						if !strings.Contains(statement, stages[i]) {
							t.Fatalf("write %d: %s", i, statement)
						}
					}
					for _, args := range tx.queryRowArguments {
						if len(args) < 2 || args[0] != f.listing.TenantID {
							t.Fatalf("unscoped read: %v", args)
						}
					}
				})
			}
		})
	}
}

func (f listingWriteFixture) run(t *testing.T, ctx context.Context, repo *PostgresRepository, tx *identityScriptedTx, operation string, success bool) error {
	t.Helper()
	resourceRow := scriptedValueRow([]any{f.listing.ResourceID})
	validationRow := scriptedValueRow([]any{f.version.Version.ID, f.version.Evidence.ID})
	switch operation {
	case "create":
		tx.queryRows = []pgx.Row{resourceRow}
		l := f.listing
		l.CandidateRevisionID = ""
		a, o := currentTestEvidence(t, "catalog_listing.created", "catalog_listing", l.ID, l.CreatedAt)
		got, err := repo.CreateListing(ctx, catalog.CreateListingParams{Listing: l, Audit: a, Outbox: o})
		if success && !reflect.DeepEqual(got, l) {
			t.Fatalf("listing=%+v", got)
		}
		return err
	case "revise":
		tx.queryRows = []pgx.Row{listingWriteRow(f.listing), resourceRow}
		a, o := currentTestEvidence(t, "catalog_listing.revised", "catalog_listing", f.listing.ID, f.listing.CreatedAt)
		l, r, err := repo.CreateListingRevision(ctx, catalog.CreateListingRevisionParams{Revision: f.revision, ExpectedRevision: 1, Audit: a, Outbox: o})
		if success && (l.Revision != 2 || l.CandidateRevisionID != r.ID || !reflect.DeepEqual(r, f.revision)) {
			t.Fatalf("listing=%+v revision=%+v", l, r)
		}
		if success && (tx.arguments[0][1] != f.listing.TenantID || tx.arguments[0][2] != f.listing.ID || tx.arguments[0][4] != f.version.VersionRevision.ID || tx.arguments[0][5] != f.revision.DisplayName || tx.arguments[1][5] != int64(1)) {
			t.Fatal("revision write lost binding or optimistic guard")
		}
		return err
	case "submit":
		tx.queryRows = []pgx.Row{listingWriteRow(f.listing), resourceRow, validationRow}
		a, o := currentTestEvidence(t, "review_submission.submitted", "review_submission", f.submission.ID, f.listing.CreatedAt)
		got, err := repo.Submit(ctx, catalog.SubmitParams{Submission: f.submission, Audit: a, Outbox: o})
		if success && (!reflect.DeepEqual(got, f.submission) || !reflect.DeepEqual(tx.arguments[1][10], []byte(`[]`))) {
			t.Fatalf("submission=%+v", got)
		}
		return err
	case "approve", "reject", "request_changes":
		tx.queryResults = originQueryFixture()
		tx.queryRows = []pgx.Row{submissionWriteRow(f.submission), validationRow, currentVersionWriteRow(f.version, "")}
		a, o := currentTestEvidence(t, "review_submission."+operation, "review_submission", f.submission.ID, f.listing.CreatedAt)
		s, v, err := repo.DecideSubmission(ctx, catalog.DecideSubmissionParams{ActorMembershipID: originReviewerMember, ActorRealmID: originRealm, TenantID: f.listing.TenantID, SubmissionID: f.submission.ID, ExpectedRevision: 1, ExpectedValidationEvidenceID: f.version.Evidence.ID, Actor: "reviewer", Action: operation, Reason: "reviewed", At: f.listing.CreatedAt, Audit: a, Outbox: o})
		want := map[string]catalog.SubmissionState{"approve": catalog.SubmissionApproved, "reject": catalog.SubmissionRejected, "request_changes": catalog.SubmissionChangesRequired}[operation]
		if success && (s.State != want || s.Revision != 2 || s.DecidedAt == nil || !s.DecidedAt.Equal(f.listing.CreatedAt) || len(s.Decisions) != 1 || s.Decisions[0].Actor != "reviewer" || s.Decisions[0].Action != operation || s.Decisions[0].Reason != "reviewed") {
			t.Fatalf("submission=%+v", s)
		}
		if success && operation == "approve" && (v.SealedRevisionID != f.version.VersionRevision.ID || v.Revision != 2) {
			t.Fatalf("version=%+v", v)
		}
		if success && operation != "approve" && (v.SealedRevisionID != "" || v.Revision != 1) {
			t.Fatalf("non-approval changed version: %+v", v)
		}
		return err
	case "publish":
		s := f.submission
		s.State = catalog.SubmissionApproved
		tx.queryRows = []pgx.Row{listingWriteRow(f.listing), submissionWriteRow(s), validationRow}
		a, o := currentTestEvidence(t, "listing_release.published", "listing_release", f.release.ID, f.listing.CreatedAt)
		l, r, prior, err := repo.Publish(ctx, catalog.PublishParams{TenantID: f.listing.TenantID, ListingID: f.listing.ID, SubmissionID: s.ID, ExpectedRevision: 1, ExpectedValidationEvidenceID: f.version.Evidence.ID, Release: f.release, Audit: a, Outbox: o})
		if success && (l.CurrentReleaseID != f.release.ID || l.Revision != 2 || !reflect.DeepEqual(r, f.release) || prior != nil) {
			t.Fatalf("listing=%+v release=%+v prior=%+v", l, r, prior)
		}
		return err
	case "adopt":
		tx.queryRows = []pgx.Row{scriptedValueRow([]any{int64(3)}), scriptedValueRow([]any{false})}
		r := f.version.VersionRevision
		r.Adoption = &catalog.AdoptionProvenance{ObservationID: "observation", SourceID: "source", SourceRevision: 2, SnapshotID: "snapshot", RetrievalDigest: "retrieval", EntryLocation: "/entries/0", ExactEntryDigest: "exact", NormalizedEntryDigest: "normalized", NormalizationProfile: "profile", AdoptedBy: "author", AdoptedAt: r.CreatedAt}
		resource := catalog.Resource{ID: f.listing.ResourceID, TenantID: f.listing.TenantID, NamespaceID: f.listing.NamespaceID, Kind: catalog.KindAgent, CanonicalIdentifier: "urn:example:agent", Revision: 1, CreatedAt: r.CreatedAt, UpdatedAt: r.CreatedAt}
		a, o := currentTestEvidence(t, "observation.adopted", "resource", resource.ID, r.CreatedAt)
		params := catalog.AdoptExternalObservationParams{SourceAdmissionRevision: 3, Resource: resource, Version: f.version.Version, VersionRevision: r, Documents: f.version.Documents, Evidence: f.version.Evidence, Listing: f.listing, ListingRevision: f.revision, Audit: a, Outbox: o}
		got, err := repo.AdoptExternalObservation(ctx, params)
		if success && (!reflect.DeepEqual(got.VersionRevision, r) || !reflect.DeepEqual(got.Resource, resource) || !reflect.DeepEqual(got.ListingRevision, f.revision)) {
			t.Fatalf("adoption=%+v", got)
		}
		if success && !reflect.DeepEqual(tx.queryRowArguments[1], []any{resource.TenantID, r.Adoption.ObservationID, r.Adoption.SourceID, r.Adoption.SnapshotID, int64(2), "retrieval", "/entries/0", "exact", "normalized", "profile"}) {
			t.Fatalf("provenance binding=%v", tx.queryRowArguments[1])
		}
		return err
	default:
		t.Fatalf("unknown operation %s", operation)
		return nil
	}
}
