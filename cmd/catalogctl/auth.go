package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/cliauth"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/safeerr"
)

const authUsage = `usage: catalogctl auth <login|status|logout> --file PATH [flags]
  --file PATH           selected credentials in an existing owned 0700 directory
  --timeout DURATION    whole operation deadline, default 5m, maximum 10m
Login only:
  --issuer URL          exact OIDC issuer; HTTPS except literal loopback IPs
  --client-id ID        separately registered public native CLI client
  --base-url URL        Catalog API origin (or CATALOG_REMOTE_BASE_URL)
  --issuer-ca-file PATH  additional issuer roots; independent from API trust
  --ca-file PATH        additional API roots (or CATALOG_REMOTE_CA_FILE)
  --listen IP:PORT      literal loopback callback, default 127.0.0.1:0
  --scope VALUE         repeat for additional scopes; openid is automatic
Register http://127.0.0.1:PORT/oauth/callback (or [::1]) with the issuer.
Open the printed authorization URL manually on this machine. S256 PKCE; no secret.
The issuer must issue an access token for the Catalog API audience. Scope
selection alone does not configure that audience. Catalog must accept the
same human subject through /api/v1/me. ID tokens are never API credentials.
Status is local only. Logout removes the file, not issuer sessions or grants.
Remote commands require explicit --login-file PATH; no implicit token cache.
`

func authCommand(args []string) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return runAuth(ctx, args, os.Getenv, os.Stdout, os.Stderr)
}

func runAuth(ctx context.Context, args []string, getenv func(string) string, output, diagnostics io.Writer) error {
	if len(args) == 0 || args[0] == "help" || args[0] == "-h" || args[0] == "--help" {
		return writeImportOutput(output, []byte(authUsage))
	}
	if args[0] != "login" && args[0] != "status" && args[0] != "logout" {
		return safeerr.New("auth: unknown command; use catalogctl auth --help")
	}
	var o cliauth.Options
	f := flag.NewFlagSet("auth", flag.ContinueOnError)
	f.SetOutput(io.Discard)
	f.StringVar(&o.File, "file", "", "private credential file")
	f.DurationVar(&o.Timeout, "timeout", 5*time.Minute, "whole operation deadline")
	if args[0] == "login" {
		f.StringVar(&o.Issuer, "issuer", "", "OIDC issuer")
		f.StringVar(&o.ClientID, "client-id", "", "public CLI client")
		f.StringVar(&o.APIOrigin, "base-url", getenv("CATALOG_REMOTE_BASE_URL"), "API origin")
		f.StringVar(&o.APICAFile, "ca-file", getenv("CATALOG_REMOTE_CA_FILE"), "API roots")
		f.StringVar(&o.IssuerCAFile, "issuer-ca-file", "", "issuer roots")
		f.StringVar(&o.Listen, "listen", "127.0.0.1:0", "loopback callback address")
		f.Func("scope", "additional OAuth scope", func(value string) error { o.Scopes = append(o.Scopes, value); return nil })
	}
	if err := f.Parse(args[1:]); errors.Is(err, flag.ErrHelp) {
		return writeImportOutput(output, []byte(authUsage))
	} else if err != nil {
		return safeerr.New("auth: invalid flags; use --help")
	}
	if f.NArg() != 0 || o.File == "" || o.Timeout <= 0 || o.Timeout > 10*time.Minute {
		return safeerr.New("auth: requires --file and a timeout greater than zero and at most 10m")
	}
	ctx, cancel := context.WithTimeout(ctx, o.Timeout)
	defer cancel()
	if args[0] == "login" {
		if err := cliauth.Login(ctx, o, diagnostics); err != nil {
			return err
		}
	}
	if args[0] == "logout" {
		if err := cliauth.Logout(ctx, o.File); err != nil {
			return err
		}
	}
	status, err := cliauth.Inspect(ctx, o.File)
	if err != nil {
		return err
	}
	if err := json.NewEncoder(output).Encode(status); err != nil {
		return safeerr.New("auth: cannot write local credential status")
	}
	return nil
}
