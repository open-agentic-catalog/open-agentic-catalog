// Unit tests exercise isolated validation and controlled dependencies.
package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
)

func writeProfile(t *testing.T, raw []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "profile.json")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestPerformanceCLIRejectsInvalidInput(t *testing.T) {
	for name, raw := range map[string]string{
		"empty": "", "oversized": strings.Repeat(" ", 4097),
		"duplicate": `{"port":1,"port":2}`, "trailing": `{} {}`,
		"unknown": `{"unknown":1}`, "wrong-type": `{"port":"8080"}`,
		"null": `null`, "invalid-bounds": `{"port":0}`,
	} {
		t.Run(name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			if code := execute([]string{writeProfile(t, []byte(raw))}, &stdout, &stderr); code != 2 || stdout.Len() != 0 || !strings.HasPrefix(stderr.String(), "invalid-profile") {
				t.Fatalf("code=%d stdout=%q stderr=%q", code, &stdout, &stderr)
			}
		})
	}
	for _, args := range [][]string{nil, {"first", "second"}, {filepath.Join(t.TempDir(), "missing")}, {t.TempDir()}} {
		var stdout, stderr bytes.Buffer
		if code := execute(args, &stdout, &stderr); code != 2 || stdout.Len() != 0 || stderr.Len() == 0 {
			t.Fatalf("invalid CLI invocation code=%d stdout=%q stderr=%q", code, &stdout, &stderr)
		}
		for _, arg := range args {
			if strings.Contains(stderr.String(), arg) {
				t.Fatal("CLI diagnostic disclosed an input path")
			}
		}
	}
}

func TestPerformanceBaselineRejectsIncompleteAndAmbiguousResults(t *testing.T) {
	unknown := append([]byte(`{"unknown":true,`), validPage(t)[1:]...)
	if _, err := validateBaseline(unknown); err == nil {
		t.Fatal("unknown response field accepted")
	}
	for name, mutate := range map[string]func(*catalog.CatalogSearchResponse){
		"duplicate-id":  func(page *catalog.CatalogSearchResponse) { page.Items[1].ID = page.Items[0].ID },
		"missing-local": func(page *catalog.CatalogSearchResponse) { page.Items[0].Local = nil },
		"empty-id":      func(page *catalog.CatalogSearchResponse) { page.Items[0].ID = "" },
		"cursor":        func(page *catalog.CatalogSearchResponse) { page.NextCursor = "cursor" },
		"live":          func(page *catalog.CatalogSearchResponse) { page.LiveCount = 1 },
		"total":         func(page *catalog.CatalogSearchResponse) { page.DurableTotal = 4 },
		"generation":    func(page *catalog.CatalogSearchResponse) { page.Generation = "" },
		"profile":       func(page *catalog.CatalogSearchResponse) { page.Profile = "" },
	} {
		t.Run(name, func(t *testing.T) {
			var page catalog.CatalogSearchResponse
			if err := json.Unmarshal(validPage(t), &page); err != nil {
				t.Fatal(err)
			}
			mutate(&page)
			raw, err := json.Marshal(page)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := validateBaseline(raw); err == nil {
				t.Fatal("invalid seed accepted")
			}
		})
	}
}

type cancelTransport struct{ cancel context.CancelFunc }

func (transport cancelTransport) RoundTrip(*http.Request) (*http.Response, error) {
	transport.cancel()
	return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(`{}`))}, nil
}

func TestPerformanceCancellationDuringWorkAndBaselineFailure(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	client := &http.Client{Transport: cancelTransport{cancel: cancel}}
	result := phase(ctx, client, profile{Port: 1, Workers: 1, MaxRequests: 2}, 60, sha256.Sum256([]byte(`{}`)))
	if result.Count != 1 || result.Succeeded != 1 {
		t.Fatalf("cancellation did not stop subsequent work: %+v", result)
	}
	if _, err := run(ctx, profile{Port: 1, Seconds: 1, Workers: 1, MaxRequests: 1}); err == nil {
		t.Fatal("canceled baseline passed")
	}
}
