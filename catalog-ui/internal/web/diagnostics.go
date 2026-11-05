// Host diagnostics project fixed outcomes without collecting private response bodies.
package web

import (
	"context"
	"crypto/rand"
	"net/http"
	"regexp"
	"strings"
	"sync/atomic"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/diagnostics"
)

type observationKey struct{}

type hostResponse struct {
	http.ResponseWriter
	status, apiOutcome           int // API outcome: zero unobserved, one responding, two unavailable.
	code, requestID, upstreamID  string
	sessionEstablished, issuerOK bool
	issuerFailed                 atomic.Bool
}

func (w *hostResponse) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (w *hostResponse) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
		w.ResponseWriter.WriteHeader(status)
	}
}

func (w *hostResponse) Write(raw []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(raw)
}

var safeRequestID = regexp.MustCompile(`^[A-Za-z0-9._-]{1,128}$`)

func admittedRequestID(header http.Header) string {
	values := header.Values("X-Request-ID")
	if len(values) == 1 && safeRequestID.MatchString(values[0]) {
		return values[0]
	}
	return ""
}

// ServeHTTP observes the host result once. Panic diagnostics stay with the
// listener's recovery owner; no response body or raw error is captured here.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	id := admittedRequestID(r.Header)
	if id == "" {
		id = rand.Text()
	}
	w.Header().Set("X-Request-ID", id)
	observed := &hostResponse{ResponseWriter: w, requestID: id}
	r = r.WithContext(context.WithValue(r.Context(), observationKey{}, observed))
	s.serveHTTP(observed, r)
	s.logResponse(r, observed)
}

func hostObservation(ctx context.Context) *hostResponse {
	observed, _ := ctx.Value(observationKey{}).(*hostResponse)
	return observed
}

func observeAPI(ctx context.Context, response *http.Response) {
	if observed := hostObservation(ctx); observed != nil {
		observed.apiOutcome = 2
		if response != nil {
			observed.upstreamID = admittedRequestID(response.Header)
			if response.StatusCode < 500 && response.StatusCode != http.StatusTooManyRequests {
				observed.apiOutcome = 1
			}
		}
	}
}

// The issuer SDK can obscure transport errors inside token-verification errors.
// Observe only transport/status failure, without changing its fixed transport,
// redirect, timeout, cancellation or credential rules. Key retrieval may run in
// an SDK goroutine, so its single outcome flag is atomic. Failed discovery/key
// GETs require the SDK's exact 200 response; the exchange owner classifies OAuth
// POST refusals without treating a normal invalid grant as an issuer outage.
type issuerTransport struct {
	http.RoundTripper
	failed *atomic.Bool
}

func (t issuerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	response, err := t.RoundTripper.RoundTrip(r)
	if err != nil || r.Method == http.MethodGet && response.StatusCode != http.StatusOK || response.StatusCode >= 500 || response.StatusCode == http.StatusTooManyRequests || response.StatusCode >= 300 && response.StatusCode < 400 {
		t.failed.Store(true)
	}
	return response, err
}

func (s *Server) issuerContext(ctx context.Context) context.Context {
	client := *s.login.client
	if observed := hostObservation(ctx); observed != nil {
		client.Transport = issuerTransport{client.Transport, &observed.issuerFailed}
	}
	return oidc.ClientContext(ctx, &client)
}

type dependencyDiagnostic struct {
	warning diagnostics.Warning
	failed  atomic.Bool
}

func (s *Server) logResponse(r *http.Request, observed *hostResponse) {
	if observed.status == 0 {
		observed.status = http.StatusOK
	}
	attrs := []any{"request_id", observed.requestID, "route", s.routeFamily(r.URL.Path), "status", observed.status}
	if observed.upstreamID != "" {
		attrs = append(attrs, "upstream_request_id", observed.upstreamID)
	}
	if r.Context().Err() == context.Canceled {
		s.logger.Debug("UI request canceled", append(attrs, "event", "ui.request_failed", "component", "ui", "error_code", "request_canceled")...)
		return
	}
	issuerOutcome := 0
	if observed.issuerOK {
		issuerOutcome = 1
	}
	if observed.issuerFailed.Load() || observed.code == "identity_provider_unavailable" {
		issuerOutcome = 2
	}
	dependent := false
	for i, outcome := range []int{observed.apiOutcome, issuerOutcome} {
		component := []string{"ui_api", "ui_issuer"}[i]
		state := &s.dependencies[i]
		if outcome == 2 {
			dependent = true
			state.failed.Store(true)
			state.warning.Warn(s.now(), s.logger, "UI dependency unavailable", append(append([]any{}, attrs...), "event", "dependency.unavailable", "component", component, "error_code", "dependency_unavailable")...)
		} else if outcome == 1 && state.failed.Swap(false) {
			s.logger.Info("UI dependency responded", append(append([]any{}, attrs...), "event", "dependency.recovered", "component", component)...)
		}
	}
	if dependent {
		return
	}
	if observed.code == "" && observed.status >= 400 {
		observed.code = "request_rejected"
		if observed.apiOutcome == 1 {
			observed.code = "catalog_rejected"
		}
	}
	if observed.code != "" {
		attrs = append(attrs, "event", "ui.request_failed", "component", "ui", "error_code", observed.code)
		switch {
		case observed.code == "session_token_too_large" || observed.code == "ui_busy":
			s.localWarning.Warn(s.now(), s.logger, "UI request could not complete", attrs...)
		case observed.status >= 500 && observed.code != "ui_draining":
			s.logger.Error("UI request failed", attrs...)
		default:
			s.logger.Debug("UI request rejected", attrs...)
		}
	} else if observed.sessionEstablished {
		s.logger.Debug("UI session established", append(attrs, "event", "ui.session_established", "component", "ui")...)
	}
}

func (s *Server) routeFamily(p string) string {
	if p == "/livez" || p == "/readyz" {
		return "health"
	}
	if !strings.HasPrefix(p, s.config.BasePath+"/") {
		return "unmatched"
	}
	p = strings.TrimPrefix(p, s.config.BasePath)
	switch p {
	case "/auth/login", "/auth/callback", "/auth/logout", "/auth/session", "/auth/options", "/runtime.json", "/_reference":
		return p
	}
	if strings.HasPrefix(p, "/_catalog/") {
		return "catalog"
	}
	if strings.HasPrefix(p, "/assets/") {
		return "assets"
	}
	if clientNavigation(p) {
		return "navigation"
	}
	return "unmatched"
}
