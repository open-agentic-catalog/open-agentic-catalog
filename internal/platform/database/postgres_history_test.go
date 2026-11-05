package database

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
)

// These tests exercise adapter control flow with scripted rows. The pool is an
// identity token only; the context supplies a fake transaction, never a connection.
// SQL execution, constraints and RLS remain covered by integration tests.
func TestHistoryAdaptersDecodeRowsBindPagesAndCloseResults(t *testing.T) {
	t.Parallel()
	const parent = "00000000-0000-7000-8000-000000000010"
	const after = "00000000-0000-7000-8000-000000000020"
	now := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	updated := now.Add(time.Hour)
	version := catalog.ResourceVersion{ID: "version", TenantID: paginationTestTenantID, ResourceID: parent, Version: "1.2.3", CandidateRevisionID: "candidate", SealedRevisionID: "sealed", Revision: 7, CreatedAt: now, UpdatedAt: updated}
	listing := catalog.CatalogListing{ID: "listing", TenantID: paginationTestTenantID, NamespaceID: "namespace", ResourceID: parent, CandidateRevisionID: "candidate", CurrentReleaseID: "release", Revision: 8, CreatedAt: now, UpdatedAt: updated}
	summary := catalog.RevisionSummary{ID: "revision", RevisionNumber: 4, Digest: "digest", CreatedBy: "author", CreatedAt: now}
	release := catalog.ListingRelease{ID: "release", TenantID: paginationTestTenantID, ListingID: parent, SubmissionID: "submission", VersionRevisionID: "version-revision", ListingRevisionID: "listing-revision", State: catalog.ReleaseDeprecated, ReplacementReleaseID: "replacement", DispositionReason: "superseded", SunsetAt: &updated, ActivatedBy: "publisher", Revision: 9, ActivatedAt: now, UpdatedAt: updated}
	submission := catalog.ReviewSubmission{ID: "submission", TenantID: paginationTestTenantID, ListingID: parent, VersionRevisionID: "version-revision", ListingRevisionID: "listing-revision", EvidenceSetID: "evidence", EvidenceDigest: "evidence-digest", PolicyVersion: "policy", State: catalog.SubmissionApproved, SubmittedBy: "author", Decisions: []catalog.ReviewDecision{{Actor: "reviewer", Action: "approve", Reason: "reviewed", At: updated}}, Revision: 10, SubmittedAt: now, DecidedAt: &updated}
	for _, test := range []struct {
		name string
		read func(*PostgresRepository, context.Context, catalog.HistoryPageRequest) (any, error)
		row  []any
		want any
	}{
		{"versions", func(p *PostgresRepository, ctx context.Context, page catalog.HistoryPageRequest) (any, error) {
			return p.ListVersions(ctx, paginationTestTenantID, parent, page)
		}, []any{version.ID, version.TenantID, version.ResourceID, version.Version, version.CandidateRevisionID, version.SealedRevisionID, version.Revision, version.CreatedAt, version.UpdatedAt}, []catalog.ResourceVersion{version}},
		{"version revisions", func(p *PostgresRepository, ctx context.Context, page catalog.HistoryPageRequest) (any, error) {
			return p.ListVersionRevisions(ctx, paginationTestTenantID, parent, page)
		}, []any{summary.ID, summary.RevisionNumber, summary.Digest, summary.CreatedBy, summary.CreatedAt}, []catalog.RevisionSummary{summary}},
		{"listings", func(p *PostgresRepository, ctx context.Context, page catalog.HistoryPageRequest) (any, error) {
			return p.ListListings(ctx, paginationTestTenantID, parent, page)
		}, []any{listing.ID, listing.TenantID, listing.NamespaceID, listing.ResourceID, listing.CandidateRevisionID, listing.CurrentReleaseID, listing.Revision, listing.CreatedAt, listing.UpdatedAt}, []catalog.CatalogListing{listing}},
		{"listing revisions", func(p *PostgresRepository, ctx context.Context, page catalog.HistoryPageRequest) (any, error) {
			return p.ListListingRevisions(ctx, paginationTestTenantID, parent, page)
		}, []any{summary.ID, summary.RevisionNumber, summary.Digest, summary.CreatedBy, summary.CreatedAt}, []catalog.RevisionSummary{summary}},
		{"releases", func(p *PostgresRepository, ctx context.Context, page catalog.HistoryPageRequest) (any, error) {
			return p.ListReleases(ctx, paginationTestTenantID, parent, page)
		}, []any{release.ID, release.TenantID, release.ListingID, release.SubmissionID, release.VersionRevisionID, release.ListingRevisionID, release.State, release.ReplacementReleaseID, release.DispositionReason, release.SunsetAt, release.ActivatedBy, release.Revision, release.ActivatedAt, release.UpdatedAt}, []catalog.ListingRelease{release}},
		{"submissions", func(p *PostgresRepository, ctx context.Context, page catalog.HistoryPageRequest) (any, error) {
			return p.ListSubmissions(ctx, paginationTestTenantID, catalog.SubmissionListRequest{HistoryPageRequest: page, ListingID: parent, State: catalog.SubmissionApproved})
		}, []any{submission.ID, submission.TenantID, submission.ListingID, submission.VersionRevisionID, submission.ListingRevisionID, submission.EvidenceSetID, submission.EvidenceDigest, submission.PolicyVersion, submission.State, submission.SubmittedBy, []byte(`[{"actor":"reviewer","action":"approve","reason":"reviewed","at":"2026-09-01T13:00:00Z"}]`), submission.Revision, submission.SubmittedAt, submission.DecidedAt, "", ""}, []catalog.ReviewSubmission{submission}},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			for _, scenario := range []string{"first page", "continuation", "empty", "query error", "scan error", "iteration error"} {
				t.Run(scenario, func(t *testing.T) {
					pool := &pgxpool.Pool{}
					repository, err := NewPostgresRepository(pool)
					if err != nil {
						t.Fatal(err)
					}
					rows := &identityScriptedRows{values: [][]any{test.row}}
					result := identityQueryResult{rows: rows}
					failure := errors.New("scripted driver failure")
					var wantError error
					var scanRows *searchHydrationRows
					switch scenario {
					case "empty":
						rows.values = nil
					case "query error":
						result = identityQueryResult{err: failure}
						wantError = failure
					case "scan error":
						scanRows = &searchHydrationRows{identityScriptedRows: identityScriptedRows{values: [][]any{{}}}, scan: func(...any) error { return failure }}
						result.rows = scanRows
						wantError = failure
					case "iteration error":
						rows.err = failure
						wantError = failure
					}
					tx := &identityScriptedTx{fakeCommandTx: &fakeCommandTx{}, queryResults: []identityQueryResult{result}}
					page := catalog.HistoryPageRequest{Limit: 3}
					var position any
					if scenario == "continuation" {
						page.AfterID, position = after, after
					}
					got, err := test.read(repository, identityRepositoryContext(pool, tx), page)
					if !errors.Is(err, wantError) {
						t.Fatalf("read error = %v, want %v", err, wantError)
					}
					if wantError != nil && catalog.ErrorCodeOf(err) != catalog.CodeDependencyUnavailable {
						t.Fatalf("driver failure lost its safe domain code: %v", err)
					}
					if scenario == "empty" {
						if value := reflect.ValueOf(got); value.IsNil() || value.Len() != 0 {
							t.Fatalf("empty result = %#v, want non-nil empty slice", got)
						}
					} else if wantError == nil && !reflect.DeepEqual(got, test.want) {
						t.Fatalf("decoded page = %#v, want %#v", got, test.want)
					}
					if scenario == "scan error" {
						if !scanRows.closed {
							t.Fatal("scan failure left rows open")
						}
					} else if scenario != "query error" && !rows.closed {
						t.Fatal("result rows left open")
					}
					requireTenantBoundQuery(t, tx.queryStatements, tx.queryArguments)
					wantArgs := []any{paginationTestTenantID, parent, position, 3}
					if test.name == "submissions" {
						wantArgs = []any{paginationTestTenantID, parent, catalog.SubmissionApproved, position, 3, false}
					}
					if !reflect.DeepEqual(tx.queryArguments[0], wantArgs) {
						t.Fatalf("page arguments = %#v, want %#v", tx.queryArguments[0], wantArgs)
					}
				})
			}
		})
	}
}

func TestSubmissionHistoryRejectsMalformedDecisionsAndBindsOptionalFilters(t *testing.T) {
	t.Parallel()
	pool := &pgxpool.Pool{}
	repository, err := NewPostgresRepository(pool)
	if err != nil {
		t.Fatal(err)
	}
	// Only the JSON column is relevant: all other destinations retain zero values.
	rows := &searchHydrationRows{identityScriptedRows: identityScriptedRows{values: [][]any{{}}}, scan: func(destinations ...any) error {
		setScanDestination(t, destinations, 10, []byte(`[{`))
		return nil
	}}
	tx := &identityScriptedTx{fakeCommandTx: &fakeCommandTx{}, queryResults: []identityQueryResult{{rows: rows}}}
	items, err := repository.ListSubmissions(identityRepositoryContext(pool, tx), paginationTestTenantID, catalog.SubmissionListRequest{HistoryPageRequest: catalog.HistoryPageRequest{Limit: 2}})
	if catalog.ErrorCodeOf(err) != catalog.CodeInternal || items != nil || !rows.closed {
		t.Fatalf("malformed decisions: items=%#v, error=%v, closed=%t", items, err, rows.closed)
	}
	want := []any{paginationTestTenantID, nil, catalog.SubmissionState(""), nil, 2, false}
	if len(tx.queryArguments) != 1 || !reflect.DeepEqual(tx.queryArguments[0], want) {
		t.Fatalf("optional filters = %#v, want %#v", tx.queryArguments, want)
	}
}

func TestPublishedIdentityLookupPreservesResultAndDriverFailure(t *testing.T) {
	t.Parallel()
	failure := errors.New("scripted lookup failure")
	for _, test := range []struct {
		name  string
		found bool
		err   error
	}{{name: "found", found: true}, {name: "absent"}, {name: "driver failure", err: failure}} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			pool := &pgxpool.Pool{}
			repository, err := NewPostgresRepository(pool)
			if err != nil {
				t.Fatal(err)
			}
			tx := &identityScriptedTx{fakeCommandTx: &fakeCommandTx{}, queryRows: []pgx.Row{scannerFunc(func(destinations ...any) error {
				setScanDestination(t, destinations, 0, test.found)
				return test.err
			})}}
			const identifier = "urn:air:catalog.example.com:default:agent"
			found, err := repository.HasPublishedIdentity(identityRepositoryContext(pool, tx), paginationTestTenantID, identifier, "1.2.3")
			if found != test.found || !errors.Is(err, test.err) {
				t.Fatalf("lookup = %t, %v, want %t, %v", found, err, test.found, test.err)
			}
			if test.err != nil && catalog.ErrorCodeOf(err) != catalog.CodeDependencyUnavailable {
				t.Fatalf("unsafe driver error: %v", err)
			}
			requireTenantBoundQuery(t, tx.queryRowStatements, tx.queryRowArguments)
			if !reflect.DeepEqual(tx.queryRowArguments[0], []any{paginationTestTenantID, identifier, "1.2.3"}) {
				t.Fatalf("lookup arguments = %#v", tx.queryRowArguments)
			}
		})
	}
}
