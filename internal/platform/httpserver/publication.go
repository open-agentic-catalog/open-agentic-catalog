// Publication handlers select a configured host and require publisher authority
// and current preconditions for state transitions.

package httpserver

import (
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/publication"
)

type publicationTarget struct {
	Host string `json:"host"`
}

type publicationTransitionRequest struct {
	Host   string `json:"host"`
	Reason string `json:"reason"`
}

type publicationHost struct {
	Host string        `json:"host"`
	Mode DiscoveryMode `json:"mode"`
}

type publicationHostInventory struct {
	Items []publicationHost `json:"items"`
}

// handlePublicationHosts exposes only the authenticated tenant's bounded configuration choices.
// Disabled bindings remain visible; publicationForControl still rejects their use.
func (s *Server) handlePublicationHosts(writer http.ResponseWriter, request *http.Request) {
	if err := requirePublicationPermission(request, "publication.read"); err != nil {
		s.fail(writer, request, err)
		return
	}
	items := make([]publicationHost, 0)
	for host, binding := range s.discovery {
		if binding.TenantID == actorTenant(request) {
			items = append(items, publicationHost{Host: host, Mode: binding.Mode})
		}
	}
	slices.SortFunc(items, func(a, b publicationHost) int { return strings.Compare(a.Host, b.Host) })
	s.respondJSON(writer, request, http.StatusOK, publicationHostInventory{Items: items})
}

// handlePublicationList requires global publisher authority and pages snapshots bound to one configured tenant host.
func (s *Server) handlePublicationList(writer http.ResponseWriter, request *http.Request) {
	if err := requirePublicationPermission(request, "publication.read"); err != nil {
		s.fail(writer, request, err)
		return
	}
	host := strings.TrimSpace(request.URL.Query().Get("host"))
	state := request.URL.Query().Get("state")
	if values, exists := request.URL.Query()["state"]; exists && (len(values) != 1 || state == "") {
		s.fail(writer, request, catalog.E(catalog.CodeInvalidArgument, "publication-snapshot state is invalid"))
		return
	}
	pageRequest, err := s.parseAdminPage(request, "publication-snapshots", host, "generation desc,id desc", map[string]string{"host": host, "state": state})
	if err != nil {
		s.fail(writer, request, err)
		return
	}
	manager, err := s.publicationForControl(actorTenant(request), host)
	if err != nil {
		s.fail(writer, request, err)
		return
	}
	listRequest := publication.SnapshotListRequest{Limit: pageRequest.Limit, State: publication.SnapshotState(state)}
	if len(pageRequest.Position) > 0 {
		if len(pageRequest.Position) != 2 {
			s.fail(writer, request, catalog.E(catalog.CodeInvalidArgument, "cursor is invalid"))
			return
		}
		generation, parseErr := strconv.ParseInt(pageRequest.Position[0], 10, 64)
		if parseErr != nil || generation < 1 {
			s.fail(writer, request, catalog.E(catalog.CodeInvalidArgument, "cursor is invalid"))
			return
		}
		listRequest.AfterGeneration, listRequest.AfterID = generation, pageRequest.Position[1]
	}
	items, err := manager.ListSnapshotSummaries(request.Context(), actorTenant(request), listRequest)
	if err != nil {
		s.fail(writer, request, err)
		return
	}
	page, err := finishCursorPage(s, pageRequest, items, func(item publication.SnapshotSummary) []string {
		return []string{strconv.FormatInt(item.Generation, 10), item.ID}
	})
	if err != nil {
		s.fail(writer, request, err)
		return
	}
	s.respondJSON(writer, request, http.StatusOK, page)
}

// handlePublicationBuild builds a candidate for the exact authorized host and returns its immutable ETag and Location.
func (s *Server) handlePublicationBuild(writer http.ResponseWriter, request *http.Request) {
	if err := requirePublicationPermission(request, "publication.generate"); err != nil {
		s.fail(writer, request, err)
		return
	}
	var target publicationTarget
	if err := decodeJSON(writer, request, s.maxBodyBytes, &target); err != nil {
		s.fail(writer, request, err)
		return
	}
	manager, err := s.publicationForControl(actorTenant(request), target.Host)
	if err != nil {
		s.fail(writer, request, err)
		return
	}
	snapshot, err := manager.BuildCandidate(request.Context(), actorTenant(request))
	if err != nil {
		s.fail(writer, request, err)
		return
	}
	writer.Header().Set("ETag", snapshot.ETag)
	writer.Header().Set("Location", "/api/v1/publication/snapshots/"+snapshot.ID+"?host="+url.QueryEscape(target.Host))
	s.respondJSON(writer, request, http.StatusCreated, snapshot)
}

// handlePublicationGet reads retained snapshot detail only through the tenant's configured host manager.
func (s *Server) handlePublicationGet(writer http.ResponseWriter, request *http.Request) {
	if err := requirePublicationPermission(request, "publication.read"); err != nil {
		s.fail(writer, request, err)
		return
	}
	host := strings.TrimSpace(request.URL.Query().Get("host"))
	manager, err := s.publicationForControl(actorTenant(request), host)
	if err != nil {
		s.fail(writer, request, err)
		return
	}
	snapshot, err := manager.GetSnapshot(request.Context(), actorTenant(request), request.PathValue("snapshotId"))
	if err != nil {
		s.fail(writer, request, err)
		return
	}
	writer.Header().Set("ETag", snapshot.ETag)
	s.respondJSON(writer, request, http.StatusOK, snapshot)
}

// handlePublicationActivate binds the command to both an exact configured host
// and the immutable candidate ETag before moving the active pointer.
func (s *Server) handlePublicationActivate(writer http.ResponseWriter, request *http.Request) {
	if err := requirePublicationPermission(request, "publication.activate"); err != nil {
		s.fail(writer, request, err)
		return
	}
	var target publicationTransitionRequest
	if err := decodeJSON(writer, request, s.maxBodyBytes, &target); err != nil {
		s.fail(writer, request, err)
		return
	}
	tenantID := actorTenant(request)
	manager, err := s.publicationForControl(tenantID, target.Host)
	if err != nil {
		s.fail(writer, request, err)
		return
	}
	snapshot, err := manager.GetSnapshot(request.Context(), tenantID, request.PathValue("snapshotId"))
	if err != nil {
		s.fail(writer, request, err)
		return
	}
	if request.Header.Get("If-Match") != snapshot.ETag {
		s.fail(writer, request, catalog.E(catalog.CodePreconditionFailed, "If-Match must contain the candidate snapshot ETag"))
		return
	}
	snapshot, err = manager.Activate(request.Context(), tenantID, snapshot.ID, target.Reason)
	if err != nil {
		s.fail(writer, request, err)
		return
	}
	writer.Header().Set("ETag", snapshot.ETag)
	s.respondJSON(writer, request, http.StatusOK, snapshot)
}

// handlePublicationUnpublish delegates host withdrawal with an explicit audit reason and returns the affected snapshot.
func (s *Server) handlePublicationUnpublish(writer http.ResponseWriter, request *http.Request) {
	if err := requirePublicationPermission(request, "publication.unpublish"); err != nil {
		s.fail(writer, request, err)
		return
	}
	var target publicationTransitionRequest
	if err := decodeJSON(writer, request, s.maxBodyBytes, &target); err != nil {
		s.fail(writer, request, err)
		return
	}
	manager, err := s.publicationForControl(actorTenant(request), target.Host)
	if err != nil {
		s.fail(writer, request, err)
		return
	}
	snapshot, err := manager.Unpublish(request.Context(), actorTenant(request), target.Reason)
	if err != nil {
		s.fail(writer, request, err)
		return
	}
	writer.Header().Set("ETag", snapshot.ETag)
	s.respondJSON(writer, request, http.StatusOK, snapshot)
}

// handlePublicationRollback requires the retained snapshot ETag before revalidating restoration through its host manager.
func (s *Server) handlePublicationRollback(writer http.ResponseWriter, request *http.Request) {
	if err := requirePublicationPermission(request, "publication.rollback"); err != nil {
		s.fail(writer, request, err)
		return
	}
	var target publicationTransitionRequest
	if err := decodeJSON(writer, request, s.maxBodyBytes, &target); err != nil {
		s.fail(writer, request, err)
		return
	}
	manager, err := s.publicationForControl(actorTenant(request), target.Host)
	if err != nil {
		s.fail(writer, request, err)
		return
	}
	snapshot, err := manager.GetSnapshot(request.Context(), actorTenant(request), request.PathValue("snapshotId"))
	if err != nil {
		s.fail(writer, request, err)
		return
	}
	if request.Header.Get("If-Match") != snapshot.ETag {
		s.fail(writer, request, catalog.E(catalog.CodePreconditionFailed, "If-Match must contain the retained snapshot ETag"))
		return
	}
	snapshot, err = manager.Rollback(request.Context(), actorTenant(request), snapshot.ID, target.Reason)
	if err != nil {
		s.fail(writer, request, err)
		return
	}
	writer.Header().Set("ETag", snapshot.ETag)
	s.respondJSON(writer, request, http.StatusOK, snapshot)
}

// actorTenant extracts the authenticated tenant for handlers that have already enforced role admission.
func actorTenant(request *http.Request) string {
	actor, _ := identity.FromContext(request.Context())
	return actor.TenantID
}

// requirePublisherRole requires tenant-wide publisher authority rather than a namespace-specific publishing grant.
func requirePublicationPermission(request *http.Request, action identity.Permission) error {
	_, err := identity.RequirePermission(request.Context(), action, "")
	if err != nil {
		return catalog.PermissionError("current publication permission is required", err)
	}
	return nil
}
