package publication

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/ratings"
)

type publicationRatingsFixture struct {
	ratings.Store
	choice       ratings.Sharing
	aggregate    ratings.Aggregate
	err          error
	aggregateErr error
	calls        int
}

func (s *publicationRatingsFixture) GetSharing(context.Context, string, string, string) (ratings.Sharing, error) {
	return s.choice, s.err
}
func (s *publicationRatingsFixture) Aggregates(_ context.Context, _ string, targets []ratings.Target) (map[string]ratings.Aggregate, error) {
	s.calls++
	out := map[string]ratings.Aggregate{}
	for _, target := range targets {
		out[ratings.Key(target)] = s.aggregate
	}
	return out, s.aggregateErr
}

func TestPublicationRatingsRequireThreeConsentsAndRemainImmutable(t *testing.T) {
	record := eligiblePublicationRecord()
	key := sha256.Sum256([]byte(t.Name()))
	now := time.Date(2026, 10, 8, 0, 0, 0, 0, time.UTC)
	store := &publicationRatingsFixture{choice: ratings.Sharing{Enabled: true}, aggregate: ratings.Aggregate{Count: 9, Sum: 40, SharedCount: 5, SharedSum: 21}}
	makeManager := func(enabled bool, minimum int) *Manager {
		t.Helper()
		m, err := NewManager(Options{Records: publicationRecordSource{records: []catalog.CatalogRecord{record}}, Store: NewMemoryStore(), Clock: publicationClock{now}, IDs: publicationIDs{}, BaseURL: "https://catalog.example.com", PublisherDomain: "catalog.example.com", ARDProfile: "ard-test", AICatalogProfile: "ai-test", CursorKey: key[:], Ratings: store, RatingsSharingEnabled: enabled, RatingsMinimumSharedCount: minimum})
		if err != nil {
			t.Fatal(err)
		}
		return m
	}
	ctx := publicationContext(t.Context(), "tenant-1")
	disabled := makeManager(false, 5)
	candidate, err := disabled.BuildCandidate(ctx, "tenant-1")
	if err != nil || candidate.ARD.Entries[0].Ratings != nil || store.calls != 0 {
		t.Fatal("deployment opt-out leaked opinions", err)
	}
	manager := makeManager(true, 5)
	store.choice.Enabled = false
	candidate, err = manager.BuildCandidate(ctx, "tenant-1")
	if err != nil || candidate.ARD.Entries[0].Ratings != nil {
		t.Fatal("audience opt-out leaked opinions", err)
	}
	store.choice.Enabled = true
	store.aggregate.SharedCount = 4
	store.aggregate.SharedSum = 20
	candidate, err = manager.BuildCandidate(ctx, "tenant-1")
	if err != nil || candidate.ARD.Entries[0].Ratings != nil {
		t.Fatal("small group leaked opinions", err)
	}
	store.aggregate.SharedCount = 5
	store.aggregate.SharedSum = 21
	candidate, err = manager.BuildCandidate(ctx, "tenant-1")
	if err != nil {
		t.Fatal(err)
	}
	summary := candidate.ARD.Entries[0].Ratings
	if summary == nil || summary.Count != 5 || summary.Sum != 21 || summary.Average() != 4.2 || !summary.MeasuredAt.Equal(candidate.CreatedAt) {
		t.Fatalf("shared private votes or wrong measurement: %+v", summary)
	}
	if candidate.AICatalog.Entries[0].Extensions[ratings.IRI] == nil {
		t.Fatal("missing AI extension")
	}
	raw, err := json.Marshal(candidate.ARD)
	if err != nil {
		t.Fatal(err)
	}
	for _, private := range []string{"principalKey", "submittedBy", "membershipId", "consent", "sharedCount"} {
		if bytes.Contains(raw, []byte(private)) {
			t.Fatalf("public summary exposes %s", private)
		}
	}
	if err = manager.validateSnapshotEligibility(ctx, candidate); err != nil {
		t.Fatal(err)
	}
	// A score edit can change the published aggregate without changing its
	// contributor count. Both fields must still match at activation/rollback.
	store.aggregate.SharedSum = 20
	if err = manager.validateSnapshotEligibility(ctx, candidate); catalog.ErrorCodeOf(err) != catalog.CodePublicationNotReady {
		t.Fatalf("stale scores remained eligible: %v", err)
	}
	store.aggregate.SharedSum = 21
	if err = manager.validateSnapshotEligibility(ctx, candidate); err != nil {
		t.Fatalf("unchanged aggregate was refused: %v", err)
	}
	// The configured threshold must control publication, rather than a fixed
	// minimum of five. Preserve the five-account candidate above for later checks.
	stricter := makeManager(true, 6)
	below, err := stricter.BuildCandidate(ctx, "tenant-1")
	if err != nil || below.ARD.Entries[0].Ratings != nil || below.AICatalog.Entries[0].Extensions[ratings.IRI] != nil {
		t.Fatal("configured minimum leaked a smaller group", err)
	}
	store.aggregate.SharedCount = 6
	store.aggregate.SharedSum = 27
	atMinimum, err := stricter.BuildCandidate(ctx, "tenant-1")
	if err != nil {
		t.Fatal(err)
	}
	if got := atMinimum.ARD.Entries[0].Ratings; got == nil || got.Count != 6 || got.Sum != 27 || got.Average() != 4.5 {
		t.Fatalf("configured minimum omitted or changed the eligible aggregate: %+v", got)
	}
	if err = stricter.validateSnapshotEligibility(ctx, atMinimum); err != nil {
		t.Fatal(err)
	}
	// Consent withdrawal does not mutate an immutable active document. It prevents
	// activating or rolling back to a candidate with an obsolete shared summary.
	store.aggregate.SharedCount = 4
	store.aggregate.SharedSum = 16
	if err = manager.validateSnapshotEligibility(ctx, candidate); catalog.ErrorCodeOf(err) != catalog.CodePublicationNotReady {
		t.Fatalf("stale consent remained eligible: %v", err)
	}
	if candidate.ARD.Entries[0].Ratings.Count != 5 {
		t.Fatal("retained snapshot changed")
	}
	next, err := manager.BuildCandidate(ctx, "tenant-1")
	if err != nil || next.ARD.Entries[0].Ratings != nil {
		t.Fatal("future snapshot retained withdrawn consent", err)
	}
	store.err = errors.New("ratings unavailable")
	if _, err = manager.BuildCandidate(ctx, "tenant-1"); !errors.Is(err, store.err) {
		t.Fatal("publication hid sharing read failure", err)
	}
	store.err = catalog.E(catalog.CodeNotFound, "sharing choice does not exist")
	calls := store.calls
	withoutChoice, err := manager.BuildCandidate(ctx, "tenant-1")
	if err != nil || withoutChoice.ARD.Entries[0].Ratings != nil || withoutChoice.AICatalog.Entries[0].Extensions[ratings.IRI] != nil || store.calls != calls {
		t.Fatal("missing tenant opt-in queried or published opinions", err)
	}
	store.err = nil
	store.aggregateErr = errors.New("aggregate read unavailable")
	if _, err = manager.BuildCandidate(ctx, "tenant-1"); !errors.Is(err, store.aggregateErr) {
		t.Fatal("publication hid aggregate read failure", err)
	}
	store.aggregateErr = nil
	store.aggregate.SharedSum = 26 // Four shared one-to-five scores cannot total 26.
	if _, err = manager.BuildCandidate(ctx, "tenant-1"); catalog.ErrorCodeOf(err) != catalog.CodeInternal {
		t.Fatal("publication accepted an impossible aggregate", err)
	}
}
