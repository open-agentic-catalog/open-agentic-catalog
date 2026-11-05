// Permission vocabulary and built-in templates are service-owned policy, not configuration.
package identity

import (
	"context"
	"slices"
	"strings"
)

type Permission string
type PermissionDescriptor struct {
	Key             Permission   `json:"key"`
	NamespaceScoped bool         `json:"namespaceScoped"`
	Delegable       bool         `json:"delegable"`
	Requires        []Permission `json:"requires"`
}
type PermissionCheck struct {
	Permission  Permission `json:"permission"`
	NamespaceID string     `json:"namespaceId,omitempty"`
}
type PermissionDecision struct {
	Revision int64  `json:"authorizationRevision"`
	Allowed  []bool `json:"catalogAllowed"`
}

const MaxPermissionChecks = 64

// MaxDevelopmentRoleTemplates bounds exact development seeds to the current
// finite built-in roster, shared by configuration and provisioning admission.
const MaxDevelopmentRoleTemplates = 14

var permissionRegistry = []PermissionDescriptor{
	{Key: "library.own", Delegable: true, Requires: []Permission{}},
	{Key: "settings.read", Delegable: true, Requires: []Permission{}},
	{Key: "settings.manage", Delegable: true, Requires: []Permission{"settings.read"}},
	{Key: "ratings.own", NamespaceScoped: true, Delegable: true, Requires: []Permission{}},
	{Key: "ratings.moderate", Delegable: true, Requires: []Permission{}},
	{Key: "ratings.sharing.manage", Delegable: true, Requires: []Permission{}},
	{Key: "resource.read", NamespaceScoped: true, Delegable: true, Requires: []Permission{}},
	{Key: "resource.read_history", NamespaceScoped: true, Delegable: true, Requires: []Permission{}},
	{Key: "resource.read_content", NamespaceScoped: true, Delegable: true, Requires: []Permission{}},
	{Key: "resource.create", NamespaceScoped: true, Delegable: true, Requires: []Permission{"namespace.read"}},
	{Key: "resource.edit", NamespaceScoped: true, Delegable: true, Requires: []Permission{"resource.read", "resource.read_history", "resource.read_content"}},
	{Key: "resource.delete_draft", NamespaceScoped: true, Delegable: true, Requires: []Permission{"resource.read"}},
	{Key: "resource.validate", NamespaceScoped: true, Delegable: true, Requires: []Permission{"resource.read_content"}},
	{Key: "review.submit", NamespaceScoped: true, Delegable: true, Requires: []Permission{"resource.read_history", "resource.read_content"}},
	{Key: "review.decide", NamespaceScoped: true, Delegable: true, Requires: []Permission{"resource.read_history", "resource.read_content"}},
	{Key: "release.publish", NamespaceScoped: true, Delegable: true, Requires: []Permission{"resource.read_history", "resource.read_content"}},
	{Key: "release.transition", NamespaceScoped: true, Delegable: true, Requires: []Permission{"resource.read"}},
	{Key: "namespace.read", NamespaceScoped: true, Delegable: true, Requires: []Permission{}},
	{Key: "namespace.manage", NamespaceScoped: true, Delegable: true, Requires: []Permission{"namespace.read"}},
	{Key: "namespace.create", NamespaceScoped: false, Delegable: true, Requires: []Permission{}},
	{Key: "publisher.read", NamespaceScoped: false, Delegable: true, Requires: []Permission{}},
	{Key: "publisher.create", NamespaceScoped: false, Delegable: true, Requires: []Permission{"publisher.read"}},
	{Key: "publisher.manage", NamespaceScoped: false, Delegable: true, Requires: []Permission{"publisher.read"}},
	{Key: "publisher.approve_domain", NamespaceScoped: false, Delegable: true, Requires: []Permission{"publisher.read"}},
	{Key: "federation.discover", NamespaceScoped: false, Delegable: true, Requires: []Permission{}},
	{Key: "source.read", NamespaceScoped: false, Delegable: true, Requires: []Permission{}},
	{Key: "source.inspect", NamespaceScoped: false, Delegable: true, Requires: []Permission{"source.read"}},
	{Key: "source.manage", NamespaceScoped: false, Delegable: true, Requires: []Permission{"source.read", "source.inspect"}},
	{Key: "source.sync", NamespaceScoped: false, Delegable: true, Requires: []Permission{"source.read"}},
	{Key: "source.read_evidence", NamespaceScoped: false, Delegable: true, Requires: []Permission{"source.read"}},
	{Key: "publication.read", NamespaceScoped: false, Delegable: true, Requires: []Permission{}},
	{Key: "publication.generate", NamespaceScoped: false, Delegable: true, Requires: []Permission{"publication.read"}},
	{Key: "publication.activate", NamespaceScoped: false, Delegable: true, Requires: []Permission{"publication.read"}},
	{Key: "publication.rollback", NamespaceScoped: false, Delegable: true, Requires: []Permission{"publication.read"}},
	{Key: "publication.unpublish", NamespaceScoped: false, Delegable: true, Requires: []Permission{"publication.read"}},
	{Key: "discovery.read", NamespaceScoped: false, Delegable: true, Requires: []Permission{}},
	{Key: "job.read", NamespaceScoped: false, Delegable: true, Requires: []Permission{}},
	{Key: "job.cancel", NamespaceScoped: false, Delegable: true, Requires: []Permission{"job.read"}},
	{Key: "job.retry", NamespaceScoped: false, Delegable: true, Requires: []Permission{"job.read"}},
	{Key: "audit.read", NamespaceScoped: false, Delegable: true, Requires: []Permission{}},
	{Key: "audit.export", NamespaceScoped: false, Delegable: true, Requires: []Permission{"audit.read"}},
	{Key: "webhook.read", NamespaceScoped: false, Delegable: true, Requires: []Permission{}},
	{Key: "webhook.manage", NamespaceScoped: false, Delegable: true, Requires: []Permission{"webhook.read"}},
	{Key: "webhook.test", NamespaceScoped: false, Delegable: true, Requires: []Permission{"webhook.read"}},
	{Key: "webhook.rotate", NamespaceScoped: false, Delegable: true, Requires: []Permission{"webhook.read"}},
	{Key: "webhook.read_evidence", NamespaceScoped: false, Delegable: true, Requires: []Permission{"webhook.read"}},
	{Key: "feedback.own", NamespaceScoped: false, Delegable: true, Requires: []Permission{}},
	{Key: "feedback.moderate", NamespaceScoped: false, Delegable: true, Requires: []Permission{}},
	{Key: "operations.reindex", NamespaceScoped: false, Delegable: true, Requires: []Permission{}},
	{Key: "access.manage", NamespaceScoped: false, Delegable: false, Requires: []Permission{}},
}
var builtinPermissions = map[string][]Permission{
	"reader":            {"ratings.own", "resource.read", "resource.read_history", "resource.read_content", "namespace.read"},
	"contributor":       {"ratings.own", "resource.read", "resource.read_history", "resource.read_content", "namespace.read", "resource.create", "resource.edit", "resource.delete_draft", "resource.validate", "review.submit"},
	"reviewer":          {"ratings.own", "resource.read", "resource.read_history", "resource.read_content", "namespace.read", "review.decide"},
	"publisher":         {"ratings.own", "resource.read", "resource.read_history", "resource.read_content", "namespace.read", "release.publish", "release.transition"},
	"discovery_reader":  {"library.own", "ratings.own", "federation.discover", "discovery.read", "publisher.read", "job.read"},
	"publisher_creator": {"publisher.read", "publisher.create"},
	"publication_admin": {"publication.read", "publication.generate", "publication.activate", "publication.rollback", "publication.unpublish", "job.read", "job.cancel", "job.retry"},
	"source_admin":      {"source.read", "source.inspect", "source.manage", "source.sync", "source.read_evidence", "federation.discover", "job.read", "job.cancel", "job.retry"},
	"security_reviewer": {"audit.read", "audit.export"},
	"webhook_admin":     {"webhook.read", "webhook.manage", "webhook.test", "webhook.rotate", "webhook.read_evidence"},
	"library_member":    {"library.own"},
	"feedback_member":   {"feedback.own"},
	"tenant_admin":      {"library.own", "settings.read", "settings.manage", "ratings.moderate", "ratings.sharing.manage", "ratings.own", "resource.read", "resource.read_history", "resource.read_content", "resource.create", "resource.edit", "resource.delete_draft", "resource.validate", "review.submit", "review.decide", "release.publish", "release.transition", "namespace.read", "namespace.manage", "namespace.create", "publisher.read", "publisher.create", "publisher.manage", "publisher.approve_domain", "federation.discover", "source.read", "source.inspect", "source.manage", "source.sync", "source.read_evidence", "publication.read", "publication.generate", "publication.activate", "publication.rollback", "publication.unpublish", "discovery.read", "job.read", "job.cancel", "job.retry", "audit.read", "audit.export", "webhook.read", "webhook.manage", "webhook.test", "webhook.rotate", "webhook.read_evidence", "feedback.own", "feedback.moderate", "operations.reindex", "access.manage"},
	"service_client":    {},
}

// Permissions returns a detached description of the finite action vocabulary.
func Permissions() []PermissionDescriptor {
	out := slices.Clone(permissionRegistry)
	for i := range out {
		out[i].Requires = append([]Permission{}, out[i].Requires...)
	}
	return out
}

// BuiltinPermissions returns explicit templates; no role-name hierarchy is inferred.
func BuiltinPermissions(key string) ([]Permission, bool) {
	values, ok := builtinPermissions[key]
	return slices.Clone(values), ok
}

// ValidatePermissionChecks rejects unknown actions, malformed scopes and unbounded work.
func ValidatePermissionChecks(checks []PermissionCheck) error {
	if len(checks) == 0 || len(checks) > MaxPermissionChecks {
		return E(CodeInvalidArgument, "permission checks must contain between 1 and 64 actions")
	}
	descriptors := Permissions()
	for _, check := range checks {
		found := false
		for _, descriptor := range descriptors {
			if descriptor.Key != check.Permission {
				continue
			}
			found = true
			if check.NamespaceID != "" && (!descriptor.NamespaceScoped || !ValidTenantID(check.NamespaceID)) {
				return E(CodeInvalidArgument, "permission scope is invalid")
			}
			break
		}
		if !found {
			return E(CodeInvalidArgument, "permission is unknown")
		}
	}
	return nil
}

// PermissionStore is the current authority boundary; OAuth claims and Actor role
// projections are not permission data. An admitted callback must not call out.
type PermissionStore interface {
	ResolvePrincipal(context.Context, Principal) (Actor, error)
	CheckPermissions(context.Context, Actor, []PermissionCheck) (PermissionDecision, error)
	WithPermissionAdmission(context.Context, Actor, []PermissionCheck, func(context.Context) error) error
}

// ValidatePermissionActor validates the compact principal before any I/O.
func ValidatePermissionActor(actor Actor) error {
	if !ValidTenantID(actor.TenantID) || !ValidTenantID(actor.MembershipID) || !ValidTenantID(actor.RealmID) || !ValidSubject(actor.Subject) || !ValidActorType(actor.ActorType) {
		return E(CodeUnauthenticated, "current Catalog principal is required")
	}
	return nil
}

// BindPermissionActor keeps admission and execution attribution on the same
// principal. Existing authenticated annotations remain owned by that context.
func BindPermissionActor(ctx context.Context, actor Actor) (context.Context, error) {
	if err := ValidatePermissionActor(actor); err != nil {
		return nil, err
	}
	current, ok := FromContext(ctx)
	if !ok {
		return WithActor(ctx, actor), nil
	}
	if !strings.EqualFold(current.TenantID, actor.TenantID) || !strings.EqualFold(current.RealmID, actor.RealmID) || !strings.EqualFold(current.MembershipID, actor.MembershipID) || current.Subject != actor.Subject || current.ActorType != actor.ActorType {
		return nil, E(CodeForbidden, "admission principal disagrees with authenticated context")
	}
	return ctx, nil
}
