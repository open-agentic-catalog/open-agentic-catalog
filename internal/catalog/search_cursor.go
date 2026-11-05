// Search continuation tokens bind normalized queries, effective authorization,
// generation state and stable result positions to a signed expiring envelope.

package catalog

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
)

const (
	catalogSearchCursorTTL = 15 * time.Minute
	MaxCatalogCursorBytes  = 4096
	CoreSearchProfile      = "core-lexical-v1"
)

type catalogSearchCursor struct {
	SortValue         string `json:"o,omitempty"`
	Version           int    `json:"v"`
	Generation        string `json:"g"`
	IndexedGeneration string `json:"x,omitempty"`
	Score             int    `json:"s"`
	DisplayName       string `json:"n"`
	ResultID          string `json:"i"`
	ExpiresAt         int64  `json:"e"`
}

// searchCursorBinding hashes the normalized local query with the actor's
// effective authorization.
func (s *Service) searchCursorBinding(ctx context.Context, actor identity.Actor, query SearchQuery) (string, error) {
	return s.searchCursorBindingFor(ctx, "local", actor, query)
}

// unifiedSearchCursorBinding binds unified discovery mode and normalized query
// inputs to current authorization.
func (s *Service) unifiedSearchCursorBinding(ctx context.Context, actor identity.Actor, query SearchQuery) (string, error) {
	return s.searchCursorBindingFor(ctx, "unified", actor, query)
}

// searchCursorBindingFor prevents replay across tenants, actors, changed grants,
// search purposes, policies, profiles, or normalized query parameters.
func (s *Service) searchCursorBindingFor(ctx context.Context, purpose string, actor identity.Actor, query SearchQuery) (string, error) {
	ctx = identity.WithActor(ctx, actor)
	query = normalizeCatalogSearchQuery(query)
	query.Cursor = ""

	raw, err := json.Marshal(struct {
		Purpose  string      `json:"purpose"`
		Decision string      `json:"decision"`
		Policy   string      `json:"policy"`
		Profile  string      `json:"profile"`
		Query    SearchQuery `json:"query"`
	}{purpose, identity.DecisionDigest(ctx), s.policy, CoreSearchProfile, query})
	if err != nil {
		return "", Wrap(CodeInternal, "bind catalog cursor", err)
	}
	mac := hmac.New(sha256.New, s.cursorKey)
	_, _ = mac.Write([]byte("catalog-search-binding-v3\x00"))
	_, _ = mac.Write(raw)
	return hex.EncodeToString(mac.Sum(nil)), nil
}

// encodeSearchCursor signs a local-generation continuation without an
// indexed-generation component.
func (s *Service) encodeSearchCursor(generation string, position CatalogSearchPosition, binding string) (string, error) {
	return s.encodeSearchCursorGenerations(generation, "", position, binding)
}

// encodeSearchCursorGenerations signs the local/indexed generations, stable
// position and expiry under the request binding.
func (s *Service) encodeSearchCursorGenerations(generation, indexedGeneration string, position CatalogSearchPosition, binding string) (string, error) {
	payload, err := json.Marshal(catalogSearchCursor{Version: 2, Generation: generation, IndexedGeneration: indexedGeneration, Score: position.Score, DisplayName: position.DisplayName, ResultID: position.ResultID, SortValue: position.SortValue, ExpiresAt: s.clock.Now().Add(catalogSearchCursorTTL).Unix()})
	if err != nil {
		return "", Wrap(CodeInternal, "encode catalog cursor", err)
	}
	mac := hmac.New(sha256.New, s.cursorKey)
	_, _ = mac.Write([]byte("catalog-search-cursor-v2\x00"))
	_, _ = mac.Write(payload)
	_, _ = mac.Write([]byte("\x00" + binding))
	token := base64.RawURLEncoding.EncodeToString(payload) + "." + hex.EncodeToString(mac.Sum(nil))
	if len(token) > MaxCatalogCursorBytes {
		return "", E(CodeInternal, "catalog cursor exceeds its safe bound")
	}
	return token, nil
}

// decodeSearchCursor verifies the bounded signature, closed envelope and
// expiry; an empty token selects the first page.
func (s *Service) decodeSearchCursor(token, binding string) (catalogSearchCursor, bool, error) {
	if token == "" {
		return catalogSearchCursor{}, false, nil
	}
	if len(token) > MaxCatalogCursorBytes {
		return catalogSearchCursor{}, false, E(CodeInvalidArgument, "catalog cursor is invalid")
	}
	parts := strings.Split(token, ".")
	if len(parts) != 2 {
		return catalogSearchCursor{}, false, E(CodeInvalidArgument, "catalog cursor is invalid")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil || base64.RawURLEncoding.EncodeToString(payload) != parts[0] {
		return catalogSearchCursor{}, false, E(CodeInvalidArgument, "catalog cursor is invalid")
	}
	provided, err := hex.DecodeString(parts[1])
	if err != nil || hex.EncodeToString(provided) != parts[1] {
		return catalogSearchCursor{}, false, E(CodeInvalidArgument, "catalog cursor is invalid")
	}
	mac := hmac.New(sha256.New, s.cursorKey)
	_, _ = mac.Write([]byte("catalog-search-cursor-v2\x00"))
	_, _ = mac.Write(payload)
	_, _ = mac.Write([]byte("\x00" + binding))
	if !hmac.Equal(provided, mac.Sum(nil)) {
		return catalogSearchCursor{}, false, E(CodeInvalidArgument, "catalog cursor is invalid for this request")
	}
	var cursor catalogSearchCursor
	decoder := json.NewDecoder(strings.NewReader(string(payload)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&cursor); err != nil || cursor.Version != 2 || cursor.Generation == "" || len(cursor.Generation) > 128 || len(cursor.IndexedGeneration) > 128 || len(cursor.SortValue) > 2048 || cursor.Score < 0 || cursor.Score > 100 || cursor.DisplayName == "" || len(cursor.DisplayName) > 640 || cursor.ResultID == "" || len(cursor.ResultID) > 256 {
		return catalogSearchCursor{}, false, E(CodeInvalidArgument, "catalog cursor is invalid")
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return catalogSearchCursor{}, false, E(CodeInvalidArgument, "catalog cursor is invalid")
	}
	if !time.Unix(cursor.ExpiresAt, 0).After(s.clock.Now()) {
		return catalogSearchCursor{}, false, E(CodeInvalidArgument, "catalog cursor has expired")
	}
	return cursor, true, nil
}

// catalogSearchGeneration fingerprints the current projection so continuations
// cannot silently cross changed Catalog state.
func catalogSearchGeneration(records []CatalogSummary) string {
	ordered := append([]CatalogSummary(nil), records...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].Release.ID < ordered[j].Release.ID })
	hash := sha256.New()
	_, _ = hash.Write([]byte("catalog-search-generation-v2\x00"))
	for _, record := range ordered {
		values := []string{
			record.Resource.ID,
			record.VersionRevision.ID,
			record.VersionRevision.EvidenceSetID,
			record.ListingRevision.ID,
			record.Release.ID,
			strconv.FormatInt(record.Release.Revision, 10),
			string(record.Release.State),
			catalogRecordPosition(record, SearchOrderPublisherAsc).SortValue,
			record.Release.UpdatedAt.UTC().Format(time.RFC3339Nano),
		}
		_, _ = hash.Write([]byte(strings.Join(values, "\x00") + "\x00"))
	}
	return hex.EncodeToString(hash.Sum(nil))
}

// catalogRecordPosition extracts the shared score/name/ID position from a local
// release-backed result.
func catalogRecordPosition(record CatalogSummary, order ...SearchOrder) CatalogSearchPosition {
	return CatalogSearchResultPosition(localCatalogSearchResult(record), order...)
}

// CatalogSearchResultPosition extracts the score, normalized display name and
// stable ID used for continuation ordering.
func CatalogSearchResultPosition(result CatalogSearchResult, order ...SearchOrder) CatalogSearchPosition {
	position := CatalogSearchPosition{Score: result.Score, DisplayName: CatalogSearchSortName(result.DisplayName), ResultID: result.ID}
	switch selectedSearchOrder(order) {
	case "", SearchOrderNameAsc, SearchOrderNameDesc:
		// Name ordering uses the existing normalized display-name position.
	case SearchOrderKindAsc, SearchOrderKindDesc:
		position.SortValue = string(result.Kind)
	case SearchOrderPublisherAsc, SearchOrderPublisherDesc:
		publisher := result.Publisher
		if result.Local != nil && result.Local.Display != nil && result.Local.Display.PublisherName != "" {
			publisher = result.Local.Display.PublisherName
		}
		position.SortValue = CatalogSearchSortName(publisher)
	}
	return position
}

// CatalogSearchPositionAfter reports whether candidate follows after in the
// shared search ordering.
func CatalogSearchPositionAfter(candidate, after CatalogSearchPosition, order ...SearchOrder) bool {
	return searchPositionAfter(candidate, after, order...)
}

func selectedSearchOrder(order []SearchOrder) SearchOrder {
	if len(order) > 0 {
		return order[0]
	}
	return ""
}

// CatalogSearchSortName normalizes a display name for deterministic result
// ordering.
func CatalogSearchSortName(value string) string {
	return strings.ToLower(strings.Join(strings.Fields(value), " "))
}

// IndexedSearchResultID identifies an indexed identity/version group,
// distinguishing an absent version from an empty version.
func IndexedSearchResultID(identifier, version string, versionPresent bool) string {
	return "indexed:v1:sha256:" + searchResultDigest(identifier, version, versionPresent)
}

// LiveSearchResultID binds a live result to its source, identity, version
// presence and exact entry digest.
func LiveSearchResultID(source, identifier, version string, versionPresent bool, entryDigest string) string {
	return "live:v1:sha256:" + searchResultDigest(source, identifier, version, versionPresent, entryDigest)
}

// searchResultDigest hashes typed identity components under the result-ID
// domain separator.
func searchResultDigest(values ...any) string {
	hash := sha256.New()
	_, _ = hash.Write([]byte("catalog-search-result-v1\x00"))
	for _, value := range values {
		switch typed := value.(type) {
		case string:
			_, _ = hash.Write([]byte(typed))
		case bool:
			if typed {
				_, _ = hash.Write([]byte{1})
			} else {
				_, _ = hash.Write([]byte{0})
			}
		}
		_, _ = hash.Write([]byte{0xff})
	}
	return hex.EncodeToString(hash.Sum(nil))
}

// recordsAfterPosition keeps only local records strictly following the shared
// search position.
func recordsAfterPosition(records []CatalogSummary, after CatalogSearchPosition, order ...SearchOrder) []CatalogSummary {
	for index, record := range records {
		if searchPositionAfter(catalogRecordPosition(record, order...), after, order...) {
			return records[index:]
		}
	}
	return nil
}

// searchPositionAfter compares descending relevance followed by normalized name
// and stable result ID.
func searchPositionAfter(candidate, after CatalogSearchPosition, orders ...SearchOrder) bool {
	order := selectedSearchOrder(orders)
	if order != "" {
		left, right := candidate.SortValue, after.SortValue
		if order == SearchOrderNameAsc || order == SearchOrderNameDesc {
			left, right = candidate.DisplayName, after.DisplayName
		}
		if left != right {
			if strings.HasSuffix(string(order), "_desc") {
				return left < right
			}
			return left > right
		}
	}
	if order == "" && candidate.Score != after.Score {
		return candidate.Score < after.Score
	}
	if candidate.DisplayName != after.DisplayName {
		return candidate.DisplayName > after.DisplayName
	}
	return candidate.ResultID > after.ResultID
}

// normalizeCatalogSearchQuery canonicalizes text and set-valued filters before
// search and cursor binding.
func normalizeCatalogSearchQuery(query SearchQuery) SearchQuery {
	query.NamespaceID = strings.ToLower(query.NamespaceID)
	if query.Origins != nil {
		query.Origins = append([]SearchResultType{}, query.Origins...)
		sort.Slice(query.Origins, func(i, j int) bool { return query.Origins[i] < query.Origins[j] })
	}
	query.Text = strings.Join(strings.Fields(query.Text), " ")
	query.Publisher = strings.TrimSpace(query.Publisher)
	query.License = strings.TrimSpace(query.License)
	if len(query.Categories) > 0 {
		query.Categories = normalizeTerms(query.Categories, 16)
	} else {
		query.Categories = nil
	}
	query.Tags = normalizeTerms(query.Tags, 32)
	query.Kinds = normalizedSearchKinds(query.Kinds)
	return query
}

// normalizedSearchKinds sorts and deduplicates kind filters into a stable query
// representation.
func normalizedSearchKinds(values []Kind) []Kind {
	seen := make(map[Kind]struct{}, len(values))
	result := make([]Kind, 0, len(values))
	for _, value := range values {
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	sort.Slice(result, func(i, j int) bool { return result[i] < result[j] })
	return result
}
