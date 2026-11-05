//go:build functional

package runtime

import (
	"context"
	"os"
	"os/exec"
	"testing"
	"time"
)

// Exercise inspection failures through the real recipe without rebuilding the
// service or depending on damaged binaries and platform-specific tool failures.
func TestMakeBinaryCheckRejectsInspectionFailures(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	writeFormattingFixture(t, directory, "Makefile", string(readTestFile(t, openTestRepository(t), "Makefile")))
	writeFormattingFixture(t, directory, "go.mod", "module example.com/binaryfixture\n\ngo 1.27.0\n")
	writeFormattingFixture(t, directory, "go-fixture.sh", `#!/bin/sh
set -eu
case "$1 $2" in
  'version -m')
    printf '%s\n' 'CGO_ENABLED=0' '-trimpath=true' 'golang.org/x/crypto/x509roots/fallback'
    if [ "$FAILURE" = metadata ] && [ "$3" = "$BINARY" ]; then exit 1; fi
    ;;
  'tool buildid')
    if [ "$3" = "$BINARY" ]; then
      case "$FAILURE" in buildid) exit 1;; retained-id) printf '%s\n' retained;; esac
    fi
    ;;
  'tool nm')
    if [ "$3" = "$BINARY" ]; then
      case "$FAILURE" in
        symbols) printf '%s\n' symbol; exit 0;;
        nm) printf '%s\n' 'tool unavailable' >&2; exit 1;;
        nm-status) printf 'reading %s: no symbols\n' "$3" >&2; exit 2;;
      esac
    fi
    printf 'reading %s: no symbol section\nreading %s: no symbols\n' "$3" "$3" >&2
    exit 1
    ;;
  *) exit 99;;
esac
`)
	for _, binary := range []string{"bin/catalogd", "bin/catalogctl"} {
		for _, failure := range []string{"none", "metadata", "buildid", "retained-id", "symbols", "nm", "nm-status"} {
			t.Run(binary+"/"+failure, func(t *testing.T) {
				ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
				defer cancel()
				// #nosec G204 -- Fixed Make target and test-owned fixture, no caller-selected command.
				command := exec.CommandContext(ctx, "make", "--no-print-directory", "--assume-old=build", "binary-check", "CATALOG_ENV_FILE=/dev/null", "GO=sh go-fixture.sh")
				command.Dir = directory
				command.Env = []string{"PATH=" + os.Getenv("PATH"), "FAILURE=" + failure, "BINARY=" + binary}
				output, err := command.CombinedOutput()
				if (err == nil) != (failure == "none") {
					t.Fatalf("inspection failure %s: err=%v output=%s", failure, err, output)
				}
			})
		}
	}
}
