// Notification repository units read controlled rows without a database.
package database

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
)

// TestPostgresNotificationState preserves absent checkpoints and bounded counters without
// acknowledging losses. SQL counter updates remain integration assertions.
func TestPostgresNotificationState(t *testing.T) {
	pool := &pgxpool.Pool{}
	store := &PostgresWebhookStore{repository: &PostgresRepository{pool: pool}}
	at := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
	failure := errors.New("notification read failure")
	for _, test := range []struct {
		name    string
		want    catalog.NotificationState
		failure error
	}{
		{name: "no losses", want: catalog.NotificationState{PendingEventLimit: catalog.PendingWebhookEventLimit}},
		{name: "loss checkpoints", want: catalog.NotificationState{PendingEvents: catalog.PendingWebhookEventLimit, PendingEventLimit: catalog.PendingWebhookEventLimit, CapacityLossCount: 7, LastCapacityLossAt: &at, DeliveryLossCount: 3, LastDeliveryLossAt: new(at.Add(time.Hour))}},
		{name: "read failure", failure: failure},
	} {
		t.Run(test.name, func(t *testing.T) {
			want := test.want
			row := scriptedValueRow([]any{want.PendingEvents, want.CapacityLossCount, want.LastCapacityLossAt, want.DeliveryLossCount, want.LastDeliveryLossAt})
			if test.failure != nil {
				row = scannerFunc(func(destinations ...any) error { setScanDestination(t, destinations, 0, int64(1)); return test.failure })
			}
			tx := &identityScriptedTx{fakeCommandTx: &fakeCommandTx{}, queryRows: []pgx.Row{row}}
			connection := &captureCommandConnection{transaction: tx}
			command := &commandTransaction{pool: pool, tenantID: restoreTestTenant, conn: connection}
			got, err := store.NotificationState(withCommandTransaction(t.Context(), command), restoreTestTenant)
			if test.failure != nil {
				if catalog.ErrorCodeOf(err) != catalog.CodeDependencyUnavailable || !errors.Is(err, failure) || got != (catalog.NotificationState{}) || tx.rollbacks != 1 || tx.commits != 0 {
					t.Fatalf("read failure=%+v, %v", got, err)
				}
			} else if err != nil || !reflect.DeepEqual(got, want) || tx.commits != 1 || tx.rollbacks != 0 {
				t.Fatalf("state=%+v, %v; want=%+v", got, err, want)
			}
			if !reflect.DeepEqual(connection.options, []pgx.TxOptions{{AccessMode: pgx.ReadOnly, IsoLevel: pgx.RepeatableRead}}) {
				t.Fatalf("options=%v", connection.options)
			}
			if !reflect.DeepEqual(tx.queryRowArguments, [][]any{{restoreTestTenant, catalog.PendingWebhookEventLimit}}) {
				t.Fatalf("state binding=%v", tx.queryRowArguments)
			}
			if len(tx.statements) != 1 || !strings.Contains(tx.statements[0], "set_config('app.tenant_id'") {
				t.Fatalf("unexpected writes=%v", tx.statements)
			}
			for _, predicate := range []string{"tenant_id=$1 AND NOT is_internal AND published_at IS NULL LIMIT $2", "state.tenant_id=tenant.tenant_id"} {
				if !strings.Contains(tx.queryRowStatements[0], predicate) {
					t.Fatalf("missing selection predicate %s", predicate)
				}
			}
		})
	}
}
