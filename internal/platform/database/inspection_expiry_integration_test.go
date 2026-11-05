//go:build integration

package database

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// PostgreSQL owns expiry. Waiting on the host wall clock can finish before the
// container's clock reaches the same timestamp, even with a small sleep margin.
func waitForInspectionExpiry(t *testing.T, pool *pgxpool.Pool, inspectionID string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	var expiresAt, databaseTime time.Time
	for {
		if err := pool.QueryRow(ctx, `SELECT expires_at,clock_timestamp() FROM source_inspections WHERE tenant_id=$1 AND id=$2`, housekeepingTenantA, inspectionID).Scan(&expiresAt, &databaseTime); err != nil {
			t.Fatalf("inspection expiry query: %v (last database time %s, expiry %s)", err, databaseTime.Format(time.RFC3339Nano), expiresAt.Format(time.RFC3339Nano))
		}
		if !databaseTime.Before(expiresAt) {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatalf("inspection did not expire within the test deadline (database time %s, expiry %s)", databaseTime.Format(time.RFC3339Nano), expiresAt.Format(time.RFC3339Nano))
		case <-ticker.C:
		}
	}
}
