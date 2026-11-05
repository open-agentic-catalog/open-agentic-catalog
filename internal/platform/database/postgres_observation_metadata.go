// Observation metadata reads retain disclosure and evidence-presence checks
// without transferring exact or normalized documents from PostgreSQL.
package database

import (
	"context"
	"encoding/json"

	"github.com/jackc/pgx/v5"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/federation"
)

const observationMetadataColumns = `id,tenant_id,source_id,snapshot_id,source_key,coalesce(identifier,''),coalesce(version,''),version_present,
  coalesce(kind,''),coalesce(media_type,''),completeness,compatibility,coalesce(octet_length(raw_entry_bytes)>0,false),
  exact_entry_digest,coalesce(identity_digest,semantic_digest),coalesce(normalized_entry_digest,''),coalesce(normalization_profile,''),
  coalesce(search_display_name,''),coalesce(search_description,''),coalesce(search_tags,'{}'),coalesce(search_representative_queries,'{}'),
  coalesce(search_publisher,''),freshness,first_seen_at,last_seen_at`

// GetObservationMetadata checks the same tenant parent and compatibility as an
// exact read. Presence is not a fresh integrity verification of retained bytes.
func (p *PostgresFederationStore) GetObservationMetadata(ctx context.Context, tenantID, observationID string) (federation.Observation, error) {
	return withTenantRead(ctx, p.repository.pool, tenantID, func(tx pgx.Tx) (federation.Observation, error) {
		return scanFederationObservationMetadata(tx.QueryRow(ctx, `SELECT `+observationMetadataColumns+` FROM observations WHERE tenant_id=$1 AND id=$2`, tenantID, observationID), nil)
	})
}

// scanFederationObservationMetadata optionally includes the scored Search
// column while sharing metadata decoding and the existing missing-evidence guard.
func scanFederationObservationMetadata(row scanner, score *int) (federation.Observation, error) {
	var item federation.Observation
	var compatibility []byte
	fields := []any{&item.ID, &item.TenantID, &item.SourceID, &item.SnapshotID, &item.SourceKey, &item.Identifier, &item.Version,
		&item.VersionPresent, &item.Kind, &item.MediaType, &item.Completeness, &compatibility, &item.RawEntryAvailable,
		&item.ExactEntryDigest, &item.IdentityDigest, &item.NormalizedEntryDigest, &item.NormalizationProfile, &item.SearchDisplayName,
		&item.SearchDescription, &item.SearchTags, &item.SearchQueries, &item.SearchPublisher, &item.Freshness, &item.FirstSeenAt, &item.LastSeenAt}
	if score != nil {
		fields = append(fields, score)
	}
	if err := row.Scan(fields...); err != nil {
		return federation.Observation{}, translateError("scan observation metadata", err)
	}
	if err := json.Unmarshal(compatibility, &item.Compatibility); err != nil {
		return federation.Observation{}, catalog.Wrap(catalog.CodeInternal, "decode observation compatibility", err)
	}
	if !item.RawEntryAvailable {
		return federation.Observation{}, catalog.E(catalog.CodeInternal, "source observation lacks exact retained bytes")
	}
	return item, nil
}
