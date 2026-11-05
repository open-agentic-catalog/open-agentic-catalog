//go:build integration

// Credential rotation uses owned exact roles and fresh wire connections so an
// authenticated pool cannot hide rejection of a retired PostgreSQL password.
package database

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestRuntimePasswordRotationRequiresNewConnections(t *testing.T) {
	db := newHousekeepingDatabase(t)
	for _, role := range []struct {
		name string
		pool *pgxpool.Pool
	}{{RuntimeRoleAPI, db.api}, {RuntimeRoleWorker, db.worker}} {
		t.Run(role.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
			defer cancel()
			old := role.pool.Config().ConnConfig.Copy()
			retained, err := pgx.ConnectConfig(ctx, old.Copy())
			if err != nil {
				t.Fatal("initial password could not establish a new connection")
			}
			closeConnection := func(connection *pgx.Conn) {
				cleanup, stop := context.WithTimeout(context.Background(), 5*time.Second)
				defer stop()
				if err := connection.Close(cleanup); err != nil {
					t.Error("owned connection cleanup failed")
				}
			}
			defer closeConnection(retained)
			// This generated fixture password and role are confined to the schema
			// helper's disposable identities; cleanup drops both roles.
			fresh := old.Copy()
			fresh.Password = "replacement-" + role.name + "-" + old.User
			if _, err := db.operator.Exec(ctx, "ALTER ROLE "+pgx.Identifier{old.User}.Sanitize()+" PASSWORD '"+fresh.Password+"'"); err != nil {
				t.Fatal("owned password replacement failed")
			}
			if err := retained.Ping(ctx); err != nil {
				t.Fatal("password replacement unexpectedly revoked an authenticated session")
			}
			if rejected, err := pgx.ConnectConfig(ctx, old.Copy()); err == nil {
				_ = rejected.Close(ctx)
				t.Fatal("retired password authenticated a genuinely new connection")
			} else {
				var failure *pgconn.PgError
				if !errors.As(err, &failure) || failure.Code != "28P01" {
					t.Fatal("old connection failed for a reason other than password rejection")
				}
			}
			accepted, err := pgx.ConnectConfig(ctx, fresh)
			if err != nil {
				t.Fatal("replacement password could not establish a new connection")
			}
			defer closeConnection(accepted)
			var current, session string
			if err := accepted.QueryRow(ctx, "SELECT current_user,session_user").Scan(&current, &session); err != nil || current != old.User || session != old.User {
				t.Fatal("replacement connection changed direct database identity")
			}
			poolConfig := role.pool.Config()
			poolConfig.ConnConfig.Password = fresh.Password
			replaced, err := pgxpool.NewWithConfig(ctx, poolConfig)
			if err != nil {
				t.Fatal("replacement pool could not be constructed")
			}
			defer replaced.Close()
			if _, err := VerifyRuntimeRole(ctx, replaced, role.name); err != nil {
				t.Fatal("replacement password failed exact-role admission")
			}
		})
	}
}
