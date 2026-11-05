package database

import (
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
)

func TestDecisionAdapterGuards(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"submission read", "stale revision", "terminal", "self review", "validation read", "stale evidence", "missing evidence", "version read", "different sealed revision", "invalid action", "already sealed"} {
		t.Run(mode, func(t *testing.T) {
			f := newListingWriteFixture(t)
			a, o := currentTestEvidence(t, "review_submission.approve", "review_submission", f.submission.ID, f.listing.CreatedAt)
			params := catalog.DecideSubmissionParams{ActorMembershipID: originReviewerMember, ActorRealmID: originRealm, TenantID: f.listing.TenantID, SubmissionID: f.submission.ID, ExpectedRevision: 1, ExpectedValidationEvidenceID: f.version.Evidence.ID, Actor: "reviewer", Action: "approve", At: f.listing.CreatedAt, Reason: "reviewed", Audit: a, Outbox: o}
			code := catalog.CodePreconditionFailed
			sealed := ""
			readFailure := errors.New("read failed")
			failRow := -1
			wantReads, wantWrites := 1, 0
			switch mode {
			case "submission read":
				failRow = 0
			case "stale revision":
				params.ExpectedRevision = 2
			case "terminal":
				f.submission.State = catalog.SubmissionRejected
				code = catalog.CodeInvalidTransition
			case "self review":
				params.Actor = f.submission.SubmittedBy
				code = catalog.CodeSeparationOfDuties
			case "validation read":
				failRow = 1
				wantReads, wantWrites = 2, 1
			case "stale evidence":
				params.ExpectedValidationEvidenceID = "old"
				wantReads, wantWrites = 2, 1
			case "missing evidence":
				params.ExpectedValidationEvidenceID = ""
				wantReads, wantWrites = 2, 1
			case "version read":
				failRow = 2
				wantReads, wantWrites = 3, 1
			case "different sealed revision":
				sealed = "other"
				code = catalog.CodeConflict
				wantReads, wantWrites = 3, 1
			case "invalid action":
				params.Action = "unknown"
				code = catalog.CodeInvalidArgument
				wantReads, wantWrites = 3, 1
			case "already sealed":
				sealed = f.version.VersionRevision.ID
				wantReads, wantWrites = 3, 4
			}
			rows := []pgx.Row{submissionWriteRow(f.submission), scriptedValueRow([]any{f.version.Version.ID, f.version.Evidence.ID}), currentVersionWriteRow(f.version, sealed)}
			if failRow >= 0 && failRow < len(rows) {
				rows[failRow] = scannerFunc(func(...any) error { return readFailure })
			}
			tx := &identityScriptedTx{fakeCommandTx: &fakeCommandTx{}, queryRows: rows, queryResults: originQueryFixture()}
			pool := &pgxpool.Pool{}
			repo, err := NewPostgresRepository(pool)
			if err != nil {
				t.Fatal(err)
			}
			ctx := identityRepositoryContext(pool, tx)
			s, v, err := repo.DecideSubmission(ctx, params)
			if mode == "already sealed" {
				if err != nil || v.Revision != 1 || v.SealedRevisionID != sealed || s.State != catalog.SubmissionApproved || tx.rollbacks != 0 {
					t.Fatalf("listing-only approval: %+v %+v %v", s, v, err)
				}
				// A listing-only approval must not rewrite sealed version history.
				if !strings.Contains(tx.statements[1], "UPDATE review_submissions") {
					t.Fatal("sealed version was updated")
				}
			} else {
				if failRow >= 0 {
					if !errors.Is(err, readFailure) {
						t.Fatalf("lost driver failure: %v", err)
					}
				} else if catalog.ErrorCodeOf(err) != code {
					t.Fatalf("error=%v want=%s", err, code)
				}
				if s.ID != "" || v.ID != "" || tx.rollbacks != 1 {
					t.Fatal("rejected decision returned a result or failed to abort")
				}
			}
			if len(tx.queryRowStatements) != wantReads || len(tx.statements) != wantWrites || tx.commits != 0 {
				t.Fatalf("reads=%d writes=%d commits=%d", len(tx.queryRowStatements), len(tx.statements), tx.commits)
			}
		})
	}
}

func TestPublicationAdapterRejectsChangedApprovalAndEvidence(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"listing revision", "wrong listing", "not approved", "missing evidence", "changed evidence"} {
		t.Run(mode, func(t *testing.T) {
			f := newListingWriteFixture(t)
			f.submission.State = catalog.SubmissionApproved
			params := catalog.PublishParams{TenantID: f.listing.TenantID, ListingID: f.listing.ID, SubmissionID: f.submission.ID, ExpectedRevision: 1, ExpectedValidationEvidenceID: f.version.Evidence.ID, Release: f.release}
			code := catalog.CodePreconditionFailed
			reads, writes := 2, 0
			switch mode {
			case "listing revision":
				params.ExpectedRevision = 2
				reads = 1
			case "wrong listing":
				f.submission.ListingID = "other"
			case "not approved":
				f.submission.State = catalog.SubmissionRejected
			case "missing evidence":
				params.ExpectedValidationEvidenceID = ""
				reads, writes = 3, 1
				code = catalog.CodePublicationNotReady
			case "changed evidence":
				params.ExpectedValidationEvidenceID = "other"
				reads, writes = 3, 1
				code = catalog.CodePublicationNotReady
			}
			tx := &identityScriptedTx{fakeCommandTx: &fakeCommandTx{}, queryRows: []pgx.Row{listingWriteRow(f.listing), submissionWriteRow(f.submission), scriptedValueRow([]any{f.version.Version.ID, f.version.Evidence.ID})}}
			pool := &pgxpool.Pool{}
			repo, err := NewPostgresRepository(pool)
			if err != nil {
				t.Fatal(err)
			}
			l, r, prior, err := repo.Publish(identityRepositoryContext(pool, tx), params)
			if catalog.ErrorCodeOf(err) != code || l.ID != "" || r.ID != "" || prior != nil || len(tx.queryRowStatements) != reads || len(tx.statements) != writes || tx.rollbacks != 1 || tx.commits != 0 {
				t.Fatalf("error=%v reads=%d writes=%d", err, len(tx.queryRowStatements), len(tx.statements))
			}
		})
	}
}

func TestAdoptionAdapterRequiresSameTenantAndCurrentConflictFreeOccurrence(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"no provenance", "foreign version", "foreign revision", "foreign listing", "foreign listing revision", "source gone", "source changed", "missing admission", "occurrence gone", "conflicting content"} {
		t.Run(mode, func(t *testing.T) {
			f := newListingWriteFixture(t)
			p := catalog.AdoptExternalObservationParams{SourceAdmissionRevision: 3, Resource: catalog.Resource{ID: f.listing.ResourceID, TenantID: restoreTestTenant}, Version: f.version.Version, VersionRevision: f.version.VersionRevision, Listing: f.listing, ListingRevision: f.revision}
			p.VersionRevision.Adoption = &catalog.AdoptionProvenance{SourceID: "source", ObservationID: "observation"}
			code := catalog.CodeInvalidArgument
			reads := 0
			rows := []pgx.Row{scriptedValueRow([]any{int64(3)}), scriptedValueRow([]any{false})}
			switch mode {
			case "no provenance":
				p.VersionRevision.Adoption = nil
			case "foreign version":
				p.Version.TenantID = "other"
			case "foreign revision":
				p.VersionRevision.TenantID = "other"
			case "foreign listing":
				p.Listing.TenantID = "other"
			case "foreign listing revision":
				p.ListingRevision.TenantID = "other"
			case "source changed", "missing admission":
				if mode == "source changed" {
					rows[0] = scriptedValueRow([]any{int64(4)})
				} else {
					p.SourceAdmissionRevision = 0
				}
				reads = 1
				code = catalog.CodePreconditionFailed
			case "source gone":
				rows[0] = scannerFunc(func(...any) error { return pgx.ErrNoRows })
				reads = 1
				code = catalog.CodePreconditionFailed
			case "occurrence gone":
				rows[1] = scannerFunc(func(...any) error { return pgx.ErrNoRows })
				reads = 2
				code = catalog.CodePreconditionFailed
			case "conflicting content":
				rows[1] = scriptedValueRow([]any{true})
				reads = 2
				code = catalog.CodeConflict
			}
			tx := &identityScriptedTx{fakeCommandTx: &fakeCommandTx{}, queryRows: rows}
			pool := &pgxpool.Pool{}
			repo, err := NewPostgresRepository(pool)
			if err != nil {
				t.Fatal(err)
			}
			got, err := repo.AdoptExternalObservation(identityRepositoryContext(pool, tx), p)
			if catalog.ErrorCodeOf(err) != code || got.Resource.ID != "" || len(tx.queryRowStatements) != reads || len(tx.statements) != 0 || tx.rollbacks != 1 || tx.commits != 0 {
				t.Fatalf("error=%v reads=%d writes=%d", err, len(tx.queryRowStatements), len(tx.statements))
			}
		})
	}
}

func TestSubmitAdapterRejectsChangedCandidatesAndReadFailures(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"listing read", "resource read", "candidate changed", "validation read", "evidence changed"} {
		t.Run(mode, func(t *testing.T) {
			f := newListingWriteFixture(t)
			if mode == "candidate changed" {
				f.listing.CandidateRevisionID = "other"
			}
			current := f.version.Evidence.ID
			if mode == "evidence changed" {
				current = "newer"
			}
			rows := []pgx.Row{listingWriteRow(f.listing), scriptedValueRow([]any{f.listing.ResourceID}), scriptedValueRow([]any{f.version.Version.ID, current})}
			sentinel := errors.New("read failed")
			failRow := -1
			wantReads, wantWrites := 3, 1
			switch mode {
			case "listing read":
				failRow = 0
				wantReads, wantWrites = 1, 0
			case "resource read":
				failRow = 1
				wantReads, wantWrites = 2, 0
			case "candidate changed":
				wantReads, wantWrites = 2, 0
			case "validation read":
				failRow = 2
			}
			if failRow >= 0 && failRow < len(rows) {
				rows[failRow] = scannerFunc(func(...any) error { return sentinel })
			}
			tx := &identityScriptedTx{fakeCommandTx: &fakeCommandTx{}, queryRows: rows}
			pool := &pgxpool.Pool{}
			repo, err := NewPostgresRepository(pool)
			if err != nil {
				t.Fatal(err)
			}
			got, err := repo.Submit(identityRepositoryContext(pool, tx), catalog.SubmitParams{Submission: f.submission})
			if failRow >= 0 {
				if !errors.Is(err, sentinel) {
					t.Fatalf("error=%v", err)
				}
			} else if catalog.ErrorCodeOf(err) != catalog.CodePreconditionFailed {
				t.Fatalf("error=%v", err)
			}
			if got.ID != "" || tx.rollbacks != 1 || tx.commits != 0 || len(tx.statements) != wantWrites || len(tx.queryRowStatements) != wantReads {
				t.Fatalf("result=%+v reads=%d writes=%d rollback=%d", got, len(tx.queryRowStatements), len(tx.statements), tx.rollbacks)
			}
		})
	}
}
