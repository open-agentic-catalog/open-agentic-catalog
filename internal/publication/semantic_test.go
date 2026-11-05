// Tests for the semantic helpers not covered by the broader manager suite:
// the pure token-parsing function and the nil-semantic guard in rankSemantic.
package publication

import (
	"testing"
)

func TestSemanticTokenPartsNonHybridPassthrough(t *testing.T) {
	// Tokens without the "h1:" prefix must pass through unchanged.
	for _, token := range []string{"", "plaintoken", "h2:something", "h1"} {
		prefix, inner := semanticTokenParts(token)
		if prefix != "" || inner != token {
			t.Fatalf("non-h1 token %q: got prefix=%q inner=%q", token, prefix, inner)
		}
	}
}

func TestSemanticTokenPartsInvalidH1TooFewParts(t *testing.T) {
	// "h1:" prefix present but fewer than 5 colon-separated parts → "invalid".
	for _, token := range []string{"h1:profile:gen", "h1:profile:gen:fp"} {
		prefix, inner := semanticTokenParts(token)
		if prefix != "invalid" || inner != token {
			t.Fatalf("invalid h1 token %q: got prefix=%q inner=%q", token, prefix, inner)
		}
	}
}

func TestSemanticTokenPartsValidH1SplitsCorrectly(t *testing.T) {
	// Exactly 5 colon-separated parts: prefix is first 4 joined with ":",
	// inner is the 5th part.
	token := "h1:profile:gen:fp:cursorpayload" //nolint:gosec // test token, not a credential
	prefix, inner := semanticTokenParts(token)
	if prefix != "h1:profile:gen:fp:" {
		t.Fatalf("prefix: want %q got %q", "h1:profile:gen:fp:", prefix)
	}
	if inner != "cursorpayload" {
		t.Fatalf("inner: want %q got %q", "cursorpayload", inner)
	}
}

func TestSemanticTokenPartsInnerIsRecoveredVerbatim(t *testing.T) {
	// Inner part may itself contain colons (e.g. a base64 cursor); only the
	// first 4 colons are consumed.
	inner := "AAABBB==:extra:colons"
	token := "h1:abc:def:ghi:" + inner
	prefix, got := semanticTokenParts(token)
	if got != inner {
		t.Fatalf("inner: want %q got %q", inner, got)
	}
	if prefix != "h1:abc:def:ghi:" {
		t.Fatalf("prefix: want %q got %q", "h1:abc:def:ghi:", prefix)
	}
}

func TestRankSemanticNilSemanticReturnsEmpty(t *testing.T) {
	// When manager.semantic is nil rankSemantic must return empty results
	// and no error — the caller falls back to lexical ranking.
	manager, _, _ := newPublicationManager(t, nil)
	snapshot := Snapshot{
		ARD: ARDManifest{Entries: []Entry{
			{DisplayName: "Agent A", Identifier: "urn:air:example.com:agent:test"},
		}},
	}
	results, ranking, err := manager.rankSemantic(t.Context(), "tenant-1", snapshot, SearchQuery{Text: "find"}, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 0 {
		t.Fatalf("expected no results, got %d", len(results))
	}
	if ranking.Profile != "" {
		t.Fatalf("expected empty ranking, got profile=%q", ranking.Profile)
	}
}
