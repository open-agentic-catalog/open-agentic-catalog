//go:build functional

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/standards/anstrust"
)

// Exercise the actual CLI entrypoint, stderr sanitization and process exit.
func TestTrustBundleCLIProcess(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	binary := filepath.Join(t.TempDir(), "catalogctl")
	build := exec.CommandContext(ctx, "go", "build", "-mod=readonly", "-o", binary, ".") // #nosec G204,G702 -- fixed command and invocation-owned test binary.
	if raw, e := build.CombinedOutput(); e != nil {
		t.Fatalf("build: %s %v", raw, e)
	}
	dir := "../../internal/standards/anstrust/testdata/native"
	raw, e := os.ReadFile(dir + "/v1.json")
	if e != nil {
		t.Fatal(e)
	}
	bundle, e := anstrust.DecodeBundle(raw)
	if e != nil {
		t.Fatal(e)
	}
	originalReceipt := append([]byte(nil), bundle.Receipt...)
	bundle.Receipt[len(bundle.Receipt)-1] ^= 1
	raw, e = json.Marshal(bundle)
	if e != nil {
		t.Fatal(e)
	}
	tampered := filepath.Join(t.TempDir(), "tampered.json")
	if e := os.WriteFile(tampered, raw, 0600); e != nil { // #nosec G703 -- fixed filename in an invocation-owned temporary directory.
		t.Fatal(e)
	}
	bundle.Receipt = originalReceipt
	var entry map[string]json.RawMessage
	if e := json.Unmarshal(bundle.Entry, &entry); e != nil {
		t.Fatal(e)
	}
	originalIdentifier := entry["identifier"]
	entry["identifier"] = json.RawMessage(`"urn:air:trust-v1.ans.test:agents:another-agent"`)
	bundle.Entry, e = json.Marshal(entry)
	if e != nil {
		t.Fatal(e)
	}
	bundle.Entry = append(bundle.Entry[:len(bundle.Entry)-1], []byte(`,"Identifier":`+string(originalIdentifier)+`}`)...)
	raw, e = json.Marshal(bundle)
	if e != nil {
		t.Fatal(e)
	}
	shadowed := filepath.Join(t.TempDir(), "shadowed.json")
	if e := os.WriteFile(shadowed, raw, 0o600); e != nil { // #nosec G703 -- fixed filename in an invocation-owned temporary directory.
		t.Fatal(e)
	}
	for _, tt := range []struct {
		name, file, at, receiptCode, statusCode, targetCode string
		success                                             bool
	}{
		{"authentic V1", dir + "/v1.json", "2026-10-09T14:31:30Z", "verified", "verified", "verified", true},
		{"authentic V2", dir + "/v2.json", "2026-10-09T14:31:30Z", "verified", "verified", "verified", true},
		{"expired", dir + "/v1.json", "2026-10-10T14:31:30Z", "verified", "status_freshness", "verified", false},
		{"tampered", tampered, "2026-10-09T14:31:30Z", "receipt_signature_or_encoding", "prerequisite_failed", "prerequisite_failed", false},
		{"case-shadowed target", shadowed, "2026-10-09T14:31:30Z", "verified", "verified", "target_mismatch", false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			cmd := exec.CommandContext(ctx, binary, "trust", "verify-bundle", "--file", tt.file, "--policy", dir+"/policy.json", "--at", tt.at) // #nosec G204,G702 -- fixed command and invocation-owned test binary.
			cmd.Env = append(os.Environ(), "CATALOG_CONFIG_FILE=/missing/synthetic-private-config", "CATALOG_DATABASE_DSN=postgres://secret:password@invalid")
			var out, diagnostic bytes.Buffer
			cmd.Stdout, cmd.Stderr = &out, &diagnostic
			e := cmd.Run()
			var r anstrust.Result
			if json.Unmarshal(out.Bytes(), &r) != nil || r.CurrentRegistration != tt.success || (e == nil) != tt.success || r.Receipt.Code != tt.receiptCode || r.Status.Code != tt.statusCode || r.Target.Code != tt.targetCode {
				t.Fatalf("exit %v output %s stderr %s", e, out.Bytes(), diagnostic.Bytes())
			}
			if bytes.Contains(diagnostic.Bytes(), []byte("password")) || bytes.Contains(diagnostic.Bytes(), []byte("private-config")) || tt.success && diagnostic.Len() != 0 {
				t.Fatal("unsafe diagnostics", diagnostic.String())
			}
		})
	}
	cmd := exec.CommandContext(ctx, binary, "trust", "verify-bundle", "--file", "missing-secret-filename", "--policy", dir+"/policy.json") // #nosec G204,G702 -- fixed command and invocation-owned test binary.
	var out, diagnostic bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &diagnostic
	if cmd.Run() == nil || out.Len() != 0 || bytes.Contains(diagnostic.Bytes(), []byte("missing-secret-filename")) {
		t.Fatalf("unsafe input failure: %s %s", out.Bytes(), diagnostic.Bytes())
	}
}
