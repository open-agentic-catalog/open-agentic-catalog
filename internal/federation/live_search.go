package federation

import (
	"context"
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"net/http"
	"sort"
	"strconv"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/fetcher"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/standards"
)

// liveSearchPeer retains only one request's bounded page progress and evidence.
// Candidates retain their owner until admission; an invalid source cannot erase
// another peer's copy of the same identity.
type liveSearchPeer struct {
	replayFetches                    []liveReplayFetch
	source                           Source
	depth, hopLimit, pages           int
	bytes                            int64
	mode, token, configurationDigest string
	usedTokens                       map[string]struct{}
	accepted                         map[string]struct{}
	access                           sourceAccess
	response                         LiveSearchResponse
	outcome                          string
	succeededIndex                   int
	validPage, invalid, stopped      bool
}

type liveSearchCandidate struct {
	peer    *liveSearchPeer
	live    LiveResult
	catalog catalog.CatalogSearchResult
}

func newLiveSearchPeer(source Source, depth, hopLimit int) *liveSearchPeer {
	return &liveSearchPeer{source: source, depth: depth, hopLimit: hopLimit,
		access: accessForSource(source, true), usedTokens: make(map[string]struct{}),
		accepted: make(map[string]struct{}), succeededIndex: -1}
}

func (p *liveSearchPeer) fail(status, message string) {
	p.response.Partial = true
	p.stopped = true
	appendLiveDiagnostic(&p.response, LiveDiagnostic{SourceID: p.source.ID, Status: status, Message: message})
}

// traverseLivePeers prioritizes new approved peers before continuation calls,
// then grants each eligible peer one page per round. Fan-out counts peers,
// whereas the derived work ceiling counts Search fetches, including failures.
func (m *Manager) traverseLivePeers(ctx context.Context, tenantID string, request LiveSearchRequest, selected []Source, response *LiveSearchResponse) ([]Source, []string, error) {
	first := make([]*liveSearchPeer, 0, m.maxFanout)
	continuations := make([]*liveSearchPeer, 0, m.maxFanout)
	peers := make([]*liveSearchPeer, 0, m.maxFanout)
	visitedSources := make(map[string]struct{}, m.maxFanout)
	visitedEndpoints := make(map[string]struct{}, m.maxFanout)
	visitedRegistryIDs := make(map[string]struct{}, m.maxFanout)
	for _, source := range selected {
		endpoint, valid := canonicalEndpoint(source.RegistrySearchURL)
		if !valid {
			response.Partial = true
			appendLiveDiagnostic(response, LiveDiagnostic{SourceID: source.ID, Status: "failed", Message: "Configured Registry endpoint is invalid."})
			continue
		}
		if _, duplicate := visitedEndpoints[endpoint]; duplicate {
			response.Partial = true
			appendLiveDiagnostic(response, LiveDiagnostic{SourceID: source.ID, Status: "fanout_limited", Message: "A duplicate configured Registry endpoint was skipped."})
			continue
		}
		hopLimit := 0
		if source.Referrals.Enabled && !request.DirectOnly {
			hopLimit = min(source.Referrals.MaxHops, m.maxHops)
		}
		first = append(first, newLiveSearchPeer(source, 0, hopLimit))
		visitedSources[source.ID] = struct{}{}
		visitedEndpoints[endpoint] = struct{}{}
	}
	candidates := make([]liveSearchCandidate, 0)
	sources := make([]Source, 0, m.maxFanout)
	fetches := 0
	for (len(first) > 0 || len(continuations) > 0) && fetches < m.maxFanout*m.maxLivePagesPerSource {
		var peer *liveSearchPeer
		if len(first) > 0 {
			peer, first = first[0], first[1:]
		} else {
			peer, continuations = continuations[0], continuations[1:]
		}
		if ctx.Err() != nil {
			response.Partial = true
			// A failed fetch or cancelled result scan already reports the stop.
			if len(peers) == 0 || !peers[len(peers)-1].stopped {
				appendLiveDiagnostic(response, LiveDiagnostic{Status: "failed", Message: "The live query ended before all eligible peer requests completed."})
			}
			break
		}
		if peer.pages == 0 {
			var err error
			peer.configurationDigest, err = sourceConfigurationDigest(peer.source)
			if err != nil {
				return sources, nil, err
			}
			peer.mode = "none"
			if peer.source.Referrals.Enabled && peer.depth < peer.hopLimit {
				peer.mode = "referrals"
			}
			peers = append(peers, peer)
		} else if !m.livePeerStillApproved(ctx, request, peer) {
			continue
		}
		remaining := int64(MaxLiveResponseBytes) - peer.bytes
		if remaining < 1024 {
			peer.stopped = true
			continue
		}
		body, err := livePageRequest(request, peer)
		if err != nil {
			return sources, nil, catalog.Wrap(catalog.CodeInternal, "encode bounded live federation request", err)
		}
		if peer.pages == 0 {
			sources = append(sources, peer.source)
		}
		if peer.token != "" {
			peer.usedTokens[peer.token] = struct{}{}
		}
		fetched, fetchErr := m.fetchWithAccess(ctx, peer.access, peer.source.RegistrySearchURL, fetcher.Request{
			Method: http.MethodPost, Body: body, ContentType: "application/json", Accept: []string{"application/json"},
			MaxResponseBytes: remaining, RejectRedirects: peer.pages > 0})
		peer.pages++
		fetches++
		if fetchErr != nil {
			peer.outcome = classifyOutboundOutcome(fetchErr)
			peer.fail("failed", "Upstream Registry query failed within the bounded request.")
			if peer.access.operationError() != nil {
				peer.invalid = true
			}
			continue
		}
		peer.bytes += int64(len(fetched.Body))
		if peer.bytes > MaxLiveResponseBytes {
			peer.fail("failed", "Upstream Registry response exceeded the live byte allowance.")
			peer.outcome = outboundOutcomeTransportError
			continue
		}
		if peer.pages > 1 && !sameEndpoint(fetched.FinalURL, peer.source.RegistrySearchURL) {
			peer.invalid = true
			peer.fail("failed", "The Registry continuation did not use the approved endpoint.")
			continue
		}
		if fetched.Status < 200 || fetched.Status >= 300 {
			peer.outcome = outboundOutcomeHTTPError
			peer.fail("failed", "Upstream Registry query returned an unsuccessful HTTP status.")
			continue
		}
		wire, decodeErr := decodeLiveResponse(fetched.Body)
		if decodeErr != nil {
			peer.outcome = outboundOutcomeParseError
			peer.fail("invalid_response", "Upstream Registry response was not valid unambiguous ARD Search JSON.")
			continue
		}
		peer.validPage = true
		peer.token = wire.PageToken
		peer.replayFetches = append(peer.replayFetches, liveReplayFetch{fetched.PolicyDigest, fetched.CredentialRevision, fetched.FinalURL, wire.PageToken != ""})
		candidates = append(candidates, m.livePageCandidates(ctx, request, peer, wire.Results)...)
		diagnostic := LiveDiagnostic{SourceID: peer.source.ID, Status: "succeeded", PolicyDigest: fetched.PolicyDigest, CredentialRevision: fetched.CredentialRevision}
		if peer.succeededIndex < 0 {
			peer.succeededIndex = len(peer.response.Diagnostics)
			appendLiveDiagnostic(&peer.response, diagnostic)
		} else if peer.succeededIndex < len(peer.response.Diagnostics) && peer.response.Diagnostics[peer.succeededIndex].Status == "succeeded" {
			peer.response.Diagnostics[peer.succeededIndex] = diagnostic
		}
		if peer.mode == "referrals" && ctx.Err() == nil {
			for _, approvedID := range peer.source.Referrals.ApprovedSourceIDs {
				if ctx.Err() != nil {
					break
				}
				if len(visitedSources) >= m.maxFanout {
					break
				}
				if _, visited := visitedSources[approvedID]; visited {
					continue
				}
				source, sourceErr := m.store.GetSource(ctx, tenantID, approvedID)
				if sourceErr != nil || source.Status != SourceActive || !source.Live.Enabled || source.RegistrySearchURL == "" || !m.sourceUsesCurrentPolicy(source) {
					continue
				}
				if !request.Administrative && !request.CatalogTenant && !sourceEligibleForDiscovery(source, request.DiscoveryHost, request.Audience) {
					continue
				}
				registryID, observed := observedReferralIdentifier(wire.Referrals, source.RegistrySearchURL)
				if !observed {
					continue
				}
				if _, duplicate := visitedRegistryIDs[registryID]; duplicate {
					peer.response.Partial = true
					appendLiveDiagnostic(&peer.response, LiveDiagnostic{SourceID: source.ID, Status: "fanout_limited", Message: "A duplicate advertised Registry identifier was skipped."})
					continue
				}
				endpoint, valid := canonicalEndpoint(source.RegistrySearchURL)
				if !valid {
					peer.response.Partial = true
					appendLiveDiagnostic(&peer.response, LiveDiagnostic{SourceID: source.ID, Status: "failed", Message: "Configured Registry endpoint is invalid."})
					continue
				}
				if _, duplicate := visitedEndpoints[endpoint]; duplicate {
					peer.response.Partial = true
					appendLiveDiagnostic(&peer.response, LiveDiagnostic{SourceID: source.ID, Status: "fanout_limited", Message: "A duplicate configured Registry endpoint was skipped."})
					continue
				}
				first = append(first, newLiveSearchPeer(source, peer.depth+1, peer.hopLimit))
				visitedSources[source.ID] = struct{}{}
				visitedEndpoints[endpoint] = struct{}{}
				visitedRegistryIDs[registryID] = struct{}{}
			}
		}
		if peer.token != "" && !peer.stopped {
			if _, repeated := peer.usedTokens[peer.token]; repeated {
				peer.fail("invalid_response", "The upstream Registry repeated a continuation token.")
			} else if peer.pages < m.maxLivePagesPerSource && len(peer.accepted) < request.PageSize && sameEndpoint(fetched.FinalURL, peer.source.RegistrySearchURL) {
				continuations = append(continuations, peer)
			}
		}
	}
	outcomes := make([]string, 0, len(peers))
	for _, peer := range peers {
		// Default one-page traversal keeps its original admission and read cost.
		// Multi-page evidence needs a final same-deadline authority check.
		if (peer.pages > 1 || request.Replay) && !peer.invalid {
			m.livePeerStillApproved(ctx, request, peer)
		}
		if peer.token != "" {
			response.Partial = true
			appendLiveDiagnostic(response, LiveDiagnostic{SourceID: peer.source.ID, Status: "continuation_unavailable", Message: "The upstream Registry has more results; this bounded live query did not continue them."})
		}
		response.Partial = response.Partial || peer.response.Partial
		for _, diagnostic := range peer.response.Diagnostics {
			if !peer.invalid || diagnostic.Status != "succeeded" {
				appendLiveDiagnostic(response, diagnostic)
			}
		}
		outcome := peer.outcome
		switch {
		case peer.validPage && (peer.response.Partial || peer.token != ""):
			outcome = outboundOutcomePartial
		case outcome != "":
		case len(peer.accepted) == 0:
			outcome = outboundOutcomeNegative
		default:
			outcome = outboundOutcomeSucceeded
		}
		outcomes = append(outcomes, outcome)
	}
	mergeLiveCandidates(request, candidates, response)
	if request.Replay {
		response.ReplayFingerprint = m.replayFingerprint(ctx, selected, peers, *response)
	}
	return sources, outcomes, nil
}

func livePageRequest(request LiveSearchRequest, peer *liveSearchPeer) ([]byte, error) {
	query := map[string]any{"text": request.Text}
	if request.Context != nil {
		query["@context"] = request.Context
	}
	if len(request.Filter) > 0 {
		query["filter"] = request.Filter
	}
	wire := map[string]any{"query": query, "federation": peer.mode, "pageSize": request.PageSize}
	if peer.token != "" {
		wire["pageToken"] = peer.token
	}
	return json.Marshal(wire)
}

// livePeerStillApproved uses the internal tenant-scoped reader, not an
// administrator-only API. No transaction is held while the peer is contacted.
func (m *Manager) livePeerStillApproved(ctx context.Context, request LiveSearchRequest, peer *liveSearchPeer) bool {
	if ctx.Err() != nil || peer.access.operationError() != nil {
		peer.invalid = true
		peer.fail("failed", "Source access could not be verified within the live query.")
		return false
	}
	current, err := m.store.GetSource(ctx, peer.source.TenantID, peer.source.ID)
	if err != nil || ctx.Err() != nil {
		peer.invalid = true
		peer.fail("failed", "Source access could not be verified within the live query.")
		return false
	}
	if !m.sourceUsesCurrentPolicy(current) {
		peer.invalid = true
		peer.fail("policy_stale", "Source policy evidence is not current.")
		return false
	}
	digest, digestErr := sourceConfigurationDigest(current)
	if digestErr != nil || current.ID != peer.source.ID || current.TenantID != peer.source.TenantID || current.Revision != peer.source.Revision || current.Endpoint != peer.source.Endpoint || current.ProfileID != peer.source.ProfileID || current.PolicyID != peer.source.PolicyID || current.PolicyRevision != peer.source.PolicyRevision || current.PolicyDigest != peer.source.PolicyDigest || digest != peer.configurationDigest || current.Status != SourceActive || !current.Live.Enabled || (!request.Administrative && !request.CatalogTenant && !sourceEligibleForDiscovery(current, request.DiscoveryHost, request.Audience)) {
		peer.invalid = true
		peer.fail("failed", "Source access changed during the live query.")
		return false
	}
	return true
}

func (m *Manager) livePageCandidates(ctx context.Context, request LiveSearchRequest, peer *liveSearchPeer, results []json.RawMessage) []liveSearchCandidate {
	source := peer.source
	candidates := make([]liveSearchCandidate, 0, len(results))
	for _, raw := range results {
		if ctx.Err() != nil {
			peer.fail("failed", "The live query ended before all page results could be checked.")
			break
		}
		if err := standards.ValidateARDSearchEntry(raw); err != nil {
			peer.response.Partial = true
			appendLiveDiagnostic(&peer.response, LiveDiagnostic{SourceID: source.ID, Status: "invalid_response", Message: "An upstream result was omitted because its present fields did not match the ARD Search contract."})
			continue
		}
		var projection struct {
			Identifier string          `json:"identifier"`
			Version    string          `json:"version"`
			Type       string          `json:"type"`
			Tags       []string        `json:"tags"`
			Score      json.RawMessage `json:"score"`
			Source     string          `json:"source"`
			URL        string          `json:"url"`
			Data       json.RawMessage `json:"data"`
		}
		// ARD extensions have exact names and must not override validated
		// canonical fields through case-insensitive struct matching.
		if err := jsonv2.Unmarshal(raw, &projection, json.DefaultOptionsV1(), jsonv2.MatchCaseInsensitiveNames(false)); err != nil {
			peer.response.Partial = true
			continue
		}
		var rawFields map[string]json.RawMessage
		if err := json.Unmarshal(raw, &rawFields); err != nil {
			peer.response.Partial = true
			continue
		}
		score, validScore := parseLiveScore(projection.Score)
		_, versionPresent := rawFields["version"]
		entry := sourceEntry{Identifier: projection.Identifier, Version: projection.Version, VersionPresent: versionPresent, Type: projection.Type, Tags: projection.Tags}
		if _, identifierErr := catalog.ParseAIRIdentifier(projection.Identifier); identifierErr != nil || versionPresent && (projection.Version == "" || !validDiscoveryVersion(projection.Version)) || !validScore || !catalog.ValidExternalHTTPSURL(projection.Source) || projection.URL != "" && !catalog.ValidExternalHTTPSURL(projection.URL) || !validARDResultData(projection.Data) {
			peer.response.Partial = true
			appendLiveDiagnostic(&peer.response, LiveDiagnostic{SourceID: source.ID, Status: "invalid_response", Message: "An upstream result was omitted because its identity, score, source, or delivery was invalid."})
			continue
		}
		// A valid result outside local policy is an expected omission, not a peer failure.
		if !scopeMatches(source.Scope, entry) {
			continue
		}
		candidate := LiveResult{SourceRevision: source.Revision, PresentationGeneration: EffectivePresentationGeneration(source), PolicyDigest: source.PolicyDigest, CredentialRevision: source.CredentialRevision, Identifier: projection.Identifier, Version: projection.Version, VersionPresent: versionPresent, Score: score, SourceID: source.ID, Source: projection.Source, RespondingURL: source.RegistrySearchURL, Entry: raw}

		item := liveSearchCandidate{peer: peer, live: candidate}
		if request.CatalogTenant {
			if request.catalogQuery.License != "" || len(request.catalogQuery.Categories) > 0 {
				declarations := projectLiveControlFilters(ctx, raw, len(request.catalogQuery.Categories) > 0)
				if declarations.insufficient(request.catalogQuery) {
					peer.response.Partial = true
					appendLiveDiagnostic(&peer.response, LiveDiagnostic{SourceID: source.ID, Status: "insufficient_evidence", Message: "An upstream result lacked bounded literal declarations required by the Catalog filters."})
					continue
				}
				if !declarations.matches(request.catalogQuery) {
					continue
				}
			}
			result, valid := catalogLiveResult(candidate, request.catalogQuery)
			if !valid {
				peer.response.Partial = true
				appendLiveDiagnostic(&peer.response, LiveDiagnostic{SourceID: source.ID, Status: "invalid_response", Message: "An upstream result was omitted because it did not satisfy the Catalog search contract."})
				continue
			}
			item.catalog = result
		}
		peer.accepted[liveCandidateIdentity(request, candidate)] = struct{}{}
		candidates = append(candidates, item)
	}
	return candidates
}

func liveResultIdentity(result LiveResult) string {
	return result.Identifier + "\x00" + strconv.FormatBool(result.VersionPresent) + "\x00" + result.Version
}

// Private administrator results preserve native media and delivery variants.
// Public search continues to merge logical resource identities.
func liveCandidateIdentity(request LiveSearchRequest, result LiveResult) string {
	key := liveResultIdentity(result)
	if request.Administrative {
		var native struct {
			Type string          `json:"type"`
			Data json.RawMessage `json:"data"`
		}
		_ = jsonv2.Unmarshal(result.Entry, &native, json.DefaultOptionsV1(), jsonv2.MatchCaseInsensitiveNames(false)) // Entry already passed strict ARD validation.
		key += "\x00" + native.Type + "\x00" + strconv.FormatBool(len(native.Data) > 0)
	}
	return key
}

// Merge only admitted candidates, in actual page visitation order. Keeping
// backups until this point preserves existing equal-score ties and provenance.
func mergeLiveCandidates(request LiveSearchRequest, candidates []liveSearchCandidate, response *LiveSearchResponse) {
	seen := make(map[string]int)
	catalogCandidates := make(map[string]liveSearchCandidate)
	for _, candidate := range candidates {
		if candidate.peer.invalid {
			continue
		}
		key := liveCandidateIdentity(request, candidate.live)
		if request.CatalogTenant {
			current, exists := catalogCandidates[key]
			if !exists || catalogSearchResultBefore(candidate.catalog, current.catalog) {
				catalogCandidates[key] = candidate
			}
		} else if index, exists := seen[key]; exists {
			if candidate.live.Score > response.Results[index].Score {
				response.Results[index] = candidate.live
			}
		} else {
			seen[key] = len(response.Results)
			response.Results = append(response.Results, candidate.live)
		}
	}
	if request.CatalogTenant {
		ranked := make([]liveSearchCandidate, 0, len(catalogCandidates))
		for _, candidate := range catalogCandidates {
			ranked = append(ranked, candidate)
		}
		sort.Slice(ranked, func(i, j int) bool { return catalogSearchResultBefore(ranked[i].catalog, ranked[j].catalog) })
		for _, candidate := range ranked {
			response.Results = append(response.Results, candidate.live)
		}
	} else {
		sort.Slice(response.Results, func(i, j int) bool {
			if response.Results[i].Score != response.Results[j].Score {
				return response.Results[i].Score > response.Results[j].Score
			}
			if response.Results[i].Identifier != response.Results[j].Identifier {
				return response.Results[i].Identifier < response.Results[j].Identifier
			}
			return response.Results[i].Version < response.Results[j].Version
		})
	}
	if len(response.Results) > request.PageSize {
		response.Results = response.Results[:request.PageSize]
	}
}
