package main

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func contextForgeInputs(t *testing.T) string {
	t.Helper()
	directory := t.TempDir()
	if err := prepareContextForge([]string{"--directory", directory}); err != nil {
		t.Fatal(err)
	}
	return directory
}

func TestContextForgePreparationRefusesNonemptyAndLinkedDirectories(t *testing.T) {
	directory := t.TempDir()
	sentinel := filepath.Join(directory, "ca.pem")
	if err := os.WriteFile(sentinel, []byte("preserve existing input"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := prepareContextForge([]string{"--directory", directory}); err == nil {
		t.Fatal("accepted an existing nonempty directory")
	}
	raw, err := os.ReadFile(sentinel) // #nosec G304 -- test-owned temporary input.
	if err != nil || string(raw) != "preserve existing input" {
		t.Fatal("preparation changed an existing file", err)
	}
	if _, err := os.Stat(filepath.Join(directory, "secrets.json")); !os.IsNotExist(err) {
		t.Fatal("refused preparation wrote credentials")
	}
	link := filepath.Join(t.TempDir(), "linked")
	if err := os.Symlink(t.TempDir(), link); err != nil {
		t.Fatal(err)
	}
	if err := prepareContextForge([]string{"--directory", link}); err == nil {
		t.Fatal("accepted a linked invocation directory")
	}
}

func TestContextForgeCertificatesSupportBrowsersWithoutSharingPrivateKeys(t *testing.T) {
	directory := contextForgeInputs(t)
	caPEM, err := os.ReadFile(filepath.Join(directory, "frontend/ca.pem")) // #nosec G304 -- test-owned public certificate.
	if err != nil {
		t.Fatal(err)
	}
	block, _ := pem.Decode(caPEM)
	if block == nil {
		t.Fatal("missing CA")
	}
	ca, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(ca)
	for name, hostname := range map[string]string{"frontend": "federation-fixture", "contextforge": "contextforge"} {
		certificate, err := tls.LoadX509KeyPair(filepath.Join(directory, name, "server.pem"), filepath.Join(directory, name, "server-key.pem"))
		if err != nil {
			t.Fatal(err)
		}
		key, ok := certificate.PrivateKey.(*ecdsa.PrivateKey)
		if !ok || key.Curve != elliptic.P256() {
			t.Fatal("server key is not browser-compatible P-256")
		}
		leaf, err := x509.ParseCertificate(certificate.Certificate[0])
		if err != nil {
			t.Fatal(err)
		}
		for _, host := range []string{hostname, "127.0.0.1"} {
			if _, err := leaf.Verify(x509.VerifyOptions{Roots: roots, DNSName: host}); err != nil {
				t.Fatal("private trust or hostname verification failed", err)
			}
		}
		if _, err := leaf.Verify(x509.VerifyOptions{Roots: x509.NewCertPool(), DNSName: hostname}); err == nil {
			t.Fatal("accepted an untrusted certificate")
		}
		if _, err := leaf.Verify(x509.VerifyOptions{Roots: roots, DNSName: "unapproved.invalid"}); err == nil {
			t.Fatal("accepted a different hostname")
		}
	}
}

func TestContextForgeInputsKeepPrivateKeysAndSignedTenancySeparate(t *testing.T) {
	directory := contextForgeInputs(t)
	var secrets struct {
		Tokens            map[string]string `json:"tokens"`
		Password          string            `json:"cfPassword"`
		UnassignedDefault string            `json:"cfDefaultPassword"`
		RuntimeA          string            `json:"cfPasswordA"`
		RuntimeB          string            `json:"cfPasswordB"`
		RuntimeEverything string            `json:"cfPasswordEverything"`
	}
	raw, err := os.ReadFile(filepath.Join(directory, "secrets.json")) // #nosec G304 -- test-owned temporary input.
	if err != nil || json.Unmarshal(raw, &secrets) != nil || len(secrets.Tokens) != 13 {
		t.Fatal("missing synthetic principals and three signed refusal controls", err)
	}
	info, _ := os.Stat(filepath.Join(directory, "secrets.json"))
	if info.Mode().Perm() != 0o600 {
		t.Fatal("credentials are not private")
	}
	passwords := map[string]bool{}
	for _, password := range []string{secrets.Password, secrets.UnassignedDefault, secrets.RuntimeA, secrets.RuntimeB, secrets.RuntimeEverything} {
		if passwords[password] {
			t.Fatal("a fixture account shares a password")
		}
		passwords[password] = true
		if len(password) < 22 || !strings.Contains(password, "!") || !strings.Contains(password, "C") {
			t.Fatal("generated password lacks native length or complexity requirements")
		}
		lower := strings.ToLower(password)
		for i := 0; i+3 <= len(lower); i++ {
			for _, sequence := range []string{"0123456789", "9876543210", "abcdefghijklmnopqrstuvwxyz", "zyxwvutsrqponmlkjihgfedcba"} {
				if strings.Contains(sequence, lower[i:i+3]) {
					t.Fatal("generated password contains a prohibited native sequence")
				}
			}
		}
	}
	var jwks struct {
		Keys []struct {
			N string `json:"n"`
			E string `json:"e"`
		} `json:"keys"`
	}
	public, _ := os.ReadFile(filepath.Join(directory, "frontend/jwks.json")) // #nosec G304 -- test-owned public key.
	if err := json.Unmarshal(public, &jwks); err != nil || len(jwks.Keys) != 1 {
		t.Fatal("missing public signing key", err)
	}
	n, _ := base64.RawURLEncoding.DecodeString(jwks.Keys[0].N)
	e, _ := base64.RawURLEncoding.DecodeString(jwks.Keys[0].E)
	key := &rsa.PublicKey{N: new(big.Int).SetBytes(n), E: int(new(big.Int).SetBytes(e).Int64())}
	for principal, token := range secrets.Tokens {
		parts := strings.Split(token, ".")
		if len(parts) != 3 {
			t.Fatal("invalid compact token")
		}
		signature, _ := base64.RawURLEncoding.DecodeString(parts[2])
		digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
		if err := rsa.VerifyPKCS1v15(key, crypto.SHA256, digest[:], signature); err != nil {
			t.Fatal("invalid token signature", err)
		}
		body, _ := base64.RawURLEncoding.DecodeString(parts[1])
		var claims map[string]any
		if err := json.Unmarshal(body, &claims); err != nil {
			t.Fatal(err)
		}
		subject, audience := principal, "open-agentic-catalog"
		if principal == "a-expired" || principal == "a-audience" || principal == "a-scope" {
			subject = "a-reader"
		}
		if principal == "a-audience" {
			audience = "wrong-owned-audience"
		}
		if claims["iss"] != "https://federation-fixture:8443" || claims["aud"] != audience || claims["sub"] != subject || claims["organization_id"] != "org-"+principal[:1] {
			t.Fatal("token lost its origin or shared tenant binding")
		}
		if principal == "a-scope" && claims["scope"] != "unrelated.scope" {
			t.Fatal("scope refusal control lost its OAuth ceiling")
		}
		if principal == "a-expired" {
			expiry, ok := claims["exp"].(float64)
			if !ok || expiry >= float64(time.Now().Unix()) {
				t.Fatal("expired control is not expired")
			}
		}
		if strings.HasSuffix(principal, "reader") && claims["scope"] != "catalog.read" {
			t.Fatal("reader token has administrative scopes")
		}
		for role, scope := range map[string]string{"contributor": "catalog.read catalog.write", "reviewer": "catalog.read catalog.review", "publisher": "catalog.read catalog.publish"} {
			if strings.HasSuffix(principal, role) && claims["scope"] != scope {
				t.Fatal("publication principal has unrelated OAuth scopes")
			}
		}
	}
	before := string(raw)
	if err := prepareContextForge([]string{"--directory", directory}); err == nil {
		t.Fatal("overwrote prepared credentials")
	}
	after, _ := os.ReadFile(filepath.Join(directory, "secrets.json")) // #nosec G304 -- test-owned temporary input.
	if string(after) != before {
		t.Fatal("rejected preparation changed credentials")
	}
	front, _ := os.ReadFile(filepath.Join(directory, "frontend/server-key.pem"))  // #nosec G304 -- test-owned temporary input.
	cf, _ := os.ReadFile(filepath.Join(directory, "contextforge/server-key.pem")) // #nosec G304 -- test-owned temporary input.
	if string(front) == string(cf) || len(front) == 0 || len(cf) == 0 {
		t.Fatal("TLS servers share a private key")
	}
}

func TestContextForgeFixedProxyPreservesMCPAndConsumesOnlyTheEverythingKey(t *testing.T) {
	directory := contextForgeInputs(t)
	key, _ := os.ReadFile(filepath.Join(directory, "frontend/everything-key")) // #nosec G304 -- test-owned temporary input.
	calls := 0
	handler, err := contextForgeHandler(filepath.Join(directory, "frontend"), peerRoundTrip(func(r *http.Request) (*http.Response, error) {
		calls++
		body, _ := io.ReadAll(r.Body)
		if r.Method != http.MethodPost || string(body) != `{"jsonrpc":"2.0","id":1,"method":"prompts/get"}` || r.Header.Get("Mcp-Protocol-Version") != "2025-11-25" {
			t.Fatal("proxy changed MCP bytes or protocol headers")
		}
		if r.URL.Host == "everything:3001" {
			if r.URL.Path != "/mcp" || r.Header.Get("X-Mcp-Test-Key") != "" || r.Header.Get("Authorization") != "" {
				t.Fatal("Everything received credentials or an incorrect path")
			}
		} else if r.URL.Host != "api:8080" || r.URL.Path != "/mcp" || r.Header.Get("Authorization") != "Bearer synthetic-catalog-token" {
			t.Fatal("Catalog forwarding changed the fixed destination or credential")
		}
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Mcp-Session-Id": {"opaque-session"}}, Body: io.NopCloser(strings.NewReader(`{"result":{}}`))}, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/mcp", "/everything/mcp"} {
		r := httptest.NewRequestWithContext(t.Context(), "POST", "https://federation-fixture:8443"+path, strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"prompts/get"}`))
		r.Header.Set("Mcp-Protocol-Version", "2025-11-25")
		if path == "/mcp" {
			r.Header.Set("Authorization", "Bearer synthetic-catalog-token")
		} else {
			r.Header.Set("X-Mcp-Test-Key", string(key))
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != 200 || w.Body.String() != `{"result":{}}` || w.Header().Get("Mcp-Session-Id") != "opaque-session" {
			t.Fatal("proxy changed the response")
		}
	}
	for _, test := range []struct {
		path, key, token string
		status           int
	}{
		{"/everything/mcp", "", "", 401}, {"/everything/mcp", "wrong", "", 401},
		{"/everything/mcp", string(key), "Bearer downstream-secret", 401},
		{"/arbitrary?url=https://external.invalid", "", "", 404},
	} {
		r := httptest.NewRequestWithContext(t.Context(), "POST", test.path, nil)
		r.Header.Set("X-Mcp-Test-Key", test.key)
		if test.token != "" {
			r.Header.Set("Authorization", test.token)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != test.status {
			t.Fatal("unexpected refusal", w.Code)
		}
	}
	r := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/everything/mcp", nil)
	r.Header.Set("X-Mcp-Test-Key", string(key))
	r.Header["Authorization"] = []string{"", "Bearer second-value-secret"}
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Fatal("duplicate Authorization values reached Everything")
	}
	if calls != 2 {
		t.Fatal("a denied request reached an upstream")
	}
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/observations", nil))
	if strings.Contains(w.Body.String(), string(key)) || strings.Contains(w.Body.String(), "downstream-secret") || strings.Contains(w.Body.String(), "second-value-secret") {
		t.Fatal("observations retained a credential")
	}
	var observed struct {
		Events []contextForgeEvent `json:"events"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &observed); err != nil || len(observed.Events) != 7 {
		t.Fatal("missing accepted or denied request observations", err)
	}
	if !observed.Events[1].TestKey || observed.Events[1].Credential != "none" || observed.Events[1].Status != http.StatusOK {
		t.Fatal("missing accepted key-only request")
	}
	if observed.Events[2].TestKey || observed.Events[2].Status != http.StatusUnauthorized || !observed.Events[4].TestKey || observed.Events[4].Credential != "other" || observed.Events[4].Status != http.StatusUnauthorized || observed.Events[6].Credential != "other" {
		t.Fatal("denied key and bearer controls were not observed")
	}
}

func TestContextForgeAdminProxyKeepsTheFixedTLSOriginAndNativeResponse(t *testing.T) {
	directory := contextForgeInputs(t)
	calls := 0
	handler, err := contextForgeHandler(filepath.Join(directory, "frontend"), peerRoundTrip(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.URL.Scheme != "https" || r.URL.Host != "contextforge:4444" || (r.URL.RequestURI() != "/v1/admin/login?next=servers" && r.URL.RequestURI() != "/admin/login?next=servers") || r.Header.Get("Authorization") != "Bearer synthetic-admin-token" || r.Header.Get("X-Forwarded-Proto") != "https" || r.Header.Get("X-Forwarded-Host") != "127.0.0.1:12345" {
			t.Fatal("admin request changed destination, path or credentials")
		}
		return &http.Response{StatusCode: http.StatusSeeOther, Header: http.Header{"Location": {"/v1/admin/"}, "Set-Cookie": {"synthetic-cookie; Secure; HttpOnly"}}, Body: io.NopCloser(strings.NewReader("native response"))}, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/v1/admin/login?next=servers", "/admin/login?next=servers"} {
		r := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "https://127.0.0.1:12345"+path, nil)
		r.Header.Set("Authorization", "Bearer synthetic-admin-token")
		r.Header.Set("X-Forwarded-Proto", "http")
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != http.StatusSeeOther || w.Body.String() != "native response" || w.Header().Get("Location") != "/v1/admin/" || w.Header().Get("Set-Cookie") != "synthetic-cookie; Secure; HttpOnly" {
			t.Fatal("admin proxy changed the native response")
		}
	}
	if calls != 2 {
		t.Fatal("admin alias did not reach the same fixed upstream")
	}
}
