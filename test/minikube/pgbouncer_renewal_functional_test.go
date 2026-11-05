//go:build minikube && functional

package main

import (
	"context"
	"crypto/tls"
	"encoding/binary"
	"io"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgproto3"
)

// Real PostgreSQL SSL negotiation verifies hostname/CA/leaf rejection. This
// transport fixture does not replace the selected native SQL/cluster tests.
func TestDatabaseForwardRequiresExactVerifiedLeaf(t *testing.T) {
	material, err := databaseTLSMaterial(time.Now())
	if err != nil {
		t.Fatal(err)
	}
	other, err := databaseTLSMaterial(time.Now())
	if err != nil {
		t.Fatal(err)
	}
	pair, err := tls.X509KeyPair([]byte(material["catalog-pgbouncer.crt"]), []byte(material["catalog-pgbouncer.key"]))
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name, trust, leaf, serverName string
		valid                         bool
	}{
		{"valid", material["ca.pem"], material["catalog-pgbouncer.crt"], "catalog-pgbouncer", true},
		{"wrong CA", other["ca.pem"], material["catalog-pgbouncer.crt"], "catalog-pgbouncer", false},
		{"wrong leaf", material["ca.pem"], material["postgres.crt"], "catalog-pgbouncer", false},
		{"wrong hostname", material["ca.pem"], material["catalog-pgbouncer.crt"], "unrelated", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			listener, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = listener.Close() }()
			finished := make(chan error, 1)
			go func() {
				socket, err := listener.Accept()
				if err != nil {
					finished <- err
					return
				}
				defer func() { _ = socket.Close() }()
				if err := socket.SetDeadline(time.Now().Add(3 * time.Second)); err != nil {
					finished <- err
					return
				}
				var request [8]byte
				if _, err := io.ReadFull(socket, request[:]); err != nil {
					finished <- err
					return
				}
				if binary.BigEndian.Uint32(request[:4]) != 8 || binary.BigEndian.Uint32(request[4:]) != 80877103 {
					finished <- io.ErrUnexpectedEOF
					return
				}
				if _, err := socket.Write([]byte("S")); err != nil {
					finished <- err
					return
				}
				encrypted := tls.Server(socket, &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{pair}})
				if err := encrypted.HandshakeContext(t.Context()); err != nil {
					finished <- err
					return
				}
				backend := pgproto3.NewBackend(encrypted, encrypted)
				if _, err := backend.ReceiveStartupMessage(); err != nil {
					finished <- err
					return
				}
				backend.Send(&pgproto3.AuthenticationOk{})
				backend.Send(&pgproto3.ReadyForQuery{TxStatus: 'I'})
				if err := backend.Flush(); err != nil {
					finished <- err
					return
				}
				_, err = backend.Receive()
				finished <- err
			}()
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			conn, err := databaseConnection(ctx, listener.Addr().String(), test.serverName, "catalog_api", "synthetic", test.trust, test.leaf)
			if conn != nil {
				_ = conn.Close(ctx)
			}
			if (err == nil) != test.valid {
				t.Fatalf("verified connection accepted=%v: %v", test.valid, err)
			}
			select {
			case serverErr := <-finished:
				if test.valid && serverErr != nil {
					t.Fatal(serverErr)
				}
			case <-ctx.Done():
				t.Fatal("TLS transport did not finish")
			}
		})
	}
}

func TestDatabaseForwardRejectsAmbientAndInvalidInputs(t *testing.T) {
	for _, address := range []string{"db.example:5432", "0.0.0.0:5432", "127.0.0.1:0", "127.0.0.1:65536", "invalid"} {
		if conn, err := databaseConnection(t.Context(), address, "postgres", "catalog_owner", "synthetic", "invalid", "invalid"); err == nil || conn != nil {
			t.Fatal("accepted non-owned or invalid database forward")
		}
	}
}

// A diagnostic container makes kubectl emit a default-container notice unless
// the SQL observer selects postgres explicitly. Reject that contaminated output.
func TestDatabaseOwnerSQLSelectsServerContainer(t *testing.T) {
	directory := t.TempDir()
	tool := filepath.Join(directory, "kubectl")
	script := `#!/bin/sh
case " $* " in
 *" exec postgres-0 -c postgres -- psql "*) printf '2|3|0\n' ;;
 *) printf 'Defaulted container postgres out of postgres, network-probe\n2|3|0\n' ;;
esac
`
	if err := os.WriteFile(tool, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	} // #nosec G306 -- Private owned executable fixture.
	h := &harness{ctx: t.Context(), options: options{kubectl: tool, repositoryRoot: directory}, kubeconfig: filepath.Join(directory, "kubeconfig"), profile: "owned-database-fixture"}
	raw, err := h.databaseOwnerSQL("SELECT fixture")
	if err != nil {
		t.Fatal(err)
	}
	if err := validatePooledBackendCounts(raw); err != nil {
		t.Fatal(err)
	}
}
