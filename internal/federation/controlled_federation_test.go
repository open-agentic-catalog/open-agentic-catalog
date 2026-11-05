package federation

import (
	"context"
	"errors"
	"fmt"
	"github.com/open-agentic-catalog/open-agentic-catalog/test/identityfixture"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/clock"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/fetcher"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/id"
)

type controlledFetcher struct {
	mu       sync.Mutex
	digest   string
	results  map[string]fetcher.Result
	requests []fetcher.Request
}

type deadlineFetcher struct {
	mu       sync.Mutex
	digest   string
	requests int
}

func (f *deadlineFetcher) PolicyDigest() string { return f.digest }

func (f *deadlineFetcher) Fetch(ctx context.Context, _ fetcher.Request) (fetcher.Result, error) {
	f.mu.Lock()
	f.requests++
	f.mu.Unlock()
	<-ctx.Done()
	return fetcher.Result{}, ctx.Err()
}

func (f *controlledFetcher) PolicyDigest() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.digest
}

func (f *controlledFetcher) Fetch(_ context.Context, request fetcher.Request) (fetcher.Result, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests = append(f.requests, request)
	method := request.Method
	if method == "" {
		method = http.MethodGet
	}
	result, exists := f.results[method+" "+request.URL]
	if !exists {
		return fetcher.Result{}, errors.New("unconfigured controlled fetch")
	}
	if result.PolicyID == "" {
		result.PolicyID = "test-policy"
	}
	if result.PolicyRevision == 0 {
		result.PolicyRevision = 1
	}
	if result.PolicyDigest == "" {
		result.PolicyDigest = f.digest
	}
	return result, nil
}

func TestInspectionBindsConnectivityAndCurrentPolicyEvidence(t *testing.T) {
	const (
		tenantID = "00000000-0000-7000-8000-000000000001"
		endpoint = "https://registry.remote.example/ard/v1/search"
		policyID = "enterprise"
		profile  = "corporate-catalogs"
		binding  = "partner-reader"
	)
	digest := "sha256:" + strings.Repeat("a", 64)
	body := `{"results":[]}`
	outbound := &controlledFetcher{digest: digest, results: map[string]fetcher.Result{
		http.MethodGet + " " + endpoint:  {Status: http.StatusMethodNotAllowed, FinalURL: endpoint, MediaType: "application/json", PolicyID: policyID, PolicyRevision: 4, PolicyDigest: digest, CredentialRevision: "secret-7"},
		http.MethodPost + " " + endpoint: {Status: http.StatusOK, FinalURL: endpoint, MediaType: "application/json", Body: []byte(body), PolicyID: policyID, PolicyRevision: 4, PolicyDigest: digest, CredentialRevision: "secret-7"},
	}}
	manager, err := NewManager(NewMemoryStore(), outbound, clock.NewFake(time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC)), id.Generator{}, WithDiscoveryTargets([]DiscoveryTarget{{Host: "catalog.internal.example", TenantID: tenantID, Audience: AudienceAuthenticated}}))
	if err != nil {
		t.Fatal(err)
	}
	ctx := identityfixture.WithActor(context.Background(), identity.Actor{TenantID: tenantID, Subject: "source-admin", Roles: []identity.Role{identity.RoleSourceAdmin}})
	inspection, err := manager.InspectWithAccess(ctx, InspectionInput{URL: endpoint, NetworkProfile: profile, CredentialBinding: binding})
	if err != nil {
		t.Fatal(err)
	}
	if inspection.PolicyID != policyID || inspection.PolicyRevision != 4 || inspection.PolicyDigest != digest || inspection.CredentialRevision != "secret-7" || inspection.NetworkProfile != profile || inspection.CredentialBinding != binding {
		t.Fatalf("inspection evidence = %#v", inspection)
	}
	for _, request := range outbound.requests {
		if request.TenantID != tenantID || request.NetworkProfile != profile || request.CredentialBinding != binding || request.ExpectedPolicyDigest != "" {
			t.Fatalf("inspection request was not bound to selected access: %#v", request)
		}
	}
	outbound.mu.Lock()
	outbound.digest = "sha256:" + strings.Repeat("b", 64)
	outbound.mu.Unlock()
	_, err = manager.Connect(ctx, ConnectInput{InspectionID: inspection.ID, DisplayName: "Remote", RetrievalMode: RetrievalLive, Scope: Scope{Publishers: []string{"remote.example"}}, UpdateMode: UpdateManual, Live: LivePolicy{Enabled: true, QueryPrivacyAcknowledged: true}, DiscoveryAssignments: []DiscoveryAssignment{{Host: "catalog.internal.example", Audience: AudienceAuthenticated}}, NetworkProfile: profile, CredentialBinding: binding, Reason: "Approve the inspected source"})
	if catalog.ErrorCodeOf(err) != catalog.CodePreconditionFailed {
		t.Fatalf("stale inspection policy returned %v", err)
	}
	outbound.mu.Lock()
	outbound.digest = digest
	outbound.mu.Unlock()
	source, err := manager.Connect(ctx, ConnectInput{InspectionID: inspection.ID, DisplayName: "Remote", RetrievalMode: RetrievalLive, Scope: Scope{Publishers: []string{"remote.example"}}, UpdateMode: UpdateManual, Live: LivePolicy{Enabled: true, QueryPrivacyAcknowledged: true}, DiscoveryAssignments: []DiscoveryAssignment{{Host: "catalog.internal.example", Audience: AudienceAuthenticated}}, NetworkProfile: profile, CredentialBinding: binding, Reason: "Approve the inspected source"})
	if err != nil {
		t.Fatal(err)
	}
	if source.PolicyDigest != digest || source.CredentialRevision != "secret-7" || source.RetrievalMode != RetrievalLive {
		t.Fatalf("connected source evidence = %#v", source)
	}
}

func TestCredentialedInspectionCannotTransferBindingToDiscoveredOrigin(t *testing.T) {
	const tenantID = "00000000-0000-7000-8000-000000000001"
	const seed = "https://seed.remote.example"
	const discovered = "https://catalog.remote.example/.well-known/ard.json"
	digest := "sha256:" + strings.Repeat("a", 64)
	outbound := &controlledFetcher{digest: digest, results: map[string]fetcher.Result{
		http.MethodGet + " " + seed:                           {Status: http.StatusOK, FinalURL: seed, MediaType: "text/html", Body: []byte(`<a>Catalog</a>`), Links: []string{`<` + discovered + `>; rel="ard"`}, CredentialRevision: "secret-7"},
		http.MethodGet + " " + seed + "/.well-known/ard.json": {Status: http.StatusNotFound, FinalURL: seed + "/.well-known/ard.json", MediaType: "application/json", CredentialRevision: "secret-7"},
		http.MethodGet + " " + discovered:                     {Status: http.StatusOK, FinalURL: discovered, MediaType: "application/ard+json", Body: []byte(`{"entries":[]}`)},
	}}
	manager, err := NewManager(NewMemoryStore(), outbound, clock.NewFake(time.Now()), id.Generator{})
	if err != nil {
		t.Fatal(err)
	}
	ctx := identityfixture.WithActor(context.Background(), identity.Actor{TenantID: tenantID, Subject: "source-admin", Roles: []identity.Role{identity.RoleSourceAdmin}})
	_, err = manager.InspectWithAccess(ctx, InspectionInput{URL: seed, CredentialBinding: "partner-reader"})
	if catalog.ErrorCodeOf(err) != catalog.CodeSourcePolicyDenied {
		t.Fatalf("credential binding transferred to discovered origin: %v", err)
	}
	if len(outbound.requests) < 2 || outbound.requests[0].CredentialBinding == "" || outbound.requests[len(outbound.requests)-1].URL != discovered || outbound.requests[len(outbound.requests)-1].CredentialBinding != "" {
		t.Fatalf("credentialed discovery requests = %#v", outbound.requests)
	}
}

func TestControlledReferralTraversalContactsOnlyApprovedObservedSources(t *testing.T) {
	for _, extensions := range []string{"none", "metadata", "case-colliding"} {
		for _, mode := range []string{"approved", "paused", "stale policy", "missing", "different audience", "not observed"} {
			t.Run(extensions+"/"+mode, func(t *testing.T) {
				const tenantID = "00000000-0000-7000-8000-000000000001"
				const host = "catalog.example.com"
				const (
					rootID       = "00000000-0000-7000-8000-000000000010"
					approvedID   = "00000000-0000-7000-8000-000000000011"
					unapprovedID = "00000000-0000-7000-8000-000000000012"
				)
				rootURL := "https://root.registry.example/search"
				approvedURL := "https://approved.registry.example/search"
				unapprovedURL := "https://unapproved.registry.example/search"
				rootResponse := `{"results":[],"referrals":[{"identifier":"urn:air:approved.registry.example:registry:main","displayName":"Approved","type":"application/ai-registry+json","url":"https://approved.registry.example:443/search"},{"identifier":"urn:air:unknown.registry.example:registry:main","displayName":"Unknown","type":"application/ai-registry+json","url":"https://unknown.registry.example/search"},{"identifier":"urn:air:unapproved.registry.example:registry:main","displayName":"Unapproved","type":"application/ai-registry+json","url":"` + unapprovedURL + `"}]}`
				rootEntry := `{"identifier":"urn:air:root.example:default:agent","type":"application/a2a-agent-card+json","score":50,"source":"https://root.registry.example/"}`
				rootResponse = strings.Replace(rootResponse, `"results":[]`, `"results":[`+rootEntry+`]`, 1)
				if extensions == "metadata" {
					rootResponse = strings.ReplaceAll(rootResponse, `"displayName":`, `"description":"Descriptive metadata","extension":{"url":"https://unapproved.registry.example/search"},"displayName":`)
				}
				if extensions == "case-colliding" {
					rootResponse = strings.ReplaceAll(rootResponse, `/search"}`, `/search","URL":"https://approved.registry.example/search"}`)
				}
				approvedResponse := `{"results":[{"identifier":"urn:air:approved.example:default:agent","version":"1.0.0","displayName":"Approved agent","type":"application/a2a-agent-card+json","url":"https://agents.approved.example/card.json","score":90,"source":"https://approved.registry.example/"}],"referrals":[{"identifier":"urn:air:root.registry.example:registry:main","displayName":"Root","type":"application/ai-registry+json","url":"` + rootURL + `"},{"identifier":"urn:air:unapproved.registry.example:registry:main","displayName":"Beyond root depth","type":"application/ai-registry+json","url":"` + unapprovedURL + `"}]}`
				digest := "sha256:" + strings.Repeat("c", 64)
				outbound := &controlledFetcher{digest: digest, results: map[string]fetcher.Result{
					http.MethodPost + " " + rootURL:       {Status: http.StatusOK, FinalURL: rootURL, MediaType: "application/json", Body: []byte(rootResponse), PolicyDigest: digest},
					http.MethodPost + " " + approvedURL:   {Status: http.StatusOK, FinalURL: approvedURL, MediaType: "application/json", Body: []byte(approvedResponse), PolicyDigest: digest},
					http.MethodPost + " " + unapprovedURL: {Status: http.StatusOK, FinalURL: unapprovedURL, MediaType: "application/json", Body: []byte(`{"results":[]}`), PolicyDigest: digest},
				}}
				store := NewMemoryStore()
				now := time.Date(2026, 8, 31, 13, 0, 0, 0, time.UTC)
				assignment := []DiscoveryAssignment{{Host: host, Audience: AudienceAnonymous}}
				sources := []Source{
					{ID: rootID, TenantID: tenantID, DisplayName: "Root", Type: SourceRegistry, Endpoint: rootURL, RegistrySearchURL: rootURL, Status: SourceActive, ProfileID: "ard-0.91-aa3e598", RetrievalMode: RetrievalLive, Scope: Scope{Kinds: []catalog.Kind{catalog.KindAgent}}, UpdateMode: UpdateManual, Live: LivePolicy{Enabled: true, QueryPrivacyAcknowledged: true, AnonymousDisclosureAcknowledged: true, FederationMode: "referrals"}, Referrals: ReferralPolicy{Enabled: true, MaxHops: 1, ApprovedSourceIDs: []string{approvedID}, AnonymousDisclosureAcknowledged: true}, DiscoveryAssignments: assignment, PolicyID: "test", PolicyRevision: 1, PolicyDigest: digest, Revision: 1, CreatedBy: "admin", CreatedAt: now, UpdatedAt: now},
					{ID: approvedID, TenantID: tenantID, DisplayName: "Approved", Type: SourceRegistry, Endpoint: approvedURL, RegistrySearchURL: approvedURL, Status: SourceActive, ProfileID: "ard-0.91-aa3e598", RetrievalMode: RetrievalLive, Scope: Scope{Kinds: []catalog.Kind{catalog.KindAgent}}, UpdateMode: UpdateManual, Live: LivePolicy{Enabled: true, ReferralOnly: true, QueryPrivacyAcknowledged: true, AnonymousDisclosureAcknowledged: true}, Referrals: ReferralPolicy{Enabled: true, MaxHops: 2, ApprovedSourceIDs: []string{unapprovedID}, AnonymousDisclosureAcknowledged: true}, DiscoveryAssignments: assignment, PolicyID: "test", PolicyRevision: 1, PolicyDigest: digest, Revision: 1, CreatedBy: "admin", CreatedAt: now, UpdatedAt: now},
					{ID: unapprovedID, TenantID: tenantID, DisplayName: "Unapproved", Type: SourceRegistry, Endpoint: unapprovedURL, RegistrySearchURL: unapprovedURL, Status: SourceActive, ProfileID: "ard-0.91-aa3e598", RetrievalMode: RetrievalLive, Scope: Scope{Kinds: []catalog.Kind{catalog.KindAgent}}, UpdateMode: UpdateManual, Live: LivePolicy{Enabled: true, ReferralOnly: true, QueryPrivacyAcknowledged: true, AnonymousDisclosureAcknowledged: true}, DiscoveryAssignments: assignment, PolicyID: "test", PolicyRevision: 1, PolicyDigest: digest, Revision: 1, CreatedBy: "admin", CreatedAt: now, UpdatedAt: now},
				}
				for _, source := range sources {
					if _, err := store.CreateSource(context.Background(), source); err != nil {
						t.Fatal(err)
					}
				}
				switch mode {
				case "paused":
					candidate := store.sources[approvedID]
					candidate.Status = SourcePaused
					store.sources[approvedID] = candidate
				case "stale policy":
					candidate := store.sources[approvedID]
					candidate.PolicyDigest = "sha256:" + strings.Repeat("d", 64)
					store.sources[approvedID] = candidate
				case "missing":
					delete(store.sources, approvedID)
				case "different audience":
					candidate := store.sources[approvedID]
					candidate.DiscoveryAssignments = []DiscoveryAssignment{{Host: host, Audience: AudienceAuthenticated}}
					store.sources[approvedID] = candidate
				case "not observed":
					result := outbound.results[http.MethodPost+" "+rootURL]
					result.Body = []byte(strings.ReplaceAll(rootResponse, "https://approved.registry.example:443/search", "https://approved.registry.example:443/other"))
					outbound.results[http.MethodPost+" "+rootURL] = result
				}
				manager, err := NewManager(store, outbound, clock.NewFake(now), id.Generator{}, WithExecutionLimits(4, 2), WithDiscoveryTargets([]DiscoveryTarget{{Host: host, TenantID: tenantID, Audience: AudienceAnonymous}}))
				if err != nil {
					t.Fatal(err)
				}
				ctx := identityfixture.WithActor(context.Background(), identity.Actor{TenantID: tenantID, Subject: "discovery", Roles: []identity.Role{identity.RoleSourceAdmin}})
				response, err := manager.LiveSearch(ctx, LiveSearchRequest{Text: "approved agent", PageSize: 10, DiscoveryHost: host, Audience: AudienceAnonymous})
				for _, request := range outbound.requests {
					if request.ExpectedPolicyDigest != digest || request.TenantID != tenantID || request.MaxResponseBytes != MaxLiveResponseBytes {
						t.Fatalf("referral fetch lost current policy or tenant binding: %#v", request)
					}
				}
				if len(store.observations) != 0 || len(store.snapshots) != 0 {
					t.Fatal("live discovery retained or adopted external content")
				}
				if mode != "approved" {
					if err != nil || response.Partial || len(response.Results) != 1 || response.Results[0].Identifier != "urn:air:root.example:default:agent" || len(outbound.requests) != 1 || outbound.requests[0].URL != rootURL {
						t.Fatalf("ineligible referral contacted: response=%+v requests=%+v error=%v", response, outbound.requests, err)
					}
					return
				}
				if err != nil || response.Partial || len(response.Results) != 2 || response.Results[0].Identifier != "urn:air:approved.example:default:agent" || response.Results[1].Identifier != "urn:air:root.example:default:agent" {
					t.Fatalf("controlled referral response = %#v, %v", response, err)
				}
				if len(outbound.requests) != 2 || outbound.requests[0].URL != rootURL || outbound.requests[1].URL != approvedURL {
					t.Fatalf("contacted topology = %#v", outbound.requests)
				}
				if !strings.Contains(string(outbound.requests[0].Body), `"federation":"referrals"`) || !strings.Contains(string(outbound.requests[1].Body), `"federation":"none"`) {
					t.Fatalf("root hop budget was not preserved in upstream requests: %#v", outbound.requests)
				}
			})
		}
	}
}

func TestLiveFederationUsesOneEndToEndDeadline(t *testing.T) {
	const tenantID = "00000000-0000-7000-8000-000000000001"
	const host = "catalog.example.com"
	digest := "sha256:" + strings.Repeat("9", 64)
	outbound := &deadlineFetcher{digest: digest}
	store := NewMemoryStore()
	now := time.Date(2026, 8, 31, 13, 30, 0, 0, time.UTC)
	assignment := []DiscoveryAssignment{{Host: host, Audience: AudienceAnonymous}}
	for index, sourceID := range []string{"00000000-0000-7000-8000-000000000040", "00000000-0000-7000-8000-000000000041"} {
		source := Source{ID: sourceID, TenantID: tenantID, DisplayName: fmt.Sprintf("Remote %d", index), Type: SourceRegistry, Endpoint: fmt.Sprintf("https://remote-%d.example/search", index), RegistrySearchURL: fmt.Sprintf("https://remote-%d.example/search", index), Status: SourceActive, ProfileID: "ard-0.91-aa3e598", RetrievalMode: RetrievalLive, Scope: Scope{Kinds: []catalog.Kind{catalog.KindAgent}}, UpdateMode: UpdateManual, Live: LivePolicy{Enabled: true, QueryPrivacyAcknowledged: true, AnonymousDisclosureAcknowledged: true}, DiscoveryAssignments: assignment, PolicyID: "test-policy", PolicyRevision: 1, PolicyDigest: digest, Revision: 1, CreatedBy: "admin", CreatedAt: now, UpdatedAt: now}
		if _, err := store.CreateSource(context.Background(), source); err != nil {
			t.Fatal(err)
		}
	}
	manager, err := NewManager(store, outbound, clock.NewFake(now), id.Generator{}, WithExecutionLimits(2, 1), WithLiveSearchTimeout(25*time.Millisecond), WithDiscoveryTargets([]DiscoveryTarget{{Host: host, TenantID: tenantID, Audience: AudienceAnonymous}}))
	if err != nil {
		t.Fatal(err)
	}
	ctx := identityfixture.WithActor(context.Background(), identity.Actor{TenantID: tenantID, Subject: "discovery", Roles: []identity.Role{identity.RoleSourceAdmin}})
	started := time.Now()
	response, err := manager.LiveSearch(ctx, LiveSearchRequest{Text: "agent", PageSize: 10, DiscoveryHost: host, Audience: AudienceAnonymous})
	if err != nil || !response.Partial || len(response.Diagnostics) != 1 || response.Diagnostics[0].Status != "failed" {
		t.Fatalf("deadline-bounded response = %#v, %v", response, err)
	}
	if elapsed := time.Since(started); elapsed >= time.Second {
		t.Fatalf("live federation exceeded its single request budget: %s", elapsed)
	}
	outbound.mu.Lock()
	requests := outbound.requests
	outbound.mu.Unlock()
	if requests != 1 {
		t.Fatalf("deadline expiry contacted %d sources, want 1", requests)
	}
}

func TestLiveFederationContactsCanonicalRegistryEndpointOnce(t *testing.T) {
	const tenantID = "00000000-0000-7000-8000-000000000001"
	const host = "catalog.example.com"
	digest := "sha256:" + strings.Repeat("4", 64)
	endpoint := "https://duplicate.registry.example/search"
	outbound := &controlledFetcher{digest: digest, results: map[string]fetcher.Result{
		http.MethodPost + " " + endpoint: {Status: http.StatusOK, FinalURL: endpoint, MediaType: "application/json", Body: []byte(`{"results":[]}`), PolicyDigest: digest},
	}}
	store := NewMemoryStore()
	now := time.Date(2026, 8, 31, 13, 40, 0, 0, time.UTC)
	assignment := []DiscoveryAssignment{{Host: host, Audience: AudienceAnonymous}}
	for index, candidate := range []string{endpoint, "https://DUPLICATE.REGISTRY.EXAMPLE:443/search"} {
		source := Source{ID: fmt.Sprintf("00000000-0000-7000-8000-%012d", index+60), TenantID: tenantID, DisplayName: fmt.Sprintf("Registry %d", index), Type: SourceRegistry, Endpoint: candidate, RegistrySearchURL: candidate, Status: SourceActive, ProfileID: "ard-0.91-aa3e598", RetrievalMode: RetrievalLive, Scope: Scope{Kinds: []catalog.Kind{catalog.KindAgent}}, UpdateMode: UpdateManual, Live: LivePolicy{Enabled: true, QueryPrivacyAcknowledged: true, AnonymousDisclosureAcknowledged: true}, DiscoveryAssignments: assignment, PolicyID: "test-policy", PolicyRevision: 1, PolicyDigest: digest, Revision: 1, CreatedBy: "admin", CreatedAt: now, UpdatedAt: now}
		// Populate legacy-equivalent rows directly so the request-time defense is
		// exercised even if storage predates canonical endpoint validation.
		store.sources[source.ID] = source
	}
	manager, err := NewManager(store, outbound, clock.NewFake(now), id.Generator{}, WithExecutionLimits(4, 2), WithDiscoveryTargets([]DiscoveryTarget{{Host: host, TenantID: tenantID, Audience: AudienceAnonymous}}))
	if err != nil {
		t.Fatal(err)
	}
	duplicate := store.sources["00000000-0000-7000-8000-000000000061"]
	if _, err := manager.normalizeAndValidateSource(context.Background(), duplicate); catalog.ErrorCodeOf(err) != catalog.CodeConflict {
		t.Fatalf("canonical duplicate source configuration returned %v", err)
	}
	ctx := identityfixture.WithActor(context.Background(), identity.Actor{TenantID: tenantID, Subject: "discovery", Roles: []identity.Role{identity.RoleSourceAdmin}})
	response, err := manager.LiveSearch(ctx, LiveSearchRequest{Text: "agent", DiscoveryHost: host, Audience: AudienceAnonymous})
	if err != nil || !response.Partial || len(response.Diagnostics) != 2 {
		t.Fatalf("duplicate endpoint response = %#v, %v", response, err)
	}
	limited := false
	for _, diagnostic := range response.Diagnostics {
		limited = limited || diagnostic.Status == "fanout_limited"
	}
	if !limited {
		t.Fatalf("duplicate endpoint diagnostic = %#v", response.Diagnostics)
	}
	if len(outbound.requests) != 1 || outbound.requests[0].URL != endpoint {
		t.Fatalf("canonical endpoint requests = %#v", outbound.requests)
	}
}

func TestReferralListingIsDeterministicallyBounded(t *testing.T) {
	const tenantID = "00000000-0000-7000-8000-000000000001"
	const host = "catalog.example.com"
	digest := "sha256:" + strings.Repeat("2", 64)
	store := NewMemoryStore()
	now := time.Date(2026, 8, 31, 13, 42, 0, 0, time.UTC)
	for index, name := range []string{"Alpha", "Beta", "Gamma"} {
		endpoint := fmt.Sprintf("https://registry-%d.example/search", index)
		source := Source{ID: fmt.Sprintf("00000000-0000-7000-8000-%012d", index+70), TenantID: tenantID, DisplayName: name, Type: SourceRegistry, Endpoint: endpoint, RegistrySearchURL: endpoint, Status: SourceActive, ProfileID: "ard-0.91-aa3e598", RetrievalMode: RetrievalLive, Scope: Scope{Kinds: []catalog.Kind{catalog.KindAgent}}, UpdateMode: UpdateManual, Live: LivePolicy{Enabled: true, QueryPrivacyAcknowledged: true, AnonymousDisclosureAcknowledged: true}, Referrals: ReferralPolicy{Enabled: true, MaxHops: 1, AnonymousDisclosureAcknowledged: true}, DiscoveryAssignments: []DiscoveryAssignment{{Host: host, Audience: AudienceAnonymous}}, PolicyID: "test-policy", PolicyRevision: 1, PolicyDigest: digest, Revision: 1, CreatedBy: "admin", CreatedAt: now, UpdatedAt: now}
		if _, err := store.CreateSource(context.Background(), source); err != nil {
			t.Fatal(err)
		}
	}
	manager, err := NewManager(store, &controlledFetcher{digest: digest, results: map[string]fetcher.Result{}}, clock.NewFake(now), id.Generator{}, WithExecutionLimits(2, 1), WithDiscoveryTargets([]DiscoveryTarget{{Host: host, TenantID: tenantID, Audience: AudienceAnonymous}}))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := manager.Referrals(context.Background(), host, AudienceAnonymous); catalog.ErrorCodeOf(err) != catalog.CodeForbidden {
		t.Fatalf("unauthorized referral listing returned %v", err)
	}
	store.sources["00000000-0000-7000-8000-000000000079"] = Source{ID: "00000000-0000-7000-8000-000000000079", TenantID: tenantID, DisplayName: "Ignored", Status: SourcePaused, RegistrySearchURL: "https://ignored.example/search", Referrals: ReferralPolicy{Enabled: true}, DiscoveryAssignments: []DiscoveryAssignment{{Host: host, Audience: AudienceAnonymous}}, PolicyID: "test-policy", PolicyRevision: 1, PolicyDigest: digest}
	ctx := identityfixture.WithActor(context.Background(), identity.Actor{TenantID: tenantID, Subject: "discovery", Roles: []identity.Role{identity.RoleSourceAdmin}})
	referrals, partial, err := manager.Referrals(ctx, host, AudienceAnonymous)
	if err != nil || !partial || len(referrals) != 2 || referrals[0].DisplayName != "Alpha" || referrals[1].DisplayName != "Beta" {
		t.Fatalf("bounded referrals = %#v, partial=%t, %v", referrals, partial, err)
	}
}

func TestLiveFederationContactsAdvertisedRegistryIdentifierOnce(t *testing.T) {
	const tenantID = "00000000-0000-7000-8000-000000000001"
	const host = "catalog.example.com"
	const rootID = "00000000-0000-7000-8000-000000000080"
	const firstID = "00000000-0000-7000-8000-000000000081"
	const secondID = "00000000-0000-7000-8000-000000000082"
	rootURL := "https://root.registry.example/search"
	firstURL := "https://first.registry.example/search"
	secondURL := "https://second.registry.example/search"
	registryID := "urn:air:shared.registry.example:registry:main"
	rootResponse := `{"results":[],"referrals":[{"identifier":"` + registryID + `","displayName":"First","type":"application/ai-registry+json","url":"` + firstURL + `"},{"identifier":"` + registryID + `","displayName":"Second","type":"application/ai-registry+json","url":"` + secondURL + `"}]}`
	digest := "sha256:" + strings.Repeat("3", 64)
	outbound := &controlledFetcher{digest: digest, results: map[string]fetcher.Result{
		http.MethodPost + " " + rootURL:  {Status: http.StatusOK, FinalURL: rootURL, MediaType: "application/json", Body: []byte(rootResponse), PolicyDigest: digest},
		http.MethodPost + " " + firstURL: {Status: http.StatusOK, FinalURL: firstURL, MediaType: "application/json", Body: []byte(`{"results":[]}`), PolicyDigest: digest},
	}}
	store := NewMemoryStore()
	now := time.Date(2026, 8, 31, 13, 44, 0, 0, time.UTC)
	assignment := []DiscoveryAssignment{{Host: host, Audience: AudienceAnonymous}}
	sources := []Source{
		{ID: rootID, TenantID: tenantID, DisplayName: "Root", Type: SourceRegistry, Endpoint: rootURL, RegistrySearchURL: rootURL, Status: SourceActive, ProfileID: "ard-0.91-aa3e598", RetrievalMode: RetrievalLive, Scope: Scope{Kinds: []catalog.Kind{catalog.KindAgent}}, UpdateMode: UpdateManual, Live: LivePolicy{Enabled: true, QueryPrivacyAcknowledged: true, AnonymousDisclosureAcknowledged: true, FederationMode: "referrals"}, Referrals: ReferralPolicy{Enabled: true, MaxHops: 1, ApprovedSourceIDs: []string{firstID, secondID}, AnonymousDisclosureAcknowledged: true}, DiscoveryAssignments: assignment, PolicyID: "test-policy", PolicyRevision: 1, PolicyDigest: digest, Revision: 1, CreatedBy: "admin", CreatedAt: now, UpdatedAt: now},
		{ID: firstID, TenantID: tenantID, DisplayName: "First", Type: SourceRegistry, Endpoint: firstURL, RegistrySearchURL: firstURL, Status: SourceActive, ProfileID: "ard-0.91-aa3e598", RetrievalMode: RetrievalLive, Scope: Scope{Kinds: []catalog.Kind{catalog.KindAgent}}, UpdateMode: UpdateManual, Live: LivePolicy{Enabled: true, ReferralOnly: true, QueryPrivacyAcknowledged: true, AnonymousDisclosureAcknowledged: true}, DiscoveryAssignments: assignment, PolicyID: "test-policy", PolicyRevision: 1, PolicyDigest: digest, Revision: 1, CreatedBy: "admin", CreatedAt: now, UpdatedAt: now},
		{ID: secondID, TenantID: tenantID, DisplayName: "Second", Type: SourceRegistry, Endpoint: secondURL, RegistrySearchURL: secondURL, Status: SourceActive, ProfileID: "ard-0.91-aa3e598", RetrievalMode: RetrievalLive, Scope: Scope{Kinds: []catalog.Kind{catalog.KindAgent}}, UpdateMode: UpdateManual, Live: LivePolicy{Enabled: true, ReferralOnly: true, QueryPrivacyAcknowledged: true, AnonymousDisclosureAcknowledged: true}, DiscoveryAssignments: assignment, PolicyID: "test-policy", PolicyRevision: 1, PolicyDigest: digest, Revision: 1, CreatedBy: "admin", CreatedAt: now, UpdatedAt: now},
	}
	for _, source := range sources {
		if _, err := store.CreateSource(context.Background(), source); err != nil {
			t.Fatal(err)
		}
	}
	manager, err := NewManager(store, outbound, clock.NewFake(now), id.Generator{}, WithExecutionLimits(4, 2), WithDiscoveryTargets([]DiscoveryTarget{{Host: host, TenantID: tenantID, Audience: AudienceAnonymous}}))
	if err != nil {
		t.Fatal(err)
	}
	ctx := identityfixture.WithActor(context.Background(), identity.Actor{TenantID: tenantID, Subject: "discovery", Roles: []identity.Role{identity.RoleSourceAdmin}})
	response, err := manager.LiveSearch(ctx, LiveSearchRequest{Text: "agent", DiscoveryHost: host, Audience: AudienceAnonymous})
	if err != nil || !response.Partial {
		t.Fatalf("duplicate Registry identifier response = %#v, %v", response, err)
	}
	if len(outbound.requests) != 2 || outbound.requests[0].URL != rootURL || outbound.requests[1].URL != firstURL {
		t.Fatalf("duplicate Registry identifier requests = %#v", outbound.requests)
	}
}

func TestTopologyRejectsUnknownAudienceAndPrivateAnonymousSource(t *testing.T) {
	const tenantID = "00000000-0000-7000-8000-000000000001"
	manager, err := NewManager(NewMemoryStore(), &controlledFetcher{digest: "sha256:" + strings.Repeat("0", 64), results: map[string]fetcher.Result{}}, clock.NewFake(time.Now()), id.Generator{}, WithDiscoveryTargets([]DiscoveryTarget{{Host: "catalog.example.com", TenantID: tenantID, Audience: AudienceAnonymous}}))
	if err != nil {
		t.Fatal(err)
	}
	ctx := identityfixture.WithActor(context.Background(), identity.Actor{TenantID: tenantID, Subject: "source-admin", Roles: []identity.Role{identity.RoleSourceAdmin}})
	if _, err := manager.LiveSearch(ctx, LiveSearchRequest{Text: "agent"}); catalog.ErrorCodeOf(err) != catalog.CodeInvalidArgument {
		t.Fatalf("live search without an explicit administrative or discovery boundary returned %v", err)
	}
	for name, sourceIDs := range map[string][]string{
		"duplicate":  {"00000000-0000-7000-8000-000000000010", "00000000-0000-7000-8000-000000000010"},
		"empty":      {""},
		"whitespace": {" 00000000-0000-7000-8000-000000000010"},
	} {
		t.Run(name+" administrative source selection", func(t *testing.T) {
			if _, err := manager.LiveSearch(ctx, LiveSearchRequest{Text: "agent", Administrative: true, SourceIDs: sourceIDs}); catalog.ErrorCodeOf(err) != catalog.CodeInvalidArgument {
				t.Fatalf("invalid administrative source selection returned %v", err)
			}
		})
	}
	base := Source{ID: "00000000-0000-7000-8000-000000000010", TenantID: tenantID, DisplayName: "Private", Type: SourceRegistry, Endpoint: "https://registry.example/search", RegistrySearchURL: "https://registry.example/search", ProfileID: "ard-0.91-aa3e598", RetrievalMode: RetrievalLive, Scope: Scope{Publishers: []string{"example.com"}}, UpdateMode: UpdateManual, Live: LivePolicy{Enabled: true, QueryPrivacyAcknowledged: true, AnonymousDisclosureAcknowledged: true}, NetworkProfile: "private", DiscoveryAssignments: []DiscoveryAssignment{{Host: "catalog.example.com", Audience: AudienceAnonymous}}}
	if _, err := manager.normalizeAndValidateSource(context.Background(), base); catalog.ErrorCodeOf(err) != catalog.CodeSourcePolicyDenied {
		t.Fatalf("private anonymous source returned %v", err)
	}
	base.NetworkProfile = ""
	base.Live.AnonymousDisclosureAcknowledged = false
	if _, err := manager.normalizeAndValidateSource(context.Background(), base); catalog.ErrorCodeOf(err) != catalog.CodeSourcePolicyDenied {
		t.Fatalf("unacknowledged anonymous source disclosure returned %v", err)
	}
	base.Live.AnonymousDisclosureAcknowledged = true
	base.DiscoveryAssignments[0].Host = "unknown.example.com"
	if _, err := manager.normalizeAndValidateSource(context.Background(), base); catalog.ErrorCodeOf(err) != catalog.CodeSourcePolicyDenied {
		t.Fatalf("unknown discovery host returned %v", err)
	}
	base.DiscoveryAssignments[0].Host = "catalog.example.com"
	base.Referrals = ReferralPolicy{Enabled: true, MaxHops: 1, ApprovedSourceIDs: []string{"missing", "missing"}, AnonymousDisclosureAcknowledged: true}
	if _, err := manager.normalizeAndValidateSource(context.Background(), base); catalog.ErrorCodeOf(err) != catalog.CodeInvalidArgument {
		t.Fatalf("duplicate referral edge returned %v", err)
	}
	eligible := Source{DiscoveryAssignments: []DiscoveryAssignment{{Host: "catalog.example.com", Audience: AudienceAnonymous}, {Host: "catalog.internal.example", Audience: AudienceAuthenticated}}}
	if !sourceEligibleForDiscovery(eligible, " CATALOG.EXAMPLE.COM ", AudienceAnonymous) {
		t.Fatal("normalized anonymous discovery assignment was not eligible")
	}
	for name, mutate := range map[string]func(*Source){
		"private profile": func(source *Source) { source.NetworkProfile = "private" },
		"live disclosure": func(source *Source) { source.Live.Enabled = true },
		"referral disclosure": func(source *Source) {
			source.Referrals.Enabled = true
		},
	} {
		t.Run(name+" is ineligible for anonymous discovery", func(t *testing.T) {
			candidate := eligible
			mutate(&candidate)
			if sourceEligibleForDiscovery(candidate, "catalog.example.com", AudienceAnonymous) {
				t.Fatal("unacknowledged anonymous source was eligible")
			}
		})
	}
	if !sourceEligibleForDiscovery(eligible, "catalog.internal.example", AudienceAuthenticated) || sourceEligibleForDiscovery(eligible, "other.example", AudienceAuthenticated) {
		t.Fatal("authenticated discovery assignment matching was not exact")
	}
}

func TestControlledSourceParticipationPoliciesFailClosed(t *testing.T) {
	const tenantID = "00000000-0000-7000-8000-000000000001"
	const authenticatedHost = "catalog.internal.example"
	const anonymousHost = "catalog.example.com"
	manager, err := NewManager(NewMemoryStore(), &controlledFetcher{digest: federationTestPolicyDigest, results: map[string]fetcher.Result{}}, clock.NewFake(time.Now()), id.Generator{}, WithExecutionLimits(3, 2), WithDiscoveryTargets([]DiscoveryTarget{
		{Host: authenticatedHost, TenantID: tenantID, Audience: AudienceAuthenticated},
		{Host: anonymousHost, TenantID: tenantID, Audience: AudienceAnonymous},
	}))
	if err != nil {
		t.Fatal(err)
	}
	base := func() Source {
		return Source{
			ID:                "00000000-0000-7000-8000-000000000010",
			TenantID:          tenantID,
			DisplayName:       "Remote Registry",
			Type:              SourceHybrid,
			Endpoint:          "https://registry.example/.well-known/ard.json",
			RegistrySearchURL: "https://registry.example/ard/v1/search",
			ProfileID:         "ard-0.91-aa3e598",
			RetrievalMode:     RetrievalHybrid,
			Scope:             Scope{Kinds: []catalog.Kind{catalog.KindAgent}},
			UpdateMode:        UpdateManual,
			Live:              LivePolicy{Enabled: true, QueryPrivacyAcknowledged: true, FederationMode: "none"},
			DiscoveryAssignments: []DiscoveryAssignment{{
				Host:     authenticatedHost,
				Audience: AudienceAuthenticated,
			}},
		}
	}
	tests := []struct {
		name   string
		code   catalog.ErrorCode
		mutate func(*Source)
	}{
		{name: "indexed source enables live queries", code: catalog.CodeInvalidArgument, mutate: func(source *Source) { source.RetrievalMode = RetrievalIndexed }},
		{name: "hybrid source disables live queries", code: catalog.CodePreconditionFailed, mutate: func(source *Source) { source.Live.Enabled = false }},
		{name: "unknown retrieval mode", code: catalog.CodeInvalidArgument, mutate: func(source *Source) { source.RetrievalMode = "streaming" }},
		{name: "Registry-only source is indexed", code: catalog.CodePreconditionFailed, mutate: func(source *Source) {
			source.Type = SourceRegistry
			source.RetrievalMode = RetrievalIndexed
			source.Live = LivePolicy{}
		}},
		{name: "live query disclosure is unacknowledged", code: catalog.CodeSourcePolicyDenied, mutate: func(source *Source) { source.Live.QueryPrivacyAcknowledged = false }},
		{name: "referral-only source disables live queries", code: catalog.CodeInvalidArgument, mutate: func(source *Source) {
			source.RetrievalMode = RetrievalIndexed
			source.Live = LivePolicy{ReferralOnly: true}
		}},
		{name: "Registry search endpoint is invalid", code: catalog.CodeInvalidArgument, mutate: func(source *Source) { source.RegistrySearchURL = "not a URL" }},
		{name: "upstream auto mode is unbounded", code: catalog.CodeInvalidArgument, mutate: func(source *Source) { source.Live.FederationMode = "auto" }},
		{name: "referrals have no Registry endpoint", code: catalog.CodePreconditionFailed, mutate: func(source *Source) {
			source.RetrievalMode = RetrievalIndexed
			source.RegistrySearchURL = ""
			source.Live = LivePolicy{}
			source.Referrals = ReferralPolicy{Enabled: true, MaxHops: 1}
		}},
		{name: "referral depth exceeds deployment bound", code: catalog.CodeInvalidArgument, mutate: func(source *Source) { source.Referrals = ReferralPolicy{Enabled: true, MaxHops: 3} }},
		{name: "live source has no discovery assignment", code: catalog.CodeSourcePolicyDenied, mutate: func(source *Source) { source.DiscoveryAssignments = nil }},
		{name: "source has too many discovery assignments", code: catalog.CodeInvalidArgument, mutate: func(source *Source) {
			source.DiscoveryAssignments = make([]DiscoveryAssignment, 17)
			for index := range source.DiscoveryAssignments {
				source.DiscoveryAssignments[index] = DiscoveryAssignment{Host: authenticatedHost, Audience: AudienceAuthenticated}
			}
		}},
		{name: "source repeats a discovery assignment", code: catalog.CodeInvalidArgument, mutate: func(source *Source) {
			source.DiscoveryAssignments = append(source.DiscoveryAssignments, source.DiscoveryAssignments[0])
		}},
		{name: "anonymous referral disclosure is unacknowledged", code: catalog.CodeSourcePolicyDenied, mutate: func(source *Source) {
			source.Live.AnonymousDisclosureAcknowledged = true
			source.Referrals = ReferralPolicy{Enabled: true, MaxHops: 1}
			source.DiscoveryAssignments = []DiscoveryAssignment{{Host: anonymousHost, Audience: AudienceAnonymous}}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			source := base()
			test.mutate(&source)
			if _, err := manager.normalizeAndValidateSource(context.Background(), source); catalog.ErrorCodeOf(err) != test.code {
				t.Fatalf("invalid controlled source returned %v", err)
			}
		})
	}
}

func TestReferralTopologyRequiresAConnectedSourceInTheSameDiscoveryAssignment(t *testing.T) {
	const tenantID = "00000000-0000-7000-8000-000000000001"
	const candidateID = "00000000-0000-7000-8000-000000000011"
	const rootID = "00000000-0000-7000-8000-000000000010"
	store := NewMemoryStore()
	now := time.Date(2026, 8, 31, 13, 15, 0, 0, time.UTC)
	candidate := Source{ID: candidateID, TenantID: tenantID, DisplayName: "Candidate", Type: SourceRegistry, Endpoint: "https://candidate.example/search", RegistrySearchURL: "https://candidate.example/search", Status: SourceActive, ProfileID: "ard-0.91-aa3e598", RetrievalMode: RetrievalLive, Scope: Scope{Kinds: []catalog.Kind{catalog.KindAgent}}, UpdateMode: UpdateManual, Live: LivePolicy{Enabled: true, QueryPrivacyAcknowledged: true}, DiscoveryAssignments: []DiscoveryAssignment{{Host: "catalog-a.example", Audience: AudienceAuthenticated}}, Revision: 1, CreatedBy: "admin", CreatedAt: now, UpdatedAt: now}
	if _, err := store.CreateSource(context.Background(), candidate); err != nil {
		t.Fatal(err)
	}
	manager, err := NewManager(store, &controlledFetcher{digest: federationTestPolicyDigest, results: map[string]fetcher.Result{}}, clock.NewFake(now), id.Generator{}, WithDiscoveryTargets([]DiscoveryTarget{
		{Host: "catalog-a.example", TenantID: tenantID, Audience: AudienceAuthenticated},
		{Host: "catalog-b.example", TenantID: tenantID, Audience: AudienceAuthenticated},
	}))
	if err != nil {
		t.Fatal(err)
	}
	root := Source{ID: rootID, TenantID: tenantID, DisplayName: "Root", Type: SourceRegistry, Endpoint: "https://root.example/search", RegistrySearchURL: "https://root.example/search", Status: SourceActive, ProfileID: "ard-0.91-aa3e598", RetrievalMode: RetrievalLive, Scope: Scope{Kinds: []catalog.Kind{catalog.KindAgent}}, UpdateMode: UpdateManual, Live: LivePolicy{Enabled: true, QueryPrivacyAcknowledged: true, FederationMode: "referrals"}, Referrals: ReferralPolicy{Enabled: true, MaxHops: 1, ApprovedSourceIDs: []string{candidateID}}, DiscoveryAssignments: []DiscoveryAssignment{{Host: "catalog-a.example", Audience: AudienceAuthenticated}}}
	if _, err := manager.normalizeAndValidateSource(context.Background(), root); err != nil {
		t.Fatalf("same-assignment referral topology: %v", err)
	}
	root.DiscoveryAssignments[0].Host = "catalog-b.example"
	if _, err := manager.normalizeAndValidateSource(context.Background(), root); catalog.ErrorCodeOf(err) != catalog.CodeSourcePolicyDenied {
		t.Fatalf("different-assignment referral topology returned %v", err)
	}
	root.DiscoveryAssignments[0].Host = "catalog-a.example"
	candidate.Referrals = ReferralPolicy{Enabled: true, MaxHops: 1, ApprovedSourceIDs: []string{rootID}}
	candidate.Live.FederationMode = "referrals"
	candidate.Revision++
	if _, err := store.UpdateSource(context.Background(), candidate, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.normalizeAndValidateSource(context.Background(), root); catalog.ErrorCodeOf(err) != catalog.CodeSourcePolicyDenied {
		t.Fatalf("cyclic referral topology returned %v", err)
	}
}

func TestStalePolicyBlocksSyncLiveContactAndResume(t *testing.T) {
	const tenantID = "00000000-0000-7000-8000-000000000001"
	const host = "catalog.example.com"
	activeDigest := "sha256:" + strings.Repeat("6", 64)
	store := NewMemoryStore()
	now := time.Date(2026, 8, 31, 13, 45, 0, 0, time.UTC)
	source := Source{ID: "00000000-0000-7000-8000-000000000050", TenantID: tenantID, DisplayName: "Stale", Type: SourceARDManifest, Endpoint: "https://stale.example/ard.json", RegistrySearchURL: "https://stale.example/search", Status: SourceActive, ProfileID: "ard-0.91-aa3e598", RetrievalMode: RetrievalHybrid, Scope: Scope{Kinds: []catalog.Kind{catalog.KindAgent}}, UpdateMode: UpdateManual, Live: LivePolicy{Enabled: true, QueryPrivacyAcknowledged: true, AnonymousDisclosureAcknowledged: true}, DiscoveryAssignments: []DiscoveryAssignment{{Host: host, Audience: AudienceAnonymous}}, PolicyID: "old-policy", PolicyRevision: 1, PolicyDigest: "sha256:" + strings.Repeat("5", 64), Revision: 1, CreatedBy: "admin", CreatedAt: now, UpdatedAt: now}
	if _, err := store.CreateSource(context.Background(), source); err != nil {
		t.Fatal(err)
	}
	outbound := &controlledFetcher{digest: activeDigest, results: map[string]fetcher.Result{}}
	manager, err := NewManager(store, outbound, clock.NewFake(now), id.Generator{}, WithDiscoveryTargets([]DiscoveryTarget{{Host: host, TenantID: tenantID, Audience: AudienceAnonymous}}))
	if err != nil {
		t.Fatal(err)
	}
	ctx := identityfixture.WithActor(context.Background(), identity.Actor{TenantID: tenantID, Subject: "source-admin", Roles: []identity.Role{identity.RoleSourceAdmin}})
	if _, err := manager.Sync(ctx, source.ID); catalog.ErrorCodeOf(err) != catalog.CodePreconditionFailed {
		t.Fatalf("stale-policy synchronization returned %v", err)
	}
	response, err := manager.LiveSearch(ctx, LiveSearchRequest{Text: "agent", DiscoveryHost: host, Audience: AudienceAnonymous})
	if err != nil || !response.Partial || len(response.Diagnostics) != 1 || response.Diagnostics[0].Status != "policy_stale" {
		t.Fatalf("stale-policy live search = %#v, %v", response, err)
	}
	paused, err := manager.SetStatus(ctx, source.ID, SourcePaused, source.Revision, StatusChangeInput{Reason: "Pause the stale source"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.SetStatus(ctx, source.ID, SourceActive, paused.Revision, StatusChangeInput{Reason: "Resume the source"}); catalog.ErrorCodeOf(err) != catalog.CodePreconditionFailed {
		t.Fatalf("stale-policy resume returned %v", err)
	}
	if len(outbound.requests) != 0 {
		t.Fatalf("stale policy contacted a source: %#v", outbound.requests)
	}
}

func TestCurrentPolicyRequiresCompleteCanonicalEvidence(t *testing.T) {
	digest := "sha256:" + strings.Repeat("6", 64)
	manager, err := NewManager(NewMemoryStore(), &controlledFetcher{digest: digest, results: map[string]fetcher.Result{}}, clock.NewFake(time.Now()), id.Generator{})
	if err != nil {
		t.Fatal(err)
	}
	source := Source{PolicyID: "current-policy", PolicyRevision: 1, PolicyDigest: digest}
	if !manager.sourceUsesCurrentPolicy(source) {
		t.Fatal("complete current policy evidence was rejected")
	}
	for name, mutate := range map[string]func(*Source){
		"missing policy ID":       func(candidate *Source) { candidate.PolicyID = "" },
		"invalid policy ID":       func(candidate *Source) { candidate.PolicyID = "Current Policy" },
		"missing policy revision": func(candidate *Source) { candidate.PolicyRevision = 0 },
		"malformed digest":        func(candidate *Source) { candidate.PolicyDigest = "sha256:not-a-digest" },
		"stale digest":            func(candidate *Source) { candidate.PolicyDigest = "sha256:" + strings.Repeat("5", 64) },
	} {
		t.Run(name, func(t *testing.T) {
			candidate := source
			mutate(&candidate)
			if manager.sourceUsesCurrentPolicy(candidate) {
				t.Fatal("partial or stale policy evidence was accepted")
			}
		})
	}
}

func TestLiveSearchBoundsStaleSourceDiagnostics(t *testing.T) {
	const tenantID = "00000000-0000-7000-8000-000000000001"
	const host = "catalog.example.com"
	activeDigest := "sha256:" + strings.Repeat("8", 64)
	store := NewMemoryStore()
	now := time.Date(2026, 8, 31, 14, 0, 0, 0, time.UTC)
	for index := 0; index < MaxLiveDiagnostics+8; index++ {
		sourceID := fmt.Sprintf("00000000-0000-7000-8000-%012d", index+100)
		endpoint := fmt.Sprintf("https://stale-%d.example/search", index)
		source := Source{ID: sourceID, TenantID: tenantID, DisplayName: fmt.Sprintf("Stale %d", index), Type: SourceRegistry, Endpoint: endpoint, RegistrySearchURL: endpoint, Status: SourceActive, ProfileID: "ard-0.91-aa3e598", RetrievalMode: RetrievalLive, Scope: Scope{Kinds: []catalog.Kind{catalog.KindAgent}}, UpdateMode: UpdateManual, Live: LivePolicy{Enabled: true, QueryPrivacyAcknowledged: true, AnonymousDisclosureAcknowledged: true}, DiscoveryAssignments: []DiscoveryAssignment{{Host: host, Audience: AudienceAnonymous}}, PolicyID: "old-policy", PolicyRevision: 1, PolicyDigest: "sha256:" + strings.Repeat("7", 64), Revision: 1, CreatedBy: "admin", CreatedAt: now, UpdatedAt: now}
		if _, err := store.CreateSource(context.Background(), source); err != nil {
			t.Fatal(err)
		}
	}
	outbound := &controlledFetcher{digest: activeDigest, results: map[string]fetcher.Result{}}
	manager, err := NewManager(store, outbound, clock.NewFake(now), id.Generator{}, WithDiscoveryTargets([]DiscoveryTarget{{Host: host, TenantID: tenantID, Audience: AudienceAnonymous}}))
	if err != nil {
		t.Fatal(err)
	}
	ctx := identityfixture.WithActor(context.Background(), identity.Actor{TenantID: tenantID, Subject: "discovery", Roles: []identity.Role{identity.RoleSourceAdmin}})
	response, err := manager.LiveSearch(ctx, LiveSearchRequest{Text: "agent", DiscoveryHost: host, Audience: AudienceAnonymous})
	if err != nil {
		t.Fatal(err)
	}
	if !response.Partial || len(response.Diagnostics) != 4 || response.Diagnostics[0].Status != "fanout_limited" || response.Diagnostics[3].Status != "policy_stale" {
		t.Fatalf("bounded diagnostics = %#v", response.Diagnostics)
	}
	if len(outbound.requests) != 0 {
		t.Fatalf("stale sources were contacted: %#v", outbound.requests)
	}
}

func TestReferralObservationRequiresExactCanonicalEndpoint(t *testing.T) {
	if identifier, observed := observedReferralIdentifier([]liveReferral{{Identifier: "urn:air:registry.example:registry:main", DisplayName: "Registry", Type: "application/ai-registry", URL: "https://registry.example:443/"}}, "https://registry.example"); !observed || identifier != "urn:air:registry.example:registry:main" {
		t.Fatal("canonical root Registry endpoint was not observed")
	}
	for name, referral := range map[string]liveReferral{
		"missing identifier": {DisplayName: "Registry", Type: "application/ai-registry+json", URL: "https://registry.example"},
		"missing display":    {Identifier: "urn:air:registry.example:registry:main", Type: "application/ai-registry+json", URL: "https://registry.example"},
		"different path":     {Identifier: "urn:air:registry.example:registry:main", DisplayName: "Registry", Type: "application/ai-registry+json", URL: "https://registry.example/other"},
		"different query":    {Identifier: "urn:air:registry.example:registry:main", DisplayName: "Registry", Type: "application/ai-registry+json", URL: "https://registry.example?scope=other"},
		"different port":     {Identifier: "urn:air:registry.example:registry:main", DisplayName: "Registry", Type: "application/ai-registry+json", URL: "https://registry.example:8443"},
		"wrong type":         {Identifier: "urn:air:registry.example:registry:main", DisplayName: "Registry", Type: "application/json", URL: "https://registry.example"},
	} {
		t.Run(name, func(t *testing.T) {
			if identifier, observed := observedReferralIdentifier([]liveReferral{referral}, "https://registry.example"); observed || identifier != "" {
				t.Fatal("non-exact Registry referral was accepted")
			}
		})
	}
	if key, ok := canonicalEndpoint("https://REGISTRY.EXAMPLE:0443/search?kind=agent"); !ok || key != "https://registry.example:443/search?kind=agent" {
		t.Fatalf("canonical endpoint = %q, %t", key, ok)
	}
	for _, invalid := range []string{"%", "https://user@registry.example/search", "https://registry.example/search#fragment", "https://registry.example:0/search"} {
		if _, ok := canonicalEndpoint(invalid); ok {
			t.Fatalf("invalid canonical endpoint %q was accepted", invalid)
		}
	}
}

func TestConnectivityChangeRequiresMatchingCurrentInspection(t *testing.T) {
	const tenantID = "00000000-0000-7000-8000-000000000001"
	now := time.Date(2026, 8, 31, 14, 0, 0, 0, time.UTC)
	digest := "sha256:" + strings.Repeat("d", 64)
	store := NewMemoryStore()
	source := Source{ID: "00000000-0000-7000-8000-000000000020", TenantID: tenantID, DisplayName: "Remote", Type: SourceARDManifest, Endpoint: "https://remote.example/ard.json", Status: SourceActive, ProfileID: "ard-0.91-aa3e598", RetrievalMode: RetrievalIndexed, Scope: Scope{Kinds: []catalog.Kind{catalog.KindAgent}}, UpdateMode: UpdateManual, PolicyID: "old-policy", PolicyRevision: 1, PolicyDigest: "sha256:" + strings.Repeat("a", 64), Revision: 1, CreatedBy: "admin", CreatedAt: now, UpdatedAt: now}
	if _, err := store.CreateSource(context.Background(), source); err != nil {
		t.Fatal(err)
	}
	// #nosec G101 -- credential binding and revision are non-secret synthetic identifiers.
	inspection := Inspection{ID: "00000000-0000-7000-8000-000000000021", TenantID: tenantID, RequestedURL: source.Endpoint, ResolvedURL: source.Endpoint, SourceType: source.Type, ProfileID: source.ProfileID, MediaType: "application/json", BodyDigest: "sha256:" + strings.Repeat("b", 64), KindCounts: map[string]int{}, Capabilities: map[string]bool{}, NetworkProfile: "private-catalogs", CredentialBinding: "partner-reader", PolicyID: "new-policy", PolicyRevision: 2, PolicyDigest: digest, CredentialRevision: "credential-2", ExpiresAt: now.Add(time.Hour), CreatedAt: now}
	if _, err := store.SaveInspection(context.Background(), inspection); err != nil {
		t.Fatal(err)
	}
	manager, err := NewManager(store, &controlledFetcher{digest: digest, results: map[string]fetcher.Result{}}, clock.NewFake(now), id.Generator{})
	if err != nil {
		t.Fatal(err)
	}
	ctx := identityfixture.WithActor(context.Background(), identity.Actor{TenantID: tenantID, Subject: "source-admin", Roles: []identity.Role{identity.RoleSourceAdmin}})
	profile := inspection.NetworkProfile
	binding := inspection.CredentialBinding
	withoutInspection := PatchInput{Intent: ChangeUpdate, NetworkProfile: &profile, CredentialBinding: &binding}
	if _, err := manager.PreviewChange(ctx, source.ID, withoutInspection); catalog.ErrorCodeOf(err) != catalog.CodePreconditionFailed {
		t.Fatalf("uninspected connectivity change returned %v", err)
	}
	if _, err := manager.PreviewChange(ctx, source.ID, PatchInput{Intent: ChangeUpdate, InspectionID: inspection.ID}); catalog.ErrorCodeOf(err) != catalog.CodePreconditionFailed {
		t.Fatalf("implicit connectivity change returned %v", err)
	}
	if _, err := manager.PreviewChange(ctx, source.ID, PatchInput{Intent: ChangeUpdate, InspectionID: inspection.ID, NetworkProfile: &profile}); catalog.ErrorCodeOf(err) != catalog.CodePreconditionFailed {
		t.Fatalf("implicit credential binding change returned %v", err)
	}
	patch := PatchInput{Intent: ChangeUpdate, InspectionID: inspection.ID, NetworkProfile: &profile, CredentialBinding: &binding}
	preview, err := manager.PreviewChange(ctx, source.ID, patch)
	if err != nil {
		t.Fatal(err)
	}
	patch.ChangePreviewDigest = preview.Digest
	patch.Reason = "Apply the inspected connectivity change"
	updated, err := manager.UpdateSource(ctx, source.ID, patch, source.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Revision != 2 || updated.NetworkProfile != profile || updated.CredentialBinding != binding || updated.PolicyID != inspection.PolicyID || updated.PolicyRevision != inspection.PolicyRevision || updated.PolicyDigest != digest || updated.CredentialRevision != inspection.CredentialRevision {
		t.Fatalf("updated connectivity evidence = %#v", updated)
	}
}

func TestSynchronizationRecordsTheCredentialRevisionActuallyUsed(t *testing.T) {
	const tenantID = "00000000-0000-7000-8000-000000000001"
	endpoint := "https://catalog.remote.example/ard.json"
	digest := "sha256:" + strings.Repeat("e", 64)
	document := `{"entries":[{"identifier":"urn:air:remote.example:default:agent","displayName":"Remote","type":"application/a2a-agent-card+json","data":{"name":"Remote"}}]}`
	outbound := &controlledFetcher{digest: digest, results: map[string]fetcher.Result{
		http.MethodGet + " " + endpoint: {Status: http.StatusOK, FinalURL: endpoint, MediaType: "application/ard+json", Body: []byte(document), BodyDigest: "sha256:" + strings.Repeat("f", 64), PolicyID: "enterprise", PolicyRevision: 1, PolicyDigest: digest, CredentialRevision: "rotated-2"},
	}}
	store := NewMemoryStore()
	now := time.Date(2026, 8, 31, 15, 0, 0, 0, time.UTC)
	source := Source{ID: "00000000-0000-7000-8000-000000000030", TenantID: tenantID, DisplayName: "Credentialed", Type: SourceARDManifest, Endpoint: endpoint, Status: SourceActive, ProfileID: "ard-0.91-aa3e598", RetrievalMode: RetrievalIndexed, Scope: Scope{Kinds: []catalog.Kind{catalog.KindAgent}}, UpdateMode: UpdateManual, CredentialBinding: "partner-reader", PolicyID: "enterprise", PolicyRevision: 1, PolicyDigest: digest, CredentialRevision: "tested-1", Revision: 1, CreatedBy: "admin", CreatedAt: now, UpdatedAt: now}
	if _, err := store.CreateSource(context.Background(), source); err != nil {
		t.Fatal(err)
	}
	manager, err := NewManager(store, outbound, clock.NewFake(now), id.Generator{})
	if err != nil {
		t.Fatal(err)
	}
	ctx := identityfixture.WithActor(context.Background(), identity.Actor{TenantID: tenantID, Subject: "source-admin", Roles: []identity.Role{identity.RoleSourceAdmin}})
	snapshot, err := manager.Sync(ctx, source.ID)
	if err != nil || snapshot.CredentialRevision != "rotated-2" || snapshot.PolicyDigest != digest {
		t.Fatalf("credential-aware synchronization = %#v, %v", snapshot, err)
	}
}

func TestEveryFederationFetchRequiresCurrentConsistentEvidence(t *testing.T) {
	const tenantID = "00000000-0000-7000-8000-000000000001"
	endpoint := "https://catalog.remote.example/ard.json"
	manager, err := NewManager(NewMemoryStore(), &sequenceFetcher{results: []fetcher.Result{
		{Status: http.StatusOK, PolicyDigest: federationTestPolicyDigest, CredentialRevision: "version-one"},
		{Status: http.StatusOK, PolicyDigest: federationTestPolicyDigest, CredentialRevision: "version-two"},
	}}, clock.NewFake(time.Now()), id.Generator{})
	if err != nil {
		t.Fatal(err)
	}
	access := newSourceAccess("00000000-0000-7000-8000-000000000001", "", "partner-reader", endpoint)
	if _, err := manager.fetchWithAccess(context.Background(), access, endpoint, fetcher.Request{}); err != nil {
		t.Fatalf("first credentialed fetch: %v", err)
	}
	if _, err := manager.fetchWithAccess(context.Background(), access, endpoint, fetcher.Request{}); err == nil || !strings.Contains(err.Error(), "changed during one federation operation") {
		t.Fatalf("mid-operation credential rotation returned %v", err)
	}

	manager, err = NewManager(NewMemoryStore(), &sequenceFetcher{results: []fetcher.Result{{Status: http.StatusOK, PolicyDigest: "sha256:" + strings.Repeat("b", 64)}}}, clock.NewFake(time.Now()), id.Generator{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.fetchWithAccess(context.Background(), newSourceAccess("tenant", "", "", endpoint), endpoint, fetcher.Request{}); catalog.ErrorCodeOf(err) != catalog.CodeDependencyUnavailable {
		t.Fatalf("stale per-fetch policy evidence returned %v", err)
	}

	manager, err = NewManager(NewMemoryStore(), &sequenceFetcher{results: []fetcher.Result{{Status: http.StatusOK, PolicyDigest: federationTestPolicyDigest, CredentialRevision: " version-one "}}}, clock.NewFake(time.Now()), id.Generator{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.fetchWithAccess(context.Background(), newSourceAccess("tenant", "", "partner-reader", endpoint), endpoint, fetcher.Request{}); catalog.ErrorCodeOf(err) != catalog.CodeDependencyUnavailable {
		t.Fatalf("non-canonical credential revision evidence returned %v", err)
	}

	contextURL := "https://catalog.remote.example/context.jsonld"
	document := `{"entries":[{"@context":"` + contextURL + `","identifier":"urn:air:remote.example:default:agent","displayName":"Remote","type":"application/a2a-agent-card+json","data":{"name":"Remote"}}]}`
	manager, err = NewManager(NewMemoryStore(), &sequenceFetcher{results: []fetcher.Result{
		{Status: http.StatusOK, FinalURL: endpoint, MediaType: "application/ard+json", Body: []byte(document), CredentialRevision: "version-one"},
		{Status: http.StatusOK, FinalURL: contextURL, MediaType: "application/ld+json", Body: []byte(`{"@context":{}}`), CredentialRevision: "version-two"},
	}}, clock.NewFake(time.Now()), id.Generator{})
	if err != nil {
		t.Fatal(err)
	}
	ctx := identityfixture.WithActor(context.Background(), identity.Actor{TenantID: tenantID, Subject: "source-admin", Roles: []identity.Role{identity.RoleSourceAdmin}})
	if _, err := manager.InspectWithAccess(ctx, InspectionInput{URL: endpoint, CredentialBinding: "partner-reader"}); err == nil || !strings.Contains(err.Error(), "changed during one federation operation") {
		t.Fatalf("credential rotation hidden by optional context parsing returned %v", err)
	}
}

func TestLiveResponseDecodingEnforcesResultReferralAndTokenBounds(t *testing.T) {
	// #nosec G101 -- the page-token values are parser fixtures, not credentials.
	tests := map[string]string{
		"results":       `{"results":[` + strings.Repeat(`{},`, MaxLiveSearchPageSize) + `{}` + `]}`,
		"referrals":     `{"results":[],"referrals":[` + strings.Repeat(`{},`, 64) + `{}` + `]}`,
		"page token":    `{"results":[],"pageToken":"` + strings.Repeat("x", catalog.MaxCatalogCursorBytes+1) + `"}`,
		"control token": `{"results":[],"pageToken":"\u0085"}`,
	}
	for name, body := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := decodeLiveResponse([]byte(body)); err == nil {
				t.Fatal("over-limit live response was accepted")
			}
		})
	}
}

func TestSourceTestCannotApproveAChangedInterface(t *testing.T) {
	const tenantID = "00000000-0000-7000-8000-000000000001"
	endpoint := "https://catalog.remote.example/catalog.json"
	digest := "sha256:" + strings.Repeat("7", 64)
	outbound := &controlledFetcher{digest: digest, results: map[string]fetcher.Result{
		http.MethodGet + " " + endpoint: {Status: http.StatusOK, FinalURL: endpoint, MediaType: "application/ai-catalog+json", Body: []byte(`{"specVersion":"1.0","entries":[]}`), PolicyDigest: digest},
	}}
	store := NewMemoryStore()
	now := time.Date(2026, 8, 31, 16, 0, 0, 0, time.UTC)
	source := Source{ID: "00000000-0000-7000-8000-000000000060", TenantID: tenantID, DisplayName: "Remote", Type: SourceARDManifest, Endpoint: endpoint, Status: SourceActive, ProfileID: "ard-0.91-aa3e598", RetrievalMode: RetrievalIndexed, Scope: Scope{Kinds: []catalog.Kind{catalog.KindAgent}}, UpdateMode: UpdateManual, Live: LivePolicy{FederationMode: "none"}, PolicyID: "older-policy", PolicyRevision: 1, PolicyDigest: "sha256:" + strings.Repeat("6", 64), Revision: 1, CreatedBy: "admin", CreatedAt: now, UpdatedAt: now}
	if _, err := store.CreateSource(context.Background(), source); err != nil {
		t.Fatal(err)
	}
	manager, err := NewManager(store, outbound, clock.NewFake(now), id.Generator{})
	if err != nil {
		t.Fatal(err)
	}
	ctx := identityfixture.WithActor(context.Background(), identity.Actor{TenantID: tenantID, Subject: "source-admin", Roles: []identity.Role{identity.RoleSourceAdmin}})
	result, err := manager.TestSourceAtRevision(ctx, source.ID, source.Revision)
	if err != nil || result.Status != "failed" || !strings.Contains(result.SafeDiagnostic, "interface changed") {
		t.Fatalf("changed source interface test = %#v, %v", result, err)
	}
	stored, err := store.GetSource(context.Background(), tenantID, source.ID)
	if err != nil || stored.Revision != source.Revision || stored.PolicyDigest != source.PolicyDigest {
		t.Fatalf("changed interface updated source evidence: %#v, %v", stored, err)
	}
}

func TestSynchronizationRejectsAChangedSourceProfile(t *testing.T) {
	const tenantID = "00000000-0000-7000-8000-000000000001"
	endpoint := "https://catalog.remote.example/catalog.json"
	result := federationResultAt(endpoint, `{"specVersion":"1.0","entries":[]}`)
	result.MediaType = aiCatalogDocumentMediaType
	store := NewMemoryStore()
	now := time.Date(2026, 9, 2, 10, 0, 0, 0, time.UTC)
	source := Source{ID: "00000000-0000-7000-8000-000000000061", TenantID: tenantID, DisplayName: "Remote", Endpoint: endpoint, Type: SourceARDManifest, Status: SourceActive, ProfileID: "ard-0.91-aa3e598", RetrievalMode: RetrievalIndexed, Scope: Scope{Kinds: []catalog.Kind{catalog.KindAgent}}, UpdateMode: UpdateManual, PolicyID: "test-policy", PolicyRevision: 1, PolicyDigest: federationTestPolicyDigest, Revision: 1, CreatedBy: "source-admin", CreatedAt: now, UpdatedAt: now}
	if _, err := store.CreateSource(context.Background(), source); err != nil {
		t.Fatal(err)
	}
	manager, err := NewManager(store, &routingFetcher{results: map[string]fetcher.Result{"GET " + endpoint: result}}, clock.NewFake(now), id.Generator{})
	if err != nil {
		t.Fatal(err)
	}
	ctx := identityfixture.WithActor(context.Background(), identity.Actor{TenantID: tenantID, Subject: "source-admin", Roles: []identity.Role{identity.RoleSourceAdmin}})
	snapshot, err := manager.Sync(ctx, source.ID)
	if catalog.ErrorCodeOf(err) != catalog.CodePreconditionFailed || snapshot.State != SnapshotFailed || snapshot.EnumerationComplete || snapshot.MemberCount != 0 {
		t.Fatalf("changed source profile synchronization = snapshot %#v, %v", snapshot, err)
	}
	stored, err := store.GetSource(context.Background(), tenantID, source.ID)
	if err != nil || stored.LastCompleteSnapshotID != "" || stored.Revision != source.Revision {
		t.Fatalf("profile drift changed approved source membership: %#v, %v", stored, err)
	}
	if observations, err := store.ListObservationPage(context.Background(), tenantID, ObservationListRequest{SourceID: source.ID, Limit: 100}); err != nil || len(observations) != 0 {
		t.Fatalf("profile drift created observations: %#v, %v", observations, err)
	}
}

func TestSourceTestCannotSilentlyNormalizeLegacyConfiguration(t *testing.T) {
	const tenantID = "00000000-0000-7000-8000-000000000001"
	endpoint := "https://legacy.remote.example/search"
	now := time.Date(2026, 8, 31, 16, 30, 0, 0, time.UTC)
	digest := "sha256:" + strings.Repeat("4", 64)
	store := NewMemoryStore()
	source := Source{ID: "00000000-0000-7000-8000-000000000090", TenantID: tenantID, DisplayName: "Legacy", Type: SourceRegistry, Endpoint: endpoint, RegistrySearchURL: endpoint, Status: SourcePaused, ProfileID: "ard-0.91-aa3e598", RetrievalMode: RetrievalLive, Scope: Scope{Kinds: []catalog.Kind{catalog.KindAgent}}, UpdateMode: UpdateManual, Live: LivePolicy{Enabled: true, QueryPrivacyAcknowledged: true}, DiscoveryAssignments: []DiscoveryAssignment{{Host: "catalog.example.com", Audience: AudienceAuthenticated}}, PolicyID: "old-policy", PolicyRevision: 1, PolicyDigest: "sha256:" + strings.Repeat("3", 64), Revision: 1, CreatedBy: "admin", CreatedAt: now, UpdatedAt: now}
	if _, err := store.CreateSource(context.Background(), source); err != nil {
		t.Fatal(err)
	}
	outbound := &controlledFetcher{digest: digest, results: map[string]fetcher.Result{}}
	manager, err := NewManager(store, outbound, clock.NewFake(now), id.Generator{}, WithDiscoveryTargets([]DiscoveryTarget{{Host: "catalog.example.com", TenantID: tenantID, Audience: AudienceAuthenticated}}))
	if err != nil {
		t.Fatal(err)
	}
	ctx := identityfixture.WithActor(context.Background(), identity.Actor{TenantID: tenantID, Subject: "source-admin", Roles: []identity.Role{identity.RoleSourceAdmin}})
	if _, err := manager.TestSourceAtRevision(ctx, source.ID, source.Revision); catalog.ErrorCodeOf(err) != catalog.CodePreconditionFailed {
		t.Fatalf("legacy source test returned %v", err)
	}
	if len(outbound.requests) != 0 {
		t.Fatalf("legacy source was contacted before configuration review: %#v", outbound.requests)
	}
}
