package httpserver

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
)

// authenticateMCP admits this request only. No SDK session owns its identity.
func (h *mcpHTTP) authenticateMCP(w http.ResponseWriter, r *http.Request, ctx context.Context) (context.Context, identity.Actor, string, string) {
	token, valid := mcpBearer(r.Header.Get("Authorization"))
	if h.discovery != nil {
		// Match the token admitted by REST discovery, including bearer spacing.
		token = strings.TrimSpace(token)
		admitted, challenge, err := h.discovery.server.discoveryPrincipal(ctx, r, h.discovery.host)
		if ctx.Err() != nil {
			writeMCPError(w, 503, nil, -32603, "Authentication was cancelled or its deadline expired.")
			return nil, identity.Actor{}, "", "cancelled"
		}
		if err != nil {
			code, status, outcome := int64(-32600), http.StatusForbidden, "rejected"
			if catalog.ErrorCodeOf(err) == catalog.CodeUnauthenticated {
				status = http.StatusUnauthorized
			} else if catalog.ErrorCodeOf(err) == catalog.CodeDependencyUnavailable {
				status, code, outcome = http.StatusServiceUnavailable, -32603, "failed"
			}

			if challenge != "" {
				reason := ""
				if strings.Contains(challenge, "invalid_token") {
					reason = "invalid_token"
				} else if strings.Contains(challenge, "insufficient_scope") {
					reason = "insufficient_scope"
				}
				h.challenge(w, reason)
			}
			writeMCPError(w, status, nil, code, "Discovery access is unavailable or refused.")
			return nil, identity.Actor{}, "", outcome
		}
		actor, _ := identity.FromContext(admitted)
		return admitted, actor, token, ""
	}
	if !valid {
		h.challenge(w, "")
		writeMCPError(w, 401, nil, -32600, "A Bearer token is required.")
		return nil, identity.Actor{}, "", "rejected"
	}
	authContext, endAuth := catalog.StartTrace(ctx, "authentication", catalog.TraceInternal, "")
	actor, err := h.authenticator.Authenticate(authContext, token)
	if err != nil {
		if ctx.Err() != nil {
			endAuth("5xx")
			writeMCPError(w, 503, nil, -32603, "Authentication was cancelled or its deadline expired.")
			return nil, identity.Actor{}, "", "cancelled"
		}
		failure := authenticationFailure(err)
		if catalog.ErrorCodeOf(failure) == catalog.CodeDependencyUnavailable {
			endAuth("5xx")
			h.dependencyWarning.Warn(time.Now(), h.logger, "MCP authentication is temporarily unavailable", "event", "mcp.authentication_unavailable", "component", "api", "request_id", requestID(ctx))
			writeMCPError(w, 503, nil, -32603, "Catalog authentication is temporarily unavailable.")
			return nil, identity.Actor{}, "", "failed"
		}
		endAuth("4xx")
		h.challenge(w, "invalid_token")
		writeMCPError(w, 401, nil, -32600, "The Bearer token was rejected.")
		return nil, identity.Actor{}, "", "rejected"
	}
	endAuth("ok")
	return identity.WithPermissionContext(identity.WithActor(ctx, actor)), actor, token, ""
}

func (h *mcpHTTP) endpoint() string {
	if h.discovery != nil {
		return ardMCPPath
	}
	return "/mcp"
}

func (h *mcpHTTP) metadataEndpoint() string {
	if h.discovery != nil {
		return ardMCPMetadataPath
	}
	return mcpMetadataPath
}
