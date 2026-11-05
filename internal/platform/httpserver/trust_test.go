package httpserver

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/audit"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/federation"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/jobs"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/clock"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/id"
	"github.com/open-agentic-catalog/open-agentic-catalog/test/identityfixture"
)

// Isolate transport refusals at existing store ports. No Job is enqueued and
// no verifier, worker, audit workflow or outbound fetch runs in these units.
func TestTrustRequestRejectsIncompleteSelectionAndUnavailableVerification(t *testing.T) {
	source := federationFixtureSource()
	manager, err := federation.NewManager(&artworkReceiptStore{source: source}, publicPolicyFetcher{}, clock.NewFake(projectionTime), id.Generator{})
	if err != nil {
		t.Fatal(err)
	}
	s := projectionServer(nil)
	s.federation = manager
	body := `{"sourceRevision":1,"identifier":"urn:air:peer.example:agents:one","versionPresent":false,"mediaType":"application/a2a-agent-card+json","delivery":"url","exactEntryDigest":"sha256:` + strings.Repeat("a", 64) + `","text":"one","reason":"Check registration"}`
	for _, trial := range []struct {
		name, body, sourceID string
		role                 identity.Role
		status               int
		code                 catalog.ErrorCode
	}{
		{"missing selection", `{}`, source.ID, identity.RoleTenantAdmin, 400, catalog.CodeInvalidArgument},
		{"missing current authority", body, source.ID, identity.RoleReader, 403, catalog.CodeForbidden},
		{"source missing", body, projectionNext, identity.RoleTenantAdmin, 404, catalog.CodeNotFound},
		{"verification disabled", body, source.ID, identity.RoleTenantAdmin, 503, catalog.CodeDependencyUnavailable},
	} {
		t.Run(trial.name, func(t *testing.T) {
			r := httptest.NewRequestWithContext(identityfixture.WithActor(t.Context(), projectionActor(trial.role)), http.MethodPost, "/trust-assessments", strings.NewReader(trial.body))
			r.Header.Set("Content-Type", "application/json")
			r.SetPathValue("sourceId", trial.sourceID)
			w := httptest.NewRecorder()
			s.handleTrustRequest(w, r)
			requireProjectionProblem(t, w, trial.status, trial.code)
			if w.Header().Get("Location") != "" || strings.Contains(w.Body.String(), "exactEntryDigest") {
				t.Fatal("refused assessment exposed selection evidence or a Job receipt")
			}
		})
	}
}

func TestTrustReadKeepsPendingWrongSourceAndGenericJobsPrivate(t *testing.T) {
	entry := []byte(`{"identifier":"urn:air:peer.example:agents:one","type":"application/a2a-agent-card+json","url":"https://peer.example/card","score":100,"source":"https://peer.example/search"}`)
	payload := jobs.TrustPayload{SourceID: projectionNext, SourceRevision: 1, SourcePolicyDigest: "sha256:" + strings.Repeat("a", 64), PolicyName: "owned", PolicyDigest: "sha256:" + strings.Repeat("b", 64), Identifier: "urn:air:peer.example:agents:one", MediaType: "application/a2a-agent-card+json", Delivery: "url", ExactEntryDigest: audit.DigestBytes(entry), Entry: entry}
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	job := projectionJob()
	job.Kind, job.Payload = jobs.TrustKind, raw
	if _, err = jobs.DecodeTrustPayload(job); err != nil {
		t.Fatal("pending control must contain valid retained evidence", err)
	}
	for _, mode := range []string{"pending assessment", "wrong source", "generic Job", "missing Job", "private reread unavailable", "reader lacks evidence permission"} {
		t.Run(mode, func(t *testing.T) {
			reads := 0
			s := jobProjectionServer(t, jobProjectionStore{get: func(_ context.Context, tenant, key string) (jobs.Job, error) {
				reads++
				if tenant != job.TenantID || key != job.ID {
					t.Fatal("trust read changed the current tenant or Job")
				}
				if mode == "missing Job" {
					return jobs.Job{}, catalog.E(catalog.CodeNotFound, "Job was not found")
				}
				if mode == "private reread unavailable" && reads == 3 {
					return jobs.Job{}, catalog.E(catalog.CodeDependencyUnavailable, "private trust storage error")
				}
				if mode == "generic Job" {
					return projectionJob(), nil
				}
				return job, nil
			}})
			s.federation, err = federation.NewManager(&artworkReceiptStore{}, publicPolicyFetcher{}, clock.NewFake(projectionTime), id.Generator{})
			if err != nil {
				t.Fatal(err)
			}
			role, sourceID := identity.RoleTenantAdmin, payload.SourceID
			status, code, wantReads := http.StatusNotFound, catalog.CodeNotFound, 3
			switch mode {
			case "pending assessment":
				status, code = http.StatusConflict, catalog.CodeInvalidTransition
			case "wrong source":
				sourceID = projectionResource
			case "missing Job":
				wantReads = 1
			case "private reread unavailable":
				status, code = http.StatusServiceUnavailable, catalog.CodeDependencyUnavailable
			case "reader lacks evidence permission":
				role, status, code, wantReads = identity.RoleReader, http.StatusForbidden, catalog.CodeForbidden, 1
			}
			r := httptest.NewRequestWithContext(identityfixture.WithActor(t.Context(), projectionActor(role)), http.MethodGet, "/trust-assessments/"+job.ID, nil)
			r.SetPathValue("sourceId", sourceID)
			r.SetPathValue("jobId", job.ID)
			w := httptest.NewRecorder()
			s.handleTrustRead(w, r)
			requireProjectionProblem(t, w, status, code)
			if reads != wantReads || strings.Contains(w.Body.String(), "private trust") || strings.Contains(w.Body.String(), "policyDigest") || strings.Contains(w.Body.String(), "peer.example/card") {
				t.Fatalf("private trust refusal = %d %s reads=%d, want=%d", w.Code, w.Body.String(), reads, wantReads)
			}
		})
	}
}
