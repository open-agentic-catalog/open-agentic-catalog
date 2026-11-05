//go:build functional

// Functional tests exercise local HTTP or composed service workflows.
package httpserver

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/federation"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/clock"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/fetcher"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/id"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/publication"
)

type countingARDSearchFetcher struct {
	responses routedFetcher
	calls     int
	requests  []fetcher.Request
	page      func(fetcher.Request, fetcher.Result) fetcher.Result
}

func (fetches *countingARDSearchFetcher) Fetch(ctx context.Context, request fetcher.Request) (fetcher.Result, error) {
	fetches.calls++
	fetches.requests = append(fetches.requests, request)
	result, err := fetches.responses.Fetch(ctx, request)
	if err == nil && fetches.page != nil {
		result = fetches.page(request, result)
	}
	return result, err
}

func (fetches *countingARDSearchFetcher) PolicyDigest() string {
	return fetches.responses.PolicyDigest()
}

func TestARDAutoSearchTraversesEveryLocalResultAndQueriesLiveOnce(t *testing.T) {
	const tenantID = "00000000-0000-7000-8000-000000000001"
	const discoveryHost = "127.0.0.1"
	const registryURL = "https://remote.example/ard/v1/search"
	now := time.Date(2026, time.September, 2, 12, 0, 0, 0, time.UTC)
	records := make([]catalog.CatalogRecord, 0, 3)
	for index, name := range []string{"alpha", "beta", "gamma"} {
		records = append(records, conformingARDSearchRecord(tenantID, name, index, now))
	}
	cursorKey := sha256.Sum256([]byte("ARD auto-search conformance cursor"))
	publicationManager, err := publication.NewManager(publication.Options{
		Records:          fixedPublicationRecordSource{tenantID: tenantID, records: records},
		Store:            publication.NewMemoryStore(),
		Clock:            clock.NewFake(now),
		IDs:              id.Generator{},
		BaseURL:          "https://127.0.0.1",
		PublisherDomain:  "catalog.example.com",
		ARDProfile:       "ard-0.91-aa3e598",
		AICatalogProfile: "ai-catalog-1.0-b062278",
		CursorKey:        cursorKey[:],
		Host:             discoveryHost,
		Audience:         publication.AudienceAnonymous,
	})
	if err != nil {
		t.Fatal(err)
	}
	candidate, err := publicationManager.BuildCandidate(publicationFixtureContext(t, tenantID), tenantID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := publicationManager.Activate(publicationFixtureContext(t, tenantID), tenantID, candidate.ID, "Activate reviewed snapshot"); err != nil {
		t.Fatal(err)
	}
	remoteID := "urn:air:remote.example:agent:resumable"
	remoteResponse := `{"results":[` +
		`{"identifier":"` + remoteID + `","version":"1.0.0","displayName":"Remote resumable agent","type":"application/a2a-agent-card+json","url":"https://agents.remote.example/card.json","score":100,"source":"https://index.example/ard/v1/"},` +
		`{"identifier":"urn:air:catalog.example.com:agent:gamma","version":"1.0.0","displayName":"Spoofed local gamma","type":"application/a2a-agent-card+json","url":"https://agents.remote.example/spoof.json","score":99,"source":"https://index.example/ard/v1/"}` +
		`],"pageToken":"upstream-has-more"}`
	policyDigest := "sha256:" + strings.Repeat("a", 64)
	fetches := &countingARDSearchFetcher{responses: routedFetcher{
		http.MethodPost + " " + registryURL: {Status: http.StatusOK, FinalURL: registryURL, MediaType: "application/json", Body: []byte(remoteResponse), PolicyID: "test-policy", PolicyRevision: 1, PolicyDigest: policyDigest},
	}}
	federationStore := federation.NewMemoryStore()
	source := federation.Source{
		ID: "00000000-0000-7000-8000-000000000010", TenantID: tenantID, DisplayName: "Remote Registry", Type: federation.SourceRegistry,
		Endpoint: registryURL, RegistrySearchURL: registryURL, Status: federation.SourceActive, ProfileID: "ard-0.91-aa3e598", RetrievalMode: federation.RetrievalLive,
		Scope: federation.Scope{Kinds: []catalog.Kind{catalog.KindAgent}}, UpdateMode: federation.UpdateManual,
		Live:                 federation.LivePolicy{Enabled: true, QueryPrivacyAcknowledged: true, AnonymousDisclosureAcknowledged: true, FederationMode: "none"},
		DiscoveryAssignments: []federation.DiscoveryAssignment{{Host: discoveryHost, Audience: federation.AudienceAnonymous}},
		PolicyID:             "test-policy", PolicyRevision: 1, PolicyDigest: policyDigest, Revision: 1, CreatedBy: "test", CreatedAt: now, UpdatedAt: now,
	}
	if _, err := federationStore.CreateSource(context.Background(), source); err != nil {
		t.Fatal(err)
	}
	federationManager, err := federation.NewManager(federationStore, fetches, clock.NewFake(now), id.Generator{}, federation.WithDiscoveryTargets([]federation.DiscoveryTarget{{Host: discoveryHost, TenantID: tenantID, Audience: federation.AudienceAnonymous}}))
	if err != nil {
		t.Fatal(err)
	}
	server := &Server{now: time.Now,
		federation: federationManager,
		discovery: map[string]DiscoveryHost{discoveryHost: {
			Host: discoveryHost, TenantID: tenantID, Mode: DiscoveryAnonymous, PublisherDomain: "catalog.example.com", Publication: publicationManager,
		}},
		maxBodyBytes: 1 << 20,
		logger:       slog.New(slog.DiscardHandler),
	}

	requestBody := publication.SearchRequest{Query: publication.SearchQuery{Text: "resumable"}, Federation: "auto", PageSize: 2}
	first := callARDSearch(t, server, requestBody)
	requireStatus(t, first, http.StatusOK)
	requireControlSchema(t, "ARDSearchResponse", first.body)
	var firstPage publication.SearchResponse
	decodeResult(t, first, &firstPage)
	if len(firstPage.Results) != 2 || firstPage.Results[0].Identifier != remoteID || firstPage.Results[0].Source != "https://index.example/ard/v1/" || firstPage.Results[1].Identifier != "urn:air:catalog.example.com:agent:alpha" || firstPage.PageToken == "" {
		t.Fatalf("first auto Search page = %#v", firstPage)
	}
	if first.header.Get("Server-Timing") != `federation;desc="partial"` || strings.Contains(string(first.body), "diagnostics") || strings.Contains(string(first.body), "partial") {
		t.Fatalf("partial live evidence leaked into the ARD response: headers=%#v body=%s", first.header, first.body)
	}

	requestBody.PageToken = firstPage.PageToken
	second := callARDSearch(t, server, requestBody)
	requireStatus(t, second, http.StatusOK)
	requireControlSchema(t, "ARDSearchResponse", second.body)
	var secondPage publication.SearchResponse
	decodeResult(t, second, &secondPage)
	if len(secondPage.Results) != 2 || secondPage.PageToken != "" {
		t.Fatalf("second auto Search page = %#v", secondPage)
	}
	seenLocal := make(map[string]int)
	for _, result := range append(append([]publication.SearchResult(nil), firstPage.Results...), secondPage.Results...) {
		if strings.HasPrefix(result.Identifier, "urn:air:catalog.example.com:agent:") {
			seenLocal[result.Identifier]++
		}
	}
	for _, name := range []string{"alpha", "beta", "gamma"} {
		identifier := "urn:air:catalog.example.com:agent:" + name
		if seenLocal[identifier] != 1 {
			t.Fatalf("local result %q appeared %d times across auto Search pages", identifier, seenLocal[identifier])
		}
	}
	if secondPage.Results[1].DisplayName == "Spoofed local gamma" {
		t.Fatal("remote collision replaced a governed local result outside the first local page")
	}
	if fetches.calls != 1 {
		t.Fatalf("auto Search contacted live federation %d times, want exactly once", fetches.calls)
	}

	requestBody.PageToken += "x"
	tampered := callARDSearch(t, server, requestBody)
	requireStatus(t, tampered, http.StatusBadRequest)
	if fetches.calls != 1 {
		t.Fatalf("invalid continuation contacted live federation; calls = %d", fetches.calls)
	}

	failedFetches := &countingARDSearchFetcher{responses: routedFetcher{}}
	failedFederation, err := federation.NewManager(federationStore, failedFetches, clock.NewFake(now), id.Generator{}, federation.WithDiscoveryTargets([]federation.DiscoveryTarget{{Host: discoveryHost, TenantID: tenantID, Audience: federation.AudienceAnonymous}}))
	if err != nil {
		t.Fatal(err)
	}
	server.federation = failedFederation
	requestBody.PageToken = ""
	failedFirst := callARDSearch(t, server, requestBody)
	requireStatus(t, failedFirst, http.StatusOK)
	var failedFirstPage publication.SearchResponse
	decodeResult(t, failedFirst, &failedFirstPage)
	if len(failedFirstPage.Results) != 2 || failedFirstPage.PageToken == "" || failedFirst.header.Get("Server-Timing") != `federation;desc="partial"` {
		t.Fatalf("failed live stage did not retain a usable local first page: headers=%#v body=%s", failedFirst.header, failedFirst.body)
	}
	requestBody.PageToken = failedFirstPage.PageToken
	failedSecond := callARDSearch(t, server, requestBody)
	requireStatus(t, failedSecond, http.StatusOK)
	var failedSecondPage publication.SearchResponse
	decodeResult(t, failedSecond, &failedSecondPage)
	if len(failedSecondPage.Results) != 1 || failedSecondPage.PageToken != "" || failedFetches.calls != 1 {
		t.Fatalf("failed live stage continuation = %#v, live calls = %d", failedSecondPage, failedFetches.calls)
	}
}

func callARDSearch(t *testing.T, server *Server, value publication.SearchRequest) httpResult {
	t.Helper()
	body, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "http://127.0.0.1/ard/v1/search", strings.NewReader(string(body)))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	server.handleARDSearch(response, request)
	return httpResult{status: response.Code, header: response.Header(), body: response.Body.Bytes()}
}
