// Search repair rebuilds derived lexical projections in bounded tenant batches
// without rewriting immutable source documents.

package database

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/audit"
)

const MaxCatalogSearchProjectionRepairBatch = 1000

// CatalogSearchProjectionRepairResult describes one resumable tenant-scoped
// rebuild batch. Search projections are derived and carry no domain authority;
// the result exposes bounded progress for operator review.
type CatalogSearchProjectionRepairResult struct {
	TenantID         string `json:"tenantId"`
	Processed        int64  `json:"processed"`
	HasMore          bool   `json:"hasMore"`
	Complete         bool   `json:"complete"`
	BeforeGeneration int64  `json:"beforeGeneration"`
	AfterGeneration  int64  `json:"afterGeneration"`
	AuditEventID     string `json:"auditEventId,omitempty"`
}

type searchProjectionRepairFacts struct {
	SelectedFirstID  string `json:"selectedFirstId"`
	SelectedLastID   string `json:"selectedLastId"`
	Processed        int64  `json:"processed"`
	HasMore          bool   `json:"hasMore"`
	BeforeGeneration int64  `json:"beforeGeneration"`
	AfterGeneration  int64  `json:"afterGeneration"`
}

// RepairCatalogSearchProjections rebuilds one bounded tenant page of missing or
// outdated observation projections using the exact repair index and audits changed
// rows.
func RepairCatalogSearchProjections(ctx context.Context, pool *pgxpool.Pool, tenantID string, limit int, reason, operatorLabel string) (CatalogSearchProjectionRepairResult, error) {
	if pool == nil {
		return CatalogSearchProjectionRepairResult{}, errors.New("PostgreSQL pool is required")
	}
	if err := ValidateCatalogSearchProjectionRepairInput(tenantID, limit, reason, operatorLabel); err != nil {
		return CatalogSearchProjectionRepairResult{}, err
	}
	reason = strings.TrimSpace(reason)
	operatorLabel = strings.TrimSpace(operatorLabel)
	if err := verifySearchProjectionRepairRole(ctx, pool); err != nil {
		return CatalogSearchProjectionRepairResult{}, err
	}
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return CatalogSearchProjectionRepairResult{}, fmt.Errorf("begin search projection repair: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	schema, _, err := currentCatalogSchema(ctx, tx)
	if err != nil {
		return CatalogSearchProjectionRepairResult{}, err
	}
	if err := verifySearchProjectionRepairIndex(ctx, tx, schema); err != nil {
		return CatalogSearchProjectionRepairResult{}, fmt.Errorf("search projection repair requires migration 000039's exact valid sparse index: %w", err)
	}
	if _, err := tx.Exec(ctx, `SELECT set_config('app.tenant_id',$1,true)`, tenantID); err != nil {
		return CatalogSearchProjectionRepairResult{}, fmt.Errorf("set search projection repair tenant: %w", err)
	}
	var tenantExists bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM tenants WHERE id=$1)`, tenantID).Scan(&tenantExists); err != nil {
		return CatalogSearchProjectionRepairResult{}, fmt.Errorf("resolve search projection repair tenant: %w", err)
	}
	if !tenantExists {
		return CatalogSearchProjectionRepairResult{}, errors.New("search projection repair tenant was not found")
	}
	result, err := repairCatalogSearchProjectionsTx(ctx, tx, tenantID, limit, reason, operatorLabel)
	if err != nil {
		return CatalogSearchProjectionRepairResult{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return CatalogSearchProjectionRepairResult{}, fmt.Errorf("commit search projection repair: %w", err)
	}
	return result, nil
}

// repairCatalogSearchProjectionsTx applies validated, normalized input after the
// caller's owner, exact-index and tenant checks. Its progress is provisional
// until the caller commits; transaction setup and cleanup remain with the caller.
func repairCatalogSearchProjectionsTx(ctx context.Context, tx pgx.Tx, tenantID string, limit int, reason, operatorLabel string) (CatalogSearchProjectionRepairResult, error) {
	var beforeGeneration int64
	// A tenant with no discovery-affecting writes has no generation row yet.
	// Keep the existing row lock when present, without creating state for an
	// empty repair. Search readers use the same zero-generation convention.
	if err := tx.QueryRow(ctx, `SELECT coalesce((SELECT generation FROM catalog_search_generations WHERE tenant_id=$1 FOR UPDATE),0)`, tenantID).Scan(&beforeGeneration); err != nil {
		return CatalogSearchProjectionRepairResult{}, fmt.Errorf("read search generation before repair: %w", err)
	}
	rows, err := tx.Query(ctx, `
WITH targets AS (
  SELECT tenant_id,id
    FROM observations
   WHERE tenant_id=$1 AND normalized_entry_digest IS NOT NULL
     AND (search_display_name IS NULL OR search_description IS NULL OR search_tags IS NULL
       OR search_representative_queries IS NULL OR search_publisher IS NULL OR search_vector IS NULL OR search_vector_version<>2 OR search_filter_version<>1)
   ORDER BY id
   LIMIT $2
   FOR UPDATE SKIP LOCKED
)
UPDATE observations observation
   SET normalized_entry_bytes=observation.normalized_entry_bytes
  FROM targets
 WHERE observation.tenant_id=targets.tenant_id AND observation.id=targets.id
RETURNING observation.id::text`, tenantID, limit)
	if err != nil {
		return CatalogSearchProjectionRepairResult{}, fmt.Errorf("rebuild Catalog search projections: %w", err)
	}
	selectedIDs := make([]string, 0, limit)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return CatalogSearchProjectionRepairResult{}, fmt.Errorf("scan repaired Catalog search projection: %w", err)
		}
		selectedIDs = append(selectedIDs, id)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return CatalogSearchProjectionRepairResult{}, fmt.Errorf("iterate repaired Catalog search projections: %w", err)
	}
	rows.Close()
	sort.Strings(selectedIDs)
	var hasMore bool
	if err := tx.QueryRow(ctx, `
SELECT EXISTS (
  SELECT 1
    FROM observations
   WHERE tenant_id=$1 AND normalized_entry_digest IS NOT NULL
     AND (search_display_name IS NULL OR search_description IS NULL OR search_tags IS NULL
       OR search_representative_queries IS NULL OR search_publisher IS NULL OR search_vector IS NULL OR search_vector_version<>2 OR search_filter_version<>1)
   ORDER BY id
   LIMIT 1
)`, tenantID).Scan(&hasMore); err != nil {
		return CatalogSearchProjectionRepairResult{}, fmt.Errorf("inspect remaining Catalog search projections: %w", err)
	}
	var afterGeneration int64
	if err := tx.QueryRow(ctx, `SELECT coalesce((SELECT generation FROM catalog_search_generations WHERE tenant_id=$1),0)`, tenantID).Scan(&afterGeneration); err != nil {
		return CatalogSearchProjectionRepairResult{}, fmt.Errorf("read search generation after repair: %w", err)
	}
	var auditEventID string
	if len(selectedIDs) > 0 {
		var occurredAt time.Time
		if err := tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&occurredAt); err != nil {
			return CatalogSearchProjectionRepairResult{}, fmt.Errorf("read search projection repair occurrence time: %w", err)
		}
		facts, err := audit.Facts(searchProjectionRepairFacts{SelectedFirstID: selectedIDs[0], SelectedLastID: selectedIDs[len(selectedIDs)-1], Processed: int64(len(selectedIDs)), HasMore: hasMore, BeforeGeneration: beforeGeneration, AfterGeneration: afterGeneration})
		if err != nil {
			return CatalogSearchProjectionRepairResult{}, err
		}
		record, err := newDatabaseAuditRecord(ctx, tx, tenantID, audit.ActionSearchProjectionRebuilt, "catalog_search_projection", tenantID, reason, operatorLabel, facts, occurredAt)
		if err != nil {
			return CatalogSearchProjectionRepairResult{}, err
		}
		record.BeforeDigest = audit.DigestBytes([]byte(fmt.Sprintf("catalog-search-generation:%d", beforeGeneration)))
		record.AfterDigest = audit.DigestBytes([]byte(fmt.Sprintf("catalog-search-generation:%d", afterGeneration)))
		if err := insertAuditEvent(ctx, tx, record); err != nil {
			return CatalogSearchProjectionRepairResult{}, fmt.Errorf("insert search projection repair audit event: %w", err)
		}
		auditEventID = record.ID
	}
	return CatalogSearchProjectionRepairResult{TenantID: tenantID, Processed: int64(len(selectedIDs)), HasMore: hasMore, Complete: !hasMore, BeforeGeneration: beforeGeneration, AfterGeneration: afterGeneration, AuditEventID: auditEventID}, nil
}

// ValidateCatalogSearchProjectionRepairInput checks tenant scope, batch bounds and
// audit-safe reason/operator text without database access.
func ValidateCatalogSearchProjectionRepairInput(tenantID string, limit int, reason, operatorLabel string) error {
	if !uuidText(tenantID) || limit < 1 || limit > MaxCatalogSearchProjectionRepairBatch {
		return fmt.Errorf("search projection repair requires an explicit UUID tenant and a limit between 1 and %d", MaxCatalogSearchProjectionRepairBatch)
	}
	if _, err := audit.NormalizeReason(audit.ActionSearchProjectionRebuilt, reason); err != nil {
		return fmt.Errorf("search projection repair reason is invalid: %w", err)
	}
	operatorLabel = strings.TrimSpace(operatorLabel)
	if len(operatorLabel) > audit.MaxActorBytes || !utf8.ValidString(operatorLabel) {
		return errors.New("search projection repair operator label is invalid")
	}
	for _, character := range operatorLabel {
		if unicode.IsControl(character) {
			return errors.New("search projection repair operator label is invalid")
		}
	}
	return nil
}

// verifySearchProjectionRepairRole requires current-user ownership of observations
// before projection maintenance.
func verifySearchProjectionRepairRole(ctx context.Context, query interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}) error {
	var currentUser, tableOwner string
	if err := query.QueryRow(ctx, `
SELECT current_user,pg_get_userbyid(class.relowner)
  FROM pg_catalog.pg_class class
 WHERE class.oid=to_regclass('observations')`).Scan(&currentUser, &tableOwner); err != nil {
		return fmt.Errorf("inspect search projection repair database authority: %w", err)
	}
	if currentUser != tableOwner {
		return fmt.Errorf("search projection repair requires the operator-controlled schema owner; current role %q does not own the observations table", currentUser)
	}
	return nil
}
