//go:build functional

// Functional tests retain real local HTTP and TLS exchanges.
package clientcompat_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/getkin/kin-openapi/openapi3filter"
	"github.com/getkin/kin-openapi/routers/gorillamux"
	"github.com/open-agentic-catalog/open-agentic-catalog/api"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/adapter"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/federation"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/feedback"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/jobs"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/authn"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/clock"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/config"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/fetcher"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/httpserver"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/id"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/idempotency"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/ratings"
	client "github.com/open-agentic-catalog/open-agentic-catalog/test/clientcompat/go/generated"
)

type noFetch struct{}

func (noFetch) Fetch(context.Context, fetcher.Request) (fetcher.Result, error) {
	return fetcher.Result{}, errors.New("fixture has no outbound content")
}

func (noFetch) PolicyDigest() string {
	return "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
}

type fixture struct {
	t          *testing.T
	service    *catalog.Service
	jobStore   *jobs.MemoryStore
	auth       authn.Authenticator
	identity   *identity.Service
	federation *federation.Manager
	jobs       *jobs.Service
	server     *httptest.Server
	document   *openapi3.T
	now        time.Time
}

// newFixture uses real authentication, authorization and audited memory services,
// with no running worker, external fetch, database or process-global test data.
func newFixture(t *testing.T) *fixture {
	return newFixtureWithFetcher(t, nil)
}

func newFixtureWithFetcher(t *testing.T, external catalog.ExternalRepresentationFetcher) *fixture {
	t.Helper()
	ctx := context.Background()
	now := time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)
	c := clock.NewFake(now)
	key := sha256.Sum256([]byte("client-compatibility-test"))
	users := map[string]config.DevelopmentUser{
		"admin":    {Subject: "admin", TenantID: tenant, Roles: []identity.Role{identity.RoleTenantAdmin, identity.RoleContributor, identity.RolePublisher, identity.RoleSourceAdmin, identity.RoleReader}},
		"reviewer": {Subject: "reviewer", TenantID: tenant, Roles: []identity.Role{identity.RoleReviewer, identity.RoleReader}},
		"reader":   {Subject: "reader", TenantID: tenant, Roles: []identity.Role{identity.RoleReader}},
		"other":    {Subject: "other", TenantID: otherTenant, Roles: []identity.Role{identity.RoleReader}},
	}
	if external != nil {
		users["scoped"] = config.DevelopmentUser{Subject: "scoped", TenantID: tenant}
	}
	seeds := make([]identity.Seed, 0, len(users))
	for _, u := range users {
		seeds = append(seeds, identity.Seed{TenantID: u.TenantID, Issuer: authn.DevelopmentIssuer, Subject: u.Subject, ActorType: identity.ActorHuman, Roles: u.Roles})
	}
	is, err := identity.NewMemoryStore(seeds)
	must(t, err)
	ids, err := identity.NewService(identity.ServiceOptions{Store: is, Clock: c, IDs: id.Generator{}, CursorKey: key[:]})
	must(t, err)
	auth, err := authn.New(ctx, config.Auth{Mode: "development", DevelopmentTokens: users}, is)
	must(t, err)
	repo := catalog.NewMemoryRepository()
	service, err := catalog.NewService(catalog.Options{Repository: repo, Clock: c, IDs: id.Generator{}, Validator: adapter.NewRegistry(), CursorKey: key[:], Policy: "client-compatibility", ExternalFetcher: external})
	must(t, err)
	federationService, err := federation.NewManager(federation.NewMemoryStore(), noFetch{}, c, id.Generator{})
	must(t, err)
	store := jobs.NewMemoryStore(repo)
	jobService, err := jobs.NewService(store, c, id.Generator{}, jobs.WithAuditEvidence())
	must(t, err)
	feedbackService, err := feedback.NewService(feedback.NewMemoryStore(repo, repo), repo, c, id.Generator{})
	must(t, err)
	h, err := httpserver.New(httpserver.Dependencies{RatingsStore: ratings.NewMemoryStore(repo), Feedback: feedbackService, Catalog: service, Identity: ids, Federation: federationService, Jobs: jobService, Authenticator: auth, Idempotency: idempotency.NewMemoryStore(), Logger: slog.New(slog.DiscardHandler), MaxBodyBytes: responseLimit, RequestTimeout: time.Second, CursorKey: key[:], Now: c.Now})
	must(t, err)
	server := httptest.NewServer(h.Handler())
	t.Cleanup(server.Close)
	document, err := openapi3.NewLoader().LoadFromData(api.ControlV1)
	must(t, err)
	document.Servers = nil // The test origin varies; operation schemas stay unchanged.
	return &fixture{t: t, service: service, jobStore: store, auth: auth, identity: ids, federation: federationService, jobs: jobService, server: server, document: document, now: now}
}

func (f *fixture) actor(token string) context.Context {
	actor, err := f.auth.Authenticate(context.Background(), token)
	must(f.t, err)
	return identity.WithActor(context.Background(), actor)
}

func (f *fixture) client(token string) *client.Client {
	f.t.Helper()
	hc := explicitHTTPClient()
	f.t.Cleanup(hc.CloseIdleConnections)
	router, err := gorillamux.NewRouter(f.document)
	must(f.t, err)
	// This wrapper validates the actual generated exchange separately from its
	// typed parser. Authentication is performed by the real Catalog handler.
	checked := doerFunc(func(request *http.Request) (*http.Response, error) {
		route, params, err := router.FindRoute(request)
		if err != nil {
			return nil, err
		}
		input := &openapi3filter.RequestValidationInput{Request: request, PathParams: params, Route: route, Options: &openapi3filter.Options{AuthenticationFunc: openapi3filter.NoopAuthenticationFunc}}
		if err := openapi3filter.ValidateRequest(request.Context(), input); err != nil {
			return nil, err
		}
		response, err := (boundedDoer{hc, responseLimit}).Do(request)
		if err != nil {
			return nil, err
		}
		data, err := io.ReadAll(response.Body)
		_ = response.Body.Close()
		if err != nil {
			return nil, err
		}
		output := &openapi3filter.ResponseValidationInput{RequestValidationInput: input, Status: response.StatusCode, Header: response.Header, Body: io.NopCloser(bytes.NewReader(data)), Options: &openapi3filter.Options{IncludeResponseStatus: true}}
		if err := openapi3filter.ValidateResponse(request.Context(), output); err != nil {
			return nil, err
		}
		response.Body = io.NopCloser(bytes.NewReader(data))
		return response, nil
	})
	c, err := client.NewClient(f.server.URL, client.WithHTTPClient(checked), client.WithRequestEditorFn(func(_ context.Context, r *http.Request) error {
		r.Header.Set("Authorization", "Bearer "+token)
		return nil
	}))
	must(f.t, err)
	return c
}

func (f *fixture) result(response *http.Response, err error, status int) []byte {
	f.t.Helper()
	must(f.t, err)
	data, readErr := io.ReadAll(response.Body)
	closeErr := response.Body.Close()
	must(f.t, readErr)
	must(f.t, closeErr)
	if response.StatusCode != status {
		f.t.Fatalf("expected %d, received %d: %s", status, response.StatusCode, data)
	}
	if response.Header.Get("X-Request-ID") == "" {
		f.t.Fatal("missing correlation header")
	}
	if status == http.StatusNoContent {
		if len(data) != 0 {
			f.t.Fatal("204 response has a body")
		}
	} else if status >= 200 && status < 300 {
		must(f.t, expectedJSON(response, data, status))
	}
	return data
}
