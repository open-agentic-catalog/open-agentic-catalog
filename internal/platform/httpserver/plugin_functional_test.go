//go:build functional

package httpserver

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/x509"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/adapter"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/federation"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/jobs"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/clock"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/config"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/fetcher"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/id"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/idempotency"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/publication"
)

// The test-only composition admits exactly its owned TLS origin and loopback.
// Production archive requests retain the anonymous public routing boundary.
type pluginHTTPFetcher struct {
	outbound *fetcher.Fetcher
	now      func() time.Time
}

func (*pluginHTTPFetcher) PolicyDigest() string { return "sha256:" + strings.Repeat("b", 64) }
func (f *pluginHTTPFetcher) FetchExternalRepresentation(ctx context.Context, request catalog.ExternalRepresentationFetchRequest) (catalog.ExternalRepresentationFetchResult, error) {
	r, err := f.outbound.Fetch(ctx, fetcher.Request{URL: request.URL, IdentityEncoding: true, RejectOriginChanges: true, MaxResponseBytes: request.MaxResponseBytes, Accept: []string{request.MediaType}})
	if err != nil {
		return catalog.ExternalRepresentationFetchResult{}, err
	}
	return catalog.ExternalRepresentationFetchResult{RequestedURL: request.URL, FinalURL: r.FinalURL, Status: r.Status, MediaType: r.MediaType, Body: r.Body, ContentDigest: r.BodyDigest, PolicyID: "plugin-test", PolicyRevision: 1, PolicyDigest: f.PolicyDigest(), RetrievedAt: f.now()}, nil
}

func TestHTTPPluginArchiveContractReplayAndDisclosure(t *testing.T) {
	var archive bytes.Buffer
	writer := zip.NewWriter(&archive)
	for _, file := range []struct{ name, content string }{
		{"plugin.json", `{"$schema":"https://agent-plugins.org/schemas/1.0.0/plugin.schema.json","name":"http-tools"}`},
		{"mcp.json", `{"$schema":"https://agent-plugins.org/schemas/1.0.0/mcp.schema.json","mcpServers":{"tools":{"type":"stdio","command":"node"}}}`},
		{"skills/read/SKILL.md", "---\nname: read\ndescription: Read only metadata.\n---\nInstructions."},
	} {
		w, err := writer.Create(file.name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = w.Write([]byte(file.content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	origin := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/plugin.zip" {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" || r.Header.Get("Accept-Encoding") != "identity" {
			t.Error("archive request leaked credentials or negotiated decoding")
		}
		w.Header().Set("Content-Type", "application/zip")
		_, _ = w.Write(archive.Bytes())
	}))
	defer origin.Close()
	roots := x509.NewCertPool()
	roots.AddCert(origin.Certificate())
	outbound, err := fetcher.New(fetcher.Policy{Timeout: 10 * time.Second, MaxResponseBytes: catalog.MaxSkillPackageBytes, MaxRedirects: 2, AllowTestAddresses: true, AllowedOrigins: map[string]struct{}{origin.URL: {}}, AllowedAddressPrefixes: []netip.Prefix{netip.MustParsePrefix("127.0.0.1/32")}, RootCAs: roots}, nil)
	if err != nil {
		t.Fatal(err)
	}
	c := clock.NewFake(time.Date(2026, 9, 12, 0, 0, 0, 0, time.UTC))
	key := sha256.Sum256([]byte(t.Name()))
	service, err := catalog.NewService(catalog.Options{Repository: catalog.NewMemoryRepository(), Clock: c, IDs: id.Generator{}, Validator: adapter.NewRegistry(), ExternalFetcher: &pluginHTTPFetcher{outbound: outbound, now: c.Now}, CursorKey: key[:], Policy: "plugin-test"})
	if err != nil {
		t.Fatal(err)
	}
	f, err := federation.NewManager(federation.NewMemoryStore(), unavailableFetcher{}, c, id.Generator{})
	if err != nil {
		t.Fatal(err)
	}
	j, err := jobs.NewService(jobs.NewMemoryStore(), c, id.Generator{})
	if err != nil {
		t.Fatal(err)
	}
	p, err := publication.NewManager(publication.Options{Records: service, Store: publication.NewMemoryStore(), Clock: c, IDs: id.Generator{}, BaseURL: "https://catalog.example.com", PublisherDomain: "catalog.example.com", ARDProfile: "ard-test", AICatalogProfile: "ai-test", CursorKey: key[:]})
	if err != nil {
		t.Fatal(err)
	}
	auth, identities := newTestIdentity(t, c, map[string]config.DevelopmentUser{"admin": {Subject: "admin", TenantID: "00000000-0000-7000-8000-000000000001", Roles: []identity.Role{identity.RoleTenantAdmin}}, "other-admin": {Subject: "other", TenantID: "00000000-0000-7000-8000-000000000002", Roles: []identity.Role{identity.RoleTenantAdmin}}, "reader": {Subject: "reader", TenantID: "00000000-0000-7000-8000-000000000001", Roles: []identity.Role{identity.RoleReader}}})
	// Exercise archive rejection and replay within the normal service budget.
	// The large encoded-overflow case is not a two-second latency assertion.
	server, err := New(Dependencies{Catalog: service, Identity: identities, Federation: f, Jobs: j, Authenticator: auth, Idempotency: idempotency.NewMemoryStore(), Logger: slog.New(slog.DiscardHandler), DiscoveryHosts: []DiscoveryHost{anonymousDiscoveryHost(p)}, MaxBodyBytes: 1 << 20, RequestTimeout: config.Defaults().HTTP.RequestTimeout.Duration, CursorKey: key[:]})
	if err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(server.Handler())
	defer httpServer.Close()
	post := func(path, key string, body any) httpResult {
		return requestJSON(t, httpServer.URL, http.MethodPost, path, "admin", key, "", body)
	}
	requireControlSchema(t, "CatalogExploreQuery", []byte(`{"kinds":["agent","mcp_server","skill","plugin"]}`))
	requireControlSchema(t, "ObservationSearch", []byte(`{"kinds":["agent","mcp_server","skill","plugin"]}`))
	observationSearch := post("/api/v1/observations:search", "four-kinds", map[string]any{"kinds": []string{"agent", "mcp_server", "skill", "plugin"}})
	requireStatus(t, observationSearch, http.StatusOK)
	requireControlSchema(t, "ObservationSearchPage", observationSearch.body)
	nsResponse := post("/api/v1/namespaces", "namespace", map[string]any{"slug": "plugins", "displayName": "Plugins"})
	requireStatus(t, nsResponse, 201)
	var ns catalog.Namespace
	decodeResult(t, nsResponse, &ns)
	resourceResponse := post("/api/v1/resources", "resource", map[string]any{"namespaceId": ns.ID, "canonicalIdentifier": "urn:air:catalog.example.com:plugins:http-tools", "kind": "plugin"})
	requireStatus(t, resourceResponse, 201)
	var resource catalog.Resource
	decodeResult(t, resourceResponse, &resource)
	path := "/api/v1/resources/" + resource.ID + "/versions"
	for name, invalidDigest := range map[string]any{"null": nil, "empty": "", "malformed": "bad", "number": 42} {
		invalid := post(path, "invalid-digest-"+name, map[string]any{"version": "invalid", "representations": []map[string]any{{"role": "primary", "mediaType": catalog.PluginMediaZIP, "url": origin.URL + "/plugin.zip", "expectedDigest": invalidDigest}}})
		requireStatus(t, invalid, http.StatusBadRequest)
	}
	body := map[string]any{"version": "1.0.0", "representations": []map[string]any{{"role": "primary", "mediaType": catalog.PluginMediaZIP, "url": origin.URL + "/plugin.zip"}}}
	created := post(path, "version", body)
	requireStatus(t, created, 201)
	requireControlSchema(t, "VersionEvidenceEnvelope", created.body)
	var envelope struct {
		Revision catalog.VersionRevision `json:"revision"`
		Evidence catalog.EvidenceSet     `json:"evidence"`
	}
	decodeResult(t, created, &envelope)
	if envelope.Evidence.State != catalog.EvidenceComplete || len(envelope.Revision.Representations[0].Plugin.Components) != 2 {
		t.Fatalf("Plugin evidence: %+v", envelope)
	}
	replay := post(path, "version", body)
	requireStatus(t, replay, 201)
	if !bytes.Equal(created.body, replay.body) {
		t.Fatal("idempotent replay changed Plugin evidence")
	}
	for _, token := range []string{"", "other-admin"} {
		response := requestJSON(t, httpServer.URL, http.MethodGet, "/api/v1/version-revisions/"+envelope.Revision.ID, token, "", "", nil)
		if response.status == http.StatusOK {
			t.Fatal("private inspection disclosed")
		}
	}
	denied := requestJSON(t, httpServer.URL, http.MethodPost, path, "reader", "reader-write", "", body)
	requireStatus(t, denied, http.StatusForbidden)
	again := post("/api/v1/version-revisions/"+envelope.Revision.ID+":validate", "validate", map[string]any{})
	requireStatus(t, again, http.StatusCreated)
	requireControlSchema(t, "RevisionEvidenceEnvelope", again.body)

	// Valid UTF-8 can expand sixfold when retained inside JSON. Serve an
	// uncompressed ZIP so this tests the storage budget, not archive ratios.
	archive.Reset()
	writer = zip.NewWriter(&archive)
	for i := -1; i < 8; i++ {
		name, content := "plugin.json", `{"$schema":"https://agent-plugins.org/schemas/1.0.0/plugin.schema.json","name":"http-tools"}`
		if i >= 0 {
			name = fmt.Sprintf("skills/skill-%d/SKILL.md", i)
			content = fmt.Sprintf("---\nname: skill-%d\ndescription: Synthetic metadata.\n---\n", i) + strings.Repeat("\x01", 450_000)
		}
		w, writeErr := writer.CreateHeader(&zip.FileHeader{Name: name, Method: zip.Store})
		if writeErr != nil {
			t.Fatal(writeErr)
		}
		if _, writeErr = w.Write([]byte(content)); writeErr != nil {
			t.Fatal(writeErr)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	body["version"] = "oversized"
	rejected := post(path, "encoded-overflow", body)
	requireStatus(t, rejected, http.StatusUnprocessableEntity)
	rejectedReplay := post(path, "encoded-overflow", body)
	requireStatus(t, rejectedReplay, http.StatusUnprocessableEntity)
	if strings.Contains(string(rejected.body), "\\u0001") || strings.Contains(string(rejected.body), "postgres") {
		t.Fatal("rejection disclosed document or database details")
	}
	versions := requestJSON(t, httpServer.URL, http.MethodGet, path, "admin", "", "", nil)
	requireStatus(t, versions, http.StatusOK)
	var page struct {
		Items []catalog.ResourceVersion `json:"items"`
	}
	decodeResult(t, versions, &page)
	if len(page.Items) != 1 || page.Items[0].Version != "1.0.0" {
		t.Fatal("encoded overflow left partial version state")
	}
}
