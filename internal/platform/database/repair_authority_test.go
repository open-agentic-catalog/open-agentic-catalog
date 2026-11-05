// Operator authority units check owner comparisons and failed reads; PostgreSQL
// integration retains actual role, ownership and tenant enforcement.
package database

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
)

func TestRepairAuthorityRequiresCurrentTableOwner(t *testing.T) {
	t.Parallel()
	// PostgreSQL role names are operator-selected and may use quoted mixed case.
	const owner = "Maintenance Owner"
	readFailure := errors.New("authority read failed")
	scanFailure := errors.New("owner scan failed")
	for _, operation := range []struct {
		name, table, actor string
		verify             func(context.Context, *identityScriptedTx) (string, error)
	}{
		{
			name: "idempotency repair", table: "idempotency_keys", actor: owner,
			verify: func(ctx context.Context, query *identityScriptedTx) (string, error) {
				return verifyIdempotencyRepairAuthority(ctx, query)
			},
		},
		{
			name: "search projection repair", table: "observations",
			verify: func(ctx context.Context, query *identityScriptedTx) (string, error) {
				return "", verifySearchProjectionRepairRole(ctx, query)
			},
		},
		{
			name: "identity provisioning", table: "tenant_identity_realms",
			verify: func(ctx context.Context, query *identityScriptedTx) (string, error) {
				return "", verifyIdentityProvisioningAuthority(ctx, query)
			},
		},
	} {
		for _, test := range []struct {
			name, currentUser string
			failure           error
			partial, accepted bool
		}{
			{name: "matching owner", currentUser: owner, accepted: true},
			{name: "API role", currentUser: "catalog_api"},
			{name: "worker role", currentUser: "catalog_worker"},
			{name: "different case", currentUser: "maintenance owner"},
			{name: "missing table", failure: pgx.ErrNoRows},
			{name: "query failure", failure: readFailure},
			{name: "partial scan failure", currentUser: owner, failure: scanFailure, partial: true},
		} {
			t.Run(operation.name+"/"+test.name, func(t *testing.T) {
				row := scriptedValueRow([]any{test.currentUser, owner})
				if test.failure != nil {
					row = scannerFunc(func(destinations ...any) error {
						if test.partial {
							setScanDestination(t, destinations, 0, test.currentUser)
						}
						return test.failure
					})
				}
				query := &identityScriptedTx{fakeCommandTx: &fakeCommandTx{}, queryRows: []pgx.Row{row}}
				actor, err := operation.verify(t.Context(), query)
				switch {
				case test.failure != nil:
					if actor != "" || !errors.Is(err, test.failure) || !strings.Contains(err.Error(), "inspect "+operation.name+" database authority") {
						t.Fatalf("failed authority read = %q, %v; want no actor and wrapped %v", actor, err, test.failure)
					}
				case !test.accepted:
					if actor != "" || err == nil || !strings.Contains(err.Error(), "requires the operator-controlled schema owner") || !strings.Contains(err.Error(), fmt.Sprintf("%q", test.currentUser)) {
						t.Fatalf("non-owner admission = %q, %v; want schema-owner rejection naming the current role", actor, err)
					}
				default:
					if err != nil || actor != operation.actor {
						t.Fatalf("owner admission = %q, %v; want %q", actor, err, operation.actor)
					}
				}
				if len(query.queryRowStatements) != 1 || len(query.queryRows) != 0 || len(query.queryRowArguments[0]) != 0 {
					t.Fatalf("authority reads = %d; unread rows = %d; arguments = %#v", len(query.queryRowStatements), len(query.queryRows), query.queryRowArguments)
				}
				if !strings.Contains(query.queryRowStatements[0], "to_regclass('"+operation.table+"')") {
					t.Fatalf("authority read did not select the %s table", operation.table)
				}
				if len(query.statements) != 0 || len(query.queryStatements) != 0 || query.commits != 0 || query.rollbacks != 0 {
					t.Fatal("authority inspection performed writes, unrelated reads or transaction completion")
				}
			})
		}
	}
}
