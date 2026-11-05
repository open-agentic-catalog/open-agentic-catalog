// Job handler units inspect one operation at controlled store ports. They do
// not enqueue real work or execute a worker.
package httpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/open-agentic-catalog/open-agentic-catalog/test/identityfixture"
	"net/http"
	"net/http/httptest"
	"reflect"
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

type jobProjectionStore struct {
	jobs.Store
	get     func(context.Context, string, string) (jobs.Job, error)
	list    func(context.Context, string, jobs.ListRequest) ([]jobs.Job, error)
	cancel  func(context.Context, string, string, time.Time, ...audit.Record) (jobs.Job, error)
	enqueue func(context.Context, jobs.Job, ...audit.Record) (jobs.Job, bool, error)
}

func (s jobProjectionStore) Get(c context.Context, t, k string) (jobs.Job, error) {
	return s.get(c, t, k)
}
func (s jobProjectionStore) GetView(c context.Context, t, k string) (jobs.View, error) {
	value, err := s.get(c, t, k)
	return jobs.PublicView(value), err
}
func (s jobProjectionStore) ListViews(c context.Context, t string, p jobs.ListRequest) ([]jobs.View, error) {
	values, err := s.list(c, t, p)
	if err != nil {
		return nil, err
	}
	views := make([]jobs.View, len(values))
	for i, value := range values {
		views[i] = jobs.PublicView(value)
	}
	return views, nil
}
func (s jobProjectionStore) RequestCancellation(c context.Context, t, k string, at time.Time, e ...audit.Record) (jobs.Job, error) {
	return s.cancel(c, t, k, at, e...)
}
func (s jobProjectionStore) Enqueue(c context.Context, j jobs.Job, e ...audit.Record) (jobs.Job, bool, error) {
	return s.enqueue(c, j, e...)
}
func jobProjectionServer(t *testing.T, store jobProjectionStore) *Server {
	t.Helper()
	service, err := jobs.NewService(store, clock.NewFake(projectionTime), id.Generator{}, jobs.WithAuditEvidence())
	if err != nil {
		t.Fatal(err)
	}
	s := projectionServer(nil)
	s.jobs = service
	return s
}
func projectionJob() jobs.Job {
	return jobs.Job{ID: projectionItem, TenantID: projectionTenant, Kind: "publication.generate", Actor: projectionActor(identity.RolePublisher).MembershipID, PayloadVersion: 1, Payload: json.RawMessage(`{"host":"private-fixture.example"}`), State: jobs.StateQueued, Priority: 2, NotBefore: projectionTime, AttemptLimit: 3, DeduplicationKey: "private-fixture-key", CreatedAt: projectionTime, UpdatedAt: projectionTime}
}
func requireJobProjection(t *testing.T, raw []byte, expected jobs.Job) {
	t.Helper()
	var view jobs.View
	if err := json.Unmarshal(raw, &view); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(view, jobs.PublicView(expected)) {
		t.Fatalf("job projection=%+v expected=%+v", view, jobs.PublicView(expected))
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"actor", "payload", "payloadVersion", "leaseOwner", "leaseExpiresAt", "deduplicationKey", "causalTrace"} {
		if _, ok := fields[key]; ok {
			t.Fatalf("private job field %s returned", key)
		}
	}
	if strings.Contains(string(raw), "private-fixture") {
		t.Fatalf("private job content returned: %s", raw)
	}
}
func TestJobCommandProjection(t *testing.T) {
	t.Parallel()
	for _, action := range []string{"get", "cancel", "retry"} {
		for _, failure := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/failure=%t", action, failure), func(t *testing.T) {
				actor := projectionActor(identity.RoleTenantAdmin)
				ctx := identityfixture.WithActor(t.Context(), actor)
				original := projectionJob()
				if action == "retry" {
					original.State = jobs.StateFailed
					original.Attempt = 1
					original.FailureClass = jobs.FailureDependency
				}
				if action == "get" {
					original.State = jobs.StateRunning
					original.Attempt = 1
					original.LeaseOwner = "private-fixture-worker"
					expiry := projectionTime.Add(time.Minute)
					original.LeaseExpiresAt = &expiry
				}
				expected := original
				reads, writes := 0, 0
				failureErr := catalog.E(catalog.CodeDependencyUnavailable, "private-fixture-store")
				checkEvidence := func(records []audit.Record, action string) {
					if len(records) != 1 {
						t.Fatalf("evidence count=%d", len(records))
					}
					r := records[0]
					if err := audit.ValidateNewRecord(r); err != nil {
						t.Fatal(err)
					}
					if r.Action != action || r.TargetID != original.ID || r.TenantID != actor.TenantID || r.Actor != actor.MembershipID || r.Reason != "Operator maintenance" {
						t.Fatalf("job evidence=%+v", r)
					}
				}
				store := jobProjectionStore{
					get: func(c context.Context, tenant, key string) (jobs.Job, error) {
						reads++
						if !sameProjectionContext(c, ctx) || tenant != actor.TenantID || key != original.ID {
							t.Fatal("job lookup binding")
						}
						if action == "get" && failure {
							return jobs.Job{}, failureErr
						}
						return original, nil
					},
					cancel: func(c context.Context, tenant, key string, at time.Time, e ...audit.Record) (jobs.Job, error) {
						writes++
						if !sameProjectionContext(c, ctx) || tenant != actor.TenantID || key != original.ID || !at.Equal(projectionTime) {
							t.Fatal("job cancellation binding")
						}
						checkEvidence(e, audit.ActionJobCancellationRequested)
						expected.State = jobs.StateCancelled
						if _, err := jobs.FinalizeCancellationEvidence(e[0], original, expected); err != nil {
							t.Fatal(err)
						}
						if failure {
							return jobs.Job{}, failureErr
						}
						return expected, nil
					},
					enqueue: func(c context.Context, j jobs.Job, e ...audit.Record) (jobs.Job, bool, error) {
						writes++
						if !sameProjectionContext(c, ctx) || j.TenantID != actor.TenantID || j.ParentJobID != original.ID || j.Actor != actor.MembershipID || j.State != jobs.StateQueued || j.Kind != original.Kind || j.PayloadVersion != original.PayloadVersion || string(j.Payload) != string(original.Payload) || j.AttemptLimit != original.AttemptLimit || !identity.ValidTenantID(j.ID) || j.ID == original.ID {
							t.Fatalf("retry handoff=%+v", j)
						}
						checkEvidence(e, audit.ActionJobRetried)
						if _, err := jobs.FinalizeEnqueueEvidence(e[0], j, false); err != nil {
							t.Fatal(err)
						}
						expected = j
						if failure {
							return jobs.Job{}, false, failureErr
						}
						return j, false, nil
					},
				}
				s := jobProjectionServer(t, store)
				r := httptest.NewRequestWithContext(ctx, http.MethodPost, "/api/v1/jobs/"+original.ID, strings.NewReader(`{"reason":" Operator maintenance "}`))
				r.Header.Set("Content-Type", "application/json")
				r.SetPathValue("jobId", original.ID)
				w := httptest.NewRecorder()
				switch action {
				case "get":
					r.Method = http.MethodGet
					s.handleJobGet(w, r)
				case "cancel":
					s.handleJobCancel(w, r)
				case "retry":
					s.handleJobRetry(w, r)
				}
				wantWrites := 1
				if action == "get" {
					wantWrites = 0
				}
				wantReads := 1
				if action == "get" && !failure {
					wantReads = 2
				}
				if reads != wantReads || writes != wantWrites {
					t.Fatalf("reads/writes=%d/%d", reads, writes)
				}
				if failure {
					requireProjectionProblem(t, w, http.StatusServiceUnavailable, catalog.CodeDependencyUnavailable)
					if w.Header().Get("Location") != "" {
						t.Fatal("failure returned Location")
					}
					return
				}
				status := http.StatusOK
				if action == "retry" {
					status = http.StatusAccepted
					if w.Header().Get("Location") != "/api/v1/jobs/"+expected.ID {
						t.Fatal("retry Location")
					}
				}
				if w.Code != status {
					t.Fatalf("status=%d body=%s", w.Code, w.Body)
				}
				requireJobProjection(t, w.Body.Bytes(), expected)
			})
		}
	}
}
func TestJobListProjection(t *testing.T) {
	t.Parallel()
	for _, failure := range []bool{false, true} {
		t.Run(fmt.Sprintf("failure=%t", failure), func(t *testing.T) {
			ctx := identityfixture.WithActor(t.Context(), projectionActor(identity.RoleTenantAdmin))
			item := projectionJob()
			next := item
			next.ID = projectionNext
			next.CreatedAt = projectionTime.Add(-time.Second)
			calls := 0
			s := jobProjectionServer(t, jobProjectionStore{list: func(c context.Context, tenant string, p jobs.ListRequest) ([]jobs.Job, error) {
				calls++
				if !sameProjectionContext(c, ctx) || tenant != projectionTenant || p.Limit != 2 {
					t.Fatalf("job list binding: %+v", p)
				}
				if failure {
					return nil, catalog.E(catalog.CodeDependencyUnavailable, "private-fixture-store")
				}
				if calls == 1 {
					if p.AfterID != "" || p.AfterCreatedAt != nil {
						t.Fatal("initial job position")
					}
					return []jobs.Job{item, next}, nil
				}
				if p.AfterID != item.ID || p.AfterCreatedAt == nil || !p.AfterCreatedAt.Equal(item.CreatedAt) {
					t.Fatalf("job continuation=%+v", p)
				}
				return nil, nil
			}})
			cursor := ""
			for page := 0; page < 2; page++ {
				r := httptest.NewRequestWithContext(ctx, http.MethodGet, "/api/v1/jobs?limit=1"+cursor, nil)
				w := httptest.NewRecorder()
				s.handleJobsList(w, r)
				if failure {
					requireProjectionProblem(t, w, http.StatusServiceUnavailable, catalog.CodeDependencyUnavailable)
					break
				}
				var got struct {
					Items      []json.RawMessage `json:"items"`
					NextCursor string            `json:"nextCursor"`
				}
				if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
					t.Fatal(err)
				}
				if w.Code != 200 || got.Items == nil {
					t.Fatalf("job page=%d %s", w.Code, w.Body)
				}
				if page == 0 {
					if len(got.Items) != 1 || got.NextCursor == "" {
						t.Fatalf("first page=%s", w.Body)
					}
					requireJobProjection(t, got.Items[0], item)
					cursor = "&cursor=" + got.NextCursor
				} else if len(got.Items) != 0 || got.NextCursor != "" {
					t.Fatalf("last page=%s", w.Body)
				}
			}
			want := 2
			if failure {
				want = 1
			}
			if calls != want {
				t.Fatalf("list calls=%d", calls)
			}
		})
	}
}
