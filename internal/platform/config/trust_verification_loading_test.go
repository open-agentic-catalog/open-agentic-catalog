package config

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"os"
	"path/filepath"
	"testing"
)

func TestTrustVerificationDisabledAndOwnerStartupDoNotReadKeys(t *testing.T) {
	clearRuntimeEnvironment(t)
	c := trustConfiguration(t)
	c.TrustVerification.Enabled = false
	c.TrustVerification.Policies[0].RegistrationKeys[0].File = "/unavailable/never-read.pem"
	c.TrustVerification.Policies[0].LogKeyFiles = []string{"/unavailable/never-read-log.pem"}
	write := func() string {
		t.Helper()
		raw, err := json.Marshal(c)
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(t.TempDir(), "config.json")
		if err := os.WriteFile(path, raw, 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	for _, role := range []Role{RoleAPI, RoleWorker, RoleAll} {
		loaded, err := LoadFor(write(), role)
		if err != nil || loaded.TrustVerification.Enabled {
			t.Fatal("disabled startup consumed authority input", role, err)
		}
	}
	c.TrustVerification.Enabled = true
	c.Database.Driver = "postgres"
	c.Database.DSN = "postgres://never-contact.invalid/catalog"
	if _, err := LoadFor(write(), RoleMigration); err != nil {
		t.Fatal("migration owner consumed verification policy", err)
	}
	// An enabled API must fail without local keys after separate egress admission.
	c.Database.Driver = "memory"
	c.Database.DSN = ""
	policy := filepath.Join(t.TempDir(), "egress.json")
	raw, err := json.Marshal(c.Federation.Policy)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(policy, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	c.Federation.PolicyFile = policy
	if _, err := LoadFor(write(), RoleAPI); err == nil {
		t.Fatal("enabled startup accepted missing authority keys")
	}
}

func TestTrustVerificationRegularProjectionAndPublicKeyType(t *testing.T) {
	c := trustConfiguration(t)
	path := filepath.Join(t.TempDir(), "projected.pem")
	if err := os.Symlink(c.TrustVerification.Policies[0].LogKeyFiles[0], path); err != nil {
		t.Fatal(err)
	}
	c.TrustVerification.Policies[0].LogKeyFiles = []string{path}
	if err := loadTrustVerification(&c); err != nil {
		t.Fatal("regular ConfigMap-style projection rejected", err)
	}
	key, err := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	path = filepath.Join(t.TempDir(), "wrong-curve.pem")
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	c.TrustVerification.Policies[0].LogKeyFiles = []string{path}
	if loadTrustVerification(&c) == nil {
		t.Fatal("non-P256 public key admitted")
	}
	if os.Geteuid() == 0 {
		t.Skip("root bypasses unreadable-file permissions")
	}
	if err := os.Chmod(path, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chmod(path, 0o600); err != nil {
			t.Error(err)
		}
	})
	if _, err := readVerificationKey(path); err == nil {
		t.Fatal("unreadable key accepted")
	}
}
