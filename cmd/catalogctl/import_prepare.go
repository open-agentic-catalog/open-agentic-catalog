package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"mime"
	"os"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/open-agentic-catalog/open-agentic-catalog/api"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/adapter"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/artwork"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/jsondoc"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/safeerr"
)

type importBatchPlan struct {
	Preview importBatchPreview  `json:"preview"`
	Items   []importPlan        `json:"items"`
	Draft   *importDraftOptions `json:"draft,omitempty"`
}

type importBatchPreview struct {
	Format           int             `json:"format"`
	LocallyValid     bool            `json:"locallyValid"`
	ManifestDigest   string          `json:"manifestDigest"`
	MaxResources     int             `json:"maxResources"`
	MaxSteps         int             `json:"maxSteps"`
	MaxPreparedBytes int             `json:"maxPreparedBytes"`
	MaxReportBytes   int             `json:"maxReportBytes"`
	PreparedBytes    int             `json:"preparedBytes"`
	Resources        []importPreview `json:"resources"`
}

type importPlan struct {
	Preview importPreview     `json:"preview"`
	Bodies  []json.RawMessage `json:"bodies"`
}

type importPreview struct {
	Format              int                      `json:"format"`
	Key                 string                   `json:"key"`
	Kind                catalog.Kind             `json:"kind"`
	LocallyValid        bool                     `json:"locallyValid"`
	ManifestDigest      string                   `json:"manifestDigest"`
	MaxRequestBytes     int                      `json:"maxRequestBytes"`
	MaxPreparedBytes    int                      `json:"maxPreparedBytes"`
	PreparedBytes       int                      `json:"preparedBytes"`
	ServerLimitsChecked bool                     `json:"serverLimitsChecked"`
	Assets              []importAsset            `json:"assets"`
	Representations     []importPreparedEvidence `json:"representations"`
	Findings            []catalog.Finding        `json:"findings"`
	Commands            []importCommandPreview   `json:"commands"`
	PendingServerChecks []string                 `json:"pendingServerChecks"`
}

type importAsset struct {
	Path   string `json:"path"`
	Bytes  int    `json:"bytes"`
	Digest string `json:"digest"`
	Scope  string `json:"scope"`
}

type importCommandPreview struct {
	Method      string   `json:"method"`
	Path        string   `json:"path"`
	BodyFields  []string `json:"bodyFields"`
	BodyBytes   int      `json:"bodyBytes"`
	BodyDigest  string   `json:"bodyDigest"`
	DigestScope string   `json:"digestScope"`
	Bindings    []string `json:"bindings,omitempty"`
	IfMatch     string   `json:"ifMatch,omitempty"`
}

type importPreparedEvidence struct {
	Role            catalog.RepresentationRole `json:"role"`
	MediaType       string                     `json:"mediaType"`
	ProfileID       string                     `json:"profileId"`
	LocalValidation string                     `json:"localValidation"`
	PackageFiles    int                        `json:"packageFiles,omitempty"`
	ExecutableFiles int                        `json:"executableFiles,omitempty"`
}

func importDigest(raw []byte) string { return fmt.Sprintf("sha256:%x", sha256.Sum256(raw)) }

func prepareImportFile(ctx context.Context, filename string, requestLimit int) (importBatchPlan, error) {
	root, name, err := openImportRoot(filename)
	if err != nil {
		return importBatchPlan{}, err
	}
	defer func() { _ = root.Close() }()
	raw, err := readImportAsset(ctx, root, name, importMaxManifestBytes)
	if err != nil {
		return importBatchPlan{}, err
	}
	manifest, err := decodeImportManifest(raw)
	if err != nil {
		return importBatchPlan{}, err
	}
	batch := importBatchPlan{Preview: importBatchPreview{Format: 1, LocallyValid: true,
		ManifestDigest: importDigest(raw), MaxResources: importMaxResources, MaxSteps: importMaxSteps,
		MaxPreparedBytes: importMaxPreparedBytes, MaxReportBytes: importMaxReportBytes}}
	seenKeys, identifiers := map[string]bool{}, map[string]int{}
	for index, resource := range manifest.Resources {
		resource, err = normalizeImportResource(resource)
		if err != nil {
			return batch, err
		}
		if seenKeys[resource.Key] {
			return batch, safeerr.New("import resource keys must be unique")
		}
		if _, exists := identifiers[resource.CanonicalIdentifier]; exists {
			return batch, safeerr.New("import canonical identifiers must be unique after normalization")
		}
		seenKeys[resource.Key], identifiers[resource.CanonicalIdentifier] = true, index
		manifest.Resources[index] = resource
	}
	if err = checkImportRelationships(manifest.Resources, identifiers); err != nil {
		return batch, err
	}
	// Parse the service-owned contract once for the entire bounded manifest.
	loader := openapi3.NewLoader()
	loader.Context = ctx
	contract, err := loader.LoadFromData(api.ControlV1)
	if err != nil {
		return batch, safeerr.Wrap(err, "cannot load the embedded import request contract")
	}
	for _, resource := range manifest.Resources {
		plan, err := prepareImportResource(ctx, root, resource, importDigest(raw), requestLimit, importMaxPreparedBytes-batch.Preview.PreparedBytes, contract)
		if err != nil {
			return batch, err
		}
		batch.Items = append(batch.Items, plan)
		batch.Preview.Resources = append(batch.Preview.Resources, plan.Preview)
		batch.Preview.PreparedBytes += plan.Preview.PreparedBytes
		batch.Preview.LocallyValid = batch.Preview.LocallyValid && plan.Preview.LocallyValid
	}
	// Qualify the actual output budget before any remote operation.
	if _, err = encodeImportReport(batch.Preview); err != nil {
		return batch, err
	}
	return batch, nil
}

func normalizeImportResource(resource importResource) (importResource, error) {
	if !catalog.ValidSkillName(resource.Key) || !identity.ValidTenantID(resource.NamespaceID) || !resource.contentOnly && !identity.ValidTenantID(resource.Listing.PublisherID) || !resource.Kind.Valid() {
		return resource, safeerr.New("import requires a stable lowercase key, existing namespace/publisher UUIDs and a supported kind")
	}
	// API/PostgreSQL UUID responses use lowercase even when input is uppercase.
	// Bind prepared requests and acknowledgements to that canonical spelling.
	resource.NamespaceID = strings.ToLower(resource.NamespaceID)
	resource.Listing.PublisherID = strings.ToLower(resource.Listing.PublisherID)
	resource.CanonicalIdentifier = strings.TrimSpace(resource.CanonicalIdentifier)
	resource.Version = strings.TrimSpace(resource.Version)
	if strings.HasPrefix(strings.ToLower(resource.CanonicalIdentifier), "urn:air:") {
		identifier, err := catalog.ParseAIRIdentifier(resource.CanonicalIdentifier)
		if err != nil {
			return resource, safeerr.Wrap(err, "import AIR canonical identifier is invalid")
		}
		resource.CanonicalIdentifier = identifier.String()
	}
	if resource.CanonicalIdentifier == "" || !utf8.ValidString(resource.CanonicalIdentifier) || utf8.RuneCountInString(resource.CanonicalIdentifier) > 512 || strings.ContainsRune(resource.CanonicalIdentifier, 0) || resource.Version == "" || !utf8.ValidString(resource.Version) || utf8.RuneCountInString(resource.Version) > 128 || strings.ContainsRune(resource.Version, 0) || len(resource.Representations) < 1 || len(resource.Representations) > 16 {
		return resource, safeerr.New("import requires a nonempty identifier/version and one to sixteen representations")
	}
	return resource, nil
}

func checkImportRelationships(resources []importResource, identifiers map[string]int) error {
	for index := range resources {
		seen := map[string]bool{}
		relationships := resources[index].Listing.Relationships
		for relationIndex, relationship := range relationships {
			relationship.Type = strings.ToLower(strings.TrimSpace(relationship.Type))
			relationship.TargetIdentifier = strings.TrimSpace(relationship.TargetIdentifier)
			relationship.TargetVersion = strings.TrimSpace(relationship.TargetVersion)
			switch relationship.Type {
			case "depends_on", "recommended_with", "supersedes", "bundled_with":
			default:
				return safeerr.New("import relationship type is not supported")
			}
			if relationship.TargetIdentifier == "" || !utf8.ValidString(relationship.TargetIdentifier) || utf8.RuneCountInString(relationship.TargetIdentifier) > 512 || strings.ContainsRune(relationship.TargetIdentifier, 0) || !utf8.ValidString(relationship.TargetVersion) || utf8.RuneCountInString(relationship.TargetVersion) > 128 || strings.ContainsRune(relationship.TargetVersion, 0) || relationship.Type == "supersedes" && relationship.TargetVersion == "" {
				return safeerr.New("import relationship target or version is invalid")
			}
			if strings.HasPrefix(strings.ToLower(relationship.TargetIdentifier), "urn:air:") {
				identifier, err := catalog.ParseAIRIdentifier(relationship.TargetIdentifier)
				if err != nil {
					return safeerr.New("import relationship AIR identifier is invalid")
				}
				relationship.TargetIdentifier = identifier.String()
			}
			if target, exists := identifiers[relationship.TargetIdentifier]; exists {
				if target >= index {
					return safeerr.New("import relationships must target earlier resources; self and forward dependencies are refused")
				}
				if relationship.TargetVersion != "" && relationship.TargetVersion != resources[target].Version {
					return safeerr.New("import relationship version does not match the earlier resource")
				}
			}
			key := relationship.Type + "\x00" + relationship.TargetIdentifier + "\x00" + relationship.TargetVersion
			if seen[key] {
				return safeerr.New("import relationships must be unique after normalization")
			}
			seen[key] = true
			relationships[relationIndex] = relationship
		}
	}
	return nil
}

func prepareImportResource(ctx context.Context, root *os.Root, resource importResource, manifestDigest string, requestLimit, remaining int, contract *openapi3.T) (importPlan, error) {
	plan := importPlan{Preview: importPreview{
		Format: 1, Key: resource.Key, Kind: resource.Kind, LocallyValid: true,
		ManifestDigest: manifestDigest, MaxRequestBytes: requestLimit, MaxPreparedBytes: importMaxPreparedBytes,
		Assets: []importAsset{}, Representations: []importPreparedEvidence{},
		PendingServerChecks: []string{"Current caller permissions and OAuth scopes", "Namespace, publisher, canonical identifier and relationship availability", "Advertised request limits and current server validation policy", "HTTPS retrieval, remote native content, artwork sealing and outbound policy", "Immutable Git/OCI handoff content, trust and rights", "Review, release and public discovery eligibility"},
	}}
	findings := catalog.NewFindingCollector(catalog.DefaultDiagnosticLimits())
	inputs := make([]catalog.RepresentationInput, 0, len(resource.Representations))
	primary := 0
	documentBytes := 0
	// Enforce the serialized Version request budget while preparing each
	// representation, before retaining a manifest-sized collection of assets.
	versionPrefix, err := json.Marshal(catalog.CreateVersionInput{Version: resource.Version, Representations: []catalog.RepresentationInput{}})
	if err != nil {
		return importPlan{}, safeerr.Wrap(err, "cannot encode import version")
	}
	versionBytes := len(versionPrefix)
	for index, source := range resource.Representations {
		input, evidence, assets, result, err := prepareImportRepresentation(ctx, root, resource.Kind, source)
		if err != nil {
			return importPlan{}, err
		}
		encodedInput, err := json.Marshal(input)
		if err != nil {
			return importPlan{}, safeerr.Wrap(err, "cannot encode import representation")
		}
		versionBytes += len(encodedInput)
		if index > 0 {
			versionBytes++
		}
		if versionBytes > requestLimit || versionBytes > remaining {
			return importPlan{}, safeerr.New("encoded import Version request exceeds the selected byte limit; JSON and base64 overhead count")
		}
		if input.Role == catalog.RepresentationPrimary {
			primary++
		}
		if nativeVersion, ok := result.NormalizedFacts["version"].(string); ok && nativeVersion != "" && nativeVersion != resource.Version {
			result.Findings = append(result.Findings, catalog.Finding{Code: "NATIVE_VERSION_MISMATCH", Severity: catalog.SeverityError})
		}
		for _, finding := range result.Findings {
			// Native schema diagnostics can quote supplied values. Emit only the
			// fixed code, severity and representation index, never private content.
			findings.Add(catalog.Finding{Code: finding.Code, Severity: finding.Severity, Path: fmt.Sprintf("/representations/%d", index), Message: "The pinned native validator reported this finding; inspect the local input."})
		}
		documentBytes += len(input.Data) + len(input.Content)
		if documentBytes > catalog.MaxCatalogDocumentBytes {
			return importPlan{}, safeerr.New("import representation documents exceed the 16 MiB revision limit")
		}
		inputs = append(inputs, input)
		plan.Preview.Assets = append(plan.Preview.Assets, assets...)
		plan.Preview.Representations = append(plan.Preview.Representations, evidence)
	}
	if primary != 1 {
		findings.Add(catalog.Finding{Code: "PRIMARY_REPRESENTATION_COUNT", Severity: catalog.SeverityError, Message: "Exactly one primary representation is required."})
	}
	findings.Add(catalog.Finding{Code: "SERVER_CHECKS_PENDING", Severity: catalog.SeverityInfo, Message: "A local preview does not establish server acceptance, approval, trust or publication eligibility."})
	plan.Preview.Findings = findings.Findings()
	for _, finding := range plan.Preview.Findings {
		if finding.Severity == catalog.SeverityError {
			plan.Preview.LocallyValid = false
		}
	}
	var listing map[string]any
	if !resource.contentOnly {
		var assets []importAsset
		listing, assets, err = prepareImportListing(ctx, root, resource.Listing)
		if err != nil {
			return importPlan{}, err
		}
		plan.Preview.Assets = append(plan.Preview.Assets, assets...)
	}
	requests := []struct {
		schema   string
		path     string
		body     any
		bindings []string
	}{
		{"CreateResource", "/api/v1/resources", map[string]any{"namespaceId": resource.NamespaceID, "canonicalIdentifier": resource.CanonicalIdentifier, "kind": resource.Kind}, nil},
		{"CreateVersion", "/api/v1/resources/{resourceId}/versions", catalog.CreateVersionInput{Version: resource.Version, Representations: inputs}, []string{"path.resourceId: Resource response"}},
		{"CreateListing", "/api/v1/catalog-listings", map[string]any{"namespaceId": resource.NamespaceID, "resourceId": importPlaceholderID}, []string{"body.resourceId: Resource response"}},
		{"CreateListingRevision", "/api/v1/catalog-listings/{listingId}/revisions", listing, []string{"path.listingId: Listing response", "body.versionRevisionId: Version response"}},
	}
	for index, request := range requests {
		if resource.contentOnly && index >= 2 {
			break
		}
		if err := ctx.Err(); err != nil {
			return importPlan{}, safeerr.Wrap(err, "import preview was cancelled or exceeded its deadline")
		}
		body, err := json.Marshal(request.body)
		if err != nil {
			return importPlan{}, safeerr.Wrap(err, "cannot encode import request")
		}
		value, err := importJSONObject(body)
		if err != nil || contract.Components.Schemas[request.schema].Value.VisitJSON(value, openapi3.EnableJSONSchema2020()) != nil {
			return importPlan{}, safeerr.New("prepared import request does not match the existing Control API contract")
		}
		// Existing Resource identity is a validation template, never an upload.
		// Retain its aggregate work bound without applying an HTTP body limit.
		upload := index != 0 || !resource.existingResource
		if upload && len(body) > requestLimit || len(body) > remaining-plan.Preview.PreparedBytes {
			return importPlan{}, safeerr.New("encoded import requests exceed the selected request or aggregate byte limit; JSON and base64 overhead count")
		}
		fields := make([]string, 0, len(value))
		for name := range value {
			fields = append(fields, name)
		}
		slices.Sort(fields)
		command := importCommandPreview{Method: "POST", Path: request.path, BodyFields: fields, BodyBytes: len(body), BodyDigest: importDigest(body), DigestScope: "encoded-request-body", Bindings: request.bindings}
		if index >= 2 {
			command.DigestScope = "request-template-body; derived UUIDs use a fixed placeholder"
		}
		if index == 3 {
			command.IfMatch = "ETag from Listing creation response"
		}
		plan.Preview.Commands = append(plan.Preview.Commands, command)
		plan.Preview.PreparedBytes += len(body)
		plan.Bodies = append(plan.Bodies, body)
	}
	return plan, nil
}

func prepareImportRepresentation(ctx context.Context, root *os.Root, kind catalog.Kind, source importRepresentation) (catalog.RepresentationInput, importPreparedEvidence, []importAsset, catalog.ValidationResult, error) {
	input := catalog.RepresentationInput{Role: source.Role, MediaType: strings.TrimSpace(source.MediaType), ProfileID: source.ProfileID, URL: source.URL, Handoff: source.Handoff, ExpectedDigest: source.ExpectedDigest}
	evidence := importPreparedEvidence{Role: input.Role, MediaType: input.MediaType, LocalValidation: "checked"}
	assets := make([]importAsset, 0, 1)
	result := catalog.ValidationResult{}
	invalid := safeerr.New("representation needs a valid role/media type and exactly one compatible source; Plugin input requires an HTTPS archive")
	fail := func(err error) (catalog.RepresentationInput, importPreparedEvidence, []importAsset, catalog.ValidationResult, error) {
		return input, evidence, assets, result, err
	}
	forms := 0
	if source.data != nil {
		forms++
	}
	if source.content != nil {
		forms++
	}
	for _, value := range []string{source.DataFile, source.ContentFile, source.PackageFile, source.PackageDirectory, source.URL} {
		if value != "" {
			forms++
		}
	}
	mediaType, _, err := mime.ParseMediaType(input.MediaType)
	if err != nil || !input.Role.Valid() || forms != 1 || kind == catalog.KindPlugin && (!catalog.IsPluginMediaType(mediaType) || source.URL == "" || source.ProfileID != "" && source.ProfileID != catalog.PluginProfile) || source.ExpectedDigest != nil && kind != catalog.KindPlugin {
		return fail(invalid)
	}
	packaged := source.PackageFile != "" || source.PackageDirectory != ""
	if packaged != (source.Handoff != nil) || packaged && (kind != catalog.KindSkill || mediaType != catalog.SkillPackageMediaZIP && mediaType != catalog.SkillPackageMediaGZIP) || source.PackageDirectory != "" && mediaType != catalog.SkillPackageMediaZIP {
		return fail(safeerr.New("local Skill archives require packageData and immutable handoff; packageDirectory produces ZIP only"))
	}
	if source.URL != "" {
		if kind == catalog.KindSkill && (mediaType == catalog.SkillPackageMediaZIP || mediaType == catalog.SkillPackageMediaGZIP) {
			return fail(safeerr.New("complete Skills require local packageFile or packageDirectory with an immutable handoff"))
		}
		if !catalog.ValidExternalHTTPSURL(source.URL) {
			return fail(safeerr.New("import representation URL must be a credential-free HTTPS reference"))
		}
		probe := adapter.NewRegistry().Validate(ctx, kind, input)
		if probe.ProfileID == adapter.ProfileUnknown {
			return fail(safeerr.New("import representation media type is not supported for its kind"))
		}
		evidence.ProfileID, evidence.LocalValidation = probe.ProfileID, "pending-server-retrieval"
		return input, evidence, assets, result, nil
	}
	name := source.DataFile
	switch {
	case source.ContentFile != "":
		name = source.ContentFile
	case source.PackageFile != "":
		name = source.PackageFile
	case source.PackageDirectory != "":
		name = source.PackageDirectory
	}
	var raw []byte
	switch {
	case source.data != nil:
		raw = source.data
	case source.content != nil:
		raw = []byte(*source.content)
	case source.PackageDirectory != "":
		raw, err = importSkillDirectory(ctx, root, name)
	default:
		raw, err = readImportAsset(ctx, root, name, catalog.MaxCatalogDocumentBytes)
	}
	if err != nil {
		return fail(err)
	}
	scope := "file-bytes"
	if source.PackageDirectory != "" {
		scope = "generated-zip-bytes; includes admitted files and executable modes"
	}
	if source.data == nil && source.content == nil {
		assets = append(assets, importAsset{Path: name, Bytes: len(raw), Digest: importDigest(raw), Scope: scope})
	}
	switch {
	case source.DataFile != "" || source.data != nil:
		if _, err = importJSONObject(raw); err != nil {
			return fail(safeerr.New("dataFile must contain one unambiguous UTF-8 JSON object"))
		}
		input.Data = json.RawMessage(raw)
	case source.ContentFile != "" || source.content != nil:
		if !utf8.Valid(raw) || bytes.ContainsRune(raw, 0) || len(raw) == 0 {
			return fail(safeerr.New("contentFile must contain nonempty UTF-8 text without NUL"))
		}
		input.Content = string(raw)
	case packaged:
		input.PackageData = raw
		handoff, err := catalog.NormalizeSkillHandoff(*source.Handoff, "")
		if err != nil {
			return fail(safeerr.New("Skill archive handoff must identify immutable Git or OCI content"))
		}
		input.Handoff = &handoff
	}
	result = adapter.NewRegistry().Validate(ctx, kind, input)
	evidence.ProfileID = result.ProfileID
	if result.Package != nil {
		evidence.PackageFiles = len(result.Package.Files)
		for _, file := range result.Package.Files {
			if file.Executable {
				evidence.ExecutableFiles++
			}
		}
	}
	return input, evidence, assets, result, nil
}

func prepareImportListing(ctx context.Context, root *os.Root, listing importListing) (map[string]any, []importAsset, error) {
	assets := []importAsset{}
	if listing.ExtensionsFile != "" {
		if len(listing.Extensions) != 0 {
			return nil, nil, safeerr.New("listing accepts extensions or extensionsFile, not both")
		}
		raw, err := readImportAsset(ctx, root, listing.ExtensionsFile, importMaxPreparedBytes)
		if err != nil {
			return nil, nil, err
		}
		listing.Extensions = raw
		assets = append(assets, importAsset{Path: listing.ExtensionsFile, Bytes: len(raw), Digest: importDigest(raw), Scope: "file-bytes"})
		listing.ExtensionsFile = ""
	}
	if len(listing.Extensions) != 0 {
		extensions, err := importJSONObject(listing.Extensions)
		if err != nil {
			return nil, nil, safeerr.New("listing extensions must be one unambiguous JSON object")
		}
		if value, present := extensions[artwork.IRI]; present {
			raw, err := json.Marshal(value)
			if err != nil {
				return nil, nil, safeerr.Wrap(err, "cannot encode listing artwork input")
			}
			if _, err := artwork.DecodeInput(raw, artwork.HardLimits()); err != nil {
				return nil, nil, safeerr.New("listing artwork input is structurally invalid; server media policy remains authoritative")
			}
		}
	}
	for _, value := range []string{listing.DocumentationURL, listing.SupportURL, listing.TermsURL} {
		if value != "" && !catalog.ValidDocumentationHTTPSURL(value) {
			return nil, nil, safeerr.New("listing links must be safe HTTPS references")
		}
	}
	if strings.TrimSpace(listing.DisplayName) == "" || strings.TrimSpace(listing.Description) == "" {
		return nil, nil, safeerr.New("listing displayName and description must not be blank")
	}
	for _, values := range [][]string{listing.Tags, listing.Categories} {
		for _, value := range values {
			if strings.ContainsRune(value, 0) {
				return nil, nil, safeerr.New("listing tags and categories must not contain NUL")
			}
		}
	}
	for _, query := range listing.RepresentativeQueries {
		query = strings.Join(strings.Fields(query), " ")
		if utf8.RuneCountInString(query) < 3 || utf8.RuneCountInString(query) > 240 {
			return nil, nil, safeerr.New("normalized representative queries must contain 3-240 characters")
		}
	}
	encoded, err := json.Marshal(listing)
	if err != nil {
		return nil, nil, safeerr.Wrap(err, "cannot encode import listing")
	}
	value, err := importJSONObject(encoded)
	if err == nil {
		value["versionRevisionId"] = importPlaceholderID
	}
	return value, assets, safeerr.Wrap(err, "cannot encode import listing")
}

func importJSONObject(raw []byte) (map[string]any, error) {
	if err := jsondoc.Validate(raw); err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value map[string]any
	if err := decoder.Decode(&value); err != nil || value == nil {
		return nil, safeerr.New("JSON object required")
	}
	return value, nil
}
