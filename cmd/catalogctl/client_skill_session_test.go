// Unit tests exercise isolated validation and controlled dependencies.
package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/jsondoc"
)

// clientSkillSessionSearch admits the public Search fields without exporting the
// HTTP DTO. Pointers distinguish omitted controls from explicit empty values.
// The demo owns general query semantics; synthetic pages support only text,
// distinct kinds and a sufficient bounded limit.
type clientSkillSessionSearch struct {
	Origins     *[]catalog.SearchResultType `json:"origins"`
	NamespaceID *string                     `json:"namespaceId"`
	Text        string                      `json:"text"`
	Kinds       []catalog.Kind              `json:"kinds"`
	Tags        *[]string                   `json:"tags"`
	Publisher   *string                     `json:"publisher"`
	Visibility  *catalog.Visibility         `json:"visibility"`
	Limit       *int                        `json:"limit"`
	Cursor      *string                     `json:"cursor"`
}

// clientSkillSessionHandler serves explicit synthetic read cases and delegates
// ordinary reads to the demo. It is not an alternative search implementation.
func clientSkillSessionHandler(demo http.Handler, plugin, large catalog.CatalogRecord) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer local-reader" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Request-ID", "client-skill-fixture")
		write := func(value any) { _ = json.NewEncoder(w).Encode(value) }
		if r.Method == http.MethodGet && r.URL.Path == "/api/v1/capabilities" {
			demo.ServeHTTP(w, r)
			return
		}
		if r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/api/v1/catalog/resources/") {
			switch strings.TrimPrefix(r.URL.Path, "/api/v1/catalog/resources/") {
			case clientSkillReleaseID:
				write(plugin)
			case large.Release.ID:
				write(large)
			default:
				demo.ServeHTTP(w, r)
			}
			return
		}
		if r.Method == http.MethodPost && r.URL.Path == "/api/v1/catalog/search" {
			// Inspect the complete bounded body so the fixture cannot hide a
			// client encoding error by forwarding only its first decoded value.
			body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 8192))
			if err != nil || !bytes.HasPrefix(bytes.TrimSpace(body), []byte("{")) {
				http.Error(w, "invalid query", http.StatusBadRequest)
				return
			}
			var query clientSkillSessionSearch
			if jsondoc.Decode(body, &query) != nil || query.Limit != nil && (*query.Limit < 1 || *query.Limit > catalog.MaxCatalogPageSize) {
				http.Error(w, "invalid query", http.StatusBadRequest)
				return
			}
			for i, kind := range query.Kinds {
				if !kind.Valid() || slices.Contains(query.Kinds[:i], kind) {
					http.Error(w, "invalid kinds", http.StatusBadRequest)
					return
				}
			}
			// Exact reserved selectors cannot intercept ordinary lexical phrases
			// or genuine continuation requests intended for the governed demo.
			synthetic := query.Text == "triage" || query.Text == "plugin" || query.Text == "large" || query.Text == "" && len(query.Kinds) == 1 && query.Kinds[0] == catalog.KindPlugin
			if synthetic {
				if query.Origins != nil || query.NamespaceID != nil || query.Tags != nil || query.Publisher != nil || query.Visibility != nil || query.Cursor != nil {
					http.Error(w, "synthetic scenario supports only text, kinds and limit; filters and continuation are unsupported", http.StatusBadRequest)
					return
				}
				var page catalog.CatalogSearchResponse
				if query.Text == "triage" {
					page = clientSkillExternalPage()
				} else {
					record := plugin
					if query.Text == "large" {
						record = large
					}
					page = catalog.CatalogSearchResponse{Tier: "core", Profile: catalog.CoreSearchProfile, Generation: "fixture", DurableTotal: 1, Items: []catalog.CatalogSearchResult{{ID: "local:" + record.Release.ID, Type: catalog.SearchResultLocal, Identifier: record.Resource.CanonicalIdentifier, Kind: record.Resource.Kind, DisplayName: record.ListingRevision.DisplayName, Version: "1.0.0", VersionPresent: true, Score: 80, Local: new(record.Summary().SearchLocal())}}}
				}
				page = clientSkillFilterKinds(page, query.Kinds)
				if query.Limit != nil && *query.Limit < len(page.Items) {
					http.Error(w, "synthetic scenario limit is smaller than its kind-filtered result count; pagination is unsupported", http.StatusBadRequest)
					return
				}
				write(page)
				return
			}
			r.Body = io.NopCloser(bytes.NewReader(body))
			demo.ServeHTTP(w, r)
			return
		}
		http.Error(w, "read fixture only", http.StatusForbidden)
	})
}

// clientSkillFilterKinds keeps synthetic responses and their result counts
// consistent when a model narrows a scenario by resource kind.
func clientSkillFilterKinds(page catalog.CatalogSearchResponse, kinds []catalog.Kind) catalog.CatalogSearchResponse {
	if len(kinds) == 0 {
		return page
	}
	page.Items = slices.DeleteFunc(page.Items, func(item catalog.CatalogSearchResult) bool { return !slices.Contains(kinds, item.Kind) })
	page.DurableTotal, page.LiveCount = 0, 0
	for _, item := range page.Items {
		if item.Type == catalog.SearchResultLive {
			page.LiveCount++
		} else {
			page.DurableTotal++
		}
	}
	return page
}

// TestClientSkillSessionReads checks the model fixture itself without starting
// a model or long-lived session. Synthetic kind filters must not invent matches.
func TestClientSkillSessionReads(t *testing.T) {
	t.Parallel()
	plugin, large := clientSkillPluginRecord(t), clientSkillLargeRecord(t)
	handler := clientSkillSessionHandler(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("synthetic read unexpectedly reached demo")
	}), plugin, large)
	for _, test := range []struct {
		name, body    string
		kinds         []catalog.Kind
		durable, live int
		partial       bool
	}{
		{"external", `{"text":"triage","limit":5}`, []catalog.Kind{catalog.KindSkill, catalog.KindAgent}, 1, 1, true},
		{"external omitted limit", `{"text":"triage"}`, []catalog.Kind{catalog.KindSkill, catalog.KindAgent}, 1, 1, true},
		{"external maximum limit", `{"text":"triage","limit":100}`, []catalog.Kind{catalog.KindSkill, catalog.KindAgent}, 1, 1, true},
		{"external minimum limit", `{"text":"triage","limit":2}`, []catalog.Kind{catalog.KindSkill, catalog.KindAgent}, 1, 1, true},
		{"external filtered minimum", `{"text":"triage","kinds":["skill"],"limit":1}`, []catalog.Kind{catalog.KindSkill}, 1, 0, true},
		{"external skill", `{"text":"triage","kinds":["skill"],"limit":5}`, []catalog.Kind{catalog.KindSkill}, 1, 0, true},
		{"external agent", `{"text":"triage","kinds":["agent"],"limit":5}`, []catalog.Kind{catalog.KindAgent}, 0, 1, true},
		{"external no match", `{"text":"triage","kinds":["plugin"],"limit":5}`, nil, 0, 0, true},
		{"plugin", `{"text":"plugin","limit":5}`, []catalog.Kind{catalog.KindPlugin}, 1, 0, false},
		{"plugin minimum limit", `{"text":"plugin","limit":1}`, []catalog.Kind{catalog.KindPlugin}, 1, 0, false},
		{"plugin filter", `{"kinds":["plugin"],"limit":5}`, []catalog.Kind{catalog.KindPlugin}, 1, 0, false},
		{"plugin wrong kind", `{"text":"plugin","kinds":["agent"],"limit":5}`, nil, 0, 0, false},
		{"large", `{"text":"large","kinds":["agent"],"limit":5}`, []catalog.Kind{catalog.KindAgent}, 1, 0, false},
		{"large wrong kind", `{"text":"large","kinds":["plugin"],"limit":5}`, nil, 0, 0, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/v1/catalog/search", strings.NewReader(test.body))
			request.Header.Set("Authorization", "Bearer local-reader")
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			var page catalog.CatalogSearchResponse
			if err := json.Unmarshal(response.Body.Bytes(), &page); response.Code != http.StatusOK || err != nil {
				t.Fatalf("invalid fixture response: status %d: %v", response.Code, err)
			}
			kinds := make([]catalog.Kind, 0, len(page.Items))
			for _, item := range page.Items {
				kinds = append(kinds, item.Kind)
				if item.Type == catalog.SearchResultLocal {
					if item.Local == nil || item.ID != "local:"+item.Local.Release.ID {
						t.Fatal("synthetic local result lost release identity")
					}
					if response.Body.Len() > 8192 || bytes.Contains(response.Body.Bytes(), []byte(`"representations"`)) || bytes.Contains(response.Body.Bytes(), []byte(`"findings"`)) || bytes.Contains(response.Body.Bytes(), []byte(`"documents"`)) {
						t.Fatal("synthetic Search included full record evidence")
					}
				}
			}
			if !slices.Equal(kinds, test.kinds) || page.DurableTotal != test.durable || page.LiveCount != test.live || page.Partial != test.partial {
				t.Fatalf("unexpected kinds/counts/partial: %v %d/%d %t", kinds, page.DurableTotal, page.LiveCount, page.Partial)
			}
		})
	}
	for _, record := range []catalog.CatalogRecord{plugin, large} {
		request := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/catalog/resources/"+record.Release.ID, nil)
		request.Header.Set("Authorization", "Bearer local-reader")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		var got catalog.CatalogRecord
		if err := json.Unmarshal(response.Body.Bytes(), &got); response.Code != http.StatusOK || err != nil || got.Release.ID != record.Release.ID || got.Resource.Kind != record.Resource.Kind {
			t.Fatalf("selected fixture record differs from search evidence: status %d: %v", response.Code, err)
		}
		want, err := json.Marshal(record)
		if err != nil || !bytes.Equal(bytes.TrimSpace(response.Body.Bytes()), want) {
			t.Fatal("selected exact record changed retained evidence or integer precision")
		}
	}
}

// TestClientSkillSessionGuard verifies that only the three authorized read
// routes can reach the demo and that malformed/oversized search is bounded.
func TestClientSkillSessionGuard(t *testing.T) {
	t.Parallel()
	var calls []string
	demo := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.Method+" "+r.URL.Path)
		if r.Method == http.MethodPost {
			var query catalog.SearchQuery
			if json.NewDecoder(r.Body).Decode(&query) != nil || query.Text != "order" || query.Limit != 5 {
				t.Error("forwarded search body was lost")
			}
		}
		w.WriteHeader(http.StatusNoContent)
	})
	handler := clientSkillSessionHandler(demo, clientSkillPluginRecord(t), clientSkillLargeRecord(t))
	for _, test := range []struct {
		name, method, path, body, token string
		status                          int
	}{
		{"missing token", "GET", "/api/v1/capabilities", "", "", 401},
		{"wrong token", "GET", "/api/v1/capabilities", "", "Bearer wrong", 401},
		{"mutation", "POST", "/api/v1/resources", `{}`, "Bearer local-reader", 403},
		{"wrong method", "DELETE", "/api/v1/catalog/resources/" + clientSkillReleaseID, "", "Bearer local-reader", 403},
		{"unlisted read", "GET", "/api/v1/resources", "", "Bearer local-reader", 403},
		{"malformed search", "POST", "/api/v1/catalog/search", `{`, "Bearer local-reader", 400},
		{"oversized search", "POST", "/api/v1/catalog/search", `{"text":"` + strings.Repeat("x", 8192) + `"}`, "Bearer local-reader", 400},
		{"oversized suffix", "POST", "/api/v1/catalog/search", `{"text":"plugin"}` + strings.Repeat(" ", 8192), "Bearer local-reader", 400},
		{"trailing value", "POST", "/api/v1/catalog/search", `{"text":"plugin"} {}`, "Bearer local-reader", 400},
		{"duplicate key", "POST", "/api/v1/catalog/search", `{"text":"order","text":"plugin"}`, "Bearer local-reader", 400},
		{"unknown field", "POST", "/api/v1/catalog/search", `{"text":"plugin","unexpected":true}`, "Bearer local-reader", 400},
		{"null search", "POST", "/api/v1/catalog/search", `null`, "Bearer local-reader", 400},
		{"capabilities", "GET", "/api/v1/capabilities", "", "Bearer local-reader", 204},
		{"demo search", "POST", "/api/v1/catalog/search", `{"text":"order","limit":5}`, "Bearer local-reader", 204},
		{"demo record", "GET", "/api/v1/catalog/resources/demo-release", "", "Bearer local-reader", 204},
	} {
		t.Run(test.name, func(t *testing.T) {
			before := len(calls)
			request := httptest.NewRequestWithContext(t.Context(), test.method, test.path, strings.NewReader(test.body))
			request.Header.Set("Authorization", test.token)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != test.status {
				t.Fatalf("status %d, want %d", response.Code, test.status)
			}
			if test.status != http.StatusNoContent && len(calls) != before {
				t.Fatal("rejected request reached demo")
			}
		})
	}
	want := []string{"GET /api/v1/capabilities", "POST /api/v1/catalog/search", "GET /api/v1/catalog/resources/demo-release"}
	if !slices.Equal(calls, want) {
		t.Fatalf("unexpected demo calls: %v", calls)
	}
}

// TestClientSkillSessionSyntheticRefusal checks every reserved selector against
// unsupported controls. Refusal must be visible and cannot reach the demo.
func TestClientSkillSessionSyntheticRefusal(t *testing.T) {
	t.Parallel()
	handler := clientSkillSessionHandler(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("refused synthetic query reached demo")
	}), clientSkillPluginRecord(t), clientSkillLargeRecord(t))
	for _, selector := range []string{`"text":"triage"`, `"text":"plugin"`, `"text":"large"`, `"kinds":["plugin"]`} {
		for _, control := range []string{
			`"tags":["order"]`, `"tags":[]`, `"publisher":"publisher"`, `"publisher":""`,
			`"visibility":"tenant"`, `"visibility":""`, `"namespaceId":"00000000-0000-7000-8000-000000000002"`, `"namespaceId":""`,
			`"origins":["local"]`, `"origins":[]`, `"cursor":"opaque"`, `"cursor":""`,
		} {
			t.Run(selector+"/"+control, func(t *testing.T) {
				request := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/v1/catalog/search", strings.NewReader("{"+selector+","+control+"}"))
				request.Header.Set("Authorization", "Bearer local-reader")
				response := httptest.NewRecorder()
				handler.ServeHTTP(response, request)
				if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "unsupported") {
					t.Fatalf("synthetic refusal not visible: status=%d body=%s", response.Code, response.Body.String())
				}
			})
		}
	}
}

// TestClientSkillSessionStrictSearch preserves strict structural admission and
// bounded kinds/limits without using a listener or an alternate search engine.
func TestClientSkillSessionStrictSearch(t *testing.T) {
	t.Parallel()
	handler := clientSkillSessionHandler(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("invalid query reached demo")
	}), clientSkillPluginRecord(t), clientSkillLargeRecord(t))
	bodies := make([]string, 0, 26)
	bodies = append(bodies,
		`{"text":"triage","limit":1}`, `{"text":"triage","kinds":["agent","skill"],"limit":1}`,
		`{"text":"plugin","limit":0}`, `{"text":"large","limit":-1}`, `{"text":"order","limit":101}`,
		`{"text":"plugin","limit":1.5}`, `{"text":"plugin","kinds":["unknown"]}`,
		`{"text":"triage","kinds":["agent","agent"]}`, `{"text":"order","kinds":[""]}`,
		`{"Text":"plugin"}`, `{"text":"plugin","Limit":5}`, `{"text":"plugin","KINDS":["plugin"]}`,
		`{"text":"\ud800"}`, "{\"text\":\""+string([]byte{0xff})+"\"}",
	)
	for _, field := range []string{"text", "kinds", "tags", "publisher", "visibility", "namespaceId", "origins", "limit", "cursor"} {
		bodies = append(bodies, "{\""+field+"\":null}")
	}
	bodies = append(bodies, `{"kinds":[null]}`, `{"tags":[null]}`, `{"origins":[null]}`)
	for _, body := range bodies {
		t.Run(body, func(t *testing.T) {
			request := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/v1/catalog/search", strings.NewReader(body))
			request.Header.Set("Authorization", "Bearer local-reader")
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != http.StatusBadRequest {
				t.Fatalf("invalid query admitted: status=%d", response.Code)
			}
		})
	}
}

// TestClientSkillSessionForwarding verifies ordinary lexical and continuation
// bodies reach the owning demo unchanged, including their whitespace/defaults.
func TestClientSkillSessionForwarding(t *testing.T) {
	t.Parallel()
	var got []byte
	calls := 0
	handler := clientSkillSessionHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		var err error
		got, err = io.ReadAll(r.Body)
		if err != nil {
			t.Fatal(err)
		}
		w.WriteHeader(http.StatusNoContent)
	}), clientSkillPluginRecord(t), clientSkillLargeRecord(t))
	for _, body := range []string{
		` { "text" : "order" } `,
		`{"text":"plugin tools","kinds":["plugin"],"limit":1}`,
		`{"text":"large orders","limit":100}`,
		`{"text":"triage orders","tags":["order"],"cursor":"opaque","limit":5}`,
		`{"text":"order","kinds":["agent","mcp_server","skill","plugin"],"origins":["local"],"namespaceId":"00000000-0000-7000-8000-000000000002","tags":["order"],"publisher":"publisher","visibility":"tenant","limit":5,"cursor":"opaque"}`,
	} {
		t.Run(body, func(t *testing.T) {
			before := calls
			request := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/v1/catalog/search", strings.NewReader(body))
			request.Header.Set("Authorization", "Bearer local-reader")
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != http.StatusNoContent || calls != before+1 || string(got) != body {
				t.Fatal("ordinary query was intercepted or its body changed")
			}
		})
	}
}
