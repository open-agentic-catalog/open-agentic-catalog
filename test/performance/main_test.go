// Unit tests exercise isolated validation and controlled dependencies.
package main

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
)

func validPage(t *testing.T) []byte {
	t.Helper()
	page := catalog.CatalogSearchResponse{DurableTotal: 3, Generation: "1", Profile: catalog.CoreSearchProfile, Tier: "core"}
	for i := range 3 {
		page.Items = append(page.Items, catalog.CatalogSearchResult{ID: strconv.Itoa(i), Type: catalog.SearchResultLocal, Local: new((catalog.CatalogRecord{Evidence: catalog.EvidenceSet{LegalRights: catalog.LegalRightsSummary{State: catalog.LegalRightsNotAssessed}}, Resource: catalog.Resource{TenantID: "00000000-0000-7000-8000-000000000001"}}).Summary().SearchLocal())})
	}
	raw, err := json.Marshal(page)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestPerformanceProfileAndBaselineContracts(t *testing.T) {
	valid := profile{Port: 8080, Seconds: 1, Workers: 1, MaxRequests: 100}
	for _, p := range []profile{{}, {Port: 0, Seconds: 1, Workers: 1, MaxRequests: 1}, {Port: 8080, Seconds: 3601, Workers: 1, MaxRequests: 100}, {Port: 8080, Seconds: 1, Workers: 9, MaxRequests: 100}, {Port: 8080, Seconds: 1, Workers: 2, MaxRequests: 1}} {
		if p.validate() == nil {
			t.Fatal("invalid profile accepted")
		}
	}
	if valid.validate() != nil {
		t.Fatal("valid profile rejected")
	}
	raw := validPage(t)
	if _, err := validateBaseline(raw); err != nil {
		t.Fatal(err)
	}
	for _, body := range [][]byte{[]byte(`{}`), []byte(`{"items":[],"items":[]}`), append(append([]byte{}, raw...), raw...), []byte(strings.Replace(string(raw), "00000000-0000-7000-8000-000000000001", "00000000-0000-7000-8000-000000000002", 1)), []byte(strings.Replace(string(raw), `"partial":false`, `"partial":true`, 1))} {
		if _, err := validateBaseline(body); err == nil {
			t.Fatal("invalid baseline accepted")
		}
	}
}
