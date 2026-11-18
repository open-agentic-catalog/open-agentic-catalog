package httpserver

import (
	"net/http"
	"regexp"
	"strings"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/federation"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
)

// Apply before idempotency so administrator disablement also fences retained
// response disclosure. Authentication and OAuth still execute first.
func (s *Server) versionAdoptionAdmission(next http.Handler) http.Handler {

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		field, value := "", ""
		switch {
		case r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/api/v1/resources/") && strings.HasSuffix(r.URL.Path, "/versions:adopt"):
			field = "resourceId"
			value = strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/api/v1/resources/"), "/versions:adopt")
		case (r.Method == http.MethodGet || r.Method == http.MethodHead) && strings.HasPrefix(r.URL.Path, "/api/v1/observations/") && strings.HasSuffix(r.URL.Path, "/adoption-target"):
			field = "observationId"
			value = strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/api/v1/observations/"), "/adoption-target")
		}
		if field == "" || strings.Contains(value, "/") {
			next.ServeHTTP(w, r)
			return
		}
		// Preserve ordinary syntax refusal before feature admission, even when the
		// command cache would otherwise intercept the router's parameter wrappers.
		r.SetPathValue(field, value)
		admit := func(w http.ResponseWriter, r *http.Request) {
			if !s.federation.VersionAdoptionEnabled() {
				s.fail(w, r, catalog.E(catalog.CodeForbidden, "upstream Version adoption is disabled by the administrator"))
				return
			}
			if s.enforceOAuthScopes {
				actor, _ := identity.FromContext(r.Context())
				if actor.Claims["scope:sources.read"] != "true" {
					s.fail(w, r, catalog.E(catalog.CodeForbidden, "sources.read OAuth scope is required for source evidence"))
					return
				}
			}
			next.ServeHTTP(w, r)
		}
		s.withQueryParameters(s.withUUIDPathParameters(admit, field))(w, r)
	})
}

func (s *Server) handleVersionAdoptionTarget(w http.ResponseWriter, r *http.Request) {
	v, err := s.federation.VersionAdoptionTarget(r.Context(), r.PathValue("observationId"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.respondJSON(w, r, http.StatusOK, v)
}

func (s *Server) handleVersionAdoption(w http.ResponseWriter, r *http.Request) {
	var input federation.AdoptVersionInput
	if err := decodeJSON(w, r, s.maxBodyBytes, &input); err != nil {
		s.fail(w, r, err)
		return
	}
	if !identity.ValidTenantID(input.ObservationID) || !identity.ValidTenantID(input.SnapshotID) || !identity.ValidTenantID(input.PriorVersionRevisionID) || !adoptionDigestPattern.MatchString(input.RetrievalDigest) || len(input.EntryLocation) > 1024 || !adoptionLocationPattern.MatchString(input.EntryLocation) {
		s.fail(w, r, catalog.E(catalog.CodeInvalidArgument, "exact observation, occurrence and prior adoption references are required"))
		return
	}
	v, err := s.federation.AdoptVersion(r.Context(), r.PathValue("resourceId"), input)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	w.Header().Set("Location", "/api/v1/resource-versions/"+v.Version.ID)
	s.respondJSON(w, r, http.StatusCreated, v)
}

var adoptionDigestPattern = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)
var adoptionLocationPattern = regexp.MustCompile(`^/(entries|items)/(0|[1-9][0-9]*)$`)
