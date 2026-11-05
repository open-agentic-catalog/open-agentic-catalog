package main

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/safeerr"
)

func openImportRoot(filename string) (*os.Root, string, error) {
	absolute, err := filepath.Abs(filename)
	if err != nil {
		return nil, "", safeerr.Wrap(err, "cannot resolve import manifest")
	}
	for name := absolute; ; name = filepath.Dir(name) {
		info, statErr := os.Lstat(name) // #nosec G703 -- explicit local CLI path; inspect ancestors before os.Root confines asset reads.
		if statErr != nil || info.Mode()&os.ModeSymlink != 0 {
			return nil, "", safeerr.New("import manifest and its ancestors must exist without symbolic links")
		}
		if filepath.Dir(name) == name {
			break
		}
	}
	root, err := os.OpenRoot(filepath.Dir(absolute))
	return root, filepath.Base(absolute), safeerr.Wrap(err, "cannot open import manifest directory")
}

func importPathInfo(root *os.Root, name string) (fs.FileInfo, error) {
	if !fs.ValidPath(name) || name == "." || len(name) > catalog.MaxSkillPackagePathBytes || strings.ContainsAny(name, "\\:\x00") {
		return nil, safeerr.New("import asset paths must be relative paths beneath the manifest directory")
	}
	parts := strings.Split(name, "/")
	var info fs.FileInfo
	for index := range parts {
		var err error
		info, err = root.Lstat(strings.Join(parts[:index+1], "/"))
		if err != nil || info.Mode()&os.ModeSymlink != 0 || index < len(parts)-1 && !info.IsDir() {
			return nil, safeerr.New("import assets must exist without symbolic links or invalid ancestors")
		}
	}
	return info, nil
}

func readImportAsset(ctx context.Context, root *os.Root, name string, limit int) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, safeerr.Wrap(err, "import preview was cancelled or exceeded its deadline")
	}
	info, err := importPathInfo(root, name)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > int64(limit) {
		return nil, safeerr.New("import assets must be regular files within their byte limit")
	}
	// Root confines resolution, but can follow links despite O_NOFOLLOW.
	// Recheck the path and opened identity before reading; NONBLOCK avoids FIFO hangs.
	file, err := root.OpenFile(name, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, safeerr.Wrap(err, "cannot open import asset")
	}
	defer func() { _ = file.Close() }()
	opened, err := file.Stat()
	actual, pathErr := importPathInfo(root, name)
	if err != nil || pathErr != nil || !os.SameFile(info, opened) || !os.SameFile(actual, opened) || !opened.Mode().IsRegular() || opened.Size() > int64(limit) {
		return nil, safeerr.New("import asset changed or exceeds its byte limit")
	}
	raw, err := io.ReadAll(io.LimitReader(file, int64(limit)+1))
	if err != nil || len(raw) > limit {
		return nil, safeerr.New("cannot read import asset within its byte limit")
	}
	if err = ctx.Err(); err != nil {
		return nil, safeerr.Wrap(err, "import preview was cancelled or exceeded its deadline")
	}
	return raw, nil
}

type importPackageMember struct {
	name string
	mode fs.FileMode
}

func importSkillDirectory(ctx context.Context, root *os.Root, directory string) ([]byte, error) {
	info, err := importPathInfo(root, directory)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() || !catalog.ValidSkillName(path.Base(directory)) {
		return nil, safeerr.New("Skill packageDirectory must be a directory with a valid Skill root name")
	}
	members := []importPackageMember{}
	files := 0
	var expanded int64
	var visit func(string) error
	visit = func(name string) error {
		if err := ctx.Err(); err != nil {
			return safeerr.Wrap(err, "import preview was cancelled or exceeded its deadline")
		}
		expected, err := importPathInfo(root, name)
		if err != nil {
			return err
		}
		file, err := root.OpenFile(name, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
		if err != nil {
			return safeerr.Wrap(err, "cannot read Skill directory")
		}
		defer func() { _ = file.Close() }()
		opened, err := file.Stat()
		actual, pathErr := importPathInfo(root, name)
		if err != nil || pathErr != nil || !opened.IsDir() || !os.SameFile(expected, opened) || !os.SameFile(actual, opened) {
			return safeerr.New("Skill directory changed while preparing the package")
		}
		for {
			entries, readErr := file.ReadDir(64)
			if readErr != nil && !errors.Is(readErr, io.EOF) {
				return safeerr.Wrap(readErr, "cannot read Skill directory")
			}
			for _, entry := range entries {
				child := name + "/" + entry.Name()
				relative := strings.TrimPrefix(child, directory+"/")
				// Archive depth includes the Skill root, not the directories
				// between the manifest and the selected Skill directory.
				if _, valid := catalog.NormalizeSkillPackagePath(path.Base(directory) + "/" + relative); !valid {
					return safeerr.New("Skill directory contains an unsafe member path")
				}
				info, err := importPathInfo(root, child)
				if err != nil {
					return err
				}
				if !info.IsDir() && !info.Mode().IsRegular() {
					return safeerr.New("Skill directories can contain only directories and regular files")
				}
				members = append(members, importPackageMember{name: child, mode: info.Mode()})
				if len(members) > catalog.MaxSkillPackageFiles*(catalog.MaxSkillPackageDepth+1) {
					return safeerr.New("Skill directory has too many members")
				}
				if info.IsDir() {
					if err := visit(child); err != nil {
						return err
					}
				} else {
					files++
					expanded += info.Size()
					if files > catalog.MaxSkillPackageFiles || info.Size() > catalog.MaxSkillPackageFileBytes || expanded > catalog.MaxSkillPackageExpandedBytes {
						return safeerr.New("Skill directory exceeds the file or expanded byte limits")
					}
				}
			}
			if errors.Is(readErr, io.EOF) {
				return nil
			}
		}
	}
	if err := visit(directory); err != nil {
		return nil, err
	}
	slices.SortFunc(members, func(a, b importPackageMember) int { return strings.Compare(a.name, b.name) })
	buffer := &importArchiveBuffer{}
	writer := zip.NewWriter(buffer)
	expanded = 0
	for _, member := range members {
		name := path.Base(directory) + "/" + strings.TrimPrefix(member.name, directory+"/")
		header := &zip.FileHeader{Name: name, Method: zip.Deflate, Modified: time.Date(1980, 1, 1, 0, 0, 0, 0, time.UTC)}
		mode := fs.FileMode(0644)
		if member.mode.IsDir() {
			header.Name += "/"
			mode = fs.ModeDir | 0755
		} else if member.mode&0111 != 0 {
			mode = 0755
		}
		header.SetMode(mode)
		entry, err := writer.CreateHeader(header)
		if err != nil {
			return nil, safeerr.Wrap(err, "cannot encode Skill archive within its byte limit")
		}
		if !member.mode.IsDir() {
			raw, err := readImportAsset(ctx, root, member.name, catalog.MaxSkillPackageFileBytes)
			if err != nil {
				return nil, err
			}
			expanded += int64(len(raw))
			if expanded > catalog.MaxSkillPackageExpandedBytes {
				return nil, safeerr.New("Skill directory changed or exceeds its expanded byte limit")
			}
			if _, err = entry.Write(raw); err != nil {
				return nil, safeerr.Wrap(err, "cannot encode Skill archive within its byte limit")
			}
		}
	}
	if err = writer.Close(); err != nil {
		return nil, safeerr.Wrap(err, "cannot encode Skill archive within its byte limit")
	}
	return buffer.Bytes(), nil
}

type importArchiveBuffer struct{ bytes.Buffer }

func (b *importArchiveBuffer) Write(raw []byte) (int, error) {
	if len(raw) > catalog.MaxSkillPackageBytes-b.Len() {
		return 0, errors.New("skill archive byte limit exceeded")
	}
	return b.Buffer.Write(raw)
}
