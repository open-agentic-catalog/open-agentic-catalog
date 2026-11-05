package httpserver

import (
	"context"
	"encoding/json"
	"github.com/open-agentic-catalog/open-agentic-catalog/test/identityfixture"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/audit"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/federation"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/jobs"
)

func TestFederationSyncReceipt(t *testing.T) {
	t.Parallel()
	for _, outcome := range []string{"queued", "existing", "stale", "source-unavailable", "enqueue-failed"} {
		t.Run(outcome, func(t *testing.T) {
			actor := projectionActor(identity.RoleSourceAdmin)
			ctx := identityfixture.WithActor(t.Context(), actor)
			source := federationFixtureSource()
			reads, writes := 0, 0
			var retained jobs.Job
			s := federationProjectionServer(t, federationProjectionStore{getSource: func(c context.Context, tenant, key string) (federation.Source, error) {
				reads++
				if !sameProjectionContext(c, ctx) || tenant != actor.TenantID || key != source.ID {
					t.Fatal("sync source binding")
				}
				if outcome == "source-unavailable" {
					return federation.Source{}, catalog.E(catalog.CodeDependencyUnavailable, "private-fixture-store")
				}
				return source, nil
			}}, nil)
			s.jobs = jobProjectionServer(t, jobProjectionStore{enqueue: func(c context.Context, job jobs.Job, e ...audit.Record) (jobs.Job, bool, error) {
				writes++
				wantPayload := `{"sourceId":"` + source.ID + `","sourceRevision":3,"trigger":"manual"}`
				if !sameProjectionContext(c, ctx) || job.TenantID != actor.TenantID || job.Actor != actor.MembershipID || job.Kind != "source.sync" || job.PayloadVersion != 2 || string(job.Payload) != wantPayload || job.DeduplicationKey != "source.sync:"+source.ID+":3" {
					t.Fatalf("sync job=%+v payload=%s", job, job.Payload)
				}
				if len(e) != 1 || audit.ValidateNewRecord(e[0]) != nil || e[0].Action != audit.ActionSourceSyncRequested || e[0].TargetID != source.ID || e[0].TenantID != actor.TenantID || e[0].Actor != actor.MembershipID {
					t.Fatalf("sync audit=%+v", e)
				}
				if outcome == "existing" {
					job.ID = projectionNext
				}
				// Stored private details must not become part of the public receipt.
				job.Payload = json.RawMessage(`{"private-fixture-payload":true}`)
				job.DeduplicationKey = "private-fixture-key"
				retained = job
				if outcome == "enqueue-failed" {
					return jobs.Job{}, false, catalog.E(catalog.CodeDependencyUnavailable, "private-fixture-jobs")
				}
				return job, outcome == "existing", nil
			}}).jobs
			r := httptest.NewRequestWithContext(ctx, http.MethodPost, "/sync", strings.NewReader(`{}`))
			r.Header.Set("Content-Type", "application/json")
			r.Header.Set("If-Match", `"rev-3"`)
			if outcome == "stale" {
				r.Header.Set("If-Match", `"rev-2"`)
			}
			r.SetPathValue("sourceId", source.ID)
			w := httptest.NewRecorder()
			s.handleSourceSync(w, r)
			wantWrites := 1
			switch outcome {
			case "stale":
				requireProjectionProblem(t, w, 412, catalog.CodePreconditionFailed)
				wantWrites = 0
			case "source-unavailable":
				requireProjectionProblem(t, w, 503, catalog.CodeDependencyUnavailable)
				wantWrites = 0
			case "enqueue-failed":
				requireProjectionProblem(t, w, 503, catalog.CodeDependencyUnavailable)
			default:
				var receipt struct {
					Job      json.RawMessage `json:"job"`
					Existing bool            `json:"existing"`
				}
				if err := json.Unmarshal(w.Body.Bytes(), &receipt); err != nil {
					t.Fatal(err)
				}
				if w.Code != 202 || receipt.Existing != (outcome == "existing") || w.Header().Get("Location") != "/api/v1/jobs/"+retained.ID {
					t.Fatalf("receipt=%d %v %s", w.Code, w.Header(), w.Body)
				}
				requireJobProjection(t, receipt.Job, retained)
			}
			if reads != 1 || writes != wantWrites {
				t.Fatalf("reads=%d writes=%d", reads, writes)
			}
			if w.Code != 202 && w.Header().Get("Location") != "" {
				t.Fatal("failed sync advertised job")
			}
		})
	}
}

func TestFederationSearchPositionAdmission(t *testing.T) {
	t.Parallel()
	s := projectionServer(nil)
	actor := projectionActor(identity.RoleSourceAdmin)
	ctx := identityfixture.WithActor(t.Context(), actor)
	filters := map[string]string{"query": `{"text":"fixture"}`}
	binding := adminCursorBinding{Purpose: "observation-search", AuthorizationDigest: fixtureDecisionDigest(t, t.Context(), actor), Order: "score desc,identifier,id", Limit: 1, Filters: filters}
	for _, position := range [][]string{{"90"}, {"invalid", federationFixtureAIR, projectionItem}, {"-1", federationFixtureAIR, projectionItem}, {"101", federationFixtureAIR, projectionItem}} {
		token, err := s.encodeAdminCursor(adminPageRequest{Limit: 1, binding: binding}, position)
		if err != nil {
			t.Fatal(err)
		}
		raw, err := json.Marshal(map[string]any{"text": "fixture", "limit": 1, "cursor": token})
		if err != nil {
			t.Fatal(err)
		}
		r := httptest.NewRequestWithContext(ctx, http.MethodPost, "/search", strings.NewReader(string(raw)))
		r.Header.Set("Content-Type", "application/json")
		limit := 1
		page, err := s.parseObservationSearchPage(r, filters, &limit, &token)
		if err != nil || !reflect.DeepEqual(page.Position, position) {
			t.Fatalf("signed search position=%+v err=%v", page, err)
		}
		w := httptest.NewRecorder()
		s.handleObservationSearch(w, r)
		requireProjectionProblem(t, w, 400, catalog.CodeInvalidArgument)
	}
	r := httptest.NewRequestWithContext(ctx, http.MethodPost, "/search", strings.NewReader(`{"text":"fixture","limit":0}`))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.handleObservationSearch(w, r)
	requireProjectionProblem(t, w, 400, catalog.CodeInvalidArgument)
}
