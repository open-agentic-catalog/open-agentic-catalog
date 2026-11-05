package httpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/catalogmcp"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/controlclient"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/observability"
)

var _ catalogmcp.Reader = (*Server)(nil)

// admitRead retains coarse scope admission for both Control handlers and direct
// MCP reads. Exact permissions and current policy remain domain responsibilities.
func (s *Server) admitRead(ctx context.Context, resource bool) error {
	actor, ok := identity.FromContext(ctx)
	if !ok {
		return catalog.E(catalog.CodeUnauthenticated, "an authenticated Catalog member is required")
	}
	if resource && s.enforceOAuthScopes && actor.Claims["scope:catalog.read"] != "true" {
		return catalog.E(catalog.CodeForbidden, "OAuth scope catalog.read is required")
	}
	return ctx.Err()
}

func readEnvelope(ctx context.Context, value any, etag string, err error) (controlclient.Envelope, error) {
	if err != nil {
		return controlclient.Envelope{}, err
	}
	data, err := json.Marshal(value)
	return controlclient.Envelope{Data: data, Meta: controlclient.Metadata{RequestID: requestID(ctx), ETag: etag}}, err
}

func (s *Server) ReadCapabilities(ctx context.Context) (controlclient.Envelope, error) {
	if _, ok := identity.FromContext(ctx); !ok {
		return controlclient.Envelope{}, catalog.E(catalog.CodeForbidden, "authenticated actor is required")
	}
	if err := s.admitRead(ctx, false); err != nil {
		return controlclient.Envelope{}, err
	}
	value, err := s.capabilitiesRead(ctx)
	return readEnvelope(ctx, value, "", err)
}

func (s *Server) ReadSearch(ctx context.Context, query catalog.SearchQuery) (controlclient.Envelope, error) {
	if err := s.admitRead(ctx, true); err != nil {
		return controlclient.Envelope{}, err
	}
	page, err := s.catalogSearch.Search(ctx, query)
	if err == nil {
		page.Items, err = s.resolveArtwork(ctx, page.Items)
	}
	if err == nil {
		page.Items, err = s.resolveRatings(ctx, page.Items)
	}
	if err == nil {
		outcome := observability.OutcomeSucceeded
		if page.Partial {
			outcome = observability.OutcomePartial
		}
		observeOperation(ctx, observability.OperationCatalogSearch, outcome)
	}
	return readEnvelope(ctx, page, "", err)
}

func (s *Server) ReadExplore(ctx context.Context, query catalog.ExploreRequest) (controlclient.Envelope, error) {
	if err := s.admitRead(ctx, true); err != nil {
		return controlclient.Envelope{}, err
	}
	value, err := s.catalog.Explore(ctx, query)
	return readEnvelope(ctx, value, "", err)
}

func (s *Server) ReadRelease(ctx context.Context, releaseID string) (controlclient.Envelope, error) {
	if err := s.admitRead(ctx, true); err != nil {
		return controlclient.Envelope{}, err
	}
	record, err := s.catalog.GetCatalogRecord(ctx, releaseID)
	return readEnvelope(ctx, record, fmt.Sprintf(`"rev-%d"`, record.Release.Revision), err)
}

func (s *Server) ReadIndexed(ctx context.Context, key catalog.IndexedIdentity) (controlclient.Envelope, error) {
	if err := s.admitRead(ctx, true); err != nil {
		return controlclient.Envelope{}, err
	}
	item, err := s.catalogSearch.GetIndexed(ctx, key)
	items := []catalog.CatalogSearchResult{item}
	if err == nil {
		items, err = s.resolveArtwork(ctx, items)
	}
	if err == nil {
		items, err = s.resolveRatings(ctx, items)
	}
	if err != nil {
		return controlclient.Envelope{}, err
	}
	return readEnvelope(ctx, items[0], "", nil)
}

func (s *Server) ReadAccess(ctx context.Context, query url.Values) (controlclient.Envelope, error) {
	if err := s.admitRead(ctx, false); err != nil {
		return controlclient.Envelope{}, err
	}
	value, err := s.selfRead(ctx, query)
	return readEnvelope(ctx, value, "", err)
}

func (s *Server) respondRead(w http.ResponseWriter, r *http.Request, reply controlclient.Envelope, err error) {
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if reply.Meta.ETag != "" {
		w.Header().Set("ETag", reply.Meta.ETag)
	}
	s.respondJSON(w, r, http.StatusOK, reply.Data)
}
