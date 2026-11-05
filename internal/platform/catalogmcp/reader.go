package catalogmcp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/controlclient"
)

// Reader supplies only the six selected Control reads. Implementations retain
// their own authentication boundary; tool arguments cannot select an authority.
type Reader interface {
	ReadCapabilities(context.Context) (controlclient.Envelope, error)
	ReadSearch(context.Context, catalog.SearchQuery) (controlclient.Envelope, error)
	ReadExplore(context.Context, catalog.ExploreRequest) (controlclient.Envelope, error)
	ReadRelease(context.Context, string) (controlclient.Envelope, error)
	ReadIndexed(context.Context, catalog.IndexedIdentity) (controlclient.Envelope, error)
	ReadAccess(context.Context, url.Values) (controlclient.Envelope, error)
}

// AccessRequest selects the caller's effective namespace permissions, never
// another user's assignments. A zero limit selects the Control default of 20.
type AccessRequest struct {
	NamespaceID string `json:"namespaceId,omitempty"`
	Limit       int    `json:"limit,omitempty"`
	Cursor      string `json:"cursor,omitempty"`
}

type remoteReader struct {
	session *controlclient.Session
	limits  *Limits
}

func (r remoteReader) exchange(ctx context.Context, path string, body any) (controlclient.Envelope, error) {
	request := controlclient.Request{Method: http.MethodGet, Path: path, MaxResponseBytes: r.limits.ResponseBytes}
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return controlclient.Envelope{}, err
		}
		request.Method, request.Body = http.MethodPost, data
	}
	return controlclient.Exchange(ctx, r.session.Client, r.session.Origin, r.session.Token, request)
}

func (r remoteReader) ReadCapabilities(ctx context.Context) (controlclient.Envelope, error) {
	return r.exchange(ctx, "/api/v1/capabilities", nil)
}
func (r remoteReader) ReadSearch(ctx context.Context, q catalog.SearchQuery) (controlclient.Envelope, error) {
	return r.exchange(ctx, "/api/v1/catalog/search", q)
}
func (r remoteReader) ReadExplore(ctx context.Context, q catalog.ExploreRequest) (controlclient.Envelope, error) {
	return r.exchange(ctx, "/api/v1/catalog/explore", q)
}
func (r remoteReader) ReadRelease(ctx context.Context, id string) (controlclient.Envelope, error) {
	return r.exchange(ctx, "/api/v1/catalog/resources/"+id, nil)
}
func (r remoteReader) ReadIndexed(ctx context.Context, id catalog.IndexedIdentity) (controlclient.Envelope, error) {
	q := url.Values{"identifier": {id.Identifier}, "versionPresent": {strconv.FormatBool(id.VersionPresent)}}
	if id.VersionPresent {
		q.Set("version", id.Version)
	}
	return r.exchange(ctx, "/api/v1/catalog/indexed-record?"+q.Encode(), nil)
}
func (r remoteReader) ReadAccess(ctx context.Context, q url.Values) (controlclient.Envelope, error) {
	path := "/api/v1/me"
	if len(q) > 0 {
		path += "?" + q.Encode()
	}
	return r.exchange(ctx, path, nil)
}

// Values preserves opaque continuation and the existing Control query shape.
func (a AccessRequest) Values() url.Values {
	q := url.Values{}
	if a.NamespaceID != "" {
		q.Set("namespaceId", a.NamespaceID)
	}
	if a.Limit != 0 {
		q.Set("limit", strconv.Itoa(a.Limit))
	}
	if a.Cursor != "" {
		q.Set("cursor", a.Cursor)
	}
	return q
}

func read(ctx context.Context, reader Reader, name string, selected selection) (controlclient.Envelope, error) {
	switch name {
	case "catalog_control_capabilities":
		return reader.ReadCapabilities(ctx)
	case "catalog_control_search":
		return reader.ReadSearch(ctx, selected.query)
	case "catalog_control_explore":
		return reader.ReadExplore(ctx, selected.explore)
	case "catalog_control_release":
		return reader.ReadRelease(ctx, selected.release)
	case "catalog_control_indexed_record":
		return reader.ReadIndexed(ctx, catalog.IndexedIdentity{Identifier: selected.identifier, Version: selected.version, VersionPresent: selected.present})
	default:
		return reader.ReadAccess(ctx, selected.access.Values())
	}
}
