package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestAtomicReportFileWritesPrivateExactBytesAndReplaces(t *testing.T) {
	directory := t.TempDir()
	name := filepath.Join(directory, "observation.json")
	for _, raw := range [][]byte{[]byte("{\"private\":\"first synthetic report\"}\n"), []byte("{\"private\":\"replacement\"}\n")} {
		if err := atomicFile(name, raw); err != nil {
			t.Fatal("write private report", err)
		}
		// #nosec G304 -- The report path is owned by this test's temporary directory.
		got, err := os.ReadFile(name)
		if err != nil || !bytes.Equal(got, raw) {
			t.Fatal("report bytes differ from the supplied report", got, err)
		}
		info, err := os.Stat(name)
		if err != nil || info.Mode().Perm() != 0600 {
			t.Fatal("report must remain private", info, err)
		}
		entries, err := os.ReadDir(directory)
		if err != nil || len(entries) != 1 || entries[0].Name() != "observation.json" {
			t.Fatal("temporary report file retained after success", entries, err)
		}
	}
}

func TestAtomicReportFileCleansTemporaryFileWhenRenameRefused(t *testing.T) {
	directory := t.TempDir()
	name := filepath.Join(directory, "occupied")
	if err := os.Mkdir(name, 0700); err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(name, "retained.json")
	retained := []byte("retained synthetic observation")
	if err := os.WriteFile(sentinel, retained, 0600); err != nil {
		t.Fatal(err)
	}
	if err := atomicFile(name, []byte("new private report")); err == nil || err.Error() != "report_render_failed" {
		t.Fatal("rename over a directory must fail without disclosing report paths or bytes", err)
	}
	// #nosec G304 -- The retained fixture is owned by this test's temporary directory.
	got, err := os.ReadFile(sentinel)
	if err != nil || !bytes.Equal(got, retained) {
		t.Fatal("rename refusal changed retained data", got, err)
	}
	entries, err := os.ReadDir(directory)
	if err != nil || len(entries) != 1 || entries[0].Name() != "occupied" || !entries[0].IsDir() {
		t.Fatal("temporary report file retained after rename refusal", entries, err)
	}
}

func TestAtomicReportFileDoesNotCreateMissingParents(t *testing.T) {
	directory := t.TempDir()
	name := filepath.Join(directory, "missing", "report.json")
	if err := atomicFile(name, []byte("private report")); err == nil || err.Error() != "report_render_failed" {
		t.Fatal("missing private report directory must fail safely", err)
	}
	entries, err := os.ReadDir(directory)
	if err != nil || len(entries) != 0 {
		t.Fatal("failed creation left report artifacts", entries, err)
	}
}
