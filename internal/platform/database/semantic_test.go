// Semantic unit tests exercise validation and read-only provisioning plans
// without PostgreSQL, containers, provider calls or shared state.
package database

import (
	"errors"
	"math"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
)

// TestSemanticGenerationValidation covers tenant, profile and snapshot identity
// independently of the lease checks performed by the PostgreSQL adapter.
func TestSemanticGenerationValidation(t *testing.T) {
	g := catalog.SemanticGeneration{ID: "00000000-0000-7000-8000-000000000901", TenantID: "00000000-0000-7000-8000-000000000801", Profile: "sha256:" + strings.Repeat("a", 64), Scope: "control", SourceGeneration: "42", Dimensions: 3}
	if err := validateSemanticGeneration(g, "worker"); err != nil {
		t.Fatal(err)
	}
	public := g
	public.Scope = "public:" + g.ID
	if err := validateSemanticGeneration(public, "worker"); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*catalog.SemanticGeneration){
		func(g *catalog.SemanticGeneration) { g.ID = "wrong" }, func(g *catalog.SemanticGeneration) { g.TenantID = "" }, func(g *catalog.SemanticGeneration) { g.Profile = "sha256:" + strings.Repeat("A", 64) }, func(g *catalog.SemanticGeneration) { g.Profile = "sha256:" + strings.Repeat("z", 64) }, func(g *catalog.SemanticGeneration) { g.Dimensions = 0 }, func(g *catalog.SemanticGeneration) { g.Dimensions = 4097 }, func(g *catalog.SemanticGeneration) { g.Scope = "private" }, func(g *catalog.SemanticGeneration) { g.Scope = "public:wrong" }, func(g *catalog.SemanticGeneration) { g.SourceGeneration = "" }, func(g *catalog.SemanticGeneration) { g.SourceGeneration = strings.Repeat("x", 257) }, func(g *catalog.SemanticGeneration) { g.SourceGeneration = "bad\x00" }, func(g *catalog.SemanticGeneration) { g.SourceGeneration = "\xff" },
	} {
		invalid := g
		mutate(&invalid)
		if err := validateSemanticGeneration(invalid, "worker"); catalog.ErrorCodeOf(err) != catalog.CodeInvalidArgument {
			t.Fatalf("accepted invalid generation %+v: %v", invalid, err)
		}
	}
	for _, owner := range []string{"", strings.Repeat("w", 129), "bad\nowner", "bad\x00owner"} {
		if err := validateSemanticGeneration(g, owner); err == nil {
			t.Fatalf("accepted owner %q", owner)
		}
	}
}

// TestSemanticMemberValidation rejects malformed provider output before any
// SQL, while allowing explicit input failures to retain lexical-only members.
func TestSemanticMemberValidation(t *testing.T) {
	m := catalog.SemanticMember{Key: "local:one", InputDigest: "sha256:" + strings.Repeat("b", 64), Vector: []float32{1, 0, 0}}
	for _, members := range [][]catalog.SemanticMember{{m}, {{Key: "large", Failure: "input_too_large"}}, {{Key: "rejected", Failure: "input_rejected"}}} {
		if err := validateSemanticMembers(members, 3); err != nil {
			t.Fatal(err)
		}
	}
	for _, members := range [][]catalog.SemanticMember{nil, make([]catalog.SemanticMember, 33), {m, m}, {{Key: "", InputDigest: m.InputDigest, Vector: m.Vector}}, {{Key: "\xff"}}, {{Key: "bad\x00"}}, {{Key: strings.Repeat("x", 1025)}}, {{Key: "x", InputDigest: "bad", Vector: m.Vector}}, {{Key: "x", InputDigest: m.InputDigest, Vector: []float32{1}}}, {{Key: "x", InputDigest: m.InputDigest, Vector: []float32{0, 0, 0}}}, {{Key: "x", InputDigest: m.InputDigest, Vector: []float32{float32(math.NaN()), 0, 0}}}, {{Key: "x", InputDigest: m.InputDigest, Vector: []float32{float32(math.Inf(1)), 0, 0}}}, {{Key: "x", Failure: "provider_down"}}, {{Key: "x", Failure: "input_rejected", InputDigest: m.InputDigest}}, {{Key: "x", Failure: "input_too_large", Vector: m.Vector}}} {
		if err := validateSemanticMembers(members, 3); catalog.ErrorCodeOf(err) != catalog.CodeInvalidArgument {
			t.Fatalf("accepted invalid members %+v: %v", members, err)
		}
	}
	t.Run("reuse", func(t *testing.T) {
		// The indexer emits digest-only reuse for retained vectors and for
		// distinct keys sharing a newly embedded input in the same batch.
		reused := catalog.SemanticMember{Key: "local:reused", InputDigest: m.InputDigest, Reuse: true}
		for _, test := range []struct {
			name    string
			members []catalog.SemanticMember
			invalid bool
		}{
			{name: "retained vector", members: []catalog.SemanticMember{reused}},
			{name: "mixed batch with shared digest", members: []catalog.SemanticMember{m, reused, {Key: "large", Failure: "input_too_large"}}},
			{name: "missing digest", members: []catalog.SemanticMember{{Key: "x", Reuse: true}}, invalid: true},
			{name: "malformed digest", members: []catalog.SemanticMember{{Key: "x", InputDigest: "bad", Reuse: true}}, invalid: true},
			{name: "unexpected vector", members: []catalog.SemanticMember{{Key: "x", InputDigest: m.InputDigest, Vector: m.Vector, Reuse: true}}, invalid: true},
			{name: "input failure", members: []catalog.SemanticMember{{Key: "x", Failure: "input_rejected", Reuse: true}}, invalid: true},
			{name: "invalid member after reuse", members: []catalog.SemanticMember{reused, {Key: "x", InputDigest: m.InputDigest}}, invalid: true},
		} {
			t.Run(test.name, func(t *testing.T) {
				err := validateSemanticMembers(test.members, 3)
				if test.invalid {
					if catalog.ErrorCodeOf(err) != catalog.CodeInvalidArgument {
						t.Fatalf("invalid reuse returned %v", err)
					}
				} else if err != nil {
					t.Fatalf("valid reuse rejected: %v", err)
				}
			})
		}
	})
}

// TestSemanticSchemaPlanning covers absent setup, exact checksums and query
// failures using the existing scripted database seam rather than a live server.
func TestSemanticSchemaPlanning(t *testing.T) {
	_, checksum, err := semanticSchemaInput()
	if err != nil {
		t.Fatal(err)
	}
	row := func(values ...any) pgx.Row { return scriptedValueRow(values) }
	failure := errors.New("query failed")
	failed := scannerFunc(func(...any) error { return failure })
	for _, tc := range []struct {
		name, errorText              string
		rows                         []pgx.Row
		applied, emptyPlan, queryErr bool
	}{
		{name: "unapplied", rows: []pgx.Row{row("extensions"), row(false)}},
		{name: "applied", rows: []pgx.Row{row("extensions"), row(true), row(1, checksum), row(true)}, applied: true},
		{name: "row security missing", rows: []pgx.Row{row("extensions"), row(true), row(1, checksum), row(false)}, errorText: "forced tenant row security"},
		{name: "row security inspection failure", rows: []pgx.Row{row("extensions"), row(true), row(1, checksum), failed}, errorText: "inspect semantic tenant isolation", queryErr: true},
		{name: "missing extension", rows: []pgx.Row{row("")}, errorText: "administrator-provisioned pgvector 0.8.7", emptyPlan: true},
		{name: "extension query failure", rows: []pgx.Row{failed}, errorText: "inspect optional vector extension", emptyPlan: true, queryErr: true},
		{name: "ledger inspection failure", rows: []pgx.Row{row("extensions"), failed}, errorText: "inspect semantic schema ledger", queryErr: true},
		{name: "ledger read failure", rows: []pgx.Row{row("extensions"), row(true), failed}, errorText: "read semantic schema ledger", queryErr: true},
		{name: "checksum drift", rows: []pgx.Row{row("extensions"), row(true), row(1, "changed")}, errorText: "checksum or version mismatch"},
		{name: "version drift", rows: []pgx.Row{row("extensions"), row(true), row(2, checksum)}, errorText: "checksum or version mismatch"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tx := &identityScriptedTx{fakeCommandTx: &fakeCommandTx{}, queryRows: tc.rows}
			plan, err := planSemanticSchema(t.Context(), tx)
			if !strings.Contains(tx.queryRowStatements[0], "e.extversion='0.8.7'") {
				t.Fatal("semantic planning did not require the selected pgvector version")
			}
			want := SemanticSchemaPlan{Version: semanticSchemaVersion, Checksum: checksum, ExtensionSchema: "extensions", Applied: tc.applied}
			if tc.emptyPlan {
				want = SemanticSchemaPlan{}
			}
			if (err != nil) != (tc.errorText != "") || err != nil && !strings.Contains(err.Error(), tc.errorText) || tc.queryErr && !errors.Is(err, failure) || plan != want {
				t.Fatalf("plan = %+v, %v", plan, err)
			}
			if len(tx.queryRowStatements) != len(tc.rows) || len(tx.queryRows) != 0 || len(tx.statements) != 0 || len(tx.queryStatements) != 0 || tx.commits != 0 || tx.rollbacks != 0 {
				t.Fatal("planning did not stop at the expected read or changed state")
			}
		})
	}
	if _, err := PlanSemanticSchema(t.Context(), nil); err == nil {
		t.Fatal("nil pool accepted")
	}
	if _, err := NewPostgresSemanticStore(t.Context(), nil); err == nil {
		t.Fatal("nil repository accepted")
	}
}

// TestSemanticPrivilegesStayClosed checks the optional function allowlist and
// quote handling, including dollar delimiters embedded in legal identifiers.
func TestSemanticPrivilegesStayClosed(t *testing.T) {
	for _, tc := range []struct {
		role, signature string
		allowed         bool
	}{
		{RuntimeRoleAPI, "array_to_vector(real[], integer, boolean)", true}, {RuntimeRoleWorker, "array_to_vector(real[], integer, boolean)", true}, {RuntimeRoleAPI, "cosine_distance(vector, vector)", true}, {RuntimeRoleWorker, "cosine_distance(vector, vector)", false}, {RuntimeRoleAPI, "vector_dims(vector)", false}, {RuntimeRoleWorker, "vector_dims(vector)", true}, {RuntimeRoleWorker, "vector_norm(vector)", true}, {RuntimeRoleWorker, "array_to_vector(double precision[], integer, boolean)", false}, {"admin", "array_to_vector(real[], integer, boolean)", false}, {RuntimeRoleAPI, "unknown()", false},
	} {
		if got := vectorFunctionAllowed(tc.role, tc.signature); got != tc.allowed {
			t.Fatalf("%s %s = %v", tc.role, tc.signature, got)
		}
	}
	statement := `GRANT SELECT (id) ON "a'$semantic_grant$"."semantic_members" TO "worker"`
	grant := optionalSemanticGrant(statement, `"a'$semantic_grant$"."semantic_members"`)
	if !strings.HasPrefix(grant, "DO E'") || !strings.Contains(grant, "a''''$semantic_grant$") || !strings.Contains(grant, "<>current_user") {
		t.Fatalf("unsafe conditional grant: %s", grant)
	}
	if got := semanticSQLLiteral(`one\two'three`); got != `E'one\\two''three'` {
		t.Fatalf("escaped literal = %s", got)
	}
}

// TestSemanticComparisonRejectsInvalidInputsBeforeSQL keeps malformed vectors,
// duplicate/unbounded keys and invalid generation identities off the database.
func TestSemanticComparisonRejectsInvalidInputsBeforeSQL(t *testing.T) {
	g := catalog.SemanticGeneration{ID: "00000000-0000-7000-8000-000000000901", TenantID: "00000000-0000-7000-8000-000000000801", Profile: "sha256:" + strings.Repeat("a", 64), Scope: "control", SourceGeneration: "0", Dimensions: 2}
	store := &PostgresSemanticStore{}
	for _, keys := range [][]string{nil, make([]string, catalog.MaxPublicationRecords+1), {""}, {"x", "x"}, {"bad\x00"}, {"\xff"}, {strings.Repeat("x", 1025)}} {
		if _, err := store.Similarities(t.Context(), g, []float32{1, 0}, keys); catalog.ErrorCodeOf(err) != catalog.CodeInvalidArgument {
			t.Fatalf("invalid keys accepted: %v", err)
		}
	}
	if _, err := store.Similarities(t.Context(), g, []float32{0, 0}, []string{"x"}); catalog.ErrorCodeOf(err) != catalog.CodeInvalidArgument {
		t.Fatalf("zero vector accepted: %v", err)
	}
	if _, err := store.Similarities(t.Context(), catalog.SemanticGeneration{}, nil, []string{"x"}); catalog.ErrorCodeOf(err) != catalog.CodeInvalidArgument {
		t.Fatalf("invalid generation accepted: %v", err)
	}
	if err := store.Complete(t.Context(), g, "worker", -1); catalog.ErrorCodeOf(err) != catalog.CodeInvalidArgument {
		t.Fatalf("negative count accepted: %v", err)
	}
	for _, profiles := range [][]string{{"bad"}, {g.Profile, g.Profile}} {
		if _, err := NewPostgresJobStore(&PostgresRepository{}, profiles...); err == nil {
			t.Fatal("invalid Job profile accepted")
		}
	}
}
