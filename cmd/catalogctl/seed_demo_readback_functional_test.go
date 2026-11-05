//go:build functional

package main

import (
	"net/url"
	"reflect"
	"slices"
	"testing"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/demodata"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/publication"
)

// Verify the expanded dataset through reader surfaces, independent of authoring responses.
func verifyDemoReaderSurfaces(t *testing.T, c *smokeHTTPClient, r demoReceipt, registryOrigin string) {
	t.Helper()
	expected := make(map[string]demoItem)
	for _, item := range r.Items {
		if item.State == "published" {
			expected[item.ReleaseID] = item
		}
	}
	counts := make(map[string]int)
	seen := make(map[string]bool)
	cursor := ""
	for pageNumber := 0; pageNumber < 7; pageNumber++ {
		page, err := demoGet[catalog.Page[catalog.CatalogSummary]](t.Context(), c, "/api/v1/catalog/resources?limit=2&cursor="+url.QueryEscape(cursor))
		if err != nil {
			t.Fatal(err)
		}
		if len(page.Items) > 2 || page.Total != 12 {
			t.Fatalf("invalid bounded Browse: count %d total %d", len(page.Items), page.Total)
		}
		for _, row := range page.Items {
			item, ok := expected[row.Release.ID]
			if !ok || seen[row.Release.ID] || row.Resource.ID != item.ResourceID || row.Resource.CanonicalIdentifier != item.Identifier {
				t.Fatal("unexpected/duplicate Browse identity")
			}
			namespace := r.NamespaceID
			if item.Kind == catalog.KindPlugin {
				namespace = r.PluginNamespaceID
			}
			if row.Resource.NamespaceID != namespace || row.Resource.Kind != item.Kind {
				t.Fatal("Browse ownership/kind mismatch")
			}
			counts[namespace]++
			seen[row.Release.ID] = true
			detail, err := demoGet[catalog.CatalogRecord](t.Context(), c, "/api/v1/catalog/resources/"+item.ReleaseID)
			if err != nil || detail.Resource.ID != item.ResourceID || detail.Release.ID != item.ReleaseID || detail.VersionRevision.ID != item.VersionRevisionID || detail.ListingRevision.ID != item.ListingRevisionID {
				t.Fatalf("published detail mismatch: %v", err)
			}
			if item.Kind == catalog.KindPlugin {
				verifyDemoPluginDetail(t, detail, item)
			}
		}
		if page.NextCursor == "" {
			break
		}
		if page.NextCursor == cursor || len(page.Items) != 2 {
			t.Fatal("Browse continuation did not advance")
		}
		cursor = page.NextCursor
	}
	if len(seen) != 12 || counts[r.NamespaceID] != 9 || counts[r.PluginNamespaceID] != 3 {
		t.Fatalf("published namespace allocation: %v", counts)
	}
	resources, err := demoGet[catalog.Page[catalog.Resource]](t.Context(), c, "/api/v1/resources?limit=20")
	if err != nil || len(resources.Items) != 14 || resources.NextCursor != "" {
		t.Fatalf("retained Resources: %v", err)
	}
	retained := make(map[string]int)
	for _, resource := range resources.Items {
		retained[resource.NamespaceID]++
	}
	if retained[r.NamespaceID] != 11 || retained[r.PluginNamespaceID] != 3 {
		t.Fatalf("retained namespace allocation: %v", retained)
	}
	for _, kind := range []catalog.Kind{catalog.KindAgent, catalog.KindMCPServer, catalog.KindSkill, catalog.KindPlugin} {
		page, err := demoGet[catalog.Page[catalog.CatalogSummary]](t.Context(), c, "/api/v1/catalog/resources?kind="+string(kind)+"&limit=20")
		if err != nil || len(page.Items) != 3 || page.NextCursor != "" {
			t.Fatalf("Browse kind %s: %v", kind, err)
		}
		for _, row := range page.Items {
			if row.Resource.Kind != kind {
				t.Fatal("kind filter ignored")
			}
		}
	}
	for tag, count := range map[string]int{demoID: 12, "catalog-demo": 6, "ide-plugins": 3, "orders": 3} {
		page, err := demoExchange[catalog.CatalogSearchResponse](t.Context(), c, "POST", "/api/v1/catalog/search", "local-reader", "", "", map[string]any{"tags": []string{tag}, "limit": 20}, 200)
		if err != nil || len(page.Items) != count || page.NextCursor != "" || page.Partial {
			t.Fatalf("Search tag %s count=%d: %v", tag, len(page.Items), err)
		}
		for _, row := range page.Items {
			if row.Local == nil || expected[row.Local.Release.ID].ReleaseID == "" {
				t.Fatal("Search returned unpublished/non-demo identity")
			}
		}
	}
	seen = make(map[string]bool)
	cursor = ""
	for pageNumber := 0; pageNumber < 7; pageNumber++ {
		page, err := demoExchange[catalog.CatalogSearchResponse](t.Context(), c, "POST", "/api/v1/catalog/search", "local-reader", "", "", map[string]any{"tags": []string{demoID}, "limit": 2, "cursor": cursor}, 200)
		if err != nil || len(page.Items) > 2 || page.Partial {
			t.Fatalf("bounded Search: %v", err)
		}
		for _, row := range page.Items {
			if row.Local == nil || seen[row.Local.Release.ID] || expected[row.Local.Release.ID].ReleaseID == "" {
				t.Fatal("invalid Search continuation identity")
			}
			seen[row.Local.Release.ID] = true
		}
		if page.NextCursor == "" {
			break
		}
		if page.NextCursor == cursor {
			t.Fatal("Search cursor did not advance")
		}
		cursor = page.NextCursor
	}
	if len(seen) != 12 {
		t.Fatalf("Search continuation got %d", len(seen))
	}
	ard, err := demoExchange[publication.ARDManifest](t.Context(), c, "GET", "/.well-known/ard.json", "", "", "", nil, 200)
	if err != nil {
		t.Fatal(err)
	}
	ai, err := demoExchange[publication.AICatalog](t.Context(), c, "GET", "/.well-known/ai-catalog.json", "", "", "", nil, 200)
	if err != nil {
		t.Fatal(err)
	}
	if len(ard.Entries) != 13 || len(ai.Entries) != 12 {
		t.Fatal("ARD requires twelve resources plus self-Registry; AI Catalog requires twelve resources")
	}
	registry := 0
	for _, entry := range ard.Entries {
		if entry.Type == "application/ai-registry+json" {
			registry++
			if entry.URL != registryOrigin+"/ard/v1/" {
				t.Fatal("Registry origin mismatch")
			}
		}
		for _, item := range r.Items {
			if item.Kind != catalog.KindPlugin || item.Identifier != entry.Identifier {
				continue
			}
			if entry.Type != catalog.PluginMediaZIP || entry.URL != r.ArchiveOrigin+"/"+item.Key+".zip" || len(entry.Data) != 0 {
				t.Fatal("Plugin ARD archive projection mismatch")
			}
			i := slices.IndexFunc(ai.Entries, func(e publication.AICatalogEntry) bool { return e.Identifier == item.Identifier })
			if i < 0 || ai.Entries[i].Type != catalog.PluginMediaZIP || ai.Entries[i].URL != entry.URL {
				t.Fatal("AI Catalog Plugin archive projection mismatch")
			}
		}
	}
	if registry != 1 {
		t.Fatal("exactly one self-Registry required")
	}
}

func verifyDemoPluginDetail(t *testing.T, detail catalog.CatalogRecord, item demoItem) {
	t.Helper()
	if len(detail.VersionRevision.Representations) != 1 {
		t.Fatal("one Plugin representation required")
	}
	rep := detail.VersionRevision.Representations[0]
	_, digest, ok := demodata.Archive("/" + item.Key + ".zip")
	if !ok || rep.ContentDigest != digest || rep.MediaType != catalog.PluginMediaZIP || rep.ProfileID != catalog.PluginProfile || rep.Plugin == nil {
		t.Fatal("retained Plugin digest/profile/inspection mismatch")
	}
	expected := map[string][]string{"repository-essentials": {"skill:skills/review-repository/SKILL.md"}, "documentation-toolkit": {"mcp_server:mcp.json"}, "release-workflow": {"mcp_server:mcp.json", "skill:skills/prepare-release/SKILL.md"}}
	actual := make([]string, 0, len(rep.Plugin.Components))
	for _, component := range rep.Plugin.Components {
		actual = append(actual, component.Kind+":"+component.Path)
	}
	slices.Sort(actual)
	if !reflect.DeepEqual(actual, expected[item.Key]) {
		t.Fatalf("Plugin components %s: %v", item.Key, actual)
	}
	for _, file := range rep.Plugin.Files {
		if file.Executable {
			t.Fatal("demo Plugin contains executable file")
		}
	}
}
