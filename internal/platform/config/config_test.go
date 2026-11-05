package config

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/schemas"
)

func TestActivePublicationProfilesAreEmbedded(t *testing.T) {
	embedded := make(map[string]struct{}, len(schemas.Profiles))
	for _, profile := range schemas.Profiles {
		embedded[profile.ID] = struct{}{}
	}
	for _, profileID := range []string{activeARDProfileID, activeAICatalogProfileID} {
		if _, exists := embedded[profileID]; !exists {
			t.Errorf("active publication profile %q is not embedded", profileID)
		}
	}
}

// TestShippedConfigurationsAreValid checks the maintained profile/role matrix,
// default-off integrations and production key requirements without dependencies.
func TestShippedConfigurationsAreValid(t *testing.T) {
	clearRuntimeEnvironment(t)
	profiles := map[string][]Role{
		"local.json":              {RoleAPI, RoleWorker, RoleAll},
		"postgres.json":           {RoleAPI, RoleWorker, RoleMigration},
		"production.example.json": {RoleAPI, RoleWorker, RoleMigration},
	}
	for name, roles := range profiles {
		for _, role := range roles {
			t.Run(name+"/"+string(role), func(t *testing.T) {
				cfg, err := LoadFor(filepath.Join("..", "..", "..", "config", name), role)
				if err != nil {
					t.Fatal(err)
				}
				if cfg.TrustVerification.Enabled || cfg.Tracing.Enabled || cfg.Webhooks.Enabled || cfg.SemanticSearch.Enabled || cfg.ManagedMedia.Enabled || cfg.SoftwareAssessment.Enabled || cfg.CredentialResolution.Enabled {
					t.Fatal("shipped baseline enables an optional capability")
				}
				if cfg.Federation.PolicyFile != "" || cfg.Federation.Policy.Public.Mode != "allowlist" || len(cfg.Federation.Policy.Public.Origins) != 0 || len(cfg.Federation.Policy.PrivateProfiles) != 0 {
					t.Fatal("shipped baseline permits outbound access without an explicitly selected policy")
				}
				secretErr := ValidateRuntimeSecrets(cfg, role)
				if cfg.Environment == "production" && role != RoleMigration {
					if secretErr == nil || !strings.Contains(secretErr.Error(), "CATALOG_CURSOR_SIGNING_KEY") {
						t.Fatalf("production profile must require an injected cursor key: %v", secretErr)
					}
					// Synthetic unit-test input only; production keys are deployment-owned.
					t.Setenv("CATALOG_CURSOR_SIGNING_KEY", base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32)))
					secretErr = ValidateRuntimeSecrets(cfg, role)
				}
				if secretErr != nil {
					t.Fatalf("baseline requires an unexpected runtime secret: %v", secretErr)
				}
			})
		}
	}
	for _, name := range []string{"postgres.json", "production.example.json"} {
		t.Run(name+"/combined", func(t *testing.T) {
			if _, err := LoadFor(filepath.Join("..", "..", "..", "config", name), RoleAll); err == nil {
				t.Fatal("PostgreSQL profile was accepted for the combined role")
			}
		})
	}
}

func TestLoadRejectsTrailingJSONValue(t *testing.T) {
	clearRuntimeEnvironment(t)
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{} {}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadFor(path, RoleAll); err == nil {
		t.Fatal("configuration with a trailing JSON value was accepted")
	}
}

func TestLoadRejectsDuplicateJSONMembers(t *testing.T) {
	clearRuntimeEnvironment(t)
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"environment":"local","environment":"production"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadFor(path, RoleAll); err == nil {
		t.Fatal("configuration with a duplicate member was accepted")
	}
}

func TestLoadRejectsExplicitNullOverrides(t *testing.T) {
	clearRuntimeEnvironment(t)
	for name, raw := range map[string]string{
		"top-level object": `{"http":null}`,
		"nested scalar":    `{"http":{"readTimeout":null}}`,
		"array member":     `{"http":{"corsOrigins":[null]}}`,
		"live page budget": `{"federation":{"maxLivePagesPerSource":null}}`,
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.json")
			if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := LoadFor(path, RoleAll); err == nil || !strings.Contains(err.Error(), "null values") {
				t.Fatalf("explicit null returned %v", err)
			}
		})
	}
}

func TestDurationRejectsNonStringAndMalformedValues(t *testing.T) {
	for _, raw := range []string{`1`, `"not-a-duration"`} {
		var duration Duration
		if err := json.Unmarshal([]byte(raw), &duration); err == nil {
			t.Fatalf("duration %s was accepted", raw)
		}
	}
}

func TestLoadAppliesOnlyDocumentedEnvironmentOverrides(t *testing.T) {
	clearRuntimeEnvironment(t)
	cfg := validTestConfig()
	raw, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CATALOG_HTTP_ADDRESS", "127.0.0.1:9090")
	t.Setenv("CATALOG_PUBLIC_BASE_URL", "https://catalog.example.com")
	t.Setenv("CATALOG_DATABASE_DSN", "postgres://catalog.example.com/catalog")
	t.Setenv("CATALOG_DISCOVERY_HOSTS", `[{"host":"catalog.example.com","tenantId":"00000000-0000-7000-8000-000000000002","mode":"authenticated","publisherDomain":"publisher.example.com"}]`)
	t.Setenv("CATALOG_WORKER_CONCURRENCY", "7")
	t.Setenv("CATALOG_WORKER_HOUSEKEEPING_INTERVAL", "6m")
	t.Setenv("CATALOG_WORKER_TERMINAL_JOB_RETENTION", "240h")
	t.Setenv("CATALOG_WORKER_ACKNOWLEDGED_OUTBOX_RETENTION", "48h")
	t.Setenv("CATALOG_WORKER_OPERATIONS_ADDRESS", "127.0.0.1:9091")
	policyPath := filepath.Join(t.TempDir(), "federation-policy.json")
	if err := os.WriteFile(policyPath, []byte(`{"schemaVersion":1,"policyId":"environment-policy","revision":2,"public":{"mode":"allowlist","origins":["https://partner.example.com:443"]}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CATALOG_FEDERATION_POLICY_FILE", policyPath)
	t.Setenv("CATALOG_HTTP_READ_TIMEOUT", "3s")
	t.Setenv("CATALOG_HTTP_CORS_ORIGINS", "https://one.example.com, https://two.example.com")
	t.Setenv("CATALOG_WEBHOOKS_ENABLED", "false")
	// Unknown runtime typos and independent UI launcher inputs have no authority.
	t.Setenv("CATALOG_SERVICE_NAM", "ignored-environment-sentinel")
	t.Setenv("CATALOG_UI_LOG_LEVEL", "warn")

	loaded, err := LoadFor(path, RoleAll)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.HTTP.Address != "127.0.0.1:9090" || loaded.HTTP.PublicBaseURL != "https://catalog.example.com" || loaded.Database.DSN != "postgres://catalog.example.com/catalog" || len(loaded.Discovery.Hosts) != 1 || loaded.Discovery.Hosts[0].TenantID != "00000000-0000-7000-8000-000000000002" || loaded.Worker.Concurrency != 7 || loaded.Worker.HousekeepingInterval.Duration != 6*time.Minute || loaded.Worker.TerminalJobRetention.Duration != 240*time.Hour || loaded.Worker.AcknowledgedOutboxRetention.Duration != 48*time.Hour || loaded.Worker.OperationsAddress != "127.0.0.1:9091" || loaded.Federation.PolicyFile != policyPath || loaded.Federation.Policy.PolicyID != "environment-policy" || loaded.Federation.PolicyDigest == "" || loaded.HTTP.ReadTimeout.Duration != 3*time.Second || len(loaded.HTTP.CORSOrigins) != 2 || loaded.Webhooks.Enabled {
		t.Fatalf("environment overrides were not applied: %#v", loaded)
	}
	if loaded.ServiceName != cfg.ServiceName || loaded.LogLevel != cfg.LogLevel {
		t.Fatal("unknown or UI-only environment input changed backend configuration")
	}
	report, err := json.Marshal(loaded.Report())
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(report, []byte("ignored-environment-sentinel")) {
		t.Fatal("unknown environment value appeared in configuration report")
	}
}

func TestEveryEnvironmentOverrideIsUniqueAndApplied(t *testing.T) {
	// #nosec G101 -- this map contains configuration names and synthetic non-secret paths only.
	overrides := map[string]string{
		"CATALOG_MCP_ENABLED":         "true",
		"CATALOG_MCP_MAX_TOOL_BYTES":  "4096",
		"CATALOG_MCP_MAX_CONCURRENCY": "2",
		"CATALOG_MCP_ALLOWED_ORIGINS": `["https://console.example.net"]`,
		"CATALOG_DEMO_ARCHIVES":       `{"origin":"https://127.0.0.1:18443","trustBundleFile":"/local.pem","allowlistFile":"/allowlist.json"}`,
		"CATALOG_TRACING":             `{"enabled":false,"installation":"environment-fixture"}`,
		"CATALOG_ENVIRONMENT":         "development",
		"CATALOG_LOG_LEVEL":           "debug", "CATALOG_LOG_FORMAT": "json",
		"CATALOG_PROFILE":                               "environment-profile",
		"CATALOG_SERVICE_NAME":                          "environment-service",
		"CATALOG_HTTP_ADDRESS":                          "0.0.0.0:8181",
		"CATALOG_PUBLIC_BASE_URL":                       "https://catalog.example.net",
		"CATALOG_HTTP_READ_TIMEOUT":                     "2s",
		"CATALOG_HTTP_REQUEST_TIMEOUT":                  "1s",
		"CATALOG_HTTP_WRITE_TIMEOUT":                    "7s",
		"CATALOG_HTTP_IDLE_TIMEOUT":                     "4s",
		"CATALOG_HTTP_SHUTDOWN_TIMEOUT":                 "7s",
		"CATALOG_HTTP_MAX_BODY_BYTES":                   "4096",
		"CATALOG_HTTP_SERVICE_ACTIVITY_ENABLED":         "true",
		"CATALOG_HTTP_CORS_ORIGINS":                     "https://one.example.net, https://two.example.net",
		"CATALOG_DATABASE_DRIVER":                       "postgres",
		"CATALOG_DATABASE_DSN":                          "postgres://catalog.example.net/catalog",
		"CATALOG_DATABASE_MAX_CONNECTIONS":              "23",
		"CATALOG_DATABASE_MAX_CONN_LIFETIME":            "45m",
		"CATALOG_DATABASE_MAX_CONN_IDLE_TIME":           "3m",
		"CATALOG_DATABASE_HEALTH_CHECK_PERIOD":          "15s",
		"CATALOG_AUTH_MODE":                             "oidc",
		"CATALOG_AUTH_ISSUER":                           "https://identity.example.net",
		"CATALOG_AUTH_TRUST_BUNDLE_FILE":                "/etc/open-agentic-catalog/oidc/trust/ca.pem",
		"CATALOG_AUTH_AUDIENCE":                         "catalog-audience",
		"CATALOG_AUTH_TENANCY_MODE":                     "shared",
		"CATALOG_AUTH_DEDICATED_TENANT_ID":              "00000000-0000-7000-8000-000000000002",
		"CATALOG_AUTH_ORGANIZATION_CLAIM":               "organization_id",
		"CATALOG_AUTH_ENFORCE_SCOPES":                   "true",
		"CATALOG_VALIDATION_MAX_FINDINGS":               "32",
		"CATALOG_VALIDATION_MAX_DIAGNOSTIC_BYTES":       "4096",
		"CATALOG_JOBS_MAX_ATTEMPTS":                     "7",
		"CATALOG_JOBS_RETRY_CEILING":                    "5.5s",
		"CATALOG_WORKER_DEPENDENCY_RETRY_INITIAL_DELAY": "1s",
		"CATALOG_WORKER_DEPENDENCY_RETRY_CEILING":       "30s",
		"CATALOG_WORKER_POLL_INTERVAL":                  "6s",
		"CATALOG_WORKER_LEASE_DURATION":                 "7s",
		"CATALOG_WORKER_CONCURRENCY":                    "8",
		"CATALOG_WORKER_SOURCE_MAINTENANCE_INTERVAL":    "12m",
		"CATALOG_WORKER_HOUSEKEEPING_INTERVAL":          "6m",
		"CATALOG_WORKER_TERMINAL_JOB_RETENTION":         "240h",
		"CATALOG_WORKER_ACKNOWLEDGED_OUTBOX_RETENTION":  "48h",
		"CATALOG_WORKER_OPERATIONS_ADDRESS":             "127.0.0.1:9091",
		"CATALOG_FEDERATION_REQUEST_TIMEOUT":            "4s",
		"CATALOG_FEDERATION_MAX_RESPONSE_BYTES":         "8192",
		"CATALOG_FEDERATION_MAX_REDIRECTS":              "2",
		"CATALOG_FEDERATION_MAX_FANOUT":                 "3",
		"CATALOG_FEDERATION_MAX_LIVE_PAGES_PER_SOURCE":  "3",
		"CATALOG_FEDERATION_MAX_HOPS":                   "2",
		"CATALOG_FEDERATION_POLICY_FILE":                "/etc/open-agentic-catalog/federation/policy.json",
		"CATALOG_WEBHOOKS_POLICY_FILE":                  "/etc/open-agentic-catalog/webhooks/policy.json",
		"CATALOG_DISCOVERY_HOSTS":                       `[{"host":"catalog.example.net","tenantId":"00000000-0000-7000-8000-000000000002","mode":"authenticated","publisherDomain":"publisher.example.net"}]`,
		"CATALOG_PUBLICATION_ARD_PROFILE":               "ard-override",
		"CATALOG_PUBLICATION_AI_CATALOG_PROFILE":        "ai-catalog-override",
		"CATALOG_WEBHOOKS_ENABLED":                      "true",
		"CATALOG_WEBHOOKS_MAX_ATTEMPTS":                 "9",
		"CATALOG_WEBHOOKS_RETRY_CEILING":                "30m",
		"CATALOG_WEBHOOKS_TIMEOUT":                      "10s",
		"CATALOG_WEBHOOKS_POLL_INTERVAL":                "11s",
		"CATALOG_SEMANTIC_SEARCH":                       `{"enabled":true}`,
		"CATALOG_RATINGS_SHARING_ENABLED":               "true",
		"CATALOG_RATINGS_MINIMUM_SHARED_COUNT":          "9",
		"CATALOG_TRUST_VERIFICATION":                    `{ "timeout": "7s" }`,
		"CATALOG_PUBLISHER_DOMAIN_VERIFICATION":         `{"mode":"optional","lookupTimeout":"7s"}`,
		"CATALOG_LISTING_MEDIA":                         `{"limits":{"maxScreenshots":3}}`,
		"CATALOG_MANAGED_MEDIA_ENABLED":                 "true",
		"CATALOG_SOFTWARE_ASSESSMENT_ENABLED":           "true",
		"CATALOG_CREDENTIAL_RESOLUTION_ENABLED":         "true",
		"CATALOG_CREDENTIAL_RESOLUTION_DIRECTORY":       "/etc/open-agentic-catalog/federation/credentials",
	}
	seen := make(map[string]struct{}, len(environmentOverrides))
	for _, override := range environmentOverrides {
		if _, exists := seen[override.name]; exists {
			t.Fatalf("duplicate environment override %s", override.name)
		}
		seen[override.name] = struct{}{}
		if _, exists := overrides[override.name]; !exists {
			t.Errorf("test fixture does not exercise %s", override.name)
		}
	}
	if len(seen)+1 != len(overrides) {
		t.Fatalf("override fixture has unsupported entries: overrides=%d loader=%d", len(overrides), len(seen))
	}

	cfg := Config{}
	if err := applyEnvironment(&cfg, func(name string) (string, bool) {
		value, exists := overrides[name]
		return value, exists
	}); err != nil {
		t.Fatal(err)
	}
	want := Config{
		PublisherDomainVerification: PublisherDomainVerification{Mode: "optional", LookupTimeout: Duration{7 * time.Second}},
		MCP:                         MCP{Enabled: true, MaxToolBytes: 4096, MaxConcurrency: 2, AllowedOrigins: []string{"https://console.example.net"}},
		TrustVerification:           TrustVerification{Timeout: Duration{7 * time.Second}},
		Ratings:                     Ratings{SharingEnabled: true, MinimumSharedCount: 9},
		ListingMedia:                ListingMedia{Limits: ListingMediaLimits{MaxScreenshots: 3}},
		DemoArchives:                DemoArchives{Origin: "https://127.0.0.1:18443", TrustBundleFile: "/local.pem", AllowlistFile: "/allowlist.json"}, LogLevel: "debug", LogFormat: "json", Validation: catalog.DiagnosticLimits{MaxFindings: 32, MaxDiagnosticBytes: 4096},
		Environment: "development",
		Profile:     "environment-profile",
		ServiceName: "environment-service",
		HTTP: HTTP{
			ServiceActivityEnabled: true,
			Address:                "0.0.0.0:8181",
			PublicBaseURL:          "https://catalog.example.net",
			ReadTimeout:            Duration{Duration: 2 * time.Second},
			RequestTimeout:         Duration{Duration: time.Second},
			WriteTimeout:           Duration{Duration: 7 * time.Second},
			IdleTimeout:            Duration{Duration: 4 * time.Second},
			ShutdownTimeout:        Duration{Duration: 7 * time.Second},
			MaxBodyBytes:           4096,
			CORSOrigins:            []string{"https://one.example.net", "https://two.example.net"},
		},
		Database: Database{Driver: "postgres", DSN: "postgres://catalog.example.net/catalog", MaxConnections: 23, MaxConnLifetime: Duration{45 * time.Minute}, MaxConnIdleTime: Duration{3 * time.Minute}, HealthCheckPeriod: Duration{15 * time.Second}},
		Auth:     Auth{Mode: "oidc", Issuer: "https://identity.example.net", TrustBundleFile: "/etc/open-agentic-catalog/oidc/trust/ca.pem", Audience: "catalog-audience", TenancyMode: "shared", DedicatedTenantID: "00000000-0000-7000-8000-000000000002", OrganizationClaim: "organization_id", EnforceScopes: true},
		Jobs:     Jobs{MaxAttempts: 7, RetryCeiling: Duration{5500 * time.Millisecond}},
		Worker: Worker{
			DependencyRetryInitialDelay: Duration{Duration: time.Second},
			DependencyRetryCeiling:      Duration{Duration: 30 * time.Second},
			PollInterval:                Duration{Duration: 6 * time.Second}, LeaseDuration: Duration{Duration: 7 * time.Second}, Concurrency: 8,
			SourceMaintenanceInterval: Duration{Duration: 12 * time.Minute}, HousekeepingInterval: Duration{Duration: 6 * time.Minute},
			TerminalJobRetention: Duration{Duration: 240 * time.Hour}, AcknowledgedOutboxRetention: Duration{Duration: 48 * time.Hour}, OperationsAddress: "127.0.0.1:9091",
		},
		Federation: Federation{
			RequestTimeout:        Duration{Duration: 4 * time.Second},
			MaxResponseBytes:      8192,
			MaxRedirects:          2,
			MaxFanout:             3,
			MaxLivePagesPerSource: 3,
			MaxHops:               2,
			PolicyFile:            "/etc/open-agentic-catalog/federation/policy.json",
		},
		Discovery:            Discovery{Hosts: []DiscoveryHost{{Host: "catalog.example.net", TenantID: "00000000-0000-7000-8000-000000000002", Mode: "authenticated", PublisherDomain: "publisher.example.net"}}},
		Publication:          Publication{ARDProfile: "ard-override", AICatalogProfile: "ai-catalog-override"},
		Tracing:              Tracing{Installation: "environment-fixture"},
		Webhooks:             Webhooks{PolicyFile: "/etc/open-agentic-catalog/webhooks/policy.json", Enabled: true, MaxAttempts: 9, RetryCeiling: Duration{Duration: 30 * time.Minute}, Timeout: Duration{Duration: 10 * time.Second}, PollInterval: Duration{Duration: 11 * time.Second}},
		SemanticSearch:       Semantic{Enabled: true},
		ManagedMedia:         Capability{Enabled: true},
		SoftwareAssessment:   Capability{Enabled: true},
		CredentialResolution: CredentialResolution{Enabled: true, Directory: "/etc/open-agentic-catalog/federation/credentials"},
	}
	if !reflect.DeepEqual(cfg, want) {
		t.Fatalf("environment result = %#v, want %#v", cfg, want)
	}

	allNames := EnvironmentVariables()
	allSeen := make(map[string]struct{}, len(allNames))
	for _, name := range allNames {
		if _, exists := allSeen[name]; exists {
			t.Errorf("EnvironmentVariables contains duplicate %s", name)
		}
		allSeen[name] = struct{}{}
	}
}

func TestLoadRejectsUnreadableUnknownAndInvalidConfiguration(t *testing.T) {
	clearRuntimeEnvironment(t)
	if _, err := LoadFor(filepath.Join(t.TempDir(), "missing.json"), RoleAll); err == nil {
		t.Fatal("missing configuration was accepted")
	}
	for name, raw := range map[string]string{
		"unknown field":                 `{"unknown":true}`,
		"removed publication namespace": `{"publication":{"namespace":"default"}}`,
		"invalid values":                `{"environment":"invalid"}`,
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.json")
			if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := LoadFor(path, RoleAll); err == nil {
				t.Fatal("invalid configuration was accepted")
			}
		})
	}
}

func TestConfigRejectsUnsafeCORSAndProductionShortcuts(t *testing.T) {
	base := validTestConfig()
	base.HTTP.CORSOrigins = []string{"*"}
	if err := base.Validate(); err == nil {
		t.Fatal("wildcard CORS origin was accepted")
	}
	base = validTestConfig()
	base.HTTP.CORSOrigins = []string{"https://console.example.com/path"}
	if err := base.Validate(); err == nil {
		t.Fatal("CORS origin with a path was accepted")
	}
	base = validTestConfig()
	base.HTTP.CORSOrigins = []string{"https://*.example.com"}
	if err := base.Validate(); err == nil {
		t.Fatal("wildcard CORS hostname was accepted")
	}
	base = validTestConfig()
	base.HTTP.CORSOrigins = []string{"https://console.example.com", "https://console.example.com"}
	if err := base.Validate(); err == nil {
		t.Fatal("duplicate CORS origin was accepted")
	}
	base = validTestConfig()
	base.HTTP.CORSOrigins = []string{"http://127.0.0.1:3000", "https://console.example.com"}
	if err := base.Validate(); err != nil {
		t.Fatalf("valid development CORS origins failed: %v", err)
	}
	base.HTTP.PublicBaseURL = "http://[::1]:8080"
	base.HTTP.CORSOrigins = []string{"http://[::1]:3000"}
	if err := base.Validate(); err != nil {
		t.Fatalf("valid IPv6 origins failed: %v", err)
	}
	base.Environment = "production"
	base.Profile = "production"
	base.HTTP.PublicBaseURL = "https://catalog.example.com"
	base.Database = Database{Driver: "postgres", DSN: "postgres://catalog@example.com/catalog", MaxConnLifetime: Defaults().Database.MaxConnLifetime, MaxConnIdleTime: Defaults().Database.MaxConnIdleTime, HealthCheckPeriod: Defaults().Database.HealthCheckPeriod}
	base.Auth = Auth{Mode: "oidc", Issuer: "https://id.example.com", Audience: "catalog", TenancyMode: "dedicated", DedicatedTenantID: "00000000-0000-7000-8000-000000000001"}
	if err := base.ValidateFor(RoleAPI); err == nil {
		t.Fatal("HTTP CORS origin was accepted in production")
	}
}

func TestConfigRejectsMalformedRuntimeBoundaries(t *testing.T) {
	tests := map[string]func(*Config){
		"listen address":   func(cfg *Config) { cfg.HTTP.Address = "localhost" },
		"public URL query": func(cfg *Config) { cfg.HTTP.PublicBaseURL = "https://catalog.example.com?token=one" },
		"public URL wildcard": func(cfg *Config) {
			cfg.HTTP.PublicBaseURL = "https://*.example.com"
		},
		"database pool": func(cfg *Config) { cfg.Database.MaxConnections = 501 },
		"runtime migration": func(cfg *Config) {
			cfg.Database.MigrateOnStart = true
		},
		"OIDC query": func(cfg *Config) {
			cfg.Auth = Auth{Mode: "oidc", Issuer: "https://identity.example.com?tenant=one", Audience: "catalog", TenancyMode: "dedicated", DedicatedTenantID: "00000000-0000-7000-8000-000000000001"}
		},
		"OIDC wildcard": func(cfg *Config) {
			cfg.Auth = Auth{Mode: "oidc", Issuer: "https://*.example.com", Audience: "catalog", TenancyMode: "dedicated", DedicatedTenantID: "00000000-0000-7000-8000-000000000001"}
		},
		"discovery tenant ID": func(cfg *Config) { cfg.Discovery.Hosts[0].TenantID = "tenant-one" },
		"discovery domain":    func(cfg *Config) { cfg.Discovery.Hosts[0].PublisherDomain = "https://catalog.example.com" },
		"discovery host port": func(cfg *Config) { cfg.Discovery.Hosts[0].Host = "catalog.example.com:443" },
		"duplicate discovery host": func(cfg *Config) {
			cfg.Discovery.Hosts = append(cfg.Discovery.Hosts, cfg.Discovery.Hosts[0])
		},
		"worker operations address": func(cfg *Config) { cfg.Worker.OperationsAddress = "catalog.example.com" },
		"development role": func(cfg *Config) {
			cfg.Auth.DevelopmentTokens = map[string]DevelopmentUser{"admin": {Subject: "admin", TenantID: "00000000-0000-7000-8000-000000000001", Roles: []identity.Role{"owner"}}}
		},
		"development namespace ID": func(cfg *Config) {
			cfg.Auth.DevelopmentTokens["admin"] = DevelopmentUser{Subject: "admin", TenantID: "00000000-0000-7000-8000-000000000001", Namespaces: map[string][]identity.Role{"default": {identity.RoleContributor}}}
		},
		"development namespace role": func(cfg *Config) {
			cfg.Auth.DevelopmentTokens["admin"] = DevelopmentUser{Subject: "admin", TenantID: "00000000-0000-7000-8000-000000000001", Namespaces: map[string][]identity.Role{"00000000-0000-7000-8000-000000000010": {"owner"}}}
		},
		"development namespace tenant admin": func(cfg *Config) {
			cfg.Auth.DevelopmentTokens["admin"] = DevelopmentUser{Subject: "admin", TenantID: "00000000-0000-7000-8000-000000000001", Namespaces: map[string][]identity.Role{"00000000-0000-7000-8000-000000000010": {identity.RoleTenantAdmin}}}
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			cfg := validTestConfig()
			mutate(&cfg)
			if err := cfg.Validate(); err == nil {
				t.Fatal("malformed configuration was accepted")
			}
		})
	}
}

func TestWorkerOperationsListenerValidationIsRoleSpecific(t *testing.T) {
	cfg := validTestConfig()
	cfg.HTTP.Address = "0.0.0.0:8080"
	cfg.Worker.OperationsAddress = "0.0.0.0:8080"
	if err := cfg.ValidateFor(RoleAll); err == nil {
		t.Fatal("combined role accepted conflicting API and worker listeners")
	}
	cfg.Worker.OperationsAddress = ":8080"
	if err := cfg.ValidateFor(RoleAll); err == nil {
		t.Fatal("combined role accepted wildcard listener collision")
	}

	cfg = validTestConfig()
	cfg.Worker.OperationsAddress = "127.0.0.1:0"
	if err := cfg.ValidateFor(RoleWorker); err != nil {
		t.Fatalf("development worker rejected an ephemeral operations port: %v", err)
	}
	cfg.Worker.OperationsAddress = ""
	if err := cfg.ValidateFor(RoleWorker); err == nil || !strings.Contains(err.Error(), "worker.operationsAddress is required") {
		t.Fatalf("empty worker listener returned %v", err)
	}
	cfg.Worker.OperationsAddress = "not-an-address"
	if err := cfg.ValidateFor(RoleAPI); err != nil {
		t.Fatalf("API role validated its unused worker listener: %v", err)
	}

	makeProductionConfig(&cfg)
	cfg.Worker.OperationsAddress = "0.0.0.0:0"
	if err := cfg.ValidateFor(RoleWorker); err == nil {
		t.Fatal("production worker accepted an ephemeral operations port")
	}
}

func TestConfigRejectsEveryOperationalBoundary(t *testing.T) {
	tests := map[string]func(*Config){
		"environment":       func(cfg *Config) { cfg.Environment = "staging" },
		"required identity": func(cfg *Config) { cfg.Profile = "" },
		"production port zero": func(cfg *Config) {
			makeProductionConfig(cfg)
			cfg.HTTP.Address = "127.0.0.1:0"
		},
		"production HTTP origin": func(cfg *Config) {
			makeProductionConfig(cfg)
			cfg.HTTP.PublicBaseURL = "http://catalog.example.com"
		},
		"body limit":   func(cfg *Config) { cfg.HTTP.MaxBodyBytes = 512 },
		"HTTP timeout": func(cfg *Config) { cfg.HTTP.ReadTimeout.Duration = 0 },
		"request timeout below minimum": func(cfg *Config) {
			cfg.HTTP.RequestTimeout.Duration = time.Second - time.Nanosecond
		},
		"request timeout above maximum": func(cfg *Config) {
			cfg.HTTP.RequestTimeout.Duration = 20*time.Second + time.Nanosecond
		},
		"write timeout excludes cleanup": func(cfg *Config) {
			cfg.HTTP.WriteTimeout.Duration = cfg.HTTP.RequestTimeout.Duration + commandCleanupAllowance + responseWriteMargin - time.Nanosecond
		},
		"shutdown timeout excludes cleanup": func(cfg *Config) {
			cfg.HTTP.ShutdownTimeout.Duration = cfg.HTTP.RequestTimeout.Duration + commandCleanupAllowance + responseWriteMargin - time.Nanosecond
		},
		"CORS scheme":     func(cfg *Config) { cfg.HTTP.CORSOrigins = []string{"ftp://console.example.com"} },
		"database driver": func(cfg *Config) { cfg.Database.Driver = "sqlite" },
		"PostgreSQL DSN": func(cfg *Config) {
			cfg.Database = Database{Driver: "postgres", MaxConnLifetime: Defaults().Database.MaxConnLifetime, MaxConnIdleTime: Defaults().Database.MaxConnIdleTime, HealthCheckPeriod: Defaults().Database.HealthCheckPeriod}
		},
		"authentication mode": func(cfg *Config) { cfg.Auth.Mode = "none" },
		"OIDC fields":         func(cfg *Config) { cfg.Auth = Auth{Mode: "oidc"} },
		"OIDC issuer": func(cfg *Config) {
			cfg.Auth = Auth{Mode: "oidc", Issuer: "ftp://identity.example.com", Audience: "catalog", TenancyMode: "dedicated", DedicatedTenantID: "00000000-0000-7000-8000-000000000001"}
		},
		"OIDC dedicated tenant": func(cfg *Config) {
			cfg.Auth = Auth{Mode: "oidc", Issuer: "https://identity.example.com", Audience: "catalog", TenancyMode: "dedicated", DedicatedTenantID: "not-a-uuid"}
		},
		"OIDC shared organization": func(cfg *Config) {
			cfg.Auth = Auth{Mode: "oidc", Issuer: "https://identity.example.com", Audience: "catalog", TenancyMode: "shared", OrganizationClaim: "bad claim"}
		},
		"development identities": func(cfg *Config) {
			cfg.Auth.DevelopmentTokens = nil
		},
		"development token": func(cfg *Config) {
			cfg.Auth.DevelopmentTokens = map[string]DevelopmentUser{" ": {Subject: "admin", TenantID: "00000000-0000-7000-8000-000000000001"}}
		},
		"worker budgets": func(cfg *Config) { cfg.Worker.Concurrency = 0 },
		"source maintenance floor": func(cfg *Config) {
			cfg.Worker.SourceMaintenanceInterval = Duration{Duration: time.Minute - time.Nanosecond}
		},
		"source maintenance ceiling": func(cfg *Config) {
			cfg.Worker.SourceMaintenanceInterval = Duration{Duration: 24*time.Hour + time.Nanosecond}
		},
		"housekeeping floor": func(cfg *Config) {
			cfg.Worker.HousekeepingInterval = Duration{Duration: time.Minute - time.Nanosecond}
		},
		"housekeeping ceiling": func(cfg *Config) {
			cfg.Worker.HousekeepingInterval = Duration{Duration: time.Hour + time.Nanosecond}
		},
		"terminal job retention floor": func(cfg *Config) {
			cfg.Worker.TerminalJobRetention = Duration{Duration: 24*time.Hour - time.Nanosecond}
		},
		"terminal job retention ceiling": func(cfg *Config) {
			cfg.Worker.TerminalJobRetention = Duration{Duration: 2160*time.Hour + time.Nanosecond}
		},
		"outbox retention floor": func(cfg *Config) {
			cfg.Worker.AcknowledgedOutboxRetention = Duration{Duration: time.Hour - time.Nanosecond}
		},
		"outbox retention ceiling": func(cfg *Config) {
			cfg.Worker.AcknowledgedOutboxRetention = Duration{Duration: 720*time.Hour + time.Nanosecond}
		},
		"federation budgets": func(cfg *Config) { cfg.Federation.MaxFanout = 0 },
		"federation timeout ceiling": func(cfg *Config) {
			cfg.Federation.RequestTimeout = Duration{Duration: 5*time.Second + time.Nanosecond}
		},
		"federation fanout ceiling":     func(cfg *Config) { cfg.Federation.MaxFanout = 13 },
		"federation live pages floor":   func(cfg *Config) { cfg.Federation.MaxLivePagesPerSource = 0 },
		"federation live pages ceiling": func(cfg *Config) { cfg.Federation.MaxLivePagesPerSource = 6 },
		"federation hop ceiling":        func(cfg *Config) { cfg.Federation.MaxHops = 3 },
		"webhook budgets":               func(cfg *Config) { cfg.Webhooks = Webhooks{Enabled: true} },
		"webhook lease boundary": func(cfg *Config) {
			cfg.Webhooks = Webhooks{Enabled: true, MaxAttempts: 1, Timeout: cfg.Worker.LeaseDuration, PollInterval: Duration{Duration: time.Second}}
		},
		"ARD publication profile": func(cfg *Config) { cfg.Publication.ARDProfile = "unsupported" },
		"AI Catalog publication profile": func(cfg *Config) {
			cfg.Publication.AICatalogProfile = "unsupported"
		},
		"semantic search unavailable":     func(cfg *Config) { cfg.SemanticSearch.Enabled = true },
		"managed media unavailable":       func(cfg *Config) { cfg.ManagedMedia.Enabled = true },
		"software assessment unavailable": func(cfg *Config) { cfg.SoftwareAssessment.Enabled = true },
		"credential resolver directory":   func(cfg *Config) { cfg.CredentialResolution.Enabled = true },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			cfg := validTestConfig()
			mutate(&cfg)
			role := RoleAll
			if strings.HasPrefix(name, "production") || name == "PostgreSQL DSN" {
				role = RoleAPI
			}
			if err := cfg.ValidateFor(role); err == nil {
				t.Fatal("out-of-contract configuration was accepted")
			} else if name == "PostgreSQL DSN" && !strings.Contains(err.Error(), "PostgreSQL DSN is required") {
				t.Fatalf("missing DSN failed an unrelated guard: %v", err)
			}
		})
	}
}

func TestConfigNameValidatorsRejectUnsafeEdges(t *testing.T) {
	for _, value := range []string{"", "127.0.0.1", "Catalog.example.com", "catalog.example.com.", " catalog.example.com", "-catalog.example.com", "catalog-.example.com", "catalog..example.com", strings.Repeat("a", 64) + ".example.com", "catalog_example.com"} {
		if validDNSName(value) {
			t.Errorf("DNS name %q was accepted", value)
		}
	}
	for _, value := range []string{"", "-default", "default-", "Default", "default_namespace", strings.Repeat("a", 65)} {
		if validNamespace(value) {
			t.Errorf("namespace %q was accepted", value)
		}
	}
}

func TestConfigAcceptsDevelopmentNamespaceAssignment(t *testing.T) {
	cfg := validTestConfig()
	cfg.Auth.DevelopmentTokens["admin"] = DevelopmentUser{
		Subject:  "admin",
		TenantID: "00000000-0000-7000-8000-000000000001",
		Namespaces: map[string][]identity.Role{
			"00000000-0000-7000-8000-000000000010": {identity.RoleContributor, identity.RoleReader},
		},
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("valid namespace assignment failed: %v", err)
	}
}

func TestConfigurationReportCannotExposeRuntimeCredentials(t *testing.T) {
	cfg := validTestConfig()
	cfg.Database.DSN = "postgres://catalog:database-secret@example.com/catalog"
	cfg.Auth.DevelopmentTokens = map[string]DevelopmentUser{
		"development-token-secret": {Subject: "admin", TenantID: "00000000-0000-7000-8000-000000000001", Roles: []identity.Role{identity.RoleTenantAdmin}},
	}

	report := cfg.Report()
	if !report.Database.DSNConfigured || report.Auth.DevelopmentIdentityCount != 1 {
		t.Fatalf("configuration report omitted safe state: %#v", report)
	}
	if len(cfg.Auth.DevelopmentTokens) != 1 {
		t.Fatal("report generation mutated the source configuration")
	}
	raw, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range [][]byte{[]byte("database-secret"), []byte("development-token-secret")} {
		if bytes.Contains(raw, secret) {
			t.Fatalf("configuration report contains %q: %s", secret, raw)
		}
	}
}

func TestLivePeerPageConfigurationDefaultsOverridesAndReport(t *testing.T) {
	clearRuntimeEnvironment(t)
	if Defaults().Federation.MaxLivePagesPerSource != 1 {
		t.Fatal("live peer paging is not opt-in")
	}
	const auth = `"auth":{"developmentTokens":{"unit-token":{"subject":"reader","tenantId":"00000000-0000-7000-8000-000000000001"}}},`
	for _, value := range []string{"", "1", "3", "5", "0", "6", `"3"`, "3.5"} {
		t.Run("json "+value, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.json")
			field := `"maxLivePagesPerSource":` + value
			if value == "" {
				field = ""
			}
			if err := os.WriteFile(path, []byte(`{`+auth+`"federation":{`+field+`}}`), 0o600); err != nil {
				t.Fatal(err)
			}
			cfg, err := LoadFor(path, RoleAll)
			valid := value == "" || value == "1" || value == "3" || value == "5"
			if (err == nil) != valid {
				t.Fatalf("value=%s config=%+v error=%v", value, cfg.Federation, err)
			}
			if valid {
				want := map[string]int{"": 1, "1": 1, "3": 3, "5": 5}[value]
				if cfg.Federation.MaxLivePagesPerSource != want || cfg.Report().Federation.MaxLivePagesPerSource != want {
					t.Fatal("configuration/report has the wrong effective live page limit", cfg.Federation)
				}
			}
		})
	}
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{`+auth+`"federation":{"maxLivePagesPerSource":1}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CATALOG_FEDERATION_MAX_LIVE_PAGES_PER_SOURCE", "3")
	cfg, err := LoadFor(path, RoleAll)
	if err != nil || cfg.Federation.MaxLivePagesPerSource != 3 || cfg.Report().Federation.MaxLivePagesPerSource != 3 {
		t.Fatalf("effective page override=%+v error=%v", cfg.Federation, err)
	}
}

func TestOptionalFileAndRoleSpecificValidation(t *testing.T) {
	clearRuntimeEnvironment(t)
	t.Setenv("CATALOG_PROFILE", "environment-only")
	t.Setenv("CATALOG_AUTH_MODE", "oidc")
	t.Setenv("CATALOG_AUTH_ISSUER", "https://identity.example.com")
	t.Setenv("CATALOG_AUTH_AUDIENCE", "catalog")
	t.Setenv("CATALOG_AUTH_TENANCY_MODE", "dedicated")
	t.Setenv("CATALOG_AUTH_DEDICATED_TENANT_ID", "00000000-0000-7000-8000-000000000001")
	t.Setenv("CATALOG_DISCOVERY_HOSTS", `[{"host":"catalog.example.com","tenantId":"00000000-0000-7000-8000-000000000001","mode":"anonymous","publisherDomain":"catalog.example.com"}]`)
	cfg, err := LoadFor("", RoleAPI)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Profile != "environment-only" || cfg.HTTP.Address == "" || cfg.Webhooks.Enabled {
		t.Fatalf("environment-only defaults = %#v", cfg)
	}

	worker := validTestConfig()
	worker.Auth = Auth{}
	worker.HTTP.Address = ""
	worker.HTTP.ReadTimeout = Duration{}
	worker.HTTP.WriteTimeout = Duration{}
	worker.HTTP.IdleTimeout = Duration{}
	if err := worker.ValidateFor(RoleWorker); err != nil {
		t.Fatalf("worker required API-only settings: %v", err)
	}
	api := validTestConfig()
	api.Worker = Worker{SourceMaintenanceInterval: Duration{Duration: 15 * time.Minute}}
	if err := api.ValidateFor(RoleAPI); err != nil {
		t.Fatalf("API required worker-only settings: %v", err)
	}
	migration := Defaults()
	migration.Environment = "production"
	migration.Profile = "migration"
	migration.Database = Database{Driver: "postgres", DSN: "postgres://catalog@example.com/catalog", MaxConnLifetime: Defaults().Database.MaxConnLifetime, MaxConnIdleTime: Defaults().Database.MaxConnIdleTime, HealthCheckPeriod: Defaults().Database.HealthCheckPeriod}
	migration.HTTP = HTTP{}
	migration.Publication = Publication{}
	if err := migration.ValidateFor(RoleMigration); err != nil {
		t.Fatalf("migration required unrelated application settings: %v", err)
	}
	migration.Database.Driver = "memory"
	if err := migration.ValidateFor(RoleMigration); err == nil || !strings.Contains(err.Error(), "migrations require database.driver=postgres") {
		t.Fatalf("memory-backed migration returned %v", err)
	}
}

// Migration skips service policy files and service-specific value bounds, while every
// supplied override still passes the same typed decoding before role validation.
func TestMigrationLoadSkipsServiceFilesButDecodesOverrides(t *testing.T) {
	clearRuntimeEnvironment(t)
	cfg := validTestConfig()
	cfg.Database.Driver = "postgres"
	cfg.Database.DSN = "postgres://owner@example.com/catalog"
	cfg.Federation.PolicyFile = filepath.Join(t.TempDir(), "missing-federation.json")
	cfg.Webhooks.Enabled = true
	cfg.Webhooks.PolicyFile = filepath.Join(t.TempDir(), "missing-webhooks.json")
	raw, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "migration.json")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CATALOG_WORKER_CONCURRENCY", "0")
	loaded, err := LoadFor(path, RoleMigration)
	if err != nil {
		t.Fatalf("migration required service inputs: %v", err)
	}
	if loaded.Worker.Concurrency != 0 || loaded.Database.DSN != cfg.Database.DSN {
		t.Fatal("migration skipped decoding instead of only service validation")
	}
	for _, value := range []string{"not-an-integer", ""} {
		t.Run("invalid override "+value, func(t *testing.T) {
			t.Setenv("CATALOG_WORKER_CONCURRENCY", value)
			if _, err := LoadFor(path, RoleMigration); err == nil || !strings.Contains(err.Error(), "CATALOG_WORKER_CONCURRENCY") {
				t.Fatalf("malformed migration override returned %v", err)
			}
		})
	}
	t.Setenv("CATALOG_LOG_LEVEL", "unsupported")
	if _, err := LoadFor(path, RoleMigration); err == nil || !strings.Contains(err.Error(), "logLevel") {
		t.Fatalf("migration skipped common logging validation: %v", err)
	}
}

func TestLoadRejectsOverflowingJSONNumber(t *testing.T) {
	clearRuntimeEnvironment(t)
	for _, number := range []string{"1048576", "1e1000"} {
		t.Run(number, func(t *testing.T) {
			c := validTestConfig()
			c.HTTP.MaxBodyBytes = 1048576
			// Preserve the valid configuration while changing one numeric token.
			raw := []byte(strings.Replace(string(mustJSON(t, c)), `"maxBodyBytes":1048576`, `"maxBodyBytes":`+number, 1))
			if !json.Valid(raw) {
				t.Fatal("fixture must remain syntactically valid JSON")
			}
			path := filepath.Join(t.TempDir(), "config.json")
			if err := os.WriteFile(path, raw, 0o600); err != nil {
				t.Fatal(err)
			}
			got, err := LoadFor(path, RoleAll)
			if number == "1e1000" {
				var typeError *json.UnmarshalTypeError
				if !errors.As(err, &typeError) || !strings.Contains(err.Error(), "decode config:") || typeError.Value != "number 1e1000" {
					t.Fatalf("overflowing JSON number returned %v", err)
				}
			} else if err != nil || got.HTTP.MaxBodyBytes != 1048576 {
				t.Fatalf("valid numeric configuration: maxBodyBytes=%d, err=%v", got.HTTP.MaxBodyBytes, err)
			}
		})
	}
}

func TestConfigRejectsWhitespaceOnlyRequiredValues(t *testing.T) {
	tests := map[string]struct {
		role   Role
		mutate func(*Config)
	}{
		"PostgreSQL DSN": {
			role: RoleAPI,
			mutate: func(cfg *Config) {
				cfg.Database = Database{Driver: "postgres", DSN: " ", MaxConnLifetime: Defaults().Database.MaxConnLifetime, MaxConnIdleTime: Defaults().Database.MaxConnIdleTime, HealthCheckPeriod: Defaults().Database.HealthCheckPeriod}
			},
		},
		"OIDC audience": {
			role: RoleAPI,
			mutate: func(cfg *Config) {
				cfg.Auth = Auth{Mode: "oidc", Issuer: "https://identity.example.com", Audience: " ", TenancyMode: "dedicated", DedicatedTenantID: "00000000-0000-7000-8000-000000000001"}
			},
		},
		"OIDC organization claim": {
			role: RoleAPI,
			mutate: func(cfg *Config) {
				cfg.Auth = Auth{Mode: "oidc", Issuer: "https://identity.example.com", Audience: "catalog", TenancyMode: "shared", OrganizationClaim: "\t"}
			},
		},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			cfg := validTestConfig()
			test.mutate(&cfg)
			if err := cfg.ValidateFor(test.role); err == nil {
				t.Fatal("whitespace-only required value was accepted")
			}
		})
	}
}

func TestProductionCoreConfigurationDoesNotRequireOptionalProvidersOrWebhookKey(t *testing.T) {
	cfg := validTestConfig()
	makeProductionConfig(&cfg)
	for _, role := range []Role{RoleAPI, RoleWorker} {
		if err := cfg.ValidateFor(role); err != nil {
			t.Fatalf("%s configuration: %v", role, err)
		}
	}
	if cfg.Webhooks.Enabled || cfg.SemanticSearch.Enabled || cfg.ManagedMedia.Enabled || cfg.SoftwareAssessment.Enabled || cfg.CredentialResolution.Enabled {
		t.Fatalf("production core unexpectedly enabled optional capabilities: %#v", cfg)
	}
}

func TestBuiltInDefaultsDoNotSupplyDiscoveryMappings(t *testing.T) {
	cfg := Defaults()
	if len(cfg.Discovery.Hosts) != 0 {
		t.Fatalf("compiled discovery mappings = %#v", cfg.Discovery.Hosts)
	}
	if err := cfg.ValidateFor(RoleWorker); err != nil {
		t.Fatalf("worker defaults unexpectedly required a discovery mapping: %v", err)
	}
}

func TestEnvironmentOverridesRejectAmbiguousValues(t *testing.T) {
	for name, value := range map[string]string{
		"CATALOG_WEBHOOKS_ENABLED":         "1",
		"CATALOG_WORKER_CONCURRENCY":       "many",
		"CATALOG_DATABASE_MAX_CONNECTIONS": "4294967296",
		"CATALOG_HTTP_MAX_BODY_BYTES":      "many",
		"CATALOG_HTTP_READ_TIMEOUT":        "soon",
		"CATALOG_HTTP_CORS_ORIGINS":        "https://one.example.com,",
		"CATALOG_PROFILE":                  "",
	} {
		t.Run(name, func(t *testing.T) {
			clearRuntimeEnvironment(t)
			t.Setenv(name, value)
			if _, err := LoadFor("", RoleWorker); err == nil {
				t.Fatalf("%s=%q was accepted", name, value)
			}
		})
	}
}

func TestDiscoveryHostEnvironmentOverrideRejectsUnsafeDocuments(t *testing.T) {
	for name, value := range map[string]string{
		"oversized":     strings.Repeat("x", (64<<10)+1),
		"duplicate key": `[{"host":"catalog.example.com","host":"other.example.com","tenantId":"00000000-0000-7000-8000-000000000001","mode":"authenticated","publisherDomain":"publisher.example.com"}]`,
		"explicit null": `[null]`,
		"wrong shape":   `{}`,
		"unknown field": `[{"host":"catalog.example.com","tenantId":"00000000-0000-7000-8000-000000000001","mode":"authenticated","publisherDomain":"publisher.example.com","unexpected":true}]`,
	} {
		t.Run(name, func(t *testing.T) {
			clearRuntimeEnvironment(t)
			t.Setenv("CATALOG_DISCOVERY_HOSTS", value)
			if _, err := LoadFor("", RoleWorker); err == nil || !strings.Contains(err.Error(), "CATALOG_DISCOVERY_HOSTS") {
				t.Fatalf("unsafe discovery-host document returned %v", err)
			}
		})
	}
}

func TestSecretFilesAndMutualExclusion(t *testing.T) {
	clearRuntimeEnvironment(t)
	key := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32))
	path := filepath.Join(t.TempDir(), "cursor-key")
	if err := os.WriteFile(path, []byte(key+"\r\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CATALOG_CURSOR_SIGNING_KEY_FILE", path)
	cfg := validTestConfig()
	decoded, err := CursorSigningKey(cfg)
	if err != nil || len(decoded) != 32 {
		t.Fatalf("mounted cursor key = %d bytes, %v", len(decoded), err)
	}
	t.Setenv("CATALOG_CURSOR_SIGNING_KEY", key)
	if _, err := CursorSigningKey(cfg); err == nil {
		t.Fatal("direct and file-backed cursor keys were accepted together")
	}
}

func TestLoadReadsDatabaseDSNFileAndRejectsDirectCollision(t *testing.T) {
	clearRuntimeEnvironment(t)
	dsn := "postgres://catalog.example.com/catalog?sslmode=require"
	dsnPath := filepath.Join(t.TempDir(), "database-dsn")
	if err := os.WriteFile(dsnPath, []byte(dsn+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := validTestConfig()
	cfg.Database = Database{Driver: "postgres", MaxConnections: 4, MaxConnLifetime: Defaults().Database.MaxConnLifetime, MaxConnIdleTime: Defaults().Database.MaxConnIdleTime, HealthCheckPeriod: Defaults().Database.HealthCheckPeriod}
	raw, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(configPath, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CATALOG_DATABASE_DSN_FILE", dsnPath)
	loaded, err := LoadFor(configPath, RoleAPI)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Database.DSN != dsn {
		t.Fatalf("database DSN = %q, want mounted value", loaded.Database.DSN)
	}
	t.Setenv("CATALOG_DATABASE_DSN", dsn)
	if _, err := LoadFor(configPath, RoleAPI); err == nil {
		t.Fatal("direct and file-backed database DSNs were accepted together")
	}
}

func TestRuntimeSecretValidationRejectsMalformedInputsAndSkipsDisabledWebhooks(t *testing.T) {
	key := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{9}, 32))
	for name, setup := range map[string]func(*testing.T){
		"empty direct value": func(t *testing.T) {
			t.Helper()
			t.Setenv("CATALOG_CURSOR_SIGNING_KEY", "")
		},
		"oversized direct value": func(t *testing.T) {
			t.Helper()
			t.Setenv("CATALOG_CURSOR_SIGNING_KEY", string(bytes.Repeat([]byte{'a'}, maxSecretFileBytes+1)))
		},
		"missing file": func(t *testing.T) {
			t.Helper()
			t.Setenv("CATALOG_CURSOR_SIGNING_KEY_FILE", filepath.Join(t.TempDir(), "missing"))
		},
		"multiline file": func(t *testing.T) {
			t.Helper()
			path := filepath.Join(t.TempDir(), "key")
			if err := os.WriteFile(path, []byte(key+"\nsecond-line"), 0o600); err != nil {
				t.Fatal(err)
			}
			t.Setenv("CATALOG_CURSOR_SIGNING_KEY_FILE", path)
		},
		"oversized file": func(t *testing.T) {
			t.Helper()
			path := filepath.Join(t.TempDir(), "key")
			if err := os.WriteFile(path, bytes.Repeat([]byte{'a'}, maxSecretFileBytes+1), 0o600); err != nil {
				t.Fatal(err)
			}
			t.Setenv("CATALOG_CURSOR_SIGNING_KEY_FILE", path)
		},
		"invalid base64": func(t *testing.T) {
			t.Helper()
			t.Setenv("CATALOG_CURSOR_SIGNING_KEY", "not-base64")
		},
		"wrong key length": func(t *testing.T) {
			t.Helper()
			t.Setenv("CATALOG_CURSOR_SIGNING_KEY", base64.StdEncoding.EncodeToString(make([]byte, 31)))
		},
	} {
		t.Run(name, func(t *testing.T) {
			clearRuntimeEnvironment(t)
			setup(t)
			if _, err := CursorSigningKey(validTestConfig()); err == nil {
				t.Fatal("malformed cursor key input was accepted")
			}
		})
	}

	t.Run("disabled webhook key is not read", func(t *testing.T) {
		clearRuntimeEnvironment(t)
		t.Setenv("CATALOG_CURSOR_SIGNING_KEY", key)
		t.Setenv("CATALOG_WEBHOOK_ENCRYPTION_KEY", "invalid-but-disabled")
		cfg := validTestConfig()
		cfg.Environment = "production"
		if err := ValidateRuntimeSecrets(cfg, RoleAPI); err != nil {
			t.Fatalf("disabled webhook key affected validation: %v", err)
		}
		cfg.Webhooks.Enabled = true
		if err := ValidateRuntimeSecrets(cfg, RoleAPI); err == nil {
			t.Fatal("enabled malformed webhook key was accepted")
		}
	})

	t.Run("migration does not require application keys", func(t *testing.T) {
		clearRuntimeEnvironment(t)
		cfg := validTestConfig()
		cfg.Environment = "production"
		cfg.Webhooks.Enabled = true
		if err := ValidateRuntimeSecrets(cfg, RoleMigration); err != nil {
			t.Fatalf("migration required application keys: %v", err)
		}
		if err := ValidateRuntimeSecrets(cfg, Role("invalid")); err == nil {
			t.Fatal("secret validation accepted an invalid role")
		}
	})
}

func validTestConfig() Config {
	cfg := Defaults()
	cfg.Environment = "test"
	cfg.Profile = "test"
	cfg.ServiceName = "catalog"
	cfg.HTTP = HTTP{Address: "127.0.0.1:0", PublicBaseURL: "http://127.0.0.1", ReadTimeout: Duration{Duration: time.Second}, RequestTimeout: Duration{Duration: time.Second}, WriteTimeout: Duration{Duration: 7 * time.Second}, IdleTimeout: Duration{Duration: time.Second}, ShutdownTimeout: Duration{Duration: 7 * time.Second}, MaxBodyBytes: 1 << 20}
	cfg.Database = Database{Driver: "memory", MaxConnLifetime: Defaults().Database.MaxConnLifetime, MaxConnIdleTime: Defaults().Database.MaxConnIdleTime, HealthCheckPeriod: Defaults().Database.HealthCheckPeriod}
	cfg.Auth = Auth{Mode: "development", DevelopmentTokens: map[string]DevelopmentUser{
		"admin": {Subject: "admin", TenantID: "00000000-0000-7000-8000-000000000001"},
	}}
	cfg.Worker = Worker{DependencyRetryInitialDelay: Duration{Duration: time.Second}, DependencyRetryCeiling: Duration{Duration: 30 * time.Second}, PollInterval: Duration{Duration: time.Second}, LeaseDuration: Duration{Duration: time.Minute}, Concurrency: 1, SourceMaintenanceInterval: Duration{Duration: 15 * time.Minute}, HousekeepingInterval: Duration{Duration: 5 * time.Minute}, TerminalJobRetention: Duration{Duration: 168 * time.Hour}, AcknowledgedOutboxRetention: Duration{Duration: 24 * time.Hour}, OperationsAddress: "127.0.0.1:9090"}
	cfg.Federation.RequestTimeout = Duration{Duration: time.Second}
	cfg.Federation.MaxResponseBytes = 1 << 20
	cfg.Federation.MaxRedirects = 1
	cfg.Federation.MaxFanout = 1
	cfg.Federation.MaxHops = 1
	cfg.Discovery = Discovery{Hosts: []DiscoveryHost{{Host: "catalog.example.com", TenantID: "00000000-0000-7000-8000-000000000001", Mode: "anonymous", PublisherDomain: "catalog.example.com"}}}
	cfg.Publication = Publication{ARDProfile: activeARDProfileID, AICatalogProfile: activeAICatalogProfileID}
	return cfg
}

func makeProductionConfig(cfg *Config) {
	cfg.Environment = "production"
	cfg.Profile = "production"
	cfg.HTTP.Address = "0.0.0.0:8080"
	cfg.Worker.OperationsAddress = "0.0.0.0:9090"
	cfg.HTTP.PublicBaseURL = "https://catalog.example.com"
	cfg.Database = Database{Driver: "postgres", DSN: "postgres://catalog@example.com/catalog", MaxConnLifetime: Defaults().Database.MaxConnLifetime, MaxConnIdleTime: Defaults().Database.MaxConnIdleTime, HealthCheckPeriod: Defaults().Database.HealthCheckPeriod}
	cfg.Auth = Auth{Mode: "oidc", Issuer: "https://identity.example.com", Audience: "catalog", TenancyMode: "dedicated", DedicatedTenantID: "00000000-0000-7000-8000-000000000001"}
	cfg.Webhooks = Webhooks{Enabled: false, MaxAttempts: 5, Timeout: Duration{Duration: time.Second}, PollInterval: Duration{Duration: time.Second}}
}

func clearRuntimeEnvironment(t *testing.T) {
	t.Helper()
	for _, name := range EnvironmentVariables() {
		value, exists := os.LookupEnv(name)
		if err := os.Unsetenv(name); err != nil {
			t.Fatal(err)
		}
		savedName, savedValue, savedExists := name, value, exists
		t.Cleanup(func() {
			if savedExists {
				_ = os.Setenv(savedName, savedValue)
			} else {
				_ = os.Unsetenv(savedName)
			}
		})
	}
}

func TestControlConfigurationNamesAreExact(t *testing.T) {
	clearRuntimeEnvironment(t)
	for _, raw := range []string{`{"HTTP":{}}`, `{"http":{"Address":":8080"}}`, `{"http":{"address":":8080","Address":":9090"}}`, `{"http":{"address":"\ud800"}}`} {
		t.Run(raw, func(t *testing.T) {
			file := filepath.Join(t.TempDir(), "config.json")
			if err := os.WriteFile(file, []byte(raw), 0o600); err != nil {
				t.Fatal(err)
			}
			cfg := validTestConfig()
			if err := decodeFile(file, &cfg); err == nil {
				t.Fatal("invalid control accepted")
			}
		})
	}
}

func TestJSONEnvironmentControlFieldsAreExact(t *testing.T) {
	for _, test := range []struct {
		name           string
		apply          func(*Config, string) error
		valid, invalid string
	}{
		{"tracing", applyTracing, `{"queueSize":64}`, `{"QueueSize":64}`},
		{"semantic", applySemantic, `{"enabled":false}`, `{"Enabled":false}`},
		{"discovery", applyDiscoveryHosts, `[]`, `[{"Host":"catalog.example"}]`},
	} {
		t.Run(test.name, func(t *testing.T) {
			cfg := validTestConfig()
			if err := test.apply(&cfg, test.valid); err != nil {
				t.Fatal(err)
			}
			if err := test.apply(&cfg, test.invalid); err == nil {
				t.Fatal("case alias accepted")
			}
			if err := test.apply(&cfg, `null`); err == nil {
				t.Fatal("null accepted")
			}
		})
	}
	cfg := validTestConfig()
	if err := applyTracing(&cfg, `{"queueSize":null}`); err == nil {
		t.Fatal("null erased tracing default")
	}
}

func TestRatingsPrivacyDefaultsAndOperationalBounds(t *testing.T) {
	clearRuntimeEnvironment(t)
	defaults := Defaults()
	if defaults.Ratings.SharingEnabled || defaults.Ratings.MinimumSharedCount != 5 {
		t.Fatal("ratings sharing must default off with a five-person minimum")
	}
	for _, minimum := range []int{0, 4, 5, 1000, 1001} {
		cfg := validTestConfig()
		cfg.Ratings.MinimumSharedCount = minimum
		err := cfg.ValidateFor(RoleAll)
		if (err == nil) != (minimum >= 5 && minimum <= 1000) {
			t.Fatalf("minimum %d: %v", minimum, err)
		}
	}
	for name, value := range map[string]string{"CATALOG_RATINGS_SHARING_ENABLED": "sometimes", "CATALOG_RATINGS_MINIMUM_SHARED_COUNT": "5.5"} {
		t.Run(name, func(t *testing.T) {
			t.Setenv(name, value)
			if _, err := LoadFor("../../../config/local.json", RoleAll); err == nil {
				t.Fatal("invalid rating policy override accepted")
			}
		})
	}
}

func TestTenantSourceIntervalFallbackIsValidatedForBothConsumers(t *testing.T) {
	for _, role := range []Role{RoleAPI, RoleWorker, RoleAll} {
		for _, interval := range []time.Duration{0, time.Minute - time.Nanosecond, time.Minute, 24 * time.Hour, 24*time.Hour + time.Nanosecond} {
			cfg := validTestConfig()
			cfg.Worker.SourceMaintenanceInterval.Duration = interval
			err := cfg.ValidateFor(role)
			valid := interval >= time.Minute && interval <= 24*time.Hour
			if (err == nil) != valid {
				t.Fatalf("role=%s interval=%s error=%v", role, interval, err)
			}
		}
	}
}
