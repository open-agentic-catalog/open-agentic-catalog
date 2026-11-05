//go:build functional

package runtime

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"log/slog"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/config"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/demoarchive"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/demodata"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/fetcher"
	"github.com/open-agentic-catalog/open-agentic-catalog/test/demofixture"
)

func TestDemoArchiveRealTLSAndFailures(t *testing.T) {
	for _, fault := range []string{"valid", "tamper", "redirect", "oversize", "untrusted", "outage"} {
		t.Run(fault, func(t *testing.T) {
			var requests atomic.Int64
			peer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				if r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" || r.Header.Get("Accept-Encoding") != "identity" {
					t.Error("unsafe archive request")
				}
				switch fault {
				case "tamper":
					w.Header().Set("Content-Type", catalog.PluginMediaZIP)
					_, _ = w.Write([]byte("changed"))
				case "redirect":
					http.Redirect(w, r, "/documentation-toolkit.zip", http.StatusFound)
				case "oversize":
					w.Header().Set("Content-Type", catalog.PluginMediaZIP)
					_, _ = w.Write(bytes.Repeat([]byte("x"), 70<<10))
				default:
					demofixture.Handler().ServeHTTP(w, r)
				}
			}))
			defer peer.Close()
			c := config.Defaults()
			c.DemoArchives = config.DemoArchives{Origin: peer.URL, TrustBundleFile: "fixture.pem", TrustBundlePEM: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: peer.Certificate().Raw}), TrustDigest: "fixture-trust", Allowlist: demodata.DefaultPlan("expanded", "").Allowlist()}
			if fault == "untrusted" {
				key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
				if err != nil {
					t.Fatal(err)
				}
				cert, err := x509.CreateCertificate(rand.Reader, &x509.Certificate{SerialNumber: big.NewInt(1), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour)}, &x509.Certificate{SerialNumber: big.NewInt(1), IsCA: true, BasicConstraintsValid: true}, &key.PublicKey, key)
				if err != nil {
					t.Fatal(err)
				}
				c.DemoArchives.TrustBundlePEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert})
			}
			ordinary := &recordingExternalOutbound{digest: "sha256:" + strings.Repeat("a", 64)}
			outbound, err := newDemoArchiveOutbound(c, ordinary)
			if err != nil {
				t.Fatal(err)
			}
			if fault == "outage" {
				peer.Close()
			}
			serviceFetch := externalRepresentationFetcher{outbound: outbound}
			for _, path := range []string{"/repository-essentials.zip", "/documentation-toolkit.zip", "/release-workflow.zip"} {
				result, err := serviceFetch.FetchExternalRepresentation(t.Context(), catalog.ExternalRepresentationFetchRequest{TenantID: demodata.TenantID, URL: peer.URL + path, MediaType: catalog.PluginMediaZIP, MaxResponseBytes: 64 << 10})
				if fault != "valid" {
					if err == nil {
						t.Fatal("fault accepted", fault)
					}
					break
				}
				raw, digest, _ := demodata.Archive(path)
				if err != nil || !bytes.Equal(result.Body, raw) || result.ContentDigest != digest || result.PolicyDigest != outbound.PolicyDigest() {
					t.Fatalf("invalid archive retrieval: %v", err)
				}
			}
			if ordinary.request.URL != "" {
				t.Fatal("fixture failure fell back to ordinary routing")
			}
			if fault == "untrusted" && requests.Load() != 0 {
				t.Fatal("request reached untrusted peer")
			}
			if fault == "redirect" && requests.Load() != 1 {
				t.Fatal("redirect followed")
			}
			// Disabled routing is the existing public transport; no private fixture is admitted.
			router, err := fetcher.NewRouter(fetcher.RoutingPolicy{ID: "disabled", Revision: 1, Digest: "sha256:" + strings.Repeat("b", 64), Public: fetcher.PublicRoute{Mode: fetcher.PublicDisabled}, Timeout: c.Federation.RequestTimeout.Duration, MaxResponseBytes: 64 << 10}, nil)
			if err != nil {
				t.Fatal(err)
			}
			disabled, err := newDemoArchiveOutbound(config.Defaults(), router)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := disabled.Fetch(t.Context(), fetcher.Request{URL: peer.URL + "/repository-essentials.zip", Method: "GET"}); err == nil {
				t.Fatal("disabled fixture accessed")
			}
		})
	}
}

// This qualifies the actual composition/configuration-to-inspection path, not a mocked success response.
func TestDemoArchiveRuntimeInspection(t *testing.T) {
	peer := httptest.NewTLSServer(demofixture.Handler())
	defer peer.Close()
	bundle := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: peer.Certificate().Raw})
	path := filepath.Join(t.TempDir(), "archive-ca.pem")
	if err := os.WriteFile(path, bundle, 0600); err != nil {
		t.Fatal(err)
	}
	cfg := localRuntimeConfig()
	cfg.Environment = "development"
	prepared := filepath.Join(t.TempDir(), "prepared")
	if err := demodata.Prepare(prepared, demodata.DefaultPlan("expanded", peer.URL)); err != nil {
		t.Fatal(err)
	}
	cfg.DemoArchives = config.DemoArchives{Origin: peer.URL, TrustBundleFile: path, AllowlistFile: filepath.Join(prepared, demodata.AllowlistFilename)}
	cfg.Auth.DevelopmentTokens["demo-admin"] = config.DevelopmentUser{Subject: "demo-admin", TenantID: demodata.TenantID, Roles: []identity.Role{identity.RoleTenantAdmin, identity.RoleContributor, identity.RoleReader}}
	runtime, err := Build(t.Context(), BuildOptions{ConfigPath: writeRuntimeConfig(t, cfg), Role: config.RoleAll, Version: "test", Commit: "test", Logger: slog.New(slog.DiscardHandler)})
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	actor, err := runtime.Authenticator.Authenticate(t.Context(), "demo-admin")
	if err != nil {
		t.Fatal(err)
	}
	ctx := identity.WithActor(t.Context(), actor)
	ns, err := runtime.Catalog.CreateNamespace(ctx, catalog.CreateNamespaceInput{Slug: "ide-plugins", DisplayName: "IDE plugins"})
	if err != nil {
		t.Fatal(err)
	}
	for _, sample := range demodata.Samples() {
		if sample.Kind != catalog.KindPlugin {
			continue
		}
		resource, err := runtime.Catalog.CreateResource(ctx, catalog.CreateResourceInput{NamespaceID: ns.ID, CanonicalIdentifier: "urn:air:demo.catalog.example.com:ide-plugins:" + sample.Key, Kind: sample.Kind})
		if err != nil {
			t.Fatal(err)
		}
		_, revision, evidence, err := runtime.Catalog.CreateVersion(ctx, resource.ID, catalog.CreateVersionInput{Version: "1.0.0", Representations: []catalog.RepresentationInput{demodata.Representation(sample, peer.URL)}})
		if err != nil || evidence.State != catalog.EvidenceComplete || len(revision.Representations) != 1 || revision.Representations[0].Plugin == nil {
			t.Fatalf("%s inspection: %v %+v", sample.Key, err, evidence)
		}
		_, digest, _ := demodata.Archive("/" + sample.Key + ".zip")
		if revision.Representations[0].ContentDigest != digest || len(revision.Representations[0].Plugin.Documents) < 2 {
			t.Fatal("retained Plugin evidence mismatch")
		}
	}
}

func TestDemoPreparedCustomArchivePolicy(t *testing.T) {
	// A new prepared path is admitted from the file, not the compiled default inventory.
	plan := demodata.DefaultPlan("expanded", "")
	plan.Resources = plan.Resources[len(plan.Resources)-1:]
	plan.Resources[0].Resource.Key = "additional-plugin"
	directory := filepath.Join(t.TempDir(), "prepared")
	if err := demodata.Prepare(directory, plan); err != nil {
		t.Fatal(err)
	}
	files, err := demodata.ReadPrepared(directory)
	if err != nil {
		t.Fatal(err)
	}
	var requests atomic.Int64
	peer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		demofixture.PreparedHandler(files).ServeHTTP(w, r)
	}))
	defer peer.Close()
	trust := filepath.Join(t.TempDir(), "trust.pem")
	if err := os.WriteFile(trust, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: peer.Certificate().Raw}), 0600); err != nil {
		t.Fatal(err)
	}
	cfg := localRuntimeConfig()
	cfg.Environment = "development"
	cfg.DemoArchives = config.DemoArchives{Origin: peer.URL, TrustBundleFile: trust, AllowlistFile: filepath.Join(directory, demodata.AllowlistFilename)}
	// Load the actual file-backed configuration too, using the same runtime constructor input contract.
	loaded, err := config.LoadFor(writeRuntimeConfig(t, cfg), config.RoleAll)
	if err != nil {
		t.Fatal(err)
	}
	ordinary := &recordingExternalOutbound{digest: "sha256:" + strings.Repeat("a", 64)}
	outbound, err := newDemoArchiveOutbound(loaded, ordinary)
	if err != nil {
		t.Fatal(err)
	}
	request := fetcher.Request{URL: peer.URL + "/additional-plugin.zip", TenantID: demodata.TenantID, Method: "GET", IdentityEncoding: true, Accept: []string{catalog.PluginMediaZIP}}
	got, err := outbound.Fetch(t.Context(), request)
	if err != nil || !bytes.Equal(got.Body, plan.Resources[0].Archive) {
		t.Fatal("custom prepared archive rejected", err)
	}
	request.URL = peer.URL + "/release-workflow.zip"
	if _, err := outbound.Fetch(t.Context(), request); err == nil || requests.Load() != 1 {
		t.Fatal("unlisted compiled-default path fetched")
	}
	changed := loaded
	changed.DemoArchives.Allowlist.Entries = append([]demoarchive.Entry{}, loaded.DemoArchives.Allowlist.Entries...)
	changed.DemoArchives.Allowlist.Entries[0].Digest = "sha256:" + strings.Repeat("b", 64)
	mismatched, err := newDemoArchiveOutbound(changed, ordinary)
	if err != nil || mismatched.PolicyDigest() == outbound.PolicyDigest() {
		t.Fatal("prepared identities not in policy digest", err)
	}
	request.URL = peer.URL + "/additional-plugin.zip"
	if _, err := mismatched.Fetch(t.Context(), request); err == nil {
		t.Fatal("server/allowlist disagreement accepted")
	}
}
