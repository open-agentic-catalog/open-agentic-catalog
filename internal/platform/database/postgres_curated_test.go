package database

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/audit"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/collections"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/test/identityfixture"
)

// These scripts qualify argument binding, decoding and command failure handling.
// The PostgreSQL integration suite owns SQL, privileges and isolation enforcement.
func curatedStorageControl(t *testing.T, tx *identityScriptedTx) (*PostgresCollectionStore, context.Context, identity.Actor, *commandTransaction) {
	t.Helper()
	pool := &pgxpool.Pool{} // Identity only; never connects.
	repository, err := NewPostgresRepository(pool)
	if err != nil {
		t.Fatal(err)
	}
	store, err := NewPostgresCollectionStore(repository)
	if err != nil {
		t.Fatal(err)
	}
	ctx := identityfixture.WithActor(identityRepositoryContext(pool, tx), identity.Actor{TenantID: restoreTestTenant, MembershipID: "00000000-0000-7000-8000-000000000002", RealmID: "00000000-0000-7000-8000-000000000003", ActorType: identity.ActorHuman, Subject: "curator", Roles: []identity.Role{identity.RoleTenantAdmin}})
	a, err := collections.Actor(ctx)
	if err != nil {
		t.Fatal(err)
	}
	return store, ctx, a, commandTransactionFrom(ctx)
}

func curatedStorageItem() collections.Collection {
	now := time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)
	return collections.Collection{ID: "00000000-0000-7000-8000-000000000004", NamespaceID: "00000000-0000-7000-8000-000000000005", Name: "Finance tools", Description: "Reviewed examples", Revision: 1, CreatedAt: now, UpdatedAt: now}
}

func curatedStorageValues(c collections.Collection) []any {
	return []any{c.ID, c.NamespaceID, c.Name, c.Description, c.Revision, c.CreatedAt, c.UpdatedAt}
}

func curatedStorageEvidence(t *testing.T, a identity.Actor, c collections.Collection, op collections.Operation, listing string) audit.Record {
	t.Helper()
	facts := map[string]any{"revision": c.Revision, "namespaceId": c.NamespaceID}
	if listing != "" {
		facts["listingId"] = listing
	}
	e, err := audit.NewRecord("00000000-0000-7000-8000-000000000009", a.TenantID, audit.Principal{ID: a.MembershipID, Type: audit.PrincipalCatalogMembership, ActorType: string(a.ActorType), RealmID: a.RealmID}, "collection."+string(op), "collection", c.ID, "", audit.Execution{}, facts, c.UpdatedAt)
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func TestPostgresCuratedReadsBindTenantAndSelection(t *testing.T) {
	if _, err := NewPostgresCollectionStore(nil); err == nil {
		t.Fatal("accepted missing repository")
	}
	want := curatedStorageItem()
	tx := &identityScriptedTx{fakeCommandTx: &fakeCommandTx{}, queryRows: []pgx.Row{scriptedValueRow(curatedStorageValues(want)), scriptedValueRow([]any{want.NamespaceID})}}
	store, ctx, a, _ := curatedStorageControl(t, tx)
	got, err := store.Get(ctx, a, want.ID)
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("collection = %+v, %v", got, err)
	}
	ns, err := store.Target(ctx, a, "listing-id")
	if err != nil || ns != want.NamespaceID {
		t.Fatalf("target namespace = %q, %v", ns, err)
	}
	if !reflect.DeepEqual(tx.queryRowArguments, [][]any{{a.TenantID, want.ID}, {a.TenantID, "listing-id"}}) {
		t.Fatalf("read bindings = %v", tx.queryRowArguments)
	}
	rows := &identityScriptedRows{values: [][]any{curatedStorageValues(want)}}
	tx.queryResults = []identityQueryResult{{rows: rows}}
	q := collections.ListRequest{NamespaceID: want.NamespaceID, AfterID: "previous", Limit: 101}
	items, err := store.List(ctx, a, q)
	if err != nil || !reflect.DeepEqual(items, []collections.Collection{want}) || !rows.closed {
		t.Fatalf("page=%+v closed=%v err=%v", items, rows.closed, err)
	}
	if !reflect.DeepEqual(tx.queryArguments[0], []any{a.TenantID, q.NamespaceID, q.AfterID, 101}) {
		t.Fatalf("page bindings = %v", tx.queryArguments)
	}
}

func TestPostgresCuratedRefusesUnboundPrincipalsBeforeIO(t *testing.T) {
	for _, mode := range []string{"missing", "tenant", "membership", "realm", "invalid current principal"} {
		t.Run(mode, func(t *testing.T) {
			tx := &identityScriptedTx{fakeCommandTx: &fakeCommandTx{}}
			store, ctx, a, _ := curatedStorageControl(t, tx)
			switch mode {
			case "missing":
				ctx = t.Context()
			case "tenant":
				a.TenantID = curatedStorageItem().ID
			case "membership":
				a.MembershipID = curatedStorageItem().ID
			case "realm":
				a.RealmID = curatedStorageItem().ID
			case "invalid current principal":
				invalid := a
				invalid.Subject = ""
				ctx = identity.WithActor(ctx, invalid)
			}
			_, getErr := store.Get(ctx, a, "id")
			_, listErr := store.List(ctx, a, collections.ListRequest{Limit: 1})
			_, targetErr := store.Target(ctx, a, "id")
			_, membersErr := store.Members(ctx, a, "id", collections.MemberRequest{})
			_, saveErr := store.Save(ctx, a, curatedStorageItem(), 0, collections.Create, "", audit.Record{})
			for _, err := range []error{getErr, listErr, targetErr, membersErr, saveErr} {
				if catalog.ErrorCodeOf(err) != catalog.CodeForbidden {
					t.Fatalf("unbound principal = %v", err)
				}
			}
			if len(tx.queryRowStatements)+len(tx.queryStatements)+len(tx.statements) != 0 {
				t.Fatal("unbound principal reached storage")
			}
		})
	}
}

func TestPostgresCuratedListFailuresDiscardPartialRows(t *testing.T) {
	for _, mode := range []string{"zero", "negative", "over limit", "query", "scan", "iteration", "empty"} {
		t.Run(mode, func(t *testing.T) {
			rows := &identityScriptedRows{values: [][]any{curatedStorageValues(curatedStorageItem())}}
			tx := &identityScriptedTx{fakeCommandTx: &fakeCommandTx{}, queryResults: []identityQueryResult{{rows: rows}}}
			store, ctx, a, _ := curatedStorageControl(t, tx)
			q := collections.ListRequest{Limit: 2}
			code := catalog.CodeDependencyUnavailable
			switch mode {
			case "zero":
				q.Limit = 0
				code = catalog.CodeInvalidArgument
			case "negative":
				q.Limit = -1
				code = catalog.CodeInvalidArgument
			case "over limit":
				q.Limit = 102
				code = catalog.CodeInvalidArgument
			case "query":
				tx.queryResults[0].err = errors.New("query failed")
			case "scan":
				rows.values = append(rows.values, []any{"incomplete row"})
			case "iteration":
				rows.err = errors.New("iteration failed")
			case "empty":
				rows.values = nil
			}
			got, err := store.List(ctx, a, q)
			if mode == "empty" {
				if err != nil || got == nil || len(got) != 0 {
					t.Fatalf("empty page=%v, %v", got, err)
				}
				return
			}
			if catalog.ErrorCodeOf(err) != code || got != nil {
				t.Fatalf("failed page = %v, %v", got, err)
			}
			if code == catalog.CodeInvalidArgument && len(tx.queryStatements) != 0 {
				t.Fatal("invalid bound reached storage")
			}
			if (mode == "scan" || mode == "iteration") && !rows.closed {
				t.Fatal("failed rows not closed")
			}
		})
	}
}

func TestPostgresCuratedMemberSnapshotAndFailures(t *testing.T) {
	for _, mode := range []string{"binding", "empty", "stale revision", "stale generation", "negative", "over limit", "parent", "generation", "query", "scan", "iteration", "hydration"} {
		t.Run(mode, func(t *testing.T) {
			item := curatedStorageItem()
			rows := &identityScriptedRows{}
			tx := &identityScriptedTx{fakeCommandTx: &fakeCommandTx{}, queryRows: []pgx.Row{scriptedValueRow(curatedStorageValues(item)), scriptedValueRow([]any{int64(7)})}, queryResults: []identityQueryResult{{rows: rows}}}
			store, ctx, a, _ := curatedStorageControl(t, tx)
			q := collections.MemberRequest{Revision: item.Revision, Generation: "7", AfterID: "previous", Limit: 101}
			code := catalog.CodeDependencyUnavailable
			switch mode {
			case "binding":
				q.Limit = 0
			case "stale revision":
				q.Revision++
				code = catalog.CodeInvalidArgument
			case "stale generation":
				q.Generation = "8"
				code = catalog.CodeInvalidArgument
			case "negative":
				q.Limit = -1
				code = catalog.CodeInvalidArgument
			case "over limit":
				q.Limit = 102
				code = catalog.CodeInvalidArgument
			case "parent":
				tx.queryRows[0] = scannerFunc(func(...any) error { return pgx.ErrNoRows })
				code = catalog.CodeNotFound
			case "generation":
				tx.queryRows[1] = scannerFunc(func(...any) error { return errors.New("generation failed") })
			case "query":
				tx.queryResults[0].err = errors.New("selection failed")
			case "scan":
				rows.values = [][]any{{"too", "many"}}
			case "iteration":
				rows.values = [][]any{{"release"}}
				rows.err = errors.New("iteration failed")
			case "hydration":
				rows.values = [][]any{{"release"}}
				tx.queryResults = append(tx.queryResults, identityQueryResult{err: errors.New("summary failed")})
			}
			got, err := store.Members(ctx, a, item.ID, q)
			if mode == "binding" || mode == "empty" {
				if err != nil || got.Collection != item || got.Generation != "7" || got.Items == nil || len(got.Items) != 0 {
					t.Fatalf("snapshot = %+v, %v", got, err)
				}
				if mode == "binding" && len(tx.queryStatements) != 0 {
					t.Fatal("binding-only read selected members")
				}
				if mode == "empty" && (!rows.closed || !reflect.DeepEqual(tx.queryArguments[0], []any{a.TenantID, item.ID, q.AfterID, q.Limit})) {
					t.Fatalf("member bindings = %v, closed=%v", tx.queryArguments, rows.closed)
				}
				return
			}
			if catalog.ErrorCodeOf(err) != code || !reflect.DeepEqual(got, collections.MemberPage{}) {
				t.Fatalf("failed member page=%+v, %v", got, err)
			}
			if (mode == "scan" || mode == "iteration" || mode == "hydration") && !rows.closed {
				t.Fatal("member rows not closed")
			}
			if code == catalog.CodeInvalidArgument && len(tx.queryStatements) != 0 {
				t.Fatal("invalid snapshot reached member selection")
			}
		})
	}
}

func TestPostgresCuratedMembersPreserveSelectedOrderAfterHydration(t *testing.T) {
	item := curatedStorageItem()
	selected := &identityScriptedRows{values: [][]any{{"release-c"}, {"release-b"}, {"release-a"}}}
	results, hydrated := searchHydrationResults()
	hydrated[0].values = [][]any{{}, {}, {}}
	scan := hydrated[0].scan
	hydrated[0].scan = func(dst ...any) error {
		if err := scan(dst...); err != nil {
			return err
		}
		// The summary query may return a different order from member selection.
		setScanDestination(t, dst, 18, []string{"release-a", "release-b", "release-c"}[hydrated[0].index-1])
		return nil
	}
	tx := &identityScriptedTx{
		fakeCommandTx: &fakeCommandTx{},
		queryRows:     []pgx.Row{scriptedValueRow(curatedStorageValues(item)), scriptedValueRow([]any{int64(7)})},
		queryResults:  append([]identityQueryResult{{rows: selected}}, results...),
	}
	store, ctx, a, _ := curatedStorageControl(t, tx)
	page, err := store.Members(ctx, a, item.ID, collections.MemberRequest{Revision: item.Revision, Generation: "7", Limit: 3})
	if err != nil || page.Collection != item || page.Generation != "7" || len(page.Items) != 3 {
		t.Fatalf("member page=%+v, %v", page, err)
	}
	if page.Items[0].Release.ID != "release-c" || page.Items[1].Release.ID != "release-b" || page.Items[2].Release.ID != "release-a" {
		t.Fatalf("hydration changed selected order: %+v", page.Items)
	}
	if len(tx.queryArguments) != 3 || !reflect.DeepEqual(tx.queryArguments[1], []any{a.TenantID, []string{"release-c", "release-b", "release-a"}}) || !reflect.DeepEqual(tx.queryArguments[2], []any{a.TenantID, []string{"version"}}) {
		t.Fatalf("hydration lost bounded same-tenant identities: %v", tx.queryArguments)
	}
	if !selected.closed || !hydrated[0].closed || !hydrated[1].closed {
		t.Fatal("member hydration left result rows open")
	}
}

func TestPostgresCuratedMutationsBindEvidenceAndShareTransaction(t *testing.T) {
	for _, op := range []collections.Operation{collections.Create, collections.Update, collections.Delete, collections.Add, collections.Remove} {
		t.Run(string(op), func(t *testing.T) {
			old := curatedStorageItem()
			next, expected, listing := old, int64(0), ""
			tx := &identityScriptedTx{fakeCommandTx: &fakeCommandTx{}}
			store, ctx, a, command := curatedStorageControl(t, tx)
			if op == collections.Create {
				tx.queryRows = []pgx.Row{scriptedValueRow([]any{old.NamespaceID}), scriptedValueRow([]any{0, 0})}
			} else {
				expected = old.Revision
				next.Revision++
				next.UpdatedAt = next.UpdatedAt.Add(time.Minute)
				tx.queryRows = []pgx.Row{scriptedValueRow(curatedStorageValues(old))}
			}
			if op == collections.Update {
				next.Name = "Updated collection"
			}
			if op == collections.Add || op == collections.Remove {
				listing = "00000000-0000-7000-8000-000000000006"
			}
			if op == collections.Add {
				ctx = identity.WithPermissionContext(ctx)
				if _, err := identity.RequirePermission(ctx, "resource.read", old.NamespaceID); err != nil {
					t.Fatal(err)
				}
				tx.queryRows = append(tx.queryRows, scriptedValueRow([]any{old.NamespaceID}), scriptedValueRow([]any{false, 0}))
			}
			if len(identity.RecordedPermissionChecks(ctx)) > 0 {
				tx.queryRows = append([]pgx.Row{scriptedValueRow([]any{int64(1)}), scriptedValueRow([]any{int64(1), []bool{true}})}, tx.queryRows...)
			}
			evidence := curatedStorageEvidence(t, a, next, op, listing)
			got, err := store.Save(ctx, a, next, expected, op, listing, evidence)
			if err != nil || got != next || command.failed != nil || tx.commits != 0 || tx.rollbacks != 0 {
				t.Fatalf("mutation=%+v, failed=%v commits=%d rollbacks=%d err=%v", got, command.failed, tx.commits, tx.rollbacks, err)
			}
			if len(tx.queryRows) != 0 {
				t.Fatal("mutation skipped a required read")
			}
			for _, args := range tx.queryRowArguments {
				if args[0] != a.TenantID {
					t.Fatalf("read lost tenant: %v", args)
				}
			}
			auditIndex := len(tx.statements) - 1
			if tx.arguments[auditIndex][1] != evidence.ID || tx.arguments[auditIndex][2] != a.TenantID {
				t.Fatalf("wrong audit identity: %v", tx.arguments[auditIndex])
			}
			if op == collections.Add || op == collections.Remove {
				if !reflect.DeepEqual(tx.arguments[0], []any{a.TenantID, next.ID, listing}) {
					t.Fatalf("member binding=%v", tx.arguments[0])
				}
			}
			switch op {
			case collections.Create:
				if !reflect.DeepEqual(tx.arguments[1], []any{a.TenantID, next.ID, next.NamespaceID, next.Name, next.Description, next.Revision, next.CreatedAt, next.UpdatedAt}) {
					t.Fatalf("create bindings=%v", tx.arguments[1])
				}
			case collections.Delete:
				if !reflect.DeepEqual(tx.arguments[1], []any{a.TenantID, next.ID}) || !reflect.DeepEqual(tx.arguments[2], []any{a.TenantID, next.ID}) {
					t.Fatalf("delete bindings=%v", tx.arguments)
				}
			case collections.Update, collections.Add, collections.Remove:
				if !reflect.DeepEqual(tx.arguments[auditIndex-1], []any{a.TenantID, next.ID, next.Name, next.Description, next.Revision, next.UpdatedAt}) {
					t.Fatalf("update bindings=%v", tx.arguments)
				}
			}
		})
	}
}

func TestPostgresCuratedMutationFailurePoisonsCommand(t *testing.T) {
	for _, tc := range []struct {
		name string
		op   collections.Operation
	}{
		{"lock", collections.Create}, {"namespace", collections.Create},
		{"capacity query", collections.Create}, {"tenant quota", collections.Create},
		{"namespace quota", collections.Create}, {"create", collections.Create}, {"audit", collections.Create},
		{"parent", collections.Update}, {"stale", collections.Update}, {"evidence", collections.Update}, {"update", collections.Update},
		{"delete members", collections.Delete}, {"delete parent", collections.Delete},
		{"remove missing", collections.Remove}, {"remove failure", collections.Remove},
		{"target", collections.Add}, {"target admission", collections.Add}, {"member capacity query", collections.Add},
		{"duplicate", collections.Add}, {"member quota", collections.Add}, {"insert member", collections.Add},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mode := tc.name
			old := curatedStorageItem()
			next := old
			op, expected, listing := tc.op, int64(0), ""
			tx := &identityScriptedTx{fakeCommandTx: &fakeCommandTx{}, queryRows: []pgx.Row{scriptedValueRow([]any{old.NamespaceID}), scriptedValueRow([]any{0, 0})}}
			store, ctx, a, command := curatedStorageControl(t, tx)
			failure := errors.New("private dependency detail")
			failedRow := scannerFunc(func(...any) error { return failure })
			code := catalog.CodeDependencyUnavailable
			if op != collections.Create {
				expected = old.Revision
				next.Revision++
				tx.queryRows = []pgx.Row{scriptedValueRow(curatedStorageValues(old))}
			}
			switch mode {
			case "lock":
				tx.execErrors = []error{failure}
			case "namespace":
				tx.queryRows[0] = failedRow
			case "capacity query":
				tx.queryRows[1] = failedRow
			case "tenant quota":
				tx.queryRows[1] = scriptedValueRow([]any{1000, 0})
				code = catalog.CodeConflict
			case "namespace quota":
				tx.queryRows[1] = scriptedValueRow([]any{0, 100})
				code = catalog.CodeConflict
			case "create":
				tx.execErrors = []error{nil, failure}
			case "audit":
				tx.execErrors = []error{nil, nil, failure}
			case "parent":
				tx.queryRows[0] = failedRow
			case "stale":
				expected++
				next.Revision++
				code = catalog.CodePreconditionFailed
			case "evidence":
				code = catalog.CodeInternal
			case "update":
				tx.execErrors = []error{failure}
			case "delete members":
				op = collections.Delete
				tx.execErrors = []error{nil, failure}
			case "delete parent":
				op = collections.Delete
				tx.execErrors = []error{nil, nil, failure}
			case "remove missing":
				op = collections.Remove
				listing = old.NamespaceID
				tx.execTags = []pgconn.CommandTag{pgconn.NewCommandTag("DELETE 0")}
				code = catalog.CodeNotFound
			case "remove failure":
				op = collections.Remove
				listing = old.NamespaceID
				tx.execErrors = []error{failure}
			default:
				op = collections.Add
				listing = old.NamespaceID
				tx.queryRows = append(tx.queryRows, scriptedValueRow([]any{old.NamespaceID}), scriptedValueRow([]any{false, 0}))
				ctx = identity.WithPermissionContext(ctx)
				if mode != "target admission" {
					if _, err := identity.RequirePermission(ctx, "resource.read", old.NamespaceID); err != nil {
						t.Fatal(err)
					}
				}
				switch mode {
				case "target":
					tx.queryRows[1] = failedRow
				case "target admission":
					code = catalog.CodeForbidden
				case "member capacity query":
					tx.queryRows[2] = failedRow
				case "duplicate":
					tx.queryRows[2] = scriptedValueRow([]any{true, 0})
					code = catalog.CodeConflict
				case "member quota":
					tx.queryRows[2] = scriptedValueRow([]any{false, 200})
					code = catalog.CodeConflict
				case "insert member":
					tx.execErrors = []error{failure}
				}
			}
			if len(identity.RecordedPermissionChecks(ctx)) > 0 {
				tx.queryRows = append([]pgx.Row{scriptedValueRow([]any{int64(1)}), scriptedValueRow([]any{int64(1), []bool{true}})}, tx.queryRows...)
			}
			evidence := curatedStorageEvidence(t, a, next, op, listing)
			if mode == "evidence" {
				evidence.TargetID = old.NamespaceID
			}
			got, err := store.Save(ctx, a, next, expected, op, listing, evidence)
			if catalog.ErrorCodeOf(err) != code || got != (collections.Collection{}) || command.failed == nil || tx.rollbacks != 1 || tx.commits != 0 {
				t.Fatalf("failure returned %+v code=%v want=%v failed=%v rollbacks=%d commits=%d err=%v", got, catalog.ErrorCodeOf(err), code, command.failed, tx.rollbacks, tx.commits, err)
			}
			if code == catalog.CodeDependencyUnavailable && !errors.Is(err, failure) {
				t.Fatalf("failure did not reach the injected dependency fault: %v", err)
			}
			if mode == "audit" && len(tx.statements) != 3 {
				t.Fatal("audit failure control did not reach evidence write")
			}
			if (mode == "stale" || mode == "evidence" || mode == "duplicate" || mode == "member quota" || mode == "target admission") && len(tx.statements) != 0 {
				t.Fatal("refused mutation wrote data")
			}
		})
	}
}
