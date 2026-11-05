package httpserver

import (
	"context"
	"github.com/open-agentic-catalog/open-agentic-catalog/test/identityfixture"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/federation"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/observability"
)

type federationAdmissionTopologyStore struct {
	federation.Store
	selectSources func(context.Context, string, string, int) ([]federation.Source, bool, error)
}

func (s federationAdmissionTopologyStore) ListCatalogLiveSources(c context.Context, tenant, policy string, limit int) ([]federation.Source, bool, error) {
	return s.selectSources(c, tenant, policy, limit)
}

// Empty topology and stale policy evidence produce responses without contacting peers.
// Outbound live federation and its audit evidence remain functional workflows.
func TestFederationLiveAdmissionProjection(t *testing.T) {
	t.Parallel()
	for _, partial := range []bool{false, true} {
		t.Run(map[bool]string{false: "empty", true: "stale-policy"}[partial], func(t *testing.T) {
			actor := projectionActor(identity.RoleSourceAdmin)
			operation := &requestOperationObservation{}
			ctx := context.WithValue(identityfixture.WithActor(t.Context(), actor), requestOperationKey{}, operation)
			reads := 0
			s := federationProjectionServer(t, federationAdmissionTopologyStore{selectSources: func(c context.Context, tenant, policy string, limit int) ([]federation.Source, bool, error) {
				reads++
				got, ok := identity.FromContext(c)
				if !ok || got.Subject != actor.Subject || tenant != actor.TenantID || policy != (publicPolicyFetcher{}).PolicyDigest() || limit < 1 || limit > federation.MaxDirectLiveSources {
					t.Fatal("administrative topology selection")
				}
				if partial {
					source := federationFixtureSource()
					source.Type = federation.SourceRegistry
					source.Endpoint = "https://peer.example/ard/v1/"
					source.RegistrySearchURL = source.Endpoint + "search"
					source.RetrievalMode = federation.RetrievalLive
					source.Live = federation.LivePolicy{Enabled: true, QueryPrivacyAcknowledged: true, FederationMode: "none"}
					source.DiscoveryAssignments = []federation.DiscoveryAssignment{{Host: publicFixtureHost, Audience: federation.AudienceAuthenticated}}
					source.PolicyDigest = "sha256:" + strings.Repeat("b", 64)
					return []federation.Source{source}, false, nil
				}
				return []federation.Source{}, false, nil
			}}, nil)
			r := httptest.NewRequestWithContext(ctx, http.MethodPost, "/live-search", strings.NewReader(`{"text":"fixture","reason":"Operator diagnosis"}`))
			r.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			s.handleLiveFederationSearch(w, r)
			want := federation.LiveSearchResponse{Results: []federation.LiveResult{}, Partial: partial}
			outcome := observability.OutcomeSucceeded
			if partial {
				want.Diagnostics = []federation.LiveDiagnostic{{SourceID: projectionResource, Status: "policy_stale", Message: "Source policy evidence is not current."}}
				outcome = observability.OutcomePartial
			}
			requireFederationJSON(t, w, 200, want)
			if reads != 1 || operation.operation != observability.OperationLiveSearch || operation.outcome != outcome {
				t.Fatalf("reads=%d operation=%+v", reads, operation)
			}
		})
	}
}
