package main

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"strings"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/safeerr"
)

// Validate responses against code-owned inputs before saved values can drive the next command.
func validateDemoStepResponse(s demoStep) error {
	fail := safeerr.New("seed-demo: command response does not match fixed fixture input")
	var body, result map[string]any
	if json.Unmarshal(s.Body, &body) != nil || json.Unmarshal(s.Response, &result) != nil || result == nil {
		return fail
	}
	check := func(object map[string]any) bool {
		id, _ := object["id"].(string)
		return identity.ValidTenantID(id) && object["tenantId"] == demoTenant
	}
	switch {
	case s.Path == "/api/v1/namespaces", s.Path == "/api/v1/publishers", s.Path == "/api/v1/resources", s.Path == "/api/v1/catalog-listings":
		if !check(result) {
			return fail
		}
		for field, value := range body {
			if !reflect.DeepEqual(result[field], value) {
				return fail
			}
		}
		if s.Path == "/api/v1/resources" && result["createdBy"] != s.Actor {
			return fail
		}
	case strings.Contains(s.Path, "/publication-domains/"):
		publisherID := strings.SplitN(strings.TrimPrefix(s.Path, "/api/v1/publishers/"), "/", 2)[0]
		if !check(result) || result["id"] != publisherID || !slices.Contains(func() []any { domains, _ := result["approvedPublicationDomains"].([]any); return domains }(), any(strings.TrimSuffix(strings.SplitN(s.Path, "/publication-domains/", 2)[1], ":approve"))) {
			return fail
		}
	case strings.HasSuffix(s.Path, ":validate"):
		revision, _ := result["revision"].(map[string]any)
		evidence, _ := result["evidence"].(map[string]any)
		if !check(revision) || !check(evidence) || revision["id"] != strings.TrimSuffix(strings.TrimPrefix(s.Path, "/api/v1/version-revisions/"), ":validate") || evidence["state"] != "complete" || revision["evidenceSetId"] != evidence["id"] || revision["contentSetDigest"] != evidence["subjectDigest"] || !demoDigestValid(fmt.Sprint(evidence["subjectDigest"])) {
			return fail
		}
	case strings.HasSuffix(s.Path, "/versions"):
		version, _ := result["version"].(map[string]any)
		revision, _ := result["revision"].(map[string]any)
		resourceID := strings.TrimSuffix(strings.TrimPrefix(s.Path, "/api/v1/resources/"), "/versions")
		if !check(version) || !check(revision) || version["resourceId"] != resourceID || version["version"] != body["version"] || version["candidateRevisionId"] != revision["id"] || revision["resourceVersionId"] != version["id"] || revision["createdBy"] != s.Actor {
			return fail
		}
		inputs, _ := body["representations"].([]any)
		actual, _ := revision["representations"].([]any)
		if len(inputs) != 1 || len(actual) != 1 {
			return fail
		}
		input, _ := inputs[0].(map[string]any)
		rep, _ := actual[0].(map[string]any)
		if rep["role"] != input["role"] || rep["mediaType"] != input["mediaType"] || rep["validationState"] != "valid" {
			return fail
		}
		expected, _ := input["expectedDigest"].(string)
		if expected != "" {
			if rep["contentDigest"] != expected || rep["url"] != input["url"] || rep["plugin"] == nil {
				return fail
			}
		} else {
			var raw []byte
			if content, ok := input["content"].(string); ok {
				raw = []byte(content)
			} else if encoded, ok := input["packageData"].(string); ok {
				var err error
				raw, err = base64.StdEncoding.DecodeString(encoded)
				if err != nil || rep["package"] == nil || !reflect.DeepEqual(rep["handoff"], input["handoff"]) {
					return fail
				}
			} else {
				var err error
				raw, err = json.Marshal(input["data"])
				if err != nil {
					return fail
				}
			}
			if rep["contentDigest"] != fmt.Sprintf("sha256:%x", sha256.Sum256(raw)) {
				return fail
			}
		}
	case strings.HasSuffix(s.Path, "/revisions"):
		listing, _ := result["listing"].(map[string]any)
		revision, _ := result["revision"].(map[string]any)
		listingID := strings.TrimSuffix(strings.TrimPrefix(s.Path, "/api/v1/catalog-listings/"), "/revisions")
		if !check(listing) || !check(revision) || listing["id"] != listingID || listing["candidateRevisionId"] != revision["id"] || revision["listingId"] != listing["id"] {
			return fail
		}
		for field, value := range body {
			if field == "extensions" {
				if !demoListingExtensionsMatch(value, revision[field]) {
					return fail
				}
				continue
			}
			if !reflect.DeepEqual(revision[field], value) {
				return fail
			}
		}
	case strings.HasSuffix(s.Path, "/submissions"):
		if !check(result) || result["state"] != "submitted" || result["submittedBy"] != s.Actor || result["listingId"] != strings.TrimSuffix(strings.TrimPrefix(s.Path, "/api/v1/catalog-listings/"), "/submissions") {
			return fail
		}
	case strings.HasSuffix(s.Path, ":approve"):
		submission, _ := result["submission"].(map[string]any)
		version, _ := result["version"].(map[string]any)
		if !check(submission) || !check(version) || submission["state"] != "approved" || submission["id"] != strings.TrimSuffix(strings.TrimPrefix(s.Path, "/api/v1/review-submissions/"), ":approve") || version["sealedRevisionId"] != submission["versionRevisionId"] {
			return fail
		}
		decisions, _ := submission["decisions"].([]any)
		if len(decisions) != 1 {
			return fail
		}
		decision, _ := decisions[0].(map[string]any)
		if decision["actor"] != s.Actor || decision["reason"] != body["reason"] || decision["action"] != "approve" {
			return fail
		}
	case strings.HasSuffix(s.Path, ":publish"):
		listing, _ := result["listing"].(map[string]any)
		release, _ := result["release"].(map[string]any)
		listingID := strings.TrimSuffix(strings.TrimPrefix(s.Path, "/api/v1/catalog-listings/"), ":publish")
		if !check(listing) || !check(release) || listing["id"] != listingID || release["state"] != string(catalog.ReleasePublished) || release["submissionId"] != body["submissionId"] || listing["currentReleaseId"] != release["id"] || release["listingId"] != listing["id"] || release["activatedBy"] != s.Actor {
			return fail
		}
	case s.Path == "/api/v1/publication/snapshots", strings.HasSuffix(s.Path, ":activate"):
		state := "candidate"
		if strings.HasSuffix(s.Path, ":activate") {
			state = "active"
			if result["id"] != strings.TrimSuffix(strings.TrimPrefix(s.Path, "/api/v1/publication/snapshots/"), ":activate") {
				return fail
			}
		}
		if !check(result) || result["host"] != body["host"] || result["state"] != state || result["audience"] != "anonymous" {
			return fail
		}
	default:
		return fail
	}
	return nil
}
