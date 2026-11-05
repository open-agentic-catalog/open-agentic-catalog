// Administration cursors bind tenant, authorization, route, filters and position
// with an expiry and signature before SQL pagination.

package httpserver

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
)

const (
	adminPageDefaultLimit  = 20
	adminPageMaximumLimit  = 100
	adminCursorMaximumSize = 2048
	adminCursorTTL         = 15 * time.Minute
)

type adminCursorPayload struct {
	Version   int      `json:"v"`
	Position  []string `json:"p"`
	ExpiresAt int64    `json:"e"`
}

type adminCursorBinding struct {
	Purpose             string            `json:"purpose"`
	AuthorizationDigest string            `json:"authorizationDigest"`
	Parent              string            `json:"parent,omitempty"`
	Filters             map[string]string `json:"filters,omitempty"`
	Order               string            `json:"order"`
	Limit               int               `json:"limit"`
}

type adminPageRequest struct {
	Limit    int
	Position []string
	binding  adminCursorBinding
}

type cursorPage[T any] struct {
	Items      []T    `json:"items"`
	NextCursor string `json:"nextCursor,omitempty"`
}

// validateQueryParameters rejects malformed queries, unknown parameter names and repeated values.
func validateQueryParameters(request *http.Request, names ...string) error {
	query, err := parseQueryParameters(request)
	if err != nil {
		return err
	}
	allowed := make(map[string]struct{}, len(names))
	for _, name := range names {
		allowed[name] = struct{}{}
	}
	for name, values := range query {
		if _, ok := allowed[name]; !ok {
			return catalog.E(catalog.CodeInvalidArgument, "this operation does not accept the "+name+" query parameter")
		}
		if len(values) != 1 {
			return catalog.E(catalog.CodeInvalidArgument, name+" must be provided at most once")
		}
	}
	return nil
}

// parseQueryParameters parses the raw query without silently accepting malformed URL encoding.
func parseQueryParameters(request *http.Request) (url.Values, error) {
	query, err := url.ParseQuery(request.URL.RawQuery)
	if err != nil {
		return nil, catalog.E(catalog.CodeInvalidArgument, "query parameters are malformed")
	}
	return query, nil
}

// withQueryParameters validates a route's closed query allowlist before calling its handler.
func (s *Server) withQueryParameters(handler http.HandlerFunc, names ...string) http.HandlerFunc {
	return func(writer http.ResponseWriter, request *http.Request) {
		if err := validateQueryParameters(request, names...); err != nil {
			s.fail(writer, request, err)
			return
		}
		handler(writer, request)
	}
}

// withUUIDPathParameters rejects malformed identifier path values before any domain lookup.
func (s *Server) withUUIDPathParameters(handler http.HandlerFunc, names ...string) http.HandlerFunc {
	return func(writer http.ResponseWriter, request *http.Request) {
		for _, name := range names {
			if !identity.ValidTenantID(request.PathValue(name)) {
				s.fail(writer, request, catalog.E(catalog.CodeInvalidArgument, name+" must be a UUID"))
				return
			}
		}
		handler(writer, request)
	}
}

// setUUIDPathValue validates a command's extracted UUID before exposing it under the handler's path key.
func (s *Server) setUUIDPathValue(writer http.ResponseWriter, request *http.Request, name, value string) bool {
	if !identity.ValidTenantID(value) {
		s.fail(writer, request, catalog.E(catalog.CodeInvalidArgument, name+" must be a UUID"))
		return false
	}
	request.SetPathValue(name, value)
	return true
}

// parseAdminPage binds a signed cursor to current authorization, filters,
// parent resource, ordering, and page size so it cannot be replayed elsewhere.
func (s *Server) parseAdminPage(request *http.Request, purpose, parent, order string, filters map[string]string) (adminPageRequest, error) {
	query, err := parseQueryParameters(request)
	if err != nil {
		return adminPageRequest{}, err
	}
	return s.parseAdminValues(request.Context(), query, purpose, parent, order, filters)
}

func (s *Server) parseAdminValues(ctx context.Context, query url.Values, purpose, parent, order string, filters map[string]string) (adminPageRequest, error) {
	allowed := map[string]struct{}{"limit": {}, "cursor": {}}
	for name := range filters {
		allowed[name] = struct{}{}
	}
	for name, values := range query {
		if _, ok := allowed[name]; !ok {
			return adminPageRequest{}, catalog.E(catalog.CodeInvalidArgument, "this operation does not accept the "+name+" query parameter")
		}
		if len(values) != 1 {
			return adminPageRequest{}, catalog.E(catalog.CodeInvalidArgument, name+" must be provided at most once")
		}
	}
	limit := adminPageDefaultLimit
	if values, present := query["limit"]; present {
		if values[0] == "" {
			return adminPageRequest{}, catalog.E(catalog.CodeInvalidArgument, "limit must be an integer between 1 and 100")
		}
		parsed, err := strconv.Atoi(values[0])
		if err != nil || parsed < 1 || parsed > adminPageMaximumLimit {
			return adminPageRequest{}, catalog.E(catalog.CodeInvalidArgument, "limit must be an integer between 1 and 100")
		}
		limit = parsed
	}
	actor, ok := identity.FromContext(ctx)
	if !ok || actor.TenantID == "" {
		return adminPageRequest{}, catalog.E(catalog.CodeUnauthenticated, "an authenticated Catalog member is required")
	}
	decision, err := identity.CurrentDecisionDigest(ctx)
	if err != nil {
		return adminPageRequest{}, catalog.PermissionError("current cursor authority is required", err)
	}
	binding := adminCursorBinding{Purpose: purpose, AuthorizationDigest: decision, Parent: parent, Filters: filters, Order: order, Limit: limit}
	page := adminPageRequest{Limit: limit, binding: binding}
	if values, present := query["cursor"]; present {
		if values[0] == "" || len(values[0]) > adminCursorMaximumSize {
			return adminPageRequest{}, catalog.E(catalog.CodeInvalidArgument, "cursor must be one non-empty value of at most 2048 characters")
		}
		position, err := s.decodeAdminCursor(values[0], binding)
		if err != nil {
			return adminPageRequest{}, err
		}
		page.Position = position
	}
	return page, nil
}

// parseObservationSearchPage binds body-supplied search pagination to current authorization and canonical filters.
func (s *Server) parseObservationSearchPage(request *http.Request, filters map[string]string, requestedLimit *int, cursor *string) (adminPageRequest, error) {
	query, err := parseQueryParameters(request)
	if err != nil {
		return adminPageRequest{}, err
	}
	if len(query) != 0 {
		return adminPageRequest{}, catalog.E(catalog.CodeInvalidArgument, "this operation does not accept query parameters")
	}
	limit := adminPageDefaultLimit
	if requestedLimit != nil {
		if *requestedLimit < 1 || *requestedLimit > adminPageMaximumLimit {
			return adminPageRequest{}, catalog.E(catalog.CodeInvalidArgument, "limit must be an integer between 1 and 100")
		}
		limit = *requestedLimit
	}
	actor, ok := identity.FromContext(request.Context())
	if !ok || actor.TenantID == "" {
		return adminPageRequest{}, catalog.E(catalog.CodeUnauthenticated, "an authenticated Catalog member is required")
	}
	decision, err := identity.CurrentDecisionDigest(request.Context())
	if err != nil {
		return adminPageRequest{}, catalog.PermissionError("current cursor authority is required", err)
	}
	binding := adminCursorBinding{Purpose: "observation-search", AuthorizationDigest: decision, Filters: filters, Order: "score desc,identifier,id", Limit: limit}
	page := adminPageRequest{Limit: limit, binding: binding}
	if cursor != nil {
		if *cursor == "" || len(*cursor) > adminCursorMaximumSize {
			return adminPageRequest{}, catalog.E(catalog.CodeInvalidArgument, "cursor must be one non-empty value of at most 2048 characters")
		}
		position, err := s.decodeAdminCursor(*cursor, binding)
		if err != nil {
			return adminPageRequest{}, err
		}
		page.Position = position
	}
	return page, nil
}

// encodeAdminCursor signs an expiring position together with the request's authorization and query binding.
func (s *Server) encodeAdminCursor(page adminPageRequest, position []string) (string, error) {
	if len(position) == 0 {
		return "", catalog.E(catalog.CodeInternal, "administration cursor position is empty")
	}
	// Cursor JSON is base64 encoded, never embedded in HTML. HTML escaping
	// would inflate valid bounded subjects beyond the token's byte limit.
	var encoded bytes.Buffer
	encoder := json.NewEncoder(&encoded)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(adminCursorPayload{Version: 1, Position: position, ExpiresAt: s.now().Add(adminCursorTTL).Unix()}); err != nil {
		return "", catalog.Wrap(catalog.CodeInternal, "encode administration cursor", err)
	}
	payload := bytes.TrimSuffix(encoded.Bytes(), []byte("\n"))
	binding, err := json.Marshal(page.binding)
	if err != nil {
		return "", catalog.Wrap(catalog.CodeInternal, "bind administration cursor", err)
	}
	mac := hmac.New(sha256.New, s.cursorKey)
	_, _ = mac.Write([]byte("administration-cursor-v1\x00"))
	_, _ = mac.Write(payload)
	_, _ = mac.Write([]byte("\x00"))
	_, _ = mac.Write(binding)
	token := base64.RawURLEncoding.EncodeToString(payload) + "." + hex.EncodeToString(mac.Sum(nil))
	if len(token) > adminCursorMaximumSize {
		return "", catalog.E(catalog.CodeInternal, "administration cursor exceeds its safe bound")
	}
	return token, nil
}

// decodeAdminCursor verifies canonical encoding, request binding, signature and expiry before returning a position.
func (s *Server) decodeAdminCursor(token string, cursorBinding adminCursorBinding) ([]string, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 2 {
		return nil, catalog.E(catalog.CodeInvalidArgument, "cursor is invalid")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil || base64.RawURLEncoding.EncodeToString(payload) != parts[0] {
		return nil, catalog.E(catalog.CodeInvalidArgument, "cursor is invalid")
	}
	provided, err := hex.DecodeString(parts[1])
	if err != nil || hex.EncodeToString(provided) != parts[1] {
		return nil, catalog.E(catalog.CodeInvalidArgument, "cursor is invalid")
	}
	binding, err := json.Marshal(cursorBinding)
	if err != nil {
		return nil, catalog.Wrap(catalog.CodeInternal, "bind administration cursor", err)
	}
	mac := hmac.New(sha256.New, s.cursorKey)
	_, _ = mac.Write([]byte("administration-cursor-v1\x00"))
	_, _ = mac.Write(payload)
	_, _ = mac.Write([]byte("\x00"))
	_, _ = mac.Write(binding)
	if !hmac.Equal(provided, mac.Sum(nil)) {
		return nil, catalog.E(catalog.CodeInvalidArgument, "cursor is invalid for this request")
	}
	var value adminCursorPayload
	decoder := json.NewDecoder(strings.NewReader(string(payload)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&value); err != nil || value.Version != 1 || len(value.Position) == 0 {
		return nil, catalog.E(catalog.CodeInvalidArgument, "cursor is invalid")
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return nil, catalog.E(catalog.CodeInvalidArgument, "cursor is invalid")
	}
	if !time.Unix(value.ExpiresAt, 0).After(s.now()) {
		return nil, catalog.E(catalog.CodeInvalidArgument, "cursor has expired")
	}
	for _, item := range value.Position {
		if len(item) > 2048 {
			return nil, catalog.E(catalog.CodeInvalidArgument, "cursor is invalid")
		}
	}
	return value.Position, nil
}

// finishCursorPage consumes a repository's bounded limit-plus-one page and signs the last visible position when more items exist.
func finishCursorPage[T any](server *Server, request adminPageRequest, items []T, position func(T) []string) (cursorPage[T], error) {
	page := cursorPage[T]{Items: items}
	if len(items) <= request.Limit {
		return page, nil
	}
	page.Items = items[:request.Limit]
	last := page.Items[len(page.Items)-1]
	next, err := server.encodeAdminCursor(request, position(last))
	if err != nil {
		return cursorPage[T]{}, err
	}
	page.NextCursor = next
	return page, nil
}
