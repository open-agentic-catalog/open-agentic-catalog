//go:build functional

package main

import (
	"encoding/json"
	"encoding/pem"
	"github.com/open-agentic-catalog/open-agentic-catalog/samples"
	"go.yaml.in/yaml/v3"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/config"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/demodata"
	platformruntime "github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/runtime"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/publication"
	"github.com/open-agentic-catalog/open-agentic-catalog/test/demofixture"
)

func configuredDemoRuntime(t *testing.T) (*platformruntime.Runtime, *httptest.Server, string, string, *atomic.Int64) {
	t.Helper()
	return configuredDemoRuntimeWithPeerControl(t, nil)
}
func configuredDemoRuntimeWithPeerControl(t *testing.T, fault *atomic.Int32) (*platformruntime.Runtime, *httptest.Server, string, string, *atomic.Int64) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "archives")
	peer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if fault != nil && fault.Load() != 0 {
			if fault.Load() == 1 {
				http.Error(w, "unavailable", http.StatusServiceUnavailable)
			} else {
				w.Header().Set("Content-Type", "application/zip")
				_, _ = w.Write([]byte("changed archive"))
			}
			return
		}
		files, e := demodata.ReadPrepared(dir)
		if e != nil {
			http.Error(w, "not ready", http.StatusServiceUnavailable)
			return
		}
		demofixture.PreparedHandler(files).ServeHTTP(w, r)
	}))
	t.Cleanup(peer.Close)
	trust := filepath.Join(t.TempDir(), "trust.pem")
	if err := os.WriteFile(trust, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: peer.Certificate().Raw}), 0600); err != nil {
		t.Fatal(err)
	}
	for _, profile := range []string{"expanded", "curated"} {
		plan, e := demodata.Load("", profile, peer.URL)
		if e != nil {
			t.Fatal(e)
		}
		if e = demodata.Prepare(dir, plan); e != nil {
			t.Fatal(e)
		}
	}
	runtime := buildSmokeTestRuntime(t, func(c *config.Config) {
		c.Environment = "development"
		// Qualify governance and recovery within the normal service budgets,
		// rather than the smoke helper's one-second request latency budget.
		defaults := config.Defaults().HTTP
		c.HTTP.RequestTimeout = defaults.RequestTimeout
		c.HTTP.WriteTimeout = defaults.WriteTimeout
		c.HTTP.ShutdownTimeout = defaults.ShutdownTimeout
		c.DemoArchives = config.DemoArchives{Origin: peer.URL, TrustBundleFile: trust, AllowlistFile: filepath.Join(dir, demodata.AllowlistFilename)}
	})
	t.Cleanup(runtime.Close)
	writes := &atomic.Int64{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && !(r.Method == http.MethodPost && (r.URL.Path == "/api/v1/catalog/search" || r.URL.Path == "/api/v1/catalog/explore")) {
			writes.Add(1)
		}
		capture := httptest.NewRecorder()
		runtime.HTTP.Handler().ServeHTTP(capture, r)
		if capture.Code >= 400 {
			var problem map[string]any
			_ = json.Unmarshal(capture.Body.Bytes(), &problem)
			t.Log(r.URL.Path, problem["code"], problem["detail"])
		}
		for k, values := range capture.Header() {
			w.Header()[k] = values
		}
		w.WriteHeader(capture.Code)
		_, _ = w.Write(capture.Body.Bytes())
	}))
	t.Cleanup(server.Close)
	return runtime, server, peer.URL, trust, writes
}
func TestConfiguredDemoCuratedAndLegacyAdoption(t *testing.T) {
	for _, legacy := range []string{"", "minimal", "expanded"} {
		t.Run("from-"+legacy, func(t *testing.T) {
			_, server, origin, trust, writes := configuredDemoRuntime(t)
			path := filepath.Join(t.TempDir(), "receipt.json")
			plan, e := demodata.Load("", "curated", origin)
			if e != nil {
				t.Fatal(e)
			}
			var old demoReport
			if legacy == "minimal" {
				old, e = executeMinimalDemo(t.Context(), server.URL, path)
			} else if legacy == "expanded" {
				old, e = executeExpandedDemo(t.Context(), server.URL, path, origin, false)
			}
			if e != nil {
				t.Fatal(e)
			}
			result, e := executeConfiguredDemo(t.Context(), server.URL, path, origin, trust, legacy != "", plan)
			if e != nil {
				t.Fatal(e)
			}
			expected := 14
			if legacy == "minimal" {
				expected = 17
			} else if legacy == "expanded" {
				expected = 26
			}
			if len(result.Items) != expected {
				t.Fatal("wrong accumulated count", len(result.Items))
			}
			for _, item := range old.Items {
				found := false
				for _, current := range result.Items {
					if current.Key == item.Key {
						found = true
						if !reflect.DeepEqual(current, item) {
							t.Fatal("rewrote old identity", item.Key)
						}
					}
				}
				if !found {
					t.Fatal("lost retained item")
				}
			}
			before := writes.Load()
			again, e := executeConfiguredDemo(t.Context(), server.URL, path, origin, "missing", false, plan)
			if e != nil || again.Status != "already_matched" || writes.Load() != before {
				t.Fatal("rerun wrote or needed archive", e, again.Status)
			}
		})
	}
}
func TestConfiguredDemoInvalidAndAppendOnlyInputs(t *testing.T) {
	_, server, origin, trust, writes := configuredDemoRuntime(t)
	path := filepath.Join(t.TempDir(), "receipt.json")
	plan, e := demodata.Load("", "curated", origin)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = executeConfiguredDemo(t.Context(), server.URL, path, origin, "missing", false, plan); e == nil || writes.Load() != 0 {
		t.Fatal("missing trust caused writes", e)
	}
	if _, e = executeConfiguredDemo(t.Context(), server.URL, path, origin, trust, false, plan); e != nil {
		t.Fatal(e)
	}
	before := writes.Load()
	for _, fault := range []string{"edit", "remove"} {
		changed, _ := demodata.Load("", "curated", origin)
		if fault == "remove" {
			changed.Resources = changed.Resources[1:]
		} else {
			changed.Resources[0].Digest = "sha256:" + strings.Repeat("a", 64)
		}
		if _, e = executeConfiguredDemo(t.Context(), server.URL, path, origin, trust, true, changed); e == nil || writes.Load() != before {
			t.Fatal("unsafe change accepted", fault, e)
		}
	}
	custom := filepath.Join(t.TempDir(), "catalog.yaml")
	if e = os.WriteFile(custom, []byte("format: 1\nformat: 1\n"), 0600); e != nil {
		t.Fatal(e)
	}
	if e = seedDemo([]string{"--data-file", custom, "--base-url", server.URL, "--state-file", path}); e == nil || writes.Load() != before {
		t.Fatal("invalid YAML wrote")
	}
}

func TestConfiguredDemoInterruptedGovernance(t *testing.T) {
	for _, suffix := range []string{"/resources", "/versions", ":publish", ":validate", ":activate"} {
		t.Run(suffix, func(t *testing.T) {
			runtime, _, origin, trust, _ := configuredDemoRuntime(t)
			var fault atomic.Bool
			var selected atomic.Int64
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				capture := httptest.NewRecorder()
				if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, suffix) && fault.CompareAndSwap(true, false) {
					runtime.HTTP.Handler().ServeHTTP(capture, r)
					if capture.Code < 200 || capture.Code >= 300 {
						t.Errorf("selected command did not succeed before response loss: HTTP %d", capture.Code)
					}
					selected.Add(1)
					http.Error(w, "lost fixture response", http.StatusServiceUnavailable)
					return
				}
				runtime.HTTP.Handler().ServeHTTP(capture, r)
				if capture.Code >= 400 {
					var problem map[string]any
					_ = json.Unmarshal(capture.Body.Bytes(), &problem)
					// The public problem detail identifies the failed search guard;
					// do not log request bodies, credentials or internal causes.
					t.Log(r.URL.Path, capture.Code, problem["code"], problem["detail"])
				}
				for key, values := range capture.Header() {
					w.Header()[key] = values
				}
				w.WriteHeader(capture.Code)
				_, _ = w.Write(capture.Body.Bytes())
			}))
			defer server.Close()
			path := filepath.Join(t.TempDir(), "receipt.json")
			old, err := executeExpandedDemo(t.Context(), server.URL, path, origin, false)
			if err != nil {
				t.Fatal(err)
			}
			plan, err := demodata.Load("", "curated", origin)
			if err != nil {
				t.Fatal(err)
			}
			fault.Store(true)
			if _, err = executeConfiguredDemo(t.Context(), server.URL, path, origin, trust, true, plan); err == nil || selected.Load() != 1 {
				t.Fatal("fault not exercised", err)
			}
			retained, _, err := readDemoReceipt(path, server.URL)
			if err != nil || !reflect.DeepEqual(retained, old.demoReceipt) {
				t.Fatal("failed extension replaced original receipt")
			}
			changed, _ := demodata.Load("", "curated", origin)
			changed.Resources[0].Digest = demoHash("changed intent")
			if _, err = executeConfiguredDemo(t.Context(), server.URL, path, origin, trust, true, changed); err == nil {
				t.Fatal("different recovery intent accepted")
			}
			result, err := executeConfiguredDemo(t.Context(), server.URL, path, origin, trust, true, plan)
			if err != nil || len(result.Items) != 26 {
				t.Fatal("exact intent recovery failed", err)
			}
			for _, item := range old.Items {
				if !slices.Contains(result.Items, item) {
					t.Fatal("recovery replaced immutable item")
				}
			}
		})
	}
}

func copyConfiguredSampleManifest(t *testing.T) string {
	t.Helper()
	// Copy the complete checked-in asset tree once. No network acquisition.
	directory := t.TempDir()
	if err := fs.WalkDir(samples.Files, ".", func(name string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return os.MkdirAll(filepath.Join(directory, name), 0700)
		}
		raw, e := fs.ReadFile(samples.Files, name)
		if e != nil {
			return e
		}
		return os.WriteFile(filepath.Join(directory, name), raw, 0600)
	}); err != nil {
		t.Fatal(err)
	}
	return filepath.Join(directory, "catalog.yaml")
}

func decodeConfiguredSampleManifest(t *testing.T, raw []byte) demodata.Manifest {
	t.Helper()
	var manifest demodata.Manifest
	var node yaml.Node
	if yaml.Unmarshal(raw, &node) != nil {
		t.Fatal("YAML")
	}
	var value any
	if node.Decode(&value) != nil {
		t.Fatal("YAML node")
	}
	encoded, err := json.Marshal(value)
	if err != nil || json.Unmarshal(encoded, &manifest) != nil {
		t.Fatal("manifest conversion", err)
	}
	return manifest
}

func TestConfiguredDemoCustomFileAndAdditiveParents(t *testing.T) {
	_, server, origin, trust, writes := configuredDemoRuntime(t)
	file := copyConfiguredSampleManifest(t)
	path := filepath.Join(t.TempDir(), "receipt.json")
	plan, err := demodata.Load(file, "curated", origin)
	if err != nil {
		t.Fatal(err)
	}
	first, err := executeConfiguredDemo(t.Context(), server.URL, path, origin, trust, false, plan)
	if err != nil {
		t.Fatal(err)
	}
	before := writes.Load()
	raw, err := os.ReadFile(file) // #nosec G304 -- only the fixed manifest copied into this test's private temporary directory.
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(file, append([]byte("# This comment must not cause writes.\n"), raw...), 0600); err != nil { // #nosec G703 -- mutate only the fixed manifest copied into this test's private temporary directory.
		t.Fatal(err)
	}
	same, err := demodata.Load(file, "curated", origin)
	if err != nil {
		t.Fatal(err)
	}
	slices.Reverse(same.Resources)
	slices.Reverse(same.Namespaces)
	if _, err = executeConfiguredDemo(t.Context(), server.URL, path, origin, "missing", false, same); err != nil || writes.Load() != before {
		t.Fatal("comment/order-only edit mutated", err)
	}
	manifest := decodeConfiguredSampleManifest(t, raw)
	var encoded []byte
	added := manifest.Resources[0]
	added.Key = "additional-agent"
	added.Name = "Additional reviewed Agent"
	added.Identifier = "urn:air:extra.catalog.example.com:additional:additional-agent"
	added.Namespace = "additional"
	added.Publisher = "additional"
	manifest.Resources = append(manifest.Resources, added)
	manifest.Namespaces = append(manifest.Namespaces, demodata.Namespace{Key: "additional", DisplayName: "Additional workspace"})
	manifest.Publishers = append(manifest.Publishers, demodata.Publisher{Key: "additional", DisplayName: "Additional Publisher", Domains: []string{"extra.catalog.example.com", "other.catalog.example.com"}})
	manifest.Profiles["curated"] = append(manifest.Profiles["curated"], added.Key)
	// JSON is also valid YAML and keeps all existing relative native asset references.
	encoded, err = json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(file, encoded, 0600); err != nil {
		t.Fatal(err)
	}
	extended, err := demodata.Load(file, "curated", origin)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = executeConfiguredDemo(t.Context(), server.URL, path, origin, trust, false, extended); err == nil || writes.Load() != before {
		t.Fatal("extension was implicit")
	}
	// Frozen load must not reread subsequent source changes during the API workflow.
	if err = os.WriteFile(file, []byte("invalid YAML : ["), 0600); err != nil {
		t.Fatal(err)
	}
	result, err := executeConfiguredDemo(t.Context(), server.URL, path, origin, trust, true, extended)
	if err != nil || len(result.Items) != 15 || len(result.Bindings.Namespaces) != 3 || len(result.Bindings.Publishers) != 2 {
		t.Fatal("custom addition failed", err)
	}
	for _, item := range first.Items {
		if !slices.Contains(result.Items, item) {
			t.Fatal("custom extension rewrote old item")
		}
	}
}

func TestConfiguredDemoOwnedCompose(t *testing.T) {
	if os.Getenv("DEMO_ARCHIVE_TEST_OWNED") != "1" {
		t.Skip("owned disposable Compose qualification not selected")
	}
	base, origin := os.Getenv("DEMO_ARCHIVE_TEST_API"), os.Getenv("DEMO_ARCHIVE_TEST_ORIGIN")
	path, trust := os.Getenv("DEMO_ARCHIVE_TEST_STATE"), os.Getenv("DEMO_ARCHIVE_TEST_TRUST")
	if path == "" || trust == "" {
		t.Fatal("explicit owned receipt and public trust paths required")
	}
	original, exists, err := readDemoReceipt(path, base)
	if err != nil || !exists || original.Dataset != expandedDemoID {
		t.Fatal("verified original expanded receipt required", err)
	}
	plan, err := demodata.Load("", "curated", origin)
	if err != nil {
		t.Fatal(err)
	}
	result, err := executeConfiguredDemo(t.Context(), base, path, origin, trust, true, plan)
	if err != nil {
		t.Fatal(err)
	}
	if result.Published != 24 || len(result.Items) != 26 || result.Status != "extended" {
		t.Fatal("unexpected accumulated inventory")
	}
	for _, item := range original.Items {
		if !slices.Contains(result.Items, item) {
			t.Fatal("rewrote original PostgreSQL identity")
		}
	}
	again, err := executeConfiguredDemo(t.Context(), base, path, origin, "missing", false, plan)
	if err != nil || again.Status != "already_matched" || !reflect.DeepEqual(again.Items, result.Items) {
		t.Fatal("completed PostgreSQL rerun failed", err)
	}
	t.Log("Real PostgreSQL: original fourteen identities/history retained; twelve curated releases added; package-only Skills visible to Reader with three explicit public exclusions; exact snapshot and paginated Search passed.")
}

func TestConfiguredDemoCompletedJournalCleanupAndCollision(t *testing.T) {
	runtime, _, origin, trust, _ := configuredDemoRuntime(t)
	path := filepath.Join(t.TempDir(), "receipt.json")
	var completed []byte
	var writes atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && !(r.Method == http.MethodPost && (r.URL.Path == "/api/v1/catalog/search" || r.URL.Path == "/api/v1/catalog/explore")) {
			writes.Add(1)
		}
		if r.URL.Path == "/.well-known/ard.json" {
			if raw, e := os.ReadFile(path + ".progress"); e == nil { // #nosec G304 -- reads only this test's private owned journal, to simulate interruption after receipt replacement.
				completed = raw
			}
		}
		runtime.HTTP.Handler().ServeHTTP(w, r)
	}))
	defer server.Close()
	plan, err := demodata.Load("", "curated", origin)
	if err != nil {
		t.Fatal(err)
	}
	first, err := executeConfiguredDemo(t.Context(), server.URL, path, origin, trust, false, plan)
	if err != nil {
		t.Fatal(err)
	}
	before := writes.Load()
	if len(completed) == 0 {
		t.Fatal("complete journal not observed")
	}
	if err = os.WriteFile(path+".progress", completed, 0600); err != nil {
		t.Fatal(err)
	}
	again, err := executeConfiguredDemo(t.Context(), server.URL, path, origin, "missing", false, plan)
	if err != nil || again.Status != "already_matched" || writes.Load() != before || !reflect.DeepEqual(again.Items, first.Items) {
		t.Fatal("complete journal caused mutation", err)
	}
	if _, err = os.Stat(path + ".progress"); !os.IsNotExist(err) {
		t.Fatal("complete journal not removed")
	}
	// A missing receipt is not permission to adopt colliding API records.
	if _, err = executeConfiguredDemo(t.Context(), server.URL, filepath.Join(t.TempDir(), "another.json"), origin, trust, false, plan); err == nil || writes.Load() != before {
		t.Fatal("unowned collision caused writes", err)
	}
	lock, err := lockDemoState(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lock.Close() }()
	if _, err = executeConfiguredDemo(t.Context(), server.URL, path, origin, trust, false, plan); err == nil || writes.Load() != before {
		t.Fatal("concurrent loader admitted")
	}
}

func TestConfiguredDemoArchiveFailureBoundaries(t *testing.T) {
	var peerFault atomic.Int32
	runtime, _, origin, trust, writes := configuredDemoRuntimeWithPeerControl(t, &peerFault)
	var validationFault atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && !(r.Method == http.MethodPost && (r.URL.Path == "/api/v1/catalog/search" || r.URL.Path == "/api/v1/catalog/explore")) {
			writes.Add(1)
		}
		if validationFault.Load() && strings.HasSuffix(r.URL.Path, ":validate") {
			peerFault.Store(1)
		}
		runtime.HTTP.Handler().ServeHTTP(w, r)
	}))
	defer server.Close()
	path := filepath.Join(t.TempDir(), "receipt.json")
	plan, err := demodata.Load("", "curated", origin)
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []int32{1, 2} {
		peerFault.Store(mode)
		if _, err = executeConfiguredDemo(t.Context(), server.URL, path, origin, trust, false, plan); err == nil || writes.Load() != 0 {
			t.Fatal("failed archive preflight mutated", mode, err)
		}
	}
	peerFault.Store(0)
	if _, err = executeConfiguredDemo(t.Context(), server.URL, path, origin, trust, false, plan); err != nil {
		t.Fatal(err)
	}
	before := writes.Load()
	peerFault.Store(1)
	if result, err := executeConfiguredDemo(t.Context(), server.URL, path, origin, "missing", false, plan); err != nil || result.Status != "already_matched" || writes.Load() != before {
		t.Fatal("archive-off read-only rerun failed", err)
	}
	// An outage AFTER successful preflight must preserve the existing blocked
	// revalidation/withdrawal contract, not silently retain an unsafe publication.
	peerFault.Store(0)
	receipt, _, err := readDemoReceipt(path, server.URL)
	if err != nil {
		t.Fatal(err)
	}
	validationFault.Store(true)
	extended := plan
	extra := plan.Resources[0]
	extra.Resource.Key = "extra-workflow"
	extra.Resource.Identifier = "urn:air:demo.catalog.example.com:demo:extra-workflow"
	extra.Digest = demoHash("new extra-workflow input")
	extended.Resources = append(slices.Clone(plan.Resources), extra)
	if _, err = executeConfiguredDemo(t.Context(), server.URL, path, origin, trust, true, extended); err == nil {
		t.Fatal("blocked revalidation was treated as complete")
	}
	retained, _, err := readDemoReceipt(path, server.URL)
	if err != nil || !reflect.DeepEqual(retained, receipt) {
		t.Fatal("failed revalidation replaced receipt")
	}
	c := &smokeHTTPClient{baseURL: server.URL, client: newLocalWorkflowClient(), safeErrors: true}
	defer c.client.CloseIdleConnections()
	snapshot, err := demoGet[publication.Snapshot](t.Context(), c, "/api/v1/publication/snapshots/"+receipt.SnapshotID+"?host=127.0.0.1")
	if err != nil || snapshot.State != publication.SnapshotRetired {
		t.Fatal("blocked revalidation did not withdraw owned snapshot", err, snapshot.State)
	}
	if err = verifyConfiguredRecords(t.Context(), c, receipt, plan); err != nil {
		t.Fatal("blocked evidence rewrote immutable records", err)
	}
}

func TestConfiguredDemoPreservesUnrelatedRelease(t *testing.T) {
	_, server, origin, trust, _ := configuredDemoRuntime(t)
	path := filepath.Join(t.TempDir(), "receipt.json")
	plan, err := demodata.Load("", "curated", origin)
	if err != nil {
		t.Fatal(err)
	}
	first, err := executeConfiguredDemo(t.Context(), server.URL, path, origin, trust, false, plan)
	if err != nil {
		t.Fatal(err)
	}
	c := &smokeHTTPClient{baseURL: server.URL, client: newLocalWorkflowClient(), safeErrors: true}
	defer c.client.CloseIdleConnections()
	fixture := demoFixtures()[0]
	fixture.Segment = "unrelated-curated"
	fixture.identifier = "urn:air:demo.catalog.example.com:demo:unrelated-curated"
	unrelated, err := createDemoItem(t.Context(), c, first.demoReceipt, fixture)
	if err != nil {
		t.Fatal(err)
	}
	originalPlan := demodata.DefaultPlan("minimal", origin)
	plan.Resources = append(plan.Resources, originalPlan.Resources[0])
	result, err := executeConfiguredDemo(t.Context(), server.URL, path, origin, trust, true, plan)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Items) != 15 || slices.ContainsFunc(result.Items, func(item demoItem) bool { return item.Identifier == unrelated.Identifier }) {
		t.Fatal("unrelated release was adopted")
	}
	public, err := demoExchange[publication.ARDManifest](t.Context(), c, "GET", "/.well-known/ard.json", "", "", "", nil, 200)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(public.Entries, func(entry publication.Entry) bool { return entry.Identifier == unrelated.Identifier }) {
		t.Fatal("unrelated release dropped from public snapshot")
	}
	if err = verifyDemoItem(t.Context(), c, first.demoReceipt, unrelated, fixture); err != nil {
		t.Fatal("unrelated release rewritten", err)
	}
}

func TestConfiguredDemoIdentityPreflight(t *testing.T) {
	runtime, _, origin, trust, _ := configuredDemoRuntime(t)
	var writes atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && !(r.Method == http.MethodPost && (r.URL.Path == "/api/v1/catalog/search" || r.URL.Path == "/api/v1/catalog/explore")) {
			writes.Add(1)
		}
		if r.URL.Path == "/api/v1/me" && r.Header.Get("Authorization") == "Bearer local-reader" {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"tenantId": demoTenant, "subject": "local-reader", "roles": []string{}})
			return
		}
		runtime.HTTP.Handler().ServeHTTP(w, r)
	}))
	defer server.Close()
	plan, err := demodata.Load("", "curated", origin)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = executeConfiguredDemo(t.Context(), server.URL, filepath.Join(t.TempDir(), "receipt.json"), origin, trust, false, plan); err == nil || writes.Load() != 0 {
		t.Fatal("missing Reader role caused mutation", err)
	}
}

func TestConfiguredDemoFirstPluginOrigin(t *testing.T) {
	_, server, origin, trust, writes := configuredDemoRuntime(t)
	path := filepath.Join(t.TempDir(), "receipt.json")
	unusedOrigin := "https://127.0.0.1:1"
	minimal, err := demodata.Load("", "minimal", unusedOrigin)
	if err != nil {
		t.Fatal(err)
	}
	first, err := executeConfiguredDemo(t.Context(), server.URL, path, unusedOrigin, "missing", false, minimal)
	if err != nil {
		t.Fatal(err)
	}
	if first.ArchiveOrigin != "" {
		t.Fatal("Plugin-free receipt bound unused archive origin")
	}
	before := writes.Load()
	if _, err = executeConfiguredDemo(t.Context(), server.URL, path, origin, "missing", false, minimal); err != nil || writes.Load() != before {
		t.Fatal("Plugin-free read-only rerun needed archive", err)
	}
	expanded, err := demodata.Load("", "expanded", origin)
	if err != nil {
		t.Fatal(err)
	}
	result, err := executeConfiguredDemo(t.Context(), server.URL, path, origin, trust, true, expanded)
	if err != nil || len(result.Items) != 14 || result.ArchiveOrigin != origin {
		t.Fatal("first Plugin extension failed", err)
	}
	for _, item := range first.Items {
		if !slices.Contains(result.Items, item) {
			t.Fatal("first Plugin extension changed original IDs")
		}
	}
}

func TestConfiguredDemoNonPublicSkillPackages(t *testing.T) {
	_, server, origin, trust, writes := configuredDemoRuntime(t)
	file := copyConfiguredSampleManifest(t)
	raw, err := os.ReadFile(file) // #nosec G304 -- fixed manifest copied into the test's private directory.
	if err != nil {
		t.Fatal(err)
	}
	manifest := decodeConfiguredSampleManifest(t, raw)
	changed := 0
	for i := range manifest.Resources {
		item := &manifest.Resources[i]
		if item.Kind == catalog.KindSkill && item.Representation.PackageDirectory != "" {
			if changed == 0 {
				item.Listing.Visibility = catalog.VisibilityPrivate
			} else {
				item.Listing.Visibility = catalog.VisibilityTenant
			}
			changed++
		}
	}
	if changed != 3 {
		t.Fatal("missing complete Skill controls", changed)
	}
	raw, err = json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(file, raw, 0600); err != nil {
		t.Fatal(err)
	}
	plan, err := demodata.Load(file, "curated", origin)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "receipt.json")
	result, err := executeConfiguredDemo(t.Context(), server.URL, path, origin, trust, false, plan)
	if err != nil || len(result.Items) != 14 {
		t.Fatal("non-public package load failed", err)
	}
	before := writes.Load()
	if _, err = executeConfiguredDemo(t.Context(), server.URL, path, origin, "missing", false, plan); err != nil || writes.Load() != before {
		t.Fatal("non-public package rerun mutated", err)
	}
}
