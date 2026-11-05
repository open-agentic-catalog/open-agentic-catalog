package database

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
)

const originAuthorMember = "00000000-0000-7000-8000-000000000091"
const originReviewerMember = "00000000-0000-7000-8000-000000000092"
const originRealm = "00000000-0000-7000-8000-000000000093"

func originQueryFixture() []identityQueryResult {
	return []identityQueryResult{{rows: &identityScriptedRows{values: [][]any{{originAuthorMember, originRealm, "author"}}}}}
}

type originScanFailureRows struct {
	*identityScriptedRows
	scanErr error
}

func (r originScanFailureRows) Scan(...any) error { return r.scanErr }

func TestOriginLookupKeepsTheBoundTransactionTenant(t *testing.T) {
	for _, mode := range []string{"same tenant", "other tenant", "other pool"} {
		t.Run(mode, func(t *testing.T) {
			// The pool is only an identity handle. The bound scripted transaction
			// performs every operation; no connection or PostgreSQL process exists.
			pool := &pgxpool.Pool{}
			tx := &identityScriptedTx{fakeCommandTx: &fakeCommandTx{}, queryResults: originQueryFixture()}
			ctx := identityRepositoryContext(pool, tx)
			repo := &PostgresRepository{pool: pool}
			tenant := "00000000-0000-7000-8000-000000000001"
			var want catalog.ErrorCode
			switch mode {
			case "other tenant":
				tenant = "00000000-0000-7000-8000-000000000002"
				want = catalog.CodeForbidden
			case "other pool":
				repo.pool = &pgxpool.Pool{}
				want = catalog.CodeInternal
			}
			origin, err := repo.GetOriginatingPrincipal(ctx, tenant, "review_submission", "target")
			if want != "" {
				if err == nil || catalog.ErrorCodeOf(err) != want || origin != (catalog.OriginatingPrincipal{}) || len(tx.queryStatements) != 0 {
					t.Fatal("an unrelated tenant or pool reached the private lookup", mode, origin, err, tx.queryStatements)
				}
			} else if err != nil || origin != (catalog.OriginatingPrincipal{TenantID: tenant, MembershipID: originAuthorMember, RealmID: originRealm, Subject: "author"}) || len(tx.queryStatements) != 1 {
				t.Fatal("valid bound origin lookup failed", origin, err)
			}
		})
	}
}

func TestOriginLookupDiscardsPartialPrincipalOnQueryFailures(t *testing.T) {
	for _, stage := range []string{"query", "scan", "iteration"} {
		t.Run(stage, func(t *testing.T) {
			sentinel := errors.New("owned origin lookup failure")
			rows := &identityScriptedRows{values: [][]any{{originAuthorMember, originRealm, "author"}}}
			result := identityQueryResult{rows: rows}
			switch stage {
			case "query":
				result.err = sentinel
			case "scan":
				result.rows = originScanFailureRows{identityScriptedRows: rows, scanErr: sentinel}
			case "iteration":
				rows.err = sentinel // A valid first row must not hide a later read failure.
			}
			tx := &identityScriptedTx{fakeCommandTx: &fakeCommandTx{}, queryResults: []identityQueryResult{result}}
			origin, err := readOriginatingPrincipal(t.Context(), tx, restoreTestTenant, "review_submission", "target")
			if !errors.Is(err, sentinel) || catalog.ErrorCodeOf(err) != catalog.CodeDependencyUnavailable || origin != (catalog.OriginatingPrincipal{}) {
				t.Fatal("read failure exposed an origin or lost its classification", origin, err)
			}
			if stage != "query" && !rows.closed {
				t.Fatal("origin result set was not closed")
			}
			want := []any{restoreTestTenant, "review_submission", "target", []string{"review_submission.submitted"}}
			if len(tx.queryArguments) != 1 || !reflect.DeepEqual(tx.queryArguments[0], want) {
				t.Fatal("origin lookup lost its exact tenant, target or action binding", tx.queryArguments)
			}
		})
	}
}

func TestOriginLookupRequiresOneBoundPrincipal(t *testing.T) {
	for _, mode := range []string{"valid", "missing", "ambiguous", "invalid realm", "missing subject", "invalid kind"} {
		t.Run(mode, func(t *testing.T) {
			values := make([][]any, 1, 2)
			values[0] = []any{originAuthorMember, originRealm, "author"}
			kind := "review_submission"
			switch mode {
			case "missing":
				values = nil
			case "ambiguous":
				values = append(values, values[0])
			case "invalid realm":
				values[0][1] = ""
			case "missing subject":
				values[0][2] = ""
			case "invalid kind":
				kind = "publisher"
			}
			tx := &identityScriptedTx{fakeCommandTx: &fakeCommandTx{}, queryResults: []identityQueryResult{{rows: &identityScriptedRows{values: values}}}}
			origin, err := readOriginatingPrincipal(t.Context(), tx, restoreTestTenant, kind, "target")
			if mode == "valid" {
				if err != nil || origin.Subject != "author" || origin.TenantID != restoreTestTenant {
					t.Fatal(origin, err)
				}
			} else if catalog.ErrorCodeOf(err) != catalog.CodeForbidden {
				t.Fatal("unbound origin admitted", origin, err)
			}
			if mode != "invalid kind" && !strings.Contains(tx.queryStatements[0], "LIMIT 2") {
				t.Fatal("unbounded origin lookup")
			}
		})
	}
}
