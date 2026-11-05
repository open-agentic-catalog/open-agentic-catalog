package database

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/test/identityfixture"
)

func TestCollectionReadWithholdsSelectionAfterPolicyFailure(t *testing.T) {
	ctx, actor, err := identity.WithCollectionContext(identityfixture.WithActor(t.Context(), authorizationUnitActor()))
	if err != nil {
		t.Fatal(err)
	}
	revision, _ := identity.CollectionRevision(ctx)
	tx := &identityScriptedTx{fakeCommandTx: &fakeCommandTx{}, queryRows: []pgx.Row{scriptedValueRow([]any{revision + 1, true, make([]bool, len(collectionPermissions))})}}
	pool := &pgxpool.Pool{} // Identity only: no connection or real SQL execution.
	command := &commandTransaction{pool: pool, tenantID: actor.TenantID, tx: tx}
	selected := withCommandTransaction(ctx, command)
	value, err := withTenantCollectionRead(selected, pool, actor.TenantID, func(pgx.Tx) (string, error) { t.Fatal("stale policy reached selection"); return "private result", nil })
	if catalog.ErrorCodeOf(err) != catalog.CodeInvalidArgument || value != "" || len(tx.statements) != 0 || command.failed == nil {
		t.Fatal("failed policy leaked selection or left shared transaction usable", value, err)
	}
}

func TestCollectionPolicyBindingAndFailClosedErrors(t *testing.T) {
	ownerCtx := t.Context()
	ctx, actor, err := identity.WithCollectionContext(identityfixture.WithActor(ownerCtx, identity.Actor{TenantID: paginationTestTenantID, Subject: "collection-reader", Roles: []identity.Role{identity.RoleReader}}))
	if err != nil {
		t.Fatal(err)
	}
	revision, _ := identity.CollectionRevision(ctx)
	for _, mode := range []string{"success", "inactive", "stale", "query", "wrong count", "bind", "wrong tenant", "owner"} {
		t.Run(mode, func(t *testing.T) {
			allowed := make([]bool, len(collectionPermissions))
			allowed[1] = true
			selectedRevision, active := revision, true
			tenant := actor.TenantID
			selected := ctx
			sentinel := errors.New("private dependency diagnostic")
			tx := &identityScriptedTx{fakeCommandTx: &fakeCommandTx{}}
			switch mode {
			case "inactive":
				active = false
			case "stale":
				selectedRevision++
			case "wrong count":
				allowed = nil
			case "bind":
				tx.execErrors = []error{sentinel}
			case "wrong tenant":
				tenant = "00000000-0000-7000-8000-000000000099"
			case "owner":
				selected = ownerCtx
			}
			tx.queryRows = []pgx.Row{scriptedValueRow([]any{selectedRevision, active, allowed})}
			if mode == "query" {
				tx.queryRows = []pgx.Row{scannerFunc(func(...any) error { return sentinel })}
			}
			err := initializeCollectionPolicy(selected, tx, tenant)
			switch mode {
			case "success":
				if err != nil || len(tx.statements) != 1 || len(tx.queryRowArguments) != 1 {
					t.Fatal("policy binding", err)
				}
				var globals map[identity.Permission]bool
				data, ok := tx.arguments[0][0].(string)
				if !ok || json.Unmarshal([]byte(data), &globals) != nil || len(globals) != len(collectionPermissions) || !globals["resource.read"] || globals["federation.discover"] {
					t.Fatal("incorrect finite global decisions", globals)
				}
				if tx.queryRowArguments[0][0] != actor.TenantID || tx.queryRowArguments[0][1] != actor.MembershipID || !strings.Contains(tx.queryRowStatements[0], currentPermissionChecksSQL) {
					t.Fatal("principal binding or evaluator diverged")
				}
			case "owner":
				if err != nil || len(tx.queryRowStatements) != 0 || len(tx.statements) != 0 {
					t.Fatal("owner acquired user policy dependency", err)
				}
			default:
				if err == nil {
					t.Fatal("failed policy accepted")
				}
				if mode != "bind" && len(tx.statements) != 0 {
					t.Fatal("failed policy installed decisions")
				}
				if mode == "inactive" || mode == "wrong tenant" {
					if catalog.ErrorCodeOf(err) != catalog.CodeForbidden {
						t.Fatal(err)
					}
				}
				if mode == "stale" && catalog.ErrorCodeOf(err) != catalog.CodeInvalidArgument {
					t.Fatal(err)
				}
			}
		})
	}
}

func TestCollectionSQLSeparatesUserSelectionFromWorkerTemplates(t *testing.T) {
	ownerCtx := t.Context()
	ctx, _, err := identity.WithCollectionContext(identityfixture.WithActor(ownerCtx, identity.Actor{TenantID: paginationTestTenantID, Subject: "collection-reader", Roles: []identity.Role{identity.RoleReader}}))
	if err != nil {
		t.Fatal(err)
	}
	for name, statement := range map[string]string{
		"local":      catalogSearchBaseCTE,
		"indexed":    indexedCurrentEligibleFromSQL,
		"resources":  collectionPermissionSQL("resource.read", "resources.namespace_id"),
		"namespaces": collectionPermissionSQL("namespace.read", "namespaces.id"),
		"reviews":    collectionPermissionSQL("resource.read_history", "p.namespace_id"),
		"jobs":       jobCollectionSQL,
	} {
		t.Run(name, func(t *testing.T) {
			if selected := collectionStatement(ctx, statement); selected != statement {
				t.Fatal("user selection lost current policy predicate")
			}
			owner := collectionStatement(ownerCtx, statement)
			for _, private := range []string{"role_assignments", "role_definitions", "role_permissions", "access_group_members", "access_groups"} {
				if strings.Contains(owner, private) {
					t.Fatalf("worker requires unrelated policy-table authority: %s", private)
				}
			}
			if strings.Contains(owner, "app.collection_permissions") && name != "jobs" {
				t.Fatal("owner retained user selection decisions")
			}
		})
	}
	for _, namespace := range []string{"", "resources.namespace_id"} {
		if statement := collectionPermissionSQL("resource.read", namespace); !strings.Contains(statement, "(SELECT coalesce((nullif(current_setting('app.collection_permissions'") {
			t.Fatal("global decision no longer uses a statement-local scalar query")
		}
	}
	for _, private := range []string{"role_assignments", "access_group_members"} {
		if strings.Contains(semanticControlInputsCTE, private) {
			t.Fatal("worker indexing includes user authorization SQL")
		}
	}
}
