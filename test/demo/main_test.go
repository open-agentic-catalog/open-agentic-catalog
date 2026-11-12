package main

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestPrepareTLSRetainsPrivateIdentity(t *testing.T) {
	directory := filepath.Join(canonicalTempDir(t), "tls")
	if err := prepareTLS(directory); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(filepath.Join(directory, "cert.pem")) // #nosec G304 -- reads only the owned temporary test certificate.
	if err != nil {
		t.Fatal(err)
	}
	if err := prepareTLS(directory); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(filepath.Join(directory, "cert.pem")) // #nosec G304 -- reads only the owned temporary test certificate.
	if string(before) != string(after) {
		t.Fatal("trust rotated")
	}
	// #nosec G302 -- deliberately insecure input inside a private temporary directory must be rejected.
	if err := os.Chmod(filepath.Join(directory, "key.pem"), 0644); err != nil {
		t.Fatal(err)
	}
	if prepareTLS(directory) == nil {
		t.Fatal("insecure key accepted")
	}
}

func TestPrepareTLSRejectsPartialAndLinkedFiles(t *testing.T) {
	for _, kind := range []string{"partial", "linked", "directory"} {
		t.Run(kind, func(t *testing.T) {
			directory := filepath.Join(canonicalTempDir(t), "tls")
			if err := os.Mkdir(directory, 0700); err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "partial":
				if err := os.WriteFile(filepath.Join(directory, "cert.pem"), []byte("bad"), 0600); err != nil {
					t.Fatal(err)
				}
			case "linked":
				if err := os.Symlink("outside", filepath.Join(directory, "key.pem")); err != nil {
					t.Fatal(err)
				}
			case "directory":
				// #nosec G302 -- deliberately insecure child-directory input must be rejected.
				if err := os.Chmod(directory, 0755); err != nil {
					t.Fatal(err)
				}
			}
			if prepareTLS(directory) == nil {
				t.Fatal("unsafe fixture accepted")
			}
		})
	}
}

func TestPrepareTLSExactPrivateFixtureAddress(t *testing.T) {
	directory := filepath.Join(canonicalTempDir(t), "tls")
	for _, ip := range []string{"127.0.0.1", "8.8.8.8", "172.030.254.41", "not-an-ip"} {
		if prepareTLSForIP(directory, ip) == nil {
			t.Fatal("unsafe fixture IP accepted", ip)
		}
	}
	if err := prepareTLSForIP(directory, "172.30.254.41"); err != nil {
		t.Fatal(err)
	}
	if prepareTLSForIP(directory, "172.30.255.41") == nil {
		t.Fatal("existing certificate silently changed address")
	}
}

func TestTLSPreparationRejectsAncestorLinkWithoutOutput(t *testing.T) {
	target := canonicalTempDir(t)
	alias := filepath.Join(canonicalTempDir(t), "alias")
	if err := os.Symlink(target, alias); err != nil {
		t.Fatal(err)
	}
	if prepareTLS(filepath.Join(alias, "new-tls")) == nil {
		t.Fatal("linked TLS ancestor accepted")
	}
	if _, err := os.Stat(filepath.Join(target, "new-tls")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("TLS wrote before rejecting linked ancestor")
	}
}

// canonicalTempDir resolves only a newly allocated, trusted test root. Explicit
// unsafe paths still reach the production symlink-refusal boundary unchanged.
func canonicalTempDir(tb testing.TB) string {
	tb.Helper()
	directory, err := filepath.EvalSymlinks(tb.TempDir())
	if err != nil {
		tb.Fatal(err)
	}
	return directory
}
