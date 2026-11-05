package database

import (
	"errors"
	"reflect"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
)

// SQL/RLS eligibility remains an integration concern. These units verify
// principal admission, exact bindings and the absence of a disclosed namespace
// when the driver refuses or fails a document read.
func TestDocumentAccessNamespaceRequiresCurrentTenantPrincipal(t *testing.T) {
	for _, mode := range []string{"missing principal", "other tenant", "authorized", "not found", "driver failure"} {
		t.Run(mode, func(t *testing.T) {
			store, tx, ctx, source := federationAdapterFixture(t)
			actor := authorizationUnitActor()
			const document = "00000000-0000-7000-8000-000000000041"
			const namespace = "00000000-0000-7000-8000-000000000031"
			failure := errors.New("document lookup failed")
			row := scriptedValueRow([]any{namespace})
			switch mode {
			case "other tenant":
				actor.TenantID = "00000000-0000-7000-8000-000000000002"
			case "not found":
				row = scannerFunc(func(...any) error { return pgx.ErrNoRows })
			case "driver failure":
				row = scannerFunc(func(...any) error { return failure })
			}
			if mode != "missing principal" {
				ctx = identity.WithActor(ctx, actor)
			}
			tx.queryRows = []pgx.Row{row}
			got, err := store.repository.DocumentAccessNamespace(ctx, source.TenantID, document)
			if mode == "authorized" {
				if err != nil || got != namespace {
					t.Fatalf("namespace=%q error=%v", got, err)
				}
			} else {
				code := catalog.CodeForbidden
				if mode == "not found" {
					code = catalog.CodeNotFound
				} else if mode == "driver failure" {
					code = catalog.CodeDependencyUnavailable
				}
				if got != "" || catalog.ErrorCodeOf(err) != code || mode == "driver failure" && !errors.Is(err, failure) {
					t.Fatalf("namespace=%q error=%v; want %s", got, err, code)
				}
			}
			if mode == "missing principal" || mode == "other tenant" {
				if len(tx.queryRowStatements) != 0 {
					t.Fatal("invalid principal reached document SQL")
				}
			} else if !reflect.DeepEqual(tx.queryRowArguments, [][]any{{source.TenantID, actor.MembershipID, actor.RealmID, actor.Subject, actor.ActorType, document}}) {
				t.Fatal("document read lost exact principal or tenant binding")
			}
		})
	}
}
