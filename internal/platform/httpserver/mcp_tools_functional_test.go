//go:build functional

package httpserver

import (
	"bytes"
	"encoding/json"
	"net/http"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/adapter"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/publication"
)

// Reuse the ordinary author/reviewer/publication fixture and exercise actual HTTP decoding.
func testHTTPMCPTools(t *testing.T, base, namespace, publisher string, externalFetcher *httpCatalogExternalFetcher) {
	t.Helper()
	raw, err := os.ReadFile("../../../samples/cards/documentation-mcp.json")
	if err != nil {
		t.Fatal(err)
	}
	declaration := `{"tools":[{"name":"extract_pdf_tables","description":"Private tool sentinel"}]}`
	resourceReply := requestJSON(t, base, http.MethodPost, "/api/v1/resources", "contributor", "mcp-tools-resource", "", map[string]any{"namespaceId": namespace, "kind": "mcp_server", "canonicalIdentifier": "urn:air:catalog.example.com:default:document-utilities"})
	requireStatus(t, resourceReply, http.StatusCreated)
	var resource catalog.Resource
	decodeResult(t, resourceReply, &resource)
	representations := []map[string]any{{"role": "primary", "mediaType": adapter.MediaMCPServerCard, "data": json.RawMessage(raw)}, {"role": "metadata", "mediaType": catalog.MCPToolsMediaType, "profileId": catalog.MCPToolsProfile, "content": declaration}}
	versionReply := requestJSON(t, base, http.MethodPost, "/api/v1/resources/"+resource.ID+"/versions", "contributor", "mcp-tools-version", "", map[string]any{"version": "1.0.0", "representations": representations})
	requireStatus(t, versionReply, http.StatusCreated)
	var version struct {
		Version  catalog.ResourceVersion `json:"version"`
		Revision catalog.VersionRevision `json:"revision"`
		Evidence catalog.EvidenceSet     `json:"evidence"`
	}
	decodeResult(t, versionReply, &version)
	if version.Evidence.State != "complete" || version.Revision.Representations[1].ArtifactID == "" {
		t.Fatal("private declaration was not retained", version)
	}
	// This otherwise valid remote primary must not be fetched when any private
	// declaration is invalid. The same remote input succeeds in the control below.
	var remoteCard map[string]any
	if err := json.Unmarshal(raw, &remoteCard); err != nil {
		t.Fatal(err)
	}
	remoteCard["version"] = "1.0.1"
	remoteBytes, err := json.Marshal(remoteCard)
	if err != nil {
		t.Fatal(err)
	}
	previousBody := externalFetcher.body
	externalFetcher.body = remoteBytes
	defer func() { externalFetcher.body = previousBody }()
	fetchesBefore := externalFetcher.calls
	remotePrimary := map[string]any{"role": "primary", "mediaType": adapter.MediaMCPServerCard, "url": "https://content.example.com/mcp-card.json"}
	// Invalid metadata is rejected before retrieval or a partial Version/revision.
	for i, body := range []string{`{"tools":[{"name":"x"},{"name":"x"}]}`, `{"tools":[{"name":"x","inputSchema":{}}]}`} {
		invalid := []map[string]any{remotePrimary, {"role": "metadata", "mediaType": catalog.MCPToolsMediaType, "data": json.RawMessage(body)}}
		reply := requestJSON(t, base, http.MethodPost, "/api/v1/resources/"+resource.ID+"/versions", "contributor", "invalid-tools-"+strconv.Itoa(i), "", map[string]any{"version": "1.0.1", "representations": invalid})
		requireControlProblem(t, reply, http.StatusBadRequest, catalog.CodeInvalidArgument)
		revisionReply := requestJSON(t, base, http.MethodPost, "/api/v1/resource-versions/"+version.Version.ID+"/revisions", "contributor", "invalid-tool-revision-"+strconv.Itoa(i), versionReply.header.Get("ETag"), map[string]any{"representations": invalid})
		requireControlProblem(t, revisionReply, http.StatusBadRequest, catalog.CodeInvalidArgument)
		if externalFetcher.calls != fetchesBefore {
			t.Fatal("invalid private metadata triggered external retrieval")
		}
	}
	largeTools := make([]catalog.MCPTool, 12)
	for i := range largeTools {
		largeTools[i] = catalog.MCPTool{Name: strings.Repeat("x", i+1), Description: strings.Repeat("<", 1024)}
	}
	largeDeclaration, err := json.Marshal(catalog.MCPToolDeclaration{Tools: largeTools})
	if err != nil {
		t.Fatal(err)
	}
	largeDeclaration = bytes.ReplaceAll(largeDeclaration, []byte(`\u003c`), []byte("<"))
	canonicalOverflow := map[string]any{"version": "1.0.1", "representations": []map[string]any{remotePrimary, {"role": "metadata", "mediaType": catalog.MCPToolsMediaType, "data": catalog.MCPToolDeclaration{Tools: largeTools}}}}
	wire, err := json.Marshal(canonicalOverflow)
	if err != nil {
		t.Fatal(err)
	}
	// Send literal characters on the wire: the raw private declaration fits
	// 64 KiB, while its canonical inline retention would exceed that limit.
	wire = bytes.ReplaceAll(wire, []byte(`\u003c`), []byte("<"))
	overflow := requestWithHeaders(t, base, http.MethodPost, "/api/v1/resources/"+resource.ID+"/versions", wire, map[string]string{"Authorization": "Bearer contributor", "Content-Type": "application/json", "Idempotency-Key": "canonical-tools-overflow"})
	requireControlProblem(t, overflow, http.StatusBadRequest, catalog.CodeInvalidArgument)
	if externalFetcher.calls != fetchesBefore {
		t.Fatal("canonical tool overflow triggered external retrieval before refusal")
	}
	unchanged := requestJSON(t, base, http.MethodGet, "/api/v1/resources/"+resource.ID+"/versions", "contributor", "", "", nil)
	requireStatus(t, unchanged, http.StatusOK)
	var versions cursorPage[catalog.ResourceVersion]
	decodeResult(t, unchanged, &versions)
	if len(versions.Items) != 1 || versions.Items[0].ID != version.Version.ID || versions.Items[0].Revision != version.Version.Revision || versions.Items[0].CandidateRevisionID != version.Revision.ID {
		t.Fatal("invalid declaration retained a partial version or changed the candidate revision", versions)
	}
	listingReply := requestJSON(t, base, http.MethodPost, "/api/v1/catalog-listings", "contributor", "mcp-tools-listing", "", map[string]any{"namespaceId": namespace, "resourceId": resource.ID})
	requireStatus(t, listingReply, http.StatusCreated)
	var listing catalog.CatalogListing
	decodeResult(t, listingReply, &listing)
	revisionReply := requestJSON(t, base, http.MethodPost, "/api/v1/catalog-listings/"+listing.ID+"/revisions", "contributor", "mcp-tools-listing-revision", listingReply.header.Get("ETag"), map[string]any{"versionRevisionId": version.Revision.ID, "publisherId": publisher, "displayName": "Document Utilities", "description": "Processes documents", "visibility": "public", "representativeQueries": []string{"Find document processors", "Browse document utilities"}})
	requireStatus(t, revisionReply, http.StatusCreated)
	submissionReply := requestJSON(t, base, http.MethodPost, "/api/v1/catalog-listings/"+listing.ID+"/submissions", "contributor", "mcp-tools-submit", "", map[string]any{})
	requireStatus(t, submissionReply, http.StatusCreated)
	var sub catalog.ReviewSubmission
	decodeResult(t, submissionReply, &sub)
	requireStatus(t, requestJSON(t, base, http.MethodPost, "/api/v1/review-submissions/"+sub.ID+":approve", "reviewer", "mcp-tools-approve", submissionReply.header.Get("ETag"), map[string]any{"reason": "Review the exact private declaration"}), http.StatusOK)
	published := requestJSON(t, base, http.MethodPost, "/api/v1/catalog-listings/"+listing.ID+":publish", "publisher", "mcp-tools-publish", revisionReply.header.Get("ETag"), map[string]any{"submissionId": sub.ID})
	requireStatus(t, published, http.StatusCreated)
	var release struct {
		Release catalog.ListingRelease `json:"release"`
	}
	decodeResult(t, published, &release)
	query := map[string]any{"text": "pdf tables", "includeMcpTools": true, "origins": []string{"local"}}
	selected := requestJSON(t, base, http.MethodPost, "/api/v1/catalog/search", "contributor", "", "", query)
	requireStatus(t, selected, http.StatusOK)
	requireControlSchema(t, "CatalogSearchResponse", selected.body)
	var page catalog.CatalogSearchResponse
	decodeResult(t, selected, &page)
	if page.DurableTotal != 1 || len(page.Items) != 1 || page.Profile != catalog.MCPToolsSearchProfile || page.Tier != "core" || page.Items[0].ToolMatch == nil || page.Items[0].ToolMatch.RepresentationID != version.Revision.Representations[1].ID || page.Items[0].Local.Release.ID != release.Release.ID {
		t.Fatal("HTTP tool search lost exact evidence", page)
	}
	ordinary := requestJSON(t, base, http.MethodPost, "/api/v1/catalog/search", "contributor", "", "", map[string]any{"text": "pdf tables", "origins": []string{"local"}})
	requireStatus(t, ordinary, http.StatusOK)
	decodeResult(t, ordinary, &page)
	if page.DurableTotal != 0 {
		t.Fatal("implicit private matching")
	}
	for _, body := range []map[string]any{{"includeMcpTools": true, "text": "pdf"}, {"includeMcpTools": true, "text": "pdf", "origins": []string{"live"}}, {"includeMcpTools": true, "text": " ", "origins": []string{"local"}}, {"includeMcpTools": true, "text": "pdf", "origins": []string{"local"}, "kinds": []string{"agent"}}, {"includeMcpTools": nil}, {"includeMcpTools": "true"}} {
		requireControlProblem(t, requestJSON(t, base, http.MethodPost, "/api/v1/catalog/search", "contributor", "", "", body), http.StatusBadRequest, catalog.CodeInvalidArgument)
	}
	facets := requestJSON(t, base, http.MethodPost, "/api/v1/catalog/explore", "contributor", "", "", map[string]any{"query": map[string]any{"text": "pdf tables", "includeMcpTools": true}, "facets": []any{map[string]any{"field": "kind"}}})
	requireStatus(t, facets, http.StatusOK)
	requireControlSchema(t, "CatalogExploreResponse", facets.body)
	var exploration catalog.ExploreResponse
	decodeResult(t, facets, &exploration)
	if exploration.Matched != 1 || exploration.Profile != catalog.MCPToolsSearchProfile {
		t.Fatal(exploration)
	}
	// Public construction and both feeds carry only the primary upstream document.
	candidateReply := requestJSON(t, base, http.MethodPost, "/api/v1/publication/snapshots", "publisher", "mcp-tools-snapshot", "", map[string]any{"host": "127.0.0.1"})
	requireStatus(t, candidateReply, http.StatusCreated)
	var snapshot publication.Snapshot
	decodeResult(t, candidateReply, &snapshot)
	activated := requestJSON(t, base, http.MethodPost, "/api/v1/publication/snapshots/"+snapshot.ID+":activate", "publisher", "mcp-tools-activate", snapshot.ETag, map[string]any{"host": "127.0.0.1", "reason": "Qualify public/private separation"})
	requireStatus(t, activated, http.StatusOK)
	for _, path := range []string{"/.well-known/ard.json", "/.well-known/ai-catalog.json"} {
		feed := requestJSON(t, base, http.MethodGet, path, "", "", "", nil)
		requireStatus(t, feed, http.StatusOK)
		if bytes.Contains(feed.body, []byte("extract_pdf_tables")) || bytes.Contains(feed.body, []byte("Private tool sentinel")) || bytes.Contains(feed.body, []byte(catalog.MCPToolsProfile)) {
			t.Fatal("private tool declaration exported", path)
		}
	}
	artifact := requestJSON(t, base, http.MethodGet, "/public/v1/artifacts/"+version.Revision.Representations[1].ArtifactID, "", "", "", nil)
	requireStatus(t, artifact, http.StatusNotFound)
	exact := requestJSON(t, base, http.MethodGet, "/api/v1/version-revisions/"+version.Revision.ID, "contributor", "", "", nil)
	requireStatus(t, exact, http.StatusOK)
	if !bytes.Contains(exact.body, []byte(catalog.MCPToolsProfile)) {
		t.Fatal("exact private readback missing")
	}
	exported := requestJSON(t, base, http.MethodGet, "/api/v1/version-revisions/"+version.Revision.ID+"/export", "contributor", "", "", nil)
	requireStatus(t, exported, http.StatusOK)
	requireControlSchema(t, "CatalogDraftFile", exported.body)
	if !bytes.Contains(exported.body, []byte("extract_pdf_tables")) || !bytes.Contains(exported.body, []byte(catalog.MCPToolsProfile)) {
		t.Fatal("reusable draft omitted private declaration")
	}
	downloaded := requestJSON(t, base, http.MethodGet, "/api/v1/artifacts/"+version.Revision.Representations[1].ArtifactID+"/content", "contributor", "", "", nil)
	requireStatus(t, downloaded, http.StatusOK)
	if !bytes.Equal(downloaded.body, []byte(declaration)) {
		t.Fatal("private declaration bytes changed")
	}
	// The same declaration is valid as exact text content, whose retained bytes
	// do not expand. Preserve that delivery form rather than over-rejecting it.
	remoteControl := requestJSON(t, base, http.MethodPost, "/api/v1/resources/"+resource.ID+"/versions", "contributor", "valid-remote-mcp-tools", "", map[string]any{"version": "1.0.1", "representations": []map[string]any{remotePrimary, {"role": "metadata", "mediaType": catalog.MCPToolsMediaType, "content": string(largeDeclaration)}}})
	requireStatus(t, remoteControl, http.StatusCreated)
	var remoteVersion struct {
		Revision catalog.VersionRevision `json:"revision"`
		Evidence catalog.EvidenceSet     `json:"evidence"`
	}
	decodeResult(t, remoteControl, &remoteVersion)
	if remoteVersion.Evidence.State != catalog.EvidenceComplete || externalFetcher.calls != fetchesBefore+1 || len(remoteVersion.Revision.Representations) != 2 {
		t.Fatal("valid remote-primary control did not reach the retrieval boundary", remoteVersion, externalFetcher.calls)
	}
	retained := requestJSON(t, base, http.MethodGet, "/api/v1/artifacts/"+remoteVersion.Revision.Representations[1].ArtifactID+"/content", "contributor", "", "", nil)
	requireStatus(t, retained, http.StatusOK)
	if !bytes.Equal(retained.body, largeDeclaration) {
		t.Fatal("valid text declaration was not retained byte for byte")
	}
}
