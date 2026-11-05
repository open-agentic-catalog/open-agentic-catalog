// Unit tests exercise isolated validation and controlled dependencies.
package httpserver

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
)

type discoveryAuthenticator map[string]identity.Actor

func (authenticator discoveryAuthenticator) Authenticate(_ context.Context, token string) (identity.Actor, error) {
	actor, ok := authenticator[token]
	if !ok {
		return identity.Actor{}, errors.New("invalid test token")
	}
	return actor, nil
}

func TestDiscoveryHostNormalizationIsExact(t *testing.T) {
	t.Parallel()
	maxHost := strings.Repeat("a", 63) + "." + strings.Repeat("b", 63) + "." + strings.Repeat("c", 63) + "." + strings.Repeat("d", 61)
	for input, expected := range map[string]string{
		"catalog.example.com":     "catalog.example.com",
		"CATALOG.EXAMPLE.COM:443": "catalog.example.com",
		"127.0.0.1:4555":          "127.0.0.1",
		"[2001:0db8:0:0:0:0:0:1]": "2001:db8::1",
		"[2001:db8::1]:443":       "2001:db8::1",
		"2001:db8::1":             "2001:db8::1",
		"a-b.example:65535":       "a-b.example",
		maxHost:                   maxHost,
	} {
		t.Run(input, func(t *testing.T) {
			if normalized, ok := normalizeDiscoveryHost(input); !ok || normalized != expected {
				t.Fatalf("normalizeDiscoveryHost(%q) = %q, %t; want %q", input, normalized, ok, expected)
			}
		})
	}
	for _, input := range []string{"", " catalog.example.com", "catalog.example.com.", "catalog.example.com:0", "catalog.example.com:", "127.000.0.1", "user@catalog.example.com", "catalog.example.com/path", "one.example,two.example", "catalog.example.com:65536", "[not-an-ip]", "[::1", "a:b:c", ":443", "a..example", "-a.example", "a-.example", "a_b.example", "caf\u00e9.example", strings.Repeat("a", 64) + ".example", maxHost + "e"} {
		t.Run("reject "+input, func(t *testing.T) {
			if normalized, ok := normalizeDiscoveryHost(input); ok {
				t.Fatalf("normalizeDiscoveryHost(%q) accepted %q", input, normalized)
			}
		})
	}
}
