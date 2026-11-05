// Bundle tests check pinned inventory, retained licenses and checksum integrity
// without fetching upstream sources or exercising protocol conformance.
package schemas

import (
	"bufio"
	"encoding/hex"
	"errors"
	"io/fs"
	"path"
	"strings"
	"testing"
	"testing/fstest"
)

// TestProfileInventoryCoversLicensedBundle prevents orphaned inputs and profiles
// without immutable source identities or retained license declarations.
func TestProfileInventoryCoversLicensedBundle(t *testing.T) {
	ids := make(map[string]bool)
	paths := make(map[string]bool)
	for _, profile := range Profiles {
		if profile.ID == "" || ids[profile.ID] {
			t.Fatalf("empty or duplicate profile ID: %q", profile.ID)
		}
		ids[profile.ID] = true
		if !fs.ValidPath(profile.Path) || !strings.HasPrefix(profile.Path, "upstream/") || path.Base(profile.Path) != profile.ID || paths[profile.Path] {
			t.Fatalf("invalid or duplicate profile path: %q", profile.Path)
		}
		paths[profile.Path] = true
		commit, err := hex.DecodeString(profile.Commit)
		if err != nil || len(commit) != 20 || profile.Commit != strings.ToLower(profile.Commit) {
			t.Fatalf("profile %s has no full immutable commit: %q", profile.ID, profile.Commit)
		}
		if profile.Project == "" || !fs.ValidPath(profile.Repository) || len(strings.Split(profile.Repository, "/")) != 2 {
			t.Fatalf("profile %s has invalid upstream identity", profile.ID)
		}
		licensed := false
		for _, name := range []string{"LICENSE", "LICENSE.md"} {
			content, err := Files.ReadFile(profile.Path + "/" + name)
			if err == nil && len(strings.TrimSpace(string(content))) > 0 {
				licensed = true
			}
		}
		if !licensed {
			t.Fatalf("profile %s has no retained license", profile.ID)
		}
	}
	if err := fs.WalkDir(Files, "upstream", func(name string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		owners := 0
		for directory := range paths {
			if strings.HasPrefix(name, directory+"/") {
				owners++
			}
		}
		if owners != 1 {
			t.Errorf("bundled file %s belongs to %d declared profiles, want 1", name, owners)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// TestEmbeddedProfileBundleVerifies checks the complete checked-in bundle.
func TestEmbeddedProfileBundleVerifies(t *testing.T) {
	report, err := VerifyBundle()
	if err != nil {
		t.Fatal(err)
	}
	if report.Status != "verified" || report.ArtifactCount != 36 || len(report.Profiles) != 7 || report.BundleDigest == "" {
		t.Fatalf("unexpected verification report: %#v", report)
	}
}

// TestProfileBundleRejectsTamperingMissingAndUnlistedFiles rejects content and
// inventory changes, including a replacement that preserves the file count.
func TestProfileBundleRejectsTamperingMissingAndUnlistedFiles(t *testing.T) {
	const manifest = "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad  upstream/profile/spec.txt\n"
	tests := []struct {
		name  string
		files fstest.MapFS
	}{
		{name: "tampered", files: fstest.MapFS{"upstream/profile/spec.txt": {Data: []byte("abd")}}},
		{name: "missing", files: fstest.MapFS{"upstream/profile": {Mode: 0o755 | 1<<31}}},
		{name: "unlisted", files: fstest.MapFS{"upstream/profile/spec.txt": {Data: []byte("abc")}, "upstream/profile/extra.txt": {Data: []byte("extra")}}},
		{name: "renamed", files: fstest.MapFS{"upstream/profile/other.txt": {Data: []byte("abc")}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := Verify(test.files, []byte(manifest)); err == nil {
				t.Fatal("expected verification failure")
			}
		})
	}
}

// TestProfileBundleRejectsMalformedManifest rejects ambiguous entries and paths
// outside the immutable upstream tree.
func TestProfileBundleRejectsMalformedManifest(t *testing.T) {
	const digest = "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"
	const entry = digest + "  upstream/profile/spec.txt\n"
	files := fstest.MapFS{"upstream/profile/spec.txt": {Data: []byte("abc")}}
	for name, manifest := range map[string]string{
		"empty":            "",
		"missing checksum": "invalid\n",
		"invalid hex":      "zz" + entry[2:],
		"duplicate path":   entry + entry,
		"wrong separator":  digest + " upstream/profile/spec.txt\n",
		"outside upstream": digest + "  profile/spec.txt\n",
		"parent traversal": digest + "  upstream/../spec.txt\n",
		"overlong line":    entry + strings.Repeat("x", bufio.MaxScanTokenSize+1),
	} {
		t.Run(name, func(t *testing.T) {
			// A missing file must not mask acceptance of an invalid manifest path.
			if _, err := parseChecksums([]byte(manifest)); err == nil {
				t.Fatal("expected malformed manifest to fail during parsing")
			}
			if _, err := Verify(files, []byte(manifest)); err == nil {
				t.Fatal("expected malformed manifest to fail")
			}
		})
	}
}

// TestProfileBundleReportsFilesystemFailures preserves read failure causes and
// never returns a successful verification report for an incomplete scan.
func TestProfileBundleReportsFilesystemFailures(t *testing.T) {
	const manifest = "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad  upstream/profile/spec.txt\n"
	for name, test := range map[string]struct {
		files fs.FS
		cause error
	}{
		"missing tree": {files: fstest.MapFS{}, cause: fs.ErrNotExist},
		"unreadable artifact": {
			files: unreadableArtifactFS{fstest.MapFS{"upstream/profile/spec.txt": {Data: []byte("abc")}}},
			cause: fs.ErrPermission,
		},
	} {
		t.Run(name, func(t *testing.T) {
			report, err := Verify(test.files, []byte(manifest))
			if !errors.Is(err, test.cause) || !strings.Contains(err.Error(), "walk embedded standards") {
				t.Fatalf("verification error = %v, want wrapped %v", err, test.cause)
			}
			if report.Status != "" || report.BundleDigest != "" || report.ArtifactCount != 0 || len(report.Profiles) != 0 {
				t.Fatalf("failed scan returned verification evidence: %#v", report)
			}
		})
	}
}

// unreadableArtifactFS permits directory traversal but denies artifact reads.
type unreadableArtifactFS struct{ fs.FS }

// ReadFile models an input that can be enumerated but cannot be read.
func (unreadableArtifactFS) ReadFile(name string) ([]byte, error) {
	return nil, &fs.PathError{Op: "read", Path: name, Err: fs.ErrPermission}
}

// TestProfileBundleDigestBindsPathsNotManifestOrder keeps verification stable
// across manifest reordering but distinguishes identical bytes at new paths.
func TestProfileBundleDigestBindsPathsNotManifestOrder(t *testing.T) {
	const digest = "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"
	const first = digest + "  upstream/profile/first.txt\n"
	const second = digest + "  upstream/profile/second.txt\n"
	files := fstest.MapFS{
		"upstream/profile/first.txt":  {Data: []byte("abc")},
		"upstream/profile/second.txt": {Data: []byte("abc")},
	}
	forward, err := Verify(files, []byte(first+second))
	if err != nil {
		t.Fatal(err)
	}
	reverse, err := Verify(files, []byte(second+first))
	if err != nil {
		t.Fatal(err)
	}
	if forward.ArtifactCount != 2 || forward.BundleDigest != reverse.BundleDigest {
		t.Fatalf("manifest order changed verification: %#v, %#v", forward, reverse)
	}
	delete(files, "upstream/profile/second.txt")
	files["upstream/profile/renamed.txt"] = &fstest.MapFile{Data: []byte("abc")}
	renamed, err := Verify(files, []byte(first+digest+"  upstream/profile/renamed.txt\n"))
	if err != nil {
		t.Fatal(err)
	}
	if renamed.BundleDigest == forward.BundleDigest {
		t.Fatal("renaming an artifact did not change the bundle digest")
	}
}
