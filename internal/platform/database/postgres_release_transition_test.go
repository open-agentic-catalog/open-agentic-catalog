package database

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
)

// These units assert repository decisions and exact driver requests. The
// scripted transaction does not execute SQL or establish database/RLS behavior.
func releaseTransitionFixture(t *testing.T, from, to catalog.ReleaseState) (catalog.ListingRelease, catalog.TransitionReleaseParams) {
	t.Helper()
	before := newListingWriteFixture(t).release
	before.State, before.Revision = from, 7
	if from == catalog.ReleaseDeprecated || from == catalog.ReleaseSuperseded {
		before.ReplacementReleaseID = "00000000-0000-7000-8000-000000000027"
		before.DispositionReason = "previous replacement guidance"
	}
	if from == catalog.ReleaseDeprecated {
		previousSunset := before.UpdatedAt.Add(48 * time.Hour)
		before.SunsetAt = &previousSunset
	}
	at := before.UpdatedAt.Add(time.Hour)
	a, o := currentTestEvidence(t, "listing_release."+string(to), "listing_release", before.ID, at)
	params := catalog.TransitionReleaseParams{TenantID: before.TenantID, ReleaseID: before.ID, ExpectedRevision: 7, TargetState: to, Reason: a.Reason, At: at, Audit: a, Outbox: o}
	if to == catalog.ReleaseDeprecated || to == catalog.ReleaseSuperseded {
		params.ReplacementID = "00000000-0000-7000-8000-000000000025"
	}
	if to == catalog.ReleaseDeprecated {
		sunset := at.Add(24 * time.Hour)
		params.SunsetAt = &sunset
	}
	return before, params
}

func releaseTransitionRow(r catalog.ListingRelease) pgx.Row {
	return scriptedValueRow([]any{r.ID, r.TenantID, r.ListingID, r.SubmissionID, r.VersionRevisionID, r.ListingRevisionID, r.State, r.ReplacementReleaseID, r.DispositionReason, r.SunsetAt, r.ActivatedBy, r.Revision, r.ActivatedAt, r.UpdatedAt})
}

func TestReleaseTransitionRepositoryPreservesHistoryAndBindsCurrentPointer(t *testing.T) {
	t.Parallel()
	// Expected legal decisions are explicit, independently of validTransition.
	for _, pair := range [][2]catalog.ReleaseState{
		{catalog.ReleasePublished, catalog.ReleaseDeprecated},
		{catalog.ReleasePublished, catalog.ReleaseRetired},
		{catalog.ReleasePublished, catalog.ReleaseSuperseded},
		{catalog.ReleasePublished, catalog.ReleaseRevoked},
		{catalog.ReleaseDeprecated, catalog.ReleaseRetired},
		{catalog.ReleaseDeprecated, catalog.ReleaseSuperseded},
		{catalog.ReleaseDeprecated, catalog.ReleaseRevoked},
		{catalog.ReleaseRetired, catalog.ReleaseRevoked},
		{catalog.ReleaseSuperseded, catalog.ReleaseRevoked},
	} {
		t.Run(string(pair[0])+"/"+string(pair[1]), func(t *testing.T) {
			before, params := releaseTransitionFixture(t, pair[0], pair[1])
			tx := &identityScriptedTx{fakeCommandTx: &fakeCommandTx{}, queryRows: []pgx.Row{releaseTransitionRow(before)}}
			// A non-current historical release can legitimately affect zero listings.
			if pair[1] != catalog.ReleaseDeprecated {
				tx.execTags = []pgconn.CommandTag{pgconn.NewCommandTag("UPDATE 1"), pgconn.NewCommandTag("UPDATE 0")}
			}
			pool := &pgxpool.Pool{}
			repo, err := NewPostgresRepository(pool)
			if err != nil {
				t.Fatal(err)
			}
			got, err := repo.TransitionRelease(identityRepositoryContext(pool, tx), params)
			want := before
			want.State, want.DispositionReason, want.ReplacementReleaseID = pair[1], params.Reason, params.ReplacementID
			want.SunsetAt, want.Revision, want.UpdatedAt = params.SunsetAt, int64(8), params.At
			if err != nil || !reflect.DeepEqual(got, want) {
				t.Fatalf("release=%+v want=%+v error=%v", got, want, err)
			}
			if len(tx.queryRowStatements) != 1 || !strings.HasSuffix(tx.queryRowStatements[0], "WHERE tenant_id=$1 AND id=$2 FOR UPDATE") || !reflect.DeepEqual(tx.queryRowArguments[0], []any{params.TenantID, before.ID}) {
				t.Fatal("release lookup lost exact tenant/release lock binding")
			}
			var sunset any
			if params.SunsetAt != nil {
				sunset = *params.SunsetAt
			}
			if tx.statements[0] != "UPDATE listing_releases SET state=$3,replacement_release_id=nullif($4,'')::uuid,disposition_reason=$5,sunset_at=$6,revision=$7,updated_at=$8 WHERE tenant_id=$1 AND id=$2" || !reflect.DeepEqual(tx.arguments[0], []any{params.TenantID, before.ID, pair[1], params.ReplacementID, params.Reason, sunset, int64(8), params.At}) {
				t.Fatalf("release write binding=%v", tx.arguments[0])
			}
			evidenceIndex := 1
			if pair[1] != catalog.ReleaseDeprecated {
				evidenceIndex++
				if tx.statements[1] != "UPDATE catalog_listings SET current_release_id=NULL,revision=revision+1,updated_at=$3 WHERE tenant_id=$1 AND id=$2 AND current_release_id=$4" || !reflect.DeepEqual(tx.arguments[1], []any{params.TenantID, before.ListingID, params.At, before.ID}) {
					t.Fatal("pointer clear must match tenant, listing and this release only")
				}
			}
			if len(tx.statements) != evidenceIndex+2 || !strings.Contains(tx.statements[evidenceIndex], "INSERT INTO audit_events") || !strings.Contains(tx.statements[evidenceIndex+1], "INSERT INTO outbox_events") {
				t.Fatal("unexpected release/evidence write sequence")
			}
			auditArgs := tx.arguments[evidenceIndex]
			if !reflect.DeepEqual(auditArgs[2:8], []any{params.TenantID, params.Audit.Actor, "listing_release." + string(pair[1]), "listing_release", before.ID, params.Reason}) || !reflect.DeepEqual(auditArgs[12], params.At) {
				t.Fatal("audit lost release action/identity/reason/time")
			}
			if !reflect.DeepEqual(tx.arguments[evidenceIndex+1][:8], []any{params.Outbox.ID, params.TenantID, "com.open.agentic.catalog.listing.release." + string(pair[1]) + ".v1", before.ID, params.Outbox.Data, false, params.At, (*time.Time)(nil)}) {
				t.Fatal("outbox lost release event/tenant/time binding")
			}
			if tx.commits != 0 || tx.rollbacks != 0 {
				t.Fatal("repository took ownership of successful command transaction")
			}
		})
	}
}

func TestReleaseTransitionRepositoryRejectsStaleAndIllegalDecisionsBeforeWrites(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name     string
		from, to catalog.ReleaseState
		stale    bool
		code     catalog.ErrorCode
	}{
		{"stale", catalog.ReleasePublished, catalog.ReleaseDeprecated, true, catalog.CodePreconditionFailed},
		{"already deprecated", catalog.ReleaseDeprecated, catalog.ReleaseDeprecated, false, catalog.CodeInvalidTransition},
		{"retired cannot supersede", catalog.ReleaseRetired, catalog.ReleaseSuperseded, false, catalog.CodeInvalidTransition},
		{"superseded cannot retire", catalog.ReleaseSuperseded, catalog.ReleaseRetired, false, catalog.CodeInvalidTransition},
		{"revocation terminal", catalog.ReleaseRevoked, catalog.ReleaseRevoked, false, catalog.CodeInvalidTransition},
	} {
		t.Run(test.name, func(t *testing.T) {
			before, params := releaseTransitionFixture(t, test.from, test.to)
			if test.stale {
				params.ExpectedRevision = 6
			}
			tx := &identityScriptedTx{fakeCommandTx: &fakeCommandTx{}, queryRows: []pgx.Row{releaseTransitionRow(before)}}
			pool := &pgxpool.Pool{}
			repo, err := NewPostgresRepository(pool)
			if err != nil {
				t.Fatal(err)
			}
			got, err := repo.TransitionRelease(identityRepositoryContext(pool, tx), params)
			if catalog.ErrorCodeOf(err) != test.code || !reflect.DeepEqual(got, catalog.ListingRelease{}) || len(tx.statements) != 0 || tx.rollbacks != 1 || tx.commits != 0 {
				t.Fatalf("release=%+v error=%v writes=%d rollback=%d", got, err, len(tx.statements), tx.rollbacks)
			}
		})
	}
}

func TestReleaseTransitionRepositoryWithholdsResultWhenMutationCannotComplete(t *testing.T) {
	t.Parallel()
	for _, stage := range []string{"read", "release write", "pointer write", "audit write", "outbox write", "wrong audit target", "wrong audit action", "outbox time mismatch"} {
		t.Run(stage, func(t *testing.T) {
			before, params := releaseTransitionFixture(t, catalog.ReleasePublished, catalog.ReleaseRetired)
			sentinel := errors.New("release transition dependency failed")
			tx := &identityScriptedTx{fakeCommandTx: &fakeCommandTx{}, queryRows: []pgx.Row{releaseTransitionRow(before)}}
			wantWrites, wantCode := 0, catalog.CodeDependencyUnavailable
			switch stage {
			case "read":
				tx.queryRows = []pgx.Row{scannerFunc(func(...any) error { return sentinel })}
			case "release write":
				tx.execErrors = []error{sentinel}
				wantWrites = 1
			case "pointer write":
				tx.execErrors = []error{nil, sentinel}
				wantWrites = 2
			case "audit write":
				tx.execErrors = []error{nil, nil, sentinel}
				wantWrites = 3
			case "outbox write":
				tx.execErrors = []error{nil, nil, nil, sentinel}
				wantWrites = 4
			case "wrong audit target":
				params.Audit.TargetID = "00000000-0000-7000-8000-000000000026"
				wantWrites, wantCode = 2, catalog.CodeInternal
			case "wrong audit action":
				params.Audit.Action = "listing_release.revoked"
				wantWrites, wantCode = 2, catalog.CodeInternal
			case "outbox time mismatch":
				params.Outbox.CreatedAt = params.At.Add(time.Second)
				wantWrites, wantCode = 2, catalog.CodeInternal
			}
			pool := &pgxpool.Pool{}
			repo, err := NewPostgresRepository(pool)
			if err != nil {
				t.Fatal(err)
			}
			got, err := repo.TransitionRelease(identityRepositoryContext(pool, tx), params)
			if catalog.ErrorCodeOf(err) != wantCode || !reflect.DeepEqual(got, catalog.ListingRelease{}) || len(tx.statements) != wantWrites || tx.rollbacks != 1 || tx.commits != 0 {
				t.Fatalf("release=%+v error=%v writes=%d rollback=%d", got, err, len(tx.statements), tx.rollbacks)
			}
			if wantCode == catalog.CodeDependencyUnavailable && !errors.Is(err, sentinel) {
				t.Fatal("dependency classification lost internal cause")
			}
		})
	}
}
