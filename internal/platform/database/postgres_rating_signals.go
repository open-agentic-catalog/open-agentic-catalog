package database

import (
	"context"
	"encoding/json"
	"github.com/jackc/pgx/v5"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/federation"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/ratings"
	"net/url"
)

// RatingSignals selects compact peer claims and exact authorized import matches
// in one bounded query. It never counts installations or hydrates native files.
func (p *PostgresFederationStore) RatingSignals(ctx context.Context, tenant string, requests []federation.RatingSignalTarget) (map[string]federation.RatingSignals, error) {
	if len(requests) > 100 {
		return nil, catalog.E(catalog.CodeInvalidArgument, "rating signals page exceeds its bound")
	}
	raw, err := json.Marshal(requests)
	if err != nil {
		return nil, err
	}
	return withTenantCollectionRead(ctx, p.repository.pool, tenant, func(tx pgx.Tx) (map[string]federation.RatingSignals, error) {
		rows, err := tx.Query(ctx, `WITH requested AS(SELECT r.target,r."entryDigest" AS digest FROM jsonb_to_recordset($2::jsonb) AS r(target jsonb,"entryDigest" text))
 SELECT requested.target,source.endpoint,peer.ratings_metadata,imported.id,imported.changed FROM requested
 JOIN federation_sources source ON source.tenant_id=$1 AND source.id=(requested.target->>'sourceId')::uuid AND source.status IN ('active','error')
 JOIN federation_source_revisions current_source ON current_source.tenant_id=source.tenant_id AND current_source.source_id=source.id AND current_source.revision=source.revision AND coalesce((current_source.configuration->>'presentationGeneration')::bigint,1)=(requested.target->>'presentationGeneration')::bigint
 LEFT JOIN LATERAL(SELECT ratings_metadata FROM observations obs WHERE obs.tenant_id=$1 AND obs.source_id=source.id AND obs.identifier=requested.target->>'identifier' AND obs.version_present=(requested.target->>'versionPresent')::boolean AND coalesce(obs.version,'')=coalesce(requested.target->>'version','') AND obs.kind=requested.target->>'kind' AND obs.normalized_entry_digest=requested.digest AND obs.freshness='current' AND obs.completeness='complete' AND obs.compatibility->>'structural'='valid' AND obs.compatibility->>'support'='supported' ORDER BY obs.id LIMIT 1)peer ON true
 LEFT JOIN LATERAL(SELECT min(matches.id::text) AS id,bool_or(matches.changed) AS changed FROM (
 SELECT resource.id, bool_or(revision.adoption_provenance->>'normalizedEntryDigest'<>requested.digest AND requested.digest<>'') AS changed
 FROM resources resource JOIN resource_versions version ON version.tenant_id=resource.tenant_id AND version.resource_id=resource.id
 JOIN resource_version_revisions revision ON revision.tenant_id=version.tenant_id AND revision.resource_version_id=version.id
 JOIN observations original ON original.tenant_id=revision.tenant_id AND original.id=(revision.adoption_provenance->>'observationId')::uuid AND original.source_id=source.id
 JOIN federation_source_revisions retained_source ON retained_source.tenant_id=revision.tenant_id AND retained_source.source_id=source.id AND retained_source.revision=(revision.adoption_provenance->>'sourceRevision')::bigint
 WHERE resource.tenant_id=$1 AND resource.deleted_at IS NULL AND resource.canonical_identifier=requested.target->>'identifier' AND resource.kind=requested.target->>'kind'
 AND revision.adoption_provenance->>'sourceId'=source.id::text AND coalesce((retained_source.configuration->>'presentationGeneration')::bigint,1)=(requested.target->>'presentationGeneration')::bigint
 AND original.identifier=requested.target->>'identifier' AND original.version_present=(requested.target->>'versionPresent')::boolean AND coalesce(original.version,'')=coalesce(requested.target->>'version','') AND original.kind=requested.target->>'kind'
 AND `+collectionPermissionSQL("resource.read", "resource.namespace_id")+`
 GROUP BY resource.id LIMIT 2) matches HAVING count(*)=1) imported ON true`, tenant, raw)
		if err != nil {
			return nil, translateError("read selected rating signals", err)
		}
		defer rows.Close()
		out := map[string]federation.RatingSignals{}
		for rows.Next() {
			var target ratings.Target
			var rawTarget, peer []byte
			var endpoint string
			var imported *string
			var changed *bool
			if err = rows.Scan(&rawTarget, &endpoint, &peer, &imported, &changed); err != nil {
				return nil, translateError("scan rating signals", err)
			}
			if err = json.Unmarshal(rawTarget, &target); err != nil {
				return nil, err
			}
			signal := federation.RatingSignals{SourceEndpoint: endpoint}
			if len(peer) > 0 {
				u, parseErr := url.Parse(endpoint)
				if parseErr == nil {
					signal.Peer = ratings.DecodeSummary(peer, "https://"+u.Host, target.Identifier, target.VersionPresent, target.Version)
				}
			}
			if imported != nil {
				signal.ImportedResourceID = *imported
			}
			if changed != nil {
				signal.ChangedEntry = *changed
			}
			out[ratings.Key(target)] = signal
		}
		return out, translateError("iterate rating signals", rows.Err())
	})
}
