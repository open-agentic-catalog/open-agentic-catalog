package catalog

import (
	"time"
	"unicode/utf8"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/artwork"
)

// CatalogPresentation contains current release-linked directory labels and a
// bounded exact listing-license declaration, never directory authority or trust.
type CatalogPresentation struct {
	PublisherName  string `json:"publisherName,omitempty"`
	NamespaceName  string `json:"namespaceName,omitempty"`
	License        string `json:"license,omitempty"`
	LicenseOmitted bool   `json:"licenseOmitted,omitempty"`
}

const MaxCompactLicenseCharacters = 256

// NewCatalogPresentation preserves complete retained declarations separately.
func NewCatalogPresentation(publisher, namespace, license string) *CatalogPresentation {
	display := &CatalogPresentation{PublisherName: publisher, NamespaceName: namespace}
	if utf8.RuneCountInString(license) > MaxCompactLicenseCharacters {
		display.LicenseOmitted = true
	} else {
		display.License = license
	}
	return display
}

// CatalogSummary is the collection projection of a current release. Exact
// records, documents, validation findings and package inventories are separate
// authorized detail reads. Private fields support in-memory ranking/facets only.
type CatalogSummary struct {
	Artwork         *artwork.Presentation  `json:"artwork,omitempty"`
	Display         *CatalogPresentation   `json:"display,omitempty"`
	Resource        CatalogResourceSummary `json:"resource"`
	Version         CatalogVersionSummary  `json:"version"`
	VersionRevision CatalogRevisionSummary `json:"versionRevision"`
	Listing         CatalogListingSummary  `json:"listing"`
	ListingRevision CatalogDisplaySummary  `json:"listingRevision"`
	Release         CatalogReleaseSummary  `json:"release"`
	Evidence        CatalogEvidenceSummary `json:"evidence"`
	RelevanceScore  int                    `json:"relevanceScore,omitempty"`
	Source          string                 `json:"source"`
}

type CatalogResourceSummary struct {
	ID                  string `json:"id"`
	TenantID            string `json:"tenantId"`
	NamespaceID         string `json:"namespaceId"`
	CanonicalIdentifier string `json:"canonicalIdentifier"`
	Kind                Kind   `json:"kind"`
}

type CatalogVersionSummary struct {
	ID      string `json:"id"`
	Version string `json:"version"`
}

type CatalogRevisionSummary struct {
	ID               string `json:"id"`
	ContentSetDigest string `json:"contentSetDigest"`
	EvidenceSetID    string `json:"evidenceSetId"`
}

type CatalogListingSummary struct {
	ID          string `json:"id"`
	NamespaceID string `json:"namespaceId"`
}

type CatalogDisplaySummary struct {
	ID                    string     `json:"id"`
	DisplayName           string     `json:"displayName"`
	Description           string     `json:"description"`
	Tags                  []string   `json:"tags"`
	PublisherID           string     `json:"publisherId"`
	Visibility            Visibility `json:"visibility"`
	Categories            []string   `json:"-"`
	License               string     `json:"-"`
	RepresentativeQueries []string   `json:"-"`
}

type CatalogReleaseSummary struct {
	ID                string       `json:"id"`
	VersionRevisionID string       `json:"versionRevisionId"`
	ListingRevisionID string       `json:"listingRevisionId"`
	State             ReleaseState `json:"state"`
	ActivatedAt       time.Time    `json:"activatedAt"`
	Revision          int64        `json:"-"`
	UpdatedAt         time.Time    `json:"-"`
}

type CatalogEvidenceSummary struct {
	ID          string              `json:"id"`
	Digest      string              `json:"digest"`
	State       EvidenceState       `json:"state"`
	Trust       CatalogTrustSummary `json:"trust"`
	LegalRights LegalRightsSummary  `json:"legalRights"`
	ExpiresAt   time.Time           `json:"expiresAt,omitzero"`
}

// Summary copies only collection metadata. The memory repository calls this
// while holding its lock, before cloning any retained documents.
func (r CatalogRecord) Summary() CatalogSummary {
	return CatalogSummary{
		Artwork:         ListingArtwork(r.ListingRevision.Extensions),
		Display:         r.Display,
		Resource:        CatalogResourceSummary{ID: r.Resource.ID, TenantID: r.Resource.TenantID, NamespaceID: r.Resource.NamespaceID, CanonicalIdentifier: r.Resource.CanonicalIdentifier, Kind: r.Resource.Kind},
		Version:         CatalogVersionSummary{ID: r.Version.ID, Version: r.Version.Version},
		VersionRevision: CatalogRevisionSummary{ID: r.VersionRevision.ID, ContentSetDigest: r.VersionRevision.ContentSetDigest, EvidenceSetID: r.VersionRevision.EvidenceSetID},
		Listing:         CatalogListingSummary{ID: r.Listing.ID, NamespaceID: r.Listing.NamespaceID},
		ListingRevision: CatalogDisplaySummary{ID: r.ListingRevision.ID, DisplayName: r.ListingRevision.DisplayName, Description: r.ListingRevision.Description, Tags: append([]string{}, r.ListingRevision.Tags...), PublisherID: r.ListingRevision.PublisherID, Visibility: r.ListingRevision.Visibility, Categories: append([]string{}, r.ListingRevision.Categories...), License: r.ListingRevision.License, RepresentativeQueries: append([]string{}, r.ListingRevision.RepresentativeQueries...)},
		Release:         CatalogReleaseSummary{ID: r.Release.ID, VersionRevisionID: r.Release.VersionRevisionID, ListingRevisionID: r.Release.ListingRevisionID, State: r.Release.State, ActivatedAt: r.Release.ActivatedAt, Revision: r.Release.Revision, UpdatedAt: r.Release.UpdatedAt},
		Evidence:        CatalogEvidenceSummary{ID: r.Evidence.ID, Digest: r.Evidence.Digest, State: r.Evidence.State, LegalRights: LegalRightsSummary{State: r.Evidence.LegalRights.State}, Trust: CatalogTrustSummary{State: r.Evidence.Trust.State}, ExpiresAt: r.Evidence.ExpiresAt},
		RelevanceScore:  r.RelevanceScore, Source: "local",
	}
}

type CatalogTrustSummary struct {
	State TrustState `json:"state"`
}

// CatalogSearchLocal retains local identities and status. SearchResult already
// carries display text, tags, publisher and rank; repeating them can exceed the
// client ceiling for a legal 100-result page.
type CatalogSearchLocal struct {
	Display         *CatalogPresentation   `json:"display,omitempty"`
	Resource        CatalogResourceSummary `json:"resource"`
	Version         CatalogVersionSummary  `json:"version"`
	VersionRevision CatalogRevisionSummary `json:"versionRevision"`
	Listing         CatalogListingSummary  `json:"listing"`
	ListingRevision CatalogListingStatus   `json:"listingRevision"`
	Release         CatalogReleaseSummary  `json:"release"`
	Evidence        CatalogEvidenceSummary `json:"evidence"`
}
type CatalogListingStatus struct {
	ID         string     `json:"id"`
	Visibility Visibility `json:"visibility"`
}

func (s CatalogSummary) SearchLocal() CatalogSearchLocal {
	return CatalogSearchLocal{Display: s.Display, Resource: s.Resource, Version: s.Version, VersionRevision: s.VersionRevision, Listing: s.Listing,
		ListingRevision: CatalogListingStatus{ID: s.ListingRevision.ID, Visibility: s.ListingRevision.Visibility}, Release: s.Release, Evidence: s.Evidence}
}

// browseSummary reconstructs the local collection shape after hybrid ranking;
// the common Search envelope owns the display fields.
func (s CatalogSearchLocal) browseSummary(result CatalogSearchResult) CatalogSummary {
	return CatalogSummary{Artwork: result.Artwork, Display: s.Display, Resource: s.Resource, Version: s.Version, VersionRevision: s.VersionRevision, Listing: s.Listing,
		ListingRevision: CatalogDisplaySummary{ID: s.ListingRevision.ID, Visibility: s.ListingRevision.Visibility, DisplayName: result.DisplayName, Description: result.Description, Tags: result.Tags, PublisherID: result.Publisher},
		Release:         s.Release, Evidence: s.Evidence, Source: "local", RelevanceScore: result.Score}
}
