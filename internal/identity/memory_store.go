// The in-memory membership store preserves tenant boundaries, revision checks
// and administrator continuity for development and tests.

package identity

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/audit"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/events"
)

type Seed struct {
	TenantID             string
	Issuer               string
	ExternalOrganization string
	Subject              string
	ActorType            ActorType
	Roles                []Role
	RoleTemplates        []string
	Namespaces           map[string][]Role
}

type memoryRealm struct {
	ID                   string
	TenantID             string
	Issuer               string
	ExternalOrganization string
	State                RealmState
}

// MemoryStore provides deterministic development identities while preserving
// the same active-realm, membership, grant, and evidence rules as PostgreSQL.
type MemoryStore struct {
	definitions       map[string]RoleDefinition
	groups            map[string]AccessGroup
	groupMembers      map[string]GroupMember
	accessAssignments map[string]AccessAssignment
	revisions         map[string]int64
	mu                sync.RWMutex
	realms            map[string]memoryRealm
	memberships       map[string]Membership
	assignments       map[string]RoleAssignment
	audit             []ChangeEvidence
	evidence          EvidenceSink
}

// NewMemoryStore validates explicit realm, membership and grant seeds before
// creating a development identity store.
func NewMemoryStore(seeds []Seed, evidence ...EvidenceSink) (*MemoryStore, error) {
	if len(evidence) > 1 || len(evidence) == 1 && evidence[0] == nil {
		return nil, E(CodeInvalidArgument, "at most one non-nil identity evidence sink is allowed")
	}
	seeds, err := NormalizeDevelopmentSeeds(seeds)
	if err != nil {
		return nil, err
	}
	store := &MemoryStore{revisions: make(map[string]int64), realms: make(map[string]memoryRealm), memberships: make(map[string]Membership), assignments: make(map[string]RoleAssignment)}
	if len(evidence) == 1 {
		store.evidence = evidence[0]
	}
	seedTime := time.Unix(0, 0).UTC()
	for _, seed := range seeds {
		store.revisions[seed.TenantID] = 1
		realmID := deterministicUUID("realm", seed.Issuer, seed.TenantID, seed.ExternalOrganization)
		store.realms[realmID] = memoryRealm{ID: realmID, TenantID: seed.TenantID, Issuer: seed.Issuer, ExternalOrganization: seed.ExternalOrganization, State: RealmActive}
		membershipID := deterministicUUID("membership", realmID, seed.Subject)
		store.memberships[membershipID] = Membership{ID: membershipID, TenantID: seed.TenantID, RealmID: realmID, Subject: seed.Subject, ActorType: seed.ActorType, State: MembershipActive, Revision: 1, CreatedAt: seedTime, UpdatedAt: seedTime}
		for _, role := range seed.Roles {
			if err := store.seedAssignment(seedTime, membershipID, seed, role, ""); err != nil {
				return nil, err
			}
		}
		for namespaceID, roles := range seed.Namespaces {
			for _, role := range roles {
				if err := store.seedAssignment(seedTime, membershipID, seed, role, namespaceID); err != nil {
					return nil, err
				}
			}
		}
	}
	store.initializeAuthorizationDefinitions(seedTime)
	// Exact template seeds are independent of the legacy development bundles.
	for _, seed := range seeds {
		for _, key := range seed.RoleTemplates {
			realmID := deterministicUUID("realm", seed.Issuer, seed.TenantID, seed.ExternalOrganization)
			memberID := deterministicUUID("membership", realmID, seed.Subject)
			id := deterministicUUID("template-assignment", memberID, key)
			store.accessAssignments[id] = AccessAssignment{ID: id, TenantID: seed.TenantID, RealmID: realmID, MembershipID: memberID, RoleDefinitionID: deterministicUUID("definition", seed.TenantID, key), State: AssignmentActive, Revision: 1, GrantedBy: "development-bootstrap", GrantReason: "Configured exact development template", GrantedAt: seedTime}
		}
	}
	// Explicit bootstrap conversion of the former tenant-wide bundles. These
	// are real assignments, not runtime role implications. Scoped grants stay
	// scoped and receive no tenant-wide companion authority.
	for _, assignment := range store.assignments {
		if assignment.NamespaceID != "" {
			continue
		}
		member := store.memberships[assignment.MembershipID]
		for _, key := range BootstrapCompanions(assignment.Role) {
			id := deterministicUUID("companion", member.ID, key)
			store.accessAssignments[id] = AccessAssignment{ID: id, TenantID: member.TenantID, RealmID: member.RealmID, MembershipID: member.ID, RoleDefinitionID: deterministicUUID("definition", member.TenantID, key), State: AssignmentActive, Revision: 1, GrantedBy: "development-bootstrap", GrantReason: "Explicit split development bundle", GrantedAt: seedTime}
		}
	}
	return store, nil
}

// seedAssignment validates a seed's role scope and creates a deterministic
// assignment identity.
func (m *MemoryStore) seedAssignment(at time.Time, membershipID string, seed Seed, role Role, namespaceID string) error {
	if err := validateRoleScope(role, namespaceID); err != nil {
		return err
	}
	idValue := deterministicUUID("assignment", membershipID, string(role), namespaceID)
	m.assignments[idValue] = RoleAssignment{ID: idValue, TenantID: seed.TenantID, MembershipID: membershipID, Subject: seed.Subject, ActorType: seed.ActorType, Role: role, NamespaceID: namespaceID, State: AssignmentActive, Revision: 1, GrantedBy: "development-bootstrap", GrantReason: "Configured development identity", GrantedAt: at}
	return nil
}

// deterministicUUID derives a stable development identifier from seed identity
// components.
func deterministicUUID(parts ...string) string {
	hash := sha256.New()
	for _, part := range parts {
		var size [8]byte
		binary.BigEndian.PutUint64(size[:], uint64(len(part)))
		_, _ = hash.Write(size[:])
		_, _ = hash.Write([]byte(part))
	}
	raw := hash.Sum(nil)[:16]
	raw[6] = raw[6]&0x0f | 0x70
	raw[8] = raw[8]&0x3f | 0x80
	text := hex.EncodeToString(raw)
	return text[:8] + "-" + text[8:12] + "-" + text[12:16] + "-" + text[16:20] + "-" + text[20:]
}

// Resolve maps an authenticated principal to an active tenant membership and
// its effective grants.
func (m *MemoryStore) Resolve(ctx context.Context, principal Principal) (Actor, error) {
	return m.resolvePrincipal(ctx, principal, true)
}

// ResolvePrincipal returns a compact, current membership with no role hydration.
func (m *MemoryStore) ResolvePrincipal(ctx context.Context, principal Principal) (Actor, error) {
	return m.resolvePrincipal(ctx, principal, false)
}
func (m *MemoryStore) resolvePrincipal(ctx context.Context, principal Principal, hydrate bool) (Actor, error) {
	if principal.Issuer == "" || !ValidSubject(principal.Subject) || (principal.TenantID == "") == (principal.ExternalOrganization == "") || principal.TenantID != "" && !ValidTenantID(principal.TenantID) {
		return Actor{}, E(CodeUnauthenticated, "verified identity is incomplete")
	}
	if !memoryAdmissionFrom(ctx, m) {
		m.mu.RLock()
		defer m.mu.RUnlock()
	}
	var realm memoryRealm
	for _, candidate := range m.realms {
		if candidate.Issuer != principal.Issuer || candidate.State != RealmActive {
			continue
		}
		if (principal.TenantID != "" && candidate.ExternalOrganization == "" && strings.EqualFold(candidate.TenantID, principal.TenantID)) || (principal.TenantID == "" && candidate.ExternalOrganization != "" && candidate.ExternalOrganization == principal.ExternalOrganization) {
			realm = candidate
			break
		}
	}
	if realm.ID == "" {
		return Actor{}, E(CodeUnauthenticated, "identity realm is not provisioned")
	}
	var membership Membership
	for _, candidate := range m.memberships {
		if candidate.TenantID == realm.TenantID && candidate.RealmID == realm.ID && candidate.Subject == principal.Subject && candidate.State == MembershipActive {
			membership = candidate
			break
		}
	}
	if membership.ID == "" {
		return Actor{}, E(CodeUnauthenticated, "identity is not an active Catalog member")
	}
	actor := BindPermissionStore(Actor{Issuer: realm.Issuer, TenantID: realm.TenantID, Subject: membership.Subject, RealmID: realm.ID, MembershipID: membership.ID, ActorType: membership.ActorType, Namespaces: make(map[string][]Role), Claims: cloneClaims(principal.Claims)}, m)
	if !hydrate {
		actor.Namespaces = nil
		return actor, nil
	}
	for _, assignment := range m.assignments {
		if assignment.MembershipID != membership.ID || assignment.State != AssignmentActive {
			continue
		}
		if assignment.NamespaceID == "" {
			actor.Roles = append(actor.Roles, assignment.Role)
		} else {
			actor.Namespaces[assignment.NamespaceID] = append(actor.Namespaces[assignment.NamespaceID], assignment.Role)
		}
	}
	actor.Roles = canonicalRoleValues(actor.Roles)
	for namespaceID, roles := range actor.Namespaces {
		actor.Namespaces[namespaceID] = canonicalRoleValues(roles)
	}
	return actor, nil
}

// ListMemberships returns tenant members ordered by subject and ID after the
// supplied stable position.
func (m *MemoryStore) ListMemberships(ctx context.Context, tenantID, afterSubject, afterID string, limit int, filters ...MembershipFilter) ([]Membership, error) {
	filter, err := ValidateMembershipFilters(filters)
	if err != nil {
		return nil, err
	}
	if !memoryAdmissionFrom(ctx, m) {
		m.mu.RLock()
		defer m.mu.RUnlock()
	}
	all := make([]Membership, 0)
	for _, membership := range m.memberships {
		if membership.TenantID == tenantID && filter.matches(membership) {
			all = append(all, membership)
		}
	}
	sort.Slice(all, func(i, j int) bool {
		if all[i].Subject != all[j].Subject {
			return all[i].Subject < all[j].Subject
		}
		return all[i].ID < all[j].ID
	})
	items := make([]Membership, 0, min(limit, len(all)))
	for _, membership := range all {
		if afterSubject != "" && (membership.Subject < afterSubject || membership.Subject == afterSubject && membership.ID <= afterID) {
			continue
		}
		items = append(items, membership)
		if len(items) == limit {
			break
		}
	}
	return items, nil
}

// GetMembership returns a member only when the explicit tenant matches.
func (m *MemoryStore) GetMembership(ctx context.Context, tenantID, membershipID string) (Membership, error) {
	if !memoryAdmissionFrom(ctx, m) {
		m.mu.RLock()
		defer m.mu.RUnlock()
	}
	membership, ok := m.memberships[membershipID]
	if !ok || membership.TenantID != tenantID {
		return Membership{}, E(CodeNotFound, "membership was not found")
	}
	return membership, nil
}

// ChangeMembership mirrors the durable lifecycle rules, including last-admin
// protection, offboarding revocation, and rollback when evidence cannot append.
func (m *MemoryStore) ChangeMembership(ctx context.Context, params ChangeMembershipParams) (MembershipLifecycleResult, error) {
	if memoryAdmissionFrom(ctx, m) {
		return MembershipLifecycleResult{}, E(CodeConflict, "authorization administration cannot upgrade shared admission")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.checkAdministratorLocked(ctx, params.TenantID); err != nil {
		return MembershipLifecycleResult{}, err
	}
	membership, ok := m.memberships[params.MembershipID]
	if !ok || membership.TenantID != params.TenantID {
		return MembershipLifecycleResult{}, E(CodeNotFound, "membership was not found")
	}
	if membership.Revision != params.ExpectedRevision {
		return MembershipLifecycleResult{}, E(CodePreconditionFailed, "membership revision does not match")
	}
	var targetState MembershipState
	switch params.Transition {
	case MembershipSuspend:
		if membership.State != MembershipActive {
			return MembershipLifecycleResult{}, E(CodeConflict, "only an active membership can be suspended")
		}
		targetState = MembershipSuspended
	case MembershipReactivate:
		if membership.State != MembershipSuspended {
			return MembershipLifecycleResult{}, E(CodeConflict, "only a suspended membership can be reactivated")
		}
		realm, exists := m.realms[membership.RealmID]
		if !exists || realm.TenantID != params.TenantID || realm.State != RealmActive {
			return MembershipLifecycleResult{}, E(CodeConflict, "membership cannot be reactivated while its identity realm is suspended")
		}
		targetState = MembershipActive
	case MembershipOffboard:
		if membership.State == MembershipOffboarded {
			return MembershipLifecycleResult{}, E(CodeConflict, "membership is already offboarded")
		}
		targetState = MembershipOffboarded
	default:
		return MembershipLifecycleResult{}, E(CodeInvalidArgument, "membership transition is invalid")
	}
	if targetState != MembershipActive && m.isActiveTenantAdministrator(membership) && m.activeTenantAdministratorCount(params.TenantID) <= 1 {
		return MembershipLifecycleResult{}, E(CodeSeparationOfDuties, "the final active tenant administrator membership cannot be disabled")
	}
	previousMembership := membership
	previousAssignments := make(map[string]RoleAssignment)
	previousAccessAssignments := make(map[string]AccessAssignment)
	previousGroupMembers := make(map[string]GroupMember)
	result := MembershipLifecycleResult{}
	if params.Transition == MembershipOffboard {
		namespaces := make(map[string]struct{})
		for idValue, assignment := range m.assignments {
			if assignment.MembershipID != membership.ID || assignment.State != AssignmentActive {
				continue
			}
			previousAssignments[idValue] = assignment
			assignment.State = AssignmentRevoked
			assignment.Revision++
			assignment.RevokedBy = params.Actor
			assignment.RevokeReason = params.Reason
			assignment.RevokedAt = &params.At
			m.assignments[idValue] = assignment
			result.RevokedRoleAssignments++
			if assignment.NamespaceID != "" {
				namespaces[assignment.NamespaceID] = struct{}{}
			}
		}
		result.RevokedNamespaceMemberships = len(namespaces)
		for idValue, assignment := range m.accessAssignments {
			if assignment.MembershipID == membership.ID && assignment.State == AssignmentActive {
				previousAccessAssignments[idValue] = assignment
				assignment.State = AssignmentRevoked
				assignment.Revision++
				assignment.RevokedBy = params.Actor
				assignment.RevokeReason = params.Reason
				assignment.RevokedAt = &params.At
				m.accessAssignments[idValue] = assignment
				result.RevokedRoleAssignments++
			}
		}
		for idValue, member := range m.groupMembers {
			if member.MembershipID == membership.ID && member.State == AssignmentActive {
				previousGroupMembers[idValue] = member
				member.State = AssignmentRevoked
				member.Revision++
				member.RevokedBy = params.Actor
				member.RevokeReason = params.Reason
				member.RevokedAt = &params.At
				m.groupMembers[idValue] = member
				result.RevokedGroupMemberships++
			}
		}
	}
	membership.State = targetState
	membership.Revision++
	membership.UpdatedAt = params.At
	m.memberships[membership.ID] = membership
	setEvidenceFacts(&params.Evidence, map[string]any{"transition": params.Transition, "revokedRoleAssignments": result.RevokedRoleAssignments, "revokedNamespaceMemberships": result.RevokedNamespaceMemberships, "revokedGroupMemberships": result.RevokedGroupMemberships})
	if err := m.appendEvidence(ctx, params.TenantID, params.Evidence); err != nil {
		m.memberships[membership.ID] = previousMembership
		for idValue, assignment := range previousAssignments {
			m.assignments[idValue] = assignment
		}
		for idValue, assignment := range previousAccessAssignments {
			m.accessAssignments[idValue] = assignment
		}
		for idValue, member := range previousGroupMembers {
			m.groupMembers[idValue] = member
		}
		return MembershipLifecycleResult{}, err
	}
	result.Membership = membership
	return result, nil
}

// setEvidenceFacts updates the prepared mutation's closed facts before evidence
// validation.
func setEvidenceFacts(evidence *ChangeEvidence, facts map[string]any) {

	if evidence.Details == nil {
		evidence.Details = make(map[string]any)
	}
	evidence.Details["facts"] = facts
}

// isActiveTenantAdministrator reports whether an active membership has a
// current tenant-wide administrator grant.
func (m *MemoryStore) isActiveTenantAdministrator(membership Membership) bool {
	if membership.State != MembershipActive {
		return false
	}
	realm, ok := m.realms[membership.RealmID]
	if !ok || realm.State != RealmActive {
		return false
	}
	for _, assignment := range m.assignments {
		if assignment.MembershipID == membership.ID && assignment.NamespaceID == "" && assignment.Role == RoleTenantAdmin && assignment.State == AssignmentActive {
			return true
		}
	}
	for _, assignment := range m.accessAssignments {
		if assignment.MembershipID == membership.ID && assignment.NamespaceID == "" && assignment.State == AssignmentActive && m.definitions[assignment.RoleDefinitionID].Key == "tenant_admin" {
			return true
		}
	}
	return false
}

// activeTenantAdministratorCount counts active administrators only in the
// selected tenant.
func (m *MemoryStore) activeTenantAdministratorCount(tenantID string) int {
	count := 0
	for _, membership := range m.memberships {
		if membership.TenantID == tenantID && m.isActiveTenantAdministrator(membership) {
			count++
		}
	}
	return count
}

// cloneClaims copies claim projections so resolved actors cannot mutate stored
// seed data.
func cloneClaims(values map[string]string) map[string]string {
	if values == nil {
		return nil
	}
	result := make(map[string]string, len(values))
	for key, value := range values {
		result[key] = value
	}
	return result
}

// canonicalRoleValues normalizes role ordering and removes duplicate effective
// grants.
func canonicalRoleValues(values []Role) []Role {
	sort.Slice(values, func(i, j int) bool { return values[i] < values[j] })
	result := values[:0]
	for _, value := range values {
		if len(result) == 0 || result[len(result)-1] != value {
			result = append(result, value)
		}
	}
	return result
}

// ListRoleAssignments returns the bounded tenant assignment page matching
// membership and state filters.
func (m *MemoryStore) ListRoleAssignments(ctx context.Context, tenantID string, request RoleAssignmentListRequest) ([]RoleAssignment, error) {
	if !memoryAdmissionFrom(ctx, m) {
		m.mu.RLock()
		defer m.mu.RUnlock()
	}
	result := make([]RoleAssignment, 0)
	for _, retained := range m.allAccessAssignmentsLocked() {
		assignment, eligible := m.namedRoleAssignmentLocked(retained)
		if !eligible {
			continue
		}
		after := request.AfterID == "" || assignment.Subject > request.AfterSubject || assignment.Subject == request.AfterSubject && (assignment.NamespaceID > request.AfterNamespaceID || assignment.NamespaceID == request.AfterNamespaceID && (assignment.Role > request.AfterRole || assignment.Role == request.AfterRole && assignment.ID > request.AfterID))
		if assignment.TenantID == tenantID && after && (request.MembershipID == "" || assignment.MembershipID == request.MembershipID) {
			result = append(result, cloneAssignment(assignment))
		}
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Subject != result[j].Subject {
			return result[i].Subject < result[j].Subject
		}
		if result[i].NamespaceID != result[j].NamespaceID {
			return result[i].NamespaceID < result[j].NamespaceID
		}
		if result[i].Role != result[j].Role {
			return result[i].Role < result[j].Role
		}
		return result[i].ID < result[j].ID
	})
	if len(result) > request.Limit {
		result = result[:request.Limit]
	}
	return result, nil
}

// GrantRole enforces same-tenant membership and scope constraints before
// appending grant evidence.
func (m *MemoryStore) GrantRole(ctx context.Context, params GrantRoleParams) (RoleAssignment, error) {
	if memoryAdmissionFrom(ctx, m) {
		return RoleAssignment{}, E(CodeConflict, "authorization administration cannot upgrade shared admission")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.checkAdministratorLocked(ctx, params.TenantID); err != nil {
		return RoleAssignment{}, err
	}
	realm, ok := m.realms[params.RealmID]
	if !ok || realm.TenantID != params.TenantID || realm.State != RealmActive {
		return RoleAssignment{}, E(CodeNotFound, "identity realm was not found")
	}
	var membership Membership
	for idValue, candidate := range m.memberships {
		if candidate.TenantID == params.TenantID && candidate.RealmID == params.RealmID && candidate.Subject == params.Subject {
			if candidate.ActorType != params.ActorType || candidate.State != MembershipActive {
				return RoleAssignment{}, E(CodeConflict, "membership exists with another type or state")
			}
			membership = candidate
			membership.ID = idValue
			break
		}
	}
	membershipCreated := membership.ID == ""
	if membershipCreated {
		membership = Membership{ID: params.MembershipID, TenantID: params.TenantID, RealmID: params.RealmID, Subject: params.Subject, ActorType: params.ActorType, State: MembershipActive, Revision: 1, CreatedAt: params.At, UpdatedAt: params.At}
		m.memberships[membership.ID] = membership
	}
	for _, assignment := range m.assignments {
		if assignment.MembershipID == membership.ID && assignment.Role == params.Role && assignment.NamespaceID == params.NamespaceID && assignment.State == AssignmentActive {
			return RoleAssignment{}, E(CodeConflict, "role assignment already exists")
		}
	}
	for _, assignment := range m.accessAssignments {
		if assignment.MembershipID == membership.ID && m.definitions[assignment.RoleDefinitionID].Key == string(params.Role) && assignment.NamespaceID == params.NamespaceID && assignment.State == AssignmentActive {
			return RoleAssignment{}, E(CodeConflict, "role assignment already exists")
		}
	}
	assignment := RoleAssignment{ID: params.AssignmentID, TenantID: params.TenantID, MembershipID: membership.ID, Subject: membership.Subject, ActorType: membership.ActorType, Role: params.Role, NamespaceID: params.NamespaceID, State: AssignmentActive, Revision: 1, GrantedBy: params.Actor, GrantReason: params.Reason, GrantedAt: params.At}
	m.assignments[assignment.ID] = assignment
	if err := m.appendEvidence(ctx, params.TenantID, params.Evidence); err != nil {
		delete(m.assignments, assignment.ID)
		if membershipCreated {
			delete(m.memberships, membership.ID)
		}
		return RoleAssignment{}, err
	}
	return cloneAssignment(assignment), nil
}

// RevokeRole checks the assignment revision and administrator continuity before
// retaining revocation evidence.
func (m *MemoryStore) RevokeRole(ctx context.Context, params RevokeRoleParams) (RoleAssignment, error) {
	if memoryAdmissionFrom(ctx, m) {
		return RoleAssignment{}, E(CodeConflict, "authorization administration cannot upgrade shared admission")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.checkAdministratorLocked(ctx, params.TenantID); err != nil {
		return RoleAssignment{}, err
	}
	assignment, legacy := m.assignments[params.AssignmentID]
	retained, modern := m.accessAssignments[params.AssignmentID]
	if !legacy && modern {
		assignment, modern = m.namedRoleAssignmentLocked(retained)
	}
	member := m.memberships[assignment.MembershipID]
	if (!legacy && !modern) || assignment.TenantID != params.TenantID || member.RealmID != params.RealmID || !ValidTenantID(params.RealmID) {
		return RoleAssignment{}, E(CodeNotFound, "role assignment was not found")
	}
	if assignment.Revision != params.ExpectedRevision {
		return RoleAssignment{}, E(CodePreconditionFailed, "role-assignment revision does not match")
	}
	if assignment.State != AssignmentActive {
		return RoleAssignment{}, E(CodeConflict, "role assignment is already revoked")
	}
	if assignment.Role == RoleTenantAdmin && assignment.NamespaceID == "" {
		membership := m.memberships[assignment.MembershipID]
		if m.isActiveTenantAdministrator(membership) && m.activeTenantAdministratorCount(params.TenantID) <= 1 {
			return RoleAssignment{}, E(CodeSeparationOfDuties, "the final tenant administrator role cannot be revoked")
		}
	}
	previous := assignment
	assignment.State = AssignmentRevoked
	assignment.Revision++
	assignment.RevokedBy = params.Actor
	assignment.RevokeReason = params.Reason
	assignment.RevokedAt = &params.At
	if legacy {
		m.assignments[assignment.ID] = assignment
	} else {
		updated := retained
		updated.State, updated.Revision = assignment.State, assignment.Revision
		updated.RevokedBy, updated.RevokeReason, updated.RevokedAt = assignment.RevokedBy, assignment.RevokeReason, assignment.RevokedAt
		m.accessAssignments[assignment.ID] = updated
	}
	if err := m.appendEvidence(ctx, params.TenantID, params.Evidence); err != nil {
		if legacy {
			m.assignments[assignment.ID] = previous
		} else {
			m.accessAssignments[assignment.ID] = retained
		}
		return RoleAssignment{}, err
	}
	return cloneAssignment(assignment), nil
}

// namedRoleAssignmentLocked projects only direct assignments of the eight exact
// built-in definitions, regardless of which administration API created them.
func (m *MemoryStore) namedRoleAssignmentLocked(value AccessAssignment) (RoleAssignment, bool) {
	definition, exists := m.definitions[value.RoleDefinitionID]
	member, memberExists := m.memberships[value.MembershipID]
	role := Role(definition.Key)
	if !exists || !memberExists || !definition.Builtin || !ValidRole(role) || value.GroupID != "" || value.MembershipID == "" || definition.TenantID != value.TenantID || member.TenantID != value.TenantID || member.RealmID != value.RealmID {
		return RoleAssignment{}, false
	}
	return RoleAssignment{ID: value.ID, TenantID: value.TenantID, MembershipID: value.MembershipID, Subject: member.Subject, ActorType: member.ActorType, Role: role, NamespaceID: value.NamespaceID, State: value.State, Revision: value.Revision, GrantedBy: value.GrantedBy, GrantReason: value.GrantReason, GrantedAt: value.GrantedAt, RevokedBy: value.RevokedBy, RevokeReason: value.RevokeReason, RevokedAt: value.RevokedAt}, true
}

// appendEvidence validates same-tenant audit/outbox bindings before calling the
// configured evidence sink.
func (m *MemoryStore) appendEvidence(ctx context.Context, tenantID string, evidence ChangeEvidence) error {
	if err := validateMemoryChangeEvidence(tenantID, evidence); err != nil {
		return err
	}
	if m.evidence != nil {
		// The identity writer has already checked current authority under its
		// exclusive lock. Its local audit sink must join that admission rather
		// than attempting to acquire the same policy lock recursively.
		guard := &currentAdmission{}
		guard.active.Store(true)
		defer guard.active.Store(false)
		ctx = context.WithValue(ctx, currentAdmissionKey{}, guard)
		if err := m.evidence.AppendIdentityEvidence(ctx, tenantID, evidence); err != nil {
			return Wrap(CodeDependency, "persist identity audit and notification evidence", err)
		}
	}
	m.revisions[tenantID]++
	m.audit = append(m.audit, evidence)
	return nil
}

// validateMemoryChangeEvidence checks the mutation's tenant, attribution and
// notification consistency.
func validateMemoryChangeEvidence(tenantID string, evidence ChangeEvidence) error {

	record := audit.Record{FormatVersion: evidence.FormatVersion, ID: evidence.AuditID, TenantID: tenantID, Actor: evidence.Actor, Action: evidence.Action, TargetType: evidence.TargetType, TargetID: evidence.TargetID, Reason: evidence.Reason, RequestID: evidence.RequestID, Details: evidence.Details, At: evidence.At}
	if err := audit.ValidateNewRecord(record); err != nil {
		return Wrap(CodeInternal, "identity audit evidence is invalid", err)
	}
	if !ValidTenantID(evidence.OutboxID) || evidence.OutboxType != events.CanonicalType(evidence.Action) || evidence.OutboxSubject != evidence.TargetID || evidence.OutboxPublished != nil && !evidence.OutboxPublished.Equal(evidence.At) {
		return E(CodeInternal, "identity notification metadata does not match its audit evidence")
	}
	if err := events.ValidateReferenceData(evidence.OutboxData, evidence.Action, evidence.TargetType, evidence.TargetID, record.AfterDigest, ""); err != nil {
		return Wrap(CodeInternal, "identity notification data does not match its audit evidence", err)
	}
	return nil
}

// cloneAssignment copies mutable assignment fields before returning repository
// state.
func cloneAssignment(value RoleAssignment) RoleAssignment {
	result := value
	if value.RevokedAt != nil {
		at := *value.RevokedAt
		result.RevokedAt = &at
	}
	return result
}
