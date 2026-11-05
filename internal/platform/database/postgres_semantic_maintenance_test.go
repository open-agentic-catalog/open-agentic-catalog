package database

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
)

func TestListDueSemanticGenerationsMapsCandidatesAndClosesRows(t *testing.T) {
	t.Parallel()
	const (
		tenantA = "00000000-0000-7000-8000-000000000011"
		tenantB = "00000000-0000-7000-8000-000000000022"
		profile = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	)
	for _, mode := range []string{"empty", "multiple", "empty exclusions", "query failure", "first scan failure", "later scan failure", "empty iteration failure", "partial iteration failure"} {
		t.Run(mode, func(t *testing.T) {
			rows := &searchHydrationRows{identityScriptedRows: identityScriptedRows{values: [][]any{
				{tenantA, "control", "7"},
				{tenantB, "public:" + semanticSnapshotTestID, "19"},
			}}}
			want := []catalog.SemanticGeneration{
				{TenantID: tenantA, Profile: profile, Dimensions: 3, Scope: "control", SourceGeneration: "7"},
				{TenantID: tenantB, Profile: profile, Dimensions: 3, Scope: "public:" + semanticSnapshotTestID, SourceGeneration: "19"},
			}
			excluded := []string{paginationTestTenantID}
			wantExcluded := []string{paginationTestTenantID}
			failure := errors.New("semantic candidate read failed")
			query := &identityScriptedTx{fakeCommandTx: &fakeCommandTx{}, queryResults: []identityQueryResult{{rows: rows}}}
			wantOperation := ""
			wantIndex, failScanAt := 2, 0
			wantClosed := true
			switch mode {
			case "empty":
				rows.values, excluded, wantIndex = nil, nil, 0
				want, wantExcluded = []catalog.SemanticGeneration{}, []string{}
			case "empty exclusions":
				excluded, wantExcluded = []string{}, []string{}
			case "query failure":
				query.queryResults[0] = identityQueryResult{err: failure}
				want, wantOperation, wantIndex, wantClosed = nil, "select semantic maintenance", 0, false
			case "first scan failure", "later scan failure":
				want, wantOperation = nil, "read semantic maintenance"
				failScanAt, wantIndex = 1, 1
				if mode == "later scan failure" {
					failScanAt, wantIndex = 2, 2
					// The row after a failed scan must never be read.
					rows.values = append(rows.values, []any{tenantB, "control", "23"})
				}
			case "empty iteration failure", "partial iteration failure":
				rows.err, wantOperation = failure, "iterate semantic maintenance"
				if mode == "empty iteration failure" {
					rows.values, want, wantIndex = nil, []catalog.SemanticGeneration{}, 0
				}
			}
			rows.scan = func(destinations ...any) error {
				if rows.index == failScanAt {
					tenant, ok := rows.values[rows.index-1][0].(string)
					if !ok {
						t.Fatal("scripted candidate tenant is not a string")
					}
					setScanDestination(t, destinations, 0, tenant)
					return failure
				}
				return rows.identityScriptedRows.Scan(destinations...)
			}
			got, err := listDueSemanticGenerations(t.Context(), query, profile, 3, excluded, 7)
			if wantOperation == "" {
				if err != nil {
					t.Fatalf("candidate read failed: %v", err)
				}
			} else {
				var domain *catalog.DomainError
				if !errors.Is(err, failure) || !errors.As(err, &domain) || domain.Code != catalog.CodeDependencyUnavailable || domain.Message != wantOperation {
					t.Fatalf("candidate error = %v; want original cause and %q dependency error", err, wantOperation)
				}
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("candidates = %#v; want %#v", got, want)
			}
			if rows.closed != wantClosed || rows.index != wantIndex {
				t.Fatalf("row cleanup/progress = closed %t, index %d; want %t, %d", rows.closed, rows.index, wantClosed, wantIndex)
			}
			if len(query.queryStatements) != 1 || len(query.queryResults) != 0 || !reflect.DeepEqual(query.queryArguments, [][]any{{profile, 3, wantExcluded, 7}}) {
				t.Fatalf("candidate query count = %d; arguments = %#v; unread results = %d", len(query.queryStatements), query.queryArguments, len(query.queryResults))
			}
			if len(query.queryRowStatements) != 0 || len(query.statements) != 0 || query.commits != 0 || query.rollbacks != 0 {
				t.Fatal("candidate read performed unrelated work or took transaction ownership")
			}
		})
	}
}

// A zero-row admission is an ordinary race/capacity result. Script that result
// without pretending to execute the underlying eligibility SQL.
type semanticAdmissionTx struct {
	*identityScriptedTx
	inserted bool
}

func (tx *semanticAdmissionTx) Exec(ctx context.Context, query string, args ...any) (pgconn.CommandTag, error) {
	tag, err := tx.identityScriptedTx.Exec(ctx, query, args...)
	if err == nil && strings.HasPrefix(query, "INSERT INTO jobs") && !tx.inserted {
		return pgconn.NewCommandTag("INSERT 0 0"), nil
	}
	return tag, err
}

func TestSemanticAdmissionRechecksSourceAndBindsJob(t *testing.T) {
	for _, mode := range []string{"admitted", "tenant override", "public", "other tenant excluded", "not inserted", "excluded", "case folded exclusion", "lock failure", "source failure", "source changed", "public missing", "public source failure", "insert failure"} {
		t.Run(mode, func(t *testing.T) {
			f := newSemanticRepositoryFixture(t, false)
			failure := errors.New("admission failed")
			tx := &semanticAdmissionTx{identityScriptedTx: f.tx, inserted: mode != "not inserted"}
			f.command.tx = tx
			f.tx.queryRows = []pgx.Row{scriptedValueRow([]any{"7"})}
			excluded := []string(nil)
			writes, reads := 2, 1
			switch mode {
			case "public", "public missing", "public source failure":
				f.g.Scope = "public:" + semanticSnapshotTestID
			case "other tenant excluded":
				excluded = []string{"00000000-0000-7000-8000-000000000002"}
			case "excluded":
				excluded = []string{f.g.TenantID}
				writes, reads = 0, 0
			case "case folded exclusion":
				f.g.TenantID = "abcdefab-cdef-7abc-8def-abcdefabcdef"
				excluded = []string{strings.ToUpper(f.g.TenantID)}
				writes, reads = 0, 0
			case "lock failure":
				f.tx.execErrors = []error{failure}
				writes, reads = 1, 0
			case "source failure":
				f.tx.queryRows[0] = semanticFaultRow(failure)
				writes = 1
			case "source changed":
				f.tx.queryRows[0] = scriptedValueRow([]any{"8"})
				writes = 1
			case "insert failure":
				f.tx.execErrors = []error{nil, failure}
			}
			if mode == "public missing" {
				f.tx.queryRows[0] = semanticFaultRow(pgx.ErrNoRows)
				writes = 1
			}
			if mode == "public source failure" {
				f.tx.queryRows[0] = semanticFaultRow(failure)
				writes = 1
			}
			// Settings is locked before maintenance and read in a separate statement.
			if mode != "excluded" && mode != "case folded exclusion" {
				if mode != "lock failure" {
					row := semanticFaultRow(pgx.ErrNoRows)
					if mode == "tenant override" {
						attempts := 3
						row = scriptedValueRow([]any{int64(1), (*bool)(nil), (*int64)(nil), &attempts, (*time.Time)(nil)})
					}
					f.tx.queryRows = append([]pgx.Row{row}, f.tx.queryRows...)
					writes++
					reads++
				}
				if mode == "insert failure" {
					f.tx.execErrors = []error{nil, nil, failure}
				}
			}
			got, err := f.store.Admit(f.context(t), f.g, excluded, 7)
			if strings.Contains(mode, "failure") {
				if got || !errors.Is(err, failure) {
					t.Fatalf("failed admission=%t %v", got, err)
				}
			} else if err != nil || got != (mode == "admitted" || mode == "tenant override" || mode == "public" || mode == "other tenant excluded") {
				t.Fatalf("admission=%t %v", got, err)
			}
			if mode == "public source failure" && catalog.ErrorCodeOf(err) != catalog.CodeDependencyUnavailable {
				t.Fatalf("public lookup failure classification=%v", err)
			}
			f.checkWrite(t, err)
			if len(f.tx.statements) != writes || len(f.tx.queryRowStatements) != reads {
				t.Fatalf("admission reads/writes=%d/%d", len(f.tx.queryRowStatements), len(f.tx.statements))
			}
			if writes == 0 {
				return
			}
			if !reflect.DeepEqual(f.tx.arguments[0], []any{catalogStateLockKey("tenant-settings", f.g.TenantID)}) {
				t.Fatal("settings lock binding changed")
			}
			if writes > 1 && !reflect.DeepEqual(f.tx.arguments[1], []any{semanticAdmissionLock}) {
				t.Fatal("admission lock binding changed")
			}
			if reads > 1 {
				want := []any{f.g.TenantID}
				if f.g.Scope != "control" {
					want = append(want, semanticSnapshotTestID)
				}
				if !reflect.DeepEqual(f.tx.queryRowArguments, [][]any{{f.g.TenantID}, want}) {
					t.Fatalf("admission source=%v", f.tx.queryRowArguments)
				}
			}
			if writes == 3 {
				args := f.tx.arguments[2]
				raw, ok := args[2].([]byte)
				if !ok {
					t.Fatalf("payload=%T", args[2])
				}
				var payload catalog.SemanticReindexInput
				if err := json.Unmarshal(raw, &payload); err != nil {
					t.Fatal(err)
				}
				if payload != (catalog.SemanticReindexInput{Profile: f.g.Profile, Scope: f.g.Scope, SourceGeneration: f.g.SourceGeneration}) {
					t.Fatalf("payload=%+v", payload)
				}
				wantAttempts := 7
				if mode == "tenant override" {
					wantAttempts = 3
				}
				if !reflect.DeepEqual(args, []any{f.g.ID, f.g.TenantID, raw, f.g.Profile + ":" + f.g.Scope, f.g.Profile, f.g.Scope, "7", 3, append([]string{}, excluded...), wantAttempts}) {
					t.Fatalf("job binding=%v", args)
				}
			}
		})
	}
}

func TestSemanticRepositoryPreflightRejectsBeforeSQL(t *testing.T) {
	f := newSemanticRepositoryFixture(t, false)
	g := f.g
	store := &PostgresSemanticStore{} // Any database access is an invalid fixture call.
	tooMany := make([]string, 257)
	for i := range tooMany {
		tooMany[i] = g.TenantID
	}
	invalidGeneration := g
	invalidGeneration.ID = "invalid"
	for name, call := range map[string]func() error{
		"begin generation": func() error { return store.Begin(t.Context(), invalidGeneration, "worker") },
		"save generation": func() error {
			return store.SaveBatch(t.Context(), invalidGeneration, "worker", []catalog.SemanticMember{{Key: "local:one", InputDigest: g.Profile, Vector: []float32{1, 0, 0}}})
		},
		"save members":        func() error { return store.SaveBatch(t.Context(), g, "worker", nil) },
		"complete generation": func() error { return store.Complete(t.Context(), invalidGeneration, "worker", 0) },
		"inputs generation":   func() error { _, _, e := store.Inputs(t.Context(), invalidGeneration, "", 1); return e },
		"reuse generation":    func() error { _, e := store.KnownInputs(t.Context(), invalidGeneration, nil); return e },
		"reuse size": func() error {
			digests := make([]string, 33)
			for i := range digests {
				digests[i] = g.Profile
			}
			_, e := store.KnownInputs(t.Context(), g, digests)
			return e
		},
		"reuse digest":             func() error { _, e := store.KnownInputs(t.Context(), g, []string{"bad"}); return e },
		"current tenant":           func() error { _, e := store.Current(t.Context(), "bad", "control"); return e },
		"current scope":            func() error { _, e := store.Current(t.Context(), g.TenantID, "public:bad"); return e },
		"active generation":        func() error { _, _, e := store.Active(t.Context(), g.TenantID, "bad", "control", 3); return e },
		"admit zero attempts":      func() error { _, e := store.Admit(t.Context(), g, nil, 0); return e },
		"admit excessive attempts": func() error { _, e := store.Admit(t.Context(), g, nil, 21); return e },
		"admit generation":         func() error { _, e := store.Admit(t.Context(), invalidGeneration, nil, 5); return e },
		"admit size":               func() error { _, e := store.Admit(t.Context(), g, tooMany, 5); return e },
		"admit exclusion":          func() error { _, e := store.Admit(t.Context(), g, []string{"bad"}, 5); return e },
	} {
		t.Run(name, func(t *testing.T) {
			if err := call(); catalog.ErrorCodeOf(err) != catalog.CodeInvalidArgument {
				t.Fatalf("preflight=%v", err)
			}
		})
	}
	for _, test := range []struct {
		after string
		limit int
	}{{"", 0}, {"", 33}, {strings.Repeat("x", 1025), 1}, {"\xff", 1}, {"bad\x00", 1}} {
		if _, _, err := store.Inputs(t.Context(), g, test.after, test.limit); catalog.ErrorCodeOf(err) != catalog.CodeInvalidArgument {
			t.Fatalf("input page bound=%v", err)
		}
	}
	for _, test := range []struct {
		tenant, profile string
		dimensions      int
	}{{"bad", g.Profile, 3}, {g.TenantID, "bad", 3}, {g.TenantID, g.Profile, 0}, {g.TenantID, g.Profile, 4097}} {
		if _, err := store.Status(t.Context(), test.tenant, test.profile, test.dimensions); catalog.ErrorCodeOf(err) != catalog.CodeInvalidArgument {
			t.Fatalf("status identity=%v", err)
		}
	}
	for _, test := range []struct {
		name, profile     string
		dimensions, limit int
		excluded          []string
	}{
		{"profile", "bad", 3, 1, nil}, {"dimensions low", g.Profile, 0, 1, nil}, {"dimensions high", g.Profile, 4097, 1, nil},
		{"limit low", g.Profile, 3, 0, nil}, {"limit high", g.Profile, 3, 33, nil}, {"exclusion count", g.Profile, 3, 1, tooMany}, {"invalid exclusion", g.Profile, 3, 1, []string{g.TenantID, "bad"}},
	} {
		t.Run("due/"+test.name, func(t *testing.T) {
			if _, err := store.Due(t.Context(), test.profile, test.dimensions, test.excluded, test.limit); catalog.ErrorCodeOf(err) != catalog.CodeInvalidArgument {
				t.Fatalf("due preflight=%v", err)
			}
		})
	}
	for _, test := range []struct {
		name, profile string
		limit         int
		excluded      []string
	}{
		{"profile", "bad", 1, nil}, {"limit low", g.Profile, 0, nil}, {"limit high", g.Profile, 1001, nil}, {"exclusion count", g.Profile, 1, tooMany}, {"invalid exclusion", g.Profile, 1, []string{g.TenantID, "bad"}},
	} {
		t.Run("cleanup/"+test.name, func(t *testing.T) {
			if _, err := store.Cleanup(t.Context(), test.profile, test.excluded, test.limit); catalog.ErrorCodeOf(err) != catalog.CodeInvalidArgument {
				t.Fatalf("cleanup preflight=%v", err)
			}
		})
	}
}
