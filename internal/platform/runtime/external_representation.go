// External representation retrieval binds domain inspection to the configured
// egress policy while preserving exact Plugin archive bytes and fetch evidence.

package runtime

import (
	"context"
	"errors"
	"net/http"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/fetcher"
)

type externalRepresentationOutbound interface {
	Fetch(context.Context, fetcher.Request) (fetcher.Result, error)
	PolicyDigest() string
}

type externalRepresentationFetcher struct {
	outbound externalRepresentationOutbound
}

// PolicyDigest identifies the active egress policy, or returns empty when unwired.
func (f externalRepresentationFetcher) PolicyDigest() string {
	if f.outbound == nil {
		return ""
	}
	return f.outbound.PolicyDigest()
}

// FetchExternalRepresentation performs a bounded same-origin retrieval and
// copies response bytes into domain-owned evidence. Plugin URL denials become
// validation failures; other outbound errors retain their original classification.
func (f externalRepresentationFetcher) FetchExternalRepresentation(ctx context.Context, request catalog.ExternalRepresentationFetchRequest) (catalog.ExternalRepresentationFetchResult, error) {
	if f.outbound == nil {
		return catalog.ExternalRepresentationFetchResult{}, errors.New("external representation outbound fetcher is unavailable")
	}
	result, err := f.outbound.Fetch(ctx, fetcher.Request{
		URL:                  request.URL,
		IdentityEncoding:     catalog.IsPluginMediaType(request.MediaType),
		Method:               http.MethodGet,
		Accept:               []string{request.MediaType},
		RejectOriginChanges:  true,
		TenantID:             request.TenantID,
		ExpectedPolicyDigest: f.outbound.PolicyDigest(),
		MaxResponseBytes:     request.MaxResponseBytes,
	})
	if err != nil {
		if catalog.IsPluginMediaType(request.MediaType) && (errors.Is(err, fetcher.ErrInvalidTarget) || errors.Is(err, fetcher.ErrPolicyDenied)) {
			return catalog.ExternalRepresentationFetchResult{}, catalog.E(catalog.CodeValidationFailed, "Plugin archive URL is invalid or denied by outbound policy")
		}
		return catalog.ExternalRepresentationFetchResult{}, err
	}
	return catalog.ExternalRepresentationFetchResult{
		RequestedURL:   result.RequestedURL,
		FinalURL:       result.FinalURL,
		Status:         result.Status,
		MediaType:      result.MediaType,
		Body:           append([]byte(nil), result.Body...),
		ContentDigest:  result.BodyDigest,
		PolicyID:       result.PolicyID,
		PolicyRevision: result.PolicyRevision,
		PolicyDigest:   result.PolicyDigest,
		RetrievedAt:    result.FetchedAt,
	}, nil
}
