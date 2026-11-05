package web

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPublicHelpAndLegalAssets(t *testing.T) {
	for _, base := range []string{"", "/nested/ui"} {
		t.Run(base, func(t *testing.T) {
			c := fixtureConfig(t, "http://127.0.0.1:8080")
			c.BasePath = base
			if err := os.Mkdir(filepath.Join(c.Assets, "legal"), 0700); err != nil {
				t.Fatal(err)
			}
			texts := map[string]string{"LICENSE.txt": "Exact project license\n", "third-party-notices.txt": "Original third-party copyright and license\n"}
			for name, text := range texts {
				if err := os.WriteFile(filepath.Join(c.Assets, "legal", name), []byte(text), 0600); err != nil {
					t.Fatal(err)
				}
			}
			s := newFixture(t, c)
			for _, page := range []string{"/about", "/getting-started", "/troubleshooting"} {
				for _, method := range []string{http.MethodGet, http.MethodHead} {
					w := uiRequest(s, method, page, "")
					if w.Code != 200 || !strings.HasPrefix(w.Header().Get("Content-Type"), "text/html") || len(w.Result().Cookies()) != 0 {
						t.Fatalf("public page: %d %v", w.Code, w.Header())
					}
				}
			}
			for name, text := range texts {
				w := uiRequest(s, http.MethodGet, "/legal/"+name, "")
				if w.Code != 200 || w.Body.String() != text || w.Header().Get("Content-Type") != "text/plain; charset=utf-8" || w.Header().Get("Cache-Control") != "no-cache" {
					t.Fatalf("legal text mismatch: %d %s", w.Code, w.Body.String())
				}
				if w := uiRequest(s, http.MethodHead, "/legal/"+name, ""); w.Code != 200 || w.Body.Len() != 0 {
					t.Fatal("HEAD legal file")
				}
			}
			for _, request := range []struct {
				method, path string
				status       int
			}{
				{"GET", "/about/extra", 404}, {"GET", "/getting-started/extra", 404},
				{"GET", "/troubleshooting/extra", 404},
				{"GET", "/legal/", 404}, {"GET", "/legal/private.txt", 404},
				{"GET", "/legal/LICENSE.txt?download=1", 400}, {"GET", "/legal/LICENSE.txt?", 400},
				{"POST", "/legal/LICENSE.txt", 405},
			} {
				if w := uiRequest(s, request.method, request.path, ""); w.Code != request.status {
					t.Fatalf("%s %s: %d", request.method, request.path, w.Code)
				}
			}
			file := filepath.Join(c.Assets, "legal/LICENSE.txt")
			if err := os.Remove(file); err != nil {
				t.Fatal(err)
			}
			if w := uiRequest(s, "GET", "/legal/LICENSE.txt", ""); w.Code != 404 {
				t.Fatal("missing legal file accepted")
			}
			if err := os.Symlink(filepath.Join(c.Assets, "index.html"), file); err != nil {
				t.Fatal(err)
			}
			if w := uiRequest(s, "GET", "/legal/LICENSE.txt", ""); w.Code != 404 {
				t.Fatal("linked legal file accepted")
			}
		})
	}
}

func TestLegalFileBoundsAndDirectoryRefusal(t *testing.T) {
	c := fixtureConfig(t, "http://127.0.0.1:8080")
	directory := filepath.Join(c.Assets, "legal")
	if err := os.Mkdir(directory, 0700); err != nil {
		t.Fatal(err)
	}
	s := newFixture(t, c)
	name := filepath.Join(directory, "LICENSE.txt")
	root, err := os.OpenRoot(directory)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })
	file, err := root.Create("LICENSE.txt")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = file.Close() })
	for _, tc := range []struct {
		size   int64
		status int
	}{{0, 404}, {8 << 20, 200}, {8<<20 + 1, 404}} {
		if err := file.Truncate(tc.size); err != nil {
			t.Fatal(err)
		}
		if w := uiRequest(s, "HEAD", "/legal/LICENSE.txt", ""); w.Code != tc.status || tc.status == 200 && w.Body.Len() != 0 {
			t.Fatalf("legal size %d: %d", tc.size, w.Code)
		}
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(name); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(name, 0700); err != nil {
		t.Fatal(err)
	}
	if w := uiRequest(s, "GET", "/legal/LICENSE.txt", ""); w.Code != 404 {
		t.Fatal("legal directory accepted")
	}
	if err := os.Rename(directory, directory+"-saved"); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(directory+"-saved", directory); err != nil {
		t.Fatal(err)
	}
	if w := uiRequest(s, "GET", "/legal/LICENSE.txt", ""); w.Code != 404 {
		t.Fatal("linked legal directory accepted")
	}
}
