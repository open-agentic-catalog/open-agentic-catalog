package demodata

import (
	"archive/zip"
	"bytes"
	"errors"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"io"
	"io/fs"
	"path"
	"slices"
	"time"
)

func packageBytes(assets fs.FS, source RepresentationSource) ([]byte, error) {
	files := map[string][]byte{}
	total := 0
	entries := 0
	if source.PackageDirectory != "" {
		if !safePath(source.PackageDirectory) {
			return nil, manifestError()
		}
		// Checking a marker through the normal asset reader rejects links in every ancestor.
		marker := "plugin.json"
		if source.MediaType != catalog.PluginMediaZIP {
			marker = "SKILL.md"
		}
		if _, err := readAsset(assets, source.PackageDirectory+"/"+marker, MaxAssetBytes); err != nil {
			return nil, manifestError()
		}
		err := walkPackage(assets, source.PackageDirectory, func(name string, entry fs.DirEntry) error {
			entries++
			if entries > MaxFiles*2 || entry.Type()&fs.ModeSymlink != 0 {
				return manifestError()
			}
			if entry.IsDir() {
				return nil
			}
			if !entry.Type().IsRegular() {
				return manifestError()
			}
			relative := name[len(source.PackageDirectory)+1:]
			if source.MediaType == catalog.SkillPackageMediaZIP {
				root := path.Base(source.PackageDirectory)
				if !validKey(root) {
					return manifestError()
				}
				relative = root + "/" + relative
			}
			raw, e := readAsset(assets, name, MaxAssetBytes)
			if e != nil {
				return e
			}
			files[relative] = raw
			total += len(raw)
			if len(files) > MaxFiles || total > MaxEncodedBodyBytes {
				return manifestError()
			}
			return nil
		})
		if err != nil {
			return nil, manifestError()
		}
	} else {
		if len(source.Files) == 0 || len(source.Files) > MaxFiles {
			return nil, manifestError()
		}
		for name, body := range source.Files {
			if !safePath(name) {
				return nil, manifestError()
			}
			raw, err := resolveBody(assets, body)
			if err != nil {
				return nil, err
			}
			files[name] = raw
			total += len(raw)
			if total > MaxEncodedBodyBytes {
				return nil, manifestError()
			}
		}
	}
	var body bytes.Buffer
	writer := zip.NewWriter(&body)
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		if !safePath(name) {
			return nil, manifestError()
		}
		header := &zip.FileHeader{Name: name, Method: zip.Store}
		header.SetMode(0444)
		header.Modified = time.Date(1980, 1, 1, 0, 0, 0, 0, time.UTC)
		member, err := writer.CreateHeader(header)
		if err != nil {
			return nil, err
		}
		if _, err = member.Write(files[name]); err != nil {
			return nil, err
		}
	}
	if err := writer.Close(); err != nil {
		return nil, err
	}
	return body.Bytes(), nil
}

// Read directories in bounded chunks rather than hydrating an unlimited directory inventory.
func walkPackage(assets fs.FS, name string, visit func(string, fs.DirEntry) error) error {
	if !safePath(name) {
		return manifestError()
	}
	file, err := assets.Open(name)
	if err != nil {
		return manifestError()
	}
	defer func() { _ = file.Close() }()
	directory, ok := file.(fs.ReadDirFile)
	if !ok {
		return manifestError()
	}
	entries, err := directory.ReadDir(MaxFiles*2 + 1)
	if err != nil && !errors.Is(err, io.EOF) || len(entries) > MaxFiles*2 {
		return manifestError()
	}
	for _, entry := range entries {
		child := name + "/" + entry.Name()
		if err = visit(child, entry); err != nil {
			return err
		}
		if entry.IsDir() {
			if err = walkPackage(assets, child, visit); err != nil {
				return err
			}
		}
	}
	return nil
}
