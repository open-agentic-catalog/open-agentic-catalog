package database

import (
	"context"
	"errors"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"reflect"
	"strings"
	"testing"
)

func authorizationUnitActor() identity.Actor {
	return identity.Actor{TenantID: "00000000-0000-7000-8000-000000000001", RealmID: "00000000-0000-7000-8000-000000000010", MembershipID: "00000000-0000-7000-8000-000000000020", Subject: "member", ActorType: identity.ActorHuman}
}
func TestCurrentPermissionQueryFailureAndScopeBindings(t *testing.T) {
	actor := authorizationUnitActor()
	checks := []identity.PermissionCheck{{Permission: "resource.read", NamespaceID: "00000000-0000-7000-8000-000000000030"}, {Permission: "access.manage"}}
	sentinel := errors.New("query unavailable")
	for _, mode := range []string{"success", "invalid actor", "missing state", "query", "scan", "wrong count", "null decisions", "extra decisions"} {
		t.Run(mode, func(t *testing.T) {
			tx := &identityScriptedTx{queryRows: []pgx.Row{scriptedValueRow([]any{int64(9), []bool{true, false}})}}
			principal := actor
			switch mode {
			case "invalid actor":
				principal.MembershipID = "bad"
			case "missing state":
				tx.queryRows[0] = scannerFunc(func(...any) error { return pgx.ErrNoRows })
			case "query":
				tx.queryRows[0] = scannerFunc(func(...any) error { return sentinel })
			case "scan":
				tx.queryRows[0] = scriptedValueRow([]any{})
			case "wrong count":
				tx.queryRows[0] = scriptedValueRow([]any{int64(9), []bool{true}})
			case "null decisions":
				tx.queryRows[0] = scriptedValueRow([]any{int64(9), []bool(nil)})
			case "extra decisions":
				tx.queryRows[0] = scriptedValueRow([]any{int64(9), []bool{true, false, true}})
			}
			got, err := checkCurrentPermissions(t.Context(), tx, principal, checks)
			if mode == "success" {
				if err != nil || !reflect.DeepEqual(got, identity.PermissionDecision{Revision: 9, Allowed: []bool{true, false}}) {
					t.Fatalf("%v %v", got, err)
				}
				want := []any{actor.TenantID, actor.MembershipID, actor.RealmID, actor.Subject, actor.ActorType, []string{"resource.read", "access.manage"}, []string{checks[0].NamespaceID, ""}}
				if !reflect.DeepEqual(tx.queryRowArguments[0], want) {
					t.Fatal("lost identity/scope bindings")
				}
				if len(tx.queryRowStatements) != 1 || len(tx.queryStatements) != 0 || !strings.Contains(tx.queryRowStatements[0], "ARRAY("+currentPermissionChecksSQL+")") {
					t.Fatal("revision and ordered decisions did not share one statement")
				}
			} else if err == nil {
				t.Fatal("failed dependency accepted")
			}
			if mode == "query" && !errors.Is(err, sentinel) || mode == "missing state" && !errors.Is(err, pgx.ErrNoRows) {
				t.Fatal("dependency cause was lost")
			}
		})
	}
}
func TestPermissionAdmissionAndWriterFailurePoisonCommand(t *testing.T) {
	actor := authorizationUnitActor()
	sentinel := errors.New("failed operation")
	for _, mode := range []string{"success", "denied", "callback", "panic", "shared upgrade", "writer failure", "stale administrator", "current administrator", "closed", "unfenced read"} {
		t.Run(mode, func(t *testing.T) {
			pool := &pgxpool.Pool{}
			repo, _ := NewPostgresRepository(pool)
			store, _ := NewPostgresIdentityStore(repo)
			allowed := []bool{true}
			tx := &identityScriptedTx{fakeCommandTx: &fakeCommandTx{}, queryRows: []pgx.Row{scriptedValueRow([]any{int64(9)}), scriptedValueRow([]any{int64(9), allowed})}}
			command := &commandTransaction{pool: pool, tenantID: actor.TenantID, tx: tx}
			ctx := withCommandTransaction(t.Context(), command)
			checks := []identity.PermissionCheck{{Permission: "resource.read"}}
			switch mode {
			case "denied":
				allowed[0] = false
			case "writer failure":
				tx.authorizationLockError = sentinel
			case "stale administrator":
				allowed[0] = false
				tx.queryRows = tx.queryRows[1:]
			case "current administrator":
				tx.queryRows = tx.queryRows[1:]
			case "closed":
				command.closed = true
			}
			if strings.Contains(mode, "administrator") || mode == "writer failure" {
				err := lockAuthorizationWriter(identity.WithActor(ctx, actor), tx, actor.TenantID)
				if mode == "current administrator" {
					if err != nil || !command.authorizationExclusive {
						t.Fatal(err)
					}
				} else if err == nil {
					t.Fatal("writer admitted without current authority")
				}
				if len(tx.authorizationLocks) != 1 || !reflect.DeepEqual(tx.authorizationLockArgs[0], []any{actor.TenantID}) {
					t.Fatal("exclusive lock lost scope")
				}
				return
			}
			if mode == "closed" || mode == "unfenced read" {
				if _, err := store.CheckPermissions(ctx, actor, checks); err == nil {
					t.Fatal("invalid command read admitted")
				}
				return
			}
			called := false
			var recovered any
			var err error
			func() {
				defer func() { recovered = recover() }()
				err = store.WithPermissionAdmission(ctx, actor, checks, func(ctx context.Context) error {
					called = true
					switch mode {
					case "callback":
						return sentinel
					case "panic":
						panic("private panic")
					case "shared upgrade":
						if e := lockAuthorizationWriter(ctx, tx, actor.TenantID); identity.ErrorCodeOf(e) != identity.CodeConflict {
							t.Fatal("shared upgrade accepted")
						}
					}
					return nil
				})
			}()
			if mode == "success" || mode == "shared upgrade" {
				if err != nil || !called || command.failed != nil {
					t.Fatalf("%v", err)
				}
				if len(tx.queryRowStatements) != 2 || !strings.Contains(tx.queryRowStatements[0], "FOR SHARE") || !strings.Contains(tx.queryRowStatements[1], "ARRAY(") || strings.Contains(tx.queryRowStatements[1], "FOR SHARE") {
					t.Fatal("permission evaluation must use a separate fresh statement after the admission lock")
				}
			} else if mode == "panic" && recovered == nil || mode != "panic" && err == nil || command.failed == nil || tx.rollbacks != 1 {
				t.Fatalf("err=%v failed=%v recovered=%v rollbacks=%d", err, command.failed, recovered, tx.rollbacks)
			}
			if mode == "denied" && called {
				t.Fatal("denied callback executed")
			}
		})
	}
}
func TestPermissionArgumentsRejectBeforeDatabaseAcquisition(t *testing.T) {
	repo, _ := NewPostgresRepository(&pgxpool.Pool{})
	store, _ := NewPostgresIdentityStore(repo)
	actor := authorizationUnitActor()
	if _, err := store.CheckPermissions(t.Context(), actor, []identity.PermissionCheck{{Permission: "unknown"}}); identity.ErrorCodeOf(err) != identity.CodeInvalidArgument {
		t.Fatal(err)
	}
	if _, err := store.CheckPermissions(t.Context(), identity.Actor{}, []identity.PermissionCheck{{Permission: "resource.read"}}); identity.ErrorCodeOf(err) != identity.CodeUnauthenticated {
		t.Fatal(err)
	}
	if err := store.WithPermissionAdmission(t.Context(), actor, []identity.PermissionCheck{{Permission: "resource.read"}}, nil); identity.ErrorCodeOf(err) != identity.CodeInvalidArgument {
		t.Fatal(err)
	}
	if err := store.WithPermissionAdmission(t.Context(), identity.Actor{}, []identity.PermissionCheck{{Permission: "resource.read"}}, func(context.Context) error { return nil }); identity.ErrorCodeOf(err) != identity.CodeUnauthenticated {
		t.Fatal(err)
	}
	different := actor
	different.Subject = "other"
	if err := store.WithPermissionAdmission(identity.WithActor(t.Context(), actor), different, []identity.PermissionCheck{{Permission: "resource.read"}}, func(context.Context) error { t.Fatal("mismatched admission acquired database"); return nil }); identity.ErrorCodeOf(err) != identity.CodeForbidden {
		t.Fatal(err)
	}

}

func TestInternalJobAdmissionRechecksClaimBeforeRepositoryWork(t *testing.T) {
	for _, readOnly := range []bool{false, true} {
		for _, mode := range []string{"current", "lost lease", "driver failure"} {
			t.Run(map[bool]string{false: "write/", true: "read/"}[readOnly]+mode, func(t *testing.T) {
				store, tx, ctx, job := jobRepositoryFixture(t)
				ctx, err := identity.WithInternalJobAuthority(ctx, "source.sync", job.TenantID, jobSourceID, job.ID, "worker-one", 2, func(context.Context) error { return nil })
				if err != nil {
					t.Fatal(err)
				}
				failure := errors.New("lease check interrupted")
				row := scriptedValueRow([]any{1})
				if mode == "lost lease" {
					row = scannerFunc(func(...any) error { return pgx.ErrNoRows })
				} else if mode == "driver failure" {
					row = scannerFunc(func(...any) error { return failure })
				}
				tx.queryRows = []pgx.Row{row}
				options := pgx.TxOptions{}
				if readOnly {
					options.AccessMode = pgx.ReadOnly
				}
				called := false
				_, err = withTenantTx(ctx, store.repository.pool, job.TenantID, options, func(pgx.Tx) (bool, error) { called = true; return true, nil })
				if mode == "current" {
					if err != nil || !called || tx.rollbacks != 0 {
						t.Fatal("current claim refused repository work", err)
					}
				} else {
					code := catalog.CodeForbidden
					if mode == "driver failure" {
						code = catalog.CodeDependencyUnavailable
					}
					if called || catalog.ErrorCodeOf(err) != code || tx.rollbacks != 1 || mode == "driver failure" && !errors.Is(err, failure) {
						t.Fatal("invalid claim admitted work or lost its cause", err)
					}
				}
				if !reflect.DeepEqual(tx.queryRowArguments, [][]any{{job.TenantID, job.ID, "worker-one", 2, "source.sync"}}) || strings.HasSuffix(tx.queryRowStatements[0], " FOR SHARE") == readOnly {
					t.Fatal("lease admission lost its exact binding or write lock")
				}
			})
		}
	}
}
