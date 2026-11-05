package catalog

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestLiveCursorBoundsAndBinding(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	key := []byte(strings.Repeat("k", 32))
	state := LiveCursor{Version: 1, Surface: "control", Generation: strings.Repeat("1", 128), IndexedGeneration: strings.Repeat("2", 128), Position: &CatalogSearchPosition{Score: 100, DisplayName: strings.Repeat("😀", 160), ResultID: "indexed:v1:sha256:" + strings.Repeat("a", 64)}, LiveOffset: 49, Fingerprint: strings.Repeat("a", 64), ExpiresAt: now.Add(15 * time.Minute).Unix()}
	token, err := EncodeLiveCursor(state, key, "tenant/principal/query")
	if err != nil || len(token) > 4096 {
		t.Fatalf("maximum valid position: %d %v", len(token), err)
	}
	decoded, err := DecodeLiveCursor(token, "control", key, "tenant/principal/query", now)
	if err != nil || !reflect.DeepEqual(state, decoded) {
		t.Fatalf("round trip: %+v %v", decoded, err)
	}
	for _, tc := range []struct {
		name, token, surface, binding string
		at                            time.Time
	}{
		{"tampered", token[:len(token)-1] + "x", "control", "tenant/principal/query", now},
		{"padded base64", strings.Replace(token, ".", ".=", 1), "control", "tenant/principal/query", now},
		{"other surface", token, "public", "tenant/principal/query", now},
		{"other binding", token, "control", "other tenant/principal/query", now},
		{"expired", token, "control", "tenant/principal/query", now.Add(15 * time.Minute)},
		{"future expiry", token, "control", "tenant/principal/query", now.Add(-time.Second)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := DecodeLiveCursor(tc.token, tc.surface, key, tc.binding, tc.at); ErrorCodeOf(err) != CodeInvalidArgument {
				t.Fatalf("accepted: %v", err)
			}
		})
	}
	// Sign independent wire payloads so malformed tests reach the shape/size guard.
	sign := func(raw []byte) string {
		mac := hmac.New(sha256.New, key)
		_, _ = mac.Write([]byte("live-client-cursor-v1\x00binding\x00"))
		_, _ = mac.Write(raw)
		return LiveCursorPrefix + base64.RawURLEncoding.EncodeToString(raw) + "." + hex.EncodeToString(mac.Sum(nil))
	}
	raw, marshalErr := json.Marshal(LiveCursor{Version: 1, Surface: "public", Generation: "1", LiveOffset: 1, Fingerprint: strings.Repeat("a", 64), ExpiresAt: now.Add(time.Minute).Unix()})
	if marshalErr != nil {
		t.Fatal(marshalErr)
	}
	for _, tc := range []struct {
		bytes, length int
		valid         bool
	}{{3020, 4096, true}, {3021, 4097, false}} {
		tok := sign(append(append([]byte{}, raw...), []byte(strings.Repeat(" ", tc.bytes-len(raw)))...))
		if len(tok) != tc.length {
			t.Fatalf("fixture size %d expected %d", len(tok), tc.length)
		}
		_, err := DecodeLiveCursor(tok, "public", key, "binding", now)
		if (err == nil) != tc.valid {
			t.Fatalf("boundary %d: %v", len(tok), err)
		}
	}
	for _, bad := range []string{
		strings.Replace(string(raw), `"v":1`, `"v":1,"v":1`, 1),
		strings.Replace(string(raw), `"v":1`, `"V":1`, 1),
		strings.Replace(string(raw), `"l":1`, `"l":51`, 1),
		strings.Replace(string(raw), `"l":1`, `"l":-1`, 1),
		strings.Replace(string(raw), `"g":"1"`, `"g":""`, 1),
		strings.Replace(string(raw), `"v":1`, `"v":1,"unknown":true`, 1),
	} {
		if _, err := DecodeLiveCursor(sign([]byte(bad)), "public", key, "binding", now); ErrorCodeOf(err) != CodeInvalidArgument {
			t.Fatalf("accepted malformed %s: %v", bad, err)
		}
	}
	if _, err := EncodeLiveCursor(LiveCursor{Generation: strings.Repeat("x", 4096)}, key, "binding"); ErrorCodeOf(err) != CodeInternal {
		t.Fatalf("oversize encoder: %v", err)
	}
}

func TestLiveCursorModeSeparation(t *testing.T) {
	now := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	key := []byte(strings.Repeat("k", 32))
	base := LiveCursor{Surface: "control", Generation: "1", IndexedGeneration: "1", ExpiresAt: now.Add(time.Minute).Unix(), Fingerprint: strings.Repeat("a", 64)}
	hybrid := base
	hybrid.Mode, hybrid.Ranking, hybrid.VectorFingerprint = "hybrid", "sha256:"+strings.Repeat("b", 64), strings.Repeat("c", 64)
	hybrid.HybridPosition = &SemanticSearchPosition{Rank: 1e12, SortName: strings.Repeat("x", 640), ID: strings.Repeat("i", 256)}
	live := base
	live.Mode = "live"
	for _, state := range []LiveCursor{base, hybrid, live} {
		token, err := EncodeLiveCursor(state, key, "binding")
		if err != nil {
			t.Fatal(err)
		}
		got, err := DecodeLiveCursor(token, "control", key, "binding", now)
		state.Version = 1
		if err != nil || !reflect.DeepEqual(got, state) {
			t.Fatalf("mode roundtrip: %+v %v", got, err)
		}
	}
	for _, change := range []func(*LiveCursor){
		func(s *LiveCursor) { s.Mode = "unknown" },
		func(s *LiveCursor) { s.Mode = "" },
		func(s *LiveCursor) { s.Mode = "live" },
		func(s *LiveCursor) { s.Position = &CatalogSearchPosition{Score: 1, DisplayName: "a", ResultID: "a"} },
		func(s *LiveCursor) { s.Fingerprint = "" },
		func(s *LiveCursor) { s.Ranking = "" },
		func(s *LiveCursor) { s.Ranking = strings.Repeat("x", 257) },
		func(s *LiveCursor) { s.VectorFingerprint = strings.Repeat("A", 64) },
		func(s *LiveCursor) { s.HybridPosition = &SemanticSearchPosition{Rank: 0, ID: "a"} },
		func(s *LiveCursor) { s.HybridPosition = &SemanticSearchPosition{Rank: 1e12 + 1, ID: "a"} },
		func(s *LiveCursor) { s.HybridPosition = &SemanticSearchPosition{Rank: 1, ID: ""} },
		func(s *LiveCursor) { s.HybridPosition = &SemanticSearchPosition{Rank: 1, ID: strings.Repeat("i", 257)} },
		func(s *LiveCursor) {
			s.HybridPosition = &SemanticSearchPosition{Rank: 1, ID: "a", SortName: strings.Repeat("n", 641)}
		},
		func(s *LiveCursor) { s.Surface = "public"; s.IndexedGeneration = "" },
	} {
		bad := hybrid
		change(&bad)
		token, err := EncodeLiveCursor(bad, key, "binding")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := DecodeLiveCursor(token, bad.Surface, key, "binding", now); ErrorCodeOf(err) != CodeInvalidArgument {
			t.Fatalf("accepted mode confusion: %+v %v", bad, err)
		}
	}
}

func TestLiveCursorRejectsInvalidPositionsAndEncodings(t *testing.T) {
	now := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	key := []byte(strings.Repeat("k", 32))
	baseline := LiveCursor{Surface: "control", Generation: "42", IndexedGeneration: "42", Fingerprint: strings.Repeat("a", 64), ExpiresAt: now.Add(time.Minute).Unix()}
	for _, mutate := range []func(*LiveCursor){
		func(c *LiveCursor) { c.IndexedGeneration = "" },
		func(c *LiveCursor) { c.Offset = 1 },
		func(c *LiveCursor) { c.Position = &CatalogSearchPosition{Score: -1, DisplayName: "x", ResultID: "i"} },
		func(c *LiveCursor) { c.Position = &CatalogSearchPosition{Score: 101, DisplayName: "x", ResultID: "i"} },
		func(c *LiveCursor) {
			c.Position = &CatalogSearchPosition{Score: 50, DisplayName: strings.Repeat("x", 641), ResultID: "i"}
		},
		func(c *LiveCursor) { c.Fingerprint = strings.Repeat("z", 64) },
		func(c *LiveCursor) { c.Fingerprint = strings.Repeat("A", 64) },
		func(c *LiveCursor) { c.Fingerprint = ""; c.LiveOffset = 1 },
	} {
		bad := baseline
		mutate(&bad)
		token, err := EncodeLiveCursor(bad, key, "binding")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := DecodeLiveCursor(token, "control", key, "binding", now); ErrorCodeOf(err) != CodeInvalidArgument {
			t.Fatalf("accepted invalid position %+v", bad)
		}
	}
	pub := baseline
	pub.Surface = "public"
	token, err := EncodeLiveCursor(pub, key, "binding")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeLiveCursor(token, "public", key, "binding", now); ErrorCodeOf(err) != CodeInvalidArgument {
		t.Fatalf("public accepted private durable position: %v", err)
	}
	for _, token := range []string{"lc1.a.b.c", "lc1.!." + strings.Repeat("a", 64), "lc1.e30." + strings.Repeat("A", 64), "lc1.e30=." + strings.Repeat("a", 64)} {
		if _, err := DecodeLiveCursor(token, "control", key, "binding", now); ErrorCodeOf(err) != CodeInvalidArgument {
			t.Fatalf("invalid encoding: %v", err)
		}
	}
}
