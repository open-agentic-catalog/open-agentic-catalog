//go:build functional

package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

func scannerTestPython(t *testing.T) string {
	t.Helper()
	if name := os.Getenv("MCP_SCANNER_TEST_PYTHON"); name != "" {
		return name
	}
	path, err := exec.LookPath("python3")
	if err != nil {
		t.Fatal("local scanner process tests require python3:", err)
	}
	return path
}

func TestScannerCommandOutputLimits(t *testing.T) {
	const child = `import sys
sys.stdout.buffer.write(b"o" * int(sys.argv[1]))
sys.stderr.buffer.write(b"e" * int(sys.argv[2]))
sys.exit(int(sys.argv[3]))
`
	scanner := pythonScanner{python: scannerTestPython(t), runner: writePrivate(t, "output.py", []byte(child))}
	const maxStdout, maxStderr = 4 << 20, 64 << 10
	for _, tc := range []struct {
		name                 string
		stdout, stderr, exit int
		overflow             bool
	}{
		{"both at limit", maxStdout, maxStderr, 0, false},
		{"extra stdout byte", maxStdout + 1, 3, 0, true},
		{"extra stderr byte", 3, maxStderr + 1, 0, true},
		{"finding exit", 3, 5, 1, false},
		{"overflow with finding exit", 3, maxStderr + 1, 1, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			raw, status, err := scanner.command(ctx, strconv.Itoa(tc.stdout), strconv.Itoa(tc.stderr), strconv.Itoa(tc.exit))
			if tc.overflow {
				requirePermanentScannerFailure(t, err, "scanner_output_limit")
				if raw != nil || status != 2 {
					t.Fatalf("overflow returned %d stdout bytes and status %d", len(raw), status)
				}
				return
			}
			if err != nil || status != tc.exit || !bytes.Equal(raw, bytes.Repeat([]byte("o"), tc.stdout)) {
				t.Fatalf("expected %d stdout bytes and exit %d, got %d bytes, status %d, err=%v", tc.stdout, tc.exit, len(raw), status, err)
			}
		})
	}
}

func TestScannerCommandStartAndIdentityFailures(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	scanner := pythonScanner{python: filepath.Join(t.TempDir(), "missing-python"), runner: writePrivate(t, "identity.py", []byte("print('{}')\n"))}
	raw, status, err := scanner.command(ctx, "identity")
	var cause *failure
	if !errors.As(err, &cause) || cause.code != "scanner_process_unavailable" || !cause.transient || raw != nil || status != 2 {
		t.Fatalf("missing executable: bytes=%d status=%d failure=%#v", len(raw), status, cause)
	}
	_, err = scanner.Identity(ctx)
	requirePermanentScannerFailure(t, err, "scanner_tools_unavailable")
	scanner.python = scannerTestPython(t)
	_, err = scanner.Identity(ctx)
	requirePermanentScannerFailure(t, err, "scanner_tools_unavailable")
}

func TestProcessScannerOutputLimits(t *testing.T) {
	python := scannerTestPython(t)
	for _, tc := range []struct {
		mode               string
		collections, scans int
	}{
		{"normal", 1, 1},
		{"identity-stderr-overflow", 0, 0},
		{"scan-stdout-overflow", 1, 1},
		{"scan-stderr-overflow", 1, 1},
	} {
		t.Run(tc.mode, func(t *testing.T) {
			a, store, _, collector, _ := fixtureApplication()
			dir := t.TempDir()
			a.scanner = &pythonScanner{python: python, runner: writeScannerDouble(t, dir, tc.mode)}
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			event := store.pending.Event
			if err := a.process(ctx); err != nil {
				t.Fatal("record scanner outcome:", err)
			}
			if store.finished != 1 || store.retried != 0 || store.pending != nil {
				t.Fatalf("finished=%d retries=%d pending=%t outcome=%q", store.finished, store.retried, store.pending != nil, store.code)
			}
			if calls := scannerCalls(t, dir); collector.calls != tc.collections || calls != tc.scans {
				t.Fatalf("collection calls=%d scan calls=%d", collector.calls, calls)
			}
			o := store.last
			if o == nil || o.Trigger == nil || *o.Trigger != event || o.AttemptedReleaseID != event.TargetID {
				t.Fatal("missing observation for the selected receipt and release")
			}
			if tc.mode == "normal" {
				if store.code != "" || o.State != "eligible_at_check" || len(store.lastRaw) == 0 {
					t.Fatalf("valid report: state=%q outcome=%q bytes=%d", o.State, store.code, len(store.lastRaw))
				}
				return
			}
			if store.code != "scanner_output_limit" || o.State != "failed" || o.Failure != store.code || store.lastRaw != nil || o.ScanKey != "" {
				t.Fatalf("overflow: state=%q outcome=%q failure=%q report bytes=%d key=%q", o.State, store.code, o.Failure, len(store.lastRaw), o.ScanKey)
			}
		})
	}
}
