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

func TestFederationHandlersRequireAdministrator(t *testing.T) {
	t.Parallel()
	s := federationProjectionServer(t, federationProjectionStore{}, nil)
	for _, tc := range []struct {
		name    string
		handler http.HandlerFunc
		body    string
	}{
		{"inspect", s.handleSourceInspect, `{}`}, {"connect", s.handleSourceConnect, `{}`}, {"get", s.handleSourceGet, `{}`}, {"inspection", s.handleSourceInspectionGet, `{}`}, {"snapshot", s.handleSourceSnapshotGet, `{}`},
		{"preview", s.handleSourceChangePreview, `{"intent":"update"}`}, {"update", s.handleSourcePatch, `{"displayName":"Renamed"}`}, {"test", s.handleSourceTest, `{}`}, {"sync", s.handleSourceSync, `{}`}, {"pause", s.handleSourcePause, `{"reason":"Operator review"}`}, {"resume", s.handleSourceResume, `{"reason":"Operator review"}`}, {"remove", s.handleSourceRemove, `{"reason":"Operator review"}`},
		{"raw", s.handleObservationRaw, `{}`}, {"live", s.handleLiveFederationSearch, `{"text":"fixture","reason":"Operator review"}`}, {"adopt", s.dispatchObservationCommand, `{}`},
		{"trust assessment", s.handleTrustRequest, `{"sourceRevision":1,"identifier":"urn:air:peer.example:agents:one","versionPresent":false,"mediaType":"application/a2a-agent-card+json","delivery":"url","exactEntryDigest":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","text":"one","reason":"Assess selected registration"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequestWithContext(identityfixture.WithActor(t.Context(), projectionActor(identity.RoleReader)), http.MethodPost, "/federation", strings.NewReader(tc.body))
			r.Header.Set("Content-Type", "application/json")
			r.Header.Set("If-Match", `"rev-3"`)
			r.SetPathValue("observationCommand", projectionItem+":adopt")
			w := httptest.NewRecorder()
			tc.handler(w, r)
			requireProjectionProblem(t, w, 403, catalog.CodeForbidden)
		})
	}
}

func TestTrustRequestHandlerRejectsInvalidEnvelopeBeforeSelection(t *testing.T) {
	t.Parallel()
	// No manager or Job service is installed: malformed input must stop before
	// either boundary. Successful selection and enqueue have functional tests.
	s := projectionServer(nil)
	for _, body := range []string{`{}`, `{"sourceRevision":null}`, `{"unknown":true}`, `[]`, `{"versionPresent":false,"version":""}`} {
		t.Run(body, func(t *testing.T) {
			r := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/trust-assessments", strings.NewReader(body))
			r.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			s.handleTrustRequest(w, r)
			requireProjectionProblem(t, w, http.StatusBadRequest, catalog.CodeInvalidArgument)
		})
	}
}

func TestFederationCommandsRequireRevision(t *testing.T) {
	t.Parallel()
	s := projectionServer(nil)
	for _, handler := range []http.HandlerFunc{s.handleSourcePatch, s.handleSourceTest, s.handleSourceSync, s.handleSourcePause, s.handleSourceResume, s.handleSourceRemove} {
		r := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/source", strings.NewReader(`{}`))
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		handler(w, r)
		requireProjectionProblem(t, w, 412, catalog.CodePreconditionFailed)
	}
}

func TestFederationPagePositionAdmission(t *testing.T) {
	t.Parallel()
	s := projectionServer(nil)
	actor := projectionActor(identity.RoleSourceAdmin)
	for _, tc := range []struct {
		purpose, parent, order, pathKey string
		filters                         map[string]string
		positions                       [][]string
		handler                         http.HandlerFunc
	}{
		{"federation-sources", "", "displayName,id", "", map[string]string{}, [][]string{{"name"}}, s.handleSourcesList},
		{"federation-source-snapshots", projectionResource, "startedAt desc,id desc", "sourceId", map[string]string{}, [][]string{{"time"}, {"invalid-time", projectionItem}}, s.handleSourceSnapshots},
		{"observations", "", "identifier,versionPresent desc,version,id", "", map[string]string{"sourceId": "", "freshness": "current"}, [][]string{{"key"}, {"identifier", "invalid-bool", "1", projectionItem}}, s.handleObservations},
		{"observation-occurrences", projectionItem, "observedAt desc,snapshotId desc,retrievalDigest,entryLocation", "observationId", map[string]string{}, [][]string{{"time"}, {"invalid-time", projectionItem, "sha256:abc", "/entries/0"}}, s.handleObservationOccurrences},
	} {
		t.Run(tc.purpose, func(t *testing.T) {
			ctx := identityfixture.WithActor(t.Context(), actor)
			r := httptest.NewRequestWithContext(ctx, http.MethodGet, "/page?limit=0", nil)
			r.SetPathValue(tc.pathKey, tc.parent)
			w := httptest.NewRecorder()
			tc.handler(w, r)
			requireProjectionProblem(t, w, 400, catalog.CodeInvalidArgument)
			binding := adminCursorBinding{Purpose: tc.purpose, Parent: tc.parent, Order: tc.order, AuthorizationDigest: fixtureDecisionDigest(t, t.Context(), actor), Limit: 1, Filters: tc.filters}
			for _, pos := range tc.positions {
				token, err := s.encodeAdminCursor(adminPageRequest{Limit: 1, binding: binding}, pos)
				if err != nil {
					t.Fatal(err)
				}
				r = httptest.NewRequestWithContext(ctx, http.MethodGet, "/page?limit=1&cursor="+url.QueryEscape(token), nil)
				r.SetPathValue(tc.pathKey, tc.parent)
				p, err := s.parseAdminPage(r, tc.purpose, tc.parent, tc.order, tc.filters)
				if err != nil || !reflect.DeepEqual(p.Position, pos) {
					t.Fatalf("position not reached: %+v %v", p, err)
				}
				w = httptest.NewRecorder()
				tc.handler(w, r)
				requireProjectionProblem(t, w, 400, catalog.CodeInvalidArgument)
			}
		})
	}
	for _, query := range []string{"sourceId=", "freshness=", "freshness=invalid"} {
		r := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/observations?"+query, nil)
		w := httptest.NewRecorder()
		s.handleObservations(w, r)
		requireProjectionProblem(t, w, 400, catalog.CodeInvalidArgument)
	}
}

func TestFederationGroupAndAdoptionAdmission(t *testing.T) {
	t.Parallel()
	s := federationProjectionServer(t, federationProjectionStore{}, nil)
	ctx := identityfixture.WithActor(t.Context(), projectionActor(identity.RoleSourceAdmin))
	for _, query := range []string{"identifier=one&identifier=two", "identifier=one", "identifier=one&versionPresent=1", "identifier=invalid&versionPresent=false"} {
		r := httptest.NewRequestWithContext(ctx, http.MethodGet, "/group?"+query, nil)
		w := httptest.NewRecorder()
		s.handleObservationGroup(w, r)
		requireProjectionProblem(t, w, 400, catalog.CodeInvalidArgument)
	}
	for _, command := range []string{"missing-action", ":adopt", projectionItem + ":delete", "invalid:adopt", projectionItem + ":adopt"} {
		body := `{}`
		if command == projectionItem+":adopt" {
			body = `{"unknown":true}`
		}
		r := httptest.NewRequestWithContext(ctx, http.MethodPost, "/observation", strings.NewReader(body))
		r.SetPathValue("observationCommand", command)
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		s.dispatchObservationCommand(w, r)
		status, code := 404, catalog.CodeNotFound
		if command == "invalid:adopt" || command == projectionItem+":adopt" {
			status, code = 400, catalog.CodeInvalidArgument
		}
		requireProjectionProblem(t, w, status, code)
	}
}
