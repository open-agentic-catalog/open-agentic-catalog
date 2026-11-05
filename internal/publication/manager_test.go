package publication

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/adapter"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
)

type publicationRecordSource struct {
	records  []catalog.CatalogRecord
	overflow bool
}

func (source publicationRecordSource) PublicationRecords(context.Context, string, bool, int) ([]catalog.CatalogRecord, bool, error) {
	return source.records, source.overflow, nil
}

type observedPublicationRecordSource struct {
	records []catalog.CatalogRecord
	calls   int
	fail    bool
}

func (source *observedPublicationRecordSource) PublicationRecords(context.Context, string, bool, int) ([]catalog.CatalogRecord, bool, error) {
	source.calls++
	if source.fail {
		return nil, false, errors.New("mutable record source must not be read")
	}
	return source.records, false, nil
}

type publicationClock struct{ now time.Time }

func (clock publicationClock) Now() time.Time { return clock.now }

type mutablePublicationClock struct{ now time.Time }

func (clock *mutablePublicationClock) Now() time.Time { return clock.now }

type publicationIDs struct{}

func (publicationIDs) New(time.Time) (string, error) { return "snapshot-1", nil }

func TestSnapshotValidationCoversBothRepresentationsAndArtifacts(t *testing.T) {
	record := eligiblePublicationRecord()
	key := sha256.Sum256([]byte("publication-manager-test"))
	manager, err := NewManager(Options{
		Records:          publicationRecordSource{records: []catalog.CatalogRecord{record}},
		Store:            NewMemoryStore(),
		Clock:            publicationClock{now: time.Date(2026, time.August, 29, 12, 0, 0, 0, time.UTC)},
		IDs:              publicationIDs{},
		BaseURL:          "https://catalog.example.com",
		PublisherDomain:  "catalog.example.com",
		ARDProfile:       "ard-test",
		AICatalogProfile: "ai-catalog-test",
		CursorKey:        key[:],
	})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := manager.BuildCandidate(publicationContext(t.Context(), "tenant-1"), "tenant-1")
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateSnapshotIntegrity(snapshot); err != nil {
		t.Fatalf("valid retained snapshot failed integrity validation: %v", err)
	}
	for name, mutate := range map[string]func(*Snapshot){
		"missing identity":          func(value *Snapshot) { value.ID = "" },
		"missing tenant":            func(value *Snapshot) { value.TenantID = "" },
		"missing host":              func(value *Snapshot) { value.Host = "" },
		"empty profile":             func(value *Snapshot) { value.ARDProfile = "" },
		"changed host":              func(value *Snapshot) { value.Host = "other.example.com" },
		"candidate activation":      func(value *Snapshot) { activated := value.CreatedAt; value.ActivatedAt = &activated },
		"active without activation": func(value *Snapshot) { value.State = SnapshotActive },
		"activation before creation": func(value *Snapshot) {
			activated := value.CreatedAt.Add(-time.Nanosecond)
			value.State = SnapshotActive
			value.ActivatedAt = &activated
		},
		"unknown state":      func(value *Snapshot) { value.State = SnapshotState("unknown") },
		"ARD digest":         func(value *Snapshot) { value.ETag = `"invalid"` },
		"AI Catalog digest":  func(value *Snapshot) { value.AICatalogETag = `"invalid"` },
		"artifact inventory": func(value *Snapshot) { value.ArtifactIDs = append(value.ArtifactIDs, "unbound-artifact") },
	} {
		t.Run("retained "+name, func(t *testing.T) {
			candidate := cloneSnapshot(snapshot)
			mutate(&candidate)
			if err := ValidateSnapshotIntegrity(candidate); catalog.ErrorCodeOf(err) != catalog.CodePublicationNotReady {
				t.Fatalf("invalid retained snapshot returned %v", err)
			}
		})
	}
	if err := manager.validateSnapshotEligibility(context.Background(), snapshot); err != nil {
		t.Fatalf("valid snapshot failed validation: %v", err)
	}
	for index, entry := range snapshot.ARD.Entries {
		if entry.Context != ARDContext {
			t.Fatalf("ARD entry %d context = %#v, want %q", index, entry.Context, ARDContext)
		}
	}
	aiEntryRaw, err := json.Marshal(snapshot.AICatalog.Entries[0])
	if err != nil {
		t.Fatal(err)
	}
	var aiEntryFields map[string]any
	if err := json.Unmarshal(aiEntryRaw, &aiEntryFields); err != nil {
		t.Fatal(err)
	}
	allowedAIFields := map[string]bool{"identifier": true, "displayName": true, "type": true, "url": true, "data": true, "version": true, "description": true, "tags": true, "updatedAt": true, "extensions": true}
	for field := range aiEntryFields {
		if !allowedAIFields[field] {
			t.Fatalf("AI Catalog entry emitted non-standard field %q: %s", field, aiEntryRaw)
		}
	}
	_, hasURL := aiEntryFields["url"]
	_, hasData := aiEntryFields["data"]
	if aiEntryFields["identifier"] == nil || aiEntryFields["type"] == nil || hasURL == hasData {
		t.Fatalf("AI Catalog entry does not meet its required shape: %s", aiEntryRaw)
	}
	metadata, ok := snapshot.AICatalog.Entries[0].Extensions[aiCatalogMetadataExtension].(map[string]any)
	if !ok || metadata[catalogReleaseIDMetadata] != record.Release.ID || metadata[catalogCreatedAtMetadata] != record.Release.ActivatedAt.UTC().Format(time.RFC3339Nano) {
		t.Fatalf("AI Catalog extension metadata = %#v", snapshot.AICatalog.Entries[0].Extensions)
	}

	tamperedAI := cloneSnapshot(snapshot)
	tamperedAI.AICatalog.Entries[0].DisplayName = "Tampered"
	if code := catalog.ErrorCodeOf(manager.validateSnapshotEligibility(context.Background(), tamperedAI)); code != catalog.CodePublicationNotReady {
		t.Fatalf("tampered AI Catalog entry returned %q", code)
	}

	tamperedArtifacts := cloneSnapshot(snapshot)
	tamperedArtifacts.ArtifactIDs = append(tamperedArtifacts.ArtifactIDs, "unpublished-artifact")
	if code := catalog.ErrorCodeOf(manager.validateSnapshotEligibility(context.Background(), tamperedArtifacts)); code != catalog.CodePublicationNotReady {
		t.Fatalf("tampered artifact allowlist returned %q", code)
	}

}

func TestActiveSnapshotIsSelfContainedAndFailsClosedOnTampering(t *testing.T) {
	newFixture := func(t *testing.T) (*Manager, *MemoryStore, *observedPublicationRecordSource, Snapshot) {
		t.Helper()
		source := &observedPublicationRecordSource{records: []catalog.CatalogRecord{eligiblePublicationRecord()}}
		store := NewMemoryStore()
		key := sha256.Sum256([]byte(t.Name()))
		manager, err := NewManager(Options{Records: source, Store: store, Clock: publicationClock{now: time.Date(2026, time.August, 31, 12, 0, 0, 0, time.UTC)}, IDs: publicationIDs{}, BaseURL: "https://catalog.example.com", PublisherDomain: "catalog.example.com", ARDProfile: "ard-test", AICatalogProfile: "ai-catalog-test", CursorKey: key[:]})
		if err != nil {
			t.Fatal(err)
		}
		candidate, err := manager.BuildCandidate(publicationContext(t.Context(), "tenant-1"), "tenant-1")
		if err != nil {
			t.Fatal(err)
		}
		active, err := manager.Activate(publicationContext(t.Context(), "tenant-1"), "tenant-1", candidate.ID, "Activate reviewed snapshot")
		if err != nil {
			t.Fatal(err)
		}
		return manager, store, source, active
	}

	manager, _, source, active := newFixture(t)
	source.fail = true
	before := source.calls
	if loaded, err := manager.Active(context.Background(), "tenant-1"); err != nil || loaded.ID != active.ID || source.calls != before {
		t.Fatalf("immutable active read = %#v, %v, record calls %d -> %d", loaded, err, before, source.calls)
	}

	for name, mutate := range map[string]func(*Snapshot){
		"ARD bytes":  func(snapshot *Snapshot) { snapshot.ARD.Entries[0].DisplayName = "tampered" },
		"ARD digest": func(snapshot *Snapshot) { snapshot.ARDContentDigest = "sha-256=:invalid:" },
		"AI digest":  func(snapshot *Snapshot) { snapshot.AICatalogETag = `"invalid"` },
		"profile":    func(snapshot *Snapshot) { snapshot.ARDProfile = "another-profile" },
		"host":       func(snapshot *Snapshot) { snapshot.Host = "other.example.com" },
		"tenant":     func(snapshot *Snapshot) { snapshot.TenantID = "tenant-2" },
		"allowlist":  func(snapshot *Snapshot) { snapshot.ArtifactIDs = []string{"unpublished"} },
	} {
		t.Run(name, func(t *testing.T) {
			manager, store, _, active := newFixture(t)
			store.mu.Lock()
			stored := store.snapshots[memorySnapshotKey("tenant-1", active.ID)]
			mutate(&stored)
			store.snapshots[memorySnapshotKey("tenant-1", active.ID)] = stored
			store.mu.Unlock()
			if _, err := manager.Active(context.Background(), "tenant-1"); catalog.ErrorCodeOf(err) != catalog.CodePublicationNotReady {
				t.Fatalf("tampered active snapshot returned %v", err)
			}
			if changed, err := manager.WithdrawDomain(context.Background(), "tenant-1", "unrelated.example.com", WithdrawalCause{Cause: "publisher_domain_revoked", OriginatingAction: "publisher.publication_domain_revoked", OriginatingTargetID: "publisher-one"}); err != nil || !changed {
				t.Fatalf("corrupt active snapshot was not conservatively withdrawn: changed=%t, err=%v", changed, err)
			}
			if _, err := manager.Active(context.Background(), "tenant-1"); catalog.ErrorCodeOf(err) != catalog.CodeNotFound {
				t.Fatalf("withdrawn corrupt snapshot remained active: %v", err)
			}
		})
	}

	// Valid document checksums do not authorize a different configured delivery
	// boundary. These cases must pass intrinsic integrity before Active rejects
	// them, rather than stopping at an unrelated digest mismatch.
	for name, mutate := range map[string]func(*Snapshot){
		"audience":             func(snapshot *Snapshot) { snapshot.Audience = AudienceAuthenticated },
		"AI host display name": func(snapshot *Snapshot) { snapshot.AICatalog.Host.DisplayName = "Another Catalog" },
		"AI host identifier":   func(snapshot *Snapshot) { snapshot.AICatalog.Host.Identifier = "https://other.example.com" },
		"Registry metadata": func(snapshot *Snapshot) {
			for index := range snapshot.ARD.Entries {
				if snapshot.ARD.Entries[index].Metadata[catalogReleaseIDMetadata] == "registry:self" {
					snapshot.ARD.Entries[index].DisplayName = "Another Registry"
				}
			}
		},
	} {
		t.Run("self-consistent "+name, func(t *testing.T) {
			manager, store, source, active := newFixture(t)
			mutate(&active)
			rehashSnapshotDocuments(t, &active)
			if err := ValidateSnapshotIntegrity(active); err != nil {
				t.Fatalf("fixture must pass intrinsic integrity: %v", err)
			}
			store.mu.Lock()
			store.snapshots[memorySnapshotKey(active.TenantID, active.ID)] = active
			store.mu.Unlock()
			source.fail = true
			before := source.calls
			loaded, err := manager.Active(t.Context(), active.TenantID)
			if catalog.ErrorCodeOf(err) != catalog.CodePublicationNotReady || loaded.ID != "" || source.calls != before {
				t.Fatalf("mismatched snapshot exposed or mutable source read: %#v, err=%v calls=%d->%d", loaded, err, before, source.calls)
			}
		})
	}
}

func TestMemoryPublicationStoreKeysSnapshotIDsByTenant(t *testing.T) {
	store := NewMemoryStore()
	for _, tenantID := range []string{"tenant-1", "tenant-2"} {
		if _, err := store.SaveCandidate(context.Background(), Snapshot{ID: "same-id", TenantID: tenantID, Generation: 1}); err != nil {
			t.Fatal(err)
		}
	}
	for _, tenantID := range []string{"tenant-1", "tenant-2"} {
		loaded, err := store.Get(publicationContext(context.Background(), tenantID), tenantID, "same-id")
		if err != nil || loaded.TenantID != tenantID {
			t.Fatalf("tenant %q snapshot = %#v, %v", tenantID, loaded, err)
		}
	}
}

func TestAICatalogCanPublishWithoutARDDiscoveryQueries(t *testing.T) {
	record := eligiblePublicationRecord()
	record.ListingRevision.RepresentativeQueries = nil
	key := sha256.Sum256([]byte("profile-specific-publication-test"))
	manager, err := NewManager(Options{
		Records:          publicationRecordSource{records: []catalog.CatalogRecord{record}},
		Store:            NewMemoryStore(),
		Clock:            publicationClock{now: time.Date(2026, time.August, 29, 12, 0, 0, 0, time.UTC)},
		IDs:              publicationIDs{},
		BaseURL:          "https://catalog.example.com",
		PublisherDomain:  "catalog.example.com",
		ARDProfile:       "ard-test",
		AICatalogProfile: "ai-catalog-test",
		CursorKey:        key[:],
	})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := manager.BuildCandidate(publicationContext(t.Context(), "tenant-1"), "tenant-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.ARD.Entries) != 1 || snapshot.ARD.Entries[0].Type != "application/ai-registry+json" {
		t.Fatalf("ARD membership should contain only the Registry self-entry: %#v", snapshot.ARD.Entries)
	}
	if len(snapshot.AICatalog.Entries) != 1 || snapshot.AICatalog.Entries[0].Identifier != record.Resource.CanonicalIdentifier {
		t.Fatalf("AI Catalog did not retain its eligible non-discoverable entry: %#v", snapshot.AICatalog.Entries)
	}
	if len(snapshot.Exclusions) != 1 || len(snapshot.Exclusions[0].Profiles) != 1 || snapshot.Exclusions[0].Profiles[0] != "ard-test" || snapshot.Exclusions[0].Reasons[0] != "REPRESENTATIVE_QUERIES_REQUIRED" {
		t.Fatalf("profile-specific exclusion = %#v", snapshot.Exclusions)
	}
	if _, err := manager.Activate(publicationContext(t.Context(), "tenant-1"), "tenant-1", snapshot.ID, "Activate reviewed snapshot"); err != nil {
		t.Fatalf("profile-specific snapshot did not activate: %v", err)
	}
}

func TestPublicationWithdrawalMatchesOnlyExactImmutableMembership(t *testing.T) {
	newActiveManager := func(t *testing.T) *Manager {
		t.Helper()
		key := sha256.Sum256([]byte(t.Name()))
		manager, err := NewManager(Options{
			Records:          publicationRecordSource{records: []catalog.CatalogRecord{eligiblePublicationRecord()}},
			Store:            NewMemoryStore(),
			Clock:            publicationClock{now: time.Date(2026, time.August, 31, 12, 0, 0, 0, time.UTC)},
			IDs:              publicationIDs{},
			BaseURL:          "https://catalog.example.com",
			PublisherDomain:  "catalog.example.com",
			ARDProfile:       "ard-test",
			AICatalogProfile: "ai-catalog-test",
			CursorKey:        key[:],
			Host:             "catalog.example.com",
			Audience:         AudienceAnonymous,
		})
		if err != nil {
			t.Fatal(err)
		}
		candidate, err := manager.BuildCandidate(publicationContext(t.Context(), "tenant-1"), "tenant-1")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := manager.Activate(publicationContext(t.Context(), "tenant-1"), "tenant-1", candidate.ID, "Activate reviewed snapshot"); err != nil {
			t.Fatal(err)
		}
		return manager
	}

	manager := newActiveManager(t)
	if changed, err := manager.WithdrawDomain(t.Context(), "tenant-1", "other.example.com", WithdrawalCause{Cause: "publisher_domain_revoked", OriginatingAction: "publisher.publication_domain_revoked", OriginatingTargetID: "publisher-one"}); err != nil || changed {
		t.Fatalf("unrelated domain withdrawal = %t, %v", changed, err)
	}
	if _, err := manager.Active(t.Context(), "tenant-1"); err != nil {
		t.Fatalf("unrelated domain deactivated the snapshot: %v", err)
	}
	if changed, err := manager.WithdrawDomain(t.Context(), "tenant-1", "catalog.example.com", WithdrawalCause{Cause: "publisher_domain_revoked", OriginatingAction: "publisher.publication_domain_revoked", OriginatingTargetID: "publisher-one"}); err != nil || !changed {
		t.Fatalf("matching domain withdrawal = %t, %v", changed, err)
	}
	if _, err := manager.Active(t.Context(), "tenant-1"); catalog.ErrorCodeOf(err) != catalog.CodeNotFound {
		t.Fatalf("domain-withdrawn snapshot remained active: %v", err)
	}

	manager = newActiveManager(t)
	if changed, err := manager.WithdrawRelease(t.Context(), "tenant-1", "other-release", WithdrawalCause{Cause: "release_retired", OriginatingAction: "listing_release.retired", OriginatingTargetID: "other-release"}); err != nil || changed {
		t.Fatalf("unrelated release withdrawal = %t, %v", changed, err)
	}
	if changed, err := manager.WithdrawRelease(t.Context(), "tenant-1", "release-1", WithdrawalCause{Cause: "release_retired", OriginatingAction: "listing_release.retired", OriginatingTargetID: "release-1"}); err != nil || !changed {
		t.Fatalf("matching release withdrawal = %t, %v", changed, err)
	}

	snapshot := Snapshot{
		ARD: ARDManifest{Entries: []Entry{
			{Identifier: "not-an-air-identifier", Metadata: map[string]any{catalogReleaseIDMetadata: "release-invalid"}},
			{Identifier: "urn:air:catalog.example.com:default:agent", Metadata: map[string]any{catalogReleaseIDMetadata: "registry:self"}},
		}},
		AICatalog: AICatalog{Entries: []AICatalogEntry{
			{Identifier: "not-an-air-identifier", Extensions: map[string]any{aiCatalogMetadataExtension: map[string]any{catalogReleaseIDMetadata: "release-invalid"}}},
			{Identifier: "urn:air:other.example.com:default:agent", Extensions: map[string]any{aiCatalogMetadataExtension: map[string]any{catalogReleaseIDMetadata: "release-ai"}}},
		}},
	}
	if snapshotContainsPublisherDomain(snapshot, "catalog.example.com") {
		t.Fatal("invalid or Registry-only ARD membership matched a publisher domain")
	}
	if !snapshotContainsPublisherDomain(snapshot, "other.example.com") {
		t.Fatal("AI Catalog membership did not match its exact publisher domain")
	}
	if !snapshotContainsRelease(snapshot, "release-invalid") || !snapshotContainsRelease(snapshot, "release-ai") || snapshotContainsRelease(snapshot, "missing") {
		t.Fatalf("release membership matching was inconsistent: %#v", snapshot)
	}
}

func TestMemoryStoreReturnsDeepCopies(t *testing.T) {
	store := NewMemoryStore()
	snapshot := Snapshot{
		ID:         "snapshot-1",
		TenantID:   "tenant-1",
		Host:       "catalog.example.com",
		Generation: 1,
		ARD: ARDManifest{Entries: []Entry{{
			Context:               map[string]any{"nested": []any{"original"}},
			Data:                  json.RawMessage(`{"name":"original"}`),
			RepresentativeQueries: []string{"original query"},
			Capabilities:          []string{"original capability"},
			Tags:                  []string{"original tag"},
			Subjects:              []string{"original topic"},
			Metadata:              map[string]any{"nested": map[string]any{"value": "original"}},
		}}},
		AICatalog:   AICatalog{Entries: []AICatalogEntry{{Extensions: map[string]any{"com.example.test": map[string]any{"value": "original"}}}}},
		Exclusions:  []Exclusion{{Reasons: []string{"original reason"}}},
		ArtifactIDs: []string{"artifact-1"},
	}
	if _, err := store.SaveCandidate(context.Background(), snapshot); err != nil {
		t.Fatal(err)
	}
	snapshot.ARD.Entries[0].Subjects[0] = "caller changed"
	loaded, err := store.Get(publicationContext(t.Context(), "tenant-1"), "tenant-1", "snapshot-1")
	if err != nil {
		t.Fatal(err)
	}
	if loaded.ARD.Entries[0].Subjects[0] != "original topic" {
		t.Fatal("input topic mutation leaked into store")
	}
	loaded.ARD.Entries[0].Subjects[0] = "changed"
	loadedContext := requireJSONMap(t, loaded.ARD.Entries[0].Context)
	requireJSONSlice(t, loadedContext["nested"])[0] = "changed"
	loaded.ARD.Entries[0].Data[0] = '['
	loaded.ARD.Entries[0].RepresentativeQueries[0] = "changed"
	loaded.ARD.Entries[0].Capabilities[0] = "changed"
	loaded.ARD.Entries[0].Tags[0] = "changed"
	requireJSONMap(t, loaded.ARD.Entries[0].Metadata["nested"])["value"] = "changed"
	requireJSONMap(t, loaded.AICatalog.Entries[0].Extensions["com.example.test"])["value"] = "changed"
	loaded.Exclusions[0].Reasons[0] = "changed"
	loaded.Exclusions[0].Profiles = []string{"changed"}
	loaded.ArtifactIDs[0] = "changed"

	reloaded, err := store.Get(publicationContext(t.Context(), "tenant-1"), "tenant-1", "snapshot-1")
	if err != nil {
		t.Fatal(err)
	}
	reloadedContext := requireJSONMap(t, reloaded.ARD.Entries[0].Context)
	if got := requireJSONSlice(t, reloadedContext["nested"])[0]; got != "original" {
		t.Fatalf("context mutation leaked into store: %v", got)
	}
	if string(reloaded.ARD.Entries[0].Data) != `{"name":"original"}` || reloaded.ARD.Entries[0].RepresentativeQueries[0] != "original query" || reloaded.ARD.Entries[0].Capabilities[0] != "original capability" || reloaded.ARD.Entries[0].Tags[0] != "original tag" || reloaded.ARD.Entries[0].Subjects[0] != "original topic" {
		t.Fatalf("entry mutation leaked into store: %#v", reloaded.ARD.Entries[0])
	}
	if got := requireJSONMap(t, reloaded.ARD.Entries[0].Metadata["nested"])["value"]; got != "original" {
		t.Fatalf("metadata mutation leaked into store: %v", got)
	}
	if requireJSONMap(t, reloaded.AICatalog.Entries[0].Extensions["com.example.test"])["value"] != "original" || reloaded.Exclusions[0].Reasons[0] != "original reason" || len(reloaded.Exclusions[0].Profiles) != 0 || reloaded.ArtifactIDs[0] != "artifact-1" {
		t.Fatalf("snapshot mutation leaked into store: %#v", reloaded)
	}
}

func requireJSONMap(t *testing.T, value any) map[string]any {
	t.Helper()
	result, ok := value.(map[string]any)
	if !ok {
		t.Fatalf("JSON value has type %T, want object", value)
	}
	return result
}

func requireJSONSlice(t *testing.T, value any) []any {
	t.Helper()
	result, ok := value.([]any)
	if !ok {
		t.Fatalf("JSON value has type %T, want array", value)
	}
	return result
}

func TestSearchResponseKeepsFederationDiagnosticsOffTheStandardWireShape(t *testing.T) {
	raw, err := json.Marshal(SearchResponse{
		Results:     []SearchResult{},
		Partial:     true,
		Diagnostics: []SourceDiagnostic{{Source: "source-1", Status: "failed", Message: "private diagnostic"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	if _, ok := fields["partial"]; ok {
		t.Fatalf("non-standard partial field leaked into ARD Search response: %s", raw)
	}
	if _, ok := fields["diagnostics"]; ok {
		t.Fatalf("internal federation diagnostics leaked into ARD Search response: %s", raw)
	}
}

func TestStructuredFiltersAreValidatedAndSupportCoreFacets(t *testing.T) {
	entry := Entry{
		Identifier:            "urn:air:catalog.example.com:default:agent",
		DisplayName:           "Agent",
		Type:                  adapter.MediaA2AAgentCard,
		RepresentativeQueries: []string{"Find an agent"},
		Capabilities:          []string{"case-management"},
		Description:           "Handles cases",
		Tags:                  []string{"support"},
		Version:               "1.0.0",
		UpdatedAt:             "2026-08-29T12:00:00Z",
		Metadata:              map[string]any{"region": "eu"},
	}
	filter := map[string]any{"publisher": "catalog.example.com", "capabilities": []any{"case-management"}, "metadata.region": "eu"}
	query := SearchQuery{Text: "agent", Filter: filter}
	if err := normalizeStructuredQuery(&query); err != nil {
		t.Fatalf("supported filter rejected: %v", err)
	}
	if !matchFilter(entry, query.Filter, nil) {
		t.Fatal("supported core filter did not match")
	}
	aliasQuery := SearchQuery{Text: "agent", Context: map[string]any{"kind": "https://agenticresourcediscovery.org/ns#mediaType"}, Filter: map[string]any{"kind": adapter.MediaA2AAgentCard}}
	if err := normalizeStructuredQuery(&aliasQuery); err != nil || aliasQuery.Filter["type"] != adapter.MediaA2AAgentCard {
		t.Fatalf("JSON-LD filter alias was not resolved: %#v, %v", aliasQuery, err)
	}
	invalidQueries := []SearchQuery{
		{Text: "agent", Context: "https://example.com/context", Filter: map[string]any{"type": adapter.MediaA2AAgentCard}},
		{Text: "agent", Filter: map[string]any{"unsupported": "value"}},
		{Text: "agent", Filter: map[string]any{"type": []any{adapter.MediaA2AAgentCard, 1}}},
		{Text: "agent", Filter: map[string]any{"metadata.deep.path": "value"}},
	}
	for _, query := range invalidQueries {
		if err := normalizeStructuredQuery(&query); catalog.ErrorCodeOf(err) != catalog.CodeInvalidArgument {
			t.Fatalf("invalid structured query %#v returned %v", query, err)
		}
	}
}

func TestListFilterGrammarAndMatching(t *testing.T) {
	filter := "displayName = 'Research and Development' AND type = 'application/a2a-agent-card+json,application/mcp-server-card+json' AND publisherId = 'catalog.example.com' AND createdAfter > '2026-08-01' AND updatedAfter > '2026-08-28T00:00:00Z'"
	clauses, err := parseListFilter(filter)
	if err != nil || len(clauses) != 5 {
		t.Fatalf("valid List filter = %#v, %v", clauses, err)
	}
	entry := Entry{Identifier: "urn:air:catalog.example.com:default:agent", DisplayName: "Research and Development", Type: adapter.MediaA2AAgentCard, UpdatedAt: "2026-08-29T12:00:00Z", Metadata: map[string]any{catalogCreatedAtMetadata: "2026-08-15T12:00:00Z"}}
	if !matchListFilter(entry, Snapshot{CreatedAt: time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)}, clauses) {
		t.Fatal("valid List filter did not match")
	}
	createdAfterUpdate := listFilterClause{field: "createdAfter", after: time.Date(2026, 8, 20, 0, 0, 0, 0, time.UTC)}
	if matchListFilter(entry, Snapshot{CreatedAt: time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)}, []listFilterClause{createdAfterUpdate}) {
		t.Fatal("createdAfter incorrectly used the entry update time")
	}
	invalidFilters := []string{
		"identifier = 'urn:air:catalog.example.com:default:agent'",
		"type > 'application/a2a-agent-card+json'",
		"createdAfter = '2026-08-01'",
		"type = 'one' AND type = 'two'",
		"updatedAfter > 'not-a-date'",
		"displayName = 'unterminated",
	}
	for _, invalid := range invalidFilters {
		if _, err := parseListFilter(invalid); catalog.ErrorCodeOf(err) != catalog.CodeInvalidArgument {
			t.Fatalf("invalid List filter %q returned %v", invalid, err)
		}
	}
}

func TestPaginationCursorIsBoundAndExpires(t *testing.T) {
	now := time.Date(2026, time.August, 29, 12, 0, 0, 0, time.UTC)
	clock := &mutablePublicationClock{now: now}
	records := make([]catalog.CatalogRecord, 0, 3)
	for index, suffix := range []string{"alpha", "beta", "gamma"} {
		record := eligiblePublicationRecord()
		record.Resource.ID = "resource-" + suffix
		record.Resource.CanonicalIdentifier = "urn:air:catalog.example.com:default:" + suffix
		record.Version.ID = "version-" + suffix
		record.ListingRevision.DisplayName = "Published agent " + suffix
		record.Release.ID = "release-" + suffix
		record.Release.UpdatedAt = now.Add(time.Duration(index) * time.Minute)
		records = append(records, record)
	}
	key := sha256.Sum256([]byte("pagination-binding-test"))
	store := NewMemoryStore()
	manager, err := NewManager(Options{Records: publicationRecordSource{records: records}, Store: store, Clock: clock, IDs: publicationIDs{}, BaseURL: "https://catalog.example.com", PublisherDomain: "catalog.example.com", ARDProfile: "ard-test", AICatalogProfile: "ai-catalog-test", CursorKey: key[:]})
	if err != nil {
		t.Fatal(err)
	}
	for _, tenantID := range []string{"tenant-1", "tenant-2"} {
		snapshot, buildErr := manager.BuildCandidate(publicationContext(context.Background(), tenantID), tenantID)
		if buildErr != nil {
			t.Fatal(buildErr)
		}
		if _, activateErr := manager.Activate(publicationContext(context.Background(), tenantID), tenantID, snapshot.ID, "Activate reviewed snapshot"); activateErr != nil {
			t.Fatal(activateErr)
		}
	}
	request := SearchRequest{Query: SearchQuery{Text: "Published agent"}, Federation: "none", PageSize: 1}
	first, err := manager.Search(context.Background(), "tenant-1", request)
	if err != nil || len(first.Results) != 1 || first.PageToken == "" {
		t.Fatalf("first page = %#v, %v", first, err)
	}
	request.PageToken = first.PageToken
	second, err := manager.Search(context.Background(), "tenant-1", request)
	if err != nil || len(second.Results) != 1 || second.Results[0].Identifier == first.Results[0].Identifier {
		t.Fatalf("second page = %#v, %v", second, err)
	}
	changedQuery := request
	changedQuery.Query.Text = "agent"
	if _, err := manager.Search(context.Background(), "tenant-1", changedQuery); catalog.ErrorCodeOf(err) != catalog.CodeInvalidArgument {
		t.Fatalf("cursor reused with another query: %v", err)
	}
	changedPageSize := request
	changedPageSize.PageSize = 2
	if _, err := manager.Search(context.Background(), "tenant-1", changedPageSize); catalog.ErrorCodeOf(err) != catalog.CodeInvalidArgument {
		t.Fatalf("cursor reused with another page size: %v", err)
	}
	if _, err := manager.Search(context.Background(), "tenant-2", request); catalog.ErrorCodeOf(err) != catalog.CodeInvalidArgument {
		t.Fatalf("cursor reused across tenants: %v", err)
	}
	if _, err := manager.List(context.Background(), "tenant-1", ListRequest{PageSize: 1, PageToken: first.PageToken}); catalog.ErrorCodeOf(err) != catalog.CodeInvalidArgument {
		t.Fatalf("search cursor reused for list: %v", err)
	}
	clock.now = now.Add(cursorTTL + time.Second)
	if _, err := manager.Search(context.Background(), "tenant-1", request); catalog.ErrorCodeOf(err) != catalog.CodeInvalidArgument {
		t.Fatalf("expired cursor accepted: %v", err)
	}
}

func eligiblePublicationRecord() catalog.CatalogRecord {
	now := time.Date(2026, time.August, 29, 12, 0, 0, 0, time.UTC)
	return catalog.CatalogRecord{
		Resource: catalog.Resource{ID: "resource-1", TenantID: "tenant-1", CanonicalIdentifier: "urn:air:catalog.example.com:default:agent", Kind: catalog.KindAgent},
		Version:  catalog.ResourceVersion{ID: "version-1", Version: "1.0.0"},
		VersionRevision: catalog.VersionRevision{Representations: []catalog.Representation{{
			Role:            catalog.RepresentationPrimary,
			MediaType:       adapter.MediaA2AAgentCard,
			Data:            json.RawMessage(`{"name":"Published agent"}`),
			ValidationState: "valid",
		}}},
		ListingRevision: catalog.ListingRevision{
			DisplayName:           "Published agent",
			Description:           "A governed published agent.",
			RepresentativeQueries: []string{"Find an agent", "Show an agent"},
			Visibility:            catalog.VisibilityPublic,
		},
		Release:  catalog.ListingRelease{ID: "release-1", ActivatedAt: now.Add(-24 * time.Hour), UpdatedAt: now},
		Evidence: catalog.EvidenceSet{State: catalog.EvidenceComplete, LegalRights: catalog.LegalRightsSummary{State: catalog.LegalRightsNotAssessed}},
	}
}
