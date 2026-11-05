// Federation fixtures use existing domain ports. Unimplemented operations fail
// immediately; the policy-only fetcher cannot perform outbound requests.
package httpserver

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/audit"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/federation"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/clock"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/id"
)

const federationFixtureAIR = "urn:air:peer.example:default:fixture"

type federationProjectionStore struct {
	federation.Store
	getSource              func(context.Context, string, string) (federation.Source, error)
	getInspection          func(context.Context, string, string) (federation.Inspection, error)
	getSnapshot            func(context.Context, string, string) (federation.Snapshot, error)
	getObservationMetadata func(context.Context, string, string) (federation.Observation, error)
	getObservation         func(context.Context, string, string) (federation.Observation, error)
	sources                func(context.Context, string, federation.SourceListRequest) ([]federation.Source, error)
	snapshots              func(context.Context, string, federation.SnapshotListRequest) ([]federation.Snapshot, error)
	observations           func(context.Context, string, federation.ObservationListRequest) ([]federation.Observation, error)
	occurrences            func(context.Context, string, federation.OccurrenceListRequest) ([]federation.ObservationOccurrence, error)
	search                 func(context.Context, string, federation.ObservationSearchRequest) ([]federation.SearchResult, error)
}

func (s federationProjectionStore) GetSource(c context.Context, tenant string, key string) (federation.Source, error) {
	return s.getSource(c, tenant, key)
}
func (s federationProjectionStore) GetInspection(c context.Context, tenant string, key string) (federation.Inspection, error) {
	return s.getInspection(c, tenant, key)
}
func (s federationProjectionStore) GetSnapshot(c context.Context, tenant string, key string) (federation.Snapshot, error) {
	return s.getSnapshot(c, tenant, key)
}
func (s federationProjectionStore) GetObservation(c context.Context, tenant string, key string) (federation.Observation, error) {
	return s.getObservation(c, tenant, key)
}
func (s federationProjectionStore) GetObservationMetadata(c context.Context, tenant string, key string) (federation.Observation, error) {
	return s.getObservationMetadata(c, tenant, key)
}
func (s federationProjectionStore) ListSourcePage(c context.Context, tenant string, page federation.SourceListRequest) ([]federation.Source, error) {
	return s.sources(c, tenant, page)
}
func (s federationProjectionStore) ListSnapshotPage(c context.Context, tenant string, page federation.SnapshotListRequest) ([]federation.Snapshot, error) {
	return s.snapshots(c, tenant, page)
}
func (s federationProjectionStore) ListObservationPage(c context.Context, tenant string, page federation.ObservationListRequest) ([]federation.Observation, error) {
	return s.observations(c, tenant, page)
}
func (s federationProjectionStore) ListObservationOccurrencePage(c context.Context, tenant string, page federation.OccurrenceListRequest) ([]federation.ObservationOccurrence, error) {
	return s.occurrences(c, tenant, page)
}
func (s federationProjectionStore) SearchObservationPage(c context.Context, tenant string, page federation.ObservationSearchRequest) ([]federation.SearchResult, error) {
	return s.search(c, tenant, page)
}

type federationProjectionEvidence struct {
	federation.EvidenceSink
	appendAudit func(context.Context, audit.Record) error
}

func (s federationProjectionEvidence) AppendAuditEvent(c context.Context, r audit.Record) error {
	return s.appendAudit(c, r)
}

func federationProjectionServer(t *testing.T, store federation.Store, evidence federation.EvidenceSink) *Server {
	t.Helper()
	if evidence == nil {
		evidence = federationProjectionEvidence{}
	}
	manager, err := federation.NewManager(store, publicPolicyFetcher{}, clock.NewFake(projectionTime), id.Generator{}, federation.WithEvidenceSink(evidence, true))
	if err != nil {
		t.Fatal(err)
	}
	s := projectionServer(nil)
	s.federation = manager
	return s
}
func federationFixtureSource() federation.Source {
	return federation.Source{PresentationGeneration: 1, ID: projectionResource, TenantID: projectionTenant, DisplayName: "Fixture source", Type: federation.SourceARDManifest, Endpoint: "https://peer.example/.well-known/ard.json", Status: federation.SourceActive, ProfileID: publicFixtureARDProfile, RetrievalMode: federation.RetrievalIndexed, Scope: federation.Scope{Kinds: []catalog.Kind{catalog.KindAgent}}, UpdateMode: federation.UpdateManual, Live: federation.LivePolicy{FederationMode: "none"}, PolicyID: "default", PolicyRevision: 1, PolicyDigest: (publicPolicyFetcher{}).PolicyDigest(), Revision: 3, CreatedBy: "administrator", CreatedAt: projectionTime.Add(-time.Hour), UpdatedAt: projectionTime.Add(-time.Minute), InspectionID: "private-fixture-transient-inspection"}
}
func federationFixtureInspection() federation.Inspection {
	source := federationFixtureSource()
	return federation.Inspection{ID: projectionItem, TenantID: projectionTenant, RequestedURL: source.Endpoint, ResolvedURL: source.Endpoint, SourceType: source.Type, ProfileID: source.ProfileID, MediaType: "application/ard+json", BodyDigest: "sha256:" + strings.Repeat("b", 64), EntryCount: 0, KindCounts: map[string]int{}, Publishers: []string{}, Tags: []string{}, Capabilities: map[string]bool{}, PolicyID: source.PolicyID, PolicyRevision: source.PolicyRevision, PolicyDigest: source.PolicyDigest, CreatedAt: projectionTime, ExpiresAt: projectionTime.Add(time.Minute)}
}
func federationFixtureSnapshot(t *testing.T) federation.Snapshot {
	t.Helper()
	snapshot := federation.Snapshot{ID: projectionItem, TenantID: projectionTenant, SourceID: projectionResource, SourceRevision: 3, State: federation.SnapshotComplete, EnumerationComplete: true, WithdrawalAuthority: true, BodyDigest: "sha256:" + strings.Repeat("b", 64), PolicyID: "default", PolicyRevision: 1, PolicyDigest: (publicPolicyFetcher{}).PolicyDigest(), StartedAt: projectionTime, CompletedAt: &projectionTime}
	if err := federation.ValidateRetainedSnapshotIntegrity(snapshot); err != nil {
		t.Fatal(err)
	}
	return snapshot
}
func federationFixtureObservation(t *testing.T, present bool) federation.Observation {
	t.Helper()
	version := "1.0.0"
	versionField := `,"version":"1.0.0"`
	if !present {
		version = ""
		versionField = ""
	}
	raw := json.RawMessage(`{"identifier":"` + federationFixtureAIR + `","displayName":"private-fixture-name","type":"application/a2a-agent-card+json"` + versionField + `,"tags":["private-fixture-tag"],"data":{"name":"Fixture agent"}}`)
	normalized, err := json.Marshal(map[string]any{"profile": "catalog-observation-v1", "identifier": federationFixtureAIR, "versionPresent": present, "version": version, "kind": "agent", "mediaType": "application/a2a-agent-card+json", "displayName": "private-fixture-name", "description": "", "url": "", "data": map[string]string{"name": "Fixture agent"}, "tags": []string{"private-fixture-tag"}, "representativeQueries": []string{}, "metadata": nil})
	if err != nil {
		t.Fatal(err)
	}
	exact := audit.DigestBytes(raw)
	o := federation.Observation{ID: projectionItem, TenantID: projectionTenant, SourceID: projectionResource, SnapshotID: projectionNext, SourceKey: "fixture", Identifier: federationFixtureAIR, Version: version, VersionPresent: present, Kind: catalog.KindAgent, MediaType: "application/a2a-agent-card+json", Completeness: federation.CompleteEntry, Compatibility: federation.Compatibility{Structural: "valid", Support: "supported", ProfileID: "a2a"}, RawEntry: raw, NormalizedEntry: normalized, RawEntryAvailable: true, ExactEntryDigest: exact, IdentityDigest: exact, NormalizedEntryDigest: audit.DigestBytes(normalized), NormalizationProfile: "catalog-observation-v1", SearchDisplayName: "private-fixture-name", SearchTags: []string{"private-fixture-tag"}, SearchPublisher: "peer.example", Freshness: federation.FreshCurrent, FirstSeenAt: projectionTime.Add(-time.Hour), LastSeenAt: projectionTime}
	if err := federation.ValidateRetainedObservationIntegrity(o, "", true); err != nil {
		t.Fatal(err)
	}
	return o
}

func federationFixtureOccurrence(t *testing.T) federation.ObservationOccurrence {
	t.Helper()
	o := federationFixtureObservation(t, true)
	occurrence := federation.ObservationOccurrence{ObservationID: o.ID, SourceID: o.SourceID, SnapshotID: projectionNext, SourceRevision: 3, RetrievalDigest: "sha256:" + strings.Repeat("b", 64), EntryLocation: "/entries/0", ExactEntryDigest: o.ExactEntryDigest, NormalizedEntryDigest: o.NormalizedEntryDigest, EvidenceComplete: true, ObservedAt: projectionTime}
	snapshot := federationFixtureSnapshot(t)
	snapshot.ID = occurrence.SnapshotID
	if err := federation.ValidateRetainedOccurrenceIntegrity(occurrence, o, snapshot); err != nil {
		t.Fatal(err)
	}
	return occurrence
}

// Compare JSON-shaped values so json:"-" fields stay private even when present
// in the stored fixture. Raw attachment tests use a separate exact-byte check.
func requireFederationJSON(t *testing.T, w *httptest.ResponseRecorder, status int, want any) {
	t.Helper()
	raw, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	var expected, got any
	if err := json.Unmarshal(raw, &expected); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if w.Code != status || w.Header().Get("Content-Type") != "application/json; charset=utf-8" || !reflect.DeepEqual(got, expected) || strings.Contains(w.Body.String(), "private-fixture") {
		t.Fatalf("response=%d %v %s want=%s", w.Code, w.Header(), w.Body, raw)
	}
}
