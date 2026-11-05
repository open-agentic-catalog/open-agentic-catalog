//go:build functional && unix

package main

import (
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"syscall"
	"testing"
)

// The subprocess confines umask changes; the parent test process stays unchanged.
func TestGeneratePreservesContainerReadPermissionsUnderUmask(t *testing.T) {
	const directoryVariable = "CATALOG_FIXTURE_PERMISSION_TEST_DIRECTORY"
	const maskVariable = "CATALOG_FIXTURE_PERMISSION_TEST_MASK"
	if directory := os.Getenv(directoryVariable); directory != "" {
		mask, err := strconv.ParseUint(os.Getenv(maskVariable), 8, 9)
		if err != nil {
			t.Fatal("invalid fixture permission test mask")
		}
		previous := syscall.Umask(int(mask))
		defer syscall.Umask(previous)
		if err := generate([]string{"--directory", directory, "--subnet", "10.203.240.0/24", "--webhooks"}); err != nil {
			t.Fatal("fixture input generation failed")
		}
		return
	}
	for _, mask := range []string{"022", "077"} {
		t.Run(mask, func(t *testing.T) {
			parent := filepath.Join(t.TempDir(), "private")
			if err := os.Mkdir(parent, 0o700); err != nil {
				t.Fatal("cannot protect fixture invocation parent")
			}
			directory := filepath.Join(parent, "fixture")
			executable, err := os.Executable()
			if err != nil {
				t.Fatal("cannot locate the current test binary")
			}
			// #nosec G204 -- current test binary and fixed helper selection; no external command input.
			command := exec.CommandContext(t.Context(), executable, "-test.run=^TestGeneratePreservesContainerReadPermissionsUnderUmask$")
			command.Env = append(os.Environ(), directoryVariable+"="+directory, maskVariable+"="+mask)
			if err := command.Run(); err != nil {
				t.Fatal("fixture generation subprocess failed")
			}
			var inputs []string
			if err := filepath.WalkDir(directory, func(path string, entry fs.DirEntry, walkErr error) error {
				if walkErr != nil {
					return walkErr
				}
				info, err := entry.Info()
				if err != nil {
					return err
				}
				want := fs.FileMode(0o444)
				switch {
				case entry.IsDir():
					want = 0o755
				case !info.Mode().IsRegular():
					t.Fatal("fixture input is not a regular file")
				default:
					relative, err := filepath.Rel(directory, path)
					if err != nil {
						return err
					}
					inputs = append(inputs, filepath.ToSlash(relative))
				}
				if info.Mode().Perm() != want {
					t.Errorf("fixture input %s has mode %o, want %o", filepath.Base(path), info.Mode().Perm(), want)
				}
				return nil
			}); err != nil {
				t.Fatal("fixture permission inspection failed")
			}
			if !slices.Equal(inputs, []string{
				"credentials/api_key.json", "credentials/api_key.token",
				"credentials/bearer.json", "credentials/bearer.token",
				"credentials/client-key.pem", "credentials/client-two-key.pem",
				"credentials/client-two.pem", "credentials/client.pem", "credentials/mtls.json",
				"oidc-jwks.json", "oidc-server-key.pem", "oidc-server.pem", "policy.json",
				"server-key.pem", "server.pem", "trust/functional-ca.pem",
				"webhooks/ca.pem", "webhooks/encryption.key", "webhooks/policy.json",
			}) {
				t.Fatal("fixture generation omitted or added an input")
			}
			info, err := os.Stat(parent)
			if err != nil || info.Mode().Perm() != 0o700 {
				t.Fatal("generation changed the private invocation parent")
			}
		})
	}
}
