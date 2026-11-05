//go:build functional

package web

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/clock"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/httpserver"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/id"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/idempotency"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/jobs"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/config"
	platformruntime "github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/runtime"
)

// Exercise the new management workflows through the cookie host and real
// composed domain/API boundary, without a running developer service or DB.
func TestWorkspaceCommandsAgainstCatalog(t *testing.T) {
	runtime, err := platformruntime.Build(t.Context(), platformruntime.BuildOptions{ConfigPath: "../../../config/local.json", Role: config.RoleAll, Logger: slog.New(slog.DiscardHandler)})
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	// Own the Job store so malformed historical worker input can be seeded
	// without weakening runtime user admission or adding a runtime test hook.
	jobStore := jobs.NewMemoryStore()
	jobService, err := jobs.NewService(jobStore, clock.Real{}, id.Generator{})
	if err != nil {
		t.Fatal(err)
	}
	key := sha256.Sum256([]byte("ui-workspace-fixture"))
	backend, err := httpserver.New(httpserver.Dependencies{Catalog: runtime.Catalog, Identity: runtime.Identity, Federation: runtime.Federation, Jobs: jobService, Authenticator: runtime.Authenticator, Idempotency: idempotency.NewMemoryStore(), MaxBodyBytes: 1 << 20, RequestTimeout: time.Minute, CursorKey: key[:]})
	if err != nil {
		t.Fatal(err)
	}
	api := httptest.NewServer(backend.Handler())
	defer api.Close()
	c := fixtureConfig(t, api.URL)
	if err := os.WriteFile(c.DevelopmentUsersFile, []byte(`[{"alias":"admin","label":"Admin","token":"local-admin"},{"alias":"reader","label":"Reader","token":"local-reader"},{"alias":"contributor","label":"Contributor","token":"local-contributor"},{"alias":"other","label":"Other tenant","token":"other-tenant-admin"}]`), 0600); err != nil {
		t.Fatal(err)
	}
	s := newFixture(t, c)
	login := func(alias string) *http.Cookie {
		t.Helper()
		w := uiRequest(s, "POST", "/auth/login", `{"alias":"`+alias+`"}`)
		if w.Code != 200 || len(w.Result().Cookies()) != 1 {
			t.Fatalf("login %s: %d", alias, w.Code)
		}
		return w.Result().Cookies()[0]
	}
	admin, reader, contributor, other := login("admin"), login("reader"), login("contributor"), login("other")
	request := func(cookie *http.Cookie, method, path, body, key, etag string, want int) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequestWithContext(t.Context(), method, c.Origin+"/_catalog"+path, strings.NewReader(body))
		r.Header.Set("Origin", c.Origin)
		r.Header.Set("X-Catalog-Request", "1")
		r.Header.Set("Content-Type", "application/json")
		if key != "" {
			r.Header.Set("Idempotency-Key", key)
		}
		if etag != "" {
			r.Header.Set("If-Match", etag)
		}
		r.AddCookie(cookie)
		w := httptest.NewRecorder()
		s.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("%s %s: got %d, want %d: %s", method, path, w.Code, want, w.Body.String())
		}
		return w
	}
	decode := func(w *httptest.ResponseRecorder, target any) {
		t.Helper()
		if err := json.Unmarshal(w.Body.Bytes(), target); err != nil {
			t.Fatal(err)
		}
	}
	for _, path := range []string{"/api/v1/namespaces?limit=1", "/api/v1/publishers?limit=1", "/api/v1/capabilities", "/version"} {
		request(reader, "GET", path, "", "", "", 200)
	}
	request(admin, "GET", "/api/v1/jobs?limit=1", "", "", "", 200)
	jobPage := request(reader, "GET", "/api/v1/jobs?limit=1", "", "", "", 200)
	var readerJobs struct {
		Items      []jobs.View `json:"items"`
		NextCursor string      `json:"nextCursor"`
	}
	decode(jobPage, &readerJobs)
	if len(readerJobs.Items) != 0 || readerJobs.NextCursor != "" {
		t.Fatal("Reader disclosed private Job families")
	}
	const namespaceBody = `{"slug":"ui-test","displayName":"UI test","description":"Synthetic workspace"}`
	request(reader, "POST", "/api/v1/namespaces", namespaceBody, "reader-create", "", 403)
	created := request(admin, "POST", "/api/v1/namespaces", namespaceBody, "namespace-create", "", 201)
	var namespace catalog.Namespace
	decode(created, &namespace)
	replayed := request(admin, "POST", "/api/v1/namespaces", namespaceBody, "namespace-create", "", 201)
	if created.Body.String() != replayed.Body.String() {
		t.Fatal("namespace replay changed result")
	}
	namespacePath := "/api/v1/namespaces/" + namespace.ID
	request(other, "GET", namespacePath, "", "", "", 404)
	detail := request(reader, "GET", namespacePath, "", "", "", 200)
	if detail.Header().Get("ETag") != created.Header().Get("ETag") {
		t.Fatal("namespace validator not forwarded")
	}
	const patch = `{"displayName":"Updated UI test","reason":"Verify UI update"}`
	request(admin, "PATCH", namespacePath, patch, "namespace-update", detail.Header().Get("ETag"), 200)
	request(admin, "PATCH", namespacePath, patch, "namespace-stale", detail.Header().Get("ETag"), 412)
	request(reader, "PATCH", namespacePath, patch, "namespace-denied", detail.Header().Get("ETag"), 403)
	request(admin, "POST", "/api/v1/namespaces", `{"slug":"ui-test-two","displayName":"Second"}`, "namespace-two", "", 201)
	var page struct {
		Items      []catalog.Namespace `json:"items"`
		NextCursor string              `json:"nextCursor"`
	}
	decode(request(admin, "GET", "/api/v1/namespaces?limit=1", "", "", "", 200), &page)
	if len(page.Items) != 1 || page.NextCursor == "" {
		t.Fatal("missing bounded namespace continuation")
	}
	request(admin, "GET", "/api/v1/namespaces?limit=1&cursor="+page.NextCursor, "", "", "", 200)
	const publisherBody = `{"displayName":"UI publisher","domains":["ui.example.com"]}`
	created = request(contributor, "POST", "/api/v1/publishers", publisherBody, "publisher-create", "", 201)
	var publisher catalog.Publisher
	decode(created, &publisher)
	publisherPath := "/api/v1/publishers/" + publisher.ID
	request(contributor, "GET", publisherPath, "", "", "", 200)
	request(other, "GET", publisherPath, "", "", "", 404)
	detail = request(reader, "GET", publisherPath, "", "", "", 200)
	updated := request(admin, "PATCH", publisherPath, `{"displayName":"Updated publisher","domains":["ui.example.com"],"reason":"Verify metadata update"}`, "publisher-update", detail.Header().Get("ETag"), 200)
	if !strings.Contains(updated.Body.String(), `"approvedPublicationDomains":[]`) {
		t.Fatal("publisher update must encode an empty approval array, not null")
	}
	if read := request(reader, "GET", publisherPath, "", "", "", 200); !strings.Contains(read.Body.String(), `"approvedPublicationDomains":[]`) {
		t.Fatal("publisher read must retain an empty approval array")
	}
	const reason = `{"reason":"Verify publication-domain lifecycle"}`
	domainPath := publisherPath + "/publication-domains/ui.example.com"
	request(reader, "POST", domainPath+":approve", reason, "domain-denied", updated.Header().Get("ETag"), 403)
	approved := request(admin, "POST", domainPath+":approve", reason, "domain-approve", updated.Header().Get("ETag"), 200)
	decode(approved, &publisher)
	if len(publisher.ApprovedPublicationDomains) != 1 {
		t.Fatal("domain not approved")
	}
	revoked := request(admin, "POST", domainPath+":revoke", reason, "domain-revoke", approved.Header().Get("ETag"), 200)
	var revocation catalog.PublicationDomainRevocation
	decode(revoked, &revocation)
	if len(revocation.Publisher.ApprovedPublicationDomains) != 0 || len(revocation.WithdrawnHosts) != 0 {
		t.Fatal("unexpected domain revocation result")
	}
	actor, err := runtime.Authenticator.Authenticate(t.Context(), "local-admin")
	if err != nil {
		t.Fatal(err)
	}
	job, _, err := jobService.Enqueue(identity.WithActor(t.Context(), actor), jobs.EnqueueInput{Kind: "publication.generate", Payload: json.RawMessage(`{}`), DeduplicationKey: "ui-job", AttemptLimit: 1})
	if err != nil {
		t.Fatal(err)
	}
	worker, err := jobs.NewWorker(jobStore, clock.Real{}, "ui-fixture", time.Minute, time.Second, time.Minute, map[string]jobs.Handler{"publication.generate": func(context.Context, jobs.Job, func(int) error) (any, error) {
		return nil, &jobs.HandlerError{Class: jobs.FailureInvalidInput, Safe: "Fixture generation failed."}
	}})
	if err != nil {
		t.Fatal(err)
	}
	if err := worker.RunOnce(t.Context()); err != nil {
		t.Fatal(err)
	}
	jobPath := "/api/v1/jobs/" + job.ID
	detail = request(admin, "GET", jobPath, "", "", "", 200)
	request(reader, "GET", jobPath, "", "", "", 403)
	var failed jobs.View
	decode(detail, &failed)
	if failed.State != jobs.StateDeadLetter {
		t.Fatalf("fixture job state: %s", failed.State)
	}
	request(other, "GET", jobPath, "", "", "", 404)
	request(reader, "POST", jobPath+":retry", reason, "job-denied", "", 403)
	retry := request(admin, "POST", jobPath+":retry", reason, "job-retry", "", 202)
	var next jobs.View
	decode(retry, &next)
	if next.ParentJobID != job.ID || next.State != jobs.StateQueued {
		t.Fatal("retry did not return a new queued job")
	}
	if replay := request(admin, "POST", jobPath+":retry", reason, "job-retry", "", 202); replay.Body.String() != retry.Body.String() {
		t.Fatal("retry replay created another job")
	}
	cancelled := request(admin, "POST", "/api/v1/jobs/"+next.ID+":cancel", reason, "job-cancel", "", 200)
	decode(cancelled, &next)
	if next.State != jobs.StateCancelled {
		t.Fatal("queued job was not cancelled")
	}
}
