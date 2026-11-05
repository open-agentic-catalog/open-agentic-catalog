package catalog

import (
	"context"
	"testing"
)

func TestReleaseTransitionMatrixIncludesExplicitSupersession(t *testing.T) {
	for _, from := range []ReleaseState{ReleasePublished, ReleaseDeprecated} {
		if !validReleaseTransition(from, ReleaseSuperseded) {
			t.Fatalf("%s release could not be explicitly superseded", from)
		}
	}
	if validReleaseTransition(ReleaseRetired, ReleaseSuperseded) || validReleaseTransition(ReleaseRevoked, ReleaseSuperseded) {
		t.Fatal("terminal release could be superseded")
	}
}

func TestMemoryRepositoryRejectsCrossNamespaceListing(t *testing.T) {
	repository := NewMemoryRepository()
	repository.namespaces["namespace-a"] = Namespace{ID: "namespace-a", TenantID: "tenant"}
	repository.namespaces["namespace-b"] = Namespace{ID: "namespace-b", TenantID: "tenant"}
	repository.resources["resource-a"] = Resource{ID: "resource-a", TenantID: "tenant", NamespaceID: "namespace-a"}
	_, err := repository.CreateListing(context.Background(), CreateListingParams{Listing: CatalogListing{ID: "listing", TenantID: "tenant", NamespaceID: "namespace-b", ResourceID: "resource-a"}})
	if ErrorCodeOf(err) != CodeInvalidArgument {
		t.Fatalf("cross-namespace listing returned %v", err)
	}
}
