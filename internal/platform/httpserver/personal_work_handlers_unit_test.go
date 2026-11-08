// Personal work projections use a scripted repository, without an application
// graph, database, listener or authoring workflow.
package httpserver

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/test/identityfixture"
)

type personalWorkHandlerRepository struct {
	catalog.Repository
	list func(context.Context, string, catalog.PersonalWorkRequest) ([]catalog.PersonalWorkItem, error)
}

func (r personalWorkHandlerRepository) ListPersonalWork(ctx context.Context, tenant string, q catalog.PersonalWorkRequest) ([]catalog.PersonalWorkItem, error) {
	return r.list(ctx, tenant, q)
}

func personalWorkHandlerItem(id, queue string) catalog.PersonalWorkItem {
	item := catalog.PersonalWorkItem{ID: "resource:" + id, Type: "resource", ResourceID: id, NamespaceID: projectionResource, Kind: catalog.KindAgent, State: "empty_resource"}
	if queue != "" && queue != "drafts" {
		item.ID, item.Type, item.State = "submission:"+id, "submission", queue
		item.VersionRevisionID = projectionNext
		item.ListingID, item.ListingRevisionID, item.SubmissionID = projectionItem, projectionItem, id
		item.CurrentCandidateRevisionID = projectionItem
	}
	return item
}

func TestPersonalWorkHandlerQueuesAndPageBounds(t *testing.T) {
	for _, queue := range []string{"", "drafts", "submitted", "changes_required", "approved"} {
		t.Run("queue="+queue, func(t *testing.T) {
			calls := 0
			item := personalWorkHandlerItem(projectionItem, queue)
			repo := personalWorkHandlerRepository{list: func(ctx context.Context, tenant string, q catalog.PersonalWorkRequest) ([]catalog.PersonalWorkItem, error) {
				calls++
				actor, ok := identity.FromContext(ctx)
				wantQueue := queue
				if wantQueue == "" {
					wantQueue = "drafts"
				}
				if !ok || actor.Subject != "library-reader" || tenant != projectionTenant || q.Queue != wantQueue || q.Limit != 21 || q.AfterID != "" {
					t.Fatalf("work owner, queue or bounded lookahead changed: actor=%+v tenant=%s query=%+v", actor, tenant, q)
				}
				return []catalog.PersonalWorkItem{item}, nil
			}}
			server, request := personalHandlerFixture(t, personalHandlerStore{})
			server.catalog = projectionCatalog(t, repo, nil)
			w := httptest.NewRecorder()
			path := "/api/v1/me/work"
			if queue != "" {
				path += "?queue=" + queue
			}
			server.handlePersonalWork(w, request("GET", path, ""))
			requireControlJSON(t, w, 200, map[string]any{"items": []catalog.PersonalWorkItem{item}})
			if calls != 1 || w.Header().Get("Cache-Control") != "no-store" {
				t.Fatal("private work response lost its read or cache policy", calls, w.Header())
			}
		})
	}
	for _, limit := range []string{"1", "100"} {
		t.Run("limit="+limit, func(t *testing.T) {
			wantLimit := 2
			if limit == "100" {
				wantLimit = 101
			}
			calls := 0
			repo := personalWorkHandlerRepository{list: func(_ context.Context, _ string, q catalog.PersonalWorkRequest) ([]catalog.PersonalWorkItem, error) {
				calls++
				if q.Limit != wantLimit {
					t.Fatalf("requested page bound %s became %d", limit, q.Limit)
				}
				return []catalog.PersonalWorkItem{}, nil
			}}
			server, request := personalHandlerFixture(t, personalHandlerStore{})
			server.catalog = projectionCatalog(t, repo, nil)
			w := httptest.NewRecorder()
			server.handlePersonalWork(w, request("GET", "/api/v1/me/work?limit="+limit, ""))
			requireControlJSON(t, w, 200, map[string]any{"items": []catalog.PersonalWorkItem{}})
			if calls != 1 {
				t.Fatal("empty bounded page skipped its read", calls)
			}
		})
	}
}

func TestPersonalWorkHandlerContinuationBindsWorkSelection(t *testing.T) {
	first := personalWorkHandlerItem(projectionItem, "drafts")
	second := personalWorkHandlerItem(projectionNext, "drafts")
	calls := 0
	repo := personalWorkHandlerRepository{list: func(_ context.Context, tenant string, q catalog.PersonalWorkRequest) ([]catalog.PersonalWorkItem, error) {
		calls++
		if tenant != projectionTenant || q.Queue != "drafts" || q.Limit != 2 {
			t.Fatal("continuation changed tenant, queue or bound", tenant, q)
		}
		switch calls {
		case 1:
			if q.AfterID != "" {
				t.Fatal("first page acquired a position", q)
			}
			return []catalog.PersonalWorkItem{first, second}, nil
		case 2:
			if q.AfterID != first.ID {
				t.Fatal("continuation must resume after the last returned item", q)
			}
			return []catalog.PersonalWorkItem{second}, nil
		default:
			t.Fatal("invalid continuation reached the repository")
			return nil, nil
		}
	}}
	server, request := personalHandlerFixture(t, personalHandlerStore{})
	server.catalog = projectionCatalog(t, repo, nil)
	w := httptest.NewRecorder()
	server.handlePersonalWork(w, request("GET", "/api/v1/me/work?limit=1", ""))
	var page cursorPage[catalog.PersonalWorkItem]
	if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil || page.NextCursor == "" {
		t.Fatal("lookahead did not create a continuation", w.Code, w.Body, err)
	}
	requireControlJSON(t, w, 200, map[string]any{"items": []catalog.PersonalWorkItem{first}, "nextCursor": page.NextCursor})
	cursor := page.NextCursor
	w = httptest.NewRecorder()
	server.handlePersonalWork(w, request("GET", "/api/v1/me/work?queue=drafts&limit=1&cursor="+url.QueryEscape(cursor), ""))
	requireControlJSON(t, w, 200, map[string]any{"items": []catalog.PersonalWorkItem{second}})
	if w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("continuation response is cacheable", w.Header())
	}
	for _, query := range []string{"queue=approved&limit=1", "queue=drafts&limit=2"} {
		w = httptest.NewRecorder()
		server.handlePersonalWork(w, request("GET", "/api/v1/me/work?"+query+"&cursor="+url.QueryEscape(cursor), ""))
		requireProjectionProblem(t, w, 400, catalog.CodeInvalidArgument)
	}
	// A valid cursor remains private to its original current principal.
	r := request("GET", "/api/v1/me/work?limit=1&cursor="+url.QueryEscape(cursor), "")
	actor, _ := identity.FromContext(r.Context())
	actor.Subject = "another-member"
	actor.MembershipID = projectionNext
	w = httptest.NewRecorder()
	server.handlePersonalWork(w, r.WithContext(identityfixture.WithActor(r.Context(), actor)))
	requireProjectionProblem(t, w, 400, catalog.CodeInvalidArgument)
	if calls != 2 {
		t.Fatal("a changed work selection or principal reached the repository", calls)
	}
}

func TestPersonalWorkHandlerRejectsInvalidPagesBeforeRepository(t *testing.T) {
	for _, query := range []string{"queue=unknown", "queue=drafts&queue=approved", "limit=0", "limit=-1", "limit=101", "limit=invalid", "limit=", "limit=1&limit=2", "cursor=", "cursor=not-a-signed-cursor", "unknown=true"} {
		t.Run(query, func(t *testing.T) {
			repo := personalWorkHandlerRepository{list: func(context.Context, string, catalog.PersonalWorkRequest) ([]catalog.PersonalWorkItem, error) {
				t.Fatal("invalid page reached the work repository")
				return nil, nil
			}}
			server, request := personalHandlerFixture(t, personalHandlerStore{})
			server.catalog = projectionCatalog(t, repo, nil)
			w := httptest.NewRecorder()
			server.handlePersonalWork(w, request("GET", "/api/v1/me/work?"+query, ""))
			requireProjectionProblem(t, w, 400, catalog.CodeInvalidArgument)
		})
	}
}

func TestPersonalWorkHandlerDependencyFailuresRemainProblems(t *testing.T) {
	for _, available := range []bool{false, true} {
		t.Run(map[bool]string{false: "reader-unavailable", true: "read-failed"}[available], func(t *testing.T) {
			var repo catalog.Repository = projectionRepository{}
			calls := 0
			if available {
				repo = personalWorkHandlerRepository{list: func(context.Context, string, catalog.PersonalWorkRequest) ([]catalog.PersonalWorkItem, error) {
					calls++
					return nil, catalog.E(catalog.CodeDependencyUnavailable, "private-fixture: personal storage failed")
				}}
			}
			server, request := personalHandlerFixture(t, personalHandlerStore{})
			server.catalog = projectionCatalog(t, repo, nil)
			w := httptest.NewRecorder()
			server.handlePersonalWork(w, request("GET", "/api/v1/me/work", ""))
			requireProjectionProblem(t, w, 503, catalog.CodeDependencyUnavailable)
			if available && calls != 1 {
				t.Fatal("work read failure did not call its selected dependency", calls)
			}
		})
	}
}
