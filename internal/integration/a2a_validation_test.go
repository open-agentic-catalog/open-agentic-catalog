//go:build functional

package integration

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/adapter"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
)

// Parser permutations belong in adapter units. Each repaired class must also
// block review through the real API without rewriting retained evidence.
func TestA2AInvalidMetadataBlocksReviewAndPreservesEvidence(t *testing.T) {
	for _, tc := range []struct {
		name      string
		version   string
		protected string
		signature string
		header    map[string]any
		code      string
		path      string
	}{
		{name: "version whitespace", version: " 1.0 ", code: "A2A_INTERFACE_INVALID", path: "/supportedInterfaces/0"},
		{name: "signature line break", signature: "c2\r\nln", code: "A2A_SIGNATURE_INVALID", path: "/signatures/0/signature"},
		{name: "algorithm shadow", protected: `{"alg":"none","ALG":"ES256","kid":"test-key"}`, code: "A2A_SIGNATURE_INVALID", path: "/signatures/0/protected"},
		{name: "missing protected key ID", protected: `{"alg":"ES256"}`, code: "A2A_SIGNATURE_INVALID", path: "/signatures/0/protected"},
		{name: "overlapping header", header: map[string]any{"kid": "test-key"}, code: "A2A_SIGNATURE_INVALID", path: "/signatures/0/header"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			runtime := buildRuntime(t)
			defer runtime.Close()
			server := httptest.NewServer(runtime.HTTP.Handler())
			defer server.Close()
			post := func(path, key string, body any) response {
				return request(t, server.URL, http.MethodPost, path, "admin", key, "", body)
			}
			created := post("/api/v1/namespaces", "namespace", map[string]any{"slug": "agents", "displayName": "Agents"})
			requireStatus(t, created, http.StatusCreated)
			var namespace catalog.Namespace
			decode(t, created, &namespace)
			created = post("/api/v1/resources", "resource", map[string]any{"namespaceId": namespace.ID, "canonicalIdentifier": "urn:air:catalog.example.com:agents:example", "kind": "agent"})
			requireStatus(t, created, http.StatusCreated)
			var resource catalog.Resource
			decode(t, created, &resource)

			invalid := a2aGovernanceCard(t, tc.version, tc.protected, tc.signature, tc.header)
			representations := func(document json.RawMessage) []any {
				return []any{map[string]any{"role": "primary", "mediaType": adapter.MediaA2AAgentCard, "data": document}}
			}
			created = post("/api/v1/resources/"+resource.ID+"/versions", "version", map[string]any{"version": "1.0.0", "representations": representations(invalid)})
			requireStatus(t, created, http.StatusCreated)
			type versionEnvelope struct {
				Version  catalog.ResourceVersion `json:"version"`
				Revision catalog.VersionRevision `json:"revision"`
				Evidence catalog.EvidenceSet     `json:"evidence"`
			}
			var original versionEnvelope
			decode(t, created, &original)
			versionETag := created.header.Get("ETag")
			assertBlocked := func(evidence catalog.EvidenceSet) {
				t.Helper()
				if evidence.State != catalog.EvidenceBlocked || len(evidence.Findings) != 1 {
					t.Fatalf("invalid A2A evidence: %#v", evidence)
				}
				finding := evidence.Findings[0]
				if finding.Code != tc.code || finding.Severity != catalog.SeverityError || finding.Path != "/representations/0"+tc.path {
					t.Fatalf("wrong A2A finding: %#v", finding)
				}
				if evidence.Trust.State != catalog.TrustNotAssessed || evidence.LegalRights.State != catalog.LegalRightsNotAssessed {
					t.Fatal("structural validation changed trust or rights assessment")
				}
			}
			assertBlocked(original.Evidence)
			rep := original.Revision.Representations[0]
			if rep.ValidationState != "invalid" || rep.ProfileID != adapter.ProfileA2AAgentCard || !bytes.Equal(rep.Data, invalid) || rep.ContentDigest != fmt.Sprintf("sha256:%x", sha256.Sum256(invalid)) || rep.Size != int64(len(invalid)) {
				t.Fatal("invalid card was normalized, relabelled or retained as valid")
			}
			renewedResponse := post("/api/v1/version-revisions/"+original.Revision.ID+":validate", "validate", map[string]any{})
			requireStatus(t, renewedResponse, http.StatusCreated)
			var renewed versionEnvelope
			decode(t, renewedResponse, &renewed)
			assertBlocked(renewed.Evidence)
			if renewed.Evidence.ID == original.Evidence.ID || renewed.Revision.ContentSetDigest != original.Revision.ContentSetDigest || !reflect.DeepEqual(renewed.Revision.Representations, original.Revision.Representations) {
				t.Fatal("revalidation rewrote the representation or reused evidence")
			}

			created = post("/api/v1/publishers", "publisher", map[string]any{"displayName": "Example", "domains": []string{"catalog.example.com"}})
			requireStatus(t, created, http.StatusCreated)
			var publisher catalog.Publisher
			decode(t, created, &publisher)
			created = post("/api/v1/catalog-listings", "listing", map[string]any{"namespaceId": namespace.ID, "resourceId": resource.ID})
			requireStatus(t, created, http.StatusCreated)
			var listing catalog.CatalogListing
			decode(t, created, &listing)
			listingInput := map[string]any{"versionRevisionId": original.Revision.ID, "displayName": "Example", "description": "Example Agent", "publisherId": publisher.ID, "visibility": "tenant", "license": "Apache-2.0"}
			revision := request(t, server.URL, http.MethodPost, "/api/v1/catalog-listings/"+listing.ID+"/revisions", "admin", "listing-revision", created.header.Get("ETag"), listingInput)
			requireStatus(t, revision, http.StatusCreated)
			submitted := post("/api/v1/catalog-listings/"+listing.ID+"/submissions", "submit-invalid", map[string]any{})
			requireStatus(t, submitted, http.StatusUnprocessableEntity)
			var problem struct {
				Code string `json:"code"`
			}
			decode(t, submitted, &problem)
			if problem.Code != string(catalog.CodeValidationFailed) {
				t.Fatalf("unexpected review failure: %s", submitted.body)
			}

			// A corrected new revision can enter review; the old invalid one and
			// its latest evidence remain unchanged and readable.
			correctedCard := a2aGovernanceCard(t, "", "", "", nil)
			correctedResponse := request(t, server.URL, http.MethodPost, "/api/v1/resource-versions/"+original.Version.ID+"/revisions", "admin", "correct-version", versionETag, map[string]any{"representations": representations(correctedCard)})
			requireStatus(t, correctedResponse, http.StatusCreated)
			var corrected versionEnvelope
			decode(t, correctedResponse, &corrected)
			if corrected.Revision.ID == original.Revision.ID || corrected.Evidence.State != catalog.EvidenceComplete || len(corrected.Evidence.Findings) != 0 || !bytes.Equal(corrected.Revision.Representations[0].Data, correctedCard) || corrected.Evidence.Trust.State != catalog.TrustNotAssessed {
				t.Fatalf("corrected card not accepted with separate identity: %#v", corrected)
			}
			listingInput["versionRevisionId"] = corrected.Revision.ID
			updated := request(t, server.URL, http.MethodPost, "/api/v1/catalog-listings/"+listing.ID+"/revisions", "admin", "correct-listing", revision.header.Get("ETag"), listingInput)
			requireStatus(t, updated, http.StatusCreated)
			submitted = post("/api/v1/catalog-listings/"+listing.ID+"/submissions", "submit-corrected", map[string]any{})
			requireStatus(t, submitted, http.StatusCreated)
			var submission catalog.ReviewSubmission
			decode(t, submitted, &submission)
			if submission.VersionRevisionID != corrected.Revision.ID || submission.EvidenceSetID != corrected.Evidence.ID {
				t.Fatal("review did not bind the corrected revision and evidence")
			}
			readback := request(t, server.URL, http.MethodGet, "/api/v1/version-revisions/"+original.Revision.ID, "reader", "", "", nil)
			requireStatus(t, readback, http.StatusOK)
			var retained versionEnvelope
			decode(t, readback, &retained)
			if !reflect.DeepEqual(retained.Revision, renewed.Revision) || !reflect.DeepEqual(retained.Evidence, renewed.Evidence) {
				t.Fatal("correcting the candidate changed retained invalid evidence")
			}
		})
	}
}

// Values are deliberately limited to strings, arrays and objects so json.Marshal
// also supplies the expected canonical representation bytes independently.
func a2aGovernanceCard(t *testing.T, version, protected, signature string, header map[string]any) json.RawMessage {
	t.Helper()
	if version == "" {
		version = "1.0"
	}
	if protected == "" {
		protected = `{"alg":"ES256","kid":"test-key"}`
	}
	if signature == "" {
		signature = "c2ln" // Structural metadata only; no cryptographic verification.
	}
	signed := map[string]any{"protected": base64.RawURLEncoding.EncodeToString([]byte(protected)), "signature": signature}
	if header != nil {
		signed["header"] = header
	}
	raw, err := json.Marshal(map[string]any{
		"name": "Agent", "description": "Local validation fixture", "version": "1.0.0",
		"supportedInterfaces": []any{map[string]any{"url": "https://agents.example.com/a2a", "protocolBinding": "JSONRPC", "protocolVersion": version}},
		"capabilities":        map[string]any{}, "defaultInputModes": []string{"text/plain"}, "defaultOutputModes": []string{"text/plain"},
		"skills":     []any{map[string]any{"id": "echo", "name": "Echo", "description": "Returns text", "tags": []string{"text"}}},
		"signatures": []any{signed},
	})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
