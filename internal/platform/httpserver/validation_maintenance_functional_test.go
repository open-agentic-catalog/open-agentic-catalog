//go:build functional

// Real HTTP maintenance uses the existing explicit, idempotent renewal command.
package httpserver

import (
	"context"
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/adapter"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
)

func TestHTTPValidationMaintenanceAuthenticationAndOAuthCeiling(t *testing.T) {
	s := newConfiguredHTTPServer(t, func(d *Dependencies) {
		d.EnforceOAuthScopes = true
		authenticator := d.Authenticator
		d.Authenticator = discoveryAdmissionAuthenticator(func(ctx context.Context, token string) (identity.Actor, error) {
			name, scope, _ := strings.Cut(token, "/")
			actor, err := authenticator.Authenticate(ctx, name)
			actor.Claims = map[string]string{"scope:" + scope: "true"}
			return actor, err
		})
	})
	defer s.Close()
	const path = "/api/v1/validation-maintenance"
	requireControlProblem(t, requestJSON(t, s.URL, http.MethodGet, path, "", "", "", nil), http.StatusUnauthorized, catalog.CodeUnauthenticated)
	for _, scope := range []string{"", "catalog.write", "tenant.admin"} {
		denied := requestJSON(t, s.URL, http.MethodGet, path, "admin/"+scope, "", "", nil)
		requireControlProblem(t, denied, http.StatusForbidden, catalog.CodeForbidden)
		if denied.header.Get("WWW-Authenticate") != `Bearer error="insufficient_scope", scope="catalog.read"` {
			t.Fatal("maintenance read did not enforce its independent OAuth ceiling", denied.header)
		}
	}
	allowed := requestJSON(t, s.URL, http.MethodGet, path, "admin/catalog.read", "", "", nil)
	requireStatus(t, allowed, http.StatusOK)
	requireControlSchema(t, "ValidationMaintenancePage", allowed.body)
}

func TestHTTPValidationMaintenanceExplicitRenewalAndPrivateScope(t *testing.T) {
	var authority *identity.Service
	var authenticate func(context.Context, string) (identity.Actor, error)
	s := newConfiguredHTTPServer(t, func(d *Dependencies) {
		authority = d.Identity
		authenticate = d.Authenticator.Authenticate
	})
	defer s.Close()
	owner, err := authenticate(t.Context(), "admin")
	if err != nil {
		t.Fatal(err)
	}
	ownerContext := identity.WithActor(t.Context(), owner)
	validator, err := authenticate(t.Context(), "contributor")
	if err != nil {
		t.Fatal(err)
	}
	created := requestJSON(t, s.URL, "POST", "/api/v1/namespaces", "admin", "maintenance-ns", "", map[string]any{"slug": "maintenance", "displayName": "Maintenance"})
	requireStatus(t, created, 201)
	var ns catalog.Namespace
	decodeResult(t, created, &ns)
	created = requestJSON(t, s.URL, "POST", "/api/v1/resources", "contributor", "maintenance-resource", "", map[string]any{"namespaceId": ns.ID, "kind": "agent", "canonicalIdentifier": "urn:air:example.com:agents:maintenance"})
	requireStatus(t, created, 201)
	var resource catalog.Resource
	decodeResult(t, created, &resource)
	created = requestJSON(t, s.URL, "POST", "/api/v1/resources/"+resource.ID+"/versions", "contributor", "maintenance-version", "", map[string]any{"version": "1.0.0", "representations": []map[string]any{{"role": "primary", "mediaType": adapter.MediaA2AAgentCard, "data": map[string]any{"name": "Incomplete card"}}}})
	requireStatus(t, created, 201)
	var original struct {
		Revision catalog.VersionRevision `json:"revision"`
		Evidence catalog.EvidenceSet     `json:"evidence"`
	}
	decodeResult(t, created, &original)
	if original.Evidence.State != catalog.EvidenceBlocked {
		t.Fatal("negative native-document control was not blocked", original.Evidence)
	}
	path := "/api/v1/validation-maintenance?state=blocked&horizonHours=24&limit=1"
	first := requestJSON(t, s.URL, "GET", path, "contributor", "", "", nil)
	requireStatus(t, first, 200)
	requireControlSchema(t, "ValidationMaintenancePage", first.body)
	type maintenancePage struct {
		Items      []catalog.ValidationMaintenanceItem `json:"items"`
		NextCursor string                              `json:"nextCursor"`
		ObservedAt time.Time                           `json:"observedAt"`
	}
	var page maintenancePage
	decodeResult(t, first, &page)
	if len(page.Items) != 1 || page.Items[0].RevisionID != original.Revision.ID || page.Items[0].EvidenceID != original.Evidence.ID || first.header.Get("Cache-Control") != "no-store" {
		t.Fatal(page, first.header)
	}
	for _, key := range []string{"representations", "findings", "externalRetrievals", "submittedBy", "snapshot"} {
		if strings.Contains(string(first.body), `"`+key+`"`) {
			t.Fatal("maintenance disclosed private detail", key)
		}
	}
	for _, actor := range []string{"reader", "other-admin"} {
		r := requestJSON(t, s.URL, "GET", path, actor, "", "", nil)
		requireStatus(t, r, 200)
		decodeResult(t, r, &page)
		if len(page.Items) != 0 {
			t.Fatal("maintenance permission or tenant escape", actor, page)
		}
	}
	validationPath := "/api/v1/version-revisions/" + original.Revision.ID + ":validate"
	renewed := requestJSON(t, s.URL, "POST", validationPath, "contributor", "maintenance-renew", "", map[string]any{})
	requireStatus(t, renewed, 201)
	var fresh struct {
		Revision catalog.VersionRevision `json:"revision"`
		Evidence catalog.EvidenceSet     `json:"evidence"`
	}
	decodeResult(t, renewed, &fresh)
	if fresh.Evidence.ID == original.Evidence.ID || fresh.Evidence.State != catalog.EvidenceBlocked || fresh.Revision.ContentSetDigest != original.Revision.ContentSetDigest || !reflect.DeepEqual(fresh.Revision.Representations, original.Revision.Representations) {
		t.Fatal("renewal rewrote content or failed to append evidence", fresh)
	}
	replay := requestJSON(t, s.URL, "POST", validationPath, "contributor", "maintenance-renew", "", map[string]any{})
	requireStatus(t, replay, 201)
	if replay.header.Get("Idempotency-Replayed") != "true" || string(replay.body) != string(renewed.body) {
		t.Fatal("renewal did not recover the exact command")
	}
	latest := requestJSON(t, s.URL, "GET", path, "contributor", "", "", nil)
	requireStatus(t, latest, 200)
	decodeResult(t, latest, &page)
	if len(page.Items) != 1 || page.Items[0].EvidenceID != fresh.Evidence.ID {
		t.Fatal("maintenance retained an earlier validation run", page)
	}
	for _, query := range []string{"state=unknown", "horizonHours=169", "state=blocked&state=expired", "state=blocked&host=private.example"} {
		requireControlProblem(t, requestJSON(t, s.URL, "GET", "/api/v1/validation-maintenance?"+query, "contributor", "", "", nil), 400, catalog.CodeInvalidArgument)
	}
	// Exercise actual continuation with two current subjects. Expected identities
	// come from command receipts, not another maintenance response.
	created = requestJSON(t, s.URL, "POST", "/api/v1/resources/"+resource.ID+"/versions", "contributor", "maintenance-version-two", "", map[string]any{"version": "2.0.0", "representations": []map[string]any{{"role": "primary", "mediaType": adapter.MediaA2AAgentCard, "data": map[string]any{"name": "Another incomplete card"}}}})
	requireStatus(t, created, 201)
	var second struct {
		Revision catalog.VersionRevision `json:"revision"`
		Evidence catalog.EvidenceSet     `json:"evidence"`
	}
	decodeResult(t, created, &second)
	if second.Evidence.State != catalog.EvidenceBlocked {
		t.Fatal("second current-subject control was not blocked")
	}
	want := map[string]string{original.Revision.ID: fresh.Evidence.ID, second.Revision.ID: second.Evidence.ID}
	first = requestJSON(t, s.URL, "GET", path, "contributor", "", "", nil)
	requireStatus(t, first, 200)
	requireControlSchema(t, "ValidationMaintenancePage", first.body)
	decodeResult(t, first, &page)
	if len(page.Items) != 1 || page.NextCursor == "" || page.ObservedAt.IsZero() || want[page.Items[0].RevisionID] != page.Items[0].EvidenceID {
		t.Fatal("first continuation page lost its current evidence", page)
	}
	firstID, observed, cursor := page.Items[0].RevisionID, page.ObservedAt, page.NextCursor
	nextPath := path + "&cursor=" + url.QueryEscape(cursor)
	next := requestJSON(t, s.URL, "GET", nextPath, "contributor", "", "", nil)
	requireStatus(t, next, 200)
	requireControlSchema(t, "ValidationMaintenancePage", next.body)
	page = maintenancePage{}
	decodeResult(t, next, &page)
	if len(page.Items) != 1 || page.NextCursor != "" || !page.ObservedAt.Equal(observed) || page.Items[0].RevisionID <= firstID || want[page.Items[0].RevisionID] != page.Items[0].EvidenceID {
		t.Fatal("continuation duplicated a subject, changed observation or selected old evidence", page)
	}
	for _, actor := range []string{"admin", "other-admin"} {
		requireControlProblem(t, requestJSON(t, s.URL, "GET", nextPath, actor, "", "", nil), 400, catalog.CodeInvalidArgument)
	}
	requireControlProblem(t, requestJSON(t, s.URL, "GET", "/api/v1/validation-maintenance?state=blocked&horizonHours=48&limit=1&cursor="+url.QueryEscape(cursor), "contributor", "", "", nil), 400, catalog.CodeInvalidArgument)
	// Revoke the actual retained validation grant, keeping the same token and
	// membership. A stale cursor must not turn old authority into disclosure.
	grants, err := authority.ListRoleAssignments(ownerContext, identity.RoleAssignmentListRequest{MembershipID: validator.MembershipID, Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	var grant identity.RoleAssignment
	for _, candidate := range grants {
		if candidate.Role == identity.RoleContributor {
			grant = candidate
		}
	}
	if grant.ID == "" {
		t.Fatal("successful contributor control had no validation grant")
	}
	if _, err := authority.RevokeRole(ownerContext, grant.ID, "Remove maintenance validation authority", "maintenance-http-revoke", grant.Revision); err != nil {
		t.Fatal(err)
	}
	requireControlProblem(t, requestJSON(t, s.URL, "GET", nextPath, "contributor", "", "", nil), 400, catalog.CodeInvalidArgument)
	denied := requestJSON(t, s.URL, "GET", path, "contributor", "", "", nil)
	requireStatus(t, denied, 200)
	requireControlSchema(t, "ValidationMaintenancePage", denied.body)
	page = maintenancePage{}
	decodeResult(t, denied, &page)
	if len(page.Items) != 0 || page.NextCursor != "" {
		t.Fatal("fresh read disclosed maintenance after validation grant revocation", page)
	}
}
