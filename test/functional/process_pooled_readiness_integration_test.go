//go:build integration

package functional_test

import (
	"context"
	"errors"
	"net/http"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// API readiness can use a retained API session while another role's pool still
// holds a failed backend login. Qualify new authenticated sessions, not a delay.
func awaitFreshPooledRoles(ctx context.Context, t *testing.T, dsn func(string) string) {
	t.Helper()
	for _, role := range []string{"api", "worker"} {
		probeCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		tick := time.NewTicker(20 * time.Millisecond)
		attempts := 0
		for {
			attempts++
			connection, err := pgx.Connect(probeCtx, dsn(role))
			if err == nil {
				var user string
				var recovery bool
				var readOnly bool
				err = connection.QueryRow(probeCtx, "SELECT current_user,pg_is_in_recovery(),current_setting('transaction_read_only')::boolean").Scan(&user, &recovery, &readOnly)
				closeCtx, closeCancel := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
				_ = connection.Close(closeCtx)
				closeCancel()
				if err == nil && (user != "catalog_"+role || recovery || readOnly) {
					tick.Stop()
					cancel()
					t.Fatal("fresh pooled session did not reach the exact role and writable server")
				}
				if err == nil {
					break
				}
			}
			var server *pgconn.PgError
			if !errors.As(err, &server) || !cachedPoolerLogin(server) {
				t.Logf("fresh pooled role refusal: role=%s sqlState=%s", role, processPostgresState(server))
				tick.Stop()
				cancel()
				t.Fatal("fresh pooled role login failed outside the known cached-login recovery boundary")
			}
			select {
			case <-probeCtx.Done():
				tick.Stop()
				cancel()
				t.Fatal("fresh pooled role login recovery deadline expired")
			case <-tick.C:
			}
		}
		tick.Stop()
		cancel()
		t.Logf("fresh pooled role ready: role=%s attempts=%d", role, attempts)
	}
}

func qualifyPooledRoleRecovery(ctx context.Context, t *testing.T, f processFixture, dsn func(string) string, configuration, apiAddress string) {
	t.Helper()
	ownerDSN := os.Getenv("CATALOG_TEST_PGBOUNCER_PROMOTED_OWNER_DSN")
	if ownerDSN == "" {
		t.Fatal("owned promoted schema-owner DSN is required")
	}
	owner, err := pgx.Connect(ctx, ownerDSN)
	if err != nil {
		t.Fatal("connect to the owned promoted schema owner")
	}
	defer func() { _ = owner.Close(context.WithoutCancel(ctx)) }()
	// Stopped workers can leave idle session backends briefly. Observe their
	// actual departure before forcing a new backend authentication attempt.
	idleCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	tick := time.NewTicker(20 * time.Millisecond)
	defer tick.Stop()
	for {
		var count int
		if err := owner.QueryRow(idleCtx, "SELECT count(*) FROM pg_stat_activity WHERE usename='catalog_worker'").Scan(&count); err != nil {
			t.Fatal("observe owned worker backend drain")
		}
		if count == 0 {
			break
		}
		select {
		case <-idleCtx.Done():
			t.Fatal("owned worker backends did not drain")
		case <-tick.C:
		}
	}
	tick.Stop()
	cancel()
	if _, err := owner.Exec(ctx, "ALTER ROLE catalog_worker NOLOGIN"); err != nil {
		t.Fatal("disable owned worker login")
	}
	defer func() {
		restoreCtx, restoreCancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
		defer restoreCancel()
		if _, err := owner.Exec(restoreCtx, "ALTER ROLE catalog_worker LOGIN"); err != nil {
			t.Error("restore owned worker login")
		}
	}()
	f.request(ctx, apiAddress, "GET", "/readyz", "", "", 200)
	f.run(ctx, f.command(ctx, "catalogd", dsn("worker"), "worker", "--config", configuration), 1, "event=process.failed", "stage=startup", "error_code=INTERNAL")
	// The API remains healthy while a new worker is refused. The unsuccessful
	// start is a counterexample, not a process that the fixture retries.
	f.request(ctx, apiAddress, "GET", "/readyz", "", "", 200)
	connection, loginErr := pgx.Connect(ctx, dsn("worker"))
	if connection != nil {
		var answer int
		loginErr = connection.QueryRow(ctx, "SELECT 1").Scan(&answer)
		_ = connection.Close(ctx)
	}
	if loginErr == nil {
		t.Fatal("disabled role unexpectedly admitted a fresh pooled login")
	}
	var cached *pgconn.PgError
	_ = errors.As(loginErr, &cached)
	t.Logf("owned worker login counterexample: API ready=true worker startup refused=true cached login=%t", cachedPoolerLogin(cached))
	if _, err := owner.Exec(ctx, "ALTER ROLE catalog_worker LOGIN"); err != nil {
		t.Fatal("restore owned worker login")
	}
	awaitFreshPooledRoles(ctx, t, dsn)
}

// Export fixed problem and row observations only. The original failed command
// stays failed; this diagnostic neither retries it nor reveals its response.
func (f processFixture) observeAssignmentRefusal(ctx context.Context, path string, request *http.Request, response *http.Response, raw []byte) {
	const prefix = "/api/v1/access/assignments/"
	if !strings.HasPrefix(path, prefix) || !strings.HasSuffix(path, ":revoke") || f.ownerDSN == "" {
		return
	}
	id := strings.TrimSuffix(strings.TrimPrefix(path, prefix), ":revoke")
	probeCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	owner, err := pgx.Connect(probeCtx, f.ownerDSN)
	if err != nil {
		f.t.Log("assignment refusal row evidence unavailable")
		return
	}
	defer func() { _ = owner.Close(probeCtx) }()
	var active bool
	var revision int64
	var granted time.Time
	if err := owner.QueryRow(probeCtx, "SELECT state='active',revision,granted_at FROM role_assignments WHERE tenant_id=$1 AND id=$2", f.tenantID, id).Scan(&active, &revision, &granted); err != nil {
		f.t.Log("assignment refusal row evidence unavailable")
		return
	}
	date, dateErr := http.ParseTime(response.Header.Get("Date"))
	code, cannotRevoke := processAssignmentProblem(raw)
	f.t.Logf("assignment refusal evidence: request_id=%s problem=%s cannotRevoke=%t active=%t revisionMatches=%t responseDateBeforeGrantSecond=%t", processRequestID(response.Header.Get("X-Request-ID")), code, cannotRevoke, active, request.Header.Get("If-Match") == `"rev-`+strconv.FormatInt(revision, 10)+`"`, dateErr == nil && date.Before(granted.Truncate(time.Second)))
}
