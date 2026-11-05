//go:build functional

// Functional tests exercise real process, network or application composition boundaries.
package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
)

func TestDemoFreshAndReadOnlyRerun(t *testing.T) {
	runtime := buildSmokeTestRuntime(t)
	defer runtime.Close()
	var writes atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.URL.Path != "/api/v1/catalog/search" {
			writes.Add(1)
		}
		runtime.HTTP.Handler().ServeHTTP(w, r)
	}))
	defer server.Close()
	path := filepath.Join(t.TempDir(), "demo.json")
	raw := captureCatalogctlStdout(t, func() error {
		return runArgs([]string{"seed-demo", "--sample-set", "minimal", "--base-url", server.URL, "--state-file", path, "--timeout", "10s"})
	})
	var first demoReport
	if err := json.Unmarshal(raw, &first); err != nil {
		t.Fatal(err)
	}
	if first.Status != "created" || len(first.Items) != 5 || first.Published != 3 || writes.Load() == 0 {
		t.Fatalf("bad first report: %#v", first)
	}
	writes.Store(0)
	second, err := executeDemo(t.Context(), server.URL, path)
	if err != nil {
		t.Fatal(err)
	}
	if second.Status != "already_matched" || !reflect.DeepEqual(first.demoReceipt, second.demoReceipt) || writes.Load() != 0 {
		t.Fatalf("rerun changed dataset: %#v writes=%d", second, writes.Load())
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("receipt permissions: %v, %v", info, err)
	}
	// The unchanged rerun makes no commands at all, so it cannot depend on
	// retained command replay responses, including after their 24-hour expiry.
	for _, kind := range []catalog.Kind{catalog.KindAgent, catalog.KindMCPServer, catalog.KindSkill} {
		response, err := requestRemote(t.Context(), remoteOptions{baseURL: server.URL, token: "local-reader", timeout: time.Second, path: "/api/v1/catalog/search", search: &remoteSearch{Text: "order", Kinds: []catalog.Kind{kind}, Tags: []string{demoID}, Limit: 6}})
		if err != nil {
			t.Fatal(err)
		}
		var results catalog.CatalogSearchResponse
		if json.Unmarshal(response.Data, &results) != nil || len(results.Items) != 1 || results.Items[0].Kind != kind {
			t.Fatalf("demo query for %s did not find exactly one published item", kind)
		}
	}
}

func TestDemoArgumentsAndBoundedPagination(t *testing.T) {
	const first = "00000000-0000-7000-8000-000000000041"
	const second = "00000000-0000-7000-8000-000000000042"
	for name, pages := range map[string][]string{
		"missing items":        {`{}`},
		"null page":            {`null`},
		"null items":           {`{"items":null}`},
		"valid":                {`{"items":[{"id":"` + first + `"}],"nextCursor":"next"}`, `{"items":[{"id":"` + second + `"}]}`},
		"repeat ID":            {`{"items":[{"id":"` + first + `"},{"id":"` + first + `"}]}`},
		"invalid ID":           {`{"items":[{"id":"not-an-id"}]}`},
		"empty continued page": {`{"items":[],"nextCursor":"next"}`},
		"repeated cursor":      {`{"items":[{"id":"` + first + `"}],"nextCursor":"next"}`, `{"items":[{"id":"` + second + `"}],"nextCursor":"next"}`},
		"overlong cursor":      {`{"items":[{"id":"` + first + `"}],"nextCursor":"` + strings.Repeat("x", 2049) + `"}`},
		"overflow":             {`{"items":[{"id":"` + first + `"},{"id":"` + second + `"}]}`},
	} {
		t.Run(name, func(t *testing.T) {
			var calls atomic.Int64
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				index := int(calls.Add(1)) - 1
				if r.URL.Query().Get("limit") != "6" || index > 0 && r.URL.Query().Get("cursor") != "next" || index >= len(pages) {
					t.Error("unbounded or incorrectly bound page request")
					http.Error(w, "bad page", http.StatusBadRequest)
					return
				}
				_, _ = w.Write([]byte(pages[index]))
			}))
			defer server.Close()
			limit := 2
			if name == "overflow" {
				limit = 1
			}
			ids, err := demoPageIDs(t.Context(), &smokeHTTPClient{baseURL: server.URL, client: server.Client(), safeErrors: true}, "/api/v1/resources", limit)
			if name == "valid" {
				if err != nil || !reflect.DeepEqual(ids, []string{first, second}) {
					t.Fatalf("valid pages: %v, %v", ids, err)
				}
			} else if err == nil {
				t.Fatal("unsafe pagination accepted")
			}
			if calls.Load() > 2 {
				t.Fatal("pagination continued beyond the test's bound")
			}
		})
	}
}

func TestDemoRefusesEditedAndMissingState(t *testing.T) {
	for _, change := range []string{"namespace", "draft version", "draft listing", "pending approval", "unpublished snapshot", "missing receipt", "wrong target", "wrong ID", "unrelated resource"} {
		t.Run(change, func(t *testing.T) {
			runtime := buildSmokeTestRuntime(t)
			defer runtime.Close()
			var refuseWrites atomic.Bool
			var writes atomic.Int64
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if refuseWrites.Load() && r.Method != http.MethodGet && r.URL.Path != "/api/v1/catalog/search" {
					writes.Add(1)
					http.Error(w, "unexpected write", http.StatusInternalServerError)
					return
				}
				runtime.HTTP.Handler().ServeHTTP(w, r)
			}))
			defer server.Close()
			path := filepath.Join(t.TempDir(), "receipt.json")
			report, err := executeDemo(t.Context(), server.URL, path)
			if err != nil {
				t.Fatal(err)
			}
			c := &smokeHTTPClient{baseURL: server.URL, client: server.Client(), safeErrors: true}
			switch change {
			case "namespace":
				_, err = demoExchange[map[string]any](t.Context(), c, "PATCH", "/api/v1/namespaces/"+report.NamespaceID, "local-admin", "edit-namespace", `"rev-1"`, map[string]any{"displayName": "Edited", "reason": "Test edited fixture"}, 200)
			case "draft version":
				representation := demoFixtures()[3].Representation
				data, ok := representation["data"].(map[string]any)
				if !ok {
					t.Fatal("Agent fixture has no structured data")
				}
				data["description"] = "An edited draft description"
				_, err = demoExchange[map[string]any](t.Context(), c, "POST", "/api/v1/resource-versions/"+report.Items[3].VersionID+"/revisions", "local-contributor", "edit-version", `"rev-1"`, map[string]any{"representations": []map[string]any{representation}}, 201)
			case "pending approval":
				_, err = demoExchange[map[string]any](t.Context(), c, "POST", "/api/v1/review-submissions/"+report.Items[4].SubmissionID+":approve", "local-reviewer", "edit-approval", `"rev-1"`, map[string]any{"reason": "Review the pending example"}, 200)
			case "draft listing":
				path := "/api/v1/catalog-listings/" + report.Items[3].ListingID
				_, etag, readErr := demoExchangeETag[catalog.CatalogListing](t.Context(), c, http.MethodGet, path, "local-contributor", "", "", nil, http.StatusOK)
				if readErr != nil {
					t.Fatal(readErr)
				}
				input := demoListingInput(demoFixtures()[3], report.PublisherID, report.Items[3].VersionRevisionID)
				input["description"] = "Edited draft listing metadata"
				_, err = demoExchange[map[string]any](t.Context(), c, http.MethodPost, path+"/revisions", "local-contributor", "edit-listing", etag, input, http.StatusCreated)
			case "unpublished snapshot":
				_, err = demoExchange[map[string]any](t.Context(), c, http.MethodPost, "/api/v1/publication:unpublish", "local-publisher", "unpublish-demo", "", map[string]any{"host": "127.0.0.1", "reason": "Withdraw the demo publication"}, http.StatusOK)
			case "unrelated resource":
				_, err = demoExchange[map[string]any](t.Context(), c, "POST", "/api/v1/resources", "local-contributor", "extra-resource", "", map[string]any{"namespaceId": report.NamespaceID, "canonicalIdentifier": "urn:air:demo.catalog.example.com:demo:extra", "kind": "agent"}, 201)
			case "missing receipt":
				err = os.Remove(path)
			case "wrong target", "wrong ID":
				r := report.demoReceipt
				if change == "wrong target" {
					r.BaseURL = "http://127.0.0.1:1"
				} else {
					r.Items[3].VersionID = "00000000-0000-7000-8000-000000000099"
				}
				raw, marshalErr := json.Marshal(r)
				if marshalErr != nil {
					t.Fatal(marshalErr)
				}
				err = os.WriteFile(path, raw, 0600)
			}
			if err != nil {
				t.Fatal(err)
			}
			refuseWrites.Store(true)
			if _, err := executeDemo(t.Context(), server.URL, path); err == nil || writes.Load() != 0 {
				t.Fatalf("changed fixture accepted or written: %v, writes=%d", err, writes.Load())
			}
		})
	}
}

func TestDemoMalformedInventoryPreventsWrites(t *testing.T) {
	runtime := buildSmokeTestRuntime(t)
	defer runtime.Close()
	var writes atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writes.Add(1)
			http.Error(w, "unexpected write", http.StatusInternalServerError)
			return
		}
		if r.URL.Path == "/api/v1/resources" {
			_, _ = w.Write([]byte(`{}`))
			return
		}
		runtime.HTTP.Handler().ServeHTTP(w, r)
	}))
	defer server.Close()
	path := filepath.Join(t.TempDir(), "receipt.json")
	_, err := executeDemo(t.Context(), server.URL, path)
	if err == nil || !strings.Contains(err.Error(), "items array") || writes.Load() != 0 {
		t.Fatalf("malformed inventory allowed writes: %v, writes=%d", err, writes.Load())
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("malformed inventory produced a receipt")
	}
}

func TestDemoReceiptValidationBeforeRequests(t *testing.T) {
	runtime := buildSmokeTestRuntime(t)
	defer runtime.Close()
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		runtime.HTTP.Handler().ServeHTTP(w, r)
	}))
	defer server.Close()
	path := filepath.Join(t.TempDir(), "original.json")
	report, err := executeDemo(t.Context(), server.URL, path)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(report.demoReceipt)
	if err != nil {
		t.Fatal(err)
	}
	for _, fault := range []string{"unknown field", "duplicate ID", "missing submission", "wrong kind", "symlink", "directory"} {
		t.Run(fault, func(t *testing.T) {
			receiptPath := filepath.Join(t.TempDir(), "receipt.json")
			var receipt demoReceipt
			if err := json.Unmarshal(raw, &receipt); err != nil {
				t.Fatal(err)
			}
			switch fault {
			case "duplicate ID":
				receipt.Items[0].VersionID = receipt.Items[0].ResourceID
			case "missing submission":
				receipt.Items[4].SubmissionID = ""
			case "wrong kind":
				receipt.Items[0].Kind = catalog.KindSkill
			}
			body, err := json.Marshal(receipt)
			if err != nil {
				t.Fatal(err)
			}
			if fault == "unknown field" {
				body = append(body[:len(body)-1], []byte(`,"unexpected":true}`)...)
			}
			switch fault {
			case "symlink":
				err = os.Symlink(path, receiptPath)
			case "directory":
				err = os.Mkdir(receiptPath, 0700)
			default:
				err = os.WriteFile(receiptPath, body, 0600)
			}
			if err != nil {
				t.Fatal(err)
			}
			calls.Store(0)
			if _, err := executeDemo(t.Context(), server.URL, receiptPath); err == nil || calls.Load() != 0 {
				t.Fatalf("invalid receipt contacted the service: %v, calls=%d", err, calls.Load())
			}
		})
	}
}

func TestDemoRejectsInconsistentDiscovery(t *testing.T) {
	type fault struct {
		path, response, message string
	}
	runtime := buildSmokeTestRuntime(t)
	defer runtime.Close()
	var active atomic.Pointer[fault]
	var writes atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if current := active.Load(); current != nil {
			if r.Method != http.MethodGet && r.URL.Path != "/api/v1/catalog/search" {
				writes.Add(1)
				http.Error(w, "unexpected mutation", http.StatusInternalServerError)
				return
			}
			if r.URL.Path == current.path {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(current.response))
				return
			}
		}
		runtime.HTTP.Handler().ServeHTTP(w, r)
	}))
	defer server.Close()
	path := filepath.Join(t.TempDir(), "receipt.json")
	if _, err := executeDemo(t.Context(), server.URL, path); err != nil {
		t.Fatal(err)
	}
	c := &smokeHTTPClient{baseURL: server.URL, client: server.Client(), safeErrors: true}
	search, err := demoExchange[catalog.CatalogSearchResponse](t.Context(), c, http.MethodPost, "/api/v1/catalog/search", "local-reader", "", "", map[string]any{"tags": []string{demoID}, "limit": 6}, http.StatusOK)
	if err != nil || len(search.Items) != 3 {
		t.Fatalf("baseline search: %v, items=%d", err, len(search.Items))
	}
	search.Partial = true
	partial, err := json.Marshal(search)
	if err != nil {
		t.Fatal(err)
	}
	search.Partial = false
	search.Items[0].Identifier = "urn:air:demo.catalog.example.com:demo:unexpected"
	wrongIdentity, err := json.Marshal(search)
	if err != nil {
		t.Fatal(err)
	}
	// Reuse one unchanged catalog; each fault replaces only its named read.
	for name, current := range map[string]fault{
		"missing registry": {"/.well-known/ard.json", `{"entries":[{},{},{},{}]}`, "public manifest must contain"},
		"empty AI Catalog": {"/.well-known/ai-catalog.json", `{"entries":[]}`, "AI Catalog must contain"},
		"partial search":   {"/api/v1/catalog/search", string(partial), "Search does not contain exactly"},
		"wrong identities": {"/api/v1/catalog/search", string(wrongIdentity), "Search identities differ"},
	} {
		t.Run(name, func(t *testing.T) {
			active.Store(&current)
			_, err := executeDemo(t.Context(), server.URL, path)
			if err == nil || !strings.Contains(err.Error(), current.message) || writes.Load() != 0 {
				t.Fatalf("inconsistent discovery accepted or mutated: %v, writes=%d", err, writes.Load())
			}
		})
	}
}

func TestDemoInterruptedSetupAndReceiptPublicationFailure(t *testing.T) {
	for _, phase := range []string{"authorization", "stale etag", "receipt collision"} {
		t.Run(phase, func(t *testing.T) {
			runtime := buildSmokeTestRuntime(t)
			defer runtime.Close()
			path := filepath.Join(t.TempDir(), "receipt.json")
			var fault atomic.Bool
			fault.Store(true)
			var writes atomic.Int64
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet && r.URL.Path != "/api/v1/catalog/search" {
					writes.Add(1)
				}
				if fault.Load() {
					if phase == "authorization" && r.URL.Path == "/api/v1/resources" && r.Method == http.MethodPost {
						http.Error(w, "private-token-must-not-leak", http.StatusForbidden)
						return
					}
					if phase == "stale etag" && strings.HasSuffix(r.URL.Path, "/revisions") {
						http.Error(w, "private-token-must-not-leak", http.StatusPreconditionFailed)
						return
					}
					if phase == "receipt collision" && r.URL.Path == "/.well-known/ard.json" {
						if err := os.WriteFile(path, []byte("keep this file"), 0600); err != nil {
							t.Error(err)
						}
					}
				}
				runtime.HTTP.Handler().ServeHTTP(w, r)
			}))
			defer server.Close()
			if _, err := executeDemo(t.Context(), server.URL, path); err == nil || strings.Contains(err.Error(), "private-token") {
				t.Fatalf("setup failure = %v", err)
			}
			if phase == "receipt collision" {
				raw, err := os.ReadFile(path) // #nosec G304 -- exact receipt in this test's owned temporary directory.
				if err != nil || string(raw) != "keep this file" {
					t.Fatal("existing receipt was replaced")
				}
			} else if _, err := os.Stat(path); !os.IsNotExist(err) {
				t.Fatal("failed setup wrote a completed receipt")
			}
			fault.Store(false)
			writes.Store(0)
			if _, err := executeDemo(t.Context(), server.URL, path); err == nil || writes.Load() != 0 {
				t.Fatal("partial setup was resumed or accepted")
			}
		})
	}
}

func TestDemoTargetsReceiptsAndTransportFailClosed(t *testing.T) {
	for _, fault := range []string{"redirect", "host", "authorization", "deadline", "oversized", "duplicate JSON"} {
		t.Run(fault, func(t *testing.T) {
			var calls atomic.Int64
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				switch fault {
				case "redirect":
					http.Redirect(w, r, "http://127.0.0.1:1", http.StatusFound)
				case "host":
					http.Error(w, "unconfigured host", http.StatusBadRequest)
				case "authorization":
					http.Error(w, "private-token", http.StatusUnauthorized)
				case "deadline":
					<-r.Context().Done()
				case "oversized":
					_, _ = w.Write([]byte(strings.Repeat("x", smokeMaximumResponseBytes+1)))
				case "duplicate JSON":
					_, _ = w.Write([]byte(`{"status":"a","status":"b"}`))
				}
			}))
			defer server.Close()
			limit := 5 * time.Second
			if fault == "deadline" {
				limit = 50 * time.Millisecond
			}
			ctx, cancel := context.WithTimeout(t.Context(), limit)
			defer cancel()
			if _, err := executeDemo(ctx, server.URL, filepath.Join(t.TempDir(), "receipt.json")); err == nil || strings.Contains(err.Error(), "private-token") || calls.Load() != 1 {
				t.Fatalf("fault %s: calls=%d err=%v", fault, calls.Load(), err)
			}
		})
	}
}
