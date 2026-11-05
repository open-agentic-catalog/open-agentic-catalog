// Projection fixtures use existing domain ports without starting listeners or
// constructing the application graph. Unexpected port calls panic.
package httpserver

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/audit"
	"github.com/open-agentic-catalog/open-agentic-catalog/test/identityfixture"
	"log/slog"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/clock"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/id"
)

const projectionTenant = "00000000-0000-7000-8000-000000000001"

const projectionItem = "00000000-0000-7000-8000-000000000010"
const projectionNext = "00000000-0000-7000-8000-000000000011"
const projectionResource = "00000000-0000-7000-8000-000000000020"

var projectionTime = time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)

type projectionRepository struct{ catalog.Repository }
type projectionValidator struct {
	catalog.RepresentationValidator
}
type projectionProvider struct{ catalog.SemanticProvider }

type projectionSemanticStore struct {
	catalog.SemanticSearchStore
	status  func(context.Context, string, string, int) (catalog.SemanticIndexStatus, error)
	current func(context.Context, string, string) (string, error)
}

func (s projectionSemanticStore) Status(ctx context.Context, tenant, profile string, dimensions int) (catalog.SemanticIndexStatus, error) {
	return s.status(ctx, tenant, profile, dimensions)
}

func (s projectionSemanticStore) Current(ctx context.Context, tenant, scope string) (string, error) {
	return s.current(ctx, tenant, scope)
}

// projectionCatalog constructs only the service whose pure decisions the
// handler uses; its unused persistence and validation ports cannot perform work.
func projectionCatalog(t *testing.T, repo catalog.Repository, semantic *catalog.SemanticSearch) *catalog.Service {
	t.Helper()
	if repo == nil {
		repo = projectionRepository{}
	}
	service, err := catalog.NewService(catalog.Options{Repository: repo, Clock: clock.NewFake(projectionTime), IDs: id.Generator{}, Validator: projectionValidator{}, CursorKey: make([]byte, 32), SemanticSearch: semantic})
	if err != nil {
		t.Fatal(err)
	}
	return service
}

func projectionSemantic(t *testing.T, store projectionSemanticStore, state func() string, excluded []string) *catalog.SemanticSearch {
	t.Helper()
	semantic, err := catalog.NewSemanticSearch(catalog.SemanticSearchOptions{Store: store, Provider: projectionProvider{}, ProviderState: state, Profile: "sha256:" + strings.Repeat("a", 64), RankingProfile: "sha256:" + strings.Repeat("b", 64), Dimensions: 2, Cutoff: 0.5, Timeout: time.Second, ExcludedTenants: excluded})
	if err != nil {
		t.Fatal(err)
	}
	return semantic
}

func projectionActor(role identity.Role) identity.Actor {
	return identity.Actor{TenantID: projectionTenant, Subject: "projection-user", MembershipID: "00000000-0000-7000-8000-000000000002", RealmID: "00000000-0000-7000-8000-000000000003", ActorType: identity.ActorHuman, Roles: []identity.Role{role}}
}

func projectionServer(service *catalog.Service) *Server {
	return &Server{catalog: service, maxBodyBytes: 1024, logger: slog.New(slog.DiscardHandler), cursorKey: []byte(strings.Repeat("k", 32)), now: func() time.Time { return projectionTime }}
}

// requireProjectionProblem checks the handler's public error projection without
// asserting middleware authentication, request IDs or command replay behavior.
func requireProjectionProblem(t *testing.T, response *httptest.ResponseRecorder, status int, code catalog.ErrorCode) {
	t.Helper()
	var problem Problem
	if err := json.Unmarshal(response.Body.Bytes(), &problem); err != nil {
		t.Fatal(err)
	}
	if response.Code != status || problem.Status != status || problem.Code != code || response.Header().Get("Content-Type") != "application/problem+json" || response.Header().Get("Cache-Control") != "no-store" || strings.Contains(response.Body.String(), "private-fixture") {
		t.Fatalf("problem status=%d headers=%v body=%s", response.Code, response.Header(), response.Body.String())
	}
}

// A continuation carries the current policy revision rather than role arrays.
func fixtureDecisionDigest(tb testing.TB, ctx context.Context, actor identity.Actor) string {
	tb.Helper()
	digest, err := identity.CurrentDecisionDigest(identityfixture.WithActor(ctx, actor))
	if err != nil {
		tb.Fatal(err)
	}
	return digest
}

// Permission collectors and writer markers may derive a context. Preserve the
// actor, cancellation, request correlation and execution attribution assertions.
func sameProjectionContext(got, want context.Context) bool {
	ga, gok := identity.FromContext(got)
	wa, wok := identity.FromContext(want)
	return gok == wok && reflect.DeepEqual(identity.BindPermissionStore(ga, nil), identity.BindPermissionStore(wa, nil)) && got.Done() == want.Done() && errors.Is(got.Err(), want.Err()) && got.Value(requestIDKey{}) == want.Value(requestIDKey{}) && reflect.DeepEqual(audit.ExecutionFrom(got), audit.ExecutionFrom(want))
}
