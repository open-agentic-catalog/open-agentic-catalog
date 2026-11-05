package web

import (
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
)

// serveLegal exposes only reviewed build assets, independently of the manual.
func (s *Server) serveLegal(w http.ResponseWriter, r *http.Request, relative string) {
	var name string
	switch relative {
	case "/legal/LICENSE.txt":
		name = "legal/LICENSE.txt"
	case "/legal/third-party-notices.txt":
		name = "legal/third-party-notices.txt"
	default:
		problem(w, 404, "not_found")
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		problem(w, 405, "method_not_allowed")
		return
	}
	if r.URL.RawQuery != "" || r.URL.ForceQuery {
		problem(w, 400, "invalid_ui_request")
		return
	}
	// Reject links as well as special files; filesystem notices are not a web root.
	for _, item := range []string{"legal", name} {
		info, err := os.Lstat(filepath.Join(s.config.Assets, item))
		if err != nil || info.Mode()&fs.ModeSymlink != 0 {
			problem(w, 404, "not_found")
			return
		}
	}
	info, err := fs.Stat(s.assets, name)
	if err != nil || !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > 8<<20 {
		problem(w, 404, "not_found")
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Content-Disposition", `inline; filename="`+filepath.Base(name)+`"`)
	w.Header().Set("Cache-Control", "no-cache")
	request := r.Clone(r.Context())
	request.URL.Path = "/" + name
	http.FileServerFS(s.assets).ServeHTTP(w, request)
}
