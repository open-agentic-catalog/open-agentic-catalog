// Plugin inspection binds a portable manifest and declared components to a
// bounded archive inventory without running or installing those components.

package adapter

import (
	"context"
	"encoding/json"
	"net/netip"
	"net/url"
	"path"
	"sort"
	"strings"
	"unicode"

	"golang.org/x/net/http/httpguts"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/jsondoc"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/standards"
)

const (
	pluginManifestName   = "plugin.json"
	pluginMCPName        = "mcp.json"
	pluginManifestPath   = "/plugin.json"
	pluginMCPPath        = "/mcp.json"
	pluginInspectionPath = "/plugin"
)

// InspectPlugin validates an archive without extracting it, following embedded
// links, expanding variables or running a component. Unsafe archives have no
// retainable inspection; bounded format errors remain individual findings.
func InspectPlugin(ctx context.Context, mediaType string, content []byte, limits catalog.DiagnosticLimits) catalog.ValidationResult {
	r := catalog.ValidationResult{ProfileID: catalog.PluginProfile}
	fail := func(code, message string) catalog.ValidationResult {
		r.Findings = []catalog.Finding{{Code: code, Message: message, Severity: catalog.SeverityError, Path: "/url"}}
		return r
	}
	if !catalog.IsPluginMediaType(mediaType) || len(content) == 0 || len(content) > catalog.MaxSkillPackageBytes {
		return fail("PLUGIN_ARCHIVE_LIMIT", "A supported bounded Plugin archive is required.")
	}
	newInventory := func(root *string) *archiveInventory {
		return &archiveInventory{plugin: true, pluginRoot: root, documents: make(map[string][]byte), limits: defaultSkillPackageLimits, archiveSize: int64(len(content)), seen: make(map[string]bool), foldedFiles: make(map[string]string), foldedDirs: make(map[string]string)}
	}
	i := newInventory(nil)
	var err error
	if mediaType == catalog.PluginMediaZIP {
		err = i.inspectZIP(ctx, content)
	} else {
		err = i.inspectGZIP(ctx, content)
	}
	if err != nil {
		return fail("PLUGIN_ARCHIVE_INVALID", "The Plugin archive is unsafe, malformed or exceeds inspection limits.")
	}
	root := ""
	if _, ok := i.seen[pluginManifestName]; !ok {
		for name := range i.seen {
			parts := strings.Split(name, "/")
			if len(parts) == 2 && parts[1] == pluginManifestName {
				if root != "" {
					return fail("PLUGIN_ROOT_AMBIGUOUS", "The Plugin archive must have one unambiguous root.")
				}
				root = parts[0]
			}
		}
		if root == "" {
			return fail("PLUGIN_MANIFEST_REQUIRED", "The Plugin archive requires root plugin.json.")
		}
	}
	prefix := ""
	if root != "" {
		prefix = root + "/"
	}
	for name, directory := range i.seen {
		if root != "" && name != root && !strings.HasPrefix(name, prefix) {
			return fail("PLUGIN_ROOT_AMBIGUOUS", "Archive entries must belong to the selected Plugin root.")
		}
		if name == prefix+pluginManifestName && directory {
			return fail("PLUGIN_MANIFEST_REQUIRED", "plugin.json must be a regular file.")
		}
	}
	// Re-read the same bounded bytes after root discovery. This avoids treating
	// binary or large extension files named mcp.json/SKILL.md as validation input.
	i = newInventory(&root)
	if mediaType == catalog.PluginMediaZIP {
		err = i.inspectZIP(ctx, content)
	} else {
		err = i.inspectGZIP(ctx, content)
	}
	if err != nil {
		return fail("PLUGIN_DOCUMENT_INVALID", "Plugin validation documents exceed inspection limits.")
	}
	p := &catalog.PluginInspection{RootDirectory: root, ExpandedSize: i.expandedSize, Assessments: catalog.NotAssessedSkillPackageAssessments()}
	for _, directory := range i.foldedDirs {
		p.Directories = append(p.Directories, directory)
	}
	sort.Strings(p.Directories)
	for _, file := range i.files {
		p.Files = append(p.Files, catalog.PluginFile{Path: file.Path, Size: file.Size, Digest: file.Digest, Executable: file.Executable})
	}
	sort.Slice(p.Files, func(a, b int) bool { return p.Files[a].Path < p.Files[b].Path })
	for name, body := range i.documents {
		p.Documents = append(p.Documents, catalog.PluginDocument{Path: name, Content: string(body)})
	}
	sort.Slice(p.Documents, func(a, b int) bool { return p.Documents[a].Path < p.Documents[b].Path })
	if err := catalog.ValidatePluginInspection(p); err != nil {
		return fail("PLUGIN_DOCUMENT_INVALID", "Plugin validation documents must be bounded UTF-8 text.")
	}
	return ValidatePluginInspection(ctx, p, limits)
}

// pluginFinding constructs a Plugin error finding at the specified archive or
// component location.
func pluginFinding(code, where, message string) catalog.Finding {
	return catalog.Finding{Code: code, Severity: catalog.SeverityError, Path: where, Message: message}
}

// ValidatePluginInspection reruns format checks against exact retained inputs.
// Archive byte availability/freshness is verified independently by the service.
func ValidatePluginInspection(ctx context.Context, p *catalog.PluginInspection, limits catalog.DiagnosticLimits) (r catalog.ValidationResult) {
	report := catalog.NewFindingCollector(limits)
	defer func() { r.Findings = report.Findings() }()
	r = catalog.ValidationResult{ProfileID: catalog.PluginProfile, Plugin: p, NormalizedFacts: make(map[string]any)}
	add := func(code, where, message string) {
		report.Add(pluginFinding(code, where, message))
	}
	if catalog.ValidatePluginInspection(p) != nil {
		r.Plugin = nil
		add("PLUGIN_INSPECTION_INVALID", pluginInspectionPath, "Plugin inspection is incomplete or inconsistent.")
		return r
	}
	copyInspection := *p
	p = &copyInspection
	r.Plugin = p
	p.Components = nil
	prefix := ""
	if p.RootDirectory != "" {
		prefix = p.RootDirectory + "/"
	}
	for _, file := range p.Files {
		if file.Path == prefix+"skills" {
			add("PLUGIN_COMPONENT_LOCATION", "/skills", "skills must be a directory.")
		}
	}
	for _, directory := range p.Directories {
		if directory == prefix+pluginMCPName {
			add("PLUGIN_COMPONENT_LOCATION", pluginMCPPath, "mcp.json must be a regular file.")
		}
	}
	foundManifest := false
	for _, doc := range p.Documents {
		if report.Exhausted() {
			break
		}
		if ctx.Err() != nil {
			add("PLUGIN_CANCELLED", pluginInspectionPath, "Plugin validation was cancelled.")
			return r
		}
		name := strings.TrimPrefix(doc.Path, prefix)
		body := []byte(doc.Content)
		switch {
		case name == pluginManifestName:
			foundManifest = true
			if jsondoc.Validate(body) != nil || standards.ValidatePluginManifest(body) != nil {
				add("PLUGIN_MANIFEST_INVALID", pluginManifestPath, "The manifest does not satisfy the pinned Plugin 1.0.0 schema.")
				continue
			}
			var manifest map[string]any
			if json.Unmarshal(body, &manifest) != nil {
				continue
			}
			for _, key := range []string{"name", "version", "description"} {
				if v, ok := manifest[key].(string); ok {
					// JSON permits escaped NUL; PostgreSQL text/jsonb does not.
					// Reject the projection without altering the retained input.
					if strings.ContainsRune(v, 0) {
						r.Plugin = nil
						add("PLUGIN_MANIFEST_INVALID", pluginManifestPath, "Manifest metadata contains an unsupported character.")
						return r
					}
					r.NormalizedFacts[key] = v
				}
			}
			if ext, ok := manifest["extensions"].(map[string]any); ok {
				for key := range ext {
					if !pluginNamespace(key) {
						add("PLUGIN_EXTENSION_NAMESPACE", "/plugin.json/extensions", "Extension keys must use reverse-domain namespaces.")
						break
					}
				}
			}
		case name == pluginMCPName:
			if jsondoc.Validate(body) != nil {
				add("PLUGIN_MCP_INVALID", pluginMCPPath, "MCP configuration must be unambiguous JSON.")
				continue
			}
			if standards.ValidatePluginMCP(body) != nil {
				add("PLUGIN_MCP_INVALID", pluginMCPPath, "MCP configuration does not satisfy the pinned Plugin 1.0.0 schema.")
			}
			var config struct {
				Servers map[string]json.RawMessage `json:"mcpServers"`
			}
			if json.Unmarshal(body, &config) != nil {
				continue
			}
			keys := make([]string, 0, len(config.Servers))
			for key := range config.Servers {
				keys = append(keys, key)
			}
			sort.Strings(keys)
			if len(keys) > catalog.MaxPluginComponents {
				add("PLUGIN_COMPONENT_LIMIT", pluginMCPPath, "Plugin contains too many components.")
				continue
			}
			for _, key := range keys {
				if report.Exhausted() {
					break
				}
				findings := validatePluginServer(config.Servers[key])
				if standards.ValidatePluginMCPServer(config.Servers[key]) != nil {
					findings = append(findings, pluginFinding("PLUGIN_MCP_SERVER_SCHEMA", pluginMCPPath, "This server does not satisfy the pinned MCP configuration schema."))
				}
				componentReport := catalog.NewFindingCollector(limits)
				componentReport.AddAll("", findings)
				findings = componentReport.Findings()
				p.Components = append(p.Components, catalog.PluginComponent{Kind: "mcp_server", Name: key, Path: doc.Path, Findings: findings})
				for _, f := range findings {
					add(f.Code, pluginMCPPath, f.Message)
				}
			}
		case strings.HasPrefix(name, "skills/") && len(strings.Split(name, "/")) == 3 && path.Base(name) == skillDocumentName:
			frontmatter, findings := parseSkillFrontmatter(doc.Content)
			if frontmatter["name"] != path.Base(path.Dir(name)) {
				findings = append(findings, pluginFinding("PLUGIN_SKILL_NAME", "/skills", "Skill name must match its immediate parent directory."))
			}
			componentReport := catalog.NewFindingCollector(limits)
			componentReport.AddAll("", findings)
			findings = componentReport.Findings()
			p.Components = append(p.Components, catalog.PluginComponent{Kind: "skill", Name: path.Base(path.Dir(name)), Path: doc.Path, Findings: findings})
			for _, f := range findings {
				add(f.Code, "/"+name, f.Message)
			}
		}
	}
	if !foundManifest {
		add("PLUGIN_MANIFEST_REQUIRED", pluginManifestPath, "The Plugin archive requires root plugin.json.")
	}
	if len(p.Components) > catalog.MaxPluginComponents {
		p.Components = p.Components[:catalog.MaxPluginComponents]
		add("PLUGIN_COMPONENT_LIMIT", pluginInspectionPath, "Plugin contains too many components.")
	}
	// Component summaries and findings are retained too. Recheck the complete
	// inspection so an archive near the storage limit cannot grow past it here.
	if catalog.ValidatePluginInspection(p) != nil {
		r.Plugin = nil
		add("PLUGIN_INSPECTION_INVALID", pluginInspectionPath, "Plugin inspection is incomplete or exceeds storage limits.")
	}
	return r
}

// pluginNamespace reports whether a client extension namespace uses the
// permitted dotted label syntax.
func pluginNamespace(s string) bool {
	parts := strings.Split(s, ".")
	if len(parts) < 2 || len(s) > 253 {
		return false
	}
	for _, part := range parts {
		if part == "" || len(part) > 63 || strings.HasPrefix(part, "-") || strings.HasSuffix(part, "-") {
			return false
		}
		for _, r := range part {
			if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-') {
				return false
			}
		}
	}
	return true
}

// validatePluginServer checks declared MCP transport metadata, contained
// commands and client-owned credential/variable boundaries without execution.
func validatePluginServer(raw []byte) []catalog.Finding {
	var s struct {
		Type    string            `json:"type"`
		Command string            `json:"command"`
		CWD     string            `json:"cwd"`
		URL     string            `json:"url"`
		Headers map[string]string `json:"headers"`
		Env     map[string]string `json:"env"`
	}
	fail := func(code, message string) []catalog.Finding {
		return []catalog.Finding{pluginFinding(code, pluginMCPPath, message)}
	}
	if json.Unmarshal(raw, &s) != nil {
		return fail("PLUGIN_MCP_SERVER", "MCP server configuration is invalid.")
	}
	switch s.Type {
	case "stdio":
		if s.Command == "" || strings.ContainsAny(s.Command, "\\\x00") || strings.IndexFunc(s.Command, unicode.IsSpace) >= 0 || strings.Contains(s.Command, "${") {
			return fail("PLUGIN_MCP_COMMAND", "MCP command must be one bare executable or contained ./ path.")
		}
		if strings.Contains(s.Command, "/") && (!strings.HasPrefix(s.Command, "./") || !pluginContained(strings.TrimPrefix(s.Command, "./"))) {
			return fail("PLUGIN_MCP_COMMAND", "MCP command escapes the package or uses an invalid path form.")
		}
		if s.CWD != "" {
			valid := false
			for _, root := range []string{"./", "${PLUGIN_ROOT}", "${PLUGIN_DATA}"} {
				if s.CWD == root && root != "./" {
					valid = true
					break
				}
				prefix := root
				if root != "./" {
					prefix += "/"
				}
				if strings.HasPrefix(s.CWD, prefix) {
					valid = pluginContained(strings.TrimPrefix(s.CWD, prefix))
					break
				}
			}
			if !valid {
				return fail("PLUGIN_MCP_CWD", "MCP working directory must remain within its declared root.")
			}
		}
	case "streamable-http", "sse":
		u, err := url.Parse(s.URL)
		if err != nil || u.Hostname() == "" || u.User != nil || u.Fragment != "" || (u.Scheme != "https" && u.Scheme != "http") {
			return fail("PLUGIN_MCP_URL", "MCP URL must be absolute HTTP(S) without credentials or a fragment.")
		}
		ip, _ := netip.ParseAddr(u.Hostname())
		if u.Scheme == "http" && u.Hostname() != "localhost" && !ip.IsLoopback() {
			return fail("PLUGIN_MCP_URL", "Non-loopback MCP endpoints require HTTPS.")
		}
		seen := make(map[string]bool)
		for name, value := range s.Headers {
			key := strings.ToLower(name)
			if seen[key] || !httpguts.ValidHeaderFieldName(name) || !httpguts.ValidHeaderFieldValue(value) {
				return fail("PLUGIN_MCP_HEADERS", "MCP headers must be valid and unique ignoring case.")
			}
			seen[key] = true
			if key == "authorization" || key == "proxy-authorization" || key == "cookie" {
				return fail("PLUGIN_CREDENTIAL_HEADER", "Authentication headers must be supplied by the client, not the Plugin.")
			}
		}
	default:
		return fail("PLUGIN_MCP_TRANSPORT", "MCP transport is not supported by Plugin 1.0.0.")
	}
	for key := range s.Env {
		if key == "PLUGIN_ROOT" || key == "PLUGIN_DATA" {
			return fail("PLUGIN_MCP_ENV", "Client-managed Plugin variables must not be supplied by the package.")
		}
	}
	return nil
}

// pluginContained reports whether a path remains under its declared package
// root after cleaning and contains no variable expansion or absolute-path
// escape.
func pluginContained(s string) bool {
	if strings.ContainsAny(s, "\\\x00\r\n") || strings.Contains(s, "${") || strings.HasPrefix(s, "/") {
		return false
	}
	clean := path.Clean(s)
	return clean != ".." && !strings.HasPrefix(clean, "../") && !strings.Contains(clean, ":")
}
