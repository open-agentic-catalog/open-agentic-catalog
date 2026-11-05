package database

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/audit"
)

func TestValidateCatalogSearchProjectionRepairInput(t *testing.T) {
	t.Parallel()
	if err := ValidateCatalogSearchProjectionRepairInput("00000000-0000-7000-8000-000000000001", MaxCatalogSearchProjectionRepairBatch, "restore derived search", "change-42"); err != nil {
		t.Fatalf("valid search projection repair input returned %v", err)
	}
	for _, input := range []struct {
		tenant string
		limit  int
	}{
		{tenant: "", limit: 1},
		{tenant: "tenant", limit: 1},
		{tenant: "00000000-0000-7000-8000-000000000001", limit: 0},
		{tenant: "00000000-0000-7000-8000-000000000001", limit: MaxCatalogSearchProjectionRepairBatch + 1},
	} {
		if err := ValidateCatalogSearchProjectionRepairInput(input.tenant, input.limit, "restore derived search", ""); err == nil {
			t.Fatalf("invalid search projection repair input was accepted: %#v", input)
		}
	}
	for _, reason := range []string{"", " ", "line\nbreak", strings.Repeat("x", 2001)} {
		if err := ValidateCatalogSearchProjectionRepairInput("00000000-0000-7000-8000-000000000001", 1, reason, ""); err == nil {
			t.Fatalf("invalid repair reason %q was accepted", reason)
		}
	}
	for _, label := range []string{"", " \t ", " change-42 ", strings.Repeat("x", audit.MaxActorBytes), "\u2003" + strings.Repeat("é", audit.MaxActorBytes/2) + "\u3000"} {
		if err := ValidateCatalogSearchProjectionRepairInput("00000000-0000-7000-8000-000000000001", 1, "restore derived search", label); err != nil {
			t.Fatalf("valid operator label %q returned %v", label, err)
		}
	}
	for name, label := range map[string]string{
		"control": "line\nbreak", "unicode control": "operator\u0085label", "invalid UTF-8": string([]byte{0xff}),
		"long ASCII": strings.Repeat("x", audit.MaxActorBytes+1), "long UTF-8": strings.Repeat("é", audit.MaxActorBytes/2) + "x",
	} {
		t.Run(name, func(t *testing.T) {
			if err := ValidateCatalogSearchProjectionRepairInput("00000000-0000-7000-8000-000000000001", 1, "restore derived search", label); err == nil || !strings.Contains(err.Error(), "operator label") {
				t.Fatalf("invalid operator label returned %v", err)
			}
		})
	}
}

const (
	searchRepairTenant = "00000000-0000-7000-8000-000000000001"
	searchRepairFirst  = "00000000-0000-7000-8000-000000000011"
	searchRepairMiddle = "00000000-0000-7000-8000-000000000012"
	searchRepairLast   = "00000000-0000-7000-8000-000000000013"
	searchRepairAudit  = "00000000-0000-7000-8000-000000000020"
	searchRepairReason = "Rebuild retained search projections"
	searchRepairLabel  = "change-42"
)

type searchRepairFixture struct {
	tx     *identityScriptedTx
	rows   *identityScriptedRows
	before int64
	after  int64
	at     time.Time
}

func newSearchRepairFixture(ids []string, before int64, hasMore bool) searchRepairFixture {
	rows := &identityScriptedRows{}
	for _, id := range ids {
		rows.values = append(rows.values, []any{id})
	}
	// The fixture supplies the documented generation delta; only PostgreSQL
	// integration can establish that projection triggers actually apply it.
	after := before + int64(len(ids))
	at := time.Date(2026, time.September, 21, 12, 0, 0, 123000, time.UTC)
	tx := &identityScriptedTx{fakeCommandTx: &fakeCommandTx{},
		queryResults: []identityQueryResult{{rows: rows}},
		queryRows: []pgx.Row{
			scriptedValueRow([]any{before}),
			scriptedValueRow([]any{hasMore}),
			scriptedValueRow([]any{after}),
		},
	}
	if len(ids) > 0 {
		tx.queryRows = append(tx.queryRows,
			scriptedValueRow([]any{at}),
			scriptedValueRow([]any{searchRepairAudit, "maintenance_login", "catalog_owner"}),
		)
	}
	return searchRepairFixture{tx: tx, rows: rows, before: before, after: after, at: at}
}

func TestRepairCatalogSearchProjectionsTxProgressAndAudit(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name        string
		ids         []string
		first, last string
		before      int64
		hasMore     bool
	}{
		{name: "empty new tenant"},
		{name: "empty completed tenant", before: 40},
		{name: "empty with remaining rows", before: 40, hasMore: true},
		{name: "single final row", ids: []string{searchRepairMiddle}, first: searchRepairMiddle, last: searchRepairMiddle, before: 40},
		{name: "unordered full final batch", ids: []string{searchRepairLast, searchRepairFirst, searchRepairMiddle}, first: searchRepairFirst, last: searchRepairLast, before: 40},
		{name: "partial with remaining rows", ids: []string{searchRepairLast, searchRepairFirst}, first: searchRepairFirst, last: searchRepairLast, before: 40, hasMore: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			f := newSearchRepairFixture(tt.ids, tt.before, tt.hasMore)
			got, err := repairCatalogSearchProjectionsTx(t.Context(), f.tx, searchRepairTenant, 3, searchRepairReason, searchRepairLabel)
			want := CatalogSearchProjectionRepairResult{TenantID: searchRepairTenant, Processed: int64(len(tt.ids)), HasMore: tt.hasMore, Complete: !tt.hasMore, BeforeGeneration: f.before, AfterGeneration: f.after}
			if len(tt.ids) > 0 {
				want.AuditEventID = searchRepairAudit
			}
			if err != nil || got != want {
				t.Fatalf("repair result = %+v, %v; want %+v", got, err, want)
			}
			wantReads := [][]any{{searchRepairTenant}, {searchRepairTenant}, {searchRepairTenant}}
			if len(tt.ids) > 0 {
				wantReads = append(wantReads, nil, nil) // Database time and audit attribution.
			}
			if !reflect.DeepEqual(f.tx.queryArguments, [][]any{{searchRepairTenant, 3}}) || !reflect.DeepEqual(f.tx.queryRowArguments, wantReads) || len(f.tx.queryRows) != 0 || len(f.tx.queryResults) != 0 {
				t.Fatal("repair lost tenant/batch bindings, skipped a read or made an extra query")
			}
			if !f.rows.closed || f.tx.commits != 0 || f.tx.rollbacks != 0 {
				t.Fatal("repair left rows open or took transaction ownership")
			}
			if len(tt.ids) == 0 {
				if len(f.tx.statements) != 0 {
					t.Fatal("empty repair wrote audit or other evidence")
				}
				return
			}
			if len(f.tx.statements) != 1 || !strings.Contains(f.tx.statements[0], "INSERT INTO audit_events(") {
				t.Fatalf("nonempty repair must write exactly one audit record and no outbox: %#v", f.tx.statements)
			}
			args := f.tx.arguments[0]
			wantFields := []any{
				new(audit.RecordFormatV1), searchRepairAudit, searchRepairTenant, "maintenance_login", audit.ActionSearchProjectionRebuilt,
				"catalog_search_projection", searchRepairTenant, searchRepairReason, "",
				audit.DigestBytes([]byte(fmt.Sprintf("catalog-search-generation:%d", f.before))),
				audit.DigestBytes([]byte(fmt.Sprintf("catalog-search-generation:%d", f.after))),
			}
			if len(args) != 13 || !reflect.DeepEqual(args[:11], wantFields) || !reflect.DeepEqual(args[12], f.at) {
				t.Fatalf("repair audit identity, attribution, digests or time = %#v", args)
			}
			raw, ok := args[11].([]byte)
			if !ok {
				t.Fatalf("repair audit details have type %T; want encoded JSON", args[11])
			}
			var details audit.Details
			if err := json.Unmarshal(raw, &details); err != nil {
				t.Fatal(err)
			}
			wantDetails := audit.Details{
				PrincipalType: audit.PrincipalDatabaseLogin, CurrentUser: "catalog_owner", OperatorLabel: searchRepairLabel,
				Facts: map[string]any{
					"selectedFirstId": tt.first, "selectedLastId": tt.last, "processed": float64(len(tt.ids)),
					"hasMore": tt.hasMore, "beforeGeneration": float64(f.before), "afterGeneration": float64(f.after),
				},
			}
			if !reflect.DeepEqual(details, wantDetails) {
				t.Fatalf("repair audit details = %#v; want %#v", details, wantDetails)
			}
		})
	}
}

func TestRepairCatalogSearchProjectionsTxStopsOnFailure(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"before generation", "query", "scan", "iteration", "remaining probe", "after generation", "audit clock", "audit attribution", "audit insert"} {
		t.Run(mode, func(t *testing.T) {
			f := newSearchRepairFixture([]string{searchRepairLast, searchRepairFirst}, 40, true)
			failure := errors.New("injected search repair failure")
			failedRow := scannerFunc(func(...any) error { return failure })
			wantReads, wantQueries, wantWrites := 1, 1, 0
			switch mode {
			case "before generation":
				f.tx.queryRows[0], wantQueries = failedRow, 0
			case "query":
				f.tx.queryResults[0].err = failure
			case "scan":
				f.tx.queryResults[0].rows = reencryptScanFailureRows{identityScriptedRows: f.rows, failure: failure}
			case "iteration":
				f.rows.err = failure
			case "remaining probe":
				f.tx.queryRows[1], wantReads = failedRow, 2
			case "after generation":
				f.tx.queryRows[2], wantReads = failedRow, 3
			case "audit clock":
				f.tx.queryRows[3], wantReads = failedRow, 4
			case "audit attribution":
				f.tx.queryRows[4], wantReads = failedRow, 5
			case "audit insert":
				f.tx.execErrors = []error{failure}
				wantReads, wantWrites = 5, 1
			}
			got, err := repairCatalogSearchProjectionsTx(t.Context(), f.tx, searchRepairTenant, 3, searchRepairReason, searchRepairLabel)
			if !errors.Is(err, failure) || got != (CatalogSearchProjectionRepairResult{}) {
				t.Fatalf("failed repair = %+v, %v; want no progress and the injected error", got, err)
			}
			if len(f.tx.queryRowStatements) != wantReads || len(f.tx.queryStatements) != wantQueries || len(f.tx.statements) != wantWrites {
				t.Fatalf("failure skipped or continued work: reads=%d queries=%d writes=%d; want %d, %d, %d", len(f.tx.queryRowStatements), len(f.tx.queryStatements), len(f.tx.statements), wantReads, wantQueries, wantWrites)
			}
			wantClosed := mode != "before generation" && mode != "query"
			if f.rows.closed != wantClosed || f.tx.commits != 0 || f.tx.rollbacks != 0 {
				t.Fatal("failed repair did not close opened rows or took transaction ownership")
			}
		})
	}
}

func TestRepairCatalogSearchProjectionsRejectsInvalidPreflight(t *testing.T) {
	t.Parallel()
	const tenant = "00000000-0000-7000-8000-000000000001"
	// An inert pool makes an unintended query fail instead of masking missing
	// validation behind the missing-pool guard.
	pool := &pgxpool.Pool{}
	for _, tt := range []struct {
		name, tenant, reason, label, want string
		pool                              *pgxpool.Pool
		limit                             int
	}{
		{name: "missing pool", tenant: tenant, limit: 1, reason: "restore derived search", want: "pool is required"},
		{name: "tenant", pool: pool, tenant: "invalid", limit: 1, reason: "restore derived search", want: "UUID tenant"},
		{name: "limit", pool: pool, tenant: tenant, limit: MaxCatalogSearchProjectionRepairBatch + 1, reason: "restore derived search", want: "limit between"},
		{name: "reason", pool: pool, tenant: tenant, limit: 1, reason: " ", want: "reason is invalid"},
		{name: "operator", pool: pool, tenant: tenant, limit: 1, reason: "restore derived search", label: "line\nbreak", want: "operator label"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, err := RepairCatalogSearchProjections(t.Context(), tt.pool, tt.tenant, tt.limit, tt.reason, tt.label)
			if err == nil || !strings.Contains(err.Error(), tt.want) || got != (CatalogSearchProjectionRepairResult{}) {
				t.Fatalf("preflight = %#v, %v; want empty result and %q", got, err, tt.want)
			}
		})
	}
}
