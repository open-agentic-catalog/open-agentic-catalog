// Public discovery serves active immutable snapshots and approved federation
// metadata through host and audience authorization.

package httpserver

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/adapter"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/federation"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/publication"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/standards"
)

// handleARDManifest serves the host-authorized active ARD snapshot with retained identity and conditional caching.
func (s *Server) handleARDManifest(writer http.ResponseWriter, request *http.Request) {
	binding, ok := s.authorizeDiscovery(writer, request)
	if !ok {
		return
	}
	snapshot, err := binding.Publication.Active(request.Context(), binding.TenantID)
	if err != nil {
		writeARDProblem(writer, request, publicSnapshotError(err, "ARD"))
		return
	}
	modifiedAt := snapshotModifiedAt(snapshot)
	setDiscoveryDocumentHeaders(writer, binding, snapshot.ETag, snapshot.ARDContentDigest, "application/json", modifiedAt.UTC().Format(http.TimeFormat))
	if publicNotModified(request, snapshot.ETag, modifiedAt) {
		writer.WriteHeader(http.StatusNotModified)
		return
	}
	s.respondARDJSON(writer, request, "application/json", snapshot.ARD)
}

// handleAICatalog serves the active AI Catalog document with its own media type, digest and ETag.
func (s *Server) handleAICatalog(writer http.ResponseWriter, request *http.Request) {
	binding, ok := s.authorizeDiscovery(writer, request)
	if !ok {
		return
	}
	snapshot, err := binding.Publication.Active(request.Context(), binding.TenantID)
	if err != nil {
		writeARDProblem(writer, request, publicSnapshotError(err, "AI Catalog"))
		return
	}
	modifiedAt := snapshotModifiedAt(snapshot)
	setDiscoveryDocumentHeaders(writer, binding, snapshot.AICatalogETag, snapshot.AIContentDigest, "application/ai-catalog+json", modifiedAt.UTC().Format(http.TimeFormat))
	if publicNotModified(request, snapshot.AICatalogETag, modifiedAt) {
		writer.WriteHeader(http.StatusNotModified)
		return
	}
	s.respondARDJSON(writer, request, "application/ai-catalog+json", snapshot.AICatalog)
}

// handleARDSearch serves the active local snapshot first. ARD auto federation
// replays one unchanged bounded live window. Hybrid results preserve durable
// order before the live tail; referrals disclose only approved sources.
func (s *Server) handleARDSearch(writer http.ResponseWriter, request *http.Request) {
	binding, ok := s.authorizeDiscovery(writer, request)
	if !ok {
		return
	}
	var query publication.SearchRequest
	if err := decodeJSON(writer, request, s.maxBodyBytes, &query); err != nil {
		writeARDProblem(writer, request, err)
		return
	}
	if query.PageSize == 0 {
		query.PageSize = 10
	}
	local, err := binding.Publication.Search(request.Context(), binding.TenantID, query)
	if err != nil {
		writeARDProblem(writer, request, err)
		return
	}
	switch query.Federation {
	case "", "auto":
		if query.PageToken != "" && !local.NeedsLiveReplay(false) {
			break
		}
		filter, filterErr := publication.ResolveSearchFilter(query.Query)
		if filterErr != nil {
			writeARDProblem(writer, request, filterErr)
			return
		}
		liveContext := discoveryFederationContext(request.Context(), binding.TenantID, binding.Host)
		audience := federation.AudienceAnonymous
		if binding.Mode == DiscoveryAuthenticated {
			audience = federation.AudienceAuthenticated
		}
		live, liveErr := s.federation.LiveSearch(liveContext, federation.LiveSearchRequest{Context: query.Query.Context, Text: query.Query.Text, Filter: query.Query.Filter, PageSize: catalog.LiveSearchWindow, Replay: true, DiscoveryHost: binding.Host, Audience: audience})
		if liveErr != nil {
			if query.PageToken != "" {
				writeARDProblem(writer, request, catalog.E(catalog.CodeDependencyUnavailable, "live search continuation is unavailable; retry the same token"))
				return
			}
			local.Partial = true
			local.Diagnostics = append(local.Diagnostics, publication.SourceDiagnostic{Source: "federation", Status: "failed", Message: "Live federation was unavailable within the request deadline."})
			break
		}
		{
			remote := local
			remote.Results, remote.PageToken = nil, ""
			mergeLiveResults(request.Context(), &remote, live, filter)
			fingerprint := live.ReplayFingerprint
			for _, d := range remote.Diagnostics {
				if d.Status == "invalid_response" {
					fingerprint = ""
				}
			}
			local.Partial, local.Diagnostics = remote.Partial, remote.Diagnostics
			local, err = binding.Publication.ContinueLiveSearch(local, remote.Results, fingerprint)
			if err != nil {
				writeARDProblem(writer, request, err)
				return
			}
			break
		}
	case "referrals":
		audience := federation.AudienceAnonymous
		if binding.Mode == DiscoveryAuthenticated {
			audience = federation.AudienceAuthenticated
		}
		sources, partial, listErr := s.federation.Referrals(discoveryFederationContext(request.Context(), binding.TenantID, binding.Host), binding.Host, audience)
		if listErr != nil {
			local.Partial = true
			local.Diagnostics = append(local.Diagnostics, publication.SourceDiagnostic{Source: "federation", Status: "failed", Message: "Federation referrals were unavailable within the request."})
		} else {
			for _, source := range sources {
				local.Referrals = append(local.Referrals, publication.Referral{Identifier: "urn:air:" + binding.PublisherDomain + ":registry:" + source.SourceID, DisplayName: source.DisplayName, Type: "application/ai-registry+json", URL: source.URL})
			}
			if partial {
				local.Partial = true
				local.Diagnostics = append(local.Diagnostics, publication.SourceDiagnostic{Source: "federation", Status: "fanout_limited", Message: "Eligible referrals exceeded the deployment fan-out."})
			}
		}
	case "none":
	default:
		writeARDProblem(writer, request, catalog.E(catalog.CodeInvalidArgument, "federation mode is invalid"))
		return
	}
	writer.Header().Set("Cache-Control", "no-store")
	if binding.Mode == DiscoveryAuthenticated {
		addVary(writer.Header(), "Authorization")
	}
	writer.Header().Set("Link", publicDiscoveryLinks())
	if local.Partial {
		writer.Header().Set("Server-Timing", `federation;desc="partial"`)
		s.federationWarning.Warn(s.now(), s.logger, "public ARD search completed with partial federation", requestLogAttributes(request, "event", "federation.partial", "component", "api", "diagnostic_count", len(local.Diagnostics))...)
	}
	setSearchModeHeaders(writer, local.Tier, local.FallbackReason, local.PartialIndex)
	s.respondARDJSON(writer, request, "application/json", local)
}

// discoveryFederationContext creates the host-bound internal discovery actor used by approved live/referral reads.
func discoveryFederationContext(ctx context.Context, tenantID, host string) context.Context {
	ctx = identity.WithActor(ctx, identity.Actor{TenantID: tenantID, Subject: "discovery-registry", ActorType: identity.ActorService})
	bound, err := identity.WithInternalAuthority(ctx, "federation.discover", tenantID, host, func(ctx context.Context) error { return ctx.Err() })
	if err != nil {
		return ctx
	} // Invalid composition has no discovery authority.
	return bound
}

// mergeLiveResults requires bound source evidence, preserves local identity precedence and orders merged metadata deterministically.
func mergeLiveResults(ctx context.Context, response *publication.SearchResponse, live federation.LiveSearchResponse, filter map[string]any) {
	index := make(map[string]int, len(response.Results))
	for position, result := range response.Results {
		index[searchResultKey(result.Identifier, result.Version)] = position
	}
	for _, remote := range live.Results {
		if remote.SourceID == "" || remote.RespondingURL == "" || !catalog.ValidExternalHTTPSURL(remote.Source) {
			response.Partial = true
			response.Diagnostics = append(response.Diagnostics, publication.SourceDiagnostic{Source: "federation", Status: "invalid_response", Message: "A live result lacked bound responding-source evidence."})
			continue
		}
		var result publication.SearchResult
		var projection struct {
			publication.Entry
			// Shadow the selected extension's array type: a peer can return
			// a compacted scalar or another prefix. Resolve it separately.
			RawSubjects json.RawMessage `json:"http://purl.org/dc/terms/subject"`
		}
		// Project only exact ARD names; extensions cannot replace canonical
		// fields. Keep numbers exact within context and scalar metadata maps.
		projectionErr := jsonv2.Unmarshal(remote.Entry, &projection, json.DefaultOptionsV1(), jsonv2.MatchCaseInsensitiveNames(false), jsonv2.WithUnmarshalers(jsonv2.UnmarshalFunc(func(raw []byte, target *any) error {
			decoder := json.NewDecoder(strings.NewReader(string(raw)))
			decoder.UseNumber()
			return decoder.Decode(target)
		})))
		var fields map[string]json.RawMessage
		if json.Unmarshal(remote.Entry, &fields) != nil || fields == nil || projectionErr != nil {
			response.Partial = true
			response.Diagnostics = append(response.Diagnostics, publication.SourceDiagnostic{Source: "federation", Status: "invalid_response", Message: "A live result could not be projected as ARD metadata."})
			continue
		}
		result.Entry = projection.Entry
		// Live admission validated either spelling and its publisher binding.
		// Project the declaration without changing retained evidence or fetching it.
		result.TrustManifest = fields["trustManifest"]
		if len(result.TrustManifest) == 0 {
			result.TrustManifest = fields["TrustManifest"]
		}
		subjects, subjectPresent, subjectErr := standards.ARDSubjectValues(fields)
		_, subjectFiltered := filter[standards.ARDSubjectIRI]
		if subjectErr != nil && (subjectPresent || subjectFiltered) {
			response.Partial = true
			response.Diagnostics = append(response.Diagnostics, publication.SourceDiagnostic{Source: "federation", Status: "invalid_response", Message: "A live result's topic declaration could not be interpreted locally."})
			continue
		}
		if subjectPresent && subjectErr == nil {
			result.Subjects = subjects
			// Mark disclosure by its IRI, independent of the peer's prefix.
			fields[standards.ARDSubjectIRI] = json.RawMessage(`[]`)
			result.Context = publication.ARDContext
		}
		result.Identifier = remote.Identifier
		result.Version = remote.Version
		result.Score = remote.Score
		result.Source = remote.Source
		matches, matchErr := publication.MatchDisclosedFilter(ctx, result.Entry, fields, filter)
		if matchErr != nil || !matches {
			response.Partial = true
			response.Diagnostics = append(response.Diagnostics, publication.SourceDiagnostic{Source: "federation", Status: "invalid_response", Message: "A live result contradicted a structured query filter."})
			continue
		}
		key := searchResultKey(result.Identifier, result.Version)
		if response.ContainsLocalIdentity(result.Identifier, result.Version) {
			continue
		}
		if _, ok := index[key]; ok {
			continue
		}
		index[key] = len(response.Results)
		response.Results = append(response.Results, result)
	}
	if response.Tier != "hybrid" {
		sort.Slice(response.Results, func(i, j int) bool {
			if response.Results[i].Score != response.Results[j].Score {
				return response.Results[i].Score > response.Results[j].Score
			}
			if response.Results[i].Identifier != response.Results[j].Identifier {
				return response.Results[i].Identifier < response.Results[j].Identifier
			}
			if response.Results[i].Version != response.Results[j].Version {
				return response.Results[i].Version < response.Results[j].Version
			}
			return response.Results[i].Source < response.Results[j].Source
		})
	}
	response.Partial = response.Partial || live.Partial
	for _, diagnostic := range live.Diagnostics {
		source := diagnostic.SourceID
		if source == "" {
			source = "federation"
		}
		response.Diagnostics = append(response.Diagnostics, publication.SourceDiagnostic{Source: source, Status: diagnostic.Status, Message: diagnostic.Message})
	}
}

// searchResultKey separates identifier/version pairs with a NUL boundary for result deduplication.
func searchResultKey(identifier, version string) string {
	return identifier + "\x00" + version
}

// handleARDExplore runs facet exploration against the authorized host's active snapshot.
func (s *Server) handleARDExplore(writer http.ResponseWriter, request *http.Request) {
	binding, ok := s.authorizeDiscovery(writer, request)
	if !ok {
		return
	}
	var query publication.ExploreRequest
	if err := decodeJSON(writer, request, s.maxBodyBytes, &query); err != nil {
		writeARDProblem(writer, request, err)
		return
	}
	response, err := binding.Publication.Explore(request.Context(), binding.TenantID, query)
	if err != nil {
		writeARDProblem(writer, request, err)
		return
	}
	writer.Header().Set("Cache-Control", "no-store")
	if binding.Mode == DiscoveryAuthenticated {
		addVary(writer.Header(), "Authorization")
	}
	writer.Header().Set("Link", publicDiscoveryLinks())
	setSearchModeHeaders(writer, response.Tier, response.FallbackReason, response.PartialIndex)
	s.respondARDJSON(writer, request, "application/json", response)
}

// setSearchModeHeaders preserves ARD body schemas and exposes only fixed safe
// mode/fallback labels, never provider identity or tenant indexing diagnostics.
func setSearchModeHeaders(writer http.ResponseWriter, tier, reason string, partial bool) {
	if tier == "hybrid" {
		writer.Header().Set("Catalog-Search-Tier", "hybrid")
		if partial {
			writer.Header().Set("Catalog-Search-Partial-Index", "true")
		}
	} else {
		writer.Header().Set("Catalog-Search-Tier", "core")
	}
	switch reason {
	case "indexing", "provider_unavailable", "index_changed", "work_limit":
		writer.Header().Set("Catalog-Search-Fallback", reason)
	}
}

// handleARDList validates bounded list paging and applies the host's public/private cache policy.
func (s *Server) handleARDList(writer http.ResponseWriter, request *http.Request) {
	binding, ok := s.authorizeDiscovery(writer, request)
	if !ok {
		return
	}
	pageSize := 20
	if request.URL.Query().Has("pageSize") {
		parsed, err := strconv.Atoi(request.URL.Query().Get("pageSize"))
		if err != nil || parsed < 1 || parsed > 100 {
			writeARDProblem(writer, request, catalog.E(catalog.CodeInvalidArgument, "pageSize must be an integer from 1 to 100"))
			return
		}
		pageSize = parsed
	}
	response, err := binding.Publication.List(request.Context(), binding.TenantID, publication.ListRequest{Filter: request.URL.Query().Get("filter"), OrderBy: request.URL.Query().Get("orderBy"), PageSize: pageSize, PageToken: request.URL.Query().Get("pageToken")})
	if err != nil {
		writeARDProblem(writer, request, err)
		return
	}
	if binding.Mode == DiscoveryAnonymous {
		writer.Header().Set("Cache-Control", "public, max-age=60, must-revalidate")
	} else {
		writer.Header().Set("Cache-Control", "private, max-age=60, must-revalidate")
		addVary(writer.Header(), "Authorization")
	}
	writer.Header().Set("Link", publicDiscoveryLinks())
	s.respondARDJSON(writer, request, "application/json", response)
}

// handlePublicArtifact requires active-snapshot artifact eligibility before returning retained bytes as an attachment.
func (s *Server) handlePublicArtifact(writer http.ResponseWriter, request *http.Request) {
	binding, ok := s.authorizeDiscovery(writer, request)
	if !ok {
		return
	}
	artifactID := request.PathValue("artifactId")
	if !binding.Publication.ArtifactAllowed(request.Context(), binding.TenantID, artifactID) {
		writeARDProblem(writer, request, catalog.E(catalog.CodeNotFound, "public artifact was not found"))
		return
	}
	artifact, content, err := s.catalog.GetPublicArtifact(request.Context(), binding.TenantID, artifactID)
	if err != nil {
		writeARDProblem(writer, request, err)
		return
	}
	etag := `"` + artifact.ID + `"`
	writer.Header().Set("ETag", etag)
	if binding.Mode == DiscoveryAnonymous {
		writer.Header().Set("Cache-Control", "public, max-age=60, must-revalidate")
	} else {
		writer.Header().Set("Cache-Control", "private, max-age=60, must-revalidate")
		addVary(writer.Header(), "Authorization")
	}
	if ifNoneMatchMatches(request.Header.Get("If-None-Match"), etag) {
		writer.WriteHeader(http.StatusNotModified)
		return
	}
	mediaType := adapter.DiscoveryMediaType(artifact.MediaType)
	if mediaType == "" {
		mediaType = "application/octet-stream"
	}
	writer.Header().Set("Content-Type", mediaType)
	writer.Header().Set("Content-Length", strconv.Itoa(len(content)))
	setContentDigest(writer, content)
	writer.Header().Set("Content-Disposition", `attachment; filename="`+artifact.ID+`"`)
	writer.WriteHeader(http.StatusOK)
	_, _ = writer.Write(content)
}

// setContentDigest writes the SHA-256 digest of the exact response bytes in Content-Digest syntax.
func setContentDigest(writer http.ResponseWriter, content []byte) {
	digest := sha256.Sum256(content)
	writer.Header().Set("Content-Digest", "sha-256=:"+base64.StdEncoding.EncodeToString(digest[:])+":")
}

// snapshotModifiedAt uses activation time for active snapshot caching and creation time when activation is absent.
func snapshotModifiedAt(snapshot publication.Snapshot) time.Time {
	if snapshot.ActivatedAt != nil {
		return *snapshot.ActivatedAt
	}
	return snapshot.CreatedAt
}

// publicNotModified gives If-None-Match precedence over second-precision modification-time checks.
func publicNotModified(request *http.Request, etag string, modifiedAt time.Time) bool {
	if candidates := request.Header.Get("If-None-Match"); candidates != "" {
		return ifNoneMatchMatches(candidates, etag)
	}
	if value := request.Header.Get("If-Modified-Since"); value != "" {
		if since, err := http.ParseTime(value); err == nil {
			return !modifiedAt.UTC().Truncate(time.Second).After(since.UTC())
		}
	}
	return false
}

// ifNoneMatchMatches accepts wildcard or weak/strong matching candidates for conditional discovery reads.
func ifNoneMatchMatches(candidates, etag string) bool {
	for _, candidate := range strings.Split(candidates, ",") {
		candidate = strings.TrimSpace(candidate)
		if candidate == "*" || strings.TrimPrefix(candidate, "W/") == etag {
			return true
		}
	}
	return false
}

// publicSnapshotError maps missing, unready and unreadable snapshots without disclosing internal causes.
func publicSnapshotError(err error, family string) error {
	code := catalog.ErrorCodeOf(err)
	if code == catalog.CodeNotFound {
		return catalog.E(catalog.CodeNotFound, "no active discovery "+family+" snapshot exists")
	}
	if code == catalog.CodePublicationNotReady {
		return catalog.E(catalog.CodePublicationNotReady, "the active discovery "+family+" snapshot is not ready")
	}
	return catalog.E(catalog.CodeDependencyUnavailable, "the discovery "+family+" snapshot could not be read")
}

// publicDiscoveryLinks returns the service's fixed well-known ARD and AI Catalog link relations.
func publicDiscoveryLinks() string {
	return `</.well-known/ard.json>; rel="ard"; type="application/json", </.well-known/ai-catalog.json>; rel="ai-catalog"; type="application/ai-catalog+json"`
}
