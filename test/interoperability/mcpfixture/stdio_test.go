package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"testing/synctest"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/demodata"
)

func TestStdioPeerShutdownWaitsForCancellationAndReportsSuccess(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		stopped := stopStdio(ctx, &http.Server{}) // #nosec G112 -- this isolated shutdown test opens no listener.
		synctest.Wait()
		select {
		case err := <-stopped:
			t.Fatalf("fixture stopped before cancellation: %v", err)
		default:
		}
		cancel()
		synctest.Wait()
		if err := <-stopped; err != nil {
			t.Fatal("idle fixture did not shut down cleanly", err)
		}
	})
}

func TestStdioPeerHasIndependentVersionAndNoExecutionControls(t *testing.T) {
	archiveCalls := 0
	handler := stdioHandler(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		archiveCalls++
		w.WriteHeader(http.StatusOK)
	}))
	request := func(path, token string, code int) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequestWithContext(t.Context(), http.MethodGet, path, nil)
		r.Header.Set("Authorization", token)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != code {
			t.Fatal("fixture status", w.Code, code)
		}
		return w
	}
	manifest := request("/.well-known/ard.json", "", 200)
	var body struct {
		Entries []map[string]any `json:"entries"`
	}
	if json.Unmarshal(manifest.Body.Bytes(), &body) != nil || len(body.Entries) != 4 {
		t.Fatal("fixed manifest")
	}
	if _, present := body.Entries[0]["version"]; present || body.Entries[1]["version"] != "" || body.Entries[2]["version"] != "1.0.0" || body.Entries[3]["version"] != "2.0.0" {
		t.Fatal("version controls collapsed")
	}
	request("/never-invoke", "", 403)
	request("/.well-known/ard.json", "Bearer private", 403)
	request("/.well-known/ard.json?unselected=true", "", 404)
	request("/archive.zip", "", 200)
	w := request("/observations", "", 200)
	var counts map[string]int
	if json.Unmarshal(w.Body.Bytes(), &counts) != nil || counts["credentials"] != 1 || counts["unexpected"] != 2 || counts["manifests"] != 1 || counts["archives"] != 1 || archiveCalls != 1 {
		t.Fatal("fixture did not isolate metadata, archive and forbidden requests", counts)
	}
}

func TestPrepareStdioConfinesTLSAndUsesExistingSyntheticArchiveData(t *testing.T) {
	for _, ip := range []string{"127.0.0.1", "8.8.8.8", "::1", "invalid"} {
		if err := prepareStdio(t.TempDir(), ip); err == nil {
			t.Fatal("accepted unselected address", ip)
		}
	}
	directory := filepath.Join(t.TempDir(), "private")
	if err := os.Mkdir(directory, 0700); err != nil {
		t.Fatal(err)
	}
	if err := prepareStdio(directory, "172.31.0.10"); err != nil {
		t.Fatal(err)
	}
	files, err := demodata.ReadPrepared(directory)
	if err != nil || len(files) == 0 {
		t.Fatal("missing owned inert archives", err)
	}
	// Match the existing embedded expanded dataset, not a second copy of it.
	for _, resource := range demodata.DefaultPlan("expanded", stdioOrigin).Resources {
		if len(resource.Archive) != 0 {
			if string(files["/"+resource.Resource.Key+".zip"]) != string(resource.Archive) || resource.Resource.Kind != catalog.KindPlugin {
				t.Fatal("archive fixture differs from native seed data")
			}
		}
	}
}
