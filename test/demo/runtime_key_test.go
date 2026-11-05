package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRuntimeKeyStableAndPrivate(t *testing.T) {
	dir := t.TempDir()
	if err := prepareRuntimeKey(dir); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "mcp-key")
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()
	initial, err := root.ReadFile("mcp-key")
	if err != nil || len(initial) != 64 {
		t.Fatal("missing generated client key")
	}
	if err := prepareRuntimeKey(dir); err != nil {
		t.Fatal(err)
	}
	actual, _ := root.ReadFile("mcp-key")
	if string(actual) != string(initial) {
		t.Fatal("rotated client key")
	}
	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0600 {
		t.Fatal("public client key")
	}
	for _, tc := range []string{"permissions", "malformed", "short", "symlink", "directory"} {
		t.Run(tc, func(t *testing.T) {
			root := t.TempDir()
			key := filepath.Join(root, "mcp-key")
			switch tc {
			case "permissions":
				testRoot, e := os.OpenRoot(root)
				if e != nil {
					t.Fatal(e)
				}
				defer func() { _ = testRoot.Close() }()
				if err := testRoot.WriteFile("mcp-key", initial, 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.Chmod(key, 0644); err != nil { // #nosec G302 -- negative permission-refusal fixture only.
					t.Fatal(err)
				}
			case "malformed":
				if err := os.WriteFile(key, make([]byte, 64), 0600); err != nil {
					t.Fatal(err)
				}
			case "short":
				if err := os.WriteFile(key, []byte("short"), 0600); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				if err := os.Symlink(path, key); err != nil {
					t.Fatal(err)
				}
			case "directory":
				if err := os.Mkdir(key, 0700); err != nil {
					t.Fatal(err)
				}
			}
			if prepareRuntimeKey(root) == nil {
				t.Fatal("unsafe existing client key accepted")
			}
		})
	}
}
