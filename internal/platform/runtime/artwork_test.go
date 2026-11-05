package runtime

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/png"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/artwork"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/config"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/fetcher"
)

type mediaOutboundFunc func(context.Context, fetcher.Request) (fetcher.Result, error)

func (f mediaOutboundFunc) Fetch(ctx context.Context, r fetcher.Request) (fetcher.Result, error) {
	return f(ctx, r)
}

func mediaRuntimeConfiguration() config.Config {
	c := config.Defaults()
	c.Federation.Policy.Public.Origins = []string{"https://images.example:443"}
	c.Federation.Policy.PrivateProfiles = []config.FederationPrivateProfile{{Name: "internal-images", Origins: []string{"https://images.internal.example:443"}, AssignedTenantIDs: []string{"tenant-a"}}}
	c.ListingMedia.AllowedOrigins = []config.MediaOrigin{{Origin: "https://images.example"}, {Origin: "https://images.internal.example", NetworkProfile: "internal-images"}}
	return c
}

func TestArtworkAdapterUsesOnlyOperatorOriginBinding(t *testing.T) {
	c := mediaRuntimeConfiguration()
	var calls int
	outbound := mediaOutboundFunc(func(ctx context.Context, r fetcher.Request) (fetcher.Result, error) {
		calls++
		if r.TenantID != "tenant-a" || r.NetworkProfile != "internal-images" || r.CredentialBinding != "" || r.ExpectedCredentialRevision != "" || len(r.Headers) != 0 || r.Method != http.MethodGet || !r.RejectRedirects || !r.RejectOriginChanges || !r.IdentityEncoding || !r.RedactQueryInErrors || r.ExpectedPolicyDigest != c.Federation.PolicyDigest || r.MaxResponseBytes != 128<<10 {
			t.Fatalf("wrong media request: %#v", r)
		}
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) > c.Federation.RequestTimeout.Duration {
			t.Fatal("adapter exceeded underlying Router deadline")
		}
		return fetcher.Result{Status: 200, MediaType: "image/png", Body: []byte("owned synthetic bytes")}, nil
	})
	adapter := artworkFetcher{outbound: outbound, configuration: c}
	r := artwork.FetchRequest{TenantID: "tenant-a", URL: "https://images.internal.example/i.png", MaxBytes: 128 << 10, Timeout: 3 * time.Second}
	got, err := adapter.Fetch(context.Background(), r)
	if err != nil || got.MediaType != "image/png" {
		t.Fatalf("admitted request: %#v %v", got, err)
	}
	for _, url := range []string{"https://other.example/i", "https://images.internal.example.attacker.test/i", "http://images.internal.example/i", "https://images.internal.example/i?token=synthetic", "https://user@images.internal.example/i"} {
		r.URL = url
		if _, err := adapter.Fetch(context.Background(), r); err == nil {
			t.Fatalf("forbidden media contact: %s", url)
		}
	}
	if calls != 1 {
		t.Fatalf("forbidden target reached Router %d times", calls)
	}
}

func TestArtworkAdapterCapsBytesAndRejectsHTTPFailures(t *testing.T) {
	c := mediaRuntimeConfiguration()
	c.Federation.MaxResponseBytes = 2048
	adapter := artworkFetcher{configuration: c, outbound: mediaOutboundFunc(func(_ context.Context, r fetcher.Request) (fetcher.Result, error) {
		if r.MaxResponseBytes != 2048 {
			t.Fatalf("outer byte budget bypass: %d", r.MaxResponseBytes)
		}
		return fetcher.Result{Status: 302}, nil
	})}
	r := artwork.FetchRequest{TenantID: "tenant-a", URL: "https://images.example/i", MaxBytes: 256 << 10, Timeout: 3 * time.Second}
	if _, err := adapter.Fetch(context.Background(), r); !errors.Is(err, artwork.ErrUnavailable) {
		t.Fatalf("redirect exposed as media success: %v", err)
	}
	c.ListingMedia.AllowedOrigins = nil
	adapter.configuration = c
	if _, err := adapter.Fetch(context.Background(), r); !errors.Is(err, fetcher.ErrPolicyDenied) {
		t.Fatalf("empty bindings did not deny before Router: %v", err)
	}
}

func TestArtworkAdapterRefusesEmptyBudgetsBeforeOutboundCalls(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*config.Config, *artwork.FetchRequest)
	}{
		{"request timeout", func(_ *config.Config, r *artwork.FetchRequest) { r.Timeout = 0 }},
		{"request bytes", func(_ *config.Config, r *artwork.FetchRequest) { r.MaxBytes = 0 }},
	} {
		t.Run(test.name, func(t *testing.T) {
			c := mediaRuntimeConfiguration()
			r := artwork.FetchRequest{TenantID: "tenant-a", URL: "https://images.example/icon", MaxBytes: 1024, Timeout: time.Second}
			test.change(&c, &r)
			adapter := artworkFetcher{configuration: c, outbound: mediaOutboundFunc(func(context.Context, fetcher.Request) (fetcher.Result, error) {
				t.Fatal("empty budget reached outbound transport")
				return fetcher.Result{}, nil
			})}
			if _, err := adapter.Fetch(t.Context(), r); !errors.Is(err, fetcher.ErrPolicyDenied) {
				t.Fatalf("empty budget accepted: %v", err)
			}
		})
	}
	cause := errors.New("controlled outbound failure")
	adapter := artworkFetcher{configuration: mediaRuntimeConfiguration(), outbound: mediaOutboundFunc(func(context.Context, fetcher.Request) (fetcher.Result, error) {
		return fetcher.Result{}, cause
	})}
	if got, err := adapter.Fetch(t.Context(), artwork.FetchRequest{TenantID: "tenant-a", URL: "https://images.example/icon", MaxBytes: 1024, Timeout: time.Second}); !errors.Is(err, cause) || len(got.Body) != 0 {
		t.Fatalf("outbound failure lost or exposed content: %+v %v", got, err)
	}
}

func TestArtworkPublicProjectionOmitsPrivateLocators(t *testing.T) {
	c := mediaRuntimeConfiguration()
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, image.NewNRGBA(image.Rect(0, 0, 1, 1))); err != nil {
		t.Fatal(err)
	}
	s, err := newArtworkService(c, mediaOutboundFunc(func(context.Context, fetcher.Request) (fetcher.Result, error) {
		return fetcher.Result{Status: 200, Body: encoded.Bytes(), MediaType: "image/png"}, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	input := artwork.Input{SchemaVersion: 1, Icon: &artwork.AssetInput{URL: "https://images.example/icon", Alt: "Icon", Rights: &artwork.Rights{Basis: "owned", Attribution: "Synthetic fixture"}}}
	d, err := s.Seal(context.Background(), "tenant-a", input)
	if err != nil {
		t.Fatal(err)
	}
	if !artworkPublicDescriptorAllowed(c, d) {
		t.Fatal("admitted public image omitted")
	}
	d.Screenshots = []artwork.Asset{*d.Icon}
	if !artworkPublicDescriptorAllowed(c, d) {
		t.Fatal("admitted public screenshot omitted")
	}
	d.Screenshots[0].URL = "https://images.internal.example/screenshot"
	if artworkPublicDescriptorAllowed(c, d) {
		t.Fatal("private screenshot projected alongside public icon")
	}
	d.Screenshots = nil
	anyPublic := c
	anyPublic.Federation.Policy.Public.Mode = fetcher.PublicAnyPublicHTTPS
	if !artworkPublicDescriptorAllowed(anyPublic, d) {
		t.Fatal("explicit media origin unavailable under public HTTPS policy")
	}
	d.Icon.URL = "https://images.internal.example/icon"
	if artworkPublicDescriptorAllowed(c, d) {
		t.Fatal("private locator projected publicly")
	}
	d.Icon.URL = "https://unconfigured.example/icon"
	if artworkPublicDescriptorAllowed(c, d) {
		t.Fatal("unadmitted locator projected publicly")
	}
	d.Icon.URL = input.Icon.URL
	d.Icon.RightsAssessment = "passed"
	if artworkPublicDescriptorAllowed(c, d) {
		t.Fatal("fabricated rights assessment projected")
	}
	d.Icon.RightsAssessment = artwork.NotAssessed
	for _, value := range []string{"https://127.0.0.1/icon", "https://10.0.0.1/icon", "https://[fc00::1]/icon", "https://169.254.169.254/icon", "https://192.0.2.1/icon"} {
		changed := c
		changed.Federation.Policy.Public.Origins = []string{value[:strings.LastIndex(value, "/")]}
		changed.ListingMedia.AllowedOrigins = []config.MediaOrigin{{Origin: value[:strings.LastIndex(value, "/")]}}
		d.Icon.URL = value
		if artworkPublicDescriptorAllowed(changed, d) {
			t.Fatalf("literal nonpublic locator projected: %s", value)
		}
	}
	d.Icon.URL = input.Icon.URL
	denied := c
	denied.Federation.Policy.AdditionalDeniedOrigins = []string{"https://images.example"}
	if artworkPublicDescriptorAllowed(denied, d) {
		t.Fatal("denied public origin projected")
	}
	disabled := c
	disabled.Federation.Policy.Public.Mode = fetcher.PublicDisabled
	if artworkPublicDescriptorAllowed(disabled, d) {
		t.Fatal("disabled public route projected")
	}
	addressDenied := c
	addressDenied.Federation.Policy.Public.Origins = []string{"https://8.8.8.8"}
	addressDenied.Federation.Policy.AdditionalDeniedAddressRanges = []string{"8.8.8.0/24"}
	addressDenied.ListingMedia.AllowedOrigins = []config.MediaOrigin{{Origin: "https://8.8.8.8"}}
	d.Icon.URL = "https://8.8.8.8/icon"
	if artworkPublicDescriptorAllowed(addressDenied, d) {
		t.Fatal("operator address denial projected")
	}
	if !strings.HasPrefix(mediaPolicyDigest(c), "sha256:") {
		t.Fatal("missing receipt fence")
	}
}
