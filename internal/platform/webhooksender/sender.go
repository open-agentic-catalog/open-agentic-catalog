// Package webhooksender sends webhooks through the shared hardened fetch boundary.
// It adapts signed delivery requests to HTTPS; the webhook domain retains
// responsibility for signing, claim ownership, retry decisions, and evidence.
package webhooksender

import (
	"context"
	"errors"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/fetcher"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/webhook"
)

type outboundFetcher interface {
	Fetch(context.Context, fetcher.Request) (fetcher.Result, error)
}

// Sender performs one delivery attempt using operator-selected egress rules.
// It retains no response body or delivery state.
type Sender struct {
	fetcher         outboundFetcher
	privateProfiles map[string]string
}

// New wraps an already configured outbound boundary and rejects a nil fetcher.
func New(outbound *fetcher.Fetcher) (*Sender, error) {
	if outbound == nil {
		return nil, errors.New("webhook fetcher is required")
	}
	return &Sender{fetcher: outbound}, nil
}

// NewWithPolicy uses an independently configured webhook egress policy. Route
// selection is determined solely by the operator's exact tenant/origin pairs;
// clients cannot select a profile, credential binding, or trust root.
func NewWithPolicy(policy fetcher.RoutingPolicy) (*Sender, error) {
	if policy.AllowHTTPForTests {
		return nil, errors.New("webhook routing requires HTTPS")
	}
	router, err := fetcher.NewRouter(policy, nil)
	if err != nil {
		return nil, err
	}
	profiles := make(map[string]string)
	for _, route := range policy.PrivateRoutes {
		for _, rawOrigin := range route.Origins {
			origin, err := fetcher.NormalizeOrigin(rawOrigin, false)
			if err != nil {
				return nil, err
			}
			for _, tenant := range route.AssignedTenantIDs {
				key := tenant + "\x00" + origin
				if _, exists := profiles[key]; exists {
					return nil, errors.New("webhook private routes have ambiguous tenant/origin assignments")
				}
				profiles[key] = route.Name
			}
		}
	}
	return &Sender{fetcher: router, privateProfiles: profiles}, nil
}

// Send posts the supplied CloudEvent bytes once and rejects redirects. A remote
// HTTP status is returned for domain retry classification; transport and policy
// failures return an error. Only status and Retry-After leave this adapter.
func (s *Sender) Send(ctx context.Context, request webhook.SendRequest) (webhook.SendResult, error) {
	profile := ""
	if len(s.privateProfiles) > 0 {
		origin, err := fetcher.NormalizeOrigin(request.URL, false)
		if err != nil {
			return webhook.SendResult{}, err
		}
		profile = s.privateProfiles[request.TenantID+"\x00"+origin]
	}
	result, err := s.fetcher.Fetch(ctx, fetcher.Request{TenantID: request.TenantID, NetworkProfile: profile, URL: request.URL, Method: "POST", Body: request.Body, ContentType: "application/cloudevents+json", Accept: []string{"application/json"}, Headers: request.Headers, RejectRedirects: true})
	if err != nil {
		return webhook.SendResult{}, err
	}
	return webhook.SendResult{Status: result.Status, RetryAfter: result.RetryAfter}, nil
}
