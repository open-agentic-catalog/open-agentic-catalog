//go:build functional

// Tests for handleSemanticReindex covering the decode-error, disabled-semantic
// error, and success paths. The json.Marshal error path is dead code because
// SemanticReindexInput contains only string fields.
package httpserver

import (
	"bytes"
	"context"
	"crypto/sha256"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/adapter"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/federation"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/jobs"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/clock"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/config"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/id"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/idempotency"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/publication"
)

// reindexSemanticStore is the minimal SemanticSearchStore needed to construct a
// *catalog.SemanticSearch so the catalog service can reach SemanticReindexTarget.
type reindexSemanticStore struct{ generation string }

func (s *reindexSemanticStore) Status(context.Context, string, string, int) (catalog.SemanticIndexStatus, error) {
	return catalog.SemanticIndexStatus{State: "ready"}, nil
}
func (s *reindexSemanticStore) Current(_ context.Context, _, _ string) (string, error) {
	return s.generation, nil
}
func (s *reindexSemanticStore) Active(context.Context, string, string, string, int) (catalog.SemanticGeneration, bool, error) {
	return catalog.SemanticGeneration{}, false, nil
}
func (s *reindexSemanticStore) Search(context.Context, catalog.SemanticSearchRequest) (catalog.SemanticSearchPage, error) {
	return catalog.SemanticSearchPage{}, nil
}
func (s *reindexSemanticStore) Explore(context.Context, catalog.SemanticSearchRequest, []catalog.FacetRequest) (catalog.ExploreResponse, error) {
	return catalog.ExploreResponse{}, nil
}
func (s *reindexSemanticStore) Similarities(context.Context, catalog.SemanticGeneration, []float32, []string) (map[string]float64, error) {
	return map[string]float64{}, nil
}

// reindexSemanticProvider is the minimal SemanticProvider needed for construction.
type reindexSemanticProvider struct{}

func (reindexSemanticProvider) Query(context.Context, string) ([]float32, error) {
	return []float32{1, 0}, nil
}
func (reindexSemanticProvider) Documents(context.Context, []string) ([][]float32, error) {
	panic("must not be called in reindex tests")
}

// newSemanticReindexServer constructs a full httptest.Server with an optional
// *catalog.SemanticSearch wired into the catalog service.
func newSemanticReindexServer(t *testing.T, semantic *catalog.SemanticSearch) (*httptest.Server, string) {
	t.Helper()
	serviceClock := clock.NewFake(time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC))
	cursorKey := sha256.Sum256([]byte("semantic-reindex-test-key"))
	tenantID := "00000000-0000-7000-8000-000000000001"

	externalFetcher := &httpCatalogExternalFetcher{now: serviceClock.Now, body: []byte(`{}`)}
	repository := catalog.NewMemoryRepository()
	catalogService, err := catalog.NewService(catalog.Options{
		Repository:      repository,
		Clock:           serviceClock,
		IDs:             id.Generator{},
		Validator:       adapter.NewRegistry(),
		ExternalFetcher: externalFetcher,
		CursorKey:       cursorKey[:],
		Policy:          "test-policy",
		SemanticSearch:  semantic,
	})
	if err != nil {
		t.Fatal(err)
	}
	federationManager, err := federation.NewManager(federation.NewMemoryStore(), unavailableFetcher{}, serviceClock, id.Generator{})
	if err != nil {
		t.Fatal(err)
	}
	publicationManager, err := publication.NewManager(publication.Options{
		Records:          catalogService,
		Store:            publication.NewMemoryStore(),
		Clock:            serviceClock,
		IDs:              id.Generator{},
		BaseURL:          "https://catalog.example.com",
		PublisherDomain:  "catalog.example.com",
		ARDProfile:       "ard-test",
		AICatalogProfile: "ai-catalog-test",
		CursorKey:        cursorKey[:],
	})
	if err != nil {
		t.Fatal(err)
	}
	jobService, err := jobs.NewService(jobs.NewMemoryStore(), serviceClock, id.Generator{})
	if err != nil {
		t.Fatal(err)
	}
	authenticator, identityService := newTestIdentity(t, serviceClock, map[string]config.DevelopmentUser{
		"admin": {Subject: "admin", TenantID: tenantID, Roles: []identity.Role{identity.RoleTenantAdmin, identity.RoleContributor}},
	})
	capState := CapabilityUnavailable
	if semantic != nil {
		capState = CapabilityEnabled
	}
	server, err := New(Dependencies{
		Catalog:          catalogService,
		Identity:         identityService,
		Federation:       federationManager,
		Jobs:             jobService,
		Authenticator:    authenticator,
		Idempotency:      idempotency.NewMemoryStore(),
		Logger:           slog.New(slog.DiscardHandler),
		DiscoveryHosts:   []DiscoveryHost{anonymousDiscoveryHost(publicationManager)},
		MaxBodyBytes:     1 << 20,
		RequestTimeout:   time.Second,
		ServiceVersion:   "test",
		Commit:           "test",
		ARDProfile:       "ard-test",
		AICatalogProfile: "ai-catalog-test",
		Capabilities: CapabilitySnapshot{
			SemanticSearch:       capState,
			CredentialResolution: CapabilityUnavailable,
			ManagedMedia:         CapabilityUnavailable,
			SoftwareAssessment:   CapabilityUnavailable,
			Webhooks:             CapabilityDisabled,
		},
		CursorKey: cursorKey[:],
		Now:       serviceClock.Now,
	})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(server.Handler())
	t.Cleanup(ts.Close)
	return ts, "admin"
}

// newReindexSemantic builds a real *catalog.SemanticSearch backed by fakes.
func newReindexSemantic(t *testing.T) *catalog.SemanticSearch {
	t.Helper()
	profile := "sha256:" + strings.Repeat("a", 64)
	rankingProfile := "sha256:" + strings.Repeat("b", 64)
	semantic, err := catalog.NewSemanticSearch(catalog.SemanticSearchOptions{
		Store:          &reindexSemanticStore{generation: "42"},
		Provider:       reindexSemanticProvider{},
		Profile:        profile,
		RankingProfile: rankingProfile,
		Dimensions:     2,
		Cutoff:         0.5,
		Timeout:        time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	return semantic
}

func TestHandleSemanticReindexRejectsMalformedBody(t *testing.T) {
	ts, token := newSemanticReindexServer(t, newReindexSemantic(t))
	req, _ := http.NewRequestWithContext(t.Context(), http.MethodPost, ts.URL+"/api/v1/catalog:reindex",
		bytes.NewReader([]byte(`{"reason": "test" trailing garbage`)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusBadRequest && resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("malformed body: got %d, want 400 or 422", resp.StatusCode)
	}
}

func TestHandleSemanticReindexFailsWhenSemanticDisabled(t *testing.T) {
	// No semantic wired → SemanticReindexTarget returns CodeDependencyUnavailable.
	ts, token := newSemanticReindexServer(t, nil)
	result := requestJSON(t, ts.URL, http.MethodPost, "/api/v1/catalog:reindex", token, "idem-disabled-01", "",
		map[string]string{"reason": "rebuild"})
	if result.status != http.StatusServiceUnavailable {
		t.Fatalf("disabled semantic: got %d, want 503; body=%s", result.status, result.body)
	}
}

func TestHandleSemanticReindexSuccessEnqueuesJobAndReturns202(t *testing.T) {
	ts, token := newSemanticReindexServer(t, newReindexSemantic(t))
	result := requestJSON(t, ts.URL, http.MethodPost, "/api/v1/catalog:reindex", token, "idem-reindex-01", "",
		map[string]string{"reason": "scheduled rebuild"})
	if result.status != http.StatusAccepted {
		t.Fatalf("success: got %d, want 202; body=%s", result.status, result.body)
	}
	if result.header.Get("Location") == "" {
		t.Fatal("success: missing Location header")
	}
	var body map[string]any
	decodeResult(t, result, &body)
	if body["kind"] != "semantic.reindex" {
		t.Fatalf("success: unexpected job kind %v", body["kind"])
	}
}

func TestHandleSemanticReindexRequiresAuthentication(t *testing.T) {
	ts, _ := newSemanticReindexServer(t, newReindexSemantic(t))
	result := requestJSON(t, ts.URL, http.MethodPost, "/api/v1/catalog:reindex", "", "", "",
		map[string]string{"reason": "unauthenticated"})
	if result.status != http.StatusUnauthorized && result.status != http.StatusForbidden {
		t.Fatalf("unauthenticated: got %d, want 401 or 403", result.status)
	}
}
