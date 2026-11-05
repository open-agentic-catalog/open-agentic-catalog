package fetcher

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestArchiveIdentityEncodingPreservesOrRejectsTransportBytes(t *testing.T) {
	for _, tc := range []struct {
		name           string
		encodings      []string
		decoded, valid bool
	}{
		{"unchanged", nil, false, true}, {"identity", []string{"identity"}, false, true}, {"gzip transfer", []string{"gzip"}, false, false}, {"already decoded", nil, true, false}, {"split headers", []string{"identity", "gzip"}, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, err := New(Policy{Timeout: time.Second, MaxResponseBytes: 1024, AllowHTTPForTests: true}, fixedResolver{})
			if err != nil {
				t.Fatal(err)
			}
			const archive = "\x1f\x8b\x00binary gzip archive"
			f.client.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
				if r.Header.Get("Accept-Encoding") != "identity" {
					t.Fatal("archive request negotiated decoding")
				}
				h := make(http.Header)
				h["Content-Encoding"] = tc.encodings
				return &http.Response{StatusCode: http.StatusOK, Header: h, Body: io.NopCloser(strings.NewReader(archive)), Request: r, Uncompressed: tc.decoded}, nil
			})
			result, err := f.Fetch(context.Background(), Request{URL: "http://example.com/plugin.tar.gz", IdentityEncoding: true})
			if (err == nil) != tc.valid || tc.valid && string(result.Body) != archive {
				t.Fatalf("body=%q err=%v", result.Body, err)
			}
		})
	}
}
