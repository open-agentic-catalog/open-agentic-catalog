package web

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// A named pipe must be refused before an open could wait for a writer.
func TestDocumentationRefusesSpecialFiles(t *testing.T) {
	c := fixtureConfig(t, "http://127.0.0.1:8080")
	writeDocumentation(t, c.Assets)
	s := newFixture(t, c)
	filename := filepath.Join(c.Assets, "docs", "guide", "index.html")
	if err := os.Remove(filename); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(filename, 0600); err != nil {
		t.Fatal(err)
	}
	if w := uiRequest(s, "GET", "/docs/guide/", ""); w.Code != 404 {
		t.Fatal("special file was served")
	}
	if host, err := New(c, nil); err == nil {
		host.Close()
		t.Fatal("special file passed startup")
	}
}
