// Live-search fixtures supply deterministic ordering and partial-failure responses.

package main

import (
	"encoding/json"
	"net/http"
)

// search serves a single bounded wire response, not an implementation of another
// Registry. Protocol permutations remain in the maintained conformance tests.
func (s *fixtureState) search(w http.ResponseWriter, r *http.Request) {
	var input map[string]json.RawMessage
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192)).Decode(&input) != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid bounded query"})
		return
	}
	s.mu.Lock()
	partial := s.mode == "partial"
	s.mu.Unlock()
	if partial {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "owned unavailable Registry"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"results": []any{map[string]any{
		// Sort ahead of the owned local identifiers at equal score so a
		// one-result auto page must retain all local results in continuation.
		"identifier": "urn:air:aaa.functional.example:default:live", "displayName": "Functional Live", "description": "One bounded live order fixture result",
		"type": "application/a2a-agent-card+json", "url": fixtureOrigin + "/live-card.json", "score": 100, "source": fixtureOrigin + "/",
	}}})
}
