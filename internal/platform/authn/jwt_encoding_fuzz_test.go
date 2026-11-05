package authn

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"
)

func FuzzValidateJWTEncoding(f *testing.F) {
	for _, test := range jwtEncodingRefusals() {
		if err := validateJWTEncoding(test.token); err == nil {
			f.Fatalf("accepted known-invalid seed: %s", test.name)
		}
		f.Add([]byte(test.token))
	}
	for _, raw := range []string{`{}`, `[]`, `null`, `true`, `{"sub":"synthetic"}`, `{"sub":"\ud83d\ude42"}`, `{"sub":"\ud800"}`, "{\"sub\":\"\xff\"}"} {
		f.Add([]byte(raw))
	}
	// These controls isolate the exact byte limits from JSON syntax and framing.
	for _, size := range []int{maxAccessTokenBytes - 1, maxAccessTokenBytes, maxAccessTokenBytes + 1} {
		prefix := compactJWTForTest([]byte(`{}`), []byte(`{}`), "")
		token := prefix + strings.Repeat("s", size-len(prefix))
		if err := validateJWTEncoding(token); (err == nil) != (size <= maxAccessTokenBytes) {
			f.Fatalf("whole-token boundary %d: %v", size, err)
		}
		f.Add([]byte(token))
	}
	for _, size := range []int{maxAccessTokenPartBytes - 1, maxAccessTokenPartBytes, maxAccessTokenPartBytes + 1} {
		raw := []byte(`{}` + strings.Repeat(" ", size-2))
		for _, token := range []string{compactJWTForTest(raw, []byte(`{}`), "opaque"), compactJWTForTest([]byte(`{}`), raw, "opaque")} {
			if len(token) > maxAccessTokenBytes {
				f.Fatal("part boundary is masked by the whole-token limit")
			}
			if err := validateJWTEncoding(token); (err == nil) != (size <= maxAccessTokenPartBytes) {
				f.Fatalf("decoded-part boundary %d: %v", size, err)
			}
			f.Add([]byte(token))
		}
		f.Add(raw)
	}
	f.Fuzz(func(t *testing.T, raw []byte) {
		err := validateJWTEncoding(string(raw))
		if len(raw) > maxAccessTokenBytes {
			if err == nil {
				t.Fatal("accepted an oversized raw token")
			}
			return
		}
		if err == nil && bytes.Count(raw, []byte(".")) != 2 {
			t.Fatal("accepted invalid compact framing")
		}
		// Preserve arbitrary document bytes, including invalid Unicode, when
		// placing the fuzz input in either encoded part. The other part is valid.
		for _, token := range []string{compactJWTForTest(raw, []byte(`{}`), "opaque"), compactJWTForTest([]byte(`{}`), raw, "opaque")} {
			err := validateJWTEncoding(token)
			if len(raw) == 0 || len(raw) > maxAccessTokenPartBytes {
				if err == nil {
					t.Fatal("accepted an empty or oversized decoded part")
				}
			} else if err == nil && (!utf8.Valid(raw) || !json.Valid(raw)) {
				t.Fatal("accepted invalid Unicode or JSON syntax")
			}
		}
		if len(raw) > 4096 {
			return // The raw token and both decoded-part limits were checked first.
		}
		// Base64 text gives arbitrary bytes a lossless, JSON-safe spelling.
		// It does not replace malformed input with repaired Unicode.
		opaque := base64.RawURLEncoding.EncodeToString(raw)
		valid := []byte(`{"opaque":"` + opaque + `"}`)
		for _, signature := range []string{"", "not-base64!"} {
			if err := validateJWTEncoding(compactJWTForTest(valid, valid, signature)); err != nil {
				t.Fatalf("rejected generated valid encoding: %v", err)
			}
		}
		// The independent syntax oracle above cannot detect duplicate names or
		// surrogate repair. These deliberately invalid transformations must fail.
		for _, invalid := range [][]byte{
			[]byte(`{"opaque":"` + opaque + `","opaque":0}`),
			[]byte(`{"opaque":"` + opaque + `","\u006fpaque":0}`),
			append(bytes.Clone(valid), []byte(" true")...),
			[]byte("{\"opaque\":\"\xff\"}"),
			[]byte(`{"opaque":"\ud800"}`),
		} {
			for _, token := range []string{compactJWTForTest(invalid, valid, "opaque"), compactJWTForTest(valid, invalid, "opaque")} {
				if err := validateJWTEncoding(token); err == nil {
					t.Fatal("accepted a deliberately invalid encoded JSON part")
				}
			}
		}
	})
}

func compactJWTForTest(header, payload []byte, signature string) string {
	return base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(payload) + "." + signature
}

// These are encoding refusals, independently of algorithm, claims or signatures.
func jwtEncodingRefusals() []struct{ name, token string } {
	valid := base64.RawURLEncoding.EncodeToString([]byte(`{}`))
	prefix := valid + "." + valid + "."
	parts := []struct{ name, encoded string }{
		{"empty part", ""},
		{"invalid base64", "%"},
		{"padded base64", valid + "="},
		{"partial base64 decode", base64.RawURLEncoding.EncodeToString([]byte("{} ")) + "!"},
		{"malformed JSON", base64.RawURLEncoding.EncodeToString([]byte(`{"sub":`))},
		{"duplicate names", base64.RawURLEncoding.EncodeToString([]byte(`{"sub":"one","sub":"two"}`))},
		{"escaped duplicate names", base64.RawURLEncoding.EncodeToString([]byte(`{"sub":"one","\u0073ub":"two"}`))},
		{"trailing JSON", base64.RawURLEncoding.EncodeToString([]byte(`{} true`))},
		{"invalid UTF-8", base64.RawURLEncoding.EncodeToString([]byte("{\"sub\":\"\xff\"}"))},
		{"lone high surrogate", base64.RawURLEncoding.EncodeToString([]byte(`{"sub":"\ud800"}`))},
		{"lone low surrogate", base64.RawURLEncoding.EncodeToString([]byte(`{"sub":"\udc00"}`))},
		{"oversized valid JSON", base64.RawURLEncoding.EncodeToString([]byte(`{}` + strings.Repeat(" ", maxAccessTokenPartBytes-1)))},
	}
	cases := make([]struct{ name, token string }, 0, 5+2*len(parts))
	cases = append(cases, []struct{ name, token string }{
		{"empty token", ""},
		{"one segment", valid},
		{"two segments", valid + "." + valid},
		{"four segments", valid + "." + valid + ".opaque.extra"},
		{"oversized token", prefix + strings.Repeat("s", maxAccessTokenBytes+1-len(prefix))},
	}...)
	for _, part := range parts {
		cases = append(cases,
			struct{ name, token string }{"header: " + part.name, part.encoded + "." + valid + ".opaque"},
			struct{ name, token string }{"payload: " + part.name, valid + "." + part.encoded + ".opaque"},
		)
	}
	return cases
}
