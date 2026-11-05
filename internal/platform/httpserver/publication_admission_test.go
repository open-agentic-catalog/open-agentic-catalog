// Publication admission tests fail before any storage call. JSON decoding and
// generic cursor signatures already have their own contract matrices.
package httpserver

import (
	"github.com/open-agentic-catalog/open-agentic-catalog/test/identityfixture"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
)

func TestPublicationHandlerAdmission(t *testing.T) {
	t.Parallel()
	for _, action := range []string{"list", "get", "build", "activate", "rollback", "unpublish"} {
		for _, admission := range []string{"missing-actor", "reader", "unknown-host"} {
			t.Run(action+"/"+admission, func(t *testing.T) {
				s := publicationControlServer(t, publicationProjectionStore{})
				actor := projectionActor(identity.RolePublisher)
				if admission == "reader" {
					actor = projectionActor(identity.RoleReader)
				}
				ctx := t.Context()
				if admission != "missing-actor" {
					ctx = identityfixture.WithActor(ctx, actor)
				}
				host := publicFixtureHost
				if admission == "unknown-host" {
					host = "unknown.example.com"
				}
				method := http.MethodPost
				handler := s.handlePublicationBuild
				switch action {
				case "list":
					method, handler = http.MethodGet, s.handlePublicationList
				case "get":
					method, handler = http.MethodGet, s.handlePublicationGet
				case "activate":
					handler = s.handlePublicationActivate
				case "rollback":
					handler = s.handlePublicationRollback
				case "unpublish":
					handler = s.handlePublicationUnpublish
				}
				r := httptest.NewRequestWithContext(ctx, method, "/publication?host="+host, strings.NewReader(`{"host":"`+host+`","reason":"Operator review"}`))
				// Build accepts only host; leave the transition reason to transition routes.
				if action == "build" {
					r = httptest.NewRequestWithContext(ctx, method, "/publication", strings.NewReader(`{"host":"`+host+`"}`))
				}
				r.Header.Set("Content-Type", "application/json")
				r.SetPathValue("snapshotId", projectionItem)
				w := httptest.NewRecorder()
				handler(w, r)
				status, code := 403, catalog.CodeForbidden
				if admission == "unknown-host" {
					status, code = 404, catalog.CodeNotFound
				}
				requireProjectionProblem(t, w, status, code)
			})
		}
	}
}

func TestPublicationPageRejectsInvalidPosition(t *testing.T) {
	t.Parallel()
	actor := projectionActor(identity.RolePublisher)
	s := publicationControlServer(t, publicationProjectionStore{})
	binding := adminCursorBinding{Purpose: "publication-snapshots", Parent: publicFixtureHost, AuthorizationDigest: fixtureDecisionDigest(t, t.Context(), actor), Order: "generation desc,id desc", Limit: 1, Filters: map[string]string{"host": publicFixtureHost}}
	for _, position := range [][]string{{"7"}, {"7", projectionItem, "extra"}, {"zero", projectionItem}, {"0", projectionItem}, {"-1", projectionItem}, {"9223372036854775808", projectionItem}, {"7", ""}} {
		t.Run(strings.Join(position, "/"), func(t *testing.T) {
			token, err := s.encodeAdminCursor(adminPageRequest{Limit: 1, binding: binding}, position)
			if err != nil {
				t.Fatal(err)
			}
			query := url.Values{"host": {publicFixtureHost}, "limit": {"1"}, "cursor": {token}}
			r := httptest.NewRequestWithContext(identityfixture.WithActor(t.Context(), actor), http.MethodGet, "/publication?"+query.Encode(), nil)
			// Ensure this case reaches position validation, not generic signature checks.
			page, err := s.parseAdminPage(r, binding.Purpose, binding.Parent, binding.Order, binding.Filters)
			if err != nil || !reflect.DeepEqual(page.Position, position) {
				t.Fatalf("signed position=%+v err=%v", page, err)
			}
			w := httptest.NewRecorder()
			s.handlePublicationList(w, r)
			requireProjectionProblem(t, w, 400, catalog.CodeInvalidArgument)
		})
	}
	r := httptest.NewRequestWithContext(identityfixture.WithActor(t.Context(), actor), http.MethodGet, "/publication?host="+publicFixtureHost+"&limit=0", nil)
	w := httptest.NewRecorder()
	s.handlePublicationList(w, r)
	requireProjectionProblem(t, w, 400, catalog.CodeInvalidArgument)
}
