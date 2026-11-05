// Skill package contracts validate retained file inventories and handoff
// evidence without fetching or executing package content.

package catalog

import (
	"errors"
	"net/netip"
	"net/url"
	"path"
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	SkillPackageMediaZIP  = "application/agent-skills+zip"
	SkillPackageMediaGZIP = "application/agent-skills+gzip"

	// MaxSkillPackageBytes bounds transient archive bytes accepted for
	// inspection. The configured HTTP request limit, including base64 and JSON
	// overhead, may impose a lower effective limit.
	MaxSkillPackageBytes = 16 << 20
	// MaxSkillPackageExpandedBytes is the combined regular-file byte budget.
	MaxSkillPackageExpandedBytes = 64 << 20
	MaxSkillPackageFileBytes     = 16 << 20
	MaxSkillPackageFiles         = 1024
	// MaxSkillPackageDepth counts path segments below the Skill root.
	MaxSkillPackageDepth          = 16
	MaxSkillPackageExpansionRatio = 100
	MaxSkillPackagePathBytes      = 1024
)

type PackageAssessmentState string

const (
	PackageAssessmentNotAssessed PackageAssessmentState = "not_assessed"
	PackageAssessmentPassed      PackageAssessmentState = "passed"
	PackageAssessmentFailed      PackageAssessmentState = "failed"
	PackageAssessmentUnavailable PackageAssessmentState = "unavailable"
)

// Valid reports whether the assessment is one of the four supported outcomes;
// it does not imply that an assessment ran.
func (s PackageAssessmentState) Valid() bool {
	return s == PackageAssessmentNotAssessed || s == PackageAssessmentPassed || s == PackageAssessmentFailed || s == PackageAssessmentUnavailable
}

type SkillPackageAssessments struct {
	Malware    PackageAssessmentState `json:"malware"`
	Secrets    PackageAssessmentState `json:"secrets"`
	License    PackageAssessmentState `json:"license"`
	SBOM       PackageAssessmentState `json:"sbom"`
	Provenance PackageAssessmentState `json:"provenance"`
}

// NotAssessedSkillPackageAssessments marks every deferred security and rights
// assessment as not assessed.
func NotAssessedSkillPackageAssessments() SkillPackageAssessments {
	return SkillPackageAssessments{
		Malware:    PackageAssessmentNotAssessed,
		Secrets:    PackageAssessmentNotAssessed,
		License:    PackageAssessmentNotAssessed,
		SBOM:       PackageAssessmentNotAssessed,
		Provenance: PackageAssessmentNotAssessed,
	}
}

type SkillPackageFile struct {
	Path       string `json:"path"`
	Size       int64  `json:"size"`
	MediaType  string `json:"mediaType"`
	Digest     string `json:"digest"`
	Executable bool   `json:"executable"`
}

type SkillPackageInspection struct {
	StructureState PackageAssessmentState  `json:"structureState"`
	RootDirectory  string                  `json:"rootDirectory,omitempty"`
	ExpandedSize   int64                   `json:"expandedSize"`
	FileCount      int                     `json:"fileCount"`
	Files          []SkillPackageFile      `json:"files,omitempty"`
	Assessments    SkillPackageAssessments `json:"assessments"`
}

type GitHandoffReference struct {
	Repository string `json:"repository"`
	Commit     string `json:"commit"`
	Directory  string `json:"directory"`
}

type OCIHandoffReference struct {
	Reference string `json:"reference"`
}

type SkillHandoff struct {
	Git *GitHandoffReference `json:"git,omitempty"`
	OCI *OCIHandoffReference `json:"oci,omitempty"`
}

var (
	fullGitCommitPattern = regexp.MustCompile(`^(?:[a-f0-9]{40}|[a-f0-9]{64})$`)
	sha256DigestPattern  = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)
	ociSegmentPattern    = regexp.MustCompile(`^[a-z0-9]+(?:(?:[._]|__|-+)[a-z0-9]+)*$`)
)

// ValidSkillName reports whether a name satisfies the pinned Skill alphabet,
// length and hyphen rules.
func ValidSkillName(value string) bool {
	if len(value) < 1 || len(value) > 64 || !utf8.ValidString(value) {
		return false
	}
	previousHyphen := false
	for index, character := range value {
		if character == '-' {
			if index == 0 || index == len(value)-1 || previousHyphen {
				return false
			}
			previousHyphen = true
			continue
		}
		if character < 'a' || character > 'z' {
			if character < '0' || character > '9' {
				return false
			}
		}
		previousHyphen = false
	}
	return true
}

// NormalizeSkillPackagePath applies the platform-neutral archive path rules
// used by both inspection and repository validation. Backslashes and drive
// separators are rejected rather than reinterpreted by a later extractor.
func NormalizeSkillPackagePath(value string) (string, bool) {
	if value == "" || len(value) > MaxSkillPackagePathBytes || !utf8.ValidString(value) || strings.ContainsRune(value, '\x00') || strings.ContainsRune(value, '\\') || path.IsAbs(value) {
		return "", false
	}
	rawSegments := strings.Split(value, "/")
	for _, segment := range rawSegments {
		if segment == "" || segment == "." || segment == ".." {
			return "", false
		}
	}
	normalized := path.Clean(value)
	if normalized != value || normalized == "." || normalized == ".." || strings.HasPrefix(normalized, "../") {
		return "", false
	}
	segments := strings.Split(normalized, "/")
	if len(segments) > MaxSkillPackageDepth+1 {
		return "", false
	}
	for _, segment := range segments {
		if segment == "" || segment == "." || segment == ".." || len(segment) > 255 || strings.ContainsRune(segment, ':') || strings.HasSuffix(segment, ".") || strings.HasSuffix(segment, " ") {
			return "", false
		}
		for _, character := range segment {
			if unicode.IsControl(character) {
				return "", false
			}
		}
	}
	return normalized, true
}

// NormalizeSkillHandoff requires at least one immutable Git or OCI handoff and
// checks a Git directory against the inspected root when expectedRoot is set.
func NormalizeSkillHandoff(input SkillHandoff, expectedRoot string) (SkillHandoff, error) {
	if input.Git == nil && input.OCI == nil {
		return SkillHandoff{}, errors.New("at least one immutable Git or OCI handoff reference is required")
	}
	var result SkillHandoff
	if input.Git != nil {
		repository, err := normalizeGitRepository(input.Git.Repository)
		if err != nil {
			return SkillHandoff{}, err
		}
		commit := strings.ToLower(strings.TrimSpace(input.Git.Commit))
		if !fullGitCommitPattern.MatchString(commit) {
			return SkillHandoff{}, errors.New("git handoff commit must be a complete 40- or 64-character hexadecimal object ID")
		}
		directory, ok := NormalizeSkillPackagePath(strings.TrimSpace(input.Git.Directory))
		if !ok {
			return SkillHandoff{}, errors.New("git handoff directory must be a normalized relative package path")
		}
		if expectedRoot != "" && path.Base(directory) != expectedRoot {
			return SkillHandoff{}, errors.New("git handoff directory must end with the validated Skill root directory")
		}
		result.Git = &GitHandoffReference{Repository: repository, Commit: commit, Directory: directory}
	}
	if input.OCI != nil {
		reference, err := normalizeOCIReference(input.OCI.Reference)
		if err != nil {
			return SkillHandoff{}, err
		}
		result.OCI = &OCIHandoffReference{Reference: reference}
	}
	return result, nil
}

// normalizeGitRepository validates and canonicalizes the external HTTPS
// repository reference retained for handoff.
func normalizeGitRepository(value string) (string, error) {
	value = strings.TrimSpace(value)
	if !ValidExternalHTTPSURL(value) {
		return "", errors.New("git handoff repository must be a credential-free HTTPS URL")
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.ForceQuery || strings.ContainsAny(value, "?#") || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.RawPath != "" || strings.Contains(parsed.EscapedPath(), "%") || parsed.Path == "" || parsed.Path == "/" {
		return "", errors.New("git handoff repository must not contain a query, fragment, encoded path, or empty repository path")
	}
	repositoryPath := strings.TrimSuffix(parsed.Path, "/")
	if repositoryPath == "" || repositoryPath == "/" || path.Clean(repositoryPath) != repositoryPath {
		return "", errors.New("git handoff repository path must not contain empty, dot, or traversal segments")
	}
	parsed.Scheme = "https"
	parsed.Host = strings.ToLower(parsed.Host)
	parsed.Path = repositoryPath
	return parsed.String(), nil
}

// normalizeOCIReference requires an explicit registry and immutable digest
// reference rather than a mutable image tag.
func normalizeOCIReference(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 2048 || strings.ContainsAny(value, "\r\n\x00") || strings.Contains(value, "://") || strings.ContainsAny(value, "?#") {
		return "", errors.New("oci handoff reference is invalid")
	}
	name, digest, found := strings.Cut(value, "@")
	if !found || strings.Contains(digest, "@") || !sha256DigestPattern.MatchString(digest) {
		return "", errors.New("oci handoff reference must be pinned by a lower-case sha256 digest")
	}
	slash := strings.IndexByte(name, '/')
	if slash <= 0 || slash == len(name)-1 {
		return "", errors.New("oci handoff reference must include an explicit registry and repository")
	}
	host, repository := name[:slash], name[slash+1:]
	parsed, err := url.Parse("https://" + host + "/" + repository)
	if err != nil || parsed.User != nil || parsed.Host != host || parsed.Path != "/"+repository || parsed.Hostname() == "" || strings.HasSuffix(host, ":") {
		return "", errors.New("oci handoff registry is invalid")
	}
	port := parsed.Port()
	if port != "" {
		portNumber, parseErr := strconv.ParseUint(port, 10, 16)
		if parseErr != nil || portNumber == 0 {
			return "", errors.New("oci handoff registry port is invalid")
		}
	}
	for _, character := range parsed.Hostname() {
		if character > 127 || character == '%' {
			return "", errors.New("oci handoff registry must use an ASCII host")
		}
	}
	if !validExplicitOCIRegistry(parsed.Hostname(), port) {
		return "", errors.New("oci handoff reference must include an unambiguous explicit registry")
	}
	if repository != strings.ToLower(repository) || strings.ContainsRune(repository, ':') {
		return "", errors.New("oci handoff repository must be lower-case and must not contain a mutable tag")
	}
	for _, segment := range strings.Split(repository, "/") {
		if !ociSegmentPattern.MatchString(segment) {
			return "", errors.New("oci handoff repository path is invalid")
		}
	}
	return strings.ToLower(host) + "/" + repository + "@" + digest, nil
}

// validExplicitOCIRegistry checks a declared registry hostname and optional
// nonzero port.
func validExplicitOCIRegistry(hostname, port string) bool {
	if hostname == "" {
		return false
	}
	if address, err := netip.ParseAddr(hostname); err == nil && address.Zone() == "" {
		return true
	}
	if len(hostname) > 253 || strings.HasPrefix(hostname, ".") || strings.HasSuffix(hostname, ".") {
		return false
	}
	for _, label := range strings.Split(hostname, ".") {
		if len(label) < 1 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, character := range label {
			if character != '-' && (character < '0' || character > '9') && (character < 'A' || character > 'Z') && (character < 'a' || character > 'z') {
				return false
			}
		}
	}
	return port != "" || strings.EqualFold(hostname, "localhost") || strings.ContainsRune(hostname, '.')
}

// ValidatePackageRepresentation protects repository implementations from
// accepting package metadata that bypassed the adapter.
func ValidatePackageRepresentation(representation Representation) error {
	if representation.Plugin != nil || IsPluginMediaType(representation.MediaType) {
		if representation.Package != nil || representation.Handoff != nil || representation.ArtifactID != "" || len(representation.Data) != 0 || !ValidExternalHTTPSURL(representation.URL) || representation.ProfileID != PluginProfile || !sha256DigestPattern.MatchString(representation.ContentDigest) || representation.Size <= 0 || representation.Size > MaxSkillPackageBytes || !IsPluginMediaType(representation.MediaType) {
			return E(CodeInvalidArgument, "Plugin representation metadata is invalid")
		}
		if representation.Plugin != nil && representation.Plugin.ExpandedSize > representation.Size*MaxSkillPackageExpansionRatio {
			return E(CodeInvalidArgument, "Plugin expansion ratio is invalid")
		}
		return ValidatePluginInspection(representation.Plugin)
	}
	if representation.Package == nil {
		if representation.Handoff != nil {
			return E(CodeInvalidArgument, "a Skill handoff requires inspected package metadata")
		}
		return nil
	}
	if representation.MediaType != SkillPackageMediaZIP && representation.MediaType != SkillPackageMediaGZIP || representation.ContentDigest == "" || !sha256DigestPattern.MatchString(representation.ContentDigest) || representation.Size <= 0 || representation.Size > MaxSkillPackageBytes || len(representation.Data) != 0 || representation.URL != "" || representation.ArtifactID != "" || representation.Handoff == nil {
		return E(CodeInvalidArgument, "Skill package representation metadata is invalid")
	}
	expectedRoot := ""
	if representation.ValidationState == "valid" && representation.Package.StructureState == PackageAssessmentPassed {
		expectedRoot = representation.Package.RootDirectory
	}
	normalizedHandoff, err := NormalizeSkillHandoff(*representation.Handoff, expectedRoot)
	if err != nil || !sameSkillHandoff(normalizedHandoff, *representation.Handoff) {
		return E(CodeInvalidArgument, "Skill package handoff metadata is invalid")
	}
	assessments := representation.Package.Assessments
	if !assessments.Malware.Valid() || !assessments.Secrets.Valid() || !assessments.License.Valid() || !assessments.SBOM.Valid() || !assessments.Provenance.Valid() {
		return E(CodeInvalidArgument, "Skill package assessment state is invalid")
	}
	if representation.ValidationState == "valid" && (assessments.Malware == PackageAssessmentFailed || assessments.Secrets == PackageAssessmentFailed || assessments.License == PackageAssessmentFailed || assessments.SBOM == PackageAssessmentFailed || assessments.Provenance == PackageAssessmentFailed) {
		return E(CodeInvalidArgument, "Skill package with a failed assessment cannot be valid")
	}
	if representation.Package.StructureState == PackageAssessmentFailed {
		if representation.Package.RootDirectory != "" || representation.Package.ExpandedSize != 0 || representation.Package.FileCount != 0 || len(representation.Package.Files) != 0 || representation.ValidationState == "valid" {
			return E(CodeInvalidArgument, "failed Skill package inspection contains a claimed inventory")
		}
		return nil
	}
	if representation.Package.StructureState != PackageAssessmentPassed || !ValidSkillName(representation.Package.RootDirectory) || representation.Package.ExpandedSize < 1 || representation.Package.ExpandedSize > MaxSkillPackageExpandedBytes || representation.Package.ExpandedSize > representation.Size*MaxSkillPackageExpansionRatio || representation.Package.FileCount != len(representation.Package.Files) || representation.Package.FileCount < 1 || representation.Package.FileCount > MaxSkillPackageFiles {
		return E(CodeInvalidArgument, "Skill package inventory summary is invalid")
	}
	var expandedSize int64
	foundSkill := false
	previousPath := ""
	foldedFiles := make(map[string]string, len(representation.Package.Files))
	foldedDirectories := make(map[string]string)
	for _, file := range representation.Package.Files {
		normalized, ok := NormalizeSkillPackagePath(file.Path)
		if !ok || normalized != file.Path || !strings.HasPrefix(file.Path, representation.Package.RootDirectory+"/") {
			return E(CodeInvalidArgument, "Skill package file inventory path is invalid")
		}
		if file.Path <= previousPath {
			return E(CodeInvalidArgument, "Skill package file inventory order is invalid")
		}
		folded := strings.ToLower(file.Path)
		if existing, exists := foldedFiles[folded]; exists && existing != file.Path {
			return E(CodeInvalidArgument, "Skill package file inventory contains case-conflicting paths")
		}
		if _, exists := foldedDirectories[folded]; exists {
			return E(CodeInvalidArgument, "Skill package file inventory contains a file and path-parent conflict")
		}
		for ancestor := path.Dir(file.Path); ancestor != "."; ancestor = path.Dir(ancestor) {
			foldedAncestor := strings.ToLower(ancestor)
			if _, exists := foldedFiles[foldedAncestor]; exists {
				return E(CodeInvalidArgument, "Skill package file inventory contains a file and path-parent conflict")
			}
			if existing, exists := foldedDirectories[foldedAncestor]; exists && existing != ancestor {
				return E(CodeInvalidArgument, "Skill package file inventory contains case-conflicting paths")
			}
			foldedDirectories[foldedAncestor] = ancestor
		}
		foldedFiles[folded] = file.Path
		if file.Size < 0 || file.Size > MaxSkillPackageFileBytes {
			return E(CodeInvalidArgument, "Skill package file inventory size is invalid")
		}
		if !sha256DigestPattern.MatchString(file.Digest) {
			return E(CodeInvalidArgument, "Skill package file inventory digest is invalid")
		}
		if !validRepresentationMediaType(file.MediaType) {
			return E(CodeInvalidArgument, "Skill package file inventory media type is invalid")
		}
		if file.Size > MaxSkillPackageExpandedBytes-expandedSize {
			return E(CodeInvalidArgument, "Skill package expanded bytes exceed the supported limit")
		}
		expandedSize += file.Size
		foundSkill = foundSkill || file.Path == representation.Package.RootDirectory+"/SKILL.md"
		previousPath = file.Path
	}
	if !foundSkill || expandedSize != representation.Package.ExpandedSize || expandedSize > MaxSkillPackageExpandedBytes {
		return E(CodeInvalidArgument, "Skill package inventory is incomplete")
	}
	return nil
}

// ValidatePackageRevisionDigest binds the full package inventory,
// assessment states, and immutable handoff to the VersionRevision digest. It
// is a no-op for revisions without a package representation so historical
// validation profiles keep their original digest contract.
func ValidatePackageRevisionDigest(revision VersionRevision) error {
	hasPackage := false
	var archiveBytes, retainedBytes int64
	for _, representation := range revision.Representations {
		hasPackage = hasPackage || representation.Package != nil || representation.Plugin != nil
		if representation.Plugin != nil {
			archiveBytes += representation.Size
			for _, document := range representation.Plugin.Documents {
				retainedBytes += int64(len(document.Content))
			}
		}
	}
	if archiveBytes > MaxCatalogDocumentBytes || retainedBytes > MaxCatalogDocumentBytes {
		return E(CodeInvalidArgument, "Plugin revision exceeds its byte budget")
	}
	if !hasPackage {
		return nil
	}
	digest, err := contentSetDigest(revision.Representations)
	if err != nil || digest != revision.ContentSetDigest {
		return E(CodeInvalidArgument, "package revision digest is invalid")
	}
	return nil
}

// sameSkillHandoff compares normalized immutable Git and OCI handoff bindings.
func sameSkillHandoff(left, right SkillHandoff) bool {
	if (left.Git == nil) != (right.Git == nil) || (left.OCI == nil) != (right.OCI == nil) {
		return false
	}
	if left.Git != nil && *left.Git != *right.Git {
		return false
	}
	return left.OCI == nil || *left.OCI == *right.OCI
}
