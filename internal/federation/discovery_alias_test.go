package federation

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/clock"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/fetcher"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/id"
)

// ARD §5.1 makes the predecessor names equivalent entry sources. A generic
// response at either alias must not trigger predecessor trust-manifest rules.
func TestInspectionPredecessorAliasesDoNotSelectLegacyPayload(t *testing.T) {
	const origin = "https://remote.example"
	const path = origin + "/.well-known/ai-catalog.json"
	const manifest = `{"specVersion":"1.0","host":{"displayName":"Native publisher","trustManifest":{"identity":"https://remote.example","identityType":"https"}},"entries":[{"identifier":"urn:air:remote.example:server:one","displayName":"One","type":"application/mcp-server-card+json","url":"https://remote.example/one"}]}`
	for _, mode := range []string{"direct", "fallback", "http-link", "html-link", "http-link-dual", "html-link-dual", "http-link-legacy", "html-link-legacy", "explicit-legacy"} {
		t.Run(mode, func(t *testing.T) {
			response := federationResultAt(path, manifest)
			response.MediaType = "application/json"
			endpoint := path
			results := map[string]fetcher.Result{"GET " + path: response}
			if mode != "direct" && mode != "explicit-legacy" {
				endpoint = origin + "/catalog"
				page := fetcher.Result{Status: http.StatusNotFound, FinalURL: endpoint, MediaType: "text/html"}
				if strings.HasPrefix(mode, "http-link") {
					page.Status = 200
					page.Links = []string{`</.well-known/ai-catalog.json>; rel="ai-catalog"`}
					if mode == "http-link-dual" {
						page.Links = []string{`</.well-known/ai-catalog.json>; rel="ard ai-catalog"`}
					} else if mode == "http-link-legacy" {
						page.Links = []string{`</.well-known/ai-catalog.json>; rel="ai-catalog"; type="application/ai-catalog+json"`}
					}
				}
				if strings.HasPrefix(mode, "html-link") {
					page.Status = 200
					page.Body = []byte(`<link rel="ai-catalog" href="/.well-known/ai-catalog.json">`)
					if mode == "html-link-dual" {
						page.Body = []byte(`<link rel="ard ai-catalog" href="/.well-known/ai-catalog.json">`)
					} else if mode == "html-link-legacy" {
						page.Body = []byte(`<link rel="ai-catalog" href="/.well-known/ai-catalog.json" type="application/ai-catalog+json">`)
					}
				}
				results["GET "+endpoint] = page
				results["GET "+origin+"/.well-known/ard.json"] = fetcher.Result{Status: 404, FinalURL: origin + "/.well-known/ard.json"}
			}
			if mode == "explicit-legacy" {
				response.MediaType = aiCatalogDocumentMediaType
				results["GET "+path] = response
			}
			fetches := &routingFetcher{results: results}
			manager, err := NewManager(NewMemoryStore(), fetches, clock.NewFake(time.Date(2026, 10, 9, 0, 0, 0, 0, time.UTC)), id.Generator{})
			if err != nil {
				t.Fatal(err)
			}
			inspection, err := manager.Inspect(auditEvidenceContext(), endpoint)
			if strings.HasSuffix(mode, "legacy") {
				if catalog.ErrorCodeOf(err) != catalog.CodeInvalidArgument || (mode == "explicit-legacy" && !strings.Contains(err.Error(), "must contain substantive trust evidence")) {
					t.Fatalf("explicit legacy declaration did not enforce trust validation: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if inspection.ResolvedURL != path || inspection.SourceType != SourceARDManifest || inspection.ProfileID != ardProfileID || inspection.EntryCount != 1 {
				t.Fatalf("alias inspection: %#v", inspection)
			}
		})
	}
}

func TestPredecessorAliasPreservesRetainedLegacyAndExplicitMedia(t *testing.T) {
	for _, tc := range []struct{ reported, retained, want string }{
		{"application/json", "", ardDocumentMediaType},
		{"application/json", "ai-catalog-1.0-b062278", aiCatalogDocumentMediaType},
		{aiCatalogDocumentMediaType, "", aiCatalogDocumentMediaType},
		{ardDocumentMediaType, "ai-catalog-1.0-b062278", ardDocumentMediaType},
	} {
		got, err := effectiveDiscoveryMediaType(tc.reported, discoveryURLMediaType("https://remote.example/.well-known/ai-catalog.json"), tc.retained)
		if err != nil || got != tc.want {
			t.Fatalf("%#v: %q, %v", tc, got, err)
		}
	}
}
