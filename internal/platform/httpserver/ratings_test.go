package httpserver

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/federation"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/clock"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/id"
	"github.com/open-agentic-catalog/open-agentic-catalog/test/identityfixture"
)

func TestLiveRatingProofCannotTransferOrOutliveAuthority(t *testing.T) {
	source := federationFixtureSource()
	source.Live.Enabled = true
	source.PresentationGeneration = 1
	store := &artworkReceiptStore{source: source}
	manager, err := federation.NewManager(store, publicPolicyFetcher{}, clock.NewFake(projectionTime), id.Generator{})
	if err != nil {
		t.Fatal(err)
	}
	server := projectionServer(nil)
	server.federation = manager
	ctx := identityfixture.WithActor(t.Context(), projectionActor(identity.RoleTenantAdmin))
	item := catalog.CatalogSearchResult{Identifier: federationFixtureAIR, Kind: catalog.KindAgent, Live: &catalog.LiveSearchEvidence{SourceID: source.ID, SourceRevision: source.Revision, PresentationGeneration: 1, PolicyDigest: source.PolicyDigest, Entry: json.RawMessage(`{"identifier":"urn:air:peer.example:default:fixture"}`)}}
	proof, err := server.signRatingReceipt(ctx, item)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = server.verifyRatingReceipt(ctx, proof); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"", proof + "=", proof + ".x", strings.Repeat("x", 4097), "invalid.sig", proof[:len(proof)-2] + "aa"} {
		if _, err = server.verifyRatingReceipt(ctx, bad); err == nil {
			t.Fatal("accepted malformed proof")
		}
	}
	artworkProof, err := server.signArtworkReceipt(ctx, item)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = server.verifyRatingReceipt(ctx, artworkProof); err == nil {
		t.Fatal("artwork proof authorized rating")
	}
	for name, mutate := range map[string]func(){
		"source revision":       func() { store.source.Revision++ },
		"replacement authority": func() { store.source.PresentationGeneration++ },
		"policy":                func() { store.source.PolicyDigest = "changed" },
		"credential":            func() { store.source.CredentialRevision = "changed" },
		"paused":                func() { store.source.Status = federation.SourcePaused },
		"live disabled":         func() { store.source.Live.Enabled = false },
		"expiry":                func() { server.now = func() time.Time { return projectionTime.Add(5 * time.Minute) } },
	} {
		t.Run(name, func(t *testing.T) {
			store.source = source
			server.now = func() time.Time { return projectionTime }
			mutate()
			if _, err := server.verifyRatingReceipt(ctx, proof); catalog.ErrorCodeOf(err) != catalog.CodePreconditionFailed {
				t.Fatalf("stale proof: %v", err)
			}
		})
	}
	store.source = source
	server.now = func() time.Time { return projectionTime }
	other := projectionActor(identity.RoleTenantAdmin)
	other.Subject = "another person"
	if _, err = server.verifyRatingReceipt(identityfixture.WithActor(t.Context(), other), proof); err == nil {
		t.Fatal("principal transfer")
	}
	other.TenantID = projectionNext
	if _, err = server.verifyRatingReceipt(identityfixture.WithActor(t.Context(), other), proof); err == nil {
		t.Fatal("tenant transfer")
	}
	input := ratingRequest{Target: ratingItemAdmission(item).Target, Receipt: proof}
	if _, err = server.ratingAdmission(ctx, input); err != nil {
		t.Fatal(err)
	}
	input.Target.VersionPresent = true
	input.Target.Version = "1"
	if _, err = server.ratingAdmission(ctx, input); err == nil {
		t.Fatal("target substitution")
	}
	broken := *item.Live
	broken.Entry = json.RawMessage(`{"broken":`)
	item.Live = &broken
	if _, err = server.signRatingReceipt(ctx, item); catalog.ErrorCodeOf(err) != catalog.CodeInternal {
		t.Fatalf("signed invalid evidence %v", err)
	}
}
