// Unit tests exercise isolated validation and controlled dependencies.
package embeddings

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/config"
)

// settings binds the test server to an explicit loopback-only inference policy.
func settings(endpoint string) config.Semantic {
	s := config.SemanticDefaults()
	s.Enabled = true
	s.Endpoint = endpoint
	s.Model = "fixture"
	s.ProfileRevision = "one"
	s.Dimensions = 2
	s.AllowHTTP = true
	s.AllowedAddressPrefixes = []string{"127.0.0.0/8"}
	return s
}

// TestStartupInputFailures checks local construction without contacting inference.
func TestStartupInputFailures(t *testing.T) {
	t.Parallel()
	base := settings("https://model.invalid/v1/embeddings")
	observations := 0
	observer := func(operation, outcome string, elapsed time.Duration) {
		observations++
		if operation != "query" || outcome != "rejected" || elapsed < 0 {
			t.Errorf("unexpected observation: %s/%s (%v)", operation, outcome, elapsed)
		}
	}
	client, err := NewObserved(base, observer)
	if err != nil || client == nil {
		t.Fatalf("valid startup: client present=%v, error=%v", client != nil, err)
	}
	t.Cleanup(client.Close)
	if client.State() != "unknown" || observations != 0 {
		t.Fatal("startup reported a provider outcome")
	}
	missingCA := filepath.Join(t.TempDir(), "missing-private-ca.pem")
	for _, tc := range []struct {
		name, private string
		mutate        func(*config.Semantic)
	}{
		{"missing CA", missingCA, func(s *config.Semantic) { s.CAFile = missingCA }},
		{"denied endpoint", "169.254.169.254", func(s *config.Semantic) { s.Endpoint = "http://169.254.169.254/v1/embeddings" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := base
			tc.mutate(&s)
			if err := s.Validate(); err != nil {
				t.Fatalf("input must reach client construction: %v", err)
			}
			got, err := NewObserved(s, observer)
			if observations != 0 {
				t.Error("failed startup emitted an observation")
			}
			if got != nil {
				got.Close()
				t.Fatal("failed startup returned a client")
			}
			if err == nil || strings.Contains(err.Error(), tc.private) {
				t.Fatalf("startup returned an absent or unsafe error: %v", err)
			}
		})
	}
	if _, err := client.Query(t.Context(), ""); !errors.Is(err, ErrRejected) || observations != 1 || client.State() != "unknown" {
		t.Fatalf("installed observer: error=%v, observations=%d, state=%s", err, observations, client.State())
	}
}

// TestTLSInputFailures retains local file/configuration rejection without a server.
func TestTLSInputFailures(t *testing.T) {
	dir := t.TempDir()
	token := filepath.Join(dir, "token")
	if err := os.WriteFile(token, []byte("fixture-token\n"), 0600); err != nil {
		t.Fatal(err)
	}
	s := settings("https://model.invalid/v1/embeddings")
	s.AllowHTTP = false
	s.TokenFile = token
	for _, tc := range []struct {
		name   string
		mutate func(*config.Semantic)
	}{
		{"missing token", func(s *config.Semantic) { s.TokenFile = filepath.Join(dir, "absent") }},
		{"directory", func(s *config.Semantic) { s.TokenFile = dir }},
		{"invalid CA", func(s *config.Semantic) { s.CAFile = token }},
		{"invalid config", func(s *config.Semantic) { s.Dimensions = 0 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			next := s
			tc.mutate(&next)
			if _, err := New(next); err == nil || strings.Contains(err.Error(), dir) {
				t.Fatalf("unsafe config error: %v", err)
			}
		})
	}
	if err := os.WriteFile(token, []byte("token\nheader"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := New(s); err == nil {
		t.Fatal("accepted multiline token")
	}
	s.Enabled = false
	s.CAFile = "/absent"
	if c, err := NewObserved(s, func(string, string, time.Duration) { t.Error("disabled client emitted an observation") }); err != nil || c != nil {
		t.Fatal("disabled client loaded inputs")
	}
	var disabled *Client
	disabled.Close()
}
