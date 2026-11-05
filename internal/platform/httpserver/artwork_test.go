package httpserver

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/artwork"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/federation"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/clock"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/id"
	"github.com/open-agentic-catalog/open-agentic-catalog/test/identityfixture"
)

type artworkHTTPFetcher struct {
	calls  int
	body   []byte
	err    error
	tenant string
}

func (f *artworkHTTPFetcher) Fetch(_ context.Context, r artwork.FetchRequest) (artwork.FetchResult, error) {
	f.calls++
	f.tenant = r.TenantID
	return artwork.FetchResult{Body: f.body, MediaType: "image/png"}, f.err
}
func artworkHTTPImage(t *testing.T) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := png.Encode(&b, image.NewNRGBA(image.Rect(0, 0, 2, 3))); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}
func artworkHTTPInput() artwork.Input {
	return artwork.Input{SchemaVersion: 1, Icon: &artwork.AssetInput{URL: "https://images.example/icon.png", Alt: "Owned icon", Rights: &artwork.Rights{Basis: "owned", Attribution: "Catalog fixture"}}}
}

type artworkHTTPRepository struct {
	catalog.Repository
	revision catalog.ListingRevision
	listing  catalog.CatalogListing
	record   catalog.CatalogRecord
}

func (p artworkHTTPRepository) GetListingRevision(_ context.Context, tenant, id string) (catalog.ListingRevision, error) {
	if tenant != projectionTenant || id != p.revision.ID {
		return catalog.ListingRevision{}, catalog.E(catalog.CodeNotFound, "revision not found")
	}
	return p.revision, nil
}
func (p artworkHTTPRepository) GetListing(_ context.Context, tenant, id string) (catalog.CatalogListing, error) {
	if tenant != projectionTenant || id != p.listing.ID {
		return catalog.CatalogListing{}, catalog.E(catalog.CodeNotFound, "listing not found")
	}
	return p.listing, nil
}
func (p artworkHTTPRepository) GetCatalogRecord(_ context.Context, tenant, id string) (catalog.CatalogRecord, error) {
	if tenant != projectionTenant || id != p.record.Release.ID {
		return catalog.CatalogRecord{}, catalog.E(catalog.CodeNotFound, "record not found")
	}
	return p.record, nil
}

func TestArtworkReadCurrentLocalReleaseAndSafeFailures(t *testing.T) {
	fetch := &artworkHTTPFetcher{body: artworkHTTPImage(t)}
	media, err := artwork.New(fetch, artwork.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	descriptor, err := media.Seal(t.Context(), projectionTenant, artworkHTTPInput())
	if err != nil {
		t.Fatal(err)
	}
	revision := catalog.ListingRevision{ID: projectionItem, ListingID: projectionNext, Extensions: map[string]any{artwork.IRI: descriptor}}
	repository := artworkHTTPRepository{revision: revision, listing: catalog.CatalogListing{ID: projectionNext, NamespaceID: projectionResource, CurrentReleaseID: projectionNext}, record: catalog.CatalogRecord{Resource: catalog.Resource{NamespaceID: projectionResource}, ListingRevision: revision, Release: catalog.ListingRelease{ID: projectionNext}}}
	server := projectionServer(projectionCatalog(t, repository, nil))
	server.artwork = media
	server.maxBodyBytes = artworkReadMaximum
	ctx := identityfixture.WithActor(t.Context(), projectionActor(identity.RoleReader))
	read := func(body string, context context.Context) *httptest.ResponseRecorder {
		r := httptest.NewRequestWithContext(context, http.MethodPost, "/api/v1/artwork/read", strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		server.handleArtworkRead(w, r)
		return w
	}
	body := `{"origin":"local","listingRevisionId":"` + projectionItem + `","slot":"icon"}`
	w := read(body, ctx)
	digest := sha256.Sum256(fetch.body)
	if w.Code != 200 || !bytes.Equal(w.Body.Bytes(), fetch.body) || w.Header().Get("Content-Type") != "image/png" || w.Header().Get("Content-Digest") != "sha-256=:"+base64.StdEncoding.EncodeToString(digest[:])+":" || w.Header().Get("Cache-Control") != "no-store" || w.Header().Get("X-Content-Type-Options") != "nosniff" || fetch.tenant != projectionTenant {
		t.Fatalf("unsafe image response: %d %v %s", w.Code, w.Header(), w.Body.String())
	}
	calls := fetch.calls
	for _, bad := range []string{`{"origin":"url","slot":"icon"}`, `{"origin":"local","listingRevisionId":"` + projectionItem + `","slot":"icon","index":0}`, `{"origin":"local","listingRevisionId":"` + projectionItem + `","slot":"screenshot","index":8}`, `{"origin":"local","listingRevisionId":"` + projectionItem + `","slot":"icon","url":"https://images.example/spoof.png"}`, `{"origin":"local","origin":"live","slot":"icon"}`, `{"origin":"local","listingRevisionId":"` + projectionItem + `","slot":"icon","target":{}}`} {
		requireProjectionProblem(t, read(bad, ctx), 400, catalog.CodeInvalidArgument)
	}
	if fetch.calls != calls {
		t.Fatal("invalid target performed outbound request")
	}
	other := projectionActor(identity.RoleReader)
	other.TenantID = projectionNext
	requireProjectionProblem(t, read(body, identityfixture.WithActor(t.Context(), other)), 404, catalog.CodeNotFound)
	fetch.err = errors.New("private-fixture secret dependency error")
	requireProjectionProblem(t, read(body, ctx), 503, catalog.CodeDependencyUnavailable)
	fetch.err = nil
	fetch.body = artworkHTTPImage(t)
	fetch.body[len(fetch.body)-1] ^= 1
	requireProjectionProblem(t, read(body, ctx), 503, catalog.CodeDependencyUnavailable)
	if requiresIdempotency(httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/v1/artwork/read", nil)) {
		t.Fatal("binary read treated as command")
	}
}

type artworkReceiptStore struct {
	federation.Store
	federation.ArtworkStore
	source         federation.Source
	overrideDigest string
	head           *federation.ArtworkOverride
}

type artworkReadStore struct {
	federation.Store
	read func(context.Context, string, []federation.ArtworkReadRequest) (map[federation.ArtworkTarget]federation.ArtworkReadContext, error)
}

func (s artworkReadStore) ArtworkReadBatch(ctx context.Context, tenant string, requests []federation.ArtworkReadRequest) (map[federation.ArtworkTarget]federation.ArtworkReadContext, error) {
	return s.read(ctx, tenant, requests)
}

func TestSearchArtworkProjectionPreservesPageAndIssuesOnlyCurrentReceipts(t *testing.T) {
	source := federationFixtureSource()
	source.ArtworkEnabled, source.Live.Enabled, source.PresentationGeneration = true, true, 1
	live := catalog.CatalogSearchResult{Identifier: federationFixtureAIR, Kind: catalog.KindAgent, Live: &catalog.LiveSearchEvidence{SourceID: source.ID, SourceRevision: source.Revision, PresentationGeneration: 1, PolicyDigest: source.PolicyDigest, CredentialRevision: source.CredentialRevision, Entry: json.RawMessage(`{"identifier":"urn:air:peer.example:default:fixture"}`)}}
	stale := live
	stale.Identifier = "urn:air:peer.example:default:stale"
	staleLive := *live.Live
	staleLive.SourceRevision++
	stale.Live = &staleLive
	items := []catalog.CatalogSearchResult{{Identifier: "urn:air:catalog.example:default:local", Kind: catalog.KindSkill}, live, stale}
	before, err := json.Marshal(items)
	if err != nil {
		t.Fatal(err)
	}
	server := projectionServer(nil)
	ctx := identityfixture.WithActor(t.Context(), projectionActor(identity.RoleTenantAdmin))
	if result, err := server.resolveArtwork(ctx, items); err != nil || len(result) != len(items) {
		t.Fatalf("disabled artwork changed the selected page: %v", err)
	}
	fetch := &artworkHTTPFetcher{}
	server.artwork, err = artwork.New(fetch, artwork.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	// No federation manager is needed for a page containing only local results.
	if result, err := server.resolveArtwork(ctx, items[:1]); err != nil || len(result) != 1 || result[0].Identifier != items[0].Identifier {
		t.Fatalf("local-only page required federation or changed identity: %v", err)
	}
	batches := 0
	var lookupFailure error
	store := artworkReadStore{read: func(_ context.Context, tenant string, requests []federation.ArtworkReadRequest) (map[federation.ArtworkTarget]federation.ArtworkReadContext, error) {
		batches++
		if tenant != source.TenantID || len(requests) != 2 {
			t.Fatal("artwork resolution widened the selected tenant or page")
		}
		contexts := map[federation.ArtworkTarget]federation.ArtworkReadContext{}
		for _, request := range requests {
			if !request.Live || request.Target.SourceID != source.ID || request.Target.PresentationGeneration != 1 {
				t.Fatal("artwork lookup lost live source identity")
			}
			contexts[request.Target] = federation.ArtworkReadContext{Source: source, Eligible: true}
		}
		return contexts, lookupFailure
	}}
	server.federation, err = federation.NewManager(store, publicPolicyFetcher{}, clock.NewFake(projectionTime), id.Generator{})
	if err != nil {
		t.Fatal(err)
	}
	result, err := server.resolveArtwork(ctx, items)
	if err != nil || len(result) != 3 || batches != 1 || fetch.calls != 0 {
		t.Fatalf("page projection failed, repeated lookup or fetched image bytes: %v", err)
	}
	for index := range items {
		if result[index].Identifier != items[index].Identifier {
			t.Fatal("artwork resolution reordered or replaced search results")
		}
	}
	if result[0].Artwork != nil || result[1].Artwork == nil || result[1].Artwork.Receipt == "" || result[2].Artwork != nil || result[2].Live.ArtworkAdmitted {
		t.Fatal("receipt was missing for admitted evidence or issued to stale/local evidence")
	}
	parts := strings.Split(result[1].Artwork.Receipt, ".")
	if len(parts) != 2 || len(result[1].Artwork.Receipt) > artworkReceiptMaximum {
		t.Fatal("admitted receipt has an invalid bounded envelope")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		t.Fatal(err)
	}
	var proof artworkReceipt
	actor, _ := identity.FromContext(ctx)
	if json.Unmarshal(payload, &proof) != nil || proof.Target.Identifier != live.Identifier || proof.Target.SourceID != source.ID || proof.SourceRevision != source.Revision || proof.Tenant != source.TenantID || proof.Principal != actor.CommandPrincipal() || proof.Authorization == "" {
		t.Fatal("search receipt lost exact source, item or current principal binding")
	}
	after, err := json.Marshal(items)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("artwork projection rewrote selected source evidence")
	}
	lookupFailure = errors.New("owned lookup unavailable")
	if result, err := server.resolveArtwork(ctx, items); result != nil || !errors.Is(err, lookupFailure) || fetch.calls != 0 {
		t.Fatal("failed artwork lookup returned a partial authorized page or fetched images")
	}
}

func TestIndexedArtworkReadRechecksSelectionBeforeImageContact(t *testing.T) {
	for _, tc := range []struct {
		name                            string
		slot                            string
		index                           *int
		change                          func(*catalog.CatalogSearchResult)
		withdrawn, shadowed, ineligible bool
		batchFailure                    bool
		status                          int
		code                            catalog.ErrorCode
		searches, batches, images       int
	}{
		{name: "current icon", slot: "icon", status: 200, searches: 1, batches: 1, images: 1},
		{name: "current screenshot", slot: "screenshot", index: artworkIndex(0), status: 200, searches: 1, batches: 1, images: 1},
		{name: "winning source changed", slot: "icon", change: func(item *catalog.CatalogSearchResult) { item.Indexed.PresentationSourceID = projectionNext }, status: 412, code: catalog.CodePreconditionFailed, searches: 1},
		{name: "generation changed", slot: "icon", change: func(item *catalog.CatalogSearchResult) { item.Indexed.PresentationGeneration++ }, status: 412, code: catalog.CodePreconditionFailed, searches: 1},
		{name: "kind changed", slot: "icon", change: func(item *catalog.CatalogSearchResult) { item.Kind = catalog.KindPlugin }, status: 412, code: catalog.CodePreconditionFailed, searches: 1},
		{name: "observation withdrawn", slot: "icon", withdrawn: true, status: 404, code: catalog.CodeNotFound, searches: 1},
		{name: "explicit indexed artwork with local counterpart", slot: "icon", shadowed: true, status: 200, searches: 1, batches: 1, images: 1},
		{name: "artwork context unavailable", slot: "icon", batchFailure: true, status: 503, code: catalog.CodeDependencyUnavailable, searches: 1, batches: 1},
		{name: "source no longer eligible", slot: "icon", ineligible: true, status: 404, code: catalog.CodeNotFound, searches: 1, batches: 1},
		{name: "missing screenshot", slot: "screenshot", index: artworkIndex(1), status: 404, code: catalog.CodeNotFound, searches: 1, batches: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fetch := &artworkHTTPFetcher{body: artworkHTTPImage(t)}
			media, err := artwork.New(fetch, artwork.DefaultLimits())
			if err != nil {
				t.Fatal(err)
			}
			input := artworkHTTPInput()
			input.Screenshots = []artwork.AssetInput{*input.Icon}
			descriptor, err := media.Seal(t.Context(), projectionTenant, input)
			if err != nil {
				t.Fatal(err)
			}
			fetch.calls = 0
			source := federationFixtureSource()
			source.ArtworkEnabled = true
			target := federation.ArtworkTarget{SourceID: source.ID, PresentationGeneration: 1, Identifier: federationFixtureAIR, VersionPresent: true, Version: "1.0", Kind: catalog.KindAgent}
			selected := catalog.IndexedIdentity{Identifier: target.Identifier, VersionPresent: true, Version: target.Version}
			item := catalog.CatalogSearchResult{Type: catalog.SearchResultIndexed, Identifier: target.Identifier, VersionPresent: true, Version: target.Version, Kind: target.Kind, Indexed: &catalog.IndexedSearchEvidence{PresentationSourceID: source.ID, PresentationGeneration: 1, NormalizedEntryDigest: "selected-evidence"}}
			if tc.change != nil {
				tc.change(&item)
			}
			searches, batches, precedenceReads := 0, 0, 0
			repo := indexedProjectionRepository{published: func(_ context.Context, tenant, identifier, version string) (bool, error) {
				precedenceReads++
				if tenant != projectionTenant || identifier != target.Identifier || version != target.Version {
					t.Fatal("local precedence lost exact tenant/identity")
				}
				return tc.shadowed, nil
			}}
			search, err := catalog.NewSearchService(projectionCatalog(t, repo, nil), indexedProjectionSearch(func(_ context.Context, request catalog.FederatedCatalogSearchRequest) (catalog.FederatedCatalogSearchPage, error) {
				searches++
				if request.Limit != 1 || request.ExactIdentity == nil || *request.ExactIdentity != selected {
					t.Fatalf("image lookup widened the selected identity: %#v", request)
				}
				if tc.withdrawn {
					return catalog.FederatedCatalogSearchPage{}, nil
				}
				return catalog.FederatedCatalogSearchPage{Indexed: []catalog.CatalogSearchResult{item}}, nil
			}))
			if err != nil {
				t.Fatal(err)
			}
			manager, err := federation.NewManager(artworkReadStore{read: func(_ context.Context, tenant string, requests []federation.ArtworkReadRequest) (map[federation.ArtworkTarget]federation.ArtworkReadContext, error) {
				batches++
				if tenant != projectionTenant || len(requests) != 1 || requests[0].Target != target || requests[0].Live || requests[0].EntryDigest != "selected-evidence" {
					t.Fatalf("artwork resolution lost selected evidence: %s %#v", tenant, requests)
				}
				if tc.batchFailure {
					return nil, catalog.Wrap(catalog.CodeDependencyUnavailable, "artwork unavailable", errors.New("private-fixture-storage"))
				}
				return map[federation.ArtworkTarget]federation.ArtworkReadContext{target: {Source: source, Eligible: !tc.ineligible, Candidates: federation.ArtworkCandidates{Publisher: &descriptor}}}, nil
			}}, publicPolicyFetcher{}, clock.NewFake(projectionTime), id.Generator{})
			if err != nil {
				t.Fatal(err)
			}
			server := projectionServer(nil)
			server.catalogSearch, server.federation, server.artwork = search, manager, media
			server.maxBodyBytes = artworkReadMaximum
			ctx := identityfixture.WithActor(t.Context(), projectionActor(identity.RoleReader))
			body, err := json.Marshal(artworkReadRequest{Origin: "indexed", Target: &target, Slot: tc.slot, Index: tc.index})
			if err != nil {
				t.Fatal(err)
			}
			r := httptest.NewRequestWithContext(ctx, http.MethodPost, "/api/v1/artwork/read", bytes.NewReader(body))
			r.Header.Set("Content-Type", "application/json")
			response := httptest.NewRecorder()
			server.handleArtworkRead(response, r)
			if tc.status == http.StatusOK {
				if response.Code != http.StatusOK || !bytes.Equal(response.Body.Bytes(), fetch.body) || response.Header().Get("Content-Type") != "image/png" {
					t.Fatalf("selected image not delivered: %d %s", response.Code, response.Body.String())
				}
			} else {
				requireProjectionProblem(t, response, tc.status, tc.code)
			}
			if searches != tc.searches || batches != tc.batches || fetch.calls != tc.images || precedenceReads != 0 {
				t.Fatalf("unexpected work: searches=%d batches=%d images=%d precedence=%d", searches, batches, fetch.calls, precedenceReads)
			}
		})
	}
}

func artworkIndex(value int) *int { return &value }

func (s *artworkReceiptStore) GetSource(_ context.Context, tenant, id string) (federation.Source, error) {
	if tenant != s.source.TenantID || id != s.source.ID {
		return federation.Source{}, catalog.E(catalog.CodeNotFound, "source not found")
	}
	return s.source, nil
}
func (s *artworkReceiptStore) GetArtworkOverride(_ context.Context, _ string, _ federation.ArtworkTarget) (federation.ArtworkOverride, error) {
	if s.head != nil {
		return *s.head, nil
	}
	if s.overrideDigest == "" {
		return federation.ArtworkOverride{}, catalog.E(catalog.CodeNotFound, "override not found")
	}
	return federation.ArtworkOverride{Digest: s.overrideDigest}, nil
}

type artworkHistoryStore struct {
	artworkReceiptStore
	history []federation.ArtworkOverride
	limit   int
	reads   int
	after   string
}

func (s *artworkHistoryStore) ListArtworkOverrideHistory(_ context.Context, tenant string, target federation.ArtworkTarget, after string, limit int) ([]federation.ArtworkOverride, error) {
	s.reads++
	s.limit, s.after = limit, after
	if tenant != s.source.TenantID || target != s.history[0].Target {
		return nil, catalog.E(catalog.CodeNotFound, "history not found")
	}
	if after == s.history[0].ID {
		return s.history[1:], nil
	}
	return s.history, nil
}

func TestArtworkAdministrativeReadsPreserveETagAndTargetBoundHistory(t *testing.T) {
	source := federationFixtureSource()
	target := federation.ArtworkTarget{SourceID: source.ID, PresentationGeneration: 1, Identifier: federationFixtureAIR, Kind: catalog.KindAgent}
	first := federation.ArtworkOverride{ID: projectionItem, Target: target, Revision: 1, TenantID: source.TenantID}
	second := federation.ArtworkOverride{ID: projectionNext, Target: target, Revision: 2, TenantID: source.TenantID}
	store := &artworkHistoryStore{artworkReceiptStore: artworkReceiptStore{source: source, head: &second}, history: []federation.ArtworkOverride{first, second}}
	manager, err := federation.NewManager(store, publicPolicyFetcher{}, clock.NewFake(projectionTime), id.Generator{})
	if err != nil {
		t.Fatal(err)
	}
	server := projectionServer(nil)
	server.federation = manager
	ctx := identityfixture.WithActor(t.Context(), projectionActor(identity.RoleTenantAdmin))
	query := url.Values{"identifier": {target.Identifier}, "versionPresent": {"false"}, "kind": {"agent"}, "presentationGeneration": {"1"}}
	read := func(handler http.HandlerFunc) *httptest.ResponseRecorder {
		r := httptest.NewRequestWithContext(ctx, http.MethodGet, "/artwork?"+query.Encode(), nil)
		r.SetPathValue("sourceId", source.ID)
		response := httptest.NewRecorder()
		handler(response, r)
		return response
	}
	response := read(server.handleArtworkOverrideGet)
	if response.Code != http.StatusOK || response.Header().Get("ETag") != `"rev-2"` {
		t.Fatalf("current head lost conditional-write revision: %d %v", response.Code, response.Header())
	}
	query.Set("limit", "1")
	response = read(server.handleArtworkOverrideHistory)
	var page cursorPage[federation.ArtworkOverride]
	if err = json.Unmarshal(response.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if response.Code != http.StatusOK || len(page.Items) != 1 || page.Items[0].ID != first.ID || page.NextCursor == "" || store.limit != 2 {
		t.Fatalf("history lost bounded continuation: %d %#v limit=%d", response.Code, page, store.limit)
	}
	query.Set("cursor", page.NextCursor)
	query.Set("kind", "skill")
	reads := store.reads
	requireProjectionProblem(t, read(server.handleArtworkOverrideHistory), http.StatusBadRequest, catalog.CodeInvalidArgument)
	if store.reads != reads {
		t.Fatal("cursor transferred between artwork identities")
	}
	query.Set("kind", "agent")
	response = read(server.handleArtworkOverrideHistory)
	page = cursorPage[federation.ArtworkOverride]{}
	if err = json.Unmarshal(response.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if response.Code != http.StatusOK || len(page.Items) != 1 || page.Items[0].ID != second.ID || page.NextCursor != "" || store.after != first.ID {
		t.Fatalf("history resumed incorrectly: %d %#v after=%s", response.Code, page, store.after)
	}
	query.Del("cursor")
	query.Set("versionPresent", "TRUE")
	reads = store.reads
	for _, handler := range []http.HandlerFunc{server.handleArtworkOverrideGet, server.handleArtworkOverrideHistory} {
		response := read(handler)
		requireProjectionProblem(t, response, http.StatusBadRequest, catalog.CodeInvalidArgument)
		if response.Header().Get("ETag") != "" || store.reads != reads {
			t.Fatal("invalid artwork identity returned a revision or read history")
		}
	}
	query.Set("versionPresent", "false")
	query.Set("limit", "0")
	requireProjectionProblem(t, read(server.handleArtworkOverrideHistory), http.StatusBadRequest, catalog.CodeInvalidArgument)
	if store.reads != reads {
		t.Fatal("invalid history page reached persistence")
	}
}

func TestLiveArtworkReceiptsBindCurrentAuthorityAndPresentation(t *testing.T) {
	source := federationFixtureSource()
	source.Live.Enabled = true
	source.PresentationGeneration = 1
	store := &artworkReceiptStore{source: source}
	manager, err := federation.NewManager(store, publicPolicyFetcher{}, clock.NewFake(projectionTime), id.Generator{})
	if err != nil {
		t.Fatal(err)
	}
	server := projectionServer(nil)
	server.federation = manager
	server.mediaPolicyDigest = "media-policy-v1"
	ctx := identityfixture.WithActor(t.Context(), projectionActor(identity.RoleTenantAdmin))
	item := catalog.CatalogSearchResult{Identifier: federationFixtureAIR, Kind: catalog.KindAgent, Live: &catalog.LiveSearchEvidence{SourceID: source.ID, SourceRevision: source.Revision, PresentationGeneration: 1, PolicyDigest: source.PolicyDigest, Entry: json.RawMessage(`{"identifier":"urn:air:peer.example:default:fixture"}`)}}
	if token, err := server.signArtworkReceipt(t.Context(), item); token != "" || catalog.ErrorCodeOf(err) != catalog.CodeUnauthenticated {
		t.Fatalf("anonymous caller received image authority: %q %v", token, err)
	}
	broken := item
	brokenLive := *item.Live
	brokenLive.Entry = json.RawMessage(`{"identifier":`)
	broken.Live = &brokenLive
	if token, err := server.signArtworkReceipt(ctx, broken); token != "" || catalog.ErrorCodeOf(err) != catalog.CodeInternal {
		t.Fatalf("invalid evidence received a signed proof: %q %v", token, err)
	}
	oversized := item
	oversizedLive := *item.Live
	oversizedLive.PolicyDigest = strings.Repeat("x", artworkReceiptMaximum)
	oversized.Live = &oversizedLive
	if token, err := server.signArtworkReceipt(ctx, oversized); token != "" || catalog.ErrorCodeOf(err) != catalog.CodeWorkLimitExceeded {
		t.Fatalf("oversized authority escaped the receipt bound: %q %v", token, err)
	}
	token, err := server.signArtworkReceipt(ctx, item)
	if err != nil {
		t.Fatal(err)
	}
	if len(token) > 4096 {
		t.Fatal("receipt over limit")
	}
	if _, err := server.verifyArtworkReceipt(ctx, token); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"", token + "=", token + ".extra", strings.Repeat("x", 4097), "not-base64.signature", "administration-cursor-v1." + token, token[:len(token)-2] + "aa"} {
		if _, err := server.verifyArtworkReceipt(ctx, bad); err == nil {
			t.Fatalf("invalid token accepted: %q", bad)
		}
	}
	parts := strings.Split(token, ".")
	for _, bad := range []string{parts[0] + "\n." + parts[1], parts[0] + "." + parts[1] + "\n"} {
		// Go's base64 decoder ignores line breaks. The receipt contract also
		// requires canonical encoding, so these equivalent byte strings fail.
		if _, err := server.verifyArtworkReceipt(ctx, bad); catalog.ErrorCodeOf(err) != catalog.CodePreconditionFailed {
			t.Fatal("noncanonical receipt text was accepted", err)
		}
	}
	for name, mutate := range map[string]func(){"source revision": func() { store.source.Revision++ }, "generation": func() { store.source.PresentationGeneration++ }, "policy": func() { store.source.PolicyDigest = "changed" }, "credential": func() { store.source.CredentialRevision = "changed" }, "paused": func() { store.source.Status = federation.SourcePaused }, "live disabled": func() { store.source.Live.Enabled = false }, "override changed": func() { store.overrideDigest = "new-override" }, "media policy": func() { server.mediaPolicyDigest = "changed" }, "expired": func() { server.now = func() time.Time { return projectionTime.Add(5 * time.Minute) } }} {
		t.Run(name, func(t *testing.T) {
			store.source = source
			store.overrideDigest = ""
			server.mediaPolicyDigest = "media-policy-v1"
			server.now = func() time.Time { return projectionTime }
			mutate()
			if _, err := server.verifyArtworkReceipt(ctx, token); catalog.ErrorCodeOf(err) != catalog.CodePreconditionFailed {
				t.Fatalf("stale proof accepted: %v", err)
			}
		})
	}
	store.source = source
	store.overrideDigest = ""
	server.mediaPolicyDigest = "media-policy-v1"
	server.now = func() time.Time { return projectionTime }
	other := projectionActor(identity.RoleTenantAdmin)
	other.Subject = "another user"
	if _, err := server.verifyArtworkReceipt(identityfixture.WithActor(t.Context(), other), token); err == nil {
		t.Fatal("receipt transferred between principals")
	}
	other.TenantID = projectionNext
	if _, err := server.verifyArtworkReceipt(identityfixture.WithActor(t.Context(), other), token); err == nil {
		t.Fatal("receipt crossed tenant")
	}
}

func TestLiveArtworkReadBindsBytesToSignedDescriptor(t *testing.T) {
	source := federationFixtureSource()
	source.Live.Enabled = true
	source.PresentationGeneration = 1
	store := &artworkReceiptStore{source: source}
	manager, err := federation.NewManager(store, publicPolicyFetcher{}, clock.NewFake(projectionTime), id.Generator{})
	if err != nil {
		t.Fatal(err)
	}
	fetch := &artworkHTTPFetcher{body: artworkHTTPImage(t)}
	media, err := artwork.New(fetch, artwork.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	descriptor, err := media.Seal(t.Context(), source.TenantID, artworkHTTPInput())
	if err != nil {
		t.Fatal(err)
	}
	server := projectionServer(nil)
	server.artwork, server.federation = media, manager
	server.maxBodyBytes = artworkReadMaximum
	ctx := identityfixture.WithActor(t.Context(), projectionActor(identity.RoleTenantAdmin))
	item := catalog.CatalogSearchResult{Identifier: federationFixtureAIR, Kind: catalog.KindAgent,
		Artwork: &artwork.Presentation{Descriptor: &descriptor, IconSource: artwork.IconSourcePublisher},
		Live:    &catalog.LiveSearchEvidence{SourceID: source.ID, SourceRevision: source.Revision, PresentationGeneration: 1, PolicyDigest: source.PolicyDigest, Entry: json.RawMessage(`{"identifier":"urn:air:peer.example:default:fixture"}`)}}
	token, err := server.signArtworkReceipt(ctx, item)
	if err != nil {
		t.Fatal(err)
	}
	read := func(value artwork.Descriptor) *httptest.ResponseRecorder {
		t.Helper()
		body, err := json.Marshal(artworkReadRequest{Origin: "live", Receipt: token, Descriptor: &value, Slot: "icon"})
		if err != nil {
			t.Fatal(err)
		}
		r := httptest.NewRequestWithContext(ctx, http.MethodPost, "/api/v1/artwork/read", bytes.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		server.handleArtworkRead(response, r)
		return response
	}
	if response := read(descriptor); response.Code != http.StatusOK || !bytes.Equal(response.Body.Bytes(), fetch.body) {
		t.Fatalf("signed image read failed: %d", response.Code)
	}
	calls := fetch.calls
	changed := descriptor
	icon := *descriptor.Icon
	icon.URL = "https://images.example/substituted.png"
	changed.Icon = &icon
	requireProjectionProblem(t, read(changed), http.StatusPreconditionFailed, catalog.CodePreconditionFailed)
	store.source.Revision++
	requireProjectionProblem(t, read(descriptor), http.StatusPreconditionFailed, catalog.CodePreconditionFailed)
	if fetch.calls != calls {
		t.Fatal("substituted descriptor or stale source reached image fetcher")
	}
}

func TestArtworkQueryTargetKeepsVersionPresenceAndGeneration(t *testing.T) {
	query := url.Values{"identifier": {federationFixtureAIR}, "versionPresent": {"true"}, "version": {"1.0"}, "kind": {"agent"}, "presentationGeneration": {"7"}}
	parse := func(values url.Values) (federation.ArtworkTarget, error) {
		r := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/artwork?"+values.Encode(), nil)
		r.SetPathValue("sourceId", projectionResource)
		return artworkQueryTarget(r)
	}
	target, err := parse(query)
	if err != nil || target.SourceID != projectionResource || target.Identifier != federationFixtureAIR || target.PresentationGeneration != 7 || !target.VersionPresent || target.Version != "1.0" {
		t.Fatalf("target lost its binding: %#v %v", target, err)
	}
	for name, replacement := range map[string]string{"presentationGeneration": "9223372036854775808", "kind": "unknown", "identifier": "not-an-air", "versionPresent": "TRUE"} {
		t.Run(name, func(t *testing.T) {
			values := query.Clone()
			values.Set(name, replacement)
			if _, err := parse(values); catalog.ErrorCodeOf(err) != catalog.CodeInvalidArgument {
				t.Fatalf("invalid target accepted: %v", err)
			}
		})
	}
	query.Set("versionPresent", "false")
	query.Set("version", "")
	if _, err := parse(query); catalog.ErrorCodeOf(err) != catalog.CodeInvalidArgument {
		t.Fatalf("explicit version member accepted for absent version: %v", err)
	}
	query.Del("version")
	if target, err := parse(query); err != nil || target.VersionPresent || target.Version != "" {
		t.Fatalf("absent version rejected: %#v %v", target, err)
	}
}

type artworkSetStore struct {
	artworkReceiptStore
	failure        error
	saved          federation.ArtworkOverride
	expected       int64
	sourceRevision int64
	writes         int
}

func (s *artworkSetStore) SaveArtworkOverride(_ context.Context, value federation.ArtworkOverride, expected, sourceRevision int64, _ federation.MutationEvidence) (federation.ArtworkOverride, error) {
	if s.failure != nil {
		return federation.ArtworkOverride{}, s.failure
	}
	s.saved, s.expected, s.sourceRevision = value, expected, sourceRevision
	s.writes++
	return value, nil
}

// This is a handler projection test with scripted persistence. Atomic audit,
// command replay and SQL behavior belong to the functional/integration suites.
func TestArtworkOverrideAdmissionRejectsInvalidCommandsBeforeImageContact(t *testing.T) {
	source := federationFixtureSource()
	source.Live.Enabled = true
	source.PresentationGeneration = 1
	store := &artworkSetStore{artworkReceiptStore: artworkReceiptStore{source: source}}
	manager, err := federation.NewManager(store, publicPolicyFetcher{}, clock.NewFake(projectionTime), id.Generator{})
	if err != nil {
		t.Fatal(err)
	}
	fetch := &artworkHTTPFetcher{body: artworkHTTPImage(t)}
	media, err := artwork.New(fetch, artwork.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	server := projectionServer(nil)
	server.artwork, server.federation = media, manager
	server.maxBodyBytes = artworkReadMaximum
	ctx := identityfixture.WithActor(t.Context(), projectionActor(identity.RoleTenantAdmin))
	target := federation.ArtworkTarget{SourceID: source.ID, PresentationGeneration: 1, Identifier: federationFixtureAIR, Kind: catalog.KindAgent}
	item := catalog.CatalogSearchResult{Identifier: target.Identifier, Kind: target.Kind, Live: &catalog.LiveSearchEvidence{SourceID: source.ID, SourceRevision: source.Revision, PresentationGeneration: 1, PolicyDigest: source.PolicyDigest, Entry: json.RawMessage(`{"identifier":"urn:air:peer.example:default:fixture"}`)}}
	receipt, err := server.signArtworkReceipt(ctx, item)
	if err != nil {
		t.Fatal(err)
	}
	input := artworkOverrideRequest{Target: target, Reason: "Hide publisher artwork", Receipt: receipt}
	input.Icon.Mode, input.Screenshots.Mode = federation.ArtworkSuppress, federation.ArtworkInherit
	invoke := func(value artworkOverrideRequest, etag string) *httptest.ResponseRecorder {
		t.Helper()
		raw, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		r := httptest.NewRequestWithContext(ctx, http.MethodPost, "/artwork", bytes.NewReader(raw))
		r.SetPathValue("sourceId", source.ID)
		r.Header.Set("Content-Type", "application/json")
		if etag != "" {
			r.Header.Set("If-Match", etag)
		}
		response := httptest.NewRecorder()
		server.handleArtworkOverrideSet(response, r)
		return response
	}
	response := invoke(input, "")
	if response.Code != http.StatusOK || store.writes != 1 || store.expected != 0 || store.sourceRevision != source.Revision || store.saved.Target != target || store.saved.Reason != input.Reason || store.saved.Icon.Mode != federation.ArtworkSuppress || store.saved.Revision != 1 {
		t.Fatalf("valid suppression lost command binding: %d %#v", response.Code, store.saved)
	}
	invalidReceipt := input
	invalidReceipt.Receipt += ".changed"
	requireProjectionProblem(t, invoke(invalidReceipt, ""), http.StatusPreconditionFailed, catalog.CodePreconditionFailed)
	adminContext := ctx
	ctx = identityfixture.WithActor(t.Context(), projectionActor(identity.RoleReader))
	requireProjectionProblem(t, invoke(input, ""), http.StatusForbidden, catalog.CodeForbidden)
	ctx = adminContext
	store.failure = catalog.E(catalog.CodeConflict, "owned persistence conflict")
	response = invoke(input, "")
	requireProjectionProblem(t, response, http.StatusConflict, catalog.CodeConflict)
	if store.writes != 1 || fetch.calls != 0 || response.Header().Get("ETag") != "" {
		t.Fatal("failed persistence reported a successful revision or fetched an image")
	}
	store.failure = nil
	for name, change := range map[string]func(*artworkOverrideRequest){
		"wrong source":        func(value *artworkOverrideRequest) { value.Target.SourceID = projectionNext },
		"empty reason":        func(value *artworkOverrideRequest) { value.Reason = "" },
		"unknown mode":        func(value *artworkOverrideRequest) { value.Icon.Mode = "unknown" },
		"missing icon":        func(value *artworkOverrideRequest) { value.Icon.Mode = federation.ArtworkReplace },
		"missing screenshots": func(value *artworkOverrideRequest) { value.Screenshots.Mode = federation.ArtworkReplace },
		"unexpected asset":    func(value *artworkOverrideRequest) { value.Icon.Asset = artworkHTTPInput().Icon },
	} {
		t.Run(name, func(t *testing.T) {
			value := input
			change(&value)
			requireProjectionProblem(t, invoke(value, ""), http.StatusBadRequest, catalog.CodeInvalidArgument)
		})
	}
	requireProjectionProblem(t, invoke(input, `"not-a-revision"`), http.StatusPreconditionFailed, catalog.CodePreconditionFailed)
	store.head = &federation.ArtworkOverride{Revision: 1}
	requireProjectionProblem(t, invoke(input, ""), http.StatusPreconditionFailed, catalog.CodePreconditionFailed)
	if fetch.calls != 0 || store.writes != 1 {
		t.Fatalf("invalid or media-free command performed extra work: images=%d writes=%d", fetch.calls, store.writes)
	}
	store.head = nil
	changedTarget := input
	changedTarget.Target.VersionPresent, changedTarget.Target.Version = true, "1.0"
	requireProjectionProblem(t, invoke(changedTarget, ""), http.StatusPreconditionFailed, catalog.CodePreconditionFailed)
	replacement := input
	replacement.Icon.Mode, replacement.Icon.Asset = federation.ArtworkReplace, artworkHTTPInput().Icon
	fetch.err = errors.New("private-fixture image credential failure")
	requireProjectionProblem(t, invoke(replacement, ""), http.StatusServiceUnavailable, catalog.CodeDependencyUnavailable)
	fetch.err = nil
	fetch.body = []byte("invalid image bytes")
	requireProjectionProblem(t, invoke(replacement, ""), http.StatusBadRequest, catalog.CodeInvalidArgument)
	server.artwork = nil
	requireProjectionProblem(t, invoke(replacement, ""), http.StatusServiceUnavailable, catalog.CodeDependencyUnavailable)
	if fetch.calls != 2 || store.writes != 1 {
		t.Fatalf("failed validation saved a revision or repeated image requests: images=%d writes=%d", fetch.calls, store.writes)
	}
}
