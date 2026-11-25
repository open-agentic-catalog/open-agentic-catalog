//go:build ignore

// The runner builds this consumer against the shipped Go SDK module.
package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
	"time"

	catalog "github.com/open-agentic-catalog/open-agentic-catalog/sdk/go"
)

const reporter = "spiffe://workloads.test/reporter"

func token(ctx context.Context) (string, error) {
	raw, err := exec.CommandContext(ctx, "/opt/spire-agent", "api", "fetch", "jwt", "-socketPath", "/workload/api.sock", "-spiffeID", reporter, "-audience", "open-agentic-catalog", "-output", "json").Output()
	if err != nil {
		return "", errors.New("Workload API credential unavailable")
	}
	var parts []struct {
		SVIDs []struct {
			ID    string `json:"spiffe_id"`
			Token string `json:"svid"`
		} `json:"svids"`
	}
	if json.Unmarshal(raw, &parts) != nil || len(parts) != 2 || len(parts[0].SVIDs) != 1 || parts[0].SVIDs[0].ID != reporter || parts[0].SVIDs[0].Token == "" {
		return "", errors.New("Workload API returned no reporter credential")
	}
	return parts[0].SVIDs[0].Token, nil
}

// The reader's private tmpfs exists only for this container invocation.
func cliCapabilities(ctx context.Context) error {
	value, err := token(ctx)
	if err != nil {
		return err
	}
	const path = "/run/reporter/catalog.jwt"
	if err := os.WriteFile(path, []byte(value), 0600); err != nil {
		return err
	}
	defer os.Remove(path)
	cmd := exec.CommandContext(ctx, "/usr/local/bin/catalogctl", "remote", "capabilities", "--base-url", "https://catalog-a.test:8443", "--token-file", path, "--ca-file", "/trust/ca.pem")
	cmd.Stdout = os.Stdout
	return cmd.Run()
}

// Check exactly the metadata and public signing key advertised on the Catalog network.
func issuerKey(ctx context.Context, pool *x509.CertPool, kid string) error {
	const issuer = "https://identity.test:8447"
	client := http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: pool}}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	get := func(url string, dst any) error {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return err
		}
		res, err := client.Do(req)
		if err != nil {
			return err
		}
		defer res.Body.Close()
		if res.StatusCode != http.StatusOK {
			return errors.New("issuer endpoint unavailable")
		}
		return json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(dst)
	}
	var metadata struct {
		Issuer string
		JWKS   string `json:"jwks_uri"`
	}
	if get(issuer+"/.well-known/openid-configuration", &metadata) != nil || metadata.Issuer != issuer || metadata.JWKS != issuer+"/keys" {
		return errors.New("unexpected issuer discovery")
	}
	var set struct {
		Keys []struct{ Kid, Kty, Crv, X, Y string }
	}
	if err := get(metadata.JWKS, &set); err != nil {
		return err
	}
	for _, k := range set.Keys {
		if k.Kid == kid && k.Kty == "EC" && k.Crv == "P-256" && k.X != "" && k.Y != "" {
			return nil
		}
	}
	return errors.New("signing key not advertised")
}

func read() error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if len(os.Args) == 2 && os.Args[1] == "--cli-capabilities" {
		return cliCapabilities(ctx)
	}
	ca, err := os.ReadFile("/trust/ca.pem")
	pool := x509.NewCertPool()
	if err != nil || !pool.AppendCertsFromPEM(ca) {
		return errors.New("fixture CA unavailable")
	}
	if len(os.Args) == 3 && os.Args[1] == "--issuer-key" {
		return issuerKey(ctx, pool, os.Args[2])
	}
	if len(os.Args) != 4 {
		return errors.New("expected resource ID, private identifier and published identifier")
	}
	var id catalog.ResourceId
	if err := id.UnmarshalText([]byte(os.Args[1])); err != nil {
		return err
	}
	client, err := catalog.New(catalog.Config{Origin: "https://catalog-a.test:8443", RootCAs: pool, TokenProvider: token})
	if err != nil {
		return err
	}
	res, err := client.GetResourceWithResponse(ctx, id)
	if err != nil || res.JSON200 == nil || res.JSON200.Id != id || res.JSON200.CanonicalIdentifier != os.Args[2] {
		return errors.New("SDK private resource read failed")
	}
	manifest, err := client.GetARDManifestWithResponse(ctx, nil)
	if err != nil || manifest.JSON200 == nil {
		return errors.New("SDK authenticated manifest read failed")
	}
	found := false
	for _, entry := range manifest.JSON200.Entries {
		if entry.Identifier == os.Args[2] {
			return errors.New("private resource disclosed in manifest")
		}
		if entry.Identifier == os.Args[3] && entry.Version != nil && *entry.Version == "1.0.0" {
			found = true
		}
	}
	if !found {
		return errors.New("expected release missing from manifest")
	}
	return nil
}

func main() {
	// The Kubernetes fixture execs this consumer under the container's registered UID.
	if len(os.Args) == 2 && os.Args[1] == "--wait" {
		ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, os.Interrupt)
		defer stop()
		<-ctx.Done()
		return
	}
	if err := read(); err != nil {
		_, _ = os.Stderr.WriteString(err.Error() + "\n")
		os.Exit(1)
	}
}
