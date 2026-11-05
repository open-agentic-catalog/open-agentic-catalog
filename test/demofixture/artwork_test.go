package demofixture

import (
	"bytes"
	"github.com/open-agentic-catalog/open-agentic-catalog/samples"
	"net/http/httptest"
	"testing"
)

func TestOwnedArtworkFixture(t *testing.T) {
	for _, tc := range []struct{ path, file, mime string }{{"/artwork-icon.png", "artwork/icon.png", "image/png"}, {"/artwork-screen.jpg", "artwork/screenshot.jpg", "image/jpeg"}} {
		expected, err := samples.Files.ReadFile(tc.file)
		if err != nil {
			t.Fatal(err)
		}
		request := httptest.NewRequestWithContext(t.Context(), "GET", tc.path, nil)
		response := httptest.NewRecorder()
		Handler().ServeHTTP(response, request)
		if response.Code != 200 || response.Header().Get("Content-Type") != tc.mime || !bytes.Equal(response.Body.Bytes(), expected) {
			t.Fatalf("owned image differs: %s", tc.path)
		}
		for _, suffix := range []string{"?token=secret", "?"} {
			r := httptest.NewRequestWithContext(t.Context(), "GET", tc.path+suffix, nil)
			w := httptest.NewRecorder()
			Handler().ServeHTTP(w, r)
			if w.Code != 404 {
				t.Fatal("query-bearing artwork accepted")
			}
		}
		for _, header := range []string{"Authorization", "Cookie"} {
			r := httptest.NewRequestWithContext(t.Context(), "GET", tc.path, nil)
			r.Header.Set(header, "synthetic-secret")
			w := httptest.NewRecorder()
			Handler().ServeHTTP(w, r)
			if w.Code != 404 {
				t.Fatal("credentials accepted")
			}
		}
	}
}
