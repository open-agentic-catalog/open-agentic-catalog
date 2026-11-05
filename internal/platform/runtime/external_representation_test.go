package runtime

import (
	"context"
	"errors"
	"net/http"
	"reflect"
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/fetcher"
)

type recordingExternalOutbound struct {
	request fetcher.Request
	result  fetcher.Result
	err     error
	digest  string
}

func (f *recordingExternalOutbound) PolicyDigest() string { return f.digest }

func (f *recordingExternalOutbound) Fetch(_ context.Context, request fetcher.Request) (fetcher.Result, error) {
	f.request = request
	return f.result, f.err
}

func TestExternalRepresentationFetcherUsesAnonymousOriginBoundPublicRoute(t *testing.T) {
	t.Parallel()

	retrievedAt := time.Date(2026, time.September, 1, 12, 0, 0, 0, time.UTC)
	outbound := &recordingExternalOutbound{
		digest: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		result: fetcher.Result{RequestedURL: "https://content.example.com/card.json", FinalURL: "https://content.example.com/current/card.json", Status: http.StatusOK, MediaType: "application/json", Body: []byte(`{"name":"agent"}`), BodyDigest: "sha256:body", PolicyID: "policy", PolicyRevision: 2, PolicyDigest: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", FetchedAt: retrievedAt},
	}
	adapter := externalRepresentationFetcher{outbound: outbound}
	result, err := adapter.FetchExternalRepresentation(context.Background(), catalog.ExternalRepresentationFetchRequest{TenantID: "tenant", URL: "https://content.example.com/card.json", MediaType: "application/json", MaxResponseBytes: 4096})
	if err != nil {
		t.Fatal(err)
	}
	if outbound.request.Method != http.MethodGet || outbound.request.URL != "https://content.example.com/card.json" || !reflect.DeepEqual(outbound.request.Accept, []string{"application/json"}) || !outbound.request.RejectOriginChanges || outbound.request.RejectRedirects || outbound.request.TenantID != "tenant" || outbound.request.NetworkProfile != "" || outbound.request.CredentialBinding != "" || outbound.request.ExpectedPolicyDigest != outbound.digest || outbound.request.MaxResponseBytes != 4096 {
		t.Fatalf("outbound request = %#v", outbound.request)
	}
	if result.RequestedURL != outbound.result.RequestedURL || result.FinalURL != outbound.result.FinalURL || result.ContentDigest != outbound.result.BodyDigest || result.PolicyDigest != outbound.digest || !reflect.DeepEqual(result.Body, outbound.result.Body) || !result.RetrievedAt.Equal(retrievedAt) || adapter.PolicyDigest() != outbound.digest {
		t.Fatalf("mapped result = %#v", result)
	}
	result.Body[0] = 'X'
	if outbound.result.Body[0] == 'X' {
		t.Fatal("mapped result aliased fetcher response bytes")
	}
}

func TestExternalRepresentationFetcherPropagatesFailureAndRejectsMissingOutbound(t *testing.T) {
	t.Parallel()

	want := errors.New("routing denied")
	outbound := &recordingExternalOutbound{digest: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", err: want}
	if _, err := (externalRepresentationFetcher{outbound: outbound}).FetchExternalRepresentation(context.Background(), catalog.ExternalRepresentationFetchRequest{}); !errors.Is(err, want) {
		t.Fatalf("outbound failure = %v", err)
	}
	missing := externalRepresentationFetcher{}
	if missing.PolicyDigest() != "" {
		t.Fatal("missing outbound reported a policy digest")
	}
	if _, err := missing.FetchExternalRepresentation(context.Background(), catalog.ExternalRepresentationFetchRequest{}); err == nil {
		t.Fatal("missing outbound was accepted")
	}
}

func TestPluginTransportUsesExactArchiveBytesWithoutPrivateAuthority(t *testing.T) {
	for _, media := range []string{catalog.PluginMediaZIP, catalog.PluginMediaGZIP, "application/json"} {
		t.Run(media, func(t *testing.T) {
			outbound := &recordingExternalOutbound{digest: "policy"}
			_, err := (externalRepresentationFetcher{outbound: outbound}).FetchExternalRepresentation(t.Context(), catalog.ExternalRepresentationFetchRequest{TenantID: "tenant", URL: "https://packages.example.com/release", MediaType: media, MaxResponseBytes: 1024})
			if err != nil {
				t.Fatal(err)
			}
			r := outbound.request
			if r.IdentityEncoding != catalog.IsPluginMediaType(media) || !r.RejectOriginChanges || r.NetworkProfile != "" || r.CredentialBinding != "" || r.MaxResponseBytes != 1024 {
				t.Fatalf("unexpected archive authority or encoding: %+v", r)
			}
		})
	}
}

func TestPluginTransportClassifiesPolicyDenialWithoutLeakingTarget(t *testing.T) {
	for _, tc := range []struct {
		name    string
		cause   error
		invalid bool
	}{
		{"policy", fetcher.ErrPolicyDenied, true},
		{"target", fetcher.ErrInvalidTarget, true},
		{"outage", errors.New("temporary failure"), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			outbound := &recordingExternalOutbound{err: tc.cause}
			_, err := (externalRepresentationFetcher{outbound: outbound}).FetchExternalRepresentation(t.Context(), catalog.ExternalRepresentationFetchRequest{MediaType: catalog.PluginMediaZIP})
			if tc.invalid && catalog.ErrorCodeOf(err) != catalog.CodeValidationFailed || !tc.invalid && !errors.Is(err, tc.cause) {
				t.Fatalf("wrong error classification: %v", err)
			}
		})
	}
}
