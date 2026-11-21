//go:build functional

package httpserver

import (
	"context"
	"strings"
	"testing"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
)

func TestHTTPListingMetadataOAuthCeilings(t *testing.T) {
	s := newConfiguredHTTPServer(t, func(d *Dependencies) {
		d.EnforceOAuthScopes = true
		auth := d.Authenticator
		d.Authenticator = discoveryAdmissionAuthenticator(func(ctx context.Context, token string) (identity.Actor, error) {
			name, scope, _ := strings.Cut(token, "/")
			a, err := auth.Authenticate(ctx, name)
			a.Claims = map[string]string{"scope:" + scope: "true"}
			return a, err
		})
	})
	defer s.Close()
	const path = "/api/v1/listing-metadata-gaps"
	requireControlProblem(t, requestJSON(t, s.URL, "GET", path, "", "", "", nil), 401, catalog.CodeUnauthenticated)
	requireControlProblem(t, requestJSON(t, s.URL, "GET", path, "contributor/catalog.write", "", "", nil), 403, catalog.CodeForbidden)
	read := requestJSON(t, s.URL, "GET", path, "contributor/catalog.read", "", "", nil)
	requireStatus(t, read, 200)
	requireControlSchema(t, "ListingMetadataGapPage", read.body)
	denied := requestJSON(t, s.URL, "POST", "/api/v1/catalog-listings/"+projectionItem+"/revisions", "contributor/catalog.read", "read-only-edit", `"rev-1"`, map[string]any{})
	requireControlProblem(t, denied, 403, catalog.CodeForbidden)
	if denied.header.Get("WWW-Authenticate") != `Bearer error="insufficient_scope", scope="catalog.write"` {
		t.Fatal(denied.header)
	}
}
