// Hybrid coordinator tests use injected stores/providers only. They cover
// request policy and continuation, not PostgreSQL or model quality.
package catalog

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/open-agentic-catalog/open-agentic-catalog/test/identityfixture"
	"strings"
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
)

type semanticSearchFixture struct {
	g                                      SemanticGeneration
	ready                                  bool
	providerError, storeError              error
	exploreError                           error
	searchError, similaritiesError         error
	searchRequest                          SemanticSearchRequest
	similaritiesCalls                      int
	activeCalls, statusCalls, exploreCalls int
	lookup                                 semanticLookup
	exploreRequest                         SemanticSearchRequest
	facets                                 []FacetRequest
	exploreResponse                        ExploreResponse
	queryText                              string
	queryHook                              func(context.Context)
	queries, searches                      int
	vector                                 []float32
	page                                   SemanticSearchPage
	searchHook                             func(SemanticSearchRequest) (SemanticSearchPage, error)
	probes                                 int
	probeRequest                           SemanticSearchRequest
}

type semanticLookup struct {
	tenant, profile, scope string
	dimensions             int
}

func (f *semanticSearchFixture) Status(_ context.Context, tenant, profile string, dimensions int) (SemanticIndexStatus, error) {
	f.statusCalls++
	f.lookup = semanticLookup{tenant: tenant, profile: profile, dimensions: dimensions}
	return SemanticIndexStatus{State: "ready", JobID: f.g.ID, Indexed: 2}, f.storeError
}

func (f *semanticSearchFixture) Current(context.Context, string, string) (string, error) {
	return f.g.SourceGeneration, f.storeError
}

func (f *semanticSearchFixture) Explore(_ context.Context, request SemanticSearchRequest, facets []FacetRequest) (ExploreResponse, error) {
	f.exploreCalls++
	f.exploreRequest, f.facets = request, facets
	return f.exploreResponse, f.exploreError
}

func (f *semanticSearchFixture) Similarities(context.Context, SemanticGeneration, []float32, []string) (map[string]float64, error) {
	f.similaritiesCalls++
	if f.similaritiesError != nil {
		return nil, f.similaritiesError
	}
	return nil, f.storeError
}

func (f *semanticSearchFixture) Active(_ context.Context, tenant, profile, scope string, dimensions int) (SemanticGeneration, bool, error) {
	f.activeCalls++
	f.lookup = semanticLookup{tenant, profile, scope, dimensions}
	return f.g, f.ready, f.storeError
}
func (f *semanticSearchFixture) Search(_ context.Context, request SemanticSearchRequest) (SemanticSearchPage, error) {
	if f.searchHook != nil {
		return f.searchHook(request)
	}
	if request.IdentityProbes != nil {
		f.probes++
		f.probeRequest = request
		keys := []IndexedIdentity{}
		for _, item := range f.page.Items {
			result := item.Result
			if item.Local != nil {
				result = localCatalogSearchResult(*item.Local)
			}
			key := IndexedIdentity{result.Identifier, result.Version, result.VersionPresent}
			for _, probe := range request.IdentityProbes {
				if probe == key {
					keys = append(keys, key)
					break
				}
			}
		}
		return SemanticSearchPage{Matches: keys}, f.searchError
	}
	f.searches++
	f.searchRequest = request
	if f.searchError != nil {
		return SemanticSearchPage{}, f.searchError
	}
	return f.page, f.storeError
}
func (f *semanticSearchFixture) Query(ctx context.Context, query string) ([]float32, error) {
	f.queries++
	f.queryText = query
	if f.queryHook != nil {
		f.queryHook(ctx)
	}
	return f.vector, f.providerError
}
func (f *semanticSearchFixture) Documents(context.Context, []string) ([][]float32, error) {
	panic("search must not index documents")
}

// newSemanticSearchTest binds real cursor logic to in-memory deterministic ports.
func newSemanticSearchTest(t *testing.T) (*Service, *semanticSearchFixture, identity.Actor) {
	t.Helper()
	actor := identity.Actor{TenantID: "00000000-0000-7000-8000-000000000801", Subject: "reader", Roles: []identity.Role{identity.RoleReader}}
	f := &semanticSearchFixture{ready: true, vector: []float32{1, 0}, g: SemanticGeneration{ID: "00000000-0000-7000-8000-000000000901", TenantID: actor.TenantID, Profile: "sha256:" + strings.Repeat("a", 64), Scope: "control", SourceGeneration: "1", Dimensions: 2}}
	f.page = SemanticSearchPage{Items: []SemanticSearchItem{{Result: CatalogSearchResult{ID: "indexed:item", DisplayName: "Item"}, Position: SemanticSearchPosition{Rank: 16393442623, SortName: "item", ID: "indexed:item"}}}, Total: 2, HasMore: true}
	semantic, err := NewSemanticSearch(SemanticSearchOptions{Store: f, Provider: f, Profile: f.g.Profile, RankingProfile: "sha256:" + strings.Repeat("b", 64), Dimensions: 2, Cutoff: 0.5, Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	s := &Service{semantic: semantic, clock: &mutableCatalogSearchClock{time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}, cursorKey: []byte(strings.Repeat("k", 32)), policy: "default"}
	return s, f, actor
}

// Exclusions protect the UUID identity, not its chosen uppercase spelling.
func TestSemanticExclusionsAndSnapshotCanonicalIdentity(t *testing.T) {
	s, f, actor := newSemanticSearchTest(t)
	actor.TenantID = "abcdefab-0000-7000-8000-000000000801"
	actor.Roles = append(actor.Roles, identity.RoleTenantAdmin)
	ctx := identityfixture.WithActor(t.Context(), actor)
	s.semantic.options.ExcludedTenants = []string{strings.ToUpper(actor.TenantID)}
	f.storeError = errors.New("excluded state must not be read")
	if enabled, _ := s.SemanticConfiguration(ctx); enabled {
		t.Fatal("case variant bypassed capability exclusion")
	}
	if _, used, _, err := s.semanticPage(ctx, actor, SearchQuery{Text: "find", Limit: 1}, false); err != nil || used {
		t.Fatal("case variant reached private search")
	}
	if _, used, _, err := s.semanticExplore(ctx, actor, ExploreRequest{Query: SearchQuery{Text: "find"}}); err != nil || used {
		t.Fatal("case variant reached private facets")
	}
	if status, err := s.SemanticStatus(ctx); err != nil || status != nil {
		t.Fatal("case variant exposed indexing status")
	}
	if _, err := s.SemanticReindexTarget(ctx, ""); ErrorCodeOf(err) != CodeDependencyUnavailable {
		t.Fatal("case variant admitted indexing")
	}
	if result, err := s.semantic.RankSnapshot(ctx, actor.TenantID, f.g.ID, "1", "find", []HybridCandidate{{ID: "entry:1"}}); err != nil || result.Profile != "" {
		t.Fatal("case variant reached public inference")
	}
	if f.queries != 0 || f.searches != 0 {
		t.Fatal("excluded tenant caused inference or matching")
	}
	s.semantic.options.ExcludedTenants = nil
	f.storeError = nil
	const snapshot = "abcdefab-0000-7000-8000-000000000901"
	target, err := s.SemanticReindexTarget(ctx, strings.ToUpper(snapshot))
	if err != nil || target.Scope != "public:"+snapshot {
		t.Fatalf("noncanonical snapshot scope: %+v %v", target, err)
	}
}

func TestSemanticSearchContinuationAndSafeRestart(t *testing.T) {
	s, f, actor := newSemanticSearchTest(t)
	query := SearchQuery{Text: "find tools", Limit: 1, Kinds: []Kind{KindAgent}, Tags: []string{"FILTER_TAG"}, Publisher: "FILTER_PUBLISHER", Visibility: VisibilityTenant}
	first, used, reason, err := s.semanticPage(t.Context(), actor, query, false)
	if err != nil || !used || reason != "" || first.Tier != "hybrid" || first.NextCursor == "" {
		t.Fatalf("first=%+v used=%v reason=%s err=%v", first, used, reason, err)
	}
	if f.queries != 1 || f.queryText != "find tools" {
		t.Fatalf("provider received more than the submitted query: %q (%d calls)", f.queryText, f.queries)
	}
	query.Cursor = first.NextCursor
	if _, used, _, err := s.semanticPage(t.Context(), actor, query, false); err != nil || !used {
		t.Fatalf("continue: %v", err)
	}
	f.vector = []float32{0, 1}
	if _, _, _, err := s.semanticPage(t.Context(), actor, query, false); ErrorCodeOf(err) != CodeInvalidArgument {
		t.Fatalf("model drift: %v", err)
	}
	f.vector = []float32{1, 0}
	for _, cause := range []error{ErrEmbeddingUnavailable, fmt.Errorf("provider: %w", &EmbeddingRetryError{RetryAfter: 2 * time.Minute})} {
		f.providerError = cause
		before := f.searches
		if _, _, _, err := s.semanticPage(t.Context(), actor, query, false); ErrorCodeOf(err) != CodeDependencyUnavailable || f.searches != before {
			t.Fatalf("continuation silently fell back: %v", err)
		}
	}
	f.providerError = nil
	f.g.SourceGeneration = "2"
	if _, _, _, err := s.semanticPage(t.Context(), actor, query, false); ErrorCodeOf(err) != CodeInvalidArgument {
		t.Fatalf("stale index: %v", err)
	}
	f.g.SourceGeneration = "1"
	actor.Subject = "different"
	before := f.queries
	if _, _, _, err := s.semanticPage(t.Context(), actor, query, false); ErrorCodeOf(err) != CodeInvalidArgument || f.queries != before {
		t.Fatalf("cursor auth binding: %v", err)
	}
}

// TestSemanticSearchRejectsMalformedCursorEnvelopes rejects malformed or expired
// continuation before index lookup, inference or matching.
func TestSemanticSearchRejectsMalformedCursorEnvelopes(t *testing.T) {
	t.Parallel()
	s, f, actor := newSemanticSearchTest(t)
	query := SearchQuery{Text: "find tools", Limit: 1}
	first, used, reason, err := s.semanticPage(t.Context(), actor, query, false)
	if err != nil || !used || reason != "" || first.NextCursor == "" {
		t.Fatalf("valid first page: %+v, used=%v, reason=%q, error=%v", first, used, reason, err)
	}
	parts := strings.Split(first.NextCursor, ".")
	if len(parts) != 3 || parts[0] != "h1" {
		t.Fatal("valid hybrid cursor has an unexpected envelope")
	}
	// Raw base64 decoding permits line breaks; canonical cursor spelling does not.
	nonCanonicalPayload := parts[1][:1] + "\n" + parts[1][1:]
	decoded, err := base64.RawURLEncoding.DecodeString(nonCanonicalPayload)
	if err != nil || base64.RawURLEncoding.EncodeToString(decoded) != parts[1] {
		t.Fatal("noncanonical payload must retain the original signed bytes")
	}
	upperMAC := strings.ToUpper(parts[2])
	if upperMAC == parts[2] {
		t.Fatal("fixture MAC must contain hex letters to change its spelling")
	}
	clock, ok := s.clock.(*mutableCatalogSearchClock)
	if !ok {
		t.Fatal("fixture must use an injected mutable clock")
	}
	now := clock.now
	clock.now = now.Add(catalogSearchCursorTTL - time.Nanosecond)
	query.Cursor = first.NextCursor
	continued, used, reason, err := s.semanticPage(t.Context(), actor, query, false)
	if err != nil || !used || reason != "" || continued.NextCursor == "" {
		t.Fatalf("valid continuation immediately before expiry: used=%v, reason=%q, error=%v", used, reason, err)
	}
	for _, tc := range []struct {
		name, token string
		advance     time.Duration
	}{
		{"missing envelope parts", "h1.", 0},
		{"missing signature part", "h1." + parts[1], 0},
		{"extra envelope part", first.NextCursor + ".extra", 0},
		{"invalid base64", "h1.%." + parts[2], 0},
		{"noncanonical base64", "h1." + nonCanonicalPayload + "." + parts[2], 0},
		{"invalid hex signature", "h1." + parts[1] + ".not-hex", 0},
		{"noncanonical hex signature", "h1." + parts[1] + "." + upperMAC, 0},
		{"expired at deadline", first.NextCursor, catalogSearchCursorTTL},
		{"continued cursor keeps original deadline", continued.NextCursor, catalogSearchCursorTTL},
	} {
		t.Run(tc.name, func(t *testing.T) {
			clock.now = now.Add(tc.advance)
			query.Cursor = tc.token
			if err := validateCatalogSearchQuery(query); err != nil {
				t.Fatalf("fixture must pass query admission: %v", err)
			}
			before := [3]int{f.activeCalls, f.queries, f.searches}
			page, used, reason, err := s.semanticPage(t.Context(), actor, query, false)
			if ErrorCodeOf(err) != CodeInvalidArgument || !used || reason != "" || len(page.Items) != 0 || page.NextCursor != "" {
				t.Fatalf("invalid continuation: %+v, used=%v, reason=%q, error=%v", page, used, reason, err)
			}
			if after := [3]int{f.activeCalls, f.queries, f.searches}; after != before {
				t.Fatalf("invalid cursor reached store or provider: before=%v, after=%v", before, after)
			}
		})
	}
}

func TestSemanticSearchFallbackAndModelFreePaths(t *testing.T) {
	for _, cause := range []error{ErrEmbeddingUnavailable, fmt.Errorf("provider: %w", &EmbeddingRetryError{RetryAfter: 2 * time.Minute}), ErrEmbeddingAuthentication, ErrEmbeddingRejected, ErrEmbeddingInvalidResponse, context.DeadlineExceeded} {
		t.Run(cause.Error(), func(t *testing.T) {
			s, f, actor := newSemanticSearchTest(t)
			f.providerError = cause
			if _, used, reason, err := s.semanticPage(t.Context(), actor, SearchQuery{Text: "find", Limit: 1}, false); err != nil || used || reason != "provider_unavailable" || f.searches != 0 {
				t.Fatalf("fallback used=%v reason=%s err=%v", used, reason, err)
			}
			if f.queries != 1 || f.queryText != "find" {
				t.Fatalf("provider fallback must follow the submitted query: %q (%d calls)", f.queryText, f.queries)
			}
		})
	}
	s, f, actor := newSemanticSearchTest(t)
	for _, query := range []SearchQuery{{Limit: 1}, {Text: "urn:air:catalog.example:default:tool", Limit: 1}, {Text: "find", Cursor: "lexical-token", Limit: 1}} {
		if _, used, _, err := s.semanticPage(t.Context(), actor, query, false); err != nil || used || f.queries != 0 {
			t.Fatalf("model-free request made inference: %v", err)
		}
	}
	f.ready = false
	query := SearchQuery{Text: "find", Limit: 1}
	if _, used, reason, err := s.semanticPage(t.Context(), actor, query, false); err != nil || used || reason != "indexing" || f.queries != 0 {
		t.Fatalf("index fallback %v %s %v", used, reason, err)
	}
	f.ready = true
	f.storeError = errors.New("core database failure")
	if _, _, _, err := s.semanticPage(t.Context(), actor, query, false); !errors.Is(err, f.storeError) || f.queries != 0 {
		t.Fatal("core failure hidden")
	}
	f.storeError = nil
	f.page = SemanticSearchPage{Items: []SemanticSearchItem{}}
	if page, used, reason, err := s.semanticPage(t.Context(), actor, query, false); err != nil || !used || reason != "" || page.DurableTotal != 0 {
		t.Fatalf("empty success misclassified: %v", err)
	}
	s.semantic.options.ExcludedTenants = []string{actor.TenantID}
	before := f.queries
	if _, used, _, err := s.semanticPage(t.Context(), actor, query, false); err != nil || used || f.queries != before {
		t.Fatalf("excluded tenant invoked model: %v", err)
	}
	// Remove the exclusion so it cannot mask the disabled capability guard.
	s.semantic.options.ExcludedTenants = nil
	s.semantic = nil
	if _, used, reason, err := s.semanticPage(t.Context(), actor, query, false); err != nil || used || reason != "" || f.queries != before {
		t.Fatalf("disabled search invoked model: used=%v reason=%q error=%v", used, reason, err)
	}
}

func TestSemanticCursorSignedPayloadBounds(t *testing.T) {
	t.Parallel()
	service, _, _ := newSemanticSearchTest(t)
	valid := semanticCursor{Generation: "00000000-0000-7000-8000-000000000901", SourceGeneration: "1", VectorFingerprint: strings.Repeat("a", 64), Position: SemanticSearchPosition{Rank: 100, SortName: "item", ID: "indexed:item"}, ExpiresAt: service.clock.Now().Add(time.Minute).Unix()}
	for _, tc := range []struct {
		name     string
		change   func(*semanticCursor)
		accepted bool
	}{
		{"valid", func(*semanticCursor) {}, true},
		{"source limit", func(c *semanticCursor) { c.SourceGeneration = strings.Repeat("s", 256) }, true},
		{"ID limit", func(c *semanticCursor) { c.Position.ID = strings.Repeat("i", 256) }, true},
		{"name limit", func(c *semanticCursor) { c.Position.SortName = strings.Repeat("n", 640) }, true},
		{"minimum rank", func(c *semanticCursor) { c.Position.Rank = 1 }, true},
		{"maximum rank", func(c *semanticCursor) { c.Position.Rank = 1e12 }, true},
		{"invalid generation", func(c *semanticCursor) { c.Generation = "invalid" }, false},
		{"missing source", func(c *semanticCursor) { c.SourceGeneration = "" }, false},
		{"oversized source", func(c *semanticCursor) { c.SourceGeneration = strings.Repeat("s", 257) }, false},
		{"short fingerprint", func(c *semanticCursor) { c.VectorFingerprint = strings.Repeat("a", 63) }, false},
		{"long fingerprint", func(c *semanticCursor) { c.VectorFingerprint = strings.Repeat("a", 65) }, false},
		{"zero rank", func(c *semanticCursor) { c.Position.Rank = 0 }, false},
		{"oversized rank", func(c *semanticCursor) { c.Position.Rank = 1e12 + 1 }, false},
		{"missing ID", func(c *semanticCursor) { c.Position.ID = "" }, false},
		{"oversized ID", func(c *semanticCursor) { c.Position.ID = strings.Repeat("i", 257) }, false},
		{"oversized name", func(c *semanticCursor) { c.Position.SortName = strings.Repeat("n", 641) }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			candidate := valid
			tc.change(&candidate)
			token, err := service.encodeSemanticCursor(candidate, "binding")
			if err != nil {
				t.Fatal(err)
			}
			got, err := service.decodeSemanticCursor(token, "binding")
			if tc.accepted {
				if err != nil || got != candidate {
					t.Fatalf("valid signed cursor = %+v, %v", got, err)
				}
			} else if ErrorCodeOf(err) != CodeInvalidArgument || got != (semanticCursor{}) {
				t.Fatalf("invalid signed cursor = %+v, %v", got, err)
			}
		})
	}
	raw, err := json.Marshal(valid)
	if err != nil {
		t.Fatal(err)
	}
	// Authenticate a valid JSON object with an extra field so the signature
	// or generic JSON validator cannot mask a relaxed closed-object decoder.
	payload := append(append([]byte{}, raw[:len(raw)-1]...), []byte(`,"unknown":true}`)...)
	mac := hmac.New(sha256.New, service.cursorKey)
	_, _ = mac.Write([]byte("catalog-hybrid-cursor-v1\x00binding\x00"))
	_, _ = mac.Write(payload)
	token := "h1." + base64.RawURLEncoding.EncodeToString(payload) + "." + hex.EncodeToString(mac.Sum(nil))
	if _, err := service.decodeSemanticCursor(token, "binding"); ErrorCodeOf(err) != CodeInvalidArgument {
		t.Fatalf("signed unknown field accepted: %v", err)
	}
}

func TestSemanticCursorExactWireBound(t *testing.T) {
	t.Parallel()
	service, _, _ := newSemanticSearchTest(t)
	cursor := semanticCursor{Generation: "00000000-0000-7000-8000-000000000901", SourceGeneration: "1", VectorFingerprint: strings.Repeat("a", 64), Position: SemanticSearchPosition{Rank: 100, ID: "indexed:item"}, ExpiresAt: service.clock.Now().Add(time.Minute).Unix()}
	raw, err := json.Marshal(cursor)
	if err != nil {
		t.Fatal(err)
	}
	// Three prefix bytes, one separator and 64 MAC bytes leave a fixed raw
	// payload budget. JSON escaping fills it without an oversized decoded name.
	remaining := (MaxCatalogCursorBytes-68)*3/4 - len(raw)
	cursor.Position.SortName = strings.Repeat("<", remaining/6) + strings.Repeat("n", remaining%6)
	token, err := service.encodeSemanticCursor(cursor, "binding")
	if err != nil || len(token) != MaxCatalogCursorBytes {
		t.Fatalf("exact encoded bound: bytes=%d, %v", len(token), err)
	}
	got, err := service.decodeSemanticCursor(token, "binding")
	if err != nil || got != cursor {
		t.Fatalf("exact bound round trip: %+v, %v", got, err)
	}
	cursor.Position.SortName += "n"
	if _, err := service.encodeSemanticCursor(cursor, "binding"); ErrorCodeOf(err) != CodeInternal {
		t.Fatalf("oversized encoding: %v", err)
	}

	// Keep every decoded field valid and vary only trailing JSON whitespace.
	// Build and authenticate the wire document independently of the encoder:
	// otherwise its own size refusal prevents reaching the decoder's guard.
	want := semanticCursor{Generation: "00000000-0000-7000-8000-000000000901", SourceGeneration: "1", VectorFingerprint: strings.Repeat("a", 64), Position: SemanticSearchPosition{Rank: 100, SortName: "item", ID: "indexed:item"}, ExpiresAt: service.clock.Now().Add(time.Minute).Unix()}
	document := fmt.Sprintf(`{"g":"00000000-0000-7000-8000-000000000901","s":"1","f":"%s","p":{"r":100,"n":"item","i":"indexed:item"},"e":%d}`, strings.Repeat("a", 64), want.ExpiresAt)
	// The h1. prefix, separator and SHA-256 hex MAC occupy 68 bytes.
	// Raw URL base64 has no canonical length of 4029, so 4098 is the first
	// representable token size above the 4096-byte bound.
	for _, tc := range []struct {
		name         string
		payloadBytes int
		tokenBytes   int
		accepted     bool
	}{
		{"below bound", 3020, 4095, true},
		{"at bound", 3021, 4096, true},
		{"above bound", 3022, 4098, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			payload := []byte(document + strings.Repeat(" ", tc.payloadBytes-len(document)))
			if !json.Valid(payload) {
				t.Fatal("padded fixture must remain valid JSON")
			}
			mac := hmac.New(sha256.New, service.cursorKey)
			_, _ = mac.Write([]byte("catalog-hybrid-cursor-v1\x00binding\x00"))
			_, _ = mac.Write(payload)
			token := "h1." + base64.RawURLEncoding.EncodeToString(payload) + "." + hex.EncodeToString(mac.Sum(nil))
			if len(token) != tc.tokenBytes {
				t.Fatalf("fixture token bytes = %d, want %d", len(token), tc.tokenBytes)
			}
			got, err := service.decodeSemanticCursor(token, "binding")
			if tc.accepted {
				if err != nil || got != want {
					t.Fatalf("valid padded cursor = %+v, %v; want %+v", got, err, want)
				}
			} else if ErrorCodeOf(err) != CodeInvalidArgument || got != (semanticCursor{}) {
				t.Fatalf("oversized valid signed cursor = %+v, %v", got, err)
			}
		})
	}
}
