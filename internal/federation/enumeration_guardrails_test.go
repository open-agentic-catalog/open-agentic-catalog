package federation

import (
	"context"
	"crypto/sha256"
	"fmt"
	"github.com/open-agentic-catalog/open-agentic-catalog/test/identityfixture"
	"net/http"
	"reflect"
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/clock"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/fetcher"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/id"
)

func TestEnumerationBindsEntriesToTheirUniqueNestedRetrieval(t *testing.T) {
	t.Parallel()
	const rootURL = "https://catalog.example.com/catalog/root.json"
	const childURL = "https://catalog.example.com/catalog/child.json"
	const canonicalChildURL = "https://catalog.example.com/catalog/canonical/child.json"
	const bounceURL = "https://catalog.example.com/bounce.json"
	const copyURL = "https://catalog.example.com/copy.json"
	child := `{"specVersion":"1.0","entries":[{"identifier":"urn:air:catalog.example.com:default:child-agent","displayName":"Child agent","type":"application/a2a-agent-card+json","data":{"name":"Child agent"}}]}`
	root := `{"entries":[` +
		`{"identifier":"urn:air:catalog.example.com:nested:first","displayName":"First","type":"application/ai-catalog+json","url":"child.json"},` +
		`{"identifier":"urn:air:catalog.example.com:nested:repeat","displayName":"Repeat","type":"application/ai-catalog+json","url":"canonical/child.json"},` +
		`{"identifier":"urn:air:catalog.example.com:nested:bounce","displayName":"Bounce","type":"application/ai-catalog+json","url":"/bounce.json"},` +
		`{"identifier":"urn:air:catalog.example.com:nested:copy","displayName":"Copy","type":"application/ai-catalog+json","url":"/copy.json"},` +
		`{"identifier":"urn:air:catalog.example.com:default:root-agent","displayName":"Root agent","type":"application/a2a-agent-card+json","data":{"name":"Root agent"}}]}`
	childResult := federationResultAt(canonicalChildURL, child)
	childResult.MediaType = aiCatalogDocumentMediaType
	bounceResult := childResult
	bounceResult.FinalURL = rootURL
	copyResult := childResult
	copyResult.FinalURL = copyURL
	requests := &routingFetcher{results: map[string]fetcher.Result{
		"GET " + rootURL:   federationResultAt(rootURL, root),
		"GET " + childURL:  childResult,
		"GET " + bounceURL: bounceResult,
		"GET " + copyURL:   copyResult,
	}}
	manager, err := NewManager(NewMemoryStore(), requests, clock.NewFake(time.Date(2026, time.October, 2, 12, 0, 0, 0, time.UTC)), id.Generator{})
	if err != nil {
		t.Fatal(err)
	}
	// Four fetches suffice for this graph: a repeated canonical child URL
	// consumes no contact, while redirect and content aliases add no members.
	manager.maxFetches = 4
	entries, rootDigest, _, complete, findings, err := manager.enumerate(t.Context(), Source{Endpoint: rootURL, Type: SourceARDManifest, Nesting: NestingPolicy{Enabled: true}})
	wantRootDigest := fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(root)))
	wantChildDigest := fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(child)))
	if err != nil || !complete || rootDigest != wantRootDigest {
		t.Fatalf("nested retrieval complete=%t digest=%q: %v", complete, rootDigest, err)
	}
	want := []struct{ identifier, digest, location string }{
		{"urn:air:catalog.example.com:nested:first", wantRootDigest, "/entries/0"},
		{"urn:air:catalog.example.com:default:child-agent", wantChildDigest, "/entries/0"},
		{"urn:air:catalog.example.com:nested:repeat", wantRootDigest, "/entries/1"},
		{"urn:air:catalog.example.com:nested:bounce", wantRootDigest, "/entries/2"},
		{"urn:air:catalog.example.com:nested:copy", wantRootDigest, "/entries/3"},
		{"urn:air:catalog.example.com:default:root-agent", wantRootDigest, "/entries/4"},
	}
	if len(entries) != len(want) {
		t.Fatalf("nested membership has %d entries, want %d", len(entries), len(want))
	}
	for i, entry := range entries {
		if entry.Identifier != want[i].identifier || entry.RetrievalDigest != want[i].digest || entry.EntryLocation != want[i].location {
			t.Fatalf("entry %d lost retrieval provenance: %+v", i, entry)
		}
	}
	contacted := make([]string, 0, len(requests.requests))
	for _, request := range requests.requests {
		contacted = append(contacted, request.URL)
	}
	if !reflect.DeepEqual(contacted, []string{rootURL, childURL, bounceURL, copyURL}) {
		t.Fatalf("nested traversal contacts = %v", contacted)
	}
	if len(findings) != 3 {
		t.Fatalf("alias diagnostics = %+v", findings)
	}
	for _, finding := range findings {
		if finding.Code != "NESTED_CATALOG_CYCLE" || finding.Severity != catalog.SeverityWarning {
			t.Fatalf("alias changed enumeration completeness: %+v", finding)
		}
	}
}

func TestRegistrySearchEndpointDiscoveryProbeAndFailures(t *testing.T) {
	t.Parallel()
	endpoint := "https://registry.example.com/ard/v1/search"
	fetches := &routingFetcher{results: map[string]fetcher.Result{
		"GET " + endpoint:  {Status: http.StatusMethodNotAllowed, FinalURL: endpoint, MediaType: "text/plain"},
		"POST " + endpoint: {Status: http.StatusOK, FinalURL: endpoint, MediaType: "application/json", Body: []byte(`{"results":[]}`)},
	}}
	manager, err := NewManager(NewMemoryStore(), fetches, clock.NewFake(time.Date(2026, time.August, 30, 12, 0, 0, 0, time.UTC)), id.Generator{})
	if err != nil {
		t.Fatal(err)
	}
	ctx := identityfixture.WithActor(context.Background(), identity.Actor{TenantID: "tenant", Subject: "source-admin", Roles: []identity.Role{identity.RoleSourceAdmin}})
	inspection, err := manager.Inspect(ctx, endpoint)
	if err != nil || inspection.SourceType != SourceRegistry || inspection.RegistrySearchURL != endpoint || !inspection.Capabilities["search"] || inspection.Capabilities["enumerable"] {
		t.Fatalf("Registry probe inspection = %#v, %v", inspection, err)
	}

	for name, result := range map[string]fetcher.Result{
		"upstream status": {Status: http.StatusServiceUnavailable, FinalURL: "https://unavailable.example.com/catalog", MediaType: "text/plain"},
		"incompatible document": {
			Status: http.StatusOK, FinalURL: "https://incompatible.example.com/catalog", MediaType: "application/json", Body: []byte(`{"not":"a catalog"}`),
		},
	} {
		t.Run(name, func(t *testing.T) {
			failedFetcher := &routingFetcher{results: map[string]fetcher.Result{"GET " + result.FinalURL: result}}
			failedManager, managerErr := NewManager(NewMemoryStore(), failedFetcher, clock.NewFake(time.Date(2026, time.August, 30, 12, 0, 0, 0, time.UTC)), id.Generator{})
			if managerErr != nil {
				t.Fatal(managerErr)
			}
			if _, inspectErr := failedManager.Inspect(ctx, result.FinalURL); catalog.ErrorCodeOf(inspectErr) != catalog.CodeInvalidArgument {
				t.Fatalf("failed discovery returned %v", inspectErr)
			}
		})
	}
}

func TestEnumerationBoundsNestedCatalogFailuresWithoutLosingEvidence(t *testing.T) {
	t.Parallel()
	endpoint := "https://catalog.example.com/.well-known/ard.json"
	newManager := func(t *testing.T, results map[string]fetcher.Result) *Manager {
		t.Helper()
		manager, err := NewManager(NewMemoryStore(), &routingFetcher{results: results}, clock.NewFake(time.Date(2026, time.August, 30, 12, 0, 0, 0, time.UTC)), id.Generator{})
		if err != nil {
			t.Fatal(err)
		}
		return manager
	}

	manager := newManager(t, map[string]fetcher.Result{})
	entries, digest, _, complete, findings, err := manager.enumerate(context.Background(), Source{Type: SourceRegistry})
	if catalog.ErrorCodeOf(err) != catalog.CodePreconditionFailed || len(entries) != 0 || digest != "" || complete || len(findings) != 0 {
		t.Fatalf("Registry enumeration = %#v, %q, %t, %#v, %v", entries, digest, complete, findings, err)
	}

	twoEntries := `{"entries":[{"identifier":"urn:air:catalog.example.com:default:one","displayName":"One","type":"application/a2a-agent-card+json","data":{"name":"One"}},{"identifier":"urn:air:catalog.example.com:default:two","displayName":"Two","type":"application/a2a-agent-card+json","data":{"name":"Two"}}]}`
	manager = newManager(t, map[string]fetcher.Result{"GET " + endpoint: federationResultAt(endpoint, twoEntries)})
	manager.maxEntries = 1
	entries, _, _, complete, findings, err = manager.enumerate(context.Background(), Source{Endpoint: endpoint, Type: SourceARDManifest})
	if err != nil || complete || len(entries) != 1 || !hasFinding(findings, "SOURCE_ENTRY_BUDGET_EXHAUSTED") {
		t.Fatalf("entry-bounded enumeration = %d entries, complete=%t, findings=%#v, %v", len(entries), complete, findings, err)
	}

	sharedInline := `{"specVersion":"1.0","entries":[]}`
	nestedFailures := `{"entries":[` +
		`{"identifier":"urn:air:catalog.example.com:nested:both","displayName":"Both","type":"application/ai-catalog+json","url":"https://nested.example.com/both","data":{"specVersion":"1.0","entries":[]}},` +
		`{"identifier":"urn:air:catalog.example.com:nested:malformed","displayName":"Malformed","type":"application/ai-catalog+json","data":{}},` +
		`{"identifier":"urn:air:catalog.example.com:nested:unsafe","displayName":"Unsafe","type":"application/ai-catalog+json","url":"http://nested.example.com/catalog"},` +
		`{"identifier":"urn:air:catalog.example.com:nested:denied","displayName":"Denied","type":"application/ai-catalog+json","url":"https://denied.example.com/catalog"},` +
		`{"identifier":"urn:air:catalog.example.com:nested:inline-one","displayName":"Inline one","type":"application/ai-catalog+json","data":` + sharedInline + `},` +
		`{"identifier":"urn:air:catalog.example.com:nested:inline-two","displayName":"Inline two","type":"application/ai-catalog+json","data":` + sharedInline + `}` +
		`]}`
	manager = newManager(t, map[string]fetcher.Result{"GET " + endpoint: federationResultAt(endpoint, nestedFailures)})
	entries, _, _, complete, findings, err = manager.enumerate(context.Background(), Source{Endpoint: endpoint, Type: SourceARDManifest, Nesting: NestingPolicy{Enabled: true}})
	if err != nil || complete || len(entries) != 6 {
		t.Fatalf("nested failure enumeration = %d entries, complete=%t, findings=%#v, %v", len(entries), complete, findings, err)
	}
	for _, code := range []string{"NESTED_CATALOG_CONTENT_INVALID", "NESTED_CATALOG_URL_INVALID", "NESTED_CATALOG_ORIGIN_DENIED", "NESTED_CATALOG_CYCLE"} {
		if !hasFinding(findings, code) {
			t.Fatalf("nested enumeration omitted %s: %#v", code, findings)
		}
	}

	nestedURL := "https://nested.example.com/catalog"
	rootWithNestedURL := `{"entries":[{"identifier":"urn:air:catalog.example.com:nested:remote","displayName":"Remote","type":"application/ai-catalog+json","url":"` + nestedURL + `"}]}`
	manager = newManager(t, map[string]fetcher.Result{"GET " + endpoint: federationResultAt(endpoint, rootWithNestedURL)})
	manager.maxFetches = 1
	entries, _, _, complete, findings, err = manager.enumerate(context.Background(), Source{Endpoint: endpoint, Type: SourceARDManifest, Nesting: NestingPolicy{Enabled: true, AllowedOrigins: []string{"https://nested.example.com"}}})
	if err != nil || complete || len(entries) != 1 || !hasFinding(findings, "NESTED_CATALOG_INCOMPLETE") {
		t.Fatalf("fetch-bounded enumeration = %d entries, complete=%t, findings=%#v, %v", len(entries), complete, findings, err)
	}

	rootWithoutTransportMetadata := fetcher.Result{Status: http.StatusOK, MediaType: "application/json", Body: []byte(`{"entries":[]}`)}
	manager = newManager(t, map[string]fetcher.Result{"GET " + endpoint: rootWithoutTransportMetadata})
	entries, digest, _, complete, findings, err = manager.enumerate(context.Background(), Source{Endpoint: endpoint, Type: SourceARDManifest})
	if err != nil || !complete || len(entries) != 0 || digest == "" || len(findings) != 0 {
		t.Fatalf("metadata-free enumeration = %#v, %q, %t, %#v, %v", entries, digest, complete, findings, err)
	}
}

func TestCredentialRevisionEvidenceAppliesOnlyToItsExactOrigin(t *testing.T) {
	rootURL := "https://catalog.remote.example/root.json"
	nestedURL := "https://nested.remote.example/catalog.json"
	root := `{"entries":[{"identifier":"urn:air:remote.example:catalog:nested","displayName":"Nested","type":"application/ai-catalog+json","url":"` + nestedURL + `"}]}`
	nested := `{"specVersion":"1.0","entries":[]}`
	rootResult := federationResultAt(rootURL, root)
	rootResult.CredentialRevision = "credential-1"
	nestedResult := federationResultAt(nestedURL, nested)
	nestedResult.MediaType = aiCatalogDocumentMediaType
	manager, err := NewManager(NewMemoryStore(), &routingFetcher{results: map[string]fetcher.Result{
		"GET " + rootURL:   rootResult,
		"GET " + nestedURL: nestedResult,
	}}, clock.NewFake(time.Date(2026, time.August, 31, 12, 0, 0, 0, time.UTC)), id.Generator{})
	if err != nil {
		t.Fatal(err)
	}
	source := Source{TenantID: "00000000-0000-7000-8000-000000000001", Endpoint: rootURL, Type: SourceARDManifest, PolicyID: "test-policy", PolicyRevision: 1, PolicyDigest: federationTestPolicyDigest, CredentialBinding: "partner-reader", Nesting: NestingPolicy{Enabled: true, AllowedOrigins: []string{"https://nested.remote.example"}}}
	_, _, revision, complete, _, err := manager.enumerate(context.Background(), source)
	if err != nil || !complete || revision != "credential-1" {
		t.Fatalf("cross-origin nested credential evidence = revision %q complete %t, %v", revision, complete, err)
	}
}
