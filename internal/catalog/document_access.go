package catalog

import (
	"context"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
)

// DocumentAccessReader selects one live, same-tenant resource reference with
// current content authority. A content digest is never itself a permission.
type DocumentAccessReader interface {
	DocumentAccessNamespace(context.Context, string, string) (string, error)
}

func (m *MemoryRepository) DocumentAccessNamespace(ctx context.Context, tenant, document string) (string, error) {
	// Do not acquire identity authority while holding the repository lock:
	// admitted writes acquire those locks in the opposite order.
	m.mu.RLock()
	scopes := map[string]struct{}{}
	for _, revision := range m.versionRevisions {
		if revision.TenantID != tenant {
			continue
		}
		version := m.versions[revision.ResourceVersionID]
		resource := m.resources[version.ResourceID]
		if resource.TenantID != tenant || resource.DeletedAt != nil {
			continue
		}
		for _, representation := range revision.Representations {
			if representation.ArtifactID == document {
				scopes[resource.NamespaceID] = struct{}{}
			}
		}
	}
	m.mu.RUnlock()
	for scope := range scopes {
		if _, err := identity.RequirePermission(ctx, "resource.read_content", scope); err == nil {
			return scope, nil
		} else if identity.ErrorCodeOf(err) != identity.CodeForbidden {
			return "", PermissionError("current content permission is required", err)
		}
	}
	return "", E(CodeNotFound, "catalog document was not found")
}
