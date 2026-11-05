// Collection reads project only metadata in the selection transaction.
package database

import (
	"context"
	"encoding/json"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/artwork"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
)

const catalogSummaryReadSQL = `
 SELECT r.id,r.tenant_id,r.namespace_id,r.canonical_identifier,r.kind,
        v.id,v.version,vr.id,vr.content_set_digest,e.id,l.id,l.namespace_id,
        display.id,display.display_name,display.description,display.tags,display.publisher_id,display.visibility,
        lr.id,lr.version_revision_id,lr.listing_revision_id,lr.state,lr.activated_at,
        e.digest,e.state,e.trust->>'state',validation.expires_at,e.validation_profile,e.external_retrievals,e.rights->>'state',
        coalesce(publisher.display_name,''),coalesce(namespace.display_name,''),
        CASE WHEN char_length(display.license)<=256 THEN display.license ELSE '' END,char_length(display.license)>256,
        CASE WHEN octet_length((display.extensions->'https://github.com/open-agentic-catalog/open-agentic-catalog/schemas/artwork/v1#descriptor')::text)<=32768 THEN display.extensions->'https://github.com/open-agentic-catalog/open-agentic-catalog/schemas/artwork/v1#descriptor' ELSE NULL END
 FROM listing_releases lr
 JOIN catalog_listings l ON l.tenant_id=lr.tenant_id AND l.current_release_id=lr.id
 JOIN resources r ON r.tenant_id=l.tenant_id AND r.id=l.resource_id AND r.deleted_at IS NULL
 JOIN resource_version_revisions vr ON vr.tenant_id=lr.tenant_id AND vr.id=lr.version_revision_id
 JOIN resource_versions v ON v.tenant_id=vr.tenant_id AND v.id=vr.resource_version_id
 JOIN listing_revisions display ON display.tenant_id=lr.tenant_id AND display.id=lr.listing_revision_id
 LEFT JOIN publishers publisher ON publisher.tenant_id=display.tenant_id AND publisher.id=display.publisher_id
 LEFT JOIN namespaces namespace ON namespace.tenant_id=l.tenant_id AND namespace.id=l.namespace_id
 JOIN LATERAL (
   SELECT run.evidence_set_id,run.expires_at FROM version_validation_runs run
   WHERE run.tenant_id=vr.tenant_id AND run.version_revision_id=vr.id
   ORDER BY run.run_number DESC LIMIT 1
 ) validation ON true
 JOIN evidence_sets e ON e.tenant_id=vr.tenant_id AND e.id=validation.evidence_set_id
 WHERE lr.tenant_id=$1 AND lr.id=ANY($2::uuid[]) AND lr.state IN ('published','deprecated')`

const catalogSummaryBindingsSQL = `
 SELECT version_revision_id,id,coalesce(external_url,''),media_type,content_digest,size_bytes,
        document_blob_id IS NOT NULL OR plugin_inspection IS NOT NULL
 FROM representations WHERE tenant_id=$1 AND version_revision_id=ANY($2::uuid[]) ORDER BY id`

// readSearchRecords preserves selected identities and external retrieval
// bindings without reading findings, documents, package inventory or extensions.
func readSearchRecords(ctx context.Context, tx pgx.Tx, tenantID string, releaseIDs []string) (map[string]*catalog.CatalogSummary, error) {
	items := make(map[string]*catalog.CatalogSummary, len(releaseIDs))
	if len(releaseIDs) == 0 {
		return items, nil
	}
	rows, err := tx.Query(ctx, catalogSummaryReadSQL, tenantID, releaseIDs)
	if err != nil {
		return nil, translateError("read search summaries", err)
	}
	defer rows.Close()
	versionIDs := make([]string, 0, len(releaseIDs))
	evidence := make(map[string]catalog.EvidenceSet)
	for rows.Next() {
		var item catalog.CatalogSummary
		item.Display = new(catalog.CatalogPresentation)
		var expiry *time.Time
		var value catalog.EvidenceSet
		var retrievals, artworkRaw []byte
		if err := rows.Scan(&item.Resource.ID, &item.Resource.TenantID, &item.Resource.NamespaceID, &item.Resource.CanonicalIdentifier, &item.Resource.Kind,
			&item.Version.ID, &item.Version.Version, &item.VersionRevision.ID, &item.VersionRevision.ContentSetDigest, &item.VersionRevision.EvidenceSetID, &item.Listing.ID, &item.Listing.NamespaceID,
			&item.ListingRevision.ID, &item.ListingRevision.DisplayName, &item.ListingRevision.Description, &item.ListingRevision.Tags, &item.ListingRevision.PublisherID, &item.ListingRevision.Visibility,
			&item.Release.ID, &item.Release.VersionRevisionID, &item.Release.ListingRevisionID, &item.Release.State, &item.Release.ActivatedAt,
			&item.Evidence.Digest, &item.Evidence.State, &item.Evidence.Trust.State, &expiry, &value.ValidationProfile, &retrievals, &item.Evidence.LegalRights.State, &item.Display.PublisherName, &item.Display.NamespaceName, &item.Display.License, &item.Display.LicenseOmitted, &artworkRaw); err != nil {
			return nil, translateError("scan search summary", err)
		}
		item.Source = "local"
		var descriptor any
		if len(artworkRaw) > 0 && json.Unmarshal(artworkRaw, &descriptor) == nil {
			item.Artwork = catalog.ListingArtwork(map[string]any{artwork.IRI: descriptor})
		}
		item.Evidence.ID = item.VersionRevision.EvidenceSetID
		if expiry != nil {
			item.Evidence.ExpiresAt = *expiry
		}
		if item.ListingRevision.Tags == nil {
			item.ListingRevision.Tags = []string{}
		}
		value.State = item.Evidence.State
		value.ExpiresAt = item.Evidence.ExpiresAt
		if err := unmarshalJSON(retrievals, &value.ExternalRetrievals); err != nil {
			return nil, err
		}
		if _, exists := evidence[item.VersionRevision.ID]; !exists {
			versionIDs = append(versionIDs, item.VersionRevision.ID)
		}
		evidence[item.VersionRevision.ID] = value
		items[item.Release.ID] = &item
	}
	if err := rows.Err(); err != nil {
		return nil, translateError("iterate search summaries", err)
	}
	rows.Close()
	for _, id := range releaseIDs {
		if items[id] == nil {
			return nil, translateError("read catalog summary", pgx.ErrNoRows)
		}
	}
	rows, err = tx.Query(ctx, catalogSummaryBindingsSQL, tenantID, versionIDs)
	if err != nil {
		return nil, translateError("read search retrieval bindings", err)
	}
	defer rows.Close()
	bindings := make(map[string][]catalog.ExternalRepresentationBinding)
	for rows.Next() {
		var revisionID string
		var value catalog.ExternalRepresentationBinding
		if err := rows.Scan(&revisionID, &value.ID, &value.URL, &value.MediaType, &value.ContentDigest, &value.Size, &value.Retained); err != nil {
			return nil, translateError("scan search retrieval binding", err)
		}
		if _, exists := evidence[revisionID]; !exists {
			return nil, catalog.E(catalog.CodeDependencyUnavailable, "stored search representation integrity check failed")
		}
		if len(bindings[revisionID]) >= 16 {
			return nil, catalog.E(catalog.CodeDependencyUnavailable, "stored search representation count is invalid")
		}
		bindings[revisionID] = append(bindings[revisionID], value)
	}
	if err := rows.Err(); err != nil {
		return nil, translateError("iterate search retrieval bindings", err)
	}
	rows.Close()
	for _, id := range versionIDs {
		if len(bindings[id]) == 0 {
			return nil, catalog.E(catalog.CodeDependencyUnavailable, "stored search representations are missing")
		}
		if err := catalog.ValidateExternalRetrievalBindings(bindings[id], evidence[id]); err != nil {
			return nil, catalog.Wrap(catalog.CodeDependencyUnavailable, "stored external retrieval evidence does not match its version revision", err)
		}
	}
	return items, nil
}
