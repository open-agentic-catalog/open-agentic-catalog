//go:build functional

package integration

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/adapter"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
)

// Exercise evidence and review through the real adapter and in-memory service.
// Parser permutations and archive-format safety stay in the adapter tests.
func TestSkillFrontmatterBlocksReviewAndPreservesEvidence(t *testing.T) {
	for _, packaged := range []bool{false, true} {
		t.Run(fmt.Sprintf("packaged=%t", packaged), func(t *testing.T) {
			runtime := buildRuntime(t)
			defer runtime.Close()
			server := httptest.NewServer(runtime.HTTP.Handler())
			defer server.Close()
			post := func(path, key string, body any) response {
				return request(t, server.URL, http.MethodPost, path, "admin", key, "", body)
			}
			created := post("/api/v1/namespaces", "namespace", map[string]any{"slug": "skills", "displayName": "Skills"})
			requireStatus(t, created, http.StatusCreated)
			var namespace catalog.Namespace
			decode(t, created, &namespace)
			created = post("/api/v1/resources", "resource", map[string]any{"namespaceId": namespace.ID, "canonicalIdentifier": "urn:air:catalog.example.com:skills:example", "kind": "skill"})
			requireStatus(t, created, http.StatusCreated)
			var resource catalog.Resource
			decode(t, created, &resource)
			content := "---\nname: example\ndescription: Example\ncompatibility: \"\"\n---\n# Instructions\n"
			retained := []byte(content)
			representation := map[string]any{"role": "primary", "mediaType": adapter.MediaSkillMarkdownARD, "content": content}
			findingPath := "/representations/0/frontmatter/compatibility"
			if packaged {
				var buffer bytes.Buffer
				writer := zip.NewWriter(&buffer)
				file, err := writer.Create("example/SKILL.md")
				if err != nil {
					t.Fatal(err)
				}
				if _, err := file.Write(retained); err != nil {
					t.Fatal(err)
				}
				if err := writer.Close(); err != nil {
					t.Fatal(err)
				}
				retained = buffer.Bytes()
				representation = map[string]any{"role": "primary", "mediaType": adapter.MediaSkillZIP, "packageData": retained, "handoff": map[string]any{"git": map[string]any{"repository": "https://git.example.com/skills.git", "commit": strings.Repeat("a", 40), "directory": "example"}}}
				findingPath = "/representations/0/package/SKILL.md/frontmatter/compatibility"
			}
			created = post("/api/v1/resources/"+resource.ID+"/versions", "version", map[string]any{"version": "1.0.0", "representations": []any{representation}})
			requireStatus(t, created, http.StatusCreated)
			type versionEnvelope struct {
				Revision catalog.VersionRevision `json:"revision"`
				Evidence catalog.EvidenceSet     `json:"evidence"`
			}
			var original versionEnvelope
			decode(t, created, &original)
			assertBlocked := func(evidence catalog.EvidenceSet) {
				t.Helper()
				if evidence.State != catalog.EvidenceBlocked {
					t.Fatalf("invalid Skill evidence: %#v", evidence)
				}
				for _, finding := range evidence.Findings {
					if finding.Code == "SKILL_COMPATIBILITY_INVALID" && finding.Severity == catalog.SeverityError && finding.Path == findingPath {
						return
					}
				}
				t.Fatalf("missing precise frontmatter finding: %#v", evidence.Findings)
			}
			assertBlocked(original.Evidence)
			rep := original.Revision.Representations[0]
			if rep.ContentDigest != fmt.Sprintf("sha256:%x", sha256.Sum256(retained)) || rep.Size != int64(len(retained)) {
				t.Fatal("validation rewrote source bytes")
			}
			if !packaged {
				document := request(t, server.URL, http.MethodGet, "/api/v1/artifacts/"+rep.ArtifactID+"/content", "admin", "", "", nil)
				requireStatus(t, document, http.StatusOK)
				if !bytes.Equal(document.body, retained) {
					t.Fatal("retained Markdown changed")
				}
			} else if rep.Package == nil || rep.Package.StructureState != catalog.PackageAssessmentFailed || len(rep.Package.Files) != 0 {
				t.Fatal("invalid package retained successful inventory")
			}
			renewedResponse := post("/api/v1/version-revisions/"+original.Revision.ID+":validate", "validate", map[string]any{})
			requireStatus(t, renewedResponse, http.StatusCreated)
			// Decode independently so slice reuse cannot mutate the baseline.
			var renewed versionEnvelope
			decode(t, renewedResponse, &renewed)
			assertBlocked(renewed.Evidence)
			if renewed.Evidence.ID == original.Evidence.ID || renewed.Revision.ContentSetDigest != original.Revision.ContentSetDigest || !reflect.DeepEqual(renewed.Revision.Representations, original.Revision.Representations) {
				t.Fatal("revalidation changed immutable content or reused evidence")
			}
			created = post("/api/v1/publishers", "publisher", map[string]any{"displayName": "Example", "domains": []string{"catalog.example.com"}})
			requireStatus(t, created, http.StatusCreated)
			var publisher catalog.Publisher
			decode(t, created, &publisher)
			created = post("/api/v1/catalog-listings", "listing", map[string]any{"namespaceId": namespace.ID, "resourceId": resource.ID})
			requireStatus(t, created, http.StatusCreated)
			var listing catalog.CatalogListing
			decode(t, created, &listing)
			revision := request(t, server.URL, http.MethodPost, "/api/v1/catalog-listings/"+listing.ID+"/revisions", "admin", "listing-revision", created.header.Get("ETag"), map[string]any{"versionRevisionId": original.Revision.ID, "displayName": "Example", "description": "Example Skill", "publisherId": publisher.ID, "visibility": "tenant", "license": "Apache-2.0"})
			requireStatus(t, revision, http.StatusCreated)
			submitted := post("/api/v1/catalog-listings/"+listing.ID+"/submissions", "submit", map[string]any{})
			requireStatus(t, submitted, http.StatusUnprocessableEntity)
			var problem struct {
				Code string `json:"code"`
			}
			decode(t, submitted, &problem)
			if problem.Code != string(catalog.CodeValidationFailed) {
				t.Fatalf("unexpected review failure: %s", submitted.body)
			}
		})
	}
}
