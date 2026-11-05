package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDemoArchiveAdmission(t *testing.T) {
	c := Defaults()
	if err := c.validateDemoArchives(); err != nil {
		t.Fatal(err)
	}
	c.DemoArchives = DemoArchives{Origin: "https://127.0.0.1:18443", TrustBundleFile: "local.pem", AllowlistFile: "allowlist.json"}
	if err := c.validateDemoArchives(); err != nil {
		t.Fatal(err)
	}
	for _, origin := range []string{"http://127.0.0.1:18443", "https://localhost:18443", "https://8.8.8.8:18443", "https://127.0.0.1:18443/", "https://127.0.0.1:18443?x=1", "https://127.0.0.1:18443?", "https://127.0.0.1:18443#fragment", "https://user@127.0.0.1:18443", "https://127.0.0.1:99999", "https://[::ffff:127.0.0.1]:18443", "https://[fe80::1%25eth0]:18443"} {
		bad := c
		bad.DemoArchives.Origin = origin
		if bad.validateDemoArchives() == nil {
			t.Fatal("accepted", origin)
		}
	}
	for _, env := range []string{"production", "test"} {
		bad := c
		bad.Environment = env
		if bad.ValidateFor(RoleAPI) == nil {
			t.Fatal("enabled outside development")
		}
	}
	bad := c
	bad.Auth.Mode = "oidc"
	if bad.validateDemoArchives() == nil {
		t.Fatal("enabled with real identities")
	}
	bad = c
	bad.DemoArchives.TrustBundleFile = ""
	if bad.validateDemoArchives() == nil {
		t.Fatal("missing trust accepted")
	}
	bad = c
	bad.DemoArchives.Origin = ""
	if bad.validateDemoArchives() == nil {
		t.Fatal("disabled trust accepted")
	}
	if !c.Report().DemoArchivesEnabled {
		t.Fatal("missing diagnostic flag")
	}
}

func TestDemoArchiveOverrideAndTrust(t *testing.T) {
	c := Defaults()
	for _, raw := range []string{`null`, `{"origin":null}`, `{"origin":"a","origin":"b"}`, `{"Origin":"a"}`, `{"trustBundlePEM":"secret"}`, `{"unknown":true}`} {
		if applyDemoArchives(&c, raw) == nil {
			t.Fatal("accepted", raw)
		}
	}
	if err := applyDemoArchives(&c, `{"origin":"https://127.0.0.1:18443","trustBundleFile":"local.pem"}`); err != nil {
		t.Fatal(err)
	}
	if c.DemoArchives.Origin != "https://127.0.0.1:18443" {
		t.Fatal("override lost")
	}
	if loadDemoArchives(&c) == nil {
		t.Fatal("missing file accepted")
	}
	path := filepath.Join(t.TempDir(), "invalid.pem")
	if err := os.WriteFile(path, []byte("not PEM"), 0600); err != nil {
		t.Fatal(err)
	}
	c.DemoArchives.TrustBundleFile = path
	if loadDemoArchives(&c) == nil {
		t.Fatal("invalid PEM accepted")
	}
	if err := os.WriteFile(path, federationTestCertificate(t), 0600); err != nil {
		t.Fatal(err)
	}
	allowlist := filepath.Join(t.TempDir(), "allowlist.json")
	if err := os.WriteFile(allowlist, []byte(`{"format":1,"tenantId":"00000000-0000-7000-8000-000000000001","entries":[]}`), 0600); err != nil {
		t.Fatal(err)
	}
	c.DemoArchives.AllowlistFile = allowlist
	if err := loadDemoArchives(&c); err != nil {
		t.Fatal(err)
	}
	if len(c.DemoArchives.TrustBundlePEM) == 0 || c.DemoArchives.TrustDigest == "" {
		t.Fatal("trust not bound")
	}
	c.DemoArchives = DemoArchives{}
	if err := loadDemoArchives(&c); err != nil {
		t.Fatal(err)
	}
}

func TestDemoArchiveAllowlistRefusalAndDisabledInputs(t *testing.T) {
	c := Defaults()
	directory := t.TempDir()
	trust := filepath.Join(directory, "trust.pem")
	allowlist := filepath.Join(directory, "allowlist.json")
	if err := os.WriteFile(trust, federationTestCertificate(t), 0600); err != nil {
		t.Fatal(err)
	}
	c.DemoArchives = DemoArchives{Origin: "https://127.0.0.1:18443", TrustBundleFile: trust, AllowlistFile: allowlist}
	if loadDemoArchives(&c) == nil {
		t.Fatal("missing allowlist accepted")
	}
	for _, raw := range []string{`{"format":1,"tenantId":"other","entries":[]}`, `{"format":1,"tenantId":"00000000-0000-7000-8000-000000000001","entries":null}`, `{"format":1,"format":1,"tenantId":"00000000-0000-7000-8000-000000000001","entries":[]}`, `{"format":1,"unknown":true}`, strings.Repeat(" ", (32<<10)+1)} {
		if err := os.WriteFile(allowlist, []byte(raw), 0600); err != nil {
			t.Fatal(err)
		}
		if loadDemoArchives(&c) == nil {
			t.Fatal("invalid allowlist accepted")
		}
	}
	c.DemoArchives.AllowlistFile = ""
	if c.validateDemoArchives() == nil {
		t.Fatal("enabled policy without allowlist file")
	}
	c.DemoArchives = DemoArchives{AllowlistFile: allowlist}
	if c.validateDemoArchives() == nil {
		t.Fatal("disabled policy with allowlist file")
	}
}
