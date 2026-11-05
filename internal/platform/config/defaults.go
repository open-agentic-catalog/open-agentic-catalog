// Default values define the local in-memory baseline and fixed process roles;
// optional capability inputs are validated only when their role enables them.

package config

import (
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"time"
)

// Role selects the dependency and database-authority contract validated at startup.
type Role string

const (
	RoleAPI       Role = "api"
	RoleWorker    Role = "worker"
	RoleAll       Role = "all"
	RoleMigration Role = "migration"

	activeARDProfileID       = "ard-0.91-aa3e598"
	activeAICatalogProfileID = "ai-catalog-1.0-b062278"
)

// Valid accepts only the API, worker, combined development and migration roles.
func (r Role) Valid() bool {
	switch r {
	case RoleAPI, RoleWorker, RoleAll, RoleMigration:
		return true
	default:
		return false
	}
}

// Defaults returns a fresh local baseline with memory storage and deny-by-default
// federation. It panics only if the embedded policy violates its own build invariant.
func Defaults() Config {
	defaults := Config{
		PublisherDomainVerification: PublisherDomainVerificationDefaults(),
		MCP:                         MCP{MaxToolBytes: 256 << 10, MaxConcurrency: 4, AllowedOrigins: []string{}},
		Ratings:                     Ratings{MinimumSharedCount: 5},
		Validation:                  catalog.DefaultDiagnosticLimits(),
		LogLevel:                    "info",
		LogFormat:                   "text",
		Environment:                 "development",
		Profile:                     "local",
		ServiceName:                 "open-agentic-catalog",
		SemanticSearch:              SemanticDefaults(),
		ListingMedia:                ListingMediaDefaults(),
		TrustVerification:           TrustVerificationDefaults(),
		Tracing:                     Tracing{SampleRatio: 0.1, QueueSize: 2048, BatchSize: 256, ExportTimeout: Duration{Duration: 5 * time.Second}},
		HTTP: HTTP{
			Address:         "127.0.0.1:8080",
			PublicBaseURL:   "http://127.0.0.1:8080",
			ReadTimeout:     Duration{Duration: 10 * time.Second},
			RequestTimeout:  Duration{Duration: 10 * time.Second},
			WriteTimeout:    Duration{Duration: 30 * time.Second},
			IdleTimeout:     Duration{Duration: 60 * time.Second},
			ShutdownTimeout: Duration{Duration: 20 * time.Second},
			MaxBodyBytes:    2 << 20,
		},
		Database: Database{
			Driver: "memory", MaxConnections: 12,
			MaxConnLifetime:   Duration{Duration: 30 * time.Minute},
			MaxConnIdleTime:   Duration{Duration: 5 * time.Minute},
			HealthCheckPeriod: Duration{Duration: 30 * time.Second},
		},
		Auth: Auth{Mode: "development"},
		Jobs: Jobs{MaxAttempts: 5, RetryCeiling: Duration{Duration: time.Hour}},
		Worker: Worker{
			PollInterval:                Duration{Duration: 250 * time.Millisecond},
			DependencyRetryInitialDelay: Duration{Duration: time.Second},
			DependencyRetryCeiling:      Duration{Duration: 30 * time.Second},
			LeaseDuration:               Duration{Duration: 30 * time.Second},
			Concurrency:                 4,
			SourceMaintenanceInterval:   Duration{Duration: 15 * time.Minute},
			HousekeepingInterval:        Duration{Duration: 5 * time.Minute},
			TerminalJobRetention:        Duration{Duration: 168 * time.Hour},
			AcknowledgedOutboxRetention: Duration{Duration: 24 * time.Hour},
			OperationsAddress:           "127.0.0.1:9090",
		},
		Federation: Federation{
			RequestTimeout:        Duration{Duration: 2 * time.Second},
			MaxResponseBytes:      2 << 20,
			MaxRedirects:          3,
			MaxFanout:             3,
			MaxLivePagesPerSource: 1,
			MaxHops:               2,
			Policy: FederationPolicy{
				SchemaVersion: 1,
				PolicyID:      "built-in-deny",
				Revision:      1,
				Public:        FederationPublicPolicy{Mode: "allowlist"},
			},
		},
		Publication: Publication{
			ARDProfile:       activeARDProfileID,
			AICatalogProfile: activeAICatalogProfileID,
		},
		Webhooks: Webhooks{
			RetryCeiling: Duration{Duration: time.Hour},
			MaxAttempts:  5,
			Timeout:      Duration{Duration: 10 * time.Second},
			PollInterval: Duration{Duration: 500 * time.Millisecond},
		},
	}
	if err := loadFederationPolicy(&defaults); err != nil {
		panic("invalid built-in federation policy: " + err.Error())
	}
	return defaults
}
