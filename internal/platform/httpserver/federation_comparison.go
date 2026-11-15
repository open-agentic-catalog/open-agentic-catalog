// Source comparison pages bind exact retained selections to current authority;
// summary reads never trigger synchronization or raw-evidence disclosure.
package httpserver

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/federation"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
)

// handleSourceSnapshotComparison reuses administration cursors, including the
// immutable common source revision in their signed position.
func (s *Server) handleSourceSnapshotComparison(writer http.ResponseWriter, request *http.Request) {
	query, err := parseQueryParameters(request)
	if err != nil {
		s.fail(writer, request, err)
		return
	}
	// UUID spellings identify the same records in PostgreSQL and memory. Use
	// canonical IDs for both lookup and cursor binding, without normalizing
	// any retained source entry identifier or version.
	before := strings.ToLower(request.PathValue("snapshotId"))
	other := strings.ToLower(query.Get("otherSnapshotId"))
	view := query.Get("view")
	if !query.Has("view") {
		view = "changes"
	}
	if !identity.ValidTenantID(other) || view != "changes" && view != "all" {
		s.fail(writer, request, catalog.E(catalog.CodeInvalidArgument, "otherSnapshotId must be a UUID and view must be changes or all"))
		return
	}
	pageRequest, err := s.parseAdminPage(request, "federation-snapshot-comparison", before, "sourceKey C,side,retrievalDigest C,entryLocation C", map[string]string{"otherSnapshotId": other, "view": view})
	if err != nil {
		s.fail(writer, request, err)
		return
	}
	selection := federation.SnapshotComparisonRequest{BeforeID: before, AfterID: other, IncludeUnchanged: view == "all", Limit: pageRequest.Limit}
	if len(pageRequest.Position) > 0 {
		if len(pageRequest.Position) != 2 {
			s.fail(writer, request, catalog.E(catalog.CodeInvalidArgument, "cursor is invalid"))
			return
		}
		revision, e1 := strconv.ParseInt(pageRequest.Position[0], 10, 64)
		position, e2 := strconv.Atoi(pageRequest.Position[1])
		if e1 != nil || e2 != nil || revision < 1 || position < 1 {
			s.fail(writer, request, catalog.E(catalog.CodeInvalidArgument, "cursor is invalid"))
			return
		}
		selection.ExpectedSourceRevision, selection.AfterPosition = revision, position
	}
	comparison, err := s.federation.CompareSnapshots(request.Context(), selection)
	if err != nil {
		s.fail(writer, request, err)
		return
	}
	page, err := finishCursorPage(s, pageRequest, comparison.Items, func(item federation.SnapshotEntryChange) []string {
		return []string{strconv.FormatInt(comparison.SourceRevision, 10), strconv.Itoa(item.Position)}
	})
	if err != nil {
		s.fail(writer, request, err)
		return
	}
	comparison.Items = page.Items
	s.respondJSON(writer, request, http.StatusOK, struct {
		federation.SnapshotComparison
		NextCursor string `json:"nextCursor,omitempty"`
	}{comparison, page.NextCursor})
}
