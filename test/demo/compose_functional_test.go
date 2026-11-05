//go:build functional

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/demodata"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/id"
)

// This explicit selection mutates only an operator-selected disposable loopback API.
func TestOwnedComposeDemoArchives(t *testing.T) {
	if os.Getenv("DEMO_ARCHIVE_TEST_OWNED") != "1" {
		t.Skip("requires explicit owned disposable Compose selection")
	}
	base, origin := os.Getenv("DEMO_ARCHIVE_TEST_API"), os.Getenv("DEMO_ARCHIVE_TEST_ORIGIN")
	api, err := url.Parse(base)
	if err != nil || api.Scheme != "http" || api.Hostname() != "127.0.0.1" || api.User != nil || api.Path != "" || api.RawQuery != "" || api.ForceQuery || api.Fragment != "" {
		t.Fatal("select one literal loopback API origin")
	}
	fixture, err := url.Parse(origin)
	if err != nil || fixture.Scheme != "https" || fixture.User != nil || fixture.Path != "" || fixture.RawQuery != "" || fixture.ForceQuery || fixture.Fragment != "" {
		t.Fatal("invalid owned archive origin")
	}
	ip, err := netip.ParseAddr(fixture.Hostname())
	if err != nil || !ip.IsPrivate() {
		t.Fatal("select one private Compose fixture origin")
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	client := &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	t.Cleanup(client.CloseIdleConnections)
	runID, err := (id.Generator{}).New(time.Now())
	if err != nil {
		t.Fatal(err)
	}
	exchange := func(method, path, token string, body any, status int) []byte {
		t.Helper()
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		var payload io.Reader
		if body != nil {
			payload = bytes.NewReader(raw)
		}
		request, err := http.NewRequestWithContext(ctx, method, base+path, payload) // #nosec G704 -- explicitly selected disposable observer; literal loopback origin validated above.
		if err != nil {
			t.Fatal(err)
		}
		request.Header.Set("Authorization", "Bearer "+token)
		request.Header.Set("Content-Type", "application/json")
		if method == http.MethodPost {
			key, err := (id.Generator{}).New(time.Now())
			if err != nil {
				t.Fatal(err)
			}
			request.Header.Set("Idempotency-Key", key)
		}
		response, err := client.Do(request) // #nosec G704 -- validated literal loopback API only; redirects cannot forward the synthetic token.
		if err != nil {
			t.Fatal("owned API exchange failed")
		}
		defer func() { _ = response.Body.Close() }()
		raw, err = io.ReadAll(io.LimitReader(response.Body, (1<<20)+1))
		if err != nil || len(raw) > 1<<20 {
			t.Fatal("invalid bounded API response")
		}
		if response.StatusCode != status {
			t.Fatalf("%s %s: HTTP %d, expected %d", method, path, response.StatusCode, status)
		}
		return raw
	}
	var namespace catalog.Namespace
	if err := json.Unmarshal(exchange(http.MethodPost, "/api/v1/namespaces", "local-admin", map[string]any{"slug": "archive-" + runID, "displayName": "Owned archive qualification"}, 201), &namespace); err != nil {
		t.Fatal(err)
	}
	for _, sample := range demodata.Samples() {
		if sample.Kind != catalog.KindPlugin {
			continue
		}
		var resource catalog.Resource
		if err := json.Unmarshal(exchange(http.MethodPost, "/api/v1/resources", "local-contributor", map[string]any{"namespaceId": namespace.ID, "canonicalIdentifier": "urn:air:demo.catalog.example.com:qualification:" + sample.Key + "-" + runID, "kind": sample.Kind}, 201), &resource); err != nil {
			t.Fatal(err)
		}
		path := "/api/v1/resources/" + resource.ID + "/versions"
		representation := demodata.Representation(sample, origin)
		var envelope struct {
			Revision catalog.VersionRevision `json:"revision"`
			Evidence catalog.EvidenceSet     `json:"evidence"`
		}
		raw := exchange(http.MethodPost, path, "local-contributor", catalog.CreateVersionInput{Version: "1.0.0", Representations: []catalog.RepresentationInput{representation}}, 201)
		if err := json.Unmarshal(raw, &envelope); err != nil {
			t.Fatal(err)
		}
		_, digest, _ := demodata.Archive("/" + sample.Key + ".zip")
		if envelope.Evidence.State != catalog.EvidenceComplete || len(envelope.Revision.Representations) != 1 || envelope.Revision.Representations[0].Plugin == nil || envelope.Revision.Representations[0].ContentDigest != digest {
			t.Fatal("real PostgreSQL Plugin inspection mismatch")
		}
		wrong := "sha256:" + strings.Repeat("0", 64)
		representation.ExpectedDigest = &wrong
		exchange(http.MethodPost, path, "local-contributor", catalog.CreateVersionInput{Version: "2.0.0", Representations: []catalog.RepresentationInput{representation}}, 422)
		var page struct {
			Items      []catalog.ResourceVersion `json:"items"`
			NextCursor string                    `json:"nextCursor"`
		}
		if err := json.Unmarshal(exchange(http.MethodGet, path+"?limit=100", "local-admin", nil, 200), &page); err != nil {
			t.Fatal(err)
		}
		if len(page.Items) != 1 || page.Items[0].ID != envelope.Revision.ResourceVersionID || page.NextCursor != "" {
			t.Fatal("checksum failure persisted a partial version")
		}
		fmt.Printf("Qualified owned Compose Plugin %s with exact digest and rejected drift.\n", sample.Key)
	}
}
