// Credential fixtures generate private test inputs and enforce selected bearer,
// API-key and client-certificate bindings on the owned federation origin.

package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/subtle"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// generateCredentials writes leaves below the invocation-owned temporary directory,
// where they are mounted read-only. The host journey atomically replaces metadata to exercise
// the real just-in-time resolver, never a production configuration shortcut.
func generateCredentials(directory string, ca *x509.Certificate, caKey ed25519.PrivateKey) error {
	directory = filepath.Join(directory, "credentials")
	if err := makeFixtureDirectory(directory); err != nil {
		return err
	}
	for _, kind := range []string{"bearer", "api_key", "mtls"} {
		metadata := map[string]any{"schemaVersion": 1, "tenantId": fixtureTenant, "revision": "one", "origin": fixtureOrigin, "type": kind}
		if kind == "mtls" {
			for _, stem := range []string{"client", "client-two"} {
				public, private, err := ed25519.GenerateKey(rand.Reader)
				if err != nil {
					return err
				}
				serial, err := randomSerial()
				if err != nil {
					return err
				}
				now := time.Now().UTC()
				leaf := &x509.Certificate{SerialNumber: serial, Subject: pkix.Name{CommonName: "owned-functional-client"}, NotBefore: now.Add(-time.Hour), NotAfter: now.Add(24 * time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}
				raw, err := x509.CreateCertificate(rand.Reader, leaf, ca, public, caKey)
				if err != nil {
					return err
				}
				if err := writePEM(filepath.Join(directory, stem+".pem"), "CERTIFICATE", raw); err != nil {
					return err
				}
				key, err := x509.MarshalPKCS8PrivateKey(private)
				if err != nil {
					return err
				}
				if err := writePEM(filepath.Join(directory, stem+"-key.pem"), "PRIVATE KEY", key); err != nil {
					return err
				}
			}
			metadata["certificateFile"], metadata["privateKeyFile"] = "client.pem", "client-key.pem"
		} else {
			secret := make([]byte, 32)
			if _, err := rand.Read(secret); err != nil {
				return err
			}
			if err := writeFixtureFile(filepath.Join(directory, kind+".token"), []byte(hex.EncodeToString(secret))); err != nil {
				return err
			}
			metadata["secretFile"] = kind + ".token"
		}
		raw, err := json.Marshal(metadata)
		if err != nil {
			return err
		}
		if err := writeFixtureFile(filepath.Join(directory, kind+".json"), raw); err != nil {
			return err
		}
	}
	return nil
}

// credentialTLS configures optional client-certificate verification against the
// generated fixture CA; routes decide whether a verified certificate is required.
func credentialTLS(directory string) (*tls.Config, error) {
	raw, err := os.ReadFile(filepath.Join(directory, "trust", "functional-ca.pem")) // #nosec G304 -- operator-owned test directory.
	if err != nil {
		return nil, err
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(raw) {
		return nil, errors.New("invalid fixture client CA")
	}
	config := defaultTLSConfig()
	config.ClientAuth, config.ClientCAs = tls.VerifyClientCertIfGiven, roots
	return config, nil
}

// credentialManifest checks the selected test credential before serving metadata,
// rereading fixture inputs so the host can exercise credential rotation.
func credentialManifest(directory string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		kind := r.PathValue("kind")
		allowed := false
		switch kind {
		case "bearer", "api_key":
			raw, err := os.ReadFile(filepath.Join(directory, "credentials", kind+".token")) // #nosec G304 G703 -- exact bearer/api_key switch, owned fixture leaf.
			got := r.Header.Get("X-Api-Key")
			if kind == "bearer" {
				got = strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
			}
			allowed = err == nil && len(raw) == 64 && subtle.ConstantTimeCompare(raw, []byte(got)) == 1 && (kind != "bearer" || strings.HasPrefix(r.Header.Get("Authorization"), "Bearer "))
		case "mtls":
			allowed = r.TLS != nil && len(r.TLS.VerifiedChains) > 0
		}
		if !allowed {
			writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "fixture authentication required"})
			return
		}
		entry := entryOne()
		delete(entry, "@context") // The credential fixture has no secondary destination.
		entry["identifier"] = "urn:air:functional.example:credentials:" + strings.ReplaceAll(kind, "_", "-")
		writeJSON(w, http.StatusOK, map[string]any{"entries": []any{entry}})
	}
}
