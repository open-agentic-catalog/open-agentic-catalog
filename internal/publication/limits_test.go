package publication

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
)

type blockingPublicationRecordSource struct {
	entered chan<- struct{}
	release <-chan struct{}
}

func (source blockingPublicationRecordSource) PublicationRecords(ctx context.Context, _ string, _ bool, _ int) ([]catalog.CatalogRecord, bool, error) {
	select {
	case source.entered <- struct{}{}:
	case <-ctx.Done():
		return nil, false, ctx.Err()
	}
	select {
	case <-source.release:
		return nil, false, nil
	case <-ctx.Done():
		return nil, false, ctx.Err()
	}
}

type recordingPublicationRecordSource struct{ called chan<- struct{} }

func (source recordingPublicationRecordSource) PublicationRecords(context.Context, string, bool, int) ([]catalog.CatalogRecord, bool, error) {
	source.called <- struct{}{}
	return nil, false, nil
}

func TestConstructionBudgetMeasuresExactDocumentGrowth(t *testing.T) {
	registry := Entry{Identifier: "registry:self", DisplayName: "Registry", Type: "application/ai-registry+json", URL: "https://catalog.example.com/ard/v1/"}
	host := AICatalogHost{DisplayName: "Catalog", Identifier: "https://catalog.example.com"}
	budget, err := newConstructionBudget(registry, host)
	if err != nil {
		t.Fatal(err)
	}
	first := Entry{Identifier: "urn:air:catalog.example.com:default:one", DisplayName: "One", Type: "application/json", Data: json.RawMessage(`{"name":"one"}`)}
	firstAI := toAICatalogEntry(first)
	if err := budget.addEntries(context.Background(), &first, firstAI, 0); err != nil {
		t.Fatal(err)
	}
	ard, err := json.Marshal(ARDManifest{Entries: []Entry{registry, first}})
	if err != nil {
		t.Fatal(err)
	}
	ai, err := json.Marshal(AICatalog{SpecVersion: "1.0", Host: host, Entries: []AICatalogEntry{firstAI}})
	if err != nil {
		t.Fatal(err)
	}
	if budget.ardBytes != len(ard) || budget.aiBytes != len(ai) {
		t.Fatalf("measured bytes = ARD %d AI %d, encoded = ARD %d AI %d", budget.ardBytes, budget.aiBytes, len(ard), len(ai))
	}
}

func TestPublicationSnapshotAndWorkBounds(t *testing.T) {
	exact := Snapshot{ARD: ARDManifest{Entries: make([]Entry, MaxPublicationARDEntries)}, AICatalog: AICatalog{Entries: make([]AICatalogEntry, MaxPublicationAICatalogEntries)}, Exclusions: make([]Exclusion, MaxPublicationExclusions), ArtifactIDs: make([]string, MaxPublicationArtifactReferences)}
	if err := ValidateSnapshotBounds(exact); err != nil {
		t.Fatalf("exact entry limits were rejected: %v", err)
	}
	overflow := exact
	overflow.ARD.Entries = append(overflow.ARD.Entries, Entry{})
	if err := ValidateSnapshotBounds(overflow); catalog.ErrorCodeOf(err) != catalog.CodeWorkLimitExceeded {
		t.Fatalf("entry overflow = %v", err)
	}
	budget := constructionBudget{workUnits: MaxPublicationWorkUnits - 1}
	if err := budget.addRecord(context.Background(), catalog.CatalogRecord{}); err != nil || budget.workUnits != MaxPublicationWorkUnits {
		t.Fatalf("exact work limit = %d, %v", budget.workUnits, err)
	}
	if err := budget.addRecord(context.Background(), catalog.CatalogRecord{}); catalog.ErrorCodeOf(err) != catalog.CodeWorkLimitExceeded {
		t.Fatalf("work overflow = %v", err)
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := budget.addRecord(cancelled, catalog.CatalogRecord{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled construction = %v", err)
	}
}

func TestPublicationEncodedByteBudgetsRejectMalformedAndOversizedEntries(t *testing.T) {
	registry := Entry{Identifier: "registry:self", Data: json.RawMessage(`{`)}
	if _, err := newConstructionBudget(registry, AICatalogHost{}); catalog.ErrorCodeOf(err) != catalog.CodeInternal {
		t.Fatalf("malformed Registry entry returned %v", err)
	}

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := (&constructionBudget{}).addEntries(cancelled, nil, AICatalogEntry{}, 0); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled entry measurement returned %v", err)
	}
	if err := (&constructionBudget{}).addEntries(context.Background(), nil, AICatalogEntry{Data: json.RawMessage(`{`)}, 0); catalog.ErrorCodeOf(err) != catalog.CodeInternal {
		t.Fatalf("malformed AI Catalog entry returned %v", err)
	}
	if err := (&constructionBudget{}).addEntries(context.Background(), &Entry{Data: json.RawMessage(`{`)}, AICatalogEntry{}, 0); catalog.ErrorCodeOf(err) != catalog.CodeInternal {
		t.Fatalf("malformed ARD entry returned %v", err)
	}
	large := strings.Repeat("x", MaxPublicationDocumentBytes)
	if err := (&constructionBudget{}).addEntries(context.Background(), nil, AICatalogEntry{Description: large}, 0); catalog.ErrorCodeOf(err) != catalog.CodeWorkLimitExceeded {
		t.Fatalf("oversized AI Catalog entry returned %v", err)
	}
	if err := (&constructionBudget{}).addEntries(context.Background(), &Entry{Description: large}, AICatalogEntry{}, 0); catalog.ErrorCodeOf(err) != catalog.CodeWorkLimitExceeded {
		t.Fatalf("oversized ARD entry returned %v", err)
	}
}

func TestValidateSnapshotBoundsRejectsMalformedAndOversizedDocuments(t *testing.T) {
	tests := []struct {
		name     string
		snapshot Snapshot
	}{
		{"malformed ARD", Snapshot{ARD: ARDManifest{Entries: []Entry{{Data: json.RawMessage(`{`)}}}}},
		{"malformed AI Catalog", Snapshot{AICatalog: AICatalog{Entries: []AICatalogEntry{{Data: json.RawMessage(`{`)}}}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := ValidateSnapshotBounds(test.snapshot); catalog.ErrorCodeOf(err) != catalog.CodePublicationNotReady {
				t.Fatalf("malformed snapshot = %v", err)
			}
		})
	}
	for _, kind := range []string{"ARD", "AI Catalog", "retained snapshot"} {
		t.Run(kind, func(t *testing.T) {
			snapshot := Snapshot{ARD: ARDManifest{Entries: []Entry{{}}}, AICatalog: AICatalog{Entries: []AICatalogEntry{{}}}, Exclusions: []Exclusion{{Reasons: []string{""}}}}
			var document any
			var padding *string
			maximum := MaxPublicationDocumentBytes
			switch kind {
			case "ARD":
				document, padding = snapshot.ARD, &snapshot.ARD.Entries[0].Description
			case "AI Catalog":
				document, padding = snapshot.AICatalog, &snapshot.AICatalog.Entries[0].Description
			default:
				document, padding = snapshot, &snapshot.Exclusions[0].Reasons[0]
				maximum = MaxPublicationSnapshotBytes
			}
			// Keep the field present while measuring JSON overhead (omitempty).
			*padding = "x"
			raw, err := json.Marshal(document)
			if err != nil {
				t.Fatal(err)
			}
			*padding = strings.Repeat("x", maximum-len(raw)+1)
			if err := ValidateSnapshotBounds(snapshot); err != nil {
				t.Fatalf("exact limit: %v", err)
			}
			*padding += "x"
			if err := ValidateSnapshotBounds(snapshot); catalog.ErrorCodeOf(err) != catalog.CodeWorkLimitExceeded {
				t.Fatalf("overflow: %v", err)
			}
		})
	}
}

func TestEncodedSnapshotBoundIsCheckedBeforeDecoding(t *testing.T) {
	raw := make([]byte, MaxPublicationSnapshotBytes+1)
	if err := ValidateEncodedSnapshotBounds(raw[:MaxPublicationSnapshotBytes]); err != nil {
		t.Fatalf("exact encoded snapshot limit was rejected: %v", err)
	}
	if err := ValidateEncodedSnapshotBounds(raw); catalog.ErrorCodeOf(err) != catalog.CodeWorkLimitExceeded {
		t.Fatalf("encoded snapshot overflow = %v", err)
	}
}

func TestRetainedPublicationReferencesStayBoundToTheirSnapshotHost(t *testing.T) {
	t.Parallel()
	artifactID := strings.Repeat("a", 64)
	if !retainedRegistryURL("catalog.example.com", "https://catalog.example.com:8443/ard/v1/") {
		t.Fatal("valid retained Registry URL was rejected")
	}
	for _, invalid := range []string{
		"https://catalog.example.com/%zz",
		"https://other.example.com/ard/v1/",
		"https://catalog.example.com/ard/v1/?token=x",
		"https://user@catalog.example.com/ard/v1/",
		"https://catalog.example.com/ard/v1",
		"ftp://catalog.example.com/ard/v1/",
		"https://catalog.example.com/ard/v1/#fragment",
	} {
		if retainedRegistryURL("catalog.example.com", invalid) {
			t.Fatalf("invalid retained Registry URL %q was accepted", invalid)
		}
	}
	if got, managed, err := retainedArtifactID("catalog.example.com", "http://catalog.example.com/public/v1/artifacts/"+artifactID); err != nil || !managed || got != artifactID {
		t.Fatalf("retained artifact = %q, managed=%t, %v", got, managed, err)
	}
	if _, managed, err := retainedArtifactID("catalog.example.com", "https://external.example.com/public/v1/artifacts/not-catalog-managed"); err != nil || managed {
		t.Fatalf("external artifact classified as managed=%t, %v", managed, err)
	}
	if _, managed, err := retainedArtifactID("catalog.example.com", "https://catalog.example.com/%zz"); err == nil || managed {
		t.Fatalf("malformed artifact URL returned managed=%t, %v", managed, err)
	}
	for _, invalid := range []string{
		"https://catalog.example.com/public/v1/artifacts/%00name",
		"https://catalog.example.com/public/v1/artifacts/%7fname",
		"https://catalog.example.com/public/v1/artifacts/" + strings.Repeat("a", 513),
		"https://catalog.example.com/public/v1/artifacts/name#fragment",
		"https://catalog.example.com/public/v1/artifacts/",
		"https://catalog.example.com/public/v1/artifacts/one/two",
		"https://catalog.example.com/public/v1/artifacts/" + artifactID + "?token=x",
		"https://user@catalog.example.com/public/v1/artifacts/" + artifactID,
		"ftp://catalog.example.com/public/v1/artifacts/" + artifactID,
	} {
		if _, managed, err := retainedArtifactID("catalog.example.com", invalid); err == nil || !managed {
			t.Fatalf("invalid managed artifact URL %q returned managed=%t, %v", invalid, managed, err)
		}
	}
}

func TestConstructionLimiterBoundsConcurrentHostsAndHonorsCancellation(t *testing.T) {
	var missing *ConstructionLimiter
	if _, err := missing.acquire(context.Background()); err == nil {
		t.Fatal("nil construction limiter was accepted")
	}
	limiter := NewConstructionLimiter()
	release, err := limiter.acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	waiting, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if _, err := limiter.acquire(waiting); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("contended construction = %v", err)
	}
	release()
	secondRelease, err := limiter.acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	secondRelease()
}

func TestSharedConstructionLimiterStopsASecondManagerBeforeItSelectsRecords(t *testing.T) {
	limiter := NewConstructionLimiter()
	key := sha256.Sum256([]byte(t.Name()))
	clock := &mutablePublicationClock{now: time.Date(2026, time.September, 4, 12, 0, 0, 0, time.UTC)}
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	called := make(chan struct{}, 1)
	newManager := func(records RecordSource, host string) *Manager {
		t.Helper()
		manager, err := NewManager(Options{Records: records, Store: NewMemoryStore(), Clock: clock, IDs: &sequencePublicationIDs{}, BaseURL: "https://" + host, PublisherDomain: host, ARDProfile: "ard-test", AICatalogProfile: "ai-test", CursorKey: key[:], Host: host, ConstructionLimiter: limiter})
		if err != nil {
			t.Fatal(err)
		}
		return manager
	}
	first := newManager(blockingPublicationRecordSource{entered: entered, release: release}, "one.example.com")
	second := newManager(recordingPublicationRecordSource{called: called}, "two.example.com")
	firstResult := make(chan error, 1)
	go func() {
		_, err := first.BuildCandidate(publicationContext(context.Background(), "tenant"), "tenant")
		firstResult <- err
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("first construction did not reach record selection")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if _, err := second.BuildCandidate(publicationContext(ctx, "tenant"), "tenant"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("contended manager returned %v", err)
	}
	select {
	case <-called:
		t.Fatal("second manager selected records before acquiring the shared construction slot")
	default:
	}
	close(release)
	if err := <-firstResult; err != nil {
		t.Fatalf("first construction failed: %v", err)
	}
}

func TestConstructionEntryBudgetAcceptsExactByteLimits(t *testing.T) {
	entry := Entry{Description: "bounded"}
	ai := toAICatalogEntry(entry)
	ardRaw, err := json.Marshal(entry)
	if err != nil {
		t.Fatal(err)
	}
	aiRaw, err := json.Marshal(ai)
	if err != nil {
		t.Fatal(err)
	}
	budget := constructionBudget{ardBytes: MaxPublicationDocumentBytes - len(ardRaw) - 1, aiBytes: MaxPublicationDocumentBytes - len(aiRaw) - 1}
	if err := budget.addEntries(t.Context(), &entry, ai, 1); err != nil || budget.ardBytes != MaxPublicationDocumentBytes || budget.aiBytes != MaxPublicationDocumentBytes {
		t.Fatalf("exact document growth: %#v, %v", budget, err)
	}
}

// A controlled JSON value cancels during encoding, after the initial context check.
type cancelPublicationEncoding struct{ cancel context.CancelFunc }

func (value cancelPublicationEncoding) MarshalJSON() ([]byte, error) {
	value.cancel()
	return []byte(`"value"`), nil
}

func TestConstructionEntryBudgetHonorsCancellationDuringEncoding(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	entry := AICatalogEntry{Extensions: map[string]any{"controlled": cancelPublicationEncoding{cancel: cancel}}}
	if err := (&constructionBudget{}).addEntries(ctx, nil, entry, 0); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation during encoding: %v", err)
	}
}
