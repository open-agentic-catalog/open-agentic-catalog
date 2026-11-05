// Federation handlers expose administrator-governed source inspection,
// configuration, synchronization and provenance reads.

package httpserver

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/audit"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/federation"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/jobs"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/observability"
)

type sourceConfigurationPatch struct {
	TrustVerificationPolicy *string                           `json:"trustVerificationPolicy,omitempty"`
	ArtworkEnabled          *bool                             `json:"artworkEnabled,omitempty"`
	InspectionID            string                            `json:"inspectionId,omitempty"`
	DisplayName             *string                           `json:"displayName,omitempty"`
	RetrievalMode           *federation.RetrievalMode         `json:"retrievalMode,omitempty"`
	Scope                   *federation.Scope                 `json:"scope,omitempty"`
	UpdateMode              *federation.UpdateMode            `json:"updateMode,omitempty"`
	Live                    *federation.LivePolicy            `json:"live,omitempty"`
	Referrals               *federation.ReferralPolicy        `json:"referrals,omitempty"`
	DiscoveryAssignments    *[]federation.DiscoveryAssignment `json:"discoveryAssignments,omitempty"`
	Nesting                 *federation.NestingPolicy         `json:"nesting,omitempty"`
	CredentialBinding       *string                           `json:"credentialBinding,omitempty"`
	NetworkProfile          *string                           `json:"networkProfile,omitempty"`
}

// domainInput copies optional configuration fields while preserving omission for domain patch semantics.
func (p sourceConfigurationPatch) domainInput() federation.PatchInput {
	return federation.PatchInput{TrustVerificationPolicy: p.TrustVerificationPolicy, ArtworkEnabled: p.ArtworkEnabled, InspectionID: p.InspectionID, DisplayName: p.DisplayName, RetrievalMode: p.RetrievalMode, Scope: p.Scope, UpdateMode: p.UpdateMode, Live: p.Live, Referrals: p.Referrals, DiscoveryAssignments: p.DiscoveryAssignments, Nesting: p.Nesting, CredentialBinding: p.CredentialBinding, NetworkProfile: p.NetworkProfile}
}

type sourceChangePreviewRequest struct {
	sourceConfigurationPatch
	Intent federation.ChangeIntent `json:"intent"`
}

type sourceUpdateRequest struct {
	sourceConfigurationPatch
	ChangePreviewDigest string `json:"changePreviewDigest"`
	Reason              string `json:"reason"`
}

type sourceReasonRequest struct {
	Reason string `json:"reason"`
}

type sourceRemovalRequest struct {
	Reason              string `json:"reason"`
	ChangePreviewDigest string `json:"changePreviewDigest"`
}

// handleSourceInspect decodes explicit source inspection input and returns retained inspection evidence and Location.
func (s *Server) handleSourceInspect(writer http.ResponseWriter, request *http.Request) {
	var input federation.InspectionInput
	if err := decodeJSON(writer, request, s.maxBodyBytes, &input); err != nil {
		s.fail(writer, request, err)
		return
	}
	inspection, err := s.federation.InspectWithAccess(request.Context(), input)
	if err != nil {
		s.fail(writer, request, err)
		return
	}
	writer.Header().Set("Location", "/api/v1/federation/source-inspections/"+inspection.ID)
	s.respondJSON(writer, request, http.StatusCreated, inspection)
}

// handleSourcesList pages source configurations by display name/ID using signed continuation.
func (s *Server) handleSourcesList(writer http.ResponseWriter, request *http.Request) {
	query, err := parseQueryParameters(request)
	if err != nil {
		s.fail(writer, request, err)
		return
	}
	filters := map[string]string{}
	for _, name := range []string{"displayName", "status"} {
		if values, present := query[name]; present {
			if len(values) != 1 || values[0] == "" {
				s.fail(writer, request, catalog.E(catalog.CodeInvalidArgument, name+" must be one non-empty value"))
				return
			}
			filters[name] = values[0]
		}
	}
	pageRequest, err := s.parseAdminPage(request, "federation-sources", "", "displayName,id", filters)
	if err != nil {
		s.fail(writer, request, err)
		return
	}
	listRequest := federation.SourceListRequest{Limit: pageRequest.Limit, DisplayName: filters["displayName"], Status: federation.SourceStatus(filters["status"])}
	if len(pageRequest.Position) > 0 {
		if len(pageRequest.Position) != 2 {
			s.fail(writer, request, catalog.E(catalog.CodeInvalidArgument, "cursor is invalid"))
			return
		}
		listRequest.AfterDisplayName, listRequest.AfterID = pageRequest.Position[0], pageRequest.Position[1]
	}
	items, err := s.federation.ListSourcePage(request.Context(), listRequest)
	if err != nil {
		s.fail(writer, request, err)
		return
	}
	page, err := finishCursorPage(s, pageRequest, items, func(item federation.Source) []string { return []string{item.DisplayName, item.ID} })
	if err != nil {
		s.fail(writer, request, err)
		return
	}
	s.respondJSON(writer, request, http.StatusOK, page)
}

// handleSourceInspectionGet returns the exact retained inspection through source-administration authorization.
func (s *Server) handleSourceInspectionGet(writer http.ResponseWriter, request *http.Request) {
	inspection, err := s.federation.GetInspection(request.Context(), request.PathValue("inspectionId"))
	if err != nil {
		s.fail(writer, request, err)
		return
	}
	s.respondJSON(writer, request, http.StatusOK, inspection)
}

// handleSourceSnapshotGet returns one retained synchronization snapshot through the federation manager.
func (s *Server) handleSourceSnapshotGet(writer http.ResponseWriter, request *http.Request) {
	snapshot, err := s.federation.GetSnapshot(request.Context(), request.PathValue("snapshotId"))
	if err != nil {
		s.fail(writer, request, err)
		return
	}
	s.respondJSON(writer, request, http.StatusOK, snapshot)
}

// handleSourceConnect connects from explicit inspected input and returns the source Location and revision ETag.
func (s *Server) handleSourceConnect(writer http.ResponseWriter, request *http.Request) {
	var input federation.ConnectInput
	if err := decodeJSON(writer, request, s.maxBodyBytes, &input); err != nil {
		s.fail(writer, request, err)
		return
	}
	source, err := s.federation.Connect(request.Context(), input)
	if err != nil {
		s.fail(writer, request, err)
		return
	}
	setETag(writer, source.Revision)
	writer.Header().Set("Location", "/api/v1/federation/sources/"+source.ID)
	s.respondJSON(writer, request, http.StatusCreated, source)
}

// handleSourceGet reads one authorized source configuration and its current ETag.
func (s *Server) handleSourceGet(writer http.ResponseWriter, request *http.Request) {
	source, err := s.federation.GetSource(request.Context(), request.PathValue("sourceId"))
	if err != nil {
		s.fail(writer, request, err)
		return
	}
	setETag(writer, source.Revision)
	s.respondJSON(writer, request, http.StatusOK, source)
}

// handleSourceChangePreview projects proposed configuration and intent into an explicit change-impact preview.
func (s *Server) handleSourceChangePreview(writer http.ResponseWriter, request *http.Request) {
	var input sourceChangePreviewRequest
	if err := decodeJSON(writer, request, s.maxBodyBytes, &input); err != nil {
		s.fail(writer, request, err)
		return
	}
	domainInput := input.domainInput()
	domainInput.Intent = input.Intent
	preview, err := s.federation.PreviewChange(request.Context(), request.PathValue("sourceId"), domainInput)
	if err != nil {
		s.fail(writer, request, err)
		return
	}
	s.respondJSON(writer, request, http.StatusOK, preview)
}

// handleSourcePatch requires If-Match and passes the preview digest and audit reason with the proposed update.
func (s *Server) handleSourcePatch(writer http.ResponseWriter, request *http.Request) {
	revision, err := expectedRevision(request)
	if err != nil {
		s.fail(writer, request, err)
		return
	}
	var input sourceUpdateRequest
	if err := decodeJSON(writer, request, s.maxBodyBytes, &input); err != nil {
		s.fail(writer, request, err)
		return
	}
	domainInput := input.domainInput()
	domainInput.ChangePreviewDigest = input.ChangePreviewDigest
	domainInput.Reason = input.Reason
	source, err := s.federation.UpdateSource(request.Context(), request.PathValue("sourceId"), domainInput, revision)
	if err != nil {
		s.fail(writer, request, err)
		return
	}
	setETag(writer, source.Revision)
	s.respondJSON(writer, request, http.StatusOK, source)
}

// handleSourceTest tests the selected approved source revision after decoding an optional empty command body.
func (s *Server) handleSourceTest(writer http.ResponseWriter, request *http.Request) {
	revision, err := expectedRevision(request)
	if err != nil {
		s.fail(writer, request, err)
		return
	}
	if err := decodeOptionalEmptyObject(writer, request, s.maxBodyBytes, &struct{}{}); err != nil {
		s.fail(writer, request, err)
		return
	}
	result, err := s.federation.TestSourceAtRevision(request.Context(), request.PathValue("sourceId"), revision)
	if err != nil {
		s.fail(writer, request, err)
		return
	}
	setETag(writer, result.SourceRevision)
	s.respondJSON(writer, request, http.StatusOK, result)
}

// handleSourceSnapshots pages synchronization history with continuation bound to the exact source.
func (s *Server) handleSourceSnapshots(writer http.ResponseWriter, request *http.Request) {
	sourceID := request.PathValue("sourceId")
	pageRequest, err := s.parseAdminPage(request, "federation-source-snapshots", sourceID, "startedAt desc,id desc", map[string]string{})
	if err != nil {
		s.fail(writer, request, err)
		return
	}
	listRequest := federation.SnapshotListRequest{SourceID: sourceID, Limit: pageRequest.Limit}
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
		listRequest.AfterStartedAt, listRequest.AfterID = &position, pageRequest.Position[1]
	}
	items, err := s.federation.ListSnapshotPage(request.Context(), listRequest)
	if err != nil {
		s.fail(writer, request, err)
		return
	}
	page, err := finishCursorPage(s, pageRequest, items, func(item federation.Snapshot) []string {
		return []string{item.StartedAt.UTC().Format(time.RFC3339Nano), item.ID}
	})
	if err != nil {
		s.fail(writer, request, err)
		return
	}
	s.respondJSON(writer, request, http.StatusOK, page)
}

// handleSourceSync captures the approved source revision in a durable Job; the
// API request never performs external synchronization inline.
func (s *Server) handleSourceSync(writer http.ResponseWriter, request *http.Request) {
	revision, err := expectedRevision(request)
	if err != nil {
		s.fail(writer, request, err)
		return
	}
	if err := decodeOptionalEmptyObject(writer, request, s.maxBodyBytes, &struct{}{}); err != nil {
		s.fail(writer, request, err)
		return
	}
	source, err := s.federation.GetSource(request.Context(), request.PathValue("sourceId"))
	if err != nil {
		s.fail(writer, request, err)
		return
	}
	if source.Revision != revision {
		s.fail(writer, request, catalog.E(catalog.CodePreconditionFailed, "source revision does not match"))
		return
	}
	payload, err := json.Marshal(struct {
		SourceID       string `json:"sourceId"`
		SourceRevision int64  `json:"sourceRevision"`
		Trigger        string `json:"trigger"`
	}{SourceID: source.ID, SourceRevision: source.Revision, Trigger: "manual"})
	if err != nil {
		s.fail(writer, request, catalog.Wrap(catalog.CodeInternal, "encode source sync job", err))
		return
	}
	job, existing, err := s.jobs.Enqueue(request.Context(), jobs.EnqueueInput{Kind: "source.sync", PayloadVersion: 2, Payload: payload, DeduplicationKey: "source.sync:" + source.ID + ":" + strconv.FormatInt(source.Revision, 10), SourceSyncAudit: &jobs.SourceSyncRequestAudit{SourceID: source.ID, SourceRevision: source.Revision, Trigger: "manual"}})
	if err != nil {
		s.fail(writer, request, err)
		return
	}
	writer.Header().Set("Location", "/api/v1/jobs/"+job.ID)
	s.respondJSON(writer, request, http.StatusAccepted, sourceSyncAcceptedResponse{Job: jobs.PublicView(job), Existing: existing})
}

// handleSourcePause selects paused status through the shared reason/precondition boundary.
func (s *Server) handleSourcePause(writer http.ResponseWriter, request *http.Request) {
	s.handleSourceStatus(writer, request, federation.SourcePaused)
}

// handleSourceResume selects active status through the shared reason/precondition boundary.
func (s *Server) handleSourceResume(writer http.ResponseWriter, request *http.Request) {
	s.handleSourceStatus(writer, request, federation.SourceActive)
}

// handleSourceRemove selects removal through the shared preview/reason/precondition boundary.
func (s *Server) handleSourceRemove(writer http.ResponseWriter, request *http.Request) {
	s.handleSourceStatus(writer, request, federation.SourceRemoved)
}

// handleSourceStatus requires revision fencing and a reason, plus preview evidence for removal.
func (s *Server) handleSourceStatus(writer http.ResponseWriter, request *http.Request, status federation.SourceStatus) {
	revision, err := expectedRevision(request)
	if err != nil {
		s.fail(writer, request, err)
		return
	}
	var input federation.StatusChangeInput
	if status == federation.SourceRemoved {
		var body sourceRemovalRequest
		if err := decodeJSON(writer, request, s.maxBodyBytes, &body); err != nil {
			s.fail(writer, request, err)
			return
		}
		input = federation.StatusChangeInput{Reason: body.Reason, ChangePreviewDigest: body.ChangePreviewDigest}
	} else {
		var body sourceReasonRequest
		if err := decodeJSON(writer, request, s.maxBodyBytes, &body); err != nil {
			s.fail(writer, request, err)
			return
		}
		input.Reason = body.Reason
	}
	source, err := s.federation.SetStatus(request.Context(), request.PathValue("sourceId"), status, revision, input)
	if err != nil {
		s.fail(writer, request, err)
		return
	}
	if status == federation.SourceRemoved {
		writer.WriteHeader(http.StatusNoContent)
		return
	}
	setETag(writer, source.Revision)
	s.respondJSON(writer, request, http.StatusOK, source)
}

// handleObservations pages provenance with source/freshness filters and explicit version-presence ordering.
func (s *Server) handleObservations(writer http.ResponseWriter, request *http.Request) {
	query := request.URL.Query()
	sourceID := query.Get("sourceId")
	freshness := query.Get("freshness")
	_, sourcePresent := query["sourceId"]
	_, freshnessPresent := query["freshness"]
	if sourcePresent && sourceID == "" || freshnessPresent && freshness == "" || freshness != "" && freshness != "current" && freshness != "all" {
		s.fail(writer, request, catalog.E(catalog.CodeInvalidArgument, "sourceId must be a non-empty UUID and freshness must be current or all"))
		return
	}
	if freshness == "" {
		freshness = "current"
	}
	pageRequest, err := s.parseAdminPage(request, "observations", "", "identifier,versionPresent desc,version,id", map[string]string{"sourceId": sourceID, "freshness": freshness})
	if err != nil {
		s.fail(writer, request, err)
		return
	}
	listRequest := federation.ObservationListRequest{SourceID: sourceID, CurrentOnly: freshness == "current", Limit: pageRequest.Limit}
	if len(pageRequest.Position) > 0 {
		if len(pageRequest.Position) != 4 || pageRequest.Position[1] != "true" && pageRequest.Position[1] != "false" {
			s.fail(writer, request, catalog.E(catalog.CodeInvalidArgument, "cursor is invalid"))
			return
		}
		listRequest.AfterIdentifier = pageRequest.Position[0]
		listRequest.AfterVersionPresent = pageRequest.Position[1] == "true"
		listRequest.AfterVersion = pageRequest.Position[2]
		listRequest.AfterID = pageRequest.Position[3]
	}
	items, err := s.federation.ObservationPage(request.Context(), listRequest)
	if err != nil {
		s.fail(writer, request, err)
		return
	}
	page, err := finishCursorPage(s, pageRequest, items, func(item federation.Observation) []string {
		return []string{item.Identifier, strconv.FormatBool(item.VersionPresent), item.Version, item.ID}
	})
	if err != nil {
		s.fail(writer, request, err)
		return
	}
	s.respondJSON(writer, request, http.StatusOK, page)
}

// handleObservationRaw delegates the sensitive-read audit before returning the
// exact retained external evidence as an attachment.
func (s *Server) handleObservationRaw(writer http.ResponseWriter, request *http.Request) {
	observation, err := s.federation.GetRawObservation(request.Context(), request.PathValue("observationId"))
	if err != nil {
		s.fail(writer, request, err)
		return
	}
	writer.Header().Set("Content-Type", "application/json")
	writer.Header().Set("Content-Disposition", `attachment; filename="`+observation.ID+`.json"`)
	writer.Header().Set("Content-Length", strconv.Itoa(len(observation.RawEntry)))
	setContentDigest(writer, observation.RawEntry)
	writer.WriteHeader(http.StatusOK)
	_, _ = writer.Write(observation.RawEntry)
}

// handleObservationOccurrences pages retained occurrences under the exact observation and provenance position.
func (s *Server) handleObservationOccurrences(writer http.ResponseWriter, request *http.Request) {
	observationID := request.PathValue("observationId")
	pageRequest, err := s.parseAdminPage(request, "observation-occurrences", observationID, "observedAt desc,snapshotId desc,retrievalDigest,entryLocation", map[string]string{})
	if err != nil {
		s.fail(writer, request, err)
		return
	}
	listRequest := federation.OccurrenceListRequest{ObservationID: observationID, Limit: pageRequest.Limit}
	if len(pageRequest.Position) > 0 {
		if len(pageRequest.Position) != 4 {
			s.fail(writer, request, catalog.E(catalog.CodeInvalidArgument, "cursor is invalid"))
			return
		}
		position, parseErr := time.Parse(time.RFC3339Nano, pageRequest.Position[0])
		if parseErr != nil {
			s.fail(writer, request, catalog.E(catalog.CodeInvalidArgument, "cursor is invalid"))
			return
		}
		listRequest.AfterObservedAt = &position
		listRequest.AfterSnapshotID = pageRequest.Position[1]
		listRequest.AfterRetrievalDigest = pageRequest.Position[2]
		listRequest.AfterEntryLocation = pageRequest.Position[3]
	}
	items, err := s.federation.ObservationOccurrencePage(request.Context(), listRequest)
	if err != nil {
		s.fail(writer, request, err)
		return
	}
	page, err := finishCursorPage(s, pageRequest, items, func(item federation.ObservationOccurrence) []string {
		return []string{item.ObservedAt.UTC().Format(time.RFC3339Nano), item.SnapshotID, item.RetrievalDigest, item.EntryLocation}
	})
	if err != nil {
		s.fail(writer, request, err)
		return
	}
	s.respondJSON(writer, request, http.StatusOK, page)
}

// handleObservationGroup requires explicit identifier/version-presence input before reading grouped provenance.
func (s *Server) handleObservationGroup(writer http.ResponseWriter, request *http.Request) {
	if err := validateQueryParameters(request, "identifier", "version", "versionPresent"); err != nil {
		s.fail(writer, request, err)
		return
	}
	identifier := request.URL.Query().Get("identifier")
	presentValue := request.URL.Query().Get("versionPresent")
	if identifier == "" || presentValue == "" {
		s.fail(writer, request, catalog.E(catalog.CodeInvalidArgument, "identifier and versionPresent are required"))
		return
	}
	if presentValue != "true" && presentValue != "false" {
		s.fail(writer, request, catalog.E(catalog.CodeInvalidArgument, "versionPresent must be true or false"))
		return
	}
	versionPresent := presentValue == "true"
	group, err := s.federation.ObservationGroup(request.Context(), identifier, request.URL.Query().Get("version"), versionPresent)
	if err != nil {
		s.fail(writer, request, err)
		return
	}
	s.respondJSON(writer, request, http.StatusOK, group)
}

// dispatchObservationCommand accepts only :adopt and delegates explicit retained-observation adoption to federation governance.
func (s *Server) dispatchObservationCommand(writer http.ResponseWriter, request *http.Request) {
	value := request.PathValue("observationCommand")
	observationID, action, ok := strings.Cut(value, ":")
	if !ok || observationID == "" || action != "adopt" {
		s.fail(writer, request, catalog.E(catalog.CodeNotFound, "observation command was not found"))
		return
	}
	if !s.setUUIDPathValue(writer, request, "observationId", observationID) {
		return
	}
	var input federation.AdoptInput
	if err := decodeJSON(writer, request, s.maxBodyBytes, &input); err != nil {
		s.fail(writer, request, err)
		return
	}
	result, err := s.federation.AdoptObservation(request.Context(), observationID, input)
	if err != nil {
		s.fail(writer, request, err)
		return
	}
	writer.Header().Set("Location", "/api/v1/resources/"+result.Resource.ID)
	s.respondJSON(writer, request, http.StatusCreated, result)
}

// handleObservationSearch binds closed search input to signed score/identifier/ID continuation.
func (s *Server) handleObservationSearch(writer http.ResponseWriter, request *http.Request) {
	var input struct {
		Text   string         `json:"text"`
		Kinds  []catalog.Kind `json:"kinds,omitempty"`
		Tags   []string       `json:"tags,omitempty"`
		Limit  *int           `json:"limit,omitempty"`
		Cursor *string        `json:"cursor,omitempty"`
	}
	if err := decodeJSON(writer, request, s.maxBodyBytes, &input); err != nil {
		s.fail(writer, request, err)
		return
	}
	query := federation.SearchQuery{Text: input.Text, Kinds: input.Kinds, Tags: input.Tags}
	filterBytes, err := json.Marshal(query)
	if err != nil {
		s.fail(writer, request, catalog.Wrap(catalog.CodeInternal, "bind observation search cursor", err))
		return
	}
	pageRequest, err := s.parseObservationSearchPage(request, map[string]string{"query": string(filterBytes)}, input.Limit, input.Cursor)
	if err != nil {
		s.fail(writer, request, err)
		return
	}
	searchRequest := federation.ObservationSearchRequest{Query: query, Limit: pageRequest.Limit}
	if len(pageRequest.Position) > 0 {
		if len(pageRequest.Position) != 3 {
			s.fail(writer, request, catalog.E(catalog.CodeInvalidArgument, "cursor is invalid"))
			return
		}
		score, parseErr := strconv.Atoi(pageRequest.Position[0])
		if parseErr != nil || score < 0 || score > 100 {
			s.fail(writer, request, catalog.E(catalog.CodeInvalidArgument, "cursor is invalid"))
			return
		}
		searchRequest.AfterScore = score
		searchRequest.AfterIdentifier = pageRequest.Position[1]
		searchRequest.AfterID = pageRequest.Position[2]
	}
	items, err := s.federation.SearchObservationPage(request.Context(), searchRequest)
	if err != nil {
		s.fail(writer, request, err)
		return
	}
	page, err := finishCursorPage(s, pageRequest, items, func(item federation.SearchResult) []string {
		return []string{strconv.Itoa(item.Score), item.Observation.Identifier, item.Observation.ID}
	})
	if err != nil {
		s.fail(writer, request, err)
		return
	}
	s.respondJSON(writer, request, http.StatusOK, page)
}

// handleLiveFederationSearch marks explicit diagnostic queries administrative and records partial live outcomes.
func (s *Server) handleLiveFederationSearch(writer http.ResponseWriter, request *http.Request) {
	var query federation.LiveSearchRequest
	if err := decodeJSON(writer, request, s.maxBodyBytes, &query); err != nil {
		s.fail(writer, request, err)
		return
	}
	query.Administrative = true
	response, err := s.federation.LiveSearch(request.Context(), query)
	if err != nil {
		s.fail(writer, request, err)
		return
	}
	searchOutcome := observability.OutcomeSucceeded
	if response.Partial {
		searchOutcome = observability.OutcomePartial
	}
	observeRequestOperation(request, observability.OperationLiveSearch, searchOutcome)
	type administrativeResult struct {
		federation.LiveResult
		SourceRevision   int64  `json:"sourceRevision"`
		ExactEntryDigest string `json:"exactEntryDigest"`
	}
	results := make([]administrativeResult, 0, len(response.Results))
	for _, item := range response.Results {
		results = append(results, administrativeResult{item, item.SourceRevision, audit.DigestBytes(item.Entry)})
	}
	s.respondJSON(writer, request, http.StatusOK, struct {
		Results     []administrativeResult      `json:"results"`
		Partial     bool                        `json:"partial"`
		Diagnostics []federation.LiveDiagnostic `json:"diagnostics,omitempty"`
	}{results, response.Partial, response.Diagnostics})
}
