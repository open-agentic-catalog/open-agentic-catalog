package database

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/audit"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/federation"
)

func (p *PostgresFederationStore) SaveArtworkOverride(ctx context.Context, value federation.ArtworkOverride, expected, sourceRevision int64, evidence federation.MutationEvidence) (federation.ArtworkOverride, error) {
	if err := federation.ValidateArtworkOverrideIntegrity(value); err != nil {
		return federation.ArtworkOverride{}, err
	}
	return withTenantTx(ctx, p.repository.pool, value.TenantID, pgx.TxOptions{}, func(tx pgx.Tx) (federation.ArtworkOverride, error) {
		source, err := readFederationSource(ctx, tx, value.TenantID, value.Target.SourceID, true)
		if err != nil {
			return federation.ArtworkOverride{}, err
		}
		if source.Status == federation.SourceRemoved || source.Revision != sourceRevision || federation.EffectivePresentationGeneration(source) != value.Target.PresentationGeneration {
			return federation.ArtworkOverride{}, catalog.E(catalog.CodePreconditionFailed, "source presentation changed")
		}
		key := federation.ArtworkTargetKey(value.Target)
		var revision int64
		var beforeDigest string
		err = tx.QueryRow(ctx, `SELECT head.revision,retained.digest FROM source_artwork_heads head JOIN source_artwork_revisions retained ON retained.tenant_id=head.tenant_id AND retained.id=head.current_revision_id WHERE head.tenant_id=$1 AND head.source_id=$2 AND head.presentation_generation=$3 AND head.target_key=$4 FOR UPDATE OF head`, value.TenantID, value.Target.SourceID, value.Target.PresentationGeneration, key).Scan(&revision, &beforeDigest)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return federation.ArtworkOverride{}, translateError("lock artwork override", err)
		}
		if revision != expected || value.Revision != expected+1 {
			return federation.ArtworkOverride{}, catalog.E(catalog.CodePreconditionFailed, "artwork revision does not match")
		}
		if err = federation.ValidateArtworkMutationEvidence(value, beforeDigest, evidence); err != nil {
			return federation.ArtworkOverride{}, err
		}
		raw, err := json.Marshal(value)
		if err != nil {
			return federation.ArtworkOverride{}, err
		}
		_, err = tx.Exec(ctx, `INSERT INTO source_artwork_revisions(tenant_id,id,source_id,presentation_generation,target_key,identifier,version_present,version,kind,revision,document,digest,created_by,created_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)`, value.TenantID, value.ID, value.Target.SourceID, value.Target.PresentationGeneration, key, value.Target.Identifier, value.Target.VersionPresent, value.Target.Version, value.Target.Kind, value.Revision, raw, value.Digest, value.CreatedBy, value.CreatedAt)
		if err != nil {
			return federation.ArtworkOverride{}, translateError("append artwork revision", err)
		}
		if revision == 0 {
			_, err = tx.Exec(ctx, `INSERT INTO source_artwork_heads(tenant_id,source_id,presentation_generation,target_key,revision,current_revision_id) VALUES($1,$2,$3,$4,$5,$6)`, value.TenantID, value.Target.SourceID, value.Target.PresentationGeneration, key, value.Revision, value.ID)
		} else {
			_, err = tx.Exec(ctx, `UPDATE source_artwork_heads SET revision=$5,current_revision_id=$6 WHERE tenant_id=$1 AND source_id=$2 AND presentation_generation=$3 AND target_key=$4`, value.TenantID, value.Target.SourceID, value.Target.PresentationGeneration, key, value.Revision, value.ID)
		}
		if err != nil {
			return federation.ArtworkOverride{}, translateError("advance artwork head", err)
		}
		if err = insertFederationEvidence(ctx, tx, value.TenantID, value.ID, []federation.MutationEvidence{evidence}, audit.ActionSourceArtworkRevised); err != nil {
			return federation.ArtworkOverride{}, err
		}
		return value, nil
	})
}
func (p *PostgresFederationStore) GetArtworkOverride(ctx context.Context, tenant string, target federation.ArtworkTarget) (federation.ArtworkOverride, error) {
	return withTenantRead(ctx, p.repository.pool, tenant, func(tx pgx.Tx) (federation.ArtworkOverride, error) {
		var raw []byte
		err := tx.QueryRow(ctx, `SELECT revision.document FROM source_artwork_heads head JOIN source_artwork_revisions revision ON revision.tenant_id=head.tenant_id AND revision.id=head.current_revision_id WHERE head.tenant_id=$1 AND head.source_id=$2 AND head.presentation_generation=$3 AND head.target_key=$4`, tenant, target.SourceID, target.PresentationGeneration, federation.ArtworkTargetKey(target)).Scan(&raw)
		if err != nil {
			return federation.ArtworkOverride{}, translateError("read artwork override", err)
		}
		var result federation.ArtworkOverride
		err = json.Unmarshal(raw, &result)
		if err == nil {
			err = federation.ValidateArtworkOverrideIntegrity(result)
		}
		return result, err
	})
}
func (p *PostgresFederationStore) ListArtworkOverrideHistory(ctx context.Context, tenant string, target federation.ArtworkTarget, after string, limit int) ([]federation.ArtworkOverride, error) {
	if limit < 1 || limit > 101 {
		return nil, catalog.E(catalog.CodeInvalidArgument, "artwork history exceeds its bound")
	}
	return withTenantRead(ctx, p.repository.pool, tenant, func(tx pgx.Tx) ([]federation.ArtworkOverride, error) {
		rows, err := tx.Query(ctx, `SELECT document FROM source_artwork_revisions WHERE tenant_id=$1 AND source_id=$2 AND presentation_generation=$3 AND target_key=$4 AND ($5='' OR revision>(SELECT cursor.revision FROM source_artwork_revisions cursor WHERE cursor.tenant_id=$1 AND cursor.source_id=$2 AND cursor.presentation_generation=$3 AND cursor.target_key=$4 AND cursor.id=nullif($5,'')::uuid)) ORDER BY revision LIMIT $6`, tenant, target.SourceID, target.PresentationGeneration, federation.ArtworkTargetKey(target), after, limit)
		if err != nil {
			return nil, translateError("read artwork history", err)
		}
		defer rows.Close()
		result := []federation.ArtworkOverride{}
		for rows.Next() {
			var raw []byte
			var value federation.ArtworkOverride
			if err = rows.Scan(&raw); err != nil {
				return nil, err
			}
			if err = json.Unmarshal(raw, &value); err != nil {
				return nil, err
			}
			if err = federation.ValidateArtworkOverrideIntegrity(value); err != nil {
				return nil, err
			}
			result = append(result, value)
		}
		return result, rows.Err()
	})
}
func nullableArtworkMetadata(raw json.RawMessage) any {
	if len(raw) == 0 {
		return nil
	}
	return raw
}
func (p *PostgresFederationStore) ArtworkReadBatch(ctx context.Context, tenant string, requests []federation.ArtworkReadRequest) (map[federation.ArtworkTarget]federation.ArtworkReadContext, error) {
	if len(requests) > 100 {
		return nil, catalog.E(catalog.CodeInvalidArgument, "artwork batch exceeds its bound")
	}
	type selection struct {
		SourceID       string `json:"sourceId"`
		Generation     int64  `json:"generation"`
		TargetKey      string `json:"targetKey"`
		Identifier     string `json:"identifier"`
		Version        string `json:"version"`
		VersionPresent bool   `json:"versionPresent"`
		Kind           string `json:"kind"`
		Digest         string `json:"digest"`
		Live           bool   `json:"live"`
	}
	selected := make([]selection, len(requests))
	for i, request := range requests {
		selected[i] = selection{request.Target.SourceID, request.Target.PresentationGeneration, federation.ArtworkTargetKey(request.Target), request.Target.Identifier, request.Target.Version, request.Target.VersionPresent, string(request.Target.Kind), request.EntryDigest, request.Live}
	}
	raw, err := json.Marshal(selected)
	if err != nil {
		return nil, err
	}
	return withTenantCollectionRead(ctx, p.repository.pool, tenant, func(tx pgx.Tx) (map[federation.ArtworkTarget]federation.ArtworkReadContext, error) {
		rows, err := tx.Query(ctx, `WITH requested AS (SELECT r."sourceId",r.generation,r."targetKey",r.identifier,r.version,r."versionPresent",r.kind,r.digest,r.live FROM jsonb_to_recordset($2::jsonb) AS r("sourceId" text,generation bigint,"targetKey" text,identifier text,version text,"versionPresent" boolean,kind text,digest text,live boolean))
 SELECT requested."sourceId",requested.generation,requested.identifier,requested.version,requested."versionPresent",requested.kind,
 revision.configuration,source.status,source.revision,art.document,observation.artwork_metadata,coalesce(observation.normalized_entry_digest,''),
 (requested.live OR observation.id IS NOT NULL) AS eligible
 FROM requested JOIN federation_sources source ON source.tenant_id=$1 AND source.id=requested."sourceId"::uuid
 JOIN federation_source_revisions revision ON revision.tenant_id=source.tenant_id AND revision.source_id=source.id AND revision.revision=source.revision
 LEFT JOIN source_artwork_heads head ON head.tenant_id=$1 AND head.source_id=source.id AND head.presentation_generation=requested.generation AND head.target_key=requested."targetKey"
 LEFT JOIN source_artwork_revisions art ON art.tenant_id=head.tenant_id AND art.id=head.current_revision_id
 LEFT JOIN LATERAL(SELECT obs.id,obs.artwork_metadata,obs.normalized_entry_digest FROM observations obs WHERE NOT requested.live AND obs.tenant_id=$1 AND obs.source_id=source.id
 AND obs.identifier=requested.identifier AND coalesce(obs.version,'')=requested.version AND obs.version_present=requested."versionPresent" AND obs.kind=requested.kind AND (requested.digest='' OR obs.normalized_entry_digest=requested.digest)
 AND obs.freshness='current' AND obs.completeness='complete' AND obs.compatibility->>'structural'='valid' AND obs.compatibility->>'support'='supported'
 AND NOT (coalesce(revision.configuration#>'{scope,excludedIdentifiers}','[]') @> jsonb_build_array(obs.identifier))
 AND (coalesce(revision.configuration#>'{scope,identifiers}','[]')='[]'::jsonb OR revision.configuration#>'{scope,identifiers}' @> jsonb_build_array(obs.identifier))
 AND (coalesce(revision.configuration#>'{scope,versions}','[]')='[]'::jsonb OR revision.configuration#>'{scope,versions}' @> jsonb_build_array(coalesce(obs.version,'')))
 AND (coalesce(revision.configuration#>'{scope,kinds}','[]')='[]'::jsonb OR revision.configuration#>'{scope,kinds}' @> jsonb_build_array(obs.kind))
 AND (coalesce(revision.configuration#>'{scope,publishers}','[]')='[]'::jsonb OR revision.configuration#>'{scope,publishers}' @> jsonb_build_array(obs.search_publisher))
 AND (coalesce(revision.configuration#>'{scope,tags}','[]')='[]'::jsonb OR to_jsonb(obs.search_tags) @> (revision.configuration#>'{scope,tags}'))
 AND revision.retrieval_mode IN ('indexed','hybrid') LIMIT 1) observation ON true
 WHERE source.status IN ('active','error') AND coalesce((revision.configuration->>'presentationGeneration')::bigint,1)=requested.generation`, tenant, raw)
		if err != nil {
			return nil, translateError("read selected artwork contexts", err)
		}
		defer rows.Close()
		result := make(map[federation.ArtworkTarget]federation.ArtworkReadContext)
		for rows.Next() {
			var target federation.ArtworkTarget
			var read federation.ArtworkReadContext
			var config, overlay, candidates []byte
			var status federation.SourceStatus
			var revision int64
			if err = rows.Scan(&target.SourceID, &target.PresentationGeneration, &target.Identifier, &target.Version, &target.VersionPresent, &target.Kind, &config, &status, &revision, &overlay, &candidates, &read.EntryDigest, &read.Eligible); err != nil {
				return nil, err
			}
			if err = json.Unmarshal(config, &read.Source); err != nil {
				return nil, err
			}
			read.Source.Status = status
			read.Source.Revision = revision
			read.Source.PresentationGeneration = federation.EffectivePresentationGeneration(read.Source)
			if len(overlay) > 0 {
				var value federation.ArtworkOverride
				if err = json.Unmarshal(overlay, &value); err != nil {
					return nil, err
				}
				if err = federation.ValidateArtworkOverrideIntegrity(value); err != nil {
					return nil, err
				}
				read.Override = &value
			}
			if len(candidates) > 0 {
				if err = json.Unmarshal(candidates, &read.Candidates); err != nil {
					return nil, err
				}
			}
			result[target] = read
		}
		return result, rows.Err()
	})
}

func (p *PostgresFederationStore) ArtworkOverrideCount(ctx context.Context, tenant, source string, generation int64, limit int) (int, bool, error) {
	type countResult struct {
		count    int
		overflow bool
	}
	result, err := withTenantRead(ctx, p.repository.pool, tenant, func(tx pgx.Tx) (countResult, error) {
		var count int
		err := tx.QueryRow(ctx, `SELECT count(*) FROM (SELECT 1 FROM source_artwork_heads WHERE tenant_id=$1 AND source_id=$2 AND presentation_generation=$3 LIMIT $4) bounded`, tenant, source, generation, limit+1).Scan(&count)
		return countResult{min(count, limit), count > limit}, err
	})
	return result.count, result.overflow, err
}

func (p *PostgresFederationStore) HasPublishedIdentity(ctx context.Context, tenant, identifier, version string) (bool, error) {
	return p.repository.HasPublishedIdentity(ctx, tenant, identifier, version)
}
