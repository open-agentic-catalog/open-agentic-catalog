package httpserver

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/artwork"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/audit"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/federation"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/jsondoc"
)

const artworkReadMaximum = 40 << 10
const artworkReceiptMaximum = 4096
const artworkReceiptPurpose = "catalog-live-artwork-v1"

type artworkReceipt struct {
	Purpose            string                   `json:"purpose"`
	Tenant             string                   `json:"tenant"`
	Principal          string                   `json:"principal"`
	Authorization      string                   `json:"authorization"`
	Target             federation.ArtworkTarget `json:"target"`
	SourceRevision     int64                    `json:"sourceRevision"`
	SourcePolicy       string                   `json:"sourcePolicy"`
	CredentialRevision string                   `json:"credentialRevision"`
	MediaPolicy        string                   `json:"mediaPolicy"`
	EntryDigest        string                   `json:"entryDigest"`
	DescriptorDigest   string                   `json:"descriptorDigest"`
	MutableIcon        bool                     `json:"mutableIcon"`
	IconSource         string                   `json:"iconSource"`
	ScreenshotsSource  string                   `json:"screenshotsSource"`
	OverrideDigest     string                   `json:"overrideDigest"`
	Expires            int64                    `json:"expires"`
}

func artworkDigest(value any) string {
	raw, err := json.Marshal(value)
	if err != nil {
		return ""
	}
	digest := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(digest[:])
}

func (s *Server) signArtworkReceipt(ctx context.Context, item catalog.CatalogSearchResult) (string, error) {
	actor, ok := identity.FromContext(ctx)
	if !ok || item.Live == nil {
		return "", catalog.E(catalog.CodeUnauthenticated, "artwork authentication is required")
	}
	authority, err := identity.CurrentDecisionDigest(ctx)
	if err != nil {
		return "", catalog.PermissionError("current artwork authority is required", err)
	}
	live := item.Live
	proof := artworkReceipt{Purpose: artworkReceiptPurpose, Tenant: actor.TenantID, Principal: actor.CommandPrincipal(), Authorization: authority,
		Target:         federation.ArtworkTarget{SourceID: live.SourceID, PresentationGeneration: live.PresentationGeneration, Identifier: item.Identifier, VersionPresent: item.VersionPresent, Version: item.Version, Kind: item.Kind},
		SourceRevision: live.SourceRevision, SourcePolicy: live.PolicyDigest, CredentialRevision: live.CredentialRevision, MediaPolicy: s.mediaPolicyDigest,
		EntryDigest: artworkDigest(live.Entry), DescriptorDigest: artworkDigest((*artwork.Descriptor)(nil)), OverrideDigest: live.ArtworkOverrideDigest, Expires: s.now().Add(5 * time.Minute).Unix()}
	if item.Artwork != nil {
		proof.DescriptorDigest = artworkDigest(item.Artwork.Descriptor)
		proof.MutableIcon = item.Artwork.MutableIcon
		proof.IconSource = item.Artwork.IconSource
		proof.ScreenshotsSource = item.Artwork.ScreenshotsSource
	}
	if proof.EntryDigest == "" || proof.DescriptorDigest == "" {
		return "", catalog.E(catalog.CodeInternal, "artwork evidence could not be encoded")
	}
	raw, err := json.Marshal(proof)
	if err != nil {
		return "", err
	}
	payload := base64.RawURLEncoding.EncodeToString(raw)
	mac := hmac.New(sha256.New, s.cursorKey)
	_, _ = mac.Write([]byte(artworkReceiptPurpose + "\x00" + payload))
	token := payload + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	if len(token) > artworkReceiptMaximum {
		return "", catalog.E(catalog.CodeWorkLimitExceeded, "artwork receipt exceeds its bound")
	}
	return token, nil
}

func (s *Server) verifyArtworkReceipt(ctx context.Context, token string) (artworkReceipt, error) {
	invalid := func() (artworkReceipt, error) {
		return artworkReceipt{}, catalog.E(catalog.CodePreconditionFailed, "live artwork authority expired or changed; search again")
	}
	if len(token) == 0 || len(token) > artworkReceiptMaximum {
		return invalid()
	}
	payload, signature, found := strings.Cut(token, ".")
	if !found {
		return invalid()
	}
	raw, err := base64.RawURLEncoding.Strict().DecodeString(payload)
	if err != nil || base64.RawURLEncoding.EncodeToString(raw) != payload {
		return invalid()
	}
	sig, err := base64.RawURLEncoding.Strict().DecodeString(signature)
	if err != nil || len(sig) != sha256.Size || base64.RawURLEncoding.EncodeToString(sig) != signature {
		return invalid()
	}
	mac := hmac.New(sha256.New, s.cursorKey)
	_, _ = mac.Write([]byte(artworkReceiptPurpose + "\x00" + payload))
	if !hmac.Equal(sig, mac.Sum(nil)) {
		return invalid()
	}
	var proof artworkReceipt
	if jsondoc.Decode(raw, &proof) != nil || proof.Purpose != artworkReceiptPurpose || proof.Expires <= s.now().Unix() || proof.Expires > s.now().Add(5*time.Minute).Unix() || proof.MediaPolicy != s.mediaPolicyDigest || federation.ValidateArtworkTarget(proof.Target) != nil {
		return invalid()
	}
	if proof.IconSource != "" && !artwork.ValidIconSource(proof.IconSource) || proof.ScreenshotsSource != "" && !artwork.ValidScreenshotsSource(proof.ScreenshotsSource) || proof.MutableIcon != (proof.IconSource == artwork.IconSourceNative) {
		return invalid()
	}
	actor, ok := identity.FromContext(ctx)
	authority, err := identity.CurrentDecisionDigest(ctx)
	if !ok || err != nil || proof.Tenant != actor.TenantID || proof.Principal != actor.CommandPrincipal() || proof.Authorization != authority {
		return invalid()
	}
	source, err := s.federation.ArtworkSourceContext(ctx, proof.Target, proof.SourceRevision, true)
	if err != nil || source.PolicyDigest != proof.SourcePolicy || source.CredentialRevision != proof.CredentialRevision {
		return invalid()
	}
	if err := s.federation.ValidateLiveArtworkOverride(ctx, proof.Target, proof.OverrideDigest); err != nil {
		return invalid()
	}
	return proof, nil
}

// resolveArtwork is applied after ranking and paging. It performs no image I/O.
func (s *Server) resolveArtwork(ctx context.Context, items []catalog.CatalogSearchResult) ([]catalog.CatalogSearchResult, error) {
	if s.artwork == nil {
		return items, nil
	}
	external := false
	for _, item := range items {
		external = external || item.Live != nil || item.Indexed != nil
	}
	if !external {
		return items, nil
	}
	resolved, err := s.federation.ResolveArtwork(ctx, items)
	if err != nil {
		return nil, err
	}
	for index := range resolved {
		item := &resolved[index]
		if item.Live == nil || !item.Live.ArtworkAdmitted {
			continue
		}
		token, err := s.signArtworkReceipt(ctx, *item)
		if err != nil {
			return nil, err
		}
		if item.Artwork == nil {
			item.Artwork = &artwork.Presentation{}
		}
		item.Artwork.Receipt = token
	}
	return resolved, nil
}

type artworkReadRequest struct {
	Origin            string                    `json:"origin"`
	ListingRevisionID string                    `json:"listingRevisionId,omitempty"`
	Target            *federation.ArtworkTarget `json:"target,omitempty"`
	Receipt           string                    `json:"receipt,omitempty"`
	Descriptor        *artwork.Descriptor       `json:"descriptor,omitempty"`
	Slot              string                    `json:"slot"`
	Index             *int                      `json:"index,omitempty"`
}

func (s *Server) indexedArtwork(ctx context.Context, target federation.ArtworkTarget) (catalog.CatalogSearchResult, error) {
	item, err := s.catalogSearch.GetIndexed(ctx, catalog.IndexedIdentity{Identifier: target.Identifier, VersionPresent: target.VersionPresent, Version: target.Version})
	if err != nil {
		return item, err
	}
	if item.Indexed == nil || item.Kind != target.Kind || item.Indexed.PresentationSourceID != target.SourceID || item.Indexed.PresentationGeneration != target.PresentationGeneration {
		return item, catalog.E(catalog.CodePreconditionFailed, "indexed artwork selection changed; refresh the resource")
	}
	items, err := s.federation.ResolveArtwork(ctx, []catalog.CatalogSearchResult{item})
	if err != nil {
		return item, err
	}
	return items[0], nil
}

func (s *Server) handleArtworkRead(w http.ResponseWriter, r *http.Request) {
	var input artworkReadRequest
	if err := decodeJSON(w, r, min(s.maxBodyBytes, artworkReadMaximum), &input); err != nil {
		s.fail(w, r, err)
		return
	}
	var presentation *artwork.Presentation
	var err error
	if input.Slot != "icon" && input.Slot != "screenshot" || input.Slot == "icon" && input.Index != nil || input.Slot == "screenshot" && (input.Index == nil || *input.Index < 0 || *input.Index >= 8) {
		s.fail(w, r, catalog.E(catalog.CodeInvalidArgument, "artwork slot is invalid"))
		return
	}
	switch input.Origin {
	case "local":
		if !identity.ValidTenantID(input.ListingRevisionID) || input.Target != nil || input.Receipt != "" || input.Descriptor != nil {
			err = catalog.E(catalog.CodeInvalidArgument, "local artwork target is invalid")
			break
		}
		presentation, err = s.catalog.ReadListingArtwork(r.Context(), input.ListingRevisionID)
	case "indexed":
		if input.Target == nil || input.ListingRevisionID != "" || input.Receipt != "" || input.Descriptor != nil || federation.ValidateArtworkTarget(*input.Target) != nil {
			err = catalog.E(catalog.CodeInvalidArgument, "indexed artwork target is invalid")
			break
		}
		var item catalog.CatalogSearchResult
		item, err = s.indexedArtwork(r.Context(), *input.Target)
		presentation = item.Artwork
	case "live":
		if input.Receipt == "" || input.Target != nil || input.ListingRevisionID != "" || input.Descriptor == nil {
			err = catalog.E(catalog.CodeInvalidArgument, "live artwork target is invalid")
			break
		}
		var proof artworkReceipt
		proof, err = s.verifyArtworkReceipt(r.Context(), input.Receipt)
		if err != nil {
			break
		}
		presentation = &artwork.Presentation{Descriptor: input.Descriptor, MutableIcon: proof.MutableIcon, IconSource: proof.IconSource, ScreenshotsSource: proof.ScreenshotsSource}
		if artworkDigest(input.Descriptor) != proof.DescriptorDigest {
			err = catalog.E(catalog.CodePreconditionFailed, "live artwork descriptor changed")
		}
	default:
		err = catalog.E(catalog.CodeInvalidArgument, "artwork origin is invalid")
	}
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if presentation == nil || presentation.Descriptor == nil || s.artwork == nil {
		s.fail(w, r, catalog.E(catalog.CodeNotFound, "artwork is unavailable"))
		return
	}
	descriptor := presentation.Descriptor
	var asset *artwork.Asset
	if input.Slot == "icon" {
		asset = descriptor.Icon
	} else if *input.Index < len(descriptor.Screenshots) {
		asset = &descriptor.Screenshots[*input.Index]
	}
	if asset == nil {
		s.fail(w, r, catalog.E(catalog.CodeNotFound, "artwork slot is unavailable"))
		return
	}
	actor, _ := identity.FromContext(r.Context())
	delivery, err := s.artwork.Deliver(r.Context(), actor.TenantID, *asset, input.Slot == "icon", input.Slot == "icon" && presentation.MutableIcon)
	if err != nil {
		s.fail(w, r, catalog.E(catalog.CodeDependencyUnavailable, "artwork could not be safely delivered"))
		return
	}
	w.Header().Set("Content-Type", delivery.MediaType)
	digest, _ := hex.DecodeString(strings.TrimPrefix(delivery.Digest, "sha256:"))
	w.Header().Set("Content-Digest", "sha-256=:"+base64.StdEncoding.EncodeToString(digest)+":")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(delivery.Body)
}

type artworkOverrideRequest struct {
	Target federation.ArtworkTarget `json:"target"`
	Icon   struct {
		Mode  federation.ArtworkMode `json:"mode"`
		Asset *artwork.AssetInput    `json:"asset,omitempty"`
	} `json:"icon"`
	Screenshots struct {
		Mode   federation.ArtworkMode `json:"mode"`
		Assets []artwork.AssetInput   `json:"assets,omitempty"`
	} `json:"screenshots"`
	Reason  string `json:"reason"`
	Receipt string `json:"receipt,omitempty"`
}

func artworkQueryTarget(r *http.Request) (federation.ArtworkTarget, error) {
	q := r.URL.Query()
	generation, err := strconv.ParseInt(q.Get("presentationGeneration"), 10, 64)
	target := federation.ArtworkTarget{SourceID: r.PathValue("sourceId"), Identifier: q.Get("identifier"), VersionPresent: q.Get("versionPresent") == "true", Version: q.Get("version"), Kind: catalog.Kind(q.Get("kind")), PresentationGeneration: generation}
	if err != nil || q.Get("versionPresent") != "true" && q.Get("versionPresent") != "false" || !target.VersionPresent && q.Has("version") || federation.ValidateArtworkTarget(target) != nil {
		return target, catalog.E(catalog.CodeInvalidArgument, "artwork target is invalid")
	}
	return target, nil
}

func (s *Server) handleArtworkOverrideGet(w http.ResponseWriter, r *http.Request) {
	target, err := artworkQueryTarget(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	value, err := s.federation.GetArtworkOverride(r.Context(), target)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	setETag(w, value.Revision)
	s.respondJSON(w, r, http.StatusOK, value)
}

func (s *Server) handleArtworkOverrideHistory(w http.ResponseWriter, r *http.Request) {
	target, err := artworkQueryTarget(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	filters := map[string]string{}
	for _, key := range []string{"identifier", "versionPresent", "version", "kind", "presentationGeneration"} {
		filters[key] = r.URL.Query().Get(key)
	}
	pageRequest, err := s.parseAdminPage(r, "source-artwork-history", target.SourceID, "id", filters)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	after := ""
	if len(pageRequest.Position) > 0 {
		if len(pageRequest.Position) != 1 {
			s.fail(w, r, catalog.E(catalog.CodeInvalidArgument, "cursor is invalid"))
			return
		}
		after = pageRequest.Position[0]
	}
	items, err := s.federation.ArtworkOverrideHistory(r.Context(), target, after, pageRequest.Limit)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	page, err := finishCursorPage(s, pageRequest, items, func(value federation.ArtworkOverride) []string { return []string{value.ID} })
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.respondJSON(w, r, http.StatusOK, page)
}

func (s *Server) handleArtworkOverrideSet(w http.ResponseWriter, r *http.Request) {
	var input artworkOverrideRequest
	if err := decodeJSON(w, r, min(s.maxBodyBytes, artworkReadMaximum), &input); err != nil {
		s.fail(w, r, err)
		return
	}
	if input.Target.SourceID != r.PathValue("sourceId") || federation.ValidateArtworkTarget(input.Target) != nil {
		s.fail(w, r, catalog.E(catalog.CodeInvalidArgument, "artwork target is invalid"))
		return
	}
	// Authorize and fence selection before any outbound image request.
	if _, err := s.federation.GetSource(r.Context(), input.Target.SourceID); err != nil {
		s.fail(w, r, err)
		return
	}
	if _, err := identity.RequirePermission(r.Context(), "source.manage", ""); err != nil {
		s.fail(w, r, catalog.PermissionError("source artwork authority is required", err))
		return
	}
	var sourceRevision int64
	if input.Receipt != "" {
		proof, err := s.verifyArtworkReceipt(r.Context(), input.Receipt)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		if proof.Target != input.Target {
			s.fail(w, r, catalog.E(catalog.CodePreconditionFailed, "live artwork target changed"))
			return
		}
		sourceRevision = proof.SourceRevision
	} else {
		source, err := s.federation.AdminArtworkSourceContext(r.Context(), input.Target)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		sourceRevision = source.Revision
	}
	expected := int64(0)
	if r.Header.Get("If-Match") != "" {
		var err error
		expected, err = expectedRevision(r)
		if err != nil {
			s.fail(w, r, err)
			return
		}
	}
	previous, readErr := s.federation.GetArtworkOverride(r.Context(), input.Target)
	if readErr != nil && catalog.ErrorCodeOf(readErr) != catalog.CodeNotFound {
		s.fail(w, r, readErr)
		return
	}
	if previous.Revision != expected {
		s.fail(w, r, catalog.E(catalog.CodePreconditionFailed, "artwork revision does not match"))
		return
	}
	if _, err := audit.NormalizeReason(audit.ActionSourceArtworkRevised, input.Reason); err != nil {
		s.fail(w, r, catalog.E(catalog.CodeInvalidArgument, "artwork reason is invalid"))
		return
	}
	validMode := func(mode federation.ArtworkMode) bool {
		return mode == federation.ArtworkInherit || mode == federation.ArtworkReplace || mode == federation.ArtworkSuppress
	}
	if !validMode(input.Icon.Mode) || !validMode(input.Screenshots.Mode) || input.Icon.Mode != federation.ArtworkReplace && input.Icon.Asset != nil || input.Screenshots.Mode != federation.ArtworkReplace && len(input.Screenshots.Assets) > 0 || input.Icon.Mode == federation.ArtworkReplace && input.Icon.Asset == nil || input.Screenshots.Mode == federation.ArtworkReplace && len(input.Screenshots.Assets) == 0 {
		s.fail(w, r, catalog.E(catalog.CodeInvalidArgument, "artwork selection is invalid"))
		return
	}
	if s.artwork == nil {
		s.fail(w, r, catalog.E(catalog.CodeDependencyUnavailable, "artwork verification is unavailable"))
		return
	}
	descriptorInput := artwork.Input{SchemaVersion: 1, Icon: input.Icon.Asset, Screenshots: input.Screenshots.Assets}
	actor, _ := identity.FromContext(r.Context())
	sealed, err := s.artwork.Seal(r.Context(), actor.TenantID, descriptorInput)
	if err != nil {
		if errors.Is(err, artwork.ErrInvalid) {
			err = catalog.E(catalog.CodeInvalidArgument, "artwork failed image validation")
		} else {
			err = catalog.E(catalog.CodeDependencyUnavailable, "artwork could not be verified")
		}
		s.fail(w, r, err)
		return
	}
	value, err := s.federation.SetArtworkOverride(r.Context(), input.Target, federation.ArtworkOverrideInput{SourceRevision: sourceRevision, Icon: federation.ArtworkIconSelection{Mode: input.Icon.Mode, Asset: sealed.Icon}, Screenshots: federation.ArtworkScreenshotSelection{Mode: input.Screenshots.Mode, Assets: sealed.Screenshots}, Reason: input.Reason}, expected)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	setETag(w, value.Revision)
	s.respondJSON(w, r, http.StatusOK, value)
}
