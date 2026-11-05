// Publication-client tests use scripted HTTP responses, without a Catalog,
// database, socket, or execution of the smoke command's complete walkthrough.
package main

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
)

// smokeClientStep is one expected request and a bounded static response.
type smokeClientStep struct {
	name, method, path, token, key, match string
	status                                int
	header                                http.Header
	body                                  string
}

// publicationClientResponses supplies only the fields read by the publication
// helper. It implements no server state, authorization or domain transitions.
func publicationClientResponses(t *testing.T, fixture smokeFixture, options smokeOptions) []smokeClientStep {
	t.Helper()
	encode := func(value any) string {
		raw, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		return string(raw)
	}
	author, selfStatus := options.ContributorToken, http.StatusForbidden
	selfBody := `{}`
	if fixture.Kind == catalog.KindAgent {
		author, selfStatus = options.AdminToken, http.StatusConflict
		selfBody = `{"code":"SEPARATION_OF_DUTIES"}`
	}
	prefix := options.RunID + "-" + fixture.Segment
	content, document := fixture.Representation["content"].(string)
	digest := sha256.Sum256([]byte(content))
	documentID := hex.EncodeToString(digest[:])
	version := struct {
		Revision catalog.VersionRevision `json:"revision"`
		Evidence catalog.EvidenceSet     `json:"evidence"`
	}{
		Revision: catalog.VersionRevision{ID: "version", ContentSetDigest: "digest", EvidenceSetID: "evidence-old"},
		Evidence: catalog.EvidenceSet{ID: "evidence-old", State: catalog.EvidenceComplete, ValidationProfile: "catalog-validation-v7", LegalRights: catalog.LegalRightsSummary{State: catalog.LegalRightsNotAssessed}, Trust: catalog.TrustSummary{State: catalog.TrustNotAssessed}},
	}
	initial := encode(version)
	version.Revision.EvidenceSetID, version.Evidence.ID = "evidence-new", "evidence-new"
	if document {
		version.Revision.Representations = []catalog.Representation{{ArtifactID: documentID}}
	}
	steps := []smokeClientStep{
		{"resource", "POST", "/api/v1/resources", author, prefix + "-resource", "", 201, http.Header{}, `{"id":"resource"}`},
		{"replay", "POST", "/api/v1/resources", author, prefix + "-resource", "", 201, http.Header{"Idempotency-Replayed": {"true"}}, `{"id":"resource"}`},
		{"cross tenant", "GET", "/api/v1/resources/resource", options.OtherTenantToken, "", "", 404, http.Header{}, `{}`},
		{"version", "POST", "/api/v1/resources/resource/versions", author, prefix + "-version", "", 201, http.Header{}, initial},
		{"validation", "POST", "/api/v1/version-revisions/version:validate", author, prefix + "-validation", "", 201, http.Header{}, encode(version)},
	}
	if document {
		header := http.Header{}
		header.Set("Content-Length", strconv.Itoa(len(content)))
		header.Set("Content-Digest", "sha-256=:"+base64.StdEncoding.EncodeToString(digest[:])+":")
		header.Set("Content-Type", "text/markdown")
		header.Set("Content-Disposition", `attachment; filename="`+documentID+`"`)
		steps = append(steps, smokeClientStep{"document", "GET", "/api/v1/artifacts/" + documentID + "/content", options.ReaderToken, "", "", 200, header, content})
	}
	return append(steps,
		smokeClientStep{"listing", "POST", "/api/v1/catalog-listings", author, prefix + "-listing", "", 201, http.Header{"Etag": {`"listing-1"`}}, `{"id":"listing"}`},
		smokeClientStep{"revision", "POST", "/api/v1/catalog-listings/listing/revisions", author, prefix + "-listing-revision", `"listing-1"`, 201, http.Header{"Etag": {`"listing-2"`}}, `{}`},
		smokeClientStep{"submission", "POST", "/api/v1/catalog-listings/listing/submissions", author, prefix + "-submission", "", 201, http.Header{"Etag": {`"submission-1"`}}, `{"id":"submission"}`},
		smokeClientStep{"self approval", "POST", "/api/v1/review-submissions/submission:approve", author, prefix + "-self-approval", `"submission-1"`, selfStatus, http.Header{}, selfBody},
		smokeClientStep{"approval", "POST", "/api/v1/review-submissions/submission:approve", options.ReviewerToken, prefix + "-approval", `"submission-1"`, 200, http.Header{}, `{}`},
		smokeClientStep{"publish", "POST", "/api/v1/catalog-listings/listing:publish", options.PublisherToken, prefix + "-publish", `"listing-2"`, 201, http.Header{}, `{"release":{"id":"release"}}`},
	)
}

// TestSmokePublicationStopsOnRejectedResponses protects fail-fast behavior and
// authority/concurrency bindings at each request boundary of this client helper.
func TestSmokePublicationStopsOnRejectedResponses(t *testing.T) {
	t.Parallel()
	transportFailure := errors.New("controlled transport failure")
	type rejection struct {
		name, step, body, removeHeader, want string
		replace                              [2]string
		transport                            bool
	}
	cases := []rejection{
		{name: "success"},
		{name: "resource JSON", step: "resource", body: `[`, want: "Resource check: invalid response"},
		{name: "resource ID", step: "resource", body: `{}`, want: "response ID is absent"},
		{name: "replay header", step: "replay", removeHeader: "Idempotency-Replayed", want: "replay header is absent"},
		{name: "version JSON", step: "version", body: `[`, want: "version check: invalid response"},
		{name: "version evidence", step: "version", replace: [2]string{"catalog-validation-v7", "unsupported"}, want: "unexpected revision or evidence"},
		{name: "validation JSON", step: "validation", body: `[`, want: "version check: invalid response"},
		{name: "changed content", step: "validation", replace: [2]string{`"digest"`, `"changed"`}, want: "unexpected revision or evidence"},
		{name: "reused evidence", step: "validation", replace: [2]string{"evidence-new", "evidence-old"}, want: "unexpected revision or evidence"},
		{name: "unbound evidence", step: "validation", replace: [2]string{`"evidenceSetId":"evidence-new"`, `"evidenceSetId":"other"`}, want: "unexpected revision or evidence"},
		{name: "incomplete evidence", step: "validation", replace: [2]string{`"state":"complete"`, `"state":"pending"`}, want: "unexpected revision or evidence"},
		{name: "expiring evidence", step: "validation", replace: [2]string{`"evidence":{`, `"evidence":{"expiresAt":"2030-01-01T00:00:00Z",`}, want: "unexpected revision or evidence"},
		{name: "listing JSON", step: "listing", body: `[`, want: "listing check: invalid response"},
		{name: "listing ID", step: "listing", body: `{}`, want: "response ID is absent"},
		{name: "revision ETag", step: "revision", removeHeader: "ETag", want: "ETag is absent"},
		{name: "submission JSON", step: "submission", body: `[`, want: "listing check: invalid response"},
		{name: "submission ID", step: "submission", body: `{}`, want: "response ID or ETag is absent"},
		{name: "submission ETag", step: "submission", removeHeader: "ETag", want: "response ID or ETag is absent"},
		{name: "publish JSON", step: "publish", body: `[`, want: "listing check: invalid response"},
		{name: "release ID", step: "publish", body: `{}`, want: "release ID is absent"},
	}
	for _, fixture := range smokeFixtures("unit") {
		if fixture.Kind == catalog.KindMCPServer {
			continue // The non-Agent author path is already exercised by the Skill.
		}
		t.Run(string(fixture.Kind), func(t *testing.T) {
			options := smokeOptions{RunID: "unit", AdminToken: "admin", ContributorToken: "author", OtherTenantToken: "other-tenant", ReaderToken: "reader", ReviewerToken: "reviewer", PublisherToken: "publisher"}
			rejections := append([]rejection(nil), cases...)
			if fixture.Kind == catalog.KindAgent {
				rejections = append(rejections, rejection{name: "separation evidence", step: "self approval", body: `{}`, want: "separation-of-duties evidence is absent"})
			} else {
				rejections = append(rejections,
					rejection{name: "document identifier", step: "validation", replace: [2]string{`"artifactId"`, `"absentArtifactId"`}, want: "bounded document identifier is absent"},
					rejection{name: "changed document", step: "document", body: "changed", want: "response bytes do not match"},
					rejection{name: "document headers", step: "document", removeHeader: "Content-Digest", want: "exact content headers are incomplete"},
				)
			}
			for _, step := range publicationClientResponses(t, fixture, options) {
				rejections = append(rejections, rejection{name: step.name + " transport", step: step.name, transport: true})
			}
			for _, tc := range rejections {
				t.Run(tc.name, func(t *testing.T) {
					steps := publicationClientResponses(t, fixture, options)
					stop := len(steps) - 1
					if tc.step != "" {
						stop = -1
						for i := range steps {
							if steps[i].name == tc.step {
								stop = i
							}
						}
						if stop < 0 {
							t.Fatal("rejection has no matching request")
						}
						step := &steps[stop]
						if tc.body != "" {
							step.body = tc.body
						}
						if tc.removeHeader != "" {
							step.header.Del(tc.removeHeader)
						}
						if tc.replace[0] != "" {
							if !strings.Contains(step.body, tc.replace[0]) {
								t.Fatal("rejection does not change the response")
							}
							step.body = strings.ReplaceAll(step.body, tc.replace[0], tc.replace[1])
						}
					}
					calls := 0
					client := &smokeHTTPClient{baseURL: "https://catalog.invalid", client: &http.Client{Transport: catalogctlRoundTripFunc(func(request *http.Request) (*http.Response, error) {
						i := calls
						calls++
						if i > stop {
							t.Errorf("issued an extra request after rejection: %s", request.URL.Path)
							return nil, transportFailure
						}
						step := steps[i]
						if request.Method != step.method || request.URL.Path != step.path || request.Header.Get("Authorization") != "Bearer "+step.token || request.Header.Get("Idempotency-Key") != step.key || request.Header.Get("If-Match") != step.match {
							t.Fatalf("request %d lost method, path, actor, command key or revision binding", i)
						}
						if i == stop && tc.transport {
							return nil, transportFailure
						}
						return &http.Response{StatusCode: step.status, Header: step.header.Clone(), Body: io.NopCloser(strings.NewReader(step.body)), Request: request}, nil
					})}}
					result, err := smokePublishFixture(t.Context(), client, options, "namespace", "publisher", "example.com", fixture)
					if calls != stop+1 {
						t.Fatalf("requests=%d, want %d", calls, stop+1)
					}
					if tc.step == "" {
						if err != nil || result.ResourceID != "resource" || result.ListingID != "listing" || result.ReleaseID != "release" || result.Kind != fixture.Kind || result.Identifier != "urn:air:example.com:readiness:"+fixture.Segment {
							t.Fatalf("valid responses produced incorrect result: %#v, %v", result, err)
						}
						if content, ok := fixture.Representation["content"].(string); ok {
							digest := sha256.Sum256([]byte(content))
							if result.DocumentID != hex.EncodeToString(digest[:]) || string(result.documentContent) != content {
								t.Fatal("exact document evidence was not retained")
							}
						}
					} else if err == nil || !reflect.DeepEqual(result, smokeResource{}) || (tc.transport && !errors.Is(err, transportFailure)) || (!tc.transport && !strings.Contains(err.Error(), tc.want)) {
						t.Fatalf("rejection returned result=%#v error=%v, want %q", result, err, tc.want)
					}
					passedRequests := calls
					if tc.transport {
						passedRequests--
					}
					if len(client.checks) != passedRequests {
						t.Fatalf("transport failure counted as a passed request: %v", client.checks)
					}
				})
			}
		})
	}
}
