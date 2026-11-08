package main

import "net/http"

// A distinct fixed Registry authority contributes the same exact entries as the
// original owned source. The packaged browser can edit its local artwork head
// without changing the selected source's presentation or bypassing uniqueness.
func registerArtworkSourceFixture(mux *http.ServeMux, state *fixtureState) {
	mux.HandleFunc("GET /artwork-alternate/.well-known/ard.json", state.artworkManifest)
	mux.HandleFunc("GET /artwork-alternate/ard/v1/agents", state.list)
}

func (s *fixtureState) artworkManifest(writer http.ResponseWriter, _ *http.Request) {
	s.mu.Lock()
	s.manifestRequests++
	s.mu.Unlock()
	writeJSON(writer, http.StatusOK, map[string]any{"entries": []map[string]any{{
		"identifier":            "urn:air:functional.example:registry:artwork-alternate",
		"displayName":           "Artwork alternate Registry",
		"type":                  "application/ai-registry+json",
		"url":                   fixtureOrigin + "/artwork-alternate/ard/v1/",
		"representativeQueries": []string{"Find functional agents", "Browse functional agents"},
	}}})
}
