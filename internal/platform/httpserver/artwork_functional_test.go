//go:build functional

package httpserver

import (
	"bytes"
	"net/http"
	"net/url"
	"strconv"
	"testing"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/federation"
)

// This extends the existing real HTTP federation journey rather than creating
// a second source onboarding fixture. The owned image bytes never leave tests.
func qualifyHTTPArtworkOverrides(t *testing.T, base string, source federation.Source, indexed catalog.CatalogSearchResult, fetch *artworkHTTPFetcher) {
	t.Helper()
	target := federation.ArtworkTarget{SourceID: source.ID, PresentationGeneration: indexed.Indexed.PresentationGeneration, Identifier: indexed.Identifier, VersionPresent: indexed.VersionPresent, Version: indexed.Version, Kind: indexed.Kind}
	path := "/api/v1/federation/sources/" + source.ID + "/artwork"
	query := url.Values{"identifier": {target.Identifier}, "versionPresent": {strconv.FormatBool(target.VersionPresent)}, "kind": {string(target.Kind)}, "presentationGeneration": {strconv.FormatInt(target.PresentationGeneration, 10)}}
	body := map[string]any{"target": target, "icon": map[string]any{"mode": "replace", "asset": artworkHTTPInput().Icon}, "screenshots": map[string]any{"mode": "inherit"}, "reason": "Use owned local presentation artwork"}
	missing := requestJSON(t, base, http.MethodGet, path+"?"+query.Encode(), "source-admin", "", "", nil)
	requireControlProblem(t, missing, http.StatusNotFound, catalog.CodeNotFound)
	requireControlProblem(t, requestJSON(t, base, http.MethodPost, path, "reader", "artwork-forbidden", "", body), http.StatusForbidden, catalog.CodeForbidden)
	created := requestJSON(t, base, http.MethodPost, path, "source-admin", "artwork-create", "", body)
	requireStatus(t, created, http.StatusOK)
	requireControlSchema(t, "ArtworkOverride", created.body)
	if created.header.Get("ETag") != `"rev-1"` {
		t.Fatal("missing override revision")
	}
	calls := fetch.calls
	replay := requestJSON(t, base, http.MethodPost, path, "source-admin", "artwork-create", "", body)
	requireStatus(t, replay, http.StatusOK)
	if replay.header.Get("Idempotency-Replayed") != "true" || !bytes.Equal(replay.body, created.body) || fetch.calls != calls {
		t.Fatal("override replay fetched or changed image")
	}
	get := requestJSON(t, base, http.MethodGet, path+"?"+query.Encode(), "source-admin", "", "", nil)
	requireStatus(t, get, http.StatusOK)
	requireControlSchema(t, "ArtworkOverride", get.body)
	readBody := map[string]any{"origin": "indexed", "target": target, "slot": "icon"}
	image := requestJSON(t, base, http.MethodPost, "/api/v1/artwork/read", "reader", "", "", readBody)
	requireStatus(t, image, http.StatusOK)
	if !bytes.Equal(image.body, fetch.body) || image.header.Get("Content-Digest") != digestHeader(fetch.body) {
		t.Fatal("indexed override bytes differ")
	}
	body["icon"] = map[string]any{"mode": "suppress"}
	suppressed := requestJSON(t, base, http.MethodPost, path, "source-admin", "artwork-suppress", `"rev-1"`, body)
	requireStatus(t, suppressed, http.StatusOK)
	requireControlProblem(t, requestJSON(t, base, http.MethodPost, "/api/v1/artwork/read", "reader", "", "", readBody), http.StatusNotFound, catalog.CodeNotFound)
	requireControlProblem(t, requestJSON(t, base, http.MethodPost, path, "source-admin", "artwork-stale", `"rev-1"`, body), http.StatusPreconditionFailed, catalog.CodePreconditionFailed)
	body["icon"] = map[string]any{"mode": "inherit"}
	requireStatus(t, requestJSON(t, base, http.MethodPost, path, "source-admin", "artwork-reset", `"rev-2"`, body), http.StatusOK)
	history := requestJSON(t, base, http.MethodGet, path+"/history?"+query.Encode()+"&limit=2", "source-admin", "", "", nil)
	requireStatus(t, history, http.StatusOK)
	requireControlSchema(t, "ArtworkOverridePage", history.body)
	var page cursorPage[federation.ArtworkOverride]
	decodeResult(t, history, &page)
	if len(page.Items) != 2 || page.NextCursor == "" {
		t.Fatal("override history missing page")
	}
	next := requestJSON(t, base, http.MethodGet, path+"/history?"+query.Encode()+"&limit=2&cursor="+url.QueryEscape(page.NextCursor), "source-admin", "", "", nil)
	requireStatus(t, next, http.StatusOK)
	page = cursorPage[federation.ArtworkOverride]{}
	decodeResult(t, next, &page)
	if len(page.Items) != 1 || page.NextCursor != "" {
		t.Fatal("override history lost final revision")
	}
	// This peer's live version has no indexed observation. A current signed
	// result still permits an exact presentation override without local adoption.
	liveSearch := func() catalog.CatalogSearchResult {
		t.Helper()
		reply := requestJSON(t, base, http.MethodPost, "/api/v1/catalog/search", "source-admin", "", "", map[string]any{"text": "remote agent", "origins": []string{"live"}, "limit": 5})
		requireStatus(t, reply, http.StatusOK)
		requireControlSchema(t, "CatalogSearchResponse", reply.body)
		var page catalog.CatalogSearchResponse
		decodeResult(t, reply, &page)
		if len(page.Items) != 1 || page.Items[0].Artwork == nil || page.Items[0].Artwork.Receipt == "" {
			t.Fatal("admitted live result missing receipt")
		}
		return page.Items[0]
	}
	live := liveSearch()
	liveTarget := target
	liveTarget.VersionPresent = true
	liveTarget.Version = live.Version
	body["target"] = liveTarget
	body["receipt"] = live.Artwork.Receipt
	body["icon"] = map[string]any{"mode": "replace", "asset": artworkHTTPInput().Icon}
	requireStatus(t, requestJSON(t, base, http.MethodPost, path, "source-admin", "live-artwork-create", "", body), http.StatusOK)
	live = liveSearch()
	if live.Artwork.Descriptor == nil || live.Artwork.Descriptor.Icon == nil {
		t.Fatal("live override not resolved")
	}
	liveRead := map[string]any{"origin": "live", "receipt": live.Artwork.Receipt, "descriptor": live.Artwork.Descriptor, "slot": "icon"}
	requireStatus(t, requestJSON(t, base, http.MethodPost, "/api/v1/artwork/read", "source-admin", "", "", liveRead), http.StatusOK)
	body["receipt"] = live.Artwork.Receipt
	body["icon"] = map[string]any{"mode": "suppress"}
	requireStatus(t, requestJSON(t, base, http.MethodPost, path, "source-admin", "live-artwork-suppress", `"rev-1"`, body), http.StatusOK)
	requireControlProblem(t, requestJSON(t, base, http.MethodPost, "/api/v1/artwork/read", "source-admin", "", "", liveRead), http.StatusPreconditionFailed, catalog.CodePreconditionFailed)
}
