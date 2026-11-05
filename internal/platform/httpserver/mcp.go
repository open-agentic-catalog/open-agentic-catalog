package httpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/authn"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/catalogmcp"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/config"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/diagnostics"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/observability"
)

const mcpMetadataPath = "/.well-known/oauth-protected-resource/mcp"

// mcpHTTP owns finite HTTP requests. SDK sessions are ephemeral; identities,
// tokens and cancellation are never shared across requests.
type mcpHTTP struct {
	reader                        catalogmcp.Reader
	authenticator                 authn.Authenticator
	settings                      config.MCP
	publicOrigin, issuer, version string
	enforceScopes                 bool
	timeout                       time.Duration
	maxBody                       int64
	slots                         chan struct{}
	logger                        *slog.Logger
	metrics                       *observability.Registry
	dependencyWarning             diagnostics.Warning
}

func (h *mcpHTTP) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	outcome, operation := "rejected", "protocol"
	defer func(logContext context.Context) {
		h.metrics.ObserveMCP(operation, outcome)
		h.logger.DebugContext(logContext, "MCP HTTP request completed", "event", "mcp.request_completed", "component", "api", "request_id", requestID(logContext), "operation", operation, "outcome", outcome)
	}(r.Context())
	w.Header().Set("Cache-Control", "no-store")
	defer func(logContext context.Context) {
		if recover() != nil {
			outcome = "failed"
			h.logger.ErrorContext(logContext, "MCP request failed safely", "event", "mcp.request_failed", "component", "api", "request_id", requestID(logContext), "reason", "panic")
			writeMCPError(w, http.StatusInternalServerError, nil, -32603, "The request failed safely.")
		}
	}(r.Context())
	if !h.origin(w, r) {
		return
	}
	if r.URL.RawQuery != "" || r.URL.ForceQuery || !mcpHeaders(r.Header) {
		writeMCPError(w, 400, nil, -32600, "The request headers or query are invalid.")
		return
	}
	if r.Method == http.MethodOptions {
		if h.preflight(w, r) {
			outcome = "completed"
		}
		return
	}
	if r.URL.Path == mcpMetadataPath {
		if h.metadata(w, r) {
			outcome = "completed"
		}
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST, OPTIONS")
		writeMCPError(w, 405, nil, -32600, "Use POST for MCP requests.")
		return
	}
	select {
	case h.slots <- struct{}{}:
		defer func() { <-h.slots }()
	default:
		outcome = "busy"
		w.Header().Set("Retry-After", "1")
		writeMCPError(w, 503, nil, -32603, "The MCP concurrency limit is reached.")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), h.timeout)
	defer cancel()
	r = r.WithContext(ctx)
	token, ok := mcpBearer(r.Header.Get("Authorization"))
	if !ok {
		h.challenge(w, "")
		writeMCPError(w, 401, nil, -32600, "A Bearer token is required.")
		return
	}
	authContext, endAuth := catalog.StartTrace(ctx, "authentication", catalog.TraceInternal, "")
	actor, err := h.authenticator.Authenticate(authContext, token)
	if err != nil {
		if ctx.Err() != nil {
			endAuth("5xx")
			outcome = "cancelled"
			writeMCPError(w, 503, nil, -32603, "Authentication was cancelled or its deadline expired.")
			return
		}
		failure := authenticationFailure(err)
		if catalog.ErrorCodeOf(failure) == catalog.CodeDependencyUnavailable {
			endAuth("5xx")
			h.dependencyWarning.Warn(time.Now(), h.logger, "MCP authentication is temporarily unavailable", "event", "mcp.authentication_unavailable", "component", "api", "request_id", requestID(ctx))
			outcome = "failed"
			writeMCPError(w, 503, nil, -32603, "Catalog authentication is temporarily unavailable.")
		} else {
			endAuth("4xx")
			h.challenge(w, "invalid_token")
			writeMCPError(w, 401, nil, -32600, "The Bearer token was rejected.")
		}
		return
	}
	endAuth("ok")
	ctx = identity.WithPermissionContext(identity.WithActor(ctx, actor))
	r = r.WithContext(ctx)
	raw, err := io.ReadAll(io.LimitReader(r.Body, h.maxBody+1))
	if err != nil {
		writeMCPError(w, 400, nil, -32700, "The request body cannot be read.")
		return
	}
	if int64(len(raw)) > h.maxBody {
		writeMCPError(w, 413, nil, -32600, "The request body exceeds the MCP limit.")
		return
	}
	admission, status, code := admitMCP(r, raw)
	if admission.ID != nil && reflectsCredential(admission.ID, token) {
		writeMCPError(w, 400, nil, -32600, "The request identifier is not safe to return.")
		return
	}
	if status != 0 {
		writeMCPError(w, status, admission.ID, code, "The MCP request is invalid or unsupported.")
		return
	}
	operation = admission.operation()
	if !normalizeMCPName(r) {
		writeMCPError(w, 400, admission.ID, mcp.CodeHeaderMismatch, "The MCP name header is invalid.")
		return
	}
	var scopeDenied, toolFailed, panicked atomic.Bool
	server := catalogmcp.NewServer(h.version, func(_ context.Context, call *mcp.CallToolRequest) (result *mcp.CallToolResult, err error) {
		// The legacy SDK handler context is detached from the HTTP request. Always
		// use this request's authenticated, cancellable context for application reads.
		defer func() {
			if recover() != nil {
				panicked.Store(true)
				result = mcpToolFailure("The request failed safely.")
				err = nil
			}
		}()
		if h.enforceScopes && mcpResourceTool(call.Params.Name) && actor.Claims["scope:catalog.read"] != "true" {
			scopeDenied.Store(true)
			return mcpToolFailure("The catalog.read scope is required."), nil
		}
		result = catalogmcp.Call(ctx, h.reader, call, h.version, h.settings.MaxToolBytes, token)
		toolFailed.Store(result.IsError)
		return result, nil
	}, []string{"2026-07-28", "2025-11-25"})
	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, &mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true, PropagateRequestCancellation: true, MaxRequestBodyBytes: h.maxBody, Logger: slog.New(slog.DiscardHandler)})
	capture := newMCPResponse(h.settings.MaxToolBytes + catalogmcp.MaxFrameBytes)
	r.Body = io.NopCloser(bytes.NewReader(raw))
	handler.ServeHTTP(capture, r)
	switch {
	case panicked.Load():
		outcome = "failed"
		h.logger.ErrorContext(ctx, "MCP read failed safely", "event", "mcp.request_failed", "component", "api", "request_id", requestID(ctx), "reason", "panic")
		writeMCPError(w, 500, admission.ID, -32603, "The request failed safely.")
	case ctx.Err() != nil:
		outcome = "cancelled"
		if ctx.Err() == context.DeadlineExceeded {
			h.metrics.RecordRequestDeadline()
		}
		writeMCPError(w, 503, admission.ID, -32603, "The request was cancelled or its deadline expired.")
	case scopeDenied.Load():
		h.challenge(w, "insufficient_scope")
		writeMCPError(w, 403, admission.ID, -32600, "The catalog.read scope is required.")
	default:
		status, body := capture.safe(admission, token)
		switch {
		case status >= 500:
			outcome = "failed"
		case status >= 400:
			outcome = "rejected"
		case toolFailed.Load():
			outcome = "tool_error"
		default:
			outcome = "completed"
		}
		// SDK cache and session headers are deliberately not copied. Only finite
		// application/json replies and the required method header can leave here.
		if allow := capture.header.Get("Allow"); allow != "" {
			w.Header().Set("Allow", allow)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write(body)
	}
}

func mcpToolFailure(message string) *mcp.CallToolResult {
	return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: message}}}
}
func mcpResourceTool(name string) bool {
	switch name {
	case "catalog_control_search", "catalog_control_explore", "catalog_control_release", "catalog_control_indexed_record":
		return true
	}
	return false
}
func mcpBearer(header string) (string, bool) {
	scheme, token, ok := strings.Cut(header, " ")
	return token, ok && strings.EqualFold(scheme, "Bearer") && token != "" && !strings.ContainsAny(token, " \t\r\n,")
}
func (h *mcpHTTP) challenge(w http.ResponseWriter, reason string) {
	value := `Bearer realm="open-agentic-catalog"`
	if h.issuer != "" {
		value += `, resource_metadata="` + h.publicOrigin + mcpMetadataPath + `"`
	}
	if reason != "" {
		value += `, error="` + reason + `"`
	}
	if reason == "insufficient_scope" {
		value += `, scope="catalog.read"`
	}
	w.Header().Set("WWW-Authenticate", value)
}
func (h *mcpHTTP) metadata(w http.ResponseWriter, r *http.Request) bool {
	if h.issuer == "" {
		writeMCPError(w, 404, nil, -32601, "OAuth resource metadata is unavailable in development mode.")
		return false
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD, OPTIONS")
		writeMCPError(w, 405, nil, -32600, "Use GET for resource metadata.")
		return false
	}
	w.Header().Set("Content-Type", "application/json")
	if r.Method == http.MethodHead {
		w.WriteHeader(http.StatusOK)
		return true
	}
	if err := json.NewEncoder(w).Encode(struct {
		Resource               string   `json:"resource"`
		AuthorizationServers   []string `json:"authorization_servers"`
		ScopesSupported        []string `json:"scopes_supported"`
		BearerMethodsSupported []string `json:"bearer_methods_supported"`
	}{h.publicOrigin + "/mcp", []string{h.issuer}, []string{"catalog.read"}, []string{"header"}}); err != nil {
		return false
	}
	return true
}
