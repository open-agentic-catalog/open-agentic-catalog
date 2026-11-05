//go:build functional

package runtime

import (
	"bytes"
	"context"
	"encoding/pem"
	"errors"
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/artwork"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/config"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/fetcher"
)

// This fixture qualifies the real media adapter, tenant-assigned private route,
// scoped TLS trust and redirect refusal without any external image host.
func TestArtworkOwnedTLSPrivateRouteAndCredentialIsolation(t *testing.T) {
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, image.NewNRGBA(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatal(err)
	}
	var contacts atomic.Int32
	peer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		contacts.Add(1)
		if r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" || r.Header.Get("X-Api-Key") != "" || r.TLS == nil || len(r.TLS.PeerCertificates) != 0 {
			t.Error("image request inherited credentials")
		}
		if r.URL.Path == "/redirect" {
			http.Redirect(w, r, "/image", http.StatusFound)
			return
		}
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(encoded.Bytes())
	}))
	defer peer.Close()
	origin, err := fetcher.NormalizeOrigin(peer.URL, false)
	if err != nil {
		t.Fatal(err)
	}
	const tenant = "00000000-0000-7000-8000-000000000001"
	cfg := config.Defaults()
	cfg.Environment = "test"
	cfg.Federation.Policy.Public = config.FederationPublicPolicy{Mode: fetcher.PublicDisabled}
	cfg.Federation.Policy.PrivateProfiles = []config.FederationPrivateProfile{{Name: "fixture-media", Origins: []string{origin}, AddressRanges: []string{"127.0.0.1/32"}, AssignedTenantIDs: []string{tenant}, TransportRef: "direct", TrustBundlePEM: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: peer.Certificate().Raw})}}
	cfg.ListingMedia.AllowedOrigins = []config.MediaOrigin{{Origin: origin, NetworkProfile: "fixture-media"}}
	cfg.Federation.PolicyDigest = "sha256:" + strings.Repeat("1", 64)
	policy, err := federationRoutingPolicy(cfg, "owned-artwork-test")
	if err != nil {
		t.Fatal(err)
	}
	cfg.Federation.PolicyDigest = policy.Digest
	router, err := fetcher.NewRouter(policy, nil)
	if err != nil {
		t.Fatal(err)
	}
	s, err := newArtworkService(cfg, router)
	if err != nil {
		t.Fatal(err)
	}
	input := artwork.Input{SchemaVersion: 1, Icon: &artwork.AssetInput{URL: peer.URL + "/image", Alt: "Owned test image", Rights: &artwork.Rights{Basis: "owned", Attribution: "Catalog synthetic TLS fixture"}}}
	descriptor, err := s.Seal(context.Background(), tenant, input)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Deliver(context.Background(), tenant, *descriptor.Icon, true, false); err != nil {
		t.Fatal(err)
	}
	if contacts.Load() != 2 {
		t.Fatalf("expected one seal and delivery contact, got %d", contacts.Load())
	}
	if _, err = s.Deliver(context.Background(), "00000000-0000-7000-8000-000000000002", *descriptor.Icon, true, false); !errors.Is(err, artwork.ErrUnavailable) {
		t.Fatalf("unassigned tenant bypassed route: %v", err)
	}
	if contacts.Load() != 2 {
		t.Fatal("unassigned tenant contacted image host")
	}
	for _, denyOrigin := range []bool{false, true} {
		deniedPolicy := policy
		if denyOrigin {
			deniedPolicy.DeniedOrigins = []string{origin}
		} else {
			deniedPolicy.DeniedAddressPrefixes = []netip.Prefix{netip.MustParsePrefix("127.0.0.1/32")}
		}
		deniedRouter, denyErr := fetcher.NewRouter(deniedPolicy, nil)
		if denyErr != nil {
			t.Fatal(denyErr)
		}
		deniedService, denyErr := newArtworkService(cfg, deniedRouter)
		if denyErr != nil {
			t.Fatal(denyErr)
		}
		if _, denyErr = deniedService.Deliver(t.Context(), tenant, *descriptor.Icon, true, false); !errors.Is(denyErr, artwork.ErrUnavailable) {
			t.Fatalf("test loopback bypassed global denial: %v", denyErr)
		}
		if contacts.Load() != 2 {
			t.Fatal("denied test loopback reached HTTP")
		}
	}
	mutable := artwork.Asset{AssetInput: artwork.AssetInput{URL: peer.URL + "/redirect"}}
	if _, err = s.Deliver(context.Background(), tenant, mutable, true, true); !errors.Is(err, artwork.ErrUnavailable) {
		t.Fatalf("redirect accepted: %v", err)
	}
	if contacts.Load() != 3 {
		t.Fatal("redirect followed to image")
	}
	policy.PrivateRoutes[0].TrustBundlePEM = nil
	untrusted, err := fetcher.NewRouter(policy, nil)
	if err != nil {
		t.Fatal(err)
	}
	s, err = newArtworkService(cfg, untrusted)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Deliver(context.Background(), tenant, *descriptor.Icon, true, false); !errors.Is(err, artwork.ErrUnavailable) {
		t.Fatalf("private TLS accepted without trust: %v", err)
	}
	if contacts.Load() != 3 {
		t.Fatal("untrusted TLS reached HTTP handler")
	}
}
