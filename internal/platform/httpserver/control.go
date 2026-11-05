// Control handlers decode governed authoring and search requests, apply
// concurrency preconditions and delegate transitions to Catalog services.

package httpserver

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/audit"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/jsondoc"
)

// handleNamespacesList returns a bounded slug/ID-ordered page with authorization-bound continuation.
func (s *Server) handleNamespacesList(writer http.ResponseWriter, request *http.Request) {
	if err := validateDirectoryQuery(request, "slug", "displayName"); err != nil {
		s.fail(writer, request, err)
		return
	}
	query, err := parseQueryParameters(request)
	if err != nil {
		s.fail(writer, request, err)
		return
	}
	listRequest, err := (catalog.NamespaceListRequest{Slug: query.Get("slug"), DisplayName: query.Get("displayName")}).NormalizeFilters()
	if err != nil {
		s.fail(writer, request, err)
		return
	}
	pageRequest, err := s.parseAdminValues(request.Context(), query, "namespaces", "", "slug,id", map[string]string{"slug": listRequest.Slug, "displayName": listRequest.DisplayName})
	if err != nil {
		s.fail(writer, request, err)
		return
	}
	listRequest.Limit = pageRequest.Limit
	if len(pageRequest.Position) > 0 {
		if len(pageRequest.Position) != 2 {
			s.fail(writer, request, catalog.E(catalog.CodeInvalidArgument, "cursor is invalid"))
			return
		}
		listRequest.AfterSlug, listRequest.AfterID = pageRequest.Position[0], pageRequest.Position[1]
	}
	items, err := s.catalog.ListNamespaces(request.Context(), listRequest)
	if err != nil {
		s.fail(writer, request, err)
		return
	}
	page, err := finishCursorPage(s, pageRequest, items, func(item catalog.Namespace) []string { return []string{item.Slug, item.ID} })
	if err != nil {
		s.fail(writer, request, err)
		return
	}
	s.respondJSON(writer, request, http.StatusOK, page)
}

// handleNamespacesCreate decodes namespace input and returns the created identity, Location and revision ETag.
func (s *Server) handleNamespacesCreate(writer http.ResponseWriter, request *http.Request) {
	var input catalog.CreateNamespaceInput
	if err := decodeJSON(writer, request, s.maxBodyBytes, &input); err != nil {
		s.fail(writer, request, err)
		return
	}
	item, err := s.catalog.CreateNamespace(request.Context(), input)
	if err != nil {
		s.fail(writer, request, err)
		return
	}
	setETag(writer, item.Revision)
	writer.Header().Set("Location", "/api/v1/namespaces/"+item.ID)
	s.respondJSON(writer, request, http.StatusCreated, item)
}

// handleNamespaceGet reads one authorized namespace and exposes its current revision ETag.
func (s *Server) handleNamespaceGet(writer http.ResponseWriter, request *http.Request) {
	item, err := s.catalog.GetNamespace(request.Context(), request.PathValue("namespaceId"))
	if err != nil {
		s.fail(writer, request, err)
		return
	}
	setETag(writer, item.Revision)
	s.respondJSON(writer, request, http.StatusOK, item)
}

// handleNamespacePatch requires If-Match before updating namespace metadata and returning the new ETag.
func (s *Server) handleNamespacePatch(writer http.ResponseWriter, request *http.Request) {
	revision, err := expectedRevision(request)
	if err != nil {
		s.fail(writer, request, err)
		return
	}
	var input catalog.PatchNamespaceInput
	if err := decodeJSON(writer, request, s.maxBodyBytes, &input); err != nil {
		s.fail(writer, request, err)
		return
	}
	item, err := s.catalog.UpdateNamespace(request.Context(), request.PathValue("namespaceId"), input, revision)
	if err != nil {
		s.fail(writer, request, err)
		return
	}
	setETag(writer, item.Revision)
	s.respondJSON(writer, request, http.StatusOK, item)
}

// handlePublishersList pages publisher summaries by display name and ID using signed continuation.
func (s *Server) handlePublishersList(writer http.ResponseWriter, request *http.Request) {
	if err := validateDirectoryQuery(request, "displayName", "declaredDomain"); err != nil {
		s.fail(writer, request, err)
		return
	}
	query, err := parseQueryParameters(request)
	if err != nil {
		s.fail(writer, request, err)
		return
	}
	listRequest, err := (catalog.PublisherListRequest{DisplayName: query.Get("displayName"), DeclaredDomain: query.Get("declaredDomain")}).NormalizeFilters()
	if err != nil {
		s.fail(writer, request, err)
		return
	}
	pageRequest, err := s.parseAdminValues(request.Context(), query, "publishers", "", "displayName,id", map[string]string{"displayName": listRequest.DisplayName, "declaredDomain": listRequest.DeclaredDomain})
	if err != nil {
		s.fail(writer, request, err)
		return
	}
	listRequest.Limit = pageRequest.Limit
	if len(pageRequest.Position) > 0 {
		if len(pageRequest.Position) != 2 {
			s.fail(writer, request, catalog.E(catalog.CodeInvalidArgument, "cursor is invalid"))
			return
		}
		listRequest.AfterDisplayName, listRequest.AfterID = pageRequest.Position[0], pageRequest.Position[1]
	}
	items, err := s.catalog.ListPublishers(request.Context(), listRequest)
	if err != nil {
		s.fail(writer, request, err)
		return
	}
	page, err := finishCursorPage(s, pageRequest, items, func(item catalog.Publisher) []string { return []string{item.DisplayName, item.ID} })
	if err != nil {
		s.fail(writer, request, err)
		return
	}
	s.respondJSON(writer, request, http.StatusOK, page)
}

// handlePublishersCreate creates a publisher from closed input and returns its Location and revision ETag.
func (s *Server) handlePublishersCreate(writer http.ResponseWriter, request *http.Request) {
	var input catalog.CreatePublisherInput
	if err := decodeJSON(writer, request, s.maxBodyBytes, &input); err != nil {
		s.fail(writer, request, err)
		return
	}
	item, err := s.catalog.CreatePublisher(request.Context(), input)
	if err != nil {
		s.fail(writer, request, err)
		return
	}
	setETag(writer, item.Revision)
	writer.Header().Set("Location", "/api/v1/publishers/"+item.ID)
	s.respondJSON(writer, request, http.StatusCreated, item)
}

// handlePublisherGet reads one authorized publisher and exposes its current revision ETag.
func (s *Server) handlePublisherGet(writer http.ResponseWriter, request *http.Request) {
	item, err := s.catalog.GetPublisher(request.Context(), request.PathValue("publisherId"))
	if err != nil {
		s.fail(writer, request, err)
		return
	}
	setETag(writer, item.Revision)
	s.respondJSON(writer, request, http.StatusOK, item)
}

// handlePublisherPatch passes closed metadata input and the required revision precondition to publisher updates.
func (s *Server) handlePublisherPatch(writer http.ResponseWriter, request *http.Request) {
	revision, err := expectedRevision(request)
	if err != nil {
		s.fail(writer, request, err)
		return
	}
	var input catalog.PatchPublisherInput
	if err := decodeJSON(writer, request, s.maxBodyBytes, &input); err != nil {
		s.fail(writer, request, err)
		return
	}
	item, err := s.catalog.UpdatePublisher(request.Context(), request.PathValue("publisherId"), input, revision)
	if err != nil {
		s.fail(writer, request, err)
		return
	}
	setETag(writer, item.Revision)
	s.respondJSON(writer, request, http.StatusOK, item)
}

// handlePublisherPublicationDomainApprove passes an exact domain, audit reason and publisher revision to approval.
func (s *Server) handlePublisherPublicationDomainApprove(writer http.ResponseWriter, request *http.Request) {
	revision, err := expectedRevision(request)
	if err != nil {
		s.fail(writer, request, err)
		return
	}
	reason, err := reasonBody(writer, request, s.maxBodyBytes)
	if err != nil {
		s.fail(writer, request, err)
		return
	}
	item, err := s.catalog.ApprovePublisherPublicationDomain(request.Context(), request.PathValue("publisherId"), request.PathValue("domain"), reason, revision)
	if err != nil {
		s.fail(writer, request, err)
		return
	}
	setETag(writer, item.Revision)
	s.respondJSON(writer, request, http.StatusOK, item)
}

// handlePublisherPublicationDomainRevoke revokes one domain with revision fencing and returns the resulting publisher impact.
func (s *Server) handlePublisherPublicationDomainRevoke(writer http.ResponseWriter, request *http.Request) {
	revision, err := expectedRevision(request)
	if err != nil {
		s.fail(writer, request, err)
		return
	}
	reason, err := reasonBody(writer, request, s.maxBodyBytes)
	if err != nil {
		s.fail(writer, request, err)
		return
	}
	result, err := s.catalog.RevokePublisherPublicationDomain(request.Context(), request.PathValue("publisherId"), request.PathValue("domain"), reason, revision)
	if err != nil {
		s.fail(writer, request, err)
		return
	}
	setETag(writer, result.Publisher.Revision)
	s.respondJSON(writer, request, http.StatusOK, result)
}

// handleResourcesList pages resources by descending update time/ID with kind and exact identifier bound into the cursor.
func (s *Server) handleResourcesList(writer http.ResponseWriter, request *http.Request) {
	kind := catalog.Kind(request.URL.Query().Get("kind"))
	if request.URL.Query().Has("kind") && kind == "" {
		s.fail(writer, request, catalog.E(catalog.CodeInvalidArgument, "resource kind is invalid"))
		return
	}
	identifier := request.URL.Query().Get("canonicalIdentifier")
	if request.URL.Query().Has("canonicalIdentifier") {
		var err error
		identifier, err = catalog.CanonicalResourceIdentifier(identifier)
		if err != nil {
			s.fail(writer, request, err)
			return
		}
	}
	pageRequest, err := s.parseAdminPage(request, "resources", "", "updatedAt desc,id desc", map[string]string{"kind": string(kind), "canonicalIdentifier": identifier})
	if err != nil {
		s.fail(writer, request, err)
		return
	}
	listRequest := catalog.ResourceListRequest{CanonicalIdentifier: identifier, Kind: kind, Limit: pageRequest.Limit}
	if len(pageRequest.Position) > 0 {
		if len(pageRequest.Position) != 2 {
			s.fail(writer, request, catalog.E(catalog.CodeInvalidArgument, "cursor is invalid"))
			return
		}
		position, parseErr := time.Parse(time.RFC3339Nano, pageRequest.Position[0])
		if parseErr != nil {
			s.fail(writer, request, catalog.E(catalog.CodeInvalidArgument, "cursor is invalid"))
			return
		}
		listRequest.AfterUpdatedAt, listRequest.AfterID = &position, pageRequest.Position[1]
	}
	items, err := s.catalog.ListResources(request.Context(), listRequest)
	if err != nil {
		s.fail(writer, request, err)
		return
	}
	page, err := finishCursorPage(s, pageRequest, items, func(item catalog.Resource) []string {
		return []string{item.UpdatedAt.UTC().Format(time.RFC3339Nano), item.ID}
	})
	if err != nil {
		s.fail(writer, request, err)
		return
	}
	s.respondJSON(writer, request, http.StatusOK, page)
}

// handleResourcesCreate creates a governed resource and returns its stable Location and revision ETag.
func (s *Server) handleResourcesCreate(writer http.ResponseWriter, request *http.Request) {
	var input catalog.CreateResourceInput
	if err := decodeJSON(writer, request, s.maxBodyBytes, &input); err != nil {
		s.fail(writer, request, err)
		return
	}
	item, err := s.catalog.CreateResource(request.Context(), input)
	if err != nil {
		s.fail(writer, request, err)
		return
	}
	setETag(writer, item.Revision)
	writer.Header().Set("Location", "/api/v1/resources/"+item.ID)
	s.respondJSON(writer, request, http.StatusCreated, item)
}

// handleResourceGet reads the authorized resource identity and current revision ETag.
func (s *Server) handleResourceGet(writer http.ResponseWriter, request *http.Request) {
	item, err := s.catalog.GetResource(request.Context(), request.PathValue("resourceId"))
	if err != nil {
		s.fail(writer, request, err)
		return
	}
	setETag(writer, item.Revision)
	s.respondJSON(writer, request, http.StatusOK, item)
}

// handleResourceDelete requires a reason and If-Match before delegating resource deletion to lifecycle rules.
func (s *Server) handleResourceDelete(writer http.ResponseWriter, request *http.Request) {
	revision, err := expectedRevision(request)
	if err != nil {
		s.fail(writer, request, err)
		return
	}
	reason, err := reasonBody(writer, request, s.maxBodyBytes)
	if err != nil {
		s.fail(writer, request, err)
		return
	}
	if err := s.catalog.DeleteResource(request.Context(), request.PathValue("resourceId"), reason, revision); err != nil {
		s.fail(writer, request, err)
		return
	}
	writer.WriteHeader(http.StatusNoContent)
}

// handleVersionCreate returns the created version, immutable revision and validation evidence in one response.
func (s *Server) handleVersionCreate(writer http.ResponseWriter, request *http.Request) {
	var input catalog.CreateVersionInput
	if err := decodeJSON(writer, request, s.maxBodyBytes, &input); err != nil {
		s.fail(writer, request, err)
		return
	}
	version, revision, evidence, err := s.catalog.CreateVersion(request.Context(), request.PathValue("resourceId"), input)
	if err != nil {
		s.fail(writer, request, err)
		return
	}
	setETag(writer, version.Revision)
	writer.Header().Set("Location", "/api/v1/resource-versions/"+version.ID)
	s.respondJSON(writer, request, http.StatusCreated, versionEvidenceResponse{Version: version, Revision: revision, Evidence: evidence})
}

// handleVersionGet reads a version's current state with its concurrency ETag.
func (s *Server) handleVersionGet(writer http.ResponseWriter, request *http.Request) {
	item, err := s.catalog.GetVersion(request.Context(), request.PathValue("versionId"))
	if err != nil {
		s.fail(writer, request, err)
		return
	}
	setETag(writer, item.Revision)
	s.respondJSON(writer, request, http.StatusOK, item)
}

// handleVersionRevisionCreate creates immutable representation evidence against the version's required revision precondition.
func (s *Server) handleVersionRevisionCreate(writer http.ResponseWriter, request *http.Request) {
	revisionNumber, err := expectedRevision(request)
	if err != nil {
		s.fail(writer, request, err)
		return
	}
	var input struct {
		Representations []catalog.RepresentationInput `json:"representations"`
	}
	if err := decodeJSON(writer, request, s.maxBodyBytes, &input); err != nil {
		s.fail(writer, request, err)
		return
	}
	version, revision, evidence, err := s.catalog.CreateVersionRevision(request.Context(), request.PathValue("versionId"), input.Representations, revisionNumber)
	if err != nil {
		s.fail(writer, request, err)
		return
	}
	setETag(writer, version.Revision)
	writer.Header().Set("Location", "/api/v1/version-revisions/"+revision.ID)
	s.respondJSON(writer, request, http.StatusCreated, versionEvidenceResponse{Version: version, Revision: revision, Evidence: evidence})
}

// handleVersionRevisionGet returns retained revision data together with its validation evidence.
func (s *Server) handleVersionRevisionGet(writer http.ResponseWriter, request *http.Request) {
	revision, evidence, namespaceID, err := s.catalog.GetVersionRevision(request.Context(), request.PathValue("revisionId"))
	if err != nil {
		s.fail(writer, request, err)
		return
	}
	s.respondJSON(writer, request, http.StatusOK, revisionEvidenceResponse{NamespaceID: namespaceID, Revision: revision, Evidence: evidence})
}

// handleVersionRevisionValidate accepts an optional empty control body and returns the revalidation result.
func (s *Server) handleVersionRevisionValidate(writer http.ResponseWriter, request *http.Request) {
	var input struct{}
	if err := decodeOptionalEmptyObject(writer, request, s.maxBodyBytes, &input); err != nil {
		s.fail(writer, request, err)
		return
	}
	revision, evidence, namespaceID, err := s.catalog.RevalidateVersionRevision(request.Context(), request.PathValue("revisionId"))
	if err != nil {
		s.fail(writer, request, err)
		return
	}
	writer.Header().Set("Location", "/api/v1/version-revisions/"+revision.ID)
	s.respondJSON(writer, request, http.StatusCreated, revisionEvidenceResponse{NamespaceID: namespaceID, Revision: revision, Evidence: evidence})
}

// handleListingCreate creates a stable listing identity and returns its Location and concurrency ETag.
func (s *Server) handleListingCreate(writer http.ResponseWriter, request *http.Request) {
	var input catalog.CreateListingInput
	if err := decodeJSON(writer, request, s.maxBodyBytes, &input); err != nil {
		s.fail(writer, request, err)
		return
	}
	item, err := s.catalog.CreateListing(request.Context(), input)
	if err != nil {
		s.fail(writer, request, err)
		return
	}
	setETag(writer, item.Revision)
	writer.Header().Set("Location", "/api/v1/catalog-listings/"+item.ID)
	s.respondJSON(writer, request, http.StatusCreated, item)
}

// handleListingGet reads the authorized listing and current revision ETag.
func (s *Server) handleListingGet(writer http.ResponseWriter, request *http.Request) {
	item, err := s.catalog.GetListing(request.Context(), request.PathValue("listingId"))
	if err != nil {
		s.fail(writer, request, err)
		return
	}
	setETag(writer, item.Revision)
	s.respondJSON(writer, request, http.StatusOK, item)
}

// handleListingRevisionCreate creates an immutable listing revision against the listing's If-Match precondition.
func (s *Server) handleListingRevisionCreate(writer http.ResponseWriter, request *http.Request) {
	revisionNumber, err := expectedRevision(request)
	if err != nil {
		s.fail(writer, request, err)
		return
	}
	var input catalog.CreateListingRevisionInput
	if err := decodeJSONUsing(writer, request, s.maxBodyBytes, &input, jsondoc.DecodeNumbers); err != nil {
		s.fail(writer, request, err)
		return
	}
	listing, revision, err := s.catalog.CreateListingRevision(request.Context(), request.PathValue("listingId"), input, revisionNumber)
	if err != nil {
		s.fail(writer, request, err)
		return
	}
	setETag(writer, listing.Revision)
	writer.Header().Set("Location", "/api/v1/listing-revisions/"+revision.ID)
	s.respondJSON(writer, request, http.StatusCreated, listingRevisionCreatedResponse{Listing: listing, Revision: revision})
}

// handleListingRevisionGet returns one retained immutable listing revision.
func (s *Server) handleListingRevisionGet(writer http.ResponseWriter, request *http.Request) {
	item, err := s.catalog.GetListingRevision(request.Context(), request.PathValue("revisionId"))
	if err != nil {
		s.fail(writer, request, err)
		return
	}
	s.respondJSON(writer, request, http.StatusOK, item)
}

// handleSubmissionCreate submits the selected listing candidate and returns the review Location and ETag.
func (s *Server) handleSubmissionCreate(writer http.ResponseWriter, request *http.Request) {
	var input struct{}
	if err := decodeOptionalEmptyObject(writer, request, s.maxBodyBytes, &input); err != nil {
		s.fail(writer, request, err)
		return
	}
	item, err := s.catalog.Submit(request.Context(), request.PathValue("listingId"))
	if err != nil {
		s.fail(writer, request, err)
		return
	}
	setETag(writer, item.Revision)
	writer.Header().Set("Location", "/api/v1/review-submissions/"+item.ID)
	s.respondJSON(writer, request, http.StatusCreated, item)
}

// handleSubmissionGet reads retained submission state with its current decision ETag.
func (s *Server) handleSubmissionGet(writer http.ResponseWriter, request *http.Request) {
	item, err := s.catalog.GetSubmission(request.Context(), request.PathValue("submissionId"))
	if err != nil {
		s.fail(writer, request, err)
		return
	}
	setETag(writer, item.Revision)
	s.respondJSON(writer, request, http.StatusOK, item)
}

// handleSubmissionApprove selects approval through the shared reason/precondition decision boundary.
func (s *Server) handleSubmissionApprove(writer http.ResponseWriter, request *http.Request) {
	s.handleSubmissionDecision(writer, request, "approve")
}

// handleSubmissionRequestChanges selects requested changes through the shared review decision boundary.
func (s *Server) handleSubmissionRequestChanges(writer http.ResponseWriter, request *http.Request) {
	s.handleSubmissionDecision(writer, request, "request_changes")
}

// handleSubmissionReject selects rejection through the shared reason/precondition decision boundary.
func (s *Server) handleSubmissionReject(writer http.ResponseWriter, request *http.Request) {
	s.handleSubmissionDecision(writer, request, "reject")
}

// handleSubmissionDecision requires a reason and submission revision before delegating the selected review decision.
func (s *Server) handleSubmissionDecision(writer http.ResponseWriter, request *http.Request, action string) {
	revision, err := expectedRevision(request)
	if err != nil {
		s.fail(writer, request, err)
		return
	}
	reason, err := reasonBody(writer, request, s.maxBodyBytes)
	if err != nil {
		s.fail(writer, request, err)
		return
	}
	submission, version, err := s.catalog.DecideSubmission(request.Context(), request.PathValue("submissionId"), action, reason, revision)
	if err != nil {
		s.fail(writer, request, err)
		return
	}
	setETag(writer, submission.Revision)
	s.respondJSON(writer, request, http.StatusOK, reviewDecisionResponse{Submission: submission, Version: version})
}

// handleListingPublish publishes against the listing revision and returns the release plus any replaced prior release.
func (s *Server) handleListingPublish(writer http.ResponseWriter, request *http.Request) {
	revision, err := expectedRevision(request)
	if err != nil {
		s.fail(writer, request, err)
		return
	}
	var input catalog.PublishInput
	if err := decodeJSON(writer, request, s.maxBodyBytes, &input); err != nil {
		s.fail(writer, request, err)
		return
	}
	listing, release, prior, err := s.catalog.Publish(request.Context(), request.PathValue("listingId"), input, revision)
	if err != nil {
		s.fail(writer, request, err)
		return
	}
	setETag(writer, listing.Revision)
	writer.Header().Set("Location", "/api/v1/listing-releases/"+release.ID)
	s.respondJSON(writer, request, http.StatusCreated, listingPublishedResponse{Listing: listing, Release: release, PriorRelease: prior})
}

// handleReleaseGet reads a retained release and its current lifecycle ETag.
func (s *Server) handleReleaseGet(writer http.ResponseWriter, request *http.Request) {
	item, err := s.catalog.GetRelease(request.Context(), request.PathValue("releaseId"))
	if err != nil {
		s.fail(writer, request, err)
		return
	}
	setETag(writer, item.Revision)
	s.respondJSON(writer, request, http.StatusOK, item)
}

// handleReleaseDeprecate selects deprecation through the shared lifecycle/precondition decoder.
func (s *Server) handleReleaseDeprecate(writer http.ResponseWriter, request *http.Request) {
	s.handleReleaseTransition(writer, request, catalog.ReleaseDeprecated)
}

// handleReleaseRetire selects retirement through the shared lifecycle/precondition decoder.
func (s *Server) handleReleaseRetire(writer http.ResponseWriter, request *http.Request) {
	s.handleReleaseTransition(writer, request, catalog.ReleaseRetired)
}

// handleReleaseSupersede selects supersession through the shared replacement/precondition decoder.
func (s *Server) handleReleaseSupersede(writer http.ResponseWriter, request *http.Request) {
	s.handleReleaseTransition(writer, request, catalog.ReleaseSuperseded)
}

// handleReleaseRevoke selects revocation through the shared lifecycle/precondition decoder.
func (s *Server) handleReleaseRevoke(writer http.ResponseWriter, request *http.Request) {
	s.handleReleaseTransition(writer, request, catalog.ReleaseRevoked)
}

// handleReleaseTransition decodes reason, replacement and sunset input before a revision-fenced release transition.
func (s *Server) handleReleaseTransition(writer http.ResponseWriter, request *http.Request, state catalog.ReleaseState) {
	revision, err := expectedRevision(request)
	if err != nil {
		s.fail(writer, request, err)
		return
	}
	var input struct {
		Reason        string     `json:"reason"`
		ReplacementID string     `json:"replacementId,omitempty"`
		SunsetAt      *time.Time `json:"sunsetAt,omitempty"`
	}
	if err := decodeJSON(writer, request, s.maxBodyBytes, &input); err != nil {
		s.fail(writer, request, err)
		return
	}
	item, err := s.catalog.TransitionRelease(request.Context(), request.PathValue("releaseId"), state, input.Reason, input.ReplacementID, input.SunsetAt, revision)
	if err != nil {
		s.fail(writer, request, err)
		return
	}
	setETag(writer, item.Revision)
	s.respondJSON(writer, request, http.StatusOK, item)
}

// handleArtifactGet returns authorized retained bytes as a digest-bound attachment with exact content length.
func (s *Server) handleArtifactGet(writer http.ResponseWriter, request *http.Request) {
	artifact, content, err := s.catalog.GetArtifact(request.Context(), request.PathValue("artifactId"))
	if err != nil {
		s.fail(writer, request, err)
		return
	}
	mediaType := artifact.MediaType
	if mediaType == "" {
		mediaType = "application/octet-stream"
	}
	writer.Header().Set("Content-Type", mediaType)
	writer.Header().Set("Content-Length", stringInt64(int64(len(content))))
	setContentDigest(writer, content)
	writer.Header().Set("Content-Disposition", `attachment; filename="`+artifact.ID+`"`)
	writer.WriteHeader(http.StatusOK)
	_, _ = writer.Write(content)
}

// handleCatalogBrowse maps local browse query parameters to a bounded Catalog search request.
func (s *Server) handleCatalogBrowse(writer http.ResponseWriter, request *http.Request) {
	limit, err := parseLimit(request, 20)
	if err != nil {
		s.fail(writer, request, err)
		return
	}
	parameters := request.URL.Query()
	if (parameters.Has("namespaceId") && !identity.ValidTenantID(parameters.Get("namespaceId"))) || (parameters.Has("kind") && !catalog.Kind(parameters.Get("kind")).Valid()) ||
		(parameters.Has("visibility") && !catalog.Visibility(parameters.Get("visibility")).Valid()) {
		s.fail(writer, request, catalog.E(catalog.CodeInvalidArgument, "provided namespace, kind and visibility filters must be supported values"))
		return
	}
	query := catalog.SearchQuery{NamespaceID: parameters.Get("namespaceId"), Limit: limit, Cursor: parameters.Get("cursor"), Visibility: catalog.Visibility(parameters.Get("visibility"))}
	if kind := parameters.Get("kind"); kind != "" {
		query.Kinds = []catalog.Kind{catalog.Kind(kind)}
	}
	page, err := s.catalog.Search(request.Context(), query)
	if err != nil {
		s.fail(writer, request, err)
		return
	}
	s.respondJSON(writer, request, http.StatusOK, page)
}

// handleCatalogSearch is the authenticated unified-search edge; the search
// service decides how local, indexed, and approved live results are combined.
func (s *Server) handleCatalogSearch(writer http.ResponseWriter, request *http.Request) {
	var input catalogSearchRequest
	if err := decodeJSON(writer, request, s.maxBodyBytes, &input); err != nil {
		s.fail(writer, request, err)
		return
	}
	query, err := input.searchQuery()
	if err != nil {
		s.fail(writer, request, err)
		return
	}
	reply, err := s.ReadSearch(request.Context(), query)
	s.respondRead(writer, request, reply, err)
}

// handleCatalogExplore decodes explicit facet requests and delegates local Catalog exploration.
func (s *Server) handleCatalogExplore(writer http.ResponseWriter, request *http.Request) {
	var input catalogExploreRequest
	if err := decodeJSON(writer, request, s.maxBodyBytes, &input); err != nil {
		s.fail(writer, request, err)
		return
	}
	explore, err := input.exploreRequest()
	if err != nil {
		s.fail(writer, request, err)
		return
	}
	reply, err := s.ReadExplore(request.Context(), explore)
	s.respondRead(writer, request, reply, err)
}

type catalogSearchRequest struct {
	Order       *catalog.SearchOrder       `json:"order,omitempty"`
	Origins     []catalog.SearchResultType `json:"origins,omitempty"`
	NamespaceID *string                    `json:"namespaceId,omitempty"`
	Text        string                     `json:"text"`
	Kinds       []catalog.Kind             `json:"kinds,omitempty"`
	Categories  []string                   `json:"categories,omitempty"`
	License     string                     `json:"license,omitempty"`
	Tags        []string                   `json:"tags,omitempty"`
	Publisher   string                     `json:"publisher,omitempty"`
	Visibility  catalog.Visibility         `json:"visibility,omitempty"`
	Limit       *int                       `json:"limit,omitempty"`
	Cursor      string                     `json:"cursor,omitempty"`
}

// searchQuery preserves omitted limits while rejecting explicit limits outside the Catalog page bound.
func (input catalogSearchRequest) searchQuery() (catalog.SearchQuery, error) {
	var order catalog.SearchOrder
	if input.Order != nil {
		order = *input.Order
		if order == "" {
			return catalog.SearchQuery{}, catalog.E(catalog.CodeInvalidArgument, "catalog search order is invalid")
		}
	}
	namespaceID, err := catalogNamespaceFilter(input.NamespaceID)
	if err != nil {
		return catalog.SearchQuery{}, err
	}
	limit := 0
	if input.Limit != nil {
		if *input.Limit < 1 || *input.Limit > catalog.MaxCatalogPageSize {
			return catalog.SearchQuery{}, catalog.E(catalog.CodeInvalidArgument, "catalog search limit must be between 1 and 100")
		}
		limit = *input.Limit
	}
	return catalog.SearchQuery{Order: order, Origins: input.Origins, NamespaceID: namespaceID, Text: input.Text, Kinds: input.Kinds, Tags: input.Tags, Categories: input.Categories, License: input.License, Publisher: input.Publisher, Visibility: input.Visibility, Limit: limit, Cursor: input.Cursor}, nil
}

type catalogExploreRequest struct {
	Query  catalogExploreQuery   `json:"query"`
	Facets []catalogFacetRequest `json:"facets"`
}

type catalogExploreQuery struct {
	NamespaceID *string            `json:"namespaceId,omitempty"`
	Text        string             `json:"text"`
	Kinds       []catalog.Kind     `json:"kinds,omitempty"`
	Categories  []string           `json:"categories,omitempty"`
	License     string             `json:"license,omitempty"`
	Tags        []string           `json:"tags,omitempty"`
	Publisher   string             `json:"publisher,omitempty"`
	Visibility  catalog.Visibility `json:"visibility,omitempty"`
}

type catalogFacetRequest struct {
	Field    string `json:"field"`
	Limit    *int   `json:"limit,omitempty"`
	MinCount int    `json:"minCount,omitempty"`
}

// exploreRequest maps closed facet input while distinguishing omitted limits from invalid explicit values.
func (input catalogExploreRequest) exploreRequest() (catalog.ExploreRequest, error) {
	namespaceID, err := catalogNamespaceFilter(input.Query.NamespaceID)
	if err != nil {
		return catalog.ExploreRequest{}, err
	}
	request := catalog.ExploreRequest{
		Query:  catalog.SearchQuery{NamespaceID: namespaceID, Text: input.Query.Text, Kinds: input.Query.Kinds, Tags: input.Query.Tags, Categories: input.Query.Categories, License: input.Query.License, Publisher: input.Query.Publisher, Visibility: input.Query.Visibility},
		Facets: make([]catalog.FacetRequest, 0, len(input.Facets)),
	}
	for _, facet := range input.Facets {
		limit := 0
		if facet.Limit != nil {
			if *facet.Limit < 1 || *facet.Limit > catalog.MaxCatalogPageSize {
				return catalog.ExploreRequest{}, catalog.E(catalog.CodeInvalidArgument, "catalog facet limit must be between 1 and 100")
			}
			limit = *facet.Limit
		}
		request.Facets = append(request.Facets, catalog.FacetRequest{Field: facet.Field, Limit: limit, MinCount: facet.MinCount})
	}
	return request, nil
}

// handleCatalogRecordGet returns one authorized Catalog record with its release revision ETag.
func (s *Server) handleCatalogRecordGet(writer http.ResponseWriter, request *http.Request) {
	reply, err := s.ReadRelease(request.Context(), request.PathValue("catalogRecordId"))
	s.respondRead(writer, request, reply, err)
}

// handleAuditEvents pages audited event reads using normalized filters and signed descending-time continuation.
func (s *Server) handleAuditEvents(writer http.ResponseWriter, request *http.Request) {
	query, filterBinding, err := parseAuditFilters(request)
	if err != nil {
		s.fail(writer, request, err)
		return
	}
	pageRequest, err := s.parseAdminPage(request, "audit-events", "", "occurredAt desc,id desc", filterBinding)
	if err != nil {
		s.fail(writer, request, err)
		return
	}
	listRequest := catalog.AuditEventListRequest{Filters: query, Limit: pageRequest.Limit}
	if len(pageRequest.Position) > 0 {
		if len(pageRequest.Position) != 2 {
			s.fail(writer, request, catalog.E(catalog.CodeInvalidArgument, "cursor is invalid"))
			return
		}
		position, parseErr := time.Parse(time.RFC3339Nano, pageRequest.Position[0])
		if parseErr != nil {
			s.fail(writer, request, catalog.E(catalog.CodeInvalidArgument, "cursor is invalid"))
			return
		}
		listRequest.AfterOccurredAt, listRequest.AfterID = &position, pageRequest.Position[1]
	}
	items, err := s.catalog.AuditEvents(request.Context(), listRequest)
	if err != nil {
		s.fail(writer, request, err)
		return
	}
	page, err := finishCursorPage(s, pageRequest, items, func(item catalog.AuditEvent) []string {
		return []string{item.At.UTC().Format(time.RFC3339Nano), item.ID}
	})
	if err != nil {
		s.fail(writer, request, err)
		return
	}
	s.respondJSON(writer, request, http.StatusOK, page)
}

type auditExportBody struct {
	Actor          strictOptionalString `json:"actor,omitempty"`
	Action         strictOptionalString `json:"action,omitempty"`
	RequestID      strictOptionalString `json:"requestId,omitempty"`
	TargetType     strictOptionalString `json:"targetType,omitempty"`
	TargetID       strictOptionalString `json:"targetId,omitempty"`
	OccurredFrom   *time.Time           `json:"occurredFrom"`
	OccurredBefore *time.Time           `json:"occurredBefore"`
	Reason         string               `json:"reason"`
}

type strictOptionalString struct {
	Value   string
	Present bool
}

// UnmarshalJSON distinguishes omission from an explicit string and rejects null or non-string values.
func (value *strictOptionalString) UnmarshalJSON(raw []byte) error {
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return errors.New("optional string must not be null")
	}
	if err := json.Unmarshal(raw, &value.Value); err != nil {
		return err
	}
	value.Present = true
	return nil
}

// handleAuditExport returns a bounded deterministic attachment. Its domain call
// also records that the sensitive evidence range was read.
func (s *Server) handleAuditExport(writer http.ResponseWriter, request *http.Request) {
	var body auditExportBody
	if err := decodeJSON(writer, request, s.maxBodyBytes, &body); err != nil {
		s.fail(writer, request, err)
		return
	}
	query := audit.Query{OccurredFrom: body.OccurredFrom, OccurredBefore: body.OccurredBefore}
	for _, filter := range []struct {
		name   string
		value  strictOptionalString
		target *string
	}{
		{name: "actor", value: body.Actor, target: &query.Actor},
		{name: "action", value: body.Action, target: &query.Action},
		{name: "requestId", value: body.RequestID, target: &query.RequestID},
		{name: "targetType", value: body.TargetType, target: &query.TargetType},
		{name: "targetId", value: body.TargetID, target: &query.TargetID},
	} {
		if !filter.value.Present {
			continue
		}
		if filter.value.Value == "" {
			s.fail(writer, request, catalog.E(catalog.CodeInvalidArgument, filter.name+" must not be empty when provided"))
			return
		}
		*filter.target = filter.value.Value
	}
	export, err := s.catalog.ExportAuditEvents(request.Context(), catalog.AuditExportInput{Filters: query, Reason: body.Reason})
	if err != nil {
		s.fail(writer, request, err)
		return
	}
	writer.Header().Set("Content-Disposition", `attachment; filename="open-agentic-catalog-audit-export.json"`)
	s.respondJSON(writer, request, http.StatusOK, export)
}

// parseAuditFilters normalizes optional audit filters and returns the same canonical values for cursor binding.
func parseAuditFilters(request *http.Request) (audit.Query, map[string]string, error) {
	values, err := parseQueryParameters(request)
	if err != nil {
		return audit.Query{}, nil, err
	}
	textValue := func(name string) (string, bool, error) {
		if len(values[name]) == 0 {
			return "", false, nil
		}
		if values[name][0] == "" {
			return "", false, catalog.E(catalog.CodeInvalidArgument, name+" must not be empty when provided")
		}
		return values[name][0], true, nil
	}
	query := audit.Query{}
	for _, filter := range []struct {
		name   string
		target *string
	}{
		{name: "actor", target: &query.Actor},
		{name: "action", target: &query.Action},
		{name: "requestId", target: &query.RequestID},
		{name: "targetType", target: &query.TargetType},
		{name: "targetId", target: &query.TargetID},
	} {
		value, present, valueErr := textValue(filter.name)
		if valueErr != nil {
			return audit.Query{}, nil, valueErr
		}
		if present {
			*filter.target = value
		}
	}
	parseTime := func(name string) (time.Time, bool, error) {
		value, present, valueErr := textValue(name)
		if valueErr != nil {
			return time.Time{}, false, valueErr
		}
		if !present {
			return time.Time{}, false, nil
		}
		parsed, parseErr := time.Parse(time.RFC3339Nano, value)
		if parseErr != nil {
			return time.Time{}, false, catalog.E(catalog.CodeInvalidArgument, name+" must be an RFC 3339 timestamp")
		}
		return parsed.UTC(), true, nil
	}
	from, fromPresent, err := parseTime("occurredFrom")
	if err != nil {
		return audit.Query{}, nil, err
	}
	if fromPresent {
		query.OccurredFrom = &from
	}
	before, beforePresent, err := parseTime("occurredBefore")
	if err != nil {
		return audit.Query{}, nil, err
	}
	if beforePresent {
		query.OccurredBefore = &before
	}
	query, err = audit.NormalizeQuery(query, false)
	if err != nil {
		return audit.Query{}, nil, catalog.Wrap(catalog.CodeInvalidArgument, "audit filters are invalid", err)
	}
	binding := map[string]string{
		"actor": query.Actor, "action": query.Action, "requestId": query.RequestID,
		"targetType": query.TargetType, "targetId": query.TargetID,
		"occurredFrom": canonicalOptionalTime(query.OccurredFrom), "occurredBefore": canonicalOptionalTime(query.OccurredBefore),
	}
	return query, binding, nil
}

// canonicalOptionalTime encodes present filter instants as UTC RFC 3339 nanoseconds and omission as empty text.
func canonicalOptionalTime(value *time.Time) string {
	if value == nil {
		return ""
	}
	return value.UTC().Format(time.RFC3339Nano)
}

// decodeOptionalEmptyObject permits an absent body while applying strict JSON decoding to any supplied object.
func decodeOptionalEmptyObject(writer http.ResponseWriter, request *http.Request, maximum int64, target any) error {
	if request.Body == nil || request.ContentLength == 0 {
		return nil
	}
	return decodeJSON(writer, request, maximum, target)
}

// stringInt64 formats decimal content-length values without locale-dependent presentation.
func stringInt64(value int64) string {
	return strconv.FormatInt(value, 10)
}

// catalogNamespaceFilter distinguishes an omitted selector from an invalid empty UUID.
func catalogNamespaceFilter(value *string) (string, error) {
	if value == nil {
		return "", nil
	}
	if !identity.ValidTenantID(*value) {
		return "", catalog.E(catalog.CodeInvalidArgument, "namespaceId must be a UUID")
	}
	return *value, nil
}
