// Reference corpus checks validate attributed wire examples, not peer deployments.
package httpserver

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/test/identityfixture"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/getkin/kin-openapi/openapi3"
	serviceapi "github.com/open-agentic-catalog/open-agentic-catalog/api"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/jsondoc"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/clock"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/id"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/publication"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/standards"
)

type ardReferenceCorpus struct {
	Profile string             `json:"profile"`
	Cases   []ardReferenceCase `json:"cases"`
}

type ardReferenceCase struct {
	Name        string          `json:"name"`
	Source      string          `json:"source"`
	Revision    string          `json:"revision"`
	Generation  string          `json:"generation"`
	Boundary    string          `json:"boundary"`
	Disposition string          `json:"disposition"`
	SourcePath  string          `json:"sourcePath"`
	License     string          `json:"license"`
	Evidence    string          `json:"evidence"`
	Note        string          `json:"note"`
	Value       json.RawMessage `json:"value"`
}

// TestARDReferenceCompatibilityCorpus checks pinned provenance and wire rules
// independently of the manager/HTTP exchange regressions.
func TestARDReferenceCompatibilityCorpus(t *testing.T) {
	raw, err := os.ReadFile("testdata/ard-reference-corpus.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := jsondoc.Validate(raw); err != nil {
		t.Fatalf("reference corpus is ambiguous JSON: %v", err)
	}
	var corpus ardReferenceCorpus
	if err := json.Unmarshal(raw, &corpus); err != nil {
		t.Fatal(err)
	}
	if corpus.Profile != "ard-0.91-aa3e598" || len(corpus.Cases) == 0 {
		t.Fatalf("reference corpus profile or case count drifted: profile=%q cases=%d", corpus.Profile, len(corpus.Cases))
	}
	document, err := openapi3.NewLoader().LoadFromData(serviceapi.ControlV1)
	if err != nil {
		t.Fatal(err)
	}
	publicationManager := referenceCorpusPublicationManager(t)
	expectedRevisions := map[string]string{
		"ards-project/ard-spec":                  "aa3e598bb7752a9175897823234311216acfa864",
		"a2aproject/A2A":                         "3303592588e388e62e0f69f701af531d2f4e3991",
		"huggingface/hf-discover":                "49c927439fcaa8f210cfd42186c0641acef579fa",
		"ards-project/connectors":                "53cc4f3a4596cf51482fabeb554d124ca248ed07",
		"agntcy/dir":                             "3bbfe8a8c1d795242c5fefbd63818558061dfd55",
		"ora.ai static OpenAPI":                  "1.24.0-retrieved-2026-09-13",
		"agentnameservice/ans":                   "83875d0572287821489c8dc5fc9cf37142ee35f0",
		"agentic-community/mcp-gateway-registry": "7d2f85b6674bebd65a69b57e89d4f3aea1631aa7",
	}
	seenNames := make(map[string]struct{}, len(corpus.Cases))
	for _, test := range corpus.Cases {
		if test.Name == "" || test.Source == "" || test.Revision == "" || test.Generation == "" || test.Boundary == "" || len(test.Value) == 0 || test.SourcePath == "" || test.License == "" || test.Note == "" {
			t.Fatalf("reference case metadata is incomplete: %#v", test)
		}
		if !slices.Contains([]string{"source-derived", "documented", "synthetic"}, test.Evidence) {
			t.Fatalf("reference case %q has unknown evidence class %q", test.Name, test.Evidence)
		}
		if _, duplicate := seenNames[test.Name]; duplicate {
			t.Fatalf("reference corpus contains duplicate case %q", test.Name)
		}
		seenNames[test.Name] = struct{}{}
		expectedRevision, expected := expectedRevisions[test.Source]
		if !expected {
			t.Fatalf("reference case %q uses an unreviewed source %q", test.Name, test.Source)
		}
		if test.Revision != expectedRevision {
			t.Fatalf("reference case %q uses revision %q for %s, want %q", test.Name, test.Revision, test.Source, expectedRevision)
		}
		t.Run(test.Name, func(t *testing.T) {
			accepted := evaluateARDReferenceCase(t, document, publicationManager, test)
			switch test.Disposition {
			case "accept":
				if !accepted {
					t.Fatalf("current-profile case from %s at %s was rejected", test.Source, test.Revision)
				}
			case "compatibility":
				if test.Boundary != "ai-catalog-compatibility" || !accepted {
					t.Fatalf("compatibility case from %s at %s did not pass only through the predecessor boundary", test.Source, test.Revision)
				}
			case "reject":
				if accepted {
					t.Fatalf("non-current case from %s at %s was accepted by %s", test.Source, test.Revision, test.Boundary)
				}
			default:
				t.Fatalf("reference case has unknown disposition %q", test.Disposition)
			}
		})
	}
	// Retain the specific attribution/type regressions, not a case-count quota
	// or an assertion that every listed product needs the same test matrix.
	for _, name := range []string{"agntcy-custom-list-envelope", "synthetic-unissued-offset-token", "hugging-face-nested-search", "ans-fresh-search", "ans-extra-stale-state", "gateway-inferred-terminal-null-token", "gateway-inferred-search-null-token", "synthetic-null-referrals"} {
		if _, present := seenNames[name]; !present {
			t.Errorf("reference corpus lost regression %q", name)
		}
	}
}

// evaluateARDReferenceCase runs only the validator named by each example.
func evaluateARDReferenceCase(t *testing.T, document *openapi3.T, manager *publication.Manager, test ardReferenceCase) bool {
	t.Helper()
	switch test.Boundary {
	case "ARDSearch", "ARDExplore", "ARDSearchResponse", "ARDExploreResponse", "ARDListResponse":
		schema := document.Components.Schemas[test.Boundary]
		if schema == nil || schema.Value == nil {
			t.Fatalf("OpenAPI schema %q is absent", test.Boundary)
		}
		var value any
		if err := json.Unmarshal(test.Value, &value); err != nil {
			t.Fatal(err)
		}
		return schema.Value.VisitJSON(value, openapi3.EnableJSONSchema2020()) == nil
	case "ard-entry":
		return standards.ValidateARDEntry(test.Value) == nil
	case "ai-catalog-compatibility":
		return standards.ValidateAICatalog(test.Value) == nil
	case "query-term":
		var term string
		if err := json.Unmarshal(test.Value, &term); err != nil {
			t.Fatal(err)
		}
		resolved, err := standards.ResolveARDTerm(term, nil)
		return err == nil && resolved == "type"
	case "native-disclosed-filter":
		var value struct {
			Entry json.RawMessage         `json:"entry"`
			Query publication.SearchQuery `json:"query"`
		}
		if err := json.Unmarshal(test.Value, &value); err != nil {
			t.Fatal(err)
		}
		var entry publication.Entry
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(value.Entry, &entry); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(value.Entry, &fields); err != nil {
			t.Fatal(err)
		}
		filter, err := publication.ResolveSearchFilter(value.Query)
		if err != nil {
			return false
		}
		matched, err := publication.MatchDisclosedFilter(t.Context(), entry, fields, filter)
		return err == nil && matched
	case "external-url":
		var value string
		if err := json.Unmarshal(test.Value, &value); err != nil {
			t.Fatal(err)
		}
		return catalog.ValidExternalHTTPSURL(value)
	case "issued-search-token":
		var token string
		if err := json.Unmarshal(test.Value, &token); err != nil {
			t.Fatal(err)
		}
		_, err := manager.Search(context.Background(), "00000000-0000-7000-8000-000000000001", publication.SearchRequest{Query: publication.SearchQuery{Text: "resumable"}, Federation: "auto", PageSize: 1, PageToken: token})
		return err == nil
	default:
		t.Fatalf("reference case has unknown boundary %q", test.Boundary)
		return false
	}
}

// referenceCorpusPublicationManager activates a small in-memory snapshot to
// test that arbitrary peer tokens are not locally issued Search continuations.
func referenceCorpusPublicationManager(t *testing.T) *publication.Manager {
	t.Helper()
	const tenantID = "00000000-0000-7000-8000-000000000001"
	now := time.Date(2026, time.September, 2, 12, 0, 0, 0, time.UTC)
	key := sha256.Sum256([]byte("ARD reference-corpus cursor"))
	manager, err := publication.NewManager(publication.Options{
		Records: fixedPublicationRecordSource{tenantID: tenantID, records: []catalog.CatalogRecord{
			conformingARDSearchRecord(tenantID, "corpus", 0, now),
		}},
		Store: publication.NewMemoryStore(), Clock: clock.NewFake(now), IDs: id.Generator{}, BaseURL: "https://127.0.0.1", PublisherDomain: "catalog.example.com",
		ARDProfile: "ard-0.91-aa3e598", AICatalogProfile: "ai-catalog-1.0-b062278", CursorKey: key[:], Host: "127.0.0.1", Audience: publication.AudienceAnonymous,
	})
	if err != nil {
		t.Fatal(err)
	}
	candidate, err := manager.BuildCandidate(identityfixture.WithActor(t.Context(), identity.Actor{TenantID: tenantID, Subject: "reference-publisher", Roles: []identity.Role{identity.RolePublisher}}), tenantID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Activate(identityfixture.WithActor(t.Context(), identity.Actor{TenantID: tenantID, Subject: "reference-publisher", Roles: []identity.Role{identity.RolePublisher}}), tenantID, candidate.ID, "Activate reviewed snapshot"); err != nil {
		t.Fatal(err)
	}
	return manager
}
