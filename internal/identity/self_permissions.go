package identity

import (
	"context"
	"slices"
)

// SelfAccessRequest selects a bounded page of the caller's effective scopes.
// NamespaceID is an exact self lookup, never a grant or an existence oracle.
type SelfAccessRequest struct {
	NamespaceID           string
	AfterNamespaceID      string
	Limit                 int
	AuthorizationRevision int64
}

type NamespacePermissionAccess struct {
	NamespaceID string       `json:"namespaceId"`
	Permissions []Permission `json:"permissions"`
}

// Permissions apply tenant-wide. AvailablePermissions only hints that an
// action is available somewhere; it cannot authorize a particular namespace.
type SelfAccessProjection struct {
	AuthorizationRevision int64                       `json:"authorizationRevision"`
	Permissions           []Permission                `json:"permissions"`
	AvailablePermissions  []Permission                `json:"availablePermissions"`
	NamespaceGrants       []NamespacePermissionAccess `json:"namespaceGrants"`
}

type SelfAccessStore interface {
	ReadSelfAccess(context.Context, Actor, SelfAccessRequest) (SelfAccessProjection, error)
}

func ValidateSelfAccessRequest(request SelfAccessRequest) error {
	if request.Limit < 1 || request.Limit > 101 || request.AuthorizationRevision < 1 || request.NamespaceID != "" && !ValidTenantID(request.NamespaceID) || request.AfterNamespaceID != "" && !ValidTenantID(request.AfterNamespaceID) {
		return E(CodeInvalidArgument, "self-access page is invalid")
	}
	return nil
}

// CurrentSelfAccess uses the authenticated store, not hydrated Actor grants.
// The repository checks activity and the policy revision in its read snapshot.
func CurrentSelfAccess(ctx context.Context, request SelfAccessRequest) (SelfAccessProjection, error) {
	ctx, actor, err := WithCollectionContext(ctx)
	if err != nil {
		return SelfAccessProjection{}, err
	}
	request.AuthorizationRevision, _ = CollectionRevision(ctx)
	if err := ValidateSelfAccessRequest(request); err != nil {
		return SelfAccessProjection{}, err
	}
	store, ok := actor.permissionStore.(SelfAccessStore)
	if !ok {
		return SelfAccessProjection{}, E(CodeDependency, "self-access projection is unavailable")
	}
	return store.ReadSelfAccess(ctx, actor, request)
}

func sortedPermissions(set map[Permission]bool) []Permission {
	result := make([]Permission, 0, len(set))
	for permission := range set {
		result = append(result, permission)
	}
	slices.Sort(result)
	return result
}

func (m *MemoryStore) ReadSelfAccess(ctx context.Context, actor Actor, request SelfAccessRequest) (SelfAccessProjection, error) {
	if err := ValidateSelfAccessRequest(request); err != nil {
		return SelfAccessProjection{}, err
	}
	var result SelfAccessProjection
	err := m.WithCollectionAdmission(ctx, actor, request.AuthorizationRevision, func(context.Context) error {
		global, available := map[Permission]bool{}, map[Permission]bool{}
		namespaces := map[string]map[Permission]bool{}
		for _, assignment := range m.allAccessAssignmentsLocked() {
			if !m.effectiveAssignmentLocked(actor, assignment) {
				continue
			}
			for _, permission := range m.definitions[assignment.RoleDefinitionID].Permissions {
				available[permission] = true
				if assignment.NamespaceID == "" {
					global[permission] = true
				} else {
					if namespaces[assignment.NamespaceID] == nil {
						namespaces[assignment.NamespaceID] = map[Permission]bool{}
					}
					namespaces[assignment.NamespaceID][permission] = true
				}
			}
		}
		ids := []string{}
		for namespaceID := range namespaces {
			if namespaceID > request.AfterNamespaceID && (request.NamespaceID == "" || namespaceID == request.NamespaceID) {
				ids = append(ids, namespaceID)
			}
		}
		slices.Sort(ids)
		if len(ids) > request.Limit {
			ids = ids[:request.Limit]
		}
		result = SelfAccessProjection{AuthorizationRevision: request.AuthorizationRevision, Permissions: sortedPermissions(global), AvailablePermissions: sortedPermissions(available), NamespaceGrants: []NamespacePermissionAccess{}}
		for _, namespaceID := range ids {
			for permission := range global {
				namespaces[namespaceID][permission] = true
			}
			result.NamespaceGrants = append(result.NamespaceGrants, NamespacePermissionAccess{NamespaceID: namespaceID, Permissions: sortedPermissions(namespaces[namespaceID])})
		}
		return nil
	})
	return result, err
}
