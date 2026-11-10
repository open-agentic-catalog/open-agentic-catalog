//go:build minikube && functional

package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestVaultListenerWaitUsesReadOnlyStatusAndExpectedSealState(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("Python is required for the owned process fixture")
	}
	for _, tc := range []struct {
		name        string
		initialized bool
		body        string
		failures    int
		exitCode    int
		wantCalls   int
		wantError   bool
		deadline    bool
	}{
		{"uninitialized sealed listener", false, `{"initialized":false,"sealed":true}`, 0, 2, 1, false, false},
		{"retained sealed listener", true, `{"initialized":true,"sealed":true}`, 0, 2, 1, false, false},
		{"listener starts after transport failures", false, `{"initialized":false,"sealed":true}`, 2, 2, 3, false, false},
		{"unexpected retained initialization", false, `{"initialized":true,"sealed":true}`, 0, 2, 1, true, false},
		{"unexpected uninitialized replacement", true, `{"initialized":false,"sealed":true}`, 0, 2, 1, true, false},
		{"unexpected unsealed server", true, `{"initialized":true,"sealed":false}`, 0, 0, 1, true, false},
		{"missing status fields", false, `{}`, 0, 2, 1, true, false},
		{"malformed status", false, `{`, 0, 2, 1, true, false},
		{"command failure is not valid status", false, `{"initialized":false,"sealed":true}`, 0, 1, 0, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			directory := t.TempDir()
			config, err := json.Marshal(map[string]any{"body": tc.body, "failures": tc.failures, "exitCode": tc.exitCode})
			if err != nil {
				t.Fatal(err)
			}
			write := func(name, source string, mode os.FileMode) {
				t.Helper()
				if err := os.WriteFile(filepath.Join(directory, name), []byte(source), mode); err != nil {
					t.Fatal(err)
				}
			}
			write("config.json", string(config), 0o600)
			write("kubectl", `#!/usr/bin/env python3
import json, os, pathlib, sys
root = pathlib.Path(__file__).parent
with (root / "kubectl-args").open("a") as stream:
    stream.write(json.dumps(sys.argv[1:]) + "\n")
command = sys.argv[sys.argv.index("--") + 1:]
environment = dict(os.environ, PATH=str(root) + os.pathsep + os.environ["PATH"])
os.execvpe(command[0], command, environment)
`, 0o700)
			write("vault", `#!/usr/bin/env python3
import json, pathlib, sys
root = pathlib.Path(__file__).parent
with (root / "vault-args").open("a") as stream:
    stream.write(json.dumps(sys.argv[1:]) + "\n")
if sys.argv[1:] != ["status", "-format=json"]:
    sys.exit(91)
config = json.loads((root / "config.json").read_text())
counter = root / "calls"
calls = int(counter.read_text()) + 1 if counter.exists() else 1
counter.write_text(str(calls))
if calls <= config["failures"]:
    print("connection refused", file=sys.stderr)
    sys.exit(1)
print(config["body"])
sys.exit(config["exitCode"])
`, 0o700)
			budget := 5 * time.Second
			if tc.deadline {
				budget = 250 * time.Millisecond
			}
			ctx, cancel := context.WithTimeout(t.Context(), budget)
			defer cancel()
			h := &harness{ctx: ctx, profile: "owned-vault", kubeconfig: filepath.Join(directory, "private-kubeconfig"), options: options{scenario: "vault", repositoryRoot: directory, kubectl: filepath.Join(directory, "kubectl")}}
			err = h.waitVaultSealed(tc.initialized)
			if (err != nil) != tc.wantError || tc.deadline && !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("status wait returned %v", err)
			}
			if tc.deadline {
				return // Cancellation may interrupt a process while it records its arguments.
			}
			fixtureRoot, err := os.OpenRoot(directory)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := fixtureRoot.Close(); err != nil {
					t.Error(err)
				}
			})
			for _, name := range []string{"kubectl-args", "vault-args"} {
				raw, err := fixtureRoot.ReadFile(name)
				if err != nil {
					t.Fatal(err)
				}
				lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
				if len(lines) != tc.wantCalls {
					t.Fatalf("%s recorded %d calls, want %d", name, len(lines), tc.wantCalls)
				}
				for _, line := range lines {
					var arguments []string
					if err := json.Unmarshal([]byte(line), &arguments); err != nil {
						t.Fatal(err)
					}
					if name == "vault-args" {
						if !slices.Equal(arguments, []string{"status", "-format=json"}) {
							t.Fatalf("status wait issued a non-read command: %v", arguments)
						}
					} else {
						prefix := []string{"--kubeconfig", h.kubeconfig, "--context", h.profile, "-n", vaultNamespace, "exec", vaultRelease + "-0", "--", "sh", "-c"}
						if len(arguments) != len(prefix)+1 || !slices.Equal(arguments[:len(prefix)], prefix) {
							t.Fatalf("status wait changed its owned target: %v", arguments)
						}
					}
				}
			}
		})
	}
}
