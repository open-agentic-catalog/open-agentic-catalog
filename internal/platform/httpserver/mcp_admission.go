package httpserver

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/jsondoc"
)

type mcpAdmission struct {
	ID      json.RawMessage `json:"id"`
	JSONRPC string          `json:"jsonrpc"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

func (a mcpAdmission) operation() string {
	switch a.Method {
	case "server/discover":
		return "discover"
	case "tools/list":
		return "list"
	case "tools/call":
		return "call"
	default:
		return "protocol"
	}
}

func admitMCP(r *http.Request, raw []byte) (a mcpAdmission, status int, code int64) {
	media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || media != "application/json" {
		return a, 415, -32600
	}
	jsonOK, streamOK := false, false
	for _, line := range r.Header.Values("Accept") {
		for _, part := range strings.Split(line, ",") {
			media, params, err := mime.ParseMediaType(strings.TrimSpace(part))
			quality := 1.0
			if text, present := params["q"]; present {
				var qerr error
				quality, qerr = strconv.ParseFloat(text, 64)
				if qerr != nil || quality > 1 {
					quality = 0
				}
			}
			if err == nil && quality > 0 {
				jsonOK = jsonOK || media == "application/json"
				streamOK = streamOK || media == "text/event-stream"
			}
		}
	}
	if !jsonOK || !streamOK {
		return a, 400, -32600
	}
	if jsondoc.Validate(raw) != nil {
		return a, 400, -32700
	}
	if len(raw) == 0 || strings.TrimSpace(string(raw))[0] != '{' || jsonv2.Unmarshal(raw, &a, json.DefaultOptionsV1(), jsonv2.MatchCaseInsensitiveNames(false)) != nil || a.JSONRPC != "2.0" || a.Method == "" {
		return mcpAdmission{}, 400, -32600
	}
	if a.ID != nil {
		var id any
		if json.Unmarshal(a.ID, &id) != nil {
			return mcpAdmission{}, 400, -32600
		}
		switch id.(type) {
		case string, float64:
		default:
			return mcpAdmission{}, 400, -32600
		}
	}
	// Every selected MCP method uses named object parameters. Null, arrays and
	// scalars must not become an implicit empty request in an SDK handler.
	if a.Params != nil && !bytes.HasPrefix(bytes.TrimSpace(a.Params), []byte("{")) {
		return a, 400, -32602
	}
	version := r.Header.Get("Mcp-Protocol-Version")
	if version == "" && a.Method != "initialize" {
		return a, 400, mcp.CodeHeaderMismatch
	}
	if version != "" && version != "2025-11-25" && version < "2026-07-28" {
		return a, 400, mcp.CodeUnsupportedProtocolVersion
	}
	// Modern versions go through SDK version/metadata admission, which preserves
	// structured supported-version errors. Only legacy initialization is headerless.
	switch a.Method {
	case "server/discover", "tools/list", "tools/call", "initialize", "ping":
		if a.ID == nil {
			return a, 400, -32600
		}
	case "notifications/initialized":
		if a.ID != nil || version != "2025-11-25" {
			return a, 400, -32600
		}
	default:
		return a, 404, -32601
	}
	if a.Method == "initialize" && version >= "2026-07-28" {
		return a, 404, -32601
	}
	return a, 0, 0
}

func mcpHeaders(header http.Header) bool {
	seen := map[string]bool{}
	for name, values := range header {
		lower := strings.ToLower(name)
		single := lower == "authorization" || lower == "content-type" || lower == "origin" || lower == "access-control-request-method" || lower == "access-control-request-headers" || strings.HasPrefix(lower, "mcp-")
		if single && (seen[lower] || len(values) != 1 || values[0] == "") {
			return false
		}
		seen[lower] = true
		if lower == "mcp-session-id" || lower == "last-event-id" || strings.HasPrefix(lower, "mcp-param-") {
			return false
		}
	}
	return true
}

func (h *mcpHTTP) origin(w http.ResponseWriter, r *http.Request) bool {
	addVary(w.Header(), "Origin")
	values := r.Header.Values("Origin")
	if len(values) == 0 {
		return true
	}
	origin := r.Header.Get("Origin")
	allowed := origin != "" && origin == h.publicOrigin
	for _, configured := range h.settings.AllowedOrigins {
		allowed = allowed || origin == configured
	}
	if len(values) != 1 || !allowed {
		writeMCPError(w, 403, nil, -32600, "The Origin is not permitted.")
		return false
	}
	w.Header().Set("Access-Control-Allow-Origin", origin)
	w.Header().Set("Access-Control-Expose-Headers", "WWW-Authenticate, X-Request-ID, Retry-After")
	return true
}

func (h *mcpHTTP) preflight(w http.ResponseWriter, r *http.Request) bool {
	method := r.Header.Get("Access-Control-Request-Method")
	allowedMethod := method == http.MethodPost
	if r.URL.Path == mcpMetadataPath {
		allowedMethod = method == http.MethodGet || method == http.MethodHead
	}
	if r.Header.Get("Origin") == "" || !allowedMethod {
		writeMCPError(w, 403, nil, -32600, "The preflight is not permitted.")
		return false
	}
	for _, header := range strings.Split(r.Header.Get("Access-Control-Request-Headers"), ",") {
		switch strings.ToLower(strings.TrimSpace(header)) {
		case "", "accept", "authorization", "content-type", "mcp-protocol-version", "mcp-method", "mcp-name", "x-request-id", "traceparent":
		default:
			writeMCPError(w, 403, nil, -32600, "The preflight headers are not permitted.")
			return false
		}
	}
	w.Header().Set("Access-Control-Allow-Methods", method)
	w.Header().Set("Access-Control-Allow-Headers", "Accept, Authorization, Content-Type, Mcp-Protocol-Version, Mcp-Method, Mcp-Name, X-Request-ID, Traceparent")
	addVary(w.Header(), "Access-Control-Request-Method")
	addVary(w.Header(), "Access-Control-Request-Headers")
	w.WriteHeader(http.StatusNoContent)
	return true
}

// Decode a mirrored name without ever using it as identity or destination.
func normalizeMCPName(r *http.Request) bool {
	value := r.Header.Get("Mcp-Name")
	if !strings.HasPrefix(value, "=?base64?") {
		return true
	}
	encoded, ok := strings.CutSuffix(strings.TrimPrefix(value, "=?base64?"), "?=")
	if !ok || len(encoded) > 1024 {
		return false
	}
	decoded, err := base64.StdEncoding.Strict().DecodeString(encoded)
	if err != nil || !utf8.Valid(decoded) || bytes.ContainsAny(decoded, "\x00\r\n") {
		return false
	}
	r.Header = r.Header.Clone()
	r.Header.Set("Mcp-Name", string(decoded))
	return true
}
