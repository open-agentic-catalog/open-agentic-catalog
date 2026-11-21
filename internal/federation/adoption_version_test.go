package federation

import (
	"context"
	"crypto/sha256"
	"fmt"
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/adapter"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/clock"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/fetcher"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/id"
	"github.com/open-agentic-catalog/open-agentic-catalog/test/identityfixture"
)

func TestVersionAdoptionUsesHistoricalProofAndCurrentPolicy(t *testing.T) {
	const endpoint = "https://source.example/ard.json"
	store := NewMemoryStore()
	clk := clock.NewFake(time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC))
	repo := catalog.NewMemoryRepository()
	store.BindLocalCatalog(repo)
	key := sha256.Sum256([]byte(t.Name()))
	service, err := catalog.NewService(catalog.Options{Repository: repo, Clock: clk, IDs: id.Generator{}, Validator: adapter.NewRegistry(), CursorKey: key[:], Policy: "version-test"})
	if err != nil {
		t.Fatal(err)
	}
	ctx := identityfixture.WithActor(t.Context(), identity.Actor{TenantID: "00000000-0000-7000-8000-000000000001", Subject: "curator", RealmID: "00000000-0000-7000-8000-000000000090", MembershipID: "00000000-0000-7000-8000-000000000091", ActorType: identity.ActorHuman, Roles: []identity.Role{identity.RoleTenantAdmin}})
	ns, err := service.CreateNamespace(ctx, catalog.CreateNamespaceInput{Slug: "adopt", DisplayName: "Adopt"})
	if err != nil {
		t.Fatal(err)
	}
	publisher, err := service.CreatePublisher(ctx, catalog.CreatePublisherInput{DisplayName: "Local", Domains: []string{"source.example"}})
	if err != nil {
		t.Fatal(err)
	}
	outbound := &routingFetcher{results: map[string]fetcher.Result{}}
	set := func(label string) {
		outbound.results["GET "+endpoint] = fetcher.Result{FinalURL: endpoint, RequestedURL: endpoint, Status: 200, MediaType: "application/ard+json", Body: []byte(fmt.Sprintf(`{"entries":[{"identifier":"urn:air:source.example:agents:versioned","version":%q,"displayName":"Upstream","description":"Metadata","type":"application/a2a-agent-card+json","data":{"name":"Upstream","version":%q}}]}`, label, label)), FetchedAt: clk.Now()}
	}
	set("old-opaque")
	manager, err := NewManager(store, outbound, clk, id.Generator{}, WithObservationAdopter(service), WithVersionAdoption(true))
	if err != nil {
		t.Fatal(err)
	}

	inspection, err := manager.Inspect(ctx, endpoint)
	if err != nil {
		t.Fatal(err)
	}
	source, err := manager.Connect(ctx, ConnectInput{InspectionID: inspection.ID, DisplayName: "Upstream", Scope: Scope{Kinds: []catalog.Kind{catalog.KindAgent}}, RetrievalMode: RetrievalIndexed, Reason: "Unit fixture"})
	if err != nil {
		t.Fatal(err)
	}
	selected := func(label string) (Observation, ObservationOccurrence) {
		t.Helper()
		clk.Advance(time.Second)
		set(label)
		if _, err := manager.Sync(ctx, source.ID); err != nil {
			t.Fatal(err)
		}
		obs, _, err := store.ListCurrentObservations(ctx, "00000000-0000-7000-8000-000000000001", source.ID, 10)
		if err != nil || len(obs) != 1 {
			t.Fatal(obs, err)
		}
		o, _, err := store.ListObservationOccurrences(ctx, "00000000-0000-7000-8000-000000000001", obs[0].ID, 10)
		if err != nil || len(o) != 1 {
			t.Fatal(o, err)
		}
		return obs[0], o[0]
	}
	old, occ := selected("old-opaque")
	first, err := manager.AdoptObservation(ctx, old.ID, AdoptInput{NamespaceID: ns.ID, PublisherID: publisher.ID, SnapshotID: occ.SnapshotID, RetrievalDigest: occ.RetrievalDigest, EntryLocation: occ.EntryLocation})
	if err != nil {
		t.Fatal(err)
	}
	next, occ := selected("next-opaque")
	before, err := store.GetObservation(ctx, "00000000-0000-7000-8000-000000000001", old.ID)
	if err != nil || before.Freshness != FreshWithdrawn {
		t.Fatal("old entry not withdrawn", err)
	}
	target, err := manager.VersionAdoptionTarget(ctx, next.ID)
	if err != nil || target.ResourceID != first.Resource.ID {
		t.Fatal(target, err)
	}
	contacts := len(outbound.requests)
	rows, err := manager.UpstreamChanges(ctx, UpstreamChangesQuery{Check: "all", Limit: 1})
	if err != nil || len(rows) != 1 || rows[0].Target != target || rows[0].Occurrence != occ || contacts != len(outbound.requests) {
		t.Fatal("inventory changed source contacts or exact selection", rows, err)
	}
	// A harmless policy revision does not rewrite selected provenance revision 1.
	changed := store.sources[source.ID]
	changed.Revision++
	store.sources[source.ID] = changed
	input := AdoptVersionInput{ObservationID: next.ID, SnapshotID: occ.SnapshotID, RetrievalDigest: occ.RetrievalDigest, EntryLocation: occ.EntryLocation, PriorVersionRevisionID: target.PriorVersionRevisionID}
	result, err := manager.AdoptVersion(ctx, target.ResourceID, input)
	if err != nil || result.VersionRevision.Adoption.SourceRevision != 1 || result.Version.Version != "next-opaque" {
		t.Fatal("historical proof/current admission", result, err)
	}
	if _, err := manager.AdoptVersion(ctx, target.ResourceID, input); catalog.ErrorCodeOf(err) != catalog.CodeConflict {
		t.Fatal("duplicate", err)
	}
	for _, mutation := range []string{"same-label", "wrong-source"} {
		selected := next
		switch mutation {
		case "same-label":
			selected.Version = old.Version
		case "wrong-source":
			selected.SourceID = "other"
		}
		proof := catalog.AdoptionProvenance{ObservationID: old.ID, SourceID: source.ID}
		if err := manager.admitPriorVersion(ctx, "00000000-0000-7000-8000-000000000001", proof, selected); catalog.ErrorCodeOf(err) != catalog.CodePreconditionFailed {
			t.Fatal(mutation, err)
		}
	}

	original := store.observations[observationKey(next)]
	unversioned := original
	unversioned.VersionPresent = false
	store.observations[observationKey(next)] = unversioned
	if _, err := manager.AdoptVersion(ctx, target.ResourceID, input); catalog.ErrorCodeOf(err) != catalog.CodePreconditionFailed {
		t.Fatal("unversioned admitted", err)
	}
	store.observations[observationKey(next)] = original
	changed.Scope.ExcludedIdentifiers = []string{next.Identifier}
	store.sources[source.ID] = changed
	if _, err := manager.AdoptVersion(ctx, target.ResourceID, input); catalog.ErrorCodeOf(err) != catalog.CodePreconditionFailed {
		t.Fatal("exclusion", err)
	}
	disabled, err := NewManager(store, outbound, clk, id.Generator{}, WithObservationAdopter(service))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := disabled.AdoptVersion(ctx, target.ResourceID, input); catalog.ErrorCodeOf(err) != catalog.CodeForbidden {
		t.Fatal("default enabled", err)
	}
	if _, err := disabled.VersionAdoptionTarget(ctx, next.ID); catalog.ErrorCodeOf(err) != catalog.CodeForbidden {
		t.Fatal("disabled target leaked", err)
	}
	reader := identityfixture.WithActor(context.Background(), identity.Actor{TenantID: "00000000-0000-7000-8000-000000000001", Subject: "reader", Roles: []identity.Role{identity.RoleReader}})
	if _, err := manager.AdoptVersion(reader, target.ResourceID, input); catalog.ErrorCodeOf(err) != catalog.CodeForbidden {
		t.Fatal("reader appended", err)
	}
	if (*Manager)(nil).VersionAdoptionEnabled() {
		t.Fatal("nil manager enabled")
	}
}
