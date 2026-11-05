package catalog

import (
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/test/identityfixture"
)

func TestAdoptionProjectionSelectsOnlyReadableRetainedImports(t *testing.T) {
	const tenant = "00000000-0000-7000-8000-000000000001"
	const namespace = "00000000-0000-7000-8000-000000000002"
	const private = "00000000-0000-7000-8000-000000000003"
	actor := identity.Actor{TenantID: tenant, Subject: "reader", ActorType: identity.ActorHuman, Namespaces: map[string][]identity.Role{namespace: {identity.RoleReader}}}
	ctx, _, err := identity.WithCollectionContext(identityfixture.WithActor(t.Context(), actor))
	if err != nil {
		t.Fatal(err)
	}
	repository := NewMemoryRepository()
	air := "urn:air:example.com:agents:imported"
	for _, r := range []Resource{{ID: "readable", TenantID: tenant, NamespaceID: namespace, CanonicalIdentifier: air, Kind: KindAgent}, {ID: "hidden", TenantID: tenant, NamespaceID: private, CanonicalIdentifier: air, Kind: KindAgent}, {ID: "other", TenantID: private, NamespaceID: namespace, CanonicalIdentifier: air, Kind: KindAgent}, {ID: "unrelated", TenantID: tenant, NamespaceID: namespace, CanonicalIdentifier: "urn:air:example.com:agents:other", Kind: KindAgent}} {
		repository.resources[r.ID] = r
		repository.versions[r.ID] = ResourceVersion{ID: r.ID, TenantID: r.TenantID, ResourceID: r.ID}
		repository.versionRevisions[r.ID] = VersionRevision{ID: r.ID, TenantID: r.TenantID, ResourceVersionID: r.ID, Adoption: &AdoptionProvenance{SourceID: "source", SourceRevision: 2, ObservationID: "retained", NormalizedEntryDigest: "exact-digest"}}
	}
	repository.versionRevisions["non-import"] = VersionRevision{ID: "non-import", TenantID: tenant, ResourceVersionID: "readable"}
	candidates, err := repository.FindAdoptions(ctx, tenant, []string{air})
	if err != nil || len(candidates) != 1 || candidates[0].ResourceID != "readable" || candidates[0].Provenance.SourceRevision != 2 {
		t.Fatal(candidates, err)
	}
	now := time.Date(2026, 10, 8, 0, 0, 0, 0, time.UTC)
	r := repository.resources["readable"]
	r.DeletedAt = &now
	repository.resources[r.ID] = r
	candidates, err = repository.FindAdoptions(ctx, tenant, []string{air})
	if err != nil || len(candidates) != 0 {
		t.Fatal("deleted import disclosed", candidates, err)
	}
	if _, err = repository.FindAdoptions(ctx, tenant, make([]string, 101)); err == nil {
		t.Fatal("unbounded request")
	}
	if _, err = repository.FindAdoptions(ctx, private, []string{air}); err == nil {
		t.Fatal("cross tenant read")
	}
}
