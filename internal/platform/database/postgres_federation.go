// Federation persistence retains source revisions, inspections and observations
// while preserving complete snapshots across partial synchronization.

package database

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/audit"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/federation"
)

// PostgresFederationStore persists source configuration, observations, and immutable sync evidence.
type PostgresFederationStore struct{ repository *PostgresRepository }

// NewPostgresFederationStore creates a durable federation store.
func NewPostgresFederationStore(repository *PostgresRepository) (*PostgresFederationStore, error) {
	if repository == nil {
		return nil, errors.New("PostgreSQL repository is required")
	}
	return &PostgresFederationStore{repository: repository}, nil
}

// SaveInspection retains the inspected document and expiry with its
// source-inspected evidence in one tenant transaction.
func (p *PostgresFederationStore) SaveInspection(ctx context.Context, inspection federation.Inspection, evidence ...federation.MutationEvidence) (federation.Inspection, error) {
	return withTenantTx(ctx, p.repository.pool, inspection.TenantID, pgx.TxOptions{}, func(tx pgx.Tx) (federation.Inspection, error) {
		document, err := json.Marshal(inspection)
		if err != nil {
			return federation.Inspection{}, catalog.Wrap(catalog.CodeInternal, "encode source inspection", err)
		}
		if _, err := tx.Exec(ctx, `INSERT INTO source_inspections(id,tenant_id,document,expires_at,created_at) VALUES ($1,$2,$3,$4,$5)`, inspection.ID, inspection.TenantID, document, inspection.ExpiresAt, inspection.CreatedAt); err != nil {
			return federation.Inspection{}, translateError("save source inspection", err)
		}
		if err := insertFederationEvidence(ctx, tx, inspection.TenantID, inspection.ID, evidence, audit.ActionSourceInspected); err != nil {
			return federation.Inspection{}, err
		}
		return inspection, nil
	})
}

// GetInspection loads an exact tenant inspection document; its caller remains
// responsible for expiry and applicability checks.
func (p *PostgresFederationStore) GetInspection(ctx context.Context, tenantID, idValue string) (federation.Inspection, error) {
	return withTenantRead(ctx, p.repository.pool, tenantID, func(tx pgx.Tx) (federation.Inspection, error) {
		var raw []byte
		if err := tx.QueryRow(ctx, `SELECT document FROM source_inspections WHERE tenant_id=$1 AND id=$2`, tenantID, idValue).Scan(&raw); err != nil {
			return federation.Inspection{}, translateError("read source inspection", err)
		}
		var inspection federation.Inspection
		if err := json.Unmarshal(raw, &inspection); err != nil {
			return federation.Inspection{}, catalog.Wrap(catalog.CodeInternal, "decode source inspection", err)
		}
		return inspection, nil
	})
}

// SaveChangePreview persists a digest-addressed consequence preview and its
// optional audit evidence atomically.
func (p *PostgresFederationStore) SaveChangePreview(ctx context.Context, preview federation.ChangePreview, evidence ...federation.MutationEvidence) (federation.ChangePreview, error) {
	return withTenantTx(ctx, p.repository.pool, preview.TenantID, pgx.TxOptions{}, func(tx pgx.Tx) (federation.ChangePreview, error) {
		document, err := json.Marshal(preview)
		if err != nil {
			return federation.ChangePreview{}, catalog.Wrap(catalog.CodeInternal, "encode source change preview", err)
		}
		if _, err := tx.Exec(ctx, `
      INSERT INTO source_change_previews(id,tenant_id,source_id,digest,document,expires_at,created_at)
      VALUES ($1,$2,$3,$4,$5,$6,$7)`, preview.ID, preview.TenantID, preview.SourceID, preview.Digest, document, preview.ExpiresAt, preview.CreatedAt); err != nil {
			return federation.ChangePreview{}, translateError("save source change preview", err)
		}
		if err := insertFederationEvidence(ctx, tx, preview.TenantID, preview.ID, evidence, audit.ActionSourceChangePreviewCreated); err != nil {
			return federation.ChangePreview{}, err
		}
		return preview, nil
	})
}

// GetChangePreview resolves an exact tenant/digest preview without treating
// retrieval as proof that the preview remains current.
func (p *PostgresFederationStore) GetChangePreview(ctx context.Context, tenantID, digest string) (federation.ChangePreview, error) {
	return withTenantRead(ctx, p.repository.pool, tenantID, func(tx pgx.Tx) (federation.ChangePreview, error) {
		var raw []byte
		if err := tx.QueryRow(ctx, `SELECT document FROM source_change_previews WHERE tenant_id=$1 AND digest=$2`, tenantID, digest).Scan(&raw); err != nil {
			return federation.ChangePreview{}, translateError("read source change preview", err)
		}
		var preview federation.ChangePreview
		if err := json.Unmarshal(raw, &preview); err != nil {
			return federation.ChangePreview{}, catalog.Wrap(catalog.CodeInternal, "decode source change preview", err)
		}
		return preview, nil
	})
}

// CreateSource clears caller-supplied synchronization state, locks the referenced
// current inspection and inserts the source, immutable revision and evidence
// atomically.
func (p *PostgresFederationStore) CreateSource(ctx context.Context, source federation.Source, evidence ...federation.MutationEvidence) (federation.Source, error) {
	source.LastCompleteSnapshotID = ""
	source.LastSyncAt = nil
	source.LastError = ""
	return withTenantTx(ctx, p.repository.pool, source.TenantID, pgx.TxOptions{}, func(tx pgx.Tx) (federation.Source, error) {
		if err := lockCurrentSourceInspection(ctx, tx, source.TenantID, source.InspectionID); err != nil {
			return federation.Source{}, err
		}
		configuration, err := marshalFederationSourceConfiguration(source)
		if err != nil {
			return federation.Source{}, catalog.Wrap(catalog.CodeInternal, "encode federation source", err)
		}
		_, err = tx.Exec(ctx, `
			INSERT INTO federation_sources(id,tenant_id,display_name,source_type,endpoint,status,revision,configuration,created_by,created_at,updated_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`,
			source.ID, source.TenantID, source.DisplayName, source.Type, source.Endpoint, source.Status, source.Revision, configuration,
			source.CreatedBy, source.CreatedAt, source.UpdatedAt)
		if err != nil {
			return federation.Source{}, translateError("create federation source", err)
		}
		if err := saveFederationSourceRevision(ctx, tx, source, configuration); err != nil {
			return federation.Source{}, err
		}
		if err := insertFederationEvidence(ctx, tx, source.TenantID, source.ID, evidence, audit.ActionSourceConnected); err != nil {
			return federation.Source{}, err
		}
		return source, nil
	})
}

// UpdateSource requires the next revision and any expected observation set,
// preserves synchronization state and appends revision evidence; pre-write
// revision conflicts leave room for safe test-attempt auditing.
func (p *PostgresFederationStore) UpdateSource(ctx context.Context, source federation.Source, expectedRevision int64, evidence ...federation.MutationEvidence) (federation.Source, error) {
	var deferredPrecondition error
	updated, err := withTenantTx(ctx, p.repository.pool, source.TenantID, pgx.TxOptions{}, func(tx pgx.Tx) (federation.Source, error) {
		current, err := readFederationSource(ctx, tx, source.TenantID, source.ID, true)
		if err != nil {
			return federation.Source{}, err
		}
		source.PresentationGeneration = federation.NextPresentationGeneration(current, source)
		if expectedRevision > 0 && current.Revision != expectedRevision {
			deferredPrecondition = catalog.E(catalog.CodePreconditionFailed, "source revision does not match")
			return federation.Source{}, nil
		}
		if source.Revision != current.Revision+1 {
			deferredPrecondition = catalog.E(catalog.CodePreconditionFailed, "source update must create the next immutable revision")
			return federation.Source{}, nil
		}
		if err := lockCurrentSourceInspection(ctx, tx, source.TenantID, source.InspectionID); err != nil {
			return federation.Source{}, err
		}
		if expected := federationExpectedObservationSet(evidence); expected != nil {
			actual, overflow, setErr := currentObservationSetTx(ctx, tx, source.TenantID, source.ID, federation.MaxSynchronizationEntries)
			if setErr != nil {
				return federation.Source{}, setErr
			}
			if overflow {
				return federation.Source{}, catalog.E(catalog.CodeWorkLimitExceeded, "current source observations exceed the supported update limit")
			}
			if actual != *expected {
				return federation.Source{}, catalog.E(catalog.CodePreconditionFailed, "source observations changed after the consequence preview")
			}
		}
		source.LastCompleteSnapshotID = current.LastCompleteSnapshotID
		source.LastSyncAt = current.LastSyncAt
		source.LastError = current.LastError
		configuration, err := marshalFederationSourceConfiguration(source)
		if err != nil {
			return federation.Source{}, catalog.Wrap(catalog.CodeInternal, "encode federation source", err)
		}
		_, err = tx.Exec(ctx, `
			UPDATE federation_sources
			   SET display_name=$3,source_type=$4,endpoint=$5,status=$6,revision=$7,configuration=$8,updated_at=$9
			 WHERE tenant_id=$1 AND id=$2`, source.TenantID, source.ID, source.DisplayName, source.Type, source.Endpoint,
			source.Status, source.Revision, configuration, source.UpdatedAt)
		if err != nil {
			return federation.Source{}, translateError("update federation source", err)
		}
		if err := saveFederationSourceRevision(ctx, tx, source, configuration); err != nil {
			return federation.Source{}, err
		}
		if source.Status == federation.SourceRemoved {
			if _, err := tx.Exec(ctx, `UPDATE observations SET freshness=$3 WHERE tenant_id=$1 AND source_id=$2 AND freshness <> $3`, source.TenantID, source.ID, federation.FreshSourceRemoved); err != nil {
				return federation.Source{}, translateError("retire removed source observations", err)
			}
		}
		if err := insertFederationEvidence(ctx, tx, source.TenantID, source.ID, evidence, audit.ActionSourceRevisionActivated, audit.ActionSourceTested, audit.ActionSourcePaused, audit.ActionSourceResumed, audit.ActionSourceRemoved); err != nil {
			return federation.Source{}, err
		}
		return source, nil
	})
	if err != nil {
		return federation.Source{}, err
	}
	// A source test can discover a concurrent revision change only after its
	// outbound request. Keep this no-write precondition from poisoning the
	// ambient command transaction so the service can append the required safe
	// attempt evidence before retaining the exact failure response. Ordinary
	// callers still receive the same error and abort the transaction normally.
	if deferredPrecondition != nil {
		return federation.Source{}, deferredPrecondition
	}
	return updated, nil
}

// CurrentObservationSet returns the count/digest of the bounded current source
// set; overflow returns no partial set and a true boolean.
func (p *PostgresFederationStore) CurrentObservationSet(ctx context.Context, tenantID, sourceID string, limit int) (federation.ObservationSet, bool, error) {
	type result struct {
		set      federation.ObservationSet
		overflow bool
	}
	value, err := withTenantRead(ctx, p.repository.pool, tenantID, func(tx pgx.Tx) (result, error) {
		set, overflow, readErr := currentObservationSetTx(ctx, tx, tenantID, sourceID, limit)
		return result{set: set, overflow: overflow}, readErr
	})
	return value.set, value.overflow, err
}

// currentObservationSetTx uses limit plus one SQL selection and hashes ordered
// observation identities/digests only when the complete set fits.
func currentObservationSetTx(ctx context.Context, tx pgx.Tx, tenantID, sourceID string, limit int) (federation.ObservationSet, bool, error) {
	if limit < 1 {
		return federation.ObservationSet{}, false, catalog.E(catalog.CodeInvalidArgument, "current observation-set limit must be positive")
	}
	var result federation.ObservationSet
	var overflow bool
	err := tx.QueryRow(ctx, `
WITH bounded AS (
  SELECT id,exact_entry_digest
    FROM observations
   WHERE tenant_id=$1 AND source_id=$2 AND freshness=$3
   ORDER BY id LIMIT $4
)
SELECT count(*),count(*)>$5,
       CASE WHEN count(*)>$5 THEN ''
            ELSE 'sha256:' || encode(digest(coalesce(string_agg(id::text || ':' || exact_entry_digest,E'\n' ORDER BY id),''),'sha256'),'hex') END
  FROM bounded`, tenantID, sourceID, federation.FreshCurrent, limit+1, limit).Scan(&result.Count, &overflow, &result.Digest)
	if err != nil {
		return federation.ObservationSet{}, false, translateError("summarize bounded current source observations", err)
	}
	if overflow {
		return federation.ObservationSet{}, true, nil
	}
	return result, false, nil
}

// marshalFederationSourceConfiguration excludes mutable synchronization outcome
// fields from retained source configuration.
func marshalFederationSourceConfiguration(source federation.Source) ([]byte, error) {
	source.LastCompleteSnapshotID = ""
	source.LastSyncAt = nil
	source.LastError = ""
	return json.Marshal(source)
}

// saveFederationSourceRevision inserts immutable configuration with
// policy/credential evidence and its exact same-revision audience and referral
// assignments.
func saveFederationSourceRevision(ctx context.Context, tx pgx.Tx, source federation.Source, configuration []byte) error {
	retrievalMode := source.RetrievalMode
	if retrievalMode == "" {
		switch {
		case source.Type == federation.SourceRegistry:
			retrievalMode = federation.RetrievalLive
		case source.Live.Enabled:
			retrievalMode = federation.RetrievalHybrid
		default:
			retrievalMode = federation.RetrievalIndexed
		}
	}
	updateMode := source.UpdateMode
	if updateMode == "" {
		updateMode = federation.UpdateManual
	}
	recordedAt := source.UpdatedAt
	if recordedAt.IsZero() {
		recordedAt = source.CreatedAt
	}
	_, err := tx.Exec(ctx, `
    INSERT INTO federation_source_revisions(
      tenant_id,source_id,revision,retrieval_mode,update_mode,registry_list_url,policy_id,policy_revision,policy_digest,credential_revision,
      configuration_digest,configuration,recorded_at,inspection_id
    ) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,'sha256:' || encode(digest($11::jsonb::text,'sha256'),'hex'),$11,$12,nullif($13,'')::uuid)`,
		source.TenantID, source.ID, source.Revision, retrievalMode, updateMode, source.RegistryListURL, source.PolicyID, source.PolicyRevision,
		source.PolicyDigest, source.CredentialRevision, configuration, recordedAt, source.InspectionID)
	if err != nil {
		return translateError("save immutable federation source revision", err)
	}
	for _, assignment := range source.DiscoveryAssignments {
		if _, err := tx.Exec(ctx, `
      INSERT INTO federation_source_audiences(tenant_id,source_id,source_revision,discovery_host,audience)
      VALUES ($1,$2,$3,$4,$5)`, source.TenantID, source.ID, source.Revision, assignment.Host, assignment.Audience); err != nil {
			return translateError("save federation source audience", err)
		}
	}
	for _, targetSourceID := range source.Referrals.ApprovedSourceIDs {
		if _, err := tx.Exec(ctx, `
      INSERT INTO federation_source_referrals(tenant_id,source_id,source_revision,target_source_id)
      VALUES ($1,$2,$3,$4)`, source.TenantID, source.ID, source.Revision, targetSourceID); err != nil {
			return translateError("save federation source referral", err)
		}
	}
	return nil
}

// lockCurrentSourceInspection protects a supplied inspection from cleanup and
// requires unexpired evidence using the database clock.
func lockCurrentSourceInspection(ctx context.Context, tx pgx.Tx, tenantID, inspectionID string) error {
	if inspectionID == "" {
		return nil
	}
	var current bool
	err := tx.QueryRow(ctx, `
    SELECT expires_at>clock_timestamp()
      FROM source_inspections
     WHERE tenant_id=$1 AND id=$2
     FOR KEY SHARE`, tenantID, inspectionID).Scan(&current)
	if errors.Is(err, pgx.ErrNoRows) || err == nil && !current {
		return catalog.E(catalog.CodePreconditionFailed, "source inspection is missing or expired")
	}
	if err != nil {
		return translateError("lock source inspection use", err)
	}
	return nil
}

// GetSource hydrates a validated current source revision and treats
// administratively removed sources as absent.
func (p *PostgresFederationStore) GetSource(ctx context.Context, tenantID, idValue string) (federation.Source, error) {
	return withTenantRead(ctx, p.repository.pool, tenantID, func(tx pgx.Tx) (federation.Source, error) {
		source, err := readFederationSource(ctx, tx, tenantID, idValue, false)
		if err == nil && source.Status == federation.SourceRemoved {
			return federation.Source{}, catalog.E(catalog.CodeNotFound, "federation source was not found")
		}
		return source, err
	})
}

// SourceEndpointConnected checks whether another nonremoved tenant source's
// current revision already claims the exact Registry Search URL.
func (p *PostgresFederationStore) SourceEndpointConnected(ctx context.Context, tenantID, exceptSourceID, endpoint string) (bool, error) {
	return withTenantRead(ctx, p.repository.pool, tenantID, func(tx pgx.Tx) (bool, error) {
		var connected bool
		if err := tx.QueryRow(ctx, `
      SELECT EXISTS(
        SELECT 1 FROM federation_sources source
        JOIN federation_source_revisions revision
          ON revision.tenant_id=source.tenant_id AND revision.source_id=source.id AND revision.revision=source.revision
        WHERE source.tenant_id=$1 AND ($2='' OR source.id<>$2::uuid) AND source.status<>$3
          AND coalesce(revision.configuration->>'registrySearchUrl','')=$4
      )`, tenantID, exceptSourceID, federation.SourceRemoved, endpoint).Scan(&connected); err != nil {
			return false, translateError("check connected federation endpoint", err)
		}
		return connected, nil
	})
}

// ListSourcePage reads a tenant-scoped administrative page using current immutable
// source configuration and cursor bounds.
func (p *PostgresFederationStore) ListSourcePage(ctx context.Context, tenantID string, request federation.SourceListRequest) ([]federation.Source, error) {
	return withTenantRead(ctx, p.repository.pool, tenantID, func(tx pgx.Tx) ([]federation.Source, error) {
		return queryFederationSources(ctx, tx, tenantID, &request)
	})
}

// ListDiscoveryLiveSources selects bounded active host/audience assignments with
// live participation enabled, returning whether additional candidates exist.
func (p *PostgresFederationStore) ListDiscoveryLiveSources(ctx context.Context, tenantID, host string, audience federation.DiscoveryAudience, policyDigest string, limit int) ([]federation.Source, bool, error) {
	return p.listDiscoverySources(ctx, tenantID, host, audience, policyDigest, limit, "live")
}

// ListDiscoveryReferralSources selects bounded active host/audience assignments
// whose approved referral policy is enabled.
func (p *PostgresFederationStore) ListDiscoveryReferralSources(ctx context.Context, tenantID, host string, audience federation.DiscoveryAudience, policyDigest string, limit int) ([]federation.Source, bool, error) {
	return p.listDiscoverySources(ctx, tenantID, host, audience, policyDigest, limit, "referrals")
}

// listDiscoverySources admits only fixed live/referral modes, applies
// anonymous-disclosure constraints and bounds identifiers before hydrating
// selected sources.
func (p *PostgresFederationStore) listDiscoverySources(ctx context.Context, tenantID, host string, audience federation.DiscoveryAudience, policyDigest string, limit int, policyMember string) ([]federation.Source, bool, error) {
	if limit < 1 || policyMember != "live" && policyMember != "referrals" {
		return nil, false, catalog.E(catalog.CodeInvalidArgument, "discovery source selection is invalid")
	}
	type page struct {
		items     []federation.Source
		truncated bool
	}
	result, err := withTenantRead(ctx, p.repository.pool, tenantID, func(tx pgx.Tx) (page, error) {
		query := `
      SELECT source.id::text
        FROM federation_sources source
        JOIN federation_source_revisions revision
          ON revision.tenant_id=source.tenant_id AND revision.source_id=source.id AND revision.revision=source.revision
        JOIN federation_source_audiences assignment
          ON assignment.tenant_id=source.tenant_id AND assignment.source_id=source.id AND assignment.source_revision=source.revision
       WHERE source.tenant_id=$1 AND source.status='active'
         AND assignment.discovery_host=$2 AND assignment.audience=$3
         AND coalesce(revision.configuration->>'registrySearchUrl','')<>''`
		if policyMember == "live" {
			query += ` AND revision.retrieval_mode IN ('live','hybrid')
                       AND revision.configuration#>>'{live,enabled}'='true'
                       AND coalesce(revision.configuration#>>'{live,referralOnly}','false')<>'true'`
		} else {
			query += ` AND revision.configuration#>>'{referrals,enabled}'='true'`
		}
		if audience == federation.AudienceAnonymous {
			query += ` AND coalesce(revision.configuration->>'networkProfile','')=''
                       AND (coalesce(revision.configuration#>>'{live,enabled}','false')<>'true'
                            OR coalesce(revision.configuration#>>'{live,anonymousDisclosureAcknowledged}','false')='true')
                       AND (coalesce(revision.configuration#>>'{referrals,enabled}','false')<>'true'
                            OR coalesce(revision.configuration#>>'{referrals,anonymousDisclosureAcknowledged}','false')='true')`
		}
		query += ` ORDER BY (coalesce(revision.configuration->>'policyDigest','')=$4) DESC,source.id LIMIT $5`
		rows, err := tx.Query(ctx, query, tenantID, host, audience, policyDigest, limit+1)
		if err != nil {
			return page{}, translateError("select bounded discovery sources", err)
		}
		ids := make([]string, 0, limit+1)
		for rows.Next() {
			var sourceID string
			if err := rows.Scan(&sourceID); err != nil {
				rows.Close()
				return page{}, translateError("scan bounded discovery source", err)
			}
			ids = append(ids, sourceID)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return page{}, translateError("iterate bounded discovery sources", err)
		}
		rows.Close()
		truncated := len(ids) > limit
		if truncated {
			ids = ids[:limit]
		}
		items := make([]federation.Source, 0, len(ids))
		for _, sourceID := range ids {
			source, readErr := readFederationSource(ctx, tx, tenantID, sourceID, false)
			if readErr != nil {
				return page{}, readErr
			}
			items = append(items, source)
		}
		return page{items: items, truncated: truncated}, nil
	})
	return result.items, result.truncated, err
}

// queryFederationSources joins current revision, audience and referral evidence
// before decoding source state, applying the supplied administrative page when
// present.
func queryFederationSources(ctx context.Context, tx pgx.Tx, tenantID string, request *federation.SourceListRequest) ([]federation.Source, error) {
	query := `
			SELECT revision.configuration,jsonb_build_object('lastError',source.last_error),source.status,source.revision,
			       coalesce(source.last_complete_snapshot_id::text,''),source.last_sync_at,source.updated_at,
			       revision.revision IS NOT NULL,coalesce(revision.retrieval_mode,''),coalesce(revision.policy_id,''),
			       coalesce(revision.policy_revision,0),coalesce(revision.policy_digest,''),coalesce(revision.credential_revision,''),
			       coalesce((SELECT jsonb_agg(jsonb_build_object('host',audience.discovery_host,'audience',audience.audience) ORDER BY audience.discovery_host,audience.audience)
			                 FROM federation_source_audiences audience
			                 WHERE audience.tenant_id=source.tenant_id AND audience.source_id=source.id AND audience.source_revision=source.revision),'[]'::jsonb),
			       coalesce((SELECT jsonb_agg(referral.target_source_id::text ORDER BY referral.target_source_id)
			                 FROM federation_source_referrals referral
			                 WHERE referral.tenant_id=source.tenant_id AND referral.source_id=source.id AND referral.source_revision=source.revision),'[]'::jsonb)
			FROM federation_sources source
      LEFT JOIN federation_source_revisions revision
        ON revision.tenant_id=source.tenant_id AND revision.source_id=source.id AND revision.revision=source.revision
      WHERE source.tenant_id=$1 AND source.status <> $2`
	arguments := []any{tenantID, federation.SourceRemoved}
	if request != nil {
		query += ` AND ($3='' OR source.display_name COLLATE "C">$3 COLLATE "C" OR (source.display_name COLLATE "C"=$3 COLLATE "C" AND source.id>nullif($4,'')::uuid))`
		arguments = append(arguments, request.AfterDisplayName, request.AfterID)
		if request.DisplayName != "" {
			arguments = append(arguments, request.DisplayName)
			query += ` AND source.display_name COLLATE "C"=$` + strconv.Itoa(len(arguments)) + ` COLLATE "C"`
		}
		if request.Status != "" {
			arguments = append(arguments, request.Status)
			query += ` AND source.status=$` + strconv.Itoa(len(arguments))
		}
	}
	query += ` ORDER BY source.display_name COLLATE "C",source.id`
	if request != nil {
		arguments = append(arguments, request.Limit)
		query += ` LIMIT $` + strconv.Itoa(len(arguments))
	}
	rows, err := tx.Query(ctx, query, arguments...)
	if err != nil {
		return nil, translateError("list federation sources", err)
	}
	defer rows.Close()
	items := make([]federation.Source, 0)
	for rows.Next() {
		var raw, currentRaw []byte
		var status federation.SourceStatus
		var revision int64
		var snapshotID string
		var lastSync *time.Time
		var updated time.Time
		var hasRevision bool
		var retrievalMode, policyID, policyDigest, credentialRevision string
		var policyRevision int64
		var assignmentsRaw, referralsRaw []byte
		if err := rows.Scan(&raw, &currentRaw, &status, &revision, &snapshotID, &lastSync, &updated, &hasRevision, &retrievalMode, &policyID, &policyRevision, &policyDigest, &credentialRevision, &assignmentsRaw, &referralsRaw); err != nil {
			return nil, translateError("scan federation source", err)
		}
		source, decodeErr := decodeFederationSource(raw, currentRaw, status, revision, snapshotID, lastSync, updated, hasRevision, retrievalMode, policyID, policyRevision, policyDigest, credentialRevision, assignmentsRaw, referralsRaw)
		if decodeErr != nil {
			return nil, decodeErr
		}
		items = append(items, source)
	}
	if err := rows.Err(); err != nil {
		return nil, translateError("list federation sources", err)
	}
	return items, nil
}

// GetRetrievalState joins exact tenant/source/revision/request validators to
// retained response bytes; a missing reusable state returns found=false without
// error.
func (p *PostgresFederationStore) GetRetrievalState(ctx context.Context, tenantID, sourceID string, sourceRevision int64, requestKey string) (federation.RetrievalState, bool, error) {
	type retrievalResult struct {
		state federation.RetrievalState
		found bool
	}
	result, err := withTenantRead(ctx, p.repository.pool, tenantID, func(tx pgx.Tx) (retrievalResult, error) {
		var state federation.RetrievalState
		state.TenantID = tenantID
		state.SourceID = sourceID
		state.SourceRevision = sourceRevision
		state.RequestKey = requestKey
		err := tx.QueryRow(ctx, `
      SELECT state.requested_url,state.final_url,state.request_identity_digest,state.profile_id,state.policy_digest,
	         state.credential_revision,state.representation_digest,state.media_type,document.body,state.etag,
             state.last_modified,state.vary_names,state.vary_selection_digest,state.list_terminal,state.updated_at
        FROM source_retrieval_states state
        JOIN source_retrieval_documents document
          ON document.tenant_id=state.tenant_id AND document.source_id=state.source_id
         AND document.source_revision=state.source_revision AND document.representation_digest=state.representation_digest
       WHERE state.tenant_id=$1 AND state.source_id=$2 AND state.source_revision=$3 AND state.request_key=$4`,
			tenantID, sourceID, sourceRevision, requestKey).Scan(
			&state.RequestedURL, &state.FinalURL, &state.RequestIdentityDigest, &state.ProfileID, &state.PolicyDigest,
			&state.CredentialRevision, &state.RepresentationDigest, &state.MediaType, &state.Body, &state.ETag,
			&state.LastModified, &state.VaryNames, &state.VarySelectionDigest, &state.ListTerminal, &state.UpdatedAt)
		if errors.Is(err, pgx.ErrNoRows) {
			return retrievalResult{}, nil
		}
		if err != nil {
			return retrievalResult{}, translateError("read source retrieval state", err)
		}
		return retrievalResult{state: state, found: true}, nil
	})
	return result.state, result.found, err
}

// readFederationSource reads current mutable status plus immutable
// revision/provenance, optionally locking the source row for mutation.
func readFederationSource(ctx context.Context, tx pgx.Tx, tenantID, idValue string, lock bool) (federation.Source, error) {
	query := `
	SELECT coalesce(revision.configuration,source.configuration),jsonb_build_object('lastError',source.last_error),source.status,source.revision,
	       coalesce(source.last_complete_snapshot_id::text,''),source.last_sync_at,source.updated_at,
	       revision.revision IS NOT NULL,coalesce(revision.retrieval_mode,''),coalesce(revision.policy_id,''),
	       coalesce(revision.policy_revision,0),coalesce(revision.policy_digest,''),coalesce(revision.credential_revision,''),
	       coalesce((SELECT jsonb_agg(jsonb_build_object('host',audience.discovery_host,'audience',audience.audience) ORDER BY audience.discovery_host,audience.audience)
	                 FROM federation_source_audiences audience
	                 WHERE audience.tenant_id=source.tenant_id AND audience.source_id=source.id AND audience.source_revision=source.revision),'[]'::jsonb),
	       coalesce((SELECT jsonb_agg(referral.target_source_id::text ORDER BY referral.target_source_id)
	                 FROM federation_source_referrals referral
	                 WHERE referral.tenant_id=source.tenant_id AND referral.source_id=source.id AND referral.source_revision=source.revision),'[]'::jsonb)
	  FROM federation_sources source
    LEFT JOIN federation_source_revisions revision
      ON revision.tenant_id=source.tenant_id AND revision.source_id=source.id AND revision.revision=source.revision
	WHERE source.tenant_id=$1 AND source.id=$2`
	if lock {
		query += ` FOR UPDATE OF source`
	}
	var raw, currentRaw []byte
	var status federation.SourceStatus
	var revision int64
	var snapshotID string
	var lastSync *time.Time
	var updated time.Time
	var hasRevision bool
	var retrievalMode, policyID, policyDigest, credentialRevision string
	var policyRevision int64
	var assignmentsRaw, referralsRaw []byte
	if err := tx.QueryRow(ctx, query, tenantID, idValue).Scan(&raw, &currentRaw, &status, &revision, &snapshotID, &lastSync, &updated, &hasRevision, &retrievalMode, &policyID, &policyRevision, &policyDigest, &credentialRevision, &assignmentsRaw, &referralsRaw); err != nil {
		return federation.Source{}, translateError("read federation source", err)
	}
	return decodeFederationSource(raw, currentRaw, status, revision, snapshotID, lastSync, updated, hasRevision, retrievalMode, policyID, policyRevision, policyDigest, credentialRevision, assignmentsRaw, referralsRaw)
}

// decodeFederationSource rejects missing or inconsistent current policy,
// credential, audience and referral evidence before combining it with mutable sync
// state.
func decodeFederationSource(raw, currentRaw []byte, status federation.SourceStatus, revision int64, snapshotID string, lastSync *time.Time, updated time.Time, hasRevision bool, retrievalMode, policyID string, policyRevision int64, policyDigest, credentialRevision string, assignmentsRaw, referralsRaw []byte) (federation.Source, error) {
	var source federation.Source
	if err := json.Unmarshal(raw, &source); err != nil {
		return federation.Source{}, catalog.Wrap(catalog.CodeInternal, "decode federation source", err)
	}
	var current struct {
		LastError string `json:"lastError"`
	}
	if err := json.Unmarshal(currentRaw, &current); err != nil {
		return federation.Source{}, catalog.Wrap(catalog.CodeInternal, "decode current federation source state", err)
	}

	if !hasRevision || source.RetrievalMode == "" || source.PolicyID == "" || source.PolicyRevision <= 0 || source.PolicyDigest == "" ||
		string(source.RetrievalMode) != retrievalMode || source.PolicyID != policyID || source.PolicyRevision != policyRevision || source.PolicyDigest != policyDigest || source.CredentialRevision != credentialRevision {
		return federation.Source{}, catalog.E(catalog.CodeInternal, "source configuration requires complete matching current revision evidence")
	}

	var assignments []federation.DiscoveryAssignment
	if err := json.Unmarshal(assignmentsRaw, &assignments); err != nil {
		return federation.Source{}, catalog.Wrap(catalog.CodeInternal, "decode federation source audiences", err)
	}
	if !slices.Equal(source.DiscoveryAssignments, assignments) {
		return federation.Source{}, catalog.E(catalog.CodeInternal, "source audience projection does not match its revision")
	}
	if len(source.DiscoveryAssignments) == 0 {
		source.DiscoveryAssignments = nil
	}
	var referrals []string
	if err := json.Unmarshal(referralsRaw, &referrals); err != nil {
		return federation.Source{}, catalog.Wrap(catalog.CodeInternal, "decode federation source referrals", err)
	}
	if !slices.Equal(source.Referrals.ApprovedSourceIDs, referrals) {
		return federation.Source{}, catalog.E(catalog.CodeInternal, "source referral projection does not match its revision")
	}
	if len(source.Referrals.ApprovedSourceIDs) == 0 {
		source.Referrals.ApprovedSourceIDs = nil
	}
	source.Status = status
	source.Revision = revision
	source.LastCompleteSnapshotID = snapshotID
	source.LastSyncAt = lastSync
	source.LastError = current.LastError
	source.UpdatedAt = updated
	return source, nil
}

// CommitSync atomically retains the immutable run, reconciles current
// observations, advances source state, and appends audit/outbox evidence. The
// expected source revision prevents a stale network run from committing.
func (p *PostgresFederationStore) CommitSync(ctx context.Context, snapshot federation.Snapshot, observations []federation.Observation, source federation.Source, expectedSourceRevision int64, evidence ...federation.MutationEvidence) (federation.Snapshot, federation.Source, error) {
	type result struct {
		snapshot federation.Snapshot
		source   federation.Source
	}
	committed, err := withTenantTx(ctx, p.repository.pool, snapshot.TenantID, pgx.TxOptions{}, func(tx pgx.Tx) (result, error) {
		current, err := readFederationSource(ctx, tx, snapshot.TenantID, snapshot.SourceID, true)
		if err != nil {
			return result{}, err
		}
		if source.ID != current.ID || source.TenantID != snapshot.TenantID || current.TenantID != snapshot.TenantID {
			return result{}, catalog.E(catalog.CodeNotFound, "federation source was not found")
		}
		if current.Revision != expectedSourceRevision || source.Revision != expectedSourceRevision || snapshot.SourceRevision != expectedSourceRevision {
			return result{}, catalog.E(catalog.CodePreconditionFailed, "source changed while the sync was running")
		}
		syncStatus := source.Status
		syncSnapshotID := source.LastCompleteSnapshotID
		syncAt := source.LastSyncAt
		syncError := source.LastError
		syncUpdatedAt := source.UpdatedAt
		source = current
		source.Status = syncStatus
		source.LastCompleteSnapshotID = syncSnapshotID
		source.LastSyncAt = syncAt
		source.LastError = syncError
		source.UpdatedAt = syncUpdatedAt
		if snapshot.ReconcileCurrentView && (current.Type != federation.SourceRegistry || current.RegistryListURL == "" || current.RetrievalMode != federation.RetrievalIndexed && current.RetrievalMode != federation.RetrievalHybrid) {
			return result{}, catalog.E(catalog.CodeInvalidArgument, "current-view reconciliation requires an inspected Registry List source")
		}
		if err := federation.ValidateSnapshotCommit(snapshot, observations); err != nil {
			return result{}, err
		}
		members := make([]string, 0, len(observations))
		memberIdentities := make([]string, 0, len(observations))
		memberExactDigests := make([]string, 0, len(observations))
		incomingDigests := make(map[string][]string, len(observations))
		for _, observation := range observations {
			members = append(members, observation.SourceKey)
			identityDigest := observation.IdentityDigest
			memberIdentities = append(memberIdentities, identityDigest)
			memberExactDigests = append(memberExactDigests, observation.ExactEntryDigest)
			if !slices.Contains(incomingDigests[observation.SourceKey], identityDigest) {
				incomingDigests[observation.SourceKey] = append(incomingDigests[observation.SourceKey], identityDigest)
			}
		}
		snapshot.Withdrawn = 0
		if snapshot.WithdrawalAuthority || snapshot.ReconcileCurrentView {
			var withdrawn int
			updateErr := tx.QueryRow(ctx, `
		WITH changed AS (
		  UPDATE observations observation SET freshness=CASE
		    WHEN $3 AND (
		      NOT EXISTS (SELECT 1 FROM unnest($5::text[]) incoming_key(value) WHERE incoming_key.value=observation.source_key)
		      OR EXISTS (SELECT 1 FROM unnest($5::text[],$7::text[]) incoming(source_key,exact_digest)
		                  WHERE incoming.source_key=observation.source_key AND incoming.exact_digest=observation.exact_entry_digest)
		    ) THEN $8 ELSE $9 END
		WHERE tenant_id=$1 AND source_id=$2 AND freshness=$4
		  AND NOT EXISTS (
		    SELECT 1 FROM unnest($5::text[],$6::text[]) incoming(source_key,identity_digest)
		     WHERE incoming.source_key=observation.source_key AND incoming.identity_digest=observation.semantic_digest
		  ) RETURNING freshness
		)
		SELECT count(*) FILTER (WHERE freshness=$8) FROM changed`, snapshot.TenantID, snapshot.SourceID, snapshot.WithdrawalAuthority, federation.FreshCurrent, members, memberIdentities, memberExactDigests, federation.FreshWithdrawn, federation.FreshStale).Scan(&withdrawn)
			if updateErr != nil {
				return result{}, translateError("reconcile missing source observations", updateErr)
			}
			if snapshot.WithdrawalAuthority {
				snapshot.Withdrawn = withdrawn
			}
		}
		if len(evidence) == 1 && evidence[0].Audit.ID != "" {
			finalized, finalizeErr := federation.FinalizeSyncEvidence(evidence[0], snapshot)
			if finalizeErr != nil {
				return result{}, finalizeErr
			}
			evidence[0] = finalized
		}
		details, err := json.Marshal(snapshot)
		if err != nil {
			return result{}, catalog.Wrap(catalog.CodeInternal, "encode source snapshot", err)
		}
		_, err = tx.Exec(ctx, `
      INSERT INTO source_snapshots(id,tenant_id,source_id,source_revision,state,enumeration_complete,withdrawal_authority,member_count,body_digest,details,started_at,completed_at)
      VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)`,
			snapshot.ID, snapshot.TenantID, snapshot.SourceID, snapshot.SourceRevision, snapshot.State, snapshot.EnumerationComplete, snapshot.WithdrawalAuthority,
			snapshot.MemberCount, snapshot.BodyDigest, details, snapshot.StartedAt, snapshot.CompletedAt)
		if err != nil {
			return result{}, translateError("save source snapshot", err)
		}
		for _, observation := range observations {
			if observation.Freshness == federation.FreshCurrent {
				if _, err := tx.Exec(ctx, `
	        UPDATE observations SET freshness=$4
	        WHERE tenant_id=$1 AND source_id=$2 AND source_key=$3 AND NOT (semantic_digest=ANY($5::text[])) AND freshness=$6`,
					observation.TenantID, observation.SourceID, observation.SourceKey, federation.FreshStale, incomingDigests[observation.SourceKey], federation.FreshCurrent); err != nil {
					return result{}, translateError("supersede source observation", err)
				}
			}
			compatibility, marshalErr := json.Marshal(observation.Compatibility)
			if marshalErr != nil {
				return result{}, catalog.Wrap(catalog.CodeInternal, "encode observation compatibility", marshalErr)
			}
			var retainedID string
			err = tx.QueryRow(ctx, `
	        INSERT INTO observations(id,tenant_id,source_id,snapshot_id,source_key,identifier,version,version_present,kind,media_type,completeness,compatibility,semantic_digest,identity_digest,exact_entry_digest,normalized_entry_digest,normalization_profile,raw_entry_bytes,normalized_entry_bytes,freshness,first_seen_at,last_seen_at,search_display_name,search_description,search_tags,search_representative_queries,search_publisher,search_vector,artwork_metadata,ratings_metadata)
	        VALUES ($1,$2,$3,$4,$5,nullif($6,''),CASE WHEN $8 THEN $7 ELSE NULL END,$8,nullif($9,''),nullif($10,''),$11,$12,$13,$13,$14,nullif($15,''),nullif($16,''),$17,$18,$19,$20,$21,
	          CASE WHEN $15<>'' THEN $22 ELSE NULL END,CASE WHEN $15<>'' THEN $23 ELSE NULL END,CASE WHEN $15<>'' THEN $24::text[] ELSE NULL END,CASE WHEN $15<>'' THEN $25::text[] ELSE NULL END,CASE WHEN $15<>'' THEN $26 ELSE NULL END,
	          CASE WHEN $15<>'' THEN catalog_core_lexical_vector_v1($6) || catalog_core_lexical_vector_v1($22) || catalog_core_lexical_vector_v1($23) || catalog_core_lexical_vector_v1(array_to_string($24::text[],' ')) || catalog_core_lexical_vector_v1(array_to_string($25::text[],' ')) ELSE NULL END,$28,$29)
	        ON CONFLICT (tenant_id,source_id,source_key,semantic_digest) DO UPDATE SET
	          freshness=CASE WHEN excluded.freshness=$27 THEN excluded.freshness ELSE observations.freshness END,
	          last_seen_at=GREATEST(observations.last_seen_at,excluded.last_seen_at),
 artwork_metadata=excluded.artwork_metadata,ratings_metadata=excluded.ratings_metadata
	        RETURNING id::text`,
				observation.ID, observation.TenantID, observation.SourceID, observation.SnapshotID, observation.SourceKey, observation.Identifier,
				observation.Version, observation.VersionPresent, observation.Kind, observation.MediaType, observation.Completeness, compatibility,
				observation.IdentityDigest, observation.ExactEntryDigest, observation.NormalizedEntryDigest, observation.NormalizationProfile, observation.RawEntry, observation.NormalizedEntry, observation.Freshness, observation.FirstSeenAt, observation.LastSeenAt,
				observation.SearchDisplayName, observation.SearchDescription, observation.SearchTags, observation.SearchQueries, observation.SearchPublisher, federation.FreshCurrent, nullableArtworkMetadata(observation.ArtworkMetadata), nullableArtworkMetadata(observation.RatingsMetadata)).Scan(&retainedID)
			if err != nil {
				return result{}, translateError("save source observation", err)
			}
			occurrence := observation.Occurrence
			if _, err := tx.Exec(ctx, `
		  INSERT INTO observation_occurrences(tenant_id,observation_id,source_id,snapshot_id,source_revision,retrieval_digest,entry_location,exact_entry_digest,normalized_entry_digest,evidence_complete,observed_at,normalization_input_digest)
		  VALUES ($1,$2,$3,$4,$5,$6,$7,$8,nullif($9,''),$10,$11,nullif($12,''))`, observation.TenantID, retainedID, occurrence.SourceID, occurrence.SnapshotID, occurrence.SourceRevision, occurrence.RetrievalDigest, occurrence.EntryLocation, occurrence.ExactEntryDigest, occurrence.NormalizedEntryDigest, occurrence.EvidenceComplete, occurrence.ObservedAt, occurrence.NormalizationInputDigest); err != nil {
				return result{}, translateError("save observation occurrence", err)
			}
		}
		for _, requestKey := range snapshot.RetrievalInvalidations {
			if _, err := tx.Exec(ctx, `DELETE FROM source_retrieval_states WHERE tenant_id=$1 AND source_id=$2 AND source_revision=$3 AND request_key=$4`, snapshot.TenantID, snapshot.SourceID, snapshot.SourceRevision, requestKey); err != nil {
				return result{}, translateError("invalidate source retrieval state", err)
			}
		}
		for _, state := range snapshot.RetrievalStates {
			if _, err := tx.Exec(ctx, `
          INSERT INTO source_retrieval_documents(tenant_id,source_id,source_revision,representation_digest,body,created_at)
          VALUES ($1,$2,$3,$4,$5,$6) ON CONFLICT DO NOTHING`, state.TenantID, state.SourceID, state.SourceRevision, state.RepresentationDigest, state.Body, state.UpdatedAt); err != nil {
				return result{}, translateError("retain source retrieval document", err)
			}
			if _, err := tx.Exec(ctx, `
          INSERT INTO source_retrieval_states(tenant_id,source_id,source_revision,request_key,requested_url,final_url,request_identity_digest,profile_id,media_type,policy_digest,credential_revision,representation_digest,etag,last_modified,vary_names,vary_selection_digest,list_terminal,updated_at)
          VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,coalesce($15::text[],ARRAY[]::text[]),$16,$17,$18)
          ON CONFLICT (tenant_id,source_id,source_revision,request_key) DO UPDATE SET
            requested_url=excluded.requested_url,final_url=excluded.final_url,request_identity_digest=excluded.request_identity_digest,
            profile_id=excluded.profile_id,media_type=excluded.media_type,policy_digest=excluded.policy_digest,credential_revision=excluded.credential_revision,
            representation_digest=excluded.representation_digest,etag=excluded.etag,last_modified=excluded.last_modified,
            vary_names=excluded.vary_names,vary_selection_digest=excluded.vary_selection_digest,list_terminal=excluded.list_terminal,updated_at=excluded.updated_at`,
				state.TenantID, state.SourceID, state.SourceRevision, state.RequestKey, state.RequestedURL, state.FinalURL, state.RequestIdentityDigest, state.ProfileID, state.MediaType, state.PolicyDigest, state.CredentialRevision, state.RepresentationDigest, state.ETag, state.LastModified, state.VaryNames, state.VarySelectionDigest, state.ListTerminal, state.UpdatedAt); err != nil {
				return result{}, translateError("save source retrieval state", err)
			}
		}
		for _, attempt := range snapshot.Retrievals {
			if _, err := tx.Exec(ctx, `
          INSERT INTO source_retrieval_attempts(tenant_id,id,source_id,source_revision,snapshot_id,request_key,requested_url_digest,final_url_digest,profile_id,status,received_body,reused,representation_digest,etag_digest,last_modified_digest,fetched_at)
          VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,nullif($13,''),nullif($14,''),nullif($15,''),$16)`,
				snapshot.TenantID, attempt.ID, snapshot.SourceID, snapshot.SourceRevision, snapshot.ID, attempt.RequestKey, attempt.RequestedURLDigest, attempt.FinalURLDigest, attempt.ProfileID, attempt.Status, attempt.ReceivedBody, attempt.Reused, attempt.RepresentationDigest, attempt.ETagDigest, attempt.LastModifiedDigest, attempt.FetchedAt); err != nil {
				return result{}, translateError("save source retrieval evidence", err)
			}
		}
		command, err := tx.Exec(ctx, `
			UPDATE federation_sources
			   SET status=$3,last_complete_snapshot_id=nullif($4,'')::uuid,last_sync_at=$5,last_error=$6,updated_at=$7
			 WHERE tenant_id=$1 AND id=$2 AND revision=$8`, source.TenantID, source.ID, source.Status,
			source.LastCompleteSnapshotID, source.LastSyncAt, source.LastError, source.UpdatedAt, source.Revision)
		if err != nil {
			return result{}, translateError("commit federation source sync state", err)
		}
		if command.RowsAffected() != 1 {
			return result{}, catalog.E(catalog.CodePreconditionFailed, "source revision changed before synchronization committed")
		}
		if err := insertFederationEvidence(ctx, tx, snapshot.TenantID, snapshot.ID, evidence, audit.ActionSourceSyncCompleted, audit.ActionSourceSyncFailed); err != nil {
			return result{}, err
		}
		return result{snapshot: snapshot, source: source}, nil
	})
	return committed.snapshot, committed.source, err
}

// federationExpectedObservationSet extracts the consequence-preview binding only
// from a single mutation-evidence envelope.
func federationExpectedObservationSet(evidence []federation.MutationEvidence) *federation.ObservationSet {
	if len(evidence) != 1 {
		return nil
	}
	return evidence[0].ExpectedObservationSet
}

// insertFederationEvidence validates tenant/target/action binding, then appends
// the supplied audit and independently optional outbox record atomically.
func insertFederationEvidence(ctx context.Context, tx pgx.Tx, tenantID, targetID string, evidence []federation.MutationEvidence, allowedActions ...string) error {
	if err := federation.ValidateMutationEvidence(tenantID, targetID, evidence, allowedActions...); err != nil {
		return err
	}
	if len(evidence) == 0 {
		return nil
	}
	if evidence[0].Audit.ID == "" {
		return nil
	}
	if err := insertAuditEvent(ctx, tx, evidence[0].Audit); err != nil {
		return err
	}
	if evidence[0].Outbox != nil {
		return insertOutboxEvent(ctx, tx, *evidence[0].Outbox)
	}
	return nil
}

// GetSnapshot loads one retained tenant-owned synchronization snapshot document by
// exact identity.
func (p *PostgresFederationStore) GetSnapshot(ctx context.Context, tenantID, idValue string) (federation.Snapshot, error) {
	return withTenantRead(ctx, p.repository.pool, tenantID, func(tx pgx.Tx) (federation.Snapshot, error) {
		var raw []byte
		if err := tx.QueryRow(ctx, `SELECT details FROM source_snapshots WHERE tenant_id=$1 AND id=$2`, tenantID, idValue).Scan(&raw); err != nil {
			return federation.Snapshot{}, translateError("read source snapshot", err)
		}
		var snapshot federation.Snapshot
		if err := json.Unmarshal(raw, &snapshot); err != nil {
			return federation.Snapshot{}, catalog.Wrap(catalog.CodeInternal, "decode source snapshot", err)
		}
		return snapshot, nil
	})
}

// ListSnapshots selects a SQL-bounded descending start-time/ID history, optionally
// narrowed to one source.
func (p *PostgresFederationStore) ListSnapshots(ctx context.Context, tenantID, sourceID string, limit int) ([]federation.Snapshot, error) {
	return withTenantRead(ctx, p.repository.pool, tenantID, func(tx pgx.Tx) ([]federation.Snapshot, error) {
		rows, err := tx.Query(ctx, `
      SELECT details FROM source_snapshots
	  WHERE tenant_id=$1 AND ($2='' OR source_id=nullif($2,'')::uuid)
	  ORDER BY started_at DESC,id DESC LIMIT $3`, tenantID, sourceID, limit)
		if err != nil {
			return nil, translateError("list source snapshots", err)
		}
		defer rows.Close()
		items := make([]federation.Snapshot, 0)
		for rows.Next() {
			var raw []byte
			if err := rows.Scan(&raw); err != nil {
				return nil, translateError("scan source snapshot", err)
			}
			var snapshot federation.Snapshot
			if err := json.Unmarshal(raw, &snapshot); err != nil {
				return nil, catalog.Wrap(catalog.CodeInternal, "decode source snapshot", err)
			}
			items = append(items, snapshot)
		}
		if err := rows.Err(); err != nil {
			return nil, translateError("list source snapshots", err)
		}
		return items, nil
	})
}

// ListSnapshotPage applies exact source scope and descending start-time/ID
// continuation at SQL selection.
func (p *PostgresFederationStore) ListSnapshotPage(ctx context.Context, tenantID string, request federation.SnapshotListRequest) ([]federation.Snapshot, error) {
	return withTenantRead(ctx, p.repository.pool, tenantID, func(tx pgx.Tx) ([]federation.Snapshot, error) {
		rows, err := tx.Query(ctx, `
      SELECT details FROM source_snapshots
	  WHERE tenant_id=$1 AND source_id=$2
	    AND ($3::timestamptz IS NULL OR started_at<$3 OR (started_at=$3 AND id<nullif($4,'')::uuid))
	  ORDER BY started_at DESC,id DESC LIMIT $5`, tenantID, request.SourceID, request.AfterStartedAt, request.AfterID, request.Limit)
		if err != nil {
			return nil, translateError("list source snapshot page", err)
		}
		defer rows.Close()
		items := make([]federation.Snapshot, 0)
		for rows.Next() {
			var raw []byte
			if err := rows.Scan(&raw); err != nil {
				return nil, translateError("scan source snapshot", err)
			}
			var snapshot federation.Snapshot
			if err := json.Unmarshal(raw, &snapshot); err != nil {
				return nil, catalog.Wrap(catalog.CodeInternal, "decode source snapshot", err)
			}
			items = append(items, snapshot)
		}
		return items, translateError("iterate source snapshot page", rows.Err())
	})
}

// ListCurrentObservations returns the complete current source set only if it fits
// the bound; overflow returns nil items and a true boolean.
func (p *PostgresFederationStore) ListCurrentObservations(ctx context.Context, tenantID, sourceID string, limit int) ([]federation.Observation, bool, error) {
	if limit < 1 {
		return nil, false, catalog.E(catalog.CodeInvalidArgument, "current observation limit must be positive")
	}
	type page struct {
		items     []federation.Observation
		truncated bool
	}
	result, err := withTenantRead(ctx, p.repository.pool, tenantID, func(tx pgx.Tx) (page, error) {
		rows, err := tx.Query(ctx, `
      SELECT id,tenant_id,source_id,snapshot_id,source_key,coalesce(identifier,''),coalesce(version,''),version_present,
             coalesce(kind,''),coalesce(media_type,''),completeness,compatibility,raw_entry_bytes,normalized_entry_bytes,
             exact_entry_digest,coalesce(identity_digest,semantic_digest),coalesce(normalized_entry_digest,''),coalesce(normalization_profile,''),coalesce(search_display_name,''),coalesce(search_description,''),coalesce(search_tags,'{}'),coalesce(search_representative_queries,'{}'),coalesce(search_publisher,''),freshness,first_seen_at,last_seen_at
        FROM observations
       WHERE tenant_id=$1 AND source_id=$2 AND freshness=$3
       ORDER BY id LIMIT $4`, tenantID, sourceID, federation.FreshCurrent, limit+1)
		if err != nil {
			return page{}, translateError("list bounded current source observations", err)
		}
		defer rows.Close()
		items := make([]federation.Observation, 0, limit+1)
		for rows.Next() {
			item, scanErr := scanFederationObservation(rows)
			if scanErr != nil {
				return page{}, scanErr
			}
			items = append(items, item)
		}
		if err := rows.Err(); err != nil {
			return page{}, translateError("iterate bounded current source observations", err)
		}
		truncated := len(items) > limit
		if truncated {
			items = nil
		}
		return page{items: items, truncated: truncated}, nil
	})
	return result.items, result.truncated, err
}

// ListObservationPage applies optional source/freshness filters and deterministic
// identity/version-presence/ID ordering within a SQL-bounded tenant page.
func (p *PostgresFederationStore) ListObservationPage(ctx context.Context, tenantID string, request federation.ObservationListRequest) ([]federation.Observation, error) {
	return withTenantRead(ctx, p.repository.pool, tenantID, func(tx pgx.Tx) ([]federation.Observation, error) {
		rows, err := tx.Query(ctx, `
      SELECT `+observationMetadataColumns+`
      FROM observations
	 WHERE tenant_id=$1 AND ($2='' OR source_id=nullif($2,'')::uuid) AND (NOT $3 OR freshness=$4)
	   AND ($8='' OR (coalesce(identifier,'') COLLATE "C",NOT version_present,coalesce(version,'') COLLATE "C",id)>($5::text COLLATE "C",NOT $6,$7::text COLLATE "C",nullif($8,'')::uuid))
	 ORDER BY coalesce(identifier,'') COLLATE "C",version_present DESC,coalesce(version,'') COLLATE "C",id LIMIT $9`, tenantID, request.SourceID, request.CurrentOnly, federation.FreshCurrent, request.AfterIdentifier, request.AfterVersionPresent, request.AfterVersion, request.AfterID, request.Limit)
		if err != nil {
			return nil, translateError("list observation page", err)
		}
		defer rows.Close()
		items := make([]federation.Observation, 0)
		for rows.Next() {
			item, scanErr := scanFederationObservationMetadata(rows, nil)
			if scanErr != nil {
				return nil, scanErr
			}
			items = append(items, item)
		}
		return items, translateError("iterate observation page", rows.Err())
	})
}

// GetObservation reads one tenant-owned observation with retained exact bytes and
// normalization metadata, including its current freshness state.
func (p *PostgresFederationStore) GetObservation(ctx context.Context, tenantID, observationID string) (federation.Observation, error) {
	return withTenantRead(ctx, p.repository.pool, tenantID, func(tx pgx.Tx) (federation.Observation, error) {
		return scanFederationObservation(tx.QueryRow(ctx, `
      SELECT id,tenant_id,source_id,snapshot_id,source_key,coalesce(identifier,''),coalesce(version,''),version_present,
             coalesce(kind,''),coalesce(media_type,''),completeness,compatibility,raw_entry_bytes,normalized_entry_bytes,
	     exact_entry_digest,coalesce(identity_digest,semantic_digest),coalesce(normalized_entry_digest,''),coalesce(normalization_profile,''),coalesce(search_display_name,''),coalesce(search_description,''),coalesce(search_tags,'{}'),coalesce(search_representative_queries,'{}'),coalesce(search_publisher,''),freshness,first_seen_at,last_seen_at
      FROM observations WHERE tenant_id=$1 AND id=$2`, tenantID, observationID))
	})
}

// ObservationHasSnapshotEvidence checks one tenant-owned provenance binding.
// EXISTS stops at the first match and never hydrates retained history.
func (p *PostgresFederationStore) ObservationHasSnapshotEvidence(ctx context.Context, tenantID, observationID, sourceID, snapshotID string, revision int64, exactDigest string) (bool, error) {
	return withTenantRead(ctx, p.repository.pool, tenantID, func(tx pgx.Tx) (bool, error) {
		var found bool
		err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM observation_occurrences
          WHERE tenant_id=$1 AND observation_id=$2 AND source_id=$3 AND snapshot_id=$4
            AND source_revision=$5 AND exact_entry_digest=$6 AND evidence_complete)`, tenantID, observationID, sourceID, snapshotID, revision, exactDigest).Scan(&found)
		if err != nil {
			return false, translateError("check observation snapshot evidence", err)
		}
		return found, nil
	})
}

// ListObservationGroupMembers selects bounded current normalized observations for
// the exact identifier and version-presence identity, ordered by source and
// digests.
func (p *PostgresFederationStore) ListObservationGroupMembers(ctx context.Context, tenantID, identifier, version string, versionPresent bool, limit int) ([]federation.Observation, error) {
	return withTenantRead(ctx, p.repository.pool, tenantID, func(tx pgx.Tx) ([]federation.Observation, error) {
		rows, err := tx.Query(ctx, `
      SELECT `+observationMetadataColumns+`
      FROM observations
      WHERE tenant_id=$1 AND identifier=$2 AND version_present=$3
        AND (($3 AND version=$4) OR (NOT $3 AND version IS NULL))
        AND normalized_entry_digest IS NOT NULL AND freshness=$5
      ORDER BY source_id,normalized_entry_digest,exact_entry_digest,id LIMIT $6`, tenantID, identifier, versionPresent, version, federation.FreshCurrent, limit)
		if err != nil {
			return nil, translateError("list bounded observation group members", err)
		}
		defer rows.Close()
		items := make([]federation.Observation, 0)
		for rows.Next() {
			item, scanErr := scanFederationObservationMetadata(rows, nil)
			if scanErr != nil {
				return nil, scanErr
			}
			items = append(items, item)
		}
		return items, translateError("iterate bounded observation group members", rows.Err())
	})
}

// ObservationGroupConflicts separately reports cross-source content/kind
// disagreement and within-source normalized-content conflicts for requested source
// IDs.
func (p *PostgresFederationStore) ObservationGroupConflicts(ctx context.Context, tenantID, identifier, version string, versionPresent bool, sourceIDs []string) (bool, map[string]bool, error) {
	type result struct {
		conflict        bool
		sourceConflicts map[string]bool
	}
	value, err := withTenantRead(ctx, p.repository.pool, tenantID, func(tx pgx.Tx) (result, error) {
		var distinctDigests, distinctKinds int
		if err := tx.QueryRow(ctx, `
      SELECT count(DISTINCT normalized_entry_digest),count(DISTINCT kind)
        FROM observations
       WHERE tenant_id=$1 AND identifier=$2 AND version_present=$3
         AND (($3 AND version=$4) OR (NOT $3 AND version IS NULL))
         AND normalized_entry_digest IS NOT NULL AND freshness=$5`, tenantID, identifier, versionPresent, version, federation.FreshCurrent).Scan(&distinctDigests, &distinctKinds); err != nil {
			return result{}, translateError("read observation group conflict", err)
		}
		conflicts := make(map[string]bool, len(sourceIDs))
		if len(sourceIDs) > 0 {
			rows, err := tx.Query(ctx, `
        SELECT source_id::text
          FROM observations
         WHERE tenant_id=$1 AND identifier=$2 AND version_present=$3
           AND (($3 AND version=$4) OR (NOT $3 AND version IS NULL))
           AND normalized_entry_digest IS NOT NULL AND freshness=$5 AND source_id=ANY($6::uuid[])
         GROUP BY source_id HAVING count(DISTINCT normalized_entry_digest)>1`, tenantID, identifier, versionPresent, version, federation.FreshCurrent, sourceIDs)
			if err != nil {
				return result{}, translateError("read observation source conflicts", err)
			}
			defer rows.Close()
			for rows.Next() {
				var sourceID string
				if err := rows.Scan(&sourceID); err != nil {
					return result{}, translateError("scan observation source conflict", err)
				}
				conflicts[sourceID] = true
			}
			if err := rows.Err(); err != nil {
				return result{}, translateError("iterate observation source conflicts", err)
			}
		}
		return result{conflict: distinctDigests > 1 || distinctKinds > 1, sourceConflicts: conflicts}, nil
	})
	return value.conflict, value.sourceConflicts, err
}

// SearchObservationPage selects current normalized observations with
// lexical/kind/tag filtering and stable score/identifier/ID continuation.
func (p *PostgresFederationStore) SearchObservationPage(ctx context.Context, tenantID string, request federation.ObservationSearchRequest) ([]federation.SearchResult, error) {
	return withTenantRead(ctx, p.repository.pool, tenantID, func(tx pgx.Tx) ([]federation.SearchResult, error) {
		if _, err := readCatalogSearchGeneration(ctx, tx, tenantID); err != nil {
			return nil, err
		}
		kinds := make([]string, len(request.Query.Kinds))
		for index, kind := range request.Query.Kinds {
			kinds[index] = string(kind)
		}
		rows, err := tx.Query(ctx, indexedApprovedSourcesCTE+`, matched AS (
        SELECT observation.id,observation.tenant_id,observation.source_id,observation.snapshot_id,observation.source_key,
               observation.identifier,observation.version,observation.version_present,observation.kind,observation.media_type,
               observation.completeness,observation.compatibility,
               coalesce(octet_length(observation.raw_entry_bytes)>0,false) AS raw_entry_available,
               observation.exact_entry_digest,observation.identity_digest,
               observation.semantic_digest,observation.normalized_entry_digest,observation.normalization_profile,
               observation.search_display_name,observation.search_description,observation.search_tags,
               observation.search_representative_queries,observation.search_publisher,observation.freshness,
               observation.first_seen_at,observation.last_seen_at,
               (CASE WHEN $2='' THEN 100 ELSE 80 END)::integer AS result_score
          FROM observations observation
          JOIN approved_sources source ON source.tenant_id=observation.tenant_id AND source.id=observation.source_id
         WHERE observation.tenant_id=$1 AND observation.freshness='current'
           AND NOT (source.excluded_identifiers @> jsonb_build_array(observation.identifier))
           AND (source.identifiers='[]'::jsonb OR source.identifiers @> jsonb_build_array(observation.identifier))
           AND (source.versions='[]'::jsonb OR source.versions @> jsonb_build_array(coalesce(observation.version,'')))
           AND (source.kinds='[]'::jsonb OR source.kinds @> jsonb_build_array(observation.kind))
           AND (source.publishers='[]'::jsonb OR source.publishers @> jsonb_build_array(observation.search_publisher))
           AND (source.tags='[]'::jsonb OR to_jsonb(observation.search_tags) @> source.tags)
           AND observation.normalized_entry_digest IS NOT NULL AND observation.search_vector IS NOT NULL
           AND (coalesce(cardinality($3::text[]),0)=0 OR observation.kind=ANY($3::text[]))
           AND (coalesce(cardinality($4::text[]),0)=0 OR observation.search_tags @> $4::text[])
           AND ($2='' OR observation.search_vector @@ catalog_core_lexical_query_v1($2))
      )
      SELECT id,tenant_id,source_id,snapshot_id,source_key,coalesce(identifier,''),coalesce(version,''),version_present,
             coalesce(kind,''),coalesce(media_type,''),completeness,compatibility,raw_entry_available,
	     exact_entry_digest,coalesce(identity_digest,semantic_digest),coalesce(normalized_entry_digest,''),coalesce(normalization_profile,''),coalesce(search_display_name,''),coalesce(search_description,''),coalesce(search_tags,'{}'),coalesce(search_representative_queries,'{}'),coalesce(search_publisher,''),freshness,first_seen_at,last_seen_at,result_score
        FROM matched
	       WHERE ($5=false OR result_score<$6 OR (result_score=$6 AND (coalesce(identifier,'') COLLATE "C">$7::text COLLATE "C" OR (coalesce(identifier,'')=$7 AND id>nullif($8,'')::uuid))))
	       ORDER BY result_score DESC,coalesce(identifier,'') COLLATE "C",id LIMIT $9`, tenantID, request.Query.Text, kinds, request.Query.Tags, request.AfterID != "", request.AfterScore, request.AfterIdentifier, request.AfterID, request.Limit)
		if err != nil {
			return nil, translateError("search observation page", err)
		}
		defer rows.Close()
		items := make([]federation.SearchResult, 0)
		for rows.Next() {
			item, scanErr := scanFederationObservationSearchResult(rows)
			if scanErr != nil {
				return nil, scanErr
			}
			items = append(items, item)
		}
		return items, translateError("iterate observation search page", rows.Err())
	})
}

// SearchIndexedCatalog performs filtering, conflict projection, scoring,
// deterministic ordering, facets, and page bounding inside one repeatable-read
// transaction; it never loads the tenant's observation corpus into Go.
func (p *PostgresFederationStore) SearchIndexedCatalog(ctx context.Context, tenantID string, request catalog.FederatedCatalogSearchRequest) (catalog.FederatedCatalogSearchPage, error) {
	return withTenantCollectionRead(ctx, p.repository.pool, tenantID, func(tx pgx.Tx) (catalog.FederatedCatalogSearchPage, error) {
		return searchIndexedCatalogTx(ctx, tx, tenantID, request)
	})
}

// searchIndexedCatalogTx validates cursor generation and performs indexed
// matching, totals and bounded result hydration within the caller's read snapshot.
func searchIndexedCatalogTx(ctx context.Context, tx pgx.Tx, tenantID string, request catalog.FederatedCatalogSearchRequest) (catalog.FederatedCatalogSearchPage, error) {
	generation, err := readCatalogSearchGeneration(ctx, tx, tenantID)
	if err != nil {
		return catalog.FederatedCatalogSearchPage{}, err
	}
	if request.ExpectedGeneration != "" && request.ExpectedGeneration != generation {
		return catalog.FederatedCatalogSearchPage{}, catalog.E(catalog.CodeInvalidArgument, "catalog cursor is stale")
	}
	if !request.Query.IncludesOrigin(catalog.SearchResultIndexed) || request.Query.Visibility != "" && request.Query.Visibility != catalog.VisibilityTenant {
		return catalog.FederatedCatalogSearchPage{Indexed: []catalog.CatalogSearchResult{}, Generation: generation}, nil
	}
	// Indexed selection has many bounded provenance subplans. LLVM compilation
	// can dominate even a no-match read. Keep these interactive queries
	// interpreted; SET LOCAL expires with the caller's transaction.
	if _, err := tx.Exec(ctx, indexedSearchPlanningSQL); err != nil {
		return catalog.FederatedCatalogSearchPage{}, translateError("configure indexed search", err)
	}
	exactIdentifier := catalog.CatalogSearchExactIdentifier(request.Query.Text)
	kinds := make([]string, 0, len(request.Query.Kinds))
	for _, kind := range request.Query.Kinds {
		kinds = append(kinds, string(kind))
	}
	tags := append([]string{}, request.Query.Tags...)
	parameters := []any{tenantID, request.Query.Text, exactIdentifier, kinds, tags, request.Query.Publisher, request.Query.License, append([]string{}, request.Query.Categories...)}
	indexedSearchCTE := collectionStatement(ctx, indexedCatalogSearchRecordsCTE)
	if !request.Query.IncludesOrigin(catalog.SearchResultLocal) {
		indexedSearchCTE = strings.Replace(indexedSearchCTE, "AND NOT (observation.version_present AND EXISTS (", "AND NOT (false AND observation.version_present AND EXISTS (", 1)
	}
	if request.IdentityProbes != nil {
		probes, err := identityProbeParameters(request.IdentityProbes)
		if err != nil {
			return catalog.FederatedCatalogSearchPage{}, err
		}
		indexedSearchCTE = strings.Replace(indexedSearchCTE, "WHERE observation.tenant_id=$1", "WHERE observation.tenant_id=$1 AND (observation.identifier,coalesce(observation.version,''),observation.version_present) IN (SELECT identifier,version,present FROM unnest($9::text[],$10::text[],$11::boolean[]) probe(identifier,version,present))", 1)
		matches, err := readLiveMatches(ctx, tx, indexedSearchCTE+"SELECT identifier,version,version_present FROM selected_identities LIMIT 51", append(parameters, probes...))
		return catalog.FederatedCatalogSearchPage{Generation: generation, Matches: matches}, err
	}
	if exact := request.ExactIdentity; exact != nil {
		// Add a fixed predicate at selection, not a Go-side page filter. The
		// ordinary search SQL and parameter layout remain unchanged.
		indexedSearchCTE = strings.Replace(indexedSearchCTE, "WHERE observation.tenant_id=$1", "WHERE observation.tenant_id=$1 AND observation.identifier=$9 AND coalesce(observation.version,'')=$10 AND observation.version_present=$11", 1)
		parameters = append(parameters, exact.Identifier, exact.Version, exact.VersionPresent)
	}
	var total int
	if err := tx.QueryRow(ctx, indexedSearchCTE+`SELECT count(*) FROM selected_identities`, parameters...).Scan(&total); err != nil {
		return catalog.FederatedCatalogSearchPage{}, translateError("count indexed catalog search", err)
	}
	// Only this repository is empty. Preserve the generation and let the
	// caller continue local/live composition after the shared snapshot closes.
	if total == 0 {
		return catalog.FederatedCatalogSearchPage{Indexed: []catalog.CatalogSearchResult{}, Generation: generation}, nil
	}
	afterPresent := request.After != nil
	var afterScore any = 0
	afterName, afterID := "", ""
	if request.After != nil {
		afterScore, afterName, afterID = request.After.Score, request.After.DisplayName, request.After.ResultID
	}
	if request.Query.Order != "" {
		afterScore = ""
		if request.After != nil {
			afterScore = request.After.SortValue
		}
	}
	limit := request.Limit
	if limit <= 0 {
		limit = 20
	}
	pageParameters := append([]any(nil), parameters...)
	pageParameters = append(pageParameters, afterPresent, afterScore, afterName, afterID, limit+1)
	pageSQL := indexedColumnOrderSQL(indexedCatalogSearchPageSQL, request.Query.Order)
	if request.ExactIdentity != nil {
		pageSQL = strings.NewReplacer("$13", "$16", "$12", "$15", "$11", "$14", "$10", "$13", "$9", "$12").Replace(pageSQL)
	}
	rows, err := tx.Query(ctx, indexedSearchCTE+pageSQL, pageParameters...)
	if err != nil {
		return catalog.FederatedCatalogSearchPage{}, translateError("query indexed catalog search", err)
	}
	items := make([]catalog.CatalogSearchResult, 0, limit+1)
	for rows.Next() {
		var item catalog.CatalogSearchResult
		var evidence catalog.IndexedSearchEvidence
		if err := rows.Scan(&item.Identifier, &item.Version, &item.VersionPresent, &item.Kind, &evidence.NormalizedEntryDigest,
			&item.DisplayName, &item.Description, &item.Tags, &item.Publisher, &item.ID, &item.Score, &evidence.Conflict,
			&evidence.SameSourceConflict, &evidence.SourceCount, &evidence.SourceIDs, &evidence.PresentationSourceID, &evidence.PresentationGeneration, &evidence.PresentationSourceRevision); err != nil {
			rows.Close()
			return catalog.FederatedCatalogSearchPage{}, translateError("scan indexed catalog search", err)
		}
		item.Type = catalog.SearchResultIndexed
		evidence.SourcesTruncated = evidence.SourceCount > len(evidence.SourceIDs)
		groupID, groupErr := federation.ObservationGroupID(item.Identifier, item.Version, item.VersionPresent)
		if groupErr != nil {
			rows.Close()
			return catalog.FederatedCatalogSearchPage{}, groupErr
		}
		evidence.ObservationGroupID = groupID
		item.Indexed = &evidence
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return catalog.FederatedCatalogSearchPage{}, translateError("iterate indexed catalog search", err)
	}
	rows.Close()
	hasMore := len(items) > limit
	if hasMore {
		items = items[:limit]
	}
	return catalog.FederatedCatalogSearchPage{Indexed: items, IndexedTotal: total, Generation: generation, HasMore: hasMore}, nil
}

// ListCatalogLiveSources selects bounded active live/hybrid non-referral-only
// sources, prioritizing the current policy digest and reporting truncation.
func (p *PostgresFederationStore) ListCatalogLiveSources(ctx context.Context, tenantID, policyDigest string, limit int) ([]federation.Source, bool, error) {
	if limit < 1 {
		return nil, false, catalog.E(catalog.CodeInvalidArgument, "live source limit must be positive")
	}
	type sourcePage struct {
		items     []federation.Source
		truncated bool
	}
	page, err := withTenantRead(ctx, p.repository.pool, tenantID, func(tx pgx.Tx) (sourcePage, error) {
		rows, err := tx.Query(ctx, `
      SELECT source.id::text
        FROM federation_sources source
        JOIN federation_source_revisions revision
          ON revision.tenant_id=source.tenant_id AND revision.source_id=source.id AND revision.revision=source.revision
       WHERE source.tenant_id=$1 AND source.status='active' AND revision.retrieval_mode IN ('live','hybrid')
         AND revision.configuration#>>'{live,enabled}'='true'
         AND coalesce(revision.configuration#>>'{live,referralOnly}','false')<>'true'
         AND coalesce(revision.configuration->>'registrySearchUrl','')<>''
       ORDER BY (coalesce(revision.configuration->>'policyDigest','')=$2) DESC,source.id LIMIT $3`, tenantID, policyDigest, limit+1)
		if err != nil {
			return sourcePage{}, translateError("select bounded live Catalog sources", err)
		}
		ids := make([]string, 0, limit+1)
		for rows.Next() {
			var sourceID string
			if err := rows.Scan(&sourceID); err != nil {
				rows.Close()
				return sourcePage{}, translateError("scan bounded live Catalog source", err)
			}
			ids = append(ids, sourceID)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return sourcePage{}, translateError("iterate bounded live Catalog sources", err)
		}
		rows.Close()
		truncated := len(ids) > limit
		if truncated {
			ids = ids[:limit]
		}
		items := make([]federation.Source, 0, len(ids))
		for _, sourceID := range ids {
			source, readErr := readFederationSource(ctx, tx, tenantID, sourceID, false)
			if readErr != nil {
				return sourcePage{}, readErr
			}
			items = append(items, source)
		}
		return sourcePage{items: items, truncated: truncated}, nil
	})
	return page.items, page.truncated, err
}

// scanFederationObservation decodes compatibility and copies retained
// exact/normalized bytes, rejecting observations with no exact evidence.
func scanFederationObservation(row scanner) (federation.Observation, error) {
	var item federation.Observation
	var compatibility, rawBytes, normalizedBytes []byte
	if err := row.Scan(&item.ID, &item.TenantID, &item.SourceID, &item.SnapshotID, &item.SourceKey, &item.Identifier, &item.Version,
		&item.VersionPresent, &item.Kind, &item.MediaType, &item.Completeness, &compatibility, &rawBytes, &normalizedBytes,
		&item.ExactEntryDigest, &item.IdentityDigest, &item.NormalizedEntryDigest, &item.NormalizationProfile, &item.SearchDisplayName, &item.SearchDescription, &item.SearchTags, &item.SearchQueries, &item.SearchPublisher, &item.Freshness, &item.FirstSeenAt, &item.LastSeenAt); err != nil {
		return federation.Observation{}, translateError("scan source observation", err)
	}
	if err := json.Unmarshal(compatibility, &item.Compatibility); err != nil {
		return federation.Observation{}, catalog.Wrap(catalog.CodeInternal, "decode observation compatibility", err)
	}
	item.RawEntryAvailable = len(rawBytes) > 0
	if !item.RawEntryAvailable {
		return federation.Observation{}, catalog.E(catalog.CodeInternal, "source observation lacks exact retained bytes")
	}
	item.RawEntry = append(json.RawMessage(nil), rawBytes...)
	item.NormalizedEntry = append(json.RawMessage(nil), normalizedBytes...)
	return item, nil
}

// scanFederationObservationSearchResult adds score/source to metadata without
// transferring retained documents from PostgreSQL.
func scanFederationObservationSearchResult(row scanner) (federation.SearchResult, error) {
	var score int
	item, err := scanFederationObservationMetadata(row, &score)
	if err != nil {
		return federation.SearchResult{}, err
	}
	return federation.SearchResult{Observation: item, Score: score, Source: item.SourceID}, nil
}

// GetObservationOccurrence resolves the exact
// tenant/observation/snapshot/retrieval/location provenance tuple.
func (p *PostgresFederationStore) GetObservationOccurrence(ctx context.Context, tenantID, observationID, snapshotID, retrievalDigest, entryLocation string) (federation.ObservationOccurrence, error) {
	return withTenantRead(ctx, p.repository.pool, tenantID, func(tx pgx.Tx) (federation.ObservationOccurrence, error) {
		return scanObservationOccurrence(tx.QueryRow(ctx, `
      SELECT observation_id,source_id,snapshot_id,coalesce(source_revision,0),retrieval_digest,entry_location,exact_entry_digest,
             coalesce(normalized_entry_digest,''),evidence_complete,observed_at,coalesce(normalization_input_digest,'')
      FROM observation_occurrences
      WHERE tenant_id=$1 AND observation_id=$2 AND snapshot_id=$3 AND retrieval_digest=$4 AND entry_location=$5`, tenantID, observationID, snapshotID, retrievalDigest, entryLocation))
	})
}

// ListObservationOccurrences returns a bounded newest-first occurrence list and a
// separately counted tenant-scoped total.
func (p *PostgresFederationStore) ListObservationOccurrences(ctx context.Context, tenantID, observationID string, limit int) ([]federation.ObservationOccurrence, int, error) {
	type occurrencePage struct {
		items []federation.ObservationOccurrence
		total int
	}
	page, err := withTenantRead(ctx, p.repository.pool, tenantID, func(tx pgx.Tx) (occurrencePage, error) {
		var total int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM observation_occurrences WHERE tenant_id=$1 AND observation_id=$2`, tenantID, observationID).Scan(&total); err != nil {
			return occurrencePage{}, translateError("count observation occurrences", err)
		}
		rows, err := tx.Query(ctx, `
      SELECT observation_id,source_id,snapshot_id,coalesce(source_revision,0),retrieval_digest,entry_location,exact_entry_digest,
             coalesce(normalized_entry_digest,''),evidence_complete,observed_at,coalesce(normalization_input_digest,'')
      FROM observation_occurrences WHERE tenant_id=$1 AND observation_id=$2
      ORDER BY observed_at DESC,snapshot_id DESC,retrieval_digest,entry_location LIMIT $3`, tenantID, observationID, limit)
		if err != nil {
			return occurrencePage{}, translateError("list observation occurrences", err)
		}
		defer rows.Close()
		items := make([]federation.ObservationOccurrence, 0)
		for rows.Next() {
			item, scanErr := scanObservationOccurrence(rows)
			if scanErr != nil {
				return occurrencePage{}, scanErr
			}
			items = append(items, item)
		}
		if err := rows.Err(); err != nil {
			return occurrencePage{}, translateError("iterate observation occurrences", err)
		}
		return occurrencePage{items: items, total: total}, nil
	})
	return page.items, page.total, err
}

// ListObservationOccurrencePage applies SQL continuation across time, snapshot,
// retrieval digest and entry location for one observation.
func (p *PostgresFederationStore) ListObservationOccurrencePage(ctx context.Context, tenantID string, request federation.OccurrenceListRequest) ([]federation.ObservationOccurrence, error) {
	return withTenantRead(ctx, p.repository.pool, tenantID, func(tx pgx.Tx) ([]federation.ObservationOccurrence, error) {
		rows, err := tx.Query(ctx, `
      SELECT observation_id,source_id,snapshot_id,coalesce(source_revision,0),retrieval_digest,entry_location,exact_entry_digest,
             coalesce(normalized_entry_digest,''),evidence_complete,observed_at,coalesce(normalization_input_digest,'')
      FROM observation_occurrences
      WHERE tenant_id=$1 AND observation_id=$2
        AND ($3::timestamptz IS NULL OR observed_at<$3 OR (observed_at=$3 AND (
          snapshot_id<nullif($4,'')::uuid OR (snapshot_id=nullif($4,'')::uuid AND (
		    retrieval_digest COLLATE "C">$5::text COLLATE "C" OR (retrieval_digest=$5 AND entry_location COLLATE "C">$6::text COLLATE "C"))))))
	      ORDER BY observed_at DESC,snapshot_id DESC,retrieval_digest COLLATE "C",entry_location COLLATE "C" LIMIT $7`, tenantID, request.ObservationID, request.AfterObservedAt, request.AfterSnapshotID, request.AfterRetrievalDigest, request.AfterEntryLocation, request.Limit)
		if err != nil {
			return nil, translateError("list observation occurrence page", err)
		}
		defer rows.Close()
		items := make([]federation.ObservationOccurrence, 0)
		for rows.Next() {
			item, scanErr := scanObservationOccurrence(rows)
			if scanErr != nil {
				return nil, scanErr
			}
			items = append(items, item)
		}
		return items, translateError("iterate observation occurrence page", rows.Err())
	})
}

// ObservationOccurrenceSummaries returns at most one count/latest tuple per
// selected group member. All counts and tuples share one statement snapshot.
func (p *PostgresFederationStore) ObservationOccurrenceSummaries(ctx context.Context, tenantID string, observationIDs []string) (map[string]federation.OccurrenceSummary, error) {
	if len(observationIDs) > federation.MaxObservationGroupContributions {
		return nil, catalog.E(catalog.CodeInvalidArgument, "observation summary selection exceeds the group bound")
	}
	if len(observationIDs) == 0 {
		return map[string]federation.OccurrenceSummary{}, nil
	}
	return withTenantRead(ctx, p.repository.pool, tenantID, func(tx pgx.Tx) (map[string]federation.OccurrenceSummary, error) {
		rows, err := tx.Query(ctx, `
      SELECT latest.* FROM unnest($2::uuid[]) AS selected(id)
      CROSS JOIN LATERAL (
        SELECT observation_id,source_id,snapshot_id,coalesce(source_revision,0),retrieval_digest,entry_location,exact_entry_digest,
               coalesce(normalized_entry_digest,''),evidence_complete,observed_at,coalesce(normalization_input_digest,''),count(*) OVER ()
          FROM observation_occurrences
         WHERE tenant_id=$1 AND observation_id=selected.id
         ORDER BY observed_at DESC,snapshot_id DESC,retrieval_digest COLLATE "C",entry_location COLLATE "C" LIMIT 1
      ) latest`, tenantID, observationIDs)
		if err != nil {
			return nil, translateError("summarize observation occurrences", err)
		}
		defer rows.Close()
		result := make(map[string]federation.OccurrenceSummary, len(observationIDs))
		for rows.Next() {
			var summary federation.OccurrenceSummary
			item := &summary.Latest
			if err := rows.Scan(&item.ObservationID, &item.SourceID, &item.SnapshotID, &item.SourceRevision, &item.RetrievalDigest, &item.EntryLocation,
				&item.ExactEntryDigest, &item.NormalizedEntryDigest, &item.EvidenceComplete, &item.ObservedAt, &item.NormalizationInputDigest, &summary.Total); err != nil {
				return nil, translateError("scan observation occurrence summary", err)
			}
			result[item.ObservationID] = summary
		}
		if err := rows.Err(); err != nil {
			return nil, translateError("iterate observation occurrence summaries", err)
		}
		for _, observationID := range observationIDs {
			if result[observationID].Total == 0 {
				return nil, catalog.E(catalog.CodeDependencyUnavailable, "observation occurrence evidence is missing")
			}
		}
		return result, nil
	})
}

// scanObservationOccurrence decodes the retained provenance tuple and
// normalization-input digest without fetching external content.
func scanObservationOccurrence(row scanner) (federation.ObservationOccurrence, error) {
	var item federation.ObservationOccurrence
	if err := row.Scan(&item.ObservationID, &item.SourceID, &item.SnapshotID, &item.SourceRevision, &item.RetrievalDigest, &item.EntryLocation, &item.ExactEntryDigest, &item.NormalizedEntryDigest, &item.EvidenceComplete, &item.ObservedAt, &item.NormalizationInputDigest); err != nil {
		return federation.ObservationOccurrence{}, translateError("scan observation occurrence", err)
	}
	return item, nil
}
