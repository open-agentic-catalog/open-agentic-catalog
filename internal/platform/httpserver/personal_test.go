package httpserver

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/audit"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/personal"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/clock"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/id"
	"github.com/open-agentic-catalog/open-agentic-catalog/test/identityfixture"
)

// Direct handlers use a scripted store, not a composed Catalog or HTTP server.
type personalHandlerStore struct {
	get  func(context.Context, identity.Actor, string) (personal.Item, error)
	list func(context.Context, identity.Actor, personal.ListRequest) ([]personal.Item, error)
	save func(context.Context, identity.Actor, personal.Item, int64, bool, audit.Record) (personal.Item, error)
}

func (s personalHandlerStore) Get(ctx context.Context, actor identity.Actor, key string) (personal.Item, error) {
	return s.get(ctx, actor, key)
}
func (s personalHandlerStore) List(ctx context.Context, actor identity.Actor, query personal.ListRequest) ([]personal.Item, error) {
	return s.list(ctx, actor, query)
}
func (s personalHandlerStore) Save(ctx context.Context, actor identity.Actor, item personal.Item, expected int64, remove bool, record audit.Record) (personal.Item, error) {
	return s.save(ctx, actor, item, expected, remove, record)
}

type personalHandlerAuthority struct{}

func (personalHandlerAuthority) Admit(context.Context, personal.Reference) error {
	return catalog.E(catalog.CodeForbidden, "resource authority is not selected by these handler tests")
}

const personalHandlerID = "00000000-0000-7000-8000-000000000010"

func personalHandlerFixture(t *testing.T, store personalHandlerStore) (*Server, func(string, string, string) *http.Request) {
	t.Helper()
	serviceClock := clock.NewFake(time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC))
	service, err := personal.New(store, personalHandlerAuthority{}, serviceClock, id.Generator{})
	if err != nil {
		t.Fatal(err)
	}
	server := &Server{personal: service, now: serviceClock.Now, maxBodyBytes: personal.MaxBodyBytes, cursorKey: []byte("0123456789abcdef0123456789abcdef")}
	actor := identity.Actor{TenantID: "00000000-0000-7000-8000-000000000001", MembershipID: "00000000-0000-7000-8000-000000000002", RealmID: "00000000-0000-7000-8000-000000000003", ActorType: identity.ActorHuman, Subject: "library-reader", Roles: []identity.Role{identity.RoleReader}}
	request := func(method, path, body string) *http.Request {
		r := httptest.NewRequestWithContext(t.Context(), method, path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		r.SetPathValue("itemId", personalHandlerID)
		return r.WithContext(identityfixture.WithActor(r.Context(), actor))
	}
	return server, request
}

func TestPersonalHandlerLiteralFilterAndContinuation(t *testing.T) {
	const secondID = "00000000-0000-7000-8000-000000000011"
	label := " Exact %_ "
	calls := 0
	store := personalHandlerStore{list: func(_ context.Context, actor identity.Actor, query personal.ListRequest) ([]personal.Item, error) {
		calls++
		if actor.Subject != "library-reader" || query.Type != "search" || query.Label == nil || *query.Label != label || query.Limit != 2 {
			t.Fatalf("literal filter, owner or limit changed: actor=%+v query=%+v", actor, query)
		}
		if calls == 1 {
			if query.AfterID != "" {
				t.Fatal("first page acquired a continuation")
			}
			return []personal.Item{{ID: personalHandlerID}, {ID: secondID}}, nil
		}
		if query.AfterID != personalHandlerID {
			t.Fatal("continuation lost its position", query.AfterID)
		}
		return []personal.Item{{ID: secondID}}, nil
	}}
	server, request := personalHandlerFixture(t, store)
	path := "/api/v1/personal/items?type=search&limit=1&label=" + url.QueryEscape(label)
	w := httptest.NewRecorder()
	server.handlePersonalList(w, request("GET", path, ""))
	var page struct {
		Items      []personal.Item `json:"items"`
		NextCursor string          `json:"nextCursor"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil || w.Code != 200 || len(page.Items) != 1 || page.Items[0].ID != personalHandlerID || page.NextCursor == "" || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("invalid private first page", w.Code, w.Body, err)
	}
	cursor := page.NextCursor
	w = httptest.NewRecorder()
	server.handlePersonalList(w, request("GET", path+"&cursor="+url.QueryEscape(cursor), ""))
	page.Items, page.NextCursor = nil, ""
	if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil || w.Code != 200 || len(page.Items) != 1 || page.Items[0].ID != secondID || page.NextCursor != "" {
		t.Fatal("invalid private continuation", w.Code, w.Body, err)
	}
	// A signed cursor must not authorize a different literal label.
	w = httptest.NewRecorder()
	server.handlePersonalList(w, request("GET", "/api/v1/personal/items?type=search&limit=1&label=changed&cursor="+url.QueryEscape(cursor), ""))
	if w.Code != 400 || calls != 2 {
		t.Fatal("changed filter reached the store", w.Code, calls)
	}
}

func TestPersonalHandlerGetAndStoreErrors(t *testing.T) {
	for _, code := range []catalog.ErrorCode{"", catalog.CodeNotFound, catalog.CodeForbidden, catalog.CodeDependencyUnavailable} {
		t.Run(string(code), func(t *testing.T) {
			store := personalHandlerStore{get: func(_ context.Context, actor identity.Actor, key string) (personal.Item, error) {
				if key != personalHandlerID || actor.Subject != "library-reader" {
					t.Fatal("read changed owner or key", actor, key)
				}
				if code != "" {
					return personal.Item{}, catalog.E(code, "saved item is unavailable")
				}
				return personal.Item{ID: key, Revision: 7, Input: personal.Input{Label: "Private saved search"}}, nil
			}}
			server, request := personalHandlerFixture(t, store)
			w := httptest.NewRecorder()
			server.handlePersonalGet(w, request("GET", "/api/v1/personal/items/"+personalHandlerID, ""))
			want := map[catalog.ErrorCode]int{"": 200, catalog.CodeNotFound: 404, catalog.CodeForbidden: 403, catalog.CodeDependencyUnavailable: 503}[code]
			if w.Code != want {
				t.Fatal("read error status", w.Code, w.Body)
			}
			if code == "" {
				var item personal.Item
				if err := json.Unmarshal(w.Body.Bytes(), &item); err != nil || item.ID != personalHandlerID || item.Revision != 7 || item.Label != "Private saved search" || w.Header().Get("ETag") != `"rev-7"` || w.Header().Get("Cache-Control") != "no-store" {
					t.Fatal("private read lost its body or headers", w.Header(), w.Body, err)
				}
			} else if w.Header().Get("ETag") != "" || strings.Contains(w.Body.String(), "Private saved search") {
				t.Fatal("failed read disclosed the saved item", w.Header(), w.Body)
			}
		})
	}
}

func TestPersonalHandlerCommandReceipts(t *testing.T) {
	for _, method := range []string{http.MethodPost, http.MethodPatch, http.MethodDelete} {
		t.Run(method, func(t *testing.T) {
			calls := 0
			store := personalHandlerStore{get: func(context.Context, identity.Actor, string) (personal.Item, error) {
				if method == http.MethodPost {
					t.Fatal("creation read an existing item")
				}
				return personal.Item{ID: personalHandlerID, Revision: 3, Input: personal.Input{Type: "search", Label: "Private label", Search: &personal.Criteria{Text: "private-query-sentinel"}}}, nil
			}, save: func(_ context.Context, actor identity.Actor, item personal.Item, expected int64, remove bool, _ audit.Record) (personal.Item, error) {
				calls++
				want := int64(3)
				if method == http.MethodPost {
					want = 0
				}
				if actor.Subject != "library-reader" || expected != want || remove != (method == http.MethodDelete) || item.Revision != want+1 {
					t.Fatal("command changed owner, revision or removal", actor, item, expected, remove)
				}
				if !remove && (item.Search == nil || item.Search.Text != "private-query-sentinel") {
					t.Fatal("command dropped the saved criteria", item)
				}
				return item, nil
			}}
			server, request := personalHandlerFixture(t, store)
			body := `{"type":"search","label":"Private label","search":{"text":"private-query-sentinel"}}`
			if method == http.MethodDelete {
				body = ""
			}
			r := request(method, "/api/v1/personal/items", body)
			wantStatus, wantRevision := 200, int64(4)
			wantETag := `"rev-4"`
			if method == http.MethodPost {
				r.SetPathValue("itemId", "")
				wantStatus, wantRevision = 201, 1
				wantETag = `"rev-1"`
			} else {
				r.Header.Set("If-Match", `"rev-3"`)
			}
			w := httptest.NewRecorder()
			server.handlePersonalSave(w, r)
			var receipt feedbackReceipt
			if err := json.Unmarshal(w.Body.Bytes(), &receipt); err != nil || w.Code != wantStatus || receipt.Revision != wantRevision || !identity.ValidTenantID(receipt.ID) || calls != 1 {
				t.Fatal("invalid command receipt", w.Code, w.Body, calls, err)
			}
			if w.Header().Get("Cache-Control") != "no-store" || w.Header().Get("ETag") != wantETag || strings.Contains(w.Body.String(), "private-query-sentinel") || strings.Contains(w.Body.String(), "Private label") {
				t.Fatal("command receipt disclosed private criteria or lost headers", w.Header(), w.Body)
			}
		})
	}
}

func TestPersonalHandlerRejectsInvalidCommandsBeforeStoreAccess(t *testing.T) {
	for _, tc := range []struct {
		name, method, body, etag string
		transfer                 bool
		status                   int
	}{
		{"missing revision", "PATCH", `{}`, "", false, 412},
		{"delete body", "DELETE", `{}`, `"rev-3"`, false, 400},
		{"chunked delete", "DELETE", "", `"rev-3"`, true, 400},
		{"unknown field", "POST", `{"type":"search","label":"Valid search","search":{"text":"query"},"owner":"forged"}`, "", false, 400},
		{"duplicate field", "POST", `{"type":"search","type":"search","label":"Valid search","search":{"text":"query"}}`, "", false, 400},
		{"malformed document", "POST", `{`, "", false, 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Unselected fake methods panic if a rejected request reaches storage.
			server, request := personalHandlerFixture(t, personalHandlerStore{})
			r := request(tc.method, "/api/v1/personal/items", tc.body)
			if tc.method == http.MethodPost {
				r.SetPathValue("itemId", "")
			}
			r.Header.Set("If-Match", tc.etag)
			if tc.transfer {
				r.TransferEncoding = []string{"chunked"}
			}
			w := httptest.NewRecorder()
			server.handlePersonalSave(w, r)
			if w.Code != tc.status {
				t.Fatal("invalid command status", w.Code, w.Body)
			}
		})
	}
}
