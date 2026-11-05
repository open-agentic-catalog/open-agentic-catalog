package demodata

import (
	"archive/zip"
	"bytes"
	"errors"
	"io"
	"io/fs"
	"reflect"
	"testing"
	"testing/fstest"
)

// MapFS supplies real Lstat/ReadLink admission; only the subsequent file I/O
// uses a controlled failure, without changing paths or filesystem metadata.
type admittedAssetFS struct {
	fstest.MapFS
	failure                     string
	problem                     error
	admitted                    []string
	opens, stats, reads, closes int
	bytesRead                   int
}

var _ fs.ReadLinkFS = (*admittedAssetFS)(nil)

func (f *admittedAssetFS) Lstat(name string) (fs.FileInfo, error) {
	f.admitted = append(f.admitted, name)
	return f.MapFS.Lstat(name)
}

func (f *admittedAssetFS) Open(name string) (fs.File, error) {
	f.opens++
	if f.failure == "open" {
		return nil, f.problem
	}
	file, err := f.MapFS.Open(name)
	if err != nil {
		return nil, err
	}
	return &admittedAssetFile{File: file, owner: f}, nil
}

type admittedAssetFile struct {
	fs.File
	owner *admittedAssetFS
}

func (f *admittedAssetFile) Stat() (fs.FileInfo, error) {
	f.owner.stats++
	if f.owner.failure == "stat" {
		return nil, f.owner.problem
	}
	return f.File.Stat()
}

func (f *admittedAssetFile) Read(p []byte) (int, error) {
	f.owner.reads++
	if f.owner.failure == "read" && len(p) > 4 {
		p = p[:4]
	}
	n, err := f.File.Read(p)
	f.owner.bytesRead += n
	if f.owner.failure == "read" {
		return n, f.owner.problem
	}
	return n, err
}

func (f *admittedAssetFile) Close() error {
	f.owner.closes++
	return f.File.Close()
}

func TestAdmittedAssetReadFailureDiscardsBytesAndClosesFile(t *testing.T) {
	t.Parallel()
	const name = "assets/entry.txt"
	body := []byte("exact asset bytes")
	problem := errors.New("private asset I/O cause")
	for _, failure := range []string{"", "open", "stat", "read"} {
		t.Run("stage="+failure, func(t *testing.T) {
			assets := &admittedAssetFS{MapFS: fstest.MapFS{name: {Data: bytes.Clone(body)}}, failure: failure, problem: problem}
			raw, err := readAsset(assets, name, len(body))
			if !reflect.DeepEqual(assets.admitted, []string{"assets", name}) || assets.opens != 1 {
				t.Fatalf("asset did not pass path admission before file I/O: %+v", assets)
			}
			if failure == "" {
				if err != nil || !bytes.Equal(raw, body) || assets.stats != 1 || assets.reads == 0 || assets.bytesRead != len(body) || assets.closes != 1 {
					t.Fatalf("valid asset=%q error=%v I/O=%+v", raw, err, assets)
				}
				return
			}
			if err == nil || err.Error() != "invalid sample manifest, asset or selected profile" || errors.Is(err, problem) || raw != nil {
				t.Fatalf("failed asset returned partial bytes or dependency cause: %q, %v", raw, err)
			}
			switch failure {
			case "open":
				if assets.stats != 0 || assets.reads != 0 || assets.closes != 0 {
					t.Fatal("open failure performed later file work")
				}
			case "stat":
				if assets.stats != 1 || assets.reads != 0 || assets.closes != 1 {
					t.Fatal("stat failure read bytes or leaked the opened file")
				}
			case "read":
				if assets.stats != 1 || assets.reads != 1 || assets.bytesRead != 4 || assets.closes != 1 {
					t.Fatal("read failure did not discard delivered bytes or close the opened file")
				}
			}
		})
	}
}

func TestPackageFileReferencePropagatesAdmittedAssetReadFailure(t *testing.T) {
	t.Parallel()
	const body = "exact package reference bytes"
	source := RepresentationSource{Files: map[string]Body{"README.txt": {File: "assets/readme.txt"}}}
	for _, failure := range []string{"", "read"} {
		t.Run("stage="+failure, func(t *testing.T) {
			assets := &admittedAssetFS{MapFS: fstest.MapFS{"assets/readme.txt": {Data: []byte(body)}}, failure: failure, problem: errors.New("private package asset cause")}
			raw, err := packageBytes(assets, source)
			if assets.opens != 1 || assets.stats != 1 || assets.closes != 1 {
				t.Fatalf("package asset I/O=%+v", assets)
			}
			if failure != "" {
				if err == nil || err.Error() != "invalid sample manifest, asset or selected profile" || raw != nil || assets.bytesRead != 4 {
					t.Fatalf("failed asset produced partial ZIP bytes: %d, %v", len(raw), err)
				}
				return
			}
			if err != nil {
				t.Fatalf("valid package reference: %v", err)
			}
			archive, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
			if err != nil || len(archive.File) != 1 || archive.File[0].Name != "README.txt" {
				t.Fatalf("reference package lost exact inventory: %v", err)
			}
			member, err := archive.File[0].Open()
			if err != nil {
				t.Fatal(err)
			}
			content, err := io.ReadAll(member)
			_ = member.Close()
			if err != nil || string(content) != body {
				t.Fatalf("reference package content=%q, %v", content, err)
			}
		})
	}
}
