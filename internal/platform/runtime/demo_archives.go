// The development fixture route admits fixed anonymous archives, never arbitrary private representations.
package runtime

import (
	"context"
	"crypto/sha256"
	"crypto/x509"
	"fmt"
	"net/http"
	"net/netip"
	"net/url"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/config"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/demoarchive"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/fetcher"
)

type demoArchiveOutbound struct {
	ordinary       externalRepresentationOutbound
	fixture        externalRepresentationOutbound
	origin, digest string
	entries        map[string]demoarchive.Entry
}

// newDemoArchiveOutbound leaves disabled deployments on their unchanged ordinary route.
func newDemoArchiveOutbound(c config.Config, ordinary externalRepresentationOutbound) (externalRepresentationOutbound, error) {
	if c.DemoArchives.Origin == "" {
		return ordinary, nil
	}
	if c.Environment != "development" || c.Auth.Mode != "development" {
		return nil, fmt.Errorf("demo archives require development authentication")
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(c.DemoArchives.TrustBundlePEM) {
		return nil, fmt.Errorf("demo archive trust is unavailable")
	}
	u, err := url.Parse(c.DemoArchives.Origin)
	if err != nil {
		return nil, err
	}
	ip, err := netip.ParseAddr(u.Hostname())
	if err != nil || !(ip.IsPrivate() || ip.IsLoopback()) {
		return nil, fmt.Errorf("demo archive address is invalid")
	}
	if demoarchive.ValidateOrigin(c.DemoArchives.Origin) != nil || c.DemoArchives.Allowlist.Validate() != nil {
		return nil, fmt.Errorf("invalid demo archive policy inputs")
	}
	entries := map[string]demoarchive.Entry{}
	for _, e := range c.DemoArchives.Allowlist.Entries {
		entries[e.Path] = e
	}
	binding := ordinary.PolicyDigest() + "\n" + c.DemoArchives.Origin + "\n" + c.DemoArchives.TrustDigest + "\ndemo-archives-v2\n" + c.DemoArchives.Allowlist.Digest()
	digest := fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(binding)))
	f, err := fetcher.NewRouter(fetcher.RoutingPolicy{ID: "development-demo-archives", Revision: 1, Digest: digest, Public: fetcher.PublicRoute{Mode: fetcher.PublicDisabled}, PrivateRoutes: []fetcher.PrivateRoute{{Name: "demo-archives", Origins: []string{c.DemoArchives.Origin}, AddressPrefixes: []netip.Prefix{netip.PrefixFrom(ip, ip.BitLen())}, AssignedTenantIDs: []string{demoarchive.TenantID}, TrustBundlePEM: c.DemoArchives.TrustBundlePEM, AllowTestAddresses: true}}, Timeout: c.Federation.RequestTimeout.Duration, MaxResponseBytes: 64 << 10, MaxRedirects: 0}, nil)
	if err != nil {
		return nil, err
	}
	return demoArchiveOutbound{ordinary: ordinary, fixture: f, origin: c.DemoArchives.Origin, digest: digest, entries: entries}, nil
}

func (d demoArchiveOutbound) PolicyDigest() string { return d.digest }

// Fetch selects the fixture only for its exact tenant, ZIP path and anonymous GET envelope.
func (d demoArchiveOutbound) Fetch(ctx context.Context, r fetcher.Request) (fetcher.Result, error) {
	if r.ExpectedPolicyDigest != "" && r.ExpectedPolicyDigest != d.digest {
		return fetcher.Result{}, fetcher.ErrPolicyChanged
	}
	u, err := url.Parse(r.URL)
	if err != nil {
		return fetcher.Result{}, fetcher.ErrInvalidTarget
	}
	if u.Scheme+"://"+u.Host != d.origin {
		r.ExpectedPolicyDigest = d.ordinary.PolicyDigest()
		result, err := d.ordinary.Fetch(ctx, r)
		result.PolicyDigest = d.digest
		return result, err
	}
	expected, ok := d.entries[u.Path]
	if !ok || r.TenantID != demoarchive.TenantID || !r.IdentityEncoding || r.Method != http.MethodGet || len(r.Headers) != 0 || len(r.Body) != 0 || r.CredentialBinding != "" || r.NetworkProfile != "" || r.ExpectedCredentialRevision != "" || r.ETag != "" || r.LastModified != "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.RawPath != "" || len(r.Accept) != 1 || r.Accept[0] != catalog.PluginMediaZIP {
		return fetcher.Result{}, fetcher.ErrPolicyDenied
	}
	r.ExpectedPolicyDigest = d.digest
	r.RejectRedirects = true
	r.NetworkProfile = "demo-archives"
	result, err := d.fixture.Fetch(ctx, r)
	if err != nil {
		return fetcher.Result{}, err
	}
	if result.Status != 200 || result.MediaType != catalog.PluginMediaZIP || result.BodyDigest != expected.Digest || len(result.Body) != expected.Size {
		return fetcher.Result{}, fetcher.ErrPolicyDenied
	}
	result.PolicyID = "development-demo-archives"
	result.PolicyRevision = 1
	result.PolicyDigest = d.digest
	return result, nil
}
