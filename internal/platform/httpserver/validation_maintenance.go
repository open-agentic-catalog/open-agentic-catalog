// Validation maintenance is a private, read-only moving inventory.
package httpserver

import (
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
)

// handleValidationMaintenance binds selection, observation time and current
// authority without fetching evidence or issuing a validation command.
func (s *Server) handleValidationMaintenance(w http.ResponseWriter, r *http.Request) {
	state := "attention"
	if r.URL.Query().Has("state") {
		state = r.URL.Query().Get("state")
	}
	hours := catalog.DefaultValidationHorizonHours
	if value, ok := r.URL.Query()["horizonHours"]; ok {
		parsed, err := strconv.Atoi(value[0])
		if err != nil || parsed < 1 || parsed > catalog.MaxValidationHorizonHours {
			s.fail(w, r, catalog.E(catalog.CodeInvalidArgument, "horizonHours must be an integer between 1 and 168"))
			return
		}
		hours = parsed
	}
	policy := s.catalog.ValidationMaintenancePolicy()
	parent := fmt.Sprintf("%s:%s:%s", policy.Profile, policy.Version, policy.ExternalDigest)
	page, err := s.parseAdminPage(r, "validation-maintenance", parent, "revision-id", map[string]string{"state": state, "horizonHours": strconv.Itoa(hours)})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	q := catalog.ValidationMaintenanceRequest{State: state, HorizonHours: hours, Limit: page.Limit, ObservedAt: s.now().UTC().Truncate(time.Microsecond)}
	if len(page.Position) > 0 {
		if len(page.Position) != 2 {
			s.fail(w, r, catalog.E(catalog.CodeInvalidArgument, "validation maintenance cursor is invalid"))
			return
		}
		q.AfterID = page.Position[0]
		q.ObservedAt, err = time.Parse(time.RFC3339Nano, page.Position[1])
		if err != nil || q.ObservedAt.After(s.now()) {
			s.fail(w, r, catalog.E(catalog.CodeInvalidArgument, "validation maintenance observation is invalid"))
			return
		}
	}
	items, err := s.catalog.ListValidationMaintenance(r.Context(), q)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	result, err := finishCursorPage(s, page, items, func(i catalog.ValidationMaintenanceItem) []string {
		return []string{i.RevisionID, q.ObservedAt.Format(time.RFC3339Nano)}
	})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	s.respondJSON(w, r, 200, struct {
		cursorPage[catalog.ValidationMaintenanceItem]
		ObservedAt   time.Time `json:"observedAt"`
		HorizonHours int       `json:"horizonHours"`
	}{result, q.ObservedAt, hours})
}
