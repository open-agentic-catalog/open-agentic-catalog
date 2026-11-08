// Command fixture generates isolated test inputs and serves controlled HTTPS and DNS origins.
package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"flag"
	"fmt"
	"log"
	"math/big"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

const (
	fixtureHostname = "federation-fixture"
	fixturePort     = 8443
	fixtureOrigin   = "https://federation-fixture:8443"
	fixtureTenant   = "00000000-0000-7000-8000-000000000001"
)

type fixtureState struct {
	mu                         sync.Mutex
	mode                       string
	delay                      time.Duration
	manifestRequests           int
	listRequests               int
	conditionalListRequests    int
	contextRequests            int
	conditionalContextRequests int
	activeListRequests         int
	maxActiveListRequests      int
	modeRequests               map[string]int
}

type stateResponse struct {
	Mode                       string         `json:"mode"`
	ManifestRequests           int            `json:"manifestRequests"`
	ListRequests               int            `json:"listRequests"`
	ConditionalListRequests    int            `json:"conditionalListRequests"`
	ContextRequests            int            `json:"contextRequests"`
	ConditionalContextRequests int            `json:"conditionalContextRequests"`
	MaxActiveListRequests      int            `json:"maxActiveListRequests"`
	ModeRequests               map[string]int `json:"modeRequests"`
}

// main selects input generation, fixture serving or explicit network probes and reports startup failures.
func main() {
	if len(os.Args) < 2 {
		log.Fatal("usage: federation-fixture generate|serve|network-probe")
	}
	switch os.Args[1] {
	case "tls-probe":
		if err := tlsProbe(os.Args[2:], os.Stdin, os.Stdout); err != nil {
			log.Fatal(err)
		}
	case "service-command":
		if err := serviceCommand(os.Stdin, os.Stdout); err != nil {
			log.Fatal(err)
		}
	case "service-probe":
		if err := serviceProbe(os.Stdin, os.Stdout); err != nil {
			log.Fatal(err)
		}
	case "contextforge-prepare":
		if err := prepareContextForge(os.Args[2:]); err != nil {
			log.Fatal(err)
		}
	case "contextforge-serve":
		if err := serveContextForge(os.Args[2:]); err != nil {
			log.Fatal(err)
		}
	case "ans-public-key":
		name := "ans-ra-signer"
		if len(os.Args) == 3 {
			name = os.Args[2]
		} else if len(os.Args) != 2 {
			log.Fatal("select one ANS public key")
		}
		if err := ansPublicKey(name, os.Stdout); err != nil {
			log.Fatal(err)
		}
	case "network-probe":
		if err := networkProbe(os.Args[2:], os.Stdout); err != nil {
			log.Fatal(err)
		}
	case "generate":
		if err := generate(os.Args[2:]); err != nil {
			log.Fatal(err)
		}
	case "serve":
		if err := serve(os.Args[2:]); err != nil {
			log.Fatal(err)
		}
	default:
		log.Fatal("unknown command")
	}
}

// generate writes invocation-owned trust and policy inputs for a bounded private
// network. Its caller owns the selected directory and any partially written files.
func generate(arguments []string) error {
	flags := flag.NewFlagSet("generate", flag.ContinueOnError)
	directory := flags.String("directory", "", "output directory")
	subnet := flags.String("subnet", "", "fixture network subnet")
	webhooks := flags.Bool("webhooks", false, "generate isolated optional webhook inputs")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	if *directory == "" {
		return errors.New("generate requires --directory")
	}
	prefix, err := netip.ParsePrefix(*subnet)
	if err != nil || !prefix.IsValid() || !prefix.Addr().IsPrivate() || prefix.Bits() < 16 {
		return errors.New("generate requires a bounded private --subnet")
	}
	cleanDirectory := filepath.Clean(*directory)
	if !filepath.IsAbs(cleanDirectory) || cleanDirectory == string(filepath.Separator) {
		return errors.New("generate output must be an absolute non-root directory")
	}
	trustDirectory := filepath.Join(cleanDirectory, "trust")
	if err := makeFixtureDirectory(trustDirectory); err != nil {
		return err
	}
	// #nosec G302 -- disposable bind-mounted fixtures must be traversable by non-root test containers.
	if err := os.Chmod(cleanDirectory, 0o755); err != nil {
		return err
	}
	caCertificate, caKey, err := createCA()
	if err != nil {
		return err
	}
	serverCertificate, serverKey, err := createServerCertificate(caCertificate, caKey)
	if err != nil {
		return err
	}
	if err := writePEM(filepath.Join(trustDirectory, "functional-ca.pem"), "CERTIFICATE", caCertificate.Raw); err != nil {
		return err
	}
	if err := writePEM(filepath.Join(cleanDirectory, "server.pem"), "CERTIFICATE", serverCertificate); err != nil {
		return err
	}
	keyBytes, err := x509.MarshalPKCS8PrivateKey(serverKey)
	if err != nil {
		return err
	}
	if err := writePEM(filepath.Join(cleanDirectory, "server-key.pem"), "PRIVATE KEY", keyBytes); err != nil {
		return err
	}
	// The independent provider's PEM loader does not accept Ed25519 private
	// keys. Give that one test transport an ordinary P-256 leaf under the same
	// invocation-owned CA; do not alter Catalog trust or its existing fixture.
	if err := writeOIDCCertificate(cleanDirectory, caCertificate, caKey); err != nil {
		return err
	}
	if err := generateCredentials(cleanDirectory, caCertificate, caKey); err != nil {
		return err
	}
	// The trust-only packaged scenario proves discovery transport, not token or
	// independent-provider compatibility. Minikube supplies its own real keys.
	if err := writeFixtureFile(filepath.Join(cleanDirectory, "oidc-jwks.json"), []byte(`{"keys":[]}`)); err != nil {
		return err
	}
	policy := map[string]any{
		"schemaVersion": 1,
		"policyId":      "functional-fixture",
		"revision":      1,
		"public":        map[string]any{"mode": "allowlist", "origins": []string{}},
		"privateProfiles": []map[string]any{{
			"name":              "functional-fixture",
			"origins":           []string{fixtureOrigin},
			"addressRanges":     []string{prefix.String()},
			"assignedTenantIds": []string{fixtureTenant},
			"trustBundleFile":   "/etc/open-agentic-catalog/federation/trust/functional-ca.pem",
			"transportRef":      "direct",
		}},
	}
	rawPolicy, err := json.MarshalIndent(policy, "", "  ")
	if err != nil {
		return err
	}
	rawPolicy = append(rawPolicy, '\n')
	if err := writeFixtureFile(filepath.Join(cleanDirectory, "policy.json"), rawPolicy); err != nil {
		return err
	}
	if *webhooks {
		return generateWebhookInputs(cleanDirectory, prefix.String(), pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caCertificate.Raw}))
	}
	return nil
}

// createCA creates a short-lived signing authority unique to this fixture invocation.
func createCA() (*x509.Certificate, ed25519.PrivateKey, error) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	now := time.Now().UTC()
	serial, err := randomSerial()
	if err != nil {
		return nil, nil, err
	}
	template := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "Open Agentic Catalog functional fixture CA"},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	raw, err := x509.CreateCertificate(rand.Reader, template, template, publicKey, privateKey)
	if err != nil {
		return nil, nil, err
	}
	certificate, err := x509.ParseCertificate(raw)
	return certificate, privateKey, err
}

// createServerCertificate binds the fixture and peer hostnames plus loopback to
// the invocation's CA, returning DER certificate bytes and the new private key.
func createServerCertificate(ca *x509.Certificate, caKey ed25519.PrivateKey) ([]byte, ed25519.PrivateKey, error) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	now := time.Now().UTC()
	serial, err := randomSerial()
	if err != nil {
		return nil, nil, err
	}
	template := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: fixtureHostname},
		DNSNames:     []string{fixtureHostname, "peer-a.test", "private-a.test", "peer-b.test", "api"},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
		NotBefore:    now.Add(-time.Hour),
		NotAfter:     now.Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	raw, err := x509.CreateCertificate(rand.Reader, template, ca, publicKey, caKey)
	return raw, privateKey, err
}

// randomSerial draws a certificate serial below the fixed 120-bit fixture ceiling.
func randomSerial() (*big.Int, error) {
	limit := new(big.Int).Lsh(big.NewInt(1), 120)
	return rand.Int(rand.Reader, limit)
}

// writeOIDCCertificate writes the isolated identity provider's TLS certificate and key.
func writeOIDCCertificate(directory string, ca *x509.Certificate, caKey ed25519.PrivateKey) error {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return err
	}
	serial, err := randomSerial()
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	leaf := &x509.Certificate{SerialNumber: serial, Subject: pkix.Name{CommonName: "catalog-oidc"}, DNSNames: []string{"catalog-oidc"}, NotBefore: now.Add(-time.Hour), NotAfter: now.Add(24 * time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	raw, err := x509.CreateCertificate(rand.Reader, leaf, ca, &key.PublicKey, caKey)
	if err != nil {
		return err
	}
	if err := writePEM(filepath.Join(directory, "oidc-server.pem"), "CERTIFICATE", raw); err != nil {
		return err
	}
	encoded, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return err
	}
	return writePEM(filepath.Join(directory, "oidc-server-key.pem"), "PRIVATE KEY", encoded)
}

// writePEM writes a read-only leaf for non-root test containers. The caller must
// keep its parent directory private; file permissions alone do not protect keys.
func writePEM(path, blockType string, raw []byte) error {
	return writeFixtureFile(path, pem.EncodeToMemory(&pem.Block{Type: blockType, Bytes: raw}))
}

// serve runs the selected controlled origins and applies a bounded shutdown on
// termination. Test controls and issuer fixtures must remain on the owned network.
func serve(arguments []string) error {
	flags := flag.NewFlagSet("serve", flag.ContinueOnError)
	directory := flags.String("directory", "/fixture", "certificate directory")
	credentials := flags.Bool("credentials", false, "enable the owned mounted-credential fixture")
	publisherDNS := flags.Bool("publisher-dns", false, "serve owned Publisher TXT answers on port 53")
	peer := flags.Bool("peer", false, "serve only the fixed Catalog A discovery frontend")
	peerNamespace := flags.String("peer-kubernetes-namespace", "", "owned primary namespace for Kubernetes Catalog peers")
	peerOtherNamespace := flags.String("peer-kubernetes-other-namespace", "", "owned secondary namespace for Kubernetes Catalog peers")
	oidcAddress := flags.String("oidc-address", "", "optional address for local OIDC discovery; issuer scheme selects TLS")
	oidcIssuer := flags.String("oidc-issuer", "", "exact issuer returned by local OIDC discovery")
	oidcJWKSet := flags.String("oidc-jwks", "", "path to the public local OIDC JWK set")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	if (*oidcAddress == "") != (*oidcIssuer == "") || (*oidcAddress == "") != (*oidcJWKSet == "") {
		return errors.New("OIDC address, issuer, and JWK set must be supplied together")
	}
	if !*peer && (*peerNamespace != "" || *peerOtherNamespace != "") {
		return errors.New("kubernetes peer namespaces require peer mode")
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	state := &fixtureState{mode: "initial", modeRequests: make(map[string]int)}
	mux := http.NewServeMux()
	registerTracingFixture(mux)
	registerPressureFixture(mux)
	mux.HandleFunc("GET /healthz", func(writer http.ResponseWriter, _ *http.Request) { writer.WriteHeader(http.StatusOK) })
	mux.HandleFunc("GET /.well-known/ard.json", state.manifest)
	registerArtworkSourceFixture(mux, state)
	if *credentials {
		mux.HandleFunc("GET /credentials/{kind}/manifest.json", credentialManifest(*directory))
	}
	mux.HandleFunc("GET /ard/v1/agents", state.list)
	mux.HandleFunc("POST /ard/v1/search", state.search)
	mux.HandleFunc("GET /context.jsonld", state.context)
	mux.HandleFunc("POST /control", state.control)
	mux.HandleFunc("GET /state", state.report)
	receiver := newWebhookReceiver()
	if *publisherDNS {
		if *peer {
			return errors.New("peer frontend cannot enable Publisher DNS")
		}
		dns := newPublisherDNS()
		closeDNS, err := dns.listen(ctx, "0.0.0.0:53")
		if err != nil {
			return err
		}
		defer closeDNS()
		mux.HandleFunc("POST /publisher-dns", dns.control)
		mux.HandleFunc("GET /publisher-dns", dns.report)
	}
	mux.HandleFunc("POST /webhooks/control", receiver.control)
	mux.HandleFunc("GET /webhooks/state", receiver.report)
	mux.HandleFunc("POST /webhooks/barrier", receiver.barrierControl)
	mux.HandleFunc("GET /webhooks/barrier", receiver.barrierReport)
	mux.HandleFunc("POST /webhooks/events", receiver.receive)
	tlsServer := &http.Server{Addr: ":" + strconv.Itoa(fixturePort), Handler: mux, ReadHeaderTimeout: 2 * time.Second, ReadTimeout: 5 * time.Second, WriteTimeout: 5 * time.Second, IdleTimeout: 10 * time.Second}
	if *peer {
		if *credentials || *oidcAddress != "" {
			return errors.New("peer frontend cannot enable other fixture modes")
		}
		transport := &http.Transport{Proxy: nil, DialContext: (&net.Dialer{Timeout: time.Second}).DialContext, ResponseHeaderTimeout: 3 * time.Second, IdleConnTimeout: 10 * time.Second, MaxConnsPerHost: 8}
		defer transport.CloseIdleConnections()
		tlsServer.Handler = peerHandler(transport)
		if *peerNamespace != "" || *peerOtherNamespace != "" {
			routes, err := kubernetesPeerRoutes(*peerNamespace, *peerOtherNamespace)
			if err != nil {
				return err
			}
			tlsServer.Handler = routedPeerHandler(transport, routes)
		}
	}
	tlsServer.TLSConfig = defaultTLSConfig()
	if *credentials {
		var err error
		tlsServer.TLSConfig, err = credentialTLS(*directory)
		if err != nil {
			return err
		}
	}
	servers := []*http.Server{tlsServer}
	listeners := []func() error{func() error {
		return tlsServer.ListenAndServeTLS(filepath.Join(*directory, "server.pem"), filepath.Join(*directory, "server-key.pem"))
	}}
	if *oidcAddress != "" {
		oidcMux, err := newOIDCMux(*oidcIssuer, *oidcJWKSet)
		if err != nil {
			return err
		}
		if strings.HasPrefix(*oidcIssuer, "https://") {
			if *oidcAddress != tlsServer.Addr || *oidcIssuer != fixtureOrigin {
				return errors.New("HTTPS OIDC fixture must use the existing private TLS origin")
			}
			mux.Handle("GET /.well-known/openid-configuration", oidcMux)
			mux.Handle("GET /keys", oidcMux)
		} else {
			oidcServer := &http.Server{Addr: *oidcAddress, Handler: oidcMux, ReadHeaderTimeout: 2 * time.Second, ReadTimeout: 5 * time.Second, WriteTimeout: 5 * time.Second, IdleTimeout: 10 * time.Second}
			servers = append(servers, oidcServer)
			listeners = append(listeners, oidcServer.ListenAndServe)
		}
	}
	result := make(chan error, 1)
	for _, listen := range listeners {
		go func(run func() error) { result <- run() }(listen)
	}
	select {
	case err := <-result:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		var shutdownError error
		for _, server := range servers {
			if err := server.Shutdown(shutdownCtx); err != nil && shutdownError == nil {
				shutdownError = err
			}
		}
		return shutdownError
	}
}

// newOIDCMux serves discovery and supplied public keys for verifier tests. It
// advertises, but does not implement, authorization or token-issuance endpoints.
func newOIDCMux(issuer, jwkSetPath string) (*http.ServeMux, error) {
	issuerURL, err := url.Parse(issuer)
	if err != nil || (issuerURL.Scheme != "http" && issuerURL.Scheme != "https") || issuerURL.Host == "" || issuerURL.User != nil || issuerURL.RawQuery != "" || issuerURL.Fragment != "" || strings.TrimRight(issuerURL.Path, "/") != "" {
		return nil, errors.New("local OIDC issuer must be an absolute HTTP or HTTPS origin")
	}
	raw, err := os.ReadFile(jwkSetPath) // #nosec G304 -- the operator selects a bounded test-only public-key fixture.
	if err != nil {
		return nil, fmt.Errorf("read local OIDC JWK set: %w", err)
	}
	if len(raw) == 0 || len(raw) > 1<<20 || !json.Valid(raw) {
		return nil, errors.New("local OIDC JWK set is empty, oversized, or invalid JSON")
	}
	discovery, err := json.Marshal(map[string]any{
		"issuer":                                issuer,
		"authorization_endpoint":                issuer + "/authorize",
		"token_endpoint":                        issuer + "/token",
		"jwks_uri":                              issuer + "/keys",
		"id_token_signing_alg_values_supported": []string{"RS256"},
	})
	if err != nil {
		return nil, err
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(writer http.ResponseWriter, _ *http.Request) { writer.WriteHeader(http.StatusOK) })
	mux.HandleFunc("GET /.well-known/openid-configuration", func(writer http.ResponseWriter, _ *http.Request) { writeRawJSON(writer, discovery) })
	mux.HandleFunc("GET /keys", func(writer http.ResponseWriter, _ *http.Request) { writeRawJSON(writer, raw) })
	return mux, nil
}

// writeRawJSON serves prevalidated fixture bytes unchanged as an HTTP 200 response.
func writeRawJSON(writer http.ResponseWriter, raw []byte) {
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(http.StatusOK)
	if _, err := writer.Write(raw); err != nil {
		log.Printf("write fixture response: %v", err)
	}
}

// defaultTLSConfig returns a fresh fixture configuration with a TLS 1.2 minimum.
func defaultTLSConfig() *tls.Config {
	return &tls.Config{MinVersion: tls.VersionTLS12}
}

// manifest advertises the fixed Registry entry and records discovery requests.
func (s *fixtureState) manifest(writer http.ResponseWriter, _ *http.Request) {
	s.mu.Lock()
	s.manifestRequests++
	s.mu.Unlock()
	writeJSON(writer, http.StatusOK, map[string]any{"entries": []map[string]any{{
		"identifier":            "urn:air:functional.example:registry:main",
		"displayName":           "Functional Registry",
		"type":                  "application/ai-registry+json",
		"url":                   fixtureOrigin + "/ard/v1/",
		"representativeQueries": []string{"Find functional agents", "Browse functional agents"},
	}}})
}

// list simulates complete, duplicate-page, replacement and denied source reads
// with bounded delay and request counters for synchronization assertions.
func (s *fixtureState) list(writer http.ResponseWriter, request *http.Request) {
	s.mu.Lock()
	s.listRequests++
	s.activeListRequests++
	if s.activeListRequests > s.maxActiveListRequests {
		s.maxActiveListRequests = s.activeListRequests
	}
	mode := s.mode
	delay := s.delay
	s.modeRequests[mode]++
	if request.Header.Get("If-None-Match") != "" {
		s.conditionalListRequests++
	}
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.activeListRequests--
		s.mu.Unlock()
	}()
	if delay > 0 {
		time.Sleep(delay)
	}
	if mode == "throttled" || mode == "unavailable" {
		status := http.StatusTooManyRequests
		if mode == "unavailable" {
			status = http.StatusServiceUnavailable
		}
		writer.Header().Set("Retry-After", "2")
		writeJSON(writer, status, map[string]any{"error": "temporary source failure"})
		return
	}
	if mode == "denied" {
		// A hint on a terminal refusal must never grant retry permission.
		writer.Header().Set("Retry-After", "120")
		writeJSON(writer, http.StatusForbidden, map[string]any{"error": "owned Registry refusal"})
		return
	}
	pageSize := request.URL.Query().Get("pageSize")
	pageToken := request.URL.Query().Get("pageToken")
	if pageSize == "1" {
		writeJSON(writer, http.StatusOK, map[string]any{"items": []any{entryOne()}, "total": 2, "pageToken": "probe-next"})
		return
	}
	if pageSize != "100" {
		writeJSON(writer, http.StatusBadRequest, map[string]any{"error": "pageSize must be bounded"})
		return
	}
	switch mode {
	case "initial":
		if pageToken == "" {
			writeJSON(writer, http.StatusOK, map[string]any{"items": []any{entryOne()}, "total": 2, "pageToken": "next"})
			return
		}
		if pageToken == "next" {
			writeJSON(writer, http.StatusOK, map[string]any{"items": []any{entryTwo()}, "total": 2})
			return
		}
	case "partial":
		if pageToken == "" {
			writeJSON(writer, http.StatusOK, map[string]any{"items": []any{entryOne()}, "total": 2, "pageToken": "next"})
			return
		}
		if pageToken == "next" {
			writeJSON(writer, http.StatusOK, map[string]any{"items": []any{entryOne()}, "total": 2})
			return
		}
	case "replacement":
		if pageToken == "" {
			writer.Header().Set("ETag", `"replacement-v1"`)
			if request.Header.Get("If-None-Match") == `"replacement-v1"` {
				writer.WriteHeader(http.StatusNotModified)
				return
			}
			writeJSON(writer, http.StatusOK, map[string]any{"items": []any{entryOne()}, "total": 1})
			return
		}
	}
	writeJSON(writer, http.StatusBadRequest, map[string]any{"error": "unexpected pagination state"})
}

// context serves a stable JSON-LD context with ETag-based conditional-read evidence.
func (s *fixtureState) context(writer http.ResponseWriter, request *http.Request) {
	s.mu.Lock()
	s.contextRequests++
	if request.Header.Get("If-None-Match") != "" {
		s.conditionalContextRequests++
	}
	s.mu.Unlock()
	writer.Header().Set("ETag", `"context-v1"`)
	if request.Header.Get("If-None-Match") == `"context-v1"` {
		writer.WriteHeader(http.StatusNotModified)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"@context": map[string]any{"fixture": "https://functional.example/vocab#fixture"}})
}

// control selects one reviewed response mode and bounded delay under the state mutex.
func (s *fixtureState) control(writer http.ResponseWriter, request *http.Request) {
	var input struct {
		Mode        string `json:"mode"`
		DelayMillis int    `json:"delayMillis"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(writer, request.Body, 4096))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil || input.DelayMillis < 0 || input.DelayMillis > 2000 || input.Mode != "initial" && input.Mode != "partial" && input.Mode != "replacement" && input.Mode != "denied" && input.Mode != "throttled" && input.Mode != "unavailable" {
		writeJSON(writer, http.StatusBadRequest, map[string]any{"error": "invalid fixture control"})
		return
	}
	s.mu.Lock()
	s.mode = input.Mode
	s.delay = time.Duration(input.DelayMillis) * time.Millisecond
	s.mu.Unlock()
	writeJSON(writer, http.StatusOK, map[string]any{"mode": input.Mode, "delayMillis": input.DelayMillis})
}

// report returns a consistent fixture mode and request-counter snapshot.
func (s *fixtureState) report(writer http.ResponseWriter, _ *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	modeRequests := make(map[string]int, len(s.modeRequests))
	for mode, count := range s.modeRequests {
		modeRequests[mode] = count
	}
	writeJSON(writer, http.StatusOK, stateResponse{Mode: s.mode, ManifestRequests: s.manifestRequests, ListRequests: s.listRequests, ConditionalListRequests: s.conditionalListRequests, ContextRequests: s.contextRequests, ConditionalContextRequests: s.conditionalContextRequests, MaxActiveListRequests: s.maxActiveListRequests, ModeRequests: modeRequests})
}

// entryOne creates the record retained in both initial and replacement snapshots.
func entryOne() map[string]any {
	return agentEntry("one", "Functional One")
}

// entryTwo creates the record withdrawn only after a complete replacement snapshot.
func entryTwo() map[string]any {
	return agentEntry("two", "Functional Two")
}

// agentEntry builds inert Agent metadata referencing the controlled remote context.
func agentEntry(segment, name string) map[string]any {
	return map[string]any{"@context": fixtureOrigin + "/context.jsonld", "identifier": "urn:air:functional.example:default:" + segment, "displayName": name, "description": "Functional order metadata", "tags": []string{"functional", "order"}, "type": "application/a2a-agent-card+json", "data": map[string]any{
		"name": name, "description": "A metadata-only functional fixture.", "version": "1.0.0",
		"supportedInterfaces": []any{map[string]any{"url": "https://agents.example.com/" + segment, "protocolBinding": "JSONRPC", "protocolVersion": "1.0"}},
		"capabilities":        map[string]any{}, "defaultInputModes": []string{"application/json"}, "defaultOutputModes": []string{"application/json"},
		"skills": []any{map[string]any{"id": "metadata", "name": "Metadata", "description": "Inert fixture metadata", "tags": []string{"functional"}}},
	}}
}

// writeJSON emits a fixture value with the selected status and logs encoding failures.
func writeJSON(writer http.ResponseWriter, status int, value any) {
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	if err := json.NewEncoder(writer).Encode(value); err != nil {
		log.Printf("encode fixture response: %v", err)
	}
}
