package catalog

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"image"
	"image/color"
	"image/png"
	"testing"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/artwork"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
)

type listingImageFetcher struct {
	calls  int
	body   []byte
	err    error
	tenant string
}

func (f *listingImageFetcher) Fetch(_ context.Context, request artwork.FetchRequest) (artwork.FetchResult, error) {
	f.calls++
	f.tenant = request.TenantID
	return artwork.FetchResult{Body: f.body, MediaType: "image/png"}, f.err
}
func listingImage(t *testing.T) []byte {
	t.Helper()
	var data bytes.Buffer
	image := image.NewNRGBA(image.Rect(0, 0, 2, 2))
	image.Set(0, 0, color.NRGBA{R: 255, A: 255})
	if err := png.Encode(&data, image); err != nil {
		t.Fatal(err)
	}
	return data.Bytes()
}
func listingImageInput() artwork.Input {
	return artwork.Input{SchemaVersion: 1, Icon: &artwork.AssetInput{URL: "https://images.example/icon.png", Alt: "Owned test icon", Rights: &artwork.Rights{Basis: "owned", Attribution: "Catalog test fixture"}}}
}

func TestListingArtworkSealsWithoutChangingInputOrOpaqueExtensions(t *testing.T) {
	fetch := &listingImageFetcher{body: listingImage(t)}
	media, err := artwork.New(fetch, artwork.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	service := &Service{artwork: media}
	input := map[string]any{artwork.IRI: listingImageInput(), "https://example/opaque": map[string]any{"large": json.Number("9007199254740993")}}
	before, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := service.sealListingArtwork(t.Context(), "tenant", input)
	if err != nil {
		t.Fatal(err)
	}
	after, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) || fetch.calls != 1 || fetch.tenant != "tenant" {
		t.Fatalf("input mutation or lost retrieval binding: calls=%d tenant=%s", fetch.calls, fetch.tenant)
	}
	presentation := ListingArtwork(sealed)
	if presentation == nil || presentation.Descriptor.Icon.Width != 2 || presentation.Descriptor.Icon.Height != 2 || presentation.Descriptor.Icon.Size != int64(len(fetch.body)) || presentation.Descriptor.Icon.Digest == "" || presentation.Descriptor.Icon.RightsAssessment != artwork.NotAssessed {
		t.Fatalf("missing sealed facts: %#v", presentation)
	}
	if err := ValidateListingRevisionDigest(ListingRevision{}); err == nil {
		t.Fatal("empty digest accepted")
	}
	revision := ListingRevision{Extensions: sealed}
	revision.Digest, err = digestJSON(revisionDigestInput(revision))
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateListingRevisionDigest(revision); err != nil {
		t.Fatal(err)
	}
	sealed[artwork.IRI] = map[string]any{"schemaVersion": 1}
	if err := ValidateListingRevisionDigest(revision); err == nil {
		t.Fatal("artwork not bound into revision digest")
	}
}

func TestListingArtworkRejectsForgedFactsAndUnavailableImages(t *testing.T) {
	fetch := &listingImageFetcher{body: listingImage(t)}
	media, err := artwork.New(fetch, artwork.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	service := &Service{artwork: media}
	sealed, err := service.sealListingArtwork(t.Context(), "tenant", map[string]any{artwork.IRI: listingImageInput()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.sealListingArtwork(t.Context(), "tenant", sealed); ErrorCodeOf(err) != CodeInvalidArgument || fetch.calls != 1 {
		t.Fatalf("client facts accepted/refetched: %v", err)
	}
	fetch.err = errors.New("private URL and secret dependency cause")
	if _, err := service.sealListingArtwork(t.Context(), "tenant", map[string]any{artwork.IRI: listingImageInput()}); ErrorCodeOf(err) != CodeDependencyUnavailable || err.Error() == fetch.err.Error() {
		t.Fatalf("unsafe failure: %v", err)
	}
	fetch.err = nil
	fetch.body = []byte("not a supported raster image")
	if sealed, err := service.sealListingArtwork(t.Context(), "tenant", map[string]any{artwork.IRI: listingImageInput()}); ErrorCodeOf(err) != CodeInvalidArgument || sealed != nil {
		t.Fatalf("invalid image was sealed or misclassified: %#v %v", sealed, err)
	}
	if _, err := (&Service{}).sealListingArtwork(t.Context(), "tenant", map[string]any{artwork.IRI: listingImageInput()}); ErrorCodeOf(err) != CodeDependencyUnavailable {
		t.Fatal(err)
	}
	for _, value := range []any{nil, "invalid", map[string]any{"schemaVersion": 99}, map[string]any{"schemaVersion": 1, "icon": map[string]any{"url": "https://images.example/icon.png"}}} {
		if ListingArtwork(map[string]any{artwork.IRI: value}) != nil {
			t.Fatalf("unrecognized artwork accepted: %#v", value)
		}
	}
	if ListingArtwork(nil) != nil {
		t.Fatal("absent artwork manufactured")
	}
}

func TestHistoricalListingArtworkRequiresContentAuthorityAndPreservesReadFailures(t *testing.T) {
	f, repo := lifecycleFailureFixture(t, "revise listing")
	repo.listingRevision.ListingID = repo.listing.ID
	media, err := artwork.New(&listingImageFetcher{body: listingImage(t)}, artwork.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	f.service.artwork = media
	repo.listingRevision.Extensions, err = f.service.sealListingArtwork(t.Context(), guardrailTenantID, map[string]any{artwork.IRI: listingImageInput()})
	if err != nil {
		t.Fatal(err)
	}
	for _, phase := range []string{"listing revision", "listing"} {
		t.Run(phase, func(t *testing.T) {
			repo.fail = phase
			repo.reads = nil
			got, err := f.service.ReadListingArtwork(f.adminContext(), repo.listingRevision.ID)
			if !errors.Is(err, repo.err) || got != nil {
				t.Fatalf("read failure lost or returned content: %#v %v", got, err)
			}
			if repo.reads[len(repo.reads)-1] != phase {
				t.Fatalf("read continued past failure: %v", repo.reads)
			}
		})
	}
	repo.fail = ""
	f.reader.Roles = nil
	if got, err := f.service.ReadListingArtwork(f.readerContext(), repo.listingRevision.ID); ErrorCodeOf(err) != CodeForbidden || got != nil {
		t.Fatalf("member without content authority received unpublished artwork: %#v %v", got, err)
	}
	// The Reader template deliberately includes retained-content access.
	f.reader.Roles = []identity.Role{identity.RoleReader}
	got, err := f.service.ReadListingArtwork(f.readerContext(), repo.listingRevision.ID)
	if err != nil || got == nil || got.Descriptor == nil || got.Descriptor.Icon == nil || got.Descriptor.Icon.URL != listingImageInput().Icon.URL {
		t.Fatalf("content reader could not read retained artwork: %#v %v", got, err)
	}
	if len(repo.writes) != 0 {
		t.Fatal("artwork read changed retained state")
	}
}
