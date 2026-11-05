// Product middleware coordinates authentication, safe request attribution,
// authorization and exact command replay/response capture.

package httpserver

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/audit"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/authn"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/idempotency"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/tracing"
)

type requestIDKey struct{}
type traceIDKey struct{}

var safeRequestID = regexp.MustCompile(`^[A-Za-z0-9._-]{1,128}$`)
var safeTraceParent = regexp.MustCompile(`^00-([0-9a-f]{32})-([0-9a-f]{16})-[0-9a-f]{2}$`)

// incomingTrace applies one admission rule to trace parenting and log fallback.
// W3C wire flags are normalized before validating retained causal metadata.
func incomingTrace(header http.Header) catalog.TraceContext {
	values := header.Values("Traceparent")
	if len(values) != 1 {
		return ""
	}
	match := safeTraceParent.FindStringSubmatch(values[0])
	if len(match) != 3 {
		return ""
	}
	flags := "00"
	if strings.ContainsRune("13579bdf", rune(values[0][54])) {
		flags = "01"
	}
	parent := catalog.TraceContext("00-" + match[1] + "-" + match[2] + "-" + flags)
	if !parent.Valid() {
		return ""
	}
	return parent
}

// requestID returns the validated or generated request correlation identifier from context.
func requestID(ctx context.Context) string {
	value, _ := ctx.Value(requestIDKey{}).(string)
	return value
}

// traceID returns a syntactically validated incoming trace identifier when one was admitted.
func traceID(ctx context.Context) string {
	value, _ := ctx.Value(traceIDKey{}).(string)
	return value
}

// requestIDMiddleware establishes safe request correlation and admits only valid nonzero W3C trace identifiers.
func requestIDMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		value := request.Header.Get("X-Request-ID")
		if !safeRequestID.MatchString(value) {
			random := make([]byte, 16)
			if _, err := rand.Read(random); err != nil {
				value = time.Now().UTC().Format("20060102150405.000000000")
			} else {
				value = hex.EncodeToString(random)
			}
		}
		writer.Header().Set("X-Request-ID", value)
		ctx := context.WithValue(request.Context(), requestIDKey{}, value)
		if parent := incomingTrace(request.Header); parent != "" {
			ctx = context.WithValue(ctx, traceIDKey{}, string(parent)[3:35])
		}
		next.ServeHTTP(writer, request.WithContext(ctx))
	})
}

// correlationLogAttributes prefers the current internal causal trace and otherwise retains admitted request correlation.
func correlationLogAttributes(ctx context.Context) []any {
	attributes := []any{"request_id", requestID(ctx)}
	if current := tracing.LogAttributes(ctx); len(current) != 0 {
		return append(attributes, current...)
	}
	if value := traceID(ctx); value != "" {
		attributes = append(attributes, "trace_id", value)
	}
	return attributes
}

// requestLogAttributes adds sanitized method and route-pattern fields without logging raw request paths or queries.
func requestLogAttributes(request *http.Request, attributes ...any) []any {
	result := correlationLogAttributes(request.Context())
	result = append(result, "method", safeLogMethod(request.Method), "route", safeLogRoute(request.Pattern))
	return append(result, attributes...)
}

// safeLogMethod reduces unknown request methods to a fixed diagnostic category.
func safeLogMethod(method string) string {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPatch, http.MethodDelete, http.MethodOptions:
		return method
	default:
		return "OTHER"
	}
}

// safeLogRoute keeps a bounded router pattern while rejecting unsuitable log text.
func safeLogRoute(pattern string) string {
	if _, route, ok := strings.Cut(pattern, " "); ok {
		pattern = route
	}
	if pattern == "" || len(pattern) > 180 || !strings.HasPrefix(pattern, "/") || strings.ContainsAny(pattern, "\r\n\t\"") {
		return "unmatched"
	}
	return pattern
}

// securityHeaders sets browser protections and disables caching for control API responses.
func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("X-Content-Type-Options", "nosniff")
		writer.Header().Set("X-Frame-Options", "DENY")
		writer.Header().Set("Referrer-Policy", "no-referrer")
		writer.Header().Set("Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'")
		writer.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		if strings.HasPrefix(request.URL.Path, "/api/") {
			writer.Header().Set("Cache-Control", "no-store")
		}
		next.ServeHTTP(writer, request)
	})
}

// cors grants response access and bounded preflight metadata only to configured exact origins.
func cors(origins []string, next http.Handler) http.Handler {
	allowed := make(map[string]struct{}, len(origins))
	for _, origin := range origins {
		allowed[origin] = struct{}{}
	}
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/mcp" || request.URL.Path == mcpMetadataPath {
			next.ServeHTTP(writer, request)
			return
		}
		origin := request.Header.Get("Origin")
		if origin == "" {
			next.ServeHTTP(writer, request)
			return
		}
		addVary(writer.Header(), "Origin")
		if _, ok := allowed[origin]; !ok {
			next.ServeHTTP(writer, request)
			return
		}
		writer.Header().Set("Access-Control-Allow-Origin", origin)
		writer.Header().Set("Access-Control-Expose-Headers", "Catalog-Search-Tier, Catalog-Search-Fallback, Catalog-Search-Partial-Index, Content-Digest, Content-Disposition, ETag, Idempotency-Replayed, Link, Location, Retry-After, Server-Timing, X-Request-ID")
		if request.Method == http.MethodOptions && request.Header.Get("Access-Control-Request-Method") != "" {
			writer.Header().Set("Access-Control-Allow-Methods", "GET, HEAD, POST, PATCH, DELETE, OPTIONS")
			writer.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type, Idempotency-Key, If-Match, If-Modified-Since, If-None-Match, Traceparent, X-Request-ID")
			writer.Header().Set("Access-Control-Max-Age", "600")
			writer.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(writer, request)
	})
}

// controlHeaderAdmission rejects ambiguous single-value Control inputs before
// authentication or command reservation. Other HTTP fields retain their own
// list/repetition semantics; this is not a blanket duplicate-header policy.
func controlHeaderAdmission(next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if strings.HasPrefix(request.URL.Path, "/api/v1/") {
			for _, name := range []string{"Authorization", "Content-Type", "Idempotency-Key", "If-Match"} {
				if len(request.Header.Values(name)) > 1 {
					writeProblem(writer, request, catalog.E(catalog.CodeInvalidArgument, "control headers must contain one unambiguous value"))
					return
				}
			}
		}
		next.ServeHTTP(writer, request)
	})
}

// authentication requires an accepted bearer for control API paths and attaches its resolved Catalog actor.
func authentication(authenticator authn.Authenticator, next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if !strings.HasPrefix(request.URL.Path, "/api/v1/") {
			next.ServeHTTP(writer, request)
			return
		}
		header := request.Header.Get("Authorization")
		if header == "" {
			writer.Header().Set("WWW-Authenticate", `Bearer realm="open-agentic-catalog"`)
			writeProblem(writer, request, catalog.E(catalog.CodeUnauthenticated, "a Bearer token is required"))
			return
		}
		scheme, token, ok := strings.Cut(header, " ")
		if !ok || !strings.EqualFold(scheme, "Bearer") || strings.TrimSpace(token) == "" {
			writer.Header().Set("WWW-Authenticate", `Bearer realm="open-agentic-catalog"`)
			writeProblem(writer, request, catalog.E(catalog.CodeUnauthenticated, "a valid Bearer token is required"))
			return
		}
		authContext, endAuth := catalog.StartTrace(request.Context(), "authentication", catalog.TraceInternal, "")
		actor, err := authenticator.Authenticate(authContext, strings.TrimSpace(token))
		if err != nil {
			failure := authenticationFailure(err)
			if catalog.ErrorCodeOf(failure) == catalog.CodeDependencyUnavailable {
				endAuth("5xx")
			} else {
				endAuth("4xx")
				writer.Header().Set("WWW-Authenticate", `Bearer realm="open-agentic-catalog", error="invalid_token"`)
			}
			writeProblem(writer, request, failure)
			return
		}
		endAuth("ok")
		next.ServeHTTP(writer, request.WithContext(identity.WithPermissionContext(identity.WithActor(request.Context(), actor))))
	})
}

// authenticationFailure preserves known current-policy outages without treating
// dependency text as public diagnostics or changing credential rejection privacy.
func authenticationFailure(err error) error {
	if identity.ErrorCodeOf(err) == identity.CodeDependency {
		return catalog.Wrap(catalog.CodeDependencyUnavailable, "Catalog authentication is temporarily unavailable", err)
	}
	return catalog.E(catalog.CodeUnauthenticated, "the Bearer token was rejected")
}

// auditExecution binds request correlation and the current authorization digest to domain audit execution.
func auditExecution(next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		actor, ok := identity.FromContext(request.Context())
		if !ok {
			next.ServeHTTP(writer, request)
			return
		}
		execution := audit.Execution{RequestID: requestID(request.Context()), AuthorizationDigest: actor.AuthorizationDigest()}
		next.ServeHTTP(writer, request.WithContext(audit.WithExecution(request.Context(), execution)))
	})
}

// authorizationScopes enforces configured coarse OAuth admission without replacing domain role checks.
func authorizationScopes(enforce bool, next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if !enforce || !strings.HasPrefix(request.URL.Path, "/api/v1/") {
			next.ServeHTTP(writer, request)
			return
		}
		required := requiredOAuthScope(request.Method, request.URL.Path)
		if required == "" {
			next.ServeHTTP(writer, request)
			return
		}
		actor, ok := identity.FromContext(request.Context())
		if !ok || actor.Claims["scope:"+required] != "true" {
			writer.Header().Set("WWW-Authenticate", `Bearer error="insufficient_scope", scope="`+required+`"`)
			writeProblem(writer, request, catalog.E(catalog.CodeForbidden, "OAuth scope "+required+" is required"))
			return
		}
		next.ServeHTTP(writer, request)
	})
}

// requiredOAuthScope maps the stable path-major API to coarse OAuth admission.
// Catalog roles and namespace grants are still enforced by domain services.
func requiredOAuthScope(method, path string) string {
	read := method == http.MethodGet || method == http.MethodHead
	switch {
	case path == "/api/v1/service-activity":
		return "tenant.admin"
	case path == "/api/v1/overview/publishers":
		return "publishers.read"
	case path == "/api/v1/overview/sources":
		return "sources.read"
	case path == "/api/v1/overview/webhook-endpoints" || path == "/api/v1/overview/webhook-deliveries":
		return "webhooks.manage"
	case path == "/api/v1/overview/jobs":
		return "catalog.read"
	case path == "/api/v1/overview/administration" || path == "/api/v1/overview/feedback" || path == "/api/v1/overview/ratings":
		return "tenant.admin"
	case path == "/api/v1/overview/reviews" || path == "/api/v1/overview/publication" || path == "/api/v1/overview/resources" || path == "/api/v1/overview/releases" || path == "/api/v1/overview/namespaces" || path == "/api/v1/overview/indexed":
		return "catalog.read"
	case path == "/api/v1/access/capabilities" || path == "/api/v1/access/self" || path == "/api/v1/access/permissions":
		return ""
	case strings.HasPrefix(path, "/api/v1/access/"):
		return "tenant.admin"
	case path == "/api/v1/tenant-settings":
		return "tenant.admin"
	case path == "/api/v1/catalog:reindex":
		return "tenant.admin"
	case path == "/api/v1/capabilities" || path == "/api/v1/me" || path == "/api/v1/service-information":
		return ""
	case method == http.MethodPost && (strings.HasPrefix(path, "/api/v1/personal/items/") && strings.HasSuffix(path, "/resolve") || path == "/api/v1/catalog/search" || path == "/api/v1/catalog/explore" || path == "/api/v1/artwork/read" || path == "/api/v1/ratings/read"):
		return "catalog.read"
	case method == http.MethodPost && (path == "/api/v1/observations:search" || path == "/api/v1/federation/sources:live-search"):
		return "sources.read"
	case method == http.MethodPost && strings.HasPrefix(path, "/api/v1/observations/") && strings.HasSuffix(path, ":adopt"):
		return "catalog.write"
	case strings.HasPrefix(path, "/api/v1/ratings"):
		if path == "/api/v1/ratings/sharing" || path == "/api/v1/ratings/inbox" || strings.HasSuffix(path, ":remove") || strings.HasSuffix(path, ":permit") {
			return "tenant.admin"
		}
		if read {
			return "catalog.read"
		}
		return "ratings.write"
	case strings.HasPrefix(path, "/api/v1/personal/items"):
		if method == http.MethodGet || strings.HasSuffix(path, "/resolve") {
			return "catalog.read"
		}
		return "personal.write"
	case strings.HasPrefix(path, "/api/v1/feedback"):
		if path == "/api/v1/feedback/inbox" || strings.HasSuffix(path, ":address") || strings.HasSuffix(path, ":reopen") || strings.HasSuffix(path, ":remove") {
			return "tenant.admin"
		}
		if read {
			return "catalog.read"
		}
		return "feedback.write"
	case strings.HasPrefix(path, "/api/v1/audit-exports"):
		return "audit.export"
	case strings.HasPrefix(path, "/api/v1/audit-events"):
		return "audit.read"
	case strings.HasPrefix(path, "/api/v1/webhooks"):
		return "webhooks.manage"
	case strings.HasPrefix(path, "/api/v1/federation") || strings.HasPrefix(path, "/api/v1/observations") || strings.HasPrefix(path, "/api/v1/observation-groups"):
		if read {
			return "sources.read"
		}
		return "sources.manage"
	case strings.HasPrefix(path, "/api/v1/publishers"):
		if read && strings.HasSuffix(path, "/verification/challenge") {
			return "publishers.manage"
		}
		if read {
			return "publishers.read"
		}
		return "publishers.manage"
	case strings.HasPrefix(path, "/api/v1/role-assignments") || strings.HasPrefix(path, "/api/v1/memberships"):
		return "tenant.admin"
	case strings.HasPrefix(path, "/api/v1/publication") || strings.Contains(path, ":publish") || strings.HasPrefix(path, "/api/v1/listing-releases"):
		if read {
			return "catalog.read"
		}
		return "catalog.publish"
	case strings.HasPrefix(path, "/api/v1/review-submissions"):
		if read {
			return "catalog.read"
		}
		return "catalog.review"
	case strings.HasPrefix(path, "/api/v1/namespaces") && !read:
		return "tenant.admin"
	case read:
		return "catalog.read"
	default:
		return "catalog.write"
	}
}

// recoverPanics tracks response commitment so panics can produce a safe failure or abort the connection.
func recoverPanics(logger *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		tracked := &statusWriter{ResponseWriter: writer}
		defer recoverHTTPPanic(request.Context(), logger, tracked, request)
		next.ServeHTTP(tracked, request)
	})
}

// recoverHTTPPanic aborts command state, hides panic values and rewrites only uncommitted or resettable responses.
func recoverHTTPPanic(ctx context.Context, logger *slog.Logger, writer *statusWriter, request *http.Request) {
	recovered := recover()
	if recovered == nil {
		return
	}
	if recoveredErr, ok := recovered.(error); ok && errors.Is(recoveredErr, http.ErrAbortHandler) {
		panic(http.ErrAbortHandler)
	}
	idempotency.AbortCommand(ctx, catalog.E(catalog.CodeInternal, "HTTP handler panicked"))
	if logger != nil {
		diagnosticOwned(ctx)
		logger.Error("HTTP handler panic", requestLogAttributes(request.WithContext(ctx), "event", "http.panic", "component", "api", "panic_type", fmt.Sprintf("%T", recovered))...)
	}
	request = request.WithContext(ctx)
	if !writer.responseCommitted() || writer.resetResponse() {
		writeProblem(writer, request, catalog.E(catalog.CodeInternal, "internal handler failure"))
		return
	}
	panic(http.ErrAbortHandler)
}

// responseCapture keeps a command reply private until its completion/replay boundary succeeds.
type responseCapture struct {
	authority         idempotency.ReplayAuthority
	validationProblem bool
	header            http.Header
	status            int
	body              bytes.Buffer
}

// newResponseCapture allocates an isolated in-memory response for command completion and replay.
func newResponseCapture() *responseCapture { return &responseCapture{header: make(http.Header)} }

// Header returns the mutable headers retained by this response capture.
func (r *responseCapture) Header() http.Header { return r.header }

// WriteHeader retains the first status without committing bytes to the network.
func (r *responseCapture) WriteHeader(status int) {
	if r.status == 0 {
		r.status = status
	}
}

// Write buffers response bytes and supplies an implicit success status.
func (r *responseCapture) Write(value []byte) (int, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	return r.body.Write(value)
}

// resetResponse discards buffered headers, status and body so recovery can replace the response.
func (r *responseCapture) resetResponse() bool {
	r.header = make(http.Header)
	r.validationProblem = false
	r.status = 0
	r.body.Reset()
	return true
}

// storedResponse is the process-local rendezvous result for one exact command binding.
type storedResponse struct {
	authority           idempotency.ReplayAuthority
	digest              string
	authorizationDigest string
	status              int
	header              http.Header
	body                []byte
	retained            bool
	done                chan struct{}
}

// idempotencyCache coordinates concurrent local requests while the injected store owns reservations.
type idempotencyCache struct {
	mu              sync.Mutex
	entries         map[string]*storedResponse
	replayAdmission func(*http.Request, idempotency.ReplayAuthority, func()) error
	atomic          idempotency.AtomicStore
	store           idempotency.Store
	logger          *slog.Logger
}

// newIdempotencyCache creates process-local rendezvous state around the supplied reservation store.
func newIdempotencyCache(store idempotency.Store, logger *slog.Logger) *idempotencyCache {
	return &idempotencyCache{entries: make(map[string]*storedResponse), store: store, logger: logger}
}

// middleware binds an idempotency key to the tenant, actor, authorization state,
// route, and exact request shape before any command handler runs.
func (c *idempotencyCache) middleware(maxBody int64, next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if !requiresIdempotency(request) {
			next.ServeHTTP(writer, request)
			return
		}
		actor, ok := identity.FromContext(request.Context())
		if !ok {
			writeProblem(writer, request, catalog.E(catalog.CodeForbidden, "authenticated actor is required"))
			return
		}
		key := strings.TrimSpace(request.Header.Get("Idempotency-Key"))
		if key == "" || len(key) > 200 || strings.ContainsAny(key, "\r\n") {
			writeProblem(writer, request, catalog.E(catalog.CodeInvalidArgument, "a valid Idempotency-Key is required for this command"))
			return
		}
		body, err := io.ReadAll(io.LimitReader(request.Body, maxBody+1))
		if err != nil || int64(len(body)) > maxBody {
			writeProblem(writer, request, catalog.E(catalog.CodeInvalidArgument, "request body exceeds the configured limit"))
			return
		}
		request.Body = io.NopCloser(bytes.NewReader(body))
		digestInput, marshalErr := json.Marshal(struct {
			Query       string `json:"query"`
			IfMatch     string `json:"ifMatch"`
			ContentType string `json:"contentType"`
			Body        []byte `json:"body"`
		}{Query: request.URL.RawQuery, IfMatch: strings.TrimSpace(request.Header.Get("If-Match")), ContentType: strings.TrimSpace(request.Header.Get("Content-Type")), Body: body})
		if marshalErr != nil {
			writeProblem(writer, request, catalog.E(catalog.CodeInternal, "request digest could not be created"))
			return
		}
		sum := sha256.Sum256(digestInput)
		digest := hex.EncodeToString(sum[:])
		route := request.Method + " " + request.URL.Path
		commandScope := idempotency.Scope{TenantID: actor.TenantID, Actor: actor.CommandPrincipal(), Route: route, Key: key, AuthorizationDigest: actor.CommandDigest()}
		localScope := strings.ToLower(actor.TenantID) + "\x00" + actor.CommandPrincipal() + "\x00" + route + "\x00" + key
		c.mu.Lock()
		entry, exists := c.entries[localScope]
		if exists && entry.digest != digest {
			c.mu.Unlock()
			writeProblem(writer, request, catalog.E(catalog.CodeIdempotencyKeyReused, "the Idempotency-Key was already used with another request"))
			return
		}
		if exists && entry.authorizationDigest != commandScope.AuthorizationDigest {
			c.mu.Unlock()
			writeProblem(writer, request, catalog.E(catalog.CodeForbidden, "current authorization does not match the idempotent command"))
			return
		}
		if exists {
			done := entry.done
			c.mu.Unlock()
			select {
			case <-done:
			case <-request.Context().Done():
				writeProblem(writer, request, catalog.E(catalog.CodeDependencyUnavailable, "idempotent command is still running"))
				return
			}
			c.mu.Lock()
			copyResponse := *entry
			c.mu.Unlock()
			c.writeReplay(writer, request, copyResponse.status, copyResponse.header, copyResponse.body, copyResponse.retained, copyResponse.authority)
			return
		}
		entry = &storedResponse{digest: digest, authorizationDigest: commandScope.AuthorizationDigest, done: make(chan struct{})}
		c.entries[localScope] = entry
		c.mu.Unlock()
		defer c.failUnfinished(localScope, entry, request)
		if c.atomic != nil {
			c.executeAtomic(writer, request, next, c.atomic, commandScope, localScope, entry, digest, key)
			return
		}
		reservation, err := c.store.Reserve(request.Context(), commandScope, digest, time.Now().UTC().Add(24*time.Hour))
		if err != nil {
			capture := newResponseCapture()
			writeProblem(capture, request, err)
			c.finish(localScope, entry, capture, false)
			copyHeaders(writer.Header(), capture.header)
			writer.WriteHeader(capture.status)
			_, _ = writer.Write(capture.body.Bytes())
			return
		}
		if reservation.Pending {
			capture := newResponseCapture()
			writeProblem(capture, request, catalog.E(catalog.CodeDependencyUnavailable, "idempotent command is already running"))
			capture.header.Set("Retry-After", "1")
			c.finish(localScope, entry, capture, false)
			copyHeaders(writer.Header(), capture.header)
			writer.WriteHeader(capture.status)
			_, _ = writer.Write(capture.body.Bytes())
			return
		}
		if !reservation.Acquired {
			capture := newResponseCapture()
			capture.authority = reservation.Response.Authority
			capture.status = reservation.Response.Status
			capture.header = http.Header(reservation.Response.Headers).Clone()
			_, _ = capture.body.Write(reservation.Response.Body)
			c.finish(localScope, entry, capture, true)
			c.writeReplay(writer, request, capture.status, capture.header, capture.body.Bytes(), true, capture.authority)
			return
		}
		capture := newResponseCapture()
		next.ServeHTTP(capture, request)
		if capture.status == 0 {
			capture.status = http.StatusOK
		}
		capture.authority = replayAuthority(request, capture)
		response := idempotency.Response{Authority: capture.authority, Digest: digest, Status: capture.status, Headers: retainedResponseHeaders(capture.header), Body: append([]byte(nil), capture.body.Bytes()...)}
		completionErr := c.complete(request.Context(), commandScope, response)
		if completionErr != nil {
			keyDigest := sha256.Sum256([]byte(key))
			diagnosticOwned(request.Context())
			c.logger.Error("idempotency response could not be durably completed", requestLogAttributes(request, "event", "idempotency.completion_failed", "component", "api", "tenant_id", actor.TenantID, "idempotency_key_digest", hex.EncodeToString(keyDigest[:]), "error_code", catalog.ErrorCodeOf(completionErr), "error_type", fmt.Sprintf("%T", completionErr))...)
		}
		c.finish(localScope, entry, capture, completionErr == nil)
		copyHeaders(writer.Header(), capture.header)
		writer.WriteHeader(capture.status)
		_, _ = writer.Write(capture.body.Bytes())
	})
}

// executeAtomic runs the handler inside the PostgreSQL command transaction so
// domain state and the replay response commit or roll back together.
func (c *idempotencyCache) executeAtomic(writer http.ResponseWriter, request *http.Request, next http.Handler, store idempotency.AtomicStore, scope idempotency.Scope, localScope string, entry *storedResponse, digest, key string) {
	var commandCapture *responseCapture
	execution, err := store.Execute(request.Context(), scope, digest, time.Now().UTC().Add(24*time.Hour), func(commandContext context.Context) idempotency.Response {
		commandCapture = newResponseCapture()
		next.ServeHTTP(commandCapture, request.WithContext(commandContext))
		if commandCapture.status == 0 {
			commandCapture.status = http.StatusOK
		}
		commandCapture.authority = replayAuthority(request.WithContext(commandContext), commandCapture)
		return idempotency.Response{
			Authority: commandCapture.authority,
			Digest:    digest,
			Status:    commandCapture.status,
			Headers:   retainedResponseHeaders(commandCapture.header),
			Body:      append([]byte(nil), commandCapture.body.Bytes()...),
		}
	})
	switch {
	case err != nil:
		if commandCapture != nil || !atomicReservationRefusal(err) {
			keyDigest := sha256.Sum256([]byte(key))
			diagnosticOwned(request.Context())
			c.logger.Error("atomic command could not be completed", requestLogAttributes(request, "event", "idempotency.completion_failed", "component", "api", "tenant_id", scope.TenantID, "idempotency_key_digest", hex.EncodeToString(keyDigest[:]), "error_code", catalog.ErrorCodeOf(err), "error_type", fmt.Sprintf("%T", err))...)
		}
		commandCapture = newResponseCapture()
		writeProblem(commandCapture, request, err)
	case execution.Pending:
		if execution.ReservationID != "" {
			diagnosticOwned(request.Context())
			c.logger.Warn("idempotent command requires retry or operator inspection", requestLogAttributes(request, "event", "idempotency.inspection_required", "component", "api", "tenant_id", scope.TenantID, "reservation_id", execution.ReservationID)...)
		}
		commandCapture = newResponseCapture()
		writeProblem(commandCapture, request, catalog.E(catalog.CodeDependencyUnavailable, "idempotent command is already running or requires operator repair"))
		commandCapture.header.Set("Retry-After", "1")
	default:
		commandCapture = captureStoredResponse(execution.Response)
	}
	c.finish(localScope, entry, commandCapture, err == nil && !execution.Pending)
	if execution.Replayed && err == nil {
		c.writeReplay(writer, request, commandCapture.status, commandCapture.header, commandCapture.body.Bytes(), true, commandCapture.authority)
		return
	}

	copyHeaders(writer.Header(), commandCapture.header)
	writer.WriteHeader(commandCapture.status)
	_, _ = writer.Write(commandCapture.body.Bytes())
}

// writeReplay applies the selected current-authority disclosure boundary to all
// local, durable-store and atomic replay paths before emitting private bytes.
func (c *idempotencyCache) writeReplay(w http.ResponseWriter, r *http.Request, status int, headers http.Header, body []byte, retained bool, authority idempotency.ReplayAuthority) {
	emit := func() {
		observeReplayedRefusal(r, status, headers, body)
		copyHeaders(w.Header(), headers)
		if retained {
			w.Header().Set("Idempotency-Replayed", "true")
		}
		w.WriteHeader(status)
		_, _ = w.Write(body)
	}
	if c.replayAdmission != nil {
		if err := c.replayAdmission(r, authority, emit); err != nil {
			writeProblem(w, r, err)
		}
		return
	}
	emit()
}

// captureStoredResponse copies a retained reply while removing correlation headers specific to its original attempt.
func captureStoredResponse(response idempotency.Response) *responseCapture {
	capture := newResponseCapture()
	capture.authority = response.Authority
	capture.status = response.Status
	capture.header = http.Header(response.Headers).Clone()
	stripAttemptHeaders(capture.header)
	_, _ = capture.body.Write(response.Body)
	return capture
}

// retainedResponseHeaders clones response headers without attempt-specific request/replay markers.
func retainedResponseHeaders(header http.Header) map[string][]string {
	retained := header.Clone()
	stripAttemptHeaders(retained)
	return map[string][]string(retained)
}

// stripAttemptHeaders removes correlation and replay markers that must be generated for each response attempt.
func stripAttemptHeaders(header http.Header) {
	header.Del("X-Request-ID")
	header.Del("Idempotency-Replayed")
}

// complete is the process-local development-store path. Cleanup ignores client cancellation
// and is bounded because the response cannot share the domain transaction.
func (c *idempotencyCache) complete(ctx context.Context, scope idempotency.Scope, response idempotency.Response) error {
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	var err error
	for attempt := 0; attempt < 3; attempt++ {
		err = c.store.Complete(cleanupCtx, scope, response)
		if err == nil {
			return nil
		}
	}
	return err
}

// finish publishes a copied terminal reply to local waiters and releases its rendezvous entry.
func (c *idempotencyCache) finish(scope string, entry *storedResponse, capture *responseCapture, retained bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry.authority = capture.authority
	entry.status = capture.status
	entry.header = capture.header.Clone()
	stripAttemptHeaders(entry.header)
	entry.body = append([]byte(nil), capture.body.Bytes()...)
	entry.retained = retained
	if current, ok := c.entries[scope]; ok && current == entry {
		delete(c.entries, scope)
	}
	close(entry.done)
}

// failUnfinished releases still-pending local waiters with a retryable failure when retention did not finish.
func (c *idempotencyCache) failUnfinished(scope string, entry *storedResponse, request *http.Request) {
	c.mu.Lock()
	current, ok := c.entries[scope]
	c.mu.Unlock()
	if !ok || current != entry {
		return
	}
	capture := newResponseCapture()
	writeProblem(capture, request, catalog.E(catalog.CodeDependencyUnavailable, "idempotent command ended before a response was retained"))
	capture.header.Set("Retry-After", "1")
	c.mu.Lock()
	defer c.mu.Unlock()
	if current, ok = c.entries[scope]; !ok || current != entry {
		return
	}
	entry.authority = capture.authority
	entry.status = capture.status
	entry.header = capture.header.Clone()
	entry.body = append([]byte(nil), capture.body.Bytes()...)
	delete(c.entries, scope)
	close(entry.done)
}

// requiresIdempotency selects mutating control methods while excluding the explicitly read-only POST searches.
func requiresIdempotency(request *http.Request) bool {
	if !strings.HasPrefix(request.URL.Path, "/api/v1/") {
		return false
	}
	if request.Method != http.MethodPost && request.Method != http.MethodPatch && request.Method != http.MethodDelete {
		return false
	}
	if request.Method == http.MethodPost {
		if strings.HasPrefix(request.URL.Path, "/api/v1/personal/items/") && strings.HasSuffix(request.URL.Path, "/resolve") {
			return false
		}
		switch request.URL.Path {
		case "/api/v1/ratings/read", "/api/v1/artwork/read", "/api/v1/catalog/search", "/api/v1/catalog/explore", "/api/v1/observations:search", "/api/v1/access/capabilities":
			return false
		}
	}
	return true
}

// copyHeaders copies retained values while preserving the current attempt's request/replay markers.
func copyHeaders(target, source http.Header) {
	for name, values := range source {
		if strings.EqualFold(name, "X-Request-ID") || strings.EqualFold(name, "Idempotency-Replayed") {
			continue
		}
		for _, value := range values {
			target.Add(name, value)
		}
	}
}

// replayAuthority captures only decisions from the original admitted operation.
func replayAuthority(r *http.Request, capture *responseCapture) idempotency.ReplayAuthority {
	return idempotency.ReplayAuthority{ValidationProblem: capture.validationProblem && capture.status == http.StatusBadRequest && len(identity.RecordedPermissionChecks(r.Context())) == 0, Version: 1, Checks: identity.RecordedPermissionChecks(r.Context()), JobID: identity.RecordedJobDisclosure(r.Context()), HumanOnly: strings.HasPrefix(r.URL.Path, "/api/v1/personal/items") || (r.URL.Path == "/api/v1/ratings" && r.Method == http.MethodPost || strings.HasPrefix(r.URL.Path, "/api/v1/ratings/") && strings.HasSuffix(r.URL.Path, ":withdraw")) || strings.HasPrefix(r.URL.Path, "/api/v1/feedback") && (r.Method == http.MethodPatch || r.URL.Path == "/api/v1/feedback" || strings.HasSuffix(r.URL.Path, ":withdraw"))}
}
