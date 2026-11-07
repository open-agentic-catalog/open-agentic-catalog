//go:build functional

// Real processes qualify owned Python selection against missing and old ambient coverage.
package packaging

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestPackagingPythonProcess checks real venv identity and exact-pin refusal without installing packages.
func TestPackagingPythonProcess(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	repository, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	qualified, err := packagingPython(repository)
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(t.TempDir(), "checkout with spaces")
	if err := os.MkdirAll(filepath.Join(root, ".tools"), 0o700); err != nil {
		t.Fatal(err)
	}
	// Reuse the installed native package through a fixture-only .pth file, so
	// correctly pinned ambient-only installations retain the fallback contract.
	qualifiedEnvironment := filepath.Join(root, "qualified python")
	prepare := "import pathlib,sys,venv; sys.path.insert(0,sys.argv[1]); from tooling_coverage import coverage_tool; module,_=coverage_tool(); venv.EnvBuilder(with_pip=False).create(sys.argv[2]); print(pathlib.Path(module.__file__).resolve().parent.parent)"
	command := exec.CommandContext(ctx, qualified, "-B", "-c", prepare, filepath.Join(repository, "test", "packaging"), qualifiedEnvironment) // #nosec G204 -- interpreter is the installed Python or its test-owned venv; no external input or shell is used.
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("prepare qualified environment: %v\n%s", err, output)
	}
	packageDirectory := strings.TrimSpace(string(output))
	command = exec.CommandContext(ctx, filepath.Join(qualifiedEnvironment, "bin", "python"), "-I", "-c", "import sysconfig; print(sysconfig.get_path('purelib'))") // #nosec G204 -- interpreter is the installed Python or its test-owned venv; no external input or shell is used.
	output, err = command.CombinedOutput()
	if err != nil {
		t.Fatalf("locate qualified fixture packages: %v\n%s", err, output)
	}
	if err := os.WriteFile(filepath.Join(strings.TrimSpace(string(output)), "qualified.pth"), []byte(packageDirectory+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(qualifiedEnvironment, filepath.Join(root, ".tools", "dev")); err != nil {
		t.Fatal(err)
	}
	ambient := filepath.Join(root, "ambient python")
	command = exec.CommandContext(ctx, qualified, "-I", "-m", "venv", "--without-pip", ambient) // #nosec G204 -- interpreter is the installed Python or its test-owned venv; no external input or shell is used.
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("prepare empty ambient environment: %v\n%s", err, output)
	}
	// The process-local fixture PATH emulates the normal unactivated workflow.
	t.Setenv("PATH", filepath.Join(ambient, "bin")+string(os.PathListSeparator)+os.Getenv("PATH"))
	for _, key := range []string{"PYTHONPATH", "PYTHONHOME", "COVERAGE_PROCESS_START", "COVERAGE_PROCESS_CONFIG", "COVERAGE_FILE"} {
		t.Setenv(key, "")
	}
	probe := "import json,sys; sys.path.insert(0,sys.argv[1]); from tooling_coverage import coverage_tool; _,version=coverage_tool(); print(json.dumps({'version':version,'executable':sys.executable,'prefix':sys.prefix}))"
	arguments := []string{"-B", "-c", probe, filepath.Join(repository, "test", "packaging")}
	for _, state := range []string{"missing", "old"} {
		t.Run(state, func(t *testing.T) {
			if state == "old" {
				command := exec.CommandContext(ctx, filepath.Join(ambient, "bin", "python3"), "-I", "-c", "import sysconfig; print(sysconfig.get_path('purelib'))") // #nosec G204 -- interpreter is the installed Python or its test-owned venv; no external input or shell is used.
				output, err := command.Output()
				if err != nil {
					t.Fatal(err)
				}
				metadata := filepath.Join(strings.TrimSpace(string(output)), "coverage-0.0.0.dist-info")
				if err := os.MkdirAll(metadata, 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(metadata, "METADATA"), []byte("Metadata-Version: 2.1\nName: coverage\nVersion: 0.0.0\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			fallback, err := packagingPython(filepath.Join(root, "no-owned-environment"))
			if err != nil || fallback != "python3" {
				t.Fatalf("absent-owned fallback: path=%q error=%v", fallback, err)
			}
			command := exec.CommandContext(ctx, fallback, arguments...) // #nosec G204 -- selection is the repository-owned fixture venv or fixed python3 fallback; probe arguments are fixed.
			output, err := command.CombinedOutput()
			if err == nil || ctx.Err() != nil {
				t.Fatalf("ambient %s coverage did not reach pin refusal: error=%v context=%v output=%s", state, err, ctx.Err(), output)
			}
			refusal := "install the locked coverage tool"
			if state == "old" {
				refusal = "coverage version differs"
			}
			if !strings.Contains(string(output), refusal) {
				t.Fatalf("ambient %s reached another refusal: %s", state, output)
			}
			selected, err := packagingPython(root)
			if err != nil {
				t.Fatal(err)
			}
			command = exec.CommandContext(ctx, selected, arguments...) // #nosec G204 -- selection is the repository-owned fixture venv or fixed python3 fallback; probe arguments are fixed.
			output, err = command.CombinedOutput()
			if err != nil {
				t.Fatalf("qualified interpreter: %v\n%s", err, output)
			}
			var actual struct {
				Version    string `json:"version"`
				Executable string `json:"executable"`
				Prefix     string `json:"prefix"`
			}
			if err := json.Unmarshal(output, &actual); err != nil {
				t.Fatal(err)
			}
			if actual.Version == "" || actual.Version == "0.0.0" || actual.Executable != selected || actual.Prefix != filepath.Join(root, ".tools", "dev") {
				t.Fatalf("venv identity or path was lost: %+v", actual)
			}
		})
	}
}
