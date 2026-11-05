package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
)

func demoJournalUnitStep(t *testing.T) demoStep {
	t.Helper()
	body, err := json.Marshal(map[string]any{"slug": demoID, "displayName": "Catalog demo"})
	if err != nil {
		t.Fatal(err)
	}
	response, err := json.Marshal(map[string]any{"id": demoTestUUID(1000), "tenantId": demoTenant, "slug": demoID, "displayName": "Catalog demo", "revision": 1})
	if err != nil {
		t.Fatal(err)
	}
	return demoStep{Path: "/api/v1/namespaces", Actor: "local-admin", Key: "unit-namespace", Body: body, Status: 201, Response: response, ResponseETag: "\"rev-1\""}
}

func TestDemoJournalCommandBindingsAndReadOnlyReplay(t *testing.T) {
	step := demoJournalUnitStep(t)
	for _, fault := range []string{"valid", "path", "actor", "key", "etag", "body", "status", "response"} {
		t.Run(fault, func(t *testing.T) {
			p := &demoProgress{Steps: []demoStep{step}}
			c := &smokeHTTPClient{demoProgress: p}
			path, actor, key, etag, status := step.Path, step.Actor, step.Key, step.ETag, step.Status
			body := map[string]any{"slug": demoID, "displayName": "Catalog demo"}
			switch fault {
			case "path":
				path = "/api/v1/resources"
			case "actor":
				actor = "local-contributor"
			case "key":
				key = "changed"
			case "etag":
				etag = "changed"
			case "body":
				body["displayName"] = "Changed"
			case "status":
				status = 200
			case "response":
				p.Steps[0].Response = json.RawMessage(`{}`)
			}
			response, err := p.command(t.Context(), c, path, actor, key, etag, body, status)
			if fault == "valid" {
				if err != nil || !bytes.Equal(response.Body, step.Response) || p.index != 1 {
					t.Fatal(err)
				}
			} else if err == nil {
				t.Fatal("binding failure admitted")
			}
		})
	}
	p := &demoProgress{dry: true}
	if _, err := p.command(t.Context(), nil, step.Path, step.Actor, step.Key, "", map[string]any{}, 201); !errors.Is(err, errDemoCheckpoint) {
		t.Fatal(err)
	}
	pending := step
	pending.Response = nil
	p.Steps = []demoStep{pending}
	var body any
	if err := json.Unmarshal(step.Body, &body); err != nil {
		t.Fatal(err)
	}
	if _, err := p.command(t.Context(), nil, step.Path, step.Actor, step.Key, "", body, 201); !errors.Is(err, errDemoCheckpoint) {
		t.Fatal(err)
	}
}

func TestDemoJournalCurrentStateReadback(t *testing.T) {
	t.Parallel()
	const root = "/api/v1/"
	id, child, version := demoTestUUID(4400), demoTestUUID(4401), demoTestUUID(4402)
	object := func(id string, fields map[string]any) map[string]any {
		value := map[string]any{"id": id, "tenantId": demoTenant}
		for k, v := range fields {
			value[k] = v
		}
		return value
	}
	listingRevisionID, releaseID := demoTestUUID(4403), demoTestUUID(4404)
	listing := object(id, map[string]any{"resourceId": id, "candidateRevisionId": listingRevisionID, "revision": 2})
	revision := object(child, map[string]any{"resourceVersionId": version, "revisionNumber": 1, "contentSetDigest": "sha256:" + strings.Repeat("a", 64)})
	listingRevision := object(listingRevisionID, map[string]any{"listingId": id, "versionRevisionId": child, "displayName": "Reviewed sample", "visibility": "public", "revisionNumber": 1})
	resourceVersion := object(version, map[string]any{"resourceId": id, "version": "1.0", "candidateRevisionId": child, "revision": 1})
	approvedVersion := object(version, map[string]any{"resourceId": id, "version": "1.0", "candidateRevisionId": child, "sealedRevisionId": child, "revision": 2})
	publishedListing := object(id, map[string]any{"resourceId": id, "candidateRevisionId": listingRevisionID, "currentReleaseId": releaseID, "revision": 3})
	release := object(releaseID, map[string]any{"listingId": id, "versionRevisionId": child, "listingRevisionId": listingRevisionID, "state": "published"})
	for _, tc := range []struct {
		name, path string
		response   any
		reads      map[string]any
	}{
		{"namespace", root + "namespaces", object(id, map[string]any{"slug": demoID, "displayName": "Catalog demo", "revision": 1}), map[string]any{root + "namespaces/" + id: object(id, map[string]any{"slug": demoID, "displayName": "Catalog demo", "revision": 1})}},
		{"publisher", root + "publishers", object(id, map[string]any{"displayName": "Sample publisher", "domains": []string{demoDomain}, "revision": 1}), map[string]any{root + "publishers/" + id: object(id, map[string]any{"displayName": "Sample publisher", "domains": []string{demoDomain}, "revision": 1})}},
		{"resource", root + "resources", object(id, map[string]any{"kind": "agent", "canonicalIdentifier": "urn:air:example.com:demo:agent", "revision": 1}), map[string]any{root + "resources/" + id: object(id, map[string]any{"kind": "agent", "canonicalIdentifier": "urn:air:example.com:demo:agent", "revision": 1})}},
		{"listing", root + "catalog-listings", listing, map[string]any{root + "catalog-listings/" + id: listing}},
		{"publisher domain", root + "publishers/" + id + "/publication-domains/" + demoDomain + ":approve", object(id, map[string]any{"approvedPublicationDomains": []string{demoDomain}}), map[string]any{root + "publishers/" + id: object(id, map[string]any{"approvedPublicationDomains": []string{demoDomain}})}},
		{"version", root + "resources/" + id + "/versions", map[string]any{"version": resourceVersion, "revision": revision}, map[string]any{root + "resource-versions/" + version: resourceVersion, root + "version-revisions/" + child: map[string]any{"revision": revision}}},
		{"listing revision", root + "catalog-listings/" + id + "/revisions", map[string]any{"listing": listing, "revision": listingRevision}, map[string]any{root + "catalog-listings/" + id: listing, root + "listing-revisions/" + listingRevisionID: listingRevision}},
		{"submission", root + "catalog-listings/" + child + "/submissions", object(id, map[string]any{"state": "submitted"}), map[string]any{root + "review-submissions/" + id: object(id, map[string]any{"state": "submitted"})}},
		{"validation", root + "version-revisions/" + child + ":validate", map[string]any{"revision": revision}, map[string]any{root + "version-revisions/" + child: map[string]any{"revision": revision}}},
		{"approval", root + "review-submissions/" + id + ":approve", map[string]any{"submission": object(id, map[string]any{"state": "approved", "versionRevisionId": child, "revision": 2}), "version": approvedVersion}, map[string]any{root + "review-submissions/" + id: object(id, map[string]any{"state": "approved", "versionRevisionId": child, "revision": 2}), root + "resource-versions/" + version: approvedVersion}},
		{"publication", root + "catalog-listings/" + id + ":publish", map[string]any{"listing": publishedListing, "release": release}, map[string]any{root + "catalog-listings/" + id: publishedListing, root + "listing-releases/" + releaseID: release}},
		{"snapshot", root + "publication/snapshots", object(id, map[string]any{"host": "127.0.0.1", "state": "candidate"}), map[string]any{root + "publication/snapshots/" + id + "?host=127.0.0.1": object(id, map[string]any{"host": "127.0.0.1", "state": "candidate"})}},
		{"activation", root + "publication/snapshots/" + id + ":activate", object(id, map[string]any{"host": "127.0.0.1", "state": "active"}), map[string]any{root + "publication/snapshots/" + id + "?host=127.0.0.1": object(id, map[string]any{"host": "127.0.0.1", "state": "active"})}},
	} {
		for _, drift := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/drift=%t", tc.name, drift), func(t *testing.T) {
				p := demoProgress{Steps: []demoStep{{Path: tc.path, Response: json.RawMessage(demoUnitJSON(t, tc.response))}, {Path: "/unfinished", Response: nil}}}
				seen := map[string]bool{}
				c := &smokeHTTPClient{baseURL: "http://127.0.0.1:8080", safeErrors: true, client: &http.Client{Transport: catalogctlRoundTripFunc(func(r *http.Request) (*http.Response, error) {
					path := r.URL.RequestURI()
					want, ok := tc.reads[path]
					if r.Method != http.MethodGet || r.Header.Get("Authorization") != "Bearer local-admin" || !ok || seen[path] {
						t.Fatalf("unexpected or mutating readback: %s %s", r.Method, path)
					}
					seen[path] = true
					raw := demoUnitJSON(t, want)
					if drift {
						raw = strings.ReplaceAll(raw, demoTenant, demoTestUUID(4499))
					}
					return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(raw))}, nil
				})}}
				err := p.verifyCurrent(t.Context(), c)
				if drift {
					if err == nil || err.Error() != "seed-demo: owned current record differs; refusing writes" || len(seen) != 1 {
						t.Fatalf("drift did not stop current-state admission: %v, reads %d", err, len(seen))
					}
				} else if err != nil || len(seen) != len(tc.reads) {
					t.Fatalf("exact current records refused: %v, reads %d", err, len(seen))
				}
			})
		}
	}
}

func TestDemoJournalCurrentStateUsesLatestResponseAndRejectsInvalidOperations(t *testing.T) {
	t.Parallel()
	id := demoTestUUID(4500)
	path := "/api/v1/publishers/" + id
	latest := map[string]any{"id": id, "tenantId": demoTenant, "revision": 2, "approvedPublicationDomains": []string{demoDomain}}
	p := demoProgress{Steps: []demoStep{
		{Path: "/api/v1/publishers", Response: json.RawMessage(demoUnitJSON(t, map[string]any{"id": id, "tenantId": demoTenant, "revision": 1}))},
		{Path: path + "/publication-domains/" + demoDomain + ":approve", Response: json.RawMessage(demoUnitJSON(t, latest))},
	}}
	calls := 0
	c := &smokeHTTPClient{baseURL: "http://127.0.0.1:8080", safeErrors: true, client: &http.Client{Transport: catalogctlRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.Method != http.MethodGet || r.URL.Path != path {
			t.Fatalf("unexpected replay read: %s %s", r.Method, r.URL.Path)
		}
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(demoUnitJSON(t, latest)))}, nil
	})}}
	if err := p.verifyCurrent(t.Context(), c); err != nil || calls != 1 {
		t.Fatalf("latest response was not authoritative for replay: %v, calls %d", err, calls)
	}
	for _, tc := range []struct {
		step demoStep
		want string
	}{
		{demoStep{Path: "/invalid", Response: json.RawMessage(`{}`)}, "seed-demo: invalid recorded operation"},
		{demoStep{Path: "/api/v1/publishers", Response: json.RawMessage(`{`)}, "seed-demo: invalid recorded response"},
	} {
		p.Steps = []demoStep{tc.step}
		if err := p.verifyCurrent(t.Context(), c); err == nil || err.Error() != tc.want || calls != 1 {
			t.Fatalf("invalid journal caused a read or was admitted: %v, calls %d", err, calls)
		}
	}
}

func TestDemoJournalIntentSavedBeforeRequest(t *testing.T) {
	step := demoJournalUnitStep(t)
	for _, fault := range []string{"none", "lost response", "malformed response"} {
		t.Run(fault, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "progress")
			p, _, err := readDemoProgress(path, "http://127.0.0.1:8080", "https://127.0.0.1:18443")
			if err != nil {
				t.Fatal(err)
			}
			requests := 0
			c := &smokeHTTPClient{baseURL: p.BaseURL, safeErrors: true, client: &http.Client{Transport: catalogctlRoundTripFunc(func(r *http.Request) (*http.Response, error) {
				requests++
				if r.Method == http.MethodGet {
					return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"items":[]}`))}, nil
				}
				onDisk, _, e := readDemoProgress(path, p.BaseURL, p.Origin)
				if e != nil || len(onDisk.Steps) != 1 || len(onDisk.Steps[0].Response) != 0 {
					t.Fatal("command ran before durable intent")
				}
				if !strings.HasPrefix(r.Header.Get("Idempotency-Key"), step.Key+"-") {
					t.Fatal("input-bound key missing")
				}
				if fault == "lost response" {
					return nil, errors.New("secret transport detail")
				}
				result := step.Response
				if fault == "malformed response" {
					result = json.RawMessage(`{}`)
				}
				return &http.Response{StatusCode: http.StatusCreated, Header: http.Header{"Etag": {step.ResponseETag}}, Body: io.NopCloser(bytes.NewReader(result))}, nil
			})}}
			var body any
			if err = json.Unmarshal(step.Body, &body); err != nil {
				t.Fatal(err)
			}
			_, err = p.command(t.Context(), c, step.Path, step.Actor, step.Key, "", body, 201)
			if fault == "none" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil || strings.Contains(err.Error(), "secret") {
				t.Fatal("unsafe response accepted")
			}
			if requests != 2 {
				t.Fatal("unexpected command count")
			}
			saved, _, e := readDemoProgress(path, p.BaseURL, p.Origin)
			if e != nil {
				t.Fatal(e)
			}
			if (len(saved.Steps[0].Response) > 0) != (fault == "none") {
				t.Fatal("completion checkpoint differs")
			}
		})
	}
}

func TestDemoReconciliationUsesCurrentExactInput(t *testing.T) {
	step := demoJournalUnitStep(t)
	step.Response = nil
	for _, fault := range []string{"valid", "absent", "ambiguous", "drift"} {
		t.Run(fault, func(t *testing.T) {
			ids := []string{demoTestUUID(1000)}
			if fault == "absent" {
				ids = nil
			}
			if fault == "ambiguous" {
				ids = append(ids, demoTestUUID(1001))
			}
			c := &smokeHTTPClient{baseURL: "http://127.0.0.1:8080", safeErrors: true, client: &http.Client{Transport: catalogctlRoundTripFunc(func(r *http.Request) (*http.Response, error) {
				if r.Method != http.MethodGet {
					t.Fatal("reconciliation wrote")
				}
				var value any
				if r.URL.Path == "/api/v1/namespaces" {
					items := make([]map[string]string, 0, len(ids))
					for _, id := range ids {
						items = append(items, map[string]string{"id": id})
					}
					value = map[string]any{"items": items}
				} else {
					value = map[string]any{"id": strings.TrimPrefix(r.URL.Path, "/api/v1/namespaces/"), "slug": demoID, "displayName": "Catalog demo", "tenantId": demoTenant, "revision": 1}
					if fault == "drift" {
						object, ok := value.(map[string]any)
						if !ok {
							t.Fatal("expected object")
						}
						object["displayName"] = "Changed"
					}
				}
				raw, e := json.Marshal(value)
				if e != nil {
					t.Fatal(e)
				}
				return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Etag": {"\"rev-1\""}}, Body: io.NopCloser(bytes.NewReader(raw))}, nil
			})}}
			_, found, err := reconcileDemoStep(t.Context(), c, step)
			switch fault {
			case "valid":
				if err != nil || !found {
					t.Fatal(err)
				}
			case "absent":
				if err != nil || found {
					t.Fatal(err)
				}
			default:
				if err == nil {
					t.Fatal("uncertain outcome silently adopted")
				}
			}
		})
	}
}

func TestDemoStepResponseRejectsChangedEvidence(t *testing.T) {
	fixture := demoFixtures()[0]
	input := map[string]any{"version": "1.0.0", "representations": []map[string]any{fixture.Representation}}
	representation := demodataRepresentationForUnit(t, fixture)
	versionID, revisionID, resourceID := demoTestUUID(1000), demoTestUUID(1001), demoTestUUID(1002)
	valid := map[string]any{
		"version":  map[string]any{"id": versionID, "tenantId": demoTenant, "resourceId": resourceID, "version": "1.0.0", "candidateRevisionId": revisionID},
		"revision": map[string]any{"id": revisionID, "tenantId": demoTenant, "resourceVersionId": versionID, "createdBy": "local-contributor", "representations": []any{representation}},
	}
	body, e := json.Marshal(input)
	if e != nil {
		t.Fatal(e)
	}
	for _, fault := range []string{"valid", "tenant", "parent", "digest", "invalid", "role"} {
		t.Run(fault, func(t *testing.T) {
			raw, e := json.Marshal(valid)
			if e != nil {
				t.Fatal(e)
			}
			var result map[string]any
			if e = json.Unmarshal(raw, &result); e != nil {
				t.Fatal(e)
			}
			version, ok := result["version"].(map[string]any)
			if !ok {
				t.Fatal("expected version")
			}
			revision, ok := result["revision"].(map[string]any)
			if !ok {
				t.Fatal("expected revision")
			}
			reps, ok := revision["representations"].([]any)
			if !ok || len(reps) != 1 {
				t.Fatal("expected representation")
			}
			rep, ok := reps[0].(map[string]any)
			if !ok {
				t.Fatal("expected representation object")
			}
			switch fault {
			case "tenant":
				version["tenantId"] = demoTestUUID(1003)
			case "parent":
				version["resourceId"] = demoTestUUID(1004)
			case "digest":
				rep["contentDigest"] = "wrong"
			case "invalid":
				rep["validationState"] = "invalid"
			case "role":
				rep["role"] = "extra"
			}
			raw, e = json.Marshal(result)
			if e != nil {
				t.Fatal(e)
			}
			err := validateDemoStepResponse(demoStep{Path: "/api/v1/resources/" + resourceID + "/versions", Actor: "local-contributor", Body: body, Response: raw})
			if (err == nil) != (fault == "valid") {
				t.Fatalf("fault %s %v", fault, err)
			}
		})
	}
}

func demodataRepresentationForUnit(t *testing.T, f demoFixture) catalog.Representation {
	t.Helper()
	raw, err := json.Marshal(f.Representation["data"])
	if err != nil {
		t.Fatal(err)
	}
	mediaType, ok := f.Representation["mediaType"].(string)
	if !ok {
		t.Fatal("expected media type")
	}
	return catalog.Representation{Role: catalog.RepresentationPrimary, MediaType: mediaType, ContentDigest: fmt.Sprintf("sha256:%x", sha256.Sum256(raw)), ValidationState: "valid"}
}

// Successful responses must satisfy the command's contract, not merely be valid JSON.
func TestDemoGovernanceResponseContracts(t *testing.T) {
	id, parent, revision := demoTestUUID(1000), demoTestUUID(1001), demoTestUUID(1002)
	object := func(id string) map[string]any { return map[string]any{"id": id, "tenantId": demoTenant} }
	cases := []struct {
		name, path, actor string
		body, result      map[string]any
		bad               func(map[string]any)
	}{
		{"namespace", "/api/v1/namespaces", "local-admin", map[string]any{"slug": demoID}, map[string]any{"id": id, "tenantId": demoTenant, "slug": demoID}, func(r map[string]any) { r["slug"] = "different" }},
		{"domain", "/api/v1/publishers/" + id + "/publication-domains/" + demoDomain + ":approve", "local-admin", map[string]any{"reason": "Approve"}, map[string]any{"id": id, "tenantId": demoTenant, "approvedPublicationDomains": []string{demoDomain}}, func(r map[string]any) { r["approvedPublicationDomains"] = []string{"other.example.com"} }},
		{"listing", "/api/v1/catalog-listings/" + id + "/revisions", "local-contributor", map[string]any{"displayName": "Example"}, map[string]any{"listing": map[string]any{"id": id, "tenantId": demoTenant, "candidateRevisionId": revision}, "revision": map[string]any{"id": revision, "tenantId": demoTenant, "listingId": id, "displayName": "Example"}}, func(r map[string]any) {
			child, ok := r["revision"].(map[string]any)
			if !ok {
				t.Fatal("missing revision")
			}
			child["displayName"] = "Changed"
		}},
		{"submission", "/api/v1/catalog-listings/" + parent + "/submissions", "local-contributor", map[string]any{}, map[string]any{"id": id, "tenantId": demoTenant, "listingId": parent, "state": "submitted", "submittedBy": "local-contributor"}, func(r map[string]any) { r["state"] = "rejected" }},
		{"approval", "/api/v1/review-submissions/" + id + ":approve", "local-reviewer", map[string]any{"reason": "Review"}, map[string]any{"submission": map[string]any{"id": id, "tenantId": demoTenant, "state": "approved", "versionRevisionId": revision, "decisions": []any{map[string]any{"actor": "local-reviewer", "reason": "Review", "action": "approve"}}}, "version": map[string]any{"id": parent, "tenantId": demoTenant, "sealedRevisionId": revision}}, func(r map[string]any) {
			child, ok := r["submission"].(map[string]any)
			if !ok {
				t.Fatal("missing submission")
			}
			child["decisions"] = []any{}
		}},
		{"release", "/api/v1/catalog-listings/" + parent + ":publish", "local-publisher", map[string]any{"submissionId": revision}, map[string]any{"listing": map[string]any{"id": parent, "tenantId": demoTenant, "currentReleaseId": id}, "release": map[string]any{"id": id, "tenantId": demoTenant, "listingId": parent, "submissionId": revision, "state": "published", "activatedBy": "local-publisher"}}, func(r map[string]any) {
			child, ok := r["release"].(map[string]any)
			if !ok {
				t.Fatal("missing release")
			}
			child["activatedBy"] = "local-contributor"
		}},
		{"snapshot", "/api/v1/publication/snapshots", "local-publisher", map[string]any{"host": "127.0.0.1"}, map[string]any{"id": id, "tenantId": demoTenant, "host": "127.0.0.1", "state": "candidate", "audience": "anonymous"}, func(r map[string]any) { r["state"] = "retired" }},
		{"activation", "/api/v1/publication/snapshots/" + id + ":activate", "local-publisher", map[string]any{"host": "127.0.0.1"}, map[string]any{"id": id, "tenantId": demoTenant, "host": "127.0.0.1", "state": "active", "audience": "anonymous"}, func(r map[string]any) { r["state"] = "candidate" }},
	}
	for _, c := range cases {
		for _, fault := range []string{"valid", "changed", "wrong identity", "null"} {
			t.Run(c.name+"/"+fault, func(t *testing.T) {
				body, err := json.Marshal(c.body)
				if err != nil {
					t.Fatal(err)
				}
				raw, err := json.Marshal(c.result)
				if err != nil {
					t.Fatal(err)
				}
				var result map[string]any
				if err = json.Unmarshal(raw, &result); err != nil {
					t.Fatal(err)
				}
				switch fault {
				case "changed":
					c.bad(result)
				case "wrong identity":
					result = object("not-a-uuid")
				case "null":
					result = nil
				}
				raw, err = json.Marshal(result)
				if err != nil {
					t.Fatal(err)
				}
				err = validateDemoStepResponse(demoStep{Path: c.path, Actor: c.actor, Body: body, Response: raw})
				if (err == nil) != (fault == "valid") {
					t.Fatalf("contract violation %s: %v", fault, err)
				}
			})
		}
	}
}

// Intent collection lookups retain parent IDs and escaped host values before a
// command is journaled. Rejection must happen without an inventory request.
func TestDemoIntentCollectionBindings(t *testing.T) {
	t.Parallel()
	const id = "00000000-0000-7000-8000-000000000041"
	for _, tc := range []struct{ name, path, body, want string }{
		{"namespace", "/api/v1/namespaces", `{}`, "/api/v1/namespaces"},
		{"publisher", "/api/v1/publishers", `{}`, "/api/v1/publishers"},
		{"resource", "/api/v1/resources", `{}`, "/api/v1/resources"},
		{"listing parent", "/api/v1/catalog-listings", `{"resourceId":"` + id + `"}`, "/api/v1/resources/" + id + "/listings"},
		{"snapshot host", "/api/v1/publication/snapshots", `{"host":"catalog.invalid:8443"}`, "/api/v1/publication/snapshots?host=catalog.invalid%3A8443"},
		{"version parent", "/api/v1/resources/" + id + "/versions", `{}`, "/api/v1/resources/" + id + "/versions"},
		{"revision parent", "/api/v1/catalog-listings/" + id + "/revisions", `{}`, "/api/v1/catalog-listings/" + id + "/revisions"},
		{"submission parent", "/api/v1/catalog-listings/" + id + "/submissions", `{}`, "/api/v1/review-submissions?listingId=" + id},
		{"missing listing parent", "/api/v1/catalog-listings", `{}`, ""},
		{"wrong listing parent type", "/api/v1/catalog-listings", `{"resourceId":41}`, ""},
		{"invalid listing parent", "/api/v1/catalog-listings", `{"resourceId":"../private"}`, ""},
		{"invalid submission parent", "/api/v1/catalog-listings/private/submissions", `{}`, ""},
		{"invalid JSON", "/api/v1/namespaces", `{`, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			step := demoStep{Path: tc.path, Body: json.RawMessage(tc.body)}
			got, err := demoStepCollection(step)
			if tc.want != "" {
				if err != nil || got != tc.want {
					t.Fatalf("collection=%q err=%v, want %q", got, err, tc.want)
				}
				return
			}
			want := "seed-demo: unsupported fixed demo intent"
			if tc.name == "invalid JSON" {
				want = "seed-demo: invalid intent"
			}
			if err == nil || err.Error() != want || got != "" {
				t.Fatalf("collection=%q err=%v", got, err)
			}
			if ids, err := demoStepIDs(t.Context(), demoUnitClient(t, nil, false, nil), step); err == nil || ids != nil {
				t.Fatalf("invalid intent inventory=%v err=%v", ids, err)
			}
		})
	}
}

func TestDemoIntentInventoryMatchesOnlyFixedFixture(t *testing.T) {
	t.Parallel()
	const matching = "00000000-0000-7000-8000-000000000041"
	const unrelated = "00000000-0000-7000-8000-000000000042"
	for _, tc := range []struct{ path, field, wanted, other string }{
		{"/api/v1/namespaces", "slug", "owned-demo", "unrelated-demo"},
		{"/api/v1/publishers", "displayName", "Owned publisher", "Unrelated publisher"},
		{"/api/v1/resources", "canonicalIdentifier", "urn:air:example.com:demo:owned", "urn:air:example.com:demo:unrelated"},
	} {
		t.Run(tc.field, func(t *testing.T) {
			body := demoUnitJSON(t, map[string]string{tc.field: tc.wanted})
			steps := []smokeClientStep{
				{name: "inventory", method: "GET", path: tc.path + "?limit=6", token: "local-admin", status: 200, body: `{"items":[{"id":"` + unrelated + `"},{"id":"` + matching + `"}]}`},
				{name: "unrelated", method: "GET", path: tc.path + "/" + unrelated, token: "local-admin", status: 200, body: demoUnitJSON(t, map[string]string{"id": unrelated, "tenantId": demoTenant, tc.field: tc.other})},
				{name: "matching", method: "GET", path: tc.path + "/" + matching, token: "local-admin", status: 200, body: demoUnitJSON(t, map[string]string{"id": matching, "tenantId": demoTenant, tc.field: tc.wanted})},
			}
			ids, err := demoStepIDs(t.Context(), demoUnitClient(t, steps, false, nil), demoStep{Path: tc.path, Body: json.RawMessage(body)})
			if err != nil || len(ids) != 1 || ids[0] != matching {
				t.Fatalf("matches=%v err=%v", ids, err)
			}
		})
	}

	// Parent-scoped collections are already restricted; their IDs must be kept
	// without fetching unrelated per-record name projections.
	for _, tc := range []struct{ path, body, collection string }{
		{"/api/v1/catalog-listings", `{"resourceId":"` + matching + `"}`, "/api/v1/resources/" + matching + "/listings"},
		{"/api/v1/publication/snapshots", `{"host":"catalog.invalid:8443"}`, "/api/v1/publication/snapshots?host=catalog.invalid%3A8443"},
		{"/api/v1/resources/" + matching + "/versions", `{}`, "/api/v1/resources/" + matching + "/versions"},
		{"/api/v1/catalog-listings/" + matching + "/revisions", `{}`, "/api/v1/catalog-listings/" + matching + "/revisions"},
		{"/api/v1/catalog-listings/" + matching + "/submissions", `{}`, "/api/v1/review-submissions?listingId=" + matching},
	} {
		t.Run(tc.path, func(t *testing.T) {
			separator := "?"
			if strings.Contains(tc.collection, "?") {
				separator = "&"
			}
			steps := []smokeClientStep{{name: "parent inventory", method: "GET", path: tc.collection + separator + "limit=6", token: "local-admin", status: 200, body: `{"items":[{"id":"` + unrelated + `"}]}`}}
			ids, err := demoStepIDs(t.Context(), demoUnitClient(t, steps, false, nil), demoStep{Path: tc.path, Body: json.RawMessage(tc.body)})
			if err != nil || len(ids) != 1 || ids[0] != unrelated {
				t.Fatalf("parent inventory=%v err=%v", ids, err)
			}
		})
	}
	for _, path := range []string{"/api/v1/version-revisions/" + matching + ":validate", "/api/v1/review-submissions/" + matching + ":approve", "/api/v1/catalog-listings/" + matching + ":publish", "/api/v1/publication/snapshots/" + matching + ":activate"} {
		if ids, err := demoStepIDs(t.Context(), demoUnitClient(t, nil, false, nil), demoStep{Path: path, Body: json.RawMessage(`{}`)}); err != nil || ids != nil {
			t.Fatalf("transition inventory=%v err=%v", ids, err)
		}
	}
}
