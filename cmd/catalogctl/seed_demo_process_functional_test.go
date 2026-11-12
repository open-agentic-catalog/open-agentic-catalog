//go:build functional

// Configured seeding retains recoverable intent across actual child-process death.
package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/demodata"
	"github.com/open-agentic-catalog/open-agentic-catalog/samples"
)

// configuredSeedProcessPlan selects one maintained published Agent without archive delivery.
func configuredSeedProcessPlan(t *testing.T, origin string) (string, demodata.Plan) {
	t.Helper()
	base := demodata.DefaultPlan("minimal", origin)
	var resource demodata.Resource
	for _, item := range base.Resources {
		if item.Resource.Key == "order-agent" {
			resource = item.Resource
		}
	}
	if resource.Kind != catalog.KindAgent || resource.State != "published" {
		t.Fatal("maintained process fixture no longer selects the published Agent")
	}
	card, err := samples.Files.ReadFile(resource.Representation.File)
	if err != nil {
		t.Fatal(err)
	}
	native := string(card)
	resource.Representation.File = ""
	resource.Representation.Inline = &native
	manifest := demodata.Manifest{
		Format: 1, Dataset: "process-recovery", Revision: "one",
		Profiles:  map[string][]string{"process-recovery": {resource.Key}},
		Resources: []demodata.Resource{resource},
	}
	for _, namespace := range base.Namespaces {
		if namespace.Key == resource.Namespace {
			manifest.Namespaces = append(manifest.Namespaces, namespace)
		}
	}
	for _, publisher := range base.Publishers {
		if publisher.Key == resource.Publisher {
			manifest.Publishers = append(manifest.Publishers, publisher)
		}
	}
	raw, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	filename := filepath.Join(canonicalTempDir(t), "catalog.yaml")
	if err = os.WriteFile(filename, raw, 0600); err != nil { // #nosec G703 -- fixed manifest in this test's private temporary directory.
		t.Fatal(err)
	}
	plan, err := demodata.Load(filename, "process-recovery", origin)
	if err != nil || len(plan.Resources) != 1 || len(plan.Namespaces) != 1 || len(plan.Publishers) != 1 {
		t.Fatal("invalid compact configured process fixture", err)
	}
	return filename, plan
}

// seedCommittedRequest captures the successful command whose response is withheld.
type seedCommittedRequest struct {
	body, response   []byte
	key, actor, etag string
	status           int
}

// TestConfiguredSeedProcessRecovery preserves a committed-but-unacknowledged Resource
// through SIGKILL and SIGTERM, then resumes in a separate process without duplicates.
func TestConfiguredSeedProcessRecovery(t *testing.T) {
	buildContext, cancelBuild := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancelBuild()
	binary := buildCatalogctlProcessBinary(t, buildContext)
	cancelBuild()
	for _, signal := range []syscall.Signal{syscall.SIGKILL, syscall.SIGTERM} {
		t.Run(signal.String(), func(t *testing.T) {
			runtime, _, origin, trust, _ := configuredDemoRuntime(t)
			file, plan := configuredSeedProcessPlan(t, origin)
			ctx, cancel := context.WithTimeout(t.Context(), 35*time.Second)
			defer cancel()
			committed := make(chan seedCommittedRequest, 1)
			disconnected := make(chan struct{})
			release := make(chan struct{})
			var once sync.Once
			unblock := func() { once.Do(func() { close(release) }) }
			var selected atomic.Bool
			var calls atomic.Int64
			var mu sync.Mutex
			var mutations []string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if r.Method != http.MethodGet && !(r.Method == http.MethodPost && (r.URL.Path == "/api/v1/catalog/search" || r.URL.Path == "/api/v1/catalog/explore")) {
					mu.Lock()
					mutations = append(mutations, r.URL.Path)
					mu.Unlock()
				}
				if r.Method == http.MethodPost && r.URL.Path == "/api/v1/resources" && selected.CompareAndSwap(false, true) {
					body, err := io.ReadAll(r.Body)
					if err != nil {
						t.Error("cannot read the selected owned mutation", err)
						return
					}
					r.Body = io.NopCloser(bytes.NewReader(body))
					capture := httptest.NewRecorder()
					runtime.HTTP.Handler().ServeHTTP(capture, r)
					committed <- seedCommittedRequest{body: body, response: bytes.Clone(capture.Body.Bytes()), key: r.Header.Get("Idempotency-Key"), actor: r.Header.Get("Authorization"), etag: r.Header.Get("If-Match"), status: capture.Code}
					select {
					case <-r.Context().Done():
						close(disconnected)
					case <-release:
					}
					return
				}
				runtime.HTTP.Handler().ServeHTTP(w, r)
			}))
			t.Cleanup(server.Close)
			t.Cleanup(unblock)
			mutationCount := func() int {
				mu.Lock()
				defer mu.Unlock()
				return len(mutations)
			}
			state := filepath.Join(canonicalTempDir(t), "PRIVATE_SEED_PROCESS", "receipt.json")
			args := []string{"seed-demo", "--data-file", file, "--sample-set", "process-recovery", "--base-url", server.URL, "--state-file", state, "--archive-origin", origin, "--archive-trust-file", trust, "--timeout", "25s"}
			start := func(collect bool) *processCommand {
				var environment []string
				if collect {
					environment = catalogctlProcessEnvironment(t)
				} else {
					// A hard-killed Go child cannot flush counters. As in the
					// import signal fixture, omit only its collector input before
					// creating a directory; all orderly commands stay measured.
					environment = catalogctlProcessBaseEnvironment(t)
				}
				return startCatalogctlProcess(t, ctx, binary, environment, args...)
			}
			pending := start(signal != syscall.SIGKILL)
			var observed seedCommittedRequest
			select {
			case observed = <-committed:
			case <-pending.done:
				t.Fatalf("seed child exited before the commit barrier: %v; stderr=%s", pending.err, pending.stderr.snapshot())
			case <-ctx.Done():
				t.Fatal("seed child did not reach its owned commit barrier")
			}
			if observed.status != http.StatusCreated {
				t.Fatal("selected Resource command did not commit successfully", observed.status)
			}
			journal := readDemoRecoveryFile(t, state+".progress")
			var progress demoProgress
			if json.Unmarshal(journal, &progress) != nil || len(progress.Steps) != 4 || progress.PlanDigest != demoPlanBinding(plan) || progress.Original != nil {
				t.Fatal("durable journal does not bind the compact configured plan")
			}
			for _, step := range progress.Steps[:3] {
				if len(step.Response) == 0 {
					t.Fatal("completed parent command was not acknowledged before Resource intent")
				}
			}
			var namespace catalog.Namespace
			var publisher, approval catalog.Publisher
			if json.Unmarshal(progress.Steps[0].Response, &namespace) != nil || namespace.ID == "" || namespace.Slug != plan.Namespaces[0].Key || json.Unmarshal(progress.Steps[1].Response, &publisher) != nil || publisher.ID == "" || json.Unmarshal(progress.Steps[2].Response, &approval) != nil || approval.ID != publisher.ID || !reflect.DeepEqual(approval.ApprovedPublicationDomains, plan.Publishers[0].Domains) {
				t.Fatal("acknowledged parent commands do not retain their Namespace, Publisher and approval")
			}
			if progress.Steps[0].Path != "/api/v1/namespaces" || progress.Steps[1].Path != "/api/v1/publishers" || progress.Steps[2].Path != "/api/v1/publishers/"+publisher.ID+"/publication-domains/"+plan.Publishers[0].Domains[0]+":approve" {
				t.Fatal("acknowledged parent prefix differs from the selected configured inputs")
			}
			step := progress.Steps[3]
			var resource catalog.Resource
			if json.Unmarshal(observed.response, &resource) != nil || resource.ID == "" {
				t.Fatal("committed Resource response lacks its retained identity")
			}
			var body map[string]any
			if json.Unmarshal(step.Body, &body) != nil || !reflect.DeepEqual(body, map[string]any{"namespaceId": resource.NamespaceID, "canonicalIdentifier": plan.Resources[0].Resource.Identifier, "kind": "agent"}) {
				t.Fatal("pending Resource intent differs from the selected authoring inputs")
			}
			if step.Path != "/api/v1/resources" || step.Actor != "local-contributor" || step.Key == "" || step.Status != 201 || step.ETag != "" || len(step.Response) != 0 || len(step.Before) != 0 || !bytes.Equal(step.Body, observed.body) || observed.actor != "Bearer "+step.Actor || observed.etag != step.ETag {
				t.Fatal("journal did not retain the exact unacknowledged command and empty pre-state")
			}
			wireHash := sha256.Sum256(append([]byte(step.Path+step.Actor+step.ETag), step.Body...))
			if observed.key != step.Key+"-"+fmt.Sprintf("%x", wireHash)[:16] {
				t.Fatal("wire command key differs from its persisted bound intent")
			}
			lock, err := os.Stat(state + ".lock") // #nosec G703 -- exact adjacent lock in this test's private state directory.
			if err != nil || lock.Mode().Perm() != 0600 {
				t.Fatal("seed lock is not a retained private file", err)
			}
			if _, err = os.Stat(state); !errors.Is(err, os.ErrNotExist) { // #nosec G703 -- exact private receipt whose completion must not be claimed yet.
				t.Fatal("pending command wrote a completed receipt", err)
			}
			beforeCalls, beforeWrites := calls.Load(), mutationCount()
			overlap := start(true)
			overlapOutput, overlapDiagnostic := overlap.wait(t, 1)
			assertProcessTerminalFailure(t, overlapOutput, overlapDiagnostic, "seed-demo", "PRIVATE_SEED_PROCESS")
			if len(overlapOutput) != 0 || !bytes.Contains(overlapDiagnostic, []byte("another seed command is running")) || calls.Load() != beforeCalls || mutationCount() != beforeWrites {
				t.Fatal("competing process did not refuse the held lock before API access")
			}
			if !bytes.Equal(journal, readDemoRecoveryFile(t, state+".progress")) {
				t.Fatal("competing command changed the pending journal")
			}
			if err = pending.command.Process.Signal(signal); err != nil {
				t.Fatal("cannot signal the owned seed child", err)
			}
			var output, diagnostic []byte
			if signal == syscall.SIGKILL {
				output, diagnostic = pending.wait(t, -1)
				var exit *exec.ExitError
				if !errors.As(pending.err, &exit) {
					t.Fatal("hard-stop child did not return a process exit")
				}
				status, ok := exit.Sys().(syscall.WaitStatus)
				if !ok || !status.Signaled() || status.Signal() != signal {
					t.Fatal("hard-stop control did not kill the selected child", pending.err)
				}
			} else {
				output, diagnostic = pending.wait(t, 1)
				assertProcessTerminalFailure(t, output, diagnostic, "seed-demo", "PRIVATE_SEED_PROCESS")
			}
			if len(output) != 0 {
				t.Fatal("interrupted seed child claimed successful completion")
			}
			if bytes.Contains(diagnostic, []byte("PRIVATE_SEED_PROCESS")) || bytes.Contains(overlapDiagnostic, []byte("PRIVATE_SEED_PROCESS")) {
				t.Fatal("failed command disclosed its private receipt path")
			}
			select {
			case <-disconnected:
			case <-ctx.Done():
				t.Fatal("interrupted seed child retained its in-flight connection")
			}
			if mutationCount() != beforeWrites || !bytes.Equal(journal, readDemoRecoveryFile(t, state+".progress")) {
				t.Fatal("interrupted child changed uncertain work or issued a later command")
			}
			resumed := start(true)
			output, diagnostic = resumed.wait(t, 0)
			if len(diagnostic) != 0 {
				t.Fatalf("fresh-process recovery wrote diagnostics: %s", diagnostic)
			}
			var report demoReport
			decoder := json.NewDecoder(bytes.NewReader(output))
			if decoder.Decode(&report) != nil || decoder.Decode(new(any)) != io.EOF || report.Status != "resumed" || report.Published != 1 || report.Drafts != 0 || report.PendingReview != 0 || len(report.Items) != 1 || report.Items[0].ResourceID != resource.ID {
				t.Fatal("recovery did not return one completed report with the original Resource")
			}
			if report.NamespaceID != namespace.ID || report.PublisherID != publisher.ID || report.Bindings == nil || report.Bindings.Namespaces[plan.Namespaces[0].Key] != namespace.ID || report.Bindings.Publishers[plan.Publishers[0].Key] != publisher.ID {
				t.Fatal("fresh recovery changed already acknowledged parent identities")
			}
			assertSeedProcessReadback(t, ctx, server.URL, report)
			retained, exists, err := readDemoReceipt(state, server.URL)
			if err != nil || !exists || !reflect.DeepEqual(retained, report.demoReceipt) {
				t.Fatal("completed receipt does not match verified process output", err)
			}
			if _, err = os.Stat(state + ".progress"); !errors.Is(err, os.ErrNotExist) { // #nosec G703 -- only the exact owned recovery journal is checked.
				t.Fatal("successful recovery retained unfinished intent", err)
			}
			currentLock, err := os.Stat(state + ".lock") // #nosec G703 -- exact persistent lock file, never removed to force recovery.
			if err != nil || !os.SameFile(lock, currentLock) {
				t.Fatal("recovery replaced the lock identity instead of releasing the original lock", err)
			}
			beforeWrites = mutationCount()
			again := start(true)
			output, diagnostic = again.wait(t, 0)
			if len(diagnostic) != 0 || mutationCount() != beforeWrites {
				t.Fatal("completed process rerun issued authoring commands")
			}
			var matched demoReport
			if json.Unmarshal(output, &matched) != nil || matched.Status != "already_matched" || !reflect.DeepEqual(matched.demoReceipt, report.demoReceipt) {
				t.Fatal("read-only completed rerun changed retained identities")
			}
			mu.Lock()
			posts := map[string]int{}
			for _, path := range mutations {
				posts[path]++
			}
			mu.Unlock()
			for _, path := range []string{progress.Steps[0].Path, progress.Steps[1].Path, progress.Steps[2].Path, "/api/v1/resources"} {
				if posts[path] != 1 {
					t.Fatal("fresh recovery repeated an acknowledged parent or committed Resource command", path, posts[path])
				}
			}
		})
	}
}

// assertSeedProcessReadback checks exact published identities and no duplicate stable objects.
func assertSeedProcessReadback(t *testing.T, ctx context.Context, origin string, report demoReport) {
	t.Helper()
	item := report.Items[0]
	c := &smokeHTTPClient{baseURL: origin, safeErrors: true, client: newLocalWorkflowClient()}
	defer c.client.CloseIdleConnections()
	for path, expected := range map[string]string{
		"/api/v1/resources": item.ResourceID,
		"/api/v1/resources/" + item.ResourceID + "/versions":       item.VersionID,
		"/api/v1/resources/" + item.ResourceID + "/listings":       item.ListingID,
		"/api/v1/catalog-listings/" + item.ListingID + "/releases": item.ReleaseID,
	} {
		ids, err := demoPageIDs(ctx, c, path, 20)
		if err != nil || len(ids) != 1 || ids[0] != expected {
			t.Fatal("fresh-process recovery duplicated or replaced an authoritative object", path, err)
		}
	}
	record, err := demoExchange[catalog.CatalogRecord](ctx, c, "GET", "/api/v1/catalog/resources/"+item.ReleaseID, "local-reader", "", "", nil, http.StatusOK)
	if err != nil || record.Resource.ID != item.ResourceID || record.Resource.NamespaceID != report.NamespaceID || record.VersionRevision.ResourceVersionID != item.VersionID || record.VersionRevision.ID != item.VersionRevisionID || record.Listing.ID != item.ListingID || record.ListingRevision.ID != item.ListingRevisionID || record.ListingRevision.PublisherID != report.PublisherID {
		t.Fatal("Reader-visible release differs from the recovered receipt", err)
	}
}
