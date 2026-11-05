// Unit tests exercise isolated validation and controlled dependencies.
package httpserver

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/federation"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/publication"
)

type fixedPublicationRecordSource struct {
	tenantID string
	records  []catalog.CatalogRecord
}

func (source fixedPublicationRecordSource) PublicationRecords(_ context.Context, tenantID string, _ bool, limit int) ([]catalog.CatalogRecord, bool, error) {
	if tenantID != source.tenantID {
		return nil, false, nil
	}
	if len(source.records) > limit {
		return nil, true, nil
	}
	return append([]catalog.CatalogRecord(nil), source.records...), false, nil
}

func TestCapabilitySnapshotDefaultsAndRejectsInconsistentWiring(t *testing.T) {
	snapshot, err := normalizeCapabilities(CapabilitySnapshot{}, false)
	if err != nil || snapshot.SemanticSearch != CapabilityUnavailable || snapshot.Webhooks != CapabilityDisabled {
		t.Fatalf("default capability snapshot = %#v, %v", snapshot, err)
	}
	if _, err := normalizeCapabilities(CapabilitySnapshot{SemanticSearch: "unknown"}, false); err == nil {
		t.Fatal("unknown capability state was accepted")
	}
	if _, err := normalizeCapabilities(CapabilitySnapshot{Webhooks: CapabilityEnabled}, false); err == nil {
		t.Fatal("enabled webhook state without a service was accepted")
	}
	if _, err := normalizeCapabilities(CapabilitySnapshot{Webhooks: CapabilityDisabled}, true); err == nil {
		t.Fatal("disabled webhook state with a wired service was accepted")
	}
}

func TestParseLimitIsStrictAndBounded(t *testing.T) {
	t.Parallel()

	for _, testCase := range []struct {
		name    string
		query   string
		want    int
		wantErr bool
	}{
		{name: "absent", want: 50},
		{name: "minimum", query: "?limit=1", want: 1},
		{name: "maximum", query: "?limit=100", want: 100},
		{name: "empty", query: "?limit=", wantErr: true},
		{name: "zero", query: "?limit=0", wantErr: true},
		{name: "negative", query: "?limit=-1", wantErr: true},
		{name: "too large", query: "?limit=101", wantErr: true},
		{name: "not numeric", query: "?limit=ten", wantErr: true},
		{name: "repeated", query: "?limit=10&limit=20", wantErr: true},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			request := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/resources"+testCase.query, nil)
			got, err := parseLimit(request, 50)
			if testCase.wantErr {
				if catalog.ErrorCodeOf(err) != catalog.CodeInvalidArgument {
					t.Fatalf("parseLimit returned %d, %v", got, err)
				}
				return
			}
			if err != nil || got != testCase.want {
				t.Fatalf("parseLimit returned %d, %v; want %d", got, err, testCase.want)
			}
		})
	}
}

func TestExpectedRevisionRequiresCanonicalStrongETag(t *testing.T) {
	request, err := http.NewRequestWithContext(context.Background(), http.MethodPost, "/api/v1/test", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("If-Match", `"rev-42"`)
	if revision, err := expectedRevision(request); err != nil || revision != 42 {
		t.Fatalf("canonical revision ETag = %d, %v", revision, err)
	}
	for _, value := range []string{"", "42", "rev-42", `"42"`, `W/"rev-42"`, `"rev-0"`, `"rev-01"`, `"rev-42""`, `"rev-9223372036854775808"`} {
		t.Run(value, func(t *testing.T) {
			request.Header.Set("If-Match", value)
			if _, err := expectedRevision(request); catalog.ErrorCodeOf(err) != catalog.CodePreconditionFailed {
				t.Fatalf("non-canonical revision ETag %q returned %v", value, err)
			}
		})
	}
}

func TestIfNoneMatchUsesWeakComparisonAndLists(t *testing.T) {
	etag := `"digest"`
	for _, value := range []string{etag, `W/` + etag, `"other", W/` + etag, "*"} {
		if !ifNoneMatchMatches(value, etag) {
			t.Fatalf("If-None-Match %q did not match %q", value, etag)
		}
	}
	for _, value := range []string{"", `"other"`, `W/"other"`} {
		if ifNoneMatchMatches(value, etag) {
			t.Fatalf("If-None-Match %q unexpectedly matched %q", value, etag)
		}
	}
}

func TestMergeLiveResultsPreservesDistinctVersions(t *testing.T) {
	identifier := "urn:air:catalog.example.com:default:agent"
	response := publication.SearchResponse{Results: []publication.SearchResult{{Entry: publication.Entry{Identifier: identifier, Version: "1.0.0"}, Score: 80, Source: "https://catalog.example.com/ard/v1/"}}}
	live := federation.LiveSearchResponse{Results: []federation.LiveResult{
		{Identifier: identifier, Version: "1.0.0", Score: 90, SourceID: "source-one", Source: "https://remote.example/ard/v1/", RespondingURL: "https://remote.example/ard/v1/search", Entry: json.RawMessage(`{"identifier":"urn:air:catalog.example.com:default:agent","version":"1.0.0","trustManifest":{"identity":"https://catalog.example.com","signature":"remote-local-collision"}}`)},
		{Identifier: identifier, Version: "2.0.0", Score: 85, SourceID: "source-one", Source: "https://remote.example/ard/v1/", RespondingURL: "https://remote.example/ard/v1/search", Entry: json.RawMessage(`{"identifier":"urn:air:catalog.example.com:default:agent","version":"2.0.0","trustManifest":{"identity":"https://catalog.example.com","signature":"winning-peer"}}`)},
		{Identifier: identifier, Version: "2.0.0", Score: 85, SourceID: "source-two", Source: "https://other.example/ard/v1/", RespondingURL: "https://other.example/ard/v1/search", Entry: json.RawMessage(`{"identifier":"urn:air:catalog.example.com:default:agent","version":"2.0.0","trustManifest":{"identity":"https://catalog.example.com","signature":"losing-peer"}}`)},
	}}
	mergeLiveResults(t.Context(), &response, live, nil)
	if len(response.Results) != 2 || response.Results[0].Version != "2.0.0" || response.Results[0].Score != 85 || response.Results[1].Version != "1.0.0" || response.Results[1].Score != 80 {
		t.Fatalf("merged multi-version response = %#v", response.Results)
	}
	if string(response.Results[0].TrustManifest) != `{"identity":"https://catalog.example.com","signature":"winning-peer"}` || len(response.Results[1].TrustManifest) != 0 {
		t.Fatalf("peer claims were merged or attached to the local winner: %+v", response.Results)
	}
}

// Local identity precedence applies to the whole active snapshot, including
// matches withheld from this page by the local page-size bound.
func TestMergeLiveResultsPreservesUnemittedLocalIdentity(t *testing.T) {
	t.Parallel()
	snapshot, _ := publicFixtureSnapshot(t, DiscoveryAnonymous)
	server := publicProjectionServer(t, DiscoveryAnonymous, func(ctx context.Context, tenant string) (publication.Snapshot, error) {
		if ctx != t.Context() || tenant != projectionTenant {
			t.Fatal("merge fixture snapshot binding")
		}
		return snapshot, nil
	})
	response, err := server.discovery[publicFixtureHost].Publication.Search(t.Context(), projectionTenant, publication.SearchRequest{Query: publication.SearchQuery{Text: "fixture"}, Federation: "auto", PageSize: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(response.Results) != 1 || response.Results[0].Identifier != snapshot.ARD.Entries[1].Identifier || response.PageToken == "" {
		t.Fatalf("local fixture page=%+v", response)
	}
	withheld := snapshot.ARD.Entries[2]
	remote := federation.LiveResult{Identifier: withheld.Identifier, Version: withheld.Version, Score: 100, SourceID: projectionNext, Source: "https://peer.example/ard/v1/", RespondingURL: "https://peer.example/ard/v1/search", Entry: json.RawMessage(`{"displayName":"Remote claim","trustManifest":{"identity":"https://catalog.example.com","signature":"withheld-local-claim"}}`)}
	mergeLiveResults(t.Context(), &response, federation.LiveSearchResponse{Results: []federation.LiveResult{remote}}, nil)
	if len(response.Results) != 1 || response.Results[0].Identifier != snapshot.ARD.Entries[1].Identifier || len(response.Results[0].TrustManifest) != 0 || response.Partial {
		t.Fatalf("withheld local identity was replaced by remote metadata: %+v", response)
	}
}

func TestMergeLiveResultsRejectsMalformedEntriesAndCarriesPartialEvidence(t *testing.T) {
	existingID := "urn:air:catalog.example.com:default:existing"
	response := publication.SearchResponse{Results: []publication.SearchResult{{Entry: publication.Entry{Identifier: existingID, Version: "1.0.0"}, Score: 90, Source: "https://catalog.example.com/ard/v1/"}}}
	live := federation.LiveSearchResponse{
		Results: []federation.LiveResult{
			{Identifier: "urn:air:remote.example:default:malformed", Version: "1.0.0", Score: 100, SourceID: "source-one", Source: "https://remote.example/ard/v1/", RespondingURL: "https://remote.example/ard/v1/search", Entry: json.RawMessage(`{`)},
			{Identifier: existingID, Version: "1.0.0", Score: 80, SourceID: "source-one", Source: "https://remote.example/ard/v1/", RespondingURL: "https://remote.example/ard/v1/search", Entry: json.RawMessage(`{"displayName":"Lower-scored duplicate"}`)},
			{Identifier: "urn:air:remote.example:default:b", Version: "1.0.0", Score: 70, SourceID: "source-one", Source: "https://remote.example/ard/v1/", RespondingURL: "https://remote.example/ard/v1/search", Entry: json.RawMessage(`{"displayName":"B"}`)},
			{Identifier: "urn:air:remote.example:default:a", Version: "2.0.0", Score: 70, SourceID: "source-one", Source: "https://remote.example/ard/v1/", RespondingURL: "https://remote.example/ard/v1/search", Entry: json.RawMessage(`{"displayName":"A two"}`)},
			{Identifier: "urn:air:remote.example:default:a", Version: "1.0.0", Score: 70, SourceID: "source-one", Source: "https://remote.example/ard/v1/", RespondingURL: "https://remote.example/ard/v1/search", Entry: json.RawMessage(`{"displayName":"A one"}`)},
		},
		Partial: true,
		Diagnostics: []federation.LiveDiagnostic{
			{Status: "failed", Message: "Remote source failed."},
			{SourceID: "source-two", Status: "timeout", Message: "Remote source timed out."},
		},
	}
	mergeLiveResults(t.Context(), &response, live, nil)
	if len(response.Results) != 4 || response.Results[0].Identifier != existingID || response.Results[0].Score != 90 {
		t.Fatalf("malformed or lower-scored live results changed the local winner: %#v", response.Results)
	}
	if response.Results[1].Identifier != "urn:air:remote.example:default:a" || response.Results[1].Version != "1.0.0" || response.Results[2].Identifier != "urn:air:remote.example:default:a" || response.Results[2].Version != "2.0.0" || response.Results[3].Identifier != "urn:air:remote.example:default:b" {
		t.Fatalf("equal-score live results were not deterministically ordered: %#v", response.Results)
	}
	if !response.Partial || len(response.Diagnostics) != 3 || response.Diagnostics[0].Status != "invalid_response" || response.Diagnostics[1].Source != "federation" || response.Diagnostics[2].Source != "source-two" {
		t.Fatalf("partial live diagnostics were not preserved safely: %#v", response)
	}
}

func TestMergeLiveResultsRejectsUnboundRespondingSourceEvidence(t *testing.T) {
	t.Parallel()

	valid := federation.LiveResult{
		Identifier:    "urn:air:remote.example:default:agent",
		Version:       "1.0.0",
		Score:         80,
		SourceID:      "source-one",
		Source:        "https://remote.example/ard/v1/",
		RespondingURL: "https://remote.example/ard/v1/search",
		Entry:         json.RawMessage(`{"displayName":"Remote agent"}`),
	}
	for _, test := range []struct {
		name   string
		mutate func(*federation.LiveResult)
	}{
		{name: "missing source identifier", mutate: func(result *federation.LiveResult) { result.SourceID = "" }},
		{name: "missing responding URL", mutate: func(result *federation.LiveResult) { result.RespondingURL = "" }},
		{name: "unsafe source URL", mutate: func(result *federation.LiveResult) { result.Source = "http://remote.example/ard/v1/" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			remote := valid
			test.mutate(&remote)
			response := publication.SearchResponse{}
			mergeLiveResults(t.Context(), &response, federation.LiveSearchResponse{Results: []federation.LiveResult{remote}}, nil)
			if len(response.Results) != 0 || !response.Partial || len(response.Diagnostics) != 1 || response.Diagnostics[0].Source != "federation" || response.Diagnostics[0].Status != "invalid_response" {
				t.Fatalf("unbound live result was not rejected safely: %#v", response)
			}
		})
	}
}

func digestHeader(content []byte) string {
	sum := sha256.Sum256(content)
	return "sha-256=:" + base64.StdEncoding.EncodeToString(sum[:]) + ":"
}

// A newly malformed projection must make a previously healthy response partial.
func TestMergeLiveResultsMarksProjectionFailurePartial(t *testing.T) {
	response := publication.SearchResponse{}
	live := federation.LiveSearchResponse{Results: []federation.LiveResult{{Identifier: "urn:air:peer.example:agent:one", Score: 80, SourceID: "source-one", Source: "https://peer.example/", RespondingURL: "https://peer.example/search", Entry: json.RawMessage(`{"displayName":42}`)}}}
	mergeLiveResults(t.Context(), &response, live, nil)
	if len(response.Results) != 0 || !response.Partial || len(response.Diagnostics) != 1 || response.Diagnostics[0].Status != "invalid_response" {
		t.Fatalf("projection failure lost partial evidence: %+v", response)
	}
}
