package httpserver

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/jsondoc"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/catalogmcp"
)

// mcpResponse deliberately has no Flusher: the admitted methods return finite
// JSON. Overflow is sticky, consumes no additional memory and emits no prefix.
type mcpResponse struct {
	header        http.Header
	body          bytes.Buffer
	status, limit int
	overflow      bool
}

func newMCPResponse(limit int) *mcpResponse {
	return &mcpResponse{header: make(http.Header), limit: limit}
}
func (w *mcpResponse) Header() http.Header { return w.header }
func (w *mcpResponse) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
	}
}
func (w *mcpResponse) Write(value []byte) (int, error) {
	if w.status == 0 {
		w.status = 200
	}
	if w.overflow || len(value) > w.limit-w.body.Len() {
		w.overflow = true
		return len(value), nil
	}
	return w.body.Write(value)
}

type mcpVersionEvidence struct {
	Supported []string `json:"supported"`
	Requested string   `json:"requested"`
}
type mcpProtocolFailure struct {
	Code    int64               `json:"code"`
	Message string              `json:"message"`
	Data    *mcpVersionEvidence `json:"data,omitempty"`
}

func mcpErrorBytes(id json.RawMessage, code int64, message string) []byte {
	if id == nil {
		id = json.RawMessage("null")
	}
	raw, err := json.Marshal(struct {
		JSONRPC string             `json:"jsonrpc"`
		ID      json.RawMessage    `json:"id"`
		Error   mcpProtocolFailure `json:"error"`
	}{"2.0", id, mcpProtocolFailure{Code: code, Message: message}})
	if err != nil {
		return []byte(`{"jsonrpc":"2.0","id":null,"error":{"code":-32603,"message":"The MCP response could not be encoded."}}`)
	}
	return raw
}
func writeMCPError(w http.ResponseWriter, status int, id json.RawMessage, code int64, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_, _ = w.Write(mcpErrorBytes(id, code, message))
}
func (w *mcpResponse) safe(a mcpAdmission, credential string) (int, []byte) {
	invalid := func() (int, []byte) {
		return 500, mcpErrorBytes(nil, -32603, "The complete MCP response is unavailable.")
	}
	if w.overflow {
		return invalid()
	}
	if w.status == http.StatusAccepted && w.body.Len() == 0 {
		return w.status, nil
	}
	raw := w.body.Bytes()
	var value map[string]json.RawMessage
	if jsondoc.Validate(raw) != nil || json.Unmarshal(raw, &value) != nil || value == nil {
		status := w.status
		if status < 400 {
			status = 500
		}
		return status, mcpErrorBytes(a.ID, -32603, "The MCP request could not be processed.")
	}
	if errorRaw, ok := value["error"]; ok {
		var protocolError struct {
			Code int64           `json:"code"`
			Data json.RawMessage `json:"data"`
		}
		if json.Unmarshal(errorRaw, &protocolError) != nil {
			return invalid()
		}
		safeError := mcpProtocolFailure{Code: protocolError.Code, Message: "The MCP request was refused."}
		if protocolError.Code == mcp.CodeUnsupportedProtocolVersion {
			var versions struct {
				Supported []string `json:"supported"`
				Requested string   `json:"requested"`
			}
			if json.Unmarshal(protocolError.Data, &versions) == nil && (versions.Requested == "2026-07-28" || len(versions.Requested) == 10) {
				safeError.Data = &mcpVersionEvidence{Supported: []string{"2026-07-28", "2025-11-25"}, Requested: versions.Requested}
			}
		}
		errorRaw, err := json.Marshal(safeError)
		if err != nil {
			return invalid()
		}
		value["error"] = errorRaw
		raw, err = json.Marshal(value)
		if err != nil {
			return invalid()
		}
	}
	if a.Method != "tools/call" && len(raw) > catalogmcp.MaxFrameBytes {
		return invalid()
	}
	if reflectsCredential(raw, credential) {
		return invalid()
	}
	status := w.status
	if status == 0 {
		status = 200
	}
	return status, raw
}
func reflectsCredential(raw []byte, credential string) bool {
	if credential == "" {
		return false
	}
	if bytes.Contains(raw, []byte(credential)) {
		return true
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			return false
		}
		if err != nil {
			return true
		}
		if value, ok := token.(string); ok && strings.Contains(value, credential) {
			return true
		}
	}
}
