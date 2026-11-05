package httpserver

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/federation"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/jsondoc"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/ratings"
	"net/http"
	"strings"
	"time"
)

const ratingReceiptPurpose = "catalog-live-rating-v1"

type ratingReceipt struct {
	Purpose       string            `json:"purpose"`
	Tenant        string            `json:"tenant"`
	Principal     string            `json:"principal"`
	Authorization string            `json:"authorization"`
	Policy        string            `json:"policy"`
	Credential    string            `json:"credential"`
	Admission     ratings.Admission `json:"admission"`
	Expires       int64             `json:"expires"`
}
type ratingClock struct{ now func() time.Time }

func (c ratingClock) Now() time.Time { return c.now() }

type ratingAuthority struct {
	local      *catalog.Service
	federation *federation.Manager
}

func (a ratingAuthority) Admit(ctx context.Context, input ratings.Admission) (ratings.Admission, error) {
	t := input.Target
	if t.Origin == "local" {
		record, err := a.local.GetCatalogRecord(ctx, input.ReleaseID)
		if err != nil {
			return input, err
		}
		if record.Resource.ID != t.ResourceID || record.VersionRevision.ID != t.VersionRevisionID || record.Resource.CanonicalIdentifier != t.Identifier || record.Resource.Kind != t.Kind || record.Version.Version != t.Version {
			return input, catalog.E(catalog.CodePreconditionFailed, "rated version changed")
		}
		input.NamespaceID = record.Resource.NamespaceID
		input.EntryDigest = record.VersionRevision.ContentSetDigest
		input.SourceRevision = 0
		return input, nil
	}
	return a.federation.AdmitRating(ctx, input)
}
func (s *Server) ratingsRoutes(router *http.ServeMux) {
	if s.ratings == nil {
		return
	}
	router.HandleFunc("GET /api/v1/overview/ratings", s.withQueryParameters(s.handleOverviewRatings))
	router.HandleFunc("POST /api/v1/ratings/read", s.withQueryParameters(s.handleRatingRead))
	router.HandleFunc("POST /api/v1/ratings", s.withQueryParameters(s.handleRatingSubmit))
	router.HandleFunc("GET /api/v1/ratings", s.withQueryParameters(s.handleRatingList, "state", "limit", "cursor"))
	router.HandleFunc("GET /api/v1/ratings/inbox", s.withQueryParameters(s.handleRatingList, "state", "limit", "cursor"))
	router.HandleFunc("GET /api/v1/ratings/sharing", s.withQueryParameters(s.handleRatingSharing, "host", "audience"))
	router.HandleFunc("POST /api/v1/ratings/sharing", s.withQueryParameters(s.handleRatingSharing))
	router.HandleFunc("GET /api/v1/ratings/{ratingId}", s.withQueryParameters(s.withUUIDPathParameters(s.handleRatingGet, "ratingId")))
	router.HandleFunc("POST /api/v1/ratings/{ratingCommand}", s.withQueryParameters(s.handleRatingCommand))
}

type ratingRequest struct {
	Target    ratings.Target `json:"target"`
	ReleaseID string         `json:"releaseId,omitempty"`
	Receipt   string         `json:"receipt,omitempty"`
}
type ratingSubmitRequest struct {
	Target    ratings.Target `json:"target"`
	ReleaseID string         `json:"releaseId,omitempty"`
	Receipt   string         `json:"receipt,omitempty"`
	Score     int            `json:"score"`
	Consent   *bool          `json:"consent,omitempty"`
}

func (s *Server) ratingAdmission(ctx context.Context, input ratingRequest) (ratings.Admission, error) {
	a := ratings.Admission{Target: input.Target, ReleaseID: input.ReleaseID}
	if input.Receipt != "" {
		proof, err := s.verifyRatingReceipt(ctx, input.Receipt)
		if err != nil {
			return a, err
		}
		if proof.Admission.Target != a.Target || input.ReleaseID != "" {
			return a, catalog.E(catalog.CodePreconditionFailed, "rating receipt target changed")
		}
		a = proof.Admission
		a.Live = true
	}
	return a, nil
}
func (s *Server) handleRatingRead(w http.ResponseWriter, r *http.Request) {
	var input ratingRequest
	if err := decodeJSON(w, r, min(s.maxBodyBytes, 8192), &input); err != nil {
		s.fail(w, r, err)
		return
	}
	a, err := s.ratingAdmission(r.Context(), input)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	view, err := s.ratings.Read(r.Context(), a)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	s.respondJSON(w, r, 200, view)
}
func (s *Server) handleRatingSubmit(w http.ResponseWriter, r *http.Request) {
	var input ratingSubmitRequest
	if err := decodeJSON(w, r, min(s.maxBodyBytes, 8192), &input); err != nil {
		s.fail(w, r, err)
		return
	}
	a, err := s.ratingAdmission(r.Context(), ratingRequest{input.Target, input.ReleaseID, input.Receipt})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	expected := int64(0)
	if r.Header.Get("If-Match") != "" {
		expected, err = expectedRevision(r)
		if err != nil {
			s.fail(w, r, err)
			return
		}
	}
	record, err := s.ratings.Submit(r.Context(), a, input.Score, input.Consent, expected)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	status := 200
	if expected == 0 {
		status = 201
	}
	s.respondRating(w, r, record, status)
}
func (s *Server) respondRating(w http.ResponseWriter, r *http.Request, record ratings.Record, status int) {
	setETag(w, record.Revision)
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Location", "/api/v1/ratings/"+record.ID)
	s.respondJSON(w, r, status, feedbackReceipt{ID: record.ID, Revision: record.Revision})
}
func (s *Server) handleRatingGet(w http.ResponseWriter, r *http.Request) {
	record, err := s.ratings.Get(r.Context(), r.PathValue("ratingId"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	setETag(w, record.Revision)
	w.Header().Set("Cache-Control", "no-store")
	s.respondJSON(w, r, 200, record)
}
func (s *Server) handleRatingList(w http.ResponseWriter, r *http.Request) {
	inbox := strings.HasSuffix(r.URL.Path, "/inbox")
	purpose := "ratings-own"
	if inbox {
		purpose = "ratings-inbox"
	}
	// Reuse signed administration cursors while retaining the ratings page default.
	if !r.URL.Query().Has("limit") {
		query := r.URL.Query()
		query.Set("limit", "25")
		r.URL.RawQuery = query.Encode()
	}
	filters := map[string]string{"state": r.URL.Query().Get("state")}
	p, err := s.parseAdminPage(r, purpose, "", "id", filters)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	q := ratings.ListInput{State: filters["state"], Limit: p.Limit}
	if len(p.Position) > 0 {
		if len(p.Position) != 1 {
			s.fail(w, r, catalog.E(catalog.CodeInvalidArgument, "rating cursor is invalid"))
			return
		}
		q.AfterID = p.Position[0]
	}
	items, err := s.ratings.List(r.Context(), q, inbox)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	page, err := finishCursorPage(s, p, items, func(v ratings.Record) []string { return []string{v.ID} })
	if err != nil {
		s.fail(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	s.respondJSON(w, r, 200, page)
}
func (s *Server) handleRatingCommand(w http.ResponseWriter, r *http.Request) {
	id, action, ok := strings.Cut(r.PathValue("ratingCommand"), ":")
	if !ok || (action != "withdraw" && action != "remove" && action != "permit") {
		s.fail(w, r, catalog.E(catalog.CodeNotFound, "rating command was not found"))
		return
	}
	if !s.setUUIDPathValue(w, r, "ratingId", id) {
		return
	}
	expected, err := expectedRevision(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	var input struct {
		Reason string `json:"reason,omitempty"`
	}
	if err = decodeJSON(w, r, min(s.maxBodyBytes, 8192), &input); err != nil {
		s.fail(w, r, err)
		return
	}
	record, err := s.ratings.Change(r.Context(), id, expected, action, input.Reason)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.respondRating(w, r, record, 200)
}
func (s *Server) handleRatingSharing(w http.ResponseWriter, r *http.Request) {
	host, audience := r.URL.Query().Get("host"), r.URL.Query().Get("audience")
	var input struct {
		Host     string `json:"host"`
		Audience string `json:"audience"`
		Enabled  *bool  `json:"enabled"`
		Reason   string `json:"reason"`
	}
	if r.Method == http.MethodPost {
		if err := decodeJSON(w, r, min(s.maxBodyBytes, 8192), &input); err != nil {
			s.fail(w, r, err)
			return
		}
		if input.Enabled == nil {
			s.fail(w, r, catalog.E(catalog.CodeInvalidArgument, "rating sharing enabled choice is required"))
			return
		}
		host, audience = input.Host, input.Audience
	}
	actor, _ := identity.FromContext(r.Context())
	binding, ok := s.discovery[host]
	if !ok || binding.TenantID != actor.TenantID || string(binding.Mode) != audience {
		s.fail(w, r, catalog.E(catalog.CodeInvalidArgument, "rating sharing requires a configured publication audience"))
		return
	}
	var record ratings.Sharing
	var err error
	if r.Method == http.MethodGet {
		record, err = s.ratings.Sharing(r.Context(), host, audience)
	} else {
		expected := int64(0)
		if r.Header.Get("If-Match") != "" {
			expected, err = expectedRevision(r)
		}
		if err == nil {
			record, err = s.ratings.SetSharing(r.Context(), host, audience, *input.Enabled, expected, input.Reason)
		}
	}
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if record.Revision > 0 {
		setETag(w, record.Revision)
	}
	w.Header().Set("Cache-Control", "no-store")
	s.respondJSON(w, r, 200, record)
}
func (s *Server) signRatingReceipt(ctx context.Context, item catalog.CatalogSearchResult) (string, error) {
	actor, ok := identity.FromContext(ctx)
	if !ok || item.Live == nil {
		return "", catalog.E(catalog.CodeUnauthenticated, "rating authentication is required")
	}
	authority, err := identity.CurrentDecisionDigest(ctx)
	if err != nil {
		return "", catalog.PermissionError("rating authority is required", err)
	}
	a := ratingItemAdmission(item)
	if ratings.ValidateTarget(a.Target) != nil || a.EntryDigest == "" || a.SourceRevision < 1 {
		return "", catalog.E(catalog.CodeInternal, "invalid live rating evidence")
	}
	proof := ratingReceipt{Purpose: ratingReceiptPurpose, Tenant: actor.TenantID, Principal: actor.CommandPrincipal(), Authorization: authority, Policy: item.Live.PolicyDigest, Credential: item.Live.CredentialRevision, Admission: a, Expires: s.now().Add(5 * time.Minute).Unix()}
	raw, err := json.Marshal(proof)
	if err != nil {
		return "", err
	}
	payload := base64.RawURLEncoding.EncodeToString(raw)
	mac := hmac.New(sha256.New, s.cursorKey)
	_, _ = mac.Write([]byte(ratingReceiptPurpose + "\x00" + payload))
	token := payload + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	if len(token) > 4096 {
		return "", catalog.E(catalog.CodeWorkLimitExceeded, "rating receipt exceeds its bound")
	}
	return token, nil
}
func (s *Server) verifyRatingReceipt(ctx context.Context, token string) (ratingReceipt, error) {
	invalid := func() (ratingReceipt, error) {
		return ratingReceipt{}, catalog.E(catalog.CodePreconditionFailed, "live rating authority expired or changed; search again")
	}
	if token == "" || len(token) > 4096 {
		return invalid()
	}
	payload, signature, ok := strings.Cut(token, ".")
	if !ok {
		return invalid()
	}
	raw, err := base64.RawURLEncoding.Strict().DecodeString(payload)
	if err != nil || base64.RawURLEncoding.EncodeToString(raw) != payload {
		return invalid()
	}
	sig, err := base64.RawURLEncoding.Strict().DecodeString(signature)
	if err != nil || len(sig) != sha256.Size || base64.RawURLEncoding.EncodeToString(sig) != signature {
		return invalid()
	}
	mac := hmac.New(sha256.New, s.cursorKey)
	_, _ = mac.Write([]byte(ratingReceiptPurpose + "\x00" + payload))
	if !hmac.Equal(sig, mac.Sum(nil)) {
		return invalid()
	}
	var proof ratingReceipt
	if jsondoc.Decode(raw, &proof) != nil || proof.Purpose != ratingReceiptPurpose || proof.Expires <= s.now().Unix() || proof.Expires > s.now().Add(5*time.Minute).Unix() || ratings.ValidateTarget(proof.Admission.Target) != nil || proof.Admission.Target.Origin != "foreign" {
		return invalid()
	}
	actor, ok := identity.FromContext(ctx)
	authority, err := identity.CurrentDecisionDigest(ctx)
	if !ok || err != nil || proof.Tenant != actor.TenantID || proof.Principal != actor.CommandPrincipal() || proof.Authorization != authority {
		return invalid()
	}
	t := proof.Admission.Target
	source, err := s.federation.ArtworkSourceContext(ctx, federation.ArtworkTarget{SourceID: t.SourceID, PresentationGeneration: t.PresentationGeneration, Identifier: t.Identifier, VersionPresent: t.VersionPresent, Version: t.Version, Kind: t.Kind}, proof.Admission.SourceRevision, true)
	if err != nil || source.PolicyDigest != proof.Policy || source.CredentialRevision != proof.Credential {
		return invalid()
	}
	return proof, nil
}
func ratingItemAdmission(item catalog.CatalogSearchResult) ratings.Admission {
	a := ratings.Admission{Target: ratings.Target{Identifier: item.Identifier, Kind: item.Kind, Version: item.Version, VersionPresent: item.VersionPresent}}
	switch {
	case item.Local != nil:
		a.Target.Origin = "local"
		a.Target.ResourceID = item.Local.Resource.ID
		a.Target.VersionRevisionID = item.Local.VersionRevision.ID
		a.ReleaseID = item.Local.Release.ID
	case item.Indexed != nil:
		a.Target.Origin = "foreign"
		a.Target.SourceID = item.Indexed.PresentationSourceID
		a.Target.PresentationGeneration = item.Indexed.PresentationGeneration
		a.SourceRevision = item.Indexed.PresentationSourceRevision
		a.EntryDigest = item.Indexed.NormalizedEntryDigest
	case item.Live != nil:
		a.Target.Origin = "foreign"
		a.Target.SourceID = item.Live.SourceID
		a.Target.PresentationGeneration = item.Live.PresentationGeneration
		a.SourceRevision = item.Live.SourceRevision
		a.EntryDigest = artworkDigest(item.Live.Entry)
		a.Live = true
	}
	return a
}
func (s *Server) resolveRatings(ctx context.Context, items []catalog.CatalogSearchResult) ([]catalog.CatalogSearchResult, error) {
	if s.ratings == nil {
		return items, nil
	}
	var err error
	items, err = s.federation.ResolveRatingSignals(ctx, items)
	if err != nil {
		return nil, err
	}
	targets := []ratings.Target{}
	for _, item := range items {
		target := ratingItemAdmission(item).Target
		if ratings.ValidateTarget(target) == nil {
			targets = append(targets, target)
		}
	}
	totals, err := s.ratings.SelectedSummaries(ctx, targets)
	if err != nil {
		return nil, err
	}
	for i := range items {
		item := &items[i]
		target := ratingItemAdmission(*item).Target
		if ratings.ValidateTarget(target) != nil {
			continue
		}
		aggregate := totals[ratings.Key(target)]
		item.Ratings = &catalog.RatingSummary{Count: aggregate.Count}
		if aggregate.Count > 0 {
			average := aggregate.Average()
			item.Ratings.Average = &average
		}
		if item.Live != nil {
			item.Ratings.Receipt, err = s.signRatingReceipt(ctx, *item)
			if err != nil {
				return nil, err
			}
		}
	}
	return items, nil
}
