package database

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/audit"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/clock"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/safeerr"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/webhook"
)

// Scripted rows test the Go adapter contract, not PostgreSQL execution or locks.
func webhookAdapterFixture(t *testing.T) (*PostgresWebhookStore, *identityScriptedTx, context.Context, webhook.Endpoint, webhook.Delivery) {
	t.Helper()
	pool := &pgxpool.Pool{}
	repo, err := NewPostgresRepository(pool)
	if err != nil {
		t.Fatal(err)
	}
	store, err := NewPostgresWebhookStore(repo)
	if err != nil {
		t.Fatal(err)
	}
	tx := &identityScriptedTx{fakeCommandTx: &fakeCommandTx{}}
	at := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
	endpoint := webhook.Endpoint{ID: "00000000-0000-7000-8000-000000000010", TenantID: restoreTestTenant, Name: "Receiver", URL: "https://hooks.example.com/events", EventTypes: []string{"*"}, Status: webhook.StatusActive, Revision: 2, SecretVersion: 1, SecretCiphertext: []byte("encrypted-fixture"), CreatedBy: "author", CreatedAt: at, UpdatedAt: at}
	delivery := webhook.Delivery{ID: "00000000-0000-7000-8000-000000000011", TenantID: restoreTestTenant, EndpointID: endpoint.ID, EventID: "00000000-0000-7000-8000-000000000012", EventType: "catalog.resource.created", State: webhook.DeliverySucceeded, Attempt: 2, ResponseStatus: 204, CreatedAt: at, UpdatedAt: at}
	return store, tx, identityRepositoryContext(pool, tx), endpoint, delivery
}

func webhookEndpointValues(e webhook.Endpoint) []any {
	return []any{e.ID, e.TenantID, e.Name, e.URL, e.EventTypes, e.Status, e.Revision, e.SecretVersion, e.SecretCiphertext, e.CreatedBy, e.CreatedAt, e.UpdatedAt}
}
func webhookDeliveryValues(d webhook.Delivery) []any {
	return []any{d.ID, d.TenantID, d.EndpointID, d.EventID, d.EventType, d.State, d.Attempt, d.ResponseStatus, d.SafeError, d.NextAttemptAt, d.CreatedAt, d.UpdatedAt}
}
func webhookEndpointAudit(t *testing.T, e webhook.Endpoint, action string) audit.Record {
	t.Helper()
	return currentTestAuditFacts(t, action, "webhook_endpoint", e.ID, e.UpdatedAt, map[string]any{"revision": e.Revision, "secretVersion": e.SecretVersion, "status": string(e.Status)})
}
func webhookDeliveryAudit(t *testing.T, d webhook.Delivery) audit.Record {
	t.Helper()
	return currentTestAuditFacts(t, audit.ActionWebhookDeliveryAttempted, "webhook_delivery", d.ID, d.UpdatedAt, map[string]any{"endpointId": d.EndpointID, "eventId": d.EventID, "attempt": d.Attempt, "state": string(d.State), "httpStatus": d.ResponseStatus})
}

func TestWebhookEndpointTimeRegressionDiagnostic(t *testing.T) {
	for _, tc := range []struct {
		name, code, message string
		delta               time.Duration
		plain, classified   bool
	}{
		{name: "backdated guard", code: "P0001", message: "webhook endpoint lifecycle transition is invalid", delta: -time.Microsecond, classified: true},
		{name: "equal time guard", code: "P0001", message: "webhook endpoint lifecycle transition is invalid"},
		{name: "later time guard", code: "P0001", message: "webhook endpoint lifecycle transition is invalid", delta: time.Microsecond},
		{name: "different guard", code: "P0001", message: "disabled webhook endpoint is terminal", delta: -time.Microsecond},
		{name: "different SQL state", code: "P0002", message: "webhook endpoint lifecycle transition is invalid", delta: -time.Microsecond},
		{name: "untyped error", message: "webhook endpoint lifecycle transition is invalid", delta: -time.Microsecond, plain: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store, tx, ctx, endpoint, _ := webhookAdapterFixture(t)
			current := endpoint
			current.Revision = 1
			endpoint.UpdatedAt = endpoint.UpdatedAt.Add(tc.delta)
			var cause error = &pgconn.PgError{Code: tc.code, Message: tc.message, Detail: "private SQL and encrypted material"}
			if tc.plain {
				cause = errors.New(tc.message)
			}
			tx.queryRows = []pgx.Row{scriptedValueRow(webhookEndpointValues(current)), scannerFunc(func(...any) error { return cause })}
			got, err := store.UpdateEndpoint(ctx, endpoint, 1, webhookEndpointAudit(t, endpoint, audit.ActionWebhookUpdated))
			wantStage := "command"
			if tc.classified {
				wantStage = "webhook.endpoint.time_regression"
			}
			if !errors.Is(err, cause) || catalog.ErrorCodeOf(err) != catalog.CodeDependencyUnavailable || safeerr.Stage(err) != wantStage || !reflect.DeepEqual(got, webhook.Endpoint{}) || tx.rollbacks != 1 || len(tx.statements) != 0 || len(tx.queryRowStatements) != 2 {
				t.Fatalf("failed endpoint update diagnostic did not preserve its cause, classification and rollback: %v", err)
			}
		})
	}
}

func TestWebhookEndpointAdapterWrites(t *testing.T) {
	t.Parallel()
	if _, err := NewPostgresWebhookStore(nil); err == nil {
		t.Fatal("nil repository accepted")
	}
	for _, operation := range []string{"create", "update"} {
		for _, mode := range []string{"success", "invalid evidence", "read failure", "stale revision", "write failure", "audit failure"} {
			if operation == "create" && (mode == "read failure" || mode == "stale revision") {
				continue
			}
			t.Run(operation+"/"+mode, func(t *testing.T) {
				store, tx, ctx, e, _ := webhookAdapterFixture(t)
				action := audit.ActionWebhookCreated
				if operation == "update" {
					action = audit.ActionWebhookUpdated
				} else {
					e.Revision = 1
				}
				r := webhookEndpointAudit(t, e, action)
				sentinel := errors.New("endpoint driver failed")
				if mode == "invalid evidence" {
					r.TargetID = "other"
				}
				current := e
				current.Revision = 1
				tx.queryRows = []pgx.Row{scriptedValueRow(webhookEndpointValues(current)), scriptedValueRow(webhookEndpointValues(e))}
				wantRows, wantWrites := 0, 2
				if operation == "update" {
					wantRows, wantWrites = 2, 1
				}
				switch mode {
				case "invalid evidence":
					wantWrites = 0
					if operation == "update" {
						wantRows = 1
					}
				case "read failure":
					tx.queryRows[0] = scannerFunc(func(...any) error { return sentinel })
					wantRows, wantWrites = 1, 0
				case "stale revision":
					current.Revision = 9
					tx.queryRows[0] = scriptedValueRow(webhookEndpointValues(current))
					wantRows, wantWrites = 1, 0
				case "write failure":
					if operation == "create" {
						tx.execErrors = []error{sentinel}
						wantWrites = 1
					} else {
						tx.queryRows[1] = scannerFunc(func(...any) error { return sentinel })
						wantWrites = 0
					}
				case "audit failure":
					if operation == "create" {
						tx.execErrors = []error{nil, sentinel}
					} else {
						tx.execErrors = []error{sentinel}
					}
				}
				var got webhook.Endpoint
				var err error
				if operation == "create" {
					tx.queryRows = nil
					got, err = store.CreateEndpoint(ctx, e, r)
				} else {
					got, err = store.UpdateEndpoint(ctx, e, 1, r)
				}
				if mode == "success" {
					if err != nil || !reflect.DeepEqual(got, e) || tx.rollbacks != 0 {
						t.Fatalf("result=%+v error=%v", got, err)
					}
				} else {
					code := catalog.CodeInternal
					if mode == "stale revision" {
						code = catalog.CodePreconditionFailed
					}
					if mode == "invalid evidence" || mode == "stale revision" {
						if catalog.ErrorCodeOf(err) != code {
							t.Fatalf("error=%v", err)
						}
					} else if !errors.Is(err, sentinel) {
						t.Fatalf("lost driver error: %v", err)
					}
					if got.ID != "" || tx.rollbacks != 1 {
						t.Fatal("failed write returned endpoint or omitted rollback")
					}
				}
				if len(tx.queryRowStatements) != wantRows || len(tx.statements) != wantWrites || tx.commits != 0 {
					t.Fatalf("reads/writes=%d/%d want=%d/%d", len(tx.queryRowStatements), len(tx.statements), wantRows, wantWrites)
				}
				if mode == "success" {
					if operation == "create" && !reflect.DeepEqual(tx.arguments[0], webhookEndpointValues(e)) {
						t.Fatal("create lost endpoint binding")
					}
					if operation == "update" && !reflect.DeepEqual(tx.queryRowArguments[1], []any{e.ID, e.Name, e.URL, e.EventTypes, e.Status, e.Revision, e.SecretVersion, e.SecretCiphertext, e.UpdatedAt, e.TenantID}) {
						t.Fatal("update lost tenant/secret/version binding")
					}
				}
			})
		}
	}
}

func TestWebhookDeliveryAdapterRejectsUnboundReturnedEvidence(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"success", "invalid evidence", "write failure", "changed returned identity", "changed returned attempt", "audit failure"} {
		t.Run(mode, func(t *testing.T) {
			store, tx, ctx, _, d := webhookAdapterFixture(t)
			r := webhookDeliveryAudit(t, d)
			returned := d
			sentinel := errors.New("delivery driver failed")
			wantRows, wantWrites := 1, 1
			switch mode {
			case "invalid evidence":
				r.TargetID = "other"
				wantRows, wantWrites = 0, 0
			case "changed returned identity":
				returned.ID = "other"
				wantWrites = 0
			case "changed returned attempt":
				returned.Attempt++
				wantWrites = 0
			case "write failure":
				wantWrites = 0
			case "audit failure":
				tx.execErrors = []error{sentinel}
			}
			tx.queryRows = []pgx.Row{scriptedValueRow(webhookDeliveryValues(returned))}
			if mode == "write failure" {
				tx.queryRows[0] = scannerFunc(func(...any) error { return sentinel })
			}
			got, err := store.SaveDelivery(ctx, d, r)
			if mode == "success" {
				if err != nil || !reflect.DeepEqual(got, d) || tx.rollbacks != 0 {
					t.Fatalf("delivery=%+v error=%v", got, err)
				}
			} else {
				if mode == "write failure" || mode == "audit failure" {
					if !errors.Is(err, sentinel) {
						t.Fatalf("error=%v", err)
					}
				} else if catalog.ErrorCodeOf(err) != catalog.CodeInternal {
					t.Fatalf("error=%v", err)
				}
				if got.ID != "" || tx.rollbacks != 1 {
					t.Fatal("failed delivery was returned or not rolled back")
				}
			}
			if len(tx.queryRowStatements) != wantRows || len(tx.statements) != wantWrites || tx.commits != 0 {
				t.Fatalf("reads=%d writes=%d", len(tx.queryRowStatements), len(tx.statements))
			}
			if wantRows > 0 && !reflect.DeepEqual(tx.queryRowArguments[0], webhookDeliveryValues(d)) {
				t.Fatal("delivery binding changed")
			}
		})
	}
}

func TestWebhookClaimedOutcomeChecksLeaseBeforeAndAfterWrites(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"success", "missing owner", "missing clock", "lost claim", "claim read failure", "first check failure", "expired before write", "write failure", "last check failure", "expired after write"} {
		t.Run(mode, func(t *testing.T) {
			store, tx, ctx, _, d := webhookAdapterFixture(t)
			r := webhookDeliveryAudit(t, d)
			owner := "worker"
			var c catalog.Clock = clock.NewFake(d.UpdatedAt)
			sentinel := errors.New("claim driver failed")
			tx.queryRows = []pgx.Row{scriptedValueRow([]any{d.EventID}), scriptedValueRow([]any{true}), scriptedValueRow(webhookDeliveryValues(d)), scriptedValueRow([]any{true})}
			wantRows, wantWrites := 4, 1
			wantCode := catalog.CodeConflict
			wantErr := error(nil)
			switch mode {
			case "missing owner":
				owner = ""
				wantRows, wantWrites = 0, 0
			case "missing clock":
				c = nil
				wantRows, wantWrites = 0, 0
			case "lost claim":
				tx.queryRows[0] = scannerFunc(func(...any) error { return pgx.ErrNoRows })
				wantRows, wantWrites = 1, 0
			case "claim read failure":
				tx.queryRows[0] = scannerFunc(func(...any) error { return sentinel })
				wantRows, wantWrites = 1, 0
				wantErr = sentinel
			case "first check failure":
				tx.queryRows[1] = scannerFunc(func(...any) error { return sentinel })
				wantRows, wantWrites = 2, 0
				wantErr = sentinel
			case "expired before write":
				tx.queryRows[1] = scriptedValueRow([]any{false})
				wantRows, wantWrites = 2, 0
			case "write failure":
				tx.queryRows[2] = scannerFunc(func(...any) error { return sentinel })
				wantRows, wantWrites = 3, 0
				wantErr = sentinel
			case "last check failure":
				tx.queryRows[3] = scannerFunc(func(...any) error { return sentinel })
				wantErr = sentinel
			case "expired after write":
				tx.queryRows[3] = scriptedValueRow([]any{false})
			}
			got, err := store.SaveClaimedDelivery(ctx, d, owner, c, r)
			if mode == "success" {
				if err != nil || !reflect.DeepEqual(got, d) || tx.rollbacks != 0 {
					t.Fatalf("delivery=%+v error=%v", got, err)
				}
			} else {
				if wantErr != nil {
					if !errors.Is(err, wantErr) {
						t.Fatalf("error=%v", err)
					}
				} else if catalog.ErrorCodeOf(err) != wantCode {
					t.Fatalf("error=%v", err)
				}
				if got.ID != "" || wantRows > 0 && tx.rollbacks != 1 || wantRows == 0 && tx.rollbacks != 0 {
					t.Fatal("invalid claim outcome or rollback")
				}
			}
			if len(tx.queryRowStatements) != wantRows || len(tx.statements) != wantWrites || tx.commits != 0 {
				t.Fatalf("reads/writes=%d/%d want=%d/%d", len(tx.queryRowStatements), len(tx.statements), wantRows, wantWrites)
			}
			if wantRows > 0 && !reflect.DeepEqual(tx.queryRowArguments[0], []any{d.TenantID, d.EventID, owner}) {
				t.Fatal("claim lost tenant/event/owner binding")
			}
			for _, index := range []int{1, 3} {
				if wantRows > index && !reflect.DeepEqual(tx.queryRowArguments[index], []any{d.TenantID, d.EventID, d.UpdatedAt}) {
					t.Fatal("lease check lost tenant/event/clock binding")
				}
			}
		})
	}
}

func TestWebhookAdapterReadsAndLists(t *testing.T) {
	t.Parallel()
	for _, operation := range []string{"endpoint", "delivery", "endpoints", "pending", "deliveries"} {
		for _, mode := range []string{"valid", "empty", "query failure", "scan failure", "iterator failure"} {
			point := operation == "endpoint" || operation == "delivery"
			if point && (mode == "empty" || mode == "iterator failure") {
				continue
			}
			t.Run(operation+"/"+mode, func(t *testing.T) {
				store, tx, ctx, e, d := webhookAdapterFixture(t)
				values := webhookEndpointValues(e)
				if operation == "delivery" || operation == "deliveries" {
					values = webhookDeliveryValues(d)
				}
				sentinel := errors.New("read failed")
				rows := &identityScriptedRows{values: [][]any{values}}
				if mode == "empty" {
					rows.values = nil
				}
				if mode == "scan failure" {
					rows.values = [][]any{{"wrong shape"}}
				}
				if mode == "iterator failure" {
					rows.err = sentinel
				}
				tx.queryResults = []identityQueryResult{{rows: rows}}
				if mode == "query failure" {
					tx.queryResults[0].err = sentinel
				}
				tx.queryRows = []pgx.Row{scriptedValueRow(values)}
				if mode == "query failure" || mode == "scan failure" {
					tx.queryRows[0] = scannerFunc(func(...any) error { return sentinel })
				}
				var err error
				var got any
				var want any
				var args []any
				switch operation {
				case "endpoint":
					got, err = store.GetEndpoint(ctx, e.TenantID, e.ID)
					want = e
					args = []any{e.TenantID, e.ID}
				case "delivery":
					got, err = store.GetDelivery(ctx, d.TenantID, d.EndpointID, d.EventID)
					want = d
					args = []any{d.TenantID, d.EndpointID, d.EventID}
				case "endpoints":
					got, err = store.ListEndpointPage(ctx, e.TenantID, webhook.EndpointListRequest{AfterName: "Previous", AfterID: e.ID, Limit: 3, Name: "Exact", Status: webhook.StatusActive})
					want = []webhook.Endpoint{e}
					args = []any{e.TenantID, "Previous", e.ID, 3, "Exact", webhook.StatusActive}
					if mode == "empty" {
						want = []webhook.Endpoint{}
					}
				case "pending":
					got, err = store.ListPendingEndpoints(ctx, catalog.OutboxEvent{ID: d.EventID, TenantID: d.TenantID, Type: d.EventType, CreatedAt: d.CreatedAt}, 3)
					want = []webhook.Endpoint{e}
					args = []any{d.TenantID, d.EventID, d.CreatedAt, d.EventType, 3}
					if mode == "empty" {
						want = []webhook.Endpoint{}
					}
				case "deliveries":
					got, err = store.ListDeliveries(ctx, d.TenantID, webhook.DeliveryListRequest{EndpointID: e.ID, AfterUpdatedAt: &d.UpdatedAt, AfterID: d.ID, Limit: 3, DeliveryID: d.ID, EventID: d.EventID, State: d.State})
					want = []webhook.Delivery{d}
					args = []any{d.TenantID, e.ID, &d.UpdatedAt, d.ID, 3, d.ID, d.EventID, d.State}
					if mode == "empty" {
						want = []webhook.Delivery{}
					}
				}
				if mode == "valid" || mode == "empty" {
					if err != nil || !reflect.DeepEqual(got, want) {
						t.Fatalf("result=%+v want=%+v error=%v", got, want, err)
					}
				} else if err == nil || mode != "scan failure" && !errors.Is(err, sentinel) {
					t.Fatalf("error=%v", err)
				}
				if point {
					if !reflect.DeepEqual(tx.queryRowArguments, [][]any{args}) {
						t.Fatal("point lookup lost binding")
					}
				} else {
					if !reflect.DeepEqual(tx.queryArguments, [][]any{args}) || mode != "query failure" && !rows.closed {
						t.Fatal("page lost binding or leaked rows")
					}
				}
				if len(tx.statements) != 0 || tx.commits != 0 {
					t.Fatal("unaudited fixture read wrote data")
				}
			})
		}
	}
	for _, limit := range []int{0, 10} {
		store, tx, ctx, _, d := webhookAdapterFixture(t)
		if _, err := store.ListPendingEndpoints(ctx, catalog.OutboxEvent{TenantID: d.TenantID}, limit); catalog.ErrorCodeOf(err) != catalog.CodeInvalidArgument || len(tx.queryStatements) != 0 {
			t.Fatalf("unbounded selection: %v", err)
		}
	}
}

func TestWebhookDeliveryReadAuditFailureDoesNotDiscloseRows(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"success", "builder failure", "audit failure"} {
		t.Run(mode, func(t *testing.T) {
			store, tx, ctx, e, d := webhookAdapterFixture(t)
			sentinel := errors.New("audit failed")
			rows := &identityScriptedRows{values: [][]any{webhookDeliveryValues(d)}}
			tx.queryResults = []identityQueryResult{{rows: rows}}
			calls := 0
			builder := func(items []webhook.Delivery) (audit.Record, error) {
				calls++
				if !rows.closed || !reflect.DeepEqual(items, []webhook.Delivery{d}) {
					t.Fatal("audit builder received incomplete rows")
				}
				if mode == "builder failure" {
					return audit.Record{}, sentinel
				}
				raw, err := json.Marshal(items)
				if err != nil {
					t.Fatal(err)
				}
				return currentTestAuditFacts(t, audit.ActionWebhookDeliveriesRead, "webhook_endpoint", e.ID, e.UpdatedAt, map[string]any{"deliveryCount": 1, "deliveriesDigest": audit.DigestBytes(raw)}), nil
			}
			if mode == "audit failure" {
				tx.execErrors = []error{sentinel}
			}
			got, err := store.ListDeliveries(ctx, e.TenantID, webhook.DeliveryListRequest{EndpointID: e.ID, Limit: 2}, builder)
			if mode == "success" {
				if err != nil || !reflect.DeepEqual(got, []webhook.Delivery{d}) {
					t.Fatalf("items=%v error=%v", got, err)
				}
			} else if !errors.Is(err, sentinel) || got != nil || tx.rollbacks != 1 {
				t.Fatalf("failed evidence disclosed rows: %v %v", got, err)
			}
			wantWrites := 1
			if mode == "builder failure" {
				wantWrites = 0
			}
			if calls != 1 || len(tx.statements) != wantWrites || tx.commits != 0 {
				t.Fatal("unexpected evidence write count")
			}
		})
	}
}
