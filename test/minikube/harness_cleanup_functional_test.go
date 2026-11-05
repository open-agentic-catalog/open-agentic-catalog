//go:build minikube && functional

package main

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// Partial startup and cancellation must still reap only invocation-owned
// resources, even when the first cleanup command fails.
func TestHarnessCleanupAfterPartialStartup(t *testing.T) {
	for _, test := range []struct {
		name         string
		clusterReady bool
		profileExit  int
		vaultFailure bool
	}{
		{name: "partial-startup"},
		{name: "ready-cluster", clusterReady: true},
		{name: "vault-cleanup-fails-still-reaps-profile", clusterReady: true, vaultFailure: true},
		{name: "partial-startup-profile-delete-fails", profileExit: 7},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			files, err := os.OpenRoot(root)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := files.Close(); err != nil {
					t.Error(err)
				}
			})
			owned, err := os.MkdirTemp(os.TempDir(), "open-agentic-catalog-minikube-cleanup-test-")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.RemoveAll(owned) })
			if err := os.WriteFile(filepath.Join(owned, "owned-state"), []byte("disposable"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := files.WriteFile("retained-state", []byte("preserve"), 0600); err != nil {
				t.Fatal(err)
			}
			// Record arguments and explicit ownership inputs using only shell
			// builtins. This executable never invokes Docker or Minikube.
			tool := filepath.Join(root, "owned-tool")
			script := "#!/bin/sh\nprintf '%s|%s|%s|%s\\n' \"$MINIKUBE_PROFILE\" \"$MINIKUBE_HOME\" \"$KUBECONFIG\" \"$*\" >> commands\n" +
				"if [ \"$1\" = --profile ]; then exit " + strconv.Itoa(test.profileExit) + "; fi\nexit 0\n"
			if test.vaultFailure {
				script = strings.Replace(script, "\nexit 0\n", "\nif [ \"$1\" = --kubeconfig ]; then exit 9; fi\nexit 0\n", 1)
			}
			if err := files.WriteFile("owned-tool", []byte(script), 0600); err != nil {
				t.Fatal(err)
			}
			if err := files.Chmod("owned-tool", 0700); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(t.Context())
			cancel()
			h := &harness{
				ctx: ctx, temporary: owned, minikubeHome: filepath.Join(owned, "minikube"), kubeconfig: filepath.Join(owned, "kubeconfig"),
				profile: "owned-profile", network: "owned-network", application: "owned:application", fixture: "owned:fixture", postgres: "owned:postgres", uiImage: "owned:ui",
				networkReady: true, clusterReady: test.clusterReady,
				options: options{scenario: "topology", repositoryRoot: root, minikube: tool, docker: tool},
			}
			if test.vaultFailure {
				h.options.scenario = "vault"
				h.options.kubectl = tool
				h.vault = &vaultState{syncReady: true, rootToken: "private-synthetic", images: map[string]string{}}
			}
			if err := h.cleanup(); (err != nil) != (test.profileExit != 0 || test.vaultFailure) || (test.profileExit != 0 && !strings.Contains(err.Error(), "delete profile owned-profile")) {
				t.Fatalf("cleanup outcome: %v", err)
			}
			raw, err := files.ReadFile("commands")
			if err != nil {
				t.Fatal(err)
			}
			originalRaw := append([]byte(nil), raw...)
			prefix := h.profile + "|" + h.minikubeHome + "|" + h.kubeconfig + "|"
			want := []string{
				prefix + "--profile owned-profile delete --purge=false",
				prefix + "network rm owned-network",
				prefix + "image rm owned:application",
				prefix + "image rm owned:fixture",
				prefix + "image rm owned:postgres",
				prefix + "image rm owned:ui",
			}
			if test.vaultFailure {
				lines := strings.SplitN(string(raw), "\n", 2)
				if len(lines) != 2 || !strings.Contains(lines[0], "--context owned-profile -n catalog-vault-system exec -i catalog-vault-0") {
					t.Fatal("cleanup did not use exact owned provider scope")
				}
				raw = []byte(lines[1])
			}
			if got := strings.Split(strings.TrimSpace(string(raw)), "\n"); !slices.Equal(got, want) {
				t.Fatalf("cleanup commands: got %q, want %q", got, want)
			}
			if _, err := os.Stat(owned); !os.IsNotExist(err) {
				t.Fatalf("owned temporary state remains: %v", err)
			}
			if got, err := files.ReadFile("retained-state"); err != nil || string(got) != "preserve" {
				t.Fatalf("unrelated state changed: %q, %v", got, err)
			}
			if err := h.cleanup(); err != nil {
				t.Fatalf("repeated cleanup: %v", err)
			}

			if again, err := files.ReadFile("commands"); err != nil || string(again) != string(originalRaw) {
				t.Fatalf("cleanup commands repeated: %q, %v", again, err)
			}
		})
	}
}
