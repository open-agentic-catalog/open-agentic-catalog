//go:build integration

package database

import (
	"fmt"
	"testing"
)

func TestSourceCleanupSQLRequiresCurrentJobPayload(t *testing.T) {
	database := newHousekeepingDatabase(t)
	const sourceID = "00000000-0000-7000-8000-000000000811"
	const otherSourceID = "00000000-0000-7000-8000-000000000812"
	payload := func(source, trigger string) string {
		return fmt.Sprintf(`{"sourceId":%q,"sourceRevision":1,"trigger":%q}`, source, trigger)
	}
	// Synthetic rows exercise defensive handling without disabling the real
	// schema's current-payload constraint or inserting unsupported retained data.
	query := `WITH state AS (
 SELECT $1::uuid tenant_id,$2::uuid source_id,1::bigint source_revision
), jobs AS (
 SELECT $3::integer payload_version,$4::jsonb payload,$5::text state,
        $6::uuid tenant_id,'source.sync'::text kind
)
SELECT NOT (true ` + sourceSyncCleanupBlocker + `) FROM state`
	for _, test := range []struct {
		name, payload, state, tenant string
		version                      int
		blocked                      bool
	}{
		{"current manual", payload(sourceID, "manual"), "queued", housekeepingTenantA, 2, true},
		{"current automatic", payload(sourceID, "automatic"), "running", housekeepingTenantA, 2, true},
		{"current other source", payload(otherSourceID, "manual"), "queued", housekeepingTenantA, 2, false},
		{"obsolete implicit manual", fmt.Sprintf(`{"sourceId":%q,"sourceRevision":1}`, otherSourceID), "queued", housekeepingTenantA, 1, true},
		{"obsolete explicit manual", payload(otherSourceID, "manual"), "waiting", housekeepingTenantA, 1, true},
		{"unknown version", payload(otherSourceID, "manual"), "queued", housekeepingTenantA, 3, true},
		{"malformed current", `{}`, "queued", housekeepingTenantA, 2, true},
		{"other tenant", `{}`, "queued", housekeepingTenantB, 2, false},
		{"terminal obsolete", `{}`, "succeeded", housekeepingTenantA, 1, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			var blocked bool
			if err := database.worker.QueryRow(t.Context(), query, housekeepingTenantA, sourceID, test.version, test.payload, test.state, test.tenant).Scan(&blocked); err != nil {
				t.Fatal(err)
			}
			if blocked != test.blocked {
				t.Fatalf("cleanup blocked = %t, want %t", blocked, test.blocked)
			}
		})
	}
}
