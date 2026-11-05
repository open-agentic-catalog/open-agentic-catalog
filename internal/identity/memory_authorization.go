package identity

import (
	"context"
	"slices"
	"strings"
	"sync/atomic"
)

type memoryAdmissionKey struct{}
type memoryAdmission struct {
	store  *MemoryStore
	active atomic.Bool
}

func memoryAdmissionFrom(ctx context.Context, store *MemoryStore) bool {
	guard, _ := ctx.Value(memoryAdmissionKey{}).(*memoryAdmission)
	return guard != nil && guard.store == store && guard.active.Load()
}

// CheckPermissions evaluates current assignments under one coherent memory lock.
func (m *MemoryStore) CheckPermissions(ctx context.Context, actor Actor, checks []PermissionCheck) (PermissionDecision, error) {
	if err := ValidatePermissionChecks(checks); err != nil {
		return PermissionDecision{}, err
	}
	if !memoryAdmissionFrom(ctx, m) {
		m.mu.RLock()
		defer m.mu.RUnlock()
	}
	if err := ctx.Err(); err != nil {
		return PermissionDecision{}, Wrap(CodeDependency, "permission check canceled", err)
	}
	return m.checkPermissionsLocked(actor, checks)
}
func (m *MemoryStore) checkPermissionsLocked(actor Actor, checks []PermissionCheck) (PermissionDecision, error) {
	if err := ValidatePermissionActor(actor); err != nil {
		return PermissionDecision{}, err
	}
	revision, exists := m.revisions[actor.TenantID]
	if !exists {
		return PermissionDecision{}, E(CodeDependency, "tenant authorization state is missing")
	}
	result := PermissionDecision{Revision: revision, Allowed: make([]bool, len(checks))}
	member, ok := m.memberships[actor.MembershipID]
	realm := m.realms[member.RealmID]
	if !ok || member.TenantID != actor.TenantID || member.RealmID != actor.RealmID || member.Subject != actor.Subject || member.ActorType != actor.ActorType || member.State != MembershipActive || realm.State != RealmActive {
		return result, nil
	}
	for _, assignment := range m.allAccessAssignmentsLocked() {
		if !m.effectiveAssignmentLocked(actor, assignment) {
			continue
		}
		permissions := m.definitions[assignment.RoleDefinitionID].Permissions
		for i, check := range checks {
			if (assignment.NamespaceID == "" || strings.EqualFold(assignment.NamespaceID, check.NamespaceID)) && slices.Contains(permissions, check.Permission) {
				result.Allowed[i] = true
			}
		}
	}
	return result, nil
}

// WithPermissionAdmission protects a local operation against concurrent authority
// changes. The callback is synchronous; its context does not retain admission
// after return. This is not a multi-repository transaction or rollback engine.
func (m *MemoryStore) WithPermissionAdmission(ctx context.Context, actor Actor, checks []PermissionCheck, operation func(context.Context) error) error {
	if err := ValidatePermissionChecks(checks); err != nil {
		return err
	}
	if operation == nil {
		return E(CodeInvalidArgument, "admitted operation is required")
	}
	var bindErr error
	ctx, bindErr = BindPermissionActor(ctx, actor)
	if bindErr != nil {
		return bindErr
	}
	if !memoryAdmissionFrom(ctx, m) {
		m.mu.RLock()
		defer m.mu.RUnlock()
	}
	if err := ctx.Err(); err != nil {
		return Wrap(CodeDependency, "permission admission canceled", err)
	}
	decision, err := m.checkPermissionsLocked(actor, checks)
	if err != nil {
		return err
	}
	for _, allowed := range decision.Allowed {
		if !allowed {
			return E(CodeForbidden, "current permission denied the operation")
		}
	}
	SetPermissionRevision(ctx, decision.Revision)
	if memoryAdmissionFrom(ctx, m) {
		return operation(ctx)
	}
	guard := &memoryAdmission{store: m}
	guard.active.Store(true)
	defer guard.active.Store(false)
	return operation(context.WithValue(ctx, memoryAdmissionKey{}, guard))
}
func (m *MemoryStore) checkAdministratorLocked(ctx context.Context, tenantID string) error {
	actor, ok := FromContext(ctx)
	if !ok {
		return nil
	} // Explicit provisioning/store callers retain their owner boundary.
	if actor.TenantID != tenantID {
		return E(CodeForbidden, "authorization administration crossed tenant boundary")
	}
	decision, err := m.checkPermissionsLocked(actor, []PermissionCheck{{Permission: "access.manage"}})
	if err != nil {
		return err
	}
	if !decision.Allowed[0] {
		return E(CodeForbidden, "current access administration permission is required")
	}
	SetPermissionRevision(ctx, decision.Revision)
	return nil
}

var _ PermissionStore = (*MemoryStore)(nil)
