//go:build functional

package fetcher

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
	"time"
)

func gzipPeer(t *testing.T, handler http.HandlerFunc) (*Fetcher, string) {
	t.Helper()
	peer := httptest.NewTLSServer(handler)
	t.Cleanup(peer.Close)
	roots := x509.NewCertPool()
	roots.AddCert(peer.Certificate())
	origin, err := NormalizeOrigin(peer.URL, false)
	if err != nil {
		t.Fatal(err)
	}
	f, err := New(Policy{Timeout: 5 * time.Second, MaxResponseBytes: 1024, RootCAs: roots,
		AllowedOrigins: map[string]struct{}{origin: {}}, AllowTestAddresses: true,
		AllowedAddressPrefixes: []netip.Prefix{netip.MustParsePrefix("127.0.0.0/8")}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(f.client.CloseIdleConnections)
	return f, peer.URL
}

func compressedFixture(t *testing.T, raw []byte) []byte {
	t.Helper()
	var output bytes.Buffer
	w := gzip.NewWriter(&output)
	if _, err := w.Write(raw); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return output.Bytes()
}

func TestActualTransportGZIPDecodedLimitsAndIntegrity(t *testing.T) {
	for _, variant := range []string{"exact", "over", "checksum", "truncated"} {
		t.Run(variant, func(t *testing.T) {
			size := 1024
			if variant == "over" {
				size++
			} else if variant != "exact" {
				size = 64 // A size refusal must not mask the decoder's read error.
			}
			raw := []byte(`{"v":"` + strings.Repeat("x", size-8) + `"}`)
			encoded := compressedFixture(t, raw)
			if len(raw) != size || len(encoded) >= 1024 {
				t.Fatal("invalid independently sized gzip fixture")
			}
			if variant == "checksum" {
				encoded[len(encoded)-8] ^= 1
			} else if variant == "truncated" {
				encoded = encoded[:len(encoded)-4]
			}
			f, origin := gzipPeer(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Accept-Encoding") != "gzip" {
					t.Errorf("native transport did not negotiate gzip: %q", r.Header.Get("Accept-Encoding"))
				}
				w.Header().Set("Content-Encoding", "gzip")
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write(encoded)
			})
			got, err := f.Fetch(t.Context(), Request{URL: origin})
			switch variant {
			case "exact":
				if err != nil || !bytes.Equal(got.Body, raw) || got.BodyDigest != fmt.Sprintf("sha256:%x", sha256.Sum256(raw)) || got.MediaType != "application/json" {
					t.Fatalf("exact decoded bytes/digest: %+v %v", got, err)
				}
			case "over":
				if err == nil || !strings.Contains(err.Error(), "decoded size limit") {
					t.Fatalf("missing decoded limit refusal: %v", err)
				}
			default:
				if err == nil || !strings.Contains(err.Error(), "read fetched body") {
					t.Fatalf("missing decoder integrity refusal: %v", err)
				}
				if variant == "checksum" && !errors.Is(err, gzip.ErrChecksum) || variant == "truncated" && !errors.Is(err, io.ErrUnexpectedEOF) {
					t.Fatalf("decoder did not reach the intended integrity guard: %v", err)
				}
			}
		})
	}
}

func TestActualTransportGZIPClosesHeldResponseOnAbortAndLimit(t *testing.T) {
	for _, over := range []bool{false, true} {
		t.Run(fmt.Sprint("over-limit-", over), func(t *testing.T) {
			ready, closed := make(chan struct{}), make(chan struct{})
			release := make(chan struct{})
			defer close(release) // Release the handler before the peer's cleanup.
			f, origin := gzipPeer(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/healthy" {
					_, _ = w.Write([]byte(`{"ok":true}`))
					return
				}
				w.Header().Set("Content-Encoding", "gzip")
				writer := gzip.NewWriter(w)
				count := 64
				if over {
					count = 1025
				}
				_, _ = writer.Write(bytes.Repeat([]byte("x"), count))
				_ = writer.Flush()
				flusher, ok := w.(http.Flusher)
				if !ok {
					t.Error("owned TLS response does not support streaming flush")
					return
				}
				flusher.Flush()
				close(ready)
				<-r.Context().Done()
				close(closed)
				// Keep HTTP completion from turning the unfinished gzip into EOF
				// while the client is still classifying its own cancellation.
				<-release
			})
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			result := make(chan error, 1)
			go func() { _, err := f.Fetch(ctx, Request{URL: origin}); result <- err }()
			select {
			case <-ready:
			case <-time.After(5 * time.Second):
				t.Fatal("held stream never became ready")
			}
			if !over {
				cancel()
			}
			select {
			case err := <-result:
				if over && (err == nil || !strings.Contains(err.Error(), "decoded size limit")) || !over && !errors.Is(err, context.Canceled) {
					t.Fatalf("wrong held-stream refusal: %v", err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("held fetch did not finish")
			}
			select {
			case <-closed:
			case <-time.After(5 * time.Second):
				t.Fatal("response stream was not closed")
			}
			if got, err := f.Fetch(t.Context(), Request{URL: origin + "/healthy"}); err != nil || string(got.Body) != `{"ok":true}` {
				t.Fatalf("explicit subsequent healthy request failed: %+v %v", got, err)
			}
		})
	}
}

func TestActualTransportArchiveIdentityIsDistinctFromGZIPFormat(t *testing.T) {
	archive := compressedFixture(t, []byte("opaque synthetic archive content"))
	f, origin := gzipPeer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Accept-Encoding") != "identity" {
			t.Errorf("archive did not request identity encoding: %q", r.Header.Get("Accept-Encoding"))
		}
		if r.URL.Path == "/encoded" {
			w.Header().Set("Content-Encoding", "gzip")
		}
		w.Header().Set("Content-Type", "application/gzip")
		_, _ = w.Write(archive)
	})
	got, err := f.Fetch(t.Context(), Request{URL: origin, IdentityEncoding: true})
	if err != nil || !bytes.Equal(got.Body, archive) || got.BodyDigest != fmt.Sprintf("sha256:%x", sha256.Sum256(archive)) {
		t.Fatalf("opaque gzip-format archive changed: %+v %v", got, err)
	}
	if _, err := f.Fetch(t.Context(), Request{URL: origin + "/encoded", IdentityEncoding: true}); err == nil || !strings.Contains(err.Error(), "unsupported HTTP content encoding") {
		t.Fatalf("HTTP-encoded archive accepted: %v", err)
	}
}
