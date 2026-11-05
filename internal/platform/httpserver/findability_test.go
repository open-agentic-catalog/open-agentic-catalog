package httpserver

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/test/identityfixture"
)

func TestResourceExactFilterNormalizesBeforeBindingCursor(t *testing.T) {
	ctx := identityfixture.WithActor(t.Context(), projectionActor(identity.RoleReader))
	reads := 0
	item := controlReadFixture(t).record.Resource
	repo := controlPageRepository{resources: func(_ context.Context, tenant string, p catalog.ResourceListRequest) ([]catalog.Resource, error) {
		reads++
		if tenant != projectionTenant || p.CanonicalIdentifier != "urn:air:catalog.example.com:Finance:MyAgent" || p.Kind != catalog.KindAgent || p.Limit != 2 {
			t.Fatalf("unbounded or changed filter: %+v", p)
		}
		if reads == 1 {
			next := item
			next.ID = projectionNext
			return []catalog.Resource{item, next}, nil
		}
		return []catalog.Resource{}, nil
	}}
	server := projectionServer(projectionCatalog(t, repo, nil))
	query := url.Values{"canonicalIdentifier": {" URN:AIR:CATALOG.EXAMPLE.COM:Finance:MyAgent "}, "kind": {"agent"}, "limit": {"1"}}
	read := func(q url.Values) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		server.handleResourcesList(w, httptest.NewRequestWithContext(ctx, http.MethodGet, "/?"+q.Encode(), nil))
		return w
	}
	first := read(query)
	if first.Code != 200 {
		t.Fatal(first.Code, first.Body.String())
	}
	var page cursorPage[catalog.Resource]
	if err := json.Unmarshal(first.Body.Bytes(), &page); err != nil || page.NextCursor == "" {
		t.Fatal(page, err)
	}
	query.Set("cursor", page.NextCursor)
	query.Set("canonicalIdentifier", "urn:air:catalog.example.com:Finance:MyAgent")
	if w := read(query); w.Code != 200 {
		t.Fatal("canonical-equivalent continuation refused", w.Code, w.Body.String())
	}
	query.Set("canonicalIdentifier", "urn:air:catalog.example.com:Finance:Other")
	requireProjectionProblem(t, read(query), 400, catalog.CodeInvalidArgument)
	query.Set("canonicalIdentifier", "urn:air:catalog.example.com:Finance:MyAgent")
	query.Set("kind", "skill")
	requireProjectionProblem(t, read(query), 400, catalog.CodeInvalidArgument)
	query.Del("cursor")
	query.Set("canonicalIdentifier", "")
	requireProjectionProblem(t, read(query), 400, catalog.CodeInvalidArgument)
	query.Set("canonicalIdentifier", "urn:air:bad")
	requireProjectionProblem(t, read(query), 400, catalog.CodeInvalidArgument)
	if reads != 2 {
		t.Fatal("invalid filter/cursor reached repository", reads)
	}
}
