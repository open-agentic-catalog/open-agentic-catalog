// Membership administration validates grants and optimistic preconditions
// before committing attributed tenant-administrator changes through the access
// store.

package identity

import (
	"context"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/audit"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/events"
)

type Clock interface{ Now() time.Time }

type IDGenerator interface {
	New(time.Time) (string, error)
}

// ServiceOptions supplies the identity store and deterministic cursor/evidence
// dependencies used by administration commands.
type ServiceOptions struct {
	Store                       Store
	Clock                       Clock
	IDs                         IDGenerator
	CursorKey                   []byte
	AcknowledgeOutboxAtCreation bool
}

// Service enforces tenant-administrator policy and optimistic concurrency for
// membership and role administration.
type Service struct {
	store                       Store
	clock                       Clock
	ids                         IDGenerator
	cursorKey                   []byte
	acknowledgeOutboxAtCreation bool
}

// NewService validates identity dependencies and copies the signing key used
// for membership continuation tokens.
func NewService(options ServiceOptions) (*Service, error) {
	if options.Store == nil || options.Clock == nil || options.IDs == nil || len(options.CursorKey) < 32 {
		return nil, E(CodeInvalidArgument, "identity service dependencies are required")
	}
	return &Service{store: options.Store, clock: options.Clock, ids: options.IDs, cursorKey: append([]byte(nil), options.CursorKey...), acknowledgeOutboxAtCreation: options.AcknowledgeOutboxAtCreation}, nil
}

// ListMemberships returns an administrator-visible tenant page with a signed
// continuation bound to current authorization and page limit.
func (s *Service) ListMemberships(ctx context.Context, limit int, cursorToken string, filters ...MembershipFilter) (MembershipPage, error) {
	ctx = WithAdministration(ctx)
	if actor, ok := FromContext(ctx); ok {
		if store, ok := s.store.(PermissionStore); ok {
			ctx = WithActor(ctx, BindPermissionStore(actor, store))
		}
	}
	actor, err := requireTenantAdministrator(ctx)
	if err != nil {
		return MembershipPage{}, err
	}
	if limit < 1 || limit > 100 {
		return MembershipPage{}, E(CodeInvalidArgument, "membership page limit must be between 1 and 100")
	}
	filter, err := ValidateMembershipFilters(filters)
	if err != nil {
		return MembershipPage{}, err
	}
	binding := DecisionDigest(ctx) + filter.binding()
	cursor, present, err := s.decodeMembershipCursor(cursorToken, binding, limit)
	if err != nil {
		return MembershipPage{}, err
	}
	afterSubject, afterID := "", ""
	if present {
		afterSubject, afterID = cursor.Subject, cursor.ID
	}
	items, err := s.store.ListMemberships(ctx, actor.TenantID, afterSubject, afterID, limit+1, filter)
	if err != nil {
		return MembershipPage{}, err
	}
	next := ""
	if len(items) > limit {
		items = items[:limit]
		last := items[len(items)-1]
		next, err = s.encodeMembershipCursor(last.Subject, last.ID, binding, limit)
		if err != nil {
			return MembershipPage{}, err
		}
	}
	return MembershipPage{Items: items, NextCursor: next}, nil
}

// GetMembership validates the membership UUID and requires tenant-administrator
// authority.
func (s *Service) GetMembership(ctx context.Context, membershipID string) (Membership, error) {
	ctx = WithAdministration(ctx)
	if actor, ok := FromContext(ctx); ok {
		if store, ok := s.store.(PermissionStore); ok {
			ctx = WithActor(ctx, BindPermissionStore(actor, store))
		}
	}
	actor, err := requireTenantAdministrator(ctx)
	if err != nil {
		return Membership{}, err
	}
	if !ValidTenantID(membershipID) {
		return Membership{}, E(CodeInvalidArgument, "membershipId must be a UUID")
	}
	return s.store.GetMembership(ctx, actor.TenantID, membershipID)
}

// SuspendMembership applies a reasoned administrator suspension under the
// expected membership revision.
func (s *Service) SuspendMembership(ctx context.Context, membershipID, reason, requestID string, expectedRevision int64) (MembershipLifecycleResult, error) {
	return s.changeMembership(ctx, membershipID, reason, requestID, expectedRevision, MembershipSuspend)
}

// ReactivateMembership applies a reasoned administrator reactivation under the
// expected membership revision.
func (s *Service) ReactivateMembership(ctx context.Context, membershipID, reason, requestID string, expectedRevision int64) (MembershipLifecycleResult, error) {
	return s.changeMembership(ctx, membershipID, reason, requestID, expectedRevision, MembershipReactivate)
}

// OffboardMembership requests permanent offboarding and associated grant
// changes under the expected revision.
func (s *Service) OffboardMembership(ctx context.Context, membershipID, reason, requestID string, expectedRevision int64) (MembershipLifecycleResult, error) {
	return s.changeMembership(ctx, membershipID, reason, requestID, expectedRevision, MembershipOffboard)
}

// changeMembership validates administrator authority, expected revision and
// reason before constructing the selected lifecycle transition.
func (s *Service) changeMembership(ctx context.Context, membershipID, reason, requestID string, expectedRevision int64, transition MembershipTransition) (MembershipLifecycleResult, error) {
	ctx = WithAdministration(ctx)
	if actor, ok := FromContext(ctx); ok {
		if store, ok := s.store.(PermissionStore); ok {
			ctx = WithActor(ctx, BindPermissionStore(actor, store))
		}
	}
	actor, err := requireTenantAdministrator(ctx)
	if err != nil {
		return MembershipLifecycleResult{}, err
	}
	if !ValidTenantID(membershipID) || expectedRevision <= 0 {
		return MembershipLifecycleResult{}, E(CodeInvalidArgument, "a membership UUID, current revision, and bounded reason are required")
	}
	var action string
	switch transition {
	case MembershipSuspend:
		action = "identity.membership.suspended"
	case MembershipReactivate:
		action = "identity.membership.reactivated"
	case MembershipOffboard:
		action = "identity.membership.offboarded"
	default:
		return MembershipLifecycleResult{}, E(CodeInvalidArgument, "membership transition is invalid")
	}
	reason, err = normalizeAuditReason(action, reason, "membership transition reason is invalid")
	if err != nil {
		return MembershipLifecycleResult{}, err
	}
	membership, err := s.store.GetMembership(ctx, actor.TenantID, membershipID)
	if err != nil {
		return MembershipLifecycleResult{}, err
	}
	// Persist one ordered time for the membership and its audit/outbox evidence.
	// The locked write still checks the revision if the membership changes after
	// this exact read. Do not turn a wall-clock correction into an invalid write.
	now := s.clock.Now().UTC()
	if now.Before(membership.UpdatedAt) {
		now = membership.UpdatedAt
	}
	now = now.UTC().Truncate(time.Microsecond)
	if now.Before(membership.UpdatedAt) {
		now = now.Add(time.Microsecond)
	}
	evidence, err := s.evidence(ctx, now, actor, action, "tenant_membership", membershipID, reason, requestID, map[string]any{"transition": transition})
	if err != nil {
		return MembershipLifecycleResult{}, err
	}
	return s.store.ChangeMembership(ctx, ChangeMembershipParams{TenantID: actor.TenantID, MembershipID: membershipID, ExpectedRevision: expectedRevision, Transition: transition, Actor: actor.Subject, Reason: reason, At: now, Evidence: evidence})
}

type GrantRoleInput struct {
	Subject     string    `json:"subject"`
	ActorType   ActorType `json:"actorType"`
	Role        Role      `json:"role"`
	NamespaceID string    `json:"namespaceId,omitempty"`
	Reason      string    `json:"reason"`
}

// ListRoleAssignments validates administrator filters and returns a bounded
// tenant assignment page.
func (s *Service) ListRoleAssignments(ctx context.Context, request RoleAssignmentListRequest) ([]RoleAssignment, error) {
	ctx = WithAdministration(ctx)
	if actor, ok := FromContext(ctx); ok {
		if store, ok := s.store.(PermissionStore); ok {
			ctx = WithActor(ctx, BindPermissionStore(actor, store))
		}
	}
	actor, err := requireTenantAdministrator(ctx)
	if err != nil {
		return nil, err
	}
	if request.MembershipID != "" {
		if !ValidTenantID(request.MembershipID) {
			return nil, E(CodeInvalidArgument, "membershipId must be a UUID")
		}
		if _, err := s.store.GetMembership(ctx, actor.TenantID, request.MembershipID); err != nil {
			return nil, err
		}
	}
	positionEmpty := request.AfterSubject == "" && request.AfterNamespaceID == "" && request.AfterRole == "" && request.AfterID == ""
	positionValid := validSubject(request.AfterSubject) && (request.AfterNamespaceID == "" || ValidTenantID(request.AfterNamespaceID)) && ValidRole(request.AfterRole) && ValidTenantID(request.AfterID)
	if request.Limit < 1 || request.Limit > 100 || !positionEmpty && !positionValid {
		return nil, E(CodeInvalidArgument, "role-assignment page position or limit is invalid")
	}
	request.Limit++
	return s.store.ListRoleAssignments(ctx, actor.TenantID, request)
}

// GrantRole validates the target membership and grant scope before retaining an
// attributed administrator change.
func (s *Service) GrantRole(ctx context.Context, input GrantRoleInput, requestID string) (RoleAssignment, error) {
	ctx = WithAdministration(ctx)
	if actor, ok := FromContext(ctx); ok {
		if store, ok := s.store.(PermissionStore); ok {
			ctx = WithActor(ctx, BindPermissionStore(actor, store))
		}
	}
	actor, err := requireTenantAdministrator(ctx)
	if err != nil {
		return RoleAssignment{}, err
	}
	if !validSubject(input.Subject) || !ValidActorType(input.ActorType) {
		return RoleAssignment{}, E(CodeInvalidArgument, "subject, actorType, role, and a bounded reason are required")
	}
	input.Reason, err = normalizeAuditReason("identity.role_assignment.granted", input.Reason, "role grant reason is invalid")
	if err != nil {
		return RoleAssignment{}, err
	}
	if actor.RealmID == "" {
		return RoleAssignment{}, E(CodeForbidden, "the authenticated identity realm cannot administer memberships")
	}
	if err := validateRoleScope(input.Role, input.NamespaceID); err != nil {
		return RoleAssignment{}, err
	}
	now := s.clock.Now()
	membershipID, err := s.ids.New(now)
	if err != nil {
		return RoleAssignment{}, Wrap(CodeInternal, "create membership identifier", err)
	}
	assignmentID, err := s.ids.New(now)
	if err != nil {
		return RoleAssignment{}, Wrap(CodeInternal, "create role-assignment identifier", err)
	}
	namespaceMembershipID := ""
	if input.NamespaceID != "" {
		namespaceMembershipID, err = s.ids.New(now)
		if err != nil {
			return RoleAssignment{}, Wrap(CodeInternal, "create namespace-membership identifier", err)
		}
	}
	evidence, err := s.evidence(ctx, now, actor, "identity.role_assignment.granted", "role_assignment", assignmentID, input.Reason, requestID, map[string]any{"membershipId": membershipID, "role": input.Role, "namespaceId": input.NamespaceID})
	if err != nil {
		return RoleAssignment{}, err
	}
	return s.store.GrantRole(ctx, GrantRoleParams{TenantID: actor.TenantID, RealmID: actor.RealmID, MembershipID: membershipID, NamespaceMembershipID: namespaceMembershipID, AssignmentID: assignmentID, Subject: input.Subject, ActorType: input.ActorType, Role: input.Role, NamespaceID: input.NamespaceID, Actor: actor.Subject, Reason: input.Reason, At: now, Evidence: evidence})
}

// RevokeRole applies a reasoned grant revocation under the expected assignment
// revision.
func (s *Service) RevokeRole(ctx context.Context, assignmentID, reason, requestID string, expectedRevision int64) (RoleAssignment, error) {
	ctx = WithAdministration(ctx)
	if actor, ok := FromContext(ctx); ok {
		if store, ok := s.store.(PermissionStore); ok {
			ctx = WithActor(ctx, BindPermissionStore(actor, store))
		}
	}
	actor, err := requireTenantAdministrator(ctx)
	if err != nil {
		return RoleAssignment{}, err
	}
	if !ValidTenantID(assignmentID) || expectedRevision <= 0 {
		return RoleAssignment{}, E(CodeInvalidArgument, "a role-assignment UUID, current revision, and bounded reason are required")
	}
	reason, err = normalizeAuditReason("identity.role_assignment.revoked", reason, "role revocation reason is invalid")
	if err != nil {
		return RoleAssignment{}, err
	}
	now := s.clock.Now()
	evidence, err := s.evidence(ctx, now, actor, "identity.role_assignment.revoked", "role_assignment", assignmentID, reason, requestID, map[string]any{"assignmentId": assignmentID})
	if err != nil {
		return RoleAssignment{}, err
	}
	return s.store.RevokeRole(ctx, RevokeRoleParams{TenantID: actor.TenantID, RealmID: actor.RealmID, AssignmentID: assignmentID, ExpectedRevision: expectedRevision, Actor: actor.Subject, Reason: reason, At: now, Evidence: evidence})
}

// requireTenantAdministrator distinguishes a missing authenticated membership
// from insufficient tenant-wide authority.
func requireTenantAdministrator(ctx context.Context) (Actor, error) {
	actor, ok := FromContext(ctx)
	if !ok || actor.TenantID == "" {
		return Actor{}, E(CodeUnauthenticated, "an authenticated Catalog member is required")
	}
	if _, err := RequirePermission(ctx, "access.manage", ""); err != nil {
		return Actor{}, err
	}
	return actor, nil
}

// evidence constructs validated membership attribution and the corresponding
// atomic outbox reference at one transition time.
func (s *Service) evidence(ctx context.Context, at time.Time, actor Actor, action, targetType, targetID, reason, requestID string, facts map[string]any) (ChangeEvidence, error) {
	reason, err := normalizeAuditReason(action, reason, "identity audit reason is invalid")
	if err != nil {
		return ChangeEvidence{}, err
	}
	auditID, err := s.ids.New(at)
	if err != nil {
		return ChangeEvidence{}, Wrap(CodeInternal, "create identity audit identifier", err)
	}
	outboxID, err := s.ids.New(at)
	if err != nil {
		return ChangeEvidence{}, Wrap(CodeInternal, "create identity outbox identifier", err)
	}
	eventType := events.CanonicalType(action)
	data, err := events.MarshalReference(action, targetType, targetID, "")
	if err != nil {
		return ChangeEvidence{}, Wrap(CodeInternal, "encode identity event", err)
	}
	var publishedAt *time.Time
	if s.acknowledgeOutboxAtCreation {
		value := at
		publishedAt = &value
	}

	execution := audit.ExecutionFrom(ctx)
	if execution.RequestID == "" {
		execution.RequestID = requestID
	}
	if execution.AuthorizationDigest == "" || len(RecordedPermissionChecks(ctx)) > 0 {
		execution.AuthorizationDigest = DecisionDigest(ctx)
	}
	record, recordErr := audit.NewRecord(auditID, actor.TenantID, audit.Principal{ID: actor.MembershipID, Type: audit.PrincipalCatalogMembership, ActorType: string(actor.ActorType), RealmID: actor.RealmID}, action, targetType, targetID, reason, execution, facts, at)
	if recordErr != nil {
		return ChangeEvidence{}, Wrap(CodeInternal, "construct identity audit event", recordErr)
	}
	if publishedAt != nil {
		value := record.At
		publishedAt = &value
	}
	return ChangeEvidence{FormatVersion: record.FormatVersion, AuditID: auditID, OutboxID: outboxID, Actor: record.Actor, Action: action, TargetType: targetType, TargetID: targetID, Reason: record.Reason, RequestID: record.RequestID, Details: record.Details, At: record.At, OutboxType: eventType, OutboxSubject: targetID, OutboxData: data, OutboxPublished: publishedAt}, nil

}

// normalizeAuditReason converts closed action-reason validation into a safe
// identity error.
func normalizeAuditReason(action, reason, message string) (string, error) {
	normalized, err := audit.NormalizeReason(action, reason)
	if err != nil {
		return "", Wrap(CodeInvalidArgument, message, err)
	}
	return normalized, nil
}
