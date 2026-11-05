// Membership contracts bind authenticated subjects to retained Catalog identities.
// Built-in assignments and scoped permissions are independent of upstream OAuth scopes.

package identity

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

type ActorType string

const (
	ActorHuman   ActorType = "human"
	ActorService ActorType = "service"
)

// ValidActorType reports whether the principal is a supported human or
// service-account actor.
func ValidActorType(value ActorType) bool {
	return value == ActorHuman || value == ActorService
}

type Principal struct {
	Issuer               string
	Subject              string
	TenantID             string
	ExternalOrganization string
	Claims               map[string]string
}

// Resolver maps a verified external principal to an active Catalog membership
// without hydrating its authorization grants.
type Resolver interface {
	ResolvePrincipal(context.Context, Principal) (Actor, error)
}

type RealmMode string

const (
	RealmDedicated RealmMode = "dedicated"
	RealmShared    RealmMode = "shared"
)

type RealmState string

const (
	RealmActive    RealmState = "active"
	RealmSuspended RealmState = "suspended"
)

type Realm struct {
	ID                   string     `json:"id"`
	TenantID             string     `json:"tenantId"`
	Issuer               string     `json:"issuer"`
	TenancyMode          RealmMode  `json:"tenancyMode"`
	ExternalOrganization string     `json:"externalOrganization,omitempty"`
	State                RealmState `json:"state"`
	Revision             int64      `json:"revision"`
	CreatedAt            time.Time  `json:"createdAt"`
	UpdatedAt            time.Time  `json:"updatedAt"`
}

type MembershipState string

const (
	MembershipActive     MembershipState = "active"
	MembershipSuspended  MembershipState = "suspended"
	MembershipOffboarded MembershipState = "offboarded"
)

type Membership struct {
	ID        string          `json:"id"`
	TenantID  string          `json:"tenantId"`
	RealmID   string          `json:"realmId"`
	Subject   string          `json:"subject"`
	ActorType ActorType       `json:"actorType"`
	State     MembershipState `json:"state"`
	Revision  int64           `json:"revision"`
	CreatedAt time.Time       `json:"createdAt"`
	UpdatedAt time.Time       `json:"updatedAt"`
}

type MembershipPage struct {
	Items      []Membership `json:"items"`
	NextCursor string       `json:"nextCursor,omitempty"`
}

// MembershipFilter selects exact current directory fields with AND semantics.
type MembershipFilter struct {
	Subject   string
	State     MembershipState
	ActorType ActorType
	RealmID   string
}

type MembershipLifecycleResult struct {
	Membership                  Membership `json:"membership"`
	RevokedRoleAssignments      int        `json:"revokedRoleAssignments"`
	RevokedNamespaceMemberships int        `json:"revokedNamespaceMemberships"`
	RevokedGroupMemberships     int        `json:"revokedGroupMemberships,omitempty"`
}

type AssignmentState string

const (
	AssignmentActive  AssignmentState = "active"
	AssignmentRevoked AssignmentState = "revoked"
)

type RoleAssignment struct {
	ID           string          `json:"id"`
	TenantID     string          `json:"tenantId"`
	MembershipID string          `json:"membershipId"`
	Subject      string          `json:"subject"`
	ActorType    ActorType       `json:"actorType"`
	Role         Role            `json:"role"`
	NamespaceID  string          `json:"namespaceId,omitempty"`
	State        AssignmentState `json:"state"`
	Revision     int64           `json:"revision"`
	GrantedBy    string          `json:"grantedBy"`
	GrantReason  string          `json:"grantReason"`
	GrantedAt    time.Time       `json:"grantedAt"`
	RevokedBy    string          `json:"revokedBy,omitempty"`
	RevokeReason string          `json:"revokeReason,omitempty"`
	RevokedAt    *time.Time      `json:"revokedAt,omitempty"`
}

type RoleAssignmentListRequest struct {
	MembershipID     string
	AfterSubject     string
	AfterNamespaceID string
	AfterRole        Role
	AfterID          string
	Limit            int
}

type ChangeEvidence struct {
	FormatVersion   *int
	AuditID         string
	OutboxID        string
	Actor           string
	Action          string
	TargetType      string
	TargetID        string
	Reason          string
	RequestID       string
	Details         map[string]any
	At              time.Time
	OutboxType      string
	OutboxSubject   string
	OutboxData      []byte
	OutboxPublished *time.Time
}

// EvidenceSink joins identity-owned in-memory transitions to the composition's
// shared audit and outbox inventory. PostgreSQL stores persist the same
// ChangeEvidence directly in their owning transaction.
type EvidenceSink interface {
	AppendIdentityEvidence(context.Context, string, ChangeEvidence) error
}

type GrantRoleParams struct {
	TenantID              string
	RealmID               string
	MembershipID          string
	NamespaceMembershipID string
	AssignmentID          string
	Subject               string
	ActorType             ActorType
	Role                  Role
	NamespaceID           string
	Actor                 string
	Reason                string
	At                    time.Time
	Evidence              ChangeEvidence
}

type RevokeRoleParams struct {
	TenantID         string
	RealmID          string
	AssignmentID     string
	ExpectedRevision int64
	Actor            string
	Reason           string
	At               time.Time
	Evidence         ChangeEvidence
}

type MembershipTransition string

const (
	MembershipSuspend    MembershipTransition = "suspend"
	MembershipReactivate MembershipTransition = "reactivate"
	MembershipOffboard   MembershipTransition = "offboard"
)

type ChangeMembershipParams struct {
	TenantID         string
	MembershipID     string
	ExpectedRevision int64
	Transition       MembershipTransition
	Actor            string
	Reason           string
	At               time.Time
	Evidence         ChangeEvidence
}

// Store is the identity system-of-record boundary. Lifecycle changes and role
// changes carry evidence that must commit with the authorization state.
type Store interface {
	Resolver
	ListMemberships(context.Context, string, string, string, int, ...MembershipFilter) ([]Membership, error)
	GetMembership(context.Context, string, string) (Membership, error)
	ChangeMembership(context.Context, ChangeMembershipParams) (MembershipLifecycleResult, error)
	ListRoleAssignments(context.Context, string, RoleAssignmentListRequest) ([]RoleAssignment, error)
	GrantRole(context.Context, GrantRoleParams) (RoleAssignment, error)
	RevokeRole(context.Context, RevokeRoleParams) (RoleAssignment, error)
}

type ErrorCode string

const (
	CodeInvalidArgument    ErrorCode = "INVALID_ARGUMENT"
	CodeUnauthenticated    ErrorCode = "UNAUTHENTICATED"
	CodeForbidden          ErrorCode = "FORBIDDEN"
	CodeNotFound           ErrorCode = "NOT_FOUND"
	CodeConflict           ErrorCode = "CONFLICT"
	CodePreconditionFailed ErrorCode = "PRECONDITION_FAILED"
	CodeSeparationOfDuties ErrorCode = "SEPARATION_OF_DUTIES"
	CodeDependency         ErrorCode = "DEPENDENCY_UNAVAILABLE"
	CodeInternal           ErrorCode = "INTERNAL"
)

type Error struct {
	Code    ErrorCode
	Message string
	Cause   error
}

// Error returns the safe message, or an empty string for a nil receiver.
func (e *Error) Error() string {
	if e == nil {
		return ""
	}
	return e.Message
}

// Unwrap exposes the internal cause for error classification.
func (e *Error) Unwrap() error { return e.Cause }

// E constructs an identity error with a client-safe message.
func E(code ErrorCode, message string) error { return &Error{Code: code, Message: message} }

// Wrap retains a diagnostic cause separately from the client-safe identity
// message.
func Wrap(code ErrorCode, message string, cause error) error {
	return &Error{Code: code, Message: message, Cause: cause}
}

// ErrorCodeOf unwraps identity errors and classifies other failures as
// internal.
func ErrorCodeOf(err error) ErrorCode {
	var typed *Error
	if errors.As(err, &typed) {
		return typed.Code
	}
	return CodeInternal
}

// ValidSubject reports whether an issuer subject is nonempty, bounded and free
// of control characters.
func ValidSubject(value string) bool {
	if value == "" || len(value) > 512 || !utf8.ValidString(value) || strings.TrimSpace(value) != value {
		return false
	}
	for _, character := range value {
		if character < 0x20 || character == 0x7f {
			return false
		}
	}
	return true
}

// validSubject shares the public issuer-subject syntax contract with internal
// membership validation.
func validSubject(value string) bool { return ValidSubject(value) }

// validNamespaceRole reports whether a Catalog role may be scoped to one
// namespace.
func validNamespaceRole(role Role) bool {
	switch role {
	case RoleReader, RoleContributor, RoleReviewer, RolePublisher:
		return true
	case RoleSourceAdmin, RoleSecurityReviewer, RoleTenantAdmin, RoleServiceClient:
		return false
	default:
		return false
	}
}

// validateRoleScope rejects unsupported roles and namespace bindings for
// tenant-only grants.
func validateRoleScope(role Role, namespaceID string) error {
	if !ValidRole(role) {
		return E(CodeInvalidArgument, "role is invalid")
	}
	if namespaceID == "" {
		return nil
	}
	if !ValidTenantID(namespaceID) {
		return E(CodeInvalidArgument, "namespaceId must be a UUID")
	}
	if !validNamespaceRole(role) {
		return E(CodeInvalidArgument, fmt.Sprintf("role %q cannot be assigned to a namespace", role))
	}
	return nil
}
