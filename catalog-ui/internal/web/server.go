// Request dispatch separates static navigation, browser session controls and product forwarding.

package web

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"sync/atomic"
	"time"
	"unicode"

	"github.com/gorilla/securecookie"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/buildinfo"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/diagnostics"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/processinfo"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/safeerr"
)

// Server serves one UI deployment. The key ring is immutable after startup;
// rolling replacement with overlapping rings provides shared-key rotation.
type Server struct {
	process       *processinfo.Process
	config        Config
	build         buildinfo.Info
	origin        *url.URL
	upstream      *url.URL
	apiClient     *http.Client
	codecs        []securecookie.Codec
	binding       string
	secure        bool
	users         []developmentUser
	login         *oidcLogin
	assets        fs.FS
	documentation *documentationBundle
	index         []byte
	now           func() time.Time
	forward       http.Handler
	admission     chan struct{}
	draining      atomic.Bool
	logger        *slog.Logger
	dependencies  [2]dependencyDiagnostic
	localWarning  diagnostics.Warning
}

var errCatalogAccessDenied = safeerr.New("catalog access denied")

// New validates all local inputs before becoming ready. Provider discovery is
// performed during explicit OIDC login, not liveness or every static request.
func New(c Config, logger *slog.Logger, identity ...buildinfo.Info) (*Server, error) {
	if err := c.validate(); err != nil {
		return nil, err
	}
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	process, err := processinfo.New()
	if err != nil {
		return nil, err
	}
	s := &Server{process: process, config: c, logger: logger, now: time.Now, admission: make(chan struct{}, 32)}
	s.build = buildinfo.New("dev", "unknown", "unknown")
	if len(identity) > 0 {
		s.build = buildinfo.New(identity[0].Version, identity[0].Commit, identity[0].SourceState)
	}
	s.origin, _ = fixedURL(c.Origin)
	s.upstream, _ = fixedURL(c.Upstream)
	s.secure = s.origin.Scheme == "https"
	if err := s.initCookies(); err != nil {
		return nil, err
	}
	client, err := newClient(s.upstream, c.APICAFile, c.ResponseBytes, time.Duration(c.TimeoutSeconds)*time.Second, false)
	if err != nil {
		return nil, err
	}
	s.apiClient = client
	s.assets = os.DirFS(c.Assets)
	s.index, err = readFile(filepath.Join(c.Assets, "index.html"), 1<<20)
	if err != nil || len(s.index) > 1<<20 {
		return nil, safeerr.New("UI assets need a readable index.html under 1 MiB")
	}
	// Build-generated entry references must exist before readiness. They are
	// local assets, not HTML supplied by Catalog records or remote providers.
	entryAssets := regexp.MustCompile(`(?:src|href)="\./(assets/[A-Za-z0-9_.-]+)"`).FindAllSubmatch(s.index, -1)
	if len(entryAssets) == 0 {
		return nil, safeerr.New("UI entry page has no compiled assets")
	}
	for _, asset := range entryAssets {
		info, err := fs.Stat(s.assets, string(asset[1]))
		if err != nil || !info.Mode().IsRegular() || info.Size() == 0 {
			return nil, safeerr.New("UI entry asset is missing or empty")
		}
	}
	s.index = bytes.ReplaceAll(s.index, []byte(`="./assets/`), []byte(`="`+c.BasePath+`/assets/`))
	if c.AuthMode == "development" {
		if err := readJSONFile(c.DevelopmentUsersFile, &s.users); err != nil || len(s.users) < 1 || len(s.users) > 32 {
			return nil, safeerr.New("UI development users must contain 1..32 fixed users")
		}
		seen := map[string]bool{}
		for _, user := range s.users {
			if user.ProfileGroup != "" && user.ProfileGroup != "bundle" && user.ProfileGroup != "template" {
				return nil, safeerr.New("invalid UI development profile group")
			}
			if !identifier.MatchString(user.Alias) || !validToken(user.Token) || strings.TrimSpace(user.Label) == "" || len(user.Label) > 100 || len(user.Description) > 300 || strings.IndexFunc(user.Label+user.Description, unicode.IsControl) >= 0 || seen[user.Alias] {
				return nil, safeerr.New("invalid or duplicate UI development user")
			}
			seen[user.Alias] = true
		}
	} else {
		s.login, err = s.newOIDCLogin()
		if err != nil {
			return nil, err
		}
	}
	s.forward, err = s.proxyRoutes()
	if err != nil {
		return nil, err
	}
	s.documentation, err = loadDocumentation(c.Assets)
	if err != nil {
		s.Close()
		return nil, err
	}
	return s, nil
}

// Drain makes readiness fail and stops admitting new authenticated work.
// In-flight requests remain governed by the HTTP server's shutdown deadline.
func (s *Server) Drain() {
	if !s.draining.Swap(true) {
		s.logger.Info("UI host draining", "event", "process.draining", "component", "ui")
	}
}

// Close releases idle outbound connections after the listener has drained.
func (s *Server) Close() {
	if s.documentation != nil {
		_ = s.documentation.root.Close()
	}
	if t, ok := s.apiClient.Transport.(fixedTransport); ok {
		t.base.CloseIdleConnections()
	}
	if s.login != nil {
		if t, ok := s.login.client.Transport.(fixedTransport); ok {
			t.base.CloseIdleConnections()
		}
	}
}

// serveHTTP enforces deployment paths, browser-origin checks and bounded admission.
// Root health endpoints report local process state without contacting dependencies.
func (s *Server) serveHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'self'; style-src 'self'; img-src 'self' data: blob:; font-src 'self'; connect-src 'self'; base-uri 'none'; frame-ancestors 'none'; form-action 'self'")
	w.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
	if r.URL.Path == "/livez" || r.URL.Path == "/readyz" {
		if r.Method != http.MethodGet {
			problem(w, 405, "method_not_allowed")
			return
		}
		if r.URL.Path == "/readyz" && s.draining.Load() {
			problem(w, http.StatusServiceUnavailable, "ui_draining")
			return
		}
		writeJSON(w, 200, map[string]string{"status": "ok"})
		return
	}
	if r.Host != s.origin.Host || r.URL.RawPath != "" || strings.ContainsAny(r.URL.Path, "\\\x00") || path.Clean(r.URL.Path) != strings.TrimSuffix(r.URL.Path, "/") && r.URL.Path != "/" {
		problem(w, 400, "invalid_ui_request")
		return
	}
	if r.URL.Path == "/docs" || strings.HasPrefix(r.URL.Path, "/docs/") {
		s.serveDocumentation(w, r)
		return
	}
	if !strings.HasPrefix(r.URL.Path, s.config.BasePath+"/") {
		problem(w, 404, "not_found")
		return
	}
	relative := strings.TrimPrefix(r.URL.Path, s.config.BasePath)
	if strings.HasPrefix(relative, "/legal/") {
		s.serveLegal(w, r, relative)
		return
	}
	if strings.HasPrefix(relative, "/assets/") {
		s.serveAsset(w, r, relative)
		return
	}
	if relative == "/runtime.json" && r.Method == http.MethodGet {
		documentationURL := s.config.DocumentationURL
		if documentationURL == "" && s.documentation != nil {
			documentationURL = "/docs/"
		}
		writeJSON(w, 200, map[string]string{"basePath": s.config.BasePath, "authMode": s.config.AuthMode, "documentationUrl": documentationURL})
		return
	}
	if relative == "/auth/callback" && r.Method == http.MethodGet && s.login != nil {
		if !s.admit(w) {
			return
		}
		defer func() { <-s.admission }()
		s.callback(w, r)
		return
	}
	if strings.HasPrefix(relative, "/auth/") || strings.HasPrefix(relative, "/_catalog/") || (relative == "/_reference" || relative == "/_build" || relative == "/_status") {
		if !s.sameOrigin(r) {
			problem(w, 403, "same_origin_required")
			return
		}
		if !s.admit(w) {
			return
		}
		defer func() { <-s.admission }()
		r.Body = http.MaxBytesReader(w, r.Body, s.config.RequestBytes)
		switch relative {
		case "/_status":
			if r.Method != http.MethodGet {
				break
			}
			if r.URL.RawQuery != "" || r.URL.ForceQuery {
				problem(w, http.StatusBadRequest, "invalid_ui_request")
				return
			}
			writeJSON(w, http.StatusOK, struct {
				buildinfo.Info
				processinfo.Snapshot
				Role string `json:"role"`
			}{s.build, s.process.Read(), "ui"})
			return
		case "/_build":
			if r.Method != http.MethodGet {
				break
			}
			writeJSON(w, http.StatusOK, s.build)
			return
		case "/_reference":
			if r.Method != http.MethodGet {
				break
			}
			s.reference(w, r)
			return
		case "/auth/options":
			if r.Method != http.MethodGet {
				break
			}
			type choice struct {
				Alias          string `json:"alias"`
				Label          string `json:"label"`
				Description    string `json:"description,omitempty"`
				ProfileGroup   string `json:"profileGroup,omitempty"`
				Elevated       bool   `json:"elevated,omitempty"`
				SeparateTenant bool   `json:"separateTenant,omitempty"`
			}
			choices := make([]choice, 0, len(s.users))
			for _, u := range s.users {
				choices = append(choices, choice{u.Alias, u.Label, u.Description, u.ProfileGroup, u.Elevated, u.SeparateTenant})
			}
			writeJSON(w, 200, map[string]any{"mode": s.config.AuthMode, "users": choices})
			return
		case "/auth/login":
			if r.Method != http.MethodPost {
				break
			}
			s.startLogin(w, r)
			return
		case "/auth/logout":
			if r.Method != http.MethodPost {
				break
			}
			s.clearCookie(w, s.config.CookieName)
			w.WriteHeader(http.StatusNoContent)
			return
		case "/auth/session":
			if r.Method != http.MethodGet {
				break
			}
			s.currentSession(w, r)
			return
		default:
			if strings.HasPrefix(relative, "/_catalog/") {
				s.forward.ServeHTTP(w, r)
				return
			}
		}
		problem(w, 404, "not_found")
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		problem(w, 405, "method_not_allowed")
		return
	}
	// Only client navigation may receive the SPA; API and asset typos stay errors.
	if !clientNavigation(relative) {
		problem(w, 404, "not_found")
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if r.Method != http.MethodHead {
		_, _ = w.Write(s.index)
	}
}

// clientNavigation permits only implemented client routes. Asset, API and
// unknown section paths must never fall through to a successful HTML response.
var caseIdentifier = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

func clientNavigation(relative string) bool {
	if relative == "/" || relative == "/access" || relative == "/service" || relative == "/api-reference" || relative == "/catalog" || strings.HasPrefix(relative, "/catalog/") {
		return true
	}
	parts := strings.Split(strings.TrimPrefix(relative, "/"), "/")
	if len(parts) < 1 || len(parts) > 2 {
		return false
	}
	switch parts[0] {
	case "feedback", "my-feedback", "ratings", "my-ratings":
		return len(parts) == 1 || caseIdentifier.MatchString(parts[1])
	case "overview", "my-work", "about", "getting-started", "troubleshooting", "settings", "indexed-record":
		return len(parts) == 1
	case "saved-items", "trust-assessments", "access-roles", "access-groups", "access-assignments", "namespaces", "publishers", "jobs", "resources", "versions", "version-revisions", "listings", "listing-revisions", "reviews", "releases", "members", "publication", "audit", "explore", "webhooks", "sources", "inspections", "source-snapshots", "observations", "observation-group", "federation-search":
		return len(parts) == 1 || parts[1] != ""
	default:
		return false
	}
}

// admit rejects drain or saturation without queueing; successful callers release one slot.
func (s *Server) admit(w http.ResponseWriter) bool {
	if s.draining.Load() {
		problem(w, http.StatusServiceUnavailable, "ui_draining")
		return false
	}
	select {
	case s.admission <- struct{}{}:
		return true
	default:
		problem(w, http.StatusServiceUnavailable, "ui_busy")
		return false
	}
}

// sameOrigin requires the UI request header and validates Origin and Fetch Metadata.
func (s *Server) sameOrigin(r *http.Request) bool {
	// The non-simple header is required even for audited GETs. Cross-origin
	// scripts cannot set it without a preflight, and this server never grants CORS.
	if r.Header.Get("X-Catalog-Request") != "1" {
		return false
	}
	if origin := r.Header.Get("Origin"); origin != "" && origin != s.config.Origin {
		return false
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead && r.Header.Get("Origin") != s.config.Origin {
		return false
	}
	if site := r.Header.Get("Sec-Fetch-Site"); site != "" && site != "same-origin" {
		return false
	}
	return true
}

// serveAsset serves regular build assets with immutable caching and no directory listing.
func (s *Server) serveAsset(w http.ResponseWriter, r *http.Request, relative string) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		problem(w, 405, "method_not_allowed")
		return
	}
	name := strings.TrimPrefix(relative, "/")
	if !fs.ValidPath(name) || strings.Contains(name, "/.") {
		problem(w, 404, "not_found")
		return
	}
	info, err := fs.Stat(s.assets, name)
	if err != nil || !info.Mode().IsRegular() {
		problem(w, 404, "not_found")
		return
	}
	w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	assetRequest := r.Clone(r.Context())
	assetRequest.URL.Path = "/" + name
	http.FileServerFS(s.assets).ServeHTTP(w, assetRequest)
}

// currentSession resolves fresh Catalog access without disclosing the retained bearer.
func (s *Server) currentSession(w http.ResponseWriter, r *http.Request) {
	if len(r.CookiesNamed(s.config.CookieName)) == 0 {
		writeJSON(w, 200, map[string]any{"access": nil, "expiresAt": 0})
		return
	}
	value, err := s.readSession(r)
	if err != nil {
		problem(w, 401, "login_required")
		return
	}
	access, err := s.access(r.Context(), value.Token)
	if err != nil {
		accessProblem(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"access": json.RawMessage(access), "expiresAt": value.Expires})
}

// access asks the Catalog to authenticate a bearer and validates its bounded self-context.
func (s *Server) access(ctx context.Context, token string) ([]byte, error) {
	r, err := s.apiRequest(ctx, http.MethodGet, "/api/v1/me", "", nil)
	if err != nil {
		return nil, safeerr.New("invalid UI API request")
	}
	r.Header.Set("Authorization", "Bearer "+token)
	r.Header.Set("Accept", "application/json")
	response, err := s.apiClient.Do(r) // #nosec G704 -- apiRequest supplies the fixed /api/v1/me path; fixedTransport enforces the configured origin and refuses redirects.
	observeAPI(ctx, response)
	if err != nil {
		return nil, safeerr.New("catalog login unavailable")
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		if response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden {
			return nil, errCatalogAccessDenied
		}
		observeAPI(ctx, nil)
		return nil, safeerr.New("catalog login unavailable")
	}
	// Read one extra byte so a valid prefix cannot hide an oversized response.
	const limit = 64 << 10
	raw, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
	var access struct {
		TenantID     string `json:"tenantId"`
		Subject      string `json:"subject"`
		MembershipID string `json:"membershipId"`
		ActorType    string `json:"actorType"`
	}
	if err != nil || len(raw) > limit || json.Unmarshal(raw, &access) != nil || access.TenantID == "" || access.MembershipID == "" || access.Subject == "" {
		observeAPI(ctx, nil)
		return nil, safeerr.New("catalog self-context is invalid")
	}
	return raw, nil
}

// accessProblem distinguishes denied login from dependency failure without leaking causes.
func accessProblem(w http.ResponseWriter, err error) {
	if errors.Is(err, errCatalogAccessDenied) {
		problem(w, http.StatusUnauthorized, "catalog_login_denied")
		return
	}
	problem(w, http.StatusServiceUnavailable, "catalog_unavailable")
}

// writeJSON writes an application/json response for a handler-owned value.
func writeJSON(w http.ResponseWriter, status int, value any) {
	writeJSONType(w, status, "application/json", value)
}

// problem emits a fixed-code host error without including private upstream diagnostics.
func problem(w http.ResponseWriter, status int, code string) {
	if observed, ok := w.(*hostResponse); ok {
		observed.code = code
	}
	writeJSONType(w, status, "application/problem+json", map[string]any{"type": "about:blank", "title": http.StatusText(status), "status": status, "code": code})
}

// writeJSONType marshals a handler-owned value before committing response headers.
func writeJSONType(w http.ResponseWriter, status int, mediaType string, value any) {
	raw, err := json.Marshal(value)
	if err != nil {
		if observed, ok := w.(*hostResponse); ok {
			observed.code = "response_encoding_failed"
		}
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", mediaType)
	w.WriteHeader(status)
	_, _ = w.Write(raw)
}
