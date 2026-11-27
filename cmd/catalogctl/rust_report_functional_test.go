//go:build functional

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/demodata"
)

func TestRustReportCurated(t *testing.T) {
	binary := os.Getenv("CATALOG_RUST_REPORT")
	if binary == "" {
		t.Skip("selected Rust SDK gate supplies the installed example executable")
	}
	_, server, origin, trust, writes := configuredDemoRuntime(t)
	plan, err := demodata.Load("", "curated", origin)
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := executeConfiguredDemo(t.Context(), server.URL, filepath.Join(t.TempDir(), "receipt.json"), origin, trust, false, plan)
	if err != nil {
		t.Fatal(err)
	}
	if receipt.Published != 12 || len(receipt.Items) != 14 {
		t.Fatal("fresh curated inventory changed")
	}
	file := filepath.Join(t.TempDir(), "reader-token")
	if err = os.WriteFile(file, []byte("local-reader\n"), 0600); err != nil {
		t.Fatal(err)
	}
	before := writes.Load()
	run := func(args ...string) string {
		t.Helper()
		ctx, cancel := context.WithTimeout(t.Context(), 75*time.Second)
		defer cancel()
		command := exec.CommandContext(ctx, binary, append([]string{"--origin", server.URL, "--token-file", file, "--allow-loopback-http"}, args...)...) // #nosec G204,G702 -- the selected SDK gate supplies its owned compiled example; all destinations are invocation-owned fixtures.
		var output, message bytes.Buffer
		command.Stdout, command.Stderr = &output, &message
		if err := command.Run(); err != nil || message.Len() != 0 {
			t.Fatal("curated report failed", err, message.String())
		}
		return output.String()
	}
	rowsByKey := map[string]string{}
	rowsByKind := map[string][]string{}
	// The values come from independent exact reads of the releases in the seed receipt.
	escape := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", "\\", "\\\\", "|", "\\|", "_", "\\_", "[", "\\[", "]", "\\]", "(", "\\(", ")", "\\)", "`", "\\`", "*", "\\*", "!", "\\!", "#", "\\#", "~", "\\~").Replace
	for _, item := range receipt.Items {
		if item.State != "published" {
			continue
		}
		request, e := http.NewRequestWithContext(t.Context(), http.MethodGet, server.URL+"/api/v1/catalog/resources/"+item.ReleaseID, nil)
		if e != nil {
			t.Fatal(e)
		}
		request.Header.Set("Authorization", "Bearer local-reader")
		response, e := http.DefaultClient.Do(request)
		if e != nil {
			t.Fatal(e)
		}
		var record struct {
			ListingRevision struct {
				DisplayName string `json:"displayName"`
				License     string `json:"license"`
			} `json:"listingRevision"`
			Resource struct {
				Kind       string `json:"kind"`
				Identifier string `json:"canonicalIdentifier"`
			} `json:"resource"`
			Version struct {
				Version string `json:"version"`
			} `json:"version"`
			Release struct {
				ID string `json:"id"`
			} `json:"release"`
		}
		e = json.NewDecoder(response.Body).Decode(&record)
		_ = response.Body.Close()
		if e != nil || response.StatusCode != http.StatusOK {
			t.Fatal("exact record readback failed", e, response.StatusCode)
		}
		if record.Release.ID != item.ReleaseID {
			t.Fatalf("requested release %s, got %s", item.ReleaseID, record.Release.ID)
		}
		license := record.ListingRevision.License
		if license == "" {
			license = "not declared"
		}
		row := fmt.Sprintf("| %s | %s | %s | %s | %s | %s |", escape(record.ListingRevision.DisplayName), escape(record.Resource.Kind), escape(record.Version.Version), record.Release.ID, escape(record.Resource.Identifier), escape(license))
		rowsByKey[item.Key] = row
		rowsByKind[record.Resource.Kind] = append(rowsByKind[record.Resource.Kind], row)
	}
	for _, kind := range []string{"agent", "mcp_server", "skill", "plugin"} {
		if len(rowsByKind[kind]) != 3 {
			t.Fatalf("expected three published %s releases, got %d", kind, len(rowsByKind[kind]))
		}
	}
	if !strings.HasPrefix(rowsByKey["webapp-testing"], "| Web Application Testing | skill |") ||
		!strings.HasPrefix(rowsByKey["web-testing-toolkit"], "| Web Testing Toolkit | plugin |") {
		t.Fatal("curated records differ from the Skill and Plugin in the worked example")
	}
	checkRows := func(output, status string, want ...string) {
		t.Helper()
		_, table, ok := strings.Cut(output, "| --- | --- | --- | --- | --- | --- |\n")
		if !ok || !strings.Contains(output, "\nStatus: Complete ("+status+").\n") {
			t.Fatalf("expected a complete report for %s, got:\n%s", status, output)
		}
		got := strings.Split(strings.TrimSuffix(table, "\n"), "\n")
		slices.Sort(got)
		slices.Sort(want)
		if !slices.Equal(got, want) {
			t.Fatalf("report rows differ from exact Catalog records:\ngot %q\nwant %q", got, want)
		}
	}
	checkRows(run(), "2 pages, 12 releases", slices.Collect(maps.Values(rowsByKey))...)
	checkRows(run("--query", "testing"), "1 page, 2 releases", rowsByKey["webapp-testing"], rowsByKey["web-testing-toolkit"])
	checkRows(run("--kind", "skill"), "1 page, 3 releases", rowsByKind["skill"]...)
	checkRows(run("--query", "testing", "--kind", "skill"), "1 page, 1 release", rowsByKey["webapp-testing"])
	empty := run("--query", "no-matching-release-987654321")
	if !strings.Contains(empty, "\nStatus: Complete (1 page, 0 releases).\n") || !strings.Contains(empty, "\nNo authorized matches.\n") || strings.Contains(empty, "\n|") {
		t.Fatal("empty query failed", empty)
	}
	if writes.Load() != before {
		t.Fatal("report mutated the Catalog")
	}
	t.Log("Rust report verified 12 exact curated rows, the tutorial's combined query and kind, an empty result and zero authoring writes.")
}
