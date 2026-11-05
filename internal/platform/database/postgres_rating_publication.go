package database

import (
	"context"
	"encoding/json"

	"github.com/jackc/pgx/v5"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/publication"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/ratings"
)

// Activation locks the audience choice and each published aggregate until commit.
// A concurrent withdrawal therefore happens before this check or after publication.
// Already published immutable copies are not recalled by a subsequent withdrawal.
func checkPublicationRatingConsent(ctx context.Context, tx pgx.Tx, snapshot publication.Snapshot) error {
	claims := map[string]*ratings.Summary{}
	for _, entry := range snapshot.ARD.Entries {
		if entry.Ratings != nil {
			release, _ := entry.Metadata["catalogReleaseId"].(string)
			if release == "" {
				return catalog.E(catalog.CodePublicationNotReady, "rated release is missing")
			}
			claims[release] = entry.Ratings
		}
	}
	for _, entry := range snapshot.AICatalog.Entries {
		if value, ok := entry.Extensions[ratings.IRI]; ok {
			raw, err := json.Marshal(value)
			if err != nil {
				return catalog.E(catalog.CodePublicationNotReady, "invalid retained rating summary")
			}
			var claim ratings.Summary
			if json.Unmarshal(raw, &claim) != nil {
				return catalog.E(catalog.CodePublicationNotReady, "invalid retained rating summary")
			}
			metadata, _ := entry.Extensions["https://ai-catalog.org/extensions/metadata"].(map[string]any)
			release, _ := metadata["catalogReleaseId"].(string)
			if release == "" {
				return catalog.E(catalog.CodePublicationNotReady, "rated release is missing")
			}
			claims[release] = &claim
		}
	}
	if len(claims) == 0 {
		return nil
	}
	var enabled bool
	err := tx.QueryRow(ctx, `SELECT enabled FROM rating_sharing WHERE tenant_id=$1 AND host=$2 AND audience=$3 FOR SHARE`, snapshot.TenantID, snapshot.Host, snapshot.Audience).Scan(&enabled)
	if err != nil || !enabled {
		return catalog.E(catalog.CodePublicationNotReady, "rating sharing is no longer enabled")
	}
	ids := make([]string, 0, len(claims))
	for release := range claims {
		ids = append(ids, release)
	}
	rows, err := tx.Query(ctx, `SELECT release.id::text,target.shared_count,target.shared_sum FROM listing_releases release JOIN catalog_listings listing ON listing.tenant_id=release.tenant_id AND listing.id=release.listing_id JOIN rating_targets target ON target.tenant_id=release.tenant_id AND target.resource_id=listing.resource_id AND target.version_revision_id=release.version_revision_id WHERE release.tenant_id=$1 AND release.id=ANY($2::uuid[]) ORDER BY target.target_key FOR SHARE OF target`, snapshot.TenantID, ids)
	if err != nil {
		return translateError("lock published rating totals", err)
	}
	defer rows.Close()
	seen := 0
	for rows.Next() {
		var release string
		var count, sum int64
		if err = rows.Scan(&release, &count, &sum); err != nil {
			return translateError("read published rating totals", err)
		}
		claim := claims[release]
		if claim == nil || count < 5 || claim.Count != count || claim.Sum != sum {
			return catalog.E(catalog.CodePublicationNotReady, "rating consent or totals changed")
		}
		seen++
	}
	if err = rows.Err(); err != nil {
		return translateError("iterate published rating totals", err)
	}
	if seen != len(claims) {
		return catalog.E(catalog.CodePublicationNotReady, "rated release is no longer available")
	}
	return nil
}
