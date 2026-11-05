// Plugin domain validation checks retained manifest/component evidence against
// its archive-bound inventory and immutable representation metadata.

package catalog

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"path"
	"strings"
	"unicode/utf8"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/jsondoc"
)

const (
	PluginMediaZIP           = "application/agent-plugins+zip"
	PluginMediaGZIP          = "application/agent-plugins+gzip"
	PluginProfile            = "agent-plugins-1.0.0-1fc1b627"
	MaxPluginComponents      = 128
	MaxPluginDocumentBytes   = 500_000
	MaxPluginInspectionBytes = 20 << 20
)

// PluginInspection retains validation inputs and file identities, never the
// downloadable archive or executable file contents.
type PluginInspection struct {
	Directories   []string                `json:"directories,omitempty"`
	RootDirectory string                  `json:"rootDirectory"`
	ExpandedSize  int64                   `json:"expandedSize"`
	Files         []PluginFile            `json:"files"`
	Documents     []PluginDocument        `json:"documents"`
	Components    []PluginComponent       `json:"components,omitempty"`
	Assessments   SkillPackageAssessments `json:"assessments"`
}

type PluginFile struct {
	Path       string `json:"path"`
	Size       int64  `json:"size"`
	Digest     string `json:"digest"`
	Executable bool   `json:"executable"`
}

type PluginDocument struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

type PluginComponent struct {
	Kind     string    `json:"kind"`
	Name     string    `json:"name"`
	Path     string    `json:"path"`
	Findings []Finding `json:"findings,omitempty"`
}

// IsPluginMediaType reports whether the parsed media type identifies the native
// Plugin archive contract.
func IsPluginMediaType(mediaType string) bool {
	return mediaType == PluginMediaZIP || mediaType == PluginMediaGZIP
}

// PluginResponseMediaType permits generic archive hosting without relaxing
// ordinary standards-document media matching. Bytes still require inspection.
func PluginResponseMediaType(declared, actual string) bool {
	return IsPluginMediaType(declared) && (actual == declared || actual == "application/octet-stream" || declared == PluginMediaZIP && actual == "application/zip" || declared == PluginMediaGZIP && actual == "application/gzip")
}

// PluginExpectedDigest reads the Catalog's flat, scalar discovery extension.
// Missing identity is allowed; a partial or malformed identity is not trusted
// as an adoption constraint. Size is descriptive and digest verifies bytes.
func PluginExpectedDigest(raw json.RawMessage) (*string, error) {
	var metadata map[string]json.RawMessage
	if len(raw) == 0 {
		return nil, nil //nolint:nilnil // An absent optional checksum is a valid adoption input.
	}
	if jsondoc.Validate(raw) != nil {
		return nil, E(CodeValidationFailed, "Plugin archive metadata is ambiguous")
	}
	if err := json.Unmarshal(raw, &metadata); err != nil {
		return nil, E(CodeValidationFailed, "Plugin archive metadata is invalid")
	}
	d, hasDigest := metadata["packageDigest"]
	s, hasSize := metadata["packageSize"]
	if !hasDigest && !hasSize {
		return nil, nil //nolint:nilnil // Unrelated discovery metadata supplies no checksum constraint.
	}
	var digest string
	var size int64
	if !hasDigest || !hasSize || json.Unmarshal(d, &digest) != nil || json.Unmarshal(s, &size) != nil || !sha256DigestPattern.MatchString(digest) || size < 1 || size > MaxSkillPackageBytes {
		return nil, E(CodeValidationFailed, "Plugin archive identity is incomplete or invalid")
	}
	return &digest, nil
}

// ValidatePluginInspection checks retained byte identities and bounded shape.
// Revalidation reruns the format adapter; inventory is not a trust assertion.
func ValidatePluginInspection(p *PluginInspection) error {
	if p == nil || len(p.Files) == 0 || len(p.Files) > MaxSkillPackageFiles || len(p.Documents) == 0 || len(p.Documents) > MaxPluginComponents+2 || len(p.Components) > MaxPluginComponents || p.ExpandedSize < 1 || p.ExpandedSize > MaxSkillPackageExpandedBytes || p.Assessments != NotAssessedSkillPackageAssessments() {
		return E(CodeValidationFailed, "Plugin inspection is incomplete or exceeds limits")
	}
	if p.RootDirectory != "" {
		if normalized, ok := NormalizeSkillPackagePath(p.RootDirectory); !ok || normalized != p.RootDirectory || strings.Contains(normalized, "/") {
			return E(CodeValidationFailed, "Plugin root is invalid")
		}
	}
	if len(p.Directories) > MaxSkillPackageFiles*MaxSkillPackageDepth {
		return E(CodeValidationFailed, "Plugin directory inventory exceeds limits")
	}
	prefix := ""
	if p.RootDirectory != "" {
		prefix = p.RootDirectory + "/"
	}
	paths := make(map[string]string)
	previousDirectory := ""
	for _, directory := range p.Directories {
		if normalized, ok := NormalizeSkillPackagePath(directory); !ok || normalized != directory || directory <= previousDirectory || prefix != "" && directory != p.RootDirectory && !strings.HasPrefix(directory, prefix) {
			return E(CodeValidationFailed, "Plugin directory inventory is invalid")
		}
		folded := strings.ToLower(directory)
		if _, exists := paths[folded]; exists {
			return E(CodeValidationFailed, "Plugin paths collide")
		}
		paths[folded] = directory
		previousDirectory = directory
	}
	files := make(map[string]PluginFile, len(p.Files))
	var expanded, retained int64
	previous := ""
	for _, file := range p.Files {
		if normalized, ok := NormalizeSkillPackagePath(file.Path); !ok || normalized != file.Path || prefix != "" && !strings.HasPrefix(file.Path, prefix) {
			return E(CodeValidationFailed, "Plugin inventory path is invalid")
		}
		folded := strings.ToLower(file.Path)
		if _, exists := paths[folded]; exists {
			return E(CodeValidationFailed, "Plugin paths collide")
		}
		paths[folded] = file.Path
		if file.Path <= previous || file.Path == "" || strings.HasPrefix(file.Path, "/") || strings.Contains(file.Path, "\\") || file.Size < 0 || file.Size > MaxSkillPackageFileBytes || !sha256DigestPattern.MatchString(file.Digest) {
			return E(CodeValidationFailed, "Plugin inventory is invalid")
		}
		for _, segment := range strings.Split(file.Path, "/") {
			if segment == "" || segment == "." || segment == ".." {
				return E(CodeValidationFailed, "Plugin inventory path is invalid")
			}
		}
		files[file.Path] = file
		previous = file.Path
		expanded += file.Size
	}
	for name := range files {
		for parent := path.Dir(name); parent != "."; parent = path.Dir(parent) {
			if _, exists := files[parent]; exists {
				return E(CodeValidationFailed, "Plugin file is used as a directory")
			}
			if paths[strings.ToLower(parent)] != parent {
				return E(CodeValidationFailed, "Plugin parent directory is missing or ambiguous")
			}
		}
	}
	if expanded != p.ExpandedSize {
		return E(CodeValidationFailed, "Plugin inventory size is inconsistent")
	}
	seen := make(map[string]bool)
	for _, doc := range p.Documents {
		relative := strings.TrimPrefix(doc.Path, prefix)
		validationDocument := relative == "plugin.json" || relative == "mcp.json" || strings.HasPrefix(relative, "skills/") && len(strings.Split(relative, "/")) == 3 && path.Base(relative) == "SKILL.md"
		file, ok := files[doc.Path]
		sum := sha256.Sum256([]byte(doc.Content))
		if !validationDocument || !ok || seen[doc.Path] || !utf8.ValidString(doc.Content) || strings.ContainsRune(doc.Content, 0) || len(doc.Content) > MaxPluginDocumentBytes || file.Size != int64(len(doc.Content)) || file.Digest != "sha256:"+hex.EncodeToString(sum[:]) {
			return E(CodeValidationFailed, "Plugin retained document is invalid")
		}
		seen[doc.Path] = true
		retained += int64(len(doc.Content))
	}
	if retained > MaxCatalogDocumentBytes {
		return E(CodeValidationFailed, "Plugin retained documents exceed the revision budget")
	}
	if !seen[prefix+"plugin.json"] {
		return E(CodeValidationFailed, "Plugin manifest is not retained")
	}
	for _, component := range p.Components {
		if (component.Kind != "skill" && component.Kind != "mcp_server") || !seen[component.Path] || !utf8.ValidString(component.Name) || strings.ContainsRune(component.Name, 0) || len(component.Findings) > MaxPluginComponents {
			return E(CodeValidationFailed, "Plugin component inventory is invalid")
		}
	}
	for name := range files {
		relative := strings.TrimPrefix(name, prefix)
		if (relative == "mcp.json" || strings.HasPrefix(relative, "skills/") && len(strings.Split(relative, "/")) == 3 && path.Base(relative) == "SKILL.md") && !seen[name] {
			return E(CodeValidationFailed, "Plugin component document is not retained")
		}
	}
	encoded, err := json.Marshal(p)
	if err != nil || pluginInspectionStorageSize(encoded) > MaxPluginInspectionBytes {
		return E(CodeValidationFailed, "Plugin encoded inspection exceeds the storage budget")
	}
	return nil
}

// pluginInspectionStorageSize accounts for JSON escaping and the separator
// spaces in PostgreSQL's jsonb text limit. Go's HTML escaping can only make this
// bound larger. Punctuation inside document strings is not a JSON separator.
func pluginInspectionStorageSize(encoded []byte) int {
	size := len(encoded)
	quoted, escaped := false, false
	for _, b := range encoded {
		if escaped {
			escaped = false
			continue
		}
		switch {
		case quoted && b == '\\':
			escaped = true
		case b == '"':
			quoted = !quoted
		case !quoted && (b == ',' || b == ':'):
			size++
		}
	}
	return size
}
