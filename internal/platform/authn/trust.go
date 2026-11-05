// Issuer trust loading accepts bounded certificate-only PEM and computes an exact
// byte digest for safe diagnostics without altering the process-wide root pool.

package authn

import (
	"bytes"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"io"
	"os"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/safeerr"
)

const maxOIDCTrustBundleBytes = 4 << 20

// loadOIDCTrustBundle extends a copy of the current roots, never process-wide
// trust. Files are deployment-owned; projected ConfigMap symlinks are supported.
func loadOIDCTrustBundle(path string) (*x509.CertPool, string, error) {
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return nil, "", safeerr.New("OIDC trust bundle must be a readable regular file")
	}
	file, err := os.Open(path) // #nosec G304 -- operator-configured trust file, never tenant-selected.
	if err != nil {
		return nil, "", safeerr.New("OIDC trust bundle could not be opened")
	}
	defer func() { _ = file.Close() }()
	raw, err := io.ReadAll(io.LimitReader(file, maxOIDCTrustBundleBytes+1))
	if err != nil || len(raw) > maxOIDCTrustBundleBytes {
		return nil, "", safeerr.New("OIDC trust bundle could not be read within the 4 MiB limit")
	}
	roots, err := x509.SystemCertPool()
	if err != nil || roots == nil {
		return nil, "", safeerr.New("OIDC public trust roots are unavailable")
	}
	count := 0
	for rest := bytes.TrimSpace(raw); len(rest) > 0; count++ {
		if !bytes.HasPrefix(rest, []byte("-----BEGIN CERTIFICATE-----")) {
			return nil, "", safeerr.New("OIDC trust bundle must contain only PEM certificates")
		}
		endMarker := []byte("-----END CERTIFICATE-----")
		end := bytes.Index(rest, endMarker)
		if end < 0 || bytes.Count(rest[:end], []byte("-----BEGIN")) != 1 {
			return nil, "", safeerr.New("OIDC trust bundle contains malformed PEM")
		}
		end += len(endMarker)
		block, trailing := pem.Decode(rest[:end])
		if block == nil || block.Type != "CERTIFICATE" || len(block.Headers) != 0 || len(bytes.TrimSpace(trailing)) != 0 {
			return nil, "", safeerr.New("OIDC trust bundle contains malformed PEM")
		}
		certificate, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			return nil, "", safeerr.New("OIDC trust bundle contains an invalid certificate")
		}
		roots.AddCert(certificate)
		rest = bytes.TrimSpace(rest[end:])
	}
	if count == 0 {
		return nil, "", safeerr.New("OIDC trust bundle contains no certificates")
	}
	digest := sha256.Sum256(raw)
	return roots, "sha256:" + hex.EncodeToString(digest[:]), nil
}
