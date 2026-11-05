// Unit tests exercise isolated validation and controlled dependencies.
package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDemoArgumentFailures(t *testing.T) {
	for _, args := range [][]string{{"--sample-set", "unknown"}, {"--sample-set", "minimal", "--extend"}, {"--unknown"}, {"--timeout", "0s"}, {"--timeout", "11m"}, {"--state-file", ""}, {"unexpected"}, {}} {
		if err := seedDemo(args); err == nil {
			t.Fatalf("invalid arguments accepted: %v", args)
		}
	}
	if err := seedDemo([]string{"--help"}); err != nil {
		t.Fatal(err)
	}
}

func TestDemoTargetAndReceiptFailures(t *testing.T) {
	for _, target := range []string{"", "https://example.com", "http://localhost:8080", "http://127.0.0.1/x", "http://user@127.0.0.1", "http://127.0.0.1/?", "http://127.0.0.1/#"} {
		if _, err := executeDemo(t.Context(), target, filepath.Join(t.TempDir(), "receipt.json")); err == nil || (!strings.Contains(err.Error(), "seed-demo: supply an owned literal loopback API origin") && !strings.Contains(err.Error(), "seed-demo: use a literal loopback address without a query or fragment")) {
			t.Fatalf("unsafe target %q did not fail preflight: %v", target, err)
		}
	}
	for _, body := range []string{"null", "{}", `{"dataset":"a","dataset":"b"}`, strings.Repeat("x", demoReceiptLimit+1)} {
		path := filepath.Join(t.TempDir(), "receipt.json")
		if err := os.WriteFile(path, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
		if _, _, err := readDemoReceipt(path, "http://127.0.0.1:8080"); err == nil {
			t.Fatal("invalid receipt accepted")
		}
	}
}
