// Fixture inputs are read-only mounts beneath a private invocation directory.
package main

import "os"

func makeFixtureDirectory(path string) error {
	// #nosec G301 -- non-root disposable containers traverse this scoped mount.
	if err := os.MkdirAll(path, 0o755); err != nil {
		return err
	}
	// MkdirAll permissions are otherwise reduced by the invoking process umask.
	// #nosec G302 -- the supervisor retains the enclosing private directory.
	return os.Chmod(path, 0o755)
}

func writeFixtureFile(path string, data []byte) error {
	// #nosec G306 -- non-root containers read these leaves through scoped mounts.
	if err := os.WriteFile(path, data, 0o444); err != nil {
		return err
	}
	// #nosec G302 -- explicit read-only mode; the invocation parent protects keys.
	return os.Chmod(path, 0o444)
}
