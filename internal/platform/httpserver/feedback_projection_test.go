// Feedback projections use one controlled repository operation at a time;
// private replay and full submission lifecycles remain functional.
package httpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/open-agentic-catalog/open-agentic-catalog/test/identityfixture"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/audit"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/feedback"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/clock"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/id"
)

type feedbackProjectionStore struct {
	feedback.Store
	get  func(context.Context, string, string) (feedback.Record, error)
	list func(context.Context, string, feedback.ListInput) ([]feedback.Record, error)
	save func(context.Context, feedback.Record, int64, audit.Record) (feedback.Record, error)
}

func (s feedbackProjectionStore) Get(ctx context.Context, tenant, key string) (feedback.Record, error) {
	return s.get(ctx, tenant, key)
}
func (s feedbackProjectionStore) List(ctx context.Context, tenant string, q feedback.ListInput) ([]feedback.Record, error) {
	return s.list(ctx, tenant, q)
}
func (s feedbackProjectionStore) Save(ctx context.Context, r feedback.Record, rev int64, e audit.Record) (feedback.Record, error) {
	return s.save(ctx, r, rev, e)
}

type feedbackProjectionContext struct {
	feedback.ContextReader
	resource func(context.Context, string, string) (catalog.Resource, error)
}

func (r feedbackProjectionContext) GetResource(ctx context.Context, tenant, key string) (catalog.Resource, error) {
	return r.resource(ctx, tenant, key)
}

func feedbackProjectionServer(t *testing.T, store feedbackProjectionStore, ctx context.Context) *Server {
	t.Helper()
	service, err := feedback.NewService(store, feedbackProjectionContext{resource: func(got context.Context, tenant, key string) (catalog.Resource, error) {
		if !sameProjectionContext(got, ctx) || tenant != projectionTenant || key != projectionResource {
			t.Fatal("feedback resource lookup lost context or tenant")
		}
		return catalog.Resource{ID: key, TenantID: tenant, NamespaceID: projectionNext, Kind: catalog.KindAgent, CanonicalIdentifier: "urn:air:catalog.example.com:default:feedback-agent", CreatedBy: "administrator", Revision: 1, CreatedAt: projectionTime, UpdatedAt: projectionTime}, nil
	}}, clock.NewFake(projectionTime), id.Generator{})
	if err != nil {
		t.Fatal(err)
	}
	s := projectionServer(nil)
	s.feedback = service
	return s
}
func feedbackProjectionRecord() feedback.Record {
	return feedback.Record{ID: projectionItem, TenantID: projectionTenant, MembershipID: projectionActor(identity.RoleReader).MembershipID, ResourceID: projectionResource, Category: "problem", Comment: "private-fixture-comment", Response: "private-fixture-response", State: "addressed", Revision: 4, CreatedAt: projectionTime, UpdatedAt: projectionTime}
}

// TestFeedbackCommandProjection checks the route, revision fence, action and
// text-free receipt against independent valid records, never a lifecycle journey.
func TestFeedbackCommandProjection(t *testing.T) {
	t.Parallel()
	for _, action := range []string{"create", "edit", "address", "reopen", "remove", "withdraw"} {
		for _, failure := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/failure=%t", action, failure), func(t *testing.T) {
				ctx := identityfixture.WithActor(t.Context(), projectionActor(identity.RoleTenantAdmin))
				old := feedbackProjectionRecord()
				var saved feedback.Record
				reads, writes := 0, 0
				s := feedbackProjectionServer(t, feedbackProjectionStore{
					get: func(got context.Context, tenant, key string) (feedback.Record, error) {
						reads++
						if !sameProjectionContext(got, ctx) || tenant != projectionTenant || key != old.ID {
							t.Fatal("feedback lookup binding")
						}
						return old, nil
					},
					save: func(got context.Context, r feedback.Record, expected int64, e audit.Record) (feedback.Record, error) {
						writes++
						wantRevision := int64(4)
						wantAction := action
						if action == "create" {
							wantRevision = 0
							wantAction = "created"
						}
						if !sameProjectionContext(got, ctx) || expected != wantRevision || r.TenantID != projectionTenant || r.MembershipID != old.MembershipID || r.ResourceID != projectionResource || r.Revision != wantRevision+1 || audit.ValidateNewRecord(e) != nil || e.Action != "feedback."+wantAction || e.TargetID != r.ID {
							t.Fatalf("feedback save=%+v rev=%d audit=%+v", r, expected, e)
						}
						before := old
						if action == "create" {
							before = feedback.Record{}
						}
						if err := feedback.ValidateSave(before, r, expected, e); err != nil {
							t.Fatal(err)
						}
						switch action {
						case "create", "edit":
							if r.Comment != "new private text" || r.Category != "suggestion" || r.State != "open" {
								t.Fatal("feedback input lost")
							}
						case "address":
							if r.Response != "new response" || r.State != "addressed" {
								t.Fatal("response lost")
							}
						case "reopen":
							if r.Response != "" || r.State != "open" {
								t.Fatal("reopen lost")
							}
						case "remove", "withdraw":
							want := "removed"
							if action == "withdraw" {
								want = "withdrawn"
							}
							if r.Comment != "" || r.Response != "" || r.State != want {
								t.Fatal("terminal action lost")
							}
						}
						saved = r
						if failure {
							return feedback.Record{}, catalog.E(catalog.CodePreconditionFailed, "concurrent change")
						}
						return r, nil
					},
				}, ctx)
				method, path, body := http.MethodPost, "/api/v1/feedback/"+old.ID+":"+action, `{}`
				status := http.StatusOK
				switch action {
				case "create":
					path = "/api/v1/feedback"
					body = `{"resourceId":"` + projectionResource + `","category":"suggestion","comment":"new private text"}`
					status = http.StatusCreated
				case "edit":
					method = http.MethodPatch
					path = "/api/v1/feedback/" + old.ID
					body = `{"category":"suggestion","comment":"new private text"}`
				case "address":
					body = `{"response":"new response"}`
				}
				r := httptest.NewRequestWithContext(ctx, method, path, strings.NewReader(body))
				r.Header.Set("Content-Type", "application/json")
				r.Header.Set("If-Match", `"rev-4"`)
				w := httptest.NewRecorder()
				mux := http.NewServeMux()
				s.feedbackRoutes(mux)
				mux.ServeHTTP(w, r)
				wantReads := 1
				if action == "create" {
					wantReads = 0
				}
				if reads != wantReads || writes != 1 {
					t.Fatalf("reads=%d writes=%d", reads, writes)
				}
				if failure {
					requireProjectionProblem(t, w, http.StatusPreconditionFailed, catalog.CodePreconditionFailed)
					if w.Header().Get("Location") != "" {
						t.Fatal("failed receipt location")
					}
					return
				}
				var receipt map[string]any
				if err := json.Unmarshal(w.Body.Bytes(), &receipt); err != nil {
					t.Fatal(err)
				}
				if w.Code != status || len(receipt) != 2 || receipt["id"] != saved.ID || receipt["revision"] != float64(saved.Revision) || w.Header().Get("ETag") != fmt.Sprintf(`"rev-%d"`, saved.Revision) || w.Header().Get("Location") != "/api/v1/feedback/"+saved.ID || w.Header().Get("Cache-Control") != "no-store" {
					t.Fatalf("feedback receipt=%d %s %v", w.Code, w.Body, w.Header())
				}
			})
		}
	}
}

// TestFeedbackReadProjection preserves private current reads and owner/inbox
// filtering while selecting limit+1 and forwarding signed continuation positions.
func TestFeedbackReadProjection(t *testing.T) {
	t.Parallel()
	ctx := identityfixture.WithActor(t.Context(), projectionActor(identity.RoleTenantAdmin))
	item := feedbackProjectionRecord()
	for _, failure := range []bool{false, true} {
		s := feedbackProjectionServer(t, feedbackProjectionStore{get: func(got context.Context, tenant, key string) (feedback.Record, error) {
			if !sameProjectionContext(got, ctx) || tenant != projectionTenant || key != item.ID {
				t.Fatal("get binding")
			}
			if failure {
				return feedback.Record{}, catalog.E(catalog.CodeNotFound, "not found")
			}
			return item, nil
		}}, ctx)
		r := httptest.NewRequestWithContext(ctx, http.MethodGet, "/api/v1/feedback/"+item.ID, nil)
		r.SetPathValue("feedbackId", item.ID)
		w := httptest.NewRecorder()
		s.handleFeedbackGet(w, r)
		if failure {
			requireProjectionProblem(t, w, 404, catalog.CodeNotFound)
			continue
		}
		var got feedback.Record
		if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		if w.Code != 200 || got != item || w.Header().Get("ETag") != `"rev-4"` || w.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("read=%d %s", w.Code, w.Body)
		}
	}
	for _, inbox := range []bool{false, true} {
		for _, failure := range []bool{false, true} {
			calls := 0
			s := feedbackProjectionServer(t, feedbackProjectionStore{list: func(got context.Context, tenant string, q feedback.ListInput) ([]feedback.Record, error) {
				calls++
				member := item.MembershipID
				if inbox {
					member = ""
				}
				after := ""
				if calls == 2 {
					after = item.ID
				}
				if !sameProjectionContext(got, ctx) || tenant != projectionTenant || q != (feedback.ListInput{ResourceID: projectionResource, Category: "problem", State: "addressed", AfterID: after, MembershipID: member, Limit: 2}) {
					t.Fatalf("feedback list=%+v", q)
				}
				if failure {
					return nil, catalog.E(catalog.CodeDependencyUnavailable, "unavailable")
				}
				if calls == 2 {
					return []feedback.Record{}, nil
				}
				next := item
				next.ID = projectionNext
				return []feedback.Record{item, next}, nil
			}}, ctx)
			path := "/api/v1/feedback"
			if inbox {
				path += "/inbox"
			}
			path += "?limit=1&resourceId=" + projectionResource + "&category=problem&state=addressed"
			for page := 0; page < 2; page++ {
				w := httptest.NewRecorder()
				s.handleFeedbackList(w, httptest.NewRequestWithContext(ctx, http.MethodGet, path, nil))
				if failure {
					requireProjectionProblem(t, w, 503, catalog.CodeDependencyUnavailable)
					break
				}
				var got cursorPage[feedback.Record]
				if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
					t.Fatal(err)
				}
				if w.Code != 200 || w.Header().Get("Cache-Control") != "no-store" || got.Items == nil {
					t.Fatalf("list=%d %s", w.Code, w.Body)
				}
				if page == 0 {
					if len(got.Items) != 1 || got.Items[0] != item || got.NextCursor == "" {
						t.Fatal("unbounded feedback page")
					}
					path += "&cursor=" + got.NextCursor
				} else if len(got.Items) != 0 || got.NextCursor != "" {
					t.Fatal("empty final page changed")
				}
			}
		}
	}
}
