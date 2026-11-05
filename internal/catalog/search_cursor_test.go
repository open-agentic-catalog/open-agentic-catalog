package catalog

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/open-agentic-catalog/open-agentic-catalog/test/identityfixture"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
)

type boundedSearchRepository struct {
	Repository
	search func(context.Context, string, CatalogSearchRepositoryRequest) (CatalogSearchRepositoryPage, error)
}

func (r *boundedSearchRepository) SearchCatalog(ctx context.Context, tenantID string, request CatalogSearchRepositoryRequest) (CatalogSearchRepositoryPage, error) {
	return r.search(ctx, tenantID, request)
}

func FuzzCatalogSearchCursor(f *testing.F) {
	now := time.Date(2026, time.August, 29, 12, 0, 0, 0, time.UTC)
	key := sha256.Sum256([]byte("catalog-cursor-fuzz-key"))
	service := &Service{clock: &mutableCatalogSearchClock{now: now}, cursorKey: key[:]}
	const generation = "0123456789abcdef"
	const binding = "bound-request"
	position := CatalogSearchPosition{Score: 73, DisplayName: "example", ResultID: "local:00000000-0000-7000-8000-000000000001"}
	valid, err := service.encodeSearchCursor(generation, position, binding)
	if err != nil {
		f.Fatal(err)
	}
	live := LiveCursor{Version: 1, Surface: "control", Generation: generation, IndexedGeneration: generation, Position: &position, Fingerprint: strings.Repeat("a", 64), ExpiresAt: now.Add(catalogSearchCursorTTL).Unix()}
	validLive, err := EncodeLiveCursor(live, key[:], binding)
	if err != nil {
		f.Fatal(err)
	}
	liveSeeds := map[string]LiveCursor{validLive: live}
	for _, mode := range []string{"hybrid", "live"} {
		state := live
		state.Mode, state.Position = mode, nil
		if mode == "hybrid" {
			state.Ranking = "sha256:" + strings.Repeat("b", 64)
			state.VectorFingerprint = strings.Repeat("c", 64)
			state.HybridPosition = &SemanticSearchPosition{Rank: 50000000, SortName: "example", ID: position.ResultID}
		}
		token, err := EncodeLiveCursor(state, key[:], binding)
		if err != nil {
			f.Fatal(err)
		}
		liveSeeds[token] = state
		f.Add(token)
	}
	parts := strings.Split(valid, ".")
	if len(parts) != 2 {
		f.Fatal("valid cursor has no MAC separator")
	}
	nonCanonicalMAC := parts[0] + "." + strings.ToUpper(parts[1])
	invalidBase64 := "%." + strings.Repeat("0", sha256.Size*2)
	invalidHex := base64.RawURLEncoding.EncodeToString([]byte(`{}`)) + ".not-hex"
	tamperedMAC := parts[0] + "." + strings.Repeat("0", sha256.Size*2)
	invalidPayload := catalogSearchCursorTokenForTest([]byte(`{`), binding, key[:])
	trailingPayload := catalogSearchCursorTokenForTest([]byte(`{"v":2,"g":"0123456789abcdef","s":73,"n":"example","i":"local:00000000-0000-7000-8000-000000000001","e":1788005700} {}`), binding, key[:])
	nonCanonicalPayload := parts[0][:1] + "\n" + parts[0][1:] + "." + parts[1]
	for _, seed := range []string{validLive, validLive + "x", LiveCursorPrefix + "%.not-hex", LiveCursorPrefix + strings.Repeat("x", MaxCatalogCursorBytes), valid, nonCanonicalPayload, nonCanonicalMAC, invalidBase64, invalidHex, tamperedMAC, invalidPayload, trailingPayload, "", "offset:7", "not-base64!", strings.Repeat("x", MaxCatalogCursorBytes+1)} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, token string) {
		if strings.HasPrefix(token, LiveCursorPrefix) {
			cursor, err := DecodeLiveCursor(token, "control", key[:], binding, now)
			if err != nil {
				if ErrorCodeOf(err) != CodeInvalidArgument {
					t.Fatalf("unsafe live cursor error: %v", err)
				}
				return
			}
			if expected, known := liveSeeds[token]; !known || !reflect.DeepEqual(cursor, expected) {
				t.Fatalf("accepted altered live cursor: %+v", cursor)
			}
			return
		}
		cursor, present, err := service.decodeSearchCursor(token, binding)
		if token == "" {
			if err != nil || present {
				t.Fatalf("empty cursor returned %#v, present=%t, %v", cursor, present, err)
			}
			return
		}
		if err != nil {
			return
		}
		if !present {
			t.Fatal("non-empty cursor was accepted without a value")
		}
		if cursor.Generation != generation || cursor.Score != position.Score || cursor.DisplayName != position.DisplayName || cursor.ResultID != position.ResultID {
			t.Fatalf("accepted cursor = %#v", cursor)
		}
		canonical, encodeErr := service.encodeSearchCursor(cursor.Generation, CatalogSearchPosition{Score: cursor.Score, DisplayName: cursor.DisplayName, ResultID: cursor.ResultID}, binding)
		if encodeErr != nil || canonical != token {
			t.Fatalf("accepted non-canonical cursor %q", token)
		}
	})
}

func TestCatalogSearchOrderingAndNormalizationHelpers(t *testing.T) {
	t.Parallel()

	if got := CatalogSearchSortName("  Beta   AGENT "); got != "beta agent" {
		t.Fatalf("normalized sort name = %q", got)
	}
	if !CatalogSearchPositionAfter(CatalogSearchPosition{Score: 4}, CatalogSearchPosition{Score: 5}) {
		t.Fatal("lower-scored result was not ordered after the cursor")
	}
	if !CatalogSearchPositionAfter(CatalogSearchPosition{Score: 5, DisplayName: "beta"}, CatalogSearchPosition{Score: 5, DisplayName: "alpha"}) {
		t.Fatal("later display name was not ordered after the cursor")
	}
	if !CatalogSearchPositionAfter(CatalogSearchPosition{Score: 5, DisplayName: "alpha", ResultID: "b"}, CatalogSearchPosition{Score: 5, DisplayName: "alpha", ResultID: "a"}) {
		t.Fatal("later result ID was not ordered after the cursor")
	}

	records := []CatalogSummary{{Release: CatalogReleaseSummary{ID: "release-a"}, ListingRevision: CatalogDisplaySummary{DisplayName: "Alpha"}, RelevanceScore: 5}}
	if got := recordsAfterPosition(records, catalogRecordPosition(records[0])); got != nil {
		t.Fatalf("records after final position = %#v", got)
	}

	kinds := normalizedSearchKinds([]Kind{KindSkill, KindAgent, KindSkill})
	if len(kinds) != 2 || kinds[0] != KindAgent || kinds[1] != KindSkill {
		t.Fatalf("normalized kinds = %#v", kinds)
	}

}

func TestCatalogSearchCursorRejectsOversizedEncodedPosition(t *testing.T) {
	t.Parallel()

	key := sha256.Sum256([]byte(t.Name()))
	service := &Service{clock: &mutableCatalogSearchClock{now: time.Date(2026, time.September, 1, 12, 0, 0, 0, time.UTC)}, cursorKey: key[:]}
	_, err := service.encodeSearchCursor("generation", CatalogSearchPosition{Score: 1, DisplayName: strings.Repeat("x", MaxCatalogCursorBytes), ResultID: "result"}, "binding")
	if ErrorCodeOf(err) != CodeInternal {
		t.Fatalf("oversized cursor returned %v", err)
	}
}

func TestCatalogSearchUsesBoundedRepositoryCursorAndPropagatesFailures(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.September, 1, 12, 0, 0, 0, time.UTC)
	key := sha256.Sum256([]byte(t.Name()))
	firstRecord := CatalogSummary{
		ListingRevision: CatalogDisplaySummary{DisplayName: "  Beta   Agent "},
		Release:         CatalogReleaseSummary{ID: "release-beta"},
		RelevanceScore:  65,
	}
	secondRecord := CatalogSummary{
		ListingRevision: CatalogDisplaySummary{DisplayName: "Gamma Agent"},
		Release:         CatalogReleaseSummary{ID: "release-gamma"},
		RelevanceScore:  55,
	}
	requests := make([]CatalogSearchRepositoryRequest, 0, 2)
	repository := &boundedSearchRepository{search: func(_ context.Context, tenantID string, request CatalogSearchRepositoryRequest) (CatalogSearchRepositoryPage, error) {
		if tenantID != "tenant-1" {
			t.Fatalf("tenant = %q", tenantID)
		}
		requests = append(requests, request)
		if len(requests) == 1 {
			return CatalogSearchRepositoryPage{Items: []CatalogSummary{firstRecord}, Total: 2, Generation: "generation-1", HasMore: true}, nil
		}
		return CatalogSearchRepositoryPage{Items: []CatalogSummary{secondRecord}, Total: 2, Generation: "generation-1"}, nil
	}}
	service := &Service{repo: repository, clock: &mutableCatalogSearchClock{now: now}, cursorKey: key[:], policy: "test-policy"}
	ctx := identityfixture.WithActor(context.Background(), identity.Actor{MembershipID: "00000000-0000-7000-8000-0000870cd836", RealmID: "00000000-0000-7000-8000-000000000099", ActorType: identity.ActorHuman, TenantID: "tenant-1", Subject: "reader-1", Roles: []identity.Role{identity.RoleReader}})
	query := SearchQuery{Text: "  invoice   agent ", Tags: []string{" Beta ", "alpha"}, License: " MIT ", Categories: []string{" TOOLS ", "finance", "tools"}, Limit: 1}

	first, err := service.Search(ctx, query)
	if err != nil || first.Total != 2 || len(first.Items) != 1 || first.Items[0].Release.ID != firstRecord.Release.ID || first.NextCursor == "" {
		t.Fatalf("first bounded page = %#v, %v", first, err)
	}
	if len(requests) != 1 || requests[0].Limit != 1 || requests[0].After != nil || requests[0].ExpectedGeneration != "" || requests[0].Query.Text != "invoice agent" || strings.Join(requests[0].Query.Tags, ",") != "alpha,beta" {
		t.Fatalf("first repository request = %#v", requests)
	}
	if requests[0].Query.License != "MIT" || strings.Join(requests[0].Query.Categories, ",") != "finance,tools" {
		t.Fatalf("Listing filters were not normalized: %+v", requests[0].Query)
	}
	for _, changed := range []SearchQuery{
		{Text: query.Text, Tags: query.Tags, License: "mit", Categories: query.Categories, Limit: 1, Cursor: first.NextCursor},
		{Text: query.Text, Tags: query.Tags, License: query.License, Categories: []string{"tools"}, Limit: 1, Cursor: first.NextCursor},
	} {
		if _, err := service.Search(ctx, changed); ErrorCodeOf(err) != CodeInvalidArgument || len(requests) != 1 {
			t.Fatalf("changed Listing criteria reached the repository: calls=%d error=%v", len(requests), err)
		}
	}

	query.Cursor = first.NextCursor
	query.License, query.Categories = "MIT", []string{"finance", "tools"}
	second, err := service.Search(ctx, query)
	if err != nil || second.Total != 2 || len(second.Items) != 1 || second.Items[0].Release.ID != secondRecord.Release.ID || second.NextCursor != "" {
		t.Fatalf("second bounded page = %#v, %v", second, err)
	}
	if len(requests) != 2 || requests[1].ExpectedGeneration != "generation-1" || requests[1].After == nil || *requests[1].After != catalogRecordPosition(firstRecord) {
		t.Fatalf("continuation repository request = %#v", requests)
	}
	if requests[1].Query.License != "MIT" || strings.Join(requests[1].Query.Categories, ",") != "finance,tools" {
		t.Fatalf("continuation lost Listing filters: %+v", requests[1].Query)
	}

	dependencyFailure := errors.New("search repository unavailable")
	repository.search = func(context.Context, string, CatalogSearchRepositoryRequest) (CatalogSearchRepositoryPage, error) {
		return CatalogSearchRepositoryPage{}, dependencyFailure
	}
	if _, err := service.Search(ctx, SearchQuery{Text: "other"}); !errors.Is(err, dependencyFailure) {
		t.Fatalf("repository failure returned %v", err)
	}
}

func catalogSearchCursorTokenForTest(payload []byte, binding string, key []byte) string {
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte("catalog-search-cursor-v2\x00"))
	_, _ = mac.Write(payload)
	_, _ = mac.Write([]byte("\x00" + binding))
	return base64.RawURLEncoding.EncodeToString(payload) + "." + hex.EncodeToString(mac.Sum(nil))
}

// Signed payloads isolate each field check from signature and JSON failures.
func TestCatalogSearchCursorSignedPayloadBounds(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, time.September, 1, 12, 0, 0, 0, time.UTC)
	service := &Service{clock: &mutableCatalogSearchClock{now: now}, cursorKey: []byte(strings.Repeat("k", 32))}
	valid := catalogSearchCursor{Version: 2, Generation: "generation", IndexedGeneration: "index", Score: 73, DisplayName: "name", ResultID: "local:release", ExpiresAt: now.Add(time.Minute).Unix()}
	for _, tc := range []struct {
		name     string
		change   func(*catalogSearchCursor)
		accepted bool
	}{
		{"valid", func(*catalogSearchCursor) {}, true},
		{"generation limit", func(c *catalogSearchCursor) { c.Generation = strings.Repeat("g", 128) }, true},
		{"index limit", func(c *catalogSearchCursor) { c.IndexedGeneration = strings.Repeat("x", 128) }, true},
		{"name limit", func(c *catalogSearchCursor) { c.DisplayName = strings.Repeat("n", 640) }, true},
		{"ID limit", func(c *catalogSearchCursor) { c.ResultID = strings.Repeat("i", 256) }, true},
		{"zero score", func(c *catalogSearchCursor) { c.Score = 0 }, true},
		{"maximum score", func(c *catalogSearchCursor) { c.Score = 100 }, true},
		{"wrong version", func(c *catalogSearchCursor) { c.Version = 1 }, false},
		{"missing generation", func(c *catalogSearchCursor) { c.Generation = "" }, false},
		{"oversized generation", func(c *catalogSearchCursor) { c.Generation = strings.Repeat("g", 129) }, false},
		{"oversized index", func(c *catalogSearchCursor) { c.IndexedGeneration = strings.Repeat("x", 129) }, false},
		{"negative score", func(c *catalogSearchCursor) { c.Score = -1 }, false},
		{"oversized score", func(c *catalogSearchCursor) { c.Score = 101 }, false},
		{"missing name", func(c *catalogSearchCursor) { c.DisplayName = "" }, false},
		{"oversized name", func(c *catalogSearchCursor) { c.DisplayName = strings.Repeat("n", 641) }, false},
		{"missing ID", func(c *catalogSearchCursor) { c.ResultID = "" }, false},
		{"oversized ID", func(c *catalogSearchCursor) { c.ResultID = strings.Repeat("i", 257) }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			candidate := valid
			tc.change(&candidate)
			raw, err := json.Marshal(candidate)
			if err != nil {
				t.Fatal(err)
			}
			token := catalogSearchCursorTokenForTest(raw, "binding", service.cursorKey)
			if len(token) > MaxCatalogCursorBytes {
				t.Fatal("field fixture exceeds envelope bound")
			}
			got, present, err := service.decodeSearchCursor(token, "binding")
			if tc.accepted {
				if err != nil || !present || got != candidate {
					t.Fatalf("valid signed cursor = %+v, %t, %v", got, present, err)
				}
			} else if ErrorCodeOf(err) != CodeInvalidArgument || present || got != (catalogSearchCursor{}) {
				t.Fatalf("invalid signed cursor = %+v, %t, %v", got, present, err)
			}
		})
	}
	raw, err := json.Marshal(valid)
	if err != nil {
		t.Fatal(err)
	}
	unknown := append(append([]byte{}, raw[:len(raw)-1]...), []byte(`,"unknown":true}`)...)
	if _, present, err := service.decodeSearchCursor(catalogSearchCursorTokenForTest(unknown, "binding", service.cursorKey), "binding"); ErrorCodeOf(err) != CodeInvalidArgument || present {
		t.Fatalf("signed unknown field accepted: %v", err)
	}
}

func TestCatalogSearchCursorExactWireBound(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, time.September, 1, 12, 0, 0, 0, time.UTC)
	service := &Service{clock: &mutableCatalogSearchClock{now: now}, cursorKey: []byte(strings.Repeat("k", 32))}
	cursor := catalogSearchCursor{Version: 2, Generation: "generation", Score: 73, ResultID: "local:release", ExpiresAt: now.Add(catalogSearchCursorTTL).Unix()}
	raw, err := json.Marshal(cursor)
	if err != nil {
		t.Fatal(err)
	}
	// '<' uses six JSON bytes. Keep the decoded name bounded while filling
	// the wire envelope, which includes raw base64, one dot and a 64-byte MAC.
	remaining := (MaxCatalogCursorBytes-65)*3/4 - len(raw)
	cursor.DisplayName = strings.Repeat("<", remaining/6) + strings.Repeat("n", remaining%6)
	position := CatalogSearchPosition{Score: cursor.Score, DisplayName: cursor.DisplayName, ResultID: cursor.ResultID}
	token, err := service.encodeSearchCursor(cursor.Generation, position, "binding")
	if err != nil || len(token) != MaxCatalogCursorBytes {
		t.Fatalf("exact encoded bound: bytes=%d, %v", len(token), err)
	}
	got, present, err := service.decodeSearchCursor(token, "binding")
	if err != nil || !present || got != cursor {
		t.Fatalf("exact bound round trip: %+v, %t, %v", got, present, err)
	}
	position.DisplayName += "n"
	if _, err := service.encodeSearchCursor(cursor.Generation, position, "binding"); ErrorCodeOf(err) != CodeInternal {
		t.Fatalf("oversized encoding: %v", err)
	}
	cursor.DisplayName = position.DisplayName
	raw, err = json.Marshal(cursor)
	if err != nil {
		t.Fatal(err)
	}
	oversized := catalogSearchCursorTokenForTest(raw, "binding", service.cursorKey)
	if len(oversized) != MaxCatalogCursorBytes+1 {
		t.Fatal("oversized fixture has wrong wire length")
	}
	if _, present, err := service.decodeSearchCursor(oversized, "binding"); ErrorCodeOf(err) != CodeInvalidArgument || present {
		t.Fatalf("oversized decoding: %v", err)
	}
}
