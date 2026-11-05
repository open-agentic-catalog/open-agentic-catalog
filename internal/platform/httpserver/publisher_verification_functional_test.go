//go:build functional

package httpserver

import (
	"context"
	"crypto/sha256"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/adapter"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/clock"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/id"
)

type domainHTTPResolver func(context.Context, string) ([]string, error)

func (f domainHTTPResolver) LookupTXT(ctx context.Context, name string) ([]string, error) {
	return f(ctx, name)
}

func TestHTTPPublisherApprovalRequiresFreshProofAndPreservesReplay(t *testing.T) {
	t.Parallel()
	c := clock.NewFake(time.Date(2026, 8, 29, 11, 0, 0, 0, time.UTC))
	policy := catalog.DefaultPublisherDomainPolicy()
	policy.Mode = catalog.DomainVerificationRequired
	var service *catalog.Service
	var authenticate func(context.Context, string) (identity.Actor, error)
	var value string
	lookups := 0
	server := newConfiguredHTTPServer(t, func(deps *Dependencies) {
		key := sha256.Sum256([]byte("domain-http-proof"))
		var err error
		service, err = catalog.NewService(catalog.Options{Repository: catalog.NewMemoryRepository(), Clock: c, IDs: id.Generator{}, Validator: adapter.NewRegistry(), CursorKey: key[:], PublisherDomainPolicy: policy, PublisherTXT: domainHTTPResolver(func(context.Context, string) ([]string, error) { lookups++; return []string{value}, nil })})
		if err != nil {
			t.Fatal(err)
		}
		deps.Catalog = service
		authenticate = deps.Authenticator.Authenticate
	})
	defer server.Close()
	actor, err := authenticate(t.Context(), "admin")
	if err != nil {
		t.Fatal(err)
	}
	native := identity.WithActor(t.Context(), actor)
	created := requestJSON(t, server.URL, http.MethodPost, "/api/v1/publishers", "admin", "proof-create", "", map[string]any{"displayName": "DNS HTTP test", "domains": []string{"catalog.example.com"}})
	requireStatus(t, created, http.StatusCreated)
	var p catalog.Publisher
	decodeResult(t, created, &p)
	path := "/api/v1/publishers/" + p.ID + "/publication-domains/catalog.example.com:approve"
	body := map[string]any{"reason": "Approve domain control evidence"}
	etag := fmt.Sprintf(`"rev-%d"`, p.Revision)
	requireControlProblem(t, requestJSON(t, server.URL, http.MethodPost, path, "admin", "proof-missing", etag, body), http.StatusPreconditionFailed, catalog.CodePreconditionFailed)
	requireControlProblem(t, requestJSON(t, server.URL, http.MethodPost, path, "contributor", "proof-forbidden", etag, body), http.StatusForbidden, catalog.CodeForbidden)
	current, err := service.GetPublisherDomainVerification(native, p.ID, "catalog.example.com")
	if err != nil {
		t.Fatal(err)
	}
	challenge, err := service.IssuePublisherDomainChallenge(native, p.ID, "catalog.example.com", "Set local fixture value", current.Version)
	if err != nil {
		t.Fatal(err)
	}
	value = challenge.Value
	proof, err := service.CheckPublisherDomain(native, p.ID, "catalog.example.com", "Check owned fixture", challenge.Status.Version)
	if err != nil {
		t.Fatal(err)
	}
	approved := requestJSON(t, server.URL, http.MethodPost, path, "admin", "proof-approval", etag, body)
	requireStatus(t, approved, http.StatusOK)
	requireControlSchema(t, "Publisher", approved.body)
	if lookups != 1 {
		t.Fatal("approval queried DNS")
	}
	c.Advance(proof.ExpiresAt.Sub(c.Now()))
	replay := requestJSON(t, server.URL, http.MethodPost, path, "admin", "proof-approval", etag, body)
	requireStatus(t, replay, http.StatusOK)
	if string(replay.body) != string(approved.body) || lookups != 1 {
		t.Fatal("expired proof changed historical replay")
	}
	requireControlProblem(t, requestJSON(t, server.URL, http.MethodPost, path, "admin", "proof-expired", approved.header.Get("ETag"), body), http.StatusPreconditionFailed, catalog.CodePreconditionFailed)
	currentPublisher, err := service.GetPublisher(native, p.ID)
	if err != nil || len(currentPublisher.ApprovedPublicationDomains) != 1 {
		t.Fatal("proof expiry revoked approval", err)
	}
}
