// Restored audit units bind trusted recovery evidence without opening PostgreSQL.
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
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/audit"
)

type restoredAuditFixture struct {
	trusted           audit.TrustedManifest
	tx                *identityScriptedTx
	metadata, records *identityScriptedRows
}

func newRestoredAuditFixture(t *testing.T, populated bool) restoredAuditFixture {
	t.Helper()
	event, metadata, records := auditExportRows(t)
	from, before := event.At.Add(-time.Hour), event.At.Add(time.Hour)
	query := audit.Query{Actor: event.Actor, Action: event.Action, RequestID: event.RequestID,
		TargetType: event.TargetType, TargetID: event.TargetID, OccurredFrom: &from, OccurredBefore: &before}
	events := []audit.Record{event}
	if !populated {
		events, metadata.values, records.values = nil, nil, nil
	}
	// Distinct range end, cutoff and generation times detect accidental rebinding.
	exported, _, err := audit.BuildExport(event.TenantID, query, before.Add(time.Hour), before.Add(2*time.Hour), events)
	if err != nil {
		t.Fatal(err)
	}
	trusted, err := audit.BindTrustedManifest(exported, "unit-trusted-recovery")
	if err != nil {
		t.Fatal(err)
	}
	tx := &identityScriptedTx{fakeCommandTx: &fakeCommandTx{},
		queryRows:    []pgx.Row{scriptedValueRow([]any{"on"})},
		queryResults: []identityQueryResult{{rows: metadata}, {rows: records}}}
	return restoredAuditFixture{trusted: trusted, tx: tx, metadata: metadata, records: records}
}

func TestCompareRestoredAuditTxBindsTrustedRange(t *testing.T) {
	t.Parallel()
	for _, populated := range []bool{false, true} {
		f := newRestoredAuditFixture(t, populated)
		got, err := compareRestoredAuditTx(t.Context(), f.tx, f.trusted, f.trusted.RecoveryPointID)
		want := audit.VerificationReport{Mode: "restore", Valid: true, RowCount: f.trusted.Manifest.RowCount,
			EventsDigest: f.trusted.Manifest.EventsDigest, RecoveryPointID: f.trusted.RecoveryPointID,
			AssuranceBoundary: "exact restored-range comparison against a separately trusted manifest bound to the same operator-proven quiesced recovery point"}
		if err != nil || got != want {
			t.Fatalf("populated=%t: restored report = %#v, %v; want %#v", populated, got, err, want)
		}
		filters := f.trusted.Manifest.Filters
		args := []any{f.trusted.Manifest.TenantID, filters.Actor, filters.Action, filters.RequestID,
			filters.TargetType, filters.TargetID, &filters.OccurredFrom, &filters.OccurredBefore, audit.MaxExportRows + 1}
		if !reflect.DeepEqual(f.tx.queryArguments, [][]any{args, args}) || !reflect.DeepEqual(f.tx.queryRowArguments, [][]any{nil}) || !f.metadata.closed || !f.records.closed {
			t.Fatalf("trusted range binding or row cleanup changed: queries=%v row queries=%v closed=%t/%t", f.tx.queryArguments, f.tx.queryRowArguments, f.metadata.closed, f.records.closed)
		}
		if len(f.tx.statements) != 0 || f.tx.commits != 0 || f.tx.rollbacks != 0 {
			t.Fatal("comparison wrote evidence or took transaction ownership")
		}
	}
}

func TestCompareRestoredAuditTxRefusesUnsafeOrMismatchedRange(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"read-only status", "writable transaction", "selection", "missing retained range"} {
		t.Run(mode, func(t *testing.T) {
			f := newRestoredAuditFixture(t, true)
			failure := errors.New("injected restored audit read failure")
			wantQueries := 0
			wantError := "did not obtain a read-only transaction"
			switch mode {
			case "read-only status":
				f.tx.queryRows[0] = scannerFunc(func(...any) error { return failure })
			case "writable transaction":
				f.tx.queryRows[0] = scriptedValueRow([]any{"off"})
			case "selection":
				f.tx.queryResults[0].err = failure
				wantQueries, wantError = 1, "inspect audit export bounds"
			case "missing retained range":
				f.metadata.values, f.records.values = nil, nil
				wantQueries, wantError = 2, "does not match the trusted manifest"
			}
			got, err := compareRestoredAuditTx(t.Context(), f.tx, f.trusted, f.trusted.RecoveryPointID)
			if err == nil || !strings.Contains(err.Error(), wantError) || got != (audit.VerificationReport{}) {
				t.Fatalf("refused comparison = %#v, %v", got, err)
			}
			if mode == "selection" && !errors.Is(err, failure) {
				t.Fatalf("selection lost its cause: %v", err)
			}
			if len(f.tx.queryRowStatements) != 1 || len(f.tx.queryStatements) != wantQueries || len(f.tx.statements) != 0 || f.tx.commits != 0 || f.tx.rollbacks != 0 {
				t.Fatalf("refusal continued work or took transaction ownership: rows=%d queries=%d writes=%d", len(f.tx.queryRowStatements), len(f.tx.queryStatements), len(f.tx.statements))
			}
			if f.metadata.closed != (wantQueries == 2) || f.records.closed != (wantQueries == 2) {
				t.Fatalf("returned rows not closed or unreturned rows touched: %t/%t", f.metadata.closed, f.records.closed)
			}
		})
	}
}

// TestCompareRestoredAuditTxPreservesExportSizeFailure reaches the envelope
// limit after valid bounded selection, preserving the cause and caller ownership.
func TestCompareRestoredAuditTxPreservesExportSizeFailure(t *testing.T) {
	t.Parallel()
	f := newRestoredAuditFixture(t, true)
	if err := audit.ValidateTrustedManifest(f.trusted, f.trusted.RecoveryPointID); err != nil {
		t.Fatalf("invalid trusted control: %v", err)
	}
	// The trusted small export stays valid; a restored range may contain different
	// evidence. All selected rows still match its tenant, filters and time range.
	event := auditScanFixture(t)
	event.Reason = "x"
	base, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	records := make([]audit.Record, audit.MaxExportRows)
	// Leave one byte below the selection's array limit. The export envelope must
	// exceed it. Subtract array brackets and commas before dividing record bytes.
	arrayLimit := audit.MaxExportResponseBytes - 1
	recordBytes := arrayLimit - 2 - (len(records) - 1)
	f.metadata.values, f.records.values = nil, nil
	for i := range records {
		record := event
		record.ID = fmt.Sprintf("00000000-0000-7000-8000-%012d", i+1)
		size := recordBytes / len(records)
		if i < recordBytes%len(records) {
			size++
		}
		// JSON escapes '<' as six ASCII bytes, filling the byte budget without
		// exceeding the reason's rune limit. Plain bytes supply the remainder.
		reasonBytes := size - (len(base) - len(event.Reason))
		if reasonBytes < 1 || reasonBytes/6+reasonBytes%6 > audit.MaxReasonRunes {
			t.Fatalf("record %d cannot fit a valid reason into %d bytes", i, reasonBytes)
		}
		record.Reason = strings.Repeat("<", reasonBytes/6) + strings.Repeat("x", reasonBytes%6)
		records[i] = record
		// The encoded size is a conservative metadata bound; actual record and
		// detail invariants are validated by EncodeRecords below.
		f.metadata.values = append(f.metadata.values, []any{record.ID, record.At, record.FormatVersion, int64(size)})
		f.records.values = append(f.records.values, auditScanValues(t, record))
	}
	encoded, err := audit.EncodeRecords(records)
	if err != nil || len(encoded) != arrayLimit {
		t.Fatalf("invalid bounded selection: rows=%d bytes=%d want=%d error=%v", len(records), len(encoded), arrayLimit, err)
	}

	got, err := compareRestoredAuditTx(t.Context(), f.tx, f.trusted, f.trusted.RecoveryPointID)
	var tooLarge *audit.RangeTooLargeError
	if !errors.As(err, &tooLarge) || tooLarge.Reason != "encoded response limit exceeded" || !strings.HasPrefix(err.Error(), "rebuild restored audit manifest: ") {
		t.Fatalf("restore lost the export-size cause or failed before rebuilding: %v", err)
	}
	if got != (audit.VerificationReport{}) {
		t.Fatalf("failed rebuild returned a verification report: %#v", got)
	}
	if len(f.tx.queryRowStatements) != 1 || len(f.tx.queryStatements) != 2 || f.metadata.index != len(records) || f.records.index != len(records) || !f.metadata.closed || !f.records.closed {
		t.Fatalf("bounded reads incomplete or rows leaked: status=%d queries=%d metadata=%d/%t records=%d/%t", len(f.tx.queryRowStatements), len(f.tx.queryStatements), f.metadata.index, f.metadata.closed, f.records.index, f.records.closed)
	}
	if len(f.tx.statements) != 0 || f.tx.commits != 0 || f.tx.rollbacks != 0 {
		t.Fatal("failed rebuild wrote evidence or took transaction ownership")
	}
}
