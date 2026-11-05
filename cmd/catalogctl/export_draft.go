package main

import (
	"context"
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"net/url"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/draftfile"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/jsondoc"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/safeerr"
)

func validateExportFormat(o exportOptions) error {
	if o.format != "native" && o.format != "draft" && !isMCPExportFormat(o.format) {
		return safeerr.New("export: --format must be native, draft, mcp-vscode, mcp-claude-code or mcp-cursor")
	}
	if o.remoteSelected && (!isMCPExportFormat(o.format) || o.remotePosition < 1) {
		return safeerr.New("export: --remote requires a client format and a positive 1-based position")
	}
	if o.format == "native" || isMCPExportFormat(o.format) {
		if !identity.ValidTenantID(o.releaseID) || o.representationID != "" && !identity.ValidTenantID(o.representationID) || o.versionRevisionID != "" || o.listingRevisionID != "" {
			return safeerr.New("export: native/client format requires a release UUID and optional representation UUID")
		}
		return nil
	}
	if o.representationSelected || o.representationID != "" || (o.releaseID == "") == (o.versionRevisionID == "") || o.releaseID != "" && (!identity.ValidTenantID(o.releaseID) || o.listingRevisionID != "") || o.versionRevisionID != "" && !identity.ValidTenantID(o.versionRevisionID) || o.listingRevisionID != "" && !identity.ValidTenantID(o.listingRevisionID) {
		return safeerr.New("export: draft format requires either a release UUID or an exact VersionRevision UUID with optional ListingRevision UUID; representation selectors are not supported")
	}
	return nil
}

func prepareDraftDownload(ctx context.Context, session *remoteSession, o exportOptions) (exportManifest, []byte, error) {
	var record exportRecord
	var err error
	if o.releaseID != "" {
		var response remoteEnvelope
		response, err = exchangeRemote(ctx, session.client, o.remote, session.origin, session.token)
		if err == nil {
			record, err = decodeExportRecord(response.Data, o.releaseID)
		}
	} else {
		record, err = readDraftExportTarget(ctx, session, o)
	}
	if err != nil {
		return exportManifest{}, nil, err
	}
	if !exportValidDigest(record.Revision.ContentSetDigest) {
		return exportManifest{}, nil, safeerr.New("export: selected revision has no valid content-set digest")
	}
	manifest := exportManifest{Format: 1, Disposition: "reusable_draft", TenantID: record.Resource.TenantID, ResourceID: record.Resource.ID, Kind: record.Resource.Kind, Version: record.Version.Version, VersionID: record.Version.ID, VersionRevisionID: record.Revision.ID, ListingRevisionID: record.ListingRevision.ID, ListingID: record.Listing.ID, ReleaseID: record.Release.ID, ContentSetDigest: record.Revision.ContentSetDigest, MediaType: "application/json", ProfileID: draftfile.Format}
	if o.selection != nil && !o.selection.matches(manifest) {
		return manifest, nil, errExportSelectionChanged
	}
	path := "/api/v1/version-revisions/" + manifest.VersionRevisionID + "/export"
	if manifest.ListingRevisionID != "" {
		path += "?listingRevisionId=" + url.QueryEscape(manifest.ListingRevisionID)
	}
	request := o.remote
	request.path = path
	response, err := exchangeRemote(ctx, session.client, request, session.origin, session.token)
	if err != nil {
		return manifest, nil, err
	}
	if int64(len(response.Data)) > o.maxContentBytes {
		return manifest, nil, safeerr.New("export: draft JSON exceeds the configured content byte limit")
	}
	file, err := draftfile.Decode(response.Data)
	if err != nil || file.Kind != record.Resource.Kind || file.CanonicalIdentifier != record.Resource.CanonicalIdentifier || file.Version != record.Version.Version || (file.Listing != nil) != (manifest.ListingRevisionID != "") || len(file.Representations) != len(record.Revision.Representations) {
		return manifest, nil, safeerr.New("export: draft response disagrees with the exact selected content")
	}
	seen := make(map[string]bool, len(file.Representations))
	for index, representation := range file.Representations {
		expected := record.Revision.Representations[index]
		if !identity.ValidTenantID(expected.ID) || seen[expected.ID] || representation.Role != expected.Role || representation.MediaType != expected.MediaType || representation.ProfileID != expected.ProfileID || expected.Plugin != nil && (representation.Input.Mode != "plugin-archive" || representation.Input.ExpectedDigest == nil || *representation.Input.ExpectedDigest != expected.ContentDigest || representation.Input.URL == nil || *representation.Input.URL != expected.URL) {
			return manifest, nil, safeerr.New("export: draft representations disagree with the exact selected revision")
		}
		seen[expected.ID] = true
		if expected.ArtifactID != "" && (representation.Input.Mode != draftfile.DocumentMode(expected.MediaType) || representation.Input.Text == nil || int64(len(*representation.Input.Text)) != expected.Size || exportChecksum([]byte(*representation.Input.Text)) != expected.ContentDigest) {
			return manifest, nil, safeerr.New("export: draft document checksum disagrees with retained content")
		}
	}
	compatible := draftfile.BrowserCompatible(file) == nil
	manifest.BrowserCompatible = &compatible
	manifest.File = &exportFile{Name: "draft.json", exportDigest: exportDigest{Digest: exportChecksum(response.Data), Size: int64(len(response.Data)), Scope: "reusable_draft_file"}}
	// Preserve the API's raw JSON attachment, including opaque native strings.
	return manifest, response.Data, nil
}

// Explicit revision selection uses only exact reads. No history or latest
// pointer is consulted. Optional Listing metadata receives the same bindings.
func readDraftExportTarget(ctx context.Context, session *remoteSession, o exportOptions) (exportRecord, error) {
	var record exportRecord
	var envelope struct {
		Revision json.RawMessage `json:"revision"`
	}
	if err := readExportJSON(ctx, session, o.remote, "/api/v1/version-revisions/"+o.versionRevisionID, &envelope); err != nil {
		return record, err
	}
	if jsonv2.Unmarshal(envelope.Revision, &record.Revision, json.DefaultOptionsV1(), jsonv2.MatchCaseInsensitiveNames(false)) != nil || record.Revision.ID != o.versionRevisionID || !identity.ValidTenantID(record.Revision.ResourceVersionID) || !identity.ValidTenantID(record.Revision.TenantID) {
		return record, safeerr.New("export: invalid exact VersionRevision response")
	}
	if err := readExportJSON(ctx, session, o.remote, "/api/v1/resource-versions/"+record.Revision.ResourceVersionID, &record.Version); err != nil {
		return record, err
	}
	if record.Version.ID != record.Revision.ResourceVersionID || record.Version.TenantID != record.Revision.TenantID || !identity.ValidTenantID(record.Version.ResourceID) {
		return record, safeerr.New("export: invalid exact Version response")
	}
	if err := readExportJSON(ctx, session, o.remote, "/api/v1/resources/"+record.Version.ResourceID, &record.Resource); err != nil {
		return record, err
	}
	if record.Resource.ID != record.Version.ResourceID || record.Resource.TenantID != record.Version.TenantID || !identity.ValidTenantID(record.Resource.NamespaceID) || !record.Resource.Kind.Valid() || len(record.Revision.Representations) < 1 || len(record.Revision.Representations) > 16 {
		return record, safeerr.New("export: invalid exact Resource response")
	}
	if o.listingRevisionID != "" {
		if err := readExportJSON(ctx, session, o.remote, "/api/v1/listing-revisions/"+o.listingRevisionID, &record.ListingRevision); err != nil {
			return record, err
		}
		if record.ListingRevision.ID != o.listingRevisionID || record.ListingRevision.TenantID != record.Resource.TenantID || record.ListingRevision.VersionRevisionID != o.versionRevisionID || !identity.ValidTenantID(record.ListingRevision.ListingID) {
			return record, safeerr.New("export: ListingRevision does not belong to the exact selected content")
		}
		if err := readExportJSON(ctx, session, o.remote, "/api/v1/catalog-listings/"+record.ListingRevision.ListingID, &record.Listing); err != nil {
			return record, err
		}
		if record.Listing.ID != record.ListingRevision.ListingID || record.Listing.TenantID != record.Resource.TenantID || record.Listing.NamespaceID != record.Resource.NamespaceID || record.Listing.ResourceID != record.Resource.ID {
			return record, safeerr.New("export: Listing does not belong to the exact selected Resource")
		}
	}
	return record, nil
}

func readExportJSON(ctx context.Context, session *remoteSession, options remoteOptions, path string, value any) error {
	options.path = path
	response, err := exchangeRemote(ctx, session.client, options, session.origin, session.token)
	if err != nil {
		return err
	}
	if jsondoc.Validate(response.Data) != nil || jsonv2.Unmarshal(response.Data, value, json.DefaultOptionsV1(), jsonv2.MatchCaseInsensitiveNames(false)) != nil {
		return safeerr.New("export: invalid exact metadata response")
	}
	return nil
}
