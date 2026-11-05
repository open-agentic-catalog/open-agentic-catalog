package httpserver

import (
	"net/http"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/publication"
)

func (s *Server) handleOverviewAdministration(w http.ResponseWriter, r *http.Request) {
	value, err := s.identity.AdministrationOverview(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.respondJSON(w, r, http.StatusOK, value)
}

func (s *Server) handleOverviewFeedback(w http.ResponseWriter, r *http.Request) {
	value, err := s.feedback.Overview(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.respondJSON(w, r, http.StatusOK, value)
}

func (s *Server) handleOverviewRatings(w http.ResponseWriter, r *http.Request) {
	value, err := s.ratings.Overview(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.respondJSON(w, r, http.StatusOK, value)
}

func (s *Server) handleOverviewResources(w http.ResponseWriter, r *http.Request) {
	value, err := s.catalog.ResourceOverview(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.respondJSON(w, r, http.StatusOK, value)
}

func (s *Server) handleOverviewReviews(w http.ResponseWriter, r *http.Request) {
	value, err := s.catalog.ReviewOverview(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.respondJSON(w, r, http.StatusOK, value)
}

func (s *Server) handleOverviewPublication(w http.ResponseWriter, r *http.Request) {
	configs := []publication.HostConfiguration{}
	for host, binding := range s.discovery {
		if binding.TenantID == actorTenant(r) {
			configs = append(configs, publication.HostConfiguration{Host: host, Mode: string(binding.Mode), ARDProfile: s.ardProfile, AICatalogProfile: s.aiCatalogProfile})
		}
	}
	value, err := publication.ReadOverview(r.Context(), configs, s.publicationOverview, s.now())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.respondJSON(w, r, http.StatusOK, value)
}

func (s *Server) handleOverviewReleases(w http.ResponseWriter, r *http.Request) {
	value, err := s.catalog.ReleaseOverview(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.respondJSON(w, r, http.StatusOK, value)
}

func (s *Server) handleOverviewDirectory(measure catalog.InventoryMeasure) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		value, err := s.catalog.DirectoryOverview(r.Context(), measure)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		s.respondJSON(w, r, http.StatusOK, value)
	}
}

func (s *Server) handleOverviewIndexed(w http.ResponseWriter, r *http.Request) {
	value, err := s.federation.IndexedOverview(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.respondJSON(w, r, http.StatusOK, value)
}

func (s *Server) handleOverviewSources(w http.ResponseWriter, r *http.Request) {
	value, err := s.federation.SourceOverview(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.respondJSON(w, r, http.StatusOK, value)
}

func (s *Server) handleOverviewJobs(w http.ResponseWriter, r *http.Request) {
	value, err := s.jobs.Overview(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.respondJSON(w, r, http.StatusOK, value)
}

func (s *Server) handleOverviewWebhookEndpoints(w http.ResponseWriter, r *http.Request) {
	value, err := s.webhooks.EndpointOverview(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.respondJSON(w, r, http.StatusOK, value)
}

func (s *Server) handleOverviewWebhookDeliveries(w http.ResponseWriter, r *http.Request) {
	value, err := s.webhooks.DeliveryOverview(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.respondJSON(w, r, http.StatusOK, value)
}
