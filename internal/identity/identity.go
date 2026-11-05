// Package identity resolves Catalog principals and enforces scoped permissions.
// Authentication supplies a [Principal]; compact resolution produces an [Actor]
// bound to retained membership. [Service] owns membership and policy changes
// with atomic evidence. Current permission checks use retained policy, not
// upstream claims, browser hints or legacy role projections.
package identity

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"regexp"
	"slices"
	"sort"
	"strings"
)

var tenantIDPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

type Role string

const (
	RoleReader           Role = "reader"
	RoleContributor      Role = "contributor"
	RoleReviewer         Role = "reviewer"
	RolePublisher        Role = "publisher"
	RoleSourceAdmin      Role = "source_admin"
	RoleSecurityReviewer Role = "security_reviewer"
	RoleTenantAdmin      Role = "tenant_admin"
	RoleServiceClient    Role = "service_client"
)

type Actor struct {
	permissionStore PermissionStore
	TenantID        string            `json:"tenantId"`
	Subject         string            `json:"subject"`
	Roles           []Role            `json:"roles"`
	Namespaces      map[string][]Role `json:"namespaces,omitempty"`
	Claims          map[string]string `json:"-"`
	Issuer          string            `json:"-"`
	RealmID         string            `json:"-"`
	MembershipID    string            `json:"-"`
	ActorType       ActorType         `json:"-"`
}

// ValidTenantID reports whether the value has the accepted UUID spelling.
func ValidTenantID(value string) bool { return tenantIDPattern.MatchString(value) }

// ValidRole reports whether the role belongs to the Catalog role vocabulary.
func ValidRole(role Role) bool {
	switch role {
	case RoleReader, RoleContributor, RoleReviewer, RolePublisher, RoleSourceAdmin, RoleSecurityReviewer, RoleTenantAdmin, RoleServiceClient:
		return true
	default:
		return false
	}
}

// HasRole inspects the legacy presentation projection. It is not current
// authorization; protected operations use RequirePermission against the store.
func (a Actor) HasRole(role Role, namespaceID string) bool {
	if slices.Contains(a.Roles, RoleTenantAdmin) || slices.Contains(a.Roles, role) {
		return true
	}
	return namespaceID != "" && slices.Contains(a.Namespaces[namespaceID], role)
}

// AuthorizationDigest binds identity, legacy projections and OAuth ceilings
// without retaining claims. Current decisions use compact identity plus the
// retained policy revision through DecisionDigest.
func (a Actor) AuthorizationDigest() string {
	hash := sha256.New()
	write := func(value string) {
		var size [8]byte
		binary.BigEndian.PutUint64(size[:], uint64(len(value)))
		_, _ = hash.Write(size[:])
		_, _ = hash.Write([]byte(value))
	}
	write("tenant")
	write(strings.ToLower(a.TenantID))
	write("subject")
	write(a.Subject)
	write("realm")
	write(a.RealmID)
	write("membership")
	write(a.MembershipID)
	write("actor-type")
	write(string(a.ActorType))
	for _, role := range canonicalRoles(a.Roles) {
		write("global-role")
		write(role)
	}
	namespaceIDs := make([]string, 0, len(a.Namespaces))
	for namespaceID := range a.Namespaces {
		namespaceIDs = append(namespaceIDs, namespaceID)
	}
	sort.Strings(namespaceIDs)
	for _, namespaceID := range namespaceIDs {
		roles := canonicalRoles(a.Namespaces[namespaceID])
		if len(roles) == 0 {
			continue
		}
		write("namespace")
		write(namespaceID)
		for _, role := range roles {
			write("namespace-role")
			write(role)
		}
	}
	claimNames := make([]string, 0, len(a.Claims))
	for name := range a.Claims {
		claimNames = append(claimNames, name)
	}
	sort.Strings(claimNames)
	for _, name := range claimNames {
		write("claim")
		write(name)
		write(a.Claims[name])
	}
	return "sha256:" + hex.EncodeToString(hash.Sum(nil))
}

// canonicalRoles returns sorted unique role spellings for stable authorization
// fingerprints.
func canonicalRoles(roles []Role) []string {
	values := make([]string, 0, len(roles))
	for _, role := range roles {
		values = append(values, string(role))
	}
	sort.Strings(values)
	return slices.Compact(values)
}

type contextKey struct{}

// WithActor attaches a resolved Catalog actor to the downstream authorization
// context.
func WithActor(ctx context.Context, actor Actor) context.Context {
	return context.WithValue(ctx, contextKey{}, actor)
}

// FromContext returns the attached actor and reports whether one is present.
func FromContext(ctx context.Context) (Actor, bool) {
	actor, ok := ctx.Value(contextKey{}).(Actor)
	return actor, ok
}
