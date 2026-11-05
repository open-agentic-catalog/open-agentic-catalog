package catalog

import (
	"context"
	"slices"
	"strings"
	"testing"
)

func TestDirectoryFilterNormalization(t *testing.T) {
	ns, err := (NamespaceListRequest{Slug: " Tools-1 ", DisplayName: " Mixed  Case ", AfterSlug: "before", AfterID: "position", Limit: 2}).NormalizeFilters()
	if err != nil || ns != (NamespaceListRequest{Slug: "tools-1", DisplayName: " Mixed  Case ", AfterSlug: "before", AfterID: "position", Limit: 2}) {
		t.Fatal(ns, err)
	}
	unicodeSlug, unicodeErr := (NamespaceListRequest{Slug: "\u2028\u0130\u212a\u3000"}).NormalizeFilters()
	if unicodeErr != nil || unicodeSlug.Slug != "ik" {
		t.Fatal(unicodeSlug, unicodeErr)
	}
	pub, err := (PublisherListRequest{DeclaredDomain: " CATALOG.Example. ", DisplayName: "日本語", AfterDisplayName: "Before", AfterID: "position", Limit: 2}).NormalizeFilters()
	if err != nil || pub != (PublisherListRequest{DeclaredDomain: "catalog.example", DisplayName: "日本語", AfterDisplayName: "Before", AfterID: "position", Limit: 2}) {
		t.Fatal(pub, err)
	}
	for _, tc := range []struct{ raw, normalized string }{
		{"\u2028\u0130\u212a.Example.\u3000", "ik.example"},
		{strings.Repeat("a", 63) + "." + strings.Repeat("b", 63) + "." + strings.Repeat("c", 63) + "." + strings.Repeat("d", 61), strings.Repeat("a", 63) + "." + strings.Repeat("b", 63) + "." + strings.Repeat("c", 63) + "." + strings.Repeat("d", 61)},
	} {
		normalized, err := (PublisherListRequest{DeclaredDomain: tc.raw}).NormalizeFilters()
		if err != nil || normalized.DeclaredDomain != tc.normalized {
			t.Fatal(normalized, err)
		}
	}
	for _, name := range []string{" ", strings.Repeat("界", 161), "bad\x00", "\xff"} {
		if _, err := (NamespaceListRequest{DisplayName: name}).NormalizeFilters(); ErrorCodeOf(err) != CodeInvalidArgument {
			t.Fatal(name, err)
		}
		if _, err := (PublisherListRequest{DisplayName: name}).NormalizeFilters(); ErrorCodeOf(err) != CodeInvalidArgument {
			t.Fatal(name, err)
		}
	}
	for _, slug := range []string{" ", "-bad", "bad_1", strings.Repeat("s", 65)} {
		if _, err := (NamespaceListRequest{Slug: slug}).NormalizeFilters(); ErrorCodeOf(err) != CodeInvalidArgument {
			t.Fatal(slug, err)
		}
	}
	for _, domain := range []string{" ", "https://catalog.example", "*.example", "bad..example", strings.Repeat("x", 254)} {
		if _, err := (PublisherListRequest{DeclaredDomain: domain}).NormalizeFilters(); ErrorCodeOf(err) != CodeInvalidArgument {
			t.Fatal(domain, err)
		}
	}
	if _, err := (NamespaceListRequest{DisplayName: strings.Repeat("界", 160)}).NormalizeFilters(); err != nil {
		t.Fatal(err)
	}
	if _, err := (PublisherListRequest{}).NormalizeFilters(); err != nil {
		t.Fatal(err)
	}
}

// Repository selection stays before the page bound, preserves duplicate names
// and exact byte ordering, and never promotes domain approval into declaration.
func TestMemoryDirectoryExactFilters(t *testing.T) {
	r := NewMemoryRepository()
	const tenant = "00000000-0000-7000-8000-000000000001"
	for i, slug := range []string{"aaa", "bbb", "ccc"} {
		id := []string{"a", "b", "c"}[i]
		r.namespaces[id] = Namespace{ID: id, TenantID: tenant, Slug: slug, DisplayName: "Shared"}
		r.publishers[id] = Publisher{ID: id, TenantID: tenant, DisplayName: "Shared", Domains: []string{slug + ".example"}, ApprovedPublicationDomains: []string{"approval.example"}}
	}
	r.namespaces["foreign"] = Namespace{ID: "foreign", TenantID: "foreign", Slug: "bbb", DisplayName: "Shared"}
	r.publishers["foreign"] = Publisher{ID: "foreign", TenantID: "foreign", DisplayName: "Shared", Domains: []string{"bbb.example"}}
	for _, tc := range []struct {
		request NamespaceListRequest
		ids     []string
	}{
		{NamespaceListRequest{Slug: "ccc", Limit: 1}, []string{"c"}},
		{NamespaceListRequest{Slug: "ccc", DisplayName: "Shared", Limit: 1}, []string{"c"}},
		{NamespaceListRequest{DisplayName: "Shared", Limit: 2}, []string{"a", "b"}},
		{NamespaceListRequest{DisplayName: "Shared", AfterSlug: "aaa", AfterID: "a", Limit: 2}, []string{"b", "c"}},
		{NamespaceListRequest{Slug: "ccc", DisplayName: "shared", Limit: 1}, nil},
	} {
		items, err := r.ListNamespaces(context.Background(), tenant, tc.request)
		ids := make([]string, 0, len(items))
		for _, item := range items {
			ids = append(ids, item.ID)
		}
		if err != nil || !slices.Equal(ids, tc.ids) {
			t.Fatal(tc.request, ids, err)
		}
	}
	for _, tc := range []struct {
		request PublisherListRequest
		ids     []string
	}{
		{PublisherListRequest{DeclaredDomain: "ccc.example", Limit: 1}, []string{"c"}},
		{PublisherListRequest{DeclaredDomain: "ccc.example", DisplayName: "Shared", Limit: 1}, []string{"c"}},
		{PublisherListRequest{DisplayName: "Shared", Limit: 2}, []string{"a", "b"}},
		{PublisherListRequest{DisplayName: "Shared", AfterDisplayName: "Shared", AfterID: "a", Limit: 2}, []string{"b", "c"}},
		{PublisherListRequest{DisplayName: "shared", DeclaredDomain: "ccc.example", Limit: 1}, nil},
		{PublisherListRequest{DeclaredDomain: "approval.example", Limit: 1}, nil},
	} {
		items, err := r.ListPublisherPage(context.Background(), tenant, tc.request)
		ids := make([]string, 0, len(items))
		for _, item := range items {
			ids = append(ids, item.ID)
		}
		if err != nil || !slices.Equal(ids, tc.ids) {
			t.Fatal(tc.request, ids, err)
		}
	}
}
