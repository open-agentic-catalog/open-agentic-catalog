package identity

import (
	"context"
	"slices"
	"strings"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/safeerr"
)

func (m *MemoryStore) initializeAuthorizationDefinitions(at time.Time) {
	m.definitions = map[string]RoleDefinition{}
	m.groups = map[string]AccessGroup{}
	m.groupMembers = map[string]GroupMember{}
	m.accessAssignments = map[string]AccessAssignment{}
	for tenant := range m.revisions {
		for key := range builtinPermissions {
			permissions, _ := BuiltinPermissions(key)
			id := deterministicUUID("definition", tenant, key)
			m.definitions[id] = RoleDefinition{ID: id, TenantID: tenant, Key: key, Builtin: true, Name: strings.ReplaceAll(key, "_", " "), State: DefinitionActive, Revision: 1, Permissions: append([]Permission{}, permissions...), NamespaceCapable: NamespaceCapable(permissions), CreatedAt: at, UpdatedAt: at}
		}
	}
}
func cloneDefinition(r RoleDefinition) RoleDefinition {
	r.Permissions = slices.Clone(r.Permissions)
	if r.Permissions == nil {
		r.Permissions = []Permission{}
	}
	return r
}
func cloneAccessAssignment(value AccessAssignment) AccessAssignment {
	if value.RevokedAt != nil {
		at := *value.RevokedAt
		value.RevokedAt = &at
	}
	return value
}
func cloneGroupMember(value GroupMember) GroupMember {
	if value.RevokedAt != nil {
		at := *value.RevokedAt
		value.RevokedAt = &at
	}
	return value
}
func (m *MemoryStore) allAccessAssignmentsLocked() []AccessAssignment {
	items := make([]AccessAssignment, 0, len(m.assignments)+len(m.accessAssignments))
	for _, a := range m.assignments {
		member := m.memberships[a.MembershipID]
		items = append(items, AccessAssignment{ID: a.ID, TenantID: a.TenantID, RealmID: member.RealmID, RoleDefinitionID: deterministicUUID("definition", a.TenantID, string(a.Role)), MembershipID: a.MembershipID, NamespaceID: a.NamespaceID, State: a.State, Revision: a.Revision, GrantedBy: a.GrantedBy, GrantReason: a.GrantReason, GrantedAt: a.GrantedAt, RevokedBy: a.RevokedBy, RevokeReason: a.RevokeReason, RevokedAt: a.RevokedAt})
	}
	for _, a := range m.accessAssignments {
		items = append(items, cloneAccessAssignment(a))
	}
	return items
}
func (m *MemoryStore) effectiveAssignmentLocked(actor Actor, a AccessAssignment) bool {
	if a.TenantID != actor.TenantID || a.State != AssignmentActive || m.definitions[a.RoleDefinitionID].State != DefinitionActive {
		return false
	}
	if a.MembershipID != "" {
		return a.MembershipID == actor.MembershipID
	}
	group := m.groups[a.GroupID]
	if group.State != GroupActive || group.RealmID != actor.RealmID {
		return false
	}
	for _, member := range m.groupMembers {
		if member.GroupID == a.GroupID && member.MembershipID == actor.MembershipID && member.State == AssignmentActive {
			return true
		}
	}
	return false
}
func (m *MemoryStore) validateCurrentPrincipalLocked(actor Actor) error {
	if err := ValidatePermissionActor(actor); err != nil {
		return err
	}
	member, ok := m.memberships[actor.MembershipID]
	realm := m.realms[member.RealmID]
	if !ok || member.TenantID != actor.TenantID || member.RealmID != actor.RealmID || member.Subject != actor.Subject || member.ActorType != actor.ActorType || member.State != MembershipActive || realm.State != RealmActive {
		return E(CodeUnauthenticated, "current Catalog principal is inactive")
	}
	return nil
}
func memoryAuthorizationRead[T any](ctx context.Context, m *MemoryStore, actor Actor, request *AuthorizationPageRequest, own bool, read func() (T, error)) (T, error) {
	var zero T
	if _, err := BindPermissionActor(ctx, actor); err != nil {
		return zero, err
	}
	if request != nil {
		if err := ValidateAuthorizationPage(*request); err != nil {
			return zero, err
		}
	}
	if !memoryAdmissionFrom(ctx, m) {
		m.mu.RLock()
		defer m.mu.RUnlock()
	}
	if err := ctx.Err(); err != nil {
		return zero, Wrap(CodeDependency, "authorization read canceled", err)
	}
	if err := m.validateCurrentPrincipalLocked(actor); err != nil {
		return zero, err
	}
	revision, exists := m.revisions[actor.TenantID]
	if !exists {
		return zero, E(CodeDependency, "authorization state is missing")
	}
	if request != nil && revision != request.AuthorizationRevision {
		return zero, E(CodeConflict, "authorization changed; restart this page")
	}
	if !own {
		decision, err := m.checkPermissionsLocked(actor, []PermissionCheck{{Permission: "access.manage"}})
		if err != nil {
			return zero, err
		}
		if !decision.Allowed[0] {
			return zero, E(CodeForbidden, "current access administration permission is required")
		}
	}
	return read()
}
func memoryAuthorizationWrite[T any](ctx context.Context, m *MemoryStore, actor Actor, write func() (T, error)) (T, error) {
	var zero T
	ctx, err := BindPermissionActor(ctx, actor)
	if err != nil {
		return zero, err
	}
	if memoryAdmissionFrom(ctx, m) {
		return zero, E(CodeConflict, "authorization administration cannot upgrade shared admission")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return zero, Wrap(CodeDependency, "authorization write canceled", err)
	}
	if err := m.validateCurrentPrincipalLocked(actor); err != nil {
		return zero, err
	}
	if err := m.checkAdministratorLocked(ctx, actor.TenantID); err != nil {
		return zero, err
	}
	return write()
}
func memoryPage[T any](items []T, limit int, id func(T) string) []T {
	slices.SortFunc(items, func(a, b T) int { return strings.Compare(id(a), id(b)) })
	if len(items) > limit {
		items = items[:limit]
	}
	return items
}
func (m *MemoryStore) appendAuthorizationEvidence(ctx context.Context, change AdministrationChange, action, target string, facts map[string]any, effective bool) error {
	record, err := AdministrationRecord(change, action, target, facts)
	if err != nil {
		return err
	}
	e := change.Evidence
	record.Details["authorizationDigest"] = DecisionDigest(ctx)
	e.Details = record.Details
	if m.evidence != nil {
		// The policy writer already holds exclusive admission. Join it for the
		// synchronous evidence sink, including self-revocation.
		guard := &currentAdmission{}
		guard.active.Store(true)
		defer guard.active.Store(false)
		ctx = context.WithValue(ctx, currentAdmissionKey{}, guard)
		if err := m.evidence.AppendIdentityEvidence(ctx, change.Actor.TenantID, e); err != nil {
			return Wrap(CodeDependency, "persist authorization audit", err)
		}
	}
	m.audit = append(m.audit, e)
	if effective {
		m.revisions[change.Actor.TenantID]++
	}
	return nil
}
func (m *MemoryStore) ListDefinitions(ctx context.Context, a Actor, r AuthorizationPageRequest) ([]RoleDefinition, error) {
	return memoryAuthorizationRead(ctx, m, a, &r, false, func() ([]RoleDefinition, error) {
		items := []RoleDefinition{}
		for _, d := range m.definitions {
			if d.TenantID == a.TenantID && d.ID > r.AfterID && (r.Name == "" || d.Name == r.Name) && (r.State == "" || string(d.State) == r.State) && (r.BuiltinKey == "" || d.Builtin && d.Key == r.BuiltinKey) {
				items = append(items, cloneDefinition(d))
			}
		}
		return memoryPage(items, r.Limit, func(d RoleDefinition) string { return d.ID }), nil
	})
}
func (m *MemoryStore) GetDefinition(ctx context.Context, a Actor, id string) (RoleDefinition, error) {
	return memoryAuthorizationRead(ctx, m, a, nil, false, func() (RoleDefinition, error) {
		d, ok := m.definitions[id]
		if !ok || d.TenantID != a.TenantID {
			return RoleDefinition{}, E(CodeNotFound, "role definition was not found")
		}
		return cloneDefinition(d), nil
	})
}
func (m *MemoryStore) SaveDefinition(ctx context.Context, p SaveDefinitionParams) (RoleDefinition, error) {
	return memoryAuthorizationWrite(ctx, m, p.Actor, func() (RoleDefinition, error) {
		if !p.Archive {
			if err := validateDisplay(p.Input.Name, p.Input.Description); err != nil {
				return RoleDefinition{}, err
			}
			values, err := ValidateCustomPermissions(p.Input.Permissions)
			if err != nil {
				return RoleDefinition{}, err
			}
			p.Input.Permissions = values
		}
		before, exists := m.definitions[p.ID]
		if exists && before.TenantID != p.Actor.TenantID {
			return RoleDefinition{}, E(CodeNotFound, "role definition was not found")
		}
		if exists && before.Builtin {
			return RoleDefinition{}, E(CodeConflict, "built-in roles are immutable")
		}
		if p.ExpectedRevision == 0 && exists {
			return RoleDefinition{}, E(CodeConflict, "role definition already exists")
		}
		if p.ExpectedRevision > 0 && !exists {
			return RoleDefinition{}, E(CodeNotFound, "role definition was not found")
		}
		if exists && before.Revision != p.ExpectedRevision {
			return RoleDefinition{}, E(CodePreconditionFailed, "role revision does not match")
		}
		if exists && (before.State != DefinitionActive || p.At.Before(before.UpdatedAt)) {
			return RoleDefinition{}, E(CodeConflict, "role cannot be changed in this state or time")
		}
		after := RoleDefinition{ID: p.ID, TenantID: p.Actor.TenantID, Name: p.Input.Name, Description: p.Input.Description, State: DefinitionActive, Revision: 1, Permissions: slices.Clone(p.Input.Permissions), NamespaceCapable: NamespaceCapable(p.Input.Permissions), CreatedAt: p.At, UpdatedAt: p.At}
		action := "authorization.role.created"
		if exists {
			after.Revision = before.Revision + 1
			after.CreatedAt = before.CreatedAt
			action = "authorization.role.updated"
		}
		if p.Archive {
			if !exists {
				return RoleDefinition{}, E(CodeNotFound, "role definition was not found")
			}
			after = cloneDefinition(before)
			after.State = DefinitionArchived
			after.Revision++
			after.UpdatedAt = p.At
			action = "authorization.role.archived"
		}
		for _, assignment := range m.allAccessAssignmentsLocked() {
			if assignment.RoleDefinitionID == p.ID && assignment.State == AssignmentActive && (p.Archive || assignment.NamespaceID != "" && !after.NamespaceCapable) {
				return RoleDefinition{}, E(CodeConflict, "active assignments conflict with this role change")
			}
		}
		effective := !exists || p.Archive || !slices.Equal(before.Permissions, after.Permissions)
		if err := m.appendAuthorizationEvidence(ctx, p.AdministrationChange, action, "role_definition", map[string]any{"before": cloneDefinition(before), "after": cloneDefinition(after)}, effective); err != nil {
			return RoleDefinition{}, err
		}
		m.definitions[p.ID] = after
		return cloneDefinition(after), nil
	})
}
func (m *MemoryStore) ListGroups(ctx context.Context, a Actor, r AuthorizationPageRequest) ([]AccessGroup, error) {
	return memoryAuthorizationRead(ctx, m, a, &r, false, func() ([]AccessGroup, error) {
		items := []AccessGroup{}
		for _, g := range m.groups {
			if g.TenantID == a.TenantID && g.RealmID == a.RealmID && g.ID > r.AfterID && (r.Name == "" || g.Name == r.Name) && (r.State == "" || string(g.State) == r.State) {
				items = append(items, g)
			}
		}
		return memoryPage(items, r.Limit, func(g AccessGroup) string { return g.ID }), nil
	})
}
func (m *MemoryStore) GetGroup(ctx context.Context, a Actor, id string) (AccessGroup, error) {
	return memoryAuthorizationRead(ctx, m, a, nil, false, func() (AccessGroup, error) { return m.currentRealmGroupLocked(a, id) })
}
func (m *MemoryStore) currentRealmGroupLocked(a Actor, id string) (AccessGroup, error) {
	g, ok := m.groups[id]
	if !ok || g.TenantID != a.TenantID || g.RealmID != a.RealmID {
		return AccessGroup{}, E(CodeNotFound, "group was not found in the current realm")
	}
	return g, nil
}
func (m *MemoryStore) SaveGroup(ctx context.Context, p SaveGroupParams) (AccessGroup, error) {
	return memoryAuthorizationWrite(ctx, m, p.Actor, func() (AccessGroup, error) {
		before, exists := m.groups[p.ID]
		if exists && (before.TenantID != p.Actor.TenantID || before.RealmID != p.Actor.RealmID) {
			return AccessGroup{}, E(CodeNotFound, "group was not found")
		}
		if p.ExpectedRevision == 0 && exists {
			return AccessGroup{}, E(CodeConflict, "group already exists")
		}
		if p.ExpectedRevision > 0 && !exists {
			return AccessGroup{}, E(CodeNotFound, "group was not found")
		}
		if exists && before.Revision != p.ExpectedRevision {
			return AccessGroup{}, E(CodePreconditionFailed, "group revision does not match")
		}
		if exists && before.State == GroupArchived {
			return AccessGroup{}, E(CodeConflict, "group cannot be changed in this state or time")
		}
		if exists && p.At.Before(before.UpdatedAt) {
			return AccessGroup{}, safeerr.At("authorization.group.time_regression", E(CodeConflict, "group cannot be changed in this state or time"))
		}
		if p.Transition == "" {
			if err := validateDisplay(p.Input.Name, p.Input.Description); err != nil {
				return AccessGroup{}, err
			}
		}
		after := AccessGroup{ID: p.ID, TenantID: p.Actor.TenantID, RealmID: p.Actor.RealmID, Name: p.Input.Name, Description: p.Input.Description, State: GroupActive, Revision: 1, CreatedAt: p.At, UpdatedAt: p.At}
		action := "authorization.group.created"
		if exists {
			after = before
			after.Name = p.Input.Name
			after.Description = p.Input.Description
			after.Revision++
			after.UpdatedAt = p.At
			action = "authorization.group.updated"
		}
		if p.Transition != "" {
			if !exists || p.Transition == GroupSuspended && before.State != GroupActive || p.Transition == GroupActive && before.State != GroupSuspended || !slices.Contains([]GroupState{GroupActive, GroupSuspended, GroupArchived}, p.Transition) {
				return AccessGroup{}, E(CodeConflict, "group transition is invalid")
			}
			after.Name = before.Name
			after.Description = before.Description
			after.State = p.Transition
			switch p.Transition {
			case GroupActive:
				action = "authorization.group.reactivated"
			case GroupSuspended:
				action = "authorization.group.suspended"
			case GroupArchived:
				action = "authorization.group.archived"
			}
		}
		if p.Transition == GroupArchived {
			for _, member := range m.groupMembers {
				if member.TenantID == p.Actor.TenantID && member.GroupID == p.ID && member.State == AssignmentActive {
					return AccessGroup{}, E(CodeConflict, "revoke active members before group archival")
				}
			}
			for _, assignment := range m.accessAssignments {
				if assignment.GroupID == p.ID && assignment.State == AssignmentActive {
					return AccessGroup{}, E(CodeConflict, "revoke active assignments before group archival")
				}
			}
		}
		if err := m.appendAuthorizationEvidence(ctx, p.AdministrationChange, action, "access_group", map[string]any{"before": before, "after": after}, !exists || after.State != before.State); err != nil {
			return AccessGroup{}, err
		}
		m.groups[p.ID] = after
		return after, nil
	})
}
func (m *MemoryStore) ListGroupMembers(ctx context.Context, a Actor, r AuthorizationPageRequest) ([]GroupMember, error) {
	return memoryAuthorizationRead(ctx, m, a, &r, false, func() ([]GroupMember, error) {
		if _, err := m.currentRealmGroupLocked(a, r.GroupID); err != nil {
			return nil, err
		}
		items := []GroupMember{}
		for _, member := range m.groupMembers {
			if member.GroupID == r.GroupID && member.ID > r.AfterID {
				items = append(items, cloneGroupMember(member))
			}
		}
		return memoryPage(items, r.Limit, func(member GroupMember) string { return member.ID }), nil
	})
}
func (m *MemoryStore) ChangeGroupMember(ctx context.Context, p ChangeGroupMemberParams) (GroupMember, error) {
	return memoryAuthorizationWrite(ctx, m, p.Actor, func() (GroupMember, error) {
		group, err := m.currentRealmGroupLocked(p.Actor, p.GroupID)
		if err != nil {
			return GroupMember{}, err
		}
		if group.State == GroupArchived {
			return GroupMember{}, E(CodeConflict, "group is archived")
		}
		if p.Revoke {
			member, ok := m.groupMembers[p.ID]
			if !ok || member.GroupID != group.ID {
				return GroupMember{}, E(CodeNotFound, "group member was not found")
			}
			if member.Revision != p.ExpectedRevision {
				return GroupMember{}, E(CodePreconditionFailed, "group member revision does not match")
			}
			if member.State != AssignmentActive {
				return GroupMember{}, E(CodeConflict, "group member cannot be revoked")
			}
			if p.At.Before(member.AddedAt) {
				return GroupMember{}, safeerr.At("authorization.group_member.time_regression", E(CodeConflict, "group member cannot be revoked"))
			}
			before := member
			member.State = AssignmentRevoked
			member.Revision++
			member.RevokedBy = p.Actor.Subject
			member.RevokeReason = p.Evidence.Reason
			member.RevokedAt = &p.At
			if err := m.appendAuthorizationEvidence(ctx, p.AdministrationChange, "authorization.group_member.revoked", "group_member", map[string]any{"before": before, "after": member}, true); err != nil {
				return GroupMember{}, err
			}
			m.groupMembers[p.ID] = member
			return cloneGroupMember(member), nil
		}
		member, ok := m.memberships[p.MembershipID]
		if !ok || member.TenantID != p.Actor.TenantID || member.RealmID != group.RealmID {
			return GroupMember{}, E(CodeNotFound, "membership was not found in the group realm")
		}
		if member.State != MembershipActive || group.State != GroupActive {
			return GroupMember{}, E(CodeConflict, "group and membership must be active")
		}
		for _, existing := range m.groupMembers {
			if existing.GroupID == group.ID && existing.MembershipID == member.ID && existing.State == AssignmentActive {
				return GroupMember{}, E(CodeConflict, "membership already belongs to this group")
			}
		}
		if _, exists := m.groupMembers[p.ID]; exists {
			return GroupMember{}, E(CodeConflict, "group member identity already exists")
		}
		value := GroupMember{ID: p.ID, TenantID: p.Actor.TenantID, RealmID: group.RealmID, GroupID: group.ID, MembershipID: member.ID, State: AssignmentActive, Revision: 1, AddedBy: p.Actor.Subject, AddReason: p.Evidence.Reason, AddedAt: p.At}
		if err := m.appendAuthorizationEvidence(ctx, p.AdministrationChange, "authorization.group_member.added", "group_member", map[string]any{"after": value}, true); err != nil {
			return GroupMember{}, err
		}
		m.groupMembers[p.ID] = value
		return value, nil
	})
}
func (m *MemoryStore) ListAccessAssignments(ctx context.Context, a Actor, r AuthorizationPageRequest) ([]AccessAssignment, error) {
	return memoryAuthorizationRead(ctx, m, a, &r, false, func() ([]AccessAssignment, error) {
		if r.RoleDefinitionID != "" {
			d, ok := m.definitions[r.RoleDefinitionID]
			if !ok || d.TenantID != a.TenantID {
				return nil, E(CodeNotFound, "role definition was not found")
			}
		}
		if r.GroupID != "" {
			if _, err := m.currentRealmGroupLocked(a, r.GroupID); err != nil {
				return nil, err
			}
		}
		items := []AccessAssignment{}
		for _, v := range m.allAccessAssignmentsLocked() {
			if v.TenantID == a.TenantID && v.ID > r.AfterID && (r.RoleDefinitionID == "" || r.RoleDefinitionID == v.RoleDefinitionID) && (r.MembershipID == "" || r.MembershipID == v.MembershipID) && (r.GroupID == "" || r.GroupID == v.GroupID) {
				items = append(items, cloneAccessAssignment(v))
			}
		}
		return memoryPage(items, r.Limit, func(v AccessAssignment) string { return v.ID }), nil
	})
}
func (m *MemoryStore) GrantAccessAssignment(ctx context.Context, p GrantAccessAssignmentParams) (AccessAssignment, error) {
	return memoryAuthorizationWrite(ctx, m, p.Actor, func() (AccessAssignment, error) {
		d, ok := m.definitions[p.Input.RoleDefinitionID]
		if !ok || d.TenantID != p.Actor.TenantID {
			return AccessAssignment{}, E(CodeNotFound, "role definition was not found")
		}
		if d.State != DefinitionActive {
			return AccessAssignment{}, E(CodeConflict, "role is archived")
		}
		if (p.Input.MembershipID == "") == (p.Input.GroupID == "") {
			return AccessAssignment{}, E(CodeInvalidArgument, "exactly one assignment target is required")
		}
		if p.Input.NamespaceID != "" && (!ValidTenantID(p.Input.NamespaceID) || !d.NamespaceCapable) {
			return AccessAssignment{}, E(CodeInvalidArgument, "role cannot be assigned at this namespace scope")
		}
		if p.Input.GroupID != "" {
			g, err := m.currentRealmGroupLocked(p.Actor, p.Input.GroupID)
			if err != nil {
				return AccessAssignment{}, err
			}
			if g.State != GroupActive || d.Key == "tenant_admin" {
				return AccessAssignment{}, E(CodeConflict, "group assignment is inactive or protected")
			}
		} else {
			member, ok := m.memberships[p.Input.MembershipID]
			if !ok || member.TenantID != p.Actor.TenantID || member.RealmID != p.Actor.RealmID {
				return AccessAssignment{}, E(CodeNotFound, "membership was not found in the current realm")
			}
			if member.State != MembershipActive {
				return AccessAssignment{}, E(CodeConflict, "assignment membership is inactive")
			}
		}
		for _, existing := range m.allAccessAssignmentsLocked() {
			if existing.ID == p.ID || existing.TenantID == p.Actor.TenantID && existing.State == AssignmentActive && existing.RoleDefinitionID == d.ID && existing.MembershipID == p.Input.MembershipID && existing.GroupID == p.Input.GroupID && strings.EqualFold(existing.NamespaceID, p.Input.NamespaceID) {
				return AccessAssignment{}, E(CodeConflict, "active assignment already exists")
			}
		}
		value := AccessAssignment{ID: p.ID, TenantID: p.Actor.TenantID, RealmID: p.Actor.RealmID, RoleDefinitionID: d.ID, MembershipID: p.Input.MembershipID, GroupID: p.Input.GroupID, NamespaceID: p.Input.NamespaceID, State: AssignmentActive, Revision: 1, GrantedBy: p.Actor.Subject, GrantReason: p.Evidence.Reason, GrantedAt: p.At}
		if err := m.appendAuthorizationEvidence(ctx, p.AdministrationChange, "authorization.assignment.granted", "role_assignment", map[string]any{"after": value}, true); err != nil {
			return AccessAssignment{}, err
		}
		m.accessAssignments[p.ID] = value
		return value, nil
	})
}
func (m *MemoryStore) RevokeAccessAssignment(ctx context.Context, p AdministrationChange) (AccessAssignment, error) {
	return memoryAuthorizationWrite(ctx, m, p.Actor, func() (AccessAssignment, error) {
		var value AccessAssignment
		found := false
		for _, v := range m.allAccessAssignmentsLocked() {
			if v.ID == p.ID && v.TenantID == p.Actor.TenantID && v.RealmID == p.Actor.RealmID {
				value = v
				found = true
				break
			}
		}
		if !found {
			return AccessAssignment{}, E(CodeNotFound, "assignment was not found")
		}
		if value.Revision != p.ExpectedRevision {
			return AccessAssignment{}, E(CodePreconditionFailed, "assignment revision does not match")
		}
		if value.State != AssignmentActive {
			return AccessAssignment{}, E(CodeConflict, "assignment cannot be revoked")
		}
		if p.At.Before(value.GrantedAt) {
			return AccessAssignment{}, safeerr.At("authorization.assignment.time_regression", E(CodeConflict, "assignment cannot be revoked"))
		}
		if m.definitions[value.RoleDefinitionID].Key == "tenant_admin" && m.isActiveTenantAdministrator(m.memberships[value.MembershipID]) && m.activeTenantAdministratorCount(p.Actor.TenantID) <= 1 {
			return AccessAssignment{}, E(CodeSeparationOfDuties, "the final active administrator cannot be revoked")
		}
		before := value
		value.State = AssignmentRevoked
		value.Revision++
		value.RevokedBy = p.Actor.Subject
		value.RevokeReason = p.Evidence.Reason
		value.RevokedAt = &p.At
		if err := m.appendAuthorizationEvidence(ctx, p, "authorization.assignment.revoked", "role_assignment", map[string]any{"before": before, "after": value}, true); err != nil {
			return AccessAssignment{}, err
		}
		if legacy, ok := m.assignments[p.ID]; ok {
			legacy.State = value.State
			legacy.Revision = value.Revision
			legacy.RevokedBy = value.RevokedBy
			legacy.RevokeReason = value.RevokeReason
			legacy.RevokedAt = value.RevokedAt
			m.assignments[p.ID] = legacy
		} else {
			m.accessAssignments[p.ID] = value
		}
		return cloneAccessAssignment(value), nil
	})
}
func (m *MemoryStore) ListEffectiveAssignments(ctx context.Context, a Actor, r AuthorizationPageRequest) ([]EffectiveAssignment, error) {
	return memoryAuthorizationRead(ctx, m, a, &r, true, func() ([]EffectiveAssignment, error) {
		items := []EffectiveAssignment{}
		for _, v := range m.allAccessAssignmentsLocked() {
			if v.ID > r.AfterID && m.effectiveAssignmentLocked(a, v) {
				item := EffectiveAssignment{Assignment: v, Role: cloneDefinition(m.definitions[v.RoleDefinitionID])}
				if v.GroupID != "" {
					item.GroupName = m.groups[v.GroupID].Name
				}
				items = append(items, item)
			}
		}
		return memoryPage(items, r.Limit, func(v EffectiveAssignment) string { return v.Assignment.ID }), nil
	})
}

var _ AdministrationStore = (*MemoryStore)(nil)
