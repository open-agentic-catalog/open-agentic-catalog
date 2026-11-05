//go:build functional

package httpserver

import (
	"bytes"
	"encoding/json"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/adapter"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
)

// Reuse the complete authoring fixture and its positive publication control.
func testHTTPDiagnosticAmplification(t *testing.T, baseURL, namespaceID, publisherID string) {
	t.Helper()
	created := requestJSON(t, baseURL, http.MethodPost, "/api/v1/resources", "contributor", "diagnostic-resource", "", map[string]any{"namespaceId": namespaceID, "canonicalIdentifier": "urn:air:catalog.example.com:default:diagnostic-agent", "kind": "agent"})
	requireStatus(t, created, http.StatusCreated)
	var resource catalog.Resource
	decodeResult(t, created, &resource)
	card := json.RawMessage(`{"name":"Agent","description":"Fixture","version":"1.0.0","supportedInterfaces":[{"url":"https://agents.example.com/a2a","protocolBinding":"JSONRPC","protocolVersion":"1.0"}],"capabilities":{},"defaultInputModes":["application/json"],"defaultOutputModes":["application/json"],"skills":[` + strings.Repeat(`{},`, 49999) + `{}]}`)
	input := catalog.CreateVersionInput{Version: "1.0.0", Representations: []catalog.RepresentationInput{{Role: catalog.RepresentationPrimary, MediaType: adapter.MediaA2AAgentCard, Data: card}}}
	raw, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) > 160000 {
		t.Fatalf("amplification fixture grew: %d", len(raw))
	}
	route := "/api/v1/resources/" + resource.ID + "/versions"
	result := requestJSON(t, baseURL, http.MethodPost, route, "contributor", "diagnostic-version", "", input)
	requireStatus(t, result, http.StatusCreated)
	requireControlSchema(t, "VersionEvidenceEnvelope", result.body)
	if len(result.body) > 512<<10 {
		t.Fatalf("diagnostics amplified response to %d bytes", len(result.body))
	}
	var version struct {
		Version  catalog.ResourceVersion `json:"version"`
		Revision catalog.VersionRevision `json:"revision"`
		Evidence catalog.EvidenceSet     `json:"evidence"`
	}
	decodeResult(t, result, &version)
	if err := catalog.ValidateEvidenceSetIntegrity(version.Evidence); err != nil {
		t.Fatal(err)
	}
	if version.Evidence.State != catalog.EvidenceBlocked || version.Revision.Representations[0].ValidationState != "invalid" {
		t.Fatal("incomplete validation accepted")
	}
	for _, findings := range [][]catalog.Finding{version.Revision.Findings, version.Evidence.Findings, version.Revision.Representations[0].Findings} {
		encoded, err := json.Marshal(findings)
		if err != nil || len(encoded) > 65536 || len(findings) == 0 || len(findings) > 128 || findings[len(findings)-1].Code != catalog.DiagnosticBudgetExhausted || findings[len(findings)-1].Severity != catalog.SeverityError {
			t.Fatalf("unbounded/incomplete report: count=%d", len(findings))
		}
	}
	replayed := requestJSON(t, baseURL, http.MethodPost, route, "contributor", "diagnostic-version", "", input)
	requireStatus(t, replayed, http.StatusCreated)
	if !bytes.Equal(result.body, replayed.body) {
		t.Fatal("bounded command replay changed")
	}
	detail := requestJSON(t, baseURL, http.MethodGet, "/api/v1/version-revisions/"+version.Revision.ID, "contributor", "", "", nil)
	requireStatus(t, detail, http.StatusOK)
	var read struct {
		Revision catalog.VersionRevision `json:"revision"`
		Evidence catalog.EvidenceSet     `json:"evidence"`
	}
	decodeResult(t, detail, &read)
	if !reflect.DeepEqual(read.Evidence, version.Evidence) || !reflect.DeepEqual(read.Revision, version.Revision) {
		t.Fatal("retained evidence or document changed")
	}
	requireStatus(t, requestJSON(t, baseURL, http.MethodGet, "/api/v1/version-revisions/"+version.Revision.ID, "other", "", "", nil), http.StatusNotFound)
	validated := requestJSON(t, baseURL, http.MethodPost, "/api/v1/version-revisions/"+version.Revision.ID+":validate", "contributor", "diagnostic-revalidate", "", map[string]any{})
	requireStatus(t, validated, http.StatusCreated)
	requireControlSchema(t, "RevisionEvidenceEnvelope", validated.body)
	decodeResult(t, validated, &read)
	// The API projects the latest validation run's ID while preserving all
	// immutable revision content and findings.
	wantRevision := version.Revision
	wantRevision.EvidenceSetID = read.Evidence.ID
	if read.Evidence.ID == version.Evidence.ID || read.Evidence.State != catalog.EvidenceBlocked || !reflect.DeepEqual(read.Evidence.Findings, version.Evidence.Findings) || !reflect.DeepEqual(read.Revision, wantRevision) {
		t.Fatal("revalidation lost diagnostic bound or denial")
	}
	if err := catalog.ValidateEvidenceSetIntegrity(read.Evidence); err != nil {
		t.Fatal(err)
	}
	validationReplay := requestJSON(t, baseURL, http.MethodPost, "/api/v1/version-revisions/"+version.Revision.ID+":validate", "contributor", "diagnostic-revalidate", "", map[string]any{})
	requireStatus(t, validationReplay, http.StatusCreated)
	if !bytes.Equal(validationReplay.body, validated.body) {
		t.Fatal("revalidation command replay changed")
	}
	listingResult := requestJSON(t, baseURL, http.MethodPost, "/api/v1/catalog-listings", "contributor", "diagnostic-listing", "", map[string]any{"namespaceId": namespaceID, "resourceId": resource.ID})
	requireStatus(t, listingResult, http.StatusCreated)
	var listing catalog.CatalogListing
	decodeResult(t, listingResult, &listing)
	listingRevision := requestJSON(t, baseURL, http.MethodPost, "/api/v1/catalog-listings/"+listing.ID+"/revisions", "contributor", "diagnostic-listing-revision", listingResult.header.Get("ETag"), map[string]any{"versionRevisionId": version.Revision.ID, "displayName": "Diagnostic fixture", "description": "Blocked validation", "publisherId": publisherID, "visibility": "tenant"})
	requireStatus(t, listingRevision, http.StatusCreated)
	submitted := requestJSON(t, baseURL, http.MethodPost, "/api/v1/catalog-listings/"+listing.ID+"/submissions", "contributor", "diagnostic-submission", "", map[string]any{})
	requireControlProblem(t, submitted, http.StatusUnprocessableEntity, catalog.CodeValidationFailed)
	t.Logf("invalid request=%d bytes, bounded create response=%d bytes", len(raw), len(result.body))
}
