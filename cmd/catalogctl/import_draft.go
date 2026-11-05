package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/open-agentic-catalog/open-agentic-catalog/api"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/artwork"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/draftfile"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/jsondoc"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/safeerr"
)

// Destination inputs are command arguments, never authority from a draft file.
// Package paths are confined to that file's directory by the native importer.
type importDraftOptions struct {
	NamespaceID        string             `json:"namespaceId,omitempty"`
	ResourceID         string             `json:"resourceId,omitempty"`
	PublisherID        string             `json:"publisherId,omitempty"`
	ListingID          string             `json:"listingId,omitempty"`
	ContentOnly        bool               `json:"contentOnly,omitempty"`
	PackageFiles       importPackagePaths `json:"packageFiles,omitempty"`
	PackageDirectories importPackagePaths `json:"packageDirectories,omitempty"`
}

type importPackagePaths map[int]string

func (p *importPackagePaths) String() string { return "INDEX=RELATIVE_PATH" }
func (p *importPackagePaths) Set(value string) error {
	index, name, ok := strings.Cut(value, "=")
	n, err := strconv.Atoi(index)
	if !ok || err != nil || n < 1 || n > 16 || name == "" {
		return safeerr.New("package input requires a representation index from 1 to 16 and a relative path")
	}
	if *p == nil {
		*p = importPackagePaths{}
	}
	if _, exists := (*p)[n]; exists {
		return safeerr.New("package input index was supplied twice")
	}
	(*p)[n] = name
	return nil
}

func (o importDraftOptions) empty() bool {
	return o.NamespaceID == "" && o.ResourceID == "" && o.PublisherID == "" && o.ListingID == "" && !o.ContentOnly && len(o.PackageFiles) == 0 && len(o.PackageDirectories) == 0
}

func prepareImportInput(ctx context.Context, filename string, requestLimit int, options importDraftOptions) (importBatchPlan, error) {
	root, name, err := openImportRoot(filename)
	if err != nil {
		return importBatchPlan{}, err
	}
	defer func() { _ = root.Close() }()
	raw, err := readImportAsset(ctx, root, name, draftfile.MaxBytes)
	if err != nil {
		return importBatchPlan{}, err
	}
	var header struct {
		Format json.RawMessage `json:"format"`
	}
	if json.Unmarshal(raw, &header) == nil && len(header.Format) > 0 && header.Format[0] == '"' {
		file, err := draftfile.Decode(raw)
		if err != nil {
			return importBatchPlan{}, safeerr.New("import: invalid reusable draft JSON; check the file contract and byte limits")
		}
		return prepareImportDraft(ctx, root, raw, file, requestLimit, options)
	}
	if !options.empty() {
		return importBatchPlan{}, safeerr.New("import: destination and package flags apply only to reusable draft JSON")
	}
	return prepareImportFile(ctx, filename, requestLimit)
}

func prepareImportDraft(ctx context.Context, root *os.Root, raw []byte, file draftfile.File, requestLimit int, options importDraftOptions) (importBatchPlan, error) {
	batch := importBatchPlan{Draft: &options, Preview: importBatchPreview{Format: 1, ManifestDigest: importDigest(raw), MaxResources: 1, MaxSteps: 4, MaxPreparedBytes: importMaxPreparedBytes, MaxReportBytes: importMaxReportBytes}}
	for _, id := range []*string{&options.NamespaceID, &options.ResourceID, &options.PublisherID, &options.ListingID} {
		if *id != "" && !validImportID(*id) {
			return batch, safeerr.New("import: destination identifiers must be UUIDs")
		}
		*id = strings.ToLower(*id)
	}
	hasListing := file.Listing != nil && !options.ContentOnly
	if options.ResourceID == "" && options.NamespaceID == "" || options.ListingID != "" && (options.ResourceID == "" || !hasListing) || options.PublisherID != "" && !hasListing {
		return batch, safeerr.New("import: choose a Namespace or exact Resource; Listing and Publisher inputs require included Listing metadata")
	}
	if hasListing && options.PublisherID == "" {
		return batch, safeerr.New("import: included Listing metadata requires --publisher-id; use --content-only to omit it")
	}
	namespace := options.NamespaceID
	if namespace == "" {
		namespace = importPlaceholderID
	} // Preview explicitly leaves exact target checks pending.
	resource := importResource{Key: "draft", NamespaceID: namespace, CanonicalIdentifier: file.CanonicalIdentifier, Kind: file.Kind, Version: file.Version, contentOnly: !hasListing, existingResource: options.ResourceID != ""}
	findings := []catalog.Finding{}
	missing := func(code, path, message string) {
		findings = append(findings, catalog.Finding{Code: code, Path: path, Message: message, Severity: catalog.SeverityError})
	}
	for index, representation := range file.Representations {
		source := importRepresentation{Role: representation.Role, MediaType: representation.MediaType, ProfileID: representation.ProfileID}
		path := fmt.Sprintf("/representations/%d", index)
		switch representation.Input.Mode {
		case "data":
			source.data = json.RawMessage(*representation.Input.Text)
			if _, err := importJSONObject(source.data); err != nil {
				missing("DRAFT_NATIVE_JSON_REQUIRED", path, "Complete the native JSON object before apply.")
			}
		case "content":
			source.content = representation.Input.Text
			if *source.content == "" {
				missing("DRAFT_CONTENT_REQUIRED", path, "Add the native document before apply.")
			}
		case "url", "plugin-archive":
			source.URL, source.ExpectedDigest = *representation.Input.URL, representation.Input.ExpectedDigest
			if !catalog.ValidExternalHTTPSURL(source.URL) {
				missing("DRAFT_URL_REQUIRED", path, "Provide a credential-free HTTPS document or archive URL.")
			}
		case "skill-package":
			source.PackageFile, source.PackageDirectory = options.PackageFiles[index+1], options.PackageDirectories[index+1]
			var handoff catalog.SkillHandoff
			if jsondoc.Decode([]byte(*representation.Input.Handoff), &handoff) != nil {
				missing("DRAFT_HANDOFF_REQUIRED", path, "Complete the immutable Git or OCI handoff JSON.")
			}
			source.Handoff = &handoff
			if source.PackageFile == "" && source.PackageDirectory == "" {
				missing("DRAFT_PACKAGE_REQUIRED", path, "Supply --package-file or --package-directory for this representation.")
			}
		case "plugin-preparation":
			missing("DRAFT_PLUGIN_ARCHIVE_REQUIRED", path, "Package the Plugin and replace preparation input with an HTTPS archive URL before apply.")
		}
		if options.PackageFiles[index+1] != "" || options.PackageDirectories[index+1] != "" {
			if representation.Input.Mode != "skill-package" || options.PackageFiles[index+1] != "" && options.PackageDirectories[index+1] != "" {
				return batch, safeerr.New("import: each package mapping must select exactly one Skill package source")
			}
		}
		resource.Representations = append(resource.Representations, source)
	}
	for _, paths := range []importPackagePaths{options.PackageFiles, options.PackageDirectories} {
		for index := range paths {
			if index > len(file.Representations) {
				return batch, safeerr.New("import: package mapping has no matching representation")
			}
		}
	}
	if hasListing {
		listing := file.Listing
		resource.Listing = importListing{PublisherID: options.PublisherID, DisplayName: listing.DisplayName, Description: listing.Description, Visibility: listing.Visibility, Tags: listing.Tags, Categories: listing.Categories, RepresentativeQueries: listing.RepresentativeQueries, DocumentationURL: listing.DocumentationURL, SupportURL: listing.SupportURL, TermsURL: listing.TermsURL, License: listing.License, Relationships: listing.Relationships}
		extensions := map[string]any{}
		if listing.Extensions != nil {
			var err error
			extensions, err = importJSONObject([]byte(*listing.Extensions))
			if err != nil {
				missing("DRAFT_EXTENSIONS_REQUIRED", "/listing/extensions", "Complete the Listing extensions JSON object before apply.")
			}
		}
		if listing.Artwork != nil {
			if _, conflict := extensions[artwork.IRI]; conflict {
				return batch, safeerr.New("import: artwork appears in both Listing extensions and artwork input")
			}
			if extensions != nil {
				extensions[artwork.IRI] = listing.Artwork
			}
		}
		var err error
		resource.Listing.Extensions, err = json.Marshal(extensions)
		if err != nil {
			return batch, safeerr.New("import: cannot encode Listing extensions")
		}
	}
	resource, err := normalizeImportResource(resource)
	if err != nil {
		return batch, err
	}
	// Relationships are destination metadata. Never rewrite or import targets.
	if err = checkImportRelationships([]importResource{resource}, map[string]int{}); err != nil {
		return batch, err
	}
	pending := []string{"Exact destination Resource identity, tenant and Namespace", "Current permissions, OAuth scopes and server request limits", "Optional Listing lookup, parent identities and strong ETag"}
	if len(findings) != 0 {
		preview := importPreview{Format: 1, Key: "draft", Kind: file.Kind, ManifestDigest: importDigest(raw), MaxRequestBytes: requestLimit, MaxPreparedBytes: importMaxPreparedBytes, Findings: findings, PendingServerChecks: pending}
		batch.Items, batch.Preview.Resources = []importPlan{{Preview: preview}}, []importPreview{preview}
		return batch, nil
	}
	loader := openapi3.NewLoader()
	loader.Context = ctx
	contract, err := loader.LoadFromData(api.ControlV1)
	if err != nil {
		return batch, safeerr.New("import: cannot load the embedded request contract")
	}
	plan, err := prepareImportResource(ctx, root, resource, importDigest(raw), requestLimit, importMaxPreparedBytes, contract)
	if err != nil {
		return batch, err
	}
	checks := make([]string, 0, len(pending)+len(plan.Preview.PendingServerChecks))
	checks = append(checks, pending...)
	plan.Preview.PendingServerChecks = append(checks, plan.Preview.PendingServerChecks...)
	batch.Items = []importPlan{plan}
	preview := plan.Preview
	if options.ResourceID != "" {
		preview.Commands = append([]importCommandPreview(nil), preview.Commands[1:]...)
		preview.Commands[0].Bindings = []string{"path.resourceId: explicit destination Resource; identity checks pending"}
		if options.ListingID != "" {
			preview.Commands = []importCommandPreview{preview.Commands[0], preview.Commands[2]}
			preview.Commands[1].Bindings = []string{"path.listingId: explicit destination Listing; parent checks pending", "body.versionRevisionId: Version response"}
			preview.Commands[1].IfMatch = "Strong ETag from exact destination Listing; check pending"
		}
	}
	batch.Preview.Resources, batch.Preview.LocallyValid, batch.Preview.PreparedBytes = []importPreview{preview}, preview.LocallyValid, preview.PreparedBytes
	if _, err = encodeImportReport(batch.Preview); err != nil {
		return batch, err
	}
	return batch, nil
}
