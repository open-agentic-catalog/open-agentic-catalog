package publication

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/standards"
)

func TestListingLicenseAndSubjectProjectionPreservesRetainedData(t *testing.T) {
	for _, tc := range []struct {
		name, license string
		included      bool
	}{{"empty", "", false}, {"declared label", "Apache-2.0", true}, {"Unicode limit", strings.Repeat("é", 256), true}, {"Unicode over limit", strings.Repeat("é", 257), false}} {
		t.Run(tc.name, func(t *testing.T) {
			record := eligiblePublicationRecord()
			record.ListingRevision.License = tc.license
			record.ListingRevision.Categories = []string{"finance", "tools"}
			record.ListingRevision.Extensions = map[string]any{"private-note": "never published"}
			before, _ := json.Marshal(record)
			manager := &Manager{baseURL: "https://catalog.example.com", audience: AudienceAnonymous}
			entry, _, reasons := manager.baseEntry(record)
			if len(reasons) != 0 || !reflect.DeepEqual(entry.Subjects, []string{"finance", "tools"}) {
				t.Fatalf("eligible projection failed: %+v, reasons=%v", entry, reasons)
			}
			gotLicense, present := entry.Metadata["license"]
			if present != tc.included || present && gotLicense != tc.license {
				t.Fatalf("license changed or omission failed: %v, %t", gotLicense, present)
			}
			entry.RepresentativeQueries = record.ListingRevision.RepresentativeQueries
			raw, err := json.Marshal(entry)
			if err != nil {
				t.Fatal(err)
			}
			if err := standards.ValidateARDEntry(raw); err != nil {
				t.Fatalf("projection breaks pinned schema: %v", err)
			}
			if strings.Contains(string(raw), "private-note") || !reflect.DeepEqual(entry.Data, record.VersionRevision.Representations[0].Data) {
				t.Fatal("private extensions leaked or native data changed")
			}
			after, _ := json.Marshal(record)
			if string(before) != string(after) {
				t.Fatal("retained record changed")
			}
			record.ListingRevision.Categories[0] = "changed draft"
			if entry.Subjects[0] != "finance" {
				t.Fatal("published topics alias mutable listing data")
			}
		})
	}
}

func TestListingFiltersAndFacetsReadOnlyActiveSnapshot(t *testing.T) {
	records := make([]catalog.CatalogRecord, 0, 4)
	for index := range 4 {
		record := eligiblePublicationRecord()
		record.Resource.CanonicalIdentifier = fmt.Sprintf("urn:air:catalog.example.com:default:agent-%d", index)
		record.Release.ID = fmt.Sprintf("release-%d", index)
		record.ListingRevision.License = []string{"Apache-2.0", "Apache-2.0", "MIT", ""}[index]
		record.ListingRevision.Categories = [][]string{{"finance", "tools"}, {"finance"}, {"finance"}, nil}[index]
		records = append(records, record)
	}
	manager, _, _ := newPublicationManager(t, records)
	active := activatePublicationSnapshot(t, manager)
	// Published reads must not fall through to mutable authoring records.
	manager.records = failingPublicationRecords{err: fmt.Errorf("unexpected authoring read")}
	query := SearchQuery{Text: "Published agent", Context: map[string]any{"topic.example": "http://purl.org/dc/terms/"}, Filter: map[string]any{"topic.example:subject": "finance", "metadata.license": "Apache-2.0"}}
	request := SearchRequest{Query: query, Federation: "none", PageSize: 1}
	first, err := manager.Search(t.Context(), "tenant-1", request)
	if err != nil || len(first.Results) != 1 || first.PageToken == "" {
		t.Fatalf("filtered first page = %+v, %v", first, err)
	}
	request.PageToken = first.PageToken
	second, err := manager.Search(t.Context(), "tenant-1", request)
	if err != nil || len(second.Results) != 1 || second.Results[0].Identifier == first.Results[0].Identifier || second.PageToken != "" {
		t.Fatalf("filtered continuation = %+v, %v", second, err)
	}
	request.Query.Filter = map[string]any{standards.ARDSubjectIRI: "finance", "metadata.license": "MIT"}
	if _, err := manager.Search(t.Context(), "tenant-1", request); catalog.ErrorCodeOf(err) != catalog.CodeInvalidArgument {
		t.Fatalf("continuation accepted changed license: %v", err)
	}
	explored, err := manager.Explore(t.Context(), "tenant-1", ExploreRequest{Query: &query, ResultType: ExploreResultType{Facets: []ExploreFacetRequest{{Field: "topic.example:subject"}, {Field: "metadata.license"}}}})
	if err != nil {
		t.Fatal(err)
	}
	if got := explored.Facets[standards.ARDSubjectIRI].Buckets; !reflect.DeepEqual(got, []ExploreBucket{{Value: "finance", Count: 2}, {Value: "tools", Count: 1}}) {
		t.Fatalf("topic facets include filtered-out or missing declarations: %+v", got)
	}
	if got := explored.Facets["metadata.license"].Buckets; !reflect.DeepEqual(got, []ExploreBucket{{Value: "Apache-2.0", Count: 2}}) {
		t.Fatalf("license facets = %+v", got)
	}
	before, err := json.Marshal(active)
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := manager.Active(t.Context(), "tenant-1")
	if err != nil {
		t.Fatal(err)
	}
	after, err := json.Marshal(loaded)
	if err != nil || string(before) != string(after) {
		t.Fatalf("query rewrote retained publication: %v", err)
	}
}

func TestEarlierProjectionRemainsReadableButRequiresRegenerationForActivation(t *testing.T) {
	record := eligiblePublicationRecord()
	record.ListingRevision.License = "Apache-2.0"
	record.ListingRevision.Categories = []string{"finance"}
	manager, store, clock := newPublicationManager(t, []catalog.CatalogRecord{record})
	candidate, err := manager.BuildCandidate(publicationContext(t.Context(), "tenant-1"), "tenant-1")
	if err != nil {
		t.Fatal(err)
	}
	// Model the previous emitter's exact shape without altering retained records.
	for index := range candidate.ARD.Entries {
		candidate.ARD.Entries[index].Subjects = nil
		candidate.ARD.Entries[index].Context = ARDContext
		delete(candidate.ARD.Entries[index].Metadata, "license")
	}
	for index := range candidate.AICatalog.Entries {
		metadata, ok := candidate.AICatalog.Entries[index].Extensions[aiCatalogMetadataExtension].(map[string]any)
		if !ok {
			t.Fatal("fixture needs AI Catalog metadata")
		}
		delete(metadata, "license")
	}
	rehashSnapshotDocuments(t, &candidate)
	if err := manager.validateSnapshotStructure(candidate); err != nil {
		t.Fatalf("earlier projection lost structural validity: %v", err)
	}
	if err := manager.validateSnapshotEligibility(t.Context(), candidate); catalog.ErrorCodeOf(err) != catalog.CodePublicationNotReady {
		t.Fatalf("activation ignored changed publication projection: %v", err)
	}
	candidate.State, candidate.ActivatedAt = SnapshotActive, &clock.now
	store.snapshots[memorySnapshotKey("tenant-1", candidate.ID)] = candidate
	store.active["tenant-1"] = candidate.ID
	manager.records = failingPublicationRecords{err: fmt.Errorf("unexpected authoring read")}
	loaded, err := manager.Active(context.Background(), "tenant-1")
	if err != nil || loaded.ETag != candidate.ETag || !reflect.DeepEqual(loaded.ARD, candidate.ARD) {
		t.Fatalf("active prior projection was rewritten or refused: %+v, %v", loaded, err)
	}
}

func TestLicenseSubjectFiltersShareExactFacetValues(t *testing.T) {
	entry := Entry{Identifier: "urn:air:catalog.example:one", Subjects: []string{"tools", "finance", "finance"}, Metadata: map[string]any{"license": "Apache-2.0"}}
	context := map[string]any{"topic.example": "http://purl.org/dc/terms/"}
	filter, err := ResolveSearchFilter(SearchQuery{Context: context, Filter: map[string]any{"topic.example:subject": []string{"other", "finance"}, "metadata.license": "Apache-2.0"}})
	if err != nil || !matchFilter(entry, filter, nil) {
		t.Fatalf("OR within / AND across declarations failed: %v, %v", filter, err)
	}
	if got := facetValues(entry, standards.ARDSubjectIRI, nil); !reflect.DeepEqual(got, []string{"finance", "tools"}) {
		t.Fatalf("facet values must count each topic once per entry: %v", got)
	}
	filter["metadata.license"] = "MIT"
	if matchFilter(entry, filter, nil) {
		t.Fatal("topic match bypassed license constraint")
	}
	for _, fields := range []map[string]any{
		{"topic.example:subject": "finance", standards.ARDSubjectIRI: "finance"},
		{"data.skills.name": "forecast"}, {"topic.example:subject.nested": "finance"},
	} {
		if _, err := ResolveSearchFilter(SearchQuery{Context: context, Filter: fields}); catalog.ErrorCodeOf(err) != catalog.CodeInvalidArgument {
			t.Fatalf("duplicate aliases or unselected paths accepted: %v", err)
		}
	}
}
