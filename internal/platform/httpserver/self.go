// Caller introspection reports bounded current permissions and OAuth ceilings.

package httpserver

import (
	"context"
	"net/http"
	"net/url"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
)

type selfResponse struct {
	TenantID              string                               `json:"tenantId"`
	Subject               string                               `json:"subject"`
	MembershipID          string                               `json:"membershipId"`
	RealmID               string                               `json:"realmId"`
	ActorType             identity.ActorType                   `json:"actorType"`
	Permissions           []identity.Permission                `json:"permissions"`
	AvailablePermissions  []identity.Permission                `json:"availablePermissions"`
	AuthorizationRevision int64                                `json:"authorizationRevision"`
	NamespaceGrants       []identity.NamespacePermissionAccess `json:"namespaceGrants"`
	NextCursor            string                               `json:"nextCursor,omitempty"`
	ScopeEnforcement      bool                                 `json:"scopeEnforcement"`
	Scopes                []string                             `json:"scopes"`
}

// handleSelf exposes only the caller's current resolved access. No product role
// is needed to discover why a workspace is unavailable.
func (s *Server) handleSelf(w http.ResponseWriter, r *http.Request) {
	query, err := parseQueryParameters(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	reply, err := s.ReadAccess(r.Context(), query)
	s.respondRead(w, r, reply, err)
}

func (s *Server) selfRead(ctx context.Context, query url.Values) (selfResponse, error) {
	// Keep cursor parsing and projection in one request-owned revision state.
	ctx = identity.WithPermissionContext(ctx)
	filters := map[string]string{}
	namespaceID := query.Get("namespaceId")
	if namespaceID != "" {
		if !identity.ValidTenantID(namespaceID) {
			return selfResponse{}, catalog.E(catalog.CodeInvalidArgument, "namespaceId must be a UUID")
		}
		filters["namespaceId"] = namespaceID
	}
	p, err := s.parseAdminValues(ctx, query, "self-access", "", "namespaceId", filters)
	if err != nil {
		return selfResponse{}, err
	}
	actor, _ := identity.FromContext(ctx)
	after := ""
	if len(p.Position) > 0 {
		if len(p.Position) != 1 || !identity.ValidTenantID(p.Position[0]) {
			return selfResponse{}, catalog.E(catalog.CodeInvalidArgument, "cursor is invalid")
		}
		after = p.Position[0]
	}
	projection, err := identity.CurrentSelfAccess(ctx, identity.SelfAccessRequest{NamespaceID: namespaceID, AfterNamespaceID: after, Limit: p.Limit + 1})
	if err != nil {
		return selfResponse{}, catalogIdentityError(err)
	}
	if identity.DecisionDigest(ctx) != p.binding.AuthorizationDigest {
		return selfResponse{}, catalog.E(catalog.CodeConflict, "authorization changed; restart this page")
	}
	items := projection.NamespaceGrants
	page, err := finishCursorPage(s, p, items, func(item identity.NamespacePermissionAccess) []string { return []string{item.NamespaceID} })
	if err != nil {
		return selfResponse{}, err
	}
	scopes := make([]string, 0)
	if s.enforceOAuthScopes {
		for _, scope := range []string{"audit.export", "audit.read", "catalog.publish", "catalog.read", "catalog.review", "catalog.write", "feedback.write", "personal.write", "publishers.manage", "publishers.read", "ratings.write", "sources.manage", "sources.read", "tenant.admin", "webhooks.manage"} {
			if actor.Claims["scope:"+scope] == "true" {
				scopes = append(scopes, scope)
			}
		}
	}
	return selfResponse{TenantID: actor.TenantID, Subject: actor.Subject, MembershipID: actor.MembershipID, RealmID: actor.RealmID, ActorType: actor.ActorType, Permissions: projection.Permissions, AvailablePermissions: projection.AvailablePermissions, AuthorizationRevision: projection.AuthorizationRevision, NamespaceGrants: page.Items, NextCursor: page.NextCursor, ScopeEnforcement: s.enforceOAuthScopes, Scopes: scopes}, nil
}
