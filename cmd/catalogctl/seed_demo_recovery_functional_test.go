//go:build functional

package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/demodata"
)

// A persisted intent is not proof of a commit. Recovery must retry only the
// uncommitted command, then verify actual Reader-visible identities.
func TestConfiguredDemoUncommittedIntentRecovery(t *testing.T) {
	runtime, _, origin, trust, _ := configuredDemoRuntime(t)
	var pause atomic.Bool
	pause.Store(true)
	var mu sync.Mutex
	var resourceKeys []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/api/v1/resources" {
			mu.Lock()
			resourceKeys = append(resourceKeys, r.Header.Get("Idempotency-Key"))
			mu.Unlock()
			if pause.CompareAndSwap(true, false) {
				http.Error(w, "private fault detail", http.StatusServiceUnavailable)
				return
			}
		}
		runtime.HTTP.Handler().ServeHTTP(w, r)
	}))
	defer server.Close()
	plan, err := demodata.Load("", "minimal", origin)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(canonicalTempDir(t), "receipt.json")
	if _, err = executeConfiguredDemo(t.Context(), server.URL, path, origin, trust, false, plan); err == nil || strings.Contains(err.Error(), "private fault detail") {
		t.Fatalf("interrupted load: %v", err)
	}
	p, exists, err := readDemoProgress(path+".progress", server.URL, origin)
	if err != nil || !exists || len(p.Steps) == 0 {
		t.Fatal("intent was not retained", err)
	}
	pending := p.Steps[len(p.Steps)-1]
	if pending.Path != "/api/v1/resources" || len(pending.Response) != 0 || len(pending.Before) != 0 {
		t.Fatal("pending intent claims a committed Resource")
	}
	if _, err = os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("interrupted load published receipt", err)
	}
	result, err := executeConfiguredDemo(t.Context(), server.URL, path, origin, trust, false, plan)
	if err != nil || result.Status != "resumed" || len(result.Items) != len(plan.Resources) {
		t.Fatal("uncommitted intent did not resume", err)
	}
	mu.Lock()
	keys := append([]string(nil), resourceKeys...)
	mu.Unlock()
	if len(keys) != len(plan.Resources)+1 || keys[0] == "" || keys[0] != keys[1] {
		t.Fatal("recovery did not retry exactly the same pending command")
	}
	c := &smokeHTTPClient{baseURL: server.URL, client: server.Client(), safeErrors: true}
	ids, err := demoPageIDs(t.Context(), c, "/api/v1/resources", 20)
	if err != nil || len(ids) != len(result.Items) {
		t.Fatal("recovery duplicated or omitted Resources", err)
	}
	for _, item := range result.Items {
		if item.State != "published" {
			continue
		}
		record, e := demoExchange[catalog.CatalogRecord](t.Context(), c, "GET", "/api/v1/catalog/resources/"+item.ReleaseID, "local-reader", "", "", nil, 200)
		if e != nil || record.Resource.ID != item.ResourceID || record.VersionRevision.ID != item.VersionRevisionID || record.ListingRevision.ID != item.ListingRevisionID {
			t.Fatal("Reader readback differs from recovered receipt", e)
		}
	}
	if _, err = os.Stat(path + ".progress"); !os.IsNotExist(err) {
		t.Fatal("successful recovery retained journal", err)
	}
}

func TestConfiguredDemoUncertainVersionRefusesAdoption(t *testing.T) {
	for _, fault := range []string{"ambiguous additions", "different version"} {
		t.Run(fault, func(t *testing.T) {
			runtime, _, origin, trust, _ := configuredDemoRuntime(t)
			var pause atomic.Bool
			pause.Store(true)
			var writes atomic.Int64
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodPost && r.URL.Path != "/api/v1/catalog/search" {
					writes.Add(1)
				}
				if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/versions") && pause.CompareAndSwap(true, false) {
					if fault == "different version" {
						var body map[string]any
						if e := json.NewDecoder(r.Body).Decode(&body); e != nil {
							t.Error(e)
							return
						}
						body["version"] = "0.9.0"
						raw, e := json.Marshal(body)
						if e != nil {
							t.Error(e)
							return
						}
						r = r.Clone(r.Context())
						r.Body = io.NopCloser(bytes.NewReader(raw))
						r.ContentLength = int64(len(raw))
					}
					capture := httptest.NewRecorder()
					runtime.HTTP.Handler().ServeHTTP(capture, r)
					if capture.Code != http.StatusCreated {
						t.Errorf("fault prerequisite version status %d", capture.Code)
					}
					http.Error(w, "lost version response", http.StatusServiceUnavailable)
					return
				}
				runtime.HTTP.Handler().ServeHTTP(w, r)
			}))
			defer server.Close()
			plan, err := demodata.Load("", "minimal", origin)
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(canonicalTempDir(t), "receipt.json")
			if _, err = executeConfiguredDemo(t.Context(), server.URL, path, origin, trust, false, plan); err == nil || pause.Load() {
				t.Fatal("uncertain version fault not exercised", err)
			}
			p, _, err := readDemoProgress(path+".progress", server.URL, origin)
			if err != nil {
				t.Fatal(err)
			}
			pending := p.Steps[len(p.Steps)-1]
			c := &smokeHTTPClient{baseURL: server.URL, client: server.Client(), safeErrors: true}
			if fault == "ambiguous additions" {
				var body map[string]any
				if err = json.Unmarshal(pending.Body, &body); err != nil {
					t.Fatal(err)
				}
				body["version"] = "0.9.0"
				if _, err = c.request(t.Context(), "owned competing version", "POST", pending.Path, "local-contributor", "competing-version", "", body, 201); err != nil {
					t.Fatal(err)
				}
			}
			journal := readDemoRecoveryFile(t, path+".progress")
			before := writes.Load()
			_, err = executeConfiguredDemo(t.Context(), server.URL, path, origin, trust, false, plan)
			want := "uncertain version differs"
			if fault == "ambiguous additions" {
				want = "ambiguous command outcome"
			}
			if err == nil || !strings.Contains(err.Error(), want) || writes.Load() != before {
				t.Fatalf("unsafe recovery: %v, new writes %d", err, writes.Load()-before)
			}
			if !bytes.Equal(journal, readDemoRecoveryFile(t, path+".progress")) {
				t.Fatal("refused recovery changed uncertain journal")
			}
			if _, err = os.Stat(path); !os.IsNotExist(err) {
				t.Fatal("refused recovery published receipt", err)
			}
		})
	}
}

// An uncertain immutable revision needs an exact readback. A failed read must
// preserve the intent so a later invocation can recover without another write.
func TestConfiguredDemoUncertainListingRevisionRecovery(t *testing.T) {
	for _, different := range []bool{false, true} {
		name := "temporary read failure"
		if different {
			name = "different committed content"
		}
		t.Run(name, func(t *testing.T) {
			runtime, _, origin, trust, _ := configuredDemoRuntime(t)
			var pause, failRead atomic.Bool
			pause.Store(true)
			var writes, failedReads, revisionWrites atomic.Int64
			var committed atomic.Pointer[catalog.ListingRevision]
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodPost && r.URL.Path != "/api/v1/catalog/search" {
					writes.Add(1)
				}
				if revision := committed.Load(); revision != nil && r.Method == http.MethodPost && r.URL.Path == "/api/v1/catalog-listings/"+revision.ListingID+"/revisions" {
					revisionWrites.Add(1)
				}
				if revision := committed.Load(); revision != nil && r.Method == http.MethodGet && r.URL.Path == "/api/v1/listing-revisions/"+revision.ID && failRead.CompareAndSwap(true, false) {
					failedReads.Add(1)
					http.Error(w, "private reconciliation failure", http.StatusServiceUnavailable)
					return
				}
				if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/revisions") && pause.CompareAndSwap(true, false) {
					revisionWrites.Add(1)
					if different {
						var body map[string]any
						if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
							t.Error(err)
							return
						}
						body["description"] = "A different committed listing description."
						raw, err := json.Marshal(body)
						if err != nil {
							t.Error(err)
							return
						}
						r = r.Clone(r.Context())
						r.Body = io.NopCloser(bytes.NewReader(raw))
						r.ContentLength = int64(len(raw))
					}
					capture := httptest.NewRecorder()
					runtime.HTTP.Handler().ServeHTTP(capture, r)
					var result struct {
						Listing  catalog.CatalogListing  `json:"listing"`
						Revision catalog.ListingRevision `json:"revision"`
					}
					if capture.Code != http.StatusCreated || json.Unmarshal(capture.Body.Bytes(), &result) != nil || result.Revision.ID == "" || result.Revision.ListingID != result.Listing.ID || result.Listing.CandidateRevisionID != result.Revision.ID {
						t.Error("listing revision did not commit before response loss")
					} else {
						committed.Store(&result.Revision)
					}
					http.Error(w, "private lost revision response", http.StatusServiceUnavailable)
					return
				}
				runtime.HTTP.Handler().ServeHTTP(w, r)
			}))
			defer server.Close()
			plan, err := demodata.Load("", "minimal", origin)
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(canonicalTempDir(t), "receipt.json")
			if _, err = executeConfiguredDemo(t.Context(), server.URL, path, origin, trust, false, plan); err == nil || committed.Load() == nil || strings.Contains(err.Error(), "private lost revision response") {
				t.Fatal("uncertain listing revision was not established safely", err)
			}
			p, exists, err := readDemoProgress(path+".progress", server.URL, origin)
			if err != nil || !exists || len(p.Steps) == 0 {
				t.Fatal("uncertain revision journal missing", err)
			}
			pending := p.Steps[len(p.Steps)-1]
			var input map[string]any
			if !strings.HasSuffix(pending.Path, "/revisions") || len(pending.Response) != 0 || len(pending.Before) != 0 || json.Unmarshal(pending.Body, &input) != nil || (committed.Load().Description != input["description"]) != different {
				t.Fatal("journal or committed revision does not match the selected fault")
			}
			journal := readDemoRecoveryFile(t, path+".progress")
			before := writes.Load()
			failRead.Store(!different)
			_, err = executeConfiguredDemo(t.Context(), server.URL, path, origin, trust, false, plan)
			if err == nil || writes.Load() != before || strings.Contains(err.Error(), "private reconciliation failure") || different && !strings.Contains(err.Error(), "uncertain listing revision differs") || !different && failedReads.Load() != 1 {
				t.Fatalf("unsafe uncertain revision recovery: %v, new writes %d", err, writes.Load()-before)
			}
			if !bytes.Equal(journal, readDemoRecoveryFile(t, path+".progress")) {
				t.Fatal("failed reconciliation changed the uncertain journal")
			}
			if _, err = os.Stat(path); !os.IsNotExist(err) {
				t.Fatal("failed reconciliation published a receipt", err)
			}
			if !different {
				result, err := executeConfiguredDemo(t.Context(), server.URL, path, origin, trust, false, plan)
				if err != nil || result.Status != "resumed" || len(result.Items) != len(plan.Resources) || result.Items[0].ListingRevisionID != committed.Load().ID {
					t.Fatal("healthy reconciliation did not preserve the committed revision", err)
				}
				if _, err = os.Stat(path + ".progress"); !os.IsNotExist(err) {
					t.Fatal("verified recovery retained the uncertain journal", err)
				}
			}
			c := &smokeHTTPClient{baseURL: server.URL, client: server.Client(), safeErrors: true}
			ids, err := demoPageIDs(t.Context(), c, pending.Path, 20)
			if err != nil || len(ids) != 1 || ids[0] != committed.Load().ID || revisionWrites.Load() != 1 {
				t.Fatal("recovery duplicated or replaced the committed listing revision", err)
			}
		})
	}
}

func readDemoRecoveryFile(t *testing.T, path string) []byte {
	t.Helper()
	raw, err := os.ReadFile(path) // #nosec G304 -- exact invocation-owned temporary receipt or recovery journal.
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestConfiguredDemoCompletedReceiptRejectsUnmatchedJournal(t *testing.T) {
	runtime, _, origin, trust, _ := configuredDemoRuntime(t)
	path := filepath.Join(canonicalTempDir(t), "receipt.json")
	var writes atomic.Int64
	var mu sync.Mutex
	var completed []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path != "/api/v1/catalog/search" {
			writes.Add(1)
		}
		if r.URL.Path == "/.well-known/ard.json" {
			raw, e := os.ReadFile(path + ".progress") // #nosec G304 -- exact invocation-owned journal, captured before receipt replacement.
			if e != nil {
				t.Error(e)
			}
			mu.Lock()
			completed = raw
			mu.Unlock()
		}
		runtime.HTTP.Handler().ServeHTTP(w, r)
	}))
	defer server.Close()
	plan, err := demodata.Load("", "minimal", origin)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = executeConfiguredDemo(t.Context(), server.URL, path, origin, trust, false, plan); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	journalBytes := append([]byte(nil), completed...)
	mu.Unlock()
	if len(journalBytes) == 0 {
		t.Fatal("completed command prefix was not observed")
	}
	receipt := readDemoRecoveryFile(t, path)
	before := writes.Load()
	for _, fault := range []string{"different binding", "incomplete prefix"} {
		t.Run(fault, func(t *testing.T) {
			var p demoProgress
			if err := json.Unmarshal(journalBytes, &p); err != nil {
				t.Fatal(err)
			}
			want := "completed receipt has mismatched progress"
			if fault == "different binding" {
				p.PlanDigest = demoHash("another frozen plan")
			} else {
				p.Steps = p.Steps[:len(p.Steps)-1]
				want = "completed receipt has incomplete or mismatched progress"
			}
			raw, err := json.Marshal(p)
			if err != nil {
				t.Fatal(err)
			}
			if err = os.WriteFile(path+".progress", raw, 0600); err != nil { // #nosec G306 -- private owned journal deliberately simulates interrupted cleanup.
				t.Fatal(err)
			}
			_, err = executeConfiguredDemo(t.Context(), server.URL, path, origin, trust, false, plan)
			if err == nil || !strings.Contains(err.Error(), want) || writes.Load() != before {
				t.Fatal("unmatched journal accepted or caused commands", err)
			}
			if !bytes.Equal(raw, readDemoRecoveryFile(t, path+".progress")) || !bytes.Equal(receipt, readDemoRecoveryFile(t, path)) {
				t.Fatal("unmatched journal refusal changed retained files")
			}
		})
	}
}

func TestConfiguredDemoFinalReadbackRecoveryWithoutCommands(t *testing.T) {
	runtime, _, origin, trust, _ := configuredDemoRuntime(t)
	var fault atomic.Bool
	fault.Store(true)
	var writes atomic.Int64
	var observed atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path != "/api/v1/catalog/search" {
			writes.Add(1)
		}
		if r.URL.Path == "/api/v1/catalog/search" && fault.CompareAndSwap(true, false) {
			capture := httptest.NewRecorder()
			runtime.HTTP.Handler().ServeHTTP(capture, r)
			var response catalog.CatalogSearchResponse
			if capture.Code != http.StatusOK || json.Unmarshal(capture.Body.Bytes(), &response) != nil || len(response.Items) == 0 {
				t.Error("final Search prerequisite failed")
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			response.Partial = true
			observed.Add(1)
			w.Header().Set("Content-Type", "application/json")
			if e := json.NewEncoder(w).Encode(response); e != nil {
				t.Error(e)
			}
			return
		}
		runtime.HTTP.Handler().ServeHTTP(w, r)
	}))
	defer server.Close()
	plan, err := demodata.Load("", "minimal", origin)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(canonicalTempDir(t), "receipt.json")
	if _, err = executeConfiguredDemo(t.Context(), server.URL, path, origin, trust, false, plan); err == nil || !strings.Contains(err.Error(), "incomplete local discovery") || observed.Load() != 1 {
		t.Fatal("partial final readback accepted", err)
	}
	if _, err = os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("partial final readback published receipt", err)
	}
	p, exists, err := readDemoProgress(path+".progress", server.URL, origin)
	if err != nil || !exists || len(p.Steps) == 0 {
		t.Fatal("completed journal missing", err)
	}
	last := p.Steps[len(p.Steps)-1]
	if !strings.HasSuffix(last.Path, ":activate") || len(last.Response) == 0 {
		t.Fatal("failure occurred before activation completed")
	}
	var expected []string
	for _, step := range p.Steps {
		if step.Path == "/api/v1/resources" {
			var resource catalog.Resource
			if err = json.Unmarshal(step.Response, &resource); err != nil {
				t.Fatal(err)
			}
			expected = append(expected, resource.ID)
		}
	}
	before := writes.Load()
	result, err := executeConfiguredDemo(t.Context(), server.URL, path, origin, trust, false, plan)
	if err != nil || result.Status != "resumed" || writes.Load() != before {
		t.Fatalf("readback recovery repeated commands: %v, writes %d", err, writes.Load()-before)
	}
	actual := make([]string, 0, len(result.Items))
	for _, item := range result.Items {
		actual = append(actual, item.ResourceID)
	}
	if !reflect.DeepEqual(actual, expected) {
		t.Fatal("final readback recovery changed retained Resource identities")
	}
	if _, err = os.Stat(path + ".progress"); !os.IsNotExist(err) {
		t.Fatal("verified recovery did not remove journal", err)
	}
}

// A well-formed success for another snapshot must not become a saved lookup
// hint. The owning command path, rather than the reply, binds recovery.
func TestConfiguredDemoActivationResponseBindsCommandPath(t *testing.T) {
	runtime, _, origin, trust, _ := configuredDemoRuntime(t)
	const otherID = "00000000-0000-7000-8000-000000000099"
	var fault atomic.Bool
	fault.Store(true)
	var writes, changed, foreignReads atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path != "/api/v1/catalog/search" {
			writes.Add(1)
		}
		if r.Method == http.MethodGet && strings.Contains(r.URL.Path, otherID) {
			foreignReads.Add(1)
		}
		if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, ":activate") && fault.CompareAndSwap(true, false) {
			capture := httptest.NewRecorder()
			runtime.HTTP.Handler().ServeHTTP(capture, r)
			var response map[string]any
			if capture.Code != http.StatusOK || json.Unmarshal(capture.Body.Bytes(), &response) != nil || response["state"] != "active" || response["tenantId"] != demoTenant || response["id"] == otherID {
				t.Error("valid activation prerequisite failed")
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			response["id"] = otherID
			changed.Add(1)
			for key, values := range capture.Header() {
				w.Header()[key] = values
			}
			w.Header().Del("Content-Length")
			w.WriteHeader(capture.Code)
			if e := json.NewEncoder(w).Encode(response); e != nil {
				t.Error(e)
			}
			return
		}
		runtime.HTTP.Handler().ServeHTTP(w, r)
	}))
	defer server.Close()
	plan, err := demodata.Load("", "minimal", origin)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(canonicalTempDir(t), "receipt.json")
	if _, err = executeConfiguredDemo(t.Context(), server.URL, path, origin, trust, false, plan); err == nil || !strings.Contains(err.Error(), "command response does not match fixed fixture input") || changed.Load() != 1 || foreignReads.Load() != 0 {
		t.Fatal("foreign snapshot success was used", err)
	}
	p, exists, err := readDemoProgress(path+".progress", server.URL, origin)
	if err != nil || !exists || len(p.Steps) == 0 {
		t.Fatal("uncertain activation intent not retained", err)
	}
	last := p.Steps[len(p.Steps)-1]
	if !strings.HasSuffix(last.Path, ":activate") || len(last.Response) != 0 {
		t.Fatal("foreign success was saved in journal")
	}
	if _, err = os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("foreign activation success published receipt", err)
	}
	before := writes.Load()
	result, err := executeConfiguredDemo(t.Context(), server.URL, path, origin, trust, false, plan)
	if err != nil || result.Status != "resumed" || writes.Load() != before || !strings.HasSuffix(last.Path, "/"+result.SnapshotID+":activate") || result.SnapshotID == otherID {
		t.Fatal("exact snapshot recovery failed or repeated commands", err)
	}
	if _, err = os.Stat(path + ".progress"); !os.IsNotExist(err) {
		t.Fatal("verified activation recovery retained journal", err)
	}
}
