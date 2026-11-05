// Public admission retains ARD error shapes before accessing snapshot state.
package httpserver

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/publication"
)

func TestPublicHandlersRejectUnboundHosts(t *testing.T) {
	t.Parallel()
	s := projectionServer(nil)
	for _, tc := range []struct {
		name    string
		handler http.HandlerFunc
	}{
		{"manifest", s.handleARDManifest}, {"AI Catalog", s.handleAICatalog}, {"search", s.handleARDSearch}, {"explore", s.handleARDExplore}, {"list", s.handleARDList}, {"artifact", s.handlePublicArtifact},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil)
			r.Host = "unknown.example"
			w := httptest.NewRecorder()
			tc.handler(w, r)
			requireARDProjectionProblem(t, w, 404, catalog.CodeNotFound)
		})
	}
}
func TestPublicQueryAdmission(t *testing.T) {
	t.Parallel()
	s := publicProjectionServer(t, DiscoveryAnonymous, nil)
	for _, tc := range []struct {
		name, path, body string
		handler          http.HandlerFunc
	}{
		{"search malformed", "/ard/v1/search", "{", s.handleARDSearch},
		{"search closed fields", "/ard/v1/search", `{"query":{"text":"fixture"},"sourceIds":["unapproved"]}`, s.handleARDSearch},
		{"search invalid mode", "/ard/v1/search", `{"query":{"text":"fixture"},"federation":"unknown"}`, s.handleARDSearch},
		{"search missing query", "/ard/v1/search", `{}`, s.handleARDSearch},
		{"explore malformed", "/ard/v1/explore", "{", s.handleARDExplore},
		{"explore closed fields", "/ard/v1/explore", `{"resultType":{"facets":[{"field":"tags"}]},"sourceIds":["unapproved"]}`, s.handleARDExplore},
		{"explore missing facets", "/ard/v1/explore", `{}`, s.handleARDExplore},
		{"list zero page size", "/ard/v1/agents?pageSize=0", "", s.handleARDList},
		{"list excessive page size", "/ard/v1/agents?pageSize=101", "", s.handleARDList},
		{"list malformed page size", "/ard/v1/agents?pageSize=bad", "", s.handleARDList},
		{"list empty page size", "/ard/v1/agents?pageSize=", "", s.handleARDList},
	} {
		t.Run(tc.name, func(t *testing.T) {
			method := http.MethodPost
			if tc.body == "" {
				method = http.MethodGet
			}
			r := httptest.NewRequestWithContext(t.Context(), method, tc.path, strings.NewReader(tc.body))
			r.Host = publicFixtureHost
			r.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			tc.handler(w, r)
			requireARDProjectionProblem(t, w, 400, catalog.CodeInvalidArgument)
		})
	}
}
func TestPublicQuerySnapshotFailures(t *testing.T) {
	t.Parallel()
	for _, operation := range []string{"search", "explore", "list"} {
		t.Run(operation, func(t *testing.T) {
			calls := 0
			s := publicProjectionServer(t, DiscoveryAnonymous, func(ctx context.Context, tenant string) (publication.Snapshot, error) {
				calls++
				if ctx != t.Context() || tenant != projectionTenant {
					t.Fatal("query failure binding")
				}
				return publication.Snapshot{}, catalog.E(catalog.CodeDependencyUnavailable, "private-fixture-snapshot")
			})
			body := `{"query":{"text":"fixture"},"federation":"none"}`
			if operation == "explore" {
				body = `{"resultType":{"facets":[{"field":"tags"}]}}`
			}
			method, path := http.MethodPost, "/ard/v1/"+operation
			if operation == "list" {
				method = http.MethodGet
				path = "/ard/v1/agents"
				body = ""
			}
			r := httptest.NewRequestWithContext(t.Context(), method, path, strings.NewReader(body))
			r.Host = publicFixtureHost
			r.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			switch operation {
			case "search":
				s.handleARDSearch(w, r)
			case "explore":
				s.handleARDExplore(w, r)
			case "list":
				s.handleARDList(w, r)
			}
			requireARDProjectionProblem(t, w, 503, catalog.CodeDependencyUnavailable)
			if calls != 1 {
				t.Fatalf("failed reads=%d", calls)
			}
		})
	}
}
