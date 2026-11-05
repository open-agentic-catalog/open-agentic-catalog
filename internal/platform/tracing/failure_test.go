// Unit tests exercise isolated validation and controlled dependencies.
package tracing

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"math/big"
	"strings"
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/config"
)

func TestSecretInputsFailClosed(t *testing.T) {
	for _, raw := range []string{`null`, `{}`, `{"Authorization":"a","authorization":"b"}`, `{"Host":"other"}`, `{"Traceparent":"x"}`, `{"Content-Encoding":"gzip"}`, `{"Authorization":"line\nbreak"}`, `{"Authorization":"bad\u0001value"}`, `{"X-Test":1}`} {
		c := config.Defaults().Tracing
		c.HeadersFile = inputFile(t, "headers.json", []byte(raw))
		if _, _, _, err := loadInputs(c); err == nil || strings.Contains(err.Error(), raw) {
			t.Fatal("invalid secret input accepted or exposed")
		}
	}
	if _, err := readInput(t.TempDir(), 100); err == nil {
		t.Fatal("directory accepted")
	}
	if _, err := readInput(inputFile(t, "large", make([]byte, 101)), 100); err == nil {
		t.Fatal("unbounded read")
	}
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	template := &x509.Certificate{SerialNumber: big.NewInt(1), NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour)}
	der, err := x509.CreateCertificate(rand.Reader, template, template, public, private)
	if err != nil {
		t.Fatal(err)
	}
	c := config.Defaults().Tracing
	certificate := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	for _, raw := range [][]byte{nil, append([]byte("garbage"), certificate...), append(certificate, []byte("garbage")...), []byte("-----BEGIN CERTIFICATE-----\nbad\n-----END CERTIFICATE-----")} {
		c.CAFile = inputFile(t, "ca.pem", raw)
		if _, _, _, err := loadInputs(c); err == nil {
			t.Fatal("malformed trust accepted")
		}
	}
}

func BenchmarkDisabledTrace(b *testing.B) {
	ctx := context.Background()
	b.ReportAllocs()
	for b.Loop() {
		_, end := catalog.StartTrace(ctx, "catalog search", catalog.TraceInternal, "")
		end("ok")
	}
}
