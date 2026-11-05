package database

import (
	"context"
	"encoding/json"

	"github.com/jackc/pgx/v5"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/federation"
)

// Retained overlay revisions are authority, unlike rebuildable image candidates.
// Each bounded page verifies document facts and current-head relation integrity.
func scanRestoreSourceArtwork(ctx context.Context, tx pgx.Tx, tenant, after string, limit int) ([]restoreRecord, bool, error) {
	limit = boundedStageLimit(limit, 16)
	rows, err := tx.Query(ctx, `SELECT id::text,octet_length(document::text)::bigint FROM source_artwork_revisions WHERE tenant_id=$1 AND ($2='' OR id>nullif($2,'')::uuid) ORDER BY id LIMIT $3`, tenant, after, limit+1)
	if err != nil {
		return nil, false, err
	}
	type position struct {
		id   string
		size int64
	}
	positions := []position{}
	for rows.Next() {
		var p position
		if err = rows.Scan(&p.id, &p.size); err != nil {
			rows.Close()
			return nil, false, err
		}
		positions = append(positions, p)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return nil, false, err
	}
	rows.Close()
	more := len(positions) > limit
	if more {
		positions = positions[:limit]
	}
	result := make([]restoreRecord, 0, len(positions))
	for _, p := range positions {
		record := restoreRecord{key: p.id, decodedBytes: p.size}
		invalid := p.size > 49152
		if invalid {
			record.decodedBytes = 0
		} else {
			var raw []byte
			var binding bool
			err = tx.QueryRow(ctx, `SELECT revision.document,
 revision.document->>'id'=revision.id::text AND revision.document->>'tenantId'=revision.tenant_id::text AND revision.document->>'digest'=revision.digest AND revision.document->>'revision'=revision.revision::text AND revision.document#>>'{target,sourceId}'=revision.source_id::text AND revision.document#>>'{target,presentationGeneration}'=revision.presentation_generation::text AND revision.document#>>'{target,identifier}'=revision.identifier AND revision.document#>>'{target,kind}'=revision.kind AND (revision.document#>>'{target,versionPresent}')::boolean=revision.version_present AND coalesce(revision.document#>>'{target,version}','')=revision.version AND
 EXISTS(SELECT 1 FROM source_artwork_heads current_head WHERE current_head.tenant_id=revision.tenant_id AND current_head.source_id=revision.source_id AND current_head.presentation_generation=revision.presentation_generation AND current_head.target_key=revision.target_key AND current_head.revision=(SELECT latest.revision FROM source_artwork_revisions latest WHERE latest.tenant_id=revision.tenant_id AND latest.source_id=revision.source_id AND latest.presentation_generation=revision.presentation_generation AND latest.target_key=revision.target_key ORDER BY latest.revision DESC LIMIT 1)) AND
 NOT EXISTS(SELECT 1 FROM source_artwork_heads head WHERE head.tenant_id=revision.tenant_id AND head.current_revision_id=revision.id AND (head.source_id<>revision.source_id OR head.presentation_generation<>revision.presentation_generation OR head.target_key<>revision.target_key OR head.revision<>revision.revision))
 FROM source_artwork_revisions revision WHERE revision.tenant_id=$1 AND revision.id=$2`, tenant, p.id).Scan(&raw, &binding)
			if err != nil {
				return nil, false, err
			}
			var value federation.ArtworkOverride
			invalid = !binding || json.Unmarshal(raw, &value) != nil || federation.ValidateArtworkOverrideIntegrity(value) != nil
		}
		if invalid {
			record.finding = integrityFinding("RESTORE_SOURCE_ARTWORK_INVALID", "source_artwork", p.id, "disable affected presentation and investigate immutable artwork revision bindings")
		}
		result = append(result, record)
	}
	return result, more, nil
}
