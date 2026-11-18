//go:build functional

package clientcompat_test

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/federation"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/fetcher"
)

type adoptionSource struct {
	body []byte
	now  func() time.Time
}

func (*adoptionSource) PolicyDigest() string { return noFetch{}.PolicyDigest() }
func (s *adoptionSource) Fetch(_ context.Context, request fetcher.Request) (fetcher.Result, error) {
	return fetcher.Result{RequestedURL: request.URL, FinalURL: request.URL, Status: 200, MediaType: "application/ard+json", Body: s.body, BodyDigest: fmt.Sprintf("sha256:%x", sha256.Sum256(s.body)), PolicyID: "sdk-adoption", PolicyRevision: 1, PolicyDigest: s.PolicyDigest(), NetworkProfile: request.NetworkProfile, FetchedAt: s.now()}, nil
}

type versionAdoptionCase struct {
	Origin                 string                       `json:"origin"`
	ResourceID             string                       `json:"resourceId"`
	PriorVersionRevisionID string                       `json:"priorVersionRevisionId"`
	SourceID               string                       `json:"sourceId"`
	Body                   federation.AdoptVersionInput `json:"body"`
}

// Each SDK consumer gets a separate enabled API and two real ingested Source
// generations. Existing default-off fixtures retain their original behavior.
func newVersionAdoptionCase(t *testing.T) versionAdoptionCase {
	t.Helper()
	outbound := &adoptionSource{}
	f := newFixtureWithFederation(t, nil, outbound, true)
	outbound.now = f.clock.Now
	ctx := f.actor("admin")
	ns, err := f.service.CreateNamespace(ctx, catalog.CreateNamespaceInput{Slug: "version-adoption", DisplayName: "Version adoption"})
	must(t, err)
	pub, err := f.service.CreatePublisher(ctx, catalog.CreatePublisherInput{DisplayName: "Local", Domains: []string{"source.example"}})
	must(t, err)
	set := func(label string) {
		outbound.body = []byte(fmt.Sprintf(`{"entries":[{"identifier":"urn:air:source.example:agents:sdk-adoption","version":%q,"displayName":"Upstream","description":"SDK Source evidence","type":"application/a2a-agent-card+json","data":{"name":"Upstream","version":%q,"description":"SDK Source evidence","supportedInterfaces":[{"url":"https://agents.example.com/adoption","protocolBinding":"JSONRPC","protocolVersion":"1.0"}],"capabilities":{},"defaultInputModes":["application/json"],"defaultOutputModes":["application/json"],"skills":[{"id":"evidence","name":"Evidence","description":"Find evidence","tags":["sdk"]}]}}]}`, label, label))
	}
	set("opaque-first")
	inspection, err := f.federation.Inspect(ctx, "https://source.example/ard.json")
	must(t, err)
	source, err := f.federation.Connect(ctx, federation.ConnectInput{InspectionID: inspection.ID, DisplayName: "Upstream", Scope: federation.Scope{Kinds: []catalog.Kind{catalog.KindAgent}}, RetrievalMode: federation.RetrievalIndexed, Reason: "Owned SDK fixture"})
	must(t, err)
	selectVersion := func(label string) (federation.Observation, federation.ObservationOccurrence) {
		t.Helper()
		f.clock.Advance(time.Second)
		set(label)
		_, err := f.federation.Sync(ctx, source.ID)
		must(t, err)
		items, _, err := f.sourceStore.ListCurrentObservations(ctx, tenant, source.ID, 10)
		must(t, err)
		if len(items) != 1 {
			t.Fatalf("current Source entries: %d", len(items))
		}
		occurrences, _, err := f.federation.ObservationOccurrences(ctx, items[0].ID, 10)
		must(t, err)
		if len(occurrences) != 1 {
			t.Fatalf("complete occurrences: %d", len(occurrences))
		}
		return items[0], occurrences[0]
	}
	old, occurrence := selectVersion("opaque-first")
	first, err := f.federation.AdoptObservation(ctx, old.ID, federation.AdoptInput{NamespaceID: ns.ID, PublisherID: pub.ID, SnapshotID: occurrence.SnapshotID, RetrievalDigest: occurrence.RetrievalDigest, EntryLocation: occurrence.EntryLocation, DisplayName: "My local title", Description: "Keep my custom description", Visibility: catalog.VisibilityPrivate})
	must(t, err)
	next, occurrence := selectVersion("opaque-next")
	resource, err := f.service.GetResource(ctx, first.Resource.ID)
	must(t, err)
	listing, err := f.service.GetListing(ctx, first.Listing.ID)
	must(t, err)
	revision, _, _, err := f.service.GetVersionRevision(ctx, first.VersionRevision.ID)
	must(t, err)
	t.Cleanup(func() {
		versions, err := f.service.ListVersions(ctx, first.Resource.ID, catalog.HistoryPageRequest{Limit: 10})
		must(t, err)
		if len(versions) != 2 {
			t.Errorf("SDK append/replay left %d Versions, want two", len(versions))
		}
		afterResource, err := f.service.GetResource(ctx, first.Resource.ID)
		must(t, err)
		afterListing, err := f.service.GetListing(ctx, first.Listing.ID)
		must(t, err)
		afterRevision, _, _, err := f.service.GetVersionRevision(ctx, first.VersionRevision.ID)
		must(t, err)
		if !reflect.DeepEqual(resource, afterResource) || !reflect.DeepEqual(listing, afterListing) || !reflect.DeepEqual(revision, afterRevision) {
			t.Error("SDK append changed parent, Listing or prior immutable revision")
		}
	})
	return versionAdoptionCase{Origin: f.server.URL, ResourceID: first.Resource.ID, PriorVersionRevisionID: first.VersionRevision.ID, SourceID: source.ID, Body: federation.AdoptVersionInput{ObservationID: next.ID, SnapshotID: occurrence.SnapshotID, RetrievalDigest: occurrence.RetrievalDigest, EntryLocation: occurrence.EntryLocation, PriorVersionRevisionID: first.VersionRevision.ID}}
}

func versionAdoptionEnvironment(t *testing.T) string {
	t.Helper()
	data, err := json.Marshal(newVersionAdoptionCase(t))
	must(t, err)
	return "CATALOG_SDK_VERSION_ADOPTION=" + string(data)
}
