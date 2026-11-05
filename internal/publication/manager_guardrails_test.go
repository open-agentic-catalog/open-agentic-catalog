package publication

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/adapter"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
)

type sequencePublicationIDs struct{ next int }

func (ids *sequencePublicationIDs) New(time.Time) (string, error) {
	ids.next++
	return fmt.Sprintf("snapshot-%d", ids.next), nil
}

type failingPublicationIDs struct{ err error }

func (ids failingPublicationIDs) New(time.Time) (string, error) { return "", ids.err }

type failingPublicationRecords struct{ err error }

func (records failingPublicationRecords) PublicationRecords(context.Context, string, bool, int) ([]catalog.CatalogRecord, bool, error) {
	return nil, false, records.err
}

type failingPublicationStore struct {
	Store
	operation string
	err       error
}

func TestMemorySnapshotSummariesUseBoundedCursorOrder(t *testing.T) {
	t.Parallel()

	store := NewMemoryStore()
	now := time.Date(2026, time.September, 2, 12, 0, 0, 0, time.UTC)
	for _, snapshot := range []Snapshot{
		{ID: "snapshot-one", TenantID: "tenant-one", Host: "catalog.example.com", Generation: 1, State: SnapshotRetired, CreatedAt: now},
		{ID: "snapshot-two-a", TenantID: "tenant-one", Host: "catalog.example.com", Generation: 2, State: SnapshotCandidate, CreatedAt: now},
		{ID: "snapshot-two-z", TenantID: "tenant-one", Host: "catalog.example.com", Generation: 2, State: SnapshotActive, CreatedAt: now},
		{ID: "snapshot-other", TenantID: "tenant-two", Host: "catalog.example.com", Generation: 9, State: SnapshotActive, CreatedAt: now},
	} {
		store.snapshots[memorySnapshotKey(snapshot.TenantID, snapshot.ID)] = snapshot
	}
	first, err := store.ListSummaries(context.Background(), "tenant-one", SnapshotListRequest{Limit: 2})
	if err != nil || len(first) != 2 || first[0].ID != "snapshot-two-z" || first[1].ID != "snapshot-two-a" {
		t.Fatalf("first publication-snapshot page = %#v, %v", first, err)
	}
	second, err := store.ListSummaries(context.Background(), "tenant-one", SnapshotListRequest{AfterGeneration: first[1].Generation, AfterID: first[1].ID, Limit: 2})
	if err != nil || len(second) != 1 || second[0].ID != "snapshot-one" {
		t.Fatalf("second publication-snapshot page = %#v, %v", second, err)
	}
	if final, err := store.ListSummaries(context.Background(), "tenant-one", SnapshotListRequest{AfterGeneration: second[0].Generation, AfterID: second[0].ID, Limit: 2}); err != nil || len(final) != 0 {
		t.Fatalf("final publication-snapshot page = %#v, %v", final, err)
	}
	if other, err := store.ListSummaries(context.Background(), "tenant-two", SnapshotListRequest{Limit: 2}); err != nil || len(other) != 1 || other[0].ID != "snapshot-other" {
		t.Fatalf("other-tenant publication-snapshot page = %#v, %v", other, err)
	}
	store.snapshots[memorySnapshotKey("tenant-one", "new-candidate")] = Snapshot{ID: "new-candidate", TenantID: "tenant-one", Generation: 3, State: SnapshotCandidate}
	for state, want := range map[SnapshotState]string{SnapshotActive: "snapshot-two-z", SnapshotCandidate: "new-candidate", SnapshotRetired: "snapshot-one"} {
		got, err := store.ListSummaries(context.Background(), "tenant-one", SnapshotListRequest{State: state, Limit: 1})
		if err != nil || len(got) != 1 || got[0].ID != want {
			t.Fatalf("state %s selection before limit: %+v, %v", state, got, err)
		}
	}
	got, err := store.ListSummaries(context.Background(), "tenant-one", SnapshotListRequest{State: SnapshotCandidate, AfterGeneration: 3, AfterID: "new-candidate", Limit: 1})
	if err != nil || len(got) != 1 || got[0].ID != "snapshot-two-a" {
		t.Fatalf("filtered continuation: %+v, %v", got, err)
	}
}

func (store failingPublicationStore) Get(ctx context.Context, tenantID, snapshotID string) (Snapshot, error) {
	if store.operation == "get" {
		return Snapshot{}, store.err
	}
	return store.Store.Get(ctx, tenantID, snapshotID)
}

func (store failingPublicationStore) Active(ctx context.Context, tenantID string) (Snapshot, error) {
	if store.operation == "active" {
		return Snapshot{}, store.err
	}
	return store.Store.Active(ctx, tenantID)
}

func (store failingPublicationStore) NextGeneration(ctx context.Context, tenantID, host string) (int64, error) {
	if store.operation == "generation" {
		return 0, store.err
	}
	return store.Store.NextGeneration(ctx, tenantID, host)
}

func (store failingPublicationStore) Deactivate(ctx context.Context, tenantID, snapshotID string, at time.Time, evidence ...MutationEvidence) (Snapshot, error) {
	if store.operation == "deactivate" {
		return Snapshot{}, store.err
	}
	return store.Store.Deactivate(ctx, tenantID, snapshotID, at, evidence...)
}

func newPublicationManager(t *testing.T, records []catalog.CatalogRecord) (*Manager, *MemoryStore, *mutablePublicationClock) {
	t.Helper()
	key := sha256.Sum256([]byte(t.Name()))
	store := NewMemoryStore()
	clock := &mutablePublicationClock{now: time.Date(2026, time.August, 30, 12, 0, 0, 0, time.UTC)}
	manager, err := NewManager(Options{
		Records:          publicationRecordSource{records: records},
		Store:            store,
		Clock:            clock,
		IDs:              &sequencePublicationIDs{},
		BaseURL:          "https://catalog.example.com/",
		PublisherDomain:  "catalog.example.com",
		ARDProfile:       "ard-test",
		AICatalogProfile: "ai-catalog-test",
		CursorKey:        key[:],
	})
	if err != nil {
		t.Fatal(err)
	}
	return manager, store, clock
}

func activatePublicationSnapshot(t *testing.T, manager *Manager) Snapshot {
	t.Helper()
	snapshot, err := manager.BuildCandidate(publicationContext(t.Context(), "tenant-1"), "tenant-1")
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err = manager.Activate(publicationContext(t.Context(), "tenant-1"), "tenant-1", snapshot.ID, "Activate reviewed snapshot")
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func TestPublicationDependencyFailuresDoNotBecomeSuccessfulStateChanges(t *testing.T) {
	t.Parallel()

	sentinel := errors.New("injected publication dependency failure")
	manager, store, _ := newPublicationManager(t, []catalog.CatalogRecord{eligiblePublicationRecord()})
	ctx := publicationContext(context.Background(), "tenant-1")

	originalRecords := manager.records
	manager.records = failingPublicationRecords{err: sentinel}
	if _, err := manager.BuildCandidate(ctx, "tenant-1"); !errors.Is(err, sentinel) {
		t.Fatalf("record-source failure returned %v", err)
	}
	manager.records = originalRecords

	originalStore := manager.store
	manager.store = failingPublicationStore{Store: originalStore, operation: "generation", err: sentinel}
	if _, err := manager.BuildCandidate(ctx, "tenant-1"); !errors.Is(err, sentinel) {
		t.Fatalf("snapshot-list failure returned %v", err)
	}
	manager.store = originalStore

	originalIDs := manager.ids
	manager.ids = failingPublicationIDs{err: sentinel}
	if _, err := manager.BuildCandidate(ctx, "tenant-1"); !errors.Is(err, sentinel) || catalog.ErrorCodeOf(err) != catalog.CodeInternal {
		t.Fatalf("snapshot identifier failure returned %v", err)
	}
	manager.ids = originalIDs

	manager.store = failingPublicationStore{Store: originalStore, operation: "get", err: sentinel}
	if _, err := manager.Activate(ctx, "tenant-1", "missing", "Activate reviewed snapshot"); !errors.Is(err, sentinel) {
		t.Fatalf("activation read failure returned %v", err)
	}
	if _, err := manager.Rollback(ctx, "tenant-1", "missing", "Restore reviewed snapshot"); !errors.Is(err, sentinel) {
		t.Fatalf("rollback read failure returned %v", err)
	}
	manager.store = originalStore

	candidate, err := manager.BuildCandidate(ctx, "tenant-1")
	if err != nil {
		t.Fatal(err)
	}
	manager.records = failingPublicationRecords{err: sentinel}
	if _, err := manager.Activate(ctx, "tenant-1", candidate.ID, "Activate reviewed snapshot"); !errors.Is(err, sentinel) {
		t.Fatalf("activation revalidation failure returned %v", err)
	}
	manager.records = originalRecords
	if _, err := manager.Activate(ctx, "tenant-1", candidate.ID, "Activate reviewed snapshot"); err != nil {
		t.Fatal(err)
	}

	manager.store = failingPublicationStore{Store: originalStore, operation: "active", err: sentinel}
	if _, err := manager.WithdrawDomain(ctx, "tenant-1", "catalog.example.com", WithdrawalCause{Cause: "publisher_domain_revoked", OriginatingAction: "publisher.publication_domain_revoked", OriginatingTargetID: "publisher-one"}); !errors.Is(err, sentinel) {
		t.Fatalf("withdrawal read failure returned %v", err)
	}
	manager.store = failingPublicationStore{Store: originalStore, operation: "deactivate", err: sentinel}
	if withdrawn, err := manager.WithdrawDomain(ctx, "tenant-1", "catalog.example.com", WithdrawalCause{Cause: "publisher_domain_revoked", OriginatingAction: "publisher.publication_domain_revoked", OriginatingTargetID: "publisher-one"}); withdrawn || !errors.Is(err, sentinel) {
		t.Fatalf("withdrawal state failure = %t, %v", withdrawn, err)
	}
	manager.store = originalStore

	active, err := store.Active(ctx, "tenant-1")
	if err != nil || active.ID != candidate.ID {
		t.Fatalf("dependency failures changed active snapshot = %#v, %v", active, err)
	}
}

// Observe attempted transitions as well as stored state: a store rejection
// must not mask a preparation guard that failed to stop the mutation handoff.
type publicationMutationSpy struct {
	Store
	calls int
}

func (s *publicationMutationSpy) Activate(ctx context.Context, tenantID, snapshotID string, at time.Time, evidence ...MutationEvidence) (Snapshot, error) {
	s.calls++
	return s.Store.Activate(ctx, tenantID, snapshotID, at, evidence...)
}

func (s *publicationMutationSpy) Rollback(ctx context.Context, tenantID, snapshotID string, at time.Time, evidence ...MutationEvidence) (Snapshot, error) {
	s.calls++
	return s.Store.Rollback(ctx, tenantID, snapshotID, at, evidence...)
}

func (s *publicationMutationSpy) Deactivate(ctx context.Context, tenantID, snapshotID string, at time.Time, evidence ...MutationEvidence) (Snapshot, error) {
	s.calls++
	return s.Store.Deactivate(ctx, tenantID, snapshotID, at, evidence...)
}

// These failures occur before the store transition. Store-level evidence
// rollback is covered separately by the memory-store tests.
func TestPublicationTransitionsPreserveSnapshotsWhenPreparationFails(t *testing.T) {
	t.Parallel()
	for _, operation := range []string{"activate", "rollback", "unpublish"} {
		for _, failure := range []string{"reason", "cancelled", "active read", "audit attribution", "eligibility"} {
			if operation == "unpublish" && failure == "eligibility" {
				continue // Unpublishing must not depend on current record eligibility.
			}
			t.Run(operation+"/"+failure, func(t *testing.T) {
				manager, store, _ := newPublicationManager(t, []catalog.CatalogRecord{eligiblePublicationRecord()})
				first := activatePublicationSnapshot(t, manager)
				active := activatePublicationSnapshot(t, manager)
				candidate, err := manager.BuildCandidate(publicationContext(t.Context(), "tenant-1"), "tenant-1")
				if err != nil {
					t.Fatal(err)
				}
				retired, err := store.Get(publicationContext(t.Context(), "tenant-1"), "tenant-1", first.ID)
				if err != nil {
					t.Fatal(err)
				}
				mutations := &publicationMutationSpy{Store: store}
				manager.store = mutations
				ctx := publicationContext(t.Context(), "tenant-1")
				reason := "Reviewed transition"
				sentinel := errors.New("publication dependency failed")
				var wantError error
				var wantCode catalog.ErrorCode
				sink := &recordingPublicationEvidence{}
				switch failure {
				case "reason":
					reason = "\x00invalid"
					wantCode = catalog.CodeInvalidArgument
				case "cancelled":
					// Occupy the slot so cancellation tests the waiting path,
					// not select's choice between two simultaneously ready cases.
					release, acquireErr := manager.constructionLimiter.acquire(ctx)
					if acquireErr != nil {
						t.Fatal(acquireErr)
					}
					t.Cleanup(release)
					var cancel context.CancelFunc
					ctx, cancel = context.WithCancel(ctx)
					cancel()
					wantError = context.Canceled
				case "active read":
					manager.store = failingPublicationStore{Store: mutations, operation: "active", err: sentinel}
					wantError = sentinel
				case "audit attribution":
					ctx = t.Context()
					manager.evidence = sink
					wantCode = catalog.CodeForbidden
				case "eligibility":
					manager.records = failingPublicationRecords{err: sentinel}
					wantError = sentinel
				}
				var result Snapshot
				switch operation {
				case "activate":
					result, err = manager.Activate(ctx, "tenant-1", candidate.ID, reason)
				case "rollback":
					result, err = manager.Rollback(ctx, "tenant-1", retired.ID, reason)
				case "unpublish":
					result, err = manager.Unpublish(ctx, "tenant-1", reason)
				}
				if err == nil || result.ID != "" || wantError != nil && !errors.Is(err, wantError) || wantCode != "" && catalog.ErrorCodeOf(err) != wantCode {
					t.Fatalf("result=%+v error=%v wantError=%v wantCode=%s", result, err, wantError, wantCode)
				}
				if mutations.calls != 0 {
					t.Fatal("preparation failure reached a store mutation")
				}
				for _, before := range []Snapshot{retired, active, candidate} {
					after, readErr := store.Get(publicationContext(t.Context(), "tenant-1"), "tenant-1", before.ID)
					if readErr != nil || !reflect.DeepEqual(before, after) {
						t.Fatalf("failed transition modified %q: %v", before.ID, readErr)
					}
				}
				current, readErr := store.Active(t.Context(), "tenant-1")
				if readErr != nil || current.ID != active.ID {
					t.Fatalf("failed transition changed active pointer: %+v %v", current, readErr)
				}
				if failure == "audit attribution" && (len(sink.audit) != 0 || len(sink.outbox) != 0) {
					t.Fatal("failed attribution emitted evidence")
				}
			})
		}
	}
}

func TestPublicationConstructionRejectsOverflowWithoutRetainingPartialState(t *testing.T) {
	key := sha256.Sum256([]byte(t.Name()))
	store := NewMemoryStore()
	manager, err := NewManager(Options{Records: publicationRecordSource{overflow: true}, Store: store, Clock: &mutablePublicationClock{now: time.Date(2026, time.September, 4, 12, 0, 0, 0, time.UTC)}, IDs: &sequencePublicationIDs{}, BaseURL: "https://catalog.example.com", PublisherDomain: "catalog.example.com", ARDProfile: "ard-test", AICatalogProfile: "ai-test", CursorKey: key[:]})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.BuildCandidate(publicationContext(t.Context(), "tenant-1"), "tenant-1"); catalog.ErrorCodeOf(err) != catalog.CodeWorkLimitExceeded {
		t.Fatalf("overflowing construction returned %v", err)
	}
	if len(store.snapshots) != 0 || len(store.active) != 0 || len(store.generation) != 0 {
		t.Fatalf("overflow retained partial publication state: snapshots=%d active=%d generations=%d", len(store.snapshots), len(store.active), len(store.generation))
	}

	oversized := eligiblePublicationRecord()
	oversized.ListingRevision.Description = strings.Repeat("x", MaxPublicationDocumentBytes)
	manager.records = publicationRecordSource{records: []catalog.CatalogRecord{oversized}}
	if _, err := manager.BuildCandidate(publicationContext(t.Context(), "tenant-1"), "tenant-1"); catalog.ErrorCodeOf(err) != catalog.CodeWorkLimitExceeded {
		t.Fatalf("encoded-byte overflow returned %v", err)
	}
	if len(store.snapshots) != 0 || len(store.generation) != 0 {
		t.Fatalf("encoded-byte overflow retained partial publication state: snapshots=%d generations=%d", len(store.snapshots), len(store.generation))
	}
}

func TestPublicationActivationReappliesAggregateWorkBound(t *testing.T) {
	record := eligiblePublicationRecord()
	manager, store, _ := newPublicationManager(t, []catalog.CatalogRecord{record})
	candidate, err := manager.BuildCandidate(publicationContext(t.Context(), "tenant-1"), "tenant-1")
	if err != nil {
		t.Fatal(err)
	}
	overflow := record
	overflow.ListingRevision.Tags = make([]string, MaxPublicationWorkUnits+1)
	manager.records = publicationRecordSource{records: []catalog.CatalogRecord{overflow}}
	if _, err := manager.Activate(publicationContext(t.Context(), "tenant-1"), "tenant-1", candidate.ID, "Activate reviewed snapshot"); catalog.ErrorCodeOf(err) != catalog.CodeWorkLimitExceeded {
		t.Fatalf("aggregate-work activation overflow returned %v", err)
	}
	if _, err := store.Active(context.Background(), "tenant-1"); catalog.ErrorCodeOf(err) != catalog.CodeNotFound {
		t.Fatalf("failed activation changed active publication: %v", err)
	}
}

func TestPublicationManagerConstructionAndEligibilityBoundaries(t *testing.T) {
	record := eligiblePublicationRecord()
	manager, store, clock := newPublicationManager(t, []catalog.CatalogRecord{record})
	if manager.baseURL != "https://catalog.example.com" || manager.displayName != "Open Agentic Catalog" {
		t.Fatalf("manager defaults = %#v", manager)
	}
	key := sha256.Sum256([]byte(t.Name()))
	base := Options{Records: publicationRecordSource{}, Store: store, Clock: clock, IDs: &sequencePublicationIDs{}, BaseURL: "https://catalog.example.com", PublisherDomain: "catalog.example.com", ARDProfile: "ard", AICatalogProfile: "ai", CursorKey: key[:]}
	for name, remove := range map[string]func(*Options){
		"records":            func(options *Options) { options.Records = nil },
		"store":              func(options *Options) { options.Store = nil },
		"clock":              func(options *Options) { options.Clock = nil },
		"ids":                func(options *Options) { options.IDs = nil },
		"base URL":           func(options *Options) { options.BaseURL = "" },
		"publisher domain":   func(options *Options) { options.PublisherDomain = "" },
		"ARD profile":        func(options *Options) { options.ARDProfile = "" },
		"AI Catalog profile": func(options *Options) { options.AICatalogProfile = "" },
		"cursor key":         func(options *Options) { options.CursorKey = make([]byte, 31) },
	} {
		t.Run("missing "+name, func(t *testing.T) {
			options := base
			remove(&options)
			_, err := NewManager(options)
			if catalog.ErrorCodeOf(err) != catalog.CodeInvalidArgument {
				t.Fatalf("NewManager returned %v", err)
			}
		})
	}

	tests := []struct {
		name       string
		mutate     func(*catalog.CatalogRecord)
		wantReason string
	}{
		{"private listing", func(record *catalog.CatalogRecord) { record.ListingRevision.Visibility = catalog.VisibilityPrivate }, "LISTING_NOT_PUBLIC"},
		{"invalid identifier", func(record *catalog.CatalogRecord) { record.Resource.CanonicalIdentifier = "urn:example:agent" }, "AIR_IDENTIFIER_REQUIRED"},
		{"blocked evidence", func(record *catalog.CatalogRecord) { record.Evidence.State = catalog.EvidenceBlocked }, "PUBLICATION_EVIDENCE_BLOCKED"},
		{"wrong media type", func(record *catalog.CatalogRecord) {
			record.VersionRevision.Representations[0].MediaType = "application/json"
		}, "STANDARD_PUBLIC_REPRESENTATION_REQUIRED"},
		{"invalid representation", func(record *catalog.CatalogRecord) {
			record.VersionRevision.Representations[0].ValidationState = "invalid"
		}, "STANDARD_PUBLIC_REPRESENTATION_REQUIRED"},
		{"missing delivery", func(record *catalog.CatalogRecord) { record.VersionRevision.Representations[0].Data = nil }, "PUBLIC_DELIVERY_FORM_REQUIRED"},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			candidate := eligiblePublicationRecord()
			testCase.mutate(&candidate)
			_, _, reasons := manager.baseEntry(candidate)
			if !containsString(reasons, testCase.wantReason) {
				t.Fatalf("reasons = %#v, want %s", reasons, testCase.wantReason)
			}
		})
	}

	artifactRecord := eligiblePublicationRecord()
	artifactRecord.Release.ActivatedAt = time.Time{}
	artifactRecord.VersionRevision.Representations[0].Data = nil
	artifactRecord.VersionRevision.Representations[0].ArtifactID = "artifact-1"
	entry, artifactID, reasons := manager.baseEntry(artifactRecord)
	if len(reasons) != 0 || artifactID != "artifact-1" || entry.URL != "https://catalog.example.com/public/v1/artifacts/artifact-1" || entry.Metadata[catalogCreatedAtMetadata] != artifactRecord.Release.UpdatedAt.UTC().Format(time.RFC3339Nano) {
		t.Fatalf("artifact entry = %#v, artifact %q, reasons %#v", entry, artifactID, reasons)
	}
	externalRecord := eligiblePublicationRecord()
	externalRecord.VersionRevision.Representations[0].Data = nil
	externalRecord.VersionRevision.Representations[0].URL = "https://agents.example.com/card"
	externalRecord.VersionRevision.Representations[0].ArtifactID = "retained-validation-bytes"
	entry, artifactID, reasons = manager.baseEntry(externalRecord)
	if len(reasons) != 0 || artifactID != "" || entry.URL != externalRecord.VersionRevision.Representations[0].URL {
		t.Fatalf("external entry = %#v, artifact %q, reasons %#v", entry, artifactID, reasons)
	}
	terminalRecord := eligiblePublicationRecord()
	terminalRecord.Resource.CanonicalIdentifier = "urn:air:catalog.example.com:terminal-agent"
	entry, _, reasons = manager.baseEntry(terminalRecord)
	if len(reasons) != 0 || entry.Identifier != terminalRecord.Resource.CanonicalIdentifier {
		t.Fatalf("terminal-only ARD entry = %#v, reasons %#v", entry, reasons)
	}
	for name, kindAndType := range map[string]struct {
		kind      catalog.Kind
		mediaType string
	}{
		"MCP Server": {catalog.KindMCPServer, adapter.MediaMCPServerCard},
		"Skill":      {catalog.KindSkill, adapter.MediaSkillMarkdown},
	} {
		t.Run(name, func(t *testing.T) {
			candidate := eligiblePublicationRecord()
			candidate.Resource.Kind = kindAndType.kind
			candidate.VersionRevision.Representations[0].MediaType = kindAndType.mediaType
			if _, _, reasons := manager.baseEntry(candidate); len(reasons) != 0 {
				t.Fatalf("eligible %s reasons = %#v", name, reasons)
			}
		})
	}
}

func TestSearchContinuationResumesAtActuallyEmittedLocalResult(t *testing.T) {
	records := make([]catalog.CatalogRecord, 0, 3)
	for index, name := range []string{"alpha", "beta", "gamma"} {
		record := eligiblePublicationRecord()
		record.Resource.ID = "resource-" + name
		record.Resource.CanonicalIdentifier = "urn:air:catalog.example.com:default:" + name
		record.Version.ID = "version-" + name
		record.ListingRevision.DisplayName = name + " resumable"
		record.ListingRevision.Description = "A resumable local result."
		record.ListingRevision.RepresentativeQueries = []string{"Find a resumable resource", "Show resumable resources"}
		record.Release.ID = "release-" + name
		record.Release.UpdatedAt = record.Release.UpdatedAt.Add(time.Duration(index) * time.Minute)
		records = append(records, record)
	}
	manager, _, _ := newPublicationManager(t, records)
	activatePublicationSnapshot(t, manager)
	request := SearchRequest{Query: SearchQuery{Text: "resumable"}, Federation: "auto", PageSize: 2}
	first, err := manager.Search(context.Background(), "tenant-1", request)
	if err != nil || len(first.Results) != 2 {
		t.Fatalf("first local page = %#v, %v", first, err)
	}
	if _, err := manager.SearchContinuation(first, -1); catalog.ErrorCodeOf(err) != catalog.CodeInternal {
		t.Fatalf("negative emitted count returned %v", err)
	}
	if _, err := manager.SearchContinuation(first, 3); catalog.ErrorCodeOf(err) != catalog.CodeInternal {
		t.Fatalf("oversized emitted count returned %v", err)
	}
	token, err := manager.SearchContinuation(first, 1)
	if err != nil || token == "" {
		t.Fatalf("rebased continuation = %q, %v", token, err)
	}
	request.PageToken = token
	second, err := manager.Search(context.Background(), "tenant-1", request)
	if err != nil || len(second.Results) != 2 || second.Results[0].Identifier != first.Results[1].Identifier || second.PageToken != "" {
		t.Fatalf("rebased local page = %#v, %v", second, err)
	}
	rotatedCandidate, err := manager.BuildCandidate(publicationContext(t.Context(), "tenant-1"), "tenant-1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Activate(publicationContext(t.Context(), "tenant-1"), "tenant-1", rotatedCandidate.ID, "Activate reviewed snapshot"); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Search(context.Background(), "tenant-1", request); catalog.ErrorCodeOf(err) != catalog.CodeInvalidArgument {
		t.Fatalf("pre-rotation auto continuation returned %v", err)
	}
}

func TestSearchCursorBindsExactHostAndAudience(t *testing.T) {
	records := []catalog.CatalogRecord{eligiblePublicationRecord(), eligiblePublicationRecord()}
	records[1].Resource.ID = "resource-two"
	records[1].Resource.CanonicalIdentifier = "urn:air:catalog.example.com:default:two"
	records[1].Version.ID = "version-two"
	records[1].Release.ID = "release-two"
	key := sha256.Sum256([]byte("host-audience-cursor-test"))
	newManager := func(host string, audience Audience) *Manager {
		t.Helper()
		manager, err := NewManager(Options{Records: publicationRecordSource{records: records}, Store: NewMemoryStore(), Clock: &mutablePublicationClock{now: time.Date(2026, time.September, 2, 12, 0, 0, 0, time.UTC)}, IDs: &sequencePublicationIDs{}, BaseURL: "https://" + host, PublisherDomain: "catalog.example.com", ARDProfile: "ard-test", AICatalogProfile: "ai-catalog-test", CursorKey: key[:], Host: host, Audience: audience})
		if err != nil {
			t.Fatal(err)
		}
		activatePublicationSnapshot(t, manager)
		return manager
	}
	request := SearchRequest{Query: SearchQuery{Text: "agent"}, Federation: "auto", PageSize: 1}
	first, err := newManager("one.example.com", AudienceAnonymous).Search(context.Background(), "tenant-1", request)
	if err != nil || first.PageToken == "" {
		t.Fatalf("first page = %#v, %v", first, err)
	}
	request.PageToken = first.PageToken
	for name, manager := range map[string]*Manager{
		"host":     newManager("two.example.com", AudienceAnonymous),
		"audience": newManager("one.example.com", AudienceAuthenticated),
	} {
		if _, err := manager.Search(context.Background(), "tenant-1", request); catalog.ErrorCodeOf(err) != catalog.CodeInvalidArgument {
			t.Fatalf("cursor crossed %s boundary: %v", name, err)
		}
	}
}

func containsString(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

func TestPublicationSnapshotLifecycleAndArtifactAllowlist(t *testing.T) {
	record := eligiblePublicationRecord()
	record.VersionRevision.Representations[0].Data = nil
	record.VersionRevision.Representations[0].ArtifactID = "artifact-1"
	manager, store, clock := newPublicationManager(t, []catalog.CatalogRecord{record})
	ctx := publicationContext(context.Background(), "tenant-1")

	first := activatePublicationSnapshot(t, manager)
	if !manager.ArtifactAllowed(ctx, "tenant-1", "artifact-1") || manager.ArtifactAllowed(ctx, "tenant-1", "missing") {
		t.Fatal("active artifact allowlist was not enforced")
	}
	if got, err := manager.GetSnapshot(ctx, "tenant-1", first.ID); err != nil || got.ID != first.ID {
		t.Fatalf("get snapshot = %#v, %v", got, err)
	}
	if snapshots, err := manager.ListSnapshotSummaries(ctx, "tenant-1", SnapshotListRequest{Limit: 10}); err != nil || len(snapshots) != 1 {
		t.Fatalf("snapshot list = %#v, %v", snapshots, err)
	}
	for _, state := range []SnapshotState{"ACTIVE", "removed", " active"} {
		if _, err := manager.ListSnapshotSummaries(ctx, "tenant-1", SnapshotListRequest{State: state, Limit: 1}); catalog.ErrorCodeOf(err) != catalog.CodeInvalidArgument {
			t.Fatalf("invalid snapshot state %q: %v", state, err)
		}
	}
	if _, err := store.Activate(ctx, "tenant-1", first.ID, clock.now); catalog.ErrorCodeOf(err) != catalog.CodeInvalidTransition {
		t.Fatalf("reactivation returned %v", err)
	}

	secondCandidate, err := manager.BuildCandidate(ctx, "tenant-1")
	if err != nil || secondCandidate.Generation != 2 {
		t.Fatalf("second candidate = %#v, %v", secondCandidate, err)
	}
	second, err := manager.Activate(ctx, "tenant-1", secondCandidate.ID, "Activate reviewed snapshot")
	if err != nil {
		t.Fatal(err)
	}
	retiredFirst, err := manager.GetSnapshot(ctx, "tenant-1", first.ID)
	if err != nil || retiredFirst.State != SnapshotRetired || second.State != SnapshotActive {
		t.Fatalf("activation states = first %#v, second %#v, %v", retiredFirst, second, err)
	}
	if _, err := manager.Rollback(ctx, "tenant-1", second.ID, "Restore reviewed snapshot"); catalog.ErrorCodeOf(err) != catalog.CodeInvalidTransition {
		t.Fatalf("active rollback returned %v", err)
	}
	retiredSecond, err := manager.Unpublish(ctx, "tenant-1", "Withdraw current publication")
	if err != nil || retiredSecond.State != SnapshotRetired || manager.ArtifactAllowed(ctx, "tenant-1", "artifact-1") {
		t.Fatalf("unpublished snapshot = %#v, %v", retiredSecond, err)
	}
	if _, err := manager.Unpublish(ctx, "tenant-1", "Withdraw current publication"); catalog.ErrorCodeOf(err) != catalog.CodeNotFound {
		t.Fatalf("second unpublish returned %v", err)
	}
	restored, err := manager.Rollback(ctx, "tenant-1", first.ID, "Restore reviewed snapshot")
	if err != nil || restored.State != SnapshotActive {
		t.Fatalf("rollback = %#v, %v", restored, err)
	}
	if _, err := manager.Rollback(ctx, "other-tenant", first.ID, "Restore reviewed snapshot"); catalog.ErrorCodeOf(err) != catalog.CodeForbidden {
		t.Fatalf("cross-tenant rollback returned %v", err)
	}
	if _, err := store.SaveCandidate(ctx, Snapshot{ID: "duplicate-generation", TenantID: "tenant-1", Host: secondCandidate.Host, Generation: secondCandidate.Generation}); catalog.ErrorCodeOf(err) != catalog.CodeConflict {
		t.Fatalf("duplicate generation returned %v", err)
	}
}

func TestPublicationDiscoveryOperationsValidateAndPageResults(t *testing.T) {
	now := time.Date(2026, time.August, 30, 12, 0, 0, 0, time.UTC)
	records := make([]catalog.CatalogRecord, 0, 3)
	for index, name := range []string{"Alpha", "Beta", "Gamma"} {
		record := eligiblePublicationRecord()
		record.Resource.ID = "resource-" + strings.ToLower(name)
		record.Resource.CanonicalIdentifier = "urn:air:catalog.example.com:default:" + strings.ToLower(name)
		record.Version.ID = "version-" + strings.ToLower(name)
		record.Version.Version = fmt.Sprintf("1.0.%d", index)
		record.ListingRevision.DisplayName = name + " agent"
		record.ListingRevision.Description = "Handles " + strings.ToLower(name) + " cases"
		record.ListingRevision.Tags = []string{"shared", strings.ToLower(name)}
		record.Release.ID = "release-" + strings.ToLower(name)
		record.Release.ActivatedAt = now.Add(time.Duration(index) * time.Hour)
		record.Release.UpdatedAt = now.Add(time.Duration(index) * time.Hour)
		records = append(records, record)
	}
	manager, _, _ := newPublicationManager(t, records)
	ctx := publicationContext(context.Background(), "tenant-1")

	searchErrors := []SearchRequest{
		{},
		{Query: SearchQuery{Text: strings.Repeat("x", 501)}},
		{Query: SearchQuery{Text: "agent"}, Federation: "live"},
		{Query: SearchQuery{Text: "agent"}, PageSize: -1},
		{Query: SearchQuery{Text: "agent"}, PageSize: 101},
	}
	for index, request := range searchErrors {
		if _, err := manager.Search(ctx, "tenant-1", request); catalog.ErrorCodeOf(err) != catalog.CodeInvalidArgument {
			t.Fatalf("invalid search %d returned %v", index, err)
		}
	}
	if _, err := manager.Search(ctx, "tenant-1", SearchRequest{Query: SearchQuery{Text: "agent"}}); catalog.ErrorCodeOf(err) != catalog.CodeNotFound {
		t.Fatalf("inactive search error = %v", err)
	}
	if _, err := manager.List(ctx, "tenant-1", ListRequest{}); catalog.ErrorCodeOf(err) != catalog.CodeNotFound {
		t.Fatalf("inactive list error = %v", err)
	}
	if _, err := manager.Explore(ctx, "tenant-1", ExploreRequest{ResultType: ExploreResultType{Facets: []ExploreFacetRequest{{Field: "type"}}}}); catalog.ErrorCodeOf(err) != catalog.CodeNotFound {
		t.Fatalf("inactive explore error = %v", err)
	}

	snapshot := activatePublicationSnapshot(t, manager)
	search, err := manager.Search(ctx, "tenant-1", SearchRequest{Query: SearchQuery{Text: "beta", Filter: map[string]any{"tags": "beta"}}, Federation: "referrals"})
	if err != nil || len(search.Results) != 1 || search.Results[0].DisplayName != "Beta agent" {
		t.Fatalf("filtered search = %#v, %v", search, err)
	}
	binding, err := manager.cursorBinding("tenant-1", "search", struct {
		Query      SearchQuery `json:"query"`
		Federation string      `json:"federation"`
		PageSize   int         `json:"pageSize"`
	}{Query: SearchQuery{Text: "agent"}, Federation: "none", PageSize: 10})
	if err != nil {
		t.Fatal(err)
	}
	staleOffset := manager.encodeCursor(snapshot.Generation, 999, binding)
	if _, err := manager.Search(ctx, "tenant-1", SearchRequest{Query: SearchQuery{Text: "agent"}, Federation: "none", PageSize: 10, PageToken: staleOffset}); catalog.ErrorCodeOf(err) != catalog.CodeInvalidArgument {
		t.Fatalf("stale search offset returned %v", err)
	}

	exploreErrors := []ExploreRequest{
		{},
		{ResultType: ExploreResultType{Facets: make([]ExploreFacetRequest, 11)}},
		{Query: &SearchQuery{Text: strings.Repeat("x", 501)}, ResultType: ExploreResultType{Facets: []ExploreFacetRequest{{Field: "type"}}}},
		{ResultType: ExploreResultType{Facets: []ExploreFacetRequest{{Field: "unknown"}}}},
		{ResultType: ExploreResultType{Facets: []ExploreFacetRequest{{Field: "type"}, {Field: "type"}}}},
		{ResultType: ExploreResultType{Facets: []ExploreFacetRequest{{Field: "type", Limit: -1}}}},
	}
	for index, request := range exploreErrors {
		if _, err := manager.Explore(ctx, "tenant-1", request); catalog.ErrorCodeOf(err) != catalog.CodeInvalidArgument {
			t.Fatalf("invalid explore %d returned %v", index, err)
		}
	}
	explore, err := manager.Explore(ctx, "tenant-1", ExploreRequest{
		Query: &SearchQuery{Text: "agent", Filter: map[string]any{"tags": "shared"}},
		ResultType: ExploreResultType{Facets: []ExploreFacetRequest{
			{Field: "tags", Limit: 1},
			{Field: "publisher"},
			{Field: "metadata." + catalogReleaseIDMetadata},
		}},
	})
	if err != nil || explore.Facets["tags"].OtherCount != 3 || len(explore.Facets["publisher"].Buckets) != 1 {
		t.Fatalf("active explore = %#v, %v", explore, err)
	}

	for _, request := range []ListRequest{{PageSize: -1}, {PageSize: 101}, {OrderBy: "identifier"}, {Filter: "invalid = 'value'"}} {
		if _, err := manager.List(ctx, "tenant-1", request); catalog.ErrorCodeOf(err) != catalog.CodeInvalidArgument {
			t.Fatalf("invalid list %#v returned %v", request, err)
		}
	}
	first, err := manager.List(ctx, "tenant-1", ListRequest{Filter: "type = '" + adapter.MediaA2AAgentCard + "'", OrderBy: "updatedAt DESC", PageSize: 1})
	if err != nil || first.Total != 3 || len(first.Items) != 1 || first.Items[0].DisplayName != "Gamma agent" || first.PageToken == "" {
		t.Fatalf("first list page = %#v, %v", first, err)
	}
	second, err := manager.List(ctx, "tenant-1", ListRequest{Filter: "type = '" + adapter.MediaA2AAgentCard + "'", OrderBy: "updatedAt DESC", PageSize: 1, PageToken: first.PageToken})
	if err != nil || len(second.Items) != 1 || second.Items[0].DisplayName != "Beta agent" {
		t.Fatalf("second list page = %#v, %v", second, err)
	}
	ascending, err := manager.List(ctx, "tenant-1", ListRequest{OrderBy: "updatedAt"})
	if err != nil || ascending.Items[0].DisplayName != "Open Agentic Catalog Registry" || ascending.Items[len(ascending.Items)-1].DisplayName != "Gamma agent" {
		t.Fatalf("ascending list = %#v, %v", ascending, err)
	}
	descending, err := manager.List(ctx, "tenant-1", ListRequest{OrderBy: "displayName DESC"})
	if err != nil || descending.Items[0].DisplayName != "Open Agentic Catalog Registry" {
		t.Fatalf("descending list = %#v, %v", descending, err)
	}
}

func TestPublicationQueryPrimitivesCoverEverySupportedShape(t *testing.T) {
	entry := Entry{
		Identifier:            "urn:air:catalog.example.com:default:agent",
		DisplayName:           "Catalog agent",
		Type:                  adapter.MediaA2AAgentCard,
		RepresentativeQueries: []string{"Find an agent"},
		Capabilities:          []string{"catalog"},
		Description:           "Finds governed resources.",
		Tags:                  []string{"governed"},
		Version:               "1.0.0",
		UpdatedAt:             "2026-08-30T12:00:00Z",
		Metadata:              map[string]any{"region": "eu"},
	}
	wantValues := map[string]string{
		"type":                  entry.Type,
		"tags":                  "governed",
		"capabilities":          "catalog",
		"representativeQueries": "Find an agent",
		"description":           entry.Description,
		"version":               entry.Version,
		"updatedAt":             entry.UpdatedAt,
		"displayName":           entry.DisplayName,
		"identifier":            entry.Identifier,
		"publisher":             "catalog.example.com",
		"metadata.region":       "eu",
	}
	for field, want := range wantValues {
		values := facetValues(entry, field, nil)
		if len(values) != 1 || values[0] != want {
			t.Fatalf("facetValues(%q) = %#v, want %q", field, values, want)
		}
	}
	invalidIdentifier := entry
	invalidIdentifier.Identifier = "not-an-air-identifier"
	if values := facetValues(invalidIdentifier, "publisher", nil); values != nil {
		t.Fatalf("invalid publisher facet = %#v", values)
	}
	if values := facetValues(entry, "metadata.missing", nil); values != nil {
		t.Fatalf("missing metadata facet = %#v", values)
	}
	if values := facetValues(entry, "unsupported", nil); values != nil {
		t.Fatalf("unsupported facet = %#v", values)
	}

	if !entryLess(Entry{Identifier: "a"}, Entry{Identifier: "b"}) || !entryLess(Entry{Identifier: "a", Version: "1"}, Entry{Identifier: "a", Version: "2"}) || !entryLess(Entry{Identifier: "a", Version: "1", Type: "a"}, Entry{Identifier: "a", Version: "1", Type: "b"}) || entryLess(entry, entry) {
		t.Fatal("entry tie-break ordering is inconsistent")
	}
	if values := toStrings([]string{"one", "two"}); len(values) != 2 || values[1] != "two" {
		t.Fatalf("string slice = %#v", values)
	}
	if values := toStrings([]any{"one", "two"}); len(values) != 2 || values[1] != "two" {
		t.Fatalf("interface slice = %#v", values)
	}
	if toStrings([]any{"one", 2}) != nil || toStrings(42) != nil {
		t.Fatal("non-string filter values were accepted")
	}
	if matchFilter(entry, map[string]any{"tags": []any{}}, nil) || matchFilter(entry, map[string]any{"tags": "missing"}, nil) {
		t.Fatal("empty or nonmatching structured filter matched")
	}
}

func TestPublicationQueryBudgetsAndCursorSyntaxFailClosed(t *testing.T) {
	tooManyFields := make(map[string]any, maxStructuredFilters+1)
	for index := 0; index <= maxStructuredFilters; index++ {
		tooManyFields[fmt.Sprintf("metadata.field%d", index)] = "value"
	}
	totalValues := make([]any, 70)
	for index := range totalValues {
		totalValues[index] = fmt.Sprintf("value-%d", index)
	}
	invalidQueries := []SearchQuery{
		{Filter: tooManyFields},
		{Context: map[string]any{"kind": "https://agenticresourcediscovery.org/ns#mediaType"}, Filter: map[string]any{"kind": "one", "type": "two"}},
		{Filter: map[string]any{"tags": make([]string, 101)}},
		{Filter: map[string]any{"tags": totalValues, "capabilities": totalValues, "representativeQueries": totalValues}},
		{Filter: map[string]any{"tags": " "}},
		{Filter: map[string]any{"tags": strings.Repeat("x", 513)}},
	}
	for index := range invalidQueries {
		query := invalidQueries[index]
		if err := normalizeStructuredQuery(&query); catalog.ErrorCodeOf(err) != catalog.CodeInvalidArgument {
			t.Fatalf("invalid structured query %d returned %v", index, err)
		}
	}

	tooManyClauses := "type = 'one' AND publisherId = 'two' AND displayName = 'three' AND createdAfter > '2026-01-01' AND updatedAfter > '2026-01-01' AND type = 'six'"
	tooManyValues := make([]string, 21)
	for index := range tooManyValues {
		tooManyValues[index] = fmt.Sprintf("type-%d", index)
	}
	invalidFilters := []string{
		strings.Repeat("x", 2049),
		"",
		tooManyClauses,
		" AND type = 'one'",
		"type = 'one' AND ",
		"displayName > 'name'",
		"displayName = ''",
		"displayName = '" + strings.Repeat("x", 161) + "'",
		"type = ''",
		"type = 'one,one'",
		"type = '" + strings.Join(tooManyValues, ",") + "'",
	}
	for _, filter := range invalidFilters {
		if _, err := parseListFilter(filter); catalog.ErrorCodeOf(err) != catalog.CodeInvalidArgument {
			t.Fatalf("invalid List filter %q returned %v", filter, err)
		}
	}

	snapshot := Snapshot{CreatedAt: time.Date(2026, time.August, 30, 12, 0, 0, 0, time.UTC)}
	entry := Entry{Identifier: "invalid", DisplayName: "Agent", Type: "type", UpdatedAt: "invalid", Metadata: map[string]any{catalogCreatedAtMetadata: "invalid"}}
	nonMatches := [][]listFilterClause{
		{{field: "displayName", values: []string{"Other"}}},
		{{field: "type", values: []string{"other"}}},
		{{field: "publisherId", values: []string{"catalog.example.com"}}},
		{{field: "createdAfter", after: snapshot.CreatedAt.Add(-time.Hour)}},
		{{field: "updatedAfter", after: snapshot.CreatedAt.Add(-time.Hour)}},
	}
	for index, clauses := range nonMatches {
		if matchListFilter(entry, snapshot, clauses) {
			t.Fatalf("nonmatching List clause %d matched", index)
		}
	}

	manager, _, clock := newPublicationManager(t, nil)
	if offset, err := manager.decodeCursor("", 1, "binding"); err != nil || offset != 0 {
		t.Fatalf("empty cursor = %d, %v", offset, err)
	}
	invalidTokens := []string{
		strings.Repeat("x", 513),
		"%",
		base64.RawURLEncoding.EncodeToString([]byte("2:1:0:999:00")),
		base64.RawURLEncoding.EncodeToString([]byte("1:1:0:999:not-hex")),
		base64.RawURLEncoding.EncodeToString([]byte("1:1:0:999:00")),
		manager.encodeCursor(2, 0, "binding"),
		manager.encodeCursor(1, -1, "binding"),
	}
	for index, token := range invalidTokens {
		if _, err := manager.decodeCursor(token, 1, "binding"); catalog.ErrorCodeOf(err) != catalog.CodeInvalidArgument {
			t.Fatalf("invalid cursor %d returned %v", index, err)
		}
	}
	expiring := manager.encodeCursor(1, 0, "binding")
	clock.now = clock.now.Add(cursorTTL + time.Second)
	if _, err := manager.decodeCursor(expiring, 1, "binding"); catalog.ErrorCodeOf(err) != catalog.CodeInvalidArgument {
		t.Fatalf("expired cursor returned %v", err)
	}
}
