package database

import (
	"errors"
	"reflect"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
)

// These tests qualify projection/error/cleanup plumbing only. The integration
// oracle independently qualifies the actual SQL semantics and least privileges.
func TestSelfProjectionBoundsBindingsAndClosesFailedRows(t *testing.T) {
	for _, mode := range []string{"valid", "empty", "invalid page", "inactive", "union failure", "scope query failure", "scope scan failure", "scope iteration failure"} {
		t.Run(mode, func(t *testing.T) {
			actor := authorizationUnitActor()
			pool := &pgxpool.Pool{}
			store := &PostgresIdentityStore{repository: &PostgresRepository{pool: pool}}
			permissions := []identity.Permission{"resource.read"}
			scopes := &identityScriptedRows{values: [][]any{{actor.TenantID, permissions}}}
			tx := &identityScriptedTx{fakeCommandTx: &fakeCommandTx{}, queryRows: []pgx.Row{scriptedValueRow([]any{int64(9), []bool{false}}), scriptedValueRow([]any{true}), scriptedValueRow([]any{permissions, permissions})}, queryResults: []identityQueryResult{{rows: scopes}}}
			ctx := identityRepositoryContext(pool, tx)
			commandTransactionFrom(ctx).authorizationShared = true
			request := identity.SelfAccessRequest{Limit: 2, AuthorizationRevision: 9, NamespaceID: actor.TenantID, AfterNamespaceID: actor.RealmID}
			switch mode {
			case "empty":
				scopes.values = nil
			case "invalid page":
				request.Limit = 102
			case "inactive":
				tx.queryRows[1] = scriptedValueRow([]any{false})
			case "union failure":
				tx.queryRows[2] = scannerFunc(func(...any) error { return errors.New("private dependency") })
			case "scope query failure":
				tx.queryResults[0].err = errors.New("private dependency")
			case "scope scan failure":
				scopes.values[0] = []any{}
			case "scope iteration failure":
				scopes.err = errors.New("private dependency")
			}
			got, err := store.ReadSelfAccess(ctx, actor, request)
			if mode == "valid" || mode == "empty" {
				if err != nil || got.AuthorizationRevision != 9 || !reflect.DeepEqual(got.Permissions, permissions) || !reflect.DeepEqual(got.AvailablePermissions, permissions) || got.NamespaceGrants == nil {
					t.Fatal(got, err)
				}
				if mode == "valid" && (len(got.NamespaceGrants) != 1 || got.NamespaceGrants[0].NamespaceID != actor.TenantID) {
					t.Fatal(got)
				}
				if mode == "empty" && len(got.NamespaceGrants) != 0 {
					t.Fatal(got)
				}
				want := []any{actor.TenantID, actor.MembershipID, actor.RealmID, request.NamespaceID, request.AfterNamespaceID, request.Limit}
				if !reflect.DeepEqual(tx.queryArguments[0], want) {
					t.Fatal("scope or bound was dropped")
				}
			} else if err == nil {
				t.Fatal("failed self projection accepted")
			}
			if (mode == "valid" || mode == "empty" || mode == "scope scan failure" || mode == "scope iteration failure") && !scopes.closed {
				t.Fatal("self rows left open")
			}
			if mode == "invalid page" && len(tx.queryRowStatements) != 0 {
				t.Fatal("invalid page reached SQL")
			}
		})
	}
}
