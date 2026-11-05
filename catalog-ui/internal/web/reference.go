// Contract browsing projects the connected API document without fetching external references.

package web

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"go.yaml.in/yaml/v3"
)

// reference projects the connected Catalog's contract for read-only browsing.
// It follows no schema references and uses the same fixed-origin transport as
// product requests. The exact source is retained for download.
func (s *Server) reference(w http.ResponseWriter, r *http.Request) {
	session, err := s.readSession(r)
	if err != nil {
		problem(w, http.StatusUnauthorized, "login_required")
		return
	}
	request, err := s.apiRequest(r.Context(), http.MethodGet, "/openapi/control-v1.yaml", "", nil)
	if err != nil {
		problem(w, http.StatusBadGateway, "catalog_unavailable")
		return
	}
	request.Header.Set("Authorization", "Bearer "+session.Token)
	response, err := s.apiClient.Do(request) // #nosec G704 -- Fixed operator origin and literal contract path; transport refuses redirects.
	observeAPI(r.Context(), response)
	if err != nil {
		problem(w, http.StatusBadGateway, "catalog_unavailable")
		return
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		observeAPI(r.Context(), nil)
		problem(w, http.StatusBadGateway, "catalog_contract_unavailable")
		return
	}
	// Keep YAML parsing and its JSON projection small even when deployments
	// permit larger artifact downloads through the normal proxy.
	const limit = 1 << 20
	source, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if err != nil || len(source) > limit {
		observeAPI(r.Context(), nil)
		problem(w, http.StatusBadGateway, "invalid_catalog_contract")
		return
	}
	var document map[string]any
	decoder := yaml.NewDecoder(bytes.NewReader(source))
	err = decoder.Decode(&document)
	var trailing any
	if err == nil && !errors.Is(decoder.Decode(&trailing), io.EOF) {
		observeAPI(r.Context(), nil)
		problem(w, http.StatusBadGateway, "invalid_catalog_contract")
		return
	}
	version, _ := document["openapi"].(string)
	paths, hasPaths := document["paths"].(map[string]any)
	if err != nil || !strings.HasPrefix(version, "3.") || !hasPaths || len(paths) == 0 {
		observeAPI(r.Context(), nil)
		problem(w, http.StatusBadGateway, "invalid_catalog_contract")
		return
	}
	encoded, err := json.Marshal(map[string]any{"document": document, "yaml": string(source)})
	if err != nil || len(encoded) > 4*limit {
		observeAPI(r.Context(), nil)
		problem(w, http.StatusBadGateway, "invalid_catalog_contract")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(encoded)
}
