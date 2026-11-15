//go:build functional

package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/adapter"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/publication"
)

type previewRecords []catalog.CatalogRecord

func (records previewRecords) PublicationRecords(context.Context, string, bool, int) ([]catalog.CatalogRecord, bool, error) {
	return records, false, nil
}

type previewTimeIDs struct{ id string }

func (previewTimeIDs) Now() time.Time                      { return time.Unix(123, 0).UTC() }
func (value previewTimeIDs) New(time.Time) (string, error) { return value.id, nil }

type observedPreviewStore struct {
	*publication.MemoryStore
	host    string
	visits  *[]string
	failure error
}

func (store *observedPreviewStore) ActiveForWithdrawalPreview(ctx context.Context, tenant string, maxBytes int) (publication.Snapshot, int, error) {
	*store.visits = append(*store.visits, store.host)
	if store.failure != nil {
		return publication.Snapshot{}, 0, store.failure
	}
	return store.MemoryStore.ActiveForWithdrawalPreview(ctx, tenant, maxBytes)
}

// Real in-memory candidate construction and activation qualify the composed
// coordinator/manager boundary. This workflow is deliberately functional.
func TestWithdrawalPreviewTraversesOwnedHostsAndDiscardsEarlierMatchesOnFailure(t *testing.T) {
	const tenant = "00000000-0000-7000-8000-000000000001"
	const release = "00000000-0000-7000-8000-000000000101"
	policy, err := identity.NewMemoryStore([]identity.Seed{{TenantID: tenant, Issuer: "urn:preview-flow", Subject: "publisher", ActorType: identity.ActorHuman, RoleTemplates: []string{"publication_admin"}}})
	if err != nil {
		t.Fatal(err)
	}
	actor, err := policy.ResolvePrincipal(t.Context(), identity.Principal{TenantID: tenant, Issuer: "urn:preview-flow", Subject: "publisher"})
	if err != nil {
		t.Fatal(err)
	}
	ctx := identity.WithActor(t.Context(), identity.BindPermissionStore(actor, policy))
	records := previewRecords{{
		Resource:        catalog.Resource{ID: "00000000-0000-7000-8000-000000000102", TenantID: tenant, CanonicalIdentifier: "urn:air:catalog.example.com:default:preview", Kind: catalog.KindAgent},
		Version:         catalog.ResourceVersion{Version: "1.0.0"},
		VersionRevision: catalog.VersionRevision{Representations: []catalog.Representation{{Role: catalog.RepresentationPrimary, MediaType: adapter.MediaA2AAgentCard, Data: json.RawMessage(`{"name":"Preview fixture"}`), ValidationState: "valid"}}},
		ListingRevision: catalog.ListingRevision{DisplayName: "Preview fixture", Visibility: catalog.VisibilityPublic},
		Release:         catalog.ListingRelease{ID: release, State: catalog.ReleasePublished, ActivatedAt: time.Unix(100, 0).UTC(), UpdatedAt: time.Unix(100, 0).UTC()},
		Evidence:        catalog.EvidenceSet{State: catalog.EvidenceComplete, LegalRights: catalog.LegalRightsSummary{State: catalog.LegalRightsNotAssessed}},
	}}
	var visits []string
	stores := make([]*observedPreviewStore, 0, 2)
	snapshots := make([]publication.Snapshot, 0, 2)
	coordinator := &publicationWithdrawalCoordinator{managers: map[string]publicationBinding{}}
	for index, host := range []string{"a.example.com", "z.example.com"} {
		ids := previewTimeIDs{id: []string{"00000000-0000-7000-8000-000000000201", "00000000-0000-7000-8000-000000000202"}[index]}
		store := &observedPreviewStore{MemoryStore: publication.NewMemoryStore(), host: host, visits: &visits}
		manager, err := publication.NewManager(publication.Options{Records: records, Store: store, Clock: ids, IDs: ids, BaseURL: "https://" + host, Host: host, PublisherDomain: "catalog.example.com", ARDProfile: "ard-test", AICatalogProfile: "ai-test", CursorKey: make([]byte, 32)})
		if err != nil {
			t.Fatal(err)
		}
		candidate, err := manager.BuildCandidate(ctx, tenant)
		if err != nil {
			t.Fatal(err)
		}
		active, err := manager.Activate(ctx, tenant, candidate.ID, "Owned preview baseline")
		if err != nil {
			t.Fatal(err)
		}
		coordinator.managers[host] = publicationBinding{tenantID: tenant, manager: manager}
		stores = append(stores, store)
		snapshots = append(snapshots, active)
	}
	empty, _ := newPublicationJobManager(t, "m.empty.example.com", func(context.Context, string) error { t.Fatal("preview loaded mutable records"); return nil })
	coordinator.managers["m.empty.example.com"] = publicationBinding{tenantID: tenant, manager: empty}
	// A foreign binding is deliberately unusable: it must never be visited.
	coordinator.managers["foreign.example.com"] = publicationBinding{tenantID: "00000000-0000-7000-8000-000000000002"}
	rows, err := coordinator.PreviewPublicationRelease(ctx, tenant, release)
	if err != nil || len(rows) != 2 || rows[0].SnapshotID != snapshots[0].ID || rows[1].SnapshotID != snapshots[1].ID || !slices.Equal(visits, []string{"a.example.com", "z.example.com"}) {
		t.Fatal("complete ordered preview", rows, visits, err)
	}
	visits = nil
	failure := catalog.E(catalog.CodeDependencyUnavailable, "owned later-host read failure")
	stores[1].failure = failure
	rows, err = coordinator.PreviewPublicationRelease(ctx, tenant, release)
	if rows != nil || !errors.Is(err, failure) || !slices.Equal(visits, []string{"a.example.com", "z.example.com"}) {
		t.Fatal("later failure became partial success", rows, visits, err)
	}
	for index, store := range stores {
		active, err := store.Active(ctx, tenant)
		if err != nil || !reflect.DeepEqual(active, snapshots[index]) {
			t.Fatal("preview failure changed active publication", active, err)
		}
	}
}
