package database

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
)

func TestAuthorizationProjectionRowsCloseOnDecodeAndIterationFailures(t *testing.T) {
	for _, mode := range []string{"empty", "decode", "iteration"} {
		t.Run(mode, func(t *testing.T) {
			rows := &identityScriptedRows{}
			if mode == "decode" {
				rows.values = [][]any{{"invalid arity"}}
			}
			if mode == "iteration" {
				rows.err = errors.New("cursor failed")
			}
			items, err := collectAuthorizationRows(rows, scanDefinition)
			if !rows.closed {
				t.Fatal("projection rows left open")
			}
			if mode == "empty" {
				if err != nil || items == nil || len(items) != 0 {
					t.Fatal("empty collection projection", err)
				}
			} else if err == nil {
				t.Fatal("row failure accepted")
			}
		})
	}
	// Scan only real retained fields; namespace capability is derived from their
	// finite permission set, never taken from a caller-supplied display flag.
	at := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	row := scriptedValueRow([]any{"role", "tenant", "", false, "Custom role", "Description", identity.DefinitionActive, int64(7), at, at, []identity.Permission{"resource.read", "namespace.read"}})
	definition, err := scanDefinition(row)
	if err != nil || !definition.NamespaceCapable || definition.Revision != 7 || definition.Builtin {
		t.Fatalf("role projection=%+v %v", definition, err)
	}
	_, err = scanAccessGroup(scriptedValueRow([]any{"invalid arity"}))
	if err == nil {
		t.Fatal("group scan failure accepted")
	}
	_, err = scanAccessAssignment(scriptedValueRow([]any{"invalid arity"}))
	if err == nil {
		t.Fatal("assignment scan failure accepted")
	}
	_, err = scanGroupMember(scriptedValueRow([]any{"invalid arity"}))
	if err == nil {
		t.Fatal("member scan failure accepted")
	}
}
func TestAuthorizationAdministrationReadRejectsUnsafeTransactionContexts(t *testing.T) {
	actor := authorizationUnitActor()
	pool := &pgxpool.Pool{}
	store := &PostgresIdentityStore{repository: &PostgresRepository{pool: pool}}
	for _, mode := range []string{"invalid page", "unfenced command", "administrator denied", "stale revision"} {
		t.Run(mode, func(t *testing.T) {
			request := identity.AuthorizationPageRequest{Limit: 21, AuthorizationRevision: 9}
			allowed := []bool{true}
			tx := &identityScriptedTx{fakeCommandTx: &fakeCommandTx{}, queryRows: []pgx.Row{scriptedValueRow([]any{int64(9), allowed})}}
			ctx := identityRepositoryContext(pool, tx)
			commandTransactionFrom(ctx).authorizationShared = true
			switch mode {
			case "invalid page":
				request.Limit = 102
			case "unfenced command":
				commandTransactionFrom(ctx).authorizationShared = false
			case "administrator denied":
				allowed[0] = false
			case "stale revision":
				request.AuthorizationRevision = 8
			}
			_, err := store.ListDefinitions(ctx, actor, request)
			if err == nil {
				t.Fatal("unsafe read accepted")
			}
			if (mode == "invalid page" || mode == "unfenced command") && (len(tx.queryRowStatements) > 0 || len(tx.queryStatements) > 0) {
				t.Fatal("invalid read reached database")
			}
			if mode == "administrator denied" && identity.ErrorCodeOf(err) != identity.CodeForbidden {
				t.Fatal("administrator refusal classification", err)
			}
			if mode == "stale revision" && identity.ErrorCodeOf(err) != identity.CodeConflict {
				t.Fatal("snapshot drift classification", err)
			}
		})
	}
}

// These scripted boundaries qualify error propagation and refusal before
// callbacks. They do not claim to prove PostgreSQL isolation or SQL eligibility.
func TestAuthorizationAdministrationBoundaryFailures(t *testing.T) {
	actor := authorizationUnitActor()
	failure := errors.New("private policy-store failure")
	for _, mode := range []string{"read binding", "write binding", "read permission", "self inactive", "self query", "write fence", "group query", "group scan", "group success"} {
		t.Run(mode, func(t *testing.T) {
			pool := &pgxpool.Pool{}
			store := &PostgresIdentityStore{repository: &PostgresRepository{pool: pool}}
			allowed := []bool{true}
			tx := &identityScriptedTx{fakeCommandTx: &fakeCommandTx{}, queryRows: []pgx.Row{scriptedValueRow([]any{int64(9), allowed})}}
			ctx := identityRepositoryContext(pool, tx)
			commandTransactionFrom(ctx).authorizationShared = true
			principal := actor
			code := identity.CodeDependency
			switch mode {
			case "read binding", "write binding":
				principal.MembershipID = "invalid"
				code = identity.CodeUnauthenticated
			case "read permission":
				tx.queryRows[0] = scannerFunc(func(...any) error { return failure })
			case "self inactive", "self query":
				allowed[0] = false
				row := scriptedValueRow([]any{false})
				if mode == "self query" {
					row = scannerFunc(func(...any) error { return failure })
				} else {
					code = identity.CodeUnauthenticated
				}
				tx.queryRows = append(tx.queryRows, row)
			case "write fence":
				commandTransactionFrom(ctx).authorizationShared = false
				tx.authorizationLockError = failure
			case "group query":
				tx.queryResults = append(tx.queryResults, identityQueryResult{err: failure})
			case "group scan":
				tx.queryResults = append(tx.queryResults, identityQueryResult{rows: &identityScriptedRows{values: [][]any{{"bad arity"}}}})
			case "group success":
				at := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
				tx.queryResults = append(tx.queryResults, identityQueryResult{rows: &identityScriptedRows{values: [][]any{{"group", actor.TenantID, actor.RealmID, "Group", "Description", identity.GroupActive, int64(1), at, at}}}})
			}
			called := false
			var err error
			switch mode {
			case "write binding", "write fence":
				_, err = postgresAuthorizationWrite(ctx, store, principal, func(pgx.Tx) (bool, error) { called = true; return true, nil })
			case "group query", "group scan", "group success":
				items, readErr := store.ListGroups(ctx, principal, identity.AuthorizationPageRequest{Limit: 21, AuthorizationRevision: 9})
				err = readErr
				if mode == "group success" {
					if err != nil || len(items) != 1 || items[0].ID != "group" || items[0].RealmID != actor.RealmID || !reflect.DeepEqual(tx.queryArguments[0], []any{actor.TenantID, actor.RealmID, "", 21}) {
						t.Fatal("group projection or bounded tenant/realm binding changed", err)
					}
					return
				}
			default:
				_, err = postgresAuthorizationRead(ctx, store, principal, nil, strings.HasPrefix(mode, "self"), func(pgx.Tx) (bool, error) { called = true; return true, nil })
			}
			if err == nil || called || identity.ErrorCodeOf(err) != code {
				t.Fatal("boundary failure admitted work or lost classification", err)
			}
			if (mode == "read permission" || mode == "self query" || mode == "write fence" || mode == "group query") && !errors.Is(err, failure) {
				t.Fatal("dependency cause lost")
			}
			if strings.HasSuffix(mode, "binding") && (len(tx.queryStatements) != 0 || len(tx.queryRowStatements) != 0 || len(tx.authorizationLocks) != 0) {
				t.Fatal("invalid identity reached SQL")
			}
		})
	}
	actorChange := identity.AdministrationChange{Actor: actor, ID: "target"}
	tx := &identityScriptedTx{fakeCommandTx: &fakeCommandTx{}}
	if err := insertAuthorizationAudit(t.Context(), tx, actorChange, "authorization.role.updated", "role_definition", nil); identity.ErrorCodeOf(err) != identity.CodeInternal || len(tx.statements) != 0 {
		t.Fatal("unbound audit reached persistence", err)
	}
}

func TestEffectiveAssignmentProjectionJoinsOnlySelectedRoles(t *testing.T) {
	actor := authorizationUnitActor()
	at := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	const assignmentID = "00000000-0000-7000-8000-000000000041"
	const definitionID = "00000000-0000-7000-8000-000000000042"
	const namespaceID = "00000000-0000-7000-8000-000000000043"
	const cursor = "00000000-0000-7000-8000-000000000040"
	wantAssignment := identity.AccessAssignment{ID: assignmentID, TenantID: actor.TenantID, RealmID: actor.RealmID, RoleDefinitionID: definitionID, MembershipID: actor.MembershipID, NamespaceID: namespaceID, State: identity.AssignmentActive, Revision: 2, GrantedBy: "administrator", GrantReason: "Grant project access", GrantedAt: at}
	wantRole := identity.RoleDefinition{ID: definitionID, TenantID: actor.TenantID, Name: "Project reader", Description: "Read this project", State: identity.DefinitionActive, Revision: 3, CreatedAt: at, UpdatedAt: at, Permissions: []identity.Permission{"resource.read"}, NamespaceCapable: true}
	for _, mode := range []string{"selected role", "no assignments", "role query failure"} {
		t.Run(mode, func(t *testing.T) {
			pool := &pgxpool.Pool{}
			store := &PostgresIdentityStore{repository: &PostgresRepository{pool: pool}}
			assignments := &identityScriptedRows{values: [][]any{{assignmentID, actor.TenantID, actor.RealmID, definitionID, actor.MembershipID, "", namespaceID, identity.AssignmentActive, int64(2), wantAssignment.GrantedBy, wantAssignment.GrantReason, at, "", "", (*time.Time)(nil)}}}
			definitions := &identityScriptedRows{values: [][]any{{definitionID, actor.TenantID, "", false, wantRole.Name, wantRole.Description, identity.DefinitionActive, int64(3), at, at, []identity.Permission{"resource.read"}}}}
			tx := &identityScriptedTx{fakeCommandTx: &fakeCommandTx{}, queryRows: []pgx.Row{scriptedValueRow([]any{int64(9), []bool{false}}), scriptedValueRow([]any{true})}, queryResults: []identityQueryResult{{rows: assignments}, {rows: definitions}}}
			failure := errors.New("role projection interrupted")
			switch mode {
			case "no assignments":
				assignments.values = nil
			case "role query failure":
				tx.queryResults[1].err = failure
			}
			ctx := identityRepositoryContext(pool, tx)
			commandTransactionFrom(ctx).authorizationShared = true
			got, err := store.ListEffectiveAssignments(ctx, actor, identity.AuthorizationPageRequest{AuthorizationRevision: 9, Limit: 2, AfterID: cursor})
			switch mode {
			case "selected role":
				want := []identity.EffectiveAssignment{{Assignment: wantAssignment, Role: wantRole}}
				if err != nil || !reflect.DeepEqual(got, want) {
					t.Fatalf("effective access=%+v error=%v", got, err)
				}
			case "no assignments":
				if err != nil || got == nil || len(got) != 0 || len(tx.queryStatements) != 1 {
					t.Fatal("empty access performed role hydration or returned null", err)
				}
			case "role query failure":
				if got != nil || identity.ErrorCodeOf(err) != identity.CodeDependency || !errors.Is(err, failure) {
					t.Fatal("failed effective access was disclosed", err)
				}
			}
			if !assignments.closed || mode != "no assignments" && mode != "role query failure" && !definitions.closed {
				t.Fatal("authorization projection leaked rows")
			}
			if !reflect.DeepEqual(tx.queryArguments[0], []any{actor.TenantID, actor.MembershipID, actor.RealmID, cursor, 2}) || !reflect.DeepEqual(tx.queryRowArguments[1], []any{actor.TenantID, actor.MembershipID, actor.RealmID, actor.Subject, actor.ActorType}) {
				t.Fatal("effective access lost exact current identity or continuation")
			}
			if mode != "no assignments" && !reflect.DeepEqual(tx.queryArguments[1], []any{actor.TenantID, []string{definitionID}, 2}) {
				t.Fatal("role hydration exceeded selected assignment scope")
			}
		})
	}
}

func TestEffectiveAssignmentCaptionsStayBoundedAndFailClosed(t *testing.T) {
	actor := authorizationUnitActor()
	at := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	const roleID = "00000000-0000-7000-8000-000000000042"
	const groupID = "00000000-0000-7000-8000-000000000043"
	for _, mode := range []string{"current caption", "missing caption", "query failure", "scan failure", "iteration failure"} {
		t.Run(mode, func(t *testing.T) {
			pool := &pgxpool.Pool{}
			store := &PostgresIdentityStore{repository: &PostgresRepository{pool: pool}}
			assignments := &identityScriptedRows{values: [][]any{
				{"00000000-0000-7000-8000-000000000041", actor.TenantID, actor.RealmID, roleID, "", groupID, "", identity.AssignmentActive, int64(1), "administrator", "Group grant", at, "", "", (*time.Time)(nil)},
				{"00000000-0000-7000-8000-000000000044", actor.TenantID, actor.RealmID, roleID, "", groupID, "00000000-0000-7000-8000-000000000045", identity.AssignmentActive, int64(1), "administrator", "Scoped group grant", at, "", "", (*time.Time)(nil)},
			}}
			definitions := &identityScriptedRows{values: [][]any{{roleID, actor.TenantID, "", false, "Read project metadata", "", identity.DefinitionActive, int64(1), at, at, []identity.Permission{"resource.read"}}}}
			captions := &identityScriptedRows{values: [][]any{{groupID, "Current team caption"}}}
			tx := &identityScriptedTx{fakeCommandTx: &fakeCommandTx{}, queryRows: []pgx.Row{scriptedValueRow([]any{int64(9), []bool{false}}), scriptedValueRow([]any{true})}, queryResults: []identityQueryResult{{rows: assignments}, {rows: definitions}, {rows: captions}}}
			switch mode {
			case "missing caption":
				captions.values = nil
			case "query failure":
				tx.queryResults[2].err = errors.New("caption query interrupted")
			case "scan failure":
				captions.values = [][]any{{groupID}}
			case "iteration failure":
				captions.err = errors.New("caption iteration interrupted")
			}
			ctx := identityRepositoryContext(pool, tx)
			commandTransactionFrom(ctx).authorizationShared = true
			got, err := store.ListEffectiveAssignments(ctx, actor, identity.AuthorizationPageRequest{AuthorizationRevision: 9, Limit: 3})
			if mode == "current caption" {
				if err != nil || len(got) != 2 || got[0].GroupName != "Current team caption" || got[1].GroupName != "Current team caption" || got[0].Assignment.ID == got[1].Assignment.ID {
					t.Fatal("bounded self captions lost exact assignment paths", got, err)
				}
			} else if err == nil || got != nil {
				t.Fatal("incomplete captions were returned as successful self evidence", got, err)
			}
			if !reflect.DeepEqual(tx.queryArguments[2], []any{actor.TenantID, actor.RealmID, []string{groupID}, 3}) || !strings.Contains(tx.queryStatements[2], "id=ANY($3::uuid[]) LIMIT $4") {
				t.Fatal("self captions escaped the selected page or current realm", tx.queryArguments)
			}
			if !assignments.closed || !definitions.closed || mode != "query failure" && !captions.closed {
				t.Fatal("self caption projection leaked rows")
			}
		})
	}
}
