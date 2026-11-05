// OpenAPI delivery serves the embedded reviewed contract with a content digest and
// conditional caching.

package httpserver

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strconv"

	serviceapi "github.com/open-agentic-catalog/open-agentic-catalog/api"
)

// handleOpenAPI serves exact embedded contract bytes with their digest, ETag and conditional response.
func (s *Server) handleOpenAPI(writer http.ResponseWriter, request *http.Request) {
	content := serviceapi.ControlV1
	digest := sha256.Sum256(content)
	etag := `"` + hex.EncodeToString(digest[:]) + `"`
	writer.Header().Set("Content-Type", "application/yaml; charset=utf-8")
	writer.Header().Set("Content-Length", strconv.Itoa(len(content)))
	writer.Header().Set("Cache-Control", "public, max-age=300")
	writer.Header().Set("ETag", etag)
	setContentDigest(writer, content)
	if request.Header.Get("If-None-Match") == etag {
		writer.WriteHeader(http.StatusNotModified)
		return
	}
	writer.WriteHeader(http.StatusOK)
	_, _ = writer.Write(content)
}
