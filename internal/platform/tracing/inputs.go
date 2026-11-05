// Tracing input loading bounds deployment credential/trust files and isolates
// certificate validation from exporter startup.

package tracing

import (
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"net/http"
	"os"

	"golang.org/x/net/http/httpguts"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/jsondoc"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/config"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/fetcher"
)

// readInput checks regular-file identity before and after open and caps bytes,
// returning diagnostics without deployment paths or file contents.
func readInput(path string, limit int64) ([]byte, error) {
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return nil, errors.New("tracing input must be a regular file")
	}
	file, err := os.Open(path) // #nosec G304 -- explicit deployment-injected input.
	if err != nil {
		return nil, errors.New("tracing input could not be opened")
	}
	defer func() { _ = file.Close() }()
	info, err = file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return nil, errors.New("tracing input must be a regular file")
	}
	raw, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil || int64(len(raw)) > limit {
		return nil, errors.New("tracing input exceeds its read bound")
	}
	return raw, nil
}

// loadInputs loads bounded authentication headers and isolated TLS roots/client
// credentials, rejecting canonical header collisions and unsafe values.
func loadInputs(c config.Tracing) (map[string]string, *x509.CertPool, []tls.Certificate, error) {
	headers := make(map[string]string)
	if c.HeadersFile != "" {
		raw, err := readInput(c.HeadersFile, 16<<10)
		if err != nil {
			return nil, nil, nil, err
		}
		if jsondoc.Validate(raw) != nil || json.Unmarshal(raw, &headers) != nil || len(headers) == 0 || len(headers) > 8 {
			return nil, nil, nil, errors.New("tracing headers must be a bounded duplicate-free string map")
		}
		canonical := make(map[string]string)
		for key, value := range headers {
			name := http.CanonicalHeaderKey(key)
			_, duplicate := canonical[name]
			if duplicate || !fetcher.ValidTelemetryHeader(name) || len(value) == 0 || len(value) > 2048 || !httpguts.ValidHeaderFieldValue(value) {
				return nil, nil, nil, errors.New("tracing authentication headers are invalid")
			}
			canonical[name] = value
		}
		headers = canonical
	}
	var roots *x509.CertPool
	if c.CAFile != "" {
		raw, err := readInput(c.CAFile, 4<<20)
		if err != nil {
			return nil, nil, nil, err
		}
		roots, err = x509.SystemCertPool()
		if err != nil || roots == nil {
			return nil, nil, nil, errors.New("tracing public trust roots unavailable")
		}
		if err := addPrivateRoots(roots, raw); err != nil {
			return nil, nil, nil, errors.New("tracing private CA bundle is invalid")
		}
	}
	var certificates []tls.Certificate
	if c.ClientCertificateFile != "" {
		cert, err := readInput(c.ClientCertificateFile, 1<<20)
		if err != nil {
			return nil, nil, nil, err
		}
		key, err := readInput(c.ClientKeyFile, 1<<20)
		if err != nil {
			return nil, nil, nil, err
		}
		pair, err := tls.X509KeyPair(cert, key)
		if err != nil {
			return nil, nil, nil, errors.New("tracing client certificate/key pair is invalid")
		}
		certificates = []tls.Certificate{pair}
	}
	return headers, roots, certificates, nil
}

// addPrivateRoots accepts only nonempty certificate-only PEM and adds parsed
// certificates to the caller-owned pool.
func addPrivateRoots(roots *x509.CertPool, raw []byte) error {
	rest := bytes.TrimSpace(raw)
	if len(rest) == 0 {
		return errors.New("empty certificate bundle")
	}
	for len(rest) > 0 {
		endMarker := []byte("-----END CERTIFICATE-----")
		end := bytes.Index(rest, endMarker)
		if !bytes.HasPrefix(rest, []byte("-----BEGIN CERTIFICATE-----")) || end < 0 || bytes.Count(rest[:end], []byte("-----BEGIN")) != 1 {
			return errors.New("malformed certificate bundle")
		}
		end += len(endMarker)
		block, trailing := pem.Decode(rest[:end])
		if block == nil || block.Type != "CERTIFICATE" || len(block.Headers) != 0 || len(bytes.TrimSpace(trailing)) != 0 {
			return errors.New("malformed certificate bundle")
		}
		certificate, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			return errors.New("invalid certificate")
		}
		roots.AddCert(certificate)
		rest = bytes.TrimSpace(rest[end:])
	}
	return nil
}
