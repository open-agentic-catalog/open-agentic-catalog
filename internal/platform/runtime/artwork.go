package runtime

import (
	"context"
	"fmt"
	"net/http"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/artwork"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/config"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/fetcher"
)

type artworkOutbound interface {
	Fetch(context.Context, fetcher.Request) (fetcher.Result, error)
}

type artworkFetcher struct {
	outbound      artworkOutbound
	configuration config.Config
}

// Fetch builds a fresh credential-free request. Neither resource metadata nor
// source credentials can choose the operator's exact origin/profile binding.
func (f artworkFetcher) Fetch(ctx context.Context, request artwork.FetchRequest) (artwork.FetchResult, error) {
	if !catalog.ValidExternalHTTPSURL(request.URL) || request.TenantID == "" {
		return artwork.FetchResult{}, fetcher.ErrInvalidTarget
	}
	binding, allowed := f.configuration.ListingMedia.OriginBinding(request.URL)
	if !allowed {
		return artwork.FetchResult{}, fetcher.ErrPolicyDenied
	}
	timeout := min(request.Timeout, f.configuration.Federation.RequestTimeout.Duration)
	if timeout <= 0 {
		return artwork.FetchResult{}, fetcher.ErrPolicyDenied
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	maximum := min(request.MaxBytes, f.configuration.Federation.MaxResponseBytes)
	if maximum <= 0 {
		return artwork.FetchResult{}, fetcher.ErrPolicyDenied
	}
	result, err := f.outbound.Fetch(ctx, fetcher.Request{
		URL: request.URL, Method: http.MethodGet, Accept: []string{"image/png", "image/jpeg"}, IdentityEncoding: true,
		TenantID: request.TenantID, NetworkProfile: binding.NetworkProfile,
		CredentialBinding: "", ExpectedCredentialRevision: "", ExpectedPolicyDigest: f.configuration.Federation.PolicyDigest,
		RejectRedirects: true, RejectOriginChanges: true, MaxResponseBytes: maximum, RedactQueryInErrors: true,
	})
	if err != nil {
		return artwork.FetchResult{}, err
	}
	if result.Status != 200 {
		return artwork.FetchResult{}, artwork.ErrUnavailable
	}
	return artwork.FetchResult{Body: result.Body, MediaType: result.MediaType}, nil
}

func newArtworkService(configuration config.Config, outbound artworkOutbound) (*artwork.Service, error) {
	if outbound == nil {
		return nil, fmt.Errorf("artwork outbound boundary is required")
	}
	if err := configuration.ListingMedia.Validate(configuration.Federation.Policy); err != nil {
		return nil, err
	}
	return artwork.New(artworkFetcher{outbound: outbound, configuration: configuration}, configuration.ListingMedia.ArtworkLimits())
}

func mediaPolicyDigest(configuration config.Config) string {
	return configuration.ListingMedia.PolicyDigest(configuration.Federation.PolicyDigest)
}

// artworkPublicDescriptorAllowed excludes every private-route locator from
// public snapshot projection without fetching images or expanding authority.
func artworkPublicDescriptorAllowed(configuration config.Config, descriptor artwork.Descriptor) bool {
	if artwork.ValidateDescriptor(descriptor, configuration.ListingMedia.ArtworkLimits()) != nil {
		return false
	}
	deniedPrefixes, err := parseFederationPrefixes(configuration.Federation.Policy.AdditionalDeniedAddressRanges)
	if err != nil {
		return false
	}
	allowed := func(asset artwork.Asset) bool {
		if !catalog.ValidExternalHTTPSURL(asset.URL) || !fetcher.PublicReferenceAllowed(asset.URL, deniedPrefixes) {
			return false
		}
		binding, found := configuration.ListingMedia.OriginBinding(asset.URL)
		if !found || binding.NetworkProfile != "" {
			return false
		}
		for _, denied := range configuration.Federation.Policy.AdditionalDeniedOrigins {
			origin, err := fetcher.NormalizeOrigin(denied, false)
			if err != nil || origin == binding.Origin {
				return false
			}
		}
		switch configuration.Federation.Policy.Public.Mode {
		case fetcher.PublicAnyPublicHTTPS:
			return true
		case fetcher.PublicAllowlist:
			for _, approved := range configuration.Federation.Policy.Public.Origins {
				origin, err := fetcher.NormalizeOrigin(approved, false)
				if err == nil && origin == binding.Origin {
					return true
				}
			}
		case fetcher.PublicDisabled:
			return false
		}
		return false
	}
	if descriptor.Icon != nil && !allowed(*descriptor.Icon) {
		return false
	}
	for _, asset := range descriptor.Screenshots {
		if !allowed(asset) {
			return false
		}
	}
	return true
}

// Compile-time assertion keeps the narrow domain port independent of Router.
var _ artwork.Fetcher = artworkFetcher{}
