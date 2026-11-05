package main

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"math/big"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// prepareContextForge creates synthetic inputs only in a new invocation directory.
// The existing CA, PEM writer and OIDC discovery handler remain shared fixtures.
func prepareContextForge(arguments []string) error {
	f := flag.NewFlagSet("contextforge-prepare", flag.ContinueOnError)
	directory := f.String("directory", "", "new absolute invocation input directory")
	if err := f.Parse(arguments); err != nil {
		return err
	}
	if !filepath.IsAbs(*directory) || filepath.Clean(*directory) == "/" {
		return errors.New("select an absolute invocation directory")
	}
	info, err := os.Lstat(*directory)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return errors.New("select a real invocation directory")
	}
	entries, err := os.ReadDir(*directory)
	if err != nil {
		return err
	}
	if len(entries) != 0 {
		return errors.New("refuse to overwrite existing ContextForge inputs")
	}
	for _, name := range []string{"backend", "frontend", "contextforge", "state", "scratch"} {
		if err := os.MkdirAll(filepath.Join(*directory, name), 0o750); err != nil {
			return err
		}
	}
	ca, caKey, err := contextForgeCA()
	if err != nil {
		return err
	}
	for _, name := range []string{"backend", "frontend", "contextforge"} {
		if err := writePEM(filepath.Join(*directory, name, "ca.pem"), "CERTIFICATE", ca.Raw); err != nil {
			return err
		}
	}
	for name, names := range map[string][]string{"frontend": {fixtureHostname}, "contextforge": {"contextforge"}} {
		if err := contextForgeCertificate(filepath.Join(*directory, name), ca, caKey, names); err != nil {
			return err
		}
	}
	jwtKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return err
	}
	encode := base64.RawURLEncoding.EncodeToString
	public := map[string]any{"keys": []any{map[string]any{"kty": "RSA", "kid": "contextforge-fixture", "use": "sig", "alg": "RS256", "n": encode(jwtKey.N.Bytes()), "e": encode(big.NewInt(int64(jwtKey.E)).Bytes())}}}
	publicBytes, err := json.Marshal(public)
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(*directory, "frontend", "jwks.json"), publicBytes, 0o640); err != nil { // #nosec G306 -- synthetic public verification input.
		return err
	}
	tokens := map[string]string{}
	values := map[string]any{"tokens": tokens}
	for _, name := range []string{"everythingKey", "cfPassword", "cfPasswordA", "cfPasswordB", "cfPasswordEverything", "cfDefaultPassword", "cfJWT", "cfEncryption"} {
		var raw [32]byte
		if _, err := rand.Read(raw[:]); err != nil {
			return err
		}
		values[name] = hex.EncodeToString(raw[:])
		if strings.HasPrefix(name, "cfPassword") || name == "cfDefaultPassword" {
			// Separate random hex pairs so the native password policy cannot
			// reject an accidental ascending or descending three-character run.
			parts := make([]string, len(raw))
			for i := range raw {
				parts[i] = hex.EncodeToString(raw[i : i+1])
			}
			values[name] = "Cf1!" + strings.Join(parts, "!")
		}
	}
	everythingKey, _ := values["everythingKey"].(string)
	if err := os.WriteFile(filepath.Join(*directory, "frontend", "everything-key"), []byte(everythingKey), 0o640); err != nil { // #nosec G306 -- invocation-only group-readable test credential.
		return err
	}
	for _, org := range []string{"a", "b"} {
		for _, role := range []string{"admin", "reader", "contributor", "reviewer", "publisher"} {
			scope := "catalog.read"
			switch role {
			case "admin":
				scope += " tenant.admin catalog.write catalog.review catalog.publish sources.read sources.manage publishers.read publishers.manage"
			case "contributor":
				scope += " catalog.write"
			case "reviewer":
				scope += " catalog.review"
			case "publisher":
				scope += " catalog.publish"
			}
			now := time.Now().UTC()
			claims := map[string]any{"iss": fixtureOrigin, "aud": "open-agentic-catalog", "sub": org + "-" + role, "organization_id": "org-" + org, "iat": now.Add(-5 * time.Second).Unix(), "exp": now.Add(2 * time.Hour).Unix(), "scope": scope}
			header, err := json.Marshal(map[string]string{"alg": "RS256", "typ": "JWT", "kid": "contextforge-fixture"})
			if err != nil {
				return err
			}
			payload, err := json.Marshal(claims)
			if err != nil {
				return err
			}
			input := encode(header) + "." + encode(payload)
			digest := sha256.Sum256([]byte(input))
			signature, err := rsa.SignPKCS1v15(rand.Reader, jwtKey, crypto.SHA256, digest[:])
			if err != nil {
				return err
			}
			tokens[org+"-"+role] = input + "." + encode(signature)
		}
	}
	for _, variant := range []string{"expired", "audience", "scope"} {
		parts := strings.Split(tokens["a-reader"], ".")
		payload, err := base64.RawURLEncoding.DecodeString(parts[1])
		if err != nil {
			return err
		}
		var claims map[string]any
		if err := json.Unmarshal(payload, &claims); err != nil {
			return err
		}
		switch variant {
		case "expired":
			claims["iat"], claims["exp"] = time.Now().Add(-2*time.Hour).Unix(), time.Now().Add(-time.Hour).Unix()
		case "audience":
			claims["aud"] = "wrong-owned-audience"
		case "scope":
			claims["scope"] = "unrelated.scope"
		}
		changed, err := json.Marshal(claims)
		if err != nil {
			return err
		}
		input := parts[0] + "." + encode(changed)
		digest := sha256.Sum256([]byte(input))
		signature, err := rsa.SignPKCS1v15(rand.Reader, jwtKey, crypto.SHA256, digest[:])
		if err != nil {
			return err
		}
		tokens["a-"+variant] = input + "." + encode(signature)
	}
	labels := map[string]string{}
	for label, token := range tokens {
		sum := sha256.Sum256([]byte(token))
		labels[hex.EncodeToString(sum[:])] = label
	}
	encodedLabels, err := json.Marshal(labels)
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(*directory, "frontend", "token-labels.json"), encodedLabels, 0o640); err != nil { // #nosec G306 -- public hashes of invocation-only synthetic credentials.
		return err
	}
	raw, err := json.Marshal(values)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(*directory, "secrets.json"), raw, 0o600)
}

// Chromium's TLS client requires a supported RSA or ECDSA server key.
// This private CA and both leaves use P-256; unrelated fixture PKI is unchanged.
func contextForgeCA() (*x509.Certificate, *ecdsa.PrivateKey, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	serial, err := randomSerial()
	if err != nil {
		return nil, nil, err
	}
	now := time.Now().UTC()
	template := &x509.Certificate{SerialNumber: serial, Subject: pkix.Name{CommonName: "ContextForge fixture CA"}, NotBefore: now.Add(-time.Hour), NotAfter: now.Add(24 * time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature}
	raw, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		return nil, nil, err
	}
	certificate, err := x509.ParseCertificate(raw)
	return certificate, key, err
}

func contextForgeCertificate(directory string, ca *x509.Certificate, key *ecdsa.PrivateKey, names []string) error {
	private, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return err
	}
	serial, err := randomSerial()
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	leaf := &x509.Certificate{SerialNumber: serial, Subject: pkix.Name{CommonName: names[0]}, DNSNames: names, IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}, NotBefore: now.Add(-time.Minute), NotAfter: now.Add(24 * time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, leaf, ca, &private.PublicKey, key)
	if err != nil {
		return err
	}
	if err := writePEM(filepath.Join(directory, "server.pem"), "CERTIFICATE", der); err != nil {
		return err
	}
	pkcs8, err := x509.MarshalPKCS8PrivateKey(private)
	if err != nil {
		return err
	}
	return writePEM(filepath.Join(directory, "server-key.pem"), "PRIVATE KEY", pkcs8)
}

// contextForgeHandler forwards only the fixed owned backends. It never
// reads a destination from metadata or logs incoming credentials/documents.
func contextForgeHandler(directory string, transport http.RoundTripper) (http.Handler, error) {
	issuer, err := newOIDCMux(fixtureOrigin, filepath.Join(directory, "jwks.json"))
	if err != nil {
		return nil, err
	}
	key, err := os.ReadFile(filepath.Join(directory, "everything-key")) // #nosec G304 -- explicit invocation-owned fixture input.
	if err != nil || len(key) < 32 {
		return nil, errors.New("missing owned Everything credential")
	}
	observations, err := contextForgeObserve(directory)
	if err != nil {
		return nil, err
	}
	proxy := func(target string) *httputil.ReverseProxy {
		u, _ := url.Parse(target)
		p := httputil.NewSingleHostReverseProxy(u)
		p.Transport = transport
		p.FlushInterval = -1
		p.ErrorHandler = func(w http.ResponseWriter, _ *http.Request, _ error) {
			http.Error(w, "owned upstream unavailable", http.StatusBadGateway)
		}
		return p
	}
	api, everything := proxy("http://api:8080"), proxy("http://everything:3001")
	admin := &httputil.ReverseProxy{
		Transport: transport, FlushInterval: -1,
		Rewrite: func(r *httputil.ProxyRequest) {
			r.SetURL(&url.URL{Scheme: "https", Host: "contextforge:4444"})
			r.Out.Host = r.In.Host
			r.SetXForwarded()
			r.Out.Header.Set("X-Forwarded-Proto", "https")
		},
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, _ error) {
			http.Error(w, "owned admin upstream unavailable", http.StatusBadGateway)
		},
	}
	apiObserved := observations.observe(api, "catalog")
	everythingObserved := observations.observe(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Mcp-Test-Key") != string(key) || len(r.Header.Values("Authorization")) != 0 {
			http.Error(w, "owned upstream credential refused", http.StatusUnauthorized)
			return
		}
		r.Header.Del("X-Mcp-Test-Key")
		r.URL.Path = "/mcp"
		r.URL.RawPath = ""
		everything.ServeHTTP(w, r)
	}), "everything")
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
		switch {
		case r.URL.Path == "/healthz" || r.URL.Path == "/.well-known/openid-configuration" || r.URL.Path == "/keys":
			issuer.ServeHTTP(w, r)
		case r.URL.Path == "/observations":
			observations.ServeHTTP(w, r)
		case r.URL.Path == "/.well-known/ard.json":
			observations.add(contextForgeEvent{Path: "manifest", Method: "read", Credential: "none", Status: http.StatusOK})
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"entries": []any{map[string]any{"identifier": "urn:air:catalog.example.com:server:contextforge-indexed", "displayName": "ContextForge indexed metadata", "type": "application/mcp-server-card+json", "url": fixtureOrigin + "/never-invoke", "version": "1.0.0", "tags": []string{"contextforge-indexed"}}}})
		case r.URL.Path == "/mcp":
			apiObserved.ServeHTTP(w, r)
		case strings.HasPrefix(r.URL.Path, "/api/v1/"):
			api.ServeHTTP(w, r)
		case strings.HasPrefix(r.URL.Path, "/v1/") || r.URL.Path == "/admin" || strings.HasPrefix(r.URL.Path, "/admin/") || strings.HasPrefix(r.URL.Path, "/static/") || r.URL.Path == "/favicon.ico":
			admin.ServeHTTP(w, r)
		case r.URL.Path == "/everything/mcp":
			everythingObserved.ServeHTTP(w, r)
		default:
			observations.add(contextForgeEvent{Path: "unexpected", Method: "other", Credential: "none", Status: http.StatusNotFound})
			http.NotFound(w, r)
		}
	}), nil
}

func serveContextForge(arguments []string) error {
	f := flag.NewFlagSet("contextforge-serve", flag.ContinueOnError)
	directory := f.String("directory", "", "owned frontend inputs")
	if err := f.Parse(arguments); err != nil {
		return err
	}
	ca, err := os.ReadFile(filepath.Join(*directory, "ca.pem")) // #nosec G304 -- explicit invocation-owned public CA.
	if err != nil {
		return err
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(ca) {
		return errors.New("invalid owned frontend CA")
	}
	transport := &http.Transport{Proxy: nil, DialContext: (&net.Dialer{Timeout: 3 * time.Second}).DialContext, ResponseHeaderTimeout: 15 * time.Second, IdleConnTimeout: 30 * time.Second, MaxConnsPerHost: 16, TLSClientConfig: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}}
	defer transport.CloseIdleConnections()
	handler, err := contextForgeHandler(*directory, transport)
	if err != nil {
		return err
	}
	s := &http.Server{Addr: ":8443", Handler: handler, ReadHeaderTimeout: 3 * time.Second, ReadTimeout: 30 * time.Second, WriteTimeout: 60 * time.Second, IdleTimeout: 15 * time.Second, TLSConfig: &tls.Config{MinVersion: tls.VersionTLS12}}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	done := make(chan error, 1)
	go func() {
		done <- s.ListenAndServeTLS(filepath.Join(*directory, "server.pem"), filepath.Join(*directory, "server-key.pem"))
	}()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := s.Shutdown(shutdown); err != nil {
			return err
		}
		if err := <-done; err != nil && !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	}
}
