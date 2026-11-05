package federation

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/open-agentic-catalog/open-agentic-catalog/test/identityfixture"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/audit"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/jobs"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/clock"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/fetcher"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/id"
)

type automaticFetcher struct {
	mu       sync.Mutex
	requests []fetcher.Request
	fetch    func(fetcher.Request, int) (fetcher.Result, error)
}

type maintenanceStoreStub struct {
	candidates []MaintenanceCandidate
	listErr    error
	admitErr   error
	admitted   []jobs.Job
}

func (s *maintenanceStoreStub) ListDueAutomaticSources(context.Context, time.Time, time.Duration, string, int) ([]MaintenanceCandidate, error) {
	return append([]MaintenanceCandidate(nil), s.candidates...), s.listErr
}

func (s *maintenanceStoreStub) AdmitAutomaticSync(_ context.Context, _ MaintenanceCandidate, job jobs.Job, _ time.Duration, _ string, _ int) (jobs.Job, bool, error) {
	s.admitted = append(s.admitted, job)
	return job, false, s.admitErr
}

type failingMaintenanceIDGenerator struct{ err error }

func (g failingMaintenanceIDGenerator) New(time.Time) (string, error) { return "", g.err }

func TestMaintenanceCandidateOrderIsStableAcrossTenantsAndSources(t *testing.T) {
	now := time.Date(2026, time.September, 4, 12, 0, 0, 0, time.UTC)
	if !maintenanceCandidateLess(MaintenanceCandidate{DueAt: now.Add(-time.Second)}, MaintenanceCandidate{DueAt: now}) {
		t.Fatal("an earlier due source did not sort first")
	}
	if !maintenanceCandidateLess(MaintenanceCandidate{DueAt: now, TenantID: "tenant-a"}, MaintenanceCandidate{DueAt: now, TenantID: "tenant-b"}) {
		t.Fatal("equal due times did not use the tenant tie-breaker")
	}
	if !maintenanceCandidateLess(MaintenanceCandidate{DueAt: now, TenantID: "tenant-a", SourceID: "source-a"}, MaintenanceCandidate{DueAt: now, TenantID: "tenant-a", SourceID: "source-b"}) {
		t.Fatal("equal tenant due times did not use the source tie-breaker")
	}
}

type maintenanceFaultJobStore struct {
	jobs.Store
	listErr, enqueueErr error
}

func (s *maintenanceFaultJobStore) List(ctx context.Context, tenant string, request jobs.ListRequest) ([]jobs.Job, error) {
	if s.listErr != nil {
		return nil, s.listErr
	}
	return s.Store.List(ctx, tenant, request)
}

func (s *maintenanceFaultJobStore) Enqueue(ctx context.Context, job jobs.Job, evidence ...audit.Record) (jobs.Job, bool, error) {
	if s.enqueueErr != nil {
		return jobs.Job{}, false, s.enqueueErr
	}
	return s.Store.Enqueue(ctx, job, evidence...)
}

func (f *automaticFetcher) Fetch(_ context.Context, request fetcher.Request) (fetcher.Result, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests = append(f.requests, request)
	result, err := f.fetch(request, len(f.requests))
	if result.RequestedURL == "" {
		result.RequestedURL = request.URL
	}
	if result.FinalURL == "" {
		result.FinalURL = request.URL
	}
	if result.BodyDigest == "" && len(result.Body) > 0 {
		sum := sha256.Sum256(result.Body)
		result.BodyDigest = "sha256:" + hex.EncodeToString(sum[:])
	}
	return withFederationTestEvidence(result, request), err
}

func (f *automaticFetcher) PolicyDigest() string { return federationTestPolicyDigest }

func TestInspectionEstablishesRegistryListCapabilityOnlyAfterProbe(t *testing.T) {
	manifestURL := "https://catalog.example/.well-known/ard.json"
	listProbeURL := "https://registry.example/ard/v1/agents?pageSize=1"
	manifest := `{"entries":[{"identifier":"urn:air:registry.example:registry:main","displayName":"Registry","type":"application/ai-registry+json","url":"https://registry.example/ard/v1/","representativeQueries":["Find agents","Browse agents"]}]}`
	outbound := &routingFetcher{results: map[string]fetcher.Result{
		"GET " + manifestURL:  federationResultAt(manifestURL, manifest),
		"GET " + listProbeURL: {Status: http.StatusOK, FinalURL: listProbeURL, MediaType: "application/json", Body: []byte(`{"items":[],"total":0}`)},
	}}
	manager, err := NewManager(NewMemoryStore(), outbound, clock.NewFake(time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)), id.Generator{})
	if err != nil {
		t.Fatal(err)
	}
	ctx := identityfixture.WithActor(context.Background(), identity.Actor{TenantID: "00000000-0000-7000-8000-000000000001", Subject: "admin", Roles: []identity.Role{identity.RoleSourceAdmin}})
	inspection, err := manager.Inspect(ctx, manifestURL)
	if err != nil {
		t.Fatal(err)
	}
	if inspection.RegistrySearchURL != "https://registry.example/ard/v1/search" || inspection.RegistryListURL != "https://registry.example/ard/v1/agents" || !inspection.Capabilities["search"] || !inspection.Capabilities["list"] || !inspection.Capabilities["enumerable"] {
		t.Fatalf("Registry capabilities = %#v", inspection)
	}
	source, err := manager.Connect(ctx, ConnectInput{InspectionID: inspection.ID, DisplayName: "Registry", Scope: Scope{Kinds: []catalog.Kind{catalog.KindAgent}}, UpdateMode: UpdateAutomatic, Reason: "Approve the inspected Registry"})
	if err != nil || source.RetrievalMode != RetrievalIndexed {
		t.Fatalf("indexed automatic Registry = %#v, %v", source, err)
	}
}

func TestRegistryListProbeRejectsUnverifiedEndpoints(t *testing.T) {
	candidate := "https://registry.example/ard/v1/agents"
	probeURL := candidate + "?pageSize=1"
	validPage := []byte(`{"items":[],"total":0}`)
	tests := []struct {
		name   string
		result fetcher.Result
		err    error
	}{
		{name: "transport failure", err: errors.New("source unavailable")},
		{name: "non-success status", result: fetcher.Result{Status: http.StatusPartialContent, FinalURL: probeURL, MediaType: "application/json", Body: validPage}},
		{name: "non-JSON media", result: fetcher.Result{Status: http.StatusOK, FinalURL: probeURL, MediaType: "text/plain", Body: validPage}},
		{name: "invalid page", result: fetcher.Result{Status: http.StatusOK, FinalURL: probeURL, MediaType: "application/json", Body: []byte(`{"items":null}`)}},
		{name: "invalid final URL", result: fetcher.Result{Status: http.StatusOK, FinalURL: "https://[::1", MediaType: "application/json", Body: validPage}},
		{name: "changed page size", result: fetcher.Result{Status: http.StatusOK, FinalURL: candidate + "?pageSize=2", MediaType: "application/json", Body: validPage}},
		{name: "extra final query", result: fetcher.Result{Status: http.StatusOK, FinalURL: probeURL + "&pageToken=unexpected", MediaType: "application/json", Body: validPage}},
		{name: "changed final path", result: fetcher.Result{Status: http.StatusOK, FinalURL: "https://registry.example/other?pageSize=1", MediaType: "application/json", Body: validPage}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			outbound := &automaticFetcher{fetch: func(fetcher.Request, int) (fetcher.Result, error) { return test.result, test.err }}
			manager, err := NewManager(NewMemoryStore(), outbound, clock.NewFake(time.Date(2026, 9, 1, 10, 15, 0, 0, time.UTC)), id.Generator{})
			if err != nil {
				t.Fatal(err)
			}
			endpoint, finding := manager.probeRegistryList(context.Background(), newSourceAccess("tenant", "", "", candidate), candidate)
			if endpoint != "" || finding.Code != "ARD_REGISTRY_LIST_UNVERIFIED" {
				t.Fatalf("unverified Registry List probe = endpoint %q finding %#v", endpoint, finding)
			}
		})
	}
	manager, err := NewManager(NewMemoryStore(), &routingFetcher{}, clock.NewFake(time.Date(2026, 9, 1, 10, 15, 0, 0, time.UTC)), id.Generator{})
	if err != nil {
		t.Fatal(err)
	}
	if endpoint, finding := manager.probeRegistryList(context.Background(), newSourceAccess("tenant", "", "", candidate), candidate+"?unexpected=true"); endpoint != "" || finding.Code != "ARD_REGISTRY_LIST_INVALID" {
		t.Fatalf("invalid Registry List descriptor = endpoint %q finding %#v", endpoint, finding)
	}
}

func TestRegistryListProbeCannotHideCredentialRotation(t *testing.T) {
	manifestURL := "https://registry.example/ard.json"
	manifest := `{"entries":[{"identifier":"urn:air:registry.example:registry:main","displayName":"Registry","type":"application/ai-registry+json","url":"https://registry.example/ard/v1/","representativeQueries":["Find agents","Browse agents"]}]}`
	outbound := &automaticFetcher{fetch: func(request fetcher.Request, call int) (fetcher.Result, error) {
		switch call {
		case 1:
			// #nosec G101 -- synthetic credential revision evidence, not a secret.
			return fetcher.Result{Status: http.StatusOK, FinalURL: manifestURL, MediaType: "application/ard+json", Body: []byte(manifest), CredentialRevision: "credential-1"}, nil
		case 2:
			// #nosec G101 -- synthetic credential revision evidence, not a secret.
			return fetcher.Result{Status: http.StatusOK, FinalURL: request.URL, MediaType: "application/json", Body: []byte(`{"items":[],"total":0}`), CredentialRevision: "credential-2"}, nil
		default:
			return fetcher.Result{}, errors.New("unexpected source inspection request")
		}
	}}
	manager, err := NewManager(NewMemoryStore(), outbound, clock.NewFake(time.Date(2026, 9, 1, 10, 30, 0, 0, time.UTC)), id.Generator{})
	if err != nil {
		t.Fatal(err)
	}
	ctx := identityfixture.WithActor(context.Background(), identity.Actor{TenantID: "00000000-0000-7000-8000-000000000001", Subject: "admin", Roles: []identity.Role{identity.RoleSourceAdmin}})
	if _, err := manager.InspectWithAccess(ctx, InspectionInput{URL: manifestURL, CredentialBinding: "registry-reader"}); err == nil || !strings.Contains(err.Error(), "changed during one federation operation") {
		t.Fatalf("Registry List probe credential rotation returned %v", err)
	}
}

func TestHybridManifestDoesNotProbeOrSelectRegistryList(t *testing.T) {
	manifestURL := "https://catalog.example/.well-known/ard.json"
	manifest := `{"entries":[{"identifier":"urn:air:registry.example:registry:main","displayName":"Registry","type":"application/ai-registry+json","url":"https://registry.example/ard/v1/","representativeQueries":["Find agents","Browse agents"]},{"identifier":"urn:air:catalog.example:default:one","displayName":"One","type":"application/a2a-agent-card+json","data":{"name":"One"}}]}`
	outbound := &routingFetcher{results: map[string]fetcher.Result{"GET " + manifestURL: federationResultAt(manifestURL, manifest)}}
	manager, err := NewManager(NewMemoryStore(), outbound, clock.NewFake(time.Date(2026, 9, 1, 10, 45, 0, 0, time.UTC)), id.Generator{})
	if err != nil {
		t.Fatal(err)
	}
	ctx := identityfixture.WithActor(context.Background(), identity.Actor{TenantID: "00000000-0000-7000-8000-000000000001", Subject: "admin", Roles: []identity.Role{identity.RoleSourceAdmin}})
	inspection, err := manager.Inspect(ctx, manifestURL)
	if err != nil {
		t.Fatal(err)
	}
	if inspection.SourceType != SourceHybrid || inspection.RegistryListURL != "" || len(outbound.requests) != 1 {
		t.Fatalf("hybrid manifest List selection = inspection %#v requests %#v", inspection, outbound.requests)
	}
}

func TestRegistryListEndpointDerivationIsExact(t *testing.T) {
	for input, expected := range map[string]string{
		"https://registry.example/ard/v1/":       "https://registry.example/ard/v1/agents",
		"https://registry.example/ard/v1/search": "https://registry.example/ard/v1/agents",
		"https://registry.example/ard/v1/agents": "https://registry.example/ard/v1/agents",
	} {
		if actual := registryListEndpoint(input); actual != expected {
			t.Fatalf("Registry endpoint derived from %q = %q, want %q", input, actual, expected)
		}
	}
	for _, input := range []string{
		"http://registry.example/ard/v1/",
		"https://user@registry.example/ard/v1/",
		"https://registry.example/ard%2fv1/",
		"https://registry.example/ard/v1/?token=secret",
		"https://registry.example/ard/v1/#fragment",
	} {
		if actual := registryListEndpoint(input); actual != "" {
			t.Fatalf("unsafe Registry endpoint %q derived as %q", input, actual)
		}
	}
	for _, test := range []struct {
		endpoint string
		token    string
	}{
		{endpoint: "https://registry.example/agents?unexpected=true"},
		{endpoint: "https://registry.example/agents", token: "bad\nvalue"},
		{endpoint: "https://registry.example/agents", token: strings.Repeat("a", maxRegistryPageToken+1)},
	} {
		if _, err := appendRegistryPageToken(test.endpoint, test.token); err == nil {
			t.Fatalf("unsafe Registry pagination input was accepted: %#v", test)
		}
	}
}

func TestRegistryListPaginationReplacesOnlyItsCurrentView(t *testing.T) {
	listURL := "https://registry.example/ard/v1/agents"
	firstURL := listURL + "?pageSize=100"
	// Gateway's reviewed offset codec yields this padded URL-safe base64 token.
	// The peer is synthetic: unlike inferred Gateway terminal output, it omits
	// the final token. Exact URL matching verifies escaping and opaque replay.
	// #nosec G101 -- encoded pagination offset, not a credential.
	const token = "eyJvZmZzZXQiOiAyMH0="
	secondURL := listURL + "?pageSize=100&pageToken=" + url.QueryEscape(token)
	cycle := 0
	outbound := &automaticFetcher{fetch: func(request fetcher.Request, _ int) (fetcher.Result, error) {
		if request.URL == firstURL {
			cycle++
			if cycle == 1 {
				return fetcher.Result{Status: http.StatusOK, FinalURL: request.URL, MediaType: "application/json", Body: []byte(`{"items":[{"identifier":"urn:air:registry.example:default:one","displayName":"One","type":"application/a2a-agent-card+json","data":{"name":"One"}}],"total":2,"pageToken":"` + token + `","extension":{"retained":true}}`)}, nil
			}
			return fetcher.Result{Status: http.StatusOK, FinalURL: request.URL, MediaType: "application/json", Body: []byte(`{"items":[{"identifier":"urn:air:registry.example:default:one","displayName":"One","type":"application/a2a-agent-card+json","data":{"name":"One"}}],"total":1}`)}, nil
		}
		if request.URL == secondURL && cycle == 1 {
			return fetcher.Result{Status: http.StatusOK, FinalURL: request.URL, MediaType: "application/json", Body: []byte(`{"items":[{"identifier":"urn:air:registry.example:default:two","displayName":"Two","type":"application/a2a-agent-card+json","data":{"name":"Two"}}],"total":2}`)}, nil
		}
		return fetcher.Result{}, errors.New("unexpected Registry List request")
	}}
	store := NewMemoryStore()
	fakeClock := clock.NewFake(time.Date(2026, 9, 1, 11, 0, 0, 0, time.UTC))
	manager, err := NewManager(store, outbound, fakeClock, id.Generator{})
	if err != nil {
		t.Fatal(err)
	}
	tenantID := "00000000-0000-7000-8000-000000000001"
	source := Source{ID: "00000000-0000-7000-8000-000000000010", TenantID: tenantID, DisplayName: "Registry", Type: SourceRegistry, Endpoint: "https://catalog.example/ard.json", RegistryListURL: listURL, Status: SourceActive, ProfileID: "ard-0.91-aa3e598", RetrievalMode: RetrievalIndexed, Scope: Scope{Kinds: []catalog.Kind{catalog.KindAgent}}, UpdateMode: UpdateAutomatic, PolicyID: "test-policy", PolicyRevision: 1, PolicyDigest: federationTestPolicyDigest, Revision: 1, CreatedBy: "admin", CreatedAt: fakeClock.Now(), UpdatedAt: fakeClock.Now()}
	if _, err := store.CreateSource(context.Background(), source); err != nil {
		t.Fatal(err)
	}
	ctx := identityfixture.WithActor(context.Background(), identity.Actor{TenantID: tenantID, Subject: "admin", Roles: []identity.Role{identity.RoleSourceAdmin}})
	first, err := manager.Sync(ctx, source.ID)
	if err != nil || first.MemberCount != 2 || !first.ReconcileCurrentView || first.WithdrawalAuthority {
		t.Fatalf("first List snapshot = %#v, %v", first, err)
	}
	fakeClock.Advance(time.Minute)
	second, err := manager.Sync(ctx, source.ID)
	if err != nil || second.MemberCount != 1 || second.Withdrawn != 0 {
		t.Fatalf("replacement List snapshot = %#v, %v", second, err)
	}
	current, err := manager.ObservationPage(ctx, ObservationListRequest{SourceID: source.ID, CurrentOnly: true, Limit: 100})
	if err != nil || len(current) != 1 || current[0].Identifier != "urn:air:registry.example:default:one" {
		t.Fatalf("current Registry view = %#v, %v", current, err)
	}
	all, err := manager.ObservationPage(ctx, ObservationListRequest{SourceID: source.ID, Limit: 100})
	if err != nil || len(all) != 2 {
		t.Fatalf("Registry history = %#v, %v", all, err)
	}
	if !containsObservationFreshness(all, "urn:air:registry.example:default:two", FreshStale) {
		t.Fatalf("absent Registry member was not stale: %#v", all)
	}
	occurrences, total, err := manager.ObservationOccurrences(ctx, current[0].ID, 10)
	if err != nil || total != 2 || len(occurrences) != 2 {
		t.Fatalf("Registry List occurrences = %#v total=%d, %v", occurrences, total, err)
	}
	for _, occurrence := range occurrences {
		if occurrence.EntryLocation != "/items/0" {
			t.Fatalf("Registry List occurrence location = %q, want /items/0", occurrence.EntryLocation)
		}
	}
}

func TestCurrentViewReconciliationRequiresAnInspectedRegistryList(t *testing.T) {
	store := NewMemoryStore()
	now := time.Date(2026, 9, 1, 11, 30, 0, 0, time.UTC)
	source := Source{ID: "source", TenantID: "tenant", Type: SourceARDManifest, Endpoint: "https://catalog.example/ard.json", Status: SourceActive, Revision: 1, CreatedAt: now, UpdatedAt: now}
	if _, err := store.CreateSource(context.Background(), source); err != nil {
		t.Fatal(err)
	}
	snapshot := Snapshot{ID: "snapshot", TenantID: source.TenantID, SourceID: source.ID, SourceRevision: source.Revision, State: SnapshotComplete, EnumerationComplete: true, ReconcileCurrentView: true, StartedAt: now, CompletedAt: &now}
	if _, _, err := store.CommitSync(context.Background(), snapshot, nil, source, source.Revision); catalog.ErrorCodeOf(err) != catalog.CodeInvalidArgument {
		t.Fatalf("manifest current-view reconciliation returned %v", err)
	}
}

func TestConditionalRetrievalReusesValidatedRepresentation(t *testing.T) {
	endpoint := "https://catalog.example/ard.json"
	body := []byte(`{"entries":[{"identifier":"urn:air:catalog.example:default:one","displayName":"One","type":"application/a2a-agent-card+json","data":{"name":"One"}}]}`)
	outbound := &automaticFetcher{fetch: func(request fetcher.Request, call int) (fetcher.Result, error) {
		if call == 1 {
			return fetcher.Result{Status: http.StatusOK, FinalURL: endpoint, MediaType: "application/ard+json", Body: body, ETag: `"catalog-v1"`, Vary: []string{"Accept"}}, nil
		}
		if request.ETag != `"catalog-v1"` || !request.RejectOriginChanges {
			return fetcher.Result{}, errors.New("conditional validators were not bound to the redirect route")
		}
		return fetcher.Result{Status: http.StatusNotModified, FinalURL: endpoint, ETag: `"catalog-v1"`}, nil
	}}
	store := NewMemoryStore()
	fakeClock := clock.NewFake(time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC))
	manager, err := NewManager(store, outbound, fakeClock, id.Generator{})
	if err != nil {
		t.Fatal(err)
	}
	tenantID := "00000000-0000-7000-8000-000000000001"
	source := Source{ID: "00000000-0000-7000-8000-000000000010", TenantID: tenantID, DisplayName: "Catalog", Type: SourceARDManifest, Endpoint: endpoint, Status: SourceActive, ProfileID: "ard-0.91-aa3e598", RetrievalMode: RetrievalIndexed, Scope: Scope{Kinds: []catalog.Kind{catalog.KindAgent}}, UpdateMode: UpdateAutomatic, PolicyID: "test-policy", PolicyRevision: 1, PolicyDigest: federationTestPolicyDigest, Revision: 1, CreatedBy: "admin", CreatedAt: fakeClock.Now(), UpdatedAt: fakeClock.Now()}
	if _, err := store.CreateSource(context.Background(), source); err != nil {
		t.Fatal(err)
	}
	ctx := identityfixture.WithActor(context.Background(), identity.Actor{TenantID: tenantID, Subject: "admin", Roles: []identity.Role{identity.RoleSourceAdmin}})
	first, err := manager.Sync(ctx, source.ID)
	if err != nil || len(first.Retrievals) != 1 || first.Retrievals[0].Reused {
		t.Fatalf("initial conditional state = %#v, %v", first, err)
	}
	fakeClock.Advance(time.Minute)
	second, err := manager.Sync(ctx, source.ID)
	if err != nil || second.BodyDigest != first.BodyDigest || len(second.Retrievals) != 1 || !second.Retrievals[0].Reused || second.Retrievals[0].ReceivedBody {
		t.Fatalf("304 snapshot = %#v, %v", second, err)
	}
	requestKey, _ := retrievalRequestIdentity(endpoint, fetcher.Request{Accept: []string{"application/ard+json", "application/ai-catalog+json", "application/json", "text/html;q=0.5"}})
	state, found, err := store.GetRetrievalState(context.Background(), tenantID, source.ID, source.Revision, requestKey)
	if err != nil || !found || len(state.VaryNames) != 1 || state.VaryNames[0] != "accept" {
		t.Fatalf("304 did not preserve the omitted Vary binding: %#v found=%t, %v", state, found, err)
	}
	current, err := manager.ObservationPage(ctx, ObservationListRequest{SourceID: source.ID, CurrentOnly: true, Limit: 100})
	if err != nil || len(current) != 1 {
		t.Fatalf("conditional current observations = %#v, %v", current, err)
	}
	occurrences, total, err := manager.ObservationOccurrences(ctx, current[0].ID, 10)
	if err != nil || total != 2 || len(occurrences) != 2 {
		t.Fatalf("conditional occurrences = %#v total=%d, %v", occurrences, total, err)
	}
}

func TestConditionalRetrievalRecoversAfterCredentialRotation(t *testing.T) {
	endpoint := "https://catalog.example/ard.json"
	body := []byte(`{"entries":[]}`)
	outbound := &automaticFetcher{fetch: func(request fetcher.Request, call int) (fetcher.Result, error) {
		switch call {
		case 1:
			if request.ExpectedCredentialRevision != "" || request.ETag != "" {
				t.Fatal("initial retrieval unexpectedly carried retained conditional state")
			}
			// #nosec G101 -- synthetic credential revision evidence, not a secret.
			return fetcher.Result{Status: http.StatusOK, FinalURL: endpoint, MediaType: "application/ard+json", Body: body, ETag: `"catalog-v1"`, CredentialRevision: "credential-1"}, nil
		case 2:
			if request.ExpectedCredentialRevision != "credential-1" || request.ETag != `"catalog-v1"` {
				t.Fatalf("stale conditional request = %#v", request)
			}
			return fetcher.Result{}, fetcher.ErrCredentialChanged
		case 3:
			if request.ExpectedCredentialRevision != "" || request.ETag != "" || request.LastModified != "" {
				t.Fatalf("credential-rotation retry retained stale validators: %#v", request)
			}
			// #nosec G101 -- synthetic credential revision evidence, not a secret.
			return fetcher.Result{Status: http.StatusOK, FinalURL: endpoint, MediaType: "application/ard+json", Body: body, ETag: `"catalog-v2"`, CredentialRevision: "credential-2"}, nil
		case 4:
			if request.ExpectedCredentialRevision != "credential-2" || request.ETag != `"catalog-v2"` {
				t.Fatalf("replacement conditional state = %#v", request)
			}
			// #nosec G101 -- synthetic credential revision evidence, not a secret.
			return fetcher.Result{Status: http.StatusNotModified, FinalURL: endpoint, ETag: `"catalog-v2"`, CredentialRevision: "credential-2"}, nil
		default:
			return fetcher.Result{}, errors.New("unexpected credentialed retrieval")
		}
	}}
	store := NewMemoryStore()
	fakeClock := clock.NewFake(time.Date(2026, 9, 1, 12, 10, 0, 0, time.UTC))
	manager, err := NewManager(store, outbound, fakeClock, id.Generator{})
	if err != nil {
		t.Fatal(err)
	}
	tenantID := "00000000-0000-7000-8000-000000000001"
	// #nosec G101 -- credential binding and revision are synthetic non-secret evidence identifiers.
	source := Source{ID: "00000000-0000-7000-8000-000000000010", TenantID: tenantID, DisplayName: "Credentialed Catalog", Type: SourceARDManifest, Endpoint: endpoint, Status: SourceActive, ProfileID: "ard-0.91-aa3e598", RetrievalMode: RetrievalIndexed, Scope: Scope{Kinds: []catalog.Kind{catalog.KindAgent}}, UpdateMode: UpdateAutomatic, NetworkProfile: "partners", CredentialBinding: "catalog-reader", PolicyID: "test-policy", PolicyRevision: 1, PolicyDigest: federationTestPolicyDigest, CredentialRevision: "credential-1", Revision: 1, CreatedBy: "admin", CreatedAt: fakeClock.Now(), UpdatedAt: fakeClock.Now()}
	if _, err := store.CreateSource(context.Background(), source); err != nil {
		t.Fatal(err)
	}
	ctx := identityfixture.WithActor(context.Background(), identity.Actor{TenantID: tenantID, Subject: "admin", Roles: []identity.Role{identity.RoleSourceAdmin}})
	if _, err := manager.Sync(ctx, source.ID); err != nil {
		t.Fatal(err)
	}
	fakeClock.Advance(time.Minute)
	rotated, err := manager.Sync(ctx, source.ID)
	if err != nil || rotated.CredentialRevision != "credential-2" || len(rotated.Retrievals) != 1 || !rotated.Retrievals[0].ReceivedBody || rotated.Retrievals[0].Reused {
		t.Fatalf("credential-rotation recovery = %#v, %v", rotated, err)
	}
	fakeClock.Advance(time.Minute)
	reused, err := manager.Sync(ctx, source.ID)
	if err != nil || reused.CredentialRevision != "credential-2" || len(reused.Retrievals) != 1 || !reused.Retrievals[0].Reused || reused.Retrievals[0].ReceivedBody {
		t.Fatalf("rotated conditional reuse = %#v, %v", reused, err)
	}
}

func TestOriginChangingResponseRequiresReinspectionAndCannotBecomeConditionalState(t *testing.T) {
	endpoint := "https://catalog.example/ard.json"
	finalURL := "https://cdn.example/ard.json"
	body := []byte(`{"entries":[{"identifier":"urn:air:catalog.example:default:one","displayName":"One","type":"application/a2a-agent-card+json","data":{"name":"One"}}]}`)
	outbound := &automaticFetcher{fetch: func(request fetcher.Request, _ int) (fetcher.Result, error) {
		if request.ETag != "" || request.LastModified != "" {
			return fetcher.Result{}, errors.New("origin-changing response was reused conditionally")
		}
		return fetcher.Result{Status: http.StatusOK, FinalURL: finalURL, MediaType: "application/ard+json", Body: body, ETag: `"catalog-v1"`}, nil
	}}
	store := NewMemoryStore()
	fakeClock := clock.NewFake(time.Date(2026, 9, 1, 12, 15, 0, 0, time.UTC))
	manager, err := NewManager(store, outbound, fakeClock, id.Generator{})
	if err != nil {
		t.Fatal(err)
	}
	tenantID := "00000000-0000-7000-8000-000000000001"
	source := Source{ID: "00000000-0000-7000-8000-000000000010", TenantID: tenantID, DisplayName: "Catalog", Type: SourceARDManifest, Endpoint: endpoint, Status: SourceActive, ProfileID: "ard-0.91-aa3e598", RetrievalMode: RetrievalIndexed, Scope: Scope{Kinds: []catalog.Kind{catalog.KindAgent}}, UpdateMode: UpdateAutomatic, PolicyID: "test-policy", PolicyRevision: 1, PolicyDigest: federationTestPolicyDigest, Revision: 1, CreatedBy: "admin", CreatedAt: fakeClock.Now(), UpdatedAt: fakeClock.Now()}
	if _, err := store.CreateSource(context.Background(), source); err != nil {
		t.Fatal(err)
	}
	ctx := identityfixture.WithActor(context.Background(), identity.Actor{TenantID: tenantID, Subject: "admin", Roles: []identity.Role{identity.RoleSourceAdmin}})
	first, err := manager.Sync(ctx, source.ID)
	if catalog.ErrorCodeOf(err) != catalog.CodePreconditionFailed || first.State != SnapshotFailed || first.EnumerationComplete || len(first.RetrievalStates) != 0 {
		t.Fatalf("origin-changing initial snapshot = %#v, %v", first, err)
	}
	fakeClock.Advance(time.Minute)
	second, err := manager.Sync(ctx, source.ID)
	if catalog.ErrorCodeOf(err) != catalog.CodePreconditionFailed || second.State != SnapshotFailed || second.EnumerationComplete || len(second.RetrievalStates) != 0 || len(outbound.requests) != 2 {
		t.Fatalf("origin-changing repeated snapshot = %#v requests=%#v, %v", second, outbound.requests, err)
	}
	requestKey, _ := retrievalRequestIdentity(endpoint, fetcher.Request{Accept: []string{"application/ard+json", "application/ai-catalog+json", "application/json", "text/html;q=0.5"}})
	if state, found, err := store.GetRetrievalState(context.Background(), tenantID, source.ID, source.Revision, requestKey); err != nil || found {
		t.Fatalf("origin-changing conditional state = %#v found=%t, %v", state, found, err)
	}
}

func TestUnusableNotModifiedRetriesOnceWithoutInventingARepresentation(t *testing.T) {
	for _, test := range []struct {
		name       string
		second     fetcher.Result
		wantFailed bool
	}{
		{name: "unconditional recovery", second: fetcher.Result{Status: http.StatusOK, MediaType: "application/ard+json", Body: []byte(`{"entries":[]}`), ETag: `"catalog-v1"`}},
		{name: "repeated not modified", second: fetcher.Result{Status: http.StatusNotModified, ETag: `"catalog-v1"`}, wantFailed: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			endpoint := "https://catalog.example/ard.json"
			outbound := &automaticFetcher{fetch: func(request fetcher.Request, call int) (fetcher.Result, error) {
				if request.ETag != "" || request.LastModified != "" {
					t.Fatal("an unusable 304 retry sent a conditional validator")
				}
				if call == 1 {
					return fetcher.Result{Status: http.StatusNotModified, FinalURL: endpoint, BodyDigest: digestText("")}, nil
				}
				result := test.second
				result.FinalURL = endpoint
				if result.Status == http.StatusNotModified {
					result.BodyDigest = digestText("")
				}
				return result, nil
			}}
			store := NewMemoryStore()
			fakeClock := clock.NewFake(time.Date(2026, 9, 1, 12, 30, 0, 0, time.UTC))
			manager, err := NewManager(store, outbound, fakeClock, id.Generator{})
			if err != nil {
				t.Fatal(err)
			}
			tenantID := "00000000-0000-7000-8000-000000000001"
			source := Source{ID: "00000000-0000-7000-8000-000000000010", TenantID: tenantID, DisplayName: "Catalog", Type: SourceARDManifest, Endpoint: endpoint, Status: SourceActive, ProfileID: "ard-0.91-aa3e598", RetrievalMode: RetrievalIndexed, Scope: Scope{Kinds: []catalog.Kind{catalog.KindAgent}}, UpdateMode: UpdateAutomatic, PolicyID: "test-policy", PolicyRevision: 1, PolicyDigest: federationTestPolicyDigest, Revision: 1, CreatedBy: "admin", CreatedAt: fakeClock.Now(), UpdatedAt: fakeClock.Now()}
			if _, err := store.CreateSource(context.Background(), source); err != nil {
				t.Fatal(err)
			}
			ctx := identityfixture.WithActor(context.Background(), identity.Actor{TenantID: tenantID, Subject: "admin", Roles: []identity.Role{identity.RoleSourceAdmin}})
			snapshot, syncErr := manager.Sync(ctx, source.ID)
			if test.wantFailed != (syncErr != nil) || len(snapshot.Retrievals) != 2 || len(outbound.requests) != 2 {
				t.Fatalf("unusable 304 result = snapshot %#v requests=%d err=%v", snapshot, len(outbound.requests), syncErr)
			}
			if snapshot.Retrievals[0].Status != http.StatusNotModified || snapshot.Retrievals[0].ReceivedBody || snapshot.Retrievals[0].Reused || snapshot.Retrievals[0].RepresentationDigest != "" {
				t.Fatalf("first unusable 304 evidence = %#v", snapshot.Retrievals[0])
			}
			if test.wantFailed && (snapshot.Retrievals[1].Status != http.StatusNotModified || snapshot.Retrievals[1].RepresentationDigest != "") {
				t.Fatalf("repeated unusable 304 evidence = %#v", snapshot.Retrievals[1])
			}
		})
	}
}

func TestNoStoreAndUnsupportedVaryDisableConditionalReuse(t *testing.T) {
	for _, test := range []struct {
		name         string
		cacheControl string
		vary         []string
	}{
		{name: "no-store", cacheControl: "private, no-store"},
		{name: "unsupported Vary", vary: []string{"Authorization"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			endpoint := "https://catalog.example/ard.json"
			body := []byte(`{"entries":[]}`)
			outbound := &automaticFetcher{fetch: func(request fetcher.Request, _ int) (fetcher.Result, error) {
				if request.ETag != "" || request.LastModified != "" {
					t.Fatal("non-reusable response seeded conditional validators")
				}
				return fetcher.Result{Status: http.StatusOK, FinalURL: endpoint, MediaType: "application/ard+json", Body: body, ETag: `"v1"`, CacheControl: test.cacheControl, Vary: test.vary}, nil
			}}
			store := NewMemoryStore()
			fakeClock := clock.NewFake(time.Date(2026, 9, 1, 13, 0, 0, 0, time.UTC))
			manager, err := NewManager(store, outbound, fakeClock, id.Generator{})
			if err != nil {
				t.Fatal(err)
			}
			tenantID := "00000000-0000-7000-8000-000000000001"
			source := Source{ID: "00000000-0000-7000-8000-000000000010", TenantID: tenantID, DisplayName: "Catalog", Type: SourceARDManifest, Endpoint: endpoint, Status: SourceActive, ProfileID: "ard-0.91-aa3e598", RetrievalMode: RetrievalIndexed, Scope: Scope{Kinds: []catalog.Kind{catalog.KindAgent}}, UpdateMode: UpdateManual, PolicyID: "test-policy", PolicyRevision: 1, PolicyDigest: federationTestPolicyDigest, Revision: 1, CreatedBy: "admin", CreatedAt: fakeClock.Now(), UpdatedAt: fakeClock.Now()}
			if _, err := store.CreateSource(context.Background(), source); err != nil {
				t.Fatal(err)
			}
			ctx := identityfixture.WithActor(context.Background(), identity.Actor{TenantID: tenantID, Subject: "admin", Roles: []identity.Role{identity.RoleSourceAdmin}})
			if _, err := manager.Sync(ctx, source.ID); err != nil {
				t.Fatal(err)
			}
			fakeClock.Advance(time.Minute)
			if _, err := manager.Sync(ctx, source.ID); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestRetrievalTextValidationIsStrictAndBounded(t *testing.T) {
	for _, test := range []struct {
		name       string
		value      string
		limit      int
		allowEmpty bool
		want       bool
	}{
		{name: "bounded", value: "profile-v1", limit: 10, want: true},
		{name: "allowed empty", limit: 10, allowEmpty: true, want: true},
		{name: "required", limit: 10},
		{name: "too long", value: "profile-v1", limit: 9},
		{name: "surrounding whitespace", value: " profile-v1", limit: 20},
		{name: "control character", value: "profile\n", limit: 20},
	} {
		t.Run(test.name, func(t *testing.T) {
			if actual := safeRetrievalText(test.value, test.limit, test.allowEmpty); actual != test.want {
				t.Fatalf("safeRetrievalText(%q, %d, %t) = %t, want %t", test.value, test.limit, test.allowEmpty, actual, test.want)
			}
		})
	}
}

func TestConditionalMetadataValidationIsStrictAndBounded(t *testing.T) {
	for _, value := range []string{`"strong"`, `W/"weak"`, `""`} {
		if !validETag(value) || validOrEmptyETag(value) != value {
			t.Fatalf("valid ETag %q was rejected", value)
		}
	}
	for _, value := range []string{"", "unquoted", `w/"lowercase"`, `"bad"quote"`, "\"line\nfeed\"", strings.Repeat("a", 513)} {
		if validETag(value) || validOrEmptyETag(value) != "" {
			t.Fatalf("invalid ETag %q was accepted", value)
		}
	}
	lastModified := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC).Format(http.TimeFormat)
	if !validLastModified(lastModified) || validOrEmptyLastModified(lastModified) != lastModified {
		t.Fatalf("valid Last-Modified value %q was rejected", lastModified)
	}
	for _, value := range []string{"", "not-a-date", "Sun, 30 Aug 2026 12:00:00 GMT\n", strings.Repeat("a", 129)} {
		if validLastModified(value) || validOrEmptyLastModified(value) != "" {
			t.Fatalf("invalid Last-Modified value %q was accepted", value)
		}
	}
	cacheable, vary := reusableResponse(fetcher.Result{ETag: `"catalog-v1"`, Vary: []string{"Accept-Encoding, , ACCEPT", "accept", " "}})
	if !cacheable || !slices.Equal(vary, []string{"accept", "accept-encoding"}) || !validVaryNames(vary) {
		t.Fatalf("normalized reusable response = cacheable %t, Vary %#v", cacheable, vary)
	}
	request := fetcher.Request{Accept: []string{"application/ard+json"}}
	selection := retrievalVarySelection(request, vary)
	if selection == retrievalVarySelection(request, []string{"accept"}) {
		t.Fatal("Vary selection omitted the transport's Accept-Encoding choice")
	}
	if selection == retrievalVarySelection(fetcher.Request{Accept: []string{"application/json"}}, vary) {
		t.Fatal("Vary selection did not bind the requested representation")
	}
	if selection != retrievalVarySelection(request, []string{"accept", "accept-encoding"}) {
		t.Fatal("equivalent normalized Vary headers changed the selection")
	}
	if cacheable, _ := reusableResponse(fetcher.Result{ETag: `"catalog-v1"`, Vary: []string{"*"}}); cacheable {
		t.Fatal("wildcard Vary response was accepted for conditional reuse")
	}
	for _, vary := range [][]string{{"accept", "accept"}, {"accept-encoding", "accept"}, {"accept", "accept-encoding", "other"}} {
		if validVaryNames(vary) {
			t.Fatalf("invalid persisted Vary names were accepted: %#v", vary)
		}
	}
}

func TestSyncRunEnforcesAggregateAccountingAndStateReplacement(t *testing.T) {
	run := newSyncRun(Source{ID: "source"})
	if err := run.accountBytes(maxSyncDecodedBytes); err != nil {
		t.Fatal(err)
	}
	if err := run.accountBytes(1); catalog.ErrorCodeOf(err) != catalog.CodeDependencyUnavailable {
		t.Fatalf("decoded-byte overflow returned %v", err)
	}
	for index := 0; index < maxSyncRequests; index++ {
		if err := run.beforeFetch(); err != nil {
			t.Fatalf("request %d returned %v", index, err)
		}
	}
	if err := run.beforeFetch(); catalog.ErrorCodeOf(err) != catalog.CodeDependencyUnavailable {
		t.Fatalf("request overflow returned %v", err)
	}
	run.invalidate("")
	run.invalidate("sha256:" + strings.Repeat("b", 64))
	run.accept(RetrievalState{})
	run.accept(RetrievalState{RequestKey: "sha256:" + strings.Repeat("b", 64), ETag: `"first"`})
	run.accept(RetrievalState{RequestKey: "sha256:" + strings.Repeat("b", 64), ETag: `"replacement"`})
	_, states, invalidations := run.snapshotEvidence()
	if len(states) != 1 || states[0].ETag != `"replacement"` || len(invalidations) != 0 {
		t.Fatalf("run-scoped state replacement = states %#v invalidations %#v", states, invalidations)
	}
}

func TestSyncRunBoundsRemoteContextReuseAndCardinality(t *testing.T) {
	t.Parallel()

	outbound := &automaticFetcher{fetch: func(fetcher.Request, int) (fetcher.Result, error) {
		t.Fatal("remote-context bounds performed an outbound request")
		return fetcher.Result{}, nil
	}}
	manager, err := NewManager(NewMemoryStore(), outbound, clock.NewFake(time.Date(2026, 9, 1, 13, 0, 0, 0, time.UTC)), id.Generator{})
	if err != nil {
		t.Fatal(err)
	}

	cachedURL := "https://contexts.example/cached"
	cachedRun := newSyncRun(Source{ID: "source"})
	cachedRun.decodedBytes = maxSyncDecodedBytes
	cachedRun.contexts[cachedURL] = contextResult{body: []byte(`{}`), digest: "sha256:" + strings.Repeat("a", 64)}
	cachedContext := context.WithValue(context.Background(), syncRunContextKey{}, cachedRun)
	if body, digest, fetchErr := manager.fetchContext(cachedContext, sourceAccess{}, cachedURL); len(body) != 0 || digest != "" || catalog.ErrorCodeOf(fetchErr) != catalog.CodeDependencyUnavailable || !cachedRun.partial {
		t.Fatalf("cached context overflow = body %q, digest %q, partial %t, err %v", body, digest, cachedRun.partial, fetchErr)
	}

	cardinalityRun := newSyncRun(Source{ID: "source"})
	for index := 0; index < maxSyncContextURLs; index++ {
		cardinalityRun.contexts[fmt.Sprintf("https://contexts.example/%d", index)] = contextResult{}
	}
	cardinalityContext := context.WithValue(context.Background(), syncRunContextKey{}, cardinalityRun)
	if body, digest, fetchErr := manager.fetchContext(cardinalityContext, sourceAccess{}, "https://contexts.example/overflow"); len(body) != 0 || digest != "" || catalog.ErrorCodeOf(fetchErr) != catalog.CodeDependencyUnavailable || !cardinalityRun.partial {
		t.Fatalf("context cardinality overflow = body %q, digest %q, partial %t, err %v", body, digest, cardinalityRun.partial, fetchErr)
	}
}

func TestTestOnlyHTTPSourceNeverPersistsConditionalState(t *testing.T) {
	endpoint := "http://catalog.example/ard.json"
	body := []byte(`{"entries":[]}`)
	outbound := &automaticFetcher{fetch: func(request fetcher.Request, _ int) (fetcher.Result, error) {
		if request.ETag != "" || request.LastModified != "" {
			t.Fatal("test-only HTTP source reused a conditional validator")
		}
		return fetcher.Result{Status: http.StatusOK, FinalURL: endpoint, MediaType: "application/ard+json", Body: body, ETag: `"catalog-v1"`}, nil
	}}
	store := NewMemoryStore()
	fakeClock := clock.NewFake(time.Date(2026, 9, 1, 13, 15, 0, 0, time.UTC))
	manager, err := NewManager(store, outbound, fakeClock, id.Generator{})
	if err != nil {
		t.Fatal(err)
	}
	tenantID := "00000000-0000-7000-8000-000000000001"
	source := Source{ID: "00000000-0000-7000-8000-000000000010", TenantID: tenantID, DisplayName: "HTTP test Catalog", Type: SourceARDManifest, Endpoint: endpoint, Status: SourceActive, ProfileID: "ard-0.91-aa3e598", RetrievalMode: RetrievalIndexed, Scope: Scope{Kinds: []catalog.Kind{catalog.KindAgent}}, UpdateMode: UpdateManual, PolicyID: "test-policy", PolicyRevision: 1, PolicyDigest: federationTestPolicyDigest, Revision: 1, CreatedBy: "admin", CreatedAt: fakeClock.Now(), UpdatedAt: fakeClock.Now()}
	if _, err := store.CreateSource(context.Background(), source); err != nil {
		t.Fatal(err)
	}
	ctx := identityfixture.WithActor(context.Background(), identity.Actor{TenantID: tenantID, Subject: "admin", Roles: []identity.Role{identity.RoleSourceAdmin}})
	for range 2 {
		if snapshot, err := manager.Sync(ctx, source.ID); err != nil || len(snapshot.Retrievals) != 1 || snapshot.Retrievals[0].Reused {
			t.Fatalf("HTTP test synchronization = %#v, %v", snapshot, err)
		}
		fakeClock.Advance(time.Minute)
	}
	requestKey, _ := retrievalRequestIdentity(endpoint, fetcher.Request{Accept: []string{"application/ard+json", "application/ai-catalog+json", "application/json", "text/html;q=0.5"}})
	if _, found, err := store.GetRetrievalState(context.Background(), tenantID, source.ID, source.Revision, requestKey); err != nil || found {
		t.Fatalf("HTTP conditional state found=%t, %v", found, err)
	}
}

func TestSynchronizationRejectsNominalPartialSuccessDocuments(t *testing.T) {
	for _, status := range []int{http.StatusCreated, http.StatusNoContent, http.StatusPartialContent} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			endpoint := "https://catalog.example/.well-known/ard.json"
			outbound := &automaticFetcher{fetch: func(request fetcher.Request, _ int) (fetcher.Result, error) {
				return fetcher.Result{Status: status, FinalURL: request.URL, MediaType: "application/ard+json", Body: []byte(`{"entries":[]}`)}, nil
			}}
			store := NewMemoryStore()
			fakeClock := clock.NewFake(time.Date(2026, 9, 1, 13, 30, 0, 0, time.UTC))
			manager, err := NewManager(store, outbound, fakeClock, id.Generator{})
			if err != nil {
				t.Fatal(err)
			}
			tenantID := "00000000-0000-7000-8000-000000000001"
			source := Source{ID: "00000000-0000-7000-8000-000000000010", TenantID: tenantID, DisplayName: "Catalog", Type: SourceARDManifest, Endpoint: endpoint, Status: SourceActive, ProfileID: "ard-0.91-aa3e598", RetrievalMode: RetrievalIndexed, Scope: Scope{Kinds: []catalog.Kind{catalog.KindAgent}}, UpdateMode: UpdateAutomatic, PolicyID: "test-policy", PolicyRevision: 1, PolicyDigest: federationTestPolicyDigest, Revision: 1, CreatedBy: "admin", CreatedAt: fakeClock.Now(), UpdatedAt: fakeClock.Now()}
			if _, err := store.CreateSource(context.Background(), source); err != nil {
				t.Fatal(err)
			}
			ctx := identityfixture.WithActor(context.Background(), identity.Actor{TenantID: tenantID, Subject: "admin", Roles: []identity.Role{identity.RoleSourceAdmin}})
			snapshot, syncErr := manager.Sync(ctx, source.ID)
			if class, _ := ClassifySyncError(syncErr); class != SyncFailurePermanent || snapshot.State != SnapshotFailed || snapshot.EnumerationComplete {
				t.Fatalf("HTTP %d synchronization = snapshot %#v class %q err=%v", status, snapshot, class, syncErr)
			}
		})
	}
}

func TestAutomaticMaintainerUsesV2PayloadAndDeduplicates(t *testing.T) {
	sourceStore := NewMemoryStore()
	jobStore := jobs.NewMemoryStore()
	maintenanceStore, err := NewMemoryMaintenanceStore(sourceStore, jobStore)
	if err != nil {
		t.Fatal(err)
	}
	fakeClock := clock.NewFake(time.Date(2026, 9, 1, 14, 0, 0, 0, time.UTC))
	source := Source{ID: "00000000-0000-7000-8000-000000000010", TenantID: "00000000-0000-7000-8000-000000000001", Status: SourceActive, Type: SourceARDManifest, RetrievalMode: RetrievalIndexed, UpdateMode: UpdateAutomatic, PolicyDigest: federationTestPolicyDigest, Revision: 3, UpdatedAt: fakeClock.Now()}
	if _, err := sourceStore.CreateSource(context.Background(), source); err != nil {
		t.Fatal(err)
	}
	maintainer, err := NewMaintainer(maintenanceStore, fakeClock, id.Generator{}, time.Minute, federationTestPolicyDigest, 7)
	if err != nil {
		t.Fatal(err)
	}
	fakeClock.Advance(7 * time.Second)
	if err := maintainer.Scan(context.Background()); err != nil {
		t.Fatal(err)
	}
	items, err := jobStore.List(context.Background(), source.TenantID, jobs.ListRequest{Limit: 10})
	if err != nil || len(items) != 1 || items[0].PayloadVersion != 2 || items[0].AttemptLimit != 7 || items[0].DeduplicationKey != sourceSyncDeduplicationKey(source.ID, source.Revision) {
		t.Fatalf("automatic jobs = %#v, %v", items, err)
	}
	var payload struct {
		SourceID       string `json:"sourceId"`
		SourceRevision int64  `json:"sourceRevision"`
		Trigger        string `json:"trigger"`
	}
	if err := json.Unmarshal(items[0].Payload, &payload); err != nil || payload.SourceID != source.ID || payload.SourceRevision != source.Revision || payload.Trigger != "automatic" {
		t.Fatalf("automatic payload = %#v, %v", payload, err)
	}
	if err := maintainer.Scan(context.Background()); err != nil {
		t.Fatal(err)
	}
	items, _ = jobStore.List(context.Background(), source.TenantID, jobs.ListRequest{Limit: 10})
	if len(items) != 1 {
		t.Fatalf("repeat scan enqueued duplicate work: %#v", items)
	}
	firstJitter := maintenanceJitter(source.ID, source.Revision, time.Minute)
	secondJitter := maintenanceJitter(source.ID, source.Revision, time.Minute)
	if firstJitter != secondJitter || firstJitter >= 6*time.Second {
		t.Fatal("maintenance jitter is not deterministic and bounded")
	}
}

func TestAutomaticMaintainerValidatesConfigurationAndRunFailures(t *testing.T) {
	fakeClock := clock.NewFake(time.Date(2026, 9, 1, 14, 30, 0, 0, time.UTC))
	store := &maintenanceStoreStub{}
	for name, create := range map[string]func() (*Maintainer, error){
		"missing store": func() (*Maintainer, error) {
			return NewMaintainer(nil, fakeClock, id.Generator{}, time.Minute, federationTestPolicyDigest, 5)
		},
		"missing clock": func() (*Maintainer, error) {
			return NewMaintainer(store, nil, id.Generator{}, time.Minute, federationTestPolicyDigest, 5)
		},
		"missing IDs": func() (*Maintainer, error) {
			return NewMaintainer(store, fakeClock, nil, time.Minute, federationTestPolicyDigest, 5)
		},
		"short interval": func() (*Maintainer, error) {
			return NewMaintainer(store, fakeClock, id.Generator{}, time.Minute-time.Nanosecond, federationTestPolicyDigest, 5)
		},
		"long interval": func() (*Maintainer, error) {
			return NewMaintainer(store, fakeClock, id.Generator{}, 24*time.Hour+time.Nanosecond, federationTestPolicyDigest, 5)
		},
		"invalid policy": func() (*Maintainer, error) {
			return NewMaintainer(store, fakeClock, id.Generator{}, time.Minute, "not-a-digest", 5)
		},
		"zero attempts": func() (*Maintainer, error) {
			return NewMaintainer(store, fakeClock, id.Generator{}, time.Minute, federationTestPolicyDigest, 0)
		},
		"excessive attempts": func() (*Maintainer, error) {
			return NewMaintainer(store, fakeClock, id.Generator{}, time.Minute, federationTestPolicyDigest, 21)
		},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := create(); err == nil {
				t.Fatal("invalid automatic-maintenance configuration was accepted")
			}
		})
	}
	maintainer, err := NewMaintainer(store, fakeClock, id.Generator{}, time.Minute, federationTestPolicyDigest, 5)
	if err != nil {
		t.Fatal(err)
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := maintainer.Run(cancelled); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled maintainer returned %v", err)
	}
	sentinel := errors.New("maintenance store unavailable")
	failed, err := NewMaintainer(&maintenanceStoreStub{listErr: sentinel}, fakeClock, id.Generator{}, time.Minute, federationTestPolicyDigest, 5)
	if err != nil {
		t.Fatal(err)
	}
	if err := failed.Run(context.Background()); !errors.Is(err, sentinel) {
		t.Fatalf("maintainer startup failure = %v", err)
	}
}

func TestAutomaticMaintainerScanHandlesRacesAndStopsOnUnexpectedFailures(t *testing.T) {
	fakeClock := clock.NewFake(time.Date(2026, 9, 1, 14, 45, 0, 0, time.UTC))
	candidate := MaintenanceCandidate{TenantID: "tenant", SourceID: "source", SourceRevision: 2, DueAt: fakeClock.Now()}
	for _, code := range []catalog.ErrorCode{catalog.CodePreconditionFailed, catalog.CodeNotFound, catalog.CodeConflict} {
		store := &maintenanceStoreStub{candidates: []MaintenanceCandidate{candidate}, admitErr: catalog.E(code, "concurrent maintenance change")}
		maintainer, err := NewMaintainer(store, fakeClock, id.Generator{}, time.Minute, federationTestPolicyDigest, 5)
		if err != nil {
			t.Fatal(err)
		}
		if err := maintainer.Scan(context.Background()); err != nil || len(store.admitted) != 1 {
			t.Fatalf("admission race %q = admitted %d, err %v", code, len(store.admitted), err)
		}
	}
	sentinel := errors.New("admission unavailable")
	store := &maintenanceStoreStub{candidates: []MaintenanceCandidate{candidate}, admitErr: sentinel}
	maintainer, err := NewMaintainer(store, fakeClock, id.Generator{}, time.Minute, federationTestPolicyDigest, 5)
	if err != nil {
		t.Fatal(err)
	}
	if err := maintainer.Scan(context.Background()); !errors.Is(err, sentinel) {
		t.Fatalf("unexpected admission failure = %v", err)
	}
	t.Run("list failure with partial candidates", func(t *testing.T) {
		listFailure := errors.New("candidate list interrupted")
		partialStore := &maintenanceStoreStub{candidates: []MaintenanceCandidate{candidate}, listErr: listFailure}
		partialMaintainer, err := NewMaintainer(partialStore, fakeClock, failingMaintenanceIDGenerator{err: errors.New("unexpected identifier allocation")}, time.Minute, federationTestPolicyDigest, 5)
		if err != nil {
			t.Fatal(err)
		}
		if err := partialMaintainer.Scan(t.Context()); !errors.Is(err, listFailure) || len(partialStore.admitted) != 0 {
			t.Fatalf("partial list failure = %v; admitted jobs = %d; want list error before creating work", err, len(partialStore.admitted))
		}
	})
	failedIDs, err := NewMaintainer(&maintenanceStoreStub{candidates: []MaintenanceCandidate{candidate}}, fakeClock, failingMaintenanceIDGenerator{err: sentinel}, time.Minute, federationTestPolicyDigest, 5)
	if err != nil {
		t.Fatal(err)
	}
	if err := failedIDs.Scan(context.Background()); !errors.Is(err, sentinel) {
		t.Fatalf("identifier failure = %v", err)
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := maintainer.Scan(cancelled); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled scan = %v", err)
	}
}

func TestMemoryAutomaticMaintenanceEligibilityAndScheduleAnchors(t *testing.T) {
	sourceStore := NewMemoryStore()
	jobStore := jobs.NewMemoryStore()
	if _, err := NewMemoryMaintenanceStore(nil, jobStore); err == nil {
		t.Fatal("missing source store was accepted")
	}
	maintenanceStore, err := NewMemoryMaintenanceStore(sourceStore, jobStore)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 1, 15, 0, 0, 0, time.UTC)
	base := Source{TenantID: "tenant", Type: SourceARDManifest, Status: SourceActive, RetrievalMode: RetrievalIndexed, UpdateMode: UpdateAutomatic, PolicyDigest: federationTestPolicyDigest, Revision: 1, UpdatedAt: now.Add(-2 * time.Minute)}
	add := func(idValue string, mutate func(*Source)) {
		t.Helper()
		source := base
		source.ID = idValue
		source.Endpoint = "https://" + idValue + ".example/catalog.json"
		if mutate != nil {
			mutate(&source)
		}
		if _, err := sourceStore.CreateSource(context.Background(), source); err != nil {
			t.Fatal(err)
		}
	}
	add("active", nil)
	add("error", func(source *Source) { source.Status = SourceError })
	add("paused", func(source *Source) { source.Status = SourcePaused })
	add("manual", func(source *Source) { source.UpdateMode = UpdateManual })
	add("live", func(source *Source) { source.RetrievalMode = RetrievalLive })
	add("stale-policy", func(source *Source) {
		source.PolicyDigest = "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	})
	add("search-only-registry", func(source *Source) { source.Type = SourceRegistry })
	completed := now
	add("recent-snapshot", func(source *Source) { source.LastCompleteSnapshotID = "snapshot" })
	sourceStore.mu.Lock()
	sourceStore.snapshots["snapshot"] = Snapshot{ID: "snapshot", CompletedAt: &completed}
	sourceStore.mu.Unlock()
	if _, err := maintenanceStore.ListDueAutomaticSources(context.Background(), now, time.Minute, federationTestPolicyDigest, 0); catalog.ErrorCodeOf(err) != catalog.CodeInvalidArgument {
		t.Fatalf("invalid automatic-source limit = %v", err)
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := maintenanceStore.ListDueAutomaticSources(cancelled, now, time.Minute, federationTestPolicyDigest, maintenanceBatchLimit); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled automatic-source scan = %v", err)
	}
	candidates, err := maintenanceStore.ListDueAutomaticSources(context.Background(), now, time.Minute, federationTestPolicyDigest, maintenanceBatchLimit)
	if err != nil {
		t.Fatal(err)
	}
	due := map[string]bool{}
	for _, candidate := range candidates {
		due[candidate.SourceID] = true
	}
	if len(due) != 2 || !due["active"] || !due["error"] {
		t.Fatalf("eligible automatic sources = %#v", candidates)
	}
	limited, err := maintenanceStore.ListDueAutomaticSources(context.Background(), now, time.Minute, federationTestPolicyDigest, 1)
	if err != nil || len(limited) != 1 {
		t.Fatalf("bounded automatic-source scan = %#v, %v", limited, err)
	}
	active := MaintenanceCandidate{TenantID: "tenant", SourceID: "active", SourceRevision: 1, DueAt: now}
	payload := json.RawMessage(`{"sourceId":"active","sourceRevision":1,"trigger":"automatic"}`)
	job := jobs.Job{ID: "job", TenantID: "tenant", Kind: "source.sync", PayloadVersion: 2, Payload: payload, State: jobs.StateQueued, CreatedAt: now, UpdatedAt: now, DeduplicationKey: sourceSyncDeduplicationKey("active", 1)}
	for _, stale := range []MaintenanceCandidate{
		{TenantID: active.TenantID, SourceID: active.SourceID, SourceRevision: 2},
		{TenantID: "another-tenant", SourceID: active.SourceID, SourceRevision: 1},
		{TenantID: active.TenantID, SourceID: "paused", SourceRevision: 1},
		{TenantID: active.TenantID, SourceID: "missing", SourceRevision: 1},
	} {
		if _, _, err := maintenanceStore.AdmitAutomaticSync(t.Context(), stale, job, time.Minute, federationTestPolicyDigest, maintenanceLiveLimit); catalog.ErrorCodeOf(err) != catalog.CodePreconditionFailed {
			t.Fatalf("stale automatic candidate %#v was admitted: %v", stale, err)
		}
	}
	if items, err := jobStore.List(t.Context(), active.TenantID, jobs.ListRequest{Limit: 10}); err != nil || len(items) != 0 {
		t.Fatalf("stale candidates created Jobs: %#v %v", items, err)
	}
	if _, existing, err := maintenanceStore.AdmitAutomaticSync(context.Background(), active, job, time.Minute, federationTestPolicyDigest, maintenanceLiveLimit); err != nil || existing {
		t.Fatalf("automatic admission existing=%t, err=%v", existing, err)
	}
	beforeInterval, err := maintenanceStore.ListDueAutomaticSources(context.Background(), now.Add(time.Minute-time.Nanosecond), time.Minute, federationTestPolicyDigest, maintenanceBatchLimit)
	if err != nil {
		t.Fatal(err)
	}
	for _, candidate := range beforeInterval {
		if candidate.SourceID == "active" {
			t.Fatalf("admitted source became due before its interval: %#v", beforeInterval)
		}
	}
	afterInterval, err := maintenanceStore.ListDueAutomaticSources(context.Background(), now.Add(2*time.Minute), time.Minute, federationTestPolicyDigest, maintenanceBatchLimit)
	if err != nil {
		t.Fatal(err)
	}
	foundActive, foundSnapshot := false, false
	for _, candidate := range afterInterval {
		foundActive = foundActive || candidate.SourceID == "active"
		foundSnapshot = foundSnapshot || candidate.SourceID == "recent-snapshot"
	}
	if !foundActive || !foundSnapshot {
		t.Fatalf("interval and complete-snapshot anchors did not become due: %#v", afterInterval)
	}
}

func TestMemoryAutomaticMaintenanceAdvancesAnotherTenantAfterCapTurnover(t *testing.T) {
	sourceStore := NewMemoryStore()
	jobStore := jobs.NewMemoryStore()
	maintenanceStore, err := NewMemoryMaintenanceStore(sourceStore, jobStore)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, time.September, 4, 12, 0, 0, 0, time.UTC)
	for _, source := range []Source{
		{ID: "source-a", TenantID: "tenant-a", Type: SourceARDManifest, Endpoint: "https://a.example/ard.json", Status: SourceActive, RetrievalMode: RetrievalIndexed, UpdateMode: UpdateAutomatic, PolicyDigest: federationTestPolicyDigest, Revision: 1, UpdatedAt: now.Add(-2 * time.Minute)},
		{ID: "source-b", TenantID: "tenant-b", Type: SourceARDManifest, Endpoint: "https://b.example/ard.json", Status: SourceActive, RetrievalMode: RetrievalIndexed, UpdateMode: UpdateAutomatic, PolicyDigest: federationTestPolicyDigest, Revision: 1, UpdatedAt: now.Add(-2 * time.Minute)},
	} {
		if _, err := sourceStore.CreateSource(context.Background(), source); err != nil {
			t.Fatal(err)
		}
	}
	first, err := maintenanceStore.ListDueAutomaticSources(context.Background(), now, time.Minute, federationTestPolicyDigest, 1)
	if err != nil || len(first) != 1 {
		t.Fatalf("first bounded admission candidate = %#v, %v", first, err)
	}
	candidate := first[0]
	job := jobs.Job{ID: "job-first", TenantID: candidate.TenantID, Kind: "source.sync", Actor: audit.InitiatorSourceMaintainer, PayloadVersion: 2, Payload: json.RawMessage(`{"sourceId":"` + candidate.SourceID + `","sourceRevision":1,"trigger":"automatic"}`), State: jobs.StateQueued, NotBefore: now, AttemptLimit: 5, DeduplicationKey: sourceSyncDeduplicationKey(candidate.SourceID, 1), CreatedAt: now, UpdatedAt: now}
	if _, existing, err := maintenanceStore.AdmitAutomaticSync(context.Background(), candidate, job, time.Minute, federationTestPolicyDigest, maintenanceLiveLimit); err != nil || existing {
		t.Fatalf("first admission existing=%t, %v", existing, err)
	}
	second, err := maintenanceStore.ListDueAutomaticSources(context.Background(), now, time.Minute, federationTestPolicyDigest, 1)
	if err != nil || len(second) != 1 || second[0].SourceID == candidate.SourceID || second[0].TenantID == candidate.TenantID {
		t.Fatalf("candidate after cap turnover = %#v, first=%#v, %v", second, candidate, err)
	}
}

func TestMemoryAutomaticMaintenanceFailureKeepsSourceDue(t *testing.T) {
	for _, stage := range []string{"count", "enqueue"} {
		t.Run(stage, func(t *testing.T) {
			sourceStore := NewMemoryStore()
			underlying := jobs.NewMemoryStore()
			sentinel := errors.New("Job admission unavailable")
			jobStore := &maintenanceFaultJobStore{Store: underlying}
			if stage == "count" {
				jobStore.listErr = sentinel
			} else {
				jobStore.enqueueErr = sentinel
			}
			maintenanceStore, err := NewMemoryMaintenanceStore(sourceStore, jobStore)
			if err != nil {
				t.Fatal(err)
			}
			now := time.Date(2026, 9, 1, 15, 30, 0, 0, time.UTC)
			source := Source{ID: "source", TenantID: "tenant", Type: SourceARDManifest, Status: SourceActive, RetrievalMode: RetrievalIndexed, UpdateMode: UpdateAutomatic, PolicyDigest: federationTestPolicyDigest, Revision: 1, UpdatedAt: now.Add(-time.Minute)}
			if _, err := sourceStore.CreateSource(t.Context(), source); err != nil {
				t.Fatal(err)
			}
			maintainer, err := NewMaintainer(maintenanceStore, clock.NewFake(now), id.Generator{}, time.Minute, federationTestPolicyDigest, 7)
			if err != nil {
				t.Fatal(err)
			}
			if err := maintainer.Scan(t.Context()); !errors.Is(err, sentinel) {
				t.Fatalf("%s failure lost: %v", stage, err)
			}
			items, err := underlying.List(t.Context(), source.TenantID, jobs.ListRequest{Limit: 10})
			if err != nil || len(items) != 0 {
				t.Fatalf("failed admission enqueued work: %#v, %v", items, err)
			}
			due, err := maintenanceStore.ListDueAutomaticSources(t.Context(), now, time.Minute, federationTestPolicyDigest, 1)
			if err != nil || len(due) != 1 || due[0].SourceID != source.ID {
				t.Fatalf("failed admission consumed source cadence: %#v %v", due, err)
			}
			jobStore.listErr, jobStore.enqueueErr = nil, nil
			if err := maintainer.Scan(t.Context()); err != nil {
				t.Fatal(err)
			}
			items, err = underlying.List(t.Context(), source.TenantID, jobs.ListRequest{Limit: 10})
			if err != nil || len(items) != 1 || items[0].AttemptLimit != 7 || items[0].Attempt != 0 || items[0].State != jobs.StateQueued {
				t.Fatalf("recovery lost configured Job budget: %#v %v", items, err)
			}
			due, err = maintenanceStore.ListDueAutomaticSources(t.Context(), now, time.Minute, federationTestPolicyDigest, 1)
			if err != nil || len(due) != 0 {
				t.Fatalf("successful admission did not advance cadence: %#v %v", due, err)
			}
		})
	}
}

func TestMemoryAutomaticMaintenanceCountsOnlyLiveAutomaticJobs(t *testing.T) {
	sourceStore := NewMemoryStore()
	jobStore := jobs.NewMemoryStore()
	maintenanceStore, err := NewMemoryMaintenanceStore(sourceStore, jobStore)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 1, 15, 45, 0, 0, time.UTC)
	source := Source{ID: "source", TenantID: "tenant", Type: SourceARDManifest, Status: SourceActive, RetrievalMode: RetrievalIndexed, UpdateMode: UpdateAutomatic, PolicyDigest: federationTestPolicyDigest, Revision: 1, UpdatedAt: now.Add(-time.Minute)}
	if _, err := sourceStore.CreateSource(context.Background(), source); err != nil {
		t.Fatal(err)
	}
	ignored := []jobs.Job{
		{ID: "terminal-automatic", TenantID: source.TenantID, Kind: "source.sync", PayloadVersion: 2, Payload: json.RawMessage(`{"trigger":"automatic"}`), State: jobs.StateSucceeded, DeduplicationKey: "terminal", CreatedAt: now.Add(time.Second), UpdatedAt: now.Add(time.Second)},
		{ID: "live-manual", TenantID: source.TenantID, Kind: "source.sync", PayloadVersion: 2, Payload: json.RawMessage(`{"trigger":"manual"}`), State: jobs.StateQueued, DeduplicationKey: "manual", CreatedAt: now.Add(2 * time.Second), UpdatedAt: now.Add(2 * time.Second)},
	}
	for _, job := range ignored {
		if _, _, err := jobStore.Enqueue(context.Background(), job); err != nil {
			t.Fatal(err)
		}
	}
	candidate := MaintenanceCandidate{TenantID: source.TenantID, SourceID: source.ID, SourceRevision: source.Revision, DueAt: now}
	live := jobs.Job{ID: "live-automatic", TenantID: source.TenantID, Kind: "source.sync", PayloadVersion: 2, Payload: json.RawMessage(`{"trigger":"automatic"}`), State: jobs.StateQueued, DeduplicationKey: sourceSyncDeduplicationKey(source.ID, source.Revision), CreatedAt: now.Add(3 * time.Second), UpdatedAt: now.Add(3 * time.Second)}
	if _, existing, err := maintenanceStore.AdmitAutomaticSync(context.Background(), candidate, live, time.Minute, federationTestPolicyDigest, 1); err != nil || existing {
		t.Fatalf("first bounded automatic admission existing=%t, err=%v", existing, err)
	}
	overflow := live
	overflow.ID = "automatic-overflow"
	// Reach capacity admission after the source is due again.
	overflow.CreatedAt = now.Add(2 * time.Minute)
	overflow.DeduplicationKey = "automatic-overflow"
	if _, _, err := maintenanceStore.AdmitAutomaticSync(context.Background(), candidate, overflow, time.Minute, federationTestPolicyDigest, 1); catalog.ErrorCodeOf(err) != catalog.CodeConflict {
		t.Fatalf("automatic live-job ceiling returned %v", err)
	}
	items, err := jobStore.List(context.Background(), source.TenantID, jobs.ListRequest{Limit: 10})
	if err != nil || len(items) != 3 {
		t.Fatalf("capacity rejection persisted unexpected work: %#v, %v", items, err)
	}
}

func TestAutomaticSearchOnlyRegistryIsRejected(t *testing.T) {
	manager, err := NewManager(NewMemoryStore(), &routingFetcher{}, clock.NewFake(time.Date(2026, 9, 1, 16, 0, 0, 0, time.UTC)), id.Generator{})
	if err != nil {
		t.Fatal(err)
	}
	source := Source{ID: "source", TenantID: "tenant", DisplayName: "Search-only Registry", Type: SourceRegistry, Endpoint: "https://registry.example/ard.json", RegistrySearchURL: "https://registry.example/ard/v1/search", Status: SourceActive, ProfileID: "ard-0.91-aa3e598", RetrievalMode: RetrievalLive, Scope: Scope{Kinds: []catalog.Kind{catalog.KindAgent}}, UpdateMode: UpdateAutomatic, Live: LivePolicy{Enabled: true, QueryPrivacyAcknowledged: true, FederationMode: "none"}, PolicyDigest: federationTestPolicyDigest, Revision: 1}
	if _, err := manager.normalizeAndValidateSource(context.Background(), source); catalog.ErrorCodeOf(err) != catalog.CodePreconditionFailed {
		t.Fatalf("automatic Search-only Registry returned %v", err)
	}
}

func TestSourceFindingsRemainBounded(t *testing.T) {
	findings := make([]catalog.Finding, 0)
	for index := 0; index < 1000; index++ {
		findings = appendBoundedFinding(findings, catalog.Finding{Code: "TEST_FINDING", Path: fmt.Sprintf("/entries/%d", index)})
	}
	if len(findings) != 101 || findings[100].Code != "SOURCE_FINDINGS_TRUNCATED" {
		t.Fatalf("bounded findings = %#v", findings)
	}
}

func TestCurrentCredentialRevisionFallsBackForMissingOrEmptyEvidence(t *testing.T) {
	t.Parallel()

	empty := ""
	current := "credential-v2"
	for name, test := range map[string]struct {
		access   sourceAccess
		fallback string
		want     string
	}{
		"missing": {fallback: "credential-v1", want: "credential-v1"},
		"empty":   {access: sourceAccess{credentialRevision: &empty}, fallback: "credential-v1", want: "credential-v1"},
		"current": {access: sourceAccess{credentialRevision: &current}, fallback: "credential-v1", want: "credential-v2"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if got := currentCredentialRevision(test.access, test.fallback); got != test.want {
				t.Fatalf("credential revision = %q, want %q", got, test.want)
			}
		})
	}
}

func containsObservationFreshness(items []Observation, identifier string, freshness Freshness) bool {
	for _, item := range items {
		if item.Identifier == identifier && item.Freshness == freshness {
			return true
		}
	}
	return false
}

func TestRegistryListDecoderRejectsAmbiguousAndContradictoryPages(t *testing.T) {
	for _, body := range []string{
		`[]`,
		`{}`,
		`{"items":null}`,
		`{"items":{}}`,
		`{"items":[],"items":[]}`,
		`{"items":[],"total":-1}`,
		`{"items":[],"total":1.5}`,
		`{"items":[],"total":"1"}`,
		`{"items":[],"total":18446744073709551616}`,
		`{"items":[],"pageToken":""}`,
		`{"items":[],"pageToken":1}`,
		`{"items":[],"pageToken":"bad\nvalue"}`,
		`{"items":[],"pageToken":"` + strings.Repeat("a", maxRegistryPageToken+1) + `"}`,
		`{"items":[` + strings.Repeat(`{},`, registryListPageSize) + `{}]}`,
	} {
		if _, err := decodeRegistryListPage([]byte(body), registryListPageSize); err == nil {
			t.Fatalf("invalid Registry List page was accepted: %.80s", body)
		}
	}
}

// TestRegistryListDecoderEnforcesRequestedPageSize distinguishes valid JSON
// exceeding the requested bound from malformed input and accepts the boundary.
func TestRegistryListDecoderEnforcesRequestedPageSize(t *testing.T) {
	const body = `{"items":[{},{}],"total":2}`
	page, err := decodeRegistryListPage([]byte(body), 2)
	if err != nil || len(page.Items) != 2 || page.Total == nil || *page.Total != 2 || page.TokenPresent {
		t.Fatalf("exact page boundary = %#v, %v", page, err)
	}
	page, err = decodeRegistryListPage([]byte(body), 1)
	if err == nil || !strings.Contains(err.Error(), "exceeds the requested page size") {
		t.Fatalf("oversized page error = %v", err)
	}
	if len(page.Items) != 0 || page.Total != nil || page.TokenPresent {
		t.Fatalf("oversized page leaked partial results: %#v", page)
	}
}

func TestRegistryListConsistencyFailuresStayNonAuthoritative(t *testing.T) {
	one := `{"identifier":"urn:air:registry.example:default:one","displayName":"One","type":"application/a2a-agent-card+json","data":{"name":"One"}}`
	two := `{"identifier":"urn:air:registry.example:default:two","displayName":"Two","type":"application/a2a-agent-card+json","data":{"name":"Two"}}`
	tests := []struct {
		name    string
		pages   []string
		finding string
		members int
	}{
		{name: "terminal total mismatch", pages: []string{`{"items":[` + one + `],"total":2}`}, finding: "REGISTRY_LIST_TOTAL_MISMATCH", members: 1},
		{name: "enumerated beyond total", pages: []string{`{"items":[` + one + `,` + two + `],"total":1}`}, finding: "REGISTRY_LIST_TOTAL_INVALID", members: 2},
		{name: "total changes", pages: []string{`{"items":[` + one + `],"total":2,"pageToken":"next"}`, `{"items":[` + two + `],"total":3}`}, finding: "REGISTRY_LIST_TOTAL_CHANGED", members: 2},
		{name: "page token repeats", pages: []string{`{"items":[],"pageToken":"repeat"}`, `{"items":[],"pageToken":"repeat"}`}, finding: "REGISTRY_LIST_TOKEN_REPEATED"},
		{name: "invalid member", pages: []string{`{"items":[1],"total":1}`}, finding: "REGISTRY_LIST_MEMBER_INVALID"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			outbound := &automaticFetcher{fetch: func(request fetcher.Request, call int) (fetcher.Result, error) {
				if call > len(test.pages) {
					return fetcher.Result{}, errors.New("unexpected extra Registry page request")
				}
				return fetcher.Result{Status: http.StatusOK, FinalURL: request.URL, MediaType: "application/json", Body: []byte(test.pages[call-1])}, nil
			}}
			_, manager, ctx, source, _ := automaticRegistryManager(t, outbound)
			snapshot, err := manager.Sync(ctx, source.ID)
			if catalog.ErrorCodeOf(err) != catalog.CodeDependencyUnavailable || snapshot.State != SnapshotIncomplete || snapshot.EnumerationComplete || snapshot.ReconcileCurrentView || snapshot.MemberCount != test.members || !hasFinding(snapshot.Findings, test.finding) {
				t.Fatalf("non-authoritative Registry result = %#v, %v", snapshot, err)
			}
		})
	}
}

func TestRegistryListEntryBudgetKeepsTheSnapshotNonAuthoritative(t *testing.T) {
	t.Parallel()

	listURL := "https://registry.example/ard/v1/agents?pageSize=100"
	one := `{"identifier":"urn:air:registry.example:default:one","displayName":"One","type":"application/a2a-agent-card+json","data":{"name":"One"}}`
	two := `{"identifier":"urn:air:registry.example:default:two","displayName":"Two","type":"application/a2a-agent-card+json","data":{"name":"Two"}}`
	outbound := &routingFetcher{results: map[string]fetcher.Result{
		"GET " + listURL: {Status: http.StatusOK, FinalURL: listURL, MediaType: "application/json", Body: []byte(`{"items":[` + one + `,` + two + `],"total":2}`)},
	}}
	_, manager, ctx, source, _ := automaticRegistryManager(t, outbound)
	manager.maxEntries = 1

	snapshot, err := manager.Sync(ctx, source.ID)
	if catalog.ErrorCodeOf(err) != catalog.CodeDependencyUnavailable || snapshot.State != SnapshotIncomplete || snapshot.EnumerationComplete || snapshot.ReconcileCurrentView || snapshot.MemberCount != 1 || !hasFinding(snapshot.Findings, "SOURCE_ENTRY_BUDGET_EXHAUSTED") {
		t.Fatalf("entry-bounded Registry snapshot = %#v, %v", snapshot, err)
	}
}

func TestRegistryListPartialRunPreservesLastCompleteView(t *testing.T) {
	listURL := "https://registry.example/ard/v1/agents"
	firstURL := listURL + "?pageSize=100"
	secondURL := firstURL + "&pageToken=next"
	cycle := 0
	outbound := &automaticFetcher{fetch: func(request fetcher.Request, _ int) (fetcher.Result, error) {
		switch request.URL {
		case firstURL:
			cycle++
			identifier := "urn:air:registry.example:default:one"
			if cycle > 1 {
				// This new member must never leak into the complete view if the
				// following page fails, even when the result count stays unchanged.
				identifier = "urn:air:registry.example:default:incomplete"
			}
			body := fmt.Sprintf(`{"items":[{"identifier":%q,"displayName":"One","type":"application/a2a-agent-card+json","data":{"name":"One"}}],"total":2,"pageToken":"next"}`, identifier)
			return fetcher.Result{Status: http.StatusOK, FinalURL: request.URL, MediaType: "application/json", Body: []byte(body)}, nil
		case secondURL:
			if cycle == 1 {
				return fetcher.Result{Status: http.StatusOK, FinalURL: request.URL, MediaType: "application/json", Body: []byte(`{"items":[{"identifier":"urn:air:registry.example:default:two","displayName":"Two","type":"application/a2a-agent-card+json","data":{"name":"Two"}}],"total":2}`)}, nil
			}
			if cycle == 2 {
				return fetcher.Result{Status: http.StatusBadGateway, FinalURL: request.URL, MediaType: "application/problem+json", Body: []byte(`{"error":"temporary"}`)}, nil
			}
			// A successful HTTP response with an invalid terminal token is still
			// incomplete enumeration, not authority to replace the previous view.
			return fetcher.Result{Status: http.StatusOK, FinalURL: request.URL, MediaType: "application/json", Body: []byte(`{"items":[],"total":2,"pageToken":null}`)}, nil
		default:
			return fetcher.Result{}, errors.New("unexpected Registry List request")
		}
	}}
	store, manager, ctx, source, fakeClock := automaticRegistryManager(t, outbound)
	complete, err := manager.Sync(ctx, source.ID)
	if err != nil {
		t.Fatal(err)
	}
	baseline, _, err := store.ListCurrentObservations(ctx, source.TenantID, source.ID, 100)
	if err != nil || len(baseline) != 2 {
		t.Fatalf("initial complete view = %#v, %v", baseline, err)
	}
	expectedDigests := make(map[string]string, len(baseline))
	for _, observation := range baseline {
		expectedDigests[observation.Identifier] = observation.ExactEntryDigest
	}
	for _, name := range []string{"HTTP failure", "null terminal token"} {
		t.Run(name, func(t *testing.T) {
			fakeClock.Advance(time.Minute)
			failed, err := manager.Sync(ctx, source.ID)
			if err == nil || failed.State != SnapshotFailed {
				t.Fatalf("partial Registry run = %#v, %v", failed, err)
			}
			current, _, err := store.ListCurrentObservations(ctx, source.TenantID, source.ID, 100)
			if err != nil || len(current) != 2 {
				t.Fatalf("last complete Registry view = %#v, %v", current, err)
			}
			for _, observation := range current {
				if digest, present := expectedDigests[observation.Identifier]; !present || digest != observation.ExactEntryDigest || observation.SnapshotID != complete.ID {
					t.Fatalf("failed pagination replaced complete evidence: %#v", observation)
				}
			}
			retained, err := store.GetSource(ctx, source.TenantID, source.ID)
			if err != nil || retained.LastCompleteSnapshotID != complete.ID {
				t.Fatalf("failed pagination changed complete snapshot pointer: %q, %v", retained.LastCompleteSnapshotID, err)
			}
		})
	}
}

func TestRegistryListRedirectRequiresReinspection(t *testing.T) {
	pageURL := "https://registry.example/ard/v1/agents?pageSize=100"
	redirectedURL := "https://registry.example/ard/v2/agents?pageSize=100"
	outbound := &automaticFetcher{fetch: func(fetcher.Request, int) (fetcher.Result, error) {
		return fetcher.Result{Status: http.StatusOK, FinalURL: redirectedURL, MediaType: "application/json", Body: []byte(`{"items":[]}`)}, nil
	}}
	store, manager, ctx, source, _ := automaticRegistryManager(t, outbound)
	snapshot, err := manager.Sync(ctx, source.ID)
	if catalog.ErrorCodeOf(err) != catalog.CodePreconditionFailed || snapshot.State != SnapshotFailed || snapshot.EnumerationComplete || snapshot.MemberCount != 0 || len(snapshot.RetrievalStates) != 0 {
		t.Fatalf("redirected Registry List snapshot = %#v, %v", snapshot, err)
	}
	if len(outbound.requests) != 1 || outbound.requests[0].URL != pageURL {
		t.Fatalf("redirected Registry List requests = %#v", outbound.requests)
	}
	if observations, listErr := store.ListObservationPage(context.Background(), source.TenantID, ObservationListRequest{SourceID: source.ID, Limit: 100}); listErr != nil || len(observations) != 0 {
		t.Fatalf("redirected Registry List changed observations: %#v, %v", observations, listErr)
	}
}

func TestRegistryListDuplicateMemberCannotReplaceCurrentView(t *testing.T) {
	listURL := "https://registry.example/ard/v1/agents"
	pageURL := listURL + "?pageSize=100"
	member := `{"identifier":"urn:air:registry.example:default:one","displayName":"One","type":"application/a2a-agent-card+json","data":{"name":"One"}}`
	outbound := &automaticFetcher{fetch: func(request fetcher.Request, _ int) (fetcher.Result, error) {
		return fetcher.Result{Status: http.StatusOK, FinalURL: pageURL, MediaType: "application/json", Body: []byte(`{"items":[` + member + `,` + member + `],"total":2}`)}, nil
	}}
	store, manager, ctx, source, _ := automaticRegistryManager(t, outbound)
	snapshot, err := manager.Sync(ctx, source.ID)
	if catalog.ErrorCodeOf(err) != catalog.CodeDependencyUnavailable || snapshot.State != SnapshotIncomplete || snapshot.EnumerationComplete || snapshot.ReconcileCurrentView {
		t.Fatalf("duplicate Registry snapshot = %#v, %v", snapshot, err)
	}
	current, _, listErr := store.ListCurrentObservations(context.Background(), source.TenantID, source.ID, 100)
	if listErr != nil || len(current) != 0 {
		t.Fatalf("duplicate Registry page changed current view = %#v, %v", current, listErr)
	}
	if len(outbound.requests) != 1 || outbound.requests[0].URL != pageURL {
		t.Fatalf("Registry requests = %#v", outbound.requests)
	}
}

func TestRegistryListEquivalentNormalizedMemberCannotReplaceCurrentView(t *testing.T) {
	listURL := "https://registry.example/ard/v1/agents"
	pageURL := listURL + "?pageSize=100"
	first := `{"identifier":"urn:air:registry.example:default:one","displayName":"One Agent","type":"application/a2a-agent-card+json","data":{"name":"One"},"tags":["beta","alpha"],"representativeQueries":["Find   one agent"]}`
	second := `{"representativeQueries":[" Find one agent "],"tags":["alpha","beta"],"data":{"name":"One"},"type":"application/a2a-agent-card+json","displayName":" One  Agent ","identifier":"urn:air:registry.example:default:one"}`
	outbound := &automaticFetcher{fetch: func(request fetcher.Request, _ int) (fetcher.Result, error) {
		return fetcher.Result{Status: http.StatusOK, FinalURL: pageURL, MediaType: "application/json", Body: []byte(`{"items":[` + first + `,` + second + `],"total":2}`)}, nil
	}}
	store, manager, ctx, source, _ := automaticRegistryManager(t, outbound)
	snapshot, err := manager.Sync(ctx, source.ID)
	if catalog.ErrorCodeOf(err) != catalog.CodeDependencyUnavailable || snapshot.State != SnapshotIncomplete || snapshot.EnumerationComplete || snapshot.ReconcileCurrentView || snapshot.MemberCount != 1 || !hasFinding(snapshot.Findings, "REGISTRY_LIST_DUPLICATE_MEMBER") {
		t.Fatalf("equivalent Registry members snapshot = %#v, %v", snapshot, err)
	}
	current, _, listErr := store.ListCurrentObservations(context.Background(), source.TenantID, source.ID, 100)
	if listErr != nil || len(current) != 0 {
		t.Fatalf("equivalent Registry members changed current view = %#v, %v", current, listErr)
	}
}

func TestRegistryListRejectsContinuationAfterDeclaredTotal(t *testing.T) {
	listURL := "https://registry.example/ard/v1/agents"
	pageURL := listURL + "?pageSize=100"
	member := `{"identifier":"urn:air:registry.example:default:one","displayName":"One","type":"application/a2a-agent-card+json","data":{"name":"One"}}`
	outbound := &automaticFetcher{fetch: func(request fetcher.Request, _ int) (fetcher.Result, error) {
		return fetcher.Result{Status: http.StatusOK, FinalURL: pageURL, MediaType: "application/json", Body: []byte(`{"items":[` + member + `],"total":1,"pageToken":"contradictory"}`)}, nil
	}}
	_, manager, ctx, source, _ := automaticRegistryManager(t, outbound)
	snapshot, err := manager.Sync(ctx, source.ID)
	if catalog.ErrorCodeOf(err) != catalog.CodeDependencyUnavailable || snapshot.State != SnapshotIncomplete || snapshot.EnumerationComplete || snapshot.ReconcileCurrentView || snapshot.MemberCount != 1 || !hasFinding(snapshot.Findings, "REGISTRY_LIST_CONTINUATION_INVALID") {
		t.Fatalf("count-contradicting continuation snapshot = %#v, %v", snapshot, err)
	}
	if len(outbound.requests) != 1 || outbound.requests[0].URL != pageURL {
		t.Fatalf("count-contradicting continuation requests = %#v", outbound.requests)
	}
}

func TestRegistryListPageBudgetProducesAnIncompleteSnapshot(t *testing.T) {
	outbound := &automaticFetcher{fetch: func(request fetcher.Request, call int) (fetcher.Result, error) {
		parsed, err := url.Parse(request.URL)
		if err != nil || parsed.Query().Get("pageSize") != fmt.Sprint(registryListPageSize) {
			return fetcher.Result{}, errors.New("unexpected Registry List page URL")
		}
		next := fmt.Sprintf("page-%d", call)
		return fetcher.Result{Status: http.StatusOK, FinalURL: request.URL, MediaType: "application/json", Body: []byte(fmt.Sprintf(`{"items":[],"pageToken":%q}`, next))}, nil
	}}
	_, manager, ctx, source, _ := automaticRegistryManager(t, outbound)
	snapshot, err := manager.Sync(ctx, source.ID)
	if catalog.ErrorCodeOf(err) != catalog.CodeDependencyUnavailable || snapshot.State != SnapshotIncomplete || snapshot.EnumerationComplete || len(snapshot.Retrievals) != maxRegistryListPages || len(outbound.requests) != maxRegistryListPages {
		t.Fatalf("Registry page budget = snapshot %#v requests=%d err=%v", snapshot, len(outbound.requests), err)
	}
	found := false
	for _, finding := range snapshot.Findings {
		found = found || finding.Code == "REGISTRY_LIST_PAGE_BUDGET_EXHAUSTED"
	}
	if !found {
		t.Fatalf("Registry page-budget finding missing: %#v", snapshot.Findings)
	}
}

func TestNonSuccessResponseInvalidatesConditionalState(t *testing.T) {
	endpoint := "https://catalog.example/.well-known/ard.json"
	body := []byte(`{"entries":[]}`)
	rootCalls := 0
	outbound := &automaticFetcher{fetch: func(request fetcher.Request, _ int) (fetcher.Result, error) {
		if request.URL != endpoint {
			return fetcher.Result{Status: http.StatusServiceUnavailable, FinalURL: request.URL, MediaType: "application/problem+json", Body: []byte(`{"error":"temporary"}`)}, nil
		}
		rootCalls++
		switch rootCalls {
		case 1:
			return fetcher.Result{Status: http.StatusOK, FinalURL: endpoint, MediaType: "application/ard+json", Body: body, ETag: `"catalog-v1"`}, nil
		case 2:
			if request.ETag != `"catalog-v1"` {
				t.Fatal("conditional validator was not used before the upstream failure")
			}
			return fetcher.Result{Status: http.StatusServiceUnavailable, FinalURL: endpoint, MediaType: "application/problem+json", Body: []byte(`{"error":"temporary"}`)}, nil
		default:
			if request.ETag != "" || request.LastModified != "" {
				t.Fatal("failed response did not invalidate the retained validators")
			}
			return fetcher.Result{Status: http.StatusOK, FinalURL: endpoint, MediaType: "application/ard+json", Body: body, ETag: `"catalog-v2"`}, nil
		}
	}}
	store := NewMemoryStore()
	fakeClock := clock.NewFake(time.Date(2026, 9, 1, 16, 30, 0, 0, time.UTC))
	manager, err := NewManager(store, outbound, fakeClock, id.Generator{})
	if err != nil {
		t.Fatal(err)
	}
	tenantID := "00000000-0000-7000-8000-000000000001"
	source := Source{ID: "00000000-0000-7000-8000-000000000010", TenantID: tenantID, DisplayName: "Catalog", Type: SourceARDManifest, Endpoint: endpoint, Status: SourceActive, ProfileID: "ard-0.91-aa3e598", RetrievalMode: RetrievalIndexed, Scope: Scope{Kinds: []catalog.Kind{catalog.KindAgent}}, UpdateMode: UpdateAutomatic, PolicyID: "test-policy", PolicyRevision: 1, PolicyDigest: federationTestPolicyDigest, Revision: 1, CreatedBy: "admin", CreatedAt: fakeClock.Now(), UpdatedAt: fakeClock.Now()}
	if _, err := store.CreateSource(context.Background(), source); err != nil {
		t.Fatal(err)
	}
	ctx := identityfixture.WithActor(context.Background(), identity.Actor{TenantID: tenantID, Subject: "admin", Roles: []identity.Role{identity.RoleSourceAdmin}})
	if _, err := manager.Sync(ctx, source.ID); err != nil {
		t.Fatal(err)
	}
	fakeClock.Advance(time.Minute)
	failed, err := manager.Sync(ctx, source.ID)
	if class, _ := ClassifySyncError(err); class != SyncFailureTransient || failed.State != SnapshotFailed {
		t.Fatalf("temporary upstream failure = snapshot %#v class %q err=%v", failed, class, err)
	}
	fakeClock.Advance(time.Minute)
	if _, err := manager.Sync(ctx, source.ID); err != nil {
		t.Fatal(err)
	}
}

func TestParentNotModifiedStillRefreshesCollapsedRemoteContexts(t *testing.T) {
	endpoint := "https://catalog.example/ard.json"
	contextURL := "https://contexts.example/aliases.jsonld"
	body := []byte(`{"entries":[{"@context":"` + contextURL + `","identifier":"urn:air:catalog.example:default:one","displayName":"One","type":"application/a2a-agent-card+json","data":{"name":"One"}},{"@context":"` + contextURL + `","identifier":"urn:air:catalog.example:default:two","displayName":"Two","type":"application/a2a-agent-card+json","data":{"name":"Two"}}]}`)
	contextOne := []byte(`{"@context":{"unused":"https://example.com/v1#unused"}}`)
	contextTwo := []byte(`{"@context":{"unused":"https://example.com/v2#unused"}}`)
	rootCalls, contextCalls := 0, 0
	outbound := &automaticFetcher{fetch: func(request fetcher.Request, _ int) (fetcher.Result, error) {
		switch request.URL {
		case endpoint:
			rootCalls++
			if rootCalls == 1 {
				return fetcher.Result{Status: http.StatusOK, FinalURL: endpoint, MediaType: "application/ard+json", Body: body, ETag: `"root-v1"`}, nil
			}
			if request.ETag != `"root-v1"` {
				return fetcher.Result{}, errors.New("root validator missing")
			}
			return fetcher.Result{Status: http.StatusNotModified, FinalURL: endpoint, ETag: `"root-v1"`}, nil
		case contextURL:
			contextCalls++
			if contextCalls == 1 {
				return fetcher.Result{Status: http.StatusOK, FinalURL: contextURL, MediaType: "application/ld+json", Body: contextOne, ETag: `"context-v1"`}, nil
			}
			if request.ETag != `"context-v1"` {
				return fetcher.Result{}, errors.New("context validator missing")
			}
			return fetcher.Result{Status: http.StatusOK, FinalURL: contextURL, MediaType: "application/ld+json", Body: contextTwo, ETag: `"context-v2"`}, nil
		default:
			return fetcher.Result{}, errors.New("unexpected retrieval")
		}
	}}
	store := NewMemoryStore()
	fakeClock := clock.NewFake(time.Date(2026, 9, 1, 15, 0, 0, 0, time.UTC))
	manager, err := NewManager(store, outbound, fakeClock, id.Generator{})
	if err != nil {
		t.Fatal(err)
	}
	tenantID := "00000000-0000-7000-8000-000000000001"
	source := Source{ID: "00000000-0000-7000-8000-000000000010", TenantID: tenantID, DisplayName: "Catalog", Type: SourceARDManifest, Endpoint: endpoint, Status: SourceActive, ProfileID: "ard-0.91-aa3e598", RetrievalMode: RetrievalIndexed, Scope: Scope{Kinds: []catalog.Kind{catalog.KindAgent}}, UpdateMode: UpdateAutomatic, PolicyID: "test-policy", PolicyRevision: 1, PolicyDigest: federationTestPolicyDigest, Revision: 1, CreatedBy: "admin", CreatedAt: fakeClock.Now(), UpdatedAt: fakeClock.Now()}
	if _, err := store.CreateSource(context.Background(), source); err != nil {
		t.Fatal(err)
	}
	ctx := identityfixture.WithActor(context.Background(), identity.Actor{TenantID: tenantID, Subject: "admin", Roles: []identity.Role{identity.RoleSourceAdmin}})
	if _, err := manager.Sync(ctx, source.ID); err != nil {
		t.Fatal(err)
	}
	first, err := manager.ObservationPage(ctx, ObservationListRequest{SourceID: source.ID, CurrentOnly: true, Limit: 100})
	if err != nil || len(first) != 2 || first[0].IdentityDigest == "" {
		t.Fatalf("initial context observations = %#v, %v", first, err)
	}
	firstIdentities := map[string]string{}
	for _, observation := range first {
		occurrences, _, occurrenceErr := manager.ObservationOccurrences(ctx, observation.ID, 1)
		if occurrenceErr != nil || len(occurrences) != 1 || occurrences[0].NormalizationInputDigest == "" {
			t.Fatalf("initial normalization inputs = %#v, %v", occurrences, occurrenceErr)
		}
		firstIdentities[observation.Identifier] = observation.IdentityDigest
	}
	fakeClock.Advance(time.Minute)
	second, err := manager.Sync(ctx, source.ID)
	if err != nil || len(second.Retrievals) != 2 || !second.Retrievals[0].Reused {
		t.Fatalf("context refresh snapshot = %#v, %v", second, err)
	}
	if rootCalls != 2 || contextCalls != 2 {
		t.Fatalf("run-scoped context collapse made root=%d context=%d requests", rootCalls, contextCalls)
	}
	current, err := manager.ObservationPage(ctx, ObservationListRequest{SourceID: source.ID, CurrentOnly: true, Limit: 100})
	if err != nil || len(current) != 2 {
		t.Fatalf("current context observations = %#v, %v", current, err)
	}
	for _, observation := range current {
		occurrences, _, occurrenceErr := manager.ObservationOccurrences(ctx, observation.ID, 1)
		if occurrenceErr != nil || len(occurrences) != 1 || observation.IdentityDigest == firstIdentities[observation.Identifier] || occurrences[0].NormalizationInputDigest == "" {
			t.Fatalf("changed context did not create a distinct observation identity: %#v", observation)
		}
	}
	all, err := manager.ObservationPage(ctx, ObservationListRequest{SourceID: source.ID, Limit: 100})
	if err != nil || len(all) != 4 {
		t.Fatalf("context-sensitive observation history = %#v, %v", all, err)
	}
	for identifier, oldIdentity := range firstIdentities {
		if !containsObservationIdentity(all, identifier, oldIdentity, FreshWithdrawn) {
			t.Fatalf("prior context meaning was not withdrawn: %#v", all)
		}
	}
}

func TestAggregateRequestBudgetMakesContextHeavyRunNonAuthoritative(t *testing.T) {
	endpoint := "https://catalog.example/ard.json"
	entries := make([]map[string]any, 33)
	for index := range entries {
		entries[index] = map[string]any{"@context": fmt.Sprintf("https://contexts.example/%d.jsonld", index), "identifier": fmt.Sprintf("urn:air:catalog.example:default:item-%d", index), "displayName": fmt.Sprintf("Item %d", index), "type": "application/a2a-agent-card+json", "data": map[string]any{"name": fmt.Sprintf("Item %d", index)}}
	}
	body, err := json.Marshal(map[string]any{"entries": entries})
	if err != nil {
		t.Fatal(err)
	}
	outbound := &automaticFetcher{fetch: func(request fetcher.Request, _ int) (fetcher.Result, error) {
		if request.URL == endpoint {
			return fetcher.Result{Status: http.StatusOK, FinalURL: endpoint, MediaType: "application/ard+json", Body: body}, nil
		}
		return fetcher.Result{Status: http.StatusOK, FinalURL: request.URL, MediaType: "application/ld+json", Body: []byte(`{"@context":{}}`)}, nil
	}}
	store := NewMemoryStore()
	fakeClock := clock.NewFake(time.Date(2026, 9, 1, 16, 0, 0, 0, time.UTC))
	manager, err := NewManager(store, outbound, fakeClock, id.Generator{})
	if err != nil {
		t.Fatal(err)
	}
	tenantID := "00000000-0000-7000-8000-000000000001"
	source := Source{ID: "00000000-0000-7000-8000-000000000010", TenantID: tenantID, DisplayName: "Context-heavy", Type: SourceARDManifest, Endpoint: endpoint, Status: SourceActive, ProfileID: "ard-0.91-aa3e598", RetrievalMode: RetrievalIndexed, Scope: Scope{Kinds: []catalog.Kind{catalog.KindAgent}}, UpdateMode: UpdateAutomatic, PolicyID: "test-policy", PolicyRevision: 1, PolicyDigest: federationTestPolicyDigest, Revision: 1, CreatedBy: "admin", CreatedAt: fakeClock.Now(), UpdatedAt: fakeClock.Now()}
	if _, err := store.CreateSource(context.Background(), source); err != nil {
		t.Fatal(err)
	}
	ctx := identityfixture.WithActor(context.Background(), identity.Actor{TenantID: tenantID, Subject: "admin", Roles: []identity.Role{identity.RoleSourceAdmin}})
	snapshot, err := manager.Sync(ctx, source.ID)
	if catalog.ErrorCodeOf(err) != catalog.CodeDependencyUnavailable || snapshot.State != SnapshotIncomplete || snapshot.EnumerationComplete || snapshot.WithdrawalAuthority || len(outbound.requests) != 32 {
		t.Fatalf("aggregate request bound = snapshot %#v requests=%d err=%v", snapshot, len(outbound.requests), err)
	}
}

func TestSyncFailureClassificationAndRetryAfterAreBounded(t *testing.T) {
	now := time.Date(2026, 9, 1, 17, 0, 0, 0, time.UTC)
	for value, expected := range map[string]time.Duration{
		"120": 2 * time.Minute,
		now.Add(5 * time.Minute).Format(http.TimeFormat): 5 * time.Minute,
		"86400":   24 * time.Hour,
		"invalid": 0,
		"86401":   0,
	} {
		for _, status := range []int{http.StatusTooManyRequests, http.StatusServiceUnavailable} {
			class, retry := ClassifySyncError(syncHTTPError(fetcher.Result{Status: status, RetryAfter: value, FetchedAt: now}))
			wantClass := SyncFailureTransient
			if status == http.StatusTooManyRequests {
				wantClass = SyncFailureRateLimited
			}
			if class != wantClass || retry != expected {
				t.Errorf("HTTP %d hint %q: class=%s retry=%s, want %s", status, value, class, retry, expected)
			}
		}
	}
	if class, retry := ClassifySyncError(nil); class != "" || retry != 0 {
		t.Fatalf("nil error class = %q retry=%v", class, retry)
	}
	cause := errors.New("private upstream detail")
	classified := &SyncError{Class: SyncFailureRateLimited, Safe: "safe detail", RetryAfter: time.Minute, Cause: cause}
	if classified.Error() != "safe detail" || !errors.Is(classified, cause) {
		t.Fatalf("classified error = %q unwrap=%v", classified.Error(), errors.Is(classified, cause))
	}
	if class, retry := ClassifySyncError(fmt.Errorf("wrapped: %w", classified)); class != SyncFailureRateLimited || retry != time.Minute {
		t.Fatalf("wrapped classified error = %q retry=%v", class, retry)
	}
	if actual := (&SyncError{Cause: cause}).Error(); actual != cause.Error() {
		t.Fatalf("cause-only error = %q", actual)
	}
	if actual := (&SyncError{}).Error(); actual != "source synchronization failed" {
		t.Fatalf("empty classified error = %q", actual)
	}
	if class, _ := ClassifySyncError(fmt.Errorf("wrapped: %w", fetcher.ErrPolicyChanged)); class != SyncFailurePolicy {
		t.Fatalf("policy change class = %q", class)
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if class, _ := ClassifySyncError(cancelled.Err()); class != SyncFailureCancelled {
		t.Fatalf("cancellation class = %q", class)
	}
	if class, _ := ClassifySyncError(context.DeadlineExceeded); class != SyncFailureTransient {
		t.Fatalf("deadline class = %q", class)
	}
	for _, status := range []int{http.StatusInternalServerError, http.StatusBadGateway, http.StatusInsufficientStorage} {
		if class, retry := ClassifySyncError(syncHTTPError(fetcher.Result{Status: status, RetryAfter: "120", FetchedAt: now})); class != SyncFailureTransient || retry != 0 {
			t.Fatalf("HTTP %d class=%q retry=%s", status, class, retry)
		}
	}
	for _, status := range []int{http.StatusNotImplemented, http.StatusHTTPVersionNotSupported} {
		if class, retry := ClassifySyncError(syncHTTPError(fetcher.Result{Status: status, RetryAfter: "120", FetchedAt: now})); class != SyncFailurePermanent || retry != 0 {
			t.Fatalf("HTTP %d class=%q retry=%s", status, class, retry)
		}
	}
	for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden} {
		if class, retry := ClassifySyncError(syncHTTPError(fetcher.Result{Status: status, RetryAfter: "120", FetchedAt: now})); class != SyncFailureAuthentication || retry != 0 {
			t.Fatalf("HTTP %d class=%q retry=%s", status, class, retry)
		}
	}
	domainClasses := []struct {
		code  catalog.ErrorCode
		class SyncFailureClass
	}{
		{code: catalog.CodeInvalidArgument, class: SyncFailureInvalidInput},
		{code: catalog.CodeSourcePolicyDenied, class: SyncFailurePolicy},
		{code: catalog.CodeDependencyUnavailable, class: SyncFailureDependency},
		{code: catalog.CodeInternal, class: SyncFailureInternal},
		{code: catalog.CodeConflict, class: SyncFailureTransient},
		{code: catalog.ErrorCode("FUTURE_CODE"), class: SyncFailureTransient},
	}
	for _, test := range domainClasses {
		if class, _ := ClassifySyncError(catalog.E(test.code, "failure")); class != test.class {
			t.Fatalf("domain code %q class = %q, want %q", test.code, class, test.class)
		}
	}
	safeMessages := []struct {
		class   SyncFailureClass
		message string
	}{
		{class: SyncFailureAuthentication, message: "Source authentication failed."},
		{class: SyncFailureRateLimited, message: "Source synchronization was rate limited."},
		{class: SyncFailurePolicy, message: "Source synchronization is not permitted by the current policy."},
		{class: SyncFailurePermanent, message: "Source returned an unsupported or invalid representation."},
		{class: SyncFailureInvalidInput, message: "Source returned an unsupported or invalid representation."},
		{class: SyncFailureCancelled, message: "Source synchronization was cancelled or exceeded its deadline."},
		{class: SyncFailureTransient, message: "Source synchronization did not complete within its safe bounds."},
		{class: SyncFailureDependency, message: "Source synchronization did not complete within its safe bounds."},
		{class: SyncFailureInternal, message: "Source synchronization did not complete within its safe bounds."},
		{class: SyncFailureClass("future_class"), message: "Source synchronization did not complete within its safe bounds."},
	}
	for _, test := range safeMessages {
		if actual := safeSyncFailureMessage(&SyncError{Class: test.class}); actual != test.message {
			t.Fatalf("safe message for %q = %q, want %q", test.class, actual, test.message)
		}
	}
}

func automaticRegistryManager(t *testing.T, outbound Fetcher) (*MemoryStore, *Manager, context.Context, Source, *clock.Fake) {
	t.Helper()
	store := NewMemoryStore()
	fakeClock := clock.NewFake(time.Date(2026, 9, 1, 18, 0, 0, 0, time.UTC))
	manager, err := NewManager(store, outbound, fakeClock, id.Generator{})
	if err != nil {
		t.Fatal(err)
	}
	tenantID := "00000000-0000-7000-8000-000000000001"
	source := Source{ID: "00000000-0000-7000-8000-000000000010", TenantID: tenantID, DisplayName: "Registry", Type: SourceRegistry, Endpoint: "https://catalog.example/ard.json", RegistryListURL: "https://registry.example/ard/v1/agents", Status: SourceActive, ProfileID: "ard-0.91-aa3e598", RetrievalMode: RetrievalIndexed, Scope: Scope{Kinds: []catalog.Kind{catalog.KindAgent}}, UpdateMode: UpdateAutomatic, PolicyID: "test-policy", PolicyRevision: 1, PolicyDigest: federationTestPolicyDigest, Revision: 1, CreatedBy: "admin", CreatedAt: fakeClock.Now(), UpdatedAt: fakeClock.Now()}
	if _, err := store.CreateSource(context.Background(), source); err != nil {
		t.Fatal(err)
	}
	ctx := identityfixture.WithActor(context.Background(), identity.Actor{TenantID: tenantID, Subject: "admin", Roles: []identity.Role{identity.RoleSourceAdmin}})
	return store, manager, ctx, source, fakeClock
}

func containsObservationIdentity(items []Observation, identifier, identityDigest string, freshness Freshness) bool {
	for _, item := range items {
		if item.Identifier == identifier && item.IdentityDigest == identityDigest && item.Freshness == freshness {
			return true
		}
	}
	return false
}

func FuzzRegistryListPage(f *testing.F) {
	for _, seed := range []string{
		`{"items":[],"total":0}`,
		`{"items":[{"identifier":"urn:air:example.com:default:one"}],"total":2,"pageToken":"next"}`,
		`{"items":null}`,
		`{"items":[],"items":[]}`,
	} {
		f.Add([]byte(seed), uint8(registryListPageSize))
	}
	f.Fuzz(func(t *testing.T, body []byte, requested uint8) {
		pageSize := int(requested)
		if pageSize == 0 {
			pageSize = 1
		}
		page, err := decodeRegistryListPage(body, pageSize)
		if err != nil {
			return
		}
		if page.Items == nil || len(page.Items) > pageSize {
			t.Fatalf("successful Registry List decode violated its item bound: %#v", page)
		}
		if page.Total != nil && *page.Total < 0 {
			t.Fatalf("successful Registry List decode returned a negative total: %#v", page)
		}
		if page.TokenPresent && (page.PageToken == "" || len(page.PageToken) > maxRegistryPageToken || strings.IndexFunc(page.PageToken, unicode.IsControl) >= 0) {
			t.Fatalf("successful Registry List decode returned an unsafe token: %#v", page)
		}
	})
}
