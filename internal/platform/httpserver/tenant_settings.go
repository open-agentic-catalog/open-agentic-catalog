package httpserver

import (
	"net/http"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/tenantsettings"
)

func (s *Server) handleTenantSettings(w http.ResponseWriter, r *http.Request) {
	if s.tenantSettings == nil {
		s.fail(w, r, catalog.E(catalog.CodeDependencyUnavailable, "tenant settings are unavailable"))
		return
	}
	var view tenantsettings.View
	var err error
	if r.Method == http.MethodGet {
		view, err = s.tenantSettings.Get(r.Context())
	} else {
		var input struct {
			Overrides *tenantsettings.Overrides `json:"overrides"`
			Reason    string                    `json:"reason"`
		}
		if err = decodeJSON(w, r, min(s.maxBodyBytes, 8192), &input); err != nil {
			s.fail(w, r, err)
			return
		}
		if input.Overrides == nil {
			s.fail(w, r, catalog.E(catalog.CodeInvalidArgument, "overrides is required"))
			return
		}
		view, err = s.tenantSettings.Save(r.Context(), *input.Overrides, r.Header.Get("If-Match"), input.Reason)
	}
	if err != nil {
		s.fail(w, r, err)
		return
	}
	w.Header().Set("ETag", s.tenantSettings.Tag(view))
	w.Header().Set("Cache-Control", "no-store")
	s.respondJSON(w, r, http.StatusOK, view)
}
