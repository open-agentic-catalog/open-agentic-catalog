// Unicode tag regressions cover administrator filters, scopes and retained
// indexed projections without network or database dependencies.
package federation

import (
	"github.com/open-agentic-catalog/open-agentic-catalog/test/identityfixture"
	"slices"
	"strings"
	"testing"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/clock"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/id"
)

// TestUnicodeTagScopesAndObservationSearch checks shared character semantics
// while retaining duplicate, whitespace, NUL and count rejection.
func TestUnicodeTagScopesAndObservationSearch(t *testing.T) {
	t.Parallel()
	store := NewMemoryStore()
	manager, err := NewManager(store, &routingFetcher{}, clock.Real{}, id.Generator{})
	if err != nil {
		t.Fatal(err)
	}
	const tenant = "00000000-0000-7000-8000-000000000001"
	store.sources["source"] = Source{ID: "source", TenantID: tenant, Status: SourceActive, RetrievalMode: RetrievalIndexed, Scope: Scope{Identifiers: []string{"urn:air:remote.example:default:one"}}}
	ctx := identityfixture.WithActor(t.Context(), identity.Actor{TenantID: tenant, Subject: "source-admin", Roles: []identity.Role{identity.RoleSourceAdmin}})
	for _, test := range []struct {
		name, tag string
		valid     bool
	}{
		{"accent", strings.Repeat("\u00e9", 80), true},
		{"supplementary", strings.Repeat("\U0001f642", 80), true},
		{"over", strings.Repeat("\u00e9", 81), false},
		{"NUL", "a\x00b", false},
		{"invalid UTF-8", "\xff", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := validateScope(Scope{Tags: []string{test.tag}})
			if (err == nil) != test.valid {
				t.Fatalf("scope valid=%t: %v", test.valid, err)
			}
			if validCatalogSearchProjection("Agent", "", []string{test.tag}, nil, "example.com") != test.valid {
				t.Fatal("projection character limit disagrees")
			}
			store.observations = map[string]Observation{"one": {ID: "one", TenantID: tenant, SourceID: "source", SourceKey: "one", Identifier: "urn:air:remote.example:default:one", Kind: catalog.KindAgent, NormalizedEntryDigest: "present", SearchTags: []string{test.tag}, Freshness: FreshCurrent}}
			page, err := manager.Search(ctx, SearchQuery{Tags: []string{test.tag}})
			if (err == nil) != test.valid {
				t.Fatalf("search valid=%t: %v", test.valid, err)
			}
			if test.valid && (len(page) != 1 || !slices.Equal(page[0].Observation.SearchTags, []string{test.tag})) {
				t.Fatalf("indexed tag lost: %+v", page)
			}
		})
	}
	for _, tags := range [][]string{{"duplicate", "duplicate"}, {" padded "}, make([]string, 101)} {
		if err := validateScope(Scope{Tags: tags}); err == nil {
			t.Fatal("scope accepted invalid collection")
		}
	}
	// The administrator query normalizes case and padding before matching, but
	// must still reject duplicate normalized tags and preserve tenant isolation.
	tag := strings.Repeat("\u00e9", 80)
	padded := " " + strings.ToUpper(tag) + " "
	store.observations = map[string]Observation{"one": {ID: "one", TenantID: tenant, SourceID: "source", SourceKey: "one", Identifier: "urn:air:remote.example:default:one", Kind: catalog.KindAgent, NormalizedEntryDigest: "present", SearchTags: []string{tag}, Freshness: FreshCurrent}}
	page, err := manager.Search(ctx, SearchQuery{Tags: []string{padded}})
	if err != nil || len(page) != 1 || !slices.Equal(page[0].Observation.SearchTags, []string{tag}) {
		t.Fatalf("normalized Unicode search: %+v, %v", page, err)
	}
	if _, err := manager.Search(ctx, SearchQuery{Tags: []string{padded, tag}}); catalog.ErrorCodeOf(err) != catalog.CodeInvalidArgument {
		t.Fatalf("normalized duplicate accepted: %v", err)
	}
	other := identityfixture.WithActor(t.Context(), identity.Actor{TenantID: "00000000-0000-7000-8000-000000000002", Subject: "source-admin", Roles: []identity.Role{identity.RoleSourceAdmin}})
	page, err = manager.Search(other, SearchQuery{Tags: []string{padded}})
	if err != nil || len(page) != 0 {
		t.Fatalf("cross-tenant Unicode search: %+v, %v", page, err)
	}
}
