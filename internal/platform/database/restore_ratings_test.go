package database

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/ratings"
)

// Scripted rows qualify Go classification and redaction. The owned PostgreSQL
// restore drill qualifies real selection, history, rollups and privileges.
func TestRestoreRatingsClassificationAndRedaction(t *testing.T) {
	target := ratings.Target{Origin: "local", ResourceID: "00000000-0000-7000-8000-000000000010", VersionRevisionID: "00000000-0000-7000-8000-000000000011", Identifier: "urn:air:example.com:test:agent", Kind: "agent", VersionPresent: true, Version: "1.0.0"}
	raw, err := json.Marshal(target)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, key        string
		raw              []byte
		invalid, finding bool
	}{
		{"target", "target:" + ratings.Key(target), raw, false, false},
		{"vote", "vote:retained-id", nil, false, false},
		{"sharing", "sharing:retained-id", []byte(`{"host":"example.com","audience":"anonymous"}`), false, false},
		{"SQL history or totals", "vote:retained-id", []byte(`{"principal":"PRIVATE_PRINCIPAL","score":"PRIVATE_SCORE"}`), true, true},
		{"target identity", "target:wrong", raw, false, true},
		{"malformed target", "target:wrong", []byte(`{"principal":"PRIVATE_PRINCIPAL"`), false, true},
		{"sharing audience", "sharing:retained-id", []byte(`{"host":"example.com","audience":"PRIVATE_PRINCIPAL"}`), false, true},
		{"sharing path", "sharing:retained-id", []byte(`{"host":"example.com/path","audience":"anonymous"}`), false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rows := &identityScriptedRows{values: [][]any{{tc.key, tc.raw, tc.invalid}}}
			tx := &identityScriptedTx{queryResults: []identityQueryResult{{rows: rows}}}
			records, more, err := scanRestoreRatings(t.Context(), tx, restoreTestTenant, "previous", 2)
			if err != nil || more || len(records) != 1 || !rows.closed {
				t.Fatalf("result=%v more=%v err=%v closed=%v", records, more, err, rows.closed)
			}
			if got := records[0].finding; (got != nil) != tc.finding {
				t.Fatalf("finding=%+v", got)
			} else if got != nil {
				if got.Code != "RESTORE_RATING_INVALID" || got.Class != "integrity" || got.Severity != "error" || got.Domain != "rating" || got.RecordID != tc.key {
					t.Fatalf("finding=%+v", got)
				}
				encoded, err := json.Marshal(got)
				if err != nil {
					t.Fatal(err)
				}
				if strings.Contains(string(encoded), "PRIVATE_PRINCIPAL") || strings.Contains(string(encoded), "PRIVATE_SCORE") {
					t.Fatal("finding leaked private evidence")
				}
			}
			if tx.queryArguments[0][0] != restoreTestTenant || tx.queryArguments[0][1] != "previous" || tx.queryArguments[0][2] != 3 {
				t.Fatalf("unbounded or wrong scope: %v", tx.queryArguments)
			}
		})
	}
}
