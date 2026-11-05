//go:build functional

// Functional tests retain real local HTTP and TLS exchanges.
package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestOIDCMuxServesExactLocalDiscoveryAndKeys(t *testing.T) {
	directory := t.TempDir()
	keySet := []byte(`{"keys":[{"kty":"RSA","kid":"fixture"}]}`)
	path := filepath.Join(directory, "jwks.json")
	if err := os.WriteFile(path, keySet, 0o600); err != nil {
		t.Fatal(err)
	}
	mux, err := newOIDCMux("http://identity-fixture:8081", path)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(mux)
	defer server.Close()

	for endpoint, want := range map[string]map[string]any{
		"/.well-known/openid-configuration": {"issuer": "http://identity-fixture:8081", "jwks_uri": "http://identity-fixture:8081/keys"},
		"/keys":                             {"keys": []any{map[string]any{"kty": "RSA", "kid": "fixture"}}},
	} {
		request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, server.URL+endpoint, nil)
		if err != nil {
			t.Fatal(err)
		}
		response, err := server.Client().Do(request) // #nosec G107 -- this is an invocation-owned httptest server.
		if err != nil {
			t.Fatal(err)
		}
		var got map[string]any
		decodeErr := json.NewDecoder(response.Body).Decode(&got)
		closeErr := response.Body.Close()
		if decodeErr != nil || closeErr != nil || response.StatusCode != http.StatusOK {
			t.Fatalf("GET %s: status %d, decode %v, close %v", endpoint, response.StatusCode, decodeErr, closeErr)
		}
		for key, value := range want {
			gotRaw, err := json.Marshal(got[key])
			if err != nil {
				t.Fatal(err)
			}
			wantRaw, err := json.Marshal(value)
			if err != nil {
				t.Fatal(err)
			}
			if string(gotRaw) != string(wantRaw) {
				t.Errorf("GET %s field %s = %s, want %s", endpoint, key, gotRaw, wantRaw)
			}
		}
	}
}
