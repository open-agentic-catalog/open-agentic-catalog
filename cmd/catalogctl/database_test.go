package main

import (
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/config"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/database"
)

func TestOperatorPoolPolicyPreservesResolvedSettings(t *testing.T) {
	configuration := config.Defaults()
	configuration.Database.Driver = "postgres"
	configuration.Database.DSN = "postgres://operator@must-not-connect.invalid/catalog?pool_max_conns=7"
	configuration.Database.MaxConnLifetime.Duration = time.Hour
	configuration.Database.MaxConnIdleTime.Duration = 1250 * time.Millisecond
	configuration.Database.HealthCheckPeriod.Duration = 75 * time.Millisecond
	path := writeCatalogctlTestConfig(t, configuration)
	// Zero must reach pgx unchanged so the DSN size remains authoritative.
	// A fractional JSON duration and an environment override must not be
	// rounded, exchanged or replaced by the old hard-coded pool timings.
	t.Setenv("CATALOG_DATABASE_MAX_CONNECTIONS", "0")
	t.Setenv("CATALOG_DATABASE_MAX_CONN_LIFETIME", "2m15s")
	resolved, err := config.LoadFor(path, config.RoleMigration)
	if err != nil {
		t.Fatal(err)
	}
	want := database.PoolConfig{
		MaxConnections:    0,
		MaxConnLifetime:   135 * time.Second,
		MaxConnIdleTime:   1250 * time.Millisecond,
		HealthCheckPeriod: 75 * time.Millisecond,
	}
	if got := databasePoolConfig(resolved.Database); got != want {
		t.Fatalf("operator pool policy = %+v, want %+v", got, want)
	}
}
