// Packaging harnesses use the installed shared Python without shell activation.
package packaging

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// packagingPython follows the existing harness convention, retaining the venv symlink path.
func packagingPython(root string) (string, error) {
	owned := filepath.Join(root, ".tools", "dev", "bin", "python")
	if _, err := os.Stat(owned); err == nil {
		return owned, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	return "python3", nil
}

// TestPackagingPython selects an owned interpreter, preserving its path and filesystem errors.
func TestPackagingPython(t *testing.T) {
	root := filepath.Join(t.TempDir(), "checkout with spaces")
	owned := filepath.Join(root, ".tools", "dev", "bin", "python")
	if err := os.MkdirAll(filepath.Dir(owned), 0o700); err != nil {
		t.Fatal(err)
	}
	if got, err := packagingPython(root); err != nil || got != "python3" {
		t.Fatalf("missing owned interpreter: path=%q error=%v", got, err)
	}
	target := filepath.Join(root, "qualified-python")
	if err := os.WriteFile(target, []byte("inert interpreter fixture\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, owned); err != nil {
		t.Fatal(err)
	}
	if got, err := packagingPython(root); err != nil || got != owned {
		t.Fatalf("owned symlink path was not retained: path=%q error=%v", got, err)
	}
	if err := os.Remove(owned); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(owned, owned); err != nil {
		t.Fatal(err)
	}
	if _, err := packagingPython(root); err == nil {
		t.Fatal("owned interpreter inspection error was hidden by fallback")
	}
}
