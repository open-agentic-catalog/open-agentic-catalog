// Publication stores bind to one host and keep retained document metadata
// consistent with candidate, active and retired row state.

package database

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/audit"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/publication"
)

const publicationBundleFamily = "catalog_bundle"

// PostgresPublicationStore persists complete, immutable publication generations.
type PostgresPublicationStore struct {
	repository *PostgresRepository
	host       string
}

// NewPostgresPublicationStore creates a durable publication store for one public host.
func NewPostgresPublicationStore(repository *PostgresRepository, host string) (*PostgresPublicationStore, error) {
	if repository == nil || host == "" {
		return nil, errors.New("PostgreSQL repository and publication host are required")
	}
	return &PostgresPublicationStore{repository: repository, host: host}, nil
}

// NextGeneration reads the next host-scoped generation number without reserving
// it; concurrent construction is still subject to the database uniqueness guard.
func (p *PostgresPublicationStore) NextGeneration(ctx context.Context, tenantID, host string) (int64, error) {
	if host != p.host {
		return 0, catalog.E(catalog.CodePublicationNotReady, "publication host does not match its configured store")
	}
	return withTenantRead(ctx, p.repository.pool, tenantID, func(tx pgx.Tx) (int64, error) {
		var generation int64
		if err := tx.QueryRow(ctx, `
      SELECT coalesce(max(generation),0)+1 FROM publication_snapshots
       WHERE tenant_id=$1 AND host=$2 AND document_family=$3`, tenantID, p.host, publicationBundleFamily).Scan(&generation); err != nil {
			return 0, translateError("read next publication generation", err)
		}
		return generation, nil
	})
}

// SaveCandidate rejects the wrong host, noncandidate state and oversized content,
// then inserts the snapshot and supplied mutation evidence atomically. Creation
// time is normalized to PostgreSQL precision before encoding the retained document.
func (p *PostgresPublicationStore) SaveCandidate(ctx context.Context, snapshot publication.Snapshot, evidence ...publication.MutationEvidence) (publication.Snapshot, error) {
	if snapshot.Host != p.host || snapshot.State != publication.SnapshotCandidate {
		return publication.Snapshot{}, catalog.E(catalog.CodePublicationNotReady, "publication candidate does not match its configured host or state")
	}
	if err := publication.ValidateSnapshotBounds(snapshot); err != nil {
		return publication.Snapshot{}, err
	}
	snapshot.CreatedAt = snapshot.CreatedAt.UTC().Truncate(time.Microsecond)
	return withTenantTx(ctx, p.repository.pool, snapshot.TenantID, pgx.TxOptions{}, func(tx pgx.Tx) (publication.Snapshot, error) {
		document, err := json.Marshal(snapshot)
		if err != nil {
			return publication.Snapshot{}, catalog.Wrap(catalog.CodeInternal, "encode publication snapshot", err)
		}
		_, err = tx.Exec(ctx, `
      INSERT INTO publication_snapshots(id,tenant_id,host,document_family,profile_id,generation,state,etag,document,created_at,activated_at)
      VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`,
			snapshot.ID, snapshot.TenantID, p.host, publicationBundleFamily, snapshot.ARDProfile+"+"+snapshot.AICatalogProfile,
			snapshot.Generation, snapshot.State, snapshot.ETag, document, snapshot.CreatedAt, snapshot.ActivatedAt)
		if err != nil {
			return publication.Snapshot{}, translateError("save publication candidate", err)
		}
		if err := insertPublicationEvidence(ctx, tx, snapshot.TenantID, snapshot.ID, evidence, audit.ActionPublicationCandidateCreated); err != nil {
			return publication.Snapshot{}, err
		}
		return snapshot, nil
	})
}

// Activate serializes tenant publication changes, rechecks active identity access
// and the expected current snapshot, then retires the prior snapshot and activates
// the candidate with its evidence in one transaction.
func (p *PostgresPublicationStore) Activate(ctx context.Context, tenantID, snapshotID string, at time.Time, evidence ...publication.MutationEvidence) (publication.Snapshot, error) {
	at = at.UTC().Truncate(time.Microsecond)
	return withTenantTx(ctx, p.repository.pool, tenantID, pgx.TxOptions{IsoLevel: pgx.Serializable}, func(tx pgx.Tx) (publication.Snapshot, error) {
		if err := lockTenantPublicationActivation(ctx, tx, tenantID); err != nil {
			return publication.Snapshot{}, err
		}
		if err := checkExpectedActivePublication(ctx, tx, tenantID, p.host, evidence); err != nil {
			return publication.Snapshot{}, err
		}
		candidate, err := scanPublicationSnapshot(tx.QueryRow(ctx, `
      SELECT id::text,tenant_id::text,host,profile_id,generation,etag,document,state,created_at,activated_at FROM publication_snapshots
      WHERE tenant_id=$1 AND id=$2 AND host=$3 AND document_family=$4 FOR UPDATE`, tenantID, snapshotID, p.host, publicationBundleFamily))
		if err != nil {
			return publication.Snapshot{}, err
		}
		if candidate.State != publication.SnapshotCandidate {
			return publication.Snapshot{}, catalog.E(catalog.CodeInvalidTransition, "only a candidate snapshot can be activated")
		}
		if err := checkPublicationRatingConsent(ctx, tx, candidate); err != nil {
			return publication.Snapshot{}, err
		}
		if _, err := tx.Exec(ctx, `
      UPDATE publication_snapshots SET state=$4,document=jsonb_set(document,'{state}',to_jsonb($4::text),false)
      WHERE tenant_id=$1 AND host=$2 AND document_family=$3 AND state=$5`,
			tenantID, p.host, publicationBundleFamily, publication.SnapshotRetired, publication.SnapshotActive); err != nil {
			return publication.Snapshot{}, translateError("retire publication snapshot", err)
		}
		candidate.State = publication.SnapshotActive
		candidate.ActivatedAt = &at
		document, err := json.Marshal(candidate)
		if err != nil {
			return publication.Snapshot{}, catalog.Wrap(catalog.CodeInternal, "encode active publication snapshot", err)
		}
		if _, err := tx.Exec(ctx, `UPDATE publication_snapshots SET state=$2,activated_at=$3,document=$4 WHERE id=$1 AND tenant_id=$5 AND host=$6`, snapshotID, candidate.State, at, document, tenantID, p.host); err != nil {
			return publication.Snapshot{}, translateError("activate publication snapshot", err)
		}
		if err := insertPublicationEvidence(ctx, tx, tenantID, snapshotID, evidence, audit.ActionPublicationActivated); err != nil {
			return publication.Snapshot{}, err
		}
		return candidate, nil
	})
}

// Deactivate retires only the specified active snapshot after checking the
// expected pointer. It preserves activation time and records unpublication evidence.
func (p *PostgresPublicationStore) Deactivate(ctx context.Context, tenantID, snapshotID string, _ time.Time, evidence ...publication.MutationEvidence) (publication.Snapshot, error) {
	return withTenantTx(ctx, p.repository.pool, tenantID, pgx.TxOptions{IsoLevel: pgx.Serializable}, func(tx pgx.Tx) (publication.Snapshot, error) {
		if err := lockTenantState(ctx, tx, tenantID, "lock tenant publication state"); err != nil {
			return publication.Snapshot{}, err
		}
		if err := checkExpectedActivePublication(ctx, tx, tenantID, p.host, evidence); err != nil {
			return publication.Snapshot{}, err
		}
		active, err := scanPublicationSnapshot(tx.QueryRow(ctx, `
      SELECT id::text,tenant_id::text,host,profile_id,generation,etag,document,state,created_at,activated_at FROM publication_snapshots
      WHERE tenant_id=$1 AND host=$2 AND document_family=$3 AND state=$4 AND id=$5 FOR UPDATE`, tenantID, p.host, publicationBundleFamily, publication.SnapshotActive, snapshotID))
		if err != nil {
			return publication.Snapshot{}, err
		}
		active.State = publication.SnapshotRetired
		document, err := json.Marshal(active)
		if err != nil {
			return publication.Snapshot{}, catalog.Wrap(catalog.CodeInternal, "encode retired publication snapshot", err)
		}
		if _, err := tx.Exec(ctx, `UPDATE publication_snapshots SET state=$2,document=$3 WHERE id=$1 AND tenant_id=$4 AND host=$5`, active.ID, active.State, document, tenantID, p.host); err != nil {
			return publication.Snapshot{}, translateError("unpublish snapshot", err)
		}
		if err := insertPublicationEvidence(ctx, tx, tenantID, snapshotID, evidence, audit.ActionPublicationUnpublished); err != nil {
			return publication.Snapshot{}, err
		}
		return active, nil
	})
}

// Rollback restores a retired host-scoped snapshot with a new activation time,
// retiring any active generation atomically. Suspended tenant identity access
// and a changed expected pointer prevent the transition.
func (p *PostgresPublicationStore) Rollback(ctx context.Context, tenantID, snapshotID string, at time.Time, evidence ...publication.MutationEvidence) (publication.Snapshot, error) {
	at = at.UTC().Truncate(time.Microsecond)
	return withTenantTx(ctx, p.repository.pool, tenantID, pgx.TxOptions{IsoLevel: pgx.Serializable}, func(tx pgx.Tx) (publication.Snapshot, error) {
		if err := lockTenantPublicationActivation(ctx, tx, tenantID); err != nil {
			return publication.Snapshot{}, err
		}
		if err := checkExpectedActivePublication(ctx, tx, tenantID, p.host, evidence); err != nil {
			return publication.Snapshot{}, err
		}
		target, err := scanPublicationSnapshot(tx.QueryRow(ctx, `
      SELECT id::text,tenant_id::text,host,profile_id,generation,etag,document,state,created_at,activated_at FROM publication_snapshots
      WHERE tenant_id=$1 AND id=$2 AND host=$3 AND document_family=$4 FOR UPDATE`, tenantID, snapshotID, p.host, publicationBundleFamily))
		if err != nil {
			return publication.Snapshot{}, err
		}
		if target.State != publication.SnapshotRetired {
			return publication.Snapshot{}, catalog.E(catalog.CodeInvalidTransition, "only a retired snapshot can be restored")
		}
		if err := checkPublicationRatingConsent(ctx, tx, target); err != nil {
			return publication.Snapshot{}, err
		}
		if _, err := tx.Exec(ctx, `
      UPDATE publication_snapshots SET state=$4,document=jsonb_set(document,'{state}',to_jsonb($4::text),false)
      WHERE tenant_id=$1 AND host=$2 AND document_family=$3 AND state=$5`,
			tenantID, p.host, publicationBundleFamily, publication.SnapshotRetired, publication.SnapshotActive); err != nil {
			return publication.Snapshot{}, translateError("retire active publication snapshot", err)
		}
		target.State = publication.SnapshotActive
		target.ActivatedAt = &at
		document, err := json.Marshal(target)
		if err != nil {
			return publication.Snapshot{}, catalog.Wrap(catalog.CodeInternal, "encode restored publication snapshot", err)
		}
		if _, err := tx.Exec(ctx, `UPDATE publication_snapshots SET state=$2,activated_at=$3,document=$4 WHERE id=$1 AND tenant_id=$5 AND host=$6`, target.ID, target.State, at, document, tenantID, p.host); err != nil {
			return publication.Snapshot{}, translateError("restore publication snapshot", err)
		}
		if err := insertPublicationEvidence(ctx, tx, tenantID, snapshotID, evidence, audit.ActionPublicationRolledBack); err != nil {
			return publication.Snapshot{}, err
		}
		return target, nil
	})
}

// insertPublicationEvidence validates action/target binding before appending
// supplied audit and optional outbox records to the caller's transaction.
func insertPublicationEvidence(ctx context.Context, tx pgx.Tx, tenantID, targetID string, values []publication.MutationEvidence, expectedActions ...string) error {
	if err := publication.ValidateMutationEvidence(tenantID, targetID, values, expectedActions...); err != nil {
		return err
	}
	if len(values) == 0 || values[0].Audit.ID == "" {
		return nil
	}
	if err := insertAuditEvent(ctx, tx, values[0].Audit); err != nil {
		return err
	}
	if values[0].Outbox != nil {
		return insertOutboxEvent(ctx, tx, *values[0].Outbox)
	}
	return nil
}

// checkExpectedActivePublication enforces an optional active-pointer precondition;
// an empty expected identifier explicitly requires that no snapshot is active.
func checkExpectedActivePublication(ctx context.Context, tx pgx.Tx, tenantID, host string, values []publication.MutationEvidence) error {
	if len(values) == 0 || values[0].ExpectedActiveID == nil {
		return nil
	}
	var current string
	err := tx.QueryRow(ctx, `SELECT id::text FROM publication_snapshots WHERE tenant_id=$1 AND host=$2 AND document_family=$3 AND state=$4`, tenantID, host, publicationBundleFamily, publication.SnapshotActive).Scan(&current)
	if errors.Is(err, pgx.ErrNoRows) {
		current = ""
	} else if err != nil {
		return translateError("read active publication pointer", err)
	}
	if current != *values[0].ExpectedActiveID {
		return catalog.E(catalog.CodePreconditionFailed, "active publication changed during the command")
	}
	return nil
}

// lockTenantPublicationActivation locks tenant state before checking that an
// active identity realm still permits publication activation or restoration.
func lockTenantPublicationActivation(ctx context.Context, tx pgx.Tx, tenantID string) error {
	if err := lockTenantState(ctx, tx, tenantID, "lock tenant publication state"); err != nil {
		return err
	}
	var activeRealm bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM tenant_identity_realms WHERE tenant_id=$1 AND state='active')`, tenantID).Scan(&activeRealm); err != nil {
		return translateError("check publication identity realm", err)
	}
	if !activeRealm {
		return catalog.E(catalog.CodePublicationNotReady, "publication cannot activate while tenant identity access is suspended")
	}
	return nil
}

// Get loads and validates one retained snapshot within this store's tenant/host boundary.
func (p *PostgresPublicationStore) Get(ctx context.Context, tenantID, snapshotID string) (publication.Snapshot, error) {
	return withTenantRead(ctx, p.repository.pool, tenantID, func(tx pgx.Tx) (publication.Snapshot, error) {
		return scanPublicationSnapshot(tx.QueryRow(ctx, `
      SELECT id::text,tenant_id::text,host,profile_id,generation,etag,document,state,created_at,activated_at FROM publication_snapshots
      WHERE tenant_id=$1 AND id=$2 AND host=$3 AND document_family=$4`, tenantID, snapshotID, p.host, publicationBundleFamily))
	})
}

// Active returns the host's validated active snapshot or CodeNotFound. Absence
// is translated after the read transaction so a normal withdrawal no-op does
// not mark an enclosing atomic command rollback-only.
func (p *PostgresPublicationStore) Active(ctx context.Context, tenantID string) (publication.Snapshot, error) {
	type lookup struct {
		snapshot publication.Snapshot
		missing  bool
	}
	result, err := withTenantRead(ctx, p.repository.pool, tenantID, func(tx pgx.Tx) (lookup, error) {
		snapshot, err := scanPublicationSnapshot(tx.QueryRow(ctx, `
      SELECT id::text,tenant_id::text,host,profile_id,generation,etag,document,state,created_at,activated_at FROM publication_snapshots
      WHERE tenant_id=$1 AND host=$2 AND document_family=$3 AND state=$4`, tenantID, p.host, publicationBundleFamily, publication.SnapshotActive))
		// No active snapshot is a normal withdrawal no-op, not a failed
		// transaction. Translate absence outside the shared command boundary
		// so a second release/domain withdrawal cannot roll back its mutation.
		if catalog.ErrorCodeOf(err) == catalog.CodeNotFound {
			return lookup{missing: true}, nil
		}
		return lookup{snapshot: snapshot}, err
	})
	if err != nil {
		return publication.Snapshot{}, err
	}
	if result.missing {
		return publication.Snapshot{}, catalog.E(catalog.CodeNotFound, "active publication snapshot was not found")
	}
	return result.snapshot, nil
}

// ListSummaries selects a SQL-bounded page ordered by descending generation and
// identifier, projecting metadata without decoding every full snapshot document.
func (p *PostgresPublicationStore) ListSummaries(ctx context.Context, tenantID string, request publication.SnapshotListRequest) ([]publication.SnapshotSummary, error) {
	return withTenantRead(ctx, p.repository.pool, tenantID, func(tx pgx.Tx) ([]publication.SnapshotSummary, error) {
		rows, err := tx.Query(ctx, `
      SELECT id::text,tenant_id::text,host,coalesce(document->>'audience',''),generation,
             coalesce(document->>'ardProfile',''),coalesce(document->>'aiCatalogProfile',''),state,etag,created_at,activated_at
        FROM publication_snapshots
       WHERE tenant_id=$1 AND host=$2 AND document_family=$3
         AND ($4::bigint=0 OR generation<$4 OR (generation=$4 AND id<nullif($5,'')::uuid))
         AND ($7::text='' OR state=$7)
       ORDER BY generation DESC,id DESC LIMIT $6`, tenantID, p.host, publicationBundleFamily, request.AfterGeneration, request.AfterID, request.Limit, request.State)
		if err != nil {
			return nil, translateError("list publication snapshot summaries", err)
		}
		defer rows.Close()
		items := make([]publication.SnapshotSummary, 0)
		for rows.Next() {
			var item publication.SnapshotSummary
			if err := rows.Scan(&item.ID, &item.TenantID, &item.Host, &item.Audience, &item.Generation, &item.ARDProfile, &item.AICatalogProfile, &item.State, &item.ETag, &item.CreatedAt, &item.ActivatedAt); err != nil {
				return nil, translateError("scan publication snapshot summary", err)
			}
			items = append(items, item)
		}
		return items, translateError("iterate publication snapshot summaries", rows.Err())
	})
}

// scanPublicationSnapshot rejects oversized retained data and disagreement
// between row identity/lifecycle metadata and the immutable snapshot document.
func scanPublicationSnapshot(row scanner) (publication.Snapshot, error) {
	var idValue, tenantID, host, profileID, etag string
	var generation int64
	var raw []byte
	var state publication.SnapshotState
	var createdAt time.Time
	var activatedAt *time.Time
	if err := row.Scan(&idValue, &tenantID, &host, &profileID, &generation, &etag, &raw, &state, &createdAt, &activatedAt); err != nil {
		return publication.Snapshot{}, translateError("read publication snapshot", err)
	}
	if err := publication.ValidateEncodedSnapshotBounds(raw); err != nil {
		return publication.Snapshot{}, err
	}
	var snapshot publication.Snapshot
	if err := json.Unmarshal(raw, &snapshot); err != nil {
		return publication.Snapshot{}, catalog.Wrap(catalog.CodePublicationNotReady, "decode publication snapshot", err)
	}
	if snapshot.ID != idValue || snapshot.TenantID != tenantID || snapshot.Host != host || snapshot.ARDProfile+"+"+snapshot.AICatalogProfile != profileID || snapshot.Generation != generation || snapshot.ETag != etag || snapshot.State != state || !samePostgresTime(snapshot.CreatedAt, createdAt) || !sameOptionalPostgresTime(snapshot.ActivatedAt, activatedAt) {
		return publication.Snapshot{}, catalog.E(catalog.CodePublicationNotReady, "publication snapshot row and immutable document metadata are inconsistent")
	}
	if err := publication.ValidateSnapshotBounds(snapshot); err != nil {
		return publication.Snapshot{}, err
	}
	return snapshot, nil
}

// samePostgresTime compares instants at PostgreSQL's microsecond precision.
func samePostgresTime(left, right time.Time) bool {
	return left.UTC().Truncate(time.Microsecond).Equal(right.UTC().Truncate(time.Microsecond))
}

// sameOptionalPostgresTime preserves absence while comparing present instants
// using the database's timestamp precision.
func sameOptionalPostgresTime(left, right *time.Time) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return samePostgresTime(*left, *right)
}
