// Typed response envelopes retain the OpenAPI disclosure boundary instead of
// serializing unconstrained internal state.

package httpserver

import (
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/jobs"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/processinfo"
)

// These response envelopes mirror the service-owned OpenAPI schemas. Keeping
// them typed prevents a handler from silently growing a public response with
// internal fields.
type serviceStatusResponse struct {
	Status string `json:"status"`
}

type serviceVersionResponse struct {
	Service          string `json:"service"`
	Version          string `json:"version"`
	Commit           string `json:"commit"`
	SourceState      string `json:"sourceState"`
	ARDProfile       string `json:"ardProfile"`
	AICatalogProfile string `json:"aiCatalogProfile"`
}

type serviceInformationResponse struct {
	serviceVersionResponse
	processinfo.Snapshot
	Role string `json:"role"`
}

type versionEvidenceResponse struct {
	Version  catalog.ResourceVersion `json:"version"`
	Revision catalog.VersionRevision `json:"revision"`
	Evidence catalog.EvidenceSet     `json:"evidence"`
}

type revisionEvidenceResponse struct {
	NamespaceID string                  `json:"namespaceId"`
	Revision    catalog.VersionRevision `json:"revision"`
	Evidence    catalog.EvidenceSet     `json:"evidence"`
}

type listingRevisionCreatedResponse struct {
	Listing  catalog.CatalogListing  `json:"listing"`
	Revision catalog.ListingRevision `json:"revision"`
}

type reviewDecisionResponse struct {
	Submission catalog.ReviewSubmission `json:"submission"`
	Version    catalog.ResourceVersion  `json:"version"`
}

type listingPublishedResponse struct {
	Listing      catalog.CatalogListing  `json:"listing"`
	Release      catalog.ListingRelease  `json:"release"`
	PriorRelease *catalog.ListingRelease `json:"priorRelease"`
}

type sourceSyncAcceptedResponse struct {
	Job      jobs.View `json:"job"`
	Existing bool      `json:"existing"`
}
