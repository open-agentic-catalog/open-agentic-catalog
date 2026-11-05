package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"syscall"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/safeerr"
)

func openExportParent(directory string) (*os.Root, string, error) {
	fail := safeerr.New("export: output must be a new directory with an existing parent and no symlink ancestors")
	absolute, err := filepath.Abs(directory)
	if err != nil {
		return nil, "", fail
	}
	for name := filepath.Dir(absolute); ; name = filepath.Dir(name) {
		info, err := os.Lstat(name)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return nil, "", fail
		}
		if filepath.Dir(name) == name {
			break
		}
	}
	root, err := os.OpenRoot(filepath.Dir(absolute))
	if err != nil {
		return nil, "", fail
	}
	name := filepath.Base(absolute)
	if _, err := root.Lstat(name); !os.IsNotExist(err) {
		_ = root.Close()
		return nil, "", fail
	}
	return root, name, nil
}

func writeExport(ctx context.Context, parent *os.Root, name string, manifest exportManifest, content []byte) (err error) {
	fail := safeerr.New("export: cannot save private files; a directory without export.json is incomplete")
	if ctx.Err() != nil {
		return safeerr.New("export: cancelled or deadline exceeded before file publication")
	}
	encoded, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil || len(encoded) > 64<<20 {
		return fail
	}
	root, err := createExportDirectory(parent, name)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	actual, actualErr := root.Stat(".")
	if actualErr != nil {
		return fail
	}
	defer func() {
		if err != nil {
			// Delete only our fixed files through the pinned directory handle.
			for _, file := range []string{"export.json", "native.json", "native.bin", "draft.json", "mcp.json", ".mcp.json"} {
				_ = root.Remove(file)
			}
			if current, e := parent.Lstat(name); e == nil && os.SameFile(actual, current) {
				_ = parent.Remove(name)
			}
		}
	}()
	if manifest.File != nil {
		allowed := manifest.File.Name == "native.json" || manifest.File.Name == "native.bin" || manifest.File.Name == "draft.json" && manifest.Disposition == "reusable_draft"
		allowed = allowed || manifest.Disposition == "mcp_client_configuration" && manifest.MCP != nil && isMCPExportFormat(manifest.MCP.ClientFormat) && manifest.File.Name == mcpExportFilename(manifest.MCP.ClientFormat)
		if !allowed || exportChecksum(content) != manifest.File.Digest || int64(len(content)) != manifest.File.Size {
			return fail
		}
		if err := writeExportFile(ctx, root, manifest.File.Name, content); err != nil {
			return err
		}
	}
	// The sidecar is published last; its presence marks a completed export.
	if err := writeExportFile(ctx, root, "export.json", append(encoded, '\n')); err != nil {
		return err
	}
	for _, directory := range []*os.Root{root, parent} {
		file, err := directory.Open(".")
		if err != nil {
			return fail
		}
		syncErr := file.Sync()
		closeErr := file.Close()
		if syncErr != nil || closeErr != nil {
			return fail
		}
	}
	if ctx.Err() != nil {
		return safeerr.New("export: cancelled or deadline exceeded during file publication")
	}
	return nil
}

func createExportDirectory(parent *os.Root, name string) (*os.Root, error) {
	fail := safeerr.New("export: cannot save private files; a directory without export.json is incomplete")
	// Exclusive creation is the overwrite policy. Never rename a staging
	// directory over a possibly existing directory after a racy precheck.
	if parent.Mkdir(name, 0700) != nil {
		return nil, fail
	}
	expected, statErr := parent.Lstat(name)
	root, openErr := parent.OpenRoot(name)
	if openErr != nil {
		return nil, fail
	}
	actual, actualErr := root.Stat(".")
	if statErr != nil || actualErr != nil || expected.Mode()&os.ModeSymlink != 0 || !os.SameFile(expected, actual) || !actual.IsDir() || actual.Mode().Perm()&0077 != 0 {
		_ = root.Close()
		return nil, fail
	}
	return root, nil
}

func writeExportFile(ctx context.Context, root *os.Root, name string, raw []byte) error {
	fail := safeerr.New("export: cannot save private files; a directory without export.json is incomplete")
	var suffix [12]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		return fail
	}
	temporary := ".export-" + hex.EncodeToString(suffix[:])
	file, err := root.OpenFile(temporary, os.O_CREATE|os.O_EXCL|os.O_WRONLY|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return fail
	}
	defer func() { _ = file.Close(); _ = root.Remove(temporary) }()
	if ctx.Err() != nil {
		return safeerr.New("export: cancelled or deadline exceeded before file publication")
	}
	if n, err := file.Write(raw); err != nil || n != len(raw) {
		return fail
	}
	if file.Sync() != nil || file.Close() != nil {
		return fail
	}
	if ctx.Err() != nil {
		return safeerr.New("export: cancelled or deadline exceeded before file publication")
	}
	// The directory is exclusively created and private, with code-owned names.
	if _, err := root.Lstat(name); !os.IsNotExist(err) || root.Rename(temporary, name) != nil {
		return fail
	}
	return nil
}
