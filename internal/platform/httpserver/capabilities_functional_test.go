//go:build functional

// Tests for handleCapabilities covering authentication, search tier projection,
// semantic-enabled and semantic-disabled branches, admin indexing status, webhook
// operation inclusion, and the SemanticStatus store-error path.
package httpserver

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
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
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/webhook"
)

// capabilitiesServerOptions controls how the test server for capabilities tests is built.
type capabilitiesServerOptions struct {
	semantic         *catalog.SemanticSearch
	capState         CapabilitySnapshot
	webhookService   *webhook.Service
	snapshotSemantic CapabilityState // overrides capState.SemanticSearch when set
}

// newCapabilitiesServer builds a test server with options.  tenantID is the
// fixed tenant used across all capability tests.
func newCapabilitiesServer(t *testing.T, opts capabilitiesServerOptions) (*httptest.Server, map[string]string) {
	t.Helper()
	serviceClock := clock.NewFake(time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC))
	cursorKey := sha256.Sum256([]byte("capabilities-test-key"))
	tenantID := "00000000-0000-7000-8000-000000000001"

	externalFetcher := &httpCatalogExternalFetcher{now: serviceClock.Now, body: []byte(`{}`)}
	catalogService, err := catalog.NewService(catalog.Options{
		Repository:      catalog.NewMemoryRepository(),
		Clock:           serviceClock,
		IDs:             id.Generator{},
		Validator:       adapter.NewRegistry(),
		ExternalFetcher: externalFetcher,
		CursorKey:       cursorKey[:],
		Policy:          "test-policy",
		SemanticSearch:  opts.semantic,
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
		"admin":       {Subject: "admin", TenantID: tenantID, Roles: []identity.Role{identity.RoleTenantAdmin, identity.RoleContributor}},
		"contributor": {Subject: "contributor", TenantID: tenantID, Roles: []identity.Role{identity.RoleContributor}},
	})

	capSnap := opts.capState
	if opts.snapshotSemantic != "" {
		capSnap.SemanticSearch = opts.snapshotSemantic
	}
	if capSnap.SemanticSearch == "" {
		if opts.semantic != nil {
			capSnap.SemanticSearch = CapabilityEnabled
		} else {
			capSnap.SemanticSearch = CapabilityUnavailable
		}
	}
	// Webhooks: must agree with whether a webhook service is provided.
	if opts.webhookService != nil {
		capSnap.Webhooks = CapabilityEnabled
	} else if capSnap.Webhooks == "" {
		capSnap.Webhooks = CapabilityDisabled
	}

	server, err := New(Dependencies{
		Catalog:          catalogService,
		Identity:         identityService,
		Federation:       federationManager,
		Jobs:             jobService,
		Webhooks:         opts.webhookService,
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
		Capabilities:     capSnap,
		CursorKey:        cursorKey[:],
		Now:              serviceClock.Now,
	})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(server.Handler())
	t.Cleanup(ts.Close)
	tokens := map[string]string{"admin": "admin", "contributor": "contributor"}
	return ts, tokens
}

// capabilitiesSemanticWithStatus builds a *catalog.SemanticSearch backed by a
// store whose Status method returns the given error.
func capabilitiesSemanticWithStatus(t *testing.T, statusErr error) *catalog.SemanticSearch {
	t.Helper()
	store := &capabilitiesStatusStore{err: statusErr}
	profile := "sha256:" + strings.Repeat("a", 64)
	rankingProfile := "sha256:" + strings.Repeat("b", 64)
	semantic, err := catalog.NewSemanticSearch(catalog.SemanticSearchOptions{
		Store:          store,
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

// capabilitiesStatusStore is a SemanticSearchStore whose Status method returns a
// configured error so we can reach the s.fail branch in handleCapabilities.
type capabilitiesStatusStore struct {
	err error
}

func (s *capabilitiesStatusStore) Status(_ context.Context, _, _ string, _ int) (catalog.SemanticIndexStatus, error) {
	return catalog.SemanticIndexStatus{}, s.err
}
func (s *capabilitiesStatusStore) Current(_ context.Context, _, _ string) (string, error) {
	return "1", nil
}
func (s *capabilitiesStatusStore) Active(context.Context, string, string, string, int) (catalog.SemanticGeneration, bool, error) {
	return catalog.SemanticGeneration{}, false, nil
}
func (s *capabilitiesStatusStore) Search(context.Context, catalog.SemanticSearchRequest) (catalog.SemanticSearchPage, error) {
	return catalog.SemanticSearchPage{}, nil
}
func (s *capabilitiesStatusStore) Explore(context.Context, catalog.SemanticSearchRequest, []catalog.FacetRequest) (catalog.ExploreResponse, error) {
	return catalog.ExploreResponse{}, nil
}
func (s *capabilitiesStatusStore) Similarities(context.Context, catalog.SemanticGeneration, []float32, []string) (map[string]float64, error) {
	return map[string]float64{}, nil
}

// TestHandleCapabilitiesRequiresAuthentication verifies that unauthenticated
// requests receive 401 or 403.
func TestHandleCapabilitiesRequiresAuthentication(t *testing.T) {
	ts, _ := newCapabilitiesServer(t, capabilitiesServerOptions{})
	result := requestJSON(t, ts.URL, http.MethodGet, "/api/v1/capabilities", "", "", "", nil)
	if result.status != http.StatusUnauthorized && result.status != http.StatusForbidden {
		t.Fatalf("unauthenticated: got %d, want 401 or 403; body=%s", result.status, result.body)
	}
}

// TestHandleCapabilitiesCoreTierNoSemantic verifies that a server without a
// semantic provider returns tier=core and semantic=unavailable.
func TestHandleCapabilitiesCoreTierNoSemantic(t *testing.T) {
	ts, tokens := newCapabilitiesServer(t, capabilitiesServerOptions{})
	result := requestJSON(t, ts.URL, http.MethodGet, "/api/v1/capabilities", tokens["contributor"], "", "", nil)
	requireStatus(t, result, http.StatusOK)

	var body map[string]any
	decodeResult(t, result, &body)
	search, ok := body["search"].(map[string]any)
	if !ok {
		t.Fatalf("search field missing or wrong type; body=%s", result.body)
	}
	if search["tier"] != "core" {
		t.Errorf("tier = %v, want core", search["tier"])
	}
	if search["semantic"] != string(CapabilityUnavailable) {
		t.Errorf("semantic = %v, want %s", search["semantic"], CapabilityUnavailable)
	}
}

// TestHandleCapabilitiesHybridTierWhenSemanticEnabled verifies that a server
// with a semantic provider returns tier=hybrid and the ranking profile.
func TestHandleCapabilitiesHybridTierWhenSemanticEnabled(t *testing.T) {
	semantic := capabilitiesSemanticWithStatus(t, nil)
	ts, tokens := newCapabilitiesServer(t, capabilitiesServerOptions{semantic: semantic})
	result := requestJSON(t, ts.URL, http.MethodGet, "/api/v1/capabilities", tokens["contributor"], "", "", nil)
	requireStatus(t, result, http.StatusOK)

	var body map[string]any
	decodeResult(t, result, &body)
	search, ok := body["search"].(map[string]any)
	if !ok {
		t.Fatalf("search field missing or wrong type; body=%s", result.body)
	}
	if search["tier"] != "hybrid" {
		t.Errorf("tier = %v, want hybrid", search["tier"])
	}
	if search["semantic"] != string(CapabilityEnabled) {
		t.Errorf("semantic = %v, want %s", search["semantic"], CapabilityEnabled)
	}
	if search["profile"] == "" || search["profile"] == nil {
		t.Error("search.profile must be set when semantic is enabled")
	}
}

// TestHandleCapabilitiesSemanticSnapshotEnabledButConfigDisabled exercises the
// else-if branch (line 149-150): the snapshot claims Enabled but
// SemanticConfiguration returns false (no wired semantic), so the response must
// downgrade semantic to "disabled".
func TestHandleCapabilitiesSemanticSnapshotEnabledButConfigDisabled(t *testing.T) {
	ts, tokens := newCapabilitiesServer(t, capabilitiesServerOptions{
		snapshotSemantic: CapabilityEnabled, // snapshot says enabled
		// semantic: nil                     // but no real provider wired
	})
	result := requestJSON(t, ts.URL, http.MethodGet, "/api/v1/capabilities", tokens["contributor"], "", "", nil)
	requireStatus(t, result, http.StatusOK)

	var body map[string]any
	decodeResult(t, result, &body)
	search, ok := body["search"].(map[string]any)
	if !ok {
		t.Fatalf("search field missing or wrong type; body=%s", result.body)
	}
	if search["semantic"] != string(CapabilityDisabled) {
		t.Errorf("semantic = %v, want %s", search["semantic"], CapabilityDisabled)
	}
	if search["tier"] != "core" {
		t.Errorf("tier = %v, want core (semantic downgraded)", search["tier"])
	}
}

// TestHandleCapabilitiesAdminSeesIndexingStatus verifies that a tenant admin
// with semantic enabled gets the indexing field populated.
func TestHandleCapabilitiesAdminSeesIndexingStatus(t *testing.T) {
	semantic := capabilitiesSemanticWithStatus(t, nil)
	ts, tokens := newCapabilitiesServer(t, capabilitiesServerOptions{semantic: semantic})
	result := requestJSON(t, ts.URL, http.MethodGet, "/api/v1/capabilities", tokens["admin"], "", "", nil)
	requireStatus(t, result, http.StatusOK)

	var body map[string]any
	decodeResult(t, result, &body)
	search, ok := body["search"].(map[string]any)
	if !ok {
		t.Fatalf("search field missing or wrong type; body=%s", result.body)
	}
	if _, ok := search["indexing"]; !ok {
		t.Errorf("admin should see indexing field; search=%v", search)
	}
}

// TestHandleCapabilitiesNonAdminNoIndexingField verifies that a non-admin actor
// does not receive the indexing field even when semantic is enabled.
func TestHandleCapabilitiesNonAdminNoIndexingField(t *testing.T) {
	semantic := capabilitiesSemanticWithStatus(t, nil)
	ts, tokens := newCapabilitiesServer(t, capabilitiesServerOptions{semantic: semantic})
	result := requestJSON(t, ts.URL, http.MethodGet, "/api/v1/capabilities", tokens["contributor"], "", "", nil)
	requireStatus(t, result, http.StatusOK)

	var body map[string]any
	decodeResult(t, result, &body)
	search, ok := body["search"].(map[string]any)
	if !ok {
		t.Fatalf("search field missing or wrong type; body=%s", result.body)
	}
	if _, ok := search["indexing"]; ok {
		t.Errorf("non-admin must not see indexing field; search=%v", search)
	}
}

// TestHandleCapabilitiesWebhooksOperationIncluded verifies that
// "webhooks.signed" appears in operations when Webhooks capability is enabled.
func TestHandleCapabilitiesWebhooksOperationIncluded(t *testing.T) {
	serviceClock := clock.NewFake(time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC))
	webhookCipher, err := webhook.NewAESGCM(make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	webhookService, err := webhook.NewService(webhook.NewMemoryStore(), webhookCipher, noopWebhookSender{}, serviceClock, id.Generator{})
	if err != nil {
		t.Fatal(err)
	}

	ts, tokens := newCapabilitiesServer(t, capabilitiesServerOptions{webhookService: webhookService})
	result := requestJSON(t, ts.URL, http.MethodGet, "/api/v1/capabilities", tokens["contributor"], "", "", nil)
	requireStatus(t, result, http.StatusOK)

	var body map[string]any
	decodeResult(t, result, &body)
	rawOps, ok := body["operations"].([]any)
	if !ok {
		t.Fatalf("operations field missing or wrong type; body=%s", result.body)
	}
	ops := make([]string, len(rawOps))
	for i, v := range rawOps {
		ops[i], _ = v.(string)
	}
	if !slices.Contains(ops, "webhooks.signed") {
		t.Errorf("webhooks.signed not in operations=%v", ops)
	}
}

// TestHandleCapabilitiesWebhooksOperationAbsent verifies that "webhooks.signed"
// is absent when Webhooks is disabled (the default).
func TestHandleCapabilitiesWebhooksOperationAbsent(t *testing.T) {
	ts, tokens := newCapabilitiesServer(t, capabilitiesServerOptions{})
	result := requestJSON(t, ts.URL, http.MethodGet, "/api/v1/capabilities", tokens["contributor"], "", "", nil)
	requireStatus(t, result, http.StatusOK)

	var body map[string]any
	decodeResult(t, result, &body)
	rawOps, _ := body["operations"].([]any)
	for _, v := range rawOps {
		if s, ok := v.(string); ok && s == "webhooks.signed" {
			t.Error("webhooks.signed must not appear when webhooks are disabled")
		}
	}
}

// TestHandleCapabilitiesSemanticStatusErrorReturns500 verifies that when the
// semantic Status store fails, the admin capabilities request returns 500.
func TestHandleCapabilitiesSemanticStatusErrorReturns500(t *testing.T) {
	storeErr := errors.New("store unavailable")
	semantic := capabilitiesSemanticWithStatus(t, storeErr)
	ts, tokens := newCapabilitiesServer(t, capabilitiesServerOptions{semantic: semantic})
	result := requestJSON(t, ts.URL, http.MethodGet, "/api/v1/capabilities", tokens["admin"], "", "", nil)
	if result.status != http.StatusInternalServerError && result.status != http.StatusServiceUnavailable {
		t.Fatalf("semantic status error: got %d, want 500 or 503; body=%s", result.status, result.body)
	}
}

// TestHandleCapabilitiesResponseStructure verifies the top-level fields in the
// capabilities response are all present.
func TestHandleCapabilitiesResponseStructure(t *testing.T) {
	ts, tokens := newCapabilitiesServer(t, capabilitiesServerOptions{})
	result := requestJSON(t, ts.URL, http.MethodGet, "/api/v1/capabilities", tokens["contributor"], "", "", nil)
	requireStatus(t, result, http.StatusOK)

	var body map[string]json.RawMessage
	decodeResult(t, result, &body)
	for _, field := range []string{"resourceKinds", "profiles", "operations", "governance", "search", "optionalCapabilities", "limits", "profileBundle", "degradations"} {
		if _, ok := body[field]; !ok {
			t.Errorf("response missing field %q", field)
		}
	}
}

// noopWebhookSender satisfies the webhook.Sender interface without doing anything.
type noopWebhookSender struct{}

func (noopWebhookSender) Send(context.Context, webhook.SendRequest) (webhook.SendResult, error) {
	return webhook.SendResult{}, nil
}
