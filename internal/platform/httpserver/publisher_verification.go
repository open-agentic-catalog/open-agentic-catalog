package httpserver

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
)

// The token binds current stored identity, effective policy and the exact
// freshness projection. It includes no moving observation time or DNS reply.
func publisherDomainTag(tenant, publisher string, status catalog.PublisherDomainStatus, policy catalog.PublisherDomainPolicy) (string, error) {
	version := status.Version
	if version.ID != "" {
		version.PublisherRevision = 0
	}
	raw, err := json.Marshal(struct {
		Tenant    string                         `json:"tenant"`
		Publisher string                         `json:"publisher"`
		Version   catalog.PublisherDomainVersion `json:"version"`
		Policy    catalog.PublisherDomainPolicy  `json:"policy"`
		Status    catalog.PublisherDomainStatus  `json:"status"`
	}{tenant, publisher, version, policy, status})
	if err != nil {
		return "", catalog.Wrap(catalog.CodeInternal, "encode publisher domain revision", err)
	}
	sum := sha256.Sum256(raw)
	return `"publisher-domain-` + hex.EncodeToString(sum[:]) + `"`, nil
}

func (s *Server) respondPublisherDomain(w http.ResponseWriter, r *http.Request, status catalog.PublisherDomainStatus, value any) {
	actor, _ := identity.FromContext(r.Context())
	tag, err := publisherDomainTag(actor.TenantID, r.PathValue("publisherId"), status, s.catalog.PublisherDomainVerificationPolicy())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	w.Header().Set("ETag", tag)
	w.Header().Set("Cache-Control", "private, no-store")
	s.respondJSON(w, r, http.StatusOK, value)
}

func (s *Server) handlePublisherDomainStatus(w http.ResponseWriter, r *http.Request) {
	status, err := s.catalog.GetPublisherDomainVerification(r.Context(), r.PathValue("publisherId"), r.PathValue("domain"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.respondPublisherDomain(w, r, status, status)
}

func (s *Server) publisherDomainEnabled(w http.ResponseWriter, r *http.Request) bool {
	if s.catalog.PublisherDomainVerificationPolicy().Mode == catalog.DomainVerificationDisabled {
		s.fail(w, r, catalog.E(catalog.CodeNotFound, "publisher domain verification is disabled"))
		return false
	}
	return true
}

func (s *Server) handlePublisherDomainChallenge(w http.ResponseWriter, r *http.Request) {
	// The domain service enforces management permission before private disclosure.
	value, err := s.catalog.GetPublisherDomainChallenge(r.Context(), r.PathValue("publisherId"), r.PathValue("domain"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if !s.publisherDomainEnabled(w, r) {
		return
	}
	s.respondPublisherDomain(w, r, value.Status, value)
}

func (s *Server) publisherDomainCommand(w http.ResponseWriter, r *http.Request, action string) {
	// Managers need no companion read permission to prepare a management command.
	current, err := s.catalog.GetPublisherDomainChallenge(r.Context(), r.PathValue("publisherId"), r.PathValue("domain"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if !s.publisherDomainEnabled(w, r) {
		return
	}
	actor, _ := identity.FromContext(r.Context())
	tag, err := publisherDomainTag(actor.TenantID, r.PathValue("publisherId"), current.Status, s.catalog.PublisherDomainVerificationPolicy())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if strings.TrimSpace(r.Header.Get("If-Match")) != tag {
		s.fail(w, r, catalog.E(catalog.CodePreconditionFailed, "If-Match must contain the current publisher verification ETag"))
		return
	}
	reason, err := reasonBody(w, r, s.maxBodyBytes)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	publisher, domain := r.PathValue("publisherId"), r.PathValue("domain")
	if action == "issue" {
		value, err := s.catalog.IssuePublisherDomainChallenge(r.Context(), publisher, domain, reason, current.Status.Version)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		s.respondPublisherDomain(w, r, value.Status, value)
		return
	}
	var status catalog.PublisherDomainStatus
	if action == "check" {
		status, err = s.catalog.CheckPublisherDomain(r.Context(), publisher, domain, reason, current.Status.Version)
	} else {
		status, err = s.catalog.ClearPublisherDomainVerification(r.Context(), publisher, domain, reason, current.Status.Version)
	}
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.respondPublisherDomain(w, r, status, status)
}
func (s *Server) handlePublisherDomainIssue(w http.ResponseWriter, r *http.Request) {
	s.publisherDomainCommand(w, r, "issue")
}
func (s *Server) handlePublisherDomainCheck(w http.ResponseWriter, r *http.Request) {
	s.publisherDomainCommand(w, r, "check")
}
func (s *Server) handlePublisherDomainClear(w http.ResponseWriter, r *http.Request) {
	s.publisherDomainCommand(w, r, "clear")
}
