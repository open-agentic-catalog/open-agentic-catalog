package httpserver

import (
	"bytes"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/federation"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/publication"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/standards"
)

func TestMergeLiveResultsPreservesTrustManifest(t *testing.T) {
	t.Parallel()
	const trust = `{"identity":"https://peer.example","trustSchema":{"governanceUri":"https://policy.example/rules","verificationMethods":["custom"],"future":9007199254740993},"attestations":[{"type":"custom","uri":"https://receipts.example/one","future":null}],"signature":"opaque","future":{"@context":"https://never-fetch.example/context","text":"<claim> & \u00e9","values":[false,null,9007199254740993]}}`
	for _, spelling := range []string{"", "trustManifest", "TrustManifest", "TRUSTMANIFEST"} {
		t.Run("spelling="+spelling, func(t *testing.T) {
			raw := json.RawMessage(`{"identifier":"urn:air:peer.example:agent:one","version":"1","score":80,"source":"https://peer.example/search"`)
			if spelling != "" {
				raw = append(raw, []byte(`,"`+spelling+`":`+trust)...)
			}
			raw = append(raw, '}')
			before := bytes.Clone(raw)
			remote := federation.LiveResult{Identifier: "urn:air:peer.example:agent:one", Version: "1", VersionPresent: true, Score: 80, SourceID: "source-one", Source: "https://peer.example/search", RespondingURL: "https://peer.example/search", Entry: raw}
			response := publication.SearchResponse{}
			mergeLiveResults(t.Context(), &response, federation.LiveSearchResponse{Results: []federation.LiveResult{remote}}, nil)
			if response.Partial || len(response.Diagnostics) != 0 || len(response.Results) != 1 || !bytes.Equal(raw, before) {
				t.Fatalf("valid sparse evidence changed or refused: %+v", response)
			}
			encoded, err := json.Marshal(response.Results[0])
			if err != nil {
				t.Fatal(err)
			}
			var fields map[string]json.RawMessage
			if err := json.Unmarshal(encoded, &fields); err != nil {
				t.Fatal(err)
			}
			if _, present := fields["TrustManifest"]; present {
				t.Fatal("Search emitted the schema-only uppercase spelling")
			}
			_, present := fields["trustManifest"]
			if want := spelling == "trustManifest" || spelling == "TrustManifest"; present != want {
				t.Fatalf("trust disclosure present=%t, want %t: %s", present, want, encoded)
			}
			if present {
				var got, want any
				for _, pair := range []struct {
					raw    []byte
					target *any
				}{{fields["trustManifest"], &got}, {[]byte(trust), &want}} {
					decoder := json.NewDecoder(bytes.NewReader(pair.raw))
					decoder.UseNumber()
					if err := decoder.Decode(pair.target); err != nil {
						t.Fatal(err)
					}
				}
				if !reflect.DeepEqual(got, want) {
					t.Fatalf("framework values changed: %s", encoded)
				}
			}
		})
	}
}

func TestMergeLiveResultsPreservesExactNumericMetadata(t *testing.T) {
	t.Parallel()
	remote := federation.LiveResult{Identifier: "urn:air:peer.example:default:precise", Version: "1.0.0", VersionPresent: true, Score: 80, SourceID: "source-one", Source: "https://peer.example/ard/v1/", RespondingURL: "https://peer.example/ard/v1/search", Entry: json.RawMessage(`{"identifier":"urn:air:peer.example:default:precise","version":"1.0.0","displayName":"Precise metadata","metadata":{"sequence":9007199254740993,"negative":-9007199254740993}}`)}
	response := publication.SearchResponse{}
	mergeLiveResults(t.Context(), &response, federation.LiveSearchResponse{Results: []federation.LiveResult{remote}}, nil)
	if response.Partial || len(response.Diagnostics) != 0 || len(response.Results) != 1 {
		t.Fatalf("valid metadata projection refused: %+v", response)
	}
	got := response.Results[0]
	if got.Identifier != remote.Identifier || got.Version != "1.0.0" || got.Source != "https://peer.example/ard/v1/" || got.Score != 80 || !reflect.DeepEqual(got.Metadata, map[string]any{"sequence": json.Number("9007199254740993"), "negative": json.Number("-9007199254740993")}) {
		t.Fatalf("numeric metadata rounded or provenance changed: %+v", got)
	}
	encoded, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	var wire struct {
		Metadata map[string]json.RawMessage `json:"metadata"`
	}
	if err := json.Unmarshal(encoded, &wire); err != nil {
		t.Fatal(err)
	}
	if string(wire.Metadata["sequence"]) != "9007199254740993" || string(wire.Metadata["negative"]) != "-9007199254740993" {
		t.Fatalf("wire projection lost integer precision: %s", encoded)
	}
}

func TestMergeLiveResultsPreservesSubjectIRIAcrossPeerPrefixes(t *testing.T) {
	t.Parallel()
	for _, extra := range []string{
		`,"http://purl.org/dc/terms/subject":"finance"`,
		`,"http://purl.org/dc/terms/subject":["finance"]`,
		`,"@context":{"foreign":"http://purl.org/dc/terms/"},"foreign:subject":"finance"`,
	} {
		raw := json.RawMessage(`{"identifier":"urn:air:peer.example:one","score":80,"source":"https://peer.example/search"` + extra + `}`)
		before := string(raw)
		response := publication.SearchResponse{}
		mergeLiveResults(t.Context(), &response, federation.LiveSearchResponse{Results: []federation.LiveResult{{Identifier: "urn:air:peer.example:one", Score: 80, SourceID: "source-one", Source: "https://peer.example/search", RespondingURL: "https://peer.example/search", Entry: raw}}}, map[string]any{standards.ARDSubjectIRI: "finance"})
		if response.Partial || len(response.Results) != 1 || !reflect.DeepEqual(response.Results[0].Subjects, []string{"finance"}) || string(raw) != before {
			t.Fatalf("topic lost, peer refused or evidence changed: %+v", response)
		}
		encoded, err := json.Marshal(response.Results[0])
		if err != nil {
			t.Fatal(err)
		}
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(encoded, &fields); err != nil || string(fields[standards.ARDSubjectIRI]) != `["finance"]` {
			t.Fatalf("topic meaning not emitted by full IRI: %s / %v", encoded, err)
		}
	}
}

func TestMergeLiveResultsRejectsDisclosedFilterContradictions(t *testing.T) {
	t.Parallel()
	local := publication.SearchResult{Entry: publication.Entry{Identifier: "urn:air:catalog.example:default:local", Version: "1.0.0", DisplayName: "Local evidence", Type: "application/a2a-agent-card+json", Tags: []string{"selected"}}, Score: 90, Source: "https://catalog.example/ard/v1/"}
	for _, test := range []struct {
		name, entry string
		rejected    bool
	}{
		{"matching", `{"identifier":"urn:air:peer.example:default:remote","version":"2.0.0","displayName":"Remote","type":"application/a2a-agent-card+json","tags":["selected","other"]}`, false},
		{"sparse", `{"identifier":"urn:air:peer.example:default:remote","version":"2.0.0","displayName":"Remote"}`, false},
		{"contradictory type", `{"identifier":"urn:air:peer.example:default:remote","version":"2.0.0","displayName":"Remote","type":"application/mcp-server-card+json","tags":["selected"]}`, true},
		{"contradictory tags", `{"identifier":"urn:air:peer.example:default:remote","version":"2.0.0","displayName":"Remote","type":"application/a2a-agent-card+json","tags":["other"]}`, true},
		{"null metadata", `{"identifier":"urn:air:peer.example:default:remote","version":"2.0.0","displayName":"Remote","type":"application/a2a-agent-card+json","tags":["selected"],"metadata":{"region":null}}`, true},
		{"literal nil metadata", `{"identifier":"urn:air:peer.example:default:remote","version":"2.0.0","displayName":"Remote","type":"application/a2a-agent-card+json","tags":["selected"],"metadata":{"region":"<nil>"}}`, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			retained := local
			retained.Tags = append([]string(nil), local.Tags...)
			response := publication.SearchResponse{Results: []publication.SearchResult{retained}}
			remote := federation.LiveResult{Identifier: "urn:air:peer.example:default:remote", Version: "2.0.0", VersionPresent: true, Score: 80, SourceID: "source-one", Source: "https://peer.example/ard/v1/", RespondingURL: "https://peer.example/ard/v1/search", Entry: json.RawMessage(test.entry)}
			mergeLiveResults(t.Context(), &response, federation.LiveSearchResponse{Results: []federation.LiveResult{remote}}, map[string]any{"type": []string{"application/a2a-agent-card+json"}, "tags": []string{"selected"}, "metadata.region": "<nil>"})
			if len(response.Results) == 0 || !reflect.DeepEqual(response.Results[0], local) {
				t.Fatalf("local evidence changed: %+v", response)
			}
			if test.rejected {
				want := []publication.SourceDiagnostic{{Source: "federation", Status: "invalid_response", Message: "A live result contradicted a structured query filter."}}
				if len(response.Results) != 1 || !response.Partial || !reflect.DeepEqual(response.Diagnostics, want) {
					t.Fatalf("contradictory disclosure accepted or misclassified: %+v", response)
				}
			} else if len(response.Results) != 2 || response.Partial || len(response.Diagnostics) != 0 || response.Results[1].Identifier != remote.Identifier || response.Results[1].Version != "2.0.0" || response.Results[1].Source != remote.Source || response.Results[1].Score != 80 || response.Results[1].DisplayName != "Remote" {
				t.Fatalf("matching or sparse disclosure refused: %+v", response)
			}
		})
	}
}
