package web

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDocumentationDestination(t *testing.T) {
	for _, base := range []string{"", "/browser"} {
		for _, bundle := range []bool{false, true} {
			for _, override := range []string{"", "https://docs.example.test/manual/"} {
				c := fixtureConfig(t, "http://127.0.0.1:8080")
				c.BasePath, c.DocumentationURL = base, override
				if bundle {
					writeDocumentation(t, c.Assets)
				}
				s := newFixture(t, c)
				s.forward = http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Fatal("bootstrap contacted API") })
				w := uiRequest(s, "GET", "/runtime.json", "")
				var body map[string]string
				if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
					t.Fatal(err)
				}
				want := override
				if want == "" && bundle {
					want = "/docs/"
				}
				if w.Code != 200 || body["documentationUrl"] != want || body["basePath"] != base || w.Header().Get("Set-Cookie") != "" {
					t.Fatalf("unexpected bootstrap %d %v", w.Code, body)
				}
			}
		}
	}
}

func writeDocumentation(t *testing.T, assets string) documentationManifest {
	t.Helper()
	m := documentationManifest{SchemaVersion: 1, Base: "/docs/", LockSHA256: strings.Repeat("a", 64), ScriptHashes: []string{}, StyleHashes: []string{}}
	for name, value := range map[string]string{"index.html": "<!doctype html><h1>Manual</h1>", "guide/index.html": "<h1>Guide</h1>", "_astro/app.12345678.js": "export const ok=true", "pagefind/pagefind-worker.js": "/* worker */", "pagefind/wasm.en.pagefind": "wasm", "generated/example.yaml": "setting: true\n"} {
		filename := filepath.Join(assets, "docs", name)
		if err := os.MkdirAll(filepath.Dir(filename), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filename, []byte(value), 0600); err != nil {
			t.Fatal(err)
		}
		digest := sha256.Sum256([]byte(value))
		m.Files = append(m.Files, documentationFile{Path: name, Size: int64(len(value)), SHA256: hex.EncodeToString(digest[:])})
	}
	writeDocumentationManifest(t, assets, m)
	return m
}

func writeDocumentationManifest(t *testing.T, assets string, m documentationManifest) {
	t.Helper()
	bytes, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(assets, "docs", documentationManifestName), bytes, 0600); err != nil {
		t.Fatal(err)
	}
}

func TestDocumentationStaticDispatch(t *testing.T) {
	for _, base := range []string{"", "/catalog"} {
		t.Run(base, func(t *testing.T) {
			c := fixtureConfig(t, "http://127.0.0.1:8080")
			c.BasePath = base
			writeDocumentation(t, c.Assets)
			s := newFixture(t, c)
			s.forward = http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Fatal("static manual forwarded to API") })
			for _, test := range []struct {
				method, target, content, mime, cache, location string
				status                                         int
			}{
				{"GET", "/docs/", "Manual", "text/html", "no-cache", "", 200},
				{"HEAD", "/docs/", "", "text/html", "no-cache", "", 200},
				{"GET", "/docs/guide/", "Guide", "text/html", "no-cache", "", 200},
				{"GET", "/docs/_astro/app.12345678.js", "export const", "javascript", "public, max-age=31536000, immutable", "", 200},
				{"GET", "/docs/pagefind/pagefind-worker.js", "worker", "javascript", "no-cache", "", 200},
				{"GET", "/docs/pagefind/wasm.en.pagefind", "wasm", "application/wasm", "no-cache", "", 200},
				{"GET", "/docs/generated/example.yaml", "setting:", "application/yaml", "no-cache", "", 200},
				{"GET", "/docs?private=discard", "", "", "", "/docs/", 301},
				{"GET", "/docs/guide", "", "", "", "/docs/guide/", 301},
				{"POST", "/docs/", "", "", "", "", 405},
				{"GET", "/docs/missing", "", "", "", "", 404},
				{"GET", "/docs/.hidden", "", "", "", "", 404},
				{"GET", "/docs/documentation-bundle.json", "", "", "", "", 404},
				{"GET", "/docs/../index.html", "", "", "", "", 400},
				{"GET", "/docs/%2e%2e/index.html", "", "", "", "", 400},
			} {
				r := httptest.NewRequestWithContext(t.Context(), test.method, c.Origin+test.target, nil)
				r.AddCookie(&http.Cookie{Name: c.CookieName, Value: "invalid-secret-session", Secure: true, HttpOnly: true, SameSite: http.SameSiteStrictMode})
				w := httptest.NewRecorder()
				s.ServeHTTP(w, r)
				if w.Code != test.status || !strings.Contains(w.Body.String(), test.content) || (test.mime != "" && !strings.Contains(w.Header().Get("Content-Type"), test.mime)) || (test.cache != "" && w.Header().Get("Cache-Control") != test.cache) || w.Header().Get("Location") != test.location {
					t.Fatalf("%s %s: %d %v %s", test.method, test.target, w.Code, w.Header(), w.Body.String())
				}
				if w.Header().Get("Set-Cookie") != "" {
					t.Fatal("manual resolved or changed a session")
				}
				if test.method == "HEAD" && w.Body.Len() != 0 {
					t.Fatal("HEAD returned content")
				}
				if test.status == 200 && (!strings.Contains(w.Header().Get("Content-Security-Policy"), "worker-src 'self'") || strings.Contains(w.Header().Get("Content-Security-Policy"), "script-src 'self' 'unsafe-inline'")) {
					t.Fatal("wrong manual policy")
				}
			}
			r := httptest.NewRequestWithContext(t.Context(), "GET", c.Origin+"/docs/", nil)
			w := httptest.NewRecorder()
			s.ServeHTTP(w, r)
			r.Header.Set("If-None-Match", w.Header().Get("ETag"))
			w = httptest.NewRecorder()
			s.ServeHTTP(w, r)
			if w.Code != 304 || w.Body.Len() != 0 {
				t.Fatal("ETag revalidation failed")
			}
			r.Host = "other.example"
			w = httptest.NewRecorder()
			s.ServeHTTP(w, r)
			if w.Code != 400 {
				t.Fatal("manual bypassed host check")
			}
			w = uiRequest(s, "GET", "/runtime.json", "")
			if strings.Contains(w.Header().Get("Content-Security-Policy"), "wasm-unsafe-eval") {
				t.Fatal("manual policy changed application CSP")
			}
		})
	}
}

func TestDocumentationAdmission(t *testing.T) {
	c := fixtureConfig(t, "http://127.0.0.1:8080")
	s := newFixture(t, c)
	if w := uiRequest(s, "GET", "/docs/", ""); w.Code != 404 {
		t.Fatal("missing bundle did not stay unavailable")
	}
	for _, change := range []struct {
		name   string
		mutate func(*documentationManifest)
	}{
		{"schema", func(m *documentationManifest) { m.SchemaVersion = 2 }},
		{"base", func(m *documentationManifest) { m.Base = "/other/" }},
		{"lock", func(m *documentationManifest) { m.LockSHA256 = "bad" }},
		{"empty", func(m *documentationManifest) { m.Files = nil }},
		{"duplicate", func(m *documentationManifest) { m.Files = append(m.Files, m.Files[0]) }},
		{"hidden", func(m *documentationManifest) { m.Files[0].Path = ".secret" }},
		{"traversal", func(m *documentationManifest) { m.Files[0].Path = "../secret" }},
		{"size", func(m *documentationManifest) { m.Files[0].Size = 33 << 20 }},
		{"negative", func(m *documentationManifest) { m.Files[0].Size = -1 }},
		{"digest", func(m *documentationManifest) { m.Files[0].SHA256 = "wrong" }},
		{"missing file", func(m *documentationManifest) { m.Files[0].Path = "missing" }},
		{"size mismatch", func(m *documentationManifest) { m.Files[0].Size++ }},
		{"no root index", func(m *documentationManifest) {
			for i, file := range m.Files {
				if file.Path == "index.html" {
					m.Files = append(m.Files[:i], m.Files[i+1:]...)
					break
				}
			}
		}},
		{"policy", func(m *documentationManifest) { m.ScriptHashes = []string{"sha256-'; unsafe-inline"} }},
	} {
		t.Run(change.name, func(t *testing.T) {
			c := fixtureConfig(t, "http://127.0.0.1:8080")
			m := writeDocumentation(t, c.Assets)
			newFixture(t, c)
			change.mutate(&m)
			writeDocumentationManifest(t, c.Assets, m)
			if host, err := New(c, nil); err == nil {
				host.Close()
				t.Fatal("invalid bundle accepted")
			}
		})
	}
	for _, base := range []string{"/docs", "/docs/nested"} {
		c.BasePath = base
		if err := c.validate(); err == nil {
			t.Fatal("reserved UI base accepted")
		}
	}
	c.BasePath = "/docs-ui"
	if err := c.validate(); err != nil {
		t.Fatalf("unrelated UI path rejected: %v", err)
	}
}

func TestDocumentationMountedInputBounds(t *testing.T) {
	for _, missing := range []string{"scriptHashes", "styleHashes"} {
		c := fixtureConfig(t, "http://127.0.0.1:8080")
		writeDocumentation(t, c.Assets)
		filename := filepath.Join(c.Assets, "docs", documentationManifestName)
		raw, err := os.ReadFile(filename) // #nosec G304 -- Synthetic manifest under this test's private temporary asset directory.
		if err != nil {
			t.Fatal(err)
		}
		var fields map[string]json.RawMessage
		if err = json.Unmarshal(raw, &fields); err != nil {
			t.Fatal(err)
		}
		delete(fields, missing)
		raw, err = json.Marshal(fields)
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(filename, raw, 0600); err != nil {
			t.Fatal(err)
		}
		if host, err := New(c, nil); err == nil {
			host.Close()
			t.Fatalf("missing %s accepted", missing)
		}
	}
	for _, contents := range []string{`{}`, `{"schemaVersion":1,"schemaVersion":1}`, `{"SchemaVersion":1}`, `{"extra":true}`, `null`, strings.Repeat(" ", 2<<20+1)} {
		c := fixtureConfig(t, "http://127.0.0.1:8080")
		writeDocumentation(t, c.Assets)
		if err := os.WriteFile(filepath.Join(c.Assets, "docs", documentationManifestName), []byte(contents), 0600); err != nil {
			t.Fatal(err)
		}
		if host, err := New(c, nil); err == nil {
			host.Close()
			t.Fatal("invalid mounted manifest accepted")
		}
	}
	for _, name := range []string{".", "../secret", "foo/.secret", "/absolute", "file\\path", strings.Repeat("a", 513), "documentation-bundle.json"} {
		if validDocumentationPath(name) {
			t.Fatalf("unsafe path accepted: %q", name)
		}
	}
	c := fixtureConfig(t, "http://127.0.0.1:8080")
	if err := os.WriteFile(filepath.Join(c.Assets, "docs"), []byte("not a directory"), 0600); err != nil {
		t.Fatal(err)
	}
	if host, err := New(c, nil); err == nil {
		host.Close()
		t.Fatal("file accepted as manual root")
	}
}

func TestDocumentationSymlinksAndChangedFiles(t *testing.T) {
	for _, change := range []string{"file-link", "directory-link", "removed", "resized"} {
		t.Run(change, func(t *testing.T) {
			c := fixtureConfig(t, "http://127.0.0.1:8080")
			writeDocumentation(t, c.Assets)
			s := newFixture(t, c)
			if w := uiRequest(s, "GET", "/docs/guide/", ""); w.Code != 200 || !strings.Contains(w.Body.String(), "Guide") {
				t.Fatal("unchanged manual was unavailable")
			}
			target := filepath.Join(c.Assets, "docs", "guide", "index.html")
			if change == "directory-link" {
				target = filepath.Dir(target)
			}
			if err := os.RemoveAll(target); err != nil {
				t.Fatal(err)
			}
			switch change {
			case "file-link", "directory-link":
				if err := os.Symlink(t.TempDir(), target); err != nil {
					t.Fatal(err)
				}
			case "resized":
				if err := os.WriteFile(target, []byte("<h1>Guide</h1> changed"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if w := uiRequest(s, "GET", "/docs/guide/", ""); w.Code != 404 || strings.Contains(w.Body.String(), "Guide") || w.Header().Get("ETag") != "" {
				t.Fatal("changed manual file served or advertised a valid ETag")
			}
			if host, err := New(c, nil); err == nil {
				host.Close()
				t.Fatal("changed bundle accepted at startup")
			}
		})
	}
}

func TestDocumentationPolicyHashes(t *testing.T) {
	digest := sha256.Sum256([]byte("theme()"))
	hash := "sha256-" + base64.StdEncoding.EncodeToString(digest[:])
	policy, err := documentationPolicy(documentationManifest{ScriptHashes: []string{hash}, StyleHashes: []string{hash}})
	if err != nil || strings.Count(policy, "'"+hash+"'") != 2 || strings.Contains(policy, "'unsafe-eval'") {
		t.Fatalf("policy %s %v", policy, err)
	}
	hashes := make([]string, 65)
	for i := range hashes {
		digest := sha256.Sum256([]byte{byte(i)})
		hashes[i] = "sha256-" + base64.StdEncoding.EncodeToString(digest[:])
	}
	if _, err := documentationPolicy(documentationManifest{ScriptHashes: hashes[:64], StyleHashes: hashes[:64]}); err != nil {
		t.Fatalf("valid maximum-sized policy rejected: %v", err)
	}
	noncanonical := hash[:len(hash)-2] + string(hash[len(hash)-2]+1) + "="
	for _, hashes := range [][]string{{"bad"}, {"sha256-!"}, {"sha256-YQ=="}, {noncanonical}, {hash + "\n"}, {hash, hash}, hashes} {
		for _, m := range []documentationManifest{{ScriptHashes: hashes}, {StyleHashes: hashes}} {
			if _, err := documentationPolicy(m); err == nil {
				t.Fatal("invalid script or style hashes accepted")
			}
		}
	}
}
