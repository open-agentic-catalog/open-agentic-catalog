//go:build functional

package tracing

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
)

func requireCollectorReceipt(t *testing.T, f *collectorFixture, client collectorIdentity) {
	t.Helper()
	select {
	case got := <-f.received:
		if !bytes.Equal(got, client.pair.Leaf.Raw) {
			t.Fatal("collector received a different client certificate")
		}
	default:
		t.Fatal("successful export has no verified collector receipt")
	}
}

func collectorHandshakeFailure(t *testing.T, f *collectorFixture) error {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	select {
	case err := <-f.listener.failures:
		return err
	case <-ctx.Done():
		t.Fatal("export failure has no collector handshake evidence")
		return nil
	}
}

func requireCollectorCertificateRejection(t *testing.T, f *collectorFixture, client collectorIdentity, authority bool, reason x509.InvalidReason) {
	t.Helper()
	err := collectorHandshakeFailure(t, f)
	var verification *tls.CertificateVerificationError
	if !errors.As(err, &verification) || len(verification.UnverifiedCertificates) != 1 || !bytes.Equal(verification.UnverifiedCertificates[0].Raw, client.pair.Leaf.Raw) {
		t.Fatalf("collector did not verify the selected client certificate: %v", err)
	}
	var unknown x509.UnknownAuthorityError
	var invalid x509.CertificateInvalidError
	if authority {
		if !errors.As(verification.Err, &unknown) {
			t.Fatalf("collector did not reject the signing authority: %v", verification.Err)
		}
	} else if !errors.As(verification.Err, &invalid) || invalid.Reason != reason {
		t.Fatalf("collector did not reach the selected validity/EKU guard: %v", verification.Err)
	}
}

func TestMutualTLSExportRequiresVerifiedClientIdentity(t *testing.T) {
	for _, scenario := range []string{"valid", "absent", "unrelated", "expired", "not-yet-valid", "wrong-EKU"} {
		t.Run(scenario, func(t *testing.T) {
			serverCA := newCollectorCA(t, "collector server authority")
			clientCA := newCollectorCA(t, "collector client authority")
			signer := &clientCA
			if scenario == "unrelated" {
				// The same issuer name makes Go select and present this leaf.
				// The collector trusts only the original CA's different key.
				other := newCollectorCA(t, "collector client authority")
				signer = &other
			}
			client := newCollectorLeaf(t, signer, 2, x509.ExtKeyUsageClientAuth, func(v *x509.Certificate) {
				switch scenario {
				case "expired":
					v.NotBefore, v.NotAfter = time.Now().Add(-2*time.Hour), time.Now().Add(-time.Hour)
				case "not-yet-valid":
					v.NotBefore, v.NotAfter = time.Now().Add(time.Hour), time.Now().Add(2*time.Hour)
				case "wrong-EKU":
					v.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}
				}
			})
			server := newCollectorLeaf(t, &serverCA, 3, x509.ExtKeyUsageServerAuth, nil)
			f := newVerifyingCollector(t, server, collectorRoots(clientCA))
			selected := &client
			if scenario == "absent" {
				selected = nil
			}
			p, metrics := newCollectorProvider(t, f.config(t, serverCA, selected))
			valid := scenario == "valid"
			if valid {
				// Preserve the original pending-span shutdown assertion with a
				// verified client identity, rather than pre-flushing the queue.
				_, end := p.Start(t.Context(), "collector shutdown fixture", catalog.TraceServer, "")
				end("ok")
				ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
				defer cancel()
				if err := p.Shutdown(ctx); err != nil {
					t.Fatalf("mutual TLS shutdown export: %v", err)
				}
				requireCollectorReceipt(t, f, client)
				if f.calls.Load() != 1 || !bytes.Contains(metrics.Render(t.Context()), []byte("open_agentic_catalog_trace_export_failures_total 0\n")) {
					t.Fatal("verified export accounting failed")
				}
				return
			}
			flushCollectorSpan(t, p, false)
			if f.calls.Load() != 0 || len(f.received) != 0 || !bytes.Contains(metrics.Render(t.Context()), []byte("open_agentic_catalog_trace_export_failures_total 1\n")) {
				t.Fatal("invalid client reached OTLP or export failure was not counted")
			}
			if scenario == "absent" {
				err := collectorHandshakeFailure(t, f)
				if !strings.Contains(err.Error(), "client didn't provide a certificate") {
					t.Fatalf("missing identity failed before the collector required it: %v", err)
				}
				return
			}
			reason := x509.Expired
			if scenario == "wrong-EKU" {
				reason = x509.IncompatibleUsage
			}
			requireCollectorCertificateRejection(t, f, client, scenario == "unrelated", reason)
		})
	}
}

func TestMutualTLSClientReplacementAndCollectorAuthorityRetirement(t *testing.T) {
	serverCA := newCollectorCA(t, "collector server authority")
	server := newCollectorLeaf(t, &serverCA, 2, x509.ExtKeyUsageServerAuth, nil)
	oldCA := newCollectorCA(t, "collector client authority")
	newCA := newCollectorCA(t, "collector client authority")
	oldClient := newCollectorLeaf(t, &oldCA, 3, x509.ExtKeyUsageClientAuth, nil)
	newClient := newCollectorLeaf(t, &newCA, 4, x509.ExtKeyUsageClientAuth, nil)
	f := newVerifyingCollector(t, server, collectorRoots(oldCA, newCA))
	c := f.config(t, serverCA, &oldClient)
	old, oldMetrics := newCollectorProvider(t, c)
	flushCollectorSpan(t, old, true)
	requireCollectorReceipt(t, f, oldClient)
	for path, value := range map[string][]byte{c.ClientCertificateFile: newClient.certificate, c.ClientKeyFile: newClient.key} {
		if err := os.WriteFile(path, value, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// Complete file replacement does not change an initialized exporter.
	flushCollectorSpan(t, old, true)
	requireCollectorReceipt(t, f, oldClient)
	replacement, _ := newCollectorProvider(t, c)
	flushCollectorSpan(t, replacement, true)
	requireCollectorReceipt(t, f, newClient)

	// Retire the old signing CA at the collector, not by deleting client files.
	f.setTLS(server, collectorRoots(newCA))
	before := f.calls.Load()
	flushCollectorSpan(t, old, false)
	requireCollectorCertificateRejection(t, f, oldClient, true, 0)
	if f.calls.Load() != before || !bytes.Contains(oldMetrics.Render(t.Context()), []byte("open_agentic_catalog_trace_export_failures_total 1\n")) {
		t.Fatal("retired identity reached OTLP or failure was not observed")
	}
	flushCollectorSpan(t, replacement, true)
	requireCollectorReceipt(t, f, newClient)
	if f.calls.Load() != 4 {
		t.Fatal("rotation changed the expected successful export count")
	}
}

func TestMutualTLSCollectorTrustReplacementRequiresProviderReplacement(t *testing.T) {
	oldServerCA := newCollectorCA(t, "old collector server authority")
	newServerCA := newCollectorCA(t, "new collector server authority")
	oldServer := newCollectorLeaf(t, &oldServerCA, 2, x509.ExtKeyUsageServerAuth, nil)
	newServer := newCollectorLeaf(t, &newServerCA, 3, x509.ExtKeyUsageServerAuth, nil)
	clientCA := newCollectorCA(t, "collector client authority")
	client := newCollectorLeaf(t, &clientCA, 4, x509.ExtKeyUsageClientAuth, nil)
	f := newVerifyingCollector(t, oldServer, collectorRoots(clientCA))
	c := f.config(t, oldServerCA, &client)
	old, _ := newCollectorProvider(t, c)
	flushCollectorSpan(t, old, true)
	requireCollectorReceipt(t, f, client)
	if err := os.WriteFile(c.CAFile, append(bytes.Clone(oldServerCA.certificate), newServerCA.certificate...), 0o600); err != nil {
		t.Fatal(err)
	}
	overlap, _ := newCollectorProvider(t, c)
	flushCollectorSpan(t, overlap, true)
	requireCollectorReceipt(t, f, client)
	f.setTLS(newServer, collectorRoots(clientCA))
	before := f.calls.Load()
	flushCollectorSpan(t, old, false)
	if f.calls.Load() != before {
		t.Fatal("initialized exporter reloaded a projected CA bundle")
	}
	flushCollectorSpan(t, overlap, true)
	requireCollectorReceipt(t, f, client)
	if err := os.WriteFile(c.CAFile, newServerCA.certificate, 0o600); err != nil {
		t.Fatal(err)
	}
	current, _ := newCollectorProvider(t, c)
	// The old server leaf remains valid; only its authority is now retired.
	f.setTLS(oldServer, collectorRoots(clientCA))
	flushCollectorSpan(t, overlap, true)
	requireCollectorReceipt(t, f, client)
	before = f.calls.Load()
	flushCollectorSpan(t, current, false)
	if f.calls.Load() != before {
		t.Fatal("replacement exporter still trusted the retired collector authority")
	}
	f.setTLS(newServer, collectorRoots(clientCA))
	flushCollectorSpan(t, current, true)
	requireCollectorReceipt(t, f, client)
	if f.calls.Load() != 5 {
		t.Fatal("collector trust transition lost a successful control")
	}
}
