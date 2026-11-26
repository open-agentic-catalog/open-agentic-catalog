// Public discovery forwards three anonymous operations to the existing API authority.
package web

import (
	"bytes"
	"encoding/json"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/jsondoc"
)

// publicDiscovery deliberately never reads a session or copies caller credentials.
// The backend's configured host/audience policy remains authoritative on every read.
func (s *Server) publicDiscovery(w http.ResponseWriter, r *http.Request, route string) {
	for _, header := range []string{"Forwarded", "X-Forwarded-Host", "X-Original-Host"} {
		if len(r.Header.Values(header)) != 0 {
			problem(w, 400, "invalid_ui_request")
			return
		}
	}
	var path, query string
	var body []byte
	switch route {
	case "/_discovery/manifest", "/_discovery/agents":
		if r.Method != http.MethodGet {
			problem(w, 405, "method_not_allowed")
			return
		}
		if r.ContentLength != 0 || len(r.TransferEncoding) != 0 {
			problem(w, 400, "invalid_ui_request")
			return
		}
		path = "/.well-known/ard.json"
		values, err := url.ParseQuery(r.URL.RawQuery)
		if err != nil || r.URL.ForceQuery {
			problem(w, 400, "invalid_ui_request")
			return
		}
		if route == "/_discovery/manifest" {
			if len(values) != 0 {
				problem(w, 400, "invalid_ui_request")
				return
			}
		} else {
			path = "/ard/v1/agents"
			for key, v := range values {
				if len(v) != 1 || (key != "pageSize" && key != "pageToken") {
					problem(w, 400, "invalid_ui_request")
					return
				}
			}
			if values.Has("pageSize") {
				n, err := strconv.Atoi(values.Get("pageSize"))
				if err != nil || n < 1 || n > 100 {
					problem(w, 400, "invalid_ui_request")
					return
				}
			}
			if !publicToken(values.Get("pageToken")) {
				problem(w, 400, "invalid_ui_request")
				return
			}
			query = values.Encode()
		}
	case "/_discovery/search":
		if r.Method != http.MethodPost {
			problem(w, 405, "method_not_allowed")
			return
		}
		media, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if len(r.Header.Values("Content-Type")) != 1 || err != nil || media != "application/json" || len(params) > 1 || (len(params) == 1 && !strings.EqualFold(params["charset"], "utf-8")) {
			problem(w, 415, "invalid_content_type")
			return
		}
		if r.URL.RawQuery != "" || r.URL.ForceQuery {
			problem(w, 400, "invalid_ui_request")
			return
		}
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			requestBodyProblem(w, err)
			return
		}
		var input struct {
			Text      string `json:"text"`
			PageSize  int    `json:"pageSize"`
			PageToken string `json:"pageToken,omitempty"`
		}
		if strictJSON(raw, &input) != nil || strings.TrimSpace(input.Text) == "" || utf8.RuneCountInString(input.Text) > 500 || strings.ContainsRune(input.Text, 0) || !publicToken(input.PageToken) {
			problem(w, 400, "invalid_ui_request")
			return
		}
		size := input.PageSize
		if size < 1 || size > 100 {
			problem(w, 400, "invalid_ui_request")
			return
		}
		// Browser input cannot enable federation or provide arbitrary JSON-LD contexts.
		body, err = json.Marshal(struct {
			Query struct {
				Text string `json:"text"`
			} `json:"query"`
			Federation string `json:"federation"`
			PageSize   int    `json:"pageSize"`
			PageToken  string `json:"pageToken,omitempty"`
		}{Query: struct {
			Text string `json:"text"`
		}{input.Text}, Federation: "none", PageSize: size, PageToken: input.PageToken})
		if err != nil {
			problem(w, 400, "invalid_ui_request")
			return
		}
		path = "/ard/v1/search"
	default:
		problem(w, 404, "not_found")
		return
	}
	request, err := s.apiRequest(r.Context(), r.Method, path, query, bytes.NewReader(body))
	if err != nil {
		problem(w, 400, "invalid_ui_request")
		return
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Cache-Control", "no-cache")
	if r.Method == http.MethodPost {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := s.apiClient.Do(request) // #nosec G704 -- Fixed operator API origin; only the three code-selected ARD paths above are admitted, without redirects or credentials.
	observeAPI(r.Context(), response)
	if err != nil {
		problem(w, 502, "catalog_unavailable")
		return
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode >= 300 && response.StatusCode < 400 {
		problem(w, 502, "catalog_redirect_refused")
		return
	}
	media, _, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
	raw, readErr := io.ReadAll(io.LimitReader(response.Body, s.config.ResponseBytes+1))
	if err != nil || media != "application/json" || readErr != nil || int64(len(raw)) > s.config.ResponseBytes || jsondoc.Validate(raw) != nil || (response.StatusCode < 400 && response.StatusCode != http.StatusOK) {
		problem(w, 502, "invalid_publication_response")
		return
	}
	for _, name := range []string{"ETag", "Last-Modified", "Content-Digest", "Retry-After"} {
		if value := response.Header.Get(name); value != "" {
			w.Header().Set(name, value)
		}
	}
	if id := admittedRequestID(response.Header); id != "" {
		w.Header().Set("X-Request-ID", id)
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(response.StatusCode)
	_, _ = w.Write(raw)
}

// publicToken treats continuation as bounded opaque data, never a route or URL.
func publicToken(value string) bool {
	return len(value) <= 4096 && utf8.ValidString(value) && !strings.ContainsRune(value, 0)
}
