package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"os"
	"path/filepath"
	"testing"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/standards/anstrust"
)

func TestOfflineTrustCommand(t *testing.T) {
	t.Setenv("CATALOG_CONFIG_FILE", "/missing/runtime-config")
	dir := "../../internal/standards/anstrust/testdata/native"
	args := []string{"trust", "verify-bundle", "--file", dir + "/v2.json", "--policy", dir + "/policy.json", "--at", "2026-10-09T14:31:30Z"}
	raw := captureCatalogctlStdout(t, func() error { return runArgs(args) })
	var r anstrust.Result
	if e := json.Unmarshal(raw, &r); e != nil || !r.CurrentRegistration {
		t.Fatalf("result %s: %v", raw, e)
	}
	args[len(args)-1] = "2026-10-10T14:31:30Z"
	var failure error
	raw = captureCatalogctlStdout(t, func() error { failure = runArgs(args); return nil })
	if failure == nil || json.Unmarshal(raw, &r) != nil || r.CurrentRegistration || r.Status.Code != "status_freshness" || r.Inclusion.State != "valid" {
		t.Fatalf("expired result %s: %v", raw, failure)
	}
	for _, args := range [][]string{{"trust"}, {"trust", "unknown"}, {"trust", "verify-bundle"}, {"trust", "verify-bundle", "--file", "x", "--policy", "x", "positional"}, {"trust", "verify-bundle", "--file", "x", "--policy", "x", "--at", "invalid"}, {"trust", "verify-bundle", "--file", "missing", "--policy", dir + "/policy.json"}} {
		if runArgs(args) == nil {
			t.Fatal("accepted invalid invocation", args)
		}
	}
}

func TestTrustPolicyAndFileFailures(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "policy.json")
	for _, body := range []string{`{}`, `{"unknown":1}`, `{"profile":null}`, `{"registrationKeys":[{"id":"k","file":"missing"}]}`, `{"maxStatusAgeSeconds":86401}`, `{"futureSkewSeconds":-1}`} {
		if e := os.WriteFile(path, []byte(body), 0600); e != nil {
			t.Fatal(e)
		}
		if _, e := loadTrustPolicy(path); e == nil {
			t.Fatal("accepted invalid policy", body)
		}
	}
	if _, e := readTrustFile(dir, 1); e == nil {
		t.Fatal("read directory")
	}
	if _, e := readTrustFile(path, 1); e == nil {
		t.Fatal("read oversized file")
	}
	if _, e := readTrustFile(filepath.Join(dir, "missing"), 1); e == nil {
		t.Fatal("read missing file")
	}
}

func TestTrustPolicyFileAdmission(t *testing.T) {
	source := "../../internal/standards/anstrust/testdata/native"
	original, e := os.ReadFile(source + "/policy.json")
	if e != nil {
		t.Fatal(e)
	}
	dir := t.TempDir()
	for _, name := range []string{"ra.pem", "tl.pem"} {
		b, e := os.ReadFile(source + "/" + name) // #nosec G304 -- fixed checked-in public test keys.
		if e != nil {
			t.Fatal(e)
		}
		if e := os.WriteFile(filepath.Join(dir, name), b, 0600); e != nil { // #nosec G703 -- fixed public-key names in an invocation-owned directory.
			t.Fatal(e)
		}
	}
	path := filepath.Join(dir, "policy.json")
	for _, change := range []func(*trustPolicyFile){
		func(f *trustPolicyFile) { f.RegistrationKeys = append(f.RegistrationKeys, f.RegistrationKeys[0]) },
		func(f *trustPolicyFile) { f.RegistrationKeys[0].File = "" },
		func(f *trustPolicyFile) { f.LogKeyFiles = []string{"missing-key"} },
		func(f *trustPolicyFile) { f.Profile = "unknown" },
		func(f *trustPolicyFile) { f.PublisherDomains = nil },
	} {
		var f trustPolicyFile
		if e := json.Unmarshal(original, &f); e != nil {
			t.Fatal(e)
		}
		change(&f)
		b, e := json.Marshal(f)
		if e != nil {
			t.Fatal(e)
		}
		if e := os.WriteFile(path, b, 0600); e != nil {
			t.Fatal(e)
		}
		if _, e := loadTrustPolicy(path); e == nil {
			t.Fatal("accepted invalid policy file")
		}
	}
	digests := make(map[string]string)
	for _, skew := range []string{"omitted", "60", "0"} {
		var document map[string]json.RawMessage
		if e := json.Unmarshal(original, &document); e != nil {
			t.Fatal(e)
		}
		delete(document, "futureSkewSeconds")
		if skew != "omitted" {
			document["futureSkewSeconds"] = json.RawMessage(skew)
		}
		b, e := json.Marshal(document)
		if e != nil {
			t.Fatal(e)
		}
		if e := os.WriteFile(path, b, 0o600); e != nil {
			t.Fatal(e)
		}
		v, e := loadTrustPolicy(path)
		if e != nil {
			t.Fatal(e)
		}
		digests[skew] = v.PolicyDigest()
	}
	if digests["omitted"] != digests["60"] || digests["0"] == digests["60"] {
		t.Fatal("CLI omission default or explicit zero-skew policy was lost")
	}
	var helpError error
	raw := captureCatalogctlStdout(t, func() error { helpError = runArgs([]string{"trust", "verify-bundle", "--help"}); return nil })
	if !errors.Is(helpError, flag.ErrHelp) || !bytes.Contains(raw, []byte("--policy POLICY")) {
		t.Fatalf("help: %s %v", raw, helpError)
	}
}
