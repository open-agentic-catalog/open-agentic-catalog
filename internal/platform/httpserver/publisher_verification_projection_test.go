package httpserver

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/adapter"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/clock"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/id"
	"github.com/open-agentic-catalog/open-agentic-catalog/test/identityfixture"
)

// A static repository port isolates each handler projection. No application
// workflow, listener, database or real DNS resolver runs in these units.
type publisherVerificationProjection struct {
	catalog.Repository
	publisher        catalog.Publisher
	state            catalog.PublisherDomainVerification
	readErr, saveErr error
	writes           int
}

func (r *publisherVerificationProjection) ReadPublisherDomainVerification(_ context.Context, tenant, publisher, domain string) (catalog.Publisher, catalog.PublisherDomainVerification, error) {
	if tenant != r.publisher.TenantID || publisher != r.publisher.ID || domain != r.state.Domain {
		return catalog.Publisher{}, catalog.PublisherDomainVerification{}, catalog.E(catalog.CodeNotFound, "unbound unit target")
	}
	return r.publisher, r.state, r.readErr
}
func (r *publisherVerificationProjection) SavePublisherDomainVerification(_ context.Context, p catalog.PublisherDomainMutation) (catalog.PublisherDomainVerification, error) {
	r.writes++
	if p.State.TenantID != r.publisher.TenantID || p.State.PublisherID != r.publisher.ID || p.State.Domain != r.state.Domain || p.Expected != r.state.Version(r.publisher) || p.Audit.TargetID != r.publisher.ID {
		return catalog.PublisherDomainVerification{}, catalog.E(catalog.CodeInternal, "lost mutation binding")
	}
	return p.State, r.saveErr
}

type publisherProjectionTXT struct {
	value string
	calls int
}

func (r *publisherProjectionTXT) LookupTXT(context.Context, string) ([]string, error) {
	r.calls++
	return []string{r.value}, nil
}

func TestPublisherVerificationHandlerProjections(t *testing.T) {
	t.Parallel()
	for _, action := range []string{"status", "challenge", "issue", "check", "clear"} {
		for _, outcome := range []string{"ok", "read-error", "disabled", "save-error", "bad-body", "stale"} {
			if (action == "status" || action == "challenge") && (outcome == "save-error" || outcome == "bad-body" || outcome == "stale") {
				continue
			}
			t.Run(action+"/"+outcome, func(t *testing.T) {
				now := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
				actor := projectionActor(identity.RoleTenantAdmin)
				repo := &publisherVerificationProjection{publisher: catalog.Publisher{ID: projectionItem, TenantID: actor.TenantID, Revision: 2, Domains: []string{"example.test"}}, state: catalog.PublisherDomainVerification{ID: projectionNext, TenantID: actor.TenantID, PublisherID: projectionItem, Domain: "example.test", Revision: 3, ChallengeID: projectionNext, Nonce: strings.Repeat("A", 43), ChallengeIssuedAt: now.Add(-time.Hour), ChallengeExpiresAt: now.Add(time.Hour), CreatedAt: now.Add(-time.Hour), UpdatedAt: now.Add(-time.Hour)}}
				policy := catalog.DefaultPublisherDomainPolicy()
				policy.Mode = catalog.DomainVerificationOptional
				if outcome == "disabled" {
					policy.Mode = catalog.DomainVerificationDisabled
				}
				resolver := &publisherProjectionTXT{value: catalog.DomainTXTValuePrefix + repo.state.Nonce}
				service, err := catalog.NewService(catalog.Options{Repository: repo, Clock: clock.NewFake(now), IDs: id.Generator{}, Validator: adapter.NewRegistry(), CursorKey: []byte("01234567890123456789012345678901"), PublisherDomainPolicy: policy, PublisherTXT: resolver})
				if err != nil {
					t.Fatal(err)
				}
				server := projectionServer(service)
				ctx := identityfixture.WithActor(t.Context(), actor)
				current, err := service.GetPublisherDomainChallenge(ctx, projectionItem, "example.test")
				if err != nil {
					t.Fatal(err)
				}
				tag, err := publisherDomainTag(actor.TenantID, projectionItem, current.Status, policy)
				if err != nil {
					t.Fatal(err)
				}
				if outcome == "read-error" {
					repo.readErr = catalog.E(catalog.CodeDependencyUnavailable, "owned read failure")
				}
				if outcome == "save-error" {
					repo.saveErr = catalog.E(catalog.CodeDependencyUnavailable, "owned write failure")
				}
				body := `{"reason":"Check projection"}`
				if outcome == "bad-body" {
					body = `{"reason":null}`
				}
				if outcome == "stale" {
					tag = `"old"`
				}
				r := httptest.NewRequestWithContext(ctx, http.MethodPost, "/", strings.NewReader(body))
				r.Header.Set("Content-Type", "application/json")
				r.Header.Set("If-Match", tag)
				r.SetPathValue("publisherId", projectionItem)
				r.SetPathValue("domain", "example.test")
				w := httptest.NewRecorder()
				handlers := map[string]http.HandlerFunc{"status": server.handlePublisherDomainStatus, "challenge": server.handlePublisherDomainChallenge, "issue": server.handlePublisherDomainIssue, "check": server.handlePublisherDomainCheck, "clear": server.handlePublisherDomainClear}
				handlers[action](w, r)
				want := 200
				switch {
				case outcome == "read-error" || outcome == "save-error":
					want = 503
				case outcome == "disabled" && action != "status":
					want = 404
				case outcome == "bad-body":
					want = 400
				case outcome == "stale":
					want = 412
				}
				if w.Code != want {
					t.Fatalf("status=%d want=%d body=%s", w.Code, want, w.Body.String())
				}
				mutation := action == "issue" || action == "check" || action == "clear"
				expectedWrites := 0
				if mutation && (outcome == "ok" || outcome == "save-error") {
					expectedWrites = 1
				}
				if repo.writes != expectedWrites {
					t.Fatal("unexpected mutation count", repo.writes)
				}
				expectedLookups := 0
				if action == "check" && (outcome == "ok" || outcome == "save-error") {
					expectedLookups = 1
				}
				if resolver.calls != expectedLookups {
					t.Fatal("unexpected resolver port calls", resolver.calls)
				}
				if want == 200 {
					if w.Header().Get("Cache-Control") != "private, no-store" || w.Header().Get("ETag") == "" {
						t.Fatal("missing private revision headers")
					}
					if strings.Contains(w.Body.String(), `"version"`) {
						t.Fatal("native concurrency object escaped the HTTP contract")
					}
					instructions := action == "challenge" || action == "issue"
					if strings.Contains(w.Body.String(), `"value"`) != instructions {
						t.Fatal("incorrect private projection")
					}
					if action == "check" && !strings.Contains(w.Body.String(), `"lastCheckOutcome":"matched"`) {
						t.Fatal("check did not report exact match")
					}
				}
			})
		}
	}
}
