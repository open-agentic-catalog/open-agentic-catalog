package federation

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/open-agentic-catalog/open-agentic-catalog/test/identityfixture"
	"net/http"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/adapter"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/clock"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/fetcher"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/id"
	"github.com/open-agentic-catalog/open-agentic-catalog/schemas"
)

const federationTestPolicyDigest = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func TestRetainedFederationEvidenceRejectsObsoleteAndCorruptState(t *testing.T) {
	now := time.Date(2026, time.September, 4, 12, 0, 0, 0, time.UTC)
	inspection := Inspection{
		ID: "00000000-0000-7000-8000-000000000010", TenantID: "00000000-0000-7000-8000-000000000001",
		RequestedURL: "https://source.example/.well-known/ard.json", ResolvedURL: "https://source.example/catalog.json",
		SourceType: SourceARDManifest, ProfileID: "ard-test", MediaType: "application/ard+json", BodyDigest: federationTestPolicyDigest,
		EntryCount: 2, KindCounts: map[string]int{"agent": 1, "skill": 1}, Publishers: []string{"source.example"}, Tags: []string{"agent", "search"}, Capabilities: map[string]bool{"enumerable": true},
		PolicyID: "test-policy", PolicyRevision: 1, PolicyDigest: federationTestPolicyDigest, CreatedAt: now, ExpiresAt: now.Add(15 * time.Minute),
	}
	err := ValidateRetainedInspectionIntegrity(inspection)
	if err != nil {
		t.Fatalf("current inspection = %v", err)
	}
	legacy := inspection
	legacy.PolicyID, legacy.PolicyDigest, legacy.PolicyRevision = "", "", 0
	if err := ValidateRetainedInspectionIntegrity(legacy); err == nil {
		t.Fatalf("legacy inspection = %v", err)
	}
	invalid := inspection
	invalid.Publishers = []string{"z.example", "a.example"}
	if err := ValidateRetainedInspectionIntegrity(invalid); err == nil {
		t.Fatal("unsorted retained inspection evidence was accepted")
	}
	for name, mutate := range map[string]func(*Inspection){
		"envelope":  func(value *Inspection) { value.RequestedURL = "http://source.example/catalog.json" },
		"type":      func(value *Inspection) { value.SourceType = SourceType("unknown") },
		"access":    func(value *Inspection) { value.RegistryListURL = "http://source.example/v0/servers" },
		"aggregate": func(value *Inspection) { value.Publishers = []string{"one", "two", "three"} },
		"kind":      func(value *Inspection) { value.KindCounts = map[string]int{"agent": 3} },
		"policy":    func(value *Inspection) { value.PolicyRevision = 0 },
	} {
		t.Run("invalid inspection "+name, func(t *testing.T) {
			candidate := inspection
			mutate(&candidate)
			if err := ValidateRetainedInspectionIntegrity(candidate); err == nil {
				t.Fatal("invalid retained inspection was accepted")
			}
		})
	}

	completed := now.Add(time.Minute)
	snapshot := Snapshot{ID: "00000000-0000-7000-8000-000000000020", TenantID: inspection.TenantID, SourceID: "00000000-0000-7000-8000-000000000021", SourceRevision: 2, State: SnapshotComplete, EnumerationComplete: true, BodyDigest: federationTestPolicyDigest, PolicyID: "test-policy", PolicyRevision: 1, PolicyDigest: federationTestPolicyDigest, MemberCount: 2, Added: 1, Updated: 1, StartedAt: now, CompletedAt: &completed}
	if err := ValidateRetainedSnapshotIntegrity(snapshot); err != nil {
		t.Fatalf("current snapshot = %v", err)
	}
	legacySnapshot := snapshot
	legacySnapshot.SourceRevision, legacySnapshot.PolicyRevision = 0, 0
	legacySnapshot.PolicyID, legacySnapshot.PolicyDigest = "", ""
	if err := ValidateRetainedSnapshotIntegrity(legacySnapshot); err == nil {
		t.Fatalf("legacy snapshot = %v", err)
	}
	invalidSnapshot := snapshot
	invalidSnapshot.BodyDigest = "invalid"
	if err := ValidateRetainedSnapshotIntegrity(invalidSnapshot); err == nil {
		t.Fatal("invalid retained snapshot digest was accepted")
	}
	for name, mutate := range map[string]func(*Snapshot){
		"envelope": func(value *Snapshot) { value.ID = "" },
		"running": func(value *Snapshot) {
			value.State = SnapshotRunning
		},
		"complete": func(value *Snapshot) { value.CompletedAt = nil },
		"incomplete": func(value *Snapshot) {
			value.State = SnapshotIncomplete
		},
		"state": func(value *Snapshot) { value.State = SnapshotState("unknown") },
		"timing": func(value *Snapshot) {
			completedBefore := value.StartedAt.Add(-time.Second)
			value.CompletedAt = &completedBefore
		},
		"retrieval": func(value *Snapshot) {
			value.Retrievals = []RetrievalEvidence{{}}
		},
	} {
		t.Run("invalid snapshot "+name, func(t *testing.T) {
			candidate := snapshot
			mutate(&candidate)
			if err := ValidateRetainedSnapshotIntegrity(candidate); err == nil {
				t.Fatal("invalid retained snapshot was accepted")
			}
		})
	}
	for _, state := range []SnapshotState{SnapshotRunning, SnapshotIncomplete, SnapshotFailed} {
		candidate := snapshot
		candidate.State = state
		candidate.EnumerationComplete = false
		candidate.WithdrawalAuthority = false
		if state == SnapshotRunning {
			candidate.CompletedAt = nil
		}
		if state == SnapshotFailed {
			candidate.BodyDigest = ""
		}
		if err := ValidateRetainedSnapshotIntegrity(candidate); err != nil {
			t.Fatalf("valid retained %s snapshot = %v", state, err)
		}
	}
}

func TestRetainedChangePreviewIntegrityBindsAggregateEvidence(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, time.September, 4, 12, 0, 0, 0, time.UTC)
	preview := ChangePreview{
		ID:                   "00000000-0000-7000-8000-000000000010",
		TenantID:             "00000000-0000-7000-8000-000000000001",
		SourceID:             "00000000-0000-7000-8000-000000000002",
		SourceRevision:       3,
		Intent:               ChangeUpdate,
		CurrentMatched:       5,
		ProposedMatched:      3,
		ExcludedObservations: 2,
		SendsQueriesUpstream: true,
		ConfigurationDigest:  federationTestPolicyDigest,
		ObservationSetDigest: "sha256:" + strings.Repeat("b", 64),
		CreatedAt:            now,
		ExpiresAt:            now.Add(time.Hour),
	}
	digestInput, err := json.Marshal(map[string]any{"id": preview.ID, "tenantId": preview.TenantID, "sourceId": preview.SourceID, "revision": preview.SourceRevision, "intent": preview.Intent, "configurationDigest": preview.ConfigurationDigest, "observationSetDigest": preview.ObservationSetDigest, "currentMatched": preview.CurrentMatched, "proposedMatched": preview.ProposedMatched, "expiresAt": preview.ExpiresAt})
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(digestInput)
	preview.Digest = "sha256:" + hex.EncodeToString(digest[:])
	if err := ValidateChangePreviewIntegrity(preview); err != nil {
		t.Fatalf("valid retained preview returned %v", err)
	}
	// The original immutable document omitted the additive artwork count.
	retained, err := json.Marshal(preview)
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]json.RawMessage
	if err = json.Unmarshal(retained, &document); err != nil {
		t.Fatal(err)
	}
	delete(document, "artworkOverridesAffected")
	retained, err = json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	var original ChangePreview
	if err = json.Unmarshal(retained, &original); err != nil {
		t.Fatal(err)
	}
	if err = ValidateChangePreviewIntegrity(original); err != nil {
		t.Fatalf("unchanged original preview history rejected: %v", err)
	}
	positive := preview
	positive.ArtworkOverridesAffected = 2
	positiveInput, err := json.Marshal(map[string]any{"id": positive.ID, "tenantId": positive.TenantID, "sourceId": positive.SourceID, "revision": positive.SourceRevision, "intent": positive.Intent, "configurationDigest": positive.ConfigurationDigest, "observationSetDigest": positive.ObservationSetDigest, "currentMatched": positive.CurrentMatched, "proposedMatched": positive.ProposedMatched, "artworkOverridesAffected": 2, "expiresAt": positive.ExpiresAt})
	if err != nil {
		t.Fatal(err)
	}
	positiveHash := sha256.Sum256(positiveInput)
	positive.Digest = "sha256:" + hex.EncodeToString(positiveHash[:])
	if err = ValidateChangePreviewIntegrity(positive); err != nil {
		t.Fatal(err)
	}
	for _, count := range []int{0, 1, 3} {
		tampered := positive
		tampered.ArtworkOverridesAffected = count
		if err = ValidateChangePreviewIntegrity(tampered); catalog.ErrorCodeOf(err) != catalog.CodeDependencyUnavailable {
			t.Fatalf("positive count tamper %d admitted: %v", count, err)
		}
	}
	previewWithInventedCount := preview
	previewWithInventedCount.ArtworkOverridesAffected = 1
	if err = ValidateChangePreviewIntegrity(previewWithInventedCount); catalog.ErrorCodeOf(err) != catalog.CodeDependencyUnavailable {
		t.Fatalf("zero count tamper admitted: %v", err)
	}

	invalidDigest := preview
	invalidDigest.Digest = federationTestPolicyDigest
	if invalidDigest.Digest == preview.Digest {
		invalidDigest.Digest = "sha256:" + strings.Repeat("c", 64)
	}
	if err := ValidateChangePreviewIntegrity(invalidDigest); catalog.ErrorCodeOf(err) != catalog.CodeDependencyUnavailable {
		t.Fatalf("tampered preview digest returned %v", err)
	}
	for name, mutate := range map[string]func(*ChangePreview){
		"missing identity": func(value *ChangePreview) { value.ID = "" },
		"revision":         func(value *ChangePreview) { value.SourceRevision = 0 },
		"intent":           func(value *ChangePreview) { value.Intent = ChangeIntent("unknown") },
		"expiry":           func(value *ChangePreview) { value.ExpiresAt = value.CreatedAt },
		"counts":           func(value *ChangePreview) { value.ExcludedObservations++ },
		"digest format":    func(value *ChangePreview) { value.ObservationSetDigest = "invalid" },
		"remove query": func(value *ChangePreview) {
			value.Intent = ChangeRemove
			value.ProposedMatched = 0
			value.ExcludedObservations = value.CurrentMatched
			value.SendsQueriesUpstream = true
		},
	} {
		t.Run(name, func(t *testing.T) {
			candidate := preview
			mutate(&candidate)
			if err := ValidateChangePreviewIntegrity(candidate); catalog.ErrorCodeOf(err) != catalog.CodeInvalidArgument {
				t.Fatalf("invalid retained preview returned %v", err)
			}
		})
	}
}

type sequenceFetcher struct {
	mu      sync.Mutex
	results []fetcher.Result
	errors  []error
}

type recordingObservationAdopter struct {
	called bool
	input  catalog.ExternalObservationAdoptionInput
}

func (a *recordingObservationAdopter) AdoptExternalObservation(_ context.Context, input catalog.ExternalObservationAdoptionInput) (catalog.ExternalObservationAdoption, error) {
	a.called = true
	a.input = input
	return catalog.ExternalObservationAdoption{}, nil
}

type routingFetcher struct {
	mu       sync.Mutex
	results  map[string]fetcher.Result
	requests []fetcher.Request
}

type invalidPolicyFetcher struct{}

func (invalidPolicyFetcher) Fetch(context.Context, fetcher.Request) (fetcher.Result, error) {
	return fetcher.Result{}, errors.New("unexpected fetch")
}

func (invalidPolicyFetcher) PolicyDigest() string { return "invalid" }

func (f *routingFetcher) Fetch(_ context.Context, request fetcher.Request) (fetcher.Result, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests = append(f.requests, request)
	key := request.Method + " " + request.URL
	if request.Method == "" {
		key = "GET " + request.URL
	}
	result, ok := f.results[key]
	if !ok {
		return fetcher.Result{}, errors.New("unconfigured request: " + key)
	}
	return withFederationTestEvidence(result, request), nil
}

func (f *routingFetcher) PolicyDigest() string { return federationTestPolicyDigest }

func (f *sequenceFetcher) Fetch(_ context.Context, request fetcher.Request) (fetcher.Result, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.results) == 0 {
		return fetcher.Result{}, errors.New("no response configured")
	}
	result := f.results[0]
	f.results = f.results[1:]
	var err error
	if len(f.errors) > 0 {
		err = f.errors[0]
		f.errors = f.errors[1:]
	}
	return withFederationTestEvidence(result, request), err
}

func (f *sequenceFetcher) PolicyDigest() string { return federationTestPolicyDigest }

func withFederationTestEvidence(result fetcher.Result, request fetcher.Request) fetcher.Result {
	if result.Status != http.StatusNotModified && result.BodyDigest == "" {
		digest := sha256.Sum256(result.Body)
		result.BodyDigest = "sha256:" + hex.EncodeToString(digest[:])
	}
	if result.PolicyID == "" {
		result.PolicyID = "test-policy"
	}
	if result.PolicyRevision == 0 {
		result.PolicyRevision = 1
	}
	if result.PolicyDigest == "" {
		result.PolicyDigest = federationTestPolicyDigest
	}
	result.NetworkProfile = request.NetworkProfile
	return result
}

func federationResult(body string) fetcher.Result {
	sum := sha256.Sum256([]byte(body))
	return fetcher.Result{Status: 200, FinalURL: "https://catalog.remote.example/.well-known/ard.json", MediaType: "application/ard+json", Body: []byte(body), BodyDigest: "sha256:" + hex.EncodeToString(sum[:])}
}

func TestFederationAccessAndManagerOptionsFailClosed(t *testing.T) {
	t.Parallel()

	firstEvidenceError := errors.New("first evidence failure")
	access := newSourceAccess("tenant-1", "private-network", "partner-reader", "https://catalog.example.com/root")
	request := access.request("https://catalog.example.com/entry", fetcher.Request{})
	if request.TenantID != "tenant-1" || request.NetworkProfile != "private-network" || request.CredentialBinding != "partner-reader" {
		t.Fatalf("same-origin source access = %#v", request)
	}
	request = access.request("https://other.example.com/entry", fetcher.Request{})
	if request.CredentialBinding != "" {
		t.Fatalf("credential crossed source origin: %#v", request)
	}
	access.recordOperationError(nil)
	access.recordOperationError(firstEvidenceError)
	access.recordOperationError(errors.New("later evidence failure"))
	if !errors.Is(access.operationError(), firstEvidenceError) {
		t.Fatalf("source access evidence error = %v", access.operationError())
	}
	if err := (sourceAccess{}).operationError(); err != nil {
		t.Fatalf("empty source access returned %v", err)
	}
	if origin := normalizedOrigin("not a URL"); origin != "" {
		t.Fatalf("invalid normalized origin = %q", origin)
	}
	if approved := accessForSource(Source{TenantID: "tenant-1"}, true); approved.expectedPolicyDigest != "unapproved-source-policy" {
		t.Fatalf("missing source policy digest = %q", approved.expectedPolicyDigest)
	}

	for _, references := range []struct {
		profile    string
		credential string
	}{
		{profile: "Invalid"},
		{profile: "private_underscore"},
		{credential: "unsafe/reference"},
		{credential: "."},
	} {
		if err := validateAccessReferences(references.profile, references.credential); catalog.ErrorCodeOf(err) != catalog.CodeInvalidArgument {
			t.Fatalf("invalid access references %#v returned %v", references, err)
		}
	}

	store := NewMemoryStore()
	serviceClock := clock.NewFake(time.Now())
	if _, err := NewManager(store, invalidPolicyFetcher{}, serviceClock, id.Generator{}); catalog.ErrorCodeOf(err) != catalog.CodeInvalidArgument {
		t.Fatalf("invalid policy digest returned %v", err)
	}
	for name, option := range map[string]ManagerOption{
		"nil option":    nil,
		"fanout":        WithExecutionLimits(0, 0),
		"hops":          WithExecutionLimits(1, MaxReferralHops+1),
		"pages floor":   WithLiveSearchPages(0),
		"pages ceiling": WithLiveSearchPages(MaxLivePagesPerSource + 1),
		"timeout":       WithLiveSearchTimeout(MaxLiveSearchTimeout + time.Nanosecond),
		"target": WithDiscoveryTargets([]DiscoveryTarget{{
			Host:     "catalog.example.com",
			TenantID: "tenant-1",
			Audience: DiscoveryAudience("invalid"),
		}}),
		"duplicate target": WithDiscoveryTargets([]DiscoveryTarget{
			{Host: "catalog.example.com", TenantID: "tenant-1", Audience: AudienceAnonymous},
			{Host: " CATALOG.EXAMPLE.COM ", TenantID: "tenant-1", Audience: AudienceAuthenticated},
		}),
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if _, err := NewManager(store, &sequenceFetcher{}, serviceClock, id.Generator{}, option); catalog.ErrorCodeOf(err) != catalog.CodeInvalidArgument {
				t.Fatalf("invalid manager option returned %v", err)
			}
		})
	}
}

func TestCompleteReconciliationAndFailedSnapshotSafety(t *testing.T) {
	first := `{"entries":[{"identifier":"urn:air:remote.example:default:one","displayName":"One","type":"application/a2a-agent-card+json","data":{"name":"One"},"tags":["selected"]},{"identifier":"urn:air:remote.example:default:two","displayName":"Two","type":"application/agent-skills+json","data":{"name":"Two"},"tags":["selected"]}]}`
	second := `{"entries":[{"identifier":"urn:air:remote.example:default:one","displayName":"One updated","type":"application/a2a-agent-card+json","data":{"name":"One updated"},"tags":["selected"]}]}`
	fetches := &sequenceFetcher{results: []fetcher.Result{federationResult(first), federationResult(first), federationResult(second), {}, federationResult(second)}, errors: []error{nil, nil, nil, errors.New("upstream unavailable")}}
	store := NewMemoryStore()
	fakeClock := clock.NewFake(time.Date(2026, 8, 29, 9, 0, 0, 0, time.UTC))
	manager, err := NewManager(store, fetches, fakeClock, id.Generator{})
	if err != nil {
		t.Fatal(err)
	}
	actor := identity.Actor{TenantID: "00000000-0000-7000-8000-000000000001", Subject: "source-admin", Roles: []identity.Role{identity.RoleSourceAdmin}}
	ctx := identityfixture.WithActor(context.Background(), actor)
	inspection, err := manager.Inspect(ctx, "https://catalog.remote.example/.well-known/ard.json")
	if err != nil {
		t.Fatal(err)
	}
	source, err := manager.Connect(ctx, ConnectInput{InspectionID: inspection.ID, DisplayName: "Remote", Scope: Scope{Tags: []string{"selected"}}, UpdateMode: UpdateAutomatic, Reason: "Approve the inspected source"})
	if err != nil {
		t.Fatal(err)
	}
	firstSnapshot, err := manager.Sync(ctx, source.ID)
	if err != nil || firstSnapshot.Added != 2 || !firstSnapshot.WithdrawalAuthority {
		t.Fatalf("first sync: %#v, %v", firstSnapshot, err)
	}
	fakeClock.Advance(time.Minute)
	secondSnapshot, err := manager.Sync(ctx, source.ID)
	if err != nil || secondSnapshot.Updated != 1 || secondSnapshot.Withdrawn != 1 {
		t.Fatalf("second sync: %#v, %v", secondSnapshot, err)
	}
	current, err := manager.ObservationPage(ctx, ObservationListRequest{SourceID: source.ID, CurrentOnly: true, Limit: 100})
	if err != nil || len(current) != 1 || current[0].Identifier != "urn:air:remote.example:default:one" || !current[0].RawEntryAvailable || len(current[0].RawEntry) != 0 {
		t.Fatalf("current observations: %#v, %v", current, err)
	}
	rawCurrent, err := manager.GetObservation(ctx, current[0].ID)
	if err != nil || len(rawCurrent.RawEntry) == 0 {
		t.Fatalf("current exact evidence lost: %v", err)
	}
	all, err := manager.ObservationPage(ctx, ObservationListRequest{SourceID: source.ID, Limit: 100})
	if err != nil || len(all) != 3 {
		t.Fatalf("historical observations: %#v, %v", all, err)
	}
	if !slices.ContainsFunc(all, func(observation Observation) bool {
		return observation.Identifier == "urn:air:remote.example:default:two" && observation.Freshness == FreshWithdrawn
	}) {
		t.Fatalf("absent authoritative member was not withdrawn: %#v", all)
	}
	fakeClock.Advance(time.Minute)
	failed, err := manager.Sync(ctx, source.ID)
	if err == nil || failed.State != SnapshotFailed || failed.WithdrawalAuthority {
		t.Fatalf("failed sync: %#v, %v", failed, err)
	}
	current, err = manager.ObservationPage(ctx, ObservationListRequest{SourceID: source.ID, CurrentOnly: true, Limit: 100})
	if err != nil || len(current) != 1 {
		t.Fatalf("failed sync withdrew current observations: %#v, %v", current, err)
	}
	retried, err := manager.SyncAtRevision(ctx, source.ID, source.Revision)
	if err != nil || retried.State != SnapshotComplete {
		t.Fatalf("retry after transient failure: %#v, %v", retried, err)
	}
	current, err = manager.ObservationPage(ctx, ObservationListRequest{SourceID: source.ID, CurrentOnly: true, Limit: 100})
	if err != nil || len(current) != 1 {
		t.Fatalf("retry current observation: %#v, %v", current, err)
	}
	occurrences, total, err := manager.ObservationOccurrences(ctx, current[0].ID, 10)
	if err != nil || total != 2 || len(occurrences) != 2 || occurrences[0].SnapshotID != retried.ID || occurrences[1].SnapshotID != secondSnapshot.ID {
		t.Fatalf("repeated snapshot occurrences: %#v total %d, %v", occurrences, total, err)
	}
	defaulted, total, err := manager.ObservationOccurrences(ctx, current[0].ID, 0)
	if err != nil || total != 2 || len(defaulted) != 2 {
		t.Fatalf("default occurrence bound: %#v total %d, %v", defaulted, total, err)
	}
	if _, _, err := manager.ObservationOccurrences(context.Background(), current[0].ID, 10); catalog.ErrorCodeOf(err) != catalog.CodeForbidden {
		t.Fatalf("unauthenticated occurrence read returned %v", err)
	}
	if _, _, err := manager.ObservationOccurrences(ctx, "00000000-0000-7000-8000-000000000099", 10); catalog.ErrorCodeOf(err) != catalog.CodeNotFound {
		t.Fatalf("missing occurrence parent returned %v", err)
	}
	recoveredSource, err := manager.GetSource(ctx, source.ID)
	if err != nil || recoveredSource.Status != SourceActive || recoveredSource.Revision != source.Revision || recoveredSource.LastError != "" {
		t.Fatalf("source after retry: %#v, %v", recoveredSource, err)
	}
	if _, err = manager.SetStatus(ctx, source.ID, SourceRemoved, source.Revision+2, StatusChangeInput{Reason: "Remove the source", ChangePreviewDigest: "sha256:" + strings.Repeat("a", 64)}); catalog.ErrorCodeOf(err) != catalog.CodePreconditionFailed {
		t.Fatalf("stale source revision returned %v", err)
	}
}

func TestReconciliationCountsConflictingExactEntriesAsSets(t *testing.T) {
	initial := `{"entries":[{"identifier":"urn:air:remote.example:default:one","displayName":"One A","type":"application/a2a-agent-card+json","data":{"name":"A"}},{"identifier":"urn:air:remote.example:default:one","displayName":"One B","type":"application/a2a-agent-card+json","data":{"name":"B"}}]}`
	changed := `{"entries":[{"identifier":"urn:air:remote.example:default:one","displayName":"One A","type":"application/a2a-agent-card+json","data":{"name":"A"}},{"identifier":"urn:air:remote.example:default:one","displayName":"One C","type":"application/a2a-agent-card+json","data":{"name":"C"}}]}`
	fetches := &sequenceFetcher{results: []fetcher.Result{federationResult(initial), federationResult(initial), federationResult(changed)}}
	store := NewMemoryStore()
	serviceClock := clock.NewFake(time.Date(2026, 9, 1, 9, 0, 0, 0, time.UTC))
	manager, err := NewManager(store, fetches, serviceClock, id.Generator{})
	if err != nil {
		t.Fatal(err)
	}
	ctx := identityfixture.WithActor(context.Background(), identity.Actor{TenantID: "00000000-0000-7000-8000-000000000001", Subject: "source-admin", Roles: []identity.Role{identity.RoleSourceAdmin}})
	inspection, err := manager.Inspect(ctx, "https://catalog.remote.example/.well-known/ard.json")
	if err != nil {
		t.Fatal(err)
	}
	source, err := manager.Connect(ctx, ConnectInput{InspectionID: inspection.ID, DisplayName: "Conflicted", Scope: Scope{Kinds: []catalog.Kind{catalog.KindAgent}}, UpdateMode: UpdateAutomatic, Reason: "Approve the inspected source"})
	if err != nil {
		t.Fatal(err)
	}
	first, err := manager.Sync(ctx, source.ID)
	if err != nil || first.Added != 2 || first.Updated != 0 {
		t.Fatalf("initial conflicting snapshot = %#v, %v", first, err)
	}
	serviceClock.Advance(time.Minute)
	second, err := manager.Sync(ctx, source.ID)
	if err != nil || second.Added != 0 || second.Updated != 1 || second.Withdrawn != 0 {
		t.Fatalf("set-based conflicting snapshot = %#v, %v", second, err)
	}
	current, err := manager.ObservationPage(ctx, ObservationListRequest{SourceID: source.ID, CurrentOnly: true, Limit: 100})
	if err != nil || len(current) != 2 {
		t.Fatalf("current exact conflict members = %#v, %v", current, err)
	}
}

func TestObservationGroupPreservesPerSourceConflictAndBlocksAdoption(t *testing.T) {
	store := NewMemoryStore()
	now := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	tenantID := "00000000-0000-7000-8000-000000000001"
	source := Source{ID: "00000000-0000-7000-8000-000000000010", TenantID: tenantID, DisplayName: "Conflicted", Endpoint: "https://source.example/ard.json", Status: SourceActive, Revision: 1, CreatedAt: now, UpdatedAt: now}
	if _, err := store.CreateSource(context.Background(), source); err != nil {
		t.Fatal(err)
	}
	completed := now.Add(time.Second)
	snapshot := Snapshot{ID: "00000000-0000-7000-8000-000000000020", TenantID: tenantID, SourceID: source.ID, SourceRevision: 1, State: SnapshotComplete, EnumerationComplete: true, WithdrawalAuthority: true, MemberCount: 2, StartedAt: now, CompletedAt: &completed}
	identifier := "urn:air:source.example:default:agent"
	makeObservation := func(idValue, variant, normalizedVariant, location string) Observation {
		raw := json.RawMessage(`{"identifier":"` + identifier + `","variant":"` + variant + `"}`)
		normalized := json.RawMessage(`{"profile":"catalog-observation-v1","identifier":"` + identifier + `","versionPresent":true,"version":"1","kind":"agent","mediaType":"application/a2a-agent-card+json","displayName":"","description":"","url":"","data":null,"tags":null,"representativeQueries":[],"metadata":null,"unknownProperties":{"variant":"` + normalizedVariant + `"}}`)
		exactDigest := observationDigest(raw)
		normalizedDigest := observationDigest(normalized)
		item := Observation{IdentityDigest: exactDigest, ID: idValue, TenantID: tenantID, SourceID: source.ID, SnapshotID: snapshot.ID, SourceKey: sourceScopedObservationKey(identifier, "1", true, "", "", ""), Identifier: identifier, Version: "1", VersionPresent: true, Kind: catalog.KindAgent, MediaType: "application/a2a-agent-card+json", Completeness: CompleteEntry, Compatibility: Compatibility{Structural: "valid", ProfileID: "ard-test", Support: "supported"}, RawEntry: raw, NormalizedEntry: normalized, RawEntryAvailable: true, ExactEntryDigest: exactDigest, NormalizedEntryDigest: normalizedDigest, NormalizationProfile: observationNormalizationProfile, SearchPublisher: "source.example", Freshness: FreshCurrent, FirstSeenAt: now, LastSeenAt: now}
		item.Occurrence = ObservationOccurrence{ObservationID: item.ID, SourceID: source.ID, SnapshotID: snapshot.ID, SourceRevision: 1, RetrievalDigest: "sha256:" + strings.Repeat("f", 64), EntryLocation: location, ExactEntryDigest: exactDigest, NormalizedEntryDigest: normalizedDigest, EvidenceComplete: true, ObservedAt: now}
		return item
	}
	first := makeObservation("00000000-0000-7000-8000-000000000030", "one", "same", "/entries/0")
	second := makeObservation("00000000-0000-7000-8000-000000000031", "two", "different", "/entries/1")
	source.LastCompleteSnapshotID = snapshot.ID
	if _, _, err := store.CommitSync(context.Background(), snapshot, []Observation{second, first}, source, 1); err != nil {
		t.Fatal(err)
	}
	secondSource := source
	secondSource.ID = "00000000-0000-7000-8000-000000000011"
	secondSource.DisplayName = "Equivalent"
	secondSource.Endpoint = "https://equivalent.example/ard.json"
	if _, err := store.CreateSource(context.Background(), secondSource); err != nil {
		t.Fatal(err)
	}
	secondSnapshot := snapshot
	secondSnapshot.ID = "00000000-0000-7000-8000-000000000021"
	secondSnapshot.SourceID = secondSource.ID
	secondSnapshot.MemberCount = 1
	equivalent := first
	equivalent.ID = "00000000-0000-7000-8000-000000000032"
	equivalent.SourceID = secondSource.ID
	equivalent.SnapshotID = secondSnapshot.ID
	equivalent.RawEntry = json.RawMessage(`{"variant":"equivalent","identifier":"` + identifier + `"}`)
	equivalent.ExactEntryDigest = observationDigest(equivalent.RawEntry)
	equivalent.IdentityDigest = equivalent.ExactEntryDigest
	equivalent.Occurrence.ObservationID = equivalent.ID
	equivalent.Occurrence.SourceID = secondSource.ID
	equivalent.Occurrence.SnapshotID = secondSnapshot.ID
	equivalent.Occurrence.EntryLocation = "/entries/2"
	equivalent.Occurrence.ExactEntryDigest = equivalent.ExactEntryDigest
	secondSource.LastCompleteSnapshotID = secondSnapshot.ID
	if _, _, err := store.CommitSync(context.Background(), secondSnapshot, []Observation{equivalent}, secondSource, 1); err != nil {
		t.Fatal(err)
	}
	if first.SourceKey != equivalent.SourceKey || first.SourceID == equivalent.SourceID {
		t.Fatal("equivalent identifier/version contributions must retain equal opaque keys and distinct source identities")
	}
	adopter := &recordingObservationAdopter{}
	manager, err := NewManager(store, &sequenceFetcher{}, clock.NewFake(now), id.Generator{}, WithObservationAdopter(adopter))
	if err != nil {
		t.Fatal(err)
	}
	ctx := identityfixture.WithActor(context.Background(), identity.Actor{TenantID: tenantID, Subject: "source-admin", Roles: []identity.Role{identity.RoleSourceAdmin, identity.RoleContributor}})
	group, err := manager.ObservationGroup(ctx, identifier, "1", true)
	if err != nil || !group.Conflict || len(group.Members) != 2 || group.Members[0].NormalizedEntryDigest >= group.Members[1].NormalizedEntryDigest {
		t.Fatalf("conflicted observation group = %#v, %v", group, err)
	}
	contributions := make(map[string]ObservationGroupContribution)
	memberSizes := make([]int, 0, len(group.Members))
	for _, member := range group.Members {
		memberSizes = append(memberSizes, len(member.Contributions))
		for _, contribution := range member.Contributions {
			contributions[contribution.Observation.ID] = contribution
		}
	}
	slices.Sort(memberSizes)
	if !slices.Equal(memberSizes, []int{1, 2}) || !contributions[first.ID].SourceConflict || !contributions[second.ID].SourceConflict || contributions[equivalent.ID].SourceConflict {
		t.Fatalf("group contribution conflicts = %#v", group.Members)
	}
	_, err = manager.AdoptObservation(ctx, first.ID, AdoptInput{SnapshotID: snapshot.ID, RetrievalDigest: first.Occurrence.RetrievalDigest, EntryLocation: first.Occurrence.EntryLocation, NamespaceID: "namespace", PublisherID: "publisher"})
	if catalog.ErrorCodeOf(err) != catalog.CodeConflict || adopter.called {
		t.Fatalf("conflicted source adoption = called %t, %v", adopter.called, err)
	}
}

func TestAdoptionChecksSourceConflictBeyondBoundedGroupProjection(t *testing.T) {
	store := NewMemoryStore()
	now := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
	tenantID := "00000000-0000-7000-8000-000000000001"
	identifier := "urn:air:source.example:default:bounded-conflict"
	version := "1"
	dummyNormalized := json.RawMessage(`{"profile":"catalog-observation-v1","identifier":"` + identifier + `","versionPresent":true,"version":"1","kind":"agent","mediaType":"application/a2a-agent-card+json","displayName":"","description":"","url":"","data":null,"tags":null,"representativeQueries":[],"metadata":null,"unknownProperties":{}}`)
	dummyDigest := observationDigest(dummyNormalized)
	for index := 1; index <= 100; index++ {
		sourceID := fmt.Sprintf("00000000-0000-7000-8000-%012d", index)
		observationID := fmt.Sprintf("00000000-0000-7000-8001-%012d", index)
		snapshotID := fmt.Sprintf("00000000-0000-7000-8002-%012d", index)
		observation := Observation{ID: observationID, TenantID: tenantID, SourceID: sourceID, SnapshotID: snapshotID, SourceKey: sourceScopedObservationKey(identifier, version, true, "", "", ""), Identifier: identifier, Version: version, VersionPresent: true, Kind: catalog.KindAgent, Completeness: CompleteEntry, NormalizedEntryDigest: dummyDigest, Freshness: FreshCurrent}
		occurrence := ObservationOccurrence{ObservationID: observationID, SourceID: sourceID, SnapshotID: snapshotID, RetrievalDigest: "sha256:" + strings.Repeat("a", 64), EntryLocation: "/entries/0", ExactEntryDigest: "sha256:" + strings.Repeat("b", 64), NormalizedEntryDigest: dummyDigest, EvidenceComplete: true, ObservedAt: now}
		store.observations[observationKey(observation)] = observation
		store.occurrences[occurrenceKey(tenantID, occurrence)] = occurrence
	}

	selectedSourceID := "00000000-0000-7000-8009-000000000999"
	selectedSnapshotID := "00000000-0000-7000-8010-000000000001"
	selectedID := "00000000-0000-7000-8011-000000000001"
	conflictingID := "00000000-0000-7000-8011-000000000002"
	makeSelected := func(idValue, variant, location string) Observation {
		raw := json.RawMessage(`{"identifier":"` + identifier + `","variant":"` + variant + `"}`)
		normalized := json.RawMessage(`{"profile":"catalog-observation-v1","identifier":"` + identifier + `","versionPresent":true,"version":"1","kind":"agent","mediaType":"application/a2a-agent-card+json","displayName":"Bounded conflict","description":"","url":"","data":{"variant":"` + variant + `"},"tags":null,"representativeQueries":[],"metadata":null,"unknownProperties":{}}`)
		item := Observation{IdentityDigest: observationDigest(raw), ID: idValue, TenantID: tenantID, SourceID: selectedSourceID, SnapshotID: selectedSnapshotID, SourceKey: sourceScopedObservationKey(identifier, version, true, "", "", ""), Identifier: identifier, Version: version, VersionPresent: true, Kind: catalog.KindAgent, MediaType: adapter.MediaA2AAgentCard, Completeness: CompleteEntry, Compatibility: Compatibility{Structural: "valid", ProfileID: "ard-test", Support: "supported"}, RawEntry: raw, NormalizedEntry: normalized, RawEntryAvailable: true, ExactEntryDigest: observationDigest(raw), NormalizedEntryDigest: observationDigest(normalized), NormalizationProfile: observationNormalizationProfile, SearchPublisher: "source.example", Freshness: FreshCurrent, FirstSeenAt: now, LastSeenAt: now}
		item.Occurrence = ObservationOccurrence{ObservationID: idValue, SourceID: selectedSourceID, SnapshotID: selectedSnapshotID, SourceRevision: 1, RetrievalDigest: "sha256:" + strings.Repeat("c", 64), EntryLocation: location, ExactEntryDigest: item.ExactEntryDigest, NormalizedEntryDigest: item.NormalizedEntryDigest, EvidenceComplete: true, ObservedAt: now}
		return item
	}
	store.sources[selectedSourceID] = Source{ID: selectedSourceID, TenantID: tenantID, Revision: 1, Scope: Scope{Identifiers: []string{identifier}}}

	selected := makeSelected(selectedID, "selected", "/entries/0")
	conflicting := makeSelected(conflictingID, "conflicting", "/entries/1")
	selectedOccurrence := selected.Occurrence
	conflictingOccurrence := conflicting.Occurrence
	selected.Occurrence = ObservationOccurrence{}
	conflicting.Occurrence = ObservationOccurrence{}
	store.observations[observationKey(selected)] = selected
	store.observations[observationKey(conflicting)] = conflicting
	store.occurrences[occurrenceKey(tenantID, selectedOccurrence)] = selectedOccurrence
	store.occurrences[occurrenceKey(tenantID, conflictingOccurrence)] = conflictingOccurrence
	completed := now.Add(time.Second)
	store.snapshots[selectedSnapshotID] = Snapshot{ID: selectedSnapshotID, TenantID: tenantID, SourceID: selectedSourceID, SourceRevision: 1, State: SnapshotComplete, EnumerationComplete: true, MemberCount: 2, StartedAt: now, CompletedAt: &completed}

	adopter := &recordingObservationAdopter{}
	manager, err := NewManager(store, &sequenceFetcher{}, clock.NewFake(now), id.Generator{}, WithObservationAdopter(adopter))
	if err != nil {
		t.Fatal(err)
	}
	ctx := identityfixture.WithActor(context.Background(), identity.Actor{TenantID: tenantID, Subject: "source-admin", Roles: []identity.Role{identity.RoleSourceAdmin, identity.RoleContributor}})
	group, err := manager.ObservationGroup(ctx, identifier, version, true)
	if err != nil || !group.ContributionsTruncated {
		t.Fatalf("bounded observation group = %#v, %v", group, err)
	}
	for _, member := range group.Members {
		for _, contribution := range member.Contributions {
			if contribution.Observation.SourceID == selectedSourceID {
				t.Fatal("selected conflict unexpectedly appeared inside the bounded group projection")
			}
		}
	}

	_, err = manager.AdoptObservation(ctx, selectedID, AdoptInput{SnapshotID: selectedSnapshotID, RetrievalDigest: selectedOccurrence.RetrievalDigest, EntryLocation: selectedOccurrence.EntryLocation, NamespaceID: "namespace", PublisherID: "publisher"})
	if catalog.ErrorCodeOf(err) != catalog.CodeConflict || adopter.called {
		t.Fatalf("conflict outside bounded group projection = called %t, %v", adopter.called, err)
	}
}

func TestAdoptObservationPreservesImmutableSourceEvidence(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		kind      catalog.Kind
		versioned bool
	}{{catalog.KindAgent, true}, {catalog.KindPlugin, true}, {catalog.KindAgent, false}} {
		t.Run(fmt.Sprintf("%s/versioned=%t", test.kind, test.versioned), func(t *testing.T) {
			kind := test.kind

			const (
				tenantID   = "00000000-0000-7000-8000-000000000001"
				sourceID   = "00000000-0000-7000-8000-000000000010"
				snapshotID = "00000000-0000-7000-8000-000000000020"
				itemID     = "00000000-0000-7000-8000-000000000030"
				identifier = "urn:air:source.example:default:adoptable-agent"
			)
			now := time.Date(2026, time.September, 1, 12, 0, 0, 0, time.UTC)
			store := NewMemoryStore()
			source := Source{ID: sourceID, TenantID: tenantID, DisplayName: "Adoptable", Endpoint: "https://source.example/ard.json", Status: SourceActive, Revision: 1, CreatedAt: now, UpdatedAt: now}
			if _, err := store.CreateSource(context.Background(), source); err != nil {
				t.Fatal(err)
			}
			entry := sourceEntry{Identifier: identifier, Version: "1.2.3", VersionPresent: true, Type: adapter.MediaA2AAgentCard, DisplayName: "Adoptable agent", Description: "Govern this observed agent locally.", URL: "https://source.example/agent.json", Data: json.RawMessage(`{"name":"Adoptable agent"}`), Tags: []string{"observed"}, RepresentativeQueries: []string{"Find an adoptable agent"}}
			if !test.versioned {
				entry.Version, entry.VersionPresent = "", false
			}
			if kind == catalog.KindPlugin {
				entry.Type = catalog.PluginMediaZIP
				entry.URL = "https://source.example/plugin.zip"
				entry.Data = nil
				entry.Metadata = json.RawMessage(`{"packageDigest":"sha256:` + strings.Repeat("a", 64) + `","packageSize":500}`)
			}
			normalized, normalizedDigest := normalizeObservationEntry(entry, identifier, kind)
			if len(normalized) == 0 || normalizedDigest == "" {
				t.Fatal("valid observation did not normalize")
			}
			raw := json.RawMessage(`{"identifier":"urn:air:source.example:default:adoptable-agent","version":"1.2.3","displayName":"Adoptable agent","description":"Govern this observed agent locally.","type":"application/a2a-agent-card+json","url":"https://source.example/agent.json","data":{"name":"Adoptable agent"},"tags":["observed"],"representativeQueries":["Find an adoptable agent"]}`)
			if kind == catalog.KindPlugin {
				raw = json.RawMessage(`{"identifier":"urn:air:source.example:default:adoptable-agent","version":"1.2.3","displayName":"Adoptable agent","description":"Govern this observed agent locally.","type":"application/agent-plugins+zip","url":"https://source.example/plugin.zip","metadata":{"packageDigest":"sha256:` + strings.Repeat("a", 64) + `","packageSize":500},"tags":["observed"],"representativeQueries":["Find an adoptable agent"]}`)
			}
			if !test.versioned {
				raw = json.RawMessage(strings.Replace(string(raw), `,"version":"1.2.3"`, "", 1))
			}
			exactDigest := observationDigest(raw)
			retrievalDigest := "sha256:" + strings.Repeat("f", 64)
			completed := now.Add(time.Second)
			snapshot := Snapshot{ID: snapshotID, TenantID: tenantID, SourceID: sourceID, SourceRevision: 1, State: SnapshotComplete, EnumerationComplete: true, WithdrawalAuthority: true, MemberCount: 1, Added: 1, StartedAt: now, CompletedAt: &completed}
			observation := Observation{IdentityDigest: exactDigest,
				ID: itemID, TenantID: tenantID, SourceID: sourceID, SnapshotID: snapshotID,
				SourceKey: sourceScopedObservationKey(identifier, entry.Version, entry.VersionPresent, "", "", ""), Identifier: identifier, Version: entry.Version, VersionPresent: entry.VersionPresent,
				Kind: kind, MediaType: entry.Type, Completeness: CompleteEntry,
				Compatibility: Compatibility{Structural: "valid", ProfileID: "ard-test", Support: "supported"}, RawEntry: raw, NormalizedEntry: normalized, RawEntryAvailable: true,
				ExactEntryDigest: exactDigest, NormalizedEntryDigest: normalizedDigest, NormalizationProfile: observationNormalizationProfile,
				SearchDisplayName: entry.DisplayName, SearchDescription: entry.Description, SearchTags: []string{"observed"}, SearchQueries: []string{"Find an adoptable agent"}, SearchPublisher: "source.example",
				Freshness: FreshCurrent, FirstSeenAt: now, LastSeenAt: now,
			}
			observation.Occurrence = ObservationOccurrence{ObservationID: itemID, SourceID: sourceID, SnapshotID: snapshotID, SourceRevision: 1, RetrievalDigest: retrievalDigest, EntryLocation: "/entries/0", ExactEntryDigest: exactDigest, NormalizedEntryDigest: normalizedDigest, EvidenceComplete: true, ObservedAt: now}
			source.LastCompleteSnapshotID = snapshotID
			if _, _, err := store.CommitSync(context.Background(), snapshot, []Observation{observation}, source, 1); err != nil {
				t.Fatal(err)
			}

			adopter := &recordingObservationAdopter{}
			outbound := &controlledFetcher{digest: federationTestPolicyDigest}
			manager, err := NewManager(store, outbound, clock.NewFake(now), id.Generator{}, WithObservationAdopter(adopter))
			if err != nil {
				t.Fatal(err)
			}
			ctx := identityfixture.WithActor(context.Background(), identity.Actor{TenantID: tenantID, Subject: "source-admin", Roles: []identity.Role{identity.RoleSourceAdmin, identity.RoleContributor}})
			beforeAdoption, err := store.GetObservation(ctx, tenantID, itemID)
			if err != nil {
				t.Fatal(err)
			}
			// Freeze buffers before the handoff; metadata JSON omits these
			// retained documents and private search fields.
			beforeAdoption.RawEntry = slices.Clone(beforeAdoption.RawEntry)
			beforeAdoption.NormalizedEntry = slices.Clone(beforeAdoption.NormalizedEntry)
			beforeAdoption.SearchTags = slices.Clone(beforeAdoption.SearchTags)
			beforeAdoption.SearchQueries = slices.Clone(beforeAdoption.SearchQueries)
			input := AdoptInput{SnapshotID: snapshotID, RetrievalDigest: retrievalDigest, EntryLocation: "/entries/0", NamespaceID: "namespace", PublisherID: "publisher", License: "Apache-2.0", Visibility: catalog.VisibilityPrivate}
			if !test.versioned {
				input.Version = " 1.2.3 "
			}
			if _, err := manager.AdoptObservation(ctx, itemID, input); err != nil {
				t.Fatal(err)
			}
			if !adopter.called || adopter.input.Version != "1.2.3" || adopter.input.DisplayName != entry.DisplayName || adopter.input.Description != entry.Description || adopter.input.Representation.URL != entry.URL || string(adopter.input.Representation.Data) != string(entry.Data) || adopter.input.Provenance.ExactEntryDigest != exactDigest || adopter.input.Provenance.NormalizedEntryDigest != normalizedDigest {
				t.Fatalf("adoption handoff = called %t, %#v", adopter.called, adopter.input)
			}
			if kind == catalog.KindPlugin && (adopter.input.Representation.ExpectedDigest == nil || *adopter.input.Representation.ExpectedDigest != "sha256:"+strings.Repeat("a", 64) || adopter.input.Representation.ProfileID != catalog.PluginProfile) {
				t.Fatal("Plugin adoption lost the archive checksum or selected the discovery profile as a package profile")
			}
			wantProvenance := catalog.AdoptionProvenance{ObservationID: itemID, SourceID: sourceID, SourceRevision: 1, SnapshotID: snapshotID, RetrievalDigest: retrievalDigest, EntryLocation: "/entries/0", ExactEntryDigest: exactDigest, NormalizedEntryDigest: normalizedDigest, NormalizationProfile: observationNormalizationProfile}
			if !reflect.DeepEqual(adopter.input.Provenance, wantProvenance) || adopter.input.CanonicalIdentifier != identifier || adopter.input.Kind != kind || adopter.input.Representation.Role != catalog.RepresentationPrimary || adopter.input.NamespaceID != "namespace" || adopter.input.PublisherID != "publisher" || adopter.input.License != "Apache-2.0" || adopter.input.Visibility != catalog.VisibilityPrivate || !reflect.DeepEqual(adopter.input.Tags, []string{"observed"}) || !reflect.DeepEqual(adopter.input.RepresentativeQueries, []string{"Find an adoptable agent"}) {
				t.Fatalf("adoption lost selected occurrence or local authoring fields: %+v", adopter.input)
			}
			input.DisplayName, input.Description = " Local name ", " Local description "
			input.Categories = []string{"operations"}
			input.DocumentationURL, input.SupportURL, input.TermsURL = "https://local.example/docs", "https://local.example/support", "https://local.example/terms"
			adopter.called = false
			if _, err := manager.AdoptObservation(ctx, itemID, input); err != nil || !adopter.called || adopter.input.DisplayName != "Local name" || adopter.input.Description != "Local description" || !reflect.DeepEqual(adopter.input.Categories, []string{"operations"}) || adopter.input.DocumentationURL != input.DocumentationURL || adopter.input.SupportURL != input.SupportURL || adopter.input.TermsURL != input.TermsURL || !reflect.DeepEqual(adopter.input.Provenance, wantProvenance) {
				t.Fatalf("local overrides changed immutable provenance: %+v, %v", adopter.input, err)
			}

			adopter.called = false
			input.Version = "2.0.0"
			if !test.versioned {
				input.Version = " "
			}
			if _, err := manager.AdoptObservation(ctx, itemID, input); catalog.ErrorCodeOf(err) != catalog.CodeInvalidArgument || adopter.called {
				t.Fatalf("mismatched local version = called %t, %v", adopter.called, err)
			}
			afterAdoption, err := store.GetObservation(ctx, tenantID, itemID)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(afterAdoption, beforeAdoption) || len(outbound.requests) != 0 {
				t.Fatalf("adoption changed retained source evidence or fetched remote content: %v", err)
			}
			// A captured current-policy decision cannot outlive a source edit.
			guard := adopter.input.AdmitCommit
			guardContext := identity.WithPermissionContext(ctx)
			if _, err := permissionActor(guardContext, "source.read_evidence"); err != nil {
				t.Fatal(err)
			}
			committed := false
			if guard == nil || adopter.input.SourceAdmissionRevision != 1 {
				t.Fatal("current policy admission missing")
			}
			if err := guard(guardContext, func(context.Context) error { committed = true; return nil }); err != nil || !committed {
				t.Fatal("allowed commit denied", err)
			}
			currentSource := store.sources[sourceID]
			editedSource := currentSource
			editedSource.Revision++
			editedSource.Scope.ExcludedIdentifiers = []string{identifier}
			store.sources[sourceID] = editedSource
			committed = false
			if err := guard(guardContext, func(context.Context) error { committed = true; return nil }); catalog.ErrorCodeOf(err) != catalog.CodePreconditionFailed || committed {
				t.Fatal("stale policy committed", err)
			}
			adopter.called = false
			input.Version = "1.2.3"
			if _, err := manager.AdoptObservation(ctx, itemID, input); catalog.ErrorCodeOf(err) != catalog.CodePreconditionFailed || adopter.called {
				t.Fatal("excluded adoption reached artifact preparation", err)
			}
			editedSource.Revision++
			editedSource.Scope.ExcludedIdentifiers = nil
			store.sources[sourceID] = editedSource
			if _, err := manager.AdoptObservation(ctx, itemID, input); err != nil || adopter.input.SourceAdmissionRevision != editedSource.Revision || adopter.input.Provenance.SourceRevision != 1 {
				t.Fatal("older eligible provenance rejected", err)
			}
			store.sources[sourceID] = currentSource
			adopter.called = false
			storedKey := observationKey(observation)
			broken := store.observations[storedKey]
			broken.NormalizedEntry = json.RawMessage(`{`)
			store.observations[storedKey] = broken
			input.Version = ""
			if !test.versioned {
				input.Version = "1.2.3"
			}
			if _, err := manager.AdoptObservation(ctx, itemID, input); catalog.ErrorCodeOf(err) != catalog.CodeDependencyUnavailable || adopter.called {
				t.Fatalf("invalid normalized evidence = called %t, %v", adopter.called, err)
			}
		})
	}
}

func TestInspectionResolvesAdvertisedARDLocation(t *testing.T) {
	manifest := `{"specVersion":"1.0","entries":[{"identifier":"urn:air:remote.example:default:one","displayName":"One","type":"application/a2a-agent-card+json","data":{"name":"One"}}]}`
	advertised := federationResultAt("https://remote.example/alternate-ard.json", manifest)
	advertised.MediaType = "application/json"
	fetches := &routingFetcher{results: map[string]fetcher.Result{
		"GET https://remote.example/.well-known/ard.json": {Status: http.StatusNotFound, FinalURL: "https://remote.example/.well-known/ard.json", MediaType: "application/json"},
		"GET https://remote.example":                      {Status: 200, FinalURL: "https://remote.example/", MediaType: "text/html", Body: []byte(`<html><head><link rel="alternate ard" type="application/ard+json" href="/.well-known/ard.json"></head></html>`), Links: []string{`</alternate-ard.json>; rel="alternate ard"; type="application/ard+json"`}},
		"GET https://remote.example/alternate-ard.json":   advertised,
	}}
	manager, err := NewManager(NewMemoryStore(), fetches, clock.NewFake(time.Date(2026, 8, 29, 9, 0, 0, 0, time.UTC)), id.Generator{})
	if err != nil {
		t.Fatal(err)
	}
	ctx := identityfixture.WithActor(context.Background(), identity.Actor{TenantID: "00000000-0000-7000-8000-000000000001", Subject: "source-admin", Roles: []identity.Role{identity.RoleSourceAdmin}})
	inspection, err := manager.Inspect(ctx, "remote.example")
	if err != nil {
		t.Fatal(err)
	}
	if inspection.ResolvedURL != "https://remote.example/alternate-ard.json" || inspection.SourceType != SourceARDManifest || inspection.ProfileID != "ard-0.91-aa3e598" || inspection.EntryCount != 1 || !hasFinding(inspection.Warnings, "ARD_DISCOVERY_RESOLVED") || !hasFinding(inspection.Warnings, "ARD_REPRESENTATIVE_QUERIES_RECOMMENDED") {
		t.Fatalf("inspection = %#v", inspection)
	}
	if len(fetches.requests) != 3 || fetches.requests[0].URL != "https://remote.example/.well-known/ard.json" || fetches.requests[1].URL != "https://remote.example" || fetches.requests[2].URL != "https://remote.example/alternate-ard.json" {
		t.Fatalf("requests = %#v", fetches.requests)
	}
}

func TestInspectionRejectsContradictoryAdvertisedProfile(t *testing.T) {
	pageURL := "https://remote.example/catalog"
	manifestURL := "https://remote.example/manifest.json"
	manifest := federationResultAt(manifestURL, `{"specVersion":"1.0","entries":[]}`)
	manifest.MediaType = aiCatalogDocumentMediaType
	fetches := &routingFetcher{results: map[string]fetcher.Result{
		"GET " + pageURL: {Status: http.StatusOK, FinalURL: pageURL, MediaType: "text/html", Body: []byte(`<html></html>`), Links: []string{`</manifest.json>; rel="ard"`}},
		"GET https://remote.example/.well-known/ard.json":        {Status: http.StatusNotFound, FinalURL: "https://remote.example/.well-known/ard.json", MediaType: "application/json"},
		"GET " + manifestURL:                                     manifest,
		"GET https://remote.example/.well-known/ai-catalog.json": {Status: http.StatusNotFound, FinalURL: "https://remote.example/.well-known/ai-catalog.json", MediaType: "application/json"},
	}}
	manager, err := NewManager(NewMemoryStore(), fetches, clock.NewFake(time.Date(2026, 9, 2, 8, 30, 0, 0, time.UTC)), id.Generator{})
	if err != nil {
		t.Fatal(err)
	}
	ctx := identityfixture.WithActor(context.Background(), identity.Actor{TenantID: "00000000-0000-7000-8000-000000000001", Subject: "source-admin", Roles: []identity.Role{identity.RoleSourceAdmin}})
	if _, err := manager.Inspect(ctx, pageURL); catalog.ErrorCodeOf(err) != catalog.CodeInvalidArgument {
		t.Fatalf("contradictory advertised profile returned %v", err)
	}
}

func TestInspectionRejectsConflictingRelationsForOneLocation(t *testing.T) {
	pageURL := "https://remote.example/catalog"
	manifestURL := "https://remote.example/manifest.json"
	fetches := &routingFetcher{results: map[string]fetcher.Result{
		"GET " + pageURL: {Status: http.StatusOK, FinalURL: pageURL, MediaType: "text/html", Body: []byte(`<html></html>`), Links: []string{`</manifest.json>; rel="ard"`, `</manifest.json>; rel="ai-catalog"; type="application/ai-catalog+json"`}},
		"GET https://remote.example/.well-known/ard.json":        {Status: http.StatusNotFound, FinalURL: "https://remote.example/.well-known/ard.json", MediaType: "application/json"},
		"GET https://remote.example/.well-known/ai-catalog.json": {Status: http.StatusNotFound, FinalURL: "https://remote.example/.well-known/ai-catalog.json", MediaType: "application/json"},
	}}
	manager, err := NewManager(NewMemoryStore(), fetches, clock.NewFake(time.Date(2026, 9, 2, 8, 45, 0, 0, time.UTC)), id.Generator{})
	if err != nil {
		t.Fatal(err)
	}
	ctx := identityfixture.WithActor(context.Background(), identity.Actor{TenantID: "00000000-0000-7000-8000-000000000001", Subject: "source-admin", Roles: []identity.Role{identity.RoleSourceAdmin}})
	if _, err := manager.Inspect(ctx, pageURL); catalog.ErrorCodeOf(err) != catalog.CodeInvalidArgument {
		t.Fatalf("conflicting advertised relations returned %v", err)
	}
	for _, request := range fetches.requests {
		if request.URL == manifestURL {
			t.Fatalf("ambiguous discovery location was fetched: %#v", fetches.requests)
		}
	}
}

func TestDomainInspectionAlwaysTriesNormativeARDWellKnown(t *testing.T) {
	rootManifest := `{"entries":[{"identifier":"urn:air:remote.example:default:root","displayName":"Root","type":"application/a2a-agent-card+json","data":{"name":"Root"}}]}`
	wellKnownManifest := `{"specVersion":"1.0","host":{"displayName":"Transport metadata"},"entries":[{"identifier":"urn:air:remote.example:default:well-known","displayName":"Well Known","type":"application/a2a-agent-card+json","data":{"name":"Well Known"}}]}`
	wellKnown := federationResultAt("https://remote.example/.well-known/ard.json", wellKnownManifest)
	wellKnown.MediaType = "application/json"
	fetches := &routingFetcher{results: map[string]fetcher.Result{
		"GET https://remote.example":                      federationResultAt("https://remote.example/", rootManifest),
		"GET https://remote.example/.well-known/ard.json": wellKnown,
	}}
	manager, err := NewManager(NewMemoryStore(), fetches, clock.NewFake(time.Date(2026, 9, 2, 9, 0, 0, 0, time.UTC)), id.Generator{})
	if err != nil {
		t.Fatal(err)
	}
	ctx := identityfixture.WithActor(context.Background(), identity.Actor{TenantID: "00000000-0000-7000-8000-000000000001", Subject: "source-admin", Roles: []identity.Role{identity.RoleSourceAdmin}})
	inspection, err := manager.Inspect(ctx, "https://remote.example")
	if err != nil {
		t.Fatal(err)
	}
	if inspection.ResolvedURL != "https://remote.example/.well-known/ard.json" || inspection.SourceType != SourceARDManifest || inspection.ProfileID != "ard-0.91-aa3e598" || inspection.EntryCount != 1 {
		t.Fatalf("well-known inspection = %#v", inspection)
	}
	if len(fetches.requests) != 1 || fetches.requests[0].URL != "https://remote.example/.well-known/ard.json" {
		t.Fatalf("domain resolution requests = %#v", fetches.requests)
	}
}

func TestDomainInspectionTriesRequestedWellKnownBeforeFollowingRootRedirect(t *testing.T) {
	manifest := `{"entries":[{"identifier":"urn:air:remote.example:default:root","displayName":"Root","type":"application/a2a-agent-card+json","data":{"name":"Root"}}]}`
	fetches := &routingFetcher{results: map[string]fetcher.Result{
		"GET https://remote.example/.well-known/ard.json":        {Status: http.StatusNotFound, FinalURL: "https://remote.example/.well-known/ard.json", MediaType: "application/json"},
		"GET https://remote.example":                             federationResultAt("https://redirected.example/", manifest),
		"GET https://remote.example/.well-known/ai-catalog.json": {Status: http.StatusNotFound, FinalURL: "https://remote.example/.well-known/ai-catalog.json", MediaType: "application/json"},
	}}
	manager, err := NewManager(NewMemoryStore(), fetches, clock.NewFake(time.Date(2026, 9, 2, 9, 0, 0, 0, time.UTC)), id.Generator{})
	if err != nil {
		t.Fatal(err)
	}
	ctx := identityfixture.WithActor(context.Background(), identity.Actor{TenantID: "00000000-0000-7000-8000-000000000001", Subject: "source-admin", Roles: []identity.Role{identity.RoleSourceAdmin}})
	inspection, err := manager.Inspect(ctx, "https://remote.example")
	if err != nil {
		t.Fatal(err)
	}
	if inspection.ResolvedURL != "https://redirected.example/" || inspection.SourceType != SourceARDManifest {
		t.Fatalf("redirected-domain inspection = %#v", inspection)
	}
	if len(fetches.requests) != 3 || fetches.requests[0].URL != "https://remote.example/.well-known/ard.json" || fetches.requests[1].URL != "https://remote.example" || fetches.requests[2].URL != "https://remote.example/.well-known/ai-catalog.json" {
		t.Fatalf("redirected-domain requests = %#v", fetches.requests)
	}
}

func TestInspectionFallsBackToSingleEmbeddedARDEntry(t *testing.T) {
	pageURL := "https://remote.example/catalog"
	htmlBody := `<html><head><script type="application/ld+json">{"@context":"https://agenticresourcediscovery.org/context/v1","identifier":"urn:air:remote.example:default:one","displayName":"One","type":"application/a2a-agent-card+json","data":{"name":"One"}}</script></head></html>`
	fetches := &routingFetcher{results: map[string]fetcher.Result{
		"GET " + pageURL: {Status: 200, FinalURL: pageURL, MediaType: "text/html", Body: []byte(htmlBody)},
	}}
	manager, err := NewManager(NewMemoryStore(), fetches, clock.NewFake(time.Date(2026, 8, 29, 9, 0, 0, 0, time.UTC)), id.Generator{})
	if err != nil {
		t.Fatal(err)
	}
	ctx := identityfixture.WithActor(context.Background(), identity.Actor{TenantID: "00000000-0000-7000-8000-000000000001", Subject: "source-admin", Roles: []identity.Role{identity.RoleSourceAdmin}})
	inspection, err := manager.Inspect(ctx, pageURL)
	if err != nil {
		t.Fatal(err)
	}
	if inspection.ResolvedURL != pageURL || inspection.EntryCount != 1 || !hasFinding(inspection.Warnings, "ARD_DISCOVERED_IN_HTML") {
		t.Fatalf("inspection = %#v", inspection)
	}
}

func TestInspectionResolvesRegistryOperationalBase(t *testing.T) {
	manifestURL := "https://registry.remote.example/.well-known/ard.json"
	manifest := `{"entries":[{"identifier":"urn:air:remote.example:registry:public","displayName":"Remote Registry","type":"application/ai-registry+json","url":"https://registry.remote.example/ard/v1/","representativeQueries":["Find agents","Discover tools"]}]}`
	fetches := &routingFetcher{results: map[string]fetcher.Result{
		"GET " + manifestURL: federationResultAt(manifestURL, manifest),
	}}
	manager, err := NewManager(NewMemoryStore(), fetches, clock.NewFake(time.Date(2026, 8, 29, 9, 0, 0, 0, time.UTC)), id.Generator{})
	if err != nil {
		t.Fatal(err)
	}
	ctx := identityfixture.WithActor(context.Background(), identity.Actor{TenantID: "00000000-0000-7000-8000-000000000001", Subject: "source-admin", Roles: []identity.Role{identity.RoleSourceAdmin}})
	inspection, err := manager.Inspect(ctx, manifestURL)
	if err != nil {
		t.Fatal(err)
	}
	if inspection.RegistrySearchURL != "https://registry.remote.example/ard/v1/search" || !inspection.Capabilities["search"] || inspection.SourceType != SourceRegistry {
		t.Fatalf("registry inspection = %#v", inspection)
	}
}

func TestInspectionDoesNotSilentlyChooseAmongRegistries(t *testing.T) {
	manifestURL := "https://catalog.remote.example/.well-known/ard.json"
	manifest := `{"entries":[{"identifier":"urn:air:remote.example:registry:first","displayName":"First Registry","type":"application/ai-registry+json","url":"https://first.registry.example/ard/v1/"},{"identifier":"urn:air:remote.example:registry:second","displayName":"Second Registry","type":"application/ai-registry","url":"https://second.registry.example/search"}]}`
	fetches := &routingFetcher{results: map[string]fetcher.Result{
		"GET " + manifestURL: federationResultAt(manifestURL, manifest),
	}}
	manager, err := NewManager(NewMemoryStore(), fetches, clock.NewFake(time.Date(2026, 8, 29, 9, 0, 0, 0, time.UTC)), id.Generator{})
	if err != nil {
		t.Fatal(err)
	}
	ctx := identityfixture.WithActor(context.Background(), identity.Actor{TenantID: "00000000-0000-7000-8000-000000000001", Subject: "source-admin", Roles: []identity.Role{identity.RoleSourceAdmin}})
	inspection, err := manager.Inspect(ctx, manifestURL)
	if err != nil {
		t.Fatal(err)
	}
	if inspection.RegistrySearchURL != "" || inspection.Capabilities["search"] || !hasFinding(inspection.Warnings, "ARD_MULTIPLE_REGISTRY_ENDPOINTS") || !hasFinding(inspection.Warnings, "ARD_REGISTRY_MEDIA_TYPE_ALIAS") {
		t.Fatalf("ambiguous Registry inspection = %#v", inspection)
	}
}

func TestSourceChangePreviewUpdateTestAndRunHistory(t *testing.T) {
	body := `{"entries":[{"identifier":"urn:air:remote.example:default:one","displayName":"One","type":"application/a2a-agent-card+json","data":{"name":"One"},"tags":["selected"]},{"identifier":"urn:air:remote.example:default:two","displayName":"Two","type":"application/agent-skills+json","data":{"name":"Two"},"tags":["selected"]}]}`
	fetches := &sequenceFetcher{results: []fetcher.Result{federationResult(body), federationResult(body), federationResult(body)}}
	store := NewMemoryStore()
	manager, err := NewManager(store, fetches, clock.NewFake(time.Date(2026, 8, 29, 9, 0, 0, 0, time.UTC)), id.Generator{})
	if err != nil {
		t.Fatal(err)
	}
	ctx := identityfixture.WithActor(context.Background(), identity.Actor{TenantID: "00000000-0000-7000-8000-000000000001", Subject: "source-admin", Roles: []identity.Role{identity.RoleSourceAdmin}})
	inspection, err := manager.Inspect(ctx, "https://catalog.remote.example/.well-known/ard.json")
	if err != nil {
		t.Fatal(err)
	}
	source, err := manager.Connect(ctx, ConnectInput{InspectionID: inspection.ID, DisplayName: "Remote", Scope: Scope{Tags: []string{"selected"}}, UpdateMode: UpdateAutomatic, Reason: "Approve the inspected source"})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := manager.Sync(ctx, source.ID)
	if err != nil {
		t.Fatal(err)
	}
	source, err = manager.GetSource(ctx, source.ID)
	if err != nil {
		t.Fatal(err)
	}
	scope := Scope{Identifiers: []string{"urn:air:remote.example:default:one"}}
	patchInput := PatchInput{Intent: ChangeUpdate, Scope: &scope}
	preview, err := manager.PreviewChange(ctx, source.ID, patchInput)
	if err != nil || preview.CurrentMatched != 2 || preview.ProposedMatched != 1 || preview.ExcludedObservations != 1 {
		t.Fatalf("preview = %#v, %v", preview, err)
	}
	patchInput.ChangePreviewDigest = "sha256:wrong"
	patchInput.Reason = "Apply the reviewed scope change"
	if _, err := manager.UpdateSource(ctx, source.ID, patchInput, source.Revision); catalog.ErrorCodeOf(err) != catalog.CodePreconditionFailed {
		t.Fatalf("wrong preview digest returned %v", err)
	}
	patchInput.ChangePreviewDigest = preview.Digest
	updated, err := manager.UpdateSource(ctx, source.ID, patchInput, source.Revision)
	if err != nil || updated.Revision != source.Revision+1 || len(updated.Scope.Identifiers) != 1 {
		t.Fatalf("updated source = %#v, %v", updated, err)
	}
	testResult, err := manager.TestSource(ctx, source.ID)
	if err != nil || testResult.Status != "succeeded" || testResult.EntryCount != 2 {
		t.Fatalf("source test = %#v, %v", testResult, err)
	}
	runs, err := manager.ListSnapshots(ctx, source.ID, 10)
	if err != nil || len(runs) != 1 || runs[0].ID != snapshot.ID {
		t.Fatalf("runs = %#v, %v", runs, err)
	}
	observations, err := manager.ObservationPage(ctx, ObservationListRequest{SourceID: source.ID, CurrentOnly: true, Limit: 100})
	if err != nil || len(observations) != 2 {
		t.Fatalf("observations = %#v, %v", observations, err)
	}
	observation, err := manager.GetObservation(ctx, observations[0].ID)
	if err != nil || observation.ID != observations[0].ID {
		t.Fatalf("observation = %#v, %v", observation, err)
	}
}

func TestLiveSearchPreservesARDSourceProvenance(t *testing.T) {
	registryURL := "https://registry.remote.example/ard/v1/search"
	body := `{"results":[{"identifier":"urn:air:remote.example:default:agent","version":"1.0.0","displayName":"Remote Agent","type":"application/a2a-agent-card+json","url":"https://agents.remote.example/v1/card.json","score":91,"source":"https://first.registry.example/"},{"identifier":"urn:air:remote.example:default:agent","version":"2.0.0","displayName":"Remote Agent","type":"application/a2a-agent-card+json","url":"https://agents.remote.example/v2/card.json","score":92,"source":"https://first.registry.example/"},{"identifier":"urn:air:remote.example:default:agent","version":"1.0.0","type":"application/a2a-agent-card+json","score":93,"source":"https://second.registry.example/"},{"identifier":"urn:air:remote.example:default:missing-score","source":"https://first.registry.example/"},{"identifier":"not-an-air-identifier","score":99,"source":"https://first.registry.example/"}],"pageToken":"opaque-upstream-token"}`
	store := NewMemoryStore()
	fetches := &routingFetcher{results: map[string]fetcher.Result{
		"POST " + registryURL: {Status: 200, FinalURL: registryURL, MediaType: "application/json", Body: []byte(body)},
	}}
	manager, err := NewManager(store, fetches, clock.NewFake(time.Date(2026, 8, 29, 9, 0, 0, 0, time.UTC)), id.Generator{})
	if err != nil {
		t.Fatal(err)
	}
	tenantID := "00000000-0000-7000-8000-000000000001"
	ctx := identityfixture.WithActor(context.Background(), identity.Actor{TenantID: tenantID, Subject: "source-admin", Roles: []identity.Role{identity.RoleSourceAdmin}})
	source := Source{ID: "00000000-0000-7000-8000-000000000010", TenantID: tenantID, DisplayName: "Remote", Type: SourceRegistry, Endpoint: registryURL, RegistrySearchURL: registryURL, Status: SourceActive, ProfileID: "ard-0.91-aa3e598", RetrievalMode: RetrievalLive, Scope: Scope{Kinds: []catalog.Kind{catalog.KindAgent}}, UpdateMode: UpdateManual, Live: LivePolicy{Enabled: true, QueryPrivacyAcknowledged: true, FederationMode: "none"}, PolicyID: "test-policy", PolicyRevision: 1, PolicyDigest: federationTestPolicyDigest, Revision: 1, CreatedBy: "source-admin"}
	if _, err := store.CreateSource(ctx, source); err != nil {
		t.Fatal(err)
	}
	queryContext := map[string]any{"kind": "https://agenticresourcediscovery.org/ns#mediaType"}
	response, err := manager.LiveSearch(ctx, LiveSearchRequest{Context: queryContext, Text: "remote agent", Filter: map[string]any{"kind": "application/a2a-agent-card+json"}, PageSize: 10, Administrative: true, Reason: "Investigate remote Agent results"})
	if err != nil || len(response.Results) != 2 {
		t.Fatalf("live response = %#v, %v", response, err)
	}
	if !response.Partial {
		t.Fatal("invalid upstream ARD result did not mark live federation partial")
	}
	if !slices.ContainsFunc(response.Diagnostics, func(diagnostic LiveDiagnostic) bool { return diagnostic.Status == "continuation_unavailable" }) {
		t.Fatalf("bounded live response did not report the ignored upstream continuation: %#v", response.Diagnostics)
	}
	if response.Results[0].Version != "1.0.0" || response.Results[1].Version != "2.0.0" {
		t.Fatalf("multi-version results were collapsed, misordered, or not re-scoped: %#v", response.Results)
	}
	if response.Results[0].Source != "https://second.registry.example/" || response.Results[1].Source != "https://first.registry.example/" || response.Results[0].RespondingURL != registryURL || response.Results[1].RespondingURL != registryURL {
		t.Fatalf("live result source provenance was replaced: %#v", response.Results)
	}
	if len(fetches.requests) != 1 {
		t.Fatalf("live requests = %#v", fetches.requests)
	}
	var requestBody struct {
		Query struct {
			Context any            `json:"@context"`
			Filter  map[string]any `json:"filter"`
		} `json:"query"`
	}
	if err := json.Unmarshal(fetches.requests[0].Body, &requestBody); err != nil {
		t.Fatal(err)
	}
	contextObject, ok := requestBody.Query.Context.(map[string]any)
	if !ok || contextObject["kind"] != queryContext["kind"] || requestBody.Query.Filter["kind"] != "application/a2a-agent-card+json" {
		t.Fatalf("live query lost its JSON-LD bindings: %s", fetches.requests[0].Body)
	}
}

func TestLiveSearchEnforcesARDQueryAndResponseShape(t *testing.T) {
	registryURL := "https://registry.remote.example/ard/v1/search"
	store := NewMemoryStore()
	fetches := &routingFetcher{results: map[string]fetcher.Result{
		"POST " + registryURL: {Status: 200, FinalURL: registryURL, MediaType: "application/json", Body: []byte(`{}`)},
	}}
	manager, err := NewManager(store, fetches, clock.NewFake(time.Date(2026, 8, 29, 9, 0, 0, 0, time.UTC)), id.Generator{})
	if err != nil {
		t.Fatal(err)
	}
	tenantID := "00000000-0000-7000-8000-000000000001"
	ctx := identityfixture.WithActor(context.Background(), identity.Actor{TenantID: tenantID, Subject: "source-admin", Roles: []identity.Role{identity.RoleSourceAdmin}})
	source := Source{ID: "00000000-0000-7000-8000-000000000010", TenantID: tenantID, DisplayName: "Remote", Type: SourceRegistry, Endpoint: registryURL, RegistrySearchURL: registryURL, Status: SourceActive, ProfileID: "ard-0.91-aa3e598", RetrievalMode: RetrievalLive, Scope: Scope{Kinds: []catalog.Kind{catalog.KindAgent}}, UpdateMode: UpdateManual, Live: LivePolicy{Enabled: true, QueryPrivacyAcknowledged: true, FederationMode: "none"}, PolicyID: "test-policy", PolicyRevision: 1, PolicyDigest: federationTestPolicyDigest, Revision: 1, CreatedBy: "source-admin"}
	if _, err := store.CreateSource(ctx, source); err != nil {
		t.Fatal(err)
	}

	invalidRequests := []LiveSearchRequest{
		{Context: true, Text: "agent"},
		{Context: "relative/context", Text: "agent"},
		{Context: make(chan int), Text: "agent"},
		{Text: "agent", Filter: map[string]any{"type": 42}},
		{Text: "agent", Filter: map[string]any{"type": []any{}}},
	}
	for _, request := range invalidRequests {
		if _, err := manager.LiveSearch(ctx, request); catalog.ErrorCodeOf(err) != catalog.CodeInvalidArgument {
			t.Fatalf("invalid live request %#v returned %v", request, err)
		}
	}
	if len(fetches.requests) != 0 {
		t.Fatal("invalid live query contacted the upstream Registry")
	}

	response, err := manager.LiveSearch(ctx, LiveSearchRequest{Context: []any{"https://agenticresourcediscovery.org/context/v1"}, Text: "agent", Administrative: true, Reason: "Investigate Registry response shape"})
	if err != nil || !response.Partial || len(response.Diagnostics) != 1 || response.Diagnostics[0].Status != "invalid_response" {
		t.Fatalf("missing results member response = %#v, %v", response, err)
	}
	// The complete wire allowlist excludes the tenant, actor, administrator
	// reason and source-selection metadata, not just an absent optional filter.
	var outbound map[string]any
	if err := json.Unmarshal(fetches.requests[0].Body, &outbound); err != nil {
		t.Fatal(err)
	}
	expectedQuery := map[string]any{"federation": "none", "pageSize": float64(10), "query": map[string]any{"@context": []any{"https://agenticresourcediscovery.org/context/v1"}, "text": "agent"}}
	if !reflect.DeepEqual(outbound, expectedQuery) || len(fetches.requests[0].Headers) != 0 {
		t.Fatalf("live query disclosed unexpected fields or headers: %#v", fetches.requests[0])
	}

	resultKey := "POST " + registryURL
	for _, test := range []struct {
		name               string
		result             fetcher.Result
		configured         bool
		diagnosticStatuses []string
	}{
		{name: "duplicate response member", result: fetcher.Result{Status: 200, Body: []byte(`{"results":[],"results":[]}`)}, configured: true, diagnosticStatuses: []string{"invalid_response"}},
		{name: "malformed result item", result: fetcher.Result{Status: 200, Body: []byte(`{"results":[1]}`)}, configured: true, diagnosticStatuses: []string{"invalid_response", "succeeded"}},
		{name: "non-success status", result: fetcher.Result{Status: 503, Body: []byte(`{"results":[]}`)}, configured: true, diagnosticStatuses: []string{"failed"}},
		{name: "fetch failure", diagnosticStatuses: []string{"failed"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			fetches.mu.Lock()
			if test.configured {
				fetches.results[resultKey] = test.result
			} else {
				delete(fetches.results, resultKey)
			}
			fetches.mu.Unlock()
			response, err := manager.LiveSearch(ctx, LiveSearchRequest{Text: "agent", Administrative: true, Reason: "Investigate Registry failure handling"})
			if err != nil || !response.Partial || len(response.Results) != 0 || len(response.Diagnostics) != len(test.diagnosticStatuses) {
				t.Fatalf("live search response = %#v, %v", response, err)
			}
			for i, status := range test.diagnosticStatuses {
				if response.Diagnostics[i].Status != status {
					t.Fatalf("diagnostic %d = %q, want %q", i, response.Diagnostics[i].Status, status)
				}
			}
		})
	}
}

func TestDiscoveryDocumentsRejectAmbiguousJSONAndApplyProfileRequirements(t *testing.T) {
	if _, err := parseDocumentWithContextInputs([]byte(`{"entries":[],"entries":[]}`), "application/json", nil); catalog.ErrorCodeOf(err) != catalog.CodeInvalidArgument {
		t.Fatalf("duplicate source member returned %v", err)
	}
	manager, err := NewManager(NewMemoryStore(), &routingFetcher{results: map[string]fetcher.Result{}}, clock.NewFake(time.Date(2026, 8, 29, 9, 0, 0, 0, time.UTC)), id.Generator{})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 29, 9, 0, 0, 0, time.UTC)
	aiDocument, err := parseDocumentWithContextInputs([]byte(`{"specVersion":"1.0","entries":[{"identifier":"urn:air:remote.example:default:agent","type":"application/a2a-agent-card+json","data":{"name":"Agent"}}]}`), "application/ai-catalog+json", nil)
	if err != nil {
		t.Fatal(err)
	}
	aiObservation, err := manager.observation("tenant", Source{ID: "source", ProfileID: aiDocument.profileID}, "snapshot", aiDocument.entries[0], 0, now)
	if err != nil || aiObservation.Compatibility.Structural != "valid" || aiObservation.Compatibility.ProfileID != aiDocument.profileID {
		t.Fatalf("valid AI Catalog entry without displayName = %#v, %v", aiObservation, err)
	}
	ardDocument, err := parseDocumentWithContextInputs([]byte(`{"entries":[{"identifier":"urn:air:remote.example:default:agent","type":"application/a2a-agent-card+json","data":{"name":"Agent"}}]}`), "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	ardObservation, err := manager.observation("tenant", Source{ID: "source", ProfileID: ardDocument.profileID}, "snapshot", ardDocument.entries[0], 0, now)
	if err != nil || ardObservation.Compatibility.Structural != "invalid" {
		t.Fatalf("ARD entry without displayName = %#v, %v", ardObservation, err)
	}
	ardScalarDocument, err := parseDocumentWithContextInputs([]byte(`{"entries":[{"identifier":"urn:air:remote.example:default:agent","displayName":"Agent","type":"application/a2a-agent-card+json","data":"not-an-object"}]}`), "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	ardScalarObservation, err := manager.observation("tenant", Source{ID: "source", ProfileID: ardScalarDocument.profileID}, "snapshot", ardScalarDocument.entries[0], 0, now)
	if err != nil || ardScalarObservation.Compatibility.Structural != "invalid" {
		t.Fatalf("ARD entry with scalar data = %#v, %v", ardScalarObservation, err)
	}
	sparseARD, err := parseDocumentWithContextInputs([]byte(`{"entries":[{"identifier":"urn:air:remote.example:agent","displayName":"Agent","type":"application/a2a-agent-card+json","data":{"name":"Agent"}}]}`), "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	sparseObservation, err := manager.observation("tenant", Source{ID: "source", ProfileID: sparseARD.profileID}, "snapshot", sparseARD.entries[0], 0, now)
	if err != nil || sparseObservation.Compatibility.Structural != "valid" || !hasFinding(sparseObservation.Compatibility.Findings, "ARD_REPRESENTATIVE_QUERIES_RECOMMENDED") || !hasFinding(sparseObservation.Compatibility.Findings, "ARD_IDENTIFIER_NAMESPACE_RECOMMENDED") {
		t.Fatalf("schema-compatible sparse ARD entry = %#v, %v", sparseObservation, err)
	}
}

// TestAICatalogDocumentRejectsNullTagsBeforeProjection keeps invalid typed
// source fields from disappearing during projection decoding.
func TestAICatalogDocumentRejectsNullTagsBeforeProjection(t *testing.T) {
	t.Parallel()

	const valid = `{"specVersion":"1.0","entries":[{"identifier":"urn:air:remote.example:agent:weather","type":"application/json","data":{},"tags":[]}]}`
	document, err := parseDocumentWithContextInputs([]byte(valid), "application/ai-catalog+json", nil)
	if err != nil || document.profileID != "ai-catalog-1.0-b062278" || len(document.entries) != 1 {
		t.Fatalf("valid AI Catalog control: %#v, %v", document, err)
	}
	// Validation must run before null could be decoded as an absent tags slice.
	invalid := strings.Replace(valid, `"tags":[]`, `"tags":null`, 1)
	if _, err := parseDocumentWithContextInputs([]byte(invalid), "application/ai-catalog+json", nil); catalog.ErrorCodeOf(err) != catalog.CodeInvalidArgument || !strings.Contains(err.Error(), "tags") {
		t.Fatalf("invalid typed field must reject the source document: %v", err)
	}
}

// Explicit AI Catalog validation preserves exact source evidence and never
// converts alternate identifiers into normalized native search records.
func TestAICatalogNamespacesPreserveEvidenceAndProfiles(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)
	manager, err := NewManager(NewMemoryStore(), &routingFetcher{results: map[string]fetcher.Result{}}, clock.NewFake(now), id.Generator{})
	if err != nil {
		t.Fatal(err)
	}
	for _, identifier := range []string{"urn:air:example.com:agent:weather", "local-weather", "https://example.com/weather"} {
		t.Run(identifier, func(t *testing.T) {
			const extensions = `{"com.example":{"future":9007199254740993,"nullable":null,"items":[null,true]}}`
			rawEntry := []byte(fmt.Sprintf(`{ "identifier":%q,"displayName":"Weather","type":"application/a2a-agent-card+json","data":{},"extensions":%s }`, identifier, extensions))
			body := []byte(`{"specVersion":"1.0","host":{"displayName":"Example Catalog"},"entries":[` + string(rawEntry) + `]}`)
			before := bytes.Clone(body)
			noFetch := func(uri string) ([]byte, string, error) {
				t.Fatalf("namespace validation fetched %q", uri)
				return nil, "", nil
			}
			document, err := parseDocumentWithContextInputs(body, aiCatalogDocumentMediaType, noFetch)
			if err != nil || document.sourceType != SourceAICatalog || document.profileID != "ai-catalog-1.0-b062278" || len(document.entries) != 1 {
				t.Fatalf("explicit AI Catalog profile: %#v, %v", document, err)
			}
			entry := document.entries[0]
			if !bytes.Equal(entry.Raw, rawEntry) || !bytes.Equal(body, before) {
				t.Fatal("AI Catalog parsing changed source bytes")
			}
			want := map[string]any{"com.example": map[string]any{"future": json.Number("9007199254740993"), "nullable": nil, "items": []any{nil, true}}}
			if !reflect.DeepEqual(entry.UnknownProperties["extensions"], want) {
				t.Fatalf("opaque extension values changed: %#v", entry.UnknownProperties)
			}
			observation, err := manager.observation("tenant", Source{ID: "source", ProfileID: document.profileID}, "snapshot", entry, 0, now)
			if err != nil || observation.Compatibility.Structural != "valid" || observation.Compatibility.ProfileID != document.profileID || observation.Kind != catalog.KindAgent || !bytes.Equal(observation.RawEntry, rawEntry) {
				t.Fatalf("AI Catalog observation: %#v, %v", observation, err)
			}
			exact := sha256.Sum256(rawEntry)
			if observation.ExactEntryDigest != "sha256:"+hex.EncodeToString(exact[:]) {
				t.Fatal("observation digest does not describe the exact source entry")
			}
			if strings.HasPrefix(identifier, "urn:air:") {
				if observation.NormalizedEntryDigest == "" || observation.SearchPublisher != "example.com" {
					t.Fatal("valid AIR control did not produce native search evidence")
				}
			} else if len(observation.NormalizedEntry) != 0 || observation.NormalizedEntryDigest != "" || observation.Identifier != "" || !hasFinding(observation.Compatibility.Findings, "ARD_IDENTIFIER_COMPATIBILITY") {
				t.Fatal("alternate identifier acquired normalized native search evidence")
			}

			// Transport-defined envelope fields do not select the predecessor format.
			ard, err := parseDocumentWithContextInputs(body, "application/json", noFetch)
			if err != nil || ard.sourceType != SourceARDManifest || ard.profileID != ardProfileID || len(ard.entries) != 1 {
				t.Fatalf("generic ARD profile: %#v, %v", ard, err)
			}
			ardObservation, err := manager.observation("tenant", Source{ID: "source", ProfileID: ard.profileID}, "snapshot", ard.entries[0], 0, now)
			if err != nil {
				t.Fatal(err)
			}
			wantStructural := "invalid"
			if strings.HasPrefix(identifier, "urn:air:") {
				wantStructural = "valid"
			}
			if ardObservation.Compatibility.Structural != wantStructural {
				t.Fatalf("ARD identifier policy = %s, want %s", ardObservation.Compatibility.Structural, wantStructural)
			}
		})
	}
}

// Invalid AI Catalog namespaces fail before projection while ARD keeps its
// separate, open framework-extension contract.
func TestDiscoveryNamespaceGrammarStaysProfileSpecific(t *testing.T) {
	t.Parallel()
	for _, key := range []string{"short", " https://example.com/metadata "} {
		t.Run(key, func(t *testing.T) {
			encodedKey, err := json.Marshal(key)
			if err != nil {
				t.Fatal(err)
			}
			entry := `{"identifier":"urn:air:example.com:agent:weather","displayName":"Weather","type":"application/json","data":{},"trustManifest":{"identity":"https://example.com/identity","trustSchema":{"identifier":"urn:trust:example","version":"1.0"},"extensions":{` + string(encodedKey) + `:null}}}`
			body := []byte(`{"specVersion":"1.0","host":{"displayName":"Catalog"},"entries":[` + entry + `]}`)
			if _, err := parseDocumentWithContextInputs(body, aiCatalogDocumentMediaType, nil); catalog.ErrorCodeOf(err) != catalog.CodeInvalidArgument || !strings.Contains(err.Error(), "extension key") {
				t.Fatalf("invalid AI Catalog namespace did not fail before projection: %v", err)
			}
			ard, err := parseDocumentWithContextInputs(body, "application/json", nil)
			if err != nil || ard.profileID != ardProfileID || len(ard.entries) != 1 || !bytes.Equal(ard.entries[0].Raw, []byte(entry)) {
				t.Fatalf("ARD framework fields were restricted or rewritten: %#v, %v", ard, err)
			}
			for _, finding := range ard.entries[0].StructuralFindings {
				if finding.Severity == catalog.SeverityError {
					t.Fatalf("AI Catalog namespace grammar leaked into ARD: %#v", finding)
				}
			}
		})
	}
}

func TestOfficialARDExamplesUseARDIngestionProfile(t *testing.T) {
	tests := []struct {
		path       string
		entryCount int
	}{
		{path: "upstream/ard/ard-0.91-aa3e598/conformance/examples/basic/ard.json", entryCount: 4},
		{path: "upstream/ard/ard-0.91-aa3e598/conformance/examples/fda-ndc/fda-ndc-catalog.json", entryCount: 1},
		{path: "upstream/ard/ard-0.91-aa3e598/conformance/examples/local-business/local-business-catalog.json", entryCount: 2},
		{path: "upstream/ard/ard-0.91-aa3e598/conformance/examples/noaa-weather/noaa-weather-catalog.json", entryCount: 1},
	}
	for _, test := range tests {
		t.Run(test.path, func(t *testing.T) {
			raw, err := schemas.Files.ReadFile(test.path)
			if err != nil {
				t.Fatal(err)
			}
			document, err := parseDocumentWithContextInputs(raw, ardDocumentMediaType, nil)
			if err != nil {
				t.Fatal(err)
			}
			if document.sourceType != SourceARDManifest || document.profileID != "ard-0.91-aa3e598" || len(document.entries) != test.entryCount {
				t.Fatalf("official ARD ingestion = type %q, profile %q, entries %d", document.sourceType, document.profileID, len(document.entries))
			}
			for index, entry := range document.entries {
				if hasFinding(entry.StructuralFindings, "ARD_ENTRY_SCHEMA_INVALID") || hasFinding(entry.StructuralFindings, "ARD_JSONLD_EXPANSION_FAILED") {
					t.Fatalf("official ARD entry %d has structural findings: %#v", index, entry.StructuralFindings)
				}
			}
		})
	}

	transportExtra := []byte(`{"specVersion":"1.0","host":{"displayName":"Transport metadata"},"entries":[]}`)
	ardDocument, err := parseDocumentWithContextInputs(transportExtra, ardDocumentMediaType, nil)
	if err != nil || ardDocument.sourceType != SourceARDManifest {
		t.Fatalf("explicit ARD media type with transport extras = %#v, %v", ardDocument, err)
	}
	genericDocument, err := parseDocumentWithContextInputs(transportExtra, "application/json; charset=utf-8", nil)
	if err != nil || genericDocument.sourceType != SourceARDManifest {
		t.Fatalf("generic manifest with transport extras = %#v, %v", genericDocument, err)
	}
	aiDocument, err := parseDocumentWithContextInputs(transportExtra, aiCatalogDocumentMediaType, nil)
	if err != nil || aiDocument.sourceType != SourceAICatalog {
		t.Fatalf("explicit AI Catalog media type = %#v, %v", aiDocument, err)
	}
}

func TestCommitSyncRejectsStaleSourceWithoutPartialState(t *testing.T) {
	store := NewMemoryStore()
	tenantID := "00000000-0000-7000-8000-000000000001"
	now := time.Date(2026, 8, 29, 9, 0, 0, 0, time.UTC)
	source := Source{ID: "00000000-0000-7000-8000-000000000010", TenantID: tenantID, DisplayName: "Remote", Endpoint: "https://remote.example/.well-known/ard.json", Status: SourceActive, Revision: 1, CreatedAt: now, UpdatedAt: now}
	if _, err := store.CreateSource(context.Background(), source); err != nil {
		t.Fatal(err)
	}
	updated := source
	updated.DisplayName = "Changed while syncing"
	updated.Revision = 2
	updated.UpdatedAt = now.Add(time.Minute)
	if _, err := store.UpdateSource(context.Background(), updated, 1); err != nil {
		t.Fatal(err)
	}
	completedAt := now.Add(time.Second)
	digest := "sha256:" + strings.Repeat("1", 64)
	snapshot := Snapshot{ID: "00000000-0000-7000-8000-000000000020", TenantID: tenantID, SourceID: source.ID, SourceRevision: source.Revision, State: SnapshotComplete, WithdrawalAuthority: true, StartedAt: now, CompletedAt: &completedAt}
	observation := Observation{IdentityDigest: digest, ID: "00000000-0000-7000-8000-000000000030", TenantID: tenantID, SourceID: source.ID, SnapshotID: snapshot.ID, SourceKey: "entry-one", Identifier: "urn:air:remote.example:default:one", ExactEntryDigest: digest, Freshness: FreshCurrent, FirstSeenAt: now, LastSeenAt: now, Occurrence: ObservationOccurrence{ObservationID: "00000000-0000-7000-8000-000000000030", SourceID: source.ID, SnapshotID: snapshot.ID, SourceRevision: source.Revision, RetrievalDigest: digest, EntryLocation: "/entries/0", ExactEntryDigest: digest, EvidenceComplete: true, ObservedAt: now}}
	staleResult := source
	staleResult.LastCompleteSnapshotID = snapshot.ID
	if _, _, err := store.CommitSync(context.Background(), snapshot, []Observation{observation}, staleResult, 1); catalog.ErrorCodeOf(err) != catalog.CodePreconditionFailed {
		t.Fatalf("stale sync returned %v", err)
	}
	if snapshots, err := store.ListSnapshots(context.Background(), tenantID, source.ID, 10); err != nil || len(snapshots) != 0 {
		t.Fatalf("stale sync committed snapshots: %#v, %v", snapshots, err)
	}
	if observations, err := store.ListObservationPage(context.Background(), tenantID, ObservationListRequest{SourceID: source.ID, Limit: 100}); err != nil || len(observations) != 0 {
		t.Fatalf("stale sync committed observations: %#v, %v", observations, err)
	}
	current, err := store.GetSource(context.Background(), tenantID, source.ID)
	if err != nil || current.Revision != 2 || current.DisplayName != updated.DisplayName || current.LastCompleteSnapshotID != "" {
		t.Fatalf("source changed by stale sync: %#v, %v", current, err)
	}
}

func TestSyncAtRevisionRejectsChangedSourceBeforeFetch(t *testing.T) {
	store := NewMemoryStore()
	fetches := &sequenceFetcher{results: []fetcher.Result{federationResult(`{"entries":[]}`)}}
	manager, err := NewManager(store, fetches, clock.NewFake(time.Date(2026, 8, 29, 9, 0, 0, 0, time.UTC)), id.Generator{})
	if err != nil {
		t.Fatal(err)
	}
	tenantID := "00000000-0000-7000-8000-000000000001"
	ctx := identityfixture.WithActor(context.Background(), identity.Actor{TenantID: tenantID, Subject: "source-admin", Roles: []identity.Role{identity.RoleSourceAdmin}})
	source := Source{ID: "00000000-0000-7000-8000-000000000010", TenantID: tenantID, DisplayName: "Remote", Endpoint: "https://remote.example/.well-known/ard.json", Status: SourceActive, Scope: Scope{Tags: []string{"selected"}}, Revision: 2}
	if _, err := store.CreateSource(ctx, source); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.SyncAtRevision(ctx, source.ID, 0); catalog.ErrorCodeOf(err) != catalog.CodeInvalidArgument {
		t.Fatalf("non-positive requested revision returned %v", err)
	}
	if _, err := manager.SyncAtRevision(ctx, source.ID, 1); catalog.ErrorCodeOf(err) != catalog.CodePreconditionFailed {
		t.Fatalf("stale requested revision returned %v", err)
	}
	if len(fetches.results) != 1 {
		t.Fatal("stale sync contacted the upstream source")
	}
}

func TestNestedCatalogTraversalRequiresExplicitSourcePolicy(t *testing.T) {
	endpoint := "https://catalog.remote.example/.well-known/ard.json"
	root := `{"entries":[{"identifier":"urn:air:remote.example:default:one","displayName":"One","type":"application/a2a-agent-card+json","data":{"name":"One"}},{"identifier":"urn:air:remote.example:catalog:engineering","displayName":"Engineering","type":"application/ai-catalog+json","url":"https://nested.example/engineering.json"}]}`
	fetches := &routingFetcher{results: map[string]fetcher.Result{"GET " + endpoint: federationResultAt(endpoint, root)}}
	manager, err := NewManager(NewMemoryStore(), fetches, clock.NewFake(time.Date(2026, 8, 29, 9, 0, 0, 0, time.UTC)), id.Generator{})
	if err != nil {
		t.Fatal(err)
	}
	ctx := identityfixture.WithActor(context.Background(), identity.Actor{TenantID: "00000000-0000-7000-8000-000000000001", Subject: "source-admin", Roles: []identity.Role{identity.RoleSourceAdmin}})
	inspection, err := manager.Inspect(ctx, endpoint)
	if err != nil {
		t.Fatal(err)
	}
	source, err := manager.Connect(ctx, ConnectInput{InspectionID: inspection.ID, DisplayName: "Remote", Scope: Scope{Kinds: []catalog.Kind{catalog.KindAgent}}, UpdateMode: UpdateAutomatic, Reason: "Approve the inspected source"})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := manager.Sync(ctx, source.ID)
	if err != nil || snapshot.State != SnapshotComplete || !hasFinding(snapshot.Findings, "NESTED_CATALOG_NOT_ENABLED") {
		t.Fatalf("snapshot = %#v, %v", snapshot, err)
	}
	current, err := manager.ObservationPage(ctx, ObservationListRequest{SourceID: source.ID, CurrentOnly: true, Limit: 100})
	if err != nil || len(current) != 1 || current[0].Identifier != "urn:air:remote.example:default:one" {
		t.Fatalf("current observations = %#v, %v", current, err)
	}
	if len(fetches.requests) != 2 {
		t.Fatalf("default source followed a nested Catalog: %#v", fetches.requests)
	}
}

func TestPermittedNestedFailurePreservesLastCompleteMembership(t *testing.T) {
	endpoint := "https://catalog.remote.example/.well-known/ard.json"
	nestedEndpoint := "https://nested.example/engineering.json"
	initial := `{"entries":[{"identifier":"urn:air:remote.example:default:one","displayName":"One","type":"application/a2a-agent-card+json","data":{"name":"One"}}]}`
	partial := `{"entries":[{"identifier":"urn:air:remote.example:default:one","displayName":"One changed","type":"application/a2a-agent-card+json","data":{"name":"One changed"}},{"identifier":"urn:air:remote.example:catalog:engineering","displayName":"Engineering","type":"application/ai-catalog+json","url":"https://nested.example/engineering.json"}]}`
	fetches := &routingFetcher{results: map[string]fetcher.Result{
		"GET " + endpoint:       federationResultAt(endpoint, initial),
		"GET " + nestedEndpoint: {Status: 503, FinalURL: nestedEndpoint, MediaType: "application/ai-catalog+json"},
	}}
	store := NewMemoryStore()
	manager, err := NewManager(store, fetches, clock.NewFake(time.Date(2026, 8, 29, 9, 0, 0, 0, time.UTC)), id.Generator{})
	if err != nil {
		t.Fatal(err)
	}
	ctx := identityfixture.WithActor(context.Background(), identity.Actor{TenantID: "00000000-0000-7000-8000-000000000001", Subject: "source-admin", Roles: []identity.Role{identity.RoleSourceAdmin}})
	inspection, err := manager.Inspect(ctx, endpoint)
	if err != nil {
		t.Fatal(err)
	}
	source, err := manager.Connect(ctx, ConnectInput{InspectionID: inspection.ID, DisplayName: "Remote", Scope: Scope{Kinds: []catalog.Kind{catalog.KindAgent}}, UpdateMode: UpdateAutomatic, Nesting: NestingPolicy{Enabled: true, MaxDepth: 4, AllowedOrigins: []string{"https://nested.example"}}, Reason: "Approve the inspected source"})
	if err != nil {
		t.Fatal(err)
	}
	completeSnapshot, err := manager.Sync(ctx, source.ID)
	if err != nil || completeSnapshot.State != SnapshotComplete {
		t.Fatalf("complete snapshot = %#v, %v", completeSnapshot, err)
	}
	fetches.mu.Lock()
	fetches.results["GET "+endpoint] = federationResultAt(endpoint, partial)
	fetches.mu.Unlock()
	partialSnapshot, err := manager.SyncAtRevision(ctx, source.ID, source.Revision)
	if catalog.ErrorCodeOf(err) != catalog.CodeDependencyUnavailable || partialSnapshot.State != SnapshotIncomplete || partialSnapshot.WithdrawalAuthority || !hasFinding(partialSnapshot.Findings, "NESTED_CATALOG_INCOMPLETE") {
		t.Fatalf("partial snapshot = %#v, %v", partialSnapshot, err)
	}
	current, err := manager.ObservationPage(ctx, ObservationListRequest{SourceID: source.ID, CurrentOnly: true, Limit: 100})
	if err != nil || len(current) != 1 || current[0].SnapshotID != completeSnapshot.ID || current[0].Identifier != "urn:air:remote.example:default:one" || !current[0].RawEntryAvailable || len(current[0].RawEntry) != 0 {
		t.Fatalf("current observations after partial sync = %#v, %v", current, err)
	}
	rawCurrent, err := manager.GetObservation(ctx, current[0].ID)
	if err != nil || !strings.Contains(string(rawCurrent.RawEntry), `"displayName":"One"`) {
		t.Fatalf("partial sync lost last complete exact evidence: %v", err)
	}
	all, err := manager.ObservationPage(ctx, ObservationListRequest{SourceID: source.ID, Limit: 100})
	if err != nil || len(all) != 2 || all[0].Freshness == FreshCurrent && all[1].Freshness == FreshCurrent {
		t.Fatalf("observation history after partial sync = %#v, %v", all, err)
	}
	storedSource, err := manager.GetSource(ctx, source.ID)
	if err != nil || storedSource.Status != SourceError || storedSource.Revision != source.Revision || storedSource.LastCompleteSnapshotID != completeSnapshot.ID {
		t.Fatalf("source after partial sync = %#v, %v", storedSource, err)
	}
}

func TestInlineNestedCatalogTraversal(t *testing.T) {
	endpoint := "https://catalog.remote.example/.well-known/ai-catalog.json"
	root := `{"specVersion":"1.0","entries":[{"identifier":"urn:air:remote.example:catalog:inline","displayName":"Inline","type":"application/ai-catalog+json","data":{"specVersion":"1.0","entries":[{"identifier":"urn:air:remote.example:default:inline-agent","displayName":"Inline Agent","type":"application/a2a-agent-card+json","data":{"name":"Inline Agent"}}]}}]}`
	result := federationResultAt(endpoint, root)
	result.MediaType = "application/json"
	fetches := &routingFetcher{results: map[string]fetcher.Result{"GET " + endpoint: result}}
	manager, err := NewManager(NewMemoryStore(), fetches, clock.NewFake(time.Date(2026, 8, 29, 9, 0, 0, 0, time.UTC)), id.Generator{})
	if err != nil {
		t.Fatal(err)
	}
	ctx := identityfixture.WithActor(context.Background(), identity.Actor{TenantID: "00000000-0000-7000-8000-000000000001", Subject: "source-admin", Roles: []identity.Role{identity.RoleSourceAdmin}})
	inspection, err := manager.Inspect(ctx, endpoint)
	if err != nil {
		t.Fatal(err)
	}
	source, err := manager.Connect(ctx, ConnectInput{InspectionID: inspection.ID, DisplayName: "Remote", Scope: Scope{Kinds: []catalog.Kind{catalog.KindAgent}}, UpdateMode: UpdateAutomatic, Nesting: NestingPolicy{Enabled: true}, Reason: "Approve the inspected source"})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := manager.Sync(ctx, source.ID)
	if err != nil || snapshot.State != SnapshotComplete {
		t.Fatalf("snapshot = %#v, %v", snapshot, err)
	}
	current, err := manager.ObservationPage(ctx, ObservationListRequest{SourceID: source.ID, CurrentOnly: true, Limit: 100})
	if err != nil || len(current) != 1 || current[0].Identifier != "urn:air:remote.example:default:inline-agent" {
		t.Fatalf("inline observations = %#v, %v", current, err)
	}
	if len(fetches.requests) != 2 {
		t.Fatalf("inline nesting made unexpected requests: %#v", fetches.requests)
	}
}

func TestInlineNestedCatalogTraversalStopsAtConfiguredDepth(t *testing.T) {
	t.Parallel()

	endpoint := "https://catalog.remote.example/.well-known/ai-catalog.json"
	leaf := `{"specVersion":"1.0","entries":[{"identifier":"urn:air:remote.example:default:deep-agent","displayName":"Deep Agent","type":"application/a2a-agent-card+json","data":{"name":"Deep Agent"}}]}`
	nested := fmt.Sprintf(`{"specVersion":"1.0","entries":[{"identifier":"urn:air:remote.example:catalog:deep","displayName":"Deep Catalog","type":"application/ai-catalog+json","data":%s}]}`, leaf)
	root := fmt.Sprintf(`{"specVersion":"1.0","entries":[{"identifier":"urn:air:remote.example:catalog:inline","displayName":"Inline Catalog","type":"application/ai-catalog+json","data":%s}]}`, nested)
	result := federationResultAt(endpoint, root)
	result.MediaType = aiCatalogDocumentMediaType
	fetches := &routingFetcher{results: map[string]fetcher.Result{"GET " + endpoint: result}}
	manager, err := NewManager(NewMemoryStore(), fetches, clock.NewFake(time.Date(2026, 8, 29, 9, 0, 0, 0, time.UTC)), id.Generator{})
	if err != nil {
		t.Fatal(err)
	}
	ctx := identityfixture.WithActor(context.Background(), identity.Actor{TenantID: "00000000-0000-7000-8000-000000000001", Subject: "source-admin", Roles: []identity.Role{identity.RoleSourceAdmin}})
	inspection, err := manager.Inspect(ctx, endpoint)
	if err != nil {
		t.Fatal(err)
	}
	source, err := manager.Connect(ctx, ConnectInput{InspectionID: inspection.ID, DisplayName: "Remote", Scope: Scope{Kinds: []catalog.Kind{catalog.KindAgent}}, UpdateMode: UpdateAutomatic, Nesting: NestingPolicy{Enabled: true, MaxDepth: 1}, Reason: "Approve the inspected source"})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := manager.Sync(ctx, source.ID)
	if catalog.ErrorCodeOf(err) != catalog.CodeDependencyUnavailable || snapshot.State != SnapshotIncomplete || snapshot.EnumerationComplete || !hasFinding(snapshot.Findings, "NESTED_CATALOG_DEPTH_EXCEEDED") {
		t.Fatalf("depth-bounded snapshot = %#v, %v", snapshot, err)
	}
	if current, observationErr := manager.ObservationPage(ctx, ObservationListRequest{SourceID: source.ID, CurrentOnly: true, Limit: 100}); observationErr != nil || len(current) != 0 {
		t.Fatalf("over-depth member became current: %#v, %v", current, observationErr)
	}
}

func TestNormalizeNestingPolicy(t *testing.T) {
	policy, err := normalizeNestingPolicy(NestingPolicy{Enabled: true, AllowedOrigins: []string{"HTTPS://NESTED.EXAMPLE/", "https://nested.example"}})
	if err != nil || policy.MaxDepth != 4 || len(policy.AllowedOrigins) != 1 || policy.AllowedOrigins[0] != "https://nested.example:443" {
		t.Fatalf("normalized policy = %#v, %v", policy, err)
	}
	for _, invalid := range []NestingPolicy{
		{Enabled: true, MaxDepth: 5},
		{Enabled: true, AllowedOrigins: []string{"http://nested.example"}},
		{Enabled: true, AllowedOrigins: []string{"https://nested.example/catalog.json"}},
	} {
		if _, err := normalizeNestingPolicy(invalid); catalog.ErrorCodeOf(err) != catalog.CodeInvalidArgument {
			t.Fatalf("invalid policy %#v returned %v", invalid, err)
		}
	}
}

func TestObservationSearchFiltersScoresSortsAndBoundsQueries(t *testing.T) {
	t.Parallel()

	tenantID := "00000000-0000-7000-8000-000000000001"
	store := NewMemoryStore()
	store.observations["agent"] = Observation{ID: "observation-agent", TenantID: tenantID, SourceID: "source-a", SourceKey: "agent", Identifier: "urn:air:remote.example:agent:invoice", Kind: catalog.KindAgent, NormalizedEntryDigest: "normalized-agent", SearchDisplayName: "Invoice agent", SearchDescription: "Reconciles disputed invoices", SearchTags: []string{"finance", "selected"}, SearchQueries: []string{"Find invoice help"}, Freshness: FreshCurrent, RawEntry: json.RawMessage(`{"identifier":"urn:air:remote.example:agent:invoice","displayName":"Invoice agent","description":"Reconciles disputed invoices","type":"application/a2a-agent-card+json","tags":["finance","selected"],"representativeQueries":["Find invoice help"]}`)}
	store.observations["skill"] = Observation{ID: "observation-skill", TenantID: tenantID, SourceID: "source-b", SourceKey: "skill", Identifier: "urn:air:remote.example:skill:invoice", Kind: catalog.KindSkill, NormalizedEntryDigest: "normalized-skill", SearchDisplayName: "Invoice skill", SearchDescription: "Explains invoices", SearchTags: []string{"finance"}, Freshness: FreshCurrent, RawEntry: json.RawMessage(`{"identifier":"urn:air:remote.example:skill:invoice","displayName":"Invoice skill","description":"Explains invoices","type":"application/agent-skills+json","tags":["finance"]}`)}
	store.observations["malformed"] = Observation{ID: "observation-malformed", TenantID: tenantID, SourceID: "source-c", SourceKey: "malformed", Identifier: "urn:air:remote.example:agent:malformed", Kind: catalog.KindAgent, Freshness: FreshCurrent, RawEntry: json.RawMessage(`{"identifier":`)}
	store.observations["stale"] = Observation{ID: "observation-stale", TenantID: tenantID, SourceID: "source-a", SourceKey: "stale", Identifier: "urn:air:remote.example:agent:stale", Kind: catalog.KindAgent, Freshness: FreshStale, RawEntry: json.RawMessage(`{"identifier":"urn:air:remote.example:agent:stale","displayName":"Stale invoice","type":"application/a2a-agent-card+json"}`)}
	for _, observation := range store.observations {
		source := store.sources[observation.SourceID]
		source.ID, source.TenantID, source.Status, source.RetrievalMode = observation.SourceID, tenantID, SourceActive, RetrievalIndexed
		source.Scope.Identifiers = append(source.Scope.Identifiers, observation.Identifier)
		store.sources[source.ID] = source
	}

	manager, err := NewManager(store, &routingFetcher{results: map[string]fetcher.Result{}}, clock.NewFake(time.Date(2026, 8, 29, 9, 0, 0, 0, time.UTC)), id.Generator{})
	if err != nil {
		t.Fatal(err)
	}
	ctx := identityfixture.WithActor(context.Background(), identity.Actor{TenantID: tenantID, Subject: "source-admin", Roles: []identity.Role{identity.RoleSourceAdmin}})

	results, err := manager.Search(ctx, SearchQuery{Text: "  invoice   agent ", Kinds: []catalog.Kind{catalog.KindAgent}, Tags: []string{" selected "}})
	if err != nil || len(results) != 1 || results[0].Observation.ID != "observation-agent" || results[0].Score != 80 || results[0].Source != "source-a" {
		t.Fatalf("filtered search = %#v, %v", results, err)
	}
	results, err = manager.Search(ctx, SearchQuery{})
	if err != nil || len(results) != 2 || results[0].Score != 100 || results[0].Observation.Identifier > results[1].Observation.Identifier {
		t.Fatalf("unfiltered search = %#v, %v", results, err)
	}
	results, err = manager.Search(ctx, SearchQuery{Text: "missing"})
	if err != nil || len(results) != 0 {
		t.Fatalf("nonmatching search = %#v, %v", results, err)
	}

	for name, query := range map[string]SearchQuery{
		"text":          {Text: strings.Repeat("x", 501)},
		"kind count":    {Kinds: []catalog.Kind{catalog.KindAgent, catalog.KindMCPServer, catalog.KindSkill, catalog.KindAgent}},
		"tag count":     {Tags: make([]string, 101)},
		"tag length":    {Tags: []string{strings.Repeat("x", 81)}},
		"duplicate tag": {Tags: []string{"selected", " selected "}},
	} {
		if _, searchErr := manager.Search(ctx, query); catalog.ErrorCodeOf(searchErr) != catalog.CodeInvalidArgument {
			t.Fatalf("%s query returned %v", name, searchErr)
		}
	}
	if _, err := manager.Search(context.Background(), SearchQuery{}); catalog.ErrorCodeOf(err) != catalog.CodeForbidden {
		t.Fatalf("unauthorized observation search returned %v", err)
	}
}

func TestSourceScopeAndObservationSearchRejectInvalidKinds(t *testing.T) {
	invalidKind := catalog.Kind("unsupported")
	if err := validateScope(Scope{Kinds: []catalog.Kind{catalog.KindAgent, invalidKind}}); catalog.ErrorCodeOf(err) != catalog.CodeInvalidArgument {
		t.Fatalf("invalid mixed source scope returned %v", err)
	}
	manager, err := NewManager(NewMemoryStore(), &routingFetcher{results: map[string]fetcher.Result{}}, clock.NewFake(time.Date(2026, 8, 29, 9, 0, 0, 0, time.UTC)), id.Generator{})
	if err != nil {
		t.Fatal(err)
	}
	ctx := identityfixture.WithActor(context.Background(), identity.Actor{TenantID: "00000000-0000-7000-8000-000000000001", Subject: "source-admin", Roles: []identity.Role{identity.RoleSourceAdmin}})
	if _, err := manager.Search(ctx, SearchQuery{Kinds: []catalog.Kind{catalog.KindAgent, invalidKind}}); catalog.ErrorCodeOf(err) != catalog.CodeInvalidArgument {
		t.Fatalf("invalid mixed observation search returned %v", err)
	}
}

func TestDiscoveryHelpersAreBoundedAndFailClosed(t *testing.T) {
	if got := normalizeInspectionURL(" catalog.example.com/path "); got != "https://catalog.example.com/path" {
		t.Fatalf("normalized inspection URL = %q", got)
	}
	if _, err := urlBase("not-a-url"); err == nil {
		t.Fatal("invalid discovery base was accepted")
	}
	if got, err := urlBase("https://catalog.example.com/path"); err != nil || got != "https://catalog.example.com" {
		t.Fatalf("discovery base = %q, %v", got, err)
	}
	for _, value := range []string{"http://catalog.example.com", "https://catalog.example.com?token=one"} {
		if endpoint := registrySearchEndpoint(value); endpoint != "" {
			t.Fatalf("unsafe Registry base %q produced %q", value, endpoint)
		}
	}
	if endpoint := registrySearchEndpoint("https://catalog.example.com/ard/v1/"); endpoint != "https://catalog.example.com/ard/v1/search" {
		t.Fatalf("Registry search endpoint = %q", endpoint)
	}
	if links := discoveryLinks([]string{`<../ard.json?fields=one,two>; rel="ard", <ignored>; rel="other"`}, "https://catalog.example.com/path/index.html"); len(links) != 1 || links[0] != (discoveryLocation{URL: "https://catalog.example.com/ard.json?fields=one,two", MediaType: ardDocumentMediaType}) {
		t.Fatalf("discovery links = %#v", links)
	}
	if links := discoveryLinks([]string{`malformed`}, "%"); len(links) != 0 {
		t.Fatalf("invalid-base discovery links = %#v", links)
	}
	if links := discoveryLinks([]string{`</both-aliases>; rel="ard ai-catalog"`}, "https://catalog.example.com/"); len(links) != 1 || links[0].MediaType != ardDocumentMediaType {
		t.Fatalf("equivalent discovery aliases did not preserve the ARD relation: %#v", links)
	}
	if linkHasRelation(`; title="ard"`, "ard") {
		t.Fatal("non-rel Link parameter was accepted")
	}
	if parts := splitHTTPList(`<one>; title="a,b", <two>`); len(parts) != 2 {
		t.Fatalf("quoted Link list split into %#v", parts)
	}
	if parts := splitHTTPParameters(`rel="ard;alternate"; title="catalog"`); len(parts) != 2 {
		t.Fatalf("quoted Link parameters split into %#v", parts)
	}

	embedded, links := htmlDiscovery([]byte(`<html><head><link rel="alternate ard" href="/.well-known/ard.json"><script type="application/ld+json; charset=utf-8">{"identifier":"urn:air:remote.example:default:agent","displayName":"Agent","type":"application/a2a-agent-card+json","data":{"name":"Agent"}}</script></head></html>`), "https://catalog.example.com/page")
	if len(links) != 1 || links[0] != (discoveryLocation{URL: "https://catalog.example.com/.well-known/ard.json", MediaType: ardDocumentMediaType}) || embedded.MediaType != ardDocumentMediaType || !json.Valid(embedded.Body) || !strings.Contains(string(embedded.Body), `"entries"`) {
		t.Fatalf("HTML discovery embedded=%s type=%q links=%#v", embedded.Body, embedded.MediaType, links)
	}
	if hasRelation("other", "ard") {
		t.Fatal("unrelated HTML relation was accepted")
	}
	if _, links := htmlDiscovery([]byte(`<html><head><link rel="ard ai-catalog" href="/both-aliases"></head></html>`), "https://catalog.example.com/page"); len(links) != 1 || links[0].MediaType != ardDocumentMediaType {
		t.Fatalf("equivalent HTML aliases did not preserve the ARD relation: %#v", links)
	}
	aiEmbedded, aiLinks := htmlDiscovery([]byte(`<html><head><link rel="ai-catalog" href="/.well-known/ai-catalog.json"><script type="application/ai-catalog+json; charset=utf-8">{"specVersion":"1.0","entries":[]}</script></head></html>`), "https://catalog.example.com/page")
	if len(aiLinks) != 1 || aiLinks[0] != (discoveryLocation{URL: "https://catalog.example.com/.well-known/ai-catalog.json", MediaType: "application/json"}) || aiEmbedded.MediaType != aiCatalogDocumentMediaType {
		t.Fatalf("AI Catalog HTML discovery embedded type=%q links=%#v", aiEmbedded.MediaType, aiLinks)
	}
	if parsed, err := parseDocumentWithContextInputs(aiEmbedded.Body, aiEmbedded.MediaType, nil); err != nil || parsed.sourceType != SourceAICatalog {
		t.Fatalf("AI Catalog embedded document = %#v, %v", parsed, err)
	}
	values := make([]discoveryLocation, 0, 14)
	values = append(values, discoveryLocation{}, discoveryLocation{URL: "excluded"}, discoveryLocation{URL: "one", MediaType: ardDocumentMediaType}, discoveryLocation{URL: "one", MediaType: ardDocumentMediaType})
	for index := range 10 {
		values = append(values, discoveryLocation{URL: fmt.Sprintf("https://catalog-%d.example.com", index)})
	}
	if unique := uniqueDiscoveryLocations(values, "excluded"); len(unique) != 8 || unique[0] != (discoveryLocation{URL: "one", MediaType: ardDocumentMediaType}) {
		t.Fatalf("bounded unique URLs = %#v", unique)
	}
	conflictingURL := "https://catalog.example.com/ambiguous"
	if unique := uniqueDiscoveryLocations([]discoveryLocation{{URL: conflictingURL, MediaType: ardDocumentMediaType}, {URL: conflictingURL, MediaType: aiCatalogDocumentMediaType}}); len(unique) != 0 {
		t.Fatalf("contradictory profiles for one discovery URL were retained: %#v", unique)
	}
	effectiveARD, err := effectiveDiscoveryMediaType("application/json", "", "ard-0.91-aa3e598")
	if err != nil || !isDomainResolutionTarget("https://catalog.example.com/") || isDomainResolutionTarget("https://catalog.example.com/page") || discoveryURLMediaType("https://catalog.example.com/.well-known/ard.json") != ardDocumentMediaType || effectiveARD != ardDocumentMediaType {
		t.Fatal("discovery target or media-type classification did not preserve its ARD profile")
	}
	if _, err := effectiveDiscoveryMediaType(aiCatalogDocumentMediaType, ardDocumentMediaType, ""); err == nil {
		t.Fatal("conflicting discovery relation and response media type were accepted")
	}
	if effective, err := effectiveDiscoveryMediaType(ardDocumentMediaType, "", "ai-catalog-1.0-b062278"); err != nil || effective != ardDocumentMediaType {
		t.Fatalf("explicit response media type did not expose a changed retained source profile: %q, %v", effective, err)
	}

	for _, testCase := range []struct {
		base      string
		reference string
	}{
		{base: "not-a-url", reference: "/nested"},
		{base: "https://catalog.example.com", reference: "%"},
		{base: "https://catalog.example.com", reference: "http://nested.example.com"},
	} {
		if _, err := resolveNestedURL(testCase.base, testCase.reference); err == nil {
			t.Fatalf("unsafe nested URL %#v was accepted", testCase)
		}
	}
	if nested, err := resolveNestedURL("https://catalog.example.com/root", "nested.json"); err != nil || nested != "https://catalog.example.com/nested.json" {
		t.Fatalf("nested URL = %q, %v", nested, err)
	}
	if nestedOriginAllowed("%", "https://nested.example.com", nil) || nestedOriginAllowed("https://catalog.example.com", "https://denied.example.com", nil) {
		t.Fatal("invalid or denied nested origin was allowed")
	}
	if !nestedOriginAllowed("https://catalog.example.com", "https://nested.example.com", []string{"https://nested.example.com"}) {
		t.Fatal("allowlisted nested origin was denied")
	}

	if kindForMediaType(`text/markdown; profile="urn:air:agent-skills"`) != catalog.KindSkill || kindForMediaType("application/vnd.unknown+json") != "" {
		t.Fatal("federated media types were classified incorrectly")
	}
}

func TestFederationManagerAuthorizationAndConnectGuardrails(t *testing.T) {
	fakeClock := clock.NewFake(time.Date(2026, 8, 29, 9, 0, 0, 0, time.UTC))
	store := NewMemoryStore()
	fetches := &sequenceFetcher{}
	for _, build := range []func() (*Manager, error){
		func() (*Manager, error) { return NewManager(nil, fetches, fakeClock, id.Generator{}) },
		func() (*Manager, error) { return NewManager(store, nil, fakeClock, id.Generator{}) },
		func() (*Manager, error) { return NewManager(store, fetches, nil, id.Generator{}) },
		func() (*Manager, error) { return NewManager(store, fetches, fakeClock, nil) },
		func() (*Manager, error) {
			return NewManager(store, fetches, fakeClock, id.Generator{}, WithExecutionLimits(MaxFederatedSources+1, MaxReferralHops))
		},
		func() (*Manager, error) {
			return NewManager(store, fetches, fakeClock, id.Generator{}, WithExecutionLimits(MaxFederatedSources, MaxReferralHops+1))
		},
		func() (*Manager, error) {
			return NewManager(store, fetches, fakeClock, id.Generator{}, WithLiveSearchTimeout(MaxLiveSearchTimeout+time.Nanosecond))
		},
	} {
		if _, err := build(); catalog.ErrorCodeOf(err) != catalog.CodeInvalidArgument {
			t.Fatalf("incomplete manager dependencies returned %v", err)
		}
	}
	manager, err := NewManager(store, fetches, fakeClock, id.Generator{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Inspect(context.Background(), "catalog.example.com"); catalog.ErrorCodeOf(err) != catalog.CodeForbidden {
		t.Fatalf("anonymous inspection returned %v", err)
	}
	tenantID := "00000000-0000-7000-8000-000000000001"
	adminContext := identityfixture.WithActor(context.Background(), identity.Actor{TenantID: tenantID, Subject: "source-admin", Roles: []identity.Role{identity.RoleSourceAdmin}})
	inspection := Inspection{ID: "inspection", TenantID: tenantID, ResolvedURL: "https://remote.example.com/.well-known/ard.json", SourceType: SourceARDManifest, ProfileID: "ard-0.91-aa3e598", PolicyID: "test-policy", PolicyRevision: 1, PolicyDigest: federationTestPolicyDigest, ExpiresAt: fakeClock.Now().Add(time.Hour)}
	if _, err := store.SaveInspection(context.Background(), inspection); err != nil {
		t.Fatal(err)
	}
	expired := inspection
	expired.ID = "expired"
	expired.ExpiresAt = fakeClock.Now()
	if _, err := store.SaveInspection(context.Background(), expired); err != nil {
		t.Fatal(err)
	}
	base := ConnectInput{InspectionID: inspection.ID, DisplayName: "Remote", Scope: Scope{Kinds: []catalog.Kind{catalog.KindAgent}}, Reason: "Approve the inspected source"}
	invalidCases := map[string]ConnectInput{
		"missing inspection": {InspectionID: "missing", DisplayName: "Remote", Scope: base.Scope},
		"expired inspection": {InspectionID: expired.ID, DisplayName: "Remote", Scope: base.Scope},
		"display name":       {InspectionID: inspection.ID, Scope: base.Scope},
		"update mode":        {InspectionID: inspection.ID, DisplayName: "Remote", Scope: base.Scope, UpdateMode: "continuous"},
		"federation mode":    {InspectionID: inspection.ID, DisplayName: "Remote", Scope: base.Scope, Live: LivePolicy{FederationMode: "full"}},
		"empty scope":        {InspectionID: inspection.ID, DisplayName: "Remote"},
		"missing Registry":   {InspectionID: inspection.ID, DisplayName: "Remote", Scope: base.Scope, Live: LivePolicy{Enabled: true, QueryPrivacyAcknowledged: true}},
		"nesting":            {InspectionID: inspection.ID, DisplayName: "Remote", Scope: base.Scope, Nesting: NestingPolicy{Enabled: true, MaxDepth: 5}},
	}
	for name, input := range invalidCases {
		t.Run(name, func(t *testing.T) {
			// Keep earlier prerequisites valid so each case reaches its named guard.
			input.Reason = base.Reason
			want := catalog.CodeInvalidArgument
			switch name {
			case "missing inspection":
				want = catalog.CodeNotFound
			case "expired inspection", "missing Registry":
				want = catalog.CodePreconditionFailed
			}
			if _, err := manager.Connect(adminContext, input); catalog.ErrorCodeOf(err) != want {
				t.Fatalf("invalid source connection returned %v, want %s", err, want)
			}
		})
	}
	for _, name := range []string{strings.Repeat("é", 161), string([]byte{0xff}), "bad\x00name"} {
		invalid := base
		invalid.DisplayName = name
		if _, err := manager.Connect(adminContext, invalid); catalog.ErrorCodeOf(err) != catalog.CodeInvalidArgument {
			t.Fatalf("invalid source name: %v", err)
		}
	}
	base.DisplayName = strings.Repeat("🙂", 160)
	source, err := manager.Connect(adminContext, base)
	if err == nil && source.DisplayName != base.DisplayName {
		t.Fatal("source name changed")
	}
	if err != nil || source.UpdateMode != UpdateManual || source.Live.FederationMode != "none" || source.Status != SourceActive || source.InspectionID != inspection.ID {
		t.Fatalf("connected source = %#v, %v", source, err)
	}

	authorizationChecks := map[string]func() error{
		"inspection read": func() error { _, err := manager.GetInspection(context.Background(), inspection.ID); return err },
		"snapshot read":   func() error { _, err := manager.GetSnapshot(context.Background(), "snapshot"); return err },
		"source list": func() error {
			_, err := manager.ListSourcePage(context.Background(), SourceListRequest{Limit: 1})
			return err
		},
		"source read": func() error { _, err := manager.GetSource(context.Background(), source.ID); return err },
		"change preview": func() error {
			_, err := manager.PreviewChange(context.Background(), source.ID, PatchInput{})
			return err
		},
		"source update": func() error {
			_, err := manager.UpdateSource(context.Background(), source.ID, PatchInput{}, 1)
			return err
		},
		"source test":   func() error { _, err := manager.TestSource(context.Background(), source.ID); return err },
		"snapshot list": func() error { _, err := manager.ListSnapshots(context.Background(), source.ID, 10); return err },
		"observation read": func() error {
			_, err := manager.GetObservation(context.Background(), "observation")
			return err
		},
		"status": func() error {
			_, err := manager.SetStatus(context.Background(), source.ID, SourcePaused, 1)
			return err
		},
		"sync": func() error { _, err := manager.Sync(context.Background(), source.ID); return err },
		"observations": func() error {
			_, err := manager.ObservationPage(context.Background(), ObservationListRequest{SourceID: source.ID, CurrentOnly: true, Limit: 1})
			return err
		},
		"search": func() error { _, err := manager.Search(context.Background(), SearchQuery{}); return err },
		"live search": func() error {
			_, err := manager.LiveSearch(context.Background(), LiveSearchRequest{Text: "agent"})
			return err
		},
	}
	for name, check := range authorizationChecks {
		t.Run(name, func(t *testing.T) {
			if err := check(); catalog.ErrorCodeOf(err) != catalog.CodeForbidden {
				t.Fatalf("unauthorized operation returned %v", err)
			}
		})
	}
	if _, err := manager.ListSnapshots(adminContext, source.ID, 0); err != nil {
		t.Fatalf("default snapshot list failed: %v", err)
	}
	if _, err := manager.GetObservation(adminContext, "missing"); catalog.ErrorCodeOf(err) != catalog.CodeNotFound {
		t.Fatalf("missing observation returned %v", err)
	}
	if _, err := manager.SetStatus(adminContext, source.ID, SourceError, source.Revision); catalog.ErrorCodeOf(err) != catalog.CodeInvalidArgument {
		t.Fatalf("invalid source status returned %v", err)
	}
	if _, err := manager.PreviewChange(adminContext, source.ID, PatchInput{}); catalog.ErrorCodeOf(err) != catalog.CodeInvalidArgument {
		t.Fatalf("empty source patch returned %v", err)
	}
	if _, err := manager.UpdateSource(adminContext, source.ID, PatchInput{}, source.Revision); catalog.ErrorCodeOf(err) != catalog.CodePreconditionFailed {
		t.Fatalf("unpreviewed source update returned %v", err)
	}
	if _, err := manager.TestSource(adminContext, source.ID); catalog.ErrorCodeOf(err) != catalog.CodeDependencyUnavailable {
		t.Fatalf("unavailable source test returned %v", err)
	}
	if _, err := manager.Search(adminContext, SearchQuery{Kinds: []catalog.Kind{"unsupported"}}); catalog.ErrorCodeOf(err) != catalog.CodeInvalidArgument {
		t.Fatalf("invalid observation search kind returned %v", err)
	}
	if _, err := manager.Search(adminContext, SearchQuery{Tags: []string{strings.Repeat("x", 81)}}); catalog.ErrorCodeOf(err) != catalog.CodeInvalidArgument {
		t.Fatalf("oversized observation search tag returned %v", err)
	}
	for _, request := range []LiveSearchRequest{{}, {Text: "agent", PageSize: -1}, {Text: "agent", PageSize: 51}} {
		if _, err := manager.LiveSearch(adminContext, request); catalog.ErrorCodeOf(err) != catalog.CodeInvalidArgument {
			t.Fatalf("invalid live request %#v returned %v", request, err)
		}
	}
}

func TestFederationAdministrativePagesEnforceAuthorityAndBoundedPositions(t *testing.T) {
	t.Parallel()

	const tenantID = "00000000-0000-7000-8000-000000000001"
	const sourceID = "00000000-0000-7000-8000-000000000010"
	const otherSourceID = "00000000-0000-7000-8000-000000000011"
	const snapshotID = "00000000-0000-7000-8000-000000000020"
	const otherSnapshotID = "00000000-0000-7000-8000-000000000021"
	const observationID = "00000000-0000-7000-8000-000000000030"
	const otherObservationID = "00000000-0000-7000-8000-000000000031"
	const missingID = "00000000-0000-7000-8000-000000000099"
	now := time.Date(2026, time.September, 3, 10, 0, 0, 0, time.UTC)
	store := NewMemoryStore()
	store.sources[sourceID] = Source{ID: sourceID, TenantID: tenantID, DisplayName: "Alpha", Status: SourceActive}
	store.sources[otherSourceID] = Source{ID: otherSourceID, TenantID: tenantID, DisplayName: "Bravo", Status: SourceActive}
	store.snapshots[snapshotID] = Snapshot{ID: snapshotID, TenantID: tenantID, SourceID: sourceID, StartedAt: now}
	store.snapshots[otherSnapshotID] = Snapshot{ID: otherSnapshotID, TenantID: tenantID, SourceID: sourceID, StartedAt: now.Add(-time.Minute)}
	store.observations[observationID] = Observation{ID: observationID, TenantID: tenantID, SourceID: sourceID, Identifier: "urn:air:remote.example:agent:alpha", Freshness: FreshCurrent}
	store.observations[otherObservationID] = Observation{ID: otherObservationID, TenantID: tenantID, SourceID: sourceID, Identifier: "urn:air:remote.example:agent:bravo", Freshness: FreshCurrent}
	for _, occurrence := range []ObservationOccurrence{
		{ObservationID: observationID, SnapshotID: snapshotID, RetrievalDigest: "sha256:" + strings.Repeat("a", 64), EntryLocation: "/entries/0", ObservedAt: now},
		{ObservationID: observationID, SnapshotID: otherSnapshotID, RetrievalDigest: "sha256:" + strings.Repeat("b", 64), EntryLocation: "/entries/1", ObservedAt: now.Add(-time.Minute)},
	} {
		store.occurrences[occurrenceKey(tenantID, occurrence)] = occurrence
	}
	manager, err := NewManager(store, &routingFetcher{results: map[string]fetcher.Result{}}, clock.NewFake(now), id.Generator{})
	if err != nil {
		t.Fatal(err)
	}
	adminContext := identityfixture.WithActor(t.Context(), identity.Actor{TenantID: tenantID, Subject: "source-admin", Roles: []identity.Role{identity.RoleSourceAdmin}})

	unauthorized := []func() error{
		func() error { _, err := manager.ListSourcePage(t.Context(), SourceListRequest{Limit: 1}); return err },
		func() error {
			_, err := manager.ListSnapshotPage(t.Context(), SnapshotListRequest{SourceID: sourceID, Limit: 1})
			return err
		},
		func() error {
			_, err := manager.ObservationPage(t.Context(), ObservationListRequest{Limit: 1})
			return err
		},
		func() error {
			_, err := manager.ObservationOccurrencePage(t.Context(), OccurrenceListRequest{ObservationID: observationID, Limit: 1})
			return err
		},
	}
	for _, call := range unauthorized {
		if err := call(); catalog.ErrorCodeOf(err) != catalog.CodeForbidden {
			t.Fatalf("unauthorized administrative page returned %v", err)
		}
	}

	sources, err := manager.ListSourcePage(adminContext, SourceListRequest{Limit: 1})
	if err != nil || len(sources) != 2 {
		t.Fatalf("source lookahead page = %#v, %v", sources, err)
	}
	if _, err := manager.ListSourcePage(adminContext, SourceListRequest{AfterDisplayName: sources[0].DisplayName, AfterID: sources[0].ID, Limit: 1}); err != nil {
		t.Fatalf("positioned source page returned %v", err)
	}
	snapshots, err := manager.ListSnapshotPage(adminContext, SnapshotListRequest{SourceID: sourceID, Limit: 1})
	if err != nil || len(snapshots) != 2 {
		t.Fatalf("snapshot lookahead page = %#v, %v", snapshots, err)
	}
	if _, err := manager.ListSnapshotPage(adminContext, SnapshotListRequest{SourceID: sourceID, AfterStartedAt: &snapshots[0].StartedAt, AfterID: snapshots[0].ID, Limit: 1}); err != nil {
		t.Fatalf("positioned snapshot page returned %v", err)
	}
	observations, err := manager.ObservationPage(adminContext, ObservationListRequest{SourceID: sourceID, Limit: 1})
	if err != nil || len(observations) != 2 {
		t.Fatalf("observation lookahead page = %#v, %v", observations, err)
	}
	if _, err := manager.ObservationPage(adminContext, ObservationListRequest{AfterIdentifier: observations[0].Identifier, AfterVersionPresent: observations[0].VersionPresent, AfterVersion: observations[0].Version, AfterID: observations[0].ID, Limit: 1}); err != nil {
		t.Fatalf("positioned observation page returned %v", err)
	}
	occurrences, err := manager.ObservationOccurrencePage(adminContext, OccurrenceListRequest{ObservationID: observationID, Limit: 1})
	if err != nil || len(occurrences) != 2 {
		t.Fatalf("occurrence lookahead page = %#v, %v", occurrences, err)
	}
	if _, err := manager.ObservationOccurrencePage(adminContext, OccurrenceListRequest{ObservationID: observationID, AfterObservedAt: &occurrences[0].ObservedAt, AfterSnapshotID: occurrences[0].SnapshotID, AfterRetrievalDigest: occurrences[0].RetrievalDigest, AfterEntryLocation: occurrences[0].EntryLocation, Limit: 1}); err != nil {
		t.Fatalf("positioned occurrence page returned %v", err)
	}

	invalidSources := []SourceListRequest{{}, {Limit: 101}, {AfterID: sourceID, Limit: 1}, {AfterDisplayName: "Alpha", AfterID: "not-a-uuid", Limit: 1}}
	for _, request := range invalidSources {
		if _, err := manager.ListSourcePage(adminContext, request); catalog.ErrorCodeOf(err) != catalog.CodeInvalidArgument {
			t.Fatalf("invalid source page %#v returned %v", request, err)
		}
	}
	invalidSnapshots := []SnapshotListRequest{{Limit: 1}, {SourceID: sourceID}, {SourceID: sourceID, Limit: 101}, {SourceID: sourceID, AfterID: snapshotID, Limit: 1}, {SourceID: sourceID, AfterStartedAt: &now, AfterID: "not-a-uuid", Limit: 1}}
	for _, request := range invalidSnapshots {
		if _, err := manager.ListSnapshotPage(adminContext, request); catalog.ErrorCodeOf(err) != catalog.CodeInvalidArgument {
			t.Fatalf("invalid snapshot page %#v returned %v", request, err)
		}
	}
	invalidObservations := []ObservationListRequest{{Limit: 0}, {Limit: 101}, {SourceID: "not-a-uuid", Limit: 1}, {AfterIdentifier: "urn:air:remote.example:agent:alpha", Limit: 1}, {AfterID: "not-a-uuid", Limit: 1}}
	for _, request := range invalidObservations {
		if _, err := manager.ObservationPage(adminContext, request); catalog.ErrorCodeOf(err) != catalog.CodeInvalidArgument {
			t.Fatalf("invalid observation page %#v returned %v", request, err)
		}
	}
	invalidOccurrences := []OccurrenceListRequest{{Limit: 1}, {ObservationID: observationID}, {ObservationID: observationID, Limit: 101}, {ObservationID: observationID, AfterObservedAt: &now, Limit: 1}, {ObservationID: observationID, AfterObservedAt: &now, AfterSnapshotID: snapshotID, AfterRetrievalDigest: "digest", AfterEntryLocation: "/entries/0", Limit: 1}}
	for _, request := range invalidOccurrences {
		if _, err := manager.ObservationOccurrencePage(adminContext, request); catalog.ErrorCodeOf(err) != catalog.CodeInvalidArgument {
			t.Fatalf("invalid occurrence page %#v returned %v", request, err)
		}
	}
	if _, err := manager.ListSnapshotPage(adminContext, SnapshotListRequest{SourceID: missingID, Limit: 1}); catalog.ErrorCodeOf(err) != catalog.CodeNotFound {
		t.Fatalf("missing source snapshot page returned %v", err)
	}
	if _, err := manager.ObservationPage(adminContext, ObservationListRequest{SourceID: missingID, Limit: 1}); catalog.ErrorCodeOf(err) != catalog.CodeNotFound {
		t.Fatalf("missing source observation page returned %v", err)
	}
	if _, err := manager.ObservationOccurrencePage(adminContext, OccurrenceListRequest{ObservationID: missingID, Limit: 1}); catalog.ErrorCodeOf(err) != catalog.CodeNotFound {
		t.Fatalf("missing observation occurrence page returned %v", err)
	}
}

func TestSourcePatchScopeAndLiveQueryValidation(t *testing.T) {
	source := Source{DisplayName: "Remote", RegistrySearchURL: "https://remote.example.com/ard/v1/search", Scope: Scope{Kinds: []catalog.Kind{catalog.KindAgent}}, UpdateMode: UpdateManual, Live: LivePolicy{FederationMode: "none"}}
	if _, err := applySourcePatch(source, PatchInput{}); catalog.ErrorCodeOf(err) != catalog.CodeInvalidArgument {
		t.Fatalf("empty patch returned %v", err)
	}
	displayName := " Updated "
	scope := Scope{Identifiers: []string{"urn:air:remote.example:default:agent"}, Kinds: []catalog.Kind{catalog.KindSkill, catalog.KindAgent}, Publishers: []string{"remote.example"}, Tags: []string{"selected"}}
	updateMode := UpdateAutomatic
	live := LivePolicy{Enabled: true, QueryPrivacyAcknowledged: true, FederationMode: "referrals"}
	nesting := NestingPolicy{Enabled: true, AllowedOrigins: []string{"https://nested.example.com", "https://nested.example.com/"}}
	credential := " binding "
	network := " public "
	patched, err := applySourcePatch(source, PatchInput{DisplayName: &displayName, Scope: &scope, UpdateMode: &updateMode, Live: &live, Nesting: &nesting, CredentialBinding: &credential, NetworkProfile: &network})
	if err != nil || patched.DisplayName != "Updated" || patched.UpdateMode != UpdateAutomatic || patched.Nesting.MaxDepth != 4 || len(patched.Nesting.AllowedOrigins) != 1 || patched.CredentialBinding != "binding" || patched.NetworkProfile != "public" || len(patched.Scope.Kinds) != 2 || len(patched.Scope.Identifiers) != 1 {
		t.Fatalf("normalized patch = %#v, %v", patched, err)
	}
	blank := " "
	emptyScope := Scope{}
	badMode := UpdateMode("continuous")
	privateLive := LivePolicy{Enabled: true, FederationMode: "none"}
	badFederation := LivePolicy{FederationMode: "full"}
	longBinding := strings.Repeat("x", 257)
	withoutRegistry := source
	withoutRegistry.RegistrySearchURL = ""
	for name, call := range map[string]func() error{
		"display":     func() error { _, err := applySourcePatch(source, PatchInput{DisplayName: &blank}); return err },
		"scope":       func() error { _, err := applySourcePatch(source, PatchInput{Scope: &emptyScope}); return err },
		"update mode": func() error { _, err := applySourcePatch(source, PatchInput{UpdateMode: &badMode}); return err },
		"privacy":     func() error { _, err := applySourcePatch(source, PatchInput{Live: &privateLive}); return err },
		"federation":  func() error { _, err := applySourcePatch(source, PatchInput{Live: &badFederation}); return err },
		"Registry":    func() error { _, err := applySourcePatch(withoutRegistry, PatchInput{Live: &live}); return err },
		"binding length": func() error {
			_, err := applySourcePatch(source, PatchInput{CredentialBinding: &longBinding})
			return err
		},
	} {
		t.Run(name, func(t *testing.T) {
			if err := call(); err == nil {
				t.Fatal("invalid source patch was accepted")
			}
		})
	}

	for name, scope := range map[string]Scope{
		"count":                  {Identifiers: make([]string, 1001)},
		"identifier":             {Identifiers: []string{strings.Repeat("i", 513)}},
		"version":                {Versions: []string{strings.Repeat("v", 129)}},
		"publisher":              {Publishers: []string{strings.Repeat("p", 256)}},
		"tag":                    {Tags: []string{strings.Repeat("t", 81)}},
		"blank":                  {Tags: []string{" "}},
		"surrounding whitespace": {Tags: []string{" selected"}},
		"duplicate string":       {Identifiers: []string{"urn:air:remote.example:default:agent", "urn:air:remote.example:default:agent"}},
		"duplicate kind":         {Kinds: []catalog.Kind{catalog.KindAgent, catalog.KindAgent}},
		"present empty array":    {Tags: []string{}, Kinds: []catalog.Kind{catalog.KindAgent}},
	} {
		t.Run(name, func(t *testing.T) {
			if err := validateScope(scope); catalog.ErrorCodeOf(err) != catalog.CodeInvalidArgument {
				t.Fatalf("invalid scope returned %v", err)
			}
		})
	}
	if scopeMatches(Scope{Publishers: []string{"remote.example"}}, sourceEntry{Identifier: "not-an-air", Type: "application/a2a-agent-card+json"}) {
		t.Fatal("invalid publisher identifier matched a scoped source")
	}
	if !scopeMatches(Scope{Versions: []string{"1.2.3"}}, sourceEntry{Version: "1.2.3"}) || scopeMatches(Scope{Versions: []string{"1.2.3"}}, sourceEntry{Version: "2.0.0"}) {
		t.Fatal("source version scope did not require an exact version match")
	}

	tooManyFields := make(map[string]any)
	for index := range 21 {
		tooManyFields[fmt.Sprintf("field-%d", index)] = "value"
	}
	tooManyValues := map[string]any{"one": make([]string, 100), "two": make([]string, 100), "three": []string{"value"}}
	invalidQueries := []struct {
		queryContext any
		filter       map[string]any
	}{
		{queryContext: make(chan struct{})},
		{queryContext: 42},
		{queryContext: "relative"},
		{filter: tooManyFields},
		{filter: map[string]any{"": "value"}},
		{filter: map[string]any{"field": []any{"value", 1}}},
		{filter: map[string]any{"field": 1}},
		{filter: map[string]any{"field": []string{}}},
		{filter: tooManyValues},
		{filter: map[string]any{"field": " "}},
	}
	for index, testCase := range invalidQueries {
		if err := validateLiveSearchQuery(testCase.queryContext, testCase.filter); catalog.ErrorCodeOf(err) != catalog.CodeInvalidArgument {
			t.Fatalf("invalid live query %d returned %v", index, err)
		}
	}
	if err := validateLiveSearchQuery(map[string]any{"term": "value"}, map[string]any{"kind": []string{"agent", "skill"}}); err != nil {
		t.Fatalf("valid live query returned %v", err)
	}
}

func TestSourceNormalizationEnforcesRetrievalAndDiscoveryTopology(t *testing.T) {
	t.Parallel()

	const tenantID = "00000000-0000-7000-8000-000000000001"
	manager, err := NewManager(NewMemoryStore(), &routingFetcher{results: map[string]fetcher.Result{}}, clock.NewFake(time.Date(2026, time.September, 1, 12, 0, 0, 0, time.UTC)), id.Generator{}, WithExecutionLimits(2, 2), WithDiscoveryTargets([]DiscoveryTarget{
		{Host: "a.catalog.example", TenantID: tenantID, Audience: AudienceAuthenticated},
		{Host: "b.catalog.example", TenantID: tenantID, Audience: AudienceAuthenticated},
	}))
	if err != nil {
		t.Fatal(err)
	}
	base := Source{ID: "source", TenantID: tenantID, DisplayName: " Source ", Type: SourceARDManifest, Endpoint: "https://source.example/ard.json", ProfileID: "ard-test", Scope: Scope{Kinds: []catalog.Kind{catalog.KindAgent}}, UpdateMode: UpdateManual}
	normalized, err := manager.normalizeAndValidateSource(context.Background(), base)
	if err != nil || normalized.DisplayName != "Source" || normalized.RetrievalMode != RetrievalIndexed || normalized.Live.FederationMode != "none" {
		t.Fatalf("normalized indexed source = %#v, %v", normalized, err)
	}

	live := base
	live.ID = "live-source"
	live.Type = SourceRegistry
	live.Endpoint = "https://registry.example/search"
	live.RegistrySearchURL = live.Endpoint
	live.Live = LivePolicy{Enabled: true, QueryPrivacyAcknowledged: true, FederationMode: "referrals"}
	live.Referrals = ReferralPolicy{Enabled: true}
	live.DiscoveryAssignments = []DiscoveryAssignment{
		{Host: " B.CATALOG.EXAMPLE ", Audience: AudienceAuthenticated},
		{Host: "a.catalog.example", Audience: AudienceAuthenticated},
	}
	normalized, err = manager.normalizeAndValidateSource(context.Background(), live)
	if err != nil || normalized.RetrievalMode != RetrievalLive || normalized.Referrals.MaxHops != 1 || len(normalized.DiscoveryAssignments) != 2 || normalized.DiscoveryAssignments[0].Host != "a.catalog.example" {
		t.Fatalf("normalized live source = %#v, %v", normalized, err)
	}

	invalid := []struct {
		name   string
		mutate func(*Source)
	}{
		{name: "display name", mutate: func(source *Source) { source.DisplayName = "" }},
		{name: "source type", mutate: func(source *Source) { source.Type = SourceType("plugin") }},
		{name: "endpoint", mutate: func(source *Source) { source.Endpoint = "" }},
		{name: "update mode", mutate: func(source *Source) { source.UpdateMode = UpdateMode("continuous") }},
		{name: "empty scope", mutate: func(source *Source) { source.Scope = Scope{} }},
		{name: "network reference", mutate: func(source *Source) { source.NetworkProfile = "Private" }},
		{name: "indexed live", mutate: func(source *Source) { source.RetrievalMode = RetrievalIndexed; source.Live = LivePolicy{Enabled: true} }},
		{name: "live without endpoint", mutate: func(source *Source) { source.RetrievalMode = RetrievalLive }},
		{name: "invalid retrieval", mutate: func(source *Source) { source.RetrievalMode = RetrievalMode("continuous") }},
		{name: "Registry indexed", mutate: func(source *Source) { source.Type = SourceRegistry; source.RetrievalMode = RetrievalIndexed }},
		{name: "referral only", mutate: func(source *Source) { source.Live.ReferralOnly = true }},
		{name: "invalid audience", mutate: func(source *Source) {
			source.DiscoveryAssignments = []DiscoveryAssignment{{Host: "a.catalog.example", Audience: DiscoveryAudience("operators")}}
		}},
	}
	for _, testCase := range invalid {
		t.Run(testCase.name, func(t *testing.T) {
			candidate := base
			testCase.mutate(&candidate)
			if _, err := manager.normalizeAndValidateSource(context.Background(), candidate); err == nil {
				t.Fatalf("invalid source was accepted: %#v", candidate)
			}
		})
	}
}

func TestConnectivityInspectionMustRemainCurrentAndExactlySelected(t *testing.T) {
	t.Parallel()

	const tenantID = "00000000-0000-7000-8000-000000000001"
	now := time.Date(2026, time.September, 1, 12, 0, 0, 0, time.UTC)
	store := NewMemoryStore()
	manager, err := NewManager(store, &routingFetcher{results: map[string]fetcher.Result{}}, clock.NewFake(now), id.Generator{})
	if err != nil {
		t.Fatal(err)
	}
	current := Source{ID: "source-one", TenantID: tenantID, Type: SourceRegistry, Endpoint: "https://registry.example/search", RegistrySearchURL: "https://registry.example/search", ProfileID: "ard-test", NetworkProfile: "public", CredentialBinding: "reader"}
	proposed := current
	proposed.NetworkProfile = "private"
	proposed.CredentialBinding = "partner"
	base := Inspection{TenantID: tenantID, ResolvedURL: current.Endpoint, SourceType: current.Type, ProfileID: current.ProfileID, RegistrySearchURL: current.RegistrySearchURL, NetworkProfile: proposed.NetworkProfile, CredentialBinding: proposed.CredentialBinding, CredentialRevision: "revision-2", PolicyID: "test-policy", PolicyRevision: 1, PolicyDigest: federationTestPolicyDigest, ExpiresAt: now.Add(time.Hour)}
	save := func(name string, mutate func(*Inspection)) Inspection {
		t.Helper()
		value := base
		value.ID = name
		if mutate != nil {
			mutate(&value)
		}
		stored, saveErr := store.SaveInspection(context.Background(), value)
		if saveErr != nil {
			t.Fatal(saveErr)
		}
		return stored
	}

	private := "private"
	partner := "partner"
	valid := save("valid", nil)
	selected, err := manager.applyConnectivityInspection(context.Background(), tenantID, current, proposed, PatchInput{InspectionID: valid.ID, NetworkProfile: &private, CredentialBinding: &partner})
	if err != nil || selected.NetworkProfile != private || selected.CredentialBinding != partner || selected.CredentialRevision != "revision-2" || selected.PolicyDigest != federationTestPolicyDigest || selected.InspectionID != valid.ID {
		t.Fatalf("selected connectivity evidence = %#v, %v", selected, err)
	}

	for _, testCase := range []struct {
		name       string
		inspection Inspection
		input      PatchInput
	}{
		{name: "missing", inspection: Inspection{ID: "missing"}, input: PatchInput{InspectionID: "missing", NetworkProfile: &private, CredentialBinding: &partner}},
		{name: "expired", inspection: save("expired", func(value *Inspection) { value.ExpiresAt = now }), input: PatchInput{NetworkProfile: &private, CredentialBinding: &partner}},
		{name: "policy changed", inspection: save("policy", func(value *Inspection) { value.PolicyDigest = "sha256:" + strings.Repeat("b", 64) }), input: PatchInput{NetworkProfile: &private, CredentialBinding: &partner}},
		{name: "interface changed", inspection: save("interface", func(value *Inspection) { value.ResolvedURL = "https://different.example/search" }), input: PatchInput{NetworkProfile: &private, CredentialBinding: &partner}},
		{name: "selection mismatch", inspection: save("selection", nil), input: PatchInput{NetworkProfile: func() *string { value := "public"; return &value }(), CredentialBinding: &partner}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			input := testCase.input
			if input.InspectionID == "" {
				input.InspectionID = testCase.inspection.ID
			}
			if _, err := manager.applyConnectivityInspection(context.Background(), tenantID, current, proposed, input); catalog.ErrorCodeOf(err) != catalog.CodePreconditionFailed && catalog.ErrorCodeOf(err) != catalog.CodeNotFound {
				t.Fatalf("stale connectivity evidence returned %v", err)
			}
		})
	}
}

func FuzzDiscoveryMetadataParsing(f *testing.F) {
	f.Add(`</.well-known/ard.json>; rel="alternate ard"`, `<html><head><link rel="ard" href="/.well-known/ard.json"></head></html>`)
	f.Add(`malformed`, `<script type="application/ld+json">{"identifier":"urn:air:example.com:agent:test"}</script>`)
	f.Fuzz(func(t *testing.T, linkHeader, htmlBody string) {
		if len(linkHeader) > 16*1024 || len(htmlBody) > 64*1024 {
			t.Skip()
		}
		_ = discoveryLinks([]string{linkHeader}, "https://example.com/catalog")
		_, _ = htmlDiscovery([]byte(htmlBody), "https://example.com/catalog")
	})
}

func hasFinding(findings []catalog.Finding, code string) bool {
	for _, finding := range findings {
		if finding.Code == code {
			return true
		}
	}
	return false
}

func federationResultAt(finalURL, body string) fetcher.Result {
	result := federationResult(body)
	result.FinalURL = finalURL
	return result
}

func TestSourceDisplayNameCharacterBounds(t *testing.T) {
	manager, err := NewManager(NewMemoryStore(), &routingFetcher{results: map[string]fetcher.Result{}}, clock.NewFake(time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)), id.Generator{})
	if err != nil {
		t.Fatal(err)
	}
	base := Source{ID: "source", TenantID: "tenant", DisplayName: "Source", Type: SourceARDManifest, Endpoint: "https://source.example/ard.json", ProfileID: "ard-test", Scope: Scope{Kinds: []catalog.Kind{catalog.KindAgent}}, UpdateMode: UpdateManual}
	for _, count := range []int{0, 159, 160, 161} {
		name := strings.Repeat("🙂", count)
		if count == 0 {
			name = "source\x00name"
		}
		source := base
		source.DisplayName = name
		normalized, err := manager.normalizeAndValidateSource(t.Context(), source)
		patched, patchErr := applySourcePatch(base, PatchInput{DisplayName: &name})
		if count > 0 && count <= 160 {
			if err != nil || patchErr != nil || normalized.DisplayName != name || patched.DisplayName != name {
				t.Fatalf("valid %d-character name: %v, %v", count, err, patchErr)
			}
		} else if catalog.ErrorCodeOf(err) != catalog.CodeInvalidArgument || catalog.ErrorCodeOf(patchErr) != catalog.CodeInvalidArgument {
			t.Fatalf("over-limit name: %v, %v", err, patchErr)
		}
	}
}
