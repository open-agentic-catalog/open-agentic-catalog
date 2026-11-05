package demodata

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestPreparedPublicationRefusesInvalidRetainedState(t *testing.T) {
	plan := DefaultPlan("expanded", "")
	for _, fault := range []string{"malformed allowlist", "declared size mismatch", "missing object"} {
		t.Run(fault, func(t *testing.T) {
			directory := filepath.Join(t.TempDir(), "prepared")
			if err := Prepare(directory, plan); err != nil {
				t.Fatal(err)
			}
			if files, err := ReadPrepared(directory); err != nil || len(files) != len(plan.Allowlist().Entries) {
				t.Fatalf("valid publication control: %v", err)
			}
			allowlist := plan.Allowlist()
			raw := manifestJSON(t, allowlist)
			switch fault {
			case "malformed allowlist":
				raw = []byte(`{"format":1}`)
			case "declared size mismatch":
				allowlist.Entries[0].Size++
				raw = manifestJSON(t, allowlist)
			case "missing object":
				if err := os.Remove(filepath.Join(directory, objectName(allowlist.Entries[0].Digest))); err != nil {
					t.Fatal(err)
				}
			}
			filename := filepath.Join(directory, AllowlistFilename)
			if err := os.WriteFile(filename, raw, 0600); err != nil {
				t.Fatal(err)
			}
			if files, err := ReadPrepared(directory); err == nil || files != nil {
				t.Fatal("invalid publication returned a usable snapshot")
			}
			if err := Prepare(directory, plan); err == nil {
				t.Fatal("invalid retained publication silently repaired")
			}
			after, err := os.ReadFile(filename) // #nosec G304 -- invocation-owned temporary allowlist.
			if err != nil || !bytes.Equal(after, raw) {
				t.Fatalf("failed preparation changed retained allowlist: %v", err)
			}
			if fault == "missing object" {
				if _, err := os.Stat(filepath.Join(directory, objectName(allowlist.Entries[0].Digest))); !errors.Is(err, os.ErrNotExist) {
					t.Fatal("failed preparation recreated missing retained object")
				}
			}
		})
	}
}

func TestPreparationRefusesUnadmittedObjectReplacement(t *testing.T) {
	plan := DefaultPlan("expanded", "")
	for _, item := range plan.Resources {
		if len(item.Archive) > 0 {
			plan.Resources = []PreparedResource{item}
			break
		}
	}
	item := plan.Resources[0]
	for _, fault := range []string{"identical bytes", "different bytes", "directory"} {
		t.Run(fault, func(t *testing.T) {
			directory := filepath.Join(t.TempDir(), "prepared")
			if err := PrepareDirectory(directory); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(filepath.Join(directory, "objects"), 0700); err != nil {
				t.Fatal(err)
			}
			name := filepath.Join(directory, objectName(item.BodyDigest))
			body := item.Archive
			if fault == "different bytes" {
				body = []byte("interrupted unadmitted object")
			}
			if fault == "directory" {
				if err := os.Mkdir(name, 0700); err != nil {
					t.Fatal(err)
				}
			} else if err := os.WriteFile(name, body, 0600); err != nil {
				t.Fatal(err)
			}
			err := Prepare(directory, plan)
			if fault == "identical bytes" {
				if err != nil {
					t.Fatal("existing complete object could not be admitted", err)
				}
				files, err := ReadPrepared(directory)
				if err != nil || len(files) != 1 || !bytes.Equal(files["/"+item.Resource.Key+".zip"], item.Archive) {
					t.Fatalf("admitted object changed: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatal("invalid preexisting object accepted")
			}
			if _, err := os.Stat(filepath.Join(directory, AllowlistFilename)); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("failed object staging published an allowlist")
			}
			if fault == "different bytes" {
				after, err := os.ReadFile(name) // #nosec G304 -- invocation-owned immutable object fixture.
				if err != nil || !bytes.Equal(after, body) {
					t.Fatalf("invalid object was overwritten: %v", err)
				}
			} else if info, err := os.Stat(name); err != nil || !info.IsDir() {
				t.Fatal("preexisting directory was replaced")
			}
		})
	}
}

func TestPreparationRejectsCorruptNewArchiveBeforePublication(t *testing.T) {
	plan := DefaultPlan("expanded", "")
	for _, item := range plan.Resources {
		if len(item.Archive) > 0 {
			plan.Resources = []PreparedResource{item}
			break
		}
	}
	if len(plan.Resources) != 1 || len(plan.Resources[0].Archive) == 0 {
		t.Fatal("no archived sample control")
	}
	control := filepath.Join(t.TempDir(), "control")
	if err := Prepare(control, plan); err != nil {
		t.Fatal("valid new archive refused", err)
	}
	files, err := ReadPrepared(control)
	if err != nil || len(files) != 1 || !bytes.Equal(files["/"+plan.Resources[0].Resource.Key+".zip"], plan.Resources[0].Archive) {
		t.Fatal("valid new archive not readable", err)
	}
	// Change bytes without changing their declared digest or size. A fresh
	// directory has no old allowlist whose identity guard could mask this check.
	plan.Resources[0].Archive = bytes.Clone(plan.Resources[0].Archive)
	plan.Resources[0].Archive[0] ^= 1
	if err := plan.Allowlist().Validate(); err != nil {
		t.Fatal("invalid metadata masks the archive integrity check", err)
	}
	directory := filepath.Join(t.TempDir(), "invalid")
	if err := Prepare(directory, plan); err == nil {
		t.Fatal("corrupt new archive was published")
	}
	if _, err := os.Stat(filepath.Join(directory, AllowlistFilename)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("failed preparation published an allowlist", err)
	}
	if files, err := ReadPrepared(directory); err == nil || files != nil {
		t.Fatal("failed preparation exposed a usable archive snapshot", err)
	}
}
