// Product forwarding preserves command headers while keeping browser credentials local.

package web

import (
	"bytes"
	"io"
	"net/http"
	"regexp"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/open-agentic-catalog/open-agentic-catalog/api"
)

// proxyRoute matches one method and full path from the embedded product contract.
type proxyRoute struct {
	method string
	path   *regexp.Regexp
}

// proxyRoutes derives an exact method/path allowlist from the embedded product
// contract. Health/metrics, arbitrary URLs and worker operations are excluded.
func (s *Server) proxyRoutes() (http.Handler, error) {
	var doc struct {
		Paths map[string]map[string]any `yaml:"paths"`
	}
	if err := yaml.Unmarshal(api.ControlV1, &doc); err != nil {
		return nil, err
	}
	var routes []proxyRoute
	parameter := regexp.MustCompile(`\\\{[^{}]+\\\}`)
	for path, methods := range doc.Paths {
		if !strings.HasPrefix(path, "/api/v1/") && path != "/version" && path != "/openapi/control-v1.yaml" {
			continue
		}
		pattern := "^" + parameter.ReplaceAllString(regexp.QuoteMeta(path), `[^/]+`) + "$"
		for method := range methods {
			switch method {
			case "get", "post", "put", "patch", "delete":
				routes = append(routes, proxyRoute{strings.ToUpper(method), regexp.MustCompile(pattern)})
			}
		}
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := strings.TrimPrefix(r.URL.Path, s.config.BasePath+"/_catalog")
		for _, route := range routes {
			if route.method == r.Method && route.path.MatchString(p) {
				s.proxy(w, r, p)
				return
			}
		}
		problem(w, 404, "not_found")
	}), nil
}

// proxy forwards an allowlisted request using only the session bearer and selected headers.
// ServeHTTP bounds the browser body; fixedTransport bounds the upstream response.
func (s *Server) proxy(w http.ResponseWriter, r *http.Request, path string) {
	session, err := s.readSession(r)
	if err != nil {
		problem(w, 401, "login_required")
		return
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		problem(w, 413, "request_too_large")
		return
	}
	request, err := s.apiRequest(r.Context(), r.Method, path, r.URL.RawQuery, bytes.NewReader(body))
	if err != nil {
		problem(w, 400, "invalid_request")
		return
	}
	for _, name := range []string{"Accept", "Content-Type", "If-Match", "If-None-Match", "Idempotency-Key"} {
		if values := r.Header.Values(name); len(values) > 1 {
			problem(w, 400, "ambiguous_header")
			return
		} else if len(values) == 1 {
			request.Header.Set(name, values[0])
		}
	}
	request.Header.Set("Authorization", "Bearer "+session.Token)
	response, err := s.apiClient.Do(request) // #nosec G704 -- The contract allowlist supplies only a path; fixedTransport enforces the immutable operator-selected origin and refuses redirects.
	observeAPI(r.Context(), response)
	if err != nil {
		problem(w, 502, "catalog_unavailable")
		return
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode >= 300 && response.StatusCode < 400 && response.StatusCode != http.StatusNotModified {
		observeAPI(r.Context(), nil)
		problem(w, 502, "catalog_redirect_refused")
		return
	}
	for _, name := range []string{"Content-Type", "Content-Disposition", "Content-Digest", "ETag", "Idempotency-Replayed", "Retry-After", "Last-Modified"} {
		if value := response.Header.Get(name); value != "" {
			w.Header().Set(name, value)
		}
	}
	if id := admittedRequestID(response.Header); id != "" {
		w.Header().Set("X-Request-ID", id)
	}
	if location := response.Header.Get("Location"); location != "" {
		if strings.HasPrefix(location, "/api/v1/") && !strings.ContainsAny(location, "\\\r\n") {
			w.Header().Set("Location", s.config.BasePath+"/_catalog"+location)
		}
	}
	if strings.Contains(response.Header.Get("Content-Type"), "text/html") || strings.Contains(response.Header.Get("Content-Type"), "image/svg") {
		w.Header().Set("Content-Disposition", `attachment; filename="catalog-document"`)
	}
	w.WriteHeader(response.StatusCode)
	_, _ = io.Copy(w, response.Body)
}
