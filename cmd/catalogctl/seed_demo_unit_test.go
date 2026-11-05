// Demo item tests verify client decisions against static responses without
// creating a Catalog, opening a listener or simulating domain transitions.
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
)

// demoUnitJSON encodes bounded static response fields for the real client decoder.
func demoUnitJSON(t *testing.T, value any) string {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// demoUnitClient requires exactly the selected request prefix. It retains the
// demo command's safe-error setting so transport details cannot escape.
func demoUnitClient(t *testing.T, steps []smokeClientStep, failTransport bool, requestFields []map[string]any) *smokeHTTPClient {
	t.Helper()
	if requestFields != nil && len(requestFields) != len(steps) {
		t.Fatal("request expectations must match the selected request prefix")
	}
	calls := 0
	t.Cleanup(func() {
		if calls != len(steps) {
			t.Errorf("requests=%d, want %d", calls, len(steps))
		}
	})
	return &smokeHTTPClient{baseURL: "https://catalog.invalid", safeErrors: true, client: &http.Client{Transport: catalogctlRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		i := calls
		calls++
		if i >= len(steps) {
			t.Errorf("request continued beyond the selected lifecycle or rejection: %s", request.URL.Path)
			return nil, errors.New("unexpected request")
		}
		step := steps[i]
		authorization := ""
		if step.token != "" {
			authorization = "Bearer " + step.token
		}
		if request.Method != step.method || request.URL.RequestURI() != step.path || request.Header.Get("Authorization") != authorization || request.Header.Get("Idempotency-Key") != step.key || request.Header.Get("If-Match") != step.match {
			t.Fatalf("%s lost method, path, actor, command key or revision binding", step.name)
		}
		if request.Method == http.MethodGet && request.Body != nil {
			body, err := io.ReadAll(request.Body)
			if err != nil || len(body) != 0 {
				t.Fatal("verification sent a body with a read")
			}
		}
		if requestFields != nil && requestFields[i] != nil {
			if request.Body == nil || request.Header.Get("Content-Type") != "application/json" {
				t.Fatalf("%s requires a JSON command body", step.name)
			}
			body, err := io.ReadAll(request.Body)
			if err != nil {
				t.Fatal(err)
			}
			var actual map[string]json.RawMessage
			if err := json.Unmarshal(body, &actual); err != nil || actual == nil {
				t.Fatalf("%s requires a JSON object: %v", step.name, err)
			}
			for field, want := range requestFields[i] {
				var got any
				if err := json.Unmarshal(actual[field], &got); err != nil || demoUnitJSON(t, got) != demoUnitJSON(t, want) {
					t.Fatalf("%s lost command body field %s: got %s, want %s", step.name, field, actual[field], demoUnitJSON(t, want))
				}
			}
		}
		if failTransport && i == len(steps)-1 {
			return nil, errors.New("private-demo-transport-cause")
		}
		return &http.Response{StatusCode: step.status, Header: step.header.Clone(), Body: io.NopCloser(strings.NewReader(step.body)), Request: request}, nil
	})}}
}

// demoUnitItem identifies one fixture independently of the response builder.
func demoUnitItem(f demoFixture) demoItem {
	item := demoItem{Key: f.Segment, Kind: f.Kind, State: f.state, Identifier: demoIdentifier(f), ResourceID: "resource", VersionID: "version", VersionRevisionID: "version-revision", ListingID: "listing", ListingRevisionID: "listing-revision"}
	if f.state != "draft" {
		item.SubmissionID = "submission"
	}
	if f.state == "published" {
		item.ReleaseID = "release"
	}
	return item
}

// demoItemReadResponses supplies consistent evidence for each built-in fixture
// state. Fault cases alter one retained field without changing later responses.
func demoItemReadResponses(t *testing.T, f demoFixture, r demoReceipt, item demoItem) []smokeClientStep {
	t.Helper()
	sealed, submissionState := "", "submitted"
	if f.state == "published" {
		sealed, submissionState = item.VersionRevisionID, "approved"
	}
	content, markdown := f.Representation["content"].(string)
	if !markdown {
		content = demoUnitJSON(t, f.Representation["data"])
	}
	digest := sha256.Sum256([]byte(content))
	mediaType, ok := f.Representation["mediaType"].(string)
	if !ok {
		t.Fatal("fixture has no media type")
	}
	listing := demoListingInput(f, r.PublisherID, item.VersionRevisionID)
	listing["id"], listing["tenantId"], listing["listingId"] = item.ListingRevisionID, demoTenant, item.ListingID
	steps := make([]smokeClientStep, 0, 7)
	add := func(name, path string, value any) {
		steps = append(steps, smokeClientStep{name: name, method: "GET", path: path, token: "local-admin", status: 200, body: demoUnitJSON(t, value)})
	}
	add("resource", "/api/v1/resources/"+item.ResourceID, map[string]any{"id": item.ResourceID, "tenantId": demoTenant, "namespaceId": r.NamespaceID, "canonicalIdentifier": item.Identifier, "kind": f.Kind})
	add("version", "/api/v1/resource-versions/"+item.VersionID, catalog.ResourceVersion{ID: item.VersionID, TenantID: demoTenant, ResourceID: item.ResourceID, Version: "1.0.0", CandidateRevisionID: item.VersionRevisionID, SealedRevisionID: sealed})
	add("revision", "/api/v1/version-revisions/"+item.VersionRevisionID, map[string]any{"revision": catalog.VersionRevision{ID: item.VersionRevisionID, TenantID: demoTenant, ResourceVersionID: item.VersionID, Representations: []catalog.Representation{{ContentDigest: "sha256:" + hex.EncodeToString(digest[:]), MediaType: mediaType}}}})
	add("listing revision", "/api/v1/listing-revisions/"+item.ListingRevisionID, listing)
	add("listing", "/api/v1/catalog-listings/"+item.ListingID, catalog.CatalogListing{ID: item.ListingID, TenantID: demoTenant, NamespaceID: r.NamespaceID, ResourceID: item.ResourceID, CandidateRevisionID: item.ListingRevisionID, CurrentReleaseID: item.ReleaseID})
	if item.SubmissionID != "" {
		add("submission", "/api/v1/review-submissions/"+item.SubmissionID, map[string]any{"id": item.SubmissionID, "tenantId": demoTenant, "listingId": item.ListingID, "versionRevisionId": item.VersionRevisionID, "listingRevisionId": item.ListingRevisionID, "state": submissionState})
	}
	if item.ReleaseID != "" {
		add("release", "/api/v1/listing-releases/"+item.ReleaseID, map[string]any{"id": item.ReleaseID, "tenantId": demoTenant, "listingId": item.ListingID, "submissionId": item.SubmissionID, "versionRevisionId": item.VersionRevisionID, "listingRevisionId": item.ListingRevisionID, "state": "published"})
	}
	return steps
}

// TestVerifyDemoItemRejectsEvidence checks exact identities, pointers and bytes
// for all five fixtures, and requires read-only failure without continuation.
func TestVerifyDemoItemRejectsEvidence(t *testing.T) {
	t.Parallel()
	type rejection struct{ name, step, field, want string }
	for _, f := range demoFixtures() {
		t.Run(f.Segment, func(t *testing.T) {
			baseline := demoItemReadResponses(t, f, demoReceipt{NamespaceID: "namespace", PublisherID: "publisher"}, demoUnitItem(f))
			cases := make([]rejection, 0, 70)
			cases = append(cases, rejection{name: "success"}, rejection{name: "extra representations", step: "revision", field: "extra representations", want: "version evidence does not match fixture"})
			for _, step := range baseline {
				for _, fault := range []string{"transport", "JSON", "status"} {
					cases = append(cases, rejection{name: step.name + " " + fault, step: step.name, field: fault})
				}
				var fields []string
				want := "fixture field"
				switch step.name {
				case "resource":
					fields = []string{"id", "tenantId", "namespaceId", "canonicalIdentifier", "kind"}
				case "version":
					fields, want = []string{"id", "tenantId", "resourceId", "version", "candidateRevisionId", "sealedRevisionId"}, "current version differs"
				case "revision":
					fields, want = []string{"id", "tenantId", "resourceVersionId", "representations"}, "version evidence does not match fixture"
				case "listing revision":
					fields = []string{"id", "tenantId", "listingId", "publisherId", "versionRevisionId", "displayName", "extensions"}
				case "listing":
					fields, want = []string{"id", "tenantId", "resourceId", "namespaceId", "candidateRevisionId", "currentReleaseId"}, "current listing differs"
				case "submission":
					fields = []string{"id", "tenantId", "listingId", "versionRevisionId", "listingRevisionId", "state"}
				case "release":
					fields = []string{"id", "tenantId", "listingId", "submissionId", "versionRevisionId", "listingRevisionId", "state"}
				}
				for _, field := range fields {
					cases = append(cases, rejection{step.name + " " + field, step.name, field, want})
				}
			}
			for _, field := range []string{"contentDigest", "mediaType"} {
				cases = append(cases, rejection{"representation " + field, "revision", field, "representation content differs"})
			}
			for _, tc := range cases {
				t.Run(tc.name, func(t *testing.T) {
					steps := append([]smokeClientStep(nil), baseline...)
					if tc.step != "" {
						found := false
						for i, step := range steps {
							if step.name == tc.step {
								steps, found = steps[:i+1], true
								break
							}
						}
						if !found {
							t.Fatal("fault has no matching request")
						}
						last := &steps[len(steps)-1]
						switch tc.field {
						case "transport":
							tc.want = "request failed or deadline exceeded"
						case "JSON":
							last.body, tc.want = `[`, "invalid API JSON response"
						case "status":
							last.status, last.body, tc.want = 503, "private-demo-response-body", "HTTP 503, expected 200"
						default:
							var response map[string]any
							if err := json.Unmarshal([]byte(last.body), &response); err != nil {
								t.Fatal(err)
							}
							target := response
							var entries []any
							if tc.step == "revision" {
								var ok bool
								target, ok = response["revision"].(map[string]any)
								if !ok {
									t.Fatal("fixture has no revision object")
								}
								entries, ok = target["representations"].([]any)
								if !ok || len(entries) != 1 {
									t.Fatal("fixture must have one representation")
								}
								if tc.field == "contentDigest" || tc.field == "mediaType" {
									target, ok = entries[0].(map[string]any)
									if !ok {
										t.Fatal("fixture has no representation object")
									}
								}
							}
							switch tc.field {
							case "representations":
								target[tc.field] = []any{}
							case "extra representations":
								target["representations"] = append(entries, entries[0])
							default:
								target[tc.field] = "wrong"
							}
							last.body = demoUnitJSON(t, response)
						}
					}
					err := verifyDemoItem(t.Context(), demoUnitClient(t, steps, tc.field == "transport", nil), demoReceipt{NamespaceID: "namespace", PublisherID: "publisher"}, demoUnitItem(f), f)
					if tc.step == "" {
						if err != nil {
							t.Fatal(err)
						}
					} else if err == nil || !strings.Contains(err.Error(), tc.want) || strings.Contains(err.Error(), "private-demo") {
						t.Fatalf("rejection=%v, want %q with no private transport/response details", err, tc.want)
					}
				})
			}
		})
	}
}

// demoItemCreationResponses supplies fixed lifecycle responses and checks command
// bindings independently of the production payload builder.
func demoItemCreationResponses(t *testing.T, f demoFixture, r demoReceipt, item demoItem) ([]smokeClientStep, []map[string]any) {
	t.Helper()
	prefix := demoID + "-" + f.Segment
	listingETag := fmt.Sprintf("%q", f.Segment+"-listing-created")
	revisionETag := fmt.Sprintf("%q", f.Segment+"-listing-revised")
	submissionETag := fmt.Sprintf("%q", f.Segment+"-submitted")
	baseline := []smokeClientStep{
		{"resource", "POST", "/api/v1/resources", "local-contributor", prefix + "-resource", "", 201, nil, demoUnitJSON(t, map[string]any{"id": item.ResourceID})},
		{"version", "POST", "/api/v1/resources/" + item.ResourceID + "/versions", "local-contributor", prefix + "-version", "", 201, nil, demoUnitJSON(t, map[string]any{"revision": map[string]any{"id": item.VersionRevisionID, "resourceVersionId": item.VersionID}})},
		{"listing", "POST", "/api/v1/catalog-listings", "local-contributor", prefix + "-listing", "", 201, http.Header{"Etag": {listingETag}}, demoUnitJSON(t, map[string]any{"id": item.ListingID})},
		{"listing revision", "POST", "/api/v1/catalog-listings/" + item.ListingID + "/revisions", "local-contributor", prefix + "-listing-revision", listingETag, 201, http.Header{"Etag": {revisionETag}}, demoUnitJSON(t, map[string]any{"revision": map[string]any{"id": item.ListingRevisionID}})},
		{"submission", "POST", "/api/v1/catalog-listings/" + item.ListingID + "/submissions", "local-contributor", prefix + "-submit", "", 201, http.Header{"Etag": {submissionETag}}, demoUnitJSON(t, map[string]any{"id": item.SubmissionID})},
		{"approval", "POST", "/api/v1/review-submissions/" + item.SubmissionID + ":approve", "local-reviewer", prefix + "-approve", submissionETag, 200, nil, `{}`},
		{"release", "POST", "/api/v1/catalog-listings/" + item.ListingID + ":publish", "local-publisher", prefix + "-publish", revisionETag, 201, nil, demoUnitJSON(t, map[string]any{"release": map[string]any{"id": item.ReleaseID}})},
	}
	requestFields := []map[string]any{
		{"namespaceId": r.NamespaceID, "canonicalIdentifier": demoIdentifier(f), "kind": f.Kind},
		{"version": "1.0.0", "representations": []map[string]any{f.Representation}},
		{"namespaceId": r.NamespaceID, "resourceId": item.ResourceID},
		{"publisherId": r.PublisherID, "versionRevisionId": item.VersionRevisionID},
		{},
		{"reason": "Independent review of synthetic demo metadata"},
		{"submissionId": item.SubmissionID},
	}
	if f.state == "draft" {
		baseline = baseline[:4]
	} else if f.state == "pending_review" {
		baseline = baseline[:5]
	}
	return baseline, requestFields[:len(baseline)]
}

// TestCreateDemoItemStopsAtLifecycle protects partial receipt accounting and
// command authority/preconditions without executing a server-side workflow.
func TestCreateDemoItemStopsAtLifecycle(t *testing.T) {
	t.Parallel()
	for _, f := range demoFixtures() {
		t.Run(f.Segment, func(t *testing.T) {
			baseline, requestFields := demoItemCreationResponses(t, f, demoReceipt{NamespaceID: "namespace", PublisherID: "publisher"}, demoUnitItem(f))
			for stop := range len(baseline) + 1 {
				for _, fault := range []string{"transport", "JSON", "status"} {
					if stop == len(baseline) && fault != "transport" {
						continue
					}
					name := "success"
					if stop < len(baseline) {
						name = baseline[stop].name + " " + fault
					}
					t.Run(name, func(t *testing.T) {
						steps := append([]smokeClientStep(nil), baseline...)
						want := demoUnitItem(f)
						failure := stop < len(baseline)
						wantError := "request failed or deadline exceeded"
						if failure {
							steps = steps[:stop+1]
							if fault == "JSON" {
								steps[stop].body = `[`
								wantError = "invalid API JSON response"
							} else if fault == "status" {
								wantError = fmt.Sprintf("HTTP 503, expected %d", steps[stop].status)
								steps[stop].status, steps[stop].body = 503, "private-demo-response-body"
							}
							want.ReleaseID = ""
							if stop <= 4 {
								want.SubmissionID = ""
							}
							if stop <= 3 {
								want.ListingRevisionID = ""
							}
							if stop <= 2 {
								want.ListingID = ""
							}
							if stop <= 1 {
								want.VersionID, want.VersionRevisionID = "", ""
							}
							if stop == 0 {
								want.ResourceID = ""
							}
						}
						got, err := createDemoItem(t.Context(), demoUnitClient(t, steps, failure && fault == "transport", requestFields[:len(steps)]), demoReceipt{NamespaceID: "namespace", PublisherID: "publisher"}, f)
						if !reflect.DeepEqual(got, want) || (err != nil) != failure || err != nil && (!strings.Contains(err.Error(), wantError) || strings.Contains(err.Error(), "private-demo")) {
							t.Fatalf("item=%#v error=%v; want item=%#v failure=%t", got, err, want, failure)
						}
					})
				}
			}
		})
	}
}
