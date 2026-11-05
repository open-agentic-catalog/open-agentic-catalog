//go:build integration

package functional_test

import (
	"context"
	"errors"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/database"
)

// TestPgBouncer deliberately selects a fixture rather than globally replacing
// integration DSNs: dynamic repository roles and owner operations remain direct.
func TestPgBouncer(t *testing.T) {
	if os.Getenv("CATALOG_TEST_PGBOUNCER_DSN") == "" {
		t.Skip("selected by make test-pgbouncer")
	}
	if !t.Run("compiled-processes", TestPostgresProcesses) {
		t.Fatal("process control must pass before protocol faults")
	}
	if !t.Run("tls-identities", qualifyPoolerTLSIdentities) {
		t.Fatal("TLS identity controls must pass before protocol faults")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	base, err := url.Parse(os.Getenv("CATALOG_TEST_PGBOUNCER_DSN"))
	if err != nil || base.Scheme != "postgres" || base.Hostname() != "127.0.0.1" || base.Path != "/catalog_test" {
		t.Fatal("requires owned loopback catalog_test pooler")
	}
	dsn := func(role, databaseName string) string {
		value := *base
		value.User = url.UserPassword("catalog_"+role, "catalog-"+role)
		value.Path = "/" + databaseName
		return value.String()
	}
	settings := database.PoolConfig{MaxConnections: 2, MaxConnLifetime: time.Minute, MaxConnIdleTime: time.Minute, HealthCheckPeriod: time.Second}
	connect := func(t *testing.T, connectionDSN string) *pgx.Conn {
		t.Helper()
		connection, err := pgx.Connect(ctx, connectionDSN)
		if err != nil {
			t.Fatal("owned protocol connection failed")
		}
		t.Cleanup(func() { _ = connection.Close(context.WithoutCancel(ctx)) })
		return connection
	}
	t.Run("runtime-admission", func(t *testing.T) {
		for _, role := range []string{"api", "worker"} {
			pool, err := database.OpenRuntimeObserved(ctx, dsn(role, "catalog_test")+"&search_path=public", settings, nil)
			if err != nil {
				t.Fatal("session runtime admission failed")
			}
			defer pool.Close()
			if _, err := database.VerifyRuntimeRole(ctx, pool, role); err != nil {
				t.Fatal("exact role/grant admission failed")
			}
			var zone, path string
			if err := pool.QueryRow(ctx, "SELECT current_setting('TimeZone'),current_setting('search_path')").Scan(&zone, &path); err != nil || zone != "UTC" || path != "public" {
				t.Fatal("runtime session parameters lost")
			}
			for i := 0; i < 3; i++ {
				var answer int
				if err := pool.QueryRow(ctx, "SELECT $1::integer + 1", i).Scan(&answer); err != nil || answer != i+1 {
					t.Fatal("cached prepared statement failed")
				}
			}
		}
		for _, mode := range []string{"transaction", "statement"} {
			pool, err := database.OpenRuntimeObserved(ctx, dsn("api", mode), settings, nil)
			if pool != nil || err == nil || !strings.Contains(err.Error(), "PostgreSQL runtime requires session pooling") {
				t.Fatal("non-session mode must fail before runtime admission")
			}
			control := connect(t, dsn("api", mode))
			if control.PgConn().ParameterStatus("pgbouncer.pool_mode") != mode {
				t.Fatal("negative control did not advertise effective mode")
			}
			_ = control.Close(ctx)
		}
		_, err := pgx.Connect(ctx, dsn("worker", "catalog_test")+"&statement_timeout=17s")
		var pgError *pgconn.PgError
		if !errors.As(err, &pgError) || pgError.Code != "08P01" || pgError.Message != "unsupported startup parameter: statement_timeout" {
			t.Fatal("unsupported startup timeout must be refused visibly")
		}
	})
	t.Run("session-lock-and-transaction-context", func(t *testing.T) {
		first, second := connect(t, dsn("api", "catalog_test")), connect(t, dsn("api", "catalog_test"))
		defer func() { _ = first.Close(ctx); _ = second.Close(ctx) }()
		lock := func(connection *pgx.Conn, want bool) {
			t.Helper()
			var acquired bool
			if err := connection.QueryRow(ctx, "SELECT pg_try_advisory_lock(761239)").Scan(&acquired); err != nil || acquired != want {
				t.Fatal("session advisory lock exclusion failed")
			}
		}
		lock(first, true)
		for _, tenant := range []string{"tenant-one", "tenant-two"} {
			tx, err := first.Begin(ctx)
			if err != nil {
				t.Fatal("begin tenant control")
			}
			var actual string
			if err := tx.QueryRow(ctx, "SELECT set_config('app.tenant_id',$1,true)", tenant).Scan(&actual); err != nil || actual != tenant {
				t.Fatal("tenant context not installed")
			}
			if err := tx.Rollback(ctx); err != nil {
				t.Fatal("rollback tenant context")
			}
			if err := first.QueryRow(ctx, "SELECT current_setting('app.tenant_id',true)").Scan(&actual); err != nil || actual != "" {
				t.Fatal("transaction-local tenant leaked after rollback")
			}
			lock(second, false)
		}
		if _, err := first.Exec(ctx, "BEGIN; SELECT 1; COMMIT"); err != nil {
			t.Fatal("short command transactions failed")
		}
		lock(second, false)
		var unlocked bool
		if err := first.QueryRow(ctx, "SELECT pg_advisory_unlock(761239)").Scan(&unlocked); err != nil || !unlocked {
			t.Fatal("owner failed to release session lock")
		}
		lock(second, true)
		if _, err := second.Exec(ctx, "SELECT pg_advisory_unlock(761239)"); err != nil {
			t.Fatal("release second lock")
		}
	})
	t.Run("reset-capacity-and-cancellation", func(t *testing.T) {
		first := connect(t, dsn("api", "capacity"))
		var before int
		if err := first.QueryRow(ctx, "SELECT pg_backend_pid()").Scan(&before); err != nil {
			t.Fatal("backend control")
		}
		if _, err := first.Exec(ctx, "SET app.private_probe='private'; SELECT pg_advisory_lock(761240)"); err != nil {
			t.Fatal("set private session control")
		}
		worker := connect(t, dsn("worker", "capacity"))
		workerCtx, stopWorker := context.WithTimeout(ctx, time.Second)
		if err := worker.Ping(workerCtx); err != nil {
			t.Fatal("API capacity exhaustion starved the worker role")
		}
		stopWorker()
		_ = worker.Close(ctx)
		// A connected client can receive the cached startup greeting while no
		// backend is free; query execution must enforce the pooler queue limit.
		queued := connect(t, dsn("api", "capacity"))
		queueCtx, stopQueue := context.WithTimeout(ctx, 5*time.Second)
		_, queueError := queued.Exec(queueCtx, "SELECT 1")
		var queuePgError *pgconn.PgError
		if !errors.As(queueError, &queuePgError) || queuePgError.Message != "query_wait_timeout" || queueCtx.Err() != nil {
			t.Fatal("server-side capacity queue did not enforce its configured timeout")
		}
		stopQueue()
		_ = queued.Close(ctx)
		waitCtx, stop := context.WithTimeout(ctx, 200*time.Millisecond)
		defer stop()
		waiting := connect(t, dsn("api", "capacity"))
		_, err := waiting.Exec(waitCtx, "SELECT 1")
		_ = waiting.Close(ctx)
		if err == nil || waitCtx.Err() == nil {
			t.Fatal("capacity query wait was not deadline bounded")
		}
		// Close can return before pgx finishes canceling and draining. Keep
		// the holder until the timed-out client leaves the pooler's queue.
		// The driver bounds that background cleanup at 15 seconds. Observe it
		// separately from the unchanged caller and pooler queue deadlines.
		cleanupCtx, stopCleanup := context.WithTimeout(ctx, 20*time.Second)
		defer stopCleanup()
		select {
		case <-waiting.PgConn().CleanupDone():
		case <-cleanupCtx.Done():
			t.Fatal("timed-out capacity client did not finish cleanup")
		}
		// Refresh activity after the queue wait, which exceeds the fixture's
		// one-second backend idle timeout. Preserve the state that must reset.
		var heldPID int
		var heldSetting string
		if err := first.QueryRow(ctx, "SELECT pg_backend_pid(),current_setting('app.private_probe')").Scan(&heldPID, &heldSetting); err != nil || heldPID != before || heldSetting != "private" {
			t.Fatal("capacity holder lost its backend or private setting before reset")
		}
		// A failed unlock must fall back to closing the client. Observe the
		// physical backend reset below, rather than assuming close releases locks.
		unlockCtx, stopUnlock := context.WithCancel(ctx)
		stopUnlock()
		if _, err := first.Exec(unlockCtx, "SELECT pg_advisory_unlock(761240)"); err == nil {
			t.Fatal("canceled unlock unexpectedly succeeded")
		}
		if err := first.Close(ctx); err != nil {
			t.Fatal("close capacity holder")
		}
		second := connect(t, dsn("api", "capacity"))
		defer func() { _ = second.Close(ctx) }()
		var after int
		var setting string
		var acquired bool
		if err := second.QueryRow(ctx, "SELECT pg_backend_pid(),coalesce(current_setting('app.private_probe',true),''),pg_try_advisory_lock(761240)").Scan(&after, &setting, &acquired); err != nil || after != before || setting != "" || !acquired {
			for line := range strings.SplitSeq(poolerLogs(t, ctx, "pooler"), "\n") {
				if strings.Contains(line, "capacity/catalog_api") && strings.Contains(line, "closing because:") {
					t.Log(line)
				}
			}
			t.Fatalf("backend reset check failed: queryError=%t reusedBackend=%t settingCleared=%t lockAvailable=%t", err != nil, after == before, setting == "", acquired)
		}
		if _, err := second.Exec(ctx, "SELECT pg_advisory_unlock(761240)"); err != nil {
			t.Fatal("release reset control lock")
		}
		_ = second.Close(ctx)
		pool, err := database.OpenRuntimeObserved(ctx, dsn("api", "capacity"), settings, nil)
		if err != nil {
			t.Fatal("open cancellation runtime")
		}
		defer pool.Close()
		cancelCtx, stopQuery := context.WithTimeout(ctx, 100*time.Millisecond)
		defer stopQuery()
		tx, err := pool.Begin(ctx)
		if err != nil {
			t.Fatal("begin cancellation rollback control")
		}
		if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock(761241)"); err != nil {
			t.Fatal("acquire cancellation transaction lock")
		}
		_, err = tx.Exec(cancelCtx, "SELECT pg_sleep(30)")
		if err == nil || cancelCtx.Err() == nil {
			t.Fatal("executing query cancellation not observed")
		}
		_ = tx.Rollback(ctx)
		if err := pool.Ping(ctx); err != nil {
			t.Fatal("pool failed reuse after bounded cancel/fallback")
		}
		var released bool
		if err := pool.QueryRow(ctx, "SELECT pg_try_advisory_xact_lock(761241)").Scan(&released); err != nil || !released {
			t.Fatal("canceled transaction retained its backend lock")
		}
	})
	t.Run("authentication-and-client-trust", func(t *testing.T) {
		bad := *base
		for _, user := range []string{"catalog_api", "catalog", "missing"} {
			bad.User = url.UserPassword(user, "incorrect-secret-sentinel")
			connection, err := pgx.Connect(ctx, bad.String())
			if connection != nil {
				_ = connection.Close(ctx)
			}
			if err == nil {
				t.Fatal("unexpected credential/owner admission")
			}
		}
		ownerAddress, err := url.Parse(os.Getenv("CATALOG_TEST_PGBOUNCER_PROMOTED_OWNER_DSN"))
		if err != nil || ownerAddress.User == nil {
			t.Fatal("missing direct owner control")
		}
		bad.User = ownerAddress.User
		if connection, err := pgx.Connect(ctx, bad.String()); err == nil {
			_ = connection.Close(ctx)
			t.Fatal("correct schema-owner credentials admitted by pooler")
		}
		plain := *base
		plain.User = url.UserPassword("catalog_api", "catalog-api")
		plainQuery := plain.Query()
		plainQuery.Set("sslmode", "disable")
		plainQuery.Del("sslrootcert")
		plain.RawQuery = plainQuery.Encode()
		if connection, err := pgx.Connect(ctx, plain.String()); err == nil {
			_ = connection.Close(ctx)
			t.Fatal("plaintext runtime connection admitted by TLS-only pooler")
		}
		bad.User = url.UserPassword("catalog_api", "catalog-api")
		query := bad.Query()
		query.Set("sslrootcert", "system")
		bad.RawQuery = query.Encode()
		if connection, err := pgx.Connect(ctx, bad.String()); err == nil {
			_ = connection.Close(ctx)
			t.Fatal("untrusted private CA admitted")
		}
		// A valid chain with the wrong DNS name must fail even at a loopback address.
		parsed, err := pgx.ParseConfig(dsn("api", "catalog_test"))
		if err != nil {
			t.Fatal("parse owned TLS config")
		}
		parsed.TLSConfig.ServerName = "unlisted.invalid"
		parsed.Fallbacks = nil
		if connection, err := pgx.ConnectConfig(ctx, parsed); err == nil {
			_ = connection.Close(ctx)
			t.Fatal("wrong pooler TLS hostname admitted")
		}
	})
	t.Run("upstream-trust-refusal", func(t *testing.T) {
		for _, candidate := range []struct{ label, address string }{
			{"upstream-hostname", dsn("api", "badhost")},
			{"upstream-ca", os.Getenv("CATALOG_TEST_PGBOUNCER_BAD_CA_DSN")},
		} {
			value, err := url.Parse(candidate.address)
			if err != nil || value.Hostname() != "127.0.0.1" {
				t.Fatal("invalid owned trust negative")
			}
			value.User = url.UserPassword("catalog_api", "catalog-api")
			stage := "connect"
			connection, err := pgx.Connect(ctx, value.String()+"&connect_timeout=5")
			if err == nil {
				stage = "query"
				_, err = connection.Exec(ctx, "SELECT 1")
				_ = connection.Close(ctx)
			}
			var pgError *pgconn.PgError
			postgresError := errors.As(err, &pgError)
			queueTimeout := postgresError && pgError.Message == "query_wait_timeout"
			contextActive := ctx.Err() == nil
			// Error text can contain credentials or connection strings. Keep only
			// fixed labels, predicate results and the Go error type.
			t.Logf("upstream trust candidate=%s stage=%s postgres_error=%t queue_timeout=%t context_active=%t driver_timeout=%t error_type=%T",
				candidate.label, stage, postgresError, queueTimeout, contextActive, pgconn.Timeout(err), err)
			if !postgresError || !queueTimeout || !contextActive {
				t.Fatal("upstream trust refusal did not reach the bounded pooler queue timeout")
			}
		}
		for _, service := range []string{"pooler", "bad-ca"} {
			logs := poolerLogs(t, ctx, service)
			if !strings.Contains(logs, "TLS handshake error") || !strings.Contains(logs, "certificate") {
				t.Fatal("upstream refusal lacked actual TLS certificate evidence")
			}
		}

	})

	t.Run("runtime-privilege-refusal", func(t *testing.T) {
		owner := connect(t, os.Getenv("CATALOG_TEST_PGBOUNCER_PROMOTED_OWNER_DSN"))
		if _, err := owner.Exec(ctx, "REVOKE SELECT (slug) ON namespaces FROM catalog_api"); err != nil {
			t.Fatal("install missing exact API privilege control")
		}
		t.Cleanup(func() {
			if _, err := owner.Exec(context.WithoutCancel(ctx), "GRANT SELECT (slug) ON namespaces TO catalog_api"); err != nil {
				t.Error("restore exact API privilege")
			}
		})
		pool, err := database.OpenRuntimeObserved(ctx, dsn("api", "catalog_test"), settings, nil)
		if err != nil {
			t.Fatal("missing-privilege control did not reach role verification")
		}
		defer pool.Close()
		if _, err := database.VerifyRuntimeRole(ctx, pool, "api"); err == nil {
			t.Fatal("pooled runtime accepted missing exact privileges")
		}
	})

	t.Run("role-timeout-defaults", func(t *testing.T) {
		// Owner authority stays on the direct promoted PostgreSQL listener. This
		// alias starts a fresh backend after setting explicit role defaults.
		owner := connect(t, os.Getenv("CATALOG_TEST_PGBOUNCER_PROMOTED_OWNER_DSN"))
		if _, err := owner.Exec(ctx, "ALTER ROLE catalog_api SET statement_timeout='2s'; ALTER ROLE catalog_api SET lock_timeout='1s'"); err != nil {
			t.Fatal("set direct-owner timeout controls")
		}
		t.Cleanup(func() {
			if _, err := owner.Exec(context.WithoutCancel(ctx), "ALTER ROLE catalog_api RESET statement_timeout; ALTER ROLE catalog_api RESET lock_timeout"); err != nil {
				t.Error("restore role timeout defaults")
			}
		})
		client := connect(t, dsn("api", "timeouts"))
		var statement, lock string
		if err := client.QueryRow(ctx, "SELECT current_setting('statement_timeout'),current_setting('lock_timeout')").Scan(&statement, &lock); err != nil || statement != "2s" || lock != "1s" {
			t.Fatal("pooled role defaults were lost")
		}
		_, err := client.Exec(ctx, "SELECT pg_sleep(3)")
		var pgError *pgconn.PgError
		if !errors.As(err, &pgError) || pgError.Code != "57014" || ctx.Err() != nil {
			t.Fatal("pooled PostgreSQL statement timeout was not enforced")
		}
		transaction, err := owner.Begin(ctx)
		if err != nil {
			t.Fatal("begin direct lock timeout control")
		}
		defer func() { _ = transaction.Rollback(ctx) }()
		if _, err := transaction.Exec(ctx, "LOCK TABLE namespaces IN ACCESS EXCLUSIVE MODE"); err != nil {
			t.Fatal("install direct lock timeout control")
		}
		_, err = client.Exec(ctx, "SELECT count(*) FROM namespaces")
		if !errors.As(err, &pgError) || pgError.Code != "55P03" || ctx.Err() != nil {
			t.Fatal("pooled PostgreSQL lock timeout was not enforced")
		}
	})

	t.Run("pooler-and-writer-recovery", func(t *testing.T) {
		pool, err := database.OpenRuntimeObserved(ctx, dsn("api", "catalog_test"), settings, nil)
		if err != nil {
			t.Fatal("recovery positive control")
		}
		defer pool.Close()
		writer, err := os.ReadFile(os.Getenv("CATALOG_TEST_PGBOUNCER_WRITER_FILE")) // #nosec G703 -- explicit invocation-owned marker from the selected fixture, never a request path.
		if err != nil || string(writer) != "standby" {
			t.Fatal("fenced writer promotion not completed")
		}
		for _, service := range []string{"pooler", "standby"} {
			poolerCompose(t, ctx, "stop", service)
			probe, cancelProbe := context.WithTimeout(ctx, 2*time.Second)
			err := pool.Ping(probe)
			cancelProbe()
			if err == nil {
				t.Fatal("stopped dependency remained usable")
			}
			poolerCompose(t, ctx, "start", service)
			deadline := time.Now().Add(30 * time.Second)
			for {
				probe, cancelProbe := context.WithTimeout(ctx, time.Second)
				err = pool.Ping(probe)
				cancelProbe()
				if err == nil {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("original pool did not recover")
				}
				time.Sleep(100 * time.Millisecond)
			}
			if _, err := database.VerifyRuntimeRole(ctx, pool, "api"); err != nil {
				t.Fatal("retained schema/role lost after recovery")
			}
		}
	})
}

func poolerCompose(t *testing.T, ctx context.Context, action, service string) {
	t.Helper()
	project := os.Getenv("CATALOG_TEST_PGBOUNCER_PROJECT")
	path := os.Getenv("CATALOG_TEST_PGBOUNCER_COMPOSE")
	if !strings.HasPrefix(project, "oac-pgbouncer-") || path == "" {
		t.Fatal("missing explicit owned pooler fixture")
	}
	command := exec.CommandContext(ctx, "docker", "compose", "--env-file", "/dev/null", "-f", path, "-p", project, action, service) // #nosec G204,G702 -- exact selected owned fixture, fixed service/action calls.
	if err := command.Run(); err != nil {
		t.Fatal("owned fixture dependency transition failed")
	}
}

func poolerLogs(t *testing.T, ctx context.Context, service string) string {
	t.Helper()
	return poolerLogsSince(t, ctx, service, "")
}

func poolerLogsSince(t *testing.T, ctx context.Context, service, since string) string {
	t.Helper()
	arguments := []string{"compose", "--env-file", "/dev/null", "-f", os.Getenv("CATALOG_TEST_PGBOUNCER_COMPOSE"), "-p", os.Getenv("CATALOG_TEST_PGBOUNCER_PROJECT"), "logs", "--no-color", "--tail", "100"}
	if since != "" {
		arguments = append(arguments, "--since", since)
	}
	command := exec.CommandContext(ctx, "docker", append(arguments, service)...) // #nosec G204,G702 -- selected owned fixture and fixed service controls; logs remain private.
	output, err := command.Output()
	if err != nil || len(output) > 256<<10 {
		t.Fatal("bounded pooler TLS diagnostics unavailable")
	}
	return string(output)
}
