// Restored audit comparison checks a trusted recovery-point range in a read-only
// snapshot without changing retained records.

package database

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/audit"
)

// CompareRestoredAudit performs a bounded read-only exact-range comparison.
// The caller must supply a separately trusted manifest and the independently
// recorded recovery-point identifier for the same quiesced-writer interval.
func CompareRestoredAudit(ctx context.Context, pool *pgxpool.Pool, trusted audit.TrustedManifest, recoveryPointID string) (audit.VerificationReport, error) {
	if pool == nil || strings.TrimSpace(recoveryPointID) == "" || audit.ValidateTrustedManifest(trusted, recoveryPointID) != nil {
		return audit.VerificationReport{}, errors.New("restored audit comparison requires a matching trusted recovery-point binding")
	}
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return audit.VerificationReport{}, fmt.Errorf("begin read-only restored audit comparison: %w", err)
	}
	defer rollbackForCleanup(ctx, tx)()
	if err := setTenantContext(ctx, tx, trusted.Manifest.TenantID); err != nil {
		return audit.VerificationReport{}, err
	}
	report, err := compareRestoredAuditTx(ctx, tx, trusted, recoveryPointID)
	if err != nil {
		return audit.VerificationReport{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return audit.VerificationReport{}, fmt.Errorf("complete read-only restored audit comparison: %w", err)
	}
	return report, nil
}

// compareRestoredAuditTx compares a validated trusted manifest in the caller's
// tenant-scoped snapshot. The caller retains commit and cleanup ownership.
func compareRestoredAuditTx(ctx context.Context, tx pgx.Tx, trusted audit.TrustedManifest, recoveryPointID string) (audit.VerificationReport, error) {
	var readOnly string
	if err := tx.QueryRow(ctx, `SHOW transaction_read_only`).Scan(&readOnly); err != nil || readOnly != "on" {
		return audit.VerificationReport{}, errors.New("restored audit comparison did not obtain a read-only transaction")
	}
	filters := trusted.Manifest.Filters
	query := audit.Query{Actor: filters.Actor, Action: filters.Action, RequestID: filters.RequestID, TargetType: filters.TargetType, TargetID: filters.TargetID, OccurredFrom: &filters.OccurredFrom, OccurredBefore: &filters.OccurredBefore}
	records, err := selectAuditExportRecords(ctx, tx, trusted.Manifest.TenantID, query)
	if err != nil {
		return audit.VerificationReport{}, err
	}
	rebuilt, _, err := audit.BuildExport(trusted.Manifest.TenantID, query, trusted.Manifest.Cutoff, trusted.Manifest.GeneratedAt, records)
	if err != nil {
		return audit.VerificationReport{}, fmt.Errorf("rebuild restored audit manifest: %w", err)
	}
	if !audit.ManifestsEqual(rebuilt.Manifest, trusted.Manifest) {
		return audit.VerificationReport{}, errors.New("restored audit range does not match the trusted manifest")
	}
	return audit.VerificationReport{Mode: "restore", Valid: true, RowCount: rebuilt.Manifest.RowCount, EventsDigest: rebuilt.Manifest.EventsDigest, RecoveryPointID: recoveryPointID, AssuranceBoundary: "exact restored-range comparison against a separately trusted manifest bound to the same operator-proven quiesced recovery point"}, nil
}
