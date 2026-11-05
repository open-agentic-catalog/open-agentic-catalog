package main

import (
	"encoding/json"
	"net/netip"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
	sdk "github.com/open-agentic-catalog/open-agentic-catalog/sdk/go"
)

func TestMappingAndCredentialInputs(t *testing.T) {
	config := configuration{Resources: []mapping{testMapping}}
	path := writePrivate(t, "mapping.json", fixtureJSON(config))
	loaded, digest, err := loadConfiguration(path)
	if err != nil || len(loaded.Resources) != 1 || !hashPattern.MatchString(digest) {
		t.Fatal("valid mapping refused")
	}
	for _, invalid := range []string{
		`{"resources":null}`, `{"resources":[]}`, `{"Resources":[]}`,
		`{"resources":[],"resources":[]}`,
		strings.Replace(string(fixtureJSON(config)), `"resourceId":`, `"ResourceId":`, 1),
		strings.Replace(string(fixtureJSON(config)), `"/private/mcp-token"`, `"relative-token"`, 1),
	} {
		name := writePrivate(t, "bad.json", []byte(invalid))
		if _, _, err := loadConfiguration(name); err == nil {
			t.Fatal("invalid mapping accepted")
		}
	}
	duplicate := configuration{Resources: []mapping{testMapping, testMapping}}
	if _, _, err := loadConfiguration(writePrivate(t, "duplicate.json", fixtureJSON(duplicate))); err == nil {
		t.Fatal("ambiguous mapping accepted")
	}
	token := writePrivate(t, "token", []byte("opaque-value\n"))
	if _, err = readToken(t.Context(), token); err != nil {
		t.Fatal("valid private token refused")
	}
	// #nosec G302 -- Deliberately public fixture verifies credential refusal.
	if os.Chmod(token, 0644) != nil {
		t.Fatal("fixture mode")
	}
	if _, err = readToken(t.Context(), token); err == nil {
		t.Fatal("public token file accepted")
	}
	link := token + "-link"
	if os.Symlink(token, link) != nil {
		t.Fatal("fixture link")
	}
	if _, err = privateFile(link, 16384); err == nil {
		t.Fatal("credential symlink followed")
	}
}

func TestOnlyConfiguredHTTPSAndPrivateAddressesAreEligible(t *testing.T) {
	if validateEndpoint(testMapping.Endpoint) != nil {
		t.Fatal("valid endpoint refused")
	}
	for _, value := range []string{"http://127.0.0.1/mcp", "https://user:pass@127.0.0.1/mcp", "https://127.0.0.1/mcp?token=x", "https://127.0.0.1/mcp#fragment", "https://127.0.0.1/a/../mcp", "https://127.0.0.1", "https://127.0.0.1/" + strings.Repeat("a", 1024)} {
		if validateEndpoint(value) == nil {
			t.Fatal("invalid endpoint accepted")
		}
	}
	for _, value := range []string{"127.0.0.1", "10.2.3.4", "172.16.0.1", "192.168.0.1", "::1", "fd00::1"} {
		if !localAddress(netip.MustParseAddr(value)) {
			t.Fatal("local address refused")
		}
	}
	for _, value := range []string{"0.0.0.0", "8.8.8.8", "169.254.169.254", "224.0.0.1", "::", "fe80::1", "2001:4860:4860::8888"} {
		if localAddress(netip.MustParseAddr(value)) {
			t.Fatal("nonlocal address accepted")
		}
	}
}

func TestExactCatalogReferenceValidation(t *testing.T) {
	valid := recordFixture()
	if _, _, err := validateRecord(valid, release, tenant, []mapping{testMapping}, fixed); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*sdk.CatalogRecord){
		func(r *sdk.CatalogRecord) { r.Source = "indexed" },
		func(r *sdk.CatalogRecord) { r.Resource.Kind = "skill" },
		func(r *sdk.CatalogRecord) { r.Resource.TenantId = uuid.MustParse(resource) },
		func(r *sdk.CatalogRecord) { r.Listing.NamespaceId = uuid.MustParse(resource) },
		func(r *sdk.CatalogRecord) { r.Release.VersionRevisionId = uuid.Nil },
		func(r *sdk.CatalogRecord) { r.Listing.CurrentReleaseId = nil },
		func(r *sdk.CatalogRecord) { r.Release.State = "revoked" },
		func(r *sdk.CatalogRecord) { r.VersionRevision.Representations[0].ValidationState = "invalid" },
		func(r *sdk.CatalogRecord) { r.VersionRevision.ContentSetDigest = "invalid" },
		func(r *sdk.CatalogRecord) {
			(*r.VersionRevision.Representations[0].Data)["remotes"] = []any{map[string]any{"type": "streamable-http", "url": "https://127.0.0.1:1/unapproved"}}
		},
	} {
		candidate := recordFixture()
		change(&candidate)
		if _, _, err := validateRecord(candidate, release, tenant, []mapping{testMapping}, fixed); err == nil {
			t.Fatal("changed binding accepted")
		}
	}
	draft := uuid.MustParse(eventID)
	valid.Version.CandidateRevisionId = &draft
	if _, _, err := validateRecord(valid, release, tenant, []mapping{testMapping}, fixed); err != nil {
		t.Fatal("new draft replaced immutable release")
	}
}

func fixtureObject(value any) map[string]any {
	m, ok := value.(map[string]any)
	if !ok {
		panic("invalid fixture object")
	}
	return m
}

func TestScanReportBindingsAndCompletionEvidence(t *testing.T) {
	c := captureFixture()
	original := reportFixture(c, testIdentity)
	if validateScan(original, c, testIdentity, 0) != nil {
		t.Fatal("valid report refused")
	}
	for _, mutate := range []func(map[string]any){
		func(r map[string]any) { r["callerContext"] = map[string]any{"catalogVerified": true} },
		func(r map[string]any) { r["contextSha256"] = strings.Repeat("0", 64) },
		func(r map[string]any) { r["plannedTools"] = 2 },
		func(r map[string]any) { r["completedTools"] = 0 },
		func(r map[string]any) {
			capture := fixtureObject(r["capture"])
			capture["inputSha256"] = strings.Repeat("0", 64)
		},
		func(r map[string]any) { capture := fixtureObject(r["capture"]); delete(capture, "hasNextCursor") },
		func(r map[string]any) { r["rulesSha256"] = strings.Repeat("0", 64) },
		func(r map[string]any) { r["results"] = nil },
	} {
		var value map[string]any
		if json.Unmarshal(original, &value) != nil {
			t.Fatal("fixture decode")
		}
		mutate(value)
		if validateScan(fixtureJSON(value), c, testIdentity, 0) == nil {
			t.Fatal("invalid report accepted")
		}
	}
	if validateScan(original, c, testIdentity, 2) == nil {
		t.Fatal("failing exit claimed complete")
	}
	partial := c
	partial.Complete = false
	var doc map[string]any
	if json.Unmarshal(partial.Document, &doc) != nil {
		t.Fatal("fixture decode")
	}
	doc["nextCursor"] = "unread"
	partial.Document = fixtureJSON(doc)
	partial.Digest = sha(partial.Document)
	if validateScan(reportFixture(partial, testIdentity), partial, testIdentity, 2) != nil {
		t.Fatal("partial capture refused")
	}
}

func TestEmptyCaptureCannotClaimCompletedAnalysis(t *testing.T) {
	c := captureFixture()
	c.Document = json.RawMessage(`{"tools":[]}`)
	c.Digest = sha(c.Document)
	var report map[string]any
	if json.Unmarshal(reportFixture(c, testIdentity), &report) != nil {
		t.Fatal("fixture decode")
	}
	report["plannedTools"], report["completedTools"] = 0, 0
	report["results"] = []any{}
	report["status"] = "skipped"
	if validateScan(fixtureJSON(report), c, testIdentity, 2) != nil {
		t.Fatal("explicit empty-work outcome refused")
	}
	report["status"] = "completed"
	if validateScan(fixtureJSON(report), c, testIdentity, 0) == nil {
		t.Fatal("empty capture claimed completed analysis")
	}
}
