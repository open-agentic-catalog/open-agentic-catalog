package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/safeerr"
)

func transferBoundaryReceipt(t *testing.T) importReceipt {
	t.Helper()
	plan := importApplyTestPlan(t)
	receipt, err := newImportReceipt(singleImportTestBatch(plan), "https://catalog.invalid", importTestPrincipal)
	if err != nil {
		t.Fatal(err)
	}
	raw := importTestJSON(t, receipt)
	if _, err := decodeImportReceipt(importTestJSON(t, importReceiptFile{Receipt: receipt, Digest: importDigest(raw)})); err != nil {
		t.Fatal("receipt control must be valid before testing local file boundaries", err)
	}
	return receipt
}

func TestTransferLocalAssetLimitAndCancellation(t *testing.T) {
	directory := canonicalTempDir(t)
	content := []byte("exact local bytes\x00with a final newline\n")
	filename := filepath.Join(directory, "asset.bin")
	if err := os.WriteFile(filename, content, 0600); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()
	got, err := readImportAsset(t.Context(), root, "asset.bin", len(content))
	if err != nil || !bytes.Equal(got, content) {
		t.Fatal("asset at its exact byte limit must preserve original bytes", got, err)
	}
	if got, err := readImportAsset(t.Context(), root, "asset.bin", len(content)-1); err == nil || got != nil || err.Error() != "import assets must be regular files within their byte limit" {
		t.Fatal("one-byte-over-limit asset did not fail before returning content", got, err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if got, err := readImportAsset(ctx, root, "asset.bin", len(content)); !errors.Is(err, context.Canceled) || got != nil || safeerr.Message(err) != "import preview was cancelled or exceeded its deadline" {
		t.Fatal("cancelled asset read lost its cause or returned bytes", got, err)
	}
	// Cancellation is checked before even an invalid local path is inspected.
	if got, err := readImportAsset(ctx, root, "missing.bin", len(content)); !errors.Is(err, context.Canceled) || got != nil {
		t.Fatal("file lookup masked prior cancellation", got, err)
	}
}

func TestTransferReceiptDecoderEnforcesExactByteCeiling(t *testing.T) {
	receipt := transferBoundaryReceipt(t)
	encoded := importTestJSON(t, receipt)
	valid := importTestJSON(t, importReceiptFile{Receipt: receipt, Digest: importDigest(encoded)})
	atLimit := bytes.Clone(valid)
	atLimit = append(atLimit, bytes.Repeat([]byte(" "), importMaxReceiptBytes-len(atLimit))...)
	got, err := decodeImportReceipt(atLimit)
	if err != nil || !reflect.DeepEqual(got, receipt) {
		t.Fatal("valid receipt at exactly 2 MiB was refused or changed", err)
	}
	oversized := bytes.Clone(atLimit)
	oversized = append(oversized, ' ')
	if !json.Valid(oversized) {
		t.Fatal("oversized control must remain otherwise valid JSON")
	}
	if _, err := decodeImportReceipt(oversized); err == nil || err.Error() != "import: state is invalid or changed; preserve it for investigation" {
		t.Fatal("valid JSON exceeding the receipt ceiling by one byte was accepted", err)
	}
}

func TestTransferStatePathRefusalsDoNotCreateProgress(t *testing.T) {
	for _, kind := range []string{"new state control", "reserved lock suffix", "missing parent"} {
		t.Run(kind, func(t *testing.T) {
			directory := canonicalTempDir(t)
			if err := os.Chmod(directory, 0700); err != nil { // #nosec G302 -- this fixture models the required private state directory.
				t.Fatal(err)
			}
			name := filepath.Join(directory, "progress.json")
			switch kind {
			case "reserved lock suffix":
				name = filepath.Join(directory, "progress.lock")
			case "missing parent":
				name = filepath.Join(directory, "missing", "private", "state.json")
			}
			state, err := openImportState(name, false)
			if kind == "new state control" {
				if err != nil || state == nil {
					t.Fatal("valid state path control was refused", err)
				}
				state.close()
				entries, err := os.ReadDir(directory)
				if err != nil || len(entries) != 1 || entries[0].Name() != "progress.json.lock" {
					t.Fatal("admission must create only the private invocation lock", entries, err)
				}
				return
			}
			if state != nil {
				state.close()
				t.Fatal("unsafe state path returned a usable writer")
			}
			if err == nil || err.Error() != "import: state requires a private directory and regular files, without symlinks or another active invocation" {
				t.Fatal("state path refusal did not retain the safe diagnostic", err)
			}
			entries, err := os.ReadDir(directory)
			if err != nil || len(entries) != 0 {
				t.Fatal("state-path refusal left a progress, lock or partial directory", entries, err)
			}
		})
	}
}

func TestTransferFirstStateSavePreservesUnexpectedExistingFile(t *testing.T) {
	receipt := transferBoundaryReceipt(t)
	for _, interference := range []bool{false, true} {
		t.Run(map[bool]string{false: "new state control", true: "file appeared before first save"}[interference], func(t *testing.T) {
			directory := canonicalTempDir(t)
			if err := os.Chmod(directory, 0700); err != nil { // #nosec G302 -- owner-only state directory is a prerequisite.
				t.Fatal(err)
			}
			state, err := openImportState(filepath.Join(directory, "progress.json"), false)
			if err != nil {
				t.Fatal(err)
			}
			defer state.close()
			retained := []byte("private progress that appeared after writer admission\n")
			if interference {
				if err := os.WriteFile(filepath.Join(directory, "progress.json"), retained, 0600); err != nil {
					t.Fatal(err)
				}
			}
			err = state.save(receipt)
			got, readErr := state.root.ReadFile("progress.json")
			if readErr != nil {
				t.Fatal(readErr)
			}
			if interference {
				if err == nil || err.Error() != "import: cannot durably save state; preserve drafts and investigate before retrying" || !bytes.Equal(got, retained) || state.previous != nil {
					t.Fatal("first save replaced unexpected retained bytes or recorded success", err)
				}
			} else {
				decoded, decodeErr := decodeImportReceipt(got)
				if err != nil || decodeErr != nil || !reflect.DeepEqual(decoded, receipt) || !bytes.Equal(state.previous, got) {
					t.Fatal("uncontested first save control did not retain the exact receipt", err, decodeErr)
				}
			}
			entries, err := os.ReadDir(directory)
			if err != nil || len(entries) != 2 || entries[0].Name() != "progress.json" || entries[1].Name() != "progress.json.lock" {
				t.Fatal("first save retained temporary files or unexpected entries", entries, err)
			}
		})
	}
}
