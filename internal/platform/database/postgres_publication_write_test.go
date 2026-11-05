package database

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
)

func TestPublicationAdapterRetiresPriorReleaseWithoutRewritingHistory(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"superseded", "retired", "prior read failure", "prior write failure"} {
		t.Run(mode, func(t *testing.T) {
			f := newListingWriteFixture(t)
			prior := f.release
			prior.ID = "00000000-0000-7000-8000-000000000025"
			prior.Revision = 4
			f.listing.CurrentReleaseID = prior.ID
			f.submission.State = catalog.SubmissionApproved
			row := scriptedValueRow([]any{prior.ID, prior.TenantID, prior.ListingID, prior.SubmissionID, prior.VersionRevisionID, prior.ListingRevisionID, prior.State, prior.ReplacementReleaseID, prior.DispositionReason, prior.SunsetAt, prior.ActivatedBy, prior.Revision, prior.ActivatedAt, prior.UpdatedAt})
			sentinel := errors.New("prior release failed")
			if mode == "prior read failure" {
				row = scannerFunc(func(...any) error { return sentinel })
			}
			tx := &identityScriptedTx{fakeCommandTx: &fakeCommandTx{}, queryRows: []pgx.Row{listingWriteRow(f.listing), submissionWriteRow(f.submission), scriptedValueRow([]any{f.version.Version.ID, f.version.Evidence.ID}), row}}
			if mode == "prior write failure" {
				tx.execErrors = []error{nil, nil, sentinel}
			}
			a, o := currentTestEvidence(t, "listing_release.published", "listing_release", f.release.ID, f.listing.CreatedAt)
			disposition := catalog.ReleaseSuperseded
			if mode == "retired" {
				disposition = catalog.ReleaseRetired
			}
			p := catalog.PublishParams{TenantID: f.listing.TenantID, ListingID: f.listing.ID, SubmissionID: f.submission.ID, ExpectedRevision: 1, ExpectedValidationEvidenceID: f.version.Evidence.ID, Release: f.release, PriorDisposition: disposition, PriorReason: "Replace prior release", Audit: a, Outbox: o}
			pool := &pgxpool.Pool{}
			repo, err := NewPostgresRepository(pool)
			if err != nil {
				t.Fatal(err)
			}
			listing, release, previous, err := repo.Publish(identityRepositoryContext(pool, tx), p)
			if strings.HasSuffix(mode, "failure") {
				wantWrites := 2
				if mode == "prior write failure" {
					wantWrites = 3
				}
				if !errors.Is(err, sentinel) || listing.ID != "" || release.ID != "" || previous != nil || len(tx.statements) != wantWrites || tx.rollbacks != 1 {
					t.Fatalf("error=%v writes=%d rollback=%d", err, len(tx.statements), tx.rollbacks)
				}
			} else {
				want := prior
				want.State = disposition
				want.Revision++
				want.DispositionReason = p.PriorReason
				want.UpdatedAt = f.release.ActivatedAt
				if disposition == catalog.ReleaseSuperseded {
					want.ReplacementReleaseID = f.release.ID
				}
				if err != nil || !reflect.DeepEqual(previous, &want) || !reflect.DeepEqual(release, f.release) || listing.CurrentReleaseID != f.release.ID || listing.Revision != 2 || len(tx.statements) != 6 || tx.rollbacks != 0 {
					t.Fatalf("previous=%+v listing=%+v error=%v", previous, listing, err)
				}
				if !reflect.DeepEqual(tx.arguments[2], []any{restoreTestTenant, prior.ID, disposition, want.ReplacementReleaseID, p.PriorReason, int64(5), f.release.ActivatedAt}) {
					t.Fatalf("retirement binding=%v", tx.arguments[2])
				}
			}
			if tx.commits != 0 || !reflect.DeepEqual(tx.queryRowArguments[3], []any{restoreTestTenant, prior.ID}) {
				t.Fatal("prior-release lookup lost tenant binding or committed outside command")
			}
		})
	}
}

type staleListingWriteTx struct{ *identityScriptedTx }

func (tx staleListingWriteTx) Exec(ctx context.Context, query string, args ...any) (pgconn.CommandTag, error) {
	tag, err := tx.identityScriptedTx.Exec(ctx, query, args...)
	if strings.Contains(query, "UPDATE catalog_listings") {
		tag = pgconn.NewCommandTag("UPDATE 0")
	}
	return tag, err
}

func TestListingRevisionAdapterRejectsStaleAndLostUpdates(t *testing.T) {
	t.Parallel()
	for _, concurrent := range []bool{false, true} {
		t.Run(map[bool]string{false: "stale revision", true: "concurrent update"}[concurrent], func(t *testing.T) {
			f := newListingWriteFixture(t)
			tx := &identityScriptedTx{fakeCommandTx: &fakeCommandTx{}, queryRows: []pgx.Row{listingWriteRow(f.listing), scriptedValueRow([]any{f.listing.ResourceID})}}
			pool := &pgxpool.Pool{}
			repo, err := NewPostgresRepository(pool)
			if err != nil {
				t.Fatal(err)
			}
			expected := int64(2)
			var transaction pgx.Tx = tx
			wantWrites := 0
			if concurrent {
				expected = 1
				transaction = staleListingWriteTx{tx}
				wantWrites = 2
			}
			l, r, err := repo.CreateListingRevision(identityRepositoryContext(pool, transaction), catalog.CreateListingRevisionParams{Revision: f.revision, ExpectedRevision: expected})
			if catalog.ErrorCodeOf(err) != catalog.CodePreconditionFailed || l.ID != "" || r.ID != "" || len(tx.statements) != wantWrites || tx.rollbacks != 1 || tx.commits != 0 {
				t.Fatalf("error=%v writes=%d rollback=%d", err, len(tx.statements), tx.rollbacks)
			}
		})
	}
}
