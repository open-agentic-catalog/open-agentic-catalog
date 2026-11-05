package demodata

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
)

// PrepareDirectory rejects links in existing ancestors before creating any output.
// TLS and archive preparation share this same private-directory boundary.
func PrepareDirectory(directory string) error {
	if directory == "" {
		return manifestError()
	}
	absolute, err := filepath.Abs(directory)
	if err != nil {
		return manifestError()
	}
	for p := absolute; ; p = filepath.Dir(p) {
		info, e := os.Lstat(p)
		if e != nil && !errors.Is(e, fs.ErrNotExist) {
			return manifestError()
		}
		if e == nil && (!info.IsDir() || info.Mode()&os.ModeSymlink != 0 || p == absolute && info.Mode().Perm() != 0700) {
			return manifestError()
		}
		if filepath.Dir(p) == p {
			break
		}
	}
	if err = os.MkdirAll(absolute, 0700); err != nil {
		return manifestError()
	}
	info, err := os.Lstat(absolute)
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0700 {
		return manifestError()
	}
	return nil
}
