package cliauth

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/safeerr"
)

func TestLoginLocalRefusalsPreserveCredentialsWithoutIssuerContact(t *testing.T) {
	for _, kind := range []string{"existing", "corrupt", "missing directory", "invalid trust", "cancelled"} {
		t.Run(kind, func(t *testing.T) {
			dir := privateDirectory(t)
			path := filepath.Join(dir, "login")
			o := Options{Issuer: "https://issuer.invalid/realm", ClientID: "cli", APIOrigin: "https://api.invalid", File: path, Listen: "127.0.0.1:0", Timeout: time.Minute}
			ctx := t.Context()
			var want string
			switch kind {
			case "existing":
				writeControl(t, path, storedControl())
				want = "credential file already exists"
			case "corrupt":
				if err := os.WriteFile(path, []byte("PRIVATE_INVALID_DOCUMENT"), 0600); err != nil {
					t.Fatal(err)
				}
				want = "invalid credential file"
			case "missing directory":
				o.File = filepath.Join(dir, "missing", "login")
				want = "existing owned 0700 directory"
			case "invalid trust":
				o.IssuerCAFile = filepath.Join(dir, "PRIVATE_MISSING_ROOTS")
				want = "cannot configure issuer TLS trust"
			case "cancelled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
				want = "cancelled or deadline exceeded"
			}
			before, beforeErr := os.ReadFile(path) // #nosec G304 -- Owned temporary credential fixture.
			var diagnostics bytes.Buffer
			err := Login(ctx, o, &diagnostics)
			if err == nil || !strings.Contains(safeerr.Message(err), want) || strings.Contains(safeerr.Message(err), "PRIVATE") || diagnostics.Len() != 0 {
				t.Fatalf("local refusal did not precede issuer discovery or leaked diagnostics: %v", err)
			}
			after, afterErr := os.ReadFile(path) // #nosec G304 -- Owned temporary credential fixture.
			if !bytes.Equal(before, after) || (beforeErr == nil) != (afterErr == nil) {
				t.Fatal("refused login changed stored credentials")
			}
		})
	}
}

func TestAccessTokenLocalRefreshFailureDoesNotReturnOrRewriteSecrets(t *testing.T) {
	path := filepath.Join(privateDirectory(t), "login")
	c := storedControl()
	c.ExpiresAt = time.Now().Add(-time.Hour)
	writeControl(t, path, c)
	before, err := os.ReadFile(path) // #nosec G304 -- Owned temporary credential fixture.
	if err != nil {
		t.Fatal(err)
	}
	token, err := AccessToken(t.Context(), path, c.APIOrigin, "", filepath.Join(filepath.Dir(path), "PRIVATE_MISSING_ROOTS"))
	if err == nil || token != "" || safeerr.Message(err) != "auth: cannot configure issuer TLS trust" {
		t.Fatalf("refresh trust failure returned stale credentials or unsafe diagnostics: %v", err)
	}
	after, err := os.ReadFile(path) // #nosec G304 -- Owned temporary credential fixture.
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("failed refresh changed credential bytes")
	}
	if token, err = AccessToken(t.Context(), path, "https://user:PRIVATE@api.example", "", ""); err == nil || token != "" || strings.Contains(safeerr.Message(err), "PRIVATE") {
		t.Fatal("invalid API origin accepted or disclosed credentials")
	}
}

func TestLoginInvalidOptionsAreRejectedBeforeLocalOrRemoteEffects(t *testing.T) {
	for _, change := range []func(*Options){
		func(o *Options) { o.ClientID = "" },
		func(o *Options) { o.APIOrigin = "https://api.invalid/path" },
		func(o *Options) { o.Scopes = []string{"read", "read"} },
		func(o *Options) { o.Scopes = []string{"é"} },
	} {
		dir := privateDirectory(t)
		o := Options{Issuer: "https://issuer.invalid/realm", ClientID: "cli", APIOrigin: "https://api.invalid", File: filepath.Join(dir, "login"), Listen: "127.0.0.1:0", Timeout: time.Minute}
		change(&o)
		if err := Login(t.Context(), o, io.Discard); err == nil {
			t.Fatal("invalid login input reached the credential or issuer boundary")
		}
		entries, err := os.ReadDir(dir)
		if err != nil || len(entries) != 0 {
			t.Fatal("invalid login created local state")
		}
	}
}

func TestCancelledHumanAdmissionDoesNotStartAnAPIExchange(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	value, err := acceptHuman(ctx, Options{APIOrigin: "https://api.invalid"}, "unit-access", "human", nil)
	if err == nil || safeerr.Message(err) != "auth: cannot configure Catalog connection" || value != (credentials{}) {
		t.Fatalf("cancelled admission returned credentials or changed safe classification: %v", err)
	}
}

func TestCancelledCredentialOperationsPreserveTheSelectedFile(t *testing.T) {
	path := filepath.Join(privateDirectory(t), "login")
	c := storedControl()
	writeControl(t, path, c)
	before, err := os.ReadFile(path) // #nosec G304 -- Owned temporary credential fixture.
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if token, err := AccessToken(ctx, path, c.APIOrigin, "", ""); err == nil || token != "" {
		t.Fatal("cancelled credential read disclosed a token")
	}
	if status, err := Inspect(ctx, path); err == nil || status != (Status{}) {
		t.Fatal("cancelled status disclosed a credential view")
	}
	if err := Logout(ctx, path); err == nil {
		t.Fatal("cancelled logout reported successful removal")
	}
	after, err := os.ReadFile(path) // #nosec G304 -- Owned temporary credential fixture.
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("cancelled credential operation changed or deleted the file")
	}
}

func TestCredentialStatusAndLogoutRefuseUnsafeEvidence(t *testing.T) {
	path := filepath.Join(privateDirectory(t), "login")
	invalid := []byte("PRIVATE_INVALID_CREDENTIALS")
	if err := os.WriteFile(path, invalid, 0600); err != nil {
		t.Fatal(err)
	}
	if status, err := Inspect(t.Context(), path); err == nil || status != (Status{}) || strings.Contains(safeerr.Message(err), "PRIVATE") {
		t.Fatal("corrupt credentials produced a successful or unsafe status")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	writeControl(t, path, storedControl())
	before, err := os.ReadFile(path) // #nosec G304 -- Owned temporary credential fixture.
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0644); err != nil { // #nosec G302 -- Unsafe permissions are the refusal fixture.
		t.Fatal(err)
	}
	if err := Logout(t.Context(), path); err == nil {
		t.Fatal("logout accepted an unsafe credential file")
	}
	after, err := os.ReadFile(path) // #nosec G304 -- Owned temporary credential fixture.
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("unsafe-file refusal removed or changed stored evidence")
	}
}

func TestCredentialStatusRejectsInvalidFileSelections(t *testing.T) {
	for _, path := range []string{"", string(filepath.Separator), strings.Repeat("PRIVATE_PATH", 400)} {
		if status, err := Inspect(t.Context(), path); err == nil || status != (Status{}) || strings.Contains(safeerr.Message(err), "PRIVATE_PATH") {
			t.Fatal("invalid credential selection was accepted or disclosed")
		}
	}
}
