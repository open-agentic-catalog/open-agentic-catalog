package runtime

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"math/big"
	"strings"
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/config"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/demodata"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/fetcher"
)

func TestDemoArchiveDisabledAndPreflight(t *testing.T) {
	c := config.Defaults()
	ordinary := &recordingExternalOutbound{digest: "sha256:ordinary"}
	got, err := newDemoArchiveOutbound(c, ordinary)
	if err != nil || got != ordinary {
		t.Fatal("disabled route changed")
	}
	c.DemoArchives = config.DemoArchives{Origin: "https://127.0.0.1:18443"}
	if _, err := newDemoArchiveOutbound(c, ordinary); err == nil {
		t.Fatal("missing trust accepted")
	}
	c.Environment = "production"
	if _, err := newDemoArchiveOutbound(c, ordinary); err == nil {
		t.Fatal("production enabled")
	}
	d := demoArchiveOutbound{ordinary: ordinary, origin: "https://127.0.0.1:18443", digest: "sha256:demo"}
	valid := fetcher.Request{URL: d.origin + "/repository-essentials.zip", Method: "GET", TenantID: demodata.TenantID, IdentityEncoding: true, Accept: []string{catalog.PluginMediaZIP}, ExpectedPolicyDigest: d.digest}
	for name, mutate := range map[string]func(*fetcher.Request){
		"tenant": func(r *fetcher.Request) { r.TenantID = "other" }, "header": func(r *fetcher.Request) { r.Headers = map[string]string{"Authorization": "private"} }, "cookie": func(r *fetcher.Request) { r.Headers = map[string]string{"Cookie": "private"} }, "credentials": func(r *fetcher.Request) { r.CredentialBinding = "private" }, "profile": func(r *fetcher.Request) { r.NetworkProfile = "private" }, "path": func(r *fetcher.Request) { r.URL = d.origin + "/unknown.zip" }, "query": func(r *fetcher.Request) { r.URL += "?a=1" }, "fragment": func(r *fetcher.Request) { r.URL += "#a" }, "encoding": func(r *fetcher.Request) { r.URL = d.origin + "/%72epository-essentials.zip" }, "media": func(r *fetcher.Request) { r.Accept = []string{"application/json"} }, "method": func(r *fetcher.Request) { r.Method = "POST" }, "body": func(r *fetcher.Request) { r.Body = []byte("private") }, "identity": func(r *fetcher.Request) { r.IdentityEncoding = false }, "etag": func(r *fetcher.Request) { r.ETag = "private" }, "credential revision": func(r *fetcher.Request) { r.ExpectedCredentialRevision = "private" },
	} {
		t.Run(name, func(t *testing.T) {
			r := valid
			mutate(&r)
			if _, err := d.Fetch(t.Context(), r); !errors.Is(err, fetcher.ErrPolicyDenied) {
				t.Fatalf("preflight: %v", err)
			}
			if ordinary.request.URL != "" {
				t.Fatal("contacted ordinary route")
			}
		})
	}
	r := valid
	r.ExpectedPolicyDigest = "old"
	if _, err := d.Fetch(t.Context(), r); !errors.Is(err, fetcher.ErrPolicyChanged) {
		t.Fatal("stale policy admitted")
	}
	r = valid
	r.URL = "https://public.example.com/card.json"
	if _, err := d.Fetch(t.Context(), r); err != nil {
		t.Fatal(err)
	}
	if ordinary.request.ExpectedPolicyDigest != ordinary.digest || ordinary.request.NetworkProfile != "" {
		t.Fatal("ordinary authority changed")
	}
}

func TestDemoArchiveConstructorAndBoundEvidence(t *testing.T) {
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(1), NotBefore: time.Unix(0, 0), NotAfter: time.Unix(1<<31, 0), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	der, err := x509.CreateCertificate(rand.Reader, template, template, public, private)
	if err != nil {
		t.Fatal(err)
	}
	c := config.Defaults()
	c.DemoArchives = config.DemoArchives{Origin: "https://127.0.0.1:18443", TrustBundlePEM: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), TrustDigest: "fixture-trust", Allowlist: demodata.DefaultPlan("expanded", "").Allowlist()}
	ordinary := &recordingExternalOutbound{digest: "sha256:" + strings.Repeat("a", 64)}
	outbound, err := newDemoArchiveOutbound(c, ordinary)
	if err != nil {
		t.Fatal(err)
	}
	if outbound.PolicyDigest() == ordinary.PolicyDigest() {
		t.Fatal("fixture authority not bound")
	}
	changed := c
	changed.DemoArchives.TrustDigest = "other-trust"
	other, err := newDemoArchiveOutbound(changed, ordinary)
	if err != nil || other.PolicyDigest() == outbound.PolicyDigest() {
		t.Fatal("trust change not bound")
	}
	for _, origin := range []string{"https://%invalid", "https://public.example.com", "https://8.8.8.8"} {
		bad := c
		bad.DemoArchives.Origin = origin
		if _, err := newDemoArchiveOutbound(bad, ordinary); err == nil {
			t.Fatal("invalid constructor origin", origin)
		}
	}
	bad := c
	bad.Federation.RequestTimeout.Duration = 0
	if _, err := newDemoArchiveOutbound(bad, ordinary); err == nil {
		t.Fatal("invalid budget admitted")
	}
	raw, digest, _ := demodata.Archive("/repository-essentials.zip")
	fixture := &recordingExternalOutbound{result: fetcher.Result{Status: 200, MediaType: catalog.PluginMediaZIP, Body: raw, BodyDigest: digest}}
	d, ok := outbound.(demoArchiveOutbound)
	if !ok {
		t.Fatal("missing fixture route")
	}
	d.fixture = fixture
	r := fetcher.Request{URL: d.origin + "/repository-essentials.zip", Method: "GET", TenantID: demodata.TenantID, IdentityEncoding: true, Accept: []string{catalog.PluginMediaZIP}, ExpectedPolicyDigest: d.digest}
	result, err := d.Fetch(t.Context(), r)
	if err != nil || result.PolicyDigest != d.digest || !bytes.Equal(result.Body, raw) {
		t.Fatal("exact evidence lost", err)
	}
	if !fixture.request.RejectRedirects || fixture.request.NetworkProfile != "demo-archives" || fixture.request.ExpectedPolicyDigest != d.digest || len(fixture.request.Headers) != 0 {
		t.Fatal("fixture authority not confined")
	}
	for _, fault := range []string{"status", "media", "digest", "error"} {
		fixture.result = fetcher.Result{Status: 200, MediaType: catalog.PluginMediaZIP, Body: raw, BodyDigest: digest}
		fixture.err = nil
		switch fault {
		case "status":
			fixture.result.Status = 404
		case "media":
			fixture.result.MediaType = "application/json"
		case "digest":
			fixture.result.BodyDigest = "sha256:wrong"
		case "error":
			fixture.err = errors.New("private dependency detail")
		}
		if _, err := d.Fetch(t.Context(), r); err == nil {
			t.Fatal("invalid evidence accepted", fault)
		}
	}
}
