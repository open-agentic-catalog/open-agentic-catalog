// Memory maintenance selection uses the same policy fence as other private inventories.
package catalog

import (
	"context"
	"slices"
	"sort"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
)

// ListValidationMaintenance deduplicates exact current pointers and limits only
// after permission and state selection. It never invokes the validator/fetcher.
func (m *MemoryRepository) ListValidationMaintenance(ctx context.Context, tenant string, q ValidationMaintenanceRequest) ([]ValidationMaintenanceItem, error) {
	if identity.NeedsCollectionAdmission(ctx) {
		return identity.WithCollectionRead(ctx, func(c context.Context) ([]ValidationMaintenanceItem, error) {
			return m.ListValidationMaintenance(c, tenant, q)
		})
	}
	if err := identity.RequireCollectionTenant(ctx, tenant); err != nil {
		return nil, err
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	current := map[string]bool{}
	for _, v := range m.versions {
		if v.TenantID == tenant {
			current[v.CandidateRevisionID] = true
			current[v.SealedRevisionID] = true
		}
	}
	for _, l := range m.listings {
		if l.TenantID != tenant {
			continue
		}
		if revision, ok := m.listingRevisions[l.CandidateRevisionID]; ok && revision.TenantID == tenant {
			current[revision.VersionRevisionID] = true
		}
		if release, ok := m.releases[l.CurrentReleaseID]; ok && release.TenantID == tenant && (release.State == ReleasePublished || release.State == ReleaseDeprecated) {
			current[release.VersionRevisionID] = true
		}
	}
	ids := make([]string, 0, len(current))
	for id := range current {
		if id != "" && id > q.AfterID {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	items := []ValidationMaintenanceItem{}
	for _, id := range ids {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		revision, ok := m.versionRevisions[id]
		if !ok || revision.TenantID != tenant {
			return nil, E(CodeDependencyUnavailable, "current validation revision is missing")
		}
		version := m.versions[revision.ResourceVersionID]
		resource, ok := m.resources[version.ResourceID]
		if !ok || resource.TenantID != tenant || resource.DeletedAt != nil {
			continue
		}
		allowed := true
		for _, permission := range []identity.Permission{"resource.read", "resource.read_content", "resource.validate"} {
			permit, err := identity.CollectionAllows(ctx, permission, resource.NamespaceID)
			if err != nil {
				return nil, err
			}
			allowed = allowed && permit
		}
		if !allowed {
			continue
		}
		evidenceID := currentValidationEvidenceID(revision, m.validationRuns[id])
		e, ok := m.evidenceSets[evidenceID]
		if !ok || e.TenantID != tenant {
			return nil, E(CodeDependencyUnavailable, "current validation evidence is missing")
		}
		conditions := validationMaintenanceConditions(e, q)
		if e.SubjectDigest == revision.ContentSetDigest && (len(conditions) == 0 || q.State != "attention" && !slices.Contains(conditions, q.State)) {
			continue
		}
		if len(revision.Representations) < 1 || len(revision.Representations) > 16 {
			return nil, E(CodeDependencyUnavailable, "stored validation representations are invalid")
		}
		if err := ValidateExternalRetrievalEvidence(revision, e); err != nil {
			return nil, Wrap(CodeDependencyUnavailable, "stored validation bindings are invalid", err)
		}
		item, err := ProjectValidationMaintenance(resource, version, revision, e, q)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
		if len(items) == q.Limit {
			break
		}
	}
	return items, nil
}
