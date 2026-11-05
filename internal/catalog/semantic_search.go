// Optional retrieval coordinates inference outside persistence, classifies
// fallback and preserves a single ranking/profile across continuation pages.
package catalog

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"math"
	"slices"
	"strings"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
)

// SemanticSearchStore reads current profile-bound generations and full matches.
// Implementations own eligibility, filter, tenant and consistent-snapshot checks.
type SemanticSearchStore interface {
	Status(context.Context, string, string, int) (SemanticIndexStatus, error)
	Current(context.Context, string, string) (string, error)
	Active(context.Context, string, string, string, int) (SemanticGeneration, bool, error)
	Search(context.Context, SemanticSearchRequest) (SemanticSearchPage, error)
	Explore(context.Context, SemanticSearchRequest, []FacetRequest) (ExploreResponse, error)
	Similarities(context.Context, SemanticGeneration, []float32, []string) (map[string]float64, error)
}

// SemanticStatus discloses indexing counts only to the tenant administrator.
// Disabled and excluded tenants never inspect retained semantic state.
func (s *Service) SemanticStatus(ctx context.Context) (*SemanticIndexStatus, error) {
	ctx = identity.WithPermissionContext(ctx)
	actor, err := requirePermission(ctx, "operations.reindex", "")
	if err != nil {
		return nil, err
	}
	if s.semantic == nil || s.semantic.excludesTenant(actor.TenantID) {
		return nil, nil //nolint:nilnil // Disabled indexing has no administrative status or store access.
	}
	status, err := s.semantic.options.Store.Status(ctx, actor.TenantID, s.semantic.options.Profile, s.semantic.options.Dimensions)
	return &status, err
}

// SemanticSearchOptions contains deployment-owned settings, never query controls.
type SemanticSearchOptions struct {
	Observe         func(string, string)
	ProviderState   func() string
	Store           SemanticSearchStore
	Provider        SemanticProvider
	Profile         string
	RankingProfile  string
	Dimensions      int
	Cutoff          float64
	Timeout         time.Duration
	ExcludedTenants []string
}

// SemanticProviderState exposes a fixed last-observed process state, never a probe.
func (s *Service) SemanticProviderState(ctx context.Context) string {
	if enabled, _ := s.SemanticConfiguration(ctx); !enabled || s.semantic.options.ProviderState == nil {
		return "unknown"
	}
	switch state := s.semantic.options.ProviderState(); state {
	case "healthy", "degraded":
		return state
	default:
		return "unknown"
	}
}

// SemanticSearch is immutable after construction and shares only bounded,
// concurrency-safe dependencies between request handlers.
type SemanticSearch struct{ options SemanticSearchOptions }

// UUID spelling must not change the tenant's disclosure policy. PostgreSQL
// treats uppercase and lowercase UUIDs as the same identity.
func (s *SemanticSearch) excludesTenant(tenant string) bool {
	return slices.ContainsFunc(s.options.ExcludedTenants, func(excluded string) bool { return strings.EqualFold(excluded, tenant) })
}

// SemanticConfiguration reports tenant-permitted configuration without querying
// the provider or exposing its endpoint, credentials or model identity.
func (s *Service) SemanticConfiguration(ctx context.Context) (bool, string) {
	actor, ok := identity.FromContext(ctx)
	if !ok || s.semantic == nil || s.semantic.excludesTenant(actor.TenantID) {
		return false, CoreSearchProfile
	}
	return true, s.semantic.options.RankingProfile
}

// SemanticReindexTarget authorizes tenant administration before inspecting the
// current control population or an active tenant-local publication snapshot.
func (s *Service) SemanticReindexTarget(ctx context.Context, snapshotID string) (SemanticReindexInput, error) {
	ctx = identity.WithPermissionContext(ctx)
	actor, err := requirePermission(ctx, "operations.reindex", "")
	if err != nil {
		return SemanticReindexInput{}, err
	}
	if s.semantic == nil || s.semantic.excludesTenant(actor.TenantID) {
		return SemanticReindexInput{}, E(CodeDependencyUnavailable, "semantic indexing is disabled for this tenant")
	}
	scope := "control"
	if snapshotID != "" {
		if !identity.ValidTenantID(snapshotID) {
			return SemanticReindexInput{}, E(CodeInvalidArgument, "snapshotId must be a UUID")
		}
		scope = "public:" + strings.ToLower(snapshotID)
	}
	generation, err := s.semantic.options.Store.Current(ctx, actor.TenantID, scope)
	return SemanticReindexInput{Profile: s.semantic.options.Profile, Scope: scope, SourceGeneration: generation}, err
}

// NewSemanticSearch validates the optional graph without contacting inference.
func NewSemanticSearch(options SemanticSearchOptions) (*SemanticSearch, error) {
	validDigest := func(value string) bool {
		raw, err := hex.DecodeString(strings.TrimPrefix(value, "sha256:"))
		return err == nil && len(raw) == 32 && value == "sha256:"+hex.EncodeToString(raw)
	}
	if options.Store == nil || options.Provider == nil || !validDigest(options.Profile) || !validDigest(options.RankingProfile) || options.Dimensions < 1 || options.Dimensions > 4096 || math.IsNaN(options.Cutoff) || math.IsInf(options.Cutoff, 0) || options.Cutoff < -1 || options.Cutoff > 1 || options.Timeout < 100*time.Millisecond || options.Timeout > 10*time.Second || len(options.ExcludedTenants) > 256 {
		return nil, E(CodeInvalidArgument, "invalid semantic search dependencies or bounds")
	}
	for _, tenant := range options.ExcludedTenants {
		if !identity.ValidTenantID(tenant) {
			return nil, E(CodeInvalidArgument, "invalid semantic tenant exclusion")
		}
	}
	options.ExcludedTenants = append([]string(nil), options.ExcludedTenants...)
	return &SemanticSearch{options}, nil
}

type semanticPageState struct {
	cursor     semanticCursor
	generation SemanticGeneration
	vector     []float32
}

// semanticPage returns either a hybrid response or a classified fresh-page
// fallback. Existing lexical cursors never switch to hybrid after recovery.
func (s *Service) semanticPage(ctx context.Context, actor identity.Actor, query SearchQuery, localOnly bool) (CatalogSearchResponse, bool, string, error) {
	return s.semanticPageWithLive(ctx, actor, query, localOnly, nil)
}

func (s *Service) semanticPageWithLive(ctx context.Context, actor identity.Actor, query SearchQuery, localOnly bool, live *LiveCursor) (response CatalogSearchResponse, used bool, fallback string, resultErr error) {
	defer func() { s.semantic.observe("search", used, fallback, resultErr) }()
	continuation := live != nil || strings.HasPrefix(query.Cursor, "h1.")
	if query.Cursor != "" && !continuation {
		return CatalogSearchResponse{}, false, "", nil
	}
	_, exactIdentifierError := ParseAIRIdentifier(query.Text)
	if query.Order != "" || s.semantic == nil || s.semantic.excludesTenant(actor.TenantID) || query.Text == "" || exactIdentifierError == nil {
		if continuation {
			return CatalogSearchResponse{}, true, "", E(CodeInvalidArgument, "hybrid search configuration changed; restart the search")
		}
		return CatalogSearchResponse{}, false, "", nil
	}
	options := s.semantic.options
	purpose := "hybrid-unified:" + options.RankingProfile
	if localOnly {
		purpose = "hybrid-local:" + options.RankingProfile
	}
	binding, err := s.searchCursorBindingFor(ctx, purpose, actor, query)
	if err != nil {
		return CatalogSearchResponse{}, true, "", err
	}
	var cursor semanticCursor
	if live == nil {
		cursor, err = s.decodeSemanticCursor(query.Cursor, binding)
		if err != nil {
			return CatalogSearchResponse{}, true, "", err
		}
	} else {
		if live.Ranking != options.RankingProfile {
			return CatalogSearchResponse{}, true, "", E(CodeInvalidArgument, "hybrid search configuration changed; restart the search")
		}
		cursor = semanticCursor{Generation: live.Generation, SourceGeneration: live.IndexedGeneration, VectorFingerprint: live.VectorFingerprint, ExpiresAt: live.ExpiresAt}
		if live.HybridPosition != nil {
			cursor.Position = *live.HybridPosition
		}
	}
	g, ready, err := options.Store.Active(ctx, actor.TenantID, options.Profile, "control", options.Dimensions)
	if err != nil {
		return CatalogSearchResponse{}, true, "", err
	}
	if !ready {
		if continuation {
			return CatalogSearchResponse{}, true, "", E(CodeInvalidArgument, "semantic index changed; restart the search")
		}
		return CatalogSearchResponse{}, false, "indexing", nil
	}
	if continuation && (cursor.Generation != g.ID || cursor.SourceGeneration != g.SourceGeneration) {
		return CatalogSearchResponse{}, true, "", E(CodeInvalidArgument, "semantic index changed; restart the search")
	}
	timeout := options.Timeout
	if deadline, ok := ctx.Deadline(); ok {
		timeout = min(timeout, time.Until(deadline)/2)
	}
	queryContext, cancel := context.WithTimeout(ctx, timeout)
	vector, err := options.Provider.Query(queryContext, query.Text)
	cancel()
	if err != nil {
		if ctx.Err() != nil {
			return CatalogSearchResponse{}, true, "", ctx.Err()
		}
		if !errors.Is(err, ErrEmbeddingUnavailable) && !errors.Is(err, ErrEmbeddingAuthentication) && !errors.Is(err, ErrEmbeddingRejected) && !errors.Is(err, ErrEmbeddingInvalidResponse) && !errors.Is(err, context.DeadlineExceeded) {
			return CatalogSearchResponse{}, true, "", err
		}
		if continuation {
			return CatalogSearchResponse{}, true, "", E(CodeDependencyUnavailable, "hybrid continuation is unavailable; retry or restart the search")
		}
		return CatalogSearchResponse{}, false, "provider_unavailable", nil
	}
	fingerprint := semanticVectorFingerprint(vector)
	if continuation && cursor.VectorFingerprint != fingerprint {
		return CatalogSearchResponse{}, true, "", E(CodeInvalidArgument, "query embedding changed; restart the search")
	}
	request := SemanticSearchRequest{Generation: g, Query: query, Vector: vector, Cutoff: options.Cutoff, LocalOnly: localOnly}
	if continuation && cursor.Position.ID != "" {
		request.After = &cursor.Position
	}
	page, err := options.Store.Search(ctx, request)
	if err != nil {
		if !continuation && errors.Is(err, ErrSearchGenerationChanged) {
			return CatalogSearchResponse{}, false, "index_changed", nil
		}
		if !continuation && ErrorCodeOf(err) == CodeWorkLimitExceeded {
			return CatalogSearchResponse{}, false, "work_limit", nil
		}
		return CatalogSearchResponse{}, true, "", err
	}
	response = CatalogSearchResponse{Items: []CatalogSearchResult{}, DurableTotal: page.Total, Profile: options.RankingProfile, Generation: combinedSearchGeneration(g.ID, g.SourceGeneration), Tier: "hybrid", PartialIndex: page.PartialIndex}
	for _, item := range page.Items {
		if item.Local != nil {
			response.Items = append(response.Items, localCatalogSearchResult(*item.Local))
		} else {
			response.Items = append(response.Items, item.Result)
		}
	}
	expiresAt := cursor.ExpiresAt
	if expiresAt == 0 {
		expiresAt = s.clock.Now().Add(catalogSearchCursorTTL).Unix()
	}
	position := cursor.Position
	if len(page.Items) > 0 {
		position = page.Items[len(page.Items)-1].Position
	}
	response.semantic = &semanticPageState{cursor: semanticCursor{Generation: g.ID, SourceGeneration: g.SourceGeneration, VectorFingerprint: fingerprint, Position: position, ExpiresAt: expiresAt}, generation: g, vector: vector}
	if page.HasMore && len(page.Items) > 0 {
		response.NextCursor, err = s.encodeSemanticCursor(response.semantic.cursor, binding)
	}
	return response, true, "", err
}

// semanticVectorFingerprint detects replica/model drift without storing vectors
// or input text in a continuation token.
func semanticVectorFingerprint(vector []float32) string {
	h := sha256.New()
	var word [4]byte
	for _, value := range vector {
		binary.BigEndian.PutUint32(word[:], math.Float32bits(value))
		_, _ = h.Write(word[:])
	}
	return hex.EncodeToString(h.Sum(nil))
}
