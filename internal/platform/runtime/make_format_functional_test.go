//go:build functional

package runtime

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Exercise the real Make recipes in disposable trees, never format this checkout
// or depend on a caller's env file, generated output, or downloaded packages.
func TestMakeFormatting(t *testing.T) {
	t.Parallel()
	makefile := readTestFile(t, openTestRepository(t), "Makefile")
	fixture := func(t *testing.T) string {
		t.Helper()
		directory := t.TempDir()
		for _, name := range []string{"api", "cmd", "examples", "internal", "migrations", "schemas", "samples", "test", "catalog-ui/cmd", "catalog-ui/internal"} {
			if err := os.MkdirAll(filepath.Join(directory, name), 0o700); err != nil {
				t.Fatal(err)
			}
		}
		writeFormattingFixture(t, directory, "Makefile", string(makefile))
		writeFormattingFixture(t, directory, "go.mod", "module example.com/formatfixture\n\ngo 1.27.0\n")
		return directory
	}
	t.Run("parse errors fail check", func(t *testing.T) {
		directory := fixture(t)
		writeFormattingFixture(t, directory, "internal/broken.go", "package broken\nfunc (\n")
		if output, err := runFormattingMake(t, directory, "fmt-check"); err == nil || !strings.Contains(output, "broken.go") {
			t.Fatalf("parse failure was not reported: err=%v output=%s", err, output)
		}
	})
	t.Run("missing source root fails check", func(t *testing.T) {
		directory := fixture(t)
		if err := os.Remove(filepath.Join(directory, "api")); err != nil {
			t.Fatal(err)
		}
		if output, err := runFormattingMake(t, directory, "fmt-check"); err == nil || !strings.Contains(output, "api") {
			t.Fatalf("traversal failure was not reported: err=%v output=%s", err, output)
		}
	})
	t.Run("owned files and whitespace paths", func(t *testing.T) {
		directory := fixture(t)
		const relative = "internal/nested directory/value file.go"
		const input = "package fixture\nvar Value=1\n"
		writeFormattingFixture(t, directory, relative, input)
		const uiRelative = "catalog-ui/internal/example/value.go"
		writeFormattingFixture(t, directory, uiRelative, input)
		const samplesRelative = "samples/embed.go"
		writeFormattingFixture(t, directory, samplesRelative, input)
		if output, err := runFormattingMake(t, directory, "fmt-check"); err == nil || !strings.Contains(output, relative) {
			t.Fatalf("unformatted file passed: err=%v output=%s", err, output)
		}
		if raw := readFormattingFixture(t, directory, relative); string(raw) != input {
			t.Fatal("fmt-check changed its input")
		}
		if output, err := runFormattingMake(t, directory, "fmt"); err != nil {
			t.Fatalf("format failed: %v: %s", err, output)
		}
		if raw := readFormattingFixture(t, directory, relative); string(raw) != "package fixture\n\nvar Value = 1\n" {
			t.Fatalf("format result differs: content=%q", raw)
		}
		if raw := readFormattingFixture(t, directory, uiRelative); string(raw) != "package fixture\n\nvar Value = 1\n" {
			t.Fatal("UI host source was not formatted")
		}
		if raw := readFormattingFixture(t, directory, samplesRelative); string(raw) != "package fixture\n\nvar Value = 1\n" {
			t.Fatal("sample embedding source was not formatted")
		}
		if output, err := runFormattingMake(t, directory, "fmt-check"); err != nil {
			t.Fatalf("formatted input rejected: %v: %s", err, output)
		}
	})
	t.Run("generated and third-party inputs stay untouched", func(t *testing.T) {
		directory := fixture(t)
		const input = "package untouched\nvar Value=1\n"
		paths := []string{"coverage/fixture.go", "output/fixture.go", "objects/fixture.go", "vendor/fixture.go", "documentation/node_modules/tool/fixture.go", "catalog-ui/node_modules/tool/fixture.go", "test/validation/node_modules/tool/fixture.go", "schemas/upstream/profile/fixture.go", "internal/a2apb/a2a.pb.go"}
		for _, relative := range paths {
			writeFormattingFixture(t, directory, relative, input)
		}
		for _, target := range []string{"fmt-check", "fmt", "fmt-check"} {
			if output, err := runFormattingMake(t, directory, target); err != nil {
				t.Fatalf("%s inspected excluded inputs: %v: %s", target, err, output)
			}
		}
		for _, relative := range paths {
			if raw := readFormattingFixture(t, directory, relative); string(raw) != input {
				t.Fatalf("excluded file %s changed", relative)
			}
		}
	})
}

func readFormattingFixture(t *testing.T, directory, relative string) []byte {
	t.Helper()
	root, err := os.OpenRoot(directory)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })
	return readTestFile(t, root, relative)
}

func writeFormattingFixture(t *testing.T, directory, relative, content string) {
	t.Helper()
	path := filepath.Join(directory, relative)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func runFormattingMake(t *testing.T, directory, target string) (string, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	// #nosec G204 -- Fixed Make executable, test-owned directory and target names.
	command := exec.CommandContext(ctx, "make", "--no-print-directory", target, "CATALOG_ENV_FILE=/dev/null")
	command.Dir = directory
	command.Env = []string{"PATH=" + os.Getenv("PATH")}
	output, err := command.CombinedOutput()
	return string(output), err
}
