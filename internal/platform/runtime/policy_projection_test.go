// Policy projection units use local configuration values without constructing
// the runtime, fetching destinations or loading operator policy files.
package runtime

import (
	"errors"
	"net/netip"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/federation"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/config"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/fetcher"
)

func TestFederationDiscoveryTargetsProjectValidatedHosts(t *testing.T) {
	t.Parallel()
	const tenantA = "00000000-0000-7000-8000-000000000011"
	const tenantB = "00000000-0000-7000-8000-000000000022"
	for _, test := range []struct {
		name  string
		hosts []config.DiscoveryHost
		want  []federation.DiscoveryTarget
	}{
		{name: "no hosts"},
		{name: "only disabled", hosts: []config.DiscoveryHost{
			{Host: "disabled.example", TenantID: tenantB, Mode: "disabled", PublisherDomain: "publisher.example"},
		}},
		{name: "mixed hosts and tenants", hosts: []config.DiscoveryHost{
			{Host: "disabled-first.example", TenantID: tenantB, Mode: "disabled", PublisherDomain: "publisher.example"},
			{Host: "public.example", TenantID: tenantA, Mode: "anonymous", PublisherDomain: "public-publisher.example"},
			{Host: "disabled-middle.example", TenantID: tenantA, Mode: "disabled", PublisherDomain: "publisher.example"},
			{Host: "authenticated.example", TenantID: tenantB, Mode: "authenticated", PublisherDomain: "private-publisher.example"},
		}, want: []federation.DiscoveryTarget{
			{Host: "public.example", TenantID: tenantA, Audience: federation.AudienceAnonymous},
			{Host: "authenticated.example", TenantID: tenantB, Audience: federation.AudienceAuthenticated},
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			configuration := config.Defaults()
			configuration.Discovery.Hosts = slices.Clone(test.hosts)
			if err := configuration.ValidateFor(config.RoleWorker); err != nil {
				t.Fatalf("invalid discovery fixture: %v", err)
			}
			got := federationDiscoveryTargets(configuration.Discovery.Hosts)
			if len(got) != len(test.want) || len(test.want) > 0 && !reflect.DeepEqual(got, test.want) {
				t.Fatalf("discovery targets = %#v; want %#v", got, test.want)
			}
			if !reflect.DeepEqual(configuration.Discovery.Hosts, test.hosts) {
				t.Fatal("discovery projection changed configured hosts")
			}
		})
	}
}

func webhookProjectionConfig(environment string) config.Config {
	return config.Config{
		Environment: environment,
		Federation: config.Federation{
			RequestTimeout: config.Duration{Duration: 4 * time.Second}, MaxResponseBytes: 8 << 20, MaxRedirects: 7,
			PolicyDigest: "sha256:" + strings.Repeat("a", 64),
			Policy: config.FederationPolicy{SchemaVersion: 1, PolicyID: "federation-only", Revision: 3,
				Public:                  config.FederationPublicPolicy{Mode: "any_public_https"},
				AdditionalDeniedOrigins: []string{"https://catalog-denied.example:443"}, AdditionalDeniedAddressRanges: []string{"192.168.10.0/24"},
				PrivateProfiles: []config.FederationPrivateProfile{{Name: "catalogs", Origins: []string{"https://catalog.internal:443"}, AddressRanges: []string{"10.10.0.0/16"}, AssignedTenantIDs: []string{"00000000-0000-7000-8000-000000000011"}, TrustBundlePEM: []byte("federation trust bytes"), TransportRef: "direct"}},
			},
		},
		Webhooks: config.Webhooks{
			Enabled: true, PolicyFile: "/mounted/webhooks/policy.json", Timeout: config.Duration{Duration: 17 * time.Second},
			PolicyDigest: "sha256:" + strings.Repeat("b", 64),
			Policy: config.FederationPolicy{SchemaVersion: 1, PolicyID: "webhooks-only", Revision: 9,
				Public:                  config.FederationPublicPolicy{Mode: "allowlist", Origins: []string{"https://receiver.example:443"}},
				AdditionalDeniedOrigins: []string{"https://receiver-denied.example:443"}, AdditionalDeniedAddressRanges: []string{"192.168.20.0/24"},
				// Trust bytes are opaque to this projection; loader and sender tests own certificate validation.
				PrivateProfiles: []config.FederationPrivateProfile{{Name: "receivers", Origins: []string{"https://receiver.internal:8443"}, AddressRanges: []string{"10.20.0.0/16", "fd20::/48"}, AssignedTenantIDs: []string{"00000000-0000-7000-8000-000000000022"}, TrustBundlePEM: []byte("webhook trust bytes"), TransportRef: "direct"}},
			},
		},
	}
}

func TestWebhookRoutingPolicyUsesIndependentTopologyAndLimits(t *testing.T) {
	t.Parallel()
	for _, environment := range []string{"development", "test", "production"} {
		t.Run(environment, func(t *testing.T) {
			configuration := webhookProjectionConfig(environment)
			before := webhookProjectionConfig(environment)
			got, err := webhookRoutingPolicy(configuration, "1.2.3")
			if err != nil {
				t.Fatal(err)
			}
			want := fetcher.RoutingPolicy{
				ID: "webhooks-only", Revision: 9, Digest: "sha256:" + strings.Repeat("b", 64),
				Public:                fetcher.PublicRoute{Mode: fetcher.PublicAllowlist, Origins: []string{"https://receiver.example:443"}},
				DeniedOrigins:         []string{"https://receiver-denied.example:443"},
				DeniedAddressPrefixes: []netip.Prefix{netip.MustParsePrefix("192.168.20.0/24")},
				PrivateRoutes: []fetcher.PrivateRoute{{
					Name: "receivers", Origins: []string{"https://receiver.internal:8443"},
					AddressPrefixes:   []netip.Prefix{netip.MustParsePrefix("10.20.0.0/16"), netip.MustParsePrefix("fd20::/48")},
					AssignedTenantIDs: []string{"00000000-0000-7000-8000-000000000022"}, TrustBundlePEM: []byte("webhook trust bytes"), AllowTestAddresses: environment == "test",
				}},
				AllowHTTPForTests: false, Timeout: 17 * time.Second, MaxResponseBytes: 1 << 20, MaxRedirects: 0,
				UserAgent: "open-agentic-catalog-webhooks/1.2.3",
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("webhook routing policy = %#v; want %#v", got, want)
			}
			if !reflect.DeepEqual(configuration, before) {
				t.Fatal("webhook projection changed its input configuration")
			}
			got.Public.Origins[0] = "https://changed.example:443"
			got.DeniedOrigins[0] = "https://changed-denial.example:443"
			got.PrivateRoutes[0].Name = "changed-profile"
			got.PrivateRoutes[0].Origins[0] = "https://changed.internal:443"
			got.PrivateRoutes[0].AssignedTenantIDs[0] = "00000000-0000-7000-8000-000000000033"
			got.PrivateRoutes[0].TrustBundlePEM[0] = 'X'
			if !reflect.DeepEqual(configuration, before) {
				t.Fatal("projected webhook policy aliases configuration slices or trust bytes")
			}
		})
	}
}

func TestWebhookRoutingPolicyPropagatesConversionFailure(t *testing.T) {
	t.Parallel()
	configuration := webhookProjectionConfig("test")
	configuration.Webhooks.Policy.PrivateProfiles[0].AddressRanges = []string{"invalid"}
	before := webhookProjectionConfig("test")
	before.Webhooks.Policy.PrivateProfiles[0].AddressRanges = []string{"invalid"}
	got, err := webhookRoutingPolicy(configuration, "1.2.3")
	if err == nil || errors.Unwrap(err) == nil || !strings.Contains(err.Error(), "parse normalized federation address range") || !reflect.DeepEqual(got, fetcher.RoutingPolicy{}) {
		t.Fatalf("invalid policy projection = %#v, %v; want no policy and the wrapped conversion error", got, err)
	}
	if !reflect.DeepEqual(configuration, before) {
		t.Fatal("failed webhook projection changed its input configuration")
	}
}
