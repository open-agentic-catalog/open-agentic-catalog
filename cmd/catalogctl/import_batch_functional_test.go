//go:build functional

package main

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
)

// Reuse the single-item protocol controls and full-body/replay assertions.
func newImportBatchHTTPFixture(t *testing.T, batch importBatchPlan) *importHTTPFixture {
	t.Helper()
	fixture := newImportHTTPFixture(t, batch.Items[0])
	fixture.plans = batch.Items
	fixture.replies = nil
	for index, plan := range batch.Items {
		replies := importTestReplies(t, plan)
		for i := range replies {
			for suffix := range 6 {
				old := fmt.Sprintf("00000000-0000-7000-8000-%012d", 101+suffix)
				replacement := fmt.Sprintf("00000000-0000-7000-8000-%012d", 101+index*10+suffix)
				replies[i].Data = bytes.ReplaceAll(replies[i].Data, []byte(old), []byte(replacement))
			}
		}
		fixture.replies = append(fixture.replies, replies...)
	}
	return fixture
}

func TestImportBatchHTTPRecoveryAtLaterSteps(t *testing.T) {
	for step := 4; step < 8; step++ {
		t.Run(fmt.Sprint(step), func(t *testing.T) {
			dir, m := importBatchTestInput(t, 3)
			batch, err := prepareImportBatchTest(t, dir, m)
			if err != nil {
				t.Fatal(err)
			}
			fixture := newImportBatchHTTPFixture(t, batch)
			fixture.fault, fixture.faultStep = "malformed", step
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fixture.serve(t, w, r) }))
			defer server.Close()
			options := remoteOptions{baseURL: server.URL, token: "snapshot-token", maxResponseBytes: 8 << 20}
			state := filepath.Join(dir, "private", "state.json")
			first, err := applyImport(t.Context(), batch, options, state, false)
			if err == nil || first.Disposition != "uncertain" || first.CurrentKey != "item-1" || first.Items[0].Disposition != "complete" || first.Items[1].Disposition != "uncertain" || first.Items[2].Disposition != "unattempted" || fixture.calls != step+1 {
				t.Fatalf("partial outcome: %+v %v", first, err)
			}
			completed, err := applyImport(t.Context(), batch, options, state, true)
			if err != nil || completed.Disposition != "complete" || fixture.commits != 12 || fixture.calls != 13 {
				t.Fatalf("recovery: %+v %v", completed, err)
			}
			for index, item := range completed.Items {
				for s, entry := range item.Steps {
					if entry.Key != first.Items[index].Steps[s].Key || entry.State != "acknowledged" {
						t.Fatal("original keys or acknowledged progress changed")
					}
				}
			}
			for range 2 {
				if result, err := applyImport(t.Context(), batch, options, state, true); err != nil || result.Disposition != "complete" {
					t.Fatal("completed rerun failed", err)
				}
			}
			if fixture.calls != 13 || fixture.commits != 12 {
				t.Fatal("completed batch repeated mutations")
			}
		})
	}
}

func TestImportBatchStopsAndPreflightsBeforeWrites(t *testing.T) {
	for _, test := range []struct {
		fault       string
		step        int
		disposition string
		calls       int
	}{
		{"blocked", 5, "blocked", 6}, {"collision", 4, "failed", 5}, {"pending", 6, "uncertain", 7},
	} {
		t.Run(test.fault, func(t *testing.T) {
			dir, m := importBatchTestInput(t, 3)
			batch, err := prepareImportBatchTest(t, dir, m)
			if err != nil {
				t.Fatal(err)
			}
			fixture := newImportBatchHTTPFixture(t, batch)
			fixture.fault, fixture.faultStep = test.fault, test.step
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fixture.serve(t, w, r) }))
			defer server.Close()
			result, err := applyImport(t.Context(), batch, remoteOptions{baseURL: server.URL, token: "snapshot-token", maxResponseBytes: 8 << 20}, filepath.Join(dir, "private", "state.json"), false)
			if err == nil || result.Disposition != test.disposition || result.Items[0].Disposition != "complete" || result.Items[1].Disposition != test.disposition || result.Items[2].Disposition != "unattempted" || fixture.calls != test.calls {
				t.Fatalf("later failure did not stop batch: %+v %v", result, err)
			}
		})
	}
	t.Run("later namespace denied", func(t *testing.T) {
		dir, m := importBatchTestInput(t, 2)
		batch, err := prepareImportBatchTest(t, dir, m)
		if err != nil {
			t.Fatal(err)
		}
		fixture := newImportBatchHTTPFixture(t, batch)
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			fixture.deny = r.URL.Path == "/api/v1/me" && r.URL.Query().Get("namespaceId") == m.Resources[1].NamespaceID
			fixture.serve(t, w, r)
		}))
		defer server.Close()
		result, err := applyImport(t.Context(), batch, remoteOptions{baseURL: server.URL, token: "snapshot-token", maxResponseBytes: 8 << 20}, filepath.Join(dir, "private", "state.json"), false)
		if err == nil || fixture.calls != 0 || result.CurrentKey != "item-1" || result.Items[0].Disposition != "unattempted" {
			t.Fatal("later authorization failure permitted earlier writes")
		}
	})
}

func TestImportBatchCancellationPreservesEarlierProgress(t *testing.T) {
	dir, manifest := importBatchTestInput(t, 3)
	batch, err := prepareImportBatchTest(t, dir, manifest)
	if err != nil {
		t.Fatal(err)
	}
	fixture := newImportBatchHTTPFixture(t, batch)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	release := make(chan struct{})
	unblock := sync.OnceFunc(func() { close(release) })
	fixture.beforeReply = func(index int) {
		if index == 5 {
			// The second item's version commits, but no response can reach the
			// cancelled client. Block delivery until apply has returned.
			cancel()
			<-release
		}
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fixture.serve(t, w, r) }))
	defer func() { unblock(); server.Close() }()
	options := remoteOptions{baseURL: server.URL, token: "snapshot-token", maxResponseBytes: 8 << 20}
	state := filepath.Join(dir, "private", "state.json")
	first, err := applyImport(ctx, batch, options, state, false)
	unblock()
	fixture.mu.Lock()
	calls, commits := fixture.calls, fixture.commits
	fixture.mu.Unlock()
	if err == nil || first.Disposition != "uncertain" || first.CurrentKey != "item-1" || first.Items[0].Disposition != "complete" || first.Items[1].Disposition != "uncertain" || first.Items[2].Disposition != "unattempted" || calls != 6 || commits != 6 {
		t.Fatalf("cancellation lost partial progress or continued writes: %+v %v", first, err)
	}
	raw, err := os.ReadFile(state) // #nosec G304 -- exact invocation-owned private receipt.
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := decodeImportReceipt(raw)
	if err != nil || receipt.Steps[5].State != "uncertain" || receipt.Steps[6].State != "ready" {
		t.Fatal("cancelled command or untouched later work was not retained", err)
	}
	completed, err := applyImport(t.Context(), batch, options, state, true)
	if err != nil || completed.Disposition != "complete" || fixture.calls != 13 || fixture.commits != 12 {
		t.Fatal("explicit recovery did not replay the original command once", err)
	}
	for index, item := range completed.Items {
		for step, entry := range item.Steps {
			if entry.Key != first.Items[index].Steps[step].Key || entry.State != "acknowledged" {
				t.Fatal("cancellation recovery replaced original command keys")
			}
		}
	}
}

func TestImportBatchResumeRequiresRetainedPrefixReads(t *testing.T) {
	dir, manifest := importBatchTestInput(t, 3)
	batch, err := prepareImportBatchTest(t, dir, manifest)
	if err != nil {
		t.Fatal(err)
	}
	fixture := newImportBatchHTTPFixture(t, batch)
	fixture.fault, fixture.faultStep = "malformed", 5
	var deniedPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fixture.mu.Lock()
		denied := r.Method == http.MethodGet && r.URL.Path == deniedPath
		fixture.mu.Unlock()
		if denied {
			w.Header().Set("Content-Type", "application/problem+json")
			w.Header().Set("X-Request-ID", "retained-read-unavailable")
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"code":"DEPENDENCY_UNAVAILABLE"}`))
			return
		}
		fixture.serve(t, w, r)
	}))
	defer server.Close()
	options := remoteOptions{baseURL: server.URL, token: "snapshot-token", maxResponseBytes: 8 << 20}
	state := filepath.Join(dir, "private", "state.json")
	first, err := applyImport(t.Context(), batch, options, state, false)
	if err == nil || first.Items[0].Disposition != "complete" || fixture.calls != 6 {
		t.Fatal("control did not retain an earlier item and an uncertain later command", err)
	}
	before, err := os.ReadFile(state) // #nosec G304 -- exact invocation-owned private receipt.
	if err != nil {
		t.Fatal(err)
	}
	fixture.mu.Lock()
	// The current item's Resource is already acknowledged, but its Version
	// response was lost. Qualify that Resource before replaying the Version.
	deniedPath = "/api/v1/resources/" + first.Items[1].Steps[0].Ack.ID
	fixture.mu.Unlock()
	refused, err := applyImport(t.Context(), batch, options, state, true)
	after, readErr := os.ReadFile(state) // #nosec G304 -- exact invocation-owned private receipt.
	if err == nil || readErr != nil || refused.CurrentKey != "item-1" || refused.Items[0].Disposition != "complete" || refused.Items[2].Disposition != "unattempted" || fixture.calls != 6 || !bytes.Equal(before, after) {
		t.Fatal("resume wrote before qualifying an earlier retained draft, or changed its receipt", err, readErr)
	}
	fixture.mu.Lock()
	deniedPath = ""
	fixture.mu.Unlock()
	completed, err := applyImport(t.Context(), batch, options, state, true)
	if err != nil || completed.Disposition != "complete" || fixture.calls != 13 || fixture.commits != 12 {
		t.Fatal("restored retained reads did not permit original-key recovery", err)
	}
}

func TestImportBatchResumeRejectsChangedWholeIntent(t *testing.T) {
	for _, change := range []string{"earlier asset", "append", "remove", "reorder"} {
		t.Run(change, func(t *testing.T) {
			dir, m := importBatchTestInput(t, 3)
			batch, err := prepareImportBatchTest(t, dir, m)
			if err != nil {
				t.Fatal(err)
			}
			fixture := newImportBatchHTTPFixture(t, batch)
			fixture.fault, fixture.faultStep = "malformed", 5
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fixture.serve(t, w, r) }))
			defer server.Close()
			options := remoteOptions{baseURL: server.URL, token: "snapshot-token", maxResponseBytes: 8 << 20}
			state := filepath.Join(dir, "private", "state.json")
			if _, err := applyImport(t.Context(), batch, options, state, false); err == nil {
				t.Fatal("missing injected interruption")
			}
			switch change {
			case "earlier asset":
				writeImportTestFile(t, dir, "agent.json", []byte(`{"name":"Changed","framework":"custom"}`), 0600)
			case "append":
				item := m.Resources[0]
				item.Key = "appended"
				item.CanonicalIdentifier = "custom:appended"
				m.Resources = append(m.Resources, item)
			case "remove":
				m.Resources = m.Resources[:2]
			case "reorder":
				m.Resources[0], m.Resources[1] = m.Resources[1], m.Resources[0]
			}
			changed, err := prepareImportBatchTest(t, dir, m)
			if err != nil {
				t.Fatal(err)
			}
			result, err := applyImport(t.Context(), changed, options, state, true)
			if err == nil || fixture.calls != 6 {
				t.Fatal("changed whole intent reached a mutation")
			}
			for _, item := range result.Items {
				if len(item.Steps) != 0 || item.Disposition != "unattempted" {
					t.Fatal("changed manifest was labeled with another item's saved progress")
				}
			}
		})
	}
}

func TestImportBatchFixtureKeepsWholeRequestAssertions(t *testing.T) {
	// A known earlier dependency remains an exact canonical/version reference.
	dir, m := importBatchTestInput(t, 2)
	m.Resources[1].Listing.Relationships = append(m.Resources[1].Listing.Relationships, catalog.Relationship{Type: "depends_on", TargetIdentifier: m.Resources[0].CanonicalIdentifier, TargetVersion: m.Resources[0].Version})
	batch, err := prepareImportBatchTest(t, dir, m)
	if err != nil {
		t.Fatal(err)
	}
	fixture := newImportBatchHTTPFixture(t, batch)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fixture.serve(t, w, r) }))
	defer server.Close()
	result, err := applyImport(t.Context(), batch, remoteOptions{baseURL: server.URL, token: "snapshot-token", maxResponseBytes: 8 << 20}, filepath.Join(dir, "private", "state.json"), false)
	if err != nil || result.Disposition != "complete" || fixture.calls != 8 {
		t.Fatal("relationship batch failed", err)
	}
	raw, err := encodeImportReport(result)
	if err != nil || strings.Contains(string(raw), "framework") {
		t.Fatal("batch report exposed native content")
	}
}
