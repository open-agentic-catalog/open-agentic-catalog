package federation

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/fetcher"
)

func TestFederationIdentityCharacterLimits(t *testing.T) {
	t.Parallel()
	for _, character := range []string{"x", "é", "🙂"} {
		for _, count := range []int{127, 128, 129} {
			t.Run(fmt.Sprintf("version/%s/%d", character, count), func(t *testing.T) {
				version := strings.Repeat(character, count)
				valid := count <= 128
				entry := sourceEntry{Identifier: "urn:air:remote.example:agents:unicode", DisplayName: "Useful agent", Type: "application/a2a-agent-card+json", Data: json.RawMessage(`{"name":"Unicode"}`), Version: version, VersionPresent: true}
				if _, structural, findings := assessSourceEntryStructure(entry, ardProfileID); (structural == "valid") != valid {
					t.Fatalf("entry admission: %s %v", structural, findings)
				}
				if err := validateScopeStrings("versions", []string{version}, 128); (err == nil) != valid {
					t.Fatalf("scope admission: %v", err)
				}
				target := ArtworkTarget{SourceID: "00000000-0000-7000-8000-000000000001", PresentationGeneration: 1, Identifier: entry.Identifier, Kind: catalog.KindAgent, VersionPresent: true, Version: version}
				if err := ValidateArtworkTarget(target); (err == nil) != valid {
					t.Fatalf("artwork target: %v", err)
				}
				manager, _, _, _, source, _ := newChangePreviewTest(t)
				entry.RetrievalDigest = "sha256:" + strings.Repeat("a", 64)
				entry.EntryLocation = "/entries/0"
				now := manager.clock.Now()
				observation, err := manager.observation(source.TenantID, source, "snapshot", entry, 0, now)
				if err != nil || valid != (observation.NormalizedEntryDigest != "") || observation.Version != version {
					t.Fatalf("normalized version evidence: %#v %v", observation, err)
				}
				if valid {
					snapshot := Snapshot{ID: "snapshot", TenantID: source.TenantID, SourceID: source.ID, SourceRevision: source.Revision, State: SnapshotComplete, EnumerationComplete: true, WithdrawalAuthority: true, MemberCount: 1, StartedAt: now, CompletedAt: &now}
					if err := ValidateSnapshotCommit(snapshot, []Observation{observation}); err != nil {
						t.Fatalf("valid Unicode observation cannot be committed: %v", err)
					}
				}
				reader := &groupFailureStore{t: t, member: Observation{TenantID: source.TenantID, Identifier: entry.Identifier, Version: version, VersionPresent: true}, fail: "missing"}
				manager.store = reader
				_, err = manager.ObservationGroup(auditEvidenceContext(), entry.Identifier, version, true)
				want := catalog.CodeInvalidArgument
				if valid {
					want = catalog.CodeNotFound
				}
				if catalog.ErrorCodeOf(err) != want || valid != (len(reader.calls) == 1) {
					t.Fatalf("exact version lookup: %v, calls=%v", err, reader.calls)
				}
			})
		}
		for _, count := range []int{511, 512, 513} {
			t.Run(fmt.Sprintf("identifier/%s/%d", character, count), func(t *testing.T) {
				value := strings.Repeat(character, count)
				if err := validateExcludedIdentifiers([]string{value}); (err == nil) != (count <= 512) {
					t.Fatalf("exclusion: %v", err)
				}
				if err := validateScopeStrings("identifiers", []string{value}, 512); (err == nil) != (count <= 512) {
					t.Fatalf("scope: %v", err)
				}
			})
		}
	}
	for _, value := range []string{"bad\x00text", "bad\xfftext"} {
		if validDiscoveryVersion(value) || validateScopeStrings("versions", []string{value}, 128) == nil {
			t.Fatalf("invalid text accepted: %q", value)
		}
	}
}

func TestLiveSearchUnicodeVersionsAcrossPeerPages(t *testing.T) {
	t.Parallel()
	manager, _, outbound, _ := newLivePagingFixture(t, 2, "unicode")
	version := strings.Repeat("🙂", 128)
	entry := func(name, label string) string {
		raw, err := json.Marshal(map[string]any{"identifier": "urn:air:remote.example:agents:" + name, "displayName": "Useful agent " + name, "type": "application/a2a-agent-card+json", "version": label, "score": 90, "source": "https://index.example/search"})
		if err != nil {
			t.Fatal(err)
		}
		return string(raw)
	}
	outbound.respond = func(_ context.Context, request fetcher.Request) (fetcher.Result, error) {
		if livePagingToken(t, request) == "" {
			return livePagingPage(t, "next", entry("first", version)), nil
		}
		return livePagingPage(t, "", entry("second", version), entry("oversized", version+"x")), nil
	}
	response, err := manager.LiveSearch(livePagingContext(t), LiveSearchRequest{Text: "agent", PageSize: 2, Administrative: true, Reason: "Check Unicode versions across peer pages"})
	if err != nil || len(response.Results) != 2 || len(outbound.requests) != 2 {
		t.Fatalf("paged live search: %#v, %v, requests=%d", response, err, len(outbound.requests))
	}
	for _, result := range response.Results {
		if result.Version != version || !result.VersionPresent || strings.HasSuffix(result.Identifier, ":oversized") {
			t.Fatal("live projection altered or admitted an oversized version")
		}
	}
}
