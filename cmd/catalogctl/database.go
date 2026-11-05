package main

import (
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/config"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/database"
)

// databasePoolConfig carries the resolved settings to every operator pool.
func databasePoolConfig(configuration config.Database) database.PoolConfig {
	return database.PoolConfig{
		MaxConnections:    configuration.MaxConnections,
		MaxConnLifetime:   configuration.MaxConnLifetime.Duration,
		MaxConnIdleTime:   configuration.MaxConnIdleTime.Duration,
		HealthCheckPeriod: configuration.HealthCheckPeriod.Duration,
	}
}
