package httpserver

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/draftfile"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
)

// handleDraftExport prepares the complete attachment before disclosing bytes.
// Exact domain reads retain their ordinary authorization and private-read audit.
func (s *Server) handleDraftExport(w http.ResponseWriter, r *http.Request) {
	listingID := strings.ToLower(r.URL.Query().Get("listingRevisionId"))
	if r.URL.Query().Has("listingRevisionId") && !identity.ValidTenantID(listingID) {
		s.fail(w, r, catalog.E(catalog.CodeInvalidArgument, "listingRevisionId must be a UUID"))
		return
	}
	file, err := s.prepareDraftExport(r.Context(), r.PathValue("revisionId"), listingID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	raw, err := draftfile.Encode(file)
	if err != nil {
		code := catalog.CodeDependencyUnavailable
		if errors.Is(err, draftfile.ErrLimit) {
			code = catalog.CodeWorkLimitExceeded
		}
		s.fail(w, r, catalog.E(code, "retained content cannot fit the reusable draft format"))
		return
	}
	if err := r.Context().Err(); err != nil {
		s.fail(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Length", strconv.Itoa(len(raw)))
	setContentDigest(w, raw)
	w.Header().Set("Content-Disposition", `attachment; filename="open-agentic-catalog-draft.json"`)
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(raw) // #nosec G705 -- strict JSON attachment with fixed application/json, nosniff and download headers; never rendered as HTML.
}

func (s *Server) prepareDraftExport(ctx context.Context, revisionID, listingID string) (draftfile.File, error) {
	revision, _, namespace, err := s.catalog.GetVersionRevision(ctx, revisionID)
	if err != nil {
		return draftfile.File{}, err
	}
	version, err := s.catalog.GetVersion(ctx, revision.ResourceVersionID)
	if err != nil {
		return draftfile.File{}, err
	}
	resource, err := s.catalog.GetResource(ctx, version.ResourceID)
	if err != nil {
		return draftfile.File{}, err
	}
	if revision.ID != revisionID || version.ID != revision.ResourceVersionID || resource.ID != version.ResourceID || resource.NamespaceID != namespace || resource.TenantID != version.TenantID || resource.TenantID != revision.TenantID {
		return draftfile.File{}, catalog.E(catalog.CodeDependencyUnavailable, "retained draft identities disagree")
	}
	file := draftfile.File{Format: draftfile.Format, Kind: resource.Kind, CanonicalIdentifier: resource.CanonicalIdentifier, Version: version.Version, Representations: []draftfile.Representation{}}
	if listingID != "" {
		listingRevision, err := s.catalog.GetListingRevision(ctx, listingID)
		if err != nil {
			return draftfile.File{}, err
		}
		listing, err := s.catalog.GetListing(ctx, listingRevision.ListingID)
		if err != nil {
			return draftfile.File{}, err
		}
		if listingRevision.ID != listingID || listingRevision.VersionRevisionID != revisionID || listingRevision.TenantID != resource.TenantID || listing.ID != listingRevision.ListingID || listing.TenantID != resource.TenantID || listing.ResourceID != resource.ID || listing.NamespaceID != namespace {
			return draftfile.File{}, catalog.E(catalog.CodeInvalidArgument, "the Listing revision does not belong to the selected Version revision")
		}
		file.Listing, err = draftfile.FromListing(listingRevision)
		if err != nil {
			return draftfile.File{}, catalog.E(catalog.CodeDependencyUnavailable, "retained Listing metadata cannot be exported")
		}
	}
	if len(revision.Representations) < 1 || len(revision.Representations) > 16 {
		return draftfile.File{}, catalog.E(catalog.CodeWorkLimitExceeded, "the revision has an unsupported representation count")
	}
	var documentBytes int64
	for _, representation := range revision.Representations {
		if err := ctx.Err(); err != nil {
			return draftfile.File{}, err
		}
		input, err := s.exportDraftInput(ctx, resource.Kind, representation)
		if err != nil {
			return draftfile.File{}, err
		}
		if input.Text != nil {
			documentBytes += int64(len(*input.Text))
			if documentBytes > catalog.MaxCatalogDocumentBytes {
				return draftfile.File{}, catalog.E(catalog.CodeWorkLimitExceeded, "retained draft documents exceed the export byte budget")
			}
		}
		file.Representations = append(file.Representations, draftfile.Representation{Role: representation.Role, MediaType: representation.MediaType, ProfileID: representation.ProfileID, Input: input})
	}
	return file, nil
}

func (s *Server) exportDraftInput(ctx context.Context, kind catalog.Kind, representation catalog.Representation) (draftfile.Input, error) {
	missing := catalog.E(catalog.CodeDependencyUnavailable, "the representation has no available retained content")
	text := func(mode, value string) draftfile.Input { return draftfile.Input{Mode: mode, Text: &value} }
	switch {
	case representation.Plugin != nil:
		if kind != catalog.KindPlugin || catalog.ValidatePackageRepresentation(representation) != nil {
			return draftfile.Input{}, missing
		}
		return draftfile.Input{Mode: "plugin-archive", URL: &representation.URL, ExpectedDigest: &representation.ContentDigest}, nil
	case representation.Package != nil:
		if kind != catalog.KindSkill || catalog.ValidatePackageRepresentation(representation) != nil {
			return draftfile.Input{}, missing
		}
		raw, err := json.Marshal(representation.Handoff)
		if err != nil {
			return draftfile.Input{}, missing
		}
		handoff := string(raw)
		return draftfile.Input{Mode: "skill-package", Handoff: &handoff}, nil
	case representation.ArtifactID != "":
		if representation.Size > catalog.MaxCatalogDocumentBytes {
			return draftfile.Input{}, catalog.E(catalog.CodeWorkLimitExceeded, "retained document exceeds the export byte budget")
		}
		artifact, content, err := s.catalog.GetArtifact(ctx, representation.ArtifactID)
		if err != nil {
			return draftfile.Input{}, err
		}
		if artifact.ID != representation.ArtifactID || artifact.Digest != representation.ContentDigest || artifact.Size != representation.Size || artifact.MediaType != representation.MediaType || int64(len(content)) != representation.Size {
			return draftfile.Input{}, missing
		}
		return text(draftfile.DocumentMode(representation.MediaType), string(content)), nil
	case len(representation.Data) != 0:
		if len(representation.Data) > catalog.MaxCatalogDocumentBytes || !strings.HasPrefix(strings.TrimSpace(string(representation.Data)), "{") {
			return draftfile.Input{}, missing
		}
		return text("data", string(representation.Data)), nil
	default:
		// A source URL is not a substitute for absent retained document bytes.
		return draftfile.Input{}, missing
	}
}
