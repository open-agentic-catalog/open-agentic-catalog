package federation

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/fetcher"
)

func TestSourceExclusionValidationAndIdentity(t *testing.T) {
	t.Parallel()
	identifier := "urn:air:example.com:Team:Agent"
	for _, tc := range []struct {
		name   string
		values []string
		valid  bool
	}{
		{"absent", nil, true}, {"empty", []string{}, true},
		{"canonical", []string{identifier}, true}, {"opaque", []string{"legacy:item"}, true},
		{"blank", []string{""}, false}, {"space", []string{" " + identifier}, false},
		{"NUL", []string{"a\x00b"}, false}, {"UTF8", []string{"\xff"}, false},
		{"character boundary", []string{strings.Repeat("é", 512)}, true},
		{"excess characters", []string{strings.Repeat("é", 513)}, false},
		{"duplicate", []string{identifier, identifier}, false},
		{"canonical duplicate", []string{identifier, "URN:AIR:EXAMPLE.COM:Team:Agent"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			scope := Scope{Kinds: []catalog.Kind{catalog.KindAgent}, ExcludedIdentifiers: tc.values}
			if err := validateScope(scope); (err == nil) != tc.valid {
				t.Fatalf("valid=%t error=%v", tc.valid, err)
			}
		})
	}
	bounded := make([]string, 0, 1001)
	for i := range 1000 {
		bounded = append(bounded, fmt.Sprintf("legacy:%04d", i))
	}
	if err := validateExcludedIdentifiers(bounded); err != nil {
		t.Fatal(err)
	}
	if err := validateExcludedIdentifiers(append(bounded, "overflow")); err == nil {
		t.Fatal("accepted overflow")
	}
	if err := validateScope(Scope{ExcludedIdentifiers: []string{identifier}}); err == nil {
		t.Fatal("exclusion-only scope accepted")
	}
	normalized := normalizeScope(Scope{Kinds: []catalog.Kind{catalog.KindAgent}, ExcludedIdentifiers: []string{"URN:AIR:EXAMPLE.COM:Team:Agent", "legacy:Z"}})
	if !slices.Equal(normalized.ExcludedIdentifiers, []string{"legacy:Z", identifier}) {
		t.Fatal(normalized)
	}
	for _, wire := range []string{identifier, "URN:AIR:EXAMPLE.COM:Team:Agent"} {
		if !scopeExcludesIdentifier(normalized, wire) {
			t.Fatalf("alias escaped: %s", wire)
		}
	}
	if scopeExcludesIdentifier(normalized, "urn:air:example.com:team:Agent") || scopeExcludesIdentifier(normalized, "legacy:z") {
		t.Fatal("case-sensitive name changed")
	}
	before := Source{Scope: Scope{Kinds: []catalog.Kind{catalog.KindAgent}}}
	after := before
	after.Scope.ExcludedIdentifiers = []string{}
	a, _ := sourceConfigurationDigest(before)
	b, _ := sourceConfigurationDigest(after)
	if a != b {
		t.Fatal("empty list changes digest")
	}
	for _, media := range []string{"application/a2a-agent-card+json", "application/mcp-server-card+json", "application/agent-skills+json", catalog.PluginMediaZIP} {
		scope := Scope{Identifiers: []string{identifier}, ExcludedIdentifiers: []string{identifier}}
		for _, version := range []string{"", "1", "2"} {
			if scopeMatches(scope, sourceEntry{Identifier: identifier, Type: media, Version: version}) {
				t.Fatal("allowlist defeated exclusion")
			}
		}
	}
}

func TestSourceExclusionsImmediatelyFilterBothSearchesAndPreserveEvidence(t *testing.T) {
	t.Parallel()
	store := NewMemoryStore()
	tenant := catalogSearchTestTenantID
	const identifier = "urn:air:remote.example:default:alpha"
	for i, sourceID := range []string{"a", "b"} {
		store.sources[sourceID] = Source{ID: sourceID, TenantID: tenant, Revision: 1, Status: SourceActive, RetrievalMode: RetrievalIndexed, Scope: Scope{Kinds: []catalog.Kind{catalog.KindAgent}}}
		obs := indexedSearchObservation(sourceID, identifier, "Alpha agent", "digest")
		obs.ID = fmt.Sprintf("observation-%d", i)
		obs.RawEntry = json.RawMessage(`{"retained":true}`)
		store.observations[obs.ID] = obs
	}
	original := store.observations["observation-0"]
	first, err := store.SearchIndexedCatalog(t.Context(), tenant, catalog.FederatedCatalogSearchRequest{Limit: 10})
	if err != nil || first.IndexedTotal != 1 || first.Indexed[0].Indexed.SourceCount != 2 {
		t.Fatalf("control=%+v %v", first, err)
	}
	changed := store.sources["a"]
	changed.Revision++
	changed.Scope.ExcludedIdentifiers = []string{identifier}
	store.sources["a"] = changed
	page, err := store.SearchIndexedCatalog(t.Context(), tenant, catalog.FederatedCatalogSearchRequest{Limit: 10})
	if err != nil || page.IndexedTotal != 1 || !slices.Equal(page.Indexed[0].Indexed.SourceIDs, []string{"b"}) {
		t.Fatalf("other source lost: %+v %v", page, err)
	}
	legacy, err := store.SearchObservationPage(t.Context(), tenant, ObservationSearchRequest{Limit: 10})
	if err != nil || len(legacy) != 1 || legacy[0].Source != "b" {
		t.Fatalf("legacy=%+v %v", legacy, err)
	}
	if _, err = store.SearchIndexedCatalog(t.Context(), tenant, catalog.FederatedCatalogSearchRequest{Limit: 10, ExpectedGeneration: first.Generation}); catalog.ErrorCodeOf(err) != catalog.CodeInvalidArgument {
		t.Fatal("stale cursor accepted", err)
	}
	if !reflect.DeepEqual(original, store.observations[original.ID]) {
		t.Fatal("exclusion rewrote retained observation")
	}
	changed.Scope.ExcludedIdentifiers = nil
	changed.Revision++
	store.sources["a"] = changed
	legacy, err = store.SearchObservationPage(t.Context(), tenant, ObservationSearchRequest{Limit: 10})
	if err != nil || len(legacy) != 2 {
		t.Fatal("cleared current evidence not restored", legacy, err)
	}
	changed.Scope.Kinds = []catalog.Kind{catalog.KindSkill}
	changed.Revision++
	store.sources["a"] = changed
	legacy, err = store.SearchObservationPage(t.Context(), tenant, ObservationSearchRequest{Limit: 10})
	if err != nil || len(legacy) != 1 {
		t.Fatal("positive scope bypass", legacy, err)
	}
	if other, err := store.SearchObservationPage(t.Context(), "other", ObservationSearchRequest{Limit: 10}); err != nil || len(other) != 0 {
		t.Fatal("cross-tenant result", other, err)
	}
}

func TestSourceExclusionsLiveOmissionIsHealthyAndDoesNotLeakPolicy(t *testing.T) {
	manager, store, outbound, sources := newLivePagingFixture(t, 3, "a")
	source := sources[0]
	source.Scope = normalizeScope(Scope{Kinds: []catalog.Kind{catalog.KindAgent}, ExcludedIdentifiers: []string{"URN:AIR:REMOTE.EXAMPLE:agent:alpha"}})
	store.sources[source.ID] = source
	outbound.respond = func(_ context.Context, request fetcher.Request) (fetcher.Result, error) {
		if strings.Contains(string(request.Body), "excludedIdentifiers") || strings.Contains(string(request.Body), "agent:alpha") {
			t.Fatal("private policy sent upstream")
		}
		if livePagingToken(t, request) == "" {
			return livePagingPage(t, "next", livePagingEntry("alpha", 99)), nil
		}
		return livePagingPage(t, "", livePagingEntry("beta", 80)), nil
	}
	result, err := manager.LiveSearch(livePagingContext(t), LiveSearchRequest{Text: "Useful agent", PageSize: 2, Administrative: true, Reason: "Check local policy"})
	if err != nil || result.Partial || len(result.Results) != 1 || result.Results[0].Identifier != "urn:air:remote.example:agent:beta" || len(outbound.requests) != 2 {
		t.Fatalf("result=%+v calls=%d error=%v", result, len(outbound.requests), err)
	}
	source.Scope.ExcludedIdentifiers = append(source.Scope.ExcludedIdentifiers, "urn:air:remote.example:agent:beta")
	source.Revision++
	store.sources[source.ID] = source
	result, err = manager.LiveSearch(livePagingContext(t), LiveSearchRequest{Text: "Useful agent", PageSize: 2, Administrative: true, Reason: "Check empty selection"})
	if err != nil || result.Partial || len(result.Results) != 0 {
		t.Fatalf("empty selection failed: %+v %v", result, err)
	}
}

func TestSourceExclusionPreviewBindsPolicyAndPreservesCurrentEvidence(t *testing.T) {
	manager, store, _, _, source, _ := newChangePreviewTest(t)
	ctx := auditEvidenceContext()
	obs := indexedSearchObservation(source.ID, "urn:air:remote.example:default:alpha", "Alpha", "digest")
	obs.TenantID = source.TenantID
	obs.ID = "00000000-0000-7000-8000-000000000031"
	obs.RawEntry = json.RawMessage(`{"identifier":"urn:air:remote.example:default:alpha","type":"application/a2a-agent-card+json"}`)
	store.observations[obs.ID] = obs
	scope := Scope{Kinds: []catalog.Kind{catalog.KindAgent}, ExcludedIdentifiers: []string{obs.Identifier}}
	patch := PatchInput{Intent: ChangeUpdate, Scope: &scope, Reason: "Exclude selected resource"}
	preview, err := manager.PreviewChange(ctx, source.ID, patch)
	if err != nil || preview.CurrentMatched != 1 || preview.ProposedMatched != 0 || preview.ExcludedObservations != 1 {
		t.Fatalf("preview=%+v %v", preview, err)
	}
	patch.ChangePreviewDigest = preview.Digest
	alteredScope := scope
	alteredScope.ExcludedIdentifiers = []string{"urn:air:remote.example:default:other"}
	alteredPatch := patch
	alteredPatch.Scope = &alteredScope
	if _, err := manager.UpdateSource(ctx, source.ID, alteredPatch, source.Revision); catalog.ErrorCodeOf(err) != catalog.CodePreconditionFailed {
		t.Fatal("preview admitted a different exclusion list", err)
	}
	if current, err := store.GetSource(ctx, source.TenantID, source.ID); err != nil || current.Revision != source.Revision || len(current.Scope.ExcludedIdentifiers) != 0 {
		t.Fatalf("rejected preview changed source policy: %+v %v", current, err)
	}
	changed, err := manager.UpdateSource(ctx, source.ID, patch, source.Revision)
	if err != nil || changed.Revision != source.Revision+1 || EffectivePresentationGeneration(changed) != EffectivePresentationGeneration(source) {
		t.Fatalf("change=%+v %v", changed, err)
	}
	if !reflect.DeepEqual(store.observations[obs.ID], obs) {
		t.Fatal("preview/update changed retained observation")
	}
}

func TestSourceRevisionCommitGuardRejectsStaleTenantAndRemoval(t *testing.T) {
	t.Parallel()
	store := NewMemoryStore()
	store.sources["source"] = Source{ID: "source", TenantID: "tenant", Revision: 2, Status: SourceActive}
	for _, tc := range []struct {
		tenant, source string
		revision       int64
		allowed        bool
	}{
		{"tenant", "source", 2, true}, {"tenant", "source", 1, false}, {"other", "source", 2, false}, {"tenant", "absent", 2, false},
	} {
		called := false
		err := store.withSourceRevision(t.Context(), tc.tenant, tc.source, tc.revision, func(context.Context) error { called = true; return nil })
		if called != tc.allowed || (err == nil) != tc.allowed {
			t.Fatalf("%+v called=%t err=%v", tc, called, err)
		}
	}
	source := store.sources["source"]
	source.Status = SourceRemoved
	store.sources["source"] = source
	if err := store.withSourceRevision(t.Context(), "tenant", "source", 2, func(context.Context) error { t.Fatal("removed source committed"); return nil }); catalog.ErrorCodeOf(err) != catalog.CodePreconditionFailed {
		t.Fatal(err)
	}
}
