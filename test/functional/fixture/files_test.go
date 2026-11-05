//go:build unix

package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFixtureFilesKeepExactBytesReadOnlyWithinPrivateParent(t *testing.T) {
	parent := filepath.Join(t.TempDir(), "private")
	if err := os.Mkdir(parent, 0o700); err != nil {
		t.Fatal(err)
	}
	directory := filepath.Join(parent, "mount")
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := makeFixtureDirectory(directory); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "input")
	if err := os.WriteFile(path, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	want := []byte("exact fixture bytes\n")
	if err := writeFixtureFile(path, want); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path) // #nosec G304 -- exact leaf within this test's private temporary directory.
	if err != nil || string(raw) != string(want) {
		t.Fatal("fixture write changed the requested bytes")
	}
	for path, mode := range map[string]os.FileMode{parent: 0o700, directory: 0o755, path: 0o444} {
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != mode {
			t.Fatalf("fixture permission differs from %o", mode)
		}
	}
	if err := makeFixtureDirectory(filepath.Join(path, "child")); err == nil {
		t.Fatal("accepted a file as the mount's parent")
	}
	if err := writeFixtureFile(filepath.Join(directory, "absent", "input"), want); err == nil {
		t.Fatal("accepted a missing output directory")
	}
}
