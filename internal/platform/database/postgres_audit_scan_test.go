// Audit row tests preserve evidence precision without querying a database.
package database

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/audit"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
)

// auditScanFixture validates a current-format record before exposing its columns
// to the scanner; generation exceeds float64's exact integer range.
func auditScanFixture(t *testing.T) catalog.AuditEvent {
	t.Helper()
	digest := "sha256:" + strings.Repeat("a", 64)
	event, err := audit.NewRecord("00000000-0000-7000-8000-000000000090", restoreTestTenant,
		audit.Principal{ID: "catalog-api", Type: audit.PrincipalServiceExecutor},
		audit.ActionPublicationActivated, "publication_snapshot", "00000000-0000-7000-8000-000000000020",
		"Activate reviewed snapshot", audit.Execution{RequestID: "request-unit-20"}, map[string]any{
			"host": "catalog.example.com", "generation": json.Number("9007199254740993"),
			"ardProfile": "ard-test", "aiCatalogProfile": "ai-catalog-test", "ardDigest": digest, "aiCatalogDigest": digest,
		}, time.Date(2026, 9, 19, 13, 0, 0, 123456000, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	event.BeforeDigest, event.AfterDigest = digest, "sha256:"+strings.Repeat("b", 64)
	if err := audit.ValidateNewRecord(event); err != nil {
		t.Fatalf("invalid audit control: %v", err)
	}
	return event
}

// auditScanValues uses the existing scripted row without emulating SQL or pgx.
func auditScanValues(t *testing.T, event catalog.AuditEvent) []any {
	t.Helper()
	details, err := json.Marshal(event.Details)
	if err != nil {
		t.Fatal(err)
	}
	return []any{event.FormatVersion, event.ID, event.TenantID, event.Actor, event.Action, event.TargetType,
		event.TargetID, event.Reason, event.RequestID, event.BeforeDigest, event.AfterDigest, details, event.At}
}

// TestScanAuditEventPreservesEvidence keeps integer details exact and timestamps
// canonical without changing attribution or the rest of the retained record.
func TestScanAuditEventPreservesEvidence(t *testing.T) {
	t.Parallel()
	want := auditScanFixture(t)
	values := auditScanValues(t, want)
	values[12] = time.Date(2026, 9, 19, 15, 0, 0, 123456789, time.FixedZone("database", 2*60*60))
	got, err := scanAuditEvent(scriptedValueRow(values))
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("audit evidence changed: got=%+v error=%v", got, err)
	}
	facts, ok := got.Details["facts"].(map[string]any)
	if !ok || facts["generation"] != json.Number("9007199254740993") || got.At.Location() != time.UTC || got.At.Nanosecond() != 123456000 {
		t.Fatalf("audit precision changed: facts=%v at=%v", facts, got.At)
	}
}

// TestScanAuditEventScanErrors discards partially scanned fields and retains the
// driver failure while classifying an absent row separately.
func TestScanAuditEventScanErrors(t *testing.T) {
	t.Parallel()
	failure := errors.New("synthetic audit scan failure")
	for _, test := range []struct {
		name string
		err  error
		code catalog.ErrorCode
	}{
		{"missing", pgx.ErrNoRows, catalog.CodeNotFound},
		{"driver", failure, catalog.CodeDependencyUnavailable},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := scanAuditEvent(scannerFunc(func(destinations ...any) error {
				setScanDestination(t, destinations, 1, "partial")
				return test.err
			}))
			if catalog.ErrorCodeOf(err) != test.code || !reflect.DeepEqual(got, catalog.AuditEvent{}) || (errors.Is(test.err, failure) && !errors.Is(err, failure)) {
				t.Fatalf("scan failure returned event=%+v error=%v", got, err)
			}
		})
	}
}
