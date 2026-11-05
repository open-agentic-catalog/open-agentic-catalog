//go:build functional

package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/adapter"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/artwork"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/audit"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/federation"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/clock"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/fetcher"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/id"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/publication"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/ratings"
	"github.com/open-agentic-catalog/open-agentic-catalog/test/identityfixture"
)

type artworkPeerRecords struct{ record catalog.CatalogRecord }

func (r *artworkPeerRecords) PublicationRecords(context.Context, string, bool, int) ([]catalog.CatalogRecord, bool, error) {
	return []catalog.CatalogRecord{r.record}, false, nil
}

type artworkPeerBytes struct {
	body  []byte
	calls int
}

func (f *artworkPeerBytes) Fetch(context.Context, artwork.FetchRequest) (artwork.FetchResult, error) {
	f.calls++
	return artwork.FetchResult{Body: f.body, MediaType: "image/png"}, nil
}

// The actual publication manager retains reviewed descriptors; the independent
// consuming store inspects and synchronizes its immutable wire documents over
// owned TLS. Image delivery/private TLS is separately qualified at runtime edge.
func TestOwnedCatalogPeerArtworkSnapshotAndAdministrativePresentation(t *testing.T) {
	const tenant = "00000000-0000-7000-8000-000000000001"
	const identifier = "urn:air:publisher.example:agents:artwork"
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	ctx = identityfixture.WithActor(ctx, identity.Actor{TenantID: tenant, Subject: "peer-artwork-admin", Issuer: "https://identity.owned.example", ActorType: identity.ActorHuman, MembershipID: "00000000-0000-7000-8000-000000000002", RealmID: "00000000-0000-7000-8000-000000000003", Roles: []identity.Role{identity.RoleTenantAdmin, identity.RolePublisher, identity.RoleReader}})
	var imageBytes bytes.Buffer
	if err := png.Encode(&imageBytes, image.NewNRGBA(image.Rect(0, 0, 3, 2))); err != nil {
		t.Fatal(err)
	}
	media := &artworkPeerBytes{body: imageBytes.Bytes()}
	validator, err := artwork.New(media, artwork.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	input := artwork.Input{SchemaVersion: 1, Icon: &artwork.AssetInput{URL: "https://images.example/owned.png", Alt: "Owned fixture", Rights: &artwork.Rights{Basis: "owned", Attribution: "Synthetic peer fixture"}}}
	descriptor, err := validator.Seal(ctx, tenant, input)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, time.October, 8, 12, 0, 0, 0, time.UTC)
	records := &artworkPeerRecords{record: catalog.CatalogRecord{Resource: catalog.Resource{ID: "00000000-0000-7000-8000-000000000011", TenantID: tenant, CanonicalIdentifier: identifier, Kind: catalog.KindAgent}, Version: catalog.ResourceVersion{ID: "version-1", Version: "1.0.0"}, VersionRevision: catalog.VersionRevision{ID: "00000000-0000-7000-8000-000000000012", ContentSetDigest: "sha256:" + strings.Repeat("a", 64), Representations: []catalog.Representation{{Role: catalog.RepresentationPrimary, MediaType: adapter.MediaA2AAgentCard, Data: json.RawMessage(`{"name":"Owned peer Agent"}`), ValidationState: "valid"}}}, ListingRevision: catalog.ListingRevision{DisplayName: "Owned peer Agent", Description: "A reviewed peer artwork fixture.", RepresentativeQueries: []string{"Find artwork Agent", "Show owned peer Agent"}, Visibility: catalog.VisibilityPublic, Extensions: map[string]any{artwork.IRI: descriptor}}, Release: catalog.ListingRelease{ID: "00000000-0000-7000-8000-000000000013", ActivatedAt: now.Add(-time.Hour), UpdatedAt: now}, Evidence: catalog.EvidenceSet{SubjectDigest: "sha256:" + strings.Repeat("a", 64), State: catalog.EvidenceComplete, LegalRights: catalog.LegalRightsSummary{State: catalog.LegalRightsNotAssessed}}}}
	var active publication.Snapshot
	var requests atomic.Int32
	peer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" || len(r.TLS.PeerCertificates) != 0 {
			t.Error("peer inherited credentials")
		}
		switch r.URL.Path {
		case "/ard.json":
			w.Header().Set("Content-Type", "application/ard+json")
			_ = json.NewEncoder(w).Encode(active.ARD)
		case "/ai.json":
			w.Header().Set("Content-Type", "application/ai-catalog+json")
			_ = json.NewEncoder(w).Encode(active.AICatalog)
		default:
			http.NotFound(w, r)
		}
	}))
	defer peer.Close()
	peerHost := "127.0.0.1"
	opinions := ratings.NewMemoryStore(catalog.NewMemoryRepository())
	ratingService, err := ratings.New(opinions, peerOpinionAuthority{}, clock.NewFake(now), id.Generator{})
	if err != nil {
		t.Fatal(err)
	}
	target := ratings.Target{Origin: "local", ResourceID: records.record.Resource.ID, VersionRevisionID: records.record.VersionRevision.ID, Identifier: identifier, Kind: catalog.KindAgent, VersionPresent: true, Version: "1.0.0"}
	admission := ratings.Admission{Target: target, ReleaseID: records.record.Release.ID, EntryDigest: records.record.VersionRevision.ContentSetDigest}
	if _, err = ratingService.SetSharing(ctx, peerHost, "anonymous", true, 0, "Share synthetic consented summaries"); err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 5; i++ {
		actor, _ := identity.FromContext(ctx)
		actor.Subject = fmt.Sprintf("peer-voter-%d", i)
		actor.MembershipID, err = (id.Generator{}).New(now)
		if err != nil {
			t.Fatal(err)
		}
		yes := true
		if _, err = ratingService.Submit(identityfixture.WithActor(t.Context(), actor), admission, i, &yes, 0); err != nil {
			t.Fatal(err)
		}
	}
	publisher, err := publication.NewManager(publication.Options{Records: records, Store: publication.NewMemoryStore(), Clock: clock.NewFake(now), IDs: id.Generator{}, BaseURL: peer.URL, Host: peerHost, Ratings: opinions, RatingsSharingEnabled: true, RatingsMinimumSharedCount: 5, PublisherDomain: "publisher.example", ARDProfile: "ard-0.91-aa3e598", AICatalogProfile: "ai-catalog-1.0-b062278", CursorKey: bytes.Repeat([]byte{9}, 32), ArtworkAllowed: func(d artwork.Descriptor) bool { return d.Icon != nil && d.Icon.URL == input.Icon.URL }})
	if err != nil {
		t.Fatal(err)
	}
	candidate, err := publisher.BuildCandidate(ctx, tenant)
	if err != nil {
		t.Fatal(err)
	}
	active, err = publisher.Activate(ctx, tenant, candidate.ID, "Publish reviewed owned artwork")
	if err != nil {
		t.Fatal(err)
	}
	if media.calls != 1 {
		t.Fatal("snapshot construction fetched media")
	}
	records.record.ListingRevision.Extensions = nil
	retained, err := publisher.Active(ctx, tenant)
	if err != nil {
		t.Fatal(err)
	}
	if len(retained.AICatalog.Entries) != 1 || retained.AICatalog.Entries[0].Extensions[artwork.IRI] == nil {
		t.Fatal("active snapshot lost reviewed artwork after draft changed")
	}
	origin, err := fetcher.NormalizeOrigin(peer.URL, false)
	if err != nil {
		t.Fatal(err)
	}
	router, err := fetcher.NewRouter(fetcher.RoutingPolicy{ID: "owned-peers", Revision: 1, Digest: "sha256:" + strings.Repeat("3", 64), Public: fetcher.PublicRoute{Mode: fetcher.PublicDisabled}, Timeout: time.Second, MaxResponseBytes: 2 << 20, PrivateRoutes: []fetcher.PrivateRoute{{Name: "owned-peer", Origins: []string{origin}, AssignedTenantIDs: []string{tenant}, AddressPrefixes: []netip.Prefix{netip.MustParsePrefix("127.0.0.1/32")}, AllowTestAddresses: true, TrustBundlePEM: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: peer.Certificate().Raw})}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, endpoint := range []string{"/ard.json", "/ai.json"} {
		t.Run(endpoint, func(t *testing.T) {
			evidence := catalog.NewMemoryRepository()
			store := federation.NewMemoryStore(evidence)
			consumer, e := federation.NewManager(store, router, clock.NewFake(now), id.Generator{}, federation.WithEvidenceSink(evidence, true))
			if e != nil {
				t.Fatal(e)
			}
			inspection, e := consumer.InspectWithAccess(ctx, federation.InspectionInput{URL: peer.URL + endpoint, NetworkProfile: "owned-peer"})
			if e != nil {
				t.Fatal(e)
			}
			source, e := consumer.Connect(ctx, federation.ConnectInput{InspectionID: inspection.ID, DisplayName: "Owned publisher", ArtworkEnabled: true, RetrievalMode: federation.RetrievalIndexed, Scope: federation.Scope{Identifiers: []string{identifier}}, Reason: "Display publisher artwork from owned peer"})
			if e != nil {
				t.Fatal(e)
			}
			jobContext := audit.WithJobExecution(ctx, audit.JobExecution{ID: "00000000-0000-7000-8000-000000000004", Kind: "source.sync", Attempt: 1, Trigger: "manual", Initiator: "00000000-0000-7000-8000-000000000002"})
			if _, e = consumer.Sync(jobContext, source.ID); e != nil {
				t.Fatal(e)
			}
			observations, overflow, e := store.ListCurrentObservations(ctx, tenant, source.ID, 10)
			if e != nil || overflow || len(observations) != 1 {
				t.Fatalf("observations=%d overflow=%v err=%v", len(observations), overflow, e)
			}
			observation := observations[0]
			if len(observation.ArtworkMetadata) == 0 || observation.ArtworkMetadata[0] != '{' {
				t.Fatal("compact descriptor not retained")
			}
			items := []catalog.CatalogSearchResult{{Identifier: identifier, Version: observation.Version, VersionPresent: observation.VersionPresent, Kind: catalog.KindAgent, Indexed: &catalog.IndexedSearchEvidence{PresentationSourceID: source.ID, PresentationGeneration: 1, NormalizedEntryDigest: observation.NormalizedEntryDigest}}}
			got, e := consumer.ResolveArtwork(ctx, items)
			if e != nil || got[0].Artwork == nil || got[0].Artwork.Descriptor.Icon.Digest != descriptor.Icon.Digest {
				t.Fatalf("publisher rendering missing: %#v %v", got, e)
			}
			if len(observation.RatingsMetadata) == 0 {
				t.Fatal("peer rating summary not retained")
			}
			rated, e := consumer.ResolveRatingSignals(ctx, items)
			if e != nil || rated[0].PeerRatings == nil || rated[0].PeerRatings.Count != 5 || rated[0].PeerRatings.Average != 3 || rated[0].PeerRatings.Reporter != peer.URL {
				t.Fatalf("peer claim lost %+v %v", rated, e)
			}
			localOpinions := ratings.NewMemoryStore(evidence)
			localRatings, e := ratings.New(localOpinions, peerOpinionAuthority{consumer}, clock.NewFake(now), id.Generator{})
			if e != nil {
				t.Fatal(e)
			}
			foreign := ratings.Admission{Target: ratings.Target{Origin: "foreign", SourceID: source.ID, PresentationGeneration: 1, Identifier: identifier, Kind: catalog.KindAgent, VersionPresent: observation.VersionPresent, Version: observation.Version}}
			before := requests.Load()
			if _, e = localRatings.Submit(ctx, foreign, 1, nil, 0); e != nil {
				t.Fatal(e)
			}
			view, e := localRatings.Read(ctx, foreign)
			if e != nil || view.Count != 1 || view.Average == nil || *view.Average != 1 || rated[0].PeerRatings.Count != 5 || requests.Load() != before {
				t.Fatal("mixed peer/local opinions or fetched during write", e)
			}
			setOptIn := func(enabled bool) {
				t.Helper()
				current, getErr := consumer.GetSource(ctx, source.ID)
				if getErr != nil {
					t.Fatal(getErr)
				}
				patch := federation.PatchInput{Intent: federation.ChangeUpdate, ArtworkEnabled: &enabled, Reason: "Change explicit source artwork preference"}
				preview, previewErr := consumer.PreviewChange(ctx, source.ID, patch)
				if previewErr != nil {
					t.Fatal(previewErr)
				}
				patch.ChangePreviewDigest = preview.Digest
				source, getErr = consumer.UpdateSource(ctx, source.ID, patch, current.Revision)
				if getErr != nil {
					t.Fatal(getErr)
				}
			}
			setOptIn(false)
			got, e = consumer.ResolveArtwork(ctx, items)
			if e != nil || got[0].Artwork != nil {
				t.Fatalf("source opt-out ignored: %#v %v", got, e)
			}
			setOptIn(true)
			target := federation.ArtworkTarget{SourceID: source.ID, PresentationGeneration: 1, Identifier: identifier, Version: observation.Version, VersionPresent: observation.VersionPresent, Kind: catalog.KindAgent}
			_, e = consumer.SetArtworkOverride(ctx, target, federation.ArtworkOverrideInput{SourceRevision: source.Revision, Icon: federation.ArtworkIconSelection{Mode: federation.ArtworkSuppress}, Screenshots: federation.ArtworkScreenshotSelection{Mode: federation.ArtworkInherit}, Reason: "Suppress peer icon locally"}, 0)
			if e != nil {
				t.Fatal(e)
			}
			got, e = consumer.ResolveArtwork(ctx, items)
			if e != nil || got[0].Artwork == nil || got[0].Artwork.Descriptor.Icon != nil || got[0].Artwork.IconSource != "local_override" {
				t.Fatalf("local suppression ignored: %#v %v", got, e)
			}
		})
	}
	if requests.Load() < 4 || media.calls != 1 {
		t.Fatal("peer transport or media no-fetch assertion failed")
	}
}

// Only publication input selection is substituted here; peer traffic uses TLS.
type peerOpinionAuthority struct{ peer *federation.Manager }

func (a peerOpinionAuthority) Admit(ctx context.Context, input ratings.Admission) (ratings.Admission, error) {
	if a.peer != nil {
		return a.peer.AdmitRating(ctx, input)
	}
	return input, nil
}
