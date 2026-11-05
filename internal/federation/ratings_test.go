package federation

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/ratings"
)

func TestPeerRatingSummaryIsOptionalAndPublisherBound(t *testing.T) {
	source := Source{Endpoint: "https://peer.example/ard.json"}
	value := ratings.Summary{Reporter: "https://peer.example", Identifier: "urn:air:peer.example:demo:agent", VersionPresent: true, Version: "1", Minimum: 1, Maximum: 5, Count: 5, Sum: 24, MeasuredAt: time.Date(2026, 10, 8, 0, 0, 0, 0, time.UTC)}
	for name, unknown := range map[string]map[string]any{"ARD": {ratings.IRI: map[string]any{"@type": "@json", "@value": value}}, "AI": {"extensions": map[string]any{ratings.IRI: value}}} {
		t.Run(name, func(t *testing.T) {
			before, _ := json.Marshal(unknown)
			raw := ExtractRatingSummary(unknown, source, value.Identifier, true, "1")
			if ratings.DecodeSummary(raw, value.Reporter, value.Identifier, true, "1") == nil {
				t.Fatalf("lost valid summary %s", raw)
			}
			after, _ := json.Marshal(unknown)
			if string(before) != string(after) {
				t.Fatal("rewrote peer evidence")
			}
		})
	}
	value.Reporter = "https://forged.example"
	if raw := ExtractRatingSummary(map[string]any{ratings.IRI: value}, source, value.Identifier, true, "1"); len(raw) != 0 {
		t.Fatal("accepted another publisher")
	}
	if raw := ExtractRatingSummary(map[string]any{ratings.IRI: map[string]any{"schemaVersion": 2}}, source, value.Identifier, true, "1"); len(raw) != 0 {
		t.Fatal("accepted future extension")
	}
}
func TestRatingAdmissionKeepsSelectedSourceAndGenerationWithoutFetching(t *testing.T) {
	manager, store, _, _, source, _ := newChangePreviewTest(t)
	target := ratings.Target{Origin: "foreign", SourceID: source.ID, PresentationGeneration: 1, Identifier: "urn:air:remote.example:demo:agent", Kind: catalog.KindAgent}
	digest := "sha256:" + strings.Repeat("a", 64)
	store.observations["rating"] = Observation{TenantID: source.TenantID, SourceID: source.ID, Identifier: target.Identifier, Kind: target.Kind, MediaType: "application/a2a-agent-card+json", NormalizedEntryDigest: digest, Freshness: FreshCurrent, Completeness: CompleteEntry, Compatibility: Compatibility{Structural: "valid", Support: "supported"}}
	ctx := auditEvidenceContext()
	got, err := manager.AdmitRating(ctx, ratings.Admission{Target: target})
	if err != nil || got.EntryDigest != digest || got.SourceRevision != 1 {
		t.Fatalf("admission %+v %v", got, err)
	}
	for name, change := range map[string]func(*ratings.Target){"generation": func(v *ratings.Target) { v.PresentationGeneration = 2 }, "peer": func(v *ratings.Target) { v.SourceID = "00000000-0000-7000-8000-000000000021" }, "version presence": func(v *ratings.Target) { v.VersionPresent = true }, "kind": func(v *ratings.Target) { v.Kind = catalog.KindSkill }} {
		t.Run(name, func(t *testing.T) {
			bad := target
			change(&bad)
			if _, err := manager.AdmitRating(ctx, ratings.Admission{Target: bad}); err == nil {
				t.Fatal("foreign target aliased another vote")
			}
		})
	}
	source.Scope.ExcludedIdentifiers = []string{target.Identifier}
	store.sources[source.ID] = source
	if _, err := manager.AdmitRating(ctx, ratings.Admission{Target: target}); catalog.ErrorCodeOf(err) != catalog.CodePreconditionFailed {
		t.Fatalf("excluded target remained rateable: %v", err)
	}
	source.Scope.ExcludedIdentifiers = nil
	store.sources[source.ID] = source
	if _, err := manager.AdmitRating(ctx, ratings.Admission{Target: target}); err != nil {
		t.Fatalf("cleared target did not recover: %v", err)
	}
	// A selected source's same-authority outage retains indexed admission. Removal
	// hides its target; replacement generation cannot inherit the old opinion.
	source.Status = SourceError
	store.sources[source.ID] = source
	if _, err = manager.AdmitRating(ctx, ratings.Admission{Target: target}); err != nil {
		t.Fatal(err)
	}
	source.Status = SourceRemoved
	store.sources[source.ID] = source
	if _, err = manager.AdmitRating(ctx, ratings.Admission{Target: target}); err == nil {
		t.Fatal("withdrawn source remained rateable")
	}
}

type ratingAdoptionReader struct {
	candidates []catalog.AdoptionCandidate
	err        error
}

func (r ratingAdoptionReader) HasPublishedIdentity(context.Context, string, string, string) (bool, error) {
	return false, nil
}
func (r ratingAdoptionReader) FindAdoptions(context.Context, string, []string) ([]catalog.AdoptionCandidate, error) {
	return r.candidates, r.err
}
func TestSelectedRatingSignalsKeepPublisherAndHistoricalImportAuthority(t *testing.T) {
	manager, store, _, _, source, _ := newChangePreviewTest(t)
	ctx := auditEvidenceContext()
	target := ratings.Target{Origin: "foreign", SourceID: source.ID, PresentationGeneration: 1, Identifier: "urn:air:remote.example:demo:agent", Kind: catalog.KindAgent}
	digest := "sha256:" + strings.Repeat("a", 64)
	summary := ratings.Summary{Reporter: "https://" + mustRatingHost(t, source.Endpoint), Identifier: target.Identifier, Minimum: 1, Maximum: 5, Count: 5, Sum: 21, MeasuredAt: time.Date(2026, 10, 8, 0, 0, 0, 0, time.UTC)}
	raw, err := json.Marshal(summary)
	if err != nil {
		t.Fatal(err)
	}
	observation := Observation{ID: "observed", TenantID: source.TenantID, SourceID: source.ID, Identifier: target.Identifier, Kind: target.Kind, NormalizedEntryDigest: digest, Freshness: FreshCurrent, Completeness: CompleteEntry, Compatibility: Compatibility{Structural: "valid", Support: "supported"}, RatingsMetadata: raw}
	store.observations[observation.ID] = observation
	candidate := catalog.AdoptionCandidate{ResourceID: "imported", Identifier: target.Identifier, Kind: target.Kind, Provenance: catalog.AdoptionProvenance{SourceID: source.ID, SourceRevision: source.Revision, ObservationID: observation.ID, NormalizedEntryDigest: digest}}
	store.BindLocalCatalog(ratingAdoptionReader{candidates: []catalog.AdoptionCandidate{candidate}})
	item := catalog.CatalogSearchResult{Identifier: target.Identifier, Kind: target.Kind, Indexed: &catalog.IndexedSearchEvidence{PresentationSourceID: source.ID, PresentationGeneration: 1, NormalizedEntryDigest: digest}}
	verify := func() catalog.CatalogSearchResult {
		t.Helper()
		items, err := manager.ResolveRatingSignals(ctx, []catalog.CatalogSearchResult{item})
		if err != nil || len(items) != 1 {
			t.Fatal(items, err)
		}
		return items[0]
	}
	got := verify()
	if got.PeerRatings == nil || got.PeerRatings.Count != 5 || got.PeerRatings.Average != 4.2 || got.ImportedHere == nil || got.ImportedHere.ResourceID != "imported" || got.ImportedHere.ChangedEntry {
		t.Fatal("lost selected signals", got)
	}
	candidate.Provenance.NormalizedEntryDigest = "sha256:" + strings.Repeat("b", 64)
	store.BindLocalCatalog(ratingAdoptionReader{candidates: []catalog.AdoptionCandidate{candidate}})
	got = verify()
	if got.ImportedHere == nil || !got.ImportedHere.ChangedEntry {
		t.Fatal("source change hidden")
	}
	other := candidate
	other.ResourceID = "ambiguous"
	store.BindLocalCatalog(ratingAdoptionReader{candidates: []catalog.AdoptionCandidate{candidate, other}})
	if verify().ImportedHere != nil {
		t.Fatal("ambiguous import disclosed")
	}
	item.VersionPresent = true
	if got = verify(); got.PeerRatings != nil || got.ImportedHere != nil {
		t.Fatal("absent and advertised empty versions aliased")
	}
	item.VersionPresent = false
	store.observations[observation.ID] = Observation{ID: observation.ID, TenantID: source.TenantID, SourceID: source.ID, Identifier: target.Identifier, Kind: target.Kind, NormalizedEntryDigest: digest, Freshness: FreshCurrent, Completeness: CompleteEntry, Compatibility: Compatibility{Structural: "valid", Support: "supported"}, RatingsMetadata: json.RawMessage(`{"count":"bad"}`)}
	if verify().PeerRatings != nil {
		t.Fatal("malformed optional claim rendered")
	}
	store.BindLocalCatalog(ratingAdoptionReader{err: errors.New("read refused")})
	if _, err = manager.ResolveRatingSignals(ctx, []catalog.CatalogSearchResult{item}); err == nil {
		t.Fatal("adoption read failure ignored")
	}
	if _, err = manager.ResolveRatingSignals(ctx, make([]catalog.CatalogSearchResult, 101)); err == nil {
		t.Fatal("unbounded selected page")
	}
}
func mustRatingHost(t *testing.T, endpoint string) string {
	t.Helper()
	u, err := url.Parse(endpoint)
	if err != nil {
		t.Fatal(err)
	}
	return u.Host
}

func TestLiveRatingSignalsUseExactPublisherClaimWithoutCachedSubstitution(t *testing.T) {
	manager, store, _, _, source, _ := newChangePreviewTest(t)
	ctx := auditEvidenceContext()
	claim := ratings.Summary{Reporter: "https://" + mustRatingHost(t, source.Endpoint), Identifier: "urn:air:remote.example:demo:agent", Minimum: 1, Maximum: 5, Count: 5, Sum: 18, MeasuredAt: time.Date(2026, 10, 8, 0, 0, 0, 0, time.UTC)}
	raw, err := json.Marshal(map[string]any{ratings.IRI: claim})
	if err != nil {
		t.Fatal(err)
	}
	item := catalog.CatalogSearchResult{Identifier: claim.Identifier, Kind: catalog.KindAgent, Live: &catalog.LiveSearchEvidence{SourceID: source.ID, PresentationGeneration: 1, SourceRevision: source.Revision, Entry: raw}}
	got, err := manager.ResolveRatingSignals(ctx, []catalog.CatalogSearchResult{item})
	if err != nil || len(got) != 1 || got[0].PeerRatings == nil || got[0].PeerRatings.Average != 3.6 {
		t.Fatal(got, err)
	}
	for _, raw := range []json.RawMessage{json.RawMessage(`{"broken":`), json.RawMessage(`{"identifier":"urn:air:remote.example:demo:agent"}`)} {
		item.Live.Entry = raw
		got, err = manager.ResolveRatingSignals(ctx, []catalog.CatalogSearchResult{item})
		if err != nil || got[0].PeerRatings != nil {
			t.Fatal("malformed optional value rejected resource", got, err)
		}
	}
	if got, err = manager.ResolveRatingSignals(ctx, []catalog.CatalogSearchResult{{Identifier: claim.Identifier, Kind: catalog.KindAgent}}); err != nil || len(got) != 1 {
		t.Fatal(got, err)
	}
	if _, err = manager.ResolveRatingSignals(t.Context(), []catalog.CatalogSearchResult{item}); err == nil {
		t.Fatal("missing policy context")
	}
	source.Status = SourceRemoved
	store.sources[source.ID] = source
	item.Live.Entry = raw
	got, err = manager.ResolveRatingSignals(ctx, []catalog.CatalogSearchResult{item})
	if err != nil || got[0].PeerRatings != nil {
		t.Fatal("removed source supplied claim", got, err)
	}
}
