// Personal backups contain reusable private intent, never server ownership or credentials.
package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/jsondoc"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/personal"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/safeerr"
)

const personalFileFormat = "oac-personal-library-v1"
const personalFileLimit = 4 << 20

type personalFile struct {
	Format      string           `json:"format"`
	Origin      string           `json:"origin"`
	TenantID    string           `json:"tenantId"`
	Consistency string           `json:"consistency"`
	Items       []personal.Input `json:"items"`
}

// decodePersonalFile rejects ambiguous JSON and checks every reusable save body.
func decodePersonalFile(raw []byte) (personalFile, error) {
	var file personalFile
	fail := safeerr.New("personal: invalid library file; expected bounded oac-personal-library-v1 JSON with exact origin, tenant and supported items")
	if len(raw) > personalFileLimit || jsondoc.Decode(raw, &file) != nil || file.Format != personalFileFormat || file.Consistency != "observed-pages" || !validImportID(file.TenantID) || file.Items == nil || len(file.Items) > personal.MaxItems {
		return personalFile{}, fail
	}
	origin, err := remoteOrigin(file.Origin)
	if err != nil || origin != file.Origin {
		return personalFile{}, fail
	}
	for _, item := range file.Items {
		encoded, err := json.Marshal(item)
		if err != nil || len(encoded) > personal.MaxBodyBytes || personal.Validate(item) != nil {
			return personalFile{}, fail
		}
	}
	return file, nil
}

// readPersonalFile bounds input; file contents never supply a request destination.
func readPersonalFile(filename string) ([]byte, personalFile, error) {
	raw, err := remoteReadFile(filename, personalFileLimit)
	if err != nil {
		return nil, personalFile{}, safeerr.New("personal: cannot read a regular library file of at most 4 MiB")
	}
	file, err := decodePersonalFile(raw)
	return raw, file, err
}

// writePersonalFile publishes only a complete export into an exclusively created private directory.
func writePersonalFile(ctx context.Context, directory string, file personalFile) (err error) {
	raw, err := json.Marshal(file)
	if err != nil {
		return safeerr.New("personal: cannot encode library export")
	}
	if _, err = decodePersonalFile(raw); err != nil {
		return err
	}
	parent, name, err := openExportParent(directory)
	if err != nil {
		return err
	}
	defer func() { _ = parent.Close() }()
	root, err := createExportDirectory(parent, name)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	owned, err := root.Stat(".")
	if err != nil {
		return safeerr.New("personal: cannot inspect private export directory")
	}
	defer func() {
		if err != nil {
			_ = root.Remove("library.json")
			if current, e := parent.Lstat(name); e == nil && os.SameFile(owned, current) {
				_ = parent.Remove(name)
			}
		}
	}()
	if err = writeExportFile(ctx, root, "library.json", raw); err != nil {
		return err
	}
	for _, directory := range []*os.Root{root, parent} {
		handle, e := directory.Open(".")
		if e != nil {
			return safeerr.New("personal: cannot sync private export directory")
		}
		e = handle.Sync()
		_ = handle.Close()
		if e != nil {
			return safeerr.New("personal: cannot sync private export directory")
		}
	}
	return nil
}

// personalSeparateFiles prevents progress replacement from changing the selected backup.
func personalSeparateFiles(input, state string) bool {
	a, err := filepath.Abs(input)
	b, other := filepath.Abs(state)
	return err == nil && other == nil && a != b && a != b+".lock"
}
