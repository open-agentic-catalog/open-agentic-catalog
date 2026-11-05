package catalog

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"
)

type fetchEvidencePort struct {
	policy   string
	result   ExternalRepresentationFetchResult
	err      error
	requests []ExternalRepresentationFetchRequest
}

func (p *fetchEvidencePort) PolicyDigest() string { return p.policy }

func (p *fetchEvidencePort) FetchExternalRepresentation(_ context.Context, request ExternalRepresentationFetchRequest) (ExternalRepresentationFetchResult, error) {
	p.requests = append(p.requests, request)
	return p.result, p.err
}

func TestExternalFetchChecksObservedBytesAndRequestPolicyBindings(t *testing.T) {
	t.Parallel()
	const url = "https://content.example.com/agent.json"
	policy := "sha256:" + strings.Repeat("a", 64)
	body := []byte(`{"name":"agent"}`)
	digest := fmt.Sprintf("sha256:%x", sha256.Sum256(body))
	at := time.Date(2026, time.September, 1, 12, 0, 0, 123456789, time.FixedZone("offset", 3600))
	valid := ExternalRepresentationFetchResult{
		RequestedURL: url, FinalURL: "https://content.example.com:443/current.json",
		Status: 200, MediaType: "application/json", Body: body, ContentDigest: digest,
		PolicyID: "production-policy", PolicyRevision: 2, PolicyDigest: policy, RetrievedAt: at,
	}
	for _, tc := range []struct {
		name   string
		mutate func(*ExternalRepresentationFetchResult)
		budget int64
	}{
		{name: "valid", budget: int64(len(body))},
		{name: "different requested path", budget: int64(len(body)), mutate: func(r *ExternalRepresentationFetchResult) { r.RequestedURL = "https://content.example.com/other.json" }},
		{name: "digest describes different bytes", budget: int64(len(body)), mutate: func(r *ExternalRepresentationFetchResult) { r.ContentDigest = "sha256:" + strings.Repeat("b", 64) }},
		{name: "exceeds caller budget", budget: int64(len(body)) - 1},
		{name: "different routing policy", budget: int64(len(body)), mutate: func(r *ExternalRepresentationFetchResult) { r.PolicyDigest = "sha256:" + strings.Repeat("b", 64) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			candidate := valid
			candidate.Body = bytes.Clone(body)
			if tc.mutate != nil {
				tc.mutate(&candidate)
			}
			port := &fetchEvidencePort{policy: policy, result: candidate}
			s := &Service{externalFetcher: port}
			result, err := s.fetchExternalRepresentation(t.Context(), guardrailTenantID, url, "application/json", tc.budget)
			wantRequest := ExternalRepresentationFetchRequest{TenantID: guardrailTenantID, URL: url, MediaType: "application/json", MaxResponseBytes: tc.budget}
			if !reflect.DeepEqual(port.requests, []ExternalRepresentationFetchRequest{wantRequest}) {
				t.Fatalf("outbound request = %+v; want %+v", port.requests, wantRequest)
			}
			if tc.name != "valid" {
				if ErrorCodeOf(err) != CodeDependencyUnavailable || !reflect.DeepEqual(result, ExternalRepresentationFetchResult{}) {
					t.Fatalf("unbound retrieval returned result=%+v error=%v", result, err)
				}
				return
			}
			want := valid
			want.RetrievedAt = time.Date(2026, time.September, 1, 11, 0, 0, 123456000, time.UTC)
			if err != nil || !reflect.DeepEqual(result, want) {
				t.Fatalf("valid retrieval = %+v, %v; want %+v", result, err, want)
			}
			port.result.Body[0] = '!'
			if !bytes.Equal(result.Body, body) {
				t.Fatal("returned validation bytes alias outbound port memory")
			}
			result.Body[1] = '!'
			if port.result.Body[1] != body[1] {
				t.Fatal("caller can mutate outbound port memory")
			}
		})
	}
}

func TestExternalFetchRequiresPolicyIdentityBeforeCallingPort(t *testing.T) {
	t.Parallel()
	for _, policy := range []string{"", "sha256:invalid"} {
		t.Run(policy, func(t *testing.T) {
			port := &fetchEvidencePort{policy: policy}
			s := &Service{externalFetcher: port}
			result, err := s.fetchExternalRepresentation(t.Context(), guardrailTenantID, "https://content.example.com/agent.json", "application/json", 1024)
			if ErrorCodeOf(err) != CodeDependencyUnavailable || len(port.requests) != 0 || !reflect.DeepEqual(result, ExternalRepresentationFetchResult{}) {
				t.Fatalf("policy-less retrieval: result=%+v error=%v calls=%d", result, err, len(port.requests))
			}
		})
	}
}

func TestExternalFetchClassifiesPluginPolicyDenial(t *testing.T) {
	t.Parallel()
	denied := E(CodeValidationFailed, "private outbound policy diagnostic")
	for _, mediaType := range []string{PluginMediaZIP, "application/json"} {
		t.Run(mediaType, func(t *testing.T) {
			port := &fetchEvidencePort{policy: "sha256:" + strings.Repeat("a", 64), err: denied}
			s := &Service{externalFetcher: port}
			result, err := s.fetchExternalRepresentation(t.Context(), guardrailTenantID, "https://content.example.com/archive", mediaType, 1024)
			if len(port.requests) != 1 || !reflect.DeepEqual(result, ExternalRepresentationFetchResult{}) {
				t.Fatalf("failed fetch returned result=%+v calls=%d", result, len(port.requests))
			}
			if mediaType == PluginMediaZIP {
				var domainErr *DomainError
				if !errors.As(err, &domainErr) || domainErr.Code != CodeValidationFailed || domainErr.Message != "Plugin archive URL is invalid or denied by outbound policy" || errors.Is(err, denied) {
					t.Fatalf("Plugin admission denial = %v", err)
				}
			} else if ErrorCodeOf(err) != CodeDependencyUnavailable || !errors.Is(err, denied) {
				t.Fatalf("document dependency denial = %v", err)
			}
		})
	}
}
