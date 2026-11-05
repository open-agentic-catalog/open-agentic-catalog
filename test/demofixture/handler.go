// Package demofixture serves embedded synthetic archives and artwork for local development and tests.
package demofixture

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/demodata"
	"github.com/open-agentic-catalog/open-agentic-catalog/samples"
	"net/http"
)

// Handler serves only fixed archives and owned artwork. It never serves a filesystem or runs components.
func Handler() http.Handler {
	files := map[string][]byte{}
	for _, item := range demodata.DefaultPlan("expanded", "").Resources {
		if len(item.Archive) > 0 {
			files["/"+item.Resource.Key+".zip"] = item.Archive
		}
	}
	return PreparedHandler(files)
}

// PreparedHandler snapshots validated bytes; HTTP requests cannot select filesystem paths.
func PreparedHandler(files map[string][]byte) http.Handler {
	snapshot := map[string][]byte{}
	for path, raw := range files {
		snapshot[path] = bytes.Clone(raw)
	}
	mediaTypes := map[string]string{"/artwork-icon.png": "image/png", "/artwork-screen.jpg": "image/jpeg"}
	for path, file := range map[string]string{"/artwork-icon.png": "artwork/icon.png", "/artwork-screen.jpg": "artwork/screenshot.jpg"} {
		raw, err := samples.Files.ReadFile(file)
		if err != nil {
			panic("missing owned artwork fixture")
		}
		snapshot[path] = raw
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, ok := snapshot[r.URL.Path]
		digest := fmt.Sprintf("sha256:%x", sha256.Sum256(raw))
		if !ok || r.URL.RawQuery != "" || r.URL.ForceQuery || r.URL.RawPath != "" || r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" {
			http.Error(w, "Not found", http.StatusNotFound)
			return
		}
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", "GET")
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		mediaType := mediaTypes[r.URL.Path]
		if mediaType == "" {
			mediaType = catalog.PluginMediaZIP
		}
		w.Header().Set("Content-Type", mediaType)
		w.Header().Set("Content-Disposition", "attachment")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("ETag", `"`+digest+`"`)
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Content-Length", fmt.Sprint(len(raw)))
		_, _ = w.Write(raw) // #nosec G705 -- only fixed owned ZIP/PNG/JPEG fixture bytes, served as an attachment with nosniff; no request content is reflected.
	})
}
