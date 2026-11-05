// Explicit adoption creates a complete local draft while retaining the selected
// external observation as provenance rather than local approval.

package catalog

import (
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
)

// AdoptExternalObservation creates one complete local draft atomically. The
// selected external evidence remains provenance; it never becomes approval.
func (s *Service) AdoptExternalObservation(ctx context.Context, input ExternalObservationAdoptionInput) (ExternalObservationAdoption, error) {
	ctx = identity.WithPermissionContext(ctx)
	actor, err := requirePermission(ctx, "resource.create", input.NamespaceID)
	if err != nil {
		return ExternalObservationAdoption{}, err
	}
	identifier, err := ParseAIRIdentifier(strings.TrimSpace(input.CanonicalIdentifier))
	if err != nil || !input.Kind.Valid() {
		return ExternalObservationAdoption{}, E(CodeInvalidArgument, "adoption requires a supported kind and normalized urn:air identifier")
	}
	input.CanonicalIdentifier = identifier.String()
	input.Version = strings.TrimSpace(input.Version)
	input.DisplayName = strings.TrimSpace(input.DisplayName)
	input.Description = strings.TrimSpace(input.Description)
	if input.NamespaceID == "" || input.Version == "" || !validTextLength(input.Version, 128) || input.DisplayName == "" || !validTextLength(input.DisplayName, 160) || input.Description == "" || !validTextLength(input.Description, 4000) {
		return ExternalObservationAdoption{}, E(CodeInvalidArgument, "namespace, local version, display name, and description are required for adoption")
	}
	if input.Visibility == "" {
		input.Visibility = VisibilityPrivate
	}
	if !input.Visibility.Valid() || input.PublisherID == "" {
		return ExternalObservationAdoption{}, E(CodeInvalidArgument, "adoption requires a publisher and valid visibility")
	}
	if err := validateListingDiscoveryMetadata(input.Tags, input.Categories, input.RepresentativeQueries); err != nil {
		return ExternalObservationAdoption{}, err
	}
	for _, candidate := range []string{input.DocumentationURL, input.SupportURL, input.TermsURL} {
		if candidate != "" && !ValidDocumentationHTTPSURL(candidate) {
			return ExternalObservationAdoption{}, E(CodeInvalidArgument, "listing URL is not a safe HTTPS reference")
		}
	}
	if err := validateAdoptionProvenance(input.Provenance); err != nil {
		return ExternalObservationAdoption{}, err
	}
	if _, err := s.repo.GetNamespace(ctx, actor.TenantID, input.NamespaceID); err != nil {
		if ErrorCodeOf(err) != CodeNotFound {
			return ExternalObservationAdoption{}, err
		}
		return ExternalObservationAdoption{}, E(CodeInvalidArgument, "adoption namespace does not exist in the tenant")
	}
	if _, err := s.repo.GetPublisher(ctx, actor.TenantID, input.PublisherID); err != nil {
		if ErrorCodeOf(err) != CodeNotFound {
			return ExternalObservationAdoption{}, err
		}
		return ExternalObservationAdoption{}, E(CodeInvalidArgument, "publisher does not exist in the tenant")
	}

	now := s.clock.Now()
	resourceID, err := s.ids.New(now)
	if err != nil {
		return ExternalObservationAdoption{}, Wrap(CodeInternal, "create adopted resource identifier", err)
	}
	versionID, err := s.ids.New(now.Add(time.Nanosecond))
	if err != nil {
		return ExternalObservationAdoption{}, Wrap(CodeInternal, "create adopted version identifier", err)
	}
	resource := Resource{ID: resourceID, TenantID: actor.TenantID, NamespaceID: input.NamespaceID, CanonicalIdentifier: input.CanonicalIdentifier, Kind: input.Kind, CreatedBy: actor.Subject, Revision: 1, CreatedAt: now, UpdatedAt: now}
	version := ResourceVersion{ID: versionID, TenantID: actor.TenantID, ResourceID: resource.ID, Version: input.Version, Revision: 1, CreatedAt: now, UpdatedAt: now}
	revision, evidence, documents, err := s.buildVersionRevision(ctx, actor, resource.Kind, version.ID, version.Version, 1, now, []RepresentationInput{input.Representation})
	if err != nil {
		return ExternalObservationAdoption{}, err
	}
	input.Provenance.AdoptedBy = actor.Subject
	input.Provenance.AdoptedAt = now
	revision.Adoption = &input.Provenance
	version.CandidateRevisionID = revision.ID

	listingID, err := s.ids.New(now.Add(2 * time.Nanosecond))
	if err != nil {
		return ExternalObservationAdoption{}, Wrap(CodeInternal, "create adopted listing identifier", err)
	}
	listingRevisionID, err := s.ids.New(now.Add(3 * time.Nanosecond))
	if err != nil {
		return ExternalObservationAdoption{}, Wrap(CodeInternal, "create adopted listing revision identifier", err)
	}
	listing := CatalogListing{ID: listingID, TenantID: actor.TenantID, NamespaceID: input.NamespaceID, ResourceID: resource.ID, CandidateRevisionID: listingRevisionID, Revision: 2, CreatedAt: now, UpdatedAt: now}
	listingRevision := ListingRevision{
		ID: listingRevisionID, TenantID: actor.TenantID, ListingID: listing.ID, RevisionNumber: 1, VersionRevisionID: revision.ID,
		DisplayName: input.DisplayName, Description: input.Description, Tags: normalizeTerms(input.Tags, 32), Categories: normalizeTerms(input.Categories, 16), RepresentativeQueries: normalizeQueries(input.RepresentativeQueries),
		PublisherID: input.PublisherID, DocumentationURL: input.DocumentationURL, SupportURL: input.SupportURL, License: strings.TrimSpace(input.License), TermsURL: input.TermsURL,
		Visibility: input.Visibility, CreatedBy: actor.Subject, CreatedAt: now,
	}
	listingRevision.Digest, err = digestJSON(revisionDigestInput(listingRevision))
	if err != nil {
		return ExternalObservationAdoption{}, Wrap(CodeInternal, "digest adopted listing revision", err)
	}
	result := ExternalObservationAdoption{Resource: resource, Version: version, VersionRevision: revision, Evidence: evidence, Listing: listing, ListingRevision: listingRevision}
	audit, outbox, err := s.evidence(ctx, actor, "observation.adopted", "resource", resource.ID, result)
	if err != nil {
		return ExternalObservationAdoption{}, err
	}
	params := AdoptExternalObservationParams{SourceAdmissionRevision: input.SourceAdmissionRevision, Resource: resource, Version: version, VersionRevision: revision, Documents: documents, Evidence: evidence, Listing: listing, ListingRevision: listingRevision, Audit: audit, Outbox: outbox}
	if input.AdmitCommit == nil {
		return s.repo.AdoptExternalObservation(ctx, params)
	}
	var adopted ExternalObservationAdoption
	err = input.AdmitCommit(ctx, func(ctx context.Context) error {
		var commitErr error
		adopted, commitErr = s.repo.AdoptExternalObservation(ctx, params)
		return commitErr
	})
	return adopted, err
}

// validateAdoptionProvenance checks bounded source, snapshot, digest and
// occurrence bindings required to retain an external origin.
func validateAdoptionProvenance(provenance AdoptionProvenance) error {
	if provenance.ObservationID == "" || provenance.SourceID == "" || provenance.SourceRevision <= 0 || provenance.SnapshotID == "" || !sha256DigestPattern.MatchString(provenance.RetrievalDigest) || !validAdoptionEntryLocation(provenance.EntryLocation) || !sha256DigestPattern.MatchString(provenance.ExactEntryDigest) || !sha256DigestPattern.MatchString(provenance.NormalizedEntryDigest) || provenance.NormalizationProfile != "catalog-observation-v1" {
		return E(CodeInvalidArgument, "exact complete observation provenance is required for adoption")
	}
	return nil
}

// validAdoptionEntryLocation reports whether the retained source-entry location
// uses the supported bounded spelling.
func validAdoptionEntryLocation(value string) bool {
	if len(value) > 1024 || value != strings.TrimSpace(value) {
		return false
	}
	// Manifest entries and current Registry List items both retain exact
	// occurrence locations. Neither is a predecessor compatibility shape.
	for _, prefix := range []string{"/entries/", "/items/"} {
		if strings.HasPrefix(value, prefix) {
			index, err := strconv.ParseUint(strings.TrimPrefix(value, prefix), 10, 64)
			return err == nil && value == prefix+strconv.FormatUint(index, 10)
		}
	}
	return false
}
