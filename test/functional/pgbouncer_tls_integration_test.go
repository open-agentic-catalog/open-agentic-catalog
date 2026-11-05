//go:build integration

package functional_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// These assertions run only inside the owned real PostgreSQL/PgBouncer gate.
// They do not change the active writer's certificates or claim renewal coverage.
func qualifyPoolerTLSIdentities(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 45*time.Second)
	defer cancel()
	directory := os.Getenv("CATALOG_TEST_PGBOUNCER_TLS_DIRECTORY")
	if directory == "" {
		t.Fatal("missing owned TLS fixture directory")
	}
	read := func(t *testing.T, name string) *x509.Certificate {
		t.Helper()
		raw, err := os.ReadFile(filepath.Join(directory, name+".pem")) // #nosec G304,G703 -- fixed names under the explicitly selected private fixture directory.
		if err != nil {
			t.Fatal("read owned public certificate")
		}
		block, rest := pem.Decode(raw)
		if block == nil || block.Type != "CERTIFICATE" || len(bytes.TrimSpace(rest)) != 0 {
			t.Fatal("expected one public certificate")
		}
		certificate, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			t.Fatal("parse owned public certificate")
		}
		return certificate
	}
	oldCA, nextCA := read(t, "ca"), read(t, "ca-next")
	if !oldCA.IsCA || !nextCA.IsCA || bytes.Equal(oldCA.RawSubjectPublicKeyInfo, nextCA.RawSubjectPublicKeyInfo) {
		t.Fatal("predecessor and successor require independent CA keys")
	}
	oldRoots, nextRoots := x509.NewCertPool(), x509.NewCertPool()
	oldRoots.AddCert(oldCA)
	nextRoots.AddCert(nextCA)
	now := time.Now()
	for _, role := range []string{"postgres", "pooler"} {
		previous := read(t, role)
		for _, variant := range []string{"", "-renewed", "-next", "-expired", "-future"} {
			name := role + variant
			certificate := read(t, name)
			// Valid key pairs, issuer, purpose and SAN isolate the date failure.
			if _, err := tls.LoadX509KeyPair(filepath.Join(directory, name+".pem"), filepath.Join(directory, name+".key")); err != nil {
				t.Fatal("invalid specimen key pair")
			}
			issuer, roots, other := oldCA, oldRoots, nextRoots
			if variant == "-next" {
				issuer, roots, other = nextCA, nextRoots, oldRoots
			}
			if (variant == "-next" || variant == "-renewed") && bytes.Equal(previous.RawSubjectPublicKeyInfo, certificate.RawSubjectPublicKeyInfo) {
				t.Fatal("replacement leaf must use a different key")
			}
			if variant == "-renewed" && !certificate.NotAfter.After(previous.NotAfter) {
				t.Fatal("same-CA renewal must extend the leaf's validity")
			}
			if err := certificate.CheckSignatureFrom(issuer); err != nil || len(certificate.ExtKeyUsage) != 1 || certificate.ExtKeyUsage[0] != x509.ExtKeyUsageServerAuth {
				t.Fatal("specimen issuer or server purpose differs")
			}
			for _, hostname := range []string{role, "127.0.0.1"} {
				if err := certificate.VerifyHostname(hostname); err != nil {
					t.Fatal("specimen SAN does not match its endpoint")
				}
			}
			if role == "postgres" {
				for _, hostname := range []string{"standby", "writer", "expired-postgres", "future-postgres"} {
					if err := certificate.VerifyHostname(hostname); err != nil {
						t.Fatal("database SAN does not match a selected fixture endpoint")
					}
				}
			}
			options := x509.VerifyOptions{Roots: roots, DNSName: "127.0.0.1", CurrentTime: now}
			_, err := certificate.Verify(options)
			switch variant {
			case "-expired", "-future":
				var invalid x509.CertificateInvalidError
				if !errors.As(err, &invalid) || invalid.Reason != x509.Expired {
					t.Fatal("date specimen did not isolate X.509 validity")
				}
				if variant == "-expired" && !now.After(certificate.NotAfter) || variant == "-future" && !now.Before(certificate.NotBefore) {
					t.Fatal("date specimen does not have its declared validity")
				}
			default:
				if err != nil {
					t.Fatal("valid neighbor failed native chain verification")
				}
				options.Roots = other
				var unknown x509.UnknownAuthorityError
				if _, err := certificate.Verify(options); !errors.As(err, &unknown) {
					t.Fatal("independent authority unexpectedly accepts the other chain")
				}
			}
			t.Logf("public TLS specimen %s sha256:%x serial=%s notBefore=%s notAfter=%s", name, sha256.Sum256(certificate.Raw), certificate.SerialNumber, certificate.NotBefore.Format(time.RFC3339), certificate.NotAfter.Format(time.RFC3339))
		}
	}
	for _, variant := range []string{"expired", "future"} {
		t.Run(variant, func(t *testing.T) {
			qualifyPoolerTLSNeighbor(t, ctx, read(t, "pooler"))
			for _, endpoint := range []struct {
				environment string
				certificate *x509.Certificate
			}{
				{"CATALOG_TEST_PGBOUNCER_" + strings.ToUpper(variant) + "_DSN", read(t, "pooler-"+variant)},
				{"CATALOG_TEST_PGBOUNCER_" + strings.ToUpper(variant) + "_POSTGRES_DSN", read(t, "postgres-"+variant)},
			} {
				cfg := poolerTLSConfig(t, os.Getenv(endpoint.environment), "catalog_api")
				connection, err := pgx.ConnectConfig(ctx, cfg)
				if connection != nil {
					_ = connection.Close(ctx)
				}
				var invalid x509.CertificateInvalidError
				if !errors.As(err, &invalid) || invalid.Reason != x509.Expired || invalid.Cert == nil || !bytes.Equal(invalid.Cert.Raw, endpoint.certificate.Raw) {
					t.Fatal("fresh connection did not reject the exact server leaf at its X.509 date guard")
				}
			}
			// The pooler validates the second hop independently. Its frontend
			// remains valid; a bounded queue error alone is insufficient evidence.
			started := time.Now()
			base, err := url.Parse(os.Getenv("CATALOG_TEST_PGBOUNCER_DSN"))
			if err != nil {
				t.Fatal("parse owned pooler endpoint")
			}
			base.Path = "/" + variant
			cfg := poolerTLSConfig(t, base.String(), "catalog_api")
			probe, stop := context.WithTimeout(ctx, 5*time.Second)
			defer stop()
			connection, err := pgx.ConnectConfig(probe, cfg)
			if err == nil {
				_, err = connection.Exec(probe, "SELECT 1")
				_ = connection.Close(ctx)
			}
			var refused *pgconn.PgError
			if !errors.As(err, &refused) || refused.Message != "query_wait_timeout" || probe.Err() != nil {
				observed := time.Now()
				adjustment := observed.UnixMilli() - started.UnixMilli() - observed.Sub(started).Milliseconds()
				t.Fatalf("upstream validity refusal: queueError=%t deadline=%t clockAdjustmentMillis=%d", refused != nil && refused.Message == "query_wait_timeout", probe.Err() != nil, adjustment)
			}
			logs := poolerLogsSince(t, ctx, "pooler", started.UTC().Format(time.RFC3339Nano))
			if !strings.Contains(logs, "TLS handshake error") || !strings.Contains(logs, "certificate verify failed") {
				t.Fatal("upstream validity refusal lacks fresh native TLS verification evidence")
			}
			qualifyPoolerTLSNeighbor(t, ctx, read(t, "pooler"))
		})
	}
}

func poolerTLSConfig(t *testing.T, dsn, role string) *pgx.ConnConfig {
	t.Helper()
	value, err := url.Parse(dsn)
	if err != nil || value.Scheme != "postgres" || value.Hostname() != "127.0.0.1" || value.Port() == "" {
		t.Fatal("missing owned loopback TLS endpoint")
	}
	if role != "" {
		value.User = url.UserPassword(role, strings.Replace(role, "_", "-", 1))
	}
	cfg, err := pgx.ParseConfig(value.String())
	if err != nil || cfg.TLSConfig == nil || cfg.TLSConfig.InsecureSkipVerify {
		t.Fatal("fixture requires verified TLS")
	}
	cfg.Fallbacks = nil
	return cfg
}

func qualifyPoolerTLSNeighbor(t *testing.T, ctx context.Context, expected *x509.Certificate) {
	t.Helper()
	for _, role := range []string{"catalog_api", "catalog_worker", ""} {
		dsn := os.Getenv("CATALOG_TEST_PGBOUNCER_DSN")
		if role == "" {
			dsn = os.Getenv("CATALOG_TEST_PGBOUNCER_PROMOTED_OWNER_DSN")
		}
		cfg := poolerTLSConfig(t, dsn, role)
		if role != "" {
			cfg.TLSConfig.VerifyConnection = func(state tls.ConnectionState) error {
				if len(state.VerifiedChains) == 0 || len(state.PeerCertificates) == 0 || !bytes.Equal(state.PeerCertificates[0].Raw, expected.Raw) {
					return errors.New("unexpected verified pooler identity")
				}
				return nil
			}
		}
		connection, err := pgx.ConnectConfig(ctx, cfg)
		if err != nil {
			t.Fatal("valid TLS neighbor failed to connect")
		}
		var actual string
		var protected bool
		err = connection.QueryRow(ctx, "SELECT current_user, ssl FROM pg_stat_ssl WHERE pid=pg_backend_pid()").Scan(&actual, &protected)
		mode := connection.PgConn().ParameterStatus("pgbouncer.pool_mode")
		_ = connection.Close(ctx)
		if err != nil || actual != cfg.User || !protected || role != "" && mode != "session" || role == "" && mode != "" {
			t.Fatal("valid neighbor lost exact identity, TLS or direct-owner/session routing")
		}
	}
}
