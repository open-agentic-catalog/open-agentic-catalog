package web

import (
	"encoding/base64"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/safeerr"
)

const documentationManifestName = "documentation-bundle.json"

type documentationFile struct {
	Path   string `json:"path"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

type documentationManifest struct {
	SchemaVersion int                 `json:"schemaVersion"`
	Base          string              `json:"base"`
	LockSHA256    string              `json:"lockSha256"`
	Files         []documentationFile `json:"files"`
	ScriptHashes  []string            `json:"scriptHashes"`
	StyleHashes   []string            `json:"styleHashes"`
}

type documentationBundle struct {
	root  *os.Root
	files map[string]documentationFile
	csp   string
}

var documentationPath = regexp.MustCompile(`^[A-Za-z0-9_/-][A-Za-z0-9_./-]*$`)
var documentationDigest = regexp.MustCompile(`^[a-f0-9]{64}$`)
var documentationImmutable = regexp.MustCompile(`^_astro/[^/]+\.[A-Za-z0-9_-]{8,}\.(?:js|css|woff2?|png|webp|svg)$`)
var errDocumentation = safeerr.New("UI documentation bundle is invalid or exceeds its bounds")

func validDocumentationPath(name string) bool {
	if !fs.ValidPath(name) || !documentationPath.MatchString(name) || len(name) > 512 || name == documentationManifestName {
		return false
	}
	for _, segment := range strings.Split(name, "/") {
		if strings.HasPrefix(segment, ".") {
			return false
		}
	}
	return true
}

func documentationPolicy(m documentationManifest) (string, error) {
	policy := "default-src 'none'; script-src 'self' 'wasm-unsafe-eval'"
	for i, hashes := range [][]string{m.ScriptHashes, m.StyleHashes} {
		if len(hashes) > 64 {
			return "", errDocumentation
		}
		if i == 1 {
			policy += "; style-src 'self'"
		}
		seen := map[string]bool{}
		for _, hash := range hashes {
			if !strings.HasPrefix(hash, "sha256-") || seen[hash] {
				return "", errDocumentation
			}
			decoded, err := base64.StdEncoding.Strict().DecodeString(strings.TrimPrefix(hash, "sha256-"))
			if err != nil || len(decoded) != 32 || "sha256-"+base64.StdEncoding.EncodeToString(decoded) != hash {
				return "", errDocumentation
			}
			seen[hash] = true
			policy += " '" + hash + "'"
		}
	}
	policy += "; style-src-attr 'unsafe-inline'; img-src 'self' data:; font-src 'self'; connect-src 'self'; worker-src 'self'; object-src 'none'; base-uri 'none'; frame-ancestors 'none'; form-action 'self'"
	if len(policy) > 8192 {
		return "", errDocumentation
	}
	return policy, nil
}

// The bundle is optional for source/test fixtures. Present malformed bundles
// fail startup. Image inspection verifies every hash; startup bounds metadata
// and file admission without hashing the entire manual on each process start.
func loadDocumentation(assets string) (*documentationBundle, error) {
	directory := filepath.Join(assets, "docs")
	info, err := os.Lstat(directory)
	if os.IsNotExist(err) {
		return nil, nil //nolint:nilnil // Missing bundle deliberately means unavailable in source/test fixtures.
	}
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, errDocumentation
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		return nil, errDocumentation
	}
	d := &documentationBundle{root: root, files: map[string]documentationFile{}}
	ok := false
	defer func() {
		if !ok {
			_ = root.Close()
		}
	}()
	data, err := d.read(documentationManifestName, 2<<20)
	if err != nil {
		return nil, errDocumentation
	}
	var m documentationManifest
	if err = strictJSON(data, &m); err != nil {
		return nil, errDocumentation
	}
	if m.SchemaVersion != 1 || m.Base != "/docs/" || !documentationDigest.MatchString(m.LockSHA256) || len(m.Files) < 1 || len(m.Files) > 10000 || m.ScriptHashes == nil || m.StyleHashes == nil {
		return nil, errDocumentation
	}
	d.csp, err = documentationPolicy(m)
	if err != nil {
		return nil, err
	}
	var total int64
	for _, file := range m.Files {
		if !validDocumentationPath(file.Path) || !documentationDigest.MatchString(file.SHA256) || file.Size < 0 || file.Size > 32<<20 {
			return nil, errDocumentation
		}
		if _, exists := d.files[file.Path]; exists {
			return nil, errDocumentation
		}
		total += file.Size
		if total > 256<<20 {
			return nil, errDocumentation
		}
		info, err := d.regular(file.Path)
		if err != nil || info.Size() != file.Size {
			return nil, errDocumentation
		}
		d.files[file.Path] = file
	}
	if _, exists := d.files["index.html"]; !exists {
		return nil, errDocumentation
	}
	ok = true
	return d, nil
}

func (d *documentationBundle) regular(name string) (fs.FileInfo, error) {
	segments := strings.Split(name, "/")
	for i := range segments {
		info, err := d.root.Lstat(strings.Join(segments[:i+1], "/"))
		if err != nil || info.Mode()&os.ModeSymlink != 0 || (i < len(segments)-1 && !info.IsDir()) || (i == len(segments)-1 && !info.Mode().IsRegular()) {
			return nil, errDocumentation
		}
		if i == len(segments)-1 {
			return info, nil
		}
	}
	return nil, errDocumentation
}

func (d *documentationBundle) read(name string, limit int64) ([]byte, error) {
	info, err := d.regular(name)
	if err != nil || info.Size() > limit {
		return nil, errDocumentation
	}
	// Root enforces containment even if a directory changes after Lstat.
	f, err := d.root.Open(name)
	if err != nil {
		return nil, errDocumentation
	}
	defer func() { _ = f.Close() }()
	info, err = f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > limit {
		return nil, errDocumentation
	}
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil || int64(len(data)) > limit {
		return nil, errDocumentation
	}
	return data, nil
}

// No browser session, dependency or API is read by this public static handler.
func (s *Server) serveDocumentation(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		problem(w, 405, "method_not_allowed")
		return
	}
	if s.documentation == nil {
		problem(w, 404, "not_found")
		return
	}
	name := strings.TrimPrefix(r.URL.Path, "/docs/")
	if r.URL.Path == "/docs" {
		http.Redirect(w, r, "/docs/", http.StatusMovedPermanently)
		return
	}
	if name == "" || strings.HasSuffix(name, "/") {
		name += "index.html"
	}
	if !validDocumentationPath(name) {
		problem(w, 404, "not_found")
		return
	}
	file, exists := s.documentation.files[name]
	if !exists {
		if _, directory := s.documentation.files[name+"/index.html"]; directory {
			http.Redirect(w, r, "/docs/"+name+"/", http.StatusMovedPermanently) // #nosec G710 -- Canonical allowlisted relative path, prefixed by the fixed local /docs/ route.
			return
		}
		problem(w, 404, "not_found")
		return
	}
	info, err := s.documentation.regular(name)
	if err != nil || info.Size() != file.Size {
		problem(w, 404, "not_found")
		return
	}
	f, err := s.documentation.root.Open(name)
	if err != nil {
		problem(w, 404, "not_found")
		return
	}
	defer func() { _ = f.Close() }()
	info, err = f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() != file.Size {
		problem(w, 404, "not_found")
		return
	}
	w.Header().Set("Content-Security-Policy", s.documentation.csp)
	w.Header().Set("Cache-Control", "no-cache")
	if documentationImmutable.MatchString(name) {
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	}
	w.Header().Set("ETag", `"sha256-`+file.SHA256+`"`)
	// Pagefind uses .pagefind for its WebAssembly modules.
	if strings.HasSuffix(name, ".pagefind") || strings.HasSuffix(name, ".wasm") {
		w.Header().Set("Content-Type", "application/wasm")
	}
	if strings.HasSuffix(name, ".yaml") || strings.HasSuffix(name, ".yml") {
		w.Header().Set("Content-Type", "application/yaml")
	}
	http.ServeContent(w, r, path.Base(name), time.Time{}, f)
}
