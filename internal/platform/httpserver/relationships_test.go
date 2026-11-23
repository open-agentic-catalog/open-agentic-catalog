// These handler tests exercise the wire contract with an isolated repository.
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

type relationshipReadRepository struct {
	catalog.Repository
	selectPage func(string, string, catalog.RelationshipPageRequest) (catalog.RelationshipSelection, error)
}

func (r relationshipReadRepository) ListReleaseRelationships(_ context.Context, tenant, release string, q catalog.RelationshipPageRequest) (catalog.RelationshipSelection, error) {
	return r.selectPage(tenant, release, q)
}

func TestRelationshipHandlerPagesSealedDeclarations(t *testing.T) {
	ctx := identityfixture.WithActor(t.Context(), projectionActor(identity.RoleReader))
	first := catalog.ReleasedRelationship{
		Declaration:     catalog.Relationship{Type: "depends_on", TargetIdentifier: "opaque:renderer", TargetVersion: "1"},
		SourceReleaseID: projectionItem, Ordinal: 1,
	}
	last := first
	last.Ordinal = 2
	last.Declaration.TargetVersion = "2"
	last.Related = &catalog.RelatedRelease{ReleaseID: projectionNext, ListingRevisionID: projectionResource, DisplayName: "Renderer", Version: "2"}
	query := catalog.RelationshipPageRequest{View: catalog.OutgoingRelationships, Limit: 1}
	generation := "10/21"
	reads := 0
	repo := relationshipReadRepository{selectPage: func(tenant, release string, req catalog.RelationshipPageRequest) (catalog.RelationshipSelection, error) {
		reads++
		if tenant != projectionTenant || release != projectionItem || req != query {
			t.Fatalf("unexpected selection %q %q %+v, want %+v", tenant, release, req, query)
		}
		items := []catalog.ReleasedRelationship{last}
		if req.AfterReleaseID == "" {
			items = []catalog.ReleasedRelationship{first, last}
		}
		return catalog.RelationshipSelection{Items: items, Generation: generation}, nil
	}}
	server := projectionServer(projectionCatalog(t, repo, nil))
	read := func(parameters string) *httptest.ResponseRecorder {
		req := httptest.NewRequestWithContext(ctx, http.MethodGet, "/?"+parameters, nil)
		req.SetPathValue("releaseId", projectionItem)
		w := httptest.NewRecorder()
		server.handleReleaseRelationships(w, req)
		return w
	}
	w := read("view=outgoing&limit=1")
	var page struct {
		NextCursor string `json:"nextCursor"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil || page.NextCursor == "" || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("first page %s, %v", w.Body, err)
	}
	requireControlJSON(t, w, 200, map[string]any{"items": []any{map[string]any{"declaration": first.Declaration}}, "nextCursor": page.NextCursor})
	query.AfterReleaseID, query.AfterOrdinal, query.Generation = projectionItem, 1, generation
	w = read("view=outgoing&limit=1&cursor=" + url.QueryEscape(page.NextCursor))
	requireControlJSON(t, w, 200, map[string]any{"items": []any{map[string]any{"declaration": last.Declaration, "related": last.Related}}})
	// Start from the emitted cursor so a binding mismatch cannot mask a bad position.
	resume := httptest.NewRequestWithContext(ctx, http.MethodGet, "/?view=outgoing&limit=1&cursor="+url.QueryEscape(page.NextCursor), nil)
	bound, err := server.parseAdminPage(resume, "release-relationships", projectionItem, "source-release,ordinal", map[string]string{"view": "outgoing"})
	if err != nil {
		t.Fatal(err)
	}
	for _, position := range [][]string{{projectionItem}, {projectionItem, "0", generation}, {projectionItem, "65", generation}} {
		token, err := server.encodeAdminCursor(bound, position)
		if err != nil {
			t.Fatal(err)
		}
		requireProjectionProblem(t, read("view=outgoing&limit=1&cursor="+url.QueryEscape(token)), http.StatusBadRequest, catalog.CodeInvalidArgument)
	}
	for _, invalid := range []string{"", "view=unrecognized", "view=incoming&view=outgoing", "view=outgoing&limit=0", "view=incoming&limit=1&cursor=" + url.QueryEscape(page.NextCursor)} {
		requireProjectionProblem(t, read(invalid), 400, catalog.CodeInvalidArgument)
	}
	if reads != 2 {
		t.Fatalf("expected two admitted reads, got %d", reads)
	}
}
