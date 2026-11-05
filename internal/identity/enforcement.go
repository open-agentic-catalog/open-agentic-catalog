package identity

import (
	"context"
	"crypto/sha256"
	"fmt"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
)

type permissionContextKey struct{}
type administrationKey struct{}
type currentAdmissionKey struct{}
type currentAdmission struct{ active atomic.Bool }

// WithAdministration selects the exclusive policy-writer admission path.
func WithAdministration(ctx context.Context) context.Context {
	return context.WithValue(WithPermissionContext(ctx), administrationKey{}, true)
}

func IsAdministration(ctx context.Context) bool {
	value, _ := ctx.Value(administrationKey{}).(bool)
	return value
}

// PermissionContext is bounded, request-owned decision evidence. It is never a
// cache: each admission rechecks the retained actions against current policy.
type permissionContext struct {
	mu       sync.Mutex
	checks   []PermissionCheck
	revision int64
	jobID    string
}

// WithPermissionContext starts a domain operation or joins its enclosing HTTP
// command. Preparatory outbound work must finish before database admission.
func WithPermissionContext(ctx context.Context) context.Context {
	if _, ok := ctx.Value(permissionContextKey{}).(*permissionContext); ok {
		return ctx
	}
	return context.WithValue(ctx, permissionContextKey{}, &permissionContext{})
}

// BindPermissionStore is a composition boundary, not a grant. The store still
// validates the exact active membership on every decision. Network inputs
// cannot supply this handle or substitute Actor role projections for policy.
func BindPermissionStore(actor Actor, store PermissionStore) Actor {
	actor.permissionStore = store
	return actor
}

// RequirePermission checks an explicit action at its canonical scope. Missing
// composition fails closed, including actors carrying forged administrator roles.
func RequirePermission(ctx context.Context, permission Permission, namespaceID string) (Actor, error) {
	if a, internal, err := internalPermission(ctx, permission, namespaceID); internal {
		return a, err
	}
	a, ok := FromContext(ctx)
	if !ok || a.permissionStore == nil {
		return Actor{}, E(CodeForbidden, "current Catalog authority is required")
	}
	check := PermissionCheck{Permission: permission, NamespaceID: namespaceID}
	d, err := a.permissionStore.CheckPermissions(ctx, a, []PermissionCheck{check})
	if err != nil {
		if ErrorCodeOf(err) == CodeUnauthenticated {
			return Actor{}, E(CodeForbidden, "a current Catalog principal is required")
		}
		return Actor{}, err
	}
	if len(d.Allowed) != 1 || !d.Allowed[0] {
		return Actor{}, E(CodeForbidden, "current permission denied the operation")
	}
	if state, ok := ctx.Value(permissionContextKey{}).(*permissionContext); ok {
		state.mu.Lock()
		defer state.mu.Unlock()
		if !slices.Contains(state.checks, check) {
			if len(state.checks) == MaxPermissionChecks {
				return Actor{}, E(CodeInvalidArgument, "permission admission exceeds the supported bound")
			}
			state.checks = append(state.checks, check)
		}
		state.revision = d.Revision
	}
	return a, nil
}

// RecordedPermissionChecks returns detached operation checks for the database
// fence and the server-owned replay description. It never trusts retry bodies.
func RecordedPermissionChecks(ctx context.Context) []PermissionCheck {
	state, _ := ctx.Value(permissionContextKey{}).(*permissionContext)
	if state == nil {
		return nil
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	return slices.Clone(state.checks)
}

// SetPermissionRevision records the revision selected by actual transaction
// admission, replacing a preparatory decision's revision.
func SetPermissionRevision(ctx context.Context, revision int64) {
	if state, ok := ctx.Value(permissionContextKey{}).(*permissionContext); ok {
		state.mu.Lock()
		defer state.mu.Unlock()
		state.revision = revision
	}
}

// RecordJobDisclosure binds a retained reply to the actual original Job. The
// retry's URL or body must never select a different target for authorization.
func RecordJobDisclosure(ctx context.Context, id string) {
	if state, ok := ctx.Value(permissionContextKey{}).(*permissionContext); ok {
		state.mu.Lock()
		defer state.mu.Unlock()
		state.jobID = id
	}
}

func RecordedJobDisclosure(ctx context.Context) string {
	state, _ := ctx.Value(permissionContextKey{}).(*permissionContext)
	if state == nil {
		return ""
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	return state.jobID
}

// CommandPrincipal is stable across grant changes but separates equal subjects
// in different realms and different memberships. Roles and scopes are excluded.
func (a Actor) CommandPrincipal() string {
	return "membership:" + a.RealmID + ":" + a.MembershipID
}

func (a Actor) CommandDigest() string {
	sum := sha256.Sum256([]byte(strings.ToLower(a.TenantID) + ":" + a.CommandPrincipal()))
	return fmt.Sprintf("sha256:%x", sum)
}

// WithReplayDisclosure holds current policy admission while emitting retained
// bytes. This does not execute the mutation or recheck its old lifecycle/ETag.
func WithReplayDisclosure(ctx context.Context, checks []PermissionCheck, emit func(context.Context) error) error {
	a, ok := FromContext(ctx)
	if !ok || a.permissionStore == nil {
		return E(CodeForbidden, "current Catalog authority is required")
	}
	return a.permissionStore.WithPermissionAdmission(ctx, a, checks, emit)
}

// NeedsPermissionAdmission selects the owning in-memory store's mutation
// boundary. Repository-only fixtures and owner-only provisioning have no
// recorded user checks. Nested bounded work joins the same active admission.
func NeedsPermissionAdmission(ctx context.Context) bool {
	guard, _ := ctx.Value(currentAdmissionKey{}).(*currentAdmission)
	return len(RecordedPermissionChecks(ctx)) > 0 && (guard == nil || !guard.active.Load())
}

// WithCurrentPermissionAdmission holds current policy through local bounded
// repository work. It must never wrap a remote fetch or an interactive callback.
func WithCurrentPermissionAdmission(ctx context.Context, work func(context.Context) error) error {
	return WithReplayDisclosure(ctx, RecordedPermissionChecks(ctx), func(bound context.Context) error {
		guard := &currentAdmission{}
		guard.active.Store(true)
		defer func() { guard.active.Store(false) }()
		return work(context.WithValue(bound, currentAdmissionKey{}, guard))
	})
}

// DecisionDigest binds evidence/cursors to current policy semantics and OAuth
// ceilings; unlike command identity it deliberately changes on policy changes.
func DecisionDigest(ctx context.Context) string {
	a, _ := FromContext(ctx)
	// Role projections are presentation only. Policy revision owns decisions.
	a.Roles, a.Namespaces = nil, nil
	var revision int64
	if state, ok := ctx.Value(permissionContextKey{}).(*permissionContext); ok {
		state.mu.Lock()
		revision = state.revision
		state.mu.Unlock()
	}
	sum := sha256.Sum256([]byte(fmt.Sprintf("catalog-permissions-v1:%s:%s:%d:%s", a.TenantID, a.CommandPrincipal(), revision, a.AuthorizationDigest())))
	return fmt.Sprintf("sha256:%x", sum)
}

// CurrentDecisionDigest obtains a current revision for continuation parsing
// before the domain selects its canonical target. A denied probe is not a grant;
// the owning operation must still enforce its actual action and scope.
func CurrentDecisionDigest(ctx context.Context) (string, error) {
	a, ok := FromContext(ctx)
	if !ok || a.permissionStore == nil {
		return "", E(CodeForbidden, "current Catalog authority is required")
	}
	ctx = WithPermissionContext(ctx)
	d, err := a.permissionStore.CheckPermissions(ctx, a, []PermissionCheck{{Permission: "access.manage"}})
	if err != nil {
		return "", err
	}
	if d.Revision < 1 || len(d.Allowed) != 1 {
		return "", E(CodeDependency, "current decision revision is invalid")
	}
	SetPermissionRevision(ctx, d.Revision)
	return DecisionDigest(ctx), nil
}
