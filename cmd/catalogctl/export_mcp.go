package main

import (
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"fmt"
	"mime"
	"net/url"
	"strconv"
	"strings"
	"unicode"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/adapter"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/jsondoc"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/safeerr"
)

// These notes belong to the private sidecar, never the client's configuration.
type exportMCPConnection struct {
	ClientFormat   string            `json:"clientFormat"`
	Remote         int               `json:"remote"`
	Transport      string            `json:"transport"`
	OmittedHeaders []exportMCPHeader `json:"omittedHeaders"`
	Setup          []string          `json:"setup"`
}

type exportMCPHeader struct {
	Name     string `json:"name"`
	Required bool   `json:"required"`
}

func isMCPExportFormat(format string) bool {
	return format == "mcp-vscode" || format == "mcp-claude-code" || format == "mcp-cursor"
}

func mcpExportFilename(format string) string {
	if format == "mcp-claude-code" {
		return ".mcp.json"
	}
	return "mcp.json"
}

// projectMCPExport consumes metadata only. It never resolves variables, requests
// an endpoint, configures authentication or executes a declared package.
func projectMCPExport(raw []byte, profile, mediaType, releaseID, format string, position int) ([]byte, *exportMCPConnection, error) {
	fail := safeerr.New("export: native MCP connection metadata is invalid or unsupported")
	media, _, err := mime.ParseMediaType(mediaType)
	registry := profile == adapter.ProfileMCPRegistry && media == adapter.MediaMCPRegistry
	card := profile == adapter.ProfileMCPServerCard && media == adapter.MediaMCPServerCard
	if err != nil || (!registry && !card) || !isMCPExportFormat(format) || !identity.ValidTenantID(releaseID) || jsondoc.Validate(raw) != nil {
		return nil, nil, fail
	}
	var doc struct {
		Schema  string            `json:"$schema"`
		Remotes []json.RawMessage `json:"remotes"`
	}
	if jsonv2.Unmarshal(raw, &doc, jsonv2.MatchCaseInsensitiveNames(false)) != nil || card && doc.Schema != "https://static.modelcontextprotocol.io/schemas/v1/server-card.schema.json" || len(doc.Remotes) == 0 {
		return nil, nil, safeerr.New("export: no supported native remote document; use native export for package or unavailable content")
	}
	if position == 0 && len(doc.Remotes) == 1 {
		position = 1
	}
	if position < 1 || position > len(doc.Remotes) {
		// Report positions and fixed transport labels only. Native URLs, header
		// values and untrusted transport strings must not reach the terminal.
		var available strings.Builder
		for i, remote := range doc.Remotes {
			if i == 16 {
				available.WriteString(", additional entries omitted; inspect native export")
				break
			}
			var fields struct {
				Type string `json:"type"`
			}
			label := "unsupported"
			if jsonv2.Unmarshal(remote, &fields, jsonv2.MatchCaseInsensitiveNames(false)) == nil && (fields.Type == "streamable-http" || fields.Type == "sse") {
				label = fields.Type
			}
			if i != 0 {
				available.WriteString(", ")
			}
			fmt.Fprintf(&available, "%d (%s)", i+1, label)
		}
		return nil, nil, safeerr.New("export: select --remote N from original remotes: " + available.String())
	}
	var remote struct {
		Type    string          `json:"type"`
		URL     string          `json:"url"`
		Headers json.RawMessage `json:"headers"`
	}
	if jsonv2.Unmarshal(doc.Remotes[position-1], &remote, jsonv2.MatchCaseInsensitiveNames(false)) != nil || remote.Type != "streamable-http" && remote.Type != "sse" {
		return nil, nil, fail
	}
	if !mcpExportURL(remote.URL) {
		return nil, nil, safeerr.New("export: selected remote requires a literal HTTPS URL without credentials, fragments or client/native substitutions")
	}
	headers, err := mcpExportHeaders(remote.Headers)
	if err != nil {
		return nil, nil, err
	}
	// Use a Catalog identity rather than a native name, which may contain client-
	// incompatible characters. Maps serialize deterministically in encoding/json.
	entry := map[string]string{"url": remote.URL}
	wrapper := "mcpServers"
	if format != "mcp-cursor" {
		transport := remote.Type
		if transport == "streamable-http" {
			transport = "http"
		}
		entry["type"] = transport
	}
	if format == "mcp-vscode" {
		wrapper = "servers"
	}
	value := map[string]map[string]map[string]string{wrapper: {"catalog-" + strings.ToLower(releaseID): entry}}
	encoded, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return nil, nil, fail
	}
	notes := &exportMCPConnection{ClientFormat: format, Remote: position, Transport: remote.Type, OmittedHeaders: headers, Setup: []string{
		"Review the endpoint before sharing this private file; its path or query can contain sensitive values.",
		"Place or merge the entry in the client's project configuration. Rename its server key if needed.",
		"Configure target authentication, TLS trust and any omitted headers in the client. Metadata does not establish anonymous access.",
		"Loading this configuration in a client can initiate a connection. Export does not contact the MCP Server.",
	}}
	return append(encoded, '\n'), notes, nil
}

func mcpExportURL(value string) bool {
	if value == "" || strings.ContainsAny(value, "\\{}") || strings.Contains(value, "${") || strings.IndexFunc(value, func(r rune) bool { return unicode.IsControl(r) || unicode.IsSpace(r) }) >= 0 {
		return false
	}
	u, err := url.Parse(value)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.Hostname() == "" || u.User != nil || u.Fragment != "" || strings.Contains(value, "#") || u.Opaque != "" {
		return false
	}
	if _, err := url.QueryUnescape(u.RawQuery); err != nil {
		return false
	}
	if port := u.Port(); port != "" {
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 {
			return false
		}
	}
	return !strings.HasSuffix(u.Host, ":")
}

func mcpExportHeaders(raw json.RawMessage) ([]exportMCPHeader, error) {
	result := []exportMCPHeader{}
	if len(raw) == 0 {
		return result, nil
	}
	fail := safeerr.New("export: selected remote has invalid header descriptors")
	var headers []struct {
		Name     string          `json:"name"`
		Required json.RawMessage `json:"isRequired"`
	}
	if strings.TrimSpace(string(raw)) == "null" || jsonv2.Unmarshal(raw, &headers, jsonv2.MatchCaseInsensitiveNames(false)) != nil {
		return nil, fail
	}
	seen := map[string]bool{}
	for _, h := range headers {
		if h.Name == "" || seen[strings.ToLower(h.Name)] {
			return nil, fail
		}
		for _, c := range h.Name {
			if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.ContainsRune("!#$%&'*+-.^_`|~", c)) {
				return nil, fail
			}
		}
		required := false
		if len(h.Required) != 0 && (strings.TrimSpace(string(h.Required)) == "null" || json.Unmarshal(h.Required, &required) != nil) {
			return nil, fail
		}
		seen[strings.ToLower(h.Name)] = true
		result = append(result, exportMCPHeader{Name: h.Name, Required: required})
	}
	return result, nil
}

func mcpExportRepresentation(rep exportRepresentation) bool {
	media, _, err := mime.ParseMediaType(rep.MediaType)
	return err == nil && (rep.ProfileID == adapter.ProfileMCPRegistry && media == adapter.MediaMCPRegistry || rep.ProfileID == adapter.ProfileMCPServerCard && media == adapter.MediaMCPServerCard)
}
