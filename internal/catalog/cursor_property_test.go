package catalog

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"pgregory.net/rapid"
)

func propertyCursorService(t *rapid.T) (*Service, *mutableCatalogSearchClock) {
	now := time.Date(2026, time.September, 1, 0, 0, 0, 0, time.UTC).
		Add(time.Duration(rapid.IntRange(0, 86400).Draw(t, "clock offset")) * time.Second)
	clock := &mutableCatalogSearchClock{now: now}
	key := sha256.Sum256([]byte("property-cursor-key"))
	return &Service{clock: clock, cursorKey: key[:]}, clock
}

func propertyCursorName(t *rapid.T) string {
	// At most 12 runes, including JSON escapes, stays well below either wire
	// ceiling. Existing examples own exact byte and field limits.
	return string(rapid.SliceOfN(rapid.SampledFrom([]rune{'a', 'b', '0', '-', 'é', '中', '🙂', '"', '\\', '<'}), 1, 12).Draw(t, "sort name"))
}

func propertyCursorDigest(t *rapid.T, label string) string {
	return rapid.StringMatching(`[0-9a-f]{64}`).Draw(t, label)
}

func propertyCursorOtherBinding(binding string) string {
	// Keep the request binding a valid 64-character hex digest.
	if binding[0] == '0' {
		return "1" + binding[1:]
	}
	return "0" + binding[1:]
}

// propertyCursorTamper retains a real signature while replacing its payload
// with another structurally valid value. Schema rejection cannot mask a missing
// MAC check, unlike flipping an arbitrary byte in the encoded JSON.
func propertyCursorTamper(t *rapid.T, token string, payloadIndex int, value any) string {
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal generated cursor: %v", err)
	}
	parts := strings.Split(token, ".")
	parts[payloadIndex] = base64.RawURLEncoding.EncodeToString(raw)
	return strings.Join(parts, ".")
}

func TestPropertyLexicalCursor(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		service, clock := propertyCursorService(t)
		binding := propertyCursorDigest(t, "binding")
		want := catalogSearchCursor{
			Version: 2, Generation: propertyCursorDigest(t, "generation"),
			Score: rapid.IntRange(0, 100).Draw(t, "score"), DisplayName: propertyCursorName(t),
			ResultID:  fmt.Sprintf("local:00000000-0000-7000-8000-%012x", rapid.Uint64Range(0, 65535).Draw(t, "result")),
			ExpiresAt: clock.now.Add(15 * time.Minute).Unix(),
		}
		if rapid.Bool().Draw(t, "indexed generation present") {
			want.IndexedGeneration = propertyCursorDigest(t, "indexed generation")
		}
		position := CatalogSearchPosition{Score: want.Score, DisplayName: want.DisplayName, ResultID: want.ResultID}
		token, err := service.encodeSearchCursorGenerations(want.Generation, want.IndexedGeneration, position, binding)
		if err != nil {
			t.Fatalf("encode valid lexical cursor: %v", err)
		}
		got, present, err := service.decodeSearchCursor(token, binding)
		if err != nil || !present || got != want {
			t.Fatalf("lexical round trip: got %+v, present=%v, error=%v; want %+v", got, present, err, want)
		}
		reject := func(token, bound string) {
			got, present, err := service.decodeSearchCursor(token, bound)
			if ErrorCodeOf(err) != CodeInvalidArgument || present || got != (catalogSearchCursor{}) {
				t.Fatalf("invalid lexical cursor accepted: %+v, present=%v, error=%v", got, present, err)
			}
		}
		reject(token, propertyCursorOtherBinding(binding))
		key := service.cursorKey
		service.cursorKey = []byte(strings.Repeat("x", 32))
		reject(token, binding)
		service.cursorKey = key
		changed := want
		changed.Score = (changed.Score + 1) % 101
		reject(propertyCursorTamper(t, token, 0, changed), binding)
		clock.now = time.Unix(want.ExpiresAt, 0).Add(-time.Nanosecond)
		if got, present, err := service.decodeSearchCursor(token, binding); err != nil || !present || got != want {
			t.Fatalf("lexical cursor rejected before expiry: %+v, %v, %v", got, present, err)
		}
		clock.now = time.Unix(want.ExpiresAt, 0)
		reject(token, binding)
	})
}

func TestPropertyHybridCursor(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		service, clock := propertyCursorService(t)
		binding := propertyCursorDigest(t, "binding")
		want := semanticCursor{
			Generation:        fmt.Sprintf("abcdefab-0000-7000-8000-%012x", rapid.Uint64Range(0, 65535).Draw(t, "generation")),
			SourceGeneration:  propertyCursorDigest(t, "source generation"),
			VectorFingerprint: propertyCursorDigest(t, "vector fingerprint"),
			Position: SemanticSearchPosition{
				Rank: rapid.Int64Range(1, 1e12).Draw(t, "rank"), SortName: propertyCursorName(t),
				ID: fmt.Sprintf("local:00000000-0000-7000-8000-%012x", rapid.Uint64Range(0, 65535).Draw(t, "result")),
			},
			ExpiresAt: clock.now.Add(time.Duration(rapid.IntRange(1, 900).Draw(t, "lifetime")) * time.Second).Unix(),
		}
		token, err := service.encodeSemanticCursor(want, binding)
		if err != nil {
			t.Fatalf("encode valid hybrid cursor: %v", err)
		}
		if got, err := service.decodeSemanticCursor(token, binding); err != nil || got != want {
			t.Fatalf("hybrid round trip: got %+v, error=%v; want %+v", got, err, want)
		}
		reject := func(token, bound string) {
			got, err := service.decodeSemanticCursor(token, bound)
			if ErrorCodeOf(err) != CodeInvalidArgument || got != (semanticCursor{}) {
				t.Fatalf("invalid hybrid cursor accepted: %+v, %v", got, err)
			}
		}
		reject(token, propertyCursorOtherBinding(binding))
		key := service.cursorKey
		service.cursorKey = []byte(strings.Repeat("x", 32))
		reject(token, binding)
		service.cursorKey = key
		changed := want
		changed.Position.Rank = changed.Position.Rank%1e12 + 1
		reject(propertyCursorTamper(t, token, 1, changed), binding)
		clock.now = time.Unix(want.ExpiresAt, 0).Add(-time.Nanosecond)
		if got, err := service.decodeSemanticCursor(token, binding); err != nil || got != want {
			t.Fatalf("hybrid cursor rejected before expiry: %+v, %v", got, err)
		}
		clock.now = time.Unix(want.ExpiresAt, 0)
		reject(token, binding)
	})
}
