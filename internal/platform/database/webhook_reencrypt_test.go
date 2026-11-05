package database

import (
	"bytes"
	"context"
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
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/webhook"
)

func TestValidateWebhookReencryptInput(t *testing.T) {
	const tenant = "00000000-0000-7000-8000-000000000001"
	for _, input := range []struct {
		tenant, after, reason string
		limit                 int
	}{{"", "", "Replace", 1}, {tenant, "invalid", "Replace", 1}, {tenant, "", "", 1}, {tenant, "", "Replace", 0}, {tenant, "", "Replace", 101}} {
		if err := ValidateWebhookReencryptInput(input.tenant, input.after, input.reason, input.limit); err == nil {
			t.Fatal("invalid maintenance request accepted")
		}
	}
	if err := ValidateWebhookReencryptInput(tenant, tenant, "Replace retained encryption key", 100); err != nil {
		t.Fatal(err)
	}
}

const (
	reencryptTenant = "00000000-0000-7000-8000-000000000001"
	reencryptAfter  = "00000000-0000-7000-8000-000000000099"
	reencryptAudit  = "00000000-0000-7000-8000-000000000900"
	reencryptReason = "Replace deployment encryption key"
)

type reencryptBatchFixture struct {
	tx                   *identityScriptedTx
	rows                 *identityScriptedRows
	oldKey, newKey       []byte
	ids                  []string
	plaintexts, retained [][]byte
	at                   time.Time
}

// Each letter selects one returned row: o uses the old key, n the replacement
// key, and x corrupt ciphertext. No connection or database state is emulated.
func newReencryptBatchFixture(t *testing.T, states string) *reencryptBatchFixture {
	t.Helper()
	f := &reencryptBatchFixture{
		oldKey: bytes.Repeat([]byte{3}, 32), newKey: bytes.Repeat([]byte{8}, 32),
		rows: &identityScriptedRows{}, at: time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC),
	}
	for index, state := range states {
		key := f.oldKey
		if state == 'n' {
			key = f.newKey
		}
		cipher, err := webhook.NewAESGCM(key)
		if err != nil {
			t.Fatal(err)
		}
		plain := []byte(fmt.Sprintf("whsec_%043d", index))
		retained, err := cipher.Encrypt(plain)
		if err != nil {
			t.Fatal(err)
		}
		if state == 'x' {
			retained = []byte("corrupt retained ciphertext")
		}
		endpointID := fmt.Sprintf("00000000-0000-7000-8000-%012d", 100+index)
		f.ids = append(f.ids, endpointID)
		f.plaintexts = append(f.plaintexts, plain)
		f.retained = append(f.retained, bytes.Clone(retained))
		f.rows.values = append(f.rows.values, []any{endpointID, retained})
	}
	f.tx = &identityScriptedTx{
		fakeCommandTx: &fakeCommandTx{}, queryResults: []identityQueryResult{{rows: f.rows}},
		queryRows: []pgx.Row{scriptedValueRow([]any{f.at}), scriptedValueRow([]any{reencryptAudit, "catalog_owner", "catalog_owner"})},
	}
	return f
}

func TestReencryptWebhookSecretBatchProgressAndEvidence(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name, states      string
		verified, changed int
		hasMore           bool
	}{
		{name: "empty"},
		{name: "partial", states: "o", verified: 1, changed: 1},
		{name: "exact limit", states: "oo", verified: 2, changed: 2},
		{name: "already replaced", states: "nn", verified: 2},
		{name: "mixed keys", states: "no", verified: 2, changed: 1},
		{name: "lookahead is not processed", states: "onx", verified: 2, changed: 1, hasMore: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			f := newReencryptBatchFixture(t, tt.states)
			oldBefore, newBefore := bytes.Clone(f.oldKey), bytes.Clone(f.newKey)
			got, err := reencryptWebhookSecretBatch(t.Context(), f.tx, reencryptTenant, reencryptAfter, reencryptReason, 2, f.oldKey, f.newKey)
			want := WebhookReencryptResult{TenantID: reencryptTenant, Verified: tt.verified, Changed: tt.changed, HasMore: tt.hasMore}
			if tt.verified > 0 {
				want.AfterID, want.AuditEventID = f.ids[tt.verified-1], reencryptAudit
			}
			if err != nil || got != want {
				t.Fatalf("batch = %+v, %v; want %+v", got, err, want)
			}
			if !reflect.DeepEqual(f.tx.queryArguments, [][]any{{reencryptTenant, reencryptAfter, 3}}) || !f.rows.closed {
				t.Fatal("batch lost tenant/cursor/lookahead binding or left rows open")
			}
			if !bytes.Equal(f.oldKey, oldBefore) || !bytes.Equal(f.newKey, newBefore) {
				t.Fatal("batch modified caller-owned keys")
			}
			for index, row := range f.rows.values {
				if !reflect.DeepEqual(row[1], f.retained[index]) {
					t.Fatal("batch modified retained input ciphertext")
				}
			}
			if f.tx.commits != 0 || f.tx.rollbacks != 0 {
				t.Fatal("batch took ownership of its caller's transaction")
			}
			if tt.verified == 0 {
				if len(f.tx.statements) != 0 || len(f.tx.queryRowStatements) != 0 {
					t.Fatal("empty batch wrote evidence or requested audit identity/time")
				}
				return
			}
			if len(f.tx.statements) != tt.changed+1 || len(f.tx.queryRowStatements) != 2 || len(f.tx.queryRows) != 0 {
				t.Fatal("batch did not write only changed ciphertext and one audit record")
			}
			newCipher, err := webhook.NewAESGCM(f.newKey)
			if err != nil {
				t.Fatal(err)
			}
			update := 0
			for index := range tt.verified {
				if tt.states[index] != 'o' {
					continue
				}
				args := f.tx.arguments[update]
				if !strings.HasPrefix(f.tx.statements[update], "UPDATE webhook_endpoints SET encrypted_secret=") || len(args) != 3 || !reflect.DeepEqual(args[:2], []any{reencryptTenant, f.ids[index]}) {
					t.Fatal("ciphertext update lost its exact tenant/endpoint binding")
				}
				replacement, ok := args[2].([]byte)
				if !ok {
					t.Fatal("replacement was not ciphertext")
				}
				plain, err := newCipher.Decrypt(replacement)
				if err != nil || !bytes.Equal(plain, f.plaintexts[index]) {
					t.Fatal("update stored the wrong endpoint's signing material")
				}
				update++
			}
			args := f.tx.arguments[tt.changed]
			wantFields := []any{new(audit.RecordFormatV1), reencryptAudit, reencryptTenant, "catalog_owner", audit.ActionWebhookSecretsReencrypted, "webhook_secret_batch", f.ids[0], reencryptReason, "", "", ""}
			if !strings.Contains(f.tx.statements[tt.changed], "INSERT INTO audit_events(") || len(args) != 13 || !reflect.DeepEqual(args[:11], wantFields) || !reflect.DeepEqual(args[12], f.at) {
				t.Fatal("audit record lost batch identity, attribution, reason or time")
			}
			raw, ok := args[11].([]byte)
			if !ok {
				t.Fatal("audit details were not encoded")
			}
			var details audit.Details
			if err := json.Unmarshal(raw, &details); err != nil {
				t.Fatal(err)
			}
			wantDetails := audit.Details{PrincipalType: audit.PrincipalDatabaseLogin, CurrentUser: "catalog_owner", Facts: map[string]any{
				"changed": float64(tt.changed), "verified": float64(tt.verified), "firstId": f.ids[0], "lastId": want.AfterID,
			}}
			if !reflect.DeepEqual(details, wantDetails) {
				t.Fatal("audit details do not describe the processed batch")
			}
		})
	}
}

type reencryptScanFailureRows struct {
	*identityScriptedRows
	failure error
}

func (rows reencryptScanFailureRows) Scan(...any) error { return rows.failure }

func TestReencryptWebhookSecretBatchStopsOnFailure(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"query", "scan", "iteration", "cancelled iteration", "corrupt first", "corrupt later", "first update", "later update", "audit clock", "audit attribution", "audit insert"} {
		t.Run(mode, func(t *testing.T) {
			f := newReencryptBatchFixture(t, "ooo")
			failure := errors.New("injected maintenance failure")
			want := WebhookReencryptResult{}
			wantWrites, wantAuditReads := 0, 0
			switch mode {
			case "query":
				f.tx.queryResults[0].err = failure
				want.TenantID = reencryptTenant
			case "scan":
				f.tx.queryResults[0].rows = reencryptScanFailureRows{identityScriptedRows: f.rows, failure: failure}
				want.TenantID = reencryptTenant
			case "iteration", "cancelled iteration":
				if mode == "cancelled iteration" {
					failure = context.Canceled
				}
				f.rows.err = failure
				want.TenantID = reencryptTenant
			case "corrupt first":
				f.rows.values[0][1] = []byte("corrupt ciphertext")
			case "corrupt later":
				f.rows.values[1][1] = []byte("corrupt ciphertext")
				wantWrites = 1
			case "first update":
				f.tx.execErrors = []error{failure}
				wantWrites = 1
			case "later update":
				f.tx.execErrors = []error{nil, failure}
				wantWrites = 2
			case "audit clock":
				f.tx.queryRows[0] = scannerFunc(func(...any) error { return failure })
				wantWrites, wantAuditReads = 2, 1
			case "audit attribution":
				f.tx.queryRows[1] = scannerFunc(func(...any) error { return failure })
				wantWrites, wantAuditReads = 2, 2
			case "audit insert":
				f.tx.execErrors = []error{nil, nil, failure}
				wantWrites, wantAuditReads = 3, 2
			}
			got, err := reencryptWebhookSecretBatch(t.Context(), f.tx, reencryptTenant, reencryptAfter, reencryptReason, 2, f.oldKey, f.newKey)
			if err == nil || got != want {
				t.Fatalf("failed batch = %+v, %v; want %+v and an error", got, err, want)
			}
			if !strings.HasPrefix(mode, "corrupt") && !errors.Is(err, failure) {
				t.Fatalf("batch lost the injected cause: %v", err)
			}
			if len(f.tx.statements) != wantWrites || len(f.tx.queryRowStatements) != wantAuditReads || len(f.tx.queryStatements) != 1 {
				t.Fatal("failed batch continued work or skipped the intended failure")
			}
			if f.rows.closed != (mode != "query") || f.tx.commits != 0 || f.tx.rollbacks != 0 {
				t.Fatal("batch failed to close rows or took transaction ownership")
			}
		})
	}
}

func TestReencryptWebhookSecretsRejectsInvalidPreflight(t *testing.T) {
	t.Parallel()
	const tenant = "00000000-0000-7000-8000-000000000001"
	// No connection is configured. Rejected input must never reach BeginTx.
	pool := &pgxpool.Pool{}
	oldKey, newKey := bytes.Repeat([]byte{1}, 32), bytes.Repeat([]byte{2}, 32)
	for _, tt := range []struct {
		name, tenant, after, reason, want string
		pool                              *pgxpool.Pool
		limit                             int
		oldKey, newKey                    []byte
	}{
		{name: "missing pool", tenant: tenant, limit: 1, reason: "Replace key", oldKey: oldKey, newKey: newKey, want: "pool is required"},
		{name: "tenant", pool: pool, tenant: "invalid", limit: 1, reason: "Replace key", oldKey: oldKey, newKey: newKey, want: "UUID tenant"},
		{name: "cursor", pool: pool, tenant: tenant, after: "invalid", limit: 1, reason: "Replace key", oldKey: oldKey, newKey: newKey, want: "UUID cursor"},
		{name: "limit", pool: pool, tenant: tenant, limit: 101, reason: "Replace key", oldKey: oldKey, newKey: newKey, want: "limit between"},
		{name: "reason", pool: pool, tenant: tenant, limit: 1, reason: " ", oldKey: oldKey, newKey: newKey, want: "reason"},
		{name: "absent old key", pool: pool, tenant: tenant, limit: 1, reason: "Replace key", newKey: newKey, want: "two distinct 32-byte keys"},
		{name: "short old key", pool: pool, tenant: tenant, limit: 1, reason: "Replace key", oldKey: bytes.Repeat([]byte{1}, 31), newKey: newKey, want: "two distinct 32-byte keys"},
		{name: "long old key", pool: pool, tenant: tenant, limit: 1, reason: "Replace key", oldKey: bytes.Repeat([]byte{1}, 33), newKey: newKey, want: "two distinct 32-byte keys"},
		{name: "absent new key", pool: pool, tenant: tenant, limit: 1, reason: "Replace key", oldKey: oldKey, want: "two distinct 32-byte keys"},
		{name: "short new key", pool: pool, tenant: tenant, limit: 1, reason: "Replace key", oldKey: oldKey, newKey: bytes.Repeat([]byte{2}, 31), want: "two distinct 32-byte keys"},
		{name: "long new key", pool: pool, tenant: tenant, limit: 1, reason: "Replace key", oldKey: oldKey, newKey: bytes.Repeat([]byte{2}, 33), want: "two distinct 32-byte keys"},
		{name: "equal bytes in distinct slices", pool: pool, tenant: tenant, limit: 1, reason: "Replace key", oldKey: oldKey, newKey: bytes.Clone(oldKey), want: "two distinct 32-byte keys"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			oldBefore, newBefore := bytes.Clone(tt.oldKey), bytes.Clone(tt.newKey)
			got, err := ReencryptWebhookSecrets(t.Context(), tt.pool, tt.tenant, tt.after, tt.reason, tt.limit, tt.oldKey, tt.newKey)
			if err == nil || !strings.Contains(err.Error(), tt.want) || got != (WebhookReencryptResult{TenantID: tt.tenant}) {
				t.Fatalf("preflight = %#v, %v; want tenant only and %q", got, err, tt.want)
			}
			if !bytes.Equal(tt.oldKey, oldBefore) || !bytes.Equal(tt.newKey, newBefore) {
				t.Fatal("preflight modified caller-owned keys")
			}
		})
	}
}
