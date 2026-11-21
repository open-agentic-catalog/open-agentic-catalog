//go:build functional

package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
)

// Stop with one release on disk and the second artifact partly sent.
// Retry from the original selection in a new directory.
func qualifyExportBatchCrash(t *testing.T, parent context.Context, binary, tokenFile string, api http.Handler, inline, artifact catalog.CatalogRecord, pluginID string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(parent, 30*time.Second)
	defer cancel()
	first, second := inline.Release.ID, artifact.Release.ID
	artifactPath := "/api/v1/artifacts/" + artifact.VersionRevision.Representations[0].ArtifactID + "/content"
	artifactStarted := make(chan struct{})
	var requests, downloads, pluginReads atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.URL.Path == "/api/v1/catalog/resources/"+pluginID {
			pluginReads.Add(1)
		}
		if r.URL.Path == artifactPath && downloads.Add(1) == 1 {
			reply := httptest.NewRecorder()
			api.ServeHTTP(reply, r)
			if reply.Code != http.StatusOK || reply.Body.Len() < 2 {
				t.Errorf("expected a readable Catalog artifact, got HTTP %d with %d bytes", reply.Code, reply.Body.Len())
				http.Error(w, "artifact unavailable", http.StatusBadGateway)
				return
			}
			maps.Copy(w.Header(), reply.Header())
			w.WriteHeader(reply.Code)
			if _, err := w.Write(reply.Body.Bytes()[:reply.Body.Len()/2]); err != nil {
				t.Error(err)
				return
			}
			if err := http.NewResponseController(w).Flush(); err != nil {
				t.Error(err)
				return
			}
			close(artifactStarted)
			select {
			case <-r.Context().Done():
			case <-ctx.Done():
			}
			return
		}
		api.ServeHTTP(w, r)
	}))
	t.Cleanup(server.Close)
	work := canonicalTempDir(t)
	root, err := os.OpenRoot(work)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()
	read := func(name string) []byte {
		t.Helper()
		raw, err := root.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}
	env := []string{"CATALOG_REMOTE_BASE_URL=" + server.URL, "CATALOG_REMOTE_TOKEN_FILE=" + tokenFile}
	batch := func(dir string, selectors ...string) *processCommand {
		return startCatalogctlProcess(t, ctx, binary, env, append([]string{"export", "batch", "--output-dir", filepath.Join(work, dir)}, selectors...)...)
	}
	child := batch("interrupted", "--release-id", first, "--release-id", second, "--release-id", pluginID)
	select {
	case <-artifactStarted:
	case <-child.done:
		t.Fatal("batch ended before the second release requested its artifact", child.err)
	case <-ctx.Done():
		t.Fatal("batch did not reach the second release's artifact")
	}
	if err := child.command.Process.Signal(syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	stdout, _ := child.wait(t, -1)
	status, ok := child.command.ProcessState.Sys().(syscall.WaitStatus)
	if !ok || !status.Signaled() || status.Signal() != syscall.SIGKILL || ctx.Err() != nil || len(stdout) != 0 || pluginReads.Load() != 0 {
		t.Fatal("hard-stopped batch reported completion or started the third release")
	}

	checkInterrupted := func() {
		t.Helper()
		for _, name := range []string{"batch.json", second + "/export.json", pluginID + "/export.json"} {
			if _, err := root.Lstat("interrupted/" + name); !os.IsNotExist(err) {
				t.Fatalf("unexpected completion marker %s: %v", name, err)
			}
		}
		var marker exportManifest
		if err := json.Unmarshal(read("interrupted/"+first+"/export.json"), &marker); err != nil {
			t.Fatal(err)
		}
		content := read("interrupted/" + first + "/native.json")
		if marker.ReleaseID != first || marker.File == nil || marker.File.Name != "native.json" ||
			marker.File.Digest != fmt.Sprintf("sha256:%x", sha256.Sum256(content)) || !bytes.Equal(content, inline.VersionRevision.Representations[0].Data) {
			t.Fatal("completed export lost its original native bytes or checksum")
		}
	}
	checkInterrupted()
	selection := filepath.Join(work, "interrupted", "selection.json")
	before := requests.Load()
	stdout, stderr := batch("interrupted", "--selection-file", selection).wait(t, 1)
	assertProcessTerminalFailure(t, stdout, stderr, "export")
	if !bytes.Contains(stderr, []byte("new directory")) || requests.Load() != before {
		t.Fatal("retry did not refuse the existing output directory before contacting the API")
	}

	stdout, stderr = batch("retried", "--selection-file", selection).wait(t, 0)
	var report exportBatchReport
	if len(stderr) != 0 || json.Unmarshal(stdout, &report) != nil || !report.Complete || !report.SelectionComplete || len(report.Items) != 3 {
		t.Fatalf("fresh export did not complete the saved selection: %s", stdout)
	}
	for i, want := range []struct{ id, outcome, disposition string }{
		{first, "complete", "inline_json"},
		{second, "complete", "exact_document"},
		{pluginID, "reference_only", "reference_only"},
	} {
		item := report.Items[i]
		if item.Selection.ReleaseID != want.id || item.Outcome != want.outcome || item.Disposition != want.disposition {
			t.Fatalf("unexpected recovery result for %s: %+v", want.id, item)
		}
		var marker exportManifest
		if json.Unmarshal(read("retried/"+want.id+"/export.json"), &marker) != nil || marker.ReleaseID != want.id || marker.Disposition != want.disposition {
			t.Fatalf("recovered release %s has no matching completion marker", want.id)
		}
	}
	var saved exportBatchReport
	if json.Unmarshal(read("retried/batch.json"), &saved) != nil || !reflect.DeepEqual(saved, report) {
		t.Fatal("fresh batch report on disk disagreed with stdout")
	}
	if !bytes.Equal(read("retried/"+first+"/native.json"), inline.VersionRevision.Representations[0].Data) {
		t.Fatal("fresh batch did not reproduce the first release's document")
	}
	blob := read("retried/" + second + "/native.bin")
	if fmt.Sprintf("sha256:%x", sha256.Sum256(blob)) != artifact.VersionRevision.Representations[0].ContentDigest || downloads.Load() != 2 || pluginReads.Load() != 1 {
		t.Fatal("fresh export missed the retained artifact or the final Plugin release")
	}
	checkInterrupted()
}
