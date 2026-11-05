// Unit tests exercise isolated validation and controlled dependencies.
package httpserver

import (
	"encoding/json"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/adapter"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
)

func conformingARDSearchRecord(tenantID, name string, index int, now time.Time) catalog.CatalogRecord {
	identifier := "urn:air:catalog.example.com:agent:" + name
	return catalog.CatalogRecord{
		Resource: catalog.Resource{ID: "resource-" + name, TenantID: tenantID, CanonicalIdentifier: identifier, Kind: catalog.KindAgent},
		Version:  catalog.ResourceVersion{ID: "version-" + name, Version: "1.0.0"},
		VersionRevision: catalog.VersionRevision{Representations: []catalog.Representation{{
			Role: catalog.RepresentationPrimary, MediaType: adapter.MediaA2AAgentCard, Data: json.RawMessage(`{"name":"` + name + `"}`), ValidationState: "valid",
		}}},
		ListingRevision: catalog.ListingRevision{DisplayName: name + " resumable agent", Description: "A governed resumable local result.", RepresentativeQueries: []string{"Find a resumable agent", "Show resumable agents"}, Visibility: catalog.VisibilityPublic},
		Release:         catalog.ListingRelease{ID: "release-" + name, ActivatedAt: now.Add(-time.Hour), UpdatedAt: now.Add(time.Duration(index) * time.Minute)},
		Evidence:        catalog.EvidenceSet{State: catalog.EvidenceComplete, LegalRights: catalog.LegalRightsSummary{State: catalog.LegalRightsNotAssessed}},
	}
}
