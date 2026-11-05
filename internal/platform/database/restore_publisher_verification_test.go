package database

import (
	"context"
	"testing"
)

func TestRestorePublisherVerificationBoundsAndPrivateFindings(t *testing.T) {
	t.Parallel()
	for _, invalid := range []bool{false, true} {
		rows := &identityScriptedRows{values: [][]any{{"00000000-0000-7000-8000-000000000011", invalid}, {"00000000-0000-7000-8000-000000000012", false}}}
		tx := &identityScriptedTx{queryResults: []identityQueryResult{{rows: rows}}}
		records, more, err := scanRestorePublisherVerification(context.Background(), tx, restoreTestTenant, "", 1)
		if err != nil || !more || len(records) != 1 || !rows.closed || tx.queryArguments[0][2] != 2 {
			t.Fatalf("bounded scan: %+v %v %v", records, more, err)
		}
		if (records[0].finding != nil) != invalid {
			t.Fatal("invalid evidence finding lost")
		}
		if invalid && (records[0].finding.Code != "RESTORE_PUBLISHER_VERIFICATION_INVALID" || records[0].finding.RecordID != records[0].key) {
			t.Fatal("finding lost private record binding")
		}
	}
}
