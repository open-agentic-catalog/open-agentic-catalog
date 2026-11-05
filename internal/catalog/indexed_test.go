package catalog

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
)

func TestIndexedLookupIdentityAndDisclosure(t *testing.T) {
	repo := NewMemoryRepository()
	selected := IndexedIdentity{Identifier: "urn:air:remote.example:default:agent", Version: strings.Repeat("🙂", 128), VersionPresent: true}
	item := CatalogSearchResult{ID: IndexedSearchResultID(selected.Identifier, selected.Version, true), Identifier: selected.Identifier, Version: selected.Version, VersionPresent: true, Type: SearchResultIndexed, Indexed: &IndexedSearchEvidence{}}
	page := FederatedCatalogSearchPage{Indexed: []CatalogSearchResult{item}}
	var calls int
	var upstreamErr error
	service := newUnifiedSearchTestService(t, repo, unifiedFederatedSearcher{search: func(ctx context.Context, request FederatedCatalogSearchRequest) (FederatedCatalogSearchPage, error) {
		calls++
		actor, _ := identity.FromContext(ctx)
		if actor.TenantID != "tenant-1" || request.ExactIdentity == nil || *request.ExactIdentity != selected || request.IncludeLive || request.Limit != 1 || request.Query.Text != "" || !reflect.DeepEqual(request.Query.Origins, []SearchResultType{SearchResultIndexed}) {
			t.Fatal("lookup did not retain an exact bounded tenant-scoped request")
		}
		return page, upstreamErr
	}})
	ctx := unifiedReaderContext()
	if got, err := service.GetIndexed(ctx, selected); err != nil || got.ID != item.ID {
		t.Fatalf("lookup: %#v / %v", got, err)
	}
	for _, invalid := range []IndexedIdentity{{Identifier: "not-air"}, {Identifier: selected.Identifier, Version: "1"}, {Identifier: selected.Identifier, Version: selected.Version + "x", VersionPresent: true}, {Identifier: selected.Identifier, Version: "bad\x00text", VersionPresent: true}, {Identifier: selected.Identifier, Version: "bad\xfftext", VersionPresent: true}} {
		if _, err := service.GetIndexed(ctx, invalid); ErrorCodeOf(err) != CodeInvalidArgument {
			t.Fatalf("invalid identity: %v", err)
		}
	}
	if _, err := service.GetIndexed(t.Context(), selected); ErrorCodeOf(err) != CodeForbidden || calls != 1 {
		t.Fatalf("unauthorized lookup reached storage: %v calls=%d", err, calls)
	}
	page.Indexed = nil
	if _, err := service.GetIndexed(ctx, selected); ErrorCodeOf(err) != CodeNotFound {
		t.Fatalf("missing projection: %v", err)
	}
	upstreamErr = errors.New("store unavailable")
	if _, err := service.GetIndexed(ctx, selected); !errors.Is(err, upstreamErr) {
		t.Fatalf("store failure: %v", err)
	}
	upstreamErr = nil
	page.Indexed = []CatalogSearchResult{item}
	page.Indexed[0].Live = &LiveSearchEvidence{}
	if got, err := service.GetIndexed(ctx, selected); ErrorCodeOf(err) != CodeInternal || got.ID != "" {
		t.Fatalf("mixed private/transient projection disclosed: %#v %v", got, err)
	}
	// An explicit indexed bookmark remains usable when a local copy exists.
	repo.resources["resource"] = Resource{ID: "resource", TenantID: "tenant-1", CanonicalIdentifier: selected.Identifier}
	repo.listings["listing"] = CatalogListing{ID: "listing", TenantID: "tenant-1", ResourceID: "resource", CurrentReleaseID: "release"}
	repo.versions["version"] = ResourceVersion{ID: "version", TenantID: "tenant-1", ResourceID: "resource", Version: selected.Version}
	repo.versionRevisions["revision"] = VersionRevision{ID: "revision", TenantID: "tenant-1", ResourceVersionID: "version"}
	repo.releases["release"] = ListingRelease{ID: "release", TenantID: "tenant-1", ListingID: "listing", VersionRevisionID: "revision", State: ReleasePublished}
	if found, err := repo.HasPublishedIdentity(ctx, "tenant-1", selected.Identifier, selected.Version); err != nil || !found {
		t.Fatalf("matching local fixture is not discoverable: %t %v", found, err)
	}
	page.Indexed[0].Live = nil
	before := calls
	if got, err := service.GetIndexed(ctx, selected); err != nil || got.ID != item.ID || calls != before+1 {
		t.Fatalf("indexed bookmark: %+v %v calls=%d", got, err, calls)
	}
	if found, err := repo.HasPublishedIdentity(ctx, "other", selected.Identifier, selected.Version); err != nil || found {
		t.Fatalf("cross-tenant precedence: %t %v", found, err)
	}
}
