// Environment overrides are an explicit typed allowlist; presence and empty
// values are distinct so malformed deployment inputs cannot silently fall back.

package config

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/jsondoc"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/safeerr"
)

type environmentOverride struct {
	name  string
	apply func(*Config, string) error
}

// environmentOverrides is deliberately explicit: adding a Config field never
// makes an arbitrary similarly named environment variable authoritative.
var environmentOverrides = []environmentOverride{
	boolOverride("CATALOG_MCP_ENABLED", func(c *Config) *bool { return &c.MCP.Enabled }),
	intOverride("CATALOG_MCP_MAX_TOOL_BYTES", func(c *Config) *int { return &c.MCP.MaxToolBytes }),
	intOverride("CATALOG_MCP_MAX_CONCURRENCY", func(c *Config) *int { return &c.MCP.MaxConcurrency }),
	{name: "CATALOG_MCP_ALLOWED_ORIGINS", apply: func(c *Config, value string) error { return jsondoc.Decode([]byte(value), &c.MCP.AllowedOrigins) }},
	boolOverride("CATALOG_RATINGS_SHARING_ENABLED", func(c *Config) *bool { return &c.Ratings.SharingEnabled }),
	intOverride("CATALOG_RATINGS_MINIMUM_SHARED_COUNT", func(c *Config) *int { return &c.Ratings.MinimumSharedCount }),
	{name: "CATALOG_DEMO_ARCHIVES", apply: applyDemoArchives},
	{name: "CATALOG_TRACING", apply: applyTracing},
	stringOverride("CATALOG_ENVIRONMENT", func(c *Config) *string { return &c.Environment }),
	stringOverride("CATALOG_LOG_LEVEL", func(c *Config) *string { return &c.LogLevel }),
	stringOverride("CATALOG_LOG_FORMAT", func(c *Config) *string { return &c.LogFormat }),
	stringOverride("CATALOG_PROFILE", func(c *Config) *string { return &c.Profile }),
	stringOverride("CATALOG_SERVICE_NAME", func(c *Config) *string { return &c.ServiceName }),
	boolOverride("CATALOG_HTTP_SERVICE_ACTIVITY_ENABLED", func(c *Config) *bool { return &c.HTTP.ServiceActivityEnabled }),
	stringOverride("CATALOG_HTTP_ADDRESS", func(c *Config) *string { return &c.HTTP.Address }),
	stringOverride("CATALOG_PUBLIC_BASE_URL", func(c *Config) *string { return &c.HTTP.PublicBaseURL }),
	durationOverride("CATALOG_HTTP_READ_TIMEOUT", func(c *Config) *Duration { return &c.HTTP.ReadTimeout }),
	durationOverride("CATALOG_HTTP_REQUEST_TIMEOUT", func(c *Config) *Duration { return &c.HTTP.RequestTimeout }),
	durationOverride("CATALOG_HTTP_WRITE_TIMEOUT", func(c *Config) *Duration { return &c.HTTP.WriteTimeout }),
	durationOverride("CATALOG_HTTP_IDLE_TIMEOUT", func(c *Config) *Duration { return &c.HTTP.IdleTimeout }),
	durationOverride("CATALOG_HTTP_SHUTDOWN_TIMEOUT", func(c *Config) *Duration { return &c.HTTP.ShutdownTimeout }),
	int64Override("CATALOG_HTTP_MAX_BODY_BYTES", func(c *Config) *int64 { return &c.HTTP.MaxBodyBytes }),
	{name: "CATALOG_HTTP_CORS_ORIGINS", apply: applyCORSOrigins},
	stringOverride("CATALOG_DATABASE_DRIVER", func(c *Config) *string { return &c.Database.Driver }),
	int32Override("CATALOG_DATABASE_MAX_CONNECTIONS", func(c *Config) *int32 { return &c.Database.MaxConnections }),
	durationOverride("CATALOG_DATABASE_MAX_CONN_LIFETIME", func(c *Config) *Duration { return &c.Database.MaxConnLifetime }),
	durationOverride("CATALOG_DATABASE_MAX_CONN_IDLE_TIME", func(c *Config) *Duration { return &c.Database.MaxConnIdleTime }),
	durationOverride("CATALOG_DATABASE_HEALTH_CHECK_PERIOD", func(c *Config) *Duration { return &c.Database.HealthCheckPeriod }),
	stringOverride("CATALOG_AUTH_MODE", func(c *Config) *string { return &c.Auth.Mode }),
	stringOverride("CATALOG_AUTH_ISSUER", func(c *Config) *string { return &c.Auth.Issuer }),
	stringOverride("CATALOG_AUTH_TRUST_BUNDLE_FILE", func(c *Config) *string { return &c.Auth.TrustBundleFile }),
	stringOverride("CATALOG_AUTH_AUDIENCE", func(c *Config) *string { return &c.Auth.Audience }),
	stringOverride("CATALOG_AUTH_TENANCY_MODE", func(c *Config) *string { return &c.Auth.TenancyMode }),
	stringOverride("CATALOG_AUTH_DEDICATED_TENANT_ID", func(c *Config) *string { return &c.Auth.DedicatedTenantID }),
	stringOverride("CATALOG_AUTH_ORGANIZATION_CLAIM", func(c *Config) *string { return &c.Auth.OrganizationClaim }),
	boolOverride("CATALOG_AUTH_ENFORCE_SCOPES", func(c *Config) *bool { return &c.Auth.EnforceScopes }),
	intOverride("CATALOG_VALIDATION_MAX_FINDINGS", func(c *Config) *int { return &c.Validation.MaxFindings }),
	intOverride("CATALOG_VALIDATION_MAX_DIAGNOSTIC_BYTES", func(c *Config) *int { return &c.Validation.MaxDiagnosticBytes }),
	intOverride("CATALOG_JOBS_MAX_ATTEMPTS", func(c *Config) *int { return &c.Jobs.MaxAttempts }),
	durationOverride("CATALOG_JOBS_RETRY_CEILING", func(c *Config) *Duration { return &c.Jobs.RetryCeiling }),
	durationOverride("CATALOG_WORKER_POLL_INTERVAL", func(c *Config) *Duration { return &c.Worker.PollInterval }),
	durationOverride("CATALOG_WORKER_DEPENDENCY_RETRY_INITIAL_DELAY", func(c *Config) *Duration { return &c.Worker.DependencyRetryInitialDelay }),
	durationOverride("CATALOG_WORKER_DEPENDENCY_RETRY_CEILING", func(c *Config) *Duration { return &c.Worker.DependencyRetryCeiling }),
	durationOverride("CATALOG_WORKER_LEASE_DURATION", func(c *Config) *Duration { return &c.Worker.LeaseDuration }),
	intOverride("CATALOG_WORKER_CONCURRENCY", func(c *Config) *int { return &c.Worker.Concurrency }),
	durationOverride("CATALOG_WORKER_SOURCE_MAINTENANCE_INTERVAL", func(c *Config) *Duration { return &c.Worker.SourceMaintenanceInterval }),
	durationOverride("CATALOG_WORKER_HOUSEKEEPING_INTERVAL", func(c *Config) *Duration { return &c.Worker.HousekeepingInterval }),
	durationOverride("CATALOG_WORKER_TERMINAL_JOB_RETENTION", func(c *Config) *Duration { return &c.Worker.TerminalJobRetention }),
	durationOverride("CATALOG_WORKER_ACKNOWLEDGED_OUTBOX_RETENTION", func(c *Config) *Duration { return &c.Worker.AcknowledgedOutboxRetention }),
	stringOverride("CATALOG_WORKER_OPERATIONS_ADDRESS", func(c *Config) *string { return &c.Worker.OperationsAddress }),
	durationOverride("CATALOG_FEDERATION_REQUEST_TIMEOUT", func(c *Config) *Duration { return &c.Federation.RequestTimeout }),
	int64Override("CATALOG_FEDERATION_MAX_RESPONSE_BYTES", func(c *Config) *int64 { return &c.Federation.MaxResponseBytes }),
	intOverride("CATALOG_FEDERATION_MAX_REDIRECTS", func(c *Config) *int { return &c.Federation.MaxRedirects }),
	intOverride("CATALOG_FEDERATION_MAX_FANOUT", func(c *Config) *int { return &c.Federation.MaxFanout }),
	intOverride("CATALOG_FEDERATION_MAX_LIVE_PAGES_PER_SOURCE", func(c *Config) *int { return &c.Federation.MaxLivePagesPerSource }),
	intOverride("CATALOG_FEDERATION_MAX_HOPS", func(c *Config) *int { return &c.Federation.MaxHops }),
	stringOverride("CATALOG_FEDERATION_POLICY_FILE", func(c *Config) *string { return &c.Federation.PolicyFile }),
	{name: "CATALOG_DISCOVERY_HOSTS", apply: applyDiscoveryHosts},
	stringOverride("CATALOG_PUBLICATION_ARD_PROFILE", func(c *Config) *string { return &c.Publication.ARDProfile }),
	stringOverride("CATALOG_PUBLICATION_AI_CATALOG_PROFILE", func(c *Config) *string { return &c.Publication.AICatalogProfile }),
	boolOverride("CATALOG_WEBHOOKS_ENABLED", func(c *Config) *bool { return &c.Webhooks.Enabled }),
	intOverride("CATALOG_WEBHOOKS_MAX_ATTEMPTS", func(c *Config) *int { return &c.Webhooks.MaxAttempts }),
	durationOverride("CATALOG_WEBHOOKS_RETRY_CEILING", func(c *Config) *Duration { return &c.Webhooks.RetryCeiling }),
	stringOverride("CATALOG_WEBHOOKS_POLICY_FILE", func(c *Config) *string { return &c.Webhooks.PolicyFile }),
	durationOverride("CATALOG_WEBHOOKS_TIMEOUT", func(c *Config) *Duration { return &c.Webhooks.Timeout }),
	durationOverride("CATALOG_WEBHOOKS_POLL_INTERVAL", func(c *Config) *Duration { return &c.Webhooks.PollInterval }),
	{name: "CATALOG_SEMANTIC_SEARCH", apply: applySemantic},
	{name: "CATALOG_LISTING_MEDIA", apply: applyListingMedia},
	{name: "CATALOG_TRUST_VERIFICATION", apply: applyTrustVerification},
	{name: "CATALOG_PUBLISHER_DOMAIN_VERIFICATION", apply: applyPublisherDomainVerification},
	boolOverride("CATALOG_MANAGED_MEDIA_ENABLED", func(c *Config) *bool { return &c.ManagedMedia.Enabled }),
	boolOverride("CATALOG_SOFTWARE_ASSESSMENT_ENABLED", func(c *Config) *bool { return &c.SoftwareAssessment.Enabled }),
	boolOverride("CATALOG_CREDENTIAL_RESOLUTION_ENABLED", func(c *Config) *bool { return &c.CredentialResolution.Enabled }),
	stringOverride("CATALOG_CREDENTIAL_RESOLUTION_DIRECTORY", func(c *Config) *string { return &c.CredentialResolution.Directory }),
}

// EnvironmentVariables returns a fresh inventory of supported runtime overrides
// and separately loaded secret inputs, without reading their values.
func EnvironmentVariables() []string {
	names := make([]string, 0, len(environmentOverrides)+6)
	for _, override := range environmentOverrides {
		names = append(names, override.name)
	}
	return append(names,
		"CATALOG_DATABASE_DSN",
		"CATALOG_DATABASE_DSN_FILE",
		"CATALOG_CURSOR_SIGNING_KEY",
		"CATALOG_CURSOR_SIGNING_KEY_FILE",
		"CATALOG_WEBHOOK_ENCRYPTION_KEY",
		"CATALOG_WEBHOOK_ENCRYPTION_KEY_FILE",
	)
}

// applyEnvironment applies only declared overrides and the mutually exclusive DSN
// inputs, rejecting obsolete object-store settings and present-but-empty values.
func applyEnvironment(cfg *Config, lookup func(string) (string, bool)) error {
	for _, name := range []string{"CATALOG_OBJECT_DRIVER", "CATALOG_OBJECT_DIRECTORY", "CATALOG_OBJECT_ENDPOINT", "CATALOG_OBJECT_BUCKET", "CATALOG_OBJECT_REGION"} {
		if _, exists := lookup(name); exists {
			return safeerr.New(fmt.Sprintf("config: %s is no longer supported; Catalog documents use PostgreSQL", name))
		}
	}
	for _, override := range environmentOverrides {
		value, exists := lookup(override.name)
		if !exists {
			continue
		}
		if value == "" {
			return safeerr.New(fmt.Sprintf("config: %s must not be empty when set", override.name))
		}
		if err := override.apply(cfg, value); err != nil {
			return safeerr.Wrap(fmt.Errorf("config: %s: %w", override.name, err), "invalid setting "+override.name+"; check its documented type and limits")
		}
	}
	dsn, exists, err := readEnvironmentSecret("CATALOG_DATABASE_DSN", "CATALOG_DATABASE_DSN_FILE", lookup)
	if err != nil {
		return fmt.Errorf("config: database DSN: %w", err)
	}
	if exists {
		cfg.Database.DSN = dsn
	}
	return nil
}

// stringOverride assigns one allowlisted string; cross-field validation follows loading.
func stringOverride(name string, selectField func(*Config) *string) environmentOverride {
	return environmentOverride{name: name, apply: func(cfg *Config, value string) error {
		*selectField(cfg) = value
		return nil
	}}
}

// durationOverride parses Go duration syntax before replacing the selected field.
func durationOverride(name string, selectField func(*Config) *Duration) environmentOverride {
	return environmentOverride{name: name, apply: func(cfg *Config, value string) error {
		parsed, err := time.ParseDuration(value)
		if err != nil {
			return safeerr.New("must be a Go duration such as 250ms, 10s, or 2m")
		}
		selectField(cfg).Duration = parsed
		return nil
	}}
}

// intOverride rejects values outside the host integer range before assignment.
func intOverride(name string, selectField func(*Config) *int) environmentOverride {
	return environmentOverride{name: name, apply: func(cfg *Config, value string) error {
		parsed, err := strconv.ParseInt(value, 10, 0)
		if err != nil {
			return safeerr.New("must be a base-10 integer")
		}
		*selectField(cfg) = int(parsed)
		return nil
	}}
}

// int32Override accepts base-10 values only within the signed 32-bit range.
func int32Override(name string, selectField func(*Config) *int32) environmentOverride {
	return environmentOverride{name: name, apply: func(cfg *Config, value string) error {
		parsed, err := strconv.ParseInt(value, 10, 32)
		if err != nil {
			return safeerr.New("must be a base-10 32-bit integer")
		}
		*selectField(cfg) = int32(parsed)
		return nil
	}}
}

// int64Override preserves a signed 64-bit bound without narrowing through int.
func int64Override(name string, selectField func(*Config) *int64) environmentOverride {
	return environmentOverride{name: name, apply: func(cfg *Config, value string) error {
		parsed, err := strconv.ParseInt(value, 10, 64)
		if err != nil {
			return safeerr.New("must be a base-10 64-bit integer")
		}
		*selectField(cfg) = parsed
		return nil
	}}
}

// boolOverride accepts exactly true or false, excluding numeric and shorthand forms.
func boolOverride(name string, selectField func(*Config) *bool) environmentOverride {
	return environmentOverride{name: name, apply: func(cfg *Config, value string) error {
		switch value {
		case "true":
			*selectField(cfg) = true
		case "false":
			*selectField(cfg) = false
		default:
			return safeerr.New("must be exactly true or false")
		}
		return nil
	}}
}

// applyCORSOrigins splits and trims the explicit origin list, rejecting empty
// members before replacement; origin syntax is checked by configuration validation.
func applyCORSOrigins(cfg *Config, value string) error {
	items := strings.Split(value, ",")
	origins := make([]string, 0, len(items))
	for _, item := range items {
		origin := strings.TrimSpace(item)
		if origin == "" {
			return safeerr.New("must be a comma-separated list of non-empty origins")
		}
		origins = append(origins, origin)
	}
	cfg.HTTP.CORSOrigins = origins
	return nil
}

// applyDiscoveryHosts replaces host mappings from bounded, closed JSON without
// null members or duplicate keys; tenancy and host rules are validated afterward.
func applyDiscoveryHosts(cfg *Config, value string) error {
	if len(value) > 64<<10 {
		return safeerr.New("must be at most 64 KiB")
	}
	raw := []byte(value)
	if err := jsondoc.Validate(raw); err != nil {
		return fmt.Errorf("must be duplicate-key-safe JSON: %w", err)
	}
	var document any
	if err := json.Unmarshal(raw, &document); err != nil || containsNull(document) {
		return safeerr.New("must be a JSON array without null values")
	}
	var hosts []DiscoveryHost
	if err := jsondoc.Decode(raw, &hosts); err != nil {
		return fmt.Errorf("decode host mappings: %w", err)
	}
	cfg.Discovery.Hosts = hosts
	return nil
}
