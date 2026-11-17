package httpserver

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/audit"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/collections"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/clock"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/id"
	"github.com/open-agentic-catalog/open-agentic-catalog/test/identityfixture"
)

// Handler units use a scripted port and response recorders. They do not compose
// the application, start a listener or qualify SQL, replay or real transport.
type collectionHandlerStore struct {
	get     func(context.Context, identity.Actor, string) (collections.Collection, error)
	list    func(context.Context, identity.Actor, collections.ListRequest) ([]collections.Collection, error)
	members func(context.Context, identity.Actor, string, collections.MemberRequest) (collections.MemberPage, error)
	target  func(context.Context, identity.Actor, string) (string, error)
	save    func(context.Context, identity.Actor, collections.Collection, int64, collections.Operation, string, audit.Record) (collections.Collection, error)
}

func (s collectionHandlerStore) Get(c context.Context, a identity.Actor, key string) (collections.Collection, error) {
	return s.get(c, a, key)
}
func (s collectionHandlerStore) List(c context.Context, a identity.Actor, q collections.ListRequest) ([]collections.Collection, error) {
	return s.list(c, a, q)
}
func (s collectionHandlerStore) Members(c context.Context, a identity.Actor, key string, q collections.MemberRequest) (collections.MemberPage, error) {
	return s.members(c, a, key, q)
}
func (s collectionHandlerStore) Target(c context.Context, a identity.Actor, key string) (string, error) {
	return s.target(c, a, key)
}
func (s collectionHandlerStore) Save(c context.Context, a identity.Actor, next collections.Collection, expected int64, op collections.Operation, listing string, evidence audit.Record) (collections.Collection, error) {
	return s.save(c, a, next, expected, op, listing, evidence)
}

func collectionHandlerItem() collections.Collection {
	return collections.Collection{ID: projectionItem, NamespaceID: projectionResource, Name: "Shared tools", Description: "private-fixture: authored description", Revision: 3, CreatedAt: projectionTime.Add(-time.Hour), UpdatedAt: projectionTime.Add(-time.Minute)}
}

func collectionHandlerFixture(t *testing.T, store collectionHandlerStore) (*Server, func(string, string, string) *http.Request) {
	t.Helper()
	serviceClock := clock.NewFake(projectionTime)
	service, err := collections.New(store, serviceClock, id.Generator{})
	if err != nil {
		t.Fatal(err)
	}
	server := &Server{collections: service, now: serviceClock.Now, maxBodyBytes: collections.MaxBodyBytes, cursorKey: []byte("0123456789abcdef0123456789abcdef")}
	a := projectionActor(identity.RoleTenantAdmin)
	request := func(method, path, body string) *http.Request {
		r := httptest.NewRequestWithContext(t.Context(), method, path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		r.SetPathValue("collectionId", projectionItem)
		return r.WithContext(identityfixture.WithActor(r.Context(), a))
	}
	return server, request
}

func TestCollectionHandlerGetHeadersAndClassifiedErrors(t *testing.T) {
	for _, code := range []catalog.ErrorCode{"", catalog.CodeNotFound, catalog.CodeForbidden, catalog.CodeDependencyUnavailable} {
		t.Run(string(code), func(t *testing.T) {
			calls := 0
			store := collectionHandlerStore{get: func(ctx context.Context, a identity.Actor, key string) (collections.Collection, error) {
				calls++
				current, ok := identity.FromContext(ctx)
				if !ok || a.CommandPrincipal() != current.CommandPrincipal() || a.TenantID != projectionTenant || key != projectionItem {
					t.Fatal("read changed principal, tenant or key", a, key)
				}
				if code != "" {
					return collections.Collection{}, catalog.Wrap(code, "collection is unavailable", errors.New("private-fixture: storage failure"))
				}
				return collectionHandlerItem(), nil
			}}
			s, request := collectionHandlerFixture(t, store)
			w := httptest.NewRecorder()
			s.handleCollectionGet(w, request("GET", "/api/v1/collections/"+projectionItem, ""))
			if calls != 1 {
				t.Fatal("read count", calls)
			}
			if code == "" {
				requireControlJSON(t, w, 200, collectionHandlerItem())
				if w.Header().Get("ETag") != `"rev-3"` || w.Header().Get("Cache-Control") != "no-store" {
					t.Fatal("private read headers", w.Header())
				}
			} else {
				requireProjectionProblem(t, w, map[catalog.ErrorCode]int{catalog.CodeNotFound: 404, catalog.CodeForbidden: 403, catalog.CodeDependencyUnavailable: 503}[code], code)
				if w.Header().Get("ETag") != "" {
					t.Fatal("failed read retained a validator")
				}
			}
		})
	}
}

func TestCollectionHandlerDirectoryContinuationBindsNamespace(t *testing.T) {
	first, second := collectionHandlerItem(), collectionHandlerItem()
	second.ID = projectionNext
	calls := 0
	store := collectionHandlerStore{list: func(_ context.Context, a identity.Actor, q collections.ListRequest) ([]collections.Collection, error) {
		calls++
		if a.TenantID != projectionTenant || q.NamespaceID != projectionResource || q.Limit != 2 {
			t.Fatalf("directory binding=%+v actor=%+v", q, a)
		}
		if calls == 1 {
			if q.AfterID != "" {
				t.Fatal("first page acquired a cursor", q)
			}
			return []collections.Collection{first, second}, nil
		}
		if q.AfterID != first.ID {
			t.Fatal("wrong continuation", q)
		}
		return []collections.Collection{second}, nil
	}}
	s, request := collectionHandlerFixture(t, store)
	path := "/api/v1/collections?limit=1&namespaceId=" + projectionResource
	w := httptest.NewRecorder()
	s.handleCollectionList(w, request("GET", path, ""))
	var page cursorPage[collections.Collection]
	if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil || w.Code != 200 || !reflect.DeepEqual(page.Items, []collections.Collection{first}) || page.NextCursor == "" || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("first page=%+v status=%d err=%v", page, w.Code, err)
	}
	cursor := page.NextCursor
	w = httptest.NewRecorder()
	s.handleCollectionList(w, request("GET", path+"&cursor="+url.QueryEscape(cursor), ""))
	page = cursorPage[collections.Collection]{}
	if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil || w.Code != 200 || !reflect.DeepEqual(page.Items, []collections.Collection{second}) || page.NextCursor != "" {
		t.Fatalf("continued page=%+v status=%d err=%v", page, w.Code, err)
	}
	for _, suffix := range []string{"namespaceId=" + projectionNext + "&limit=1&cursor=" + url.QueryEscape(cursor), "limit=0", "limit=101", "limit=1&limit=2", "unknown=1", "cursor=broken"} {
		w = httptest.NewRecorder()
		s.handleCollectionList(w, request("GET", "/api/v1/collections?"+suffix, ""))
		requireProjectionProblem(t, w, 400, catalog.CodeInvalidArgument)
		if calls != 2 {
			t.Fatal("invalid directory request reached selection")
		}
	}
}

func TestCollectionHandlerMemberCursorBindsBothRevisions(t *testing.T) {
	parent := collectionHandlerItem()
	generation := "7"
	first := controlReadFixture(t).record.Summary()
	second := first
	second.Listing.ID, second.Release.ID = projectionNext, projectionNext
	selected := 0
	store := collectionHandlerStore{members: func(_ context.Context, a identity.Actor, key string, q collections.MemberRequest) (collections.MemberPage, error) {
		if a.TenantID != projectionTenant || key != parent.ID {
			t.Fatal("member scope changed", a, key)
		}
		page := collections.MemberPage{Collection: parent, Generation: generation, Items: []catalog.CatalogSummary{}}
		if q.Limit == 0 {
			if q.Revision != 0 || q.Generation != "" {
				t.Fatal("binding read supplied client revisions", q)
			}
			return page, nil
		}
		selected++
		if q.Limit != 2 || q.Revision != parent.Revision || q.Generation != generation {
			t.Fatal("selection lost snapshot or lookahead", q)
		}
		if q.AfterID == "" {
			page.Items = []catalog.CatalogSummary{first, second}
		} else {
			if q.AfterID != first.Listing.ID {
				t.Fatal("member continuation key", q)
			}
			page.Items = []catalog.CatalogSummary{second}
		}
		return page, nil
	}}
	s, request := collectionHandlerFixture(t, store)
	path := "/api/v1/collections/" + parent.ID + "/members?limit=1"
	w := httptest.NewRecorder()
	s.handleCollectionMembers(w, request("GET", path, ""))
	var page cursorPage[catalog.CatalogSummary]
	if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil || w.Code != 200 || len(page.Items) != 1 || page.Items[0].Listing.ID != first.Listing.ID || page.NextCursor == "" || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("member page=%+v status=%d err=%v", page, w.Code, err)
	}
	cursor := page.NextCursor
	w = httptest.NewRecorder()
	s.handleCollectionMembers(w, request("GET", path+"&cursor="+url.QueryEscape(cursor), ""))
	page = cursorPage[catalog.CatalogSummary]{}
	if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil || w.Code != 200 || len(page.Items) != 1 || page.Items[0].Listing.ID != second.Listing.ID || page.NextCursor != "" {
		t.Fatalf("member continuation=%+v status=%d err=%v", page, w.Code, err)
	}
	for _, change := range []string{"parent revision", "release generation"} {
		t.Run(change, func(t *testing.T) {
			if change == "parent revision" {
				parent.Revision++
			} else {
				generation = "8"
			}
			w = httptest.NewRecorder()
			s.handleCollectionMembers(w, request("GET", path+"&cursor="+url.QueryEscape(cursor), ""))
			requireProjectionProblem(t, w, 400, catalog.CodeInvalidArgument)
			if selected != 2 {
				t.Fatal("stale snapshot reached member selection")
			}
			parent.Revision = 3
			generation = "7"
		})
	}
}

func TestCollectionHandlerCommandReceiptsAndExactBindings(t *testing.T) {
	for _, tc := range []struct {
		name, method, body string
		op                 collections.Operation
		status             int
	}{
		{"create", "POST", `{"namespaceId":"` + projectionResource + `","name":" New tools ","description":" Shared text "}`, collections.Create, 201},
		{"update", "PATCH", `{"name":"Edited tools","description":""}`, collections.Update, 200},
		{"add", "POST", `{"listingId":"` + projectionNext + `"}`, collections.Add, 201},
		{"remove", "DELETE", "", collections.Remove, 200},
		{"delete", "DELETE", "", collections.Delete, 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			old := collectionHandlerItem()
			var saved collections.Collection
			calls, targets := 0, 0
			store := collectionHandlerStore{
				get: func(_ context.Context, a identity.Actor, key string) (collections.Collection, error) {
					if tc.op == collections.Create {
						t.Fatal("create read an existing collection")
					}
					if a.TenantID != projectionTenant || key != old.ID {
						t.Fatal("command read changed tenant or parent", a, key)
					}
					return old, nil
				},
				target: func(_ context.Context, _ identity.Actor, key string) (string, error) {
					targets++
					if key != projectionNext {
						t.Fatal("target identity", key)
					}
					return projectionResource, nil
				},
				save: func(_ context.Context, a identity.Actor, next collections.Collection, expected int64, op collections.Operation, listing string, e audit.Record) (collections.Collection, error) {
					calls++
					saved = next
					wantExpected, wantRevision := int64(3), int64(4)
					wantName, wantDescription := old.Name, old.Description
					if tc.op == collections.Create {
						wantExpected, wantRevision = 0, 1
						wantName, wantDescription = "New tools", "Shared text"
					}
					if tc.op == collections.Update {
						wantName, wantDescription = "Edited tools", ""
					}
					wantListing := ""
					if tc.op == collections.Add || tc.op == collections.Remove {
						wantListing = projectionNext
					}
					if a.TenantID != projectionTenant || expected != wantExpected || next.Revision != wantRevision || op != tc.op || listing != wantListing || next.Name != wantName || next.Description != wantDescription || next.NamespaceID != projectionResource || !next.UpdatedAt.Equal(projectionTime) {
						t.Fatalf("wrong command: actor=%+v next=%+v expected=%d op=%s listing=%s", a, next, expected, op, listing)
					}
					if e.TargetID != next.ID || e.TargetType != "collection" || e.Actor != a.MembershipID || e.TenantID != a.TenantID || e.Action != "collection."+string(tc.op) || !e.At.Equal(next.UpdatedAt) {
						t.Fatal("receipt command lost required evidence", e)
					}
					facts := map[string]any{"revision": wantRevision, "namespaceId": projectionResource}
					if wantListing != "" {
						facts["listingId"] = wantListing
					}
					actualFacts, actualErr := json.Marshal(e.Details["facts"])
					wantedFacts, wantedErr := json.Marshal(facts)
					if actualErr != nil || wantedErr != nil || string(actualFacts) != string(wantedFacts) {
						t.Fatalf("command evidence facts=%v want=%v", e.Details["facts"], facts)
					}
					return next, nil
				},
			}
			s, request := collectionHandlerFixture(t, store)
			path := "/api/v1/collections/" + old.ID
			if tc.op == collections.Create {
				path = "/api/v1/collections"
			}
			if tc.op == collections.Add {
				path += "/members"
			}
			if tc.op == collections.Remove {
				path += "/members/" + projectionNext
			}
			r := request(tc.method, path, tc.body)
			r.Header.Set("If-Match", `"rev-3"`)
			w := httptest.NewRecorder()
			mux := http.NewServeMux()
			s.collectionRoutes(mux)
			mux.ServeHTTP(w, r)
			requireControlJSON(t, w, tc.status, feedbackReceipt{ID: saved.ID, Revision: saved.Revision})
			wantTargets, wantETag := 0, `"rev-4"`
			if tc.op == collections.Add {
				wantTargets = 1
			}
			if tc.op == collections.Create {
				wantETag = `"rev-1"`
			}
			if calls != 1 || targets != wantTargets || w.Header().Get("ETag") != wantETag || w.Header().Get("Cache-Control") != "no-store" || strings.Contains(w.Body.String(), saved.Name) || saved.Description != "" && strings.Contains(w.Body.String(), saved.Description) {
				t.Fatalf("command receipt or reads changed: calls=%d targets=%d headers=%v body=%s", calls, targets, w.Header(), w.Body)
			}
		})
	}
}

func TestCollectionHandlerInvalidCommandsNeverReachStorage(t *testing.T) {
	for _, tc := range []struct {
		name, method, body, etag string
		status                   int
	}{
		{"missing precondition", "PATCH", `{"name":"New"}`, "", 412},
		{"null name", "PATCH", `{"name":null}`, `"rev-3"`, 400},
		{"null description", "PATCH", `{"description":null}`, `"rev-3"`, 400},
		{"duplicate field", "PATCH", `{"name":"one","name":"two"}`, `"rev-3"`, 400},
		{"owner transfer", "PATCH", `{"namespaceId":"` + projectionResource + `"}`, `"rev-3"`, 400},
		{"wrong case", "PATCH", `{"Name":"New"}`, `"rev-3"`, 400},
		{"member oversize", "POST", `{"listingId":"` + projectionNext + `"}` + strings.Repeat(" ", collections.MaxMemberBodyBytes), `"rev-3"`, 400},
		{"delete body", "DELETE", `{}`, `"rev-3"`, 400},
		{"chunked delete", "DELETE", ``, `"rev-3"`, 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, request := collectionHandlerFixture(t, collectionHandlerStore{})
			r := request(tc.method, "/api/v1/collections/"+projectionItem, tc.body)
			r.Header.Set("If-Match", tc.etag)
			if tc.name == "chunked delete" {
				r.TransferEncoding = []string{"chunked"}
			}
			w := httptest.NewRecorder()
			s.handleCollectionChange(w, r)
			code := catalog.CodeInvalidArgument
			if tc.status == 412 {
				code = catalog.CodePreconditionFailed
			}
			requireProjectionProblem(t, w, tc.status, code)
		})
	}
	for _, body := range []string{`{"namespaceId":"` + projectionResource + `","name":null}`, `{"namespaceId":"` + projectionResource + `","name":"one","name":"two"}`, `{"namespaceId":"` + projectionResource + `","name":" "}`, `{"namespaceId":"` + projectionResource + `","name":"Name"}` + strings.Repeat(" ", collections.MaxBodyBytes)} {
		s, request := collectionHandlerFixture(t, collectionHandlerStore{})
		w := httptest.NewRecorder()
		s.handleCollectionCreate(w, request("POST", "/api/v1/collections", body))
		requireProjectionProblem(t, w, 400, catalog.CodeInvalidArgument)
	}
}

func TestCollectionHandlerReadErrorsDiscardPartialResults(t *testing.T) {
	for _, operation := range []string{"directory", "snapshot", "members"} {
		t.Run(operation, func(t *testing.T) {
			calls := 0
			entry := controlReadFixture(t).record.Summary()
			failure := catalog.Wrap(catalog.CodeDependencyUnavailable, "collection storage unavailable", errors.New("private-fixture: cause"))
			store := collectionHandlerStore{
				list: func(context.Context, identity.Actor, collections.ListRequest) ([]collections.Collection, error) {
					calls++
					return []collections.Collection{collectionHandlerItem()}, failure
				},
				members: func(_ context.Context, _ identity.Actor, _ string, q collections.MemberRequest) (collections.MemberPage, error) {
					calls++
					page := collections.MemberPage{Collection: collectionHandlerItem(), Generation: "7"}
					if q.Limit == 0 && operation == "members" {
						return page, nil
					}
					page.Items = []catalog.CatalogSummary{entry}
					return page, failure
				},
			}
			s, request := collectionHandlerFixture(t, store)
			w := httptest.NewRecorder()
			if operation == "directory" {
				s.handleCollectionList(w, request("GET", "/api/v1/collections", ""))
			} else {
				s.handleCollectionMembers(w, request("GET", "/api/v1/collections/"+projectionItem+"/members", ""))
			}
			requireProjectionProblem(t, w, 503, catalog.CodeDependencyUnavailable)
			wantCalls := 1
			if operation == "members" {
				wantCalls = 2
			}
			var envelope map[string]json.RawMessage
			if err := json.Unmarshal(w.Body.Bytes(), &envelope); err != nil {
				t.Fatal(err)
			}
			if calls != wantCalls || envelope["items"] != nil || envelope["collection"] != nil || w.Header().Get("ETag") != "" {
				t.Fatalf("failed read leaked state or skipped the intended fault: calls=%d headers=%v body=%s", calls, w.Header(), w.Body)
			}
		})
	}
}

func TestCollectionHandlerFailedSavesHaveNoSuccessReceipt(t *testing.T) {
	for _, create := range []bool{false, true} {
		t.Run(map[bool]string{false: "update", true: "create"}[create], func(t *testing.T) {
			var saveErr error
			saves := 0
			store := collectionHandlerStore{
				get: func(context.Context, identity.Actor, string) (collections.Collection, error) {
					return collectionHandlerItem(), nil
				},
				save: func(_ context.Context, _ identity.Actor, next collections.Collection, _ int64, _ collections.Operation, _ string, _ audit.Record) (collections.Collection, error) {
					saves++
					return next, saveErr
				},
			}
			s, request := collectionHandlerFixture(t, store)
			command := func(etag string) *httptest.ResponseRecorder {
				t.Helper()
				w := httptest.NewRecorder()
				if create {
					s.handleCollectionCreate(w, request("POST", "/api/v1/collections", `{"namespaceId":"`+projectionResource+`","name":"New tools"}`))
				} else {
					r := request("PATCH", "/api/v1/collections/"+projectionItem, `{"name":"Edited tools"}`)
					r.Header.Set("If-Match", etag)
					s.handleCollectionChange(w, r)
				}
				return w
			}
			control := command(`"rev-3"`)
			wantStatus := 200
			if create {
				wantStatus = 201
			}
			if control.Code != wantStatus || saves != 1 || control.Header().Get("ETag") == "" {
				t.Fatal("successful save control failed", control.Code, control.Body, saves)
			}
			saveErr = catalog.Wrap(catalog.CodeDependencyUnavailable, "collection save unavailable", errors.New("private-fixture: audit write failed"))
			w := command(`"rev-3"`)
			requireProjectionProblem(t, w, 503, catalog.CodeDependencyUnavailable)
			var body map[string]json.RawMessage
			if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if saves != 2 || body["id"] != nil || body["revision"] != nil || w.Header().Get("ETag") != "" {
				t.Fatal("failed save emitted a success receipt", w.Header(), w.Body, saves)
			}
			if !create {
				stale := command(`"rev-2"`)
				requireProjectionProblem(t, stale, 412, catalog.CodePreconditionFailed)
				if saves != 2 {
					t.Fatal("stale edit reached the failing save")
				}
			}
		})
	}
}
