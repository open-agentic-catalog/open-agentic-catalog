package federation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/open-agentic-catalog/open-agentic-catalog/test/identityfixture"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/audit"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/events"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/clock"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/fetcher"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/id"
)

const (
	auditEvidenceTenantID     = "00000000-0000-7000-8000-000000000001"
	auditEvidenceRealmID      = "00000000-0000-7000-8000-000000000002"
	auditEvidenceMembershipID = "00000000-0000-7000-8000-000000000003"
)

type recordingFederationEvidence struct {
	audit  []audit.Record
	outbox []catalog.OutboxEvent
	err    error
}

type rejectingSourceUpdateStore struct {
	Store
	err error
}

func (s rejectingSourceUpdateStore) UpdateSource(context.Context, Source, int64, ...MutationEvidence) (Source, error) {
	return Source{}, s.err
}

func (s *recordingFederationEvidence) AppendAuditEvent(_ context.Context, record audit.Record) error {
	if s.err != nil {
		return s.err
	}
	s.audit = append(s.audit, record)
	return nil
}

func (s *recordingFederationEvidence) AppendEvidence(_ context.Context, record audit.Record, outbox *catalog.OutboxEvent) error {
	if s.err != nil {
		return s.err
	}
	s.audit = append(s.audit, record)
	if outbox != nil {
		s.outbox = append(s.outbox, *outbox)
	}
	return nil
}

type fixedAuditFetcher struct {
	result fetcher.Result
	err    error
}

func (f fixedAuditFetcher) Fetch(_ context.Context, request fetcher.Request) (fetcher.Result, error) {
	return withFederationTestEvidence(f.result, request), f.err
}

func (fixedAuditFetcher) PolicyDigest() string { return federationTestPolicyDigest }

type scriptedFederationAuditIDs struct {
	values []string
	err    error
	calls  int
}

func (g *scriptedFederationAuditIDs) New(time.Time) (string, error) {
	if g.calls >= len(g.values) {
		return "", g.err
	}
	value := g.values[g.calls]
	g.calls++
	return value, nil
}

func auditEvidenceContext() context.Context {
	actor := identity.Actor{
		TenantID:     auditEvidenceTenantID,
		Subject:      "external-subject-is-not-audit-identity",
		Roles:        []identity.Role{identity.RoleSourceAdmin},
		RealmID:      auditEvidenceRealmID,
		MembershipID: auditEvidenceMembershipID,
		ActorType:    identity.ActorHuman,
	}
	ctx := identityfixture.WithActor(context.Background(), actor)
	return audit.WithExecution(ctx, audit.Execution{RequestID: "audit-evidence-request", AuthorizationDigest: actor.AuthorizationDigest()})
}

func auditFacts(t *testing.T, record audit.Record) map[string]any {
	t.Helper()
	facts, ok := record.Details["facts"].(map[string]any)
	if !ok {
		t.Fatalf("audit facts are absent: %#v", record.Details)
	}
	return facts
}

func TestSourceInspectionRetainsClassifiedAuditWithoutSensitiveInput(t *testing.T) {
	t.Parallel()

	endpoint := "https://catalog.remote.example/ard.json"
	validBody := `{"entries":[]}`
	tests := []struct {
		name        string
		fetcher     fixedAuditFetcher
		wantOutcome string
		wantCode    catalog.ErrorCode
	}{
		{name: "success", fetcher: fixedAuditFetcher{result: fetcher.Result{Status: http.StatusOK, FinalURL: endpoint, MediaType: "application/ard+json", Body: []byte(validBody)}}, wantOutcome: outboundOutcomeSucceeded},
		{name: "policy refusal", fetcher: fixedAuditFetcher{err: fetcher.ErrPolicyDenied}, wantOutcome: outboundOutcomePolicyDenied, wantCode: catalog.CodeSourcePolicyDenied},
		{name: "transport failure", fetcher: fixedAuditFetcher{err: errors.New("dial failed")}, wantOutcome: outboundOutcomeTransportError, wantCode: catalog.CodeDependencyUnavailable},
		{name: "HTTP failure", fetcher: fixedAuditFetcher{result: fetcher.Result{Status: http.StatusServiceUnavailable, FinalURL: endpoint, MediaType: "application/json", Body: []byte(`{"error":"private-upstream-detail"}`)}}, wantOutcome: outboundOutcomeHTTPError, wantCode: catalog.CodeInvalidArgument},
		{name: "parse failure", fetcher: fixedAuditFetcher{result: fetcher.Result{Status: http.StatusOK, FinalURL: endpoint, MediaType: "application/json", Body: []byte(`{"entries":`)}}, wantOutcome: outboundOutcomeParseError, wantCode: catalog.CodeInvalidArgument},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			evidence := &recordingFederationEvidence{}
			store := NewMemoryStore(evidence)
			manager, err := NewManager(store, test.fetcher, clock.NewFake(time.Date(2026, time.September, 3, 12, 0, 0, 0, time.UTC)), id.Generator{}, WithEvidenceSink(evidence, true))
			if err != nil {
				t.Fatal(err)
			}
			inspection, operationErr := manager.Inspect(auditEvidenceContext(), endpoint)
			if test.wantCode == "" {
				if operationErr != nil || inspection.ID == "" {
					t.Fatalf("inspection = %#v, %v", inspection, operationErr)
				}
			} else if catalog.ErrorCodeOf(operationErr) != test.wantCode || !catalog.ErrorPreservesEvidence(operationErr) || inspection.ID != "" {
				t.Fatalf("classified inspection failure = %#v, %v", inspection, operationErr)
			}
			if len(evidence.audit) != 1 || len(evidence.outbox) != 0 || evidence.audit[0].Action != audit.ActionSourceInspected || evidence.audit[0].Actor != auditEvidenceMembershipID || evidence.audit[0].RequestID != "audit-evidence-request" {
				t.Fatalf("inspection evidence = audit %#v outbox %#v", evidence.audit, evidence.outbox)
			}
			facts := auditFacts(t, evidence.audit[0])
			if facts["outcome"] != test.wantOutcome || facts["policyDigest"] != federationTestPolicyDigest || facts["configurationDigest"] == "" {
				t.Fatalf("inspection facts = %#v", facts)
			}
			encoded, err := json.Marshal(evidence.audit[0])
			if err != nil {
				t.Fatal(err)
			}
			for _, secret := range []string{endpoint, "private-upstream-detail", "external-subject-is-not-audit-identity"} {
				if strings.Contains(string(encoded), secret) {
					t.Fatalf("inspection audit exposed %q: %s", secret, encoded)
				}
			}
		})
	}
}

func TestInspectionAndRawObservationReadsFailClosedBeforeReturningEvidence(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.September, 3, 12, 0, 0, 0, time.UTC)
	inspectionID := "00000000-0000-7000-8000-000000000010"
	observationID := "00000000-0000-7000-8000-000000000011"
	inspection := Inspection{ID: inspectionID, TenantID: auditEvidenceTenantID, RequestedURL: "https://private.example/source", BodyDigest: "sha256:" + strings.Repeat("b", 64), PolicyDigest: federationTestPolicyDigest, CreatedAt: now, ExpiresAt: now.Add(time.Minute)}
	observation := Observation{ID: observationID, TenantID: auditEvidenceTenantID, RawEntry: []byte(`{"private":"document"}`), RawEntryAvailable: true, ExactEntryDigest: "sha256:" + strings.Repeat("c", 64)}

	for _, test := range []struct {
		name string
		read func(*Manager) error
	}{
		{name: "source inspection", read: func(manager *Manager) error {
			loaded, err := manager.GetInspection(auditEvidenceContext(), inspectionID)
			if loaded.ID != "" {
				t.Fatalf("inspection bytes were disclosed: %#v", loaded)
			}
			return err
		}},
		{name: "raw observation", read: func(manager *Manager) error {
			loaded, err := manager.GetRawObservation(auditEvidenceContext(), observationID)
			if loaded.ID != "" || loaded.RawEntry != nil {
				t.Fatalf("raw observation bytes were disclosed: %#v", loaded)
			}
			return err
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			store := NewMemoryStore()
			store.inspections[inspectionID] = inspection
			store.observations[observationID] = observation
			evidence := &recordingFederationEvidence{err: errors.New("audit unavailable")}
			manager, err := NewManager(store, fixedAuditFetcher{}, clock.NewFake(now), id.Generator{}, WithEvidenceSink(evidence, true))
			if err != nil {
				t.Fatal(err)
			}
			if err := test.read(manager); catalog.ErrorCodeOf(err) != catalog.CodeDependencyUnavailable {
				t.Fatalf("audit-write failure returned %v", err)
			}
			if len(evidence.audit) != 0 || len(evidence.outbox) != 0 {
				t.Fatalf("failed append retained partial evidence: %#v %#v", evidence.audit, evidence.outbox)
			}
		})
	}
}

func TestOutboundResultsAreNotReturnedOrRetainedWhenAuditAppendFails(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.September, 3, 12, 0, 0, 0, time.UTC)
	endpoint := "https://catalog.remote.example/ard.json"
	sentinel := errors.New("audit unavailable")
	evidence := &recordingFederationEvidence{err: sentinel}
	store := NewMemoryStore(evidence)
	manager, err := NewManager(store, fixedAuditFetcher{result: fetcher.Result{Status: http.StatusOK, FinalURL: endpoint, MediaType: "application/ard+json", Body: []byte(`{"entries":[]}`)}}, clock.NewFake(now), id.Generator{}, WithEvidenceSink(evidence, true))
	if err != nil {
		t.Fatal(err)
	}
	inspection, err := manager.Inspect(auditEvidenceContext(), endpoint)
	if !errors.Is(err, sentinel) || inspection.ID != "" {
		t.Fatalf("inspection audit failure disclosed a result: %#v, %v", inspection, err)
	}
	if len(store.inspections) != 0 {
		t.Fatalf("inspection was retained without its audit: %#v", store.inspections)
	}

	registryEndpoint := "https://registry.remote.example/ard/v1/search"
	sourceID := "00000000-0000-7000-8000-000000000020"
	store.sources[sourceID] = Source{
		ID: sourceID, TenantID: auditEvidenceTenantID, DisplayName: "Remote", Type: SourceRegistry, Endpoint: registryEndpoint, RegistrySearchURL: registryEndpoint,
		Status: SourceActive, RetrievalMode: RetrievalLive, Scope: Scope{Identifiers: []string{"urn:air:remote.example:default:allowed"}},
		Live: LivePolicy{Enabled: true, QueryPrivacyAcknowledged: true}, PolicyID: "test-policy", PolicyRevision: 1, PolicyDigest: federationTestPolicyDigest, Revision: 1,
	}
	manager.fetcher = fixedAuditFetcher{result: fetcher.Result{Status: http.StatusOK, FinalURL: registryEndpoint, MediaType: "application/json", Body: []byte(`{"results":[{"identifier":"urn:air:remote.example:default:allowed","version":"1","type":"application/a2a-agent-card+json","score":10,"source":"https://remote.example/"}]}`)}}
	response, err := manager.LiveSearch(auditEvidenceContext(), LiveSearchRequest{Text: "private-query", Administrative: true, Reason: "Investigate the approved source"})
	if !errors.Is(err, sentinel) || catalog.ErrorCodeOf(err) != catalog.CodeDependencyUnavailable || len(response.Results) != 0 || len(response.Diagnostics) != 0 {
		t.Fatalf("live-search audit failure disclosed a result: %#v, %v", response, err)
	}
}

func TestSuccessfulSourceTestCommitsItsRevisionAndPublicEvidenceTogether(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.September, 3, 12, 0, 0, 0, time.UTC)
	endpoint := "https://catalog.remote.example/ard.json"
	sourceID := "00000000-0000-7000-8000-000000000020"
	evidence := &recordingFederationEvidence{}
	store := NewMemoryStore(evidence)
	source := Source{
		ID: sourceID, TenantID: auditEvidenceTenantID, DisplayName: "Remote", Type: SourceARDManifest, Endpoint: endpoint,
		Status: SourceActive, ProfileID: "ard-0.91-aa3e598", RetrievalMode: RetrievalIndexed, Scope: Scope{Kinds: []catalog.Kind{catalog.KindAgent}},
		UpdateMode: UpdateManual, Live: LivePolicy{FederationMode: "none"}, PolicyID: "test-policy", PolicyRevision: 1,
		PolicyDigest: federationTestPolicyDigest, Revision: 1, CreatedBy: auditEvidenceMembershipID, CreatedAt: now, UpdatedAt: now,
	}
	if _, err := store.CreateSource(context.Background(), source); err != nil {
		t.Fatal(err)
	}
	manager, err := NewManager(store, fixedAuditFetcher{result: fetcher.Result{Status: http.StatusOK, FinalURL: endpoint, MediaType: "application/ard+json", Body: []byte(`{"entries":[]}`)}}, clock.NewFake(now.Add(-2*time.Second)), id.Generator{}, WithEvidenceSink(evidence, true))
	if err != nil {
		t.Fatal(err)
	}

	result, err := manager.TestSourceAtRevision(auditEvidenceContext(), source.ID, source.Revision)
	if err != nil || result.Status != "succeeded" || result.SourceRevision != 2 {
		t.Fatalf("source test = %#v, %v", result, err)
	}
	stored, err := store.GetSource(context.Background(), source.TenantID, source.ID)
	if err != nil || stored.Revision != 2 {
		t.Fatalf("tested source = %#v, %v", stored, err)
	}
	if len(evidence.audit) != 1 || len(evidence.outbox) != 1 || evidence.audit[0].Action != audit.ActionSourceTested || evidence.outbox[0].Type != events.CanonicalType("source.revision.activated") {
		t.Fatalf("source-test evidence = audit %#v outbox %#v", evidence.audit, evidence.outbox)
	}
	facts := auditFacts(t, evidence.audit[0])
	if fmt.Sprint(facts["beforeRevision"]) != "1" || fmt.Sprint(facts["afterRevision"]) != "2" || facts["outcome"] != outboundOutcomeSucceeded || facts["stateChanged"] != true || evidence.audit[0].BeforeDigest == evidence.audit[0].AfterDigest {
		t.Fatalf("source-test facts = %#v, before=%q after=%q", facts, evidence.audit[0].BeforeDigest, evidence.audit[0].AfterDigest)
	}
	var reference events.NotificationReference
	if err := json.Unmarshal(evidence.outbox[0].Data, &reference); err != nil {
		t.Fatal(err)
	}
	if reference.Action != "source.revision.activated" || reference.TargetID != source.ID || reference.Digest != evidence.audit[0].AfterDigest {
		t.Fatalf("source-test notification = %#v", reference)
	}
	if !result.TestedAt.Equal(now) || !stored.UpdatedAt.Equal(now) || !evidence.audit[0].At.Equal(now) || !evidence.outbox[0].CreatedAt.Equal(now) || evidence.outbox[0].PublishedAt == nil || !evidence.outbox[0].PublishedAt.Equal(now) {
		t.Fatal("clock backstep made the result, source and committed evidence disagree")
	}
}

func TestSourceRemovalRollsBackWhenAuditPersistenceFails(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.September, 3, 12, 30, 0, 0, time.UTC)
	sourceID := "00000000-0000-7000-8000-000000000020"
	observationID := "00000000-0000-7000-8000-000000000021"
	previewID := "00000000-0000-7000-8000-000000000022"
	previewDigest := "sha256:" + strings.Repeat("d", 64)
	sentinel := errors.New("audit unavailable")
	evidence := &recordingFederationEvidence{err: sentinel}
	store := NewMemoryStore(evidence)
	source := Source{ID: sourceID, TenantID: auditEvidenceTenantID, DisplayName: "Remote", Type: SourceARDManifest, Endpoint: "https://catalog.remote.example/ard.json", Status: SourceActive, ProfileID: "ard-0.91-aa3e598", RetrievalMode: RetrievalIndexed, Scope: Scope{Kinds: []catalog.Kind{catalog.KindAgent}}, UpdateMode: UpdateManual, PolicyID: "test-policy", PolicyRevision: 1, PolicyDigest: federationTestPolicyDigest, Revision: 1, CreatedBy: auditEvidenceMembershipID, CreatedAt: now, UpdatedAt: now}
	if _, err := store.CreateSource(context.Background(), source); err != nil {
		t.Fatal(err)
	}
	store.observations[observationID] = Observation{ID: observationID, TenantID: source.TenantID, SourceID: source.ID, SourceKey: "entry-one", ExactEntryDigest: "sha256:" + strings.Repeat("e", 64), Freshness: FreshCurrent}
	observationSet, overflow, err := store.CurrentObservationSet(context.Background(), source.TenantID, source.ID, 100)
	if err != nil {
		t.Fatal(err)
	}
	if overflow {
		t.Fatal("current observation set unexpectedly overflowed")
	}
	store.previews[previewDigest] = ChangePreview{ID: previewID, TenantID: source.TenantID, SourceID: source.ID, SourceRevision: source.Revision, Intent: ChangeRemove, CurrentMatched: observationSet.Count, ObservationSetDigest: observationSet.Digest, Digest: previewDigest, CreatedAt: now, ExpiresAt: now.Add(time.Hour)}
	manager, err := NewManager(store, fixedAuditFetcher{}, clock.NewFake(now), id.Generator{}, WithEvidenceSink(evidence, true))
	if err != nil {
		t.Fatal(err)
	}

	removed, err := manager.SetStatus(auditEvidenceContext(), source.ID, SourceRemoved, source.Revision, StatusChangeInput{Reason: "Remove a retired source", ChangePreviewDigest: previewDigest})
	if !errors.Is(err, sentinel) || removed.ID != "" {
		t.Fatalf("source removal audit failure = %#v, %v", removed, err)
	}
	stored, err := store.GetSource(context.Background(), source.TenantID, source.ID)
	if err != nil || stored.Status != SourceActive || stored.Revision != source.Revision {
		t.Fatalf("source changed despite audit failure: %#v, %v", stored, err)
	}
	observations, _, err := store.ListCurrentObservations(context.Background(), source.TenantID, source.ID, 100)
	if err != nil || len(observations) != 1 || observations[0].Freshness != FreshCurrent {
		t.Fatalf("source observations changed despite audit failure: %#v, %v", observations, err)
	}
}

func TestSourceTestFailureAuditRetainsAvailableResponseMetadata(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.September, 3, 12, 0, 0, 0, time.UTC)
	endpoint := "https://catalog.remote.example/ard.json"
	tests := []struct {
		name          string
		fetcher       fixedAuditFetcher
		wantOutcome   string
		wantHTTPClass string
		wantBody      bool
	}{
		{name: "policy refusal", fetcher: fixedAuditFetcher{err: fetcher.ErrPolicyDenied}, wantOutcome: outboundOutcomePolicyDenied},
		{name: "transport failure", fetcher: fixedAuditFetcher{err: errors.New("dial failed")}, wantOutcome: outboundOutcomeTransportError},
		{name: "HTTP failure", fetcher: fixedAuditFetcher{result: fetcher.Result{Status: http.StatusServiceUnavailable, FinalURL: endpoint, MediaType: "application/json", Body: []byte(`{"private":"detail"}`)}}, wantOutcome: outboundOutcomeHTTPError, wantHTTPClass: "5xx", wantBody: true},
		{name: "parse failure", fetcher: fixedAuditFetcher{result: fetcher.Result{Status: http.StatusOK, FinalURL: endpoint, MediaType: "application/json", Body: []byte(`{"entries":`)}}, wantOutcome: outboundOutcomeParseError, wantHTTPClass: "2xx", wantBody: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			evidence := &recordingFederationEvidence{}
			store := NewMemoryStore(evidence)
			source := Source{
				ID: "00000000-0000-7000-8000-000000000022", TenantID: auditEvidenceTenantID, DisplayName: "Remote", Type: SourceARDManifest, Endpoint: endpoint,
				Status: SourceActive, ProfileID: "ard-0.91-aa3e598", RetrievalMode: RetrievalIndexed, Scope: Scope{Kinds: []catalog.Kind{catalog.KindAgent}},
				UpdateMode: UpdateManual, Live: LivePolicy{FederationMode: "none"}, PolicyID: "test-policy", PolicyRevision: 1,
				PolicyDigest: federationTestPolicyDigest, Revision: 1, CreatedBy: auditEvidenceMembershipID, CreatedAt: now, UpdatedAt: now,
			}
			if _, err := store.CreateSource(context.Background(), source); err != nil {
				t.Fatal(err)
			}
			manager, err := NewManager(store, test.fetcher, clock.NewFake(now), id.Generator{}, WithEvidenceSink(evidence, true))
			if err != nil {
				t.Fatal(err)
			}

			result, err := manager.TestSourceAtRevision(auditEvidenceContext(), source.ID, source.Revision)
			if result.SourceID != "" || catalog.ErrorCodeOf(err) != catalog.CodeDependencyUnavailable || !catalog.ErrorPreservesEvidence(err) {
				t.Fatalf("source test failure = %#v, %v", result, err)
			}
			if len(evidence.audit) != 1 || len(evidence.outbox) != 0 || evidence.audit[0].Action != audit.ActionSourceTested {
				t.Fatalf("source-test failure evidence = audit %#v outbox %#v", evidence.audit, evidence.outbox)
			}
			facts := auditFacts(t, evidence.audit[0])
			gotHTTPClass, _ := facts["httpClass"].(string)
			if facts["outcome"] != test.wantOutcome || gotHTTPClass != test.wantHTTPClass {
				t.Fatalf("source-test failure facts = %#v", facts)
			}
			bodyDigest, hasBodyDigest := facts["bodyDigest"].(string)
			if hasBodyDigest != test.wantBody || test.wantBody && !validPolicyDigest(bodyDigest) {
				t.Fatalf("source-test body evidence = %#v", facts["bodyDigest"])
			}
			encoded, marshalErr := json.Marshal(evidence.audit[0])
			if marshalErr != nil {
				t.Fatal(marshalErr)
			}
			if strings.Contains(string(encoded), "private") || strings.Contains(string(encoded), endpoint) {
				t.Fatalf("source-test evidence retained private response data: %s", encoded)
			}
		})
	}
}

func TestSourceTestRetainsAuditWhenAConcurrentRevisionWinsAfterFetching(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.September, 3, 12, 0, 0, 0, time.UTC)
	endpoint := "https://catalog.remote.example/ard.json"
	sourceID := "00000000-0000-7000-8000-000000000021"
	evidence := &recordingFederationEvidence{}
	base := NewMemoryStore()
	source := Source{
		ID: sourceID, TenantID: auditEvidenceTenantID, DisplayName: "Remote", Type: SourceARDManifest, Endpoint: endpoint,
		Status: SourceActive, ProfileID: "ard-0.91-aa3e598", RetrievalMode: RetrievalIndexed, Scope: Scope{Kinds: []catalog.Kind{catalog.KindAgent}},
		UpdateMode: UpdateManual, Live: LivePolicy{FederationMode: "none"}, PolicyID: "test-policy", PolicyRevision: 1,
		PolicyDigest: federationTestPolicyDigest, Revision: 1, CreatedBy: auditEvidenceMembershipID, CreatedAt: now, UpdatedAt: now,
	}
	if _, err := base.CreateSource(context.Background(), source); err != nil {
		t.Fatal(err)
	}
	conflict := catalog.E(catalog.CodePreconditionFailed, "source revision does not match")
	store := rejectingSourceUpdateStore{Store: base, err: conflict}
	manager, err := NewManager(store, fixedAuditFetcher{result: fetcher.Result{Status: http.StatusOK, FinalURL: endpoint, MediaType: "application/ard+json", Body: []byte(`{"entries":[]}`)}}, clock.NewFake(now), id.Generator{}, WithEvidenceSink(evidence, true))
	if err != nil {
		t.Fatal(err)
	}

	result, err := manager.TestSourceAtRevision(auditEvidenceContext(), source.ID, source.Revision)
	if result.SourceID != "" || catalog.ErrorCodeOf(err) != catalog.CodePreconditionFailed || !catalog.ErrorPreservesEvidence(err) {
		t.Fatalf("concurrent source test = %#v, %v", result, err)
	}
	stored, getErr := base.GetSource(context.Background(), source.TenantID, source.ID)
	if getErr != nil || stored.Revision != source.Revision {
		t.Fatalf("source changed after concurrent test = %#v, %v", stored, getErr)
	}
	if len(evidence.audit) != 1 || len(evidence.outbox) != 0 || evidence.audit[0].Action != audit.ActionSourceTested {
		t.Fatalf("concurrent source-test evidence = audit %#v outbox %#v", evidence.audit, evidence.outbox)
	}
	facts := auditFacts(t, evidence.audit[0])
	if fmt.Sprint(facts["beforeRevision"]) != "1" || fmt.Sprint(facts["afterRevision"]) != "1" || facts["outcome"] != outboundOutcomeNegative || facts["stateChanged"] != false || evidence.audit[0].BeforeDigest != evidence.audit[0].AfterDigest {
		t.Fatalf("concurrent source-test facts = %#v, before=%q after=%q", facts, evidence.audit[0].BeforeDigest, evidence.audit[0].AfterDigest)
	}
}

func TestAdministrativeLiveSearchAuditsSafeClassificationsAfterTheOutboundBoundary(t *testing.T) {
	t.Parallel()

	endpoint := "https://registry.remote.example/ard/v1/search"
	valid := `{"results":[{"identifier":"urn:air:remote.example:default:allowed","version":"1","type":"application/a2a-agent-card+json","score":10,"source":"https://remote.example/"}]}`
	tests := []struct {
		name        string
		fetcher     fixedAuditFetcher
		wantOutcome string
	}{
		{name: "success", fetcher: fixedAuditFetcher{result: fetcher.Result{Status: http.StatusOK, FinalURL: endpoint, MediaType: "application/json", Body: []byte(valid)}}, wantOutcome: outboundOutcomeSucceeded},
		{name: "policy refusal", fetcher: fixedAuditFetcher{err: fetcher.ErrPolicyDenied}, wantOutcome: outboundOutcomePolicyDenied},
		{name: "transport failure", fetcher: fixedAuditFetcher{err: errors.New("dial failed")}, wantOutcome: outboundOutcomeTransportError},
		{name: "HTTP failure", fetcher: fixedAuditFetcher{result: fetcher.Result{Status: http.StatusServiceUnavailable, FinalURL: endpoint, MediaType: "application/json", Body: []byte(valid)}}, wantOutcome: outboundOutcomeHTTPError},
		{name: "parse failure", fetcher: fixedAuditFetcher{result: fetcher.Result{Status: http.StatusOK, FinalURL: endpoint, MediaType: "application/json", Body: []byte(`{"results":`)}}, wantOutcome: outboundOutcomeParseError},
		// Inject expiry at the outbound boundary; the traversal deadline is tested
		// separately by TestLiveFederationUsesOneEndToEndDeadline.
		{name: "deadline", fetcher: fixedAuditFetcher{err: context.DeadlineExceeded}, wantOutcome: outboundOutcomeTransportError},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			evidence := &recordingFederationEvidence{}
			store := NewMemoryStore(evidence)
			sourceID := "00000000-0000-7000-8000-000000000020"
			store.sources[sourceID] = Source{
				ID: sourceID, TenantID: auditEvidenceTenantID, DisplayName: "Remote", Type: SourceRegistry, Endpoint: endpoint, RegistrySearchURL: endpoint,
				Status: SourceActive, RetrievalMode: RetrievalLive, Scope: Scope{Identifiers: []string{"urn:air:remote.example:default:allowed"}},
				Live: LivePolicy{Enabled: true, QueryPrivacyAcknowledged: true}, PolicyID: "test-policy", PolicyRevision: 1, PolicyDigest: federationTestPolicyDigest, Revision: 1,
			}
			manager, err := NewManager(store, test.fetcher, clock.NewFake(time.Date(2026, time.September, 3, 12, 0, 0, 0, time.UTC)), id.Generator{}, WithEvidenceSink(evidence, true))
			if err != nil {
				t.Fatal(err)
			}
			response, err := manager.LiveSearch(auditEvidenceContext(), LiveSearchRequest{Text: "needle-secret-query", PageSize: 10, Administrative: true, Reason: "Investigate an approved source"})
			if err != nil || test.wantOutcome != outboundOutcomeSucceeded && !response.Partial {
				t.Fatalf("live response = %#v, %v", response, err)
			}
			if len(evidence.audit) != 1 || len(evidence.outbox) != 0 || evidence.audit[0].Action != audit.ActionFederationLiveSearchDiagnostic {
				t.Fatalf("live-search evidence = audit %#v outbox %#v", evidence.audit, evidence.outbox)
			}
			facts := auditFacts(t, evidence.audit[0])
			if facts["outcome"] != test.wantOutcome || facts["resultCount"] == nil || facts["diagnosticCount"] == nil {
				t.Fatalf("live-search facts = %#v", facts)
			}
			encoded, err := json.Marshal(evidence.audit[0])
			if err != nil {
				t.Fatal(err)
			}
			for _, secret := range []string{"needle-secret-query", endpoint, `"results"`} {
				if strings.Contains(string(encoded), secret) {
					t.Fatalf("live-search audit exposed %q: %s", secret, encoded)
				}
			}
		})
	}
}

func TestAdministrativeLiveSearchAuditsCompleteMixedPositiveAndEmptyResults(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.September, 3, 12, 0, 0, 0, time.UTC)
	positiveEndpoint := "https://positive.remote.example/ard/v1/search"
	emptyEndpoint := "https://empty.remote.example/ard/v1/search"
	positiveBody := []byte(`{"results":[{"identifier":"urn:air:remote.example:default:allowed","version":"1","type":"application/a2a-agent-card+json","score":10,"source":"https://remote.example/"}]}`)
	evidence := &recordingFederationEvidence{}
	store := NewMemoryStore(evidence)
	for _, source := range []Source{
		{ID: "00000000-0000-7000-8000-000000000020", TenantID: auditEvidenceTenantID, DisplayName: "Positive", Type: SourceRegistry, Endpoint: positiveEndpoint, RegistrySearchURL: positiveEndpoint, Status: SourceActive, RetrievalMode: RetrievalLive, Scope: Scope{Identifiers: []string{"urn:air:remote.example:default:allowed"}}, Live: LivePolicy{Enabled: true, QueryPrivacyAcknowledged: true}, PolicyID: "test-policy", PolicyRevision: 1, PolicyDigest: federationTestPolicyDigest, Revision: 1},
		{ID: "00000000-0000-7000-8000-000000000021", TenantID: auditEvidenceTenantID, DisplayName: "Empty", Type: SourceRegistry, Endpoint: emptyEndpoint, RegistrySearchURL: emptyEndpoint, Status: SourceActive, RetrievalMode: RetrievalLive, Scope: Scope{Identifiers: []string{"urn:air:remote.example:default:allowed"}}, Live: LivePolicy{Enabled: true, QueryPrivacyAcknowledged: true}, PolicyID: "test-policy", PolicyRevision: 1, PolicyDigest: federationTestPolicyDigest, Revision: 1},
	} {
		store.sources[source.ID] = source
	}
	outbound := &routingFetcher{results: map[string]fetcher.Result{
		http.MethodPost + " " + positiveEndpoint: {Status: http.StatusOK, FinalURL: positiveEndpoint, MediaType: "application/json", Body: positiveBody},
		http.MethodPost + " " + emptyEndpoint:    {Status: http.StatusOK, FinalURL: emptyEndpoint, MediaType: "application/json", Body: []byte(`{"results":[]}`)},
	}}
	manager, err := NewManager(store, outbound, clock.NewFake(now), id.Generator{}, WithEvidenceSink(evidence, true))
	if err != nil {
		t.Fatal(err)
	}
	response, err := manager.LiveSearch(auditEvidenceContext(), LiveSearchRequest{Text: "allowed", PageSize: 10, Administrative: true, Reason: "Investigate complete aggregate results"})
	if err != nil || response.Partial || len(response.Results) != 1 {
		t.Fatalf("mixed positive and empty live response = %#v, %v", response, err)
	}
	if len(evidence.audit) != 1 || len(evidence.outbox) != 0 {
		t.Fatalf("mixed live-search evidence = audit %#v outbox %#v", evidence.audit, evidence.outbox)
	}
	facts := auditFacts(t, evidence.audit[0])
	if facts["outcome"] != outboundOutcomeSucceeded || facts["partial"] != false {
		t.Fatalf("mixed live-search audit facts = %#v", facts)
	}
}

func TestAdministrativeLiveSearchAuditsOnlySourcesThatReachedTheOutboundBoundary(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.September, 3, 12, 0, 0, 0, time.UTC)
	endpoint := "https://contacted.remote.example/ard/v1/search"
	contactedID := "00000000-0000-7000-8000-000000000020"
	evidence := &recordingFederationEvidence{}
	store := NewMemoryStore(evidence)
	for _, source := range []Source{
		{ID: contactedID, TenantID: auditEvidenceTenantID, DisplayName: "Contacted", Type: SourceRegistry, Endpoint: endpoint, RegistrySearchURL: endpoint, Status: SourceActive, RetrievalMode: RetrievalLive, Live: LivePolicy{Enabled: true, QueryPrivacyAcknowledged: true}, PolicyID: "test-policy", PolicyRevision: 1, PolicyDigest: federationTestPolicyDigest, Revision: 1},
		{ID: "00000000-0000-7000-8000-000000000021", TenantID: auditEvidenceTenantID, DisplayName: "Paused", Type: SourceRegistry, Endpoint: "https://paused.remote.example/ard/v1/search", RegistrySearchURL: "https://paused.remote.example/ard/v1/search", Status: SourcePaused, RetrievalMode: RetrievalLive, Live: LivePolicy{Enabled: true, QueryPrivacyAcknowledged: true}, PolicyID: "test-policy", PolicyRevision: 1, PolicyDigest: federationTestPolicyDigest, Revision: 1},
		{ID: "00000000-0000-7000-8000-000000000022", TenantID: auditEvidenceTenantID, DisplayName: "Stale", Type: SourceRegistry, Endpoint: "https://stale.remote.example/ard/v1/search", RegistrySearchURL: "https://stale.remote.example/ard/v1/search", Status: SourceActive, RetrievalMode: RetrievalLive, Live: LivePolicy{Enabled: true, QueryPrivacyAcknowledged: true}, PolicyID: "test-policy", PolicyRevision: 1, PolicyDigest: "sha256:" + strings.Repeat("e", 64), Revision: 1},
	} {
		store.sources[source.ID] = source
	}
	body := []byte(`{"results":[{"identifier":"urn:air:remote.example:default:allowed","version":"1","type":"application/a2a-agent-card+json","score":10,"source":"https://remote.example/"}]}`)
	manager, err := NewManager(store, fixedAuditFetcher{result: fetcher.Result{Status: http.StatusOK, FinalURL: endpoint, MediaType: "application/json", Body: body}}, clock.NewFake(now), id.Generator{}, WithEvidenceSink(evidence, true))
	if err != nil {
		t.Fatal(err)
	}
	response, err := manager.LiveSearch(auditEvidenceContext(), LiveSearchRequest{Text: "allowed", PageSize: 10, Administrative: true, Reason: "Investigate contacted source evidence"})
	if err != nil || !response.Partial || len(evidence.audit) != 1 {
		t.Fatalf("live response = %#v, audit = %#v, error = %v", response, evidence.audit, err)
	}
	facts := auditFacts(t, evidence.audit[0])
	selected, selectedOK := facts["contactedSourceIds"].([]any)
	policies, policiesOK := facts["policyDigests"].([]any)
	if !selectedOK || len(selected) != 1 || selected[0] != contactedID || !policiesOK || len(policies) != 1 || policies[0] != federationTestPolicyDigest {
		t.Fatalf("contacted source evidence = %#v", facts)
	}
}

func TestAdministrativeLiveSearchDoesNotAuditBeforeTheOutboundBoundary(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.September, 3, 12, 0, 0, 0, time.UTC)
	endpoint := "https://registry.remote.example/ard/v1/search"
	for _, test := range []struct {
		name        string
		mutate      func(*Source)
		wantCode    catalog.ErrorCode
		wantPartial bool
	}{
		{name: "paused source", mutate: func(source *Source) { source.Status = SourcePaused }, wantCode: catalog.CodeSourcePolicyDenied},
		{name: "stale policy", mutate: func(source *Source) { source.PolicyDigest = "sha256:" + strings.Repeat("e", 64) }, wantCode: catalog.CodeSourcePolicyDenied},
		{name: "invalid retained endpoint", mutate: func(source *Source) { source.RegistrySearchURL = "not-an-endpoint" }, wantPartial: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			evidence := &recordingFederationEvidence{}
			store := NewMemoryStore(evidence)
			sourceID := "00000000-0000-7000-8000-000000000020"
			source := Source{
				ID: sourceID, TenantID: auditEvidenceTenantID, DisplayName: "Remote", Type: SourceRegistry, Endpoint: endpoint, RegistrySearchURL: endpoint,
				Status: SourceActive, RetrievalMode: RetrievalLive, Live: LivePolicy{Enabled: true, QueryPrivacyAcknowledged: true}, PolicyID: "test-policy", PolicyRevision: 1, PolicyDigest: federationTestPolicyDigest, Revision: 1,
			}
			test.mutate(&source)
			store.sources[sourceID] = source
			outbound := &routingFetcher{}
			manager, err := NewManager(store, outbound, clock.NewFake(now), id.Generator{}, WithEvidenceSink(evidence, true))
			if err != nil {
				t.Fatal(err)
			}
			response, operationErr := manager.LiveSearch(auditEvidenceContext(), LiveSearchRequest{Text: "private query", SourceIDs: []string{sourceID}, Administrative: true, Reason: "Investigate configured source state"})
			wrongError := test.wantCode == "" && operationErr != nil || test.wantCode != "" && catalog.ErrorCodeOf(operationErr) != test.wantCode
			if wrongError || response.Partial != test.wantPartial || catalog.ErrorPreservesEvidence(operationErr) {
				t.Fatalf("pre-boundary result = %#v, %v", response, operationErr)
			}
			if len(outbound.requests) != 0 {
				t.Fatalf("pre-boundary rejection attempted %d outbound requests", len(outbound.requests))
			}
			if len(evidence.audit) != 0 || len(evidence.outbox) != 0 {
				t.Fatalf("pre-boundary failure created evidence: %#v %#v", evidence.audit, evidence.outbox)
			}
		})
	}
}

func TestCommitSyncFinalizesWithdrawalEvidenceInsideTheMutation(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.September, 3, 12, 0, 0, 0, time.UTC)
	sourceID := "00000000-0000-7000-8000-000000000030"
	snapshotID := "00000000-0000-7000-8000-000000000031"
	jobID := "00000000-0000-7000-8000-000000000032"
	digest := "sha256:" + strings.Repeat("d", 64)
	evidenceSink := &recordingFederationEvidence{}
	store := NewMemoryStore(evidenceSink)
	source := Source{ID: sourceID, TenantID: auditEvidenceTenantID, Type: SourceARDManifest, Status: SourceActive, Revision: 1, PolicyDigest: digest}
	store.sources[sourceID] = source
	old := Observation{ID: "00000000-0000-7000-8000-000000000033", TenantID: auditEvidenceTenantID, SourceID: sourceID, SourceKey: "retired-entry", ExactEntryDigest: digest, Freshness: FreshCurrent}
	store.observations[observationKey(old)] = old

	completedAt := now.Add(time.Second)
	snapshot := Snapshot{ID: snapshotID, TenantID: auditEvidenceTenantID, SourceID: sourceID, SourceRevision: 1, State: SnapshotComplete, EnumerationComplete: true, WithdrawalAuthority: true, PolicyDigest: digest, StartedAt: now, CompletedAt: &completedAt}
	source.LastCompleteSnapshotID = snapshotID
	source.LastSyncAt = &completedAt
	source.UpdatedAt = completedAt
	manager, err := NewManager(store, fixedAuditFetcher{}, clock.NewFake(completedAt), id.Generator{}, WithEvidenceSink(evidenceSink, true))
	if err != nil {
		t.Fatal(err)
	}
	ctx := audit.WithJobExecution(auditEvidenceContext(), audit.JobExecution{ID: jobID, Kind: "source.sync", Attempt: 1, Initiator: auditEvidenceMembershipID, Trigger: "manual"})
	actor, _ := identity.FromContext(ctx)
	mutationEvidence, err := manager.syncEvidence(ctx, actor, source, snapshot, "")
	if err != nil {
		t.Fatal(err)
	}
	committed, _, err := store.CommitSync(ctx, snapshot, nil, source, 1, mutationEvidence)
	if err != nil {
		t.Fatal(err)
	}
	if committed.Withdrawn != 1 || len(evidenceSink.audit) != 1 || len(evidenceSink.outbox) != 1 {
		t.Fatalf("finalized synchronization = %#v, audit %#v, outbox %#v", committed, evidenceSink.audit, evidenceSink.outbox)
	}
	facts := auditFacts(t, evidenceSink.audit[0])
	if value, ok := facts["withdrawn"].(int); !ok || value != 1 {
		t.Fatalf("final audit withdrawal count = %#v", facts["withdrawn"])
	}
	wantDigest, err := snapshotStateDigest(committed)
	if err != nil {
		t.Fatal(err)
	}
	if evidenceSink.audit[0].AfterDigest != wantDigest {
		t.Fatalf("final audit digest = %q, want %q", evidenceSink.audit[0].AfterDigest, wantDigest)
	}
	var reference events.NotificationReference
	if err := json.Unmarshal(evidenceSink.outbox[0].Data, &reference); err != nil {
		t.Fatal(err)
	}
	if reference.Digest != evidenceSink.audit[0].AfterDigest || reference.TargetID != snapshotID || reference.Action != "source.sync.completed" {
		t.Fatalf("final synchronization notification = %#v", reference)
	}
}

func TestOutboundAuditHelpersKeepOnlyBoundedClassifications(t *testing.T) {
	t.Parallel()

	sentinel := errors.New("upstream failed")
	if withOutboundOutcome(nil, outboundOutcomeParseError) != nil || withOutboundResult(nil, outboundOutcomeHTTPError, fetcher.Result{}) != nil {
		t.Fatal("a nil outbound error was classified")
	}
	classified := withOutboundOutcome(sentinel, outboundOutcomeParseError)
	if classified.Error() != sentinel.Error() || !errors.Is(classified, sentinel) || classifyOutboundOutcome(classified) != outboundOutcomeParseError || outboundEvidenceFromError(classified) != (outboundEvidence{}) {
		t.Fatalf("classified error = %#v / %v", classified, classified)
	}
	digest := "sha256:" + strings.Repeat("d", 64)
	credentialRevision := fmt.Sprintf("revision-%d", 2)
	withEvidence := withOutboundResult(sentinel, outboundOutcomeHTTPError, fetcher.Result{Status: 503, BodyDigest: digest, PolicyDigest: digest, CredentialRevision: credentialRevision})
	got := outboundEvidenceFromError(withEvidence)
	if got.HTTPStatus != 503 || got.BodyDigest != digest || got.PolicyDigest != digest || got.CredentialRevision != credentialRevision || classifyOutboundOutcome(withEvidence) != outboundOutcomeHTTPError {
		t.Fatalf("bounded outbound evidence = %#v", got)
	}
	dropped := outboundEvidenceFromError(withOutboundResult(sentinel, outboundOutcomeHTTPError, fetcher.Result{Status: 99, BodyDigest: "secret", PolicyDigest: "secret", CredentialRevision: strings.Repeat("x", 513)}))
	if dropped != (outboundEvidence{}) || outboundEvidenceFromError(sentinel) != (outboundEvidence{}) {
		t.Fatalf("unsafe outbound evidence was retained: %#v", dropped)
	}

	for _, testCase := range []struct {
		err  error
		want string
	}{
		{err: fetcher.ErrInvalidTarget, want: outboundOutcomeInvalidInput},
		{err: fetcher.ErrPolicyDenied, want: outboundOutcomePolicyDenied},
		{err: fetcher.ErrPolicyChanged, want: outboundOutcomePolicyDenied},
		{err: fetcher.ErrCredentialChanged, want: outboundOutcomePolicyDenied},
		{err: catalog.E(catalog.CodeSourcePolicyDenied, "denied"), want: outboundOutcomePolicyDenied},
		{err: sentinel, want: outboundOutcomeTransportError},
	} {
		if got := classifyOutboundOutcome(testCase.err); got != testCase.want {
			t.Fatalf("classifyOutboundOutcome(%v) = %q, want %q", testCase.err, got, testCase.want)
		}
	}
}

func TestPrimaryDiscoveryFailuresTakePrecedenceAndRetainOnlySafeEvidence(t *testing.T) {
	t.Parallel()

	fallback := errors.New("fallback policy refused a private endpoint")
	primary := errors.New("normative discovery transport failed")
	failure := discoveryFailure{}
	failure.record(outboundOutcomePolicyDenied, fallback)
	failure.recordPrimary(outboundOutcomeTransportError, primary)
	err := failure.err()
	if catalog.ErrorCodeOf(err) != catalog.CodeDependencyUnavailable || classifyOutboundOutcome(err) != outboundOutcomeTransportError || !errors.Is(err, primary) || errors.Is(err, fallback) {
		t.Fatalf("primary transport failure = %v", err)
	}

	digest := "sha256:" + strings.Repeat("e", 64)
	credentialRevision := fmt.Sprintf("revision-%d", 4)
	result := fetcher.Result{
		Status:             http.StatusServiceUnavailable,
		FinalURL:           "https://private.example/path?opaque=value",
		Body:               []byte("private response body"),
		BodyDigest:         digest,
		PolicyDigest:       digest,
		CredentialRevision: credentialRevision,
	}
	primaryHTTP := errors.New("normative discovery returned HTTP 503")
	failure = discoveryFailure{}
	failure.recordPrimaryResult(outboundOutcomeHTTPError, primaryHTTP, result)
	failure.record(outboundOutcomePolicyDenied, fallback)
	failure.recordPrimary(outboundOutcomeTransportError, nil)
	err = failure.err()
	evidence := outboundEvidenceFromError(err)
	if catalog.ErrorCodeOf(err) != catalog.CodeInvalidArgument || classifyOutboundOutcome(err) != outboundOutcomeHTTPError || !errors.Is(err, primaryHTTP) || evidence.HTTPStatus != http.StatusServiceUnavailable || evidence.BodyDigest != digest || evidence.PolicyDigest != digest || evidence.CredentialRevision != result.CredentialRevision {
		t.Fatalf("primary HTTP failure = %v, evidence %#v", err, evidence)
	}
	encoded := fmt.Sprintf("%v %#v", err, evidence)
	if strings.Contains(encoded, result.FinalURL) || strings.Contains(encoded, string(result.Body)) {
		t.Fatalf("discovery failure retained private response material: %s", encoded)
	}

	empty := discoveryFailure{}
	if empty.err() != nil {
		t.Fatalf("empty discovery failure returned %v", empty.err())
	}
}

func TestAuditAggregationAndPresentationHelpersAreDeterministicAndBounded(t *testing.T) {
	t.Parallel()

	for _, testCase := range []struct {
		outcomes []string
		partial  bool
		want     string
	}{
		{want: outboundOutcomeSucceeded},
		{partial: true, want: outboundOutcomePartial},
		{outcomes: []string{outboundOutcomeHTTPError, outboundOutcomeHTTPError}, want: outboundOutcomeHTTPError},
		{outcomes: []string{outboundOutcomeHTTPError, outboundOutcomeParseError}, want: outboundOutcomeHTTPError},
		{outcomes: []string{outboundOutcomeSucceeded, outboundOutcomeNegative}, want: outboundOutcomeSucceeded},
		{outcomes: []string{outboundOutcomeNegative, outboundOutcomeNegative}, want: outboundOutcomeNegative},
		{outcomes: []string{outboundOutcomeHTTPError, outboundOutcomeParseError}, partial: true, want: outboundOutcomePartial},
		{outcomes: []string{outboundOutcomeSucceeded}, partial: true, want: outboundOutcomePartial},
	} {
		if got := aggregateLiveOutcome(testCase.outcomes, testCase.partial); got != testCase.want {
			t.Fatalf("aggregateLiveOutcome(%v, %t) = %q, want %q", testCase.outcomes, testCase.partial, got, testCase.want)
		}
	}
	if originClass(" \t") != "public" || originClass(" private-1 ") != "named_private:private-1" {
		t.Fatalf("origin classes = %q / %q", originClass(" \t"), originClass(" private-1 "))
	}
	capabilities := map[string]bool{"disabled": false}
	for index := 39; index >= 0; index-- {
		capabilities[fmt.Sprintf("capability-%02d", index)] = true
	}
	sorted := sortedCapabilities(capabilities)
	if len(sorted) != 32 || sorted[0] != "capability-00" || sorted[31] != "capability-31" {
		t.Fatalf("bounded capabilities = %#v", sorted)
	}
	for status, want := range map[int]string{99: "", 100: "1xx", 299: "2xx", 599: "5xx", 600: ""} {
		if got := httpClass(status); got != want {
			t.Fatalf("httpClass(%d) = %q, want %q", status, got, want)
		}
	}
}

func TestSynchronizationAuditRequiresExactJobAttributionAndFinalShape(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.September, 3, 12, 0, 0, 0, time.UTC)
	completedAt := now.Add(time.Second)
	digest := "sha256:" + strings.Repeat("d", 64)
	source := Source{ID: "00000000-0000-7000-8000-000000000030", TenantID: auditEvidenceTenantID, Revision: 2, PolicyDigest: digest}
	snapshot := Snapshot{ID: "00000000-0000-7000-8000-000000000031", TenantID: auditEvidenceTenantID, SourceID: source.ID, SourceRevision: source.Revision, State: SnapshotComplete, EnumerationComplete: true, PolicyDigest: digest, StartedAt: now, CompletedAt: &completedAt}
	actor, _ := identity.FromContext(auditEvidenceContext())
	if evidence, err := (&Manager{}).syncEvidence(context.Background(), actor, source, snapshot, ""); err != nil || evidence.Audit.ID != "" {
		t.Fatalf("disabled evidence = %#v, %v", evidence, err)
	}

	sink := &recordingFederationEvidence{}
	manager, err := NewManager(NewMemoryStore(sink), fixedAuditFetcher{}, clock.NewFake(now), id.Generator{}, WithEvidenceSink(sink, true))
	if err != nil {
		t.Fatal(err)
	}
	incompleteSnapshot := snapshot
	incompleteSnapshot.CompletedAt = nil
	if _, err := manager.syncEvidence(audit.WithJobExecution(auditEvidenceContext(), audit.JobExecution{ID: "00000000-0000-7000-8000-000000000032", Kind: "source.sync", Attempt: 1, Trigger: "manual"}), actor, source, incompleteSnapshot, ""); catalog.ErrorCodeOf(err) != catalog.CodeInternal {
		t.Fatalf("snapshot without completion time returned %v", err)
	}
	wrongSource := source
	wrongSource.ID = "00000000-0000-7000-8000-000000000099"
	if _, err := manager.syncEvidence(audit.WithJobExecution(auditEvidenceContext(), audit.JobExecution{ID: "00000000-0000-7000-8000-000000000032", Kind: "source.sync", Attempt: 1, Trigger: "manual"}), actor, wrongSource, snapshot, ""); catalog.ErrorCodeOf(err) != catalog.CodeInternal {
		t.Fatalf("snapshot bound to another source returned %v", err)
	}
	for _, job := range []audit.JobExecution{
		{},
		{ID: "not-a-uuid", Kind: "source.sync", Attempt: 1, Trigger: "manual"},
		{ID: "00000000-0000-7000-8000-000000000032", Kind: "other", Attempt: 1, Trigger: "manual"},
		{ID: "00000000-0000-7000-8000-000000000032", Kind: "source.sync", Attempt: 0, Trigger: "manual"},
		{ID: "00000000-0000-7000-8000-000000000032", Kind: "source.sync", Attempt: 1, Trigger: "scheduled"},
	} {
		ctx := auditEvidenceContext()
		if job != (audit.JobExecution{}) {
			ctx = audit.WithJobExecution(ctx, job)
		}
		if _, err := manager.syncEvidence(ctx, actor, source, snapshot, ""); catalog.ErrorCodeOf(err) != catalog.CodeInternal {
			t.Fatalf("invalid Job attribution %#v returned %v", job, err)
		}
	}

	jobID := "00000000-0000-7000-8000-000000000032"
	ctx := audit.WithJobExecution(auditEvidenceContext(), audit.JobExecution{ID: jobID, Kind: "source.sync", Attempt: 1, Initiator: "external-subject", Trigger: "manual"})
	evidence, err := manager.syncEvidence(ctx, actor, source, snapshot, "")
	if err != nil || evidence.Audit.Action != audit.ActionSourceSyncCompleted || evidence.Outbox == nil {
		t.Fatalf("complete synchronization evidence = %#v, %v", evidence, err)
	}
	facts := auditFacts(t, evidence.Audit)
	if _, present := facts["initiator"]; present {
		t.Fatalf("untrusted initiator was retained: %#v", facts)
	}

	// Finalization must recheck attribution even when action, identity and
	// synchronization facts were valid when the evidence was constructed.
	if _, err := FinalizeSyncEvidence(evidence, snapshot); err != nil {
		t.Fatalf("valid final synchronization attribution = %v", err)
	}
	invalidAttribution := evidence
	invalidAttribution.Audit.Details = make(map[string]any, len(evidence.Audit.Details)+1)
	for name, value := range evidence.Audit.Details {
		invalidAttribution.Audit.Details[name] = value
	}
	invalidAttribution.Audit.Details["realmId"] = auditEvidenceRealmID
	if got, err := FinalizeSyncEvidence(invalidAttribution, snapshot); catalog.ErrorCodeOf(err) != catalog.CodeInternal || !strings.Contains(err.Error(), "validate final source synchronization evidence") || !strings.Contains(err.Error(), "service-executor audit attribution contains fields for another principal type") || got.Audit.ID != "" || got.Outbox != nil {
		t.Fatalf("invalid final synchronization attribution = %#v, %v", got, err)
	}
	if _, present := evidence.Audit.Details["realmId"]; present {
		t.Fatal("invalid-attribution fixture changed the valid evidence")
	}

	if got, err := FinalizeSyncEvidence(MutationEvidence{}, snapshot); err != nil || got.Audit.ID != "" {
		t.Fatalf("empty finalization = %#v, %v", got, err)
	}
	wrong := evidence
	wrong.Audit.Action = audit.ActionSourceConnected
	if _, err := FinalizeSyncEvidence(wrong, snapshot); catalog.ErrorCodeOf(err) != catalog.CodeInternal {
		t.Fatalf("unexpected action returned %v", err)
	}
	missingFacts := evidence
	missingFacts.Audit.Details = make(map[string]any, len(evidence.Audit.Details))
	for name, value := range evidence.Audit.Details {
		missingFacts.Audit.Details[name] = value
	}
	delete(missingFacts.Audit.Details, "facts")
	if _, err := FinalizeSyncEvidence(missingFacts, snapshot); catalog.ErrorCodeOf(err) != catalog.CodeInternal {
		t.Fatalf("missing facts returned %v", err)
	}
	withoutOutbox := evidence
	withoutOutbox.Outbox = nil
	if finalized, err := FinalizeSyncEvidence(withoutOutbox, snapshot); err != nil || finalized.Outbox != nil {
		t.Fatalf("audit-only finalization = %#v, %v", finalized, err)
	}

	failedSnapshot := snapshot
	failedSnapshot.State = SnapshotFailed
	failedSnapshot.EnumerationComplete = false
	failedEvidence, err := manager.syncEvidence(ctx, actor, source, failedSnapshot, "dependency")
	if err != nil {
		t.Fatal(err)
	}
	finalized, err := FinalizeSyncEvidence(failedEvidence, failedSnapshot)
	if err != nil || finalized.Audit.Action != audit.ActionSourceSyncFailed || finalized.Outbox == nil || finalized.Outbox.Type != events.CanonicalType("source.sync.failed") {
		t.Fatalf("failed synchronization finalization = %#v, %v", finalized, err)
	}
}

func TestFederationAuditConstructionFailurePathsFailClosed(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.September, 3, 12, 0, 0, 0, time.UTC)
	actor, _ := identity.FromContext(auditEvidenceContext())
	sentinel := errors.New("identifier unavailable")
	sink := &recordingFederationEvidence{}
	manager := &Manager{evidence: sink, clock: clock.NewFake(now), ids: &scriptedFederationAuditIDs{err: sentinel}}
	if err := manager.appendLiveSearchAudit(auditEvidenceContext(), actor, LiveSearchRequest{Reason: "Investigate source"}, nil, LiveSearchResponse{}, outboundOutcomeSucceeded); !errors.Is(err, sentinel) {
		t.Fatalf("live-search identifier failure = %v", err)
	}
	if record, err := manager.auditRecord(context.Background(), actor, audit.ActionSourceInspected, "source_inspection", "00000000-0000-7000-8000-000000000040", "", func() {}, now); err == nil || record.ID != "" {
		t.Fatalf("unencodable facts = %#v, %v", record, err)
	}

	firstID := "00000000-0000-7000-8000-000000000041"
	manager.ids = &scriptedFederationAuditIDs{values: []string{firstID}, err: sentinel}
	_, err := manager.mutationEvidence(auditEvidenceContext(), actor, audit.ActionSourceConnected, "federation_source", "00000000-0000-7000-8000-000000000042", "Connect source", sourceConnectionFacts{InspectionID: "00000000-0000-7000-8000-000000000043", SourceRevision: 1, ConfigurationDigest: federationTestPolicyDigest, RetrievalMode: RetrievalIndexed, UpdateMode: UpdateManual, PolicyDigest: federationTestPolicyDigest}, "", federationTestPolicyDigest, "source.connected", "federation_source", now)
	if !errors.Is(err, sentinel) {
		t.Fatalf("outbox identifier failure = %v", err)
	}

	manager.ids = &scriptedFederationAuditIDs{values: []string{firstID, "00000000-0000-7000-8000-000000000044"}}
	_, err = manager.mutationEvidence(auditEvidenceContext(), actor, audit.ActionSourceConnected, "federation_source", "00000000-0000-7000-8000-000000000042", "Connect source", sourceConnectionFacts{InspectionID: "00000000-0000-7000-8000-000000000043", SourceRevision: 1, ConfigurationDigest: federationTestPolicyDigest, RetrievalMode: RetrievalIndexed, UpdateMode: UpdateManual, PolicyDigest: federationTestPolicyDigest}, "", federationTestPolicyDigest, "internal.unknown", "federation_source", now)
	if err == nil {
		t.Fatal("an unregistered public event was accepted")
	}

	manager.ids = &scriptedFederationAuditIDs{values: []string{firstID, "00000000-0000-7000-8000-000000000044"}}
	precise := now.Add(789 * time.Nanosecond)
	paired, err := manager.mutationEvidence(auditEvidenceContext(), actor, audit.ActionSourceConnected, "federation_source", "00000000-0000-7000-8000-000000000042", "Connect source", sourceConnectionFacts{InspectionID: "00000000-0000-7000-8000-000000000043", SourceRevision: 1, ConfigurationDigest: federationTestPolicyDigest, RetrievalMode: RetrievalIndexed, UpdateMode: UpdateManual, PolicyDigest: federationTestPolicyDigest}, "", federationTestPolicyDigest, "source.connected", "federation_source", precise)
	if err != nil || paired.Outbox == nil || !paired.Outbox.CreatedAt.Equal(paired.Audit.At) || paired.Audit.At.Nanosecond()%1000 != 0 {
		t.Fatalf("normalized paired evidence = %#v, %v", paired, err)
	}

	manager.evidence = nil
	if got, err := manager.mutationEvidence(context.Background(), actor, "", "", "", "", nil, "", "", "", "", now); err != nil || got.Audit.ID != "" {
		t.Fatalf("disabled mutation evidence = %#v, %v", got, err)
	}
}

func TestFederationMutationEvidenceIsBoundToItsExactMutation(t *testing.T) {
	t.Parallel()

	tenantID := auditEvidenceTenantID
	targetID := "00000000-0000-7000-8000-000000000050"
	otherID := "00000000-0000-7000-8000-000000000051"
	now := time.Date(2026, time.September, 3, 12, 0, 0, 0, time.UTC)
	digest := "sha256:" + strings.Repeat("a", 64)
	principal := audit.Principal{ID: auditEvidenceMembershipID, Type: audit.PrincipalCatalogMembership, ActorType: "human", RealmID: auditEvidenceRealmID}
	recordFor := func(idValue, action, targetType, reason string, facts map[string]any) audit.Record {
		record, err := audit.NewRecord(idValue, tenantID, principal, action, targetType, targetID, reason, audit.Execution{}, facts, now)
		if err != nil {
			t.Fatal(err)
		}
		record.AfterDigest = digest
		if err := audit.ValidateNewRecord(record); err != nil {
			t.Fatal(err)
		}
		return record
	}
	outboxFor := func(idValue, publicAction string, record audit.Record) *catalog.OutboxEvent {
		data, err := events.MarshalReference(publicAction, record.TargetType, targetID, record.AfterDigest)
		if err != nil {
			t.Fatal(err)
		}
		return &catalog.OutboxEvent{ID: idValue, TenantID: tenantID, Type: events.CanonicalType(publicAction), Subject: targetID, Data: data, CreatedAt: record.At}
	}
	base := MutationEvidence{
		Audit:  audit.Record{ID: "00000000-0000-7000-8000-000000000052", TenantID: tenantID, TargetID: targetID},
		Outbox: &catalog.OutboxEvent{TenantID: tenantID, Subject: targetID},
	}
	connectedRecord := recordFor("00000000-0000-7000-8000-000000000052", audit.ActionSourceConnected, "federation_source", "Connect reviewed source", map[string]any{"inspectionId": otherID, "sourceRevision": 1, "configurationDigest": digest, "retrievalMode": "indexed", "updateMode": "manual_review", "policyDigest": digest})
	versioned := MutationEvidence{Audit: connectedRecord, Outbox: outboxFor("00000000-0000-7000-8000-000000000053", "source.connected", connectedRecord)}
	testedRecord := recordFor("00000000-0000-7000-8000-000000000054", audit.ActionSourceTested, "federation_source", "", map[string]any{"beforeRevision": 1, "afterRevision": 2, "outcome": "succeeded", "stateChanged": true, "httpClass": "2xx", "policyDigest": digest, "bodyDigest": digest})
	tested := MutationEvidence{Audit: testedRecord, Outbox: outboxFor("00000000-0000-7000-8000-000000000055", "source.revision.activated", testedRecord)}
	inspectedRecord := recordFor("00000000-0000-7000-8000-000000000056", audit.ActionSourceInspected, "source_inspection", "", map[string]any{"outcome": "succeeded", "originClass": "public", "policyDigest": digest, "configurationDigest": digest, "inspectionDigest": digest, "expiresAt": now.Add(time.Hour).Format(time.RFC3339Nano)})
	inspected := MutationEvidence{Audit: inspectedRecord}
	wrongType := *versioned.Outbox
	wrongType.Type = catalog.CanonicalEventType(audit.ActionSourcePaused)
	wrongData := *versioned.Outbox
	wrongData.Data = json.RawMessage(`{"action":"source.connected","targetType":"federation_source","targetId":"` + targetID + `","digest":"sha256:` + strings.Repeat("b", 64) + `"}`)
	wrongTime := *versioned.Outbox
	wrongTime.CreatedAt = now.Add(time.Second)
	wrongPublishedTime := *versioned.Outbox
	publishedAt := now.Add(time.Second)
	wrongPublishedTime.PublishedAt = &publishedAt
	internal := *versioned.Outbox
	internal.Internal = true
	leased := *versioned.Outbox
	leased.LeaseOwner = "worker"
	for _, testCase := range []struct {
		name     string
		evidence []MutationEvidence
		valid    bool
	}{
		{name: "absent", valid: true},
		{name: "empty placeholder", evidence: []MutationEvidence{{}}, valid: true},
		{name: "unversioned bundle", evidence: []MutationEvidence{base}},
		{name: "valid versioned bundle", evidence: []MutationEvidence{versioned}, valid: true},
		{name: "valid tested-source mapping", evidence: []MutationEvidence{tested}, valid: true},
		{name: "valid audit-only inspection", evidence: []MutationEvidence{inspected}, valid: true},
		{name: "ambiguous", evidence: []MutationEvidence{{}, {}}},
		{name: "orphan notification", evidence: []MutationEvidence{{Outbox: &catalog.OutboxEvent{TenantID: tenantID, Subject: targetID}}}},
		{name: "audit tenant", evidence: []MutationEvidence{{Audit: audit.Record{ID: base.Audit.ID, TenantID: otherID, TargetID: targetID}}}},
		{name: "audit target", evidence: []MutationEvidence{{Audit: audit.Record{ID: base.Audit.ID, TenantID: tenantID, TargetID: otherID}}}},
		{name: "notification tenant", evidence: []MutationEvidence{{Audit: base.Audit, Outbox: &catalog.OutboxEvent{TenantID: otherID, Subject: targetID}}}},
		{name: "notification target", evidence: []MutationEvidence{{Audit: base.Audit, Outbox: &catalog.OutboxEvent{TenantID: tenantID, Subject: otherID}}}},
		{name: "notification type", evidence: []MutationEvidence{{Audit: versioned.Audit, Outbox: &wrongType}}},
		{name: "notification data", evidence: []MutationEvidence{{Audit: versioned.Audit, Outbox: &wrongData}}},
		{name: "notification time", evidence: []MutationEvidence{{Audit: versioned.Audit, Outbox: &wrongTime}}},
		{name: "notification published time", evidence: []MutationEvidence{{Audit: versioned.Audit, Outbox: &wrongPublishedTime}}},
		{name: "internal notification", evidence: []MutationEvidence{{Audit: versioned.Audit, Outbox: &internal}}},
		{name: "leased notification", evidence: []MutationEvidence{{Audit: versioned.Audit, Outbox: &leased}}},
		{name: "missing required notification", evidence: []MutationEvidence{{Audit: versioned.Audit}}},
		{name: "notification for audit-only action", evidence: []MutationEvidence{{Audit: inspected.Audit, Outbox: versioned.Outbox}}},
		{name: "unrelated audit action", evidence: []MutationEvidence{{Audit: audit.Record{FormatVersion: connectedRecord.FormatVersion, ID: base.Audit.ID, TenantID: tenantID, Action: "namespace.created", TargetID: targetID}}}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			err := ValidateMutationEvidence(tenantID, targetID, testCase.evidence)
			if (err == nil) != testCase.valid {
				t.Fatalf("evidence validation = %v, valid = %t", err, testCase.valid)
			}
		})
	}

	wantNotifications := map[string]string{
		audit.ActionSourceConnected:         "source.connected",
		audit.ActionSourceRevisionActivated: "source.revision.activated",
		audit.ActionSourceTested:            "source.revision.activated",
		audit.ActionSourcePaused:            "source.paused",
		audit.ActionSourceResumed:           "source.resumed",
		audit.ActionSourceRemoved:           "source.removed",
		audit.ActionSourceSyncCompleted:     "source.sync.completed",
		audit.ActionSourceSyncFailed:        "source.sync.failed",
	}
	for action, publicAction := range wantNotifications {
		if got := federationNotificationType(action); got != catalog.CanonicalEventType(publicAction) {
			t.Errorf("notification type for %q = %q", action, got)
		}
		if got := federationNotificationAction(action); got != publicAction {
			t.Errorf("notification action for %q = %q", action, got)
		}
	}
	for _, action := range []string{audit.ActionSourceInspected, audit.ActionSourceChangePreviewCreated} {
		if !federationAuditOnlyMutation(action) || federationNotificationType(action) != "" {
			t.Errorf("audit-only source action %q has a notification classification", action)
		}
	}
	if federationAuditOnlyMutation("namespace.created") || federationNotificationType("namespace.created") != "" || federationNotificationAction("namespace.created") != "" {
		t.Fatal("an unrelated action entered the federation evidence classification")
	}
	if err := ValidateMutationEvidence(tenantID, targetID, []MutationEvidence{versioned}, audit.ActionSourcePaused); catalog.ErrorCodeOf(err) != catalog.CodeInternal {
		t.Fatalf("connection evidence was accepted for a pause store operation: %v", err)
	}
}

func TestCommitSyncRollsBackEveryMapWhenEvidenceCannotBeFinalizedOrAppended(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.September, 3, 12, 0, 0, 0, time.UTC)
	completedAt := now.Add(time.Second)
	tenantID := auditEvidenceTenantID
	sourceID := "00000000-0000-7000-8000-000000000060"
	snapshotID := "00000000-0000-7000-8000-000000000061"
	digest := "sha256:" + strings.Repeat("e", 64)
	source := Source{ID: sourceID, TenantID: tenantID, Type: SourceARDManifest, Status: SourceActive, Revision: 1, PolicyDigest: digest, UpdatedAt: completedAt}
	snapshot := Snapshot{ID: snapshotID, TenantID: tenantID, SourceID: sourceID, SourceRevision: 1, State: SnapshotComplete, EnumerationComplete: true, PolicyDigest: digest, StartedAt: now, CompletedAt: &completedAt}

	for _, testCase := range []struct {
		name     string
		evidence MutationEvidence
	}{
		{name: "finalization", evidence: MutationEvidence{Audit: audit.Record{ID: "00000000-0000-7000-8000-000000000062", Action: audit.ActionSourceConnected}}},
		{name: "append validation", evidence: MutationEvidence{Outbox: &catalog.OutboxEvent{TenantID: tenantID, Subject: snapshotID}}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			store := NewMemoryStore()
			store.sources[sourceID] = source
			store.retrievals["retained"] = RetrievalState{RequestKey: "retained"}
			if committed, updated, err := store.CommitSync(context.Background(), snapshot, nil, source, source.Revision, testCase.evidence); err == nil || committed.ID != "" || updated.ID != "" {
				t.Fatalf("failed synchronization commit = %#v, %#v, %v", committed, updated, err)
			}
			storedSource := store.sources[sourceID]
			if len(store.sources) != 1 || storedSource.ID != source.ID || storedSource.Revision != source.Revision || storedSource.LastCompleteSnapshotID != source.LastCompleteSnapshotID || len(store.snapshots) != 0 || len(store.observations) != 0 || len(store.occurrences) != 0 || len(store.retrievals) != 1 || store.retrievals["retained"].RequestKey != "retained" {
				t.Fatalf("failed synchronization changed store: sources %#v snapshots %#v observations %#v occurrences %#v retrievals %#v", store.sources, store.snapshots, store.observations, store.occurrences, store.retrievals)
			}
		})
	}
}
