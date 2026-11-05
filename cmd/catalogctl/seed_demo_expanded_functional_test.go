//go:build functional

package main

import (
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/config"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/demodata"
	platformruntime "github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/runtime"
	"github.com/open-agentic-catalog/open-agentic-catalog/test/demofixture"
)

func expandedDemoRuntime(t *testing.T) (*platformruntime.Runtime, string) {
	t.Helper()
	peer := httptest.NewTLSServer(demofixture.Handler())
	t.Cleanup(peer.Close)
	trust := filepath.Join(t.TempDir(), "trust.pem")
	if err := os.WriteFile(trust, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: peer.Certificate().Raw}), 0600); err != nil {
		t.Fatal(err)
	}
	prepared := filepath.Join(t.TempDir(), "prepared")
	if err := demodata.Prepare(prepared, demodata.DefaultPlan("expanded", peer.URL)); err != nil {
		t.Fatal(err)
	}
	runtime := buildSmokeTestRuntime(t, func(c *config.Config) {
		c.Environment = "development"
		c.DemoArchives = config.DemoArchives{Origin: peer.URL, TrustBundleFile: trust, AllowlistFile: filepath.Join(prepared, demodata.AllowlistFilename)}
	})
	t.Cleanup(runtime.Close)
	return runtime, peer.URL
}

func TestExpandedDemoFreshExtensionAndRerun(t *testing.T) {
	for _, extend := range []bool{false, true} {
		t.Run(map[bool]string{false: "fresh", true: "extend"}[extend], func(t *testing.T) {
			runtime, origin := expandedDemoRuntime(t)
			var writes atomic.Int64
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet && r.URL.Path != "/api/v1/catalog/search" {
					writes.Add(1)
				}
				runtime.HTTP.Handler().ServeHTTP(w, r)
			}))
			defer server.Close()
			path := filepath.Join(t.TempDir(), "receipt.json")
			var original demoReport
			var err error
			if extend {
				original, err = executeDemo(t.Context(), server.URL, path)
				if err != nil {
					t.Fatal(err)
				}
			}
			report, err := executeExpandedDemo(t.Context(), server.URL, path, origin, extend)
			if err != nil {
				t.Fatal(err)
			}
			if report.Published != 12 || len(report.Items) != 14 || report.PluginNamespaceID == report.NamespaceID {
				t.Fatalf("bad report %#v", report)
			}
			if extend && !reflect.DeepEqual(original.Items, report.Items[:5]) {
				t.Fatal("originals changed")
			}
			writes.Store(0)
			again, err := executeExpandedDemo(t.Context(), server.URL, path, origin, false)
			if err != nil || writes.Load() != 0 || again.Status != "already_matched" {
				t.Fatalf("rerun %v writes %d", err, writes.Load())
			}
			if _, err := executeMinimalDemo(t.Context(), server.URL, path); err == nil || writes.Load() != 0 {
				t.Fatal("minimal sample set accepted expanded receipt or wrote data")
			}
			c := &smokeHTTPClient{baseURL: server.URL, client: server.Client(), safeErrors: true}
			verifyDemoReaderSurfaces(t, c, report.demoReceipt, "https://127.0.0.1")
			for _, kind := range []catalog.Kind{catalog.KindAgent, catalog.KindMCPServer, catalog.KindSkill, catalog.KindPlugin} {
				response, e := demoExchange[catalog.CatalogSearchResponse](t.Context(), c, "POST", "/api/v1/catalog/search", "local-reader", "", "", map[string]any{"kinds": []catalog.Kind{kind}, "tags": []string{demoID}, "limit": 20}, 200)
				if e != nil || len(response.Items) != 3 {
					t.Fatalf("kind %s: %v count %d", kind, e, len(response.Items))
				}
			}
		})
	}
}

func TestExpandedDemoInterruptedExtension(t *testing.T) {
	for _, suffix := range []string{"/versions", ":approve", ":publish", ":activate"} {
		t.Run(suffix, func(t *testing.T) {
			runtime, origin := expandedDemoRuntime(t)
			var fault atomic.Bool
			var attempts atomic.Int64
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if fault.Load() && r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, suffix) {
					runtime.HTTP.Handler().ServeHTTP(httptest.NewRecorder(), r)
					fault.Store(false)
					attempts.Add(1)
					http.Error(w, "private failure", http.StatusServiceUnavailable)
					return
				}
				runtime.HTTP.Handler().ServeHTTP(w, r)
			}))
			defer server.Close()
			path := filepath.Join(t.TempDir(), "receipt.json")
			original, err := executeDemo(t.Context(), server.URL, path)
			if err != nil {
				t.Fatal(err)
			}
			fault.Store(true)
			if _, err = executeExpandedDemo(t.Context(), server.URL, path, origin, true); err == nil {
				t.Fatal("fault not exercised")
			}
			retained, _, err := readDemoReceipt(path, server.URL)
			if err != nil || !reflect.DeepEqual(retained, original.demoReceipt) {
				t.Fatal("original receipt changed")
			}
			result, err := executeExpandedDemo(t.Context(), server.URL, path, origin, true)
			if err != nil {
				t.Fatal(err)
			}
			if attempts.Load() != 1 || len(result.Items) != 14 {
				t.Fatal("bad recovery")
			}
			raw, err := json.Marshal(result)
			if err != nil || strings.Contains(string(raw), "private failure") {
				t.Fatal("unsafe report")
			}
		})
	}
}

func TestExpandedDemoRefusesDriftCollisionAndMissingReceipt(t *testing.T) {
	for _, fault := range []string{"edited namespace", "later identifier collision", "missing receipt", "nonempty fresh", "changed receipt", "target mismatch"} {
		t.Run(fault, func(t *testing.T) {
			runtime, origin := expandedDemoRuntime(t)
			var forbid atomic.Bool
			var writes atomic.Int64
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if forbid.Load() && r.Method != http.MethodGet && r.URL.Path != "/api/v1/catalog/search" {
					writes.Add(1)
				}
				runtime.HTTP.Handler().ServeHTTP(w, r)
			}))
			defer server.Close()
			path := filepath.Join(t.TempDir(), "receipt.json")
			old, err := executeDemo(t.Context(), server.URL, path)
			if err != nil {
				t.Fatal(err)
			}
			c := &smokeHTTPClient{baseURL: server.URL, client: server.Client(), safeErrors: true}
			switch fault {
			case "edited namespace":
				_, err = demoExchange[map[string]any](t.Context(), c, "PATCH", "/api/v1/namespaces/"+old.NamespaceID, "local-admin", "test-edit", "\"rev-1\"", map[string]any{"displayName": "Edited namespace", "reason": "Test drift"}, 200)
			case "later identifier collision":
				_, err = demoExchange[catalog.Resource](t.Context(), c, "POST", "/api/v1/resources", "local-contributor", "test-collision", "", map[string]any{"namespaceId": old.NamespaceID, "canonicalIdentifier": "urn:air:demo.catalog.example.com:ide-plugins:release-workflow", "kind": "plugin"}, 201)
			case "changed receipt", "target mismatch":
				receipt := old.demoReceipt
				if fault == "changed receipt" {
					receipt.Items[0].Key = "edited"
				} else {
					receipt.BaseURL = "http://127.0.0.1:1"
				}
				raw, e := json.Marshal(receipt)
				if e != nil {
					t.Fatal(e)
				}
				err = os.WriteFile(path, raw, 0600)
			case "missing receipt":
				err = os.Remove(path)
			case "nonempty fresh":
				path = filepath.Join(t.TempDir(), "fresh.json")
			}
			if err != nil {
				t.Fatal(err)
			}
			forbid.Store(true)
			extend := fault != "nonempty fresh"
			if _, err = executeExpandedDemo(t.Context(), server.URL, path, origin, extend); err == nil || writes.Load() != 0 {
				t.Fatalf("unsafe acceptance %v writes %d", err, writes.Load())
			}
		})
	}
}

func TestExpandedDemoPreservesUnrelatedPublishedRelease(t *testing.T) {
	runtime, origin := expandedDemoRuntime(t)
	server := httptest.NewServer(runtime.HTTP.Handler())
	defer server.Close()
	path := filepath.Join(t.TempDir(), "receipt.json")
	original, err := executeDemo(t.Context(), server.URL, path)
	if err != nil {
		t.Fatal(err)
	}
	c := &smokeHTTPClient{baseURL: server.URL, client: server.Client(), safeErrors: true}
	f := demoFixtures()[3]
	f.Segment = "unrelated-agent"
	f.identifier = "urn:air:demo.catalog.example.com:demo:unrelated-agent"
	f.Name = "Unrelated Agent"
	f.listing.RepresentativeQueries = []string{"Find Unrelated Agent", "Find order tools in the catalog demo"}
	item, err := createDemoItem(t.Context(), c, original.demoReceipt, f)
	if err != nil {
		t.Fatal(err)
	}
	listing, etag, err := demoExchangeETag[catalog.CatalogListing](t.Context(), c, "GET", "/api/v1/catalog-listings/"+item.ListingID, "local-contributor", "", "", nil, 200)
	if err != nil {
		t.Fatal(err)
	}
	input := demoListingInput(f, original.PublisherID, item.VersionRevisionID)
	input["tags"] = []string{"unrelated"}
	revision, listingETag, err := demoExchangeETag[struct {
		Revision catalog.ListingRevision `json:"revision"`
	}](t.Context(), c, "POST", "/api/v1/catalog-listings/"+listing.ID+"/revisions", "local-contributor", "extra-revision", etag, input, 201)
	if err != nil {
		t.Fatal(err)
	}
	submission, submissionETag, err := demoExchangeETag[catalog.ReviewSubmission](t.Context(), c, "POST", "/api/v1/catalog-listings/"+listing.ID+"/submissions", "local-contributor", "extra-submit", "", map[string]any{}, 201)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = demoExchange[map[string]any](t.Context(), c, "POST", "/api/v1/review-submissions/"+submission.ID+":approve", "local-reviewer", "extra-approve", submissionETag, map[string]any{"reason": "Review unrelated evidence"}, 200); err != nil {
		t.Fatal(err)
	}
	if _, err = demoExchange[map[string]any](t.Context(), c, "POST", "/api/v1/catalog-listings/"+listing.ID+":publish", "local-publisher", "extra-publish", listingETag, map[string]any{"submissionId": submission.ID}, 201); err != nil {
		t.Fatal(err)
	}
	result, err := executeExpandedDemo(t.Context(), server.URL, path, origin, true)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := demoExchange[struct {
		Entries []struct {
			Identifier string `json:"identifier"`
		} `json:"entries"`
	}](t.Context(), c, "GET", "/.well-known/ard.json", "", "", "", nil, 200)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, entry := range manifest.Entries {
		if entry.Identifier == demoIdentifier(f) {
			found = true
		}
	}
	if !found || revision.Revision.ListingID != item.ListingID {
		t.Fatal("unrelated release omitted")
	}
	if _, err = executeExpandedDemo(t.Context(), server.URL, path, origin, false); err != nil {
		t.Fatal(err)
	}
	if len(result.Items) != 14 {
		t.Fatal("unrelated record adopted")
	}
}

func TestExpandedDemoFreshRecoveryWithoutCommandReplay(t *testing.T) {
	for _, pathSuffix := range []string{"/namespaces", "/publishers", ":approve", "/resources", "/versions", "/catalog-listings", "/revisions", "/submissions", "/publication/snapshots", ":activate"} {
		t.Run(pathSuffix, func(t *testing.T) {
			runtime, origin := expandedDemoRuntime(t)
			var fault atomic.Bool
			fault.Store(true)
			seen := map[string]bool{}
			var repeated atomic.Int64
			var mu sync.Mutex
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodPost && r.URL.Path != "/api/v1/catalog/search" {
					mu.Lock()
					key := r.Header.Get("Idempotency-Key")
					repeat := seen[key]
					seen[key] = true
					mu.Unlock()
					if repeat {
						repeated.Add(1)
						http.Error(w, "expired replay unavailable", http.StatusGone)
						return
					}
					if fault.Load() && strings.HasSuffix(r.URL.Path, pathSuffix) {
						runtime.HTTP.Handler().ServeHTTP(httptest.NewRecorder(), r)
						fault.Store(false)
						http.Error(w, "lost command response", http.StatusServiceUnavailable)
						return
					}
				}
				runtime.HTTP.Handler().ServeHTTP(w, r)
			}))
			defer server.Close()
			path := filepath.Join(t.TempDir(), "receipt.json")
			if _, err := executeExpandedDemo(t.Context(), server.URL, path, origin, false); err == nil || fault.Load() {
				t.Fatal("fault not selected")
			}
			result, err := executeExpandedDemo(t.Context(), server.URL, path, origin, false)
			if err != nil || result.Status != "resumed" || repeated.Load() != 0 {
				t.Fatalf("recovery %v repeated %d", err, repeated.Load())
			}
		})
	}
}

func TestExpandedDemoPartialDriftFailsBeforeNewCommands(t *testing.T) {
	runtime, origin := expandedDemoRuntime(t)
	var fault atomic.Bool
	fault.Store(true)
	var forbid atomic.Bool
	var writes atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path != "/api/v1/catalog/search" {
			if forbid.Load() {
				writes.Add(1)
			}
			if fault.Load() && r.URL.Path == "/api/v1/resources" {
				fault.Store(false)
				http.Error(w, "pause", http.StatusServiceUnavailable)
				return
			}
		}
		runtime.HTTP.Handler().ServeHTTP(w, r)
	}))
	defer server.Close()
	path := filepath.Join(t.TempDir(), "receipt.json")
	if _, err := executeExpandedDemo(t.Context(), server.URL, path, origin, false); err == nil {
		t.Fatal("pause not selected")
	}
	p, _, err := readDemoProgress(path+".progress", server.URL, origin)
	if err != nil {
		t.Fatal(err)
	}
	var namespace catalog.Namespace
	if err = json.Unmarshal(p.Steps[0].Response, &namespace); err != nil {
		t.Fatal(err)
	}
	c := &smokeHTTPClient{baseURL: server.URL, client: server.Client(), safeErrors: true}
	if _, err = demoExchange[catalog.Namespace](t.Context(), c, "PATCH", "/api/v1/namespaces/"+namespace.ID, "local-admin", "edit-partial", "\"rev-1\"", map[string]any{"displayName": "Edited partial fixture", "reason": "Test refusal"}, 200); err != nil {
		t.Fatal(err)
	}
	forbid.Store(true)
	if _, err = executeExpandedDemo(t.Context(), server.URL, path, origin, false); err == nil || writes.Load() != 0 {
		t.Fatalf("drift admitted %v writes %d", err, writes.Load())
	}
}

// Only explicitly selected invocation-owned disposable Compose data may be mutated.
func TestExpandedDemoOwnedCompose(t *testing.T) {
	if os.Getenv("DEMO_ARCHIVE_TEST_OWNED") != "1" {
		t.Skip("owned disposable Compose qualification not selected")
	}
	baseURL, origin := os.Getenv("DEMO_ARCHIVE_TEST_API"), os.Getenv("DEMO_ARCHIVE_TEST_ORIGIN")
	parsed, err := url.Parse(baseURL)
	if err != nil || parsed.Scheme != "http" || parsed.Hostname() != "127.0.0.1" || parsed.Path != "" || parsed.RawQuery != "" || parsed.User != nil || parsed.Fragment != "" {
		t.Fatal("owned loopback origin required")
	}
	path := filepath.Join(t.TempDir(), "compose-receipt.json")
	original, err := executeDemo(t.Context(), baseURL, path)
	if err != nil {
		t.Fatal(err)
	}
	result, err := executeExpandedDemo(t.Context(), baseURL, path, origin, true)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(original.Items, result.Items[:5]) || result.Published != 12 {
		t.Fatal("original evidence changed")
	}
	if _, err = executeExpandedDemo(t.Context(), baseURL, path, origin, false); err != nil {
		t.Fatal(err)
	}
	c := &smokeHTTPClient{baseURL: baseURL, client: newLocalWorkflowClient(), safeErrors: true}
	t.Cleanup(c.client.CloseIdleConnections)
	verifyDemoReaderSurfaces(t, c, result.demoReceipt, baseURL)
	t.Log("Real PostgreSQL: original five records preserved, nine reviewed releases added, twelve published resources verified, completed rerun passed.")
}
