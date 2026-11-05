package demodata

import (
	"bytes"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/demoarchive"
)

const AllowlistFilename = "allowlist.json"

// Allowlist contains only exact nonsecret runtime identities, not manifest content.
func (p Plan) Allowlist() demoarchive.Allowlist {
	result := demoarchive.Allowlist{Format: 1, TenantID: TenantID, Entries: []demoarchive.Entry{}}
	for _, item := range p.Resources {
		if len(item.Archive) > 0 {
			result.Entries = append(result.Entries, demoarchive.Entry{Path: "/" + item.Resource.Key + ".zip", Digest: item.BodyDigest, Size: len(item.Archive)})
		}
	}
	slices.SortFunc(result.Entries, func(a, b demoarchive.Entry) int { return strings.Compare(a.Path, b.Path) })
	return result
}
func objectName(digest string) string {
	return "objects/" + strings.TrimPrefix(digest, "sha256:") + ".zip"
}

// Prepare installs immutable content first, then atomically publishes its complete allowlist.
// Interrupted staging cannot change the admitted set. Existing paths may only retain identical bytes.
func Prepare(directory string, plan Plan) error {
	a := plan.Allowlist()
	if a.Validate() != nil {
		return manifestError()
	}
	if err := PrepareDirectory(directory); err != nil {
		return err
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		return manifestError()
	}
	defer func() { _ = root.Close() }()
	lock, err := root.OpenFile("prepare.lock", os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return manifestError()
	}
	defer func() { _ = lock.Close() }()
	info, err := lock.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB) != nil {
		return errors.New("sample preparation is unavailable or already running")
	}
	defer func() { _ = syscall.Flock(int(lock.Fd()), syscall.LOCK_UN) }()
	oldRaw, err := readAsset(root.FS(), AllowlistFilename, demoarchive.MaxAllowlistBytes)
	if err == nil {
		old, e := demoarchive.Decode(oldRaw)
		if e != nil {
			return manifestError()
		}
		byPath := map[string]demoarchive.Entry{}
		for _, entry := range a.Entries {
			byPath[entry.Path] = entry
		}
		for _, entry := range old.Entries {
			if current, ok := byPath[entry.Path]; ok && current != entry {
				return errors.New("prepared archive path changed; preserve existing identities")
			}
			if _, ok := byPath[entry.Path]; !ok {
				a.Entries = append(a.Entries, entry)
			}
		}
		slices.SortFunc(a.Entries, func(x, y demoarchive.Entry) int { return strings.Compare(x.Path, y.Path) })
		if a.Validate() != nil {
			return manifestError()
		}
		// Validate the old publication before any staging; no implicit repair of malformed content.
		if _, e = ReadPrepared(directory); e != nil {
			return manifestError()
		}
	} else {
		if _, e := root.Lstat(AllowlistFilename); !errors.Is(e, fs.ErrNotExist) {
			return manifestError()
		}
	}
	if err = root.Mkdir("objects", 0700); err != nil && !errors.Is(err, fs.ErrExist) {
		return manifestError()
	}
	info, err = root.Lstat("objects")
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0700 {
		return manifestError()
	}
	for _, item := range plan.Resources {
		if len(item.Archive) == 0 {
			continue
		}
		if digest(item.Archive) != item.BodyDigest || len(item.Archive) > demoarchive.MaxArchiveBytes {
			return manifestError()
		}
		name := objectName(item.BodyDigest)
		existing, e := readAsset(root.FS(), name, demoarchive.MaxArchiveBytes)
		if e == nil {
			if !bytes.Equal(existing, item.Archive) {
				return manifestError()
			}
			continue
		}
		if _, e = root.Lstat(name); !errors.Is(e, fs.ErrNotExist) {
			return manifestError()
		}
		// A crash can leave an unreferenced incomplete object; publish from a temporary file with no replacement.
		if err = atomicFile(directory, name, item.Archive, false); err != nil {
			return manifestError()
		}
	}
	raw, err := json.Marshal(a)
	if err != nil || len(raw) > demoarchive.MaxAllowlistBytes {
		return manifestError()
	}
	if bytes.Equal(raw, oldRaw) {
		return nil
	}
	return atomicFile(directory, AllowlistFilename, raw, true)
}
func atomicFile(directory, name string, raw []byte, replace bool) error {
	destination := filepath.Join(directory, filepath.FromSlash(name))
	file, err := os.CreateTemp(filepath.Dir(destination), ".prepare-*")
	if err != nil {
		return manifestError()
	}
	temporary := file.Name()
	defer func() { _ = os.Remove(temporary) }()
	if replace {
		if err = file.Chmod(0644); err != nil {
			_ = file.Close()
			return manifestError()
		}
	}
	if _, err = file.Write(raw); err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err != nil || closeErr != nil {
		return manifestError()
	}
	if !replace {
		if err = os.Link(temporary, destination); err != nil {
			return manifestError()
		}
	} else {
		if err = os.Rename(temporary, destination); err != nil {
			return manifestError()
		}
	}
	directoryFile, err := os.Open(filepath.Dir(destination))
	if err != nil {
		return manifestError()
	}
	defer func() { _ = directoryFile.Close() }()
	return directoryFile.Sync()
}

// ReadPrepared snapshots and verifies every admitted archive before the listener starts.
func ReadPrepared(directory string) (map[string][]byte, error) {
	if _, err := os.Lstat(directory); err != nil {
		return nil, manifestError()
	}
	if err := PrepareDirectory(directory); err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		return nil, manifestError()
	}
	defer func() { _ = root.Close() }()
	raw, err := readAsset(root.FS(), AllowlistFilename, demoarchive.MaxAllowlistBytes)
	if err != nil {
		return nil, manifestError()
	}
	a, err := demoarchive.Decode(raw)
	if err != nil {
		return nil, manifestError()
	}
	files := map[string][]byte{}
	for _, entry := range a.Entries {
		raw, e := readAsset(root.FS(), objectName(entry.Digest), demoarchive.MaxArchiveBytes)
		if e != nil || digest(raw) != entry.Digest || len(raw) != entry.Size {
			return nil, manifestError()
		}
		files[entry.Path] = raw
	}
	return files, nil
}

// PrepareWithReceipt materializes known original archives omitted by a selected
// profile, including N-88 TLS-only directories predating file-backed preparation.
// Receipt keys are unverified local hints: only the embedded exact originals may
// be added here, and API loading independently verifies ownership/current state.
func PrepareWithReceipt(directory string, plan Plan, receipt, origin string) error {
	preview, err := PreviewReceipt(plan, receipt)
	if err != nil {
		return err
	}
	if plan.Dataset == "catalog-demo" {
		selected := map[string]bool{}
		for _, item := range plan.Resources {
			selected[item.Resource.Key] = true
		}
		outside := map[string]bool{}
		for _, item := range preview.Items {
			if !selected[item.Key] {
				outside[item.Key] = true
			}
		}
		if len(outside) > 0 {
			plan.Resources = slices.Clone(plan.Resources)
			for _, original := range DefaultPlan("expanded", origin).Resources {
				if outside[original.Resource.Key] && len(original.Archive) > 0 {
					plan.Resources = append(plan.Resources, original)
				}
			}
		}
	}
	return Prepare(directory, plan)
}
