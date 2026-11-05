//go:build functional

// Functional tests exercise local HTTP or composed service workflows.
package httpserver

import (
	"bytes"
	"crypto/sha256"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/clock"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/id"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/publication"
	"github.com/open-agentic-catalog/open-agentic-catalog/test/identityfixture"
)

func TestDiscoveryHostIsolationAndAuthentication(t *testing.T) {
	const tenantOne = "00000000-0000-7000-8000-000000000001"
	const tenantTwo = "00000000-0000-7000-8000-000000000002"
	publicManager := activeEmptyDiscoveryManager(t, tenantOne, "public.example.com", "public.example.com", publication.AudienceAnonymous)
	privateManager := activeEmptyDiscoveryManager(t, tenantOne, "private.example.com", "private.example.com", publication.AudienceAuthenticated)
	otherManager := activeEmptyDiscoveryManager(t, tenantTwo, "other.example.com", "other.example.com", publication.AudienceAnonymous)
	server := &Server{
		authenticator: discoveryAuthenticator{
			"right":         {TenantID: tenantOne, Subject: "right", Claims: map[string]string{"scope:catalog.read": "true"}},
			"missing-scope": {TenantID: tenantOne, Subject: "missing-scope"},
			"wrong-tenant":  {TenantID: tenantTwo, Subject: "wrong", Claims: map[string]string{"scope:catalog.read": "true"}},
		},
		discovery: map[string]DiscoveryHost{
			"public.example.com":  {Host: "public.example.com", TenantID: tenantOne, Mode: DiscoveryAnonymous, PublisherDomain: "public.example.com", Publication: publicManager},
			"private.example.com": {Host: "private.example.com", TenantID: tenantOne, Mode: DiscoveryAuthenticated, PublisherDomain: "private.example.com", Publication: privateManager},
			"other.example.com":   {Host: "other.example.com", TenantID: tenantTwo, Mode: DiscoveryAnonymous, PublisherDomain: "other.example.com", Publication: otherManager},
			"disabled.example.com": {Host: "disabled.example.com", TenantID: tenantOne, Mode: DiscoveryDisabled,
				PublisherDomain: "disabled.example.com"},
		},
		enforceOAuthScopes: true,
		logger:             slog.New(slog.DiscardHandler),
	}

	authenticator, ok := server.authenticator.(discoveryAuthenticator)
	if !ok {
		t.Fatal("unexpected authenticator")
	}
	for token, a := range authenticator {
		a.ActorType = identity.ActorHuman
		a.Roles = []identity.Role{identity.RoleReader}
		a.MembershipID = "00000000-0000-7000-8000-000000000501"
		a.RealmID = "00000000-0000-7000-8000-000000000502"
		bound, _ := identity.FromContext(identityfixture.WithActor(t.Context(), a))
		authenticator[token] = bound
	}
	request := func(host, token string, headers map[string]string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "http://"+host+"/.well-known/ard.json", nil)
		req.Host = host
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		for name, value := range headers {
			req.Header.Set(name, value)
		}
		response := httptest.NewRecorder()
		server.handleARDManifest(response, req)
		return response
	}

	public := request("PUBLIC.EXAMPLE.COM:443", "not-a-token", nil)
	if public.Code != http.StatusOK || public.Header().Get("Cache-Control") != "public, no-cache, no-transform" || public.Header().Get("Vary") != "Accept-Encoding" {
		t.Fatalf("anonymous discovery = status %d, headers %#v, body %s", public.Code, public.Header(), public.Body.String())
	}
	if !bytes.Contains(public.Body.Bytes(), []byte("urn:air:public.example.com:registry:open-agentic-catalog")) {
		t.Fatalf("anonymous host served another host's snapshot: %s", public.Body.String())
	}
	for name, response := range map[string]*httptest.ResponseRecorder{
		"unknown":      request("unknown.example.com", "", nil),
		"disabled":     request("disabled.example.com", "", nil),
		"trailing dot": request("public.example.com.", "", nil),
		"forwarded":    request("public.example.com", "", map[string]string{"Forwarded": "host=other.example.com"}),
		"forwarded host": request("public.example.com", "", map[string]string{
			"X-Forwarded-Host": "other.example.com",
		}),
		"original host": request("public.example.com", "", map[string]string{"X-Original-Host": "other.example.com"}),
	} {
		t.Run(name, func(t *testing.T) {
			if response.Code != http.StatusNotFound {
				t.Fatalf("host isolation response = %d, body %s", response.Code, response.Body.String())
			}
		})
	}
	absoluteRequest := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "http://other.example.com/.well-known/ard.json", nil)
	absoluteRequest.Host = "public.example.com"
	absoluteResponse := httptest.NewRecorder()
	server.handleARDManifest(absoluteResponse, absoluteRequest)
	if absoluteResponse.Code != http.StatusNotFound {
		t.Fatalf("conflicting absolute request target = %d, body %s", absoluteResponse.Code, absoluteResponse.Body.String())
	}
	missing := request("private.example.com", "", nil)
	if missing.Code != http.StatusUnauthorized || missing.Header().Get("WWW-Authenticate") == "" {
		t.Fatalf("missing private credential = status %d, headers %#v", missing.Code, missing.Header())
	}
	if invalid := request("private.example.com", "invalid", nil); invalid.Code != http.StatusUnauthorized {
		t.Fatalf("invalid private credential = %d", invalid.Code)
	}
	if wrong := request("private.example.com", "wrong-tenant", nil); wrong.Code != http.StatusForbidden {
		t.Fatalf("cross-tenant private credential = %d", wrong.Code)
	}
	if missingScope := request("private.example.com", "missing-scope", nil); missingScope.Code != http.StatusForbidden || missingScope.Header().Get("WWW-Authenticate") == "" {
		t.Fatalf("missing scope ceiling = status %d, headers %#v", missingScope.Code, missingScope.Header())
	}
	private := request("private.example.com", "right", nil)
	if private.Code != http.StatusOK || private.Header().Get("Cache-Control") != "private, no-cache, no-transform" || private.Header().Get("Vary") != "Accept-Encoding, Authorization" {
		t.Fatalf("authenticated discovery = status %d, headers %#v, body %s", private.Code, private.Header(), private.Body.String())
	}
	if other := request("other.example.com", "", nil); other.Code != http.StatusOK || !bytes.Contains(other.Body.Bytes(), []byte("urn:air:other.example.com:registry:open-agentic-catalog")) {
		t.Fatalf("second tenant discovery = status %d, body %s", other.Code, other.Body.String())
	}

	header := make(http.Header)
	header.Set("Vary", "Origin")
	addVary(header, "Authorization", "origin")
	if header.Get("Vary") != "Authorization, Origin" {
		t.Fatalf("Vary merge = %q", header.Get("Vary"))
	}
}

func activeEmptyDiscoveryManager(t *testing.T, tenantID, host, publisherDomain string, audience publication.Audience) *publication.Manager {
	t.Helper()
	serviceClock := clock.NewFake(time.Date(2026, time.August, 31, 12, 0, 0, 0, time.UTC))
	cursorKey := sha256.Sum256([]byte(host))
	manager, err := publication.NewManager(publication.Options{Records: fixedPublicationRecordSource{tenantID: tenantID}, Store: publication.NewMemoryStore(), Clock: serviceClock, IDs: id.Generator{}, BaseURL: "https://" + host, PublisherDomain: publisherDomain, ARDProfile: "ard-test", AICatalogProfile: "ai-test", CursorKey: cursorKey[:], Host: host, Audience: audience})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := manager.BuildCandidate(publicationFixtureContext(t, tenantID), tenantID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Activate(publicationFixtureContext(t, tenantID), tenantID, snapshot.ID, "Activate reviewed snapshot"); err != nil {
		t.Fatal(err)
	}
	return manager
}
