package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/demodata"
)

// Existing-object responses must identify the object addressed by the command,
// even when a different object's same-tenant evidence is internally consistent.
func TestDemoResponseBindsAddressedIdentity(t *testing.T) {
	t.Parallel()
	addressed, other, child := demoTestUUID(3100), demoTestUUID(3101), demoTestUUID(3102)
	for _, operation := range []string{"publisher domain", "listing revision", "publish", "activate"} {
		for _, wrong := range []bool{false, true} {
			t.Run(operation+map[bool]string{false: "/control", true: "/different-object"}[wrong], func(t *testing.T) {
				returned := addressed
				if wrong {
					returned = other
				}
				object := func(id string) map[string]any { return map[string]any{"id": id, "tenantId": demoTenant} }
				step := demoStep{Actor: "local-publisher"}
				body, result := map[string]any{}, object(returned)
				switch operation {
				case "publisher domain":
					step.Actor = "local-admin"
					step.Path = "/api/v1/publishers/" + addressed + "/publication-domains/" + demoDomain + ":approve"
					body["reason"] = "Approve"
					result["approvedPublicationDomains"] = []string{demoDomain}
				case "listing revision":
					step.Actor = "local-contributor"
					step.Path = "/api/v1/catalog-listings/" + addressed + "/revisions"
					body["displayName"] = "Example"
					listing, revision := object(returned), object(child)
					listing["candidateRevisionId"] = child
					revision["listingId"], revision["displayName"] = returned, "Example"
					result = map[string]any{"listing": listing, "revision": revision}
				case "publish":
					step.Path = "/api/v1/catalog-listings/" + addressed + ":publish"
					body["submissionId"] = demoTestUUID(3103)
					listing, release := object(returned), object(child)
					listing["currentReleaseId"] = child
					release["listingId"], release["submissionId"] = returned, body["submissionId"]
					release["state"], release["activatedBy"] = "published", step.Actor
					result = map[string]any{"listing": listing, "release": release}
				case "activate":
					step.Path = "/api/v1/publication/snapshots/" + addressed + ":activate"
					body["host"] = "127.0.0.1"
					result["host"], result["state"], result["audience"] = body["host"], "active", "anonymous"
				}
				step.Body = json.RawMessage(demoUnitJSON(t, body))
				step.Response = json.RawMessage(demoUnitJSON(t, result))
				err := validateDemoStepResponse(step)
				if wrong {
					if err == nil || err.Error() != "seed-demo: command response does not match fixed fixture input" {
						t.Fatalf("different addressed identity accepted or unsafe error: %v", err)
					}
				} else if err != nil {
					t.Fatalf("valid addressed identity refused: %v", err)
				}
			})
		}
	}
}

// Recovery accepts one unfinished final intent, never a gap in completed work
// or an unbounded, ambiguous or differently bound journal. Rejection is read-only.
func TestDemoJournalSafetyAdmission(t *testing.T) {
	t.Parallel()
	for _, fault := range []string{"control", "original control", "pending final", "pending interior", "empty key", "duplicate key", "invalid plan", "foreign original", "too many steps", "oversized file"} {
		t.Run(fault, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "progress.json")
			p := demoProgress{Dataset: expandedDemoID, BaseURL: "http://127.0.0.1:8080", Origin: "https://127.0.0.1:18443", PlanDigest: "sha256:" + strings.Repeat("a", 64), Steps: make([]demoStep, 0, 601)}
			// The exact documented ceiling succeeds before testing limit + 1.
			for i := range 600 {
				step := demoJournalUnitStep(t)
				step.Key = fmt.Sprintf("command-%d", i)
				p.Steps = append(p.Steps, step)
			}
			switch fault {
			case "pending final":
				p.Steps[599].Response = nil
			case "pending interior":
				p.Steps[598].Response = nil
			case "empty key":
				p.Steps[599].Key = ""
			case "duplicate key":
				p.Steps[599].Key = p.Steps[0].Key
			case "invalid plan":
				p.PlanDigest = "sha256:" + strings.Repeat("A", 64)
			case "original control", "foreign original":
				original := demoVerificationReceipt()
				original.BaseURL = p.BaseURL
				if fault == "foreign original" {
					original.BaseURL = "http://127.0.0.1:8081"
				}
				p.Original = &original
			case "too many steps":
				step := demoJournalUnitStep(t)
				step.Key = "command-600"
				p.Steps = append(p.Steps, step)
			}
			raw := []byte(demoUnitJSON(t, p))
			if err := os.WriteFile(path, raw, 0600); err != nil {
				t.Fatal(err)
			}
			if fault == "oversized file" {
				if err := os.Truncate(path, demoProgressLimit+1); err != nil {
					t.Fatal(err)
				}
			}
			got, exists, err := readDemoProgress(path, p.BaseURL, p.Origin)
			if fault == "control" || fault == "original control" || fault == "pending final" {
				if err != nil || !exists || got == nil || len(got.Steps) != 600 || got.index != 0 || !bytes.Equal(got.diskBytes, raw) {
					t.Fatalf("valid recovery checkpoint refused or altered: %v", err)
				}
			} else if err == nil || got != nil || exists || err.Error() != "seed-demo: invalid or mismatched private progress file" {
				t.Fatalf("invalid journal admitted or unsafe error: %v", err)
			}
			if fault != "oversized file" {
				saved, err := remoteReadFile(path, demoProgressLimit)
				if err != nil || !bytes.Equal(saved, raw) {
					t.Fatal("journal admission changed retained bytes")
				}
			} else if info, err := os.Stat(path); err != nil || info.Size() != demoProgressLimit+1 {
				t.Fatal("oversized journal changed during refusal")
			}
		})
	}
}

func TestDemoStateReplacementPreservesUnsafeDestination(t *testing.T) {
	t.Parallel()
	for _, fault := range []string{"control", "symlink", "public mode", "unencodable value"} {
		t.Run(fault, func(t *testing.T) {
			directory := t.TempDir()
			path := filepath.Join(directory, "receipt.json")
			target := filepath.Join(directory, "owned.json")
			before := []byte("{\"state\":\"old\"}\n")
			if err := os.WriteFile(target, before, 0600); err != nil {
				t.Fatal(err)
			}
			if fault == "symlink" {
				if err := os.Symlink(target, path); err != nil {
					t.Fatal(err)
				}
			} else if err := os.WriteFile(path, before, 0600); err != nil {
				t.Fatal(err)
			}
			if fault == "public mode" {
				if err := os.Chmod(path, 0644); err != nil { // #nosec G302 -- deliberately unsafe private-state mode to verify rejection.
					t.Fatal(err)
				}
			}
			var value any = map[string]string{"state": "new"}
			if fault == "unencodable value" {
				value = make(chan int)
			}
			err := replaceDemoFile(path, value, before, true)
			want := before
			if fault == "control" {
				want = []byte("{\"state\":\"new\"}\n")
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil || err.Error() != "seed-demo: cannot safely save private demo state; retain the progress file" {
				t.Fatalf("unsafe replacement admitted or private cause disclosed: %v", err)
			}
			// Read the selected symlink deliberately to assert its target was not overwritten.
			saved, readErr := os.ReadFile(path) // #nosec G304 -- invocation-owned fixture path, including its explicitly created symlink.
			if readErr != nil || !bytes.Equal(saved, want) {
				t.Fatal("replacement changed the wrong retained bytes")
			}
			untouched, readErr := remoteReadFile(target, demoReceiptLimit)
			if readErr != nil || !bytes.Equal(untouched, before) {
				t.Fatal("replacement changed the separate target")
			}
			entries, err := os.ReadDir(directory)
			if err != nil || len(entries) != 2 {
				t.Fatal("replacement leaked its temporary state file")
			}
		})
	}
}

// Native wire forms require exact bytes and package evidence before their
// responses can advance the saved command plan.
func TestDemoVersionResponseBindsNativeWireForms(t *testing.T) {
	t.Parallel()
	for _, form := range []string{"content", "skill package", "plugin archive"} {
		for _, fault := range []string{"control", "digest", "missing package", "handoff or url", "invalid base64", "representation count"} {
			if form == "content" && (fault == "missing package" || fault == "handoff or url" || fault == "invalid base64") || form == "plugin archive" && fault == "invalid base64" {
				continue
			}
			t.Run(form+"/"+fault, func(t *testing.T) {
				resource, version, revision := demoTestUUID(3200), demoTestUUID(3201), demoTestUUID(3202)
				native := []byte("exact retained native bytes\n")
				input := map[string]any{"role": "primary", "mediaType": "text/markdown", "content": string(native)}
				rep := map[string]any{"role": "primary", "mediaType": "text/markdown", "validationState": "valid", "contentDigest": fmt.Sprintf("sha256:%x", sha256.Sum256(native))}
				if form != "content" {
					delete(input, "content")
					input["mediaType"], rep["mediaType"] = "application/zip", "application/zip"
					if form == "skill package" {
						input["packageData"] = base64.StdEncoding.EncodeToString(native)
						input["handoff"] = map[string]any{"git": map[string]any{"repository": "https://example.com/skills.git", "commit": strings.Repeat("a", 40), "directory": "skills/example"}}
						rep["package"], rep["handoff"] = map[string]any{"fileCount": 1}, input["handoff"]
					} else {
						input["expectedDigest"], input["url"] = rep["contentDigest"], "https://example.com/plugin.zip"
						rep["url"], rep["plugin"] = input["url"], map[string]any{"files": []any{map[string]any{"path": "plugin.json"}}}
					}
				}
				switch fault {
				case "digest":
					rep["contentDigest"] = "sha256:" + strings.Repeat("b", 64)
				case "missing package":
					delete(rep, "package")
					delete(rep, "plugin")
				case "handoff or url":
					rep["handoff"], rep["url"] = map[string]any{}, "https://example.com/different.zip"
				case "invalid base64":
					input["packageData"] = "not-base64!"
				}
				actual := []any{rep}
				if fault == "representation count" {
					actual = append(actual, rep)
				}
				body := map[string]any{"version": "1.0.0", "representations": []any{input}}
				result := map[string]any{
					"version":  map[string]any{"id": version, "tenantId": demoTenant, "resourceId": resource, "version": "1.0.0", "candidateRevisionId": revision},
					"revision": map[string]any{"id": revision, "tenantId": demoTenant, "resourceVersionId": version, "createdBy": "local-contributor", "representations": actual},
				}
				err := validateDemoStepResponse(demoStep{Path: "/api/v1/resources/" + resource + "/versions", Actor: "local-contributor", Body: []byte(demoUnitJSON(t, body)), Response: []byte(demoUnitJSON(t, result))})
				if (err == nil) != (fault == "control") {
					t.Fatalf("native response contract %s/%s: %v", form, fault, err)
				}
			})
		}
	}
}

func TestDemoArchivePreflightTrustAdmission(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "trust.pem")
	// Disabled archive work must not need an origin, trust file or provider.
	if err := preflightDemoArchives(t.Context(), demodata.Plan{}, "", path); err != nil {
		t.Fatal("archive-free plan required trust", err)
	}
	plan := demodata.Plan{Resources: []demodata.PreparedResource{{Resource: demodata.Resource{Key: "example", Kind: catalog.KindPlugin}, Archive: []byte("archive"), BodyDigest: "sha256:" + strings.Repeat("a", 64)}}}
	for _, trust := range []string{"missing", "malformed"} {
		t.Run(trust, func(t *testing.T) {
			if trust == "malformed" {
				if err := os.WriteFile(path, []byte("private-unparseable-trust-data"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			err := preflightDemoArchives(t.Context(), plan, "https://127.0.0.1:18443", path)
			if err == nil || err.Error() != "seed-demo: archive preflight failed; verify private trust, readiness and unchanged prepared bytes before any mutation" {
				t.Fatalf("unsafe trust admitted or private cause disclosed: %v", err)
			}
		})
	}
}
