package identity

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

type DefinitionState string

const (
	DefinitionActive   DefinitionState = "active"
	DefinitionArchived DefinitionState = "archived"
)

type GroupState string

const (
	GroupActive    GroupState = "active"
	GroupSuspended GroupState = "suspended"
	GroupArchived  GroupState = "archived"
)

// RoleDefinition identities and built-in keys are distinct from display names.
type RoleDefinition struct {
	ID               string          `json:"id"`
	TenantID         string          `json:"tenantId"`
	Key              string          `json:"key,omitempty"`
	Builtin          bool            `json:"builtin"`
	Name             string          `json:"name"`
	Description      string          `json:"description"`
	State            DefinitionState `json:"state"`
	Revision         int64           `json:"revision"`
	Permissions      []Permission    `json:"permissions"`
	NamespaceCapable bool            `json:"namespaceCapable"`
	CreatedAt        time.Time       `json:"createdAt"`
	UpdatedAt        time.Time       `json:"updatedAt"`
}
type AccessGroup struct {
	ID          string     `json:"id"`
	TenantID    string     `json:"tenantId"`
	RealmID     string     `json:"realmId"`
	Name        string     `json:"name"`
	Description string     `json:"description"`
	State       GroupState `json:"state"`
	Revision    int64      `json:"revision"`
	CreatedAt   time.Time  `json:"createdAt"`
	UpdatedAt   time.Time  `json:"updatedAt"`
}

// Exactly one of MembershipID and GroupID identifies an assignment target.
type AccessAssignment struct {
	ID               string          `json:"id"`
	TenantID         string          `json:"tenantId"`
	RealmID          string          `json:"realmId"`
	RoleDefinitionID string          `json:"roleDefinitionId"`
	MembershipID     string          `json:"membershipId,omitempty"`
	GroupID          string          `json:"groupId,omitempty"`
	NamespaceID      string          `json:"namespaceId,omitempty"`
	State            AssignmentState `json:"state"`
	Revision         int64           `json:"revision"`
	GrantedBy        string          `json:"grantedBy"`
	GrantReason      string          `json:"grantReason"`
	GrantedAt        time.Time       `json:"grantedAt"`
	RevokedBy        string          `json:"revokedBy,omitempty"`
	RevokeReason     string          `json:"revokeReason,omitempty"`
	RevokedAt        *time.Time      `json:"revokedAt,omitempty"`
}
type GroupMember struct {
	ID           string          `json:"id"`
	TenantID     string          `json:"tenantId"`
	RealmID      string          `json:"realmId"`
	GroupID      string          `json:"groupId"`
	MembershipID string          `json:"membershipId"`
	State        AssignmentState `json:"state"`
	Revision     int64           `json:"revision"`
	AddedBy      string          `json:"addedBy"`
	AddReason    string          `json:"addReason"`
	AddedAt      time.Time       `json:"addedAt"`
	RevokedBy    string          `json:"revokedBy,omitempty"`
	RevokeReason string          `json:"revokeReason,omitempty"`
	RevokedAt    *time.Time      `json:"revokedAt,omitempty"`
}
type EffectiveAssignment struct {
	Assignment AccessAssignment `json:"assignment"`
	Role       RoleDefinition   `json:"role"`
	GroupName  string           `json:"groupName,omitempty"`
}
type AuthorizationPage[T any] struct {
	Items                 []T    `json:"items"`
	AuthorizationRevision int64  `json:"authorizationRevision"`
	NextCursor            string `json:"nextCursor,omitempty"`
}
type AuthorizationPageRequest struct {
	AfterID               string
	Limit                 int
	RoleDefinitionID      string
	MembershipID          string
	GroupID               string
	AuthorizationRevision int64
	Name                  string
	State                 string
	BuiltinKey            string
}
type RoleDefinitionInput struct {
	Name        string       `json:"name"`
	Description string       `json:"description"`
	Permissions []Permission `json:"permissions"`
	Reason      string       `json:"reason"`
}
type GroupInput struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Reason      string `json:"reason"`
}
type AccessAssignmentInput struct {
	RoleDefinitionID string `json:"roleDefinitionId"`
	MembershipID     string `json:"membershipId,omitempty"`
	GroupID          string `json:"groupId,omitempty"`
	NamespaceID      string `json:"namespaceId,omitempty"`
	Reason           string `json:"reason"`
}
type AdministrationChange struct {
	Actor            Actor
	ID               string
	ExpectedRevision int64
	At               time.Time
	Evidence         ChangeEvidence
}
type SaveDefinitionParams struct {
	AdministrationChange
	Input   RoleDefinitionInput
	Archive bool
}
type SaveGroupParams struct {
	AdministrationChange
	Input      GroupInput
	Transition GroupState
}
type ChangeGroupMemberParams struct {
	AdministrationChange
	GroupID      string
	MembershipID string
	Revoke       bool
}
type GrantAccessAssignmentParams struct {
	AdministrationChange
	Input AccessAssignmentInput
}

// AdministrationStore owns current reads and atomic authority/evidence changes.
// Implementations recheck administration inside the authority-writer fence.
type AdministrationStore interface {
	PermissionStore
	ReadAdministrationOverview(context.Context, Actor) (AdministrationOverviewRows, error)
	ListDefinitions(context.Context, Actor, AuthorizationPageRequest) ([]RoleDefinition, error)
	GetDefinition(context.Context, Actor, string) (RoleDefinition, error)
	SaveDefinition(context.Context, SaveDefinitionParams) (RoleDefinition, error)
	ListGroups(context.Context, Actor, AuthorizationPageRequest) ([]AccessGroup, error)
	GetGroup(context.Context, Actor, string) (AccessGroup, error)
	SaveGroup(context.Context, SaveGroupParams) (AccessGroup, error)
	ListGroupMembers(context.Context, Actor, AuthorizationPageRequest) ([]GroupMember, error)
	ChangeGroupMember(context.Context, ChangeGroupMemberParams) (GroupMember, error)
	ListAccessAssignments(context.Context, Actor, AuthorizationPageRequest) ([]AccessAssignment, error)
	GrantAccessAssignment(context.Context, GrantAccessAssignmentParams) (AccessAssignment, error)
	RevokeAccessAssignment(context.Context, AdministrationChange) (AccessAssignment, error)
	ListEffectiveAssignments(context.Context, Actor, AuthorizationPageRequest) ([]EffectiveAssignment, error)
	ListMemberContributions(context.Context, Actor, AuthorizationPageRequest) (MemberContributionSelection, error)
}

func validateDisplay(name, description string) error {
	if !utf8.ValidString(name) || !utf8.ValidString(description) || strings.TrimSpace(name) != name || utf8.RuneCountInString(name) < 1 || utf8.RuneCountInString(name) > 120 || utf8.RuneCountInString(description) > 2000 {
		return E(CodeInvalidArgument, "name must contain 1-120 characters and description at most 2000")
	}
	for _, field := range []string{name, description} {
		for _, r := range field {
			if unicode.IsControl(r) && r != '\n' && r != '\t' {
				return E(CodeInvalidArgument, "display metadata contains a control character")
			}
		}
	}
	if strings.ContainsAny(name, "\n\t") {
		return E(CodeInvalidArgument, "name must be a single line")
	}
	return nil
}

// ValidateCustomPermissions requires an exact, finite set and named companions.
// It returns a detached sorted set and never adds implicit authority.
func ValidateCustomPermissions(values []Permission) ([]Permission, error) {
	if len(values) < 1 || len(values) >= len(permissionRegistry) {
		return nil, E(CodeInvalidArgument, fmt.Sprintf("custom permissions must contain 1-%d actions", len(permissionRegistry)-1))
	}
	seen := map[Permission]bool{}
	for _, key := range values {
		if seen[key] {
			return nil, E(CodeInvalidArgument, "permission set contains a duplicate")
		}
		seen[key] = true
		index := slices.IndexFunc(permissionRegistry, func(d PermissionDescriptor) bool { return d.Key == key })
		if index < 0 || !permissionRegistry[index].Delegable {
			return nil, E(CodeInvalidArgument, "permission is unknown or protected")
		}
	}
	missing := []string{}
	for _, d := range permissionRegistry {
		if seen[d.Key] {
			for _, required := range d.Requires {
				if !seen[required] {
					missing = append(missing, string(required))
				}
			}
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return nil, E(CodeInvalidArgument, "missing companion permissions: "+strings.Join(slices.Compact(missing), ", "))
	}
	out := slices.Clone(values)
	slices.Sort(out)
	return out, nil
}
func NamespaceCapable(values []Permission) bool {
	for _, key := range values {
		index := slices.IndexFunc(permissionRegistry, func(d PermissionDescriptor) bool { return d.Key == key })
		if index < 0 || !permissionRegistry[index].NamespaceScoped {
			return false
		}
	}
	return true
}
func ValidateAuthorizationPage(request AuthorizationPageRequest) error {
	if request.Limit < 1 || request.Limit > 101 || request.AuthorizationRevision < 1 {
		return E(CodeInvalidArgument, "authorization page bound or revision is invalid")
	}
	for _, value := range []string{request.AfterID, request.RoleDefinitionID, request.MembershipID, request.GroupID} {
		if value != "" && !ValidTenantID(value) {
			return E(CodeInvalidArgument, "authorization page identifiers must be UUIDs")
		}
	}
	return nil
}
func (s *Service) administrationStore() (AdministrationStore, error) {
	store, ok := s.store.(AdministrationStore)
	if !ok {
		return nil, E(CodeDependency, "authorization administration is unavailable")
	}
	return store, nil
}
func (s *Service) currentAuthorization(ctx context.Context, admin bool) (Actor, int64, error) {
	actor, ok := FromContext(ctx)
	if !ok {
		return Actor{}, 0, E(CodeUnauthenticated, "an authenticated Catalog member is required")
	}
	store, err := s.administrationStore()
	if err != nil {
		return Actor{}, 0, err
	}
	decision, err := store.CheckPermissions(ctx, actor, []PermissionCheck{{Permission: "access.manage"}})
	if err != nil {
		return Actor{}, 0, err
	}
	if admin && !decision.Allowed[0] {
		return Actor{}, 0, E(CodeForbidden, "current access administration permission is required")
	}
	SetPermissionRevision(ctx, decision.Revision)
	return actor, decision.Revision, nil
}

// CurrentPermissionDecisions reports Catalog authority only; OAuth ceilings and
// domain eligibility remain separate requirements on the actual operation.
func (s *Service) CurrentPermissionDecisions(ctx context.Context, checks []PermissionCheck) (PermissionDecision, error) {
	actor, ok := FromContext(ctx)
	if !ok {
		return PermissionDecision{}, E(CodeUnauthenticated, "an authenticated Catalog member is required")
	}
	store, err := s.administrationStore()
	if err != nil {
		return PermissionDecision{}, err
	}
	return store.CheckPermissions(ctx, actor, checks)
}

func authorizationPage[T any](ctx context.Context, s *Service, purpose string, request AuthorizationPageRequest, limit int, cursor string, own bool, fetch func(AdministrationStore, Actor, AuthorizationPageRequest) ([]T, error), position func(T) string) (AuthorizationPage[T], error) {
	if limit < 1 || limit > 100 {
		return AuthorizationPage[T]{}, E(CodeInvalidArgument, "page limit must be between 1 and 100")
	}
	actor, revision, err := s.currentAuthorization(ctx, !own)
	if err != nil {
		return AuthorizationPage[T]{}, err
	}
	store, err := s.administrationStore()
	if err != nil {
		return AuthorizationPage[T]{}, err
	}
	binding := fmt.Sprintf("%s/%d/%s/%s/%s/%s/%q/%q/%q", actor.AuthorizationDigest(), revision, purpose, request.RoleDefinitionID, request.GroupID, request.MembershipID, request.Name, request.State, request.BuiltinKey)
	decoded, present, err := s.decodeMembershipCursor(cursor, binding, limit)
	if err != nil {
		return AuthorizationPage[T]{}, err
	}
	request.Limit = limit + 1
	request.AuthorizationRevision = revision
	if present {
		if decoded.Subject != purpose {
			return AuthorizationPage[T]{}, E(CodeInvalidArgument, "cursor belongs to another collection")
		}
		request.AfterID = decoded.ID
	}
	if err := ValidateAuthorizationPage(request); err != nil {
		return AuthorizationPage[T]{}, err
	}
	items, err := fetch(store, actor, request)
	if err != nil {
		return AuthorizationPage[T]{}, err
	}
	page := AuthorizationPage[T]{Items: items, AuthorizationRevision: revision}
	if len(items) > limit {
		page.Items = items[:limit]
		page.NextCursor, err = s.encodeMembershipCursor(purpose, position(page.Items[limit-1]), binding, limit)
	}
	return page, err
}

func (s *Service) ListRoleDefinitions(ctx context.Context, limit int, cursor string, filters ...DirectoryFilter) (AuthorizationPage[RoleDefinition], error) {
	r, err := directoryRequest(filters, false)
	if err != nil {
		return AuthorizationPage[RoleDefinition]{}, err
	}
	return authorizationPage(ctx, s, "role-definitions", r, limit, cursor, false, func(store AdministrationStore, a Actor, r AuthorizationPageRequest) ([]RoleDefinition, error) {
		return store.ListDefinitions(ctx, a, r)
	}, func(r RoleDefinition) string { return r.ID })
}
func (s *Service) GetRoleDefinition(ctx context.Context, id string) (RoleDefinition, error) {
	if !ValidTenantID(id) {
		return RoleDefinition{}, E(CodeInvalidArgument, "role ID must be a UUID")
	}
	actor, _, err := s.currentAuthorization(ctx, true)
	if err != nil {
		return RoleDefinition{}, err
	}
	store, err := s.administrationStore()
	if err != nil {
		return RoleDefinition{}, err
	}
	return store.GetDefinition(ctx, actor, id)
}
func (s *Service) SaveRoleDefinition(ctx context.Context, id string, revision int64, input RoleDefinitionInput, archive bool, requestID string) (RoleDefinition, error) {
	ctx = WithAdministration(ctx)
	if archive && id == "" {
		return RoleDefinition{}, E(CodeInvalidArgument, "archive requires an existing role")
	}
	if !archive {
		if err := validateDisplay(input.Name, input.Description); err != nil {
			return RoleDefinition{}, err
		}
		var err error
		input.Permissions, err = ValidateCustomPermissions(input.Permissions)
		if err != nil {
			return RoleDefinition{}, err
		}
	}
	action := "authorization.role.updated"
	if id == "" {
		action = "authorization.role.created"
	}
	if archive {
		action = "authorization.role.archived"
	}
	change, err := s.administrationChange(ctx, id, revision, input.Reason, action, "role_definition", requestID)
	if err != nil {
		return RoleDefinition{}, err
	}
	store, err := s.administrationStore()
	if err != nil {
		return RoleDefinition{}, err
	}
	return store.SaveDefinition(ctx, SaveDefinitionParams{AdministrationChange: change, Input: input, Archive: archive})
}
func (s *Service) ListAccessGroups(ctx context.Context, limit int, cursor string, filters ...DirectoryFilter) (AuthorizationPage[AccessGroup], error) {
	r, err := directoryRequest(filters, true)
	if err != nil {
		return AuthorizationPage[AccessGroup]{}, err
	}
	return authorizationPage(ctx, s, "access-groups", r, limit, cursor, false, func(store AdministrationStore, a Actor, r AuthorizationPageRequest) ([]AccessGroup, error) {
		return store.ListGroups(ctx, a, r)
	}, func(g AccessGroup) string { return g.ID })
}
func (s *Service) GetAccessGroup(ctx context.Context, id string) (AccessGroup, error) {
	if !ValidTenantID(id) {
		return AccessGroup{}, E(CodeInvalidArgument, "group ID must be a UUID")
	}
	actor, _, err := s.currentAuthorization(ctx, true)
	if err != nil {
		return AccessGroup{}, err
	}
	store, err := s.administrationStore()
	if err != nil {
		return AccessGroup{}, err
	}
	return store.GetGroup(ctx, actor, id)
}
func (s *Service) SaveAccessGroup(ctx context.Context, id string, revision int64, input GroupInput, transition GroupState, requestID string) (AccessGroup, error) {
	ctx = WithAdministration(ctx)
	if transition == "" {
		if err := validateDisplay(input.Name, input.Description); err != nil {
			return AccessGroup{}, err
		}
	} else if id == "" || !slices.Contains([]GroupState{GroupActive, GroupSuspended, GroupArchived}, transition) {
		return AccessGroup{}, E(CodeInvalidArgument, "group transition is invalid")
	}
	action := "authorization.group.updated"
	if id == "" {
		action = "authorization.group.created"
	}
	switch transition {
	case GroupActive:
		action = "authorization.group.reactivated"
	case GroupSuspended:
		action = "authorization.group.suspended"
	case GroupArchived:
		action = "authorization.group.archived"
	}
	change, err := s.administrationChange(ctx, id, revision, input.Reason, action, "access_group", requestID)
	if err != nil {
		return AccessGroup{}, err
	}
	store, err := s.administrationStore()
	if err != nil {
		return AccessGroup{}, err
	}
	return store.SaveGroup(ctx, SaveGroupParams{AdministrationChange: change, Input: input, Transition: transition})
}
func (s *Service) ListAccessGroupMembers(ctx context.Context, groupID string, limit int, cursor string) (AuthorizationPage[GroupMember], error) {
	if !ValidTenantID(groupID) {
		return AuthorizationPage[GroupMember]{}, E(CodeInvalidArgument, "group ID must be a UUID")
	}
	return authorizationPage(ctx, s, "group-members", AuthorizationPageRequest{GroupID: groupID}, limit, cursor, false, func(store AdministrationStore, a Actor, r AuthorizationPageRequest) ([]GroupMember, error) {
		return store.ListGroupMembers(ctx, a, r)
	}, func(m GroupMember) string { return m.ID })
}
func (s *Service) ChangeAccessGroupMember(ctx context.Context, groupID, id, membershipID string, revision int64, reason, requestID string, revoke bool) (GroupMember, error) {
	ctx = WithAdministration(ctx)
	if !ValidTenantID(groupID) || !revoke && !ValidTenantID(membershipID) || revoke && id == "" || !revoke && (id != "" || revision != 0) {
		return GroupMember{}, E(CodeInvalidArgument, "group/member IDs are required")
	}
	action := "authorization.group_member.added"
	if revoke {
		action = "authorization.group_member.revoked"
	}
	change, err := s.administrationChange(ctx, id, revision, reason, action, "group_member", requestID)
	if err != nil {
		return GroupMember{}, err
	}
	store, err := s.administrationStore()
	if err != nil {
		return GroupMember{}, err
	}
	return store.ChangeGroupMember(ctx, ChangeGroupMemberParams{AdministrationChange: change, GroupID: groupID, MembershipID: membershipID, Revoke: revoke})
}
func (s *Service) ListAuthorizationAssignments(ctx context.Context, request AuthorizationPageRequest, limit int, cursor string) (AuthorizationPage[AccessAssignment], error) {
	return authorizationPage(ctx, s, "access-assignments", request, limit, cursor, false, func(store AdministrationStore, a Actor, r AuthorizationPageRequest) ([]AccessAssignment, error) {
		return store.ListAccessAssignments(ctx, a, r)
	}, func(a AccessAssignment) string { return a.ID })
}
func (s *Service) GrantAuthorizationAssignment(ctx context.Context, input AccessAssignmentInput, requestID string) (AccessAssignment, error) {
	ctx = WithAdministration(ctx)
	if !ValidTenantID(input.RoleDefinitionID) || (input.MembershipID == "") == (input.GroupID == "") {
		return AccessAssignment{}, E(CodeInvalidArgument, "role and exactly one membership or group are required")
	}
	for _, value := range []string{input.MembershipID, input.GroupID, input.NamespaceID} {
		if value != "" && !ValidTenantID(value) {
			return AccessAssignment{}, E(CodeInvalidArgument, "assignment IDs must be UUIDs")
		}
	}
	change, err := s.administrationChange(ctx, "", 0, input.Reason, "authorization.assignment.granted", "role_assignment", requestID)
	if err != nil {
		return AccessAssignment{}, err
	}
	store, err := s.administrationStore()
	if err != nil {
		return AccessAssignment{}, err
	}
	return store.GrantAccessAssignment(ctx, GrantAccessAssignmentParams{AdministrationChange: change, Input: input})
}
func (s *Service) RevokeAuthorizationAssignment(ctx context.Context, id string, revision int64, reason, requestID string) (AccessAssignment, error) {
	ctx = WithAdministration(ctx)
	if id == "" {
		return AccessAssignment{}, E(CodeInvalidArgument, "assignment ID is required")
	}
	change, err := s.administrationChange(ctx, id, revision, reason, "authorization.assignment.revoked", "role_assignment", requestID)
	if err != nil {
		return AccessAssignment{}, err
	}
	store, err := s.administrationStore()
	if err != nil {
		return AccessAssignment{}, err
	}
	return store.RevokeAccessAssignment(ctx, change)
}
func (s *Service) CurrentAccessAssignments(ctx context.Context, limit int, cursor string) (AuthorizationPage[EffectiveAssignment], error) {
	return authorizationPage(ctx, s, "own-access-assignments", AuthorizationPageRequest{}, limit, cursor, true, func(store AdministrationStore, a Actor, r AuthorizationPageRequest) ([]EffectiveAssignment, error) {
		return store.ListEffectiveAssignments(ctx, a, r)
	}, func(a EffectiveAssignment) string { return a.Assignment.ID })
}

// WithAdministrationDisclosure fences a retained private response against current
// authority changes. It does not run or reauthorize the original mutation.
func (s *Service) WithAdministrationDisclosure(ctx context.Context, operation func(context.Context) error) error {
	actor, ok := FromContext(ctx)
	if !ok {
		return E(CodeUnauthenticated, "an authenticated Catalog member is required")
	}
	store, err := s.administrationStore()
	if err != nil {
		return err
	}
	return store.WithPermissionAdmission(ctx, actor, []PermissionCheck{{Permission: "access.manage"}}, operation)
}
