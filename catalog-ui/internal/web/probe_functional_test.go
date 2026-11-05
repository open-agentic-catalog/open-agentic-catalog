//go:build functional

package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestReadinessProbe(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
		ok     bool
	}{
		{"ready", 200, `{"status":"ok"}`, true},
		{"draining", 503, `{}`, false},
		{"redirect", 302, `{}`, false},
		{"invalid JSON", 200, `<html>`, false},
		{"oversized", 200, strings.Repeat(" ", 4097), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/readyz" {
					t.Errorf("unexpected probe path %s", r.URL.Path)
				}
				w.Header().Set("Location", "https://unused.invalid")
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer server.Close()
			origin, err := url.Parse(server.URL)
			if err != nil {
				t.Fatal(err)
			}
			if err := probeReadiness(t.Context(), origin); (err == nil) != tc.ok {
				t.Fatalf("probe error = %v, want success %v", err, tc.ok)
			}
			ctx, cancel := context.WithCancel(t.Context())
			cancel()
			if probeReadiness(ctx, origin) == nil {
				t.Fatal("canceled probe succeeded")
			}
		})
	}
}
