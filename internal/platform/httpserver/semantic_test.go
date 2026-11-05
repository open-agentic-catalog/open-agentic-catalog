// Reindex handler tests isolate strict command decoding, scope selection and
// public Job receipts; worker execution and HTTP replay remain functional.
package httpserver

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/open-agentic-catalog/open-agentic-catalog/test/identityfixture"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/audit"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/jobs"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/clock"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/id"
)

type reindexProjectionStore struct {
	jobs.Store
	enqueue func(context.Context, jobs.Job, ...audit.Record) (jobs.Job, bool, error)
}

func (s reindexProjectionStore) Enqueue(ctx context.Context, job jobs.Job, evidence ...audit.Record) (jobs.Job, bool, error) {
	return s.enqueue(ctx, job, evidence...)
}

// TestSemanticHandlerRejectsBodyBeforeServices pins the handler's closed object
// and byte limit, leaving generic decoder permutations in their existing tests.
func TestSemanticHandlerRejectsBodyBeforeServices(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ name, body, media string }{
		{name: "malformed", body: `{"reason":`, media: "application/json"},
		{name: "duplicate", body: `{"reason":"one","reason":"two"}`, media: "application/json"},
		{name: "provider override", body: `{"reason":"rebuild","model":"private-fixture-model"}`, media: "application/json"},
		{name: "trailing value", body: `{"reason":"rebuild"} {}`, media: "application/json"},
		{name: "wrong media", body: `{"reason":"rebuild"}`, media: "text/plain"},
		{name: "body bound", body: `{"reason":"` + strings.Repeat("x", 1024) + `"}`, media: "application/json"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/v1/catalog:reindex", strings.NewReader(tc.body))
			request.Header.Set("Content-Type", tc.media)
			response := httptest.NewRecorder()
			projectionServer(nil).handleSemanticReindex(response, request)
			requireProjectionProblem(t, response, http.StatusBadRequest, catalog.CodeInvalidArgument)
		})
	}
}

// TestSemanticHandlerScopeAndReceipt observes one request at existing domain
// ports, including deduplicated receipts and errors without a false Location.
func TestSemanticHandlerScopeAndReceipt(t *testing.T) {
	t.Parallel()
	const snapshot = "abcdef01-0000-7000-8000-000000000004"
	for _, tc := range []struct {
		name, body, scope              string
		reader, disabled, reused       bool
		currentFailure, enqueueFailure error
		currentCalls, enqueueCalls     int
		status                         int
		code                           catalog.ErrorCode
	}{
		{name: "control", body: `{"reason":"  Rebuild metadata  "}`, scope: "control", currentCalls: 1, enqueueCalls: 1, status: http.StatusAccepted},
		{name: "public", body: `{"reason":"Rebuild metadata","snapshotId":"` + strings.ToUpper(snapshot) + `"}`, scope: "public:" + snapshot, currentCalls: 1, enqueueCalls: 1, status: http.StatusAccepted},
		{name: "existing job", body: `{"reason":"Rebuild metadata"}`, scope: "control", reused: true, currentCalls: 1, enqueueCalls: 1, status: http.StatusAccepted},
		{name: "reader forbidden", body: `{"reason":"Rebuild metadata"}`, reader: true, status: http.StatusForbidden, code: catalog.CodeForbidden},
		{name: "disabled", body: `{"reason":"Rebuild metadata"}`, disabled: true, status: http.StatusServiceUnavailable, code: catalog.CodeDependencyUnavailable},
		{name: "invalid snapshot", body: `{"reason":"Rebuild metadata","snapshotId":"bad"}`, status: http.StatusBadRequest, code: catalog.CodeInvalidArgument},
		{name: "missing reason", body: `{}`, scope: "control", currentCalls: 1, status: http.StatusBadRequest, code: catalog.CodeInvalidArgument},
		{name: "missing snapshot", body: `{"reason":"Rebuild metadata","snapshotId":"` + snapshot + `"}`, scope: "public:" + snapshot, currentFailure: catalog.E(catalog.CodeNotFound, "snapshot not found"), currentCalls: 1, status: http.StatusNotFound, code: catalog.CodeNotFound},
		{name: "enqueue failure", body: `{"reason":"Rebuild metadata"}`, scope: "control", enqueueFailure: catalog.Wrap(catalog.CodeDependencyUnavailable, "job store unavailable", errors.New("private-fixture-database")), currentCalls: 1, enqueueCalls: 1, status: http.StatusServiceUnavailable, code: catalog.CodeDependencyUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			actor := projectionActor(identity.RoleTenantAdmin)
			if tc.reader {
				actor.Roles = []identity.Role{identity.RoleReader}
			}
			ctx := identityfixture.WithActor(t.Context(), actor)
			currentCalls, enqueueCalls := 0, 0
			var semantic *catalog.SemanticSearch
			if !tc.disabled {
				semantic = projectionSemantic(t, projectionSemanticStore{current: func(got context.Context, tenant, scope string) (string, error) {
					currentCalls++
					if !sameProjectionContext(got, ctx) || tenant != actor.TenantID || scope != tc.scope {
						t.Fatalf("current scope handoff: tenant=%s scope=%s", tenant, scope)
					}
					return "42", tc.currentFailure
				}}, nil, nil)
			}
			var returned jobs.Job
			service, err := jobs.NewService(reindexProjectionStore{enqueue: func(got context.Context, job jobs.Job, evidence ...audit.Record) (jobs.Job, bool, error) {
				enqueueCalls++
				payload, payloadErr := catalog.DecodeSemanticReindex(job.Payload, job.PayloadVersion)
				wantPayload := catalog.SemanticReindexInput{Profile: "sha256:" + strings.Repeat("a", 64), Scope: tc.scope, SourceGeneration: "42"}
				if !sameProjectionContext(got, ctx) || job.TenantID != actor.TenantID || job.Actor != actor.MembershipID || job.Kind != "semantic.reindex" || job.PayloadVersion != 1 || payloadErr != nil || payload != wantPayload || job.DeduplicationKey != "sha256:"+strings.Repeat("a", 64)+":"+tc.scope || job.State != jobs.StateQueued {
					t.Fatalf("enqueue binding=%+v payload=%s", job, job.Payload)
				}
				if len(evidence) != 1 || audit.ValidateNewRecord(evidence[0]) != nil || evidence[0].Reason != "Rebuild metadata" || evidence[0].TenantID != actor.TenantID || evidence[0].Actor != actor.MembershipID || evidence[0].Action != audit.ActionSemanticReindexRequested || evidence[0].TargetID != job.ID {
					t.Fatalf("request evidence=%+v", evidence)
				}
				if tc.reused {
					job.ID = "00000000-0000-7000-8000-000000000009"
					job.State = jobs.StateRunning
					job.Actor = "00000000-0000-7000-8000-000000000008"
					job.Attempt = 1
					job.LeaseOwner = "private-fixture-worker"
					expires := projectionTime.Add(time.Minute)
					job.LeaseExpiresAt = &expires
				}
				returned = job
				return job, tc.reused, tc.enqueueFailure
			}}, clock.NewFake(projectionTime), id.Generator{}, jobs.WithAuditEvidence())
			if err != nil {
				t.Fatal(err)
			}
			server := projectionServer(projectionCatalog(t, nil, semantic))
			server.jobs = service
			request := httptest.NewRequestWithContext(ctx, http.MethodPost, "/api/v1/catalog:reindex", strings.NewReader(tc.body))
			request.Header.Set("Content-Type", "application/json")
			response := httptest.NewRecorder()
			server.handleSemanticReindex(response, request)
			if currentCalls != tc.currentCalls || enqueueCalls != tc.enqueueCalls {
				t.Fatalf("current calls=%d enqueue calls=%d", currentCalls, enqueueCalls)
			}
			if tc.status != http.StatusAccepted {
				requireProjectionProblem(t, response, tc.status, tc.code)
				if response.Header().Get("Location") != "" {
					t.Fatal("failed command emitted a Job location")
				}
				return
			}
			var view jobs.View
			if err := json.Unmarshal(response.Body.Bytes(), &view); err != nil {
				t.Fatal(err)
			}
			if response.Code != http.StatusAccepted || response.Header().Get("Location") != "/api/v1/jobs/"+returned.ID || response.Header().Get("Content-Type") != "application/json; charset=utf-8" || view.ID != returned.ID || view.Kind != "semantic.reindex" || view.TenantID != actor.TenantID || view.State != returned.State || !view.CreatedAt.Equal(projectionTime) {
				t.Fatalf("job receipt=%d %s headers=%v", response.Code, response.Body.String(), response.Header())
			}
			for _, private := range []string{`"payload"`, `"actor"`, `"deduplicationKey"`, `"sourceGeneration"`, `"leaseOwner"`, `"leaseExpiresAt"`, "private-fixture"} {
				if strings.Contains(response.Body.String(), private) {
					t.Fatalf("private Job field in receipt: %s", private)
				}
			}
		})
	}
}
