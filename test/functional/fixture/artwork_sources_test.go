package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

func TestArtworkSourceUsesDistinctRegistryWithMatchingBoundedEntries(t *testing.T) {
	state := &fixtureState{mode: "initial", modeRequests: make(map[string]int)}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /.well-known/ard.json", state.manifest)
	mux.HandleFunc("GET /ard/v1/agents", state.list)
	registerArtworkSourceFixture(mux, state)
	read := func(path string) *httptest.ResponseRecorder {
		t.Helper()
		response := httptest.NewRecorder()
		mux.ServeHTTP(response, httptest.NewRequestWithContext(t.Context(), http.MethodGet, path, nil))
		return response
	}
	type manifestEntry struct {
		Identifier string `json:"identifier"`
		URL        string `json:"url"`
	}
	manifests := make([]manifestEntry, 0, 2)
	for _, path := range []string{"/.well-known/ard.json", "/artwork-alternate/.well-known/ard.json"} {
		response := read(path)
		var body struct {
			Entries []manifestEntry `json:"entries"`
		}
		if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &body) != nil || len(body.Entries) != 1 {
			t.Fatalf("manifest %s: status=%d body=%s", path, response.Code, response.Body)
		}
		manifests = append(manifests, body.Entries[0])
	}
	if !reflect.DeepEqual(manifests, []manifestEntry{
		{Identifier: "urn:air:functional.example:registry:main", URL: "https://federation-fixture:8443/ard/v1/"},
		{Identifier: "urn:air:functional.example:registry:artwork-alternate", URL: "https://federation-fixture:8443/artwork-alternate/ard/v1/"},
	}) {
		t.Fatalf("Registry authorities changed: %#v", manifests)
	}
	for _, query := range []string{"?pageSize=1", "?pageSize=100", "?pageSize=100&pageToken=next"} {
		original := read("/ard/v1/agents" + query)
		alternate := read("/artwork-alternate/ard/v1/agents" + query)
		if original.Code != http.StatusOK || alternate.Code != http.StatusOK || original.Body.String() != alternate.Body.String() {
			t.Fatalf("contributions differ for %s: original=%d/%s alternate=%d/%s", query, original.Code, original.Body, alternate.Code, alternate.Body)
		}
		var body struct {
			Items []struct {
				Identifier string `json:"identifier"`
			} `json:"items"`
			PageToken string `json:"pageToken"`
		}
		if err := json.Unmarshal(alternate.Body.Bytes(), &body); err != nil || len(body.Items) != 1 {
			t.Fatalf("bounded contribution page: %#v %v", body, err)
		}
		wantIdentifier, wantToken := "urn:air:functional.example:default:one", "next"
		if query == "?pageSize=1" {
			wantToken = "probe-next"
		}
		if query == "?pageSize=100&pageToken=next" {
			wantIdentifier, wantToken = "urn:air:functional.example:default:two", ""
		}
		if body.Items[0].Identifier != wantIdentifier || body.PageToken != wantToken {
			t.Fatalf("wrong contribution identity or continuation: %#v", body)
		}
	}
	if response := read("/artwork-alternate/ard/v1/agents?pageSize=1000"); response.Code != http.StatusBadRequest {
		t.Fatalf("unbounded alternate List admitted: %d", response.Code)
	}
	if response := read("/artwork-alternate/unconfigured"); response.Code != http.StatusNotFound {
		t.Fatalf("unconfigured route admitted: %d", response.Code)
	}
}
