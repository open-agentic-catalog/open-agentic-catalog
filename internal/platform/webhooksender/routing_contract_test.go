package webhooksender

import (
	"context"
	"net/netip"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/fetcher"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/webhook"
)

func TestPrivateRouteSelectionBindsNormalizedOriginAndTenant(t *testing.T) {
	const tenant = "00000000-0000-7000-8000-000000000001"
	for _, tc := range []struct{ name, tenant, url, profile string }{
		{"exact", tenant, "https://hooks.example.com/events", "receiver"},
		{"normalized", tenant, "https://HOOKS.example.com:443/events", "receiver"},
		{"different tenant", "00000000-0000-7000-8000-000000000002", "https://hooks.example.com/events", ""},
		{"different port", tenant, "https://hooks.example.com:8443/events", ""},
		{"lookalike host", tenant, "https://hooks.example.com.attacker.test/events", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			outbound := &recordingFetcher{result: fetcher.Result{Status: 204}}
			s := &Sender{fetcher: outbound, privateProfiles: map[string]string{tenant + "\x00https://hooks.example.com:443": "receiver"}}
			if _, err := s.Send(context.Background(), webhook.SendRequest{TenantID: tc.tenant, URL: tc.url}); err != nil {
				t.Fatal(err)
			}
			if outbound.request.TenantID != tc.tenant || outbound.request.NetworkProfile != tc.profile || !outbound.request.RejectRedirects {
				t.Fatalf("wrong routing request: tenant=%q profile=%q", outbound.request.TenantID, outbound.request.NetworkProfile)
			}
			// An empty profile delegates to the router's independently enforced
			// public policy; it never grants access to this private destination.
		})
	}
	for _, rawURL := range []string{"https://%zz", "http://hooks.example.com/events", "/relative"} {
		outbound := &recordingFetcher{}
		s := &Sender{fetcher: outbound, privateProfiles: map[string]string{tenant + "\x00https://hooks.example.com:443": "receiver"}}
		result, err := s.Send(context.Background(), webhook.SendRequest{TenantID: tenant, URL: rawURL})
		if err == nil || result != (webhook.SendResult{}) || outbound.request.URL != "" {
			t.Fatal("invalid private origin reached the fetch boundary")
		}
	}
}

func TestWebhookPolicyConstructionRejectsInvalidTopology(t *testing.T) {
	valid := fetcher.RoutingPolicy{ID: "webhooks", Revision: 1, Digest: "sha256:" + strings.Repeat("a", 64), Public: fetcher.PublicRoute{Mode: fetcher.PublicDisabled}, Timeout: time.Second, MaxResponseBytes: 1024}
	for _, change := range []func(*fetcher.RoutingPolicy){
		func(p *fetcher.RoutingPolicy) { p.AllowHTTPForTests = true },
		func(p *fetcher.RoutingPolicy) { p.ID = "" },
		func(p *fetcher.RoutingPolicy) { p.Timeout = 0 },
		func(p *fetcher.RoutingPolicy) { p.Public.Mode = "unknown" },
		func(p *fetcher.RoutingPolicy) { p.DeniedOrigins = []string{"http://unsafe.example"} },
	} {
		policy := valid
		change(&policy)
		if s, err := NewWithPolicy(policy); err == nil || s != nil {
			t.Fatal("invalid routing policy constructed a sender")
		}
	}
}

func TestWebhookPolicyConstructionBindsPrivateAssignments(t *testing.T) {
	const firstTenant = "00000000-0000-7000-8000-000000000001"
	const secondTenant = "00000000-0000-7000-8000-000000000002"
	policy := fetcher.RoutingPolicy{
		ID: "webhooks", Revision: 1, Digest: "sha256:" + strings.Repeat("a", 64),
		Public: fetcher.PublicRoute{Mode: fetcher.PublicDisabled}, Timeout: time.Second, MaxResponseBytes: 1024,
		PrivateRoutes: []fetcher.PrivateRoute{{
			Name: "receiver", Origins: []string{"https://HOOKS.example.com", "https://hooks.example.com:8443"},
			AddressPrefixes: []netip.Prefix{netip.MustParsePrefix("10.20.0.0/16")}, AssignedTenantIDs: []string{firstTenant, secondTenant},
		}},
	}
	sender, err := NewWithPolicy(policy)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		firstTenant + "\x00https://hooks.example.com:443":   "receiver",
		firstTenant + "\x00https://hooks.example.com:8443":  "receiver",
		secondTenant + "\x00https://hooks.example.com:443":  "receiver",
		secondTenant + "\x00https://hooks.example.com:8443": "receiver",
	}
	if !reflect.DeepEqual(sender.privateProfiles, want) {
		t.Fatalf("compiled private assignments = %#v, want %#v", sender.privateProfiles, want)
	}
	if _, ok := sender.fetcher.(*fetcher.Router); !ok {
		t.Fatal("policy constructor bypassed the hardened router")
	}
	// Different profiles assigning the same tenant and canonical origin are
	// ambiguous even if their original URL spelling differs.
	duplicate := policy.PrivateRoutes[0]
	duplicate.Name = "another-receiver"
	duplicate.Origins = []string{"https://hooks.example.com:443"}
	policy.PrivateRoutes = append(policy.PrivateRoutes, duplicate)
	if sender, err := NewWithPolicy(policy); sender != nil || err == nil || err.Error() != "webhook private routes have ambiguous tenant/origin assignments" {
		t.Fatalf("ambiguous webhook policy = %v, %v", sender, err)
	}
	policy.PrivateRoutes = nil
	sender, err = NewWithPolicy(policy)
	if err != nil || sender == nil || len(sender.privateProfiles) != 0 {
		t.Fatalf("policy without private routes = %v, %v", sender, err)
	}
}
