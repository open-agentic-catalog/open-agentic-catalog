package database

import (
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/config"
)

func testPoolConfig(size int32) PoolConfig {
	c := config.Defaults().Database
	return PoolConfig{MaxConnections: size, MaxConnLifetime: c.MaxConnLifetime.Duration, MaxConnIdleTime: c.MaxConnIdleTime.Duration, HealthCheckPeriod: c.HealthCheckPeriod.Duration}
}

func TestPoolConfigurationPrecedenceAndConnectionPolicy(t *testing.T) {
	t.Parallel()
	const dsn = "postgres://user@localhost/catalog?pool_max_conns=7&pool_min_conns=2&pool_min_idle_conns=1&pool_max_conn_lifetime=1h&pool_max_conn_idle_time=1h&pool_health_check_period=1h&pool_max_conn_lifetime_jitter=3s&connect_timeout=9&application_name=pool-unit"
	for _, runtime := range []bool{false, true} {
		for _, size := range []int32{0, 4} {
			settings := PoolConfig{MaxConnections: size, MaxConnLifetime: 40 * time.Minute, MaxConnIdleTime: 2 * time.Minute, HealthCheckPeriod: 1500 * time.Millisecond}
			tracer := queryTracer{observer: &recordingQueryObserver{}, now: time.Now}
			c, err := configurePool(dsn, settings, tracer, runtime)
			if err != nil {
				t.Fatal(err)
			}
			wantSize := size
			if size == 0 {
				wantSize = 7
			}
			if c.MaxConns != wantSize || c.MaxConnLifetime != 40*time.Minute || c.MaxConnIdleTime != 2*time.Minute || c.HealthCheckPeriod != 1500*time.Millisecond {
				t.Fatalf("effective pool policy: %+v", c)
			}
			if c.MinConns != 2 || c.MinIdleConns != 1 || c.MaxConnLifetimeJitter != 3*time.Second || c.ConnConfig.ConnectTimeout != 9*time.Second || c.ConnConfig.RuntimeParams["application_name"] != "pool-unit" {
				t.Fatal("unrelated DSN settings lost")
			}
			if c.ConnConfig.Tracer == nil || c.AfterConnect == nil || (c.ConnConfig.ValidateConnect != nil) != runtime {
				t.Fatal("connection callbacks lost")
			}
			watcher, ok := c.ConnConfig.BuildContextWatcherHandler(nil).(*pgconn.CancelRequestContextWatcherHandler)
			if !ok || watcher.DeadlineDelay != 500*time.Millisecond {
				t.Fatal("cancellation recovery lost")
			}
		}
	}
	defaults, err := configurePool(dsn, testPoolConfig(4), nil, false)
	if err != nil || defaults.MaxConnLifetime != 30*time.Minute || defaults.MaxConnIdleTime != 5*time.Minute || defaults.HealthCheckPeriod != 30*time.Second {
		t.Fatalf("default timing precedence: %v", err)
	}
	for _, runtime := range []bool{false, true} {
		c, err := configurePool(dsn+"&target_session_attrs=read-only", testPoolConfig(4), nil, runtime)
		if err != nil || c.ConnConfig.ValidateConnect == nil {
			t.Fatalf("DSN validator lost: %v", err)
		}
	}
}

func TestPoolRejectsNonpositiveDurationsBeforeConnecting(t *testing.T) {
	t.Parallel()
	for _, field := range []string{"lifetime", "idle", "health"} {
		for _, value := range []time.Duration{0, -time.Second} {
			settings := testPoolConfig(1)
			switch field {
			case "lifetime":
				settings.MaxConnLifetime = value
			case "idle":
				settings.MaxConnIdleTime = value
			case "health":
				settings.HealthCheckPeriod = value
			}
			pool, err := Open(t.Context(), "postgres://must-not-connect.invalid/catalog", settings)
			if pool != nil || err == nil || err.Error() != "PostgreSQL pool durations must be positive" {
				t.Fatalf("%s %v: %v", field, value, err)
			}
		}
	}
}

func TestRuntimePoolMode(t *testing.T) {
	for _, mode := range []string{"", "session", "transaction", "statement", "SESSION", "session\n", "private-untrusted-value"} {
		t.Run(mode, func(t *testing.T) {
			err := validateRuntimePoolMode(mode)
			wantAccepted := mode == "" || mode == "session"
			if (err == nil) != wantAccepted {
				t.Fatalf("accepted=%v want=%v", err == nil, wantAccepted)
			}
			if err != nil && err.Error() != "PostgreSQL runtime requires session pooling" {
				t.Fatal("pool mode diagnostic must be fixed, not provider text")
			}
		})
	}
}
