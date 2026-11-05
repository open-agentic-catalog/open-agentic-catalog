package federation

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/artwork"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/audit"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/test/identityfixture"
)

func TestPublisherArtworkExtractionAcrossProfilesPreservesOptionalEvidence(t *testing.T) {
	asset := artwork.Asset{AssetInput: artwork.AssetInput{URL: "https://images.example/publisher.png", Alt: "Publisher icon", Rights: &artwork.Rights{Basis: "owned", Attribution: "Example publisher"}}, Digest: "sha256:" + strings.Repeat("a", 64), MediaType: "image/png", Size: 80, Width: 16, Height: 16, RightsAssessment: artwork.NotAssessed}
	descriptor := artwork.Descriptor{SchemaVersion: 1, Icon: &asset, Screenshots: []artwork.Asset{asset}}
	if err := artwork.ValidateDescriptor(descriptor, artwork.HardLimits()); err != nil {
		t.Fatal(err)
	}
	for name, unknown := range map[string]map[string]any{"ARD": {artwork.IRI: map[string]any{"@type": "@json", "@value": descriptor}}, "AI": {"extensions": map[string]any{artwork.IRI: descriptor}}} {
		t.Run(name, func(t *testing.T) {
			before, err := json.Marshal(unknown)
			if err != nil {
				t.Fatal(err)
			}
			raw := ExtractArtworkCandidates(unknown, nil)
			var got ArtworkCandidates
			if err = json.Unmarshal(raw, &got); err != nil || got.Publisher == nil || got.Publisher.Icon == nil || got.Publisher.Icon.Digest != asset.Digest || len(got.Publisher.Screenshots) != 1 {
				t.Fatalf("publisher projection: %s %v", raw, err)
			}
			if err = ValidateArtworkMetadata(raw); err != nil {
				t.Fatal(err)
			}
			after, err := json.Marshal(unknown)
			if err != nil || string(before) != string(after) {
				t.Fatalf("remote evidence changed: %v", err)
			}
		})
	}
	for name, value := range map[string]any{"future": map[string]any{"schemaVersion": 2, "future": true}, "malformed": map[string]any{"schemaVersion": 1, "icon": map[string]any{"url": "http://unsafe.example/icon.svg"}}, "null": nil} {
		t.Run(name, func(t *testing.T) {
			unknown := map[string]any{"extensions": map[string]any{artwork.IRI: value}}
			raw := ExtractArtworkCandidates(unknown, []byte(`{"iconUrl":"https://images.example/native.png"}`))
			var got ArtworkCandidates
			if err := json.Unmarshal(raw, &got); err != nil || got.Publisher != nil || got.NativeIcon == nil {
				t.Fatalf("optional malformed artwork affected native record: %s %v", raw, err)
			}
		})
	}
}

type artworkUnavailableStore struct{ Store }

type artworkFailedBatchStore struct{ Store }

func (artworkFailedBatchStore) ArtworkReadBatch(context.Context, string, []ArtworkReadRequest) (map[ArtworkTarget]ArtworkReadContext, error) {
	return nil, errors.New("artwork batch unavailable")
}

func TestArtworkDependencyFailuresDoNotAdmitOrMutate(t *testing.T) {
	manager, store, _, _, source, _ := newChangePreviewTest(t)
	target := ArtworkTarget{SourceID: source.ID, PresentationGeneration: 1, Identifier: "urn:air:remote.example:agents:example", Kind: catalog.KindAgent}
	ctx := auditEvidenceContext()
	manager.store = artworkUnavailableStore{Store: store}
	if _, err := manager.GetArtworkOverride(ctx, target); catalog.ErrorCodeOf(err) != catalog.CodeDependencyUnavailable {
		t.Fatalf("missing head store: %v", err)
	}
	if _, err := manager.ArtworkOverrideHistory(ctx, target, "", 1); catalog.ErrorCodeOf(err) != catalog.CodeDependencyUnavailable {
		t.Fatalf("missing history store: %v", err)
	}
	if _, err := manager.AdminArtworkSourceContext(ctx, target); catalog.ErrorCodeOf(err) != catalog.CodeDependencyUnavailable {
		t.Fatalf("missing admission store: %v", err)
	}
	if err := manager.ValidateLiveArtworkOverride(ctx, target, ""); catalog.ErrorCodeOf(err) != catalog.CodeDependencyUnavailable {
		t.Fatalf("missing receipt store: %v", err)
	}
	if result, err := manager.ResolveArtwork(ctx, []catalog.CatalogSearchResult{{Identifier: target.Identifier}}); err != nil || len(result) != 1 {
		t.Fatalf("optional unsupported decoration: %#v %v", result, err)
	}
	manager.store = artworkFailedBatchStore{Store: store}
	if _, err := manager.AdminArtworkSourceContext(ctx, target); err == nil {
		t.Fatal("failed administrative batch admitted")
	}
	if _, err := manager.ResolveArtwork(ctx, nil); err == nil {
		t.Fatal("failed batch swallowed")
	}
}

func TestArtworkFinalIndexedPageUsesCurrentEvidenceAndSlotChoices(t *testing.T) {
	manager, store, _, _, source, _ := newChangePreviewTest(t)
	source.ArtworkEnabled = true
	store.sources[source.ID] = source
	identifier := "urn:air:remote.example:agents:example"
	store.observations["current"] = Observation{TenantID: source.TenantID, SourceID: source.ID, Identifier: identifier, Kind: catalog.KindAgent, MediaType: "application/a2a-agent-card+json", NormalizedEntryDigest: "selected", Freshness: FreshCurrent, Completeness: CompleteEntry, Compatibility: Compatibility{Structural: "valid", Support: "supported"}, ArtworkMetadata: ExtractArtworkCandidates(nil, []byte(`{"iconUrl":"https://images.example/current.png"}`))}
	items := []catalog.CatalogSearchResult{{Type: catalog.SearchResultIndexed, Identifier: identifier, Kind: catalog.KindAgent, Indexed: &catalog.IndexedSearchEvidence{PresentationSourceID: source.ID, PresentationGeneration: 1, NormalizedEntryDigest: "selected"}}, {Identifier: identifier}, {Identifier: "urn:air:remote.example:agents:other", Kind: catalog.KindAgent, Indexed: &catalog.IndexedSearchEvidence{PresentationSourceID: source.ID, PresentationGeneration: 1, NormalizedEntryDigest: "different"}}}
	result, err := manager.ResolveArtwork(auditEvidenceContext(), items)
	if err != nil || result[0].Artwork == nil || result[0].Artwork.IconSource != "native" || !result[0].Artwork.MutableIcon || result[1].Artwork != nil || result[2].Artwork != nil || items[0].Artwork != nil {
		t.Fatalf("selected evidence decoration: %#v %v", result, err)
	}
	screenshot := artwork.Asset{AssetInput: artwork.AssetInput{URL: "https://images.example/screenshot.png", Alt: "Screenshot"}}
	read := ArtworkReadContext{Source: source, Candidates: ArtworkCandidates{Publisher: &artwork.Descriptor{SchemaVersion: 1, Screenshots: []artwork.Asset{screenshot}}}, Override: &ArtworkOverride{Icon: ArtworkIconSelection{Mode: ArtworkInherit}, Screenshots: ArtworkScreenshotSelection{Mode: ArtworkReplace, Assets: []artwork.Asset{screenshot}}}}
	if got := resolveArtworkPresentation(read); got == nil || got.ScreenshotsSource != "local_override" || len(got.Descriptor.Screenshots) != 1 {
		t.Fatalf("replacement screenshot: %#v", got)
	}
	read.Override.Screenshots = ArtworkScreenshotSelection{Mode: ArtworkSuppress}
	if got := resolveArtworkPresentation(read); got == nil || got.ScreenshotsSource != "local_override" || len(got.Descriptor.Screenshots) != 0 {
		t.Fatalf("suppressed screenshot: %#v", got)
	}
}

func TestArtworkStoredIntegrityAndAuditBindingsRejectTampering(t *testing.T) {
	manager, store, _, evidence, source, _ := newChangePreviewTest(t)
	target := ArtworkTarget{SourceID: source.ID, PresentationGeneration: 1, Identifier: "urn:air:remote.example:agents:example", Kind: catalog.KindAgent}
	input := ArtworkOverrideInput{SourceRevision: source.Revision, Icon: ArtworkIconSelection{Mode: ArtworkSuppress}, Screenshots: ArtworkScreenshotSelection{Mode: ArtworkInherit}, Reason: "Hide source icon"}
	head, err := manager.SetArtworkOverride(auditEvidenceContext(), target, input, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*ArtworkOverride){func(v *ArtworkOverride) { v.Digest = "bad" }, func(v *ArtworkOverride) { v.Target.SourceID = "bad"; v.Digest = artworkOverrideDigest(*v) }, func(v *ArtworkOverride) { v.Icon.Mode = "bad"; v.Digest = artworkOverrideDigest(*v) }} {
		candidate := head
		mutate(&candidate)
		if err = ValidateArtworkOverrideIntegrity(candidate); catalog.ErrorCodeOf(err) != catalog.CodeDependencyUnavailable {
			t.Fatalf("tampered integrity: %v", err)
		}
	}
	if _, err = store.SaveArtworkOverride(t.Context(), ArtworkOverride{}, 0, 1, MutationEvidence{}); err == nil {
		t.Fatal("invalid stored value admitted")
	}
	validEvidence := MutationEvidence{Audit: evidence.audit[0]}
	if err = ValidateArtworkMutationEvidence(head, "", validEvidence); err != nil {
		t.Fatal(err)
	}
	if err = ValidateArtworkMutationEvidence(head, "different-before", validEvidence); err == nil {
		t.Fatal("wrong before digest admitted")
	}
	altered := validEvidence
	altered.Audit.Details = map[string]any{"facts": map[string]any{"sourceId": "different"}}
	if err = ValidateArtworkMutationEvidence(head, "", altered); err == nil {
		t.Fatal("wrong transition facts admitted")
	}
	altered = validEvidence
	altered.Audit.Action = audit.ActionSourceRemoved
	if err = ValidateArtworkMutationEvidence(head, "", altered); err == nil {
		t.Fatal("wrong audit action admitted")
	}
	cloned := cloneArtworkOverride(ArtworkOverride{Icon: ArtworkIconSelection{Asset: &artwork.Asset{AssetInput: artwork.AssetInput{Rights: &artwork.Rights{Basis: "owned"}}}}, Screenshots: ArtworkScreenshotSelection{Assets: []artwork.Asset{{AssetInput: artwork.AssetInput{Rights: &artwork.Rights{Basis: "owned"}}}}}})
	original := cloneArtworkOverride(cloned)
	cloned.Icon.Asset.Rights.Basis = "changed"
	cloned.Screenshots.Assets[0].Rights.Basis = "changed"
	if original.Icon.Asset.Rights.Basis != "owned" || original.Screenshots.Assets[0].Rights.Basis != "owned" {
		t.Fatal("returned asset mutated retained rights")
	}
}

func TestArtworkOverrideAtomicHistoryAndSourceFence(t *testing.T) {
	manager, store, _, evidence, source, _ := newChangePreviewTest(t)
	target := ArtworkTarget{SourceID: source.ID, PresentationGeneration: 1, Identifier: "urn:air:remote.example:agents:example", Kind: catalog.KindAgent}
	input := ArtworkOverrideInput{SourceRevision: source.Revision, Icon: ArtworkIconSelection{Mode: ArtworkSuppress}, Screenshots: ArtworkScreenshotSelection{Mode: ArtworkInherit}, Reason: "Hide remote artwork"}
	first, err := manager.SetArtworkOverride(auditEvidenceContext(), target, input, 0)
	if err != nil {
		t.Fatal(err)
	}
	if first.Revision != 1 || len(evidence.audit) != 1 || len(evidence.outbox) != 0 {
		t.Fatalf("wrong atomic evidence: %#v", first)
	}
	if err = ValidateArtworkOverrideIntegrity(first); err != nil {
		t.Fatal(err)
	}
	if _, err = manager.SetArtworkOverride(auditEvidenceContext(), target, input, 0); catalog.ErrorCodeOf(err) != catalog.CodePreconditionFailed {
		t.Fatalf("stale head: %v", err)
	}
	evidence.err = errors.New("audit unavailable")
	input.Icon.Mode = ArtworkInherit
	if _, err = manager.SetArtworkOverride(auditEvidenceContext(), target, input, 1); err == nil {
		t.Fatal("failed audit committed")
	}
	evidence.err = nil
	head, err := store.GetArtworkOverride(t.Context(), source.TenantID, target)
	if err != nil || head.Digest != first.Digest {
		t.Fatalf("failed transition changed head: %#v %v", head, err)
	}
	second, err := manager.SetArtworkOverride(auditEvidenceContext(), target, input, 1)
	if err != nil || second.Revision != 2 {
		t.Fatalf("reset: %#v %v", second, err)
	}
	history, err := manager.ArtworkOverrideHistory(auditEvidenceContext(), target, "", 10)
	if err != nil || len(history) != 2 || history[0].Icon.Mode != ArtworkSuppress || history[1].Icon.Mode != ArtworkInherit {
		t.Fatalf("immutable history: %#v %v", history, err)
	}
	if _, err = store.GetArtworkOverride(t.Context(), "00000000-0000-7000-8000-000000000099", target); catalog.ErrorCodeOf(err) != catalog.CodeNotFound {
		t.Fatalf("cross tenant: %v", err)
	}
	input.SourceRevision++
	if _, err = manager.SetArtworkOverride(auditEvidenceContext(), target, input, 2); catalog.ErrorCodeOf(err) != catalog.CodePreconditionFailed {
		t.Fatalf("source revision fence: %v", err)
	}
	target.PresentationGeneration++
	if _, err = manager.SetArtworkOverride(auditEvidenceContext(), target, input, 2); catalog.ErrorCodeOf(err) != catalog.CodePreconditionFailed {
		t.Fatalf("generation fence: %v", err)
	}
}
func TestPresentationGenerationDomainDerivationTracksInterfaceOnly(t *testing.T) {
	source := Source{PresentationGeneration: 7, Type: SourceARDManifest, Endpoint: "https://peer.example/ard", ProfileID: "ard-current"}
	after := source
	after.ArtworkEnabled = true
	after.Revision++
	after.Scope.Tags = []string{"allowed"}
	after.Status = SourcePaused
	if got := NextPresentationGeneration(source, after); got != 7 {
		t.Fatalf("routine change advanced generation: %d", got)
	}
	after.Endpoint = "https://peer.example/new"
	after.PresentationGeneration = NextPresentationGeneration(source, after)
	if after.PresentationGeneration != 8 {
		t.Fatal("interface did not advance")
	}
	restored := source
	if got := NextPresentationGeneration(after, restored); got != 9 {
		t.Fatalf("returning interface reused old generation: %d", got)
	}
}

func TestArtworkAdministrativeAdmissionAndHistoryBounds(t *testing.T) {
	manager, store, _, _, source, _ := newChangePreviewTest(t)
	target := ArtworkTarget{SourceID: source.ID, PresentationGeneration: 1, Identifier: "urn:air:remote.example:agents:example", Kind: catalog.KindAgent}
	ctx := auditEvidenceContext()
	if _, err := manager.GetArtworkOverride(ctx, target); catalog.ErrorCodeOf(err) != catalog.CodeNotFound {
		t.Fatalf("missing head: %v", err)
	}
	input := ArtworkOverrideInput{SourceRevision: source.Revision, Icon: ArtworkIconSelection{Mode: ArtworkSuppress}, Screenshots: ArtworkScreenshotSelection{Mode: ArtworkInherit}, Reason: "Hide source icon"}
	head, err := manager.SetArtworkOverride(ctx, target, input, 0)
	if err != nil {
		t.Fatal(err)
	}
	if got, readErr := manager.GetArtworkOverride(ctx, target); readErr != nil || got.Digest != head.Digest {
		t.Fatalf("read head: %#v %v", got, readErr)
	}
	if page, pageErr := manager.ArtworkOverrideHistory(ctx, target, head.ID, 1); pageErr != nil || len(page) != 0 {
		t.Fatalf("exclusive history cursor: %#v %v", page, pageErr)
	}
	if page, pageErr := store.ListArtworkOverrideHistory(t.Context(), "other-tenant", target, head.ID, 1); pageErr != nil || len(page) != 0 {
		t.Fatalf("foreign cursor: %#v %v", page, pageErr)
	}
	for _, limit := range []int{0, 101} {
		if _, pageErr := manager.ArtworkOverrideHistory(ctx, target, "", limit); catalog.ErrorCodeOf(pageErr) != catalog.CodeInvalidArgument {
			t.Fatalf("history limit %d: %v", limit, pageErr)
		}
	}
	if _, err = manager.ArtworkOverrideHistory(ctx, target, "not-an-id", 1); catalog.ErrorCodeOf(err) != catalog.CodeInvalidArgument {
		t.Fatalf("cursor syntax: %v", err)
	}
	for _, limit := range []int{0, 1, 2} {
		count, truncated, countErr := store.ArtworkOverrideCount(t.Context(), source.TenantID, source.ID, 1, limit)
		if countErr != nil || count != min(limit, 1) || truncated != (limit == 0) {
			t.Fatalf("count bound %d: %d %v %v", limit, count, truncated, countErr)
		}
	}
	store.observations["current"] = Observation{TenantID: source.TenantID, SourceID: source.ID, Identifier: target.Identifier, Kind: target.Kind, MediaType: "application/a2a-agent-card+json", Freshness: FreshCurrent, Completeness: CompleteEntry, Compatibility: Compatibility{Structural: "valid", Support: "supported"}}
	if current, admissionErr := manager.AdminArtworkSourceContext(ctx, target); admissionErr != nil || current.ID != source.ID {
		t.Fatalf("administrative admission: %#v %v", current, admissionErr)
	}
	source.Status = SourcePaused
	store.sources[source.ID] = source
	if _, err = manager.AdminArtworkSourceContext(ctx, target); catalog.ErrorCodeOf(err) != catalog.CodeNotFound {
		t.Fatalf("paused target: %v", err)
	}
}

func TestArtworkCurrentAuthorityAndReceiptSourceFences(t *testing.T) {
	manager, store, _, _, source, _ := newChangePreviewTest(t)
	target := ArtworkTarget{SourceID: source.ID, PresentationGeneration: 1, Identifier: "urn:air:remote.example:agents:example", Kind: catalog.KindAgent}
	ctx := identityfixture.WithActor(t.Context(), identity.Actor{TenantID: source.TenantID, Subject: "reader", Roles: []identity.Role{identity.RoleReader}})
	if _, err := manager.ArtworkSourceContext(ctx, target, source.Revision, false); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.ArtworkSourceContext(ctx, target, source.Revision, true); catalog.ErrorCodeOf(err) != catalog.CodePreconditionFailed {
		t.Fatalf("disabled live: %v", err)
	}
	source.Live.Enabled = true
	store.sources[source.ID] = source
	if _, err := manager.ArtworkSourceContext(ctx, target, source.Revision, true); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.ArtworkSourceContext(ctx, target, source.Revision+1, true); catalog.ErrorCodeOf(err) != catalog.CodePreconditionFailed {
		t.Fatalf("revision fence: %v", err)
	}
	source.Scope.ExcludedIdentifiers = []string{target.Identifier}
	store.sources[source.ID] = source
	for _, live := range []bool{false, true} {
		if _, err := manager.ArtworkSourceContext(ctx, target, source.Revision, live); catalog.ErrorCodeOf(err) != catalog.CodePreconditionFailed {
			t.Fatalf("excluded target admitted media/rating context: %v", err)
		}
	}
	source.Scope.ExcludedIdentifiers = nil
	store.sources[source.ID] = source
	if _, err := manager.GetArtworkOverride(t.Context(), target); err == nil {
		t.Fatal("anonymous head read admitted")
	}
	if _, err := manager.ArtworkOverrideHistory(t.Context(), target, "", 1); err == nil {
		t.Fatal("anonymous history admitted")
	}
	if _, err := manager.AdminArtworkSourceContext(ctx, target); err == nil {
		t.Fatal("reader managed source artwork")
	}
	if _, err := manager.ArtworkSourceContext(t.Context(), target, 1, true); err == nil {
		t.Fatal("anonymous source receipt admitted")
	}
	if _, err := manager.ResolveArtwork(t.Context(), nil); err == nil {
		t.Fatal("anonymous batch admitted")
	}
	if err := manager.ValidateLiveArtworkOverride(t.Context(), target, ""); err == nil {
		t.Fatal("anonymous image admitted")
	}
	if _, err := manager.ResolveArtwork(ctx, make([]catalog.CatalogSearchResult, 101)); catalog.ErrorCodeOf(err) != catalog.CodeInvalidArgument {
		t.Fatalf("page bound: %v", err)
	}
	if _, err := store.ArtworkReadBatch(t.Context(), source.TenantID, make([]ArtworkReadRequest, 101)); catalog.ErrorCodeOf(err) != catalog.CodeInvalidArgument {
		t.Fatalf("store bound: %v", err)
	}
}

func TestArtworkRejectsInvalidTargetSelectionAndDerivedMetadata(t *testing.T) {
	manager, _, _, _, source, _ := newChangePreviewTest(t)
	target := ArtworkTarget{SourceID: source.ID, PresentationGeneration: 1, Identifier: "urn:air:remote.example:agents:example", Kind: catalog.KindAgent}
	bad := target
	bad.Version = "1"
	if err := ValidateArtworkTarget(bad); catalog.ErrorCodeOf(err) != catalog.CodeInvalidArgument {
		t.Fatalf("version presence: %v", err)
	}
	if _, err := manager.GetArtworkOverride(auditEvidenceContext(), bad); err == nil {
		t.Fatal("invalid read target admitted")
	}
	if _, err := manager.AdminArtworkSourceContext(auditEvidenceContext(), bad); err == nil {
		t.Fatal("invalid admin target admitted")
	}
	if _, err := manager.ArtworkSourceContext(auditEvidenceContext(), bad, 1, false); err == nil {
		t.Fatal("invalid receipt target admitted")
	}
	if err := manager.ValidateLiveArtworkOverride(auditEvidenceContext(), bad, ""); err == nil {
		t.Fatal("invalid live target admitted")
	}
	if _, err := manager.ArtworkOverrideHistory(auditEvidenceContext(), bad, "", 1); err == nil {
		t.Fatal("invalid history target admitted")
	}
	input := ArtworkOverrideInput{SourceRevision: 1, Icon: ArtworkIconSelection{Mode: ArtworkInherit}, Screenshots: ArtworkScreenshotSelection{Mode: ArtworkInherit}, Reason: "Explain presentation choice"}
	for _, mode := range []ArtworkMode{"", "bogus", ArtworkReplace} {
		candidate := input
		candidate.Icon.Mode = mode
		if err := validateArtworkSelection(candidate); err == nil {
			t.Fatalf("invalid icon mode %q admitted", mode)
		}
	}
	input.Icon.Mode = ArtworkReplace
	input.Icon.Asset = &artwork.Asset{AssetInput: artwork.AssetInput{URL: "https://images.example/unsealed.png", Alt: "Unsealed"}}
	if err := validateArtworkSelection(input); err == nil {
		t.Fatal("unsealed replacement admitted")
	}
	for _, raw := range []json.RawMessage{[]byte(`{"unknown":true}`), []byte(`{"publisher":{"schemaVersion":1,"icon":{"url":"https://images.example/unsealed.png"}}}`), []byte(`{"nativeIcon":{"url":"http://images.example/unsafe.png","alt":"Unsafe"}}`)} {
		if err := ValidateArtworkMetadata(raw); err == nil {
			t.Fatalf("invalid metadata admitted: %s", raw)
		}
	}
	if err := ValidateArtworkMetadata(ExtractArtworkCandidates(nil, []byte(`{"iconUrl":"https://images.example/native.png"}`))); err != nil {
		t.Fatal(err)
	}
}

func TestArtworkMergePrecedenceDoesNotRewritePublisher(t *testing.T) {
	asset := &artwork.Asset{AssetInput: artwork.AssetInput{URL: "https://images.example/publisher.png", Alt: "Publisher"}}
	publisher := &artwork.Descriptor{SchemaVersion: 1, Icon: asset}
	read := ArtworkReadContext{Source: Source{ArtworkEnabled: true}, Candidates: ArtworkCandidates{Publisher: publisher, NativeIcon: &artwork.AssetInput{URL: "https://images.example/native.png"}}}
	projection := resolveArtworkPresentation(read)
	if projection.IconSource != "publisher" || projection.MutableIcon {
		t.Fatalf("publisher precedence: %#v", projection)
	}
	read.Override = &ArtworkOverride{Icon: ArtworkIconSelection{Mode: ArtworkSuppress}, Screenshots: ArtworkScreenshotSelection{Mode: ArtworkInherit}}
	projection = resolveArtworkPresentation(read)
	if projection.Descriptor.Icon != nil || projection.IconSource != "local_override" || publisher.Icon != asset {
		t.Fatalf("suppression mutated evidence: %#v", projection)
	}
	read.Source.ArtworkEnabled = false
	read.Override.Icon.Mode = ArtworkReplace
	read.Override.Icon.Asset = &artwork.Asset{AssetInput: artwork.AssetInput{URL: "https://images.example/local.png", Alt: "Local"}}
	projection = resolveArtworkPresentation(read)
	if projection.Descriptor.Icon.Alt != "Local" || projection.IconSource != "local_override" {
		t.Fatalf("source opt-out hid local choice: %#v", projection)
	}
	read.Override = nil
	if projection = resolveArtworkPresentation(read); projection != nil {
		t.Fatalf("source opt-out leaked source media: %#v", projection)
	}
}

func TestLiveArtworkReceiptFencesAndGlyphAdmission(t *testing.T) {
	manager, store, _, _, source, _ := newChangePreviewTest(t)
	source.Live.Enabled = true
	source.RetrievalMode = RetrievalHybrid
	store.mu.Lock()
	store.sources[source.ID] = source
	store.mu.Unlock()
	ctx := identityfixture.WithActor(t.Context(), identity.Actor{TenantID: source.TenantID, Subject: "reader", Roles: []identity.Role{identity.RoleReader}})
	target := ArtworkTarget{SourceID: source.ID, PresentationGeneration: 1, Identifier: "urn:air:remote.example:agents:example", Kind: catalog.KindAgent}
	result := catalog.CatalogSearchResult{Type: catalog.SearchResultLive, Identifier: target.Identifier, Kind: target.Kind, Live: &catalog.LiveSearchEvidence{SourceID: source.ID, SourceRevision: source.Revision, PresentationGeneration: 1, PolicyDigest: source.PolicyDigest, CredentialRevision: source.CredentialRevision, Entry: []byte(`{"identifier":"urn:air:remote.example:agents:example","type":"application/agent+json","url":"https://remote.example/agent.json"}`)}}
	admitted, err := manager.ResolveArtwork(ctx, []catalog.CatalogSearchResult{result})
	if err != nil || !admitted[0].Live.ArtworkAdmitted || admitted[0].Artwork != nil || result.Live.ArtworkAdmitted {
		t.Fatalf("glyph receipt admission or shared result mutation: %#v %v", admitted, err)
	}
	if err = manager.ValidateLiveArtworkOverride(ctx, target, ""); err != nil {
		t.Fatal(err)
	}
	input := ArtworkOverrideInput{SourceRevision: source.Revision, Icon: ArtworkIconSelection{Mode: ArtworkSuppress}, Screenshots: ArtworkScreenshotSelection{Mode: ArtworkInherit}, Reason: "Hide publisher artwork"}
	overlay, err := manager.SetArtworkOverride(auditEvidenceContext(), target, input, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err = manager.ValidateLiveArtworkOverride(ctx, target, ""); catalog.ErrorCodeOf(err) != catalog.CodePreconditionFailed {
		t.Fatalf("old receipt survived override: %v", err)
	}
	if err = manager.ValidateLiveArtworkOverride(ctx, target, overlay.Digest); err != nil {
		t.Fatal(err)
	}
	source.Scope.ExcludedIdentifiers = []string{target.Identifier}
	store.sources[source.ID] = source
	if _, err = manager.ArtworkSourceContext(ctx, target, source.Revision, true); catalog.ErrorCodeOf(err) != catalog.CodePreconditionFailed {
		t.Fatalf("exclusion did not invalidate receipt source admission: %v", err)
	}
	if _, err = manager.SetArtworkOverride(auditEvidenceContext(), target, input, overlay.Revision); catalog.ErrorCodeOf(err) != catalog.CodePreconditionFailed {
		t.Fatalf("excluded override accepted: %v", err)
	}
	if _, err = manager.GetArtworkOverride(auditEvidenceContext(), target); err != nil {
		t.Fatalf("administrative override hidden: %v", err)
	}
	clearedSelection := input
	clearedSelection.Icon.Mode = ArtworkInherit
	if _, err = manager.SetArtworkOverride(auditEvidenceContext(), target, clearedSelection, overlay.Revision); err != nil {
		t.Fatalf("excluded override could not be cleared: %v", err)
	}
	source.Scope.ExcludedIdentifiers = nil
	store.sources[source.ID] = source
	result.Live.SourceRevision++
	denied, err := manager.ResolveArtwork(ctx, []catalog.CatalogSearchResult{result})
	if err != nil || denied[0].Live.ArtworkAdmitted || denied[0].Artwork != nil {
		t.Fatalf("stale revision retained receipt or artwork: %#v %v", denied, err)
	}
}

func TestNativeArtworkCandidatesAreBoundedMutableHints(t *testing.T) {
	icons := make([]nativeArtworkIcon, 17)
	for i := range icons {
		icons[i] = nativeArtworkIcon{Src: "http://unsafe.example/icon.svg", MimeType: "image/svg+xml"}
	}
	icons[16] = nativeArtworkIcon{Src: "https://images.example/ignored.png", MimeType: "image/png"}
	if got := selectNativeArtworkIcon(icons); got != nil {
		t.Fatalf("inspected seventeenth icon: %#v", got)
	}
	icons[5] = nativeArtworkIcon{Src: "https://images.example/dark.png", MimeType: "image/png", Theme: "dark", Sizes: []string{"128x128"}}
	icons[7] = nativeArtworkIcon{Src: "https://images.example/small.png", MimeType: "image/png", Sizes: []string{"16x16"}}
	icons[9] = nativeArtworkIcon{Src: "https://images.example/neutral.png", MimeType: "image/png", Sizes: []string{"128x128"}}
	if got := selectNativeArtworkIcon(icons); got == nil || got.URL != icons[9].Src || got.Rights != nil {
		t.Fatalf("deterministic native hint: %#v", got)
	}
	raw := json.RawMessage(`{"icons":[{"src":"http://unsafe.example/icon.svg","mimeType":"image/svg+xml"}]}`)
	before := string(raw)
	if got := ExtractArtworkCandidates(nil, raw); got != nil || string(raw) != before {
		t.Fatalf("optional unsafe hint changed native evidence: %s", got)
	}
	raw = json.RawMessage(`{"icons":[{"src":"https://images.example/icon.png","mimeType":"image/png"}]}`)
	var candidates ArtworkCandidates
	if err := json.Unmarshal(ExtractArtworkCandidates(nil, raw), &candidates); err != nil || candidates.NativeIcon == nil || candidates.NativeIcon.Rights != nil {
		t.Fatalf("native bytes falsely sealed: %#v %v", candidates, err)
	}
}

func TestNativeArtworkCandidateValidationPreservesOptionalPublisherAndNativeEvidence(t *testing.T) {
	asset := artwork.Asset{AssetInput: artwork.AssetInput{URL: "https://images.example/publisher.png", Alt: "Publisher icon", Rights: &artwork.Rights{Basis: "owned", Attribution: "Example publisher"}}, Digest: "sha256:" + strings.Repeat("a", 64), MediaType: "image/png", Size: 80, Width: 16, Height: 16, RightsAssessment: artwork.NotAssessed}
	publisher := map[string]any{artwork.IRI: map[string]any{"@type": "@json", "@value": artwork.Descriptor{SchemaVersion: 1, Icon: &asset}}}
	for name, url := range map[string]string{"empty fragment": "https://images.example/icon.png#", "credential": "https://images.example/icon.png?token=fixture", "control": "https://images.example/icon\x01.png", "oversize": "https://images.example/" + strings.Repeat("a", 2048)} { //nolint:gosec // Deliberately synthetic credential query tests reference rejection; no network request is made.
		t.Run(name, func(t *testing.T) {
			if validNativeArtworkURL(url) {
				t.Fatal("invalid candidate admitted")
			}
			for _, native := range []any{map[string]any{"iconUrl": url}, map[string]any{"icons": []nativeArtworkIcon{{Src: url, MimeType: "image/png"}, {Src: "https://images.example/later.jpg", MimeType: "image/jpeg"}}}} {
				raw, err := json.Marshal(native)
				if err != nil {
					t.Fatal(err)
				}
				before := string(raw)
				projection := ExtractArtworkCandidates(publisher, raw)
				if err = ValidateArtworkMetadata(projection); err != nil {
					t.Fatalf("optional native hint rejects projection: %v", err)
				}
				var got ArtworkCandidates
				if err = json.Unmarshal(projection, &got); err != nil || got.Publisher == nil || got.Publisher.Icon.Digest != asset.Digest || string(raw) != before {
					t.Fatalf("publisher or native evidence changed: %s %v", projection, err)
				}
				if _, isIconURL := native.(map[string]any)["iconUrl"]; isIconURL {
					if got.NativeIcon != nil {
						t.Fatal("invalid A2A hint retained")
					}
				} else if got.NativeIcon == nil || got.NativeIcon.URL != "https://images.example/later.jpg" {
					t.Fatal("invalid preferred icon hid valid later candidate")
				}
			}
		})
	}
	// Malformed UTF-8 is rejected at the source JSON boundary; the URL helper
	// also refuses it rather than admitting a candidate Go cannot retain exactly.
	if validNativeArtworkURL("https://images.example/" + string([]byte{0xff})) {
		t.Fatal("invalid UTF-8 candidate admitted")
	}
	manager, store, fakeClock, _, source, _ := newChangePreviewTest(t)
	now := fakeClock.Now()
	entry := sourceEntry{Identifier: "urn:air:remote.example:agents:example", Type: "application/a2a-agent-card+json", Version: "1", VersionPresent: true, Data: json.RawMessage(`{"iconUrl":"https://images.example/icon.png#"}`), ProfileID: source.ProfileID, EntryLocation: "/entries/0"}
	raw, err := json.Marshal(entry)
	if err != nil {
		t.Fatal(err)
	}
	entry.Raw = raw
	entry.RetrievalDigest = observationDigest(raw)
	snapshot := Snapshot{ID: "00000000-0000-7000-8000-000000000031", TenantID: source.TenantID, SourceID: source.ID, SourceRevision: source.Revision, State: SnapshotComplete, EnumerationComplete: true, WithdrawalAuthority: true, MemberCount: 1, PolicyID: source.PolicyID, PolicyRevision: source.PolicyRevision, PolicyDigest: source.PolicyDigest, StartedAt: now, CompletedAt: &now}
	observation, err := manager.observation(source.TenantID, source, snapshot.ID, entry, 0, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(observation.ArtworkMetadata) != 0 {
		t.Fatal("invalid optional hint retained")
	}
	if _, _, err = store.CommitSync(t.Context(), snapshot, []Observation{observation}, source, source.Revision); err != nil {
		t.Fatalf("optional icon rejected snapshot: %v", err)
	}
}

type artworkShadowReader struct{}

func (artworkShadowReader) HasPublishedIdentity(context.Context, string, string, string) (bool, error) {
	return true, nil
}
func TestLiveArtworkReceiptRechecksLocalPublishedPrecedence(t *testing.T) {
	manager, store, _, _, source, _ := newChangePreviewTest(t)
	store.BindLocalCatalog(artworkShadowReader{})
	ctx := identityfixture.WithActor(t.Context(), identity.Actor{TenantID: source.TenantID, Subject: "reader", Roles: []identity.Role{identity.RoleReader}})
	target := ArtworkTarget{SourceID: source.ID, PresentationGeneration: 1, Identifier: "urn:air:remote.example:agents:example", VersionPresent: true, Version: "1", Kind: catalog.KindAgent}
	if err := manager.ValidateLiveArtworkOverride(ctx, target, ""); catalog.ErrorCodeOf(err) != catalog.CodeNotFound {
		t.Fatalf("local publication did not invalidate live receipt: %v", err)
	}
}

func TestMemorySyncRefreshesDerivedArtworkWithoutChangingEvidence(t *testing.T) {
	manager, store, fakeClock, _, source, _ := newChangePreviewTest(t)
	now := fakeClock.Now()
	entry := sourceEntry{Identifier: "urn:air:remote.example:agents:example", Type: "application/a2a-agent-card+json", Version: "1", VersionPresent: true, Data: json.RawMessage(`{"iconUrl":"https://images.example/native.png"}`), ProfileID: source.ProfileID, EntryLocation: "/entries/0"}
	raw, err := json.Marshal(entry)
	if err != nil {
		t.Fatal(err)
	}
	entry.Raw = raw
	entry.RetrievalDigest = observationDigest(raw)
	snapshot := Snapshot{ID: "00000000-0000-7000-8000-000000000031", TenantID: source.TenantID, SourceID: source.ID, SourceRevision: source.Revision, State: SnapshotComplete, EnumerationComplete: true, WithdrawalAuthority: true, MemberCount: 1, PolicyID: source.PolicyID, PolicyRevision: source.PolicyRevision, PolicyDigest: source.PolicyDigest, StartedAt: now, CompletedAt: &now}
	original, err := manager.observation(source.TenantID, source, snapshot.ID, entry, 0, now)
	if err != nil {
		t.Fatal(err)
	}
	original.ArtworkMetadata = nil
	if _, _, err = store.CommitSync(t.Context(), snapshot, []Observation{original}, source, source.Revision); err != nil {
		t.Fatal(err)
	}
	fakeClock.Advance(time.Second)
	later := fakeClock.Now()
	snapshot.ID = "00000000-0000-7000-8000-000000000032"
	snapshot.StartedAt = later
	snapshot.CompletedAt = &later
	incoming, err := manager.observation(source.TenantID, source, snapshot.ID, entry, 0, later)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = store.CommitSync(t.Context(), snapshot, []Observation{incoming}, source, source.Revision); err != nil {
		t.Fatal(err)
	}
	retained := store.observations[observationKey(original)]
	if retained.ID != original.ID || retained.ExactEntryDigest != original.ExactEntryDigest || retained.IdentityDigest != original.IdentityDigest || string(retained.RawEntry) != string(original.RawEntry) || string(retained.NormalizedEntry) != string(original.NormalizedEntry) || len(retained.ArtworkMetadata) == 0 || len(store.snapshots) != 2 || len(store.occurrences) != 2 {
		t.Fatalf("derived refresh changed sealed evidence/history: %#v", retained)
	}
}
