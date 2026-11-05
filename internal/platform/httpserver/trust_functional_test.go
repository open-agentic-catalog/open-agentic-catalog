//go:build functional

package httpserver

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/adapter"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/audit"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/federation"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/jobs"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/clock"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/config"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/fetcher"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/id"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/idempotency"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/standards/anstrust"
)

type httpTrustPolicy struct {
	enabled       bool
	validationErr error
}

func (p *httpTrustPolicy) Enabled() bool { return p.enabled }
func (p *httpTrustPolicy) Policy(string) (string, string, bool) {
	return "sha256:" + strings.Repeat("b", 64), "owned", p.enabled
}
func (p *httpTrustPolicy) ValidateSource(context.Context, federation.Source) error {
	return p.validationErr
}
func (*httpTrustPolicy) Assess(context.Context, jobs.TrustPayload, func(context.Context) error, func() time.Time) (anstrust.Bundle, anstrust.Result, error) {
	panic("HTTP request must not acquire evidence")
}

func TestTrustHTTPAdmissionReplayAndPrivateRead(t *testing.T) {
	const tenant = "00000000-0000-7000-8000-000000000001"
	const sourceID = "00000000-0000-7000-8000-000000000010"
	clk := clock.NewFake(time.Date(2026, 10, 9, 14, 31, 24, 0, time.UTC))
	key := sha256.Sum256([]byte("trust HTTP fixture"))
	repo := catalog.NewMemoryRepository()
	cat, err := catalog.NewService(catalog.Options{Repository: repo, Clock: clk, IDs: id.Generator{}, Validator: adapter.NewRegistry(), CursorKey: key[:], Policy: "test"})
	if err != nil {
		t.Fatal(err)
	}
	raw := []byte(`{ "identifier":"urn:air:peer.example:agents:one", "type":"application/a2a-agent-card+json", "url":"https://peer.example/card", "score":100, "source":"https://peer.example/search" }`)
	source := federation.Source{ID: sourceID, TenantID: tenant, DisplayName: "Owned peer", Type: federation.SourceRegistry, Endpoint: "https://peer.example/search", RegistrySearchURL: "https://peer.example/search", Status: federation.SourceActive, ProfileID: "ard-0.91-aa3e598", RetrievalMode: federation.RetrievalLive, Scope: federation.Scope{Publishers: []string{"peer.example"}}, UpdateMode: federation.UpdateManual, Live: federation.LivePolicy{Enabled: true, QueryPrivacyAcknowledged: true, AnonymousDisclosureAcknowledged: true, FederationMode: "none"}, PolicyID: "test-policy", PolicyRevision: 1, PolicyDigest: "sha256:" + strings.Repeat("a", 64), Revision: 1, TrustVerificationPolicy: "owned", CreatedBy: "fixture", CreatedAt: clk.Now(), UpdatedAt: clk.Now()}
	source.DiscoveryAssignments = []federation.DiscoveryAssignment{{Host: "catalog.example.com", Audience: federation.AudienceAuthenticated}}
	sources := federation.NewMemoryStore(repo)
	if _, err = sources.CreateSource(t.Context(), source); err != nil {
		t.Fatal(err)
	}
	peer := &countingARDSearchFetcher{responses: routedFetcher{http.MethodPost + " " + source.Endpoint: fetcher.Result{Status: 200, MediaType: "application/json", FinalURL: source.Endpoint, Body: append(append([]byte(`{"results":[`), raw...), []byte("]}")...), PolicyID: source.PolicyID, PolicyRevision: 1, PolicyDigest: source.PolicyDigest}}}
	policy := &httpTrustPolicy{enabled: true}
	manager, err := federation.NewManager(sources, peer, clk, id.Generator{}, federation.WithTrustVerifier(policy), federation.WithEvidenceSink(repo, true))
	if err != nil {
		t.Fatal(err)
	}
	store := jobs.NewMemoryStore(repo)
	service, err := jobs.NewService(store, clk, id.Generator{}, jobs.WithAuditEvidence(), jobs.WithTargetValidation(manager.ValidateTrustJob), jobs.WithEnqueueValidation(manager.ValidateTrustEnqueue))
	if err != nil {
		t.Fatal(err)
	}
	auth, identities := newTestIdentity(t, clk, map[string]config.DevelopmentUser{"admin": {Subject: "admin", TenantID: tenant, Roles: []identity.Role{identity.RoleTenantAdmin}}, "reader": {Subject: "reader", TenantID: tenant, Roles: []identity.Role{identity.RoleReader}}, "other": {Subject: "admin", TenantID: "00000000-0000-7000-8000-000000000002", Roles: []identity.Role{identity.RoleTenantAdmin}}})
	server, err := New(Dependencies{Catalog: cat, Identity: identities, Federation: manager, Jobs: service, Authenticator: auth, Idempotency: idempotency.NewMemoryStore(), Logger: slog.New(slog.DiscardHandler), MaxBodyBytes: 1 << 20, RequestTimeout: time.Second, CursorKey: key[:], Now: clk.Now})
	if err != nil {
		t.Fatal(err)
	}
	call := func(method, path, token, body, key string) *httptest.ResponseRecorder {
		req := httptest.NewRequestWithContext(t.Context(), method, path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+token)
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		if key != "" {
			req.Header.Set("Idempotency-Key", key)
		}
		w := httptest.NewRecorder()
		server.Handler().ServeHTTP(w, req)
		return w
	}
	path := "/api/v1/federation/sources/" + sourceID + "/trust-assessments"
	request := federation.TrustRequest{SourceRevision: 1, Identifier: "urn:air:peer.example:agents:one", MediaType: "application/a2a-agent-card+json", Delivery: "url", ExactEntryDigest: audit.DigestBytes(raw), Text: "one", Reason: "Verify exact registration"}
	body, marshalErr := json.Marshal(request)
	if marshalErr != nil {
		t.Fatal(marshalErr)
	}
	first := call("POST", path, "admin", string(body), "trust-request-1")
	if first.Code != 202 {
		t.Fatalf("request %d: %s", first.Code, first.Body)
	}
	requireControlSchema(t, "Job", first.Body.Bytes())
	var job jobs.View
	if json.Unmarshal(first.Body.Bytes(), &job) != nil || job.ID == "" {
		t.Fatal("missing accepted Job")
	}
	if first.Header().Get("Location") != "/api/v1/jobs/"+job.ID {
		t.Fatal("accepted assessment did not identify its Job")
	}
	if peer.calls != 1 {
		t.Fatalf("selection calls=%d", peer.calls)
	}
	again := call("POST", path, "admin", string(body), "trust-request-1")
	if again.Code != 202 || again.Body.String() != first.Body.String() || peer.calls != 1 || again.Header().Get("Idempotency-Replayed") != "true" {
		t.Fatalf("replay=%d calls=%d", again.Code, peer.calls)
	}
	denied := call("POST", path, "reader", string(body), "trust-request-1")
	if denied.Code != 403 || peer.calls != 1 {
		t.Fatalf("denied replay=%d calls=%d", denied.Code, peer.calls)
	}
	for token, status := range map[string]int{"reader": http.StatusForbidden, "other": http.StatusNotFound} {
		w := call("GET", path+"/"+job.ID, token, "", "")
		if w.Code != status {
			t.Fatalf("private read leaked to %s: %d", token, w.Code)
		}
	}
	pending := call("GET", path+"/"+job.ID, "admin", "", "")
	if pending.Code != http.StatusConflict || !strings.Contains(pending.Body.String(), `"code":"INVALID_TRANSITION"`) {
		t.Fatalf("pending assessment returned wrong failure: %d %s", pending.Code, pending.Body)
	}
	policy.validationErr = fetcher.ErrCredentialChanged
	staleCredential := call("POST", path, "admin", string(body), "trust-stale-credential")
	if staleCredential.Code != http.StatusPreconditionFailed || !strings.Contains(staleCredential.Body.String(), `"code":"PRECONDITION_FAILED"`) || peer.calls != 1 {
		t.Fatalf("credential refusal=%d %s calls=%d", staleCredential.Code, staleCredential.Body, peer.calls)
	}
	policy.validationErr = nil
	generic := call("GET", "/api/v1/jobs/"+job.ID, "admin", "", "")
	if generic.Code != 200 || strings.Contains(generic.Body.String(), "entryDigest") {
		t.Fatal("generic Job leaked payload")
	}
	claimed, e := store.Claim(t.Context(), "owned-worker", clk.Now(), time.Minute)
	if e != nil {
		t.Fatal(e)
	}
	selected, e := jobs.DecodeTrustPayload(claimed)
	if e != nil {
		t.Fatal(e)
	}
	na := anstrust.Check{State: "not_assessed", Code: "prerequisite_failed"}
	assessment := jobs.TrustAssessment{SourceID: sourceID, SourceRevision: 1, PolicyName: selected.PolicyName, PolicyDigest: selected.PolicyDigest, Authority: "owned", ExactEntryDigest: selected.ExactEntryDigest, MediaType: selected.MediaType, Delivery: selected.Delivery, SoftwareTrust: "not_assessed", ArtifactIntegrity: "not_assessed", Rights: "not_assessed", Evidence: anstrust.Bundle{Profile: anstrust.Profile, Entry: raw, Receipt: []byte{}, Checkpoint: []byte{}, StatusToken: []byte{}}, Assessment: anstrust.Result{Profile: anstrust.Profile, PolicyDigest: strings.Repeat("b", 64), EntryDigest: strings.TrimPrefix(selected.ExactEntryDigest, "sha256:"), CheckedAt: clk.Now(), Receipt: anstrust.Check{State: "unavailable", Code: "receipt_missing"}, Producer: na, Log: na, Checkpoint: na, Inclusion: na, Target: na, Status: na}}
	resultRaw, marshalErr := json.Marshal(assessment)
	if marshalErr != nil {
		t.Fatal(marshalErr)
	}
	facts, _ := audit.Facts(map[string]any{"sourceId": sourceID, "sourceRevision": 1, "entryDigest": selected.ExactEntryDigest, "policyDigest": selected.PolicyDigest, "resultDigest": audit.DigestBytes(resultRaw), "currentRegistration": false, "jobId": job.ID, "jobKind": jobs.TrustKind, "jobAttempt": claimed.Attempt, "initiator": claimed.Actor})
	record, e := audit.NewRecord("00000000-0000-7000-8000-000000000031", tenant, audit.Principal{ID: audit.PrincipalIDCatalogWorker, Type: audit.PrincipalServiceExecutor}, audit.ActionSourceTrustCompleted, "job", job.ID, "", audit.Execution{}, facts, clk.Now())
	if e != nil {
		t.Fatal(e)
	}
	if _, e = store.Complete(t.Context(), tenant, job.ID, "owned-worker", clk.Now(), resultRaw, record); e != nil {
		t.Fatal(e)
	}
	done := call("GET", path+"/"+job.ID, "admin", "", "")
	requireControlSchema(t, "TrustAssessmentResponse", done.Body.Bytes())
	var private federation.TrustResult
	if done.Code != 200 || json.Unmarshal(done.Body.Bytes(), &private) != nil || private.Applicability != "negative_or_insufficient" || private.Current || string(private.Result.Evidence.Entry) != string(raw) {
		t.Fatalf("private result=%d %s", done.Code, done.Body)
	}
	generic = call("GET", "/api/v1/jobs/"+job.ID, "admin", "", "")
	if generic.Code != http.StatusOK || strings.Contains(generic.Body.String(), `"evidence"`) || strings.Contains(generic.Body.String(), `"assessment"`) || strings.Contains(generic.Body.String(), `"result"`) {
		t.Fatalf("completed generic Job exposed a private result: %d %s", generic.Code, generic.Body)
	}
	// Selection can succeed while durable Job storage is unavailable. The API
	// must not return a receipt or append request evidence for the refused write.
	enqueueCalls := 0
	unavailable, err := jobs.NewService(reindexProjectionStore{Store: store, enqueue: func(_ context.Context, candidate jobs.Job, evidence ...audit.Record) (jobs.Job, bool, error) {
		enqueueCalls++
		if candidate.TenantID != tenant || candidate.Kind != jobs.TrustKind || len(evidence) != 1 || evidence[0].Action != audit.ActionSourceTrustRequested {
			t.Fatal("storage refusal did not receive the admitted Job and bound evidence")
		}
		return jobs.Job{}, false, catalog.Wrap(catalog.CodeDependencyUnavailable, "Job storage unavailable", errors.New("private-trust-job-storage"))
	}}, clk, id.Generator{}, jobs.WithAuditEvidence(), jobs.WithTargetValidation(manager.ValidateTrustJob), jobs.WithEnqueueValidation(manager.ValidateTrustEnqueue))
	if err != nil {
		t.Fatal(err)
	}
	server.jobs = unavailable
	failed := call("POST", path, "admin", string(body), "trust-job-storage-unavailable")
	if failed.Code != http.StatusServiceUnavailable || !strings.Contains(failed.Body.String(), `"code":"DEPENDENCY_UNAVAILABLE"`) || failed.Header().Get("Location") != "" || strings.Contains(failed.Body.String(), "private-trust-job-storage") || enqueueCalls != 1 || peer.calls != 2 {
		t.Fatalf("storage refusal disclosed a receipt or internal cause: %d %s, enqueue=%d peer=%d", failed.Code, failed.Body, enqueueCalls, peer.calls)
	}
	retainedJobs, err := store.List(t.Context(), tenant, jobs.ListRequest{Limit: 10})
	if err != nil || len(retainedJobs) != 1 || retainedJobs[0].ID != job.ID || retainedJobs[0].State != jobs.StateSucceeded {
		t.Fatal("failed enqueue changed retained Jobs", retainedJobs, err)
	}
	server.jobs = service
	policy.enabled = false
	refused := call("POST", path, "admin", string(body), "trust-request-2")
	if refused.Code != 503 || peer.calls != 2 {
		t.Fatalf("disabled command=%d calls=%d", refused.Code, peer.calls)
	}
	policy.enabled = true
	source.Status = federation.SourceRemoved
	source.Revision++
	if _, e = sources.UpdateSource(t.Context(), source, 1); e != nil {
		t.Fatal(e)
	}
	historical := call("GET", path+"/"+job.ID, "admin", "", "")
	if historical.Code != 200 || !strings.Contains(historical.Body.String(), `"historical"`) {
		t.Fatalf("removed-source history=%d %s", historical.Code, historical.Body)
	}
	requireControlSchema(t, "TrustAssessmentResponse", historical.Body.Bytes())
	records, err := repo.AuditEvents(t.Context(), tenant, catalog.AuditEventListRequest{Filters: audit.Query{Action: audit.ActionSourceTrustRequested}, Limit: 10})
	if err != nil || len(records) != 1 {
		t.Fatalf("enqueue audit count=%d err=%v", len(records), err)
	}
	reads, err := repo.AuditEvents(t.Context(), tenant, catalog.AuditEventListRequest{Filters: audit.Query{Action: audit.ActionSourceTrustRead}, Limit: 10})
	if err != nil || len(reads) != 2 {
		t.Fatalf("private disclosure audit count=%d err=%v", len(reads), err)
	}
	observations, _, err := sources.ListCurrentObservations(t.Context(), tenant, sourceID, 10)
	if err != nil || len(observations) != 0 {
		t.Fatal("live assessment changed indexed evidence", err)
	}
}
