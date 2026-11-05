//go:build minikube

package main

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/observability"
)

func TestInstrumentationRequiresLiveRuntimeAndWorkerEvidence(t *testing.T) {
	body := observability.New(nil).Render(t.Context())
	response := httpResult{Status: 200, Header: http.Header{"Content-Type": {"text/plain; version=0.0.4"}}, Body: body}
	if err := validateInstrumentation(response, false); err != nil {
		t.Fatal(err)
	}
	if err := validateInstrumentation(response, true); err == nil {
		t.Fatal("API-only exposition accepted as a worker snapshot")
	}
	response.Body = append(response.Body, []byte("open_agentic_catalog_operational_snapshot_available 1\nopen_agentic_catalog_job_oldest_runnable_age_seconds 0\nopen_agentic_catalog_outbox_oldest_pending_age_seconds 0\n")...)
	if err := validateInstrumentation(response, true); err != nil {
		t.Fatal(err)
	}
	for _, change := range [][2]string{
		{"open_agentic_catalog_go_heap_bytes ", "missing_heap "},
		{`metric="open_agentic_catalog_go_goroutines"} 1`, `metric="open_agentic_catalog_go_goroutines"} 0`},
		{"snapshot_available 1", "snapshot_available 0"},
		{"oldest_pending_age_seconds 0", "oldest_pending_age_seconds NaN"},
	} {
		bad := response
		bad.Body = []byte(strings.ReplaceAll(string(response.Body), change[0], change[1]))
		if err := validateInstrumentation(bad, true); err == nil {
			t.Fatalf("invalid instrumentation accepted: %s", change[0])
		}
	}
	for _, bad := range []httpResult{{Status: 503}, {Status: 200, Header: http.Header{"Content-Type": {"text/html"}}}, {Status: 200, Header: response.Header, Body: make([]byte, (1<<20)+1)}} {
		if err := validateInstrumentation(bad, false); err == nil {
			t.Fatal("invalid metric envelope accepted")
		}
	}
}

func TestInstrumentationMetricParsingRejectsMissingDuplicateAndNonFinite(t *testing.T) {
	for _, body := range []string{"", "# only a comment\n", "metric NaN\n", "metric +Inf\n", "metric -1\n", "metric private-data\n", "metric 1 extra\n", "metric 1\nmetric 2\n"} {
		if _, err := fixedMetric([]byte(body), "metric"); err == nil {
			t.Fatalf("accepted malformed metric %q", body)
		}
	}
	if value, err := fixedMetric([]byte("# HELP metric description\nmetric 0.5\n"), "metric"); err != nil || value != 0.5 {
		t.Fatalf("valid metric: %v %v", value, err)
	}
}

func TestInstrumentationSearchRequiresCompleteTenantBoundedBaseline(t *testing.T) {
	page := catalog.CatalogSearchResponse{Generation: "1", DurableTotal: 3}
	for _, id := range []string{"first", "second", "third"} {
		page.Items = append(page.Items, catalog.CatalogSearchResult{ID: id, Local: new((catalog.CatalogRecord{Resource: catalog.Resource{TenantID: primaryTenantID}, Evidence: catalog.EvidenceSet{LegalRights: catalog.LegalRightsSummary{State: catalog.LegalRightsNotAssessed}}}).Summary().SearchLocal())})
	}
	encode := func() httpResult {
		raw, err := json.Marshal(page)
		if err != nil {
			t.Fatal(err)
		}
		return httpResult{Status: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: raw}
	}
	if !validInstrumentationSearch(encode()) {
		t.Fatal("valid baseline rejected")
	}
	page.Items[1].ID = page.Items[0].ID
	if validInstrumentationSearch(encode()) {
		t.Fatal("duplicate result accepted")
	}
	page.Items[1].ID = "second"
	page.Items[1].Local.Resource.TenantID = otherTenantID
	if validInstrumentationSearch(encode()) {
		t.Fatal("cross-tenant result accepted")
	}
	page.Items[1].Local.Resource.TenantID = primaryTenantID
	page.NextCursor = "next"
	if validInstrumentationSearch(encode()) {
		t.Fatal("partial seed accepted")
	}
}
