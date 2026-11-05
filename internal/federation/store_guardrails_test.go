package federation

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/audit"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/clock"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/fetcher"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/id"
)

func TestFederationDocumentParsingPreservesInvalidEvidenceSafely(t *testing.T) {
	t.Parallel()

	for name, test := range map[string]struct {
		document  []byte
		mediaType string
	}{
		"ambiguous":          {document: []byte(`{"entries":[],"entries":[]}`), mediaType: "application/json"},
		"not object":         {document: []byte(`[]`), mediaType: "application/json"},
		"missing entries":    {document: []byte(`{}`), mediaType: "application/json"},
		"invalid entry":      {document: []byte(`{"entries":["entry"]}`), mediaType: "application/json"},
		"invalid AI Catalog": {document: []byte(`{"specVersion":"1.0","entries":[{"identifier":"item"}]}`), mediaType: aiCatalogDocumentMediaType},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := parseDocumentWithContextInputs(test.document, test.mediaType, nil); catalog.ErrorCodeOf(err) != catalog.CodeInvalidArgument {
				t.Fatalf("parse returned %v", err)
			}
		})
	}
	unsupported := []byte(`{"specVersion":"2.0","entries":[]}`)
	if _, err := parseDocumentWithContextInputs(unsupported, "application/ai-catalog+json", nil); catalog.ErrorCodeOf(err) != catalog.CodeUnsupportedProfile {
		t.Fatalf("unsupported AI Catalog version returned %v", err)
	}
	openDocument, err := parseDocumentWithContextInputs([]byte(`{"entries":[],"futureField":true}`), "application/json", nil)
	if err != nil || openDocument.sourceType != SourceARDManifest {
		t.Fatalf("open standards document = %#v, %v", openDocument, err)
	}
	invalidARD, err := parseDocumentWithContextInputs([]byte(`{"entries":[{"identifier":"urn:air:remote.example:default:item","displayName":"Item","type":"invalid","data":{}}]}`), "application/json", nil)
	if err != nil || len(invalidARD.entries) != 1 || !hasFinding(invalidARD.entries[0].StructuralFindings, "ARD_ENTRY_SCHEMA_INVALID") {
		t.Fatalf("invalid ARD evidence = %#v, %v", invalidARD, err)
	}
	invalidContext, err := parseDocumentWithContextInputs([]byte(`{"entries":[{"@context":"https://contexts.example.com/missing","identifier":"urn:air:remote.example:default:item","displayName":"Item","type":"application/json","data":{}}]}`), "application/json", nil)
	if err != nil || !hasFinding(invalidContext.entries[0].StructuralFindings, "ARD_JSONLD_EXPANSION_FAILED") {
		t.Fatalf("invalid context evidence = %#v, %v", invalidContext, err)
	}
	for name, raw := range map[string]json.RawMessage{
		"empty": nil, "object": json.RawMessage(`{}`),
		"opaque numbers": json.RawMessage(`{"extra":1e1000,"nested":[{"tiny":1e-1000,"precise":9007199254740993}]}`),
	} {
		if !validARDResultData(raw) {
			t.Fatalf("%s ARD result data was rejected", name)
		}
	}
	for name, raw := range map[string]json.RawMessage{
		"array": json.RawMessage(`[]`), "scalar": json.RawMessage(`"value"`), "invalid": json.RawMessage(`{`),
		"null": json.RawMessage(`null`), "number": json.RawMessage(`1e1000`),
		"duplicate":       json.RawMessage(`{"x":{"y":1,"y":2}}`),
		"invalid Unicode": json.RawMessage(`{"x":"\ud800"}`), "trailing": json.RawMessage(`{} {}`),
	} {
		if validARDResultData(raw) {
			t.Fatalf("%s ARD result data was accepted", name)
		}
	}
}

func TestFederationManagerFetchesRemoteJSONLDContextsThroughItsBoundary(t *testing.T) {
	t.Parallel()

	contextURL := "https://contexts.example.com/ard"
	document := []byte(`{"entries":[{"@context":"` + contextURL + `","identifier":"urn:air:remote.example:default:item","label":"Item","artifactType":"application/json","artifactUrl":"https://example.com/item","representativeQueries":["Find item resources","Show item resources"]}]}`)
	contextBody := `{"@context":{"@version":1.1,"label":"https://agenticresourcediscovery.org/ns#displayName","artifactType":"https://agenticresourcediscovery.org/ns#mediaType","artifactUrl":{"@id":"https://agenticresourcediscovery.org/ns#url","@type":"@id"}}}`
	contextCalls := 0
	direct, err := parseDocumentWithContextInputs(document, "application/ard+json", func(uri string) ([]byte, string, error) {
		contextCalls++
		if uri != contextURL {
			return nil, "", errors.New("unexpected context URL")
		}
		return []byte(contextBody), "", nil
	})
	if err != nil || contextCalls != 1 || len(direct.entries) != 1 || direct.entries[0].DisplayName != "Item" || direct.entries[0].Type != "application/json" || direct.entries[0].URL != "https://example.com/item" {
		t.Fatalf("direct context parsing = %#v, calls=%d, %v", direct, contextCalls, err)
	}
	for name, result := range map[string]fetcher.Result{
		"success":             {Status: http.StatusOK, Body: []byte(contextBody)},
		"HTTP failure":        {Status: http.StatusBadGateway},
		"unsupported version": {Status: http.StatusOK, Body: []byte(strings.Replace(contextBody, `"@version":1.1`, `"@version":1.2`, 1))},
		"string version":      {Status: http.StatusOK, Body: []byte(strings.Replace(contextBody, `"@version":1.1`, `"@version":"1.1"`, 1))},
	} {
		t.Run(name, func(t *testing.T) {
			outbound := &routingFetcher{results: map[string]fetcher.Result{"GET " + contextURL: result}}
			manager, err := NewManager(NewMemoryStore(), outbound, clock.NewFake(time.Now()), id.Generator{})
			if err != nil {
				t.Fatal(err)
			}
			parsed, parseErr := manager.parseDocumentWithAccess(context.Background(), document, "application/json", sourceAccess{})
			if parseErr != nil {
				t.Fatal(parseErr)
			}
			failed := hasFinding(parsed.entries[0].StructuralFindings, "ARD_JSONLD_EXPANSION_FAILED")
			if failed != (name != "success") {
				t.Fatalf("findings = %#v", parsed.entries[0].StructuralFindings)
			}
		})
	}
	outbound := &routingFetcher{results: map[string]fetcher.Result{}}
	manager, err := NewManager(NewMemoryStore(), outbound, clock.NewFake(time.Now()), id.Generator{})
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := manager.parseDocumentWithAccess(context.Background(), document, "application/json", sourceAccess{})
	if err != nil || !hasFinding(parsed.entries[0].StructuralFindings, "ARD_JSONLD_EXPANSION_FAILED") {
		t.Fatalf("fetch failure findings = %#v, %v", parsed, err)
	}
}

func TestFederationMemoryStoreRejectsAmbiguousOrOrphanEvidence(t *testing.T) {
	t.Parallel()

	for name, evidence := range map[string][]MutationEvidence{
		"multiple evidence records":  {{}, {}},
		"notification without audit": {{Outbox: &catalog.OutboxEvent{ID: "outbox"}}},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			store := NewMemoryStore()
			inspection := Inspection{ID: "inspection", TenantID: "tenant"}
			if _, err := store.SaveInspection(context.Background(), inspection, evidence...); catalog.ErrorCodeOf(err) != catalog.CodeInternal {
				t.Fatalf("invalid evidence returned %v", err)
			}
			if _, err := store.GetInspection(context.Background(), inspection.TenantID, inspection.ID); catalog.ErrorCodeOf(err) != catalog.CodeNotFound {
				t.Fatalf("mutation survived rejected evidence: %v", err)
			}
		})
	}
}

func TestFederationMemoryStoreIsolationOrderingAndReconciliation(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store := NewMemoryStore()
	now := time.Date(2026, time.August, 30, 12, 0, 0, 0, time.UTC)
	tenantID := "tenant-one"
	otherTenant := "tenant-two"
	inspection := Inspection{ID: "inspection-1", TenantID: tenantID}
	if got, err := store.SaveInspection(ctx, inspection); err != nil || got.ID != inspection.ID {
		t.Fatalf("saved inspection = %#v, %v", got, err)
	}
	if _, err := store.GetInspection(ctx, otherTenant, inspection.ID); catalog.ErrorCodeOf(err) != catalog.CodeNotFound {
		t.Fatalf("cross-tenant inspection returned %v", err)
	}
	preview := ChangePreview{ID: "preview-1", TenantID: tenantID, Digest: "digest-1"}
	if _, err := store.SaveChangePreview(ctx, preview); err != nil {
		t.Fatal(err)
	}
	if got, err := store.GetChangePreview(ctx, tenantID, preview.Digest); err != nil || got.ID != preview.ID {
		t.Fatalf("change preview = %#v, %v", got, err)
	}
	if _, err := store.GetChangePreview(ctx, otherTenant, preview.Digest); catalog.ErrorCodeOf(err) != catalog.CodeNotFound {
		t.Fatalf("cross-tenant change preview returned %v", err)
	}

	source := Source{ID: "source-b", TenantID: tenantID, DisplayName: "Beta", Endpoint: "https://beta.example/catalog", Status: SourceActive, Revision: 1}
	if _, err := store.CreateSource(ctx, source); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateSource(ctx, Source{ID: "duplicate", TenantID: tenantID, Endpoint: source.Endpoint, Status: SourceActive}); catalog.ErrorCodeOf(err) != catalog.CodeConflict {
		t.Fatalf("duplicate source endpoint returned %v", err)
	}
	alpha := Source{ID: "source-a", TenantID: tenantID, DisplayName: "Alpha", Endpoint: "https://alpha.example/catalog", Status: SourceActive, Revision: 1}
	alphaTie := Source{ID: "source-aa", TenantID: tenantID, DisplayName: "Alpha", Endpoint: "https://alpha-tie.example/catalog", Status: SourceActive, Revision: 1}
	other := Source{ID: "source-other", TenantID: otherTenant, DisplayName: "Other", Endpoint: source.Endpoint, Status: SourceActive, Revision: 1}
	if _, err := store.CreateSource(ctx, alpha); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateSource(ctx, alphaTie); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateSource(ctx, other); err != nil {
		t.Fatal(err)
	}
	if items, err := store.ListSourcePage(ctx, tenantID, SourceListRequest{Limit: 10}); err != nil || len(items) != 3 || items[0].ID != alpha.ID || items[1].ID != alphaTie.ID {
		t.Fatalf("source list = %#v, %v", items, err)
	}
	if _, err := store.UpdateSource(ctx, Source{ID: "missing", TenantID: tenantID}, 1); catalog.ErrorCodeOf(err) != catalog.CodeNotFound {
		t.Fatalf("missing source update returned %v", err)
	}
	updated := source
	updated.DisplayName = "Updated"
	updated.Revision = 2
	if _, err := store.UpdateSource(ctx, updated, 2); catalog.ErrorCodeOf(err) != catalog.CodePreconditionFailed {
		t.Fatalf("stale source update returned %v", err)
	}
	nonSequential := updated
	nonSequential.Revision = 3
	if _, err := store.UpdateSource(ctx, nonSequential, 1); catalog.ErrorCodeOf(err) != catalog.CodePreconditionFailed {
		t.Fatalf("non-sequential source update returned %v", err)
	}
	if _, err := store.UpdateSource(ctx, updated, 1); err != nil {
		t.Fatal(err)
	}

	oldDigest := "sha256:" + strings.Repeat("1", 64)
	newRaw := json.RawMessage(`{"identifier":"urn:air:remote.example:default:item"}`)
	newDigest := observationDigest(newRaw)
	firstObservation := Observation{IdentityDigest: oldDigest, ID: "observation-old", TenantID: tenantID, SourceID: source.ID, SnapshotID: "snapshot-old", SourceKey: "item", Identifier: "urn:air:remote.example:default:item", ExactEntryDigest: oldDigest, Freshness: FreshCurrent, FirstSeenAt: now, LastSeenAt: now}
	store.observations[observationKey(firstObservation)] = firstObservation
	completedAt := now.Add(time.Minute)
	snapshot := Snapshot{ID: "snapshot-1", TenantID: tenantID, SourceID: source.ID, SourceRevision: updated.Revision, State: SnapshotComplete, EnumerationComplete: true, WithdrawalAuthority: true, MemberCount: 1, StartedAt: now.Add(time.Minute), CompletedAt: &completedAt}
	newObservation := Observation{IdentityDigest: newDigest, ID: "observation-new", TenantID: tenantID, SourceID: source.ID, SnapshotID: snapshot.ID, SourceKey: "item", Identifier: firstObservation.Identifier, RawEntry: newRaw, RawEntryAvailable: true, ExactEntryDigest: newDigest, Freshness: FreshCurrent, FirstSeenAt: completedAt, LastSeenAt: completedAt}
	newObservation.Occurrence = ObservationOccurrence{ObservationID: newObservation.ID, SourceID: source.ID, SnapshotID: snapshot.ID, SourceRevision: updated.Revision, RetrievalDigest: newDigest, EntryLocation: "/entries/0", ExactEntryDigest: newDigest, EvidenceComplete: true, ObservedAt: completedAt}
	updated.LastCompleteSnapshotID = snapshot.ID
	if _, _, err := store.CommitSync(ctx, Snapshot{ID: "missing-snapshot", TenantID: tenantID, SourceID: "missing"}, nil, Source{ID: "missing", TenantID: tenantID, Revision: 1}, 1); catalog.ErrorCodeOf(err) != catalog.CodeNotFound {
		t.Fatalf("missing source commit returned %v", err)
	}
	if _, _, err := store.CommitSync(ctx, snapshot, []Observation{{TenantID: otherTenant}}, updated, updated.Revision); catalog.ErrorCodeOf(err) != catalog.CodeInvalidArgument {
		t.Fatalf("foreign observation commit returned %v", err)
	}
	syncResult := updated
	syncResult.DisplayName = "untrusted synchronization copy"
	committed, committedSource, err := store.CommitSync(ctx, snapshot, []Observation{newObservation}, syncResult, updated.Revision)
	if err != nil || committed.Withdrawn != 0 || committedSource.DisplayName != updated.DisplayName {
		t.Fatalf("committed snapshot/source = %#v / %#v, %v", committed, committedSource, err)
	}
	if _, _, err := store.CommitSync(ctx, snapshot, []Observation{newObservation}, updated, updated.Revision); catalog.ErrorCodeOf(err) != catalog.CodeConflict {
		t.Fatalf("duplicate immutable snapshot returned %v", err)
	}
	store.observations["foreign"] = Observation{IdentityDigest: "sha256:" + strings.Repeat("f", 64), ID: "observation-foreign", TenantID: otherTenant, SourceID: source.ID, SourceKey: "foreign", ExactEntryDigest: "sha256:" + strings.Repeat("f", 64), Freshness: FreshCurrent}
	all, err := store.ListObservationPage(ctx, tenantID, ObservationListRequest{SourceID: source.ID, Limit: 100})
	if err != nil || len(all) != 2 {
		t.Fatalf("observation history = %#v, %v", all, err)
	}
	current, _, err := store.ListCurrentObservations(ctx, tenantID, source.ID, 100)
	if err != nil || len(current) != 1 || current[0].ExactEntryDigest != newDigest {
		t.Fatalf("current observations = %#v, %v", current, err)
	}
	later := completedAt.Add(time.Minute)
	staleCompleted := later.Add(time.Second)
	staleSnapshot := Snapshot{ID: "snapshot-stale", TenantID: tenantID, SourceID: source.ID, SourceRevision: updated.Revision, State: SnapshotIncomplete, MemberCount: 1, StartedAt: now.Add(-time.Minute), CompletedAt: &staleCompleted}
	staleObservation := newObservation
	staleObservation.ID = "observation-stale-replay"
	staleObservation.SnapshotID = staleSnapshot.ID
	staleObservation.Freshness = FreshStale
	staleObservation.LastSeenAt = later
	staleObservation.Occurrence.ObservationID = staleObservation.ID
	staleObservation.Occurrence.SnapshotID = staleSnapshot.ID
	staleObservation.Occurrence.ObservedAt = later
	if _, _, err := store.CommitSync(ctx, staleSnapshot, []Observation{staleObservation}, updated, updated.Revision); err != nil {
		t.Fatal(err)
	}
	current, _, err = store.ListCurrentObservations(ctx, tenantID, source.ID, 100)
	if err != nil || len(current) != 1 || current[0].ID != newObservation.ID || current[0].SnapshotID != snapshot.ID || current[0].Freshness != FreshCurrent || !current[0].LastSeenAt.Equal(later) {
		t.Fatalf("stale replay replaced current observation: %#v, %v", current, err)
	}
	if got, err := store.GetSnapshot(ctx, tenantID, snapshot.ID); err != nil || got.ID != snapshot.ID {
		t.Fatalf("snapshot get = %#v, %v", got, err)
	}
	if _, err := store.GetSnapshot(ctx, otherTenant, snapshot.ID); catalog.ErrorCodeOf(err) != catalog.CodeNotFound {
		t.Fatalf("cross-tenant snapshot returned %v", err)
	}
	older := snapshot
	older.ID = "snapshot-older"
	older.StartedAt = now
	store.snapshots[older.ID] = older
	tied := snapshot
	tied.ID = "snapshot-z"
	store.snapshots[tied.ID] = tied
	if snapshots, err := store.ListSnapshots(ctx, tenantID, source.ID, 1); err != nil || len(snapshots) != 1 || snapshots[0].ID != tied.ID {
		t.Fatalf("bounded snapshots = %#v, %v", snapshots, err)
	}

	removed := updated
	removed.Status = SourceRemoved
	removed.Revision++
	if _, err := store.UpdateSource(ctx, removed, updated.Revision); err != nil {
		t.Fatal(err)
	}
	if _, err := store.GetSource(ctx, tenantID, source.ID); catalog.ErrorCodeOf(err) != catalog.CodeNotFound {
		t.Fatalf("removed source read returned %v", err)
	}
	removedObservations, err := store.ListObservationPage(ctx, tenantID, ObservationListRequest{SourceID: source.ID, Limit: 100})
	if err != nil || len(removedObservations) != 2 {
		t.Fatalf("removed observations = %#v, %v", removedObservations, err)
	}
	for _, observation := range removedObservations {
		if observation.Freshness != FreshSourceRemoved {
			t.Fatalf("removed observation retained freshness %s", observation.Freshness)
		}
	}
	if _, err := store.CreateSource(ctx, Source{ID: "replacement", TenantID: tenantID, Endpoint: source.Endpoint, Status: SourceActive}); err != nil {
		t.Fatalf("removed endpoint could not be reconnected: %v", err)
	}
	if maxTime(now.Add(time.Minute), now) != now.Add(time.Minute) || maxTime(now, now.Add(time.Minute)) != now.Add(time.Minute) {
		t.Fatal("maxTime did not select the later instant")
	}
}

func TestFederationMemoryStoreRequiresLiveSameTenantInspectionEvidence(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store := NewMemoryStore()
	now := time.Date(2026, time.September, 5, 12, 0, 0, 0, time.UTC)
	valid := Inspection{ID: "inspection-valid", TenantID: "tenant-a", ExpiresAt: now.Add(time.Minute)}
	foreign := Inspection{ID: "inspection-foreign", TenantID: "tenant-b", ExpiresAt: now.Add(time.Minute)}
	expired := Inspection{ID: "inspection-expired", TenantID: "tenant-a", ExpiresAt: now}
	for _, inspection := range []Inspection{valid, foreign, expired} {
		if _, err := store.SaveInspection(ctx, inspection); err != nil {
			t.Fatal(err)
		}
	}
	for _, inspectionID := range []string{"missing", foreign.ID, expired.ID} {
		source := Source{ID: "source-" + inspectionID, TenantID: "tenant-a", Endpoint: "https://" + inspectionID + ".example/catalog", Status: SourceActive, Revision: 1, CreatedAt: now, InspectionID: inspectionID}
		if _, err := store.CreateSource(ctx, source); catalog.ErrorCodeOf(err) != catalog.CodePreconditionFailed {
			t.Fatalf("create with inspection %q returned %v", inspectionID, err)
		}
	}

	source := Source{ID: "source-valid", TenantID: "tenant-a", Endpoint: "https://valid.example/catalog", Status: SourceActive, Revision: 1, CreatedAt: now, InspectionID: valid.ID}
	if _, err := store.CreateSource(ctx, source); err != nil {
		t.Fatal(err)
	}
	if persisted, err := store.GetSource(ctx, source.TenantID, source.ID); err != nil || persisted.InspectionID != "" {
		t.Fatalf("persisted source retained transient inspection use = %#v, %v", persisted, err)
	}
	updated := source
	updated.Revision = 2
	updated.UpdatedAt = now.Add(time.Second)
	updated.InspectionID = "missing"
	if _, err := store.UpdateSource(ctx, updated, 1); catalog.ErrorCodeOf(err) != catalog.CodePreconditionFailed {
		t.Fatalf("update with missing inspection returned %v", err)
	}
	updated.InspectionID = valid.ID
	if _, err := store.UpdateSource(ctx, updated, 1); err != nil {
		t.Fatal(err)
	}
	persisted, err := store.GetSource(ctx, source.TenantID, source.ID)
	if err != nil || persisted.InspectionID != "" {
		t.Fatalf("updated source retained transient inspection use = %#v, %v", persisted, err)
	}
	persisted.Revision = 3
	persisted.UpdatedAt = now.Add(2 * time.Minute)
	if _, err := store.UpdateSource(ctx, persisted, 2); err != nil {
		t.Fatalf("later update depended on expired connection inspection: %v", err)
	}
}

func TestMemoryCurrentObservationSetIsBoundedAndTenantScoped(t *testing.T) {
	store := NewMemoryStore()
	store.observations["second"] = Observation{IdentityDigest: "sha256:" + strings.Repeat("b", 64), ID: "observation-b", TenantID: "tenant-a", SourceID: "source-a", ExactEntryDigest: "sha256:" + strings.Repeat("b", 64), Freshness: FreshCurrent}
	store.observations["first"] = Observation{IdentityDigest: "sha256:" + strings.Repeat("a", 64), ID: "observation-a", TenantID: "tenant-a", SourceID: "source-a", ExactEntryDigest: "sha256:" + strings.Repeat("a", 64), Freshness: FreshCurrent}
	store.observations["other"] = Observation{IdentityDigest: "sha256:" + strings.Repeat("c", 64), ID: "observation-other", TenantID: "tenant-b", SourceID: "source-a", ExactEntryDigest: "sha256:" + strings.Repeat("c", 64), Freshness: FreshCurrent}

	exact, overflow, err := store.CurrentObservationSet(context.Background(), "tenant-a", "source-a", 2)
	wantDigest := audit.DigestBytes([]byte("observation-a:sha256:" + strings.Repeat("a", 64) + "\nobservation-b:sha256:" + strings.Repeat("b", 64)))
	if err != nil || overflow || exact.Count != 2 || exact.Digest != wantDigest {
		t.Fatalf("exact observation set = %#v, overflow=%t, %v", exact, overflow, err)
	}
	if bounded, overflow, err := store.CurrentObservationSet(context.Background(), "tenant-a", "source-a", 1); err != nil || !overflow || bounded != (ObservationSet{}) {
		t.Fatalf("overflow observation set = %#v, overflow=%t, %v", bounded, overflow, err)
	}
	other, overflow, err := store.CurrentObservationSet(context.Background(), "tenant-b", "source-a", 2)
	if err != nil || overflow || other.Count != 1 {
		t.Fatalf("other-tenant observation set = %#v, overflow=%t, %v", other, overflow, err)
	}
	if _, _, err := store.CurrentObservationSet(context.Background(), "tenant-a", "source-a", 0); catalog.ErrorCodeOf(err) != catalog.CodeInvalidArgument {
		t.Fatalf("invalid observation-set limit returned %v", err)
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := store.CurrentObservationSet(cancelled, "tenant-a", "source-a", 2); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled observation set returned %v", err)
	}
}

func TestFederationMemoryPagesUseExactlyTheirCursorOrder(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store := NewMemoryStore()
	tenantID := "tenant-one"
	now := time.Date(2026, time.September, 2, 12, 0, 0, 0, time.UTC)
	for _, source := range []Source{
		{ID: "source-z", TenantID: tenantID, DisplayName: "Zulu", Endpoint: "https://zulu.example/catalog", Status: SourceActive},
		{ID: "source-a2", TenantID: tenantID, DisplayName: "Alpha", Endpoint: "https://alpha-two.example/catalog", Status: SourceActive},
		{ID: "source-a1", TenantID: tenantID, DisplayName: "Alpha", Endpoint: "https://alpha-one.example/catalog", Status: SourceActive},
		{ID: "source-other", TenantID: "tenant-two", DisplayName: "Other", Endpoint: "https://other.example/catalog", Status: SourceActive},
	} {
		source.RetrievalMode = RetrievalIndexed
		source.Scope = Scope{Versions: []string{"1"}}
		store.sources[source.ID] = source
	}
	firstSources, err := store.ListSourcePage(ctx, tenantID, SourceListRequest{Limit: 2})
	if err != nil || len(firstSources) != 2 || firstSources[0].ID != "source-a1" || firstSources[1].ID != "source-a2" {
		t.Fatalf("first source page = %#v, %v", firstSources, err)
	}
	secondSources, err := store.ListSourcePage(ctx, tenantID, SourceListRequest{AfterDisplayName: firstSources[1].DisplayName, AfterID: firstSources[1].ID, Limit: 2})
	if err != nil || len(secondSources) != 1 || secondSources[0].ID != "source-z" {
		t.Fatalf("second source page = %#v, %v", secondSources, err)
	}
	if final, err := store.ListSourcePage(ctx, tenantID, SourceListRequest{AfterDisplayName: secondSources[0].DisplayName, AfterID: secondSources[0].ID, Limit: 2}); err != nil || len(final) != 0 {
		t.Fatalf("final source page = %#v, %v", final, err)
	}

	store.snapshots["snapshot-old"] = Snapshot{ID: "snapshot-old", TenantID: tenantID, SourceID: "source-z", StartedAt: now}
	store.snapshots["snapshot-new-a"] = Snapshot{ID: "snapshot-new-a", TenantID: tenantID, SourceID: "source-z", StartedAt: now.Add(time.Minute)}
	store.snapshots["snapshot-new-z"] = Snapshot{ID: "snapshot-new-z", TenantID: tenantID, SourceID: "source-z", StartedAt: now.Add(time.Minute)}
	firstSnapshots, err := store.ListSnapshotPage(ctx, tenantID, SnapshotListRequest{SourceID: "source-z", Limit: 2})
	if err != nil || len(firstSnapshots) != 2 || firstSnapshots[0].ID != "snapshot-new-z" || firstSnapshots[1].ID != "snapshot-new-a" {
		t.Fatalf("first snapshot page = %#v, %v", firstSnapshots, err)
	}
	secondSnapshots, err := store.ListSnapshotPage(ctx, tenantID, SnapshotListRequest{SourceID: "source-z", AfterStartedAt: &firstSnapshots[1].StartedAt, AfterID: firstSnapshots[1].ID, Limit: 2})
	if err != nil || len(secondSnapshots) != 1 || secondSnapshots[0].ID != "snapshot-old" {
		t.Fatalf("second snapshot page = %#v, %v", secondSnapshots, err)
	}

	identifier := "urn:air:remote.example:default:tied"
	observations := []Observation{
		{ID: "observation-empty", TenantID: tenantID, SourceID: "source-z", Freshness: FreshCurrent},
		{ID: "observation-b", TenantID: tenantID, SourceID: "source-z", Identifier: identifier, Version: "1", VersionPresent: true, Kind: catalog.KindAgent, NormalizedEntryDigest: "digest-b", SearchDisplayName: "Tied Agent", SearchTags: []string{"selected"}, Freshness: FreshCurrent},
		{ID: "observation-c", TenantID: tenantID, SourceID: "source-a1", Identifier: identifier, Version: "1", VersionPresent: true, Kind: catalog.KindAgent, NormalizedEntryDigest: "digest-c", SearchDisplayName: "Tied Agent", SearchTags: []string{"selected"}, Freshness: FreshCurrent},
		{ID: "observation-other", TenantID: "tenant-two", SourceID: "source-other", Identifier: identifier, Freshness: FreshCurrent},
	}
	for _, item := range observations {
		store.observations[item.ID] = item
	}
	firstObservations, err := store.ListObservationPage(ctx, tenantID, ObservationListRequest{Limit: 2})
	if err != nil || len(firstObservations) != 2 || firstObservations[0].ID != "observation-empty" || firstObservations[1].ID != "observation-b" {
		t.Fatalf("first observation page = %#v, %v", firstObservations, err)
	}
	position := firstObservations[1]
	secondObservations, err := store.ListObservationPage(ctx, tenantID, ObservationListRequest{AfterIdentifier: position.Identifier, AfterVersionPresent: position.VersionPresent, AfterVersion: position.Version, AfterID: position.ID, Limit: 2})
	if err != nil || len(secondObservations) != 1 || secondObservations[0].ID != "observation-c" {
		t.Fatalf("second observation page = %#v, %v", secondObservations, err)
	}
	search := ObservationSearchRequest{Query: SearchQuery{Text: "tied agent", Kinds: []catalog.Kind{catalog.KindAgent}, Tags: []string{"selected"}}, Limit: 1}
	firstSearch, err := store.SearchObservationPage(ctx, tenantID, search)
	if err != nil || len(firstSearch) != 1 || firstSearch[0].Observation.ID != "observation-b" || firstSearch[0].Score != 80 {
		t.Fatalf("first observation-search page = %#v, %v", firstSearch, err)
	}
	search.AfterScore = firstSearch[0].Score
	search.AfterIdentifier = firstSearch[0].Observation.Identifier
	search.AfterID = firstSearch[0].Observation.ID
	secondSearch, err := store.SearchObservationPage(ctx, tenantID, search)
	if err != nil || len(secondSearch) != 1 || secondSearch[0].Observation.ID != "observation-c" {
		t.Fatalf("second observation-search page = %#v, %v", secondSearch, err)
	}
	search.AfterScore = secondSearch[0].Score
	search.AfterIdentifier = secondSearch[0].Observation.Identifier
	search.AfterID = secondSearch[0].Observation.ID
	if final, err := store.SearchObservationPage(ctx, tenantID, search); err != nil || len(final) != 0 {
		t.Fatalf("final observation-search page = %#v, %v", final, err)
	}
}

func TestFederationMemoryStoreRetainsImmutableRepeatedEvidence(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store := NewMemoryStore()
	now := time.Date(2026, time.August, 31, 10, 0, 0, 0, time.UTC)
	source := Source{ID: "source", TenantID: "tenant", Endpoint: "https://source.example/catalog", Status: SourceActive, Revision: 1}
	if _, err := store.CreateSource(ctx, source); err != nil {
		t.Fatal(err)
	}
	raw := json.RawMessage(`{"identifier":"urn:air:source.example:default:original","displayName":"Current","type":"application/a2a-agent-card+json","data":{"name":"Current"}}`)
	exactDigest := observationDigest(raw)
	normalized := json.RawMessage(`{"profile":"catalog-observation-v1","identifier":"urn:air:source.example:default:original","versionPresent":false,"version":"","kind":"agent","mediaType":"application/a2a-agent-card+json","displayName":"Current","description":"","url":"","data":{"name":"Current"},"tags":null,"representativeQueries":[],"metadata":{"license":"MIT"},"unknownProperties":{"http://purl.org/dc/terms/subject":[" CAFÉ ","TOOLS"]}}`)
	normalizedDigest := observationDigest(normalized)
	sourceKey := sourceScopedObservationKey("urn:air:source.example:default:original", "", false, "", "", "")
	original := Observation{IdentityDigest: exactDigest, RawEntry: raw, RawEntryAvailable: true, NormalizedEntry: normalized, NormalizedEntryDigest: normalizedDigest, NormalizationProfile: observationNormalizationProfile, SearchDisplayName: "Current", SearchPublisher: "source.example", ID: "original-observation", TenantID: source.TenantID, SourceID: source.ID, SnapshotID: "original-snapshot", SourceKey: sourceKey, Identifier: "urn:air:source.example:default:original", Kind: catalog.KindAgent, MediaType: "application/a2a-agent-card+json", Completeness: CompleteEntry, Compatibility: Compatibility{Structural: "valid", ProfileID: "ard-0.91-aa3e598", Support: "supported"}, ExactEntryDigest: exactDigest, Freshness: FreshWithdrawn, FirstSeenAt: now, LastSeenAt: now}
	store.observations[observationKey(original)] = original
	store.occurrences[occurrenceKey(source.TenantID, ObservationOccurrence{SnapshotID: original.SnapshotID, RetrievalDigest: exactDigest, EntryLocation: "/entries/0"})] = ObservationOccurrence{ObservationID: original.ID, SourceID: source.ID, SnapshotID: original.SnapshotID, SourceRevision: 1, EvidenceComplete: true, NormalizedEntryDigest: normalizedDigest, RetrievalDigest: exactDigest, EntryLocation: "/entries/0" + original.ID, ExactEntryDigest: exactDigest, ObservedAt: now}

	observedAt := now.Add(time.Minute)
	completedAt := observedAt
	snapshot := Snapshot{ID: "current-snapshot", TenantID: source.TenantID, SourceID: source.ID, SourceRevision: source.Revision, State: SnapshotComplete, EnumerationComplete: true, WithdrawalAuthority: true, BodyDigest: exactDigest, MemberCount: 1, Added: 1, StartedAt: observedAt.Add(-time.Second), CompletedAt: &completedAt}
	incoming := Observation{IdentityDigest: exactDigest, ID: "new-observation", TenantID: source.TenantID, SourceID: source.ID, SnapshotID: snapshot.ID, SourceKey: sourceKey, Identifier: original.Identifier, Kind: original.Kind, MediaType: original.MediaType, Completeness: CompleteEntry, Compatibility: original.Compatibility, RawEntry: raw, NormalizedEntry: normalized, RawEntryAvailable: true, ExactEntryDigest: exactDigest, NormalizedEntryDigest: normalizedDigest, NormalizationProfile: observationNormalizationProfile, SearchDisplayName: "Current", SearchPublisher: "source.example", Freshness: FreshCurrent, FirstSeenAt: observedAt, LastSeenAt: observedAt}
	incoming.Occurrence = ObservationOccurrence{ObservationID: incoming.ID, SourceID: source.ID, SnapshotID: snapshot.ID, SourceRevision: source.Revision, RetrievalDigest: exactDigest, EntryLocation: "/entries/0", ExactEntryDigest: exactDigest, NormalizedEntryDigest: normalizedDigest, EvidenceComplete: true, ObservedAt: observedAt}
	source.LastCompleteSnapshotID = snapshot.ID
	source.LastSyncAt = &observedAt
	source.UpdatedAt = observedAt
	committed, _, err := store.CommitSync(ctx, snapshot, []Observation{incoming}, source, source.Revision)
	if err != nil || committed.Withdrawn != 0 {
		t.Fatalf("re-observation commit = %#v, %v", committed, err)
	}
	retained, err := store.GetObservation(ctx, source.TenantID, original.ID)
	if err != nil || retained.ID != original.ID || retained.SnapshotID != original.SnapshotID || retained.Freshness != FreshCurrent || !retained.RawEntryAvailable || string(retained.RawEntry) != string(raw) || retained.NormalizedEntryDigest != normalizedDigest || string(retained.NormalizedEntry) != string(normalized) || !retained.FirstSeenAt.Equal(now) || !retained.LastSeenAt.Equal(observedAt) {
		t.Fatalf("retained observation = %#v, %v", retained, err)
	}
	if retained.controlFilters.License == nil || *retained.controlFilters.License != "MIT" || !slices.Equal(retained.controlFilters.Categories, []string{"café", "tools"}) {
		t.Fatal("repeat ingestion failed to derive filters from retained normalized bytes")
	}
	if _, err := store.GetObservation(ctx, source.TenantID, incoming.ID); catalog.ErrorCodeOf(err) != catalog.CodeNotFound {
		t.Fatalf("temporary incoming identity was retained: %v", err)
	}
	occurrences, total, err := store.ListObservationOccurrences(ctx, source.TenantID, original.ID, 10)
	if err != nil || total != 2 || len(occurrences) != 2 || !occurrences[0].EvidenceComplete || occurrences[0].ObservationID != original.ID || !occurrences[1].EvidenceComplete {
		t.Fatalf("retained occurrences = %#v total %d, %v", occurrences, total, err)
	}
}

func TestFederationObservationRetainsUnknownAndSourceScopedEntries(t *testing.T) {
	t.Parallel()

	manager, err := NewManager(NewMemoryStore(), &routingFetcher{results: map[string]fetcher.Result{}}, clock.NewFake(time.Now()), id.Generator{})
	if err != nil {
		t.Fatal(err)
	}
	observation, err := manager.observation("tenant", Source{ID: "source", ProfileID: "custom-profile"}, "snapshot", sourceEntry{Identifier: "external-id", Type: "application/x-custom", Data: json.RawMessage(`{}`)}, 3, time.Now())
	if err != nil || observation.SourceKey == "" || observation.Compatibility.Support != "unknown" || observation.Compatibility.ProfileID != "custom-profile" || len(observation.RawEntry) == 0 {
		t.Fatalf("unknown observation = %#v, %v", observation, err)
	}
	if !hasFinding(observation.Compatibility.Findings, "ARD_IDENTIFIER_COMPATIBILITY") {
		t.Fatalf("identifier compatibility finding missing: %#v", observation.Compatibility.Findings)
	}
	missingIdentifier, err := manager.observation("tenant", Source{ID: "source"}, "snapshot", sourceEntry{DisplayName: "Missing identifier", Type: "application/json", URL: "https://example.com"}, 4, time.Now())
	if err != nil || !strings.HasPrefix(missingIdentifier.SourceKey, "entry:") || !strings.HasSuffix(missingIdentifier.SourceKey, missingIdentifier.ExactEntryDigest) || missingIdentifier.Compatibility.Structural != "invalid" {
		t.Fatalf("source-scoped observation = %#v, %v", missingIdentifier, err)
	}
	emptyVersion, err := manager.observation("tenant", Source{ID: "source", ProfileID: "ard-0.91-aa3e598", Revision: 1}, "snapshot", sourceEntry{Identifier: "urn:air:remote.example:default:item", DisplayName: "Empty version", Type: "application/a2a-agent-card+json", Data: json.RawMessage(`{}`), VersionPresent: true, RetrievalDigest: "sha256:" + strings.Repeat("a", 64), EntryLocation: "/entries/0"}, 5, time.Now())
	if err != nil || emptyVersion.Compatibility.Structural != "invalid" || emptyVersion.NormalizedEntryDigest != "" || !hasFinding(emptyVersion.Compatibility.Findings, "DISCOVERY_ENTRY_VERSION_INVALID") {
		t.Fatalf("empty typed version observation = %#v, %v", emptyVersion, err)
	}
}
