//go:build functional

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
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
	output := run()
	if !strings.Contains(output, "Complete (2 pages, 12 releases)") {
		t.Fatal("curated paging/count failed", output)
	}
	counts := map[string]int{}
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
		// Curated names contain no control characters. This independent encoder
		// checks each actual Markdown row, not merely UUID presence or row count.
		escape := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", "\\", "\\\\", "|", "\\|", "_", "\\_", "[", "\\[", "]", "\\]", "(", "\\(", ")", "\\)", "`", "\\`", "*", "\\*", "!", "\\!", "#", "\\#", "~", "\\~").Replace
		license := record.ListingRevision.License
		if license == "" {
			license = "not declared"
		}
		row := fmt.Sprintf("| %s | %s | %s | %s | %s | %s |", escape(record.ListingRevision.DisplayName), escape(record.Resource.Kind), escape(record.Version.Version), record.Release.ID, escape(record.Resource.Identifier), escape(license))
		if strings.Count(output, row) != 1 {
			t.Fatal("row disagrees with exact API readback", row)
		}
		counts[record.Resource.Kind]++
	}
	for _, kind := range []string{"agent", "mcp_server", "skill", "plugin"} {
		if counts[kind] != 3 {
			t.Fatal("four-kind inventory mismatch", kind, counts)
		}
	}
	query := run("--query", "testing")
	if !strings.Contains(query, "Web Application Testing") {
		t.Fatal("worked testing query did not match")
	}
	kind := run("--kind", "skill")
	if strings.Count(kind, "| skill |") != 3 || !strings.Contains(kind, "Complete (1 page, 3 releases)") {
		t.Fatal("kind filter failed", kind)
	}
	empty := run("--query", "no-matching-release-987654321")
	if !strings.Contains(empty, "No authorized matches.") {
		t.Fatal("empty query failed", empty)
	}
	if writes.Load() != before {
		t.Fatal("report mutated the Catalog")
	}
	t.Log("Rust report: twelve exact curated rows, three per kind, real two-page Search, testing/kind/empty queries and no authoring mutation; owned memory/API/TLS archive fixtures cleaned.")
}
