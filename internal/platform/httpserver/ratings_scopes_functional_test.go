//go:build functional

package httpserver

import (
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/ratings"
)

// Reuse a genuinely reviewed release and its current Catalog membership. Only
// the authenticated token's OAuth ceiling changes between the two controls.
func qualifyHTTPRatingScopes(t *testing.T, dependencies Dependencies, releaseID string) {
	t.Helper()
	authenticator := dependencies.Authenticator
	dependencies.EnforceOAuthScopes = true
	dependencies.Authenticator = discoveryAdmissionAuthenticator(func(ctx context.Context, token string) (identity.Actor, error) {
		missing := token == "contributor-read-scope-only" // #nosec G101 -- synthetic owned fixture credential.
		if missing {
			token = "contributor"
		}
		actor, err := authenticator.Authenticate(ctx, token)
		actor.Claims = map[string]string{"scope:catalog.read": "true"}
		if !missing {
			actor.Claims["scope:ratings.write"] = "true"
		}
		return actor, err
	})
	server, err := New(dependencies)
	if err != nil {
		t.Fatal(err)
	}
	owned := httptest.NewServer(server.Handler())
	defer owned.Close()
	for _, token := range []string{"contributor", "contributor-read-scope-only"} {
		self := requestJSON(t, owned.URL, http.MethodGet, "/api/v1/me", token, "", "", nil)
		requireStatus(t, self, http.StatusOK)
		requireControlSchema(t, "CurrentAccess", self.body)
		var access selfResponse
		decodeResult(t, self, &access)
		if !access.ScopeEnforcement || !slices.Contains(access.Permissions, "ratings.own") || slices.Contains(access.Scopes, "ratings.write") != (token == "contributor") {
			t.Fatal("current grants and OAuth scope hints disagree with the token")
		}
	}
	read := requestJSON(t, owned.URL, http.MethodGet, "/api/v1/catalog/resources/"+releaseID, "contributor", "", "", nil)
	requireStatus(t, read, http.StatusOK)
	var record catalog.CatalogRecord
	decodeResult(t, read, &record)
	target := ratings.Admission{Target: ratings.Target{Origin: "local", ResourceID: record.Resource.ID, VersionRevisionID: record.VersionRevision.ID, Identifier: record.Resource.CanonicalIdentifier, Kind: record.Resource.Kind, VersionPresent: true, Version: record.Version.Version}, ReleaseID: releaseID}
	input := map[string]any{"target": target.Target, "releaseId": releaseID, "score": 4}
	missing := requestJSON(t, owned.URL, http.MethodPost, "/api/v1/ratings", "contributor-read-scope-only", "scope-rating-submit", "", input)
	requireControlProblem(t, missing, http.StatusForbidden, catalog.CodeForbidden)
	if missing.header.Get("WWW-Authenticate") != `Bearer error="insufficient_scope", scope="ratings.write"` || !strings.Contains(string(missing.body), "OAuth scope ratings.write is required") {
		t.Fatal("missing rating scope was not rejected by OAuth admission")
	}
	// The refusal must not consume the command key or create an opinion.
	created := requestJSON(t, owned.URL, http.MethodPost, "/api/v1/ratings", "contributor", "scope-rating-submit", "", input)
	requireStatus(t, created, http.StatusCreated)
	var receipt feedbackReceipt
	decodeResult(t, created, &receipt)
	path := "/api/v1/ratings/" + receipt.ID
	active := requestJSON(t, owned.URL, http.MethodGet, path, "contributor", "", "", nil)
	requireStatus(t, active, http.StatusOK)
	var vote ratings.Record
	decodeResult(t, active, &vote)
	if vote.State != "active" || vote.Score != 4 || vote.Target != target.Target {
		t.Fatal("rating submission did not retain the exact opinion")
	}
	missing = requestJSON(t, owned.URL, http.MethodPost, path+":withdraw", "contributor-read-scope-only", "scope-rating-withdraw", active.header.Get("ETag"), map[string]any{})
	requireControlProblem(t, missing, http.StatusForbidden, catalog.CodeForbidden)
	unchanged := requestJSON(t, owned.URL, http.MethodGet, path, "contributor", "", "", nil)
	requireStatus(t, unchanged, http.StatusOK)
	if string(unchanged.body) != string(active.body) {
		t.Fatal("missing write scope changed the retained rating")
	}
	withdrawn := requestJSON(t, owned.URL, http.MethodPost, path+":withdraw", "contributor", "scope-rating-withdraw", active.header.Get("ETag"), map[string]any{})
	requireStatus(t, withdrawn, http.StatusOK)
	final := requestJSON(t, owned.URL, http.MethodGet, path, "contributor", "", "", nil)
	requireStatus(t, final, http.StatusOK)
	decodeResult(t, final, &vote)
	if vote.State != "withdrawn" || vote.Score != 0 || vote.Consent || vote.Target != target.Target || vote.Revision != receipt.Revision+1 {
		t.Fatal("withdrawal did not clear the exact owned rating")
	}
}
