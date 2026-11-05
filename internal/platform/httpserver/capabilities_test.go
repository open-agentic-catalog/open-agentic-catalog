// Capability projection tests focus on disclosure and configured metadata;
// authentication exchanges and composed wiring retain their functional tests.
package httpserver

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/open-agentic-catalog/open-agentic-catalog/test/identityfixture"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
)

// TestCapabilitiesProjection checks exact administrative disclosure and avoids
// touching semantic state for readers, excluded tenants or disabled indexing.
func TestCapabilitiesProjection(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name                                   string
		wired, admin, enforce, scope, excluded bool
		profiles, webhooks, indexing           bool
		configured                             CapabilityState
		provider                               string
		failure                                error
	}{
		{name: "unavailable", configured: CapabilityUnavailable},
		{name: "disabled", configured: CapabilityDisabled, profiles: true},
		{name: "unwired enabled snapshot", configured: CapabilityEnabled},
		{name: "reader sees degradation", wired: true, configured: CapabilityEnabled, provider: "degraded", profiles: true, webhooks: true},
		{name: "admin status", wired: true, admin: true, configured: CapabilityEnabled, provider: "healthy", indexing: true},
		{name: "admin lacks OAuth scope", wired: true, admin: true, enforce: true, configured: CapabilityEnabled, provider: "healthy"},
		{name: "OAuth scope without admin", wired: true, enforce: true, scope: true, configured: CapabilityEnabled, provider: "healthy"},
		{name: "scoped admin", wired: true, admin: true, enforce: true, scope: true, configured: CapabilityEnabled, provider: "healthy", indexing: true},
		{name: "excluded admin", wired: true, admin: true, excluded: true, configured: CapabilityEnabled, provider: "healthy"},
		{name: "private provider state", wired: true, configured: CapabilityEnabled, provider: "private-fixture-endpoint"},
		{name: "status failure", wired: true, admin: true, configured: CapabilityEnabled, provider: "healthy", indexing: true, failure: errors.New("private-fixture-database")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			actor := projectionActor(identity.RoleReader)
			if tc.admin {
				actor.Roles = []identity.Role{identity.RoleTenantAdmin}
			}
			if tc.scope {
				actor.Claims = map[string]string{"scope:tenant.admin": "true"}
			}
			ctx := identityfixture.WithActor(t.Context(), actor)
			statusCalls, providerCalls := 0, 0
			status := catalog.SemanticIndexStatus{State: "building", Indexed: 7, Failed: 2, JobID: "00000000-0000-7000-8000-000000000004"}
			var semantic *catalog.SemanticSearch
			if tc.wired {
				var excluded []string
				if tc.excluded {
					excluded = []string{projectionTenant}
				}
				semantic = projectionSemantic(t, projectionSemanticStore{status: func(got context.Context, tenant, profile string, dimensions int) (catalog.SemanticIndexStatus, error) {
					statusCalls++
					if !sameProjectionContext(got, ctx) || tenant != actor.TenantID || profile != "sha256:"+strings.Repeat("a", 64) || dimensions != 2 {
						t.Fatal("status lost tenant, context or deployment binding")
					}
					return status, tc.failure
				}}, func() string { providerCalls++; return tc.provider }, excluded)
			}
			server := projectionServer(projectionCatalog(t, nil, semantic))
			server.enforceOAuthScopes = tc.enforce
			server.capabilities = CapabilitySnapshot{SemanticSearch: tc.configured, CredentialResolution: CapabilityDisabled, ManagedMedia: CapabilityUnavailable, SoftwareAssessment: CapabilityUnavailable, Webhooks: CapabilityDisabled}
			if tc.webhooks {
				server.capabilities.Webhooks = CapabilityEnabled
			}
			if tc.profiles {
				server.ardProfile, server.aiCatalogProfile = "ard-fixture", "ai-catalog-fixture"
			}
			server.profileBundleDigest, server.profileArtifactCount = "sha256:"+strings.Repeat("c", 64), 17
			response := httptest.NewRecorder()
			server.handleCapabilities(response, httptest.NewRequestWithContext(ctx, http.MethodGet, "/api/v1/capabilities", nil))
			wantStatusCalls, wantProviderCalls := 0, 0
			if tc.indexing {
				wantStatusCalls = 1
			}
			if tc.wired && !tc.excluded {
				wantProviderCalls = 1
			}
			if statusCalls != wantStatusCalls || providerCalls != wantProviderCalls {
				t.Fatalf("status calls=%d provider state calls=%d", statusCalls, providerCalls)
			}
			if tc.failure != nil {
				requireProjectionProblem(t, response, http.StatusInternalServerError, catalog.CodeInternal)
				return
			}
			var got capabilitiesResponse
			if err := json.Unmarshal(response.Body.Bytes(), &got); err != nil {
				t.Fatal(err)
			}
			wantSearch := searchCapabilities{Tier: searchTierCore, Profile: catalog.CoreSearchProfile, Semantic: tc.configured}
			if tc.wired && !tc.excluded {
				state := tc.provider
				if state != "healthy" && state != "degraded" {
					state = "unknown"
				}
				wantSearch = searchCapabilities{Tier: "hybrid", Profile: "sha256:" + strings.Repeat("b", 64), Semantic: CapabilityEnabled, ProviderState: state, Degraded: state == "degraded"}
			} else if wantSearch.Semantic == CapabilityEnabled {
				wantSearch.Semantic = CapabilityDisabled
			}
			if tc.indexing {
				wantSearch.Indexing = &status
			}
			if response.Code != http.StatusOK || response.Header().Get("Content-Type") != "application/json; charset=utf-8" || !reflect.DeepEqual(got.Search, wantSearch) || strings.Contains(response.Body.String(), "private-fixture") {
				t.Fatalf("capabilities status=%d search=%+v body=%s", response.Code, got.Search, response.Body.String())
			}
			if strings.Contains(response.Body.String(), `"indexing"`) != tc.indexing || slices.Contains(got.Operations, "webhooks.signed") != tc.webhooks || got.OptionalCapabilities != (optionalCapabilities{CredentialResolution: CapabilityDisabled, ManagedMedia: CapabilityUnavailable, SoftwareAssessment: CapabilityUnavailable, Webhooks: server.capabilities.Webhooks}) {
				t.Fatal("capability availability or administrator-only field changed")
			}
			if got.Profiles.ARD == nil || got.Profiles.AICatalog == nil || (tc.profiles && (!slices.Equal(got.Profiles.ARD, []string{server.ardProfile}) || !slices.Equal(got.Profiles.AICatalog, []string{server.aiCatalogProfile}))) || (!tc.profiles && (len(got.Profiles.ARD) != 0 || len(got.Profiles.AICatalog) != 0)) {
				t.Fatalf("configured profiles=%+v", got.Profiles)
			}
			if got.Limits.ValidationMaxFindings != 128 || got.Limits.ValidationMaxDiagnosticBytes != 65536 || got.Limits.RequestBodyBytes != server.maxBodyBytes || got.ProfileBundle.Digest != server.profileBundleDigest || got.ProfileBundle.Artifacts != 17 || got.Degradations == nil || got.Governance.PublisherAuthority != catalog.TrustNotAssessed || got.Governance.LegalRights != catalog.LegalRightsNotAssessed {
				t.Fatal("capability metadata lost configured limits, provenance or assessment boundaries")
			}
		})
	}
}

// TestCapabilitiesMissingActorStopsBeforeDependencies checks the direct handler
// guard; the outer authentication middleware's 401 remains a separate contract.
func TestCapabilitiesMissingActorStopsBeforeDependencies(t *testing.T) {
	response := httptest.NewRecorder()
	(&Server{}).handleCapabilities(response, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/capabilities", nil))
	requireProjectionProblem(t, response, http.StatusForbidden, catalog.CodeForbidden)
}
