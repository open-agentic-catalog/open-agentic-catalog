// Indexed handler tests preserve URL identity and response/error projection
// through bounded local ports; they never inspect or synchronize a source.
package httpserver

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/open-agentic-catalog/open-agentic-catalog/test/identityfixture"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/federation"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
)

type indexedProjectionRepository struct {
	catalog.Repository
	published func(context.Context, string, string, string) (bool, error)
}

func (r indexedProjectionRepository) HasPublishedIdentity(ctx context.Context, tenant, identifier, version string) (bool, error) {
	return r.published(ctx, tenant, identifier, version)
}

type indexedProjectionSearch func(context.Context, catalog.FederatedCatalogSearchRequest) (catalog.FederatedCatalogSearchPage, error)

func (f indexedProjectionSearch) SearchFederatedCatalog(ctx context.Context, request catalog.FederatedCatalogSearchRequest) (catalog.FederatedCatalogSearchPage, error) {
	return f(ctx, request)
}

// TestIndexedHandlerRejectsInvalidVersionPresence checks the HTTP-only presence
// rule before any service access, including explicitly empty forbidden versions.
func TestIndexedHandlerRejectsInvalidVersionPresence(t *testing.T) {
	t.Parallel()
	for _, query := range []string{"", "versionPresent=", "versionPresent=TRUE", "versionPresent=1", "versionPresent=false&version=", "versionPresent=false&version=1"} {
		t.Run(query, func(t *testing.T) {
			response := httptest.NewRecorder()
			projectionServer(nil).handleIndexedCatalogRecord(response, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/catalog/indexed-record?"+query, nil))
			requireProjectionProblem(t, response, http.StatusBadRequest, catalog.CodeInvalidArgument)
		})
	}
}

// TestIndexedHandlerIdentityAndResults verifies exact query binding and public
// projection, retaining the distinct absent and present-empty bookmark forms.
func TestIndexedHandlerIdentityAndResults(t *testing.T) {
	t.Parallel()
	const identifier = "urn:air:remote.example:default:agent"
	for _, tc := range []struct {
		name, query, version string
		present, missing     bool
		failure              error
		status               int
		code                 catalog.ErrorCode
	}{
		{name: "absent", query: "versionPresent=false", status: http.StatusOK},
		{name: "present empty", query: "versionPresent=true&version=", present: true, status: http.StatusOK},
		{name: "present implicit empty", query: "versionPresent=true", present: true, status: http.StatusOK},
		{name: "encoded version", query: "versionPresent=true&version=1%2Bbuild", present: true, version: "1+build", status: http.StatusOK},
		{name: "withdrawn", query: "versionPresent=false", missing: true, status: http.StatusNotFound, code: catalog.CodeNotFound},
		{name: "dependency failure", query: "versionPresent=false", failure: catalog.Wrap(catalog.CodeDependencyUnavailable, "index unavailable", errors.New("private-fixture-storage")), status: http.StatusServiceUnavailable, code: catalog.CodeDependencyUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := identityfixture.WithActor(t.Context(), projectionActor(identity.RoleReader))
			selected := catalog.IndexedIdentity{Identifier: identifier, VersionPresent: tc.present, Version: tc.version}
			groupID, err := federation.ObservationGroupID(identifier, tc.version, tc.present)
			if err != nil {
				t.Fatal(err)
			}
			item := catalog.CatalogSearchResult{ID: catalog.IndexedSearchResultID(identifier, tc.version, tc.present), Type: catalog.SearchResultIndexed, Identifier: identifier, Version: tc.version, VersionPresent: tc.present, Kind: catalog.KindAgent, DisplayName: "Indexed agent", Tags: []string{"fixture"}, Indexed: &catalog.IndexedSearchEvidence{ObservationGroupID: groupID, NormalizedEntryDigest: "sha256:" + strings.Repeat("a", 64), SourceIDs: []string{"00000000-0000-7000-8000-000000000005"}, SourceCount: 1}}
			publishedCalls, searchCalls := 0, 0
			repo := indexedProjectionRepository{published: func(got context.Context, tenant, air, version string) (bool, error) {
				publishedCalls++
				if !sameProjectionContext(got, ctx) || tenant != projectionTenant || air != identifier || version != tc.version {
					t.Fatal("local precedence query lost the selected identity or tenant")
				}
				return false, nil
			}}
			search, err := catalog.NewSearchService(projectionCatalog(t, repo, nil), indexedProjectionSearch(func(got context.Context, request catalog.FederatedCatalogSearchRequest) (catalog.FederatedCatalogSearchPage, error) {
				searchCalls++
				want := catalog.FederatedCatalogSearchRequest{Query: catalog.SearchQuery{Origins: []catalog.SearchResultType{catalog.SearchResultIndexed}}, Limit: 1, ExactIdentity: &selected}
				if !sameProjectionContext(got, ctx) || !reflect.DeepEqual(request, want) {
					t.Fatalf("indexed handoff=%+v", request)
				}
				if tc.missing || tc.failure != nil {
					return catalog.FederatedCatalogSearchPage{}, tc.failure
				}
				return catalog.FederatedCatalogSearchPage{Indexed: []catalog.CatalogSearchResult{item}}, nil
			}))
			if err != nil {
				t.Fatal(err)
			}
			server := projectionServer(nil)
			server.catalogSearch = search
			response := httptest.NewRecorder()
			server.handleIndexedCatalogRecord(response, httptest.NewRequestWithContext(ctx, http.MethodGet, "/api/v1/catalog/indexed-record?identifier="+url.QueryEscape(identifier)+"&"+tc.query, nil))
			wantPublishedCalls := 0
			if searchCalls != 1 || publishedCalls != wantPublishedCalls {
				t.Fatalf("search calls=%d local checks=%d", searchCalls, publishedCalls)
			}
			if tc.status != http.StatusOK {
				requireProjectionProblem(t, response, tc.status, tc.code)
				return
			}
			var got catalog.CatalogSearchResult
			if err := json.Unmarshal(response.Body.Bytes(), &got); err != nil {
				t.Fatal(err)
			}
			if response.Code != http.StatusOK || response.Header().Get("Content-Type") != "application/json; charset=utf-8" || !reflect.DeepEqual(got, item) {
				t.Fatalf("indexed response=%d %s", response.Code, response.Body.String())
			}
		})
	}
}
