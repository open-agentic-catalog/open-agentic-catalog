package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"mime"
	"strings"
	"unicode/utf8"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/jsondoc"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/safeerr"
)

// Decode only consumed fields. Native values and inspections stay raw: decoding
// arbitrary extension numbers through float64 would irreversibly change them.
type exportRecord struct {
	Source   string                  `json:"source"`
	Resource catalog.Resource        `json:"resource"`
	Version  catalog.ResourceVersion `json:"version"`
	Release  catalog.ListingRelease  `json:"release"`
	Listing  catalog.CatalogListing  `json:"listing"`
	Revision struct {
		ID                string                 `json:"id"`
		ContentSetDigest  string                 `json:"contentSetDigest"`
		ResourceVersionID string                 `json:"resourceVersionId"`
		TenantID          string                 `json:"tenantId"`
		Representations   []exportRepresentation `json:"representations"`
	} `json:"versionRevision"`
	ListingRevision struct {
		ID                string `json:"id"`
		TenantID          string `json:"tenantId"`
		ListingID         string `json:"listingId"`
		VersionRevisionID string `json:"versionRevisionId"`
	} `json:"listingRevision"`
}

type exportRepresentation struct {
	ID              string                     `json:"id"`
	Role            catalog.RepresentationRole `json:"role"`
	MediaType       string                     `json:"mediaType"`
	ProfileID       string                     `json:"profileId"`
	ContentDigest   string                     `json:"contentDigest"`
	Size            int64                      `json:"size"`
	ArtifactID      string                     `json:"artifactId,omitempty"`
	Data            json.RawMessage            `json:"data,omitempty"`
	URL             string                     `json:"url,omitempty"`
	Package         json.RawMessage            `json:"package,omitempty"`
	Plugin          json.RawMessage            `json:"plugin,omitempty"`
	Handoff         json.RawMessage            `json:"handoff,omitempty"`
	ValidationState string                     `json:"validationState"`
}

type exportDigest struct {
	Digest string `json:"digest"`
	Size   int64  `json:"size"`
	Scope  string `json:"scope"`
}

type exportFile struct {
	Name string `json:"name"`
	exportDigest
}

type exportManifest struct {
	Format            int                  `json:"format"`
	TenantID          string               `json:"tenantId"`
	ReleaseID         string               `json:"releaseId"`
	ResourceID        string               `json:"resourceId"`
	VersionID         string               `json:"versionId"`
	VersionRevisionID string               `json:"versionRevisionId"`
	ListingID         string               `json:"listingId"`
	ListingRevisionID string               `json:"listingRevisionId"`
	RepresentationID  string               `json:"representationId"`
	Kind              catalog.Kind         `json:"kind"`
	Version           string               `json:"version"`
	MediaType         string               `json:"mediaType"`
	ProfileID         string               `json:"profileId"`
	Disposition       string               `json:"disposition"`
	Recorded          exportDigest         `json:"recorded,omitzero"`
	ContentSetDigest  string               `json:"contentSetDigest,omitempty"`
	BrowserCompatible *bool                `json:"browserCompatible,omitempty"`
	File              *exportFile          `json:"file,omitempty"`
	URL               string               `json:"url,omitempty"`
	Package           json.RawMessage      `json:"package,omitempty"`
	Plugin            json.RawMessage      `json:"plugin,omitempty"`
	Handoff           json.RawMessage      `json:"handoff,omitempty"`
	MCP               *exportMCPConnection `json:"mcp,omitempty"`
}

func prepareExport(ctx context.Context, session *remoteSession, options exportOptions) (exportManifest, []byte, error) {
	if options.format == "draft" {
		return prepareDraftDownload(ctx, session, options)
	}
	reply, err := exchangeRemote(ctx, session.client, options.remote, session.origin, session.token)
	if err != nil {
		return exportManifest{}, nil, err
	}
	record, err := decodeExportRecord(reply.Data, options.releaseID)
	if err != nil {
		return exportManifest{}, nil, err
	}
	if isMCPExportFormat(options.format) && (record.Resource.Kind != catalog.KindMCPServer || record.Release.State != catalog.ReleasePublished && record.Release.State != catalog.ReleaseDeprecated) {
		return exportManifest{}, nil, safeerr.New("export: client configuration requires a current local published or deprecated MCP release")
	}
	manifest, representation, err := selectExportRecord(record, options.representationID)
	if err != nil {
		return manifest, nil, err
	}
	if isMCPExportFormat(options.format) && !mcpExportRepresentation(representation) {
		return manifest, nil, safeerr.New("export: client configuration requires a supported retained native MCP document; use native export otherwise")
	}
	if options.selection != nil && !options.selection.matches(manifest) {
		return manifest, nil, errExportSelectionChanged
	}
	var content []byte
	switch manifest.Disposition {
	case "exact_document":
		content, err = downloadExportArtifact(ctx, session, representation, options.maxContentBytes)
	case "inline_json":
		content = representation.Data
		if int64(len(content)) > options.maxContentBytes {
			err = safeerr.New("export: inline JSON exceeds the configured content byte limit")
		}
	}
	if err != nil {
		return manifest, nil, err
	}
	if isMCPExportFormat(options.format) {
		content, manifest.MCP, err = projectMCPExport(content, representation.ProfileID, representation.MediaType, manifest.ReleaseID, options.format, options.remotePosition)
		if err != nil {
			return manifest, nil, err
		}
		if int64(len(content)) > options.maxContentBytes {
			return manifest, nil, safeerr.New("export: client JSON exceeds the configured content byte limit")
		}
		manifest.Disposition = "mcp_client_configuration"
		manifest.File = &exportFile{Name: mcpExportFilename(options.format), exportDigest: exportDigest{Scope: "generated_client_configuration"}}
	}
	if manifest.File != nil {
		manifest.File.Digest = exportChecksum(content)
		manifest.File.Size = int64(len(content))
	}
	return manifest, content, nil
}

func selectExportRecord(record exportRecord, representationID string) (exportManifest, exportRepresentation, error) {
	if representationID == "" && len(record.Revision.Representations) != 1 {
		return exportManifest{}, exportRepresentation{}, safeerr.New("export: several representations are available; supply --representation-id")
	}
	return selectExportRepresentation(record, representationID)
}

func decodeExportRecord(raw []byte, releaseID string) (exportRecord, error) {
	var record exportRecord
	fail := safeerr.New("export: record identities or selected representation are invalid")
	// Unlike closed request objects, Catalog responses can gain new fields. Keep
	// exact field names and reject ambiguous JSON without rejecting those additions.
	if jsondoc.Validate(raw) != nil || jsonv2.Unmarshal(raw, &record, json.DefaultOptionsV1(), jsonv2.MatchCaseInsensitiveNames(false)) != nil {
		return record, fail
	}
	ids := []string{record.Resource.ID, record.Resource.TenantID, record.Resource.NamespaceID, record.Version.ID, record.Release.ID, record.Revision.ID, record.Listing.ID, record.ListingRevision.ID}
	for _, id := range ids {
		if !identity.ValidTenantID(id) {
			return record, fail
		}
	}
	if record.Source != "local" || record.Release.ID != releaseID || record.Version.ResourceID != record.Resource.ID || record.Revision.ResourceVersionID != record.Version.ID || record.Release.VersionRevisionID != record.Revision.ID || record.Release.ListingID != record.Listing.ID || record.Release.ListingRevisionID != record.ListingRevision.ID || record.Listing.ResourceID != record.Resource.ID || record.Listing.NamespaceID != record.Resource.NamespaceID || record.ListingRevision.ListingID != record.Listing.ID || record.ListingRevision.VersionRevisionID != record.Revision.ID {
		return record, fail
	}
	for _, tenant := range []string{record.Version.TenantID, record.Revision.TenantID, record.Release.TenantID, record.Listing.TenantID, record.ListingRevision.TenantID} {
		if tenant != record.Resource.TenantID {
			return record, fail
		}
	}
	if !record.Resource.Kind.Valid() || record.Version.Version == "" || utf8.RuneCountInString(record.Version.Version) > 128 || len(record.Revision.Representations) < 1 || len(record.Revision.Representations) > 16 {
		return record, fail
	}
	return record, nil
}

func selectExportRepresentation(record exportRecord, representationID string) (exportManifest, exportRepresentation, error) {
	fail := safeerr.New("export: record identities or selected representation are invalid")
	selected := -1
	seen := map[string]bool{}
	for index, representation := range record.Revision.Representations {
		if !identity.ValidTenantID(representation.ID) || seen[representation.ID] {
			return exportManifest{}, exportRepresentation{}, fail
		}
		seen[representation.ID] = true
		if representationID == "" || representation.ID == representationID {
			selected = index
		}
	}
	if selected < 0 {
		return exportManifest{}, exportRepresentation{}, fail
	}
	representation := record.Revision.Representations[selected]
	media, _, mediaErr := mime.ParseMediaType(representation.MediaType)
	if mediaErr != nil || !strings.Contains(media, "/") || len(representation.MediaType) > 255 || representation.ProfileID == "" || len(representation.ProfileID) > 160 || !exportValidDigest(representation.ContentDigest) || representation.Size <= 0 || representation.ValidationState != "valid" {
		return exportManifest{}, exportRepresentation{}, fail
	}
	manifest := exportManifest{Format: 1, TenantID: record.Resource.TenantID, ReleaseID: record.Release.ID, ResourceID: record.Resource.ID, VersionID: record.Version.ID, VersionRevisionID: record.Revision.ID, ListingID: record.Listing.ID, ListingRevisionID: record.ListingRevision.ID, RepresentationID: representation.ID, Kind: record.Resource.Kind, Version: record.Version.Version, MediaType: representation.MediaType, ProfileID: representation.ProfileID,
		Recorded: exportDigest{representation.ContentDigest, representation.Size, "retained_document"}}
	hasData := len(representation.Data) != 0
	hasInspection := len(representation.Package) != 0 || len(representation.Plugin) != 0 || len(representation.Handoff) != 0
	switch {
	case representation.ArtifactID != "":
		if representation.ContentDigest != "sha256:"+representation.ArtifactID || hasData || hasInspection {
			return exportManifest{}, exportRepresentation{}, fail
		}
		manifest.Disposition = "exact_document"
		manifest.File = &exportFile{Name: "native.bin", exportDigest: exportDigest{Scope: "exact_retained_bytes"}}
	case hasData:
		if hasInspection || representation.URL != "" || !bytes.HasPrefix(bytes.TrimSpace(representation.Data), []byte("{")) {
			return exportManifest{}, exportRepresentation{}, fail
		}
		manifest.Disposition = "inline_json"
		manifest.File = &exportFile{Name: "native.json", exportDigest: exportDigest{Scope: "record_inline_json"}}
	case hasInspection:
		// Validate the existing package shape, but retain raw inspections rather
		// than reserializing unknown native values or reconstructing an archive.
		var full catalog.Representation
		encoded, err := json.Marshal(representation)
		if err != nil || json.Unmarshal(encoded, &full) != nil || catalog.ValidatePackageRepresentation(full) != nil || full.Package == nil && full.Plugin == nil || full.Package != nil && record.Resource.Kind != catalog.KindSkill || full.Plugin != nil && record.Resource.Kind != catalog.KindPlugin {
			return exportManifest{}, exportRepresentation{}, fail
		}
		manifest.Disposition = "reference_only"
		manifest.Recorded.Scope = "external_archive"
		manifest.URL, manifest.Package, manifest.Plugin, manifest.Handoff = representation.URL, representation.Package, representation.Plugin, representation.Handoff
	default:
		return exportManifest{}, exportRepresentation{}, fail
	}
	return manifest, representation, nil
}

func exportChecksum(raw []byte) string {
	digest := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(digest[:])
}

func exportValidDigest(value string) bool {
	if len(value) != 71 || !strings.HasPrefix(value, "sha256:") {
		return false
	}
	for _, c := range value[7:] {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}
