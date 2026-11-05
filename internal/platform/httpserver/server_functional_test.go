//go:build functional

// Functional tests exercise local HTTP or composed service workflows.
package httpserver

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/getkin/kin-openapi/openapi3"
	serviceapi "github.com/open-agentic-catalog/open-agentic-catalog/api"
	"github.com/open-agentic-catalog/open-agentic-catalog/test/identityfixture"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/adapter"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/artwork"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/audit"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/federation"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/jobs"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/personal"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/authn"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/clock"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/config"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/fetcher"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/id"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/idempotency"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/observability"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/publication"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/ratings"
)

type unavailableFetcher struct{}

func (unavailableFetcher) Fetch(context.Context, fetcher.Request) (fetcher.Result, error) {
	return fetcher.Result{}, errors.New("no remote source configured")
}

func (unavailableFetcher) PolicyDigest() string {
	return "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
}

type routedFetcher map[string]fetcher.Result

func (responses routedFetcher) Fetch(_ context.Context, request fetcher.Request) (fetcher.Result, error) {
	method := request.Method
	if method == "" {
		method = http.MethodGet
	}
	result, ok := responses[method+" "+request.URL]
	if !ok {
		return fetcher.Result{}, errors.New("no response configured for " + method + " " + request.URL)
	}
	return result, nil
}

func (routedFetcher) PolicyDigest() string {
	return "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
}

type httpCatalogExternalFetcher struct {
	now   func() time.Time
	body  []byte
	calls int
}

func (*httpCatalogExternalFetcher) PolicyDigest() string {
	return "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
}

func (f *httpCatalogExternalFetcher) FetchExternalRepresentation(_ context.Context, request catalog.ExternalRepresentationFetchRequest) (catalog.ExternalRepresentationFetchResult, error) {
	f.calls++
	digest := sha256.Sum256(f.body)
	return catalog.ExternalRepresentationFetchResult{RequestedURL: request.URL, FinalURL: request.URL, Status: http.StatusOK, MediaType: request.MediaType, Body: append([]byte(nil), f.body...), ContentDigest: "sha256:" + hex.EncodeToString(digest[:]), PolicyID: "test-policy", PolicyRevision: 1, PolicyDigest: f.PolicyDigest(), RetrievedAt: f.now()}, nil
}

type testPublicationWithdrawer struct {
	manager *publication.Manager
}

func (w *testPublicationWithdrawer) WithdrawPublicationDomain(ctx context.Context, tenantID, domain, action, targetID, cause string) ([]string, error) {
	changed, err := w.manager.WithdrawDomain(ctx, tenantID, domain, publication.WithdrawalCause{Cause: cause, OriginatingAction: action, OriginatingTargetID: targetID})
	if err != nil || !changed {
		return nil, err
	}
	return []string{"127.0.0.1"}, nil
}

func (w *testPublicationWithdrawer) WithdrawPublicationRelease(ctx context.Context, tenantID, releaseID, action, targetID, cause string) ([]string, error) {
	changed, err := w.manager.WithdrawRelease(ctx, tenantID, releaseID, publication.WithdrawalCause{Cause: cause, OriginatingAction: action, OriginatingTargetID: targetID})
	if err != nil || !changed {
		return nil, err
	}
	return []string{"127.0.0.1"}, nil
}

type httpResult struct {
	status int
	header http.Header
	body   []byte
}

func newTestIdentity(t *testing.T, serviceClock identity.Clock, users map[string]config.DevelopmentUser) (authn.Authenticator, *identity.Service) {
	t.Helper()
	seeds := make([]identity.Seed, 0, len(users))
	for _, user := range users {
		seeds = append(seeds, identity.Seed{TenantID: user.TenantID, Issuer: authn.DevelopmentIssuer, Subject: user.Subject, ActorType: identity.ActorHuman, Roles: user.Roles, Namespaces: user.Namespaces})
	}
	store, err := identity.NewMemoryStore(seeds)
	if err != nil {
		t.Fatal(err)
	}
	cursorKey := sha256.Sum256([]byte("http-identity-test-key"))
	service, err := identity.NewService(identity.ServiceOptions{Store: store, Clock: serviceClock, IDs: id.Generator{}, CursorKey: cursorKey[:], AcknowledgeOutboxAtCreation: true})
	if err != nil {
		t.Fatal(err)
	}
	authenticator, err := authn.New(context.Background(), config.Auth{Mode: "development", DevelopmentTokens: users}, store)
	if err != nil {
		t.Fatal(err)
	}
	return authenticator, service
}

func anonymousDiscoveryHost(manager *publication.Manager) DiscoveryHost {
	return DiscoveryHost{Host: "127.0.0.1", TenantID: "00000000-0000-7000-8000-000000000001", Mode: DiscoveryAnonymous, PublisherDomain: "catalog.example.com", Publication: manager}
}

func TestHTTPAuthoringPublicationSecurityAndReplay(t *testing.T) {
	serviceClock := clock.NewFake(time.Date(2026, 8, 29, 11, 0, 0, 0, time.UTC))
	repository := catalog.NewMemoryRepository()
	cursorKey := sha256.Sum256([]byte("http-contract-test-key"))
	imageFetcher := &artworkHTTPFetcher{body: artworkHTTPImage(t)}
	media, mediaErr := artwork.New(imageFetcher, artwork.DefaultLimits())
	if mediaErr != nil {
		t.Fatal(mediaErr)
	}
	tenantID := "00000000-0000-7000-8000-000000000001"
	withdrawer := &testPublicationWithdrawer{}
	externalFetcher := &httpCatalogExternalFetcher{now: serviceClock.Now, body: []byte(`{"name":"External HTTP Agent","description":"Exercises bounded HTTPS representations","version":"2.0.0","supportedInterfaces":[{"url":"https://agents.example.com/external","protocolBinding":"JSONRPC","protocolVersion":"1.0"}],"capabilities":{},"defaultInputModes":["application/json"],"defaultOutputModes":["application/json"],"skills":[{"id":"external-http","name":"External HTTP","description":"Exercises external HTTP representation validation","tags":["http"]}]}`)}
	catalogService, err := catalog.NewService(catalog.Options{Artwork: media, Repository: repository, Clock: serviceClock, IDs: id.Generator{}, Validator: adapter.NewRegistry(), ExternalFetcher: externalFetcher, CursorKey: cursorKey[:], Policy: "test-policy", PublicationWithdrawer: withdrawer})
	if err != nil {
		t.Fatal(err)
	}
	federationManager, err := federation.NewManager(federation.NewMemoryStore(), unavailableFetcher{}, serviceClock, id.Generator{})
	if err != nil {
		t.Fatal(err)
	}
	publicationManager, err := publication.NewManager(publication.Options{Records: catalogService, Store: publication.NewMemoryStore(), Clock: serviceClock, IDs: id.Generator{}, BaseURL: "https://catalog.example.com", PublisherDomain: "catalog.example.com", ARDProfile: "ard-test", AICatalogProfile: "ai-catalog-test", CursorKey: cursorKey[:]})
	if err != nil {
		t.Fatal(err)
	}
	withdrawer.manager = publicationManager
	jobStore := jobs.NewMemoryStore()
	jobService, err := jobs.NewService(jobStore, serviceClock, id.Generator{})
	if err != nil {
		t.Fatal(err)
	}
	authenticator, identityService := newTestIdentity(t, serviceClock, map[string]config.DevelopmentUser{
		"admin":       {Subject: "admin", TenantID: tenantID, Roles: []identity.Role{identity.RoleTenantAdmin, identity.RoleContributor, identity.RolePublisher}},
		"contributor": {Subject: "contributor", TenantID: tenantID, Roles: []identity.Role{identity.RoleContributor, identity.RoleReader}},
		"reviewer":    {Subject: "reviewer", TenantID: tenantID, Roles: []identity.Role{identity.RoleReviewer, identity.RoleReader}},
		"publisher":   {Subject: "publisher", TenantID: tenantID, Roles: []identity.Role{identity.RolePublisher, identity.RoleReader}},
		"other":       {Subject: "other", TenantID: "00000000-0000-7000-8000-000000000002", Roles: []identity.Role{identity.RoleReader}},
		"other-admin": {Subject: "other-admin", TenantID: "00000000-0000-7000-8000-000000000002", Roles: []identity.Role{identity.RoleTenantAdmin, identity.RoleReader}},
	})
	// This semantic journey includes the 50,000-entry diagnostic amplification
	// fixture. Use the normal work budget, not an accidental one-second latency
	// requirement under -race; deadline tests exercise their own short budgets.
	server, err := New(Dependencies{PersonalStore: personal.NewMemoryStore(repository), Artwork: media, MediaPolicyDigest: "fixture-media-policy", Catalog: catalogService, Identity: identityService, Federation: federationManager, Jobs: jobService, Authenticator: authenticator, Idempotency: idempotency.NewMemoryStore(), Logger: slog.New(slog.DiscardHandler), DiscoveryHosts: []DiscoveryHost{anonymousDiscoveryHost(publicationManager)}, MaxBodyBytes: 1 << 20, RequestTimeout: config.Defaults().HTTP.RequestTimeout.Duration, ServiceVersion: "test", Commit: "test", ARDProfile: "ard-test", AICatalogProfile: "ai-catalog-test", Capabilities: CapabilitySnapshot{SemanticSearch: CapabilityUnavailable, CredentialResolution: CapabilityUnavailable, ManagedMedia: CapabilityUnavailable, SoftwareAssessment: CapabilityUnavailable, Webhooks: CapabilityDisabled}, CursorKey: cursorKey[:], Now: serviceClock.Now})
	if err != nil {
		t.Fatal(err)
	}
	testServer := httptest.NewServer(server.Handler())
	defer testServer.Close()
	missingManifest := requestJSON(t, testServer.URL, http.MethodGet, "/.well-known/ard.json", "", "", "", nil)
	requireStatus(t, missingManifest, http.StatusNotFound)
	missingAICatalog := requestJSON(t, testServer.URL, http.MethodGet, "/.well-known/ai-catalog.json", "", "", "", nil)
	requireStatus(t, missingAICatalog, http.StatusNotFound)
	invalidAuthentication := requestWithHeaders(t, testServer.URL, http.MethodGet, "/api/v1/capabilities", nil, map[string]string{"Authorization": "Basic invalid"})
	requireStatus(t, invalidAuthentication, http.StatusUnauthorized)
	if !strings.HasPrefix(invalidAuthentication.header.Get("Content-Type"), "application/problem+json") || invalidAuthentication.header.Get("X-Request-ID") == "" {
		t.Fatalf("authentication error is not an attributable problem response: %#v", invalidAuthentication.header)
	}
	missingAuthentication := requestJSON(t, testServer.URL, http.MethodGet, "/api/v1/capabilities", "", "", "", nil)
	requireStatus(t, missingAuthentication, http.StatusUnauthorized)
	capabilitiesResult := requestJSON(t, testServer.URL, http.MethodGet, "/api/v1/capabilities", "admin", "", "", nil)
	requireStatus(t, capabilitiesResult, http.StatusOK)
	requireControlSchema(t, "Capabilities", capabilitiesResult.body)
	var capabilities capabilitiesResponse
	decodeResult(t, capabilitiesResult, &capabilities)
	if capabilities.Governance.PublicationAuthorization != publicationAuthorizationAdministratorApproved || capabilities.Governance.PublisherAuthority != catalog.TrustNotAssessed || capabilities.Governance.LegalRights != catalog.LegalRightsNotAssessed {
		t.Fatalf("capabilities governance = %#v", capabilities.Governance)
	}
	if capabilities.Search.Tier != searchTierCore || capabilities.Search.Profile != catalog.CoreSearchProfile || capabilities.Search.Semantic != CapabilityUnavailable || capabilities.Search.Degraded {
		t.Fatalf("capabilities search = %#v", capabilities.Search)
	}
	if capabilities.OptionalCapabilities.Webhooks != CapabilityDisabled || capabilities.OptionalCapabilities.CredentialResolution != CapabilityUnavailable || capabilities.OptionalCapabilities.ManagedMedia != CapabilityUnavailable || capabilities.OptionalCapabilities.SoftwareAssessment != CapabilityUnavailable {
		t.Fatalf("optional capabilities = %#v", capabilities.OptionalCapabilities)
	}
	disabledWebhooks := requestJSON(t, testServer.URL, http.MethodGet, "/api/v1/webhooks", "admin", "", "", nil)
	requireControlProblem(t, disabledWebhooks, http.StatusNotFound, catalog.CodeNotFound)
	for index, testCase := range []struct {
		method  string
		path    string
		ifMatch string
		body    any
	}{
		{method: http.MethodPost, path: "/api/v1/webhooks", body: map[string]any{}},
		{method: http.MethodGet, path: "/api/v1/webhooks/missing"},
		{method: http.MethodPatch, path: "/api/v1/webhooks/missing", ifMatch: `"rev-1"`, body: map[string]any{}},
		{method: http.MethodDelete, path: "/api/v1/webhooks/missing", ifMatch: `"rev-1"`},
		{method: http.MethodPost, path: "/api/v1/webhooks/missing:rotate-secret", ifMatch: `"rev-1"`, body: map[string]any{}},
		{method: http.MethodPost, path: "/api/v1/webhooks/missing:test", ifMatch: `"rev-1"`, body: map[string]any{}},
		{method: http.MethodGet, path: "/api/v1/webhooks/missing/deliveries"},
	} {
		t.Run("disabled webhook "+testCase.method+" "+testCase.path, func(t *testing.T) {
			key := ""
			if testCase.method == http.MethodPost || testCase.method == http.MethodPatch || testCase.method == http.MethodDelete {
				key = "disabled-webhook-" + strconv.Itoa(index)
			}
			result := requestJSON(t, testServer.URL, testCase.method, testCase.path, "admin", key, testCase.ifMatch, testCase.body)
			requireControlProblem(t, result, http.StatusNotFound, catalog.CodeNotFound)
		})
	}
	if capabilities.Limits.RequestBodyBytes != 1<<20 || capabilities.Limits.CatalogDocumentBytes != catalog.MaxCatalogDocumentBytes || capabilities.Limits.SkillPackageBytes != catalog.MaxSkillPackageBytes || capabilities.Limits.SkillPackageExpandedBytes != catalog.MaxSkillPackageExpandedBytes || capabilities.Limits.SkillPackageFileBytes != catalog.MaxSkillPackageFileBytes || capabilities.Limits.SkillPackageFiles != catalog.MaxSkillPackageFiles || capabilities.Limits.SkillPackageDepth != catalog.MaxSkillPackageDepth || capabilities.Limits.SkillPackageExpansionRatio != catalog.MaxSkillPackageExpansionRatio || capabilities.Limits.CatalogPageSize != catalog.MaxCatalogPageSize || capabilities.Limits.ARDPageSize != publication.MaxARDPageSize || capabilities.Limits.LiveFederationPageSize != federation.MaxLiveSearchPageSize || capabilities.Limits.PublicationRecords != catalog.MaxPublicationRecords || capabilities.Limits.PublicationProjectionBytes != catalog.MaxPublicationProjectionBytes || capabilities.Limits.PublicationARDEntries != publication.MaxPublicationARDEntries || capabilities.Limits.PublicationAICatalogEntries != publication.MaxPublicationAICatalogEntries || capabilities.Limits.PublicationExclusions != publication.MaxPublicationExclusions || capabilities.Limits.PublicationArtifactReferences != publication.MaxPublicationArtifactReferences || capabilities.Limits.PublicationDocumentBytes != publication.MaxPublicationDocumentBytes || capabilities.Limits.PublicationSnapshotBytes != publication.MaxPublicationSnapshotBytes || capabilities.Limits.PublicationWorkUnits != publication.MaxPublicationWorkUnits || capabilities.Limits.PublicationConstructions != publication.MaxConcurrentConstructions || len(capabilities.Degradations) != 0 {
		t.Fatalf("capability limits or degradations = %#v, %#v", capabilities.Limits, capabilities.Degradations)
	}
	if bytes.Contains(capabilitiesResult.body, []byte(`"maxFanout"`)) || bytes.Contains(capabilitiesResult.body, []byte(`"maxHops"`)) {
		t.Fatalf("capabilities advertised unwired federation limits: %s", capabilitiesResult.body)
	}
	forbiddenRoles := requestJSON(t, testServer.URL, http.MethodGet, "/api/v1/role-assignments", "contributor", "", "", nil)
	requireStatus(t, forbiddenRoles, http.StatusForbidden)
	grantRole := requestJSON(t, testServer.URL, http.MethodPost, "/api/v1/role-assignments", "admin", "grant-service-reader", "", map[string]any{"subject": "service-reader", "actorType": "service", "role": "reader", "reason": "Allow governed service reads"})
	requireStatus(t, grantRole, http.StatusCreated)
	requireControlSchema(t, "RoleAssignment", grantRole.body)
	var grantedRole identity.RoleAssignment
	decodeResult(t, grantRole, &grantedRole)
	if grantedRole.ActorType != identity.ActorService || grantedRole.Role != identity.RoleReader || grantRole.header.Get("ETag") != `"rev-1"` || grantRole.header.Get("Location") != "/api/v1/role-assignments/"+grantedRole.ID {
		t.Fatalf("granted role = %#v, headers %#v", grantedRole, grantRole.header)
	}
	grantReplay := requestJSON(t, testServer.URL, http.MethodPost, "/api/v1/role-assignments", "admin", "grant-service-reader", "", map[string]any{"subject": "service-reader", "actorType": "service", "role": "reader", "reason": "Allow governed service reads"})
	requireStatus(t, grantReplay, http.StatusCreated)
	if grantReplay.header.Get("Idempotency-Replayed") != "true" || !bytes.Equal(grantReplay.body, grantRole.body) {
		t.Fatalf("role grant replay = headers %#v, body %s", grantReplay.header, grantReplay.body)
	}
	forbiddenMemberships := requestJSON(t, testServer.URL, http.MethodGet, "/api/v1/memberships", "contributor", "", "", nil)
	requireControlProblem(t, forbiddenMemberships, http.StatusForbidden, catalog.CodeForbidden)
	invalidMembershipQuery := requestJSON(t, testServer.URL, http.MethodGet, "/api/v1/memberships?unknown=true", "admin", "", "", nil)
	requireControlProblem(t, invalidMembershipQuery, http.StatusBadRequest, catalog.CodeInvalidArgument)
	invalidMembershipLimit := requestJSON(t, testServer.URL, http.MethodGet, "/api/v1/memberships?limit=0", "admin", "", "", nil)
	requireControlProblem(t, invalidMembershipLimit, http.StatusBadRequest, catalog.CodeInvalidArgument)
	emptyMembershipCursor := requestJSON(t, testServer.URL, http.MethodGet, "/api/v1/memberships?cursor=", "admin", "", "", nil)
	requireControlProblem(t, emptyMembershipCursor, http.StatusBadRequest, catalog.CodeInvalidArgument)
	membershipFirstPage := requestJSON(t, testServer.URL, http.MethodGet, "/api/v1/memberships?limit=2", "admin", "", "", nil)
	requireStatus(t, membershipFirstPage, http.StatusOK)
	requireControlSchema(t, "MembershipPage", membershipFirstPage.body)
	var firstMembershipPage identity.MembershipPage
	decodeResult(t, membershipFirstPage, &firstMembershipPage)
	if len(firstMembershipPage.Items) != 2 || firstMembershipPage.NextCursor == "" {
		t.Fatalf("first membership page = %#v", firstMembershipPage)
	}
	membershipSecondPage := requestJSON(t, testServer.URL, http.MethodGet, "/api/v1/memberships?limit=2&cursor="+firstMembershipPage.NextCursor, "admin", "", "", nil)
	requireStatus(t, membershipSecondPage, http.StatusOK)
	wrongLimitMembershipCursor := requestJSON(t, testServer.URL, http.MethodGet, "/api/v1/memberships?limit=3&cursor="+firstMembershipPage.NextCursor, "admin", "", "", nil)
	requireControlProblem(t, wrongLimitMembershipCursor, http.StatusBadRequest, catalog.CodeInvalidArgument)
	allMembershipsResult := requestJSON(t, testServer.URL, http.MethodGet, "/api/v1/memberships?limit=100", "admin", "", "", nil)
	requireStatus(t, allMembershipsResult, http.StatusOK)
	var allMemberships identity.MembershipPage
	decodeResult(t, allMembershipsResult, &allMemberships)
	var serviceMembership, administratorMembership identity.Membership
	for _, membership := range allMemberships.Items {
		if membership.Subject == "service-reader" {
			serviceMembership = membership
		}
		if membership.Subject == "admin" {
			administratorMembership = membership
		}
	}
	if serviceMembership.ID == "" || administratorMembership.ID == "" {
		t.Fatalf("membership page omitted expected identities: %#v", allMemberships)
	}
	membershipGet := requestJSON(t, testServer.URL, http.MethodGet, "/api/v1/memberships/"+serviceMembership.ID, "admin", "", "", nil)
	requireStatus(t, membershipGet, http.StatusOK)
	requireControlSchema(t, "Membership", membershipGet.body)
	if membershipGet.header.Get("ETag") != `"rev-1"` {
		t.Fatalf("membership ETag = %q", membershipGet.header.Get("ETag"))
	}
	otherMembershipsResult := requestJSON(t, testServer.URL, http.MethodGet, "/api/v1/memberships?limit=100", "other-admin", "", "", nil)
	requireStatus(t, otherMembershipsResult, http.StatusOK)
	var otherMemberships identity.MembershipPage
	decodeResult(t, otherMembershipsResult, &otherMemberships)
	if len(otherMemberships.Items) == 0 {
		t.Fatal("other tenant membership page was empty")
	}
	crossTenantMembership := requestJSON(t, testServer.URL, http.MethodGet, "/api/v1/memberships/"+otherMemberships.Items[0].ID, "admin", "", "", nil)
	requireControlProblem(t, crossTenantMembership, http.StatusNotFound, catalog.CodeNotFound)
	missingMembershipMatch := requestJSON(t, testServer.URL, http.MethodPost, "/api/v1/memberships/"+serviceMembership.ID+":suspend", "admin", "suspend-service-membership-missing-match", "", map[string]any{"reason": "Missing revision"})
	requireControlProblem(t, missingMembershipMatch, http.StatusPreconditionFailed, catalog.CodePreconditionFailed)
	invalidMembershipBody := requestJSON(t, testServer.URL, http.MethodPost, "/api/v1/memberships/"+serviceMembership.ID+":suspend", "admin", "suspend-service-membership-invalid-body", `"rev-1"`, map[string]any{"reason": "Temporary access hold", "unknown": true})
	requireControlProblem(t, invalidMembershipBody, http.StatusBadRequest, catalog.CodeInvalidArgument)
	unknownMembershipCommand := requestJSON(t, testServer.URL, http.MethodPost, "/api/v1/memberships/"+serviceMembership.ID+":archive", "admin", "archive-service-membership", `"rev-1"`, map[string]any{"reason": "Unsupported transition"})
	requireControlProblem(t, unknownMembershipCommand, http.StatusNotFound, catalog.CodeNotFound)
	suspendMembership := requestJSON(t, testServer.URL, http.MethodPost, "/api/v1/memberships/"+serviceMembership.ID+":suspend", "admin", "suspend-service-membership", `"rev-1"`, map[string]any{"reason": "Temporary access hold"})
	requireStatus(t, suspendMembership, http.StatusOK)
	requireControlSchema(t, "MembershipLifecycleResult", suspendMembership.body)
	var suspendedMembership identity.MembershipLifecycleResult
	decodeResult(t, suspendMembership, &suspendedMembership)
	if suspendedMembership.Membership.State != identity.MembershipSuspended || suspendMembership.header.Get("ETag") != `"rev-2"` {
		t.Fatalf("suspended membership = %#v, headers %#v", suspendedMembership, suspendMembership.header)
	}
	reactivateMembership := requestJSON(t, testServer.URL, http.MethodPost, "/api/v1/memberships/"+serviceMembership.ID+":reactivate", "admin", "reactivate-service-membership", `"rev-2"`, map[string]any{"reason": "Access restored"})
	requireStatus(t, reactivateMembership, http.StatusOK)
	finalAdministratorSuspend := requestJSON(t, testServer.URL, http.MethodPost, "/api/v1/memberships/"+administratorMembership.ID+":suspend", "admin", "suspend-final-administrator", `"rev-1"`, map[string]any{"reason": "Must retain an administrator"})
	requireControlProblem(t, finalAdministratorSuspend, http.StatusConflict, catalog.CodeSeparationOfDuties)
	roleList := requestJSON(t, testServer.URL, http.MethodGet, "/api/v1/role-assignments", "admin", "", "", nil)
	requireStatus(t, roleList, http.StatusOK)
	var rolePage struct {
		Items []identity.RoleAssignment `json:"items"`
	}
	decodeResult(t, roleList, &rolePage)
	if len(rolePage.Items) < 2 {
		t.Fatalf("role assignment page = %s", roleList.body)
	}
	staleRoleRevoke := requestJSON(t, testServer.URL, http.MethodPost, "/api/v1/role-assignments/"+grantedRole.ID+":revoke", "admin", "revoke-service-reader-stale", `"rev-2"`, map[string]any{"reason": "Stale offboarding attempt"})
	requireStatus(t, staleRoleRevoke, http.StatusPreconditionFailed)
	revokeRole := requestJSON(t, testServer.URL, http.MethodPost, "/api/v1/role-assignments/"+grantedRole.ID+":revoke", "admin", "revoke-service-reader", `"rev-1"`, map[string]any{"reason": "Service access ended"})
	requireStatus(t, revokeRole, http.StatusOK)
	requireControlSchema(t, "RoleAssignment", revokeRole.body)
	revokeReplay := requestJSON(t, testServer.URL, http.MethodPost, "/api/v1/role-assignments/"+grantedRole.ID+":revoke", "admin", "revoke-service-reader", `"rev-1"`, map[string]any{"reason": "Service access ended"})
	requireStatus(t, revokeReplay, http.StatusOK)
	if !bytes.Equal(revokeReplay.body, revokeRole.body) || revokeReplay.header.Get("Idempotency-Replayed") != "true" {
		t.Fatal("role revocation did not replay the original response")
	}
	offboardMembership := requestJSON(t, testServer.URL, http.MethodPost, "/api/v1/memberships/"+serviceMembership.ID+":offboard", "admin", "offboard-service-membership", `"rev-3"`, map[string]any{"reason": "Service identity retired"})
	requireStatus(t, offboardMembership, http.StatusOK)
	var offboardedMembership identity.MembershipLifecycleResult
	decodeResult(t, offboardMembership, &offboardedMembership)
	if offboardedMembership.Membership.State != identity.MembershipOffboarded || offboardedMembership.RevokedRoleAssignments != 0 {
		t.Fatalf("offboarded membership = %#v", offboardedMembership)
	}
	terminalMembership := requestJSON(t, testServer.URL, http.MethodPost, "/api/v1/memberships/"+serviceMembership.ID+":reactivate", "admin", "reactivate-offboarded-membership", `"rev-4"`, map[string]any{"reason": "Terminal state must remain closed"})
	requireControlProblem(t, terminalMembership, http.StatusConflict, catalog.CodeConflict)
	var adminRole identity.RoleAssignment
	for _, assignment := range rolePage.Items {
		if assignment.Subject == "admin" && assignment.Role == identity.RoleTenantAdmin {
			adminRole = assignment
			break
		}
	}
	if adminRole.ID == "" {
		t.Fatal("seeded tenant administrator assignment was not listed")
	}
	finalAdminRevoke := requestJSON(t, testServer.URL, http.MethodPost, "/api/v1/role-assignments/"+adminRole.ID+":revoke", "admin", "revoke-final-admin", `"rev-1"`, map[string]any{"reason": "Must retain one administrator"})
	requireStatus(t, finalAdminRevoke, http.StatusConflict)
	snapshotHistoryAdvertised := false
	skillPackageAdvertised := false
	skillHandoffAdvertised := false
	for _, operation := range capabilities.Operations {
		if operation == "webhooks.signed" {
			t.Fatal("capabilities advertised webhooks when the webhook service was not configured")
		}
		if operation == "source.run_history" {
			t.Fatal("capabilities advertised the unimplemented generic source run history")
		}
		snapshotHistoryAdvertised = snapshotHistoryAdvertised || operation == "source.snapshot_history"
		skillPackageAdvertised = skillPackageAdvertised || operation == "skill.package.inspect"
		skillHandoffAdvertised = skillHandoffAdvertised || operation == "skill.handoff"
	}
	if !snapshotHistoryAdvertised {
		t.Fatal("capabilities omitted the implemented source snapshot history")
	}
	if !skillPackageAdvertised || !skillHandoffAdvertised {
		t.Fatal("capabilities omitted Skill package inspection or immutable handoff")
	}
	invalidCatalogLimit := requestJSON(t, testServer.URL, http.MethodGet, "/api/v1/catalog/resources?limit=101", "admin", "", "", nil)
	requireStatus(t, invalidCatalogLimit, http.StatusBadRequest)
	invalidCatalogCursor := requestJSON(t, testServer.URL, http.MethodGet, "/api/v1/catalog/resources?cursor=offset%3A0", "admin", "", "", nil)
	requireStatus(t, invalidCatalogCursor, http.StatusBadRequest)
	duplicateJSON := requestWithHeaders(t, testServer.URL, http.MethodPost, "/api/v1/namespaces", []byte(`{"slug":"one","slug":"two","displayName":"Duplicate"}`), map[string]string{"Authorization": "Bearer admin", "Content-Type": "application/json", "Idempotency-Key": "duplicate-json"})
	requireStatus(t, duplicateJSON, http.StatusBadRequest)

	namespaceResult := requestJSON(t, testServer.URL, http.MethodPost, "/api/v1/namespaces", "admin", "namespace-one", "", map[string]any{"slug": "default", "displayName": "Default"})
	requireStatus(t, namespaceResult, http.StatusCreated)
	requireControlSchema(t, "Namespace", namespaceResult.body)
	var namespace catalog.Namespace
	decodeResult(t, namespaceResult, &namespace)
	namespaceList := requestJSON(t, testServer.URL, http.MethodGet, "/api/v1/namespaces?limit=1", "admin", "", "", nil)
	requireStatus(t, namespaceList, http.StatusOK)
	requireControlSchema(t, "NamespacePage", namespaceList.body)
	namespaceGet := requestJSON(t, testServer.URL, http.MethodGet, "/api/v1/namespaces/"+namespace.ID, "admin", "", "", nil)
	requireStatus(t, namespaceGet, http.StatusOK)
	requireControlSchema(t, "Namespace", namespaceGet.body)
	missingNamespaceMatch := requestJSON(t, testServer.URL, http.MethodPatch, "/api/v1/namespaces/"+namespace.ID, "admin", "namespace-patch-missing-match", "", map[string]any{"displayName": "Missing revision", "reason": "Missing precondition"})
	requireControlProblem(t, missingNamespaceMatch, http.StatusPreconditionFailed, catalog.CodePreconditionFailed)
	invalidNamespacePatch := requestJSON(t, testServer.URL, http.MethodPatch, "/api/v1/namespaces/"+namespace.ID, "admin", "namespace-patch-invalid-body", `"rev-1"`, map[string]any{"displayName": "Invalid body", "reason": "Reject unknown fields", "unknown": true})
	requireControlProblem(t, invalidNamespacePatch, http.StatusBadRequest, catalog.CodeInvalidArgument)
	namespacePatch := requestJSON(t, testServer.URL, http.MethodPatch, "/api/v1/namespaces/"+namespace.ID, "admin", "namespace-patch", `"rev-1"`, map[string]any{"displayName": "Default namespace", "description": "Primary authoring namespace.", "reason": "Clarify namespace metadata"})
	requireStatus(t, namespacePatch, http.StatusOK)
	requireControlSchema(t, "Namespace", namespacePatch.body)
	var updatedNamespace catalog.Namespace
	decodeResult(t, namespacePatch, &updatedNamespace)
	if updatedNamespace.Slug != namespace.Slug || updatedNamespace.DisplayName != "Default namespace" || updatedNamespace.Revision != 2 || namespacePatch.header.Get("ETag") != `"rev-2"` {
		t.Fatalf("updated namespace = %#v, headers %#v", updatedNamespace, namespacePatch.header)
	}
	staleNamespacePatch := requestJSON(t, testServer.URL, http.MethodPatch, "/api/v1/namespaces/"+namespace.ID, "admin", "namespace-patch-stale", `"rev-1"`, map[string]any{"displayName": "Stale", "reason": "Stale update"})
	requireControlProblem(t, staleNamespacePatch, http.StatusPreconditionFailed, catalog.CodePreconditionFailed)
	replay := requestJSON(t, testServer.URL, http.MethodPost, "/api/v1/namespaces", "admin", "namespace-one", "", map[string]any{"slug": "default", "displayName": "Default"})
	requireStatus(t, replay, http.StatusCreated)
	if replay.header.Get("Idempotency-Replayed") != "true" {
		t.Fatal("idempotent create was not replayed")
	}
	conflict := requestJSON(t, testServer.URL, http.MethodPost, "/api/v1/namespaces", "admin", "namespace-one", "", map[string]any{"slug": "different", "displayName": "Different"})
	requireStatus(t, conflict, http.StatusConflict)

	deletableResult := requestJSON(t, testServer.URL, http.MethodPost, "/api/v1/resources", "contributor", "deletable-resource", "", map[string]any{"namespaceId": namespace.ID, "canonicalIdentifier": "urn:air:catalog.example.com:default:temporary-http-agent", "kind": "agent"})
	requireStatus(t, deletableResult, http.StatusCreated)
	var deletable catalog.Resource
	decodeResult(t, deletableResult, &deletable)
	missingDeleteMatch := requestJSON(t, testServer.URL, http.MethodPost, "/api/v1/resources/"+deletable.ID+":delete", "contributor", "delete-draft-missing-match", "", map[string]any{"reason": "Missing revision"})
	requireControlProblem(t, missingDeleteMatch, http.StatusPreconditionFailed, catalog.CodePreconditionFailed)
	invalidDeleteBody := requestJSON(t, testServer.URL, http.MethodPost, "/api/v1/resources/"+deletable.ID+":delete", "contributor", "delete-draft-invalid-body", `"rev-1"`, map[string]any{"reason": "Reject unknown fields", "unknown": true})
	requireControlProblem(t, invalidDeleteBody, http.StatusBadRequest, catalog.CodeInvalidArgument)
	deleteUnowned := requestJSON(t, testServer.URL, http.MethodPost, "/api/v1/resources/"+deletable.ID+":delete", "reviewer", "delete-unowned-resource", `"rev-1"`, map[string]any{"reason": "Not the creator"})
	requireControlProblem(t, deleteUnowned, http.StatusForbidden, catalog.CodeForbidden)
	deleteDraft := requestJSON(t, testServer.URL, http.MethodPost, "/api/v1/resources/"+deletable.ID+":delete", "contributor", "delete-draft-resource", `"rev-1"`, map[string]any{"reason": "Discard unused draft"})
	requireStatus(t, deleteDraft, http.StatusNoContent)
	deleteReplay := requestJSON(t, testServer.URL, http.MethodPost, "/api/v1/resources/"+deletable.ID+":delete", "contributor", "delete-draft-resource", `"rev-1"`, map[string]any{"reason": "Discard unused draft"})
	requireStatus(t, deleteReplay, http.StatusNoContent)
	if deleteReplay.header.Get("Idempotency-Replayed") != "true" {
		t.Fatalf("resource deletion replay headers = %#v", deleteReplay.header)
	}
	deletedGet := requestJSON(t, testServer.URL, http.MethodGet, "/api/v1/resources/"+deletable.ID, "contributor", "", "", nil)
	requireControlProblem(t, deletedGet, http.StatusNotFound, catalog.CodeNotFound)

	publisherResult := requestJSON(t, testServer.URL, http.MethodPost, "/api/v1/publishers", "admin", "publisher-one", "", map[string]any{"displayName": "Example", "domains": []string{"catalog.example.com"}})
	requireStatus(t, publisherResult, http.StatusCreated)
	requireControlSchema(t, "Publisher", publisherResult.body)
	var publisher catalog.Publisher
	decodeResult(t, publisherResult, &publisher)
	if publisher.ApprovedPublicationDomains == nil || len(publisher.ApprovedPublicationDomains) != 0 || containsJSONKey(publisherResult.body, "verifiedDomains") {
		t.Fatalf("publisher compatibility projection = %#v, body=%s", publisher, publisherResult.body)
	}
	publisherGet := requestJSON(t, testServer.URL, http.MethodGet, "/api/v1/publishers/"+publisher.ID, "admin", "", "", nil)
	requireStatus(t, publisherGet, http.StatusOK)
	requireControlSchema(t, "Publisher", publisherGet.body)
	publisherList := requestJSON(t, testServer.URL, http.MethodGet, "/api/v1/publishers?limit=1", "admin", "", "", nil)
	requireStatus(t, publisherList, http.StatusOK)
	requireControlSchema(t, "PublisherPage", publisherList.body)
	missingPublisherPatchMatch := requestJSON(t, testServer.URL, http.MethodPatch, "/api/v1/publishers/"+publisher.ID, "admin", "publisher-patch-missing-match", "", map[string]any{"displayName": "Missing revision", "reason": "Missing precondition"})
	requireControlProblem(t, missingPublisherPatchMatch, http.StatusPreconditionFailed, catalog.CodePreconditionFailed)
	invalidPublisherPatch := requestJSON(t, testServer.URL, http.MethodPatch, "/api/v1/publishers/"+publisher.ID, "admin", "publisher-patch-invalid-body", `"rev-1"`, map[string]any{"displayName": "Invalid body", "reason": "Reject unknown fields", "unknown": true})
	requireControlProblem(t, invalidPublisherPatch, http.StatusBadRequest, catalog.CodeInvalidArgument)
	missingApprovalKey := requestJSON(t, testServer.URL, http.MethodPost, "/api/v1/publishers/"+publisher.ID+"/publication-domains/catalog.example.com:approve", "admin", "", `"rev-1"`, map[string]any{"reason": "missing idempotency key"})
	requireStatus(t, missingApprovalKey, http.StatusBadRequest)
	missingApprovalMatch := requestJSON(t, testServer.URL, http.MethodPost, "/api/v1/publishers/"+publisher.ID+"/publication-domains/catalog.example.com:approve", "admin", "publisher-publication-domain-missing-match", "", map[string]any{"reason": "missing precondition"})
	requireStatus(t, missingApprovalMatch, http.StatusPreconditionFailed)
	forbiddenApproval := requestJSON(t, testServer.URL, http.MethodPost, "/api/v1/publishers/"+publisher.ID+"/publication-domains/catalog.example.com:approve", "contributor", "publisher-publication-domain-forbidden", `"rev-1"`, map[string]any{"reason": "not authorized"})
	requireStatus(t, forbiddenApproval, http.StatusForbidden)
	crossTenantApproval := requestJSON(t, testServer.URL, http.MethodPost, "/api/v1/publishers/"+publisher.ID+"/publication-domains/catalog.example.com:approve", "other-admin", "publisher-publication-domain-cross-tenant", `"rev-1"`, map[string]any{"reason": "not visible"})
	requireStatus(t, crossTenantApproval, http.StatusNotFound)
	staleApproval := requestJSON(t, testServer.URL, http.MethodPost, "/api/v1/publishers/"+publisher.ID+"/publication-domains/catalog.example.com:approve", "admin", "publisher-publication-domain-stale", `"rev-99"`, map[string]any{"reason": "stale revision"})
	requireStatus(t, staleApproval, http.StatusPreconditionFailed)
	approve := requestJSON(t, testServer.URL, http.MethodPost, "/api/v1/publishers/"+publisher.ID+"/publication-domains/catalog.example.com:approve", "admin", "publisher-publication-domain-approve", `"rev-1"`, map[string]any{"reason": "approve public publication"})
	requireStatus(t, approve, http.StatusOK)
	var approvedPublisher catalog.Publisher
	decodeResult(t, approve, &approvedPublisher)
	if approvedPublisher.Revision != 2 || len(approvedPublisher.ApprovedPublicationDomains) != 1 || approvedPublisher.ApprovedPublicationDomains[0] != "catalog.example.com" || containsJSONKey(approve.body, "verifiedDomains") {
		t.Fatalf("approved publisher = %#v, body=%s", approvedPublisher, approve.body)
	}
	approvalReplay := requestJSON(t, testServer.URL, http.MethodPost, "/api/v1/publishers/"+publisher.ID+"/publication-domains/catalog.example.com:approve", "admin", "publisher-publication-domain-approve", `"rev-1"`, map[string]any{"reason": "approve public publication"})
	requireStatus(t, approvalReplay, http.StatusOK)
	if approvalReplay.header.Get("Idempotency-Replayed") != "true" || string(approvalReplay.body) != string(approve.body) {
		t.Fatalf("publication-domain approval was not replayed exactly: headers=%#v body=%s", approvalReplay.header, approvalReplay.body)
	}

	resourceResult := requestJSON(t, testServer.URL, http.MethodPost, "/api/v1/resources", "contributor", "resource-one", "", map[string]any{"namespaceId": namespace.ID, "canonicalIdentifier": "urn:air:catalog.example.com:default:http-agent", "kind": "agent"})
	requireStatus(t, resourceResult, http.StatusCreated)
	requireControlSchema(t, "Resource", resourceResult.body)
	var resource catalog.Resource
	decodeResult(t, resourceResult, &resource)
	resourceGet := requestJSON(t, testServer.URL, http.MethodGet, "/api/v1/resources/"+resource.ID, "contributor", "", "", nil)
	requireStatus(t, resourceGet, http.StatusOK)
	requireControlSchema(t, "Resource", resourceGet.body)
	resourceList := requestJSON(t, testServer.URL, http.MethodGet, "/api/v1/resources?kind=agent&limit=1", "contributor", "", "", nil)
	requireStatus(t, resourceList, http.StatusOK)
	requireControlSchema(t, "ResourcePage", resourceList.body)
	otherRead := requestJSON(t, testServer.URL, http.MethodGet, "/api/v1/resources/"+resource.ID, "other", "", "", nil)
	requireStatus(t, otherRead, http.StatusNotFound)

	versionResult := requestJSON(t, testServer.URL, http.MethodPost, "/api/v1/resources/"+resource.ID+"/versions", "contributor", "version-one", "", map[string]any{"version": "1.0.0", "representations": []map[string]any{{"role": "primary", "mediaType": adapter.MediaA2AAgentCard, "data": map[string]any{"name": "HTTP Agent", "description": "Exercises the complete HTTP contract", "version": "1.0.0", "supportedInterfaces": []map[string]any{{"url": "https://agents.example.com/http", "protocolBinding": "JSONRPC", "protocolVersion": "1.0"}}, "capabilities": map[string]any{}, "defaultInputModes": []string{"application/json"}, "defaultOutputModes": []string{"application/json"}, "skills": []map[string]any{{"id": "exercise-http", "name": "Exercise HTTP", "description": "Exercises the HTTP contract", "tags": []string{"http"}}}}}}})
	requireStatus(t, versionResult, http.StatusCreated)
	requireControlSchema(t, "VersionEvidenceEnvelope", versionResult.body)
	var versionEnvelope struct {
		Version  catalog.ResourceVersion `json:"version"`
		Revision catalog.VersionRevision `json:"revision"`
		Evidence catalog.EvidenceSet     `json:"evidence"`
	}
	decodeResult(t, versionResult, &versionEnvelope)
	if versionEnvelope.Evidence.ValidationProfile != "catalog-validation-v7" || versionEnvelope.Evidence.LegalRights.State != catalog.LegalRightsNotAssessed || versionEnvelope.Evidence.Trust.State != catalog.TrustNotAssessed || bytes.Contains(versionResult.body, []byte(`"rights"`)) {
		t.Fatalf("version evidence projection = %#v, body=%s", versionEnvelope.Evidence, versionResult.body)
	}
	versionRevisionBody := map[string]any{"representations": []map[string]any{{"role": "primary", "mediaType": adapter.MediaA2AAgentCard, "data": map[string]any{"name": "HTTP Agent", "description": "Exercises revision preconditions", "version": "1.0.0", "supportedInterfaces": []map[string]any{{"url": "https://agents.example.com/http", "protocolBinding": "JSONRPC", "protocolVersion": "1.0"}}, "capabilities": map[string]any{}, "defaultInputModes": []string{"application/json"}, "defaultOutputModes": []string{"application/json"}, "skills": []map[string]any{{"id": "exercise-http", "name": "Exercise HTTP", "description": "Exercises the HTTP contract", "tags": []string{"http"}}}}}}}
	missingVersionPrecondition := requestJSON(t, testServer.URL, http.MethodPost, "/api/v1/resource-versions/"+versionEnvelope.Version.ID+"/revisions", "contributor", "version-revision-missing-precondition", "", versionRevisionBody)
	requireStatus(t, missingVersionPrecondition, http.StatusPreconditionFailed)
	staleVersionPrecondition := requestJSON(t, testServer.URL, http.MethodPost, "/api/v1/resource-versions/"+versionEnvelope.Version.ID+"/revisions", "contributor", "version-revision-stale-precondition", `"rev-99"`, versionRevisionBody)
	requireStatus(t, staleVersionPrecondition, http.StatusPreconditionFailed)
	versionRevisionResult := requestJSON(t, testServer.URL, http.MethodPost, "/api/v1/resource-versions/"+versionEnvelope.Version.ID+"/revisions", "contributor", "version-revision-one", versionResult.header.Get("ETag"), versionRevisionBody)
	requireStatus(t, versionRevisionResult, http.StatusCreated)
	requireControlSchema(t, "VersionEvidenceEnvelope", versionRevisionResult.body)
	decodeResult(t, versionRevisionResult, &versionEnvelope)
	versionGet := requestJSON(t, testServer.URL, http.MethodGet, "/api/v1/resource-versions/"+versionEnvelope.Version.ID, "contributor", "", "", nil)
	requireStatus(t, versionGet, http.StatusOK)
	requireControlSchema(t, "ResourceVersion", versionGet.body)
	versionRevisionGet := requestJSON(t, testServer.URL, http.MethodGet, "/api/v1/version-revisions/"+versionEnvelope.Revision.ID, "contributor", "", "", nil)
	requireStatus(t, versionRevisionGet, http.StatusOK)
	requireControlSchema(t, "RevisionEvidenceEnvelope", versionRevisionGet.body)
	var retainedRevision revisionEvidenceResponse
	decodeResult(t, versionRevisionGet, &retainedRevision)
	if retainedRevision.NamespaceID != resource.NamespaceID {
		t.Fatalf("revision namespace differs from its canonical resource: %q", retainedRevision.NamespaceID)
	}
	requireControlProblem(t, requestJSON(t, testServer.URL, http.MethodGet, "/api/v1/version-revisions/"+versionEnvelope.Revision.ID, "other", "", "", nil), http.StatusNotFound, catalog.CodeNotFound)

	validationPath := "/api/v1/version-revisions/" + versionEnvelope.Revision.ID + ":validate"
	requireControlProblem(t, requestJSON(t, testServer.URL, http.MethodPost, validationPath, "contributor", "", "", map[string]any{}), http.StatusBadRequest, catalog.CodeInvalidArgument)
	requireControlProblem(t, requestJSON(t, testServer.URL, http.MethodPost, validationPath, "reviewer", "version-validation-reader", "", map[string]any{}), http.StatusForbidden, catalog.CodeForbidden)
	requireControlProblem(t, requestJSON(t, testServer.URL, http.MethodPost, validationPath, "other", "version-validation-other-tenant", "", map[string]any{}), http.StatusNotFound, catalog.CodeNotFound)
	requireControlProblem(t, requestJSON(t, testServer.URL, http.MethodPost, validationPath, "contributor", "version-validation-unknown-field", "", map[string]any{"unexpected": true}), http.StatusBadRequest, catalog.CodeInvalidArgument)
	requireControlProblem(t, requestJSON(t, testServer.URL, http.MethodPost, validationPath, "contributor", "version-validation-namespace-input", "", map[string]any{"namespaceId": resource.NamespaceID}), http.StatusBadRequest, catalog.CodeInvalidArgument)

	requireControlProblem(t, requestJSON(t, testServer.URL, http.MethodPost, "/api/v1/version-revisions/"+versionEnvelope.Revision.ID+":unknown", "contributor", "version-validation-unknown-command", "", map[string]any{}), http.StatusNotFound, catalog.CodeNotFound)
	priorEvidenceID := versionEnvelope.Evidence.ID
	priorContentDigest := versionEnvelope.Revision.ContentSetDigest
	validationResult := requestJSON(t, testServer.URL, http.MethodPost, validationPath, "contributor", "version-validation-one", "", map[string]any{})
	requireStatus(t, validationResult, http.StatusCreated)
	requireControlSchema(t, "RevisionEvidenceEnvelope", validationResult.body)
	var validationEnvelope struct {
		NamespaceID string                  `json:"namespaceId"`
		Revision    catalog.VersionRevision `json:"revision"`
		Evidence    catalog.EvidenceSet     `json:"evidence"`
	}
	decodeResult(t, validationResult, &validationEnvelope)
	if validationEnvelope.NamespaceID != retainedRevision.NamespaceID {
		t.Fatalf("validation changed the canonical namespace: %q", validationEnvelope.NamespaceID)
	}

	if validationResult.header.Get("Location") != "/api/v1/version-revisions/"+versionEnvelope.Revision.ID || validationEnvelope.Revision.ID != versionEnvelope.Revision.ID || validationEnvelope.Revision.ContentSetDigest != priorContentDigest || validationEnvelope.Revision.EvidenceSetID != validationEnvelope.Evidence.ID || validationEnvelope.Evidence.ID == priorEvidenceID || validationEnvelope.Evidence.ValidationProfile != "catalog-validation-v7" || !validationEnvelope.Evidence.ExpiresAt.IsZero() || bytes.Contains(validationResult.body, []byte(`"expiresAt"`)) {
		t.Fatalf("version validation response = headers %#v revision %#v evidence %#v body=%s", validationResult.header, validationEnvelope.Revision, validationEnvelope.Evidence, validationResult.body)
	}
	validationReplay := requestJSON(t, testServer.URL, http.MethodPost, validationPath, "contributor", "version-validation-one", "", map[string]any{})
	requireStatus(t, validationReplay, http.StatusCreated)
	if validationReplay.header.Get("Idempotency-Replayed") != "true" || !bytes.Equal(validationReplay.body, validationResult.body) {
		t.Fatalf("version validation replay = headers %#v body=%s", validationReplay.header, validationReplay.body)
	}
	currentRevisionGet := requestJSON(t, testServer.URL, http.MethodGet, "/api/v1/version-revisions/"+versionEnvelope.Revision.ID, "contributor", "", "", nil)
	requireStatus(t, currentRevisionGet, http.StatusOK)
	decodeResult(t, currentRevisionGet, &validationEnvelope)
	if validationEnvelope.Evidence.ID == priorEvidenceID || validationEnvelope.Revision.EvidenceSetID != validationEnvelope.Evidence.ID {
		t.Fatalf("version revision did not project current validation evidence: %s", currentRevisionGet.body)
	}
	versionEnvelope.Revision = validationEnvelope.Revision
	versionEnvelope.Evidence = validationEnvelope.Evidence
	externalResourceResult := requestJSON(t, testServer.URL, http.MethodPost, "/api/v1/resources", "contributor", "resource-external", "", map[string]any{"namespaceId": namespace.ID, "canonicalIdentifier": "urn:air:catalog.example.com:default:external-http-agent", "kind": "agent"})
	requireStatus(t, externalResourceResult, http.StatusCreated)
	var externalResource catalog.Resource
	decodeResult(t, externalResourceResult, &externalResource)
	externalURL := "https://content.example.com/agent-card.json"
	externalVersionResult := requestJSON(t, testServer.URL, http.MethodPost, "/api/v1/resources/"+externalResource.ID+"/versions", "contributor", "version-external", "", map[string]any{"version": "2.0.0", "representations": []map[string]any{{"role": "primary", "mediaType": adapter.MediaA2AAgentCard, "url": externalURL}}})
	requireStatus(t, externalVersionResult, http.StatusCreated)
	requireControlSchema(t, "VersionEvidenceEnvelope", externalVersionResult.body)
	var externalEnvelope struct {
		Version  catalog.ResourceVersion `json:"version"`
		Revision catalog.VersionRevision `json:"revision"`
		Evidence catalog.EvidenceSet     `json:"evidence"`
	}
	decodeResult(t, externalVersionResult, &externalEnvelope)
	if externalEnvelope.Evidence.State != catalog.EvidenceComplete || len(externalEnvelope.Evidence.ExternalRetrievals) != 1 || externalEnvelope.Evidence.ExpiresAt.IsZero() || externalEnvelope.Revision.Representations[0].URL != externalURL || externalEnvelope.Revision.Representations[0].ArtifactID == "" || externalFetcher.calls != 1 {
		t.Fatalf("external version response = %#v / %#v, calls=%d body=%s", externalEnvelope.Revision, externalEnvelope.Evidence, externalFetcher.calls, externalVersionResult.body)
	}
	externalReplay := requestJSON(t, testServer.URL, http.MethodPost, "/api/v1/resources/"+externalResource.ID+"/versions", "contributor", "version-external", "", map[string]any{"version": "2.0.0", "representations": []map[string]any{{"role": "primary", "mediaType": adapter.MediaA2AAgentCard, "url": externalURL}}})
	requireStatus(t, externalReplay, http.StatusCreated)
	if externalReplay.header.Get("Idempotency-Replayed") != "true" || externalFetcher.calls != 1 || !bytes.Equal(externalReplay.body, externalVersionResult.body) {
		t.Fatalf("external version replay = calls %d headers %#v body=%s", externalFetcher.calls, externalReplay.header, externalReplay.body)
	}
	externalValidationPath := "/api/v1/version-revisions/" + externalEnvelope.Revision.ID + ":validate"
	externalValidationResult := requestJSON(t, testServer.URL, http.MethodPost, externalValidationPath, "contributor", "version-validation-external", "", map[string]any{})
	requireStatus(t, externalValidationResult, http.StatusCreated)
	requireControlSchema(t, "RevisionEvidenceEnvelope", externalValidationResult.body)
	var externalValidationEnvelope struct {
		Revision catalog.VersionRevision `json:"revision"`
		Evidence catalog.EvidenceSet     `json:"evidence"`
	}
	decodeResult(t, externalValidationResult, &externalValidationEnvelope)
	if externalValidationEnvelope.Revision.ContentSetDigest != externalEnvelope.Revision.ContentSetDigest || externalValidationEnvelope.Evidence.ID == externalEnvelope.Evidence.ID || len(externalValidationEnvelope.Evidence.ExternalRetrievals) != 1 || externalValidationEnvelope.Evidence.ExpiresAt.IsZero() || externalFetcher.calls != 2 {
		t.Fatalf("external validation response = %#v / %#v, calls=%d body=%s", externalValidationEnvelope.Revision, externalValidationEnvelope.Evidence, externalFetcher.calls, externalValidationResult.body)
	}
	externalValidationReplay := requestJSON(t, testServer.URL, http.MethodPost, externalValidationPath, "contributor", "version-validation-external", "", map[string]any{})
	requireStatus(t, externalValidationReplay, http.StatusCreated)
	if externalValidationReplay.header.Get("Idempotency-Replayed") != "true" || externalFetcher.calls != 2 || !bytes.Equal(externalValidationReplay.body, externalValidationResult.body) {
		t.Fatalf("external validation replay = calls %d headers %#v body=%s", externalFetcher.calls, externalValidationReplay.header, externalValidationReplay.body)
	}
	listingResult := requestJSON(t, testServer.URL, http.MethodPost, "/api/v1/catalog-listings", "contributor", "listing-one", "", map[string]any{"namespaceId": namespace.ID, "resourceId": resource.ID})
	requireStatus(t, listingResult, http.StatusCreated)
	requireControlSchema(t, "CatalogListing", listingResult.body)
	var listing catalog.CatalogListing
	decodeResult(t, listingResult, &listing)
	listingGet := requestJSON(t, testServer.URL, http.MethodGet, "/api/v1/catalog-listings/"+listing.ID, "contributor", "", "", nil)
	requireStatus(t, listingGet, http.StatusOK)
	requireControlSchema(t, "CatalogListing", listingGet.body)
	listingRevisionBody := map[string]any{"extensions": map[string]any{artwork.IRI: artworkHTTPInput()}, "versionRevisionId": versionEnvelope.Revision.ID, "displayName": "HTTP Agent", "description": "A complete HTTP lifecycle fixture.", "representativeQueries": []string{"Find the HTTP agent", "Show catalog HTTP resources"}, "publisherId": publisher.ID, "visibility": "public", "license": "Apache-2.0", "categories": []string{"developer tools"}}
	// UTF-8 encoding width must not shorten the OpenAPI character limit.
	unicodeTags := []string{strings.Repeat("\u00e9", 80), strings.Repeat("\U0001f642", 80)}
	listingRevisionBody["tags"] = []string{strings.Repeat("\u00e9", 81)}
	invalidUnicodeListing := requestJSON(t, testServer.URL, http.MethodPost, "/api/v1/catalog-listings/"+listing.ID+"/revisions", "contributor", "listing-unicode-over-limit", listingResult.header.Get("ETag"), listingRevisionBody)
	requireControlProblem(t, invalidUnicodeListing, http.StatusBadRequest, catalog.CodeInvalidArgument)
	listingRevisionBody["tags"] = unicodeTags

	for i, queries := range [][]string{{"🙂🙂"}, {strings.Repeat("🙂", 241)}, {"one", "two", "three", "four", "five", "six"}} {
		listingRevisionBody["visibility"] = "private"
		listingRevisionBody["representativeQueries"] = queries
		rejected := requestJSON(t, testServer.URL, http.MethodPost, "/api/v1/catalog-listings/"+listing.ID+"/revisions", "contributor", fmt.Sprintf("listing-invalid-query-%d", i), listingResult.header.Get("ETag"), listingRevisionBody)
		requireStatus(t, rejected, http.StatusBadRequest)
	}
	listingRevisionBody["visibility"] = "public"
	listingRevisionBody["representativeQueries"] = []string{strings.Repeat("🙂", 240), "🙂🙂🙂"}
	missingListingPrecondition := requestJSON(t, testServer.URL, http.MethodPost, "/api/v1/catalog-listings/"+listing.ID+"/revisions", "contributor", "listing-revision-missing-precondition", "", listingRevisionBody)
	requireStatus(t, missingListingPrecondition, http.StatusPreconditionFailed)
	staleListingPrecondition := requestJSON(t, testServer.URL, http.MethodPost, "/api/v1/catalog-listings/"+listing.ID+"/revisions", "contributor", "listing-revision-stale-precondition", `"rev-99"`, listingRevisionBody)
	requireStatus(t, staleListingPrecondition, http.StatusPreconditionFailed)
	listingRevisionResult := requestJSON(t, testServer.URL, http.MethodPost, "/api/v1/catalog-listings/"+listing.ID+"/revisions", "contributor", "listing-revision-one", listingResult.header.Get("ETag"), listingRevisionBody)
	requireStatus(t, listingRevisionResult, http.StatusCreated)
	requireControlSchema(t, "ListingRevisionCreated", listingRevisionResult.body)
	var listingRevisionEnvelope struct {
		Listing  catalog.CatalogListing  `json:"listing"`
		Revision catalog.ListingRevision `json:"revision"`
	}
	decodeResult(t, listingRevisionResult, &listingRevisionEnvelope)
	if !slices.Equal(listingRevisionEnvelope.Revision.RepresentativeQueries, []string{strings.Repeat("🙂", 240), "🙂🙂🙂"}) {
		t.Fatalf("Unicode queries were lost: %q", listingRevisionEnvelope.Revision.RepresentativeQueries)
	}

	beforeReplayImages := imageFetcher.calls
	listingReplay := requestJSON(t, testServer.URL, http.MethodPost, "/api/v1/catalog-listings/"+listing.ID+"/revisions", "contributor", "listing-revision-one", listingResult.header.Get("ETag"), listingRevisionBody)
	requireStatus(t, listingReplay, http.StatusCreated)
	if listingReplay.header.Get("Idempotency-Replayed") != "true" || imageFetcher.calls != beforeReplayImages || !bytes.Equal(listingReplay.body, listingRevisionResult.body) {
		t.Fatal("listing artwork replay repeated retrieval or changed facts")
	}
	imageRead := requestJSON(t, testServer.URL, http.MethodPost, "/api/v1/artwork/read", "contributor", "", "", map[string]any{"origin": "local", "listingRevisionId": listingRevisionEnvelope.Revision.ID, "slot": "icon"})
	requireStatus(t, imageRead, http.StatusOK)
	if !bytes.Equal(imageRead.body, imageFetcher.body) || imageRead.header.Get("Content-Digest") != digestHeader(imageFetcher.body) {
		t.Fatal("listing preview image lost validated bytes")
	}
	listingRevisionGet := requestJSON(t, testServer.URL, http.MethodGet, "/api/v1/listing-revisions/"+listingRevisionEnvelope.Revision.ID, "contributor", "", "", nil)
	requireStatus(t, listingRevisionGet, http.StatusOK)
	requireControlSchema(t, "ListingRevision", listingRevisionGet.body)
	submissionResult := requestJSON(t, testServer.URL, http.MethodPost, "/api/v1/catalog-listings/"+listing.ID+"/submissions", "contributor", "submission-one", "", map[string]any{})
	requireStatus(t, submissionResult, http.StatusCreated)
	requireControlSchema(t, "ReviewSubmission", submissionResult.body)
	var submission catalog.ReviewSubmission
	decodeResult(t, submissionResult, &submission)
	submissionGet := requestJSON(t, testServer.URL, http.MethodGet, "/api/v1/review-submissions/"+submission.ID, "contributor", "", "", nil)
	requireStatus(t, submissionGet, http.StatusOK)
	requireControlSchema(t, "ReviewSubmission", submissionGet.body)
	selfApproval := requestJSON(t, testServer.URL, http.MethodPost, "/api/v1/review-submissions/"+submission.ID+":approve", "contributor", "self-approval", `"rev-1"`, map[string]any{"reason": "self"})
	requireStatus(t, selfApproval, http.StatusForbidden)
	blankApproval := requestJSON(t, testServer.URL, http.MethodPost, "/api/v1/review-submissions/"+submission.ID+":approve", "reviewer", "approval-blank-reason", `"rev-1"`, map[string]any{"reason": "  "})
	requireStatus(t, blankApproval, http.StatusBadRequest)
	controlApproval := requestJSON(t, testServer.URL, http.MethodPost, "/api/v1/review-submissions/"+submission.ID+":approve", "reviewer", "approval-control-reason", `"rev-1"`, map[string]any{"reason": "line\nbreak"})
	requireStatus(t, controlApproval, http.StatusBadRequest)
	approval := requestJSON(t, testServer.URL, http.MethodPost, "/api/v1/review-submissions/"+submission.ID+":approve", "reviewer", "approval-one", `"rev-1"`, map[string]any{"reason": "independent review"})
	requireStatus(t, approval, http.StatusOK)
	requireControlSchema(t, "ReviewDecision", approval.body)
	publishResult := requestJSON(t, testServer.URL, http.MethodPost, "/api/v1/catalog-listings/"+listing.ID+":publish", "publisher", "publish-one", `"rev-`+jsonNumber(listingRevisionEnvelope.Listing.Revision)+`"`, map[string]any{"submissionId": submission.ID})
	requireStatus(t, publishResult, http.StatusCreated)
	requireControlSchema(t, "ListingPublished", publishResult.body)
	var publishEnvelope struct {
		Release catalog.ListingRelease `json:"release"`
	}
	decodeResult(t, publishResult, &publishEnvelope)
	t.Run("bounded authoring histories and exact submitted evidence", func(t *testing.T) {
		histories := map[string]string{
			"/api/v1/resources/" + resource.ID + "/versions":                         versionEnvelope.Version.ID,
			"/api/v1/resource-versions/" + versionEnvelope.Version.ID + "/revisions": versionEnvelope.Revision.ID,
			"/api/v1/resources/" + resource.ID + "/listings":                         listing.ID,
			"/api/v1/catalog-listings/" + listing.ID + "/revisions":                  listingRevisionEnvelope.Revision.ID,
			"/api/v1/catalog-listings/" + listing.ID + "/releases":                   publishEnvelope.Release.ID,
			"/api/v1/review-submissions?listingId=" + listing.ID + "&state=approved": submission.ID,
		}
		for path, wantID := range histories {
			response := requestJSON(t, testServer.URL, http.MethodGet, path, "admin", "", "", nil)
			requireStatus(t, response, http.StatusOK)
			var page struct {
				Items []struct {
					ID string `json:"id"`
				} `json:"items"`
			}
			decodeResult(t, response, &page)
			found := false
			for _, item := range page.Items {
				found = found || item.ID == wantID
			}
			if !found {
				t.Fatalf("%s did not enumerate its child", path)
			}
			separator := "?"
			if strings.Contains(path, "?") {
				separator = "&"
			}
			requireStatus(t, requestJSON(t, testServer.URL, http.MethodGet, path+separator+"limit=101", "admin", "", "", nil), http.StatusBadRequest)
			requireStatus(t, requestJSON(t, testServer.URL, http.MethodGet, path+separator+"cursor=invalid", "admin", "", "", nil), http.StatusBadRequest)
			requireStatus(t, requestJSON(t, testServer.URL, http.MethodGet, path, "", "", "", nil), http.StatusUnauthorized)
		}
		evidenceReply := requestJSON(t, testServer.URL, http.MethodGet, "/api/v1/review-submissions/"+submission.ID+"/evidence", "admin", "", "", nil)
		requireStatus(t, evidenceReply, http.StatusOK)
		requireControlSchema(t, "EvidenceSet", evidenceReply.body)
		var evidence catalog.EvidenceSet
		decodeResult(t, evidenceReply, &evidence)
		if evidence.ID != submission.EvidenceSetID || evidence.Digest != submission.EvidenceDigest || evidence.PolicyVersion != submission.PolicyVersion {
			t.Fatal("returned unbound review evidence")
		}
	})
	releaseGet := requestJSON(t, testServer.URL, http.MethodGet, "/api/v1/listing-releases/"+publishEnvelope.Release.ID, "publisher", "", "", nil)
	requireStatus(t, releaseGet, http.StatusOK)
	requireControlSchema(t, "ListingRelease", releaseGet.body)
	catalogRecord := requestJSON(t, testServer.URL, http.MethodGet, "/api/v1/catalog/resources/"+publishEnvelope.Release.ID, "admin", "", "", nil)
	requireStatus(t, catalogRecord, http.StatusOK)
	requireControlSchema(t, "CatalogRecord", catalogRecord.body)
	catalogBrowse := requestJSON(t, testServer.URL, http.MethodGet, "/api/v1/catalog/resources?limit=10", "admin", "", "", nil)
	requireStatus(t, catalogBrowse, http.StatusOK)
	requireControlSchema(t, "CatalogRecordPage", catalogBrowse.body)
	catalogSearch := requestJSON(t, testServer.URL, http.MethodPost, "/api/v1/catalog/search", "admin", "", "", map[string]any{"text": "HTTP agent", "tags": unicodeTags})
	requireStatus(t, catalogSearch, http.StatusOK)
	requireControlSchema(t, "CatalogSearchResponse", catalogSearch.body)
	var catalogSearchResponse catalog.CatalogSearchResponse
	decodeResult(t, catalogSearch, &catalogSearchResponse)
	if len(catalogSearchResponse.Items) != 1 || catalogSearchResponse.Items[0].Type != catalog.SearchResultLocal || catalogSearchResponse.Items[0].Local == nil || catalogSearchResponse.Items[0].Local.Release.ID != publishEnvelope.Release.ID || catalogSearchResponse.DurableTotal != 1 || catalogSearchResponse.LiveCount != 0 || catalogSearchResponse.Profile != catalog.CoreSearchProfile || catalogSearchResponse.Tier != "core" || catalogSearchResponse.Generation == "" || catalogSearchResponse.Partial {
		t.Fatalf("unexpected unified Catalog Search response: %#v", catalogSearchResponse)
	}
	if strings.Join(catalogSearchResponse.Items[0].Tags, "|") != strings.Join(unicodeTags, "|") {
		t.Fatal("HTTP listing/search round trip changed Unicode tags")
	}
	t.Run("origin and listing namespace selectors", func(t *testing.T) {
		for mask := 1; mask < 8; mask++ {
			origins := []string{}
			for i, value := range []string{"local", "indexed", "live"} {
				if mask&(1<<i) != 0 {
					origins = append(origins, value)
				}
			}
			reply := requestJSON(t, testServer.URL, http.MethodPost, "/api/v1/catalog/search", "admin", "", "", map[string]any{"text": "HTTP agent", "origins": origins})
			requireStatus(t, reply, http.StatusOK)
			requireControlSchema(t, "CatalogSearchResponse", reply.body)
			var page catalog.CatalogSearchResponse
			decodeResult(t, reply, &page)
			want := 0
			if mask&1 != 0 {
				want = 1
			}
			if len(page.Items) != want || page.DurableTotal != want || page.NextCursor != "" {
				t.Fatalf("origin subset %d: %+v", mask, page)
			}
		}
		namespaceID := catalogSearchResponse.Items[0].Local.Listing.NamespaceID
		for _, id := range []string{namespaceID, "00000000-0000-7000-8000-000000000099"} {
			response := requestJSON(t, testServer.URL, http.MethodPost, "/api/v1/catalog/search", "admin", "", "", map[string]any{"namespaceId": id})
			requireStatus(t, response, http.StatusOK)
			var page catalog.CatalogSearchResponse
			decodeResult(t, response, &page)
			want := 0
			if id == namespaceID {
				want = 1
			}
			if page.DurableTotal != want {
				t.Fatalf("namespace total=%d", page.DurableTotal)
			}
			browse := requestJSON(t, testServer.URL, http.MethodGet, "/api/v1/catalog/resources?namespaceId="+id, "admin", "", "", nil)
			requireStatus(t, browse, http.StatusOK)
			requireControlSchema(t, "CatalogRecordPage", browse.body)
			explore := requestJSON(t, testServer.URL, http.MethodPost, "/api/v1/catalog/explore", "admin", "", "", map[string]any{"query": map[string]any{"namespaceId": id}, "facets": []any{map[string]any{"field": "namespace"}}})
			requireStatus(t, explore, http.StatusOK)
			var facets catalog.ExploreResponse
			decodeResult(t, explore, &facets)
			if facets.Matched != want {
				t.Fatal("facet total differs")
			}
		}
		for _, body := range []map[string]any{{"origins": []string{}}, {"origins": []string{"local", "local"}}, {"origins": []string{"unknown"}}, {"origins": nil}, {"namespaceId": nil}, {"namespaceId": "foreign"}, {"namespaceId": ""}, {"namespaceId": namespaceID, "origins": []string{"indexed"}}, {"origins": []string{"live"}, "cursor": "invalid"}} {
			response := requestJSON(t, testServer.URL, http.MethodPost, "/api/v1/catalog/search", "admin", "", "", body)
			requireStatus(t, response, http.StatusBadRequest)
			requireControlSchema(t, "Problem", response.body)
		}
		requireStatus(t, requestJSON(t, testServer.URL, http.MethodGet, "/api/v1/catalog/resources?namespaceId=", "admin", "", "", nil), http.StatusBadRequest)
	})
	t.Run("release linked labels do not grant publisher directory scope", func(t *testing.T) {
		// Reuse the governed release and membership; restrict only OAuth
		// admission, without changing ordinary directory role contracts.
		scoped := discoveryAdmissionAuthenticator(func(ctx context.Context, token string) (identity.Actor, error) {
			actor, err := authenticator.Authenticate(ctx, token)
			actor.Claims = map[string]string{"scope:catalog.read": "true"}
			return actor, err
		})
		readerServer, err := New(Dependencies{PersonalStore: personal.NewMemoryStore(repository), Catalog: catalogService, Identity: identityService, Federation: federationManager, Jobs: jobService, Authenticator: scoped, Idempotency: idempotency.NewMemoryStore(), Logger: slog.New(slog.DiscardHandler), MaxBodyBytes: 1 << 20, RequestTimeout: time.Second, CursorKey: cursorKey[:], Now: serviceClock.Now, EnforceOAuthScopes: true})
		if err != nil {
			t.Fatal(err)
		}
		owned := httptest.NewServer(readerServer.Handler())
		defer owned.Close()
		for _, path := range []string{"/api/v1/publishers", "/api/v1/publishers/" + publisher.ID} {
			denied := requestJSON(t, owned.URL, http.MethodGet, path, "contributor", "", "", nil)
			requireControlProblem(t, denied, http.StatusForbidden, catalog.CodeForbidden)
			if !strings.Contains(denied.header.Get("WWW-Authenticate"), "publishers.read") {
				t.Fatal("directory denial did not reach OAuth scope admission")
			}
		}
		for _, path := range []string{"/api/v1/catalog/resources/" + publishEnvelope.Release.ID, "/api/v1/catalog/resources?limit=10"} {
			reply := requestJSON(t, owned.URL, http.MethodGet, path, "contributor", "", "", nil)
			requireStatus(t, reply, http.StatusOK)
			if !bytes.Contains(reply.body, []byte(`"publisherName":"Example"`)) || !bytes.Contains(reply.body, []byte(`"namespaceName":"Default namespace"`)) {
				t.Fatal("authorized release labels missing without directory scope")
			}
		}
		search := requestJSON(t, owned.URL, http.MethodPost, "/api/v1/catalog/search", "contributor", "", "", map[string]any{"text": "HTTP agent"})
		requireStatus(t, search, http.StatusOK)
		requireControlSchema(t, "CatalogSearchResponse", search.body)
		if !bytes.Contains(search.body, []byte(`"publisherName":"Example"`)) {
			t.Fatal("Search dropped authorized presentation")
		}
		// Namespace reads already use catalog.read for tenant Readers.
		requireStatus(t, requestJSON(t, owned.URL, http.MethodGet, "/api/v1/namespaces/"+namespace.ID, "contributor", "", "", nil), http.StatusOK)
	})
	qualifyHTTPPersonalExactReference(t, testServer.URL, "contributor", personal.Reference{Origin: "local", ReleaseID: publishEnvelope.Release.ID, VersionRevisionID: publishEnvelope.Release.VersionRevisionID})
	t.Run("current-user rating scopes and HTTP commands", func(t *testing.T) {
		qualifyHTTPRatingScopes(t, Dependencies{RatingsStore: ratings.NewMemoryStore(repository), Catalog: catalogService, Identity: identityService, Federation: federationManager, Jobs: jobService, Authenticator: authenticator, Idempotency: idempotency.NewMemoryStore(), Logger: slog.New(slog.DiscardHandler), MaxBodyBytes: 1 << 20, RequestTimeout: config.Defaults().HTTP.RequestTimeout.Duration, CursorKey: cursorKey[:], Now: serviceClock.Now}, publishEnvelope.Release.ID)
	})
	for _, body := range []any{map[string]any{"limit": 0}, map[string]any{"kinds": []string{"agent", "agent"}}, map[string]any{"tags": []string{strings.Repeat("\u00e9", 81)}}, map[string]any{"tags": []string{"a\x00b"}}, map[string]any{"publisher": "\x00", "limit": 1}, map[string]any{"text": "\x00", "limit": 1}} {
		invalidSearch := requestJSON(t, testServer.URL, http.MethodPost, "/api/v1/catalog/search", "admin", "", "", body)
		requireControlProblem(t, invalidSearch, http.StatusBadRequest, catalog.CodeInvalidArgument)
	}
	explore := requestJSON(t, testServer.URL, http.MethodPost, "/api/v1/catalog/explore", "admin", "", "", map[string]any{"query": map[string]any{"text": "HTTP", "tags": unicodeTags}, "facets": []map[string]any{{"field": "kind"}, {"field": "tag"}, {"field": "category"}}})
	requireStatus(t, explore, http.StatusOK)
	requireControlSchema(t, "CatalogExploreResponse", explore.body)
	ignoredExplorePagination := requestJSON(t, testServer.URL, http.MethodPost, "/api/v1/catalog/explore", "admin", "", "", map[string]any{"query": map[string]any{"text": "HTTP", "limit": 1}, "facets": []map[string]any{{"field": "kind"}}})
	requireStatus(t, ignoredExplorePagination, http.StatusBadRequest)
	zeroFacetLimit := requestJSON(t, testServer.URL, http.MethodPost, "/api/v1/catalog/explore", "admin", "", "", map[string]any{"facets": []map[string]any{{"field": "kind", "limit": 0}}})
	requireStatus(t, zeroFacetLimit, http.StatusBadRequest)

	candidateResult := requestJSON(t, testServer.URL, http.MethodPost, "/api/v1/publication/snapshots", "publisher", "snapshot-one", "", map[string]any{"host": "127.0.0.1"})
	requireStatus(t, candidateResult, http.StatusCreated)
	requireControlSchema(t, "PublicationSnapshot", candidateResult.body)
	var candidate publication.Snapshot
	decodeResult(t, candidateResult, &candidate)
	activateResult := requestJSON(t, testServer.URL, http.MethodPost, "/api/v1/publication/snapshots/"+candidate.ID+":activate", "publisher", "activate-one", candidate.ETag, map[string]any{"host": "127.0.0.1", "reason": "Activate the reviewed snapshot"})
	requireStatus(t, activateResult, http.StatusOK)
	requireControlSchema(t, "PublicationSnapshot", activateResult.body)
	canonicalSnapshot := requestJSON(t, testServer.URL, http.MethodGet, "/api/v1/publication/snapshots/"+candidate.ID+"?host=127.0.0.1", "publisher", "", "", nil)
	requireStatus(t, canonicalSnapshot, http.StatusOK)
	requireControlSchema(t, "PublicationSnapshot", canonicalSnapshot.body)
	publicationList := requestJSON(t, testServer.URL, http.MethodGet, "/api/v1/publication/snapshots?host=127.0.0.1&limit=1", "publisher", "", "", nil)
	requireStatus(t, publicationList, http.StatusOK)
	requireControlSchema(t, "PublicationSnapshotPage", publicationList.body)
	for state, want := range map[string]int{"active": 1, "candidate": 0, "retired": 0} {
		filtered := requestJSON(t, testServer.URL, http.MethodGet, "/api/v1/publication/snapshots?host=127.0.0.1&limit=1&state="+state, "publisher", "", "", nil)
		requireStatus(t, filtered, http.StatusOK)
		requireControlSchema(t, "PublicationSnapshotPage", filtered.body)
		var page cursorPage[publication.SnapshotSummary]
		decodeResult(t, filtered, &page)
		if len(page.Items) != want || want == 1 && (page.Items[0].ID != candidate.ID || page.Items[0].State != publication.SnapshotActive) {
			t.Fatalf("snapshot state=%s: %+v", state, page)
		}
	}
	for _, query := range []string{"state=", "state=ACTIVE", "state=%20active", "state=removed", "state=active&state=active", "State=active"} {
		invalid := requestJSON(t, testServer.URL, http.MethodGet, "/api/v1/publication/snapshots?host=127.0.0.1&"+query, "publisher", "", "", nil)
		requireControlProblem(t, invalid, http.StatusBadRequest, catalog.CodeInvalidArgument)
	}
	serviceClock.Advance(time.Minute)
	auditList := requestJSON(t, testServer.URL, http.MethodGet, "/api/v1/audit-events?limit=10", "admin", "", "", nil)
	requireStatus(t, auditList, http.StatusOK)
	requireControlSchema(t, "AuditEventPage", auditList.body)
	var auditPage struct {
		Items []audit.Record `json:"items"`
	}
	decodeResult(t, auditList, &auditPage)
	if len(auditPage.Items) == 0 || auditPage.Items[0].FormatVersion == nil || auditPage.Items[0].RequestID == "" {
		t.Fatalf("versioned attributable audit page = %#v", auditPage.Items)
	}
	selectedAudit := auditPage.Items[0]
	auditFrom := selectedAudit.At.Add(-time.Nanosecond).UTC()
	auditBefore := serviceClock.Now().UTC()
	auditFilterPath := "/api/v1/audit-events?actor=" + url.QueryEscape(selectedAudit.Actor) +
		"&action=" + url.QueryEscape(selectedAudit.Action) +
		"&requestId=" + url.QueryEscape(selectedAudit.RequestID) +
		"&targetType=" + url.QueryEscape(selectedAudit.TargetType) +
		"&targetId=" + url.QueryEscape(selectedAudit.TargetID) +
		"&occurredFrom=" + url.QueryEscape(auditFrom.Format(time.RFC3339Nano)) +
		"&occurredBefore=" + url.QueryEscape(auditBefore.Format(time.RFC3339Nano)) + "&limit=1"
	filteredAudit := requestJSON(t, testServer.URL, http.MethodGet, auditFilterPath, "admin", "", "", nil)
	requireStatus(t, filteredAudit, http.StatusOK)
	requireControlSchema(t, "AuditEventPage", filteredAudit.body)
	var filteredAuditPage struct {
		Items []audit.Record `json:"items"`
	}
	decodeResult(t, filteredAudit, &filteredAuditPage)
	if len(filteredAuditPage.Items) != 1 || filteredAuditPage.Items[0].ID != selectedAudit.ID {
		t.Fatalf("exact audit filters returned %#v, want %s", filteredAuditPage.Items, selectedAudit.ID)
	}

	exportBody := map[string]any{
		"actor": selectedAudit.Actor, "action": selectedAudit.Action, "requestId": selectedAudit.RequestID,
		"targetType": selectedAudit.TargetType, "targetId": selectedAudit.TargetID,
		"occurredFrom": auditFrom, "occurredBefore": auditBefore, "reason": "qualify retained evidence for restore",
	}
	auditExportResult := requestJSON(t, testServer.URL, http.MethodPost, "/api/v1/audit-exports", "admin", "audit-export-one", "", exportBody)
	requireStatus(t, auditExportResult, http.StatusOK)
	requireControlSchema(t, "AuditExport", auditExportResult.body)
	if auditExportResult.header.Get("Content-Disposition") != `attachment; filename="open-agentic-catalog-audit-export.json"` {
		t.Fatalf("audit export content disposition = %q", auditExportResult.header.Get("Content-Disposition"))
	}
	verifiedExport, verification, err := audit.VerifyExport(auditExportResult.body)
	if err != nil || !verification.Valid || verifiedExport.Manifest.RowCount != 1 || verifiedExport.Manifest.First == nil || verifiedExport.Manifest.First.ID != selectedAudit.ID {
		t.Fatalf("HTTP audit export verification = %#v, %#v, %v", verifiedExport.Manifest, verification, err)
	}
	auditExportReplay := requestJSON(t, testServer.URL, http.MethodPost, "/api/v1/audit-exports", "admin", "audit-export-one", "", exportBody)
	requireStatus(t, auditExportReplay, http.StatusOK)
	if auditExportReplay.header.Get("Idempotency-Replayed") != "true" || auditExportReplay.header.Get("Content-Disposition") != auditExportResult.header.Get("Content-Disposition") || !bytes.Equal(auditExportReplay.body, auditExportResult.body) {
		t.Fatalf("audit export replay headers=%#v bodyEqual=%t", auditExportReplay.header, bytes.Equal(auditExportReplay.body, auditExportResult.body))
	}
	exportAccessEvents, err := repository.AuditEvents(context.Background(), tenantID, catalog.AuditEventListRequest{Filters: audit.Query{Action: "audit.exported"}, Limit: 100})
	if err != nil || len(exportAccessEvents) != 1 || exportAccessEvents[0].AfterDigest != verifiedExport.Manifest.EventsDigest {
		t.Fatalf("audit export evidence = %#v, %v", exportAccessEvents, err)
	}

	for name, result := range map[string]httpResult{
		"missing idempotency": requestJSON(t, testServer.URL, http.MethodPost, "/api/v1/audit-exports", "admin", "", "", exportBody),
		"missing range":       requestJSON(t, testServer.URL, http.MethodPost, "/api/v1/audit-exports", "admin", "audit-export-missing-range", "", map[string]any{"occurredBefore": auditBefore, "reason": "missing lower bound"}),
		"unpaired target":     requestJSON(t, testServer.URL, http.MethodPost, "/api/v1/audit-exports", "admin", "audit-export-unpaired", "", map[string]any{"targetType": "resource", "occurredFrom": auditFrom, "occurredBefore": auditBefore, "reason": "missing target"}),
		"reader role":         requestJSON(t, testServer.URL, http.MethodPost, "/api/v1/audit-exports", "contributor", "audit-export-reader", "", exportBody),
	} {
		want := http.StatusBadRequest
		if name == "reader role" {
			want = http.StatusForbidden
		}
		if result.status != want {
			t.Fatalf("%s audit export = %d, want %d: %s", name, result.status, want, result.body)
		}
	}
	otherTenantExport := requestJSON(t, testServer.URL, http.MethodPost, "/api/v1/audit-exports", "other-admin", "audit-export-other-tenant", "", exportBody)
	requireStatus(t, otherTenantExport, http.StatusOK)
	otherVerified, _, err := audit.VerifyExport(otherTenantExport.body)
	if err != nil || otherVerified.Manifest.RowCount != 0 || otherVerified.Manifest.First != nil || otherVerified.Manifest.Last != nil {
		t.Fatalf("cross-tenant empty audit export = %#v, %v", otherVerified.Manifest, err)
	}

	manifest := requestJSON(t, testServer.URL, http.MethodGet, "/.well-known/ard.json", "", "", "", nil)
	requireStatus(t, manifest, http.StatusOK)
	requireControlSchema(t, "ARDManifest", manifest.body)
	if manifest.header.Get("ETag") != candidate.ETag || manifest.header.Get("Content-Digest") != digestHeader(manifest.body) || manifest.header.Get("Content-Type") != "application/json" || manifest.header.Get("Cache-Control") != "public, no-cache, no-transform" || manifest.header.Get("Last-Modified") == "" || !strings.Contains(manifest.header.Get("Link"), `rel="ard"`) || !strings.Contains(manifest.header.Get("Link"), `rel="ai-catalog"`) {
		t.Fatalf("invalid public headers: %#v", manifest.header)
	}
	conditional := requestWithHeaders(t, testServer.URL, http.MethodGet, "/.well-known/ard.json", nil, map[string]string{"If-None-Match": candidate.ETag})
	requireStatus(t, conditional, http.StatusNotModified)
	modifiedConditional := requestWithHeaders(t, testServer.URL, http.MethodGet, "/.well-known/ard.json", nil, map[string]string{"If-Modified-Since": manifest.header.Get("Last-Modified")})
	requireStatus(t, modifiedConditional, http.StatusNotModified)
	aiCatalog := requestJSON(t, testServer.URL, http.MethodGet, "/.well-known/ai-catalog.json", "", "", "", nil)
	requireStatus(t, aiCatalog, http.StatusOK)
	requireControlSchema(t, "AICatalog", aiCatalog.body)
	if aiCatalog.header.Get("ETag") == manifest.header.Get("ETag") || aiCatalog.header.Get("Content-Digest") != digestHeader(aiCatalog.body) {
		t.Fatalf("AI Catalog headers are not independently bound: %#v", aiCatalog.header)
	}
	search := requestJSON(t, testServer.URL, http.MethodPost, "/ard/v1/search", "", "", "", map[string]any{"query": map[string]any{"text": "HTTP agent"}, "federation": "none"})
	requireStatus(t, search, http.StatusOK)
	requireControlSchema(t, "ARDSearchResponse", search.body)
	if search.header.Get("Cache-Control") != "no-store" || !strings.Contains(search.header.Get("Link"), `rel="ard"`) || bytes.Contains(search.body, []byte(`"partial"`)) || bytes.Contains(search.body, []byte(`"diagnostics"`)) {
		t.Fatalf("invalid public Search headers or extension fields: headers=%#v body=%s", search.header, search.body)
	}
	var searchResponse publication.SearchResponse
	decodeResult(t, search, &searchResponse)
	if len(searchResponse.Results) != 1 || searchResponse.Results[0].Identifier != resource.CanonicalIdentifier {
		t.Fatalf("unexpected public search: %#v", searchResponse)
	}
	aliasedSearch := requestJSON(t, testServer.URL, http.MethodPost, "/ard/v1/search", "", "", "", map[string]any{"query": map[string]any{"@context": map[string]any{"kind": "https://agenticresourcediscovery.org/ns#mediaType"}, "text": "HTTP agent", "filter": map[string]any{"kind": "application/a2a-agent-card+json"}}, "federation": "none"})
	requireStatus(t, aliasedSearch, http.StatusOK)
	requireControlSchema(t, "ARDSearchResponse", aliasedSearch.body)
	mediaTypeSearch := requestJSON(t, testServer.URL, http.MethodPost, "/ard/v1/search", "", "", "", map[string]any{"query": map[string]any{"text": "HTTP agent", "filter": map[string]any{"mediaType": "application/a2a-agent-card+json"}}, "federation": "none"})
	requireStatus(t, mediaTypeSearch, http.StatusOK)
	requireControlSchema(t, "ARDSearchResponse", mediaTypeSearch.body)
	explorePublic := requestJSON(t, testServer.URL, http.MethodPost, "/ard/v1/explore", "", "", "", map[string]any{"resultType": map[string]any{"facets": []map[string]any{{"field": "type"}}}})
	requireStatus(t, explorePublic, http.StatusOK)
	requireControlSchema(t, "ARDExploreResponse", explorePublic.body)
	if explorePublic.header.Get("Cache-Control") != "no-store" || !strings.Contains(explorePublic.header.Get("Link"), `rel="ard"`) {
		t.Fatalf("invalid public Explore headers: %#v", explorePublic.header)
	}
	listPublic := requestJSON(t, testServer.URL, http.MethodGet, "/ard/v1/agents?pageSize=1", "", "", "", nil)
	requireStatus(t, listPublic, http.StatusOK)
	requireControlSchema(t, "ARDListResponse", listPublic.body)
	for _, invalidSearch := range []any{
		map[string]any{"query": map[string]any{"text": "HTTP", "filter": map[string]any{"unsupported": "value"}}, "federation": "none"},
		map[string]any{"query": map[string]any{"text": "HTTP", "filter": map[string]any{"type": []any{"application/a2a-agent-card+json", 1}}}, "federation": "none"},
		map[string]any{"query": map[string]any{"@context": "https://example.com/context", "text": "HTTP", "filter": map[string]any{"type": "application/a2a-agent-card+json"}}, "federation": "none"},
		map[string]any{"query": map[string]any{"text": "HTTP"}, "federation": "none", "pageSize": -1},
		map[string]any{"query": map[string]any{"text": "HTTP"}, "federation": "none", "pageSize": 101},
		map[string]any{"query": "HTTP", "federation": "none"},
	} {
		result := requestJSON(t, testServer.URL, http.MethodPost, "/ard/v1/search", "", "", "", invalidSearch)
		requireStatus(t, result, http.StatusBadRequest)
	}
	for _, invalidExplore := range []map[string]any{
		{"resultType": map[string]any{"facets": []any{}}},
		{"resultType": map[string]any{"facets": []map[string]any{{"field": "unsupported"}}}},
		{"resultType": map[string]any{"facets": []map[string]any{{"field": "type", "limit": -1}}}},
		{"resultType": map[string]any{"facets": []map[string]any{{"field": "type"}, {"field": "type"}}}},
	} {
		result := requestJSON(t, testServer.URL, http.MethodPost, "/ard/v1/explore", "", "", "", invalidExplore)
		requireStatus(t, result, http.StatusBadRequest)
	}
	for _, invalidPageSize := range []string{"invalid", "0", "101"} {
		result := requestJSON(t, testServer.URL, http.MethodGet, "/ard/v1/agents?pageSize="+invalidPageSize, "", "", "", nil)
		requireStatus(t, result, http.StatusBadRequest)
	}
	if manifest.header.Get("X-Request-ID") == "" || manifest.header.Get("X-Content-Type-Options") != "nosniff" {
		t.Fatal("common security and request headers are missing")
	}
	openAPI := requestJSON(t, testServer.URL, http.MethodGet, "/openapi/control-v1.yaml", "", "", "", nil)
	requireStatus(t, openAPI, http.StatusOK)
	if openAPI.header.Get("ETag") == "" || openAPI.header.Get("Content-Digest") != digestHeader(openAPI.body) || !bytes.Contains(openAPI.body, []byte("openapi: 3.1.0")) {
		t.Fatal("embedded OpenAPI response is not content-bound")
	}

	secondCandidateResult := requestJSON(t, testServer.URL, http.MethodPost, "/api/v1/publication/snapshots", "publisher", "snapshot-two", "", map[string]any{"host": "127.0.0.1"})
	requireStatus(t, secondCandidateResult, http.StatusCreated)
	var secondCandidate publication.Snapshot
	decodeResult(t, secondCandidateResult, &secondCandidate)
	secondActivate := requestJSON(t, testServer.URL, http.MethodPost, "/api/v1/publication/snapshots/"+secondCandidate.ID+":activate", "publisher", "activate-two", secondCandidate.ETag, map[string]any{"host": "127.0.0.1", "reason": "Activate the replacement snapshot"})
	requireStatus(t, secondActivate, http.StatusOK)
	rollback := requestJSON(t, testServer.URL, http.MethodPost, "/api/v1/publication/snapshots/"+candidate.ID+":rollback", "publisher", "rollback-one", candidate.ETag, map[string]any{"host": "127.0.0.1", "reason": "Restore the prior snapshot"})
	requireStatus(t, rollback, http.StatusOK)
	missingDomainRevocationMatch := requestJSON(t, testServer.URL, http.MethodPost, "/api/v1/publishers/"+publisher.ID+"/publication-domains/catalog.example.com:revoke", "admin", "publisher-publication-domain-revoke-missing-match", "", map[string]any{"reason": "Publisher authority ended"})
	requireStatus(t, missingDomainRevocationMatch, http.StatusPreconditionFailed)
	forbiddenDomainRevocation := requestJSON(t, testServer.URL, http.MethodPost, "/api/v1/publishers/"+publisher.ID+"/publication-domains/catalog.example.com:revoke", "contributor", "publisher-publication-domain-revoke-forbidden", `"rev-2"`, map[string]any{"reason": "Publisher authority ended"})
	requireStatus(t, forbiddenDomainRevocation, http.StatusForbidden)
	staleDomainRevocation := requestJSON(t, testServer.URL, http.MethodPost, "/api/v1/publishers/"+publisher.ID+"/publication-domains/catalog.example.com:revoke", "admin", "publisher-publication-domain-revoke-stale", `"rev-99"`, map[string]any{"reason": "Publisher authority ended"})
	requireStatus(t, staleDomainRevocation, http.StatusPreconditionFailed)
	domainRevocation := requestJSON(t, testServer.URL, http.MethodPost, "/api/v1/publishers/"+publisher.ID+"/publication-domains/catalog.example.com:revoke", "admin", "publisher-publication-domain-revoke", `"rev-2"`, map[string]any{"reason": "Publisher authority ended"})
	requireStatus(t, domainRevocation, http.StatusOK)
	var revocation catalog.PublicationDomainRevocation
	decodeResult(t, domainRevocation, &revocation)
	if domainRevocation.header.Get("ETag") != `"rev-3"` || revocation.Publisher.Revision != 3 || len(revocation.Publisher.ApprovedPublicationDomains) != 0 || len(revocation.AffectedReleaseIDs) != 1 || revocation.AffectedReleaseIDs[0] != publishEnvelope.Release.ID || len(revocation.WithdrawnHosts) != 1 || revocation.WithdrawnHosts[0] != "127.0.0.1" {
		t.Fatalf("publication-domain revocation = %#v, headers %#v", revocation, domainRevocation.header)
	}
	domainWithdrawnManifest := requestJSON(t, testServer.URL, http.MethodGet, "/.well-known/ard.json", "", "", "", nil)
	requireStatus(t, domainWithdrawnManifest, http.StatusNotFound)
	reapprove := requestJSON(t, testServer.URL, http.MethodPost, "/api/v1/publishers/"+publisher.ID+"/publication-domains/catalog.example.com:approve", "admin", "publisher-publication-domain-reapprove", `"rev-3"`, map[string]any{"reason": "Publisher authority restored"})
	requireStatus(t, reapprove, http.StatusOK)
	removeActivePublisherDomain := requestJSON(t, testServer.URL, http.MethodPatch, "/api/v1/publishers/"+publisher.ID, "admin", "publisher-remove-active-domain", `"rev-4"`, map[string]any{"domains": []string{"replacement.example.com"}, "reason": "Cannot remove an approved active domain"})
	requireControlProblem(t, removeActivePublisherDomain, http.StatusConflict, catalog.CodeConflict)
	publisherPatch := requestJSON(t, testServer.URL, http.MethodPatch, "/api/v1/publishers/"+publisher.ID, "admin", "publisher-patch", `"rev-4"`, map[string]any{"displayName": "Updated example publisher", "reason": "Clarify publisher metadata"})
	requireStatus(t, publisherPatch, http.StatusOK)
	requireControlSchema(t, "Publisher", publisherPatch.body)
	var patchedPublisher catalog.Publisher
	decodeResult(t, publisherPatch, &patchedPublisher)
	if patchedPublisher.DisplayName != "Updated example publisher" || patchedPublisher.Revision != 5 || publisherPatch.header.Get("ETag") != `"rev-5"` {
		t.Fatalf("patched publisher = %#v, headers %#v", patchedPublisher, publisherPatch.header)
	}
	thirdCandidateResult := requestJSON(t, testServer.URL, http.MethodPost, "/api/v1/publication/snapshots", "publisher", "snapshot-three", "", map[string]any{"host": "127.0.0.1"})
	requireStatus(t, thirdCandidateResult, http.StatusCreated)
	var thirdCandidate publication.Snapshot
	decodeResult(t, thirdCandidateResult, &thirdCandidate)
	thirdActivate := requestJSON(t, testServer.URL, http.MethodPost, "/api/v1/publication/snapshots/"+thirdCandidate.ID+":activate", "publisher", "activate-three", thirdCandidate.ETag, map[string]any{"host": "127.0.0.1", "reason": "Activate the final snapshot"})
	requireStatus(t, thirdActivate, http.StatusOK)
	revoke := requestJSON(t, testServer.URL, http.MethodPost, "/api/v1/listing-releases/"+publishEnvelope.Release.ID+":revoke", "admin", "revoke-one", `"rev-`+jsonNumber(publishEnvelope.Release.Revision)+`"`, map[string]any{"reason": "security revocation test"})
	requireStatus(t, revoke, http.StatusOK)
	staleManifest := requestJSON(t, testServer.URL, http.MethodGet, "/.well-known/ard.json", "", "", "", nil)
	requireStatus(t, staleManifest, http.StatusNotFound)
	unpublish := requestJSON(t, testServer.URL, http.MethodPost, "/api/v1/publication:unpublish", "publisher", "unpublish-one", "", map[string]any{"host": "127.0.0.1", "reason": "Withdraw the active snapshot"})
	requireStatus(t, unpublish, http.StatusNotFound)
	unpublishedManifest := requestJSON(t, testServer.URL, http.MethodGet, "/.well-known/ard.json", "", "", "", nil)
	requireStatus(t, unpublishedManifest, http.StatusNotFound)
	t.Run("bounded diagnostics preserve retrieval and denial", func(t *testing.T) { testHTTPDiagnosticAmplification(t, testServer.URL, namespace.ID, publisher.ID) })
	t.Run("bounded Job metadata preserves retained results", func(t *testing.T) {
		testHTTPJobMetadata(t, testServer.URL, tenantID, jobStore, serviceClock.Now().Add(time.Hour))
	})
}

func TestHTTPArtifactAccessRequiresTenantAndActivePublication(t *testing.T) {
	serviceClock := clock.NewFake(time.Date(2026, 8, 29, 11, 0, 0, 0, time.UTC))
	tenantID := "00000000-0000-7000-8000-000000000001"
	cursorKey := sha256.Sum256([]byte("http-artifact-test-key"))
	repository := catalog.NewMemoryRepository()
	catalogService, err := catalog.NewService(catalog.Options{Repository: repository, Clock: serviceClock, IDs: id.Generator{}, Validator: adapter.NewRegistry(), CursorKey: cursorKey[:]})
	if err != nil {
		t.Fatal(err)
	}
	authorContext := identityfixture.WithActor(context.Background(), identity.Actor{MembershipID: "00000000-0000-7000-8000-000000000093", RealmID: "00000000-0000-7000-8000-000000000094", ActorType: identity.ActorHuman, TenantID: tenantID, Subject: "author", Roles: []identity.Role{identity.RoleTenantAdmin, identity.RoleContributor}})
	namespace, err := catalogService.CreateNamespace(authorContext, catalog.CreateNamespaceInput{Slug: "default", DisplayName: "Default"})
	if err != nil {
		t.Fatal(err)
	}
	resource, err := catalogService.CreateResource(authorContext, catalog.CreateResourceInput{NamespaceID: namespace.ID, CanonicalIdentifier: "urn:air:catalog.example.com:default:test-skill", Kind: catalog.KindSkill})
	if err != nil {
		t.Fatal(err)
	}
	content := []byte("---\nname: test-skill\ndescription: A test skill.\n---\n\n# Test skill\n")
	version, revision, evidence, err := catalogService.CreateVersion(authorContext, resource.ID, catalog.CreateVersionInput{Version: "1.0.0", Representations: []catalog.RepresentationInput{{Role: catalog.RepresentationPrimary, MediaType: adapter.MediaSkillMarkdown, Content: string(content)}}})
	if err != nil {
		t.Fatal(err)
	}
	artifact, stored, err := catalogService.GetArtifact(identityfixture.WithActor(context.Background(), identity.Actor{MembershipID: "00000000-0000-7000-8000-000000000093", RealmID: "00000000-0000-7000-8000-000000000094", ActorType: identity.ActorHuman, TenantID: tenantID, Subject: "reader", Roles: []identity.Role{identity.RoleReader}}), revision.Representations[0].ArtifactID)
	if err != nil || !bytes.Equal(stored, content) {
		t.Fatalf("repository-backed document setup = %#v, %q, %v", artifact, stored, err)
	}
	record := catalog.CatalogRecord{
		Resource:        resource,
		Version:         version,
		VersionRevision: revision,
		ListingRevision: catalog.ListingRevision{DisplayName: "Test skill", Description: "A governed published skill.", RepresentativeQueries: []string{"Find the test skill", "Show the test skill"}, Visibility: catalog.VisibilityPublic},
		Release:         catalog.ListingRelease{ID: "release-1", ActivatedAt: serviceClock.Now().Add(-time.Hour), UpdatedAt: serviceClock.Now()},
		Evidence:        evidence,
	}
	publicationManager, err := publication.NewManager(publication.Options{Records: fixedPublicationRecordSource{tenantID: tenantID, records: []catalog.CatalogRecord{record}}, Store: publication.NewMemoryStore(), Clock: serviceClock, IDs: id.Generator{}, BaseURL: "https://catalog.example.com", PublisherDomain: "catalog.example.com", ARDProfile: "ard-test", AICatalogProfile: "ai-catalog-test", CursorKey: cursorKey[:]})
	if err != nil {
		t.Fatal(err)
	}
	candidate, err := publicationManager.BuildCandidate(publicationFixtureContext(t, tenantID), tenantID)
	if err != nil {
		t.Fatal(err)
	}
	if len(candidate.ArtifactIDs) != 1 || candidate.ArtifactIDs[0] != artifact.ID {
		t.Fatal("snapshot lost the retained artifact", candidate.ArtifactIDs)
	}
	candidateJSON, err := json.Marshal(candidate)
	if err != nil {
		t.Fatal(err)
	}
	requireControlSchema(t, "PublicationSnapshot", candidateJSON)
	document, err := openapi3.NewLoader().LoadFromData(serviceapi.ControlV1)
	if err != nil {
		t.Fatal(err)
	}
	var snapshotJSON map[string]any
	if err := json.Unmarshal(candidateJSON, &snapshotJSON); err != nil {
		t.Fatal(err)
	}
	for _, invalid := range []string{candidate.ID, strings.Repeat("A", 64), strings.Repeat("a", 63), "sha256:" + strings.Repeat("a", 64)} {
		snapshotJSON["artifactIds"] = []any{invalid}
		if err := document.Components.Schemas["PublicationSnapshot"].Value.VisitJSON(snapshotJSON, openapi3.EnableJSONSchema2020()); err == nil {
			t.Fatalf("accepted invalid artifact ID %q", invalid)
		}
	}
	federationManager, err := federation.NewManager(federation.NewMemoryStore(), unavailableFetcher{}, serviceClock, id.Generator{})
	if err != nil {
		t.Fatal(err)
	}
	jobService, err := jobs.NewService(jobs.NewMemoryStore(), serviceClock, id.Generator{})
	if err != nil {
		t.Fatal(err)
	}
	authenticator, identityService := newTestIdentity(t, serviceClock, map[string]config.DevelopmentUser{
		"reader": {Subject: "reader", TenantID: tenantID, Roles: []identity.Role{identity.RoleReader}},
		"other":  {Subject: "other", TenantID: "00000000-0000-7000-8000-000000000002", Roles: []identity.Role{identity.RoleReader}},
	})
	server, err := New(Dependencies{PersonalStore: personal.NewMemoryStore(repository), Catalog: catalogService, Identity: identityService, Federation: federationManager, Jobs: jobService, Authenticator: authenticator, Idempotency: idempotency.NewMemoryStore(), Logger: slog.New(slog.DiscardHandler), DiscoveryHosts: []DiscoveryHost{anonymousDiscoveryHost(publicationManager)}, MaxBodyBytes: 1 << 20, RequestTimeout: time.Second, ServiceVersion: "test", Commit: "test", ARDProfile: "ard-test", AICatalogProfile: "ai-catalog-test", CursorKey: cursorKey[:], Now: serviceClock.Now})
	if err != nil {
		t.Fatal(err)
	}
	testServer := httptest.NewServer(server.Handler())
	defer testServer.Close()

	authenticated := requestJSON(t, testServer.URL, http.MethodGet, "/api/v1/artifacts/"+artifact.ID+"/content", "reader", "", "", nil)
	requireStatus(t, authenticated, http.StatusOK)
	if !bytes.Equal(authenticated.body, content) || authenticated.header.Get("Content-Length") != jsonNumber(int64(len(content))) || authenticated.header.Get("Content-Digest") != digestHeader(content) || !strings.Contains(authenticated.header.Get("Content-Disposition"), artifact.ID) {
		t.Fatalf("invalid authenticated artifact response: %#v", authenticated.header)
	}
	requireStatus(t, requestJSON(t, testServer.URL, http.MethodGet, "/api/v1/artifacts/missing/content", "reader", "", "", nil), http.StatusBadRequest)
	requireStatus(t, requestJSON(t, testServer.URL, http.MethodGet, "/api/v1/artifacts/"+artifact.ID+"/content", "other", "", "", nil), http.StatusNotFound)
	requireStatus(t, requestJSON(t, testServer.URL, http.MethodGet, "/public/v1/artifacts/"+artifact.ID, "", "", "", nil), http.StatusNotFound)

	if _, err := publicationManager.Activate(publicationFixtureContext(t, tenantID), tenantID, candidate.ID, "Activate reviewed snapshot"); err != nil {
		t.Fatal(err)
	}
	publicArtifact := requestJSON(t, testServer.URL, http.MethodGet, "/public/v1/artifacts/"+artifact.ID, "", "", "", nil)
	requireStatus(t, publicArtifact, http.StatusOK)
	if !bytes.Equal(publicArtifact.body, content) || publicArtifact.header.Get("ETag") != `"`+artifact.ID+`"` || publicArtifact.header.Get("Content-Length") != jsonNumber(int64(len(content))) || publicArtifact.header.Get("Content-Digest") != digestHeader(content) || !strings.Contains(publicArtifact.header.Get("Content-Disposition"), artifact.ID) || publicArtifact.header.Get("Cache-Control") == "" || publicArtifact.header.Get("Content-Type") != adapter.MediaSkillMarkdownARD {
		t.Fatalf("invalid public artifact response: %#v", publicArtifact.header)
	}
	publicArtifactConditional := requestWithHeaders(t, testServer.URL, http.MethodGet, "/public/v1/artifacts/"+artifact.ID, nil, map[string]string{"If-None-Match": `W/"` + artifact.ID + `"`})
	requireStatus(t, publicArtifactConditional, http.StatusNotModified)
	requireStatus(t, requestJSON(t, testServer.URL, http.MethodGet, "/public/v1/artifacts/missing", "", "", "", nil), http.StatusNotFound)
}

func TestHTTPFederationLifecycleAndEvidence(t *testing.T) {
	serviceClock := clock.NewFake(time.Date(2026, 8, 29, 11, 0, 0, 0, time.UTC))
	tenantID := "00000000-0000-7000-8000-000000000001"
	cursorKey := sha256.Sum256([]byte("http-federation-test-key"))
	imageFetcher := &artworkHTTPFetcher{body: artworkHTTPImage(t)}
	media, mediaErr := artwork.New(imageFetcher, artwork.DefaultLimits())
	if mediaErr != nil {
		t.Fatal(mediaErr)
	}
	catalogRepository := catalog.NewMemoryRepository()
	catalogService, err := catalog.NewService(catalog.Options{Artwork: media, Repository: catalogRepository, Clock: serviceClock, IDs: id.Generator{}, Validator: adapter.NewRegistry(), CursorKey: cursorKey[:]})
	if err != nil {
		t.Fatal(err)
	}
	endpoint := "https://catalog.remote.example/.well-known/ard.json"
	registrySearchURL := "https://registry.remote.example/ard/v1/search"
	manifest := `{"entries":[{"identifier":"urn:air:remote.example:registry:public","displayName":"Remote Registry","type":"application/ai-registry+json","url":"https://registry.remote.example/ard/v1/","representativeQueries":["Find agents","Discover tools"]},{"identifier":"urn:air:remote.example:default:one","displayName":"Remote agent","description":"A remotely observed agent.","type":"application/a2a-agent-card+json","data":{"name":"Remote agent"},"representativeQueries":["Find the remote agent","Show the remote agent"],"tags":["selected"]}]}`
	liveResults := `{"results":[{"identifier":"urn:air:remote.example:default:one","version":"1.0.0","displayName":"Remote agent","type":"application/a2a-agent-card+json","data":{"name":"Remote agent"},"tags":["selected"],"score":91,"source":"https://registry.remote.example/"}]}`
	responses := routedFetcher{
		http.MethodGet + " " + endpoint:           federationHTTPResult(endpoint, "application/ard+json", manifest),
		http.MethodPost + " " + registrySearchURL: federationHTTPResult(registrySearchURL, "application/json", liveResults),
	}
	federationStore := federation.NewMemoryStore(catalogRepository)
	fetches := &countingARDSearchFetcher{responses: responses}
	federationManager, err := federation.NewManager(federationStore, fetches, serviceClock, id.Generator{}, federation.WithDiscoveryTargets([]federation.DiscoveryTarget{{Host: "catalog.example.com", TenantID: tenantID, Audience: federation.AudienceAnonymous}}), federation.WithObservationAdopter(catalogService), federation.WithEvidenceSink(catalogRepository, true))
	if err != nil {
		t.Fatal(err)
	}
	publicationManager, err := publication.NewManager(publication.Options{Records: catalogService, Store: publication.NewMemoryStore(), Clock: serviceClock, IDs: id.Generator{}, BaseURL: "https://catalog.example.com", PublisherDomain: "catalog.example.com", ARDProfile: "ard-test", AICatalogProfile: "ai-catalog-test", CursorKey: cursorKey[:]})
	if err != nil {
		t.Fatal(err)
	}
	jobService, err := jobs.NewService(jobs.NewMemoryStore(catalogRepository), serviceClock, id.Generator{}, jobs.WithAuditEvidence())
	if err != nil {
		t.Fatal(err)
	}
	authenticator, identityService := newTestIdentity(t, serviceClock, map[string]config.DevelopmentUser{
		"source-reader":    {Subject: "source-reader", TenantID: tenantID, Roles: []identity.Role{}},
		"source-admin":     {Subject: "source-admin", TenantID: tenantID, Roles: []identity.Role{identity.RoleSourceAdmin, identity.RoleTenantAdmin, identity.RoleContributor, identity.RoleReader}},
		"source-only":      {Subject: "source-only", TenantID: tenantID, Roles: []identity.Role{identity.RoleSourceAdmin, identity.RoleReader}},
		"no-reader":        {Subject: "no-reader", TenantID: tenantID, Roles: []identity.Role{identity.RoleReviewer}},
		"other-reader":     {Subject: "other-reader", TenantID: "00000000-0000-7000-8000-000000000002", Roles: []identity.Role{identity.RoleReader}},
		"contributor-only": {Subject: "contributor-only", TenantID: tenantID, Roles: []identity.Role{identity.RoleContributor, identity.RoleReader}},
		"reader":           {Subject: "reader", TenantID: tenantID, Roles: []identity.Role{identity.RoleReader}},
		"adopter":          {Subject: "adopter", TenantID: tenantID, Roles: []identity.Role{identity.RoleServiceClient}},
	})
	dependencies := Dependencies{PersonalStore: personal.NewMemoryStore(catalogRepository), RatingsStore: ratings.NewMemoryStore(catalogRepository), Artwork: media, MediaPolicyDigest: "fixture-media-policy", Catalog: catalogService, Identity: identityService, Federation: federationManager, Jobs: jobService, Authenticator: authenticator, Idempotency: idempotency.NewMemoryStore(), Logger: slog.New(slog.DiscardHandler), DiscoveryHosts: []DiscoveryHost{anonymousDiscoveryHost(publicationManager)}, MaxBodyBytes: 1 << 20, RequestTimeout: time.Second, ServiceVersion: "test", Commit: "test", ARDProfile: "ard-test", AICatalogProfile: "ai-catalog-test", CursorKey: cursorKey[:], Now: serviceClock.Now}
	server, err := New(dependencies)
	if err != nil {
		t.Fatal(err)
	}
	testServer := httptest.NewServer(server.Handler())
	defer testServer.Close()
	readRole := requestJSON(t, testServer.URL, http.MethodPost, "/api/v1/access/roles", "source-admin", "artwork-reader-role", "", map[string]any{"name": "Artwork evidence reader", "permissions": []string{"source.read"}, "reason": "Qualify retained evidence without discovery authority"})
	requireStatus(t, readRole, http.StatusCreated)
	var artworkReaderRole identity.RoleDefinition
	decodeResult(t, readRole, &artworkReaderRole)
	membersReply := requestJSON(t, testServer.URL, http.MethodGet, "/api/v1/memberships?limit=100", "source-admin", "", "", nil)
	requireStatus(t, membersReply, http.StatusOK)
	var members struct {
		Items []identity.Membership `json:"items"`
	}
	decodeResult(t, membersReply, &members)
	var readerID string
	for _, member := range members.Items {
		if member.Subject == "source-reader" {
			readerID = member.ID
		}
	}
	if readerID == "" {
		t.Fatal("fixture source reader membership missing")
	}
	artworkReaderGrant := requestJSON(t, testServer.URL, http.MethodPost, "/api/v1/access/assignments", "source-admin", "artwork-reader-grant", "", map[string]any{"roleDefinitionId": artworkReaderRole.ID, "membershipId": readerID, "reason": "Permit retained source evidence only"})
	requireStatus(t, artworkReaderGrant, http.StatusCreated)
	var artworkReaderAssignment identity.AccessAssignment
	decodeResult(t, artworkReaderGrant, &artworkReaderAssignment)

	namespaceResult := requestJSON(t, testServer.URL, http.MethodPost, "/api/v1/namespaces", "source-admin", "federation-namespace", "", map[string]any{"slug": "adopted", "displayName": "Adopted observations"})
	requireStatus(t, namespaceResult, http.StatusCreated)
	var namespace catalog.Namespace
	decodeResult(t, namespaceResult, &namespace)
	publisherResult := requestJSON(t, testServer.URL, http.MethodPost, "/api/v1/publishers", "source-admin", "federation-publisher", "", map[string]any{"displayName": "Remote publisher", "domains": []string{"remote.example"}})
	requireStatus(t, publisherResult, http.StatusCreated)
	var publisher catalog.Publisher
	decodeResult(t, publisherResult, &publisher)

	forbiddenInspection := requestJSON(t, testServer.URL, http.MethodPost, "/api/v1/federation/source-inspections", "reader", "forbidden-inspection", "", map[string]any{"url": endpoint})
	requireStatus(t, forbiddenInspection, http.StatusForbidden)
	inspectionResult := requestJSON(t, testServer.URL, http.MethodPost, "/api/v1/federation/source-inspections", "source-admin", "inspection-one", "", map[string]any{"url": endpoint})
	requireStatus(t, inspectionResult, http.StatusCreated)
	var inspection federation.Inspection
	decodeResult(t, inspectionResult, &inspection)
	requireControlSchema(t, "FederationSourceInspection", inspectionResult.body)
	if inspection.EntryCount != 2 || inspection.RegistrySearchURL != registrySearchURL || inspectionResult.header.Get("Location") != "/api/v1/federation/source-inspections/"+inspection.ID {
		t.Fatalf("unexpected source inspection: %#v", inspection)
	}
	inspectionGet := requestJSON(t, testServer.URL, http.MethodGet, "/api/v1/federation/source-inspections/"+inspection.ID, "source-admin", "", "", nil)
	requireStatus(t, inspectionGet, http.StatusOK)
	requireControlSchema(t, "FederationSourceInspection", inspectionGet.body)

	connectResult := requestJSON(t, testServer.URL, http.MethodPost, "/api/v1/federation/sources", "source-admin", "source-one", "", map[string]any{
		"inspectionId":         inspection.ID,
		"displayName":          "Remote catalog",
		"scope":                map[string]any{"tags": []string{"selected"}},
		"updateMode":           federation.UpdateAutomatic,
		"live":                 map[string]any{"enabled": true, "queryPrivacyAcknowledged": true, "anonymousDisclosureAcknowledged": true, "federationMode": "none"},
		"discoveryAssignments": []map[string]any{{"host": "catalog.example.com", "audience": "anonymous"}},
		"reason":               "Approve the inspected source",
	})
	requireStatus(t, connectResult, http.StatusCreated)
	var source federation.Source
	decodeResult(t, connectResult, &source)
	requireControlSchema(t, "FederationSource", connectResult.body)
	if source.Revision != 1 || connectResult.header.Get("ETag") != `"rev-1"` || connectResult.header.Get("Location") != "/api/v1/federation/sources/"+source.ID {
		t.Fatalf("unexpected connected source: %#v", source)
	}
	sourcesResult := requestJSON(t, testServer.URL, http.MethodGet, "/api/v1/federation/sources", "source-admin", "", "", nil)
	requireStatus(t, sourcesResult, http.StatusOK)
	requireControlSchema(t, "FederationSourcePage", sourcesResult.body)
	for _, query := range []string{
		"displayName=" + url.QueryEscape(source.DisplayName) + "&status=active",
		"displayName=" + url.QueryEscape(source.DisplayName), "status=active",
	} {
		filtered := requestJSON(t, testServer.URL, http.MethodGet, "/api/v1/federation/sources?"+query, "source-admin", "", "", nil)
		requireStatus(t, filtered, http.StatusOK)
		requireControlSchema(t, "FederationSourcePage", filtered.body)
		var page cursorPage[federation.Source]
		decodeResult(t, filtered, &page)
		if len(page.Items) != 1 || page.Items[0].ID != source.ID {
			t.Fatalf("exact source selection: %#v", page)
		}
	}
	for _, query := range []string{"displayName=" + url.QueryEscape(strings.ToUpper(source.DisplayName)), "displayName=" + url.QueryEscape(" "+source.DisplayName), "status=paused", "status=error"} {
		filtered := requestJSON(t, testServer.URL, http.MethodGet, "/api/v1/federation/sources?"+query, "source-admin", "", "", nil)
		requireStatus(t, filtered, http.StatusOK)
		var page cursorPage[federation.Source]
		decodeResult(t, filtered, &page)
		if len(page.Items) != 0 {
			t.Fatalf("nonexact source selection: %#v", page)
		}
	}
	for _, query := range []string{"displayName=", "displayName=a&displayName=b", "status=", "status=active&status=paused", "status=removed", "status=Active", "displayName=a%00b", "displayName=" + url.QueryEscape(strings.Repeat("界", 161))} {
		requireControlProblem(t, requestJSON(t, testServer.URL, http.MethodGet, "/api/v1/federation/sources?"+query, "source-admin", "", "", nil), http.StatusBadRequest, catalog.CodeInvalidArgument)
	}
	sourceResult := requestJSON(t, testServer.URL, http.MethodGet, "/api/v1/federation/sources/"+source.ID, "source-admin", "", "", nil)
	requireStatus(t, sourceResult, http.StatusOK)
	requireControlSchema(t, "FederationSource", sourceResult.body)

	directSyncInitiator := "00000000-0000-7000-8000-000000000091"
	actorContext := identityfixture.WithActor(context.Background(), identity.Actor{TenantID: tenantID, Subject: "source-admin", Roles: []identity.Role{identity.RoleSourceAdmin, identity.RoleReader}, RealmID: "00000000-0000-7000-8000-000000000092", MembershipID: directSyncInitiator, ActorType: identity.ActorHuman})
	actorContext = audit.WithJobExecution(actorContext, audit.JobExecution{ID: "00000000-0000-7000-8000-000000000090", Kind: "source.sync", Attempt: 1, Initiator: directSyncInitiator, Trigger: "manual"})
	snapshot, err := federationManager.Sync(actorContext, source.ID)
	if err != nil {
		t.Fatal(err)
	}
	snapshotsResult := requestJSON(t, testServer.URL, http.MethodGet, "/api/v1/federation/sources/"+source.ID+"/snapshots?limit=10", "source-admin", "", "", nil)
	requireStatus(t, snapshotsResult, http.StatusOK)
	requireControlSchema(t, "FederationSnapshotPage", snapshotsResult.body)
	snapshotResult := requestJSON(t, testServer.URL, http.MethodGet, "/api/v1/federation/snapshots/"+snapshot.ID, "source-admin", "", "", nil)
	requireStatus(t, snapshotResult, http.StatusOK)
	requireControlSchema(t, "FederationSnapshot", snapshotResult.body)
	observationsResult := requestJSON(t, testServer.URL, http.MethodGet, "/api/v1/observations?sourceId="+source.ID, "source-admin", "", "", nil)
	requireStatus(t, observationsResult, http.StatusOK)
	requireControlSchema(t, "ObservationPage", observationsResult.body)
	var observationsPage cursorPage[federation.Observation]
	decodeResult(t, observationsResult, &observationsPage)
	if len(observationsPage.Items) != 1 || observationsPage.Items[0].Identifier != "urn:air:remote.example:default:one" {
		t.Fatalf("unexpected observations: %#v", observationsPage)
	}
	observation := observationsPage.Items[0]
	indexedPath := "/api/v1/catalog/indexed-record?identifier=" + observation.Identifier + "&versionPresent=false"
	indexedDetail := requestJSON(t, testServer.URL, http.MethodGet, indexedPath, "reader", "", "", nil)
	requireStatus(t, indexedDetail, http.StatusOK)
	requireControlSchema(t, "CatalogSearchResult", indexedDetail.body)
	var indexed catalog.CatalogSearchResult
	decodeResult(t, indexedDetail, &indexed)
	if indexed.Type != catalog.SearchResultIndexed || indexed.Identifier != observation.Identifier || indexed.VersionPresent || indexed.Indexed == nil || indexed.Local != nil || indexed.Live != nil {
		t.Fatalf("reader indexed detail exposed the wrong projection: %#v", indexed)
	}
	qualifyHTTPArtworkOverrides(t, testServer.URL, source, indexed, imageFetcher)
	requireControlProblem(t, requestJSON(t, testServer.URL, http.MethodGet, indexedPath, "no-reader", "", "", nil), http.StatusForbidden, catalog.CodeForbidden)
	requireControlProblem(t, requestJSON(t, testServer.URL, http.MethodGet, indexedPath, "other-reader", "", "", nil), http.StatusNotFound, catalog.CodeNotFound)
	requireStatus(t, requestJSON(t, testServer.URL, http.MethodGet, indexedPath, "", "", "", nil), http.StatusUnauthorized)
	for _, invalidPath := range []string{indexedPath + "&version=1", indexedPath + "&versionPresent=true", "/api/v1/catalog/indexed-record?identifier=bad&versionPresent=true", "/api/v1/catalog/indexed-record?identifier=" + observation.Identifier} {
		requireControlProblem(t, requestJSON(t, testServer.URL, http.MethodGet, invalidPath, "reader", "", "", nil), http.StatusBadRequest, catalog.CodeInvalidArgument)
	}
	requireControlProblem(t, requestJSON(t, testServer.URL, http.MethodGet, strings.Replace(indexedPath, "versionPresent=false", "versionPresent=true&version=1", 1), "reader", "", "", nil), http.StatusNotFound, catalog.CodeNotFound)
	if bytes.Contains(observationsResult.body, []byte(`"rawEntry"`)) || !observation.RawEntryAvailable || observation.ExactEntryDigest == "" || observation.NormalizedEntryDigest == "" {
		t.Fatalf("observation response exposed raw data or omitted digest state: %s", observationsResult.body)
	}
	rawObservation := requestJSON(t, testServer.URL, http.MethodGet, "/api/v1/observations/"+observation.ID+"/raw", "source-admin", "", "", nil)
	requireStatus(t, rawObservation, http.StatusOK)
	if rawObservation.header.Get("Content-Digest") != digestHeader(rawObservation.body) || !strings.Contains(rawObservation.header.Get("Content-Disposition"), observation.ID) {
		t.Fatalf("invalid raw observation response: %#v", rawObservation.header)
	}
	occurrencesResult := requestJSON(t, testServer.URL, http.MethodGet, "/api/v1/observations/"+observation.ID+"/occurrences?limit=10", "source-admin", "", "", nil)
	requireStatus(t, occurrencesResult, http.StatusOK)
	requireControlSchema(t, "ObservationOccurrencePage", occurrencesResult.body)
	var occurrencesPage cursorPage[federation.ObservationOccurrence]
	decodeResult(t, occurrencesResult, &occurrencesPage)
	if len(occurrencesPage.Items) != 1 || !occurrencesPage.Items[0].EvidenceComplete || occurrencesPage.Items[0].EntryLocation != "/entries/1" {
		t.Fatalf("unexpected observation occurrences: %#v", occurrencesPage)
	}
	occurrence := occurrencesPage.Items[0]
	groupResult := requestJSON(t, testServer.URL, http.MethodGet, "/api/v1/observation-groups?identifier="+observation.Identifier+"&versionPresent=false", "source-admin", "", "", nil)
	requireStatus(t, groupResult, http.StatusOK)
	requireControlSchema(t, "ObservationGroup", groupResult.body)
	var group federation.ObservationGroup
	decodeResult(t, groupResult, &group)
	if group.Conflict || len(group.Members) != 1 || len(group.Members[0].Contributions) != 1 || group.Members[0].Contributions[0].OccurrenceCount != 1 || group.Members[0].Contributions[0].LatestOccurrence.SnapshotID != snapshot.ID {
		t.Fatalf("unexpected observation group: %#v", group)
	}
	requireControlProblem(t, requestJSON(t, testServer.URL, http.MethodGet, "/api/v1/observation-groups?identifier="+observation.Identifier+"&versionPresent=false&version=1", "source-admin", "", "", nil), http.StatusBadRequest, catalog.CodeInvalidArgument)
	requireControlProblem(t, requestJSON(t, testServer.URL, http.MethodGet, "/api/v1/observations/"+observation.ID+"/occurrences", "contributor-only", "", "", nil), http.StatusForbidden, catalog.CodeForbidden)
	adoptBody := map[string]any{"snapshotId": occurrence.SnapshotID, "retrievalDigest": occurrence.RetrievalDigest, "entryLocation": occurrence.EntryLocation, "namespaceId": namespace.ID, "publisherId": publisher.ID, "version": "1.0.0"}
	requireControlProblem(t, requestJSON(t, testServer.URL, http.MethodPost, "/api/v1/observations/"+observation.ID+":adopt", "source-only", "adopt-source-only", "", adoptBody), http.StatusForbidden, catalog.CodeForbidden)
	requireControlProblem(t, requestJSON(t, testServer.URL, http.MethodPost, "/api/v1/observations/"+observation.ID+":adopt", "contributor-only", "adopt-contributor-only", "", adoptBody), http.StatusForbidden, catalog.CodeForbidden)
	adoptResult, adoptReplay := qualifyHTTPWriteOnlyCreationAndAdoption(t, testServer.URL, dependencies, namespace.ID, observation.ID, adoptBody)
	requireStatus(t, adoptResult, http.StatusCreated)
	requireControlSchema(t, "ObservationAdoption", adoptResult.body)
	var adoption catalog.ExternalObservationAdoption
	decodeResult(t, adoptResult, &adoption)
	if adoption.Resource.CanonicalIdentifier != observation.Identifier || adoption.Version.Version != "1.0.0" || adoption.VersionRevision.Adoption == nil || adoption.VersionRevision.Adoption.SnapshotID != snapshot.ID || adoption.Listing.CandidateRevisionID != adoption.ListingRevision.ID || adoptResult.header.Get("Location") != "/api/v1/resources/"+adoption.Resource.ID {
		t.Fatalf("unexpected observation adoption: %#v", adoption)
	}
	requireStatus(t, adoptReplay, http.StatusCreated)
	if adoptReplay.header.Get("Idempotency-Replayed") != "true" || !bytes.Equal(adoptReplay.body, adoptResult.body) {
		t.Fatalf("observation adoption replay changed: %#v", adoptReplay.header)
	}
	observationSearch := requestJSON(t, testServer.URL, http.MethodPost, "/api/v1/observations:search", "source-admin", "", "", map[string]any{"text": "remote agent", "kinds": []catalog.Kind{catalog.KindAgent}, "tags": []string{"selected"}})
	requireStatus(t, observationSearch, http.StatusOK)
	requireControlSchema(t, "ObservationSearchPage", observationSearch.body)

	testResult := requestJSON(t, testServer.URL, http.MethodPost, "/api/v1/federation/sources/"+source.ID+":test", "source-admin", "source-test", `"rev-1"`, map[string]any{})
	requireStatus(t, testResult, http.StatusOK)
	requireControlSchema(t, "FederationTestResult", testResult.body)
	if testResult.header.Get("ETag") != `"rev-2"` {
		t.Fatalf("source test ETag = %q", testResult.header.Get("ETag"))
	}
	requireControlProblem(t, requestJSON(t, testServer.URL, http.MethodPost, "/api/v1/federation/sources/"+source.ID+":test", "source-admin", "source-test-stale", `"rev-1"`, map[string]any{}), http.StatusPreconditionFailed, catalog.CodePreconditionFailed)
	requireControlProblem(t, requestJSON(t, testServer.URL, http.MethodPost, "/api/v1/federation/sources/"+source.ID+":sync", "source-admin", "source-sync-stale", `"rev-1"`, map[string]any{}), http.StatusPreconditionFailed, catalog.CodePreconditionFailed)
	syncResult := requestJSON(t, testServer.URL, http.MethodPost, "/api/v1/federation/sources/"+source.ID+":sync", "source-admin", "source-sync", `"rev-2"`, map[string]any{})
	requireStatus(t, syncResult, http.StatusAccepted)
	requireControlSchema(t, "SourceSyncAccepted", syncResult.body)
	var syncEnvelope struct {
		Job jobs.View `json:"job"`
	}
	decodeResult(t, syncResult, &syncEnvelope)
	if syncEnvelope.Job.Kind != "source.sync" || syncResult.header.Get("Location") != "/api/v1/jobs/"+syncEnvelope.Job.ID {
		t.Fatalf("unexpected source sync job: %#v", syncEnvelope)
	}
	jobGet := requestJSON(t, testServer.URL, http.MethodGet, "/api/v1/jobs/"+syncEnvelope.Job.ID, "source-admin", "", "", nil)
	requireStatus(t, jobGet, http.StatusOK)
	requireControlSchema(t, "Job", jobGet.body)
	jobList := requestJSON(t, testServer.URL, http.MethodGet, "/api/v1/jobs?limit=10", "source-admin", "", "", nil)
	requireStatus(t, jobList, http.StatusOK)
	requireControlSchema(t, "JobPage", jobList.body)

	previewResult := requestJSON(t, testServer.URL, http.MethodPost, "/api/v1/federation/sources/"+source.ID+"/change-previews", "source-admin", "preview-one", "", map[string]any{"intent": "update", "displayName": "Remote catalog updated"})
	requireStatus(t, previewResult, http.StatusOK)
	requireControlSchema(t, "FederationChangePreview", previewResult.body)
	var preview federation.ChangePreview
	decodeResult(t, previewResult, &preview)
	missingPatchPrecondition := requestJSON(t, testServer.URL, http.MethodPatch, "/api/v1/federation/sources/"+source.ID, "source-admin", "patch-missing-precondition", "", map[string]any{"displayName": "Remote catalog updated", "changePreviewDigest": preview.Digest, "reason": "Apply the reviewed display-name change"})
	requireStatus(t, missingPatchPrecondition, http.StatusPreconditionFailed)
	patchResult := requestJSON(t, testServer.URL, http.MethodPatch, "/api/v1/federation/sources/"+source.ID, "source-admin", "patch-one", `"rev-2"`, map[string]any{"displayName": "Remote catalog updated", "changePreviewDigest": preview.Digest, "reason": "Apply the reviewed display-name change"})
	requireStatus(t, patchResult, http.StatusOK)
	decodeResult(t, patchResult, &source)
	requireControlSchema(t, "FederationSource", patchResult.body)
	if source.Revision != 3 || source.DisplayName != "Remote catalog updated" {
		t.Fatalf("unexpected updated source: %#v", source)
	}

	liveBody := map[string]any{"text": "remote agent", "pageSize": 5, "sourceIds": []string{source.ID}, "reason": "Investigate configured Registry results"}
	beforeComplete := fetches.calls
	completeSearch := requestJSON(t, testServer.URL, http.MethodPost, "/api/v1/federation/sources:live-search", "source-admin", "source-live-search", "", liveBody)
	requireStatus(t, completeSearch, http.StatusOK)
	requireControlSchema(t, "LiveSearchResponse", completeSearch.body)
	var completeResponse federation.LiveSearchResponse
	decodeResult(t, completeSearch, &completeResponse)
	if len(completeResponse.Results) != 1 || completeResponse.Results[0].Score != 91 || completeResponse.Partial || len(completeResponse.Diagnostics) != 1 || completeResponse.Diagnostics[0].Status != "succeeded" || fetches.calls != beforeComplete+1 {
		t.Fatalf("complete administrator search changed results, diagnostics, or request limit: %#v", completeResponse)
	}
	// Preserve the complete-response control; only the next upstream page adds
	// a continuation token. The bounded query must retain the same useful entry.
	boundedLiveResults := strings.TrimSuffix(liveResults, "}") + `,"pageToken":"opaque-upstream-next-page"}`
	responses[http.MethodPost+" "+registrySearchURL] = federationHTTPResult(registrySearchURL, "application/json", boundedLiveResults)
	beforeLive := fetches.calls
	liveSearch := requestJSON(t, testServer.URL, http.MethodPost, "/api/v1/federation/sources:live-search", "source-admin", "source-live-search-bounded", "", liveBody)
	requireStatus(t, liveSearch, http.StatusOK)
	var liveResponse federation.LiveSearchResponse
	decodeResult(t, liveSearch, &liveResponse)
	if len(liveResponse.Results) != 1 || liveResponse.Results[0].Score != 91 || !liveResponse.Partial || len(liveResponse.Diagnostics) != 2 || liveResponse.Diagnostics[0].Status != "continuation_unavailable" || liveResponse.Diagnostics[0].SourceID != source.ID || liveResponse.Diagnostics[1].Status != "succeeded" || fetches.calls != beforeLive+1 {
		t.Fatalf("bounded administrator search lost useful results, diagnostics, or request limit: %#v (calls=%d)", liveResponse, fetches.calls-beforeLive)
	}
	liveReplay := requestJSON(t, testServer.URL, http.MethodPost, "/api/v1/federation/sources:live-search", "source-admin", "source-live-search-bounded", "", liveBody)
	requireStatus(t, liveReplay, http.StatusOK)
	if liveReplay.header.Get("Idempotency-Replayed") != "true" || !bytes.Equal(liveReplay.body, liveSearch.body) || fetches.calls != beforeLive+1 {
		t.Fatal("diagnostic replay changed the response or queried the upstream again")
	}
	beforeActivity, activityErr := server.observability.Activity(false)
	if activityErr != nil {
		t.Fatal(activityErr)
	}
	beforeCatalog := fetches.calls
	catalogSearch := requestJSON(t, testServer.URL, http.MethodPost, "/api/v1/catalog/search", "reader", "", "", map[string]any{"text": "remote agent", "origins": []string{"live"}, "limit": 5})
	requireStatus(t, catalogSearch, http.StatusOK)
	var catalogResponse catalog.CatalogSearchResponse
	decodeResult(t, catalogSearch, &catalogResponse)
	if len(catalogResponse.Items) != 1 || catalogResponse.Items[0].Type != catalog.SearchResultLive || catalogResponse.Items[0].Identifier != liveResponse.Results[0].Identifier || catalogResponse.LiveCount != 1 || catalogResponse.DurableTotal != 0 || !catalogResponse.Partial || len(catalogResponse.Diagnostics) != 1 || catalogResponse.Diagnostics[0].Status != "continuation_unavailable" || catalogResponse.Diagnostics[0].SourceID != source.ID || fetches.calls != beforeCatalog+1 {
		t.Fatalf("bounded Catalog search lost useful results, diagnostics, or request limit: %#v (calls=%d)", catalogResponse, fetches.calls-beforeCatalog)
	}
	afterActivity := activityAfterCompletion(t, server.observability, beforeActivity.HTTP.Completed+1)
	if afterActivity.HTTP.Families[observability.FamilyControlSearch].Completed != beforeActivity.HTTP.Families[observability.FamilyControlSearch].Completed+1 || afterActivity.HTTP.Families[observability.FamilyControlSearch].StatusClasses[1].Completed != beforeActivity.HTTP.Families[observability.FamilyControlSearch].StatusClasses[1].Completed+1 || afterActivity.ControlSearch.Outcomes[1].Completed != beforeActivity.ControlSearch.Outcomes[1].Completed+1 {
		t.Fatal("HTTP 200 partial Search did not retain its separate partial outcome")
	}
	for schema, body := range map[string][]byte{"LiveSearchResponse": liveSearch.body, "CatalogSearchResponse": catalogSearch.body} {
		t.Run("bounded continuation "+schema, func(t *testing.T) {
			requireControlSchema(t, schema, body)
		})
	}

	t.Run("live rating proof refresh", func(t *testing.T) {
		qualifyHTTPLiveRatingProofRefresh(t, testServer.URL, serviceClock, responses, registrySearchURL, liveResults)
	})

	versionPresent := indexed.VersionPresent
	qualifyHTTPPersonalExactReference(t, testServer.URL, "reader", personal.Reference{Origin: "foreign", Kind: indexed.Kind, SourceID: indexed.Indexed.PresentationSourceID, PresentationGeneration: indexed.Indexed.PresentationGeneration, Identifier: indexed.Identifier, VersionPresent: &versionPresent, Version: indexed.Version})
	requireControlProblem(t, requestJSON(t, testServer.URL, http.MethodPost, "/api/v1/federation/sources/"+source.ID+":pause", "source-admin", "source-pause-stale", `"rev-2"`, map[string]any{"reason": "Pause the source"}), http.StatusPreconditionFailed, catalog.CodePreconditionFailed)
	pauseResult := requestJSON(t, testServer.URL, http.MethodPost, "/api/v1/federation/sources/"+source.ID+":pause", "source-admin", "source-pause", `"rev-3"`, map[string]any{"reason": "Pause the source for maintenance"})
	requireStatus(t, pauseResult, http.StatusOK)
	// Retained evidence is not discovery or media delivery. The old exact
	// generation stays readable by a source reader after the source is paused.

	artworkQuery := url.Values{"identifier": {indexed.Identifier}, "versionPresent": {strconv.FormatBool(indexed.VersionPresent)}, "kind": {string(indexed.Kind)}, "presentationGeneration": {strconv.FormatInt(indexed.Indexed.PresentationGeneration, 10)}}
	if indexed.VersionPresent {
		artworkQuery.Set("version", indexed.Version)
	}
	artworkPath := "/api/v1/federation/sources/" + source.ID + "/artwork"
	beforeEvidenceImages, beforeEvidencePeers := imageFetcher.calls, fetches.calls
	retainedHead := requestJSON(t, testServer.URL, http.MethodGet, artworkPath+"?"+artworkQuery.Encode(), "source-reader", "", "", nil)
	requireStatus(t, retainedHead, http.StatusOK)
	requireControlSchema(t, "ArtworkOverride", retainedHead.body)
	var retainedOverride federation.ArtworkOverride
	decodeResult(t, retainedHead, &retainedOverride)
	expectedTarget := federation.ArtworkTarget{SourceID: source.ID, PresentationGeneration: indexed.Indexed.PresentationGeneration, Identifier: indexed.Identifier, VersionPresent: indexed.VersionPresent, Version: indexed.Version, Kind: indexed.Kind}
	if retainedOverride.Target != expectedTarget || retainedOverride.Revision != 3 || retainedHead.header.Get("ETag") != `"rev-3"` || retainedOverride.Icon.Mode != federation.ArtworkInherit {
		t.Fatalf("retained head changed after pause: %#v", retainedOverride)
	}
	// Exact historical reads must preserve all revisions and cursor bounds,
	// without granting media delivery or mutable source authority.
	artworkHistoryPath := artworkPath + "/history?" + artworkQuery.Encode() + "&limit=1"
	artworkCursor := ""
	firstArtworkCursor := ""
	var retainedRevisions []int64
	for pageNumber := 1; pageNumber <= 3; pageNumber++ {
		path := artworkHistoryPath
		if artworkCursor != "" {
			path += "&cursor=" + url.QueryEscape(artworkCursor)
		}
		retained := requestJSON(t, testServer.URL, http.MethodGet, path, "source-reader", "", "", nil)
		requireStatus(t, retained, http.StatusOK)
		requireControlSchema(t, "ArtworkOverridePage", retained.body)
		var page cursorPage[federation.ArtworkOverride]
		decodeResult(t, retained, &page)
		if len(page.Items) != 1 || page.Items[0].Target != expectedTarget || (page.NextCursor != "") != (pageNumber < 3) {
			t.Fatalf("retained history lost exact bounded page %d: %#v", pageNumber, page)
		}
		retainedRevisions = append(retainedRevisions, page.Items[0].Revision)
		artworkCursor = page.NextCursor
		if pageNumber == 1 {
			firstArtworkCursor = page.NextCursor
		}
	}
	slices.Sort(retainedRevisions)
	if !slices.Equal(retainedRevisions, []int64{1, 2, 3}) {
		t.Fatal("retained history duplicated or omitted a revision", retainedRevisions)
	}
	deniedEdit := map[string]any{"target": expectedTarget, "icon": map[string]any{"mode": "suppress"}, "screenshots": map[string]any{"mode": "inherit"}, "reason": "Reader cannot edit retained evidence"}
	requireControlProblem(t, requestJSON(t, testServer.URL, http.MethodPost, artworkPath, "source-reader", "artwork-reader-denied", `"rev-3"`, deniedEdit), http.StatusForbidden, catalog.CodeForbidden)
	requireControlProblem(t, requestJSON(t, testServer.URL, http.MethodPost, "/api/v1/artwork/read", "source-reader", "", "", map[string]any{"origin": "indexed", "target": expectedTarget, "slot": "icon"}), http.StatusForbidden, catalog.CodeForbidden)
	unchangedHead := requestJSON(t, testServer.URL, http.MethodGet, artworkPath+"?"+artworkQuery.Encode(), "source-reader", "", "", nil)
	requireStatus(t, unchangedHead, http.StatusOK)
	if !bytes.Equal(unchangedHead.body, retainedHead.body) || unchangedHead.header.Get("ETag") != retainedHead.header.Get("ETag") || imageFetcher.calls != beforeEvidenceImages || fetches.calls != beforeEvidencePeers {
		t.Fatal("read-only evidence checks changed the head or contacted a destination")
	}
	requireControlProblem(t, requestJSON(t, testServer.URL, http.MethodGet, artworkPath+"?"+artworkQuery.Encode(), "reader", "", "", nil), http.StatusForbidden, catalog.CodeForbidden)
	requireControlProblem(t, requestJSON(t, testServer.URL, http.MethodPost, "/api/v1/catalog/search", "source-reader", "", "", map[string]any{"text": "remote", "origins": []string{"indexed"}}), http.StatusForbidden, catalog.CodeForbidden)

	// Retained evidence is private even when a caller has its exact identifiers
	// or an earlier valid cursor. Regrant restores access without rewriting it.
	revokedRead := requestJSON(t, testServer.URL, http.MethodPost, "/api/v1/access/assignments/"+artworkReaderAssignment.ID+":revoke", "source-admin", "artwork-reader-revoke", artworkReaderGrant.header.Get("ETag"), map[string]any{"reason": "Remove only the private evidence path"})
	requireStatus(t, revokedRead, http.StatusOK)
	for _, test := range []struct {
		path   string
		status int
		code   catalog.ErrorCode
	}{
		{artworkPath + "?" + artworkQuery.Encode(), http.StatusForbidden, catalog.CodeForbidden},
		{artworkHistoryPath, http.StatusForbidden, catalog.CodeForbidden},
		// Administration cursors bind current policy before the history read.
		{artworkHistoryPath + "&cursor=" + url.QueryEscape(firstArtworkCursor), http.StatusBadRequest, catalog.CodeInvalidArgument},
	} {
		refused := requestJSON(t, testServer.URL, http.MethodGet, test.path, "source-reader", "", "", nil)
		requireControlProblem(t, refused, test.status, test.code)
		if refused.header.Get("ETag") != "" || bytes.Contains(refused.body, []byte(retainedOverride.Reason)) || bytes.Contains(refused.body, []byte(retainedOverride.ID)) {
			t.Fatal("revoked reader received private artwork evidence")
		}
	}
	regrantedRead := requestJSON(t, testServer.URL, http.MethodPost, "/api/v1/access/assignments", "source-admin", "artwork-reader-regrant", "", map[string]any{"roleDefinitionId": artworkReaderRole.ID, "membershipId": readerID, "reason": "Restore only the evidence path"})
	requireStatus(t, regrantedRead, http.StatusCreated)
	var replacementRead identity.AccessAssignment
	decodeResult(t, regrantedRead, &replacementRead)
	if replacementRead.ID == artworkReaderAssignment.ID || replacementRead.State != identity.AssignmentActive {
		t.Fatal("regrant rewrote the revoked assignment")
	}
	restoredHead := requestJSON(t, testServer.URL, http.MethodGet, artworkPath+"?"+artworkQuery.Encode(), "source-reader", "", "", nil)
	requireStatus(t, restoredHead, http.StatusOK)
	if !bytes.Equal(restoredHead.body, retainedHead.body) || restoredHead.header.Get("ETag") != retainedHead.header.Get("ETag") || imageFetcher.calls != beforeEvidenceImages || fetches.calls != beforeEvidencePeers {
		t.Fatal("evidence revocation/regrant changed the override or contacted a destination")
	}

	decodeResult(t, pauseResult, &source)
	resumeResult := requestJSON(t, testServer.URL, http.MethodPost, "/api/v1/federation/sources/"+source.ID+":resume", "source-admin", "source-resume", `"rev-4"`, map[string]any{"reason": "Resume the tested source"})
	requireStatus(t, resumeResult, http.StatusOK)
	decodeResult(t, resumeResult, &source)
	removalPreviewResult := requestJSON(t, testServer.URL, http.MethodPost, "/api/v1/federation/sources/"+source.ID+"/change-previews", "source-admin", "removal-preview", "", map[string]any{"intent": "remove"})
	requireStatus(t, removalPreviewResult, http.StatusOK)
	var removalPreview federation.ChangePreview
	decodeResult(t, removalPreviewResult, &removalPreview)
	removalPath := "/api/v1/federation/sources/" + source.ID + ":remove"
	for _, check := range []struct {
		name, actor, etag string
		body              map[string]any
		status            int
	}{
		{"missing preview", "source-admin", `"rev-5"`, map[string]any{"reason": "Remove the retired source"}, http.StatusPreconditionFailed},
		{"wrong preview", "source-admin", `"rev-5"`, map[string]any{"reason": "Remove the retired source", "changePreviewDigest": "sha256:" + strings.Repeat("f", 64)}, http.StatusPreconditionFailed},
		{"stale revision", "source-admin", `"rev-4"`, map[string]any{"reason": "Remove the retired source", "changePreviewDigest": removalPreview.Digest}, http.StatusPreconditionFailed},
		{"unknown field", "source-admin", `"rev-5"`, map[string]any{"reason": "Remove the retired source", "changePreviewDigest": removalPreview.Digest, "unknown": true}, http.StatusBadRequest},
		{"unauthorized", "reader", `"rev-5"`, map[string]any{"reason": "Remove the retired source", "changePreviewDigest": removalPreview.Digest}, http.StatusForbidden},
	} {
		result := requestJSON(t, testServer.URL, http.MethodPost, removalPath, check.actor, "remove-negative-"+check.name, check.etag, check.body)
		requireStatus(t, result, check.status)
	}
	removeResult := requestJSON(t, testServer.URL, http.MethodPost, "/api/v1/federation/sources/"+source.ID+":remove", "source-admin", "source-remove", `"rev-5"`, map[string]any{"reason": "Remove the retired source", "changePreviewDigest": removalPreview.Digest})
	requireStatus(t, removeResult, http.StatusNoContent)
	removeReplay := requestJSON(t, testServer.URL, http.MethodPost, removalPath, "source-admin", "source-remove", `"rev-5"`, map[string]any{"reason": "Remove the retired source", "changePreviewDigest": removalPreview.Digest})
	requireStatus(t, removeReplay, http.StatusNoContent)
	if removeReplay.header.Get("Idempotency-Replayed") != "true" {
		t.Fatal("source removal did not replay after removal")
	}
	requireStatus(t, requestJSON(t, testServer.URL, http.MethodGet, "/api/v1/federation/sources/"+source.ID, "source-admin", "", "", nil), http.StatusNotFound)
	removedObservations := requestJSON(t, testServer.URL, http.MethodGet, "/api/v1/observations?freshness=all", "source-admin", "", "", nil)
	requireStatus(t, removedObservations, http.StatusOK)

	auditEvents, err := catalogRepository.AuditEvents(context.Background(), tenantID, catalog.AuditEventListRequest{Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	byAction := make(map[string][]catalog.AuditEvent)
	for _, event := range auditEvents {
		byAction[event.Action] = append(byAction[event.Action], event)
	}
	wantCounts := map[string]int{
		audit.ActionSourceInspected:                1,
		audit.ActionSourceInspectionRead:           1,
		audit.ActionSourceConnected:                1,
		audit.ActionSourceSyncCompleted:            1,
		audit.ActionRawObservationRead:             1,
		audit.ActionSourceTested:                   1,
		audit.ActionSourceSyncRequested:            1,
		audit.ActionSourceChangePreviewCreated:     2,
		audit.ActionSourceRevisionActivated:        1,
		audit.ActionFederationLiveSearchDiagnostic: 2,
		audit.ActionSourcePaused:                   1,
		audit.ActionSourceResumed:                  1,
		audit.ActionSourceRemoved:                  1,
	}
	for action, want := range wantCounts {
		if got := len(byAction[action]); got != want {
			t.Fatalf("federation HTTP audit action %q count = %d, want %d: %#v", action, got, want, byAction[action])
		}
	}
	syncAudit := byAction[audit.ActionSourceSyncCompleted][0]
	syncFacts, _ := syncAudit.Details["facts"].(map[string]any)
	if syncAudit.Actor != audit.PrincipalIDCatalogWorker || syncFacts["initiator"] != directSyncInitiator || syncFacts["jobId"] != "00000000-0000-7000-8000-000000000090" || syncFacts["trigger"] != "manual" {
		t.Fatalf("source synchronization audit attribution = %#v", syncAudit)
	}
	requestAudit := byAction[audit.ActionSourceSyncRequested][0]
	requestFacts, _ := requestAudit.Details["facts"].(map[string]any)
	if requestAudit.TargetID != source.ID || requestFacts["jobId"] != syncEnvelope.Job.ID || requestFacts["deduplicated"] != false {
		t.Fatalf("manual synchronization request audit = %#v", requestAudit)
	}
	liveAudit, err := json.Marshal(byAction[audit.ActionFederationLiveSearchDiagnostic][0])
	if err != nil {
		t.Fatal(err)
	}
	for _, privateValue := range []string{"remote agent", endpoint, registrySearchURL} {
		if bytes.Contains(bytes.ToLower(liveAudit), []byte(privateValue)) {
			t.Fatalf("live-search audit retained private value %q: %s", privateValue, liveAudit)
		}
	}
}

func TestHTTPSkillPackageInventoryAndHandoffContract(t *testing.T) {
	testServer := newWebhookHTTPServer(t)
	defer testServer.Close()

	namespaceResult := requestJSON(t, testServer.URL, http.MethodPost, "/api/v1/namespaces", "admin", "skill-package-namespace", "", map[string]any{"slug": "skill-packages", "displayName": "Skill packages"})
	requireStatus(t, namespaceResult, http.StatusCreated)
	var namespace catalog.Namespace
	decodeResult(t, namespaceResult, &namespace)
	resourceResult := requestJSON(t, testServer.URL, http.MethodPost, "/api/v1/resources", "admin", "skill-package-resource", "", map[string]any{"namespaceId": namespace.ID, "canonicalIdentifier": "urn:air:catalog.example.com:skill-packages:safe-skill", "kind": "skill"})
	requireStatus(t, resourceResult, http.StatusCreated)
	var resource catalog.Resource
	decodeResult(t, resourceResult, &resource)

	archive := httpSkillPackageZIP(t)
	versionResult := requestJSON(t, testServer.URL, http.MethodPost, "/api/v1/resources/"+resource.ID+"/versions", "admin", "skill-package-version", "", map[string]any{
		"version": "1.0.0",
		"representations": []map[string]any{{
			"role":        "primary",
			"mediaType":   adapter.MediaSkillZIP,
			"packageData": archive,
			"handoff": map[string]any{
				"git": map[string]any{"repository": "https://git.example.com/team/skills.git", "commit": strings.Repeat("a", 40), "directory": "skills/safe-skill"},
				"oci": map[string]any{"reference": "registry.example.com/team/safe-skill@sha256:" + strings.Repeat("b", 64)},
			},
		}},
	})
	requireStatus(t, versionResult, http.StatusCreated)
	requireControlSchema(t, "VersionEvidenceEnvelope", versionResult.body)
	var envelope struct {
		Revision catalog.VersionRevision `json:"revision"`
		Evidence catalog.EvidenceSet     `json:"evidence"`
	}
	decodeResult(t, versionResult, &envelope)
	if envelope.Evidence.State != catalog.EvidenceComplete || len(envelope.Revision.Representations) != 1 {
		t.Fatalf("package response = %#v / %#v", envelope.Revision, envelope.Evidence)
	}
	representation := envelope.Revision.Representations[0]
	if representation.Package == nil || representation.Package.StructureState != catalog.PackageAssessmentPassed || representation.Package.RootDirectory != "safe-skill" || representation.Package.FileCount != 2 || representation.Handoff == nil || representation.Handoff.Git == nil || representation.Handoff.OCI == nil || bytes.Contains(versionResult.body, []byte(`"packageData"`)) || bytes.Contains(versionResult.body, []byte("must-not-run")) {
		t.Fatalf("package representation = %#v, body=%s", representation, versionResult.body)
	}

	getResult := requestJSON(t, testServer.URL, http.MethodGet, "/api/v1/version-revisions/"+envelope.Revision.ID, "admin", "", "", nil)
	requireStatus(t, getResult, http.StatusOK)
	requireControlSchema(t, "RevisionEvidenceEnvelope", getResult.body)
	if !bytes.Contains(getResult.body, []byte(`"reference":"registry.example.com/team/safe-skill@sha256:`)) {
		t.Fatalf("immutable OCI handoff missing from revision read: %s", getResult.body)
	}

	invalidBase64 := requestWithHeaders(t, testServer.URL, http.MethodPost, "/api/v1/resources/"+resource.ID+"/versions", []byte(`{"version":"2.0.0","representations":[{"role":"primary","mediaType":"application/agent-skills+zip","packageData":"***","handoff":{"oci":{"reference":"registry.example.com/team/safe-skill@sha256:`+strings.Repeat("b", 64)+`"}}}]}`), map[string]string{"Authorization": "Bearer admin", "Content-Type": "application/json", "Idempotency-Key": "skill-package-invalid-base64"})
	requireStatus(t, invalidBase64, http.StatusBadRequest)
}

func httpSkillPackageZIP(t *testing.T) []byte {
	t.Helper()
	var buffer bytes.Buffer
	writer := zip.NewWriter(&buffer)
	files := map[string]string{
		"safe-skill/SKILL.md":       "---\nname: safe-skill\ndescription: A package exercised through the HTTP contract.\n---\n# Instructions\n",
		"safe-skill/scripts/run.sh": "#!/bin/sh\necho must-not-run\n",
	}
	for _, name := range []string{"safe-skill/SKILL.md", "safe-skill/scripts/run.sh"} {
		file, err := writer.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := file.Write([]byte(files[name])); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

func requestJSON(t *testing.T, baseURL, method, path, token, key, ifMatch string, value any) httpResult {
	t.Helper()
	var body []byte
	if value != nil {
		var err error
		body, err = json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
	}
	headers := map[string]string{}
	if token != "" {
		headers["Authorization"] = "Bearer " + token
	}
	if key != "" {
		headers["Idempotency-Key"] = key
	}
	if ifMatch != "" {
		headers["If-Match"] = ifMatch
	}
	if value != nil {
		headers["Content-Type"] = "application/json"
	}
	return requestWithHeaders(t, baseURL, method, path, body, headers)
}

func requestWithHeaders(t *testing.T, baseURL, method, path string, body []byte, headers map[string]string) httpResult {
	t.Helper()
	request, err := http.NewRequestWithContext(context.Background(), method, baseURL+path, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	for name, value := range headers {
		request.Header.Set(name, value)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = response.Body.Close() }()
	raw, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	return httpResult{status: response.StatusCode, header: response.Header.Clone(), body: raw}
}

func requireStatus(t *testing.T, result httpResult, status int) {
	t.Helper()
	if result.status != status {
		t.Fatalf("status = %d, want %d; body = %s", result.status, status, result.body)
	}
}

func decodeResult(t *testing.T, result httpResult, target any) {
	t.Helper()
	if err := json.Unmarshal(result.body, target); err != nil {
		t.Fatalf("decode response: %v; body = %s", err, result.body)
	}
}

func federationHTTPResult(urlValue, mediaType, body string) fetcher.Result {
	sum := sha256.Sum256([]byte(body))
	return fetcher.Result{Status: http.StatusOK, FinalURL: urlValue, MediaType: mediaType, Body: []byte(body), BodyDigest: "sha256:" + hex.EncodeToString(sum[:]), PolicyID: "http-test", PolicyRevision: 1, PolicyDigest: "sha256:" + strings.Repeat("a", 64)}
}

func jsonNumber(value int64) string {
	return strconv.FormatInt(value, 10)
}
