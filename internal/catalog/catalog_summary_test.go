package catalog

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestCatalogSummaryAllowlistAndMaximumPageBytes(t *testing.T) {
	// '<' requires six JSON bytes, more than any UTF-8 scalar. Both common
	// Search fields and the local projection count toward the complete envelope.
	tags := make([]string, 32)
	for i := range tags {
		// All 80 characters require six JSON bytes; five binary positions
		// make the 32 tags unique without reducing the encoded bound.
		characters := []byte(strings.Repeat("<", 80))
		for bit := 0; bit < 5; bit++ {
			if i&(1<<bit) != 0 {
				characters[bit] = '>'
			}
		}
		tags[i] = string(characters)
	}
	if err := validateListingDiscoveryMetadata(tags, nil, nil); err != nil {
		t.Fatal(err)
	}
	uuid := "00000000-0000-7000-8000-000000000001"
	identifier := "urn:air:example.com:" + strings.Repeat(strings.Repeat("n", 64)+":", 7) + strings.Repeat("x", 37)
	if _, err := ParseAIRIdentifier(identifier); err != nil || len(identifier) != 512 {
		t.Fatalf("invalid maximum identifier fixture: %v", err)
	}
	at := time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)
	full := CatalogRecord{RelevanceScore: 100,
		Display:         NewCatalogPresentation(strings.Repeat("<", 160), strings.Repeat("<", 160), strings.Repeat("<", 256)),
		Resource:        Resource{ID: uuid, TenantID: uuid, NamespaceID: uuid, Kind: KindAgent, CanonicalIdentifier: identifier},
		Version:         ResourceVersion{ID: uuid, Version: strings.Repeat("<", 128)},
		VersionRevision: VersionRevision{ID: uuid, ContentSetDigest: "sha256:" + strings.Repeat("a", 64), EvidenceSetID: uuid, Representations: []Representation{{Data: json.RawMessage(`{"private":"retained document"}`)}}},
		Listing:         CatalogListing{ID: uuid, NamespaceID: uuid},
		ListingRevision: ListingRevision{ID: uuid, DisplayName: strings.Repeat("<", 160), Description: strings.Repeat("<", 4000), Tags: tags, PublisherID: uuid, Visibility: VisibilityPublic, Extensions: map[string]any{"private": "extension"}, Categories: []string{"category"}, License: "private retained license", RepresentativeQueries: []string{"private ranking query"}},
		Release:         ListingRelease{ID: uuid, VersionRevisionID: uuid, ListingRevisionID: uuid, State: ReleasePublished, ActivatedAt: at},
		Evidence:        EvidenceSet{ID: uuid, Digest: "sha256:" + strings.Repeat("b", 64), State: EvidenceComplete, Trust: TrustSummary{State: TrustNotAssessed, Reason: "private trust reason"}, LegalRights: LegalRightsSummary{State: LegalRightsNotAssessed}, Findings: []Finding{{Message: "private finding"}}},
	}
	summary := full.Summary()
	if summary.ListingRevision.License != full.ListingRevision.License {
		t.Fatal("summary lost the exact license needed for local filtering")
	}
	raw, err := json.Marshal(summary)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"private", "representations", "findings", "extensions", "categories", "representativeQueries", "reason"} {
		if strings.Contains(string(raw), forbidden) {
			t.Fatalf("detail field leaked: %s", forbidden)
		}
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil {
		t.Fatal(err)
	}
	if len(object) != 10 {
		t.Fatalf("unexpected summary member count: %d", len(object))
	}
	page := Page[CatalogSummary]{Items: make([]CatalogSummary, 100), Total: 101, NextCursor: strings.Repeat("c", MaxCatalogCursorBytes)}
	search := CatalogSearchResponse{Items: make([]CatalogSearchResult, 100), DurableTotal: 101, NextCursor: page.NextCursor, Profile: "core-lexical-v1", Generation: "sha256:" + strings.Repeat("c", 64)}
	for i := range page.Items {
		page.Items[i] = summary
		search.Items[i] = localCatalogSearchResult(summary)
	}
	for name, value := range map[string]any{"browse": page, "search": search} {
		// Include the HTTP data/meta wrapper and maximum request identifier.
		encoded, err := json.Marshal(map[string]any{"data": value, "meta": map[string]string{"requestId": strings.Repeat("r", 128)}})
		if err != nil || len(encoded) > 8<<20 {
			t.Fatalf("%s response bytes=%d: %v", name, len(encoded), err)
		}
		t.Logf("%s maximum local page: %d bytes", name, len(encoded))
	}
	summary.ListingRevision.Tags[0] = "changed"
	summary.ListingRevision.Categories[0] = "changed"
	summary.ListingRevision.RepresentativeQueries[0] = "changed"
	if full.ListingRevision.Tags[0] != strings.Repeat("<", 80) || full.ListingRevision.Categories[0] != "category" || full.ListingRevision.RepresentativeQueries[0] != "private ranking query" {
		t.Fatal("summary aliases retained metadata")
	}
}

func TestCatalogPresentationExactBoundAndSearchRoundTrip(t *testing.T) {
	for _, tc := range []struct {
		license string
		omitted bool
	}{
		{"", false}, {"MIT", false}, {strings.Repeat("𐐀", 256), false}, {strings.Repeat("𐐀", 257), true},
	} {
		display := NewCatalogPresentation("Current publisher", "Listing namespace", tc.license)
		want := tc.license
		if tc.omitted {
			want = ""
		}
		if display.License != want || display.LicenseOmitted != tc.omitted {
			t.Fatalf("wrong bounded declaration: %+v", display)
		}
		summary := CatalogSummary{Display: display}
		result := localCatalogSearchResult(summary)
		back := result.Local.browseSummary(result)
		if back.Display != display || back.Display.PublisherName != "Current publisher" || back.Display.NamespaceName != "Listing namespace" {
			t.Fatal("display lost in Search/browse conversion")
		}
	}
}

func TestMemoryCatalogPresentationUsesExactTenantDirectoryLabels(t *testing.T) {
	repo := NewMemoryRepository()
	repo.resources["resource"] = Resource{ID: "resource", TenantID: "tenant", NamespaceID: "resource-namespace"}
	repo.versions["version"] = ResourceVersion{ID: "version", ResourceID: "resource"}
	repo.versionRevisions["revision"] = VersionRevision{ID: "revision", ResourceVersionID: "version", EvidenceSetID: "evidence"}
	repo.evidenceSets["evidence"] = EvidenceSet{ID: "evidence"}
	repo.listingRevisions["listing-revision"] = ListingRevision{ID: "listing-revision", PublisherID: "publisher", License: "MIT"}
	repo.releases["release"] = ListingRelease{ID: "release", State: ReleasePublished, VersionRevisionID: "revision", ListingRevisionID: "listing-revision"}
	listing := CatalogListing{ID: "listing", TenantID: "tenant", NamespaceID: "listing-namespace", ResourceID: "resource", CurrentReleaseID: "release"}
	repo.publishers["publisher"] = Publisher{ID: "publisher", TenantID: "tenant", DisplayName: "Current publisher"}
	repo.namespaces["listing-namespace"] = Namespace{ID: "listing-namespace", TenantID: "tenant", DisplayName: "Listing namespace"}
	repo.namespaces["resource-namespace"] = Namespace{ID: "resource-namespace", TenantID: "tenant", DisplayName: "Different resource namespace"}
	record, ok := repo.catalogRecordLocked("tenant", listing, false)
	if !ok || record.Display.PublisherName != "Current publisher" || record.Display.NamespaceName != "Listing namespace" || record.Display.License != "MIT" {
		t.Fatalf("wrong release-linked labels: %+v", record.Display)
	}
	// Current names may change; previously returned values remain independent.
	repo.publishers["publisher"] = Publisher{ID: "publisher", TenantID: "other", DisplayName: "Cross-tenant private name"}
	repo.namespaces["listing-namespace"] = Namespace{ID: "listing-namespace", TenantID: "other", DisplayName: "Cross-tenant private namespace"}
	second, ok := repo.catalogRecordLocked("tenant", listing, false)
	if !ok || second.Display.PublisherName != "" || second.Display.NamespaceName != "" || record.Display.PublisherName != "Current publisher" {
		t.Fatal("directory lookup leaked another tenant or mutated prior result")
	}
}
