// Retrieval headers extend HTTP metadata, never the pinned public ARD body.
package httpserver

import (
	"net/http/httptest"
	"testing"
)

func TestSearchModeHeadersClosedVocabulary(t *testing.T) {
	for _, tc := range []struct{ tier, reason, wantTier, wantReason, wantPartial string }{
		{"hybrid", "", "hybrid", "", "true"},
		{"core", "indexing", "core", "indexing", ""},
		{"core", "provider_unavailable", "core", "provider_unavailable", ""},
		{"core", "work_limit", "core", "work_limit", ""},
		{"private model", "private failure", "core", "", ""},
	} {
		r := httptest.NewRecorder()
		setSearchModeHeaders(r, tc.tier, tc.reason, true)
		if r.Header().Get("Catalog-Search-Tier") != tc.wantTier || r.Header().Get("Catalog-Search-Fallback") != tc.wantReason || r.Header().Get("Catalog-Search-Partial-Index") != tc.wantPartial {
			t.Fatalf("unexpected public headers: %v", r.Header())
		}
	}
}
